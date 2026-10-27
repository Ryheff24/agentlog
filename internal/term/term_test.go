package term

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEscaper(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"plain text\n\tindented", "plain text\n\tindented"},
		{"héllo → ok", "héllo → ok"},
		{"\x1b]0;owned\x07title", `\x1b]0;owned\x07title`},
		{"\x1b[2J\x1b[Hclear", `\x1b[2J\x1b[Hclear`},
		{"real\rfake", "realfake"},
		{"crlf\r\n", "crlf\n"},
		{"\x00\x08\x7f", `\x00\x08\x7f`},
		{"c1 \u009b31m", `c1 \u009b31m`},
		{" nbsp", " nbsp"},
	} {
		var b bytes.Buffer
		n, err := Escaper{W: &b}.Write([]byte(tt.in))
		if err != nil || n != len(tt.in) {
			t.Errorf("%q: wrote %d, %v; want %d, nil", tt.in, n, err, len(tt.in))
		}
		if b.String() != tt.want {
			t.Errorf("%q: got %q, want %q", tt.in, b.String(), tt.want)
		}
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestEscaperReportsWriteErrors(t *testing.T) {
	if n, err := (Escaper{W: failing{}}).Write([]byte("x")); err == nil || n != 0 {
		t.Fatalf("got %d, %v", n, err)
	}
}

func TestSafeLeavesPipesAndFilesAlone(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b bytes.Buffer
	if w := Safe(&b, f); w != &b {
		t.Fatalf("a file got %T", w)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if got := Safe(&b, w); got != &b {
		t.Fatalf("a pipe got %T", got)
	}
	if tty, err := os.Open("/dev/null"); err == nil {
		// /dev/null is a character device, which is as close to a terminal
		// as a test gets.
		defer tty.Close()
		if _, ok := Safe(&b, tty).(Escaper); !ok {
			t.Fatal("a character device was not escaped")
		}
	}
}
