package supervise

// A relaunch of a claim holding a take runs that take's task once (dispatch://INFRA-2811). The
// take-back of a task a process holds is made where the process is let go and not before — at a
// launch that runs now, before it lets go; at a parked launch, at the wake's turn (replaceNow,
// runWake) — and a take whose turn is over is settled by its claim's contract: finished by its
// end frame where the end frame finishes a task, taken back with the note a cut turn gets where
// only a report naming the number does (interrupted, settle).

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// migratedTake is a claim restored in state on a plugin below TaskTakeContract — the harness's
// own contract — holding the take the migration's fill makes of an acknowledged, unconfirmed
// prompt of the release before: confirmed, its text the prompt's, its turn not started.
func migratedTake(state ClaimState) Claim {
	c := fixture(state)
	c.PluginContract = 24
	c.Pending.ConfirmedAt, c.Pending.TakenText, c.Pending.TurnStarted, c.TaskTaken = epoch, c.Pending.Task, false, 1
	return c
}

// restoreMigrated is a harness over the migrated take, its process's shim reconnected, paced at
// the shipped rate with the burst already spent when paced, so the next launch parks. The old
// process's plugin takes nothing (it is the release before the take's), so the harness's worker
// asks nothing until relaunchedTakes hands the claim to the process a relaunch starts.
func restoreMigrated(t *testing.T, paced bool) *harness {
	t.Helper()
	h := newBareHarness(t)
	if paced {
		pacedDeps(wakeRate)(&h.deps)
	}
	h.manualTakes()
	h.restore(migratedTake(StateReady))
	h.connect()
	if paced {
		h.deps.WakePace.Reserve(h.clock.Now())
	}
	return h
}

// relaunchedTakes walks the process a relaunch started to ready, its plugin taking as the
// current one does.
func (h *harness) relaunchedTakes() {
	h.t.Helper()
	h.worker.manual = false
	h.relaunched()
}

// The migrated take's task runs once whichever way its claim is relaunched.
//
//   - A parked relaunch with the old turn in the wait: the take stands over the wait, so the
//     turn the old process starts there is the take's; the wake's turn cuts it off and takes the
//     task back with its note, and the process the launch starts takes it again behind that note.
//     Negative control: the take-back made at the park (replaceNow's interrupted ahead of the
//     launch) — the old turn is then no take's, and the new process is handed the task with no
//     word of the turn that ran it.
//   - A cut turn: the take's turn runs when a parked relaunch is decided and ends in the wait;
//     the end frame finishes it, and the new process is handed nothing. Negative control: the
//     same take-back at the park — the task is then taken back from a turn that runs whole, and
//     the new process runs it a second time.
//   - A relaunch held for the old turn's end: the stale-plugin lane waits out the take's turn,
//     the end frame finishes the task, and the relaunch that follows hands the new process
//     nothing. Negative control: the end frame finishing no task (endFrameFinishes false) — the
//     task then stands unfinished and the relaunch never runs.
//   - A sweep's relaunch inside the end-frame bound: the take's turn ended and its bound runs when
//     the relaunch lets the process go; the task is finished, not taken back. Negative control:
//     a take whose turn is over taken back whatever the contract (interrupted's gate) — the new
//     process runs it again.
//   - A relaunch before any turn: the take is given back before the process is let go, and the
//     new process's take is decided afresh. Negative control: no take-back at a launch that runs
//     now (launchReplacing's takeBack) — the new process is answered the old take from the record.
func TestRelaunchOfAMigratedTakeRunsItOnce(t *testing.T) {
	t.Run("a parked relaunch with the old turn in the wait", func(t *testing.T) {
		h := restoreMigrated(t, true)
		h.observe(runtime.StaleAddress)
		waiting := h.wantWaitingWake(WakeLaunch)
		if p := h.pending(); !p.awaitsTurn() {
			t.Fatalf("the task after the park is %+v, want the migrated take standing", p)
		}
		h.must(StreamTurnStart{Claim: h.token})
		if p := h.pending(); !p.turnStarted() {
			t.Fatalf("the turn in the wait left %+v, want it the take's turn", p)
		}

		h.advance(waiting.At.Sub(h.clock.Now()))

		if p := h.pending(); p.taken() || !p.Interrupted {
			t.Fatalf("the task after the wake's turn is %+v, want it taken back from the turn the wake cut off", p)
		}
		h.relaunchedTakes()
		if again := h.wantPrompts(1)[0]; again.Message == "the task" {
			t.Fatalf("the new process took %+v, want the task behind the word of its cut turn", again)
		}
	})
	t.Run("a cut turn", func(t *testing.T) {
		h := restoreMigrated(t, true)
		h.must(StreamTurnStart{Claim: h.token})
		h.observe(runtime.StaleAddress)
		waiting := h.wantWaitingWake(WakeLaunch)
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportActivity()

		h.advance(waiting.At.Sub(h.clock.Now()))
		h.relaunchedTakes()

		if p := h.claim().Pending; p != nil {
			t.Fatalf("pending %+v after the relaunch, want the task its turn ran finished", p)
		}
		h.wantPrompts(0)
	})
	t.Run("a relaunch held for the old turn's end", func(t *testing.T) {
		h := restoreMigrated(t, false)
		h.must(StreamTurnStart{Claim: h.token})
		h.deps.PluginContract, h.m.deps.PluginContract, h.m.deps.PluginRelaunchable = 25, 25, true
		h.must(StalePlugin{Claim: h.token, Trigger: "sweep"})
		h.wantCalls("Resume", 0)
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportActivity()

		h.advance(testRPC)
		h.relaunchedTakes()

		if p := h.claim().Pending; p != nil {
			t.Fatalf("pending %+v after the relaunch, want the task its turn ran finished", p)
		}
		h.wantCalls("Resume", 1)
		h.wantPrompts(0)
	})
	t.Run("a sweep's relaunch inside the end-frame bound", func(t *testing.T) {
		h := restoreMigrated(t, false)
		h.must(StreamTurnStart{Claim: h.token})
		h.mustQuiet(StreamTurnEnd{Claim: h.token})
		h.reportActivity()

		h.observe(runtime.StaleAddress)
		h.relaunchedTakes()

		if p := h.claim().Pending; p != nil {
			t.Fatalf("pending %+v after the relaunch, want the task its turn ran finished", p)
		}
		h.wantPrompts(0)
	})
	t.Run("a relaunch before any turn", func(t *testing.T) {
		h := restoreMigrated(t, false)
		h.reportActivity()

		h.observe(runtime.StaleAddress)
		if p := h.pending(); p.taken() || p.Interrupted {
			t.Fatalf("the task after the relaunch is %+v, want the take given back with no note: no turn of it ran", p)
		}
		h.relaunchedTakes()

		if again := h.wantPrompts(1)[0]; again.Message != "the task" {
			t.Fatalf("the new process took %+v, want the task alone", again)
		}
		answered := h.records.of(answeredLine)
		wantAttr(t, answered[len(answered)-1], "repeat", "false")
	})
}

// A take whose turn is over, on a plugin that names its task's number, is taken back with the
// note a cut turn gets when a relaunch lets its process go: only a report naming the number
// finishes such a task, and the end of a run says nothing of whose run it was. The process the
// relaunch starts takes it again behind that note.
//
// The negative control is the end frame finishing it whatever the contract (interrupted's gate
// before this change): the task is finished, and the new process is handed nothing.
func TestRelaunchTakesBackACurrentPluginsTakeWhoseTurnIsOverWithItsNote(t *testing.T) {
	h := newHarnessOf(t, queuedClaim(), currentPlugin)
	h.reach(StateReady)
	h.takesTurnEndsWithItsReportToCome()
	number := h.pending().Number
	h.reportActivity()

	h.observe(runtime.StaleAddress)

	if p := h.pending(); p.taken() || !p.Interrupted || p.Number != number {
		t.Fatalf("the task after the relaunch is %+v, want task %d taken back with the note of a cut turn", p, number)
	}
	h.relaunched()
	if again := h.wantPrompts(2)[1]; again.Number != number || again.Message == "review the pull request" {
		t.Fatalf("the new process took %+v, want task %d behind the word of its cut turn", again, number)
	}
}
