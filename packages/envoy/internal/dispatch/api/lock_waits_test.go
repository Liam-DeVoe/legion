package api

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The tables two kinds of this package's tests hold locked, each until a call under test waits
// on it: search_term_stats, which the decision-block and related-item checks read
// (TestDecisionBlockCheckDoesNotHoldTheConditionalEditGate,
// TestRelatedCheckBoundsARequestThatEndsFirstIsRecordedCancelled), and going_requests, which a
// going stream's state read reads (TestGoingStreamCarriesAnEndingStoredBetweenItsTwoReads).
var (
	searchTermStatsLock = storetest.RelationLock("search_term_stats")
	goingRequestsLock   = storetest.RelationLock("going_requests")
)

// searchTermStatsWaiters counts the backends of this test's database that wait on
// search_term_stats, which tells a test its check has reached the table it holds locked. Its own
// database alone (storetest.Waiting): every test's database is a clone of one template, so the
// table has the same oid in each, and a check waiting in another test's database would count.
func searchTermStatsWaiters(t *testing.T, database *store.Store) int {
	t.Helper()
	return waitersOn(t, database, searchTermStatsLock)
}

// goingRequestsWaiters counts the backends of this test's database that wait on going_requests,
// as searchTermStatsWaiters counts search_term_stats's.
func goingRequestsWaiters(t *testing.T, database *store.Store) int {
	t.Helper()
	return waitersOn(t, database, goingRequestsLock)
}

// waitersOn is storetest.Waiting, failing t on a failed read.
func waitersOn(t *testing.T, database *store.Store, lock storetest.Lock) int {
	t.Helper()
	waiting, err := storetest.Waiting(context.Background(), database.Pool, lock)
	if err != nil {
		t.Fatal(err)
	}
	return waiting
}

// Each lock-wait probe of this package counts its own database's waiters alone. A waiter on the
// same table in another database of the server, as another test's check would be, counts for
// nothing, and one in its own database counts once: the second is the control that shows the
// probe sees a waiter at all, so that the first's zero is not a probe that sees nothing.
func TestEveryLockWaitProbeCountsItsOwnDatabasesWaitersAlone(t *testing.T) {
	for _, probe := range []struct {
		name  string
		lock  storetest.Lock
		count func(*testing.T, *store.Store) int
	}{
		{"search_term_stats", searchTermStatsLock, searchTermStatsWaiters},
		{"going_requests", goingRequestsLock, goingRequestsWaiters},
	} {
		t.Run(probe.name, func(t *testing.T) {
			database := storetest.Open(t)
			elsewhere := storetest.Queue(t, storetest.Open(t), probe.lock)
			if waiting := probe.count(t, database); waiting != 0 {
				t.Fatalf("the probe counted %d waiters on %s with none in its own database and backend %d waiting in another, want 0", waiting, probe.lock, elsewhere)
			}
			here := storetest.Queue(t, database, probe.lock)
			if waiting := probe.count(t, database); waiting != 1 {
				t.Fatalf("the probe counted %d waiters on %s with backend %d waiting in its own database, want 1", waiting, probe.lock, here)
			}
		})
	}
}
