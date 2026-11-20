package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// droppedForward is the merge forward of main onto c0de that a move of LEGION-208 into
// awaiting_children dropped before it landed (record.PhaseEnded.MergeForward).
var droppedForward = record.DroppedMergeForward{Head: "c0de", Base: "main"}

// droppedForwardBody is the body of the role message the implementer of LEGION-208 is sent when a
// move into awaiting_children that stops no phase of its own drops droppedForward
// (dispatch://INFRA-3320 R10): the merge forward named, the begun and not-begun rule, when a merge
// forward is its to make again, and when the issue's next task comes. internal/admit's
// TestParentWaitsHoldsEveryPhaseStart asserts the same words (pwDroppedBody).
const droppedForwardBody = "LEGION-208: the issue moved into awaiting_children, where it waits on its open Dispatch children." +
	" The daemon dropped the merge forward of main onto head c0de that it asked of you for LEGION-208: if you have begun that merge, finish it, push it and do nothing more; if you have not begun it, begin none." +
	" The daemon does not ask for that merge forward again: in a later task of the issue, merge main forward only once GitHub reads its pull request CONFLICTING." +
	" The daemon gives the issue its next task once its children are finished."

// endedDroppingTheForward is the phase-ended message the admission pass queues when it stops the
// tester of LEGION-208 in the middle of testing and drops droppedForward, and
// endedDroppingTheForwardText what it publishes: the form for a worker other than the
// implementer, which writes and pushes nothing more. internal/record's
// TestAPhaseEndedMessageIntoAwaitingChildrenGivesTheWaitsOwnWords reads the same form word for
// word.
var (
	endedDroppingTheForward = record.PhaseEnded{Role: claim.RoleTester, From: phase.Testing, To: phase.AwaitingChildren,
		Reason: endedByTheWait.Reason, MergeForward: &droppedForward}
	endedDroppingTheForwardText = "LEGION-208: the daemon ended your testing phase, and the issue is in awaiting_children now: it waits on its open Dispatch children, and no task of yours follows." +
		" A completion of testing is refused (HANDOFF_NOT_CURRENT_PHASE)." +
		" Write and push nothing more, because the implementer may be merging in the workspace you share: put what you found on the Dispatch issue, and do no more work on testing." +
		" The daemon gives the issue its next task once its children are finished." +
		" Nothing of yours is stopped: your turn, your jobs, your process and your session run on." +
		" Reason: " + endedByTheWait.Reason + "."
)

// The daemon's side of a parent's move into awaiting_children that drops a merge forward before
// it landed (dispatch://INFRA-3320 R10, and its check
// dispatch://INFRA-3320/artifact/verification-md#b-k12, whose command runs this test beside
// internal/admit's of the same name): the outbox executor delivers the rows that move writes.
// The implementer's role message is a role message as main delivers every one
// (outbox.roleMessage): to a released implementer it ends with no publish and no wake, as
// TestOutboxRoleMessageToAReleasedRoleEndsWithoutAWake reads of any role message, and to a claim
// not released it is published to the implementer's role topic with its body whole. The
// phase-ended message to a released tester takes its delivery wake as the plain one does
// (TestAPhaseEndedMessageForAReleasedPodIsKeptForItsWokenSession), and publishes the words for a
// worker other than the implementer.
func TestParentWaitsHoldsEveryPhaseStart(t *testing.T) {
	t.Run("the implementer's role message naming the dropped merge forward ends at a released implementer with no publish and no wake", func(t *testing.T) {
		ctx := context.Background()
		publisher := &outboxPublisher{}
		runner, sup, pool := hookRunner(t, publisher)
		implementer := releasedClaim(t, sup, claim.RoleImplementer)
		token := mustClaimToken(t, "LEGION-208", claim.RoleImplementer)
		enqueueOutbox(t, pool, runner.records, mustOutboxRow(t, "LEGION-208",
			record.RoleMessage{Role: string(token), Body: droppedForwardBody}, time.Now().Add(-time.Minute)))

		if err := runner.RunOnce(ctx); err != nil {
			t.Fatalf("drain the role message: %v", err)
		}

		if published := publisher.publishes(); len(published) != 0 {
			t.Fatalf("publishes = %#v, want none: the released implementer is not told", published)
		}
		if left := outboxRows(t, pool); left != 0 {
			t.Fatalf("outbox rows left = %d, want the message finished", left)
		}
		if held := implementer.Claim(); !held.Released() {
			t.Fatalf("the implementer is %s by %s, want it still released: the message is no wake", held.State, held.SuspendedBy)
		}
		if resumes := podResumes(t, pool, "LEGION-208"); resumes != 0 {
			t.Fatalf("recorded wakes = %d, want none: the message is no wake", resumes)
		}
	})

	t.Run("the implementer's role message naming the dropped merge forward reaches a claim not released whole", func(t *testing.T) {
		ctx := context.Background()
		publisher := &outboxPublisher{}
		runner, sup, pool := hookRunner(t, publisher)
		startedClaim(t, sup, claim.RoleImplementer)
		token := mustClaimToken(t, "LEGION-208", claim.RoleImplementer)
		enqueueOutbox(t, pool, runner.records, mustOutboxRow(t, "LEGION-208",
			record.RoleMessage{Role: string(token), Body: droppedForwardBody}, time.Now().Add(-time.Minute)))

		if err := runner.RunOnce(ctx); err != nil {
			t.Fatalf("drain the role message: %v", err)
		}

		topic := notify.RoleTopicPrefix + string(token)
		if published := publisher.publishes(); len(published) != 1 || published[0].topic != topic || published[0].message != droppedForwardBody {
			t.Fatalf("publishes = %#v, want the one message to %s, its body whole:\n\t%q", published, topic, droppedForwardBody)
		}
		if left := outboxRows(t, pool); left != 0 {
			t.Fatalf("outbox rows left = %d, want the message finished", left)
		}
	})

	t.Run("the phase-ended message naming the dropped merge forward wakes the released tester and tells it to write and push nothing more", func(t *testing.T) {
		keptAcrossTheWake(t, endedDroppingTheForward, claim.RoleTester, endedDroppingTheForwardText)
	})
}
