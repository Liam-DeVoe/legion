package daemon

import (
	"context"
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
