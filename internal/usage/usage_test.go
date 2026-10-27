package usage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Commands as agents wrote them in real logs.
func TestKindAndPure(t *testing.T) {
	tests := []struct {
		command string
		kind    string
		count   int
		pure    bool
	}{
		{"agentlog", "home", 1, true},
		{"cd ~ && agentlog 2>&1 | head -80", "home", 1, true},
		{"agentlog 0d3b5a91#49-80 --kind user,assistant --max 1500 2>&1 | head -260", "event", 1, true},
		{"agentlog '0ba6fe59#557' --full 2>&1 | head -60", "event", 1, true},
		{`for f in a.py b.py; do echo "=== $f ==="; agentlog "$f" all --list 2>&1 | head -6; done`, "list", 1, true},
		{"agentlog all --grep pgvector --list", "grep", 1, true},
		{"agentlog --projects", "projects", 1, true},
		{`agentlog feedback add "the tail was clear"`, "feedback", 1, true},
		{"agentlog doctor", "doctor", 1, true},
		{"agentlog --help 2>&1 | head -40", "help", 1, true},
		{"agentlog 5e8c1a07 --subagents | grep -A3 x | cut -c1-450", "subagents", 1, true},
		{"./bin/agentlog abc --edits", "edits", 1, true},
		{"TZ=UTC agentlog 3d --tail 5", "timeline", 1, true},
		{"agentlog 2048163d; agentlog f1a2b3c4 --usage", "preview", 2, true},
		// Other work in the same command: its cost is not agentlog's.
		{"go build -o bin/agentlog ./cmd/agentlog && agentlog --tools", "tools", 1, false},
		{"agentlog abc | xargs rm", "preview", 1, false},
		// Not a run of agentlog at all.
		{"cd dashboard/agentlog && go test ./...", "", 0, false},
		{"grep -rn agentlog docs/", "", 0, false},
		// A Codex exec script: the commands are its cmd strings.
		{`text(await tools.exec_command({cmd:"agentlog fa0c3e21 --kind user","max_output_tokens":6500}));`, "timeline", 1, true},
		{`text(await tools.exec_command({cmd:"cat SKILL.md; agentlog --help"}));`, "help", 1, false},
	}
	for _, tt := range tests {
		kind, n := Kind(tt.command)
		if kind != tt.kind || n != tt.count {
			t.Errorf("Kind(%q) = %q ×%d, want %q ×%d", tt.command, kind, n, tt.kind, tt.count)
		}
		if n > 0 {
			if got := Pure(tt.command); got != tt.pure {
				t.Errorf("Pure(%q) = %v, want %v", tt.command, got, tt.pure)
			}
		}
	}
}

func writeLines(t *testing.T, path string, recs ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

var t0 = time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC)

func at(s int) string { return t0.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }

func claudeUse(s int, id, command string) map[string]any {
	return map[string]any{"type": "assistant", "timestamp": at(s), "sessionId": "5e55", "cwd": "/work/app",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}}}}}
}

func claudeResult(s int, id string, content any) map[string]any {
	return map[string]any{"type": "user", "timestamp": at(s), "sessionId": "5e55",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}}}}
}

func TestReadClaude(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "-work-app", "5e55.jsonl")
	writeLines(t, path,
		claudeUse(0, "t1", "agentlog 3f2a --tail 5 2>&1 | head -20"),
		claudeResult(3, "t1", "claude 3f2a …"),
		map[string]any{"type": "assistant", "timestamp": at(10), "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}},
		claudeUse(20, "t2", "go test ./..."),
		claudeResult(25, "t2", strings.Repeat("x", 1000)),
		// A result as a list of parts, for a call made by a subagent file
		// below; and a call whose result never came.
		claudeUse(30, "t3", "agentlog --projects"),
		claudeResult(31, "t3", []any{map[string]any{"type": "text", "text": "projects (last 7d)"}}),
		claudeUse(40, "t4", "agentlog abc"),
	)
	sub := filepath.Join(root, "-work-app", "5e55", "subagents", "agent-a9f.jsonl")
	writeLines(t, sub, claudeUse(50, "s1", "agentlog doctor"), claudeResult(52, "s1", "claude 100% read"))

	calls, sessions := Scan(root, "", t0.Add(-time.Hour))
	if len(calls) != 3 || len(sessions) != 2 {
		t.Fatalf("%d calls in %d sessions, want 3 in 2: %+v", len(calls), len(sessions), calls)
	}
	c := calls[0]
	if c.Kind != "timeline" || !c.Pure || c.WaitMs() != 3000 || c.ReadMs() != 7000 || c.Out != len("claude 3f2a …") || c.Session != "5e55" || c.CWD != "/work/app" {
		t.Errorf("first call = %+v", c)
	}
	// Its next step is the call that never got a result.
	if calls[1].Kind != "projects" || calls[1].Out != len("projects (last 7d)") || calls[1].ReadMs() != 9000 {
		t.Errorf("second call = %+v, want its parts read", calls[1])
	}
	if calls[2].ReadMs() != -1 {
		t.Errorf("the subagent's call has no next step, got %v", calls[2].ReadMs())
	}
	for _, s := range sessions {
		if s.Agent == "" && s.ToolBytes < 1000 {
			t.Errorf("session tool bytes = %d, want every result counted", s.ToolBytes)
		}
	}
	if calls[2].Agent != "a9f" {
		t.Errorf("subagent call agent = %q", calls[2].Agent)
	}
	// Logs older than the window are not read.
	if calls, _ := Scan(root, "", time.Now().Add(time.Hour)); len(calls) != 0 {
		t.Errorf("future window read %d calls", len(calls))
	}
}

func TestReadCodex(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "2026", "09", "26", "rollout-x.jsonl")
	item := func(s int, payload map[string]any) map[string]any {
		return map[string]any{"timestamp": at(s), "type": "response_item", "payload": payload}
	}
	writeLines(t, path,
		map[string]any{"timestamp": at(0), "type": "session_meta", "payload": map[string]any{"id": "01a0", "cwd": "/work/api"}},
		item(1, map[string]any{"type": "custom_tool_call", "call_id": "c1", "name": "exec",
			"input": `text(await tools.exec_command({cmd:"agentlog 3f2a --kind user","max_output_tokens":6500}));`}),
		item(4, map[string]any{"type": "custom_tool_call_output", "call_id": "c1",
			"output": []any{map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.8 seconds\nOutput:\n"}, map[string]any{"type": "input_text", "text": "3f2a prompts"}}}),
		item(9, map[string]any{"type": "reasoning"}),
		item(10, map[string]any{"type": "function_call", "call_id": "c2", "name": "shell", "arguments": `{"command":["bash","-lc","agentlog --list"]}`}),
		item(12, map[string]any{"type": "function_call_output", "call_id": "c2", "output": "listed"}),
		item(13, map[string]any{"type": "function_call", "call_id": "c3", "name": "shell", "arguments": `{"command":["ls"]}`}),
		item(14, map[string]any{"type": "function_call_output", "call_id": "c3", "output": "a b"}),
	)
	calls, sessions := Scan("", root, t0.Add(-time.Hour))
	if len(calls) != 2 || len(sessions) != 1 || sessions[0].ID != "01a0" || sessions[0].CWD != "/work/api" {
		t.Fatalf("calls %+v sessions %+v", calls, sessions)
	}
	c := calls[0]
	if c.Kind != "timeline" || c.ShellMs != 800 || c.WaitMs() != 3000 || c.ReadMs() != 5000 || !c.Pure {
		t.Errorf("exec call = %+v", c)
	}
	if calls[1].Kind != "list" || calls[1].Out != len("listed") {
		t.Errorf("shell call = %+v", calls[1])
	}
}

// agentlog's own record of a run is matched to the call it ran in by the
// calling session and time, and the report splits the wait with it.
func TestJoinAndReport(t *testing.T) {
	calls := []Call{
		{Harness: "claude", Session: "5e55", File: "f", Kind: "preview", Count: 1, Pure: true, Command: "agentlog 3f2a",
			Start: t0, End: t0.Add(4 * time.Second), Next: t0.Add(9 * time.Second), Out: 4000},
		{Harness: "claude", Session: "5e55", File: "f", Kind: "list", Count: 1, Command: "go build && agentlog --list",
			Start: t0.Add(time.Minute), End: t0.Add(time.Minute + time.Second), Out: 9000},
	}
	runs := []Run{
		{Start: t0.Add(2 * time.Second), Ms: 1500, Session: "5e55", Out: 4000, Phases: map[string]float64{"discover": 200, "load": 1100}},
		{Start: t0.Add(time.Hour), Ms: 100, Session: "5e55"}, // another call
		{Start: t0.Add(2 * time.Second), Ms: 90, Session: "other"},
	}
	Join(calls, runs)
	if len(calls[0].Self) != 1 || calls[0].SelfMs() != 1500 || len(calls[1].Self) != 0 {
		t.Fatalf("joined %+v", calls)
	}
	var b bytes.Buffer
	Report(&b, calls, []Session{{Harness: "claude", ID: "5e55", File: "f", CWD: "/work/app", ToolBytes: 8000}}, runs, t0.Add(-time.Hour), 10)
	out := b.String()
	for _, want := range []string{
		"2 calls (2 runs) from 1 sessions (claude 2)",
		"1 of them matched to agentlog's own record",
		"the 1 calls that ran only agentlog; 1 also ran other commands",
		"the agent waited (call to result)               4.0s",
		"agentlog ran (its own record)                   1.5s",
		"outside agentlog (shell, sandbox, approval)     2.5s",
		"the model took to its next step                 5.0s",
		"load ", "discover ",
		"context: 3.9KB into agents, ≈1.0k tokens",
		"claude 5e55 app",
		"50%", // 4000 of its 8000 bytes of tool output
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "go build") {
		t.Errorf("a mixed call was measured:\n%s", out)
	}
}

func TestLoadRuns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "runs.jsonl")
	line := func(s int, session string) string {
		b, _ := json.Marshal(Run{Start: t0.Add(time.Duration(s) * time.Second), Session: session, Ms: 5})
		return string(b) + "\n"
	}
	os.WriteFile(p+".1", []byte(line(1, "a")), 0o644)
	os.WriteFile(p, []byte(line(3, "a")+"not json\n"+line(2, "b")), 0o644)
	runs := LoadRuns([]string{p, p, filepath.Join(dir, "missing.jsonl")})
	if len(runs) != 3 || runs[0].Session != "a" || runs[1].Session != "b" {
		t.Errorf("runs = %+v, want three, oldest first, the same file read once", runs)
	}
}

// Codex has written a shell call's command three ways, and a result three
// ways; each is the text the agent ran or read.
func TestCodexShapes(t *testing.T) {
	for args, want := range map[string]string{
		`{"cmd":"agentlog x"}`:                           "agentlog x",
		`{"command":"agentlog x"}`:                       "agentlog x",
		`{"command":["bash","-lc","agentlog x | head"]}`: "agentlog x | head",
		`{"command":["agentlog","x"]}`:                   "agentlog x",
		`not json agentlog`:                              "not json agentlog",
	} {
		if got := codexCommand(args, nil); got != want {
			t.Errorf("codexCommand(%s) = %q, want %q", args, got, want)
		}
	}
	if got := codexCommand("", json.RawMessage(`{"command":["agentlog","y"]}`)); got != "agentlog y" {
		t.Errorf("local shell action = %q", got)
	}
	for raw, want := range map[string]string{
		`"plain"`:                     "plain",
		`[{"text":"a"},{"text":"b"}]`: "ab",
		`{"output":"wrapped"}`:        "wrapped",
		`42`:                          "42",
	} {
		if got := text(json.RawMessage(raw)); got != want {
			t.Errorf("text(%s) = %q, want %q", raw, got, want)
		}
	}
}

// The report reads at every scale: nothing, a Codex call timed by its
// harness, sizes and times from bytes to megabytes and minutes, and more
// sessions and calls than --top shows.
func TestReportScales(t *testing.T) {
	var b bytes.Buffer
	Report(&b, nil, nil, nil, t0, 3)
	if !strings.Contains(b.String(), "0 calls (0 runs)") {
		t.Errorf("empty report:\n%s", b.String())
	}
	var calls []Call
	var sessions []Session
	for i, out := range []int{10, 5000, 3 << 20, 700, 20} {
		id := fmt.Sprintf("0123456789-%d", i)
		sessions = append(sessions, Session{Harness: "codex", ID: id, File: id, Agent: "agent-with-a-name"})
		calls = append(calls, Call{Harness: "codex", Session: id, File: id, Kind: "preview", Count: 1, Pure: true,
			Command: "agentlog " + strings.Repeat("word ", 40), Start: t0, End: t0.Add(time.Duration(out) * time.Millisecond * 50),
			Out: out, ShellMs: 700})
	}
	b.Reset()
	Report(&b, calls, sessions, nil, t0.Add(-time.Hour), 3)
	out := b.String()
	for _, want := range []string{"the harness ran the command (Codex)            700ms   700ms", "3.0MB", "4.9KB", "700B",
		"… 2 more sessions", "codex 01234567/agent-with-a-name", "1.2k", "786.4k", "43.7h", "…"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if c, x := Roots(); !strings.HasSuffix(c, filepath.Join(".claude", "projects")) && os.Getenv("CLAUDE_CONFIG_DIR") == "" || !strings.HasSuffix(x, "sessions") {
		t.Errorf("roots = %s, %s", c, x)
	}
}

func TestKindCountsCommandsNotMentions(t *testing.T) {
	for _, cmd := range []string{`rg -n 'x|agentlog' src`, `rg -e 'agentlog' docs`, `echo 'agentlog --help'`, "cat <<'EOF'\nagentlog x\nEOF", `python3 -c "print('agentlog x')"`, `# agentlog --help`, `bash -c 'echo agentlog'`} {
		if kind, n := Kind(cmd); kind != "" || n != 0 {
			t.Errorf("%q = %s x%d", cmd, kind, n)
		}
	}
	for _, cmd := range []string{`env FOO=bar agentlog --help`, `bash -lc 'agentlog --help'`, `command agentlog --help`, `for x in a b; do agentlog --help; done`} {
		if kind, n := Kind(cmd); kind != "help" || n != 1 {
			t.Errorf("%q = %s x%d", cmd, kind, n)
		}
	}
}
