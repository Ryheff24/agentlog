package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"agentlog/internal/transcript"
)

func TestParseClues(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 16, 15, 30, 0, 0, time.Local)

	c := ParseClues([]string{"Codex", "codex://threads/01a0a5c3-7e10-7d24-9f31-6b28c40e1a95", "here", "sub", "./sub", "2h", "Fullscreen"}, cwd, now)
	if !slices.Equal(c.Providers, []string{transcript.Codex, transcript.Codex}) {
		t.Errorf("Providers = %v", c.Providers)
	}
	if !slices.Equal(c.IDs, []string{"01a0a5c3-7e10-7d24-9f31-6b28c40e1a95"}) {
		t.Errorf("IDs = %v", c.IDs)
	}
	// A bare name without a slash is a word even if a directory of that name exists.
	if !slices.Equal(c.Dirs, []string{cwd, filepath.Join(cwd, "sub")}) {
		t.Errorf("Dirs = %v", c.Dirs)
	}
	if !slices.Equal(c.Words, []string{"sub", "fullscreen"}) {
		t.Errorf("Words = %v", c.Words)
	}
	if want := now.Add(-2 * time.Hour); !c.Since.Equal(want) {
		t.Errorf("Since = %v, want %v", c.Since, want)
	}
}

func TestParseCluesRefs(t *testing.T) {
	now := time.Now()
	tests := []struct {
		arg      string
		id       string
		from, to int
	}{
		{"01a0ab27-c514#85", "01a0ab27-c514", 85, 0},
		{"3f2a91bc#10-20", "3f2a91bc", 10, 20},
		{"agent-a1b2c3d4e5f60718", "a1b2c3d4e5f60718", 0, 0},
		{"#7", "", 7, 0},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			c := ParseClues([]string{tt.arg}, "/", now)
			var id string
			if len(c.IDs) > 0 {
				id = c.IDs[0]
			}
			if id != tt.id || c.Event != tt.from || c.EventTo != tt.to {
				t.Errorf("ParseClues(%q) = id %q #%d-%d, want id %q #%d-%d", tt.arg, id, c.Event, c.EventTo, tt.id, tt.from, tt.to)
			}
		})
	}
}

func TestParseCluesWordsVsIDs(t *testing.T) {
	// A named Claude subagent's id, as every ref prints it, is an id: read
	// as a word it widened the search and matched by name. Its name alone
	// is still a word.
	// A short id that is all digits and a "d" reads as a count of days too;
	// at id length it is the id.
	c := ParseClues([]string{"decade", "abc", "fix-84", "Atasks-frontend-f7491e15c6c5771e", "atasks-frontend", "2048163d", "today", "all"},
		"/", time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC))
	// "decade" is valid hex; ids need six or more characters, so it counts as one.
	if !slices.Equal(c.IDs, []string{"decade", "atasks-frontend-f7491e15c6c5771e", "2048163d"}) {
		t.Errorf("IDs = %v", c.IDs)
	}
	if !slices.Equal(c.Words, []string{"abc", "fix-84", "atasks-frontend"}) {
		t.Errorf("Words = %v", c.Words)
	}
	if !c.AllTime || c.Since.Hour() != 0 {
		t.Errorf("AllTime = %v Since = %v", c.AllTime, c.Since)
	}
	if !c.Narrowed() {
		t.Error("Narrowed() = false")
	}
	if (Clues{AllTime: true}).Narrowed() {
		t.Error("\"all\" alone should not count as narrowing")
	}
}

func TestParseCluesDays(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 30, 0, 0, time.Local)
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.Local) }
	tests := []struct {
		arg  string
		want time.Time // zero means "not a day"
	}{
		{"2026-09-09", day(2026, time.September, 9)},
		{"2026/9/9", day(2026, time.September, 9)},
		{"9/9", day(2026, time.September, 9)},
		{"9/9/26", day(2026, time.September, 9)},
		{"sep9", day(2026, time.September, 9)},
		{"sep-9", day(2026, time.September, 9)},
		{"september 9", day(2026, time.September, 9)},
		{"9sep", day(2026, time.September, 9)},
		{"9th sep", day(2026, time.September, 9)},
		{"25/12", day(2025, time.December, 25)}, // 25 cannot be a month
		// A month and day that have not happened yet this year are last year's.
		{"dec25", day(2025, time.December, 25)},
		{"feb31", time.Time{}},  // no such day
		{"phase2", time.Time{}}, // not a month
		{"9d", time.Time{}},     // a duration, and durations win
		{"decade", time.Time{}}, // an id
		{"fix-84", time.Time{}}, // a word
		{"01a0a5c3", time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			c := ParseClues([]string{tt.arg}, "/", now)
			if tt.want.IsZero() {
				if !c.Until.IsZero() {
					t.Fatalf("ParseClues(%q) read a day: %v", tt.arg, c.Since)
				}
				return
			}
			if !c.Since.Equal(tt.want) {
				t.Fatalf("ParseClues(%q).Since = %v, want %v", tt.arg, c.Since, tt.want)
			}
			if want := tt.want.AddDate(0, 0, 1); !c.Until.Equal(want) {
				t.Errorf("ParseClues(%q).Until = %v, want %v", tt.arg, c.Until, want)
			}
			if len(c.Words) != 0 || len(c.IDs) != 0 {
				t.Errorf("ParseClues(%q) also produced words %v ids %v", tt.arg, c.Words, c.IDs)
			}
		})
	}
}

func TestParseCluesOpenRange(t *testing.T) {
	c := ParseClues([]string{"01a0ab27-c514#40-"}, "/", time.Now())
	if c.Event != 40 || c.EventTo != -1 {
		t.Errorf("#40- = #%d-%d, want #40 to the end (-1)", c.Event, c.EventTo)
	}
	if len(c.IDs) != 1 || c.IDs[0] != "01a0ab27-c514" {
		t.Errorf("IDs = %v", c.IDs)
	}
}

// A path clue asks a different question from every other clue: not which
// session to open, but which sessions touched this file. A date that looks
// like a path ("9/9") is still a date.
func TestParseCluesFilePaths(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "reportdata.go")
	if err := os.WriteFile(src, []byte("package analytics\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "019e0a00.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 16, 15, 30, 0, 0, time.Local)
	c := ParseClues([]string{src}, dir, now)
	if len(c.Paths) != 1 || c.Paths[0] != src {
		t.Errorf("Paths = %v, want the source file", c.Paths)
	}
	if len(c.Files) != 0 {
		t.Errorf("Files = %v, want the source file not taken for a log", c.Files)
	}
	if c = ParseClues([]string{log}, dir, now); len(c.Files) != 1 || len(c.Paths) != 0 {
		t.Errorf("a .jsonl gave Files=%v Paths=%v, want it read as a log", c.Files, c.Paths)
	}
	if c = ParseClues([]string{dir}, dir, now); len(c.Dirs) != 1 || len(c.Paths) != 0 {
		t.Errorf("a directory gave Dirs=%v Paths=%v", c.Dirs, c.Paths)
	}
	// Gone from disk, still a fair question.
	if c = ParseClues([]string{"src/App.tsx"}, dir, now); len(c.Paths) != 1 || c.Paths[0] != filepath.Join(dir, "src/App.tsx") {
		t.Errorf("Paths = %v, want the missing file resolved against the cwd", c.Paths)
	}
	if c = ParseClues([]string{"9/9"}, dir, now); len(c.Paths) != 0 || c.Since.IsZero() {
		t.Errorf("9/9 gave Paths=%v Since=%v, want a date", c.Paths, c.Since)
	}
}

// Arguments arrive from a shell an agent composed: quoted empty strings and
// padding happen, and words that are also commands ("doctor") or relative
// days mean the command, not a search for the word.
func TestParseCluesKeywords(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 30, 0, 0, time.UTC)
	c := ParseClues([]string{"", "   ", " Doctor ", "yesterday"}, "/", now)
	if !c.Doctor || len(c.Words) != 0 {
		t.Errorf("Doctor = %v words %v, want the doctor and no words", c.Doctor, c.Words)
	}
	if want := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC); !c.Since.Equal(want) || !c.Until.IsZero() {
		t.Errorf("yesterday = since %v until %v, want since %v and no end", c.Since, c.Until, want)
	}
	if c.Narrowed() != true {
		t.Error("a day should narrow")
	}
	if (ParseClues([]string{"", "doctor"}, "/", now)).Narrowed() {
		t.Error("doctor and blanks alone narrowed the search")
	}
}

// Durations are unit-suffixed counts. A count too large for a Duration
// (about 292 years) must not wrap into a time in the future, which would
// silently find nothing; it means "as far back as there is".
func TestParseCluesDurations(t *testing.T) {
	now := time.Date(2026, 9, 16, 15, 30, 0, 0, time.UTC)
	tests := []struct {
		arg     string
		ago     time.Duration
		allTime bool
	}{
		{"30m", 30 * time.Minute, false},
		{"90min", 90 * time.Minute, false},
		{"2hr", 2 * time.Hour, false},
		{"3days", 72 * time.Hour, false},
		{"2wk", 14 * 24 * time.Hour, false},
		{"0d", 0, false},
		{"20000w", 0, true},
		{"99999999999999999999w", 0, true}, // past int64 too
	}
	for _, tt := range tests {
		c := ParseClues([]string{tt.arg}, "/", now)
		switch {
		case tt.allTime:
			if !c.AllTime || !c.Since.IsZero() {
				t.Errorf("%s = since %v all %v, want all time", tt.arg, c.Since, c.AllTime)
			}
		case !c.Since.Equal(now.Add(-tt.ago)):
			t.Errorf("%s = since %v, want %v", tt.arg, c.Since, now.Add(-tt.ago))
		}
		if c.Since.After(now) {
			t.Errorf("%s put the window in the future: %v", tt.arg, c.Since)
		}
	}
}

// ~ is the home directory, as a shell would have expanded it had the agent
// not quoted it.
func TestParseCluesExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	c := ParseClues([]string{"~/no-such-project-agentlog/app.go"}, "/", time.Now())
	if want := filepath.Join(home, "no-such-project-agentlog/app.go"); len(c.Paths) != 1 || c.Paths[0] != want {
		t.Errorf("Paths = %v, want %s", c.Paths, want)
	}
	if c := ParseClues([]string{"~"}, "/", time.Now()); len(c.Dirs) != 1 || c.Dirs[0] != home {
		t.Errorf("~ gave Dirs = %v, want %s", c.Dirs, home)
	}
}
