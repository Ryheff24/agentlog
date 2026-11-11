// Package screens shows a person a change to the two screens every agent
// reads first, a bare `agentlog` and `agentlog --help`: as a comment for a
// pull request, and as one page with all four outputs side by side.
//
// The screens come from the files TestScreens pins in
// internal/app/testdata/screens, which hold the test world's made-up sessions,
// so nothing here ever shows a real log. The text of one side may come from an
// outside pull request, so it is handled as data: cut to a size, cleaned of
// control characters, and fenced so it cannot write markup of its own.
package screens

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

// Side is what one build prints.
type Side struct {
	Label string // which build, for a person: "main", "this pull request"
	Bare  string // agentlog
	Help  string // agentlog --help
}

// Marker starts the comment, so the workflow finds its earlier comment and
// updates it instead of adding another.
const Marker = "<!-- agentlog-screens -->"

// maxPane keeps four panes and the words around them under GitHub's limit of
// 65,536 characters for a comment.
const maxPane = 14000

type pane struct {
	command       string
	before, after string
}

func panes(before, after Side) []pane {
	return []pane{
		{"agentlog", before.Bare, after.Bare},
		{"agentlog --help", before.Help, after.Help},
	}
}

// Changed reports whether the two builds print a different screen.
func Changed(before, after Side) bool {
	return before.Bare != after.Bare || before.Help != after.Help
}

// Comment is the pull request comment: which screens changed, and each changed
// screen whole, before and then after. The new screen is the one left open. A
// screen that did not change is only named.
func Comment(before, after Side) string {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	if !Changed(before, after) {
		b.WriteString("### The home screen and `--help` are unchanged\n\n")
		b.WriteString("An earlier version of this pull request changed them. It no longer does.\n")
		return b.String()
	}
	b.WriteString("### This pull request changes what an agent sees first\n\n")
	b.WriteString("`agentlog` and `agentlog --help` are the first screens every agent reads, and the maintainer placed each line by hand. ")
	b.WriteString("These are the pinned copies from `internal/app/testdata/screens`: the sessions in them are the test world's, not real ones. ")
	b.WriteString("They match what the build prints only when the `test` jobs pass.\n\n")
	fmt.Fprintf(&b, "| screen | %s | %s |\n|---|---|---|\n", cell(before.Label), cell(after.Label))
	for _, p := range panes(before, after) {
		now := size(p.after)
		if p.before == p.after {
			now = "unchanged"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", p.command, size(p.before), now)
	}
	for _, p := range panes(before, after) {
		if p.before == p.after {
			continue
		}
		block(&b, p.command, before.Label, p.before, false)
		block(&b, p.command, after.Label, p.after, true)
	}
	if before.Bare == before.Help && after.Bare != after.Help {
		b.WriteString("\nBefore this change both commands printed the same screen. Now they differ.\n")
	}
	return b.String()
}

// block writes one screen in a fenced block that folds away.
func block(b *strings.Builder, command, label, text string, open bool) {
	attr := ""
	if open {
		attr = " open"
	}
	fmt.Fprintf(b, "\n<details%s><summary><code>%s</code>, %s (%s)</summary>\n\n", attr, command, html.EscapeString(label), size(text))
	if text == "" {
		b.WriteString("No such screen is pinned on this side.\n\n</details>\n")
		return
	}
	text = cut(clean(text))
	f := fence(text)
	fmt.Fprintf(b, "%stext\n%s\n%s\n\n</details>\n", f, strings.TrimSuffix(text, "\n"), f)
}

// size is a screen's lines and bytes, the two numbers an agent pays in.
func size(text string) string {
	if text == "" {
		return "none"
	}
	return fmt.Sprintf("%d lines, %d bytes", lines(text), len(text))
}

func lines(text string) int {
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// cell makes a label safe inside a table cell.
func cell(s string) string {
	return strings.ReplaceAll(html.EscapeString(clean(s)), "|", "\\|")
}

// clean writes every character that could hide or reorder text as its code
// point: control characters other than a newline and a tab, and the Unicode
// marks that change the direction of text. Bytes that are not UTF-8 are
// written as their value.
func clean(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, "\\x%02x", s[0])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r < 0xa0) ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200e || r == 0x200f:
			fmt.Fprintf(&b, "\\u{%04x}", r)
		default:
			b.WriteRune(r)
		}
		s = s[n:]
	}
	return b.String()
}

// cut keeps whole lines up to maxPane bytes and says how many it left out.
func cut(text string) string {
	if len(text) <= maxPane {
		return text
	}
	keep, end := text[:maxPane], ""
	if i := strings.LastIndexByte(keep, '\n'); i >= 0 {
		keep = keep[:i+1]
	} else {
		// One line longer than the limit: cut inside it, on a whole character.
		for !utf8.ValidString(keep) {
			keep = keep[:len(keep)-1]
		}
		end = "\n"
	}
	return fmt.Sprintf("%s%s(cut here: %d more lines, %d more bytes)\n", keep, end, lines(text)-lines(keep), len(text)-len(keep))
}

// fence is a run of backticks longer than any in text, so nothing in text can
// close the block.
func fence(text string) string {
	longest, run := 0, 0
	for _, r := range text {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// Page is one HTML page with the four outputs in a grid: before on the left,
// after on the right, `agentlog` in the top row and `--help` below it. Each
// pane is the exact text, one row per line, nothing wrapped or marked.
func Page(before, after Side) string {
	data, _ := json.Marshal(map[string]string{
		"before-bare": before.Bare, "before-help": before.Help,
		"after-bare": after.Bare, "after-help": after.Help,
	})
	r := strings.NewReplacer(
		"BEFORE", html.EscapeString(before.Label),
		"AFTER", html.EscapeString(after.Label),
		// json.Marshal writes <, > and & as \u escapes, so the text cannot end the script.
		"DATA", string(data),
	)
	return r.Replace(page)
}

const page = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>agentlog stdout compare</title><style>
html,body{margin:0;background:#1e1f22;color:#e6e6e3;font:13px -apple-system,system-ui,sans-serif}
.grid{display:grid;grid-template-columns:max-content max-content;width:max-content;gap:10px;padding:10px}
.label{margin:0 0 4px;color:#9b9b96}
.label b{color:#e6e6e3;font-family:ui-monospace,Menlo,monospace}
pre{margin:0;padding:6px;background:#282a2e;white-space:pre;tab-size:8;font-family:Menlo,ui-monospace,monospace;font-size:12px;line-height:1.35}
</style></head><body>
<div class="grid">
<div><p class="label">before · <b>agentlog</b> · BEFORE · <span id="n-before-bare"></span></p><pre id="before-bare"></pre></div>
<div><p class="label">after · <b>agentlog</b> · AFTER · <span id="n-after-bare"></span></p><pre id="after-bare"></pre></div>
<div><p class="label">before · <b>agentlog --help</b> · BEFORE · <span id="n-before-help"></span></p><pre id="before-help"></pre></div>
<div><p class="label">after · <b>agentlog --help</b> · AFTER · <span id="n-after-help"></span></p><pre id="after-help"></pre></div>
</div>
<script>
const out = DATA;
for (const [id, text] of Object.entries(out)) {
  document.getElementById(id).textContent = text;
  const lines = text.split("\n").length - (text.endsWith("\n") ? 1 : 0);
  document.getElementById("n-" + id).textContent = [...text].length + " characters, " + new TextEncoder().encode(text).length + " bytes, " + lines + " lines";
}
</script></body></html>
`
