package app

import (
	"agentlog/internal/clip"
	"agentlog/internal/engine"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONAndTextShareMax(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/clip", time.Hour)
	body := strings.Repeat("你好 long output\n", 800)
	l.user("-", body)
	l.assistant("", "tool_use", toolUse("x", "Bash", map[string]any{"command": body}))
	l.result("x", body, false, nil)
	l.assistant("", "tool_use", toolUse("edit", "Write", map[string]any{"file_path": "a.go", "content": body}))
	l.result("edit", "ok", false, nil)
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	for _, max := range []string{"100", "50000", "0"} {
		for _, full := range []bool{false, true} {
			args := []string{siblingID + "#1-", "--max", max}
			if full {
				args = append(args, "--full")
			}
			out, code := w.run("/", append(args, "--json")...)
			if code != 0 {
				t.Fatal(out)
			}
			var r engine.Response
			if err := json.Unmarshal([]byte(out), &r); err != nil {
				t.Fatal(err)
			}
			limit := 6000
			if max == "100" {
				limit = 100
			}
			if max == "50000" {
				limit = 50000
			}
			if full {
				limit = 0
			}
			want := clip.Block(body, limit)
			wantInput := want
			if limit == 0 || len(body) <= limit {
				wantInput = body
			}
			if r.Events[0].Text != want || r.Events[1].Input != wantInput || r.Events[1].Output != wantInput {
				t.Fatalf("max %s full %v text %d/%d input %d/%d output %d/%d", max, full, len(r.Events[0].Text), len(want), len(r.Events[1].Input), len(wantInput), len(r.Events[1].Output), len(want))
			}
			if len(r.Events[2].Changes[0].Diff) > limit+100 && limit > 0 {
				t.Fatal("diff ignored max")
			}
			text, code := w.run("/", args...)
			if code != 0 {
				t.Fatal(text)
			}
			for _, line := range strings.Split(strings.TrimSpace(want), "\n") {
				if !strings.Contains(text, line) {
					t.Fatalf("text missing clipped line %q", line)
				}
			}
		}
	}
}
