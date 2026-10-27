package codex

import (
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/spec"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// See the note on the same test for Claude. Codex writes three families of
// record, so the fixture is built from the prefix of the declared name.
func TestTableMatchesTheReader(t *testing.T) {
	for _, r := range spec.Of(transcript.Codex).Records {
		t.Run(r.Type, func(t *testing.T) {
			home := t.TempDir()
			f := writeRollout(t, home, "rollout-2026-09-15T14-00-00-"+threadID+".jsonl", []rec{
				meta(threadID, nil),
				declared(r.Type),
			})
			if n := load(t, threadID, f).Unknowns[r.Type]; n != 0 {
				t.Errorf("%q is in the table but the reader has no case for it", r.Type)
			}
		})
	}
}

// declared builds the emptiest record of a declared type that is still that type.
func declared(name string) rec {
	family, sub, nested := strings.Cut(name, ":")
	switch {
	case !nested:
		return rec{at: 1, typ: family, payload: map[string]any{}}
	case family == "item":
		return item(1, map[string]any{"type": sub, "id": "item-1"})
	default:
		return rec{at: 1, typ: family, payload: map[string]any{"type": sub}}
	}
}
