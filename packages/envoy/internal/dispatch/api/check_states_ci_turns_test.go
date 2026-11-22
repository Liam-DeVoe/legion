// check_states_ci_turns_test.go holds the settler to its reads of a check CI proves at the former
// heads of an open pull request (deriveCIResults, formerHeadsOf): however many of them GitHub does
// not answer for, a healthy one is read within a bounded number of settles, a read that never
// started is no failed read, and a test handoff answers no serial over a state such a read left as
// it was. The rig is check_states_test.go's.
package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
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

// TestAnUnreadHeadsOldPassNeverCrossesATestHandoff: #70 is pushed through nine heads, settled at
// each, and is open at a tenth; k1, k2 and k3 read yes at the first of the nine, the head the
// tester tested. Then k3's run there fails, and the issue is marked owed, as the settled run marks
// it. Where the settle reads that head, it publishes k3 no for fail there, and the test handoff
// naming the head answers a serial at or above that event's. Where GitHub stops answering for the
// eight later heads, so the settle's bound ends before the read of the first head starts, the
// state published there is still the old yes, and the test handoff answers no serial over it: it
// waits for that head's read and refuses with 503 CHECK_STATES_UNSETTLED once its wait ends, or
// answers once the state there is the failure. The next settle reads the first head before the
// others, publishes no for fail there, and the handoff then answers a serial at or above that
// event's.
func TestAnUnreadHeadsOldPassNeverCrossesATestHandoff(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "the read completes"
		if queued {
			name = "the read never starts"
		}
		t.Run(name, func(t *testing.T) {
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
			for index, sha := range visited {
				rig.github.listings.set(sha, completedRun(int64(900+index), ciJobName, "success"))
			}
			rig.github.listings.set(eachHeadH4, completedRun(950, ciJobName, "success"))
			eachHeadResult(t, rig.handler, rig.issue, "k1", "tester", "pass", first)
			eachHeadResult(t, rig.handler, rig.issue, "k2", "tester", "pass", first)
			for _, sha := range append(visited, eachHeadH4) {
				upsertHead(t, rig, 70, sha, rig.issue, "A change")
				settleOwed(t, rig)
			}
			for _, check := range []string{"k1", "k2", "k3"} {
				if row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, first, check); !held || row.state == nil ||
					*row.state != model.PassStandsYes {
					t.Fatalf("%s at the first head is published as %+v before k3's run there fails, want yes", check, row)
				}
			}

			rig.github.listings.set(first, completedRun(900, ciJobName, "failure"))
			rig.served.checkStatesSchedule.fenceWait = time.Second
			// The settler's next pass a minimum gap on would follow at once in production; this test
			// runs each settle itself, so the window between two settles is the test's to hold.
			settler := rig.newSettler(t, "check states unread pass")
			settler.schedule.minGap = time.Hour
			owe := func(statement string) {
				if _, err := rig.database.Pool.Exec(context.Background(), statement, rig.issue); err != nil {
					t.Fatalf("%s: %v", statement, err)
				}
			}
			owe(`select check_states_owe($1)`)
			testHandoff := fmt.Sprintf(`{"head":%q}`, first)
			if !queued {
				settler.settle(store.WithTransactionTracking(context.Background()), rig.issue)
			} else {
				entered := rig.github.listings.blockReadsOf(t, later...)
				bounds := holdCIReadBounds(t)
				settle := func(what string) {
					done := make(chan struct{})
					go func() {
						defer close(done)
						settler.settle(store.WithTransactionTracking(context.Background()), rig.issue)
					}()
					for range ciHeadReadConcurrency {
						ciAwait(t, "every read slot of "+what+" to be taken", entered)
					}
					bounds.expire(t)
					ciAwait(t, what+" to end", done)
				}
				settle("the settle that does not read the first head")
				row, _ := publishedRow(t, rig.database, rig.issue, ciJobRepository, first, "k3")
				response := postHandoff(t, rig.handler, rig.issue, "test", testHandoff)
				switch code, _ := refusalOf(t, response); {
				case response.Code == http.StatusCreated:
					if row.state == nil || *row.state == model.PassStandsYes {
						t.Fatalf("the test handoff answers 201 with check_states_serial %s while k3 at the first head, whose run failed and whose read never started, is published as %s: the old pass crosses the handoff",
							readHandoffJSON(t, rig, decodeBody[model.HandoffSummary](t, response).ID)["check_states_serial"], publishedLine(row))
					}
				case response.Code != http.StatusServiceUnavailable || code != "CHECK_STATES_UNSETTLED":
					t.Fatalf("the test handoff while the first head's read waits: status=%d body=%s, want 503 CHECK_STATES_UNSETTLED or a serial over the failure", response.Code, response.Body.String())
				}
				owe(`update check_states_owed set due_at = now() where issue_key = $1 and due_at > now()`)
				settle("the next settle")
			}

			row, held := publishedRow(t, rig.database, rig.issue, ciJobRepository, first, "k3")
			if !held || row.state == nil || *row.state != model.PassStandsNo || row.reason != model.PassSubjectReasonFail {
				t.Fatalf("k3 at the first head after its failed run was read is published as %s, want no for fail", publishedLine(row))
			}
			var published int64 = -1
			for _, event := range checkStateEventsAfter(t, rig.database, 0) {
				if event.Issue == rig.issue && event.Seq == row.eventSeq {
					published = event.Payload.Serial
				}
			}
			response := postHandoff(t, rig.handler, rig.issue, "test", testHandoff)
			if response.Code != http.StatusCreated {
				t.Fatalf("the test handoff once k3's failure at the first head is published: status=%d body=%s, want 201", response.Code, response.Body.String())
			}
			raw := readHandoffJSON(t, rig, decodeBody[model.HandoffSummary](t, response).ID)["check_states_serial"]
			serial, err := strconv.ParseInt(string(raw), 10, 64)
			if err != nil || published < 0 || serial < published {
				t.Fatalf("the test handoff answers check_states_serial %s, and k3's failure at the first head has serial %d: want a serial at or above it", raw, published)
			}
		})
	}
}

// publishedLine is a published row's state and reason as one line a failure prints.
func publishedLine(row publishedState) string {
	if row.state == nil {
		return "no state"
	}
	return *row.state + "/" + row.reason
}
