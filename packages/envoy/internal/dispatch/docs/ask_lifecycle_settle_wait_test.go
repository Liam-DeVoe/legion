package docs

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/testdeadline"
)

// lateAppendStall is how long the test holds a room's durable append back behind the document's
// lock once it waits there: longer than the three seconds the close once gave its wait for the
// rooms' appends, and than three attempts of three seconds each, so a bound of that size on the
// wait ends it while the database still stores the append, however the attempts retry it.
const lateAppendStall = 12 * time.Second

// The close waits for its rooms' durable appends as long as its caller's context allows. A browser
// edit whose append waits behind another session's hold of the document's lock, past the three
// seconds the wait once had, is waited for: once the lock goes, the append lands, the settlement
// runs, and the ask block written before it is an ask, credited to its writer, for the close to
// withdraw.
func TestSettlingAnIssuesDocumentsWaitsForAnAppendTheDatabaseStoresLate(t *testing.T) {
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	service, artifactID := newTestService(t)
	service.settle = time.Hour
	seedServiceText(t, service, artifactID, "# Decision\n\nContext.\n")
	actor := model.Actor{Kind: "session", ID: "late-append-session"}
	writeThroughLedger(t, service, artifactID, actor, freshBlock, withoutVersion)

	// Another session holds the document's lock, which every append of the room takes first
	// (appendRoomTx), so the browser edit's append waits in the database until it goes.
	holder, err := pgx.Connect(ctx, service.store.Pool.Config().ConnString())
	if err != nil {
		t.Fatalf("connect the session that holds the document's lock: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close(context.WithoutCancel(ctx)) })
	if _, err := holder.Exec(ctx, `select pg_advisory_lock(hashtext($1))`, artifactID); err != nil {
		t.Fatalf("hold the document's lock: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if _, err := holder.Exec(context.WithoutCancel(ctx), `select pg_advisory_unlock(hashtext($1))`, artifactID); err != nil {
			t.Errorf("release the document's lock: %v", err)
		}
	}
	t.Cleanup(release)
	editLiveTree(t, service, artifactID, replaceRun("Context.", "Context, edited."))
	awaitAppendLandedOrQueued(t, service, artifactID)
	if !service.hasDurableAppend(artifactID) {
		t.Fatal("the browser edit's append landed though the document's lock was held, so nothing here waits")
	}

	started := time.Now()
	settled := make(chan error, 1)
	go func() { settled <- service.SettleIssueDocuments(store.WithTransactionTracking(ctx), "DOC-1") }()
	select {
	case err := <-settled:
		t.Fatalf("SettleIssueDocuments returned %v %s after it began, while the room's append still waited on the document's lock: a bound of its own ended the wait",
			err, time.Since(started).Round(time.Millisecond))
	case <-time.After(lateAppendStall):
	}
	release()
	select {
	case err := <-settled:
		if err != nil {
			t.Fatalf("settling the issue's documents once the append landed: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("SettleIssueDocuments did not return by the test's deadline once the document's lock went: %v", ctx.Err())
	}
	ask := blockAskOn(t, service, artifactID, "fresh")
	if ask.State != "open" || ask.Author != actor {
		t.Fatalf("the settled block's ask is %s by %+v, want open by %+v", ask.State, ask.Author, actor)
	}
	requireSettledAtLatestVersion(t, service, artifactID)
}
