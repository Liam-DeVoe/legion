package supervise

// A prompt result whose agent ran no turn, behind the turn's end a project pause's stop was held
// for. That stop waits for the prompt's result (stopWaitsForResult), and the result that says no
// turn ran is charged as a failed prompt (promptFailed, chargePrompt). Before that result ran the
// pause's stop (stopAtResult), nothing else did: the turn was over, so no turn's end was coming,
// and the copy's end runs only a stop whose wait for it has begun (endTurnCopyWait). The pause
// then never stopped the role.

import (
	"errors"
	"testing"
	"time"
)

// pauseHeldForANoTurnResult is a claim in a turn of a task, whose project's pause holds its stop
// for the turn's end, and whose turn has ended with the stop still waiting for the prompt's
// result. copyTurnEnd is the CopyTurnEnd the claim's machine is given. It answers the delivery that
// result names.
func pauseHeldForANoTurnResult(t *testing.T, copyTurnEnd func(Claim, TurnEndCopied) (time.Duration, bool), limits Limits) (*harness, string) {
	t.Helper()
	h, flag, _ := pauseHarness(t, queuedClaim(), func(d *Deps) { d.CopyTurnEnd, d.Limits = copyTurnEnd, limits })
	h.reach(StateReady)
	h.must(RequestDeliver{Claim: h.token, Task: providerErrorTaskText})
	h.must(StreamTurnStart{Claim: h.token})
	task := h.pending().ID
	flag.set(7)
	if err := h.handle(pauseStop(h.token, 7)); !errors.Is(err, ErrSuspendHeld) {
		t.Fatalf("the pause's stop of a claim in a turn = %v, want it held", err)
	}
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if n := processStops(h); n != 0 {
		t.Fatalf("the process was stopped %d times at the turn's end, want none while the pause's stop waits for the prompt's result", n)
	}
	return h, task
}

// noTurnResult is the prompt's result for task saying its agent ran no turn.
func noTurnResult(h *harness, task string) StreamPromptResult {
	return StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: false, Error: "the agent ran no turn"}
}

// Below the prompt-failure limit, with no copy started at the turn's end, the result is charged
// and the pause's stop then runs, once: the claim is suspended by the pause.
//
// The control is promptResulted without the stopAtResult after promptFailed: the claim is then
// left idle holding the pause's stop, and the test fails at the first stop it wants.
func TestANoTurnResultBelowThePromptLimitRunsThePausesHeldStop(t *testing.T) {
	none := func(Claim, TurnEndCopied) (time.Duration, bool) { return 0, false }
	h, task := pauseHeldForANoTurnResult(t, none, testLimits())

	if err := h.handle(noTurnResult(h, task)); err != nil {
		t.Logf("the prompt's result answered %v", err)
	}

	if n := processStops(h); n != 1 {
		t.Fatalf("the process was stopped %d times after the no-turn result (state %s), want the pause's stop once", n, h.claim().State)
	}
	if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != SuspendedByReleased {
		t.Fatalf("the claim is %s by %q after the no-turn result, want it suspended by the pause", c.State, c.SuspendedBy)
	}
	if c := h.claim(); c.Budgets.PromptFailures != 1 {
		t.Fatalf("the no-turn result charged %d prompt failures, want one", c.Budgets.PromptFailures)
	}
}

// At the prompt-failure limit, with the copy of the turn's end still being taken, the failure is
// charged and its retirement left to the pause's stop (chargePrompt), and the result begins that
// stop's wait for the copy: no stop before the copy ends, one after it.
//
// The control is promptResulted without the stopAtResult after promptFailed: no wait for the copy
// begins, the copy's end runs nothing, and the test fails at the stop it wants once the copy ended.
func TestANoTurnResultAtThePromptLimitWaitsForTheTurnsCopyAPausesHeldStopIsOver(t *testing.T) {
	copies := &turnEndCopies{}
	limits := testLimits()
	limits.PromptFailures = 1
	h, task := pauseHeldForANoTurnResult(t, copies.start, limits)
	if started := copies.started(); len(started) != 1 {
		t.Fatalf("the turn's end started %d copies, want one", len(started))
	}

	if err := h.handle(noTurnResult(h, task)); err != nil {
		t.Logf("the prompt's result answered %v", err)
	}

	stopsOnceAfterTheCopy(t, h, copies)
}
