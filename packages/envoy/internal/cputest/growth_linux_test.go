package cputest

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// spinSteps is how many steps of userWork the smaller window of the test below takes: about 25 ms
// on a core of a few GHz, so the window of scale of them lasts 50 ms or more.
const spinSteps = 25_000_000

// faultedBytes is how much fresh memory faultPages writes to, one byte in each page, and
// faultRounds how many times it does that: about 300 ms of the kernel's work on the thread, several
// times the user work of the smaller window.
const (
	faultedBytes = 256 << 20
	faultRounds  = 2
)

// spinSink keeps the compiler from dropping the work of userWork.
var spinSink uint64

// userWork is steps of xorshift, work in user mode that makes no system call and allocates nothing.
func userWork(steps int) {
	state := uint64(88172645463325252)
	for range steps {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
	}
	spinSink += state
}

// faultPages maps faultedBytes of fresh memory faultRounds times, in small pages, and writes one
// byte in each page: the kernel finds and zeroes a page at each write, the work that it does, with
// the reclaim of a page first, when a machine or a cgroup is short of memory.
func faultPages(t *testing.T) {
	for range faultRounds {
		memory, err := unix.Mmap(-1, 0, faultedBytes, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
		if err != nil {
			t.Fatalf("mmap %d bytes: %v", faultedBytes, err)
		}
		if err := unix.Madvise(memory, unix.MADV_NOHUGEPAGE); err != nil {
			t.Fatalf("madvise MADV_NOHUGEPAGE: %v", err)
		}
		for page := 0; page < len(memory); page += unix.Getpagesize() {
			memory[page] = 1
		}
		if err := unix.Munmap(memory); err != nil {
			t.Fatalf("munmap: %v", err)
		}
	}
}

// Growth counts the work of the caller's own code, and not the kernel's work for the caller's
// thread. The larger window does scale times the user work of the smaller one, and it also faults
// fresh pages, which is kernel work several times the user work of the smaller window. Its growth
// stays near scale. The control: the faults take more of the thread's whole CPU time (OfCall) than
// the smaller window takes, so a measure that counted them would read a growth of more than twice
// scale.
func TestGrowthLeavesOutTheKernelsWorkForTheThread(t *testing.T) {
	const scale, samples = 4, 3
	growth, small, large := Growth(samples, scale,
		func() { userWork(spinSteps) },
		func() {
			userWork(scale * spinSteps)
			faultPages(t)
		})
	var kernel time.Duration
	for sample := range samples {
		if took := OfCall(func() { faultPages(t) }); sample == 0 || took < kernel {
			kernel = took
		}
	}
	t.Logf("smaller window %v, larger window %v, %.1f times; the faults alone on the thread's whole CPU time %v", small, large, growth, kernel)
	if small < 50*time.Millisecond {
		t.Fatalf("the smaller window lasts %v, under the 50 ms that the measure needs", small)
	}
	if kernel < small {
		t.Fatalf("the faults took %v of the thread's CPU time, less than the smaller window's %v, so this test reads nothing", kernel, small)
	}
	if growth >= 1.5*scale {
		t.Errorf("the work grew %.1f times, from %v for %d runs to %v for one run of %d times the work and the faults, want less than %.0f times",
			growth, small, scale, large, scale, 1.5*scale)
	}
}
