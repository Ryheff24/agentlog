package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/engine"
	"github.com/Ryheff24/agentlog/internal/render"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

func TestFeedbackFixCommands(t *testing.T) {
	w := newWorld(t)
	id := w.feedbackWorld()
	for _, tc := range []struct{ args, want, drop []string }{
		{[]string{id, "--user"}, []string{"human first", "human pasted", "human attached"}, []string{"peer", "background", "refused", "#999"}},
		{[]string{id + "#1-", "--kind", "owner", "--full"}, []string{"human first", "[pasted]", "human pasted"}, []string{"cross-session", "refused"}},
		{[]string{id, "--user", "--grep", "attached", "--count"}, []string{"1 matching events in 1 sessions"}, nil},
		{[]string{id, "--kind", "refusal"}, []string{"policy", "blocked"}, []string{"human first"}},
		{[]string{id + "#5", "--full"}, []string{"#999 assistant historical-only-needle"}, nil},
		{[]string{id + "#5"}, []string{"[handback]", "agentlog other#1"}, []string{"#999 assistant"}},
		{[]string{id, "--grep", "historical-only-needle", "--count"}, []string{"0 matching events in 0 sessions"}, nil},
		{[]string{id, "--grep", "historical-only-needle", "--include-handbacks", "--count"}, []string{"1 matching events"}, nil},
		{[]string{"claude", "all", "--content", "historical-only-needle", "--include-handbacks", "--count"}, []string{"1 matching events"}, nil},
		{[]string{id, "--brief"}, []string{"human first"}, []string{"… human"}},
		{[]string{teamID, "--tree"}, []string{render.ShortID(teamID), render.ShortID(teslaID)}, nil},
		{[]string{teslaID, "--tree"}, []string{"parent:"}, nil},
		{[]string{"/work/no/such-file.go", "3d", "--list"}, []string{"no session"}, []string{"5a1e0000"}},
		{[]string{id, "--list"}, []string{"ephemeral jobs without rollouts"}, nil},
		{[]string{id}, []string{"working tree unknown", "--user"}, nil},
		{[]string{"--help"}, []string{"--user", "--images", "--count", "--content", "--brief", "--tree", "refusal"}, nil},
	} {
		out, code := w.run("/work/feedback", tc.args...)
		if code != 0 {
			t.Errorf("%v code %d: %s", tc.args, code, out)
		}
		for _, s := range tc.want {
			if !strings.Contains(out, s) {
				t.Errorf("%v missing %q: %s", tc.args, s, out)
			}
		}
		for _, s := range tc.drop {
			if strings.Contains(out, s) {
				t.Errorf("%v includes %q: %s", tc.args, s, out)
			}
		}
	}
	// JSON must honor --full, including the range and preview response paths.
	for _, args := range [][]string{{id, "--user", "--json", "--full"}, {id + "#1-", "--kind", "owner", "--json", "--full"}, {id, "--json", "--full"}} {
		out, code := w.run("/work/feedback", args...)
		if code != 0 || !json.Valid([]byte(out)) || !strings.Contains(out, strings.Repeat("x", 3000)) || strings.Contains(out, "long fields clipped") {
			t.Errorf("full JSON %v: %s", args, out)
		}
	}
	out, _ := w.run("/work/feedback", id, "--user", "--json", "--max", "2000")
	if !strings.Contains(out, "long fields clipped") || strings.Contains(out, strings.Repeat("x", 3000)) {
		t.Error(out)
	}
	for _, h := range []string{codexID, copilotID} {
		out, code := w.run("/work", h, "--user", "--count")
		if code != 0 || strings.Contains(out, "0 matching events") {
			t.Errorf("%s: %s", h, out)
		}
	}
	out, _ = w.run("/work", reviewerID, "--user", "--count")
	if !strings.Contains(out, "0 matching events") {
		t.Error(out)
	}
}

func TestImageCommandSourcedRecords(t *testing.T) {
	w := newWorld(t)
	id := w.feedbackWorld()
	for _, args := range [][]string{{id, "--images", "--json"}, {id + "#6", "--images", "--json"}, {id + "#7", "--images", "--json"}} {
		out, code := w.run("/work/feedback", args...)
		if code != 0 {
			t.Fatal(out)
		}
		var imgs []imageFile
		if err := json.Unmarshal([]byte(out), &imgs); err != nil {
			t.Fatal(err, out)
		}
		if len(imgs) == 0 {
			t.Fatal("images missing", args)
		}
		for _, img := range imgs {
			t.Cleanup(func() { _ = os.Remove(img.Path) })
			b, err := os.ReadFile(img.Path)
			if err != nil || string(b) != "image" || img.Ref == "" {
				t.Errorf("%+v %s %v", img, b, err)
			}
		}
	}
	out, code := w.run("/work", id+"#1", "--images")
	if code != 0 || !strings.Contains(out, "no images") {
		t.Fatal(out)
	}
}

func TestImageFormats(t *testing.T) {
	for _, raw := range []string{
		`{"type":"local_image","path":"/tmp/a.png"}`,
		`{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}`,
		`{"type":"image_url","url":"https://example.test/image.png"}`,
		`{"type":"image","source":{"url":"https://example.test/image.png"}}`,
		`{"output":"{\"content\":[{\"type\":\"local_image\",\"path\":\"a.png\"}]}"}`,
	} {
		var v any
		_ = json.Unmarshal([]byte(raw), &v)
		n := 0
		err := walkImages(v, func(value, media string) error { n++; return nil })
		if err != nil || n != 1 {
			t.Errorf("%s: %d %v", raw, n, err)
		}
	}
}

func TestDoctorPerTypeVersionsAndWriterOrdering(t *testing.T) {
	w := newWorld(t)
	for i, v := range []string{"2.3.0", "2.4.0", "2.5.0"} {
		id := []string{"fade0001-0000-4000-8000-000000000001", "fade0002-0000-4000-8000-000000000001", "fade0003-0000-4000-8000-000000000001"}[i]
		w.file(filepath.Join(w.root, "claude", "projects", "-work-more", id+".jsonl"), time.Hour, []map[string]any{{"type": "future-unknown", "version": v, "sessionId": id, "cwd": "/work/more", "timestamp": w.at(time.Hour), "detail": "hello"}})
	}
	out, code := w.run("/work", "doctor", "claude")
	if code != 0 || !strings.Contains(out, "observed versions:") || !strings.Contains(out, "2.5.0") || !strings.Contains(out, "+1 more") {
		t.Fatal(out)
	}
	out, code = w.run("/work/shop", "/work/shop/src/checkout.ts", "all", "--edits", "--files")
	if code != 0 || !strings.Contains(out, "last write:") {
		t.Fatal(out)
	}
}

func TestCountAndTreeJSON(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{codexID, "--user", "--count", "--json"}, {codexID + "#1-", "--count", "--json"}, {teamID, "--tree", "--json"}, {"claude", "all", "--grep", "checkout", "--count", "--json"}} {
		out, code := w.run("/work", args...)
		if code != 0 || !json.Valid([]byte(out)) {
			t.Fatal(args, out)
		}
	}
	var b bytes.Buffer
	if err := writeJSON(jsonWriter{Writer: &b, Full: true}, []*engine.Response{{Events: []transcript.Event{{Text: strings.Repeat("x", 3000)}}}}); err != nil || !strings.Contains(b.String(), strings.Repeat("x", 3000)) {
		t.Fatal(b.String(), err)
	}
}

func TestInterAgentMessagesAreSearchable(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/messages", time.Hour)
	l.user("-", "do the work")
	l.assistant("", "tool_use", toolUse("send", "SendMessage", map[string]any{"to": "peer", "message": "real-message-needle"}))
	l.result("send", "{\"success\":true}", false, nil)
	l.user("", "<teammate-message teammate_id=\"peer\">real-peer-needle</teammate-message>")
	l.assistant("", "tool_use", toolUse("read", "Bash", map[string]any{"command": "agentlog other#1"}))
	l.result("read", "#999 assistant quoted-transcript-needle", false, nil)
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	for _, tc := range []struct {
		pattern string
		want    int
	}{{"^SendMessage$", 1}, {"real-message-needle", 1}, {"real-peer-needle", 1}, {"quoted-transcript-needle", 0}} {
		out, code := w.run("/", siblingID, "--grep", tc.pattern, "--count")
		if code != 0 || !strings.Contains(out, fmt.Sprintf("%d matching events", tc.want)) {
			t.Fatal(out)
		}
	}
}
