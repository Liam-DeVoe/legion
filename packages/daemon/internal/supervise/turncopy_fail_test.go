package supervise

// A failure of the claim while a held stop waits for the copy its turn's end started (R1 of
// dispatch://INFRA-1117). A provider error that holds the issue and a run of refused turns at the
// launch-failure limit each fail the claim, and a failure ends the claim's process (fail,
// endProcess): decided while the held stop waits, it would stop the process the copy still asks,
// as the seven paths of turncopy_replace_test.go and turncopy_prompt_test.go would.

import "testing"

// processStops counts the runtime's stops and releases of the claim's process.
func processStops(h *harness) int {
	return len(h.rt.CallsOf("Suspend")) + len(h.rt.CallsOf("Release"))
}

// stopsOnceAfterTheCopy hands back the end of the index-th copy the claim's turn ends started, and
// fails unless the held stop stopped the claim's process once at that end, as stopsOnceAt holds.
func stopsOnceAfterTheCopy(t *testing.T, h *harness, copies *turnEndCopies, index int, by SuspendSource) {
	t.Helper()
	stopsOnceAt(t, h, by, "the copy ended", func() { h.must(copies.copied(t, index)) })
}

// stopsOnceAt runs over, what ends the hold of a stop held over the claim for its turn's copy (the
// copy's end, or its due passing), and fails unless nothing stopped the claim's process before it,
// the held stop stopped that process once at it, and the claim is then suspended by by, the source
// of the stop held over it: the pause's (SuspendedByReleased) for a project pause's stop, the
// daemon's for holdStop's. what names the moment in a failure.
func stopsOnceAt(t *testing.T, h *harness, by SuspendSource, what string, over func()) {
	t.Helper()
	if n := processStops(h); n != 0 {
		t.Fatalf("the process was stopped %d times while the held stop waited for the copy of its turn's end (state %s), want none before %s",
			n, h.claim().State, what)
	}
	loc := h.locator()
	over()
	if n := processStops(h); n != 1 {
		t.Fatalf("the process was stopped %d times once %s, want once", n, what)
	}
	if suspend := h.wantCalls("Suspend", 1)[0]; suspend.Locator != loc {
		t.Fatalf("suspended %+v once %s, want the process the claim recorded, %+v", suspend.Locator, what, loc)
	}
	if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != by {
		t.Fatalf("the claim is %s by %q once %s, want it suspended by the held stop's source %q", c.State, c.SuspendedBy, what, by)
	}
}

// turnEndedUnderAHold brings h's claim into a turn of a task a provider will refuse, has hold hold
// a stop over that turn, and ends the turn. copies is the claim's CopyTurnEnd where it is not nil,
// and the helper then fails unless the turn's end started one copy. beforeTurn, where one is
// given, runs between the task's delivery and the turn and answers the turn's start, which is
// otherwise one naming no delivery. It answers the delivery the prompt's result names.
func turnEndedUnderAHold(t *testing.T, h *harness, copies *turnEndCopies, hold func(), beforeTurn ...func(*harness) StreamTurnStart) string {
	t.Helper()
	h.reach(StateReady)
	h.must(RequestDeliver{Claim: h.token, Task: providerErrorTaskText})
	start := StreamTurnStart{Claim: h.token}
	for _, step := range beforeTurn {
		start = step(h)
	}
	h.must(start)
	task := h.pending().ID
	hold()
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if copies != nil {
		if started := copies.started(); len(started) != 1 {
			t.Fatalf("the turn's end started %d copies, want one", len(started))
		}
	}
	return task
}

// The turn of a task ends into a held daemon stop, which waits for the turn's copy, and the
// prompt's result comes behind the end, as Oh My Pi sends it: the turn ended in a provider error
// that holds the issue (a 400, a request too long for the model), which fails the claim. Failed
// there, the claim's process is stopped while the copy still asks its shim, the copy fails, and
// the turn's end is in no copy once the role is failed. The failure waits for the copy as the
// held stop does.
//
// The negative control is the code at 7e5fbdc9, where providerErrored fails the claim at once:
// the process is then stopped at the result, before the copy ends.
func TestAProviderErrorThatFailsTheClaimWaitsForTheTurnsCopyItsHeldStopWaitsFor(t *testing.T) {
	h, copies := copyingHarness(t)
	task := turnEndedUnderAHold(t, h, copies, func() { holdStop(t, h, "LEGION-208 left implementing") })

	if err := h.handle(holdingProviderError(h, task)); err != nil {
		t.Logf("the prompt's result answered %v", err)
	}

	stopsOnceAfterTheCopy(t, h, copies, 0, SuspendedByDaemon)
}

// A run of refused turns at the launch-failure limit fails the claim (turnOutcome). The plugin
// reports each turn's outcome at its end, so the report that reaches the limit can come while a
// held stop waits for that turn's copy. Failed there, the claim's process is stopped while the
// copy still asks its shim. The failure waits for the copy as the held stop does.
//
// The negative control is the code at 7e5fbdc9, where turnOutcome fails the claim at once: the
// process is then stopped at the report, before the copy ends.
func TestARefusedTurnRunThatFailsTheClaimWaitsForTheTurnsCopyItsHeldStopWaitsFor(t *testing.T) {
	h, copies, _ := copyWaiting(t)

	refuseTurnsToTheLimit(t, h)

	stopsOnceAfterTheCopy(t, h, copies, 0, SuspendedByDaemon)
}
