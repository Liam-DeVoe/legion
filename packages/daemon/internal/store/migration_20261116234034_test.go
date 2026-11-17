package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// phaseReviewedHeadVersion is the version of the migration this file tests, so the test can
// migrate through everything before it whatever lands beside it.
const phaseReviewedHeadVersion = 20261116234034

// 20261116234034 adds the commit the reviewer's own newest deciding review read
// (record.PhaseRow.ReviewedHead), which no entry into a phase clears. The release before kept no
// such commit, so every reviewer's row the upgrade finds reads none, and the next round of an issue
// already in review reads the whole change as it did before; the decision beside it is left as it
// was. A commit written after it reads back as it was written, through a store reopened as a
// daemon restart reopens it.
func TestThePhaseReviewedHeadMigrationLeavesEveryRowWithNoCommitAndTheCommitRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, phaseReviewedHeadVersion-1)
	for _, seed := range []string{
		`insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
			values ('LEGION-208', 'LEGION-208', 'LEGION', 'root', 'reviewing', 1, 'needs_review', 'A', 1)`,
		`insert into phases (issue, role, claim, handoff_commit, rounds, verdict, decision, entry, entry_task)
			values ('LEGION-208', 'reviewer', 'legion-LEGION-208-reviewer', '', 0, '',
			'{"state": "changes_requested", "head": "1111111111111111111111111111111111111111"}', 1, 'Continue root. Issue: LEGION-208. Phase: reviewing.')`,
	} {
		if _, err := store.Pool().Exec(ctx, seed); err != nil {
			t.Fatalf("record before the migration: %v", err)
		}
	}
	_, err := store.Pool().Exec(ctx, "update phases set reviewed_head = 'x'")
	if err == nil || !strings.Contains(err.Error(), "reviewed_head") {
		t.Fatalf("writing reviewed_head before the migration = %v, want the column to be unknown: the fixture is not the old shape", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the reviewed commit: %v", err)
	}
	reviewerOf := func(st *Store) record.PhaseRow {
		t.Helper()
		var rows []record.PhaseRow
		if err := pgx.BeginFunc(ctx, st.Pool(), func(tx pgx.Tx) error {
			var err error
			rows, err = record.NewStore().Phases(ctx, tx, "LEGION-208")
			return err
		}); err != nil {
			t.Fatalf("read the phases rows of LEGION-208: %v", err)
		}
		return record.PhaseRowOf(rows, claim.RoleReviewer)
	}
	found := reviewerOf(store)
	if found.Decision == nil || found.Decision.State != "changes_requested" || found.Entry != 1 {
		t.Fatalf("the reviewer's row after the migration = decision %+v at entry %d, want the row the release before left", found.Decision, found.Entry)
	}
	if found.ReviewedHead != "" {
		t.Fatalf("the reviewer's row after the migration reads the reviewed commit %q, want none: the release before kept none", found.ReviewedHead)
	}

	const reviewed = "2222222222222222222222222222222222222222"
	want := found
	want.ReviewedHead = reviewed
	if err := pgx.BeginFunc(ctx, store.Pool(), func(tx pgx.Tx) error {
		return record.NewStore().PutPhase(ctx, tx, want)
	}); err != nil {
		t.Fatalf("put the reviewer's row with the commit its review read: %v", err)
	}
	for _, st := range []*Store{store, reopenStore(t, store)} {
		if got := reviewerOf(st).ReviewedHead; got != reviewed {
			t.Fatalf("the reviewer's row reads the reviewed commit %q, want %q as it was written", got, reviewed)
		}
	}
}
