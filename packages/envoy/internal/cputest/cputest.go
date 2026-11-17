// Package cputest reads this process's own CPU time, the measure every growth test in this module
// times a call by, and the CPU time of the one thread that runs a call (OfCall). A call's wall time
// counts every moment the scheduler leaves the process off a core, so a loaded machine stretches it
// on either side of a comparison without the call doing more work; CPU time leaves that out. It is
// not immune to load: a busy machine still slows the process's own CPU time through shared caches
// and clock frequency, so a comparison still samples each side more than once. pmdoc's linear-time
// tests and the api's save ladders both time calls this way, and the form of The Spec times its
// judgment on the thread, so it lives here once; it is an ordinary package rather than a _test.go
// file because Go cannot share test-only code across packages (see internal/stacktest's identical
// reasoning).
package cputest

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// Process is this process's total CPU time, user and system, since it started
// (getrusage(RUSAGE_SELF)). It counts every goroutine, so a test that times one call by it runs
// no other measured work beside that call: no test in the packages that use it calls t.Parallel.
func Process() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		panic(fmt.Sprintf("getrusage(RUSAGE_SELF): %v", err))
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

// OfCall is the CPU time that one run of call spends on the thread that runs it (Thread): it holds
// the calling goroutine on its thread (runtime.LockOSThread) from the reading before call to the
// reading after it, so both readings are of that thread. Work that call hands to another goroutine
// is not in it, and nor is the collector's work on another thread, or the work of another process
// that call waits for, such as a database server; work that allocates nothing is.
func OfCall(call func()) time.Duration {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before := Thread()
	call()
	return Thread() - before
}

// Growth is how many times the work of large grows over the work of small, where the input of
// large is scale times the input of small. The work of each is its CPU time on the thread that
// runs it (OfCall). Small runs scale times in one timed window and large once in another, so that
// for work in proportion to the input the two windows last about as long, and a load of the
// machine that comes and goes reaches both alike: a window that is much shorter than the other is
// more likely to fall between two bursts. Each window is timed samples times, the two in turn,
// each after a collection of the heap (runtime.GC), so that neither pays for the garbage of the
// other. A busy machine only adds to the CPU time of a window, so the least of each is its work,
// and the answer holds the least of each window. The growth is scale times the work of the large
// window over the work of the small one: about scale for work in proportion to the input, and
// about scale times scale for work for each pair of units. It counts the thread of the caller
// alone, as OfCall does: the work of a database server that the call waits for is not in it, so a
// test that judges work through one also counts what the call sends to the server.
func Growth(samples, scale int, small, large func()) (growth float64, smallWindow, largeWindow time.Duration) {
	windows := []func(){func() {
		for range scale {
			small()
		}
	}, large}
	least := make([]time.Duration, len(windows))
	for sample := range samples {
		for index, window := range windows {
			runtime.GC()
			if took := OfCall(window); sample == 0 || took < least[index] {
				least[index] = took
			}
		}
	}
	return float64(scale) * float64(least[1]) / float64(least[0]), least[0], least[1]
}

// resolution is the smallest step Process moves in on this machine, measured once: the fewest
// nanoseconds between two readings that differ, over resolutionSamples spins. A Linux kernel moves
// it in steps far under a millisecond; gVisor moves it in 10 ms steps, so there a call shorter than
// 10 ms reads as 0 or 10 ms.
var resolution = sync.OnceValue(func() time.Duration {
	var step time.Duration
	for range resolutionSamples {
		start := Process()
		next := Process()
		for next == start {
			next = Process()
		}
		if step == 0 || next-start < step {
			step = next - start
		}
	}
	return step
})

// resolutionSamples is how many changes of Process resolution waits for. A sample that also
// counts another goroutine's CPU time reads a larger step, never a smaller one, so the smallest
// of a few is the clock's own step.
const resolutionSamples = 5

// windowSteps is how many steps of resolution a timed window must last before Measure divides it
// by its runs. A reading is off by up to one step at each end, so this keeps that error near 4% of
// the reading, where a single call shorter than a step reads as 0 or as one whole step.
const windowSteps = 50

// Measure returns the CPU time one run of an operation takes. batch runs the operation runs times
// in one timed window and returns the window's CPU time (Process after less Process before); it
// builds every run's input before the window opens, a fresh one for each run of an operation
// that changes its input, and returns an error that makes the window's time meaningless. Measure
// calls batch with more runs until a window lasts windowSteps steps of resolution, then divides
// that window's time by its runs. A window shorter than that is discarded, never averaged in.
func Measure(batch func(runs int) (time.Duration, error)) (time.Duration, error) {
	want := windowSteps * resolution()
	for runs := 1; ; {
		took, err := batch(runs)
		if err != nil {
			return took / time.Duration(runs), err
		}
		if took >= want {
			return took / time.Duration(runs), nil
		}
		if took <= 0 {
			runs *= 10
			continue
		}
		// Aim a quarter past the window, so a run that takes a little longer than this one's
		// average still lands the next window past it.
		runs = max(runs*2, int(int64(runs)*int64(want)*5/(int64(took)*4))+1)
	}
}
