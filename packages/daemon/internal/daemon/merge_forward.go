package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/wait"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// errMergeForwardWaits is a merge-forward message to an issue's owner that waits for the owner's
// slot to hold the App of the role it asks an act as (ownerSlotActs).
var errMergeForwardWaits = wait.New("the merge-forward message waits for its owner's slot")

// mergeForwardMessage publishes one message of a merge forward (record.MergeForwardMessage,
// dispatch://INFRA-4374 R1) to the Envoy topic of the role it names on the row's issue, and to
// nothing else: the ask that the implementer merge the base forward and push, the hold that tells
// the worker of the phase the issue stays in to stop writing the workspace while it does, and, once
// the merge lands, the landed to that worker and the review to the reviewer of an issue past its
// review whose task has a reviewer step (payload.Message, composed from the pull request as this
// attempt reads it). It stops nothing it reaches.
//
// The row is held as a merge-queue removal's is (mergeQueueRemoval), not ended as a plain role
// message to a released claim is (roleMessage): the issue keeps its phase while the merge runs and
// no phase start carries the ask, so a released implementer that is not told never merges, and the
// conflicting head stands with no CI and nobody at work on it. So the row is delivered as
// deliverHeld delivers a held message: a released role is woken, and the row waits until a session
// holds the role.
//
// Each attempt first reads the issue, its pull request and whether its tree lingers in one
// transaction, and finishes the row undelivered, with one log line naming why, once the message is
// no longer to be delivered by the workflow's rule for these rows (workflow.MergeForwardAt): the
// record holds no such issue, the issue has left the workflow, its tree lingers, the record holds
// no pull request or one that is not open, it holds no merge forward or one of another head, or the
// row is an ask or a hold and the merge forward has landed. Role-message rows run in no order of
// the issue's, so a hold still queued behind its landed would otherwise tell the worker to wait for
// a head that already came. What follows - a claim that failed or retired, the design gate, the
// wake and the publish - is deliverHeld's, as it is a merge-queue removal's. The message reaches the
// claim it names (record.MergeForwardMessage.Claim), the worker of its role's step when the
// workflow wrote it; a row the release before wrote names none and reaches the claim the record
// gives that step when it runs (workerToken). A message that reaches the issue's owner and asks an
// act as its role waits, once the owner's session runs, for the owner's slot to hold that role's
// App (ownerSlotActs).
func (r *outbox) mergeForwardMessage(ctx context.Context, row record.OutboxRow, payload record.MergeForwardMessage) error {
	if r.notices == nil {
		return errors.New("merge-forward message executor has no Envoy publisher")
	}
	if r.supervisor == nil {
		return errors.New("merge-forward message executor has no claim supervisor")
	}
	token, err := r.workerToken(ctx, row, payload.Role, payload.Claim)
	if err != nil {
		return err
	}
	var issue *record.Issue
	var pr *record.PullRequest
	var left string
	if err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		issue, pr, left, err = workflow.MergeForwardAt(ctx, r.records, tx, row.Issue, payload.Step, payload.Head)
		return err
	}); err != nil {
		return fmt.Errorf("read the issue and the pull request of %s: %w", row.Issue, err)
	}
	if issue == nil {
		left = "the issue is not recorded"
	}
	attributes := []any{"row", row.ID, "issue", row.Issue, "role", payload.Role, "step", payload.Step, "head", payload.Head}
	if left != "" {
		r.log.Info("outbox merge-forward message finished undelivered: the workflow no longer delivers it", append(attributes, "why", left)...)
		return nil
	}
	return r.deliverHeld(ctx, row, heldMessage{issue: *issue, token: token, role: payload.Role,
		what: fmt.Sprintf("the merge-forward %s of %s", payload.Step, row.Issue), message: payload.Message(row.Issue, *pr),
		attributes: attributes,
		claimEnded: "outbox merge-forward message finished undelivered: the role's claim has failed or retired",
		delivered:  "outbox merge-forward message delivered",
		holds:      func(ctx context.Context) error { return r.ownerSlotActs(ctx, row, *issue, token, payload) }})
}

// ownerSlotActs is the hold of a merge-forward message that reaches token where token is the
// owner of issue (record.OwnerClaim): the owner works the step of the message's role, so the message
// asks it for an act as that role, and the owner's slot holds the App of the role the act is done
// as. The ask is an act as the implementer, the merge forward the record holds asked of the owner,
// for which the slot acts as the implementer (record.OwnersMergeForward), so the runtime writes the
// slot and the ask waits until its gh files hold the implement App. The review a landing asks of
// the reviewer is an act as the reviewer, an approval GitHub refuses from the pull request's
// author, the implement App, so it waits while the owner's slot acts as an App of another role,
// through the owner's retro of its own implementation step and through a merge forward asked of it,
// and then until the write-back of the owner's own App has reached its gh files: it reaches the
// owner once, after that write-back. The landed told to the worker of the issue's phase, the
// owner's own step, says to go on with that step at the new head, so it waits the same way for that
// step's App. The hold asks no act, only that the worker stop writing the workspace, and goes at
// once. A message to another agent's claim waits for nothing here.
func (r *outbox) ownerSlotActs(ctx context.Context, row record.OutboxRow, issue record.Issue, token claim.Token, payload record.MergeForwardMessage) error {
	if payload.Step == record.MergeForwardHold {
		return nil
	}
	if owner, err := record.OwnerClaim(issue); err != nil || owner != token {
		return nil
	}
	_, slot, err := r.ownerRoles(ctx, token)
	if err != nil {
		return err
	}
	if !r.sameApp(ctx, row.Issue, slot, payload.Role) {
		return fmt.Errorf("%w: the merge-forward %s row %d of %s waits for %s's slot to act as %s, and it acts as %s",
			errMergeForwardWaits, payload.Step, row.ID, row.Issue, token, payload.Role, slot)
	}
	_, err = r.slotHoldsApp(ctx, row, token, payload.Role, errMergeForwardWaits, "merge-forward "+string(payload.Step)+" row")
	return err
}
