package usage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report prints what agentlog cost the agents that used it.
func Report(w io.Writer, calls []Call, sessions []Session, runs []Run, since time.Time, top int) {
	n := 0
	byHarness := map[string]int{}
	matched := 0
	for _, c := range calls {
		n += c.Count
		byHarness[c.Harness]++
		if len(c.Self) > 0 {
			matched++
		}
	}
	fmt.Fprintf(w, "agentlog as agents used it, since %s: %d calls (%d runs) from %d sessions", since.Local().Format("Jan 2 15:04"), len(calls), n, len(sessions))
	var hs []string
	for h, k := range byHarness {
		hs = append(hs, fmt.Sprintf("%s %d", h, k))
	}
	sort.Strings(hs)
	if len(hs) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(hs, ", "))
	}
	fmt.Fprintf(w, "\n%d of them matched to agentlog's own record of the run; the others ran before it kept one, or where it could not write\n", matched)
	all := calls
	calls = nil
	for _, c := range all {
		if c.Pure {
			calls = append(calls, c)
		}
	}
	fmt.Fprintf(w, "measured below: the %d calls that ran only agentlog; %d also ran other commands, whose time and output would be counted as agentlog's\n",
		len(calls), len(all)-len(calls))
	if len(calls) == 0 {
		return
	}

	// Latency, from the outside in.
	var wait, shell, self, outside, read []float64
	for _, c := range calls {
		wait = append(wait, c.WaitMs())
		if c.ShellMs > 0 {
			shell = append(shell, c.ShellMs)
		}
		if len(c.Self) > 0 {
			self = append(self, c.SelfMs())
			outside = append(outside, c.WaitMs()-c.SelfMs())
		}
		if r := c.ReadMs(); r >= 0 {
			read = append(read, r)
		}
	}
	fmt.Fprintf(w, "\nlatency%41s %7s %7s %5s\n", "p50", "p90", "max", "n")
	row := func(label string, v []float64) {
		if len(v) == 0 {
			fmt.Fprintf(w, "  %-44s %7s %7s %7s %5d\n", label, "-", "-", "-", 0)
			return
		}
		fmt.Fprintf(w, "  %-44s %7s %7s %7s %5d\n", label, dur(pct(v, 50)), dur(pct(v, 90)), dur(pct(v, 100)), len(v))
	}
	row("the agent waited (call to result)", wait)
	row("the harness ran the command (Codex)", shell)
	row("agentlog ran (its own record)", self)
	row("outside agentlog (shell, sandbox, approval)", outside)
	row("the model took to its next step", read)

	// Inside agentlog: every recorded run in the window, agents' or not.
	phases := map[string][]float64{}
	var walls []float64
	for _, r := range runs {
		if r.Start.Before(since) {
			continue
		}
		walls = append(walls, r.Ms)
		for p, v := range r.Phases {
			phases[p] = append(phases[p], v)
		}
	}
	if len(walls) > 0 {
		fmt.Fprintf(w, "\ninside agentlog (%d recorded runs; load adds up parallel reads)\n", len(walls))
		row("whole run", walls)
		var names []string
		for p := range phases {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			row(p, phases[p])
		}
	}

	// Context: what the agents read.
	var outs []float64
	total := 0
	for _, c := range calls {
		outs = append(outs, float64(c.Out))
		total += c.Out
	}
	fmt.Fprintf(w, "\ncontext: %s into agents, ≈%s tokens (bytes/4); per call p50 %s, p90 %s, max %s\n",
		size(total), count(total/4), size(int(pct(outs, 50))), size(int(pct(outs, 90))), size(int(pct(outs, 100))))

	// By view.
	kinds := map[string][]Call{}
	for _, c := range calls {
		kinds[c.Kind] = append(kinds[c.Kind], c)
	}
	var kindNames []string
	for k := range kinds {
		kindNames = append(kindNames, k)
	}
	sort.Slice(kindNames, func(i, j int) bool { return sum(kinds[kindNames[i]]) > sum(kinds[kindNames[j]]) })
	fmt.Fprintf(w, "\nby view        calls  wait p50  wait p90   out p50   out p90   out max   ≈tokens\n")
	for _, k := range kindNames {
		cs := kinds[k]
		var wv, ov []float64
		for _, c := range cs {
			wv = append(wv, c.WaitMs())
			ov = append(ov, float64(c.Out))
		}
		fmt.Fprintf(w, "  %-11s %6d %9s %9s %9s %9s %9s %9s\n", k, len(cs), dur(pct(wv, 50)), dur(pct(wv, 90)),
			size(int(pct(ov, 50))), size(int(pct(ov, 90))), size(int(pct(ov, 100))), count(sum(cs)/4))
	}

	// By session: what agentlog cost each caller, against all its tools.
	type agg struct {
		s     Session
		calls []Call
	}
	bySession := map[string]*agg{}
	for _, s := range sessions {
		bySession[s.File] = &agg{s: s}
	}
	for _, c := range calls {
		if a := bySession[c.File]; a != nil {
			a.calls = append(a.calls, c)
		}
	}
	var aggs []*agg
	for _, a := range bySession {
		if len(a.calls) > 0 {
			aggs = append(aggs, a)
		}
	}
	sort.Slice(aggs, func(i, j int) bool { return sum(aggs[i].calls) > sum(aggs[j].calls) })
	fmt.Fprintf(w, "\nby session (most read first; calls that ran only agentlog)  calls   ≈tokens  of its tool output   waited\n")
	for i, a := range aggs {
		if i == top {
			fmt.Fprintf(w, "  … %d more sessions\n", len(aggs)-top)
			break
		}
		bytes, waited := sum(a.calls), 0.0
		for _, c := range a.calls {
			waited += c.WaitMs()
		}
		share := "-"
		if a.s.ToolBytes > 0 {
			share = fmt.Sprintf("%.0f%%", 100*float64(bytes)/float64(a.s.ToolBytes))
		}
		fmt.Fprintf(w, "  %-56s %6d %9s %20s %8s\n", sessionName(a.s), len(a.calls), count(bytes/4), share, dur(waited))
	}

	// The heaviest calls, to see what produced them.
	sorted := append([]Call(nil), calls...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Out > sorted[j].Out })
	fmt.Fprintf(w, "\nheaviest calls\n")
	for i, c := range sorted {
		if i == top {
			break
		}
		fmt.Fprintf(w, "  %8s %7s  %-9s %s  %s\n", size(c.Out), dur(c.WaitMs()), c.Kind, short(c.Session), oneLine(c.Command, 90))
	}
	fmt.Fprintf(w, "\nslowest calls\n")
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].WaitMs() > sorted[j].WaitMs() })
	for i, c := range sorted {
		if i == top {
			break
		}
		self := ""
		if len(c.Self) > 0 {
			self = " (agentlog " + dur(c.SelfMs()) + ")"
		}
		fmt.Fprintf(w, "  %7s%s  %-9s %s  %s\n", dur(c.WaitMs()), self, c.Kind, short(c.Session), oneLine(c.Command, 80))
	}
}

func sessionName(s Session) string {
	name := s.Harness + " " + short(s.ID)
	if s.Agent != "" {
		name += "/" + s.Agent
	}
	if s.CWD != "" {
		name += " " + filepath.Base(s.CWD)
	}
	return oneLine(name, 56)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sum(cs []Call) int {
	t := 0
	for _, c := range cs {
		t += c.Out
	}
	return t
}

// pct is the pth percentile, nearest rank.
func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(p/100*float64(len(s))+0.5) - 1
	return s[max(0, min(i, len(s)-1))]
}

func dur(ms float64) string {
	switch {
	case ms < 0:
		return "-"
	case ms < 1000:
		return fmt.Sprintf("%.0fms", ms)
	case ms < 60000:
		return fmt.Sprintf("%.1fs", ms/1000)
	case ms < 3600000:
		return fmt.Sprintf("%.1fm", ms/60000)
	default:
		return fmt.Sprintf("%.1fh", ms/3600000)
	}
}

func size(b int) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%dB", b)
	case b < 1<<20:
		return fmt.Sprintf("%.1fKB", float64(b)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(b)/(1<<20))
	}
}

func count(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1e6:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n-1]) + "…"
	}
	return s
}

// Roots are the harness log directories, as the harnesses themselves find them.
func Roots() (claude, codex string) {
	home, _ := os.UserHomeDir()
	claude = os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	codex = os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	return filepath.Join(claude, "projects"), filepath.Join(codex, "sessions")
}
