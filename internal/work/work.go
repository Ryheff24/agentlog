// Package work is the one place that decides how much of the machine this
// process may take, and the one place that hands it out.
//
// agentlog runs on the laptop somebody is working on, started by an agent
// rather than by them, usually while they are doing something else. The
// fan-out that makes a search fast is exactly the thing that runs the fans up,
// and the failure mode is not a slow search: it is a hot, loud machine and a
// person who did not ask for any of it.
//
// So there is one limit, not one per feature. Search, doctor, the file lookup
// and enrichment all take their workers from the same pool, because limits
// that are counted separately are limits that stack: four here and four there
// is eight, and eight was the bug. GOMAXPROCS is set to the same number, so
// the runtime cannot spread what the pool is holding back.
package work

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"
)

// jobsEnv overrides the default, for a machine or a caller that wants more.
const jobsEnv = "AGENTLOG_JOBS"

// maxJobs is the ceiling whatever the machine has. Reading logs is bound by
// the disk long before it is bound by cores, and the last cores bought little
// while heating the machine a lot.
const maxJobs = 4

// Default is the limit when nobody has said otherwise.
func Default() int {
	if n, err := strconv.Atoi(os.Getenv(jobsEnv)); err == nil && n > 0 {
		return n
	}
	return max(1, min(maxJobs, runtime.NumCPU()/2))
}

var (
	mu    sync.Mutex
	limit int
	slots chan struct{}
)

// Limit sets the one cap: the size of the shared pool and the runtime's
// parallelism. It is called once, at startup, before anything fans out.
func Limit(n int) int {
	if n <= 0 {
		n = Default()
	}
	mu.Lock()
	defer mu.Unlock()
	limit, slots = n, make(chan struct{}, n)
	runtime.GOMAXPROCS(n)
	return n
}

// Jobs is the limit in force.
func Jobs() int {
	mu.Lock()
	defer mu.Unlock()
	if limit == 0 {
		limit, slots = Default(), make(chan struct{}, Default())
	}
	return limit
}

// pool returns the shared slots, creating them if nothing has set a limit.
func pool() chan struct{} {
	Jobs()
	mu.Lock()
	defer mu.Unlock()
	return slots
}

// Restrain applies the default limit and asks the scheduler to prefer
// everything else on the machine. The priority change is best-effort: a
// refused one is not worth failing a lookup over.
func Restrain() int {
	n := Limit(Default())
	nice()
	return n
}

// Do runs fn for each of n items through the shared pool and returns when they
// have all finished, or when ctx is cancelled — whichever comes first.
//
// This is the only way anything in agentlog fans out. fn must not call Do
// itself: a task waiting on the pool while holding a slot of it would be
// waiting for itself.
func Do(ctx context.Context, n int, fn func(i int)) {
	if n <= 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sem := pool()
	var wg sync.WaitGroup
	defer wg.Wait()
	for i := range n {
		// Checked first: when a slot is free and ctx is done, select picks
		// either at random, and a cancelled caller would still start work.
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
}
