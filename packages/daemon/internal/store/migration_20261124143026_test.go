package store

import (
	"context"
	"strings"
	"testing"
)

// releaseTeardownVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const releaseTeardownVersion = 20261124143026

// 20261124143026 adds issue_releases.teardown, what the teardown of the release of a pod no role
// can use has met of the runtime so far (ReleaseTeardown), and clears nothing: a mark standing
// across it is a release in flight, which keeps its mark, and it reads the empty note, since no
// teardown of this release has reached the runtime for it. The column takes the empty note,
// `reached` and `took_nothing` and no other word. A release before it names its columns in every
// write of a mark, so its own write still succeeds afterwards and reads the empty note.
//
// The negative control is the read of the column before the migration, which the database
// refuses as a column it does not know. The control of the check is the migration without it: the
// write of another word then succeeds, and this test fails.
func TestAReleaseMarkNotesItsTeardownWith20261124143026(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, releaseTeardownVersion-1)
	mark := func(issue string) error {
		_, err := store.Pool().Exec(ctx, `insert into issue_releases
			(project, issue, generation, first_row, boundary_row, last_row, started_row, role, kind)
			values ('legion', $1, 1, 18, 20, 22, 0, '', 'unowned')`, issue)
		return err
	}
	// The release in flight across the migration, which it must keep.
	if err := mark("LEGION-208"); err != nil {
		t.Fatalf("mark the release standing before the migration: %v", err)
	}
	var teardown string
	err := store.Pool().QueryRow(ctx, `select teardown from issue_releases where issue = 'LEGION-208'`).Scan(&teardown)
	if err == nil || !strings.Contains(err.Error(), "teardown") {
		t.Fatalf("reading teardown before the migration = %v, want the column to be unknown", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the teardown's note: %v", err)
	}

	if err := store.Pool().QueryRow(ctx, `select teardown from issue_releases where issue = 'LEGION-208'`).Scan(&teardown); err != nil {
		t.Fatalf("read the mark the migration found standing: %v", err)
	}
	if teardown != "" {
		t.Fatalf("the mark standing across the migration notes %q, want the empty note: no teardown of it reached the runtime", teardown)
	}
	// A release before the migration writes its mark as it did, and the mark reads the empty note.
	if err := mark("LEGION-209"); err != nil {
		t.Fatalf("mark a release with the columns a release before the migration names: %v", err)
	}
	for _, note := range []ReleaseTeardown{TeardownReached, TeardownTookNothing, TeardownNotReached} {
		if _, err := store.Pool().Exec(ctx, `update issue_releases set teardown = $1 where issue = 'LEGION-209'`, string(note)); err != nil {
			t.Fatalf("note %q on a mark: %v", note, err)
		}
	}
	_, err = store.Pool().Exec(ctx, `update issue_releases set teardown = 'taken' where issue = 'LEGION-209'`)
	if err == nil || !strings.Contains(err.Error(), "issue_releases_teardown_check") {
		t.Fatalf("noting a word the check does not name = %v, want the teardown check to refuse it", err)
	}
}
