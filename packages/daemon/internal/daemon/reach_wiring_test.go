package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/api"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/fleetalert"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/stream"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The close of a connection carries the generation its hello resolved to into the machine, so
// the close of a generation the claim has left is not read as the claim's close (R3).
func TestTheStreamsCloseCarriesItsGenerationToTheMachine(t *testing.T) {
	ev, err := superviseEvent(stream.Closed{Claim: "legion-legion-legion-209-implementer", Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	if closed, ok := ev.(supervise.StreamClosed); !ok || closed.Generation != 7 {
		t.Fatalf("superviseEvent = %#v, want a StreamClosed of generation 7", ev)
	}
}

// relaunchRig is the daemon's relaunch of a claim it has marked out of reach (relaunchByDaemon)
// over a bare supervisor on the fake runtime and the reach rig's keeper, as openSupervision wires
// it: the keeper's relaunch posts RequestRelaunch to the claim's machine.
func relaunchRig(t *testing.T) (*reachRig, *supervisor) {
	t.Helper()
	sup, _ := newPluginSupervisor(t, api.DaemonAPIVersion, true)
	// The store's report of the launch, which the machine's own guard reads: the activity the
	// claim was restored with, read off its published view, since the read is made inside the
	// machine's decision.
	sup.deps.ClaimActivity = func(_ context.Context, token claim.Token, _ uint64) (supervise.ActivityReport, bool, error) {
		m, ok := sup.Machine(token)
		if !ok {
			return supervise.ActivityReport{}, false, nil
		}
		c := m.View().Claim
		return supervise.ActivityReport{At: c.ActivityAt, Background: c.ActivityBackground, Kinds: c.ActivityKinds}, !c.ActivityAt.IsZero(), nil
	}
	r := newReachRig(t, false)
	s := &supervision{log: quietLogger(), supervisor: sup, reach: r.k}
	r.k.relaunch = s.relaunchByDaemon
	return r, sup
}

// waitingQuietly is c in a long wait with nothing running and a report of its launch, which the
// guard lets through.
func waitingQuietly(c supervise.Claim) supervise.Claim {
	c.State = supervise.StateWorking
	c.Session, c.SessionFile = "ses_"+string(c.Role), "/sessions/"+string(c.Role)+".jsonl"
	c.PluginContract = api.DaemonAPIVersion
	c.Step = supervise.StepRecord{At: reachBoot.Add(-time.Hour), Tool: "wait", CallID: "toolu_wait", StartedAt: reachBoot.Add(-time.Hour)}
	c.ActivityAt = reachBoot.Add(-50 * time.Minute)
	c.Pending = &supervise.Delivery{ID: "delivery", Task: "work", QueuedAt: reachBoot.Add(-2 * time.Hour),
		DeliveredAt: reachBoot.Add(-2 * time.Hour), ConfirmedAt: reachBoot.Add(-2 * time.Hour)}
	return c
}

// The daemon's relaunch reaches the claim's machine once the keeper has marked the claim: its
// launch moves the claim's generation and records the relaunch, which the keeper takes in, and no
// later sweep within 6 hours relaunches it again.
func TestTheDaemonsRelaunchReachesTheClaimsMachineOnce(t *testing.T) {
	r, sup := relaunchRig(t)
	worker := waitingQuietly(reachClaim(t, "LEGION-208", "LEGION-209", claim.RoleImplementer, "ses_implementer"))
	if _, err := sup.restore(context.Background(), []supervise.Claim{worker}); err != nil {
		t.Fatal(err)
	}
	m, _ := sup.Machine(worker.Token)
	generation := m.Claim().Generation

	r.at(19 * time.Minute)
	r.listing(map[string]time.Duration{"ses_other": 19 * time.Minute})
	r.sweep(worker)
	if got := m.Claim().Generation; got != generation || r.reach(worker.Token).Unreachable() {
		t.Fatalf("at 19 minutes the claim is at generation %d with the record %+v, want %d and no mark", got, r.reach(worker.Token), generation)
	}

	r.at(21 * time.Minute)
	r.listing(map[string]time.Duration{"ses_other": 21 * time.Minute})
	r.sweep(worker)
	testwait.Eventually(t, "the relaunch to move the claim's generation", func() bool { return m.Claim().Generation > generation })
	if got := r.reach(worker.Token); !got.Unreachable() || !got.Relaunched || !got.RelaunchedAt.Equal(m.Claim().LastRelaunch) {
		t.Fatalf("the keeper's record after the relaunch = %+v, want the claim marked and the relaunch at %v recorded", got, m.Claim().LastRelaunch)
	}
	relaunched := m.Claim().Generation
	for minute := 22; minute <= 30; minute++ {
		r.at(time.Duration(minute) * time.Minute)
		r.listing(map[string]time.Duration{"ses_other": time.Duration(minute) * time.Minute})
		r.sweep(r.with(m.Claim()))
	}
	if got := m.Claim().Generation; got != relaunched {
		t.Fatalf("after nine more sweeps the claim is at generation %d, want %d: one relaunch", got, relaunched)
	}
}

// A relaunch the machine's own guard refuses, reading a later activity report than the sweep
// read, is recorded as the guard's refusal, relaunches nothing and writes no line before the
// guard has held the claim for 60 minutes.
func TestTheDaemonsRelaunchRefusedByTheMachinesGuardIsRecorded(t *testing.T) {
	r, sup := relaunchRig(t)
	worker := waitingQuietly(reachClaim(t, "LEGION-208", "LEGION-209", claim.RoleImplementer, "ses_implementer"))
	busy := worker
	busy.ActivityBackground, busy.ActivityKinds = true, []string{"subagent"}
	if _, err := sup.restore(context.Background(), []supervise.Claim{busy}); err != nil {
		t.Fatal(err)
	}
	m, _ := sup.Machine(worker.Token)
	generation := m.Claim().Generation
	r.at(21 * time.Minute)
	r.listing(map[string]time.Duration{"ses_other": 21 * time.Minute})
	r.sweep(worker)

	if got := r.reach(worker.Token); got.GuardRefusedAt.IsZero() || got.Relaunched {
		t.Fatalf("the keeper's record after the machine's refusal = %+v, want the refusal recorded and no relaunch", got)
	}
	if got := m.Claim().Generation; got != generation {
		t.Fatalf("the refused relaunch moved the claim to generation %d, want %d", got, generation)
	}
	if lines := r.lines(fleetalert.ClaimRelaunchRefused.Word); lines != 0 {
		t.Fatalf("R6 wrote %d refused lines at the guard's first refusal, want none", lines)
	}
}

// The daemon's path refuses a pod that holds two or more live claims, every one out of reach,
// before the machine is asked: neither claim's generation moves. Once one of them registers again,
// the other is relaunched through its machine.
func TestTheDaemonsRelaunchRefusesAPodWhoseEveryClaimIsOutOfReach(t *testing.T) {
	r, sup := relaunchRig(t)
	pod := func(c supervise.Claim) supervise.Claim {
		c.Locator = &runtime.Locator{Runtime: runtime.RuntimeSandbox, Claim: c.Token, Incarnation: "pod-a:3",
			Sandbox: &runtime.SandboxLocator{Namespace: "legion", Name: "legion-legion-208", PodUID: "pod-a", Container: "roles", Generation: 3}}
		return c
	}
	worker := pod(waitingQuietly(reachClaim(t, "LEGION-208", "LEGION-209", claim.RoleImplementer, "ses_implementer")))
	tester := pod(waitingQuietly(reachClaim(t, "LEGION-208", "LEGION-209", claim.RoleTester, "ses_tester")))
	if _, err := sup.restore(context.Background(), []supervise.Claim{worker, tester}); err != nil {
		t.Fatal(err)
	}
	m, _ := sup.Machine(worker.Token)
	generation := m.Claim().Generation
	r.at(21 * time.Minute)
	r.listing(map[string]time.Duration{"ses_other": 21 * time.Minute})
	r.sweep(worker, tester)
	if got := m.Claim().Generation; got != generation || r.lines(fleetalert.ClaimRelaunchRefused.Word) != 2 {
		t.Fatalf("a pod whose two claims are out of reach: the worker at generation %d and %d refused lines, want %d and 2",
			got, r.lines(fleetalert.ClaimRelaunchRefused.Word), generation)
	}

	// The tester registers again: the pod reaches Envoy for one claim, and the worker's relaunch
	// goes on.
	r.at(22 * time.Minute)
	r.listing(map[string]time.Duration{"ses_tester": 22 * time.Minute})
	r.sweep(r.with(worker), r.with(tester))
	testwait.Eventually(t, "the worker's relaunch to move its generation", func() bool { return m.Claim().Generation > generation })
}

// An operator's suspension held over the claim for its turn refuses the daemon's relaunch at the
// claim's machine: the operator decides its process. The claim's generation does not move, no line
// is written however long the suspension is held, and the refusal is logged once.
func TestTheDaemonsRelaunchOfAClaimAnOperatorSuspendsIsRefusedWithNoLine(t *testing.T) {
	r, sup := relaunchRig(t)
	worker := waitingQuietly(reachClaim(t, "LEGION-208", "LEGION-209", claim.RoleImplementer, "ses_implementer"))
	if _, err := sup.restore(context.Background(), []supervise.Claim{worker}); err != nil {
		t.Fatal(err)
	}
	m, _ := sup.Machine(worker.Token)
	if err := m.Handle(context.Background(), supervise.RequestSuspend{Claim: worker.Token, By: supervise.SuspendedByOperator,
		Reason: "the operator suspends it"}); !errors.Is(err, supervise.ErrSuspendHeld) {
		t.Fatalf("the operator's suspension of the claim in its turn = %v, want it held for the turn", err)
	}
	generation := m.Claim().Generation
	for minute := 21; minute <= 90; minute++ {
		r.at(time.Duration(minute) * time.Minute)
		r.listing(map[string]time.Duration{"ses_other": time.Duration(minute) * time.Minute})
		r.sweep(r.with(m.Claim()))
	}
	if got := m.Claim().Generation; got != generation {
		t.Fatalf("the relaunch moved the claim an operator suspends to generation %d, want %d", got, generation)
	}
	if lines := r.lines(fleetalert.ClaimRelaunchRefused.Word) + r.lines(fleetalert.ClaimUnreachable.Word); lines != 0 {
		t.Fatalf("R6 wrote %d lines for the claim an operator suspends, want none", lines)
	}
	if got := r.reach(worker.Token); got.SuspensionHeldAt.IsZero() || !got.HeldAt.IsZero() || !got.GuardRefusedAt.IsZero() {
		t.Fatalf("the keeper's record = %+v, want the operator's suspension recorded and no other refusal", got)
	}
}

// While the project is paused the daemon makes no relaunch of a workflow claim, since a paused
// project starts no turn of a role but the controller's: the claim's generation does not move, no
// line is written and no guard refusal is recorded. The sweep after the resume relaunches it.
func TestTheDaemonsRelaunchOfAWorkflowClaimWaitsWhileTheProjectIsPaused(t *testing.T) {
	r, sup := relaunchRig(t)
	worker := waitingQuietly(reachClaim(t, "LEGION-208", "LEGION-209", claim.RoleImplementer, "ses_implementer"))
	if _, err := sup.restore(context.Background(), []supervise.Claim{worker}); err != nil {
		t.Fatal(err)
	}
	// The supervisor's own reading of the pause, which the daemon's relaunch path reads; set after
	// the restore, so the machine is restored as it would be in a running project.
	paused := &atomic.Bool{}
	paused.Store(true)
	sup.deps.Paused = func() (int64, bool) { return 7, paused.Load() }
	m, _ := sup.Machine(worker.Token)
	generation := m.Claim().Generation
	for minute := 21; minute <= 25; minute++ {
		r.at(time.Duration(minute) * time.Minute)
		r.listing(map[string]time.Duration{"ses_other": time.Duration(minute) * time.Minute})
		r.sweep(r.with(m.Claim()))
	}
	got := r.reach(worker.Token)
	if m.Claim().Generation != generation || !got.Unreachable() || got.Relaunched || !got.GuardRefusedAt.IsZero() {
		t.Fatalf("while paused the claim is at generation %d with the record %+v, want generation %d, marked, not relaunched and no guard refusal",
			m.Claim().Generation, got, generation)
	}
	if lines := r.lines(fleetalert.ClaimRelaunchRefused.Word) + r.lines(fleetalert.ClaimUnreachable.Word); lines != 0 {
		t.Fatalf("R6 wrote %d lines for a claim whose relaunch waits for the resume, want none", lines)
	}
	paused.Store(false)
	r.at(26 * time.Minute)
	r.listing(map[string]time.Duration{"ses_other": 26 * time.Minute})
	r.sweep(r.with(m.Claim()))
	testwait.Eventually(t, "the relaunch after the resume to move the claim's generation", func() bool { return m.Claim().Generation > generation })
}
