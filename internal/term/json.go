package term

import (
	"fmt"
	"strconv"
)

// JSON rewrites the strings in encoded JSON so no control character is left
// in them, as a byte or as an escape. encoding/json escapes C0 controls as
// \u00NN, which a reader decodes straight back to the bytes, and writes DEL
// and C1 controls as they are. Each becomes what Visible makes of it.
//
// With exact, the strings keep their values: C0 escapes stay, and DEL and C1
// become \u00NN escapes too, so the output holds no raw control byte while a
// JSON decoder still recovers every character as logged.
//
// b must be the output of encoding/json, which is valid UTF-8.
func JSON(b []byte, exact bool) []byte {
	out := make([]byte, 0, len(b))
	in := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if !in {
			out = append(out, c)
			in = c == '"'
			continue
		}
		switch {
		case c == '"':
			out = append(out, c)
			in = false
		case c == '\\' && i+1 < len(b):
			n := escapeLen(b[i:])
			if exact {
				out = append(out, b[i:i+n]...)
				i += n - 1
				continue
			}
			r, ok := escaped(b[i : i+n])
			switch {
			case !ok || r == '\n' || r == '\t' || r >= 0x20 && r < 0x7f || r > 0x9f:
				out = append(out, b[i:i+n]...)
			case r == '\r' && i+n+1 < len(b) && b[i+n] == '\\' && b[i+n+1] == 'n':
				// a line ending: dropped, as in Visible
			default:
				out = appendVisible(out, r)
			}
			i += n - 1
		case c == 0x7f:
			out = appendControl(out, 0x7f, exact)
		case c == 0xc2 && i+1 < len(b) && b[i+1] >= 0x80 && b[i+1] <= 0x9f:
			out = appendControl(out, rune(b[i+1]), exact)
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// escapeLen is the length of the escape sequence that starts e.
func escapeLen(e []byte) int {
	if e[1] == 'u' && len(e) >= 6 {
		return 6
	}
	return 2
}

// escaped decodes one JSON escape. ok is false for an escape that does not
// stand for a single control-range character, such as half a surrogate pair.
func escaped(e []byte) (rune, bool) {
	switch string(e) {
	case `\n`:
		return '\n', true
	case `\t`:
		return '\t', true
	case `\r`:
		return '\r', true
	case `\b`:
		return '\b', true
	case `\f`:
		return '\f', true
	}
	if len(e) != 6 {
		return 0, false
	}
	v, err := strconv.ParseUint(string(e[2:]), 16, 16)
	if err != nil || v > 0x9f {
		return 0, false
	}
	return rune(v), true
}

// appendControl writes a DEL or C1 control found raw inside a JSON string.
func appendControl(out []byte, r rune, exact bool) []byte {
	if exact {
		return fmt.Appendf(out, `\u%04x`, r)
	}
	return appendVisible(out, r)
}

// appendVisible writes what Visible makes of r, escaped for a JSON string.
func appendVisible(out []byte, r rune) []byte {
	if r >= 0x80 && r <= 0x9f {
		return fmt.Appendf(out, `\\u%04x`, r)
	}
	return append(out, Visible(string(r))...)
}
