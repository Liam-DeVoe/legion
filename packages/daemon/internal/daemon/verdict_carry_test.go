package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// pushedTo is a push that moves pull request 86 from one head to another: GitHub moves its head,
// and its synchronize reaches the daemon.
func (rig *verdictRig) pushedTo(t *testing.T, from, to string) {
	t.Helper()
	rig.github.mu.Lock()
	rig.github.head86 = to
	rig.github.mu.Unlock()
	rig.apply(t, intake.PullRequestSynchronized{Repo: "acme/widgets", Number: 86, Branch: "legion/CAPTURE-1", Base: "main", Before: from, HeadSHA: to})
}

// mergeState is GitHub's merge state of pull request 86 at the head it stands at, as the merger
// reads it before it merges: `blocked` while the required legion-verdict there is not success.
func (rig *verdictRig) mergeState(t *testing.T) string {
	t.Helper()
	response, err := http.Get(rig.w.githubAPI + "/repos/acme/widgets/pulls/86")
	if err != nil {
		t.Fatalf("read pull request 86: %v", err)
	}
	defer response.Body.Close()
	var pull struct {
		State string `json:"mergeable_state"`
	}
	if err := json.NewDecoder(response.Body).Decode(&pull); err != nil {
		t.Fatalf("decode pull request 86: %v", err)
	}
	return pull.State
}

// noCompare fails the test where the daemon asked GitHub for a compare of two commits.
func (rig *verdictRig) noCompare(t *testing.T) {
	t.Helper()
	if _, events := rig.github.recorded(); slices.ContainsFunc(events, func(e string) bool { return strings.HasPrefix(e, "compared") }) {
		t.Fatalf("GitHub saw %v, want no compare call: the carry reads the reviewer's testedCommit, never a diff", events)
	}
}

// nothingWrittenOn fails the test where legion-verdict has a run, a status or a recorded write at
// head.
func (rig *verdictRig) nothingWrittenOn(t *testing.T, head string) {
	t.Helper()
	if runs := rig.github.legionRuns(head); len(runs) != 0 {
		t.Fatalf("legion-verdict runs on %s = %+v, want none", head, runs)
	}
	if recorded := verdictWrites(t, rig.pool, head); len(recorded) != 0 {
		t.Fatalf("the store records writes %+v on %s, want none", recorded, head)
	}
}

// approveCarrying is the reviewer's completion of reviewing approving commit, carrying the
// tester's pass at tested ("" for none).
func approveCarrying(commit, tested string, n int) intake.HandoffComplete {
	fact := reviewerVerdict("approve", commit, n)
	fact.TestedCommit = tested
	return fact
}

// verdictPastReview is the recorded reviewer's verdict past review at commit, carrying the tester's
// pass at tested ("" for none), as the review-verdict route queues it.
func verdictPastReview(word, commit, tested string) intake.ReviewVerdict {
	return intake.ReviewVerdict{Issue: "CAPTURE-1", Claim: "review-claim", Verdict: word, Commit: commit, TestedCommit: tested, Summary: "read the push"}
}

// testerPassAt fails the test unless CAPTURE-1's tester row holds its pass at commit, or no pass at
// all for "".
func testerPassAt(t *testing.T, pool *store.Pool, commit string) {
	t.Helper()
	var verdict, tested string
	if err := pool.QueryRow(context.Background(), "select verdict, handoff_commit from phases where issue = 'CAPTURE-1' and role = 'tester'").Scan(&verdict, &tested); err != nil {
		t.Fatalf("read the tester's row: %v", err)
	}
	if want := map[bool]string{true: "pass", false: ""}[commit != ""]; verdict != want || tested != commit {
		t.Fatalf("the tester's row holds %q at %q, want %q at %q", verdict, tested, want, commit)
	}
}

// C2's carry, on CAPTURE-1's legion/ head with the tester's pass recorded at code (H1), in
// reviewing. A push to code-2 (H2) and the reviewer's approve with commit H2 and testedCommit H1
// keep the tester's pass at H2 and write success there, and the fake GitHub records no compare
// call. After a push to code-3 (H3), an approve with commit H3 and no testedCommit, or with
// testedCommit code-0 (H0), a commit that is not the tester's, carries no pass: it moves the issue
// back to testing and writes nothing on H3. H3's approve is past review when it follows H2's
// settled round (the review-verdict call), and the reviewer's completion when the push to H3 came
// in reviewing; both carry by the one rule.
func TestLegionVerdictCarry(t *testing.T) {
	carried := func(t *testing.T) *verdictRig {
		t.Helper()
		rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
		rig.pushedTo(t, "code", "code-2")
		rig.apply(t, approveCarrying("code-2", "code", 1))
		testwait.Eventually(t, "the approve of code-2 carrying code to settle the round as approved", func() bool {
			got, _ := capturePhase(t, rig.pool)
			return got == phase.Accepting
		})
		rig.written(t)
		return rig
	}

	t.Run("testedCommit H1 carries the tester's pass to H2", func(t *testing.T) {
		rig := carried(t)
		if runs := rig.github.legionRuns("code-2"); len(runs) != 1 || runs[0].Conclusion != "success" {
			t.Fatalf("legion-verdict runs on code-2 = %+v, want one concluding success", runs)
		}
		testerPassAt(t, rig.pool, "code")
		rig.noCompare(t)
	})

	for _, tc := range []struct {
		name   string
		tested string
	}{{"no testedCommit", ""}, {"testedCommit H0", "code-0"}} {
		t.Run("past review, after a push to H3, an approve naming "+tc.name+" carries no pass", func(t *testing.T) {
			rig := carried(t)
			rig.pushedTo(t, "code-2", "code-3")
			rig.apply(t, verdictPastReview("approve", "code-3", tc.tested))
			rig.written(t)
			rig.nothingWrittenOn(t, "code-3")
			if got, _ := capturePhase(t, rig.pool); got != phase.Testing {
				t.Fatalf("after the approve of code-3 naming %q the issue is in %s, want testing", tc.tested, got)
			}
			testerPassAt(t, rig.pool, "")
			if writes, _ := rig.github.recorded(); len(writes) != 1 {
				t.Fatalf("writes = %+v, want only code-2's success", writes)
			}
			rig.noCompare(t)
		})

		t.Run("in reviewing, after a push to H3, an approve naming "+tc.name+" carries no pass", func(t *testing.T) {
			rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, reviewingWithPass)
			rig.pushedTo(t, "code", "code-3")
			rig.apply(t, approveCarrying("code-3", tc.tested, 1))
			rig.written(t)
			rig.nothingWrittenOn(t, "code-3")
			if got, _ := capturePhase(t, rig.pool); got != phase.Testing {
				t.Fatalf("after the approve of code-3 naming %q the issue is in %s, want testing", tc.tested, got)
			}
			testerPassAt(t, rig.pool, "")
			if writes, _ := rig.github.recorded(); len(writes) != 0 {
				t.Fatalf("writes = %+v, want none", writes)
			}
			rig.noCompare(t)
		})
	}
}

// reviewerApprovedCode is CAPTURE-1's reviewer row as its approve of code, which ended the round,
// left it under the claim review-claim.
var reviewerApprovedCode = record.PhaseRow{Issue: "CAPTURE-1", Role: claim.RoleReviewer, Claim: "review-claim",
	HandoffCommit: "code", HandoffRecord: "review-1", HandoffVersion: 1, Summary: "reviewed",
	Decision: &record.ReviewDecision{State: "approved", Body: "reviewed", Head: "code"}}

// verdictAsks is every ask of a verdict past review CAPTURE-1's outbox holds (record's
// MergeForwardReview rows), in the order written.
func verdictAsks(t *testing.T, pool *store.Pool) []record.MergeForwardMessage {
	t.Helper()
	rows, err := pool.Query(context.Background(), "select payload from outbox where issue = 'CAPTURE-1' and kind = $1 order by id", string(record.OutboxKindMergeForward))
	if err != nil {
		t.Fatalf("read the merge-forward rows: %v", err)
	}
	defer rows.Close()
	asks := []record.MergeForwardMessage{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan a merge-forward row: %v", err)
		}
		var message record.MergeForwardMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("decode the merge-forward row %s: %v", payload, err)
		}
		if message.Step == record.MergeForwardReview {
			asks = append(asks, message)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the merge-forward rows: %v", err)
	}
	return asks
}

// C57: each time the head moves while the issue stands past its review, the daemon asks the
// round's recorded reviewer for a verdict at the new head, with no merge forward asked: in retro,
// in accepting and in merging. GitHub holds the new head, which has no legion-verdict, so the
// merger does not merge it; the recorded reviewer's approve there, carrying the tester's pass,
// writes success, GitHub then answers it clean and the merger merges it, and the issue keeps its
// phase. A push in reviewing re-asks the round as on main and asks for no verdict past review. The
// ask reaches the reviewer's own topic while the head it names stands, and a later push, which
// asks again, ends it undelivered.
func TestAHeadThatMovesPastReviewAsksTheRecordedReviewer(t *testing.T) {
	for _, tc := range []struct {
		at     phase.Phase
		status string
	}{{phase.Retro, "retro"}, {phase.Accepting, "needs_review"}, {phase.Merging, "retro"}} {
		t.Run(string(tc.at), func(t *testing.T) {
			rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, func(t *testing.T, pool *store.Pool) {
				seedCapture(t, pool, tc.at, tc.status, testerPassAtCode, reviewerApprovedCode)
			})
			rig.pushedTo(t, "code", "code-2")
			want := []record.MergeForwardMessage{{Role: claim.RoleReviewer, Step: record.MergeForwardReview, Head: "code", Landed: "code-2",
				Base: "main", Phase: tc.at, PullRequest: 86}}
			if asks := verdictAsks(t, rig.pool); !slices.Equal(asks, want) {
				t.Fatalf("the push in %s asked %+v, want %+v", tc.at, asks, want)
			}
			if asked := rig.askedHeads(); len(asked) != 0 {
				t.Fatalf("the push asked for writes on %v, want none: no verdict names code-2", asked)
			}
			if state := rig.mergeState(t); state != "blocked" {
				t.Fatalf("GitHub answers the merger %q for code-2 before the reviewer's verdict, want blocked: legion-verdict there is not success", state)
			}

			rig.apply(t, verdictPastReview("approve", "code-2", "code"))
			rig.written(t)
			if runs := rig.github.legionRuns("code-2"); len(runs) != 1 || runs[0].Conclusion != "success" {
				t.Fatalf("legion-verdict runs on code-2 = %+v, want one concluding success", runs)
			}
			if state := rig.mergeState(t); state != "clean" {
				t.Fatalf("GitHub answers the merger %q for code-2 after the reviewer's approve, want clean", state)
			}
			if got, _ := capturePhase(t, rig.pool); got != tc.at {
				t.Fatalf("after the reviewer's verdict the issue is in %s, want %s", got, tc.at)
			}
		})
	}

	t.Run("a push in reviewing re-asks the round and asks for no verdict past review", func(t *testing.T) {
		rig := newVerdictRig(t, roleTokens{}, []config.VerdictRepository{widgetsEntry}, func(t *testing.T, pool *store.Pool) {
			seedCapture(t, pool, phase.Reviewing, "needs_review", testerPassAtCode, reviewerApprovedCode)
			clearCaptureMergeState(t, pool)
		})
		rig.pushedTo(t, "code", "code-2")
		rig.written(t)
		if got, pr := capturePhase(t, rig.pool); got != phase.Reviewing || pr.HeadSHA != "code-2" {
			t.Fatalf("after the push the issue is in %s at %s, want reviewing at code-2: the approve of code approves no other head", got, pr.HeadSHA)
		}
		if asks := verdictAsks(t, rig.pool); len(asks) != 0 {
			t.Fatalf("the push in reviewing asked %+v, want no verdict past review", asks)
		}
	})

	t.Run("the ask reaches the reviewer while its head stands", func(t *testing.T) {
		ask := record.MergeForwardMessage{Role: claim.RoleReviewer, Step: record.MergeForwardReview, Head: "sha-1", Landed: "sha-2", Base: "main",
			Phase: phase.Retro, PullRequest: 86}
		reviewer := notify.RoleTopicPrefix + string(mustClaimToken(t, "LEGION-208", claim.RoleReviewer))
		for _, head := range []string{"sha-2", "sha-3"} {
			t.Run("the pull request at "+head, func(t *testing.T) {
				publisher := &holderPublisher{}
				logs := &syncBuffer{}
				runner, _, pool := runnerIn(t, phase.Retro, "retro", publisher, slog.New(slog.NewJSONHandler(logs, nil)))
				if err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
					return runner.records.PutPullRequest(context.Background(), tx, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208",
						Repo: "acme/widgets", Number: 86, Branch: "legion/LEGION-208", HeadSHA: head, HeadUpdatedAt: time.Now(), Failing: []string{},
						CheckRuns: []record.AttemptRun{}, Base: "main"})
				}); err != nil {
					t.Fatalf("seed the pull request: %v", err)
				}
				enqueueOutbox(t, pool, runner.records, mustOutboxRow(t, "LEGION-208", ask, time.Now().Add(-time.Minute)))
				drainAt(t, runner, 0, "the ask's attempt")
				_, delivered := publisher.snapshot()
				if head == "sha-2" {
					if len(delivered) != 1 || delivered[0].topic != reviewer || delivered[0].message != ask.Message("LEGION-208", record.PullRequest{Repo: "acme/widgets"}) {
						t.Fatalf("delivered %+v, want the ask once to %s", delivered, reviewer)
					}
					return
				}
				if len(delivered) != 0 || outboxRows(t, pool) != 0 {
					t.Fatalf("delivered %+v with %d rows left, want the ask finished undelivered", delivered, outboxRows(t, pool))
				}
				if lines := loggedLines(t, logs.String(), mergeForwardLeftLine); len(lines) != 1 || !strings.Contains(fmt.Sprint(lines[0]["why"]), "moved on to sha-3") {
					t.Fatalf("the row logged %v, want one line naming the head it moved on to", lines)
				}
			})
		}
	})
}
