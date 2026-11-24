package admit

import (
	"reflect"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// A parent whose plan has no acceptance step stands held before its retro while a Dispatch child of
// it is open (record.HoldForChildren, dispatch://INFRA-3320 R8): here a child its architect filed
// and never released, which the daemon keeps no record of. Its close in Dispatch changes the
// daemon's mirror alone and writes no issue of the parent's tree, so no settle of the engine reads
// it: the admission pass reads every held parent (childrenWaits, workflow's ReleaseHeldRetro), and
// the pass after the close starts the retro, with nobody acting. While the child is open each pass
// leaves the parent held, and its architect is told once which child holds it, however many
// passes run. The hold itself, which the reviewer's pass enters, is workflow's
// TestAChildNeverReleasedHoldsAParentBeforeItsRetro.
func TestAParentHeldForAChildNeverReleasedStartsItsRetroWhenDispatchClosesIt(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	spSeedParent(t, pool, spParent, phase.Held, false, func(issue *record.Issue) {
		issue.Hold = &record.Hold{From: phase.Retro, Reason: record.HoldForChildren}
	})
	planByOwner(t, pool, spParent, record.TaskPlace{Kind: record.PlaceRetro})
	seedMirrorChild(t, pool, spChild, "backlog", spParent)

	spPass(t, pool, admission)
	spPass(t, pool, admission)

	assertHeldForChildren(t, pool, spParent, "while "+spChild+" is open in Dispatch")
	want := []record.Notice{{Kind: "retro-held", Role: claim.RoleImplementer, Phase: phase.Retro, Reason: spParent + " passed its last step, and Legion holds it before its retro " +
		"while these Dispatch children of it are open: " + spChild + " (backlog). " +
		"A parent is accepted after its children, a child that runs as a tree of its own included: run each one to done, releasing one that was never released. " +
		"A child that runs as a tree of its own is moved by its own architect or a person. " +
		"Legion starts the retro by itself once nothing holds it any longer: no issue beneath it is still in the workflow, " +
		"no root that rests on its approval is still in the workflow, and no Dispatch child of it is open. " +
		record.StatusNotTakenYet + " " + record.EndChildRule}}
	if got := gateNotices(t, pool, spParent, "retro-held"); !reflect.DeepEqual(got, want) {
		t.Fatalf("the retro-held notices after two passes = %+v, want %+v", got, want)
	}

	spMirror(t, pool, spChild, "done", spParent, 2)
	spPass(t, pool, admission)

	assertRetroStarted(t, pool, spParent, "once Dispatch closed "+spChild)
	if got := gateNotices(t, pool, spParent, "retro-held"); len(got) != 1 {
		t.Fatalf("%s was told %d retro-held notices in all, want the one", spParent, len(got))
	}
}
