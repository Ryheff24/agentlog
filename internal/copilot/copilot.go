// Package copilot reads GitHub Copilot CLI sessions
// (~/.copilot/session-state/<id>/events.jsonl + workspace.yaml).
package copilot

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentlog/internal/clip"
	"agentlog/internal/generic"
	"agentlog/internal/jsonl"
	"agentlog/internal/patch"
	"agentlog/internal/transcript"
)

// Provider locates and parses Copilot CLI logs under Home (default ~/.copilot).
type Provider struct {
	Home string
}

// New returns a provider rooted at $COPILOT_HOME or ~/.copilot.
func New() *Provider {
	home := os.Getenv("COPILOT_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".copilot")
		}
	}
	return &Provider{Home: home}
}

// Name implements the catalog provider contract.
func (p *Provider) Name() string { return transcript.Copilot }

// Roots lists the directories scanned.
func (p *Provider) Roots() []string { return []string{filepath.Join(p.Home, "session-state")} }

// SelfID is not exposed by Copilot CLI.
func (p *Provider) SelfID() string { return "" }

// Discover lists sessions whose events.jsonl changed at or after q.Since.
func (p *Provider) Discover(q transcript.Query) ([]*transcript.Session, error) {
	dirs, err := os.ReadDir(p.Roots()[0])
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*transcript.Session
	for _, d := range dirs {
		if !d.IsDir() || !q.MatchID(d.Name()) {
			continue
		}
		dir := filepath.Join(p.Roots()[0], d.Name())
		path := filepath.Join(dir, "events.jsonl")
		st, err := os.Stat(path)
		if err != nil || st.ModTime().Before(q.Since) || st.Size() == 0 {
			continue
		}
		ws := readYAML(filepath.Join(dir, "workspace.yaml"))
		s := &transcript.Session{
			Provider: transcript.Copilot,
			ID:       d.Name(),
			Files:    []string{path},
			Size:     st.Size(),
			Updated:  st.ModTime(),
			CWD:      ws["cwd"],
			Branch:   ws["branch"],
			Title:    firstNonEmpty(ws["name"], ws["summary"]),
			Live:     q.Live(st.ModTime()),
		}
		if t, err := time.Parse(time.RFC3339Nano, ws["created_at"]); err == nil {
			s.Started = t
		}
		out = append(out, s)
	}
	return out, nil
}

// readYAML reads flat "key: value" lines, which is all workspace.yaml holds.
func readYAML(path string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok && !strings.HasPrefix(k, " ") {
			m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return m
}

type event struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Data      struct {
		Content          string          `json:"content"`
		ToolCallID       string          `json:"toolCallId"`
		ToolName         string          `json:"toolName"`
		Arguments        json.RawMessage `json:"arguments"`
		Success          *bool           `json:"success"`
		ParentToolCallID string          `json:"parentToolCallId"`
		Result           struct {
			Content string `json:"content"`
		} `json:"result"`
		Encrypted    string `json:"encryptedContent"`
		ToolRequests []struct {
			Name string `json:"name"`
		} `json:"toolRequests"`
		Error            json.RawMessage            `json:"error"`
		ErrorType        string                     `json:"errorType"`
		Message          string                     `json:"message"`
		Reason           string                     `json:"reason"`
		NewModel         string                     `json:"newModel"`
		CopilotVersion   string                     `json:"copilotVersion"`
		WarningType      string                     `json:"warningType"`
		AgentName        string                     `json:"agentName"`
		AgentDisplayName string                     `json:"agentDisplayName"`
		TokenDetails     map[string]json.RawMessage `json:"tokenDetails"`
		ModelMetrics     map[string]modelMetric     `json:"modelMetrics"`
		TotalToolCalls   int                        `json:"totalToolCalls"`
		SummaryContent   string                     `json:"summaryContent"`
		Context          struct {
			CWD    string `json:"cwd"`
			Branch string `json:"branch"`
		} `json:"context"`
	} `json:"data"`
}

// ignoredTypes are Copilot events the reader knows about and does not model:
// turn and hook boundaries, permission prompts, posture and session-level
// telemetry. Anything outside this list is reported as an unknown type.
var ignoredTypes = map[string]bool{
	"session.auto_mode_resolved": true, "session.usage_checkpoint": true,
	"assistant.turn_start": true, "assistant.turn_end": true, "turn.completed": true,
	"response.completed": true, "hook.start": true, "hook.end": true,
	"permission.requested": true, "permission.completed": true,
	"session.mode_changed": true, "session.info": true,
	"session.plan_changed": true, "session.task_complete": true,
	"session.compaction_start": true, "system.notification": true, "system.message": true,
	"skill.invoked": true, "subagent.selected": true, "subagent.deselected": true,
	"session.permissions_changed": true,
	"model.turn_started":          true, "model.turn_ended": true, "model.model_call_started": true,
	"model.model_call_failure": true, "model.captured_assignment_context": true,
}

// Load parses a Copilot session. Events emitted inside subagents (those with
// a parentToolCallId) are counted as hidden; the task call carries the result.
func (p *Provider) Load(s *transcript.Session, o transcript.Load) (*transcript.Transcript, error) {
	t := &transcript.Transcript{Session: *s}
	calls := map[string]int{}
	account := usageAccount{}
	answered := map[int]bool{}
	// Copilot runs subagents inline: every event a subagent emits carries the
	// tool call id of the task that started it, and nothing else identifies it.
	agentNames := map[string]string{} // task call id -> subagent name
	hidden := map[string]int{}        // task call id -> records left out
	childOf := map[int]string{}       // event index -> task call id that ran it
	skipped := map[string]bool{}      // call ids deliberately not modelled, so
	//                                   their completions are not read as lost
	add := func(e transcript.Event) int {
		e.Source = s.File()
		transcript.ClassifyPrompt(&e, &t.Session)
		// Copilot writes tool arguments as the model produced them, so a path
		// is relative whenever the model wrote it that way.
		transcript.Localise(&e, t.Session.CWD)
		t.Events = append(t.Events, e)
		return len(t.Events) - 1
	}
	read := jsonl.Each
	switch {
	case o.Window.TailBytes > 0:
		read = func(path string, fn func([]byte, int) error) error { return jsonl.Tail(path, o.Window.TailBytes, fn) }
	case o.Window.HeadBytes > 0:
		read = func(path string, fn func([]byte, int) error) error { return jsonl.Head(path, o.Window.HeadBytes, fn) }
	}
	err := read(s.File(), func(line []byte, n int) error {
		t.Records++
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			// A line that is not JSON is the one being written right now. A
			// record that is JSON but whose fields changed type is a format
			// change, and must show as one rather than vanish.
			var te *json.UnmarshalTypeError
			if !errors.As(err, &te) || ev.Type == "" {
				return nil
			}
			t.Unknown(ev.Type)
			add(generic.Event(ev.Type, line, ev.Timestamp, n))
			return nil
		}
		d := &ev.Data
		base := transcript.Event{Time: ev.Timestamp, Line: n}
		if d.ParentToolCallID != "" {
			if !o.Subagents {
				t.Hidden++
				hidden[d.ParentToolCallID]++
				return nil
			}
			base.Tag = transcript.TagSubagent
		}
		before := len(t.Events)
		switch ev.Type {
		case "subagent.started":
			// The name is the whole of what this record carries, and it is
			// carried by the task call. Counting it says the record was read
			// even though it produced nothing — so the day the field is
			// renamed, the count stops and the drift shows.
			if name := firstNonEmpty(d.AgentDisplayName, d.AgentName); name != "" {
				agentNames[d.ToolCallID] = name
				t.Ignore("subagent.started")
			}
		case "session.shutdown":
			account.shutdown(d.ModelMetrics, len(d.TokenDetails) > 0)
			t.Ignore(ev.Type)
		case "session.resume":
			account.resume()
			t.Ignore(ev.Type)
		case "session.start":
			account.resume()
			if t.Session.CWD == "" {
				t.Session.CWD = d.Context.CWD
			}
			t.Session.Version = d.CopilotVersion
		case "session.model_change":
			t.Session.Model = d.NewModel
		case "user.message":
			if text := clip.StripInjected(d.Content); text != "" {
				base.Kind, base.Text = transcript.KindUser, text
				i := add(base)
				transcript.PromptHints(&t.Events[i], line)
			}
		case "assistant.message":
			// Copilot encrypts the model's own words and sends them back to
			// itself; what is left in the clear is sometimes nothing at all.
			// A turn that only made tool calls is carried by those calls, and
			// is counted rather than shown. A turn that said something
			// unreadable is shown as unreadable, because an assistant event
			// that is simply missing reads as a turn where nothing happened.
			switch text := strings.TrimSpace(d.Content); {
			case text != "":
				base.Kind, base.Text = transcript.KindAssistant, text
				add(base)
			case len(d.ToolRequests) > 0:
				t.Ignore("assistant.message:tool calls only")
			case d.Encrypted != "":
				base.Kind, base.Tag = transcript.KindAssistant, "encrypted"
				base.Text = fmt.Sprintf("[%d bytes the harness encrypted; this build cannot read them]", len(d.Encrypted))
				add(base)
			default:
				t.Ignore("assistant.message:empty")
			}
		case "assistant.reasoning":
			if text := strings.TrimSpace(d.Content); text != "" {
				base.Kind, base.Text = transcript.KindReasoning, text
				add(base)
			}
		case "tool.execution_start":
			if d.ToolName == "report_intent" {
				t.Ignore("tool.execution_start:report_intent")
				skipped[d.ToolCallID] = true
				return nil
			}
			e := toolEvent(base, d.ToolName, d.Arguments)
			e.CallID = d.ToolCallID
			if e.Kind == transcript.KindAgent && e.Agent != "" {
				agentNames[d.ToolCallID] = e.Agent
			}
			calls[d.ToolCallID] = add(e)
		case "tool.execution_complete":
			if skipped[d.ToolCallID] {
				t.Ignore("tool.execution_complete:report_intent")
				return nil
			}
			i, ok := calls[d.ToolCallID]
			if !ok {
				// A completion whose start is not in this file: shown as
				// itself rather than thrown away.
				add(t.Orphan(ev.Type, d.Result.Content, ev.Timestamp, n))
				return nil
			}
			answered[i] = true
			e := &t.Events[i]
			e.Output = d.Result.Content
			if d.Success != nil && !*d.Success {
				e.Failed = true
				if len(d.Error) > 0 && string(d.Error) != "null" {
					e.Output = strings.TrimSpace(e.Output + "\n" + clip.JSON(d.Error, 500))
				}
			}
		case "subagent.completed", "subagent.failed":
			i, ok := calls[d.ToolCallID]
			if !ok {
				add(t.Orphan(ev.Type, d.SummaryContent, ev.Timestamp, n))
				return nil
			}
			t.Ignore(ev.Type)
			e := &t.Events[i]
			e.Tag = strings.TrimPrefix(ev.Type, "subagent.")
			if ev.Type == "subagent.failed" {
				e.Failed = true
			}
		case "session.error":
			base.Kind, base.Text = transcript.KindError, strings.TrimSpace(d.ErrorType+": "+d.Message)
			if transcript.IsUsageLimit(base.Text) {
				base.Tag = transcript.TagUsageLimit
			}
			add(base)
		case "model.turn_failed":
			// The model call behind the turn failed; this is why it stopped.
			why := strings.TrimSpace(string(d.Error))
			if err := json.Unmarshal(d.Error, &why); err != nil && why == "" {
				why = "unknown error"
			}
			base.Kind, base.Text = transcript.KindError, "turn failed: "+why
			if transcript.IsUsageLimit(base.Text) {
				base.Tag = transcript.TagUsageLimit
			}
			add(base)
		case "session.warning":
			base.Kind, base.Text = transcript.KindNotice, strings.TrimSpace(d.WarningType+": "+d.Message)
			add(base)
		case "abort":
			base.Kind, base.Tag, base.Text = transcript.KindNotice, transcript.TagInterrupted, "aborted: "+d.Reason
			add(base)
		case "session.compaction_complete":
			base.Kind, base.Tag = transcript.KindNotice, transcript.TagCompaction
			base.Text = strings.TrimSpace("context compacted\n" + d.SummaryContent)
			add(base)
		default:
			if ignoredTypes[ev.Type] {
				t.Ignore(ev.Type)
				return nil
			}
			t.Unknown(ev.Type)
			add(generic.Event(ev.Type, line, ev.Timestamp, n))
		}
		if d.ParentToolCallID != "" {
			for i := before; i < len(t.Events); i++ {
				childOf[i] = d.ParentToolCallID
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.File(), err)
	}
	for i := range t.Events {
		if t.Events[i].CallID != "" && !answered[i] {
			t.Events[i].Pending = true
		}
	}
	// A subagent names itself after its first events are already in, so the
	// attribution is resolved once the whole file has been read.
	for i, callID := range childOf {
		t.Events[i].Agent = agentNames[callID]
	}
	switch {
	case len(hidden) > 0:
		t.Notes = append(t.Notes, fmt.Sprintf("%d log records written inside %s are hidden (%s) — --subagents to include them",
			sum(hidden), plural(len(hidden), "subagent run"), byName(agentNames, hidden)))
	case len(childOf) > 0:
		t.Notes = append(t.Notes, fmt.Sprintf("%d of these events were run by %s, each marked with the subagent that ran it",
			len(childOf), plural(len(agentRuns(childOf)), "subagent run")))
	}
	t.Usage = account.usage()
	t.MarkHandbacks()
	t.Number()
	for i, e := range t.Events {
		if e.Tool != "task" {
			continue
		}
		parent := 0
		if call := childOf[i]; call != "" {
			if index, ok := calls[call]; ok {
				parent = t.Events[index].Index
			}
		}
		name := firstNonEmpty(agentNames[e.CallID], e.Agent)
		t.Session.InlineAgents = append(t.Session.InlineAgents, transcript.InlineAgent{Event: e.Index, ParentEvent: parent, Name: name, Subagents: o.Subagents})
	}

	t.Session.Ending = transcript.Ending(t.Events)
	return t, nil
}

func toolEvent(e transcript.Event, name string, args json.RawMessage) transcript.Event {
	e.Kind, e.Tool = transcript.KindTool, name
	var a map[string]json.RawMessage
	_ = json.Unmarshal(args, &a)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(a[k], &s)
		return s
	}
	switch name {
	case "bash":
		e.Input = str("command")
	case "view", "read_file":
		e.Input = str("path")
	case "rg", "grep", "glob":
		// Newer versions name the location "paths", as a string or a list.
		where := str("path")
		if where == "" {
			var list []string
			if json.Unmarshal(a["paths"], &list) == nil {
				where = strings.Join(list, " ")
			} else {
				where = str("paths")
			}
		}
		e.Input = strings.TrimSpace(str("pattern") + " " + where)
	case "apply_patch":
		var s string
		if json.Unmarshal(args, &s) != nil {
			s = firstNonEmpty(str("input"), str("patch"))
		}
		e.Kind, e.Changes = transcript.KindEdit, patch.ParseEnvelope(s)
		e.Input = patch.Summary(e.Changes)
	case "filesystem-write_file":
		e.Kind = transcript.KindEdit
		e.Changes = []transcript.Change{patch.Write(str("path"), str("content"))}
		e.Input = patch.Summary(e.Changes)
	case "filesystem-edit_file":
		var dryRun bool
		_ = json.Unmarshal(a["dryRun"], &dryRun)
		if dryRun {
			e.Input = clip.JSON(args, 400)
			break
		}
		var edits []struct {
			Old string `json:"oldText"`
			New string `json:"newText"`
		}
		_ = json.Unmarshal(a["edits"], &edits)
		e.Kind = transcript.KindEdit
		for _, edit := range edits {
			e.Changes = append(e.Changes, patch.Replace(str("path"), edit.Old, edit.New))
		}
		e.Input = patch.Summary(e.Changes)
	case "edit", "str_replace_editor":
		e.Kind = transcript.KindEdit
		e.Changes = []transcript.Change{patch.Replace(str("path"), str("old_str"), str("new_str"))}
		e.Input = patch.Summary(e.Changes)
	case "create":
		e.Kind = transcript.KindEdit
		e.Changes = []transcript.Change{patch.Write(str("path"), str("file_text"))}
		e.Input = patch.Summary(e.Changes)
	case "task":
		e.Kind = transcript.KindAgent
		e.Agent = firstNonEmpty(str("name"), str("agent_type"))
		e.Input = str("description")
		e.Text = str("prompt")
	default:
		e.Input = clip.JSON(args, 400)
	}
	return e
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// byName totals a per-task-call count by subagent name, busiest first. One
// subagent is usually run many times, so the names repeat.
func byName(agentNames map[string]string, counts map[string]int) string {
	totals := map[string]int{}
	runs := map[string]int{}
	for id, n := range counts {
		name := agentNames[id]
		if name == "" {
			name = "unnamed"
		}
		totals[name] += n
		runs[name]++
	}
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	// Ties break on the name so the note reads the same way twice.
	sort.Slice(names, func(i, j int) bool {
		if totals[names[i]] != totals[names[j]] {
			return totals[names[i]] > totals[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, 0, len(names))
	for i, name := range names {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(names)-4))
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", name, runs[name]))
	}
	return strings.Join(parts, ", ")
}

// agentRuns is the set of task calls that produced the given child events.
func agentRuns(childOf map[int]string) map[string]bool {
	runs := map[string]bool{}
	for _, callID := range childOf {
		runs[callID] = true
	}
	return runs
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
