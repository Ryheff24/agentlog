package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// A session in a subdirectory of a repository is that repository's work; a
// repository nested in another is its own project, as dashboard is inside
// orchestrator; a worktree is its repository's; a directory in no repository
// is a project of its own.
func TestProjectRoot(t *testing.T) {
	top := t.TempDir()
	outer := filepath.Join(top, "orchestrator")
	inner := filepath.Join(outer, "dashboard")
	for _, d := range []string{filepath.Join(outer, ".git"), filepath.Join(inner, ".git"), filepath.Join(inner, "agentlog", "internal"), filepath.Join(top, "notes")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := map[string]string{}
	for dir, want := range map[string]string{
		filepath.Join(inner, "agentlog", "internal"):      inner,
		filepath.Join(outer, "docs"):                      outer, // gone from disk, still inside the repository
		filepath.Join(inner, ".claude/worktrees/fix/src"): inner,
		filepath.Join(top, "notes"):                       filepath.Join(top, "notes"),
	} {
		if got := projectRoot(dir, roots); got != want {
			t.Errorf("projectRoot(%s) = %s, want %s", dir, got, want)
		}
	}
	// Answers are kept: a repository deleted since is still where its
	// sessions were.
	if err := os.RemoveAll(filepath.Join(inner, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := projectRoot(filepath.Join(inner, "agentlog", "internal"), roots); got != inner {
		t.Errorf("cached root = %s, want %s", got, inner)
	}
}
