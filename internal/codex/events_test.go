package codex

import (
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

func event(at float64, p map[string]any) rec { return rec{at: at, typ: "event_msg", payload: p} }

// The event_msg records that are a step of the session, and the ones that
// change what the session is called or aiming at.
func TestEventMessages(t *testing.T) {
	type want struct{ kind, tag, text string }
	for _, tc := range []struct {
		name string
		recs []rec
		want []want
		// checks beyond the events themselves
		check func(t *testing.T, tr *transcript.Transcript)
	}{
		{"prompt with pasted images", []rec{
			event(1, map[string]any{"type": "user_message", "message": "what is wrong here", "images": []string{"data:a", "data:b"}})},
			[]want{{"user", "", "what is wrong here [2 image(s)]"}}, nil},
		{"prompt that is only an image", []rec{
			event(1, map[string]any{"type": "user_message", "message": "", "images": []string{"data:a"}})},
			[]want{{"user", "", "[1 image(s)]"}}, nil},
		{"turn aborted", []rec{
			event(1, map[string]any{"type": "turn_aborted", "reason": "interrupted", "turn_id": "t1"})},
			[]want{{"notice", transcript.TagInterrupted, "turn aborted: interrupted"}},
			func(t *testing.T, tr *transcript.Transcript) {
				if tr.Session.Ending != transcript.EndingInterrupted {
					t.Errorf("Ending = %q", tr.Session.Ending)
				}
			}},
		{"stream error", []rec{
			event(1, map[string]any{"type": "error", "message": "stream disconnected before completion"})},
			[]want{{"error", "", "stream disconnected before completion"}}, nil},
		{"usage limit named only in the error info", []rec{
			event(1, map[string]any{"type": "error", "message": "request failed", "codex_error_info": "usage_limit_exceeded"})},
			[]want{{"error", transcript.TagUsageLimit, "request failed"}}, nil},
		{"a turn that completed cleanly adds nothing", []rec{
			event(1, map[string]any{"type": "task_complete", "last_agent_message": "done", "turn_id": "t1"})}, nil, nil},
		// The goal record is rewritten as time and tokens tick over; only a
		// change of objective or status is a step.
		{"goal set, repeated, then completed", []rec{
			event(1, map[string]any{"type": "thread_goal_updated", "goal": map[string]any{"objective": "ship the parser", "status": "active", "tokensUsed": 10}}),
			event(2, map[string]any{"type": "thread_goal_updated", "goal": map[string]any{"objective": "ship the parser", "status": "active", "tokensUsed": 20}}),
			event(3, map[string]any{"type": "thread_goal_updated", "goal": map[string]any{"objective": "ship the parser", "status": "complete"}}),
			event(4, map[string]any{"type": "thread_goal_updated", "goal": map[string]any{"objective": ""}})},
			[]want{{"plan", "goal", "goal: ship the parser [active]"}, {"plan", "goal", "goal: ship the parser [complete]"}},
			func(t *testing.T, tr *transcript.Transcript) {
				if tr.Goal != "ship the parser [complete]" {
					t.Errorf("Goal = %q", tr.Goal)
				}
			}},
		{"goal without a status", []rec{
			event(1, map[string]any{"type": "thread_goal_updated", "goal": map[string]any{"objective": "ship it"}})},
			[]want{{"plan", "goal", "goal: ship it"}}, nil},
		{"the first name a thread is given is its title", []rec{
			event(1, map[string]any{"type": "thread_name_updated", "thread_name": "Parser audit"}),
			event(2, map[string]any{"type": "thread_name_updated", "thread_name": "Later name"})},
			nil,
			func(t *testing.T, tr *transcript.Transcript) {
				if tr.Session.Title != "Parser audit" {
					t.Errorf("Title = %q", tr.Session.Title)
				}
			}},
		{"rollback", []rec{
			event(1, map[string]any{"type": "thread_rolled_back", "num_turns": 2})},
			[]want{{"notice", transcript.TagRollback, "user rolled back 2 turn(s)"}}, nil},
		{"review mode", []rec{
			event(1, map[string]any{"type": "entered_review_mode"}), event(2, map[string]any{"type": "exited_review_mode"})},
			[]want{{"notice", "", "entered review mode"}, {"notice", "", "exited review mode"}}, nil},
		// Drift: an event type added after this build stands in for itself.
		{"an event this build has never seen", []rec{
			event(1, map[string]any{"type": "mcp_startup_complete", "servers": []string{"db"}})},
			[]want{{"record", "event_msg:mcp_startup_complete", ""}},
			func(t *testing.T, tr *transcript.Transcript) {
				if tr.Unknowns["event_msg:mcp_startup_complete"] != 1 {
					t.Errorf("Unknowns = %v", tr.Unknowns)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadRecs(t, tc.recs...)
			if len(tr.Events) != len(tc.want) {
				t.Fatalf("events = %q, want %d", kinds(tr), len(tc.want))
			}
			for i, w := range tc.want {
				e := tr.Events[i]
				if string(e.Kind) != w.kind || e.Tag != w.tag || (w.text != "" && e.Text != w.text) {
					t.Errorf("event %d = kind %q tag %q text %q, want %+v", i, e.Kind, e.Tag, e.Text, w)
				}
			}
			if tc.check != nil {
				tc.check(t, tr)
			}
		})
	}
}

// Older CLI versions logged a command's end and a patch's result as events
// beside the call rather than as outputs or items.
func TestLegacyCompletionEvents(t *testing.T) {
	one := 1
	t.Run("exec_command_end answers its call", func(t *testing.T) {
		tr := loadRecs(t,
			call(1, "shell", "c1", map[string]any{"command": []string{"bash", "-lc", "make"}}),
			event(2, map[string]any{"type": "exec_command_end", "call_id": "c1", "command": []string{"bash", "-lc", "make"},
				"exit_code": 2, "aggregated_output": "make: *** no rule"}),
		)
		if e := tr.Events[0]; len(tr.Events) != 1 || e.Pending || !e.Failed || e.Output != "make: *** no rule" || *e.ExitCode != 2 {
			t.Errorf("events %q, first %+v", kinds(tr), e)
		}
	})
	t.Run("exec_command_end without a call is the command", func(t *testing.T) {
		tr := loadRecs(t,
			event(1, map[string]any{"type": "exec_command_end", "call_id": "c9", "command": []string{"/bin/zsh", "-lc", "ls"},
				"exit_code": one, "aggregated_output": "denied"}),
		)
		if e := tr.Events[0]; e.Tool != "shell" || e.Input != "ls" || !e.Failed || e.Pending {
			t.Errorf("event = %+v", e)
		}
	})
	t.Run("a failed patch keeps its stderr", func(t *testing.T) {
		tr := loadRecs(t,
			responseItem(1, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "p1",
				"input": "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch"}),
			event(2, map[string]any{"type": "patch_apply_end", "call_id": "p1", "success": false, "stderr": "context not found"}),
		)
		if e := tr.Events[0]; !e.Failed || e.Output != "context not found" {
			t.Errorf("event = failed %v output %q", e.Failed, e.Output)
		}
	})
	t.Run("a successful patch end changes nothing", func(t *testing.T) {
		tr := loadRecs(t,
			responseItem(1, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "p1",
				"input": "*** Begin Patch\n*** Update File: a.go\n@@\n-x\n+y\n*** End Patch"}),
			event(2, map[string]any{"type": "patch_apply_end", "call_id": "p1", "success": true, "stdout": "ok"}),
		)
		if e := tr.Events[0]; e.Failed {
			t.Errorf("event = %+v", e)
		}
	})
}

// A collab spawn's end record names the child thread. It answers the
// spawn_agent call it belongs to, or stands for the spawn when that call was
// not logged.
func TestCollabSpawnEnd(t *testing.T) {
	spawnEnd := func(at float64, callID string) rec {
		return event(at, map[string]any{"type": "collab_agent_spawn_end", "call_id": callID, "new_thread_id": childID,
			"new_agent_nickname": "Hooke", "new_agent_role": "worker", "prompt": "audit the parser"})
	}
	t.Run("answers its call", func(t *testing.T) {
		tr := loadRecs(t,
			call(1, "spawn_agent", "c1", map[string]any{"agent_type": "worker", "message": "audit the parser"}),
			spawnEnd(2, "c1"),
		)
		e := tr.Events[0]
		if len(tr.Events) != 1 || e.AgentID != childID || e.Agent != "Hooke (worker)" || e.Pending || e.Output != "spawned" {
			t.Errorf("events %q, spawn %+v", kinds(tr), e)
		}
	})
	t.Run("stands for a spawn that was not logged", func(t *testing.T) {
		tr := loadRecs(t, spawnEnd(1, "c1"))
		e := tr.Events[0]
		if e.Kind != transcript.KindAgent || e.AgentID != childID || e.Text != "audit the parser" || e.Pending {
			t.Errorf("spawn = %+v", e)
		}
	})
	// Versions with CollabAgentToolCall items log the spawn as a spawn_agent
	// call already; a lone end record there is not a second spawn.
	t.Run("not duplicated where items log the spawn", func(t *testing.T) {
		tr := loadRecs(t,
			item(0.5, map[string]any{"type": "CollabAgentToolCall", "tool": "spawn_agent", "status": "completed"}),
			spawnEnd(1, "c1"),
		)
		if len(tr.Events) != 0 {
			t.Errorf("events = %q, want none", kinds(tr))
		}
	})
}

// Messages between agents in a multi-agent thread. The text opens with a
// header naming the message type; the payload after it is plain text or
// encrypted, depending on the kind of message.
func TestInterAgentMessages(t *testing.T) {
	msg := func(content ...map[string]any) rec {
		return responseItem(1, map[string]any{"type": "agent_message", "author": "/root/audit", "recipient": "/root", "content": content})
	}
	encrypted := map[string]any{"type": "encrypted_content", "encrypted_content": "gAAAAA" + strings.Repeat("x", 80)}
	for _, tc := range []struct {
		name      string
		rec       rec
		tag, text string
	}{
		{"final answer with the payload inline", msg(map[string]any{"type": "input_text",
			"text": "Message Type: FINAL_ANSWER\nTask name: /root/audit\nSender: /root/audit\nPayload:\nNo defects found."}),
			"final_answer", "No defects found."},
		{"new task whose payload is encrypted", msg(map[string]any{"type": "input_text",
			"text": "Message Type: NEW_TASK\nTask name: /root/audit\nSender: /root\nPayload:"}, encrypted),
			"new_task", "[encrypted]"},
		{"message with an empty payload line and encrypted body", msg(map[string]any{"type": "input_text",
			"text": "Message Type: MESSAGE\nTask name: /root/audit\nSender: /root\nPayload:\n"}, encrypted),
			"message", "[encrypted]"},
		// Drift: no header at all. The text is still the message.
		{"text without the header", msg(map[string]any{"type": "input_text", "text": "plain words"}), "", "plain words"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadRecs(t, tc.rec)
			if len(tr.Events) != 1 {
				t.Fatalf("events = %q", kinds(tr))
			}
			e := tr.Events[0]
			if e.Kind != transcript.KindAgent || e.Agent != "/root/audit → /root" || e.Tag != tc.tag || e.Text != tc.text {
				t.Errorf("event = agent %q tag %q text %q, want tag %q text %q", e.Agent, e.Tag, e.Text, tc.tag, tc.text)
			}
		})
	}
}

// Items of the typed stream in the shapes current versions write, including
// the statuses and change types that are rare but real.
func TestItemShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		it    map[string]any
		check func(t *testing.T, e transcript.Event)
	}{
		{"a command killed before it exited", map[string]any{"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", "sleep 100"},
			"status": "declined", "stdout": "", "stderr": ""},
			func(t *testing.T, e transcript.Event) {
				if !e.Failed || e.ExitCode != nil || e.Output != "[declined]" {
					t.Errorf("event = failed %v exit %v output %q", e.Failed, e.ExitCode, e.Output)
				}
			}},
		{"a command with separate stdout and stderr", map[string]any{"type": "CommandExecution", "command": []string{"ls"},
			"status": "completed", "exit_code": 0, "stdout": "a", "stderr": "warn"},
			func(t *testing.T, e transcript.Event) {
				if e.Failed || e.Output != "a\nwarn" {
					t.Errorf("event = failed %v output %q", e.Failed, e.Output)
				}
			}},
		{"deleted, renamed and declined changes", map[string]any{"type": "FileChange", "status": "declined", "changes": map[string]any{
			"/w/old.go": map[string]any{"type": "delete", "content": "package w\nfunc A() {}\n"},
			"/w/b.go":   map[string]any{"type": "update", "unified_diff": "", "move_path": "/w/c.go"},
			"/w/gone":   map[string]any{"type": "delete"}}},
			func(t *testing.T, e transcript.Event) {
				if !e.Failed || len(e.Changes) != 3 {
					t.Fatalf("event = failed %v changes %+v", e.Failed, e.Changes)
				}
				// Sorted by path: b.go, gone, old.go.
				if c := e.Changes[0]; c.Op != "update" || c.MoveTo != "/w/c.go" || c.Added+c.Removed != 0 {
					t.Errorf("rename = %+v", c)
				}
				if c := e.Changes[1]; c.Op != "delete" || c.Removed != 0 {
					t.Errorf("delete without content = %+v", c)
				}
				if c := e.Changes[2]; c.Op != "delete" || c.Removed != 2 {
					t.Errorf("delete with content = %+v", c)
				}
			}},
		{"an MCP call that failed with an error object", map[string]any{"type": "McpToolCall", "server": "db", "tool": "query", "status": "failed",
			"arguments": map[string]any{"q": "select"}, "error": map[string]any{"message": "timeout"}},
			func(t *testing.T, e transcript.Event) {
				if !e.Failed || e.Tool != "db.query" || !strings.Contains(e.Output, "timeout") {
					t.Errorf("event = %+v", e)
				}
			}},
		{"an MCP call that failed inside its result", map[string]any{"type": "McpToolCall", "server": "db", "tool": "query", "status": "failed",
			"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "syntax error"}, {"type": "image"}}, "isError": true}},
			func(t *testing.T, e transcript.Event) {
				if !e.Failed || e.Output != "syntax error\n[image]" {
					t.Errorf("event = failed %v output %q", e.Failed, e.Output)
				}
			}},
		{"an MCP result with structured content only", map[string]any{"type": "McpToolCall", "server": "db", "tool": "count", "status": "completed",
			"result": map[string]any{"structuredContent": map[string]any{"n": 3}}},
			func(t *testing.T, e transcript.Event) {
				if e.Failed || !strings.Contains(e.Output, `"n":3`) {
					t.Errorf("event = failed %v output %q", e.Failed, e.Output)
				}
			}},
		{"a dynamic tool call", map[string]any{"type": "DynamicToolCall", "tool": "js", "status": "failed", "arguments": map[string]any{"code": "1/0"},
			"content_items": []map[string]any{{"type": "inputText", "text": "RangeError"}}},
			func(t *testing.T, e transcript.Event) {
				if e.Tool != "js" || !e.Failed || e.Output != "RangeError" || e.Input != `{"code":"1/0"}` {
					t.Errorf("event = %+v", e)
				}
			}},
		{"a web search", map[string]any{"type": "WebSearch", "query": "", "action": map[string]any{"type": "search", "queries": []string{"go 1.30", "go release"}}},
			func(t *testing.T, e transcript.Event) {
				if e.Tool != "web_search" || e.Input != "go 1.30 | go release" {
					t.Errorf("event = %+v", e)
				}
			}},
		{"an extension's web search with results", map[string]any{"type": "Extension", "kind": "web.search", "query": "codex rollout",
			"results": []map[string]any{{"title": "Docs", "url": "https://example.com"}}},
			func(t *testing.T, e transcript.Event) {
				if e.Tool != "web.search" || e.Input != "codex rollout" || e.Output != "Docs https://example.com" {
					t.Errorf("event = %+v", e)
				}
			}},
		{"an extension with nothing but a kind", map[string]any{"type": "Extension", "kind": "clock.sleep", "durationMs": 100},
			func(t *testing.T, e transcript.Event) {
				if e.Tool != "clock.sleep" {
					t.Errorf("event = %+v", e)
				}
			}},
		{"an image viewed by file URL", map[string]any{"type": "ImageView", "path": "file:///w/shot.png"},
			func(t *testing.T, e transcript.Event) {
				if e.Tool != "view_image" || e.Input != "/w/shot.png" {
					t.Errorf("event = %+v", e)
				}
			}},
		{"a plan item", map[string]any{"type": "Plan", "text": "1. read\n2. fix"},
			func(t *testing.T, e transcript.Event) {
				if e.Kind != transcript.KindPlan || e.Text != "1. read\n2. fix" {
					t.Errorf("event = %+v", e)
				}
			}},
		{"reasoning with only raw content", map[string]any{"type": "Reasoning", "summary_text": []string{}, "raw_content": []string{"thinking aloud"}},
			func(t *testing.T, e transcript.Event) {
				if e.Kind != transcript.KindReasoning || e.Text != "thinking aloud" {
					t.Errorf("event = %+v", e)
				}
			}},
		// Drift: an item type added after this build.
		{"an item type this build has never seen", map[string]any{"type": "BrowserSession", "url": "http://localhost"},
			func(t *testing.T, e transcript.Event) {
				if e.Tag != "item:BrowserSession" {
					t.Errorf("event = %+v", e)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadRecs(t, item(1, tc.it))
			if len(tr.Events) != 1 {
				t.Fatalf("events = %q, want one", kinds(tr))
			}
			tc.check(t, tr.Events[0])
		})
	}
}

// Legacy reasoning records repeat what reasoning items already said; only one
// of the two is read.
func TestLegacyReasoning(t *testing.T) {
	legacy := responseItem(2, map[string]any{"type": "reasoning", "summary": []map[string]any{{"type": "summary_text", "text": "checking the list"}},
		"encrypted_content": "gAAAAA" + strings.Repeat("x", 80)})
	if tr := loadRecs(t, legacy); kinds(tr) != "reasoning" || tr.Events[0].Text != "checking the list" {
		t.Errorf("legacy only: %q", kinds(tr))
	}
	tr := loadRecs(t, item(1, map[string]any{"type": "Reasoning", "summary_text": []string{"checking the list"}}), legacy)
	if kinds(tr) != "reasoning" {
		t.Errorf("with items: %q, want the summary once", kinds(tr))
	}
}

// Old versions wrote turn_context without a payload wrapper, and compacted
// records with the summary in them.
func TestOlderRecordShapes(t *testing.T) {
	home := t.TempDir()
	f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{meta(threadID, nil)})
	appendLine(t, f, `{"timestamp":"2026-09-15T14:00:01Z","type":"turn_context","model":"gpt-5-codex","cwd":"/work/moved"}`)
	appendLine(t, f, `{"timestamp":"2026-09-15T14:00:02Z","type":"compacted","payload":{"message":"Summary: fixed the parser."}}`)
	appendLine(t, f, `{"timestamp":"2026-09-15T14:00:03Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"p1","input":"*** Begin Patch\n*** Add File: notes.md\n+x\n*** End Patch"}}`)
	tr := load(t, threadID, f)
	if tr.Session.Model != "gpt-5-codex" {
		t.Errorf("Model = %q", tr.Session.Model)
	}
	if got := kinds(tr); got != "notice edit" {
		t.Fatalf("kinds = %q", got)
	}
	if !strings.HasSuffix(tr.Events[0].Text, "\nSummary: fixed the parser.") {
		t.Errorf("compaction = %q", tr.Events[0].Text)
	}
	// The patch's relative path is resolved against the turn's directory.
	if p := tr.Events[1].Changes[0].Path; p != "/work/moved/notes.md" {
		t.Errorf("path = %q", p)
	}
}
