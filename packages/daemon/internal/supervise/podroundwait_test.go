package supervise

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// A start the runtime refuses because the live pod it would keep carries no exchange identity and
// the deployment names no transition token (runtime.ErrAwaitingPodRound) is the pod's state, not
// the claim's failing: the claim keeps its session, its failure budget stays as it was, and the
// same session is started again a minute later, and every minute after that until the pod's update
// round has replaced the pod. A round can take hours, so the wait logs one line, naming the pod, at
// the refusal that begins it, and none at the retries of it; a refusal after a start that went
// through begins a new wait, which logs its own line.
func TestAWaitForAPodRoundIsNotChargedToTheClaim(t *testing.T) {
	const pod = "legion-legion-legion-208"
	h := newBareHarness(t)
	h.store.PutClaim(h.ctx, queuedClaim())
	h.start(queuedClaim())
	h.reach(StateIdle)
	waiting := fmt.Errorf("launch %s: %w: pod %s carries no exchange identity and dispatch_token_transition_file is not set; its update round replaces it",
		testToken, runtime.ErrAwaitingPodRound, pod)
	h.rt.OnResume = func(context.Context) error { return waiting }
	wantWaitLines := func(want int, when string) {
		t.Helper()
		lines := h.records.of(podRoundWaitLine)
		if len(lines) != want {
			t.Fatalf("%s the wait for the pod's round logged %d lines, want %d", when, len(lines), want)
		}
		for _, line := range lines {
			if logged, _ := line.attr("error"); !strings.Contains(logged.String(), "pod "+pod) {
				t.Errorf("the wait's line carries the error %q, want the refusal naming the pod", logged)
			}
		}
	}

	// The death's relaunch reports the refusal to its caller, which is how a start that nothing
	// charged is still visible as a failure.
	if err := h.handle(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now()}}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the death's relaunch answered %v, want the runtime's wait for the pod's round", err)
	}
	h.wantState(StateLaunching)
	h.wantBudgets(Budgets{LaunchFailures: 1}) // the death's charge alone: the wait costs nothing
	c := h.claim()
	if c.Session != session || c.SessionFile != sessionFile {
		t.Fatalf("claim %+v after the refusal, want its session kept", c)
	}
	if !strings.Contains(c.LastStartFailure, "pod "+pod) || !strings.Contains(c.LastStartFailure, "update round") {
		t.Errorf("the claim records the refused start as %q, want the refusal naming the pod and its round", c.LastStartFailure)
	}
	wantWaitLines(1, "at the refusal that began it,")

	// A minute later the same session is started again, and the second refusal is charged nothing
	// either, and is the same wait, which logs nothing more.
	h.advance(podRoundRetry)
	h.wantCalls("Resume", 2)
	h.wantState(StateLaunching)
	h.wantBudgets(Budgets{LaunchFailures: 1})
	if resumed := h.calls("Resume")[1]; resumed.Spec.ResumeSessionFile != sessionFile {
		t.Errorf("the retry is %+v, want the same session %s", resumed.Spec, sessionFile)
	}
	wantWaitLines(1, "after two refusals in a row,")

	// Once the round has replaced the pod, the next minute's start goes through, on that session,
	// with the budget still as the death left it.
	h.rt.OnResume = nil
	h.advance(podRoundRetry)
	h.wantCalls("Resume", 3)
	h.wantBudgets(Budgets{LaunchFailures: 1})
	if c := h.claim(); c.Locator == nil || c.SessionFile != sessionFile {
		t.Errorf("claim %+v after the round, want it running its own session again", c)
	}
	wantWaitLines(1, "once a start went through,")

	// A refusal after that start is a new wait, which logs a line of its own, and its own retry
	// logs nothing more.
	h.rt.OnResume = func(context.Context) error { return waiting }
	if err := h.handle(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now()}}); !errors.Is(err, runtime.ErrAwaitingPodRound) {
		t.Fatalf("the second death's relaunch answered %v, want the runtime's wait for the pod's round", err)
	}
	wantWaitLines(2, "at a refusal after a start that went through,")
	h.advance(podRoundRetry)
	h.wantCalls("Resume", 5)
	wantWaitLines(2, "after that new wait's retry,")
	h.wantBudgets(Budgets{LaunchFailures: 2}) // the two deaths' charges: neither wait costs anything
}
