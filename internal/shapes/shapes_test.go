package shapes

import (
	"encoding/json"
	"strings"
	"testing"
)

// The one thing this package must never do is carry somebody's words into a
// file that gets committed.
func TestRedactKeepsShapeAndDropsContent(t *testing.T) {
	raw := `{
		"type": "user",
		"uuid": "3f2a91bc-1111-4222-8333-444455556666",
		"cwd": "/Users/someone/Documents/secret-startup/api",
		"message": {"role": "user", "content": "the staging password is hunter2, rotate it"},
		"toolUseResult": {"filePath": "/Users/someone/src/billing.go", "numLines": 42},
		"files": {"/Users/someone/src/billing.go": {"added": 3}},
		"model": "claude-opus-5",
		"version": "2.1.259"
	}`
	out := string(Redact([]byte(raw)))

	for _, gone := range []string{"hunter2", "rotate", "someone", "secret-startup", "billing", "Documents"} {
		if strings.Contains(out, gone) {
			t.Errorf("redacted record still contains %q:\n%s", gone, out)
		}
	}
	// What a reader branches on has to survive, or the fixture tests nothing.
	for _, kept := range []string{`"type":"user"`, `"role":"user"`, `"model":"claude-opus-5"`, `"version":"2.1.259"`, "numLines", "toolUseResult", "filePath", ".go"} {
		if !strings.Contains(out, kept) {
			t.Errorf("redacted record lost %q:\n%s", kept, out)
		}
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("redacted record is not JSON: %s", out)
	}
}

func TestRedactSurvivesAnything(t *testing.T) {
	for _, src := range []string{
		``, `{`, `{}`, `[]`, `null`, `"bare"`, `{"a":[[[1]]]}`, `{"":""}`,
		`{"a":` + strings.Repeat(`{"b":`, 200) + `1` + strings.Repeat(`}`, 200) + `}`,
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Redact(%.30q) panicked: %v", src, r)
				}
			}()
			if out := Redact([]byte(src)); !json.Valid(out) {
				t.Errorf("Redact(%.30q) = %q, which is not JSON", src, out)
			}
		}()
	}
}

// Two records are the same shape when they carry the same fields, whatever is
// in them — and different shapes the moment a provider adds one.
func TestOfDistinguishesFieldsNotValues(t *testing.T) {
	a := Of("claude", "2.1.259", []byte(`{"type":"assistant","message":{"role":"assistant","content":"one"}}`))
	b := Of("claude", "2.1.259", []byte(`{"type":"assistant","message":{"role":"assistant","content":"something else entirely"}}`))
	c := Of("claude", "2.1.259", []byte(`{"type":"assistant","message":{"role":"assistant","content":"one","thinking":"hm"}}`))
	if a.Key != b.Key {
		t.Errorf("same fields gave two keys:\n%s\n%s", a.Key, b.Key)
	}
	if a.Key == c.Key {
		t.Errorf("a new field did not make a new shape: %s", c.Key)
	}
	if a.Type != "assistant" {
		t.Errorf("type = %q, want assistant", a.Type)
	}
}

func TestOfNamesNestedTypesAndFindsTheVersion(t *testing.T) {
	s := Of("codex", "", []byte(`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"command_execution"}}}`))
	if s.Type != "event_msg:item_completed:command_execution" {
		t.Errorf("type = %q, want the nested names", s.Type)
	}
	meta := Of("codex", "", []byte(`{"type":"session_meta","payload":{"cli_version":"0.153.4","id":"x"}}`))
	if meta.Version != "0.153.4" {
		t.Errorf("version = %q, want the one in the record", meta.Version)
	}
	if got := Of("codex", "0.153.4", []byte(`{"type":"turn_context","payload":{"schema_version":"1"}}`)); got.Version != "0.153.4" {
		t.Errorf("version = %q, want the one the file already established", got.Version)
	}
}

// An array is capped, so one enormous record does not become one enormous
// fixture, and the shape of "a list of these" is kept.
func TestRedactCapsArrays(t *testing.T) {
	var items []string
	for range 50 {
		items = append(items, `{"type":"text","text":"a sentence someone wrote"}`)
	}
	out := Redact([]byte(`{"content":[` + strings.Join(items, ",") + `]}`))
	var got struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != maxItems {
		t.Errorf("kept %d items, want %d", len(got.Content), maxItems)
	}
}
