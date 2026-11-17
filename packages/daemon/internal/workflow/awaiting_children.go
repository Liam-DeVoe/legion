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
// release of a hold (retryPhase) and the admission pass (StopForChildren) — what the move says,
// and the return the admission pass makes once no open child is left (ReturnFromChildren).

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
// implementing round after a wait on the children (withForwardMerge): the tester reads it and
// runs a full round of every check of the issue rather than the forward-merge re-check
// (dispatch://INFRA-3320 R12).
const childrenMergedLine = "children merged: full test round"

// childrenHold is one move of a parent into awaiting_children (Engine.awaitChildren): the start
// the move holds and what the move tells. Each caller fills it from what it has already read —
// the transition from the row it chose and the task it built, the release of a hold from the
// phase it restores, and the admission pass from the phase the parent stands in (StopForChildren)
// — so the one move writes the same record, the same notice and the same message whichever door
// it came through.
type childrenHold struct {
	// Start is the phase whose start the move holds, one of planning through accepting
	// (record.OpenChildHolds).
	Start phase.Phase
	// Task is the task that start gives its worker, which the return carries
	// (record.ChildrenWait.Task): the task a transition built, a retry's, the task a start
	// written and not yet delivered carries, or the task the stopped phase's entry was raised
	// with (record.PhaseRow.EntryTask). The return takes any forward-merge request out of it and
	// adds the request once while the merge is owed (ReturnFromChildren).
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

// awaitChildren moves issue into awaiting_children in place of the start hold names, and is the
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
// An issue already in awaiting_children is left as it stands and nothing is sent again. No door
// passes one — each moves an issue standing in planning through accepting, and the admission
// pass's next look at a parent that waits stops at StopForChildren's own phase check — so that
// test is a defensive one. It writes no count: a move back counted before the call counts what
// it counts today (recordPass, recordRound), and the move itself counts nothing more (R10).
//
// The admission pass (internal/admit) reaches it through StopForChildren, which moves a parent in
// accepting, one whose start is written and not delivered, and one whose worker is in the middle
// of a phase through this same function.
func (e *Engine) awaitChildren(ctx context.Context, tx pgx.Tx, issue record.Issue, hold childrenHold) error {
	if issue.Phase == phase.AwaitingChildren {
		return nil
	}
	if !record.OpenChildHolds(hold.Start) || hold.Task == "" || hold.Cause == "" || len(hold.Open) == 0 {
		return fmt.Errorf("a move of %s into awaiting_children holds a start of planning through accepting with its task and its cause, for an open child; this one holds %q with a task of %d bytes, cause %q and %d open children",
			issue.Key, hold.Start, len(hold.Task), hold.Cause, len(hold.Open))
	}
	from, now := issue.Phase, e.now()
	reason := childrenWaitReason(issue.Key, hold)
	status := PhaseStatus(phase.AwaitingChildren)
	if err := e.status(ctx, tx, issue, status, ""); err != nil {
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

// childrenHolding is the open Dispatch children of issue that hold its start of to (R9's guard),
// none where the rule does not reach that start. It reaches a start of planning through accepting
// of an issue standing in one of those phases (record.OpenChildHolds) whose pull request has not
// merged, so it leaves out the first phase (from admitted), a conflicting head's move back from
// retro or merging, and the sign-off into retro. The open children are read whatever the tree's
// design gate stands at (childrenOpen).
func (e *Engine) childrenHolding(ctx context.Context, tx pgx.Tx, issue record.Issue, to phase.Phase, pr *record.PullRequest) ([]record.OpenChild, error) {
	if !record.OpenChildHolds(issue.Phase) || !record.OpenChildHolds(to) || (pr != nil && pr.State == record.PullRequestMerged) {
		return nil, nil
	}
	return e.childrenOpen(ctx, tx, issue, nil)
}

// holdForChildren moves issue to awaiting_children in place of the start of to that trigger's row
// chose for it (awaitChildren). The hold keeps the task the start would have given its worker
// (heldTask). The notice it sends carries what the one it replaces (noticeFor) would have
// carried, and the role a conflicting head ended is told its phase ended (phaseEndedRole).
func (e *Engine) holdForChildren(ctx context.Context, tx pgx.Tx, issue record.Issue, trigger TriggerKind, to phase.Phase, handoff record.PhaseRow,
	pr *record.PullRequest, reason string, open []record.OpenChild,
) error {
	replaced := noticeFor(trigger, issue.Phase, to, handoff, reason)
	return e.awaitChildren(ctx, tx, issue, childrenHold{Start: to, Task: heldTask(issue, to, handoff, pr, reason),
		Cause: heldCause(trigger, issue.Phase, replaced, reason), Open: open,
		Summary: replaced.Summary, Verdict: replaced.Verdict, Ended: phaseEndedRole(trigger, issue.Phase)})
}

// heldTask is the task a start of to gives issue's worker, held while the parent waits on its
// children: built as transition builds one (task), but with no forward-merge request, which the
// return adds to the implementing task it starts while the merge is owed (ReturnFromChildren). The
// transition's guard (holdForChildren) and the release of a hold (retryPhase) both hold it.
func heldTask(issue record.Issue, to phase.Phase, handoff record.PhaseRow, pr *record.PullRequest, reason string) string {
	issue.Phase, issue.ForwardMergeOwed = to, false
	return task(issue, handoff, pr, reason)
}

// heldCause is what a start the transition's guard held was for (childrenHold.Cause): the reason
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
func childrenWaitReason(parent string, hold childrenHold) string {
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
// into awaiting_children (awaitChildren): the parent, the start it holds and what that start was
// for, each open child with its Dispatch status, each child whose own blocked_by link names an
// issue above it on its parent chain (ChildCycleReason), since that wait never ends by itself,
// and what ends the wait. It carries the summary and the verdict the phase-finished notice it
// replaces carried, and the phase the parent left. The architect's levers are the children: the
// notice names no move of a child under another started issue, which would make that issue wait
// too (dispatch://INFRA-3320 R11).
func awaitingChildrenNotice(parent string, from phase.Phase, hold childrenHold) record.Notice {
	reason := fmt.Sprintf("%s is in awaiting_children, holding the start of %s: %s. No phase of %s starts while Dispatch shows an open child under it: %s.",
		parent, hold.Start, hold.Cause, parent, openChildNames(hold.Open)) + childCycleSentences(hold.Open)
	reason += fmt.Sprintf(" The wait ends when no child under %[1]s is open: each one released and run until it leaves the workflow, closed, moved to icebox, taken out with park_child, or moved out from under %[1]s to an issue outside its tree or to no parent."+
		" Then %[1]s goes back to the start it held by itself, a held testing, reviewing or accepting as implementing, and unless its pull request merged meanwhile its first implementing round merges the default branch forward and runs a full test round."+
		" It holds no admission slot while it waits, and request_backward_move and sign_off on it are refused (%[2]s)."+
		" Close a child, move it to icebox or take it out with park_child "+parkRule+", and cite that reason in a comment on the child.",
		parent, waitingOnChildren)
	return record.Notice{Kind: AwaitingChildrenNoticeKind, Role: claim.RoleArchitect, Phase: from, Summary: hold.Summary, Verdict: hold.Verdict, Reason: reason}
}

// withForwardMerge is text, a task of issue's phase worker, ending with the request to merge the
// default branch into the branch before anything else, push, and complete with a new handoff
// record, however the children left, and to end the forward-merge comment with the line the
// tester reads as asking for a full round (forwardMergeText, childrenMergedLine), where issue
// stands in implementing and owes the forward merge of its children's work
// (record.Issue.ForwardMergeOwed); text as it is otherwise (dispatch://INFRA-3320 R12).
//
// The rule is that every task of an implementing phase whose issue owes the merge asks for it, so
// every place that builds such a task ends it here: task, which builds a transition's task and a
// retry's (retryPhase), the return from awaiting_children (ReturnFromChildren), and the resume task
// of a phase under way (ResumePhaseTask), which the silence repair, the boot's resume, the
// catch-up, a re-admitted tree's promotion and the send of an entry that names no task (entryWork)
// hand out. The pull request is not read: a merge clears the flag in the transaction that records
// it (Engine.merged), and every path into awaiting_children skips a merged pull request, so the
// flag alone says whether the merge is owed. Writing such a task settles nothing; the
// implementer's completion of that round does (settleForwardMerge).
func withForwardMerge(text string, issue record.Issue) string {
	if issue.Phase != phase.Implementing || !issue.ForwardMergeOwed {
		return text
	}
	return text + " " + forwardMergeText(issue)
}

// settleForwardMerge clears the forward merge issue owes (record.Issue.ForwardMergeOwed) where
// trigger is the implementer's completion of implementing (TriggerImplementationReady): the round
// the request asked to merge the default branch forward, push and complete with a new handoff
// record is over. Writing a task that carries the request settles nothing, since the round that
// task starts can be started again before it is done — a claim that fails before it takes its
// start and the architect's retry, a fresh session, a re-admitted tree's promotion — and each of
// those tasks asks for the merge again because the flag still stands (withForwardMerge). A
// merged pull request clears the flag as well (Engine.merged). The transition is the one caller,
// and a completion an open child holds moves the parent into a new wait before it gets here,
// which owes the merge again (awaitChildren).
func settleForwardMerge(issue *record.Issue, trigger TriggerKind) {
	if trigger == TriggerImplementationReady {
		issue.ForwardMergeOwed = false
	}
}

// forwardMergeText is the words of issue's forward-merge request whether or not the issue owes
// one: the one text withForwardMerge adds and the return takes out of a held task that already
// carries it (ReturnFromChildren), so a task carries the request once or not at all.
func forwardMergeText(issue record.Issue) string {
	base := "the default branch"
	if issue.DefaultBranch != "" {
		base += " " + issue.DefaultBranch
	}
	return fmt.Sprintf("%s waited on its children: before anything else, merge %s into the branch, push, and complete with a new handoff record, however the children left; end your forward-merge comment with the line `%s`.",
		issue.Key, base, childrenMergedLine)
}

// StopForChildren is the admission pass's move of a started parent that an open Dispatch child
// now holds (dispatch://INFRA-3320 R9), through the one move into awaiting_children
// (awaitChildren), and answers whether it moved the parent. open is the parent's open children as
// the pass judged them (record.LineFacts.StartedParentWaits), and owed the starts the parent owes
// its workers that no lane holds (its own rows of record.LineFacts.OwedRows). The pass meets three
// cases no transition raises:
//
//   - a parent in accepting, where no worker runs: it holds accepting, with the task that start
//     is given (task, over the reviewer's handoff);
//   - a parent whose start is written and not yet delivered — a start of the phase it stands in,
//     owed its worker at its own generation: it holds that start with the task its queued row
//     carries (queuedTask), and no role is told, since none has been given that phase;
//   - a parent whose worker is in the middle of its phase: it holds that phase with the task its
//     worker was given, the work the phase entry was raised with (entryWork, as SendOwedEntry
//     sends it), and that worker is told on its own topic that its phase ended
//     (childrenHold.Ended, record.PhaseEnded), which wakes a pod the idle rule released.
//
// Every start the parent owes is finished in the same transaction (Store.FinishOutbox), so none
// delivers later: the wait keeps its start on the record (record.ChildrenWait), and the return
// writes a start of its own from the moment the wait ended (ReturnFromChildren). The phase stops
// at this pass, and the slot it held is the pass's own release to free, since awaiting_children
// keeps none (record.PhaseKeepsSlot).
//
// A parent standing anywhere else is left as it stands, by the phase check that opens this
// function (record.OpenChildHolds): held, whose release is a start the guard in retryPhase reads,
// retro, merging, admitted, and awaiting_children itself, so that a second pass sends nothing
// again. So is a parent whose pull request has merged, whose worker R9 leaves unmessaged.
//
// So is a parent whose phase's role stands on an entry a move out of retro or merging raised
// (record.PhaseRow.EntryFrom, acceptedEntry): the move a head that conflicts with its base makes
// back to implementing after the daemon accepted the issue. R9 holds only the starts each from a
// phase before retro, and the transition's guard leaves that move's start alone
// (childrenHolding), so the pass leaves it too, whether that start is still written and not
// delivered or its worker is in the middle of the phase it gave. The exemption is that entry's
// alone: the parent's next start, out of implementing, is a start from a phase before retro, and
// the transition's guard holds it while a child is open.
//
// The worker the move stops has the task its claim holds for the stopped phase retired, in this
// transaction (a `retire_task` row, record.SuperviseRequest, which the outbox runs through
// supervise.Machine.RetireStoppedTask), bounded by the run and by the entry the phase stood on,
// so the return's own start, on an entry of its own, is never taken. While the parent waits,
// every send of a pending delivery drops such a task already (supervise's PhaseHolds), but the
// return puts the issue back in that same phase at the same run, where a task held back all the
// wait would pass that question again and reach the worker before the return's start has its
// slot.
func (e *Engine) StopForChildren(ctx context.Context, tx pgx.Tx, issue record.Issue, open []record.OpenChild, owed []record.OwedStart) (bool, error) {
	if !record.OpenChildHolds(issue.Phase) || len(open) == 0 {
		return false, nil
	}
	pr, err := e.store.PullRequest(ctx, tx, issue.Key)
	if err != nil || (pr != nil && pr.State == record.PullRequestMerged) {
		return false, err
	}
	role := RoleFor(issue.Phase)
	entry, err := e.phaseRow(ctx, tx, issue.Key, role)
	if err != nil || acceptedEntry(entry) {
		return false, err
	}
	hold := childrenHold{Start: issue.Phase, Open: open}
	switch due, owes := undeliveredStart(issue, owed); {
	case issue.Phase == phase.Accepting:
		review, err := e.phaseRow(ctx, tx, issue.Key, claim.RoleReviewer)
		if err != nil {
			return false, err
		}
		hold.Task, hold.Cause = task(issue, review, pr, ""), "its reviewed head waited in accepting for the architect's sign-off"
	case owes:
		if hold.Task, err = e.queuedTask(ctx, tx, issue, due, entry); err != nil {
			return false, err
		}
		hold.Cause = fmt.Sprintf("its start of %s was written and had not reached the %s", issue.Phase, role)
	default:
		hold.Task = entryWork(issue, entry)
		hold.Cause, hold.Ended = fmt.Sprintf("the admission pass stopped the %s's %s phase", role, issue.Phase), role
		if err := e.enqueue(ctx, tx, issue.Key, record.SuperviseRequest{Op: "retire_task", Tree: issue.Tree, Role: role,
			Generation: issue.Generation, Phase: issue.Phase, Entry: entry.Entry}); err != nil {
			return false, err
		}
	}
	for _, start := range owed {
		if err := e.store.FinishOutbox(ctx, tx, start.ID, start.LeaseToken); err != nil {
			return false, fmt.Errorf("finish the start of %s's %s before its wait on its children: %w", issue.Key, start.Role, err)
		}
	}
	return true, e.awaitChildren(ctx, tx, issue, hold)
}

// undeliveredStart is the start of the phase issue stands in that it owes that phase's worker at
// its own generation, among owed: the start written and not yet delivered that StopForChildren
// holds.
func undeliveredStart(issue record.Issue, owed []record.OwedStart) (record.OwedStart, bool) {
	for _, start := range owed {
		if start.Phase == issue.Phase && start.Generation == issue.Generation && start.Role == RoleFor(issue.Phase) {
			return start, true
		}
	}
	return record.OwedStart{}, false
}

// acceptedEntry says whether row's entry was raised by a move out of retro or merging, the phases
// past the daemon's acceptance of the issue (record.PhaseRow.EntryFrom): the move a head that
// conflicts with its base makes back to implementing, whose start R9 does not hold. It is the one
// statement of that exemption, read by the admission pass (StopForChildren) and by the release of a
// hold, which restarts the held phase on that same entry (retryPhase).
func acceptedEntry(row record.PhaseRow) bool {
	return record.AcceptedPhase(row.EntryFrom)
}

// queuedTask is the task the start row due carries for issue, read from its role's queued
// supervise rows (Store.RoleRun): the very task that start would have delivered, which for the
// retry of a held phase is the retry's own and not its entry's. A row no longer queued — its lane
// finished it after the pass read it — leaves the work of entry, the role's phases row the caller
// read (entryWork).
func (e *Engine) queuedTask(ctx context.Context, tx pgx.Tx, issue record.Issue, due record.OwedStart, entry record.PhaseRow) (string, error) {
	project, err := claim.ProjectToken(issue.Project)
	if err != nil {
		return "", err
	}
	token, err := claim.NewToken(project, issue.Key, due.Role)
	if err != nil {
		return "", err
	}
	run, err := e.store.RoleRun(ctx, tx, token, issue.Key, due.Role, due.Generation)
	if err != nil {
		return "", err
	}
	for _, queued := range run.Queued {
		if queued.ID == due.ID && queued.Request.Task != "" {
			return queued.Request.Task, nil
		}
	}
	return entryWork(issue, entry), nil
}

// ReturnFromChildren takes issue back from awaiting_children to the start it held, once no open
// Dispatch child stands under it (dispatch://INFRA-3320 R12): the admission pass calls it on the
// pass that finds the last open child gone (admit's childrenWaits), with nobody acting.
//
//   - A held start of planning or implementing starts that phase, with the task the start held,
//     which carries the reason its transition wrote. A held start of testing, reviewing or
//     accepting starts implementing in its place, since a test or a review of a head without the
//     children's merges proves nothing, with a task that names the held start and its reason.
//   - While the forward merge is owed, the implementing task ends with the request to merge the
//     default branch forward (withForwardMerge), once, whatever the held task carried. The record
//     owes the merge until the implementer completes that round (settleForwardMerge), so a task
//     that starts the round again asks for it too.
//   - A parent whose pull request merged during the wait goes back to the very start it held,
//     with no forward merge (the merge cleared the debt, Engine.merged), and keeps today's path
//     for a merged pull request from there. A held accepting goes back to accepting, where no
//     worker starts: its architect learns of an issue entering accepting from the notice of the
//     move that took it there, which the wait's own notice replaced, so the return tells it once,
//     under the wait's kind (AwaitingChildrenNoticeKind), that the issue waits on its sign_off.
//   - The start raises a new phase entry (enter, through start), not a resend of the entry the
//     wait stopped (restart), and marks it as a move back's out of awaiting_children
//     (record.PhaseRow.EntryMovedBack, record.PhaseRow.EntryFrom): the
//     return asks for the phase's work to be done again on a head that is to carry the children's
//     merges, so a completion of an entry from before the wait is refused whatever head it names,
//     as leftItsPhase refuses one of a phase the issue has left. The starting role's handoff is
//     cleared, as a transition clears it (clearHandoff).
//   - The held start goes with the phase (record.Issue.ChildrenWait), and the moment of the last
//     wait stays (record.Issue.ChildrenWaitedAt), which leftItsPhase reads after the return.
//
// It counts nothing — no pass, no pair, no round — and does not wait for the design gate, which
// holds no phase already under way. The start goes through the one writer of a phase worker's
// start (start, StartOrOwe), and the parent holds no admission slot, since awaiting_children
// keeps none: a start of planning through reviewing therefore waits in the line for one, among the
// issues whose work came back, from the moment this return wrote it (record.Waiting, OwedStart),
// or there as a waiting issue while a blocked_by link still holds the parent
// (record.LineWaitingOnIssue), ordered by that same moment.
func (e *Engine) ReturnFromChildren(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
	if issue.Phase != phase.AwaitingChildren || issue.ChildrenWait == nil {
		return nil
	}
	held := *issue.ChildrenWait
	pr, err := e.store.PullRequest(ctx, tx, issue.Key)
	if err != nil {
		return err
	}
	back := issue
	back.Phase, back.Hold, back.ChildrenWait = held.Start, nil, nil
	merged := pr != nil && pr.State == record.PullRequestMerged
	if !merged && held.Start != phase.Planning && held.Start != phase.Implementing {
		back.Phase = phase.Implementing
	}
	// The work the start hands its role: the held task where the start goes back as it was held,
	// with any forward-merge request it carried taken out so that the one rule below adds it,
	// and otherwise an implementing task of its own that names the start it replaces (task, which
	// adds the request itself).
	work := strings.ReplaceAll(held.Task, " "+forwardMergeText(back), "")
	if back.Phase != held.Start {
		work = task(back, record.PhaseRow{}, pr, fmt.Sprintf("%s goes back from awaiting_children now that no Dispatch child of it is open, and starts implementing in place of the start of %s it held, since a test or a review of a head without its children's merges proves nothing. The held start of %s was for this: %s",
			issue.Key, held.Start, held.Start, held.Reason))
	} else {
		work = withForwardMerge(work, back)
	}
	status := PhaseStatus(back.Phase)
	if err := e.status(ctx, tx, issue, status, ""); err != nil {
		return err
	}
	if status != "" {
		back.Status = status
	}
	if err := e.store.PutIssue(ctx, tx, back); err != nil {
		return err
	}
	if err := e.store.MarkIssueMoved(ctx, tx, issue.Key, e.now()); err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: a parent's open children are finished, so it goes back to the start it held", "issue", issue.Key,
		"tree", issue.Tree, "held", held.Start, "to", back.Phase, "merged", merged)
	role := RoleFor(back.Phase)
	if err := e.clearHandoff(ctx, tx, issue.Key, role); err != nil {
		return err
	}
	if role != "" && role != claim.RoleArchitect {
		return e.start(ctx, tx, back, role, work, phase.AwaitingChildren)
	}
	return e.notice(ctx, tx, issue.Key, record.Notice{Kind: AwaitingChildrenNoticeKind, Role: claim.RoleArchitect, Phase: phase.AwaitingChildren,
		Reason: fmt.Sprintf("%s left awaiting_children: no Dispatch child of it is open now, and its pull request merged during the wait, so it is back in %s with no forward merge. It waits on your sign_off, as an issue in %s does.",
			issue.Key, back.Phase, back.Phase)})
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
		"Act on the children: release one that should run, or move one out from under %[1]s to an issue outside its tree or to no parent; close one, move it to icebox or take it out with park_child "+parkRule+", and cite that reason in a comment on the child",
		issue.Key, openChildNames(open), act, held)), nil
}
