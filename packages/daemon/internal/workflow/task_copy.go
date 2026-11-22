package workflow

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// startTaskCopy gives task of issue's plan its branch at a start of the task, in the transaction
// that starts it (record.Store.AssignTaskBranch: once, the issue's own branch for the first task of
// the generation to start, else the first give of the task's slug no task of the generation took).
// A task whose branch is not the issue's own, or that needs other tasks, gets one task_branch row
// (record.TaskBranch), which makes its branch on GitHub at the joined work of its needs before any
// start of the issue's roles goes out. The task that holds the issue's own branch and needs none
// queues nothing: the issue_branch row its first step queues makes that branch (gateOpened). A
// task the plan does not hold is refused, naming it.
func (e *Engine) startTaskCopy(ctx context.Context, tx pgx.Tx, issue record.Issue, task string) error {
	branch, err := e.store.AssignTaskBranch(ctx, tx, issue.Key, issue.Generation, task)
	if err != nil {
		return err
	}
	stored, err := e.store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil {
		return err
	}
	// AssignTaskBranch refused a task the plan does not hold, and holds the generation's task rows
	// locked, each of which names a version of the plan (its foreign key): the plan holds the task.
	at := slices.IndexFunc(stored.Tasks, func(row record.PlanTask) bool { return row.Task == task })
	if branch == claim.IssueBranch(issue.Key) && len(stored.Tasks[at].Needs) == 0 {
		return nil
	}
	return e.enqueue(ctx, tx, issue.Key, record.TaskBranch{Generation: issue.Generation, Task: task})
}

// stepOfStart is the running step start starts on issue (record.Issue.Running): the step of the
// agent its claim names, or, for a start that names none, of the agent of the claim the row then
// reaches, worker, the claim of the worker on the start's role's step (record.WorkerClaim). An
// agent runs at most one step, and a step that waits runs none, so a step of another task that
// waits for the same agent is never the answer. False where the claim names no agent of the issue
// or its agent runs no step.
func stepOfStart(issue record.Issue, start record.SuperviseRequest, worker claim.Token) (record.RunningStep, bool) {
	token := start.Claim
	if token == "" {
		token = worker
	}
	if _, agent, ok := token.Cut(); ok {
		return issue.StepOfAgent(agent)
	}
	return record.RunningStep{}, false
}

// startBranch is the branch start names (record.SuperviseRequest.Branch): the branch of the task of
// the step it starts (stepOfStart, record.PlanTask.BranchOf), and "" where that branch is the
// issue's own, where the start starts no running step, and where the issue has no plan rows at its
// generation, so each such start reads as every start did before tasks had branches of their own.
// rows is the issue's phases rows.
func startBranch(ctx context.Context, tx pgx.Tx, store record.Store, issue record.Issue, start record.SuperviseRequest, rows []record.PhaseRow) (string, error) {
	stored, err := store.Plan(ctx, tx, issue.Key, issue.Generation)
	if err != nil || stored == nil {
		return "", err
	}
	project, err := claim.ProjectToken(issue.Project)
	if err != nil {
		return "", err
	}
	worker, err := record.WorkerClaim(project, issue.Key, start.Role, rows)
	if err != nil {
		return "", err
	}
	step, ok := stepOfStart(issue, start, worker)
	if !ok {
		return "", nil
	}
	at := slices.IndexFunc(stored.Tasks, func(row record.PlanTask) bool { return row.Task == step.Task })
	if at < 0 {
		return "", nil
	}
	if branch := stored.Tasks[at].BranchOf(issue.Key); branch != claim.IssueBranch(issue.Key) {
		return branch, nil
	}
	return "", nil
}
