package copilot

import (
	"testing"

	"github.com/Ryheff24/agentlog/internal/spec"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// See the note on the same test for Claude: the table and the reader are two
// statements of one thing, and this is what keeps them the same thing.
func TestTableMatchesTheReader(t *testing.T) {
	for _, r := range spec.Of(transcript.Copilot).Records {
		t.Run(r.Type, func(t *testing.T) {
			home := t.TempDir()
			path := writeEvents(t, home, []map[string]any{
				{"type": "session.start", "data": map[string]any{"context": map[string]any{"cwd": "/work"}}},
				{"type": r.Type, "data": map[string]any{}},
			})
			tr := load(t, home, path)
			if n := tr.Unknowns[r.Type]; n != 0 {
				t.Errorf("%q is in the table but the reader has no case for it", r.Type)
			}
		})
	}
}
