package supervise

// Which turn is the take's (dispatch://INFRA-2811). The daemon gives the first turn that starts
// after an accepted take to that take (turnStarted); a worker answered while a run no take
// started is running holds the text until that run is over and reports the run as no task's.
// These tests pin what the daemon then does: a report naming no task gives the take its turn
// back, a take sent again after the turn it was given started is answered the same text, and the
// end-frame bound a daemon start leaves to a restored take's turn runs from the worker's first
// act, not from the start.

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// currentPlugin is a harness whose worker's plugin names its task's number in every turn's
// report: the daemon's own contract is TaskTakeContract, which the harness's registration records
// for the claim (register).
func currentPlugin(d *Deps) { d.PluginContract = TaskTakeContract }

// givesARunNoTakeStartedToTheTake walks the claim to an accepted take of a task, then a run no
// take started — a background result's, which starts before the take's answer reached the worker
// — that the daemon gives to the take, and that run's end, with no report behind it yet. It
// answers the text the take was answered with.
func (h *harness) givesARunNoTakeStartedToTheTake() string {
	h.t.Helper()
	h.manualTakes()
	h.reach(StateReady)
	h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
	text := h.mustTake()
	h.must(StreamTurnStart{Claim: h.token})
	if p := h.pending(); !p.turnStarted() || p.TakenText != text {
		h.t.Fatalf("the run that started after the take is %+v, want it given to the take, its text kept", p)
	}
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	return text
}

// A worker whose plugin names its task's number reports the run the daemon gave its take as no
// task's: that run was not the take's. The take awaits its turn again, with the text it was
// answered, in memory and in the store, and nothing finishes the task meanwhile, since only a
// report naming its number finishes such a claim's task; the turn that starts next is the
// take's, and its report finishes the task. A worker whose plugin is older than the take names no
// number in any report, so its claim keeps the end-frame finish, which is what finishes a
// migrated old worker's task (decision 8).
//
// The negative control is the report's give-back taken out (notTheTakesTurn): the take then
// stays on the run that was not its own, and the next turn, the held text's, is no take's.
func TestTurnReportNamingNoTaskGivesTheTakeItsTurnBack(t *testing.T) {
	t.Run("a plugin that names its task's number", func(t *testing.T) {
		h := newHarnessOf(t, queuedClaim(), currentPlugin)
		text := h.givesARunNoTakeStartedToTheTake()
		number := h.pending().Number

		if err := h.reportTurn(0, nil); err != nil {
			t.Fatalf("report the run as no task's: %v", err)
		}

		if p := h.pending(); !p.awaitsTurn() || p.TakenText != text {
			t.Fatalf("after the report the task is %+v, want its take awaiting its turn again with the text %q", p, text)
		}
		if stored, ok := h.store.delivery(h.token); !ok || !stored.awaitsTurn() || stored.TakenText != text {
			t.Fatalf("the store holds %+v, want the take awaiting its turn again", stored)
		}
		if lines := h.logs.lines(notTheTakesTurnLine); len(lines) != 1 {
			t.Fatalf("logged %q, want the take's turn given back once", lines)
		}
		h.advance(2 * testRPC)
		if p := h.claim().Pending; p == nil || p.Number != number || !p.awaitsTurn() {
			t.Fatalf("two end-frame bounds after the report the claim holds %+v, want task %d still awaiting its turn", p, number)
		}

		h.must(StreamTurnStart{Claim: h.token})
		if p := h.pending(); !p.turnStarted() {
			t.Fatalf("the turn after the report is %+v, want it the take's", p)
		}
		h.must(StreamTurnEnd{Claim: h.token})
		if p := h.claim().Pending; p != nil {
			t.Fatalf("pending %+v after the take's own turn was reported, want task %d finished", p, number)
		}
		finished := h.records.of(finishedLine)
		if len(finished) != 1 {
			t.Fatalf("%q was logged %d times, want once", finishedLine, len(finished))
		}
		wantAttr(t, finished[0], "by", finishedByReport)
	})
	t.Run("a plugin older than the take finishes at the end frame", func(t *testing.T) {
		h := newHarness(t)
		h.givesARunNoTakeStartedToTheTake()

		if err := h.reportTurn(0, nil); err != nil {
			t.Fatalf("report the run as no task's: %v", err)
		}
		if lines := h.logs.lines(notTheTakesTurnLine); len(lines) != 0 {
			t.Fatalf("logged %q, want no turn given back for a plugin that names no number", lines)
		}
		h.advance(testRPC)

		if p := h.claim().Pending; p != nil {
			t.Fatalf("pending %+v an end-frame bound after the run, want the task finished by its end", p)
		}
		finished := h.records.of(finishedLine)
		if len(finished) != 1 {
			t.Fatalf("%q was logged %d times, want once", finishedLine, len(finished))
		}
		wantAttr(t, finished[0], "by", finishedByEndFrame)
	})
}

// A take a report gave its turn back is in R5's second wait again from that report: a worker
// that then starts no turn is charged one second bound after it, and the take is taken back.
//
// The negative control is the report's give-back taken out (notTheTakesTurn): the take then
// stays on the run that was not its own, no second wait is on it, and no bound charges anyone.
func TestChargeTheSecondWaitOnATakeAReportGaveItsTurnBack(t *testing.T) {
	h := waitsHarness(t, currentPlugin)
	h.givesARunNoTakeStartedToTheTake()
	if err := h.reportTurn(0, nil); err != nil {
		t.Fatalf("report the run as no task's: %v", err)
	}

	h.advance(turnStartBound - time.Second)
	h.wantCharges()
	h.advance(time.Second)

	h.wantCharges(WaitTurnStart)
	if p := h.pending(); p.taken() {
		t.Fatalf("pending %+v after the second bound, want the take taken back", p)
	}
}

// A take of the number the claim holds as its accepted, unfinished take is a take whose answer
// was lost, whether or not the turn the daemon gave the take has started: a run that started while
// the worker waited on its answer is given to the take, and a worker refused in-turn then would
// never run the text. It is answered the text the first answer carried, and applies nothing
// again — no write, no second adoption, no second telling of the workflow — in the daemon that
// answered it and in one a restart built over the stored claim, which keeps the text through the
// turn's start (Delivery.TakenText, TurnStarted).
//
// The negative control is the repeat answered only while the take awaits its turn
// (answeredFromRecord's case before this change): the take is then refused in-turn.
func TestTakeRepeatedAfterItsTurnStartedIsAnsweredTheSameAndRecordsNothing(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "in the same daemon", true: "after a daemon restart"}[restart], func(t *testing.T) {
			h := newHarness(t)
			confirmed := 0
			h.deps.TaskConfirmed = func(Claim) { confirmed++ }
			h.deps.Identity = adoptingIdentity
			h.restart()
			h.manualTakes()
			h.reach(StateReady)
			h.must(RequestDeliver{Claim: h.token, Task: "implement the plan", Phase: phase.Implementing, Generation: 7})
			first := h.mustTake()
			h.must(StreamTurnStart{Claim: h.token})
			if restart {
				h.restart()
			}
			puts, log := len(h.store.history()), h.store.deliveryLog()

			answer := h.take()

			if answer.Outcome != TakeOutcomeAccepted || answer.Text != first {
				t.Fatalf("the take sent again after its turn started = %+v, want it accepted with %q", answer, first)
			}
			answered := h.records.of(answeredLine)
			wantAttr(t, answered[len(answered)-1], "repeat", "true")
			if got := len(h.store.history()); got != puts {
				t.Errorf("the repeated take wrote the claim %d times, want none", got-puts)
			}
			if got := h.store.deliveryLog(); len(got) != len(log) {
				t.Errorf("the repeated take wrote the delivery: %v", got[len(log):])
			}
			if confirmed != 1 {
				t.Errorf("the workflow was told %d times, want once", confirmed)
			}
			h.wantCalls("AdoptWorkingCopy", 1)
			if p := h.pending(); !p.turnStarted() || p.TakenText != first {
				t.Fatalf("pending %+v after the repeated take, want the take's turn still started with its text", p)
			}
		})
	}
}

// A report the worker sends again to a daemon that restarted between the take's turn's end and
// the report reaches its task. For a claim whose plugin names no number — the one claim an
// end-frame bound watches (endFrameFinishes) — the bound a restored ready or idle claim waits on
// for that turn runs from the worker's first act after the start, not from the start, so the time
// no daemon served the worker is no time its report had. A report of a provider error then takes
// the task to the ladder, rather than finding it finished by a bound that ran first; and a worker
// that comes back with a hello and no report has the bound run from that hello. (The harness's
// plugin contract is below TaskTakeContract.)
//
// The negative control is the bound armed at the machine's build again (NewMachine): it passes
// while the worker is still away, and the task is finished before its report lands.
func TestTurnOutcomeSentAgainToARestartedDaemonReachesItsTask(t *testing.T) {
	t.Run("a provider error's report takes the task to the ladder", func(t *testing.T) {
		h := newHarness(t)
		h.reach(StateReady)
		h.takesTurnEndsWithItsReportToCome()
		number := h.pending().Number

		h.outage(time.Minute)
		h.advance(2 * testRPC)
		if p := h.claim().Pending; p == nil || p.Number != number || !p.turnStarted() {
			t.Fatalf("two end-frame bounds after the restart, with no act of the worker's, the claim holds %+v, want task %d's turn still waiting for its report", p, number)
		}
		if err := h.reportTurn(number, &ProviderError{Message: "overloaded_error", Status: 529}); err != nil {
			t.Fatalf("send the report of the provider error again: %v", err)
		}

		if p := h.pending(); p.Number != number || p.taken() || p.ProviderErrorRetries != 1 {
			t.Fatalf("after the report the task is %+v, want task %d taken back for the ladder", p, number)
		}
		if finished := h.records.of(finishedLine); len(finished) != 0 {
			t.Fatalf("%q was logged %d times, want the task kept for the ladder", finishedLine, len(finished))
		}
	})
	t.Run("a hello with no report runs the bound from the hello", func(t *testing.T) {
		h := newHarness(t)
		h.reach(StateReady)
		h.takesTurnEndsWithItsReportToCome()

		h.outage(time.Minute)
		h.advance(2 * testRPC)
		h.connect()
		h.advance(testRPC - time.Second)
		if p := h.claim().Pending; p == nil || !p.turnStarted() {
			t.Fatalf("less than a bound after the hello the claim holds %+v, want its take's turn still waiting for its report", p)
		}
		h.advance(time.Second)

		if p := h.claim().Pending; p != nil {
			t.Fatalf("pending %+v a bound after the hello, want the task finished by its turn's end", p)
		}
		finished := h.records.of(finishedLine)
		if len(finished) != 1 {
			t.Fatalf("%q was logged %d times, want once", finishedLine, len(finished))
		}
		wantAttr(t, finished[0], "by", finishedByEndFrame)
	})
}

// A claim whose plugin names its task's number has its task finished by the report that names
// it and by nothing else (decision 8 of dispatch://INFRA-2811, plan v21): the end of the take's
// turn arms no bound, so a report that comes long after the end, in the same daemon or across a
// restart, still finds the task and finishes it.
//
// The negative control is the end-frame bound armed for such a claim again (armEndFrameBound
// arming whatever the plugin): the task is then finished by the end of its turn, and the report
// finds nothing.
func TestTurnOutcomeAloneFinishesTheTaskOfAPluginThatNamesItsNumber(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "in the same daemon", true: "across a daemon restart"}[restart], func(t *testing.T) {
			h := newHarnessOf(t, queuedClaim(), currentPlugin)
			h.reach(StateReady)
			h.takesTurnEndsWithItsReportToCome()
			number := h.pending().Number
			if restart {
				h.outage(time.Minute)
				h.connect()
			}

			h.advance(10 * testRPC)
			if p := h.claim().Pending; p == nil || p.Number != number || !p.turnStarted() {
				t.Fatalf("ten end-frame bounds after the turn's end the claim holds %+v, want task %d's turn still waiting for its report", p, number)
			}
			if finished := h.records.of(finishedLine); len(finished) != 0 {
				t.Fatalf("%q was logged %d times before the report, want none", finishedLine, len(finished))
			}
			h.mustReport(nil)

			if p := h.claim().Pending; p != nil {
				t.Fatalf("pending %+v after the report, want task %d finished", p, number)
			}
			finished := h.records.of(finishedLine)
			if len(finished) != 1 {
				t.Fatalf("%q was logged %d times, want once", finishedLine, len(finished))
			}
			wantAttr(t, finished[0], "by", finishedByReport)
		})
	}
}

// An end report sent again after its first attempt finished its task — the worker heard no
// answer — is answered as the first was, with no failure for the worker, and applies nothing
// again (decision 8 of dispatch://INFRA-2811, plan v21): the task it named stays finished once,
// and the claim's next task, whose take's turn has started since, is not touched, though the
// report names a number no take of the claim holds now.
//
// The negative control is the repeat applied as a first report (turnOutcome with no stop at a
// repeat): it then reads as the report of the next task's turn naming another number, and gives
// that take its turn back.
func TestTurnOutcomeRepeatedAfterItsTaskFinishedAnswersTheSameAndAppliesNothing(t *testing.T) {
	h := newHarnessOf(t, queuedClaim(), currentPlugin)
	h.reach(StateReady)
	h.must(RequestDeliver{Claim: h.token, Task: "the first task"})
	number := h.pending().Number
	h.must(StreamTurnStart{Claim: h.token})
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	h.worker.sequence++
	first := RequestTurnOutcome{Claim: h.token, Generation: h.generation(), Sequence: h.worker.sequence, Task: number}
	if err := h.m.Handle(h.ctx, first); err != nil {
		t.Fatalf("report the first task's turn: %v", err)
	}
	h.m.Wait()
	h.must(RequestDeliver{Claim: h.token, Task: "the second task"})
	h.must(StreamTurnStart{Claim: h.token})
	second := h.pending()
	if second.Number == number || !second.turnStarted() {
		t.Fatalf("the claim holds %+v, want the second task's take with its turn started", second)
	}
	run := h.claim().RefusedRun()

	if err := h.m.Handle(h.ctx, first); err != nil {
		t.Fatalf("the report sent again = %v, want it answered as the first was", err)
	}
	h.m.Wait()

	if p := h.pending(); p.Number != second.Number || !p.turnStarted() {
		t.Fatalf("after the repeated report the claim holds %+v, want task %d's turn still started", p, second.Number)
	}
	if finished := h.records.of(finishedLine); len(finished) != 1 {
		t.Fatalf("%q was logged %d times, want once: the repeat finishes nothing again", finishedLine, len(finished))
	}
	if lines := h.logs.lines("a turn's report repeats one already applied"); len(lines) != 1 {
		t.Fatalf("the lines that say the report was a repeat are %q, want one", lines)
	}
	if got := h.claim().RefusedRun(); got != run {
		t.Fatalf("the refused run is %d after the repeat, want %d as before it", got, run)
	}
}
