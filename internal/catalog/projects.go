package catalog

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Project is the sessions that worked in one repository, or in one
// directory that is in none.
type Project struct {
	Dir      string                // the repository's root, or the directory
	Sessions []*transcript.Session // newest work first
	Dirs     []string              // other directories inside it that sessions ran in
}

// Latest is the time of the project's most recent work.
func (p Project) Latest() time.Time { return Worked(p.Sessions[0]) }

// Subagents counts the subagents of every session in the project.
func (p Project) Subagents() int {
	n := 0
	for _, s := range p.Sessions {
		n += len(s.Children)
	}
	return n
}

// Worked is when a session last did something: its last event once the log
// has been read, else its last write.
func Worked(s *transcript.Session) time.Time {
	if !s.LastEvent.IsZero() && !s.Live {
		return s.LastEvent
	}
	return s.Updated
}

// Projects groups the matches by repository, newest work first. Every
// session is read, and the ones with nothing asked in them are set aside in
// res.Blank, so a project made only of cleared sessions is not listed.
func (c *Catalog) Projects(ctx context.Context, res *Result) []Project {
	all := make([]*transcript.Session, len(res.Matches))
	for i, m := range res.Matches {
		all[i] = m.Session
	}
	c.Enrich(ctx, all)

	roots := map[string]string{}
	byDir := map[string]*Project{}
	var order []*Project
	for _, s := range all {
		if blank(s) {
			res.Blank = append(res.Blank, s)
			continue
		}
		dir := projectRoot(s.CWD, roots)
		p := byDir[dir]
		if p == nil {
			p = &Project{Dir: dir}
			byDir[dir] = p
			order = append(order, p)
		}
		p.Sessions = append(p.Sessions, s)
		if s.CWD != dir && !slices.Contains(p.Dirs, s.CWD) {
			p.Dirs = append(p.Dirs, s.CWD)
		}
	}
	out := make([]Project, 0, len(order))
	for _, p := range order {
		sort.SliceStable(p.Sessions, func(i, j int) bool { return Worked(p.Sessions[i]).After(Worked(p.Sessions[j])) })
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Latest().After(out[j].Latest()) })
	return out
}

// projectRoot is the repository a directory is in: the nearest directory at
// or above it holding .git, or the directory itself when there is none. A
// Claude Code worktree counts as its repository. Answers are cached in roots.
func projectRoot(dir string, roots map[string]string) string {
	if i := strings.Index(dir, "/.claude/worktrees/"); i >= 0 {
		dir = dir[:i]
	}
	if r, ok := roots[dir]; ok {
		return r
	}
	root := dir
	home, _ := os.UserHomeDir()
	for d := dir; d != "" && d != home && d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
	}
	roots[dir] = root
	return root
}
