package daemon

// INFRA-341's reviewer's point 1 on #5285 at 0259f8af (R7 of dispatch://INFRA-3271): the moment a
// keep of a pod records is the moment after which a turn of the issue owes a read, once that turn
// has ended. A turn that started before the read, and that the reading the keep was decided on did
// not show, must not fall before that moment: its work may not be in the read, and no later read
// would be made for it. So the sweep's walk decides only whether the keep holds the pod; the read
// that may end the keep is a step after the walk, which reads the claims as their machines hold
// them then, and the keep's moment is taken before that reading and moved before the start of any
// turn that runs once the read is made. The same holds for the keep a round makes at its read.

import (
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// keptRoles are the two roles of the kept pod in each case here.
var keptRoles = []claim.Role{claim.RoleImplementer, claim.RoleTester}

// The tester's turn ends, so the read of the kept pod is due. The sweep reads its claims, and the
// implementer's turn starts before the sweep reads the workspace: the snapshot shows no turn. The
// read of the workspace the implementer's turn owes is made once that turn has ended.
func TestAKeptPodIsReadAgainAfterATurnTheSweepsSnapshotMissed(t *testing.T) {
	t.Parallel()
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, roundUnpushed)
	rig.answerRound(podUpdateDrift, keptRoles...)
	rig.roundEndsKept("over a workspace that holds an unpushed commit", keptRoles...)
	rig.turn(claim.RoleTester, nil)

	snapshot, err := rig.sup.Claims(rig.ctx)
	if err != nil {
		t.Fatalf("read the sweep's snapshot: %v", err)
	}
	rig.clock.advance(time.Minute)
	rig.envoyTurn(claim.RoleImplementer)
	rig.s.reconcilePodUpdates(rig.ctx, snapshot)
	rig.handle(claim.RoleImplementer, supervise.StreamTurnEnd{Claim: rig.tokens[claim.RoleImplementer]})

	ended := rig.workspaceReads()
	for range roundSweeps {
		rig.tick()
	}
	if reads := rig.workspaceReads() - ended; reads != 1 {
		t.Fatalf("the kept pod's workspace was read %d times after the implementer's turn ended, want once: that turn started before the sweep's read, which cannot have seen its work", reads)
	}
}

// The implementer's turn starts while the round's own read runs, at the moment the read was taken.
// The round keeps the pod over the commit it found, and the read that turn owes is made once it has
// ended.
func TestAKeptPodIsReadAgainAfterATurnThatStartedAsItsRoundRead(t *testing.T) {
	t.Parallel()
	rig := newPodUpdateRig(t, readyWorker(claim.RoleImplementer), readyWorker(claim.RoleTester))
	rig.rt.ScriptWorkspaceWork(podUpdateIssue, roundUnpushed)
	rig.answerRound(podUpdateDrift, keptRoles...)
	var started bool
	rig.rt.OnWorkspaceWork(func() {
		if !started {
			started = true
			rig.envoyTurn(claim.RoleImplementer)
		}
	})
	rig.roundEndsKept("over a workspace that holds an unpushed commit", keptRoles...)
	rig.rt.OnWorkspaceWork(nil)
	if !started {
		t.Fatal("the round read no workspace, so this case proves nothing")
	}
	if reads := rig.workspaceReads(); reads != 1 {
		t.Fatalf("the round read the workspace %d times, want once", reads)
	}

	rig.handle(claim.RoleImplementer, supervise.StreamTurnEnd{Claim: rig.tokens[claim.RoleImplementer]})
	for range roundSweeps {
		rig.tick()
	}
	if reads := rig.workspaceReads(); reads != 2 {
		t.Fatalf("the kept pod's workspace was read %d times after the implementer's turn ended, want once: that turn ran as the round read", reads-1)
	}
}
