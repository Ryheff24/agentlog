package jsonl

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts content in a fresh log file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// seen collects what a reader hands its callback as "n:line" strings.
func seen(read func(fn func([]byte, int) error) error) ([]string, error) {
	var got []string
	err := read(func(line []byte, n int) error {
		got = append(got, fmt.Sprintf("%d:%s", n, line))
		return nil
	})
	return got, err
}

// Line numbers are what a person types back ("show me #12"), so they must
// count the file's physical lines: blank and whitespace-only lines are not
// handed to the callback but still take a number, and a CRLF log (written on
// Windows, or by a tool that forgot) reads the same as an LF one.
func TestEachNumbersPhysicalLines(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"plain", "{\"a\":1}\n{\"b\":2}\n", []string{`1:{"a":1}`, `2:{"b":2}`}},
		{"blank lines keep their numbers", "{\"a\":1}\n\n  \n{\"b\":2}\n", []string{`1:{"a":1}`, `4:{"b":2}`}},
		{"crlf", "{\"a\":1}\r\n{\"b\":2}\r\n", []string{`1:{"a":1}`, `2:{"b":2}`}},
		// The harness is writing the last record right now: it is handed
		// over as it is, and the caller's decoder rejects it.
		{"final line being written", "{\"a\":1}\n{\"b\":", []string{`1:{"a":1}`, `2:{"b":`}},
		{"empty file", "", nil},
		{"only newlines", "\n\n\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, tt.src)
			got, err := seen(func(fn func([]byte, int) error) error { return Each(path, fn) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("Each() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A single record can be tens of megabytes (a tool result with a file in
// it), far past any buffer size. It must come through whole, and the line
// after it must keep its number.
func TestEachReadsLinesPastTheBuffer(t *testing.T) {
	big := `{"out":"` + strings.Repeat("x", 3<<20) + `"}`
	path := write(t, big+"\n{\"next\":1}\n")
	var sizes []int
	var nums []int
	if err := Each(path, func(line []byte, n int) error {
		sizes = append(sizes, len(line))
		nums = append(nums, n)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 2 || sizes[0] != len(big) || nums[1] != 2 {
		t.Errorf("sizes %v numbers %v, want [%d 11] [1 2]", sizes, nums, len(big))
	}
}

// A log can be deleted (a session cleaned up, a worktree removed) between
// the listing that found it and the read. The caller has to be able to tell
// that from a read that failed some other way.
func TestReadersReportAVanishedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.jsonl")
	nop := func([]byte, int) error { return nil }
	for name, err := range map[string]error{
		"Each": Each(path, nop),
		"Head": Head(path, 100, nop),
		"Tail": Tail(path, 100, nop),
	} {
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s() = %v, want a not-exist error", name, err)
		}
	}
}

// A callback ends a read early with ErrStop (found what it wanted), even
// wrapped; any other error is the caller's and comes back unchanged.
func TestCallbackErrors(t *testing.T) {
	path := write(t, "1\n2\n3\n")
	calls := 0
	stop := func(_ []byte, n int) error {
		calls++
		if n == 2 {
			return fmt.Errorf("done looking: %w", ErrStop)
		}
		return nil
	}
	if err := Each(path, stop); err != nil || calls != 2 {
		t.Errorf("Each() with ErrStop = %v after %d calls, want nil after 2", err, calls)
	}
	boom := errors.New("boom")
	if err := Each(path, func([]byte, int) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("Each() = %v, want the callback's error", err)
	}
}

// A directory is not a log. Opening one succeeds on unix, so the failure
// comes from the read and must surface rather than read as an empty log.
func TestReadingADirectoryIsAnError(t *testing.T) {
	dir := t.TempDir()
	nop := func([]byte, int) error { return nil }
	if err := Each(dir, nop); err == nil {
		t.Error("Each(dir) = nil, want an error")
	}
	if err := Tail(dir, 1, nop); err == nil {
		t.Error("Tail(dir) = nil, want an error")
	}
}

// Head is the cheap look at a log's start used to discover what a session
// is and to preview it. The limit is in bytes, so it can cut a line in two.
// The cut half is not a record: handed over, every windowed read of a log
// bigger than the window reported one record it could not decode. A last
// line with no newline where the file really ends (being written) is still
// handed over, as Each does.
func TestHeadStopsAtTheByteLimit(t *testing.T) {
	full := write(t, "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n") // 8 bytes a line
	writing := write(t, "{\"a\":1}\n{\"b\":")
	tests := []struct {
		name string
		path string
		max  int64
		want []string
	}{
		{"window ends on a newline", full, 8, []string{`1:{"a":1}`}},
		{"window ends before a newline", full, 7, nil},
		{"window cuts the second line", full, 12, []string{`1:{"a":1}`}},
		{"window one byte short of the end", full, 23, []string{`1:{"a":1}`, `2:{"b":2}`}},
		{"window exactly the file", full, 24, []string{`1:{"a":1}`, `2:{"b":2}`, `3:{"c":3}`}},
		{"zero window", full, 0, nil},
		{"window past the end", full, 1 << 20, []string{`1:{"a":1}`, `2:{"b":2}`, `3:{"c":3}`}},
		{"file ends mid-line inside the window", writing, 100, []string{`1:{"a":1}`, `2:{"b":`}},
		{"file ends mid-line at the window", writing, 13, []string{`1:{"a":1}`, `2:{"b":`}},
		{"window cuts a line still being written", writing, 11, []string{`1:{"a":1}`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := seen(func(fn func([]byte, int) error) error { return Head(tt.path, tt.max, fn) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("Head(%d) = %q, want %q", tt.max, got, tt.want)
			}
		})
	}
}

// Tail is how a preview reads the end of a 170MB log without the rest. It
// must hand over only whole lines: the window's first line is usually cut and
// is dropped, but a window that opens exactly on a line boundary has no cut
// line and must not lose the whole one it starts with.
func TestTailHandsOverWholeLines(t *testing.T) {
	src := "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n" // 8 bytes a line
	path := write(t, src)
	tests := []struct {
		name string
		max  int64
		want []string
	}{
		{"window on a line boundary", 8, []string{`0:{"c":3}`}},
		{"window on two boundaries", 16, []string{`0:{"b":2}`, `0:{"c":3}`}},
		{"window mid-line", 12, []string{`0:{"c":3}`}},
		{"window one byte into the previous line", 9, []string{`0:{"c":3}`}},
		{"window larger than the file", 1 << 20, []string{`0:{"a":1}`, `0:{"b":2}`, `0:{"c":3}`}},
		{"window exactly the file", int64(len(src)), []string{`0:{"a":1}`, `0:{"b":2}`, `0:{"c":3}`}},
		{"window of the last newline only", 1, nil},
		{"zero window", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := seen(func(fn func([]byte, int) error) error { return Tail(path, tt.max, fn) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("Tail(%d) = %q, want %q", tt.max, got, tt.want)
			}
		})
	}
}

// The window can land inside one record bigger than itself (a huge tool
// result at the end of a session). There is no whole line to show, which is
// not an error. A final line still being written is handed over like Each
// does.
func TestTailInsideOneGiantLine(t *testing.T) {
	path := write(t, "{\"a\":1}\n{\"out\":\""+strings.Repeat("x", 1000)+"\"}\n")
	got, err := seen(func(fn func([]byte, int) error) error { return Tail(path, 100, fn) })
	if err != nil || len(got) != 0 {
		t.Errorf("Tail() = %q, %v, want nothing and no error", got, err)
	}
	// The same giant record, still being written: no newline anywhere in
	// the window.
	path = write(t, "{\"a\":1}\n{\"out\":\""+strings.Repeat("x", 1000))
	got, err = seen(func(fn func([]byte, int) error) error { return Tail(path, 100, fn) })
	if err != nil || len(got) != 0 {
		t.Errorf("Tail() = %q, %v, want nothing and no error", got, err)
	}
	path = write(t, "{\"a\":1}\n{\"b\":2}\n{\"c\":")
	got, err = seen(func(fn func([]byte, int) error) error { return Tail(path, 13, fn) }) // 8+5 bytes: line b, then the cut one
	if err != nil || strings.Join(got, " ") != `0:{"b":2} 0:{"c":` {
		t.Errorf("Tail() = %q, %v, want the last whole line and the one being written", got, err)
	}
}
