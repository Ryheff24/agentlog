package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentlog/internal/catalog"
	"agentlog/internal/transcript"
)

func TestHomeKeepsCrossProjectRunningWorkVisible(t *testing.T) {
	w := newWorld(t)
	// More completed local sessions than the entire default preview. The
	// A user report reproduced this with hundreds of local sessions.
	for i := range 15 {
		id := fmt.Sprintf("ca110000-0000-4000-8000-%012d", i)
		l := w.claude(id, "/work/busy", time.Hour)
		l.user("-", "completed local work")
		l.assistant("", "end_turn", text("Done."))
		l.save(filepath.Join(w.claudeProject("/work/busy"), id+".jsonl"), 10*time.Minute)
	}
	id := "01a17000-5e3d-7523-ac47-0c0ffee0cafe"
	w.file(filepath.Join(w.root, "codex", "sessions", "2026", "09", "16", "rollout-2026-09-16T11-59-00-"+id+".jsonl"), 10*time.Second, []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": "/work/webapp", "timestamp": w.at(time.Minute), "source": "cli"}},
		{"type": "response_item", "timestamp": w.at(10 * time.Second), "payload": map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "Audit TLA+ modeling coverage"}}}},
	})

	for _, args := range [][]string{nil, {"--limit", "1"}, {"--json", "--limit", "1"}} {
		out, code := w.run("/work/busy", args...)
		if code != 0 || !strings.Contains(out, "01a17000-5e3d") {
			t.Fatalf("home %v hid cross-project Codex work: %d\n%s", args, code, out)
		}
		if len(args) > 0 && strings.Contains(out, "ca110000") {
			t.Fatalf("home %v put completed local work ahead of running work: %s", args, out)
		}
	}
	// A named lookup remains explicitly scoped to the directory requested.
	out, code := w.run("/work/busy", ".", "--list", "--limit", "1")
	if code != 0 || strings.Contains(out, "01a17000") || !strings.Contains(out, "ca110000") {
		t.Fatalf("directory lookup changed: %d\n%s", code, out)
	}
}

func TestHomeDefaultIncludesAllRunningSessions(t *testing.T) {
	w := newWorld(t)
	for i := range 15 {
		id := fmt.Sprintf("aa110000-0000-4000-8000-%012d", i)
		l := w.claude(id, "/work/elsewhere", time.Minute)
		l.user("-", "still working")
		l.assistant("", "tool_use", toolUse("tool", "Bash", map[string]any{"command": "make test"}))
		l.save(filepath.Join(w.claudeProject("/work/elsewhere"), id+".jsonl"), 10*time.Second)
	}
	for _, args := range [][]string{nil, {"--json"}} {
		out, code := w.run("/work/blog", args...)
		if code != 0 {
			t.Fatalf("home %v: %d\n%s", args, code, out)
		}
		for i := range 15 {
			// JSON contains the full ID; text contains only the distinguishing
			// short ref, so count the title lines there instead.
			if len(args) > 0 && !strings.Contains(out, fmt.Sprintf("aa110000-0000-4000-8000-%012d", i)) {
				t.Fatalf("JSON home omitted running session %d: %s", i, out)
			}
		}
		if len(args) == 0 && strings.Count(out, "first prompt: still working") != 15 {
			t.Fatalf("text home omitted running work: %s", out)
		}
	}
	out, _ := w.run("/work/blog", "--json", "--limit", "3")
	var response struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil || len(response.Sessions) != 3 {
		t.Fatalf("explicit limit ignored: %v\n%s", err, out)
	}
}

func TestHomePromotesParentsWithRunningDescendants(t *testing.T) {
	res := &catalog.Result{Matches: []catalog.Match{
		{Session: &transcript.Session{ID: "local"}},
		{Session: &transcript.Session{ID: "parent", Children: []*transcript.Session{
			{Children: []*transcript.Session{{Live: true}}},
		}}},
		{Session: &transcript.Session{ID: "other"}},
	}}
	if limit := prepareHome(res, 0); limit != 12 || res.Matches[0].Session.ID != "parent" || res.Matches[1].Session.ID != "local" {
		t.Fatalf("limit %d, matches %+v", limit, res.Matches)
	}
}
