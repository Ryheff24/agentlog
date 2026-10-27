package render

import (
	"fmt"
	"io"
	"strings"

	"agentlog/internal/catalog"
	"agentlog/internal/clip"
	"agentlog/internal/transcript"
)

// projectSessions is how many of a project's sessions are shown in full.
const projectSessions = 3

// sessionState is how a session stands, with its subagents: the ones still
// running are the ones doing the work.
func sessionState(s *transcript.Session) string {
	out := ""
	if s.Live {
		out = "  RUNNING"
	} else if s.Ending != "" {
		out = "  ended:" + s.Ending
	}
	if n := visibleChildren(s); n > 0 {
		running := 0
		for _, c := range s.Children {
			if c.Live {
				running++
			}
		}
		out += "  +" + plural(n, "subagent")
		if running > 0 {
			out += fmt.Sprintf(" (%d running)", running)
		}
	}
	return out
}

// Projects prints one entry per repository: when it was last worked on,
// what ran there, and where its latest session left off.
func Projects(w io.Writer, res *catalog.Result, ps []catalog.Project, o Options) {
	fmt.Fprintf(w, "projects (%s; newest work first):\n", window(o.Now, res))
	for _, p := range ps {
		counts := plural(len(p.Sessions), "session")
		if n := p.Subagents(); n > 0 {
			counts += ", " + plural(n, "subagent")
		}
		fmt.Fprintf(w, "\n%s  last work %s  %s\n", tilde(p.Dir), stamp(o.Now, p.Latest()), counts)
		if len(p.Dirs) > 0 {
			dirs := make([]string, len(p.Dirs))
			for i, d := range p.Dirs {
				dirs[i] = strings.TrimPrefix(d, p.Dir+"/")
			}
			fmt.Fprintf(w, "  also in %s\n", clip.Line(strings.Join(dirs, ", "), o.width()-10))
		}
		// Work on a project runs in parallel sessions, so the latest one
		// alone is not where it stands.
		shown := p.Sessions[:min(len(p.Sessions), projectSessions)]
		for _, s := range shown {
			fmt.Fprintf(w, "  %s  %-7s %s  %s%s\n", Ref(s), s.Provider, stamp(o.Now, catalog.Worked(s)), clip.Line(sessionLabel(s), o.width()-50), sessionState(s))
			if s.LastPrompt != "" {
				fmt.Fprintf(w, "      asked     %s\n", clip.Line(s.LastPrompt, o.width()-16))
			}
			if s.LastSay != "" {
				fmt.Fprintf(w, "      left off  %s\n", clip.Line(s.LastSay, o.width()-16))
			}
		}
		if rest := p.Sessions[len(shown):]; len(rest) > 0 {
			refs := make([]string, 0, len(rest))
			for _, x := range rest {
				refs = append(refs, Ref(x))
			}
			fmt.Fprintf(w, "  earlier   %s\n", clip.Line(strings.Join(refs, " "), o.width()-12))
		}
	}
	if len(ps) == 0 {
		fmt.Fprintln(w, "  none")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "next: agentlog <id> for a session's preview, with its subagents' latest messages")
	Notes(w, res, "")
}
