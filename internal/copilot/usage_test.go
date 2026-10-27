package copilot

import (
	"testing"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

func shutdown(model string, input, output, read, write int64, requests int, cumulative bool) map[string]any {
	data := map[string]any{"modelMetrics": map[string]any{model: map[string]any{"usage": map[string]any{"inputTokens": input, "outputTokens": output, "cacheReadTokens": read, "cacheWriteTokens": write, "reasoningTokens": int64(2)}, "requests": map[string]any{"count": requests}}}}
	if cumulative {
		data["tokenDetails"] = map[string]any{"input": map[string]any{"tokenCount": input - read - write}}
	}
	return map[string]any{"type": "session.shutdown", "data": data}
}

func TestShutdownUsageSegmentsAndCumulativeSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     []map[string]any
		total    int64
		requests int
	}{
		{"older resumes reset metrics", []map[string]any{shutdown("model", 100, 10, 80, 0, 3, false), {"type": "session.resume"}, shutdown("model", 50, 5, 40, 0, 2, false), {"type": "session.resume"}, {"type": "session.shutdown", "data": map[string]any{"modelMetrics": map[string]any{}}}}, 165, 5},
		{"newer resumes preserve cumulative metrics", []map[string]any{shutdown("model", 100, 10, 70, 20, 3, true), {"type": "session.resume"}, shutdown("model", 100, 10, 70, 20, 3, true), {"type": "session.resume"}, shutdown("model", 150, 15, 100, 30, 5, true), {"type": "session.shutdown", "data": map[string]any{"modelMetrics": map[string]any{}}}}, 165, 5},
		{"repeated shutdown in same segment", []map[string]any{shutdown("model", 100, 10, 80, 0, 3, false), shutdown("model", 100, 10, 80, 0, 3, false)}, 110, 3},
		{"partial cache counts", []map[string]any{shutdown("model", 10, 5, 20, 0, 1, false)}, 15, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tr := load(t, home, writeEvents(t, home, tc.rows))
			if tr.Usage.Total != tc.total || tr.Usage.Requests != tc.requests || tr.Usage.Input < 0 {
				t.Fatalf("usage=%+v", tr.Usage)
			}
			if len(tr.Events) != 0 {
				t.Fatal("shutdown renumbered events")
			}
		})
	}
	home := t.TempDir()
	tr := loadWith(t, home, writeEvents(t, home, []map[string]any{shutdown("a", 100, 10, 70, 20, 2, false), {"type": "session.start"}, shutdown("b", 50, 5, 40, 0, 1, false)}), transcript.Load{Subagents: true})
	if tr.Usage.ByModel["a"] != 110 || tr.Usage.ByModel["b"] != 55 || tr.Usage.CacheWrite != 20 {
		t.Fatal(tr.Usage)
	}
}
