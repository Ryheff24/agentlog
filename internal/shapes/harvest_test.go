package shapes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// secrets are the words a harvest reads and must never write. Each fixture
// log below carries them somewhere a real log does: a prompt, a command, a
// file path, a map key, a tool result.
var secrets = []string{"hunter2", "acme-merger", "jdoe", "Which database should we drop"}

func logFile(t *testing.T, dir, name string, age time.Duration, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Now().Add(-age)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

// The harvest is run by hand on somebody's real logs and its output is
// committed. Whatever it read, the file it writes holds shapes and nothing
// anyone typed: not in values, not in paths, and not in map keys, which
// Claude's AskUserQuestion result uses for the question itself.
func TestHarvestWritesNoContent(t *testing.T) {
	root := t.TempDir()
	logFile(t, root, "proj/a.jsonl", time.Hour,
		`{"type":"user","version":"2.1.259","cwd":"/Users/jdoe/acme-merger","message":{"role":"user","content":"the password is hunter2"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"grep hunter2 /Users/jdoe/acme-merger/.env"}}]}}`,
		`{"type":"user","toolUseResult":{"answers":{"Which database should we drop?":"acme-merger prod"}},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"hunter2"}]}}`,
		`{"type":"file-history-snapshot","snapshot":{"trackedFileBackups":{"/Users/jdoe/acme-merger/plan.md":{"backupFileName":"hunter2@v1"}}}}`,
	)
	found, err := Harvest{Provider: "claude", Roots: []string{root}}.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 4 {
		t.Fatalf("harvested %d shapes, want 4", len(found))
	}
	out := filepath.Join(t.TempDir(), "nested", "claude.jsonl")
	if err := Write(out, found); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(string(b), s) {
			t.Errorf("written harvest contains %q:\n%s", s, b)
		}
	}
	// And the shape survives: the reader still has an answers map to read.
	if !strings.Contains(string(b), `"answers":{"key-`) || !strings.Contains(string(b), `"name":"Bash"`) {
		t.Errorf("harvest lost the structure a reader switches on:\n%s", b)
	}
}

// A shape keyed by a question is one shape however many questions are asked;
// otherwise every harvest adds a "new" shape per question, forever.
func TestContentKeysDoNotMakeNewShapes(t *testing.T) {
	a := Of("claude", "", []byte(`{"type":"user","toolUseResult":{"answers":{"Which database?":"pg"}}}`))
	b := Of("claude", "", []byte(`{"type":"user","toolUseResult":{"answers":{"Ship it today, or wait?":"wait"}}}`))
	c := Of("claude", "", []byte(`{"type":"user","toolUseResult":{"answers":{"db":"pg"}}}`))
	if a.Key != b.Key {
		t.Errorf("two questions gave two shapes: %s, %s", a.Key, b.Key)
	}
	if a.Key == c.Key {
		t.Errorf("a field name was taken for content: %s", c.Key)
	}
}

func TestContentKey(t *testing.T) {
	for k, want := range map[string]bool{
		"toolUseResult": false, "tool_use_id": false, "x-request-id": false, "README.md": false,
		"mcp__server__tool": false, "@type": false,
		"Which database?": true, "two words": true, "": false, "naïve": true,
		strings.Repeat("a", 65): true,
	} {
		if got := contentKey(k); got != want {
			t.Errorf("contentKey(%.20q) = %v, want %v", k, got, want)
		}
	}
}

// Merge never drops a shape, and an already recorded example is kept rather
// than replaced by a newer one of the same key, so re-running a harvest does
// not churn the fixtures.
func TestMergeKeepsEveryShapeAndTheFirstExample(t *testing.T) {
	kept := []Shape{{Key: "b", Record: []byte(`{"old":1}`)}, {Key: "d", Record: []byte(`{}`)}}
	found := []Shape{{Key: "b", Record: []byte(`{"new":1}`)}, {Key: "a"}, {Key: "c"}}
	got := Merge(kept, found)
	var keys []string
	for _, s := range got {
		keys = append(keys, s.Key)
	}
	if strings.Join(keys, " ") != "a b c d" {
		t.Errorf("keys = %v, want every shape, in key order", keys)
	}
	if string(got[1].Record) != `{"old":1}` {
		t.Errorf("b = %s, want the recorded example kept", got[1].Record)
	}
	if len(Merge(nil, nil)) != 0 {
		t.Error("merging nothing made something")
	}
}

// What Write records, Read returns: the fixtures are a round trip, and a
// harvest with no file yet is empty, not an error.
func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x", "codex.jsonl")
	if got, err := Read(path); err != nil || got != nil {
		t.Fatalf("Read(missing) = %v, %v, want an empty harvest", got, err)
	}
	in := []Shape{
		Of("codex", "0.153.4", []byte(`{"type":"session_meta","payload":{"cli_version":"0.153.4"}}`)),
		Of("codex", "", []byte(`{"type":"event_msg","payload":{"type":"agent_message","message":"hi"}}`)),
	}
	if err := Write(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("read %d shapes, wrote %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Key != in[i].Key || out[i].Version != in[i].Version || out[i].Type != in[i].Type || string(out[i].Record) != string(in[i].Record) {
			t.Errorf("shape %d = %+v, want %+v", i, out[i], in[i])
		}
	}
}

// A fixture file edited by hand can pick up blank lines, which are fine, or a
// broken line, which must fail loudly rather than drop shapes silently.
func TestReadRejectsABrokenFixture(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.jsonl")
	os.WriteFile(good, []byte("\n{\"key\":\"a\",\"record\":{}}\n\n  \n"), 0o644)
	if got, err := Read(good); err != nil || len(got) != 1 {
		t.Errorf("Read(blank lines) = %v, %v, want one shape", got, err)
	}
	bad := filepath.Join(dir, "bad.jsonl")
	os.WriteFile(bad, []byte("{\"key\":\"a\",\"record\":{}}\n{\"key\":\"b\",\n"), 0o644)
	if _, err := Read(bad); err == nil {
		t.Error("Read(broken line) = nil error")
	}
	if _, err := Read(dir); err == nil {
		t.Error("Read(a directory) = nil error")
	}
	// A path whose parent is a file cannot be written.
	if err := Write(filepath.Join(good, "x.jsonl"), nil); err == nil {
		t.Error("Write under a file = nil error")
	}
}

// The harvest reads the newest logs first and stops at Files; a record over
// Max is skipped (a 40MB tool result is not a shape worth a fixture), and so
// is a line cut off because the log is being written right now; a type
// with a free-form payload is capped at Per shapes; the version a file
// announces is carried to the records after it; and files that are not
// logs, or roots that do not exist, are passed over.
func TestHarvestRunLimits(t *testing.T) {
	root := t.TempDir()
	logFile(t, root, "new.jsonl", time.Minute,
		`{"type":"session_meta","payload":{"cli_version":"0.160.0"}}`,
		`{"type":"note","a":1}`, `{"type":"note","b":1}`, `{"type":"note","c":1}`,
		`{"type":"big","out":"`+strings.Repeat("x", 500)+`"}`,
		`{"type":"cut","par`,
	)
	logFile(t, root, "sub/old.jsonl", time.Hour, `{"type":"only-in-old"}`)
	logFile(t, root, "notes.txt", 0, `{"type":"not-a-log"}`)

	run := func(h Harvest) map[string]Shape {
		h.Provider, h.Roots = "codex", []string{root, filepath.Join(root, "missing")}
		got, err := h.Run()
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]Shape{}
		for _, s := range got {
			m[s.Type+"/"+s.Key] = s
		}
		return m
	}
	count := func(m map[string]Shape, typ string) int {
		n := 0
		for _, s := range m {
			if s.Type == typ {
				n++
			}
		}
		return n
	}

	all := run(Harvest{})
	if count(all, "only-in-old") != 1 || count(all, "not-a-log") != 0 || count(all, "note") != 3 || count(all, "big") != 1 || count(all, "cut") != 0 {
		t.Errorf("all = %v", keys(all))
	}
	for _, s := range all {
		if s.Type == "note" && s.Version != "0.160.0" {
			t.Errorf("note version = %q, want the one the file announced", s.Version)
		}
	}
	limited := run(Harvest{Files: 1, Max: 200, Per: 2})
	if count(limited, "only-in-old") != 0 || count(limited, "big") != 0 || count(limited, "note") != 2 {
		t.Errorf("limited = %v, want the newest file, no big record, two notes", keys(limited))
	}
}

func keys(m map[string]Shape) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
