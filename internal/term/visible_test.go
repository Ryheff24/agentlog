package term

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// hasControl reports a raw control byte of the kinds an agent should never
// be handed: C0 other than newline and tab, DEL, C1, or a byte that is not
// UTF-8.
func hasControl(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
			return true
		}
	}
	return false
}

func TestVisible(t *testing.T) {
	for _, tt := range []struct{ name, in, want string }{
		{"plain text is untouched", "plain text\n\tindented", "plain text\n\tindented"},
		{"other languages are untouched", "héllo → ok, 日本語, ✓, emoji 🙂", "héllo → ok, 日本語, ✓, emoji 🙂"},
		{"a real U+FFFD stays", "bad � char", "bad � char"},
		{"empty", "", ""},
		{"a colour code", "\x1b[31mFAIL\x1b[0m", "␛[31mFAIL␛[0m"},
		{"a lone ESC", "a\x1bb", "a␛b"},
		{"a title-setting sequence", "\x1b]0;owned\x07title", "␛]0;owned␇title"},
		{"CRLF is a line ending", "one\r\ntwo\r\n", "one\ntwo\n"},
		{"a lone CR is shown", "10%\r20%", "10%␍20%"},
		{"CR at the end", "end\r", "end␍"},
		{"NUL, backspace, DEL", "\x00\x08\x7f", "␀␈␡"},
		{"every C0 has a picture", "\x01\x1f\x0b\x0c", "␁␟␋␌"},
		{"C1 is named", "c1 \u009b31m \u0085", `c1 \u009b31m \u0085`},
		{"invalid UTF-8 becomes U+FFFD", "a\xffb\xc3", "a�b�"},
		{"a cut multi-byte character", "ok \xe2\x86", "ok ��"},
		{"non-breaking space is text", " nbsp", " nbsp"},
	} {
		got := Visible(tt.in)
		if got != tt.want {
			t.Errorf("%s: Visible(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
		if hasControl(got) {
			t.Errorf("%s: %q still holds a control byte", tt.name, got)
		}
	}
}

// A text with nothing to change comes back as the same string, not a copy.
func TestVisibleLeavesTextAlone(t *testing.T) {
	s := strings.Repeat("ordinary output, ünïcödé\n", 100)
	if got := Visible(s); got != s {
		t.Fatal("changed ordinary text")
	}
}

func TestCount(t *testing.T) {
	c := Count("a\x00b\x1b[0m\r\n\x7f\u0085�\xff\t")
	want := Counts{Bytes: 17, Runes: 14, Control: 3, Replaced: 2}
	if c != want {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestBinary(t *testing.T) {
	binary := strings.Repeat("\x00\x01ELF\xff", 20)
	for _, tt := range []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"text", "go test ./...\nok\n", false},
		{"all binary", binary, true},
		{"binary decoded with replacement", strings.Repeat("�PNG��", 10), true},
		{"a short burst under the minimum", strings.Repeat("\x01", 15), false},
		{"sixteen in a short text", strings.Repeat("\x01", 16), true},
		{"sparse in a long text", strings.Repeat("x", 2000) + strings.Repeat("\x01", 30), false},
		{"colour codes are not binary", strings.Repeat("\x1b[31mred\x1b[0m\r\n", 50), false},
	} {
		if got := Count(tt.in).Binary(); got != tt.want {
			t.Errorf("%s: Binary = %v, want %v (%+v)", tt.name, got, tt.want, Count(tt.in))
		}
	}
}

func TestStandIn(t *testing.T) {
	for _, tt := range []struct {
		c    Counts
		want string
	}{
		{Counts{Bytes: 1012, Control: 123, Replaced: 53}, "[binary, not shown: 1012 bytes, 123 control and 53 U+FFFD characters; agentlog x#1 --bytes prints it as logged]"},
		{Counts{Bytes: 3 << 10}, "3.0KB"},
		{Counts{Bytes: 5 << 20}, "5.0MB"},
	} {
		if got := StandIn(tt.c, "agentlog x#1 --bytes"); !strings.Contains(got, tt.want) || strings.Contains(got, "\n") {
			t.Errorf("StandIn(%+v) = %q, want it to hold %q on one line", tt.c, got, tt.want)
		}
	}
}

// The writer gets text in pieces of any size: a CRLF or a character split
// between two writes must come out as if written whole.
func TestShownAcrossWrites(t *testing.T) {
	in := "colour \x1b[1mbold\x1b[0m\r\nnext 日本 line\r\nlone\rCR\x00 é\xff end\r"
	want := Visible(in)
	for size := 1; size <= len(in); size++ {
		var b bytes.Buffer
		s := &Shown{W: &b}
		for i := 0; i < len(in); i += size {
			j := min(i+size, len(in))
			if n, err := s.Write([]byte(in[i:j])); err != nil || n != j-i {
				t.Fatalf("size %d: wrote %d, %v", size, n, err)
			}
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Errorf("size %d: got %q, want %q", size, b.String(), want)
		}
	}
}

func TestShownFlushWithNothingHeld(t *testing.T) {
	var b bytes.Buffer
	s := &Shown{W: &b}
	s.Write([]byte("done\n"))
	if err := s.Flush(); err != nil || b.String() != "done\n" {
		t.Fatalf("got %q, %v", b.String(), err)
	}
}

func TestShownReportsWriteErrors(t *testing.T) {
	s := &Shown{W: failing{}}
	if n, err := s.Write([]byte("x")); err == nil || n != 0 {
		t.Fatalf("got %d, %v", n, err)
	}
	s.Write([]byte("x\r"))
	if err := s.Flush(); err == nil {
		t.Fatal("Flush lost the error")
	}
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestJSON(t *testing.T) {
	logged := "out \x1b[31mred\x1b[0m\r\nbell\x07 nul\x00 bs\b ff\f del\x7f c1\u0085 cr\r end \"q\" \\x01 \\u0001   é �"
	v := map[string]any{"output": logged, "n": 3, "list": []string{"\x01", "plain"}, "key\x02": "k"}
	enc := encode(t, v)

	got := JSON(enc, false)
	if hasControl(string(got)) {
		t.Fatalf("raw control byte left: %q", got)
	}
	var back map[string]any
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("not JSON any more: %v\n%s", err, got)
	}
	for k, x := range back {
		if hasControl(k) {
			t.Errorf("key %q holds a control character", k)
		}
		if s, ok := x.(string); ok && hasControl(s) {
			t.Errorf("%s holds a control character: %q", k, s)
		}
	}
	if want := Visible(logged); back["output"] != want {
		t.Errorf("output decodes to\n%q\nwant what Visible makes of it\n%q", back["output"], want)
	}
	if back["n"] != 3.0 || back["key␂"] != "k" {
		t.Errorf("other values changed: %v", back)
	}

	exact := JSON(enc, true)
	if hasControl(string(exact)) {
		t.Fatalf("exact output holds a raw control byte: %q", exact)
	}
	var same map[string]any
	if err := json.Unmarshal(exact, &same); err != nil {
		t.Fatal(err)
	}
	if same["output"] != logged || same["key\x02"] != "k" {
		t.Errorf("exact output does not decode to what was logged: %q", same["output"])
	}
}

func TestJSONLeavesOrdinaryOutputAlone(t *testing.T) {
	enc := encode(t, map[string]any{"text": "line one\nline two\ttab \"quoted\" \\ back / slash é 🙂  ", "x": []int{1, 2}})
	for _, exact := range []bool{false, true} {
		if got := JSON(enc, exact); !bytes.Equal(got, enc) {
			t.Errorf("exact=%v changed ordinary JSON:\n%s\n%s", exact, enc, got)
		}
	}
}
