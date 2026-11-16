package supervise

// Two more cases of the rule that the daemon's own relaunch record ends neither the run of ends
// from outside the pod nor the deaths (inRelaunchRecordTurn), both about which turn is the
// record's: the rule reads the persisted claim, so a daemon restart in the middle of the record's
// turn answers the same; and it counts a turn as the record's only when the record was confirmed at
// or after that turn's start, so a turn of real work that starts while the record still waits for
// its prompt's result is the claim's own work and ends the run.

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

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
// message then starts a turn of the claim's own work, which completes. That turn started after the
// record was confirmed, so it is not the record's, and it ends the run. Taken for the record's
// because the record is still pending, every turn of real work in that window would leave the run
// and the deaths standing, and an end from outside the pod after them would be charged against
// work that got through.
func TestATurnOfRealWorkWhileTheRecordAwaitsItsResultEndsTheRun(t *testing.T) {
	h := newHarness(t)
	h.reach(StateReady)
	h.messageTurnEndedWithItsPod(runtime.PodEndNotOurs)
	h.relaunched()
	p := h.claim().Pending
	if p == nil || !p.Relaunch() {
		t.Fatalf("the relaunched claim holds %+v, want the relaunch's record", p)
	}
	h.must(StreamTurnStart{Claim: h.token})
	h.advance(time.Minute)
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if c := h.claim().Pending; c == nil || c.ID != p.ID {
		t.Fatalf("the record's turn ended with %+v pending, want the record standing until its prompt's result", c)
	}
	h.wantBudgets(Budgets{PodEndsNotOurs: 1})

	h.advance(time.Minute)
	h.must(StreamTurnStart{Claim: h.token})
	h.advance(time.Minute)
	h.mustQuiet(StreamTurnEnd{Claim: h.token})

	h.wantBudgets(Budgets{})
	if stored := h.store.load(h.token).Budgets; stored != (Budgets{}) {
		t.Fatalf("the stored budgets after the turn of real work are %+v, want the run ended", stored)
	}
}
