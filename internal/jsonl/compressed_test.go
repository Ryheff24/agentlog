package jsonl

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Ryheff24/agentlog/internal/zstd/zstdtest"
)

// The testdata logs were made by the zstd command from one 914KB log of 4,000
// records, one of them a 180KB line: at -19 in one frame, as two frames back to
// back, and with a 128MB window and no declared size (zstd --long=27, from a
// pipe). This is the sha256 of that log, taken before compressing it.
const fixtureSHA = "35b6cce87ca7de0c5ec44e21a087069758dd1a4a93ff1da63249c75f6b4b79ad"

var fixtures = []string{"log.jsonl.zst", "frames.jsonl.zst", "long.jsonl.zst"}

// plainCopy decompresses a testdata log into a plain one, so the two can be
// read side by side.
func plainCopy(t *testing.T, name string) (compressed, plain string) {
	t.Helper()
	compressed = filepath.Join("testdata", name)
	r, err := Open(compressed)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != fixtureSHA {
		t.Fatalf("%s decompressed to something other than the log that was compressed", name)
	}
	plain = filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(plain, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return compressed, plain
}

// A compressed log is the same log: every reader hands over exactly what it
// would from the plain copy, line numbers and windows included.
func TestCompressedLogsReadLikePlain(t *testing.T) {
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			zst, plain := plainCopy(t, name)
			st, err := os.Stat(plain)
			if err != nil {
				t.Fatal(err)
			}
			size := st.Size()
			readers := map[string]func(path string, fn func([]byte, int) error) error{
				"each": Each,
			}
			for _, w := range []int64{1, 50, 4 << 10, 64 << 10, 200 << 10, size - 1, size, size + 1} {
				readers["head "+strconv.FormatInt(w, 10)] = func(p string, fn func([]byte, int) error) error { return Head(p, w, fn) }
				readers["tail "+strconv.FormatInt(w, 10)] = func(p string, fn func([]byte, int) error) error { return Tail(p, w, fn) }
			}
			for what, read := range readers {
				want, err := seen(func(fn func([]byte, int) error) error { return read(plain, fn) })
				if err != nil {
					t.Fatal(err)
				}
				got, err := seen(func(fn func([]byte, int) error) error { return read(zst, fn) })
				if err != nil {
					t.Fatalf("%s: %v", what, err)
				}
				if strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Errorf("%s: %d lines from the compressed log, %d from the plain one", what, len(got), len(want))
				}
			}
			if lines, _ := seen(func(fn func([]byte, int) error) error { return Each(zst, fn) }); len(lines) != 4000 {
				t.Errorf("Each() = %d lines, want 4000", len(lines))
			}
		})
	}
}

// A tail window that opens inside a line too long for it holds no whole
// record, compressed or not, whether or not the log ends in a newline.
func TestCompressedTailInsideOneGiantLine(t *testing.T) {
	for _, end := range []string{"\n", ""} {
		src := "{\"a\":1}\n{\"b\":\"" + strings.Repeat("x", 300<<10) + "\"}" + end
		path := filepath.Join(t.TempDir(), "log.jsonl.zst")
		if err := os.WriteFile(path, zstdtest.Frame([]byte(src)), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := seen(func(fn func([]byte, int) error) error { return Tail(path, 64<<10, fn) })
		if err != nil || len(got) != 0 {
			t.Errorf("end %q: Tail() = %d lines, %v; want none", end, len(got), err)
		}
		got, err = seen(func(fn func([]byte, int) error) error { return Each(path, fn) })
		if err != nil || len(got) != 2 {
			t.Errorf("end %q: Each() = %d lines, %v; want 2", end, len(got), err)
		}
	}
}

// A damaged compressed log is an error wherever it is read, never a log that
// quietly ends early: a reader that stopped at the damage would answer for a
// session it only half read.
func TestDamagedCompressedLogIsAnError(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("testdata", "log.jsonl.zst"))
	if err != nil {
		t.Fatal(err)
	}
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)/2] ^= 0xff
	for name, b := range map[string][]byte{
		"cut short":       good[:len(good)/2],
		"a byte changed":  flipped,
		"not zstd at all": []byte("{\"a\":1}\n"),
		"no blocks":       good[:12],
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "log.jsonl.zst")
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := Each(path, func([]byte, int) error { return nil }); err == nil {
				t.Error("Each() = nil error")
			}
			if err := Tail(path, 4<<10, func([]byte, int) error { return nil }); err == nil {
				t.Error("Tail() = nil error")
			}
		})
	}
}

func TestLogNames(t *testing.T) {
	for name, want := range map[string]bool{
		"a.jsonl": true, "rollout-x.jsonl.zst": true, "a.zst": false, "a.json": false, "a.jsonl.gz": false, "events.jsonl": true,
	} {
		if IsLog(name) != want {
			t.Errorf("IsLog(%q) = %v", name, !want)
		}
	}
	for name, want := range map[string]string{"a.jsonl": "a", "a.jsonl.zst": "a", "a": "a"} {
		if got := TrimLog(name); got != want {
			t.Errorf("TrimLog(%q) = %q, want %q", name, got, want)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing.jsonl.zst")
	if _, err := Open(missing); err == nil {
		t.Error("Open() of a missing log = nil error")
	}
	if err := Tail(missing, 4<<10, func([]byte, int) error { return nil }); err == nil {
		t.Error("Tail() of a missing log = nil error")
	}
}
