package render

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentlog/internal/catalog"
	"agentlog/internal/clip"
	"agentlog/internal/engine"
	"agentlog/internal/transcript"
)

// Preview prints the overview of one session: identity, how it ended, what
// was asked, what its subagents reported, what it changed, and its last steps.
func Preview(w io.Writer, t *transcript.Transcript, kids []*transcript.Transcript, alternatives []catalog.Match, o Options) {
	s := &t.Session
	ref := Ref(s)
	cwd := s.CWD
	o.CWD = cwd

	title := ""
	if s.Title != "" {
		title = ` "` + s.Title + `"`
	}
	fmt.Fprintf(w, "%s %s%s%s\n", s.Provider, s.ID, title, Own(s))
	if s.ParentID != "" {
		fmt.Fprintf(w, "subagent  %s of parent %s\n", agentLabel(s), ShortID(s.ParentID))
	}
	git := ""
	if s.Branch != "" || s.Commit != "" {
		git = fmt.Sprintf(" (git %s", s.Branch)
		if len(s.Commit) >= 7 {
			git += " @" + s.Commit[:7]
		}
		git += " at start)"
	}
	moved := ""
	if len(s.Dirs) > 0 {
		earlier := make([]string, len(s.Dirs))
		for i, d := range s.Dirs {
			earlier[i] = tilde(d)
		}
		moved = " (started in " + strings.Join(earlier, ", then ") + ")"
	}
	fmt.Fprintf(w, "dir       %s%s%s\n", tilde(cwd), moved, git)
	if s.LastEvent.IsZero() {
		s.LastEvent = transcript.Newest(t.Events)
	}
	start := s.Started
	if q := quietSince(s); !q.IsZero() {
		fmt.Fprintf(w, "time      %s → %s (last event %s; log written %s with nothing new)\n", day(start), day(q), ago(o.Now, q), ago(o.Now, s.Updated))
	} else {
		fmt.Fprintf(w, "time      %s → %s (last write %s)\n", day(start), day(s.Updated), ago(o.Now, s.Updated))
	}
	meta := []string{clip.Size(int(s.Size))}
	if s.Model != "" {
		meta = append(meta, s.Model)
	}
	if s.Origin != "" {
		meta = append(meta, s.Origin)
	}
	fmt.Fprintf(w, "log       %s · %s\n", strings.Join(meta, " · "), tilde(s.File()))
	if len(s.Files) > 1 {
		pages := 0
		for _, f := range s.Files[:len(s.Files)-1] {
			// A log the session left in another project folder is where its
			// earlier turns are; say where.
			if filepath.Dir(f) != filepath.Dir(s.File()) {
				fmt.Fprintf(w, "          also %s\n", tilde(f))
			} else {
				pages++
			}
		}
		if pages > 0 {
			fmt.Fprintf(w, "          (+%s)\n", plural(pages, "earlier page file"))
		}
	}
	if s.Live {
		fmt.Fprintf(w, "state     RUNNING — log written %s; content below may be incomplete\n", ago(o.Now, s.Updated))
	} else {
		fmt.Fprintf(w, "state     %s\n", endingText(s))
	}

	switch {
	case s.ContinuedFrom != "":
		fmt.Fprintf(w, "from      %s, compacted into this session; its earlier turns are there (agentlog %s)\n", ShortID(s.ContinuedFrom), ShortID(s.ContinuedFrom))
	case s.Continues:
		fmt.Fprintln(w, "from      an earlier session, compacted into this one; its log is not on disk, so the turns before #1 are gone")
	}
	if s.ContinuedIn != "" {
		fmt.Fprintf(w, "then      continued in %s, which carries on from here (agentlog %s)\n", ShortID(s.ContinuedIn), ShortID(s.ContinuedIn))
	}

	if line := commitLine(t, ref); line != "" {
		fmt.Fprintf(w, "commits   %s\n", line)
	}
	fmt.Fprintf(w, "evidence  %s\n", engine.Provenance(t))
	if line := toolLine(t, ref); line != "" {
		fmt.Fprintf(w, "tools     %s\n", line)
	}
	if line := spendLine(t.Usage, ref, o.Now); line != "" {
		fmt.Fprintf(w, "spent     %s\n", line)
	}

	fmt.Fprintln(w)
	census(w, t)
	TranscriptNotes(w, t)
	Skipped(w, t, "note      ")
	if t.Goal != "" {
		fmt.Fprintf(w, "goal      %s\n", clip.Line(t.Goal, o.width()*2))
	}
	if len(t.Plan) > 0 {
		fmt.Fprintln(w, "plan (latest):")
		for _, st := range t.Plan {
			fmt.Fprintf(w, "  %s %s\n", statusMark(st.Status), clip.Line(st.Text, o.width()))
		}
	}

	prompts(w, t, o)
	subagents(w, s, kids, o)
	edits(w, t, kids, o)
	lastEvents(w, t, o, 12)
	finalMessage(w, t, o)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "next:")
	fmt.Fprintf(w, "  agentlog %s#N               one event in full (tool output, whole message)\n", ref)
	fmt.Fprintf(w, "  agentlog %s --user           human turns only (pastes marked when the log identifies them)\n", ref)
	fmt.Fprintf(w, "  agentlog %s --tail 60       timeline; narrow with --kind user,assistant or --grep REGEX\n", ref)
	if countKind(t, transcript.KindEdit) > 0 || len(s.Children) > 0 {
		fmt.Fprintf(w, "  agentlog %s --edits         diffs of every file change (--subagents includes theirs)\n", ref)
	}
	for _, c := range s.Children {
		if c.AgentRole == "guardian" {
			continue
		}
		fmt.Fprintf(w, "  agentlog %s                 %s\n", Ref(c), strings.TrimSpace("subagent "+agentLabel(c)))
	}
	if len(alternatives) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "other matches:")
		for i, m := range alternatives {
			if i == 5 {
				fmt.Fprintf(w, "  … %d more (--list)\n", len(alternatives)-5)
				break
			}
			a := m.Session
			fmt.Fprintf(w, "  %s  %-7s %s  %s  %s\n", Ref(a), a.Provider, stamp(o.Now, a.Updated), tilde(a.CWD), clip.Line(sessionLabel(a), 70))
		}
	}
}

// Skipped reports log records that produced no event. Unrecognised types get
// their own line: they are how a new harness release shows up.
func Skipped(w io.Writer, t *transcript.Transcript, prefix string) {
	if n := total(t.Unknowns); n > 0 {
		fmt.Fprintf(w, "%s%s of unrecognised type (%s), shown as generic events — this reader may be behind the harness; run: agentlog doctor\n",
			prefix, plural(n, "record"), topSkips(t.Unknowns, 4))
	}
	if n := total(t.Ignored); n > 0 {
		fmt.Fprintf(w, "%s%s not modelled (%s)\n", prefix, plural(n, "log record"), topSkips(t.Ignored, 3))
	}
}

func total(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// topSkips names the most common skipped types, newest format drift first.
func topSkips(m map[string]int, max int) string {
	skips := (&transcript.Transcript{Ignored: m}).Skips()
	parts := make([]string, 0, max+1)
	for i, s := range skips {
		if i == max {
			parts = append(parts, fmt.Sprintf("+%d more", len(skips)-max))
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", s.Type, s.Count))
	}
	return strings.Join(parts, ", ")
}

func census(w io.Writer, t *transcript.Transcript) {
	counts := map[transcript.Kind]int{}
	failed := map[transcript.Kind]int{}
	for _, e := range t.Events {
		counts[e.Kind]++
		if e.Failed {
			failed[e.Kind]++
		}
	}
	parts := []string{}
	for _, k := range transcript.AllKinds {
		if counts[k] == 0 {
			continue
		}
		p := fmt.Sprintf("%d %s", counts[k], k)
		if n := failed[k]; n > 0 && (k == transcript.KindTool || k == transcript.KindEdit) {
			p += fmt.Sprintf(" (%d failed)", n)
		}
		parts = append(parts, p)
	}
	fmt.Fprintf(w, "events    %d: %s\n", len(t.Events), strings.Join(parts, " · "))
	// Claude Code writes a compaction as a boundary and then the summary it
	// handed the model: two notices for one compaction. Nothing happens
	// between them, so a compaction notice right after another is the same
	// compaction.
	var compactions []string
	for i, e := range t.Events {
		if e.Tag == transcript.TagCompaction && (i == 0 || t.Events[i-1].Tag != transcript.TagCompaction) {
			compactions = append(compactions, fmt.Sprintf("#%d", e.Index))
		}
	}
	// The compaction a continuation opens on happened in the other session,
	// and its history is there; the "from" line says so.
	if t.Session.Continues && len(compactions) > 0 {
		compactions = compactions[1:]
	}
	if len(compactions) > 0 {
		fmt.Fprintf(w, "note      context compacted at %s (full history is still in the log)\n", strings.Join(compactions, ", "))
	}
}

func prompts(w io.Writer, t *transcript.Transcript, o Options) {
	var ps []transcript.Event
	for _, e := range t.Events {
		if e.Kind == transcript.KindUser {
			ps = append(ps, e)
		}
	}
	if len(ps) == 0 {
		return
	}
	fmt.Fprintln(w)
	const keepLast = 5
	if len(ps) <= keepLast+1 {
		fmt.Fprintf(w, "prompts (%d):\n", len(ps))
	} else {
		fmt.Fprintf(w, "prompts (%d; first and last %d):\n", len(ps), keepLast)
	}
	for i, e := range ps {
		if len(ps) > keepLast+1 && i > 0 && i < len(ps)-keepLast {
			if i == 1 {
				fmt.Fprintf(w, "  … %d more (--kind user)\n", len(ps)-keepLast-1)
			}
			continue
		}
		width := o.width()
		if i == len(ps)-1 {
			width *= 3 // the latest request usually defines what is unfinished
		}
		fmt.Fprintf(w, "  #%-4d %s  %s\n", e.Index, stamp(o.Now, e.Time), clip.Line(e.Text, width))
	}
}

func subagents(w io.Writer, s *transcript.Session, kids []*transcript.Transcript, o Options) {
	guardians := 0
	for _, c := range s.Children {
		if c.AgentRole == "guardian" {
			guardians++
		}
	}
	if len(kids) == 0 && guardians == 0 {
		return
	}
	fmt.Fprintln(w)
	header := fmt.Sprintf("subagents (%d", len(kids))
	if guardians > 0 {
		header += fmt.Sprintf("; %d approval-review threads hidden", guardians)
	}
	fmt.Fprintln(w, header+"):")
	for _, k := range kids {
		c := &k.Session
		state := endingText(c)
		if c.Live {
			state = "RUNNING"
		}
		fmt.Fprintf(w, "  %s  %s  %s → %s  %s  %s\n", Ref(c), agentLabel(c),
			stamp(o.Now, c.Started), stamp(o.Now, c.Updated), clip.Size(int(c.Size)), state)
		if c.Title != "" && c.Title != c.AgentName {
			fmt.Fprintf(w, "      task: %s\n", clip.Line(c.Title, o.width()))
		} else if task := firstReadable(k, transcript.KindUser, transcript.KindAgent); task != nil {
			fmt.Fprintf(w, "      task #%d: %s\n", task.Index, clip.Line(task.Text, o.width()))
		}
		if n := countKind(k, transcript.KindEdit); n > 0 {
			fmt.Fprintf(w, "      %s\n", plural(n, "edit"))
		}
		final, last := lastAssistant(k, "final"), lastAssistant(k, "")
		if final != nil {
			fmt.Fprintf(w, "      final answer #%d %s: %s\n", final.Index, stamp(o.Now, final.Time), clip.Line(final.Text, o.width()*2))
		}
		if last != nil && (final == nil || last.Index > final.Index) {
			fmt.Fprintf(w, "      latest message #%d %s: %s\n", last.Index, stamp(o.Now, last.Time), clip.Line(last.Text, o.width()*2))
		}
	}
}

// firstReadable returns the first event of the given kinds with plaintext.
func firstReadable(t *transcript.Transcript, kinds ...transcript.Kind) *transcript.Event {
	for i := range t.Events {
		e := &t.Events[i]
		for _, k := range kinds {
			if e.Kind == k && e.Text != "" && e.Text != "[encrypted]" {
				return e
			}
		}
	}
	return nil
}

// lastAssistant returns the last assistant message, optionally with a tag.
func lastAssistant(t *transcript.Transcript, tag string) *transcript.Event {
	for i := len(t.Events) - 1; i >= 0; i-- {
		e := &t.Events[i]
		if e.Kind == transcript.KindAssistant && (tag == "" || e.Tag == tag) {
			return e
		}
	}
	return nil
}

type fileStat struct {
	path           string
	edits, add, rm int
	failed         int
	last           time.Time
	lastRef        string
	by             map[string]bool
}

func edits(w io.Writer, t *transcript.Transcript, kids []*transcript.Transcript, o Options) {
	stats := map[string]*fileStat{}
	for _, tt := range append([]*transcript.Transcript{t}, kids...) {
		by := "main"
		if tt != t {
			by = firstNonEmpty(tt.Session.AgentName, Ref(&tt.Session))
		}
		for _, e := range tt.Events {
			if e.Kind != transcript.KindEdit {
				continue
			}
			for _, c := range e.Changes {
				fs := stats[c.Path]
				if fs == nil {
					fs = &fileStat{path: c.Path, by: map[string]bool{}}
					stats[c.Path] = fs
				}
				fs.edits++
				fs.by[by] = true
				if !e.Time.Before(fs.last) {
					fs.last, fs.lastRef = e.Time, fmt.Sprintf("%s#%d", Ref(&tt.Session), e.Index)
				}
				if e.Failed {
					fs.failed++
					continue
				}
				fs.add += c.Added
				fs.rm += c.Removed
			}
		}
	}
	if len(stats) == 0 {
		return
	}
	list := make([]*fileStat, 0, len(stats))
	for _, fs := range stats {
		list = append(list, fs)
	}
	// Ties break on the path: two files edited in the same second must not
	// swap places between runs.
	sort.Slice(list, func(i, j int) bool {
		if !list[i].last.Equal(list[j].last) {
			return list[i].last.After(list[j].last)
		}
		return list[i].path < list[j].path
	})
	fmt.Fprintln(w)
	scope := ""
	if len(kids) > 0 {
		scope = ", including subagents"
	}
	// A file whose every edit failed was not changed; it is listed, since the
	// attempt is worth knowing about, but not counted.
	changed := 0
	for _, fs := range list {
		if fs.failed < fs.edits {
			changed++
		}
	}
	attempted := ""
	if n := len(list) - changed; n > 0 {
		attempted = fmt.Sprintf("; %d only attempted", n)
	}
	fmt.Fprintf(w, "files changed (%d%s%s; most recent first):\n", changed, attempted, scope)
	for i, fs := range list {
		if i == 12 {
			more := "--edits --files"
			if len(kids) > 0 {
				more += " --subagents"
			}
			fmt.Fprintf(w, "  … %d more (%s)\n", len(list)-12, more)
			break
		}
		what := fmt.Sprintf("+%d -%d  %s", fs.add, fs.rm, plural(fs.edits, "edit"))
		if fs.failed == fs.edits {
			what = "not changed: " + plural(fs.failed, "failed edit")
		} else if fs.failed > 0 {
			what += fmt.Sprintf(" (%d failed)", fs.failed)
		}
		if len(kids) > 0 {
			names := make([]string, 0, len(fs.by))
			for n := range fs.by {
				names = append(names, n)
			}
			sort.Strings(names)
			what += " by " + strings.Join(names, ",")
		}
		fmt.Fprintf(w, "  %-45s %s  last %s %s\n", relTo(fs.path, o.CWD), what, stamp(o.Now, fs.last), fs.lastRef)
	}
}

func lastEvents(w io.Writer, t *transcript.Transcript, o Options, n int) {
	var shown []transcript.Event
	opaque := 0
	for i := len(t.Events) - 1; i >= 0 && len(shown) < n; i-- {
		e := t.Events[i]
		if e.Kind == transcript.KindReasoning {
			continue
		}
		if e.Kind == transcript.KindAgent && e.Text == "[encrypted]" && e.Input == "" {
			opaque++
			continue
		}
		shown = append(shown, e)
	}
	if len(shown) == 0 {
		return
	}
	fmt.Fprintln(w)
	omitted := "reasoning"
	if opaque > 0 {
		omitted += " and " + plural(opaque, "encrypted inter-agent message")
	}
	fmt.Fprintf(w, "last %d events (%s omitted):\n", len(shown), omitted)
	c := &clock{}
	for i := len(shown) - 1; i >= 0; i-- {
		fmt.Fprintln(w, "  "+eventLine(shown[i], o, c))
	}
}

// finalMessage prints the last assistant message in full (bounded), since it
// usually states what was done and what remains.
func finalMessage(w io.Writer, t *transcript.Transcript, o Options) {
	for i := len(t.Events) - 1; i >= 0; i-- {
		e := t.Events[i]
		if e.Kind != transcript.KindAssistant {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "last assistant message #%d (%s):\n", e.Index, stamp(o.Now, e.Time))
		indent(w, clip.Block(e.Text, min(o.max(), 2500)), "  ")
		return
	}
}

func countKind(t *transcript.Transcript, k transcript.Kind) int {
	n := 0
	for _, e := range t.Events {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// TranscriptNotes prints what the parser left out of a session, such as
// turns hidden on rewound branches. Every view of a session's events prints
// them: a search that found nothing has to say where it did not look.
func TranscriptNotes(w io.Writer, t *transcript.Transcript) {
	for _, n := range t.Notes {
		fmt.Fprintf(w, "note      %s\n", n)
	}
}
