package engine

import (
	"fmt"
	"sort"
	"strings"

	"agentlog/internal/transcript"
)

// Drift says what this build could not read, in one line, or nothing when it
// read everything. Every view ends with it: a reader must never have to guess
// whether what it is looking at is the whole session.
func Drift(ts ...*transcript.Transcript) string {
	all := map[string]int{}
	for _, t := range ts {
		for k, n := range t.Unknowns {
			all[k] += n
		}
	}
	if len(all) == 0 {
		return ""
	}
	n := 0
	for _, c := range all {
		n += c
	}
	return fmt.Sprintf("note: %s of %s this build cannot read, shown as generic events (%s) — run: agentlog doctor",
		plural(n, "record"), plural(len(all), "type"), strings.Join(Unrecognised(all, 4), ", "))
}

// Unrecognised names the most common unreadable types, most frequent first,
// with a count of the rest.
func Unrecognised(types map[string]int, max int) []string {
	names := make([]string, 0, len(types))
	for k := range types {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if a, b := types[names[i]], types[names[j]]; a != b {
			return a > b
		}
		return names[i] < names[j]
	})
	var out []string
	for i, name := range names {
		if i == max {
			out = append(out, fmt.Sprintf("+%d more", len(names)-max))
			break
		}
		out = append(out, fmt.Sprintf("%s ×%d", name, types[name]))
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
