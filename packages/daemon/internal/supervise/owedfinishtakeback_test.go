package supervise

// One phase rule over the take (decision 8 of dispatch://INFRA-2811, review handoff v4's Q1): where
// a report named the take's number and the store refused its finish (Machine.finishOwed), the
// report that came decides, whichever turn the take awaits. Each of the three functions that take
// a take back — interrupted, takeBackUnstarted and takeBack — reads the take's phase (takePhase),
// and over a finish owed (waitFinish) finishes the task by that report and takes nothing back.

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
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
			// The end-frame bound's finish takes the deadline's retry with the task (row 29c).
			h.wantCalls("Resume", 0)
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

// finishesOf is the finish lines of task number: a harness that ran an earlier task logs its
// finish too.
func (h *harness) finishesOf(number uint64) []loggedRecord {
	var lines []loggedRecord
	for _, r := range h.records.of(finishedLine) {
		if task, _ := r.attr("task"); task.String() == strconv.FormatUint(number, 10) {
			lines = append(lines, r)
		}
	}
	return lines
}

// startsOf is the turn start lines that name task number.
func (h *harness) startsOf(number uint64) []loggedRecord {
	var lines []loggedRecord
	for _, r := range h.records.of(turnStartedLine) {
		if task, _ := r.attr("task"); task.String() == strconv.FormatUint(number, 10) {
			lines = append(lines, r)
		}
	}
	return lines
}

// Row 20: after a daemon start, a take the store held accepted is anchored (anchorRestored).
// Before the first hello the report of the take's own turn arrives naming its number, and the
// store refuses its finish (Machine.finishOwed, set after the anchor exists). The hello puts
// nothing back: the report that came decides. The take's own turn, replayed behind the hello, is
// no second run of the task — one start line names it, of the turn the daemon gave the take — and
// the end-frame bound makes the finish once the store answers, once. The anchor stands in one of
// two ways (plan v104):
//
//   - its given run running at the restart, whose report arrives first saying the take's text was
//     held through it, kept for the hello: the take's own report, of the turn the anchor gave it,
//     spends the anchor (learnTurns), so the hello binds none;
//   - the take awaiting its turn: its own report starts that turn first (startedUnseen), which
//     leaves the anchor, and the hello binds it with no turn given (takeAnchor.given 0), which puts
//     nothing back.
//
// Both pass on the commit under this change too, which already left the take where it was. They
// pin the order; the guard that would hold if the anchor were bound over a given turn is putBack's
// own (waitFinish).
func TestTakeRestoredWhoseOwnReportsFinishIsRefusedBeforeTheHelloIsNotPutBackAtIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before func(h *harness)
		held   bool
		own    uint64
		replay func(h *harness)
	}{
		{name: "its given run running at the restart", before: givenRunRunning, held: true, own: 3,
			replay: func(h *harness) {
				h.mustQuiet(StreamTurnEnd{Claim: h.token})
				h.must(StreamTurnStart{Claim: h.token})
				h.mustQuiet(StreamTurnEnd{Claim: h.token})
			}},
		{name: "awaiting its turn", before: nothingMore, own: 2,
			replay: func(h *harness) {
				h.must(StreamTurnStart{Claim: h.token})
				h.mustQuiet(StreamTurnEnd{Claim: h.token})
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, number := takeAcrossARestart(t, tc.before, withWaits)
			if tc.held {
				h.reportHeldOf(tc.own-1, 0, number)
			}
			h.store.fail("RetireDelivery", errBoom)
			if err := h.m.Handle(h.ctx, RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: tc.own, Task: number}); err == nil {
				t.Fatal("the take's own report whose finish the store refused answered no failure")
			}
			h.m.Wait()

			h.conn.SetStreaming(false)
			h.connect()

			if lines := h.logs.lines(notTheTakesTurnLine); len(lines) != 0 {
				t.Errorf("the hello logged %q over the take whose finish is owed, want nothing put back", lines)
			}
			tc.replay(h)
			if starts := h.startsOf(number); len(starts) != 1 {
				t.Errorf("%d start lines name task %d, want one: the take's own turn, replayed, is no second run of it", len(starts), number)
			}
			h.advance(2 * turnStartBound)
			h.wantCharges()
			h.store.fail("RetireDelivery", nil)
			h.advance(testRPC)
			if p := h.claim().Pending; p != nil {
				t.Fatalf("the claim holds %+v once the store answered, want task %d finished", p, number)
			}
			finished := h.finishesOf(number)
			if len(finished) != 1 {
				t.Fatalf("task %d was finished %d times, want once", number, len(finished))
			}
			wantAttr(t, finished[0], "by", finishedByReport)
			if value, held := h.values.value(h.token); held && value.Number == number {
				t.Fatalf("the bucket still holds %+v, want task %d's value deleted", value, number)
			}
		})
	}
}

// Row 21: a take of a number whose finish is owed (takePhase, waitFinish) is answered refused with
// hold not-owed, handed no text, charges nobody and applies nothing (answeredFromRecord): the
// report came, and no run of the task is owed. The end-frame bound's finish then deletes the value,
// once the store answers. Two takes can meet it:
//
//   - the same process sending its take again after the answer it had lost, the report of the
//     take's turn having come meanwhile, its finish refused: red on the commit under this change,
//     which answered it the kept text, and the worker ran the task again;
//   - a process relaunched after a let-go, here a suspension, whose finish the store refused:
//     every decision while the store refuses finishes the task first or fails before its row
//     (settle), so no relaunch takes the number while its finish is owed, and the relaunched
//     process is handed nothing of it. It passes on the commit under this change too; it pins that
//     the let-go's refusal never hands the number to the next process.
func TestTakeOfANumberWhoseFinishIsOwedIsRefusedNotOwed(t *testing.T) {
	t.Run("a take sent again after its lost answer", func(t *testing.T) {
		h := newHarnessOf(t, queuedClaim(), currentPlugin, withWaits)
		h.manualTakes()
		h.reach(StateReady)
		number := h.takeOf("the task whose answer was lost")
		asked, _ := h.values.value(h.token)
		h.store.fail("RetireDelivery", errBoom)
		if err := h.m.Handle(h.ctx, RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: 1, Task: number}); err == nil {
			t.Fatal("the report whose finish the store refused answered no failure")
		}
		h.m.Wait()

		answer, err := h.takeValue(asked)

		if err != nil || answer.Outcome != TakeOutcomeRefused || answer.Hold != HoldNotOwed || answer.Text != "" {
			t.Fatalf("the take sent again was answered %+v, %v; want it refused not-owed with no text", answer, err)
		}
		h.wantPrompts(1)
		h.wantCharges()
		h.store.fail("RetireDelivery", nil)
		h.advance(testRPC)
		h.wantOwedFinishMade(number)
		h.wantCharges()
	})
	t.Run("a process relaunched after a let-go the store would not let finish", func(t *testing.T) {
		h, number := owedFinishAwaitingItsTurn(t, true)
		if err := h.handle(operatorSuspension(h)); err == nil {
			t.Fatal("the suspension whose finish the store refused answered no failure")
		}
		h.wantState(StateSuspended)
		if err := h.handle(RequestResume{Claim: h.token}); err == nil {
			t.Fatal("the resume while the store refuses the finish answered no failure")
		}
		h.wantState(StateSuspended)
		h.store.fail("RetireDelivery", nil)

		h.must(RequestResume{Claim: h.token})
		h.relaunched()

		h.wantOwedFinishMade(number)
		h.wantPrompts(1)
		h.wantCharges()
	})
}

// Row 22: a take whose finish is owed — the report of its turn named its number and the store
// refused the finish (Machine.finishOwed) — and a later report naming its number with a provider
// error the ladder would wait out, a 429 or a 503, with the store answering by then. The report
// that came first decides: the later one finishes the task once by it, puts nothing on the
// ladder, and leaves no value of the task for the worker to take again (takeToLadder, waitFinish).
// A status that holds the issue meets the same rule through takeBack (the provider error's case of
// TestTakeBackOverAnOwedFinishFinishesOnEveryPath).
//
// The negative control is the commit under this change, where the ladder's clearing branch took
// the take back in memory with no read of its phase (takeBackInMemory): the owed finish went, the
// task waited on the ladder, and its value was written again for a second run.
func TestTakeWhoseFinishIsOwedIsFinishedByALaterReportWithAProviderError(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			h := newHarnessOf(t, queuedClaim(), currentPlugin, withWaits)
			h.manualTakes()
			h.reach(StateReady)
			number := h.takeOf("the task whose report's finish the store refused")
			h.must(StreamTurnStart{Claim: h.token})
			h.mustQuiet(StreamTurnEnd{Claim: h.token})
			h.store.fail("RetireDelivery", errBoom)
			if err := h.m.Handle(h.ctx, RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: 1, Task: number}); err == nil {
				t.Fatal("the report whose finish the store refused answered no failure")
			}
			h.m.Wait()
			h.store.fail("RetireDelivery", nil)

			if err := h.m.Handle(h.ctx, RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: 2, Task: number,
				ProviderError: &ProviderError{Status: status, Retryable: true}}); err != nil {
				t.Fatalf("the later report with a %d answered %v", status, err)
			}
			h.m.Wait()

			h.wantOwedFinishMade(number)
			h.wantUnarmed("after the later report", TimerPromptRetry)
			h.advance(promptRetryMax)
			if value, held := h.values.value(h.token); held && value.Number == number {
				t.Fatalf("the bucket holds %+v past the ladder's longest wait, want no value of task %d to take again", value, number)
			}
			h.wantPrompts(1)
			h.wantCharges()
		})
	}
}

// Row 22a: as row 22, with the later report's own finish refused too, and the daemon restarted
// before a finish lands; in a second case, three newer reports, of the host's own turns after the
// take's, land before the restart. While the finish is owed the window keeps the clean report that
// named the take, as it keeps a report never applied (recordTurnOutcome), and the restored machine
// reads that report rather than the later provider error's (owedReport): the task is finished
// once, by that report, nothing goes on the ladder, and no value of it is left to take again.
//
// The negative control is the commit under this change. With no newer report, the restore read
// the newest report naming the take, the 429, and took the task to the ladder; with three, the
// window had dropped both reports naming the take, so the restore owed nothing and no bound
// finished the task.
func TestTakeWhoseFinishIsOwedAcrossARestartIsFinishedByItsCleanReportNotALaterProviderError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		newer int
	}{
		{"a restart after the later report", 0},
		{"three newer reports, then a restart", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarnessOf(t, queuedClaim(), currentPlugin, withWaits)
			h.manualTakes()
			h.reach(StateReady)
			number := h.takeOf("the task whose report's finish the store refused")
			h.must(StreamTurnStart{Claim: h.token})
			h.mustQuiet(StreamTurnEnd{Claim: h.token})
			h.store.fail("RetireDelivery", errBoom)
			if err := h.reportTurn(number, nil); err == nil {
				t.Fatal("the report whose finish the store refused answered no failure")
			}
			if err := h.reportTurn(number, &ProviderError{Status: 429, Retryable: true}); err == nil {
				t.Fatal("the later report with a 429, whose finish the store refused too, answered no failure")
			}
			for range tc.newer {
				h.hostsRun()
				if err := h.reportTurn(0, nil); err != nil {
					t.Fatalf("the report of a turn of the host's own answered %v", err)
				}
			}
			h.store.fail("RetireDelivery", nil)

			h.restart()
			h.connect()
			h.advance(testRPC)

			h.wantOwedFinishMade(number)
			h.wantUnarmed("after the restart", TimerPromptRetry)
			h.advance(promptRetryMax)
			if value, held := h.values.value(h.token); held && value.Number == number {
				t.Fatalf("the bucket holds %+v past the ladder's longest wait, want no value of task %d to take again", value, number)
			}
			h.wantPrompts(1)
			h.wantCharges()
		})
	}
}

// Row 23: a take whose finish is owed reads, to every reader of whether the take's turn ran, as a
// take whose turn ran, since the report that came decides (takesTurnRan, takeAwaitsTurn). Over a
// take still awaiting its turn — Q1's state, made by hand as rows 17 to 19 make it, since no live
// order at this head reaches it: a report ahead of its turn's start marks that turn started before
// the finish (startedUnseen), a daemon start does the same (NewMachine), and no put-back moves a
// take whose finish is owed (putBack) —
//
//   - a turn that starts is given to no take, its line names no task, and its end arms the
//     end-frame bound again, which makes the finish once the store answers (turnStarted, turnEnded);
//   - the finish earns the serving credit a finished take's turn earns, and the entry the task
//     carried is not put back to be queued again (retireTask);
//   - a newer task takes its place, the task it replaces finished by its report rather than the
//     newer one refused as behind a turn that has not started (queue, retirePending).
//
// Two cases are live. A newer task queued over a take whose turn ran and whose finish is owed
// retired the task unfinished, the owed finish dropped (retirePending). And the operator's
// compaction of a migrated worker's take awaiting its turn, whose phase is no wait's (waitNone),
// took it back with no line; the line reads the phase as the take-back does (compactStart).
//
// The negative control is the commit under this change, where each reader asked whether the
// take's turn started alone (Delivery.awaitsTurn, Delivery.turnStarted), and the compaction's line
// whether the take was in waitTake.
func TestTakeWhoseFinishIsOwedReadsAsATakeWhoseTurnRan(t *testing.T) {
	t.Run("a turn starts", func(t *testing.T) {
		h, number := owedFinishAwaitingItsTurn(t, true)

		h.must(StreamTurnStart{Claim: h.token})

		starts := h.records.of(turnStartedLine)
		if task, named := starts[len(starts)-1].attr("task"); named {
			t.Errorf("the start names task %s, want it no turn of the task, whose report came", task)
		}
		h.wantAwaitingItsTurn(number, "after the start")
		h.advance(testRPC)
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		if !h.armed(TimerFinish) {
			t.Fatal("the turn's end armed no end-frame bound over the owed finish")
		}
		h.store.fail("RetireDelivery", nil)
		h.advance(testRPC)
		h.wantOwedFinishMade(number)
		h.wantCharges()
	})
	t.Run("its finish earns the serving credit", func(t *testing.T) {
		h, number := owedFinishOfAwaitingItsTurn(t, RequestDeliver{Task: "implement the plan", ID: "outbox:40", Row: 40,
			Phase: phase.Implementing, Generation: 7, Entry: 1}, false)

		h.advance(testRPC)

		h.wantOwedFinishMade(number)
		for where, c := range map[string]Claim{"the machine": h.claim(), "the store": h.store.load(h.token)} {
			if c.GivenEntry != 1 || c.ServedEntry != 1 || c.ServingGeneration != 7 {
				t.Fatalf("%s records entry %d given and %d served, run %d; want entry 1 given and served, run 7: the task's turn ran",
					where, c.GivenEntry, c.ServedEntry, c.ServingGeneration)
			}
		}
	})
	t.Run("a newer task over the take awaiting its turn", func(t *testing.T) {
		h, number := owedFinishAwaitingItsTurn(t, false)

		h.must(RequestDeliver{Claim: h.token, Task: "the newer task"})

		h.wantOwedFinishMade(number)
		if p := h.pending(); p.Task != "the newer task" {
			t.Fatalf("the claim holds %+v, want the newer task queued", p)
		}
	})
	t.Run("a newer task over the take whose turn ran", func(t *testing.T) {
		h := newHarnessOf(t, queuedClaim(), currentPlugin, withWaits)
		h.manualTakes()
		h.reach(StateReady)
		number := h.takeOf("the task whose report's finish the store refused")
		h.must(StreamTurnStart{Claim: h.token})
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.store.fail("RetireDelivery", errBoom)
		if err := h.reportTurn(number, nil); err == nil {
			t.Fatal("the report whose finish the store refused answered no failure")
		}
		h.store.fail("RetireDelivery", nil)

		h.must(RequestDeliver{Claim: h.token, Task: "the newer task"})

		h.wantOwedFinishMade(number)
		if p := h.pending(); p.Task != "the newer task" {
			t.Fatalf("the claim holds %+v, want the newer task queued", p)
		}
	})
	t.Run("the operator's compaction of a migrated worker's take", func(t *testing.T) {
		h := newHarness(t)
		h.manualTakes()
		h.reach(StateReady)
		number := h.takeOf("the task a migrated worker took")

		if err := h.m.Compact(h.ctx, testCompactInstructions); err != nil {
			t.Fatalf("the compaction answered %v", err)
		}
		h.m.Wait()

		if p := h.pending(); p.Number != number || p.taken() {
			t.Fatalf("the claim holds %+v, want task %d taken back by the compaction", p, number)
		}
		if lines := h.logs.lines("the compaction aborts the turn this task's take was to start"); len(lines) != 1 {
			t.Fatalf("logged %q, want the compaction's abort of the take's turn logged once", lines)
		}
	})
}
