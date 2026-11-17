package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// beneathRetro is the issues beneath issue still in the workflow (openBeneath) where its move into
// to is the pass of its task's last step into the retro, on a plan with no acceptance step, and
// nothing for every other move (B-8, C2-1). A parent's head proves its children's merged work, and
// a task with no step acceptance:owner has no sign-off to refuse a parent while one of them runs
// (SIGNOFF_CHILD_IN_WORKFLOW), so such a parent is held before its retro instead (transition),
// from retro for its children (record.HoldForChildren), until the last of them leaves
// (startHeldRetro). The hold stands in place of the retro, which keeps no slot (record.KeepsSlot
// reads record.StandsAt), so the parent holds none of the slots its children may need. An issue
// with no plan rows walks today's line, whose retro follows the owner's acceptance, so it is never
// held so (R20).
func (e *Engine) beneathRetro(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, to phase.Phase) ([]string, error) {
	if !w.Stored || to != phase.Retro || slices.Contains(w.Line, phase.Accepting) {
		return nil, nil
	}
	return e.openBeneath(ctx, tx, issue)
}

// retroHeldNotice tells the owner of parent that Legion holds it before its retro for beneath, the
// issues beneath it still in the workflow, and what ends the hold: each of them leaving it, done
// or parked under the park rule (parkRule, carried word for word as every notice of a park is).
// It names the retro and the implementer whose work the hold keeps, as the held notice of any
// hold names the phase it is from (HeldNotice).
func retroHeldNotice(parent string, beneath []string) record.Notice {
	return record.Notice{Kind: "retro-held", Role: phase.RoleFor(phase.Retro), Phase: phase.Retro, Reason: fmt.Sprintf(
		"%s passed its last step, and Legion holds it before its retro while these issues beneath it are still in the workflow: %s. "+
			"A parent's head proves its children's merged work. Run each one to done, or park one with park_child "+parkRule+
			"; Legion starts the retro when the last one leaves.", parent, strings.Join(beneath, ", "))}
}

// startHeldRetro starts the retro of parent where it is held before it for the issues beneath it
// (record.HoldForChildren) and the last of them has left the workflow (openBeneath is empty), in
// the transaction of that leave (leave, which runs this on every ancestor of the issue that
// leaves); every other issue it leaves as it stands. Nothing runs while parent's tree lingers, as
// no transition does.
//
// The hold is cleared and parent stands in its retro, where its task already stands ({retro 0},
// moveTask at the hold). The retro's Dispatch status is queued through Engine.status, since the
// hold wrote none and the record still holds the status of the last step (PhaseStatus(held) is
// empty). The implementer is started with the task the pass into the retro would have started it
// with (transition): the handoff of the step the task passed last, which the hold kept on that
// step's phases row, and the pull request. Its start raises its entry and the count of starts of
// the retro's place (start, which writes the retro's step), not restart, which raises neither,
// since this retro never ran. The act is children_done, which the log line names: no row of the
// table moves a held issue.
func (e *Engine) startHeldRetro(ctx context.Context, tx pgx.Tx, parent record.Issue) error {
	if !record.HeldForChildren(parent) || !record.InWorkflow(parent) {
		return nil
	}
	if lingers, err := record.TreeLingers(ctx, e.store, tx, parent.Tree); err != nil || lingers {
		return err
	}
	if beneath, err := e.openBeneath(ctx, tx, parent); err != nil || len(beneath) > 0 {
		return err
	}
	w, err := e.walk(ctx, tx, parent)
	if err != nil {
		return err
	}
	steps := w.steps()
	if !w.Stored || len(steps) == 0 || w.Place != (record.TaskPlace{Kind: record.PlaceRetro}) {
		return fmt.Errorf("start the held retro of %s: task %s stands at %+v of the line %v, not at its retro", parent.Key, w.Task, w.Place, w.Line)
	}
	last, err := e.phaseRow(ctx, tx, parent.Key, phase.RoleFor(steps[len(steps)-1]))
	if err != nil {
		return err
	}
	pr, err := e.store.PullRequest(ctx, tx, parent.Key)
	if err != nil {
		return err
	}
	status := PhaseStatus(phase.Retro)
	if err := e.status(ctx, tx, parent, status, ""); err != nil {
		return err
	}
	parent.Phase, parent.Hold, parent.Status = phase.Retro, nil, status
	if err := e.putIssue(ctx, tx, parent); err != nil {
		return err
	}
	// The release takes the issue out of held into a phase, which is a movement (the rule in
	// dispatchIssue).
	if err := e.store.MarkIssueMoved(ctx, tx, parent.Key, e.now()); err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: the last issue beneath a held parent left the workflow, and its retro starts", "issue", parent.Key, "tree", parent.Tree,
		"task", w.Task, "act", "children_done", "from", phase.Held, "to", phase.Retro, "status", parent.Status)
	role := phase.RoleFor(phase.Retro)
	if err := e.clearHandoff(ctx, tx, parent.Key, role); err != nil {
		return err
	}
	return e.start(ctx, tx, parent, role, func(at Standing) string {
		return task(parent, at, last, pr, "", "")
	}, false)
}
