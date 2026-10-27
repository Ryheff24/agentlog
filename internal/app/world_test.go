package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentlog/internal/catalog"
	"agentlog/internal/claude"
	"agentlog/internal/codex"
	"agentlog/internal/copilot"
)

// world is a home directory holding sessions from all three harnesses, laid
// out the way each harness writes them. Commands run against it through Run,
// the same entry point the binary uses, so a test exercises clue parsing,
// lookup, view selection and rendering together.
//
// Each session reproduces cases seen in real logs; the comments say which.
type world struct {
	t    *testing.T
	root string
	now  time.Time
}

// Session ids, chosen so their short forms are easy to read in assertions.
const (
	checkoutID = "5a1e0000-0000-4000-8000-000000000001" // Claude, /work/shop, an hour old
	invoiceID  = "1b0c0000-0000-4000-8000-000000000002" // Claude, /work/shop, nine days old
	launchID   = "d4af0000-0000-4000-8000-000000000003" // Claude, /work/blog
	reviewerID = "a0b1c2d3e4f5a6b7c"                    // subagent of checkout
	codexID    = "01a0dddd-0000-7000-8000-000000000004" // Codex, /work/shop, code mode
	copilotID  = "c0c0c0c0-0000-4000-8000-000000000005" // Copilot, /work/ccr
	releaseID  = "7e1e0000-0000-4000-8000-000000000006" // Claude, /work/shop, running now
	draftsID   = "d7af0000-0000-4000-8000-000000000007" // Claude, /work/blog, many prompts, interrupted
	renameID   = "0a0a0000-0000-4000-8000-000000000008" // Claude, /work/api, continued in docsID
	docsID     = "0a0b0000-0000-4000-8000-000000000009" // Claude, /work/api, continues renameID, running now
	docsAgent  = "b5b6c7d8e9f0a1b2c"                    // background subagent of docs, running now
	lostID     = "0a0c0000-0000-4000-8000-00000000000a" // Claude, /work/api, continues a log that is gone
	teamID     = "01a0eeee-0000-7000-8000-00000000000b" // Codex, /work/api, multi-agent, two pages
	teslaID    = "01a0fffe-0000-7000-8000-00000000000c" // subagent of team
	worktreeID = "3c3c0000-0000-4000-8000-00000000000d" // Claude, a worktree of /work/shop
	clearedID  = "c1ea0000-0000-4000-8000-00000000000e" // Claude, /work/shop, opened and cleared, nothing said
	openedID   = "0be40000-0000-4000-8000-00000000000f" // Claude, /work/shop, opened and closed, no timed record
	movedID    = "3007ed00-0000-4000-8000-000000000010" // Claude, began in /work, moved into /work/site
)

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("COPILOT_HOME", filepath.Join(root, "copilot"))
	t.Setenv("AGENTLOG_FEEDBACK_FILE", filepath.Join(root, "feedback.jsonl"))
	// Never the person's own types.json.
	t.Setenv("AGENTLOG_CONFIG", filepath.Join(root, "config"))
	// The harness running these tests sets these for itself.
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "COPILOT_SESSION_ID", "AGENTLOG_JOBS"} {
		t.Setenv(v, "")
	}
	// A fixed clock, mid-afternoon, so "today" and day clues do not depend on
	// when the tests run.
	w := &world{t: t, root: root, now: time.Date(2026, 9, 25, 15, 0, 0, 0, time.Local)}
	w.checkout()
	w.invoice()
	w.launch()
	w.release()
	w.drafts()
	w.codexSession()
	w.copilotSession()
	w.rename()
	w.docs()
	w.lost()
	w.team()
	w.worktree()
	w.blank()
	w.moved()
	return w
}

// run executes one agentlog command line from cwd and returns what it printed
// on stdout and stderr together, and its exit code.
func (w *world) run(cwd string, args ...string) (string, int) {
	w.t.Helper()
	cat := &catalog.Catalog{
		Providers: []catalog.Provider{claude.New(), codex.New(), copilot.New()},
		CWD:       cwd,
		Now:       w.now,
	}
	var out bytes.Buffer
	code := Run(Env{Catalog: cat, Stdout: &out, Stderr: &out, Ctx: context.Background()}, args)
	return out.String(), code
}

// feedbackWorld reproduces noisy queued prompts, a paste, refusal details,
// nested agentlog output, and images in both a prompt and a tool result.
func (w *world) feedbackWorld() string {
	id := "feed0000-0000-4000-8000-000000000001"
	u := func(text string) map[string]any {
		return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
	}
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aW1hZ2U="}}
	recs := []map[string]any{
		u("human first"),
		{"type": "queue-operation", "operation": "enqueue", "content": "<cross-session-message from-name=\"peer\">tool-only-needle</cross-session-message>"},
		{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": "<task-notification><status>done</status><summary>background</summary></task-notification>"}},
		{"type": "user", "pasted": true, "message": map[string]any{"role": "user", "content": "human pasted " + strings.Repeat("x", 3000)}},
		{"type": "assistant", "message": map[string]any{"model": "claude-opus", "content": []any{map[string]any{"type": "tool_use", "id": "nest", "name": "Bash", "input": map[string]any{"command": "agentlog other#1 --full"}}}}},
		{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "nest", "content": "#999 assistant historical-only-needle"}}}},
		{"type": "attachment", "attachment": map[string]any{"type": "queued_command", "prompt": []any{map[string]any{"type": "text", "text": "human attached"}, image}}},
		{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "img", "name": "Bash", "input": map[string]any{"command": "draw"}}}}},
		{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "img", "content": []any{image}}}}},
		{"type": "assistant", "message": map[string]any{"stop_details": map[string]any{"type": "refusal", "category": "policy", "explanation": "blocked"}, "content": []any{map[string]any{"type": "text", "text": "refused"}}}},
	}
	for i, r := range recs {
		r["sessionId"] = id
		r["cwd"] = "/work/feedback"
		r["timestamp"] = w.at(time.Hour - time.Duration(i)*time.Second)
		r["version"] = "2.1.289"
	}
	w.file(filepath.Join(w.root, "claude", "projects", "-work-feedback", id+".jsonl"), time.Hour, recs)
	return id
}

// at is a time relative to now.
func (w *world) at(ago time.Duration) string {
	return w.now.Add(-ago).UTC().Format(time.RFC3339Nano)
}

// file writes JSONL records and dates the file the way the harness would
// have left it: last written age ago.
func (w *world) file(path string, age time.Duration, records []map[string]any) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			w.t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	w.write(path, age, b.String())
}

func (w *world) write(path string, age time.Duration, body string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
	mt := w.now.Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		w.t.Fatal(err)
	}
}

// ─── Claude ─────────────────────────────────────────────────────

// claudeLog builds a Claude Code session log. Every record is linked to the
// one before it unless it names its parent, the way Claude Code writes them.
type claudeLog struct {
	w       *world
	session string
	cwd     string
	start   time.Duration // how long ago the first record was written
	step    time.Duration
	n       int
	last    string
	prefix  string         // uuid prefix, unique per log
	version string         // the Claude Code that wrote it
	extra   map[string]any // fields every record carries, like a subagent's agentId
	records []map[string]any
}

func (w *world) claude(session, cwd string, start time.Duration) *claudeLog {
	return &claudeLog{w: w, session: session, cwd: cwd, start: start, step: time.Minute, prefix: session[:8], version: "2.1.281"}
}

// uuid is the uuid of the nth record added, so a record can name one that
// has not been written yet.
func (l *claudeLog) uuid(n int) string { return l.prefix + "-0000-4000-8000-" + fmt12(n) }

// add appends a record, linking it to parent ("" for the previous record, "-"
// for none) and returning its uuid.
func (l *claudeLog) add(parent string, r map[string]any) string {
	l.n++
	uuid := l.uuid(l.n)
	switch parent {
	case "":
		parent = l.last
	case "-":
		parent = ""
	}
	r["uuid"] = uuid
	if parent != "" {
		r["parentUuid"] = parent
	} else {
		r["parentUuid"] = nil
	}
	r["sessionId"] = l.session
	r["cwd"] = l.cwd
	r["version"] = l.version
	r["gitBranch"] = "main"
	r["timestamp"] = l.w.at(l.start - time.Duration(l.n)*l.step)
	for k, v := range l.extra {
		r[k] = v
	}
	l.records = append(l.records, r)
	l.last = uuid
	return uuid
}

func (l *claudeLog) user(parent, text string) string {
	return l.add(parent, map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
}

func (l *claudeLog) assistant(parent, stop string, blocks ...map[string]any) string {
	return l.add(parent, map[string]any{"type": "assistant", "message": map[string]any{
		"id": "msg_" + itoa(l.n+1), "role": "assistant", "model": "claude-opus-5-5", "content": blocks,
		"stop_reason": stop, "usage": map[string]any{"input_tokens": 1200, "output_tokens": 80, "cache_read_input_tokens": 5000},
	}})
}

func (l *claudeLog) result(id, content string, isError bool, extra map[string]any) string {
	r := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]any{
		{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": isError},
	}}}
	if extra != nil {
		r["toolUseResult"] = extra
	}
	return l.add("", r)
}

// raw appends a record that takes no part in the uuid chain.
func (l *claudeLog) raw(r map[string]any) { l.records = append(l.records, r) }

func (l *claudeLog) save(path string, age time.Duration) { l.w.file(path, age, l.records) }

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
func thinking(s string) map[string]any {
	return map[string]any{"type": "thinking", "thinking": s, "signature": "sig"}
}
func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func (w *world) claudeProject(cwd string) string {
	return filepath.Join(w.root, "claude", "projects", strings.ReplaceAll(cwd, "/", "-"))
}

// checkout is the busy session: a fix with reads, an edit, a failing test run
// and a reviewer subagent, then a compaction whose logical parent is written
// after it, then a rewound prompt and a message from a
// teammate.
func (w *world) checkout() {
	l := w.claude(checkoutID, "/work/shop", 70*time.Minute)
	l.add("-", map[string]any{"type": "ai-title", "aiTitle": "Fix checkout rounding"})
	l.user("-", "fix the rounding bug in checkout")
	l.assistant("", "tool_use", thinking("The total is rounded before tax is applied."))
	l.assistant("", "tool_use", text("Reading the checkout code first."))
	l.assistant("", "tool_use", toolUse("toolu_read", "Read", map[string]any{"file_path": "/work/shop/src/checkout.ts"}))
	l.result("toolu_read", "export function total(sub, tax) {\n  return round(sub) * tax\n}", false, nil)
	l.assistant("", "tool_use", toolUse("toolu_edit", "Edit", map[string]any{
		"file_path": "/work/shop/src/checkout.ts", "old_string": "  return round(sub) * tax", "new_string": "  return round(sub * tax)"}))
	l.result("toolu_edit", "The file /work/shop/src/checkout.ts has been updated.", false, nil)
	l.assistant("", "tool_use", toolUse("toolu_test", "Bash", map[string]any{"command": "npm test -- checkout", "description": "Run the checkout tests"}))
	l.result("toolu_test", "FAIL src/checkout.test.ts\n  expected 10.8 to equal 10.80", true, nil)
	l.assistant("", "tool_use", toolUse("toolu_agent", "Agent", map[string]any{
		"description": "review the fix", "subagent_type": "reviewer", "prompt": "Check the rounding change in src/checkout.ts."}))
	l.result("toolu_agent", "The change is right; the test compares strings.", false, map[string]any{"agentId": reviewerID})

	// A compaction as Claude Code writes it: the boundary has no parent, and
	// names as its logical parent a record written after it; attachments
	// and the summary follow.
	boundary := l.add("-", map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted",
		"level": "info", "logicalParentUuid": l.uuid(l.n + 4), "compactMetadata": map[string]any{"trigger": "auto", "preTokens": 367135}})
	l.add(boundary, map[string]any{"type": "attachment", "attachment": map[string]any{"type": "date", "date": "2026-09-25"}})
	l.add("", map[string]any{"type": "user", "isCompactSummary": true, "isVisibleInTranscriptOnly": true, "message": map[string]any{"role": "user",
		"content": "This session is being continued from a previous conversation that ran out of context."}})
	l.add("", map[string]any{"type": "attachment", "attachment": map[string]any{"type": "total_tokens_reminder"}})
	l.user("", "now add a test for the rounding")
	answered := l.assistant("", "end_turn", text("Added a test for the rounding."))

	// Rewound: the user asked this, went back to before it, and asked
	// something else instead.
	l.user(answered, "try a different approach with floats")
	l.assistant("", "end_turn", text("Switched the totals to floats."))
	l.user(answered, "keep the decimals and make the test compare numbers")
	l.assistant("", "end_turn", text("The test compares numbers now."))

	l.user("", `<teammate-message teammate_id="team-lead" summary="status">Where are we on checkout?</teammate-message>`)
	l.assistant("", "end_turn", text("Tests pass and the fix is in."))
	l.save(filepath.Join(w.claudeProject("/work/shop"), checkoutID+".jsonl"), 10*time.Minute)

	sub := w.claude(checkoutID, "/work/shop", 60*time.Minute)
	sub.prefix = reviewerID[:8]
	sub.extra = map[string]any{"isSidechain": true, "agentId": reviewerID}
	sub.user("-", "Check the rounding change in src/checkout.ts.")
	sub.assistant("", "tool_use", toolUse("toolu_r1", "Read", map[string]any{"file_path": "/work/shop/src/checkout.ts"}))
	sub.result("toolu_r1", "export function total(sub, tax) {\n  return round(sub * tax)\n}", false, nil)
	sub.assistant("", "end_turn", text("The change is right; the test compares strings."))
	dir := filepath.Join(w.claudeProject("/work/shop"), checkoutID, "subagents")
	sub.save(filepath.Join(dir, "agent-"+reviewerID+".jsonl"), 55*time.Minute)
	w.write(filepath.Join(dir, "agent-"+reviewerID+".meta.json"), 55*time.Minute,
		`{"agentType":"reviewer","description":"review the fix","toolUseId":"toolu_agent","spawnDepth":1,"model":"sonnet"}`)
}

// invoice is older than the default seven-day window.
func (w *world) invoice() {
	l := w.claude(invoiceID, "/work/shop", 9*24*time.Hour)
	l.add("-", map[string]any{"type": "ai-title", "aiTitle": "Export invoices to CSV"})
	l.user("-", "export the invoices to csv")
	l.assistant("", "end_turn", text("Wrote invoices.csv."))
	l.save(filepath.Join(w.claudeProject("/work/shop"), invoiceID+".jsonl"), 9*24*time.Hour-10*time.Minute)
}

// launch is in another project and writes a new file. The agent left
// feedback about agentlog from inside a sandbox, where only its log keeps it.
func (w *world) launch() {
	l := w.claude(launchID, "/work/blog", 3*time.Hour)
	l.add("-", map[string]any{"type": "ai-title", "aiTitle": "Draft the launch post"})
	l.user("-", "draft the launch post")
	l.assistant("", "tool_use", toolUse("toolu_w", "Write", map[string]any{
		"file_path": "/work/blog/posts/launch.md", "content": "# Launch\n\nWe shipped.\n"}))
	l.result("toolu_w", "File created successfully at: /work/blog/posts/launch.md", false, nil)
	l.assistant("", "tool_use", toolUse("toolu_fb", "Bash", map[string]any{
		"command": `agentlog feedback add "a word search should say when it matched titles only"`}))
	l.result("toolu_fb", "feedback noted: `agentlog feedback` reads it from this session's log, so nothing needs saving", false, nil)
	l.assistant("", "end_turn", text("The draft is in posts/launch.md."))
	l.save(filepath.Join(w.claudeProject("/work/blog"), launchID+".jsonl"), 2*time.Hour)
}

// release is being written right now and is waiting on a tool call. It keeps
// a plan, fails an edit (the string was not in the file), commits, and calls
// an MCP tool whose arguments have no obvious subject. One record is of a type
// this reader has never seen, the way a new Claude Code release first shows
// up.
func (w *world) release() {
	l := w.claude(releaseID, "/work/shop", 8*time.Minute)
	l.raw(map[string]any{"type": "ai-title", "aiTitle": "Ship the rounding fix", "sessionId": releaseID})
	l.user("-", "commit the fix and open a PR")
	l.assistant("", "tool_use", toolUse("toolu_todo", "TodoWrite", map[string]any{"todos": []map[string]any{
		{"content": "Run the tests", "status": "completed", "activeForm": "Running the tests"},
		{"content": "Commit the fix", "status": "in_progress", "activeForm": "Committing the fix"},
		{"content": "Open the PR", "status": "pending", "activeForm": "Opening the PR"},
	}}))
	l.result("toolu_todo", "Todos have been modified successfully.", false, nil)
	l.assistant("", "tool_use", toolUse("toolu_log", "Edit", map[string]any{
		"file_path": "/work/shop/CHANGELOG.md", "old_string": "## Unreleased", "new_string": "## Unreleased\n- Round after tax"}))
	l.result("toolu_log", "<tool_use_error>String to replace not found in file.\nString: ## Unreleased</tool_use_error>", true, nil)
	l.assistant("", "tool_use", toolUse("toolu_commit", "Bash", map[string]any{"command": `git commit -am "Round after tax"`, "description": "Commit the fix"}))
	l.result("toolu_commit", "[main 4f3a2b1] Round after tax\n 1 file changed, 1 insertion(+), 1 deletion(-)", false, map[string]any{
		"stdout": "[main 4f3a2b1] Round after tax", "stderr": "", "interrupted": false,
		"gitOperation": map[string]any{"commit": map[string]any{"sha": "4f3a2b1", "kind": "committed", "branch": "main"}}})
	l.raw(map[string]any{"type": "session-weather", "sessionId": releaseID, "forecast": "sunny", "timestamp": w.at(3 * time.Minute)})
	l.assistant("", "tool_use", toolUse("toolu_pr", "mcp__github__create_pull_request", map[string]any{
		"owner": "acme", "repo": "shop", "title": "Round after tax", "head": "fix-rounding", "base": "main"}))
	l.result("toolu_pr", `{"number": 42, "html_url": "https://github.com/acme/shop/pull/42"}`, false, nil)
	l.assistant("", "tool_use", toolUse("toolu_watch", "Bash", map[string]any{"command": "gh pr checks 42 --watch"}))
	l.save(filepath.Join(w.claudeProject("/work/shop"), releaseID+".jsonl"), 20*time.Second)
}

// drafts has more prompts than a preview lists, and the person stopped the
// last reply partway.
func (w *world) drafts() {
	l := w.claude(draftsID, "/work/blog", 7*time.Hour)
	l.raw(map[string]any{"type": "ai-title", "aiTitle": "Tighten the launch post", "sessionId": draftsID})
	// The instructions Claude Code hands every session (CLAUDE.md) are an
	// attachment, which no view shows.
	l.raw(map[string]any{"type": "attachment", "sessionId": draftsID, "timestamp": w.at(7 * time.Hour),
		"attachment": map[string]any{"type": "instructions", "content": "House rules (see STYLE.md): short sentences, no exclamation marks."}})
	parent := "-"
	for i, p := range []string{"shorter intro", "cut the second paragraph", "add a pricing line", "link the docs",
		"fix the heading case", "swap the hero image", "less hype in the ending", "one more pass on the title"} {
		l.user(parent, p)
		if i < 7 {
			l.assistant("", "end_turn", text("Done: "+p+"."))
		}
		parent = ""
	}
	l.user("", "[Request interrupted by user]")
	l.save(filepath.Join(w.claudeProject("/work/blog"), draftsID+".jsonl"), 6*time.Hour)
}

// rename is a mechanical change across a package: more files than a preview
// lists, more tools than its tools line names, and commits on two branches
// with a push between them. It ran out of context and was continued in docs
// (cbfe7aac), and it was written by an older Claude Code than the rest.
func (w *world) rename() {
	l := w.claude(renameID, "/work/api", 5*time.Hour)
	l.step = 30 * time.Second
	l.version = "2.1.279"
	l.add("-", map[string]any{"type": "ai-title", "aiTitle": "Rename the client package"})
	l.user("-", "rename pkg/client to pkg/apiclient everywhere")
	for i, call := range []map[string]any{
		toolUse("toolu_grep", "Grep", map[string]any{"pattern": "pkg/client"}),
		toolUse("toolu_glob", "Glob", map[string]any{"pattern": "**/*.go"}),
		toolUse("toolu_read", "Read", map[string]any{"file_path": "/work/api/go.mod"}),
		toolUse("toolu_fetch", "WebFetch", map[string]any{"url": "https://go.dev/ref/mod", "prompt": "module path rules"}),
	} {
		l.assistant("", "tool_use", call)
		l.result(call["id"].(string), "result "+itoa(i), false, nil)
	}
	for _, f := range []string{"cmd/api/main.go", "pkg/apiclient/client.go", "pkg/apiclient/retry.go", "pkg/apiclient/auth.go",
		"pkg/apiclient/errors.go", "pkg/apiclient/client_test.go", "internal/billing/charge.go", "internal/billing/refund.go",
		"internal/users/sync.go", "internal/users/import.go", "internal/jobs/nightly.go", "internal/jobs/export.go", "tools/gen/main.go"} {
		id := "toolu_e_" + strings.ReplaceAll(f, "/", "_")
		l.assistant("", "tool_use", toolUse(id, "Edit", map[string]any{
			"file_path": "/work/api/" + f, "old_string": `"example.com/api/pkg/client"`, "new_string": `"example.com/api/pkg/apiclient"`}))
		l.result(id, "The file /work/api/"+f+" has been updated.", false, nil)
	}
	for i, op := range []map[string]any{
		{"commit": map[string]any{"sha": "a1a1a1a", "kind": "committed", "branch": "rename"}},
		{"commit": map[string]any{"sha": "b2b2b2b", "kind": "committed", "branch": "rename"}},
		{"commit": map[string]any{"sha": "c3c3c3c", "kind": "committed", "branch": "rename"}, "push": map[string]any{"branch": "rename"}},
		{"commit": map[string]any{"sha": "d4d4d4d", "kind": "committed", "branch": "rename"}},
		{"commit": map[string]any{"sha": "e5e5e5e", "kind": "committed", "branch": "main"}},
	} {
		id := "toolu_git" + itoa(i)
		l.assistant("", "tool_use", toolUse(id, "Bash", map[string]any{"command": "git commit -am 'rename step " + itoa(i) + "'"}))
		l.result(id, "committed", false, map[string]any{"stdout": "committed", "stderr": "", "interrupted": false, "gitOperation": op})
	}
	// Plugin MCP tools have long names that differ only at the end, and
	// numbers for arguments.
	l.assistant("", "tool_use", toolUse("toolu_shot", "mcp__plugin_chrome-devtools-mcp_chrome-devtools__resize_page", map[string]any{"width": 1280, "height": 800}))
	l.result("toolu_shot", "resized", false, nil)
	// The person switched models with /model for the last reply.
	l.assistant("", "end_turn", text("The package is renamed; the docs still say pkg/client."))
	l.records[len(l.records)-1]["message"].(map[string]any)["model"] = "claude-sonnet-5-5"
	l.raw(map[string]any{"type": "continued-in", "sessionId": renameID, "continuedInSessionId": docsID, "timestamp": w.at(3 * time.Hour)})
	l.save(filepath.Join(w.claudeProject("/work/api"), renameID+".jsonl"), 3*time.Hour)
}

// docs continues rename: it opens on the compaction that carried rename's
// context over, compacts again later, and is running now with a background
// subagent that is also still writing. Its own edit of a doc failed where the
// subagent's landed. It was written by a newer Claude Code that brought
// five record types this build has never seen.
func (w *world) docs() {
	l := w.claude(docsID, "/work/api", 3*time.Hour)
	l.version = "2.2.0"
	carried := w.claude(renameID, "/work/api", 0)
	carried.n = 0
	l.add("-", map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted", "level": "info",
		"logicalParentUuid": carried.uuid(40), "compactMetadata": map[string]any{"trigger": "auto", "preTokens": 180000}})
	l.add("", map[string]any{"type": "user", "isCompactSummary": true, "isVisibleInTranscriptOnly": true, "message": map[string]any{"role": "user",
		"content": "This session is being continued from a previous conversation that ran out of context."}})
	l.user("", "finish the rename in the docs")
	l.assistant("", "tool_use", toolUse("toolu_bg", "Agent", map[string]any{
		"description": "update the docs", "subagent_type": "general-purpose", "run_in_background": true,
		"prompt": "Replace pkg/client with pkg/apiclient in docs/."}))
	l.result("toolu_bg", "Async agent launched successfully.", false, map[string]any{"agentId": docsAgent, "isAsync": true})
	l.assistant("", "tool_use", toolUse("toolu_doc", "Edit", map[string]any{
		"file_path": "/work/api/docs/client.md", "old_string": "pkg/client", "new_string": "pkg/apiclient"}))
	l.result("toolu_doc", "<tool_use_error>File has been modified since read, either by the user or by a linter.</tool_use_error>", true, nil)
	for _, typ := range []string{"agent-color", "hook-progress", "mcp-reconnect", "session-mode", "tip-shown"} {
		l.raw(map[string]any{"type": typ, "sessionId": docsID, "timestamp": w.at(2 * time.Hour)})
	}
	l.add("-", map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted", "level": "info",
		"logicalParentUuid": l.last, "compactMetadata": map[string]any{"trigger": "auto", "preTokens": 190000}})
	l.user("", "keep going while the docs agent works")
	l.assistant("", "tool_use", toolUse("toolu_build", "Bash", map[string]any{"command": "go build ./..."}))
	l.save(filepath.Join(w.claudeProject("/work/api"), docsID+".jsonl"), 30*time.Second)

	// No .meta.json beside it, so it has no title and no type: the preview
	// has to say what it was asked from its log.
	sub := w.claude(docsID, "/work/api", 20*time.Minute)
	sub.prefix = docsAgent[:8]
	sub.extra = map[string]any{"isSidechain": true, "agentId": docsAgent}
	sub.user("-", "Replace pkg/client with pkg/apiclient in docs/.")
	sub.assistant("", "tool_use", toolUse("toolu_s1", "Edit", map[string]any{
		"file_path": "/work/api/docs/client.md", "old_string": "pkg/client", "new_string": "pkg/apiclient"}))
	sub.result("toolu_s1", "The file /work/api/docs/client.md has been updated.", false, nil)
	sub.assistant("", "tool_use", text("docs/client.md is done; the README is next."))
	sub.save(filepath.Join(w.claudeProject("/work/api"), docsID, "subagents", "agent-"+docsAgent+".jsonl"), 10*time.Second)
}

// worktree changed the checkout code in a worktree of the shop repository,
// the way Claude Code's worktrees sit inside it (.claude/worktrees/…): the
// same file at another path.
func (w *world) worktree() {
	cwd := "/work/shop/.claude/worktrees/tax"
	l := w.claude(worktreeID, cwd, 26*time.Hour)
	l.add("-", map[string]any{"type": "ai-title", "aiTitle": "Tax rounding in a worktree"})
	l.user("-", "try rounding per line item in a worktree")
	l.assistant("", "tool_use", toolUse("toolu_wt", "Edit", map[string]any{
		"file_path": cwd + "/src/checkout.ts", "old_string": "  return round(sub) * tax", "new_string": "  return items.map(round).sum() * tax"}))
	l.result("toolu_wt", "The file "+cwd+"/src/checkout.ts has been updated.", false, nil)
	// The API gave up before the model could reply.
	l.add("", map[string]any{"type": "system", "subtype": "api_error", "level": "error",
		"content": "API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"})
	l.save(filepath.Join(w.claudeProject(cwd), worktreeID+".jsonl"), 25*time.Hour)
}

// blank is two sessions with nothing said in them, the newest in /work/shop:
// one cleared as soon as it opened, and one closed
// before anything with a time was written.
func (w *world) blank() {
	l := w.claude(clearedID, "/work/shop", 6*time.Minute)
	l.raw(map[string]any{"type": "mode", "mode": "normal", "sessionId": clearedID})
	l.add("-", map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": "<command-name>/clear</command-name>\n            <command-message>clear</command-message>\n            <command-args></command-args>"}})
	l.add("", map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<local-command-stdout></local-command-stdout>"}})
	l.save(filepath.Join(w.claudeProject("/work/shop"), clearedID+".jsonl"), 4*time.Minute)
	o := w.claude(openedID, "/work/shop", 5*time.Minute)
	o.raw(map[string]any{"type": "mode", "mode": "normal", "sessionId": openedID})
	o.raw(map[string]any{"type": "permission-mode", "permissionMode": "auto", "sessionId": openedID})
	o.save(filepath.Join(w.claudeProject("/work/shop"), openedID+".jsonl"), 5*time.Minute)
}

// moved began in /work and moved into /work/site, where Claude Code filed
// its log and where the work was.
func (w *world) moved() {
	l := w.claude(movedID, "/work", 4*time.Hour+5*time.Minute)
	l.user("-", "make a site for the shop")
	l.assistant("", "end_turn", text("Starting it in /work/site."))
	l.raw(map[string]any{"type": "relocated", "sessionId": movedID, "relocatedCwd": "/work/site"})
	l.cwd = "/work/site"
	l.user("", "add a pricing page")
	l.assistant("", "end_turn", text("The pricing page is in."))
	l.save(filepath.Join(w.claudeProject("/work/site"), movedID+".jsonl"), 4*time.Hour)
}

// lost opens on a compaction carried over from a session whose log is no
// longer on disk (94285df6), and stops on a test run that never returned: the
// process was killed while the tool ran.
func (w *world) lost() {
	l := w.claude(lostID, "/work/api", 50*time.Hour)
	l.add("-", map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted", "level": "info",
		"logicalParentUuid": "ffffffff-0000-4000-8000-000000000001", "compactMetadata": map[string]any{"trigger": "auto", "preTokens": 150000}})
	l.add("", map[string]any{"type": "user", "isCompactSummary": true, "isVisibleInTranscriptOnly": true, "message": map[string]any{"role": "user",
		"content": "This session is being continued from a previous conversation that ran out of context."}})
	l.user("", "pick up the retry work where it stopped")
	l.assistant("", "end_turn", text("Retries back off exponentially now."))
	l.user("", "push the branch")
	l.assistant("", "tool_use", toolUse("toolu_push", "Bash", map[string]any{"command": "git push -u origin retries"}))
	l.result("toolu_push", "branch 'retries' set up to track 'origin/retries'.", false, map[string]any{
		"stdout": "", "stderr": "", "interrupted": false, "gitOperation": map[string]any{"push": map[string]any{"branch": "retries"}}})
	l.user("", "run the whole suite")
	l.assistant("", "tool_use", toolUse("toolu_suite", "Bash", map[string]any{"command": "go test ./..."}))
	l.save(filepath.Join(w.claudeProject("/work/api"), lostID+".jsonl"), 49*time.Hour)
}

// ─── Codex ──────────────────────────────────────────────────────

// codexSession writes shell calls the way code mode does, as an exec script,
// patches a file, and ends on a usage limit.
func (w *world) codexSession() {
	start := 40 * time.Minute
	rec := func(i int, typ string, payload map[string]any) map[string]any {
		return map[string]any{"timestamp": w.at(start - time.Duration(i)*time.Minute), "type": typ, "payload": payload}
	}
	item := func(i int, p map[string]any) map[string]any { return rec(i, "response_item", p) }
	patch := "*** Begin Patch\n*** Update File: src/checkout.test.ts\n@@\n-  expect(total(9, 1.2)).toBe('10.80')\n+  expect(total(9, 1.2)).toBeCloseTo(10.8)\n*** End Patch"
	records := []map[string]any{
		rec(0, "session_meta", map[string]any{"id": codexID, "timestamp": w.at(start), "cwd": "/work/shop", "originator": "codex_cli_rs", "cli_version": "0.130.0"}),
		rec(0, "turn_context", map[string]any{"cwd": "/work/shop", "model": "gpt-6-astra", "effort": "high"}),
		item(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "make the checkout test compare numbers"}}}),
		rec(1, "event_msg", map[string]any{"type": "user_message", "message": "make the checkout test compare numbers"}),
		rec(1, "event_msg", map[string]any{"type": "thread_goal_updated", "goal": map[string]any{"objective": "the checkout tests compare numbers", "status": "active"}}),
		item(1, map[string]any{"type": "function_call", "name": "update_plan", "call_id": "u1",
			"arguments": `{"plan":[{"step":"run the failing test","status":"completed"},{"step":"compare as numbers","status":"in_progress"}]}`}),
		item(1, map[string]any{"type": "function_call_output", "call_id": "u1", "output": "Plan updated"}),
		item(2, map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c1", "input": `text(await tools.exec_command({cmd:"npm test -- checkout"}))`}),
		item(3, map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": "FAIL src/checkout.test.ts"}),
		item(4, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "p1", "input": patch}),
		item(5, map[string]any{"type": "custom_tool_call_output", "call_id": "p1", "output": "Success. Updated the following files:\nM src/checkout.test.ts"}),
		rec(5, "event_msg", map[string]any{"type": "token_count",
			"info": map[string]any{"total_token_usage": map[string]any{"input_tokens": 48000, "cached_input_tokens": 30000, "output_tokens": 2200, "reasoning_output_tokens": 900, "total_tokens": 50200},
				"last_token_usage": map[string]any{"input_tokens": 9000, "cached_input_tokens": 8000, "output_tokens": 300, "reasoning_output_tokens": 100, "total_tokens": 9300}, "model_context_window": 258400},
			"rate_limits": map[string]any{"limit_id": "codex", "plan_type": "pro",
				"primary":   map[string]any{"used_percent": 62.0, "window_minutes": 300, "resets_at": w.now.Add(2 * time.Hour).Unix()},
				"secondary": map[string]any{"used_percent": 18.0, "window_minutes": 10080, "resets_at": w.now.Add(72 * time.Hour).Unix()}}}),
		item(6, map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []map[string]any{{"type": "output_text", "text": "The test compares numbers now."}}}),
		rec(7, "event_msg", map[string]any{"type": "task_complete", "last_agent_message": nil, "error": map[string]any{
			"message": "You've hit your usage limit. Try again at 7:46 PM.", "codex_error_info": "usage_limit_exceeded"}}),
	}
	day := w.now.Add(-start)
	path := filepath.Join(w.root, "codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"),
		"rollout-"+day.Format("2006-01-02T15-04-05")+"-"+codexID+".jsonl")
	w.file(path, 30*time.Minute, records)
}

// team is a multi-agent Codex thread: the model's reasoning and
// the messages between agents are encrypted, one subagent does the work, and
// the thread was rolled back once, so a second page carries on from the
// first. One output arrives with a call id its call never had.
func (w *world) team() {
	start := 2 * time.Hour
	rec := func(i int, typ string, payload map[string]any) map[string]any {
		return map[string]any{"timestamp": w.at(start - time.Duration(i)*time.Minute), "type": typ, "payload": payload}
	}
	item := func(i int, p map[string]any) map[string]any { return rec(i, "response_item", p) }
	secret := "gAAAAAB" + strings.Repeat("qoA4BztorQIuBor6Pj-3Ig0OInjm", 4)
	meta := func(i int, extra map[string]any) map[string]any {
		m := map[string]any{"id": teamID, "timestamp": w.at(start - time.Duration(i)*time.Minute), "cwd": "/work/api", "originator": "Codex Desktop",
			"cli_version": "0.130.0", "git": map[string]any{"branch": "rename", "commit_hash": "9c1d2e3f4a5b6c7d8e9f"}}
		for k, v := range extra {
			m[k] = v
		}
		return rec(i, "session_meta", m)
	}
	dir := func(at time.Duration) string {
		day := w.now.Add(-at)
		return filepath.Join(w.root, "codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	}
	stamp := func(at time.Duration) string { return w.now.Add(-at).Format("2006-01-02T15-04-05") }
	w.file(filepath.Join(dir(start), "rollout-"+stamp(start)+"-"+teamID+".jsonl"), 90*time.Minute, []map[string]any{
		meta(0, nil),
		item(1, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "audit the api docs for the old package name"}}}),
		item(2, map[string]any{"type": "reasoning", "summary": []map[string]any{{"type": "summary_text", "text": "One agent can read the docs while I check the code."}}, "encrypted_content": secret}),
		item(2, map[string]any{"type": "function_call", "name": "update_plan", "call_id": "u1",
			"arguments": `{"explanation":"The docs are the slow part.","plan":[{"step":"audit the docs","status":"in_progress"},{"step":"fix what is stale","status":"pending"}]}`}),
		item(2, map[string]any{"type": "function_call_output", "call_id": "u1", "output": "Plan updated"}),
		item(3, map[string]any{"type": "function_call", "name": "spawn_agent", "call_id": "s1",
			"arguments": `{"task_name":"docs_audit","fork_turns":"none","message":"` + secret + `"}`}),
		item(3, map[string]any{"type": "function_call_output", "call_id": "s1", "output": `{"agent_id":"` + teslaID + `"}`}),
		item(4, map[string]any{"type": "function_call", "name": "send_message", "call_id": "m1", "arguments": `{"target":"/root/docs_audit","message":"` + secret + `"}`}),
		item(4, map[string]any{"type": "function_call_output", "call_id": "m1", "output": "delivered"}),
		item(5, map[string]any{"type": "function_call_output", "call_id": "gone", "output": "exit 0"}),
		item(6, map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []map[string]any{{"type": "output_text", "text": "The docs audit found one stale link."}}}),
		rec(6, "event_msg", map[string]any{"type": "task_complete", "last_agent_message": "The docs audit found one stale link."}),
	})
	w.file(filepath.Join(dir(start-10*time.Minute), "rollout-"+stamp(start-10*time.Minute)+"-"+teamID+"_01a0eeee-1111-7000-8000-000000000001.jsonl"), 80*time.Minute, []map[string]any{
		meta(10, map[string]any{"history_base": map[string]any{"thread_id": teamID, "end_ordinal_exclusive": 99}}),
		item(11, map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "fix the stale link too"}}}),
		item(12, map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []map[string]any{{"type": "output_text", "text": "Fixed the link."}}}),
		// Read two hours ago: the 5h window has turned over since.
		rec(12, "event_msg", map[string]any{"type": "token_count",
			"info": map[string]any{"total_token_usage": map[string]any{"input_tokens": 9000, "cached_input_tokens": 6000, "output_tokens": 700, "total_tokens": 9700}},
			"rate_limits": map[string]any{"limit_id": "codex",
				"primary": map[string]any{"used_percent": 97.0, "window_minutes": 300, "resets_at": w.now.Add(-30 * time.Minute).Unix()}}}),
		rec(12, "event_msg", map[string]any{"type": "task_complete", "last_agent_message": "Fixed the link."}),
	})

	sub := func(i int, typ string, p map[string]any) map[string]any { return rec(i, typ, p) }
	w.file(filepath.Join(dir(start-3*time.Minute), "rollout-"+stamp(start-3*time.Minute)+"-"+teslaID+".jsonl"), 85*time.Minute, []map[string]any{
		sub(3, "session_meta", map[string]any{"id": teslaID, "timestamp": w.at(start - 3*time.Minute), "cwd": "/work/api", "originator": "Codex Desktop", "cli_version": "0.130.0",
			"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": teamID, "depth": 1, "agent_path": "/root/docs_audit", "agent_nickname": "Tesla"}}}}),
		sub(3, "response_item", map[string]any{"type": "agent_message", "author": "/root", "recipient": "/root/docs_audit",
			"content": []map[string]any{{"type": "input_text", "text": "Message Type: NEW_TASK\nTask name: /root/docs_audit"}, {"type": "encrypted_content", "text": secret}}}),
		sub(4, "response_item", map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "p1",
			"input": "*** Begin Patch\n*** Update File: docs/index.md\n@@\n-[client](pkg/client)\n+[client](pkg/apiclient)\n*** Update File: docs/client.md\n@@\n-see pkg/client\n+see pkg/apiclient\n*** End Patch"}),
		sub(5, "response_item", map[string]any{"type": "custom_tool_call_output", "call_id": "p1", "output": "Success. Updated the following files:\nM docs/client.md"}),
		// Codex writes an answer twice: as a message, and as the item whose
		// phase says it is the final one.
		sub(6, "response_item", map[string]any{"type": "message", "role": "assistant", "phase": "final_answer", "content": []map[string]any{{"type": "output_text", "text": "Updated docs/client.md; one link still points at pkg/client."}}}),
		sub(6, "event_msg", map[string]any{"type": "item_completed", "thread_id": teslaID, "item": map[string]any{"type": "AgentMessage", "phase": "final_answer",
			"content": []map[string]any{{"type": "Text", "text": "Updated docs/client.md; one link still points at pkg/client."}}}}),
		sub(7, "response_item", map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "The stale link is in docs/index.md."}}}),
	})
}

// ─── Copilot ────────────────────────────────────────────────────

func (w *world) copilotSession() {
	start := 5 * time.Hour
	ev := func(i int, typ string, data map[string]any) map[string]any {
		r := map[string]any{"type": typ, "data": data, "id": "e" + itoa(i), "parentId": nil, "timestamp": w.at(start - time.Duration(i)*time.Minute)}
		if i > 0 {
			r["parentId"] = "e" + itoa(i-1)
		}
		return r
	}
	dir := filepath.Join(w.root, "copilot", "session-state", copilotID)
	w.file(filepath.Join(dir, "events.jsonl"), 4*time.Hour, []map[string]any{
		ev(0, "session.start", map[string]any{"sessionId": copilotID, "version": 1, "producer": "copilot-agent", "copilotVersion": "1.0.40",
			"startTime": w.at(start), "context": map[string]any{"cwd": "/work/ccr", "gitRoot": "/work/ccr", "branch": "main"}}),
		ev(1, "user.message", map[string]any{"content": "fix the tunnel reconnect"}),
		ev(2, "tool.execution_start", map[string]any{"toolCallId": "t1", "toolName": "bash", "arguments": map[string]any{"command": "go test ./tunnel/..."}}),
		ev(3, "tool.execution_complete", map[string]any{"toolCallId": "t1", "success": true, "result": map[string]any{"content": "ok  tunnel 1.2s"}}),
		ev(4, "assistant.message", map[string]any{"content": "The tunnel reconnects after a drop now."}),
	})
	w.write(filepath.Join(dir, "workspace.yaml"), 4*time.Hour, "id: "+copilotID+"\ncwd: /work/ccr\ngit_root: /work/ccr\nbranch: main\nsummary: Fix the tunnel reconnect\ncreated_at: "+w.at(start)+"\nupdated_at: "+w.at(4*time.Hour)+"\n")
}

func fmt12(n int) string {
	s := itoa(n)
	return strings.Repeat("0", 12-len(s)) + s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
