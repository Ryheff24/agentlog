package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestARunIsRecordedWithItsCallerAndPhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("AGENTLOG_RUNS", path)
	t.Setenv("AGENTLOG_NO_MONITOR", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "01a0")
	t.Setenv("COPILOT_SESSION_ID", "")

	Phase("before")() // no run yet: ignored
	Start([]string{"3f2a", "--tail", "5"}, "/work")
	Phase("load")()
	Phase("load")()
	Finish(0, 1234, 5)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Harness != "codex" || r.Session != "01a0" || r.Out != 1234 || r.Err != 5 || r.CWD != "/work" || len(r.Args) != 3 {
		t.Errorf("run = %+v", r)
	}
	if _, ok := r.Phases["load"]; !ok || len(r.Phases) != 1 {
		t.Errorf("phases = %v, want load only", r.Phases)
	}
	// Finished, a second Finish writes nothing.
	Finish(0, 1, 1)
	if b2, _ := os.ReadFile(path); len(b2) != len(b) {
		t.Error("a finished run was written twice")
	}
}

// The first place that takes the write gets it; a person can turn it off.
func TestWhereRunsGo(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A path under a file cannot be created.
	if write(filepath.Join(blocked, "runs.jsonl"), []byte("x\n")) {
		t.Error("wrote under a file")
	}
	good := filepath.Join(dir, "d", "runs.jsonl")
	if !write(good, []byte("x\n")) {
		t.Error("could not write a fresh path")
	}

	t.Setenv("AGENTLOG_RUNS", "")
	t.Setenv("TMPDIR", dir)
	ps := Paths()
	if len(ps) < 2 || !strings.HasSuffix(ps[0], filepath.Join(".config", "agentlog", "runs.jsonl")) || !strings.HasPrefix(ps[1], dir) {
		t.Errorf("paths = %v, want the config dir, then the temp dir", ps)
	}

	t.Setenv("AGENTLOG_RUNS", good)
	t.Setenv("AGENTLOG_NO_MONITOR", "1")
	Start(nil, "")
	Finish(0, 0, 0)
	if b, _ := os.ReadFile(good); string(b) != "x\n" {
		t.Errorf("AGENTLOG_NO_MONITOR still wrote: %q", b)
	}
}

// A runs file is bounded: past the limit it moves aside, replacing the one
// before, and a new one starts.
func TestRunsFileRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	if err := os.WriteFile(path, make([]byte, maxBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	if !write(path, []byte("new\n")) {
		t.Fatal("write failed")
	}
	if b, _ := os.ReadFile(path); string(b) != "new\n" {
		t.Errorf("runs file = %d bytes, want a fresh one", len(b))
	}
	if st, err := os.Stat(path + ".1"); err != nil || st.Size() != maxBytes {
		t.Errorf("rotated file: %v", err)
	}
}
