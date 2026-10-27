package engine

import (
	"fmt"

	"github.com/Ryheff24/agentlog/internal/clip"
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
		e := &r.Events[i]
		fields := []*string{&e.Output, &e.Text, &e.Input}
		for j := range e.Changes {
			fields = append(fields, &e.Changes[j].Diff)
		}
		for _, f := range fields {
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
