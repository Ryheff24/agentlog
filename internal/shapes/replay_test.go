package shapes_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"agentlog/internal/catalog"
	"agentlog/internal/claude"
	"agentlog/internal/codex"
	"agentlog/internal/copilot"
	"agentlog/internal/shapes"
	"agentlog/internal/transcript"
)

var (
	harvest = flag.Bool("harvest", false, "read the real logs and add any new record shapes to testdata")
	update  = flag.Bool("update", false, "rewrite the replay golden")
	files   = flag.Int("harvest-files", 120, "newest log files per harness to harvest from")
)

// maxRecord skips the rare enormous record. Its shape is almost always the
// shape of a small one with a long string in it.
const maxRecord = 256 << 10

const fakeID = "3f2a91bc-1111-4222-8333-444455556666"

func providers() []catalog.Provider {
	return []catalog.Provider{claude.New(), codex.New(), copilot.New()}
}

func testdata(provider string) string {
	return filepath.Join("testdata", "shapes", provider+".jsonl")
}

// TestHarvest is the dev command, not a test: it reads the logs on this
// machine, redacts every record it keeps, and adds the shapes it has not seen
// before to testdata. Run it after a provider updates:
//
//	go test ./internal/shapes -harvest -v
//
// then read the diff before committing it. Nothing is ever removed, so a
// shape the providers have stopped writing is still one the readers are held
// to.
func TestHarvest(t *testing.T) {
	if !*harvest {
		t.Skip("reads real logs; run with -harvest")
	}
	for _, p := range providers() {
		h := shapes.Harvest{Provider: p.Name(), Roots: p.Roots(), Files: *files, Max: maxRecord}
		found, err := h.Run()
		if err != nil {
			t.Fatalf("%s: %v", p.Name(), err)
		}
		path := testdata(p.Name())
		kept, err := shapes.Read(path)
		if err != nil {
			t.Fatalf("%s: %v", p.Name(), err)
		}
		all := shapes.Merge(kept, found)
		if err := shapes.Write(path, all); err != nil {
			t.Fatalf("%s: %v", p.Name(), err)
		}
		t.Logf("%-8s %d shapes found, %d known, %d new", p.Name(), len(found), len(kept), len(all)-len(kept))
	}
	t.Log("harvested; now run: go test ./internal/shapes -update")
}

// Every shape ever seen, fed back through the reader that has to read it.
//
// This is the regression that drift trips. The fixtures are real records with
// the words taken out, so when a provider moves a field and a reader stops
// understanding it, the line for that shape changes — from an event to an
// unread record, or from a tool call to something less — and the golden says
// so, in a diff, without anyone going back to the raw logs to find out.
func TestReadersReadEveryShapeEverSeen(t *testing.T) {
	var b strings.Builder
	total := 0
	for _, p := range providers() {
		ss, err := shapes.Read(testdata(p.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ss {
			total++
			line := outcome(t, s)
			if unreadable(line) {
				t.Errorf("%s: %s\n  %s", s.Key, line, s.Record)
			}
			fmt.Fprintf(&b, "%-64s %s\n", s.Key, line)
		}
	}
	if total == 0 {
		t.Skip("no shapes harvested yet; run with -harvest")
	}

	golden := filepath.Join("testdata", "replay.txt")
	if *update {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d shapes)", golden, total)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/shapes -update)", err)
	}
	if diff := firstDiff(string(want), b.String()); diff != "" {
		t.Errorf("a reader reads a known record shape differently than it did:\n%s\n\nif the change is intended: go test ./internal/shapes -update", diff)
	}
}

// unreadable is an outcome a reader has to be changed for: a record lost,
// or one it had no case for. Accepting it into the golden would bless it.
func unreadable(outcome string) bool {
	return strings.HasPrefix(outcome, "DROPPED") || strings.HasPrefix(outcome, "NOT READ") ||
		!strings.Contains(outcome, "unread=0 ")
}

// The harnesses change their logs without notice, often daily. This reads
// the newest logs on this machine, the way the harvest does, and fails on
// any record shape the readers cannot read, so the first test run after a
// harness update names what changed. It writes nothing; to keep the shapes
// as fixtures once a reader handles them:
//
//	go test ./internal/shapes -harvest && go test ./internal/shapes -update
func TestTodaysLogsAreRead(t *testing.T) {
	if testing.Short() {
		t.Skip("reads real logs")
	}
	checked := 0
	for _, p := range providers() {
		h := shapes.Harvest{Provider: p.Name(), Roots: p.Roots(), Files: 40, Max: maxRecord}
		found, err := h.Run()
		if err != nil {
			continue // no logs from this harness here
		}
		for _, s := range found {
			checked++
			if line := outcome(t, s); unreadable(line) {
				t.Errorf("%s (harness %s): %s\n  %s", s.Key, s.Version, line, s.Record)
			}
		}
	}
	if checked == 0 {
		t.Skip("no harness logs on this machine")
	}
}

// outcome is what one record became: the events it produced, and whether the
// reader had a case for it at all.
func outcome(t *testing.T, s shapes.Shape) string {
	t.Helper()
	tr := load(t, s)
	if tr == nil {
		return "NOT READ AT ALL (the reader returned an error)"
	}
	var parts []string
	for _, e := range tr.Events {
		k := string(e.Kind)
		if e.Tool != "" {
			k += "(" + e.Tool + ")"
		}
		parts = append(parts, k)
	}
	unread, ignored := 0, 0
	for _, n := range tr.Unknowns {
		unread += n
	}
	for _, n := range tr.Ignored {
		ignored += n
	}
	kinds := strings.Join(parts, " ")
	if kinds == "" {
		kinds = "-"
	}
	if meta := session(tr); meta != "" {
		kinds = "sets " + meta
	}
	// Nothing at all is the one outcome that is always wrong: not an event,
	// not something about the session, not a record held back as a subagent's,
	// not a type the reader knows it cannot read or means to pass over. A
	// record that lands here has been lost.
	if len(tr.Events)+unread+ignored+tr.Hidden == 0 && kinds == "-" {
		return "DROPPED — nothing came back, and nothing said why"
	}
	return fmt.Sprintf("events=%-2d unread=%-2d ignored=%-2d hidden=%-2d %s",
		len(tr.Events), unread, ignored, tr.Hidden, kinds)
}

// session lists what a record told the reader about the session itself. A
// record that produces no event but fills in the model, or the version, or
// where the work happened, has been read, not lost.
func session(tr *transcript.Transcript) string {
	var got []string
	for _, f := range []struct {
		name  string
		value string
	}{
		{"cwd", tr.Session.CWD}, {"title", tr.Session.Title}, {"model", tr.Session.Model},
		{"branch", tr.Session.Branch}, {"commit", tr.Session.Commit}, {"origin", tr.Session.Origin},
		{"version", tr.Session.Version}, {"agent", tr.Session.AgentName + tr.Session.AgentRole},
		{"ending", tr.Session.Ending},
	} {
		if f.value != "" {
			got = append(got, f.name)
		}
	}
	if tr.Usage.Known() {
		got = append(got, "usage")
	}
	for _, n := range tr.Notes {
		// The hidden-branch note repeats the hidden= column.
		if !strings.Contains(n, "hidden") {
			got = append(got, "note")
			break
		}
	}
	return strings.Join(got, "+")
}

// load replays one record through its provider's reader. A panic here is the
// failure this whole file exists to catch, so it is caught and reported
// against the shape that caused it rather than taking the run down.
func load(t *testing.T, s shapes.Shape) (tr *transcript.Transcript) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: the reader panicked on a record it has seen before: %v", s.Key, r)
			tr = nil
		}
	}()
	dir := t.TempDir()
	o := transcript.Load{Branches: true}
	switch s.Provider {
	case transcript.Claude:
		path := write(t, filepath.Join(dir, "projects", "-Users-me-project", fakeID+".jsonl"), s.Record)
		tr, _ = (&claude.Provider{Home: dir}).Load(&transcript.Session{Provider: s.Provider, ID: fakeID, Files: []string{path}}, o)
	case transcript.Codex:
		name := "rollout-2026-09-15T14-00-00-" + fakeID + ".jsonl"
		path := write(t, filepath.Join(dir, "sessions", "2026", "09", "15", name), preamble(s.Record), s.Record)
		tr, _ = (&codex.Provider{Home: dir}).Load(&transcript.Session{Provider: s.Provider, ID: fakeID, Files: []string{path}}, o)
	case transcript.Copilot:
		path := write(t, filepath.Join(dir, "session-state", fakeID, "events.jsonl"), s.Record)
		tr, _ = (&copilot.Provider{Home: dir}).Load(&transcript.Session{Provider: s.Provider, ID: fakeID, Files: []string{path}}, o)
	default:
		t.Fatalf("no reader for %q", s.Provider)
	}
	return tr
}

// preamble is the session header Codex writes first and its reader expects.
// A harvested session_meta stands in for it; anything else needs one.
func preamble(record []byte) []byte {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(record, &head) == nil && head.Type == "session_meta" {
		return nil
	}
	return []byte(`{"timestamp":"2026-09-15T14:00:00.000Z","type":"session_meta","payload":` +
		`{"id":"` + fakeID + `","timestamp":"2026-09-15T14:00:00.000Z","cwd":"/Users/me/project","originator":"codex_cli_rs"}}`)
}

func write(t *testing.T, path string, lines ...[]byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b []byte
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		b = append(append(b, l...), '\n')
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// firstDiff reports the first line that differs, with its neighbours, because
// a whole-file diff of several hundred shapes says less than three lines do.
// firstDiff compares two replays shape by shape, since a harvest inserts
// new shapes anywhere: what is new, what is gone, and what reads differently.
func firstDiff(want, got string) string {
	byKey := func(text string) map[string]string {
		m := map[string]string{}
		for _, line := range strings.Split(text, "\n") {
			if k, _, ok := strings.Cut(line, " "); ok {
				m[k] = line
			}
		}
		return m
	}
	w, g := byKey(want), byKey(got)
	var out []string
	for k, line := range w {
		switch other, ok := g[k]; {
		case !ok:
			out = append(out, "- "+line+"  (shape no longer harvested)")
		case other != line:
			out = append(out, "- "+line, "+ "+other)
		}
	}
	for k, line := range g {
		if _, ok := w[k]; !ok {
			out = append(out, "+ "+line+"  (new shape)")
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i][2:] < out[j][2:] })
	return strings.Join(out, "\n")
}

func at(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "(nothing)"
}
