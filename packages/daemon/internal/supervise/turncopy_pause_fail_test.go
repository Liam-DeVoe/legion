package supervise

// A failure of the claim while a project pause's stop is held over the copy its turn's end started
// (R1 of dispatch://INFRA-1117). A pause's stop held for a turn's end waits first for the prompt's
// result (stopWaitsForResult), and begins its wait for the copy only at that result
// (stopAtResult), so the decisions the result and the turn's report make before it — a provider
// error that holds the issue, a run of refused turns at the launch-failure limit — meet a stop that
// is held but not yet waiting. Each would fail the claim and stop the process the copy still asks;
// each leaves the stop to the copy's end instead (stopHeldOverTurnCopy), as turncopy_fail_test.go
// pins for a daemon's stop.

import (
	"errors"
	"testing"
)

// pauseHeldOverTurnCopy is a claim in a turn of a task a provider will refuse, whose project's
// pause holds its stop for the turn's end, and whose turn has ended (turnEndedUnderAHold): the
// copy of that end is being taken, and the stop waits for the prompt's result, which has not come.
// It answers the copies and the delivery the result names.
func pauseHeldOverTurnCopy(t *testing.T) (*harness, *turnEndCopies, string) {
	t.Helper()
	copies := &turnEndCopies{}
	h, flag, _ := pauseHarness(t, queuedClaim(), func(d *Deps) { d.CopyTurnEnd = copies.start })
	return h, copies, turnEndedUnderAHold(t, h, copies, pausesStop(t, h, flag))
}

// pausesStop is the hold of a project pause's stop over h's claim in its turn: the pause flag names
// pause 7 and the pause's stop row reaches the claim, which holds the stop for the turn's end.
func pausesStop(t *testing.T, h *harness, flag *pauseFlag) func() {
	return func() {
		t.Helper()
		flag.set(7)
		if err := h.handle(pauseStop(h.token, 7)); !errors.Is(err, ErrSuspendHeld) {
			t.Fatalf("the pause's stop of a claim in a turn = %v, want it held", err)
		}
	}
}

// The prompt's result behind the turn's end carries a provider error that holds the issue (a 400),
// which fails the claim. The pause's stop is held, its wait for the copy not yet begun. The
// failure leaves the stop to the copy, and the pause's stop then begins its wait at this result;
// the copy's end runs it.
//
// The control is the guard asking whether the wait has begun (stopWaitsForTurnCopy): the claim is
// then failed at the result, its process stopped while the copy is still being taken.
func TestAProviderErrorThatFailsTheClaimWaitsForTheTurnsCopyAPausesHeldStopIsOver(t *testing.T) {
	h, copies, task := pauseHeldOverTurnCopy(t)

	if err := h.handle(StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: true,
		Error: "prompt is too long", HTTPStatus: 400}); err != nil {
		t.Logf("the prompt's result answered %v", err)
	}

	stopsOnceAfterTheCopy(t, h, copies)
}

// The turn's reports reach the launch-failure limit of refused turns before the prompt's result
// comes, while the pause's stop is held and its wait for the copy not yet begun. The failure
// leaves the stop to the copy; the result, a provider error the ladder retries, then begins the
// pause's wait, and the copy's end runs the stop.
//
// The control is the guard asking whether the wait has begun (stopWaitsForTurnCopy): the claim is
// then failed at the last report, its process stopped while the copy is still being taken.
func TestARefusedTurnRunThatFailsTheClaimWaitsForTheTurnsCopyAPausesHeldStopIsOver(t *testing.T) {
	h, copies, task := pauseHeldOverTurnCopy(t)

	for sequence := uint64(1); sequence <= uint64(h.deps.Limits.LaunchFailures); sequence++ {
		if err := h.handle(RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: sequence,
			Refused: true, Message: "the provider refused the request"}); err != nil {
			t.Logf("the turn outcome %d answered %v", sequence, err)
		}
	}
	if err := h.handle(StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: true,
		Error: "overloaded_error", HTTPStatus: 529}); err != nil {
		t.Logf("the prompt's result answered %v", err)
	}

	stopsOnceAfterTheCopy(t, h, copies)
}
