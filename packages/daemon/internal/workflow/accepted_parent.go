package workflow

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// openBeneath is OpenBeneath over the Dispatch chains of issue's project, read here with issue
// named (record.ReadChains).
func (e *Engine) openBeneath(ctx context.Context, tx pgx.Tx, issue record.Issue) ([]string, error) {
	chains, err := record.ReadChains(ctx, tx, e.store, issue.Project, []string{issue.Key})
	if err != nil {
		return nil, err
	}
	return record.OpenBeneath(chains, issue.Key), nil
}

// ChildUnderAcceptedParentNotice is what the architect above the child is told when a person
// moves an issue to todo beneath an ancestor whose own reviewed head the daemon has already
// accepted (record.AcceptedAncestor): the issue is recorded, and left standing at admitted until
// that ancestor closes, because work entering the workflow beneath it now would let that ancestor
// merge a head which does not carry the work. The two doors an agent uses refuse such an issue
// outright (the API's CHILD_UNDER_ACCEPTED_PARENT), so what reaches this is a person's own
// move, which the daemon records rather than undoes.
//
// It is the child-status kind every other word about a child entering or leaving its tree is
// told under, so it reaches the architect above the child by the route those already take.
func ChildUnderAcceptedParentNotice(child, ancestor string) record.Notice {
	return record.Notice{Kind: "child-status", Role: claim.RoleArchitect, Reason: fmt.Sprintf(
		"%s is beneath %s, whose own reviewed head the daemon has already accepted, so %s is recorded at admitted and nothing starts it until %s closes. "+
			"Take %s back with a backward move to run %s under it now, or leave %s where it is and it starts once %s has closed.",
		child, ancestor, child, ancestor, ancestor, child, child, ancestor)}
}

// ChildUnderAcceptedParent is what release_children and rerun_child answer for an issue whose
// ancestor the daemon has already accepted (CHILD_UNDER_ACCEPTED_PARENT), naming the ancestor and
// what undoes the refusal. Both calls say one sentence, because they refuse for one reason: the
// ancestor was accepted on checks of its children's work as it stood, so nothing enters the
// workflow beneath a head already accepted. operation is what the caller would do again once the
// ancestor has closed.
func ChildUnderAcceptedParent(child, ancestor, operation string) string {
	return fmt.Sprintf("%s is beneath %s, whose own reviewed head the daemon has already accepted, and work entering the workflow beneath %s now would let it merge a head that does not carry that work; %s once %s has closed, or take %s back with a backward move first",
		child, ancestor, ancestor, operation, ancestor, ancestor)
}
