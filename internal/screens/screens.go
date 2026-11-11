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

// maxDiff is the most lines a screen may have for its changed lines to be
// worked out: the table behind it grows with the product of the two sides. A
// screen is under a hundred lines.
const maxDiff = 2000

// The list of changed lines in the comment is bounded in lines, in bytes and
// in the width of a line, so it stays a summary and the comment stays under
// its limit.
const (
	maxChangeLines = 40
	maxChangeBytes = 2500
	maxChangeWidth = 160
)

type pane struct {
	command       string
	before, after string
	beforeLabel   string
}

func panes(before, after Side) []pane {
	return []pane{
		{"agentlog", before.Bare, after.Bare, before.Label},
		{"agentlog --help", before.Help, after.Help, before.Label},
	}
}

// Changed reports whether the two builds print a different screen.
func Changed(before, after Side) bool {
	return before.Bare != after.Bare || before.Help != after.Help
}

// Comment is the pull request comment: which screens changed, the lines that
// changed in each, and each changed screen whole, before and then after. The
// new screen is the one left open. A screen that did not change is only named.
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
	fmt.Fprintf(&b, "| screen | %s | %s | lines changed |\n|---|---|---|---|\n", cell(before.Label), cell(after.Label))
	for _, p := range panes(before, after) {
		now, lines := size(p.after), "none"
		if p.before == p.after {
			now = "unchanged"
		} else {
			lines = changedCount(changes(p.before, p.after))
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", p.command, size(p.before), now, lines)
	}
	for _, p := range panes(before, after) {
		if p.before == p.after {
			continue
		}
		changedLines(&b, p)
		block(&b, p.command, before.Label, p.before, false)
		block(&b, p.command, after.Label, p.after, true)
	}
	if before.Bare == before.Help && after.Bare != after.Help {
		b.WriteString("\nBefore this change both commands printed the same screen. Now they differ.\n")
	}
	return b.String()
}

// changedCount says how many lines a change removed and added.
func changedCount(gone, added []int, ok bool) string {
	if !ok {
		return "too long to compare line by line"
	}
	return fmt.Sprintf("%d removed, %d added", len(gone), len(added))
}

// changedLines lists the lines a change removed and added, each with its
// line number on its own side, so a reader does not have to find one changed
// line among eighty. A screen that is new or gone on one side has no list:
// every line of it changed.
func changedLines(b *strings.Builder, p pane) {
	gone, added, ok := changes(p.before, p.after)
	if !ok || p.before == "" || p.after == "" {
		return
	}
	old, now := splitLines(p.before), splitLines(p.after)
	var list strings.Builder
	shown, i, j := 0, 0, 0
	for (i < len(gone) || j < len(added)) && shown < maxChangeLines {
		// In the order a reader meets them going down the screen.
		var line string
		if j == len(added) || i < len(gone) && gone[i] <= added[j] {
			line = fmt.Sprintf("- %d: %s\n", gone[i]+1, clip(clean(old[gone[i]])))
			i++
		} else {
			line = fmt.Sprintf("+ %d: %s\n", added[j]+1, clip(clean(now[added[j]])))
			j++
		}
		if list.Len()+len(line) > maxChangeBytes {
			break
		}
		list.WriteString(line)
		shown++
	}
	if left := len(gone) + len(added) - shown; left > 0 {
		fmt.Fprintf(&list, "(%d more changed lines; both screens are whole below)\n", left)
	}
	text := list.String()
	f := fence(text)
	fmt.Fprintf(b, "\nLines changed in `%s`: `-` is a line of %s that is gone, `+` a line that is new, each with its line number.\n\n%stext\n%s%s\n",
		p.command, cell(p.beforeLabel), f, text, f)
}

// clip shortens one listed line to maxChangeWidth bytes, on a whole character.
func clip(line string) string {
	if len(line) <= maxChangeWidth {
		return line
	}
	line = line[:maxChangeWidth]
	for !utf8.ValidString(line) {
		line = line[:len(line)-1]
	}
	return line + "…"
}

// splitLines is the lines of a screen, without the empty one after a final
// newline.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// changes compares two screens line by line and returns the lines of before
// that are gone and the lines of after that are new, counted from zero. It
// keeps the longest run of lines the two share, in order, so a block that
// moved shows as gone in one place and new in another. ok is false when a
// side has more lines than maxDiff.
func changes(before, after string) (gone, added []int, ok bool) {
	a, b := splitLines(before), splitLines(after)
	if len(a) > maxDiff || len(b) > maxDiff {
		return nil, nil, false
	}
	// keep[i][j] is how many lines a[i:] and b[j:] share in order.
	keep := make([][]int, len(a)+1)
	for i := range keep {
		keep[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				keep[i][j] = keep[i+1][j+1] + 1
			} else {
				keep[i][j] = max(keep[i+1][j], keep[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			i, j = i+1, j+1
		case keep[i+1][j] >= keep[i][j+1]:
			gone = append(gone, i)
			i++
		default:
			added = append(added, j)
			j++
		}
	}
	for ; i < len(a); i++ {
		gone = append(gone, i)
	}
	for ; j < len(b); j++ {
		added = append(added, j)
	}
	return gone, added, true
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
// pane is the exact text, one row per line, nothing wrapped. A line that is
// gone or new is marked by a sign in the margin and a tint, and each pane's
// label counts them and links to the first, so one changed line is not lost
// among eighty. The sign and the tint are not part of the text.
func Page(before, after Side) string {
	data, _ := json.Marshal(map[string]string{
		"before-bare": before.Bare, "before-help": before.Help,
		"after-bare": after.Bare, "after-help": after.Help,
	})
	marks := map[string][]int{}
	for id, p := range map[string]pane{"bare": panes(before, after)[0], "help": panes(before, after)[1]} {
		gone, added, _ := changes(p.before, p.after)
		marks["before-"+id], marks["after-"+id] = orNone(gone), orNone(added)
	}
	marked, _ := json.Marshal(marks)
	r := strings.NewReplacer(
		"BEFORE", html.EscapeString(before.Label),
		"AFTER", html.EscapeString(after.Label),
		// json.Marshal writes <, > and & as \u escapes, so the text cannot end the script.
		"DATA", string(data),
		"MARKS", string(marked),
	)
	return r.Replace(page)
}

// orNone keeps an empty list from being written as null.
func orNone(lines []int) []int {
	if lines == nil {
		return []int{}
	}
	return lines
}

const page = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>agentlog stdout compare</title><style>
html,body{margin:0;background:#1e1f22;color:#e6e6e3;font:13px -apple-system,system-ui,sans-serif}
.grid{display:grid;grid-template-columns:max-content max-content;width:max-content;gap:10px;padding:10px}
.label{margin:0 0 4px;color:#9b9b96}
.label b{color:#e6e6e3;font-family:ui-monospace,Menlo,monospace}
.label a{color:#e6e6e3}
pre{margin:0;padding:6px;background:#282a2e;white-space:pre;tab-size:8;font-family:Menlo,ui-monospace,monospace;font-size:12px;line-height:1.35}
.l{display:inline-block;min-width:100%;box-sizing:border-box;padding-left:2ch}
.l.gone,.l.new{font-weight:700}
.l.gone{background:rgba(255,176,0,.20)}
.l.new{background:rgba(88,166,255,.24)}
.l.gone::before,.l.new::before{display:inline-block;width:2ch;margin-left:-2ch}
.l.gone::before{content:"\2212"}
.l.new::before{content:"+"}
</style></head><body>
<div class="grid">
<div><p class="label">before · <b>agentlog</b> · BEFORE · <span id="n-before-bare"></span></p><pre id="before-bare"></pre></div>
<div><p class="label">after · <b>agentlog</b> · AFTER · <span id="n-after-bare"></span></p><pre id="after-bare"></pre></div>
<div><p class="label">before · <b>agentlog --help</b> · BEFORE · <span id="n-before-help"></span></p><pre id="before-help"></pre></div>
<div><p class="label">after · <b>agentlog --help</b> · AFTER · <span id="n-after-help"></span></p><pre id="after-help"></pre></div>
</div>
<script>
const out = DATA;
const marks = MARKS;
for (const [id, text] of Object.entries(out)) {
  const pre = document.getElementById(id);
  const lines = text.split("\n");
  if (text.endsWith("\n")) lines.pop();
  const marked = new Set(marks[id]);
  const kind = id.startsWith("before") ? "gone" : "new";
  lines.forEach((line, i) => {
    const row = document.createElement("span");
    row.className = marked.has(i) ? "l " + kind : "l";
    row.textContent = line;
    if (i === marks[id][0]) row.id = "first-" + id;
    pre.append(row, "\n");
  });
  const n = document.getElementById("n-" + id);
  n.textContent = [...text].length + " characters, " + new TextEncoder().encode(text).length + " bytes, " + lines.length + " lines";
  if (marked.size > 0) {
    const a = document.createElement("a");
    a.href = "#first-" + id;
    a.textContent = marked.size + (marked.size === 1 ? " line " : " lines ") + (kind === "gone" ? "gone" : "new") + ", first at line " + (marks[id][0] + 1);
    n.append(" \u00b7 ", a);
  } else {
    n.append(" \u00b7 no line " + (kind === "gone" ? "gone" : "new"));
  }
}
</script></body></html>
`
