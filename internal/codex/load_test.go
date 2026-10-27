package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentlog/internal/transcript"
)

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func prompt(at float64, text string) rec {
	return item(at, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": text}}})
}

// A rollout can hold lines this reader cannot use: a partial line from a
// process killed mid-write, or a record with no type. They are counted, and
// the records around them are read as usual.
func TestLoadSurvivesBrokenLines(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{meta(threadID, nil), prompt(1, "first")})
	appendLine(t, f, `{"timestamp":"2026-09-15T14:00:02Z","payload":{"type":"user_message"}}`)
	appendLine(t, f, `{"timestamp":"2026-09-15T14:00:03Z","type":"event_msg","payload":{"type":"item_compl`)
	appendLine(t, f, `{"timestamp":"2026-09-15T14:00:04Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"second"}]}}}`)
	tr := load(t, threadID, f)
	if got := kinds(tr); got != "user user" {
		t.Fatalf("kinds = %q", got)
	}
	if tr.Unknowns["undecodable-line"] != 2 {
		t.Errorf("Unknowns = %v, want both broken lines counted", tr.Unknowns)
	}
}

func TestLoadWithNoReadablePage(t *testing.T) {
	dir := t.TempDir()
	noMeta := filepath.Join(dir, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl")
	if err := os.WriteFile(noMeta, []byte(`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, files := range [][]string{{filepath.Join(dir, "gone.jsonl")}, {noMeta}} {
		if _, err := (&Provider{}).Load(&transcript.Session{ID: threadID, Files: files}, transcript.Load{}); err == nil {
			t.Errorf("Load(%v) returned no error", files)
		}
	}
}

// A thread edited or rolled back into a new page leaves the pages of the old
// branch on disk. The listing windows read only the pages at either end.
func TestLoadPagedThread(t *testing.T) {
	home := t.TempDir()
	const abandoned = "01a0a630-0000-7000-8000-000000000003"
	first := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil), prompt(1, "opening prompt"), prompt(2, "second prompt"),
	})
	base := func(at time.Duration) rec {
		r := meta(threadID, map[string]any{"history_base": map[string]any{"thread_id": threadID, "end_ordinal_exclusive": 99}})
		r.payload.(map[string]any)["timestamp"] = t0.Add(at)
		return r
	}
	// A page from an older edit, superseded by the next one.
	old := writeRollout(t, home, "rollout-2026-09-15T14-00-30-"+threadID+"_"+abandoned+".jsonl", []rec{base(30 * time.Second), prompt(31, "abandoned edit")})
	last := writeRollout(t, home, "rollout-2026-09-15T14-01-00-"+threadID+"_"+pageID+".jsonl", []rec{base(time.Minute), prompt(61, "latest prompt")})
	s := &transcript.Session{Provider: transcript.Codex, ID: threadID, Files: []string{first, old, last}}

	whole, err := (&Provider{}).Load(s, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range whole.Events {
		texts = append(texts, e.Text)
	}
	if got := strings.Join(texts, "|"); got != "opening prompt|second prompt|latest prompt" {
		t.Errorf("whole = %q", got)
	}
	if len(whole.Notes) == 0 || !strings.Contains(whole.Notes[0], "1 abandoned branch page") {
		t.Errorf("notes = %q", whole.Notes)
	}

	for _, tc := range []struct {
		name   string
		window transcript.Window
		want   string
	}{
		{"head reads the first page", transcript.Window{HeadBytes: 1 << 20}, "opening prompt|second prompt"},
		{"tail reads the last page", transcript.Window{TailBytes: 1 << 20}, "latest prompt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := (&Provider{}).Load(s, transcript.Load{Window: tc.window})
			if err != nil {
				t.Fatal(err)
			}
			var texts []string
			for _, e := range tr.Events {
				texts = append(texts, e.Text)
			}
			if got := strings.Join(texts, "|"); got != tc.want {
				t.Errorf("events = %q, want %q", got, tc.want)
			}
		})
	}
}

// A page whose history base names a page that is not on disk (deleted, or
// in the other sessions root) ends the chain there rather than failing.
func TestLoadPageWithMissingBase(t *testing.T) {
	home := t.TempDir()
	r := meta(threadID, map[string]any{"history_base": map[string]any{"thread_id": "01a0a6ff-0000-7000-8000-00000000dead", "end_ordinal_exclusive": 3}})
	f := writeRollout(t, home, "rollout-2026-09-15T14-01-00-"+threadID+"_"+pageID+".jsonl", []rec{r, prompt(61, "continued")})
	tr := load(t, threadID, f)
	if len(tr.Events) != 1 || tr.Events[0].Text != "continued" {
		t.Errorf("events = %+v", tr.Events)
	}
}

// Where a thread came from is in its first page's session_meta.source, which
// has been a string, an object naming a review, and an object describing the
// spawn, over the versions seen.
func TestDiscoverReadsWhereAThreadCameFrom(t *testing.T) {
	home := t.TempDir()
	ids := map[string]string{
		"cli":      "01a0a700-0000-7000-8000-000000000001",
		"review":   "01a0a700-0000-7000-8000-000000000002",
		"guardian": "01a0a700-0000-7000-8000-000000000003",
		"other":    "01a0a700-0000-7000-8000-000000000004",
		"bare":     "01a0a700-0000-7000-8000-000000000005",
		"meta":     "01a0a700-0000-7000-8000-000000000006",
	}
	metas := map[string]map[string]any{
		"cli":      {"source": "cli", "git": map[string]any{"branch": "main", "commit_hash": "abc123"}},
		"review":   {"source": map[string]any{"subagent": "review"}},
		"guardian": {"source": "unified_exec_startup", "thread_source": "guardian_review"},
		"other":    {"source": map[string]any{"subagent": map[string]any{"other": "guardian"}}},
		"bare":     {"source": map[string]any{"subagent": map[string]any{}}},
		// Some versions put the agent's name and path in the meta itself.
		"meta": {"source": "exec", "agent_nickname": "Hooke", "agent_path": "/root/audit"},
	}
	for k, id := range ids {
		writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+id+".jsonl", []rec{meta(id, metas[k])})
	}
	// A rollout whose first line is not session_meta is not a thread.
	writeRollout(t, home, "rollout-2026-09-15T14-00-00-01a0a700-0000-7000-8000-00000000beef.jsonl", []rec{prompt(1, "orphan page")})
	// Titles come from the session index; a later line renames the thread.
	index := strings.Join([]string{
		`{"id":"` + ids["cli"] + `","thread_name":"First name","updated_at":"2026-09-15T14:00:00Z"}`,
		`{"id":"` + ids["cli"] + `","thread_name":"Renamed","updated_at":"2026-09-15T15:00:00Z"}`,
		`{"id":"` + ids["review"] + `","thread_name":""}`,
		`not json`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	ss, err := (&Provider{Home: home}).Discover(transcript.Query{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*transcript.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	if len(ss) != len(ids) {
		t.Fatalf("sessions = %d, want %d", len(ss), len(ids))
	}
	for k, want := range map[string]struct{ role, name, title string }{
		"cli":      {"", "", "Renamed"},
		"review":   {"review", "", ""},
		"guardian": {"guardian", "", ""},
		"other":    {"guardian", "", ""},
		"bare":     {"", "", ""},
		"meta":     {"/root/audit", "Hooke", ""},
	} {
		s := byID[ids[k]]
		if s.AgentRole != want.role || s.AgentName != want.name || s.Title != want.title {
			t.Errorf("%s: role %q name %q title %q, want %+v", k, s.AgentRole, s.AgentName, s.Title, want)
		}
	}
	if s := byID[ids["cli"]]; s.Branch != "main" || s.Commit != "abc123" || s.ParentID != "" {
		t.Errorf("cli = branch %q commit %q parent %q", s.Branch, s.Commit, s.ParentID)
	}
}

// A thread is listed when any of its pages is recent, with all of its pages.
func TestDiscoverKeepsOldPagesOfARecentThread(t *testing.T) {
	home := t.TempDir()
	first := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{meta(threadID, nil)})
	next := meta(threadID, nil)
	next.payload.(map[string]any)["timestamp"] = t0.Add(time.Minute)
	writeRollout(t, home, "rollout-2026-09-15T14-01-00-"+threadID+"_"+pageID+".jsonl", []rec{next})
	stale := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+childID+".jsonl", []rec{meta(childID, nil)})
	old := time.Now().Add(-48 * time.Hour)
	for _, f := range []string{first, stale} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := (&Provider{Home: home}).Discover(transcript.Query{Since: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != threadID || len(ss[0].Files) != 2 || ss[0].Files[0] != first {
		t.Fatalf("sessions = %+v, want the recent thread with both pages, oldest first", ss)
	}
}

// A tail window can open after a subagent was started, on the record saying it
// finished. That is still the subagent, and it still finished.
func TestSubagentFinishingInsideTheWindowOnly(t *testing.T) {
	tr := loadRecs(t, item(1, map[string]any{"type": "SubAgentActivity", "id": "subagent-completed-1", "kind": "completed",
		"agent_thread_id": childID, "agent_path": "/root/parser_audit"}))
	if len(tr.Events) != 1 {
		t.Fatalf("events = %q", kinds(tr))
	}
	if e := tr.Events[0]; e.AgentID != childID || e.Tag != "completed" || e.Failed || e.Pending {
		t.Errorf("event = %+v", e)
	}
}

// The two streams log the same prompt; a long one is still recognised as the
// same prompt, whatever follows the part they are compared on.
func TestLongPromptLoggedByBothStreamsAppearsOnce(t *testing.T) {
	long := strings.Repeat("Refactor the parser so that each record type has one reader. ", 12)
	tr := loadRecs(t,
		responseItem(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": long}}}),
		prompt(1.001, long),
	)
	if got := kinds(tr); got != "user" {
		t.Errorf("kinds = %q, want the prompt once", got)
	}
}

// When a legacy call could be paired with an item only by its tool name, and
// two calls to that tool are waiting, neither is merged: guessing would give
// one call the other's output.
func TestAmbiguousPairingLeavesBothCalls(t *testing.T) {
	tr := loadRecs(t,
		call(1, "mcp__browser__click", "legacy_a", map[string]any{"selector": "#a"}),
		call(2, "mcp__browser__click", "legacy_b", map[string]any{"selector": "#b"}),
		// The item spells its arguments differently, so only the name matches.
		item(3, map[string]any{"type": "McpToolCall", "id": "item-3", "server": "browser", "tool": "click",
			"arguments": map[string]any{"target": "#a"}, "status": "completed",
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "clicked"}}}}),
	)
	if len(tr.Events) != 3 {
		t.Fatalf("events = %q, want both calls and the item", kinds(tr))
	}
	for _, e := range tr.Events[:2] {
		if e.Output != "" {
			t.Errorf("call %q took an output it cannot be shown to own: %q", e.Input, e.Output)
		}
	}
}

// Drift: a response_item type added after this build stands in for itself.
func TestUnknownResponseItemStandsIn(t *testing.T) {
	tr := loadRecs(t, responseItem(1, map[string]any{"type": "image_generation_call", "status": "completed", "result": "…"}))
	if len(tr.Events) != 1 || tr.Unknowns["response_item:image_generation_call"] != 1 {
		t.Errorf("events %q unknowns %v", kinds(tr), tr.Unknowns)
	}
}

// Drift in the running token account: a field changes type. The record is
// skipped, and the account already read stands.
func TestTokenCountInAnUnreadableShape(t *testing.T) {
	good := event(1, map[string]any{"type": "token_count", "info": map[string]any{
		"total_token_usage": map[string]any{"input_tokens": 100, "output_tokens": 10, "total_tokens": 110}},
		"rate_limits": map[string]any{"primary": map[string]any{"used_percent": 5.0, "window_minutes": 300}}})
	bad := event(2, map[string]any{"type": "token_count", "info": "unavailable"})
	u := loadRecs(t, good, bad).Usage
	if u.Total != 110 {
		t.Errorf("Total = %d, want the last readable account", u.Total)
	}
	// A limit with no reset time says so, rather than resetting at 1970.
	if len(u.Limits) != 1 || !u.Limits[0].Resets.IsZero() {
		t.Errorf("Limits = %+v", u.Limits)
	}
}
