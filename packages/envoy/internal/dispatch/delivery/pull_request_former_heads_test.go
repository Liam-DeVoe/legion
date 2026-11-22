// pull_request_former_heads_test.go holds the re-key of a pull request stored under its
// repository's old name to the heads it left while open (pull_request_former_heads): whichever
// build deletes the old row, and whatever a writer of either row does meanwhile, the heads the
// pull request left reach the canonical row while it is open, and none reach it once it is closed.
package delivery

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// reKeyRig is a pull request of gadgetsRepo that arrived under both names: stored first under
// gadgetsOldRepo, where it left the head left and stands at oldHead, then read through under
// gadgetsRepo, where GitHub answers it at head, attributed to issue.
type reKeyRig struct {
	pool                  *store.Pool
	ctx                   context.Context
	issue                 string
	number                int
	left, oldHead, head   string
	refresh               func() error
	formerHeadsOf         func(repo string) []string
	canonicalStateAndHead func() (string, string)
}

func newReKeyRig(t *testing.T, number int, left, oldHead, head string) *reKeyRig {
	t.Helper()
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	issue := seedGadgetsIssue(t, ctx, pool, number)
	seedRenamedGadgets(t, ctx, pool)
	earlier := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	stale := time.Now().Add(-10 * time.Minute)
	for index, sha := range []string{left, oldHead} {
		at := earlier.Add(time.Duration(index) * time.Minute)
		if err := UpsertPullRequestHead(ctx, pool, nil, PullRequestHead{
			Repo: gadgetsOldRepo, Number: number, HeadSHA: sha,
			HeadRef: "legion/work", BaseRef: "main", State: "open", EventAt: &at, CheckedAt: &stale,
		}); err != nil {
			t.Fatalf("store the head of #%d at %s under the old name: %v", number, sha, err)
		}
	}
	fake := gadgetsFake(t, number, issue+": the work", head)
	client := fake.newTestClient()
	if written, _, err := ReadThroughCommitPulls(ctx, pool, client, nil, gadgetsRepo, head, time.Now()); err != nil || written != 1 {
		t.Fatalf("read through %s at %s = %d, %v; want one row written", gadgetsRepo, head, written, err)
	}
	rig := &reKeyRig{pool: pool, ctx: ctx, issue: issue, number: number, left: left, oldHead: oldHead, head: head}
	rig.refresh = func() error {
		return RefreshPullRequestHead(context.Background(), pool, client, nil, gadgetsOldRepo, number, time.Now())
	}
	rig.formerHeadsOf = func(repo string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `select head_sha from pull_request_former_heads where repo = $1 and number = $2 order by head_sha`, repo, number)
		if err != nil {
			t.Fatalf("read the former heads of %s#%d: %v", repo, number, err)
		}
		shas, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("read the former heads of %s#%d: %v", repo, number, err)
		}
		return shas
	}
	rig.canonicalStateAndHead = func() (string, string) {
		t.Helper()
		row := readHead(t, ctx, pool, gadgetsRepo, number)
		return row.State, row.HeadSHA
	}
	return rig
}

// refreshBehind runs the re-key of the old row while holder, a transaction of another writer, holds
// that row's lock, and answers once the re-key's delete waits behind it. The caller commits holder
// and then reads the answer.
func (r *reKeyRig) refreshBehind(t *testing.T, holder pgx.Tx) <-chan error {
	t.Helper()
	answered := make(chan error, 1)
	go func() { answered <- r.refresh() }()
	if waiting := storetest.WaitForLockWaiters(t, holder, 1, 30*time.Second); waiting < 1 {
		select {
		case err := <-answered:
			t.Fatalf("the re-key of %s#%d answered %v without waiting for the writer that holds its row", gadgetsOldRepo, r.number, err)
		default:
			t.Fatalf("the re-key of %s#%d never queued behind the writer that holds its row", gadgetsOldRepo, r.number)
		}
	}
	return answered
}

// TestTheReKeyCarriesTheHeadsAWriterLeftWhileItWaited: a writer holds #524's row under the old name
// and moves its head twice, from 7474… to 7575… and then to 7676…, while the reconcile re-keys the
// row. The re-key waits for that writer, and once it commits, the canonical row holds every head
// the pull request left: the one before, the two the writer left and the old row's last head, and
// the old name keeps none.
func TestTheReKeyCarriesTheHeadsAWriterLeftWhileItWaited(t *testing.T) {
	const left, oldHead, head = "7171717171717171717171717171717171717171", "7474747474747474747474747474747474747474", "7373737373737373737373737373737373737373"
	const moved, last = "7575757575757575757575757575757575757575", "7676767676767676767676767676767676767676"
	rig := newReKeyRig(t, 524, left, oldHead, head)
	writer, err := rig.pool.Begin(rig.ctx)
	if err != nil {
		t.Fatalf("begin the writer: %v", err)
	}
	defer writer.Rollback(context.Background())
	for _, sha := range []string{moved, last} {
		if _, err := writer.Exec(rig.ctx, `update pull_request_heads set head_sha = $3 where repo = $1 and number = $2`, gadgetsOldRepo, rig.number, sha); err != nil {
			t.Fatalf("move %s#%d to %s: %v", gadgetsOldRepo, rig.number, sha, err)
		}
	}
	answered := rig.refreshBehind(t, writer)
	if err := writer.Commit(rig.ctx); err != nil {
		t.Fatalf("commit the writer: %v", err)
	}
	if err := <-answered; err != nil {
		t.Fatalf("re-key %s#%d: %v", gadgetsOldRepo, rig.number, err)
	}
	want := []string{left, oldHead, moved, last}
	slices.Sort(want)
	if got := rig.formerHeadsOf(gadgetsRepo); !slices.Equal(got, want) {
		t.Fatalf("#%d under %s left %v after the re-key, want %v: the heads the writer left while the re-key waited are lost", rig.number, gadgetsRepo, got, want)
	}
	if got := rig.formerHeadsOf(gadgetsOldRepo); len(got) != 0 {
		t.Fatalf("the old name %s still keeps the former heads %v of #%d", gadgetsOldRepo, got, rig.number)
	}
}

// TestTheReKeyCarriesNoHeadToAPullRequestClosedWhileItWaited: a writer holds #525's row under the
// old name while the reconcile re-keys it, and meanwhile the canonical row closes, which drops its
// former heads. Once the writer commits, the closed canonical row holds no former head: a reopen
// later must not bring back heads the pull request left before it closed.
func TestTheReKeyCarriesNoHeadToAPullRequestClosedWhileItWaited(t *testing.T) {
	const left, oldHead, head = "7171717171717171717171717171717171717171", "7272727272727272727272727272727272727272", "7373737373737373737373737373737373737373"
	rig := newReKeyRig(t, 525, left, oldHead, head)
	writer, err := rig.pool.Begin(rig.ctx)
	if err != nil {
		t.Fatalf("begin the writer: %v", err)
	}
	defer writer.Rollback(context.Background())
	if _, err := writer.Exec(rig.ctx, `update pull_request_heads set checked_at = now() where repo = $1 and number = $2`, gadgetsOldRepo, rig.number); err != nil {
		t.Fatalf("stamp %s#%d: %v", gadgetsOldRepo, rig.number, err)
	}
	answered := rig.refreshBehind(t, writer)
	if _, err := rig.pool.Exec(rig.ctx, `update pull_request_heads set state = 'closed' where repo = $1 and number = $2`, gadgetsRepo, rig.number); err != nil {
		t.Fatalf("close %s#%d: %v", gadgetsRepo, rig.number, err)
	}
	if err := writer.Commit(rig.ctx); err != nil {
		t.Fatalf("commit the writer: %v", err)
	}
	if err := <-answered; err != nil {
		t.Fatalf("re-key %s#%d: %v", gadgetsOldRepo, rig.number, err)
	}
	if state, _ := rig.canonicalStateAndHead(); state != "closed" {
		t.Fatalf("%s#%d is %s after the close, want closed", gadgetsRepo, rig.number, state)
	}
	if got := rig.formerHeadsOf(gadgetsRepo); len(got) != 0 {
		t.Fatalf("#%d under %s, closed, keeps the former heads %v the re-key carried after the close", rig.number, gadgetsRepo, got)
	}
	if got := rig.formerHeadsOf(gadgetsOldRepo); len(got) != 0 {
		t.Fatalf("the old name %s still keeps the former heads %v of #%d", gadgetsOldRepo, got, rig.number)
	}
}

// TestAReKeyByTheReleaseBeforeCarriesTheHeadsItLeft: the release before this one re-keys #526 with
// a plain delete of the old row, after its write of the canonical row, and carries nothing itself.
// The canonical row holds the heads the pull request left under the old name all the same.
func TestAReKeyByTheReleaseBeforeCarriesTheHeadsItLeft(t *testing.T) {
	const left, oldHead, head = "7171717171717171717171717171717171717171", "7272727272727272727272727272727272727272", "7373737373737373737373737373737373737373"
	rig := newReKeyRig(t, 526, left, oldHead, head)
	if _, err := rig.pool.Exec(rig.ctx, `delete from pull_request_heads where repo = $1 and number = $2`, gadgetsOldRepo, rig.number); err != nil {
		t.Fatalf("delete %s#%d as the release before does: %v", gadgetsOldRepo, rig.number, err)
	}
	if got, want := rig.formerHeadsOf(gadgetsRepo), []string{left, oldHead}; !slices.Equal(got, want) {
		t.Fatalf("#%d under %s left %v after the release before deleted its old row, want %v", rig.number, gadgetsRepo, got, want)
	}
	if got := rig.formerHeadsOf(gadgetsOldRepo); len(got) != 0 {
		t.Fatalf("the old name %s still keeps the former heads %v of #%d", gadgetsOldRepo, got, rig.number)
	}
}
