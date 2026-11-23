package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// beneathRetro is the issues beneath issue still in the workflow (openBeneath) where issue moves
// into the retro (to) and its task's last step is not the owner's sign-off, and nothing for every
// other move. A parent's head proves its children's merged work. The owner's sign-off is refused
// while one of them runs (SIGNOFF_CHILD_IN_WORKFLOW), so a task whose last step is the sign-off
// passes into its retro with none of them in the workflow. A task whose last step is another has
// no such refusal before its retro: a task with no step acceptance:owner, and a task whose
// sign-off stands before another step (`implementer acceptance:owner reviewer`), where an issue
// beneath the parent may enter the workflow after the sign-off. Such a parent is held before its
// retro instead (transition), from retro for its children (record.HoldForChildren), until none of
// them is open beneath it (releaseHeldParents). The hold stands in place of the retro, which keeps
// no slot (record.KeepsSlot reads record.StandsAt), so the parent holds none of the slots its
// children may need. An issue with no plan rows walks today's line, whose retro follows the
// owner's sign-off, so it is never held so.
func (e *Engine) beneathRetro(ctx context.Context, tx pgx.Tx, issue record.Issue, w Walk, to phase.Phase) ([]string, error) {
	steps := w.steps()
	if !w.Stored || to != phase.Retro || len(steps) == 0 || steps[len(steps)-1] == phase.Accepting {
		return nil, nil
	}
	return e.openBeneath(ctx, tx, issue)
}

// retroHeldNotice tells the owner of parent that Legion holds it before its retro for beneath, the
// issues beneath it still in the workflow, and what ends the hold: each of them run to done, or
// ended by the owner's own status write in Dispatch. It ends with the sentence on a write the
// daemon has not taken yet and that write's rules word for word (endChildWords), as the pr-merged
// notice does (waitingForTree). It names the retro and the implementer whose work the hold keeps,
// as the held notice of any hold names the phase it is from (HeldNotice).
func retroHeldNotice(parent string, beneath []string) record.Notice {
	return record.Notice{Kind: "retro-held", Role: phase.RoleFor(phase.Retro), Phase: phase.Retro, Reason: fmt.Sprintf(
		"%s passed its last step, and Legion holds it before its retro while these issues beneath it are still in the workflow: %s. "+
			"A parent is accepted only once no issue beneath it is still in the workflow. Run each one to done, or end it; Legion starts the retro when the last one leaves.",
		parent, strings.Join(beneath, ", ")) + " " + endChildWords}
}

// heldParentWait is why parent, held before its retro for the issues beneath it
// (record.HoldForChildren), still waits, decided once (heldParentWaitOf) for both answers that say
// it: the refusal of a release of the hold (planHoldRefusal) and the reason posted beside the status
// a todo on a held root is set back to (releaseHeldRoot). beneath names the issues beneath it still
// open on its Dispatch chain (record.OpenBeneath). With none open its tree alone holds it, since
// the settle of every fact releases such a parent where its tree runs (releaseHeldParents): tree
// is the tree the state names for parent on that chain (record.Chains.StateTree), whose gate root
// runs nothing yet, as R4's rule reads it (record.TreeRuns): it waits in the line for the slot it
// does not hold.
type heldParentWait struct {
	parent  record.Issue
	beneath []string
	tree    string
}

// heldParentWaitOf decides why parent, held for the issues beneath it, still waits, on the
// Dispatch chains of its project read with parent named (record.ReadChains).
func (e *Engine) heldParentWaitOf(ctx context.Context, tx pgx.Tx, parent record.Issue) (heldParentWait, error) {
	chains, err := record.ReadChains(ctx, tx, e.store, parent.Project, []string{parent.Key})
	if err != nil {
		return heldParentWait{}, err
	}
	return heldParentWait{parent: parent, beneath: record.OpenBeneath(chains, parent.Key), tree: chains.StateTree(parent.Key)}, nil
}

// refusal is the answer to a release of the hold, which would start the retro's role early: the
// issues beneath still open, the last of which starts the retro (intake.RetryOfHeldParent), or why
// the tree runs nothing yet (intake.RetryOfHeldParentUntilTreeRuns).
func (w heldParentWait) refusal() *intake.Refusal {
	if len(w.beneath) > 0 {
		return intake.RetryOfHeldParent(w.parent.Key, w.beneath)
	}
	return intake.RetryOfHeldParentUntilTreeRuns(w.parent.Key, w.tree)
}

// reason is the reason posted beside the status a todo on the held root is set back to, which the
// person who set the todo reads: the issues beneath still open, the last of which starts the retro
// (intake.HeldParentWaitsForChildren), or why its tree runs nothing yet
// (intake.HeldParentWaitsForTree). It carries none of the architect's rules for ending a child,
// which only the architect's retro-held notice gives (retroHeldNotice).
func (w heldParentWait) reason() string {
	if len(w.beneath) > 0 {
		return intake.HeldParentWaitsForChildren(w.parent.Key, w.beneath)
	}
	return intake.HeldParentWaitsForTree(w.parent.Key, w.tree)
}

// releaseHeldParents is the one place a parent held before its retro for the issues beneath it
// (record.HoldForChildren) is released, at the settle of every fact and of every other
// transaction the daemon settles (SettleFact, intake.OpenSettlers) that wrote an issue
// (record.IssuesWritten): each such parent of each project of a written issue has its retro
// started where nothing beneath it on its Dispatch chain is in the workflow and its tree runs
// (startHeldRetro). Every change of what stands beneath a parent writes an issue: an issue beneath
// it that leaves the workflow (leave), a change of parent in Dispatch that moves one out from under
// it, which admission records on the moved issue (admit's recordObservation), and the promotion or
// the return to work of a gate root, which makes a tree that ran nothing run again. A change of
// parent leaves the moved issue on a chain that no longer passes through the parent it left, so
// the parents are read by project, and not from the chains of the written issues: the issues of
// the project held for their children, and those alone (record.Store.HeldForChildrenIssues), so a
// settle costs one read and the work of the parents held so, whatever the number of issues at
// work. So nothing can leave a parent held with nothing open beneath it in a tree that runs: such
// a parent stands in a tree that runs nothing, whose gate root waits in the line, and the hold's
// refusal of a retry names that wait (planHoldRefusal) where it names no issue still open beneath
// it. Each project is read once, and a fact that wrote no issue reads nothing.
func (e *Engine) releaseHeldParents(ctx context.Context, tx pgx.Tx, written []string) error {
	if len(written) == 0 {
		return nil
	}
	issues, err := e.store.IssuesByKey(ctx, tx, written)
	if err != nil {
		return err
	}
	var projects []string
	for _, issue := range issues {
		if !slices.Contains(projects, issue.Project) {
			projects = append(projects, issue.Project)
		}
	}
	for _, project := range projects {
		held, err := e.store.HeldForChildrenIssues(ctx, tx, project)
		if err != nil {
			return err
		}
		for _, parent := range held {
			if err := e.startHeldRetro(ctx, tx, parent); err != nil {
				return err
			}
		}
	}
	return nil
}

// startHeldRetro starts the retro of parent where it is held before it for the issues beneath it
// (record.HoldForChildren), nothing beneath it on its Dispatch chain is in the workflow
// (record.OpenBeneath is empty) and its tree runs: its gate root on that chain
// (record.Chains.GateRoot) runs by R4's rule (gateRootRuns, record.TreeRuns). The chains are its
// project's, read with parent named (record.ReadChains), in the transaction whose settle finds it
// so (releaseHeldParents); every other issue it leaves as it stands. A tree that runs nothing, one
// whose gate root waits in the line for its slot, or one on a chain with no gate root, starts no
// retro, as it runs no transition: the promotion or the return that runs it again writes its gate
// root, and that settle starts the retro.
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
	chains, err := record.ReadChains(ctx, tx, e.store, parent.Project, []string{parent.Key})
	if err != nil {
		return err
	}
	gateRoot := chains.GateRoot(parent.Key)
	root, recorded := chains.Record(gateRoot)
	if !recorded {
		return nil
	}
	if running, err := e.gateRootRuns(ctx, tx, root, gateRoot); err != nil || !running {
		return err
	}
	if len(record.OpenBeneath(chains, parent.Key)) > 0 {
		return nil
	}
	w, err := e.walk(ctx, tx, parent)
	if err != nil {
		return err
	}
	// A parent whose plan rows a way back dropped is held by no plan (planHold): it is a plain
	// hold, which a retry releases, and nothing starts a retro its task cannot stand at.
	if !w.Stored {
		return nil
	}
	steps := w.steps()
	if len(steps) == 0 || w.Place != (record.TaskPlace{Kind: record.PlaceRetro}) {
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
	if err := e.WriteIssue(ctx, tx, parent); err != nil {
		return err
	}
	// The release takes the issue out of held into a phase, which is a movement (the rule in
	// dispatchIssue).
	if err := e.store.MarkIssueMoved(ctx, tx, parent.Key, e.now()); err != nil {
		return err
	}
	e.logOnCommit(ctx, "workflow: the last issue beneath a held parent left the workflow, and its retro starts", "issue", parent.Key, "gate_root", gateRoot,
		"task", w.Task, "act", "children_done", "from", phase.Held, "to", phase.Retro, "status", parent.Status)
	// The parent now stands in its retro, so it is accepted (record.Accepted) as a pass into the
	// retro makes it, and every step of another root that waits on it as an ancestor ends its wait
	// (dispatch://INFRA-3320, transition).
	if err := e.endPendingOn(ctx, tx, parent.Key, ancestorLeft(parent.Key, fmt.Sprintf("The daemon moved %s into %s", parent.Key, phase.Retro), "", true)); err != nil {
		return err
	}
	role := phase.RoleFor(phase.Retro)
	if err := e.clearHandoff(ctx, tx, parent.Key, role); err != nil {
		return err
	}
	if err := e.start(ctx, tx, parent, w, role, func(at Standing) string {
		return task(parent, at, last, pr, "")
	}, phase.Held, false); err != nil {
		return err
	}
	// The release moves the parent into its retro as the pass into the retro would have
	// (transition), so it queues the reconcile of its tree's news as that pass does
	// (queueMovedNews). It asks nothing of the merge forward, as the hold before it asked nothing
	// (holdBeforeRetro): the retro is where the merge comes next (classify.MergeComesNext), where a
	// move asks nothing (mergeForwardOnEntry).
	return e.queueMovedNews(ctx, tx, parent)
}
