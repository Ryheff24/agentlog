package app

import (
	"agentlog/internal/engine"
	"encoding/json"
	"testing"
)

// Real atools-a has its parent's CLAUDE_CODE_SESSION_ID, while the log's
// agentId is distinct and absent from env. CHILD_SESSION also occurs in
// top-level callers and cannot safely identify a subagent.
func TestInheritedClaudeIdentityMustNotHideParent(t *testing.T) {
	t.Skip("ISSUES.md: inherited identity reproduced; needs an owner-approved child identity signal or inference rule")
	w := newWorld(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", checkoutID)
	out, code := w.run("/work/shop", "claude", ".", "all", "--list", "--json")
	if code != 0 {
		t.Fatal(out)
	}
	var r engine.Response
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Sessions {
		if s.ID == checkoutID {
			return
		}
	}
	t.Fatal("a subagent's inherited parent identity hides the parent session")
}
