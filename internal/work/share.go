package work

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Ryheff24/agentlog/internal/private"
)

// An agent does not run one agentlog. It runs four, in parallel, because it
// has four questions — and four processes each politely taking four workers is
// sixteen, which is the whole machine and the thing this package exists to
// prevent. So the limit is shared between processes as well as inside one.
//
// The mechanism is a directory of claims: one file per live agentlog, named
// for its pid, holding the number of workers it took. A starting process adds
// up the live claims and takes what is left, never less than one — a slow
// answer beats a hot laptop, and beats waiting for a lock that a crashed
// process is holding.
//
// Everything here fails soft. If the directory cannot be read or written, the
// process simply takes its own limit: bounding one agentlog is the job, and
// bounding several is the bonus.

// claimDirs are where live agentlog processes record what they took, best
// first. The cache directory is the right home for it; a sandbox that will not
// let anything be written there is common enough to have a second choice.
func claimDirs() []string {
	var out []string
	if dir, err := os.UserCacheDir(); err == nil {
		out = append(out, filepath.Join(dir, "agentlog", "workers"))
	}
	return append(out, filepath.Join(os.TempDir(), "agentlog-workers-"+strconv.Itoa(os.Getuid())))
}

// stale is how old a claim may be before it is ignored whatever its pid says.
// Pids are reused; a claim this old is from a process that is either finished
// or is doing something no laptop should be asked to sit through anyway.
const stale = 30 * time.Minute

// Share returns this process's portion of the limit, having recorded it, and a
// function that gives it back. It is called once, at startup.
func Share(want int) (got int, release func()) {
	if want < 1 {
		want = 1
	}
	for _, dir := range claimDirs() {
		if private.Dir(dir) != nil {
			continue
		}
		unlock := lock(dir)
		got = max(1, want-held(dir))
		path := filepath.Join(dir, strconv.Itoa(os.Getpid()))
		err := writeClaim(path, got)
		unlock()
		if err != nil {
			continue
		}
		return got, func() { os.Remove(path) }
	}
	return want, func() {}
}

// lock serialises the count-then-claim, which is short and happens once per
// process. Agents start several agentlog runs in the same breath, and without
// this they all count an empty directory and all take everything.
//
// It is advisory and it gives up: a lock nobody released is removed after a
// moment, and a lock that cannot be taken at all is proceeded without. The
// cost of being wrong is a run that took more workers than its share, not a
// run that hangs.
//
// Only a lock someone holds is waited for. A sandbox that will not let the
// lock file be written refuses it on every try, and waiting out the tries
// cost every sandboxed run 200ms.
func lock(dir string) (unlock func()) {
	path := filepath.Join(dir, "lock")
	for range lockTries {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }
		}
		if !errors.Is(err, fs.ErrExist) {
			return func() {}
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > lockStale {
			os.Remove(path) // whoever held it is gone
			continue
		}
		time.Sleep(lockWait)
	}
	return func() {}
}

const (
	lockTries = 50
	lockWait  = 4 * time.Millisecond
	lockStale = 5 * time.Second
)

// Reserve claims this process's share of the machine, applies it as the one
// limit, and returns it with the release to call when the process is done.
func Reserve(want int) (int, func()) {
	if want <= 0 {
		want = Default()
	}
	got, release := Share(want)
	return Limit(got), release
}

// held adds up what live agentlog processes have taken, tidying up after any
// that died without saying so.
func held(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	total := 0
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) > stale || !alive(pid) {
			os.Remove(path)
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
			total += n
		}
	}
	return total
}

// writeClaim records how many workers this process took, never through a
// link someone planted under its pid.
func writeClaim(path string, n int) error {
	f, err := private.Open(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strconv.Itoa(n))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
