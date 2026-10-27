package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"agentlog/internal/transcript"
)

// loadRecs writes one rollout for the thread and reads it back.
func loadRecs(t *testing.T, recs ...rec) *transcript.Transcript {
	t.Helper()
	f := writeRollout(t, t.TempDir(), "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", append([]rec{meta(threadID, nil)}, recs...))
	return load(t, threadID, f)
}

func call(at float64, name, callID string, args any) rec {
	s, ok := args.(string)
	if !ok {
		b, _ := json.Marshal(args)
		s = string(b)
	}
	return responseItem(at, map[string]any{"type": "function_call", "name": name, "call_id": callID, "arguments": s})
}

func output(at float64, callID string, out any) rec {
	return responseItem(at, map[string]any{"type": "function_call_output", "call_id": callID, "output": out})
}

// Each function Codex exposes is rendered from the argument that says what it
// did. The argument shapes are the ones current and past CLI versions write.
func TestFunctionCallRendersEachTool(t *testing.T) {
	encrypted := "gAAAAA" + strings.Repeat("x", 80)
	for _, tc := range []struct {
		name  string
		tool  string
		args  any
		want  transcript.Event // only the fields set here are compared
		steps int              // plan steps expected
	}{
		{"exec_command", "exec_command", map[string]any{"cmd": "go test ./...", "workdir": "/w", "yield_time_ms": 1000},
			transcript.Event{Kind: transcript.KindTool, Tool: "shell", Input: "go test ./..."}, 0},
		{"shell_command takes a string", "shell_command", map[string]any{"command": "ls -la", "workdir": "/w"},
			transcript.Event{Kind: transcript.KindTool, Tool: "shell", Input: "ls -la"}, 0},
		{"old shell takes an argv", "shell", map[string]any{"command": []string{"bash", "-lc", "make build"}, "workdir": "/w"},
			transcript.Event{Kind: transcript.KindTool, Tool: "shell", Input: "make build"}, 0},
		{"argv without a shell wrapper", "shell", map[string]any{"command": []string{"rg", "-n", "TODO"}},
			transcript.Event{Kind: transcript.KindTool, Tool: "shell", Input: "rg -n TODO"}, 0},
		{"apply_patch as a function", "apply_patch", map[string]any{"input": "*** Begin Patch\n*** Add File: notes.md\n+hello\n*** End Patch"},
			transcript.Event{Kind: transcript.KindEdit, Input: "add notes.md (+1 -0)"}, 0},
		{"plan with an explanation", "update_plan", map[string]any{"explanation": "reordered", "plan": []map[string]any{
			{"step": "read", "status": "completed"}, {"step": "fix", "status": "in_progress"}}},
			transcript.Event{Kind: transcript.KindPlan, Text: "reordered"}, 2},
		{"spawn with a message", "spawn_agent", map[string]any{"agent_type": "worker", "message": "audit the parser"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "worker", Text: "audit the parser"}, 0},
		{"spawn whose message is encrypted", "spawn_agent", map[string]any{"task_name": "parser_audit", "message": encrypted},
			transcript.Event{Kind: transcript.KindAgent, Agent: "parser_audit", Text: "[encrypted]"}, 0},
		// Some versions send the prompt as content items (text plus pasted
		// images) instead of a message string.
		{"spawn with the prompt as items", "spawn_agent", map[string]any{"agent_type": "worker", "fork_context": false, "items": []map[string]any{
			{"type": "text", "text": "build the prototype"}, {"type": "local_image", "path": "/w/sketch.png"}}},
			transcript.Event{Kind: transcript.KindAgent, Agent: "worker", Text: "build the prototype\n[image]"}, 0},
		{"send_input with items", "send_input", map[string]any{"target": "worker-1", "interrupt": false, "items": []map[string]any{
			{"type": "text", "text": "use the new schema"}}},
			transcript.Event{Kind: transcript.KindAgent, Agent: "worker-1", Text: "use the new schema"}, 0},
		{"message to a subagent", "send_message", map[string]any{"target": "/root/audit", "message": "status?"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "/root/audit", Text: "status?"}, 0},
		{"follow-up task", "followup_task", map[string]any{"target": "/root/audit", "message": "now the writer"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "/root/audit", Text: "now the writer"}, 0},
		{"closing a subagent", "close_agent", map[string]any{"target": "/root/audit"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "/root/audit"}, 0},
		{"resuming one by id", "resume_agent", map[string]any{"id": childID},
			transcript.Event{Kind: transcript.KindAgent, Agent: childID}, 0},
		{"waiting on subagents", "wait_agent", map[string]any{"targets": []string{"/root/a"}, "timeout_ms": 30000},
			transcript.Event{Kind: transcript.KindTool, Tool: "wait_agent", Input: `{"targets":["/root/a"],"timeout_ms":30000}`}, 0},
		{"image", "view_image", map[string]any{"path": "/w/shot.png", "detail": "high"},
			transcript.Event{Kind: transcript.KindTool, Tool: "view_image", Input: "/w/shot.png"}, 0},
		{"MCP tool", "mcp__browser__click", map[string]any{"selector": "#play"},
			transcript.Event{Kind: transcript.KindTool, Tool: "browser.click", Input: `{"selector":"#play"}`}, 0},
		{"a tool this build has never heard of", "request_user_input_async", map[string]any{"questions": []map[string]any{{"title": "which?"}}},
			transcript.Event{Kind: transcript.KindTool, Tool: "request_user_input_async", Input: `{"questions":[{"title":"which?"}]}`}, 0},
		// The model can emit arguments that are not JSON at all. The call is
		// still shown, with what it sent.
		{"arguments that are not JSON", "exec_command", "{cmd: oops",
			transcript.Event{Kind: transcript.KindTool, Tool: "shell"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadRecs(t, call(1, tc.tool, "call_1", tc.args))
			if len(tr.Events) != 1 {
				t.Fatalf("events = %q, want one", kinds(tr))
			}
			got, w := tr.Events[0], tc.want
			if got.Kind != w.Kind {
				t.Errorf("Kind = %q, want %q", got.Kind, w.Kind)
			}
			for _, f := range []struct{ name, got, want string }{
				{"Tool", got.Tool, w.Tool}, {"Input", got.Input, w.Input}, {"Agent", got.Agent, w.Agent}, {"Text", got.Text, w.Text},
			} {
				if f.want != "" && f.got != f.want {
					t.Errorf("%s = %q, want %q", f.name, f.got, f.want)
				}
			}
			if len(got.Plan) != tc.steps {
				t.Errorf("plan = %+v, want %d steps", got.Plan, tc.steps)
			}
			// A plan update is answered by being made; everything else waits
			// for its output.
			if wantPending := w.Kind != transcript.KindPlan; got.Pending != wantPending {
				t.Errorf("Pending = %v, want %v", got.Pending, wantPending)
			}
		})
	}
}

// The output of a call is a string whose shape depends on the tool and the CLI
// version. Exit codes live in a header the harness puts in front of it, in
// one of several spellings, or in a JSON wrapper around it.
func TestFunctionOutputIsUnwrapped(t *testing.T) {
	zero, one, two := 0, 1, 2
	for _, tc := range []struct {
		name   string
		call   rec
		out    any
		output string
		exit   *int
		failed bool
	}{
		{"unified exec header", call(1, "exec_command", "c1", map[string]any{"cmd": "go test"}),
			"Chunk ID: 4f2a\nWall time: 1.2 seconds\nProcess exited with code 1\nOriginal token count: 3\nOutput:\nFAIL", "FAIL", &one, true},
		{"unified exec header with the command echoed", call(1, "exec_command", "c1", map[string]any{"cmd": "true"}),
			"Command: /bin/zsh -lc true\nChunk ID: 1\nWall time: 0.0 seconds\nProcess exited with code 0\nOriginal token count: 0\nOutput:\n", "", &zero, false},
		// shell_command in 0.61 and 0.14x writes "Exit code: N" instead.
		{"shell_command header", call(1, "shell_command", "c1", map[string]any{"command": "brew list"}),
			"Exit code: 2\nWall time: 0.1 seconds\nOutput:\nError: no such keg", "Error: no such keg", &two, true},
		// A command still running when its output was taken has no exit code
		// yet, and has not failed.
		{"still running", call(1, "exec_command", "c1", map[string]any{"cmd": "npm run dev"}),
			"Chunk ID: 1\nWall time: 10.0 seconds\nProcess running with session ID 7\nOriginal token count: 4\nOutput:\nready", "", nil, false},
		{"JSON wrapper with metadata", call(1, "apply_patch", "c1", map[string]any{"input": "*** Begin Patch\n*** Add File: a\n+x\n*** End Patch"}),
			`{"output":"Success. Updated the following files:\nA a\n","metadata":{"exit_code":0,"duration_seconds":0.0}}`,
			"Success. Updated the following files:\nA a\n", &zero, false},
		{"JSON wrapper reporting a failure", call(1, "shell", "c1", map[string]any{"command": []string{"bash", "-lc", "false"}}),
			`{"output":"","metadata":{"exit_code":1}}`, "", &one, true},
		// A tool whose output happens to be JSON is not a wrapper.
		{"JSON output that is not a wrapper", call(1, "mcp__db__query", "c1", map[string]any{"q": "select 1"}),
			`{"rows":[[1]]}`, `{"rows":[[1]]}`, nil, false},
		{"content items with an image", call(1, "mcp__browser__screenshot", "c1", map[string]any{}),
			[]map[string]any{{"type": "input_text", "text": "captured"}, {"type": "input_image", "image_url": "data:image/png;base64,AAAA"}},
			"captured\n[image]", nil, false},
		// Drift: an output that is neither a string nor a list of items.
		{"an output of an unexpected shape is kept raw", call(1, "mcp__db__query", "c1", map[string]any{}),
			map[string]any{"rows": 1}, `{"rows":1}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadRecs(t, tc.call, output(2, "c1", tc.out))
			if len(tr.Events) != 1 {
				t.Fatalf("events = %q, want the call alone", kinds(tr))
			}
			e := tr.Events[0]
			if tc.output != "" && e.Output != tc.output {
				t.Errorf("Output = %q, want %q", e.Output, tc.output)
			}
			if (e.ExitCode == nil) != (tc.exit == nil) || (e.ExitCode != nil && *e.ExitCode != *tc.exit) {
				t.Errorf("ExitCode = %v, want %v", deref(e.ExitCode), deref(tc.exit))
			}
			if e.Failed != tc.failed || e.Pending {
				t.Errorf("Failed = %v Pending = %v, want failed %v", e.Failed, e.Pending, tc.failed)
			}
		})
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// Code mode runs a JavaScript cell that calls tools. The cell is shown as the
// one call it made when there is one; when it made several and the item
// stream logged each, the cell itself is not shown again.
func TestCodeModeCell(t *testing.T) {
	exec := func(at float64, callID, code string) rec {
		return responseItem(at, map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": callID, "input": code})
	}
	cellOut := func(at float64, callID, status, body string) rec {
		return responseItem(at, map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": []map[string]any{
			{"type": "input_text", "text": "Script " + status + "\nWall time 0.1 seconds\nOutput:\n"}, {"type": "input_text", "text": body}}})
	}
	t.Run("one MCP call is shown as that call", func(t *testing.T) {
		tr := loadRecs(t, exec(1, "c1", `text(await tools.mcp__browser__navigate({url:"http://localhost"}))`))
		if e := tr.Events[0]; e.Tool != "browser.navigate" || e.Kind != transcript.KindTool {
			t.Errorf("event = tool %q kind %q", e.Tool, e.Kind)
		}
	})
	t.Run("one shell call with a command built at run time stays the cell", func(t *testing.T) {
		tr := loadRecs(t, exec(1, "c1", `const cmd = "ls"; text(await tools.exec_command({cmd}))`))
		if e := tr.Events[0]; e.Tool != "exec" || !strings.Contains(e.Input, "const cmd") {
			t.Errorf("event = tool %q input %q", e.Tool, e.Input)
		}
	})
	t.Run("a cell that failed is a failed call", func(t *testing.T) {
		tr := loadRecs(t,
			exec(1, "c1", `text(tab.url)`),
			cellOut(2, "c1", "failed", "Script error:\nReferenceError: tab is not defined"),
		)
		if e := tr.Events[0]; !e.Failed || !strings.Contains(e.Output, "ReferenceError") {
			t.Errorf("event = failed %v output %q", e.Failed, e.Output)
		}
	})
	t.Run("a cell that completed is not failed", func(t *testing.T) {
		tr := loadRecs(t, exec(1, "c1", `text("hi")`), cellOut(2, "c1", "completed", "hi"))
		if e := tr.Events[0]; e.Failed || e.Pending {
			t.Errorf("event = failed %v pending %v", e.Failed, e.Pending)
		}
	})
	t.Run("several calls the items logged are not shown twice", func(t *testing.T) {
		code := `await tools.exec_command({cmd:"ls"}); await tools.apply_patch("*** Begin Patch"); await tools.mcp__db__query({})`
		tr := loadRecs(t,
			item(0.5, map[string]any{"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", "ls"}, "exit_code": 0, "status": "completed"}),
			item(0.6, map[string]any{"type": "FileChange", "status": "completed", "changes": map[string]any{"/w/a": map[string]any{"type": "add", "content": "x\n"}}}),
			item(0.7, map[string]any{"type": "McpToolCall", "server": "db", "tool": "query", "status": "completed"}),
			exec(1, "c1", code),
			cellOut(2, "c1", "completed", "done"),
		)
		if got := kinds(tr); got != "tool edit tool" {
			t.Errorf("kinds = %q, want the three items and no cell", got)
		}
		if tr.Unpaired != 0 {
			t.Errorf("the skipped cell's output was reported as unpaired")
		}
	})
	t.Run("several calls the items did not log keep the cell", func(t *testing.T) {
		tr := loadRecs(t, exec(1, "c1", `await tools.exec_command({cmd:"ls"}); await tools.view_image({path:"a.png"})`))
		if e := tr.Events[0]; len(tr.Events) != 1 || e.Tool != "exec" {
			t.Errorf("events = %q, tool %q", kinds(tr), e.Tool)
		}
	})
}

// An output whose call this build cannot find — the call is before a tail
// window, or the two records disagree about ids — is shown on its own.
func TestOutputWithoutItsCallIsKept(t *testing.T) {
	tr := loadRecs(t,
		output(1, "call_gone", "Exit code: 0\nWall time: 0.1 seconds\nOutput:\nall green"),
		responseItem(2, map[string]any{"type": "custom_tool_call_output", "call_id": "call_gone2", "output": "Success."}),
	)
	if got := kinds(tr); got != "record record" {
		t.Fatalf("kinds = %q", got)
	}
	if tr.Events[0].Text != "all green" || tr.Events[1].Text != "Success." || tr.Unpaired != 2 {
		t.Errorf("orphans = %q, %q (unpaired %d)", tr.Events[0].Text, tr.Events[1].Text, tr.Unpaired)
	}
	if !strings.Contains(tr.Events[1].Tag, "custom_tool_call_output") {
		t.Errorf("tag = %q, want it to say which record it was", tr.Events[1].Tag)
	}
}

// A spawn's output names the thread that answered, which is what re-opens it.
func TestSpawnOutputNamesTheChild(t *testing.T) {
	tr := loadRecs(t,
		call(1, "spawn_agent", "c1", map[string]any{"task_name": "parser_audit", "message": "audit"}),
		output(2, "c1", `{"agent_id":"`+childID+`","nickname":"Hooke"}`),
	)
	e := tr.Events[0]
	if e.AgentID != childID || e.Agent != "Hooke parser_audit" || e.Pending {
		t.Errorf("spawn = agent %q id %q pending %v", e.Agent, e.AgentID, e.Pending)
	}
}
