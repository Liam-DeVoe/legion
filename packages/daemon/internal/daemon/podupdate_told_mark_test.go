package daemon

// The mark the sweep makes on each claim of a round it has told the controller of, past the
// round's bound (markUpdateTold, markOwedTold): the sweep never waits on a claim's machine to make
// it, and a round is told once all the same. The limit rig is podupdate_limits_test.go's, with no
// limit set.

import (
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// holdInARelaunch holds the one worker of issue inside a Resume, as a relaunch into a pod being
// built holds it: the claim's machine keeps its own lock until release is called, which the
// test's cleanup calls too. A relaunching claim records no pod (supervise's letGo), so every sweep
// from here on reads issue podless.
func (r *fleetRig) holdInARelaunch(issue string) (release func()) {
	r.t.Helper()
	// The locator is read before the machine is held: a read of the claim itself waits the
	// decision in flight out (Machine.Claim).
	architect := r.token(issue, claim.RoleArchitect)
	died := *r.claimOf(issue, claim.RoleArchitect).Locator
	r.rt.ScriptResume(fake.SpawnResult{Locator: *podUpdateLocator(architect, issue, r.pods[issue])})
	entered, held := make(chan struct{}), make(chan struct{})
	release = sync.OnceFunc(func() { close(held) })
	r.t.Cleanup(release)
	r.rt.HoldResume(func() {
		r.rt.HoldResume(nil)
		close(entered)
		<-held
	})
	go func() {
		m, _ := r.sup.Machine(architect)
		_ = m.Handle(r.ctx, supervise.RuntimeObservation{
			Observation: runtime.Observation{Locator: died, Kind: runtime.Gone, Detail: "the process ended"},
		})
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		r.t.Fatal("the architect's machine never reached the Resume this test holds it in")
	}
	r.podless(issue)
	return release
}

// sweepEnds is the rig's tick run on a goroutine of its own, and fails the test, naming what,
// when the sweep has not ended within testwait's bound: a sweep that waits on a held machine would
// otherwise hold the test with it. The claims are read here, on the test's goroutine, which is
// the one a failed read may stop (sweepClaims).
func (r *fleetRig) sweepEnds(what string) {
	r.t.Helper()
	claims := r.sweepClaims()
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		r.s.reconcilePodUpdates(r.ctx, claims)
	}()
	testwait.Eventually(r.t, what, func() bool {
		select {
		case <-swept:
			return true
		default:
			return false
		}
	})
}

// The sweep never waits on a claim's machine to mark a late round told. A relaunch into a pod
// being built holds the claim's machine for minutes, and a sweep that waited on it would hold
// every other issue's round, and the orphan reaping behind them, for as long. The mark of a claim
// whose machine is busy is made at a later sweep instead, dated when the round was told, and the
// round is told once all the same: not again while the mark waits, and not again after.
func TestPodUpdateTheSweepMarksALateRoundToldWithoutWaitingOnARelaunch(t *testing.T) {
	relaunching, other := "ACME-1", "ACME-2"
	rig := newLimitRig(t, relaunching, other)

	// The other issue drifts only after the first sweep, so its round is one the held sweep opens.
	rig.drifts(other)
	rig.tick()
	rig.awaitRounds("the first sweep", relaunching)
	rig.drifts(other, limitDrift)

	// Past the bound of the first issue's round, with its one worker in a relaunch, the sweep
	// tells the controller of the round, and goes on to open the other issue's round.
	release := rig.holdInARelaunch(relaunching)
	rig.clock.advance(podUpdateGrace + time.Minute)
	told := rig.clock.Now()
	rig.sweepEnds("the sweep past the late round's bound to end while a relaunch holds its one claim")
	if notices := rig.controllerNotices(); len(notices) != 1 {
		t.Fatalf("the sweep past the bound told the controller %d times, want once: %+v", len(notices), notices)
	}
	rig.wantPodUpdateRecord("the sweep that told the late round", api.PodUpdateRounds{Swept: true, InRound: []string{other}})
	rig.awaitRound(other, claim.RoleArchitect)

	// A minute on, the relaunch still holds the claim, and its mark is still owed: the round is
	// not told again.
	rig.clock.advance(time.Minute)
	rig.sweepEnds("a second sweep to end while the relaunch holds the claim")
	if notices := rig.controllerNotices(); len(notices) != 1 {
		t.Fatalf("the controller has been told %d times while the mark waited for the relaunch, want once: %+v", len(notices), notices)
	}

	// The relaunch ends, and a later sweep makes the mark it owed, dated when the round was told.
	// Which sweep is no part of what this pins: the machine can be deciding what the relaunch left
	// it at the first one.
	release()
	rig.sent(relaunching, claim.RoleArchitect)
	testwait.Eventually(t, "a sweep after the relaunch to make the told mark it owed", func() bool {
		rig.clock.advance(time.Minute)
		rig.tick()
		return rig.claimOf(relaunching, claim.RoleArchitect).UpdateToldAt != nil
	})
	if got := rig.claimOf(relaunching, claim.RoleArchitect).UpdateToldAt; !got.Equal(told) {
		t.Errorf("the round was marked told at %s, want %s, when the sweep told it", got, told)
	}
	rig.clock.advance(time.Minute)
	rig.tick()
	if notices := rig.controllerNotices(); len(notices) != 1 {
		t.Fatalf("the controller has been told %d times of the round, want once: %+v", len(notices), notices)
	}
}
