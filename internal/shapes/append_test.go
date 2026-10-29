package shapes_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Ryheff24/agentlog/internal/catalog"
	"github.com/Ryheff24/agentlog/internal/transcript"
)

// An agent that read #40 an hour ago and opens #40 now must see the same event.
// Each cut below is the log as it stood at one moment: the full log has to keep every event the cut already numbered.
func TestNumbersSurviveAppend(t *testing.T) {
	if testing.Short() {
		t.Skip("reads real logs")
	}
	checked := 0
	for _, p := range providers() {
		sessions, err := p.Discover(transcript.Query{Since: time.Now().AddDate(0, 0, -30)})
		if err != nil {
			continue // no logs from this harness here
		}
		for _, s := range sample(sessions, 60) {
			checked++
			for _, problem := range numberDrift(t, p, s) {
				t.Errorf("%s %s: %s", p.Name(), s.ID, problem)
			}
		}
	}
	if checked == 0 {
		t.Skip("no harness logs on this machine")
	}
}

// sample takes up to n sessions spread evenly over size, so short and long logs are both read.
func sample(sessions []*transcript.Session, n int) []*transcript.Session {
	var fit []*transcript.Session
	for _, s := range sessions {
		if len(s.Files) > 0 {
			fit = append(fit, s)
		}
	}
	sort.Slice(fit, func(i, j int) bool { return size(fit[i]) < size(fit[j]) })
	if len(fit) <= n {
		return fit
	}
	out := make([]*transcript.Session, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fit[i*len(fit)/n])
	}
	return out
}

func size(s *transcript.Session) int64 {
	st, err := os.Stat(s.Files[len(s.Files)-1])
	if err != nil {
		return 0
	}
	return st.Size()
}

// numberDrift loads the session whole and cut at several line ends, and reports each numbered event that moved.
// Only the last file grows, so only it is cut. The bytes are read once, so a log written meanwhile cannot differ between loads.
func numberDrift(t *testing.T, p catalog.Provider, s *transcript.Session) []string {
	t.Helper()
	last := s.Files[len(s.Files)-1]
	data, err := os.ReadFile(last)
	if err != nil {
		return nil
	}
	full := loadWith(t, p, s, last, data)
	if full == nil {
		return nil
	}
	var problems []string
	for _, cut := range lineEnds(data, 8) {
		prefix := loadWith(t, p, s, last, data[:cut])
		if prefix == nil {
			continue
		}
		for i, e := range prefix.Events {
			// A prompt queued while the model was busy is a placeholder until its delivery takes it down, by design.
			if e.Tag == transcript.TagUnanswered {
				break
			}
			if i >= len(full.Events) {
				problems = append(problems, fmt.Sprintf("cut at byte %d: #%d (%s, line %d) is gone from the full log", cut, e.Index, e.Kind, e.Line))
				break
			}
			f := full.Events[i]
			if f.Kind != e.Kind || f.Tool != e.Tool || f.Line != e.Line {
				problems = append(problems, fmt.Sprintf("cut at byte %d: #%d was %s %q line %d, is now %s %q line %d", cut, e.Index, e.Kind, e.Tool, e.Line, f.Kind, f.Tool, f.Line))
				break
			}
		}
	}
	return problems
}

// loadWith loads the session with its last file replaced by data, written under the same name in a fresh folder.
func loadWith(t *testing.T, p catalog.Provider, s *transcript.Session, last string, data []byte) *transcript.Transcript {
	t.Helper()
	path := filepath.Join(t.TempDir(), filepath.Base(last))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cp := *s
	cp.Files = append(append([]string(nil), s.Files[:len(s.Files)-1]...), path)
	tr, err := p.Load(&cp, transcript.Load{})
	if err != nil {
		return nil
	}
	return tr
}

// lineEnds returns up to n byte offsets just after a newline, spread evenly through data.
func lineEnds(data []byte, n int) []int {
	var ends []int
	for i := 1; i <= n; i++ {
		at := len(data) * i / (n + 1)
		j := bytes.IndexByte(data[at:], '\n')
		if j < 0 {
			break
		}
		if cut := at + j + 1; len(ends) == 0 || cut > ends[len(ends)-1] {
			ends = append(ends, cut)
		}
	}
	return ends
}
