// Package patch turns the different edit representations agents log (Codex
// apply_patch envelopes, Claude old/new string edits and structured hunks,
// unified diffs) into transcript.Change values.
package patch

import (
	"fmt"
	"strings"

	"agentlog/internal/transcript"
)

// ParseEnvelope parses the "*** Begin Patch" format used by Codex and Copilot
// apply_patch. Text outside an envelope (e.g. a surrounding heredoc) is ignored.
func ParseEnvelope(s string) []transcript.Change {
	start := strings.Index(s, "*** Begin Patch")
	if start < 0 {
		return nil
	}
	s = s[start:]
	if end := strings.Index(s, "*** End Patch"); end >= 0 {
		s = s[:end]
	}
	var (
		out []transcript.Change
		cur *transcript.Change
		b   strings.Builder
	)
	flush := func() {
		if cur == nil {
			return
		}
		cur.Diff = strings.TrimRight(b.String(), "\n")
		out = append(out, *cur)
		cur = nil
		b.Reset()
	}
	for _, ln := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(ln, "*** Begin Patch"):
		case strings.HasPrefix(ln, "*** Update File: "):
			flush()
			cur = &transcript.Change{Path: strings.TrimSpace(ln[len("*** Update File: "):]), Op: "update"}
		case strings.HasPrefix(ln, "*** Add File: "):
			flush()
			cur = &transcript.Change{Path: strings.TrimSpace(ln[len("*** Add File: "):]), Op: "add"}
		case strings.HasPrefix(ln, "*** Delete File: "):
			flush()
			cur = &transcript.Change{Path: strings.TrimSpace(ln[len("*** Delete File: "):]), Op: "delete"}
		case strings.HasPrefix(ln, "*** Move to: "):
			if cur != nil {
				cur.MoveTo = strings.TrimSpace(ln[len("*** Move to: "):])
				cur.Op = "move"
			}
		case strings.HasPrefix(ln, "*** End of File"):
		default:
			if cur == nil {
				continue
			}
			b.WriteString(ln)
			b.WriteByte('\n')
			switch {
			case strings.HasPrefix(ln, "+"):
				cur.Added++
			case strings.HasPrefix(ln, "-"):
				cur.Removed++
			}
		}
	}
	flush()
	return out
}

// Count returns added and removed line counts of a unified diff body.
func Count(diff string) (added, removed int) {
	for _, ln := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(ln, "+++"), strings.HasPrefix(ln, "---"):
		case strings.HasPrefix(ln, "+"):
			added++
		case strings.HasPrefix(ln, "-"):
			removed++
		}
	}
	return added, removed
}

// Replace builds a change from an exact string replacement (Claude Edit,
// Copilot edit). The diff is the removed then added text, without context.
func Replace(path, oldText, newText string) transcript.Change {
	var b strings.Builder
	b.WriteString("@@\n")
	c := transcript.Change{Path: path, Op: "update"}
	if oldText != "" {
		for _, ln := range strings.Split(strings.TrimSuffix(oldText, "\n"), "\n") {
			b.WriteString("-" + ln + "\n")
			c.Removed++
		}
	}
	if newText != "" {
		for _, ln := range strings.Split(strings.TrimSuffix(newText, "\n"), "\n") {
			b.WriteString("+" + ln + "\n")
			c.Added++
		}
	}
	c.Diff = strings.TrimRight(b.String(), "\n")
	return c
}

// Write builds a change for a whole-file write.
func Write(path, content string) transcript.Change {
	c := Replace(path, "", content)
	c.Op = "write"
	return c
}

// Hunk is Claude's structuredPatch element.
type Hunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// FromHunks renders structured hunks as a unified diff body.
func FromHunks(path, op string, hunks []Hunk) transcript.Change {
	var b strings.Builder
	c := transcript.Change{Path: path, Op: op}
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, ln := range h.Lines {
			b.WriteString(ln)
			b.WriteByte('\n')
			switch {
			case strings.HasPrefix(ln, "+"):
				c.Added++
			case strings.HasPrefix(ln, "-"):
				c.Removed++
			}
		}
	}
	c.Diff = strings.TrimRight(b.String(), "\n")
	return c
}

// Summary renders changes as "op path (+a -r)" joined by commas.
func Summary(cs []transcript.Change) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s %s (+%d -%d)", c.Op, c.Path, c.Added, c.Removed))
	}
	return strings.Join(parts, ", ")
}
