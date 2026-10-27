package work

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDefaultStaysWellUnderTheMachine(t *testing.T) {
	t.Setenv(jobsEnv, "")
	got := Default()
	if got < 1 || got > maxJobs {
		t.Errorf("Default() = %d, want between 1 and %d on a %d-core machine", got, maxJobs, runtime.NumCPU())
	}
	if got > runtime.NumCPU() {
		t.Errorf("Default() = %d, more than the machine has", got)
	}
}

func TestDefaultHonoursTheOverride(t *testing.T) {
	base := func() int { t.Setenv(jobsEnv, ""); return Default() }()
	for _, tt := range []struct {
		env  string
		want int
	}{
		{"7", 7},
		{"1", 1},
		{"0", base}, // nonsense falls back
		{"many", base},
	} {
		t.Setenv(jobsEnv, tt.env)
		if got := Default(); got != tt.want {
			t.Errorf("%s=%q gave %d, want %d", jobsEnv, tt.env, got, tt.want)
		}
	}
}

// The point of the package: every fan-out in the CLI draws on one pool, so
// two of them running at once cannot add up to more than the limit.
func TestDoNeverExceedsTheLimit(t *testing.T) {
	defer Limit(Default())
	Limit(3)

	var now, peak int64
	watch := func(int) {
		if n := atomic.AddInt64(&now, 1); n > atomic.LoadInt64(&peak) {
			atomic.StoreInt64(&peak, n)
		}
		for range 200 {
			runtime.Gosched()
		}
		atomic.AddInt64(&now, -1)
	}

	var wg sync.WaitGroup
	for range 4 { // four separate fan-outs, as four features would
		wg.Add(1)
		go func() {
			defer wg.Done()
			Do(context.Background(), 25, watch)
		}()
	}
	wg.Wait()

	if peak > 3 {
		t.Errorf("%d tasks ran at once, limit was 3", peak)
	}
	if peak < 2 {
		t.Errorf("peak concurrency %d: the pool is not being used at all", peak)
	}
}

func TestDoStopsWhenCancelled(t *testing.T) {
	defer Limit(Default())
	Limit(2)

	ctx, cancel := context.WithCancel(context.Background())
	var ran int64
	Do(ctx, 1000, func(int) {
		if atomic.AddInt64(&ran, 1) == 4 {
			cancel()
		}
	})
	if n := atomic.LoadInt64(&ran); n > 100 {
		t.Errorf("ran %d of 1000 tasks after cancellation, want it to stop promptly", n)
	}
	if ctx.Err() == nil {
		t.Fatal("test did not cancel")
	}
}

func TestDoWaitsForEveryTask(t *testing.T) {
	defer Limit(Default())
	Limit(2)
	var done int64
	Do(context.Background(), 50, func(int) { atomic.AddInt64(&done, 1) })
	if done != 50 {
		t.Errorf("%d of 50 tasks finished before Do returned", done)
	}
}

// A caller that has already given up (the pipe closed before the fan-out
// began) must start nothing. With a slot free and the context done, a bare
// select would pick either, so some runs would start a task anyway.
func TestDoStartsNothingForACancelledCaller(t *testing.T) {
	defer Limit(Default())
	Limit(4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran int64
	for range 200 {
		Do(ctx, 10, func(int) { atomic.AddInt64(&ran, 1) })
	}
	if ran != 0 {
		t.Errorf("%d tasks started for a cancelled caller", ran)
	}
}

// Nothing to do returns at once, and a nil context is treated as one that is
// never cancelled rather than a panic.
func TestDoEdgeCases(t *testing.T) {
	called := false
	Do(context.Background(), 0, func(int) { called = true })
	Do(context.Background(), -1, func(int) { called = true })
	if called {
		t.Error("Do ran a task for n <= 0")
	}
	var n int64
	var none context.Context
	Do(none, 5, func(int) { atomic.AddInt64(&n, 1) })
	if n != 5 {
		t.Errorf("Do(nil ctx) ran %d of 5", n)
	}
}

// Restrain is the first thing main does: the default limit, applied.
func TestRestrainAppliesTheDefault(t *testing.T) {
	defer Limit(Default())
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	t.Setenv(jobsEnv, "2")
	if got := Restrain(); got != 2 || Jobs() != 2 {
		t.Errorf("Restrain() = %d, Jobs %d; want 2", got, Jobs())
	}
	if got := Limit(0); got != 2 {
		t.Errorf("Limit(0) = %d, want the default", got)
	}
}
