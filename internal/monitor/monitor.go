// Package monitor records what each agentlog run cost: how long it took, in
// which phases, and how much it printed into the caller's context. One JSON
// line per run goes to the first writable of a few places, since an agent's
// sandbox usually allows only its temp directory. Nothing here may fail or
// slow the command it measures; every error is dropped.
package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/Ryheff24/agentlog/internal/private"
)

// Run is one agentlog invocation.
type Run struct {
	Start   time.Time          `json:"t"`
	Ms      float64            `json:"ms"` // wall time, start to exit
	Args    []string           `json:"args"`
	CWD     string             `json:"cwd,omitempty"`
	Harness string             `json:"harness,omitempty"` // who called: claude, codex, copilot, or empty for a person
	Session string             `json:"session,omitempty"` // the calling session's id, when its harness says
	Exit    int                `json:"exit"`
	Out     int64              `json:"out"`              // bytes written to stdout: what the caller reads
	Err     int64              `json:"err"`              // bytes written to stderr
	Phases  map[string]float64 `json:"phases,omitempty"` // ms spent in each, summed over calls
}

var (
	mu  sync.Mutex
	cur *Run
)

// Start begins recording this process's run.
func Start(args []string, cwd string) {
	mu.Lock()
	defer mu.Unlock()
	cur = &Run{Start: time.Now(), Args: args, CWD: cwd, Phases: map[string]float64{}}
	for _, c := range []struct{ harness, env string }{
		{"claude", "CLAUDE_CODE_SESSION_ID"}, {"codex", "CODEX_THREAD_ID"}, {"copilot", "COPILOT_SESSION_ID"},
	} {
		if id := os.Getenv(c.env); id != "" {
			cur.Harness, cur.Session = c.harness, id
			break
		}
	}
}

// Phase times one step of the run; call the returned func when it ends.
// Steps that run more than once add up.
func Phase(name string) func() {
	t := time.Now()
	return func() {
		d := ms(time.Since(t))
		mu.Lock()
		if cur != nil {
			cur.Phases[name] += d
		}
		mu.Unlock()
	}
}

// Finish records the run's outcome and writes it out.
func Finish(exit int, out, err int64) {
	mu.Lock()
	r := cur
	cur = nil
	mu.Unlock()
	if r == nil || os.Getenv("AGENTLOG_NO_MONITOR") != "" {
		return
	}
	r.Ms, r.Exit, r.Out, r.Err = ms(time.Since(r.Start)), exit, out, err
	line, e := json.Marshal(r)
	if e != nil {
		return
	}
	line = append(line, '\n')
	for _, path := range Paths() {
		if write(path, line) {
			return
		}
	}
}

// maxBytes bounds a runs file; past it the file is moved to .1, replacing
// the one before, so at most twice this is kept.
const maxBytes = 8 << 20

func write(path string, line []byte) bool {
	if private.Dir(filepath.Dir(path)) != nil {
		return false
	}
	if st, err := os.Stat(path); err == nil && st.Size()+int64(len(line)) > maxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := private.Open(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return false
	}
	_, err = f.Write(line)
	return f.Close() == nil && err == nil
}

// Paths are where runs are written, in order of preference, and so where a
// reader looks: AGENTLOG_RUNS if set; the config directory, which a person's
// shell can write; the temp directory an agent's sandbox allows; and /tmp.
func Paths() []string {
	if p := os.Getenv("AGENTLOG_RUNS"); p != "" {
		return []string{p}
	}
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".config", "agentlog", "runs.jsonl"))
	}
	// Named so as not to collide with anything an agent builds there.
	dir := "agentlog-runs-" + strconv.Itoa(os.Getuid())
	if t := os.Getenv("TMPDIR"); t != "" {
		out = append(out, filepath.Join(t, dir, "runs.jsonl"))
	}
	out = append(out, filepath.Join(os.TempDir(), dir, "runs.jsonl"), filepath.Join("/tmp", dir, "runs.jsonl"))
	return dedupe(out)
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
