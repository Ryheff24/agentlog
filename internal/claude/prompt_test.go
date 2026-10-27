package claude

import (
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// A user-role record is not always a person typing. The harness writes its
// own notices, slash commands, shell escapes and other sessions' messages
// under the same role, told apart only by the tags the text opens with.
func TestUserRoleTextIsClassified(t *testing.T) {
	type want struct{ kind, tag, agent, text, input string }
	for _, tc := range []struct {
		name    string
		records []map[string]any
		want    []want
	}{
		{"prompt with an injected reminder", []map[string]any{
			user("<system-reminder>\nctx\n</system-reminder>\nwhy does the list jump")},
			[]want{{kind: "user", text: "why does the list jump"}}},
		{"a record that is only injected context says nothing", []map[string]any{
			user("<system-reminder>ctx</system-reminder>")}, nil},
		{"compaction summary", []map[string]any{
			{"type": "user", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "Summary: fixed the parser."}}},
			[]want{{kind: "notice", tag: transcript.TagCompaction, text: "compaction summary handed to the model:\nSummary: fixed the parser."}}},
		{"background task notification with its result", []map[string]any{
			user("<task-notification>\n<task-id>b1</task-id>\n<status>failed</status>\n<summary>Build failed</summary>\n<result>exit 2</result>\n</task-notification>")},
			[]want{{kind: "notice", tag: transcript.TagNotification, text: "failed: Build failed\nexit 2"}}},
		// A person writing about the harness's own tags is still a person.
		// Only a one-line banner may stand before a wrapper; a prompt that
		// mentions one mid-sentence was read as a blank notification.
		{"a prompt that mentions a harness tag", []map[string]any{
			user("why does the <task-notification> block render twice?")},
			[]want{{kind: "user", text: "why does the <task-notification> block render twice?"}}},
		{"a prompt quoting a teammate tag", []map[string]any{
			user("the parser reads <teammate-message teammate_id=\"x\"> wrong")},
			[]want{{kind: "user", text: "the parser reads <teammate-message teammate_id=\"x\"> wrong"}}},
		{"slash command as Claude Code writes it, message first", []map[string]any{
			user("<command-message>go-backend</command-message>\n<command-name>/go-backend</command-name>\n<command-args>review the server</command-args>")},
			[]want{{kind: "notice", tag: transcript.TagCommand, text: "/go-backend review the server"}}},
		{"output of a local command", []map[string]any{
			user("<local-command-stdout>Set model to opus</local-command-stdout>")},
			[]want{{kind: "notice", tag: transcript.TagCommand, text: "Set model to opus"}}},
		{"shell escape and its output", []map[string]any{
			user("<bash-input>git status</bash-input>"),
			user("<bash-stdout>clean</bash-stdout><bash-stderr></bash-stderr>")},
			[]want{{kind: "tool", tag: "user", input: "git status"}}},
		{"interrupt marker as a plain string", []map[string]any{
			user("[Request interrupted by user]")},
			[]want{{kind: "notice", tag: transcript.TagInterrupted, text: "[Request interrupted by user]"}}},
		{"pasted image and document beside the text", []map[string]any{
			user([]map[string]any{{"type": "text", "text": "compare these"}, {"type": "image", "source": map[string]any{}}, {"type": "document", "source": map[string]any{}}})},
			[]want{{kind: "user", text: "compare these\n[image]\n[document]"}}},
		// Drift: a block type added after this build. The record stands in
		// for itself, and the text beside it is still the prompt.
		{"a block type this build has never seen", []map[string]any{
			user([]map[string]any{{"type": "audio", "source": map[string]any{}}, {"type": "text", "text": "listen to this"}})},
			[]want{{kind: "record", tag: "user:audio"}, {kind: "user", text: "listen to this"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadLog(t, transcript.Load{}, tc.records...)
			if len(tr.Events) != len(tc.want) {
				t.Fatalf("events = %q, want %d", kinds(tr), len(tc.want))
			}
			for i, w := range tc.want {
				e := tr.Events[i]
				if string(e.Kind) != w.kind || e.Tag != w.tag || (w.text != "" && e.Text != w.text) || e.Input != w.input || (w.agent != "" && e.Agent != w.agent) {
					t.Errorf("event %d = kind %q tag %q text %q input %q, want %+v", i, e.Kind, e.Tag, e.Text, e.Input, w)
				}
			}
		})
	}
}

// The shell escape's output is written as a record of its own, right after the
// command. A tail window can start between the two.
func TestShellEscapeOutput(t *testing.T) {
	tr := loadLog(t, transcript.Load{},
		user("<bash-input>git status</bash-input>"),
		user("<bash-stdout>clean</bash-stdout><bash-stderr>warning: x</bash-stderr>"),
	)
	if len(tr.Events) != 1 || tr.Events[0].Output != "clean\nwarning: x" {
		t.Fatalf("events = %+v, want the command holding its output", tr.Events)
	}

	// Output whose command is outside the log is still output.
	tr = loadLog(t, transcript.Load{}, user("go"), user("<bash-stdout>clean</bash-stdout><bash-stderr></bash-stderr>"))
	if got := kinds(tr); got != "user record" {
		t.Fatalf("kinds = %q, want the stray output kept", got)
	}
	if e := tr.Events[1]; e.Text != "clean" || tr.Unpaired != 1 {
		t.Errorf("stray output = %+v (unpaired %d)", e, tr.Unpaired)
	}
}

// System records carry the harness's own messages. The subtypes that are a
// step of the session become events; the rest are counted.
func TestSystemRecords(t *testing.T) {
	sys := func(subtype string, content any) map[string]any {
		return map[string]any{"type": "system", "subtype": subtype, "content": content, "level": "info"}
	}
	for _, tc := range []struct {
		name       string
		rec        map[string]any
		kind, tag  string
		text       string
		ignoredKey string
	}{
		{"API error", sys("api_error", "Overloaded"), "error", "", "Overloaded", ""},
		{"API error that is a usage limit", sys("api_error", "You've hit your usage limit"), "error", transcript.TagUsageLimit, "You've hit your usage limit", ""},
		// Newer versions carry the error as an object beside empty content.
		{"API error with no text", map[string]any{"type": "system", "subtype": "api_error", "retryAttempt": 1}, "error", "", "API error", ""},
		{"slash command", sys("local_command", "<command-name>/context</command-name>\n            <command-message>context</command-message>\n            <command-args></command-args>"),
			"notice", transcript.TagCommand, "/context", ""},
		// The command's output is written as a local_command too, wrapped the
		// same way it is in a user record. The wrapper is not the output.
		{"slash command output", sys("local_command", "<local-command-stdout>Context Usage 41%</local-command-stdout>"),
			"notice", transcript.TagCommand, "Context Usage 41%", ""},
		{"informational warning", sys("informational", "Unknown command: /mco. Did you mean /mcp?"), "notice", "", "Unknown command: /mco. Did you mean /mcp?", ""},
		{"scheduled task firing", sys("scheduled_task_fire", "Running scheduled task (Sep 24 7:09am)"), "notice", "", "Running scheduled task (Sep 24 7:09am)", ""},
		{"informational with nothing in it", sys("informational", ""), "", "", "", ""},
		{"a subtype this build does not model", sys("stop_hook_summary", "x"), "", "", "", "system:stop_hook_summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := loadLog(t, transcript.Load{}, tc.rec)
			if tc.kind == "" {
				if len(tr.Events) != 0 {
					t.Fatalf("events = %+v, want none", tr.Events)
				}
				if tc.ignoredKey != "" && tr.Ignored[tc.ignoredKey] != 1 {
					t.Errorf("Ignored = %v, want %s counted", tr.Ignored, tc.ignoredKey)
				}
				return
			}
			if len(tr.Events) != 1 {
				t.Fatalf("events = %q, want one", kinds(tr))
			}
			if e := tr.Events[0]; string(e.Kind) != tc.kind || e.Tag != tc.tag || e.Text != tc.text {
				t.Errorf("event = kind %q tag %q text %q, want %q %q %q", e.Kind, e.Tag, e.Text, tc.kind, tc.tag, tc.text)
			}
		})
	}
}

// Assistant records that are not plain text or tool calls.
func TestAssistantRecordEdges(t *testing.T) {
	t.Run("an API error that is not a limit ends the session in error", func(t *testing.T) {
		tr := loadLog(t, transcript.Load{}, user("go"),
			map[string]any{"type": "assistant", "isApiErrorMessage": true, "error": "invalid_request",
				"message": map[string]any{"model": "<synthetic>", "content": []map[string]any{{"type": "text", "text": "API Error: 400 prompt is too long"}}}})
		if e := tr.Events[1]; e.Kind != transcript.KindError || e.Tag != "" {
			t.Errorf("event = %+v", e)
		}
		if tr.Session.Ending != transcript.EndingError || tr.Session.Model != "" {
			t.Errorf("ending %q model %q; the synthetic model is not the session's", tr.Session.Ending, tr.Session.Model)
		}
	})
	t.Run("the harness's empty-turn placeholder is not a reply", func(t *testing.T) {
		tr := loadLog(t, transcript.Load{}, user("go"), assistant(map[string]any{"type": "text", "text": "(no content)"}))
		if got := kinds(tr); got != "user" {
			t.Errorf("kinds = %q", got)
		}
	})
	// Drift: block types that arrive after this build. Each record stands in
	// once, however many unfamiliar blocks it holds.
	t.Run("unfamiliar blocks give one stand-in per record", func(t *testing.T) {
		rec := assistant(map[string]any{"type": "server_tool_use", "id": "s1", "name": "web_search"},
			map[string]any{"type": "web_search_tool_result", "tool_use_id": "s1"},
			map[string]any{"type": "text", "text": "Found it."})
		for _, w := range []transcript.Window{{}, {TailBytes: 1 << 20}} {
			tr := loadLog(t, transcript.Load{Window: w}, user("go"), rec)
			// The stand-in reads the record by shape, so what kind it comes
			// out as is the generic reader's call; that there is one is ours.
			if n := len(tr.Events); n != 3 || tr.Events[2].Kind != transcript.KindAssistant {
				t.Errorf("window %+v: kinds = %q, want user, one stand-in, assistant", w, kinds(tr))
			}
			if tr.Unknowns["assistant:server_tool_use"] != 1 || tr.Unknowns["assistant:web_search_tool_result"] != 1 {
				t.Errorf("window %+v: Unknowns = %v, want both block types counted", w, tr.Unknowns)
			}
		}
	})
	t.Run("usage in a shape this build cannot read is skipped, not fatal", func(t *testing.T) {
		rec := assistant(map[string]any{"type": "text", "text": "ok"})
		rec["message"].(map[string]any)["usage"] = "n/a"
		rec["version"] = "2.9.0"
		tr := loadLog(t, transcript.Load{}, rec)
		if len(tr.Events) != 1 || tr.Usage.Known() || tr.Session.Version != "2.9.0" {
			t.Errorf("events %d usage %+v version %q", len(tr.Events), tr.Usage, tr.Session.Version)
		}
	})
}

// Old versions wrote a subagent's turns into the parent log, marked as a
// sidechain. The subagent has its own log; in the parent they are hidden and
// said to be.
func TestInlinedSubagentRecordsAreHidden(t *testing.T) {
	side := func(e map[string]any) map[string]any { e["isSidechain"] = true; return e }
	tr := loadLog(t, transcript.Load{},
		user("audit it"),
		side(user("subagent prompt")),
		side(assistant(map[string]any{"type": "text", "text": "subagent reply"})),
		assistant(map[string]any{"type": "text", "text": "done"}),
	)
	if got := kinds(tr); got != "user assistant" || tr.Hidden != 2 {
		t.Fatalf("kinds = %q hidden %d", got, tr.Hidden)
	}
	if len(tr.Notes) != 1 || !strings.Contains(tr.Notes[0], "2 inlined subagent records") {
		t.Errorf("notes = %q", tr.Notes)
	}
}
