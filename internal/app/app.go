// Package app is the agentlog command: it turns clues and view flags into a
// catalog lookup and one rendered view.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/clip"
	"github.com/Ryheff24/agentlog/internal/engine"
	"github.com/Ryheff24/agentlog/internal/render"
	"github.com/Ryheff24/agentlog/internal/scan"
	"github.com/Ryheff24/agentlog/internal/transcript"
	"github.com/Ryheff24/agentlog/internal/work"
)

// Env is what the command needs from the outside world.
type Env struct {
	Catalog *catalog.Catalog
	Stdout  io.Writer
	Stderr  io.Writer
	Ctx     context.Context
}

// ctx is the cancellation the caller supplied, if any.
func (e Env) ctx() context.Context {
	if e.Ctx != nil {
		return e.Ctx
	}
	return context.Background()
}

// Run executes one invocation and returns the process exit code.
func Run(env Env, args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(env.Stdout, "agentlog", version())
		return 0
	}
	if len(args) > 0 && args[0] == "feedback" {
		if err := feedback(env, args[1:]); err != nil {
			fmt.Fprintf(env.Stderr, "agentlog: %v\n", err)
			return 1
		}
		return 0
	}
	opts, clueArgs, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(env.Stderr, "agentlog: %v\n\n%s\n", err, render.Usage)
		return 2
	}
	// --help is the home screen: the sessions an agent is most likely to ask
	// about, then the usage, so it never has to make both calls.
	if opts.help {
		opts, clueArgs = options{jobs: opts.jobs}, nil
	}
	printed := &byteWriter{Writer: env.Stdout}
	env.Stdout = printed
	defer func() {
		if !opts.json {
			fmt.Fprintf(env.Stderr, "returned %d bytes\n", printed.n)
		}
	}()
	// One limit on the machine, claimed before anything reads a file and given
	// back at the end. Raising it is a decision about somebody else's laptop,
	// so a raised limit says so where the person will see it.
	n, release := work.Reserve(opts.jobs)
	defer release()
	if opts.jobs > 0 {
		crowded := ""
		if n < opts.jobs {
			crowded = "; other agentlog runs hold the rest"
		}
		fmt.Fprintf(env.Stderr, "agentlog: reading with %s (default %d)%s — only raise --jobs when the person asks for more speed\n",
			plural(n, "worker"), work.Default(), crowded)
	}
	if err := run(env, opts, clueArgs); err != nil {
		fmt.Fprintf(env.Stderr, "agentlog: %v\n", err)
		return 1
	}
	return 0
}

func run(env Env, opts options, clueArgs []string) error {
	if opts.user {
		opts.kinds = "owner"
	}
	cat := env.Catalog
	w := env.Stdout
	ro := render.Options{Now: cat.Now, CWD: cat.CWD, Width: opts.width, Max: opts.max, Full: opts.full, Reasoning: opts.reasoning}
	clues := catalog.ParseClues(clueArgs, cat.CWD, cat.Now)
	clues.ReadPrompts = opts.list
	if opts.content && len(clues.Words) > 0 {
		opts.grep = regexp.QuoteMeta(strings.Join(clues.Words, " "))
		clues.Words = nil
	}
	if opts.brief {
		ro.Brief, ro.Width = true, 160
	}
	if opts.json {
		w = jsonWriter{Writer: w, Max: orDefault(opts.max, 6000), Full: opts.full}
		env.Stdout = w
	}

	res, err := cat.Find(env.ctx(), clues)
	if err != nil && res == nil {
		return err
	}

	if clues.Doctor {
		return doctor(env, res, opts)
	}
	if opts.projects {
		return projects(env, res, opts, clues, ro)
	}

	home := !clues.Narrowed() && !opts.wantsView() && clues.Event == 0
	// A widened window changes which sessions were considered at all, so say so
	// before any view. Home and List already print it with their own notes.
	if (res.Widened || !clues.Until.IsZero()) && !opts.json && !opts.list && !home {
		render.Notes(w, res, "")
	}

	// Bare call: the home screen.
	if home {
		limit := prepareHome(res, opts.limit)
		cat.EnrichTop(env.ctx(), res, limit)
		if opts.json {
			r := &engine.Response{Query: describe(clues), Sessions: sessions(res.Matches, limit),
				Notices: lookupNotices(res), Next: []string{"agentlog <id>", "agentlog all --grep REGEX"}}
			if n := len(res.Matches) - len(r.Sessions); n > 0 { // the text view says "… N more"
				r.Truncated = append(r.Truncated, engine.Cut{What: "further sessions", Count: n, Get: "agentlog --list --limit N"})
			}
			return writeJSON(w, r)
		}
		render.Home(w, res, ro, limit)
		return nil
	}

	if len(res.Matches) == 0 {
		search := ""
		if len(clues.Words) > 0 {
			search = contentSearch(clues, cat.Now)
		}
		if opts.json {
			r := &engine.Response{Query: describe(clues), Notices: lookupNotices(res)}
			if search != "" {
				r.Next = []string{search}
			}
			return writeJSON(w, r)
		}
		if only := render.OnlyOwn(res, cat.Now); only != "" {
			fmt.Fprintf(w, "%s, for %s\n", only, describe(clues))
			return nil
		}
		fmt.Fprintf(w, "no session matches %s\n", describe(clues))
		fmt.Fprintln(w, "harnesses searched:")
		for _, h := range res.Harnesses {
			// A harness the clues left out was not searched; "0 sessions"
			// would say it was, and found nothing.
			if len(clues.Providers) > 0 && !slices.Contains(clues.Providers, h.Name) {
				continue
			}
			found := "not found"
			if h.Present {
				found = plural(h.Sessions, "session")
			}
			fmt.Fprintf(w, "  %-8s %s\n", h.Name, found)
		}
		if res.Since.IsZero() {
			fmt.Fprintln(w, "all time was searched; try fewer clues or a broader dir")
		} else {
			fmt.Fprintln(w, "try fewer clues, a broader dir, or 'all' to search all time")
		}
		// Agents reach for a word clue to find what was said, which is --grep.
		if search != "" {
			fmt.Fprintln(w, "words match a session's project, title, prompts and subagent names, not what was said in it; to search that:")
			fmt.Fprintln(w, "  "+search)
		}
		return nil
	}

	// A file lookup is already a list of sessions, the ones that touched the
	// file; --list with a file clue used to list every recent session instead.
	fileLookup := len(clues.Paths) > 0 && len(clues.IDs) == 0
	if opts.list && !fileLookup {
		if !opts.json {
			fmt.Fprintln(w, "catalog: persisted harness logs; ephemeral jobs without rollouts and logs outside harness roots are not catalogued")
		}
		limit := orDefault(opts.limit, 20)
		cat.EnrichTop(env.ctx(), res, limit)
		if opts.json {
			r := &engine.Response{Query: describe(clues), Sessions: sessions(res.Matches, limit), Notices: lookupNotices(res)}
			if n := len(res.Matches) - len(r.Sessions); n > 0 {
				r.Truncated = append(r.Truncated, engine.Cut{What: "further matches", Count: n, Get: "agentlog --list --limit N"})
			}
			return writeJSON(w, r)
		}
		render.List(w, res, ro, limit)
		return nil
	}

	// A session with nothing said in it is not one to open, or to offer as
	// another match, unless there is nothing else, or it was asked for by id.
	if len(clues.IDs) == 0 && len(clues.Files) == 0 && len(clues.Paths) == 0 && opts.grep == "" && len(res.Matches) > 1 {
		before := res.Matches
		cat.EnrichTop(env.ctx(), res, max(res.Tied(), 6))
		if len(res.Matches) == 0 {
			res.Matches, res.Blank = before, nil
		}
	}

	// Several sessions matching a naming clue equally well is not a session
	// to open; it is a question to hand back.
	if n := res.Tied(); n > 1 && names(clues) && !opts.wantsView() && clues.Event == 0 && !opts.list {
		return ambiguous(env, res, opts, clues, ro, n)
	}

	// A file names itself, not a session: the answer is which sessions
	// touched it.
	if fileLookup {
		return touched(env, res, opts, clues, ro)
	}

	// Grep without a pinned session searches every candidate.
	if opts.grep != "" && len(clues.IDs) == 0 && len(clues.Files) == 0 && len(res.Matches) > 1 {
		return searchAcross(env, res, opts, ro)
	}

	if opts.images && (len(clues.IDs) > 1 || opts.subagents) {
		return imageSessions(env, res, clues, opts)
	}
	best := res.Matches[0].Session
	t, err := cat.Load(best, loadOpts(opts))
	if err != nil {
		return err
	}
	t.Session.Children = best.Children
	cat.Enrich(env.ctx(), best.Children)

	// Every view ends by saying what this build could not read.
	viewed := []*transcript.Transcript{t}
	defer func() {
		if !opts.json && !opts.count && !opts.images && !opts.tree {
			if opts.tools || opts.usage || opts.edits || opts.files {
				for _, tt := range viewed {
					render.TranscriptNotes(env.Stdout, tt)
				}
			}
			render.Drift(env.Stdout, viewed...)
		}
	}()

	switch {
	case clues.Event > 0:
		to := clues.EventTo
		if to < 0 {
			to = t.LastIndex() // "#40-": everything from there on
		}
		// A range shows every event in it, reasoning included, unless the
		// call also names kinds or a pattern; then it shows the ones that match.
		events := eventRange(t, clues.Event, to)
		sel := engine.Selection{Session: &t.Session, Events: events, Total: len(t.Events), Matched: len(events)}
		matched := len(events)
		if opts.kinds != "" || opts.grep != "" || opts.head > 0 || opts.tail > 0 {
			f, err := filter(opts)
			if err != nil {
				return err
			}
			f.Reasoning = true
			f.Tail = opts.tail
			part := *t
			part.Events = events
			sel = engine.Select(&part, f)
			events, matched = sel.Events, sel.Matched
			indices := map[int]bool{}
			for _, e := range events {
				indices[e.Index] = true
			}
			ro.Keep = func(e transcript.Event) bool { return indices[e.Index] }
			ro.Next = sel.Continuation(render.Ref(&t.Session))
		}
		if opts.images {
			return imageView(env, t, events, opts)
		}
		if opts.count {
			return countView(w, matched, boolInt(matched > 0), opts.json)
		}
		if opts.json {
			r := engine.FromSelection(sel, render.Ref(&t.Session), describe(clues), t)
			return writeJSON(w, r)
		}
		render.Events(w, t, clues.Event, to, ro)
		render.TranscriptNotes(w, t)
	case opts.tree:
		return sessionTree(env, &t.Session, opts)
	case opts.images:
		return imageView(env, t, t.Events, opts)
	case opts.tools:
		ts := []*transcript.Transcript{t}
		if opts.subagents {
			ts = append(ts, loadChildren(cat, best, loadOpts(opts))...)
		}
		viewed = ts
		if opts.json {
			census := engine.Census(ts...)
			if len(ts) > 1 {
				// Counted across sessions, an event number names no event.
				for i := range census {
					census[i].First, census[i].Last = 0, 0
				}
			}
			return writeJSON(w, &engine.Response{Ref: render.Ref(&t.Session), Query: describe(clues),
				Session: &t.Session, Tools: census, Notices: notices(ts)})
		}
		render.Tools(w, ts, ro)
	case opts.usage:
		ts := []*transcript.Transcript{t}
		if opts.subagents {
			ts = append(ts, loadChildren(cat, best, loadOpts(opts))...)
		}
		viewed = ts
		if opts.json {
			return writeJSON(w, spendResponse(ts, describe(clues)))
		}
		render.Spend(w, ts, ro)
	case opts.edits || opts.files:
		ts := []*transcript.Transcript{t}
		if opts.edits && opts.files && len(clues.IDs) == 0 {
			for _, m := range res.Matches[1:] {
				other, err := cat.Load(m.Session, loadOpts(opts))
				if err != nil {
					return err
				}
				ts = append(ts, other)
			}
		}
		if opts.subagents {
			ts = append(ts, loadChildren(cat, best, loadOpts(opts))...)
		}
		viewed = ts
		var pathRe *regexp.Regexp
		if opts.path != "" {
			if pathRe, err = regexp.Compile(opts.path); err != nil {
				return fmt.Errorf("--path: %w", err)
			}
		}
		if opts.json {
			return writeJSON(w, editsResponse(ts, pathRe, describe(clues)))
		}
		render.Edits(w, ts, pathRe, opts.files, ro)
	case opts.wantsView():
		f, err := filter(opts)
		if err != nil {
			return err
		}
		ts := []*transcript.Transcript{t}
		if opts.subagents {
			ts = append(ts, loadChildren(cat, best, loadOpts(opts))...)
		}
		viewed = ts
		if opts.count {
			n, ss := 0, 0
			for _, tt := range ts {
				sel := engine.Select(tt, f)
				n += sel.Matched
				ss += boolInt(sel.Matched > 0)
			}
			return countView(w, n, ss, opts.json)
		}
		if opts.json {
			var rs []*engine.Response
			for _, tt := range ts {
				r := engine.FromSelection(engine.Select(tt, f), render.Ref(&tt.Session), describe(clues), tt)
				rs = append(rs, r)
			}
			return writeJSON(w, rs)
		}
		for i, tt := range ts {
			if i > 0 {
				fmt.Fprintln(w)
			}
			render.Timeline(w, engine.Select(tt, f), ro)
			render.TranscriptNotes(w, tt)
		}
	default:
		alts := res.Matches[1:]
		cat.Enrich(env.ctx(), topSessions(alts, 5))
		kids := loadChildren(cat, best, loadOpts(opts))
		// The preview names the session's unreadable records itself; the
		// footer is left to say what its subagents held, if anything.
		viewed = kids
		if opts.json {
			return writeJSON(w, previewResponse(t, kids, alts, describe(clues)))
		}
		render.Preview(w, t, kids, alts, ro)
	}
	return nil
}

// prepareHome keeps running work across projects ahead of directory affinity.
// The default preview includes every running session; an explicit limit still
// bounds the output. Named lookups retain their relevance ranking.
func prepareHome(res *catalog.Result, limit int) int {
	slices.SortStableFunc(res.Matches, func(a, b catalog.Match) int {
		if hasLiveWork(a.Session) == hasLiveWork(b.Session) {
			return 0
		}
		if hasLiveWork(a.Session) {
			return -1
		}
		return 1
	})
	if limit > 0 {
		return limit
	}
	running := 0
	for _, m := range res.Matches {
		if hasLiveWork(m.Session) {
			running++
		}
	}
	return max(12, running)
}

func hasLiveWork(s *transcript.Session) bool {
	if s.Live {
		return true
	}
	for _, child := range s.Children {
		if hasLiveWork(child) {
			return true
		}
	}
	return false
}

func filter(opts options) (engine.Filter, error) {
	f := engine.Filter{Flags: selectionFlags(opts), Head: opts.head, Tail: opts.tail, Reasoning: opts.reasoning, IncludeHandbacks: opts.includeHandbacks}
	if opts.kinds != "" {
		f.Kinds = map[transcript.Kind]bool{}
		for _, k := range strings.Split(opts.kinds, ",") {
			k = strings.TrimSpace(k)
			valid := k == "owner" || k == "refusal"
			for _, known := range transcript.AllKinds {
				if string(known) == k {
					valid = true
				}
			}
			if !valid {
				return f, fmt.Errorf("unknown --kind %q (have %v)", k, transcript.AllKinds)
			}
			f.Kinds[transcript.Kind(k)] = true
		}
	}
	if opts.grep != "" {
		re, err := regexp.Compile("(?i)" + opts.grep)
		if err != nil {
			return f, fmt.Errorf("--grep: %w", err)
		}
		f.Grep = re
	}
	if f.Tail == 0 && f.Head == 0 && f.Grep == nil {
		f.Tail = 80
	}
	return f, nil
}

// searchAcross scans the raw bytes of every candidate log in parallel and
// parses only the ones that matched. The scan is the cheap part — no JSON is
// decoded — so it covers every candidate instead of the best few.
func searchAcross(env Env, res *catalog.Result, opts options, ro render.Options) error {
	f, err := filter(opts)
	if err != nil {
		return err
	}
	raw := f.Grep // the view's own pattern: the scan finds what the view will show
	cands := candidates(res)

	shown, matched := 0, 0
	// Sessions whose log matched but whose shown events did not, by where
	// the match was instead.
	unshown := map[string]int{}
	var responses []*engine.Response
	var found scan.Result

	// Output is not a cumulative allowance: every candidate is scanned and one
	// matching session never crowds out the sessions after it.
	for from := 0; from < len(cands); from += scanBatch {
		part := cands[from:min(from+scanBatch, len(cands))]
		batch := scan.Sessions(env.ctx(), part, raw)
		found.Sessions += batch.Sessions
		found.Files += batch.Files
		found.Bytes += batch.Bytes
		found.Failed += batch.Failed
		found.Unread += batch.Unread
		at := 0
		for at < len(batch.Matches) {
			for _, hit := range selectBatch(env, batch.Matches[at:], opts, f) {
				// Whether a session has anything to show is a fact about its
				// events: the byte scan matched the whole log, including the
				// parts this view leaves out.
				if hit.t == nil || !hit.sel.Any() {
					unshown[whereElse(env, batch.Matches[at].Session, hit.t, opts, f)]++
					at++
					continue
				}
				matched += hit.sel.Matched
				r := engine.FromSelection(hit.sel, render.Ref(hit.sel.Session), opts.grep, hit.t)
				if opts.json {
					responses = append(responses, r)
				} else if !opts.count {
					render.Timeline(env.Stdout, hit.sel, ro)
					render.TranscriptNotes(env.Stdout, hit.t)
					fmt.Fprintln(env.Stdout)
				}
				at, shown = at+1, shown+1
			}
		}
		found.Matches = append(found.Matches, batch.Matches[:at]...)
	}
	if opts.count {
		return countView(env.Stdout, matched, shown, opts.json)
	}
	notices := []string{searched(found, shown, opts.grep)}
	if n := unshownNotice(unshown); n != "" {
		notices = append(notices, n)
	}
	if opts.json {
		r := &engine.Response{
			Query:    opts.grep,
			Notices:  notices,
			Sessions: sessionsOf(responses),
			Events:   eventsOf(responses),
		}
		return writeJSON(env.Stdout, r)
	}
	for _, n := range notices {
		fmt.Fprintln(env.Stdout, n)
	}
	return nil
}

// Where a match was, in a session whose log matched but whose events as
// shown do not.
const (
	inRewound   = "in rewound or edited turns (add --branches to see them)"
	inOtherKind = "in events --kind leaves out"
	inNoEvent   = "in text that is not an event, such as instructions the harness adds"
	unreadable  = "in a log that could not be read"
)

// whereElse finds where a session's match went. A byte match with no event
// to show used to be put down to rewound turns, which sent agents to
// --branches after text that no view prints.
func whereElse(env Env, s *transcript.Session, t *transcript.Transcript, opts options, f engine.Filter) string {
	if t == nil {
		return unreadable
	}
	wide := f
	wide.Kinds, wide.Reasoning = nil, true
	if engine.Select(t, wide).Any() {
		return inOtherKind
	}
	if opts.branches || t.Hidden == 0 {
		return inNoEvent
	}
	load := loadOpts(opts)
	load.Branches = true
	all, err := env.Catalog.Load(s, load)
	if err != nil {
		return inNoEvent
	}
	if engine.Select(all, f).Any() {
		return inRewound
	}
	if engine.Select(all, wide).Any() {
		return inOtherKind
	}
	return inNoEvent
}

func unshownNotice(by map[string]int) string {
	total := 0
	var parts []string
	for _, where := range []string{inRewound, inOtherKind, inNoEvent, unreadable} {
		if n := by[where]; n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%d %s", n, where))
		}
	}
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("%s matched only outside the events shown: %s", plural(total, "more session"), strings.Join(parts, "; "))
}

// scanBatch keeps the workers fed without retaining the entire corpus.
const scanBatch = 48

// candidates flattens a lookup into the sessions a search will read, best
// first, so that stopping early stops on the least promising ones.
func candidates(res *catalog.Result) []*transcript.Session {
	var ss []*transcript.Session
	for _, m := range res.Matches {
		ss = append(ss, m.Session)
		ss = append(ss, m.Session.Children...)
	}
	return ss
}

// lookupNotices states what the lookup did that the clues did not ask for.
func lookupNotices(res *catalog.Result) []string {
	var out []string
	if res.Widened {
		out = append(out, "nothing matched in the last 7 days, so all time was searched")
	}
	if !res.Clues.Until.IsZero() {
		out = append(out, "sessions that were running on "+res.Clues.Since.Local().Format(time.DateOnly))
	}
	if res.HiddenSelf {
		out = append(out, "your own session is hidden; pass its id to include it")
		if len(res.Matches) == 0 && res.Own != nil {
			out = append(out, "no sessions but your own (agentlog "+render.Ref(res.Own)+" opens it)")
		}
	}
	if n := res.PromptsUnread; n > 0 {
		out = append(out, fmt.Sprintf("words were matched against the prompts of the newest sessions only; %d older ones by title, project and id alone", n))
	}
	if b := res.Blank; len(b) > 0 {
		out = append(out, render.BlankNote(b))
	}
	return out
}

// hit is one matching session, selected but not yet rendered.
type hit struct {
	t   *transcript.Transcript
	sel engine.Selection
}

// selectBatch parses and filters the next few matches at once. A match earns a
// full parse, the largest logs here are tens of megabytes, and the sessions
// are independent, so doing them one after another is most of what a search
// costs. A batch is as wide as the machine is allowed to be.
func selectBatch(env Env, ms []scan.Match, opts options, f engine.Filter) []hit {
	n := min(len(ms), work.Jobs())
	out := make([]hit, n)
	work.Do(env.ctx(), n, func(i int) {
		t, err := env.Catalog.Load(ms[i].Session, loadOpts(opts))
		if err != nil {
			return
		}
		out[i] = hit{t: t, sel: engine.Select(t, f)}
	})
	return out
}

// searched reports what was covered as well as what was found: "0 hits" and
// "0 hits in the 40 logs I got round to" are different answers.
func searched(found scan.Result, shown int, pattern string) string {
	return fmt.Sprintf("searched %s of %s (%s) for %q: %s",
		plural(found.Sessions-found.Unread, "session"), plural(found.Sessions, "session"),
		clip.Size(int(found.Bytes)), pattern, outcome(found, shown))
}

// sessionsOf and eventsOf flatten per-session responses into one, so a search
// answer is a hit list rather than a list of lists.
func sessionsOf(rs []*engine.Response) []*transcript.Session {
	var out []*transcript.Session
	for _, r := range rs {
		out = append(out, r.Session)
	}
	return out
}

func eventsOf(rs []*engine.Response) []transcript.Event {
	var out []transcript.Event
	for _, r := range rs {
		out = append(out, r.Events...)
	}
	return out
}

// outcome states what the scan found in the words the caller needs: a real
// zero is worth saying plainly, and so is a match that no view would show.
func outcome(found scan.Result, shown int) string {
	switch {
	case len(found.Matches) == 0:
		return "no log contains it"
	case shown == 0:
		return fmt.Sprintf("%s contain it, none in the events this view shows (try --kind or --full)", plural(len(found.Matches), "log"))
	default:
		return fmt.Sprintf("%s with events that match", plural(shown, "session"))
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// loadOpts turns view flags into reader options.
func loadOpts(o options) transcript.Load {
	return transcript.Load{Branches: o.branches, Subagents: o.subagents}
}

func loadChildren(cat *catalog.Catalog, s *transcript.Session, load transcript.Load) []*transcript.Transcript {
	var out []*transcript.Transcript
	for _, c := range s.Children {
		if c.AgentRole == "guardian" {
			continue
		}
		if t, err := cat.Load(c, load); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// notices is what a hand-built response has to say for itself.
func notices(ts []*transcript.Transcript) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Notes...)
	}
	if d := engine.Drift(ts...); d != "" {
		out = append(out, d)
	}
	return out
}

// names reports whether the clues point at a particular session rather than
// at a set of them. A dir or a day narrows the field; an id prefix or a word
// is an attempt to name one, and failing to name one is worth saying.
func names(cl catalog.Clues) bool { return len(cl.IDs) > 0 || len(cl.Words) > 0 }

// ambiguous hands the question back instead of answering the wrong one. The
// cost of being wrong here is high and silent: the reader gets a real session,
// reads it, and never learns it was not the one it asked for.
func ambiguous(env Env, res *catalog.Result, opts options, clues catalog.Clues, ro render.Options, tied int) error {
	limit := orDefault(opts.limit, 12)
	shown := min(tied, limit)
	env.Catalog.Enrich(env.ctx(), topSessions(res.Matches[:shown], shown))
	notice := fmt.Sprintf("%d sessions match %s equally well — add a word, a dir, or more of the id to pick one",
		tied, describe(clues))
	if opts.json {
		r := &engine.Response{Query: describe(clues), Sessions: sessions(res.Matches[:shown], shown),
			Notices: append([]string{notice}, lookupNotices(res)...)}
		if tied > shown {
			r.Truncated = append(r.Truncated, engine.Cut{What: "further sessions matching equally well",
				Count: tied - shown, Get: "agentlog --list --limit N"})
		}
		return writeJSON(env.Stdout, r)
	}
	fmt.Fprintf(env.Stdout, "%s\n\n", notice)
	render.List(env.Stdout, res, ro, shown)
	if tied > shown {
		fmt.Fprintf(env.Stdout, "\n%s more matching equally well (--limit N)\n", plural(tied-shown, "session"))
	}
	return nil
}

// spendResponse answers --usage. What a session cost is a handful of numbers,
// so it carries no events: they would only crowd out the answer.
func spendResponse(ts []*transcript.Transcript, query string) []*engine.Response {
	var rs []*engine.Response
	for _, t := range ts {
		r := &engine.Response{Ref: render.Ref(&t.Session), Query: query, Session: &t.Session, Usage: &t.Usage,
			Notices: notices([]*transcript.Transcript{t})}
		if !t.Usage.Known() {
			r.Notices = append(r.Notices, "no tokens recorded in this log")
		}
		rs = append(rs, r)
	}
	return rs
}

// editsResponse answers --edits with the sessions and their changes.
func editsResponse(ts []*transcript.Transcript, pathRe *regexp.Regexp, query string) []*engine.Response {
	f := engine.Filter{Kinds: map[transcript.Kind]bool{transcript.KindEdit: true}, Path: pathRe}
	var rs []*engine.Response
	for _, t := range ts {
		r := engine.FromSelection(engine.Select(t, f), render.Ref(&t.Session), query, t)
		r.Commits = engine.Commits(t)
		rs = append(rs, r)
	}
	return rs
}

// previewResponse answers the default view: the session, the alternatives that
// were not chosen, and the last events, so a reader that took the wrong match
// can see that and say which one it meant.
func previewResponse(t *transcript.Transcript, kids []*transcript.Transcript, alts []catalog.Match, query string) *engine.Response {
	sel := engine.Select(t, engine.Filter{Tail: previewEvents})
	r := engine.FromSelection(sel, render.Ref(&t.Session), query, t)
	r.Provenance = engine.Provenance(t)
	if t.Usage.Known() {
		r.Usage = &t.Usage
	}
	r.Commits = engine.Commits(t)
	for _, k := range kids {
		r.Sessions = append(r.Sessions, &k.Session)
	}
	r.Sessions = append(r.Sessions, sessions(alts, 5)...)
	return r
}

// previewEvents is how much of the tail a preview carries.
const previewEvents = 20

func eventRange(t *transcript.Transcript, from, to int) []transcript.Event {
	return t.Range(from, to)
}

func topSessions(ms []catalog.Match, n int) []*transcript.Session {
	var out []*transcript.Session
	for i, m := range ms {
		if i == n {
			break
		}
		out = append(out, m.Session)
	}
	return out
}

func sessions(ms []catalog.Match, n int) []*transcript.Session { return topSessions(ms, n) }

func describe(c catalog.Clues) string {
	var parts []string
	for _, p := range c.Providers {
		parts = append(parts, "harness="+p)
	}
	for _, id := range c.IDs {
		parts = append(parts, "id="+id)
	}
	for _, d := range c.Dirs {
		parts = append(parts, "dir="+d)
	}
	for _, f := range c.Files {
		parts = append(parts, "file="+f)
	}
	for _, p := range c.Paths {
		parts = append(parts, "path="+p)
	}
	for _, w := range c.Words {
		parts = append(parts, "word="+w)
	}
	switch {
	case !c.Until.IsZero():
		parts = append(parts, "on="+c.Since.Local().Format(time.DateOnly))
	case !c.Since.IsZero():
		parts = append(parts, "since="+c.Since.Local().Format(time.DateTime))
	}
	return strings.Join(parts, " ")
}

// contentSearch is the command that looks for the words in what the
// sessions said, keeping the other clues and the time they covered.
func contentSearch(c catalog.Clues, now time.Time) string {
	args := []string{"agentlog"}
	args = append(args, c.Providers...)
	for _, d := range c.Dirs {
		args = append(args, shellWord(d))
	}
	switch {
	case !c.Until.IsZero():
		args = append(args, c.Since.Local().Format(time.DateOnly))
	case !c.Since.IsZero() && !c.AllTime:
		args = append(args, fmt.Sprintf("%dh", int(math.Ceil(now.Sub(c.Since).Hours()))))
	default:
		args = append(args, "all")
	}
	args = append(args, "--grep", shellWord(regexp.QuoteMeta(strings.Join(c.Words, " "))))
	return strings.Join(args, " ")
}

// shellWord quotes s for a POSIX shell when it needs it.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

// writeJSON writes the complete selected response. Individual long event
// fields are clipped; there is no cumulative response cap.
func writeJSON(w io.Writer, v any) error {
	o, configured := w.(jsonWriter)
	if !configured {
		o.Max = 6000
	}
	if !o.Full {
		switch r := v.(type) {
		case *engine.Response:
			r.ClipLongFields(o.Max)
		case []*engine.Response:
			for _, one := range r {
				one.ClipLongFields(o.Max)
			}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

type jsonWriter struct {
	io.Writer
	Max  int
	Full bool
}

type byteWriter struct {
	io.Writer
	n int
}

func (w *byteWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += n
	return n, err
}

// projectEntry is one project in --projects --json.
type projectEntry struct {
	Dir       string              `json:"dir"`
	LastWork  time.Time           `json:"last_work"`
	Sessions  []string            `json:"sessions"` // newest first
	Subagents int                 `json:"subagents"`
	Dirs      []string            `json:"also_in,omitempty"`
	Latest    *transcript.Session `json:"latest"`
}

// projects answers "what has been worked on, and where did it stop": one
// entry per repository, its latest session read.
func projects(env Env, res *catalog.Result, opts options, clues catalog.Clues, ro render.Options) error {
	ps := env.Catalog.Projects(env.ctx(), res)
	if opts.json {
		out := struct {
			Query    string         `json:"query"`
			Projects []projectEntry `json:"projects"`
			Notices  []string       `json:"notices,omitempty"`
		}{Query: describe(clues), Notices: lookupNotices(res)}
		for _, p := range ps {
			latest := *p.Sessions[0]
			latest.Children = nil // counted in Subagents; agentlog <id> has them
			e := projectEntry{Dir: p.Dir, LastWork: p.Latest(), Subagents: p.Subagents(), Dirs: p.Dirs, Latest: &latest}
			for _, s := range p.Sessions {
				e.Sessions = append(e.Sessions, s.ID)
			}
			out.Projects = append(out.Projects, e)
		}
		return writeJSON(env.Stdout, out)
	}
	render.Projects(env.Stdout, res, ps, ro)
	return nil
}

// selectionFlags retains the selection and output settings in drill-down hints.
func selectionFlags(o options) string {
	var flags []string
	for _, pair := range []struct{ name, value string }{{"kind", o.kinds}, {"grep", o.grep}} {
		if pair.value != "" {
			flags = append(flags, "--"+pair.name, shellWord(pair.value))
		}
	}
	for _, pair := range []struct {
		name string
		set  bool
	}{{"branches", o.branches}, {"subagents", o.subagents}, {"include-handbacks", o.includeHandbacks}, {"reasoning", o.reasoning}, {"json", o.json}, {"full", o.full}, {"brief", o.brief}} {
		if pair.set {
			flags = append(flags, "--"+pair.name)
		}
	}
	if o.max > 0 {
		flags = append(flags, "--max", fmt.Sprint(o.max))
	}
	if len(flags) == 0 {
		return ""
	}
	return " " + strings.Join(flags, " ")
}
