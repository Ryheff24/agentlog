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
const Usage = `usage: agentlog [clues...] [flags]   (--help prints this same screen)
clues (any order, all optional):
  claude | codex | copilot           harness
  . | here | ~/path | project        dir (exact, subdir or parent) or project name
  3f9c2e1a | codex://threads/ID      session id prefix or link: opens that session
  agent-ID                           a subagent, by the id its ref prints
  2h | 3d | today | yesterday | all  recency (default 7 days; widening is announced)
  2026-09-09 | sep9 | 9/9            one day: sessions running on it
  src/App.tsx | /abs/path.go         a file: sessions that changed or mentioned it
  path/to/log.jsonl                  read that log file
  <ref>#N  <ref>#N-M  <ref>#N-       one event, a range, or to the end, in full
  any other word                     matches project, title, prompts, subagent names
  doctor                             what this build can't read in each harness's logs
which sessions:
  --list, -l  --limit N, -n N  every match, not just the best (20 rows; home 12)
  --projects                   per repository: last work, its sessions, where each stopped
  --tree                       the match's parent and subagent sessions
what happened (default: a preview of the best match):
  --head N  --tail N   first or last N events
  --kind K,K           user assistant reasoning tool edit agent plan error notice record
                       owner (what the person typed)  refusal (with category and reason)
  --user               same as --kind owner
  --grep RE            regex over events; across all matches when no id is given
  --content            search word clues in what was said, not session metadata
  --count              only how many events and sessions match
  --subagents          add the subagents' own events
  --branches           add turns that were rewound or edited away
  --reasoning          add reasoning, left out by default
  --include-handbacks  let --grep match earlier agentlog output
what it changed, used and cost:
  --edits, --diff  the diffs it made
  --files          changed files and who last wrote each
  --path RE        only paths matching RE (with --edits or --files)
  --tools          every tool called: count, failures, output size
  --usage          tokens, cost, models, rate limits at the time
  --images         save inline images to private temp files; list paths and URLs
output:
  --brief              one line per event, at most 160 characters
  --json               typed JSON, never the raw log
  --max BYTES  --full  clip each expanded block at BYTES (default 6000), or never
  --width N            clip one-line summaries at N characters (default 150)
  --flag=VALUE         for a value starting with -, e.g. --grep=--help
  --                   every argument after it is a clue
how much of the machine it takes:
  --jobs N | AGENTLOG_JOBS=N  workers for this run (default half the cores, at most 4, low priority)
                              agents: do NOT raise this unless the person asks for more speed
the tool itself:
  agentlog feedback add MESSAGE    leave a note about using agentlog
  agentlog feedback list [--json]  read the notes
  --version  --help, -h`

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
