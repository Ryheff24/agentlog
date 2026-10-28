package codex

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
	"github.com/Ryheff24/agentlog/internal/zstd/zstdtest"
)

// compress replaces a rollout with its .zst, as Codex does to a rollout about
// a week old, keeping its modification time.
func compress(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	zst := path + ".zst"
	if err := os.WriteFile(zst, zstdtest.Frame(b), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(zst, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return zst
}

func discover(t *testing.T, home string, q transcript.Query) map[string]*transcript.Session {
	t.Helper()
	ss, err := (&Provider{Home: home}).Discover(q)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*transcript.Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	return byID
}

func thread(t *testing.T, home string) (first, next string) {
	t.Helper()
	first = writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
		meta(threadID, map[string]any{"git": map[string]any{"branch": "main"}}),
		prompt(1, "port the parser"),
	})
	// The next page continues the first, taking over after all of it.
	m := meta(threadID, map[string]any{"history_base": map[string]any{"thread_id": threadID, "end_ordinal_exclusive": 100}})
	m.payload.(map[string]any)["timestamp"] = t0.Add(time.Minute)
	next = writeRollout(t, home, "rollout-2026-09-15T14-01-00-"+threadID+"_"+pageID+".jsonl", []rec{m, prompt(61, "now the tests")})
	return first, next
}

// Most of a Codex user's history is compressed: a thread whose pages are
// .jsonl.zst is found, opened and read like any other, and an old compressed
// page of a thread still running is part of it.
func TestCompressedRolloutsAreRead(t *testing.T) {
	plainHome, home := t.TempDir(), t.TempDir()
	thread(t, plainHome)
	first, next := thread(t, home)
	zstFirst := compress(t, first)

	want := discover(t, plainHome, transcript.Query{})[threadID]
	s := discover(t, home, transcript.Query{})[threadID]
	if s == nil || len(s.Files) != 2 || s.Files[0] != zstFirst || s.Files[1] != next || s.Branch != "main" || !s.Started.Equal(want.Started) {
		t.Fatalf("session = %+v, want both pages, the compressed one first", s)
	}
	tr := load(t, threadID, s.Files...)
	if got, wantTr := kinds(tr), kinds(load(t, threadID, want.Files...)); got != wantTr || got != "user user" {
		t.Errorf("events = %q, want %q", got, wantTr)
	}

	// Both pages compressed, and the thread is old: still found when the
	// window reaches back to it, and still read whole.
	zstNext := compress(t, next)
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, f := range []string{zstFirst, zstNext} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if got := discover(t, home, transcript.Query{Since: time.Now().Add(-7 * 24 * time.Hour)}); len(got) != 0 {
		t.Errorf("a thread last written a month ago is in a week's window: %v", got)
	}
	s = discover(t, home, transcript.Query{})[threadID]
	if s == nil || len(s.Files) != 2 {
		t.Fatalf("old compressed thread = %+v", s)
	}
	if got := kinds(load(t, threadID, s.Files...)); got != "user user" {
		t.Errorf("events = %q", got)
	}
}

// While Codex compresses a page, both copies exist for a moment. The page is
// read once, from the plain copy Codex was writing.
func TestPageInBothFormsIsReadOnce(t *testing.T) {
	home := t.TempDir()
	first, next := thread(t, home)
	b, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first+".zst", zstdtest.Frame(b), 0o644); err != nil {
		t.Fatal(err)
	}
	s := discover(t, home, transcript.Query{})[threadID]
	if s == nil || strings.Join(s.Files, " ") != first+" "+next {
		t.Fatalf("files = %v, want the plain copy of each page once", s)
	}
	if got := kinds(load(t, threadID, s.Files...)); got != "user user" {
		t.Errorf("events = %q, want each prompt once", got)
	}
}

// A compressed rollout that cannot be decompressed is not dropped from sight:
// the thread is still listed, from its file name, and reading it says what
// failed. A damaged page of a thread with others costs only that page, and
// the reader is told.
func TestDamagedCompressedRolloutIsReported(t *testing.T) {
	home := t.TempDir()
	first, next := thread(t, home)
	zstFirst := compress(t, first)
	if err := os.WriteFile(zstFirst, []byte("not zstd"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := discover(t, home, transcript.Query{})[threadID]
	if s == nil || len(s.Files) != 2 {
		t.Fatalf("session = %+v, want the thread with both pages", s)
	}
	tr := load(t, threadID, s.Files...)
	if kinds(tr) != "user" || tr.Unknowns["unreadable-page"] != 1 || len(tr.Notes) == 0 || !strings.Contains(strings.Join(tr.Notes, " "), zstFirst) {
		t.Errorf("events %q, unknowns %v, notes %q; want the readable page, and the damaged one named", kinds(tr), tr.Unknowns, tr.Notes)
	}

	// The only page is damaged: listed under its own id and start, and
	// opening it is an error that names the file.
	if err := os.Remove(next); err != nil {
		t.Fatal(err)
	}
	s = discover(t, home, transcript.Query{})[threadID]
	if s == nil || s.ID != threadID || s.Started.IsZero() || s.Started.Format("15:04") != "14:00" {
		t.Fatalf("session = %+v, want the thread listed from its file name", s)
	}
	_, err := (&Provider{}).Load(s, transcript.Load{})
	if err == nil || !strings.Contains(err.Error(), zstFirst) {
		t.Errorf("Load() error = %v, want one naming %s", err, zstFirst)
	}
}

func TestStartFromName(t *testing.T) {
	fallback := time.Unix(1, 0)
	for name, want := range map[string]string{
		"rollout-2026-09-15T14-00-00-" + threadID + ".jsonl.zst": "2026-09-15 14:00:00",
		"rollout-short.jsonl":                 "fallback",
		"rollout-2026-13-45T99-00-00-x.jsonl": "fallback",
		"other.jsonl":                         "fallback",
	} {
		got := startFromName(name, fallback)
		if want == "fallback" && !got.Equal(fallback) || want != "fallback" && got.Format(time.DateTime) != want {
			t.Errorf("startFromName(%q) = %v, want %s", name, got, want)
		}
	}
}
