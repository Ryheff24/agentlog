package app

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/render"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Agents learn agentlog from its usage text and nowhere else, so anything the
// parser accepts and the text leaves out is a feature no agent will find.
func TestUsageNamesEveryFlag(t *testing.T) {
	bools, ints, strs := flagTables(&options{})
	var names []string
	for n := range bools {
		names = append(names, n)
	}
	for n := range ints {
		names = append(names, n)
	}
	for n := range strs {
		names = append(names, n)
	}
	names = append(names, "version")
	for _, n := range names {
		flag := "--" + n
		if len(n) == 1 {
			flag = "-" + n
		}
		if !regexp.MustCompile(`(^|[^-\w])` + regexp.QuoteMeta(flag) + `\b`).MatchString(render.Usage) {
			t.Errorf("the usage text never names %s", flag)
		}
	}
}

func TestUsageNamesEveryKind(t *testing.T) {
	start := strings.Index(render.Usage, "--kind K,K")
	end := strings.Index(render.Usage, "--user ")
	if start < 0 || end < start {
		t.Fatal("no --kind entry in the usage text")
	}
	entry := strings.Fields(render.Usage[start:end])
	for _, k := range append(slices.Clone(transcript.AllKinds), "owner", "refusal") {
		if !slices.Contains(entry, string(k)) {
			t.Errorf("--kind %s is accepted but not listed", k)
		}
	}
}

// A setting read from the environment is documented for people in
// docs/reference.md. It stays out of the usage text: an agent shown a setting
// starts passing it on every call.
func TestEverySettingIsDocumented(t *testing.T) {
	// Set by the harness that runs agentlog, not by a person.
	harness := []string{"TMPDIR", "CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "COPILOT_SESSION_ID"}
	ref, err := os.ReadFile("../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	lit := regexp.MustCompile(`Getenv\("([A-Z_]+)"\)|"(AGENTLOG_[A-Z_]+)"`)
	found := map[string]bool{}
	for _, root := range []string{"../../internal", "../../cmd/agentlog"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			// agentlog-usage is a separate program with its own help.
			if strings.Contains(filepath.ToSlash(path), "internal/usage/") {
				return nil
			}
			src, err := os.ReadFile(path)
			for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
				found[m[1]+m[2]] = true
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) == 0 {
		t.Fatal("found no environment variables; the scan is broken")
	}
	for name := range found {
		if !slices.Contains(harness, name) && !strings.Contains(string(ref), name) {
			t.Errorf("%s is read from the environment but docs/reference.md never names it", name)
		}
	}
}

// --help and a bare call print the same screen, so an agent that runs both
// pays twice for nothing, and one that runs either sees everything.
func TestHelpIsTheHomeScreen(t *testing.T) {
	w := newWorld(t)
	w.feedbackWorld()
	bare, code := w.run("/work/feedback")
	if code != 0 {
		t.Fatalf("bare: code %d: %s", code, bare)
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"codex", "--help"}} {
		got, code := w.run("/work/feedback", args...)
		if code != 0 || got != bare {
			t.Errorf("%v (code %d) differs from a bare call:\n%s\n--- bare:\n%s", args, code, got, bare)
		}
	}
	if !strings.Contains(bare, "recent sessions") || !strings.Contains(bare, render.Usage) {
		t.Errorf("the home screen lacks the sessions or the usage text:\n%s", bare)
	}
}
