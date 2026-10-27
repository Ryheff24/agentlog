// Package flat turns any JSON record into plain text, without knowing
// anything about it.
//
// Two jobs depend on that ignorance. A record whose type this build has never
// heard of still has to be readable, so the reader that has no table entry for
// it falls back to here. And a search has to match what a person actually
// typed: in the log their words are JSON-escaped, so "it's broken\nfix it" is
// stored as `it's broken\nfix it` and no grep for the sentence will find it.
// Flattening unescapes, which is what makes those quotes findable at all.
//
// The rules that keep it safe on a 170MB log: one record at a time, values
// clipped as they are decoded rather than after, and anything that is plainly
// not text — base64, a signature, an encrypted blob — named and skipped rather
// than carried.
package flat

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Line is one leaf of a record: where it is, and what it says.
type Line struct {
	Path  string // dotted path, e.g. payload.item.command
	Value string // decoded, clipped, whitespace-normalised
	Raw   int    // the value's size in the source, before clipping
	Start int    // byte offset of the value in the record
}

// Clipped reports whether the value was cut down.
func (l Line) Clipped() bool { return l.Raw > len(l.Value) }

// Options bound one flattening. The zero value is usable and safe.
type Options struct {
	MaxValue int                    // longest value kept, default valueBytes
	MaxLines int                    // most leaves emitted, default lineCount
	Skip     func(path string) bool // leaves to leave out entirely
}

const (
	// valueBytes is how much of one value survives by default. Long enough
	// for a command, an error or a paragraph of a prompt; short enough that a
	// record full of file contents stays readable.
	valueBytes = 2000
	// lineCount bounds a record with a huge array in it.
	lineCount = 400
	// blobBytes is the length past which an unbroken run of base64-ish
	// characters is called a blob instead of being quoted.
	blobBytes = 256
)

// Text renders a record as "path: value" lines.
func Text(record []byte, o Options) string {
	lines := Lines(record, o)
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Path)
		b.WriteString(": ")
		b.WriteString(l.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// Search renders a record as the text a search should match against: the
// values only, unescaped, one per line. The paths are left out because a
// pattern meant for a person's words should not match a key name.
func Search(record []byte, o Options) string {
	var b strings.Builder
	for _, l := range Lines(record, o) {
		b.WriteString(l.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// Record renders one log record the way a view shows a record nobody can
// read: compact, with the envelope bookkeeping left out, since a reader
// looking at an unrecognised record wants its content, not its uuid.
func Record(raw []byte) string {
	return strings.TrimRight(Text(raw, Options{MaxValue: recordValue, MaxLines: recordLines, Skip: envelope}), "\n")
}

const (
	// recordValue and recordLines keep a stand-in event about the size of a
	// rendered event, not the size of the record behind it.
	recordValue = 400
	recordLines = 40
)

// envelope reports the fields that carry no information for a reader: ids
// that link records to each other, and timestamps the view already shows.
func envelope(path string) bool {
	if path == "type" || path == "subtype" {
		return true // the view names the record; it need not say it twice
	}
	key := path
	if i := strings.LastIndexAny(key, ".["); i >= 0 {
		key = key[i+1:]
	}
	switch strings.ToLower(strings.TrimSuffix(key, "]")) {
	case "uuid", "parentuuid", "logicalparentuuid", "sessionid", "session_id",
		"timestamp", "ordinal", "version", "gitbranch", "cwd", "userid", "user_id",
		"requestid", "request_id", "leafuuid", "thread_id", "turn_id":
		return true
	}
	return false
}

// Lines walks a record and returns its leaves in the order they appear.
// Anything that is not valid JSON stops the walk: what was read before the
// break is still returned, since a truncated record is the normal end of a log
// that is being written to right now.
func Lines(record []byte, o Options) []Line {
	o = o.withDefaults()
	w := &walker{src: record, o: o}
	w.value(w.skipSpace(0), "")
	return w.out
}

func (o Options) withDefaults() Options {
	if o.MaxValue <= 0 {
		o.MaxValue = valueBytes
	}
	if o.MaxLines <= 0 {
		o.MaxLines = lineCount
	}
	return o
}

type walker struct {
	src  []byte
	o    Options
	out  []Line
	path []byte // reused buffer for the current path
}

// full reports whether the line budget is spent, and records that it is.
func (w *walker) full() bool {
	if len(w.out) < w.o.MaxLines {
		return false
	}
	if len(w.out) == w.o.MaxLines {
		w.out = append(w.out, Line{Path: "…", Value: "(record has more fields than this view shows)"})
	}
	return true
}

// value walks the value starting at i and returns the offset just past it.
// A negative return means the record ended or did not parse.
func (w *walker) value(i int, path string) int {
	if i < 0 || i >= len(w.src) {
		return -1
	}
	switch c := w.src[i]; {
	case c == '{':
		return w.object(i, path)
	case c == '[':
		return w.array(i, path)
	case c == '"':
		end := stringEnd(w.src, i)
		if end < 0 {
			return -1
		}
		w.emit(path, w.src[i+1:end-1], i, true)
		return end
	default:
		end := scalarEnd(w.src, i)
		if end <= i {
			return -1
		}
		w.emit(path, w.src[i:end], i, false)
		return end
	}
}

func (w *walker) object(i int, path string) int {
	i = w.skipSpace(i + 1)
	if i < len(w.src) && w.src[i] == '}' {
		w.emit(path, nil, i, false) // an empty object is a fact worth keeping
		return i + 1
	}
	for i >= 0 && i < len(w.src) {
		if w.src[i] != '"' {
			return -1
		}
		keyEnd := stringEnd(w.src, i)
		if keyEnd < 0 {
			return -1
		}
		key := string(w.src[i+1 : keyEnd-1])
		i = w.skipSpace(keyEnd)
		if i >= len(w.src) || w.src[i] != ':' {
			return -1
		}
		i = w.value(w.skipSpace(i+1), join(path, key))
		if i < 0 {
			return -1
		}
		i = w.skipSpace(i)
		if i >= len(w.src) {
			return -1
		}
		switch w.src[i] {
		case ',':
			i = w.skipSpace(i + 1)
		case '}':
			return i + 1
		default:
			return -1
		}
	}
	return -1
}

func (w *walker) array(i int, path string) int {
	i = w.skipSpace(i + 1)
	if i < len(w.src) && w.src[i] == ']' {
		w.emit(path, nil, i, false)
		return i + 1
	}
	for n := 0; i >= 0 && i < len(w.src); n++ {
		i = w.value(i, path+"["+strconv.Itoa(n)+"]")
		if i < 0 {
			return -1
		}
		i = w.skipSpace(i)
		if i >= len(w.src) {
			return -1
		}
		switch w.src[i] {
		case ',':
			i = w.skipSpace(i + 1)
		case ']':
			return i + 1
		default:
			return -1
		}
	}
	return -1
}

// emit records one leaf, deciding on the way whether it is text at all.
func (w *walker) emit(path string, raw []byte, at int, quoted bool) {
	if path == "" {
		path = "."
	}
	if w.o.Skip != nil && w.o.Skip(path) {
		return
	}
	if w.full() {
		return
	}
	line := Line{Path: path, Raw: len(raw), Start: at}
	switch {
	case len(raw) == 0:
		line.Value = "(empty)"
	case quoted:
		line.Value = w.text(raw, path)
	default:
		line.Value = string(raw)
	}
	w.out = append(w.out, line)
}

// text decodes a JSON string, clipping as it goes so a 40MB tool result is
// never assembled in order to throw most of it away.
func (w *walker) text(raw []byte, path string) string {
	if blob := blobName(raw, path); blob != "" {
		return blob
	}
	var b strings.Builder
	b.Grow(min(len(raw), w.o.MaxValue) + 16)
	space := false // the previous byte written was whitespace
	i := 0
	for i < len(raw) && b.Len() < w.o.MaxValue {
		c := raw[i]
		if c != '\\' {
			i++
			if c == '\n' || c == '\r' || c == '\t' || c == ' ' {
				// Newlines are what make a quote ungreppable in the raw log;
				// collapsing them is what makes one line of it matchable.
				if !space && b.Len() > 0 {
					b.WriteByte(' ')
					space = true
				}
				continue
			}
			b.WriteByte(c)
			space = false
			continue
		}
		r, size := unescape(raw[i:])
		i += size
		switch {
		case r < 0:
			i = len(raw) // a broken escape ends the value
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
				space = true
			}
		default:
			b.WriteRune(r)
			space = false
		}
	}
	out := b.String()
	if i < len(raw) {
		out = clipRunes(out, w.o.MaxValue)
	}
	// A log is bytes, not promises: a harness can write a stray byte, and a
	// view or a search index should get a replacement mark, not the byte.
	return strings.TrimSpace(strings.ToValidUTF8(out, "\uFFFD"))
}

// clipRunes cuts s to at most n bytes without splitting a character. The
// clip is made byte by byte as the value is decoded, so it can land inside a
// multi-byte character, and a decoded escape can overshoot n by a few bytes.
func clipRunes(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	for j := len(s) - 1; j >= 0 && j >= len(s)-utf8.UTFMax; j-- {
		if utf8.RuneStart(s[j]) {
			if !utf8.FullRuneInString(s[j:]) {
				s = s[:j]
			}
			break
		}
	}
	return s
}

// unescape decodes one backslash escape and returns the rune and how many
// source bytes it took. A rune of -1 means the escape was malformed.
func unescape(s []byte) (rune, int) {
	if len(s) < 2 {
		return -1, len(s)
	}
	switch s[1] {
	case 'n':
		return '\n', 2
	case 'r':
		return '\r', 2
	case 't':
		return '\t', 2
	case 'b', 'f':
		return ' ', 2
	case '"', '\\', '/':
		return rune(s[1]), 2
	case 'u':
		if len(s) < 6 {
			return -1, len(s)
		}
		n, err := strconv.ParseUint(string(s[2:6]), 16, 32)
		if err != nil {
			return -1, 6
		}
		r := rune(n)
		// A surrogate pair is two escapes; joined here, since half of one is
		// not a character and would render as a replacement box.
		if utf16High(r) && len(s) >= 12 && s[6] == '\\' && s[7] == 'u' {
			if lo, err := strconv.ParseUint(string(s[8:12]), 16, 32); err == nil && utf16Low(rune(lo)) {
				return 0x10000 + (r-0xD800)<<10 + (rune(lo) - 0xDC00), 12
			}
		}
		if !utf8.ValidRune(r) {
			return utf8.RuneError, 6
		}
		return r, 6
	}
	return -1, 2
}

func utf16High(r rune) bool { return r >= 0xD800 && r <= 0xDBFF }
func utf16Low(r rune) bool  { return r >= 0xDC00 && r <= 0xDFFF }

// blobName names a value that is not prose — a signature, an encrypted
// payload, an inlined image — instead of quoting it. Carrying a megabyte of
// base64 into a view or a search index helps nobody and costs everybody.
func blobName(raw []byte, path string) string {
	if name := secretKey(path); name != "" {
		return "<" + name + ", " + size(len(raw)) + " not shown>"
	}
	if len(raw) < blobBytes || !base64ish(raw) {
		return ""
	}
	return "<binary or encoded data, " + size(len(raw)) + " not shown>"
}

// secretKey names the fields whose contents are never worth reading and
// sometimes dangerous to print.
func secretKey(path string) string {
	key := path
	if i := strings.LastIndexAny(key, ".["); i >= 0 {
		key = key[i+1:]
	}
	key = strings.ToLower(strings.TrimSuffix(key, "]"))
	switch {
	case strings.Contains(key, "signature"):
		return "signature"
	case strings.Contains(key, "encrypted"):
		return "encrypted content"
	case strings.Contains(key, "base64"), strings.Contains(key, "image_data"):
		return "encoded data"
	}
	return ""
}

// base64ish reports whether a run of bytes is plausibly encoded rather than
// written. It only has to be right about long values.
func base64ish(raw []byte) bool {
	for _, c := range raw {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '+', c == '/', c == '=', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + "MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + "KB"
	default:
		return strconv.Itoa(n) + "B"
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func (w *walker) skipSpace(i int) int {
	for i >= 0 && i < len(w.src) {
		switch w.src[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// stringEnd returns the offset just past the closing quote of the string that
// starts at i, or -1 if it never closes.
func stringEnd(src []byte, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return -1
}

// scalarEnd returns the offset just past a number, true, false or null.
func scalarEnd(src []byte, i int) int {
	for j := i; j < len(src); j++ {
		switch src[j] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return j
		}
	}
	return len(src)
}
