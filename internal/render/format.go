// Package render prints catalog results and transcripts as compact text meant
// to be read by agents: stable refs on every line, bounded sizes, and the
// next commands spelled out.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/clip"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Options control output size.
type Options struct {
	Brief     bool
	Now       time.Time
	CWD       string
	Width     int  // single-line preview width in runes
	Max       int  // byte budget for one expanded block (message, output, diff)
	Full      bool // disable truncation of expanded blocks
	Reasoning bool // include reasoning in timelines
	// Keep, when set, limits a range of events to the ones it accepts.
	Keep func(transcript.Event) bool
	Next string // filtered continuation supplied by the selection
}

func (o Options) max() int {
	if o.Full {
		return 0
	}
	if o.Max <= 0 {
		return 6000
	}
	return o.Max
}

func (o Options) width() int {
	if o.Width <= 0 {
		return 150
	}
	return o.Width
}

// ShortID is the ref form of an id, accepted anywhere a full id is. Random
// UUIDs are unique in 8 characters; time-ordered UUIDv7 (Codex) start with a
// millisecond timestamp, so siblings spawned together need all 12 of those
// hex digits.
func ShortID(id string) string {
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		return id
	}
	if id[14] == '7' {
		return id[:13]
	}
	return id[:8]
}

// Ref returns the argument that re-opens a session.
func Ref(s *transcript.Session) string { return ShortID(s.ID) }

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func stamp(now, t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	t = t.Local()
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2 15:04")
	}
	return t.Format("2006-01-02 15:04")
}

// hhmmss is the exact time of an event, which the expanded views print
// because nothing else in the output carries it.
func hhmmss(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Local().Format("15:04:05")
}

var home, _ = os.UserHomeDir()

// tilde shortens paths under the home directory.
func tilde(p string) string {
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// RelTo renders p relative to dir when p is inside it.
func RelTo(p, dir string) string { return relTo(p, dir) }

// relTo renders p relative to dir when p is inside it.
func relTo(p, dir string) string {
	if dir != "" {
		if rel, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return tilde(p)
}

// subject renders a call's arguments for a one-line view. The tool name has
// already said what the arguments mean, so their JSON keys are dropped; paths
// are shown relative to the session's directory and ids are cut to the prefix
// that agentlog accepts back. The arguments as they were logged stay one `#N`
// away.
func subject(in, cwd string) string {
	if v, ok := argValues(in); ok {
		in = v
	}
	return shortIDs(relPaths(in, cwd))
}

// subjectKeys are the argument names that identify a call, in the order they
// read best. Anything else is schema noise on a one-line view.
var subjectKeys = []string{"command", "cmd", "file_path", "path", "pattern", "query", "url", "prompt", "description", "old_string", "text"}

func argValues(in string) (string, bool) {
	in = strings.TrimSpace(in)
	if !strings.HasPrefix(in, "{") || !strings.HasSuffix(in, "}") {
		return "", false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(in), &m); err != nil || len(m) == 0 {
		return "", false
	}
	var parts []string
	for _, k := range subjectKeys {
		if s, ok := m[k].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	// A call whose arguments are named nothing familiar keeps its keys: without
	// one recognised name there is nothing to say which value is which.
	if len(parts) == 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(parts) == 3 {
				parts = append(parts, fmt.Sprintf("+%d more", len(keys)-3))
				break
			}
			parts = append(parts, k+"="+valueText(m[k]))
		}
	}
	return strings.Join(parts, " "), true
}

func valueText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// relPaths shortens the absolute paths inside a line. Every path in a session
// tends to share its working directory, which the header already states.
func relPaths(s, cwd string) string {
	if cwd != "" && strings.Contains(s, cwd) {
		s = strings.ReplaceAll(s, cwd+"/", "")
		s = strings.ReplaceAll(s, cwd, ".")
	}
	if home != "" {
		s = strings.ReplaceAll(s, home+"/", "~/")
	}
	return s
}

var uuidRe = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

// shortIDs replaces full UUIDs with the ref form. A UUID costs ~38 Claude
// tokens and the short form is the one that can be typed back.
func shortIDs(s string) string {
	if !strings.Contains(s, "-") {
		return s
	}
	return uuidRe.ReplaceAllStringFunc(s, ShortID)
}

// lastChars keeps the end of a string, which is where names that share a long
// prefix — MCP tools, generated ids — differ from each other.
func lastChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n+1:]
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// indent prefixes every line of s.
func indent(w io.Writer, s, prefix string) {
	for _, ln := range strings.Split(s, "\n") {
		fmt.Fprintln(w, prefix+ln)
	}
}

// endingText explains an Ending value in words.
func endingText(s *transcript.Session) string {
	switch s.Ending {
	case transcript.EndingDone:
		return "idle after a reply (turn finished)"
	case transcript.EndingUsageLimit:
		return "stopped on a usage/rate limit"
	case transcript.EndingInterrupted:
		return "interrupted"
	case transcript.EndingError:
		return "stopped on an error"
	case transcript.EndingMidTool:
		return "stopped mid tool call (no result recorded)"
	case transcript.EndingWorking:
		return "stopped mid-turn (last event is not a final reply)"
	case transcript.EndingContinued:
		return "continued in " + ShortID(s.ContinuedIn)
	}
	return "unknown"
}

// clock prints a time only when the minute changes. Consecutive events in a
// log nearly always share one, and a repeated timestamp costs the reader about
// ten tokens to learn nothing.
type clock struct{ last string }

// only returns s the first time it differs from the last thing it returned.
func (c *clock) only(s string) string {
	if s == c.last {
		return ""
	}
	c.last = s
	return s
}

func (c *clock) at(now, t time.Time) string { return c.only(stamp(now, t)) }

// day returns the date of t once per calendar day, for views that print the
// time of day on every line.
func (c *clock) day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return c.only(t.Local().Format("2006-01-02"))
}

// eventLine renders one event as a single line. Every column is paid for by
// the agent reading the thread, so the kind word appears only when the body
// does not already name itself, and the exact time stays one `#N` away.
func eventLine(e transcript.Event, o Options, c *clock) string {
	head := "#" + strconv.Itoa(e.Index)
	if at := c.at(o.Now, e.Time); at != "" {
		head += " " + at
	}
	if !named(e) {
		head += " " + string(e.Kind)
	}
	body := clip.Line(eventBody(e, o), o.width())
	if e.Pasted {
		body = "[pasted] " + body
	}
	if e.Handback {
		body = "[handback] " + body
	}
	if e.Tag == transcript.TagSubagent {
		body = "via " + firstNonEmpty(e.Agent, "subagent") + ": " + body
	}
	if e.Abandoned {
		body = "[abandoned] " + body
	}
	return head + " " + body
}

// named reports whether the body of an event opens with something that says
// what the event is — a tool name, a file operation — which makes the kind
// word a repeat of the next word.
func named(e transcript.Event) bool {
	switch e.Kind {
	case transcript.KindTool, transcript.KindAgent:
		return e.Tool != "" || e.Agent != ""
	case transcript.KindEdit:
		return len(e.Changes) > 0
	}
	return false
}

func eventBody(e transcript.Event, o Options) string {
	switch e.Kind {
	case transcript.KindTool:
		s := e.Tool
		if e.Input != "" {
			s = strings.TrimSpace(s + " " + subject(e.Input, o.CWD))
		}
		return s + resultNote(e)
	case transcript.KindEdit:
		return relChanges(e.Changes, o.CWD) + resultNote(e)
	case transcript.KindAgent:
		s := e.Tool
		if e.Agent != "" {
			s = strings.TrimSpace(s + " " + e.Agent)
		}
		if e.Tag != "" {
			s += " [" + e.Tag + "]"
		}
		if e.AgentID != "" {
			s += " (" + ShortID(e.AgentID) + ")"
		}
		if text := firstNonEmpty(e.Input, e.Text); text != "" {
			s += " " + subject(text, o.CWD)
		}
		return s + resultNote(e)
	case transcript.KindPlan:
		if len(e.Plan) > 0 {
			return planLine(e.Plan)
		}
		return e.Text
	default:
		// A prompt's tag says how it reached the model — typed while it was
		// working, never delivered at all — which changes what the reply means.
		if e.Tag != "" {
			return "[" + e.Tag + "] " + e.Text
		}
		return e.Text
	}
}

func resultNote(e transcript.Event) string {
	var parts []string
	switch {
	case e.Pending:
		parts = append(parts, "NO RESULT")
	case e.ExitCode != nil:
		parts = append(parts, fmt.Sprintf("exit %d", *e.ExitCode))
	case e.Failed:
		parts = append(parts, "FAILED")
	}
	if e.Failed && e.ExitCode != nil && *e.ExitCode == 0 {
		parts = append(parts, "FAILED")
	}
	if len(e.Output) > 0 {
		parts = append(parts, clip.Size(len(e.Output)))
	}
	if len(parts) == 0 {
		return ""
	}
	return " → " + strings.Join(parts, " ")
}

func relChanges(cs []transcript.Change, cwd string) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s %s (+%d -%d)", c.Op, relTo(c.Path, cwd), c.Added, c.Removed))
	}
	return strings.Join(parts, ", ")
}

func planLine(steps []transcript.Step) string {
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		parts = append(parts, statusMark(s.Status)+" "+s.Text)
	}
	return strings.Join(parts, " | ")
}

func statusMark(s string) string {
	switch strings.ToLower(s) {
	case "completed", "done":
		return "[x]"
	case "in_progress", "in-progress", "active":
		return "[>]"
	default:
		return "[ ]"
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// quietSince is the time of a session's last event when its log was written
// well after it: reopened, or costs saved on exit, with nothing done. Zero
// when the last write is the last thing that happened. Of 297 logs on one
// machine, 247 were last written within a minute of their last event; the
// rest from 2 minutes to days after, when the session was closed or reopened.
func quietSince(s *transcript.Session) time.Time {
	if s.Live || s.LastEvent.IsZero() || s.Updated.Sub(s.LastEvent) < 5*time.Minute {
		return time.Time{}
	}
	return s.LastEvent
}

// day is a full timestamp for the preview's header; "?" when unknown rather
// than the zero time, which prints as a plausible-looking date.
func day(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Local().Format("Mon Jan 2 15:04")
}
