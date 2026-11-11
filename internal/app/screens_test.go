package app

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/claude"
	"github.com/Ryheff24/agentlog/internal/codex"
	"github.com/Ryheff24/agentlog/internal/copilot"
	"github.com/Ryheff24/agentlog/internal/render"
)

var update = flag.Bool("update", false, "rewrite the pinned screens in testdata/screens")

// The home screen and --help are the first thing every agent reads, and the
// maintainer placed each line by hand. They are pinned as files, so a change
// to either shows in a diff as the screen itself, and a pull request that
// changes one gets a comment with both versions (.github/workflows/screens.yml).
// The sessions are the test world's, so the files hold nothing from a real log.
func TestScreens(t *testing.T) {
	w := newWorld(t)
	for _, s := range []struct {
		name string
		args []string
	}{{"bare", nil}, {"help", []string{"--help"}}} {
		got := w.pinned(w.stdout("/work/shop", s.args...))
		if strings.Contains(got, w.root) {
			t.Fatalf("the %s screen still names the test's own folder:\n%s", s.name, got)
		}
		path := filepath.Join("testdata", "screens", s.name+".txt")
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("agentlog %s no longer prints %s.\n%s\nIf the change is meant, run: go test ./internal/app -run TestScreens -update",
				strings.Join(s.args, " "), path, firstDifference(string(want), got))
		}
	}
}

// pinned copies the format of Home's harness line. This fails when Home
// changes it, so the copy changes with it.
func TestPinnedPadsLikeHome(t *testing.T) {
	var b strings.Builder
	render.Home(&b, &catalog.Result{Harnesses: []catalog.Harness{{Name: "x", Roots: []string{"/r"}}}}, render.Options{}, 1)
	line, _, _ := strings.Cut(strings.TrimPrefix(b.String(), "harnesses:\n"), "\n")
	if want := fmt.Sprintf(harnessFormat, "x", "/r", "not found"); line != want {
		t.Errorf("Home prints a harness as %q, and pinned would print %q", line, want)
	}
}

func TestFirstDifference(t *testing.T) {
	for _, c := range []struct{ want, got, says string }{
		{"a\nb\n", "a\nc\n", "line 2:\n  pinned: b\n  now:    c"},
		{"a\n", "a\nb\n", "line 2:\n  pinned: (no such line)\n  now:    b"},
		{"a\nb\n", "a\n", "line 2:\n  pinned: b\n  now:    (no such line)"},
		{"a\n", "a\n", ""},
	} {
		if says := firstDifference(c.want, c.got); says != c.says {
			t.Errorf("firstDifference(%q, %q) = %q, want %q", c.want, c.got, says, c.says)
		}
	}
}

// stdout runs one agentlog command line and returns only its stdout: the
// screen an agent reads, without the byte count that goes to stderr.
func (w *world) stdout(cwd string, args ...string) string {
	w.t.Helper()
	cat := &catalog.Catalog{
		Providers: []catalog.Provider{claude.New(), codex.New(), copilot.New()},
		CWD:       cwd,
		Now:       w.now,
	}
	var out bytes.Buffer
	if code := Run(Env{Catalog: cat, Stdout: &out, Stderr: io.Discard, Ctx: context.Background()}, args); code != 0 {
		w.t.Fatalf("agentlog %s: code %d:\n%s", strings.Join(args, " "), code, out.String())
	}
	return out.String()
}

const harnessFormat = "  %-8s %-45s %s"

var harnessLine = regexp.MustCompile(`(?m)^  (\S+) +(\S.*?) +(\d+ sessions? in .*|not found)$`)

// pinned makes a screen the same on every run and machine. The harness roots
// are under the test's temp folder, whose path and length change, and Home
// pads them to a column. So "~" takes the folder's place and the line is
// padded again, the way Home prints roots under a real home folder.
func (w *world) pinned(screen string) string {
	return harnessLine.ReplaceAllStringFunc(screen, func(line string) string {
		m := harnessLine.FindStringSubmatch(line)
		return fmt.Sprintf(harnessFormat, m[1], strings.ReplaceAll(m[2], w.root, "~"), m[3])
	})
}

// firstDifference names the first line where two screens differ.
func firstDifference(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	line := func(s []string, i int) string {
		if i >= len(s) || (i == len(s)-1 && s[i] == "") {
			return "(no such line)"
		}
		return s[i]
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		if x, y := line(a, i), line(b, i); x != y {
			return fmt.Sprintf("line %d:\n  pinned: %s\n  now:    %s", i+1, x, y)
		}
	}
	return ""
}
