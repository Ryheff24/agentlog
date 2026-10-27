package transcript

import (
	"testing"
)

func TestHumanPromptProvenance(t *testing.T) {
	for _, tc := range []struct {
		text, raw, parent, role, tag string
		human, pasted                bool
		kind                         Kind
	}{
		{"please fix it", `{"origin":{"kind":"human"}}`, "", "", "", true, false, KindUser},
		{"parent task", `{}`, "parent", "", "", false, false, KindUser},
		{"worker task", `{}`, "", "worker", "", false, false, KindUser},
		{"inline task", `{}`, "", "", TagSubagent, false, false, KindUser},
		{"peer text", `{"origin":{"kind":"peer"}}`, "", "", "", false, false, KindUser},
		{"harness text", `{"data":{"origin":"system","isMeta":true}}`, "", "", "", false, false, KindUser},
		{"summary", `{"isCompactSummary":true}`, "", "", "", false, false, KindUser},
		{"quoted transcript", `{"attachment":{"commandMode":"paste"}}`, "", "", "", true, true, KindUser},
		{"[Pasted text #1]", `{}`, "", "", "", true, true, KindUser},
		{"compare [image]", `{"isPaste":true}`, "", "", "", true, true, KindUser},
		{"hello", `not json`, "", "", "", true, false, KindUser},
		{"plain", `{"origin":"human","pasted":true}`, "", "", "", true, true, KindUser},
		{"<cross-session-message from-name=\"peer\">done</cross-session-message>", `{}`, "", "", "", false, false, KindAgent},
		{"Another session sent a message:\n<teammate-message teammate_id=\"peer\">done</teammate-message>", `{}`, "", "", "", false, false, KindAgent},
		{"<task-notification>done</task-notification>", `{}`, "", "", "", false, false, KindNotice},
		{"<command-name>/skill</command-name>", `{}`, "", "", "", false, false, KindNotice},
		{"<local-command-stdout>done</local-command-stdout>", `{}`, "", "", "", false, false, KindNotice},
		{"This session is being continued from a previous conversation: summary", `{}`, "", "", "", false, false, KindNotice},
		{"<compaction-summary>summary</compaction-summary>", `{}`, "", "", "", false, false, KindNotice},
		{"[Request interrupted by user]", `{}`, "", "", "", false, false, KindNotice},
		{"please explain <task-notification>", `{}`, "", "", "", true, false, KindUser},
	} {
		e := Event{Kind: KindUser, Text: tc.text, Tag: tc.tag}
		ClassifyPrompt(&e, &Session{ParentID: tc.parent, AgentRole: tc.role})
		PromptHints(&e, []byte(tc.raw))
		if e.Human != tc.human || e.Pasted != tc.pasted || e.Kind != tc.kind {
			t.Errorf("%q: %+v", tc.text, e)
		}
	}
	e := Event{Kind: KindTool}
	ClassifyPrompt(&e, &Session{})
	PromptHints(&e, []byte(`{}`))
	if e.Human {
		t.Fatal("tool is human")
	}
}

func TestHistoricalOutputProvenance(t *testing.T) {
	tr := &Transcript{Events: []Event{{Kind: KindTool, Input: "agentlog abc#1"}, {Kind: KindTool, Input: "/tmp/bin/agentlog abc"}, {Kind: KindTool, Input: "echo agentlog"}, {Kind: KindAgent, Tag: "message"}, {Kind: KindAgent, Tool: "Agent", Output: "Async agent launched successfully."}, {Kind: KindAgent, Tool: "wait_agent", Output: "handback"}}}
	tr.MarkHandbacks()
	for i, want := range []bool{true, true, false, false, false, false} {
		if tr.Events[i].Handback != want {
			t.Errorf("event %d: %+v", i, tr.Events[i])
		}
	}
}
