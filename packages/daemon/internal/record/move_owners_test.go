package record

import (
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// MoveOwners is the one rule of which architects own a move of an issue into or out of the
// workflow (dispatch://INFRA-3178), read on the Dispatch chain (R18 of dispatch://INFRA-3320): the
// engine's exception for the owning architect's status on a child asks it (workflow's
// sessionStatusWrite). The architect of every issue above the issue on its chain owns it, so the
// root's architect owns every issue under it and a sub-architect the issues under its own issue,
// and not its own issue, which an architect above it moves. A root has no owner of a move, its own
// architect included. An issue whose parent is outside Legion's work and the copy stands under no
// issue in Legion's work, so nobody owns it either.
func TestMoveOwnersIsTheRuleOfTheArchitectsMovesOfAChild(t *testing.T) {
	const (
		root            = "LEGION-1"
		child           = "LEGION-2"
		grandchild      = "LEGION-3" // under child
		greatGrandchild = "LEGION-4" // under grandchild
		sibling         = "LEGION-5"
		nephew          = "LEGION-6" // under sibling
		strayed         = "LEGION-7" // its parent is no issue of the copy
	)
	member := func(key, parent string) Issue {
		return chainIssue(key, root, parent, phase.Implementing, "in_progress")
	}
	records := []Issue{
		member(root, ""), member(child, root), member(grandchild, child), member(greatGrandchild, grandchild),
		member(sibling, root), member(nephew, sibling), member(strayed, "OTHER-1"),
	}
	chains := chainsOf(records, copyOf(records...))
	for _, tc := range []struct {
		name      string
		architect string
		target    string
		owns      bool
	}{
		{name: "the root's architect on a child", architect: root, target: child, owns: true},
		{name: "the root's architect on a grandchild", architect: root, target: grandchild, owns: true},
		{name: "the root's architect on its own root", architect: root, target: root, owns: false},
		{name: "the root's architect on an issue whose parent is no issue of the copy", architect: root, target: strayed, owns: false},
		{name: "a sub-architect on its own issue", architect: child, target: child, owns: false},
		{name: "a sub-architect on an issue under it", architect: child, target: grandchild, owns: true},
		{name: "a sub-architect on a grandchild under it", architect: child, target: greatGrandchild, owns: true},
		{name: "a sub-architect on its sibling", architect: child, target: sibling, owns: false},
		{name: "a sub-architect on an issue under its sibling", architect: child, target: nephew, owns: false},
		{name: "a sub-architect on the root", architect: child, target: root, owns: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := slices.Contains(MoveOwners(chains, tc.target), tc.architect); got != tc.owns {
				t.Fatalf("the architect of %s among MoveOwners(%s) = %v: %t, want %t", tc.architect, tc.target, MoveOwners(chains, tc.target), got, tc.owns)
			}
		})
	}
}
