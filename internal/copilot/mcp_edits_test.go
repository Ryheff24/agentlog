package copilot

import (
	"strings"
	"testing"
)

func TestMCPFilesystemChanges(t *testing.T) {
	home := t.TempDir()
	records := []map[string]any{
		{"type": "session.start", "data": map[string]any{"context": map[string]any{"cwd": "/work/app"}}},
		{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "write", "toolName": "filesystem-write_file", "arguments": map[string]any{"path": "a.go", "content": "one\ntwo\n"}}},
		{"type": "tool.execution_complete", "data": map[string]any{"toolCallId": "write", "success": true, "result": map[string]any{"content": "written"}}},
		{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "edit", "toolName": "filesystem-edit_file", "arguments": map[string]any{"path": "a.go", "edits": []any{map[string]any{"oldText": "one", "newText": "three"}, map[string]any{"oldText": "two", "newText": "four"}}}}},
		{"type": "tool.execution_complete", "data": map[string]any{"toolCallId": "edit", "success": false, "result": map[string]any{"content": "not applied"}}},
		{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "preview", "toolName": "filesystem-edit_file", "arguments": map[string]any{"path": "a.go", "dryRun": true, "edits": []any{map[string]any{"oldText": "one", "newText": "preview"}}}}},
	}
	tr := load(t, home, writeEvents(t, home, records))
	if got := kinds(tr); got != "edit edit tool" {
		t.Fatal(got)
	}
	if tr.Events[0].Changes[0].Path != "/work/app/a.go" || tr.Events[0].Changes[0].Added != 2 {
		t.Fatal(tr.Events[0])
	}
	if len(tr.Events[1].Changes) != 2 || !tr.Events[1].Failed || !strings.Contains(tr.Events[1].Changes[1].Diff, "+four") {
		t.Fatal(tr.Events[1])
	}
	if len(tr.Events[2].Changes) != 0 {
		t.Fatal("dry-run counted as a writer")
	}
}
