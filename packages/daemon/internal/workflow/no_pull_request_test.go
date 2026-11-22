package workflow

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// rootWithNoPullRequest is the root LEGION-208 at admitted, generation 1, whose plan names words,
// with no pull request record.
func rootWithNoPullRequest(t *testing.T, pool *pgxpool.Pool, words ...string) record.Issue {
	t.Helper()
	root := stepRoot(t, pool, phase.Admitted)
	if _, err := pool.Exec(t.Context(), "delete from pull_requests where issue = $1", root.Key); err != nil {
		t.Fatalf("delete the pull request of %s: %v", root.Key, err)
	}
	seedStepsPlan(t, pool, root.Key, root.Generation, words...)
	return root
}

// productionChecksOf is the number of production checks the store holds of key.
func productionChecksOf(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "select count(*) from production_checks where issue = $1", key).Scan(&n); err != nil {
		t.Fatalf("count the production checks of %s: %v", key, err)
	}
	return n
}

// A task whose issue has no pull request has no merge: its retro's end makes it done, with its
// task done, no merger started, Dispatch's status done queued once, and no production check, which
// opens only on a merged pull request. With a pull request record, the retro's end moves the issue
// to its merge, as today. The control is the table whose retro always moves to merging: the issue
// with no pull request stands at merging, with a merger started, waiting for a merge nothing opens.
func TestATaskWithNoPullRequestIsDoneWhenItsRetroEnds(t *testing.T) {
	t.Run("no pull request: done at the retro's end", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := rootWithNoPullRequest(t, pool, "tester")
		move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerTesterPassed, record.TaskPlace{}, record.PhaseRow{})
		assertWalksAt(t, pool, engine, root.Key, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro})
		move(t, pool, engine, root.Key, TriggerRetroCompleted, record.TaskPlace{}, record.PhaseRow{})

		if got := issueOf(t, pool, engine, root.Key); got.Phase != phase.Done {
			t.Fatalf("%s after its retro stands in %s, want done", root.Key, got.Phase)
		}
		if task := taskOf(t, pool, engine, root.Key); task.State != record.TaskDone {
			t.Fatalf("task a of %s after its retro is %s, want done", root.Key, task.State)
		}
		if starts := roleStartsOf(t, pool, root.Key, claim.RoleMerger); len(starts) != 0 {
			t.Fatalf("%s's retro wrote %d starts of the merger, want none: it has no pull request to merge", root.Key, len(starts))
		}
		var done int
		for _, write := range statusWrites(t, pool, root.Key) {
			if write.Status == "done" {
				done++
			}
		}
		if done != 1 {
			t.Fatalf("%s's retro queued %d status writes of done, want one", root.Key, done)
		}
		if n := productionChecksOf(t, pool, root.Key); n != 0 {
			t.Fatalf("%s holds %d production checks, want none: no pull request merged", root.Key, n)
		}
	})

	t.Run("a pull request record: the merge, as today", func(t *testing.T) {
		pool := migratedPool(t)
		engine := returnEngine()
		root := rootWithNoPullRequest(t, pool, "tester")
		move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
		move(t, pool, engine, root.Key, TriggerTesterPassed, record.TaskPlace{}, record.PhaseRow{})
		seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: root.Key, Repo: "sjawhar/legion", Number: 42,
			Branch: "legion/" + root.Key, HeadSHA: "head-42", Failing: []string{}})
		move(t, pool, engine, root.Key, TriggerRetroCompleted, record.TaskPlace{}, record.PhaseRow{})

		assertWalksAt(t, pool, engine, root.Key, phase.Merging, record.TaskPlace{Kind: record.PlaceMerge})
		if starts := roleStartsOf(t, pool, root.Key, claim.RoleMerger); len(starts) != 1 {
			t.Fatalf("%s's retro wrote %d starts of the merger, want one", root.Key, len(starts))
		}
	})
}
