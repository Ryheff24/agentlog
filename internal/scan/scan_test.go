package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// write lays down a log file and returns a session pointing at it.
func write(t *testing.T, dir, name string, lines ...string) *transcript.Session {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &transcript.Session{ID: name, Files: []string{path}}
}

func matched(res Result) []string {
	var out []string
	for _, m := range res.Matches {
		out = append(out, m.Session.ID)
	}
	return out
}

func TestSessionsFindsMatchesAndReportsWhatItRead(t *testing.T) {
	dir := t.TempDir()
	ss := []*transcript.Session{
		write(t, dir, "a", `{"text":"the PGVector migration"}`),
		write(t, dir, "b", `{"text":"nothing of interest"}`),
		write(t, dir, "c", `{"text":"pgvector again"}`, `{"text":"and again"}`),
	}
	ss[1].Files = append(ss[1].Files, filepath.Join(dir, "gone.jsonl"))

	res := Sessions(t.Context(), ss, regexp.MustCompile(`(?i)pgvector`))
	if got, want := strings.Join(matched(res), " "), "a c"; got != want {
		t.Errorf("matches = %q, want %q", got, want)
	}
	if res.Sessions != 3 || res.Files != 3 || res.Failed != 1 {
		t.Errorf("scanned %d sessions, %d files, %d failed; want 3/3/1", res.Sessions, res.Files, res.Failed)
	}
	if res.Bytes == 0 {
		t.Error("Bytes = 0, want the bytes it read")
	}
}

// The prefilter is allowed to say yes too often — the parse behind it decides
// — but it must never say no when the pattern is there.
func TestSessionsNeverMissesWhatTheRegexpEngineFinds(t *testing.T) {
	dir := t.TempDir()
	body := `{"cmd":"go test ./...","out":"PGvector Ready\nFAILED: 2 of 30\n","path":"/work/shop/src/a.go"}`
	s := write(t, dir, "log", body)
	patterns := []string{
		"(?i)pgvector", "(?i)PGVECTOR", "(?i)ready", "(?i)absent",
		`(?i)failed: \d+`, `(?i)go test`, `(?i)/work/shop`, `(?i)a\.go`,
		"(?i)f.iled", "(?i)^", "(?i)(pg|my)vector",
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			re := regexp.MustCompile(p)
			want := re.MatchString(body)
			got := len(Sessions(t.Context(), []*transcript.Session{s}, re).Matches) == 1
			if got != want {
				t.Errorf("scan = %v, regexp = %v", got, want)
			}
		})
	}
}

// A log line can be larger than the buffer the scan reads through, so a match
// that lands on a chunk boundary is the case most likely to be missed.
func TestSessionsFindsAMatchAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	for _, offset := range []int{-3, -1, 0, 1, chunk / 2} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			pad := chunk + offset - len("pgvector")/2
			s := write(t, dir, fmt.Sprintf("log%d", offset), strings.Repeat("x", pad)+"pgvector"+strings.Repeat("y", 4096))
			if n := len(Sessions(t.Context(), []*transcript.Session{s}, regexp.MustCompile(`(?i)pgvector`)).Matches); n != 1 {
				t.Errorf("matches = %d, want 1 (match at byte %d)", n, pad)
			}
		})
	}
}

func TestScanLiteral(t *testing.T) {
	tests := []struct {
		pattern, want string
	}{
		{"(?i)pgvector", "pgvector"},
		{"(?i)PgVector", "pgvector"},
		// A space in the pattern may be a newline in the log, so only the
		// longest unbreakable run is searched for.
		{"(?i)two words", "words"},
		{"(?i)fix the scroll jump", "scroll"},
		{`(?i)say "hi" now`, "say"},
		{`(?i)a\.go`, ""},           // escaped, so the engine decides
		{"(?i)a.go", ""},            // a metacharacter
		{"(?i)(a|b)", ""},           //
		{"pgvector", ""},            // case-sensitive: folding would widen it
		{"(?i)", ""},                // matches everything, and the engine says so
		{"(?i)naïve", ""},           // what is left of it is too short to filter on
		{"(?i)naïvety now", "vety"}, // the longest ASCII run is enough
	}
	for _, tt := range tests {
		if got := string(scanLiteral(regexp.MustCompile(tt.pattern))); got != tt.want {
			t.Errorf("scanLiteral(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}

func BenchmarkScan(b *testing.B) {
	dir := b.TempDir()
	line := `{"type":"event_msg","payload":{"type":"item_completed","item":{"aggregated_output":"` + strings.Repeat("build output ", 512) + `"}}}`
	var sb strings.Builder
	for sb.Len() < 32<<20 {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	path := filepath.Join(dir, "big.jsonl")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	ss := []*transcript.Session{{ID: "big", Files: []string{path}}}

	// The pattern is absent, so the whole log is read: the honest worst case.
	b.Run("literal", func(b *testing.B) {
		b.SetBytes(int64(sb.Len()))
		re := regexp.MustCompile(`(?i)pgvector`)
		for b.Loop() {
			Sessions(b.Context(), ss, re)
		}
	})
	b.Run("regexp", func(b *testing.B) {
		b.SetBytes(int64(sb.Len()))
		re := regexp.MustCompile(`(?i)pg.ector`)
		for b.Loop() {
			Sessions(b.Context(), ss, re)
		}
	})
}

// A scan must stop when the caller has stopped caring: the reader closed the
// pipe, or the person pressed ^C. Reading the rest of a corpus to write it
// nowhere is the whole of the bug this guards.
func TestSessionsStopsWhenTheContextIsCancelled(t *testing.T) {
	dir := t.TempDir()
	var ss []*transcript.Session
	for i := range 50 {
		ss = append(ss, write(t, dir, fmt.Sprintf("log%d", i), `{"text":"pgvector"}`))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	res := Sessions(ctx, ss, regexp.MustCompile(`(?i)pgvector`))
	if !res.Stopped {
		t.Error("Stopped = false, want the scan to say it gave up")
	}
	if res.Bytes > int64(len(ss))*64 {
		t.Errorf("read %d bytes after cancellation, want almost none", res.Bytes)
	}
}

// The point of the weaker literal: a sentence a person typed is broken by an
// escape in the log, and the prefilter has to let the session through anyway.
func TestSessionsFindsAQuoteBrokenByAnEscape(t *testing.T) {
	dir := t.TempDir()
	typed := "it's broken, fix the scroll\njump today"
	record, err := json.Marshal(map[string]any{"type": "user", "text": typed})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), "scroll jump") {
		t.Fatal("fixture is not escaped, so it proves nothing")
	}
	s := write(t, dir, "log", string(record))

	re := regexp.MustCompile(`(?i)fix the scroll jump today`)
	if n := len(Sessions(t.Context(), []*transcript.Session{s}, re).Matches); n != 1 {
		t.Errorf("matches = %d, want the session whose log holds that sentence, escaped", n)
	}
}

// A path that opens but cannot be read (a directory where a log was
// expected, a file on a disk that went away) was not searched. Counting it as
// scanned would make "not found in N logs" claim more than was looked at.
func TestSessionsCountsAnUnreadableFileAsFailed(t *testing.T) {
	dir := t.TempDir()
	s := write(t, dir, "a", `{"text":"nothing here"}`)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	s.Files = append(s.Files, sub)
	res := Sessions(t.Context(), []*transcript.Session{s}, regexp.MustCompile(`(?i)pgvector`))
	if res.Files != 1 || res.Failed != 1 || res.Unread != 0 {
		t.Errorf("files %d failed %d unread %d, want 1 read, 1 failed, and the session still searched", res.Files, res.Failed, res.Unread)
	}

	// "Searched N sessions" counts sessions: a thread of two pages that both
	// fail is one session unread, not two taken off the total.
	gone := &transcript.Session{ID: "gone", Files: []string{sub, sub}}
	res = Sessions(t.Context(), []*transcript.Session{s, gone}, regexp.MustCompile(`(?i)pgvector`))
	if res.Sessions-res.Unread != 1 || res.Failed != 3 {
		t.Errorf("sessions %d unread %d failed %d, want 1 of 2 searched from 3 failed files", res.Sessions, res.Unread, res.Failed)
	}
}

// Empty logs exist (a session opened and closed at once, a subagent that
// never wrote); a session with no files at all is what a listing hands over
// for a log that has vanished. Neither matches anything, even a pattern that
// matches the empty string.
func TestSessionsWithNothingToRead(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ss := []*transcript.Session{{ID: "empty", Files: []string{empty}}, {ID: "none"}}
	for _, p := range []string{`(?i)pgvector`, `(?i)x*`} {
		res := Sessions(t.Context(), ss, regexp.MustCompile(p))
		if len(res.Matches) != 0 || res.Files != 1 || res.Failed != 0 || res.Bytes != 0 {
			t.Errorf("%s: %+v, want no matches, 1 file read, nothing failed", p, res)
		}
	}
}

// Once one file of a session matches, its other files are not read: one
// match is what earns the session a parse.
func TestSessionsStopsReadingASessionAtItsFirstMatch(t *testing.T) {
	dir := t.TempDir()
	s := write(t, dir, "main", `{"text":"pgvector"}`)
	other := write(t, dir, "sub", strings.Repeat("x", 1<<16))
	s.Files = append(s.Files, other.Files...)
	res := Sessions(t.Context(), []*transcript.Session{s}, regexp.MustCompile(`(?i)pgvector`))
	if len(res.Matches) != 1 || res.Files != 1 || res.Matches[0].Bytes >= 1<<16 {
		t.Errorf("files %d, match %+v; want the first file only", res.Files, res.Matches)
	}
}

// A cancellation that arrives while one 170MB log is being read stops that
// read at the next chunk, rather than after the whole file.
func TestFileStopsBetweenChunks(t *testing.T) {
	dir := t.TempDir()
	s := write(t, dir, "big", strings.Repeat("x", 3*chunk))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m := &matcher{re: regexp.MustCompile(`(?i)x`), buf: make([]byte, chunk)}
	read, matched, err := file(ctx, s.Files[0], m)
	if read != 0 || matched || err != nil {
		t.Errorf("file() = %d, %v, %v; want nothing read", read, matched, err)
	}
}
