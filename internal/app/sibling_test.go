package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const siblingID = "6b0e0000-0000-4000-8000-000000000021"

// sibling is a session that began in one project and carried on in a sibling,
// leaving a log under each project folder: a full copy carried on, the way
// Claude Code writes it, or only the continuation.
func (w *world) sibling(shape string) {
	const alpha, beta = "/Users/sam/projects/alpha", "/Users/sam/projects/beta"
	l := w.claude(siblingID, alpha, 3*time.Hour)
	l.user("-", "fix the alpha parser")
	l.assistant("", "end_turn", text("Fixed in alpha."))
	l.save(filepath.Join(w.claudeProject(alpha), siblingID+".jsonl"), 150*time.Minute)
	if shape == continued {
		l.records = nil
	}
	l.cwd = beta
	l.user("", "now port it to beta")
	l.assistant("", "end_turn", text("Ported to beta."))
	l.save(filepath.Join(w.claudeProject(beta), siblingID+".jsonl"), time.Minute)
}

// A session that moved is still a session of the directory it began in, and
// says where it began when opened.
func TestMovedSessionIsFoundFromEitherDirectory(t *testing.T) {
	for _, shape := range []string{continued, copied} {
		t.Run(shape, func(t *testing.T) {
			w := bareWorld(t)
			w.sibling(shape)
			for _, cwd := range []string{"/Users/sam/projects/alpha", "/Users/sam/projects/beta"} {
				out, _ := w.run(cwd, ".", "all", "--list")
				if strings.Count(out, "6b0e0000  claude") != 1 {
					t.Errorf("from %s, want the session listed once:\n%s", cwd, out)
				}
			}
			out, code := w.run("/", "6b0e0000")
			if code != 0 || !strings.Contains(out, "/Users/sam/projects/beta (started in /Users/sam/projects/alpha") {
				t.Errorf("header does not say where the session began (exit %d):\n%s", code, out)
			}
			out, _ = w.run("/", "6b0e0000", "--json")
			if !strings.Contains(out, `"dirs":[`) || !strings.Contains(out, `"/Users/sam/projects/alpha"`) {
				t.Errorf("--json does not carry the earlier directory:\n%s", out)
			}
		})
	}
}

// A copy carried on in a new folder can bring the subagents/ folder with it,
// so one subagent has a log in each. It is still one subagent.
func TestSubagentCopiedWithItsSessionIsOne(t *testing.T) {
	const alpha, beta = "/Users/sam/projects/alpha", "/Users/sam/projects/beta"
	const agent = "a6b0e0000000000001"
	w := bareWorld(t)
	w.sibling(copied)
	for _, cwd := range []string{alpha, beta} {
		dir := filepath.Join(w.claudeProject(cwd), siblingID, "subagents")
		sub := w.claude(siblingID, alpha, 170*time.Minute)
		sub.prefix = agent[:8]
		sub.extra = map[string]any{"isSidechain": true, "agentId": agent}
		sub.user("-", "check the parser")
		sub.assistant("", "end_turn", text("parser checked"))
		age := 150 * time.Minute
		if cwd == beta {
			age = time.Minute
		}
		sub.save(filepath.Join(dir, "agent-"+agent+".jsonl"), age)
	}
	for _, args := range [][]string{{"claude", "all", "--list"}, {"claude", "1h", "--list"}} {
		out, _ := w.run("/", args...)
		if !strings.Contains(out, "+1 subagent") {
			t.Errorf("%v: want one subagent:\n%s", args, out)
		}
	}
	out, _ := w.run("/", "6b0e0000", "--tree")
	if strings.Count(out, agent[:8]) != 1 {
		t.Errorf("--tree shows the copied subagent more than once:\n%s", out)
	}
}

func TestRepeatedRecordsStayAddressableButLeaveCounts(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/repeat", time.Hour)
	l.user("-", "original prompt")
	l.assistant("", "end_turn", text("original answer"))
	l.records = append(l.records, l.records...)
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	out, code := w.run("/", "6b0e0000", "--kind", "user,assistant", "--json", "--full")
	if code != 0 || !strings.Contains(out, `"total_events":2`) || !strings.Contains(out, "2 repeated records hidden") {
		t.Fatal(out)
	}
	for _, args := range [][]string{{"6b0e0000#4"}, {"6b0e0000#3-", "--json", "--full"}} {
		out, code = w.run("/", args...)
		if code != 0 || !strings.Contains(out, "original answer") {
			t.Fatal(out)
		}
		if args[0] == "6b0e0000#4" && !strings.Contains(out, "repeat of #2") {
			t.Fatal(out)
		}
		if len(args) > 1 && !strings.Contains(out, `"repeat_of":2`) {
			t.Fatal(out)
		}
	}
	out, _ = w.run("/", "6b0e0000")
	if !strings.Contains(out, "events    2:") {
		t.Fatal(out)
	}
}
