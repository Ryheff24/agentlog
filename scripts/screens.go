//go:build ignore

// screens shows a person a change to the two screens every agent reads first,
// a bare `agentlog` and `agentlog --help`, from the copies TestScreens pins in
// internal/app/testdata/screens. Those hold the test world's made-up
// sessions, so the page is safe to share or screenshot.
//
//	go run scripts/screens.go [-before REF|DIR] [-after REF|DIR] [-html FILE] [-md FILE]
//
// A side is a git ref, read at that commit, or a folder that holds bare.txt
// and help.txt. -before defaults to origin/main and -after to the working
// tree's pinned files. A file that is missing on a side is an empty screen.
// -html writes one page with the four outputs side by side; -md writes the
// comment .github/workflows/screens.yml posts. It prints "changed" or "same".
//
// After changing a screen on purpose:
//
//	go test ./internal/app -run TestScreens -update
//	go run scripts/screens.go -html "$TMPDIR/screens.html"
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Ryheff24/agentlog/internal/screens"
)

const pinned = "internal/app/testdata/screens"

// A pinned screen is a few kilobytes. More than this is not a screen.
const maxFile = 1 << 20

func main() {
	before := flag.String("before", "origin/main", "the old side: a git ref or a folder")
	after := flag.String("after", pinned, "the new side: a git ref or a folder")
	beforeLabel := flag.String("before-label", "", "what to call the old side (default: its ref or folder)")
	afterLabel := flag.String("after-label", "", "what to call the new side (default: its ref or folder)")
	page := flag.String("html", "", "write the four outputs side by side to this file")
	comment := flag.String("md", "", "write the pull request comment to this file")
	flag.Parse()

	old, err := side(*before, *beforeLabel)
	if err != nil {
		fail(err)
	}
	if *after == pinned && *afterLabel == "" {
		*afterLabel = "the working tree"
	}
	now, err := side(*after, *afterLabel)
	if err != nil {
		fail(err)
	}
	if *page != "" {
		if err := os.WriteFile(*page, []byte(screens.Page(old, now)), 0o644); err != nil {
			fail(err)
		}
	}
	if *comment != "" {
		if err := os.WriteFile(*comment, []byte(screens.Comment(old, now)), 0o644); err != nil {
			fail(err)
		}
	}
	if screens.Changed(old, now) {
		fmt.Println("changed")
	} else {
		fmt.Println("same")
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "screens:", err)
	os.Exit(1)
}

func side(spec, label string) (screens.Side, error) {
	if label == "" {
		label = spec
	}
	read := fromFolder
	if st, err := os.Stat(spec); err != nil || !st.IsDir() {
		// Not a folder, so it has to be a commit. A ref that names no commit is
		// a mistake to report, not an empty side.
		if err := exec.Command("git", "rev-parse", "--verify", "--quiet", spec+"^{commit}").Run(); err != nil {
			return screens.Side{}, fmt.Errorf("%s is neither a folder nor a commit", spec)
		}
		read = fromCommit
	}
	bare, err := read(spec, "bare.txt")
	if err != nil {
		return screens.Side{}, err
	}
	help, err := read(spec, "help.txt")
	if err != nil {
		return screens.Side{}, err
	}
	return screens.Side{Label: label, Bare: bare, Help: help}, nil
}

func fromFolder(dir, name string) (string, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFile))
	return string(b), err
}

func fromCommit(ref, name string) (string, error) {
	out, err := exec.Command("git", "show", ref+":"+pinned+"/"+name).Output()
	if err != nil {
		return "", nil // the commit has no such file
	}
	return string(out[:min(len(out), maxFile)]), nil
}
