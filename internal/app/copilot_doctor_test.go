package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorCopilotVersionsRespectTimeScope(t *testing.T) {
	w := bareWorld(t)
	for _, s := range []struct {
		id, version string
		age         time.Duration
	}{{copilotID, "0.0.0", time.Hour}, {checkoutID, "1.0.17", 30 * 24 * time.Hour}} {
		w.file(filepath.Join(w.root, "copilot", "session-state", s.id, "events.jsonl"), s.age, []map[string]any{{"type": "session.start", "data": map[string]any{"copilotVersion": s.version, "context": map[string]any{"cwd": "/work"}}}})
	}
	out, code := w.run("/", "doctor", "copilot")
	if code != 0 || !strings.Contains(out, "0.0.0 ×1") || strings.Contains(out, "1.0.17") {
		t.Fatal(out)
	}
	out, code = w.run("/", "doctor", "copilot", "all", "--limit", "100")
	if code != 0 || !strings.Contains(out, "1.0.17 ×1") || !strings.Contains(out, "0.0.0 ×1") {
		t.Fatal(out)
	}
}
