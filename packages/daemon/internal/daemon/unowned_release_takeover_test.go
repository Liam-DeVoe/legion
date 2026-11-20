package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// stoppedPodForTakeover leaves the rig's pod as the release of a pod no claim owns takes it: an
// architect an earlier release left `released` and an implementer whose launch budget ran out,
// so every claim is stopped, none records a process and the pod has been quiet past the idle
// period. It answers the architect's machine, which is the claim an operator takes over.
func stoppedPodForTakeover(t *testing.T) (*idleRig, *supervise.Machine) {
	t.Helper()
	rig := newIdleRig(t)
	architect := rig.releasedArchitect(t)
	rig.stoppedImplementer(t, false)
	return rig, architect
}

// suspendAs is a suspension of a claim that is already suspended, posted to its machine as the
// operator's route (`legion claims suspend`) or a daemon rule posts it. A source that outranks
// the one holding the claim takes the suspension over and keeps the claim's session
// (supervise's suspendedAgain). It reports failures with Errorf, since the runtime's hook calls
// it from the teardown's own row.
func suspendAs(t *testing.T, m *supervise.Machine, by supervise.SuspendSource) {
	t.Helper()
	c := m.Claim()
	if err := m.Handle(context.Background(), supervise.RequestSuspend{Claim: c.Token,
		Reason: "suspended after the release was queued", By: by}); err != nil {
		t.Errorf("the %s suspend of %s: %v", by, c.Token, err)
		return
	}
	if got := m.Claim(); got.State != supervise.StateSuspended || got.SuspendedBy != by {
		t.Errorf("after the %s suspend the architect is %s by %s, want suspended by %s", by, got.State, got.SuspendedBy, by)
	}
}

// queueUnownedRelease runs the pass of the second release rule and requires it to have queued
// that release's one row, the teardown, under the release's mark.
func queueUnownedRelease(t *testing.T, rig *idleRig) {
	t.Helper()
	queued := rig.queuedBy(t, func() { rig.w.releaseUnownedPods(context.Background()) })
	if len(queued) != 1 || queued[0] != "issue_release" || !rig.marked(t) {
		t.Fatalf("the pass queued %v (marked %t), want the release's teardown alone under its mark", queued, rig.marked(t))
	}
}

// keptForTheOperator requires what a teardown that found the operator's suspension leaves: the
// pod standing, the release cancelled with its mark gone, no release recorded and one hold under
// the second rule's own name for the operator's suspension, the architect still the operator's
// with its session, and the cancel's own line, which says no role is coming back.
func keptForTheOperator(t *testing.T, rig *idleRig, architect *supervise.Machine, logs *syncBuffer) {
	t.Helper()
	if _, teardowns := rig.rt.reads(); teardowns != 0 {
		t.Fatalf("Sandbox teardowns = %d, want none: the operator's suspension holds the pod", teardowns)
	}
	if rig.marked(t) {
		t.Fatal("the cancelled release left its mark behind")
	}
	records := rig.records(t)
	if len(records.Releases) != 0 {
		t.Fatalf("releases recorded = %+v, want none: the release was cancelled", records.Releases)
	}
	want := workflow.IdleHoldUnownedOperatorSuspended.String()
	if len(records.Holds) != 1 || records.Holds[0].Reason != want || records.Holds[0].Issue != rig.issue.Key {
		t.Fatalf("holds recorded = %+v, want the one %s of %s", records.Holds, want, rig.issue.Key)
	}
	if got := architect.Claim(); got.State != supervise.StateSuspended || got.SuspendedBy != supervise.SuspendedByOperator ||
		got.Session != "ses-architect" {
		t.Fatalf("the architect is %s by %s with session %q, want the operator's suspension keeping ses-architect",
			got.State, got.SuspendedBy, got.Session)
	}
	line := releaseLogLine(t, logs, idleRigUnownedCancel)
	if !strings.Contains(line, "issue="+rig.issue.Key) || !strings.Contains(line, "hold="+want) {
		t.Errorf("the cancelled release's line does not name the issue and the hold that stopped it:\n%s", line)
	}
	saysNoRoleComesBack(t, logs, line, "the cancelled release's line")
}

// The release of a pod no claim owns holds a pod for as long as an operator's suspension holds a
// claim of it (workflow.UnownedPodRelease). The pass reads that suspension when it queues the
// release, and the release is one row, the teardown, which runs later on the issue's lane. In
// between, an operator can suspend a claim of the pod that is already suspended, which takes
// the suspension over. The teardown reads it again, at its own read and under the runtime's pod
// lock, and keeps the pod: the operator's resume then starts the role in the pod that is still
// there, with the workspace as the roles left it.
//
// The controls are the same suspension made before the queue, which the pass itself holds the
// pod for; a daemon's suspension after the queue, which is no operator's and holds nothing; and
// a pause after the queue, which a teardown of this release does not read (the pause holds the
// rows its own decisions name, and the teardown is none of them).
func TestAnOperatorsSuspensionAfterTheUnownedReleaseIsQueuedKeepsThePod(t *testing.T) {
	t.Run("the operator suspends a claim after the queue", func(t *testing.T) {
		rig, architect := stoppedPodForTakeover(t)
		logs, log := watchedLog()
		rig.runner.log = log
		queueUnownedRelease(t, rig)

		suspendAs(t, architect, supervise.SuspendedByOperator)
		rig.drain(t)

		keptForTheOperator(t, rig, architect, logs)
	})

	t.Run("the operator suspends a claim under the runtime's pod lock", func(t *testing.T) {
		rig, architect := stoppedPodForTakeover(t)
		logs, log := watchedLog()
		rig.runner.log = log
		queueUnownedRelease(t, rig)
		// The teardown's own read has answered that the pod may go; the runtime's read under its
		// pod lock is the one the pod is taken on, and the suspension lands between the two.
		rig.rt.beforeAuthorize = func() { suspendAs(t, architect, supervise.SuspendedByOperator) }

		rig.drain(t)

		if refused := rig.rt.refusals(); refused != 1 {
			t.Fatalf("authorizations refused under the pod lock = %d, want the one the teardown asked for", refused)
		}
		keptForTheOperator(t, rig, architect, logs)
	})

	t.Run("control: the operator's suspension before the queue keeps the pod", func(t *testing.T) {
		rig, architect := stoppedPodForTakeover(t)
		suspendAs(t, architect, supervise.SuspendedByOperator)
		// The suspend's write stamps the claim row now, which is a write inside the quiet
		// period; backdated with the rest of the pod, the suspension is what holds it.
		rig.wrote(t, architect.Claim().Token, 30)

		if queued := rig.queuedBy(t, func() { rig.w.releaseUnownedPods(context.Background()) }); len(queued) != 0 {
			t.Fatalf("the pass queued %v, want nothing: the operator's suspension holds the pod", queued)
		}
		rig.drain(t)

		if _, teardowns := rig.rt.reads(); teardowns != 0 {
			t.Fatalf("Sandbox teardowns = %d, want none", teardowns)
		}
		want := workflow.IdleHoldUnownedOperatorSuspended.String()
		if holds := rig.records(t).Holds; len(holds) != 1 || holds[0].Reason != want {
			t.Fatalf("holds recorded = %+v, want the pass's one %s", holds, want)
		}
	})

	t.Run("control: a daemon's suspension after the queue holds nothing", func(t *testing.T) {
		rig, architect := stoppedPodForTakeover(t)
		queueUnownedRelease(t, rig)

		suspendAs(t, architect, supervise.SuspendedByDaemon)
		rig.drain(t)

		if _, teardowns := rig.rt.reads(); teardowns != 1 {
			t.Fatalf("Sandbox teardowns = %d, want the one: a daemon's suspension is one a daemon rule ends, and holds no pod", teardowns)
		}
		if holds := rig.records(t).Holds; len(holds) != 0 {
			t.Fatalf("holds recorded = %+v, want none: the pod was released", holds)
		}
	})

	t.Run("control: a pause after the queue does not hold the teardown", func(t *testing.T) {
		rig, _ := stoppedPodForTakeover(t)
		queueUnownedRelease(t, rig)

		openLegionPause(t, rig.pool, time.Now().UTC())
		rig.drain(t)

		if _, teardowns := rig.rt.reads(); teardowns != 1 {
			t.Fatalf("Sandbox teardowns = %d, want the one: a release of a pod no claim owns has no save, so a pause never cancels it", teardowns)
		}
		if records := rig.records(t); len(records.Releases) != 1 || len(records.Holds) != 0 {
			t.Fatalf("records = %d releases and holds %+v, want the one release and no hold", len(records.Releases), records.Holds)
		}
	})
}

// releaseID is the id of the release the rig's issue has in flight (issue_releases.id).
func (r *idleRig) releaseID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := r.pool.QueryRow(context.Background(), `select id from issue_releases where issue = $1`, r.issue.Key).Scan(&id); err != nil {
		t.Fatalf("read the release in flight: %v", err)
	}
	return id
}

// storedSuspension is the state and the suspension's source the store holds for m's claim.
func (r *idleRig) storedSuspension(t *testing.T, m *supervise.Machine) (state, by string) {
	t.Helper()
	if err := r.pool.QueryRow(context.Background(), `select state, suspended_by from claims where token = $1`,
		string(m.Claim().Token)).Scan(&state, &by); err != nil {
		t.Fatalf("read the stored claim: %v", err)
	}
	return state, by
}

// suspendAsOperator posts the operator's suspend (`legion claims suspend`) to m's claim and answers
// what the machine answered, which the route reads: a *supervise.RefusedError is its 409.
func suspendAsOperator(m *supervise.Machine) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return m.Handle(ctx, supervise.RequestSuspend{Claim: m.Claim().Token, Reason: "the operator suspended it",
		By: supervise.SuspendedByOperator})
}

// refusedWhileTheReleaseTakesThePod requires err to be the operator's answer while release takes
// the rig's pod: a refusal (409 on the operator's route) whose sentence names the release and
// the pod's Sandbox, so the operator learns the pod is going.
func refusedWhileTheReleaseTakesThePod(t *testing.T, err error, rig *idleRig, release int64) {
	t.Helper()
	var refused *supervise.RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("the operator's suspend after the teardown's last read answered %v, want a refusal: the pod it would hold is being deleted", err)
	}
	if named := fmt.Sprintf("release %d of %s", release, rig.issue.Key); !strings.Contains(err.Error(), named) ||
		!strings.Contains(err.Error(), rig.rt.sandboxName) {
		t.Fatalf("the refusal %q does not name %q and the Sandbox %s", err, named, rig.rt.sandboxName)
	}
}

// The runtime takes the pod on the teardown's last read, the one it asks under its pod lock,
// and reads the store no more: it sets the Sandbox suspended, waits the pod out and deletes it.
// An operator's suspension that comes in that window cannot keep the pod, so the daemon refuses
// it, naming the release and the pod, and leaves the claim as it was: no operator is told a pod
// is held that is going. Once the release has ended, the same suspension is taken. Both release
// rules take the pod on that same read, so both refuse it.
//
// The control is a daemon's suspension in the same window, which holds no pod and is taken.
func TestAnOperatorsSuspensionAfterTheTeardownsLastReadIsRefused(t *testing.T) {
	t.Run("the release of a pod no claim owns refuses it and takes the pod", func(t *testing.T) {
		rig, architect := stoppedPodForTakeover(t)
		queueUnownedRelease(t, rig)
		release := rig.releaseID(t)
		hooked := false
		var suspended error
		rig.rt.afterAuthorize = func() { hooked, suspended = true, suspendAsOperator(architect) }

		rig.drain(t)

		if !hooked {
			t.Fatal("the runtime's last read never answered that the pod goes, so the window was not reached")
		}
		refusedWhileTheReleaseTakesThePod(t, suspended, rig, release)
		if got := architect.Claim(); !got.Released() || got.Session != "ses-architect" {
			t.Fatalf("the architect is %s by %s with session %q, want it left released with ses-architect", got.State, got.SuspendedBy, got.Session)
		}
		if state, by := rig.storedSuspension(t, architect); state != "suspended" || by != "released" {
			t.Fatalf("the stored architect is %s by %s, want suspended by released: a refused suspension writes nothing", state, by)
		}
		if _, teardowns := rig.rt.reads(); teardowns != 1 {
			t.Fatalf("Sandbox teardowns = %d, want the one the last read answered", teardowns)
		}
		if records := rig.records(t); len(records.Releases) != 1 || len(records.Holds) != 0 {
			t.Fatalf("records = %d releases and holds %+v, want the one release and no hold", len(records.Releases), records.Holds)
		}
		if rig.marked(t) {
			t.Fatal("the finished release left its mark behind")
		}
		// The release has ended, and the same suspension is taken now: it holds the claim, whose
		// next resume builds the issue's pod again.
		if err := suspendAsOperator(architect); err != nil {
			t.Fatalf("the operator's suspend once the release ended = %v, want it taken", err)
		}
		if state, by := rig.storedSuspension(t, architect); state != "suspended" || by != "operator" {
			t.Fatalf("the stored architect is %s by %s, want suspended by operator", state, by)
		}
	})

	t.Run("an idle release refuses it for a claim it suspended and takes the pod", func(t *testing.T) {
		rig := newIdleRig(t)
		queued := rig.queuedBy(t, func() { rig.w.releaseIdlePods(context.Background()) })
		if want := []string{"issue_bundle", "supervise:release", "issue_release"}; !slices.Equal(queued, want) {
			t.Fatalf("queued rows = %v, want the save, then the role stop, then the teardown", queued)
		}
		release := rig.releaseID(t)
		hooked := false
		var suspended error
		rig.rt.afterAuthorize = func() { hooked, suspended = true, suspendAsOperator(rig.machine) }

		rig.drain(t)

		if !hooked {
			t.Fatal("the runtime's last read never answered that the pod goes, so the window was not reached")
		}
		refusedWhileTheReleaseTakesThePod(t, suspended, rig, release)
		if got := rig.machine.Claim(); !got.Released() {
			t.Fatalf("the implementer is %s by %s, want it left released", got.State, got.SuspendedBy)
		}
		if state, by := rig.storedSuspension(t, rig.machine); state != "suspended" || by != "released" {
			t.Fatalf("the stored implementer is %s by %s, want suspended by released", state, by)
		}
		if _, teardowns := rig.rt.reads(); teardowns != 1 {
			t.Fatalf("Sandbox teardowns = %d, want the one the last read answered", teardowns)
		}
		if records := rig.records(t); len(records.Releases) != 1 || len(records.Holds) != 0 {
			t.Fatalf("records = %d releases and holds %+v, want the one release and no hold", len(records.Releases), records.Holds)
		}
		if err := suspendAsOperator(rig.machine); err != nil {
			t.Fatalf("the operator's suspend once the release ended = %v, want it taken", err)
		}
		if got := rig.machine.Claim(); got.SuspendedBy != supervise.SuspendedByOperator {
			t.Fatalf("the implementer is suspended by %s, want the operator", got.SuspendedBy)
		}
	})

	t.Run("control: a daemon's suspension in the same window is taken, and the pod goes", func(t *testing.T) {
		rig, architect := stoppedPodForTakeover(t)
		queueUnownedRelease(t, rig)
		hooked := false
		var suspended error
		rig.rt.afterAuthorize = func() {
			hooked = true
			suspended = architect.Handle(context.Background(), supervise.RequestSuspend{Claim: architect.Claim().Token,
				Reason: "a daemon rule suspended it", By: supervise.SuspendedByDaemon})
		}

		rig.drain(t)

		if !hooked || suspended != nil {
			t.Fatalf("hook ran %t, the daemon's suspend = %v; want it run and taken: a daemon's suspension holds no pod", hooked, suspended)
		}
		if state, by := rig.storedSuspension(t, architect); state != "suspended" || by != "daemon" {
			t.Fatalf("the stored architect is %s by %s, want suspended by daemon", state, by)
		}
		if _, teardowns := rig.rt.reads(); teardowns != 1 {
			t.Fatalf("Sandbox teardowns = %d, want the one", teardowns)
		}
	})
}
