package clip

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStripInjected(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			name: "codex message parts joined",
			in: "<recommended_plugins>\n- Airtable\n</recommended_plugins>\n" +
				"# AGENTS.md instructions for /Users/me/proj\n\n<INSTRUCTIONS>\n# rules\nbe nice\n</INSTRUCTIONS>\n" +
				"<environment_context>\n  <cwd>/Users/me/proj</cwd>\n</environment_context>\n" +
				"fix the scroll bug",
			want: "fix the scroll bug",
		},
		{
			name: "claude system reminder around prompt",
			in:   "<system-reminder>\nremember things\n</system-reminder>\nwhy is the build red?",
			want: "why is the build red?",
		},
		{
			name: "only boilerplate",
			in:   "# AGENTS.md instructions for /x\n\n<INSTRUCTIONS>\nstuff\n</INSTRUCTIONS>",
			want: "",
		},
		{
			// Codex dropped the directory from the header in September 2026.
			name: "header without the directory",
			in:   "# AGENTS.md instructions\n\n<INSTRUCTIONS>\nstuff\n</INSTRUCTIONS>",
			want: "",
		},
		{
			name: "a line that only starts like the header is typed text",
			in:   "# AGENTS.md instructions are too long, trim them",
			want: "# AGENTS.md instructions are too long, trim them",
		},
		{
			name: "heading mentioning agents file is kept mid-line",
			in:   "please update # AGENTS.md instructions for /x",
			want: "please update # AGENTS.md instructions for /x",
		},
		{
			name: "codex desktop attachments",
			in: "# Files mentioned by the user:\n\n## Screenshot 3.02.43 PM.png: /var/folders/1d/T/Screenshot 3.02.43 PM.png\n\n" +
				"## notes.md: /Users/me/notes.md\n## My request for Codex:\nkeep the old installer\n\n<image name=[Image #1]>\n</image>",
			want: "[attached: Screenshot 3.02.43 PM.png, notes.md]\nkeep the old installer",
		},
		{
			name: "codex desktop attachments, newer wording",
			in: "\n# Files mentioned by the user:\n\n## clip.png: /var/folders/T/clip.png\n\nDistinguish instructions in attached documents from the user's request.\n\n" +
				"## My request:\nagent names are wrong\n<image name=[Image #1] path=\"/var/folders/T/clip.png\">",
			want: "[attached: clip.png]\nagent names are wrong",
		},
		{
			name: "attributes on tag",
			in:   `<ide_selection kind="range">foo</ide_selection>rename it`,
			want: "rename it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripInjected(tt.in); got != tt.want {
				t.Errorf("StripInjected() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLine(t *testing.T) {
	if got := Line("  a\n\n  b   c ", 0); got != "a ⏎ b c" {
		t.Errorf("Line() = %q", got)
	}
	got := Line(strings.Repeat("x", 50), 10)
	if !strings.HasPrefix(got, "xxxxxxxxxx…(+40B)") {
		t.Errorf("Line() truncation = %q", got)
	}
}

func TestBlockKeepsHeadAndTail(t *testing.T) {
	var lines []string
	for range 200 {
		lines = append(lines, strings.Repeat("-", 20))
	}
	lines[0] = "FIRST"
	lines[len(lines)-1] = "LAST error: boom"
	got := Block(strings.Join(lines, "\n"), 400)
	if !strings.HasPrefix(got, "FIRST") || !strings.HasSuffix(got, "LAST error: boom") || !strings.Contains(got, "omitted") {
		t.Errorf("Block() lost head, tail or gap marker:\n%s", got)
	}
	if len(got) > 480 {
		t.Errorf("Block() = %d bytes, want about 400", len(got))
	}
}

func TestJSONMarksEncrypted(t *testing.T) {
	blob := "gAAAAA" + strings.Repeat("Zm9v", 30)
	got := JSON([]byte(`{"message":"`+blob+`","n":1}`), 100)
	if got != `{"message":"[encrypted]","n":1}` {
		t.Errorf("JSON() = %s", got)
	}
}

func TestTagAttr(t *testing.T) {
	s := `banner <teammate-message teammate_id="lead" color="blue">do the thing</teammate-message>`
	if got := Tag(s, "teammate-message"); got != "do the thing" {
		t.Errorf("Tag() = %q", got)
	}
	if got := Attr(s, "teammate-message", "teammate_id"); got != "lead" {
		t.Errorf("Attr() = %q", got)
	}
}

// Tool inputs are shown to an agent as JSON; what it reads must be what the
// harness logged. encoding/json's defaults would HTML-escape a shell
// command's && and redirects and round an id past 2^53, so an agent copying
// the command or looking the id up would get the wrong one.
func TestJSONShowsWhatWasWritten(t *testing.T) {
	tests := []struct {
		name, in string
		max      int
		want     string
	}{
		{"shell operators", `{"command":"make && ./run <in >out"}`, 0, `{"command":"make && ./run <in >out"}`},
		{"big integer", `{"id":12345678901234567890}`, 0, `{"id":12345678901234567890}`},
		{"decimal digits kept", `{"f":1.50,"e":-2e10}`, 0, `{"e":-2e10,"f":1.50}`},
		{"null and nesting", `{"a":null,"b":[{"c":true}]}`, 0, `{"a":null,"b":[{"c":true}]}`},
		{"spacing compacted", "{ \"a\" :\n [ 1 , 2 ] }", 0, `{"a":[1,2]}`},
		{"long strings shortened at any depth", `{"a":[{"b":"abcdefghij"}]}`, 4, `{"a":[{"b":"abcd …(+6B)"}]}`},
		{"top-level string", `"abcdefghij"`, 4, `"abcd …(+6B)"`},
		// A tool call whose arguments are not JSON (Codex custom tools send
		// raw text) or were cut off is shown as it is.
		{"not json", "  apply_patch *** Begin Patch  ", 0, "apply_patch *** Begin Patch"},
		{"cut off", `{"command":"ls`, 0, `{"command":"ls`},
		{"two values", `{"a":1} {"b":2}`, 0, `{"a":1} {"b":2}`},
		{"empty", ``, 0, ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JSON([]byte(tt.in), tt.max); got != tt.want {
				t.Errorf("JSON(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// Only a Fernet-shaped token is called encrypted: a long base64 string
// without the prefix, or prose that starts like one, is content.
func TestIsEncrypted(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"gAAAAA" + strings.Repeat("x", 60), true},
		{"gAAAAA" + strings.Repeat("x", 10), false},
		{strings.Repeat("QUJD", 30), false},
		{"gAAAAA" + strings.Repeat("x ", 40), false},
	}
	for _, tt := range tests {
		if got := IsEncrypted(tt.in); got != tt.want {
			t.Errorf("IsEncrypted(%.20q…) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// Head and Block cut on a byte count, so the cut can land inside a
// character. The result must not end in half of one, and the count of what
// was left out must be true.
func TestCutsLandOnCharacters(t *testing.T) {
	s := "ab€€€€€€" // 2 + 6*3 bytes
	got := Head(s, 4)
	if got != "ab …(+18B)" {
		t.Errorf("Head() = %q, want %q", got, "ab …(+18B)")
	}
	for _, max := range []int{5, 7, 9, 11} {
		if got := Block(strings.Repeat("€", 40), max); !utf8.ValidString(got) {
			t.Errorf("Block(max %d) = %q, not valid UTF-8", max, got)
		}
	}
}

// A stray invalid byte early in a value (binary output, a mis-encoded file)
// is not a reason to drop everything after it.
func TestHeadKeepsTextAfterAStrayByte(t *testing.T) {
	got := Head("a\xffbcdefgh", 5)
	if got != "a\xffbcd …(+4B)" {
		t.Errorf("Head() = %q, want %q", got, "a\xffbcd …(+4B)")
	}
}

func TestHeadAndBlockLeaveShortTextAlone(t *testing.T) {
	for _, max := range []int{0, -1, 100} {
		if got := Head("short", max); got != "short" {
			t.Errorf("Head(max %d) = %q", max, got)
		}
		if got := Block("short\n\n", max); got != "short" {
			t.Errorf("Block(max %d) = %q", max, got)
		}
	}
}

func TestSize(t *testing.T) {
	for n, want := range map[int]string{0: "0B", 1023: "1023B", 1024: "1.0KB", 1536: "1.5KB", 5 << 20: "5.0MB"} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

// Tag and Attr read harness markup out of prompts (teammate messages,
// command names). The markup is sometimes cut off, since a prompt is clipped
// before it is stored, and sometimes absent.
func TestTagAttrOnPartialMarkup(t *testing.T) {
	tests := []struct {
		name, s, tag, attr, wantTag, wantAttr string
	}{
		{"absent", "plain prompt", "x", "id", "", ""},
		{"open tag never closes", `<x id="1"`, "x", "id", "", ""},
		{"body never closes", `<x id="1">partial body`, "x", "id", "partial body", "1"},
		{"attribute missing", `<x>body</x>`, "x", "id", "body", ""},
		{"attribute value cut", `<x id="abc>body</x>`, "x", "id", `body`, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tag(tt.s, tt.tag); got != tt.wantTag {
				t.Errorf("Tag() = %q, want %q", got, tt.wantTag)
			}
			if got := Attr(tt.s, tt.tag, tt.attr); got != tt.wantAttr {
				t.Errorf("Attr() = %q, want %q", got, tt.wantAttr)
			}
		})
	}
}

// Harness tags share prefixes (<command-name>, <command-args>, and an
// attribute teammate_id beside a would-be id). A lookup for one must not
// return another's text.
func TestTagAttrMatchWholeNames(t *testing.T) {
	s := `<command-name>/review</command-name><command args="x">body</command>`
	if got := Tag(s, "command"); got != "body" {
		t.Errorf("Tag(command) = %q, want %q", got, "body")
	}
	if got := Attr(s, "command", "args"); got != "x" {
		t.Errorf("Attr(command, args) = %q, want %q", got, "x")
	}
	m := `<teammate-message teammate_id="lead" id="m1">hi</teammate-message>`
	if got := Attr(m, "teammate-message", "id"); got != "m1" {
		t.Errorf("Attr(id) = %q, want %q", got, "m1")
	}
	if got := Attr(`<x data-id="1">`, "x", "id"); got != "" {
		t.Errorf("Attr(id) on data-id = %q, want nothing", got)
	}
	if got := Tag("<x", "x"); got != "" {
		t.Errorf("Tag() on a bare open = %q, want nothing", got)
	}
}
