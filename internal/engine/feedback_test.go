package engine

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

func TestOwnerRefusalAndHandbackSelectors(t *testing.T) {
	tr := &transcript.Transcript{Events: []transcript.Event{{Kind: transcript.KindUser, Human: true, Text: "needle"}, {Kind: transcript.KindUser, Text: "needle"}, {Kind: transcript.KindError, Tag: "refusal", Text: "needle"}, {Kind: transcript.KindTool, Handback: true, Output: "needle"}}}
	for _, tc := range []struct {
		f     Filter
		count int
	}{{Filter{Kinds: map[transcript.Kind]bool{"owner": true}}, 1}, {Filter{Kinds: map[transcript.Kind]bool{"refusal": true}}, 1}, {Filter{Grep: regexp.MustCompile("needle")}, 3}, {Filter{Grep: regexp.MustCompile("needle"), IncludeHandbacks: true}, 4}} {
		if got := Select(tr, tc.f).Matched; got != tc.count {
			t.Errorf("%+v: %d", tc.f, got)
		}
	}
}

func TestFinalProvenanceUsesRecordedEvidence(t *testing.T) {
	tr := &transcript.Transcript{}
	if !strings.Contains(Provenance(tr), "unknown") {
		t.Fatal(Provenance(tr))
	}
	tr.Events = []transcript.Event{{Index: 1, Git: []transcript.GitOp{{Action: "committed", SHA: "abc"}}}, {Index: 2, Git: []transcript.GitOp{{Action: "pushed"}}}, {Index: 3, Changes: []transcript.Change{{Path: "a"}}}, {Index: 4, Input: "git status", Output: strings.Repeat(" M file\n", 100)}}
	got := Provenance(tr)
	for _, s := range []string{"abc #1", "later push recorded", "1 later edit", "last logged git status"} {
		if !strings.Contains(got, s) {
			t.Error(got)
		}
	}
	tr.Events = tr.Events[:1]
	if !strings.Contains(Provenance(tr), "no later push") {
		t.Fatal(Provenance(tr))
	}
}
