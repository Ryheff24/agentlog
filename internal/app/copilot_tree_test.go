package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCopilotTreeShowsInlineRunsWithEventRefs(t *testing.T) {
	w := bareWorld(t)
	w.file(filepath.Join(w.root, "copilot", "session-state", copilotID, "events.jsonl"), time.Hour, []map[string]any{
		{"type": "session.start", "data": map[string]any{"context": map[string]any{"cwd": "/work/tree"}}},
		{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "task1", "toolName": "task", "arguments": map[string]any{"agent_type": "research", "prompt": "look"}}},
		{"type": "subagent.started", "data": map[string]any{"toolCallId": "task1", "agentDisplayName": "Research Agent"}},
		{"type": "tool.execution_start", "data": map[string]any{"parentToolCallId": "task1", "toolCallId": "task2", "toolName": "task", "arguments": map[string]any{"prompt": "nested"}}},
	})
	for _, args := range [][]string{{copilotID, "--tree"}, {copilotID, "--tree", "--json"}, {copilotID, "--tree", "--subagents"}} {
		out, code := w.run("/", args...)
		if code != 0 || !strings.Contains(out, "Research Agent") {
			t.Fatal(out)
		}
		if len(args) == 2 && !strings.Contains(out, "c0c0c0c0#1") {
			t.Fatal("missing addressable task ref", out)
		}
		if args[len(args)-1] == "--subagents" && !strings.Contains(out, "c0c0c0c0#2 --subagents") {
			t.Fatal("nested ref loses inline view", out)
		}
	}
}
