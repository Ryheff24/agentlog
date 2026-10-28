// Package codex reads OpenAI Codex rollout logs
// (~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<thread>[_<page>].jsonl). Codex
// compresses a rollout to .jsonl.zst after about a week; both are read.
package codex

import (
	"encoding/json"
	"fmt"
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
			if !strings.HasPrefix(name, "rollout-") || !jsonl.IsLog(name) {
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
		for _, f := range onePerPage(paths) {
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
			ok, err := readMeta(pg.path, &pg.meta)
			switch {
			case err != nil:
				// The log is there but can't be read. Listing it from its
				// name keeps it in sight: opening it reports the error, and
				// doctor counts it, instead of the thread silently missing.
				pg.meta = sessionMeta{ID: threadIDFromName(filepath.Base(pg.path)), Timestamp: startFromName(filepath.Base(pg.path), pg.modTime)}
			case !ok:
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

// threadPages re-reads page headers for loading. failed holds an error for
// each page whose log could not be read.
func threadPages(s *transcript.Session) (out []*page, failed []error) {
	for _, f := range s.Files {
		st, err := os.Stat(f)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		pg := &page{path: f, size: st.Size(), modTime: st.ModTime(), pageID: pageIDFromName(filepath.Base(f))}
		ok, err := readMeta(f, &pg.meta)
		switch {
		case err != nil:
			failed = append(failed, fmt.Errorf("%s: %w", f, err))
		case ok:
			pg.src = parseSource(pg.meta.Source, &pg.meta)
			out = append(out, pg)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].meta.Timestamp.Before(out[j].meta.Timestamp) })
	return out, failed
}

// reasons formats why pages could not be read, for an error that has to say.
func reasons(failed []error) string {
	var b strings.Builder
	for _, err := range failed {
		b.WriteString("; ")
		b.WriteString(err.Error())
	}
	return b.String()
}

// onePerPage drops a compressed page whose plain copy is also there, as it is
// for a moment while Codex compresses it, so no page is read twice. The plain
// copy is the one Codex was still writing.
func onePerPage(paths []string) []string {
	plain := map[string]bool{}
	for _, p := range paths {
		if !strings.HasSuffix(p, jsonl.Compressed) {
			plain[p] = true
		}
	}
	out := paths[:0:0]
	for _, p := range paths {
		if !plain[strings.TrimSuffix(p, jsonl.Compressed)] || !strings.HasSuffix(p, jsonl.Compressed) {
			out = append(out, p)
		}
	}
	return out
}

// startFromName reads <ts> from rollout-<ts>-<thread>.jsonl, the local time
// the thread started, or returns fallback.
func startFromName(name string, fallback time.Time) time.Time {
	ts, ok := strings.CutPrefix(name, "rollout-")
	if !ok || len(ts) < len("2006-01-02T15-04-05") {
		return fallback
	}
	t, err := time.ParseInLocation("2006-01-02T15-04-05", ts[:len("2006-01-02T15-04-05")], time.Local)
	if err != nil {
		return fallback
	}
	return t
}

// threadIDFromName extracts <thread> from rollout-<ts>-<thread>[_<page>].jsonl.
func threadIDFromName(name string) string {
	name = jsonl.TrimLog(name)
	if i := strings.IndexByte(name, '_'); i >= 0 {
		name = name[:i]
	}
	if len(name) >= 36 {
		return name[len(name)-36:]
	}
	return name
}

// readMeta reads a page's session_meta header. ok is false for a log that has
// none yet; err is set for one that could not be read.
func readMeta(path string, m *sessionMeta) (ok bool, err error) {
	err = jsonl.Each(path, func(line []byte, n int) error {
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
	return ok, err
}

// pageIDFromName extracts <page> from rollout-<ts>-<thread>_<page>.jsonl, or
// the thread id when the file is the thread's first page.
func pageIDFromName(name string) string {
	name = jsonl.TrimLog(name)
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
