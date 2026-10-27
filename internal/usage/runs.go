package usage

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"agentlog/internal/monitor"
)

// Run is agentlog's record of one of its own runs.
type Run = monitor.Run

// LoadRuns reads every runs file agentlog may have written, and the rotated
// copies beside them, oldest first.
func LoadRuns(paths []string) []Run {
	var runs []Run
	seen := map[string]bool{}
	for _, p := range paths {
		for _, f := range []string{p + ".1", p} {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, line := range splitLines(b) {
				var r Run
				if json.Unmarshal(line, &r) != nil {
					continue
				}
				// Two paths can be one file (a symlinked temp dir).
				key := r.Start.Format(time.RFC3339Nano) + r.Session
				if !seen[key] {
					seen[key] = true
					runs = append(runs, r)
				}
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Start.Before(runs[j].Start) })
	return runs
}

// Join gives each call the runs agentlog recorded inside it: the same
// calling session, started while the call was open.
func Join(calls []Call, runs []Run) {
	bySession := map[string][]Run{}
	for _, r := range runs {
		if r.Session != "" {
			bySession[r.Session] = append(bySession[r.Session], r)
		}
	}
	const slack = time.Second // the two clocks are one clock, but logs round
	for i := range calls {
		c := &calls[i]
		for _, r := range bySession[c.Session] {
			if !r.Start.Before(c.Start.Add(-slack)) && !r.Start.After(c.End.Add(slack)) {
				c.Self = append(c.Self, r)
			}
		}
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := 0
		for i < len(b) && b[i] != '\n' {
			i++
		}
		if i > 0 {
			out = append(out, b[:i])
		}
		if i == len(b) {
			break
		}
		b = b[i+1:]
	}
	return out
}
