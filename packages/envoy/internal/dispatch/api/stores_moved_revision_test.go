package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/storesmove"
	"github.com/sjawhar/envoy/internal/dispatch/storesmove/storesmovetest"
)

// TestARevisionReadBeforeTheMoveIsRefusedAfterIt reads R32's fourth rule across the move at boot:
// a revision of an issue's advances set that the release before answered, the old seq of the
// newest tuple of the set in priority_tuples, is never taken as current once the move has run. The
// old tables' sequence is set above the graph's, so a move that gave the moved tuples the next
// seqs of the graph without drawing past the greatest old seq would hand the issue's newest tuple
// the very seq the release before answered. After the move, the issue answers a revision above
// every old seq, its advances set as it was, and a whole-set write carrying the old revision is
// refused 409 ADVANCES_REVISION, naming the revision the store holds.
func TestARevisionReadBeforeTheMoveIsRefusedAfterIt(t *testing.T) {
	ctx := context.Background()
	handler, database := newTestHandlerWithStore(t)
	pool := database.Pool
	createTestProject(t, handler, "REV")
	issue := createAdvancingIssue(t, handler, map[string]any{"project": "REV", "title": "Advances a goal", "force": true}, "alice")

	var graphSeq int64
	if err := pool.QueryRow(ctx, `select last_value from graph_tuples_seq_seq`).Scan(&graphSeq); err != nil {
		t.Fatalf("read the graph's sequence: %v", err)
	}
	if _, err := pool.Exec(ctx, `select setval(pg_get_serial_sequence('priority_tuples', 'seq'), $1)`, graphSeq+2); err != nil {
		t.Fatalf("set the old sequence above the graph's: %v", err)
	}
	who := model.Actor{Kind: "user", ID: "alice", Proven: true}
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	storesmovetest.CreatePriority(t, pool, "first-goal", map[string]string{"title": "First goal", "rank": "a0"}, who, at)
	storesmovetest.CreatePriority(t, pool, "second-goal", map[string]string{"title": "Second goal", "rank": "a1"}, who, at)
	storesmovetest.Advance(t, pool, issue.Key, "first-goal", true, who, at.Add(time.Minute))
	var before, greatest int64
	if err := pool.QueryRow(ctx, `
		select (select max(seq) from priority_tuples where from_kind = 'issue' and from_id = $1 and kind = 'advances'),
		       (select max(seq) from priority_tuples)
	`, issue.Key).Scan(&before, &greatest); err != nil {
		t.Fatalf("read the revision the release before answered: %v", err)
	}

	if _, err := storesmove.Move(ctx, pool.Config().ConnString(), storesmove.Options{Batch: 2}); err != nil {
		t.Fatalf("move: %v", err)
	}
	after := readTestIssue(t, handler, issue.Key)
	if after.AdvancesRevision <= greatest {
		t.Fatalf("%s answers the revision %d after the move, want one above %d, the greatest seq of priority_tuples, which the release before answered %d of",
			issue.Key, after.AdvancesRevision, greatest, before)
	}
	if got := advanceEntries(after.Advances); !slices.Equal(got, []string{"first-goal own"}) {
		t.Fatalf("%s advances %v after the move, want first-goal alone", issue.Key, got)
	}

	stale := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/issues/"+issue.Key, map[string]any{
		"advances": []string{"first-goal", "second-goal"}, "advances_revision": before,
	}, "alice")
	code, text := refusalOf(t, stale)
	if stale.Code != http.StatusConflict || code != codeAdvancesRevision {
		t.Fatalf("a whole set carrying the revision read before the move, %d: status=%d code=%s error=%q, want 409 %s",
			before, stale.Code, code, text, codeAdvancesRevision)
	}
	if !strings.Contains(text, fmt.Sprint(after.AdvancesRevision)) {
		t.Fatalf("the refusal is %q, want it to name the revision the store holds, %d", text, after.AdvancesRevision)
	}
	t.Logf("the revision %d read before the move is refused after it: %s", before, text)
}
