package workflow

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// walkedWrite is one write of a step record as these tests compare it: a start or an end, the
// task, the kind and the place, the role and the type, the end's result and the step the work
// returns to (0 for none: every return of these tests goes to a step, from place 1), and the
// count of starts its id is computed from (StepID).
type walkedWrite struct {
	start      bool
	task, kind string
	step       int
	role, typ  string
	result     string
	returnedTo int
	attempt    int64
}

// assertStepWrites fails unless the step record rows key's outbox holds are want, in order, each
// carrying the id StepID gives its issue, run, task, kind, place and count of starts: so a start
// and the end of its run, which want gives one count, have one id.
func assertStepWrites(t *testing.T, pool *pgxpool.Pool, key string, generation uint64, want []walkedWrite) {
	t.Helper()
	writes := stepWrites(t, pool, key)
	got := make([]walkedWrite, len(writes))
	for i, w := range writes {
		got[i] = walkedWrite{start: isStart(w), task: w.Task, kind: w.Kind, step: w.Step, role: w.Role, typ: w.Type, result: resultOf(w)}
		if w.ReturnedTo != nil {
			got[i].returnedTo = *w.ReturnedTo
		}
		if i < len(want) {
			got[i].attempt = want[i].attempt
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the step record writes of %s =\n%s\nwant\n%s", key, describeWrites(got), describeWrites(want))
	}
	for i, w := range writes {
		if isStart(w) == isEnd(w) {
			t.Fatalf("step record write %d of %s = %+v, want a start or an end", i, key, w)
		}
		if id := StepID(key, generation, want[i].task, want[i].kind, want[i].step, want[i].attempt); w.ID != id {
			t.Fatalf("step record write %d of %s (%+v) has id %s, want %s: the id of start %d of %s/%s/%d",
				i, key, got[i], w.ID, id, want[i].attempt, want[i].task, want[i].kind, want[i].step)
		}
	}
}

func describeWrites(writes []walkedWrite) string {
	text := ""
	for _, w := range writes {
		text += fmt.Sprintf("  %+v\n", w)
	}
	return text
}

// stepStartCounts is the count of starts step_starts holds for each place of key, as
// `task/kind/place=starts`, in the order of the place.
func stepStartCounts(t *testing.T, pool *pgxpool.Pool, key string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select task, kind, place, starts from step_starts where issue = $1 order by task, kind, place`, key)
	if err != nil {
		t.Fatalf("read the starts of %s: %v", key, err)
	}
	defer rows.Close()
	var counts []string
	for rows.Next() {
		var task, kind string
		var place int
		var starts int64
		if err := rows.Scan(&task, &kind, &place, &starts); err != nil {
			t.Fatalf("scan a start count of %s: %v", key, err)
		}
		counts = append(counts, fmt.Sprintf("%s/%s/%d=%d", task, kind, place, starts))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the starts of %s: %v", key, err)
	}
	return counts
}

// The step records of an issue whose owner gave a plan are the plan's (B-7, C1): each names the
// plan's task, the step's place in it, from 1, and the role and the type the plan's rows record
// for it, and the retro and the merge are that task's too, at place 0. A start and the end of its
// run have one id, computed from the count of starts of that place (step_starts), which each start
// raises. A step the plan does not hold has no record. A task its owner stops ends the record of
// its running step stopped. An issue with no plan rows records today's steps under the task
// default, at their places in today's line, with the ids they always had (R20).
func TestTheRecordsOfAnOwnersPlanAreItsTaskAndSteps(t *testing.T) {
	implementer := func(attempt int64) walkedWrite {
		return walkedWrite{start: true, task: "a", kind: record.PlaceStep, step: 1, role: "implementer", typ: "implementation", attempt: attempt}
	}
	reviewer := func(attempt int64) walkedWrite {
		return walkedWrite{start: true, task: "a", kind: record.PlaceStep, step: 2, role: "reviewer", typ: "review", attempt: attempt}
	}
	ended := func(start walkedWrite, result string, returnedTo int) walkedWrite {
		start.start, start.result, start.returnedTo = false, result, returnedTo
		return start
	}

	t.Run("the plan implementer reviewer with one rejected review, to its merge", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		reviewRequestsChanges(t, pool, engine, root.Key)
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerReviewApproved, "", record.PhaseRow{})
		assertWalksAt(t, pool, engine, root.Key, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro})
		seedPR(t, pool, record.PullRequest{State: record.PullRequestMerged, Issue: root.Key, Repo: "sjawhar/legion", Number: 42,
			Branch: "legion/LEGION-208", HeadSHA: "head", MergeSHA: "merge", Failing: []string{}})
		move(t, pool, engine, root.Key, TriggerRetroCompleted, "", record.PhaseRow{})

		retro := walkedWrite{start: true, task: "a", kind: record.PlaceRetro, role: "implementer", attempt: 1}
		merge := walkedWrite{start: true, task: "a", kind: record.PlaceMerge, role: "merger", attempt: 1}
		assertStepWrites(t, pool, root.Key, root.Generation, []walkedWrite{
			implementer(1), ended(implementer(1), "passed", 0),
			reviewer(1), ended(reviewer(1), "failed", 1),
			implementer(2), ended(implementer(2), "passed", 0),
			reviewer(2), ended(reviewer(2), "passed", 0),
			retro, ended(retro, "passed", 0),
			merge, ended(merge, "passed", 0),
		})
		if got, want := stepStartCounts(t, pool, root.Key), []string{"a/merge/0=1", "a/retro/0=1", "a/step/1=2", "a/step/2=2"}; !slices.Equal(got, want) {
			t.Fatalf("the starts of %s = %v, want %v: each place of task a counted, and no place of the task default", root.Key, got, want)
		}
		if got := issueAt(t, pool, root.Key); got != phase.Done {
			t.Fatalf("%s stands at %s, want done after its merge", root.Key, got)
		}
	})

	t.Run("a stopped task's running record ends stopped", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		if result := applyFacts(t, pool, engine)("stop", ownersStop(t, root.Key, "a")); result.Refusal != nil {
			t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
		}
		assertStepWrites(t, pool, root.Key, root.Generation, []walkedWrite{
			implementer(1), ended(implementer(1), "passed", 0),
			reviewer(1), ended(reviewer(1), "stopped", 0),
		})
	})

	t.Run("the retro's rating reaches the record of the task's retro", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := stepRoot(t, pool, phase.Admitted)
		seedStepsPlan(t, pool, root.Key, root.Generation, "implementer", "reviewer")
		move(t, pool, engine, root.Key, TriggerGateOpened, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerImplementationReady, "", record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerReviewApproved, "", record.PhaseRow{})
		applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 1, Issue: root.Key, Role: claim.RoleImplementer,
			Claim: "implement-claim", Summary: "retro done", Commit: "head", PlanRating: 2, PlanReason: "The plan held no tester."})
		var rated []record.StepRecord
		for _, write := range stepWrites(t, pool, root.Key) {
			if write.Rating != nil {
				rated = append(rated, write)
			}
		}
		if want := StepID(root.Key, 1, "a", record.PlaceRetro, 0, 1); len(rated) != 1 || rated[0].ID != want || rated[0].Task != "a" ||
			rated[0].Kind != record.PlaceRetro || rated[0].Role != "implementer" {
			t.Fatalf("the ratings = %+v, want one on the record %s of the first run of task a's retro", rated, want)
		}
	})

	t.Run("an issue with no plan rows records today's steps of the task default", func(t *testing.T) {
		pool := migratedPool(t)
		stepRoot(t, pool, phase.Implementing)
		move(t, pool, returnEngine(), "LEGION-208", TriggerImplementationReady, "", record.PhaseRow{})
		// The implementer's run started under no count of this release (EndUncountedStep), so
		// its end is the record of count 0; the tester's start is the first of its place.
		assertStepWrites(t, pool, "LEGION-208", 1, []walkedWrite{
			{task: defaultTask, kind: record.PlaceStep, step: 2, role: "implementer", typ: "implementation", result: "passed", attempt: 0},
			{start: true, task: defaultTask, kind: record.PlaceStep, step: 3, role: "tester", typ: "testing", attempt: 1},
		})
		if got, want := stepStartCounts(t, pool, "LEGION-208"), []string{"default/step/2=0", "default/step/3=1"}; !slices.Equal(got, want) {
			t.Fatalf("the starts of LEGION-208 = %v, want %v", got, want)
		}
	})
}
