package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sjawhar/envoy/agentloop"
)

// The agent's test hooks (Config.Hooks), which an end-to-end test of the agent process drives
// through the fake chat model's replies: the server hands them on to its agent only under its own
// test hooks (cmd/dispatch: DISPATCH_TEST_AGENT_HOOKS), never in a deployment. Each stands in for
// a defect the agent must outlive, or for a load it must carry beside its server.
const (
	// hookHang is a tool that ignores its context and never returns: a stuck tool.
	hookHang = "test_hang"
	// hookPanic is a tool that panics on the model loop's own goroutine, which agentloop recovers.
	hookPanic = "test_panic"
	// hookCrash is a tool that panics on a goroutine of its own, which nothing recovers: a panic
	// past the run's recovery, which ends the agent process.
	hookCrash = "test_crash"
	// hookSoftLimit is a tool that works the agent at its soft limit until the run's context ends
	// (workAtSoftLimit): the load of check c29, which the agent's CPUs and priority bound.
	hookSoftLimit = "test_soft_limit"
	// hookHangText is prose that, streamed by the model, makes the run's OnText ignore its
	// context and never return: a model stream that ignores its context.
	hookHangText = "[[test_hang]]"
)

// hookTools are the test hooks' tools in the step whose model call is numbered base, each run
// through the worker's toolRun as every tool of a run is.
func (w *worker) hookTools(base int) []agentloop.Tool {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	tool := func(name, description string, run func(ctx context.Context)) agentloop.Tool {
		return agentloop.Tool{Name: name, Description: description, Schema: empty,
			Run: w.toolRun(base, name, func(ctx context.Context, _ int, _ json.RawMessage) (string, bool) {
				run(ctx)
				return "", false
			})}
	}
	return []agentloop.Tool{
		tool(hookHang, "A test hook: never returns.", func(context.Context) { select {} }),
		tool(hookPanic, "A test hook: panics.", func(context.Context) { panic("the test hook's own panic") }),
		tool(hookCrash, "A test hook: panics past every recover.", func(context.Context) {
			go panic("the test hook's own panic, past every recover")
			select {}
		}),
		tool(hookSoftLimit, "A test hook: works the agent at its soft limit until the run ends.", workAtSoftLimit),
	}
}

// hookOnText is the run's OnText under the test hooks: it never returns once the reply's prose
// holds hookHangText.
func hookOnText(delta string) error {
	if strings.Contains(delta, hookHangText) {
		select {}
	}
	return nil
}

// softLimitNode is one object of the soft-limit hook's heap, 64 bytes: a pointer, which each
// collection follows, and the rest.
type softLimitNode struct {
	next *softLimitNode
	_    [56]byte
}

// softLimitGarbage is where the soft-limit hook's goroutines put each chain they allocate, which
// the next chain replaces, so each one reaches the heap and is garbage at once.
var softLimitGarbage atomic.Pointer[softLimitNode]

// workAtSoftLimit works the agent at its soft limit until ctx ends: it holds a live heap of
// pointers, which each collection marks, of nine tenths of the process's memory limit
// (GOMEMLIMIT), and allocates more of the same without pause on one goroutine for each CPU the
// runtime schedules on (GOMAXPROCS), so the collector runs at the limit the whole time. With no
// limit set it holds nothing and waits for ctx. What it held is garbage once it returns.
func workAtSoftLimit(ctx context.Context) {
	limit := debug.SetMemoryLimit(-1)
	if limit == math.MaxInt64 {
		slog.Warn("agent: " + hookSoftLimit + ": the agent runs with no memory limit, so the hook holds nothing")
		<-ctx.Done()
		return
	}
	var held *softLimitNode
	for range limit * 9 / 10 / 64 {
		held = &softLimitNode{next: held}
	}
	procs := runtime.GOMAXPROCS(0)
	slog.Info("agent: "+hookSoftLimit+": the agent works at its soft limit", "limit", limit, "held", limit*9/10, "goroutines", procs)
	var allocating sync.WaitGroup
	for range procs {
		allocating.Go(func() {
			for ctx.Err() == nil {
				var chain *softLimitNode
				for range 1024 {
					chain = &softLimitNode{next: chain}
				}
				softLimitGarbage.Store(chain)
			}
		})
	}
	allocating.Wait()
	runtime.KeepAlive(held)
}
