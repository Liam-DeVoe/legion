package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A parent starts no phase while a Dispatch child of it is open (dispatch://INFRA-3320 R9). In
// place of a start of planning, implementing, testing, reviewing or accepting, the daemon moves
// the parent to awaiting_children, where it holds no admission slot and no role of it works, and
// keeps the start it held for the moment no open child is left (record.Issue.ChildrenWait). This
// file holds the one move every door takes — the transition's guard (Engine.transition), the
// release of a hold (retryPhase) and the admission pass — and what the move says.

// AwaitingChildrenNoticeKind is the kind of the notice a parent's architect is told when the
// parent enters awaiting_children (awaitingChildrenNotice). It replaces the phase-finished or
// checks-red notice of the start the move held, since each of those tells the architect that a
// phase started (dispatch://INFRA-3320 R10).
const AwaitingChildrenNoticeKind = record.NoticeKind("awaiting-children")

// waitingOnChildren is the refusal code of request_backward_move and sign_off on a parent in
// awaiting_children (waitingRefusal): each acts on a phase, and the parent stands in none
// (dispatch://INFRA-3320 R11).
const waitingOnChildren = "WAITING_ON_CHILDREN"

// childrenMergedLine is the line an implementer ends its forward-merge comment with in the first
// implementing round after a wait on the children (ForwardMergeRequest): the tester reads it and
// runs a full round of every check of the issue rather than the forward-merge re-check
// (dispatch://INFRA-3320 R12).
const childrenMergedLine = "children merged: full test round"

// ChildrenHold is one move of a parent into awaiting_children (Engine.AwaitChildren): the start
// the move holds and what the move tells. Each caller fills it from what it has already read —
// the transition from the row it chose and the task it built, the release of a hold from the
// phase it restores, and the admission pass from the phase a worker is in — so the one move writes
// the same record, the same notice and the same message whichever door it came through.
type ChildrenHold struct {
	// Start is the phase whose start the move holds, one of planning through accepting
	// (holdsForChildren).
	Start phase.Phase
	// Task is the task that start gives its worker, which the return carries
	// (record.ChildrenWait.Task): the task a transition built, a retry's, or the task the
	// stopped phase's entry was raised with (record.PhaseRow.EntryTask). It carries no
	// forward-merge request, which the return adds while the merge is owed (ForwardMergeRequest).
	Task string
	// Cause is what the start was for: the reason the notice the move replaces carried — a
	// worker's own move back with its words, or the base a conflicting head merges forward — or
	// else the completion, review, retry or stop that raised it (heldCause). The move adds the
	// open children to it for the held start's reason (childrenWaitReason).
	Cause string
	// Open is each open Dispatch child of the parent, with the status that holds it and the issue
	// above it a blocked_by link of its own names (record.OpenChild, Engine.childrenOpen). A move
	// names at least one.
	Open []record.OpenChild
	// Summary and Verdict are the worker's summary and the tester's verdict the replaced
	// phase-finished notice carried, and empty where it carried none.
	Summary, Verdict string
	// Ended is the role whose phase the move ends and that is told so on its own topic
	// (record.PhaseEnded), and "" where no role is: the role of a phase a conflicting head ended
	// (phaseEndedRole), or the worker the admission pass stops in the middle of its phase.
	Ended claim.Role
}

// AwaitChildren moves issue into awaiting_children in place of the start hold names, and is the
// one move into that phase: the transition's guard, the release of a hold and the admission pass
// all write it here. In one transaction it
//
//   - writes in_progress where the record shows another status (Engine.status): the parent's
//     work is under way in its children (dispatch://INFRA-3320 R11);
//   - writes the phase with the held start — its phase, its task, its reason naming the open
//     children, and the moment the wait began (record.ChildrenWait) — the forward merge owed
//     (record.Issue.ForwardMergeOwed) and the moment of the move, which outlives the phase
//     (record.Issue.ChildrenWaitedAt, leftItsPhase);
//   - marks the issue moved, since this is a phase write (the rule in dispatchIssue);
//   - cancels every open backstop of the parent's pod, since no role of it has a phase for one to
//     wake it into (R11), where the engine holds the pod tables (Config.Pods);
//   - tells the parent's architect once (awaitingChildrenNotice), in place of the phase-finished or
//     checks-red notice the start would have sent (R10);
//   - and tells the role whose phase the move ends, where there is one, that its phase ended
//     and no task of it follows (record.PhaseEnded into awaiting_children).
//
// An issue already in awaiting_children is left as it stands and nothing is sent again, so a
// second call for one parent — the admission pass reads every parent on every pass — writes
// nothing. It writes no count: a move back counted before the call counts what it counts today
// (recordPass, recordRound), and the move itself counts nothing more (R10).
//
// It is exported for the admission pass (internal/admit), which holds the engine and moves a
// parent in accepting, one whose start waits for a slot, and one whose worker is in the middle of
// a phase through this same function.
func (e *Engine) AwaitChildren(ctx context.Context, tx pgx.Tx, issue record.Issue, hold ChildrenHold) error {
	if issue.Phase == phase.AwaitingChildren {
		return nil
	}
	if !holdsForChildren(hold.Start) || hold.Task == "" || hold.Cause == "" || len(hold.Open) == 0 {
		return fmt.Errorf("a move of %s into awaiting_children holds a start of planning through accepting with its task and its cause, for an open child; this one holds %q with a task of %d bytes, cause %q and %d open children",
			issue.Key, hold.Start, len(hold.Task), hold.Cause, len(hold.Open))
	}
	from, now := issue.Phase, e.now()
	reason := childrenWaitReason(issue.Key, hold)
	status := PhaseStatus(phase.AwaitingChildren)
	if err := e.status(ctx, tx, issue, status); err != nil {
		return err
	}
	issue.Phase, issue.Hold, issue.Status = phase.AwaitingChildren, nil, status
	issue.ChildrenWait = &record.ChildrenWait{Start: hold.Start, Task: hold.Task, Reason: reason, Since: now}
	issue.ForwardMergeOwed, issue.ChildrenWaitedAt = true, &now
	if err := e.store.PutIssue(ctx, tx, issue); err != nil {
		return err
	}
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, now); err != nil {
		return err
	}
	if e.cfg.Pods != nil {
		if err := e.cfg.Pods.CancelPodBackstops(ctx, tx, issue.Key); err != nil {
			return err
		}
	}
	e.logOnCommit(ctx, "workflow: a parent waits on its open children", "issue", issue.Key, "tree", issue.Tree,
		"from", from, "holds", hold.Start, "children", openChildNames(hold.Open))
	if err := e.notice(ctx, tx, issue.Key, awaitingChildrenNotice(issue.Key, from, hold)); err != nil {
		return err
	}
	if hold.Ended == "" {
		return nil
	}
	return e.enqueue(ctx, tx, issue.Key, record.PhaseEnded{Role: hold.Ended, From: from, To: phase.AwaitingChildren, Reason: reason})
}

// holdsForChildren says whether p is a phase whose start an open Dispatch child holds
// (dispatch://INFRA-3320 R9): planning through accepting. The issue's first phase is not among the
// starts it reaches — that start is the admission line's (children-first, StartFirstPhase) — nor
// retro and merging, which start once the daemon has accepted the issue, after its children.
func holdsForChildren(p phase.Phase) bool {
	switch p {
	case phase.Planning, phase.Implementing, phase.Testing, phase.Reviewing, phase.Accepting:
		return true
	}
	return false
}

// childrenHolding is the open Dispatch children of issue that hold its start of to (R9's guard),
// none where the rule does not reach that start. It reaches a start of planning through accepting
// of an issue standing in one of those phases (holdsForChildren) whose pull request has not
// merged, so it leaves out the first phase (from admitted), a conflicting head's move back from
// retro or merging, and the sign-off into retro. The open children are read whatever the tree's
// design gate stands at (childrenOpen).
func (e *Engine) childrenHolding(ctx context.Context, tx pgx.Tx, issue record.Issue, to phase.Phase, pr *record.PullRequest) ([]record.OpenChild, error) {
	if !holdsForChildren(issue.Phase) || !holdsForChildren(to) || (pr != nil && pr.State == record.PullRequestMerged) {
		return nil, nil
	}
	return e.childrenOpen(ctx, tx, issue, nil)
}

// holdForChildren moves issue to awaiting_children in place of the start of to that trigger's row
// chose for it (AwaitChildren). The hold keeps the task the start would have given its worker,
// built as transition builds it but with no forward-merge request, which the return adds to the
// implementing task it starts while the merge is owed. The notice it sends carries what the one
// it replaces (noticeFor) would have carried, and the role a conflicting head ended is told its
// phase ended (phaseEndedRole).
func (e *Engine) holdForChildren(ctx context.Context, tx pgx.Tx, issue record.Issue, trigger TriggerKind, to phase.Phase, handoff record.PhaseRow,
	pr *record.PullRequest, reason string, open []record.OpenChild,
) error {
	replaced := noticeFor(trigger, issue.Phase, to, handoff, reason)
	held := issue
	held.Phase, held.ForwardMergeOwed = to, false
	return e.AwaitChildren(ctx, tx, issue, ChildrenHold{Start: to, Task: task(held, handoff, pr, reason),
		Cause: heldCause(trigger, issue.Phase, replaced, reason), Open: open,
		Summary: replaced.Summary, Verdict: replaced.Verdict, Ended: phaseEndedRole(trigger, issue.Phase)})
}

// heldCause is what a start the transition's guard held was for (ChildrenHold.Cause): the reason
// the notice the move replaces carried — a worker's own move back, with the phase it asked for,
// the role and its words, or the base a conflicting head merges forward (noticeFor) — and
// otherwise the completion or the review that raised the start, with the review's own words where
// it asked for changes.
func heldCause(trigger TriggerKind, from phase.Phase, replaced record.Notice, reason string) string {
	switch {
	case replaced.Reason != "":
		return replaced.Reason
	case trigger == TriggerReviewApproved:
		return "a review approved the head"
	case trigger == TriggerReviewRejected && reason != "":
		return "a review asked for changes: " + reason
	case trigger == TriggerReviewRejected:
		return "a review asked for changes"
	}
	return fmt.Sprintf("the %s completed %s", RoleFor(from), from)
}

// childrenWaitReason is the reason the held start keeps (record.ChildrenWait.Reason) and the
// phase-ended message carries: what the start was for, and the open children that held it.
func childrenWaitReason(parent string, hold ChildrenHold) string {
	return fmt.Sprintf("%s, while these Dispatch children of %s were open: %s", hold.Cause, parent, openChildNames(hold.Open))
}

// openChildNames names each open child with the status Dispatch shows it in, as the sign-off's
// refusal and the children-first notice name them: "LEGION-209 (todo), LEGION-210 (backlog)".
func openChildNames(open []record.OpenChild) string {
	named := make([]string, len(open))
	for i, child := range open {
		named[i] = fmt.Sprintf("%s (%s)", child.Key, child.Status)
	}
	return strings.Join(named, ", ")
}

// awaitingChildrenNotice is what the parent's architect is told when the parent moves out of from
// into awaiting_children (AwaitChildren): the parent, the start it holds and what that start was
// for, each open child with its Dispatch status, each child whose own blocked_by link names an
// issue above it on its parent chain (ChildCycleReason), since that wait never ends by itself,
// and what ends the wait. It carries the summary and the verdict the phase-finished notice it
// replaces carried, and the phase the parent left. The architect's levers are the children: the
// notice names no move of a child under another started issue, which would make that issue wait
// too (dispatch://INFRA-3320 R11).
func awaitingChildrenNotice(parent string, from phase.Phase, hold ChildrenHold) record.Notice {
	reason := fmt.Sprintf("%s is in awaiting_children, holding the start of %s: %s. No phase of %s starts while Dispatch shows an open child under it: %s.",
		parent, hold.Start, hold.Cause, parent, openChildNames(hold.Open))
	for _, child := range hold.Open {
		if child.Cycle != "" {
			reason += " " + ChildCycleReason(child.Key, child.Cycle) + "."
		}
	}
	reason += fmt.Sprintf(" The wait ends when no child under %[1]s is open: each one released and run until it leaves the workflow, closed, moved to icebox, taken out with park_child, or moved out from under %[1]s to an issue outside its tree or to no parent."+
		" Then %[1]s goes back to the start it held by itself, a held testing, reviewing or accepting as implementing, and unless its pull request merged meanwhile its first implementing round merges the default branch forward and runs a full test round."+
		" It holds no admission slot while it waits, and request_backward_move and sign_off on it are refused (%[2]s)."+
		" Close, icebox or park a child "+parkRule+", and cite that reason in a comment on the child.",
		parent, waitingOnChildren)
	return record.Notice{Kind: AwaitingChildrenNoticeKind, Role: claim.RoleArchitect, Phase: from, Summary: hold.Summary, Verdict: hold.Verdict, Reason: reason}
}

// ForwardMergeRequest is the request an implementing task of issue carries while the parent owes
// the forward merge of its children's work (record.Issue.ForwardMergeOwed) and its pull request
// has not merged, and "" for every other task (dispatch://INFRA-3320 R12): merge the default
// branch into the branch before anything else, push, and complete with a new handoff record,
// however the children left, and end the forward-merge comment with the line the tester reads as
// asking for a full round (childrenMergedLine). task adds it, and the write that puts the issue in
// the phase that task is for clears the flag (transition, retryPhase). It is exported for the
// admission pass, whose return to a held start builds the implementing task from the task the
// start held.
func ForwardMergeRequest(issue record.Issue, pr *record.PullRequest) string {
	if issue.Phase != phase.Implementing || !issue.ForwardMergeOwed || (pr != nil && pr.State == record.PullRequestMerged) {
		return ""
	}
	base := "the default branch"
	if issue.DefaultBranch != "" {
		base += " " + issue.DefaultBranch
	}
	return fmt.Sprintf("%s waited on its children: before anything else, merge %s into the branch, push, and complete with a new handoff record, however the children left; end your forward-merge comment with the line `%s`.",
		issue.Key, base, childrenMergedLine)
}

// waitingRefusal refuses act — request_backward_move or sign_off — on issue, which stands in
// awaiting_children (WAITING_ON_CHILDREN, dispatch://INFRA-3320 R11): each acts on a phase, and
// the parent stands in none. The reason names the open children as the daemon reads them now,
// the start the parent holds, and the architect's levers, which are the children. A parent whose
// last open child has gone since the last admission pass is told that the next pass brings it
// back.
func (e *Engine) waitingRefusal(ctx context.Context, tx pgx.Tx, issue record.Issue, act string) (intake.Result, error) {
	open, err := e.childrenOpen(ctx, tx, issue, nil)
	if err != nil {
		return intake.Result{}, err
	}
	held := phase.Phase("")
	if issue.ChildrenWait != nil {
		held = issue.ChildrenWait.Start
	}
	if len(open) == 0 {
		return refused(waitingOnChildren, fmt.Sprintf("%s waits in awaiting_children, holding the start of %s, and no Dispatch child of it is open now: the next admission pass takes it back to that start by itself. It stands in no phase that %s acts on, so this %s changed nothing",
			issue.Key, held, act, act)), nil
	}
	return refused(waitingOnChildren, fmt.Sprintf("%[1]s waits in awaiting_children on these open Dispatch children of it: %[2]s. It stands in no phase that %[3]s acts on, so this %[3]s changed nothing; %[1]s goes back to the start it holds, %[4]s, by itself once no child under it is open. "+
		"Act on the children: release one that should run, or move one out from under %[1]s to an issue outside its tree or to no parent; close, icebox or park one "+parkRule+", and cite that reason in a comment on the child",
		issue.Key, openChildNames(open), act, held)), nil
}
