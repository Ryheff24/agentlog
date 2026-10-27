package app

import (
	"testing"

	"agentlog/internal/catalog"
	"agentlog/internal/transcript"
)

// A clue that narrows the field is not a clue that names a session: opening
// the newest session in a directory is what the reader asked for, opening one
// of four that matched a word equally well is a guess.
func TestNamesDistinguishesNamingCluesFromNarrowingOnes(t *testing.T) {
	tests := []struct {
		name string
		cl   catalog.Clues
		want bool
	}{
		{"a word", catalog.Clues{Words: []string{"fullscreen"}}, true},
		{"an id prefix", catalog.Clues{IDs: []string{"01a0"}}, true},
		{"a directory", catalog.Clues{Dirs: []string{"/w/shop"}}, false},
		{"a harness", catalog.Clues{Providers: []string{"codex"}}, false},
		{"nothing", catalog.Clues{}, false},
	}
	for _, tt := range tests {
		if got := names(tt.cl); got != tt.want {
			t.Errorf("names(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The same file lives at different absolute paths in every worktree, and a
// lookup that only matched the exact path would miss the session that did the
// work. Matching from the right finds it; matching on the basename alone would
// answer a question about every config.ts in the corpus.
func TestMatchesPathAcrossCheckouts(t *testing.T) {
	asked := []string{"/w/shop/src/internal/analytics/reportdata.go"}
	tests := []struct {
		path string
		want bool
	}{
		{"/w/shop/src/internal/analytics/reportdata.go", true},
		{"/w/shop-raids/src/internal/analytics/reportdata.go", true}, // another worktree
		{"/elsewhere/analytics/reportdata.go", true},               // same dir and file
		{"/w/shop/src/internal/reportdata.go", false},                // different directory
		{"/w/shop/src/internal/analytics/reportdata_test.go", false}, // different file
		{"/w/shop/src/internal/analytics/reportdata.go.orig", false}, //
		{"", false},
	}
	for _, tt := range tests {
		if got := matchesPath(tt.path, asked); got != tt.want {
			t.Errorf("matchesPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// A session that edited the file in another checkout has answered the
// question, but not about the path the reader typed, and the difference has to
// survive into the answer.
func TestUsesOfSeparatesChangesFromMentions(t *testing.T) {
	tr := &transcript.Transcript{Session: transcript.Session{ID: "cbfe7aac"}}
	tr.Events = []transcript.Event{
		{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "/w/shop-raids/src/analytics/reportdata.go"}}},
		{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "/w/shop/src/analytics/reportdata.go"}}},
		{Kind: transcript.KindTool, Tool: "Bash", Input: "go test ./src/analytics/reportdata.go"},
		{Kind: transcript.KindAssistant, Text: "unrelated"},
		{Kind: transcript.KindEdit, Changes: []transcript.Change{{Path: "/w/shop/src/analytics/other.go"}}},
	}
	tr.Number()

	u := usesOf(tr, []string{"/w/shop/src/analytics/reportdata.go"})
	if u.Edits != 2 || u.Reads != 1 {
		t.Errorf("edits %d, mentions %d; want 2 and 1", u.Edits, u.Reads)
	}
	if len(u.Refs) != 2 || u.Refs[0] != "#1" || u.Refs[1] != "#2" {
		t.Errorf("refs = %v, want the two edits", u.Refs)
	}
	if len(u.Paths) != 1 || u.Paths[0] != "/w/shop-raids/src/analytics/reportdata.go" {
		t.Errorf("Paths = %v, want only the checkout that was not asked about", u.Paths)
	}
	if got := u.What(); got != "2 changes, 1 mention" {
		t.Errorf("What() = %q", got)
	}
}
