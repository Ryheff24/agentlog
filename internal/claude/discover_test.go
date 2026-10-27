package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Running Claude Code processes each keep ~/.claude/sessions/<pid>.json with
// the session they hold and whether they are busy. A session is live only if
// that process is still running: the files outlive crashed processes.
func TestDiscoverMarksSessionsHeldByARunningBusyProcess(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "projects", "-work-shop")
	const busy, idle, crashed = "11111111-0000-4000-8000-000000000001", "11111111-0000-4000-8000-000000000002", "11111111-0000-4000-8000-000000000003"
	for _, id := range []string{busy, idle, crashed} {
		writeJSONL(t, filepath.Join(proj, id+".jsonl"), []map[string]any{user("hello")})
	}
	// A pid that has certainly exited: a child this test ran and waited for.
	done := exec.Command("true")
	if err := done.Run(); err != nil {
		t.Skip("cannot run a child process:", err)
	}
	dead := done.Process.Pid
	self := os.Getpid()

	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		strconv.Itoa(self) + ".json": `{"pid":1,"sessionId":"` + busy + `","status":"busy","kind":"interactive"}`,
		strconv.Itoa(dead) + ".json": `{"sessionId":"` + crashed + `","status":"busy"}`,
		// The same directory holds per-process key files and, after a crash
		// mid-write, half a record; neither is a session.
		strconv.Itoa(self) + ".abc123.key": `secret`,
		"not-a-pid.json":                   `{"sessionId":"` + idle + `","status":"busy"}`,
	} {
		if err := os.WriteFile(filepath.Join(sessions, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Files old enough that only the process table can make them live.
	old := time.Now().Add(-time.Hour)
	for _, id := range []string{busy, idle, crashed} {
		if err := os.Chtimes(filepath.Join(proj, id+".jsonl"), old, old); err != nil {
			t.Fatal(err)
		}
	}
	ss, err := (&Provider{Home: home}).Discover(transcript.Query{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	for _, s := range ss {
		live[s.ID] = s.Live
	}
	if len(live) != 3 || !live[busy] || live[idle] || live[crashed] {
		t.Errorf("live = %v, want only the session a running busy process holds", live)
	}
}

func TestLiveSessionsReadsOnlyRunningProcesses(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self := strconv.Itoa(os.Getpid())
	for _, tc := range []struct {
		name, body string
		want       map[string]bool
	}{
		{"waiting for input", `{"sessionId":"s1","status":"waiting"}`, map[string]bool{"s1": false}},
		{"truncated mid-write", `{"sessionId":"s1","sta`, map[string]bool{}},
		{"no session yet", `{"pid":1,"status":"busy"}`, map[string]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, self+".json"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got := (&Provider{Home: home}).liveSessions()
			if len(got) != len(tc.want) {
				t.Fatalf("liveSessions = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if b, ok := got[k]; !ok || b != v {
					t.Errorf("liveSessions = %v, want %v", got, tc.want)
				}
			}
		})
	}
	if got := (&Provider{Home: t.TempDir()}).liveSessions(); len(got) != 0 {
		t.Errorf("no sessions directory: %v", got)
	}
}

// What a listing shows before any log is opened in full comes from the
// directory layout and the first records.
func TestDiscoverReadsTheLogHead(t *testing.T) {
	home := t.TempDir()
	// The directory name encodes the cwd lossily; the log's own cwd wins.
	proj := filepath.Join(home, "projects", "-work-my-app")
	writeJSONL(t, filepath.Join(proj, sessionID+".jsonl"), []map[string]any{
		{"type": "user", "cwd": "/work/my-app", "gitBranch": "main", "entrypoint": "cli",
			"message": map[string]any{"role": "user", "content": "go"}},
		// A session renamed by hand has a custom title and, in the logs
		// seen so far, no generated one.
		{"type": "custom-title", "customTitle": "Named by hand"},
	})
	other := "22222222-0000-4000-8000-000000000001"
	writeJSONL(t, filepath.Join(proj, other+".jsonl"), []map[string]any{{"type": "ai-title", "aiTitle": "No cwd written"}})
	// Stray files beside the projects are not projects.
	if err := os.WriteFile(filepath.Join(home, "projects", ".DS_Store"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A session directory with no subagents in it, and a subagent log with
	// no meta file beside it.
	if err := os.MkdirAll(filepath.Join(proj, other, "tool-results"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, filepath.Join(proj, sessionID, "subagents", "agent-"+agentID+".jsonl"), []map[string]any{user("sub")})

	ss, err := (&Provider{Home: home}).Discover(transcript.Query{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*transcript.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	if len(ss) != 3 {
		t.Fatalf("sessions = %d, want 3", len(ss))
	}
	s := byID[sessionID]
	if s.CWD != "/work/my-app" || s.Branch != "main" || s.Origin != "cli" || s.Title != "Named by hand" || s.Started.IsZero() {
		t.Errorf("session = cwd %q branch %q origin %q title %q started %v", s.CWD, s.Branch, s.Origin, s.Title, s.Started)
	}
	if o := byID[other]; o.CWD != "/work/my/app" || o.Title != "No cwd written" {
		t.Errorf("fallback cwd = %q title %q, want the decoded directory name and the generated title", o.CWD, o.Title)
	}
	if sub := byID[agentID]; sub.ParentID != sessionID || sub.AgentName != "" || sub.CWD != "/work/shop" {
		t.Errorf("subagent without meta = %+v", sub)
	}

	// Asking for one id, or only for recent logs, narrows the scan.
	one, err := (&Provider{Home: home}).Discover(transcript.Query{ID: other})
	if err != nil || len(one) != 1 || one[0].ID != other {
		t.Errorf("Discover(ID) = %v, %v", one, err)
	}
	none, err := (&Provider{Home: home}).Discover(transcript.Query{Since: time.Now().Add(time.Hour)})
	if err != nil || len(none) != 0 {
		t.Errorf("Discover(Since future) = %d sessions, %v", len(none), err)
	}
}

// A first prompt with a pasted screenshot is larger than the head window, and
// the records before it carry no time. The start is still the prompt's time,
// not the zero time a preview printed as "Sun Dec 31 19:03".
func TestDiscoverFindsTheStartPastALargeFirstPrompt(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "projects", "-work-app")
	start := time.Date(2026, 9, 25, 16, 9, 52, 0, time.UTC)
	writeJSONL(t, filepath.Join(proj, sessionID+".jsonl"), []map[string]any{
		{"type": "mode", "mode": "normal", "timestamp": nil}, // bookkeeping carries no time
		{"type": "user", "cwd": "/work/app", "timestamp": start,
			"message": map[string]any{"role": "user", "content": strings.Repeat("i", 300<<10)}},
	})
	ss, err := (&Provider{Home: home}).Discover(transcript.Query{})
	if err != nil || len(ss) != 1 {
		t.Fatalf("Discover() = %v, %v", ss, err)
	}
	if !ss[0].Started.Equal(start) {
		t.Errorf("Started = %v, want %v", ss[0].Started, start)
	}
}

func TestDiscoverWithoutProjectsDirectory(t *testing.T) {
	ss, err := (&Provider{Home: t.TempDir()}).Discover(transcript.Query{})
	if err != nil || ss != nil {
		t.Errorf("Discover() = %v, %v; a machine without Claude Code has no sessions and no error", ss, err)
	}
	// A projects path that exists but cannot be listed is an error worth
	// reporting, not an empty machine.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "projects"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Provider{Home: home}).Discover(transcript.Query{}); err == nil {
		t.Error("Discover() over a file named projects returned no error")
	}
}

// A log moved between folders is one session: the newest part's identity,
// both files oldest first, and the facts only one part knows.
func TestMergeKeepsWhatEachPartKnows(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	home := &transcript.Session{ID: "s", CWD: "/home/user", Files: []string{"a.jsonl"}, Started: t0, Updated: t0.Add(3 * time.Hour), Size: 10, Branch: "main", Live: true}
	moved := &transcript.Session{ID: "s", CWD: "/home/user/proj", Files: []string{"b.jsonl"}, Started: t0.Add(time.Hour), Updated: t0.Add(2 * time.Hour), Size: 5, Title: "fix tunnel"}
	s := merge([]*transcript.Session{moved, home})
	if s.CWD != "/home/user/proj" || strings.Join(s.Files, ",") != "a.jsonl,b.jsonl" {
		t.Errorf("cwd %q files %v: want the newest folder and both files oldest first", s.CWD, s.Files)
	}
	if !s.Started.Equal(t0) || !s.Updated.Equal(t0.Add(3*time.Hour)) || s.Size != 15 {
		t.Errorf("started %v updated %v size %d: want the span and sum of both parts", s.Started, s.Updated, s.Size)
	}
	if s.Branch != "main" || s.Title != "fix tunnel" || !s.Live {
		t.Errorf("branch %q title %q live %v: a fact one part carries belongs to the session", s.Branch, s.Title, s.Live)
	}
	if home.Size != 10 || len(moved.Files) != 1 {
		t.Error("merge changed its inputs")
	}
}
