package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentlog/internal/transcript"
)

const (
	threadID = "01a0a5c3-7e10-7d24-9f31-6b28c40e1a95"
	childID  = "01a0a61c-0000-7000-8000-000000000001"
	pageID   = "01a0a62f-0000-7000-8000-000000000002"
)

var t0 = time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)

// rec is one rollout record; at is seconds after t0.
type rec struct {
	at      float64
	ordinal int
	typ     string
	payload any
}

func writeRollout(t *testing.T, home, name string, recs []rec) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "09", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range recs {
		line, err := json.Marshal(map[string]any{
			"timestamp": t0.Add(time.Duration(r.at * float64(time.Second))),
			"ordinal":   r.ordinal,
			"type":      r.typ,
			"payload":   r.payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func meta(id string, extra map[string]any) rec {
	p := map[string]any{"id": id, "timestamp": t0, "cwd": "/work/player", "originator": "codex_cli_rs"}
	for k, v := range extra {
		p[k] = v
	}
	return rec{typ: "session_meta", payload: p}
}

func item(at float64, it map[string]any) rec {
	return rec{at: at, typ: "event_msg", payload: map[string]any{"type": "item_completed", "item": it}}
}

func responseItem(at float64, p map[string]any) rec {
	return rec{at: at, typ: "response_item", payload: p}
}

func load(t *testing.T, id string, files ...string) *transcript.Transcript {
	t.Helper()
	tr, err := (&Provider{}).Load(&transcript.Session{Provider: transcript.Codex, ID: id, Files: files}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func kinds(tr *transcript.Transcript) string {
	var ks []string
	for _, e := range tr.Events {
		ks = append(ks, string(e.Kind))
	}
	return strings.Join(ks, " ")
}

func TestLoadItemStream(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		item(1, map[string]any{"type": "UserMessage", "content": []map[string]any{
			{"type": "text", "text": "# AGENTS.md instructions for /work/player\n\n<INSTRUCTIONS>\nrules\n</INSTRUCTIONS>"},
			{"type": "text", "text": "<environment_context>\n<cwd>/work/player</cwd>\n</environment_context>"},
			{"type": "text", "text": "fix the fullscreen regression"},
		}}),
		// The legacy stream is still written alongside items and must not double up.
		responseItem(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "fix the fullscreen regression"}}}),
		item(2, map[string]any{"type": "Reasoning", "summary_text": []string{"Looking at the player."}}),
		item(3, map[string]any{"type": "Reasoning", "summary_text": []string{"Looking at the player.", "Native ownership is lost."}}),
		responseItem(4, map[string]any{"type": "function_call", "name": "exec_command", "call_id": "c1", "arguments": `{"cmd":"npm test"}`}),
		item(4, map[string]any{"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", "npm test"}, "exit_code": 1, "status": "completed", "aggregated_output": "1 failing"}),
		item(5, map[string]any{"type": "FileChange", "status": "completed", "changes": map[string]any{
			"/work/player/src/player.ts":    map[string]any{"type": "update", "unified_diff": "@@ -1 +1 @@\n-a\n+b"},
			"/work/player/tests/legacy.mjs": map[string]any{"type": "add", "content": "import test from 'node:test'\ntest('x')\n"},
		}}),
		responseItem(6, map[string]any{"type": "function_call", "name": "update_plan", "call_id": "p1", "arguments": `{"plan":[{"step":"fix","status":"completed"},{"step":"test","status":"in_progress"}]}`}),
		item(7, map[string]any{"type": "AgentMessage", "phase": "final_answer", "content": []map[string]any{{"type": "text", "text": "Implemented a narrow fix."}}}),
	})

	tr := load(t, threadID, f)
	if got, want := kinds(tr), "user reasoning reasoning tool edit plan assistant"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	ev := tr.Events
	if ev[0].Text != "fix the fullscreen regression" {
		t.Errorf("user text = %q", ev[0].Text)
	}
	if ev[2].Text != "Native ownership is lost." {
		t.Errorf("reasoning delta = %q, want only the new summary", ev[2].Text)
	}
	if ev[3].Input != "npm test" || !ev[3].Failed || ev[3].ExitCode == nil || *ev[3].ExitCode != 1 {
		t.Errorf("command = %+v", ev[3])
	}
	if len(ev[4].Changes) != 2 {
		t.Fatalf("changes = %+v", ev[4].Changes)
	}
	if c := ev[4].Changes[1]; c.Op != "add" || c.Added != 2 {
		t.Errorf("added file = %s +%d, want add +2", c.Op, c.Added)
	}
	if len(tr.Plan) != 2 || tr.Plan[1].Status != "in_progress" {
		t.Errorf("plan = %+v", tr.Plan)
	}
	if ev[6].Tag != "final" || tr.Session.Ending != transcript.EndingDone {
		t.Errorf("final tag = %q ending = %q", ev[6].Tag, tr.Session.Ending)
	}
}

func TestLoadLegacyStream(t *testing.T) {
	home := t.TempDir()
	envelope := "*** Begin Patch\n*** Update File: src/a.ts\n@@\n-x\n+y\n*** End Patch"
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		responseItem(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "<user_instructions>x</user_instructions>"}}}),
		{at: 1, typ: "event_msg", payload: map[string]any{"type": "user_message", "message": "please fix it"}},
		responseItem(2, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "p1", "input": envelope}),
		responseItem(3, map[string]any{"type": "custom_tool_call_output", "call_id": "p1", "output": "Success. Updated the following files:\nM src/a.ts"}),
		responseItem(4, map[string]any{"type": "function_call", "name": "shell", "call_id": "s1", "arguments": `{"command":["bash","-lc","go test ./..."]}`}),
	})

	tr := load(t, threadID, f)
	if got, want := kinds(tr), "user edit tool"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	if tr.Events[0].Text != "please fix it" {
		t.Errorf("user text = %q", tr.Events[0].Text)
	}
	if c := tr.Events[1].Changes; len(c) != 1 || c[0].Path != "src/a.ts" || c[0].Added != 1 || c[0].Removed != 1 {
		t.Errorf("patch changes = %+v", c)
	}
	if tr.Events[1].Pending {
		t.Error("answered apply_patch marked pending")
	}
	if !tr.Events[2].Pending || tr.Events[2].Input != "go test ./..." || tr.Session.Ending != transcript.EndingMidTool {
		t.Errorf("unanswered shell = %+v, ending %q", tr.Events[2], tr.Session.Ending)
	}
}

func TestLoadPairsLegacyRecordsWithPartialItemStream(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		responseItem(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "run the tests"}}}),
		item(1, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": "run the tests"}, {"type": "image"}}}),
		// Parallel calls: both requested before either item completes.
		responseItem(2, map[string]any{"type": "function_call", "name": "exec_command", "call_id": "c1", "arguments": `{"cmd":"ls"}`}),
		responseItem(2, map[string]any{"type": "function_call", "name": "write_stdin", "call_id": "c2", "arguments": `{"session_id":7,"chars":"y"}`}),
		item(3, map[string]any{"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", "ls"}, "exit_code": 0, "status": "completed", "aggregated_output": "a.txt"}),
		responseItem(3, map[string]any{"type": "function_call_output", "call_id": "c1", "output": "Chunk ID: 1\nProcess exited with code 0\nOutput:\na.txt"}),
		responseItem(3, map[string]any{"type": "function_call_output", "call_id": "c2", "output": "ok"}),
		// Code mode in a version that logs no item for the inner command.
		responseItem(4, map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c3", "input": `text(await tools.exec_command({cmd:"go test \"./...\""}))`}),
		responseItem(5, map[string]any{"type": "custom_tool_call_output", "call_id": "c3", "output": "ok"}),
		item(6, map[string]any{"type": "AgentMessage", "phase": "final_answer", "content": []map[string]any{{"type": "text", "text": "Tests pass."}}}),
		responseItem(6, map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "Tests pass."}}}),
	})

	tr := load(t, threadID, f)
	if got, want := kinds(tr), "user tool tool tool assistant"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	ev := tr.Events
	if ev[1].Input != "ls" || ev[1].Output != "a.txt" || ev[1].Pending {
		t.Errorf("paired command = input %q output %q pending %v", ev[1].Input, ev[1].Output, ev[1].Pending)
	}
	if ev[2].Tool != "write_stdin" || ev[2].Pending {
		t.Errorf("legacy-only call = %+v", ev[2])
	}
	if ev[3].Tool != "shell" || ev[3].Input != `go test "./..."` || ev[3].Output != "ok" {
		t.Errorf("code-mode command = tool %q input %q output %q", ev[3].Tool, ev[3].Input, ev[3].Output)
	}
	if ev[4].Tag != "final" {
		t.Errorf("assistant tag = %q, want final from the item", ev[4].Tag)
	}
}

func TestLoadPromptBeforeItemStreamNotDuplicated(t *testing.T) {
	home := t.TempDir()
	prompt := "make a safari extension"
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		// The legacy record lands just before the first item switches the stream over.
		responseItem(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": prompt}}}),
		item(1.001, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": prompt}}}),
		item(60, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": prompt}}}),
	})

	tr := load(t, threadID, f)
	// A deliberate repeat a minute later is kept.
	if got := kinds(tr); got != "user user" {
		t.Errorf("kinds = %q, want %q", got, "user user")
	}
}

func TestLoadTaskCompleteUsageLimit(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		item(1, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": "add the legacy test"}}}),
		item(2, map[string]any{"type": "FileChange", "status": "completed", "changes": map[string]any{
			"/work/player/tests/legacy-api.mjs": map[string]any{"type": "add", "content": "test()\n"},
		}}),
		{at: 3, typ: "event_msg", payload: map[string]any{"type": "task_complete", "last_agent_message": nil, "error": map[string]any{
			"message": "You've hit your usage limit. Upgrade to Pro or try again at 7:46 PM.", "codex_error_info": "usage_limit_exceeded",
		}}},
	})

	tr := load(t, threadID, f)
	if got, want := kinds(tr), "user edit error"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	if tr.Session.Ending != transcript.EndingUsageLimit {
		t.Errorf("Ending = %q, want %q", tr.Session.Ending, transcript.EndingUsageLimit)
	}
}

func TestLoadForkHidesReplayedHistory(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+childID+".jsonl", []rec{
		meta(childID, map[string]any{"forked_from_id": threadID}),
		item(0, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": "parent prompt"}}}),
		item(0.1, map[string]any{"type": "AgentMessage", "content": []map[string]any{{"type": "text", "text": "parent reply"}}}),
		item(5, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": "write the test"}}}),
	})

	tr := load(t, childID, f)
	if len(tr.Events) != 1 || tr.Events[0].Text != "write the test" {
		t.Fatalf("events = %+v", tr.Events)
	}
	if tr.Hidden != 2 {
		t.Errorf("Hidden = %d, want 2", tr.Hidden)
	}
}

func TestLoadPagedHistoryDropsRolledBackRecords(t *testing.T) {
	home := t.TempDir()
	msg := func(at float64, ord int, text string) rec {
		r := item(at, map[string]any{"type": "UserMessage", "content": []map[string]any{{"type": "text", "text": text}}})
		r.ordinal = ord
		return r
	}
	first := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		msg(1, 1, "kept"),
		msg(2, 2, "rolled back"),
	})
	next := meta(threadID, map[string]any{"history_base": map[string]any{"thread_id": threadID, "end_ordinal_exclusive": 2}})
	next.payload.(map[string]any)["timestamp"] = t0.Add(time.Minute)
	second := writeRollout(t, home, "rollout-2026-09-15T14-01-00-"+threadID+"_"+pageID+".jsonl", []rec{
		next,
		msg(61, 2, "edited turn"),
	})

	tr := load(t, threadID, first, second)
	var texts []string
	for _, e := range tr.Events {
		texts = append(texts, e.Text)
	}
	if got := strings.Join(texts, "|"); got != "kept|edited turn" {
		t.Errorf("events = %q, want %q", got, "kept|edited turn")
	}
	if tr.Hidden != 1 {
		t.Errorf("Hidden = %d, want 1", tr.Hidden)
	}
}

func TestDiscoverLinksSubagent(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{meta(threadID, nil)})
	writeRollout(t, home, "rollout-2026-09-15T14-00-05-"+childID+".jsonl", []rec{meta(childID, map[string]any{
		"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{
			"parent_thread_id": threadID, "agent_path": "/root/regression_fix", "agent_nickname": "Hooke",
		}}},
	})})

	ss, err := (&Provider{Home: home}).Discover(transcript.Query{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*transcript.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	child := byID[childID]
	if len(ss) != 2 || child == nil {
		t.Fatalf("Discover() = %d sessions, child %v", len(ss), child)
	}
	if child.ParentID != threadID || child.AgentName != "Hooke" || child.AgentRole != "/root/regression_fix" {
		t.Errorf("child = parent %q name %q role %q", child.ParentID, child.AgentName, child.AgentRole)
	}
	if byID[threadID].CWD != "/work/player" {
		t.Errorf("CWD = %q", byID[threadID].CWD)
	}
}

func TestLoadCountsUnmodelledRecords(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		{at: 1, typ: "event_msg", payload: map[string]any{"type": "user_message", "message": "go"}},
		{at: 2, typ: "event_msg", payload: map[string]any{"type": "task_started"}},
		{at: 3, typ: "token_usage_record", payload: map[string]any{"usage": map[string]any{}}},
		{at: 4, typ: "response_item", payload: map[string]any{"type": "tool_search_call", "query": "x"}},
		item(5, map[string]any{"type": "ContextCompaction", "id": "item-5"}),
		{at: 6, typ: "quantum_state", payload: map[string]any{"type": "collapsed"}},
	})

	tr := load(t, threadID, f)
	// quantum_state is a type this build has never heard of: it stands up as
	// a record rather than vanishing. The rest are ignored on purpose.
	if got, want := kinds(tr), "user record"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	want := map[string]int{
		"event_msg:task_started":         1,
		"token_usage_record":             1,
		"response_item:tool_search_call": 1,
		"item:ContextCompaction":         1,
		"session_meta":                   1,
	}
	for k, n := range want {
		if tr.Ignored[k] != n {
			t.Errorf("Ignored[%q] = %d, want %d (all: %v)", k, tr.Ignored[k], n, tr.Ignored)
		}
	}
	if tr.Unknowns["quantum_state"] != 1 {
		t.Errorf("Unknowns = %v, want quantum_state:1", tr.Unknowns)
	}
}

// Codex runs tool calls in parallel and they complete out of order. Pairing on
// the tool name alone merged whichever sibling was nearest, so the displayed
// arguments could belong to one call and the output to another.
func TestPairsParallelCallsToTheSameTool(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		responseItem(1, map[string]any{"type": "function_call", "name": "mcp__browser__click", "call_id": "call_a", "arguments": `{"selector":"#play"}`}),
		responseItem(2, map[string]any{"type": "function_call", "name": "mcp__browser__click", "call_id": "call_b", "arguments": `{"selector":"#fullscreen"}`}),
		// Both calls are in flight, and the first one finishes first. A scan
		// backwards for "the nearest click" finds the second call.
		item(3, map[string]any{"type": "McpToolCall", "id": "call_a", "server": "browser", "tool": "click",
			"arguments": map[string]any{"selector": "#play"}, "status": "completed",
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "started playback"}}}}),
		item(4, map[string]any{"type": "McpToolCall", "id": "call_b", "server": "browser", "tool": "click",
			"arguments": map[string]any{"selector": "#fullscreen"}, "status": "completed",
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "entered fullscreen"}}}}),
	})

	tr := load(t, threadID, f)
	if len(tr.Events) != 2 {
		t.Fatalf("events = %d, want 2 (one per call)", len(tr.Events))
	}
	// Events stay in the order the calls were issued, each holding its own
	// arguments and its own result. Merging on the name alone moved the first
	// call's result onto the second call's event, leaving the first pending.
	want := []struct{ input, output string }{
		{"#play", "started playback"},
		{"#fullscreen", "entered fullscreen"},
	}
	for i, w := range want {
		e := tr.Events[i]
		if !strings.Contains(e.Input, w.input) {
			t.Errorf("event %d input = %q, want the call on %s", i, e.Input, w.input)
		}
		if e.Output != w.output {
			t.Errorf("event %d output = %q, want %q", i, e.Output, w.output)
		}
		if e.Pending {
			t.Errorf("event %d (%s) is pending; its result was merged elsewhere", i, w.input)
		}
	}
}

// Without call ids on the items, the arguments still tell the calls apart.
func TestPairsParallelCallsWithoutCallIDs(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		responseItem(1, map[string]any{"type": "function_call", "name": "mcp__browser__click", "call_id": "call_a", "arguments": `{"selector":"#play"}`}),
		responseItem(2, map[string]any{"type": "function_call", "name": "mcp__browser__click", "call_id": "call_b", "arguments": `{"selector":"#fullscreen"}`}),
		item(3, map[string]any{"type": "McpToolCall", "id": "item-3", "server": "browser", "tool": "click",
			"arguments": map[string]any{"selector": "#play"}, "status": "completed",
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "started playback"}}}}),
	})

	tr := load(t, threadID, f)
	if len(tr.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(tr.Events))
	}
	for _, e := range tr.Events {
		if strings.Contains(e.Input, "#fullscreen") && e.Output != "" {
			t.Errorf("#fullscreen took the other call's output %q", e.Output)
		}
		if strings.Contains(e.Input, "#play") && e.Output != "started playback" {
			t.Errorf("#play output = %q", e.Output)
		}
	}
}

// Two edits to one file in the same window are told apart by their size, and
// when even that is equal neither is merged rather than merging the wrong one.
func TestPairsSiblingEditsToTheSameFile(t *testing.T) {
	home := t.TempDir()
	one := "*** Begin Patch\n*** Update File: src/a.ts\n@@\n-x\n+y\n*** End Patch"
	two := "*** Begin Patch\n*** Update File: src/a.ts\n@@\n-p\n-q\n+r\n+s\n+t\n*** End Patch"
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		responseItem(1, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "call_a", "input": one}),
		responseItem(2, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "call_b", "input": two}),
		// The item belongs to the first edit, not the nearest one.
		item(3, map[string]any{"type": "FileChange", "id": "item-9", "status": "failed", "stderr": "conflict",
			"changes": map[string]any{"/work/player/src/a.ts": map[string]any{"type": "update", "unified_diff": "@@ -1,1 +1,1 @@\n-x\n+y"}}}),
	})

	tr := load(t, threadID, f)
	if len(tr.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(tr.Events))
	}
	if !tr.Events[0].Failed {
		t.Errorf("the +1-1 edit should carry its own item's failure")
	}
	if tr.Events[1].Failed {
		t.Errorf("the +3-2 edit was marked failed by the other edit's item")
	}
}

func TestSubAgentActivityLinksChildThread(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		responseItem(1, map[string]any{"type": "function_call", "name": "spawn_agent", "call_id": "call_a",
			"arguments": `{"task_name":"parser_audit","message":"audit the parser"}`}),
		item(2, map[string]any{"type": "SubAgentActivity", "id": "call_a", "kind": "started",
			"agent_thread_id": childID, "agent_path": "/root/parser_audit"}),
		item(3, map[string]any{"type": "SubAgentActivity", "id": "call_b", "kind": "interacted",
			"agent_thread_id": childID, "agent_path": "/root/parser_audit"}),
		item(4, map[string]any{"type": "SubAgentActivity", "id": "subagent-completed-1", "kind": "completed",
			"agent_thread_id": childID, "agent_path": "/root/parser_audit"}),
	})

	tr := load(t, threadID, f)
	if got, want := kinds(tr), "agent"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	e := tr.Events[0]
	if e.AgentID != childID {
		t.Errorf("AgentID = %q, want the child thread id %q", e.AgentID, childID)
	}
	if e.Tool != "spawn_agent" || e.Tag != "completed" || e.Pending {
		t.Errorf("spawn = tool %q tag %q pending %v, want spawn_agent/completed/false", e.Tool, e.Tag, e.Pending)
	}
	if tr.Ignored["item:SubAgentActivity:interacted"] != 1 {
		t.Errorf("interacted not counted: %v", tr.Ignored)
	}
}

func TestSubAgentActivityWithoutSpawnCall(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		item(1, map[string]any{"type": "SubAgentActivity", "id": "call_a", "kind": "started",
			"agent_thread_id": childID, "agent_path": "/root/parser_audit"}),
		item(2, map[string]any{"type": "SubAgentActivity", "id": "call_b", "kind": "interrupted",
			"agent_thread_id": childID, "agent_path": "/root/parser_audit"}),
	})

	tr := load(t, threadID, f)
	if got, want := kinds(tr), "agent"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	e := tr.Events[0]
	if e.AgentID != childID || e.Agent != "/root/parser_audit" {
		t.Errorf("event = %+v, want the child thread id and path", e)
	}
	if e.Tag != "interrupted" || !e.Failed {
		t.Errorf("tag = %q failed = %v, want interrupted/true", e.Tag, e.Failed)
	}
}

// token_count carries a running total and is written more often than the model
// is called, so what it grew by is what a model spent. Adding up the
// per-response figure beside it charged one session 47.1M tokens against a
// total of 46.0M, which is the kind of number an agent repeats.
func TestTokenCountChargesTheGrowthToTheCurrentModel(t *testing.T) {
	home := t.TempDir()
	total := func(at float64, in, cached, out int) rec {
		return rec{at: at, typ: "event_msg", payload: map[string]any{
			"type": "token_count",
			"info": map[string]any{
				"total_token_usage": map[string]any{"input_tokens": in, "cached_input_tokens": cached,
					"output_tokens": out, "reasoning_output_tokens": out / 2, "total_tokens": in + out},
				"last_token_usage":     map[string]any{"total_tokens": 999999},
				"model_context_window": 258400,
			},
			"rate_limits": map[string]any{
				"primary":   map[string]any{"used_percent": 67.0, "window_minutes": 300, "resets_at": t0.Add(2 * time.Hour).Unix()},
				"secondary": map[string]any{"used_percent": 37.0, "window_minutes": 10080, "resets_at": t0.Add(48 * time.Hour).Unix()},
				"credits":   nil,
			},
		}}
	}
	turn := func(at float64, model string) rec {
		return rec{at: at, typ: "turn_context", payload: map[string]any{"model": model}}
	}
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, nil),
		turn(1, "gpt-5.5"),
		total(2, 1000, 900, 100),
		total(3, 1000, 900, 100), // rewritten unchanged: not a second response
		total(4, 3000, 2700, 300),
		turn(5, "gpt-5.5-codex"),
		total(6, 5000, 4500, 500),
	})

	u := load(t, threadID, f).Usage
	if u.Total != 5500 {
		t.Errorf("Total = %d, want 5500 (the last running total, not the sum)", u.Total)
	}
	if u.Input != 500 || u.CacheRead != 4500 || u.Output != 500 || u.Reasoning != 250 {
		t.Errorf("usage = %+v, want the cached input split out of the input", u)
	}
	if u.Requests != 3 {
		t.Errorf("Requests = %d, want 3 growths from 4 records", u.Requests)
	}
	if u.ByModel["gpt-5.5"] != 3300 || u.ByModel["gpt-5.5-codex"] != 2200 {
		t.Errorf("ByModel = %v, want each model charged what it added", u.ByModel)
	}
	if got := u.ByModel["gpt-5.5"] + u.ByModel["gpt-5.5-codex"]; int64(got) != u.Total {
		t.Errorf("models add up to %d, total is %d", got, u.Total)
	}
	if u.ContextWindow != 258400 {
		t.Errorf("ContextWindow = %d", u.ContextWindow)
	}
	if len(u.Limits) != 2 {
		t.Fatalf("Limits = %+v, want primary and secondary", u.Limits)
	}
	p := u.Limits[0]
	if p.Name != "primary" || p.Percent != 67 || p.Window != 5*time.Hour {
		t.Errorf("primary = %+v, want the 5h window at 67%%", p)
	}
	if !p.Resets.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("Resets = %v, want %v", p.Resets, t0.Add(2*time.Hour))
	}
	// A reading is only as good as when it was taken.
	if !p.Taken.Equal(t0.Add(6 * time.Second)) {
		t.Errorf("Taken = %v, want the last record's time", p.Taken)
	}
	if u.Source == "" {
		t.Error("Source is empty, want it to name the records")
	}
}
