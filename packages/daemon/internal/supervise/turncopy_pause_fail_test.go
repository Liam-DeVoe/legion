package supervise

// A failure of the claim while a project pause's stop is held over the copy its turn's end started
// (R1 of dispatch://INFRA-1117). A pause's stop held for a turn's end waits first for the prompt's
// answer (stopWaitsForResult), and begins its wait for the copy only at the end of that answer's
// row (stopAfterAnswer, stopAtResult), so the decisions the result and the turn's report make
// before it — a provider error that holds the issue, a run of refused turns at the launch-failure
// limit — meet a stop that is held but not yet waiting. Each would fail the claim and stop the
// process the copy still asks; each leaves the stop to the copy's end instead
// (stopHeldOverTurnCopy), as turncopy_fail_test.go pins for a daemon's stop.

import (
	"errors"
	"testing"
	"time"
)

// pauseHeldForTheResult is a project pause's stop held over the copy of a turn's end before its
// wait for that copy has begun: a claim in a turn of a task a provider will refuse, whose project's
// pause holds its stop for the turn's end, and whose turn has ended (turnEndedUnderAHold) with the
// stop still waiting for the prompt's result (stopWaitsForResult) and no process stopped. copies is
// the claim's CopyTurnEnd, and the turn's end started one copy, still being taken; nil gives the
// claim one that starts no copy. tune, where it is not nil, changes the rest of the Deps before the
// machine is built, and beforeTurn is turnEndedUnderAHold's, handed the harness. It answers the
// delivery the result names.
func pauseHeldForTheResult(t *testing.T, copies *turnEndCopies, tune func(*Deps), beforeTurn ...func(*harness) StreamTurnStart) (*harness, string) {
	t.Helper()
	copyTurnEnd := func(Claim, TurnEndCopied) (time.Duration, bool) { return 0, false }
	if copies != nil {
		copyTurnEnd = copies.start
	}
	h, flag, _ := pauseHarness(t, queuedClaim(), func(d *Deps) {
		d.CopyTurnEnd = copyTurnEnd
		if tune != nil {
			tune(d)
		}
	})
	steps := make([]func() StreamTurnStart, 0, len(beforeTurn))
	for _, step := range beforeTurn {
		steps = append(steps, func() StreamTurnStart { return step(h) })
	}
	task := turnEndedUnderAHold(t, h, copies, pausesStop(t, h, flag), steps...)
	if n := processStops(h); n != 0 {
		t.Fatalf("the process was stopped %d times at the turn's end, want none while the pause's stop waits for the prompt's result", n)
	}
	return h, task
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

// suspendedOnceAfterTheCopy is stopsOnceAfterTheCopy held to the stop that waited: once the copy
// ended, the claim is suspended by by, the source of the stop held over it — the pause's
// (SuspendedByReleased) for a project pause's stop, the daemon's for holdStop's. A claim failed at
// the copy's end, which stopsOnceAfterTheCopy lets through, fails here.
func suspendedOnceAfterTheCopy(t *testing.T, h *harness, copies *turnEndCopies, by SuspendSource) {
	t.Helper()
	stopsOnceAfterTheCopy(t, h, copies)
	if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != by {
		t.Fatalf("the claim is %s by %q once the copy ended, want it suspended by the held stop's source %q", c.State, c.SuspendedBy, by)
	}
}

// The prompt's result behind the turn's end carries a provider error that holds the issue (a 400),
// which fails the claim. The pause's stop is held, its wait for the copy not yet begun. The
// failure leaves the stop to the copy, and the pause's stop then begins its wait at this result;
// the copy's end runs it, and the claim is suspended by the pause.
//
// The control is the guard narrowed to a wait that has begun (turnCopy.stopWaits): the claim is
// then failed at the result, its process stopped while the copy is still being taken.
func TestAProviderErrorThatFailsTheClaimWaitsForTheTurnsCopyAPausesHeldStopIsOver(t *testing.T) {
	copies := &turnEndCopies{}
	h, task := pauseHeldForTheResult(t, copies, nil)

	if err := h.handle(StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: true,
		Error: "prompt is too long", HTTPStatus: 400}); err != nil {
		t.Logf("the prompt's result answered %v", err)
	}

	suspendedOnceAfterTheCopy(t, h, copies, SuspendedByReleased)
}

// The turn's reports reach the launch-failure limit of refused turns before the prompt's result
// comes, while the pause's stop is held and its wait for the copy not yet begun. The failure
// leaves the stop to the copy; the result, a provider error the ladder retries, then begins the
// pause's wait, and the copy's end runs the stop.
//
// The control is the guard narrowed to a wait that has begun (turnCopy.stopWaits): the claim is
// then failed at the last report, its process stopped while the copy is still being taken.
func TestARefusedTurnRunThatFailsTheClaimWaitsForTheTurnsCopyAPausesHeldStopIsOver(t *testing.T) {
	copies := &turnEndCopies{}
	h, task := pauseHeldForTheResult(t, copies, nil)

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

	suspendedOnceAfterTheCopy(t, h, copies, SuspendedByReleased)
}
