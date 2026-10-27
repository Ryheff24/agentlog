// Package claude reads Claude Code transcripts
// (~/.claude/projects/<encoded-cwd>/<session>.jsonl and
// <session>/subagents/agent-<id>.jsonl).
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"agentlog/internal/jsonl"
	"agentlog/internal/transcript"
)

// Provider locates and parses Claude Code logs under Home (default ~/.claude).
type Provider struct {
	Home string
}

// New returns a provider rooted at $CLAUDE_CONFIG_DIR or ~/.claude.
func New() *Provider {
	home := os.Getenv("CLAUDE_CONFIG_DIR")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".claude")
		}
	}
	return &Provider{Home: home}
}

// Name implements the catalog provider contract.
func (p *Provider) Name() string { return transcript.Claude }

// Roots lists the directories scanned.
func (p *Provider) Roots() []string { return []string{filepath.Join(p.Home, "projects")} }

// SelfID is the session id of a Claude Code process running this command.
func (p *Provider) SelfID() string { return os.Getenv("CLAUDE_CODE_SESSION_ID") }

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Name        string `json:"name"`
	Model       string `json:"model"`
}

// Discover lists sessions and subagents modified at or after q.Since.
func (p *Provider) Discover(q transcript.Query) ([]*transcript.Session, error) {
	projects, err := os.ReadDir(p.Roots()[0])
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	live := p.liveSessions()
	var out []*transcript.Session
	// One session can have a log in more than one project folder: a move that
	// left the old file behind, or a copy carried on in the new directory.
	// Each is part of the session, so the window is applied to all of them
	// together and they are read as one.
	logs := map[string][]*transcript.Session{}
	var ids []string
	// A copy can bring the subagents/ folder along, so a subagent has a log
	// in each folder too.
	subs := map[string][]*transcript.Session{}
	var subIDs []string
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		dir := filepath.Join(p.Roots()[0], proj.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, ent := range entries {
			name := ent.Name()
			switch {
			case !ent.IsDir() && strings.HasSuffix(name, ".jsonl"):
				id := strings.TrimSuffix(name, ".jsonl")
				s := p.stat(filepath.Join(dir, name), id, transcript.Query{ID: q.ID, Now: q.Now})
				if s == nil {
					continue
				}
				s.CWD = decodeDir(proj.Name())
				if logs[id] == nil {
					ids = append(ids, id)
				}
				logs[id] = append(logs[id], s)
			case ent.IsDir():
				for _, sub := range p.subagents(filepath.Join(dir, name), name, q) {
					key := sub.ParentID + "/" + sub.ID
					if subs[key] == nil {
						subIDs = append(subIDs, key)
					}
					subs[key] = append(subs[key], sub)
				}
			}
		}
	}
	for _, key := range subIDs {
		out = append(out, merge(subs[key]))
	}
	for _, id := range ids {
		parts := logs[id]
		if !slices.ContainsFunc(parts, func(s *transcript.Session) bool { return !s.Updated.Before(q.Since) }) {
			continue
		}
		for _, s := range parts {
			folder := encodeDir(s.CWD)
			p.head(s)
			if encodeDir(s.CWD) != folder {
				moved(s, folder)
			}
		}
		s := merge(parts)
		s.Live = s.Live || live[id]
		out = append(out, s)
	}
	return out, nil
}

// merge makes one session of the logs one session id left in several project
// folders, oldest first, so the newest is the one File names. It is where the
// session is now, and most of what is known about it.
func merge(parts []*transcript.Session) *transcript.Session {
	if len(parts) == 1 {
		return parts[0]
	}
	sort.SliceStable(parts, func(i, j int) bool {
		a, b := parts[i], parts[j]
		if !a.Started.Equal(b.Started) && !a.Started.IsZero() && !b.Started.IsZero() {
			return a.Started.Before(b.Started)
		}
		return a.Updated.Before(b.Updated)
	})
	s := *parts[len(parts)-1]
	s.Files = nil
	for _, part := range parts {
		if part.CWD != "" && part.CWD != s.CWD && !slices.Contains(s.Dirs, part.CWD) {
			s.Dirs = append(s.Dirs, part.CWD)
		}
		s.Files = append(s.Files, part.Files...)
		s.Live = s.Live || part.Live
		if part.Updated.After(s.Updated) {
			s.Updated = part.Updated
		}
		if !part.Started.IsZero() && (s.Started.IsZero() || part.Started.Before(s.Started)) {
			s.Started = part.Started
		}
		if s.Title == "" {
			s.Title = part.Title
		}
		if s.Branch == "" {
			s.Branch = part.Branch
		}
		if s.Origin == "" {
			s.Origin = part.Origin
		}
	}
	s.Size = 0
	for _, part := range parts {
		s.Size += part.Size
	}
	return &s
}

func (p *Provider) subagents(sessionDir, parentID string, q transcript.Query) []*transcript.Session {
	subDir := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return nil
	}
	var out []*transcript.Session
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		path := filepath.Join(subDir, name)
		s := p.stat(path, id, q)
		if s == nil {
			continue
		}
		s.ParentID = parentID
		var m agentMeta
		if b, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); err == nil {
			_ = json.Unmarshal(b, &m)
		}
		s.AgentName = m.Name
		s.AgentRole = m.AgentType
		s.Title = m.Description
		s.Model = m.Model
		p.head(s)
		out = append(out, s)
	}
	return out
}

func (p *Provider) stat(path, id string, q transcript.Query) *transcript.Session {
	if !q.MatchID(id) {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil || st.ModTime().Before(q.Since) {
		return nil
	}
	return &transcript.Session{
		Provider: transcript.Claude,
		ID:       id,
		Files:    []string{path},
		Size:     st.Size(),
		Updated:  st.ModTime(),
		Live:     q.Live(st.ModTime()),
	}
}

// head reads cwd, branch, start time and titles from the start of the log.
func (p *Provider) head(s *transcript.Session) {
	cwdFromLog := false
	_ = jsonl.Head(s.File(), 256<<10, func(line []byte, _ int) error {
		var e struct {
			Type        string    `json:"type"`
			Timestamp   time.Time `json:"timestamp"`
			CWD         string    `json:"cwd"`
			GitBranch   string    `json:"gitBranch"`
			Entrypoint  string    `json:"entrypoint"`
			AITitle     string    `json:"aiTitle"`
			CustomTitle string    `json:"customTitle"`
		}
		if json.Unmarshal(line, &e) != nil {
			return nil
		}
		if s.Started.IsZero() && !e.Timestamp.IsZero() {
			s.Started = e.Timestamp
		}
		if e.CWD != "" && !cwdFromLog {
			s.CWD, cwdFromLog = e.CWD, true
		}
		if s.Branch == "" {
			s.Branch = e.GitBranch
		}
		if s.Origin == "" {
			s.Origin = e.Entrypoint
		}
		switch {
		case e.CustomTitle != "":
			s.Title = e.CustomTitle
		case e.AITitle != "" && s.Title == "":
			s.Title = e.AITitle
		}
		if !s.Started.IsZero() && cwdFromLog && s.Branch != "" && s.Title != "" {
			return jsonl.ErrStop
		}
		return nil
	})
	// The first prompt carries its pasted images inline, so it can be larger
	// than the window above, and the bookkeeping records before it carry no
	// time. Read on until something does.
	if s.Started.IsZero() {
		_ = jsonl.Head(s.File(), 16<<20, func(line []byte, _ int) error {
			var e struct {
				Timestamp time.Time `json:"timestamp"`
			}
			if json.Unmarshal(line, &e) == nil && !e.Timestamp.IsZero() {
				s.Started = e.Timestamp
				return jsonl.ErrStop
			}
			return nil
		})
	}
}

// moved finds where a session went on to work. Claude Code files a log under
// the directory it is working in, so a log whose first cwd does not match
// its folder moved (a "relocated" record, or a cd the harness followed), and
// a search for the project it was moved to should find it. The tail says
// where it ended up; the head says only where it began.
func moved(s *transcript.Session, folder string) {
	_ = jsonl.Tail(s.File(), 1<<20, func(line []byte, _ int) error {
		var e struct {
			CWD       string `json:"cwd"`
			Relocated string `json:"relocatedCwd"`
		}
		if json.Unmarshal(line, &e) != nil {
			return nil
		}
		for _, d := range []string{e.Relocated, e.CWD} {
			if d != "" && encodeDir(d) == folder {
				s.CWD = d
			}
		}
		return nil
	})
}

// encodeDir is how Claude Code names a project's folder: every character
// that is not a letter or a digit becomes "-".
func encodeDir(path string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, path)
}

// decodeDir turns "-Users-me-proj" back into a best-effort path. It is lossy
// (hyphens and dots also become "-"); the cwd field in the log wins.
func decodeDir(name string) string {
	return strings.ReplaceAll(name, "-", "/")
}

// liveSessions maps session ids of running Claude Code processes to whether
// they report being busy, from ~/.claude/sessions/<pid>.json.
func (p *Provider) liveSessions() map[string]bool {
	out := map[string]bool{}
	dir := filepath.Join(p.Home, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, ent := range entries {
		if !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(ent.Name(), ".json"))
		if err != nil || syscall.Kill(pid, 0) != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		var m struct {
			SessionID string `json:"sessionId"`
			Status    string `json:"status"`
		}
		if json.Unmarshal(b, &m) == nil && m.SessionID != "" {
			out[m.SessionID] = m.Status == "busy"
		}
	}
	return out
}
