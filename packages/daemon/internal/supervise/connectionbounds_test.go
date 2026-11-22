package supervise

// The bounds and the connection (decision 8 of dispatch://INFRA-2811, its state table). Three
// bounds judge the worker by the frames its connection carries: R5's second wait (TimerTurnStart),
// the wait for a take's report (TimerReport), with nothing held as with work held, and R5's first
// wait over a value the worker reported seeing (TimerTake). Each runs only while the claim's
// connection shows the machine every frame of its turns: a close disarms all three, none fires
// while no connection stands, and each is armed again whole once the agent answers the ask a
// reconnect hello makes, or a turn start or end frame arrives on the new connection. Each test is
// one row of the plan's table, named so the check that row names selects it (c2's Take, c5's
// Charge, c3's Relaunch); the row's number heads its comment.

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
)

// longReconnect sets the reconnect deadline (the registration deadline's bound, Timeouts.Boot ×
// RegistrationIntervals) past both of R5's bounds, for a row that lets a whole bound pass while no
// connection stands: at the daemon's defaults that deadline comes first, and relaunches the worker.
func longReconnect(d *Deps) { d.Timeouts.Boot = turnStartBound }

// boundsHarness is a claim on the current plugin with R5's two bounds set, ready, every take of it
// driven by the test, and whatever else set changes of its deps.
func boundsHarness(t *testing.T, set ...func(*Deps)) *harness {
	t.Helper()
	h := waitsHarness(t, append([]func(*Deps){currentPlugin}, set...)...)
	h.manualTakes()
	h.reach(StateReady)
	return h
}

// helloOf is the shim's hello on the claim's connection after a close, saying its backlog is
// whole or not, its agent in a turn as streaming says. What the hello asks is answered when the
// test delivers that answer (answerAsk), behind whatever frames the shim replays first.
func (h *harness) helloOf(whole, streaming bool) {
	h.t.Helper()
	h.conn.SetStreaming(streaming)
	h.conns.Register(h.token, h.conn)
	h.must(StreamHello{Claim: h.token, Generation: h.generation(), BacklogWhole: whole})
}

// answerAsk is the agent's answer to the last ask of the claim's connection whether it is in a
// turn, as the worker stream delivers it: behind every frame the shim sent before it.
func (h *harness) answerAsk(streaming bool) {
	h.t.Helper()
	h.must(StreamTurnAnswered{Claim: h.token, Streaming: streaming, Ask: h.conn.LastAsk()})
}

// dueOf is when timer kind fires, and whether it is armed.
func (h *harness) dueOf(kind TimerKind) (time.Time, bool) {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	a, ok := h.m.timers[kind]
	return a.due, ok
}

// wantArmedFromNow holds that timer kind is armed whole from this moment: bound from now.
func (h *harness) wantArmedFromNow(kind TimerKind, bound time.Duration, when string) {
	h.t.Helper()
	due, ok := h.dueOf(kind)
	if want := h.clock.Now().Add(bound); !ok || !due.Equal(want) {
		h.t.Fatalf("%s the %s timer is armed %t, due %s; want it armed whole from now, due %s", when, kind, ok, due, want)
	}
}

// wantUnarmed holds that no timer of kinds is armed.
func (h *harness) wantUnarmed(when string, kinds ...TimerKind) {
	h.t.Helper()
	for _, kind := range kinds {
		if due, ok := h.dueOf(kind); ok {
			h.t.Fatalf("%s the %s timer is armed, due %s; want it unarmed", when, kind, due)
		}
	}
}

// wantAwaitingItsTurn holds that the take of task number stands, awaiting its turn: nothing took
// it back.
func (h *harness) wantAwaitingItsTurn(number uint64, when string) {
	h.t.Helper()
	if p := h.pending(); p.Number != number || !p.awaitsTurn() || p.Interrupted {
		h.t.Fatalf("%s the claim holds %+v, want task %d's take standing, awaiting its turn", when, p, number)
	}
}

// wantRunOnce holds that task number was taken once and finished once, by its report, and that
// nothing of it is left to take: one accepted take, one finish, and no value of it in the bucket.
func (h *harness) wantRunOnce(number uint64) {
	h.t.Helper()
	takes := 0
	for _, took := range h.prompts() {
		if took.Number == number {
			takes++
		}
	}
	if takes != 1 {
		h.t.Fatalf("task %d was taken %d times (%+v), want once", number, takes, h.prompts())
	}
	if p := h.claim().Pending; p != nil && p.Number == number {
		h.t.Fatalf("the claim still holds %+v, want task %d finished", p, number)
	}
	h.wantFinishedOnce(finishedByReport)
	wantAttr(h.t, h.records.of(finishedLine)[0], "task", strconv.FormatUint(number, 10))
	if value, held := h.values.value(h.token); held && value.Number == number {
		h.t.Fatalf("the bucket still holds %+v, want task %d's value deleted", value, number)
	}
}

// Row 1: a close disarms the second wait over a take awaiting its turn, and arms the reconnect
// deadline, whole from the close.
func TestTakeAwaitingItsTurnLosesItsSecondWaitAtAClose(t *testing.T) {
	h := boundsHarness(t)
	h.takeOf("implement the plan")
	if !h.armed(TimerTurnStart) {
		t.Fatal("the accepted take's second wait is not armed")
	}

	h.reconnectLater()

	h.wantUnarmed("after the close", TimerTurnStart)
	h.wantArmedFromNow(TimerRegistration, deadline, "after the close")
}

// Row 2: no bound of the second wait passes while no connection stands. The second case is the
// reviewer's probe (review handoff v4's N1, waits.go:192 at fe79c4d9): the worker's report of a
// run of the host's own given the take, G, reaches the daemon over HTTP while the shim is cut off,
// and puts the take back; the take's own turn, T, runs meanwhile, unseen. Its start is replayed
// behind the hello, and T's report finishes the task once. The negative control is the commit
// under this change, where the put-back arms the second wait and its bound charges the worker and
// takes the task back while T runs.
func TestChargeNothingForTheSecondWaitWhileNoConnectionStands(t *testing.T) {
	t.Run("a take awaiting its turn", func(t *testing.T) {
		h := boundsHarness(t, longReconnect)
		number := h.takeOf("implement the plan")
		h.reconnectLater()

		h.advance(turnStartBound)

		h.wantCharges()
		h.wantAwaitingItsTurn(number, "a whole second bound after the close")
	})
	t.Run("a take a report put back while no connection stands", func(t *testing.T) {
		h := boundsHarness(t, longReconnect)
		number := h.takeOf("implement the plan")
		h.hostsRun()
		h.reconnectLater()
		h.reportOf(1, 0)
		h.wantAwaitingItsTurn(number, "after G's report over HTTP")

		h.advance(turnStartBound)

		h.wantCharges()
		h.wantAwaitingItsTurn(number, "a whole second bound after the put-back")
		h.helloOf(true, true)
		h.must(StreamTurnStart{Claim: h.token})
		h.answerAsk(true)
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportOf(2, number)
		h.wantRunOnce(number)
		h.wantCharges()
	})
}

// Row 3: a close disarms the bound of the wait for the report of a take whose turn is over, with
// nothing held, and arms the reconnect deadline.
func TestTakeWhoseReportIsOwedLosesItsReportBoundAtAClose(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.must(StreamTurnStart{Claim: h.token})
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	h.wantTakesTurnStanding(number, "at the take's turn's end")
	if !h.armed(TimerReport) {
		t.Fatal("the bound of the wait for the take's report is not armed at its turn's end")
	}

	h.reconnectLater()

	h.wantUnarmed("after the close", TimerReport)
	h.wantArmedFromNow(TimerRegistration, deadline, "after the close")
}

// Row 4: no bound of the wait for a take's report passes while the daemon cannot see the worker's
// frames. These are the reviewer's two probes of the bound with nothing held (review handoff v4's
// N1, reportwait.go:256 at fe79c4d9): the daemon gave the take a run of the host's own, G, saw it
// start and end, and the take's own turn, T, runs while the shim is cut off. In the first the
// shim comes back with its backlog whole after a whole bound; in the second it comes back at once,
// its hello saying the backlog dropped frames (T's start among them), and the agent answers only
// after a whole bound. T's report finishes the task once in both. The negative control is the
// commit under this change, where the bound armed at G's end takes the task back as a cut turn
// while T runs, writes its value again, and T's report finishes nothing.
func TestTakeWhoseReportIsOwedIsNotTakenBackWhileNoConnectionStands(t *testing.T) {
	t.Run("the shim back with a whole backlog after a whole bound", func(t *testing.T) {
		h := boundsHarness(t, longReconnect)
		number := h.takeOf("implement the plan")
		h.hostsRun()
		h.reconnectLater()

		h.advance(turnStartBound)

		h.wantTakesTurnStanding(number, "a whole report bound after G's end, no connection standing,")
		h.helloOf(true, true)
		h.must(StreamTurnStart{Claim: h.token})
		h.answerAsk(true)
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportOf(2, number)
		h.wantRunOnce(number)
		h.wantCharges()
	})
	t.Run("a backlog cut, the agent answering after a whole bound", func(t *testing.T) {
		h := boundsHarness(t, longReconnect)
		number := h.takeOf("implement the plan")
		h.hostsRun()
		h.reconnectLater()
		h.helloOf(false, true)

		h.advance(turnStartBound)

		h.wantTakesTurnStanding(number, "a whole report bound after G's end, the hello unanswered,")
		h.answerAsk(true)
		h.wantState(StateWorking)
		h.advance(2 * turnStartBound)
		h.wantTakesTurnStanding(number, "two bounds into T")
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportOf(2, number)
		h.wantRunOnce(number)
		h.wantCharges()
	})
}

// Row 5: the take's turn, T, started while no connection stood, and its start is replayed behind
// a hello that says the backlog is whole. No bound is armed at the hello; the replayed start is
// the take's turn, no bound runs over it, and T's report finishes the task once.
func TestTakeWhoseTurnStartedUnseenIsGivenItByItsReplayedStart(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()

	h.helloOf(true, true)

	h.wantUnarmed("at the hello", TimerTurnStart, TimerReport, TimerTake)
	h.must(StreamTurnStart{Claim: h.token})
	h.wantTakesTurnStanding(number, "after T's replayed start")
	h.answerAsk(true)
	h.advance(2 * turnStartBound)
	h.wantCharges()
	h.wantTakesTurnStanding(number, "two bounds into T")
	h.must(StreamTurnEnd{Claim: h.token})
	h.wantRunOnce(number)
}

// Row 5a: T's start frame sat in the socket's send buffer when the connection broke, so the shim,
// which keeps only a frame whose write failed, replays none, and its hello says its backlog is
// whole. The agent's answer that it is in a turn is the take's turn starting: no bound runs during
// T, and T's report finishes the task once. The negative control is the commit under this change:
// the second wait stays armed through the hello, and its bound charges the worker and takes the
// task back during T.
func TestTakeWhoseStartFrameWasLostIsGivenItsTurnByTheAnswer(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.helloOf(true, true)
	h.wantUnarmed("at the hello", TimerTurnStart, TimerReport, TimerTake)

	h.answerAsk(true)

	h.wantState(StateWorking)
	h.wantTakesTurnStanding(number, "at the answer")
	h.advance(2 * turnStartBound)
	h.wantCharges()
	h.wantTakesTurnStanding(number, "two bounds into T")
	h.must(StreamTurnEnd{Claim: h.token})
	h.wantRunOnce(number)
}

// Row 5b: the take's turn ended while no connection stood, its end frame lost. The agent's answer
// that it is in no turn ends that turn, and the bound of the wait for the take's report is armed
// whole from the answer; the report that lands inside it finishes the task.
func TestTakeBoundsArmFromTheAnswerThatEndsATurnWhoseEndFrameWasLost(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.must(StreamTurnStart{Claim: h.token})
	h.reconnectLater()
	h.advance(time.Minute)
	h.helloOf(true, false)
	h.wantUnarmed("at the hello", TimerReport)
	h.advance(time.Minute)

	h.answerAsk(false)

	h.wantState(StateIdle)
	h.wantArmedFromNow(TimerReport, turnStartBound, "at the answer")
	h.advance(turnStartBound - time.Second)
	h.wantTakesTurnStanding(number, "a bound less a second after the answer")
	h.reportOf(1, number)
	h.wantRunOnce(number)
}

// Row 6: a take awaiting its turn when the connection closed, and an agent that answers the
// reconnect hello's ask that it is in a turn, whether or not the hello said its backlog is whole:
// the take's turn has started, no bound runs during it, and its report finishes the task once.
func TestTakeAwaitingItsTurnIsGivenItByAnAnswerInATurn(t *testing.T) {
	for _, whole := range []bool{true, false} {
		t.Run("backlog whole "+strconv.FormatBool(whole), func(t *testing.T) {
			h := boundsHarness(t)
			number := h.takeOf("implement the plan")
			h.reconnectLater()
			h.helloOf(whole, true)
			h.wantUnarmed("before the answer", TimerTurnStart)

			h.answerAsk(true)

			h.wantState(StateWorking)
			h.wantTakesTurnStanding(number, "at the answer")
			h.advance(2 * turnStartBound)
			h.wantCharges()
			h.must(StreamTurnEnd{Claim: h.token})
			h.wantRunOnce(number)
		})
	}
}

// Row 7: an agent that answers it is in no turn: R5's second wait is armed whole from the answer.
// A report of the take's turn, run whole while no connection stood, that lands inside that bound
// finishes the task; with no such report the second wait charges the worker at the bound counted
// from the answer, and not before. The negative control is the commit under this change, whose
// second wait, armed at the take, charges a bound after the take, during the outage's time.
func TestChargeTheSecondWaitFromTheAnswerOfNoTurnAfterAReconnect(t *testing.T) {
	answered := func(t *testing.T) (*harness, uint64) {
		t.Helper()
		h := boundsHarness(t)
		number := h.takeOf("implement the plan")
		h.reconnectLater()
		h.advance(time.Minute)
		h.helloOf(true, false)
		h.advance(5 * time.Minute)
		h.answerAsk(false)
		h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the answer")
		h.advance(turnStartBound - time.Second)
		h.wantCharges()
		return h, number
	}
	t.Run("a report of the turn run unseen inside the bound", func(t *testing.T) {
		h, number := answered(t)

		h.reportOf(1, number)

		h.wantRunOnce(number)
		h.advance(2 * turnStartBound)
		h.wantCharges()
	})
	t.Run("no report", func(t *testing.T) {
		h, number := answered(t)

		h.advance(time.Second)

		h.wantCharges(WaitTurnStart)
		if p := h.pending(); p.Number != number || p.taken() {
			t.Fatalf("the claim holds %+v, want task %d taken back at the second bound", p, number)
		}
	})
}

// Row 8: an agent that answers nothing, and sends no turn frame, by the reconnect deadline counted
// from the hello. The hello asks once, and the sweep before the deadline asks nothing again; at
// the deadline the process is relaunched, charged nothing, and the take is taken back as a cut
// turn, so the relaunched worker takes it again told its turn on it may have run.
func TestTakeIsTakenBackAsACutTurnWhenTheAgentAnswersNothingByTheReconnectDeadline(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.helloOf(true, false)
	if ask := h.conn.LastAsk(); ask != "ask-1" {
		t.Fatalf("the hello's connection was asked up to %q, want the one ask the hello makes", ask)
	}
	h.advance(deadline / 2)
	h.observe(runtime.Alive)
	if ask := h.conn.LastAsk(); ask != "ask-1" {
		t.Fatalf("the connection was asked up to %q after the sweep, want no ask but the hello's", ask)
	}
	h.wantCalls("Resume", 0)

	h.advance(deadline / 2)

	h.wantCalls("Resume", 1)
	h.wantState(StateLaunching)
	h.wantBudgets(Budgets{})
	h.wantCharges()
	h.wantTakenBackWithItsNote(number)
	// A late end frame of the process let go disarms nothing of the launch that replaces it.
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	if !h.armed(TimerRegistration) {
		t.Fatal("a late frame of the process let go disarmed the new launch's registration deadline")
	}
	h.wantTakenOnceMore(number, relaunchedAndTaken)
}

// Row 9: no answer, and a turn end frame on the new connection: the second wait is armed whole
// from that end, and the reconnect deadline the hello armed goes.
func TestTakeBoundsArmFromATurnEndBeforeTheAnswer(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.helloOf(true, false)
	h.advance(time.Minute)

	h.mustQuiet(StreamTurnEnd{Claim: h.token})

	h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the turn's end")
	h.wantAwaitingItsTurn(number, "at the turn's end")
	h.advance(deadline)
	h.wantCalls("Resume", 0)
}

// Row 9a: no answer, and a turn start frame on the new connection: it is the take's turn, no bound
// runs during it, the reconnect deadline the hello armed goes, and the turn's report finishes the
// task once. It passes on the commit under this change too, where a start always gave the take
// its turn and no deadline was armed at the hello.
func TestTakeAwaitingItsTurnIsGivenTheTurnStartBeforeTheAnswer(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.helloOf(true, true)

	h.must(StreamTurnStart{Claim: h.token})

	h.wantTakesTurnStanding(number, "at the start")
	h.advance(2 * turnStartBound)
	h.wantCharges()
	h.wantCalls("Resume", 0)
	h.must(StreamTurnEnd{Claim: h.token})
	h.wantRunOnce(number)
}

// Row 10: a live process whose shim never says hello again after its connection closed, while
// its claim holds a pending task: at the reconnect deadline counted from the close the process is
// probed, retired and relaunched with a charged launch failure, never_reconnected, and the take is
// taken back as a cut turn. The negative control is the commit under this change, where a close
// arms no deadline and the claim waits on a connection that never comes.
func TestRelaunchAProcessWhoseShimNeverReconnectsAfterAClose(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	alive := h.locator()
	h.reconnectLater()
	h.advance(deadline - time.Second)
	h.wantCalls("Resume", 0)

	h.advance(time.Second)

	if suspend := h.wantCalls("Suspend", 1)[0]; suspend.Locator != alive {
		t.Fatalf("suspended %+v, want the process whose shim never came back", suspend.Locator)
	}
	h.wantCalls("Resume", 1)
	h.wantState(StateLaunching)
	h.wantBudgets(Budgets{LaunchFailures: 1})
	if cause := h.claim().PendingCause; cause != CauseNeverReconnected {
		t.Fatalf("the relaunch is pending cause %q, want %q", cause, CauseNeverReconnected)
	}
	h.wantTakenBackWithItsNote(number)
	h.wantTakenOnceMore(number, relaunchedAndTaken)
}

// Row 11: a hello before the reconnect deadline the close armed disarms it, and the bounds are
// armed again as rows 5 to 9a say: here from the agent's answer of no turn.
func TestTakeHelloBeforeTheReconnectDeadlineDisarmsIt(t *testing.T) {
	h := boundsHarness(t)
	number := h.takeOf("implement the plan")
	h.reconnectLater()
	h.advance(deadline - 10*time.Second)

	h.helloOf(true, false)
	h.advance(20 * time.Second)

	h.wantCalls("Resume", 0)
	h.wantState(StateReady)
	h.answerAsk(false)
	h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the answer")
	h.wantAwaitingItsTurn(number, "at the answer")
}

// Row 12: a claim holding no pending task arms no deadline at its close, its hello asks nothing,
// and a hello that comes after the registration bound relaunches nothing, as before this change.
// It passes on the commit under this change too; it pins that the rule reaches a claim with a
// pending task only.
func TestRelaunchNothingForALateHelloOfAClaimHoldingNoTask(t *testing.T) {
	h := boundsHarness(t)
	h.reach(StateIdle)
	if p := h.claim().Pending; p != nil {
		t.Fatalf("the claim holds %+v, want no task", p)
	}
	h.reconnectLater()
	h.wantUnarmed("after the close", TimerRegistration)
	h.advance(deadline + time.Minute)

	h.helloOf(true, false)
	h.advance(2 * deadline)

	if ask := h.conn.LastAsk(); ask != "" {
		t.Fatalf("the hello's connection was asked %q, want nothing asked of a claim holding no task", ask)
	}
	h.wantCalls("Resume", 0)
	h.wantState(StateIdle)
	h.wantBudgets(Budgets{})
}

// Row 13: a value the worker reported seeing and has not taken, its first wait armed, when the
// connection closes: the first wait is disarmed, a whole first bound with no connection charges
// nothing, and the wait is armed whole again at the agent's answer to the reconnect hello's ask.
func TestChargeNothingForTheFirstWaitWhileNoConnectionStands(t *testing.T) {
	h := boundsHarness(t, longReconnect)
	h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
	h.seen()
	if !h.armed(TimerTake) {
		t.Fatal("the first wait over the seen value is not armed")
	}

	h.reconnectLater()

	h.wantUnarmed("after the close", TimerTake)
	h.advance(takeBound)
	h.wantCharges()
	h.helloOf(true, false)
	h.advance(takeBound)
	h.wantCharges()
	h.answerAsk(false)
	h.wantArmedFromNow(TimerTake, takeBound, "at the answer")
	h.advance(takeBound - time.Second)
	h.wantCharges()
	h.advance(time.Second)
	h.wantCharges(WaitTake)
}

// Row 14: after a daemon start, a worker's first act that reaches the daemon over HTTP before its
// shim's hello arms none of the three bounds; the hello and the agent's answer arm them. The
// negative control is the commit under this change, where any first act armed them.
func TestTakeOrReportBeforeTheHelloAfterADaemonStartArmsNoBound(t *testing.T) {
	t.Run("a take", func(t *testing.T) {
		h := boundsHarness(t)
		h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
		h.restart()

		h.mustTake()

		h.wantUnarmed("after the take before the hello", TimerTurnStart, TimerTake, TimerReport)
		h.connect()
		h.wantUnarmed("at the hello", TimerTurnStart)
		h.answerAsk(false)
		h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the answer")
	})
	t.Run("a seen report", func(t *testing.T) {
		h := boundsHarness(t)
		h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
		h.restart()

		h.seen()

		h.wantUnarmed("after the seen report before the hello", TimerTake)
		h.connect()
		h.wantUnarmed("at the hello", TimerTake)
		h.answerAsk(false)
		h.wantArmedFromNow(TimerTake, takeBound, "at the answer")
	})
	t.Run("a turn's report", func(t *testing.T) {
		h := boundsHarness(t)
		number := h.takeOf("implement the plan")
		h.restart()

		h.reportOf(1, 0)

		h.wantUnarmed("after the report before the hello", TimerTurnStart, TimerReport)
		h.connect()
		h.answerAsk(false)
		h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the answer")
		h.wantAwaitingItsTurn(number, "at the answer")
	})
}

// Row 15: a restored take whose first hello says the backlog is whole and whose agent answers on
// it binds its anchor, and the bound of the wait for its report with nothing held reaches it, as
// before this change: the store records the run the daemon before gave the take running at the
// restart, and that run's end, on the new connection, arms the bound whole. It passes on the
// commit under this change too; it pins that the ask a reconnect hello makes leaves such a take in
// the bound.
func TestTakeRestoredWithAWholeBacklogAndAnAnswerIsInTheNothingHeldBound(t *testing.T) {
	h, number := takeAcrossARestart(t, givenRunRunning, withWaits)
	h.connectInTurn()
	h.mustQuiet(StreamTurnEnd{Claim: h.token})
	h.reportOf(2, 0)

	h.wantTakeBackAtTheBound(number)
	h.wantRetakenAndRunOnce(number)
}

// Row 16: a restored take whose first hello does not say its backlog is whole is left out of the
// bound with nothing held (the stated case), and R5's second wait over a restored take awaiting
// its turn is armed as rows 6 to 9a say: not at the hello, and whole from the agent's answer of no
// turn. The first case passes on the commit under this change too, which left such a take out
// already; the second fails there, where the hello armed the second wait.
func TestTakeRestoredBehindACutBacklogIsLeftOutOfTheNothingHeldBound(t *testing.T) {
	t.Run("its given run over, nothing held", func(t *testing.T) {
		h, number := takeAcrossARestart(t, givenRunRunning, withWaits)
		h.conn.SetStreaming(true)
		h.cutHello()
		h.answerAsk(true)
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportOf(2, 0)
		putBacks := len(h.records.of(notTheTakesTurnLine))

		h.advance(2 * turnStartBound)

		h.wantCharges()
		h.wantStillItsTurn(number, putBacks, "two bounds after the given run's end")
		h.ownTurnFinishes(number, 3)
	})
	t.Run("awaiting its turn", func(t *testing.T) {
		h, number := takeAcrossARestart(t, nothingMore, withWaits)
		h.cutHello()
		h.wantUnarmed("at the hello", TimerTurnStart)

		h.answerAsk(false)

		h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the answer")
		h.wantAwaitingItsTurn(number, "at the answer")
	})
}

// owedFinishAwaitingItsTurn is a claim on the current plugin, ready, whose take of a task awaits
// its turn while the finish its report decided is owed (Machine.finishOwed): the report named the
// take's number ahead of the turn's start, the store refused the finish, and the take was then
// put back to await its turn, as a put-back leaves a take (putBack). No path of the daemon is
// shown to reach that state (review handoff v4's Q1), so the put-back is made in memory, and the
// next decision writes it as a put-back's own write would (settle). refusing is whether the store
// still refuses the finish when this returns.
func owedFinishAwaitingItsTurn(t *testing.T, refusing bool, set ...func(*Deps)) (*harness, uint64) {
	t.Helper()
	return owedFinishOfAwaitingItsTurn(t, RequestDeliver{Task: "the task whose report came ahead of its turn"}, refusing, set...)
}

// owedFinishOfAwaitingItsTurn is owedFinishAwaitingItsTurn over the task deliver queues, for a row
// that reads what the delivery carries: its run and its entry.
func owedFinishOfAwaitingItsTurn(t *testing.T, deliver RequestDeliver, refusing bool, set ...func(*Deps)) (*harness, uint64) {
	t.Helper()
	h := newHarnessOf(t, queuedClaim(), append([]func(*Deps){currentPlugin, withWaits}, set...)...)
	h.manualTakes()
	h.reach(StateReady)
	deliver.Claim = h.token
	h.must(deliver)
	h.mustTake()
	number := h.pending().Number
	h.store.fail("RetireDelivery", errBoom)
	if err := h.reportTurn(number, nil); err == nil {
		t.Fatal("the report whose finish the store refused answered no failure")
	}
	h.m.mu.Lock()
	p := h.m.claim.Pending
	p.TurnStarted, h.m.turns.take, h.m.reportedAhead, h.m.unsaved = false, 0, aheadReport{}, true
	owed, awaits := h.m.finishOwed, p.awaitsTurn()
	h.m.mu.Unlock()
	if !owed || !awaits {
		t.Fatalf("the claim holds %+v with its finish owed %t, want task %d's take awaiting its turn with its finish owed", h.pending(), owed, number)
	}
	if !refusing {
		h.store.fail("RetireDelivery", nil)
	}
	return h, number
}

// owedFinishOverATurnThatRan is a claim on the current plugin, ready, whose take of a task ran its
// turn, started and ended, and whose finish the report naming its number decided and the store
// refused (Machine.finishOwed): the state every live order reaches, which owedFinishAwaitingItsTurn
// makes by hand over a take put back. refusing is whether the store still refuses the finish when
// this returns.
func owedFinishOverATurnThatRan(t *testing.T, refusing bool) (*harness, uint64) {
	t.Helper()
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
	if !refusing {
		h.store.fail("RetireDelivery", nil)
	}
	return h, number
}

// wantOwedFinishMade holds that task number was finished once, by its report, and nothing of it
// taken back: the store records it finished, and no value of it is left for a process to take.
func (h *harness) wantOwedFinishMade(number uint64) {
	h.t.Helper()
	h.wantFinishedOnce(finishedByReport)
	for _, r := range h.recordsNaming(takenBackLine, number) {
		cause, _ := r.attr("cause")
		h.t.Fatalf("task %d was taken back (%s), want it finished by its owed report", number, cause)
	}
	if p := h.claim().Pending; p != nil && p.Number == number {
		h.t.Fatalf("the claim still holds %+v, want task %d finished", p, number)
	}
	if stored := h.store.load(h.token); stored.TaskFinished != number {
		h.t.Fatalf("the store records task %d finished, want %d", stored.TaskFinished, number)
	}
	if value, held := h.values.value(h.token); held && value.Number == number {
		h.t.Fatalf("the bucket still holds %+v, want task %d's value deleted", value, number)
	}
}

// Row 17: a take awaiting its turn while its finish is owed (Q1) is under neither R5's second wait
// nor the bound of the wait for its report: whole bounds of both charge nothing and take nothing
// back, and the end-frame bound makes the finish again, by the report, once the store answers.
// The negative control is the commit under this change, whose second wait read the take awaiting
// its turn and charged the worker and took the task back under its number.
func TestTakeAwaitingItsTurnWithAnOwedFinishIsUnderNoBound(t *testing.T) {
	h, number := owedFinishAwaitingItsTurn(t, true)
	h.observe(runtime.Alive)

	h.advance(2 * turnStartBound)

	h.wantCharges()
	h.wantAwaitingItsTurn(number, "two bounds into the owed finish")
	if lines := h.logs.lines(reportMissedLine); len(lines) != 0 {
		t.Fatalf("logged %q, want nothing taken back", lines)
	}
	h.store.fail("RetireDelivery", nil)
	h.advance(testRPC)
	h.wantOwedFinishMade(number)
}

// Row 18: a take awaiting its turn while its finish is owed, when the connection closes and no
// hello comes by the reconnect deadline: the task is finished once by its owed report, nothing of
// it is taken back, and the worker is given nothing of it again. The store refuses the finish at
// the deadline, so the process is kept and the deadline tried again at the probe interval; the
// end-frame bound makes the finish once the store answers, and the deadline's retry goes with the
// task (row 29c, dropReconnectWithTask), so the worker, holding no task now, is not retired as one
// that never reconnected. The negative control is the commit under this change, where a close
// armed no deadline.
func TestTakeAwaitingItsTurnWithAnOwedFinishIsFinishedAtTheReconnectDeadline(t *testing.T) {
	h, number := owedFinishAwaitingItsTurn(t, true)
	h.reconnectLater()

	h.advance(deadline)

	h.wantCalls("Resume", 0)
	if p := h.pending(); p.Number != number || !p.taken() {
		t.Fatalf("the claim holds %+v at the deadline, want task %d's take kept while its finish is refused", p, number)
	}
	h.store.fail("RetireDelivery", nil)
	h.advance(testProbe)
	h.wantOwedFinishMade(number)
	h.wantUnarmed("once the task is finished", TimerRegistration)
	h.wantCalls("Resume", 0)
	h.wantBudgets(Budgets{})
}

// Row 24: after a daemon start, a claim restored launching, shim-connected or registered whose
// agent says it is ready over HTTP before its shim's first hello to this daemon. Every claim a
// daemon start restores with a process has no connection the daemon reads until that hello
// (viewNoHello), so the take accepted then arms none of the three bounds; the hello and the
// agent's answer arm them. The negative control is the commit under this change, where such a
// claim started with its view whole, so the take armed R5's second wait over no connection.
func TestTakeOfAClaimRestoredBeforeItsReadyArmsNoBoundBeforeItsHello(t *testing.T) {
	for _, restored := range []ClaimState{StateLaunching, StateShimConnected, StateRegistered} {
		t.Run(string(restored), func(t *testing.T) {
			h := waitsHarness(t, currentPlugin)
			h.manualTakes()
			h.reach(restored)
			h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
			h.restart()
			if restored != StateRegistered {
				h.register()
			}
			h.ready()

			h.mustTake()

			h.wantUnarmed("after the take before the hello", TimerTurnStart, TimerTake, TimerReport)
			h.connect()
			h.wantUnarmed("at the hello", TimerTurnStart)
			h.answerAsk(false)
			h.wantArmedFromNow(TimerTurnStart, turnStartBound, "at the answer")
		})
	}
}

// Row 25: the connection of a registered claim holding a pending task closes before its agent
// says ready, and the agent says ready over HTTP with no hello since. The ready keeps a reconnect
// deadline at the later of the registration deadline standing and the close plus the reconnect
// bound (readyView): a hello inside that window, past the registration deadline's own time,
// retires nothing; with no hello by it, the no-hello path runs, the live process retired and
// relaunched with never_reconnected and the take it accepted taken back as a cut turn. The
// negative control is the commit under this change, where the ready disarmed the deadline, and a
// shim that never came back held the claim and its take for ever.
func TestTakeOfAClaimReadiedAfterItsConnectionClosedIsBoundByTheReconnectDeadline(t *testing.T) {
	// readiedAfterAClose is the claim registered, a task delivered, its connection closed late in
	// the registration deadline's bound, and its agent ready and its take accepted after the close:
	// the deadline the ready keeps is the close's, due closeToDeadline from then.
	const sinceRegistration, afterClose = deadline - time.Minute, 10 * time.Second
	closeToDeadline := deadline - afterClose
	readiedAfterAClose := func(t *testing.T) (*harness, uint64) {
		h := waitsHarness(t, currentPlugin)
		h.manualTakes()
		h.reach(StateRegistered)
		h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
		h.advance(sinceRegistration)
		h.reconnectLater()
		h.advance(afterClose)
		h.ready()
		h.mustTake()
		return h, h.pending().Number
	}
	t.Run("a hello inside the window", func(t *testing.T) {
		h, number := readiedAfterAClose(t)
		h.advance(2 * time.Minute)
		h.wantCalls("Suspend", 0)

		h.helloOf(true, false)
		h.answerAsk(false)

		h.advance(deadline)
		h.wantCalls("Suspend", 0)
		h.wantCalls("Resume", 0)
		h.wantState(StateReady)
		h.wantAwaitingItsTurn(number, "after the hello")
	})
	t.Run("no hello", func(t *testing.T) {
		h, number := readiedAfterAClose(t)
		alive := h.locator()
		h.advance(closeToDeadline - time.Second)
		h.wantCalls("Resume", 0)

		h.advance(time.Second)

		if suspend := h.wantCalls("Suspend", 1)[0]; suspend.Locator != alive {
			t.Fatalf("suspended %+v, want the process whose shim never came back", suspend.Locator)
		}
		h.wantCalls("Resume", 1)
		if cause := h.claim().PendingCause; cause != CauseNeverReconnected {
			t.Fatalf("the relaunch is pending cause %q, want %q", cause, CauseNeverReconnected)
		}
		h.wantTakenBackWithItsNote(number)
	})
}

// Row 27: after a daemon start, a claim restored registered whose agent says it is ready over
// HTTP and takes its task, and whose shim never says hello to this daemon. The ready keeps the
// reconnect deadline the start armed (viewNoHello, readyView), so the claim does not hold its take
// for ever: at the deadline the live process is retired and relaunched with never_reconnected,
// the take taken back as a cut turn, and the relaunched worker takes it again told its turn on it
// may have run. The negative control is the commit under this change, where the ready disarmed
// the deadline and nothing ended the wait.
func TestTakeOfARestoredClaimReadiedWithNoHelloIsEndedByTheReconnectDeadline(t *testing.T) {
	h := waitsHarness(t, currentPlugin)
	h.manualTakes()
	h.reach(StateRegistered)
	h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
	h.restart()
	h.ready()
	h.mustTake()
	number := h.pending().Number
	h.advance(deadline - time.Second)
	h.wantCalls("Resume", 0)

	h.advance(time.Second)

	h.wantCalls("Suspend", 1)
	h.wantCalls("Resume", 1)
	if cause := h.claim().PendingCause; cause != CauseNeverReconnected {
		t.Fatalf("the relaunch is pending cause %q, want %q", cause, CauseNeverReconnected)
	}
	h.wantTakenBackWithItsNote(number)
	h.wantTakenOnceMore(number, relaunchedAndTaken)
}

// retiredByTheOperator is the operator's stop of the claim, which retires it (retire).
func retiredByTheOperator(t *testing.T, h *harness) {
	t.Helper()
	h.must(RequestStop{Claim: h.token})
	h.wantState(StateRetired)
}

// retriedAfterRetirement is the operator's retry of the retired claim, the process it relaunches
// brought up to ready and its take accepted.
func retriedAfterRetirement(h *harness) {
	h.t.Helper()
	h.must(RequestOperatorRetry{Claim: h.token})
	relaunchedAndTaken(h)
}

// relaunchedAndTaken is the process a relaunch started brought up to ready, its take accepted.
func relaunchedAndTaken(h *harness) {
	h.t.Helper()
	h.relaunched()
	h.mustTake()
}

// compactedByTheOperator is the operator's compaction of the claim's session (compactStart),
// which takes back a take awaiting its turn and keeps the process, run to its end (compactEnded).
func compactedByTheOperator(t *testing.T, h *harness) {
	t.Helper()
	if err := h.m.Compact(h.ctx, testCompactInstructions); err != nil {
		t.Fatalf("the compaction answered %v", err)
	}
	h.m.Wait()
}

// Row 26: a take awaiting its turn while the claim's connection does not show every frame of its
// turns — no hello since a close or a daemon start, or no answer since the hello — taken back by a
// let-go that is no reconnect deadline: a retirement, a held suspension's deadline, a death, the
// one the close's own probe finds among them, or a relaunch of a process still running for a stale
// address; or by the operator's compaction, which keeps the process. Its turn may have run unseen,
// its start frame lost with the connection, so it goes back as a turn cut off, and the next
// process, or the same one after the compaction, takes it told its turn on it may have run, as row
// 8 takes it back. A stale plugin's relaunch waits for a whole view (row 29) and runs over a cut
// one only at the reconnect deadline, whose take-back
// TestAStaleRelaunchBehindABacklogCutTakesTheTakeBackAsACutTurn holds. The negative control is the
// commit under this change, where each let-go gave the take back unstarted with no note: only the
// reconnect deadline marked such a take (turnUnseen), the close probed its process before it cut
// the view, and a relaunch's take-back, made before its let-go records anything, read the mark
// alone (interrupted). The compaction's is the head before the change that made clearTake read
// the view (2da324b8), where its take-back (takeBack, clearTake) read neither the view nor the
// mark and gave the take back with no note.
func TestTakeAwaitingItsTurnIsTakenBackAsACutTurnWhileTheViewIsCut(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cut   func(h *harness)
		letGo func(t *testing.T, h *harness)
		next  func(h *harness)
	}{
		{name: "a retirement after a close", cut: (*harness).reconnectLater, letGo: retiredByTheOperator, next: retriedAfterRetirement},
		{name: "a retirement after a daemon start, before the hello", cut: (*harness).restart, letGo: retiredByTheOperator, next: retriedAfterRetirement},
		{name: "a retirement after a hello no answer followed", cut: func(h *harness) {
			h.reconnectLater()
			h.helloOf(true, false)
		}, letGo: retiredByTheOperator, next: retriedAfterRetirement},
		{name: "a death the close's own probe finds", cut: func(*harness) {}, letGo: func(t *testing.T, h *harness) {
			h.rt.ScriptProbe(fake.ProbeResult{Kind: runtime.Gone})
			h.reconnectLater()
			h.wantState(StateLaunching)
		}, next: relaunchedAndTaken},
		{name: "a death after a daemon start, before the hello", cut: (*harness).restart, letGo: func(t *testing.T, h *harness) {
			h.observe(runtime.Gone)
			h.wantState(StateLaunching)
		}, next: relaunchedAndTaken},
		{name: "a stale address's relaunch after a close", cut: (*harness).reconnectLater, letGo: func(t *testing.T, h *harness) {
			h.observe(runtime.StaleAddress)
			h.wantState(StateLaunching)
		}, next: relaunchedAndTaken},
		{name: "the operator's compaction after a hello no answer followed", cut: func(h *harness) {
			h.reconnectLater()
			h.helloOf(true, false)
		}, letGo: compactedByTheOperator, next: func(h *harness) { h.mustTake() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := boundsHarness(t)
			number := h.takeOf("implement the plan")
			tc.cut(h)

			tc.letGo(t, h)

			h.wantTakenBackWithItsNote(number)
			h.wantTakenOnceMore(number, tc.next)
		})
	}
	t.Run("a held suspension's deadline after a close", func(t *testing.T) {
		h := boundsHarness(t)
		number := h.takeOf("implement the plan")
		h.hostsRun()
		if err := h.handle(operatorSuspension(h)); !errors.Is(err, ErrSuspendHeld) {
			t.Fatalf("the suspension answered %v, want it held for the report of the take's turn", err)
		}
		h.reportOf(1, 0)
		h.wantAwaitingItsTurn(number, "after the given run's report")
		h.reconnectLater()

		h.advance(testStop)

		h.wantState(StateSuspended)
		h.wantTakenBackWithItsNote(number)
		h.wantTakenOnceMore(number, func(h *harness) {
			h.must(RequestResume{Claim: h.token})
			relaunchedAndTaken(h)
		})
	})
}

// Row 28: a take awaiting its turn, its turn's start frame lost in the socket's send buffer as the
// connection broke. The shim keeps only a frame whose write failed, so its hello says the backlog
// is whole, and the replay carries that turn's end alone, a turn this daemon never saw begin
// (blindTurnEnded); the agent then answers that it is in no turn, which makes the view whole. A
// let-go before that turn's report — a retirement or a death — takes the take back as a cut turn,
// and the next process takes it told its turn on it may have run: the blind end over a take
// awaiting its turn marked it as one whose turn may have run unseen (Machine.turnUnseen). The
// negative control is the commit under this change, where the blind end marked nothing, and the
// let-go over the whole view gave the take back unstarted with no note.
//
// A migrated worker's take awaiting its turn, on a plugin below TaskTakeContract (waitNone), is
// marked as a current one's is (markTurnUnseen: one test at every site that marks), so a death
// after the blind end takes it back as a cut turn too. Its negative control is the head before
// this change (2da324b8), where the blind end marked only a take in waitTake, and the death gave
// the migrated take back unstarted with no note.
func TestTakeAwaitingItsTurnBehindAnEndNeverSeenBegunIsTakenBackAsACutTurn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		letGo func(t *testing.T, h *harness)
		next  func(h *harness)
	}{
		{"a retirement", retiredByTheOperator, retriedAfterRetirement},
		{"a death", func(t *testing.T, h *harness) {
			h.observe(runtime.Gone)
			h.wantState(StateLaunching)
		}, relaunchedAndTaken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := boundsHarness(t)
			number := h.takeOf("implement the plan")
			h.reconnectLater()
			h.helloOf(true, false)
			h.mustQuiet(StreamTurnEnd{Claim: h.token})
			h.answerAsk(false)
			h.wantAwaitingItsTurn(number, "after the answer")

			tc.letGo(t, h)

			h.wantTakenBackWithItsNote(number)
			h.wantTakenOnceMore(number, tc.next)
		})
	}
	t.Run("a migrated worker's take, a death", func(t *testing.T) {
		h := restoreMigrated(t, false)
		number := h.pending().Number
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.answerAsk(false)
		h.wantAwaitingItsTurn(number, "after the answer")

		h.observe(runtime.Gone)
		h.wantState(StateLaunching)

		h.wantTakenBackWithItsNote(number)
		h.wantTakenOnceMore(number, (*harness).relaunchedTakes)
	})
}

// staleToTheDaemon makes the claim's recorded plugin contract an earlier one than this daemon's,
// which can relaunch onto its own: the stale-plugin lane's case (refreshPlugin).
func staleToTheDaemon(h *harness) {
	h.deps.PluginContract, h.m.deps.PluginContract, h.m.deps.PluginRelaunchable = TaskTakeContract+1, TaskTakeContract+1, true
}

// Row 29: a claim on a stale plugin holding a take, whose connection closed since the agent's last
// answer: a sweep's post relaunches nothing and logs that the relaunch waits for the view
// (refreshPlugin), and a post after a hello no answer followed does the same. The first post after
// the agent's answer on the new connection decides it; with neither answer nor turn frame, the
// reconnect deadline's own path runs at it, and takes the take back as a cut turn. The negative
// control is the commit under this change, where the sweep relaunched the process over the cut
// view, so a take whose own turn may have been running unseen was taken back and run again.
func TestStalePluginRelaunchOfAClaimHoldingATakeWaitsForAWholeView(t *testing.T) {
	postedOverACut := func(t *testing.T) (*harness, uint64) {
		t.Helper()
		h := boundsHarness(t)
		number := h.takeOf("implement the plan")
		h.reconnectLater()
		staleToTheDaemon(h)

		h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})

		h.wantCalls("Resume", 0)
		if lines := h.logs.lines(relaunchWaitsForViewLine, "trigger=sweep"); len(lines) != 1 {
			t.Fatalf("logged %q, want the relaunch's wait for the view once", lines)
		}
		h.wantAwaitingItsTurn(number, "after the sweep over the cut view")
		return h, number
	}
	t.Run("the agent's answer on the next connection, then a sweep", func(t *testing.T) {
		h, number := postedOverACut(t)
		h.helloOf(true, false)
		h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})
		h.wantCalls("Resume", 0)
		h.answerAsk(false)

		h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})

		h.wantCalls("Resume", 1)
		if p := h.pending(); p.Number != number || p.taken() || p.Interrupted {
			t.Fatalf("the relaunch over the whole view left %+v, want task %d given back unstarted", p, number)
		}
	})
	t.Run("no hello by the reconnect deadline", func(t *testing.T) {
		h, number := postedOverACut(t)

		h.advance(deadline)

		h.wantCalls("Resume", 1)
		if cause := h.claim().PendingCause; cause != CauseNeverReconnected {
			t.Fatalf("the relaunch is pending cause %q, want %q", cause, CauseNeverReconnected)
		}
		h.wantTakenBackWithItsNote(number)
	})
}

// Row 29a: a claim on a stale plugin holding no take, whose connection closed: a sweep's post
// relaunches it onto the current plugin, as before, since only a take the daemon accepted can have
// a turn running unseen. Two cases: idle, holding no pending task; and ready, holding a pending task
// no take holds, which waits for the process the relaunch starts. Both pass on the commit under this
// change too; they pin that the guard of the view reads a take, not a pending task.
func TestStalePluginRelaunchOfAClaimHoldingNoTakeRunsOverAClosedConnection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim func(t *testing.T) *harness
	}{
		{"idle, holding no pending task", func(t *testing.T) *harness {
			h := waitsHarness(t, currentPlugin)
			h.reach(StateIdle)
			if p := h.claim().Pending; p != nil {
				t.Fatalf("the idle claim holds %+v, want no pending task", p)
			}
			return h
		}},
		{"ready, holding a pending task no take holds", func(t *testing.T) *harness {
			h := boundsHarness(t)
			h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
			return h
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.claim(t)
			h.reconnectLater()
			staleToTheDaemon(h)

			h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})

			h.wantCalls("Resume", 1)
			if lines := h.logs.lines(relaunchWaitsForViewLine); len(lines) != 0 {
				t.Fatalf("logged %q, want no wait for the view over a claim holding no take", lines)
			}
			if p := h.claim().Pending; p != nil && (p.taken() || p.Interrupted) {
				t.Fatalf("the relaunch left %+v, want the untaken task waiting for the process it starts", p)
			}
		})
	}
}

// Row 29b: a claim whose connection closed while it held no pending task, so no reconnect deadline
// stands, is written a task: the task becoming pending over the cut view arms the deadline whole
// from then (armReconnectForTask, queue). The worker takes it over HTTP, and with no hello by the
// deadline the no-hello path runs: the live process is retired and relaunched with
// never_reconnected, and the take is taken back as a cut turn, which the relaunched worker takes
// told its turn may have run. The negative control is the commit under this change, where the task
// armed no deadline and nothing bounded the take while the process lived.
func TestTakeOfATaskWrittenOverAClosedConnectionIsBoundByTheReconnectDeadline(t *testing.T) {
	h := boundsHarness(t)
	h.reconnectLater()
	h.wantUnarmed("after a close over no task", TimerRegistration)

	h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})

	h.wantArmedFromNow(TimerRegistration, deadline, "once the task is pending")
	h.mustTake()
	number := h.pending().Number
	h.advance(deadline)
	h.wantCalls("Resume", 1)
	if cause := h.claim().PendingCause; cause != CauseNeverReconnected {
		t.Fatalf("the relaunch is pending cause %q, want %q", cause, CauseNeverReconnected)
	}
	h.wantTakenBackWithItsNote(number)
	h.wantTakenOnceMore(number, relaunchedAndTaken)
}

// Row 29c: a claim whose connection closed while it held a pending task, the reconnect deadline
// armed, whose worker takes the task and reports its turn over HTTP while the shim stays down, the
// store finishing it: the deadline goes with the task (dropReconnectWithTask), so its time passes
// with nothing retired and nothing charged. In a second case the claim is registered and not ready:
// the close keeps the registration deadline it runs under, and when its task is dropped that
// deadline stays and retires the process that never says ready. The negative control of the first
// case is the commit under this change, where the deadline stood with no task, and its no-hello
// path retired the worker and charged it a launch failure as never reconnected; the second passes
// there too, and pins that only a claim whose agent said it is ready loses the deadline.
func TestTakeFinishedWhileItsConnectionIsClosedTakesTheReconnectDeadlineWithIt(t *testing.T) {
	t.Run("ready, the task taken, reported and finished over HTTP", func(t *testing.T) {
		h := boundsHarness(t)
		h.must(RequestDeliver{Claim: h.token, Task: "implement the plan"})
		h.reconnectLater()
		h.mustTake()
		number := h.pending().Number
		if err := h.reportTurn(number, nil); err != nil {
			t.Fatalf("the report of the take's turn answered %v", err)
		}
		if p := h.claim().Pending; p != nil {
			t.Fatalf("the claim holds %+v after the report, want task %d finished", p, number)
		}
		h.wantUnarmed("once the task is finished", TimerRegistration)

		h.advance(deadline)

		h.wantCalls("Suspend", 0)
		h.wantCalls("Resume", 0)
		h.wantBudgets(Budgets{})
		h.wantCharges()
	})
	t.Run("registered, the task dropped", func(t *testing.T) {
		holds := true
		h := waitsHarness(t, currentPlugin, func(d *Deps) {
			d.PhaseHolds = func(context.Context, string, phase.Phase, uint64) (bool, error) { return holds, nil }
		})
		h.manualTakes()
		h.reach(StateRegistered)
		h.must(RequestDeliver{Claim: h.token, Task: "implement the plan", Phase: phase.Implementing, Generation: 5})
		h.reconnectLater()
		holds = false

		h.must(RequestDeliver{Claim: h.token, Task: "review the pull request"})

		if lines := h.logs.lines("the issue has left the phase this task was queued for"); len(lines) != 1 {
			t.Fatalf("logged %q, want the first task dropped once", lines)
		}
		if !h.armed(TimerRegistration) {
			t.Fatal("the drop disarmed the registration deadline of a claim not ready")
		}
		h.advance(deadline)
		h.wantCalls("Resume", 1)
	})
}

// restoredWorking is a daemon start over a claim restored working on the current plugin, its
// process's shim not yet said hello to this daemon: the claim whose turn an earlier daemon saw
// start, which the start asks its agent about at the first hello (askFirst). It holds the
// fixture's pending task, its take's turn running, as change leaves it, and every take of it is
// driven by the test.
func restoredWorking(t *testing.T, change func(*Claim)) *harness {
	t.Helper()
	h := newBareHarness(t)
	h.deps.PluginContract = TaskTakeContract
	h.manualTakes()
	restored := fixture(StateWorking)
	restored.PluginContract = TaskTakeContract
	if change != nil {
		change(&restored)
	}
	h.restore(restored)
	return h
}

// A daemon start's own ask of a claim restored working (askFirst) serves only the first hello since
// that start, since the stream reads an answer only on the connection that asked. A later hello over
// the pending task asks once on its own connection and arms the reconnect deadline whole from
// itself, as every reconnect hello does (askView); the start's ask makes no second ask on it
// (askTurnOver), and no sweep asks that connection again (observe, row 31): the deadline bounds
// the ask. The negative control of the hello's ask and deadline is the commit under the change
// that made them, where the later hello let the start's ask stand for it: it asked through
// askTurnOver and armed no deadline, so a shim that went quiet after that hello was bounded by
// nothing but the stall backstop. That of the sweep is the head before row 31 (2da324b8), where
// each sweep asked the connection again through askTurnOver.
func TestTakeOfARestoredWorkingClaimIsAskedOnceAtALaterHelloUnderItsOwnDeadline(t *testing.T) {
	h := restoredWorking(t, nil)
	h.conn.FailGetState(errBoom)
	h.connect()
	h.reconnectLater()

	h.helloOf(true, true)

	if ask := h.conn.LastAsk(); ask != "ask-1" {
		t.Fatalf("the later hello's connection was asked up to %q, want the one ask the hello makes", ask)
	}
	h.wantArmedFromNow(TimerRegistration, deadline, "at the later hello")
	h.observe(runtime.Alive)
	if ask := h.conn.LastAsk(); ask != "ask-1" {
		t.Fatalf("the later hello's connection was asked up to %q after a sweep, want no ask but the hello's", ask)
	}
}

// Row 30, at a later hello: a claim restored working holds a pending task, its first hello's ask
// went unanswered, its connection closed, and the next hello armed the reconnect deadline whole at
// itself; a sweep passes, and the agent answers that hello's ask. The answer makes the view whole
// in the one place every answer does (answerView), which disarms the deadline, so at the
// deadline's time nothing is retired, charged or taken back, and the task's turn runs on. The
// negative control is the head before this change (2da324b8): the sweep asked the connection
// again through askTurnOver, whose answer made the view whole and left the deadline armed; the
// hello's own answer then found the view whole and changed nothing, and at the deadline the path
// of a shim that never said hello (retireNeverReady) retired the live worker in its turn, charged
// it a launch failure and took its task back, so the task ran twice.
func TestTakeOfARestoredWorkingClaimAnsweredAtALaterHelloIsNotRetiredAtItsDeadline(t *testing.T) {
	h := restoredWorking(t, nil)
	h.conn.FailGetState(errBoom)
	h.connect()
	h.reconnectLater()
	h.helloOf(true, true)
	h.observe(runtime.Alive)
	h.answerAsk(true)

	h.advance(deadline)

	h.wantCalls("Suspend", 0)
	h.wantCalls("Resume", 0)
	h.wantBudgets(Budgets{})
	h.wantCharges()
	h.wantState(StateWorking)
	if p := h.pending(); p.Number != 1 || !p.turnStarted() || p.Interrupted {
		t.Fatalf("the claim holds %+v, want task 1's take with its turn running", p)
	}
}

// Row 32, with rows 30 and 31: a daemon start restores a claim working holding a pending task
// (askFirst), here a take awaiting its turn behind the turn the earlier daemon saw start. The first
// hello's ask is the start's own (askTurnOver), and that hello arms the reconnect deadline whole at
// itself, as every hello that asks over a pending task does (askView).
//
//   - Unanswered, the ask is not asked again at a sweep, and at the deadline the process is
//     relaunched, charged nothing, and the take taken back as a cut turn, so the relaunched worker
//     takes it again told its turn on it may have run. The negative control is the head before
//     this change (2da324b8), where the hello disarmed the start's deadline and armed none, so the
//     unanswered ask was bounded only by the sweep's repeat and the stall backstop.
//   - Answered at the hello, the answer disarms the deadline where it makes the view whole
//     (answerView), and at the deadline's time nothing is relaunched or charged. It passes at the
//     head before this change too, where no deadline was armed at the hello; with that deadline
//     armed and askTurnOver making the view whole without disarming it, the deadline would retire
//     the live worker as one whose shim never said hello.
func TestTakeOfARestoredWorkingClaimIsBoundedByTheReconnectDeadlineArmedAtItsFirstHello(t *testing.T) {
	awaiting := func(c *Claim) { c.Pending.TurnStarted, c.Pending.TakenText = false, c.Pending.Task }
	t.Run("an unanswered ask", func(t *testing.T) {
		h := restoredWorking(t, awaiting)
		number := h.pending().Number
		h.conn.FailGetState(errBoom)
		// The hello comes half a deadline after the daemon's start, so a deadline counted from the
		// start would come due half a deadline early.
		h.advance(deadline / 2)
		h.connect()
		h.wantArmedFromNow(TimerRegistration, deadline, "at the first hello")
		h.advance(deadline / 2)
		h.observe(runtime.Alive)
		if asks := h.logs.lines("supervise: the agent could not say whether it is in a turn"); len(asks) != 1 {
			t.Fatalf("the asks the agent left unanswered are %q, want the first hello's alone", asks)
		}
		h.advance(deadline/2 - time.Second)
		h.wantCalls("Resume", 0)

		h.advance(time.Second)

		h.wantCalls("Resume", 1)
		h.wantState(StateLaunching)
		h.wantBudgets(Budgets{})
		h.wantCharges()
		if cause := h.claim().PendingCause; cause != "" {
			t.Fatalf("the relaunch is pending cause %q, want none: it is charged nothing", cause)
		}
		h.wantTakenBackWithItsNote(number)
		h.wantTakenOnceMore(number, relaunchedAndTaken)
	})
	t.Run("an answered ask", func(t *testing.T) {
		h := restoredWorking(t, awaiting)
		number := h.pending().Number
		h.conn.SetStreaming(true)
		h.connect()
		h.wantUnarmed("after the answer to the first hello's ask", TimerRegistration)

		h.advance(deadline)

		h.wantCalls("Suspend", 0)
		h.wantCalls("Resume", 0)
		h.wantBudgets(Budgets{})
		h.wantState(StateWorking)
		h.wantAwaitingItsTurn(number, "at the deadline's time")
	})
}
