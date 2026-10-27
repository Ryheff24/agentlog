package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// splitID is the cloud session of issue #1: one Claude Code session that
// began in /home/user and carried on in /home/user/webapp, and that Claude
// Code filed under both project folders with the same id.
const splitID = "15142094-0000-4000-8000-000000000011"

// splitAgents are its subagents: the first three under -home-user, the last
// two under -home-user-webapp (171 and 12 in the real container).
var splitAgents = []string{
	"a1000000000000001", "a1000000000000002", "a1000000000000003",
	"a2000000000000004", "a2000000000000005",
}

// How the second file relates to the first. On this machine a move takes
// the whole log, subagents and all, into the new folder (seen three
// times), so nothing is left behind. In the
// container something was, and which of these it was is not yet known.
const (
	continued = "the second file holds only what came after the move"
	copied    = "the second file is a copy of the first, carried on"
)

// cloudWorld is the claude.ai cloud container from issue #1: Ubuntu, home
// /home/user, and nothing on it but the one session agentlog is asked about,
// which is usually the session running agentlog.
func cloudWorld(t *testing.T, shape string) *world {
	t.Helper()
	w := bareWorld(t)
	w.split(shape)
	return w
}

// bareWorld is a machine with no logs and no caller session.
func bareWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	for k, v := range map[string]string{"CLAUDE_CONFIG_DIR": "claude", "CODEX_HOME": "codex", "COPILOT_HOME": "copilot", "AGENTLOG_FEEDBACK_FILE": "feedback.jsonl", "AGENTLOG_CONFIG": "config"} {
		t.Setenv(k, filepath.Join(root, v))
	}
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "COPILOT_SESSION_ID", "AGENTLOG_JOBS"} {
		t.Setenv(v, "")
	}
	return &world{t: t, root: root, now: time.Date(2026, 9, 25, 15, 0, 0, 0, time.Local)}
}

func (w *world) split(shape string) {
	spawn := func(l *claudeLog, agents []string) {
		for _, id := range agents {
			call := "toolu_" + id
			l.assistant("", "tool_use", toolUse(call, "Agent", map[string]any{
				"description": "haiku " + id, "subagent_type": "general-purpose", "model": "haiku", "prompt": "haiku task " + id}))
			l.result(call, "done "+id, false, map[string]any{"agentId": id})
		}
	}
	subagents := func(cwd string, start, age time.Duration, agents []string) {
		dir := filepath.Join(w.claudeProject(cwd), splitID, "subagents")
		for i, id := range agents {
			sub := w.claude(splitID, cwd, start-time.Duration(i+1)*time.Minute)
			sub.prefix = id[:8]
			sub.extra = map[string]any{"isSidechain": true, "agentId": id}
			sub.user("-", "haiku task "+id)
			sub.assistant("", "end_turn", text("finished "+id))
			sub.save(filepath.Join(dir, "agent-"+id+".jsonl"), age)
			w.write(filepath.Join(dir, "agent-"+id+".meta.json"), age,
				fmt.Sprintf(`{"agentType":"general-purpose","description":"haiku %s","model":"haiku"}`, id))
		}
	}

	l := w.claude(splitID, "/home/user", 3*time.Hour)
	l.user("-", "start in home")
	spawn(l, splitAgents[:3])
	l.assistant("", "end_turn", text("Done in /home/user."))
	l.save(filepath.Join(w.claudeProject("/home/user"), splitID+".jsonl"), 150*time.Minute)
	subagents("/home/user", 3*time.Hour, 150*time.Minute, splitAgents[:3])

	// The conversation goes on from where it was, in the new directory.
	if shape == continued {
		l.records = nil
	}
	l.cwd = "/home/user/webapp"
	l.user("", "continue in webapp")
	spawn(l, splitAgents[3:])
	l.assistant("", "end_turn", text("Done in /home/user/webapp."))
	l.save(filepath.Join(w.claudeProject("/home/user/webapp"), splitID+".jsonl"), time.Minute)
	subagents("/home/user/webapp", 2*time.Hour, 90*time.Minute, splitAgents[3:])
}

// Issue #1: with CLAUDE_CODE_SESSION_ID set, the container's only session is
// the caller's, a listing hides it, and what comes back reads as an empty
// machine: "no session matches" over a footer that counts two sessions, the
// usage text, and doctor's "claude no sessions". It has to say that the
// only session there is the caller's own, and how to open it.
func TestOnlyOwnSessionIsSaid(t *testing.T) {
	w := cloudWorld(t, continued)
	t.Setenv("CLAUDE_CODE_SESSION_ID", splitID)

	for _, args := range [][]string{
		{"claude", "all", "--list"},
		{"--list"},
		{"."},
	} {
		out, _ := w.run("/home/user", args...)
		if !strings.Contains(out, "no sessions but your own") || !strings.Contains(out, "agentlog 15142094") {
			t.Errorf("agentlog %s does not say the only session is the caller's:\n%s", strings.Join(args, " "), out)
		}
	}
	out, _ := w.run("/home/user", "doctor", "claude")
	if !strings.Contains(out, "claude    2.1.281") && !strings.Contains(out, "no sessions but your own") {
		t.Errorf("doctor reads the caller's session as no sessions at all:\n%s", out)
	}
}

// Wherever the caller's own session is shown, it is labelled as the caller's,
// so an agent does not read its own work as another agent's. JSON already
// carries "self":true; the text views say nothing.
func TestOwnSessionIsMarkedWhenShown(t *testing.T) {
	w := newWorld(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", checkoutID)
	for _, args := range [][]string{
		{"5a1e0000"},
		{"5a1e0000", "--list"},
		{"5a1e0000", "--tree"},
	} {
		out, code := w.run("/work/shop", args...)
		if code != 0 || !strings.Contains(out, "your own session") {
			t.Errorf("agentlog %s (exit %d) does not mark the caller's session:\n%s", strings.Join(args, " "), code, out)
		}
	}
	out, _ := w.run("/work/shop", "7e1e0000")
	if strings.Contains(out, "your own session") {
		t.Errorf("another session is marked as the caller's:\n%s", out)
	}
}

// Issue #1: one session id filed under two project folders is one session,
// whichever way the second file came about. Today it lists as two rows with
// the same id, every subagent hangs off whichever row was read last,
// --subagents stops to ask which row was meant (which no longer id can
// answer), and opening it shows one file and never mentions the other.
func TestSessionSplitAcrossFoldersIsOneSession(t *testing.T) {
	for _, shape := range []string{continued, copied} {
		t.Run(shape, func(t *testing.T) {
			w := cloudWorld(t, shape)

			out, _ := w.run("/home/user", "claude", "all", "--list")
			if n := strings.Count(out, "15142094  claude"); n != 1 {
				t.Errorf("listed %d rows for one session, want 1:\n%s", n, out)
			}
			if !strings.Contains(out, "+5 subagents") {
				t.Errorf("subagents not counted from both folders, want +5:\n%s", out)
			}

			// A window that holds only the newer file still finds the
			// session whole: the older file is the same session.
			out, _ = w.run("/home/user", "claude", "1h", "--list")
			if strings.Count(out, "15142094  claude") != 1 || !strings.Contains(out, "+5 subagents") {
				t.Errorf("a window over the newer file lost the older half:\n%s", out)
			}

			out, code := w.run("/home/user", "15142094", "--subagents")
			if code != 0 || strings.Contains(out, "equally well") {
				t.Errorf("--subagents asked which of two identical ids was meant (exit %d):\n%s", code, out)
			}
			if missing := absent(out, "task: haiku ", splitAgents); len(missing) > 0 {
				t.Errorf("--subagents is missing %v:\n%s", missing, out)
			}

			out, _ = w.run("/home/user", "15142094", "--tree")
			if n := strings.Count(out, "claude 15142094"); n != 1 {
				t.Errorf("--tree shows the session %d times, want 1:\n%s", n, out)
			}
			if missing := absent(out, "claude ", splitAgents); len(missing) > 0 {
				t.Errorf("--tree is missing %v:\n%s", missing, out)
			}

			// Opening it reads the whole conversation once, in order: it did
			// not stop when it changed folders, and a copied half is not
			// shown twice.
			out, _ = w.run("/home/user", "15142094", "--user")
			home, drd := strings.Index(out, "start in home"), strings.Index(out, "continue in webapp")
			if home < 0 || drd < home || strings.Count(out, "start in home") != 1 || strings.Count(out, "continue in webapp") != 1 {
				t.Errorf("opening the session does not show each prompt once, in order:\n%s", out)
			}
		})
	}
}

func absent(out, prefix string, ids []string) []string {
	var missing []string
	for _, id := range ids {
		if !strings.Contains(out, prefix+id) {
			missing = append(missing, id)
		}
	}
	return missing
}
