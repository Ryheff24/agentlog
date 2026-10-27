package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"agentlog/internal/clip"
	"agentlog/internal/generic"
	"agentlog/internal/jsonl"
	"agentlog/internal/patch"
	"agentlog/internal/transcript"
)

type entry struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	CWD               string          `json:"cwd"`
	Version           string          `json:"version"`
	Timestamp         time.Time       `json:"timestamp"`
	IsSidechain       bool            `json:"isSidechain"`
	IsMeta            bool            `json:"isMeta"`
	IsCompactSummary  bool            `json:"isCompactSummary"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	Error             string          `json:"error"`
	Content           json.RawMessage `json:"content"` // system entries
	ToolUseResult     json.RawMessage `json:"toolUseResult"`
	AITitle           string          `json:"aiTitle"`
	CustomTitle       string          `json:"customTitle"`
	Operation         string          `json:"operation"` // queue-operation
	UUID              string          `json:"uuid"`
	ParentUUID        string          `json:"parentUuid"`
	LogicalParentUUID string          `json:"logicalParentUuid"` // bridges a compaction
	ContinuedIn       string          `json:"continuedInSessionId"`
	RelocatedCWD      string          `json:"relocatedCwd"` // relocated
	AgentSetting      string          `json:"agentSetting"` // agent-setting
	WorktreeSession   *struct {
		Path   string `json:"worktreePath"`
		Branch string `json:"worktreeBranch"`
	} `json:"worktreeSession"` // worktree-state; null on leaving
	Message struct {
		ID          string          `json:"id"`
		Model       string          `json:"model"`
		Content     json.RawMessage `json:"content"`
		Usage       json.RawMessage `json:"usage"`
		StopReason  string          `json:"stop_reason"`
		StopDetails struct {
			Type        string `json:"type"`
			Category    string `json:"category"`
			Explanation string `json:"explanation"`
		} `json:"stop_details"`
	} `json:"message"`
	// cost-state: the harness's own accounting for the whole thread.
	TotalCostUSD float64               `json:"totalCostUSD"`
	ModelUsage   map[string]modelUsage `json:"modelUsage"`
	Attachment   struct {
		Type string `json:"type"`
		// A queued prompt is a plain string in most versions and a content
		// block list (text plus pasted images) in others.
		Prompt json.RawMessage `json:"prompt"`
	} `json:"attachment"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// Load parses a session or subagent transcript.
func (p *Provider) Load(s *transcript.Session, o transcript.Load) (*transcript.Transcript, error) {
	t := &transcript.Transcript{Session: *s}
	b := &builder{t: t, calls: map[string]int{}, answered: map[int]bool{}, parent: map[string]string{},
		counted: map[string]bool{}, subagent: s.ParentID != ""}
	if s.ParentID == "" {
		b.children = map[string]string{}
		for _, f := range s.Files {
			maps.Copy(b.children, childIndex(filepath.Join(filepath.Dir(f), s.ID, "subagents")))
		}
	}
	read := jsonl.Each
	files := s.Files
	switch {
	case o.Window.TailBytes > 0:
		read = func(path string, fn func([]byte, int) error) error { return jsonl.Tail(path, o.Window.TailBytes, fn) }
		files = files[len(files)-1:]
		// A tail window that starts after the first byte opens partway
		// through the conversation, whatever its first record looks like.
		if st, err := os.Stat(s.File()); len(s.Files) > 1 || err == nil && st.Size() > o.Window.TailBytes {
			b.midway = true
		}
	case o.Window.HeadBytes > 0:
		read = func(path string, fn func([]byte, int) error) error { return jsonl.Head(path, o.Window.HeadBytes, fn) }
		files = files[:min(1, len(files))]
	}
	// A session with logs in two folders may have been copied rather than
	// moved, so a later file can repeat what an earlier one holds; a record
	// is read once across files, preserving the refs of the legacy merge.
	// Within a file, copies keep their event slots but are hidden after numbering.
	prior, seen := map[string]bool{}, map[string]bool{}
	first := map[string]recordSource{}
	repeats := map[recordSource]recordSource{}
	remember := func(uuid string, at time.Time, src recordSource) {
		if uuid == "" {
			return
		}
		key := uuid + "\x00" + at.Format(time.RFC3339Nano)
		if original, ok := first[key]; ok {
			repeats[src] = original
		} else {
			first[key] = src
		}
	}
	for _, file := range files {
		b.file = file
		maps.Copy(prior, seen)
		err := read(file, func(line []byte, n int) error {
			b.raw = line
			var e entry
			if json.Unmarshal(line, &e) != nil {
				t.Records++
				b.unknown("undecodable-record", time.Time{}, n)
				// A record the reader cannot decode is still a link in the message
				// DAG; dropping it would orphan every record before it.
				var link struct {
					UUID              string    `json:"uuid"`
					ParentUUID        string    `json:"parentUuid"`
					LogicalParentUUID string    `json:"logicalParentUuid"`
					Timestamp         time.Time `json:"timestamp"`
				}
				if json.Unmarshal(line, &link) == nil {
					remember(link.UUID, link.Timestamp, recordSource{file, n})
					b.link(link.UUID, link.ParentUUID, link.LogicalParentUUID)
				}
				return nil
			}
			if len(files) > 1 {
				key := e.UUID
				if key == "" {
					key = string(line)
				}
				if prior[key] {
					return nil
				}
				seen[key] = true
			}
			remember(e.UUID, e.Timestamp, recordSource{file, n})
			t.Records++
			b.entry(e, n)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
	}
	for i := range t.Events {
		if t.Events[i].CallID != "" && !b.answered[i] {
			t.Events[i].Pending = true
		}
	}
	if b.sidechain > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%d inlined subagent records hidden (an older log format)", b.sidechain))
	}
	b.resolveQueue()
	b.spent()
	// Only a whole-file read sees enough of the DAG to tell an abandoned
	// branch from a parent that simply lies outside the window.
	if o.Window.Whole() {
		b.selectBranch(o.Branches)
	}
	t.MarkHandbacks()
	t.Number()
	hideRepeats(t, repeats)
	t.Session.Ending = transcript.Ending(t.Events)
	if t.Session.ContinuedIn != "" {
		// However its last turn looks, the conversation went on elsewhere.
		t.Session.Ending = transcript.EndingContinued
	}
	// Reading the logs beside this one is for a whole session, not for
	// the head and tail windows a listing reads.
	if t.Session.Continues && o.Window.Whole() {
		t.Session.ContinuedFrom = predecessor(s, b.carried)
	}
	return t, nil
}

// predecessor finds the session a continuation came from among the logs
// beside it: the one that says it continued in this session, or, for logs
// written before Claude Code said so, the one holding the record the
// opening compaction names as its logical parent. Only logs still written
// to after this session began can be it.
func predecessor(s *transcript.Session, carried string) string {
	dir := filepath.Dir(s.File())
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	pointer := regexp.MustCompile(`"continuedInSessionId"\s*:\s*"` + regexp.QuoteMeta(s.ID) + `"`)
	var record *regexp.Regexp
	if carried != "" {
		record = regexp.MustCompile(`"uuid"\s*:\s*"` + regexp.QuoteMeta(carried) + `"`)
	}
	for _, de := range entries {
		id, ok := strings.CutSuffix(de.Name(), ".jsonl")
		if !ok || de.IsDir() || id == s.ID {
			continue
		}
		if info, err := de.Info(); err != nil || (!s.Started.IsZero() && info.ModTime().Before(s.Started.Add(-time.Hour))) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}
		if pointer.Match(data) || (record != nil && record.Match(data)) {
			return id
		}
	}
	return ""
}

type builder struct {
	t        *transcript.Transcript
	calls    map[string]int
	answered map[int]bool
	subagent bool
	children map[string]string // subagent name or description -> agent id

	// The message DAG. Claude Code appends rather than rewrites: a rewind, an
	// edited prompt or a restarted turn leaves the abandoned records in place
	// and starts a new branch from their shared parent.
	parent map[string]string // uuid -> parent uuid (logical parent across a compaction)
	head   string            // uuid of the newest record, the tip of the live branch
	source []string          // record uuid per event, parallel to t.Events
	cur    string            // uuid of the record being decoded

	file      string // the log file being read
	raw       []byte // the record being decoded, for the stand-in event
	generic   int    // record (by count) already given a stand-in, so one record gives one
	cwd       string // the directory the current record ran in
	sidechain int    // inlined subagent records skipped
	talked    bool   // a user or assistant record has been read
	midway    bool   // the read starts partway through the log (a tail window)
	carried   string // logical parent of a compaction this log opens on

	// Prompts typed while the model was busy. Each enqueue adds an event and a
	// pending entry; a delivery removes the entry, and the event with it.
	queued      []pending
	placeholder map[int]bool // event indices an enqueue record created

	// The two accounts of what the thread spent, settled by spent().
	counted map[string]bool // message ids whose usage has been counted
	tokens  transcript.Usage
	cost    transcript.Usage
}

// pending is one prompt sitting in the queue, with the event standing in for it.
type pending struct {
	index int
	text  string
}

func (b *builder) add(e transcript.Event) int {
	e.Source = b.file
	transcript.ClassifyPrompt(&e, &b.t.Session)
	transcript.PromptHints(&e, b.raw)
	transcript.Localise(&e, b.cwdOr())
	b.t.Events = append(b.t.Events, e)
	b.source = append(b.source, b.cur)
	return len(b.t.Events) - 1
}

// cwdOr is the directory to resolve a relative path against: the one the
// record named, or failing that the one the session started in.
func (b *builder) cwdOr() string {
	if b.cwd != "" {
		return b.cwd
	}
	return b.t.Session.CWD
}

// selectBranch drops every event whose record is not on the chain walked back
// from the newest record. With keepAll the events stay and are marked instead.
func (b *builder) selectBranch(keepAll bool) {
	if b.head == "" {
		return
	}
	live := map[string]bool{}
	for u := b.head; u != "" && !live[u]; u = b.parent[u] {
		live[u] = true
	}
	// A branch root is an off-chain record whose own parent is on the chain
	// (or is gone), which makes it the point the session walked away from.
	branches := 0
	for u, p := range b.parent {
		if !live[u] && (p == "" || live[p]) {
			branches++
		}
	}
	dropped := 0
	kept := b.t.Events[:0]
	for i, e := range b.t.Events {
		if u := b.source[i]; u != "" && !live[u] {
			dropped++
			if !keepAll {
				continue
			}
			e.Abandoned = true
		}
		kept = append(kept, e)
	}
	if dropped == 0 {
		return
	}
	b.t.Events = kept
	if keepAll {
		b.t.Notes = append(b.t.Notes, fmt.Sprintf("%d of these events are on %s the session abandoned (marked)",
			dropped, plural(branches, "branch", "branches")))
		return
	}
	b.t.Hidden += dropped
	b.t.Notes = append(b.t.Notes, fmt.Sprintf("%d events on %s hidden: rewound, edited or restarted turns (--branches to include them); %d rewound turns not counted (--branches)",
		dropped, plural(branches, "branch", "branches"), dropped))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// ignoredTypes are records the harness writes that carry no step of the
// conversation: session bookkeeping, the harness's own file-history store, and
// posture flags. They are counted so the totals still add up.
var ignoredTypes = map[string]bool{
	"last-prompt": true, "mode": true, "permission-mode": true, "atis-latch": true,
	"bridge-session": true, "agent-name": true, "file-history-snapshot": true,
	"file-history-delta": true, "frame-link": true, "summary": true,
}

// modelUsage is one model's share of a cost-state record.
type modelUsage struct {
	Input      int64   `json:"inputTokens"`
	Output     int64   `json:"outputTokens"`
	Thinking   int64   `json:"thinkingTokens"`
	CacheRead  int64   `json:"cacheReadInputTokens"`
	CacheWrite int64   `json:"cacheCreationInputTokens"`
	CostUSD    float64 `json:"costUSD"`
}

// usage adds one API response's token counts to the session total. A response
// that arrived in several parts is written down once per part with the same
// message id and the same usage, so the id is what is counted, not the record.
func (b *builder) usage(e entry) {
	if len(e.Message.Usage) == 0 || b.counted[e.Message.ID] {
		return
	}
	b.counted[e.Message.ID] = true
	var u struct {
		Input      int64 `json:"input_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
		Output     int64 `json:"output_tokens"`
		Details    struct {
			Thinking int64 `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
	}
	if json.Unmarshal(e.Message.Usage, &u) != nil {
		return
	}
	b.tokens.Input += u.Input
	b.tokens.CacheRead += u.CacheRead
	b.tokens.CacheWrite += u.CacheWrite
	b.tokens.Output += u.Output
	b.tokens.Reasoning += u.Details.Thinking
	b.tokens.Total += u.Input + u.CacheRead + u.CacheWrite + u.Output
	b.tokens.Requests++
	b.tokens.Model(e.Message.Model, u.Input+u.CacheRead+u.CacheWrite+u.Output)
}

// costState is the harness's own account of the thread, rewritten as it goes.
// It is worth more than the sum of the response records: it is where the money
// is, and it counts the calls the harness makes on its own behalf, which are
// never written down as turns. The largest one wins, since it is a running
// total and a windowed read may start partway through.
func (b *builder) costState(e entry) {
	b.t.Ignore("cost-state")
	if e.TotalCostUSD <= b.cost.CostUSD && b.cost.Source != "" {
		return
	}
	u := transcript.Usage{Source: "cost-state", CostUSD: e.TotalCostUSD, Requests: b.cost.Requests}
	for model, m := range e.ModelUsage {
		u.Input += m.Input
		u.CacheRead += m.CacheRead
		u.CacheWrite += m.CacheWrite
		u.Output += m.Output
		u.Reasoning += m.Thinking
		u.Total += m.Input + m.CacheRead + m.CacheWrite + m.Output
		u.Model(model, m.Input+m.CacheRead+m.CacheWrite+m.Output)
	}
	b.cost = u
}

// spent settles the two accounts the harness keeps. They do not agree — the
// response records only hold what this thread was shown — so one is chosen
// rather than added, and which one is said out loud.
func (b *builder) spent() {
	switch {
	case b.cost.Total > 0:
		b.cost.Requests = b.tokens.Requests
		b.t.Usage = b.cost
	case b.tokens.Known():
		b.tokens.Source = "api responses"
		b.t.Usage = b.tokens
		b.t.Usage.CostUSD = b.cost.CostUSD
	case b.cost.CostUSD > 0:
		b.t.Usage = b.cost
	}
}

// link records one edge of the message DAG and moves the head forward.
func (b *builder) link(uuid, parent, logicalParent string) {
	b.cur = uuid
	if uuid == "" {
		return
	}
	if _, seen := b.parent[uuid]; !seen {
		p := parent
		if p == "" && logicalParent != "" {
			p = logicalParent
			// A compaction's logical parent should be the last record before
			// it. About one compaction in five names a record written after
			// it instead, one that descends from the compaction itself; the
			// chain then loops back to the compaction and every turn before
			// it is dropped as abandoned. The newest record before the
			// compaction is what it continues from.
			if _, written := b.parent[logicalParent]; !written && b.head != "" {
				p = b.head
			}
		}
		b.parent[uuid] = p
	}
	b.head = uuid
}

func (b *builder) entry(e entry, line int) {
	if e.Version != "" {
		b.t.Session.Version = e.Version
	}
	if e.CWD != "" {
		b.cwd = e.CWD
	}
	b.link(e.UUID, e.ParentUUID, e.LogicalParentUUID)
	if e.IsSidechain && !b.subagent {
		// Old versions inlined subagent turns into the parent log.
		b.t.Hidden++
		b.sidechain++
		b.t.Ignore("sidechain")
		return
	}
	switch e.Type {
	case "assistant":
		b.assistant(e, line)
		b.talked = true
	case "user":
		b.user(e, line)
		b.talked = true
	case "continued-in":
		b.t.Session.ContinuedIn = e.ContinuedIn
	// Where the session ran and as what: the paths in its events are
	// relative to the directory it had moved to.
	case "relocated":
		if e.RelocatedCWD != "" {
			b.note("working directory moved to " + e.RelocatedCWD)
		}
	case "worktree-state":
		if w := e.WorktreeSession; w != nil && w.Path != "" {
			b.note(strings.TrimSpace("worked in worktree " + w.Path + " " + branchNote(w.Branch)))
		} else {
			// Written at the start of sessions that never entered one, too.
			b.t.Ignore("worktree-state:none")
		}
	case "agent-setting":
		if e.AgentSetting != "" {
			b.note("ran as agent " + e.AgentSetting)
		}
	case "pr-link":
		b.add(generic.Event("pr-link", b.raw, e.Timestamp, line))
	case "system":
		b.system(e, line)
	case "attachment":
		b.attachment(e, line)
	case "queue-operation":
		b.queueOp(e, line)
	case "cost-state":
		b.costState(e)
	case "custom-title":
		if e.CustomTitle != "" {
			b.t.Session.Title = e.CustomTitle
		}
	case "ai-title":
		if e.AITitle != "" && b.t.Session.Title == "" {
			b.t.Session.Title = e.AITitle
		}
	default:
		if ignoredTypes[e.Type] {
			b.t.Ignore(e.Type)
			return
		}
		b.unknown(e.Type, e.Timestamp, line)
	}
}

// note adds a transcript note once, however often the harness repeats the
// record behind it.
func (b *builder) note(s string) {
	for _, n := range b.t.Notes {
		if n == s {
			return
		}
	}
	b.t.Notes = append(b.t.Notes, s)
}

func branchNote(branch string) string {
	if branch == "" {
		return ""
	}
	return "(branch " + branch + ")"
}

// unknown stands a record up that this build has no reading for. The record is
// flattened into the event, so a renamed type costs its pretty rendering and
// nothing else: it keeps its place, its number, and its content.
func (b *builder) unknown(recordType string, at time.Time, line int) {
	b.t.Unknown(recordType)
	// One stand-in per record, however many parts of it are strange. Records
	// are told apart by their count, not their line: a tail window has no
	// line numbers.
	if b.generic == b.t.Records {
		return
	}
	b.generic = b.t.Records
	b.add(generic.Event(recordType, b.raw, at, line))
}

// queueOp follows the queue of prompts typed while the model was working. A
// queued prompt is delivered later, as an attachment or as an ordinary user
// record, so each enqueue stands an event up and each delivery takes one back
// down. Whatever is still queued when the log ends was never answered.
func (b *builder) queueOp(e entry, line int) {
	b.t.Ignore("queue-operation:" + e.Operation)
	text := strings.TrimSpace(resultText(e.Content))
	switch e.Operation {
	case "enqueue":
		if text == "" {
			return
		}
		before := len(b.t.Events)
		b.prompt(e, text, line)
		if len(b.t.Events) == before {
			return
		}
		i := before
		if b.t.Events[i].Kind != transcript.KindUser {
			b.dequeue(text)
			return
		}
		b.t.Events[i].Tag = transcript.TagUnanswered
		if b.placeholder == nil {
			b.placeholder = map[int]bool{}
		}
		b.placeholder[i] = true
		b.queued = append(b.queued, pending{i, text})
	case "remove", "dequeue", "popAll":
		b.dequeue(text)
	}
}

// dequeue forgets the prompt a queue record released. A record that names no
// prompt took one without saying which, so with several waiting all of them
// are treated as delivered: guessing FIFO would eventually call a prompt that
// did reach the model "never answered", and a wrong claim is worse than a
// missed one. A prompt that is still queued with no release after it at all —
// the session died holding it — is unambiguous, and that is the case worth
// reporting.
func (b *builder) dequeue(text string) {
	if text == "" {
		b.queued = b.queued[:0]
		return
	}
	for i, q := range b.queued {
		if q.text == text {
			b.queued = append(b.queued[:i], b.queued[i+1:]...)
			return
		}
	}
}

// resolveQueue removes the stand-in event for every queued prompt that was
// delivered — the queue said so, or the same text turned up as a prompt of its
// own. Only prompts the session never got to are left, and they are marked.
func (b *builder) resolveQueue() {
	if len(b.placeholder) == 0 {
		return
	}
	said := map[string]bool{}
	for i, e := range b.t.Events {
		if e.Kind == transcript.KindUser && !b.placeholder[i] {
			said[e.Text] = true
		}
	}
	keep := map[int]bool{}
	for _, q := range b.queued {
		if !said[q.text] {
			keep[q.index] = true
		}
	}
	events, source := b.t.Events[:0], b.source[:0]
	for i, e := range b.t.Events {
		if b.placeholder[i] && !keep[i] {
			continue
		}
		events, source = append(events, e), append(source, b.source[i])
	}
	b.t.Events, b.source = events, source
	if n := len(keep); n > 0 {
		b.t.Notes = append(b.t.Notes, fmt.Sprintf("%s typed while the model was working and never answered, still queued when the log ends",
			plural(n, "prompt", "prompts")))
	}
}

// attachment handles the one subtype that is a conversation step. The rest are
// a known class the reader does not model; the subtype is kept in the count so
// the note says what was dropped.
func (b *builder) attachment(e entry, line int) {
	if e.Attachment.Type == "queued_command" {
		if text := strings.TrimSpace(resultText(e.Attachment.Prompt)); text != "" {
			b.dequeue(text)
			before := len(b.t.Events)
			b.prompt(e, text, line)
			if len(b.t.Events) > before && b.t.Events[before].Kind == transcript.KindUser {
				b.t.Events[before].Tag = transcript.TagQueued
			}
			return
		}
	}
	b.t.Ignore("attachment:" + e.Attachment.Type)
}

func (b *builder) assistant(e entry, line int) {
	if e.Message.Model != "" && e.Message.Model != "<synthetic>" {
		b.t.Session.Model = e.Message.Model
	}
	b.usage(e)
	var blocks []block
	_ = json.Unmarshal(e.Message.Content, &blocks)
	if e.Message.StopReason == "refusal" || e.Message.StopDetails.Type == "refusal" {
		text := strings.TrimSpace(e.Message.StopDetails.Category + "\n" + e.Message.StopDetails.Explanation)
		for _, bl := range blocks {
			if bl.Text != "" {
				text += "\n" + bl.Text
			}
		}
		b.add(transcript.Event{Time: e.Timestamp, Line: line, Kind: transcript.KindError, Tag: "refusal", Text: strings.TrimSpace(text)})
		return
	}
	if e.IsAPIErrorMessage {
		var parts []string
		for _, bl := range blocks {
			if bl.Text != "" {
				parts = append(parts, bl.Text)
			}
		}
		ev := transcript.Event{Time: e.Timestamp, Kind: transcript.KindError, Text: strings.Join(parts, "\n"), Line: line}
		if e.Error == "rate_limit" || transcript.IsUsageLimit(ev.Text) {
			ev.Tag = transcript.TagUsageLimit
		}
		b.add(ev)
		return
	}
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			if t := strings.TrimSpace(bl.Text); t != "" && t != "(no content)" {
				b.add(transcript.Event{Time: e.Timestamp, Kind: transcript.KindAssistant, Text: t, Line: line})
			}
		case "thinking":
			if t := strings.TrimSpace(bl.Thinking); t != "" {
				b.add(transcript.Event{Time: e.Timestamp, Kind: transcript.KindReasoning, Text: t, Line: line})
			}
		case "tool_use":
			ev := b.toolUse(bl)
			ev.Time, ev.Line, ev.CallID = e.Timestamp, line, bl.ID
			b.calls[bl.ID] = b.add(ev)
		default:
			b.unknown("assistant:"+bl.Type, e.Timestamp, line)
		}
	}
}

func (b *builder) toolUse(bl block) transcript.Event {
	var in map[string]json.RawMessage
	_ = json.Unmarshal(bl.Input, &in)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(in[k], &s)
		return s
	}
	e := transcript.Event{Kind: transcript.KindTool, Tool: bl.Name}
	switch bl.Name {
	case "Bash":
		e.Input = str("command")
	case "Read":
		e.Input = str("file_path")
		if o, l := string(in["offset"]), string(in["limit"]); o != "" || l != "" {
			e.Input += fmt.Sprintf(" [offset %s limit %s]", orDash(o), orDash(l))
		}
	case "Write":
		e.Kind = transcript.KindEdit
		e.Changes = []transcript.Change{patch.Write(str("file_path"), str("content"))}
	case "Edit":
		e.Kind = transcript.KindEdit
		e.Changes = []transcript.Change{patch.Replace(str("file_path"), str("old_string"), str("new_string"))}
	case "MultiEdit":
		e.Kind = transcript.KindEdit
		var edits []struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		_ = json.Unmarshal(in["edits"], &edits)
		for _, ed := range edits {
			e.Changes = append(e.Changes, patch.Replace(str("file_path"), ed.Old, ed.New))
		}
	case "NotebookEdit":
		e.Kind = transcript.KindEdit
		e.Changes = []transcript.Change{patch.Replace(str("notebook_path"), "", str("new_source"))}
	case "Grep":
		e.Input = strings.TrimSpace(str("pattern") + " " + str("path") + " " + str("glob"))
	case "Glob":
		e.Input = strings.TrimSpace(str("pattern") + " " + str("path"))
	case "WebFetch":
		e.Input = str("url")
	case "WebSearch":
		e.Input = str("query")
	case "Skill":
		e.Input = strings.TrimSpace(str("skill") + " " + str("args"))
	case "Agent", "Task":
		e.Kind = transcript.KindAgent
		e.Agent = firstNonEmpty(str("name"), str("subagent_type"), "subagent")
		e.Input = str("description")
		e.Text = str("prompt")
		e.AgentID = firstNonEmpty(b.children[str("name")], b.children[str("description")])
	case "SendMessage":
		e.Kind = transcript.KindAgent
		// The input names the recipient twice (to, recipient) and the text
		// twice (message, and a clipped preview in content); either spelling
		// is enough if a release drops the other.
		e.Agent = firstNonEmpty(str("to"), str("recipient"))
		e.Text = firstNonEmpty(str("message"), str("content"), str("summary"))
	case "TodoWrite":
		e.Kind = transcript.KindPlan
		var todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		}
		_ = json.Unmarshal(in["todos"], &todos)
		for _, td := range todos {
			e.Plan = append(e.Plan, transcript.Step{Text: td.Content, Status: td.Status})
		}
		b.t.Plan = e.Plan
	case "TaskCreate", "TaskUpdate":
		e.Kind = transcript.KindPlan
		var parts []string
		for _, k := range []string{"taskId", "subject", "status", "description"} {
			if v := strings.TrimSpace(str(k)); v != "" {
				parts = append(parts, v)
			}
		}
		e.Text = strings.Join(parts, " ")
	case "ExitPlanMode":
		e.Kind = transcript.KindPlan
		e.Text = str("plan")
	default:
		name := bl.Name
		if strings.HasPrefix(name, "mcp__") {
			name = strings.ReplaceAll(strings.TrimPrefix(name, "mcp__"), "__", ".")
		}
		e.Tool = name
		e.Input = clip.JSON(bl.Input, 400)
	}
	if e.Kind == transcript.KindEdit {
		e.Input = patch.Summary(e.Changes)
	}
	return e
}

var exitCodeRe = regexp.MustCompile(`^Exit code (-?\d+)`)

func (b *builder) user(e entry, line int) {
	if e.IsMeta {
		// Skill bodies, image attachments and resume markers the harness
		// injects under a user role.
		b.t.Ignore("user:meta")
		return
	}
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		b.prompt(e, s, line)
		return
	}
	var blocks []block
	_ = json.Unmarshal(e.Message.Content, &blocks)
	var texts []string
	for _, bl := range blocks {
		switch bl.Type {
		case "tool_result":
			b.toolResult(e, bl, line)
		case "text":
			texts = append(texts, bl.Text)
		case "image":
			texts = append(texts, "[image]")
		case "document":
			texts = append(texts, "[document]")
		default:
			b.unknown("user:"+bl.Type, e.Timestamp, line)
		}
	}
	if len(texts) > 0 {
		b.prompt(e, strings.Join(texts, "\n"), line)
	}
}

func (b *builder) toolResult(e entry, bl block, line int) {
	i, ok := b.calls[bl.ToolUseID]
	if !ok {
		// The call is outside the window being read, or the id that used to
		// join the two has moved. Either way the output is still an answer.
		b.add(b.t.Orphan("tool_result", resultText(bl.Content), e.Timestamp, line))
		return
	}
	b.answered[i] = true
	ev := &b.t.Events[i]
	ev.ResultSource, ev.ResultLine = b.file, line
	ev.Output = resultText(bl.Content)
	ev.Failed = bl.IsError
	if m := exitCodeRe.FindStringSubmatch(ev.Output); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			ev.ExitCode = &n
		}
	}
	if len(e.ToolUseResult) == 0 || e.ToolUseResult[0] != '{' {
		return
	}
	var r struct {
		FilePath        string                    `json:"filePath"`
		Type            string                    `json:"type"`
		StructuredPatch []patch.Hunk              `json:"structuredPatch"`
		AgentID         string                    `json:"agentId"`
		Interrupted     bool                      `json:"interrupted"`
		Git             map[string]map[string]any `json:"gitOperation"`
	}
	if json.Unmarshal(e.ToolUseResult, &r) != nil {
		return
	}
	if ev.Kind == transcript.KindEdit && len(r.StructuredPatch) > 0 && len(ev.Changes) > 0 {
		op := ev.Changes[0].Op
		if r.Type == "create" {
			op = "add"
		}
		ev.Changes = []transcript.Change{patch.FromHunks(firstNonEmpty(r.FilePath, ev.Changes[0].Path), op, r.StructuredPatch)}
		ev.Input = patch.Summary(ev.Changes)
	}
	if ev.Kind == transcript.KindAgent && r.AgentID != "" {
		ev.AgentID = r.AgentID
	}
	if r.Interrupted {
		ev.Failed = true
	}
	ev.Git = gitOps(r.Git)
}

// gitOps reads a tool result's gitOperation, which Claude Code writes as
// one object per operation: {"commit": {sha, kind, branch}, "push":
// {branch}, "branch": {ref, action}}. Each is read by its fields rather than
// its name, so an operation added in a later release is not dropped.
func gitOps(git map[string]map[string]any) []transcript.GitOp {
	names := make([]string, 0, len(git))
	for name := range git {
		names = append(names, name)
	}
	// In the order they happen: a command that commits and pushes does so
	// in that order.
	rank := map[string]int{"commit": 1, "branch": 2, "push": 3}
	sort.Slice(names, func(i, j int) bool {
		if rank[names[i]] != rank[names[j]] {
			return rank[names[i]] < rank[names[j]]
		}
		return names[i] < names[j]
	})
	var ops []transcript.GitOp
	for _, name := range names {
		f := git[name]
		str := func(k string) string { s, _ := f[k].(string); return s }
		action := firstNonEmpty(str("kind"), str("action"))
		if action == "" {
			action = map[string]string{"commit": "committed", "push": "pushed"}[name]
		}
		ops = append(ops, transcript.GitOp{Action: firstNonEmpty(action, name), SHA: str("sha"),
			Branch: firstNonEmpty(str("branch"), str("ref"))})
	}
	return ops
}

// resultText flattens tool_result content (string or content blocks).
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ToolName string `json:"tool_name"`
	}
	_ = json.Unmarshal(raw, &blocks)
	parts := make([]string, 0, len(blocks))
	for _, bl := range blocks {
		switch {
		case bl.Text != "":
			parts = append(parts, bl.Text)
		case bl.Type == "tool_reference":
			parts = append(parts, "[tool "+bl.ToolName+"]")
		default:
			parts = append(parts, "["+bl.Type+"]")
		}
	}
	return strings.Join(parts, "\n")
}

var bashInputRe = regexp.MustCompile(`(?s)<bash-input>(.*?)</bash-input>`)

// prompt classifies a user-role text: human prompt, harness notification,
// teammate message, slash command or interrupt marker.
func (b *builder) prompt(e entry, s string, line int) {
	ev := transcript.Event{Time: e.Timestamp, Line: line}
	trimmed := strings.TrimSpace(s)
	// Harness wrappers can be preceded by a short banner line ("Another
	// Claude session sent a message:"). Only a single line counts: a person
	// who writes about one of these tags mid-sentence is still a person.
	for _, tag := range []string{"<teammate-message", "<task-notification>"} {
		if i := strings.Index(trimmed, tag); i > 0 && i < 200 && trimmed[i-1] == '\n' &&
			!strings.Contains(trimmed[:i-1], "\n") {
			trimmed = trimmed[i:]
		}
	}
	switch {
	case e.IsCompactSummary:
		ev.Kind, ev.Tag, ev.Text = transcript.KindNotice, transcript.TagCompaction, "compaction summary handed to the model:\n"+trimmed
	case strings.HasPrefix(trimmed, "<task-notification>"):
		ev.Kind, ev.Tag = transcript.KindNotice, transcript.TagNotification
		ev.Text = strings.TrimSpace(clip.Tag(trimmed, "status") + ": " + clip.Tag(trimmed, "summary"))
		if r := clip.Tag(trimmed, "result"); r != "" {
			ev.Text += "\n" + r
		}
	case strings.HasPrefix(trimmed, "<teammate-message"):
		ev.Kind, ev.Tag = transcript.KindAgent, "message"
		ev.Agent = clip.Attr(trimmed, "teammate-message", "teammate_id") + " → me"
		ev.Text = clip.Tag(trimmed, "teammate-message")
	case strings.HasPrefix(trimmed, "<command-name>") || strings.HasPrefix(trimmed, "<command-message>"):
		ev.Kind, ev.Tag = transcript.KindNotice, transcript.TagCommand
		ev.Text = strings.TrimSpace(clip.Tag(trimmed, "command-name") + " " + clip.Tag(trimmed, "command-args"))
	case strings.HasPrefix(trimmed, "<local-command-stdout>"):
		ev.Kind, ev.Tag, ev.Text = transcript.KindNotice, transcript.TagCommand, clip.Tag(trimmed, "local-command-stdout")
	case strings.HasPrefix(trimmed, "<bash-input>"):
		m := bashInputRe.FindStringSubmatch(trimmed)
		ev.Kind, ev.Tool, ev.Tag = transcript.KindTool, "user-shell", "user"
		if m != nil {
			ev.Input = m[1]
		}
	case strings.HasPrefix(trimmed, "<bash-stdout>") || strings.HasPrefix(trimmed, "<bash-stderr>"):
		out := strings.TrimSpace(clip.Tag(trimmed, "bash-stdout") + "\n" + clip.Tag(trimmed, "bash-stderr"))
		if n := len(b.t.Events); n > 0 && b.t.Events[n-1].Tool == "user-shell" {
			b.t.Events[n-1].Output = out
			return
		}
		// The command is outside the window being read. The output is
		// still what the person saw.
		if out != "" {
			b.add(b.t.Orphan("user-shell output", out, e.Timestamp, line))
		}
		return
	case strings.HasPrefix(trimmed, "[Request interrupted by user"):
		ev.Kind, ev.Tag, ev.Text = transcript.KindNotice, transcript.TagInterrupted, trimmed
	default:
		text := clip.StripInjected(s)
		if text == "" {
			return
		}
		ev.Kind, ev.Text = transcript.KindUser, text
	}
	if ev.Text == "" && ev.Input == "" {
		return
	}
	b.add(ev)
}

func (b *builder) system(e entry, line int) {
	var content string
	_ = json.Unmarshal(e.Content, &content)
	ev := transcript.Event{Time: e.Timestamp, Line: line}
	switch e.Subtype {
	case "compact_boundary":
		ev.Kind, ev.Tag, ev.Text = transcript.KindNotice, transcript.TagCompaction, "context compacted (earlier turns remain in this log)"
		// A log that opens on a compaction is a conversation carried over
		// from another session; nothing before it is in this file.
		if !b.talked && !b.subagent && !b.midway {
			b.t.Session.Continues, b.carried = true, e.LogicalParentUUID
			ev.Text = "context compacted in the session this one continues (earlier turns are in that session's log, not this one)"
		}
	case "api_error":
		ev.Kind, ev.Text = transcript.KindError, firstNonEmpty(content, "API error")
		if transcript.IsUsageLimit(content) {
			ev.Tag = transcript.TagUsageLimit
		}
	case "local_command":
		ev.Kind, ev.Tag = transcript.KindNotice, transcript.TagCommand
		ev.Text = strings.TrimSpace(clip.Tag(content, "command-name") + " " + clip.Tag(content, "command-args"))
		if ev.Text == "" {
			// The command's output is written the same way, in the wrapper
			// a user record uses for it.
			ev.Text = clip.Tag(content, "local-command-stdout")
		}
		if ev.Text == "" {
			ev.Text = clip.StripInjected(content)
		}
	case "informational", "scheduled_task_fire":
		ev.Kind, ev.Text = transcript.KindNotice, content
	default:
		b.t.Ignore("system:" + e.Subtype)
		return
	}
	if strings.TrimSpace(ev.Text) != "" {
		b.add(ev)
	}
}

// childIndex maps subagent names and descriptions to agent ids so Agent tool
// calls in the parent can point at the subagent's own transcript.
func childIndex(dir string) map[string]string {
	out := map[string]string{}
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	for _, m := range metas {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var am agentMeta
		if json.Unmarshal(raw, &am) != nil {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "agent-"), ".meta.json")
		if am.Name != "" {
			out[am.Name] = id
		}
		if am.Description != "" {
			out[am.Description] = id
		}
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func orDash(s string) string {
	if s == "" || s == "null" {
		return "-"
	}
	return s
}

type recordSource struct {
	file string
	line int
}

// hideRepeats runs after legacy parsing, queue resolution, branch selection
// and numbering. Pairing and every existing event slot therefore stay intact.
func hideRepeats(t *transcript.Transcript, repeats map[recordSource]recordSource) {
	if len(repeats) == 0 {
		return
	}
	originals := map[recordSource][]int{}
	offsets := map[recordSource]int{}
	kept := t.Events[:0]
	for _, e := range t.Events {
		src := recordSource{e.Source, e.Line}
		if original, ok := repeats[src]; ok {
			offset := offsets[src]
			offsets[src]++
			if indices := originals[original]; offset < len(indices) {
				e.RepeatOf = indices[offset]
				t.Copies = append(t.Copies, e)
				continue
			}
		}
		originals[src] = append(originals[src], e.Index)
		kept = append(kept, e)
	}
	t.Events = kept
	t.Notes = append(t.Notes, fmt.Sprintf("%d repeated records hidden", len(repeats)))
}
