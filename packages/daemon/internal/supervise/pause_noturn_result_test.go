package supervise

// An answer of the prompt that says its agent ran no turn, behind the turn's end a project pause's
// stop was held for. That stop waits for the prompt's result (stopWaitsForResult), and each of the
// answers that end that wait with no turn run is a decision of its own: the result that says no
// turn ran and a late refusal with no turn running are charged as a failed prompt (promptFailed,
// chargePrompt), and the answer of a prompt whose wait for its turn had already run out takes back
// the confirmation that turn took for it (waitedOutAnswer). Before each ran the pause's stop
// (stopAtResult), nothing else did: the turn was over, so no turn's end was coming, and the copy's
// end runs only a stop whose wait for it has begun (endTurnCopyWait). The pause then never stopped
// the role.

import (
	"testing"
	"time"
)

// pauseHeldForANoTurnResult is a claim in a turn of a task, whose project's pause holds its stop
// for the turn's end, and whose turn has ended with the stop still waiting for the prompt's result
// (turnEndedUnderAHold). copies is the claim's CopyTurnEnd, and nil gives it one that starts no
// copy. It answers the delivery that result names.
func pauseHeldForANoTurnResult(t *testing.T, copies *turnEndCopies, limits Limits) (*harness, string) {
	t.Helper()
	copyTurnEnd := func(Claim, TurnEndCopied) (time.Duration, bool) { return 0, false }
	if copies != nil {
		copyTurnEnd = copies.start
	}
	h, flag, _ := pauseHarness(t, queuedClaim(), func(d *Deps) { d.CopyTurnEnd, d.Limits = copyTurnEnd, limits })
	task := turnEndedUnderAHold(t, h, copies, pausesStop(t, h, flag))
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
	h, task := pauseHeldForANoTurnResult(t, nil, testLimits())

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

// Two answers that say no turn ran, each with the copy of the turn's end still being taken: no stop
// before the copy ends, one after it. Each row's control is named with it.
func TestANoTurnAnswerRunsThePausesHeldStopOnceTheTurnsCopyIsOver(t *testing.T) {
	for _, tc := range []struct {
		name string
		// promptFailures is the prompt-failure limit the claim runs under.
		promptFailures int
		answer         func(h *harness, task string) Event
	}{
		// At the prompt-failure limit the failure is charged and its retirement left to the
		// pause's stop (chargePrompt), and the result begins that stop's wait for the copy.
		//
		// The control is promptResulted without the stopAtResult after promptFailed: no wait for
		// the copy begins, the copy's end runs nothing, and the row fails at the stop it wants once
		// the copy ended.
		{name: "ANoTurnResultAtThePromptLimitWaitsForTheTurnsCopyAPausesHeldStopIsOver", promptFailures: 1,
			answer: func(h *harness, task string) Event { return noTurnResult(h, task) }},
		// The agent refuses the acknowledged prompt with no turn running, as Oh My Pi does when a
		// provider rejects the prompt before any turn begins (lateRefused). The refusal is the
		// prompt's answer, so it ends the pause's wait for the result as a result would: the prompt
		// is charged, and the pause's stop begins its wait for the copy.
		//
		// The control is lateRefused without the stopAtResult after promptFailed, which fails the
		// row the same way.
		{name: "ALateRefusalWithNoTurnRunsThePausesHeldStopOnceTheTurnsCopyIsOver", promptFailures: testLimits().PromptFailures,
			answer: func(h *harness, task string) Event {
				return StreamLateRefusal{Claim: h.token, DeliveryID: task, Error: "No API key found for anthropic"}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copies := &turnEndCopies{}
			limits := testLimits()
			limits.PromptFailures = tc.promptFailures
			h, task := pauseHeldForANoTurnResult(t, copies, limits)

			if err := h.handle(tc.answer(h, task)); err != nil {
				t.Logf("the answer was answered %v", err)
			}

			stopsOnceAfterTheCopy(t, h, copies)
		})
	}
}

// waitedOut is the step between the delivery and the turn that runs the wait for the prompt's turn
// out (noTurn), which takes the task back under a new id, and keeps that prompt's id in first.
func waitedOut(t *testing.T, h *harness, first *string) {
	t.Helper()
	*first = h.wantPrompts(1)[0].DeliveryID
	h.advance(testRPC)
	if h.pending().ID == *first {
		t.Fatal("the wait for the prompt's turn ran out and left the task under the prompt's id, want it taken back under one of its own")
	}
}

// The wait for the prompt's turn runs out (noTurn), which takes the task back under a new id; a
// turn then starts, which is taken for that prompt's and confirms the task, and the pause's stop is
// held for its end. The prompt's own answer, naming the id the wait took back, says it ran no turn
// (waitedOutAnswer): the turn was another input's, and the task goes back to waiting. That answer
// ends the pause's wait for the result, so the stop begins its wait for the copy of the turn's
// end, and the copy's end runs it.
//
// The control is waitedOutAnswer handing the idle claim to afterTurn whatever stop it holds: no
// wait for the copy begins, the copy's end runs nothing, and the test fails at the stop it wants
// once the copy ended.
func TestAWaitedOutPromptsNoTurnAnswerRunsThePausesHeldStopOnceTheTurnsCopyIsOver(t *testing.T) {
	copies := &turnEndCopies{}
	h, flag, _ := pauseHarness(t, queuedClaim(), func(d *Deps) { d.CopyTurnEnd = copies.start })
	var first string
	turnEndedUnderAHold(t, h, copies, pausesStop(t, h, flag), func() StreamTurnStart {
		waitedOut(t, h, &first)
		return StreamTurnStart{Claim: h.token}
	})
	if h.pending().ConfirmedAt.IsZero() {
		t.Fatal("the turn that started while the waited-out prompt was outstanding confirmed nothing, want it taken for that prompt's")
	}

	if err := h.handle(noTurnResult(h, first)); err != nil {
		t.Logf("the waited-out prompt's answer answered %v", err)
	}

	stopsOnceAfterTheCopy(t, h, copies)
}

// The answer of a waited-out prompt that finds the task unconfirmed (waitedOutAnswer's last
// branch) never meets a pause's stop held for its result: such a stop is held for the end of a turn
// that ran while the prompt was outstanding, and the one start that could leave the task
// unconfirmed then, a start naming the prompt's own id, which the task has moved off, is dropped
// at the fence. The claim stays in no turn, so the pause's stop that reaches it runs at once and
// holds nothing for the answer; a start naming no delivery confirms the task, as the test above
// shows.
func TestATurnStartNamingAWaitedOutPromptLeavesNoPausesStopWaitingForItsAnswer(t *testing.T) {
	h, flag, _ := pauseHarness(t, queuedClaim())
	h.reach(StateReady)
	h.must(RequestDeliver{Claim: h.token, Task: providerErrorTaskText})
	var first string
	waitedOut(t, h, &first)

	if err := h.handle(StreamTurnStart{Claim: h.token, DeliveryID: first}); err != nil {
		t.Logf("the start naming the waited-out prompt answered %v", err)
	}
	h.wantState(StateReady)
	if !h.pending().ConfirmedAt.IsZero() {
		t.Fatal("the start naming the waited-out prompt confirmed the task, want it dropped")
	}
	flag.set(7)
	if err := h.handle(pauseStop(h.token, 7)); err != nil {
		t.Fatalf("the pause's stop of a claim in no turn = %v, want it run at once", err)
	}

	if c := h.claim(); c.State != StateSuspended || c.SuspensionHeld {
		t.Fatalf("the claim after the pause's stop is %s holding a stop = %t, want it suspended at once with nothing held for the prompt's answer", c.State, c.SuspensionHeld)
	}
}
