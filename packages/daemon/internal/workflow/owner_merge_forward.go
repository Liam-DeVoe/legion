package workflow

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A merge forward is an act as the implementer, so while the record holds one asked of the owner
// that has not ended (record.OwnersMergeForward) the owner's slot holds the implement App
// (record.OwnerActsAs, which the daemon's ActsAs reads at each write of the slot). Each end of it
// gives the owner's slot its own App back at once, as the end of each step the owner does as
// another role does (ownerSlotCredential), so no path leaves the slot on the implement App. The
// ends are the writes of two records, and each of the two writers checks it, once, for every
// caller:
//   - the pull request's (putPullRequest): the landing (mergeForwardLanded), the clear of the ask
//     on a move into implementing or a leave (clearMergeForward), and the close or the merge of the
//     pull request (closed, merged);
//   - the issue's (writeIssueOn): a hold, a stopped task's among them, and the issue's leave of the
//     workflow.
//
// A newer ask that replaces the one the record holds (mergeForward) is no end: the head moved, or
// the move out of implementing asks again, and it is asked of the same implementer's claim, so the
// slot stays on the implement App until the new ask ends. A tree whose root lingers has every role
// suspended (record.TreeLingers), and the daemon's ActsAs answers the owner's own App there, which
// the owner's next launch writes; no fact of the linger is a write of the slot.

// mergeForwardAskOpen says whether pr holds an ask of a merge forward that has not ended by the
// pull request's own state: pr is open and holds an ask that has not landed. The issue's side, and
// whose the ask was, are record.OwnersMergeForward's.
func mergeForwardAskOpen(pr record.PullRequest) bool {
	return pr.State == record.PullRequestOpen && pr.MergeForward != nil && pr.MergeForward.Landed == ""
}

// pullRequestEndsOwnersMergeForward queues the write-back of the owner's slot where after, the
// write of a pull request over before, the row the record held, ends a merge forward asked of the
// owner of its issue: before held an open ask that after does not, and that ask was the owner's
// with the issue as it stands in the transaction (record.OwnersMergeForward). An issue a write of
// the same fact took out of the workflow or held already had its write-back queued by that write
// (issueEndsOwnersMergeForward).
func (e *Engine) pullRequestEndsOwnersMergeForward(ctx context.Context, tx pgx.Tx, before *record.PullRequest, after record.PullRequest) error {
	if before == nil || !mergeForwardAskOpen(*before) || mergeForwardAskOpen(after) {
		return nil
	}
	issue, err := e.store.Issue(ctx, tx, after.Issue)
	if err != nil || issue == nil {
		return err
	}
	rows, err := e.store.Phases(ctx, tx, issue.Key)
	if err != nil {
		return err
	}
	if !record.OwnersMergeForward(*issue, false, before, rows) {
		return nil
	}
	return e.mergeForwardWriteBack(ctx, tx, *issue)
}

// issueEndsOwnersMergeForward queues the write-back of the owner's slot where after, a write of an
// issue that holds it or takes it out of the workflow, ends a merge forward asked of its owner: the
// issue as the record holds it before the write was held by nothing and in the workflow, and its
// pull request holds that ask, open (record.OwnersMergeForward). Every other write leaves the
// issue's side of the ask as it was, so it reads nothing more.
func (e *Engine) issueEndsOwnersMergeForward(ctx context.Context, tx pgx.Tx, after record.Issue) error {
	if after.Hold == nil && record.InWorkflow(after) {
		return nil
	}
	before, err := e.store.Issue(ctx, tx, after.Key)
	if err != nil || before == nil || before.Hold != nil || !record.InWorkflow(*before) {
		return err
	}
	pr, err := e.store.PullRequest(ctx, tx, after.Key)
	if err != nil || pr == nil || !mergeForwardAskOpen(*pr) {
		return err
	}
	rows, err := e.store.Phases(ctx, tx, after.Key)
	if err != nil {
		return err
	}
	if !record.OwnersMergeForward(*before, false, pr, rows) {
		return nil
	}
	return e.mergeForwardWriteBack(ctx, tx, after)
}

// mergeForwardWriteBack queues the supervise slot_credential that writes the slot of issue's owner
// back to the App of its own role (internal/daemon's slotCredential), for the end of a merge
// forward asked of the owner, once in a fact however many of its writes end it
// (factRuns.writtenBack). An issue whose key makes no claim token has no owner's slot.
func (e *Engine) mergeForwardWriteBack(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
	owner, err := record.OwnerClaim(issue)
	if err != nil {
		return nil
	}
	if runs, err := factRunsOf(ctx); err == nil {
		if runs.writtenBack[owner] {
			return nil
		}
		if runs.writtenBack == nil {
			runs.writtenBack = map[claim.Token]bool{}
		}
		runs.writtenBack[owner] = true
	}
	e.logOnCommit(ctx, "workflow: a merge forward asked of the owner ended, and its slot is written back to its own App", "issue", issue.Key,
		"tree", issue.Tree, "owner", owner)
	return e.enqueue(ctx, tx, issue.Key, record.SuperviseRequest{Op: "slot_credential", Tree: issue.Tree, Role: claim.RoleArchitect,
		Claim: owner, Generation: issue.Generation})
}
