package claude

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// loadLog writes entries as a session log and reads it back.
func loadLog(t *testing.T, o transcript.Load, entries ...map[string]any) *transcript.Transcript {
	t.Helper()
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, entries)
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, o)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// Every tool Claude Code ships is rendered from the few input fields that say
// what it did. The inputs here are the shapes the harness writes today; the
// cases after them are the drift the reader has to survive.
func TestToolUseRendersEachTool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tool  string
		input map[string]any
		want  transcript.Event // only the fields set here are compared
	}{
		{"shell command", "Bash", map[string]any{"command": "go vet ./...", "description": "vet"},
			transcript.Event{Kind: transcript.KindTool, Tool: "Bash", Input: "go vet ./..."}},
		{"read of a slice of a file", "Read", map[string]any{"file_path": "/w/a.go", "offset": 120, "limit": 40},
			transcript.Event{Kind: transcript.KindTool, Input: "/w/a.go [offset 120 limit 40]"}},
		{"read with only a limit", "Read", map[string]any{"file_path": "/w/a.go", "limit": 40},
			transcript.Event{Kind: transcript.KindTool, Input: "/w/a.go [offset - limit 40]"}},
		{"read with an explicit null offset", "Read", map[string]any{"file_path": "/w/a.go", "offset": nil, "limit": 40},
			transcript.Event{Kind: transcript.KindTool, Input: "/w/a.go [offset - limit 40]"}},
		{"whole-file read", "Read", map[string]any{"file_path": "/w/a.go"},
			transcript.Event{Kind: transcript.KindTool, Input: "/w/a.go"}},
		{"new file", "Write", map[string]any{"file_path": "/w/new.go", "content": "package w\n\nfunc F() {}\n"},
			transcript.Event{Kind: transcript.KindEdit, Input: "write /w/new.go (+3 -0)"}},
		{"several edits to one file", "MultiEdit", map[string]any{"file_path": "/w/a.go", "edits": []map[string]any{
			{"old_string": "a", "new_string": "b"}, {"old_string": "c\nd", "new_string": "e", "replace_all": true}}},
			transcript.Event{Kind: transcript.KindEdit, Input: "update /w/a.go (+1 -1), update /w/a.go (+1 -2)"}},
		{"notebook cell", "NotebookEdit", map[string]any{"notebook_path": "/w/n.ipynb", "cell_id": "c3", "new_source": "print(1)", "edit_mode": "replace"},
			transcript.Event{Kind: transcript.KindEdit, Input: "update /w/n.ipynb (+1 -0)"}},
		{"search", "Grep", map[string]any{"pattern": "TODO", "path": "src", "glob": "*.go", "output_mode": "content"},
			transcript.Event{Kind: transcript.KindTool, Input: "TODO src *.go"}},
		{"search with only a pattern", "Grep", map[string]any{"pattern": "TODO"},
			transcript.Event{Kind: transcript.KindTool, Input: "TODO"}},
		{"file glob", "Glob", map[string]any{"pattern": "**/*.go"},
			transcript.Event{Kind: transcript.KindTool, Input: "**/*.go"}},
		{"fetch", "WebFetch", map[string]any{"url": "https://example.com/doc", "prompt": "summarise"},
			transcript.Event{Kind: transcript.KindTool, Input: "https://example.com/doc"}},
		{"web search", "WebSearch", map[string]any{"query": "go 1.30 release notes"},
			transcript.Event{Kind: transcript.KindTool, Input: "go 1.30 release notes"}},
		{"skill", "Skill", map[string]any{"skill": "go-backend", "args": "review"},
			transcript.Event{Kind: transcript.KindTool, Input: "go-backend review"}},
		{"subagent under its old tool name", "Task", map[string]any{"description": "Audit parser", "prompt": "look", "subagent_type": "Explore"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "Explore", Input: "Audit parser", Text: "look"}},
		{"subagent with neither name nor type", "Agent", map[string]any{"description": "Audit parser", "prompt": "look"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "subagent", Input: "Audit parser"}},
		{"message to a teammate", "SendMessage", map[string]any{"to": "lead", "summary": "short", "message": "the full note",
			"type": "message", "recipient": "lead", "content": "the full…"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "lead", Text: "the full note"}},
		// The input also carries recipient and a clipped content preview. If
		// the harness drops the older pair, the other still says who and what.
		{"message whose to/message fields were renamed away", "SendMessage", map[string]any{"recipient": "lead", "content": "the full…"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "lead", Text: "the full…"}},
		{"message with only a summary", "SendMessage", map[string]any{"to": "lead", "summary": "short"},
			transcript.Event{Kind: transcript.KindAgent, Agent: "lead", Text: "short"}},
		{"new-style task", "TaskCreate", map[string]any{"subject": "Write tests", "description": "cover the reader", "activeForm": "Writing tests"},
			transcript.Event{Kind: transcript.KindPlan, Text: "Write tests cover the reader"}},
		{"task status change", "TaskUpdate", map[string]any{"taskId": "3", "status": "completed"},
			transcript.Event{Kind: transcript.KindPlan, Text: "3 completed"}},
		{"plan handed back for approval", "ExitPlanMode", map[string]any{"plan": "# Plan\n1. read"},
			transcript.Event{Kind: transcript.KindPlan, Text: "# Plan\n1. read"}},
		{"MCP tool", "mcp__plugin_github__search_issues", map[string]any{"q": "is:open"},
			transcript.Event{Kind: transcript.KindTool, Tool: "plugin_github.search_issues", Input: `{"q":"is:open"}`}},
		{"tool this build has never heard of", "Workflow", map[string]any{"steps": []string{"a", "b"}},
			transcript.Event{Kind: transcript.KindTool, Tool: "Workflow", Input: `{"steps":["a","b"]}`}},
		// Drift: an input that is not an object at all. The call is still a
		// call; it just has nothing to render.
		{"known tool with a non-object input", "Bash", nil,
			transcript.Event{Kind: transcript.KindTool, Tool: "Bash", Input: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadLog(t, transcript.Load{}, assistant(toolUse("t1", tc.tool, tc.input)))
			if len(tr.Events) != 1 {
				t.Fatalf("events = %d, want 1", len(tr.Events))
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
			if w.Input == "" && tc.input == nil && got.Input != "" {
				t.Errorf("Input = %q, want empty", got.Input)
			}
			if !got.Pending {
				t.Error("a call with no result in the log is not marked pending")
			}
		})
	}
}

// A todo list is the session's plan: the latest TodoWrite replaces it.
func TestTodoWriteReplacesThePlan(t *testing.T) {
	todos := func(items ...[2]string) map[string]any {
		var l []map[string]any
		for _, it := range items {
			l = append(l, map[string]any{"content": it[0], "status": it[1], "activeForm": it[0]})
		}
		return map[string]any{"todos": l}
	}
	tr := loadLog(t, transcript.Load{},
		assistant(toolUse("t1", "TodoWrite", todos([2]string{"read", "in_progress"}, [2]string{"fix", "pending"}))),
		toolResult("t1", "Todos have been modified successfully", nil),
		assistant(toolUse("t2", "TodoWrite", todos([2]string{"read", "completed"}, [2]string{"fix", "in_progress"}))),
		toolResult("t2", "Todos have been modified successfully", nil),
	)
	if got := kinds(tr); got != "plan plan" {
		t.Fatalf("kinds = %q", got)
	}
	if len(tr.Plan) != 2 || tr.Plan[0].Status != "completed" || tr.Plan[1].Text != "fix" {
		t.Errorf("plan = %+v, want the second list", tr.Plan)
	}
	if len(tr.Events[0].Plan) != 2 || tr.Events[0].Plan[0].Status != "in_progress" {
		t.Errorf("first event plan = %+v, want the list as it was then", tr.Events[0].Plan)
	}
}

// The tool result is where Claude Code says what an edit really did. These are
// the ways that record varies, and the ways it can go missing.
func TestToolResultRefinesTheCall(t *testing.T) {
	hunk := []map[string]any{{"oldStart": 1, "oldLines": 0, "newStart": 1, "newLines": 2, "lines": []string{"+a", "+b"}}}
	for _, tc := range []struct {
		name   string
		call   map[string]any
		result map[string]any // the user record carrying the tool_result
		check  func(t *testing.T, e transcript.Event)
	}{
		{"a Write that created the file is an add, with the harness's own diff",
			toolUse("t1", "Write", map[string]any{"file_path": "/w/new.go", "content": "a\nb\n"}),
			toolResult("t1", "File created", map[string]any{"toolUseResult": map[string]any{"type": "create", "filePath": "/w/new.go", "structuredPatch": hunk}}),
			func(t *testing.T, e transcript.Event) {
				if c := e.Changes; len(c) != 1 || c[0].Op != "add" || c[0].Added != 2 || !strings.HasPrefix(c[0].Diff, "@@ -1,0 +1,2 @@") {
					t.Errorf("changes = %+v", c)
				}
				if e.Input != "add /w/new.go (+2 -0)" {
					t.Errorf("Input = %q", e.Input)
				}
			}},
		{"a structured patch without a path keeps the call's path",
			toolUse("t1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "x", "new_string": "a\nb"}),
			toolResult("t1", "ok", map[string]any{"toolUseResult": map[string]any{"structuredPatch": hunk}}),
			func(t *testing.T, e transcript.Event) {
				if c := e.Changes; len(c) != 1 || c[0].Path != "/w/a.go" || c[0].Op != "update" {
					t.Errorf("changes = %+v", c)
				}
			}},
		// Most tools' toolUseResult is a string, which says nothing more than
		// the result text already does.
		{"a string toolUseResult leaves the call as the call described it",
			toolUse("t1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "x", "new_string": "y"}),
			toolResult("t1", "ok", map[string]any{"toolUseResult": "Error: file changed on disk"}),
			func(t *testing.T, e transcript.Event) {
				if c := e.Changes; len(c) != 1 || c[0].Diff != "@@\n-x\n+y" {
					t.Errorf("changes = %+v", c)
				}
			}},
		// Drift: a field the reader types (structuredPatch as a list) arrives
		// as something else. The result still answers the call.
		{"an undecodable toolUseResult still answers the call",
			toolUse("t1", "Edit", map[string]any{"file_path": "/w/a.go", "old_string": "x", "new_string": "y"}),
			toolResult("t1", "ok", map[string]any{"toolUseResult": map[string]any{"structuredPatch": "not a list"}}),
			func(t *testing.T, e transcript.Event) {
				if e.Pending || e.Output != "ok" || len(e.Changes) != 1 {
					t.Errorf("event = %+v", e)
				}
			}},
		{"an error result marks the call failed",
			toolUse("t1", "Bash", map[string]any{"command": "false"}),
			user([]map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": "Exit code 1", "is_error": true}}),
			func(t *testing.T, e transcript.Event) {
				if !e.Failed || e.ExitCode == nil || *e.ExitCode != 1 {
					t.Errorf("failed %v exit %v", e.Failed, e.ExitCode)
				}
			}},
		// Tool results can be content blocks: text, images a tool returned,
		// and tool_reference blocks from ToolSearch.
		{"a result made of blocks is flattened, naming what is not text",
			toolUse("t1", "ToolSearch", map[string]any{"query": "select:Read"}),
			user([]map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": []map[string]any{
				{"type": "text", "text": "found"}, {"type": "tool_reference", "tool_name": "Read"}, {"type": "image", "source": map[string]any{}}}}}),
			func(t *testing.T, e transcript.Event) {
				if e.Output != "found\n[tool Read]\n[image]" {
					t.Errorf("Output = %q", e.Output)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadLog(t, transcript.Load{}, assistant(tc.call), tc.result)
			if len(tr.Events) != 1 {
				t.Fatalf("events = %d (%s), want the call alone", len(tr.Events), kinds(tr))
			}
			tc.check(t, tr.Events[0])
		})
	}
}

// A result whose call is not in the log — the call is before a tail window, or
// the id that joins them changed — is still the answer to something, and is
// shown rather than dropped.
func TestToolResultWithoutItsCallIsKept(t *testing.T) {
	tr := loadLog(t, transcript.Load{},
		user("go"),
		toolResult("gone", "the output someone is looking for", nil),
	)
	if got := kinds(tr); got != "user record" {
		t.Fatalf("kinds = %q", got)
	}
	if e := tr.Events[1]; e.Text != "the output someone is looking for" || !strings.Contains(e.Tag, "no call") {
		t.Errorf("orphan = %+v", e)
	}
	if tr.Unpaired != 1 {
		t.Errorf("Unpaired = %d, want 1", tr.Unpaired)
	}
}

// Claude Code writes commit, push and branch today. An operation added in a
// later release is read by its fields, not dropped for its name.
func TestGitOperationsKeepOnesThisBuildDoesNotRank(t *testing.T) {
	tr := loadLog(t, transcript.Load{},
		assistant(toolUse("t1", "Bash", map[string]any{"command": "git commit -m x && git tag v1 && git push"})),
		toolResult("t1", "ok", map[string]any{"toolUseResult": map[string]any{"gitOperation": map[string]any{
			"push":   map[string]any{"branch": "main"},
			"tag":    map[string]any{"ref": "v1"},
			"commit": map[string]any{"sha": "abc1234", "kind": "committed", "branch": "main"},
		}}}),
	)
	var got []string
	for _, g := range tr.Events[0].Git {
		got = append(got, g.Text())
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "tag on v1") {
		t.Errorf("git = %q, want the unranked tag operation kept", joined)
	}
	// Known operations stay in the order they happen.
	if c, p := strings.Index(joined, "committed abc1234 on main"), strings.Index(joined, "pushed on main"); c < 0 || p < 0 || c > p {
		t.Errorf("git = %q, want the commit before the push", joined)
	}
}
