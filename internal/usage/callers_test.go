package usage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDeduplicatedCallersAndDevelopment(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"original", "copied"} {
		writeLines(t, filepath.Join(root, name+".jsonl"), claudeUse(0, "same-call", "agentlog abc"), claudeResult(1, "same-call", "answer"))
	}
	dev := claudeUse(2, "development-call", "agentlog doctor")
	dev["cwd"] = "/work/dashboard/agentlog"
	writeLines(t, filepath.Join(root, "dev.jsonl"), dev, claudeResult(3, "development-call", "read"))
	edit := claudeUse(4, "edited-call", "agentlog --usage")
	writeLines(t, filepath.Join(root, "edit.jsonl"), edit, claudeResult(5, "edited-call", "read"), map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": "github.com/Ryheff24/agentlog/internal/a.go"}}}}})
	cs, ss := Scan(root, "", time.Time{})
	if len(cs) != 3 {
		t.Fatalf("dedup: %+v", cs)
	}
	cs, ss = Callers(cs, ss)
	if len(cs) != 2 || len(ss) != 2 || cs[0].ToolID != "same-call" {
		t.Fatalf("callers: %+v %+v", cs, ss)
	}
}

func TestForkReplayAndEditClassification(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "fork.jsonl")
	writeLines(t, p, map[string]any{"type": "session_meta", "timestamp": at(10), "payload": map[string]any{"id": "fork", "cwd": "/work/app", "timestamp": at(10), "forked_from_id": "parent"}},
		map[string]any{"type": "response_item", "timestamp": at(10), "payload": map[string]any{"type": "function_call", "call_id": "copied", "arguments": `{"cmd":"agentlog abc"}`}},
		map[string]any{"type": "response_item", "timestamp": at(11), "payload": map[string]any{"type": "function_call_output", "call_id": "copied", "output": "answer"}},
		map[string]any{"type": "response_item", "timestamp": at(12), "payload": map[string]any{"type": "function_call", "call_id": "new", "arguments": `{"cmd":"agentlog abc"}`}},
		map[string]any{"type": "response_item", "timestamp": at(13), "payload": map[string]any{"type": "function_call_output", "call_id": "new", "output": "answer"}})
	cs, _ := Scan("", root, time.Time{})
	if len(cs) != 1 || cs[0].ToolID != "new" {
		t.Fatalf("%+v", cs)
	}
	inside := func(p string) bool { return p == "/work/agentlog/a.go" }
	for _, v := range []any{map[string]any{"path": "agentlog/a.go"}, []any{map[string]any{"file_path": "/work/agentlog/a.go"}}, "*** Update File: agentlog/a.go"} {
		if !editsAgentlog(v, "/work", inside) {
			t.Errorf("not development: %v", v)
		}
	}
	if editsAgentlog(map[string]any{"file_path": "elsewhere.go"}, "/work", inside) {
		t.Fatal("unrelated edit")
	}
}
