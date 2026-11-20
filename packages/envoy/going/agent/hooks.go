package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sjawhar/envoy/agentloop"
)

// The agent's test hooks (Config.Hooks), which an end-to-end test of the agent process drives
// through the fake chat model's replies: the server hands them on to its agent only under its own
// test hooks (cmd/dispatch: DISPATCH_TEST_AGENT_HOOKS), never in a deployment. Each stands in for
// a defect the agent must outlive.
const (
	// hookHang is a tool that ignores its context and never returns: a stuck tool.
	hookHang = "test_hang"
	// hookPanic is a tool that panics on the model loop's own goroutine, which agentloop recovers.
	hookPanic = "test_panic"
	// hookCrash is a tool that panics on a goroutine of its own, which nothing recovers: a panic
	// past the run's recovery, which ends the agent process.
	hookCrash = "test_crash"
	// hookHangText is prose that, streamed by the model, makes the run's OnText ignore its
	// context and never return: a model stream that ignores its context.
	hookHangText = "[[test_hang]]"
)

// hookTools are the test hooks' tools in the step whose model call is numbered base, each run
// through the worker's toolRun as every tool of a run is.
func (w *worker) hookTools(base int) []agentloop.Tool {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	tool := func(name, description string, run func()) agentloop.Tool {
		return agentloop.Tool{Name: name, Description: description, Schema: empty,
			Run: w.toolRun(base, name, func(context.Context, int, json.RawMessage) (string, bool) {
				run()
				return "", false
			})}
	}
	return []agentloop.Tool{
		tool(hookHang, "A test hook: never returns.", func() { select {} }),
		tool(hookPanic, "A test hook: panics.", func() { panic("the test hook's own panic") }),
		tool(hookCrash, "A test hook: panics past every recover.", func() {
			go panic("the test hook's own panic, past every recover")
			select {}
		}),
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
