package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

type fakeProvider struct {
	name     string
	self     string
	roots    []string
	sessions []*transcript.Session
	err      error // what Discover fails with

	// heads and tails are what a head or tail window of a session's log
	// reads, by session id; a session with neither has no readable log.
	heads, tails map[string]*transcript.Transcript
	mu           sync.Mutex
	loads        map[string]int
}

func (f *fakeProvider) Name() string    { return f.name }
func (f *fakeProvider) Roots() []string { return f.roots }
func (f *fakeProvider) SelfID() string  { return f.self }

func (f *fakeProvider) Discover(q transcript.Query) ([]*transcript.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []*transcript.Session
	for _, s := range f.sessions {
		if q.MatchID(s.ID) && !s.Updated.Before(q.Since) {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeProvider) Load(s *transcript.Session, o transcript.Load) (*transcript.Transcript, error) {
	f.mu.Lock()
	if f.loads == nil {
		f.loads = map[string]int{}
	}
	f.loads[s.ID]++
	f.mu.Unlock()
	src := f.tails
	if o.Window.HeadBytes > 0 {
		src = f.heads
	}
	if t := src[s.ID]; t != nil {
		cp := *t
		return &cp, nil
	}
	return nil, errors.New("no log")
}

func ids(ms []Match) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Session.ID)
	}
	return strings.Join(out, " ")
}

func newCatalog(now time.Time) *Catalog {
	sess := func(provider, id, cwd string, age time.Duration) *transcript.Session {
		return &transcript.Session{Provider: provider, ID: id, CWD: cwd, Started: now.Add(-age - time.Hour), Updated: now.Add(-age)}
	}
	parent := sess(transcript.Codex, "01a0a5c3-7e10-7d24-9f31-6b28c40e1a95", "/work/video/player", 2*time.Hour)
	child := sess(transcript.Codex, "01a0ab27-c514-7000-8000-000000000001", "/work/video/player", 2*time.Hour)
	child.ParentID, child.AgentName, child.AgentRole = parent.ID, "Hooke", "/root/regression_fix"
	guardian := sess(transcript.Codex, "01a0ab28-0000-7000-8000-000000000002", "/work/video/player", 2*time.Hour)
	guardian.ParentID, guardian.AgentRole = parent.ID, "guardian"
	return &Catalog{
		CWD: "/work/video/player",
		Now: now,
		Providers: []Provider{
			&fakeProvider{name: transcript.Codex, sessions: []*transcript.Session{
				parent, child, guardian,
				sess(transcript.Codex, "01a09000-0000-7000-8000-000000000003", "/work/shop", time.Hour),
				sess(transcript.Codex, "01a01000-0000-7000-8000-000000000004", "/work/video/player", 30*24*time.Hour),
			}},
			&fakeProvider{name: transcript.Claude, self: "3f2a91bc-1111-4222-8333-444455556666", sessions: []*transcript.Session{
				sess(transcript.Claude, "3f2a91bc-1111-4222-8333-444455556666", "/work/video/player", 0),
				sess(transcript.Claude, "77aa0000-1111-4222-8333-444455556666", "/work/video/player/web", 3*time.Hour),
			}},
		},
	}
}

func TestFindByDirAndProvider(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)

	res, err := c.Find(context.Background(), ParseClues([]string{"codex", "."}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	// Subagents, guardians and sessions older than the default window stay out.
	if got := ids(res.Matches); got != "01a0a5c3-7e10-7d24-9f31-6b28c40e1a95" {
		t.Fatalf("matches = %q", got)
	}
	kids := res.Matches[0].Session.Children
	if len(kids) != 2 {
		t.Errorf("parent children = %d, want 2", len(kids))
	}
}

func TestFindHidesSelfAndRanksNearbyDirs(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)

	res, err := c.Find(context.Background(), ParseClues([]string{"claude"}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Matches); got != "77aa0000-1111-4222-8333-444455556666" || !res.HiddenSelf {
		t.Errorf("matches = %q hiddenSelf = %v", got, res.HiddenSelf)
	}
}

func TestFindByIDReachesSubagentAndWidens(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)

	res, err := c.Find(context.Background(), ParseClues([]string{"01a0ab27-c514#85"}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || res.Matches[0].Session.AgentName != "Hooke" || res.Clues.Event != 85 {
		t.Fatalf("matches = %q event %d", ids(res.Matches), res.Clues.Event)
	}

	// Two months on, nothing is inside the default window, so all time is searched.
	c.Now = now.Add(60 * 24 * time.Hour)
	res, err = c.Find(context.Background(), ParseClues([]string{"codex", "."}, c.CWD, c.Now))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Widened || len(res.Matches) != 2 {
		t.Errorf("widened = %v matches = %q", res.Widened, ids(res.Matches))
	}
}

func TestFindWordsMatchAgentNames(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)

	res, err := c.Find(context.Background(), ParseClues([]string{"hooke"}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) == 0 || res.Matches[0].Session.AgentName != "Hooke" {
		t.Fatalf("matches = %q", ids(res.Matches))
	}
}

func TestFindOnADayKeepsSessionsThatWereRunning(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)
	// A session that opened before the named day and was still being written
	// after it was running on that day.
	spanning := &transcript.Session{Provider: transcript.Claude, ID: "5ba10000-1111-4222-8333-444455556666",
		CWD: "/work/video/player", Started: now.AddDate(0, 0, -4), Updated: now}
	c.Providers[1].(*fakeProvider).sessions = append(c.Providers[1].(*fakeProvider).sessions, spanning)

	onDay := func(day string) string {
		t.Helper()
		res, err := c.Find(context.Background(), ParseClues([]string{"claude", day}, c.CWD, now))
		if err != nil {
			t.Fatal(err)
		}
		if res.Widened {
			t.Errorf("%s widened the window", day)
		}
		return ids(res.Matches)
	}
	if got := onDay("2026-09-14"); got != spanning.ID {
		t.Errorf("on 2026-09-14 = %q, want the session that was open across it", got)
	}
	// Nothing had started yet two days before it opened.
	if got := onDay("2026-09-10"); got != "" {
		t.Errorf("on 2026-09-10 = %q, want nothing", got)
	}
}

func TestFindCountsOnlySessionsAPersonStarted(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)
	// Codex logs a guardian as a top-level thread with no parent id, so only
	// its role tells it apart from a session someone opened.
	watcher := &transcript.Session{Provider: transcript.Codex, ID: "01a0b000-0000-7000-8000-000000000005",
		CWD: "/work/video/player", AgentRole: "guardian", Started: now.Add(-2 * time.Hour), Updated: now.Add(-time.Hour)}
	c.Providers[0].(*fakeProvider).sessions = append(c.Providers[0].(*fakeProvider).sessions, watcher)

	res, err := c.Find(context.Background(), ParseClues([]string{"codex", "."}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Matches); strings.Contains(got, watcher.ID) {
		t.Errorf("matches = %q, want the guardian left out", got)
	}
	// Two codex sessions in the window were started by a person; the subagent,
	// the guardian watching it and the one-month-old session are not counted.
	for _, h := range res.Harnesses {
		if h.Name == transcript.Codex && h.Sessions != 2 {
			t.Errorf("codex count = %d, want 2", h.Sessions)
		}
	}
	// Named by id, it opens like anything else.
	res, err = c.Find(context.Background(), ParseClues([]string{watcher.ID}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Matches); got != watcher.ID {
		t.Errorf("by id = %q, want the guardian", got)
	}
}

func TestTiedCountsOnlyTheTop(t *testing.T) {
	tests := []struct {
		name   string
		scores []int
		want   int
	}{
		{"one winner", []int{30, 20, 20}, 1},
		{"two level", []int{20, 20, 10}, 2},
		{"all level", []int{10, 10, 10}, 3},
		{"nothing", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := &Result{}
			for i, sc := range tt.scores {
				res.Matches = append(res.Matches, Match{Session: &transcript.Session{ID: string(rune('a' + i))}, Score: sc})
			}
			if got := res.Tied(); got != tt.want {
				t.Errorf("Tied() = %d, want %d", got, tt.want)
			}
		})
	}
}

// transcript builds what a window of a log reads: events of the given kinds
// and texts, alternating kind then text.
func transcriptOf(title string, ending string, kindText ...string) *transcript.Transcript {
	t := &transcript.Transcript{Session: transcript.Session{Title: title, Ending: ending}}
	for i := 0; i+1 < len(kindText); i += 2 {
		t.Events = append(t.Events, transcript.Event{Kind: transcript.Kind(kindText[i]), Text: kindText[i+1]})
	}
	return t
}

// The usual way a person names a session is by what they asked for in it,
// which no listing metadata holds. When the words match no title, project or
// id, the prompts of the newest candidates are read, and the session whose
// first prompt says it is found, with the reason given.
func TestFindReadsPromptsWhenMetadataMissesTheWords(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)
	codex := c.Providers[0].(*fakeProvider)
	target := "01a09000-0000-7000-8000-000000000003"
	codex.heads = map[string]*transcript.Transcript{
		target: transcriptOf("", "", "assistant", "hello", "user", "the scroll jumps in firefox", "user", "second prompt"),
	}
	codex.tails = map[string]*transcript.Transcript{
		target: transcriptOf("Scroll fix", transcript.EndingDone, "user", "earlier", "user", "ship it", "assistant", "shipped", "tool", ""),
	}

	res, err := c.Find(context.Background(), ParseClues([]string{"firefox"}, c.CWD, now))
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Matches); got != target {
		t.Fatalf("matches = %q, want the session whose prompt names firefox", got)
	}
	m := res.Matches[0]
	if !slices.Contains(m.Why, "firefox∈prompt") {
		t.Errorf("why = %v, want the prompt named as the reason", m.Why)
	}
	s := m.Session
	// The first user event of the head, the last of the tail; the tail's
	// title is the newer one.
	if s.FirstPrompt != "the scroll jumps in firefox" || s.LastPrompt != "ship it" || s.LastSay != "shipped" ||
		s.Ending != transcript.EndingDone || s.Title != "Scroll fix" {
		t.Errorf("enriched = first %q last %q say %q ending %q title %q", s.FirstPrompt, s.LastPrompt, s.LastSay, s.Ending, s.Title)
	}
}

// Reading prompts is bounded: only the newest deepLimit candidates are read,
// and the result says how many were not, so a miss is not reported as a
// clean negative.
func TestFindSaysHowManyPromptsWentUnread(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	p := &fakeProvider{name: transcript.Claude, heads: map[string]*transcript.Transcript{}, tails: map[string]*transcript.Transcript{}}
	for i := range deepLimit + 10 {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000000", i)
		p.sessions = append(p.sessions, &transcript.Session{Provider: transcript.Claude, ID: id, CWD: "/w",
			Started: now.Add(-time.Duration(i+2) * time.Minute), Updated: now.Add(-time.Duration(i+1) * time.Minute)})
		p.tails[id] = transcriptOf("", transcript.EndingDone)
	}
	// Only the oldest session asked about it, and it is past the limit.
	oldest := p.sessions[len(p.sessions)-1].ID
	p.heads[oldest] = transcriptOf("", "", "user", "the pgvector migration")
	c := &Catalog{Providers: []Provider{p}, Now: now}

	res, err := c.Find(context.Background(), ParseClues([]string{"pgvector"}, "/", now))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 0 || res.PromptsUnread != 10 {
		t.Errorf("matches %q unread %d, want none and 10 unread", ids(res.Matches), res.PromptsUnread)
	}
	if p.loads[oldest] != 0 {
		t.Errorf("the oldest session was read %d times, want not at all", p.loads[oldest])
	}
}

// An id prefix can name a parent and its subagent at once, so the same
// session reaches Enrich twice: as a match and as the match's child. It must
// be read once; twice at the same time is two goroutines writing one session
// (go test -race catches that).
func TestEnrichReadsASessionOnce(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)
	codex := c.Providers[0].(*fakeProvider)
	codex.heads, codex.tails = map[string]*transcript.Transcript{}, map[string]*transcript.Transcript{}
	for _, s := range codex.sessions {
		codex.heads[s.ID] = transcriptOf("", "", "user", "nothing relevant")
		codex.tails[s.ID] = transcriptOf("", transcript.EndingDone)
	}
	// "01a0a" is too short to parse as an id, so the clue is built by hand;
	// the prefix is shared by the parent and both its subagents.
	cl := Clues{Providers: []string{transcript.Codex}, IDs: []string{"01a0a"}, Words: []string{"unfindable"}}
	if _, err := c.Find(context.Background(), cl); err != nil {
		t.Fatal(err)
	}
	for id, n := range codex.loads {
		if n != 2 {
			t.Errorf("%s loaded %d times, want once for the head and once for the tail", id, n)
		}
	}
	// Enriched sessions, and sessions of a provider nobody registered, are
	// not read again.
	before := len(codex.loads)
	done := &transcript.Session{Provider: transcript.Codex, ID: "done", FirstPrompt: "x"}
	stray := &transcript.Session{Provider: "gemini", ID: "stray"}
	c.Enrich(context.Background(), []*transcript.Session{done, stray})
	if len(codex.loads) != before {
		t.Errorf("Enrich read an enriched or unknown session: %v", codex.loads)
	}
}

// A session whose log cannot be read (deleted since discovery, or a format
// the reader rejects) is left as it was, not half-filled.
func TestEnrichLeavesAnUnreadableSessionAlone(t *testing.T) {
	p := &fakeProvider{name: transcript.Claude}
	c := &Catalog{Providers: []Provider{p}}
	s := &transcript.Session{Provider: transcript.Claude, ID: "gone", Title: "kept"}
	c.Enrich(context.Background(), []*transcript.Session{s})
	if s.FirstPrompt != "" || s.Ending != "" || s.Title != "kept" || !s.Unreadable {
		t.Errorf("session = %+v, want it unchanged and marked unreadable", s)
	}
	s = &transcript.Session{Provider: transcript.Claude, ID: "gone", Title: "kept"}
	// A head that reads but a tail that does not still fills what it can,
	// and does not take a model or title the head left blank.
	p.heads = map[string]*transcript.Transcript{"gone": {
		Session: transcript.Session{Model: "opus"},
		Events:  []transcript.Event{{Kind: transcript.KindUser, Text: "first"}},
	}}
	c.Enrich(context.Background(), []*transcript.Session{s})
	if s.FirstPrompt != "first" || s.Model != "opus" || s.Title != "kept" || s.Ending != "" || s.Unreadable {
		t.Errorf("session = first %q model %q title %q ending %q", s.FirstPrompt, s.Model, s.Title, s.Ending)
	}
}

// A log path clue names one session exactly, subagent logs included: a
// subagent named by its own file surfaces even though its parent is there.
func TestFindByLogFile(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c := newCatalog(now)
	codex := c.Providers[0].(*fakeProvider)
	for _, s := range codex.sessions {
		s.Files = []string{"/logs/" + s.ID + ".jsonl"}
	}
	child := codex.sessions[1]
	res, err := c.Find(context.Background(), Clues{Files: []string{"/logs/nope.jsonl", child.Files[0]}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Matches); got != child.ID || !slices.Contains(res.Matches[0].Why, "file") {
		t.Errorf("matches = %q, want the subagent its file names", got)
	}
}

// A directory clue matches the directory, what is under it, and what it is
// under, in that order of confidence: asking from a repo's root finds work
// done in its web/ folder, and asking from web/ finds work done at the root.
func TestFindByDirRanksExactOverNested(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	mk := func(id, cwd string) *transcript.Session {
		return &transcript.Session{Provider: transcript.Claude, ID: id, CWD: cwd, Started: now.Add(-2 * time.Hour), Updated: now.Add(-time.Hour)}
	}
	p := &fakeProvider{name: transcript.Claude, sessions: []*transcript.Session{
		mk("aaaaaaaa-exact", "/w/app"), mk("bbbbbbbb-under", "/w/app/web"), mk("cccccccc-above", "/w"),
		mk("dddddddd-sibling", "/w/app2"), mk("eeeeeeee-dotdot", "/w/app/..cache"), mk("ffffffff-nocwd", ""),
	}}
	c := &Catalog{Providers: []Provider{p}, Now: now}
	res, err := c.Find(context.Background(), Clues{Dirs: []string{"/w/app"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range res.Matches {
		got = append(got, m.Session.ID+"="+strings.Join(m.Why, ","))
	}
	// ..cache is a folder inside /w/app with an odd name, not a way out.
	want := "aaaaaaaa-exact=dir bbbbbbbb-under=dir (subdir) eeeeeeee-dotdot=dir (subdir) cccccccc-above=dir (parent)"
	if strings.Join(got, " ") != want {
		t.Errorf("matches = %q\nwant      %q", strings.Join(got, " "), want)
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		path, dir string
		want      bool
	}{
		{"/w/app/web", "/w/app", true},
		{"/w/app", "/w/app", false},
		{"/w/app2", "/w/app", false},
		{"/w", "/w/app", false},
		{"/w/app/..cache", "/w/app", true},
		{"/w/app/../other", "/w/app", false},
		{"/anything", "/", false}, // everything is under /, which says nothing
		{"", "/w", false},
	}
	for _, tt := range tests {
		if got := within(tt.path, tt.dir); got != tt.want {
			t.Errorf("within(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
		}
	}
}

// Between two sessions that match equally, the one still running and the one
// in the caller's own directory come first: that is usually the one meant.
func TestFindPrefersLiveAndNearbySessions(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	mk := func(id, cwd string, live bool, age time.Duration) *transcript.Session {
		return &transcript.Session{Provider: transcript.Claude, ID: id, CWD: cwd, Live: live, Started: now.Add(-age - time.Hour), Updated: now.Add(-age)}
	}
	p := &fakeProvider{name: transcript.Claude, sessions: []*transcript.Session{
		mk("aaaaaaaa-newest-elsewhere", "/other", false, time.Minute),
		mk("bbbbbbbb-live-elsewhere", "/other", true, time.Hour),
		mk("cccccccc-here", "/w/app", false, 2*time.Hour),
		mk("dddddddd-no-cwd", "", false, 3*time.Hour),
	}}
	c := &Catalog{Providers: []Provider{p}, Now: now, CWD: "/w/app"}
	res, err := c.Find(context.Background(), Clues{})
	if err != nil {
		t.Fatal(err)
	}
	want := "cccccccc-here bbbbbbbb-live-elsewhere aaaaaaaa-newest-elsewhere dddddddd-no-cwd"
	if got := ids(res.Matches); got != want {
		t.Errorf("order = %q\nwant    %q", got, want)
	}
}

// A harness whose logs cannot be listed (permissions, a corrupt index) is an
// error the caller reports, not an empty result that reads as "no sessions".
// Whether a harness is installed at all is read from its roots.
func TestFindReportsDiscoveryFailures(t *testing.T) {
	root := t.TempDir()
	broken := &fakeProvider{name: transcript.Codex, roots: []string{filepath.Join(root, "missing"), root}, err: os.ErrPermission}
	c := &Catalog{Providers: []Provider{broken}, Now: time.Now()}
	if _, err := c.Find(context.Background(), Clues{}); !errors.Is(err, os.ErrPermission) {
		t.Errorf("Find() = %v, want the discovery error", err)
	}
	if _, err := c.Find(context.Background(), Clues{AllTime: true}); err == nil {
		t.Error("Find(all time) = nil error")
	}
	res := &Result{}
	if _, err := c.discover(Clues{}, time.Time{}, res, true); err == nil || !res.Harnesses[0].Present {
		t.Errorf("harness = %+v, want present (one root exists) and an error", res.Harnesses)
	}
	// The widening pass fails the same way: nothing in the window, and the
	// provider breaks when asked for all time.
	flaky := &flakyProvider{fakeProvider: fakeProvider{name: transcript.Claude}}
	c = &Catalog{Providers: []Provider{flaky}, Now: time.Now()}
	if _, err := c.Find(context.Background(), Clues{}); err == nil {
		t.Error("Find() = nil after the widened discovery failed")
	}
}

// flakyProvider lists nothing recent and fails when asked for everything.
type flakyProvider struct{ fakeProvider }

func (f *flakyProvider) Discover(q transcript.Query) ([]*transcript.Session, error) {
	if q.Since.IsZero() {
		return nil, errors.New("index unreadable")
	}
	return nil, nil
}

// Load goes to the session's own provider; a session from a harness this
// build has no reader for is an error, not a nil transcript.
func TestLoadDispatchesByProvider(t *testing.T) {
	p := &fakeProvider{name: transcript.Claude, tails: map[string]*transcript.Transcript{"a": transcriptOf("A", "")}}
	c := &Catalog{Providers: []Provider{p}}
	if tr, err := c.Load(&transcript.Session{Provider: transcript.Claude, ID: "a"}, transcript.Load{}); err != nil || tr.Session.Title != "A" {
		t.Errorf("Load() = %v, %v", tr, err)
	}
	if _, err := c.Load(&transcript.Session{Provider: "gemini", ID: "a"}, transcript.Load{}); err == nil || !strings.Contains(err.Error(), "gemini") {
		t.Errorf("Load(unknown provider) = %v, want an error naming it", err)
	}
}

// Some logs carry no start time (an empty session, a format that does not
// record one). On a named day they are placed by their last write.
func TestFindOnADayPlacesUnstartedLogsByLastWrite(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	p := &fakeProvider{name: transcript.Claude, sessions: []*transcript.Session{
		{Provider: transcript.Claude, ID: "aaaaaaaa-on-the-day", Updated: day.Add(10 * time.Hour)},
		{Provider: transcript.Claude, ID: "bbbbbbbb-day-after", Updated: day.Add(30 * time.Hour)},
	}}
	c := &Catalog{Providers: []Provider{p}, Now: now}
	res, err := c.Find(context.Background(), Clues{Since: day, Until: day.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Matches); got != "aaaaaaaa-on-the-day" {
		t.Errorf("matches = %q, want the log last written that day", got)
	}
}

// A session that ran all week had subagents all week. A listing of the last
// day still shows it with all of them, not only those written in that day,
// but the early ones are not listed as matches of their own.
func TestFindCountsSubagentsWrittenBeforeTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	lead := &transcript.Session{Provider: transcript.Claude, ID: "5e8c1a07-0000-4000-8000-000000000001", CWD: "/work/k",
		Started: now.Add(-6 * 24 * time.Hour), Updated: now.Add(-time.Hour)}
	kid := func(id string, age time.Duration) *transcript.Session {
		return &transcript.Session{Provider: transcript.Claude, ID: id, CWD: "/work/k", ParentID: lead.ID, AgentName: "review",
			Started: now.Add(-age - time.Minute), Updated: now.Add(-age)}
	}
	early, late := kid("aearly-0000000000000001", 5*24*time.Hour), kid("alate-0000000000000002", 2*time.Hour)
	// A subagent of the early one, listed before its parent.
	nested := kid("anested-0000000000000003", 5*24*time.Hour)
	nested.ParentID = early.ID
	c := &Catalog{CWD: "/", Now: now, Providers: []Provider{&fakeProvider{name: transcript.Claude,
		sessions: []*transcript.Session{nested, lead, early, late}}}}

	res, err := c.Find(context.Background(), ParseClues([]string{"1d"}, "/", now))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches %q, want the lead", ids(res.Matches))
	}
	if n := len(res.Matches[0].Session.Children); n != 2 {
		t.Errorf("the lead has %d children, want both", n)
	}
	// Named, the early one is outside the day and is not a match.
	if res, _ = c.Find(context.Background(), ParseClues([]string{"1d", "review"}, "/", now)); ids(res.Matches) != late.ID {
		t.Errorf("review in the last day = %q, want only %s", ids(res.Matches), late.ID)
	}
	// By id, the tree comes whole.
	res, err = c.Find(context.Background(), ParseClues([]string{"5e8c1a07"}, "/", now))
	if err != nil || len(res.Matches) != 1 {
		t.Fatalf("by id = %v, %v", res, err)
	}
	var early2 *transcript.Session
	for _, k := range res.Matches[0].Session.Children {
		if k.ID == early.ID {
			early2 = k
		}
	}
	if early2 == nil || len(early2.Children) != 1 {
		t.Errorf("by id, the early subagent = %+v, want it with its own subagent", early2)
	}
}

// Opening Claude Code and clearing it leaves a log with no prompt in it, and
// that log is the newest in its directory. Listed, it reads as the latest
// work there. EnrichTop takes such sessions out and fills the rows with the
// next sessions, which it enriches in their place.
func TestEnrichTopLeavesOutBlankSessions(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	f := &fakeProvider{name: transcript.Claude, heads: map[string]*transcript.Transcript{}, tails: map[string]*transcript.Transcript{}}
	for i, id := range []string{"b1", "b2", "work1", "b3", "work2", "gone", "work3"} {
		f.sessions = append(f.sessions, &transcript.Session{Provider: transcript.Claude, ID: id, Updated: now.Add(-time.Duration(i) * time.Minute)})
		f.heads[id] = transcriptOf("", "", "notice", "/clear")
		f.tails[id] = transcriptOf("", "", "notice", "/clear")
		switch {
		case strings.HasPrefix(id, "work"):
			f.heads[id] = transcriptOf("", "", "user", "fix the build")
		case id == "b2":
			// A self-test that ran one shell command and got a reply.
			f.tails[id] = transcriptOf("", "", "tool", "echo selftest", "assistant", "It echoed.")
		case id == "gone":
			// Unreadable: not known to be blank, so it stays.
			delete(f.heads, id)
			delete(f.tails, id)
		}
	}
	c := &Catalog{CWD: "/", Now: now, Providers: []Provider{f}}
	res, err := c.Find(context.Background(), Clues{})
	if err != nil {
		t.Fatal(err)
	}
	c.EnrichTop(context.Background(), res, 2)
	if got := ids(res.Matches); got != "work1 work2 gone work3" {
		t.Errorf("matches = %q, want the sessions with work in them, newest first", got)
	}
	var blank []string
	for _, s := range res.Blank {
		blank = append(blank, s.ID)
	}
	// b3 came up into the rows when b1 and b2 went, and was read in turn.
	if got := strings.Join(blank, " "); got != "b1 b2 b3" {
		t.Errorf("blank = %q, want every one read and found empty", got)
	}
	if res.Matches[1].Session.FirstPrompt == "" {
		t.Error("work2 moved up into the rows without being enriched")
	}
}

// Two sessions with the same score and the same last write come out in the
// same order however discovery happened to hand them over.
func TestRankBreaksTiesTheSameWay(t *testing.T) {
	at := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	c := &Catalog{Now: at.Add(time.Hour)}
	order := func(all []*transcript.Session) []string {
		var ids []string
		for _, m := range c.rank(all, Clues{}, &Result{}) {
			ids = append(ids, m.Session.ID)
		}
		return ids
	}
	a := &transcript.Session{Provider: transcript.Claude, ID: "3007ed00", Updated: at}
	b := &transcript.Session{Provider: transcript.Copilot, ID: "c0c0c0c0", Updated: at}
	if one, other := order([]*transcript.Session{a, b}), order([]*transcript.Session{b, a}); !slices.Equal(one, other) {
		t.Errorf("the same tie ranked %v one way and %v the other", one, other)
	}
}
