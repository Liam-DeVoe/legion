package daemon

// A record written before records carried their repository holds none (record.Issue.Unfilled).
// The backfill fills it (fillRepositories), with checks a correction makes none of. So no pass
// over a recorded issue's resolution corrects such a record or reads GitHub for it: a live event's
// preparation (prepareIssue), a hand-over's (prepareHandover), the follow (followListing) and the
// boot's listing, which asks for no follow of it (prepareRecorded). These tests run the daemon's
// own preparations and its one writer (workflow.Engine.ApplyRepositoryChange) over a real
// PostgreSQL and NATS, with a GitHub that names strangerRepo as strangerNamed.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/dispatch"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
)

// unfilledKey is the issue whose record these tests write with no repository.
const unfilledKey = "LEGION-1121"

// recordUnfilled records key as recordCorrectionRoot does, and then empties its repository
// columns: the record store refuses to write a record with no repository, so the row is made the
// way the migration that added the columns left every earlier row.
func recordUnfilled(t *testing.T, w *workflowRuntime, key string) {
	t.Helper()
	recordCorrectionRoot(t, w.pool, w.records, key, key, 0)
	if _, err := w.pool.Exec(context.Background(), "update issues set repo = '', default_branch = '', github_repo = '' where key = $1", key); err != nil {
		t.Fatalf("empty the repository of %s: %v", key, err)
	}
}

// applyCorrection applies fact through the one writer of a recorded issue's repository, in one
// transaction with its commit hooks, as every pass over a recorded issue's resolution does.
func applyCorrection(t *testing.T, w *workflowRuntime, fact intake.DispatchIssue) {
	t.Helper()
	scoped, committed := intake.WithCommitHooks(context.Background())
	if err := pgx.BeginFunc(scoped, w.pool, func(tx pgx.Tx) error {
		stored, err := w.records.Issue(scoped, tx, fact.Key)
		if err != nil {
			return err
		}
		return w.engine.ApplyRepositoryChange(scoped, tx, *stored, fact)
	}); err != nil {
		t.Fatalf("apply the correction of %s: %v", fact.Key, err)
	}
	committed()
}

// Every pass over a recorded issue's resolution leaves a record with no repository as it is:
// it reads GitHub for none of it, writes no repository, no repository it left and no change
// waiting, says nothing on the issue, and the boot's listing asks for no follow of it. The
// backfill then fills the record from Dispatch's resolution, at GitHub's spelling and default
// branch. Control: with the passes correcting such a record, each pass's preparation reads
// GitHub for stranger/tools, and the correction fills the record onto Stranger/Tools.
func TestNoPassCorrectsARecordWithNoRepositoryAndTheBackfillFillsIt(t *testing.T) {
	ctx := context.Background()
	implement := "implement " + strangerRepo
	for _, tc := range []struct {
		name string
		pass func(t *testing.T, w *workflowRuntime)
	}{
		{name: "a live event", pass: func(t *testing.T, w *workflowRuntime) {
			prepared, err := prepareAsDelivered(t, w, intake.DispatchIssue{Key: unfilledKey, Seq: 2, Type: "issue.updated", Status: "in_progress",
				Title: unfilledKey, Rank: "A", HandedOver: true, ResolvedRepo: strangerRepo})
			if err != nil {
				t.Fatalf("prepare the event: %v", err)
			}
			applyCorrection(t, w, prepared)
		}},
		{name: "a hand-over the outbox prepares", pass: func(t *testing.T, w *workflowRuntime) {
			handover, err := w.prepareHandover(ctx, intake.Handover{Issue: unfilledKey, ResolvedRepo: strangerRepo})
			if err != nil {
				t.Fatalf("prepare the hand-over: %v", err)
			}
			// The fact admission's consumeHandover builds from the hand-over.
			applyCorrection(t, w, intake.DispatchIssue{Key: unfilledKey, ResolvedRepo: handover.ResolvedRepo,
				RepositoryRead: handover.RepositoryRead, RepositoryRefusal: handover.RepositoryRefusal,
				RepositoryRefusalKind: handover.RepositoryRefusalKind, PreparedAgainst: handover.PreparedAgainst})
		}},
		{name: "the follow", pass: func(t *testing.T, w *workflowRuntime) {
			if left, err := w.followListing(ctx, map[string]bool{unfilledKey: true}); err != nil || len(left) != 0 {
				t.Fatalf("the follow left %v (%v), want nothing left and no error", left, err)
			}
		}},
		{name: "the boot's listing", pass: func(t *testing.T, w *workflowRuntime) {
			asked := w.followRequested.Load()
			recorded := recordedIssue(t, w, unfilledKey)
			w.prepareRecorded(ctx, intake.DispatchIssue{Key: unfilledKey, Seq: 1, Status: "in_progress", Title: unfilledKey,
				ResolvedRepo: strangerRepo}, recorded, true, nil)
			if w.followRequested.Load() != asked {
				t.Fatal("the boot's listing asked for a follow of a record with no repository, want none: the backfill fills it")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installs := &installations{}
			w, client, github := changingRig(t, installs)
			recordUnfilled(t, w, unfilledKey)
			client.show(dispatch.Issue{Key: unfilledKey, ResolvedRepo: strangerRepo, Title: unfilledKey, Status: "in_progress", Rank: "A",
				HandedOver: true, LastSeq: 1})

			tc.pass(t, w)

			if reads, named := installs.reads(implement), github.reads.Load(); reads != 0 || named != 0 {
				t.Fatalf("the pass read %s's implement App %d times and its name %d times, want neither: a record with no repository is the backfill's",
					strangerRepo, reads, named)
			}
			got := recordedIssue(t, w, unfilledKey)
			if got == nil || got.Repo != "" || got.DefaultBranch != "" || got.GitHubRepo != "" || got.RepoChange != nil {
				t.Fatalf("%s = %+v, want it still with no repository and nothing waiting", unfilledKey, got)
			}
			var corrected bool
			if err := w.pool.QueryRow(ctx, "select repo_corrected_at is not null from issues where key = $1", unfilledKey).Scan(&corrected); err != nil {
				t.Fatalf("read whether %s records a correction: %v", unfilledKey, err)
			}
			if corrected {
				t.Fatalf("%s records the moment of a correction, want none: nothing corrected it", unfilledKey)
			}
			if notes := repositoryNotes(t, w, unfilledKey); len(notes) != 0 {
				t.Fatalf("notes on %s = %q, want none", unfilledKey, notes)
			}

			if _, left, err := backfillRepositories(ctx, w.pool, w.records, client, "LEGION", t.TempDir(), w.defaultBranches, ghrepo.Renames{}, quietLogger()); err != nil || left {
				t.Fatalf("the backfill left a record = %t (%v), want every record filled", left, err)
			}
			got = recordedIssue(t, w, unfilledKey)
			if got == nil || got.Repo != strangerNamed || got.DefaultBranch != "trunk" || got.GitHubRepo != strangerNamed {
				t.Fatalf("%s after the backfill = %+v, want it filled onto %s at trunk, GitHub's spelling and default branch", unfilledKey, got, strangerNamed)
			}
		})
	}
}
