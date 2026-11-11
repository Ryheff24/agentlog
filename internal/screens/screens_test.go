package screens

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const home = "harnesses:\n  claude   ~/claude/projects   2 sessions\n\nusage: agentlog [clues...] [flags]\n  --help, -h\n"

// The case that asked for this: a pull request moves the usage to the top of
// --help and leaves a bare call alone.
func flipped() (Side, Side) {
	usageFirst := "usage: agentlog [clues...] [flags]\n  --help, -h\n\nharnesses:\n  claude   ~/claude/projects   2 sessions\n"
	return Side{Label: "main", Bare: home, Help: home}, Side{Label: "this pull request", Bare: home, Help: usageFirst}
}

func TestChanged(t *testing.T) {
	before, after := flipped()
	if !Changed(before, after) {
		t.Error("a moved usage is not a change")
	}
	if Changed(before, Side{Label: "another name", Bare: home, Help: home}) {
		t.Error("the same screens under another label are a change")
	}
	if !Changed(before, Side{Bare: home + " ", Help: home}) {
		t.Error("one more space on the bare screen is not a change")
	}
}

func TestCommentShowsOnlyTheScreenThatChanged(t *testing.T) {
	before, after := flipped()
	c := Comment(before, after)
	for _, want := range []string{
		Marker + "\n### This pull request changes what an agent sees first",
		"| screen | main | this pull request | lines changed |",
		"| `agentlog` | 5 lines, 102 bytes | unchanged | none |",
		"| `agentlog --help` | 5 lines, 102 bytes | 5 lines, 102 bytes | 3 removed, 3 added |",
		// The lines that changed, before the whole screens, each with the
		// line number on its own side.
		"Lines changed in `agentlog --help`: `-` is a line of main that is gone, `+` a line that is new, each with its line number.\n\n```text\n- 1: harnesses:\n- 2:   claude   ~/claude/projects   2 sessions\n- 3: \n+ 3: \n+ 4: harnesses:\n+ 5:   claude   ~/claude/projects   2 sessions\n```\n",
		"<details open><summary><code>agentlog --help</code>, this pull request (5 lines, 102 bytes)</summary>\n\n```text\nusage: agentlog [clues...] [flags]\n  --help, -h\n\nharnesses:\n  claude   ~/claude/projects   2 sessions\n```\n\n</details>",
		"<details><summary><code>agentlog --help</code>, main (5 lines, 102 bytes)</summary>\n\n```text\nharnesses:",
		"Before this change both commands printed the same screen. Now they differ.",
		"not real ones",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("the comment lacks %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "<code>agentlog</code>") {
		t.Errorf("the comment prints the bare screen, which did not change:\n%s", c)
	}
	if strings.Contains(c, "Lines changed in `agentlog`:") {
		t.Errorf("the comment lists changed lines for the bare screen, which did not change:\n%s", c)
	}
	if strings.Index(c, "Lines changed in") > strings.Index(c, "<details") {
		t.Errorf("the changed lines come after the whole screens:\n%s", c)
	}
	// Before, then after, the way the table and the page read.
	if strings.Index(c, ", main (") > strings.Index(c, ", this pull request (") {
		t.Errorf("the new screen comes before the old one:\n%s", c)
	}
}

func TestCommentWhenBothScreensChangeTheSameWay(t *testing.T) {
	before := Side{Label: "main", Bare: home, Help: home}
	after := Side{Label: "this pull request", Bare: home + "one more line\n", Help: home + "one more line\n"}
	c := Comment(before, after)
	if strings.Count(c, "<details") != 4 {
		t.Errorf("two changed screens want four blocks:\n%s", c)
	}
	if strings.Contains(c, "Now they differ") {
		t.Errorf("the comment says the two commands differ, and they print the same screen:\n%s", c)
	}
	if !strings.Contains(c, "| `agentlog` | 5 lines, 102 bytes | 6 lines, 116 bytes | 0 removed, 1 added |") {
		t.Errorf("the table lacks the bare screen's new size:\n%s", c)
	}
	if strings.Count(c, "```text\n+ 6: one more line\n```") != 2 {
		t.Errorf("each screen wants its one new line listed with its number:\n%s", c)
	}
}

// The pull request that first pins the screens has nothing before it, and one
// that deletes a pinned file has nothing after.
func TestCommentWithAScreenMissingOnOneSide(t *testing.T) {
	c := Comment(Side{Label: "main"}, Side{Label: "this pull request", Bare: home, Help: home})
	for _, want := range []string{
		"| `agentlog` | none | 5 lines, 102 bytes | 0 removed, 5 added |",
		"<details><summary><code>agentlog</code>, main (none)</summary>\n\nNo such screen is pinned on this side.\n\n</details>",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("the comment lacks %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "Now they differ") {
		t.Errorf("two empty screens are not two commands that printed the same screen:\n%s", c)
	}
	// Every line of a screen that is new is new: a list of them says nothing.
	if strings.Contains(c, "Lines changed in") {
		t.Errorf("a screen with nothing before it has a list of changed lines:\n%s", c)
	}
	if gone := Comment(Side{Label: "main", Bare: home, Help: home}, Side{Label: "this pull request"}); strings.Contains(gone, "Lines changed in") || !strings.Contains(gone, "| 5 removed, 0 added |") {
		t.Errorf("a screen with nothing after it:\n%s", gone)
	}
}

func TestCommentWhenAChangeWasTakenBack(t *testing.T) {
	before, _ := flipped()
	c := Comment(before, Side{Label: "this pull request", Bare: home, Help: home})
	if !strings.HasPrefix(c, Marker+"\n### The home screen and `--help` are unchanged") || strings.Contains(c, "<details") {
		t.Errorf("an unchanged pair should say so and show no screen:\n%s", c)
	}
}

// A pinned file in an outside pull request is that person's text. It must not
// close its block, hide text, or write markup into the comment.
func TestCommentCannotBeWrittenByTheScreen(t *testing.T) {
	hostile := "usage\n```\n</details>\n# Approved by the maintainer\n@someone ````\n\x1b[2Jgone\r\nright\u202eleft\x00\xff end\n"
	c := Comment(Side{Label: "main", Bare: home, Help: home}, Side{Label: "a|b <b>", Bare: home, Help: hostile})
	open := "`````text\n"
	// The list of changed lines comes first and holds the same text, a line at
	// a time. It needs the same fence, and closes before the screens start.
	list := c[strings.Index(c, "Lines changed in"):strings.Index(c, "<details")]
	if strings.Count(list, "`````") != 2 || !strings.Contains(list, open+"- 1: harnesses:\n+ 1: usage\n") {
		t.Fatalf("the list of changed lines is not fenced past the backticks inside it:\n%s", list)
	}
	for _, want := range []string{"\n+ 2: ```\n", "\n+ 3: </details>\n", "\n+ 4: # Approved by the maintainer\n", "\n+ 5: @someone ````\n", "\n+ 6: \\u{001b}[2Jgone\\u{000d}\n", "\n+ 7: right\\u{202e}left\\u{0000}\\xff end\n`````\n"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list of changed lines lacks %q:\n%s", want, list)
		}
	}
	start := strings.Index(c, "<details open>") + strings.Index(c[strings.Index(c, "<details open>"):], open)
	if !strings.Contains(c[strings.Index(c, "<details open>"):], open) {
		t.Fatalf("the block's fence is not longer than the four backticks inside it:\n%s", c)
	}
	body := c[start+len(open):]
	end := strings.Index(body, "\n`````\n")
	if end < 0 {
		t.Fatalf("the block never closes:\n%s", c)
	}
	body = body[:end]
	for _, want := range []string{"```\n</details>\n# Approved by the maintainer\n@someone ````", `\u{001b}[2Jgone\u{000d}`, `right\u{202e}left\u{0000}\xff end`} {
		if !strings.Contains(body, want) {
			t.Errorf("inside the block, want %q:\n%s", want, body)
		}
	}
	for _, r := range c {
		if r != '\n' && (r < 0x20 || r == 0x202e || r == utf8.RuneError) {
			t.Errorf("the comment still holds the character %U", r)
		}
	}
	if !strings.Contains(c, "| screen | main | a\\|b &lt;b&gt; |") || !strings.Contains(c, ", a|b &lt;b&gt; (") {
		t.Errorf("the label is not escaped:\n%s", c)
	}
}

func TestCleanKeepsWhatAScreenReallyPrints(t *testing.T) {
	// The marks agentlog prints itself, and a tab, pass through.
	real := "last prompt: human attached ⏎ [image] · \"Fix\" … 1 more\n\tindented — ok\n"
	if got := clean(real); got != real {
		t.Errorf("clean changed a real screen:\n%q\n%q", real, got)
	}
	if got := clean("a\u2066b\u2069c\u200ed\u200fe\x7ff\u0085g"); got != `a\u{2066}b\u{2069}c\u{200e}d\u{200f}e\u{007f}f\u{0085}g` {
		t.Errorf("clean left a hidden mark: %q", got)
	}
}

func TestCommentFitsGitHubsLimit(t *testing.T) {
	long := strings.Repeat(strings.Repeat("é", 60)+"\n", 2000) // 242,000 bytes
	c := Comment(Side{Label: "main", Bare: long, Help: long}, Side{Label: "this pull request", Bare: long + "x\n", Help: long + "y\n"})
	if n := utf8.RuneCountInString(c); n > 65536 {
		t.Errorf("the comment is %d characters, over GitHub's 65,536", n)
	}
	if !utf8.ValidString(c) {
		t.Error("the cut left half a character")
	}
	// 14,000 bytes hold 115 whole lines of 121 bytes.
	if want := "(cut here: 1885 more lines, 228085 more bytes)"; !strings.Contains(c, want) {
		t.Errorf("the comment does not say %q", want)
	}
	if !strings.Contains(c, "2001 lines, 242002 bytes") {
		t.Error("the table gives the size after the cut, not the screen's own")
	}
	// Comparing line by line costs the product of the two sides, so a screen
	// this long is not compared, and the comment says so.
	if !strings.Contains(c, "| too long to compare line by line |") || strings.Contains(c, "Lines changed in") {
		t.Error("a screen over the line limit was compared line by line")
	}
}

// The worst case that is still compared: every line of both screens changed,
// every line as wide as a list entry may be, nothing but one-byte characters.
func TestCommentFitsWithEveryLineChanged(t *testing.T) {
	old := strings.Repeat(strings.Repeat("a", 200)+"\n", maxDiff)
	now := strings.Repeat(strings.Repeat("b", 200)+"\n", maxDiff)
	c := Comment(Side{Label: "main", Bare: old, Help: old}, Side{Label: "this pull request", Bare: now, Help: now})
	if n := utf8.RuneCountInString(c); n > 65536 {
		t.Errorf("the comment is %d characters, over GitHub's 65,536", n)
	}
	if !strings.Contains(c, "| 2000 removed, 2000 added |") {
		t.Errorf("the table does not count the changed lines:\n%s", c[:600])
	}
	// 2,500 bytes hold 14 entries of 169 ("- N: ", 160 bytes, the mark, a
	// newline): seven lines gone, each followed by the line in its place.
	if !strings.Contains(c, "+ 7: "+strings.Repeat("b", 160)+"…\n(3986 more changed lines; both screens are whole below)\n```") || strings.Contains(c, "- 8: ") {
		t.Errorf("the list of changed lines is not cut to its size:\n%s", c[:3600])
	}
}

func TestChangedLinesStopAtTheLineLimit(t *testing.T) {
	var old, now strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&old, "old %d\n", i)
		fmt.Fprintf(&now, "new %d\n", i)
	}
	c := Comment(Side{Label: "main", Bare: home, Help: old.String()}, Side{Label: "this pull request", Bare: home, Help: now.String()})
	// 30 gone and 30 new, each gone line followed by the one in its place:
	// the 40 listed are the first 20 of each.
	if !strings.Contains(c, "```text\n- 1: old 0\n+ 1: new 0\n- 2: old 1\n") || !strings.Contains(c, "- 20: old 19\n+ 20: new 19\n(20 more changed lines; both screens are whole below)\n") {
		t.Errorf("the list does not stop at its 40 lines:\n%s", c)
	}
}

func TestChanges(t *testing.T) {
	for _, c := range []struct {
		name, before, after string
		gone, added         []int
	}{
		{"the same screen", "a\nb\n", "a\nb\n", nil, nil},
		{"two empty screens", "", "", nil, nil},
		{"a line added in the middle", "a\nc\n", "a\nb\nc\n", nil, []int{1}},
		{"a line added at the end", "a\n", "a\nb\n", nil, []int{1}},
		{"a line added at the top", "b\n", "a\nb\n", nil, []int{0}},
		{"a line removed", "a\nb\nc\n", "a\nc\n", []int{1}, nil},
		{"a line reworded", "a\nb\nc\n", "a\nB\nc\n", []int{1}, []int{1}},
		{"a block moved down", "u\nv\n\nh\n", "h\n\nu\nv\n", []int{2, 3}, []int{0, 1}},
		{"a last line without its newline", "a\nb", "a\nb\n", nil, nil},
		{"a blank line added", "a\nb\n", "a\n\nb\n", nil, []int{1}},
		{"nothing before", "", "a\nb\n", nil, []int{0, 1}},
		{"nothing after", "a\nb\n", "", []int{0, 1}, nil},
	} {
		gone, added, ok := changes(c.before, c.after)
		if !ok || fmt.Sprint(gone) != fmt.Sprint(c.gone) || fmt.Sprint(added) != fmt.Sprint(c.added) {
			t.Errorf("%s: gone %v added %v ok %v, want gone %v added %v", c.name, gone, added, ok, c.gone, c.added)
		}
	}
	long := strings.Repeat("x\n", maxDiff+1)
	if _, _, ok := changes(long, "x\n"); ok {
		t.Error("a screen over the line limit was compared")
	}
	if _, _, ok := changes("x\n", long); ok {
		t.Error("a screen over the line limit was compared")
	}
	if _, added, ok := changes(strings.Repeat("x\n", maxDiff), strings.Repeat("x\n", maxDiff-1)+"y\n"); !ok || len(added) != 1 {
		t.Error("a screen at the line limit was not compared")
	}
}

func TestClipKeepsWholeCharacters(t *testing.T) {
	if short := "a short line"; clip(short) != short {
		t.Error("clip changed a short line")
	}
	got := clip("a" + strings.Repeat("é", 100)) // the limit falls inside a character
	if want := "a" + strings.Repeat("é", 79) + "…"; got != want {
		t.Errorf("clip = %q", got)
	}
}

func TestCutOneLongLine(t *testing.T) {
	text := "a" + strings.Repeat("é", maxPane) // no newline, and the limit falls inside a character
	got := cut(text)
	if want := "a" + strings.Repeat("é", maxPane/2-1) + "\n(cut here: 0 more lines, 14002 more bytes)\n"; got != want {
		t.Errorf("cut = …%q", got[len(got)-60:])
	}
	if short := "one line\n"; cut(short) != short {
		t.Error("cut changed a short screen")
	}
}

func TestSizeCountsALastLineWithoutANewline(t *testing.T) {
	for text, want := range map[string]string{"": "none", "a\n": "1 lines, 2 bytes", "a\nb": "2 lines, 3 bytes", "a": "1 lines, 1 bytes"} {
		if got := size(text); got != want {
			t.Errorf("size(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestPageHoldsTheExactText(t *testing.T) {
	before, after := flipped()
	after.Help += "</script><script>alert(1)</script> & \"quoted\" \u2028 ⏎\n"
	after.Label = "PR <14> & co"
	p := Page(before, after)
	start := strings.Index(p, "const out = ")
	end := strings.Index(p, ";\nconst marks = ")
	if start < 0 || end < 0 {
		t.Fatalf("the page has no data:\n%s", p)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(p[start+len("const out = "):end]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"before-bare": before.Bare, "before-help": before.Help, "after-bare": after.Bare, "after-help": after.Help}
	for id, text := range want {
		if got[id] != text {
			t.Errorf("pane %s holds %q, want %q", id, got[id], text)
		}
		if !strings.Contains(p, `<pre id="`+id+`"></pre>`) || !strings.Contains(p, `<span id="n-`+id+`"></span>`) {
			t.Errorf("the page has no pane for %s", id)
		}
	}
	// The lines that changed, counted from zero, for the page to mark: the
	// flip's three gone and three new, the hostile line as one more new line,
	// and empty lists (not null) for the bare screen, which did not change.
	from := strings.Index(p, "const marks = ") + len("const marks = ")
	var marks map[string][]int
	if err := json.Unmarshal([]byte(p[from:from+strings.Index(p[from:], ";\n")]), &marks); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(marks), "map[after-bare:[] after-help:[2 3 4 5] before-bare:[] before-help:[0 1 2]]"; got != want {
		t.Errorf("marks = %s, want %s", got, want)
	}
	if strings.Contains(p[from:from+200], "null") {
		t.Error("a pane with no changed line has null where the page wants a list")
	}
	for _, want := range []string{`.l.gone::before{content:"\2212"}`, `.l.new::before{content:"+"}`, `row.textContent = line;`, `pre.append(row, "\n");`, `"first-" + id`} {
		if !strings.Contains(p, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Count(p, "</script>") != 1 || strings.Contains(p, "alert(1)</script>") {
		t.Error("text in a pane can end the page's script")
	}
	if strings.Count(p, "· PR &lt;14&gt; &amp; co ·") != 2 || strings.Count(p, "· main ·") != 2 {
		t.Errorf("the labels are missing or not escaped:\n%s", p[:1500])
	}
	// The layout the maintainer chose: before on the left and after on the
	// right, in a grid of two columns, with a bare call in the top row.
	if a, b, c, d := strings.Index(p, `id="before-bare"`), strings.Index(p, `id="after-bare"`), strings.Index(p, `id="before-help"`), strings.Index(p, `id="after-help"`); !(a < b && b < c && c < d) {
		t.Error("the panes are out of order")
	}
	if !strings.Contains(p, "grid-template-columns:max-content max-content") {
		t.Error("the grid is not two columns, so before and after are not side by side")
	}
}
