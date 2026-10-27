// Package term keeps log text from driving a person's terminal. Session logs
// hold whatever a tool printed or a web page said, and an escape sequence in
// there can rewrite the screen, set the window title or, in some terminals,
// write the clipboard. An agent reading through a pipe gets the bytes as they
// are; a person at a terminal gets them made visible.
package term

import (
	"fmt"
	"io"
	"os"
)

// Safe returns w as it is unless f is a terminal, in which case control
// characters written to w are shown as \xNN instead of acted on.
func Safe(w io.Writer, f *os.File) io.Writer {
	if !IsTerminal(f) {
		return w
	}
	return Escaper{W: w}
}

// IsTerminal reports whether f is a character device, which for stdout and
// stderr means a terminal.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Escaper shows control characters as text. Newlines and tabs pass through;
// carriage returns are dropped, since alone they move back over a line and
// hide what was printed. C1 controls, which some terminals act on when they
// arrive UTF-8 encoded, are shown as \u00NN.
type Escaper struct{ W io.Writer }

func (e Escaper) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\n' || c == '\t':
			out = append(out, c)
		case c == '\r':
		case c < 0x20 || c == 0x7f:
			out = fmt.Appendf(out, `\x%02x`, c)
		case c == 0xc2 && i+1 < len(p) && p[i+1] >= 0x80 && p[i+1] <= 0x9f:
			out = fmt.Appendf(out, `\u%04x`, p[i+1])
			i++
		default:
			out = append(out, c)
		}
	}
	if _, err := e.W.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
