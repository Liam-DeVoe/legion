package grapherase

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/graphstore"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// TestAPersonsEraseLeavesTheKeywordFillAlone erases person:alice, who has no row of the keyword
// table (search_text_versions) and whose erase reaches none there (lockOwnersThenNode), and holds
// the erase to taking no lock on the table of the fill's cursor row (search_text_versions_fill)
// by the time it waits at its node row, the step after the one where it would hold the fill back.
// The cursor row stands, as it does once the fill has run, and alice's comment is erased first so
// that her erase goes through (TestTheEraseRefusesAPersonACommentNamesUntilItGoes). A transaction
// holds alice's node row; while the erase waits behind it the test reads, from pg_locks, every
// table the erase holds a lock on (lockstep.TablesLockedBy), as the lock-step tests read them,
// then lets go and holds the erase to having removed her. An erase that held the fill back for
// every node would hold the cursor row `for share` there, a lock on that table, and no batch of
// the fill would write for as long as the erase lasted. The erase of a comment does hold it
// (TestTheEraseWaitsForAKeywordBatchInFlightAndListsWhatItWrote).
func TestAPersonsEraseLeavesTheKeywordFillAlone(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	requireErased(t, database, commentNodeKind, seeded.comment)
	if _, err := database.Pool.Exec(ctx, `
		insert into search_text_versions_fill (id, event_id, holder, rules) values (1, 0, null, 1) on conflict (id) do nothing`); err != nil {
		t.Fatalf("create the keyword fill's cursor row as its first batch does: %v", err)
	}
	const person = "alice"
	node := lockstep.HoldRow(t, database, &lockstep.Hold{
		Name: "person alice's node row", Step: lockstep.StepNode, Taken: true, Kind: graphstore.KindPerson, ID: person,
		Statement: "select supertag from graph_nodes where kind = $1 and id = $2 for update",
	}, `select 1 from graph_nodes where kind = $1 and id = $2 for update`, graphstore.KindPerson, person)

	pid, answered := startErase(t, database, graphstore.KindPerson, person)
	if early, done := storetest.WaitForQueuedBehind(t, node.Tx, pid, answered, lockstep.Wait); done {
		t.Fatalf("the erase of person %s answered (err %v) without waiting on its node row, which another transaction held", person, early.err)
	}
	lockstep.RequireStoppedAt(t, ctx, database, pid, "the erase of person "+person, node)
	if locked := lockstep.TablesLockedBy(t, ctx, database, pid); slices.Contains(locked, "search_text_versions_fill") {
		t.Errorf("the erase of person %s holds a lock on search_text_versions_fill while it waits at its node row (it holds locks on %v): it held the keyword fill back, which writes no row of a person, and no row of which the erase of a person deletes or rewrites",
			person, locked)
	}
	node.Release()

	var answer eraseAnswer
	select {
	case answer = <-answered:
	case <-time.After(lockstep.Wait):
		t.Fatalf("the erase of person %s did not answer within %s of its node row's release", person, lockstep.Wait)
	}
	if answer.err != nil {
		t.Fatalf("the erase of person %s failed once its node row was let go: %v", person, answer.err)
	}
	if !answer.erasure.Applied {
		t.Fatalf("the erase of person %s removed nothing once its node row was let go: %q", person, answer.erasure.Blockers)
	}
}
