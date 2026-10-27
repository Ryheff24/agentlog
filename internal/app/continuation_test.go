package app

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/engine"
	"github.com/Ryheff24/agentlog/internal/shell"
)

func TestFollowingHintsContinuesSelection(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/pages", time.Hour)
	for i := 0; i < 12; i++ {
		l.user("", "ask")
		l.assistant("", "tool_use", toolUse(string(rune('a'+i)), "Bash", map[string]any{"command": "echo selected 'word'"}))
		l.result(string(rune('a'+i)), "ok", false, nil)
	}
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	for _, page := range []string{"--head", "--tail"} {
		args := []string{siblingID, "--kind", "tool", "--grep", "selected 'word'", "--branches", "--subagents", "--include-handbacks", "--json", page, "3"}
		seen := map[int]bool{}
		for step := 0; step < 4; step++ {
			out, code := w.run("/", args...)
			if code != 0 {
				t.Fatal(out)
			}
			var rs []engine.Response
			if strings.HasPrefix(out, "[") {
				if err := json.Unmarshal([]byte(out), &rs); err != nil {
					t.Fatal(err)
				}
			} else {
				rs = make([]engine.Response, 1)
				if err := json.Unmarshal([]byte(out), &rs[0]); err != nil {
					t.Fatal(err)
				}
			}
			r := rs[0]
			if len(r.Events) != 3 {
				t.Fatalf("step %d events=%d: %s", step, len(r.Events), out)
			}
			for _, e := range r.Events {
				if e.Kind != "tool" || seen[e.Index] {
					t.Fatalf("overlap or wrong kind: %+v", e)
				}
				seen[e.Index] = true
			}
			if step < 3 {
				if len(r.Next) == 0 {
					t.Fatal("no continuation")
				}
				next := r.Next[0]
				for _, flag := range []string{"--kind tool", "--grep", "--branches", "--subagents", "--include-handbacks", "--json", page + " 3"} {
					if !strings.Contains(next, flag) {
						t.Fatalf("lost %s: %s", flag, next)
					}
				}
				commands := shell.Commands(next)
				args = commands[0].Args[1:]
			}
		}
		if len(seen) != 12 {
			t.Fatal(seen)
		}
	}
}

func TestTextContinuationRetainsFilterAndPage(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{checkoutID, "--kind", "tool", "--head", "1", "--max", "100", "--brief"}, {checkoutID + "#1-", "--kind", "tool", "--head", "1"}} {
		out, code := w.run("/", args...)
		if code != 0 || !strings.Contains(out, "(next: agentlog ") || !strings.Contains(out, "--kind tool --head 1") && !strings.Contains(out, "--kind tool --brief --max 100 --head 1") {
			t.Fatal(out)
		}
	}
}
