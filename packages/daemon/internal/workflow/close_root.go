package workflow

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// closeRoot ends a tree its root architect closes before the tree's first phase starts: a human
// decided no change at the design gate, or the issue is moot. sign_off
// needs the production check and a backward move a phase worker, so without this only a human's
// status write could end such a tree. The close queues done to Dispatch with the architect's
// reason, which the write posts on the issue first (writeClose), and the root keeps its phase, its
// claims and its slot until Dispatch shows it done: it then moves to phase done and leaves as any
// close does (closeLanded). A close Dispatch refuses changes nothing and is written again once
// each child the root then has is done (closeRefused, WriteDueClose).
//
// It is refused, as the sign-off is and with the same code, while any issue beneath the root is
// still in the workflow (OpenBeneath): the close would close their parent under those issues
// mid-turn, and the order a tree keeps is that its children
// finish first. A root at admitted with children running is exactly the shape children-first
// creates, so this is the common case rather than a corner of it. Past that, it is refused as the
// sign-off is, in the same order (acceptanceHeld, dispatch://INFRA-3320 R8), while a root whose
// step rests on the root's approval is still in the workflow (SIGNOFF_RESTING_ROOT_IN_WORKFLOW),
// and then while a Dispatch child of the root is open (SIGNOFF_CHILD_OPEN). After R8's codes it is
// refused while a Dispatch child of the root is not done (CHILDREN_NOT_DONE, R24): Dispatch closes
// an issue only once each of its children is done, so a child in icebox or the backlog holds the
// close as an open one does.
//
// A root waiting on a person (phase.AwaitingPerson) closes the same way, because it has started
// no phase either: a root reaches that phase only from admitted, and it is where the answer
// "close this" arrives (dispatch://INFRA-545, whose architect could not end its tree and whose
// controller's own Dispatch close the daemon set back, as a session's write on a waiting root).
// It keeps its slot while it waits (blockers.waitsOf), and leave frees that slot once.
func (e *Engine) closeRoot(ctx context.Context, tx pgx.Tx, fact intake.CloseRoot) (intake.Result, error) {
	root, err := e.store.Issue(ctx, tx, fact.Issue)
	if err != nil || root == nil {
		return intake.Result{}, err
	}
	// A root is one on the Dispatch chain this transaction reads, closed by its own architect
	// (architectCallRefusal, R18 of dispatch://INFRA-3320): an issue under another in Legion's
	// work is no root whatever tree its record stores.
	if refusal, err := e.architectCallRefusal(ctx, tx, fact.Architect, *root, closeCall); err != nil || refusal != nil {
		return intake.Result{Refusal: refusal}, err
	}
	if !closableAtPhase(root.Phase) || record.OutOfWorkflow(root.Status) {
		return refused("CLOSE_AFTER_PHASE_STARTED", fmt.Sprintf("%s is in phase %s with status %s; close_root ends a tree only while its root has started no phase — at admitted, whether or not it holds its slot, or waiting on a person — and a tree past that, or one already leaving, ends through its workflow or a human", root.Key, root.Phase, root.Status)), nil
	}
	held, err := e.acceptanceHeld(ctx, tx, *root)
	if err != nil {
		return intake.Result{}, err
	}
	switch held.code {
	case signOffChildInWorkflow:
		return refused(held.code, fmt.Sprintf("%s is not closed while these issues beneath it are still in the workflow: %s. Closing it would close their parent under them mid-turn; run each one to done, and close the tree again. Park one with park_child "+parkRule+", and say in a comment which of those it is. Ask the person about a child that can reach neither done nor a park under that rule, and hold the close until they answer",
			root.Key, held.names)), nil
	case signOffRestingRoot:
		return refused(held.code, fmt.Sprintf("%s is not closed while these roots that rest on its approval are still in the workflow: %s. The design-gate step of each rests on an approval of %s, so %s closes after them. Each is a tree of its own: its own architect, or a person, moves it. Close the tree again once each one is done or out of the workflow",
			root.Key, held.names, root.Key, root.Key)), nil
	case signOffChildOpen:
		return refused(held.code, fmt.Sprintf("%s is not closed while these Dispatch children of it are open: %s. A parent closes after its children. Close or icebox each child that was never released "+parkRule+", say in a comment which of those it is, and close the tree again. A child that runs as a tree of its own is moved by its own architect or a person. Ask the person about a child that can reach neither done nor icebox under that rule, and hold the close until they answer",
			root.Key, held.names)), nil
	}
	open, err := e.childrenNotDoneOf(ctx, tx, *root)
	if err != nil {
		return intake.Result{}, err
	}
	if len(open) > 0 {
		return childrenNotDoneRefusal("close_root", root.Key, open), nil
	}
	return intake.Result{}, e.writeClose(ctx, tx, *root, closeKindRoot, fact.Message())
}

// closableAtPhase says whether a root at this phase has started no phase of its tree, which is
// what close_root ends. Both are states a root sits in with its design gate unanswered.
func closableAtPhase(p phase.Phase) bool {
	return p == phase.Admitted || p == phase.AwaitingPerson
}
