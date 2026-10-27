package private

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDirNarrowsAndRefusesLinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if err := Dir(dir); err != nil {
		t.Fatal(err)
	}
	// An older agentlog made its directories 0755.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Dir(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v, want 0700", info.Mode().Perm())
	}

	// Someone made the shared-temp directory first, as a link to elsewhere.
	elsewhere := filepath.Join(root, "elsewhere")
	os.Mkdir(elsewhere, 0o700)
	link := filepath.Join(root, "agentlog-runs")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if err := Dir(link); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("linked directory: %v, want ErrNotPrivate", err)
	}
	// A file where the directory should be.
	file := filepath.Join(root, "file")
	os.WriteFile(file, nil, 0o600)
	if err := Dir(file); err == nil {
		t.Fatal("a file passed as a directory")
	}
}

func TestOpenNeverFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "runs.jsonl")
	if err := os.Symlink(victim, planted); err != nil {
		t.Fatal(err)
	}
	if f, err := Open(planted, os.O_CREATE|os.O_WRONLY|os.O_TRUNC); err == nil {
		f.Close()
		t.Fatal("opened through a link")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatalf("the link's target was written: %q", b)
	}

	// A file left by an older agentlog at 0644 is narrowed.
	old := filepath.Join(dir, "old")
	os.WriteFile(old, []byte("x\n"), 0o644)
	f, err := Open(old, os.O_APPEND|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if info, _ := os.Stat(old); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}

	// Not a regular file.
	if _, err := Open(dir, os.O_RDONLY); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("directory opened as a file: %v", err)
	}
}
