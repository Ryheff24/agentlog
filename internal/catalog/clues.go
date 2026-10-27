package catalog

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/transcript"
)

// Clues is everything an agent passed as free-form positional arguments.
type Clues struct {
	Providers []string  // claude / codex / copilot
	IDs       []string  // id prefixes (also from codex://threads/<id> links)
	Dirs      []string  // absolute directories ("." and "here" resolve to cwd)
	Doctor    bool      // report what this build can and cannot read
	Files     []string  // explicit .jsonl paths
	Paths     []string  // a file to find the sessions that touched it
	Words     []string  // free text matched against project, title, prompts, agent names
	Event     int       // #N suffix on a ref: jump to one event
	EventTo   int       // #N-M range end; negative means "to the last event"
	Since     time.Time // from 30m / 2h / 3d / 1w / today / yesterday / a date
	Until     time.Time // end of a named day, exclusive; zero means "up to now"
	AllTime   bool      // "all" disables the default recency window

	// ReadPrompts matches words against every candidate's prompts, not only
	// when nothing matched on title, project or id. A list promises every
	// match; a preview only needs the best one.
	ReadPrompts bool
}

// Narrowed reports whether any clue was given.
func (c Clues) Narrowed() bool {
	return len(c.Providers)+len(c.IDs)+len(c.Dirs)+len(c.Files)+len(c.Paths)+len(c.Words) > 0 || !c.Since.IsZero()
}

var (
	providerAliases = map[string]string{
		"claude": transcript.Claude, "claude-code": transcript.Claude, "claudecode": transcript.Claude, "cc": transcript.Claude,
		"codex": transcript.Codex, "openai": transcript.Codex,
		"copilot": transcript.Copilot, "gh-copilot": transcript.Copilot,
	}
	durationRe = regexp.MustCompile(`^(\d+)(m|min|h|hr|d|day|days|w|wk)$`)
	idRe       = regexp.MustCompile(`^[0-9a-f][0-9a-f-]{5,}$`)
	eventRe    = regexp.MustCompile(`^(.*)#(\d+)(-\d*)?$`)
	// Absolute days: 2026-09-09, 2026/9/9, 9/9, 9/9/26, sep9, sep-9, 9 sep.
	isoDayRe   = regexp.MustCompile(`^(\d{4})[-/](\d{1,2})[-/](\d{1,2})$`)
	numDayRe   = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})(?:/(\d{2}|\d{4}))?$`)
	monthDayRe = regexp.MustCompile(`^([a-z]{3,9})[ .-]?(\d{1,2})(?:st|nd|rd|th)?$`)
	dayMonthRe = regexp.MustCompile(`^(\d{1,2})(?:st|nd|rd|th)?[ .-]?([a-z]{3,9})$`)
	months     = []string{"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december"}
	linkRe    = regexp.MustCompile(`^codex://threads/([0-9a-f-]+)`)
	agentIDRe = regexp.MustCompile(`^agent-(.+?)(\.jsonl)?$`)
	// A Claude subagent's id carries its name: a<name>-<16 hex>, the form
	// every ref to one is printed in.
	namedAgentRe = regexp.MustCompile(`^a[a-z0-9][a-z0-9-]*-[0-9a-f]{16}$`)
)

// ParseClues classifies free-form arguments. cwd resolves relative paths.
func ParseClues(args []string, cwd string, now time.Time) Clues {
	var c Clues
	for _, raw := range args {
		a := strings.TrimSpace(raw)
		if a == "" {
			continue
		}
		if m := eventRe.FindStringSubmatch(a); m != nil {
			c.Event, _ = strconv.Atoi(m[2])
			switch {
			case m[3] == "-":
				c.EventTo = -1 // "#40-": from there to the end
			case m[3] != "":
				c.EventTo, _ = strconv.Atoi(m[3][1:])
			}
			if a = m[1]; a == "" {
				continue
			}
		}
		lower := strings.ToLower(a)
		if p, ok := providerAliases[lower]; ok {
			c.Providers = append(c.Providers, p)
			continue
		}
		if m := linkRe.FindStringSubmatch(lower); m != nil {
			c.IDs = append(c.IDs, m[1])
			c.Providers = append(c.Providers, transcript.Codex)
			continue
		}
		switch lower {
		case ".", "here", "this", "cwd":
			c.Dirs = append(c.Dirs, cwd)
			continue
		case "all", "any", "ever":
			c.AllTime = true
			continue
		case "today":
			y, mo, d := now.Date()
			c.Since = time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
			continue
		case "yesterday":
			y, mo, d := now.AddDate(0, 0, -1).Date()
			c.Since = time.Date(y, mo, d, 0, 0, 0, 0, now.Location())
			continue
		}
		// A short id can be all digits and a "d" ("2048163d"): six or more
		// hex characters is an id, since no one asks for 27 years in days.
		if m := durationRe.FindStringSubmatch(lower); m != nil && !idRe.MatchString(lower) {
			n, err := strconv.Atoi(m[1])
			unit := map[byte]time.Duration{'m': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[m[2][0]]
			if err != nil || n > int(math.MaxInt64/unit) {
				// Further back than a Duration reaches (about 292 years):
				// multiplying would wrap to a time in the future.
				c.AllTime = true
				continue
			}
			c.Since = now.Add(-time.Duration(n) * unit)
			continue
		}
		if lower == "doctor" {
			c.Doctor = true
			continue
		}
		if looksLikePath(a) {
			p := expandPath(a, cwd)
			st, err := os.Stat(p)
			switch {
			case err == nil && st.IsDir():
				c.Dirs = append(c.Dirs, p)
				continue
			case err == nil && strings.HasSuffix(p, ".jsonl"):
				c.Files = append(c.Files, p)
				continue
			case err == nil:
				// A source file: the question is which sessions touched it.
				c.Paths = append(c.Paths, p)
				continue
			}
		}
		if day, ok := parseDay(lower, now); ok {
			c.Since, c.Until = day, day.AddDate(0, 0, 1)
			continue
		}
		// A path to something that is not there is still a fair question: the
		// file may have been deleted, renamed, or be in another checkout. It
		// is tried after the dates, since "9/9" is a date, not a path.
		if strings.Contains(a, "/") && looksLikePath(a) {
			c.Paths = append(c.Paths, expandPath(a, cwd))
			continue
		}
		if m := agentIDRe.FindStringSubmatch(a); m != nil {
			c.IDs = append(c.IDs, m[1])
			continue
		}
		if idRe.MatchString(lower) || namedAgentRe.MatchString(lower) {
			c.IDs = append(c.IDs, lower)
			continue
		}
		c.Words = append(c.Words, lower)
	}
	return c
}

// parseDay reads an absolute day. A date with no year is the most recent one
// that has already happened: logs are in the past, so "sep9" in September 2026
// means 2026-09-09, and "dec25" means the December before.
func parseDay(s string, now time.Time) (time.Time, bool) {
	day := func(y, mo, d int) (time.Time, bool) {
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, now.Location())
		// Reject the days Go rolls over, e.g. February 31st.
		if int(t.Month()) != mo || t.Day() != d {
			return time.Time{}, false
		}
		return t, true
	}
	past := func(mo, d int) (time.Time, bool) {
		t, ok := day(now.Year(), mo, d)
		if ok && t.After(now) {
			return day(now.Year()-1, mo, d)
		}
		return t, ok
	}
	switch {
	case isoDayRe.MatchString(s):
		m := isoDayRe.FindStringSubmatch(s)
		return day(atoi(m[1]), atoi(m[2]), atoi(m[3]))
	case numDayRe.MatchString(s):
		m := numDayRe.FindStringSubmatch(s)
		mo, d := atoi(m[1]), atoi(m[2])
		if mo > 12 && d <= 12 {
			mo, d = d, mo // 25/12 can only be day/month
		}
		if m[3] == "" {
			return past(mo, d)
		}
		y := atoi(m[3])
		if y < 100 {
			y += 2000
		}
		return day(y, mo, d)
	case monthDayRe.MatchString(s):
		m := monthDayRe.FindStringSubmatch(s)
		if mo, ok := month(m[1]); ok {
			return past(mo, atoi(m[2]))
		}
	case dayMonthRe.MatchString(s):
		m := dayMonthRe.FindStringSubmatch(s)
		if mo, ok := month(m[2]); ok {
			return past(mo, atoi(m[1]))
		}
	}
	return time.Time{}, false
}

// month resolves an unambiguous prefix of an English month name.
func month(name string) (int, bool) {
	for i, full := range months {
		if strings.HasPrefix(full, name) {
			return i + 1, true
		}
	}
	return 0, false
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func looksLikePath(s string) bool {
	return strings.ContainsRune(s, '/') || strings.HasPrefix(s, "~") || strings.HasSuffix(s, ".jsonl")
}

func expandPath(p, cwd string) string {
	if strings.HasPrefix(p, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[1:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}
