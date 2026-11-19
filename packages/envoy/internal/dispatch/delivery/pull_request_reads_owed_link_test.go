package delivery

// A link by hand owes a read of the pull request that it links, also where Dispatch stores the
// head of that pull request (R5 of the results lookup). A link says nothing of the head, so that
// owe hides no stored head from the readers by issue (owedReadJoin). Each other owe says that the
// head can have moved, so it hides the head, whichever of the two owes comes first. A read that
// began before the later owe settles neither.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// linkByHand owes the read of acme/widgets#number as the link write of that pull request's URL
// does, in a transaction of its own (OweLinkedPullRequestReads).
func linkByHand(t *testing.T, ctx context.Context, pool *store.Pool, number int) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the link write: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := OweLinkedPullRequestReads(ctx, tx, []string{fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number)}); err != nil {
		t.Fatalf("owe the read of the link to #%d: %v", number, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the link write: %v", err)
	}
}

// storeReadHead makes acme/widgets#number an open pull request of issue whose head row a read of
// GitHub wrote at the head first. It answers GitHub's pull requests of acme/widgets and the client
// that reads them.
func storeReadHead(t *testing.T, ctx context.Context, pool *store.Pool, number int, first string) (*githubPulls, *fakeGitHub) {
	t.Helper()
	issue := fmt.Sprintf("HEAD-%d", number)
	fake := newFakeGitHub(t)
	github := serveGitHubPulls(t, fake, "acme/widgets")
	opened := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	github.set("acme/widgets", number, "open", first, "legion/"+issue, "Dispatch: "+issue, opened)
	if _, err := newIntake(nil, pool, nil, fake.newTestClient(), nil, nil).handlePullRequestEnvelope(ctx, nil,
		prEnvelope("acme/widgets", number, "opened", first, "legion/"+issue, "Dispatch: "+issue, opened)); err != nil {
		t.Fatalf("handle the envelope that opens #%d: %v", number, err)
	}
	if row, owed := readOwedRow(t, ctx, pool, "acme/widgets", number); owed {
		t.Fatalf("SETUP: #%d is owed a %s read after a read of GitHub wrote its head, want none", number, row.reason)
	}
	return github, fake
}

// Each of three owes comes before or after the owe of a link of the same pull request, whose head
// row a read of GitHub wrote: the owe of an event whose read failed, the owe of a read that GitHub
// refused, and the owe of a write of the release before that changes the head. With the link's
// owe alone, HeadsAttributedTo lists the stored head as known. After both owes, in either order,
// it lists the head as not read, and a refusal keeps its word. A settle at the revision of the
// first owe, the one a read that began between the two owes took, deletes nothing.
func TestALinksOwedReadHidesNoStoredHeadAndEachOtherOweHidesItInEitherOrder(t *testing.T) {
	others := []struct {
		name      string
		refusedAs string
		owe       func(t *testing.T, ctx context.Context, pool *store.Pool, number int)
	}{
		{name: "an_event_whose_read_failed", owe: func(t *testing.T, ctx context.Context, pool *store.Pool, number int) {
			if err := owePullRequestRead(ctx, pool, "acme/widgets", number, owedFailure, ""); err != nil {
				t.Fatalf("owe the failed read of an event of #%d: %v", number, err)
			}
		}},
		{name: "a_read_that_github_refused", refusedAs: model.RefusedAsAppNotInstalled, owe: func(t *testing.T, ctx context.Context, pool *store.Pool, number int) {
			if err := owePullRequestRead(ctx, pool, "acme/widgets", number, owedRefusal, model.RefusedAsAppNotInstalled); err != nil {
				t.Fatalf("owe the refused read of #%d: %v", number, err)
			}
		}},
		{name: "a_write_of_the_release_before", owe: func(t *testing.T, ctx context.Context, pool *store.Pool, number int) {
			// The release before writes an envelope's head over the row with no read of GitHub,
			// and the issue that it judged. The trigger pull_request_heads_unread_write owes a read
			// in the same statement.
			if _, err := pool.Exec(ctx, `update pull_request_heads set head_sha = $2, issue_key = $3 where repo = 'acme/widgets' and number = $1`,
				number, fakeSHA("c", number), fmt.Sprintf("HEAD-%d", number)); err != nil {
				t.Fatalf("write the head of #%d as the release before does: %v", number, err)
			}
		}},
	}
	for i, other := range others {
		for _, linkFirst := range []bool{true, false} {
			order := "the_link_last"
			if linkFirst {
				order = "the_link_first"
			}
			t.Run(other.name+"/"+order, func(t *testing.T) {
				fastPageRetries(t)
				pool, ctx := deliveryTestPool(t)
				number := 770 + 2*i
				if linkFirst {
					number++
				}
				issue := seedHeadIssue(t, ctx, pool, number)
				first := fakeSHA("a", number)
				storeReadHead(t, ctx, pool, number, first)
				var before *int64
				if linkFirst {
					linkByHand(t, ctx, pool, number)
					if owedCount(t, ctx, pool, "acme/widgets", number) != 1 {
						t.Fatalf("after the link to #%d, no read of it is owed; want one", number)
					}
					wantAttributedHead(t, "after the link alone", attributedHeadOf(t, ctx, pool, issue, number),
						AttributedHead{Number: number, HeadSHA: first, HeadKnown: true})
					before = owedRevision(ctx, pool, "acme/widgets", number)
					other.owe(t, ctx, pool, number)
				} else {
					other.owe(t, ctx, pool, number)
					before = owedRevision(ctx, pool, "acme/widgets", number)
					linkByHand(t, ctx, pool, number)
				}
				if before == nil {
					t.Fatalf("SETUP: no revision of the first owe of #%d", number)
				}
				hidden := func(step string) {
					t.Helper()
					head := attributedHeadOf(t, ctx, pool, issue, number)
					if head.HeadKnown || head.RefusedAs != other.refusedAs {
						t.Errorf("%s: HeadsAttributedTo lists #%d with HeadKnown %v and RefusedAs %q; want the head not read, with RefusedAs %q",
							step, number, head.HeadKnown, head.RefusedAs, other.refusedAs)
					}
				}
				hidden("after both owes")
				if err := settleOwedRead(ctx, pool, "acme/widgets", number, *before); err != nil {
					t.Fatalf("settle the read of #%d at the first owe's revision: %v", number, err)
				}
				if owedCount(t, ctx, pool, "acme/widgets", number) != 1 {
					t.Errorf("a settle at the first owe's revision deleted the owed read of #%d; want it kept for the second owe", number)
				}
				hidden("after a settle at the first owe's revision")
			})
		}
	}
}

// A link owes a read of acme/widgets#779, whose head row a read of GitHub wrote. The reconcile's
// read of it fails at the first pass, and GitHub refuses it at the second, as an installation that
// loses the repository does. Neither says that the head moved, so HeadsAttributedTo lists the
// stored head as known after each pass, and the read stays owed. When GitHub serves the pull
// request again, the next pass reads it and settles the owe.
func TestAFailedOrRefusedReadOfALinksOweHidesNoStoredHead(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	const number = 779
	issue := seedHeadIssue(t, ctx, pool, number)
	first := fakeSHA("a", number)
	github, fake := storeReadHead(t, ctx, pool, number, first)
	known := AttributedHead{Number: number, HeadSHA: first, HeadKnown: true}
	linkByHand(t, ctx, pool, number)
	client := fake.newTestClient()
	reconcile := NewReconcile(pool, client, events.NewBroker())

	github.mu.Lock()
	github.failing = true
	github.mu.Unlock()
	reconcile.runOnce(ctx)
	if row, owed := readOwedRow(t, ctx, pool, "acme/widgets", number); !owed || row.reason != owedFailure || row.reads != 1 {
		t.Errorf("after a pass whose read of #%d failed, it is owed %v (%+v); want a failure read one time", number, owed, row)
	}
	wantAttributedHead(t, "after the failed read", attributedHeadOf(t, ctx, pool, issue, number), known)

	github.mu.Lock()
	github.failing = false
	github.mu.Unlock()
	fake.notInstalled.Store("acme/widgets", true)
	reconcile = NewReconcile(pool, fake.newTestClient(), events.NewBroker())
	reconcile.runOnce(ctx)
	if row, owed := readOwedRow(t, ctx, pool, "acme/widgets", number); !owed || row.reason != owedRefusal || row.reads != 2 {
		t.Errorf("after a pass whose read of #%d GitHub refused, it is owed %v (%+v); want a refusal read two times", number, owed, row)
	}
	wantAttributedHead(t, "after the refused read", attributedHeadOf(t, ctx, pool, issue, number), known)

	fake.notInstalled.Delete("acme/widgets")
	reconcile = NewReconcile(pool, fake.newTestClient(), events.NewBroker())
	reconcile.runOnce(ctx)
	if row, owed := readOwedRow(t, ctx, pool, "acme/widgets", number); owed {
		t.Errorf("after the pass that reads #%d, it is still owed a %s read (%+v); want none", number, row.reason, row)
	}
	wantAttributedHead(t, "after the read that GitHub answers", attributedHeadOf(t, ctx, pool, issue, number), known)
}

// acme/widgets#781 has a timeline row of HEAD-781 and no results row, and a link owes its read.
// GitHub refuses the read, since the App is not installed on the repository. There is no stored
// head to hide, so HeadsAttributedTo lists #781 as a head not read with the word of the refusal
// and its time, as for each other owed refusal (owedReadJoin).
func TestALinksRefusedReadOfAPullRequestWithNoResultsRowNamesTheRefusal(t *testing.T) {
	fastPageRetries(t)
	pool, ctx := deliveryTestPool(t)
	const number = 781
	issue := seedHeadIssue(t, ctx, pool, number)
	if _, err := pool.Exec(ctx, `insert into delivery_pull_requests (repo, number, title, url, author, issue_key) values ('acme/widgets', $1, 'A change', $2, 'octocat', $3)`,
		number, fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number), issue); err != nil {
		t.Fatalf("store the timeline row of #%d: %v", number, err)
	}
	fake := newFakeGitHub(t)
	serveGitHubPulls(t, fake, "acme/widgets")
	fake.notInstalled.Store("acme/widgets", true)
	linkByHand(t, ctx, pool, number)
	NewReconcile(pool, fake.newTestClient(), events.NewBroker()).runOnce(ctx)
	row, owed := readOwedRow(t, ctx, pool, "acme/widgets", number)
	if !owed || row.reason != owedRefusal || row.refusedAs == nil || *row.refusedAs != model.RefusedAsAppNotInstalled || row.refusedSince == nil {
		t.Fatalf("after a pass whose read of #%d GitHub refused, it is owed %v (%+v); want a refusal as %s with its time", number, owed, row, model.RefusedAsAppNotInstalled)
	}
	wantAttributedHead(t, "after the refused read", attributedHeadOf(t, ctx, pool, issue, number),
		AttributedHead{Number: number, RefusedAs: model.RefusedAsAppNotInstalled, RefusedSince: row.refusedSince})
}
