package admit

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// The admission cap is read at every decision that needs one (Admission.readCap), not copied
// when admission was built: a cap raised through the settings route — the same Apply an operator
// call makes — is the cap the next admission decision promotes under, with nothing rebuilt. The
// change says as much to the caller that asked for it: a decision-class path, taking effect at
// once, naming no holder.
func TestTheAdmissionCapChangedThroughTheSettingsHolderIsUsedByTheNextDecision(t *testing.T) {
	pool := migratedPool(t)
	live := liveCap(t, 1)
	admission := newAdmissionReading(t, func() int { return live.Snapshot().AdmissionCap }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	seedWaiting(t, pool, "LEGION-A", "A")
	seedWaiting(t, pool, "LEGION-B", "B")
	seedWaiting(t, pool, "LEGION-C", "C")
	summaries := []dispatch.IssueSummary{
		{Key: "LEGION-A", ResolvedRepo: "acme/widgets", Title: "A", Status: "todo", Rank: "A", HandedOver: handed},
		{Key: "LEGION-B", ResolvedRepo: "acme/widgets", Title: "B", Status: "todo", Rank: "B", HandedOver: handed},
		{Key: "LEGION-C", ResolvedRepo: "acme/widgets", Title: "C", Status: "todo", Rank: "C", HandedOver: handed},
	}

	reconcile(t, pool, admission, summaries)
	assertSlots(t, pool, []record.Slot{{Issue: "LEGION-A", Index: 0, AdmittedAt: fixedNow}})

	applied, refused, err := live.Apply(config.SourceRoute, "someone", map[string]any{"admission_cap": 3})
	if err != nil || len(refused) != 0 {
		t.Fatalf("apply admission_cap=3: %v, refused %+v", err, refused)
	}
	if len(applied) != 1 || applied[0].Path != "admission_cap" || applied[0].New != "3" {
		t.Fatalf("the apply reported %+v, want one admission_cap change to 3", applied)
	}
	if applied[0].Class != config.ClassDecision || applied[0].Outcome != config.OutcomeAtOnce || applied[0].Holder != "" {
		t.Fatalf("the change is %+v, want a decision-class change taking effect at once with no holder", applied[0])
	}

	reconcile(t, pool, admission, summaries)
	assertSlots(t, pool, []record.Slot{
		{Issue: "LEGION-A", Index: 0, AdmittedAt: fixedNow},
		{Issue: "LEGION-B", Index: 1, AdmittedAt: fixedNow},
		{Issue: "LEGION-C", Index: 2, AdmittedAt: fixedNow},
	})
}

// One admission pass reads the cap once, before the stall measure, and the measure's free-slot
// term (lineCanMove, through capLets) and the promotion both judge that one number. A change of
// `admission_cap` through the settings route can land while a pass runs: the reader below makes
// that change, through the same Apply an operator's route call makes, right after the pass first
// reads the cap, so every later read in the pass answers the new cap.
//
// In this project the cap alone decides both answers. A root waiting on a person holds the one
// row, its architect is idle and its tree has been quiet past the bound; a root that has never
// run waits in the line, needs a row and waits on no other issue. Under a cap of 1 nothing of the
// project can move: the holder waits by design, no claim is at work, and the line's one entry
// needs a row the cap does not have. So the waiting tree has stopped and is named, and the
// promotion starts nothing. Under a cap of 2 the line's entry takes the free row: the line moves,
// no tree is named, and the promotion starts the queued root. Whichever number the pass judges
// by, one pass either names the tree and starts nothing, or starts the queued root and names
// nothing. Those two outcomes are the invariant this test asserts.
//
// A second reading splits the pass across the change. That is the measure reading the cap inside
// lineCanMove and the promotion reading it again after the measure, as the pass did after main's
// capLets was merged onto the live cap:
//
//   - raised, 1 then 2: the measure judges 1 and names the tree, and the promotion judges 2 and
//     starts the queued root. The controller is told of a stall in a project whose line this same
//     pass moved, which is the false notice capLets exists to stop;
//   - lowered, 2 then 1: the measure judges 2 and names nothing, and the promotion judges 1 and
//     starts nothing. A real stall goes unnamed for the pass.
//
// The existing rig reaches both directions, so each is a subtest.
func TestOnePassJudgesTheStallAndThePromotionByOneReadingOfTheCap(t *testing.T) {
	const (
		awaiting = "LEGION-950"
		queued   = "LEGION-960"
	)
	for _, tc := range []struct {
		name     string
		from, to int
	}{
		{"raised", 1, 2},
		{"lowered", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			live := liveCap(t, tc.from)
			changed := false
			var change error
			read := func() int {
				cap := live.Snapshot().AdmissionCap
				if !changed {
					changed = true
					_, refused, err := live.Apply(config.SourceRoute, "someone", map[string]any{"admission_cap": tc.to})
					if err == nil && len(refused) != 0 {
						err = fmt.Errorf("refused %+v", refused)
					}
					change = err
				}
				return cap
			}
			admission := newAdmissionReading(t, read, slog.New(slog.NewTextHandler(io.Discard, nil)))
			admission.UseStalledTreeNotice(deploymentBound(t, ""))
			registerController(t, pool)
			records := claimStore(t, pool)
			seedQuietRoot(t, pool, awaiting, "A", phase.AwaitingPerson)
			seedClaim(t, records, awaiting, awaiting, claim.RoleArchitect, supervise.StateIdle)
			seedWaiting(t, pool, queued, "B")

			// The premise: the root waiting on a person holds the one row, and the line's one
			// entry is the queued root, which holds none.
			assertRowHolders(t, pool, awaiting)
			assertWaiting(t, pool, []string{queued})

			settled := settleMovements(t, pool, awaiting)
			admission.now = func() time.Time { return settled.Add(7 * time.Hour) }
			tick(t, pool, admission, tc.name)

			if change != nil {
				t.Fatalf("apply admission_cap=%d through the route inside the pass: %v", tc.to, change)
			}
			if got := live.Snapshot().AdmissionCap; !changed || got != tc.to {
				t.Fatalf("the cap after the pass is %d (changed inside it: %t), want %d: the premise is that the route changed the cap under the running pass",
					got, changed, tc.to)
			}
			told := stalledRoots(t, pool)
			if len(told) > 1 || len(told) == 1 && told[0] != awaiting {
				t.Fatalf("the controller was told of %v, want %s at most: no other tree of the project runs", told, awaiting)
			}
			named, promoted := len(told) == 1, holdsSlot(t, pool, queued)
			if named && promoted {
				t.Fatalf("the pass told the controller that the tree of %s has stopped and started %s from the line: the measure and the promotion judged two readings of the cap (%d, then %d)",
					awaiting, queued, tc.from, tc.to)
			}
			if !named && !promoted {
				t.Fatalf("the pass told the controller nothing and started nothing: under the cap the promotion judged, the tree of %s has stopped, and the measure judged another reading of the cap (%d, then %d)",
					awaiting, tc.from, tc.to)
			}
		})
	}
}

// liveCap is a settings holder over a real legion.yaml whose admission_cap is cap: a route change
// is applied by reading that file again under the overlay, so the test needs the file and not a
// Config it built in memory.
func liveCap(t *testing.T, cap int) *config.Live {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "legion.yaml")
	body := fmt.Sprintf("project: %s\nstate_dir: %s\npostgres_dsn: postgres://legion@127.0.0.1:5432/legion\nadmission_cap: %d\n",
		testProject, dir, cap)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write legion.yaml: %v", err)
	}
	live, err := config.NewLive(path, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return live
}
