// Package jsonl reads newline-delimited JSON logs whose lines can be many
// megabytes long, including cheap head and tail windows for discovery.
package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrStop can be returned by a callback to end iteration early without error.
var ErrStop = errors.New("stop")

// Each calls fn for every non-empty line of the file with its 1-based line
// number. The line slice is only valid during the call.
func Each(path string, fn func(line []byte, n int) error) error {
	f, err := os.Open(path)
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
	f, err := os.Open(path)
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
