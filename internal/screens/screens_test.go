package screens

import (
	"encoding/json"
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
		"| screen | main | this pull request |",
		"| `agentlog` | 5 lines, 102 bytes | unchanged |",
		"| `agentlog --help` | 5 lines, 102 bytes | 5 lines, 102 bytes |",
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
	if !strings.Contains(c, "| `agentlog` | 5 lines, 102 bytes | 6 lines, 116 bytes |") {
		t.Errorf("the table lacks the bare screen's new size:\n%s", c)
	}
}

// The pull request that first pins the screens has nothing before it, and one
// that deletes a pinned file has nothing after.
func TestCommentWithAScreenMissingOnOneSide(t *testing.T) {
	c := Comment(Side{Label: "main"}, Side{Label: "this pull request", Bare: home, Help: home})
	for _, want := range []string{
		"| `agentlog` | none | 5 lines, 102 bytes |",
		"<details><summary><code>agentlog</code>, main (none)</summary>\n\nNo such screen is pinned on this side.\n\n</details>",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("the comment lacks %q:\n%s", want, c)
		}
	}
	if strings.Contains(c, "Now they differ") {
		t.Errorf("two empty screens are not two commands that printed the same screen:\n%s", c)
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
	start := strings.Index(c, open)
	if start < 0 {
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
	end := strings.Index(p, ";\nfor (const")
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
