// Package scan finds which logs contain a pattern by reading their bytes,
// without decoding any JSON. It is the filter in front of parsing: a search
// over a whole corpus touches gigabytes, and only the few logs whose raw bytes
// match are worth the cost of turning into events.
package scan

import (
	"bytes"
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"

	"agentlog/internal/monitor"
	"agentlog/internal/transcript"
	"agentlog/internal/work"
)

// Match is one session whose log contains the pattern.
type Match struct {
	Session *transcript.Session
	Bytes   int64 // bytes read before the match was found
}

// Result is what a scan looked at, so a caller can report an honest negative:
// how much was searched, not only what was found.
type Result struct {
	Matches  []Match
	Sessions int   // sessions scanned
	Files    int   // log files read
	Bytes    int64 // bytes read
	Failed   int   // files that could not be read
	Unread   int   // sessions none of whose files could be read
	Stopped  bool  // the caller gave up before the end
}

const (
	// chunk is how much of a log is held at once. A 170MB log costs 170MB of
	// reads and one of these of memory.
	chunk = 1 << 20
	// overlap is how much of each chunk is carried into the next, so a match
	// straddling the split is still found. A pattern needing more than this to
	// match is out of reach, which no grep pattern is.
	overlap = 1 << 16
	// minLiteral is the shortest run worth scanning for. Below this a literal
	// matches nearly every log, and the prefilter costs more than it saves.
	minLiteral = 3
)

// Sessions scans every file of every session and returns the ones that
// matched, in the order the sessions were given. It reads a few files at a
// time, not as many as the machine has cores: this runs on someone's laptop
// beside their own work, and a search that finishes half a second sooner is
// not worth the fans.
func Sessions(ctx context.Context, ss []*transcript.Session, re *regexp.Regexp) Result {
	defer monitor.Phase("scan")()
	res := Result{Sessions: len(ss)}
	found := make([]Match, len(ss))
	var mu sync.Mutex

	lit := scanLiteral(re)
	work.Do(ctx, len(ss), func(i int) {
		if ctx.Err() != nil {
			return
		}
		m := &matcher{re: re, lit: lit, buf: make([]byte, chunk)}
		if lit != nil {
			m.fold = make([]byte, chunk)
		}
		var (
			hit           bool
			read          int64
			files, failed int
		)
		for _, path := range ss[i].Files {
			n, matched, err := file(ctx, path, m)
			read += n
			switch {
			case err != nil:
				failed++
			default:
				files++
			}
			if matched {
				hit = true
				break // one match is enough to earn a parse
			}
		}
		mu.Lock()
		res.Files += files
		res.Failed += failed
		if files == 0 && failed > 0 {
			res.Unread++
		}
		res.Bytes += read
		mu.Unlock()
		if hit {
			found[i] = Match{Session: ss[i], Bytes: read}
		}
	})
	if ctx.Err() != nil {
		res.Stopped = true
	}

	for _, m := range found {
		if m.Session != nil {
			res.Matches = append(res.Matches, m)
		}
	}
	return res
}

// matcher tests chunks of a log against the pattern. A plain case-insensitive
// literal — what an agent's grep almost always is — is answered by folding the
// chunk and searching it, which runs an order of magnitude faster than the
// regexp engine over the same bytes.
//
// The literal it searches for is deliberately weaker than the pattern: see
// scanLiteral. A prefilter may say yes too often, and must never say no
// wrongly, because the parse behind it is what decides.
type matcher struct {
	re   *regexp.Regexp
	lit  []byte // lowercased literal, nil when the pattern needs the engine
	buf  []byte // the chunk being read
	fold []byte // the chunk, lowercased
}

func (m *matcher) match(b []byte) bool {
	if m.lit == nil {
		return m.re.Match(b)
	}
	return bytes.Contains(lower(m.fold[:len(b)], b), m.lit)
}

// scanLiteral returns the lowercased text to look for in the raw bytes on
// behalf of a case-insensitive ASCII literal pattern, and nil when the pattern
// needs the regexp engine.
//
// It is the longest run of the pattern that JSON cannot have written
// differently. A person's words are stored escaped: the sentence "scroll
// jump" may be `scroll\njump` in the file, and `say "hi"` is `say \"hi\"`, so
// searching the raw bytes for the pattern itself finds neither. Whatever the
// escaping did, it did at a space, a quote, a backslash or a control
// character, so the longest run without one of those is still there to be
// found. The full pattern is applied afterwards to the decoded events.
func scanLiteral(re *regexp.Regexp) []byte {
	s, ok := strings.CutPrefix(re.String(), "(?i)")
	if !ok || s == "" || strings.ContainsAny(s, `\.+*?()|[]{}^$`) {
		return nil
	}
	best := ""
	for _, run := range strings.FieldsFunc(s, escapable) {
		if len(run) > len(best) {
			best = run
		}
	}
	if len(best) < minLiteral {
		// Too short to filter with: it would match nearly every log and turn
		// the prefilter into a full parse of the corpus. The engine, reading
		// raw bytes, is the better bet for these.
		return nil
	}
	return []byte(strings.ToLower(best))
}

// escapable reports whether a character may be written differently in JSON
// than it was typed. Non-ASCII counts: a writer is free to emit \uXXXX.
func escapable(r rune) bool {
	return r <= ' ' || r == '"' || r == '\\' || r >= 0x80
}

// lower writes the ASCII-lowercased form of src into dst, which must be the
// same length, and returns it.
func lower(dst, src []byte) []byte {
	for i, c := range src {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		dst[i] = c
	}
	return dst
}

// file reports whether one log matches, reading it in chunks and stopping at
// the first hit. Matching whole chunks rather than lines is what keeps the
// per-byte cost down: the matcher is entered a few hundred times for a large
// log instead of once per record.
func file(ctx context.Context, path string, m *matcher) (read int64, matched bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()

	keep := 0 // bytes carried over from the previous chunk
	for {
		if ctx.Err() != nil {
			return read, false, nil
		}
		n, err := io.ReadFull(f, m.buf[keep:])
		read += int64(n)
		end := keep + n
		if end > 0 && m.match(m.buf[:end]) {
			return read, true, nil
		}
		switch {
		case err == io.EOF, err == io.ErrUnexpectedEOF:
			return read, false, nil // the end of the log
		case err != nil:
			// Not the end but a failed read: the log was not searched, and
			// saying it was would make a negative dishonest.
			return read, false, err
		}
		keep = copy(m.buf, m.buf[end-overlap:end])
	}
}
