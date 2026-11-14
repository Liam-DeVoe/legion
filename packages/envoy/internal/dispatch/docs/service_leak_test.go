package docs

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/sjawhar/envoy/internal/testwait"
)

// The leak registry answers one question this package's tests cannot otherwise ask: when the test
// binary ends, how many of the services its tests built are still alive? A test that builds a
// service and never shuts it down keeps that service - and every room it loaded, with that room's
// document in memory - for the rest of the binary's run, which is the memory this check exists to
// catch. The registry holds each service through a weak pointer, so it pins nothing itself: what
// stays alive stays alive because a test left it so.

// builtService is one service New built, named by the test that built it, and whether its own
// Shutdown has reported that it finished (shutdownServiceHook).
type builtService struct {
	service  weak.Pointer[Service]
	test     string
	shutDown *atomic.Bool
}

var (
	builtServicesMu sync.Mutex
	builtServices   []builtService
)

// New and Shutdown run from tests that run in parallel, so the hooks work under the mutex.
func init() {
	newServiceHook = recordBuiltService
	shutdownServiceHook = recordShutDownService
}

func recordBuiltService(service *Service) {
	entry := builtService{service: weak.Make(service), test: buildingTest(), shutDown: &atomic.Bool{}}
	builtServicesMu.Lock()
	defer builtServicesMu.Unlock()
	builtServices = append(builtServices, entry)
}

// recordShutDownService marks the service whose Shutdown has just finished.
func recordShutDownService(service *Service) {
	builtServicesMu.Lock()
	defer builtServicesMu.Unlock()
	for _, entry := range builtServices {
		if entry.service.Value() == service {
			entry.shutDown.Store(true)
			return
		}
	}
}

// buildingTest names the nearest frame of the call to New that belongs to a test of this package:
// the test to fix when the service it built is still alive at the end of the run. Helpers of this
// package that build a service for a test are skipped, so the name is the test's own.
func buildingTest() string {
	var pcs [64]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		frame, more := frames.Next()
		if name := frame.Function; strings.Contains(name, "docs.Test") {
			if cut := strings.LastIndex(name, "/"); cut >= 0 {
				name = name[cut+1:]
			}
			return name
		}
		if !more {
			return "an unnamed caller"
		}
	}
}

// liveServices reports how many services the tests built, and names the ones a weak pointer still
// resolves, in the order they were built: those whose Shutdown never finished, and those whose
// Shutdown finished and which something still holds.
func liveServices() (built int, neverShutDown, shutDownButHeld []string) {
	builtServicesMu.Lock()
	defer builtServicesMu.Unlock()
	for _, entry := range builtServices {
		if entry.service.Value() == nil {
			continue
		}
		if entry.shutDown.Load() {
			shutDownButHeld = append(shutDownButHeld, entry.test)
		} else {
			neverShutDown = append(neverShutDown, entry.test)
		}
	}
	return len(builtServices), neverShutDown, shutDownButHeld
}

// collect forces two collections: the first can leave a service reachable from a finalizer it
// queued.
func collect() {
	runtime.GC()
	runtime.GC()
}

// reportLeakedServices prints to stderr the services the tests built that are still alive once the
// tests have run, and reports whether there is one. It fails the run for one: TestMain
// (persistence_test.go) turns a true here into a non-zero exit code when the tests themselves
// passed. A service whose own Shutdown never reported that it finished (shutdownServiceHook) was
// built by a test that never shut it down, so that service, its rooms and their documents stayed in
// memory for the rest of the binary's life; it is reported at once, and the lines after the count
// name the tests to fix, each by a t.Cleanup that shuts the service it built down. A service whose
// Shutdown did finish can still be reachable for a moment while the goroutines that shutdown ended
// unwind, so the report waits for it to be collected through the shared wait (testwait.InMain),
// forcing collections only to read that, and reports it if the wait gives up.
func reportLeakedServices() bool {
	collect()
	built, neverShutDown, held := liveServices()
	if len(held) > 0 {
		if err := testwait.InMain("the services the tests shut down to be collected", func() bool {
			collect()
			_, _, held = liveServices()
			return len(held) == 0
		}); err != nil {
			fmt.Fprintln(os.Stderr, "docs:", err)
		}
	}
	fmt.Fprintf(os.Stderr, "docs: %d services built, %d never shut down, %d shut down and still alive\n",
		built, len(neverShutDown), len(held))
	for _, test := range neverShutDown {
		fmt.Fprintf(os.Stderr, "docs: a service built by %s was never shut down and is still alive\n", test)
	}
	for _, test := range held {
		fmt.Fprintf(os.Stderr, "docs: a service built by %s was shut down and is still alive\n", test)
	}
	if len(neverShutDown) == 0 && len(held) == 0 {
		return false
	}
	fmt.Fprintln(os.Stderr, "docs: a service still alive fails this run")
	return true
}
