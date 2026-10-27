package claude

import (
	"agentlog/internal/transcript"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Whole histories are replayed with restamped cwd/version/branch metadata.
// UUID and timestamp identify the original record, including multi-block calls.
func TestRepeatedHistoryKeepsRefsAndFirstContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	first := withUUID(user("original prompt"), "u", "")
	first["timestamp"] = t0
	call := withUUID(assistant(map[string]any{"type": "text", "text": "original answer"}, toolUse("t", "Bash", map[string]any{"command": "pwd"})), "a", "u")
	call["timestamp"] = t0.Add(time.Second)
	result := withUUID(toolResult("t", "first output", nil), "r", "a")
	result["timestamp"] = t0.Add(2 * time.Second)
	copyPrompt := withUUID(user("original prompt"), "u", "")
	copyPrompt["timestamp"], copyPrompt["cwd"], copyPrompt["gitBranch"] = t0, "/work/new", "new-branch"
	copyCall := withUUID(assistant(map[string]any{"type": "text", "text": "changed copy answer"}, toolUse("t", "Bash", map[string]any{"command": "pwd"})), "a", "u")
	copyCall["timestamp"] = call["timestamp"]
	copyResult := withUUID(toolResult("t", "copy output", nil), "r", "a")
	copyResult["timestamp"] = result["timestamp"]
	later := withUUID(user("new timestamp is not a repeat"), "u", "r")
	later["timestamp"] = t0.Add(3 * time.Second)
	writeJSONL(t, path, []map[string]any{first, call, result, copyPrompt, copyCall, copyResult, later, user("no uuid"), user("no uuid")})
	s := &transcript.Session{ID: sessionID, Files: []string{path}}
	tr, err := (&Provider{}).Load(s, transcript.Load{Branches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Events) != 6 {
		t.Fatalf("visible events = %d, want 6", len(tr.Events))
	}
	if !strings.Contains(strings.Join(tr.Notes, " "), "3 repeated records hidden") {
		t.Fatal(tr.Notes)
	}
	want := []string{"original prompt", "original answer", "", "original prompt", "changed copy answer", "", "new timestamp is not a repeat", "no uuid", "no uuid"}
	for i, text := range want {
		evs := tr.Range(i+1, i+1)
		if len(evs) != 1 || evs[0].Text != text {
			t.Fatalf("#%d = %+v, want %q", i+1, evs, text)
		}
		if i >= 3 && i <= 5 && evs[0].RepeatOf != i-2 {
			t.Errorf("#%d repeat = %d", i+1, evs[0].RepeatOf)
		}
	}
	if tr.Events[2].Output != "first output" || tr.Range(6, 6)[0].Output != "copy output" {
		t.Fatal("tool result pairing changed")
	}
	if tr.LastIndex() != 9 {
		t.Fatal(tr.LastIndex())
	}
	// The legacy cross-file merge still does not allocate numbers to copies
	// that were already omitted before this fix.
	second := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, second, []map[string]any{first, call, result, withUUID(user("continued"), "end", "r")})
	s.Files = append(s.Files, second)
	merged, err := (&Provider{}).Load(s, transcript.Load{Branches: true})
	if err != nil {
		t.Fatal(err)
	}
	if merged.LastIndex() != 10 || merged.Range(10, 10)[0].Text != "continued" {
		t.Fatalf("cross-file refs: %+v", merged)
	}
}

func TestRepeatedUndecodableShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	row := map[string]any{"type": "system", "uuid": "bad", "timestamp": t0, "content": map[string]any{"notice": "shape drift"}, "error": map[string]any{"code": "x"}}
	writeJSONL(t, path, []map[string]any{row, row})
	tr, err := (&Provider{}).Load(&transcript.Session{Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Events) != 1 || len(tr.Copies) != 1 || tr.Copies[0].RepeatOf != 1 {
		t.Fatalf("events=%+v copies=%+v", tr.Events, tr.Copies)
	}
}
