package transcript

import (
	"agentlog/internal/clip"
	"encoding/json"
	"regexp"
	"strings"
)

// PromptHints reads provenance flags without interpreting arbitrary nested
// tool content as an origin. Older records have no origin; wrappers and
// session linkage still classify those records.
func PromptHints(e *Event, raw []byte) {
	if e.Kind != KindUser {
		return
	}
	var r map[string]json.RawMessage
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	for _, key := range []string{"payload", "data", "attachment"} {
		if v := r[key]; len(v) > 0 {
			var child map[string]json.RawMessage
			if json.Unmarshal(v, &child) == nil {
				for k, v := range child {
					r[k] = v
				}
			}
		}
	}
	var origin string
	if v := r["origin"]; len(v) > 0 {
		if json.Unmarshal(v, &origin) != nil {
			var o struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal(v, &o)
			origin = o.Kind
		}
	}
	if origin != "" && origin != "human" && origin != "user" {
		e.Human = false
	}
	for _, k := range []string{"isMeta", "isCompactSummary", "isSidechain"} {
		var yes bool
		_ = json.Unmarshal(r[k], &yes)
		if yes {
			e.Human = false
		}
	}
	for _, k := range []string{"pasted", "isPaste"} {
		var yes bool
		_ = json.Unmarshal(r[k], &yes)
		e.Pasted = e.Pasted || yes
	}
	var mode string
	_ = json.Unmarshal(r["commandMode"], &mode)
	e.Pasted = e.Pasted || mode == "paste" || strings.Contains(e.Text, "[Pasted text") || strings.Contains(e.Text, "[image]") || strings.Contains(e.Text, "[document]")
}

// ClassifyPrompt separates leading harness wrappers from human words.
func ClassifyPrompt(e *Event, s *Session) {
	if e.Kind != KindUser {
		return
	}
	text := strings.TrimSpace(e.Text)
	for _, tag := range []string{"<cross-session-message", "<teammate-message", "<task-notification>"} {
		if i := strings.Index(text, tag); i > 0 && i < 200 && text[i-1] == '\n' && !strings.Contains(text[:i-1], "\n") {
			text = text[i:]
		}
	}
	switch {
	case strings.HasPrefix(text, "<cross-session-message"):
		e.Kind, e.Tag, e.Agent, e.Text = KindAgent, "message", clip.Attr(text, "cross-session-message", "from-name"), clip.Tag(text, "cross-session-message")
	case strings.HasPrefix(text, "<teammate-message"):
		e.Kind, e.Tag, e.Agent, e.Text = KindAgent, "message", clip.Attr(text, "teammate-message", "teammate_id"), clip.Tag(text, "teammate-message")
	case strings.HasPrefix(text, "<task-notification>"):
		e.Kind, e.Tag = KindNotice, TagNotification
	case strings.HasPrefix(text, "<command-name>"), strings.HasPrefix(text, "<command-message>"), strings.HasPrefix(text, "<local-command-stdout>"):
		e.Kind, e.Tag = KindNotice, TagCommand
	case strings.HasPrefix(text, "This session is being continued from a previous conversation"), strings.HasPrefix(text, "<compaction-summary>"):
		e.Kind, e.Tag = KindNotice, TagCompaction
	case strings.HasPrefix(text, "[Request interrupted by user"):
		e.Kind, e.Tag = KindNotice, TagInterrupted
	default:
		e.Human = s.ParentID == "" && s.AgentRole == "" && e.Tag != TagSubagent
	}
}

// MarkHandbacks attributes historical output to its containing call.
func (t *Transcript) MarkHandbacks() {
	for i := range t.Events {
		e := &t.Events[i]
		if standaloneAgentlog(e.Input) {
			e.Handback = true
		}
	}
}

var commandString = regexp.MustCompile(`"?cmd"?\s*:\s*("(?:[^"\\]|\\.)*")`)

func standaloneAgentlog(input string) bool {
	commands := commandString.FindAllStringSubmatch(input, -1)
	if len(commands) > 0 {
		for _, m := range commands {
			var command string
			if json.Unmarshal([]byte(m[1]), &command) != nil || !standaloneAgentlog(command) {
				return false
			}
		}
		return true
	}
	words := strings.Fields(input)
	return len(words) > 0 && (words[0] == "agentlog" || strings.HasSuffix(words[0], "/agentlog")) && !strings.ContainsAny(input, ";&\n")
}
