package admit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// adoption is a recorded issue that joins the live tree of target, that tree's root, recorded
// under the Dispatch parent and held on the blockers of child, the release's own read of it: a
// queued root with every record of its own tree (workflow's Engine.Adopt), or, where member
// holds, the one member of a closed tree the release named (Engine.AdoptMember, C4 of
// dispatch://INFRA-3107). The flag is read off the record once, where the wave sorts the children
// it was given, so the loop that writes asks no question again.
type adoption struct {
	issue, target record.Issue
	child         intake.AdoptedChild
	member        bool
}

// adoptChildren applies an architect's release of children recorded outside its tree: each root
// that queued in admission before a person made it a child of fact.Tree joins that tree with every
// record of its own (workflow's Engine.Adopt), under the Dispatch parent the release read, and the
// line is promoted as after any other fact; it reports the slots that promotion's roots returned.
// A child already recorded in fact.Tree needs nothing, and a member of another child's queued tree
// joins with that child.
//
// A released child that is a member of a closed tree joins alone (workflow's Engine.AdoptMember),
// which is C4 of dispatch://INFRA-3107: the daemon starts no issue of the tree it stood in, and
// the tree that takes it takes the one issue the architect released, never the rest of that
// tree. A member that holds an admission slot is work the daemon has already started, which C4
// leaves where it stands: the slot rows are read once for the wave, beside the root of fact.Tree,
// and such a member refuses the wave by name (intake.ChildNotAdoptable).
//
// The wave is one decision: when any child cannot join (workflow's AdoptionRefusal), or the tree
// is not live when the first child that could join is asked about, that refusal is the answer and
// nothing is written, no adoption and no promotion, since intake commits a refused fact.
// Reconcile's holds do not apply: the release's own Dispatch read just proved each child's parent
// chain reaches the tree.
func (a *Admission) adoptChildren(ctx context.Context, tx pgx.Tx, fact intake.ChildAdoption) ([]string, *intake.Refusal, error) {
	var outside []adoption
	seen := make(map[string]bool, len(fact.Children))
	for _, child := range fact.Children {
		if seen[child.Issue] {
			continue
		}
		seen[child.Issue] = true
		stored, err := a.store.Issue(ctx, tx, child.Issue)
		if err != nil {
			return nil, nil, fmt.Errorf("read released child %s: %w", child.Issue, err)
		}
		if stored != nil && stored.Tree != fact.Tree {
			outside = append(outside, adoption{issue: *stored, child: child,
				member: !claim.IsTreeRoot(stored.Key, stored.Tree)})
		}
	}
	// The tree's root, its liveness and the project's slot rows are read once for every child the
	// release asks about.
	var root *record.Issue
	live := false
	var slotted map[string]record.Slot
	if len(outside) > 0 {
		var err error
		if root, err = a.store.Issue(ctx, tx, fact.Tree); err != nil {
			return nil, nil, fmt.Errorf("read the root of %s: %w", fact.Tree, err)
		}
		if root != nil {
			if live, err = record.TreeLive(ctx, a.store, tx, *root); err != nil {
				return nil, nil, fmt.Errorf("read whether the tree of %s is live: %w", fact.Tree, err)
			}
		}
		rows, err := a.store.Slots(ctx, tx)
		if err != nil {
			return nil, nil, fmt.Errorf("read the admission slots for the release of %s: %w", fact.Tree, err)
		}
		slotted = make(map[string]record.Slot, len(rows))
		for _, slot := range rows {
			slotted[slot.Issue] = slot
		}
	}
	queued := make(map[string]bool, len(outside))
	for _, child := range outside {
		if !child.member {
			queued[child.issue.Key] = true
		}
	}
	var joining []adoption
	for _, child := range outside {
		if child.member && queued[child.issue.Tree] {
			continue
		}
		refusal, err := a.engine.AdoptionRefusal(ctx, tx, child.issue, fact.Tree)
		if err != nil {
			return nil, nil, fmt.Errorf("read whether %s joins the tree of %s: %w", child.issue.Key, fact.Tree, err)
		}
		if refusal != nil {
			return nil, refusal, nil
		}
		if !live {
			return nil, intake.ChildNotAdoptable(child.issue.Key, fact.Tree, fmt.Sprintf("the tree of %s is not live", fact.Tree)), nil
		}
		// C4 takes a member of a closed tree that holds no admission slot: one that holds a row is
		// at work in the tree it stands in, and the daemon keeps what it started out of the route.
		if slot, holds := slotted[child.issue.Key]; child.member && holds {
			return nil, intake.ChildNotAdoptable(child.issue.Key, fact.Tree,
				fmt.Sprintf("it holds admission slot %d in the tree of %s", slot.Index, child.issue.Tree)), nil
		}
		// A live tree's root is recorded.
		child.target = *root
		joining = append(joining, child)
	}
	for _, child := range joining {
		join := a.engine.Adopt
		if child.member {
			join = a.engine.AdoptMember
		}
		if err := join(ctx, tx, child.issue, child.target, child.child); err != nil {
			return nil, nil, fmt.Errorf("adopt %s into the tree of %s: %w", child.issue.Key, fact.Tree, err)
		}
	}
	_, returned, err := a.promote(ctx, tx)
	return returned, nil, err
}
