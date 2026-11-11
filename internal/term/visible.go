package term

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Visible returns s with every control character turned into text that names
// it, so logged bytes reach a reader as something it can read rather than as
// bytes it has to guess at. Ordinary text, any language, newlines and tabs,
// comes back unchanged.
//
//   - C0 controls and DEL become their Unicode control pictures: ESC is ␛,
//     NUL is ␀, BEL is ␇, DEL is ␡. One character each, and not hex.
//   - CR before LF is a line ending and is dropped; a lone CR becomes ␍.
//   - C1 controls (U+0080 to U+009F), which have no pictures, become \u0085.
//   - A byte that is not UTF-8 becomes U+FFFD, as a JSON decoder would make it.
func Visible(s string) string {
	if !needsWork(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r' && i+1 < len(s) && s[i+1] == '\n':
		case r < 0x20:
			b.WriteRune(0x2400 + r)
		case r == 0x7f:
			b.WriteRune('␡')
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// needsWork reports whether Visible would change s.
func needsWork(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\n' && c != '\t' || c == 0x7f || c == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f {
			return true
		}
	}
	return !utf8.ValidString(s)
}

// Counts says how much of a text is not text.
type Counts struct {
	Bytes, Runes int
	// Control counts C0 controls other than newline, tab, CR and ESC, plus
	// DEL and C1. CR and ESC are left out because line endings and terminal
	// colours are common in ordinary output.
	Control int
	// Replaced counts U+FFFD and bytes that are not UTF-8: what a reader
	// leaves where it could not decode binary.
	Replaced int
	// Opaque counts what binary is made of and text is not: Replaced, NUL
	// and the C0 controls other than newline, tab, CR, ESC, backspace, form
	// feed and vertical tab, plus DEL. Kinds is how many different
	// characters those were, U+FFFD and undecodable bytes counting as one.
	// C1 is left out: in real logs it is mostly UTF-8 text decoded as
	// Latin-1, which is still text.
	Opaque, Kinds int
}

// Count measures s.
func Count(s string) Counts {
	c := Counts{Bytes: len(s)}
	var seen [0x81]bool // C0 and DEL by value; 0x80 stands for U+FFFD
	opaque := func(k rune) {
		c.Opaque++
		if !seen[k] {
			seen[k] = true
			c.Kinds++
		}
	}
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		c.Runes++
		switch {
		case r == utf8.RuneError:
			c.Replaced++
			opaque(0x80)
		case r < 0x20 && r != '\n' && r != '\t' && r != '\r' && r != 0x1b, r == 0x7f:
			c.Control++
			if r != '\b' && r != '\f' && r != '\v' {
				opaque(r)
			}
		case r >= 0x80 && r <= 0x9f:
			c.Control++
		}
		i += size
	}
	return c
}

// Binary reports whether a text is mostly bytes no reader can use: at least
// 16 opaque characters of at least 3 kinds, making up at least 2% of it.
// Across one machine's logs the texts that pass are binary read as text,
// with tens to thousands of them in many kinds. Text with a stray few, a
// NUL-separated list, backspaced or form-fed output, Latin-1 text decoded
// as UTF-8 (all U+FFFD) and UTF-8 decoded as Latin-1 (all C1) stay below.
func (c Counts) Binary() bool {
	return c.Opaque >= 16 && c.Opaque*50 >= c.Runes && c.Kinds >= 3
}

// StandIn is the one line printed in place of a binary text: its size, what
// made it binary, and the command that prints it as it was logged.
func StandIn(c Counts, get string) string {
	return fmt.Sprintf("[binary, not shown: %s, %d control and %d U+FFFD characters; %s prints it as logged]",
		size(c.Bytes), c.Control, c.Replaced, get)
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// Shown is a writer that passes everything through Visible. A CR or a
// character split across two writes is held until the next write, so call
// Flush when done.
type Shown struct {
	W     io.Writer
	carry []byte
}

func (s *Shown) Write(p []byte) (int, error) {
	buf := append(s.carry, p...)
	cut := len(buf)
	if cut > 0 && buf[cut-1] == '\r' {
		cut--
	} else {
		for j := cut - 1; j >= 0 && j >= cut-utf8.UTFMax; j-- {
			if utf8.RuneStart(buf[j]) {
				if !utf8.FullRune(buf[j:]) {
					cut = j
				}
				break
			}
		}
	}
	out := Visible(string(buf[:cut]))
	s.carry = append([]byte(nil), buf[cut:]...)
	if _, err := io.WriteString(s.W, out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush writes what a previous write held back.
func (s *Shown) Flush() error {
	if len(s.carry) == 0 {
		return nil
	}
	out := Visible(string(s.carry))
	s.carry = nil
	_, err := io.WriteString(s.W, out)
	return err
}
