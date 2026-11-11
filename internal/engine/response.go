package engine

import (
	"fmt"

	"github.com/Ryheff24/agentlog/internal/clip"
	"github.com/Ryheff24/agentlog/internal/term"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Response is what --json emits. It is never the raw transcript: a transcript
// is a log format, not an answer. It has no budget, no ref to follow, and no
// account of what it left out — the three things a reader needs most when what
// it is reading does not fit.
type Response struct {
	Provenance string                `json:"provenance,omitempty"`
	Ref        string                `json:"ref,omitempty"`   // what re-opens this
	Query      string                `json:"query,omitempty"` // the clues, as understood
	Session    *transcript.Session   `json:"session,omitempty"`
	Sessions   []*transcript.Session `json:"sessions,omitempty"`
	Usage      *transcript.Usage     `json:"usage,omitempty"`
	Tools      []ToolUse             `json:"tools,omitempty"`
	Commits    []Commit              `json:"commits,omitempty"`
	Health     any                   `json:"health,omitempty"` // agentlog doctor
	Events     []transcript.Event    `json:"events,omitempty"`
	Counts     *Counts               `json:"counts,omitempty"`
	Notices    []string              `json:"notices,omitempty"`
	Truncated  []Cut                 `json:"truncated,omitempty"`
	Next       []string              `json:"next,omitempty"`
}

// Counts is the arithmetic behind a selection, so a reader can tell "there are
// no more" from "there are more and you did not get them".
type Counts struct {
	Total   int `json:"total_events"`
	Matched int `json:"matched"`
	Shown   int `json:"shown"`
}

// Cut is one thing left out and the exact command that returns it. A
// truncation that does not say how to undo itself is a dead end.
type Cut struct {
	What  string `json:"what"`
	Count int    `json:"count,omitempty"`
	Bytes int    `json:"bytes,omitempty"`
	Get   string `json:"get,omitempty"`
}

// FromSelection builds a response for one session's events.
func FromSelection(sel Selection, ref, query string, t *transcript.Transcript) *Response {
	r := &Response{
		Ref:     ref,
		Query:   query,
		Session: sel.Session,
		Events:  sel.Events,
		Counts:  &Counts{Total: sel.Total, Matched: sel.Matched, Shown: len(sel.Events)},
		Notices: t.Notes,
	}
	if d := Drift(t); d != "" {
		r.Notices = append(r.Notices, d)
	}
	if n := sel.Dropped(); n > 0 {
		get := sel.Continuation(ref)
		r.Truncated = append(r.Truncated, Cut{
			What:  "events that matched but fell outside the page",
			Count: n,
			Get:   get,
		})
	}
	if next := sel.Continuation(ref); next != "" {
		r.Next = append(r.Next, next)
	}
	if from, to := sel.Range(); to > 0 {
		r.Next = append(r.Next, fmt.Sprintf("agentlog %s#%d-%d%s", ref, from, to, sel.filter.Flags))
	}

	return r
}

// ClipLongFields bounds individual messages, tool outputs and inputs without
// imposing a cumulative cap on the response or dropping whole events.
func (r *Response) ClipLongFields(maxBytes int) {
	if maxBytes <= 0 {
		return
	}
	clipped, bytes := 0, 0
	for i := range r.Events {
		for _, f := range longFields(&r.Events[i]) {
			if n := len(*f); n > maxBytes {
				*f = clip.Block(*f, maxBytes)
				bytes += n - len(*f)
				clipped++
			}
		}
	}
	if clipped > 0 {
		r.Truncated = append(r.Truncated, Cut{
			What:  "long fields clipped",
			Count: clipped,
			Bytes: bytes,
			Get:   ref(r, "%s#N --full"),
		})
	}
}

// longFields are the fields of an event that can hold a whole tool result,
// message or diff.
func longFields(e *transcript.Event) []*string {
	fields := []*string{&e.Output, &e.Text, &e.Input}
	for j := range e.Changes {
		fields = append(fields, &e.Changes[j].Diff)
	}
	return fields
}

// HideBinary replaces each long event field that is mostly binary with a
// one-line stand-in naming the command that returns it as logged, and says
// how many it replaced. maxBytes is the clip that will follow, 0 for none.
// Run it before ClipLongFields, so the judgement is made on the whole field.
func (r *Response) HideBinary(maxBytes int) {
	hidden, bytes := 0, 0
	for i := range r.Events {
		e := &r.Events[i]
		for _, f := range longFields(e) {
			c, binary := Opaque(*f, maxBytes)
			if !binary {
				continue
			}
			*f = term.StandIn(c, BytesGet(r.Ref, e.Index, maxBytes > 0 && len(*f) > maxBytes, true))
			hidden++
			bytes += c.Bytes
		}
	}
	if hidden > 0 {
		r.Truncated = append(r.Truncated, Cut{
			What:  "binary fields not shown",
			Count: hidden,
			Bytes: bytes,
			Get:   BytesGet(r.Ref, 0, false, true),
		})
	}
}

// Opaque measures a field and reports whether it is binary, judged on the
// whole field and on what a clip at max keeps of it, so a binary head on a
// long text does not slip through a clipped view.
func Opaque(s string, max int) (term.Counts, bool) {
	c := term.Count(s)
	if c.Binary() {
		return c, true
	}
	if max > 0 && len(s) > max && term.Count(clip.Block(s, max)).Binary() {
		return c, true
	}
	return c, false
}

// BytesGet is the command that prints event index of ref as logged; index 0
// stands for any event. long adds --full, for a field past the clip.
func BytesGet(ref string, index int, long, json bool) string {
	if ref == "" {
		ref = "<ref>"
	}
	n := "N"
	if index > 0 {
		n = fmt.Sprint(index)
	}
	get := fmt.Sprintf("agentlog %s#%s --bytes", ref, n)
	if long {
		get += " --full"
	}
	if json {
		get += " --json"
	}
	return get
}

func ref(r *Response, format string) string {
	if r.Ref == "" {
		return ""
	}
	return "agentlog " + fmt.Sprintf(format, r.Ref)
}

// Continuation opens the adjacent matching page, excluding the current page.
func (s Selection) Continuation(ref string) string {
	if s.Dropped() <= 0 {
		return ""
	}
	from, to := s.Range()
	if s.headed {
		return fmt.Sprintf("agentlog %s#%d-%s --head %d", ref, to+1, s.filter.Flags, s.filter.Head)
	}
	return fmt.Sprintf("agentlog %s#1-%d%s --tail %d", ref, from-1, s.filter.Flags, s.filter.Tail)
}
