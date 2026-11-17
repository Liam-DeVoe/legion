package supervise

// What the daemon's own relaunch record (queueRelaunch) does to the budgets a claim's work ends —
// the run of ends from outside the pod that found the ready agent at work
// (Budgets.PodEndsNotOurs) and the deaths (Budgets.Deaths) — and what the record of such an end
// tells the next process. A completed turn of the claim's own work ends both; a record's turn
// ends neither, and takes back only what the end that cut that record's earlier turn charged, at
// most one of each (turnEndsCounts): a fresh record's turn takes back nothing, and the turn of a
// record a let-go took back and the relaunch sent again takes back that one end. No record's
// retirement ends either. What a turn is the turn of, and what it takes back, are read from the
// persisted claim at the turn's end (Claim.RecordTurn), as after a restart. The record of an end
// from outside the pod says the pod was ended from outside it (endFromOutside), and only the
// daemon's own replacement of a pod is told as a replacement (endDisplaced).

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// An issue's role whose process dies in every turn a message starts fails once its deaths run
// out, though the turn of the daemon's own relaunch record completes after each relaunch: a death
// in a turn a message started is charged against that open turn (chargeDeath), and the relaunched
// agent holds nothing, so it is given the record of what the death ended at its ready, a fresh
// record whose turn ends none of the deaths (turnEndsCounts). Taken for the claim's work, the
// record's turn would end the deaths after every relaunch, each death would be the first, and the
// claim would be relaunched for ever.
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

// The daemon restarts while the record's turn runs. The rebuilt machine holds nothing in memory of
// the turn, and reads the claim it stored: the record confirmed at the turn's start. The record's
// turn then ends neither the run nor the deaths, as it would have without the restart. Read from
// memory, the rule would see no record turn after the restart, and the run would end at every
// relaunch whose record's turn a restart split.
func TestARestartInTheRelaunchRecordsTurnLeavesTheRunStanding(t *testing.T) {
	h := newHarness(t)
	h.reach(StateReady)
	h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)
	h.relaunched()
	if p := h.claim().Pending; p == nil || !p.Relaunch() {
		t.Fatalf("the relaunched claim holds %+v, want the relaunch's record", p)
	}
	h.must(StreamTurnStart{Claim: h.token})
	found := h.budgets()
	if found.PodEndsNotOurs != 1 {
		t.Fatalf("the record's turn started with budgets %+v, want the run of 1", found)
	}

	h.restart()
	h.wantState(StateWorking)
	h.advance(time.Minute)
	h.mustQuiet(StreamTurnEnd{Claim: h.token})

	if got, stored := h.budgets(), h.store.load(h.token).Budgets; got != found || stored != found {
		t.Fatalf("the record's turn, split by a restart, left budgets %+v (%+v stored), want the %+v it found",
			got, stored, found)
	}
}

// The record's turn ends and its prompt's result has not come yet, so the record still stands; a
// message then starts a turn of the claim's own work, which completes. That turn's start is no
// start of the pending delivery, which is confirmed already, so it confirms nothing and forgets
// the record turn (forgetRecordTurn): the turn is not the record's, and it ends the run and the
// deaths. The daemon restarts between that turn's start and its end in the second case, and the
// rebuilt machine reads the same from the claim it stored. Taken for the record's because the
// claim still holds the record confirmed, every turn of real work in that window would leave both
// standing, and an end from outside the pod after it would be charged against work that got
// through.
func TestATurnOfRealWorkWhileTheRelaunchRecordAwaitsItsResultEndsTheRun(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "no restart"
		if restart {
			name = "a restart inside the turn of real work"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.reach(StateReady)
			h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)
			h.relaunched()
			record := h.pending()
			if !record.Relaunch() {
				t.Fatalf("the relaunched claim holds %+v, want the relaunch's record", record)
			}
			h.must(StreamTurnStart{Claim: h.token})
			h.advance(time.Minute)
			h.mustQuiet(StreamTurnEnd{Claim: h.token})
			if c := h.claim().Pending; c == nil || c.ID != record.ID {
				t.Fatalf("the record's turn ended with %+v pending, want the record standing until its prompt's result", c)
			}
			h.wantBudgets(Budgets{PodEndsNotOurs: 1})

			h.advance(time.Minute)
			h.must(StreamTurnStart{Claim: h.token})
			if restart {
				h.restart()
				h.wantState(StateWorking)
				if p := h.pending(); p.ID != record.ID || p.ConfirmedAt.IsZero() {
					t.Fatalf("the rebuilt claim holds %+v, want the record %s confirmed and waiting for its prompt's result", p, record.ID)
				}
			}
			h.advance(time.Minute)
			h.mustQuiet(StreamTurnEnd{Claim: h.token})

			if got, stored := h.budgets(), h.store.load(h.token).Budgets; got != (Budgets{}) || stored != (Budgets{}) {
				t.Fatalf("the turn of real work while the record waits left budgets %+v (%+v stored), want the run and the deaths ended",
					got, stored)
			}
		})
	}
}

// recordTurnEnd is one of the two ends of a claim's process the turns of its relaunch record are
// walked under (recordTurnEnds), read as reading: its pod ended from outside it
// (runtime.PodEndNotOurs), or its own death, an end the runtime read neither way. counted is the
// budgets the n-th such end since the claim last completed a turn leaves, each counted against an
// open turn, before the relaunch's ready: an end from outside the pod is continued uncharged below
// launch_failure_limit and charged a death from there, where a death is charged at once.
// counted(0) is an end that finds no work outstanding, which counts in no run, though a death is
// still charged the launch failure the ready ends. fails is the end that fails the claim.
type recordTurnEnd struct {
	name    string
	reading runtime.PodEndReading
	counted func(n, limit int) Budgets
	fails   func(limit int) int
}

var recordTurnEnds = []recordTurnEnd{
	{
		name:    "an end from outside the pod",
		reading: runtime.PodEndNotOurs,
		counted: func(n, limit int) Budgets {
			if n < limit {
				return Budgets{PodEndsNotOurs: n}
			}
			return Budgets{LaunchFailures: 1, Deaths: n - limit + 1, PodEndsNotOurs: n}
		},
		fails: func(limit int) int { return 2*limit - 1 },
	},
	{
		name:    "the claim's own process death",
		counted: func(n, _ int) Budgets { return Budgets{LaunchFailures: 1, Deaths: n} },
		fails:   func(limit int) int { return limit },
	},
}

// atReady is the budgets b leaves once the relaunched agent is ready: the ready ends the launch
// failures and the run of a start's ends, and nothing else (ready).
func atReady(b Budgets) Budgets {
	b.LaunchFailures, b.StartEndsNotOurs = 0, 0
	return b
}

// parkedEndWhileIdle ends the process of a parked claim — idle, holding no task — as end reads,
// after the process reported a subagent running. The end finds no work outstanding, so it counts
// in no run and charges no death, and the claim is relaunched with its counts as they stood. The
// report is what gives the let-go something to record (recordEnd), so the relaunched agent,
// holding nothing, is given a fresh relaunch record at its ready (queueRelaunch): unconfirmed, and
// not marked Interrupted.
func (h *harness) parkedEndWhileIdle(end recordTurnEnd, limit int) {
	h.t.Helper()
	h.wantState(StateIdle)
	if p := h.claim().Pending; p != nil {
		h.t.Fatalf("the claim holds %+v, want nothing: the claim is parked", p)
	}
	before := h.budgets()
	h.reportActivity("subagent")
	h.advance(time.Minute)
	h.observePodEnd(runtime.Gone, end.reading)

	h.wantState(StateLaunching)
	ended := before
	ended.LaunchFailures += end.counted(0, limit).LaunchFailures
	h.wantBudgets(ended)
	h.relaunched()
	h.wantBudgets(atReady(before))
	if p := h.pending(); !p.Relaunch() || p.Interrupted || !p.ConfirmedAt.IsZero() {
		h.t.Fatalf("the relaunched claim holds %+v, want a fresh relaunch record, unconfirmed", p)
	}
}

// endInRecordsTurn runs a turn of the relaunch record the ready claim holds and ends its process
// in it a minute in, as reading reads. The turn's start confirms the record, so the end is counted
// against that open turn (workOutstanding). Short of failing the claim, the let-go takes the
// record back (interrupted): it stays the claim's pending delivery, under a new id that is still
// a record's, unconfirmed and marked Interrupted, for the relaunch to send again.
func (h *harness) endInRecordsTurn(reading runtime.PodEndReading) {
	h.t.Helper()
	h.wantState(StateReady)
	record := h.pending()
	if !record.Relaunch() || !record.ConfirmedAt.IsZero() {
		h.t.Fatalf("the claim holds %+v, want a relaunch record waiting for its turn", record)
	}
	h.must(StreamTurnStart{Claim: h.token})
	if p := h.pending(); p.ID != record.ID || p.ConfirmedAt.IsZero() {
		h.t.Fatalf("the turn's start left %+v pending, want the record %s confirmed: the turn is the record's", p, record.ID)
	}
	h.advance(time.Minute)
	h.observePodEnd(runtime.Gone, reading)
	if h.state() == StateFailed {
		return
	}
	h.wantState(StateLaunching)
	if p := h.pending(); !p.Relaunch() || p.ID == record.ID || !p.Interrupted || !p.ConfirmedAt.IsZero() {
		h.t.Fatalf("the end in the record's turn left %+v pending, want the record taken back under a new id, unconfirmed and interrupted", p)
	}
}

// cutRecordsTurnCompletes runs to completion the turn of the relaunch record a let-go took back
// from an earlier turn of it, which the relaunch's ready sent again: its start confirms the
// record, its end completes it, and the prompt's result behind that end retires it. The turn's end
// leaves the counts at want, in memory and in the store, before the result retires the record,
// and the retirement leaves them there: the turn takes back only what the end that cut its
// earlier turn charged (turnEndsCounts), and the retirement takes nothing.
func (h *harness) cutRecordsTurnCompletes(cycle int, want Budgets) {
	h.t.Helper()
	h.wantState(StateReady)
	record := h.pending()
	if !record.Relaunch() || !record.Interrupted || !record.ConfirmedAt.IsZero() {
		h.t.Fatalf("cycle %d: the relaunched claim holds %+v, want the record taken back from its turn, sent again", cycle, record)
	}
	h.must(StreamTurnStart{Claim: h.token})
	if p := h.pending(); p.ID != record.ID || p.ConfirmedAt.IsZero() {
		h.t.Fatalf("cycle %d: the turn's start left %+v pending, want the record %s confirmed", cycle, p, record.ID)
	}
	h.advance(time.Minute)
	h.mustQuiet(StreamTurnEnd{Claim: h.token})

	h.wantState(StateIdle)
	if got, stored := h.budgets(), h.store.load(h.token).Budgets; got != want || stored != want {
		h.t.Fatalf("cycle %d: the turn of the record sent again ended with budgets %+v (%+v stored), want %+v: "+
			"that turn takes back what the end in its earlier turn charged, and nothing else", cycle, got, stored, want)
	}
	h.must(StreamPromptResult{Claim: h.token, DeliveryID: record.ID, AgentInvoked: true})
	if p := h.claim().Pending; p != nil {
		h.t.Fatalf("cycle %d: the record's prompt's result left %+v pending, want the record retired", cycle, p)
	}
	h.wantBudgets(want)
}

// A parked claim — idle, holding no task — whose process ends in the turns of its relaunch record
// is never failed for it, however often that happens. In each cycle its process reports a
// subagent and is ended while the claim is idle, which counts in no run; the relaunch gives the
// claim a fresh relaunch record, and the end in that record's turn is counted against the open
// turn; the next relaunch sends the record again, and its turn, completed, takes that end back,
// since it completes the work the end cut off (turnEndsCounts), so both counts are back where
// they were. The walk runs launch_failure_limit + 3 cycles, for an end from outside the pod and
// for the claim's own process death, whose charge died makes after the take-back of the turn
// (interrupted). Kept by every turn of a record, the count would grow by one a cycle until the
// claim failed, where a parked agent the environment ends now and then is relaunched however
// often it happens (Budgets.Deaths).
func TestAParkedClaimWhoseProcessEndsInItsRelaunchRecordsTurnsIsNeverFailed(t *testing.T) {
	limit := testLimits().LaunchFailures
	for _, end := range recordTurnEnds {
		t.Run(end.name, func(t *testing.T) {
			h := newHarness(t)
			h.reach(StateIdle)
			for cycle := 1; cycle <= limit+3; cycle++ {
				h.parkedEndWhileIdle(end, limit)
				h.endInRecordsTurn(end.reading)
				h.wantBudgets(end.counted(1, limit))
				h.relaunched()
				h.wantBudgets(atReady(end.counted(1, limit)))
				h.cutRecordsTurnCompletes(cycle, Budgets{})
			}
			h.wantNoFailure()
		})
	}
}

// A run of ends short of the limit whose last end cuts a relaunch record's turn is not set back
// when the record sent again completes: that turn takes back the one end that cut the record's
// earlier turn, and every end before it stands, so the claim fails at the limit. These are the
// review's shapes, nine cycles each: m turns a message started are each ended, by an end from
// outside the pod or a death, and each relaunch's fresh record has its turn complete; then the
// parked claim's process is ended while idle, which counts nothing, and the end in the turn of
// the fresh record that relaunch gives it is counted; then that record, sent again, completes.
// Ending every count there, as the turn of a record sent again once did, set each cycle back to
// zero, and none of these claims ever failed: an end from outside the pod with m of one, two or
// three, where three charged two deaths every cycle, and a death with m of one.
func TestARunOfEndsWhoseLastCutsARelaunchRecordsTurnFailsAtTheLimit(t *testing.T) {
	limit := testLimits().LaunchFailures
	for _, tc := range []struct {
		name         string
		end          recordTurnEnd
		messageTurns int
	}{
		{name: "ends from outside the pod, one message turn a cycle", end: recordTurnEnds[0], messageTurns: 1},
		{name: "ends from outside the pod, two message turns a cycle", end: recordTurnEnds[0], messageTurns: 2},
		{name: "ends from outside the pod, three message turns a cycle", end: recordTurnEnds[0], messageTurns: 3},
		{name: "process deaths, one message turn a cycle", end: recordTurnEnds[1], messageTurns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.reach(StateIdle)
			failed := func() bool { return h.state() == StateFailed }
		cycles:
			for cycle := 1; cycle <= 9; cycle++ {
				for range tc.messageTurns {
					if h.messageTurnEndedWithItsPod(tc.end.reading); failed() {
						break cycles
					}
					h.relaunched()
					h.relaunchRecordTurnCompletes(true)
				}
				h.parkedEndWhileIdle(tc.end, limit)
				before := h.budgets()
				if h.endInRecordsTurn(tc.end.reading); failed() {
					break cycles
				}
				h.relaunched()
				h.cutRecordsTurnCompletes(cycle, before)
			}

			h.wantState(StateFailed)
			if got := h.claim(); got.Budgets.Deaths != limit || got.FailureCause != CauseDied {
				t.Fatalf("the failed claim carries budgets %+v and cause %q, want deaths at the limit %d and %q",
					got.Budgets, got.FailureCause, limit, CauseDied)
			}
		})
	}
}

// One relaunch record cut twice and then completed takes back one end, the second: each
// confirmation of the record measures what the end before it charged from the counts at the
// confirmation before, so the record sent the second time takes back the second end alone, and
// the counts stand one end above where they were. A death under a compaction between the cut and
// the record sent again is the other way two charges meet in one difference without a turn
// start: the compaction takes the record's prompt back and puts the claim in working, so the
// death is counted, and the record's turn still takes back one death, no more. Forgiving both
// ends, as the turn of a record sent again once did, or every death the difference holds, would
// let a claim that ends on each turn of its record twice in a row run for ever.
func TestARelaunchRecordsTurnTakesBackOneEndAtMost(t *testing.T) {
	limit := testLimits().LaunchFailures
	t.Run("a record cut twice, then completed", func(t *testing.T) {
		for _, end := range recordTurnEnds {
			t.Run(end.name, func(t *testing.T) {
				h := newHarness(t)
				h.reach(StateIdle)
				h.parkedEndWhileIdle(end, limit)
				h.endInRecordsTurn(end.reading)
				h.relaunched()
				h.endInRecordsTurn(end.reading)
				h.relaunched()
				h.wantBudgets(atReady(end.counted(2, limit)))
				h.cutRecordsTurnCompletes(1, atReady(end.counted(1, limit)))
			})
		}
	})
	t.Run("a record cut, then a death under a compaction before it is sent again", func(t *testing.T) {
		h := newHarness(t)
		h.reach(StateIdle)
		death := recordTurnEnds[1]
		h.parkedEndWhileIdle(death, limit)
		h.endInRecordsTurn(death.reading)
		h.relaunched()
		release, compacted := h.compactHeld()
		h.observe(runtime.Gone)
		h.wantState(StateLaunching)
		release()
		if err := <-compacted; !errors.Is(err, ErrCompactProcessGone) {
			t.Fatalf("the compaction whose process died answered %v, want ErrCompactProcessGone", err)
		}
		h.m.Wait()
		h.relaunched()
		h.wantBudgets(Budgets{Deaths: 2})
		h.cutRecordsTurnCompletes(1, Budgets{Deaths: 1})
	})
}

// A claim whose process ends in every turn of its relaunch record completes no turn, so each of
// those ends is counted against the record's open turn (workOutstanding) and none is ever ended:
// the claim is charged at launch_failure_limit and fails once its deaths run out, for an end from
// outside the pod and for its own process death. A session Oh My Pi crashes on at its first
// prompt, and a Burstable pod that evicts itself in every such turn, are the loops this bounds.
// Left uncounted because they land in a record's turn, those ends would relaunch the claim for
// ever.
func TestAClaimWhoseProcessEndsInEveryTurnOfItsRelaunchRecordFailsAtTheLimit(t *testing.T) {
	limit := testLimits().LaunchFailures
	for _, end := range recordTurnEnds {
		t.Run(end.name, func(t *testing.T) {
			h := newHarness(t)
			h.reach(StateIdle)
			h.parkedEndWhileIdle(end, limit)
			fails := end.fails(limit)
			for n := 1; n < fails; n++ {
				h.endInRecordsTurn(end.reading)
				h.wantBudgets(end.counted(n, limit))
				h.relaunched()
				h.wantBudgets(atReady(end.counted(n, limit)))
			}

			h.endInRecordsTurn(end.reading)

			h.wantState(StateFailed)
			if got := h.claim(); got.Budgets.Deaths != limit || got.FailureCause != CauseDied {
				t.Fatalf("the failed claim carries budgets %+v and cause %q, want deaths at the limit %d and %q",
					got.Budgets, got.FailureCause, limit, CauseDied)
			}
		})
	}
}

// The daemon restarts around a relaunch record sent again: between the cut of the record's
// earlier turn and the confirmation of the record sent again, or inside the turn of the record
// sent again. The rebuilt machine holds nothing of either in memory and reads the claim it stored
// (Claim.RecordTurn): the base the earlier turn's confirmation recorded, and the turn the
// confirmation of the record sent again dated with what it takes back. Its completion then takes
// back exactly the end that cut the earlier turn, as it would have without the restart. The claim
// comes up holding the count of an end in a turn a message started, so a restart that lost the
// record turn shows either way: read as a turn of work, the record's turn would end that count
// too, and with no base, the record sent again would take back nothing.
func TestARestartAroundARelaunchRecordSentAgainTakesBackExactlyTheEndThatCutIt(t *testing.T) {
	limit := testLimits().LaunchFailures
	for _, end := range recordTurnEnds {
		for _, inTurn := range []bool{false, true} {
			name := end.name + ", a restart before the record sent again is confirmed"
			if inTurn {
				name = end.name + ", a restart inside the turn of the record sent again"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t)
				h.reach(StateReady)
				h.messageTurnEndedWithItsPod(end.reading)
				h.relaunched()
				found := h.budgets()
				h.wantBudgets(atReady(end.counted(1, limit)))
				h.endInRecordsTurn(end.reading)
				h.relaunched()
				h.wantBudgets(atReady(end.counted(2, limit)))
				if !inTurn {
					h.restart()
					h.wantState(StateReady)
				}
				h.must(StreamTurnStart{Claim: h.token})
				if p := h.pending(); !p.Relaunch() || !p.Interrupted || p.ConfirmedAt.IsZero() {
					t.Fatalf("the turn's start left %+v pending, want the record sent again confirmed", p)
				}
				if inTurn {
					h.restart()
					h.wantState(StateWorking)
				}
				h.advance(time.Minute)
				h.mustQuiet(StreamTurnEnd{Claim: h.token})

				if got, stored := h.budgets(), h.store.load(h.token).Budgets; got != found || stored != found {
					t.Fatalf("the turn of the record sent again, with a restart, left budgets %+v (%+v stored), want the %+v "+
						"the claim held before the end that cut the record's earlier turn", got, stored, found)
				}
			})
		}
	}
}

// A turn that confirms no record, started between the cut of a record's turn and the
// confirmation of that record sent again, forgets the base the cut would be measured from
// (forgetRecordTurn). Here the record sent again finds the agent in a turn of its own, which the
// busy answer records (recordBusyTurn), and an end cuts that turn too. Measured from the base, one
// difference would hold both ends, and the record's turn would take one of them back; it takes
// back nothing, both ends stand, and the claim fails sooner rather than forgiving the end of a
// turn that was not the record's.
func TestATurnThatConfirmsNoRecordBeforeTheRelaunchRecordSentAgainLeavesItNothingToTakeBack(t *testing.T) {
	limit := testLimits().LaunchFailures
	for _, end := range recordTurnEnds {
		t.Run(end.name, func(t *testing.T) {
			h := newHarness(t)
			h.reach(StateIdle)
			h.parkedEndWhileIdle(end, limit)
			h.endInRecordsTurn(end.reading)
			h.relaunched()
			record := h.pending()
			h.must(StreamLateRefusal{Claim: h.token, DeliveryID: record.ID, Error: busyAnswer})
			h.wantState(StateWorking)
			if p := h.pending(); !p.Relaunch() || !p.Interrupted || !p.ConfirmedAt.IsZero() {
				t.Fatalf("the busy answer left %+v pending, want the record sent again waiting, unconfirmed", p)
			}
			h.advance(time.Minute)
			h.observePodEnd(runtime.Gone, end.reading)
			h.relaunched()
			both := atReady(end.counted(2, limit))
			h.wantBudgets(both)
			h.cutRecordsTurnCompletes(1, both)
		})
	}
}

// A turn of a fresh relaunch record ends neither count, though at its end the claim holds another
// delivery, or the record unconfirmed, and though the daemon restarts in the turn before or after
// either: what the turn is the turn of is read at its end from the claim's record of the turn
// that confirmed the record (Claim.RecordTurn, turnEndsCounts), which neither a delivery nor a
// prompt's result moves, and which a restart reads back from the store — never from the delivery
// the claim holds at the turn's end. The claim comes up from its second relaunch holding a run of
// ends from outside the pod and a death, both counted against message turns, and the fresh record
// it is given starts its turn. Then a task is delivered, which retires the confirmed record and
// takes its place (queue), or the record's prompt answers a provider error before the turn ends,
// which takes the record back unconfirmed (providerErrored), each with a restart before it, after
// it or neither. Read from the claim at the turn's end, each would make the record's turn a turn
// of work, and both counts would end at a turn that completed none of what they were counted
// against.
func TestAFreshRelaunchRecordsTurnEndsNeitherCountWhateverTheClaimHoldsAtItsEnd(t *testing.T) {
	const task = "implement the plan"
	deliver := func(h *harness, _ Delivery) {
		h.must(RequestDeliver{Claim: h.token, Task: task, Phase: "implementing", Generation: 5})
		if p := h.pending(); p.Relaunch() || p.Task != task {
			h.t.Fatalf("pending %+v, want the task %q in the confirmed record's place", p, task)
		}
	}
	providerError := func(h *harness, record Delivery) {
		h.must(StreamPromptResult{Claim: h.token, DeliveryID: record.ID, AgentInvoked: true,
			Error: "overloaded_error", HTTPStatus: 529})
		if p := h.pending(); !p.Relaunch() || !p.ConfirmedAt.IsZero() {
			h.t.Fatalf("pending %+v, want the record taken back unconfirmed", p)
		}
	}
	restart := func(h *harness, _ Delivery) {
		h.restart()
		h.wantState(StateWorking)
	}
	for _, tc := range []struct {
		name   string
		during []func(h *harness, record Delivery)
	}{
		{name: "a task delivered in the turn", during: []func(*harness, Delivery){deliver}},
		{name: "a provider error's result before the turn's end", during: []func(*harness, Delivery){providerError}},
		{name: "a task delivered, then a restart", during: []func(*harness, Delivery){deliver, restart}},
		{name: "a provider error's result, then a restart", during: []func(*harness, Delivery){providerError, restart}},
		{name: "a restart, then a task delivered", during: []func(*harness, Delivery){restart, deliver}},
		{name: "a restart, then a provider error's result", during: []func(*harness, Delivery){restart, providerError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counts := Budgets{Deaths: 1, PodEndsNotOurs: 1}
			h := newHarness(t)
			h.reach(StateReady)
			h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)
			h.relaunched()
			h.relaunchRecordTurnCompletes(true)
			h.messageTurnEndedWithItsPod("")
			h.relaunched()
			h.wantBudgets(counts)
			record := h.pending()
			if !record.Relaunch() || record.Interrupted {
				t.Fatalf("the relaunched claim holds %+v, want a fresh relaunch record", record)
			}
			h.must(StreamTurnStart{Claim: h.token})
			if p := h.pending(); p.ID != record.ID || p.ConfirmedAt.IsZero() {
				t.Fatalf("the turn's start left %+v pending, want the record %s confirmed: the turn is the record's", p, record.ID)
			}
			h.advance(time.Minute)

			for _, step := range tc.during {
				step(h, record)
			}

			h.wantState(StateWorking)
			h.wantBudgets(counts)
			h.mustQuiet(StreamTurnEnd{Claim: h.token})

			h.wantState(StateIdle)
			if got, stored := h.budgets(), h.store.load(h.token).Budgets; got != counts || stored != counts {
				t.Fatalf("the fresh record's turn ended with budgets %+v (%+v stored), want the %+v it found", got, stored, counts)
			}
		})
	}
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

// A record cut off twice, first by a process death and then by an end from outside the pod below
// the limit, is forgiven the second cut alone: the confirmation of the record sent after the first
// cut moves the base to the counts it finds, so the completed turn of the record sent after the
// second takes back the outside end and leaves the death standing. A base kept from the record's
// first confirmation would measure both ends, and the cap of one a count would then take back the
// death as well; a record cut twice by ends of one kind cannot tell those two apart.
func TestARelaunchRecordCutTwiceByDifferentEndsForgivesOnlyTheLast(t *testing.T) {
	limit := testLimits().LaunchFailures
	h := newHarness(t)
	h.reach(StateIdle)
	h.parkedEndWhileIdle(recordTurnEnds[1], limit)
	h.endInRecordsTurn("")
	h.relaunched()
	h.endInRecordsTurn(runtime.PodEndNotOurs)
	h.relaunched()
	before := h.budgets()
	h.pendingTurnCompletes()
	if got := h.budgets(); got.Deaths != 1 || got.PodEndsNotOurs != 0 {
		t.Errorf("the record cut by a death and then an outside end left budgets %+v after its turn (%+v before), "+
			"want the death standing and the outside end taken back", got, before)
	}
}

// A fresh relaunch record takes back nothing, whatever an earlier record's turn left in the claim's
// record: here an earlier record's turn completes, a compaction of the idle claim loses its process,
// the death is charged, and the relaunch gives the claim a fresh record, whose completed turn must
// leave that death standing. Measured from the earlier record's base, as a record cut off would be,
// it would take the death back, though no end cut a turn of this record.
func TestAFreshRelaunchRecordAfterADeathUnderACompactionTakesNothingBack(t *testing.T) {
	limit := testLimits().LaunchFailures
	h := newHarness(t)
	h.reach(StateIdle)
	h.parkedEndWhileIdle(recordTurnEnds[1], limit)
	h.pendingTurnCompletes()
	h.wantState(StateIdle)
	release, compacted := h.compactHeld()
	h.observe(runtime.Gone)
	release()
	if err := <-compacted; !errors.Is(err, ErrCompactProcessGone) {
		t.Fatalf("the compaction answered %v, want ErrCompactProcessGone", err)
	}
	h.m.Wait()
	h.relaunched()
	if held := h.claim().Pending; held == nil || !held.Relaunch() {
		t.Fatalf("the claim holds %+v after the death under a compaction, want a fresh relaunch record", held)
	}
	h.pendingTurnCompletes()
	if got := h.budgets(); got.Deaths != 1 {
		t.Errorf("a fresh record's turn after a death under a compaction left budgets %+v, want the death standing", got)
	}
}

// pendingTurnCompletes is a turn of the delivery the claim holds, its start confirming it, its end
// a minute later and its prompt's result after that, which retires it.
func (h *harness) pendingTurnCompletes() {
	h.t.Helper()
	delivery := h.pending()
	h.must(StreamTurnStart{Claim: h.token})
	h.advance(time.Minute)
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	h.must(StreamPromptResult{Claim: h.token, DeliveryID: delivery.ID, AgentInvoked: true})
}
