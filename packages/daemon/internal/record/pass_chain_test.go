package record

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/phase"
)

// One pass walks one chain (chainOf): the hold under an accepted ancestor (Waiting) and the
// children-first cycle (ChildWaits) are two walks of the same records, so the map they walk is
// built in one place. It holds the pass's own records and the records above them that its read
// leaves out (LineFacts.Parents), which is what lets either walk cross an ancestor already done;
// the pass's own records stay a map of their own, since what runs and what an issue ranks against
// is read from those alone.
func TestOnePassWalksOneChain(t *testing.T) {
	root := chainIssue("LEGION-900", "LEGION-900", "", phase.Admitted, "in_progress")
	merging := chainIssue("LEGION-901", "LEGION-900", "LEGION-900", phase.Merging, "needs_review")
	doneParent := chainIssue("LEGION-902", "LEGION-900", "LEGION-901", phase.Done, "done")
	child := chainIssue("LEGION-903", "LEGION-900", "LEGION-902", phase.Admitted, "todo")
	issues := []Issue{root, merging, child}
	above := map[string]Issue{doneParent.Key: doneParent}

	chain := chainOf(byKey(issues), above)
	want := map[string]Issue{root.Key: root, merging.Key: merging, child.Key: child, doneParent.Key: doneParent}
	if !reflect.DeepEqual(chain, want) {
		t.Fatalf("the chain of the pass holds %v, want %v: its own records and the records above them",
			slices.Sorted(maps.Keys(chain)), slices.Sorted(maps.Keys(want)))
	}
	// The one walk both readers make crosses the done parent and names the merging ancestor,
	// which is the whole reason the records above the pass are in the map at all.
	if ancestor := AcceptedAncestor(child, chain); ancestor != merging.Key {
		t.Fatalf("the accepted ancestor of %s over the pass's chain = %q, want %s past the done parent %s", child.Key, ancestor, merging.Key, doneParent.Key)
	}
	if ancestor := AcceptedAncestor(child, byKey(issues)); ancestor != "" {
		t.Fatalf("the accepted ancestor of %s over the pass's own records alone = %q, want none: that walk stops at the parent they leave out", child.Key, ancestor)
	}
}

// aboveRead is a store whose read of the records above a pass (AncestorsOf) answers above, and
// whose every other read is the store's own.
type aboveRead struct {
	Store
	above []Issue
}

func (s aboveRead) AncestorsOf(context.Context, pgx.Tx, string, []string) ([]Issue, error) {
	return s.above, nil
}

// allocated is the bytes the heap gave out while do ran.
func allocated(do func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	do()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// One pass builds its parent chain once (passChain), and every walk of it ReadLineFacts makes
// reads that one map: the cycle of each Dispatch child (ChildWaits), the child left out of the
// started parents' waits and of the retro-held parents' (withoutHeldUnderAccepted) and the issues
// beneath a parent held before its retro (retroHoldsOf). The pass here makes all four: a started
// root and a root held before its retro, each with a child filed under it that no tree records.
//
// Nothing of a build shows outside the pass but its cost, so the builds are counted by their bytes.
// The records above the pass are many (aboveRecords, which the read of them answers), each a done
// tree of its own that no walk climbs, so one copy of them outweighs everything else the pass
// allocates. The pass's bytes, less those of the read of those records (ReadParents, which
// ReadLineFacts makes as it is) and of the one walk that lists every record of the chain
// (openBeneathIn, for the held root), are a whole number of chains, each the bytes of one build
// (chainOf). The band around one catches a walk that builds a copy of its own, which adds one
// chain: a pass whose four walks each build one comes to four.
func TestAPassBuildsItsParentChainOnce(t *testing.T) {
	const (
		started      = "LEGION-960" // a root in implementing, filed under a done issue
		held         = "LEGION-961" // a root held before its retro
		filedUnder   = "LEGION-962" // the done issue the started root was filed under
		startedChild = "LEGION-963" // filed under the started root, never released
		heldChild    = "LEGION-964" // filed under the held root, never released
		aboveRecords = 4000
	)
	ctx := context.Background()
	st := migratedStore(t)
	heldRoot := planHeld(held, held, nil, phase.Retro, HoldForChildren)
	issues := []Issue{chainIssue(started, started, filedUnder, phase.Implementing, "in_progress"), heldRoot}
	above := []Issue{chainIssue(filedUnder, filedUnder, "", phase.Done, DoneStatus)}
	for i := len(above); i < aboveRecords; i++ {
		key := fmt.Sprintf("LEGION-%d", 10000+i)
		above = append(above, chainIssue(key, key, "", phase.Done, DoneStatus))
	}
	records := aboveRead{Store: NewStore(), above: above}

	inTx(t, st, func(tx pgx.Tx) {
		for _, reading := range []DispatchReading{
			{Key: startedChild, Project: "LEGION", Seq: 1, Status: Carry("backlog"), Parent: Carry(started), Blockers: Carry[[]string](nil)},
			{Key: heldChild, Project: "LEGION", Seq: 1, Status: Carry("backlog"), Parent: Carry(held), Blockers: Carry[[]string](nil)},
		} {
			must(t, records.PutDispatchIssue(ctx, tx, reading))
		}
		var facts LineFacts
		var err error
		pass := allocated(func() { facts, err = ReadLineFacts(ctx, tx, records, "LEGION", issues, nil) })
		must(t, err)
		if len(facts.Parents) != aboveRecords || len(facts.StartedParentWaits[started].Open) != 1 || len(facts.RetroHolds[held].Open) != 1 {
			t.Fatalf("the pass holds %d records above it, the open children %+v of %s and the holds %+v of %s; want %d, and %s and %s open: without all four walks this test proves nothing",
				len(facts.Parents), facts.StartedParentWaits[started].Open, started, facts.RetroHolds[held], held, aboveRecords, startedChild, heldChild)
		}
		read := allocated(func() { _, err = ReadParents(ctx, tx, records, "LEGION", issues) })
		must(t, err)
		var chain map[string]Issue
		one := allocated(func() { chain = chainOf(byKey(issues), facts.Parents) })
		listed := allocated(func() { openBeneathIn(heldRoot, chain) })
		beyond := int64(pass) - int64(read) - int64(listed)
		builds := float64(beyond) / float64(one)
		t.Logf("the pass allocated %d bytes: %d for the read of the records above it, %d for the list of the held root's tree, and %.2f chains of %d bytes",
			pass, read, listed, builds, one)
		if builds < 0.5 || builds > 1.5 {
			t.Fatalf("the pass built its parent chain %.2f times over (%d bytes beyond its read and its list, %d bytes a chain), want once: every walk reads the one chain of the pass (passChain)",
				builds, beyond, one)
		}
	})
}
