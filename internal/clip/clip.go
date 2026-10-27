// Package clip shortens and cleans text so transcripts fit in an agent's
// context: single-line previews, head+tail blocks, and removal of harness
// boilerplate that agents inject into prompts.
package clip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Line collapses whitespace to single spaces and truncates to width runes,
// noting how much was cut. width <= 0 disables truncation.
func Line(s string, width int) string {
	s = strings.Join(strings.Fields(newlines.ReplaceAllString(strings.TrimSpace(s), " ⏎ ")), " ")
	if width <= 0 || utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width]) + fmt.Sprintf("…(+%s)", Size(len(string(r[width:]))))
}

// Block keeps the head and tail of s within max bytes, marking the gap. Tails
// matter for command output (errors are usually last), so ~40% is kept there.
// max <= 0 disables truncation.
func Block(s string, max int) string {
	s = strings.TrimRight(s, "\n ")
	if max <= 0 || len(s) <= max {
		return s
	}
	headN := max * 6 / 10
	tailN := max - headN
	head := cutRunes(s[:headN])
	tail := s[len(s)-tailN:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	// Prefer cutting at line boundaries when one is near.
	if i := strings.LastIndexByte(head, '\n'); i > headN/2 {
		head = head[:i]
	}
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < tailN/2 {
		tail = tail[i+1:]
	}
	omitted := len(s) - len(head) - len(tail)
	return head + fmt.Sprintf("\n  … [%s omitted] …\n", Size(omitted)) + tail
}

// Head keeps the first max bytes of s.
func Head(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	head := cutRunes(s[:max])
	return head + fmt.Sprintf(" …(+%s)", Size(len(s)-len(head)))
}

// cutRunes drops a character the cut left incomplete at the end of s. Only
// the end: a stray invalid byte earlier on is the log's, not the cut's, and
// is no reason to throw away the text after it.
func cutRunes(s string) string {
	for j := len(s) - 1; j >= 0 && j >= len(s)-utf8.UTFMax; j-- {
		if utf8.RuneStart(s[j]) {
			if !utf8.FullRuneInString(s[j:]) {
				return s[:j]
			}
			break
		}
	}
	return s
}

// JSON renders a JSON value compactly for display, shortening long strings
// and marking opaque encrypted blobs. What it shows is what the harness
// wrote: numbers keep their digits (an id past 2^53 is not rounded) and a
// command's && or <file is not HTML-escaped.
func JSON(raw []byte, maxString int) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if _, err := dec.Token(); err != io.EOF {
		return strings.TrimSpace(string(raw)) // more than one value
	}
	v = shorten(v, maxString)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return strings.TrimRight(b.String(), "\n")
}

func shorten(v any, max int) any {
	switch t := v.(type) {
	case string:
		if IsEncrypted(t) {
			return "[encrypted]"
		}
		if max > 0 && len(t) > max {
			return Head(t, max)
		}
		return t
	case map[string]any:
		for k, x := range t {
			t[k] = shorten(x, max)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = shorten(x, max)
		}
		return t
	default:
		return v
	}
}

// IsEncrypted reports whether s looks like an opaque Fernet-style token
// (Codex encrypts reasoning and inter-agent messages).
func IsEncrypted(s string) bool {
	return len(s) > 60 && strings.HasPrefix(s, "gAAAAA") && !strings.ContainsAny(s, " \n")
}

// Size renders a byte count compactly.
func Size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

var (
	injectedBlocks = compileBlocks("system-reminder", "environment_context", "permissions instructions",
		"user_instructions", "app-context", "recommended_plugins", "codex_internal_context",
		"local-command-caveat", "INSTRUCTIONS", "skills_instructions", "collaboration_mode",
		"ide_selection", "ide_opened_file", "in-app-browser-context")
	// Codex Desktop prefixes prompts with attachments, then the typed request.
	mentionedFiles = regexp.MustCompile(`(?s)^\s*# Files mentioned by the user:\s*\n(.*?)## My request[^\n]*:[ \t]*\n`)
	mentionedName  = regexp.MustCompile(`(?m)^## (.+?): /`)
	emptyImageTag  = regexp.MustCompile(`<image name=[^>]*>(\s*</image>)?`)
	// The <INSTRUCTIONS> body is removed with the tag blocks; this drops the
	// header line Codex puts above it, wherever the part landed in the message.
	agentsMD  = regexp.MustCompile(`(?m)^# AGENTS\.md instructions( for .*)?[ \t]*$`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
	newlines  = regexp.MustCompile(`\s*\n\s*`)
)

// compileBlocks builds one matcher per tag; RE2 has no backreferences, so a
// single alternation could pair mismatched open and close tags.
func compileBlocks(names ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(names))
	for i, n := range names {
		q := regexp.QuoteMeta(n)
		out[i] = regexp.MustCompile(`(?s)<` + q + `(\s[^>]*)?>.*?</` + q + `>`)
	}
	return out
}

// StripInjected removes harness-injected blocks (system reminders, environment
// context, AGENTS.md dumps) from a prompt, leaving what a person typed.
func StripInjected(s string) string {
	if strings.IndexByte(s, '<') >= 0 {
		for _, re := range injectedBlocks {
			s = re.ReplaceAllString(s, "")
		}
	}
	s = agentsMD.ReplaceAllString(s, "")
	s = emptyImageTag.ReplaceAllString(s, "")
	if m := mentionedFiles.FindStringSubmatchIndex(s); m != nil {
		var names []string
		for _, n := range mentionedName.FindAllStringSubmatch(s[m[2]:m[3]], -1) {
			names = append(names, n[1])
		}
		s = "[attached: " + strings.Join(names, ", ") + "]\n" + s[m[1]:]
	}
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// Tag returns the inner text of the first <name>…</name> element in s.
func Tag(s, name string) string {
	i := openTag(s, name)
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return ""
	}
	start := i + j + 1
	end := strings.Index(s[start:], "</"+name+">")
	if end < 0 {
		return strings.TrimSpace(s[start:])
	}
	return strings.TrimSpace(s[start : start+end])
}

// Attr returns the value of attr="…" inside the first <name …> tag in s.
func Attr(s, name, attr string) string {
	i := openTag(s, name)
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(s[i:], '>')
	if j < 0 {
		return ""
	}
	tag := s[i : i+j]
	for k := 0; ; {
		n := strings.Index(tag[k:], attr+`="`)
		if n < 0 {
			return ""
		}
		k += n
		// The name must stand on its own: id is not the end of teammate_id.
		if k > 0 && isSpace(tag[k-1]) {
			v := tag[k+len(attr)+2:]
			if e := strings.IndexByte(v, '"'); e >= 0 {
				return v[:e]
			}
			return v
		}
		k += len(attr)
	}
}

// openTag returns where the first <name tag starts in s, or -1. The name
// must end there: <command is not the start of <command-name>.
func openTag(s, name string) int {
	open := "<" + name
	for k := 0; ; {
		n := strings.Index(s[k:], open)
		if n < 0 {
			return -1
		}
		k += n
		if e := k + len(open); e == len(s) || s[e] == '>' || s[e] == '/' || isSpace(s[e]) {
			return k
		}
		k += len(open)
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
