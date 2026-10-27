package generic

import (
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

var at = time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)

func read(src string) transcript.Event { return Event("some_record", []byte(src), at, 7) }

// The case this package exists for: a provider renames a tool record, and the
// reader that had a case for the old name has none for the new one. What comes
// back has to be the tool call anyway.
func TestEventReadsAToolCallByShape(t *testing.T) {
	tests := []struct {
		name, src, tool, input, output string
	}{
		{
			"claude-ish",
			`{"tool_use_id":"call_1","name":"Bash","input":{"command":"go test ./..."},"result":{"stdout":"ok","is_error":false}}`,
			"Bash", "go test ./...", "ok",
		},
		{
			"codex-ish",
			`{"item":{"tool_name":"shell","invocation_id":"c9","arguments":{"cmd":"ls -l"},"aggregated_output":"a.go"}}`,
			"shell", "cmd: ls -l", "a.go",
		},
		{
			"renamed everything",
			`{"invocation":{"tool_name":"shell_v3","arguments":{"command":"make"},"result":{"stdout":"built"}}}`,
			"shell_v3", "make", "built",
		},
		{
			"output only",
			`{"function":"web_fetch","response":"200 OK"}`,
			"web_fetch", "", "200 OK",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := read(tt.src)
			if e.Kind != transcript.KindTool {
				t.Fatalf("kind = %q, want a tool call: %+v", e.Kind, e)
			}
			if e.Tool != tt.tool {
				t.Errorf("tool = %q, want %q", e.Tool, tt.tool)
			}
			if !strings.Contains(e.Input, tt.input) {
				t.Errorf("input = %q, want it to contain %q", e.Input, tt.input)
			}
			if !strings.Contains(e.Output, tt.output) {
				t.Errorf("output = %q, want it to contain %q", e.Output, tt.output)
			}
		})
	}
}

func TestEventNotesFailure(t *testing.T) {
	e := read(`{"name":"Bash","input":{"command":"false"},"output":"","exit_code":3}`)
	if !e.Failed || e.ExitCode == nil || *e.ExitCode != 3 {
		t.Errorf("event = %+v, want a failure with exit 3", e)
	}
	if ok := read(`{"name":"Bash","input":{"command":"true"},"output":"","exit_code":0}`); ok.Failed {
		t.Error("exit 0 was read as a failure")
	}
	if err := read(`{"name":"Bash","arguments":{"q":"x"},"output":"","is_error":true}`); !err.Failed {
		t.Error("is_error:true was not read as a failure")
	}
}

// Anything that is not recognisably a call still has to arrive whole.
func TestEventKeepsWhatItCannotClassify(t *testing.T) {
	e := read(`{"type":"sparkle_mode","sparkles":3,"detail":{"colour":"green","since":"tuesday"}}`)
	if e.Kind != transcript.KindRecord {
		t.Fatalf("kind = %q, want a record", e.Kind)
	}
	if e.Tag != "some_record" {
		t.Errorf("tag = %q, want the name the reader gave up on", e.Tag)
	}
	for _, want := range []string{"sparkles", "3", "colour", "green", "tuesday"} {
		if !strings.Contains(e.Text, want) {
			t.Errorf("text = %q, want it to contain %q", e.Text, want)
		}
	}
}

// Drift arrives as fields that were not there before, fields that have gone,
// and fields in another order. None of them may lose anything.
func TestEventSurvivesFieldsComingAndGoing(t *testing.T) {
	tests := []struct{ name, src string }{
		{"extra fields", `{"name":"Bash","input":{"command":"ls"},"output":"a","novel_field":{"deep":[1,2]},"another":true}`},
		{"reordered", `{"output":"a","novel_field":1,"input":{"command":"ls"},"name":"Bash"}`},
		{"nested deeper", `{"envelope":{"payload":{"item":{"name":"Bash","input":{"command":"ls"},"output":"a"}}}}`},
		{"arrays", `{"name":"Bash","input":[{"command":"ls"}],"output":["a"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event("x", []byte(tt.src), at, 1)
			if !strings.Contains(e.Input+e.Output+e.Text, "ls") {
				t.Errorf("event lost the command: %+v", e)
			}
			if !strings.Contains(e.Input+e.Output+e.Text, "a") {
				t.Errorf("event lost the output: %+v", e)
			}
		})
	}
}

func TestEventNeverPanics(t *testing.T) {
	for _, src := range []string{
		``, `{`, `{}`, `[]`, `null`, `"bare string"`, `{"a":`, `{"a":"unterminated`,
		`{"":""}`, `{"name":{"nested":"object where a string was"}}`,
		`{"exit_code":"not a number"}`, `{"timestamp":"not a time"}`,
		strings.Repeat(`{"a":`, 200) + "1" + strings.Repeat(`}`, 200),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Event(%.40q) panicked: %v", src, r)
				}
			}()
			Event("x", []byte(src), at, 1)
		}()
	}
}

// A record with no time of its own borrows the one the reader had; a record
// carrying one gets to keep it.
func TestEventTakesATimestampWhenItHasOne(t *testing.T) {
	e := Event("x", []byte(`{"created_at":"2026-09-19T12:30:00Z","note":"hi"}`), time.Time{}, 1)
	if got := e.Time.UTC().Format(time.RFC3339); got != "2026-09-19T12:30:00Z" {
		t.Errorf("time = %s, want the record's own", got)
	}
	if e := read(`{"note":"hi"}`); !e.Time.Equal(at) {
		t.Errorf("time = %v, want the reader's %v", e.Time, at)
	}
}
