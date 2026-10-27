package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// noAgent clears the variables that mark a run as an agent's, including
// the ones the harness running these tests set.
func noAgent(t *testing.T) {
	for _, v := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "COPILOT_SESSION_ID"} {
		t.Setenv(v, "")
	}
}

func TestFeedbackAddAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	t.Setenv("AGENTLOG_FEEDBACK_FILE", path)
	noAgent(t)
	var out bytes.Buffer
	env := Env{Catalog: &catalog.Catalog{CWD: "/work/project"}, Stdout: &out, Stderr: &out}

	if err := feedback(env, []string{"add", "search", "was", "easy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("feedback file: %v", err)
	}
	out.Reset()
	if err := feedback(env, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no agent session", "search was easy", "/work/project"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q does not contain %q", out.String(), want)
		}
	}
}

// A harness whose session variable agentlog does not know is still
// sandboxed; the refused write is the sign, and the note is in its log.
func TestFeedbackAddWhereTheStoreIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTLOG_FEEDBACK_FILE", filepath.Join(dir, "agentlog", "feedback.jsonl"))
	noAgent(t)
	var out bytes.Buffer
	if err := feedback(Env{Catalog: &catalog.Catalog{}, Stdout: &out}, []string{"add", "note"}); err != nil {
		t.Fatalf("a refused store failed the note: %v", err)
	}
	if !strings.Contains(out.String(), "feedback noted") {
		t.Errorf("output %q does not say the note was taken", out.String())
	}
}

func TestFeedbackRequiresAMessage(t *testing.T) {
	var out bytes.Buffer
	if err := feedback(Env{Stdout: &out}, []string{"add"}); err == nil {
		t.Fatal("empty feedback was accepted")
	}
}

// Inside an agent session nothing is written: the note is already in the
// session's log, and a write is what sandboxes refuse.
func TestFeedbackAddInAgentSessionWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feedback.jsonl")
	t.Setenv("AGENTLOG_FEEDBACK_FILE", path)
	noAgent(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "6c2e9f40-1d7a-4b3e-8a55-2f9e0d4c7b18")
	var out bytes.Buffer
	if err := feedback(Env{Catalog: &catalog.Catalog{}, Stdout: &out}, []string{"add", "fine"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("feedback store was written: %v", err)
	}
	if !strings.Contains(out.String(), "feedback noted") {
		t.Errorf("output %q does not acknowledge the note", out.String())
	}
}

func TestFeedbackInReadsShellCalls(t *testing.T) {
	at := time.Date(2026, 9, 24, 20, 31, 0, 0, time.UTC)
	tr := &transcript.Transcript{
		Session: transcript.Session{Provider: "claude", ID: "6c2e9f40-1d7a-4b3e-8a55-2f9e0d4c7b18", CWD: "/work/dashboard"},
		Events: []transcript.Event{
			{Index: 3, Kind: transcript.KindTool, Tool: "Bash", Input: `agentlog feedback add "refs were clear" 2>&1 | tail -1`, Time: at},
			{Index: 4, Kind: transcript.KindTool, Tool: "Bash", Input: `grep -rn "agentlog feedback add" .`, Time: at},
			{Index: 5, Kind: transcript.KindTool, Tool: "Write", Input: `agentlog feedback add "in a file"`, Time: at},
			{Index: 6, Kind: transcript.KindAssistant, Text: `run agentlog feedback add "x"`, Time: at},
		},
	}
	got := feedbackIn(tr)
	if len(got) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(got), got)
	}
	if got[0].Message != "refs were clear" || !strings.HasSuffix(got[0].Ref, "#3") || got[0].Provider != "claude" {
		t.Errorf("unexpected note %+v", got[0])
	}
}

func TestMergeFeedbackKeepsTheLogCopy(t *testing.T) {
	early, late := time.Unix(100, 0), time.Unix(200, 0)
	logged := []feedbackEntry{
		{Time: late, SessionID: "s1", Message: "same", Ref: "s1#9"},
		{Time: late.Add(time.Minute), SessionID: "s1", Message: "same", Ref: "s1#12"}, // retried
		{Time: late, SessionID: "s2", Message: "same", Ref: "s2#9"},                   // resumed copy
	}
	stored := []feedbackEntry{
		{Time: late, SessionID: "s1", Message: "same"},
		{Time: early, Message: "from a terminal"},
	}
	got := mergeFeedback(stored, logged)
	if len(got) != 2 || got[0].Message != "from a terminal" || got[1].Ref != "s1#9" {
		t.Errorf("unexpected merge %+v", got)
	}
}

// withStdin runs f with text on standard input, the way a shell pipe hands
// it to `agentlog feedback add -`.
func withStdin(t *testing.T, text string, f func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(text); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin, _ = old, r.Close() }()
	f()
}

// The feedback command as agents and people use it, end to end.
func TestFeedbackCommand(t *testing.T) {
	t.Run("a note piped in is saved from outside any agent", func(t *testing.T) {
		w := newWorld(t)
		var out string
		var code int
		withStdin(t, "  the ref syntax took a while to find\n", func() { out, code = w.run("/work/shop", "feedback", "add", "-") })
		if code != 0 || !strings.Contains(out, "feedback saved") {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		out, _ = w.run("/work/shop", "feedback")
		if !strings.Contains(out, "no agent session\nthe ref syntax took a while to find\ncwd: /work/shop") {
			t.Errorf("piped note not listed as saved:\n%s", out)
		}
	})
	t.Run("nothing piped in is refused", func(t *testing.T) {
		w := newWorld(t)
		var out string
		var code int
		withStdin(t, " \n", func() { out, code = w.run("/work/shop", "feedback", "add", "-") })
		if code != 1 || !strings.Contains(out, "usage: agentlog feedback add MESSAGE") {
			t.Errorf("exit %d:\n%s", code, out)
		}
	})
	t.Run("an unknown subcommand prints the usage", func(t *testing.T) {
		w := newWorld(t)
		out, code := w.run("/work/shop", "feedback", "show")
		if code != 1 || !strings.Contains(out, "usage: agentlog feedback [list [--json] | add MESSAGE]") {
			t.Errorf("exit %d:\n%s", code, out)
		}
	})
	t.Run("--json and list --json hold every note", func(t *testing.T) {
		w := newWorld(t)
		w.run("/work/shop", "feedback", "add", "from a terminal")
		for _, args := range [][]string{{"feedback", "--json"}, {"feedback", "list", "--json"}} {
			out, code := w.run("/work/shop", args...)
			var notes []feedbackEntry
			if err := json.Unmarshal([]byte(out), &notes); code != 0 || err != nil {
				t.Fatalf("%v: exit %d, %v:\n%s", args, code, err, out)
			}
			if len(notes) != 2 || notes[0].Ref != "d4af0000#3" || notes[1].Message != "from a terminal" {
				t.Errorf("%v: %+v", args, notes)
			}
		}
	})
	t.Run("--json with no notes is an empty list", func(t *testing.T) {
		w := newWorld(t)
		os.Remove(filepath.Join(w.claudeProject("/work/blog"), launchID+".jsonl"))
		out, _ := w.run("/work/shop", "feedback", "--json")
		if strings.TrimSpace(out) != "[]" {
			t.Errorf("got %s", out)
		}
	})
	// A line cut short by a write that did not finish, or a hand edit, used
	// to fail the whole listing, taking every other note with it.
	t.Run("an unreadable line in the store is left out and said", func(t *testing.T) {
		w := newWorld(t)
		store := filepath.Join(w.root, "feedback.jsonl")
		w.write(store, time.Hour, `{"time":"2026-09-20T10:00:00Z","message":"kept note"}`+"\n"+
			`{"time":"2026-09-21T10:00:00Z","mess`+"\n\n"+
			`{"time":"2026-09-22T10:00:00Z","message":"after the bad line","session_id":"s1","provider":"claude"}`+"\n")
		out, code := w.run("/work/shop", "feedback")
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		for _, s := range []string{"kept note", "claude s1\nafter the bad line", "a word search should say", "3 notes: 1 from agents' logs (searched ", "), 2 from " + store + "\n", "left out 1 line of " + store} {
			if !strings.Contains(out, s) {
				t.Errorf("missing %q:\n%s", s, out)
			}
		}
	})
	t.Run("a store that cannot be opened fails the listing", func(t *testing.T) {
		w := newWorld(t)
		t.Setenv("AGENTLOG_FEEDBACK_FILE", w.root) // a directory
		out, code := w.run("/work/shop", "feedback")
		if code != 1 || !strings.Contains(out, "read feedback") {
			t.Errorf("exit %d:\n%s", code, out)
		}
	})
	// An agent adds a note and then lists the notes to check it landed. Its
	// own session is hidden from lookups, but its notes are not.
	t.Run("the calling agent's own notes are listed", func(t *testing.T) {
		w := newWorld(t)
		t.Setenv("CLAUDE_CODE_SESSION_ID", launchID)
		out, _ := w.run("/work/blog", "feedback")
		if !strings.Contains(out, "claude d4af0000#3\na word search should say when it matched titles only") {
			t.Errorf("own note missing:\n%s", out)
		}
	})
	// With no AGENTLOG_FEEDBACK_FILE the store is in the user's config
	// directory.
	t.Run("the default store", func(t *testing.T) {
		w := newWorld(t)
		t.Setenv("AGENTLOG_FEEDBACK_FILE", "")
		t.Setenv("HOME", w.root)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(w.root, ".config"))
		out, code := w.run("/work/shop", "feedback", "add", "default store")
		dir, _ := os.UserConfigDir()
		want := filepath.Join(dir, "agentlog", "feedback.jsonl")
		if code != 0 || !strings.Contains(out, "feedback saved: "+want) || !strings.HasPrefix(want, w.root) {
			t.Errorf("exit %d, want %s:\n%s", code, want, out)
		}
		t.Setenv("HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		out, code = w.run("/work/shop", "feedback")
		if code != 1 || !strings.Contains(out, "find user config directory") {
			t.Errorf("no home: exit %d:\n%s", code, out)
		}
	})
}
