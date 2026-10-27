package claude

import (
	"path/filepath"
	"testing"

	"github.com/Ryheff24/agentlog/internal/spec"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// The table is a second statement of what the reader knows, and two statements
// of the same thing drift apart unless something checks. This is that check:
// every declared type, fed to the reader, must not come back as unread.
func TestTableMatchesTheReader(t *testing.T) {
	for _, r := range spec.Of(transcript.Claude).Records {
		t.Run(r.Type, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), sessionID+".jsonl")
			writeJSONL(t, path, []map[string]any{user("go"), {"type": r.Type}})
			tr, err := (&Provider{}).Load(&transcript.Session{ID: sessionID, Files: []string{path}}, transcript.Load{})
			if err != nil {
				t.Fatal(err)
			}
			if n := tr.Unknowns[r.Type]; n != 0 {
				t.Errorf("%q is in the table but the reader has no case for it", r.Type)
			}
		})
	}
}
