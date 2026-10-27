package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentlog/internal/transcript"
)

const sessionID = "7c1e2bad-1111-4222-8333-444455556666"

var t0 = time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)

// writeEvents lays out a session directory and returns its events.jsonl path.
func writeEvents(t *testing.T, home string, events []map[string]any) string {
	t.Helper()
	dir := filepath.Join(home, "session-state", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, e := range events {
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
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, home, path string) *transcript.Transcript {
	t.Helper()
	return loadWith(t, home, path, transcript.Load{})
}

func loadWith(t *testing.T, home, path string, o transcript.Load) *transcript.Transcript {
	t.Helper()
	tr, err := (&Provider{Home: home}).Load(&transcript.Session{Provider: transcript.Copilot, ID: sessionID, Files: []string{path}}, o)
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

func TestLoadCountsUnmodelledRecords(t *testing.T) {
	home := t.TempDir()
	path := writeEvents(t, home, []map[string]any{
		{"type": "session.start", "data": map[string]any{"context": map[string]any{"cwd": "/work/ccr"}}},
		{"type": "user.message", "data": map[string]any{"content": "fix the tunnel"}},
		{"type": "assistant.turn_start", "data": map[string]any{}},
		{"type": "hook.start", "data": map[string]any{"hookType": "postToolUse"}},
		{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "c0", "toolName": "report_intent", "arguments": map[string]any{}}},
		{"type": "assistant.turn_end", "data": map[string]any{}},
		{"type": "teleport.engaged", "data": map[string]any{}},
	})

	tr := load(t, home, path)
	// teleport.engaged is a type this build has never heard of: it stands up
	// as a record rather than vanishing. The rest are ignored on purpose.
	if got, want := kinds(tr), "user record"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	if tr.Unknowns["teleport.engaged"] != 1 {
		t.Errorf("Unknowns = %v, want the unread type counted too", tr.Unknowns)
	}
	want := map[string]int{
		"assistant.turn_start":               1,
		"assistant.turn_end":                 1,
		"hook.start":                         1,
		"tool.execution_start:report_intent": 1,
	}
	for k, n := range want {
		if tr.Ignored[k] != n {
			t.Errorf("Ignored[%q] = %d, want %d (all: %v)", k, tr.Ignored[k], n, tr.Ignored)
		}
	}
	if tr.Unknowns["teleport.engaged"] != 1 {
		t.Errorf("Unknowns = %v, want teleport.engaged:1", tr.Unknowns)
	}
}

// subagentEvents is a session whose task call runs a subagent inline: the
// subagent's own steps carry the task's call id and nothing else.
func subagentEvents() []map[string]any {
	return []map[string]any{
		{"type": "session.start", "data": map[string]any{"context": map[string]any{"cwd": "/work/ccr"}}},
		{"type": "user.message", "data": map[string]any{"content": "audit the parser"}},
		{"type": "tool.execution_start", "data": map[string]any{"toolCallId": "task1", "toolName": "task",
			"arguments": map[string]any{"name": "parser-audit", "description": "audit the parser"}}},
		{"type": "subagent.started", "data": map[string]any{"toolCallId": "task1", "agentDisplayName": "parser-audit"}},
		{"type": "assistant.message", "data": map[string]any{"parentToolCallId": "task1", "content": "reading the parser"}},
		{"type": "tool.execution_start", "data": map[string]any{"parentToolCallId": "task1", "toolCallId": "c1",
			"toolName": "bash", "arguments": map[string]any{"command": "go test ./..."}}},
		{"type": "tool.execution_complete", "data": map[string]any{"parentToolCallId": "task1", "toolCallId": "c1", "result": map[string]any{"content": "ok"}}},
		{"type": "subagent.completed", "data": map[string]any{"toolCallId": "task1"}},
		{"type": "tool.execution_complete", "data": map[string]any{"toolCallId": "task1", "result": map[string]any{"content": "the parser is fine"}}},
		{"type": "assistant.message", "data": map[string]any{"content": "done"}},
	}
}

func TestLoadHidesSubagentStepsByDefault(t *testing.T) {
	home := t.TempDir()
	tr := load(t, home, writeEvents(t, home, subagentEvents()))

	if got, want := kinds(tr), "user agent assistant"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	if tr.Hidden != 3 {
		t.Errorf("Hidden = %d, want 3", tr.Hidden)
	}
	if len(tr.Notes) != 1 || !strings.Contains(tr.Notes[0], "parser-audit") || !strings.Contains(tr.Notes[0], "--subagents") {
		t.Errorf("notes = %q, want one naming parser-audit and --subagents", tr.Notes)
	}
	// The task call still reports what the subagent came back with.
	if got := tr.Events[1].Output; got != "the parser is fine" {
		t.Errorf("task output = %q", got)
	}
}

func TestLoadAttributesSubagentSteps(t *testing.T) {
	home := t.TempDir()
	tr := loadWith(t, home, writeEvents(t, home, subagentEvents()), transcript.Load{Subagents: true})

	if got, want := kinds(tr), "user agent assistant tool assistant"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	for _, i := range []int{2, 3} {
		e := tr.Events[i]
		if e.Tag != transcript.TagSubagent || e.Agent != "parser-audit" {
			t.Errorf("event %d: tag %q agent %q, want %q / parser-audit", i+1, e.Tag, e.Agent, transcript.TagSubagent)
		}
	}
	if e := tr.Events[4]; e.Tag != "" || e.Agent != "" {
		t.Errorf("parent event tagged as a subagent step: %+v", e)
	}
	if tr.Hidden != 0 {
		t.Errorf("Hidden = %d, want 0", tr.Hidden)
	}
	if len(tr.Notes) != 1 || !strings.Contains(tr.Notes[0], "1 subagent run") {
		t.Errorf("notes = %q, want one counting the subagent run", tr.Notes)
	}
}

func start(id, tool string, args any) map[string]any {
	return map[string]any{"type": "tool.execution_start", "data": map[string]any{"toolCallId": id, "toolName": tool, "arguments": args}}
}

func complete(id string, data map[string]any) map[string]any {
	data["toolCallId"] = id
	return map[string]any{"type": "tool.execution_complete", "data": data}
}

// Each tool is shown by what it acted on, not as a JSON blob: the command,
// the file, the pattern and where it looked, the files a patch touched. The
// arguments are whatever the model produced, so a location can be under
// "path" or, in newer versions, "paths" as a string or a list.
func TestLoadShowsEachToolByWhatItActedOn(t *testing.T) {
	home := t.TempDir()
	patchText := "*** Begin Patch\n*** Update File: a.go\n@@\n-old\n+new\n*** End Patch"
	path := writeEvents(t, home, []map[string]any{
		{"type": "session.start", "data": map[string]any{"context": map[string]any{"cwd": "/work/ccr"}, "copilotVersion": "0.0.400"}},
		start("1", "bash", map[string]any{"command": "go test ./..."}),
		start("2", "view", map[string]any{"path": "/work/ccr/main.go"}),
		start("3", "rg", map[string]any{"pattern": "TODO", "path": "internal"}),
		start("4", "glob", map[string]any{"pattern": "*.go", "paths": "cmd"}),
		start("5", "rg", map[string]any{"pattern": "FIXME", "paths": []string{"a", "b"}}),
		start("6", "apply_patch", patchText),
		start("7", "apply_patch", map[string]any{"input": patchText}),
		start("8", "edit", map[string]any{"path": "b.go", "old_str": "x", "new_str": "y"}),
		start("9", "create", map[string]any{"path": "c.go", "file_text": "package c\n"}),
		start("10", "web_fetch", map[string]any{"url": "https://example.com/a?b=1&c=2"}),
	})
	tr := load(t, home, path)
	want := []struct {
		kind  transcript.Kind
		input string
	}{
		{transcript.KindTool, "go test ./..."},
		{transcript.KindTool, "/work/ccr/main.go"},
		{transcript.KindTool, "TODO internal"},
		{transcript.KindTool, "*.go cmd"},
		{transcript.KindTool, "FIXME a b"},
		{transcript.KindEdit, ""},
		{transcript.KindEdit, ""},
		{transcript.KindEdit, ""},
		{transcript.KindEdit, ""},
		{transcript.KindTool, `{"url":"https://example.com/a?b=1&c=2"}`},
	}
	if len(tr.Events) != len(want) {
		t.Fatalf("kinds = %q, want %d events", kinds(tr), len(want))
	}
	for i, w := range want {
		e := tr.Events[i]
		if e.Kind != w.kind || (w.input != "" && e.Input != w.input) {
			t.Errorf("event %d (%s) = %s %q, want %s %q", i+1, e.Tool, e.Kind, e.Input, w.kind, w.input)
		}
	}
	// Edits are resolved against the session's directory, whichever form
	// the patch came in.
	for i, file := range map[int]string{5: "/work/ccr/a.go", 6: "/work/ccr/a.go", 7: "/work/ccr/b.go", 8: "/work/ccr/c.go"} {
		if cs := tr.Events[i].Changes; len(cs) != 1 || cs[0].Path != file {
			t.Errorf("event %d changes = %+v, want one to %s", i+1, cs, file)
		}
	}
	if tr.Session.Version != "0.0.400" {
		t.Errorf("version = %q", tr.Session.Version)
	}
}

// How a tool call ended: its output, a failure with the error the harness
// gave, or no completion at all (still running, or the log was cut) —
// which is pending, not success.
func TestLoadPairsToolCallsWithTheirOutcome(t *testing.T) {
	home := t.TempDir()
	path := writeEvents(t, home, []map[string]any{
		start("ok", "bash", map[string]any{"command": "true"}),
		start("bad", "bash", map[string]any{"command": "false"}),
		start("nullerr", "bash", map[string]any{"command": "exit 3"}),
		start("open", "bash", map[string]any{"command": "sleep 100"}),
		complete("ok", map[string]any{"success": true, "result": map[string]any{"content": "done"}}),
		complete("bad", map[string]any{"success": false, "result": map[string]any{"content": "exit 1"},
			"error": map[string]any{"message": "command failed", "code": "E1"}}),
		complete("nullerr", map[string]any{"success": false, "result": map[string]any{"content": "exit 3"}, "error": nil}),
		// A completion whose start was before a compaction or in a file
		// this build did not read.
		complete("stranger", map[string]any{"result": map[string]any{"content": "orphaned output"}}),
	})
	tr := load(t, home, path)
	if len(tr.Events) != 5 {
		t.Fatalf("kinds = %q, want four calls and one orphan", kinds(tr))
	}
	ok, bad, nullerr, open := tr.Events[0], tr.Events[1], tr.Events[2], tr.Events[3]
	if ok.Failed || ok.Pending || ok.Output != "done" {
		t.Errorf("ok = %+v", ok)
	}
	if !bad.Failed || bad.Output != `exit 1`+"\n"+`{"code":"E1","message":"command failed"}` {
		t.Errorf("bad = failed %v output %q", bad.Failed, bad.Output)
	}
	if !nullerr.Failed || nullerr.Output != "exit 3" {
		t.Errorf("null error = failed %v output %q", nullerr.Failed, nullerr.Output)
	}
	if !open.Pending || open.Failed {
		t.Errorf("unanswered call = pending %v failed %v, want pending", open.Pending, open.Failed)
	}
	if !strings.Contains(tr.Events[4].Text+tr.Events[4].Output, "orphaned output") {
		t.Errorf("orphan = %+v, want its output kept", tr.Events[4])
	}
}

// Session-level trouble is shown where it happened: a usage limit is tagged
// as one (it is why a session stopped), an abort says why, and a compaction
// carries its summary.
func TestLoadSessionNotices(t *testing.T) {
	home := t.TempDir()
	path := writeEvents(t, home, []map[string]any{
		{"type": "session.model_change", "data": map[string]any{"newModel": "gpt-6"}},
		{"type": "session.warning", "data": map[string]any{"warningType": "context", "message": "nearly full"}},
		{"type": "session.compaction_complete", "data": map[string]any{"summaryContent": "we fixed the tunnel"}},
		{"type": "abort", "data": map[string]any{"reason": "user"}},
		{"type": "session.error", "data": map[string]any{"errorType": "rate_limit", "message": "You have hit your limit"}},
		{"type": "model.turn_started", "data": map[string]any{"kind": "turn_started", "model": "gpt-6-luna"}},
		{"type": "model.model_call_failure", "data": map[string]any{"kind": "model_call_failure", "modelCall": map[string]any{"status": 400}}},
		{"type": "model.turn_failed", "data": map[string]any{"kind": "turn_failed", "error": "model gpt-6-luna is not available"}},
		{"type": "model.turn_failed", "data": map[string]any{"kind": "turn_failed"}},
	})
	tr := load(t, home, path)
	if got := kinds(tr); got != "notice notice notice error error error" {
		t.Fatalf("kinds = %q", got)
	}
	if tr.Events[4].Text != "turn failed: model gpt-6-luna is not available" || tr.Events[5].Text != "turn failed: unknown error" || len(tr.Unknowns) != 0 {
		t.Errorf("failed turn = %q, unknown = %v", tr.Events[4].Text, tr.Unknowns)
	}
	tags := []string{tr.Events[0].Tag, tr.Events[1].Tag, tr.Events[2].Tag, tr.Events[3].Tag}
	if tags[1] != transcript.TagCompaction || tags[2] != transcript.TagInterrupted || tags[3] != transcript.TagUsageLimit {
		t.Errorf("tags = %q", tags)
	}
	if tr.Events[0].Text != "context: nearly full" || !strings.Contains(tr.Events[1].Text, "we fixed the tunnel") || tr.Events[2].Text != "aborted: user" {
		t.Errorf("texts = %q / %q / %q", tr.Events[0].Text, tr.Events[1].Text, tr.Events[2].Text)
	}
	if tr.Session.Model != "gpt-6" {
		t.Errorf("model = %q", tr.Session.Model)
	}
}

// A subagent that failed marks its task call failed; a subagent end whose
// task call is not in the log is kept as an orphan with its summary.
func TestLoadSubagentOutcomes(t *testing.T) {
	home := t.TempDir()
	path := writeEvents(t, home, []map[string]any{
		start("t1", "task", map[string]any{"agent_type": "explore", "description": "look around", "prompt": "find the bug"}),
		{"type": "subagent.failed", "data": map[string]any{"toolCallId": "t1"}},
		{"type": "subagent.completed", "data": map[string]any{"toolCallId": "gone", "summaryContent": "found it"}},
	})
	tr := load(t, home, path)
	if len(tr.Events) != 2 {
		t.Fatalf("kinds = %q", kinds(tr))
	}
	task := tr.Events[0]
	if task.Kind != transcript.KindAgent || task.Agent != "explore" || task.Text != "find the bug" || !task.Failed || task.Tag != "failed" {
		t.Errorf("task = kind %s agent %q text %q failed %v tag %q", task.Kind, task.Agent, task.Text, task.Failed, task.Tag)
	}
	if !strings.Contains(tr.Events[1].Text+tr.Events[1].Output, "found it") {
		t.Errorf("orphan = %+v", tr.Events[1])
	}
}

// A known record whose fields changed type (Copilot turning a string into a
// list, say) is a format change. It shows up as unreadable, counted as drift,
// instead of disappearing the way a half-written line does.
func TestLoadShowsARecordWhoseShapeChanged(t *testing.T) {
	home := t.TempDir()
	path := writeEvents(t, home, []map[string]any{
		{"type": "user.message", "data": map[string]any{"content": "hello"}},
		{"type": "user.message", "data": map[string]any{"content": []string{"hello", "world"}}},
	})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"user.message","data":{"content":"cut o`) // being written
	f.Close()

	tr := load(t, home, path)
	if tr.Records != 3 || len(tr.Events) != 2 {
		t.Fatalf("records %d, kinds %q; want 3 records, the message and the changed one", tr.Records, kinds(tr))
	}
	if tr.Unknowns["user.message"] != 1 {
		t.Errorf("unknowns = %v, want the changed record counted", tr.Unknowns)
	}
}

// Head and tail windows read only part of the log, for cheap previews.
func TestLoadWindows(t *testing.T) {
	home := t.TempDir()
	var evs []map[string]any
	for i := range 50 {
		evs = append(evs, map[string]any{"type": "user.message", "data": map[string]any{"content": strings.Repeat("x", 100) + string(rune('A'+i%26))}})
	}
	path := writeEvents(t, home, evs)
	head := loadWith(t, home, path, transcript.Load{Window: transcript.Window{HeadBytes: 600}})
	tail := loadWith(t, home, path, transcript.Load{Window: transcript.Window{TailBytes: 600}})
	if len(head.Events) == 0 || len(head.Events) >= 50 || !strings.HasSuffix(head.Events[0].Text, "A") {
		t.Errorf("head = %d events starting %q", len(head.Events), head.Events[0].Text)
	}
	if len(tail.Events) == 0 || len(tail.Events) >= 50 || !strings.HasSuffix(tail.Events[len(tail.Events)-1].Text, "X") {
		t.Errorf("tail = %d events", len(tail.Events))
	}
	// The log was removed after it was listed.
	os.Remove(path)
	_, err := (&Provider{Home: home}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
	if err == nil {
		t.Error("Load(vanished log) = nil error")
	}
}

// The hidden-subagent note names the busiest subagents, runs counted per
// name, and summarises past four so the note stays one line.
func TestByName(t *testing.T) {
	names := map[string]string{"a1": "audit", "a2": "audit", "b": "build", "c": "check", "d": "docs", "e": "eval"}
	counts := map[string]int{"a1": 5, "a2": 5, "b": 7, "c": 1, "d": 1, "e": 1, "x": 2}
	if got, want := byName(names, counts), "audit ×2, build ×1, unnamed ×1, check ×1, +2 more"; got != want {
		t.Errorf("byName() = %q, want %q", got, want)
	}
}

// Copilot encrypts the model's words. A turn that only made tool calls is
// carried by those calls and counted; a turn that said something unreadable
// is shown as unreadable; an empty one is counted. None of them vanish.
func TestLoadAssistantMessagesWithNothingInTheClear(t *testing.T) {
	home := t.TempDir()
	path := writeEvents(t, home, []map[string]any{
		{"type": "assistant.message", "data": map[string]any{"content": "  ", "toolRequests": []map[string]any{{"name": "bash"}}}},
		{"type": "assistant.message", "data": map[string]any{"content": "", "encryptedContent": "gAAAAAxyz"}},
		{"type": "assistant.message", "data": map[string]any{}},
		{"type": "assistant.reasoning", "data": map[string]any{"content": "thinking it over"}},
		{"type": "assistant.reasoning", "data": map[string]any{"content": ""}},
	})
	tr := load(t, home, path)
	if got := kinds(tr); got != "assistant reasoning" {
		t.Fatalf("kinds = %q, want the encrypted turn and the reasoning", got)
	}
	if e := tr.Events[0]; e.Tag != "encrypted" || !strings.Contains(e.Text, "9 bytes") {
		t.Errorf("encrypted turn = tag %q text %q", e.Tag, e.Text)
	}
	if tr.Ignored["assistant.message:tool calls only"] != 1 || tr.Ignored["assistant.message:empty"] != 1 {
		t.Errorf("ignored = %v, want the tool-call turn and the empty turn counted", tr.Ignored)
	}
}
