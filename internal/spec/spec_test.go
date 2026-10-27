package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTypes points the user's types.json at body, read afresh.
func withTypes(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "types.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AGENTLOG_CONFIG", dir)
}

func TestOfAddsWhatThePersonDeclared(t *testing.T) {
	Register(Table{Provider: "test", Records: []Record{{Type: "known", Role: Step}}})
	withTypes(t, `{"test": [{"type": "session.warning", "role": "ignore", "note": "MCP warnings"},
		{"type": "known", "role": "ignore"}]}`)

	tab := Of("test")
	if len(tab.Records) != 2 {
		t.Fatalf("records %+v: a declared type that the reader knows must not be added twice", tab.Records)
	}
	got := tab.Records[1]
	if got.Type != "session.warning" || got.Role != Ignore || !strings.Contains(got.Note, "MCP warnings (from types.json)") {
		t.Errorf("declared record = %+v", got)
	}
	if !UserDeclared("test", "session.warning") || UserDeclared("test", "other") || UserDeclared("other", "session.warning") {
		t.Error("UserDeclared answers for the wrong types")
	}
}

// A missing or broken file is no file: the tables still answer.
func TestOfWithoutAUsableTypesFile(t *testing.T) {
	Register(Table{Provider: "test", Records: []Record{{Type: "known", Role: Step}}})
	for name, body := range map[string]string{"missing": "", "not json": "{nope", "wrong shape": `["a"]`} {
		t.Run(name, func(t *testing.T) {
			withTypes(t, body)
			if tab := Of("test"); len(tab.Records) != 1 || !tab.Knows("known") || tab.Knows("session.warning") {
				t.Errorf("records %+v", tab.Records)
			}
		})
	}
}
