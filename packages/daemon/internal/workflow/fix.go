package workflow

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A fix is the first implementation step of a task started again beside the task's other steps,
// because a check that a CI job runs failed at the task's head (record.TaskFix). The fix rule
// (fixRule) is the one place that decides what a failure of such a check does: it runs at the
// tail of every fact that names an issue (apply), over the check states the daemon holds
// (checkState), so a failure is judged once, in the transaction of the fact that made it known.

// taskHead is where the task of pr's issue stands: the repository and the head of its open pull
// request, which the task's checks are read at, and "" where the issue has no open pull request.
func taskHead(pr *record.PullRequest) (repo, head string) {
	if pr == nil || pr.State != record.PullRequestOpen {
		return "", ""
	}
	return pr.Repo, pr.HeadSHA
}

// failedChecks is each check of head's set that fails there by the rule of a CI check
// (ciVerdict's ciFailed), in the set's order, with its state. A set not held fails nothing.
func failedChecks(head record.HeadStates) []record.CheckState {
	var failed []record.CheckState
	for _, check := range head.Checks {
		state, held := head.States[check]
		if ciVerdict(state, held) == ciFailed {
			failed = append(failed, state)
		}
	}
	return failed
}

// firstImplementation is the place of the first step of w's task whose recorded type is
// implementation (record.PlanStep.Type), which a fix runs again; false for a task with none.
func firstImplementation(w Walk) (int, bool) {
	for i, step := range w.Steps {
		if step.Type == plan.TypeImplementation {
			return i + 1, true
		}
	}
	return 0, false
}

// taskRuns says whether the task of w's run stands running: a task of a stored walk in
// record.TaskRunning, and the task of a run with no plan rows while its issue is in the workflow.
func taskRuns(issue record.Issue, w Walk) bool {
	if w.Stored {
		return w.State == record.TaskRunning
	}
	return InWorkflow(issue)
}

// atTaskEnd says whether issue stands at its task's end: held before its retro for its fix or
// for its CI.
func atTaskEnd(issue record.Issue) bool {
	return record.HeldFor(issue, record.HoldForFix) || record.HeldFor(issue, record.HoldForCI)
}

// fixPass is what one run of the fix rule read of an issue: its record, its walk, its task's
// head, the task's open fix (nil for none) and whether its run takes a fix.
type fixPass struct {
	issue   record.Issue
	w       Walk
	head    string
	open    *record.TaskFix
	fixesCI bool
}

// fixRule is the one place a failure of a check that a CI job runs at the task's head is decided,
// applied at the tail of every fact that names the issue key. It returns where the task does not
// run (taskRuns) or has no head (taskHead), and otherwise takes each check that fails at the
// head (failedChecks), in the set's order, to the first of these that applies, by the check's
// failure row at that head (record.CIFailure), which says whom the failure went to:
//
//   - a fix of the task is open (reading its job or running), wherever the task stands: the
//     failure joins that fix, and the implementer is told it is part of its fix where the fix
//     runs (a fix still reading its job names it in its task). A failure some fix took already
//     needs nothing. No second fix starts while one is open.
//   - the task stands at its retro or its merge: the failure goes to the merger, which reads
//     GitHub, and nothing else is done.
//   - before the task's end, for a task that takes a fix (a stored walk, record.PlanTask.FixesCI),
//     a failure no one was given yet: where the task's implementation step runs, it goes to that
//     step, whose implementer is told; where a step after the task's first implementation step
//     runs or waits, a fix starts (openFix); before the first implementation step nothing is
//     written, and that step's start names the failure.
//
// A task that takes no fix gets nothing of this rule before its end, and its CI settlements are
// told as before (tellChecks).
func (e *Engine) fixRule(ctx context.Context, tx pgx.Tx, key string) error {
	issue, err := e.store.Issue(ctx, tx, key)
	if err != nil || issue == nil {
		return err
	}
	pr, err := e.store.PullRequest(ctx, tx, key)
	if err != nil {
		return err
	}
	repo, head := taskHead(pr)
	if head == "" {
		return nil
	}
	held, err := e.store.HeadStates(ctx, tx, key, repo, head)
	if err != nil {
		return err
	}
	failed := failedChecks(held)
	if len(failed) == 0 {
		return nil
	}
	w, err := e.walk(ctx, tx, *issue)
	if err != nil || !taskRuns(*issue, w) {
		return err
	}
	pass := &fixPass{issue: *issue, w: w, head: head}
	if w.Stored {
		if pass.open, err = e.store.RunningFix(ctx, tx, key, issue.Generation, w.Task); err != nil {
			return err
		}
		if pass.fixesCI, err = e.store.RunFixesCI(ctx, tx, key, issue.Generation); err != nil {
			return err
		}
	}
	for _, state := range failed {
		if err := e.fixFailure(ctx, tx, pass, state); err != nil {
			return err
		}
	}
	return nil
}

// fixFailure takes the failure of state's check at the pass's head to the first branch of the
// fix rule that applies (fixRule).
func (e *Engine) fixFailure(ctx context.Context, tx pgx.Tx, pass *fixPass, state record.CheckState) error {
	issue, w := pass.issue, pass.w
	row, err := e.store.CIFailureAt(ctx, tx, issue.Key, issue.Generation, w.Task, state.Check, pass.head)
	if err != nil {
		return err
	}
	failure := record.CIFailure{Issue: issue.Key, Generation: issue.Generation, Task: w.Task, Check: state.Check, Head: pass.head, At: e.now()}
	switch {
	case pass.open != nil:
		return e.joinFix(ctx, tx, pass, state, row, failure)
	case record.StandsIn(issue, phase.Retro, phase.Merging) && !atTaskEnd(issue):
		if row != nil {
			return nil
		}
		failure.WentTo = record.CIFailureToMerger
		_, err := e.store.PutCIFailure(ctx, tx, failure)
		return err
	case atTaskEnd(issue) || !w.Stored || !pass.fixesCI || row != nil:
		return nil
	}
	first, hasImplementation := firstImplementation(w)
	switch {
	case w.Place.Kind == record.PlaceStep && w.Place.Position >= 1 && w.Place.Position <= len(w.Steps) &&
		w.Steps[w.Place.Position-1].Type == plan.TypeImplementation:
		runs, err := e.stepRuns(ctx, tx, issue.Key, claim.Role(w.Steps[w.Place.Position-1].Role))
		if err != nil || !runs {
			return err
		}
		failure.WentTo = record.CIFailureToImplementer
		if _, err := e.store.PutCIFailure(ctx, tx, failure); err != nil {
			return err
		}
		return e.tellImplementer(ctx, tx, issue, ciFailureSentence(state, pass.head)+" Fix it in this step.")
	case hasImplementation && w.Place.Kind == record.PlaceStep && w.Place.Position > first:
		return e.openFix(ctx, tx, pass, state.Check, first, failure)
	}
	return nil
}

// stepRuns says whether role's phases row of issue holds an open entry: one raised that its
// completion has not ended.
func (e *Engine) stepRuns(ctx context.Context, tx pgx.Tx, issue string, role claim.Role) (bool, error) {
	row, err := e.phaseRow(ctx, tx, issue, role)
	if err != nil {
		return false, err
	}
	return row.Entry > 0 && !row.Completed(), nil
}

// joinFix is the fix rule's branch of an open fix of the task, pass.open: a failure that no fix
// took (no row, or a row of the implementer, the merger or the owner) joins it, and the
// implementer is told where the fix runs. A failure some fix took already needs nothing.
func (e *Engine) joinFix(ctx context.Context, tx pgx.Tx, pass *fixPass, state record.CheckState, row *record.CIFailure, failure record.CIFailure) error {
	fix := pass.open
	switch {
	case row != nil && row.Fix > 0:
		return nil
	case row == nil:
		failure.WentTo, failure.Fix = record.CIFailureToFix, fix.Fix
		if _, err := e.store.PutCIFailure(ctx, tx, failure); err != nil {
			return err
		}
	default:
		if err := e.store.MarkCIFailureFix(ctx, tx, failure.Issue, failure.Generation, failure.Task, failure.Check, failure.Head, fix.Fix); err != nil {
			return err
		}
	}
	if fix.State != record.FixRunning {
		return nil
	}
	return e.tellImplementer(ctx, tx, pass.issue, ciFailureSentence(state, pass.head)+" It is part of your fix.")
}

// openFix starts fix of the task of the pass for check at the pass's head, at place, the task's
// first implementation step: the fix reads its failed job first (record.FixReading), the failure
// names it, and one read of the job is queued (record.FixJobRead), whose answer starts the fix's
// step. Each other check that fails at the head in the same pass joins it (joinFix), since the
// pass now holds it open.
func (e *Engine) openFix(ctx context.Context, tx pgx.Tx, pass *fixPass, check string, place int, failure record.CIFailure) error {
	issue, w := pass.issue, pass.w
	latest, err := e.store.LatestFix(ctx, tx, issue.Key, issue.Generation, w.Task)
	if err != nil {
		return err
	}
	number := 1
	if latest != nil {
		number = latest.Fix + 1
	}
	fix := record.TaskFix{Issue: issue.Key, Generation: issue.Generation, Task: w.Task, Fix: number, State: record.FixReading,
		Check: check, Head: pass.head, Place: place}
	if err := e.store.PutFix(ctx, tx, fix); err != nil {
		return err
	}
	failure.WentTo, failure.Fix = record.CIFailureToFix, number
	if _, err := e.store.PutCIFailure(ctx, tx, failure); err != nil {
		return err
	}
	if err := e.enqueue(ctx, tx, issue.Key, record.FixJobRead{Generation: issue.Generation, Task: w.Task, Fix: number, Check: check, Head: pass.head}); err != nil {
		return err
	}
	pass.open = &fix
	e.logOnCommit(ctx, "workflow: a failed CI check starts a fix beside the task's steps", "issue", issue.Key, "task", w.Task,
		"fix", number, "check", check, "head", pass.head, "place", place, "phase", issue.Phase)
	return nil
}

// ciFailureSentence is the sentence that names a failure of state's check at head: the check, the
// head and how its CI job ended (its ci_case). The daemon holds no name of the job before a fix
// reads it, so the job is the check's.
func ciFailureSentence(state record.CheckState, head string) string {
	return fmt.Sprintf("Check %s fails at %s: CI job of the check ended %s.", state.Check, head, state.CICase)
}

// tellImplementer sends body to the implementer of issue as one role message.
func (e *Engine) tellImplementer(ctx context.Context, tx pgx.Tx, issue record.Issue, body string) error {
	addressed, err := e.addressRoles(issue.Project, []roleRecipient{{issue: issue.Key, role: claim.RoleImplementer, row: issue.Key}}, nil)
	if err != nil {
		return err
	}
	for _, to := range addressed {
		if err := e.enqueue(ctx, tx, to.issue, record.RoleMessage{Role: string(to.token), Body: body}); err != nil {
			return err
		}
	}
	return nil
}
