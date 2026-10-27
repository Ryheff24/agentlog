package engine

import (
	"regexp"
	"strings"
	"testing"

	"agentlog/internal/transcript"
)

func transcriptOf(events ...transcript.Event) *transcript.Transcript {
	t := &transcript.Transcript{Session: transcript.Session{Provider: transcript.Codex, ID: "01a0a5c3-7e10-7d24-9f31-6b28c40e1a95"}}
	t.Events = events
	t.Number()
	return t
}

func ev(k transcript.Kind, text string) transcript.Event {
	return transcript.Event{Kind: k, Text: text}
}

func indices(sel Selection) []int {
	out := make([]int, 0, len(sel.Events))
	for _, e := range sel.Events {
		out = append(out, e.Index)
	}
	return out
}

func TestSelectKindsAndReasoning(t *testing.T) {
	tr := transcriptOf(
		ev(transcript.KindUser, "fix it"),
		ev(transcript.KindReasoning, "thinking"),
		ev(transcript.KindAssistant, "done"),
	)
	if got := indices(Select(tr, Filter{})); len(got) != 2 || got[1] != 3 {
		t.Errorf("default = %v, want reasoning left out", got)
	}
	if got := indices(Select(tr, Filter{Reasoning: true})); len(got) != 3 {
		t.Errorf("--reasoning = %v, want all three", got)
	}
	f := Filter{Kinds: map[transcript.Kind]bool{transcript.KindReasoning: true}}
	if got := indices(Select(tr, f)); len(got) != 1 || got[0] != 2 {
		t.Errorf("--kind reasoning = %v, want just the reasoning", got)
	}
}

// The counts are the point of a Selection: an agent that sees 2 of 5 events
// needs to know whether the other 3 failed the filter or fell off the page.
func TestSelectSeparatesFilteredFromPaged(t *testing.T) {
	tr := transcriptOf(
		ev(transcript.KindUser, "one scroll"),
		ev(transcript.KindAssistant, "two"),
		ev(transcript.KindUser, "three scroll"),
		ev(transcript.KindUser, "four scroll"),
		ev(transcript.KindUser, "five scroll"),
	)
	sel := Select(tr, Filter{Grep: regexp.MustCompile("scroll"), Tail: 2})
	if got, want := indices(sel), []int{4, 5}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if sel.Total != 5 || sel.Matched != 4 || sel.Dropped() != 2 {
		t.Errorf("total %d matched %d dropped %d, want 5/4/2", sel.Total, sel.Matched, sel.Dropped())
	}
	if from, to := sel.Range(); from != 4 || to != 5 {
		t.Errorf("range = %d-%d, want 4-5", from, to)
	}
	if !sel.Any() {
		t.Error("Any() = false")
	}
}

// A person searching for a sentence types it with spaces; the log wrapped it
// wherever their terminal did. Before this, "UPDATE issue SET title" found
// nothing in a corpus that contained it eight times.
func TestSelectMatchesAcrossLineBreaks(t *testing.T) {
	tr := transcriptOf(
		transcript.Event{Kind: transcript.KindUser, Text: "UPDATE issue\nSET title = 'x'"},
		transcript.Event{Kind: transcript.KindTool, Tool: "bash", Output: "failed:\n\n   2 of 30\ttests"},
		transcript.Event{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "a.sql", Diff: "+UPDATE\nissue"}}},
		transcript.Event{Kind: transcript.KindAssistant, Text: "unrelated"},
	)
	for _, pattern := range []string{`(?i)UPDATE issue SET title`, `(?i)failed: 2 of 30 tests`, `(?i)\+UPDATE issue`} {
		sel := Select(tr, Filter{Grep: regexp.MustCompile(pattern)})
		if len(sel.Events) != 1 {
			t.Errorf("%q matched %d events, want 1", pattern, len(sel.Events))
		}
	}
	// The snippet has to come back folded too, or it cannot be read as one.
	sel := Select(tr, Filter{Grep: regexp.MustCompile(`(?i)UPDATE issue SET`)})
	if len(sel.Snippets) == 0 {
		t.Fatal("no snippet for a match")
	}
	for _, ss := range sel.Snippets {
		if !strings.Contains(ss[0], "UPDATE issue SET") {
			t.Errorf("snippet = %q, want the folded sentence", ss[0])
		}
	}
}

func TestFold(t *testing.T) {
	tests := []struct{ in, want string }{
		{"already fine", "already fine"},
		{"a\nb", "a b"},
		{"a \n\t b", "a b"},
		{"  leading", "leading"},
		{"trailing  ", "trailing"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := fold(tt.in); got != tt.want {
			t.Errorf("fold(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSelectMatchesEveryFieldAnEventHas(t *testing.T) {
	tr := transcriptOf(
		transcript.Event{Kind: transcript.KindTool, Tool: "rg", Input: "pgvector"},
		transcript.Event{Kind: transcript.KindTool, Tool: "bash", Output: "pgvector installed"},
		transcript.Event{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "db/pgvector.sql", Op: "add"}}},
		transcript.Event{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "db/schema.sql", Diff: "+CREATE EXTENSION pgvector"}}},
		transcript.Event{Kind: transcript.KindAgent, Agent: "pgvector-migration"},
		transcript.Event{Kind: transcript.KindAssistant, Text: "unrelated"},
	)
	sel := Select(tr, Filter{Grep: regexp.MustCompile("(?i)pgvector"), Reasoning: true})
	if got, want := len(sel.Events), 5; got != want {
		t.Errorf("matched %d events, want %d: %v", got, want, indices(sel))
	}
}

// --path narrows to the files a change touched, which is not the same question
// as --grep: a diff body that mentions a path must not pull the event in.
func TestSelectPathLooksAtPathsOnly(t *testing.T) {
	tr := transcriptOf(
		transcript.Event{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "src/player.ts"}}},
		transcript.Event{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "docs/readme.md", Diff: "see src/player.ts"}}},
	)
	sel := Select(tr, Filter{Path: regexp.MustCompile(`player\.ts`)})
	if got := indices(sel); len(got) != 1 || got[0] != 1 {
		t.Errorf("events = %v, want only the edit to that file", got)
	}
}

func TestSnippetsQuoteWhatMatched(t *testing.T) {
	e := transcript.Event{Kind: transcript.KindTool,
		Output: strings.Repeat("x", 200) + "the pgvector extension is missing" + strings.Repeat("y", 200)}
	got := Snippets(regexp.MustCompile("(?i)pgvector"), e, 2)
	if len(got) != 1 {
		t.Fatalf("snippets = %v, want one", got)
	}
	if !strings.Contains(got[0], "pgvector extension is missing") {
		t.Errorf("snippet = %q, want the text around the match", got[0])
	}
	if len(got[0]) > 2*contextBytes+40 {
		t.Errorf("snippet is %d bytes, want about %d", len(got[0]), 2*contextBytes)
	}
}

func TestResponseClipsLongFieldsWithoutDroppingEvents(t *testing.T) {
	tr := transcriptOf(
		transcript.Event{Kind: transcript.KindTool, Tool: "bash", Output: strings.Repeat("a", 50_000)},
		transcript.Event{Kind: transcript.KindTool, Tool: "bash", Output: strings.Repeat("b", 50_000)},
		transcript.Event{Kind: transcript.KindAssistant, Text: "done"},
	)
	r := FromSelection(Select(tr, Filter{}), "01a0a5c3-7e10", "test", tr)
	r.ClipLongFields(2000)

	if len(r.Truncated) == 0 {
		t.Fatal("nothing recorded as truncated")
	}
	for _, cut := range r.Truncated {
		if cut.What == "" || cut.Get == "" {
			t.Errorf("cut %+v does not say what was lost or how to get it", cut)
		}
		if !strings.Contains(cut.Get, "01a0a5c3-7e10") {
			t.Errorf("cut command %q does not name the session", cut.Get)
		}
	}
	if len(r.Events) != 3 {
		t.Errorf("events = %v, want every event kept", indices(Selection{Events: r.Events}))
	}
	if r.Counts.Shown != len(r.Events) || r.Counts.Total != 3 {
		t.Errorf("counts = %+v, want them to match what survived", r.Counts)
	}
}

// A census that counted only the events still labelled "tool" left out every
// subagent spawn, plan write and agent message, which are tool calls that the
// parser gives a more useful kind. Against 14 sessions of the corpus that was
// the difference between agreeing with the raw logs and not.
func TestCensusCountsEveryToolCallWhateverKindItBecame(t *testing.T) {
	tr := transcriptOf(
		transcript.Event{Kind: transcript.KindTool, Tool: "Bash", Output: "one"},
		transcript.Event{Kind: transcript.KindTool, Tool: "Bash", Output: "two!", Failed: true},
		transcript.Event{Kind: transcript.KindAgent, Tool: "Agent", Agent: "reviewer"},
		transcript.Event{Kind: transcript.KindPlan, Tool: "TodoWrite"},
		transcript.Event{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "a.go"}}},
		transcript.Event{Kind: transcript.KindAssistant, Text: "not a tool call"},
	)
	got := Census(tr)
	want := []ToolUse{
		{Tool: "Bash", Calls: 2, Failed: 1, Bytes: 7, First: 1, Last: 2},
		{Tool: "Agent", Calls: 1, First: 3, Last: 3},
		{Tool: "TodoWrite", Calls: 1, First: 4, Last: 4},
		{Tool: "edit", Calls: 1, First: 5, Last: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("census = %+v, want %d tools", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("census[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSelectionLeavesRepeatedCopiesOut(t *testing.T) {
	tr := transcriptOf(ev(transcript.KindUser, "original"), ev(transcript.KindUser, "copy"))
	tr.Events[1].RepeatOf = 1
	if got := Select(tr, Filter{}); got.Matched != 1 {
		t.Fatal(got)
	}
}

func TestDisabledJSONFieldClip(t *testing.T) {
	r := &Response{Events: []transcript.Event{{Text: strings.Repeat("x", 10000)}}}
	r.ClipLongFields(0)
	if len(r.Events[0].Text) != 10000 || len(r.Truncated) != 0 {
		t.Fatal(r)
	}
}
