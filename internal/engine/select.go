// Package engine answers questions about transcripts. It decides which events
// a view shows, what was left out and why, and what to run next — all of it on
// typed events, with no opinion about how any of it is printed.
//
// The rule that shapes it: nothing here may depend on rendered text. A search
// that decides whether it found something by looking for a "#" in its own
// output cannot say why an event was dropped, cannot be reused by a second
// frontend, and breaks the moment the text changes.
package engine

import (
	"regexp"
	"strings"

	"agentlog/internal/transcript"
)

// Filter is the question a view asks of a transcript.
type Filter struct {
	Flags            string // shell-quoted view flags to retain in follow-up commands
	IncludeHandbacks bool
	Kinds            map[transcript.Kind]bool // empty: every kind except reasoning
	Grep             *regexp.Regexp
	Path             *regexp.Regexp // edits: only changes to matching paths
	Head             int            // keep the first N of what matched
	Tail             int            // keep the last N of what matched
	Reasoning        bool           // include reasoning when Kinds is empty
}

// Selection is what a view will show and what it left out. Counts are kept
// apart because they answer different questions: an agent that sees 40 of 900
// events needs to know whether the other 860 failed the filter or fell off the
// end of the page.
type Selection struct {
	Session  *transcript.Session `json:"session,omitempty"`
	Events   []transcript.Event  `json:"events"`
	Total    int                 `json:"total"`   // events in the transcript
	Matched  int                 `json:"matched"` // events passing the filter
	Snippets map[int][]string    `json:"snippets,omitempty"`

	// headed is set when --head cut the page, so what fell off it is after
	// the page, not before it as with --tail.
	headed bool
	filter Filter
}

// Any reports whether anything survived the filter.
func (s Selection) Any() bool { return len(s.Events) > 0 }

// Dropped is how many events matched but did not fit the head or tail.
func (s Selection) Dropped() int { return s.Matched - len(s.Events) }

// Range returns the event numbers at each end of the selection.
func (s Selection) Range() (from, to int) {
	if len(s.Events) == 0 {
		return 0, 0
	}
	return s.Events[0].Index, s.Events[len(s.Events)-1].Index
}

// snippetsPerEvent bounds how much of a match is quoted back, per event.
const snippetsPerEvent = 2

// Select applies a filter to a transcript.
func Select(t *transcript.Transcript, f Filter) Selection {
	sel := Selection{Session: &t.Session, Total: len(t.Events), filter: f}
	for _, e := range t.Events {
		if f.keep(e) {
			sel.Events = append(sel.Events, e)
		}
	}
	sel.Matched = len(sel.Events)
	switch {
	case f.Head > 0 && len(sel.Events) > f.Head:
		sel.Events, sel.headed = sel.Events[:f.Head], true
	case f.Tail > 0 && len(sel.Events) > f.Tail:
		sel.Events = sel.Events[len(sel.Events)-f.Tail:]
	}
	if f.Grep != nil {
		sel.Snippets = map[int][]string{}
		for _, e := range sel.Events {
			if s := Snippets(f.Grep, e, snippetsPerEvent); len(s) > 0 {
				sel.Snippets[e.Index] = s
			}
		}
	}
	return sel
}

// Keeps reports whether an event passes the filter's kind, path and pattern
// tests, leaving out the head and tail limits.
func (f Filter) Keeps(e transcript.Event) bool { return f.keep(e) }

func (f Filter) keep(e transcript.Event) bool {
	if e.RepeatOf > 0 {
		return false
	}
	if len(f.Kinds) > 0 {
		if !f.Kinds[e.Kind] && !(f.Kinds["owner"] && e.Human) && !(f.Kinds["refusal"] && e.Kind == transcript.KindError && e.Tag == "refusal") {
			return false
		}
	} else if e.Kind == transcript.KindReasoning && !f.Reasoning {
		return false
	}
	if f.Path != nil && !changesMatch(f.Path, e.Changes, pathOf) {
		return false
	}
	if f.Grep != nil {
		if e.Handback && !f.IncludeHandbacks {
			return false
		}
		return Matches(f.Grep, e)
	}
	return true
}

// Matches reports whether a pattern appears anywhere in an event's content.
//
// The text is whitespace-folded first, because a person searching for a
// sentence they remember types it with spaces, and the log wrapped it wherever
// the terminal did. Searching for "UPDATE issue SET title" over the corpus
// found nothing until this fold, and the sentence was there.
func Matches(re *regexp.Regexp, e transcript.Event) bool {
	for _, field := range fields(e) {
		if re.MatchString(fold(field)) {
			return true
		}
	}
	return changesMatch(re, e.Changes, allOf)
}

// fields is everything of an event a search looks at.
func fields(e transcript.Event) []string {
	return []string{e.Text, e.Input, e.Output, e.Tool, e.Agent}
}

// fold collapses every run of whitespace to one space. It leaves a string
// that has none alone, which is most of them.
func fold(s string) string {
	if !needsFold(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := range len(s) {
		if c := s[i]; c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\v' || c == '\f' {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		b.WriteByte(s[i])
		space = false
	}
	return strings.TrimRight(b.String(), " ")
}

func needsFold(s string) bool {
	for i := range len(s) {
		switch c := s[i]; c {
		case '\n', '\t', '\r', '\v', '\f':
			return true
		case ' ':
			if i+1 < len(s) && s[i+1] == ' ' {
				return true
			}
		}
	}
	return false
}

func pathOf(c transcript.Change) string { return c.Path + "\n" + c.MoveTo }
func allOf(c transcript.Change) string  { return c.Path + "\n" + c.MoveTo + "\n" + c.Diff }

func changesMatch(re *regexp.Regexp, cs []transcript.Change, text func(transcript.Change) string) bool {
	for _, c := range cs {
		if re.MatchString(fold(text(c))) {
			return true
		}
	}
	return false
}

// contextBytes is how much of the surrounding line a quoted match carries.
const contextBytes = 80

// Snippets returns the text around a pattern's matches in one event, so a hit
// list can show what matched rather than only where.
func Snippets(re *regexp.Regexp, e transcript.Event, max int) []string {
	var out []string
	for _, field := range append(fields(e), diffText(e.Changes)) {
		field = fold(field)
		for _, loc := range re.FindAllStringIndex(field, max) {
			start, end := loc[0]-contextBytes, loc[1]+contextBytes
			if start < 0 {
				start = 0
			}
			if end > len(field) {
				end = len(field)
			}
			out = append(out, strings.ToValidUTF8(strings.TrimSpace(field[start:end]), ""))
			if len(out) == max {
				return out
			}
		}
	}
	return out
}

func diffText(cs []transcript.Change) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Path)
		b.WriteByte('\n')
		b.WriteString(c.Diff)
		b.WriteByte('\n')
	}
	return b.String()
}
