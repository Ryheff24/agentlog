package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// tailFrom returns a TailBytes that cuts the log a few bytes before record
// index first begins, so the window opens on the end of the record before it,
// as a byte window almost always does.
func tailFrom(t *testing.T, path string, first int) int64 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	off := 0
	for i := 0; i < first; i++ {
		off += bytes.IndexByte(data[off:], '\n') + 1
	}
	return int64(len(data) - off + 5)
}

// Listings read only the last part of a log. That window starts wherever the
// byte count lands: partway through a record, after a call whose result is
// inside, after the parent of every record in it.
func TestTailWindowStartsMidConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		withUUID(user("first ask"), "u1", ""),
		withUUID(assistant(toolUse("t1", "Bash", map[string]any{"command": "go test ./..."})), "a1", "u1"),
		withUUID(toolResult("t1", "ok", nil), "r1", "a1"),
		withUUID(assistant(map[string]any{"type": "text", "text": "tests pass"}), "a2", "r1"),
		withUUID(user("second ask"), "u2", "a2"),
		withUUID(assistant(map[string]any{"type": "text", "text": "done"}), "a3", "u2"),
	})
	tail := transcript.Load{Window: transcript.Window{TailBytes: tailFrom(t, path, 2)}}
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, tail)
	if err != nil {
		t.Fatal(err)
	}
	// The cut record is skipped whole, not read as a broken one; the result
	// whose call is before the window is kept on its own; and nothing is
	// hidden as an abandoned branch just because its parent is out of view.
	if got, want := kinds(tr), "record assistant user assistant"; got != want {
		t.Fatalf("kinds = %q, want %q", got, want)
	}
	if tr.Unknowns["undecodable-record"] != 0 || tr.Hidden != 0 {
		t.Errorf("unknowns %v hidden %d", tr.Unknowns, tr.Hidden)
	}
	if tr.Events[0].Text != "ok" || tr.Unpaired != 1 {
		t.Errorf("orphan result = %+v", tr.Events[0])
	}
	if tr.Session.Ending != transcript.EndingDone {
		t.Errorf("Ending = %q", tr.Session.Ending)
	}
	if tr.Events[3].Line != 0 {
		t.Errorf("Line = %d; a tail window has no line numbers", tr.Events[3].Line)
	}
}

// A compaction the tail window happens to open on is one in the middle of the
// session, not the start of a continued one: the turns before it are in this
// same log, just outside the window.
func TestTailWindowOpeningOnACompactionIsNotAContinuation(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	boundary := withUUID(map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"}, "c1", "")
	boundary["logicalParentUuid"] = "a1"
	writeJSONL(t, path, []map[string]any{
		withUUID(user("first ask"), "u1", ""),
		withUUID(assistant(map[string]any{"type": "text", "text": "first answer"}), "a1", "u1"),
		boundary,
		withUUID(user("after compaction"), "u2", "c1"),
	})
	for _, tc := range []struct {
		name      string
		window    transcript.Window
		continues bool
	}{
		{"window opens on the compaction", transcript.Window{TailBytes: tailFrom(t, path, 2)}, false},
		{"window larger than the log", transcript.Window{TailBytes: 1 << 20}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{Window: tc.window})
			if err != nil {
				t.Fatal(err)
			}
			if tr.Session.Continues != tc.continues {
				t.Errorf("Continues = %v, want %v (events %q)", tr.Session.Continues, tc.continues, kinds(tr))
			}
		})
	}

	// And a log that really does open on a compaction is still a
	// continuation when the window covers all of it.
	path2 := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path2, []map[string]any{boundary, withUUID(user("after compaction"), "u2", "c1")})
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path2}},
		transcript.Load{Window: transcript.Window{TailBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Session.Continues {
		t.Error("a whole log opening on a compaction, read through a tail window, lost its continuation")
	}
}

// The head window is what a listing reads for the first prompt. A call near
// the end of it has its result beyond the window.
func TestHeadWindowEndsMidConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{
		{"type": "custom-title", "customTitle": "Parser audit"},
		user("audit the parser"),
		assistant(toolUse("t1", "Read", map[string]any{"file_path": "/w/parse.go"})),
		toolResult("t1", "package claude", nil),
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Up to the end of the call record, exactly.
	head := int64(bytes.Index(data, []byte(`"Read"`)))
	head += int64(bytes.IndexByte(data[head:], '\n')) + 1
	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}},
		transcript.Load{Window: transcript.Window{HeadBytes: head}})
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(tr); got != "user tool" {
		t.Fatalf("kinds = %q", got)
	}
	if !tr.Events[1].Pending || tr.Events[1].Line != 3 || tr.Session.Title != "Parser audit" {
		t.Errorf("call = %+v, title %q", tr.Events[1], tr.Session.Title)
	}
}

func TestLoadMissingLogIsAnError(t *testing.T) {
	_, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{filepath.Join(t.TempDir(), "gone.jsonl")}}, transcript.Load{})
	if err == nil {
		t.Fatal("Load of a log that is not there returned no error")
	}
}

// Only a log still being written after this session began can be the one it
// continues: an older log that happens to hold the same record id (a
// session resumed twice from one point) finished before this one existed.
func TestPredecessorIgnoresLogsFinishedBeforeThisSessionBegan(t *testing.T) {
	const stale = "bbbb0000-1111-4222-8333-444455556666"
	dir := t.TempDir()
	writeJSONL(t, filepath.Join(dir, stale+".jsonl"), []map[string]any{withUUID(user("start"), "a9", "")})
	started := t0.Add(24 * time.Hour)
	old := started.Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, stale+".jsonl"), old, old); err != nil {
		t.Fatal(err)
	}
	boundary := withUUID(map[string]any{"type": "system", "subtype": "compact_boundary", "content": "Conversation compacted"}, "c1", "")
	boundary["logicalParentUuid"] = "a9"
	path := filepath.Join(dir, sessionID+".jsonl")
	writeJSONL(t, path, []map[string]any{boundary, withUUID(user("carry on"), "u2", "c1")})

	tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}, Started: started}, transcript.Load{})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Session.Continues || tr.Session.ContinuedFrom != "" {
		t.Errorf("continues %v from %q, want a continuation whose source is not on disk", tr.Session.Continues, tr.Session.ContinuedFrom)
	}
}
