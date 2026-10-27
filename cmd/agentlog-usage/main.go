// Command agentlog-usage reports what agentlog costs the agents that call
// it: how long each call kept them waiting, how much of that was agentlog
// and how much the shell, sandbox and approvals around it, and how much it
// put into their context. It reads the harnesses' session logs itself, not
// through agentlog, so it keeps working when agentlog's readers do not.
//
//	agentlog-usage [7d | 24h | all] [--top N] [--json]
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"agentlog/internal/monitor"
	"agentlog/internal/usage"
)

func main() {
	since := time.Now().Add(-7 * 24 * time.Hour)
	top, asJSON := 10, false
	callers := true
	window := regexp.MustCompile(`^(\d+)([hdw])$`)
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "all":
			since = time.Time{}
		case a == "--json":
			asJSON = true
		case a == "--all-callers":
			callers = false
		case a == "--callers":
			callers = true
		case a == "--top" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				fail("--top wants a number, got %q", args[i])
			}
			top = n
		case window.MatchString(a):
			m := window.FindStringSubmatch(a)
			n, _ := strconv.Atoi(m[1])
			unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
			since = time.Now().Add(-time.Duration(n) * unit)
		case a == "-h" || a == "--help":
			fmt.Println("usage: agentlog-usage [7d | 24h | all] [--top N] [--json] [--callers | --all-callers] (default: non-development callers)")
			return
		default:
			fail("unknown argument %q (usage: agentlog-usage [7d | 24h | all] [--top N] [--json])", a)
		}
	}

	claude, codex := usage.Roots()
	calls, sessions := usage.Scan(claude, codex, since)
	if callers {
		calls, sessions = usage.Callers(calls, sessions)
	}
	runs := usage.LoadRuns(monitor.Paths())
	usage.Join(calls, runs)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(struct {
			Since    time.Time       `json:"since"`
			Calls    []usage.Call    `json:"calls"`
			Sessions []usage.Session `json:"sessions"`
		}{since, calls, sessions})
		return
	}
	usage.Report(os.Stdout, calls, sessions, runs, since, top)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "agentlog-usage: "+format+"\n", a...)
	os.Exit(2)
}
