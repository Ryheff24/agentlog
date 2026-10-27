package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/clip"
	"github.com/Ryheff24/agentlog/internal/generic"
	"github.com/Ryheff24/agentlog/internal/jsonl"
	"github.com/Ryheff24/agentlog/internal/patch"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// record is one rollout line, lazily decoded.
type record struct {
	file    string
	line    int
	ts      time.Time
	ordinal int64 // -1 when absent (older CLI versions)
	typ     string
	sub     string          // payload.type
	item    string          // payload.item.type, for the typed item stream
	itemID  string          // payload.item.id
	body    json.RawMessage // payload.item, the item stream's record
	payload json.RawMessage
	raw     []byte // whole line; turn_context in old versions has no payload wrapper
}

// Load parses the pages on the thread's current branch into a transcript.
func (p *Provider) Load(s *transcript.Session, o transcript.Load) (*transcript.Transcript, error) {
	pages := threadPages(s)
	if len(pages) == 0 {
		return nil, fmt.Errorf("codex thread %s: no readable rollout files", s.ID)
	}
	chain, limits, abandoned := branchChain(pages)

	t := &transcript.Transcript{Session: *s}
	if abandoned > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%d abandoned branch page(s) skipped (edited or rolled-back turns)", abandoned))
	}
	read := jsonl.Each
	switch {
	case o.Window.TailBytes > 0:
		chain = chain[len(chain)-1:]
		read = func(path string, fn func([]byte, int) error) error { return jsonl.Tail(path, o.Window.TailBytes, fn) }
	case o.Window.HeadBytes > 0:
		chain = chain[:1]
		read = func(path string, fn func([]byte, int) error) error { return jsonl.Head(path, o.Window.HeadBytes, fn) }
	}
	var recs []record
	rolledBack := 0
	for i, pg := range chain {
		limit, hasLimit := limits[pg.pageID]
		err := read(pg.path, func(line []byte, n int) error {
			t.Records++
			r, ok := decode(line, n)
			if !ok {
				t.Unknown("undecodable-line")
				return nil
			}
			if hasLimit && r.ordinal >= 0 && r.ordinal >= limit {
				t.Hidden++
				rolledBack++
				return nil
			}
			if r.typ == "session_meta" && (i > 0 || len(recs) > 0) {
				return nil
			}
			r.file = pg.path
			recs = append(recs, r)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", pg.path, err)
		}
	}
	if rolledBack > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%d records after the point a later page took over are hidden (rolled-back turns)", rolledBack))
	}
	b := newBuilder(t, pages[0].meta)
	b.build(recs)
	t.MarkHandbacks()
	t.Number()
	t.Session.Ending = transcript.Ending(t.Events)
	return t, nil
}

// branchChain orders the pages of the newest branch oldest-first and returns,
// per base page, the ordinal at which the newer page takes over.
func branchChain(pages []*page) (chain []*page, limits map[string]int64, abandoned int) {
	byID := map[string]*page{}
	for _, pg := range pages {
		byID[pg.pageID] = pg
	}
	limits = map[string]int64{}
	head := pages[len(pages)-1]
	chain = []*page{head}
	seen := map[string]bool{head.pageID: true}
	for cur := head; cur.meta.HistoryBase != nil; {
		base := byID[cur.meta.HistoryBase.ThreadID]
		if base == nil || seen[base.pageID] {
			break
		}
		limits[base.pageID] = cur.meta.HistoryBase.EndOrdinalExclusive
		seen[base.pageID] = true
		chain = append([]*page{base}, chain...)
		cur = base
	}
	return chain, limits, len(pages) - len(chain)
}

// decode reads the shape of a record without decoding its contents. These
// records are mostly tool output and run to tens of megabytes; handing the
// whole line to encoding/json to reach four small fields scans those megabytes
// once per nesting level, so the spans are found first and the decoder is
// given only what a branch actually reads.
func decode(line []byte, n int) (record, bool) {
	// A line cut off mid-write (the process was killed, or the file is still
	// being written) is not a record of whatever type it began as. Checking
	// how it ends is enough to tell, without scanning it.
	if len(line) == 0 || line[len(line)-1] != '}' {
		return record{}, false
	}
	env := jsonl.Fields(line, "timestamp", "ordinal", "type", "payload")
	typ := text(env[2])
	if typ == "" {
		return record{}, false
	}
	r := record{line: n, ordinal: -1, typ: typ, payload: env[3], raw: line}
	if len(env[0]) > 0 {
		_ = r.ts.UnmarshalJSON(env[0])
	}
	if v, err := strconv.ParseInt(string(env[1]), 10, 64); err == nil {
		r.ordinal = v
	}
	if len(r.payload) > 0 && r.payload[0] == '{' {
		head := jsonl.Fields(r.payload, "type", "item")
		r.sub, r.body = text(head[0]), head[1]
		if len(r.body) > 0 && r.body[0] == '{' {
			it := jsonl.Fields(r.body, "type", "id")
			r.item, r.itemID = text(it[0]), text(it[1])
		}
	}
	return r, true
}

// text reads a raw JSON string value. Everything it is used on is a record
// type or an id, which never carry escapes, so the slow path is a formality.
func text(raw []byte) string {
	if len(raw) < 2 || raw[0] != '"' {
		return ""
	}
	if !bytes.ContainsRune(raw, '\\') {
		return string(raw[1 : len(raw)-1])
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// Codex logs a turn twice: the legacy records (response_item, event_msg
// user_message / exec_command_end) and the typed item stream (event_msg
// item_completed). Which item types exist depends on the CLI version, and
// neither stream is complete: code-mode exec, write_stdin and failed calls
// can be legacy-only, while items carry clean outputs and full diffs. Events
// from both are kept and paired by content, so each action appears once.
type source uint8

const (
	fromLegacy source = iota + 1
	fromItem
)

type origin struct {
	src source
	// key identifies one action: the tool it called and the arguments it was
	// called with. "" never pairs. loose identifies only the tool, and is used
	// only when exactly one candidate is waiting, because the two streams do
	// not always spell arguments the same way.
	key    string
	loose  string
	paired bool
}

// pairWindow bounds how many events back a record looks for its counterpart.
// Parallel calls complete out of order, so the pair is not always adjacent.
const pairWindow = 64

type builder struct {
	userRecords map[string]record
	current     record
	t           *transcript.Transcript
	meta        sessionMeta

	origins  []origin        // parallel to t.Events
	calls    map[string]int  // call_id -> index in t.Events
	agents   map[string]int  // child thread id -> index in t.Events
	answered map[int]bool    // event indices whose call has a result
	skipped  map[string]bool // call ids deliberately not modelled, whose
	//                               output is therefore not a lost result
	items         map[string]bool
	forkCutoff    time.Time // records at or before this were replayed from the parent
	hasUserEvts   bool      // legacy event_msg user_message present
	cwd           string    // the directory the current turn runs in
	spent         int64     // token total as of the last token_count record
	lastGoal      string
	lastReasoning string
}

func newBuilder(t *transcript.Transcript, meta sessionMeta) *builder {
	t.Session.Version = meta.CLIVersion
	return &builder{userRecords: map[string]record{}, t: t, meta: meta, calls: map[string]int{}, agents: map[string]int{},
		answered: map[int]bool{}, skipped: map[string]bool{}, items: map[string]bool{}}
}

func (b *builder) build(recs []record) {
	for _, r := range recs {
		switch {
		case r.typ == "event_msg" && r.sub == "user_message":
			b.hasUserEvts = true
		case r.typ == "event_msg" && r.sub == "item_completed":
			b.items[r.item] = true
		}
	}
	if b.meta.ForkedFromID != "" {
		// A fork replays the parent's history, stamped at creation time.
		b.forkCutoff = b.meta.Timestamp.Add(250 * time.Millisecond)
	}
	inherited := 0
	for _, r := range recs {
		b.current = r
		if !b.forkCutoff.IsZero() && !r.ts.After(b.forkCutoff) && r.typ != "session_meta" {
			if r.typ == "response_item" || r.typ == "event_msg" || r.typ == "compacted" {
				b.t.Hidden++
				inherited++
			}
			continue
		}
		switch r.typ {
		case "turn_context":
			b.turnContext(r)
		case "compacted":
			b.compacted(r)
		case "event_msg":
			b.eventMsg(r)
		case "response_item":
			b.responseItem(r)
		case "session_meta", "token_usage_record", "world_state", "inter_agent_communication", "inter_agent_communication_metadata":
			b.t.Ignore(r.typ)
		default:
			b.unknown(r.typ, r)
		}
	}
	if inherited > 0 {
		b.t.Notes = append(b.t.Notes, fmt.Sprintf("%d records replayed from the thread this was forked from are hidden", inherited))
	}
	for i := range b.t.Events {
		e := &b.t.Events[i]
		if e.CallID != "" && !b.answered[i] && e.Kind != transcript.KindPlan {
			e.Pending = true
		}
	}
}

// unknown stands a record up that this build has no reading for, flattened
// into the event: a renamed type costs its pretty rendering, not its content,
// its position or its number. Each record is dispatched once, so it gets
// one stand-in at most.
func (b *builder) unknown(recordType string, r record) {
	b.t.Unknown(recordType)
	b.add(generic.Event(recordType, r.raw, r.ts, r.line))
}

func (b *builder) add(e transcript.Event) int {
	e.Source = b.current.file
	transcript.ClassifyPrompt(&e, &b.t.Session)
	transcript.PromptHints(&e, b.current.raw)
	transcript.Localise(&e, b.cwdOr())
	b.t.Events = append(b.t.Events, e)
	b.origins = append(b.origins, origin{})
	return len(b.t.Events) - 1
}

// addPaired adds e, or merges it into the counterpart the other stream logged
// for the same action, and returns the event's index. Candidates are found by
// call id first, then by the full content key, and only then by tool name —
// and by name only when exactly one candidate is waiting. Codex runs calls in
// parallel, so matching on the name alone would merge one call's arguments
// with another call's output whenever completions interleave.
func (b *builder) addPaired(e transcript.Event, src source, key, loose string) int {
	if i, ok := b.counterpart(e.CallID, src, key, loose); ok {
		b.merge(i, e, src)
		return i
	}
	i := b.add(e)
	b.origins[i] = origin{src: src, key: key, loose: loose}
	if e.CallID != "" {
		b.calls[e.CallID] = i
	}
	return i
}

func (b *builder) counterpart(callID string, src source, key, loose string) (int, bool) {
	if callID != "" {
		if i, ok := b.calls[callID]; ok && b.origins[i].src != src && !b.origins[i].paired {
			return i, true
		}
	}
	first, found := 0, false
	for i := len(b.t.Events) - 1; i >= 0 && i >= len(b.t.Events)-pairWindow; i-- {
		o := &b.origins[i]
		if o.src == src || o.paired {
			continue
		}
		if key != "" && o.key == key {
			return i, true
		}
		if loose != "" && o.loose == loose {
			if found {
				return 0, false // ambiguous: leave both calls standing
			}
			first, found = i, true
		}
	}
	return first, found
}

func (b *builder) merge(i int, e transcript.Event, src source) {
	b.origins[i].paired = true
	b.answered[i] = true
	dst := &b.t.Events[i]
	originalSource := dst.Source
	if src == fromItem {
		preferItem(dst, e)
	} else {
		merged := e
		preferItem(&merged, *dst)
		merged.Time, merged.Line = dst.Time, dst.Line
		*dst = merged
	}
	dst.Source = originalSource
	if e.Line > 0 {
		dst.ResultLine, dst.ResultSource = e.Line, b.current.file
	}
	if dst.CallID == "" && e.CallID != "" {
		dst.CallID = e.CallID
		b.calls[e.CallID] = i
	}
}

// preferItem copies what the item knows into the legacy event: items carry
// unwrapped output, complete diffs, explicit status and the answer phase.
func preferItem(dst *transcript.Event, it transcript.Event) {
	if it.Tool != "" {
		dst.Tool = it.Tool
	}
	if it.Input != "" {
		dst.Input = it.Input
	}
	if it.Output != "" {
		dst.Output = it.Output
	}
	if it.ExitCode != nil {
		dst.ExitCode = it.ExitCode
	}
	if len(it.Changes) > 0 {
		dst.Changes = it.Changes
	}
	if it.Tag != "" {
		dst.Tag = it.Tag
	}
	dst.Failed = dst.Failed || it.Failed
}

var imageCount = regexp.MustCompile(`\s*\[\d+ image\(s\)\]`)

// textKey identifies a message; only one stream notes attached images.
func textKey(kind, text string) string {
	text = strings.Join(strings.Fields(imageCount.ReplaceAllString(text, "")), " ")
	if len(text) > 300 {
		text = text[:300]
	}
	return kind + "\x00" + text
}

func shellKey(script string) string { return "shell\x00" + strings.TrimSpace(script) }

// patchKeys identify an edit. The two streams spell the diff differently — a
// legacy envelope has no hunk headers and may use relative paths where the
// item uses absolute ones — so the precise key discriminates sibling edits to
// the same file by how many lines they changed, which both forms agree on.
func patchKeys(cs []transcript.Change) (key, loose string) {
	names := make([]string, 0, len(cs))
	added, removed := 0, 0
	for _, c := range cs {
		names = append(names, filepath.Base(c.Path))
		added, removed = added+c.Added, removed+c.Removed
	}
	sort.Strings(names)
	loose = "patch\x00" + strings.Join(names, "\x00")
	return fmt.Sprintf("%s\x00+%d-%d", loose, added, removed), loose
}

// callKeys identify a named tool call. MCP names are prefixed
// (mcp__server__tool) in legacy records but not in items, so only the tool
// name is comparable. The discriminator is the rendered input rather than the
// raw arguments, because both streams render it through the same canonical
// form even when they store it differently.
func callKeys(name, input string) (key, loose string) {
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = name[i+2:]
	}
	loose = "call\x00" + name
	if input = strings.TrimSpace(input); input == "" {
		return "", loose
	}
	if len(input) > 300 {
		input = input[:300]
	}
	return loose + "\x00" + input, loose
}

// cwdOr is the directory to resolve a relative path against: the turn's, or
// failing that the one the thread started in.
func (b *builder) cwdOr() string {
	if b.cwd != "" {
		return b.cwd
	}
	return b.t.Session.CWD
}

func (b *builder) turnContext(r record) {
	var tc struct {
		Model string `json:"model"`
		CWD   string `json:"cwd"`
	}
	src := r.payload
	if len(src) == 0 {
		src = r.raw
	}
	if json.Unmarshal(src, &tc) != nil {
		return
	}
	if tc.Model != "" {
		b.t.Session.Model = tc.Model
	}
	// A turn can move: the cwd here is the one its relative paths mean.
	if tc.CWD != "" {
		b.cwd = tc.CWD
	}
}

// tokenCount is the harness's running account of what the thread has spent,
// rewritten after every response. The totals are cumulative, so the last
// record wins over adding them up, and the per-response figure beside them is
// charged to whichever model was current — the only way to split a thread that
// switched models halfway.
func (b *builder) tokenCount(r record) {
	var tc struct {
		Info struct {
			Total  tokens `json:"total_token_usage"`
			Window int64  `json:"model_context_window"`
		} `json:"info"`
		Limits struct {
			Primary   *limit `json:"primary"`
			Secondary *limit `json:"secondary"`
		} `json:"rate_limits"`
	}
	// Reached only through event_msg, whose type is read from the payload,
	// so the payload is always there.
	if json.Unmarshal(r.payload, &tc) != nil {
		return
	}
	u := &b.t.Usage
	if n := tc.Info.Total.Total; n > 0 {
		// input_tokens counts the cached ones too, unlike the other harness.
		u.Input = tc.Info.Total.Input - tc.Info.Total.Cached
		u.CacheRead = tc.Info.Total.Cached
		u.CacheWrite = tc.Info.Total.CacheWrite
		u.Output = tc.Info.Total.Output
		u.Reasoning = tc.Info.Total.Reasoning
		u.Total, u.Source = n, "token_count records"
		// The record is written more often than the model is called, so what
		// the running total grew by — not the per-response figure beside it —
		// is what this model actually spent, and a growth is a response.
		if n > b.spent {
			u.Model(b.t.Session.Model, n-b.spent)
			u.Requests++
		}
		b.spent = n
	}
	if tc.Info.Window > 0 {
		u.ContextWindow = tc.Info.Window
	}
	for _, l := range []struct {
		name string
		l    *limit
	}{{"primary", tc.Limits.Primary}, {"secondary", tc.Limits.Secondary}} {
		if l.l == nil {
			continue
		}
		u.Limit(transcript.Limit{
			Name:    l.name,
			Percent: l.l.Percent,
			Window:  time.Duration(l.l.Minutes) * time.Minute,
			Resets:  unix(l.l.Resets),
			Taken:   r.ts,
		})
	}
}

// tokens is one token count, in the shape both the running total and the
// per-response figure use.
type tokens struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type limit struct {
	Percent float64 `json:"used_percent"`
	Minutes int64   `json:"window_minutes"`
	Resets  int64   `json:"resets_at"`
}

func unix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func (b *builder) compacted(r record) {
	var c struct {
		Message string `json:"message"`
	}
	src := r.payload
	if len(src) == 0 {
		src = r.raw
	}
	_ = json.Unmarshal(src, &c)
	text := "context compacted: earlier turns were summarized for the model (the log still holds them)"
	if strings.TrimSpace(c.Message) != "" {
		text += "\n" + c.Message
	}
	b.add(transcript.Event{Time: r.ts, Kind: transcript.KindNotice, Tag: transcript.TagCompaction, Text: text, Line: r.line})
}

func (b *builder) eventMsg(r record) {
	switch r.sub {
	case "item_completed":
		b.item(r)
	case "user_message":
		var m struct {
			Message string   `json:"message"`
			Images  []string `json:"images"`
		}
		_ = json.Unmarshal(r.payload, &m)
		text := clip.StripInjected(m.Message)
		if len(m.Images) > 0 {
			text = strings.TrimSpace(text + fmt.Sprintf(" [%d image(s)]", len(m.Images)))
		}
		if text != "" {
			i := b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindUser, Text: text, Line: r.line}, fromLegacy, textKey("user", text), "")
			if other, ok := b.userRecords[textKey("user", text)]; ok {
				b.t.Events[i].ResultSource, b.t.Events[i].ResultLine = other.file, other.line
				delete(b.userRecords, textKey("user", text))
			}
		}
	case "exec_command_end":
		var m struct {
			CallID           string   `json:"call_id"`
			Command          []string `json:"command"`
			ExitCode         *int     `json:"exit_code"`
			AggregatedOutput string   `json:"aggregated_output"`
		}
		_ = json.Unmarshal(r.payload, &m)
		if i, ok := b.calls[m.CallID]; ok {
			b.answered[i] = true
			e := &b.t.Events[i]
			e.ExitCode = m.ExitCode
			e.Failed = m.ExitCode != nil && *m.ExitCode != 0
			if e.Output == "" {
				e.Output = m.AggregatedOutput
			}
			return
		}
		// The end record is the command and its result at once.
		script := shellCommand(m.Command)
		i := b.addCall(transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: "shell", Input: script,
			Output: m.AggregatedOutput, ExitCode: m.ExitCode, Failed: m.ExitCode != nil && *m.ExitCode != 0, Line: r.line}, m.CallID, shellKey(script), "")
		b.answered[i] = true
	case "patch_apply_end":
		var m struct {
			CallID  string `json:"call_id"`
			Success *bool  `json:"success"`
			Stderr  string `json:"stderr"`
		}
		_ = json.Unmarshal(r.payload, &m)
		if i, ok := b.calls[m.CallID]; ok && m.Success != nil && !*m.Success {
			b.t.Events[i].Failed = true
			if b.t.Events[i].Output == "" {
				b.t.Events[i].Output = m.Stderr
			}
		}
	case "turn_aborted":
		var m struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(r.payload, &m)
		b.add(transcript.Event{Time: r.ts, Kind: transcript.KindNotice, Tag: transcript.TagInterrupted, Text: "turn aborted: " + m.Reason, Line: r.line})
	case "error":
		var m turnError
		_ = json.Unmarshal(r.payload, &m)
		b.add(m.event(r))
	case "task_complete":
		// A turn that ended on an API failure (usage limit, bad request)
		// records it here rather than as a separate error event.
		var m struct {
			Error *turnError `json:"error"`
		}
		_ = json.Unmarshal(r.payload, &m)
		if m.Error != nil && m.Error.Message != "" {
			b.add(m.Error.event(r))
		}
	case "thread_goal_updated":
		var m struct {
			Goal struct {
				Objective string `json:"objective"`
				Status    string `json:"status"`
			} `json:"goal"`
		}
		_ = json.Unmarshal(r.payload, &m)
		if m.Goal.Objective == "" {
			return
		}
		b.t.Goal = m.Goal.Objective
		if m.Goal.Status != "" {
			b.t.Goal += " [" + m.Goal.Status + "]"
		}
		if b.t.Goal != b.lastGoal {
			b.lastGoal = b.t.Goal
			b.add(transcript.Event{Time: r.ts, Kind: transcript.KindPlan, Tag: "goal", Text: "goal: " + b.t.Goal, Line: r.line})
		}
	case "thread_name_updated":
		var m struct {
			ThreadName string `json:"thread_name"`
		}
		_ = json.Unmarshal(r.payload, &m)
		if m.ThreadName != "" && b.t.Session.Title == "" {
			b.t.Session.Title = m.ThreadName
		}
	case "thread_rolled_back":
		var m struct {
			NumTurns int `json:"num_turns"`
		}
		_ = json.Unmarshal(r.payload, &m)
		b.add(transcript.Event{Time: r.ts, Kind: transcript.KindNotice, Tag: transcript.TagRollback, Text: fmt.Sprintf("user rolled back %d turn(s)", m.NumTurns), Line: r.line})
	case "entered_review_mode", "exited_review_mode":
		b.add(transcript.Event{Time: r.ts, Kind: transcript.KindNotice, Text: strings.ReplaceAll(r.sub, "_", " "), Line: r.line})
	case "collab_agent_spawn_end":
		var m struct {
			CallID      string  `json:"call_id"`
			NewThreadID *string `json:"new_thread_id"`
			Nickname    string  `json:"new_agent_nickname"`
			Role        string  `json:"new_agent_role"`
			Prompt      string  `json:"prompt"`
		}
		_ = json.Unmarshal(r.payload, &m)
		idx, ok := b.calls[m.CallID]
		if !ok {
			if b.items["CollabAgentToolCall"] {
				return // spawns are logged as spawn_agent calls in these versions
			}
			idx = b.addCall(transcript.Event{Time: r.ts, Kind: transcript.KindAgent, Tool: "spawn_agent", Line: r.line}, m.CallID, "", "")
		}
		b.answered[idx] = true
		e := &b.t.Events[idx]
		e.Agent = strings.TrimSpace(m.Nickname + " " + parens(m.Role))
		if m.NewThreadID != nil {
			e.AgentID = *m.NewThreadID
		}
		if e.Text == "" {
			e.Text = m.Prompt
		}
		e.Output = "spawned"
	case "token_count":
		b.tokenCount(r)
	case "task_started", "thread_settings_applied", "agent_message":
		// Turn boundaries and posture: known, not yet modelled. An
		// agent_message repeats the assistant response_item written after
		// it (guardian reviews write their verdict this way).
		b.t.Ignore("event_msg:" + r.sub)
	default:
		b.unknown("event_msg:"+r.sub, r)
	}
}

type turnError struct {
	Message   string          `json:"message"`
	ErrorInfo json.RawMessage `json:"codex_error_info"`
}

func (m turnError) event(r record) transcript.Event {
	e := transcript.Event{Time: r.ts, Kind: transcript.KindError, Text: m.Message, Line: r.line}
	if transcript.IsUsageLimit(m.Message) || transcript.IsUsageLimit(string(m.ErrorInfo)) {
		e.Tag = transcript.TagUsageLimit
	}
	return e
}

// reasoning emits only what is new: consecutive reasoning items in a turn
// repeat the summaries of earlier ones.
func (b *builder) reasoning(r record, text string) {
	text = strings.TrimSpace(text)
	delta := strings.TrimSpace(strings.TrimPrefix(text, b.lastReasoning))
	if text != "" {
		b.lastReasoning = text
	}
	if delta != "" {
		b.add(transcript.Event{Time: r.ts, Kind: transcript.KindReasoning, Text: delta, Line: r.line})
	}
}

// addCall adds a legacy tool call, pairing it with the item logged for it.
func (b *builder) addCall(e transcript.Event, callID, key, loose string) int {
	e.CallID = callID
	i := b.addPaired(e, fromLegacy, key, loose)
	if callID != "" {
		b.calls[callID] = i
	}
	return i
}

func (b *builder) responseItem(r record) {
	switch r.sub {
	case "message":
		var m struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(r.payload, &m)
		var parts []string
		for _, c := range m.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		text := strings.Join(parts, "\n")
		switch m.Role {
		case "assistant":
			if strings.TrimSpace(text) != "" {
				b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindAssistant, Text: text, Line: r.line}, fromLegacy, textKey("say", text), "")
			}
		case "user":
			if b.hasUserEvts {
				key := textKey("user", clip.StripInjected(text))
				b.userRecords[key] = r
				for i := len(b.t.Events) - 1; i >= 0 && i >= len(b.t.Events)-pairWindow; i-- {
					e := &b.t.Events[i]
					if e.Kind == transcript.KindUser && textKey("user", e.Text) == key {
						e.ResultSource, e.ResultLine = r.file, r.line
						delete(b.userRecords, key)
						break
					}
				}
				return
			}
			if text = clip.StripInjected(text); text != "" {
				b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindUser, Text: text, Line: r.line}, fromLegacy, textKey("user", text), "")
			}
		}
	case "reasoning":
		if b.items["Reasoning"] {
			return // items repeat the summaries cumulatively; mixing the two duplicates them
		}
		var m struct {
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		}
		_ = json.Unmarshal(r.payload, &m)
		var parts []string
		for _, s := range m.Summary {
			if s.Text != "" {
				parts = append(parts, s.Text)
			}
		}
		b.reasoning(r, strings.Join(parts, "\n"))
	case "function_call":
		var m struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Arguments string `json:"arguments"`
			CallID    string `json:"call_id"`
		}
		_ = json.Unmarshal(r.payload, &m)
		e := b.functionCall(r, m.Name, m.Arguments)
		key, loose := legacyKeys(m.Name, e)
		b.addCall(e, m.CallID, key, loose)
	case "function_call_output", "custom_tool_call_output":
		var m struct {
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		_ = json.Unmarshal(r.payload, &m)
		if b.skipped[m.CallID] {
			b.t.Ignore("response_item:" + r.sub + ":logged as items")
			return
		}
		i, ok := b.calls[m.CallID]
		if !ok {
			// Codex writes the call and its output as two records, and the two
			// streams do not always agree about ids. An output whose call this
			// build cannot find is shown on its own rather than dropped.
			orphan := b.t.Orphan(r.sub, "", r.ts, r.line)
			attachOutput(&orphan, m.Output)
			orphan.Text, orphan.Output = strings.TrimSpace(orphan.Output), ""
			b.add(orphan)
			return
		}
		b.answered[i] = true
		if e := &b.t.Events[i]; e.Output == "" || e.Kind == transcript.KindAgent {
			e.ResultLine, e.ResultSource = r.line, r.file
			attachOutput(e, m.Output) // a paired item's output is already unwrapped
		}
	case "custom_tool_call":
		var m struct {
			Name   string `json:"name"`
			Input  string `json:"input"`
			CallID string `json:"call_id"`
		}
		_ = json.Unmarshal(r.payload, &m)
		switch m.Name {
		case "apply_patch":
			e := transcript.Event{Time: r.ts, Kind: transcript.KindEdit, Tool: m.Name, Changes: patch.ParseEnvelope(m.Input), Line: r.line}
			e.Input = patch.Summary(e.Changes)
			key, loose := patchKeys(e.Changes)
			b.addCall(e, m.CallID, key, loose)
		case "exec":
			b.codeModeCall(r, m.Input, m.CallID)
		default:
			key, loose := callKeys(m.Name, m.Input)
			b.addCall(transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: m.Name, Input: m.Input, Line: r.line}, m.CallID, key, loose)
		}
	case "web_search_call":
		var m struct {
			Action struct {
				Type    string   `json:"type"`
				Query   string   `json:"query"`
				Queries []string `json:"queries"`
				URL     string   `json:"url"`
			} `json:"action"`
		}
		_ = json.Unmarshal(r.payload, &m)
		in := firstNonEmpty(m.Action.Query, strings.Join(m.Action.Queries, " | "), m.Action.URL)
		b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: "web_search", Input: in, Line: r.line}, fromLegacy, "web\x00"+in, "")
	case "agent_message":
		b.interAgent(r)
	case "tool_search_call", "tool_search_output", "compaction":
		// Dynamic tool discovery and the encrypted compaction item.
		b.t.Ignore("response_item:" + r.sub)
	default:
		b.unknown("response_item:"+r.sub, r)
	}
}

var shellTools = map[string]bool{"exec_command": true, "shell_command": true, "shell": true, "container.exec": true, "local_shell": true}

// legacyKeys pair a legacy function call with the item logged for it.
func legacyKeys(name string, e transcript.Event) (key, loose string) {
	switch {
	case e.Tool == "shell":
		return shellKey(e.Input), ""
	case e.Kind == transcript.KindEdit:
		return patchKeys(e.Changes)
	case name == "view_image":
		return "image\x00" + e.Input, ""
	case e.Kind == transcript.KindAgent || e.Kind == transcript.KindPlan:
		return "", "" // no item carries these
	}
	return callKeys(name, e.Input)
}

var (
	codeToolRe = regexp.MustCompile(`\btools\.([A-Za-z0-9_]+)\(`)
	codeCmdRe  = regexp.MustCompile(`\bcmd\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// codeModeCall maps a code-mode exec script, which calls tools from
// JavaScript. A script making one call is shown as that call. Versions that
// log each inner call as an item make multi-call scripts redundant.
func (b *builder) codeModeCall(r record, code, callID string) {
	calls := codeToolRe.FindAllStringSubmatch(code, -1)
	e := transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: "exec", Input: code, Line: r.line}
	switch {
	case len(calls) == 1 && shellTools[calls[0][1]]:
		if m := codeCmdRe.FindStringSubmatch(code); m != nil {
			if cmd, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
				e.Tool, e.Input = "shell", cmd
				b.addCall(e, callID, shellKey(cmd), "")
				return
			}
		}
		b.addCall(e, callID, "", "")
	case len(calls) == 1:
		e.Tool = strings.ReplaceAll(strings.TrimPrefix(calls[0][1], "mcp__"), "__", ".")
		key, loose := callKeys(calls[0][1], e.Input)
		b.addCall(e, callID, key, loose)
	case len(calls) > 1 && b.itemsCover(calls):
		// The script called several tools and the item stream logged each of
		// them separately: showing the script as well would say everything
		// twice. Its combined output is skipped for the same reason.
		b.t.Ignore("response_item:custom_tool_call:logged as items")
		b.skipped[callID] = true
		return
	default:
		b.addCall(e, callID, "", "")
	}
}

func (b *builder) itemsCover(calls [][]string) bool {
	for _, c := range calls {
		name := c[1]
		switch {
		case shellTools[name] && b.items["CommandExecution"]:
		case name == "apply_patch" && b.items["FileChange"]:
		case strings.HasPrefix(name, "mcp__") && b.items["McpToolCall"]:
		default:
			return false
		}
	}
	return true
}

var (
	// The header in front of a command's output spells the exit code two
	// ways: unified exec ("Process exited with code N") and shell_command
	// and apply_patch in 0.61 and 0.14x ("Exit code: N").
	exitCodeRe = regexp.MustCompile(`(?m)^(?:Process exited with code|Exit code:) (-?\d+)`)
	// A code-mode cell's output opens with how the script ended.
	cellHeaderRe = regexp.MustCompile(`^Script (completed|failed|running[^\n]*)\nWall time [^\n]*\nOutput:\n\n?`)
)

func (b *builder) functionCall(r record, name, args string) transcript.Event {
	e := transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: name, Line: r.line}
	var a map[string]json.RawMessage
	_ = json.Unmarshal([]byte(args), &a)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(a[k], &s)
		return s
	}
	switch name {
	case "exec_command", "shell_command", "shell", "container.exec", "local_shell":
		e.Tool = "shell"
		if s := firstNonEmpty(str("cmd"), str("command")); s != "" {
			e.Input = s
		} else {
			var argv []string
			_ = json.Unmarshal(a["command"], &argv)
			e.Input = shellCommand(argv)
		}
	case "apply_patch":
		e.Kind = transcript.KindEdit
		e.Changes = patch.ParseEnvelope(firstNonEmpty(str("input"), str("patch"), args))
		e.Input = patch.Summary(e.Changes)
	case "update_plan":
		var p struct {
			Explanation string `json:"explanation"`
			Plan        []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		_ = json.Unmarshal([]byte(args), &p)
		e.Kind = transcript.KindPlan
		for _, s := range p.Plan {
			e.Plan = append(e.Plan, transcript.Step{Text: s.Step, Status: s.Status})
		}
		e.Text = p.Explanation
		b.t.Plan = e.Plan
	case "spawn_agent":
		e.Kind = transcript.KindAgent
		e.Agent = firstNonEmpty(str("task_name"), str("agent_type"))
		e.Text = encryptedOr(firstNonEmpty(str("message"), str("prompt"), itemsText(a["items"])))
		e.Input = clip.JSON([]byte(args), 80)
	case "send_message", "followup_task", "send_input":
		e.Kind = transcript.KindAgent
		e.Agent = firstNonEmpty(str("target"), str("task_name"), str("id"))
		e.Text = encryptedOr(firstNonEmpty(str("message"), str("prompt"), str("input"), itemsText(a["items"])))
	case "interrupt_agent", "close_agent", "resume_agent":
		e.Kind = transcript.KindAgent
		e.Agent = firstNonEmpty(str("target"), str("id"))
	case "wait_agent", "wait", "list_agents":
		e.Input = clip.JSON([]byte(args), 120)
	case "view_image":
		e.Input = str("path")
	default:
		if strings.HasPrefix(name, "mcp__") {
			e.Tool = strings.ReplaceAll(strings.TrimPrefix(name, "mcp__"), "__", ".")
		}
		e.Input = clip.JSON([]byte(args), 400)
	}
	return e
}

// itemsText reads a prompt sent as content items (text beside pasted
// images), which some versions send to a subagent instead of a message.
func itemsText(raw json.RawMessage) string {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, it := range items {
		switch {
		case it.Text != "":
			parts = append(parts, it.Text)
		case strings.Contains(it.Type, "image"):
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

func encryptedOr(s string) string {
	if clip.IsEncrypted(s) {
		return "[encrypted]"
	}
	return s
}

func attachOutput(e *transcript.Event, raw json.RawMessage) {
	out := outputText(raw)
	if m := cellHeaderRe.FindStringSubmatch(out); m != nil {
		out = out[len(m[0]):]
		if m[1] == "failed" {
			e.Failed = true
		}
	}
	// apply_patch / shell wrappers: {"output":"…","metadata":{"exit_code":N}}
	var wrapped struct {
		Output   *string `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") && json.Unmarshal([]byte(out), &wrapped) == nil && wrapped.Output != nil {
		out = *wrapped.Output
		if wrapped.Metadata.ExitCode != nil {
			e.ExitCode = wrapped.Metadata.ExitCode
		}
	}
	if m := exitCodeRe.FindStringSubmatch(out); m != nil && e.ExitCode == nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			e.ExitCode = &n
		}
		if i := strings.Index(out, "\nOutput:\n"); i >= 0 {
			out = out[i+len("\nOutput:\n"):]
		}
	}
	if e.ExitCode != nil && *e.ExitCode != 0 {
		e.Failed = true
	}
	if e.Kind == transcript.KindAgent {
		var spawn struct {
			AgentID  string `json:"agent_id"`
			Nickname string `json:"nickname"`
			TaskName string `json:"task_name"`
		}
		if json.Unmarshal([]byte(out), &spawn) == nil {
			if spawn.AgentID != "" {
				e.AgentID = spawn.AgentID
			}
			if spawn.TaskName != "" {
				e.Agent = spawn.TaskName
			}
			if spawn.Nickname != "" {
				e.Agent = strings.TrimSpace(spawn.Nickname + " " + e.Agent)
			}
		}
	}
	e.Output = out
}

// outputText flattens a function output that is either a string or a list
// of content items.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	}
	if json.Unmarshal(raw, &items) == nil {
		var parts []string
		for _, it := range items {
			switch {
			case it.Text != "":
				parts = append(parts, it.Text)
			case strings.Contains(it.Type, "image"):
				parts = append(parts, "[image]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func (b *builder) interAgent(r record) {
	var m struct {
		Author    string `json:"author"`
		Recipient string `json:"recipient"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(r.payload, &m)
	var parts []string
	msgType := ""
	encrypted := false
	for _, c := range m.Content {
		switch {
		case c.Type == "encrypted_content":
			encrypted = true
		case c.Text != "":
			text := c.Text
			if strings.HasPrefix(text, "Message Type: ") {
				msgType, _, _ = strings.Cut(strings.TrimPrefix(text, "Message Type: "), "\n")
				if _, after, ok := strings.Cut(text, "Payload:\n"); ok {
					text = after
				} else {
					text = ""
				}
			}
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	text := strings.Join(parts, "\n")
	if encrypted && text == "" {
		text = "[encrypted]"
	}
	b.add(transcript.Event{Time: r.ts, Kind: transcript.KindAgent, Tag: strings.ToLower(msgType),
		Agent: m.Author + " → " + m.Recipient, Text: text, Line: r.line})
}

// subAgent records one edge between this thread and a subagent. It is the only
// place the child's thread id is written down: a spawn_agent call says what was
// asked for, not which thread answered it, and most sessions that run subagents
// have no spawn_agent call at all. The thread id is what re-opens the child.
func (b *builder) subAgent(r record, raw json.RawMessage) {
	var it struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Thread string `json:"agent_thread_id"`
		Path   string `json:"agent_path"`
	}
	_ = json.Unmarshal(raw, &it)
	switch it.Kind {
	case "started":
		if i, ok := b.calls[it.ID]; ok && b.t.Events[i].Kind == transcript.KindAgent {
			e := &b.t.Events[i]
			e.AgentID, e.Agent = it.Thread, firstNonEmpty(e.Agent, it.Path)
			b.agents[it.Thread] = i
			return
		}
		b.agents[it.Thread] = b.add(transcript.Event{Time: r.ts, Kind: transcript.KindAgent, Tool: "subagent",
			Tag: it.Kind, Agent: it.Path, AgentID: it.Thread, Line: r.line})
	case "completed", "interrupted":
		i, ok := b.agents[it.Thread]
		if !ok {
			i = b.add(transcript.Event{Time: r.ts, Kind: transcript.KindAgent, Tool: "subagent",
				Agent: it.Path, AgentID: it.Thread, Line: r.line})
		}
		e := &b.t.Events[i]
		e.Tag, e.Failed = it.Kind, it.Kind == "interrupted"
		b.answered[i] = true
	default:
		// "interacted": the messages themselves are logged as agent_message
		// records, which carry the author, the recipient and the text.
		b.t.Ignore("item:SubAgentActivity:" + it.Kind)
	}
}

// item maps the typed item stream (event_msg item_completed). The item's type
// and id were lifted when the record was decoded; only the body is read here.
func (b *builder) item(r record) {
	env := struct{ Item json.RawMessage }{r.body}
	head := struct{ Type, ID string }{r.item, r.itemID}
	// Tool-call items carry the call id of the legacy record for the same
	// action; message and reasoning items carry an item-N id instead.
	callID := ""
	if strings.HasPrefix(head.ID, "call_") {
		callID = head.ID
	}

	switch head.Type {
	case "UserMessage":
		var it struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(env.Item, &it)
		var parts []string
		images := 0
		for _, c := range it.Content {
			switch {
			case c.Text != "":
				parts = append(parts, c.Text)
			case strings.Contains(strings.ToLower(c.Type), "image"):
				images++
			}
		}
		text := clip.StripInjected(strings.Join(parts, "\n"))
		if images > 0 {
			text = strings.TrimSpace(text + fmt.Sprintf(" [%d image(s)]", images))
		}
		if text != "" {
			b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindUser, Text: text, Line: r.line}, fromItem, textKey("user", text), "")
		}
	case "AgentMessage":
		var it struct {
			Phase   string `json:"phase"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(env.Item, &it)
		var parts []string
		for _, c := range it.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		if text := strings.Join(parts, "\n"); strings.TrimSpace(text) != "" {
			tag := ""
			if it.Phase == "final_answer" {
				tag = "final"
			}
			b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindAssistant, Tag: tag, Text: text, Line: r.line}, fromItem, textKey("say", text), "")
		}
	case "Reasoning":
		var it struct {
			SummaryText []string `json:"summary_text"`
			RawContent  []string `json:"raw_content"`
		}
		_ = json.Unmarshal(env.Item, &it)
		parts := it.SummaryText
		if len(parts) == 0 {
			parts = it.RawContent
		}
		b.reasoning(r, strings.Join(parts, "\n"))
	case "CommandExecution":
		var it struct {
			Command          []string `json:"command"`
			CWD              string   `json:"cwd"`
			ExitCode         *int     `json:"exit_code"`
			Status           string   `json:"status"`
			AggregatedOutput string   `json:"aggregated_output"`
			Stdout           string   `json:"stdout"`
			Stderr           string   `json:"stderr"`
		}
		_ = json.Unmarshal(env.Item, &it)
		script := shellCommand(it.Command)
		e := transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: "shell", Input: script,
			Output: firstNonEmpty(it.AggregatedOutput, strings.TrimSpace(it.Stdout+"\n"+it.Stderr)), ExitCode: it.ExitCode, Line: r.line}
		e.Failed = (it.ExitCode != nil && *it.ExitCode != 0) || (it.Status != "" && it.Status != "completed")
		if it.Status != "" && it.Status != "completed" && it.ExitCode == nil {
			e.Output = strings.TrimSpace(e.Output + "\n[" + it.Status + "]")
		}
		e.CallID = callID
		b.addPaired(e, fromItem, shellKey(script), "")
	case "FileChange":
		var it struct {
			Status  string `json:"status"`
			Stderr  string `json:"stderr"`
			Stdout  string `json:"stdout"`
			Changes map[string]struct {
				Type        string `json:"type"`
				UnifiedDiff string `json:"unified_diff"`
				Content     string `json:"content"` // add / delete carry the whole file
				MovePath    string `json:"move_path"`
			} `json:"changes"`
		}
		_ = json.Unmarshal(env.Item, &it)
		e := transcript.Event{Time: r.ts, Kind: transcript.KindEdit, Tool: "apply_patch", Line: r.line,
			Failed: it.Status != "" && it.Status != "completed", Output: strings.TrimSpace(it.Stdout + "\n" + it.Stderr)}
		paths := make([]string, 0, len(it.Changes))
		for path := range it.Changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			c := it.Changes[path]
			var ch transcript.Change
			switch {
			case c.UnifiedDiff == "" && c.Type == "delete":
				ch = patch.Replace(path, c.Content, "")
			case c.UnifiedDiff == "" && c.Content != "":
				ch = patch.Write(path, c.Content)
			default:
				ch = transcript.Change{Path: path, Diff: c.UnifiedDiff}
				ch.Added, ch.Removed = patch.Count(c.UnifiedDiff)
			}
			ch.Op, ch.MoveTo = c.Type, c.MovePath
			e.Changes = append(e.Changes, ch)
		}
		e.Input = patch.Summary(e.Changes)
		e.CallID = callID
		key, loose := patchKeys(e.Changes)
		b.addPaired(e, fromItem, key, loose)
	case "McpToolCall":
		var it struct {
			Server    string          `json:"server"`
			Tool      string          `json:"tool"`
			Status    string          `json:"status"`
			Arguments json.RawMessage `json:"arguments"`
			Result    json.RawMessage `json:"result"`
			Error     json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(env.Item, &it)
		e := transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: it.Server + "." + it.Tool, Line: r.line,
			Input: clip.JSON(it.Arguments, 400), Output: mcpResultText(it.Result)}
		if len(it.Error) > 0 && string(it.Error) != "null" {
			e.Failed = true
			e.Output = strings.TrimSpace(e.Output + "\n" + string(it.Error))
		}
		if it.Status == "failed" {
			e.Failed = true
		}
		e.CallID = callID
		key, loose := callKeys(it.Tool, e.Input)
		b.addPaired(e, fromItem, key, loose)
	case "DynamicToolCall":
		var it struct {
			Tool         string          `json:"tool"`
			Arguments    json.RawMessage `json:"arguments"`
			Status       string          `json:"status"`
			ContentItems []struct {
				Text string `json:"text"`
			} `json:"content_items"`
		}
		_ = json.Unmarshal(env.Item, &it)
		var out []string
		for _, c := range it.ContentItems {
			if c.Text != "" {
				out = append(out, c.Text)
			}
		}
		e := transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: it.Tool, Input: clip.JSON(it.Arguments, 400),
			Output: strings.Join(out, "\n"), Failed: it.Status == "failed", Line: r.line, CallID: callID}
		key, loose := callKeys(it.Tool, e.Input)
		b.addPaired(e, fromItem, key, loose)
	case "WebSearch":
		var it struct {
			Query  string `json:"query"`
			Action struct {
				Query   string   `json:"query"`
				Queries []string `json:"queries"`
				URL     string   `json:"url"`
			} `json:"action"`
		}
		_ = json.Unmarshal(env.Item, &it)
		in := firstNonEmpty(it.Action.Query, strings.Join(it.Action.Queries, " | "), it.Action.URL, it.Query)
		b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: "web_search", Input: in, Line: r.line}, fromItem, "web\x00"+in, "")
	case "Extension":
		var it struct {
			Kind   string `json:"kind"`
			Query  string `json:"query"`
			Action struct {
				Queries []string `json:"queries"`
				URL     string   `json:"url"`
			} `json:"action"`
			Results []struct {
				Title string `json:"title"`
				URL   string `json:"url"`
			} `json:"results"`
		}
		_ = json.Unmarshal(env.Item, &it)
		var lines []string
		for _, res := range it.Results {
			lines = append(lines, res.Title+" "+res.URL)
		}
		b.add(transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: firstNonEmpty(it.Kind, "extension"), Line: r.line,
			Input: firstNonEmpty(strings.Join(it.Action.Queries, " | "), it.Query, it.Action.URL), Output: strings.Join(lines, "\n")})
	case "ImageView":
		var it struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(env.Item, &it)
		path := strings.TrimPrefix(it.Path, "file://")
		b.addPaired(transcript.Event{Time: r.ts, Kind: transcript.KindTool, Tool: "view_image", Input: path, Line: r.line}, fromItem, "image\x00"+path, "")
	case "SubAgentActivity":
		b.subAgent(r, env.Item)
	case "ContextCompaction", "CollabAgentToolCall", "EnteredReviewMode", "ExitedReviewMode":
		// Covered by compacted records, agent function calls and review-mode events.
		b.t.Ignore("item:" + head.Type)
	case "Plan":
		var it struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(env.Item, &it)
		b.add(transcript.Event{Time: r.ts, Kind: transcript.KindPlan, Text: it.Text, Line: r.line})
	default:
		// An item type this build has never seen: read it by shape rather
		// than dropping to a blob of JSON, and count it so --doctor says so.
		b.t.Unknown("item:" + head.Type)
		b.add(generic.Event("item:"+head.Type, r.raw, r.ts, r.line))
	}
}

func mcpResultText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &res) == nil && len(res.Content) > 0 {
		var parts []string
		for _, c := range res.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			} else {
				parts = append(parts, "["+c.Type+"]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return clip.JSON(raw, 2000)
}

// shellCommand renders ["/bin/zsh","-lc","script"] as "script".
func shellCommand(argv []string) string {
	if n := len(argv); n >= 3 && (argv[n-2] == "-lc" || argv[n-2] == "-c") {
		return argv[n-1]
	}
	return strings.Join(argv, " ")
}

func parens(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}
