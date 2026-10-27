package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/render"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// A range with --kind or --grep shows the events in it that match, not every
// event in it.
func TestRangeKeepsOnlyWhatTheFilterAsks(t *testing.T) {
	tr := &transcript.Transcript{
		Session: transcript.Session{Provider: "claude", ID: "6c2e9f40-1d7a-4b3e-8a55-2f9e0d4c7b18"},
		Events: []transcript.Event{
			{Index: 1, Kind: transcript.KindUser, Text: "ask one"},
			{Index: 2, Kind: transcript.KindReasoning, Text: "thinking about the ask"},
			{Index: 3, Kind: transcript.KindTool, Tool: "Bash", Input: "ls"},
			{Index: 4, Kind: transcript.KindUser, Text: "ask two"},
		},
	}
	for _, tc := range []struct {
		name       string
		opts       options
		want, drop []string
	}{
		{"kind", options{kinds: "user"}, []string{"#1 user", "#4 user"}, []string{"#2 ", "#3 "}},
		{"grep keeps reasoning", options{grep: "about the ask"}, []string{"#2 reasoning"}, []string{"#1 ", "#3 ", "#4 "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := filter(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			f.Reasoning = true
			var out bytes.Buffer
			render.Events(&out, tr, 1, 4, render.Options{Keep: f.Keeps})
			for _, s := range tc.want {
				if !strings.Contains(out.String(), s) {
					t.Errorf("missing %q in\n%s", s, out.String())
				}
			}
			for _, s := range tc.drop {
				if got := "\n" + out.String(); strings.Contains(got, "\n"+s) {
					t.Errorf("kept %q in\n%s", s, out.String())
				}
			}
		})
	}
}
