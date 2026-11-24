package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// storeRows is the number of rows of every table of the store.
func storeRows(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select table_name from information_schema.tables
		where table_schema = current_schema() and table_type = 'BASE TABLE' order by table_name`)
	if err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(tables) == 0 {
		t.Fatalf("list the tables = %q, %v: want the store's tables", tables, err)
	}
	counts := map[string]int{}
	for _, table := range tables {
		var n int
		if err := pool.QueryRow(t.Context(), `select count(*) from `+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// settledRows is storeRows after the boot's pass that comes before any fact the daemon applies
// (bootBefore, which applyFacts runs too), so the rows a delivery writes are its own.
func settledRows(t *testing.T, pool *pgxpool.Pool, engine *Engine) map[string]int {
	t.Helper()
	bootBefore(t, pool, engine)
	return storeRows(t, pool)
}

// A give of the version in force applies nothing whatever its id: under the id the release before
// named it by (`plan:<issue>:<generation>:<document>:<version>`, api's registerPlan), which the
// store marked when it took the version, and under an id the store never marked, one that also
// names the giver, as a route that named a registration so would give it. Each is answered as
// taken, with no refusal and no write, and its id is marked: a daemon of the release before, after
// a way back, reads each as a give already applied.
//
// The fault it catches is the version in force judged as a later version, as main judged a give
// under an id never marked (its control: versionRefusal's sameVersion false, under which each give
// again is refused 409 PLAN_VERSION_OLD).
func TestAGiveOfTheVersionInForceAppliesNothingWhateverItsID(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	recordedAt(t, pool, "LEGION-208", "LEGION-208")
	given := ownersPlan(t, "LEGION-208", 1, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))
	mustTake(t, "version 1", applyFacts(t, pool, engine)(planID("LEGION-208", 1, 1), given))
	before := settledRows(t, pool, engine)

	for _, id := range []string{planID("LEGION-208", 1, 1), planID("LEGION-208", 1, 1) + ":" + string(given.GivenBy)} {
		if result := applyFacts(t, pool, engine)(id, given); !reflect.DeepEqual(result, intake.Result{}) {
			t.Errorf("version 1 given again as %s = %+v, want it answered with no refusal", id, result)
		}
		if !processed(t, pool, id) {
			t.Errorf("the give %s is not marked, want it marked for a release before", id)
		}
	}
	after := storeRows(t, pool)
	// The give under the id never marked marks it: one processed_events row more, and nothing else.
	before["processed_events"]++
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the gives again changed the rows of the store beside the one mark:\n before %v\n after  %v", before, after)
	}
}

// A give of a plan is judged on the record its fact reads, whatever its id's mark: a give that
// was taken, delivered again after its giver owns the issue no more, is refused ISSUE_OUTSIDE_TREE,
// and one delivered again after the issue moved to a later generation is refused
// PLAN_STALE_GENERATION, each writing nothing. The move comes between the two deliveries, applied
// in a transaction of its own before the second, so the second reads it. Here the sub-architect of
// LEGION-209 gives version 1 for LEGION-210, a member of the tree LEGION-208; then LEGION-209 is
// adopted into another tree, or LEGION-210 starts its next run.
//
// The fault it catches is a give answered by its id's mark before the owner and the generation are
// judged (its control: intake.ApplyFact answering a PlanGiven whose id is marked as a duplicate, as
// a release before does, under which both deliveries answer as taken).
func TestAGiveIsJudgedOnTheRecordItsSecondDeliveryReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		move func(t *testing.T, pool *pgxpool.Pool, engine *Engine, sub, target record.Issue)
		want func(target record.Issue, given intake.PlanGiven) *intake.Refusal
	}{
		{"its giver owns the issue no more",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine, sub, target record.Issue) {
				adoptInto(t, pool, engine, sub.Key, otherRoot(t, pool))
			},
			func(target record.Issue, given intake.PlanGiven) *intake.Refusal {
				return intake.IssueOutsideTree(target.Key, given.GivenBy, "register_plan")
			}},
		{"the issue moved to a later generation",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine, sub, target record.Issue) {
				target.Generation++
				seedIssue(t, pool, target)
			},
			func(target record.Issue, given intake.PlanGiven) *intake.Refusal {
				return intake.PlanStaleGeneration(target.Key, target.Generation, target.Generation+1)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			stepRoot(t, pool, phase.Implementing)
			sub := todoChild(t, pool, "LEGION-209")
			target := stepChild(t, pool, "LEGION-210", phase.Admitted, nil)
			given := ownersPlan(t, target.Key, target.Generation, 1, givenTask(t, "Build the widget.", "implementer", "reviewer"))
			given.GivenBy = claim.Token(treeRoleToken(t, sub.Key, claim.RoleArchitect))
			id := planID(target.Key, target.Generation, 1)
			apply := applyFacts(t, pool, engine)
			mustTake(t, "the sub-architect's version 1", apply(id, given))

			tc.move(t, pool, engine, sub, target)
			before := settledRows(t, pool, engine)
			result := apply(id, given)
			if want := tc.want(target, given); !reflect.DeepEqual(result.Refusal, want) || result.Duplicate {
				t.Errorf("the second delivery = %+v, want the refusal %+v", result, want)
			}
			if after := storeRows(t, pool); !reflect.DeepEqual(after, before) {
				t.Errorf("the refused delivery changed the rows of the store:\n before %v\n after  %v", before, after)
			}
		})
	}
}

// failingHandler fails every fact it is handed, after the handlers before it wrote: the
// transaction rolls back, as a fact whose apply failed does.
type failingHandler struct{}

func (failingHandler) Apply(context.Context, pgx.Tx, intake.Fact) (intake.Result, error) {
	return intake.Result{}, errors.New("the apply failed")
}

// A give of a plan delivered again writes nothing a second time and tells nobody a second time,
// whatever its first delivery came to, and answers the same: a give refused for good (an issue that
// started its work before Legion took plans), one an issue's group takes no plan for
// (PLAN_NOT_TAKEN), one in a lingering tree, one whose first apply failed and rolled back, which
// its second delivery takes and its third writes nothing of, one taken, and one taken before its
// tree began to linger, which is refused TREE_LINGERING after that. The rows of every table of the
// store, counted before and after each delivery, hold it: no plan row, no notice, no outbox row, no
// message on the issue, no mark of a refusal.
//
// The fault it catches is a delivery that writes again (its control: planGiven writing a give of
// the version in force as a later version, under which the taken give's second delivery fails as
// a version stored already).
func TestARedeliveredPlanGiveWritesNothingASecondTime(t *testing.T) {
	widget := func(t *testing.T) plan.Task { return givenTask(t, "Build the widget.", "implementer", "reviewer") }
	type delivery struct {
		writes bool
		want   *intake.Refusal
	}
	for _, tc := range []struct {
		name       string
		setup      func(t *testing.T, pool *pgxpool.Pool, engine *Engine) (string, intake.PlanGiven)
		deliveries []delivery
	}{
		{"refused for good: the issue started its work before Legion took plans",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine) (string, intake.PlanGiven) {
				root := stepRoot(t, pool, phase.Planning)
				return planID(root.Key, root.Generation, 1), ownersPlan(t, root.Key, root.Generation, 1, widget(t))
			},
			[]delivery{{false, intake.PlanRefused([]plan.Refusal{intake.StartedBeforePlans("LEGION-208")})},
				{false, intake.PlanRefused([]plan.Refusal{intake.StartedBeforePlans("LEGION-208")})}}},
		{"PLAN_NOT_TAKEN: the issue's group takes no plan from its owner",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine) (string, intake.PlanGiven) {
				root := stepRoot(t, pool, phase.Admitted)
				drawnInto(t, pool, root.Key, "planning", "b", "default")
				return planID(root.Key, root.Generation, 1), ownersPlan(t, root.Key, root.Generation, 1, widget(t))
			},
			[]delivery{{false, planNotTakenOfB("LEGION-208")}, {false, planNotTakenOfB("LEGION-208")}}},
		{"a plan given in a lingering tree",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine) (string, intake.PlanGiven) {
				root := stepRoot(t, pool, phase.Implementing)
				member := stepChild(t, pool, "LEGION-209", phase.Admitted, nil)
				leaveAs(t, pool, engine, root.Key, "done")
				return planID(member.Key, member.Generation, 1), ownersPlan(t, member.Key, member.Generation, 1, widget(t))
			},
			[]delivery{{false, lingeringPlan()}, {false, lingeringPlan()}}},
		{"a plan whose first apply failed",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine) (string, intake.PlanGiven) {
				root := stepRoot(t, pool, phase.Admitted)
				id, given := planID(root.Key, root.Generation, 1), ownersPlan(t, root.Key, root.Generation, 1, widget(t))
				before := settledRows(t, pool, engine)
				if _, err := intake.ApplyFact(context.Background(), pool, "api", id, given, engine, failingHandler{}); err == nil {
					t.Fatal("premise: the apply with a failing handler succeeded, want it failed")
				}
				if after := storeRows(t, pool); !reflect.DeepEqual(after, before) {
					t.Fatalf("premise: the failed apply left rows:\n before %v\n after  %v", before, after)
				}
				return id, given
			},
			[]delivery{{true, nil}, {false, nil}}},
		{"a plan taken",
			func(t *testing.T, pool *pgxpool.Pool, engine *Engine) (string, intake.PlanGiven) {
				root := stepRoot(t, pool, phase.Admitted)
				return planID(root.Key, root.Generation, 1), ownersPlan(t, root.Key, root.Generation, 1, widget(t))
			},
			[]delivery{{true, nil}, {false, nil}, {false, nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			id, given := tc.setup(t, pool, engine)
			for i, want := range tc.deliveries {
				before := settledRows(t, pool, engine)
				result := applyFacts(t, pool, engine)(id, given)
				if !reflect.DeepEqual(result.Refusal, want.want) || result.Duplicate {
					t.Fatalf("delivery %d = %+v, want the refusal %+v", i+1, result, want.want)
				}
				if after := storeRows(t, pool); reflect.DeepEqual(after, before) == want.writes {
					t.Fatalf("delivery %d wrote rows %v (before %v), want it to write: %v", i+1, after, before, want.writes)
				}
			}
		})
	}

	t.Run("a plan taken before its tree began to linger", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Implementing)
		member := stepChild(t, pool, "LEGION-209", phase.Admitted, nil)
		id, given := planID(member.Key, member.Generation, 1), ownersPlan(t, member.Key, member.Generation, 1, widget(t))
		mustTake(t, "version 1", applyFacts(t, pool, engine)(id, given))
		leaveAs(t, pool, engine, root.Key, "done")
		before := settledRows(t, pool, engine)
		if result := applyFacts(t, pool, engine)(id, given); !reflect.DeepEqual(result.Refusal, lingeringPlan()) {
			t.Errorf("the second delivery = %+v, want the refusal %+v: the plan runs no more", result, lingeringPlan())
		}
		if after := storeRows(t, pool); !reflect.DeepEqual(after, before) {
			t.Errorf("the refused delivery changed the rows of the store:\n before %v\n after  %v", before, after)
		}
	})
}

// lingeringPlan is the refusal of version 1 of the plan of LEGION-209 while its tree LEGION-208
// lingers.
func lingeringPlan() *intake.Refusal {
	return &intake.Refusal{Status: 409, Code: "TREE_LINGERING",
		Message: "the tree LEGION-208 is lingering after it left the workflow, so version 1 of the owner's plan of LEGION-209 changed nothing"}
}
