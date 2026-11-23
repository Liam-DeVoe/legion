package workflow

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// ownerAtItsStep records LEGION-208's tree with its child LEGION-209 at step 1 of its plan
// `implementer:owner reviewer`: the step started, which opened its run and told the child's own
// architect its owner-step, that notice gone (delivered, or finished undelivered), and the child's
// own architect's claim failed.
func ownerAtItsStep(t *testing.T, pool *pgxpool.Pool, engine *Engine) record.Issue {
	t.Helper()
	stepRoot(t, pool, phase.Implementing)
	child := stepChild(t, pool, "LEGION-209", phase.Implementing, nil)
	seedStepsAt(t, pool, child.Key, child.Generation, 1, nil, "implementer:owner", "reviewer")
	factTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		issue, err := engine.store.Issue(ctx, tx, child.Key)
		if err != nil {
			return err
		}
		return engine.StartStepRecord(ctx, tx, *issue)
	})
	if told := ownerSteps(t, pool, child.Key); len(told) != 1 {
		t.Fatalf("the child's step start told %+v, want one owner-step on the child", told)
	}
	dropNotices(t, pool, child.Key)
	seedIssuesArchitect(t, pool, child.Key, supervise.StateFailed, "")
	return child
}

// ownerFailed applies the n-th failure of key's own architect's claim: its launches never
// registered, until they ran out.
func ownerFailed(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string, n int) {
	t.Helper()
	applyOwned(t, pool, engine, fmt.Sprintf("owner-failed-%d", n), intake.ClaimFailed{Issue: key, Role: claim.RoleArchitect,
		Reason: "launch failures ran out", Cause: supervise.CauseNeverRegistered})
}

// startsRan is the outbox running every start key's outbox holds: each row is gone, as a run that
// succeeds deletes it.
func startsRan(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `delete from outbox where kind = 'supervise' and issue = $1 and payload->>'op' = 'start'`, key); err != nil {
		t.Fatalf("run the starts of %s: %v", key, err)
	}
}

// lastWorkerDied is the reason of the newest worker-died key's outbox holds.
func lastWorkerDied(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	died := noticesOf(t, pool, key, "worker-died")
	if len(died) == 0 {
		t.Fatalf("%s's outbox holds no worker-died", key)
	}
	return died[len(died)-1].Reason
}

// A child's own architect that fails while its child stands at a step of its own, which only that
// architect reports (complete_step takes the report from the child's own architect alone), is
// started again at once: no phase start comes while the child stands there, and the start the
// child's next phase start would queue (reviveArchitect) would never come. The start is the plain
// one a phase start queues (childArchitectStart), the worker-died tells the architect above that
// it runs again, and the launch's ready tells the owner its step again (the catch-up's owed step).
//
// The control is a child at a step its owner does not do: its architect's failure queues no start
// and says the next phase start brings it back, as before.
func TestAChildsOwnArchitectThatFailsAtItsOwnStepIsStartedAgainAndToldTheStep(t *testing.T) {
	t.Run("at the owner's step", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		child := ownerAtItsStep(t, pool, engine)

		ownerFailed(t, pool, engine, child.Key, 1)
		if got, want := architectStartsOf(t, pool, child.Key), []record.SuperviseRequest{plainArchitectStart(child.Generation)}; !reflect.DeepEqual(got, want) {
			t.Fatalf("the child's architect starts after its failure at its own step = %#v, want the plain start %#v", got, want)
		}
		if got, want := lastWorkerDied(t, pool, child.Key), fmt.Sprintf("LEGION-209's own architect failed at step 1 (implementer) of task a, "+
			"which only it reports: the daemon starts it again now (1 of %d at this step), and its notices come to you until it runs again", OwnerRestarts); got != want {
			t.Fatalf("the worker-died's reason = %q, want %q", got, want)
		}

		claimReady(t, pool, engine, child.Key, claim.RoleArchitect, 2)
		if told := ownerSteps(t, pool, child.Key); len(told) != 1 || told[0].Role != claim.RoleImplementer {
			t.Fatalf("the restarted owner's ready told %+v, want its owner-step of step 1 again", told)
		}
	})

	t.Run("at a worker's step", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		stepRoot(t, pool, phase.Implementing)
		child := stepChild(t, pool, "LEGION-209", phase.Reviewing, nil)
		seedStepsAt(t, pool, child.Key, child.Generation, 2, nil, "implementer:owner", "reviewer")
		seedIssuesArchitect(t, pool, child.Key, supervise.StateFailed, "")

		ownerFailed(t, pool, engine, child.Key, 1)
		if got := architectStartsOf(t, pool, child.Key); len(got) != 0 {
			t.Fatalf("the child's architect starts after its failure at the reviewer's step = %#v, want none: the next phase start brings it back", got)
		}
		if got, want := lastWorkerDied(t, pool, child.Key), "LEGION-209's own architect failed; its notices come to you until it runs again, "+
			"and the daemon starts it again at LEGION-209's next phase start"; got != want {
			t.Fatalf("the worker-died's reason = %q, want %q", got, want)
		}
	})
}

// The daemon starts a failed owner again at most OwnerRestarts times in one run of its step: a
// launch that always fails is started that many times, and then left failed, where the stranded
// list names it for the operator's retry, as the last worker-died says. A start already queued is
// the start, and counts nothing. The operator's retry of the owner's claim (intake.RestartPhase of
// the architect, what `legion claims retry` applies) tells the owner its step again with one more
// owner-step, and gives the daemon its restarts again: the next failure is started again.
func TestAFailedOwnersRestartsAtOneStepAreBoundedAndTheOperatorsRetryResetsThem(t *testing.T) {
	pool := migratedPool(t)
	engine := returnEngine()
	child := ownerAtItsStep(t, pool, engine)

	started := 0
	for n := 1; n <= OwnerRestarts+2; n++ {
		ownerFailed(t, pool, engine, child.Key, n)
		starts := len(architectStartsOf(t, pool, child.Key))
		if want := 1; n > OwnerRestarts {
			if starts != 0 {
				t.Fatalf("failure %d queued %d starts, want none: the daemon started the owner again %d times at this step already", n, starts, OwnerRestarts)
			}
		} else if starts != want {
			t.Fatalf("failure %d queued %d starts, want %d", n, starts, want)
		}
		if n == 1 {
			// A second report while the first start waits queues none beside it and counts nothing.
			ownerFailed(t, pool, engine, child.Key, 100)
			if again := len(architectStartsOf(t, pool, child.Key)); again != 1 {
				t.Fatalf("a failure while a start waits left %d starts, want the one", again)
			}
		}
		started += starts
		startsRan(t, pool, child.Key)
	}
	if started != OwnerRestarts {
		t.Fatalf("a launch that always fails was started %d times, want %d", started, OwnerRestarts)
	}
	token := claim.Token(treeRoleToken(t, child.Key, claim.RoleArchitect))
	if got, want := lastWorkerDied(t, pool, child.Key), fmt.Sprintf("LEGION-209's own architect failed at step 1 (implementer) of task a, "+
		"which only it reports, and the daemon has started it again %d times at this step: it stays failed, and the stranded list names it "+
		"for the operator's retry (legion claims retry %s); its notices come to you until it runs again", OwnerRestarts, token); got != want {
		t.Fatalf("the spent worker-died's reason = %q, want %q", got, want)
	}

	if result := applyOwned(t, pool, engine, "operator-retry", intake.RestartPhase{Issue: child.Key, Role: claim.RoleArchitect, Claim: token}); result.Refusal != nil {
		t.Fatalf("the operator's retry of the owner = %+v, want it applied", result.Refusal)
	}
	if told := ownerSteps(t, pool, child.Key); len(told) != 1 || told[0].Role != claim.RoleImplementer {
		t.Fatalf("the operator's retry told %+v, want one more owner-step of step 1", told)
	}
	ownerFailed(t, pool, engine, child.Key, OwnerRestarts+3)
	if got := architectStartsOf(t, pool, child.Key); len(got) != 1 {
		t.Fatalf("the failure after the operator's retry queued %#v, want one start: the retry gave the daemon its restarts again", got)
	}
}

// The stranded list names a child's own architect that failed while its child stands at a place
// only that architect ends — a step of its own, or its retro — with no start of it queued: nothing
// but the operator's retry starts it then. It names none where a start is queued (the daemon's
// own restart), none at a worker's step (the child's next phase start brings it back), and none at
// an acceptance the owner does, which the architect above ends with sign_off as well.
func TestStrandedNamesAFailedChildArchitectAtAPlaceOnlyItEnds(t *testing.T) {
	owners := func(key string, at phase.Phase) record.Issue {
		issue := strandedIssue(key, "LEGION-1", at)
		issue.PlaceRole = claim.RoleArchitect
		return issue
	}
	issues := []record.Issue{
		strandedIssue("LEGION-1", "LEGION-1", phase.Implementing),
		owners("LEGION-2", phase.Testing),
		owners("LEGION-3", phase.Testing),
		strandedIssue("LEGION-4", "LEGION-1", phase.Testing),
		owners("LEGION-5", phase.Accepting),
		owners("LEGION-6", phase.Retro),
	}
	stuck := strandedClaim(t, "LEGION-1", "LEGION-2", claim.RoleArchitect, supervise.StateFailed)
	restarting := strandedClaim(t, "LEGION-1", "LEGION-3", claim.RoleArchitect, supervise.StateFailed)
	worker := strandedClaim(t, "LEGION-1", "LEGION-4", claim.RoleArchitect, supervise.StateFailed)
	accepting := strandedClaim(t, "LEGION-1", "LEGION-5", claim.RoleArchitect, supervise.StateFailed)
	retro := strandedClaim(t, "LEGION-1", "LEGION-6", claim.RoleArchitect, supervise.StateFailed)
	claims := []supervise.Claim{stuck, restarting, worker, accepting, retro}
	stored := map[claim.Token]supervise.Claim{}
	for _, c := range claims {
		stored[c.Token] = c
	}
	s := &strandedStore{issues: issues, claims: stored, slots: slotsOf("LEGION-1"),
		queued: map[claim.Token][]record.QueuedSupervise{restarting.Token: {{ID: 7,
			Request: record.SuperviseRequest{Op: "start", Tree: "LEGION-1", Role: claim.RoleArchitect, Generation: 2}}}}}

	got, err := Stranded(context.Background(), nil, s, "LEGION", nil, claims, nil, strandedNow)
	if err != nil {
		t.Fatalf("Stranded: %v", err)
	}
	if want := []supervise.Claim{stuck, retro}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stranded = %v, want %v", names(got), names(want))
	}
}
