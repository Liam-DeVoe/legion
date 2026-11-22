// pull_request_identity_test.go holds the record of a pull request's former heads to one identity
// of the pull request under every name its repository goes by: a record a writer appended under a
// name, before or after Dispatch learned that its repository was renamed, is applied to the same
// history as every other record of that pull request, and a close under any name ends that one
// history's lifecycle, whatever order the records commit in.
package delivery

import (
	"context"
	"slices"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// formerHeadsRecordsOf is how many records the writes of pull_request_heads appended for pull
// request number, under any name, that no settle has applied yet.
func formerHeadsRecordsOf(t *testing.T, ctx context.Context, pool *store.Pool, number int) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `select count(*) from pull_request_former_heads_moves where number = $1`, number).Scan(&count); err != nil {
		t.Fatalf("count the records of #%d: %v", number, err)
	}
	return count
}

// TestRecordsWrittenBeforeARenameIsKnownReachThePullRequestsHistory: #940 is stored under the
// repository's old name, open at H1, and moves to H2 there; both writes append their records, and
// no settle applies them yet. The canonical row is then written at H3 and the old row deleted, as
// the re-key does, and only then are the records applied. Whether Dispatch knew of the rename
// before the writes or learned it between them and their application, the records of both names
// are of one pull request: its history holds H1 and H2, the heads it left, under the canonical
// name. Where each name's records were applied apart before the rename was known, the re-key joins
// both histories: H1, H2 and H3.
func TestRecordsWrittenBeforeARenameIsKnownReachThePullRequestsHistory(t *testing.T) {
	const h1, h2, h3 = "9191919191919191919191919191919191919191", "9292929292929292929292929292929292929292", "9393939393939393939393939393939393939393"
	const h4 = "9494949494949494949494949494949494949494"
	for _, row := range []struct {
		name          string
		number        int
		known, folded bool
		want          []string
	}{
		{"the rename is known before the writes", 940, true, false, []string{h1, h2}},
		{"the rename is learned between the writes and their application", 941, false, false, []string{h1, h2}},
		{"each name's records were applied apart before the rename was learned", 942, false, true, []string{h1, h2, h3}},
	} {
		t.Run(row.name, func(t *testing.T) {
			pool, ctx := deliveryTestPool(t)
			issue := seedGadgetsIssue(t, ctx, pool, row.number)
			if row.known {
				seedRenamedGadgets(t, ctx, pool)
			}
			exec := func(statement string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, statement, args...); err != nil {
					t.Fatalf("%s: %v", statement, err)
				}
			}
			exec(`insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ($1, $2, $3, 'open', $4)`, gadgetsOldRepo, row.number, h1, issue)
			exec(`update pull_request_heads set head_sha = $3 where repo = $1 and number = $2`, gadgetsOldRepo, row.number, h2)
			var queued int
			if err := pool.QueryRow(ctx, `select count(*) from pull_request_former_heads_moves where number = $1`, row.number).Scan(&queued); err != nil {
				t.Fatalf("count the records of #%d: %v", row.number, err)
			}
			if queued < 2 {
				t.Fatalf("the two writes of #%d under %s appended %d records, want two: this test holds nothing queued", row.number, gadgetsOldRepo, queued)
			}
			canonicalHead := h3
			if row.folded {
				foldedFormerHeadsOf(t, ctx, pool, gadgetsOldRepo, row.number)
				exec(`insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ($1, $2, $3, 'open', $4)`, gadgetsRepo, row.number, h3, issue)
				exec(`update pull_request_heads set head_sha = $3 where repo = $1 and number = $2`, gadgetsRepo, row.number, h4)
				foldedFormerHeadsOf(t, ctx, pool, gadgetsRepo, row.number)
				canonicalHead = h4
			}
			if !row.known {
				seedRenamedGadgets(t, ctx, pool)
			}
			if !row.folded {
				exec(`insert into pull_request_heads (repo, number, head_sha, state, issue_key) values ($1, $2, $3, 'open', $4)`, gadgetsRepo, row.number, canonicalHead, issue)
			}
			exec(`delete from pull_request_heads where repo = $1 and number = $2`, gadgetsOldRepo, row.number)
			foldedFormerHeadsOf(t, ctx, pool, gadgetsRepo, row.number)
			foldedFormerHeadsOf(t, ctx, pool, gadgetsOldRepo, row.number)
			got := foldedFormerHeadsOf(t, ctx, pool, gadgetsRepo, row.number)
			if !slices.Equal(got, row.want) {
				t.Fatalf("#%d under %s holds the former heads %v once every record is applied, want %v: a record written under %s is of the same pull request", row.number, gadgetsRepo, got, row.want, gadgetsOldRepo)
			}
			if left := formerHeadsRecordsOf(t, ctx, pool, row.number); left != 0 {
				t.Fatalf("%d records of #%d are left after both names were applied, want none", left, row.number)
			}
		})
	}
}

// TestACloseUnderTheOldNameThatCommitsLateEndsTheLifecycleItClosed: #947 is stored under the old
// name, where it left 7171… and stands at 7272…, and read through under the canonical name at
// 7373…; both names' records are applied. The old row's close is then written and held open, and
// meanwhile the canonical row moves to 7474… and on to 7575…, both committed and applied: 7474… is
// a former head then. Once the close commits and is applied, it ends the lifecycle it closed
// under every name, by the place it was written at, so the heads reached before it go and 7474…,
// reached after it, stays; no record is left.
func TestACloseUnderTheOldNameThatCommitsLateEndsTheLifecycleItClosed(t *testing.T) {
	const h4, h5 = "7474747474747474747474747474747474747474", "7575757575757575757575757575757575757575"
	rig := newReKeyRig(t, 947, "7171717171717171717171717171717171717171", "7272727272727272727272727272727272727272", "7373737373737373737373737373737373737373")
	if queued := formerHeadsRecordsOf(t, rig.ctx, rig.pool, rig.number); queued == 0 {
		t.Fatalf("the writes of #%d appended no record: this test holds nothing queued", rig.number)
	}
	rig.formerHeadsOf(gadgetsRepo)
	closing, err := rig.pool.Begin(rig.ctx)
	if err != nil {
		t.Fatalf("begin the close: %v", err)
	}
	defer closing.Rollback(context.Background())
	if _, err := closing.Exec(rig.ctx, `update pull_request_heads set state = 'closed' where repo = $1 and number = $2`, gadgetsOldRepo, rig.number); err != nil {
		t.Fatalf("close %s#%d: %v", gadgetsOldRepo, rig.number, err)
	}
	for _, head := range []string{h4, h5} {
		if _, err := rig.pool.Exec(rig.ctx, `update pull_request_heads set head_sha = $3 where repo = $1 and number = $2`, gadgetsRepo, rig.number, head); err != nil {
			t.Fatalf("move %s#%d to %s: %v", gadgetsRepo, rig.number, head, err)
		}
	}
	if before := rig.formerHeadsOf(gadgetsRepo); !slices.Contains(before, h4) {
		t.Fatalf("#%d holds the former heads %v while the close is open, want 7474… among them: this test holds no head of the later lifecycle", rig.number, before)
	}
	if err := closing.Commit(rig.ctx); err != nil {
		t.Fatalf("commit the close: %v", err)
	}
	if after := rig.formerHeadsOf(gadgetsRepo); !slices.Equal(after, []string{h4}) {
		t.Fatalf("#%d holds the former heads %v once the earlier close committed, want 7474… alone: the close ends what was reached before it and nothing after", rig.number, after)
	}
	if left := formerHeadsRecordsOf(t, rig.ctx, rig.pool, rig.number); left != 0 {
		t.Fatalf("%d records of #%d are left after the close was applied, want none", left, rig.number)
	}
}
