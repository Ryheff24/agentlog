package engine

import (
	"agentlog/internal/transcript"
	"fmt"
	"strings"
)

// Provenance describes recorded evidence, never the current checkout.
func Provenance(t *transcript.Transcript) string {
	last, sha, pushed, edits := 0, "", false, 0
	status := "working tree unknown (no logged status snapshot)"
	for _, e := range t.Events {
		for _, op := range e.Git {
			if op.Action == "committed" && op.SHA != "" {
				last, sha, pushed, edits = e.Index, op.SHA, false, 0
			}
			if op.Action == "pushed" && last > 0 {
				pushed = true
			}
		}
		if last > 0 && e.Index > last && len(e.Changes) > 0 && !e.Failed {
			edits++
		}
		if !e.Failed && strings.HasPrefix(strings.TrimSpace(e.Input), "git status") && e.Output != "" {
			status = "last logged git status: " + strings.Join(strings.Fields(e.Output), " ")
			if len(status) > 180 {
				status = status[:180] + "…"
			}
		}
	}
	if sha == "" {
		return "no commit recorded; " + status
	}
	push := "no later push recorded"
	if pushed {
		push = "later push recorded"
	}
	return fmt.Sprintf("last recorded commit %s #%d; %s; %d later edit events; %s", sha, last, push, edits, status)
}
