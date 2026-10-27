// Command agentlog lets a coding agent find and read other agents' session
// logs (Claude Code, Codex, Copilot CLI) in as few, as small, calls as possible.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ryheff24/agentlog/internal/app"
	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/claude"
	"github.com/Ryheff24/agentlog/internal/codex"
	"github.com/Ryheff24/agentlog/internal/copilot"
	"github.com/Ryheff24/agentlog/internal/monitor"
	"github.com/Ryheff24/agentlog/internal/term"
	"github.com/Ryheff24/agentlog/internal/work"
)

func main() {
	// Before anything reads a file: this runs on the machine somebody is
	// working on, uninvited, and a few cores at low priority is the whole of
	// what it may take. The command settles the final number once it has read
	// --jobs; this is the floor under everything that happens first.
	work.Restrain()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, _ := os.Getwd()
	monitor.Start(os.Args[1:], cwd)
	cat := &catalog.Catalog{
		Providers: []catalog.Provider{claude.New(), codex.New(), copilot.New()},
		CWD:       cwd,
		Now:       time.Now(),
	}
	// Log text can carry terminal escape sequences; a person's terminal gets
	// them made visible, an agent's pipe gets the bytes.
	out := &counter{w: term.Safe(os.Stdout, os.Stdout)}
	errOut := &counter{w: term.Safe(os.Stderr, os.Stderr)}
	code := app.Run(app.Env{Catalog: cat, Stdout: out, Stderr: errOut, Ctx: ctx}, os.Args[1:])
	monitor.Finish(code, out.n, errOut.n)
	os.Exit(code)
}

// counter counts what reaches the caller, which for an agent is what enters
// its context.
type counter struct {
	w io.Writer
	n int64
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
