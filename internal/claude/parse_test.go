package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

const (
	sessionID = "3f2a91bc-1111-4222-8333-444455556666"
	agentID   = "a1b2c3d4e5f60718"
)

var t0 = time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)

func writeJSONL(t *testing.T, path string, entries []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, e := range entries {
		if _, ok := e["timestamp"]; !ok {
			e["timestamp"] = t0.Add(time.Duration(i) * time.Second)
		}
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func user(content any) map[string]any {
	return map[string]any{"type": "user", "cwd": "/work/shop", "message": map[string]any{"role": "user", "content": content}}
}

func assistant(blocks ...map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "message": map[string]any{"model": "claude-opus-5", "content": blocks}}
}

func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(id, content string, extra map[string]any) map[string]any {
	e := user([]map[string]any{{"type": "tool_result", "tool_use_id": id, "content": content}})
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func TestLoadClassifiesPromptsAndTools(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-work-shop")
	path := filepath.Join(projDir, sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Join(projDir, sessionID, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, sessionID, "subagents", "agent-"+agentID+".meta.json"),
		[]byte(`{"agentType":"general-purpose","description":"Fix scroll jump","name":"fix-84"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, path, []map[string]any{
		user("<system-reminder>ctx</system-reminder>\nthe viewer jumps when scrolling"),
		{"type": "user", "isMeta": true, "message": map[string]any{"role": "user", "content": "<local-command-caveat>x</local-command-caveat>"}},
		user("<command-name>/model</command-name>\n<command-args>opus</command-args>"),
		assistant(map[string]any{"type": "thinking", "thinking": "look at the list"}, map[string]any{"type": "text", "text": "Looking."}),
		assistant(toolUse("t1", "Bash", map[string]any{"command": "go test ./..."})),
		toolResult("t1", "Exit code 1\nFAIL", map[string]any{"toolUseResult": "Error: Exit code 1"}),
		assistant(toolUse("t2", "Edit", map[string]any{"file_path": "/work/shop/list.tsx", "old_string": "a", "new_string": "b"})),
		toolResult("t2", "ok", map[string]any{"toolUseResult": map[string]any{"filePath": "/work/shop/list.tsx",
			"structuredPatch": []map[string]any{{"oldStart": 4, "oldLines": 2, "newStart": 4, "newLines": 3, "lines": []string{" ctx", "-a", "+b", "+c"}}}}}),
		assistant(toolUse("t3", "Agent", map[string]any{"name": "fix-84", "description": "Fix scroll jump", "prompt": "fix it", "subagent_type": "general-purpose"})),
		toolResult("t3", "done", map[string]any{"toolUseResult": map[string]any{"agentId": agentID}}),
		user("Another Claude session sent a message:\n<teammate-message teammate_id=\"lead\">ship it</teammate-message>"),
		user("<task-notification>\n<status>completed</status>\n<summary>Agent finished</summary>\n</task-notification>"),
		{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"},
		assistant(toolUse("t4", "Read", map[string]any{"file_path": "/work/shop/list.tsx"})),
		{"type": "assistant", "isApiErrorMessage": true, "error": "rate_limit",
			"message": map[string]any{"model": "<synthetic>", "content": []map[string]any{{"type": "text", "text": "You've hit your session limit"}}}},
		{"type": "ai-title", "aiTitle": "Fix viewer scroll jump"},
	})

	s := &transcript.Session{Provider: transcript.Claude, ID: sessionID, Files: []string{path}}
	tr, err := (&Provider{Home: home}).Load(s, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range tr.Events {
		got = append(got, string(e.Kind)+":"+e.Tag)
	}
	want := "user: notice:command reasoning: assistant: tool: edit: agent: agent:message notice:notification notice:compaction tool: error:usage_limit"
	if strings.Join(got, " ") != want {
		t.Fatalf("events\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	ev := tr.Events
	if ev[0].Text != "the viewer jumps when scrolling" {
		t.Errorf("prompt = %q", ev[0].Text)
	}
	if ev[1].Text != "/model opus" {
		t.Errorf("command = %q", ev[1].Text)
	}
	if ev[4].ExitCode == nil || *ev[4].ExitCode != 1 {
		t.Errorf("bash exit code = %v", ev[4].ExitCode)
	}
	if c := ev[5].Changes; len(c) != 1 || c[0].Added != 2 || c[0].Removed != 1 || !strings.HasPrefix(c[0].Diff, "@@ -4,2 +4,3 @@") {
		t.Errorf("structured patch = %+v", c)
	}
	if ev[6].AgentID != agentID || ev[6].Agent != "fix-84" {
		t.Errorf("agent call = %q (%s)", ev[6].Agent, ev[6].AgentID)
	}
	if ev[7].Agent != "lead → me" || ev[7].Text != "ship it" {
		t.Errorf("teammate message = %q %q", ev[7].Agent, ev[7].Text)
	}
	if ev[8].Text != "completed: Agent finished" {
		t.Errorf("notification = %q", ev[8].Text)
	}
	if !ev[10].Pending || ev[4].Pending {
		t.Errorf("pending: read %v bash %v", ev[10].Pending, ev[4].Pending)
	}
	if tr.Session.Ending != transcript.EndingUsageLimit {
		t.Errorf("Ending = %q", tr.Session.Ending)
	}
	if tr.Session.Model != "claude-opus-5" || tr.Session.Title != "Fix viewer scroll jump" {
		t.Errorf("model %q title %q", tr.Session.Model, tr.Session.Title)
	}
}

func TestLoadInterruptedEnding(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("do it"),
		assistant(toolUse("t1", "Bash", map[string]any{"command": "sleep 100"})),
		toolResult("t1", "interrupted", map[string]any{"toolUseResult": map[string]any{"interrupted": true}}),
		user([]map[string]any{{"type": "text", "text": "[Request interrupted by user for tool use]"}}),
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Session.Ending != transcript.EndingInterrupted || !tr.Events[1].Failed {
		t.Errorf("Ending = %q, bash failed = %v", tr.Session.Ending, tr.Events[1].Failed)
	}
}

func TestDiscoverSubagents(t *testing.T) {
	home := t.TempDir()
	projDir := filepath.Join(home, "projects", "-work-shop")
	writeJSONL(t, filepath.Join(projDir, sessionID+".jsonl"), []map[string]any{
		user("hello"), {"type": "ai-title", "aiTitle": "Greeting"},
	})
	sub := filepath.Join(projDir, sessionID, "subagents", "agent-"+agentID)
	writeJSONL(t, sub+".jsonl", []map[string]any{user("fix it")})
	if err := os.WriteFile(sub+".meta.json", []byte(`{"agentType":"general-purpose","description":"Fix scroll jump","name":"fix-84"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ss, err := (&Provider{Home: home}).Discover(transcript.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("Discover() = %d sessions, want 2", len(ss))
	}
	for _, s := range ss {
		switch s.ID {
		case sessionID:
			if s.CWD != "/work/shop" || s.Title != "Greeting" || s.ParentID != "" {
				t.Errorf("parent = cwd %q title %q parent %q", s.CWD, s.Title, s.ParentID)
			}
		case agentID:
			if s.ParentID != sessionID || s.AgentName != "fix-84" || s.AgentRole != "general-purpose" || s.Title != "Fix scroll jump" {
				t.Errorf("subagent = %+v", s)
			}
		default:
			t.Errorf("unexpected session %q", s.ID)
		}
	}
}

// kinds lists the kinds of a transcript's events, in order.
func kinds(tr *transcript.Transcript) string {
	var ks []string
	for _, e := range tr.Events {
		ks = append(ks, string(e.Kind))
	}
	return strings.Join(ks, " ")
}

func TestLoadCountsUnmodelledRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("do it"),
		{"type": "cost-state", "totalCostUSD": 0.57},
		{"type": "attachment", "attachment": map[string]any{"type": "total_tokens_reminder"}},
		{"type": "attachment", "attachment": map[string]any{"type": "total_tokens_reminder"}},
		{"type": "system", "subtype": "turn_duration", "content": "", "durationMs": 1200},
		{"type": "sparkle-mode", "sparkles": 3},
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	// The deliberately ignored types produce no event; the type nobody has
	// heard of does, so drift costs fidelity and never costs the record.
	if got, want := kinds(tr), "user record"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	if e := tr.Events[1]; e.Tag != "sparkle-mode" || !strings.Contains(e.Text, "sparkles") {
		t.Errorf("stand-in event = %+v, want it named after the record and carrying it", e)
	}
	if tr.Unknowns["sparkle-mode"] != 1 {
		t.Errorf("Unknowns = %v, want the unread type counted as well as shown", tr.Unknowns)
	}
	want := map[string]int{"cost-state": 1, "attachment:total_tokens_reminder": 2, "system:turn_duration": 1}
	for k, n := range want {
		if tr.Ignored[k] != n {
			t.Errorf("Ignored[%q] = %d, want %d (all: %v)", k, tr.Ignored[k], n, tr.Ignored)
		}
	}
	if tr.Unknowns["sparkle-mode"] != 1 {
		t.Errorf("Unknowns = %v, want sparkle-mode:1", tr.Unknowns)
	}
}

// withUUID stamps a record with its place in the message DAG.
func withUUID(e map[string]any, uuid, parent string) map[string]any {
	e["uuid"] = uuid
	if parent != "" {
		e["parentUuid"] = parent
	}
	return e
}

func TestLoadHidesAbandonedBranches(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	// The user asked twice from the same point: u2/a2 were rewound away, and
	// u3/a3 are what actually happened. An undecodable record sits on the live
	// chain and must not orphan everything before it.
	writeJSONL(t, path, []map[string]any{
		withUUID(user("first ask"), "u1", ""),
		withUUID(assistant(map[string]any{"type": "text", "text": "first answer"}), "a1", "u1"),
		withUUID(user("second ask, later rewound"), "u2", "a1"),
		withUUID(assistant(map[string]any{"type": "text", "text": "answer to the rewound ask"}), "a2", "u2"),
		withUUID(map[string]any{"type": "attachment", "attachment": map[string]any{
			"type": "queued_command", "prompt": []map[string]any{{"type": "text", "text": "queued while busy"}}}}, "q1", "a1"),
		withUUID(user("third ask"), "u3", "q1"),
		withUUID(assistant(map[string]any{"type": "text", "text": "answer to the third ask"}), "a3", "u3"),
	})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range tr.Events {
		texts = append(texts, e.Text)
	}
	want := "first ask first answer queued while busy third ask answer to the third ask"
	if got := strings.Join(texts, " "); got != want {
		t.Fatalf("live chain\n got %q\nwant %q", got, want)
	}
	if tr.Hidden != 2 || len(tr.Notes) != 1 || !strings.Contains(tr.Notes[0], "2 events on 1 branch") {
		t.Errorf("hidden = %d notes = %v", tr.Hidden, tr.Notes)
	}
	if tr.Events[2].Tag != transcript.TagQueued {
		t.Errorf("queued attachment tag = %q", tr.Events[2].Tag)
	}

	all, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{Branches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Events) != 7 {
		t.Fatalf("--branches events = %d, want 7", len(all.Events))
	}
	for i, e := range all.Events {
		if want := i == 2 || i == 3; e.Abandoned != want {
			t.Errorf("event %d (%q) abandoned = %v, want %v", i, e.Text, e.Abandoned, want)
		}
	}
	if all.Hidden != 0 {
		t.Errorf("--branches hidden = %d, want 0", all.Hidden)
	}
}

// Turns before a compaction stay on the live chain whichever way the
// compaction names the record it continues from.
func TestLoadKeepsTurnsBeforeCompaction(t *testing.T) {
	for _, tc := range []struct {
		name, logicalParent string
	}{
		{"names the last record before it", "a1"},
		{"names a record written after it", "u2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), sessionID+".jsonl")
			boundary := withUUID(map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"}, "c1", "")
			boundary["logicalParentUuid"] = tc.logicalParent
			writeJSONL(t, path, []map[string]any{
				withUUID(user("before compaction"), "u1", ""),
				withUUID(assistant(map[string]any{"type": "text", "text": "answer before"}), "a1", "u1"),
				boundary,
				withUUID(user("after compaction"), "u2", "c1"),
				withUUID(assistant(map[string]any{"type": "text", "text": "answer after"}), "a2", "u2"),
			})
			tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
			if err != nil {
				t.Fatal(err)
			}
			var texts []string
			for _, e := range tr.Events {
				if e.Tag != transcript.TagCompaction {
					texts = append(texts, e.Text)
				}
			}
			want := "before compaction answer before after compaction answer after"
			if got := strings.Join(texts, " "); got != want || tr.Hidden != 0 {
				t.Fatalf("events %q (hidden %d), want %q", got, tr.Hidden, want)
			}
		})
	}
}

func TestLoadKeepsChainAcrossUndecodableRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		withUUID(user("ask"), "u1", ""),
		// "error" is a string field for this reader; an object fails to decode.
		withUUID(map[string]any{"type": "system", "subtype": "informational", "content": "x",
			"error": map[string]any{"code": 500}}, "s1", "u1"),
		withUUID(assistant(map[string]any{"type": "text", "text": "answer"}), "a1", "s1"),
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	// The record that would not decode is still shown, in its place, and the
	// chain across it is unbroken.
	if got, want := kinds(tr), "user record assistant"; got != want {
		t.Fatalf("kinds = %q, want %q (a record the reader cannot decode must not cut the chain)", got, want)
	}
	if tr.Hidden != 0 {
		t.Errorf("hidden = %d, want 0", tr.Hidden)
	}
	if tr.Unknowns["undecodable-record"] != 1 {
		t.Errorf("Unknowns = %v", tr.Unknowns)
	}
}

// enqueue is a prompt typed while the model was working.
func enqueue(text string) map[string]any {
	return map[string]any{"type": "queue-operation", "operation": "enqueue", "content": text}
}

func TestLoadKeepsOnlyQueuedPromptsThatWereNeverDelivered(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "projects", "-work-shop", sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("start the audit"),
		// Delivered by the queue naming it, then arriving as its own record.
		enqueue("also check the parser"),
		{"type": "queue-operation", "operation": "remove", "content": "also check the parser", "reason": "absorbed_mid_turn"},
		user("also check the parser"),
		// Delivered as an attachment, which is how most queued prompts arrive.
		enqueue("and the writer"),
		{"type": "queue-operation", "operation": "dequeue"},
		{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": "and the writer"}},
		// Typed last; the session ended before it was taken.
		enqueue("do not ship yet"),
		assistant(map[string]any{"type": "text", "text": "audit done"}),
	})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range tr.Events {
		got = append(got, string(e.Kind)+":"+e.Tag+":"+e.Text)
	}
	want := []string{
		"user::start the audit",
		"user::also check the parser",
		"user:queued:and the writer",
		"user:unanswered:do not ship yet",
		"assistant::audit done",
	}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("events =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if len(tr.Notes) != 1 || !strings.Contains(tr.Notes[0], "never answered") {
		t.Errorf("notes = %q, want one about the unanswered prompt", tr.Notes)
	}
}

func TestLoadDoesNotGuessWhichQueuedPromptWasTaken(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "projects", "-work-shop", sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("start the audit"),
		enqueue("first"),
		enqueue("second"),
		// One prompt was taken and the record does not say which. Claiming the
		// other was never answered would be a guess.
		{"type": "queue-operation", "operation": "dequeue"},
		assistant(map[string]any{"type": "text", "text": "done"}),
	})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Events) != 2 {
		t.Fatalf("events = %d, want just the prompt and the reply", len(tr.Events))
	}
	if len(tr.Notes) != 0 {
		t.Errorf("notes = %q, want none", tr.Notes)
	}
}

// A response that arrives in parts is written down once per part, with the
// same message id and the same usage on each. Adding the records up doubled
// or quadrupled what the session was charged: in the corpus, one session's
// 1094 responses were written as 2411 records.
func TestUsageCountsAResponseOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	usage := map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1000,
		"cache_creation_input_tokens": 100, "output_tokens": 50,
		"output_tokens_details": map[string]any{"thinking_tokens": 20}}
	part := func(id string, blocks ...map[string]any) map[string]any {
		e := assistant(blocks...)
		m := e["message"].(map[string]any)
		m["id"], m["usage"] = id, usage
		return e
	}
	writeJSONL(t, path, []map[string]any{
		user("go"),
		part("msg_1", map[string]any{"type": "text", "text": "thinking about it"}),
		part("msg_1", toolUse("call_1", "Bash", map[string]any{"command": "ls"})),
		part("msg_2", map[string]any{"type": "text", "text": "done"}),
	})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	u := tr.Usage
	if u.Requests != 2 {
		t.Errorf("Requests = %d, want 2 responses from 3 records", u.Requests)
	}
	if u.Total != 2320 {
		t.Errorf("Total = %d, want 2320 (two responses, not three)", u.Total)
	}
	if u.Input != 20 || u.CacheRead != 2000 || u.CacheWrite != 200 || u.Output != 100 || u.Reasoning != 40 {
		t.Errorf("usage = %+v, want each field counted twice", u)
	}
	if u.ByModel["claude-opus-5"] != 2320 {
		t.Errorf("ByModel = %v, want the model charged the whole total", u.ByModel)
	}
	if u.Source != "api responses" {
		t.Errorf("Source = %q, want it to say where the numbers came from", u.Source)
	}
}

// cost-state is the harness's own account, and it is worth more than the sum
// of the response records: it has the money in it, and it counts the calls the
// harness makes for itself, which are never written down as turns.
func TestUsagePrefersTheHarnessOwnAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	cost := func(total float64, opus int) map[string]any {
		return map[string]any{"type": "cost-state", "totalCostUSD": total, "modelUsage": map[string]any{
			"claude-opus-5":             map[string]any{"inputTokens": opus, "outputTokens": 5, "cacheReadInputTokens": 1, "thinkingTokens": 2},
			"claude-haiku-4-5-20251001": map[string]any{"inputTokens": 7, "outputTokens": 3},
		}}
	}
	writeJSONL(t, path, []map[string]any{
		user("go"),
		cost(1.5, 100),
		cost(9.25, 400), // rewritten as the session goes: the largest is current
		cost(9.25, 400),
	})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	u := tr.Usage
	if u.CostUSD != 9.25 {
		t.Errorf("CostUSD = %v, want the running total's last word", u.CostUSD)
	}
	if u.Total != 416 {
		t.Errorf("Total = %d, want 416 (406 opus + 10 haiku)", u.Total)
	}
	if u.ByModel["claude-haiku-4-5-20251001"] != 10 {
		t.Errorf("ByModel = %v, want the harness's own calls counted too", u.ByModel)
	}
	if u.Source != "cost-state" {
		t.Errorf("Source = %q, want cost-state", u.Source)
	}
	if tr.Ignored["cost-state"] != 3 {
		t.Errorf("Ignored[cost-state] = %d, want the records still counted", tr.Ignored["cost-state"])
	}
}

// The sha a session produced appears in exactly one place in the log: beside
// the tool result, as gitOperation. The command's own output says "1 file
// changed", not what it is called, so without this there is no way back from a
// finished session to what it landed.
func TestToolResultRecordsWhatWasCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("ship it"),
		assistant(toolUse("call_1", "Bash", map[string]any{"command": "git commit -am done"})),
		toolResult("call_1", "1 file changed", map[string]any{"toolUseResult": map[string]any{
			"stdout":       "1 file changed",
			"gitOperation": map[string]any{"commit": map[string]any{"sha": "66d219b", "kind": "committed", "branch": "perf/raids"}},
		}}),
		assistant(toolUse("call_2", "Bash", map[string]any{"command": "git merge fix/x"})),
		toolResult("call_2", "merged", map[string]any{"toolUseResult": map[string]any{
			"gitOperation": map[string]any{"branch": map[string]any{"ref": "fix/x", "action": "merged"}},
		}}),
		assistant(toolUse("call_3", "Bash", map[string]any{"command": "ls"})),
		toolResult("call_3", "a.go", nil),
		assistant(toolUse("call_4", "Bash", map[string]any{"command": "git commit -am more && git push"})),
		toolResult("call_4", "pushed", map[string]any{"toolUseResult": map[string]any{
			"gitOperation": map[string]any{"push": map[string]any{"branch": "perf/raids"},
				"commit": map[string]any{"sha": "77e3c0a", "kind": "committed", "branch": "perf/raids"}},
		}}),
		// An operation this build has no name for keeps what it says.
		assistant(toolUse("call_5", "Bash", map[string]any{"command": "git tag v1"})),
		toolResult("call_5", "", map[string]any{"toolUseResult": map[string]any{
			"gitOperation": map[string]any{"tag": map[string]any{"ref": "v1"}},
		}}),
	})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	var ops []string
	for _, e := range tr.Events {
		for _, g := range e.Git {
			ops = append(ops, g.Text())
		}
	}
	want := []string{"committed 66d219b on perf/raids", "merged on fix/x",
		"committed 77e3c0a on perf/raids", "pushed on perf/raids", "tag on v1"}
	if len(ops) != len(want) {
		t.Fatalf("git operations = %v, want %v", ops, want)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Errorf("operation %d = %q, want %q", i, ops[i], want[i])
		}
	}
}

// A conversation compacted into a new session is two logs (cbfe7aac, which
// continues a3d1cf8b). The new one opens on the compaction; the earlier
// turns are found in the other log by its continued-in record or, in logs
// from before Claude Code wrote one, by the record the compaction names.
func TestLoadFindsTheSessionAContinuationCameFrom(t *testing.T) {
	const before = "aaaa0000-1111-4222-8333-444455556666"
	opening := func(logicalParent string) []map[string]any {
		boundary := withUUID(map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"}, "c1", "")
		boundary["logicalParentUuid"] = logicalParent
		return []map[string]any{
			{"type": "ai-title", "aiTitle": "Docs"},
			boundary,
			withUUID(user("This session is being continued from a previous conversation."), "s1", "c1"),
			withUUID(user("carry on"), "u2", "s1"),
		}
	}
	for _, tc := range []struct {
		name    string
		earlier []map[string]any // the log beside it; nil for none
		want    string
	}{
		{"by its continued-in record", []map[string]any{
			withUUID(user("start"), "u0", ""),
			{"type": "continued-in", "sessionId": before, "continuedInSessionId": sessionID},
		}, before},
		{"by the record the compaction names", []map[string]any{withUUID(user("start"), "a9", "")}, before},
		{"not on disk", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.earlier != nil {
				writeJSONL(t, filepath.Join(dir, before+".jsonl"), tc.earlier)
			}
			path := filepath.Join(dir, sessionID+".jsonl")
			writeJSONL(t, path, opening("a9"))
			tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
			if err != nil {
				t.Fatal(err)
			}
			if !tr.Session.Continues || tr.Session.ContinuedFrom != tc.want {
				t.Fatalf("continues %v from %q, want from %q", tr.Session.Continues, tr.Session.ContinuedFrom, tc.want)
			}
			if !strings.Contains(tr.Events[0].Text, "not this one") {
				t.Errorf("opening compaction says %q", tr.Events[0].Text)
			}
		})
	}

	// The earlier log says where the conversation went, and so does its state.
	path := filepath.Join(t.TempDir(), before+".jsonl")
	writeJSONL(t, path, []map[string]any{user("start"),
		{"type": "continued-in", "sessionId": before, "continuedInSessionId": sessionID}})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: before, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Session.ContinuedIn != sessionID || tr.Session.Ending != transcript.EndingContinued || tr.Session.Continues {
		t.Fatalf("continued in %q, ending %q, continues %v", tr.Session.ContinuedIn, tr.Session.Ending, tr.Session.Continues)
	}
}

// A compaction after the conversation has started is not a continuation.
func TestLoadMidSessionCompactionIsNotAContinuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		withUUID(user("ask"), "u1", ""),
		withUUID(map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"}, "c1", ""),
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Session.Continues {
		t.Fatal("a compaction after the first prompt was taken for a continuation")
	}
}

// Records that say where and as what a session ran become notes, once each,
// not events: Claude Code repeats them on every resume.
func TestLoadNotesWhereTheSessionRan(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	moved := map[string]any{"type": "relocated", "relocatedCwd": "/w/.claude/worktrees/fix"}
	writeJSONL(t, path, []map[string]any{
		{"type": "agent-setting", "agentSetting": "spec-reader"},
		user("go"),
		{"type": "worktree-state", "worktreeSession": map[string]any{"worktreePath": "/w/.claude/worktrees/fix", "worktreeBranch": "fix"}},
		moved, moved,
		{"type": "worktree-state", "worktreeSession": nil},
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ran as agent spec-reader", "worked in worktree /w/.claude/worktrees/fix (branch fix)",
		"working directory moved to /w/.claude/worktrees/fix"}
	if strings.Join(tr.Notes, "|") != strings.Join(want, "|") || len(tr.Events) != 1 || len(tr.Unknowns) != 0 {
		t.Fatalf("notes %q, %d events, unknown %v", tr.Notes, len(tr.Events), tr.Unknowns)
	}
}

// Since 2.1.300 Claude Code keeps a copy of every request it sent the model.
// The conversation is already in the log, so the copies add no events; they
// are counted, and never reported as records this build cannot read.
func TestLoadIgnoresRequestCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("do it"),
		{"type": "api-request-shape", "shapeHash": "a1", "shape": map[string]any{"system": []any{map[string]any{"type": "text", "text": "system prompt"}}}},
		{"type": "api-request-blob", "hash": "b1", "message": map[string]any{"role": "user", "content": "do it"}},
		{"type": "api-request-blob", "hash": "b2", "agentId": "a2", "message": map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": "reminder"}}}},
		{"type": "api-request", "id": "r1", "shapeHash": "a1", "tail": []any{"b1", "b2"}},
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(tr); got != "user" {
		t.Errorf("kinds = %q, want only the prompt", got)
	}
	if len(tr.Unknowns) != 0 {
		t.Errorf("Unknowns = %v, want none", tr.Unknowns)
	}
	want := map[string]int{"api-request": 1, "api-request-shape": 1, "api-request-blob": 2}
	for k, n := range want {
		if tr.Ignored[k] != n {
			t.Errorf("Ignored[%q] = %d, want %d (all: %v)", k, tr.Ignored[k], n, tr.Ignored)
		}
	}
}

// Recent Claude Code versions write which artifacts a session watches, its artifact reactions, and the tools it used.
// The log already holds those tool calls as events, so these records add none.
func TestLoadIgnoresArtifactAndToolBookkeeping(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		user("do it"),
		{"type": "artifact-comment-monitor", "v": 1, "sessionId": sessionID, "artifacts": map[string]any{"a1": map[string]any{"state": "armed", "writtenAtMs": 1, "title": "plan"}}},
		{"type": "artifact-autoreact-ledger", "v": 1, "sessionId": sessionID, "accountUuid": "u1", "artifacts": map[string]any{"a1": map[string]any{"savedAt": 1, "everBaselined": true, "threads": []any{}}}},
		{"type": "artifact-autoreact-ledger", "v": 1, "sessionId": sessionID, "accountUuid": "u1", "artifacts": map[string]any{}},
		{"type": "tools-used", "sessionId": sessionID, "tools": []any{"Bash", "Read"}},
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(tr); got != "user" {
		t.Errorf("kinds = %q, want only the prompt", got)
	}
	if len(tr.Unknowns) != 0 {
		t.Errorf("Unknowns = %v, want none", tr.Unknowns)
	}
	want := map[string]int{"artifact-comment-monitor": 1, "artifact-autoreact-ledger": 2, "tools-used": 1}
	for k, n := range want {
		if tr.Ignored[k] != n {
			t.Errorf("Ignored[%q] = %d, want %d (all: %v)", k, tr.Ignored[k], n, tr.Ignored)
		}
	}
}

// A forked subagent's log opens with a link to the parent record it forked from.
// The parent is already known from the folder the log sits in, so the link adds no event.
func TestLoadIgnoresForkContextRef(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-f1.jsonl")
	writeJSONL(t, path, []map[string]any{
		{"type": "fork-context-ref", "agentId": "f1", "parentSessionId": sessionID, "parentLastUuid": "p9", "contextLength": 10},
		user("do the one directive"),
	})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: "f1", ParentID: sessionID, Files: []string{path}}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(tr); got != "user" {
		t.Errorf("kinds = %q, want only the prompt", got)
	}
	if len(tr.Unknowns) != 0 {
		t.Errorf("Unknowns = %v, want none", tr.Unknowns)
	}
	if tr.Ignored["fork-context-ref"] != 1 {
		t.Errorf("Ignored = %v, want fork-context-ref counted once", tr.Ignored)
	}
}
