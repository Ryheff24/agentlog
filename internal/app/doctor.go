package app

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"agentlog/internal/catalog"
	"agentlog/internal/clip"
	"agentlog/internal/engine"
	"agentlog/internal/render"
	"agentlog/internal/spec"
	"agentlog/internal/transcript"
	"agentlog/internal/work"
)

// doctor reports how much of each harness's logs this build can still read.
//
// The providers change their formats without telling anyone, and the failure
// that costs most is the silent one: a reader that quietly stops understanding
// a third of a session and keeps answering questions about it. This is the
// command that makes that visible, and its output is meant to be enough to fix
// the reader from — the type, how often, which sessions, the exact ref of one,
// and what that record actually contains — so that nobody has to go and read
// raw logs to find out what changed.
func doctor(env Env, res *catalog.Result, opts options) error {
	limit := orDefault(opts.limit, doctorSessions)
	byProvider := map[string][]*transcript.Session{}
	for _, m := range res.Matches {
		p := m.Session.Provider
		if len(byProvider[p]) < limit {
			byProvider[p] = append(byProvider[p], m.Session)
		}
	}

	asked := map[string]bool{}
	for _, p := range res.Clues.Providers {
		asked[p] = true
	}
	var reports []health
	for _, h := range res.Harnesses {
		// `doctor codex` is about Codex; the others were not looked at, and
		// reporting them as empty would say something false about them.
		if len(asked) > 0 && !asked[h.Name] {
			continue
		}
		r := examine(env, byProvider[h.Name], opts)
		r.Provider, r.Present = h.Name, h.Present
		r.Known = len(spec.Of(h.Name).Records)
		reports = append(reports, r)
	}

	if opts.json {
		return writeJSON(env.Stdout, doctorResponse(reports, limit))
	}
	w := env.Stdout
	fmt.Fprintf(w, "how much of each harness's logs this build can read (newest %s per harness)\n\n", plural(limit, "session"))
	window := "no sessions"
	if !res.Since.IsZero() {
		window = fmt.Sprintf("no sessions since %s (add 'all' to look further back)", res.Since.Local().Format("Jan 2 15:04"))
	}
	for _, r := range reports {
		r.print(w, window)
	}
	fmt.Fprintln(w, "read = a record this build has a case for. The rest are still shown, in")
	fmt.Fprintln(w, "place and addressable by #N, as generic events read by shape.")
	fmt.Fprintln(w, "catalog covers persisted harness roots; ephemeral jobs without rollouts and relocated logs outside those roots are not included.")
	return nil
}

// doctorSessions is how many sessions per harness are examined by default.
// Drift is new, so the newest sessions are where it is; the whole corpus is a
// --limit away for anyone who wants it.
const doctorSessions = 25

// health is one harness's report.
type health struct {
	Provider string         `json:"provider"`
	Present  bool           `json:"present"`
	Sessions int            `json:"sessions"`
	Records  int            `json:"records"`
	Unread   int            `json:"unread_records"`
	Unpaired int            `json:"unpaired_results,omitempty"`
	Failed   int            `json:"unreadable_files,omitempty"`
	Known    int            `json:"known_types"`
	Versions map[string]int `json:"cli_versions,omitempty"`
	Types    []unread       `json:"unread_types,omitempty"`
}

// unread is one record type this build has no case for, with everything
// needed to write that case.
type unread struct {
	Type     string         `json:"type"`
	Count    int            `json:"count"`
	Sessions int            `json:"sessions"`
	Ref      string         `json:"sample_ref"`
	Sample   string         `json:"sample"`
	Version  string         `json:"cli_version,omitempty"`
	Versions map[string]int `json:"cli_versions,omitempty"`
	Declared string         `json:"declared_as,omitempty"` // a table entry exists but the reader has no case
}

// Coverage is the share of records this build has a reading for.
func (h health) Coverage() float64 {
	if h.Records == 0 {
		return 1
	}
	return float64(h.Records-h.Unread) / float64(h.Records)
}

// print writes one harness's report; empty is what to say when it had no
// sessions to examine.
func (h health) print(w io.Writer, empty string) {
	if !h.Present {
		fmt.Fprintf(w, "%-9s not installed, or no logs where this build looks\n\n", h.Provider)
		return
	}
	if h.Sessions == 0 {
		// Nothing was read, so there is no share of it to report.
		if h.Failed > 0 {
			empty = plural(h.Failed, "log file") + " could not be read at all"
		}
		fmt.Fprintf(w, "%-9s %s\n\n", h.Provider, empty)
		return
	}
	fmt.Fprintf(w, "%-9s %-36s %9s records  %5.1f%% read\n", h.Provider,
		fit(versions(h.Versions), 36), count(h.Records), 100*h.Coverage())
	fmt.Fprintf(w, "          %d record types in the table\n", h.Known)
	if h.Failed > 0 {
		fmt.Fprintf(w, "          %s could not be read at all\n", plural(h.Failed, "log file"))
	}
	if h.Unpaired > 0 {
		// Whole files were read here, so nothing is outside the window: these
		// are results whose call the pairing rule failed to find.
		fmt.Fprintf(w, "          %s whose call was not found, shown on their own\n", plural(h.Unpaired, "tool result"))
	}
	for _, u := range h.Types {
		fmt.Fprintf(w, "          %-34s ×%-6d in %-12s agentlog %s%s\n",
			u.Type, u.Count, plural(u.Sessions, "session"), u.Ref, declaredBy(u.Declared))
		if u.Sample != "" {
			fmt.Fprintf(w, "            %s\n", clip.Line(strings.ReplaceAll(u.Sample, "\n", " · "), 150))
		}
		fmt.Fprintf(w, "            observed versions: %s\n", versions(u.Versions))
	}
	fmt.Fprintln(w)
}

// examine parses a harness's newest sessions and adds up what was readable.
func examine(env Env, ss []*transcript.Session, opts options) health {
	h := health{Versions: map[string]int{}}
	if len(ss) == 0 {
		return h
	}
	load := loadOpts(opts)
	load.Branches = true // an abandoned record is still a record this build met
	ts := make([]*transcript.Transcript, len(ss))
	work.Do(env.ctx(), len(ss), func(i int) {
		if t, err := env.Catalog.Load(ss[i], load); err == nil {
			ts[i] = t
		}
	})

	types := map[string]*unread{}
	for _, t := range ts {
		if t == nil {
			h.Failed++
			continue
		}
		h.Sessions++
		h.Records += t.Records
		h.Unpaired += t.Unpaired
		h.Versions[t.Session.Version]++
		for name, n := range t.Unknowns {
			h.Unread += n
			u := types[name]
			if u == nil {
				u = &unread{Type: name, Version: t.Session.Version, Versions: map[string]int{}}
				types[name] = u
			}
			u.Count += n
			u.Versions[t.Session.Version]++
			u.Sessions++
			if u.Ref == "" {
				u.Ref, u.Sample = sampleOf(t, name)
				if r, ok := declared(t.Session.Provider, name); ok {
					u.Declared = string(r.Role)
				}
			}
		}
	}
	for _, u := range types {
		h.Types = append(h.Types, *u)
	}
	sort.Slice(h.Types, func(i, j int) bool {
		if a, b := h.Types[i].Count, h.Types[j].Count; a != b {
			return a > b
		}
		return h.Types[i].Type < h.Types[j].Type
	})
	return h
}

// sampleOf finds the stand-in event for one unread type and returns the ref
// that opens it and what it holds. The sample comes from the flattened record,
// so its long values are already clipped and its blobs already named — what is
// printed is field names and shapes, which is what a fix needs.
func sampleOf(t *transcript.Transcript, name string) (ref, sample string) {
	for _, e := range t.Events {
		if e.Kind != transcript.KindRecord && e.Tag != name {
			continue
		}
		if e.Tag != name {
			continue
		}
		body := e.Text
		if body == "" {
			body = strings.TrimSpace(e.Tool + " " + e.Input + " " + e.Output)
		}
		return fmt.Sprintf("%s#%d", render.Ref(&t.Session), e.Index), body
	}
	return render.Ref(&t.Session), ""
}

// declared finds a table entry for a type the reader could not read. When one
// exists, the table and the reader disagree, which is a different bug from a
// type nobody has heard of — and a much louder one.
func declared(provider, name string) (spec.Record, bool) {
	for _, r := range spec.Of(provider).Records {
		if r.Type == name {
			return r, true
		}
	}
	return spec.Record{}, false
}

func declaredBy(role string) string {
	if role == "" {
		return ""
	}
	return "   [declared as " + role + " in the table, but the reader has no case for it]"
}

func versions(m map[string]int) string {
	var names []string
	for v := range m {
		if v != "" {
			names = append(names, v)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if a, b := m[names[i]], m[names[j]]; a != b {
			return a > b
		}
		return names[i] > names[j]
	})
	var out []string
	for i, v := range names {
		if i == 2 {
			out = append(out, fmt.Sprintf("+%d more", len(names)-2))
			break
		}
		out = append(out, fmt.Sprintf("%s ×%d", v, m[v]))
	}
	if len(out) == 0 {
		return "version not recorded"
	}
	return strings.Join(out, ", ")
}

// fit truncates to a column width. clip.Line would add the bytes it dropped,
// which is worth knowing about a log line and only noise inside a table.
func fit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// count groups a number in threes.
func count(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func doctorResponse(reports []health, limit int) *engine.Response {
	r := &engine.Response{Query: "doctor"}
	worst := 1.0
	for _, h := range reports {
		if h.Present && h.Sessions > 0 && h.Coverage() < worst {
			worst = h.Coverage()
		}
	}
	r.Notices = append(r.Notices, fmt.Sprintf("examined the newest %s per harness; %.1f%% of records read by the worst harness", plural(limit, "session"), 100*worst))
	r.Health = reports
	r.Next = []string{"agentlog doctor --limit 200", "agentlog <ref>#N"}
	return r
}
