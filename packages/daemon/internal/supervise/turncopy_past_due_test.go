package supervise

// Past the due of the copy its turn's end started (R1 of dispatch://INFRA-1117), a stop held over
// the claim is held for that copy no more (turnCopyHolds): each guard that left the stop to the copy
// does what it does where no copy is taken. These are the rows past the due in a project pause's
// window, where the stop still waits for the prompt's answer and arms no bound of its own, of the
// six guards whose rows inside the budget are in turncopy_replace_test.go,
// turncopy_pause_fail_test.go and pause_noturn_result_test.go. The stall backstop's and the parked
// wake's rows past the due are with theirs, in turncopy_replace_test.go.

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// failsOnceAt runs over, and fails unless nothing stopped the claim's process before it, over
// stopped it once, and the claim is then failed: what a failure does where no copy is taken (fail).
// what names the moment in a failure.
func failsOnceAt(t *testing.T, h *harness, what string, over func()) {
	t.Helper()
	if n := processStops(h); n != 0 {
		t.Fatalf("the process was stopped %d times before %s, want none", n, what)
	}
	over()
	if n := processStops(h); n != 1 {
		t.Fatalf("the process was stopped %d times once %s, want once", n, what)
	}
	if c := h.claim(); c.State != StateFailed {
		t.Fatalf("the claim is %s once %s, want it failed", c.State, what)
	}
}

// Each row's claim is in a pause's window (pauseHeldForTheResult), and the clock reaches the due of
// the copy its turn's end started with that copy still unreported; the guard's event then comes,
// and the guard acts as it does where no copy is taken, leaving the claim holding no stop.
//
// The control is turnCopyHolds without the due: each row's guard then leaves the stop to the copy
// past its due, and the row fails at what it wants.
func TestEachGuardActsPastTheTurnsCopysDueAsWhereNoCopyIsTaken(t *testing.T) {
	for _, tc := range []struct {
		name string
		// tune changes the Deps before the machine is built, where it is not nil.
		tune func(*Deps)
		// act brings the guard its event, and fails unless what followed is what the guard does
		// where no copy is taken. task is the delivery the prompt's answer names.
		act func(t *testing.T, h *harness, task string)
	}{
		// A prompt failure at the limit while a stop is held is that stop (chargePrompt).
		{name: "APromptFailureAtTheLimitRunsTheStop", tune: func(d *Deps) { d.Limits.PromptFailures = 1 },
			act: func(t *testing.T, h *harness, task string) {
				stopsOnceAt(t, h, SuspendedByReleased, "the no-turn result came", func() {
					if err := h.handle(noTurnResult(h, task)); err != nil {
						t.Logf("the prompt's result answered %v", err)
					}
				})
			}},
		// A provider error that holds the issue fails the claim (providerErrored,
		// providerErrorTurnEnded).
		{name: "AProviderErrorThatHoldsTheIssueFailsTheClaim", act: func(t *testing.T, h *harness, task string) {
			failsOnceAt(t, h, "the provider error came", func() {
				if err := h.handle(holdingProviderError(h, task)); err != nil {
					t.Logf("the prompt's result answered %v", err)
				}
			})
		}},
		// A stale-plugin relaunch replaces the process, which ends the held stop (refreshPlugin,
		// replaceNow, endHeld).
		{name: "AStalePluginRelaunchEndsTheStop", act: func(t *testing.T, h *harness, _ string) {
			speaksAnOlderContract(h)
			stopsOnceAt(t, h, SuspendedByReleased, "the stale-plugin sweep posted the claim", func() {
				h.must(StalePlugin{Claim: h.token, Trigger: "sweep", Credit: h.m.deps.RefreshGate.Admit()})
			})
		}},
		// A registration on another plugin contract over a claim whose relaunch for this contract
		// was already made is charged a launch failure and its relaunch raised, and is answered
		// that the daemon is relaunching it (staleRegistration, relaunchAfterFailure). The project
		// is paused, so its gate holds that launch and stops the claim for the pause, as it does
		// any launch of a paused project (reserveWake, pauseGate, pauseStopDue).
		{name: "AStaleRegistrationIsChargedAndItsRelaunchRaised", act: func(t *testing.T, h *harness, _ string) {
			charged := h.claim().Budgets.LaunchFailures

			stale, err := registerOnTheOlderContract(h)

			want := claim.StalePluginContract(stale, refreshContract).Message
			var refusal claim.Refusal
			if !errors.As(err, &refusal) || refusal.Status != http.StatusConflict || refusal.Message != want {
				t.Fatalf("the registration on another plugin contract was answered %v, want a 409 saying %q", err, want)
			}
			if got := h.claim().Budgets.LaunchFailures; got != charged+1 {
				t.Fatalf("the registration charged %d launch failures, want one", got-charged)
			}
			stopsOnceAt(t, h, SuspendedByReleased, "the pause's gate stopped the claim its relaunch was held for", func() {
				h.advance(0)
			})
		}},
		// A process found at a stale address is replaced, which ends the held stop (judge,
		// repoint, endHeld).
		{name: "AStaleAddressEndsTheStop", act: func(t *testing.T, h *harness, _ string) {
			stopsOnceAt(t, h, SuspendedByReleased, "the process was found at a stale address", func() {
				h.observe(runtime.StaleAddress)
			})
		}},
		// The agent's ready said again runs a held stop (reready).
		{name: "AReadySaidAgainRunsTheStop", act: func(t *testing.T, h *harness, _ string) {
			stopsOnceAt(t, h, SuspendedByReleased, "the agent said ready again", func() {
				h.must(RequestReady{Claim: h.token, Generation: h.generation(), Session: session})
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, task := pauseHeldPastTheCopysDue(t, tc.tune)
			tc.act(t, h, task)
		})
	}
}

// pauseHeldPastTheCopysDue is pauseHeldForTheResult's claim with the clock moved to the due of
// the copy its turn's end started, that copy still unreported: idle, holding the pause's stop,
// which still waits for the prompt's answer. It answers the delivery the answer names.
func pauseHeldPastTheCopysDue(t *testing.T, tune func(*Deps)) (*harness, string) {
	t.Helper()
	h, task := pauseHeldForTheResult(t, &turnEndCopies{}, tune)
	h.advance(turnCopyDue(t, h).Sub(h.clock.Now()))
	turnCopyDue(t, h)
	if c := h.claim(); c.State != StateIdle || !c.SuspensionHeld {
		t.Fatalf("the claim at the copy's due is %s holding a stop = %t, want it idle, holding the pause's stop", c.State, c.SuspensionHeld)
	}
	return h, task
}

// A pause's stop begins its wait for the copy of its turn's end at the prompt's answer
// (stopAfterAnswer, stopAtResult). Past that copy's due, with the copy still unreported, no wait
// begins (waitForTurnCopy): the stop runs in the answer's own decision, as it does where no copy is
// taken, and the claim is suspended by the pause. Nothing says the copy ran out of its budget,
// since no wait for it was cut off.
//
// The control is waitForTurnCopy without the check of the copy's budget: the wait then begins
// past the due on a bound armed for no time, the answer stops nothing, and the test fails there.
func TestAPausesStopAtThePromptsAnswerPastTheCopysDueRunsInThatDecision(t *testing.T) {
	h, task := pauseHeldPastTheCopysDue(t, nil)

	stopsOnceAt(t, h, SuspendedByReleased, "the prompt's answer came", func() {
		if err := h.handle(StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: true}); err != nil {
			t.Logf("the prompt's result answered %v", err)
		}
	})
	if lines := h.logs.lines("did not end within its budget"); len(lines) != 0 {
		t.Fatalf("the stop past the copy's due logged %q, want no line that the copy ran out of its budget", lines)
	}
}

// Past the due of the copy its turn's end started, a suspension of an idle claim is decided at
// once by its own asker (suspend, suspendNow), as where no copy is taken, even over a project
// pause's stop still held for the prompt's answer: nothing holds it for the copy, so it neither
// joins that stop nor logs a hold for the copy. A daemon's suspension would otherwise join the
// pause's hold, which it does not take over (holdRequest), and the claim would end suspended by the
// pause. An operator's takes any hold over, so it ends suspended by the operator either way; what
// tells the two apart for it is the lines a hold for the copy writes, which must not be written.
//
// The control is suspend's gate back to any copy in flight, whatever its due: the suspension then
// goes through the hold for the copy first. The daemon's row then fails at the source the claim is
// suspended by, and the operator's at the hold's lines.
func TestASuspensionPastTheCopysDueOverAPausesHeldStopIsDecidedByItsAsker(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  RequestSuspend
	}{
		{name: "ADaemonsSuspension", ask: RequestSuspend{By: SuspendedByDaemon, Reason: "the tree of LEGION-208 lingers"}},
		{name: "AnOperatorsSuspension", ask: RequestSuspend{By: SuspendedByOperator, Reason: "an operator's drain"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := pauseHeldPastTheCopysDue(t, nil)
			tc.ask.Claim = h.token

			stopsOnceAt(t, h, tc.ask.By, "the suspension came", func() {
				if err := h.handle(tc.ask); err != nil {
					t.Fatalf("the suspension past the copy's due = %v, want it decided now", err)
				}
			})
			for _, line := range []string{heldForTurnCopy.held, heldForTurnCopy.deadline, "takes the held suspension over"} {
				if lines := h.logs.lines(line); len(lines) != 0 {
					t.Fatalf("the suspension past the copy's due logged %q, want no hold of it for the copy", lines)
				}
			}
		})
	}
}
