package workflow

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// seedPastReview seeds LEGION-208 standing at at with status, its pull request #42 open at headOne,
// and the rows the phases before it left: the reviewer's round decided by an approval of approved
// (none where approved is ""), and the implementer and the merger on their claims.
func seedPastReview(t *testing.T, pool *pgxpool.Pool, at phase.Phase, hold *record.Hold, status, approved string) {
	t.Helper()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: at, Hold: hold, Generation: 1, Status: status, Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", Base: "main", HeadSHA: headOne, Failing: []string{}})
	reviewer := record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Entry: 1, ReviewedHead: approved,
		HandoffRecord: "review-1", HandoffVersion: 1, CompletedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)}
	if approved != "" {
		reviewer.Decision = &record.ReviewDecision{State: "approved", Body: "ship it", Head: approved}
	}
	seedPhase(t, pool, reviewer)
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleMerger, Claim: "merger-claim"})
}

// An approval is of the head it names and no other, and the merger merges the head its review
// approved, so a head that reaches the pull request after the review decided - in accepting, in
// retro, where the implementer works and a push of its own is one of them, or in merging - sends
// the issue back to reviewing, as a head that moves in reviewing re-asks the round there. Each way
// a head reaches the record does it: the push, which leaves code on its way from the approved head
// before its head arrives, the synchronize that brings the head, a reopen at another head, and a
// merge GitHub made at another head before the issue reached merging. The move is the daemon's
// own: it counts a pass, clears the approval with the round it ended, starts the reviewer told
// why, tells the architect, tells the role whose phase it ended, and says on the issue why it
// went back. On main the issue stayed where it stood, and the merger merged whatever head it read.
func TestAHeadPastTheReviewSendsTheIssueBackToReviewing(t *testing.T) {
	stands := "the review approved head " + headOne + ", and pull request #42 stands at head " + headTwo + ", which no review approved"
	onItsWay := "the review approved head " + headOne + ", and a push to pull request #42 moves its branch on from that head"
	facts := []struct {
		name   string
		fact   intake.Fact
		reason string
		// merges is a merge, which closes an issue in merging rather than moving it.
		merges bool
	}{
		{name: "a push from the approved head", reason: onItsWay,
			fact: intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo, Pusher: "legion-implementer[bot]"}},
		{name: "the new head arriving", reason: stands,
			fact: intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headTwo, Before: headOne}},
		{name: "a reopen at another head", reason: stands,
			fact: intake.PullRequestOpened{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", Base: "main", HeadSHA: headTwo,
				Reopened: true, UpdatedAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)}},
		{name: "a merge at another head", reason: stands, merges: true,
			fact: intake.PullRequestMerged{Repo: "sjawhar/legion", Number: 42, HeadSHA: headTwo, MergeSHA: "merge-commit"}},
	}
	for _, at := range []struct {
		from   phase.Phase
		status string
		// told is the role whose phase the move ended and which is told so; accepting's is the
		// architect, which the notice tells.
		told claim.Role
	}{
		{phase.Accepting, "needs_review", ""},
		{phase.Retro, "retro", claim.RoleImplementer},
		{phase.Merging, "retro", claim.RoleMerger},
	} {
		for _, tc := range facts {
			if tc.merges && at.from == phase.Merging {
				continue
			}
			t.Run(tc.name+" in "+string(at.from), func(t *testing.T) {
				pool := migratedPool(t)
				seedPastReview(t, pool, at.from, nil, at.status, headOne)
				applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), tc.fact)

				if got := issuePhase(t, pool); got != phase.Reviewing {
					t.Fatalf("after %s in %s the issue is in %s, want reviewing: no review approved the head it carries", tc.name, at.from, got)
				}
				if task := startTask(t, pool, claim.RoleReviewer); !strings.Contains(task, "Phase: reviewing.") || !strings.Contains(task, "Reason: "+tc.reason+".") {
					t.Fatalf("the reviewer's task = %q, want it started in reviewing with the reason %q", task, tc.reason)
				}
				if decision := reviewerDecision(t, pool); decision != nil {
					t.Fatalf("the reviewer's decision = %+v, want it cleared with the round it ended", decision)
				}
				if pass := implementerPass(t, pool); pass != 1 {
					t.Fatalf("pass = %d, want the move back counted as one", pass)
				}
				reason, from, role := phaseFinishedNotice(t, pool)
				if want := "moved back to reviewing by the daemon: " + tc.reason; reason != want || from != at.from || role != string(phase.RoleFor(at.from)) {
					t.Fatalf("the architect's notice = %q from %s by %q, want %q from %s by %q", reason, from, role, want, at.from, phase.RoleFor(at.from))
				}
				want := []record.PhaseEnded{}
				if at.told != "" {
					want = append(want, record.PhaseEnded{Role: at.told, From: at.from, To: phase.Reviewing, Reason: tc.reason})
				}
				if ended := phaseEndedRows(t, pool); !slices.Equal(ended, want) {
					t.Fatalf("phase-ended messages = %+v, want %+v", ended, want)
				}
				posted := "Legion moved this issue from " + string(at.from) + " back to reviewing, because " + tc.reason + ". " +
					"An approval covers only the head it names, so the reviewer reviews the head the pull request carries now."
				if got := movePosts(t, pool); len(got) != 1 || got[0] != posted {
					t.Fatalf("posted on the issue %q, want %q once", got, posted)
				}
			})
		}
	}
}

// A fact past the review that leaves the approval standing on the head moves nothing: the push of
// the approved head itself delivered after its head arrived, which the move already spent; a
// synchronize that brings no new head; and a reopen at the approved head. An issue whose
// reviewer's row holds no approval - a new generation's clear leaves a member past its review with
// none - has no head to hold the pull request to, and a push moves it nowhere; its merger's task
// says no head was approved (TestTheMergersTaskNamesTheApprovedHead).
func TestAFactPastTheReviewThatLeavesTheApprovalStandingMovesNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approved string
		fact     intake.Fact
	}{
		{name: "the approved head's own push, delivered after it", approved: headOne,
			fact: intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headEarlier, After: headOne, Pusher: "legion-implementer[bot]"}},
		{name: "a synchronize that brings no new head", approved: headOne,
			fact: intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne}},
		{name: "a reopen at the approved head", approved: headOne,
			fact: intake.PullRequestOpened{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", Base: "main", HeadSHA: headOne,
				Reopened: true, UpdatedAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)}},
		{name: "a push where no approval is on record",
			fact: intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo, Pusher: "legion-implementer[bot]"}},
	} {
		for _, at := range []phase.Phase{phase.Accepting, phase.Retro, phase.Merging} {
			t.Run(tc.name+" in "+string(at), func(t *testing.T) {
				pool := migratedPool(t)
				seedPastReview(t, pool, at, nil, PhaseStatus(at), tc.approved)
				applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), tc.fact)
				if got := issuePhase(t, pool); got != at {
					t.Fatalf("after %s the issue is in %s, want it left in %s", tc.name, got, at)
				}
				assertOutboxCount(t, pool, "supervise", 0)
				if pass := implementerPass(t, pool); pass != 0 {
					t.Fatalf("pass = %d, want nothing counted", pass)
				}
			})
		}
	}
}

// The merger merges the head the review approved and no other, so the task that starts it on
// merging names that head, the one the reviewer's row records for the decided round, and its
// prompt holds the pull request's head to it and passes it to GitHub as the head the merge must
// match. A row that holds no approval names none, which the merger reads as no head it may merge.
// A retry of a held merge names it too, and a push that landed while the merge was held sends the
// issue back to reviewing at the retry rather than starting the merger on it.
func TestTheMergersTaskNamesTheApprovedHead(t *testing.T) {
	retro := intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim", Commit: headOne, Summary: "retro done"}
	retry := intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision}
	push := intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: headOne, After: headTwo, Pusher: "a-person"}
	held := &record.Hold{From: phase.Merging, Reason: record.HoldEscalated}
	for _, tc := range []struct {
		name     string
		at       phase.Phase
		hold     *record.Hold
		approved string
		facts    []intake.Fact
		want     phase.Phase
		// sentence is the end of the merger's task, "" where no merger is started.
		sentence string
	}{
		{name: "retro completes over an approval", at: phase.Retro, approved: headOne, facts: []intake.Fact{retro},
			want: phase.Merging, sentence: " Approved head: " + headOne + "."},
		{name: "retro completes where no approval is on record", at: phase.Retro, facts: []intake.Fact{retro},
			want: phase.Merging, sentence: " Approved head: none."},
		{name: "a held merge is retried", at: phase.Held, hold: held, approved: headOne, facts: []intake.Fact{retry},
			want: phase.Merging, sentence: " Approved head: " + headOne + "."},
		{name: "a push lands while the merge is held, then the retry", at: phase.Held, hold: held, approved: headOne, facts: []intake.Fact{push, retry},
			want: phase.Reviewing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedPastReview(t, pool, tc.at, tc.hold, "retro", tc.approved)
			engine := testEngine(config.DesignGateRootIssues, nil)
			for _, fact := range tc.facts {
				applyRefusingNothing(t, pool, engine, fact)
				if _, pushed := fact.(intake.Push); pushed {
					if got := issuePhase(t, pool); got != phase.Held {
						t.Fatalf("after the push the issue is in %s, want it still held", got)
					}
				}
			}
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("the issue is in %s, want %s", got, tc.want)
			}
			task := startTask(t, pool, claim.RoleMerger)
			if tc.sentence == "" {
				if task != "" {
					t.Fatalf("the merger was started with %q, want no merger started on a head no review approved", task)
				}
				if reviewer := startTask(t, pool, claim.RoleReviewer); !strings.Contains(reviewer, "Phase: reviewing.") {
					t.Fatalf("the reviewer's task = %q, want it started in reviewing", reviewer)
				}
				return
			}
			if !strings.Contains(task, "Phase: merging.") || !strings.HasSuffix(task, tc.sentence) {
				t.Fatalf("the merger's task = %q, want it started on merging and ending %q", task, tc.sentence)
			}
		})
	}
}

// phaseFinishedNotice is the architect's one phase-finished notice the outbox holds: its reason, the
// phase it names and the role.
func phaseFinishedNotice(t *testing.T, pool *pgxpool.Pool) (reason string, from phase.Phase, role string) {
	t.Helper()
	var stopped string
	if err := pool.QueryRow(t.Context(), `select coalesce(payload->>'reason', ''), coalesce(payload->>'phase', ''), coalesce(payload->>'role', '')
		from outbox where kind = 'notice' and payload->>'kind' = 'phase-finished'`).Scan(&reason, &stopped, &role); err != nil {
		t.Fatalf("read the architect's phase-finished notice: %v", err)
	}
	return reason, phase.Phase(stopped), role
}

// phaseEndedRows is every phase-ended message the outbox holds, in order.
func phaseEndedRows(t *testing.T, pool *pgxpool.Pool) []record.PhaseEnded {
	t.Helper()
	rows, err := pool.Query(t.Context(), "select id, kind, issue, payload from outbox where kind = $1 order by id", string(record.OutboxKindPhaseEnded))
	if err != nil {
		t.Fatalf("read the phase-ended messages: %v", err)
	}
	defer rows.Close()
	var ended []record.PhaseEnded
	for rows.Next() {
		var row record.OutboxRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.Issue, &row.Payload); err != nil {
			t.Fatalf("scan a phase-ended message: %v", err)
		}
		payload, err := record.DecodeOutboxPayload(row)
		if err != nil {
			t.Fatalf("decode phase-ended row %d: %v", row.ID, err)
		}
		message, ok := payload.(record.PhaseEnded)
		if !ok {
			t.Fatalf("phase-ended row %d decodes to %T", row.ID, payload)
		}
		ended = append(ended, message)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the phase-ended messages: %v", err)
	}
	return ended
}

// movePosts is every text the outbox posts on the issue for a move: the reason a status write
// carries, and the body of a message posted in its place where the status does not change.
func movePosts(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `select coalesce(payload->>'reason', payload->>'body', '') from outbox
		where kind in ('dispatch_status', 'dispatch_message') order by id`)
	if err != nil {
		t.Fatalf("read the posts: %v", err)
	}
	defer rows.Close()
	posts := []string{}
	for rows.Next() {
		var post string
		if err := rows.Scan(&post); err != nil {
			t.Fatalf("scan a post: %v", err)
		}
		if post != "" {
			posts = append(posts, post)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the posts: %v", err)
	}
	return posts
}
