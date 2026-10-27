// Package catalog discovers sessions across providers, links subagents to
// their parents, and ranks sessions against the clues an agent supplies.
package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ryheff24/agentlog/internal/monitor"
	"github.com/Ryheff24/agentlog/internal/transcript"
	"github.com/Ryheff24/agentlog/internal/work"
)

// Provider is one agent harness's log reader.
type Provider interface {
	Name() string
	Roots() []string
	SelfID() string
	Discover(q transcript.Query) ([]*transcript.Session, error)
	Load(s *transcript.Session, o transcript.Load) (*transcript.Transcript, error)
}

// Catalog answers lookups over a set of providers.
type Catalog struct {
	Providers []Provider
	CWD       string
	Now       time.Time
}

// DefaultWindow is how far back an unscoped search looks before widening.
const DefaultWindow = 7 * 24 * time.Hour

// Harness describes one provider for the home screen.
type Harness struct {
	Name     string    `json:"name"`
	Roots    []string  `json:"roots"`
	Present  bool      `json:"present"`
	Sessions int       `json:"recent_sessions"`
	Latest   time.Time `json:"latest,omitempty"`
}

// Match is a ranked candidate.
type Match struct {
	Session *transcript.Session
	Score   int
	Why     []string // which clues matched, for the agent to judge confidence
}

// Tied is how many matches score exactly as well as the best one. A clue that
// names a particular session — an id prefix, a word — and leaves several
// sessions level has not picked one, and saying which of them is "best" would
// be a guess dressed as an answer.
func (r *Result) Tied() int {
	if len(r.Matches) == 0 {
		return 0
	}
	n := 0
	for _, m := range r.Matches {
		if m.Score != r.Matches[0].Score {
			break
		}
		n++
	}
	return n
}

// Result is a completed lookup.
type Result struct {
	Clues      Clues
	Matches    []Match // best first; top-level sessions carry Children
	Harnesses  []Harness
	Since      time.Time
	Widened    bool                // default window found nothing, so all time was searched
	HiddenSelf bool                // the calling agent's own session was left out
	Own        *transcript.Session // that session, so an empty result can name it

	// PromptsUnread counts candidates whose prompts were not read for word
	// matching, because only the newest deepLimit are.
	PromptsUnread int

	// Blank holds sessions taken out of Matches by EnrichTop because
	// nothing was said in them: opened and closed, or only /clear.
	Blank []*transcript.Session
}

// EnrichTop enriches the first n matches and takes out the blank ones,
// enriching more in their place, so a listing of n rows is n sessions in
// which something happened. A session left open and cleared is still the
// newest in its directory, and listed, it reads as work done there.
func (c *Catalog) EnrichTop(ctx context.Context, res *Result, n int) {
	for {
		top := res.Matches[:min(n, len(res.Matches))]
		ss := make([]*transcript.Session, len(top))
		for i, m := range top {
			ss[i] = m.Session
		}
		c.Enrich(ctx, ss)
		kept := make([]Match, 0, len(res.Matches))
		for _, m := range top {
			if blank(m.Session) {
				res.Blank = append(res.Blank, m.Session)
			} else {
				kept = append(kept, m)
			}
		}
		if len(kept) == len(top) || ctx.Err() != nil {
			return
		}
		res.Matches = append(kept, res.Matches[len(top):]...)
	}
}

// blank reports an enriched session nobody asked anything in: opened and
// cleared, or a tool's self-test that only ran a shell command, whatever it
// replied. A log that could not be read is not known to be blank.
func blank(s *transcript.Session) bool {
	return s.FirstPrompt == "" && s.LastPrompt == "" && s.Title == "" && !s.Unreadable &&
		len(s.Children) == 0 && !s.Live && !s.Self && s.ParentID == ""
}

func (c *Catalog) provider(name string) Provider {
	for _, p := range c.Providers {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// Find runs discovery and ranks sessions against clues.
func (c *Catalog) Find(ctx context.Context, cl Clues) (*Result, error) {
	res := &Result{Clues: cl}
	since := cl.Since
	if since.IsZero() && !cl.AllTime && len(cl.IDs) == 0 {
		since = c.Now.Add(-DefaultWindow)
	}
	done := monitor.Phase("discover")
	all, err := c.discover(cl, since, res, true)
	done()
	if err != nil {
		return nil, err
	}
	res.Since = since
	res.Matches = c.rankDeep(ctx, all, cl, res)
	if len(res.Matches) == 0 && cl.Since.IsZero() && !cl.AllTime && !since.IsZero() {
		done := monitor.Phase("discover")
		all, err = c.discover(cl, time.Time{}, res, true)
		done()
		if err != nil {
			return nil, err
		}
		res.Since, res.Widened = time.Time{}, true
		res.Matches = c.rankDeep(ctx, all, cl, res)
	}
	return res, nil
}

// deepLimit bounds how many sessions get their prompts read when cheap
// metadata did not match the words.
const deepLimit = 60

// rankDeep ranks on cheap metadata first; when words match nothing, it reads
// prompts from the newest candidates and ranks again.
func (c *Catalog) rankDeep(ctx context.Context, all []*transcript.Session, cl Clues, res *Result) []Match {
	res.PromptsUnread = 0
	matches := c.rank(all, cl, res)
	if len(cl.Words) == 0 || (!cl.ReadPrompts && len(matches) > 0 && wordHits(matches[0]) == len(cl.Words)) {
		return matches
	}
	words := cl.Words
	cl.Words = nil
	pool := c.rank(all, cl, res) // every session passing the non-word clues
	cl.Words = words
	sort.Slice(pool, func(i, j int) bool { return pool[i].Session.Updated.After(pool[j].Session.Updated) })
	if len(pool) > deepLimit {
		res.PromptsUnread = len(pool) - deepLimit
		pool = pool[:deepLimit]
	}
	var ss []*transcript.Session
	for _, m := range pool {
		ss = append(ss, m.Session)
		ss = append(ss, m.Session.Children...)
	}
	c.Enrich(ctx, ss)
	return c.rank(all, cl, res)
}

func wordHits(m Match) int {
	n := 0
	for _, why := range m.Why {
		if strings.Contains(why, "∈") {
			n++
		}
	}
	return n
}

// Enrich fills first/last prompt, last message and ending from cheap head and
// tail windows. Sessions already enriched are skipped.
func (c *Catalog) Enrich(ctx context.Context, ss []*transcript.Session) {
	defer monitor.Phase("enrich")()
	var todo []*transcript.Session
	queued := map[*transcript.Session]bool{}
	for _, s := range ss {
		// A session can be handed over twice (a subagent named by an id
		// prefix is both a match and its parent's child); reading it twice
		// at once would be two writers on one session.
		if s.Ending != "" || s.FirstPrompt != "" || s.Unreadable || queued[s] {
			continue
		}
		queued[s] = true
		if c.provider(s.Provider) != nil {
			todo = append(todo, s)
		}
	}
	work.Do(ctx, len(todo), func(i int) {
		s := todo[i]
		p := c.provider(s.Provider)
		head, headErr := p.Load(s, transcript.Load{Window: transcript.Window{HeadBytes: 512 << 10}})
		if headErr == nil {
			for _, e := range head.Events {
				if e.Kind == transcript.KindUser {
					s.FirstPrompt = e.Text
					break
				}
			}
			if s.Title == "" {
				s.Title = head.Session.Title
			}
			if s.Model == "" {
				s.Model = head.Session.Model
			}
		}
		tail, err := p.Load(s, transcript.Load{Window: transcript.Window{TailBytes: 768 << 10}})
		if err != nil {
			s.Unreadable = headErr != nil
			return
		}
		for j := len(tail.Events) - 1; j >= 0; j-- {
			e := tail.Events[j]
			if s.LastPrompt == "" && e.Kind == transcript.KindUser {
				s.LastPrompt = e.Text
			}
			if s.LastSay == "" && e.Kind == transcript.KindAssistant {
				s.LastSay = e.Text
			}
		}
		s.LastEvent = transcript.Newest(tail.Events)
		s.Ending = tail.Session.Ending
		s.ContinuedIn = tail.Session.ContinuedIn
		if tail.Session.Title != "" {
			s.Title = tail.Session.Title
		}
		if tail.Session.Model != "" {
			s.Model = tail.Session.Model
		}
	})
}

// discover lists the sessions written since since; with kids, it also
// fetches the earlier subagents of the sessions it found.
func (c *Catalog) discover(cl Clues, since time.Time, res *Result, kids bool) ([]*transcript.Session, error) {
	wanted := map[string]bool{}
	for _, p := range cl.Providers {
		wanted[p] = true
	}
	queries := []transcript.Query{{Since: since, Now: c.Now}}
	if len(cl.IDs) > 0 {
		queries = queries[:0]
		for _, id := range cl.IDs {
			queries = append(queries, transcript.Query{ID: id, Now: c.Now})
		}
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		all      []*transcript.Session
		firstErr error
	)
	res.Harnesses = res.Harnesses[:0]
	harness := make([]Harness, len(c.Providers))
	for i, p := range c.Providers {
		harness[i] = Harness{Name: p.Name(), Roots: p.Roots()}
		for _, r := range p.Roots() {
			if _, err := os.Stat(r); err == nil {
				harness[i].Present = true
			}
		}
		if len(wanted) > 0 && !wanted[p.Name()] {
			continue
		}
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			for _, q := range queries {
				ss, err := p.Discover(q)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("discover %s: %w", p.Name(), err)
				}
				self := p.SelfID()
				for _, s := range ss {
					s.Self = self != "" && s.ID == self
					// Count what a person could open: the harness's own runs and
					// the work of subagents are reached through their parent, and
					// counting them makes the harness look busier than it was.
					if s.ParentID == "" && !sideRun(s) {
						harness[i].Sessions++
						if s.Updated.After(harness[i].Latest) {
							harness[i].Latest = s.Updated
						}
					}
				}
				all = append(all, ss...)
				mu.Unlock()
			}
		}(i, p)
	}
	wg.Wait()
	res.Harnesses = harness

	// Subagents are linked from their own logs. An id lookup finds only the
	// named session, and a window finds only the subagents written in it,
	// though a session that ran for a week had subagents all through it. So
	// scan once more, from the earliest start, for the rest of them.
	if !kids || len(all) == 0 {
		return all, firstErr
	}
	var earliest time.Time
	seen := map[string]bool{}
	for _, s := range all {
		seen[s.Provider+":"+s.ID] = true
		if !s.Started.IsZero() && (earliest.IsZero() || s.Started.Before(earliest)) {
			earliest = s.Started
		}
	}
	if len(cl.IDs) == 0 && (earliest.IsZero() || !earliest.Before(since)) {
		return all, firstErr
	}
	idless := cl
	idless.IDs = nil
	if !earliest.IsZero() {
		earliest = earliest.Add(-time.Minute)
	}
	more, err := c.discover(idless, earliest, &Result{}, false)
	if err != nil && firstErr == nil {
		firstErr = err
	}
	// Subagents nest, and a grandchild can come before its parent.
	for added := true; added; {
		added = false
		for _, s := range more {
			if s.ParentID != "" && seen[s.Provider+":"+s.ParentID] && !seen[s.Provider+":"+s.ID] {
				seen[s.Provider+":"+s.ID] = true
				all = append(all, s)
				added = true
			}
		}
	}
	return all, firstErr
}

// rank links children, filters by clues, and scores what is left.
func (c *Catalog) rank(all []*transcript.Session, cl Clues, res *Result) []Match {
	byID := map[string]*transcript.Session{}
	for _, s := range all {
		s.Children = nil
		byID[s.Provider+":"+s.ID] = s
	}
	for _, s := range all {
		if s.ParentID == "" {
			continue
		}
		if parent := byID[s.Provider+":"+s.ParentID]; parent != nil {
			parent.Children = append(parent.Children, s)
		}
	}
	for _, s := range all {
		sort.Slice(s.Children, func(i, j int) bool { return s.Children[i].Started.Before(s.Children[j].Started) })
	}

	var out []Match
	for _, s := range all {
		m := Match{Session: s}
		// A named day is a window, not a floor: the session has to have been
		// running during it. Logs without a start time are placed by their
		// last write, which is all that is known about them.
		if !cl.Until.IsZero() {
			start := s.Started
			if start.IsZero() {
				start = s.Updated
			}
			if !start.Before(cl.Until) || s.Updated.Before(cl.Since) {
				continue
			}
		}
		if !c.matchIDs(s, cl, &m) || !c.matchFiles(s, cl, &m) || !c.matchDirs(s, cl, &m) {
			continue
		}
		words := c.matchWords(s, cl, &m)
		if len(cl.Words) > 0 && words == 0 {
			continue
		}
		// A subagent fetched for its parent is outside the window asked for.
		if len(cl.IDs) == 0 && !res.Since.IsZero() && s.Updated.Before(res.Since) {
			continue
		}
		// doctor asks whether the logs can be read, and the caller's log
		// is as good a sample as any.
		if s.Self && len(cl.IDs) == 0 && !cl.Doctor {
			res.HiddenSelf, res.Own = true, s
			continue
		}
		// Subagents only surface on their own when a clue points at them;
		// otherwise they are reached through their parent.
		if s.ParentID != "" && byID[s.Provider+":"+s.ParentID] != nil && len(cl.IDs) == 0 && words == 0 && len(cl.Files) == 0 {
			continue
		}
		if sideRun(s) && len(cl.IDs) == 0 {
			continue
		}
		m.Score += words * 10
		m.Score += c.affinity(s)
		if s.Live {
			m.Score += 3
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Session.Updated.After(out[j].Session.Updated)
	})
	return out
}

// sideRun reports a session the harness started for itself — a guardian
// watching a run, a review pass — rather than one a person asked for. They are
// reachable by id and stay out of counts and listings otherwise.
func sideRun(s *transcript.Session) bool {
	return s.AgentRole == "guardian" || s.AgentRole == "review"
}

func (c *Catalog) matchIDs(s *transcript.Session, cl Clues, m *Match) bool {
	if len(cl.IDs) == 0 {
		return true
	}
	for _, id := range cl.IDs {
		if strings.HasPrefix(s.ID, id) {
			m.Score += 100
			m.Why = append(m.Why, "id")
			return true
		}
	}
	return false
}

func (c *Catalog) matchFiles(s *transcript.Session, cl Clues, m *Match) bool {
	if len(cl.Files) == 0 {
		return true
	}
	for _, f := range cl.Files {
		for _, sf := range s.Files {
			if sf == f {
				m.Score += 100
				m.Why = append(m.Why, "file")
				return true
			}
		}
	}
	return false
}

func (c *Catalog) matchDirs(s *transcript.Session, cl Clues, m *Match) bool {
	if len(cl.Dirs) == 0 {
		return true
	}
	for _, d := range cl.Dirs {
		for _, cwd := range dirs(s) {
			switch {
			case cwd == d:
				m.Score += 20
				m.Why = append(m.Why, "dir")
				return true
			case within(cwd, d):
				m.Score += 15
				m.Why = append(m.Why, "dir (subdir)")
				return true
			case within(d, cwd):
				m.Score += 10
				m.Why = append(m.Why, "dir (parent)")
				return true
			}
		}
	}
	return false
}

// dirs are the directories a session worked in, current first.
func dirs(s *transcript.Session) []string {
	return append([]string{s.CWD}, s.Dirs...)
}

// matchWords counts words found in the session's identifying text.
func (c *Catalog) matchWords(s *transcript.Session, cl Clues, m *Match) int {
	if len(cl.Words) == 0 {
		return 0
	}
	type field struct{ name, text string }
	fields := []field{
		{"title", strings.ToLower(s.Title)},
		{"agent", strings.ToLower(s.AgentName + " " + s.AgentRole)},
		{"id", strings.ToLower(s.ID)},
		{"prompt", strings.ToLower(s.FirstPrompt + "\n" + s.LastPrompt)},
		{"reply", strings.ToLower(s.LastSay)},
	}
	// A subagent shares its parent's directory; only its own identity should
	// pull it out from under the parent.
	if s.ParentID == "" {
		fields = append([]field{
			{"project", strings.ToLower(filepath.Base(s.CWD))},
			{"path", strings.ToLower(s.CWD)},
			{"branch", strings.ToLower(s.Branch)},
		}, fields...)
	}
	n := 0
	for _, w := range cl.Words {
		for _, f := range fields {
			if f.text != "" && strings.Contains(f.text, w) {
				n++
				m.Why = append(m.Why, w+"∈"+f.name)
				break
			}
		}
	}
	return n
}

// affinity favors sessions related to the caller's working directory.
func (c *Catalog) affinity(s *transcript.Session) int {
	best := 0
	for _, cwd := range dirs(s) {
		switch {
		case c.CWD == "" || cwd == "":
		case cwd == c.CWD:
			best = max(best, 6)
		case within(cwd, c.CWD), within(c.CWD, cwd):
			best = max(best, 3)
		}
	}
	return best
}

func within(path, dir string) bool {
	if path == "" || dir == "" || dir == "/" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	// "..cache" is a directory inside dir, not a way out of it.
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Load parses a session, honouring the window and branch options.
func (c *Catalog) Load(s *transcript.Session, o transcript.Load) (*transcript.Transcript, error) {
	p := c.provider(s.Provider)
	if p == nil {
		return nil, fmt.Errorf("no provider %q", s.Provider)
	}
	// Loads run in parallel, so this phase adds up the time of each.
	defer monitor.Phase("load")()
	return p.Load(s, o)
}
