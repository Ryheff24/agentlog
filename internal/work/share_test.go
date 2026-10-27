package work

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHeldCountsOnlyLiveClaims(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, strconv.Itoa(os.Getpid()), "3")  // this process: not counted against itself
	write(t, dir, strconv.Itoa(os.Getppid()), "2") // a live process
	dead := write(t, dir, "999999", "4")           // a pid that is not running
	write(t, dir, "notapid", "1")                  // whatever else is in there

	if got := held(dir); got != 2 {
		t.Errorf("held = %d, want 2 (only the live claim, and not our own)", got)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("the claim of a process that is gone was left behind")
	}
}

func TestHeldIgnoresAndRemovesStaleClaims(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, strconv.Itoa(os.Getppid()), "4")
	old := time.Now().Add(-2 * stale)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := held(dir); got != 0 {
		t.Errorf("held = %d, want 0: a claim older than %v is not worth believing", got, stale)
	}
}

func TestLockIsExclusiveAndGivesUp(t *testing.T) {
	dir := t.TempDir()
	unlock := lock(dir)
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		lock(dir)() // blocked until it gives up, since nothing released
		done <- time.Since(start)
	}()
	select {
	case waited := <-done:
		if waited < lockWait {
			t.Errorf("second lock returned in %v: it did not wait for the first", waited)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lock never gave up")
	}
	unlock()
	if _, err := os.Stat(filepath.Join(dir, "lock")); !os.IsNotExist(err) {
		t.Error("unlock left the lock file behind")
	}
}

// A sandbox that refuses writes to the claim directory refuses the lock file
// on every try; waiting for it to be released would wait for nothing.
func TestLockDoesNotWaitOnADirItCannotWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("running as a user that can write a read-only dir")
	}
	start := time.Now()
	lock(dir)()
	if waited := time.Since(start); waited >= lockWait {
		t.Errorf("waited %v for a lock the dir will never take", waited)
	}
}

// isolate points the claim directories at fresh temp dirs, so a test neither
// reads the claims of agentlog runs on this machine nor leaves one behind.
func isolate(t *testing.T) (cache, tmp string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", t.TempDir())
	dirs := claimDirs()
	if len(dirs) != 2 || !strings.HasPrefix(dirs[0], home) {
		t.Skipf("claim dirs %v are not under the test's home", dirs)
	}
	return dirs[0], dirs[1]
}

func readOnly(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("running as a user that can write a read-only dir")
	}
}

// Four agentlog runs started in the same breath share one budget: the first
// takes what it asked for, and while its claim stands the next gets what is
// left, never less than one. Releasing gives it back.
func TestShareSplitsTheLimitBetweenProcesses(t *testing.T) {
	cache, _ := isolate(t)
	got, release := Share(4)
	if got != 4 {
		t.Fatalf("first Share(4) = %d, want 4 with nothing else running", got)
	}
	claim := filepath.Join(cache, strconv.Itoa(os.Getpid()))
	if b, err := os.ReadFile(claim); err != nil || string(b) != "4" {
		t.Errorf("claim = %q, %v, want 4 recorded under this pid", b, err)
	}
	release()
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Error("release left the claim behind")
	}

	// Another live process (the test's parent stands in for one) holds 3.
	write(t, cache, strconv.Itoa(os.Getppid()), "3")
	for _, tt := range []struct{ want, got int }{{4, 1}, {5, 2}, {2, 1}, {0, 1}, {-3, 1}} {
		got, release := Share(tt.want)
		release()
		if got != tt.got {
			t.Errorf("Share(%d) with 3 held = %d, want %d", tt.want, got, tt.got)
		}
	}
}

// A sandbox that will not let the cache directory be written is common; the
// claim goes in the temp dir instead. If nothing can be written, the run
// takes its own limit and bounds only itself.
func TestShareFallsBackWhenItCannotWrite(t *testing.T) {
	cache, tmp := isolate(t)
	readOnly(t, cache)
	got, release := Share(3)
	if got != 3 {
		t.Errorf("Share(3) = %d, want 3", got)
	}
	if _, err := os.Stat(filepath.Join(tmp, strconv.Itoa(os.Getpid()))); err != nil {
		t.Errorf("no claim in the fallback dir: %v", err)
	}
	release()

	readOnly(t, tmp)
	got, release = Share(3)
	release() // a no-op, and must not panic
	if got != 3 {
		t.Errorf("Share(3) with nowhere to write = %d, want its own limit", got)
	}
}

// A process killed while holding the lock leaves the file behind. The next
// run must take it over once it is old, not wait out every try on it.
func TestLockTakesOverAnAbandonedLock(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "lock", "")
	old := time.Now().Add(-2 * lockStale)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	unlock := lock(dir)
	if waited := time.Since(start); waited >= lockWait {
		t.Errorf("waited %v on a lock abandoned %v ago", waited, 2*lockStale)
	}
	if info, err := os.Stat(path); err != nil || time.Since(info.ModTime()) > lockStale {
		t.Errorf("lock file = %v, %v; want a fresh one held by this call", info, err)
	}
	unlock()
}

// Reserve is what startup calls: its share becomes the one limit in force.
func TestReserveAppliesTheShare(t *testing.T) {
	isolate(t)
	defer Limit(Default())
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	got, release := Reserve(2)
	defer release()
	if got != 2 || Jobs() != 2 || runtime.GOMAXPROCS(0) != 2 {
		t.Errorf("Reserve(2) = %d, Jobs %d, GOMAXPROCS %d; want 2 for all", got, Jobs(), runtime.GOMAXPROCS(0))
	}
	release()
	t.Setenv(jobsEnv, "3")
	got, release = Reserve(0)
	if got != 3 {
		t.Errorf("Reserve(0) = %d, want the default (3 from %s)", got, jobsEnv)
	}
}
