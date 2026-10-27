package jsonl

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestFieldsAgreesWithEncodingJSON is the property that matters: for any
// object encoding/json can decode into raw messages, Fields must return the
// same bytes for the same keys.
func TestFieldsAgreesWithEncodingJSON(t *testing.T) {
	objects := []string{
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"call_1"}}}`,
		`{"a":1,"b":-2.5e10,"c":true,"d":false,"e":null}`,
		`{"s":"plain","esc":"a \" b \\\\ c","uni":"héllo ☃","nl":"line\nbreak"}`,
		`{"arr":[1,{"k":"v"},[2,3],"}"],"after":"yes"}`,
		`{"brace":"{not an object}","bracket":"[not an array]","after":1}`,
		`{ "spaced" : { "k" : [ 1 , 2 ] } , "next" : "x" }`,
		`{"empty":{},"none":[],"str":""}`,
		`{}`,
		`{"only":"one"}`,
	}
	for _, src := range objects {
		t.Run(clip(src), func(t *testing.T) {
			var want map[string]json.RawMessage
			if err := json.Unmarshal([]byte(src), &want); err != nil {
				t.Fatalf("fixture is not valid JSON: %v", err)
			}
			keys := make([]string, 0, len(want)+1)
			for k := range want {
				keys = append(keys, k)
			}
			keys = append(keys, "absent")
			got := Fields([]byte(src), keys...)
			for i, k := range keys {
				if k == "absent" {
					if got[i] != nil {
						t.Errorf("Fields(%q) = %q, want nil", k, got[i])
					}
					continue
				}
				if !sameJSON(t, got[i], want[k]) {
					t.Errorf("Fields(%q) = %q, want %q", k, got[i], want[k])
				}
			}
		})
	}
}

// sameJSON compares two raw values by what they decode to, since Fields
// returns the source bytes and encoding/json may have re-spaced them.
func sameJSON(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()
	if string(got) == string(want) {
		return true
	}
	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal(want, &b) != nil {
		return false
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// A repeated key is the one documented difference from encoding/json: Fields
// stops at the first, because stopping early is the point.
func TestFieldsKeepsTheFirstOfARepeatedKey(t *testing.T) {
	if got := string(Fields([]byte(`{"dup":"first","dup":"second"}`), "dup")[0]); got != `"first"` {
		t.Errorf("dup = %q, want the first", got)
	}
}

func TestFieldsOnBrokenRecords(t *testing.T) {
	tests := []struct {
		name, src, key, want string
	}{
		{"truncated mid value", `{"a":"one","b":{"c":`, "a", `"one"`},
		{"truncated key", `{"a":"one","b`, "a", `"one"`},
		{"not an object", `["a","b"]`, "a", ""},
		{"empty", ``, "a", ""},
		{"key after a huge value", `{"big":"` + strings.Repeat("x", 4096) + `","last":7}`, "last", "7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fields([]byte(tt.src), tt.key)[0]
			if string(got) != tt.want {
				t.Errorf("Fields(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestFieldsStopsAtTheKeysItWasAsked is what makes it cheap: a record is one
// huge value and the keys a reader wants are usually in front of it.
func TestFieldsStopsAtTheKeysItWasAsked(t *testing.T) {
	src := []byte(`{"type":"event_msg","payload":{"body":"` + strings.Repeat("y", 1<<20) + `"}}`)
	if got := string(Fields(src, "type")[0]); got != `"event_msg"` {
		t.Fatalf("type = %q", got)
	}
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

func BenchmarkFields(b *testing.B) {
	src := []byte(`{"timestamp":"2026-09-15T14:00:00Z","ordinal":42,"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"call_1","aggregated_output":"` + strings.Repeat("out ", 1<<18) + `"}}}`)
	b.SetBytes(int64(len(src)))
	b.Run("Fields", func(b *testing.B) {
		for b.Loop() {
			Fields(src, "timestamp", "ordinal", "type", "payload")
		}
	})
	b.Run("Unmarshal", func(b *testing.B) {
		for b.Loop() {
			var env struct {
				Timestamp string          `json:"timestamp"`
				Ordinal   *int64          `json:"ordinal"`
				Type      string          `json:"type"`
				Payload   json.RawMessage `json:"payload"`
			}
			_ = json.Unmarshal(src, &env)
		}
	})
}

// A record cut off by a writer that has not finished it yet: every key before
// the cut comes back, and nothing after it does, however the cut falls. The
// reader asks for a key that is never there too, so the walk has to reach the
// cut instead of stopping at the keys it found.
func TestFieldsStopsCleanlyAtEveryKindOfCut(t *testing.T) {
	tests := []struct {
		name, src string
		wantA     string
	}{
		{"unquoted key", `{"a":1,b:2}`, "1"},
		{"key never closes", `{"a":1,"b`, "1"},
		{"no colon", `{"a":1,"b" 2}`, "1"},
		{"value missing", `{"a":1,"b":`, "1"},
		{"string never closes", `{"a":1,"b":"par`, "1"},
		{"object never closes", `{"a":1,"b":{"c":[1,2]`, "1"},
		{"string inside an object never closes", `{"a":1,"b":{"c":"par`, "1"},
		{"no comma", `{"a":1 "b":2}`, "1"},
		{"cut after a value", `{"a":1,"b":2`, "1"},
		// A number cut by the end of input is a different number, not a
		// shorter one; a reader must not be handed 12 for 12345.
		{"number cut at the end", `{"b":2,"a":12`, ""},
		{"literal cut at the end", `{"a":tr`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fields([]byte(tt.src), "a", "never")
			if string(got[0]) != tt.wantA || got[1] != nil {
				t.Errorf("Fields(%s) = %q, %q, want %q, nil", tt.src, got[0], got[1], tt.wantA)
			}
		})
	}
}
