package engine

import (
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

func TestOpaque(t *testing.T) {
	head := strings.Repeat("\x01\x02\x00\x7f", 100)
	long := head + strings.Repeat("ordinary line\n", 4000)
	for _, tt := range []struct {
		name string
		s    string
		max  int
		want bool
	}{
		{"empty", "", 6000, false},
		{"text", "ok\n", 6000, false},
		{"all binary", head, 6000, true},
		{"binary head on a long text, clipped", long, 6000, true},
		{"the same text unclipped is mostly text", long, 0, false},
		{"a clip that keeps none of the head", strings.Repeat("ordinary line\n", 4000) + head[:20], 6000, false},
	} {
		if _, got := Opaque(tt.s, tt.max); got != tt.want {
			t.Errorf("%s: Opaque = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHideBinary(t *testing.T) {
	binary := strings.Repeat("\x00\x01ELF�", 30)
	r := &Response{Ref: "abcd1234", Events: []transcript.Event{
		{Index: 4, Output: binary, Input: "cat image", Changes: []transcript.Change{{Diff: "+" + binary}}},
		{Index: 5, Output: "fine \x1b[0m"},
	}}
	r.HideBinary(100)
	e := r.Events[0]
	if want := "agentlog abcd1234#4 --bytes --full --json prints it as logged"; !strings.Contains(e.Output, want) {
		t.Errorf("output %q, want it to name %q", e.Output, want)
	}
	if !strings.HasPrefix(e.Changes[0].Diff, "[binary, not shown: ") || e.Input != "cat image" {
		t.Errorf("event %+v", e)
	}
	if r.Events[1].Output != "fine \x1b[0m" {
		t.Errorf("a sparse field was replaced: %q", r.Events[1].Output)
	}
	want := Cut{What: "binary fields not shown", Count: 2, Bytes: 2*len(binary) + 1, Get: "agentlog abcd1234#N --bytes --json"}
	if len(r.Truncated) != 1 || r.Truncated[0] != want {
		t.Errorf("truncated %+v, want %+v", r.Truncated, want)
	}

	quiet := &Response{Events: []transcript.Event{{Output: "text"}}}
	quiet.HideBinary(0)
	if len(quiet.Truncated) != 0 {
		t.Errorf("nothing hidden, yet %+v", quiet.Truncated)
	}
}

func TestBytesGet(t *testing.T) {
	for _, tt := range []struct {
		ref        string
		index      int
		long, json bool
		want       string
	}{
		{"abcd1234", 7, false, false, "agentlog abcd1234#7 --bytes"},
		{"abcd1234", 0, true, true, "agentlog abcd1234#N --bytes --full --json"},
		{"", 3, false, false, "agentlog <ref>#3 --bytes"},
	} {
		if got := BytesGet(tt.ref, tt.index, tt.long, tt.json); got != tt.want {
			t.Errorf("BytesGet(%q, %d, %v, %v) = %q, want %q", tt.ref, tt.index, tt.long, tt.json, got, tt.want)
		}
	}
}
