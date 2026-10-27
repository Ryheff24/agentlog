package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/clip"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Usage is the one-screen syntax reminder printed on the home screen and by
// --help. Keep it short: agents read it on every bare call.
const Usage = `usage: agentlog [clues...] [view flags]
clues (any order, all optional, combine freely):
  codex | claude | copilot        harness
  . | ~/path | project-name       working dir or project (exact dir, subdir, or name substring)
  01a0a5c3 | codex://threads/ID   session id prefix or link → opens that session directly
  2h | 3d | today | all           recency (default: last 7 days; widening is announced)
  2026-09-09 | sep9 | 9/9         one day: sessions that were running on it
  any other word                  matched against project, title, prompts, subagent names
  <ref>#N  <ref>#N-M  <ref>#N-    one event / a range / from there to the end
  src/App.tsx | /abs/path.go      a file: which sessions changed or mentioned it
  doctor                          how much of each harness's logs this build can still read
views on the best match (default: preview):
  --tail N  --head N  --kind user,assistant,tool,edit,agent,plan,error,notice,reasoning
	--user | --kind owner           human turns only; composes with ranges, grep, head/tail and JSON
	--kind refusal                  refusal errors with category and explanation
	--content                       search word clues in event content instead of session metadata
	--count                         matching event and session counts only
	--brief                         one line per event, at most 160 characters
	--images                        save inline images to temp files, or list local paths/URLs
	--tree                          sourced parent and descendant sessions
	--include-handbacks             include historical agentlog output and agent messages in grep
  --grep REGEX   --edits [--files]   --usage   --tools   --subagents   --json   --full   --max BYTES
  --list [--limit N]              every match, not only the best (default 20 rows)
  --projects                      one entry per repository: last work, its sessions, where each left off
  --branches                      also show turns that were rewound or edited away
output size:
  --max BYTES                     clip each expanded message/output/diff (default 6000)
  --full                          do not clip individual expanded blocks
how much of the machine it takes:
  --jobs N | AGENTLOG_JOBS        workers, shared by everything this run does
                                  (default: half the cores, at most 4, at low priority)
                                  agents: do NOT raise this unless the person asks for more speed`

// Home prints harnesses and recent sessions for a bare invocation.
func Home(w io.Writer, res *catalog.Result, o Options, limit int) {
	fmt.Fprintln(w, "harnesses:")
	for _, h := range res.Harnesses {
		status := "not found"
		if h.Present {
			status = fmt.Sprintf("%s in %s", plural(h.Sessions, "session"), window(o.Now, res))
			if !h.Latest.IsZero() {
				status += ", latest " + ago(o.Now, h.Latest)
			}
		}
		roots := make([]string, len(h.Roots))
		for i, r := range h.Roots {
			roots[i] = tilde(r)
		}
		fmt.Fprintf(w, "  %-8s %-45s %s\n", h.Name, strings.Join(roots, ", "), status)
	}
	fmt.Fprintln(w)
	if only := OnlyOwn(res, o.Now); only != "" {
		fmt.Fprintln(w, only)
	} else if len(res.Matches) == 0 {
		fmt.Fprintln(w, "no sessions in "+window(o.Now, res))
	} else {
		fmt.Fprintf(w, "recent sessions (running across projects first, then this dir and newest):\n")
		List(w, res, o, limit)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, Usage)
}

// List prints ranked matches, two lines each.
func List(w io.Writer, res *catalog.Result, o Options, limit int) {
	shown := res.Matches
	if limit > 0 && len(shown) > limit {
		shown = shown[:limit]
	}
	for _, m := range shown {
		s := m.Session
		when := stamp(o.Now, s.Updated)
		if q := quietSince(s); !q.IsZero() {
			when = stamp(o.Now, q) + " (log touched " + when + ")"
		}
		fmt.Fprintf(w, "  %s  %-7s %s  %s  %s", Ref(s), s.Provider, when, clip.Size(int(s.Size)), tilde(s.CWD))
		if s.ParentID != "" {
			fmt.Fprintf(w, "  subagent of %s", ShortID(s.ParentID))
		}
		if n := len(s.Children); n > 0 {
			fmt.Fprintf(w, "  +%s", plural(visibleChildren(s), "subagent"))
		}
		if s.Live {
			fmt.Fprint(w, "  RUNNING")
		} else if s.Ending != "" {
			fmt.Fprint(w, "  ended:"+s.Ending)
		}
		fmt.Fprint(w, Own(s))
		if len(m.Why) > 0 && (len(res.Clues.Words) > 0 || len(res.Clues.Dirs) > 0) {
			fmt.Fprintf(w, "  [%s]", strings.Join(m.Why, ","))
		}
		fmt.Fprintln(w)
		desc := sessionLabel(s)
		if s.LastPrompt != "" && s.LastPrompt != desc {
			desc += " · last prompt: " + s.LastPrompt
		}
		fmt.Fprintf(w, "            %s\n", clip.Line(desc, o.width()))
	}
	if extra := len(res.Matches) - len(shown); extra > 0 {
		fmt.Fprintf(w, "  … %d more (add clues, or --list --limit N)\n", extra)
	}
	Notes(w, res, "  ")
}

// window names the span discovery actually covered, so a count is never
// labelled with a window that was not the one searched.
func window(now time.Time, res *catalog.Result) string {
	switch {
	case !res.Clues.Until.IsZero():
		return res.Clues.Since.Local().Format("2006-01-02")
	case res.Since.IsZero():
		return "all time"
	}
	if d := now.Sub(res.Since); d < 48*time.Hour {
		return fmt.Sprintf("last %dh", int(d.Hours()+0.5))
	}
	return fmt.Sprintf("last %dd", int(now.Sub(res.Since).Hours()/24+0.5))
}

// Notes states what the lookup did that the clues did not ask for. A widened
// window changes which sessions were even considered, so it is never silent.
func Notes(w io.Writer, res *catalog.Result, prefix string) {
	if cl := res.Clues; !cl.Until.IsZero() {
		fmt.Fprintf(w, "%s(sessions that were running on %s — a session is listed under its last write, which can be a later day)\n",
			prefix, cl.Since.Local().Format("2006-01-02"))
	}
	if res.Widened {
		fmt.Fprintf(w, "%s(nothing matched in the last 7 days, so ALL TIME was searched instead — pass a time clue such as 'all', '30d' or '2026-09-09' to say which window you meant)\n", prefix)
	}
	if res.HiddenSelf {
		fmt.Fprintf(w, "%s(your own session is hidden; pass its id to include it)\n", prefix)
	}
	if n := res.PromptsUnread; n > 0 {
		fmt.Fprintf(w, "%s(words were matched against the prompts of the newest sessions only; %d older ones by title, project and id alone — a time or dir clue narrows the search)\n", prefix, n)
	}
	if len(res.Blank) > 0 {
		fmt.Fprintf(w, "%s(%s)\n", prefix, BlankNote(res.Blank))
	}
}

// BlankNote names the sessions left out of a listing for having nothing in
// them, so none of them is missing without a word.
func BlankNote(ss []*transcript.Session) string {
	refs := make([]string, 0, len(ss))
	for _, s := range ss {
		refs = append(refs, Ref(s))
	}
	return fmt.Sprintf("left out %s with no prompt in them (opened and cleared, or a shell command alone): %s",
		plural(len(ss), "session"), strings.Join(refs, " "))
}

// OnlyOwn says so when the caller's own session is all there was, which an
// empty result would otherwise pass off as a machine with no sessions.
func OnlyOwn(res *catalog.Result, now time.Time) string {
	if len(res.Matches) > 0 || res.Own == nil {
		return ""
	}
	return fmt.Sprintf("no sessions but your own (agentlog %s opens it) in %s", Ref(res.Own), window(now, res))
}

// Own labels the caller's own session, so an agent shown its own work does
// not take it for another agent's.
func Own(s *transcript.Session) string {
	if s.Self {
		return "  (your own session)"
	}
	return ""
}

func sessionLabel(s *transcript.Session) string {
	parts := []string{}
	if name := agentLabel(s); name != "" {
		parts = append(parts, name)
	}
	if s.Title != "" {
		parts = append(parts, `"`+s.Title+`"`)
	}
	if s.FirstPrompt != "" && s.Title == "" {
		parts = append(parts, "first prompt: "+s.FirstPrompt)
	}
	if len(parts) == 0 {
		return "(no title)"
	}
	return strings.Join(parts, " · ")
}

func visibleChildren(s *transcript.Session) int {
	n := 0
	for _, c := range s.Children {
		if c.AgentRole != "guardian" {
			n++
		}
	}
	return n
}

// agentLabel names a subagent by nickname and role without repeating itself.
func agentLabel(s *transcript.Session) string {
	if s.AgentRole == "" || s.AgentRole == s.AgentName {
		return s.AgentName
	}
	return strings.TrimSpace(s.AgentName + " " + s.AgentRole)
}
