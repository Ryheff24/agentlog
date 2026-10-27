// Package transcript defines the provider-neutral model that every agent log
// is normalized into: a Session (cheap metadata from a header/tail scan) and a
// Transcript (the full ordered list of Events).
package transcript

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentlog/internal/spec"
)

// Provider names.
const (
	Claude  = "claude"
	Codex   = "codex"
	Copilot = "copilot"
)

// Session is what discovery learns about one agent run without parsing the
// whole log.
type Session struct {
	Provider string   `json:"provider"`
	ID       string   `json:"id"`
	Files    []string `json:"files"` // oldest first; Codex threads can span pages
	Size     int64    `json:"size_bytes"`
	CWD      string   `json:"cwd,omitempty"`
	// Dirs are directories the session worked in before CWD, oldest first:
	// a Claude session that carried on elsewhere leaves a log in each.
	Dirs    []string  `json:"dirs,omitempty"`
	Title   string    `json:"title,omitempty"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`
	// LastEvent is the newest event's time, once the log has been read. A
	// log is also written when nothing happens in it (reopened, costs
	// saved on exit), so it can be much older than Updated.
	LastEvent time.Time `json:"last_event,omitzero"`
	// Unreadable is set when a listing tried to read the log and could not.
	Unreadable bool   `json:"unreadable,omitempty"`
	Model      string `json:"model,omitempty"`
	Branch     string `json:"git_branch,omitempty"`
	Commit     string `json:"git_commit,omitempty"`
	Origin     string `json:"origin,omitempty"` // e.g. "Codex Desktop", "cli"

	FirstPrompt string `json:"first_prompt,omitempty"`
	LastPrompt  string `json:"last_prompt,omitempty"`
	LastSay     string `json:"last_message,omitempty"`
	Ending      string `json:"ending,omitempty"` // best guess from the log tail, see Ending* consts

	// Subagent linkage. ParentID is empty for top-level sessions.
	ParentID     string        `json:"parent_id,omitempty"`
	AgentName    string        `json:"agent_name,omitempty"` // nickname / teammate name
	AgentRole    string        `json:"agent_role,omitempty"` // subagent type, task path, "guardian"
	Children     []*Session    `json:"subagents,omitempty"`
	InlineAgents []InlineAgent `json:"inline_subagents,omitempty"`
	Version      string        `json:"cli_version,omitempty"` // the harness that wrote it

	// A conversation compacted into a new session is two logs, and the new
	// one opens on the compaction: its earlier turns are in the other.
	Continues     bool   `json:"continues,omitempty"`      // opens on a compaction carried over from another session
	ContinuedFrom string `json:"continued_from,omitempty"` // that session, when its log is still on disk
	ContinuedIn   string `json:"continued_in,omitempty"`   // the session this one went on in

	Live bool `json:"live,omitempty"` // log written very recently or process reports busy
	Self bool `json:"self,omitempty"` // the session running this command
}

// InlineAgent is a sourced task run in its parent's file, rather than a
// separately addressable session. Event refs open it in the stated view.
type InlineAgent struct {
	Event       int    `json:"event"`
	ParentEvent int    `json:"parent_event,omitempty"`
	Name        string `json:"name,omitempty"`
	Subagents   bool   `json:"requires_subagents,omitempty"`
}

// File returns the newest log file of the session.
func (s *Session) File() string {
	if len(s.Files) == 0 {
		return ""
	}
	return s.Files[len(s.Files)-1]
}

// Ending values describe how the log tail looks.
const (
	EndingDone        = "done"        // last turn completed normally
	EndingUsageLimit  = "usage-limit" // provider usage/rate limit hit
	EndingInterrupted = "interrupted" // user or system aborted the turn
	EndingError       = "error"       // API or runtime error ended the turn
	EndingMidTool     = "mid-tool"    // last event is a tool call with no result
	EndingWorking     = "working"     // mid-turn, no terminal marker yet
	EndingContinued   = "continued"   // went on in another session
)

// Event tags shared across providers.
const (
	TagUsageLimit   = "usage_limit"
	TagInterrupted  = "interrupted"
	TagCompaction   = "compaction"
	TagCommand      = "command"      // slash command
	TagNotification = "notification" // background task / teammate notifications
	TagRollback     = "rollback"
	TagQueued       = "queued"     // prompt typed while the model was busy
	TagUnanswered   = "unanswered" // queued prompt the session never got to
	TagSubagent     = "subagent"   // a step a subagent ran, logged inline in the parent
)

// IsUsageLimit reports whether an error text describes a usage or rate limit.
func IsUsageLimit(s string) bool {
	l := strings.ToLower(s)
	for _, k := range []string{"usage limit", "usage_limit", "session limit", "hit your limit", "out of usage", "rate limit", "rate_limit", "quota"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// Kind classifies an Event.
type Kind string

// Event kinds.
const (
	KindUser      Kind = "user"      // a human (or orchestrator) prompt
	KindAssistant Kind = "assistant" // visible assistant text
	KindReasoning Kind = "reasoning" // thinking / reasoning summary
	KindTool      Kind = "tool"      // tool call with its result
	KindEdit      Kind = "edit"      // file modification (a tool call with Changes)
	KindAgent     Kind = "agent"     // subagent spawn / message / result
	KindPlan      Kind = "plan"      // todo list, plan or goal update
	KindError     Kind = "error"     // API error, usage limit, failed turn
	KindNotice    Kind = "notice"    // compaction, abort, notifications, slash commands
	// KindRecord is a record this build has no reading for: a type that did
	// not exist when it was written, or one that has been renamed since. It
	// carries the record itself, flattened, so drift costs fidelity and never
	// costs the record.
	KindRecord Kind = "record"
)

// AllKinds lists kinds in display order.
var AllKinds = []Kind{KindUser, KindAssistant, KindReasoning, KindTool, KindEdit, KindAgent, KindPlan, KindError, KindNotice, KindRecord}

// Event is one normalized step of a transcript.
type Event struct {
	Human        bool      `json:"human,omitempty"`
	Pasted       bool      `json:"pasted,omitempty"`
	Source       string    `json:"source_file,omitempty"`
	ResultLine   int       `json:"result_line,omitempty"`
	ResultSource string    `json:"result_source_file,omitempty"`
	RepeatOf     int       `json:"repeat_of,omitempty"` // original event; copies retain their refs
	Handback     bool      `json:"handback,omitempty"`
	Index        int       `json:"i"` // 1-based, stable for append-only logs
	Time         time.Time `json:"time"`
	Kind         Kind      `json:"kind"`

	Text string `json:"text,omitempty"` // message text, notice text, error text
	Tag  string `json:"tag,omitempty"`  // sub-classification, e.g. "compaction", "usage_limit", "interrupted"

	Tool     string   `json:"tool,omitempty"`
	Input    string   `json:"input,omitempty"`  // compact, human-oriented rendering of the call
	Output   string   `json:"output,omitempty"` // full result text
	Failed   bool     `json:"failed,omitempty"`
	ExitCode *int     `json:"exit_code,omitempty"`
	Pending  bool     `json:"pending,omitempty"` // call has no recorded result
	Changes  []Change `json:"changes,omitempty"`

	// Git is what the step did to the repository, when the harness says so:
	// one command can commit and push.
	Git []GitOp `json:"git,omitempty"`

	Agent   string `json:"agent,omitempty"`    // subagent name / task path involved
	AgentID string `json:"agent_id,omitempty"` // child session id when known
	Plan    []Step `json:"plan,omitempty"`

	// Abandoned marks a step the session walked away from — a rewound, edited
	// or restarted turn. Only set when abandoned branches were asked for.
	Abandoned bool `json:"abandoned,omitempty"`

	CallID string `json:"-"` // provider call id, used while pairing results
	Line   int    `json:"line"`
}

// GitOp is a repository operation a step performed. The harness records it
// beside the tool result, which is the only place the sha a session produced
// appears at all: the command output says "1 file changed", not what it is
// called. Without it there is no way back from a session to the commit it
// landed, which is the first thing anyone asks of a session that is finished.
type GitOp struct {
	Action string `json:"action"`           // committed, pushed, merged, checkout, created
	SHA    string `json:"sha,omitempty"`    //
	Branch string `json:"branch,omitempty"` //
}

// Text renders a git operation in one phrase.
func (g GitOp) Text() string {
	parts := []string{g.Action}
	if g.SHA != "" {
		parts = append(parts, g.SHA)
	}
	if g.Branch != "" {
		parts = append(parts, "on "+g.Branch)
	}
	return strings.Join(parts, " ")
}

// Change is one file modification.
type Change struct {
	Path    string `json:"path"`
	Op      string `json:"op"` // add, update, delete, move, write
	MoveTo  string `json:"move_to,omitempty"`
	Diff    string `json:"diff,omitempty"` // unified diff body (hunks)
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Step is one item of a plan / todo list.
type Step struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// Transcript is a fully parsed session.
type Transcript struct {
	Session Session  `json:"session"`
	Events  []Event  `json:"events"`
	Copies  []Event  `json:"-"` // hidden repeated records, reachable by explicit ref
	Goal    string   `json:"goal,omitempty"`
	Plan    []Step   `json:"plan,omitempty"`   // latest plan / todo state
	Hidden  int      `json:"hidden,omitempty"` // records deliberately skipped (inherited fork history, rolled-back branches)
	Notes   []string `json:"notes,omitempty"`

	// Every log record a reader does not turn into an event is counted here, so
	// a format change shows up as a number instead of a silent gap. Ignored
	// types are known and deliberately unmodelled; Unknown ones are not known
	// at all, which is how a new harness release announces itself.
	// Records is how many lines the log held, whether or not this build could
	// read them: the denominator of how much of a session is understood.
	Records int `json:"records,omitempty"`

	Ignored  map[string]int `json:"ignored,omitempty"`
	Unknowns map[string]int `json:"unknown,omitempty"`

	// Unpaired counts results whose call this build could not find. Over a
	// window of a log it means the call is simply outside the window; over a
	// whole one it means the pairing rule has stopped working, which is the
	// quietest way a reader can start losing half of every tool call.
	Unpaired int `json:"unpaired_results,omitempty"`

	// Usage is what the session spent, when the harness wrote it down.
	Usage Usage `json:"usage,omitempty"`
}

// Abs resolves a path a harness wrote down against the directory the turn ran
// in. Harnesses are inconsistent about this — the same tool writes an absolute
// path in one log format and a relative one in another — and a path that only
// means something next to its cwd cannot be compared, searched for, or handed
// to a reader who is somewhere else.
func Abs(path, cwd string) string {
	if path == "" || cwd == "" || filepath.IsAbs(path) {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		return path
	}
	return filepath.Join(cwd, path)
}

// Localise resolves every change path in an event against a directory.
func Localise(e *Event, cwd string) {
	for i := range e.Changes {
		e.Changes[i].Path = Abs(e.Changes[i].Path, cwd)
		e.Changes[i].MoveTo = Abs(e.Changes[i].MoveTo, cwd)
	}
}

// Usage is what a session cost. Every harness records some of it and none of
// them record all of it, so a zero means "not written down", not "none".
//
// The fields are normalised across harnesses, which do not agree with each
// other: Input counts only the tokens that were not served from cache,
// CacheRead and CacheWrite the ones that were, and Reasoning is the part of
// Output the model spent thinking. Total is everything.
type Usage struct {
	Input      int64 `json:"input_tokens,omitempty"`
	CacheRead  int64 `json:"cached_input_tokens,omitempty"`
	CacheWrite int64 `json:"cache_write_tokens,omitempty"`
	Output     int64 `json:"output_tokens,omitempty"`
	Reasoning  int64 `json:"reasoning_tokens,omitempty"`
	Total      int64 `json:"total_tokens,omitempty"`

	Source        string           `json:"source,omitempty"` // which records this came from
	ContextWindow int64            `json:"context_window,omitempty"`
	CostUSD       float64          `json:"cost_usd,omitempty"`
	ByModel       map[string]int64 `json:"tokens_by_model,omitempty"`
	Requests      int              `json:"requests,omitempty"`
	Limits        []Limit          `json:"rate_limits,omitempty"`
}

// Limit is one rate-limit window as the harness last saw it. It is a reading
// taken at a moment, not a live number: Taken says when.
type Limit struct {
	Name    string        `json:"name"`
	Percent float64       `json:"used_percent"`
	Window  time.Duration `json:"window"`
	Resets  time.Time     `json:"resets_at,omitempty"`
	Taken   time.Time     `json:"as_of"`
}

// Known reports whether anything was written down.
func (u Usage) Known() bool { return u.Total > 0 || u.Input > 0 || u.Output > 0 || u.CostUSD > 0 }

// Limit records a rate-limit reading, keeping the most recent of each window.
// Readings are snapshots, so an older one says nothing about now.
func (u *Usage) Limit(l Limit) {
	for i, have := range u.Limits {
		if have.Name == l.Name {
			if !l.Taken.Before(have.Taken) {
				u.Limits[i] = l
			}
			return
		}
	}
	u.Limits = append(u.Limits, l)
}

// Model records tokens against a model name.
func (u *Usage) Model(name string, tokens int64) {
	if name == "" || tokens == 0 {
		return
	}
	if u.ByModel == nil {
		u.ByModel = map[string]int64{}
	}
	u.ByModel[name] += tokens
}

// Ignore counts one record of a type the reader knows about and does not model.
func (t *Transcript) Ignore(recordType string) {
	if t.Ignored == nil {
		t.Ignored = map[string]int{}
	}
	t.Ignored[recordType]++
}

// Unknown counts one record of a type the reader has no case for at all, and
// returns the event that stands in for it. Counting alone was the old
// behaviour and it is the one thing a reader of someone else's log cannot
// afford: a provider renames a record on a Tuesday and the tool quietly starts
// answering questions about a session it is only half reading.
func (t *Transcript) Unknown(recordType string) {
	// A type the person declared in types.json is known, if not modelled.
	if spec.UserDeclared(t.Session.Provider, recordType) {
		t.Ignore(recordType)
		return
	}
	if t.Unknowns == nil {
		t.Unknowns = map[string]int{}
	}
	t.Unknowns[recordType]++
}

// Generic builds the stand-in event for a record this build cannot read. The
// flattening is the caller's, so that this package stays free of it.
func Generic(recordType, text string, at time.Time, line int) Event {
	return Event{Kind: KindRecord, Tag: recordType, Text: text, Time: at, Line: line}
}

// Orphan is the event for a result whose call is missing. Dropping it was the
// old behaviour, and it is the wrong one twice over: the output is often the
// answer someone is looking for, and a pairing rule that has quietly stopped
// matching looks exactly like a session where nothing happened.
func (t *Transcript) Orphan(what, output string, at time.Time, line int) Event {
	t.Unpaired++
	e := Generic("result with no call in this log", strings.TrimSpace(output), at, line)
	if what != "" {
		e.Tag = what + " with no call in this log"
	}
	return e
}

// Skip is one record type that produced no event, with how often it appeared.
type Skip struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// Skips returns the unmodelled record types, most frequent first. Unknown
// types sort ahead of ignored ones at equal counts.
func (t *Transcript) Skips() []Skip {
	out := make([]Skip, 0, len(t.Unknowns)+len(t.Ignored))
	unknown := map[string]bool{}
	for k, n := range t.Unknowns {
		unknown[k] = true
		out = append(out, Skip{k, n})
	}
	for k, n := range t.Ignored {
		out = append(out, Skip{k, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if unknown[out[i].Type] != unknown[out[j].Type] {
			return unknown[out[i].Type]
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Number assigns 1-based indices in order.
func (t *Transcript) Number() {
	for i := range t.Events {
		t.Events[i].Index = i + 1
	}
}

// Ending classifies the tail of the event list.
func Ending(events []Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		switch e.Kind {
		case KindError:
			if e.Tag == TagUsageLimit {
				return EndingUsageLimit
			}
			return EndingError
		case KindNotice:
			if e.Tag == TagInterrupted {
				return EndingInterrupted
			}
			continue
		case KindReasoning, KindPlan:
			continue
		case KindTool, KindEdit, KindAgent:
			if e.Pending {
				return EndingMidTool
			}
			return EndingWorking
		case KindAssistant:
			return EndingDone
		case KindUser:
			return EndingWorking
		}
	}
	return ""
}

// Query narrows discovery. Zero value matches everything.
type Query struct {
	Since time.Time // newest file modified at or after
	ID    string    // session id prefix
	Now   time.Time // the clock the caller reads; zero means the wall clock
}

// Live reports whether a log last written at mod is being written now.
func (q Query) Live(mod time.Time) bool {
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	return now.Sub(mod) < 90*time.Second
}

// MatchID reports whether id satisfies the query's id prefix.
func (q Query) MatchID(id string) bool {
	return q.ID == "" || strings.HasPrefix(id, q.ID)
}

// Window limits how much of a log is parsed; the zero value parses it all.
// Windows are for cheap previews: event indices are not meaningful.
type Window struct {
	HeadBytes int64 // parse only the first HeadBytes of the oldest file
	TailBytes int64 // parse only the last TailBytes of the newest file
}

// Whole reports whether the window covers the entire log.
func (w Window) Whole() bool { return w.HeadBytes <= 0 && w.TailBytes <= 0 }

// Load controls how a reader turns a log into a transcript. The zero value
// reads everything and shows only what the session actually did.
type Load struct {
	Window Window

	// Branches keeps the records of turns the session walked away from — a
	// rewind, an edited prompt, a restarted turn. They are hidden by default
	// because presenting them as history shows work that was undone.
	Branches bool

	// Subagents keeps the work of subagents that a harness logs inline in the
	// parent's file rather than in one of their own. Off by default: a
	// subagent's steps are the parent's tool call from the parent's point of
	// view, and inlining them buries what the parent did.
	Subagents bool
}

// Newest returns the time of the latest timed event that was work: a
// notice (a /model, a hook, a compaction) or an unread record is not.
func Newest(evs []Event) time.Time {
	var t time.Time
	for _, e := range evs {
		if e.Kind == KindNotice || e.Kind == KindRecord {
			continue
		}
		if e.Time.After(t) {
			t = e.Time
		}
	}
	return t
}

// LastIndex is the final addressable event, including hidden copies.
func (t *Transcript) LastIndex() int {
	n := 0
	for _, events := range [][]Event{t.Events, t.Copies} {
		if len(events) > 0 {
			n = max(n, events[len(events)-1].Index)
		}
	}
	return n
}

// Range opens explicit refs, including copies hidden from ordinary views.
func (t *Transcript) Range(from, to int) []Event {
	to = max(from, to)
	var out []Event
	for _, events := range [][]Event{t.Events, t.Copies} {
		for _, e := range events {
			if e.Index >= from && e.Index <= to {
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}
