package engine

import (
	"sort"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// ToolUse is one tool's share of a session: how often it was called, how often
// that failed, and how much output it poured into the context. The last is the
// one no other view shows, and it is usually where a session's tokens went.
type ToolUse struct {
	Tool   string `json:"tool"`
	Calls  int    `json:"calls"`
	Failed int    `json:"failed,omitempty"`
	Bytes  int    `json:"output_bytes,omitempty"`
	First  int    `json:"first_event,omitempty"`
	Last   int    `json:"last_event,omitempty"`
}

// Census counts tool use across transcripts, busiest first. Ties break on the
// name so two runs of the same corpus agree.
func Census(ts ...*transcript.Transcript) []ToolUse {
	by := map[string]*ToolUse{}
	for _, t := range ts {
		for _, e := range t.Events {
			// Every event a tool call produced, whatever kind the parser gave
			// it: spawning a subagent, writing a plan and messaging another
			// agent are all tool calls, and a census that counted only the
			// ones still labelled "tool" would quietly leave them out.
			name := e.Tool
			if name == "" {
				if e.Kind != transcript.KindEdit {
					continue
				}
				name = string(e.Kind)
			}
			u, ok := by[name]
			if !ok {
				u = &ToolUse{Tool: name, First: e.Index}
				by[name] = u
			}
			u.Calls++
			u.Bytes += len(e.Output)
			u.Last = e.Index
			if e.Failed {
				u.Failed++
			}
		}
	}
	out := make([]ToolUse, 0, len(by))
	for _, u := range by {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}
