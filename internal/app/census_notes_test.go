package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCensusesExplainRewoundTurns(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/rewound", time.Hour)
	root := l.user("-", "start")
	l.assistant("", "tool_use", toolUse("old", "Agent", map[string]any{"prompt": "abandoned", "description": "old"}))
	l.result("old", "done", false, nil)
	l.user(root, "live replacement")
	l.assistant("", "end_turn", text("done"))
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	for _, view := range []string{"--tools", "--usage", "--edits"} {
		for _, format := range []string{"", "--json"} {
			args := []string{siblingID, view}
			if format != "" {
				args = append(args, format)
			}
			out, code := w.run("/", args...)
			if code != 0 || !strings.Contains(out, "1 rewound turns not counted (--branches)") {
				t.Fatal(args, out)
			}
			out, _ = w.run("/", append(args, "--branches")...)
			if strings.Contains(out, "not counted") {
				t.Fatal(out)
			}
		}
	}
}
