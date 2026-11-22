package workflow

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// Each write of a step record names the experiment and the variant of its issue's group, the row
// of its draw that says how the issue is done (groupOf): the start and the end of a step and the
// retro's rating alike, so Dispatch can tell each run of an issue in an experiment by its variant.
// An issue whose draw names no group, here one with no row at all, writes neither key in any
// payload, so its rows are the rows the release before queued. The two walks are the opening of an
// issue's gate, which starts its planner, and the planner's completion, which ends the planner and
// starts the implementer; and the sign-off of an issue at accepting, which ends the acceptance and
// starts the retro, and the retro's completion with its rating, which ends the retro, rates the
// plan and starts the merge. Each walk runs for an issue drawn into the variant b of planning and
// for one with no row, and every step record row of each is read as the outbox holds it, its
// payload as raw JSON.
func TestEachStepRecordNamesTheVariantOfItsIssue(t *testing.T) {
	walks := []struct {
		name string
		// from is the phase LEGION-208 stands at before the walk.
		from phase.Phase
		// walk drives LEGION-208 from there to where the walk ends, on pool.
		walk func(t *testing.T, pool *pgxpool.Pool)
		// writes is how many step record rows the walk queues: what this test reads.
		writes int
	}{
		{name: "the planner's start and end, and the implementer's start", from: phase.Admitted, writes: 3, walk: func(t *testing.T, pool *pgxpool.Pool) {
			engine := stepEngine(nil)
			firstStep(t, pool, engine, "LEGION-208")
			move(t, pool, engine, "LEGION-208", TriggerPlannerCompleted, "", record.PhaseRow{})
		}},
		{name: "the acceptance's end, the retro's start, end and rating, and the merge's start", from: phase.Accepting, writes: 5, walk: func(t *testing.T, pool *pgxpool.Pool) {
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
			engine := stepEngine(nil)
			move(t, pool, engine, "LEGION-208", TriggerSignOff, "", record.PhaseRow{})
			applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleImplementer,
				Claim: "implement-claim", Summary: "retro done", Commit: "head",
				PlanRating: &record.PlanRating{Rating: 4, Reason: "The plan fit the work."}})
			assertPhase(t, pool, phase.Merging)
		}},
	}
	for _, walk := range walks {
		for _, drawn := range []bool{true, false} {
			name := walk.name + ", of an issue with no group"
			if drawn {
				name = walk.name + ", of an issue in the variant b of planning"
			}
			t.Run(name, func(t *testing.T) {
				pool := migratedPool(t)
				stepRoot(t, pool, walk.from)
				if drawn {
					drawnInto(t, pool, "LEGION-208", "planning", "b", "default")
				}
				walk.walk(t, pool)
				rows := issueOutboxOf(t, pool, "LEGION-208", string(record.OutboxKindStepRecord))
				if len(rows) != walk.writes {
					t.Fatalf("the walk queued %d step record rows, want %d", len(rows), walk.writes)
				}
				rated := false
				for _, row := range rows {
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(row.payload, &fields); err != nil {
						t.Fatalf("read a step record row: %v", err)
					}
					_, rating := fields["rating"]
					rated = rated || rating
					experiment, named := fields["experiment"]
					variant, varied := fields["variant"]
					switch {
					case drawn && (string(experiment) != `"planning"` || string(variant) != `"b"`):
						t.Errorf("the row %s names the experiment %s and the variant %s, want planning and b, its issue's", row.payload, experiment, variant)
					case !drawn && (named || varied):
						t.Errorf("the row %s of an issue with no group names the experiment %s and the variant %s, want neither key", row.payload, experiment, variant)
					}
				}
				if walk.from == phase.Accepting && !rated {
					t.Fatalf("no row of the retro's completion carries its rating, so this test reads no rating's write")
				}
			})
		}
	}
}
