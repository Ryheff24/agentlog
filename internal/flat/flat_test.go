package flat

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func paths(ls []Line) string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Path+"="+l.Value)
	}
	return strings.Join(out, " | ")
}

func TestLinesWalksAnyShape(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"flat", `{"a":1,"b":"x"}`, `a=1 | b=x`},
		{"nested", `{"p":{"q":{"r":"deep"}}}`, `p.q.r=deep`},
		{"array", `{"xs":[1,"two",{"k":"v"}]}`, `xs[0]=1 | xs[1]=two | xs[2].k=v`},
		{"scalars", `{"t":true,"f":false,"n":null,"d":-2.5e3}`, `t=true | f=false | n=null | d=-2.5e3`},
		{"empties", `{"o":{},"a":[],"s":""}`, `o=(empty) | a=(empty) | s=(empty)`},
		{"spaced", `{ "a" : [ 1 , 2 ] }`, `a[0]=1 | a[1]=2`},
		{"top-level scalar", `"lonely"`, `.=lonely`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paths(Lines([]byte(tt.src), Options{})); got != tt.want {
				t.Errorf("Lines() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The reason this package exists: a person's words are stored escaped, so the
// sentence they typed cannot be found in the raw bytes.
func TestTextUnescapesSoQuotesAreFindable(t *testing.T) {
	typed := "it's broken\nfix it \"now\"\tplease"
	record, err := json.Marshal(map[string]any{"message": typed})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), "broken\nfix") {
		t.Fatal("fixture is not escaped, so it proves nothing")
	}
	got := Search(record, Options{})
	for _, want := range []string{"it's broken fix it", `"now"`, "please"} {
		if !strings.Contains(got, want) {
			t.Errorf("Search() = %q, want it to contain %q", got, want)
		}
	}
}

func TestTextDecodesEscapes(t *testing.T) {
	tests := []struct{ src, want string }{
		{`{"s":"aéb"}`, "aéb"},
		{`{"s":"😀 hi"}`, "😀 hi"},
		{`{"s":"back\\slash"}`, `back\slash`},
		{`{"s":"a\/b"}`, "a/b"},
		{`{"s":"line\nline"}`, "line line"},
		{`{"s":"   padded   "}`, "padded"},
		{`{"s":"many     spaces"}`, "many spaces"},
	}
	for _, tt := range tests {
		got := Lines([]byte(tt.src), Options{})
		if len(got) != 1 || got[0].Value != tt.want {
			t.Errorf("Lines(%s) = %q, want %q", tt.src, paths(got), tt.want)
		}
	}
}

// A 40MB tool result must never be assembled in order to throw it away.
func TestTextClipsWithoutBuildingTheWholeValue(t *testing.T) {
	huge := strings.Repeat("output ", 1<<18)
	record, _ := json.Marshal(map[string]any{"stdout": huge})
	got := Lines(record, Options{MaxValue: 100})
	if len(got) != 1 {
		t.Fatalf("lines = %d, want 1", len(got))
	}
	if n := len(got[0].Value); n > 100 {
		t.Errorf("value is %d bytes, want at most 100", n)
	}
	if !got[0].Clipped() || got[0].Raw < len(huge) {
		t.Errorf("line does not report the clip: %+v", Line{Path: got[0].Path, Raw: got[0].Raw})
	}
}

func TestBlobsAreNamedNotQuoted(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"signature", `{"thinking_signature":"abc"}`, "<signature, 3B not shown>"},
		{"encrypted", `{"encryptedContent":"zz"}`, "<encrypted content, 2B not shown>"},
		{"long base64", `{"data":"` + strings.Repeat("QUJD", 200) + `"}`, "<binary or encoded data, 800B not shown>"},
		{"prose stays", `{"data":"` + strings.Repeat("hello there ", 40) + `"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lines([]byte(tt.src), Options{})[0].Value
			if tt.want == "" {
				if strings.HasPrefix(got, "<") {
					t.Errorf("prose was taken for a blob: %q", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("value = %q, want %q", got, tt.want)
			}
		})
	}
}

// Logs are read while they are being written, so the last record is often half
// there. Whatever was complete must still come back.
func TestLinesKeepsWhatItReadOfABrokenRecord(t *testing.T) {
	got := paths(Lines([]byte(`{"type":"assistant","message":{"text":"partial`), Options{}))
	if !strings.Contains(got, "type=assistant") {
		t.Errorf("Lines() = %q, want the fields before the break", got)
	}
}

func TestLinesBoundsAHugeArray(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"xs":[`)
	for i := range 5000 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"x"`)
	}
	sb.WriteString(`]}`)
	got := Lines([]byte(sb.String()), Options{MaxLines: 10})
	if len(got) != 11 {
		t.Fatalf("lines = %d, want 10 and a note", len(got))
	}
	if !strings.Contains(got[10].Value, "more fields") {
		t.Errorf("last line = %q, want it to say what was left out", got[10].Value)
	}
}

func TestSkipLeavesFieldsOut(t *testing.T) {
	o := Options{Skip: func(p string) bool { return strings.Contains(strings.ToLower(p), "uuid") }}
	got := paths(Lines([]byte(`{"uuid":"x","parentUuid":"y","text":"keep"}`), o))
	if got != "text=keep" {
		t.Errorf("Lines() = %q, want the uuids left out", got)
	}
}

func BenchmarkLines(b *testing.B) {
	record, _ := json.Marshal(map[string]any{
		"type": "event_msg",
		"payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{"type": "CommandExecution", "id": "call_1",
				"aggregated_output": strings.Repeat("build output line\n", 1<<14)},
		},
	})
	b.SetBytes(int64(len(record)))
	for b.Loop() {
		Lines(record, Options{})
	}
}

// Harnesses write non-ASCII text as \u escapes (Codex does for every emoji
// and accented character), and a character outside the BMP as a surrogate
// pair. A record cut off mid-escape, or an escape a harness got wrong, must
// end that value rather than print garbage or panic.
func TestTextDecodesUnicodeEscapes(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"bmp", `{"s":"caf\u00e9"}`, "café"},
		{"surrogate pair", `{"s":"ok \ud83d\ude00"}`, "ok 😀"},
		{"lone high surrogate", `{"s":"x\ud83dy"}`, "x\uFFFDy"},
		{"lone low surrogate", `{"s":"x\ude00y"}`, "x\uFFFDy"},
		{"high then not low", `{"s":"x\ud83d\u0041"}`, "x\uFFFDA"},
		{"uppercase hex", `{"s":"\u00C9t\u00E9"}`, "Été"},
		{"escape cut short", `{"s":"ab\u00"}`, "ab"},
		{"bad hex ends value", `{"s":"ab\uZZZZcd"}`, "ab"},
		{"unknown escape ends value", `{"s":"ab\qcd"}`, "ab"},
		{"backspace and formfeed are space", `{"s":"a\b\f b"}`, "a b"},
		{"escaped quote in key and value", `{"k\"ey":"say \"hi\""}`, `say "hi"`},
		{"escaped crlf collapses", `{"s":"one\r\n\r\ntwo"}`, "one two"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lines([]byte(tt.src), Options{})
			if len(got) != 1 || got[0].Value != tt.want {
				t.Errorf("Lines(%s) = %q, want %q", tt.src, paths(got), tt.want)
			}
		})
	}
}

// unescape is handed the tail of a value; a lone backslash at the very end
// cannot come from the walker (it would escape the closing quote) but the
// function must still not index past its input.
func TestUnescapeShortInput(t *testing.T) {
	for _, s := range []string{`\`, `\u`, `\u12`, `\u123`} {
		if r, n := unescape([]byte(s)); r != -1 || n != len(s) {
			t.Errorf("unescape(%q) = %d, %d, want -1, %d", s, r, n, len(s))
		}
	}
}

// A clip lands on a byte count, and a value is decoded byte by byte, so the
// cut can fall inside a multi-byte character. What comes back is printed to
// a terminal and indexed for search, so it must always be valid UTF-8, and
// never longer than asked for (a stray byte, replaced by a 3-byte mark, is
// the one thing allowed to grow it).
func TestClippedValuesStayValidUTF8(t *testing.T) {
	tests := []struct {
		name, src string
		max       int
	}{
		{"raw multibyte", `{"s":"abcd€€€€€€"}`, 5},
		{"escaped multibyte", `{"s":"abcd\u20ac\u20ac\u20ac"}`, 5},
		{"escaped astral", `{"s":"abc\ud83d\ude00\ud83d\ude00"}`, 5},
		{"stray byte in the log", "{\"s\":\"a\xffbcdefgh\"}", 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lines([]byte(tt.src), Options{MaxValue: 5})[0].Value
			if !utf8.ValidString(got) {
				t.Errorf("value %q is not valid UTF-8", got)
			}
			if len(got) > tt.max {
				t.Errorf("value %q is %d bytes, want at most %d", got, len(got), tt.max)
			}
		})
	}
}

// A walk stops at the first byte that is not JSON, keeping what came before.
// These are the ways a line in a log is not JSON: cut off (the harness is
// writing it now), or not a record at all.
func TestLinesStopsAtMalformedInput(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"cut after a value", `{"a":1`, `a=1`},
		{"cut after a comma", `{"a":1,`, `a=1`},
		{"cut after a key", `{"a":1,"b"`, `a=1`},
		{"cut inside a key", `{"a":1,"b`, `a=1`},
		{"missing colon", `{"a" 1}`, ``},
		{"unquoted key", `{a:1}`, ``},
		{"no separator", `{"a":1 "b":2}`, `a=1`},
		{"missing value", `{"a":,"b":2}`, ``},
		{"array cut", `{"xs":[1,2`, `xs[0]=1 | xs[1]=2`},
		{"array no separator", `{"xs":[1 2]}`, `xs[0]=1`},
		{"array cut after comma", `{"xs":[1,`, `xs[0]=1`},
		{"empty input", ``, ``},
		{"only whitespace", "  \n", ``},
		{"top-level number", `42`, `.=42`},
		{"top-level null with trailing space", "null  ", `.=null`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paths(Lines([]byte(tt.src), Options{})); got != tt.want {
				t.Errorf("Lines(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

// A signature, an image or an encrypted blob is named with its size, so a
// reader can tell a 3MB screenshot from an empty field.
func TestBlobSizesAreHumanReadable(t *testing.T) {
	tests := []struct {
		key  string
		n    int
		want string
	}{
		{"image_data", 2048, "<encoded data, 2.0KB not shown>"},
		{"source.base64", 3 << 20, "<encoded data, 3.0MB not shown>"},
		{"data", 1536, "<binary or encoded data, 1.5KB not shown>"},
	}
	for _, tt := range tests {
		src := `{"` + strings.ReplaceAll(tt.key, ".", `":{"`) + `":"` + strings.Repeat("A", tt.n) + `"` + strings.Repeat("}", strings.Count(tt.key, ".")+1)
		got := Lines([]byte(src), Options{})
		if len(got) != 1 || got[0].Value != tt.want {
			t.Errorf("%s of %d bytes = %q, want %q", tt.key, tt.n, paths(got), tt.want)
		}
	}
}

// Record is the stand-in view for a record nobody can read: its content, not
// its bookkeeping. The envelope is dropped at any depth, but a nested "type"
// is content (it says what kind of block this is) and stays.
func TestRecordDropsTheEnvelope(t *testing.T) {
	src := `{"type":"brand-new","uuid":"u1","parentUuid":"u0","sessionId":"s","timestamp":"t",` +
		`"message":{"type":"note","uuid":"u2","text":"the content","items":[{"request_id":"r","v":1}]}}`
	got := Record([]byte(src))
	want := "message.type: note\nmessage.text: the content\nmessage.items[0].v: 1"
	if got != want {
		t.Errorf("Record() =\n%s\nwant\n%s", got, want)
	}
}

// A record with a huge array or a huge value becomes a stand-in about the
// size of an ordinary event, not the size of the record.
func TestRecordIsBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"xs":[`)
	for i := range 200 {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(`"` + strings.Repeat("word ", 200) + `"`)
	}
	sb.WriteString(`]}`)
	got := Record([]byte(sb.String()))
	if n := strings.Count(got, "\n") + 1; n != recordLines+1 {
		t.Errorf("Record() has %d lines, want %d and a note", n, recordLines+1)
	}
	for _, line := range strings.Split(got, "\n") {
		if len(line) > recordValue+len("xs[199]: ") {
			t.Fatalf("line of %d bytes survived: %.60q…", len(line), line)
		}
	}
}
