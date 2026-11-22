// check_states_ci_turns_test.go holds the settler to its reads of a check CI proves at the former
// heads of an open pull request (deriveCIResults, formerHeadsOf): however many of them GitHub does
// not answer for, a healthy one is read within a bounded number of settles, and a read that never
// started is no failed read. The rig is check_states_test.go's.
package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/githubapp"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// TestAHealthyFormerHeadIsReadWithinItsTurns: #70 is pushed through nine heads, settled at each, and
// is open at a tenth. k3's run at the first of the nine went on while #70 was there, so k3 reads no
// for not_completed there. Then that run passes, and GitHub stops answering for the eight later
// heads: each read of one of them waits until its bound ends, and the eight hold every read slot
// until the bound of their settle ends. The first head is read within ceil(9/8) settles all the
// same, and its state moves to yes from CI; a settle in which its read never started leaves its
// state as it was, and never reports a failed read there.
func TestAHealthyFormerHeadIsReadWithinItsTurns(t *testing.T) {
	savedTimeout := githubapp.RequestTimeout
	githubapp.RequestTimeout = 24 * time.Hour
	t.Cleanup(func() { githubapp.RequestTimeout = savedTimeout })
	rig := newCheckStatesRig(t, checkStatesWithCI)
	// The nine heads, in the order #70 was at them; the first sorts last by commit.
	visited := make([]string, 9)
	for index := range visited {
		visited[index] = fmt.Sprintf("%040x", len(visited)-index)
	}
	first, later := visited[0], visited[1:]
	rig.github.listings.set(first, pendingRun(900, ciJobName, "in_progress"))
	for index, sha := range later {
		rig.github.listings.set(sha, completedRun(int64(901+index), ciJobName, "success"))
	}
	rig.github.listings.set(eachHeadH4, completedRun(950, ciJobName, "success"))
	for _, sha := range append(visited, eachHeadH4) {
		upsertHead(t, rig, 70, sha, rig.issue, "A change")
		settleOwed(t, rig)
	}
	if row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, first, "k3"); !held || row.state == nil ||
		*row.state != model.PassStandsNo || row.ciCase != ciCaseNotCompleted {
		t.Fatalf("k3 at the first head while its run goes on is published as %+v, want no with ci_case %s", row, ciCaseNotCompleted)
	}

	rig.github.listings.set(first, completedRun(900, ciJobName, "success"))
	entered := rig.github.listings.blockReadsOf(t, later...)
	bounds := holdCIReadBounds(t)
	// A settle leaves the mark of a head it did not read due a minute gap on, which this test makes
	// due itself, so each settle below is one round and no clock decides when the next one runs.
	settler := rig.newSettler(t, "check states turns")
	settler.schedule.minGap = time.Hour
	owe := func(statement string) {
		if _, err := rig.database.Pool.Exec(context.Background(), statement, rig.issue); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	owe(`select check_states_owe($1)`)
	turns := (len(visited) + ciHeadReadConcurrency - 1) / ciHeadReadConcurrency
	for settle := 1; ; settle++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			settler.settle(store.WithTransactionTracking(context.Background()), rig.issue)
		}()
		for range ciHeadReadConcurrency {
			ciAwait(t, "eight of the later heads to hold every read slot", entered)
		}
		bounds.expire(t)
		ciAwait(t, fmt.Sprintf("settle %d to end", settle), done)
		row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, first, "k3")
		if !held || row.state == nil {
			t.Fatalf("settle %d left no state of k3 at the first head", settle)
		}
		if *row.state == model.PassStandsYes {
			t.Logf("the first head was read in settle %d of at most %d", settle, turns)
			return
		}
		if row.reason == model.PassSubjectReasonReadFailed {
			t.Fatalf("settle %d published k3 at the first head as unknown for read_failed, though GitHub answers for it: a read that never started is no failed read", settle)
		}
		if settle == turns {
			t.Fatalf("k3 at the first head still reads %s/%s (ci_case %q) after %d settles: its healthy read never started", *row.state, row.reason, row.ciCase, settle)
		}
		owe(`update check_states_owed set due_at = now() where issue_key = $1`)
	}
}
