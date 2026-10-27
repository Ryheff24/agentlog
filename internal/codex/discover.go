// Package codex reads OpenAI Codex rollout logs
// (~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<thread>[_<page>].jsonl).
package codex

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/jsonl"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Provider locates and parses Codex logs under Home (default ~/.codex).
type Provider struct {
	Home string
}

// New returns a provider rooted at $CODEX_HOME or ~/.codex.
func New() *Provider {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		}
	}
	return &Provider{Home: home}
}

// Name implements the catalog provider contract.
func (p *Provider) Name() string { return transcript.Codex }

// Roots lists the directories scanned.
func (p *Provider) Roots() []string {
	return []string{filepath.Join(p.Home, "sessions"), filepath.Join(p.Home, "archived_sessions")}
}

// SelfID is the thread id of a Codex process running this command.
func (p *Provider) SelfID() string { return os.Getenv("CODEX_THREAD_ID") }

type sessionMeta struct {
	ID            string          `json:"id"`
	Timestamp     time.Time       `json:"timestamp"`
	CWD           string          `json:"cwd"`
	Originator    string          `json:"originator"`
	CLIVersion    string          `json:"cli_version"`
	Source        json.RawMessage `json:"source"`
	ThreadSource  string          `json:"thread_source"`
	AgentNickname string          `json:"agent_nickname"`
	AgentRole     string          `json:"agent_role"`
	AgentPath     string          `json:"agent_path"`
	ForkedFromID  string          `json:"forked_from_id"`
	Git           struct {
		CommitHash string `json:"commit_hash"`
		SHA        string `json:"sha"`
		Branch     string `json:"branch"`
	} `json:"git"`
	HistoryBase *struct {
		ThreadID            string `json:"thread_id"`
		EndOrdinalExclusive int64  `json:"end_ordinal_exclusive"`
	} `json:"history_base"`
}

type sourceInfo struct {
	parentID string
	nickname string
	role     string
	kind     string // "", "subagent", "guardian", "review"
}

func parseSource(raw json.RawMessage, m *sessionMeta) sourceInfo {
	var si sourceInfo
	var obj struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &obj) != nil || len(obj.Subagent) == 0 {
		if m.ThreadSource == "guardian_review" {
			si.kind = "guardian"
		}
		return si
	}
	var name string
	if json.Unmarshal(obj.Subagent, &name) == nil {
		si.kind, si.role = "review", name
		return si
	}
	var sub struct {
		ThreadSpawn *struct {
			ParentThreadID string `json:"parent_thread_id"`
			AgentPath      string `json:"agent_path"`
			AgentNickname  string `json:"agent_nickname"`
			AgentRole      string `json:"agent_role"`
		} `json:"thread_spawn"`
		Other string `json:"other"`
	}
	_ = json.Unmarshal(obj.Subagent, &sub)
	switch {
	case sub.ThreadSpawn != nil:
		si.kind = "subagent"
		si.parentID = sub.ThreadSpawn.ParentThreadID
		si.nickname = sub.ThreadSpawn.AgentNickname
		si.role = firstNonEmpty(sub.ThreadSpawn.AgentPath, sub.ThreadSpawn.AgentRole)
	case sub.Other != "":
		si.kind, si.role = sub.Other, sub.Other
	default:
		si.kind = "subagent"
	}
	return si
}

// page is one rollout file of a thread.
type page struct {
	path    string
	pageID  string
	meta    sessionMeta
	src     sourceInfo
	size    int64
	modTime time.Time
}

// Discover lists threads with any page modified at or after q.Since. All pages
// of a matching thread are included, however old.
func (p *Provider) Discover(q transcript.Query) ([]*transcript.Session, error) {
	files := map[string][]string{} // thread id (from file name) -> pages
	for _, root := range p.Roots() {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				return nil
			}
			if tid := threadIDFromName(name); q.MatchID(tid) {
				files[tid] = append(files[tid], path)
			}
			return nil
		})
	}

	var pages []*page
	for _, paths := range files {
		var group []*page
		recent := false
		for _, f := range paths {
			st, err := os.Stat(f)
			if err != nil {
				continue
			}
			recent = recent || !st.ModTime().Before(q.Since)
			group = append(group, &page{path: f, size: st.Size(), modTime: st.ModTime(), pageID: pageIDFromName(filepath.Base(f))})
		}
		if !recent {
			continue
		}
		for _, pg := range group {
			if !readMeta(pg.path, &pg.meta) {
				continue
			}
			pg.src = parseSource(pg.meta.Source, &pg.meta)
			pages = append(pages, pg)
		}
	}
	ss := groupThreads(pages, p.titles())
	for _, s := range ss {
		s.Live = q.Live(s.Updated)
	}
	return ss, nil
}

// threadPages re-reads page headers for loading.
func threadPages(s *transcript.Session) []*page {
	var out []*page
	for _, f := range s.Files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		pg := &page{path: f, size: st.Size(), modTime: st.ModTime(), pageID: pageIDFromName(filepath.Base(f))}
		if readMeta(f, &pg.meta) {
			pg.src = parseSource(pg.meta.Source, &pg.meta)
			out = append(out, pg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].meta.Timestamp.Before(out[j].meta.Timestamp) })
	return out
}

// threadIDFromName extracts <thread> from rollout-<ts>-<thread>[_<page>].jsonl.
func threadIDFromName(name string) string {
	name = strings.TrimSuffix(name, ".jsonl")
	if i := strings.IndexByte(name, '_'); i >= 0 {
		name = name[:i]
	}
	if len(name) >= 36 {
		return name[len(name)-36:]
	}
	return name
}

func readMeta(path string, m *sessionMeta) bool {
	ok := false
	_ = jsonl.Each(path, func(line []byte, n int) error {
		var rec struct {
			Type    string      `json:"type"`
			Payload sessionMeta `json:"payload"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Type == "session_meta" {
			*m = rec.Payload
			ok = m.ID != ""
		}
		return jsonl.ErrStop
	})
	return ok
}

// pageIDFromName extracts <page> from rollout-<ts>-<thread>_<page>.jsonl, or
// the thread id when the file is the thread's first page.
func pageIDFromName(name string) string {
	name = strings.TrimSuffix(name, ".jsonl")
	if i := strings.LastIndexByte(name, '_'); i >= 0 {
		return name[i+1:]
	}
	if len(name) >= 36 {
		return name[len(name)-36:]
	}
	return name
}

func groupThreads(pages []*page, titles map[string]string) []*transcript.Session {
	byThread := map[string][]*page{}
	for _, pg := range pages {
		byThread[pg.meta.ID] = append(byThread[pg.meta.ID], pg)
	}
	out := make([]*transcript.Session, 0, len(byThread))
	for id, pgs := range byThread {
		sort.Slice(pgs, func(i, j int) bool { return pgs[i].meta.Timestamp.Before(pgs[j].meta.Timestamp) })
		first := pgs[0]
		s := &transcript.Session{
			Provider:  transcript.Codex,
			ID:        id,
			CWD:       strings.TrimPrefix(first.meta.CWD, "file://"),
			Title:     titles[id],
			Started:   first.meta.Timestamp,
			Branch:    first.meta.Git.Branch,
			Commit:    firstNonEmpty(first.meta.Git.CommitHash, first.meta.Git.SHA),
			Origin:    first.meta.Originator,
			ParentID:  first.src.parentID,
			AgentName: firstNonEmpty(first.src.nickname, first.meta.AgentNickname),
			AgentRole: firstNonEmpty(first.src.role, first.meta.AgentPath, first.meta.AgentRole),
		}
		if first.src.kind == "guardian" {
			s.AgentRole = "guardian"
		}
		for _, pg := range pgs {
			s.Files = append(s.Files, pg.path)
			s.Size += pg.size
			if pg.modTime.After(s.Updated) {
				s.Updated = pg.modTime
			}
		}
		out = append(out, s)
	}
	return out
}

// titles reads ~/.codex/session_index.jsonl (thread id -> latest name).
func (p *Provider) titles() map[string]string {
	m := map[string]string{}
	_ = jsonl.Each(filepath.Join(p.Home, "session_index.jsonl"), func(line []byte, _ int) error {
		var r struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &r) == nil && r.ID != "" && r.ThreadName != "" {
			m[r.ID] = r.ThreadName
		}
		return nil
	})
	return m
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
