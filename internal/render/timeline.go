package render

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/clip"
	"github.com/Ryheff24/agentlog/internal/engine"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Timeline prints a selection: one line per event, with the excerpts that
// matched when the selection came from a search.
func Timeline(w io.Writer, sel engine.Selection, o Options) {
	s := sel.Session
	o.CWD = s.CWD
	fmt.Fprintf(w, "%s %s %s · %s · %d of %d events", s.Provider, Ref(s), clip.Line(sessionLabel(s), 60),
		tilde(s.CWD), len(sel.Events), sel.Total)
	if n := sel.Dropped(); n > 0 {
		fmt.Fprintf(w, " (%d more matched)", n)
	}
	fmt.Fprintln(w, Own(s))
	c := &clock{}
	for _, e := range sel.Events {
		line := eventLine(e, o, c)
		if o.Brief {
			line = clip.Line(line, 160)
		}
		fmt.Fprintln(w, line)
		if o.Brief {
			continue
		}
		for _, snip := range sel.Snippets[e.Index] {
			fmt.Fprintln(w, "        … "+clip.Line(snip, 200))
		}
	}
	if next := sel.Continuation(Ref(s)); next != "" {
		fmt.Fprintf(w, "(next: %s)\n", next)
	}
	if from, to := sel.Range(); to > 0 {
		fmt.Fprintf(w, "(full content: agentlog %s#%d or #%d-%d)\n", Ref(s), to, from, to)
	}
}

// Events prints events from..to in full, each block bounded by Options.Max.
func Events(w io.Writer, t *transcript.Transcript, from, to int, o Options) {
	o.CWD = t.Session.CWD
	if to < from {
		to = from
	}
	if from < 1 || from > t.LastIndex() {
		fmt.Fprintf(w, "no event #%d: %s %s has %d events\n", from, t.Session.Provider, Ref(&t.Session), t.LastIndex())
		return
	}
	if to > t.LastIndex() {
		to = t.LastIndex()
	}
	c := &clock{}
	shown := 0
	for _, e := range t.Range(from, to) {
		if o.Keep != nil && !o.Keep(e) {
			continue
		}
		abandoned := ""
		if e.RepeatOf > 0 {
			abandoned = fmt.Sprintf(" · repeat of #%d", e.RepeatOf)
		}
		if e.Abandoned {
			abandoned = " · ABANDONED BRANCH (this turn was rewound or edited)"
		}
		if shown > 0 {
			fmt.Fprintln(w)
		}
		shown++
		if o.Brief {
			fmt.Fprintln(w, clip.Line(eventLine(e, o, c), 160))
			continue
		}
		if e.Pasted {
			fmt.Fprintln(w, "[pasted]")
		}
		// The date is the same for long runs of events; the time of day is not.
		if d := c.day(e.Time); d != "" {
			fmt.Fprintf(w, "── %s\n", d)
		}
		// Every view starts an event's line with #N, so a count or a grep
		// written against one view holds for the others.
		fmt.Fprintf(w, "#%d %s · %s · line %d%s\n", e.Index, e.Kind, hhmmss(e.Time), e.Line, abandoned)
		switch {
		case e.Kind == transcript.KindAgent && e.Tool == "" && e.Output == "" && !e.Pending:
			// A message between agents, such as a teammate's, was not a call
			// and has no result to wait for.
			fmt.Fprintf(w, "message: %s\n", e.Agent)
			indent(w, clip.Block(e.Text, o.max()), "  ")
		case e.Kind == transcript.KindTool, e.Kind == transcript.KindEdit, e.Kind == transcript.KindAgent:
			label := strings.TrimSpace(e.Tool + " " + e.Agent)
			if e.AgentID != "" {
				label += " (session " + ShortID(e.AgentID) + ")"
			}
			fmt.Fprintf(w, "call: %s\n", label)
			if e.Input != "" {
				indent(w, clip.Block(e.Input, o.max()), "  ")
			}
			if e.Text != "" {
				fmt.Fprintln(w, "message:")
				indent(w, clip.Block(e.Text, o.max()), "  ")
			}
			for _, c := range e.Changes {
				fmt.Fprintf(w, "diff %s %s (+%d -%d):\n", c.Op, relTo(c.Path, o.CWD), c.Added, c.Removed)
				indent(w, clip.Block(c.Diff, o.max()), "  ")
			}
			fmt.Fprintf(w, "result%s:\n", resultNote(e))
			if e.Output != "" {
				if e.Handback && !o.Full {
					fmt.Fprintf(w, "  [handback] %s; %d bytes (agentlog %s#%d --full)\n", e.Input, len(e.Output), Ref(&t.Session), e.Index)
				} else {
					indent(w, clip.Block(e.Output, o.max()), "  ")
				}
			}
		case e.Kind == transcript.KindPlan:
			if len(e.Plan) > 0 {
				for _, st := range e.Plan {
					fmt.Fprintf(w, "  %s %s\n", statusMark(st.Status), st.Text)
				}
			}
			if e.Text != "" {
				indent(w, clip.Block(e.Text, o.max()), "  ")
			}
		default:
			if e.Tag != "" {
				fmt.Fprintf(w, "[%s]\n", e.Tag)
			}
			indent(w, clip.Block(e.Text, o.max()), "  ")
		}
	}
	if o.Next != "" {
		fmt.Fprintf(w, "(next: %s)\n", o.Next)
	} else if o.Keep == nil && to < t.LastIndex() {
		fmt.Fprintf(w, "(next: agentlog %s#%d)\n", Ref(&t.Session), to+1)
	}
}

// Edits prints every file change in order, or per-file totals with filesOnly.
func Edits(w io.Writer, ts []*transcript.Transcript, pathRe *regexp.Regexp, filesOnly bool, o Options) {
	type row struct {
		t *transcript.Transcript
		e transcript.Event
		c transcript.Change
	}
	var rows []row
	for _, t := range ts {
		for _, e := range t.Events {
			if e.Kind != transcript.KindEdit {
				continue
			}
			for _, c := range e.Changes {
				if pathRe != nil && !pathRe.MatchString(c.Path) {
					continue
				}
				rows = append(rows, row{t, e, c})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].e.Time.Before(rows[j].e.Time) })
	if len(rows) == 0 {
		fmt.Fprintln(w, "no file changes recorded")
		return
	}
	base := ts[0].Session.CWD
	commits(w, ts, base)
	if filesOnly {
		type agg struct {
			last                   time.Time
			writer                 string
			edits, add, rm, failed int
			who                    map[string]bool
		}
		totals := map[string]*agg{}
		var order []string
		for _, r := range rows {
			a := totals[r.c.Path]
			if a == nil {
				a = &agg{who: map[string]bool{}}
				totals[r.c.Path] = a
				order = append(order, r.c.Path)
			}
			a.edits++
			a.who[who(r.t)] = true
			if r.e.Failed {
				a.failed++
				continue
			}
			a.add += r.c.Added
			a.rm += r.c.Removed
			a.last, a.writer = r.e.Time, fmt.Sprintf("%s#%d", Ref(&r.t.Session), r.e.Index)
		}
		sort.SliceStable(order, func(i, j int) bool { return totals[order[i]].last.After(totals[order[j]].last) })
		for _, p := range order {
			a := totals[p]
			names := make([]string, 0, len(a.who))
			for n := range a.who {
				names = append(names, n)
			}
			sort.Strings(names)
			// A file whose every edit failed was not changed, and "+0 -0 1 edit"
			// would read as a change that happened to be empty.
			what := fmt.Sprintf("+%d -%d  %s", a.add, a.rm, plural(a.edits, "edit"))
			switch {
			case a.failed == a.edits:
				what = "not changed: " + plural(a.failed, "failed edit")
			case a.failed > 0:
				what += fmt.Sprintf(" (%d failed)", a.failed)
			}
			fmt.Fprintf(w, "%-55s %s  by %s\n", relTo(p, base), what, strings.Join(names, ","))
			if a.writer != "" {
				fmt.Fprintf(w, "  last writer: %s at %s\n", a.writer, a.last.Format(time.RFC3339))
			}
		}
		return
	}
	for _, r := range rows {
		status := ""
		if r.e.Failed {
			status = "  FAILED (not applied)"
		}
		fmt.Fprintf(w, "── %s#%d %s %s %s (+%d -%d)%s\n", Ref(&r.t.Session), r.e.Index, stamp(o.Now, r.e.Time), r.c.Op,
			relTo(r.c.Path, base), r.c.Added, r.c.Removed, status)
		if who := who(r.t); who != Ref(&r.t.Session) {
			fmt.Fprintf(w, "   by %s\n", who)
		}
		indent(w, clip.Block(r.c.Diff, o.max()), "")
	}
}

// commits lists what the session landed, above the changes themselves: a
// change that made it into a commit and one that was left in the tree are
// different facts, and only the log knows which sha the work became.
func commits(w io.Writer, ts []*transcript.Transcript, base string) {
	cs := engine.Commits(ts...)
	if len(cs) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", plural(len(cs), "commit"))
	for _, c := range cs {
		fmt.Fprintf(w, "  %-10s #%-6d %s\n", c.SHA, c.Event, c.Branch)
	}
	fmt.Fprintln(w)
}

func who(t *transcript.Transcript) string {
	s := &t.Session
	if s.AgentName != "" {
		return s.AgentName
	}
	return Ref(s)
}
