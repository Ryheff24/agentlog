package shapes

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Ryheff24/agentlog/internal/jsonl"
)

// Harvest reads real logs and returns one redacted example of every record
// shape it meets. It is a development tool, run by hand: what it returns is
// redacted on the way out, because what it read was somebody's work.
type Harvest struct {
	Provider string
	Roots    []string
	Files    int // newest log files to read; 0 for all of them
	Max      int // largest record considered, in bytes
	Per      int // most shapes kept per record type; 0 for a sensible few
}

// Run walks the roots newest first and collects shapes.
func (h Harvest) Run() ([]Shape, error) {
	files, err := logs(h.Roots, h.Files)
	if err != nil {
		return nil, err
	}
	found := map[string]Shape{}
	per := map[string]int{} // record type -> shapes kept, so that one type with
	if h.Per <= 0 {         // a free-form payload cannot fill the fixtures
		h.Per = perType
	}
	for _, f := range files {
		version := ""
		// A file that cannot be read is not a failure of the harvest: logs are
		// written while they are read, and a half-written line is normal.
		_ = jsonl.Each(f, func(line []byte, n int) error {
			if h.Max > 0 && len(line) > h.Max {
				return nil
			}
			// The last line of a log being written is cut off. It is not a
			// shape; recorded, it would be a fixture no reader could pass.
			if !json.Valid(line) {
				return nil
			}
			s := Of(h.Provider, version, line)
			version = s.Version
			if _, seen := found[s.Key]; seen || per[s.Type] >= h.Per {
				return nil
			}
			found[s.Key], per[s.Type] = s, per[s.Type]+1
			return nil
		})
	}
	return sorted(found), nil
}

// perType is how many variations of one record type are worth keeping. The
// files are read newest first, so these are the most recent ones.
const perType = 4

// logs lists the JSONL files under the roots, newest first.
func logs(roots []string, limit int) ([]string, error) {
	type file struct {
		path string
		mod  int64
	}
	var all []file
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
				return nil //nolint:nilerr // an unreadable directory is skipped, not fatal
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			all = append(all, file{path, info.ModTime().UnixNano()})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod > all[j].mod })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	out := make([]string, len(all))
	for i, f := range all {
		out[i] = f.path
	}
	return out, nil
}

// Merge adds newly harvested shapes to the ones already recorded. Nothing is
// ever dropped: a shape that stopped appearing in the logs last month is still
// a shape the reader has to go on reading.
func Merge(kept, found []Shape) []Shape {
	all := map[string]Shape{}
	for _, s := range kept {
		all[s.Key] = s
	}
	for _, s := range found {
		if _, seen := all[s.Key]; !seen {
			all[s.Key] = s
		}
	}
	return sorted(all)
}

func sorted(m map[string]Shape) []Shape {
	out := make([]Shape, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Read loads a harvested file. A missing file is an empty harvest.
func Read(path string) ([]Shape, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Shape
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var s Shape
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// Write records a harvest, one shape per line, in key order so that a new
// shape shows up in a diff as a new line rather than as a reshuffle.
func Write(path string, shapes []Shape) error {
	var b strings.Builder
	for _, s := range shapes {
		line, err := json.Marshal(s)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
