// Package usage measures agentlog from the outside: it reads the harnesses'
// own session logs for the shell calls agents made to agentlog, and reports
// how long each took as the agent waited for it and how much it put into the
// agent's context. It shares no reading code with agentlog, so a change that
// breaks agentlog's readers does not also blind the thing measuring it; the
// one thing it takes from agentlog is the record each run writes of itself
// (package monitor), to split the wait into agentlog's part and the rest.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/shell"
)

// Call is one tool call in which an agent ran agentlog.
type Call struct {
	ToolID      string `json:"tool_id,omitempty"`
	Development bool   `json:"development,omitempty"`
	Harness     string `json:"harness"`
	Session     string `json:"session"`         // the log's session id
	Agent       string `json:"agent,omitempty"` // a Claude subagent's id, when the caller was one
	CWD         string `json:"cwd,omitempty"`
	File        string `json:"file"`
	Command     string `json:"command"`
	Kind        string `json:"kind"`  // which view: preview, list, grep, projects, …
	Count       int    `json:"count"` // agentlog invocations in the command
	// Pure is a command that runs only agentlog (piped through head and the
	// like, or several in a row): its time and output are agentlog's. A
	// command that also builds, greps or cats mixes in other costs.
	Pure  bool      `json:"pure"`
	Start time.Time `json:"start"` // the model asked for the call
	End   time.Time `json:"end"`   // the result was recorded
	Next  time.Time `json:"next"`  // the model's next step after reading it
	Out   int       `json:"out"`   // bytes of result the agent read
	Error bool      `json:"error,omitempty"`
	// ShellMs is the harness's own timing of the command, when it logs one
	// (Codex writes "Wall time N seconds").
	ShellMs float64 `json:"shell_ms,omitempty"`
	// Self are agentlog's own records of the runs inside this call.
	Self []Run `json:"self,omitempty"`
}

// WaitMs is how long the agent waited for the call.
func (c Call) WaitMs() float64 { return ms(c.End.Sub(c.Start)) }

// ReadMs is how long the model took to take its next step after the result.
func (c Call) ReadMs() float64 {
	if c.Next.IsZero() {
		return -1
	}
	return ms(c.Next.Sub(c.End))
}

// SelfMs is how long agentlog itself ran within the call, when it recorded it.
func (c Call) SelfMs() float64 {
	t := 0.0
	for _, r := range c.Self {
		t += r.Ms
	}
	return t
}

// Session is one log's totals, so agentlog's share of what the agent read
// can be seen against everything its tools returned.
type Session struct {
	Development bool   `json:"development,omitempty"`
	Harness     string `json:"harness"`
	ID          string `json:"id"`
	Agent       string `json:"agent,omitempty"`
	CWD         string `json:"cwd,omitempty"`
	File        string `json:"file"`
	ToolBytes   int    `json:"tool_bytes"` // every tool result, about
}

// Scan reads the logs under the harness roots written since since.
func Scan(claudeRoot, codexRoot string, since time.Time) ([]Call, []Session) {
	var calls []Call
	var sessions []Session
	walk := func(root string, read func(string) ([]Call, Session)) {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			if info, err := d.Info(); err != nil || info.ModTime().Before(since) {
				return nil
			}
			cs, s := read(path)
			dev, cutoff := callerMetadata(path, s.CWD)
			s.Development = dev
			kept := cs[:0]
			for _, c := range cs {
				c.Development = dev
				if !cutoff.IsZero() && !c.Start.After(cutoff) {
					continue
				}
				kept = append(kept, c)
			}
			cs = kept
			if len(cs) > 0 {
				calls = append(calls, cs...)
				sessions = append(sessions, s)
			}
			return nil
		})
	}
	if claudeRoot != "" {
		walk(claudeRoot, readClaude)
	}
	if codexRoot != "" {
		walk(codexRoot, readCodex)
	}
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].Start.Before(calls[j].Start) })
	seen := map[string]bool{}
	unique := calls[:0]
	for _, c := range calls {
		key := c.Harness + "/" + c.ToolID
		if c.ToolID != "" && seen[key] {
			continue
		}
		if c.ToolID != "" {
			seen[key] = true
		}
		unique = append(unique, c)
	}
	files := map[string]bool{}
	for _, c := range unique {
		files[c.File] = true
	}
	keptSessions := sessions[:0]
	for _, s := range sessions {
		if files[s.File] {
			keptSessions = append(keptSessions, s)
		}
	}
	return unique, keptSessions
}

// Kind names the view a command asked agentlog for, from its first run.
func Kind(command string) (kind string, count int) {
	if ms := jsCmd.FindAllStringSubmatch(command, -1); len(ms) > 0 {
		for _, m := range ms {
			k, n := Kind(unescape(m[1]))
			if kind == "" {
				kind = k
			}
			count += n
		}
		return kind, count
	}
	var args []string
	for _, c := range shell.Commands(command) {
		words := shell.CommandWords(c.Args)
		if len(words) == 0 {
			continue
		}
		if filepath.Base(words[0]) == "agentlog" {
			if count == 0 {
				args = words[1:]
			}
			count++
		} else if base := filepath.Base(words[0]); base == "bash" || base == "sh" || base == "zsh" {
			for i := 1; i+1 < len(words); i++ {
				if strings.HasPrefix(words[i], "-") && !strings.HasPrefix(words[i], "--") && strings.Contains(words[i], "c") {
					k, n := Kind(words[i+1])
					if kind == "" && count == 0 {
						kind = k
					}
					count += n
					break
				}
			}
		}
	}
	if count == 0 {
		return "", 0
	}
	if kind != "" {
		return kind, count
	}
	has := func(names ...string) bool {
		for _, a := range args {
			name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
			for _, n := range names {
				if strings.HasPrefix(a, "-") && name == n {
					return true
				}
			}
		}
		return false
	}
	ref := false
	for _, a := range args {
		if strings.Contains(a, "#") && !strings.HasPrefix(a, "-") {
			ref = true
		}
	}
	switch {
	case len(args) > 0 && args[0] == "feedback":
		kind = "feedback"
	case len(args) > 0 && args[0] == "doctor":
		kind = "doctor"
	case has("help", "h"):
		kind = "help"
	case has("projects"):
		kind = "projects"
	case has("grep"):
		kind = "grep"
	case has("list", "l"):
		kind = "list"
	case ref:
		kind = "event"
	case has("edits", "diff", "files"):
		kind = "edits"
	case has("usage"):
		kind = "usage"
	case has("tools"):
		kind = "tools"
	case has("subagents"):
		kind = "subagents"
	case has("tail", "head", "kind"):
		kind = "timeline"
	case len(args) == 0:
		kind = "home"
	default:
		kind = "preview"
	}
	return kind, count
}

// jsCmd is a command string in a Codex exec script: cmd:"…" or "cmd":"…".
var jsCmd = regexp.MustCompile(`"?cmd"?\s*:\s*"((?:[^"\\]|\\.)*)"`)

var envAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// filter is what an agent pipes agentlog's output through to cut it.
var filter = regexp.MustCompile(`^(head|tail|cut|sed|grep|rg|awk|wc|jq|python3?|less|cat|sort|uniq|tr)\b`)

// Pure reports a command that runs agentlog and nothing else of cost: cd,
// echo, loops, and the filters output is piped through are allowed.
func Pure(command string) bool {
	if ms := jsCmd.FindAllStringSubmatch(command, -1); len(ms) > 0 {
		for _, m := range ms {
			if !Pure(unescape(m[1])) {
				return false
			}
		}
		return true
	}
	ran := false
	for _, seg := range regexp.MustCompile(`&&|\|\||;|\n`).Split(command, -1) {
		seg = strings.TrimSpace(seg)
		for _, kw := range []string{"do ", "then ", "time "} {
			seg = strings.TrimSpace(strings.TrimPrefix(seg, kw))
		}
		head, _, piped := strings.Cut(seg, "|")
		word := strings.Fields(head)
		for len(word) > 1 && envAssign.MatchString(word[0]) {
			word = word[1:]
		}
		head = strings.Join(word, " ")
		switch {
		case head == "" || head == "done" || head == "fi" || head == "else":
		case len(word) > 0 && (word[0] == "cd" || word[0] == "echo" || word[0] == "for" || word[0] == "if" || word[0] == "printf"):
		case len(word) > 0 && filepath.Base(word[0]) == "agentlog":
			ran = true
			if piped {
				_, rest, _ := strings.Cut(seg, "|")
				for _, f := range strings.Split(rest, "|") {
					if !filter.MatchString(strings.TrimSpace(f)) {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	return ran
}

// ─── Claude Code ─────────────────────────────────────────────────

func readClaude(path string) ([]Call, Session) {
	s := Session{Harness: "claude", File: path}
	if base := filepath.Base(path); strings.HasPrefix(base, "agent-") && filepath.Base(filepath.Dir(path)) == "subagents" {
		s.Agent = strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")
	}
	var (
		calls   []Call
		pending = map[string]int{} // tool_use id → index in calls
		reading []int              // calls whose result is in, waiting for the model's next step
	)
	eachLine(path, func(line []byte) {
		if s.ID == "" {
			s.ID = field(line, "sessionId")
		}
		if s.CWD == "" {
			s.CWD = field(line, "cwd")
		}
		switch {
		case bytes.Contains(line, []byte(`"type":"assistant"`)):
			ts := stamp(line)
			for _, i := range reading {
				calls[i].Next = ts
			}
			reading = reading[:0]
			if !bytes.Contains(line, []byte("agentlog")) || !bytes.Contains(line, []byte(`"tool_use"`)) {
				return
			}
			var r struct {
				Message struct {
					Content []struct {
						Type  string `json:"type"`
						ID    string `json:"id"`
						Input struct {
							Command string `json:"command"`
						} `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &r) != nil {
				return
			}
			for _, c := range r.Message.Content {
				if c.Type != "tool_use" || c.Input.Command == "" {
					continue
				}
				if kind, n := Kind(c.Input.Command); n > 0 {
					pending[c.ID] = len(calls)
					calls = append(calls, Call{ToolID: c.ID, Harness: "claude", Session: s.ID, Agent: s.Agent, CWD: s.CWD, File: path,
						Command: c.Input.Command, Kind: kind, Count: n, Pure: Pure(c.Input.Command), Start: ts})
				}
			}
		case bytes.Contains(line, []byte(`"tool_result"`)):
			s.ToolBytes += len(line)
			hit := false
			for id := range pending {
				if bytes.Contains(line, []byte(id)) {
					hit = true
				}
			}
			if !hit {
				return
			}
			var r struct {
				Message struct {
					Content []struct {
						Type      string          `json:"type"`
						ToolUseID string          `json:"tool_use_id"`
						Content   json.RawMessage `json:"content"`
						IsError   bool            `json:"is_error"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &r) != nil {
				return
			}
			ts := stamp(line)
			for _, c := range r.Message.Content {
				i, ok := pending[c.ToolUseID]
				if c.Type != "tool_result" || !ok {
					continue
				}
				delete(pending, c.ToolUseID)
				calls[i].End, calls[i].Out, calls[i].Error = ts, len(text(c.Content)), c.IsError
				reading = append(reading, i)
			}
		}
	})
	return finished(calls), s
}

// ─── Codex ───────────────────────────────────────────────────────

var wallTime = regexp.MustCompile(`Wall time:? ([0-9.]+) seconds`)

func readCodex(path string) ([]Call, Session) {
	s := Session{Harness: "codex", File: path}
	var (
		calls   []Call
		pending = map[string]int{}
		reading []int
	)
	eachLine(path, func(line []byte) {
		var r struct {
			Type    string `json:"type"`
			Payload struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				CWD       string          `json:"cwd"`
				Role      string          `json:"role"`
				CallID    string          `json:"call_id"`
				Arguments string          `json:"arguments"`
				Input     string          `json:"input"`
				Action    json.RawMessage `json:"action"`
				Output    json.RawMessage `json:"output"`
			} `json:"payload"`
		}
		isOutput := bytes.Contains(line, []byte(`_call_output"`))
		if isOutput {
			s.ToolBytes += len(line)
		}
		wanted := bytes.Contains(line, []byte(`"session_meta"`)) || isOutput && len(pending) > 0 ||
			bytes.Contains(line, []byte(`"response_item"`)) && (len(reading) > 0 || bytes.Contains(line, []byte("agentlog")))
		if !wanted || json.Unmarshal(line, &r) != nil {
			return
		}
		ts := stamp(line)
		p := r.Payload
		switch {
		case r.Type == "session_meta":
			s.ID, s.CWD = p.ID, p.CWD
		case strings.HasSuffix(p.Type, "_call_output"):
			i, ok := pending[p.CallID]
			if !ok {
				return
			}
			delete(pending, p.CallID)
			out := text(p.Output)
			calls[i].End, calls[i].Out = ts, len(out)
			if m := wallTime.FindStringSubmatch(out); m != nil {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					calls[i].ShellMs = v * 1000
				}
			}
			reading = append(reading, i)
		case r.Type == "response_item" && p.Type != "message" || p.Role == "assistant":
			for _, i := range reading {
				calls[i].Next = ts
			}
			reading = reading[:0]
			command := p.Input + p.Arguments + string(p.Action)
			if p.CallID == "" || !strings.Contains(command, "agentlog") {
				return
			}
			if p.Input == "" {
				command = codexCommand(p.Arguments, p.Action)
			}
			if kind, n := Kind(command); n > 0 {
				pending[p.CallID] = len(calls)
				calls = append(calls, Call{ToolID: p.CallID, Harness: "codex", Session: s.ID, CWD: s.CWD, File: path,
					Command: command, Kind: kind, Count: n, Pure: Pure(command), Start: ts})
			}
		}
	})
	return finished(calls), s
}

// codexCommand is the shell command of a Codex function or local shell
// call: {"command": "…" | ["bash", "-lc", "…"]} or {"cmd": "…"}.
func codexCommand(arguments string, action json.RawMessage) string {
	var a struct {
		Command json.RawMessage `json:"command"`
		Cmd     string          `json:"cmd"`
	}
	if json.Unmarshal([]byte(arguments), &a) != nil {
		_ = json.Unmarshal(action, &a)
	}
	if a.Cmd != "" {
		return a.Cmd
	}
	var one string
	if json.Unmarshal(a.Command, &one) == nil {
		return one
	}
	var argv []string
	if json.Unmarshal(a.Command, &argv) == nil && len(argv) > 0 {
		// bash -lc "script": the script is the command.
		if len(argv) >= 3 && (argv[1] == "-lc" || argv[1] == "-c") {
			return argv[len(argv)-1]
		}
		return strings.Join(argv, " ")
	}
	return arguments + string(action)
}

// unescape turns the JSON-in-JSON of a Codex call's arguments into the
// command text, so a quote inside it reads as a quote.
func unescape(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\n`, "\n", `\\`, `\`).Replace(s)
}

// ─── shared ──────────────────────────────────────────────────────

// finished drops calls whose result never came (the session was cut off).
func finished(cs []Call) []Call {
	out := cs[:0]
	for _, c := range cs {
		if !c.End.IsZero() {
			out = append(out, c)
		}
	}
	return out
}

func eachLine(path string, fn func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			fn(line)
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

// field is the first "name":"value" in a line, unparsed.
func field(line []byte, name string) string {
	key := []byte(`"` + name + `":"`)
	i := bytes.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

func stamp(line []byte) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, field(line, "timestamp"))
	return t
}

// text is a tool result's content: a string, or a list of text parts.
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Output
	}
	return string(raw)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
