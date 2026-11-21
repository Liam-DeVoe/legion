package supervise

// An answer of the prompt that says its agent ran no turn, behind the turn's end a project pause's
// stop was held for. That stop waits for the prompt's answer (stopWaitsForResult), and each of the
// answers that end that wait with no turn run is a decision of its own: the result that says no
// turn ran and a late refusal with no turn running are charged as a failed prompt (promptFailed,
// chargePrompt); the answer of a prompt whose wait for its turn had already run out takes back
// the confirmation a turn took for it, or nothing where no turn confirmed the task
// (waitedOutAnswer); and a refusal the shim replays after a reconnect takes back the confirmation
// a foreign turn took (refusedElsewhere, takesBackConfirmed). The row each answer comes in on runs
// the pause's stop once its decision is over (stopAfterAnswer), and nothing else would: the turn
// was over, so no turn's end was coming, and the copy's end runs only a stop whose wait for it has
// begun (endTurnCopyWait). The pause then never stopped the role until its stop row asked again.

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// noTurnResult is the prompt's result for task saying its agent ran no turn.
func noTurnResult(h *harness, task string) StreamPromptResult {
	return StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: false, Error: "the agent ran no turn"}
}

// Below the prompt-failure limit, with no copy started at the turn's end, the result is charged
// and the pause's stop then runs, once: the claim is suspended by the pause.
//
// The control is the result's row without stopAfterAnswer: the claim is then left idle holding the
// pause's stop, and the test fails at the first stop it wants.
func TestANoTurnResultBelowThePromptLimitRunsThePausesHeldStop(t *testing.T) {
	h, task := pauseHeldForTheResult(t, nil, nil)

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
		// The control is the result's row without stopAfterAnswer: no wait for the copy begins,
		// the copy's end runs nothing, and the row fails at the stop it wants once the copy ended.
		{name: "ANoTurnResultAtThePromptLimitWaitsForTheTurnsCopyAPausesHeldStopIsOver", promptFailures: 1,
			answer: func(h *harness, task string) Event { return noTurnResult(h, task) }},
		// The agent refuses the acknowledged prompt with no turn running, as Oh My Pi does when a
		// provider rejects the prompt before any turn begins (lateRefused). The refusal is the
		// prompt's answer, so it ends the pause's wait for the result as a result would: the prompt
		// is charged, and the pause's stop begins its wait for the copy.
		//
		// The control is the late refusal's row without stopAfterAnswer, which fails the row the
		// same way.
		{name: "ALateRefusalWithNoTurnRunsThePausesHeldStopOnceTheTurnsCopyIsOver", promptFailures: testLimits().PromptFailures,
			answer: func(h *harness, task string) Event {
				return StreamLateRefusal{Claim: h.token, DeliveryID: task, Error: "No API key found for anthropic"}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copies := &turnEndCopies{}
			h, task := pauseHeldForTheResult(t, copies, func(d *Deps) { d.Limits.PromptFailures = tc.promptFailures })

			if err := h.handle(tc.answer(h, task)); err != nil {
				t.Logf("the answer was answered %v", err)
			}

			suspendedOnceAfterTheCopy(t, h, copies, SuspendedByReleased)
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
// ends the pause's wait for the result, so waitedOutAnswer leaves the idle claim to the pause's
// stop rather than to the turn's aftermath (afterTurn), the stop begins its wait for the copy of
// the turn's end at the end of the result's row (stopAfterAnswer), and the copy's end runs it.
//
// The control is the result's row without stopAfterAnswer: no wait for the copy begins, the copy's
// end runs nothing, and the test fails at the stop it wants once the copy ended.
func TestAWaitedOutPromptsNoTurnAnswerRunsThePausesHeldStopOnceTheTurnsCopyIsOver(t *testing.T) {
	copies := &turnEndCopies{}
	var first string
	h, _ := pauseHeldForTheResult(t, copies, nil, func(h *harness) StreamTurnStart {
		waitedOut(t, h, &first)
		return StreamTurnStart{Claim: h.token}
	})
	if h.pending().ConfirmedAt.IsZero() {
		t.Fatal("the turn that started while the waited-out prompt was outstanding confirmed nothing, want it taken for that prompt's")
	}

	if err := h.handle(noTurnResult(h, first)); err != nil {
		t.Logf("the waited-out prompt's answer answered %v", err)
	}

	suspendedOnceAfterTheCopy(t, h, copies, SuspendedByReleased)
}

// A turn start naming the prompt whose wait for its turn ran out, an id the task has moved off, is
// dropped at the fence: the claim stays in no turn and the task unconfirmed, so a pause's stop that
// reaches the claim then runs at once and holds nothing for that prompt's answer.
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

// A busy answer to a later send of the task takes the task back unconfirmed under a new id while
// the prompt whose wait ran out is still outstanding, and records the claim working for the turn
// the agent named (promptResulted, recordBusyTurn). A pause's stop held over that turn waits at its
// end for that earlier prompt's answer. That answer, saying the prompt ran no turn, finds the task
// unconfirmed and nothing more to take back (waitedOutAnswer's last branch), and it ends the
// pause's wait as the other no-turn answers do: the stop begins its wait for the copy of the turn's
// end, and the copy's end runs it. Each row is one answer the branch returns from, and names its
// control.
func TestAWaitedOutPromptsAnswerAfterABusyAnswerRunsThePausesHeldStopOnceTheTurnsCopyIsOver(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(h *harness, first string) Event
		// unread is whether the answer says the prompt was never read, so its read mark goes
		// (markUnread); a result that ran no turn leaves the mark, since the agent may have read it.
		unread bool
	}{
		// The prompt's result, saying it ran no turn (read).
		//
		// The control is the result's row without stopAfterAnswer: no wait for the copy begins,
		// the copy's end runs nothing, and the row fails at the stop it wants once the copy ended.
		{name: "ItsNoTurnResult", answer: func(h *harness, first string) Event { return noTurnResult(h, first) }},
		// The agent's refusal of the acknowledged prompt with no turn running, as Oh My Pi gives
		// when a provider rejects the prompt before any turn begins: no busy answer, so nothing is
		// recorded working, and the prompt was never read (lateRefused, refusedElsewhere).
		//
		// The control is the late refusal's row without stopAfterAnswer, which fails the row the
		// same way.
		{name: "ItsLateRefusalWithNoTurnRunning", unread: true, answer: func(h *harness, first string) Event {
			return StreamLateRefusal{Claim: h.token, DeliveryID: first, Error: "No API key found for anthropic"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copies := &turnEndCopies{}
			h, flag, _ := pauseHarness(t, queuedClaim(), func(d *Deps) { d.CopyTurnEnd = copies.start })
			h.reach(StateReady)
			h.must(RequestDeliver{Claim: h.token, Task: providerErrorTaskText})
			var first string
			waitedOut(t, h, &first)
			// The next trigger sends the task again under its new id, and Oh My Pi answers that send busy.
			h.observe(runtime.Alive)
			later := h.wantPrompts(2)[1].DeliveryID
			if err := h.handle(StreamPromptResult{Claim: h.token, DeliveryID: later, AgentInvoked: false, Error: busyAnswer}); err != nil {
				t.Logf("the busy answer answered %v", err)
			}
			h.wantState(StateWorking)
			if !h.pending().ConfirmedAt.IsZero() {
				t.Fatal("the busy answer left the task confirmed, want it taken back unconfirmed")
			}
			pausesStop(t, h, flag)()
			h.mustQuiet(StreamTurnEnd{Claim: h.token})
			if started := copies.started(); len(started) != 1 {
				t.Fatalf("the turn's end started %d copies, want one", len(started))
			}
			if h.pending().DeliveredAt.IsZero() {
				t.Fatal("the task carries no read mark before the waited-out prompt's answer, want the mark that prompt set")
			}

			if err := h.handle(tc.answer(h, first)); err != nil {
				t.Logf("the waited-out prompt's answer answered %v", err)
			}

			if unread := h.pending().DeliveredAt.IsZero(); unread != tc.unread {
				t.Fatalf("the read mark is gone = %t after the waited-out prompt's answer, want %t", unread, tc.unread)
			}
			suspendedOnceAfterTheCopy(t, h, copies, SuspendedByReleased)
		})
	}
}

// The agent starts a turn of its own while the task's prompt waits acknowledged, and the turn's
// start is taken for that prompt's and confirms the task; the pause's stop is held for the turn's
// end, which leaves the stop waiting for the prompt's answer. The shim reconnects, and its backlog
// replays Oh My Pi's refusal of the prompt, busy with that turn: the turn was another input's
// (refusedElsewhere, takesBackConfirmed), so the task goes back unread and charged nothing. That
// refusal is the prompt's answer, so it ends the pause's wait as any answer does, and the stop
// runs once the copy of the turn's end is over, or at once where the end started none. Before, the
// replayed refusal took the confirmation back and ran no stop: the claim stayed idle holding the
// pause's stop until the pause's stop row asked again.
//
// The control is the late refusal's row without stopAfterAnswer: each row then fails at the stop
// it wants, the claim idle and holding the pause's stop.
func TestAReplayedRefusalTakingBackAForeignTurnsConfirmationRunsThePausesHeldStop(t *testing.T) {
	for _, tc := range []struct {
		name string
		// copies is the claim's CopyTurnEnd; nil starts no copy.
		copies *turnEndCopies
	}{
		{name: "AfterTheCopyOfTheTurnsEnd", copies: &turnEndCopies{}},
		{name: "WithNoCopyStartedAtTheTurnsEnd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, task := pauseHeldForTheResult(t, tc.copies, nil)
			charged := h.claim().Budgets.PromptFailures
			replacement := reconnect(h, false)

			if err := h.handle(StreamLateRefusal{Claim: h.token, DeliveryID: task, Error: busyAnswer, Replayed: true,
				ConnSequence: replacement.Sequence()}); err != nil {
				t.Logf("the replayed refusal answered %v", err)
			}

			if p := h.pending(); !p.ConfirmedAt.IsZero() || !p.DeliveredAt.IsZero() {
				t.Fatalf("pending after the replayed refusal = %+v, want the foreign turn's confirmation taken back unread", p)
			}
			if tc.copies != nil {
				suspendedOnceAfterTheCopy(t, h, tc.copies, SuspendedByReleased)
			} else if n := processStops(h); n != 1 {
				t.Fatalf("the process was stopped %d times after the replayed refusal (state %s), want the pause's stop once", n, h.claim().State)
			}
			if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != SuspendedByReleased || c.Budgets.PromptFailures != charged {
				t.Fatalf("the claim is %s by %q with %d prompt failures charged, want it suspended by the pause and nothing charged",
					c.State, c.SuspendedBy, c.Budgets.PromptFailures-charged)
			}
		})
	}
}
