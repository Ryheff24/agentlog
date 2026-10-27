package render

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Spend prints what sessions cost: tokens, money, and how close the account
// was to its limits when the harness last looked. A rate limit is a reading
// taken at a moment, so when it was read is printed beside it — an agent that
// quotes a four-day-old percentage as the current one is worse than useless.
func Spend(w io.Writer, ts []*transcript.Transcript, o Options) {
	for i, t := range ts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s %s %s\n", t.Session.Provider, Ref(&t.Session), sessionName(&t.Session))
		spend(w, t.Usage, o)
	}
}

func spend(w io.Writer, u transcript.Usage, o Options) {
	if !u.Known() {
		fmt.Fprintln(w, "no tokens recorded in this log")
		return
	}
	parts := []string{count(u.Total) + " total"}
	for _, p := range []struct {
		n int64
		s string
	}{{u.Input, "fresh input"}, {u.CacheRead, "cached input"}, {u.CacheWrite, "cache writes"}, {u.Output, "output"}} {
		if p.n > 0 {
			parts = append(parts, count(p.n)+" "+p.s)
		}
	}
	if u.Reasoning > 0 {
		parts = append(parts, count(u.Reasoning)+" reasoning (part of output)")
	}
	fmt.Fprintf(w, "tokens    %s\n", strings.Join(parts, " · "))
	if u.CostUSD > 0 {
		fmt.Fprintf(w, "cost      $%.2f\n", u.CostUSD)
	}
	if u.Requests > 0 || u.ContextWindow > 0 {
		line := []string{}
		if u.Requests > 0 {
			line = append(line, plural(u.Requests, "model response"))
		}
		if u.ContextWindow > 0 {
			line = append(line, count(u.ContextWindow)+" token context window")
		}
		fmt.Fprintf(w, "calls     %s\n", strings.Join(line, " · "))
	}
	if len(u.ByModel) > 0 {
		names := make([]string, 0, len(u.ByModel))
		for m := range u.ByModel {
			names = append(names, m)
		}
		sort.Slice(names, func(i, j int) bool {
			if a, b := u.ByModel[names[i]], u.ByModel[names[j]]; a != b {
				return a > b
			}
			return names[i] < names[j]
		})
		for i, m := range names {
			fmt.Fprintf(w, "%-9s %s %s\n", head(i, "models"), m, count(u.ByModel[m]))
		}
	}
	for i, l := range u.Limits {
		fmt.Fprintf(w, "%-9s %s\n", head(i, "limits"), limitLine(l, o.Now))
	}
	if u.Source != "" {
		fmt.Fprintf(w, "source    %s written into the log\n", u.Source)
	}
}

// limitLine states a rate-limit reading, when it was taken and when the window
// turns over, since all three are needed to know whether it still holds.
func limitLine(l transcript.Limit, now time.Time) string {
	s := fmt.Sprintf("%-10s %.0f%% used", l.Name+" "+parens(windowName(l.Window)), l.Percent)
	if !l.Resets.IsZero() {
		if l.Resets.After(now) {
			s += fmt.Sprintf(", resets %s (in %s)", l.Resets.Local().Format("Mon Jan 2 15:04"), short(l.Resets.Sub(now)))
		} else {
			s += fmt.Sprintf(", window reset %s ago", short(now.Sub(l.Resets)))
		}
	}
	if !l.Taken.IsZero() {
		s += fmt.Sprintf(" — read %s", ago(now, l.Taken))
	}
	return s
}

// spendLine is the one-line form for the preview: what it cost, and the limit
// nearest its ceiling, which is the part a reader acts on.
func spendLine(u transcript.Usage, ref string, now time.Time) string {
	if !u.Known() {
		return ""
	}
	parts := []string{count(u.Total) + " tokens"}
	if u.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", u.CostUSD))
	}
	worst := transcript.Limit{}
	for _, l := range u.Limits {
		if l.Percent > worst.Percent {
			worst = l
		}
	}
	if worst.Name != "" {
		p := fmt.Sprintf("%s limit %.0f%% used at the time", worst.Name+" "+parens(windowName(worst.Window)), worst.Percent)
		// A reading from a window that has since turned over says nothing
		// about the limit now.
		if !worst.Resets.IsZero() && !worst.Resets.After(now) {
			p += ", reset since"
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("%s  (agentlog %s --usage)", strings.Join(parts, " · "), ref)
}

// sessionName heads a per-session section: its title, or for a subagent
// its name, or else its directory.
func sessionName(s *transcript.Session) string {
	if s.Title != "" {
		return `"` + s.Title + `"`
	}
	if name := agentLabel(s); name != "" {
		return name
	}
	return tilde(s.CWD)
}

// short is a duration a person reads at a glance: the two largest units.
func short(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}

// windowName names a rate-limit window the way the harness's own UI does.
func windowName(d time.Duration) string {
	switch {
	case d == 0:
		return ""
	case d%(24*time.Hour) == 0 && d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return short(d)
	}
}

func parens(s string) string {
	if s == "" {
		return ""
	}
	return "(" + s + ")"
}

// head labels the first line of a block and indents the rest under it.
func head(i int, name string) string {
	if i == 0 {
		return name
	}
	return ""
}

// count groups a number in threes. Token counts run to nine digits, and an
// unreadable number is a number that gets misread.
func count(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
