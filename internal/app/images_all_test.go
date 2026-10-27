package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/claude"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

func TestImagesExportEverySelectedSessionAndChild(t *testing.T) {
	w := bareWorld(t)
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aW1hZ2U="}}
	for _, id := range []string{siblingID, checkoutID} {
		l := w.claude(id, "/work/images", time.Hour)
		l.add("-", map[string]any{"type": "user", "message": map[string]any{"content": []any{image}}})
		l.save(filepath.Join(w.claudeProject(l.cwd), id+".jsonl"), time.Minute)
	}
	sub := w.claude(siblingID, "/work/images", time.Hour)
	sub.prefix = reviewerID[:8]
	sub.extra = map[string]any{"agentId": reviewerID, "isSidechain": true}
	sub.add("-", map[string]any{"type": "user", "message": map[string]any{"content": []any{image}}})
	sub.save(filepath.Join(w.claudeProject(sub.cwd), siblingID, "subagents", "agent-"+reviewerID+".jsonl"), time.Minute)
	for _, tc := range []struct {
		args  []string
		count int
	}{{[]string{siblingID, checkoutID, "--images", "--json"}, 2}, {[]string{siblingID, checkoutID, "--images", "--subagents", "--json"}, 3}, {[]string{siblingID, "--images", "--subagents", "--json"}, 2}} {
		out, code := w.run("/", tc.args...)
		if code != 0 {
			t.Fatal(out)
		}
		var imgs []imageFile
		if err := json.Unmarshal([]byte(out), &imgs); err != nil {
			t.Fatal(err, out)
		}
		if len(imgs) != tc.count {
			t.Fatalf("got %d want %d: %s", len(imgs), tc.count, out)
		}
		refs := map[string]bool{}
		for _, img := range imgs {
			refs[strings.Split(img.Ref, "#")[0]] = true
			data, err := os.ReadFile(img.Path)
			os.Remove(img.Path)
			if err != nil || string(data) != "image" {
				t.Fatal(err)
			}
		}
		if len(refs) != tc.count {
			t.Fatal(refs)
		}
	}
	out, _ := w.run("/", siblingID, checkoutID, "--images", "--kind", "assistant", "--json")
	if strings.TrimSpace(out) != "null" && strings.TrimSpace(out) != "[]" {
		t.Fatal(out)
	}
}

func TestImageSourcesAndErrors(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/images", time.Hour)
	l.user("-", "first")
	l.add("", map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "image", "source": map[string]any{"url": "https://example.test/image.png"}}}}})
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	for _, args := range [][]string{{siblingID + "#2-", "--images", "--subagents"}, {siblingID + "#2", "--images", "--subagents", "--head", "1", "--json"}, {siblingID, "--images", "--subagents", "--grep", "["}} {
		out, code := w.run("/", args...)
		if args[len(args)-1] == "[" {
			if code != 1 {
				t.Fatal(out)
			}
		} else if code != 0 || !strings.Contains(out, "https://example.test/image.png") {
			t.Fatal(out)
		}
	}
	path := filepath.Join(t.TempDir(), "record.jsonl")
	tr := &transcript.Transcript{Session: transcript.Session{Files: []string{path}, ID: siblingID}}
	evs := []transcript.Event{{Index: 1, Line: 1, Source: path}}
	for _, body := range []string{"bad json", `{"type":"image","source":{"media_type":"image/png","data":"not base64!"}}`} {
		os.WriteFile(path, []byte(body+"\n"), 0600)
		if _, err := readImages(tr, evs); err == nil {
			t.Fatal("bad image accepted")
		}
	}
	os.Remove(path)
	if _, err := readImages(tr, evs); err == nil {
		t.Fatal("missing source accepted")
	}
	os.WriteFile(path, []byte(`{"type":"image","source":{"media_type":"image/unknown","data":"aW1hZ2U="}}`+"\n"), 0600)
	imgs, err := readImages(tr, evs)
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range imgs {
		os.Remove(img.Path)
		if !strings.HasSuffix(img.Path, ".image") {
			t.Fatal(img)
		}
	}
	// A sandbox can make its temp path unavailable: surface that failure.
	badtmp := filepath.Join(t.TempDir(), "not-a-directory")
	os.WriteFile(badtmp, []byte("x"), 0600)
	t.Setenv("TMPDIR", badtmp)
	if _, err := readImages(tr, evs); err == nil {
		t.Fatal("unwritable temp accepted")
	}
}

func TestMultiImageSourceFailuresAreReturned(t *testing.T) {
	w := bareWorld(t)
	l := w.claude(siblingID, "/work/images", time.Hour)
	l.add("-", map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "image", "source": map[string]any{"media_type": "image/png", "data": "invalid!"}}}}})
	l.save(filepath.Join(w.claudeProject(l.cwd), siblingID+".jsonl"), time.Minute)
	out, code := w.run("/", siblingID, "--images", "--subagents")
	if code != 1 || !strings.Contains(out, "image:") {
		t.Fatal(out)
	}
	out, code = w.run("/", siblingID, "--images")
	if code != 1 {
		t.Fatal(out)
	}
	env := Env{Catalog: &catalog.Catalog{Providers: []catalog.Provider{claude.New()}}, Stdout: io.Discard}
	missing := &transcript.Session{ID: siblingID, Provider: "claude", Files: []string{filepath.Join(w.root, "missing.jsonl")}}
	err := imageSessions(env, &catalog.Result{Matches: []catalog.Match{{Session: missing}}}, catalog.Clues{}, options{})
	if err == nil {
		t.Fatal("missing log accepted")
	}
}
