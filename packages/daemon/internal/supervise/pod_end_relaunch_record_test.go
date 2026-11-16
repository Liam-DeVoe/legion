package supervise

// What the daemon's own relaunch record (queueRelaunch) does to the budgets a claim's work ends —
// the run of ends from outside the pod that found the ready agent at work
// (Budgets.PodEndsNotOurs) and the deaths (Budgets.Deaths) — and what the record of such an end
// tells the next process. The record is the daemon's own act, so its turn and its retirement end
// neither (inRelaunchRecordTurn), where a turn of the claim's own work still ends both. The record
// of an end from outside the pod says the pod was ended from outside it (endFromOutside), and
// only the daemon's own replacement of a pod is told as a replacement (endDisplaced).

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// The loop an issue's role whose message turns the cluster keeps ending runs, whole: a message's
// turn the pod ends from outside, the relaunch, the turn of the daemon's own relaunch record
// started and completed, and the next message's turn ended from outside again. The run counts each
// message's turn — 1, 2 — and the end at launch_failure_limit is charged as the claim's own death.
// Taken for the claim's work, the record's turn would end the run at every relaunch, and the claim
// would be relaunched for ever, charged nothing.
func TestARunOfMessageTurnsEndedFromOutsideSurvivesTheRelaunchRecordsTurnsAndIsChargedAtTheLimit(t *testing.T) {
	limit := testLimits().LaunchFailures
	h := newHarness(t)
	h.reach(StateReady)
	for end := 1; end < limit; end++ {
		if end > 1 {
			h.relaunchRecordTurnCompletes(true)
		}
		h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)

		h.wantState(StateLaunching)
		h.wantBudgets(Budgets{PodEndsNotOurs: end})
		h.relaunched()
	}
	h.relaunchRecordTurnCompletes(true)
	h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)

	h.wantState(StateLaunching)
	h.wantBudgets(Budgets{LaunchFailures: 1, Deaths: 1, PodEndsNotOurs: limit})
	if charged := h.records.of(chargedLine); len(charged) != 1 || charged[0].level != slog.LevelWarn {
		t.Fatalf("%q was written %+v, want once at WARN for the end at the limit", chargedLine, charged)
	}
}

// An issue's role whose process dies in every turn a message starts fails once its deaths run
// out, though the turn of the daemon's own relaunch record completes after each relaunch: a death
// in a turn a message started is charged against that open turn (chargeDeath), and the relaunched
// agent holds nothing, so it is given the record of what the death ended at its ready, whose turn
// ends none of the deaths (inRelaunchRecordTurn). Taken for the claim's work, the record's turn
// would end the deaths after every relaunch, each death would be the first, and the claim would be
// relaunched for ever.
func TestAMessageTurnKilledAtEachRelaunchFailsTheClaimAtTheDeathLimitThoughTheRecordsTurnsComplete(t *testing.T) {
	limit := testLimits().LaunchFailures
	h := newHarness(t)
	h.reach(StateReady)
	for death := 1; death < limit; death++ {
		if death > 1 {
			h.relaunchRecordTurnCompletes(true)
		}
		h.messageTurnEndedWithItsPod("")

		h.wantState(StateLaunching)
		h.wantBudgets(Budgets{LaunchFailures: 1, Deaths: death})
		h.relaunched()
		h.wantBudgets(Budgets{Deaths: death})
	}
	h.relaunchRecordTurnCompletes(true)
	h.messageTurnEndedWithItsPod("")

	h.wantState(StateFailed)
	if got := h.claim(); got.Budgets.Deaths != limit || got.FailureCause != CauseDied {
		t.Fatalf("the failed claim carries budgets %+v and cause %q, want deaths at the limit %d and %q",
			got.Budgets, got.FailureCause, limit, CauseDied)
	}
}

// A turn of the claim's own work still ends the run and the deaths, within the claim whose
// relaunch record's turns kept them: the rule reads the delivery the turn confirmed, and a task of
// the claim's is no record. The claim's pod is ended from outside in a message's turn, and after
// the relaunch and its record's turn its process dies in the next message's turn, so the claim
// comes up from the second relaunch holding a run and a death; its record's turn keeps both, and
// then it is given a task of its phase, whose turn completes. Both end at that turn's end, before
// the prompt's result behind it retires the task, in memory and in the store. Read as a record,
// the task's turn would leave both standing until some retirement ended them, and an end in that
// window would be charged against work that got through.
func TestATasksTurnAfterTheRelaunchRecordsTurnEndsTheRunAndTheDeaths(t *testing.T) {
	const task = "implement the plan"
	h := newHarness(t)
	h.reach(StateReady)
	h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)
	h.wantBudgets(Budgets{PodEndsNotOurs: 1})
	h.relaunched()
	h.relaunchRecordTurnCompletes(true)
	h.messageTurnEndedWithItsPod("")
	h.wantBudgets(Budgets{LaunchFailures: 1, Deaths: 1, PodEndsNotOurs: 1})
	h.relaunched()
	h.relaunchRecordTurnCompletes(true)
	h.wantBudgets(Budgets{Deaths: 1, PodEndsNotOurs: 1})

	h.must(RequestDeliver{Claim: h.token, Task: task, Phase: "implementing", Generation: 5})
	p := h.pending()
	if p.Relaunch() || p.Task != task || !p.ConfirmedAt.IsZero() {
		t.Fatalf("pending %+v, want the task %q sent and unconfirmed", p, task)
	}
	h.must(StreamTurnStart{Claim: h.token})
	if c := h.pending(); c.ID != p.ID || c.ConfirmedAt.IsZero() {
		t.Fatalf("the turn's start left %+v pending, want the task %s confirmed: the turn is the task's", c, p.ID)
	}
	h.advance(time.Minute)
	h.mustQuiet(StreamTurnEnd{Claim: h.token})

	h.wantState(StateIdle)
	if c := h.pending(); c.ID != p.ID {
		t.Fatalf("the task's turn ended with %+v pending, want the task held until its prompt's result", c)
	}
	h.wantBudgets(Budgets{})
	if stored := h.store.load(h.token).Budgets; stored != (Budgets{}) {
		t.Fatalf("the stored budgets after the task's turn are %+v, want the run and the deaths ended", stored)
	}

	h.must(StreamPromptResult{Claim: h.token, DeliveryID: p.ID, AgentInvoked: true})

	if c := h.claim().Pending; c != nil {
		t.Fatalf("the task's prompt's result left %+v pending, want the task retired", c)
	}
	h.wantBudgets(Budgets{})
}

// What a relaunched process is told ended its last one says who ended it (displaced): an end
// something outside the pod made is recorded as an end from outside the pod (endFromOutside),
// whether it is continued in a turn or finds no work outstanding, and only the daemon's own
// replacement of the pod is recorded as a replacement (endDisplaced). Each is recorded first,
// before its take-back (recordGoneEnd), so where a suspension is held for the turn the end cut
// off, the stop that suspension then makes finds the record standing (endHeld): the resumed
// process is told what ended its pod, never that it was suspended. The next process reads the
// cause in the prompt it is sent — the task taken back from the turn, or the record alone for a
// claim that held nothing.
func TestAnEndFromOutsideThePodIsToldAsSuchAndOnlyTheDaemonsReplacementAsAReplacement(t *testing.T) {
	const detail = "the cluster marked the pod a disruption target (TerminationByKubelet)"
	for _, tc := range []struct {
		name      string
		idle      bool
		held      bool
		podEnd    runtime.PodEndReading
		displaced bool
		cause     string
	}{
		{name: "an end from outside the pod in a turn", podEnd: runtime.PodEndNotOurs, cause: endFromOutside},
		{name: "an end from outside the pod in a turn a suspension is held for", held: true, podEnd: runtime.PodEndNotOurs, cause: endFromOutside},
		{name: "an end from outside the pod with no work outstanding", idle: true, podEnd: runtime.PodEndNotOurs, cause: endFromOutside},
		{name: "the daemon's replacement of the pod in a turn", displaced: true, cause: endDisplaced},
		{name: "the daemon's replacement of the pod in a turn a suspension is held for", held: true, displaced: true, cause: endDisplaced},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			want := EndedWork{Cause: tc.cause, Background: true, Kinds: []string{"subagent"}, Reported: true}
			if tc.idle {
				h.reach(StateIdle)
			} else {
				h.reach(StateWorking)
				want.Turn, want.TaskTurn = true, true
			}
			h.reportActivity("subagent")
			if tc.held {
				held := RequestSuspend{Claim: h.token, By: SuspendedByOperator, Reason: "the operator is draining the daemon"}
				if err := h.handle(held); !errors.Is(err, ErrSuspendHeld) {
					t.Fatalf("the suspension answered %v, want it held for the turn's end", err)
				}
			}
			want.Generation, want.At = h.generation(), h.clock.Now()

			h.must(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now(),
				PodEnd: tc.podEnd, Displaced: tc.displaced, Detail: detail}})

			h.wantEnded(want)
			h.wantEndedLine(tc.cause)
			sent := len(h.prompts())
			if tc.held {
				h.wantState(StateSuspended)
				h.must(RequestResume{Claim: h.token})
				h.reach(StateReady)
			} else {
				h.relaunched()
			}
			if told, says := h.wantPrompts(sent + 1)[sent], "ended because "+tc.cause+"."; !strings.Contains(told.Message, says) {
				t.Errorf("the next process was sent %q, want it to say %q", told.Message, says)
			}
		})
	}
}
