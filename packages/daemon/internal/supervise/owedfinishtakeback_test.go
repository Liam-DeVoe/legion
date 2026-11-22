package supervise

// One phase rule over the take (decision 8 of dispatch://INFRA-2811, review handoff v4's Q1): where
// a report named the take's number and the store refused its finish (Machine.finishOwed), the
// report that came decides, whichever turn the take awaits. Each of the three functions that take
// a take back — interrupted, takeBackUnstarted and takeBack — reads the take's phase (takePhase),
// and over a finish owed (waitFinish) finishes the task by that report and takes nothing back.

import (
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// Every caller of the three take-backs, each driven over a take awaiting its turn while its
// finish is owed (owedFinishAwaitingItsTurn), ends with the task finished once by its report,
// nothing of it taken back, and no value of it left for a process to take (wantOwedFinishMade).
// The callers, at the head this test was written on (77beb282):
//
//   - interrupted (delivery.go): settle (delivery.go:1063), here at the claim's failure; died
//     (machine.go:3660); replaceNow's take-back (repoint.go:211), made at once by launchReplacing
//     (machine.go:2982) for a stale address (repoint.go:42), the stale-plugin relaunch
//     (plugin.go:362) and the stall backstop (stallbackstop.go:179), or carried to the wake's turn
//     (machine.go:2992, launchWake at wake.go:550); the wake's own take-back (wake.go:547);
//     reportMissed (reportwait.go:360), for the wait's bound and the held suspension's deadline;
//     the held suspension's deadline in a turn and under a compaction (suspend.go:377, :387);
//     suspendOverADeadProcess (suspend.go:682); and reconnectDeadline (waits.go:334), to which
//     this change adds the relaunch at the reconnect deadline of a hello no answer followed;
//   - takeBackUnstarted (delivery.go): a retirement (machine.go:4047) and a suspension
//     (suspended, suspend.go:516);
//   - takeBack (take.go): the operator's compaction (compactStart, compact.go:350), R5's second
//     wait (waits.go:281) and a provider error's report (takeToLadder, providererror.go:98).
//
// A caller that runs only in a turn meets the owed finish with the take's turn started, since a
// turn's start over a take awaiting its turn is that take's turn (turnStarted): those cases start
// a turn first. R5's second wait and the bound of the report wait never arm over an owed finish
// (secondWaitNow, reportBounded), which their cases show by letting both bounds pass; and a
// compaction under which a suspension's deadline would come finds no owed take to cut, since its
// own start finishes it (the compaction's case). The wake's own take-back with no replacement's
// carried on it (wake.go:547) is reached only by a wake no replacement raised over a live process,
// which no take awaiting its turn meets; its take-back is interrupted's all the same.
//
// The negative control is the commit under this change, where each of them gave the take back
// unstarted, or cut off as a turn, and the task ran again under its number.
func TestTakeBackOverAnOwedFinishFinishesOnEveryPath(t *testing.T) {
	// refusedUntil holds the store refusing the finish until after the caller ran, for a caller
	// that comes after the end-frame bound would have made the finish: the caller's own finish is
	// refused and takes nothing back, and the bound makes it once the store answers.
	refusedUntil := func(t *testing.T, h *harness, number uint64, caller func(h *harness), retry time.Duration) {
		t.Helper()
		caller(h)
		if p := h.pending(); p.Number != number || !p.taken() {
			t.Fatalf("the caller left %+v while the store refused the finish, want task %d's take kept", p, number)
		}
		h.store.fail("RetireDelivery", nil)
		h.advance(retry)
	}
	for _, tc := range []struct {
		name     string
		refusing bool
		set      []func(*Deps)
		run      func(t *testing.T, h *harness, number uint64)
	}{
		{name: "settle, at the claim's failure", run: func(t *testing.T, h *harness, number uint64) {
			for sequence := uint64(2); sequence <= 4; sequence++ {
				h.report(sequence, true, "the provider refused the turn")
			}
			h.wantState(StateFailed)
		}},
		{name: "a death", run: func(t *testing.T, h *harness, number uint64) {
			h.observe(runtime.Gone)
		}},
		{name: "a replacement for a stale address, made at once", run: func(t *testing.T, h *harness, number uint64) {
			h.observe(runtime.StaleAddress)
			h.wantState(StateLaunching)
		}},
		{name: "the stale-plugin relaunch", run: func(t *testing.T, h *harness, number uint64) {
			h.m.mu.Lock()
			h.m.claim.PluginContract = h.deps.PluginContract + 1
			h.m.mu.Unlock()
			h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})
			h.wantState(StateLaunching)
		}},
		{name: "a replacement carried to the wake's turn", refusing: true, set: []func(*Deps){pacedDeps(wakeRate)}, run: func(t *testing.T, h *harness, number uint64) {
			h.observe(runtime.StaleAddress)
			waiting := h.wantWaitingWake(WakeLaunch)
			refusedUntil(t, h, number, func(h *harness) { h.advance(waiting.At.Sub(h.clock.Now())) }, testRPC)
		}},
		{name: "the stall backstop, in a turn", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.must(StreamTurnStart{Claim: h.token})
			last := h.clock.Now()
			h.advance(backstopBound + time.Minute)
			h.store.fail("RetireDelivery", nil)
			h.must(h.backstop(last))
			h.wantState(StateLaunching)
		}},
		{name: "the report wait's bound, which arms over no owed finish", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.observe(runtime.Alive)
			refusedUntil(t, h, number, func(h *harness) { h.advance(2 * turnStartBound) }, testRPC)
			if lines := h.logs.lines(reportMissedLine); len(lines) != 0 {
				t.Fatalf("logged %q, want the report wait's bound never armed", lines)
			}
		}},
		{name: "the held suspension's deadline, in a turn", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.must(StreamTurnStart{Claim: h.token})
			if err := h.handle(operatorSuspension(h)); !errors.Is(err, ErrSuspendHeld) {
				t.Fatalf("the suspension answered %v, want it held for the turn", err)
			}
			refusedUntil(t, h, number, func(h *harness) { h.advance(testStop) }, testProbe)
			h.wantState(StateSuspended)
		}},
		{name: "a suspension over a process the daemon found gone, in a turn", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.must(StreamTurnStart{Claim: h.token})
			if err := h.handle(RuntimeObservation{Observation: runtime.Observation{Locator: h.locator(), Kind: runtime.Gone, At: h.clock.Now()}}); err == nil {
				t.Fatal("the death was judged whole while the store refused the finish")
			}
			h.store.fail("RetireDelivery", nil)
			if err := h.handle(operatorSuspension(h)); err != nil {
				t.Fatalf("the suspension over the dead process answered %v", err)
			}
			h.wantState(StateSuspended)
		}},
		{name: "the reconnect deadline of a close", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.reconnectLater()
			refusedUntil(t, h, number, func(h *harness) { h.advance(deadline) }, testProbe)
			h.wantCalls("Resume", 1)
		}},
		{name: "the reconnect deadline of a hello no answer followed", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.reconnectLater()
			h.helloOf(true, false)
			refusedUntil(t, h, number, func(h *harness) { h.advance(deadline) }, testProbe)
			h.wantCalls("Resume", 1)
		}},
		{name: "a retirement", run: func(t *testing.T, h *harness, number uint64) {
			h.must(RequestStop{Claim: h.token})
			h.wantState(StateRetired)
		}},
		{name: "a suspension", run: func(t *testing.T, h *harness, number uint64) {
			h.must(operatorSuspension(h))
			h.wantState(StateSuspended)
		}},
		{name: "the operator's compaction", run: func(t *testing.T, h *harness, number uint64) {
			if err := h.m.Compact(h.ctx, testCompactInstructions); err != nil {
				t.Fatalf("the compaction answered %v, want the claim given back", err)
			}
			h.m.Wait()
		}},
		{name: "R5's second wait, which arms over no owed finish", refusing: true, run: func(t *testing.T, h *harness, number uint64) {
			h.observe(runtime.Alive)
			refusedUntil(t, h, number, func(h *harness) { h.advance(2 * turnStartBound) }, testRPC)
			h.wantCharges()
		}},
		{name: "a provider error's report", run: func(t *testing.T, h *harness, number uint64) {
			if err := h.m.Handle(h.ctx, RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: 2,
				Task: number, ProviderError: &ProviderError{Status: 400}}); err != nil {
				t.Fatalf("the provider error's report answered %v", err)
			}
			h.m.Wait()
			h.wantState(StateFailed)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, number := owedFinishAwaitingItsTurn(t, tc.refusing, tc.set...)

			tc.run(t, h, number)

			h.wantOwedFinishMade(number)
		})
	}
}

// The live order that reaches a take whose finish is owed while a report of the run the daemon
// gave it says that run was not its own (plan v99): the take is given a run of the host's own, G,
// whose start and end the daemon sees; the take's own turn, T, runs, and its report naming the
// number comes ahead of T's start frame, and the store refuses its finish (Machine.finishOwed);
// then G's report, naming no task, arrives late, of the turn the daemon gave the take, with no
// start seen since. That report came after the one that decides: the take is left where T's
// report put it, its finish owed (takePhase, waitFinish), and is put back to await no turn. No
// bound takes it back while the store refuses, T's late start frame is no turn of the task, and
// the end-frame bound makes the finish once the store answers, once, by T's report.
//
// The negative control is the commit under this change, where G's late report put back the take
// whose finish was owed (notTheTakesTurn, putBack), and T's start frame was then given the take
// as its turn, a second run of the task.
func TestTakeWhoseFinishIsOwedIsNotPutBackByTheLateReportOfItsGivenRun(t *testing.T) {
	h := newHarnessOf(t, queuedClaim(), currentPlugin, withWaits)
	h.manualTakes()
	h.reach(StateReady)
	number := h.takeOf("the task whose own turn reported ahead of its start")
	h.hostsRun()
	h.wantTakesTurnStanding(number, "after G's end")
	h.store.fail("RetireDelivery", errBoom)
	if err := h.m.Handle(h.ctx, RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: 2, Task: number}); err == nil {
		t.Fatal("T's report whose finish the store refused answered no failure")
	}
	h.m.Wait()

	h.reportOf(1, 0)

	if lines := h.logs.lines(notTheTakesTurnLine); len(lines) != 0 {
		t.Errorf("G's late report logged %q over the take whose finish is owed, want it put back to await no turn", lines)
	}
	if p := h.pending(); p.Number != number || !p.taken() {
		t.Fatalf("after G's late report the claim holds %+v, want task %d's take kept with its finish owed", p, number)
	}
	h.advance(2 * turnStartBound)
	h.wantCharges()
	if lines := h.logs.lines(reportMissedLine); len(lines) != 0 {
		t.Fatalf("logged %q, want no bound to take the task back", lines)
	}
	h.must(StreamTurnStart{Claim: h.token})
	starts := h.records.of(turnStartedLine)
	if task, named := starts[len(starts)-1].attr("task"); named {
		t.Errorf("T's late start frame names task %s, want it no turn of the task, whose report came", task)
	}
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	h.store.fail("RetireDelivery", nil)
	h.advance(testRPC)
	h.wantRunOnce(number)
	h.wantCharges()
}
