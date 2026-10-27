package render

import (
	"fmt"
	"io"
	"strings"

	"agentlog/internal/clip"
	"agentlog/internal/engine"
	"agentlog/internal/transcript"
)

// Tools prints the tool census: what a session actually did, in one screen,
// without reading a single event.
//
// With subagents each session gets its own table: an event number means
// something only within its own session.
func Tools(w io.Writer, ts []*transcript.Transcript, o Options) {
	for i, t := range ts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		tools(w, t)
	}
}

func tools(w io.Writer, t *transcript.Transcript) {
	census := engine.Census(t)
	fmt.Fprintf(w, "%s %s %s\n", t.Session.Provider, Ref(&t.Session), sessionName(&t.Session))
	if len(census) == 0 {
		fmt.Fprintln(w, "no tool calls in this session")
		return
	}
	calls, failed, bytes := 0, 0, 0
	for _, u := range census {
		calls, failed, bytes = calls+u.Calls, failed+u.Failed, bytes+u.Bytes
	}
	fmt.Fprintf(w, "%s across %s, %d failed, %s of output\n\n",
		plural(calls, "call"), plural(len(census), "tool"), failed, clip.Size(bytes))
	// MCP tool names differ only at the end, so the column is sized to the
	// names and what does not fit loses its front, not its point.
	width := len("tool")
	for _, u := range census {
		width = max(width, len(u.Tool))
	}
	width = min(width, 44)
	fmt.Fprintf(w, "%-*s %7s %7s %9s  %s\n", width, "tool", "calls", "failed", "output", "events")
	for _, u := range census {
		fail := ""
		if u.Failed > 0 {
			fail = fmt.Sprint(u.Failed)
		}
		fmt.Fprintf(w, "%-*s %7d %7s %9s  #%d-%d\n", width, lastChars(u.Tool, width), u.Calls, fail, clip.Size(u.Bytes), u.First, u.Last)
	}
}

// commitLine lists what the session landed, with the event that landed it.
func commitLine(t *transcript.Transcript, ref string) string {
	commits := engine.Commits(t)
	if len(commits) == 0 {
		if p := engine.Pushes(t); len(p) > 0 {
			last := p[len(p)-1]
			return strings.TrimSpace(fmt.Sprintf("none; pushed %s (#%d)", last.Branch, last.Event))
		}
		return ""
	}
	const shown = 4
	branches := map[string]bool{}
	var parts []string
	for i, c := range commits {
		branches[c.Branch] = true
		if i < shown {
			parts = append(parts, fmt.Sprintf("%s (#%d)", c.SHA, c.Event))
		}
	}
	line := strings.Join(parts, " · ")
	if n := len(commits) - shown; n > 0 {
		line += fmt.Sprintf(" · +%s", plural(n, "more"))
	}
	if len(branches) == 1 {
		for b := range branches {
			line += " on " + b
		}
	} else {
		line += fmt.Sprintf(" on %d branches", len(branches))
	}
	line += pushNote(commits, engine.Pushes(t))
	return fmt.Sprintf("%s  (agentlog %s --edits)", line, ref)
}

// pushNote says whether the commits were pushed, as far as the log says.
func pushNote(commits, pushes []engine.Commit) string {
	if len(pushes) == 0 {
		return "; no push recorded"
	}
	last := pushes[len(pushes)-1]
	note := fmt.Sprintf("; pushed (#%d)", last.Event)
	if commits[len(commits)-1].Event > last.Event {
		note += ", with no push recorded after the last commit"
	}
	return note
}

// toolLine is the preview form: the busiest few, and how many were left out.
func toolLine(t *transcript.Transcript, ref string) string {
	census := engine.Census(t)
	if len(census) == 0 {
		return ""
	}
	const shown = 5
	var parts []string
	for i, u := range census {
		if i == shown {
			parts = append(parts, fmt.Sprintf("+%s", plural(len(census)-shown, "more tool")))
			break
		}
		p := fmt.Sprintf("%s ×%d", u.Tool, u.Calls)
		if u.Failed > 0 {
			p += fmt.Sprintf(" (%d failed)", u.Failed)
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("%s  (agentlog %s --tools)", strings.Join(parts, " · "), ref)
}
