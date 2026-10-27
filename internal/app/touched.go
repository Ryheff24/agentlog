package app

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"agentlog/internal/catalog"
	"agentlog/internal/clip"
	"agentlog/internal/engine"
	"agentlog/internal/render"
	"agentlog/internal/scan"
	"agentlog/internal/transcript"
)

// touched answers "who worked on this file" — the question an agent asks
// before changing something it did not write. There is no index: candidates
// come from the same lookup as any other clue, the basename is a cheap literal
// to scan the logs for, and only the sessions whose bytes contain it are
// parsed. That is enough to search a whole corpus in a second, and it means
// the answer is never stale.
func touched(env Env, res *catalog.Result, opts options, clues catalog.Clues, ro render.Options) error {
	paths := clues.Paths
	re := regexp.MustCompile(strings.Join(quoteAll(paths), "|")) // quoted literals always compile
	// Every candidate is read: the answer is a ranking over all of them, and
	// "who touched this file" with half the corpus looked at is not an answer.
	found := scan.Sessions(env.ctx(), candidates(res), re)

	var uses []use
	for at := 0; at < len(found.Matches); {
		for _, h := range selectBatch(env, found.Matches[at:], opts, engine.Filter{}) {
			at++
			if h.t == nil {
				continue
			}
			if u := usesOf(h.t, paths); u.Edits+u.Reads > 0 {
				if opts.edits && u.Edits == 0 {
					continue
				}
				uses = append(uses, u)
			}
		}
	}
	// Whoever changed it outranks whoever only read it, then recency.
	sort.SliceStable(uses, func(i, j int) bool {
		if opts.edits && opts.files {
			return uses[i].LastWrite.After(uses[j].LastWrite)
		}
		if uses[i].Edits != uses[j].Edits {
			return uses[i].Edits > uses[j].Edits
		}
		return uses[i].Session.Updated.After(uses[j].Session.Updated)
	})

	if opts.json {
		return writeJSON(env.Stdout, fileResponse(uses, found, paths))
	}
	w := env.Stdout
	fmt.Fprintf(w, "%s\n\n", strings.Join(shortPaths(paths, ro.CWD), ", "))
	limit := orDefault(opts.limit, fileSessions)
	shown := 0
	for _, u := range uses {
		if shown == limit {
			break
		}
		shown++
		s := u.Session
		fmt.Fprintf(w, "%-8s %-14s %s\n", s.Provider, render.Ref(s), u.What())
		if !u.LastWrite.IsZero() {
			fmt.Fprintf(w, "         last write: %s\n", u.LastWrite.Format(time.RFC3339))
		}
		fmt.Fprintf(w, "         %s · %s\n", s.Updated.Local().Format("Mon Jan 2 15:04"), clip.Line(pick(s.Title, s.FirstPrompt, s.CWD), 96))
		for _, p := range u.Paths {
			fmt.Fprintf(w, "         as %s\n", render.RelTo(p, ro.CWD))
		}
		if len(u.Refs) > 0 {
			fmt.Fprintf(w, "         %s %s\n", render.Ref(s), strings.Join(u.Refs, " "))
		}
	}
	if shown > 0 {
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, fileOutcome(found, len(uses), shown, paths))
	if shown > 0 {
		fmt.Fprintf(w, "next: %s\n", nextFor(uses[0], paths))
	}
	return nil
}

// nextFor is the command that shows what the top session did with the file:
// its first change in full, or where it mentioned the file.
func nextFor(u use, paths []string) string {
	ref := render.Ref(u.Session)
	if len(u.Refs) > 0 {
		return fmt.Sprintf("agentlog %s%s shows that change in full; agentlog %s --edits shows all of them", ref, u.Refs[0], ref)
	}
	return fmt.Sprintf("agentlog %s --grep %s shows where it was mentioned", ref, shellWord(strings.Join(quoteAll(paths), "|")))
}

// fileSessions is how many sessions a file lookup prints before it stops and
// says how many more there were.
const fileSessions = 12

// use is one session's dealings with the files asked about.
type use struct {
	LastWrite time.Time
	Session   *transcript.Session
	Edits     int
	Reads     int
	Refs      []string
	Paths     []string // what it actually touched, when that is not what was asked
}

// What describes a session's dealings in the fewest words that stay true.
func (u use) What() string {
	var parts []string
	if u.Edits > 0 {
		parts = append(parts, plural(u.Edits, "change"))
	}
	if u.Reads > 0 {
		parts = append(parts, plural(u.Reads, "mention"))
	}
	return strings.Join(parts, ", ")
}

// usesOf counts what one session did with the files. A change is the strong
// signal and is taken from the typed change list; a mention is anything else
// in the session that named the file, which is weaker but is how a reader
// finds the session that only read it, or only discussed it.
func usesOf(t *transcript.Transcript, paths []string) use {
	u := use{Session: &t.Session}
	for _, e := range t.Events {
		edited := false
		for _, c := range e.Changes {
			for _, p := range []string{c.Path, c.MoveTo} {
				if !matchesPath(p, paths) {
					continue
				}
				edited = true
				u.note(p, paths)
			}
		}
		switch {
		case edited:
			if !e.Failed && e.Time.After(u.LastWrite) {
				u.LastWrite = e.Time
			}
			u.Edits++
			if len(u.Refs) < fileRefs {
				u.Refs = append(u.Refs, fmt.Sprintf("#%d", e.Index))
			}
		case mentions(e, paths):
			u.Reads++
		}
	}
	return u
}

// note records a path the session touched that is not the one asked about —
// the same file in another worktree or checkout. Left unsaid, a reader would
// take the answer to be about the path it typed.
func (u *use) note(p string, paths []string) {
	for _, want := range paths {
		if p == want {
			return
		}
	}
	for _, have := range u.Paths {
		if have == p {
			return
		}
	}
	if len(u.Paths) < 3 {
		u.Paths = append(u.Paths, p)
	}
}

// fileRefs is how many event refs one session contributes to the answer.
const fileRefs = 8

func matchesPath(p string, paths []string) bool {
	if p == "" {
		return false
	}
	for _, want := range paths {
		if p == want || strings.HasSuffix(p, "/"+filepath.Base(want)) && sameTail(p, want) {
			return true
		}
	}
	return false
}

// sameTail compares two paths from the right, which is how the same file looks
// in two checkouts of the same repository. At least the file and the directory
// holding it have to agree: on a basename alone the question "who changed this
// config.ts" would be answered with every config.ts in the corpus. The cost is
// that a file at the top of a checkout only matches at its exact path.
func sameTail(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 1; i <= min(len(as), len(bs)); i++ {
		if as[len(as)-i] != bs[len(bs)-i] {
			return i > 2 // the segments before the mismatch agreed
		}
	}
	return true
}

func mentions(e transcript.Event, paths []string) bool {
	for _, want := range paths {
		base := filepath.Base(want)
		if strings.Contains(e.Input, base) || strings.Contains(e.Output, base) || strings.Contains(e.Text, base) {
			return true
		}
	}
	return false
}

func fileOutcome(found scan.Result, total, shown int, paths []string) string {
	what := strings.Join(shortPaths(paths, ""), ", ")
	searched := fmt.Sprintf("searched %s of %s (%s) for %s",
		plural(found.Sessions-found.Unread, "session"), plural(found.Sessions, "session"),
		clip.Size(int(found.Bytes)), what)
	switch {
	case total == 0 && len(found.Matches) > 0:
		return searched + fmt.Sprintf(": %s named it but none touched it", plural(len(found.Matches), "session"))
	case total == 0:
		return searched + ": no session touched it"
	case shown < total:
		return searched + fmt.Sprintf(": %d sessions, %d shown (--limit N for more)", total, shown)
	default:
		return searched + fmt.Sprintf(": %s", plural(total, "session"))
	}
}

func fileResponse(uses []use, found scan.Result, paths []string) *engine.Response {
	r := &engine.Response{Query: strings.Join(paths, " "), Notices: []string{fileOutcome(found, len(uses), min(len(uses), fileSessions), paths)}}
	for i, u := range uses {
		if i == fileSessions {
			r.Truncated = append(r.Truncated, engine.Cut{What: "further sessions that touched it", Count: len(uses) - i})
			break
		}
		r.Sessions = append(r.Sessions, u.Session)
		if len(u.Refs) > 0 {
			r.Next = append(r.Next, "agentlog "+render.Ref(u.Session)+u.Refs[0])
		}
	}
	return r
}

func pick(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func quoteAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = regexp.QuoteMeta(filepath.Base(p))
	}
	return out
}

func shortPaths(paths []string, cwd string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = render.RelTo(p, cwd)
	}
	return out
}
