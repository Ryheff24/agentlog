// Package jsonl reads newline-delimited JSON logs whose lines can be many
// megabytes long, including cheap head and tail windows for discovery.
// A log whose name ends in .zst is read decompressed, wherever it is read.
package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Ryheff24/agentlog/internal/zstd"
)

// ErrStop can be returned by a callback to end iteration early without error.
var ErrStop = errors.New("stop")

// Compressed is the suffix of a log stored zstd-compressed. Codex compresses
// its rollouts after about a week, so most of its history has it.
const Compressed = ".zst"

// IsLog reports whether name is a JSONL log, plain or compressed.
func IsLog(name string) bool {
	return strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl"+Compressed)
}

// TrimLog removes the .jsonl or .jsonl.zst suffix from name.
func TrimLog(name string) string {
	return strings.TrimSuffix(strings.TrimSuffix(name, Compressed), ".jsonl")
}

// Open opens a log for reading its records, decompressing it when its name
// says it is compressed.
func Open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, Compressed) {
		return f, nil
	}
	return decompressed{zstd.NewReader(f), f}, nil
}

type decompressed struct {
	*zstd.Reader
	f *os.File
}

func (d decompressed) Close() error { return d.f.Close() }

// Each calls fn for every non-empty line of the file with its 1-based line
// number. The line slice is only valid during the call.
func Each(path string, fn func(line []byte, n int) error) error {
	f, err := Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return each(f, 1, fn, nil)
}

// Head is like Each but stops after maxBytes have been consumed. A line the
// window cut in two is left out: it is not a record, and handing it over
// would have every windowed read of a large log report one it could not
// decode. A last line with no newline where the file itself ends is still
// handed over, as Each does.
func Head(path string, maxBytes int64, fn func(line []byte, n int) error) error {
	f, err := Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lr := &io.LimitedReader{R: f, N: maxBytes}
	cut := func() bool {
		if lr.N > 0 {
			return false // the file ended inside the window
		}
		var b [1]byte
		n, _ := f.Read(b[:])
		return n > 0
	}
	return each(lr, 1, fn, cut)
}

// Tail calls fn for each complete line within the last maxBytes of the file.
// Line numbers are unknown and passed as 0.
func Tail(path string, maxBytes int64, fn func(line []byte, n int) error) error {
	if strings.HasSuffix(path, Compressed) {
		return tailStream(path, maxBytes, fn)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	off := st.Size() - maxBytes
	skipPartial := off > 0
	if skipPartial {
		// Start one byte early: if that byte ends a line, the window opens on
		// a whole line, and skipping "the partial first line" skips only it.
		off--
	} else {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("seek tail: %w", err)
	}
	r := bufio.NewReaderSize(f, 1<<16)
	if skipPartial {
		if _, err := r.ReadBytes('\n'); err != nil {
			if err == io.EOF {
				return nil // window is inside one giant line
			}
			return err
		}
	}
	return each(r, 0, fn, nil)
}

// tailStream is Tail for a log that can only be read from its start, as a
// compressed one can: it reads all of it and keeps the window Tail would have
// seeked to, the last maxBytes and the byte before them.
func tailStream(path string, maxBytes int64, fn func(line []byte, n int) error) error {
	f, err := Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	window := int(maxBytes) + 1
	var keep []byte
	var total int64
	buf := make([]byte, 1<<16)
	for {
		n, err := f.Read(buf)
		total += int64(n)
		keep = append(keep, buf[:n]...)
		if len(keep) > 2*window {
			keep = keep[:copy(keep, keep[len(keep)-window:])]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	skipPartial := total > maxBytes
	if len(keep) > window {
		keep = keep[len(keep)-window:]
	}
	r := bufio.NewReaderSize(bytes.NewReader(keep), 1<<16)
	if skipPartial {
		if _, err := r.ReadBytes('\n'); err != nil {
			return nil // the window is inside one giant line
		}
	}
	return each(r, 0, fn, nil)
}

// each hands fn every non-empty line of r. cut, when given, is asked about a
// final line with no newline: true means the reader was cut short there and
// the line is only part of one.
func each(r io.Reader, start int, fn func(line []byte, n int) error, cut func() bool) error {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReaderSize(r, 1<<16)
	}
	n := start
	for {
		line, err := br.ReadBytes('\n')
		if err == io.EOF && len(line) > 0 && cut != nil && cut() {
			return nil
		}
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			// A final line without '\n' may be a partial write; json decoding
			// in the callback rejects it, which is fine.
			if len(trimmed) > 0 {
				if cbErr := fn(trimmed, n); cbErr != nil {
					if errors.Is(cbErr, ErrStop) {
						return nil
					}
					return cbErr
				}
			}
			if start > 0 {
				n++
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
