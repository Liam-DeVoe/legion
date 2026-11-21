package grapherase

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/api"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest/lockstep"
)

// termTotalsTakeStatement is how the statement starts that takes a project's term statistics
// totals row and clears its snapshot: the erase's take before its owner locks
// (api.TakeErasedTermTotalsTx) and its bounded take under them (api.ForgetErasedTermsTx) alike.
const termTotalsTakeStatement = "insert into search_term_totals (project_key, items, refreshed_at) values ($1, 0, now())"

// erasedTermTotalsRefusal is how long TestTheEraseIsRefusedWhenAPassHoldsTheStatisticsOfAProjectOutsideItsOwners
// gives the confirmed erase to answer: its search for the rows it leaves, its locks and the read
// under them, and then the 100 ms api.ForgetErasedTermsTx waits at most for the totals row of a
// project the erase did not take before its owner locks. An erase that waited for the pass instead
// would wait for as long as the test holds the row.
const erasedTermTotalsRefusal = 10 * time.Second

// holdTermTotals holds project's term statistics totals row `for update` in a transaction of its
// own, as a refresh pass running holds it for its whole transaction (api's searchTermTotalsMode).
func holdTermTotals(t *testing.T, database *store.Store, project string) *lockstep.Hold {
	t.Helper()
	return lockstep.HoldRow(t, database, &lockstep.Hold{Name: "project " + project + "'s term statistics totals row"},
		`select 1 from search_term_totals where project_key = $1 for update`, project)
}

// countTermStats counts each of projects' term statistics as a refresh pass counts one
// (api.RefreshSearchTermStats), so each project's totals row stands.
func countTermStats(t *testing.T, database *store.Store, projects ...string) {
	t.Helper()
	for _, project := range projects {
		if err := api.RefreshSearchTermStats(context.Background(), database.Pool, project); err != nil {
			t.Fatalf("count project %s's term statistics: %v", project, err)
		}
	}
}

// TestTheEraseWaitsForARunningStatisticsPassBeforeItTakesAnOwnerLock holds a confirmed erase to
// taking the term statistics totals row of each project of its owners before its first owner lock
// (lockInStoreOrder, api.TakeErasedTermTotalsTx). A refresh pass of a project holds that row for
// its whole transaction, which production's logs show reaching 18.5 s, and every writer of an issue
// takes the issue's owner lock first and waits there while another transaction holds it, so an
// erase that waited for the pass under that lock would hold every such writer for as long.
//
// The run erases the comment seedAgreeingStore writes on a document of issue CORE-1, of project
// CORE. A pass counts CORE first, so its totals row stands, and once the run without --confirm has
// printed its digest a transaction holds that row `for update`, as a running pass holds it. The
// confirmed run is started: the test reads that it waits behind that hold, at the statement that
// takes the row. While it waits there a comment create on CORE-1 runs as the API's createComment
// orders its writes (createACommentUnderItsOwnerLock) and must commit: a run that took the row
// under its owner locks would hold CORE-1's owner lock there, and the create would wait for it until
// its lock_timeout ended it. Once the row is let go the run takes its locks, reads the rest of its
// listing, finds the digest it was given and erases the comment.
func TestTheEraseWaitsForARunningStatisticsPassBeforeItTakesAnOwnerLock(t *testing.T) {
	database := storetest.Open(t)
	seeded := seedAgreeingStore(t, database)
	countTermStats(t, database, "CORE")
	listing := dryRun(t, database, commentNodeKind, seeded.comment)
	pass := holdTermTotals(t, database, "CORE")

	pid, answered := startConfirmedErase(t, database, commentNodeKind, seeded.comment, listing)
	if early, done := storetest.WaitForQueuedBehind(t, pass.Tx, pid, answered, lockstep.Wait); done {
		t.Fatalf("the confirmed erase answered (err %v) without waiting on %s, which a running refresh pass held", early.err, pass.Name)
	}
	waited := strings.ToLower(lockstep.WaitingStatement(t, database, pid))
	t.Logf("the confirmed erase waits behind %s at %q", pass.Name, waited)
	if !strings.HasPrefix(waited, termTotalsTakeStatement) {
		t.Errorf("the confirmed erase waits behind %s at %q, which is not the statement that takes the row (it starts %q)",
			pass.Name, waited, termTotalsTakeStatement)
	}

	if err := createACommentUnderItsOwnerLock(t, database, "A remark written while the erase waited for a statistics pass."); err != nil {
		t.Fatalf("a comment create on issue CORE-1 failed while the confirmed erase of comment %s waited for %s: %v; the erase holds the issue's owner lock while it waits for the pass, and every writer of the issue waits for it",
			seeded.comment, pass.Name, err)
	}

	pass.Release()
	var answer eraseAnswer
	select {
	case answer = <-answered:
	case <-time.After(lockstep.Wait):
		t.Fatalf("the confirmed erase did not answer within %s of the release of %s", lockstep.Wait, pass.Name)
	}
	if answer.err != nil {
		t.Fatalf("the confirmed erase failed once %s was let go: %v", pass.Name, answer.err)
	}
	requireTheCommentErased(t, database, answer.erasure, seeded.comment)
}

// TestTheEraseIsRefusedWhenAPassHoldsTheStatisticsOfAProjectOutsideItsOwners holds the erase to
// the bound on its wait under its owner locks for the totals row of a project it did not take
// before them (api.ForgetErasedTermsTx). Every item the erase changes stands under an owner it
// holds, so no such project is expected; one stands here as a queue row of the erased comment
// written under issue OPS-1, of another project, OPS. The term statistics read that row as the
// comment's change in OPS, so the erase takes the erased words out of OPS's statistics too, under
// its locks, and has taken OPS's totals row before none of them.
//
// A pass counts CORE and OPS first, so both totals rows stand, the row is planted, and once the run
// without --confirm has printed its digest a transaction holds OPS's totals row `for update`, as a
// running pass holds it. The confirmed run must answer within erasedTermTotalsRefusal with the
// refusal that names OPS and says nothing was removed, on Postgres's lock_not_available (55P03),
// and the comment, its node and no record of an erase stand afterwards. A run that waited for the
// pass would hold every writer of CORE-1 for as long as the pass held the row. Once the row is let
// go the erase run again takes OPS's row at once, erases the comment and names both projects as
// those it took the erased words out of.
func TestTheEraseIsRefusedWhenAPassHoldsTheStatisticsOfAProjectOutsideItsOwners(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	other := seedQuotingDocument(t, database, "OPS")
	// Counted before the row is planted: a pass of OPS drains the queue rows of OPS it sees.
	countTermStats(t, database, "CORE", "OPS")
	if _, err := database.Pool.Exec(ctx, `
		insert into search_term_queue (kind, issue_key, item_id) values ('comment', $1, $2::uuid)
	`, other.issue, seeded.comment); err != nil {
		t.Fatalf("plant a queue row of comment %s under issue %s: %v", seeded.comment, other.issue, err)
	}
	listing := dryRun(t, database, commentNodeKind, seeded.comment)
	pass := holdTermTotals(t, database, "OPS")

	answered := make(chan eraseAnswer, 1)
	go func() {
		erasure, err := Erase(context.Background(), database.Pool.Config().ConnString(), commentNodeKind, seeded.comment,
			listing.Digest, time.Millisecond)
		answered <- eraseAnswer{erasure, err}
	}()
	var answer eraseAnswer
	select {
	case answer = <-answered:
	case <-time.After(erasedTermTotalsRefusal):
		t.Fatalf("the confirmed erase of comment %s did not answer within %s while %s was held: it waits for the pass under its owner locks rather than refuse",
			seeded.comment, erasedTermTotalsRefusal, pass.Name)
	}
	var timedOut *pgconn.PgError
	if answer.err == nil || !errors.As(answer.err, &timedOut) || timedOut.Code != "55P03" ||
		!strings.Contains(answer.err.Error(), "the erase changed items of project OPS") ||
		!strings.Contains(answer.err.Error(), "nothing was removed") {
		t.Fatalf("the confirmed erase of comment %s answered %v while %s was held, want the refusal naming project OPS on lock_not_available (55P03)",
			seeded.comment, answer.err, pass.Name)
	}
	t.Logf("the confirmed erase was refused: %v", answer.err)
	var nodeStands, commentStands bool
	var erasures int64
	if err := database.Pool.QueryRow(ctx, `
		select exists (select 1 from graph_nodes where kind = $1 and id = $2),
		       exists (select 1 from comments where id::text = $2),
		       (select count(*) from graph_erasures where node_kind = $1 and node_id = $2)
	`, commentNodeKind, seeded.comment).Scan(&nodeStands, &commentStands, &erasures); err != nil {
		t.Fatalf("read what the refused erase left: %v", err)
	}
	if !nodeStands || !commentStands || erasures != 0 {
		t.Fatalf("after the refused erase the comment's node row stands: %t, its comments row: %t, and %d erasure(s) name it; want both standing and none",
			nodeStands, commentStands, erasures)
	}

	pass.Release()
	erasure, err := confirmedErase(t, database, commentNodeKind, seeded.comment, time.Millisecond)
	if err != nil {
		t.Fatalf("erase comment %s again once %s was let go: %v", seeded.comment, pass.Name, err)
	}
	requireTheCommentErased(t, database, erasure, seeded.comment)
	if !slices.Equal(erasure.Recounted, []string{"CORE", "OPS"}) {
		t.Errorf("the erase run again answered %v as the projects it took the erased words out of, want [CORE OPS]", erasure.Recounted)
	}
}
