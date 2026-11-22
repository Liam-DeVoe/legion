package supervise

// What would let a live process go while its held stop waits for the copy its turn's end started
// (R1 of dispatch://INFRA-1117): a stale-plugin relaunch, a stale address, the turn of a wake the
// pacing parked over that process, the agent's ready said again and the stall backstop over a turn
// the agent started since. Each of these five would run or end the held stop while the copy still
// asks the process's shim, so each waits for that copy as the stop does. A registration on another
// plugin contract cannot wait: its plugin exits on the refusal. It is neither relaunched nor
// charged, and its process's exit meets the held stop.
//
// Each runs in both windows a stop is held over the copy in (copyWindow): a daemon's stop that
// waits for the copy, and a project pause's stop that still waits for the prompt's result and has
// not begun its wait for the copy. Each guard asks whether the stop is held for the copy
// (turnCopyHolds); narrowed to whether the stop's wait for the copy has begun (turnCopy.stopWaits),
// it lets the event through in the pause's window, and that window's row fails where the row's own
// control does. The parked wake and the stall backstop run past the copy's due as well, where the
// hold has ended with no end of the copy reported and each does what it does where no copy is
// taken; with the due taken out of the guard, the pause's window's row fails there.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// copyWaiting is the copying harness's claim idle on its process, holding a daemon's stop that
// waits for the copy its turn's end started. It answers the copies and the process the copy asks.
func copyWaiting(t *testing.T) (*harness, *turnEndCopies, runtime.Locator) {
	t.Helper()
	h, copies := copyingHarness(t)
	h.reach(StateWorking)
	holdStop(t, h, "LEGION-208 left implementing")
	h.must(StreamTurnEnd{Claim: h.token})
	h.wantState(StateIdle)
	return h, copies, h.locator()
}

// copyWindow is one window in which a stop is held over a claim idle on its process for the copy
// its last turn's end started (turnCopyHolds).
type copyWindow struct {
	name string
	// by is the source of the held stop, which the claim is suspended by once the stop runs.
	by SuspendSource
	// open brings a claim whose daemon starts a copy at each turn's end into the window, the turn's
	// end having started one copy. begin is what begins the stop's wait for that copy where the
	// window has not begun it, so that the copy's end runs the stop.
	open func(t *testing.T) (h *harness, copies *turnEndCopies, begin func())
}

// copyWindows are the two windows: a daemon's stop held for the turn's end, which waits for the
// copy from that end (copyWaiting), and a project pause's stop held for the turn's end, which
// waits for the prompt's result first and begins its wait for the copy only at that result
// (pauseHeldForTheResult), which its begin hands the machine.
func copyWindows() []copyWindow {
	return []copyWindow{
		{name: "ADaemonsStopWaitingForTheCopy", by: SuspendedByDaemon, open: func(t *testing.T) (*harness, *turnEndCopies, func()) {
			h, copies, _ := copyWaiting(t)
			return h, copies, func() {}
		}},
		{name: "APausesStopWaitingForTheResult", by: SuspendedByReleased, open: func(t *testing.T) (*harness, *turnEndCopies, func()) {
			copies := &turnEndCopies{}
			h, task := pauseHeldForTheResult(t, copies, nil)
			return h, copies, func() {
				t.Helper()
				h.must(StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: true})
			}
		}},
	}
}

// copyEnds are the two ways the hold of a stop held for a turn's copy ends (turnCopyHolds): the
// copy's reported end, and its due passing with no end reported, named for a subtest.
var copyEnds = []struct {
	name    string
	pastDue bool
}{{name: "AtTheCopysEnd"}, {name: "PastTheCopysDue", pastDue: true}}

// wakeTurnsAtTheDue bounds the timers the parked wake's row fires on its clock's move to the
// copy's due: that move fires one or two at the due, and a wake parked again at the due for no
// time at all comes due again at once, which an unbounded move would never finish.
const wakeTurnsAtTheDue = 20

// turnCopyDue is the due of the copy of its turn's end the claim's machine is still waiting to hear
// end (turnCopy.due), read under the machine's lock. It fails where the machine waits for none.
func turnCopyDue(t *testing.T, h *harness) time.Time {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if h.m.turnCopy == nil {
		t.Fatal("the machine waits for no copy of its turn's end, want one still being taken")
	}
	return h.m.turnCopy.due
}

// speaksAnOlderContract has h's claim speak the contract of the daemon before this one, which can
// relaunch onto its plugin.
func speaksAnOlderContract(h *harness) {
	h.deps.PluginContract, h.m.deps.PluginContract = refreshContract, refreshContract
	h.m.deps.PluginRelaunchable = true
}

// waitsOnItsProcess fails unless the claim is still idle on loc, holding its stop, with no process
// stopped: what came in was left to the copy's end.
func waitsOnItsProcess(t *testing.T, h *harness, loc runtime.Locator) {
	t.Helper()
	h.wantState(StateIdle)
	h.wantCalls("Suspend", 0)
	if c := h.claim(); !c.SuspensionHeld || c.Locator == nil || *c.Locator != loc {
		t.Fatalf("the claim is %+v, want it idle on its process %+v, holding the stop that waits for the copy", c, loc)
	}
}

// Three events reach a claim idle on its process while a stop is held over its turn's copy, and
// each would replace that process or run the stop. Each is left alone while the copy is taken, and
// the copy's end runs the stop, which takes the process. Each row's control is named with it.
func TestAnEventThatWouldLetTheProcessGoWaitsForTheTurnsCopyItsHeldStopWaitsFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		// stale is whether the claim speaks the contract of the daemon before this one.
		stale bool
		event func(h *harness)
	}{
		// The stale-plugin sweep posts every claim behind the daemon's contract, which at a
		// release that moves the contract is every claim. A claim whose held stop waits for its
		// turn's copy is idle, so the relaunch was not held back for a turn, and it let the
		// process go through the held stop while the copy still asked its shim: the copy failed
		// and the turn's end was in no copy once the role was stopped. While the stop waits the
		// relaunch is not made, and nothing holds it for later.
		//
		// The control is the relaunch held back for a turn alone: the claim is then suspended at
		// the sweep's post, while the copy is still being taken, and the row fails there.
		{name: "AStalePluginRelaunchWaitsForTheTurnsCopyItsHeldStopWaitsFor", stale: true, event: func(h *harness) {
			h.must(StalePlugin{Claim: h.token, Trigger: "sweep", Admitted: true})
		}},
		// A process found at a stale address is replaced, and that replacement lets the process
		// go through a held stop. While the stop waits for its turn's copy the observation is left
		// alone: the runtime observes the process again at every tick.
		//
		// The control is the observation replacing the process as it comes: the claim is then
		// suspended while the copy is still being taken, and the row fails there.
		{name: "AStaleAddressWaitsForTheTurnsCopyItsHeldStopWaitsFor", stale: true, event: func(h *harness) {
			h.observe(runtime.StaleAddress)
		}},
		// An agent that says ready again finds a held stop and runs it (reready), which a held
		// stop that waits for its turn's copy must not do: the stop would take the process the
		// copy still asks. The ready said again changes nothing then.
		//
		// The control is the ready running any held stop as it comes: the claim is then suspended
		// at the ready, while the copy is still being taken, and the row fails there.
		{name: "AReadySaidAgainWaitsForTheTurnsCopyItsHeldStopWaitsFor", event: func(h *harness) {
			h.must(RequestReady{Claim: h.token, Generation: h.generation(), Session: session})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, window := range copyWindows() {
				t.Run(window.name, func(t *testing.T) {
					h, copies, begin := window.open(t)
					if tc.stale {
						speaksAnOlderContract(h)
					}
					loc := h.locator()

					tc.event(h)

					waitsOnItsProcess(t, h, loc)
					begin()
					waitsOnItsProcess(t, h, loc)
					stopsOnceAfterTheCopy(t, h, copies, 0, window.by)
				})
			}
		})
	}
}

// parkStaleRelaunch raises a stale-plugin relaunch over h's claim, idle or ready on its process,
// which the pacing parks over that process.
func parkStaleRelaunch(t *testing.T, h *harness) {
	t.Helper()
	speaksAnOlderContract(h)
	h.must(StalePlugin{Claim: h.token, Trigger: "sweep", Admitted: true})
	h.wantWaitingWake(WakePluginRefresh)
}

// A wake the pacing parked over a live process — a stale-plugin relaunch raised while the claim
// was idle or ready — can come due after the agent's turn has ended into a stop held over that
// turn's copy. Its turn ends the hold (endHeld), which would let the process go while the copy
// still asks its shim, so the wake waits for that copy too and stays on the machine, parked again
// for the copy's due. Where the copy's end comes first, it runs the stop, whose let-go takes the
// wake with the process and launches nothing. Where the due passes with no end reported, the hold
// is over (turnCopyHolds), and the wake's turn at the due ends the hold as a parked wake does where
// no copy is taken: the claim is suspended by the held stop's source, and nothing launches. In a
// pause's window that is before the prompt's answer has come.
//
// The control of the rows at the copy's end is the wake's turn ending the hold as it comes: the
// claim is then suspended at the wake's turn, while the copy is still being taken, and those rows
// fail there. The control of the pause's window's row past the due is wakeNow's guard without the
// due: the wake is parked again at the due for no time at all and its turn comes again at once,
// so the clock's move to the due never ends; the move is bounded (advanceAtMost), and the row
// fails there, naming the wake parked again at the due.
func TestAParkedWakeWaitsForTheTurnsCopyItsHeldStopWaitsFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		// by is the source of the held stop, as copyWindow's.
		by SuspendSource
		// park brings a paced claim whose daemon copies each turn's end into its window with a
		// stale-plugin relaunch parked over its process, and returns the index of the copy the stop
		// is held over and begin, as copyWindow's.
		park func(t *testing.T, copies *turnEndCopies) (h *harness, index int, begin func())
	}{
		{name: "ADaemonsStopWaitingForTheCopy", by: SuspendedByDaemon, park: func(t *testing.T, copies *turnEndCopies) (*harness, int, func()) {
			h := pacedHarness(t, wakeRate, func(d *Deps) { d.CopyTurnEnd = copies.start })
			h.reach(StateIdle)
			h.must(copies.copied(t, 0))
			parkStaleRelaunch(t, h)
			h.must(StreamTurnStart{Claim: h.token})
			holdStop(t, h, "LEGION-208 left implementing")
			h.mustQuiet(StreamTurnEnd{Claim: h.token})
			h.wantState(StateIdle)
			return h, 1, func() {}
		}},
		// The relaunch is raised over the ready claim whose prompt is acknowledged, before the
		// turn that prompt starts: once that turn has ended, nothing sends a prompt the pause's
		// stop could wait for while the wake waits over the process (launchWaits).
		{name: "APausesStopWaitingForTheResult", by: SuspendedByReleased, park: func(t *testing.T, copies *turnEndCopies) (*harness, int, func()) {
			h, task := pauseHeldForTheResult(t, copies, pacedDeps(wakeRate), func(h *harness) StreamTurnStart {
				parkStaleRelaunch(t, h)
				return StreamTurnStart{Claim: h.token}
			})
			return h, 0, func() {
				t.Helper()
				h.must(StreamPromptResult{Claim: h.token, DeliveryID: task, AgentInvoked: true})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, end := range copyEnds {
				t.Run(end.name, func(t *testing.T) {
					copies := &turnEndCopies{}
					h, index, begin := tc.park(t, copies)
					waiting := h.wantWaitingWake(WakePluginRefresh)
					loc := h.locator()
					starts := len(h.calls("Resume")) + len(h.calls("Spawn"))

					h.advance(waiting.At.Sub(h.clock.Now()))

					waitsOnItsProcess(t, h, loc)
					if h.m.View().WakeWaiting == nil {
						t.Fatal("the wake is off the machine, want it waiting for the copy with the stop")
					}
					if end.pastDue {
						due := turnCopyDue(t, h)
						stopsOnceAt(t, h, tc.by, "the copy's due passed", func() {
							if !h.advanceAtMost(due.Sub(h.clock.Now()), wakeTurnsAtTheDue) {
								t.Fatalf("the wake came due %d times at the copy's due and was parked again each time, want its turn there to end the hold", wakeTurnsAtTheDue)
							}
						})
					} else {
						begin()
						waitsOnItsProcess(t, h, loc)
						stopsOnceAfterTheCopy(t, h, copies, index, tc.by)
					}
					if got := len(h.calls("Resume")) + len(h.calls("Spawn")); got != starts {
						t.Fatalf("the runtime was asked %d launches after the turn's end, want none: the stop takes the wake with the process", got-starts)
					}
					if h.m.View().WakeWaiting != nil {
						t.Fatalf("the claim still holds the wake %+v, want it taken by the stop's let-go", h.m.View().WakeWaiting)
					}
				})
			}
		})
	}
}

// The agent can start a turn of its own while a stop is held over the copy of the turn before it,
// and the copy's end then leaves the stop to this turn's end (endTurnCopyWait). The stall backstop
// over this turn replaces the process, which ends the hold (endHeld) while the copy still asks the
// process's shim. So the backstop leaves the turn alone while the stop is held for the copy
// (turnCopyHolds), and writes one line naming the copy's due as until, in either window. The
// sweep's next post once the copy's end is reported, or once its due has passed with no end
// reported, ends the stalled turn as a backstop does where no copy is taken: the claim is
// suspended by the held stop's source. The bound the backstop carries is the daemon's to choose; a
// short one here reaches the wait.
//
// The control of the rows at the copy's end is the backstop acting whatever the stop waits for:
// the claim is then suspended at the first post, while the copy is still being taken, and those
// rows fail there. The control of the pause's window's row past the due is the backstop's guard
// without the due: the post after the due leaves the turn alone again, and the row fails there.
func TestTheStallBackstopWaitsForTheTurnsCopyItsHeldStopWaitsFor(t *testing.T) {
	for _, window := range copyWindows() {
		t.Run(window.name, func(t *testing.T) {
			for _, end := range copyEnds {
				t.Run(end.name, func(t *testing.T) {
					h, copies, _ := window.open(t)
					loc := h.locator()
					due := turnCopyDue(t, h)
					h.must(StreamTurnStart{Claim: h.token})
					h.stepped("bash", "toolu_own_turn_0", toolArgs, false)
					last := h.clock.Now()
					h.advance(2 * time.Minute)
					backstop := StallBackstop{Claim: h.token, Bound: time.Minute, Deadline: last.Add(time.Minute), Step: h.store.step(h.token)}

					h.must(backstop)

					h.wantState(StateWorking)
					h.wantCalls("Suspend", 0)
					if c := h.claim(); !c.SuspensionHeld || c.Locator == nil || *c.Locator != loc {
						t.Fatalf("the claim is %+v, want it working on its process %+v, holding the stop held over the copy", c, loc)
					}
					deferred := h.records.of(backstopLeftToTheTurnCopy)
					if len(deferred) != 1 {
						t.Fatalf("the backstop wrote %d lines leaving the turn to the copy, want one", len(deferred))
					}
					if until, ok := deferred[0].attr("until"); !ok || until.Kind() != slog.KindTime || !until.Time().Equal(due) {
						t.Fatalf("the backstop's line names until %v (written %t), want the copy's due %s", until, ok, due)
					}
					if end.pastDue {
						h.advance(due.Sub(h.clock.Now()))
					} else {
						h.must(copies.copied(t, 0))
					}
					h.wantState(StateWorking)

					stopsOnceAt(t, h, window.by, "the backstop posted again", func() { h.must(backstop) })
				})
			}
		})
	}
}

// A registration on another plugin contract over a claim whose relaunch for this contract was
// already made charges a launch failure and relaunches (staleRegistration, relaunchAfterFailure),
// and the relaunch would drop a held stop with the process. While a stop is held for the copy of
// the last turn's end (turnCopyHolds), the registration is refused as stale and nothing is
// relaunched or charged. That keeps no process for the copy: the plugin stops its agent on the
// refusal, so the process's death follows, and it meets the held stop, which suspends the claim
// charged nothing, as the stop's own end would. The refusal is the 409 of any stale registration,
// in words that promise no relaunch and say the process's exit meets the stop
// (claim.StalePluginContractUnderStop), which the plugin logs as it stops.
//
// The control is the relaunch made whatever the stop is held over: the registration then charges a
// launch failure and relaunches the claim, and the test fails there.
func TestAStaleRegistrationUnderAHeldStopOverTheTurnsCopyIsNeitherRelaunchedNorCharged(t *testing.T) {
	for _, window := range copyWindows() {
		t.Run(window.name, func(t *testing.T) {
			h, _, _ := window.open(t)
			speaksAnOlderContract(h)
			loc := h.locator()
			stale := h.claim().PluginContract
			h.m.mu.Lock()
			h.m.claim.PluginRelaunchFor = refreshContract
			h.m.mu.Unlock()
			charged := h.claim().Budgets.LaunchFailures

			err := h.handle(RequestRegister{Claim: h.token, Generation: h.generation(), Session: session, SessionFile: sessionFile,
				CapabilityHash: []byte("capability"), PluginContract: stale})

			want := fmt.Sprintf("pi-legion speaks daemon API contract %d; this daemon requires %d: the daemon relaunches nothing: "+
				"this process's exit on this refusal meets the stop held over this claim, which suspends it, and the claim's next launch is onto the current plugin",
				stale, refreshContract)
			var refusal claim.Refusal
			if !errors.As(err, &refusal) || refusal.Status != http.StatusConflict || refusal.Message != want {
				t.Fatalf("the registration on another plugin contract was answered %v, want a 409 saying %q", err, want)
			}
			waitsOnItsProcess(t, h, loc)
			if got := h.claim().Budgets.LaunchFailures; got != charged {
				t.Fatalf("the refused registration charged %d launch failures, want none", got-charged)
			}

			h.observe(runtime.Gone)

			if c := h.claim(); c.State != StateSuspended || c.SuspendedBy != window.by || c.Budgets.LaunchFailures != charged {
				t.Fatalf("the claim after its process exited on the refusal is %s by %q with %d launch failures, want suspended by the held stop's source %q and %d",
					c.State, c.SuspendedBy, c.Budgets.LaunchFailures, window.by, charged)
			}
		})
	}
}
