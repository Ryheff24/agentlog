package engine

import (
	"regexp"
	"strings"
	"testing"

	"agentlog/internal/transcript"
)

// The drift footer is how a reader learns it is not looking at the whole
// session. Counts add up across a session and its subagents, the commonest
// types are named first, and a long tail is summarised rather than listed.
func TestDrift(t *testing.T) {
	clean := transcriptOf()
	if got := Drift(clean, transcriptOf()); got != "" {
		t.Errorf("Drift(clean) = %q, want nothing", got)
	}
	one := transcriptOf()
	one.Unknowns = map[string]int{"relocated": 1}
	if got := Drift(one); !strings.HasPrefix(got, "note: 1 record of 1 type this build cannot read") || !strings.Contains(got, "(relocated ×1)") {
		t.Errorf("Drift(one) = %q", got)
	}
	main, sub := transcriptOf(), transcriptOf()
	main.Unknowns = map[string]int{"a": 5, "b": 2, "c": 2, "d": 1}
	sub.Unknowns = map[string]int{"a": 1, "e": 1, "f": 9}
	got := Drift(main, clean, sub)
	for _, want := range []string{"21 records of 6 types", "(f ×9, a ×6, b ×2, c ×2, +2 more)", "agentlog doctor"} {
		if !strings.Contains(got, want) {
			t.Errorf("Drift() = %q, want it to contain %q", got, want)
		}
	}
}

func TestUnrecognisedOrderAndLimit(t *testing.T) {
	types := map[string]int{"x": 3, "b": 1, "a": 1, "y": 3}
	tests := []struct {
		max  int
		want string
	}{
		{4, "x ×3, y ×3, a ×1, b ×1"}, // ties by name, so the line is stable
		{5, "x ×3, y ×3, a ×1, b ×1"},
		{2, "x ×3, y ×3, +2 more"},
		{0, "+4 more"},
	}
	for _, tt := range tests {
		if got := strings.Join(Unrecognised(types, tt.max), ", "); got != tt.want {
			t.Errorf("Unrecognised(max %d) = %q, want %q", tt.max, got, tt.want)
		}
	}
	if got := Unrecognised(nil, 4); len(got) != 0 {
		t.Errorf("Unrecognised(nil) = %v", got)
	}
}

// What a session landed: commits with a sha, in order, across a session and
// its subagents. A commit that failed (no sha) did not land; a push is
// listed separately, since whether it left the machine is its own question.
func TestCommitsAndPushes(t *testing.T) {
	main := transcriptOf(
		transcript.Event{Kind: transcript.KindTool, Git: []transcript.GitOp{{Action: "committed", SHA: "abc1234", Branch: "main"}}},
		transcript.Event{Kind: transcript.KindTool, Git: []transcript.GitOp{{Action: "committed"}}}, // hook rejected it
		transcript.Event{Kind: transcript.KindTool, Git: []transcript.GitOp{
			{Action: "committed", SHA: "def5678"}, {Action: "pushed", Branch: "main"},
		}},
	)
	sub := transcriptOf(transcript.Event{Kind: transcript.KindTool, Git: []transcript.GitOp{{Action: "committed", SHA: "0123abc"}}})

	var got []string
	for _, c := range Commits(main, sub) {
		got = append(got, c.SHA+"@"+string(rune('0'+c.Event)))
	}
	if strings.Join(got, " ") != "abc1234@1 def5678@3 0123abc@1" {
		t.Errorf("Commits() = %v", got)
	}
	p := Pushes(main, sub)
	if len(p) != 1 || p[0].Event != 3 || p[0].Branch != "main" {
		t.Errorf("Pushes() = %+v, want the one push at #3", p)
	}
	if Commits() != nil || Pushes(transcriptOf()) != nil {
		t.Error("no transcripts, or no git, should land nothing")
	}
}

// Keeps is the per-event test a range view uses: the filter's kinds and
// pattern, with no head or tail, since a range is already the page.
func TestKeepsIgnoresThePage(t *testing.T) {
	f := Filter{Grep: regexp.MustCompile("scroll"), Head: 1}
	events := []transcript.Event{ev(transcript.KindUser, "scroll one"), ev(transcript.KindUser, "scroll two"), ev(transcript.KindUser, "other")}
	n := 0
	for _, e := range events {
		if f.Keeps(e) {
			n++
		}
	}
	if n != 2 {
		t.Errorf("Keeps passed %d, want 2 (both matches, whatever Head says)", n)
	}
}

// When a page cuts matching events, the response says how to get them. With
// --tail the missing events come before the page; with --head, after it.
// Pointing a --head reader at #1 hands back the page they already have.
func TestResponseSaysWhereTheCutEventsAre(t *testing.T) {
	var events []transcript.Event
	for range 6 {
		events = append(events, ev(transcript.KindUser, "scroll"))
	}
	tr := transcriptOf(events...)
	tests := []struct {
		f    Filter
		want string
	}{
		{Filter{Tail: 2}, "agentlog ref#1-4 --tail 2"},
		{Filter{Head: 2}, "agentlog ref#3- --head 2"},
	}
	for _, tt := range tests {
		r := FromSelection(Select(tr, tt.f), "ref", "", tr)
		if len(r.Truncated) != 1 || r.Truncated[0].Get != tt.want || r.Truncated[0].Count != 4 {
			t.Errorf("head %d tail %d: cuts = %+v, want 4 at %q", tt.f.Head, tt.f.Tail, r.Truncated, tt.want)
		}
	}
	// Nothing matched: no page, so nothing to continue to, and the empty
	// range is 0-0 rather than an index out of range.
	sel := Select(tr, Filter{Grep: regexp.MustCompile("absent")})
	if from, to := sel.Range(); from != 0 || to != 0 {
		t.Errorf("Range() of nothing = %d-%d", from, to)
	}
	if r := FromSelection(sel, "ref", "absent", tr); len(r.Next) != 0 || len(r.Truncated) != 0 {
		t.Errorf("empty response = next %v cuts %v", r.Next, r.Truncated)
	}
}

// A drift line travels with a JSON response too, after the session's notes.
func TestResponseCarriesDrift(t *testing.T) {
	tr := transcriptOf(ev(transcript.KindUser, "hi"))
	tr.Notes = []string{"compacted at #1"}
	tr.Unknowns = map[string]int{"relocated": 2}
	r := FromSelection(Select(tr, Filter{}), "ref", "", tr)
	if len(r.Notices) != 2 || r.Notices[0] != "compacted at #1" || !strings.Contains(r.Notices[1], "relocated ×2") {
		t.Errorf("notices = %q", r.Notices)
	}
	// Without a ref there is no command to give for clipped fields.
	r = &Response{Events: []transcript.Event{{Output: strings.Repeat("x", 3*2000)}}}
	r.ClipLongFields(2000)
	if len(r.Truncated) != 1 || r.Truncated[0].Get != "" {
		t.Errorf("cuts = %+v, want one with no command", r.Truncated)
	}
}
