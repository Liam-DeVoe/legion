package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// clockReads sets engine's clock to at for every fact it applies from here on. The test engine's
// clock reads one moment (testEngine), where each fact of a live tree comes at a moment of its own,
// and an entry is raised at the moment its start reads (record.PhaseRow.EntryRaisedAt, which enter
// writes from this clock).
func clockReads(engine *Engine, at time.Time) {
	engine.cfg.Clock = func() time.Time { return at }
}

// readmitsTheTree is the re-admission of LEGION-208's root LEGION-207 at at: the engine's clock
// reads at from here on, and the tree's generation is cleared (record.Store.ClearTreeGeneration),
// as admission's re-admission clears it.
func readmitsTheTree(t *testing.T, pool *pgxpool.Pool, engine *Engine, at time.Time) {
	t.Helper()
	clockReads(engine, at)
	applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		return record.NewStore().ClearTreeGeneration(ctx, tx, "LEGION-207", at)
	})
}

// promotesTheReviewer is the promotion's start of LEGION-208's reviewer, mid-phase, at at
// (StartPromotedWorker), which raises the reviewer's entry at that moment.
func promotesTheReviewer(t *testing.T, pool *pgxpool.Pool, engine *Engine, at time.Time) {
	t.Helper()
	clockReads(engine, at)
	member := issueRecord(t, pool, "LEGION-208")
	applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
		return engine.StartPromotedWorker(ctx, tx, *member, claim.RoleReviewer)
	})
}

// mergeStateUnknown records GitHub's answer `unknown` for headOne, LEGION-208's head, which GitHub
// gives while it computes the merge state: an approval of that head waits on GitHub's next answer
// rather than ending its round (classify.RoundWaitsOnMergeState), so the issue stays in reviewing.
// A re-admission keeps it, as it keeps the pull request's mergeability beside it
// (record.Store.ClearTreeGeneration).
func mergeStateUnknown(t *testing.T, apply func(string, intake.Fact) intake.Result) {
	t.Helper()
	apply("merge-state-unknown", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateUnknown})
}

// A verdict applied between its tree's re-admission and the promotion that starts the reviewer
// again moves no commit (record.PhaseRow.ReviewedHead): the clear emptied the reviewer's row of
// its entry's task, and only a verdict while the row holds one moves the commit, so the first
// review of the new generation reads the whole change. The verdict still decides its round: here
// it approves the pull request's head while GitHub is still computing that head's merge state, so
// the approval waits on GitHub's answer and keeps the issue in reviewing, and the promotion starts
// the reviewer on the resume task of that phase, which names no commit.
func TestAReviewFromBeforeAReadmissionAppliedBeforeThePromotionMovesNoCommit(t *testing.T) {
	pool, engine, apply := reviewedChild(t)
	mergeStateUnknown(t, apply)
	entered := engine.now()
	readmitsTheTree(t, pool, engine, entered.Add(2*time.Hour))
	apply("late-approval", reviewAt(2, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the late approval the issue is in %s, want reviewing: the approval waits on GitHub's merge state", got)
	}
	if row := reviewerRow(t, pool); row.Decision == nil || row.Decision.State != "approved" || row.Decision.Head != headOne {
		t.Fatalf("after the late approval the reviewer's row holds the decision %+v, want the approval at %s: the verdict reached the write", row.Decision, headOne)
	}
	if got := reviewerRow(t, pool).ReviewedHead; got != "" {
		t.Fatalf("after the late approval the reviewer's row reads the reviewed commit %q, want none: the re-admission cleared the generation and its entry's task", got)
	}
	promotesTheReviewer(t, pool, engine, entered.Add(3*time.Hour))
	want := "Continue child. Issue: LEGION-208. Phase: reviewing. Working copy: branch legion/LEGION-208. Resume the existing phase work." + reviewedPartOf("none") + reviewEntryPartOf(reviewerRow(t, pool).Entry)
	if task := reviewerRow(t, pool).EntryTask; task != want {
		t.Fatalf("the promoted reviewer's first task = %q, want %q: the first review of the new generation reads the whole change", task, want)
	}
}

// After the promotion started the reviewer again, the row holds the promoted entry's task, and a
// verdict records the commit it names, though it comes the very moment the daemon raised that
// entry: no time is compared. A person's request for changes then ends the promoted round (it
// decides the round and moves no commit), and the next round's task names the commit the verdict
// recorded.
func TestAVerdictAfterThePromotionRecordsItsCommit(t *testing.T) {
	pool, engine, apply := reviewedChild(t)
	mergeStateUnknown(t, apply)
	entered := engine.now()
	entry := reviewerRow(t, pool).Entry
	readmitsTheTree(t, pool, engine, entered.Add(2*time.Hour))
	if cleared := reviewerRow(t, pool); cleared.ReviewedHead != "" || cleared.EntryTask != "" {
		t.Fatalf("after the re-admission the reviewer's row reads the reviewed commit %q and the task %q, want neither: the re-admission clears the generation", cleared.ReviewedHead, cleared.EntryTask)
	}
	promotesTheReviewer(t, pool, engine, entered.Add(3*time.Hour))
	promoted := reviewerRow(t, pool)
	if promoted.Entry != entry+1 || !promoted.EntryRaisedAt.Equal(entered.Add(3*time.Hour)) || !endsNamingNoCommit(promoted.EntryTask, promoted.Entry) {
		t.Fatalf("the promoted reviewer is on entry %d raised at %s with the task %q, want entry %d raised at %s and a task naming no reviewed commit",
			promoted.Entry, promoted.EntryRaisedAt, promoted.EntryTask, entry+1, entered.Add(3*time.Hour))
	}
	apply("approval", reviewAt(2, "approved", headOne))
	if got := issuePhase(t, pool); got != phase.Reviewing {
		t.Fatalf("after the approval the issue is in %s, want reviewing: the approval waits on GitHub's merge state", got)
	}
	if got := reviewerRow(t, pool).ReviewedHead; got != headOne {
		t.Fatalf("after the approval the reviewer's row on entry %d reads the reviewed commit %q, want %s", promoted.Entry, got, headOne)
	}
	apply("person-requests-changes", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 3, State: "changes_requested", CommitID: headOne,
		Author: "a-writer", AuthorCanWrite: true, Body: "rename it", SubmittedAt: entered.Add(5 * time.Hour)})
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the person's request for changes the issue is in %s, want implementing", got)
	}
	backToReviewing(t, pool, apply, 3)
	task := reviewerRow(t, pool).EntryTask
	readsReviewingTask(t, "the next round", task, 3)
	if !strings.Contains(task, reviewedPartOf(headOne)) {
		t.Fatalf("the next round's task = %q, want it to name %s, the commit the promoted reviewer's verdict named", task, headOne)
	}
}
