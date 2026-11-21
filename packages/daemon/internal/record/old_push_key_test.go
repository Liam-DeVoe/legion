package record

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/store"
)

// A build before one App served every role wrote each push the review App made with the key
// byReviewApp (`"byReviewApp":true`), in the pull request's pushes, in a move's repeats and in the
// pushes of its head run. One App pushes for every role now, so the daemon tells no push of one
// role from another's and keeps no such key: a row that holds it still decodes, the key read as
// nothing, and the row this daemon writes back holds the key nowhere. The row is written here as
// that build left it, its JSON edited in the database, because nothing this build has can write
// the key.
//
// This file is the one place in the daemon that spells the old key, and it spells it in full on
// purpose: the fixture is the row as that build wrote it, and a key assembled from pieces to slip
// past the search for the old names (the reviewer-verdict check's git grep) would hide the one
// reference to it that has to stay, so that search leaves this file out by name.
func TestAPushTheReviewAppRecordedStillDecodes(t *testing.T) {
	ctx := context.Background()
	st := migratedStore(t)
	records := NewStore()
	issue := issueFixture("LEGION-208")
	pushes := []ClassifiedPush{{SHA: "b1c2d3", Before: "a0b1c2", Sender: "legion-review[bot]"}, {SHA: "c2d3e4", Before: "b1c2d3"}}
	moves := []Move{{Before: "a0b1c2", SHA: "b1c2d3", Pushed: true, Repeats: []ClassifiedPush{{SHA: "b1c2d3", Before: "a0b1c2", Sender: "legion-review[bot]"}}}}
	pr := PullRequest{Issue: issue.Key, Repo: "sjawhar/legion", Number: 1243, Branch: "legion/LEGION-208", HeadSHA: "c2d3e4",
		Failing: []string{}, Cancelled: []string{}, State: PullRequestOpen, Pushes: pushes, Moves: moves,
		Run: &HeadRun{Anchor: Judgement{HeadSHA: "a0b1c2", Failing: []string{}, Cancelled: []string{}, CheckRuns: []AttemptRun{}},
			Pushes: pushes[:1], Steps: []RunStep{{Head: "b1c2d3", Before: "a0b1c2"}}}}
	inTx(t, st, func(tx pgx.Tx) {
		must(t, records.PutIssue(ctx, tx, issue))
		must(t, records.PutPullRequest(ctx, tx, pr))
		if _, err := tx.Exec(ctx, `update pull_requests set
			pushes = jsonb_set(pushes, '{0,byReviewApp}', 'true'),
			head_moves = jsonb_set(head_moves, '{0,repeats,0,byReviewApp}', 'true'),
			head_run = jsonb_set(head_run, '{pushes,0,byReviewApp}', 'true')
			where issue = $1`, issue.Key); err != nil {
			t.Fatalf("write the key byReviewApp as a build before one App wrote it: %v", err)
		}
	})
	if stored := storedPushColumns(t, st, issue.Key); strings.Count(stored, `"byReviewApp": true`) != 3 {
		t.Fatalf("the seeded row's pushes, moves and head run = %s, want the key byReviewApp in each of the three", stored)
	}

	var read *PullRequest
	inTx(t, st, func(tx pgx.Tx) {
		var err error
		read, err = records.PullRequest(ctx, tx, issue.Key)
		if err != nil || read == nil {
			t.Fatalf("read the pull request whose pushes hold byReviewApp = %v, %v; want it decoded", read, err)
		}
	})
	if !reflect.DeepEqual(read.Pushes, pushes) || !reflect.DeepEqual(read.Moves, moves) || read.Run == nil || !reflect.DeepEqual(read.Run.Pushes, pushes[:1]) {
		t.Fatalf("the decoded pushes %#v, moves %#v and head run %#v; want %#v, %#v and the run's %#v: the key byReviewApp reads as nothing",
			read.Pushes, read.Moves, read.Run, pushes, moves, pushes[:1])
	}

	inTx(t, st, func(tx pgx.Tx) { must(t, records.PutPullRequest(ctx, tx, *read)) })
	if stored := storedPushColumns(t, st, issue.Key); strings.Contains(stored, "byReviewApp") {
		t.Fatalf("the row written back = %s, want no key byReviewApp: one App pushes for every role", stored)
	}
}

// storedPushColumns is the pushes, the moves and the head run of issue's pull request as the
// database holds them, joined by newlines.
func storedPushColumns(t *testing.T, st *store.Store, issue string) string {
	t.Helper()
	var pushes, moves, run string
	inTx(t, st, func(tx pgx.Tx) {
		must(t, tx.QueryRow(context.Background(), "select pushes::text, head_moves::text, head_run::text from pull_requests where issue = $1", issue).
			Scan(&pushes, &moves, &run))
	})
	return pushes + "\n" + moves + "\n" + run
}
