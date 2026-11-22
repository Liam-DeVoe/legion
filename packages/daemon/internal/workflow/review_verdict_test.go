package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// reviewerVerdict is LEGION-208's reviewer completing its round with verdict at commit, carrying
// the tester's pass at tested ("" for none), as the completion route queues it
// (api/handoff.go).
func reviewerVerdict(verdict, commit, tested string) intake.HandoffComplete {
	return intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
		Summary: "reviewed", Verdict: verdict, Commit: commit, TestedCommit: tested, HandoffRecord: "review-1", HandoffVersion: 1}
}

// storedDecision is LEGION-208's reviewer's decision as the database holds it, each key "" where
// the row holds none: read from the JSON itself, since a build before the reviewer's verdict reads
// that same JSON (record.ReviewDecision).
func storedDecision(t *testing.T, pool *pgxpool.Pool) (state, head, tested string) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `select coalesce(decision->>'state', ''), coalesce(decision->>'head', ''), coalesce(decision->>'testedCommit', '')
		from phases where issue = 'LEGION-208' and role = 'reviewer'`).Scan(&state, &head, &tested); err != nil {
		t.Fatalf("read the reviewer's stored decision: %v", err)
	}
	return state, head, tested
}

// The reviewer's verdict decides its round: its completion carries approve or changes at the head
// it reviewed (api/handoff.go), and the daemon writes it as the round's decision at that commit,
// with the tester's pass it carries. No GitHub review written with the one App's token decides a
// round, and on a Legion head a person's approval decides none either, while a person's request
// for changes still refuses the head. A round is stuck for want of a decision only when its
// reviewer completed with no verdict.
func TestReviewRoundFromTheReviewersVerdict(t *testing.T) {
	t.Run("an approve at the head decides the round at that commit", func(t *testing.T) {
		pool := migratedPool(t)
		seedReviewOf(t, pool, headOne, "green")
		applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("approve", headOne, ""))
		if got := issuePhase(t, pool); got != phase.Accepting {
			t.Fatalf("after the reviewer's approve of the clean head the issue is in %s, want accepting", got)
		}
		if state, head, _ := storedDecision(t, pool); state != "approved" || head != headOne {
			t.Fatalf("the round's decision = %q at %q, want approved at %s, the commit the verdict names", state, head, headOne)
		}
		if got := reviewerRow(t, pool).ReviewedHead; got != headOne {
			t.Fatalf("the commit the reviewer read = %q, want %s, the commit its verdict names", got, headOne)
		}
		if n := len(reviewStuckNotices(t, pool)); n != 0 {
			t.Fatalf("review-stuck notices = %d, want none: the verdict decided the round", n)
		}
	})

	t.Run("a changes at the head sends the work back and names the reviewer's verdict", func(t *testing.T) {
		pool := migratedPool(t)
		seedReviewOf(t, pool, headOne, "green")
		applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("changes", headOne, ""))
		if got := issuePhase(t, pool); got != phase.Implementing {
			t.Fatalf("after the reviewer's changes the issue is in %s, want implementing", got)
		}
		if state, head, _ := storedDecision(t, pool); state != "changes_requested" || head != headOne {
			t.Fatalf("the round's decision = %q at %q, want changes_requested at %s", state, head, headOne)
		}
		if got := implementerRounds(t, pool); got != 1 {
			t.Fatalf("review rounds = %d, want 1: the reviewer's changes ends a round", got)
		}
		task := startTask(t, pool, claim.RoleImplementer)
		if !strings.Contains(task, " Reviewer's verdict: changes.") || strings.Contains(task, "Tester's verdict") {
			t.Fatalf("the implementer's task %q, want it to name the reviewer's verdict as the reviewer's and no tester's verdict", task)
		}
	})

	t.Run("an approve at H2 carrying the tester's pass at H1 records both and ends the round at H2", func(t *testing.T) {
		pool := migratedPool(t)
		seedReviewOf(t, pool, headTwo, "green")
		applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("approve", headTwo, headOne))
		if state, head, tested := storedDecision(t, pool); state != "approved" || head != headTwo || tested != headOne {
			t.Fatalf("the round's decision = %q at %q carrying %q, want approved at %s carrying the tester's pass at %s", state, head, tested, headTwo, headOne)
		}
		if got := issuePhase(t, pool); got != phase.Accepting {
			t.Fatalf("after the approve of the clean head %s the issue is in %s, want accepting: the round settles at the verdict's commit, not the tested one", headTwo, got)
		}
		if got := reviewerRow(t, pool).ReviewedHead; got != headTwo {
			t.Fatalf("the commit the reviewer read = %q, want %s, the commit the verdict names", got, headTwo)
		}
		if n := len(reviewStuckNotices(t, pool)); n != 0 {
			t.Fatalf("review-stuck notices = %d, want none", n)
		}
	})

	t.Run("an approve of a head the record has not reached stands once that head arrives", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			arrives string
			want    phase.Phase
		}{
			{name: "the approved head arrives", arrives: headTwo, want: phase.Accepting},
			{name: "a head above the approved one arrives", arrives: headThree, want: phase.Reviewing},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pool := migratedPool(t)
				seedReviewOf(t, pool, headOne, "green")
				apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
				apply("verdict", reviewerVerdict("approve", headTwo, ""))
				if got := issuePhase(t, pool); got != phase.Reviewing {
					t.Fatalf("after the approve of %s while the record holds %s the issue is in %s, want reviewing", headTwo, headOne, got)
				}
				if state, head, _ := storedDecision(t, pool); state != "approved" || head != headTwo {
					t.Fatalf("the round's decision = %q at %q, want approved at %s", state, head, headTwo)
				}
				apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: tc.arrives})
				apply("read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: tc.arrives,
					Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
				if got := issuePhase(t, pool); got != tc.want {
					t.Fatalf("after %s arrived, read clean, the issue is in %s, want %s with no further verdict", tc.arrives, got, tc.want)
				}
				if state, head, _ := storedDecision(t, pool); tc.want == phase.Reviewing && (state != "approved" || head != headTwo) {
					t.Fatalf("the round's decision = %q at %q, want the approval of %s kept", state, head, headTwo)
				}
			})
		}
	})

	t.Run("a retry ends a round the reviewer approved before its hold", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnknown)
		apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
		apply("verdict", reviewerVerdict("approve", headOne, ""))
		apply("failed", intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleReviewer})
		apply("clean", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
			Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
		if got := issuePhase(t, pool); got != phase.Held {
			t.Fatalf("after GitHub's answer while held the issue is in %s, want held", got)
		}
		apply("retry", intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision})
		if got := issuePhase(t, pool); got != phase.Accepting {
			t.Fatalf("after the retry the issue is in %s, want accepting: the round was decided before the hold", got)
		}
		var starts int
		if err := pool.QueryRow(t.Context(), `select count(*) from outbox where kind = 'supervise' and payload->>'op' = 'start'
			and payload->>'role' = 'reviewer' and coalesce(payload->>'task', '') <> ''`).Scan(&starts); err != nil {
			t.Fatal(err)
		}
		if starts != 0 || implementerRounds(t, pool) != 0 {
			t.Fatalf("the retry started the reviewer %d times and counted %d rounds, want neither: its round had already ended", starts, implementerRounds(t, pool))
		}
	})

	t.Run("a completion of an entry the round has left decides nothing unless it is at the head", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			commit    string
			movedBack bool
			refused   bool
		}{
			{name: "at a head the record has left", commit: headEarlier, refused: true},
			{name: "at the head, the entry raised by a move back", commit: headOne, movedBack: true, refused: true},
			{name: "at the head the record holds", commit: headOne},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pool := migratedPool(t)
				seedReviewOf(t, pool, headOne, "green")
				row := reviewerRow(t, pool)
				row.Entry, row.EntryMovedBack = 2, tc.movedBack
				seedPhase(t, pool, row)
				// The completion passed the route's entry fence while the reviewer's row stood on
				// entry 1, and reached the workflow after the phase was entered again (entry 2): it
				// carries entry 1, the entry the claim served when the route held it.
				stale := reviewerVerdict("changes", tc.commit, "")
				stale.Entry = 1
				result := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("stale", stale)
				if got := refusalCode(result); tc.refused && got != "HANDOFF_STALE_ENTRY" || !tc.refused && got != "" {
					t.Fatalf("the completion of entry 1 at %s = %+v, want refused %v", tc.commit, result.Refusal, tc.refused)
				}
				state, _, _ := storedDecision(t, pool)
				if tc.refused && (state != "" || issuePhase(t, pool) != phase.Reviewing) {
					t.Fatalf("after the refused completion the decision is %q and the issue is in %s, want none and reviewing", state, issuePhase(t, pool))
				}
				if !tc.refused && (state != "changes_requested" || issuePhase(t, pool) != phase.Implementing) {
					t.Fatalf("after the completion at the head the decision is %q and the issue is in %s, want changes_requested and implementing", state, issuePhase(t, pool))
				}
			})
		}
	})

	t.Run("a GitHub review by the one App's login decides no round", func(t *testing.T) {
		for _, state := range []string{"approved", "changes_requested"} {
			t.Run(state, func(t *testing.T) {
				pool := migratedPool(t)
				seedReviewOf(t, pool, headOne, "green")
				// The review names the reviewer's own session and the head in its Legion footer,
				// and carries the write access no App login is ever read for: none of it counts.
				applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 5,
					SubmittedAt: time.Date(2026, 9, 23, 0, 1, 0, 0, time.UTC), State: state, CommitID: headOne, Author: testAppLogin, AuthorCanWrite: true,
					Body: reviewBody("the App's "+state, headOne)})
				if got := issuePhase(t, pool); got != phase.Reviewing {
					t.Fatalf("after the App's %s the issue is in %s, want reviewing", state, got)
				}
				if got, _, _ := storedDecision(t, pool); got != "" {
					t.Fatalf("the round's decision = %q, want none: a review written with the one App's token decides nothing", got)
				}
			})
		}
	})

	t.Run("a COMMENT with a recorded verdict tells nothing, and a completion with no verdict tells once", func(t *testing.T) {
		comment := intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 9, SubmittedAt: time.Date(2026, 9, 23, 0, 1, 0, 0, time.UTC),
			State: "commented", CommitID: headOne, Author: testAppLogin, Body: commentBody("the findings, inline")}
		for _, tc := range []struct {
			name    string
			verdict string
			told    []string
		}{
			// GitHub has not answered for the head yet, so the approval waits where it stands.
			{name: "approve, then the reviewer's comment", verdict: "approve", told: []string{}},
			// A completion recorded with no verdict is what a reviewer before the verdict sent.
			{name: "no verdict, then the reviewer's comment", verdict: "", told: []string{byCompletion}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pool := migratedPool(t)
				seedMergeStateRound(t, pool, record.MergeStateUnread)
				commit := headOne
				if tc.verdict == "" {
					commit = ""
				}
				applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict(tc.verdict, commit, ""), comment)
				told := []string{}
				for _, notice := range reviewStuckNotices(t, pool) {
					told = append(told, notice.Summary)
					if notice.Topic != notify.RoleTopicPrefix+string(reviewClaim) || !strings.Contains(notice.Reason, "with no verdict") {
						t.Fatalf("the review-stuck notice %+v, want it on the reviewer's role topic, naming a completion with no verdict", notice)
					}
				}
				if strings.Join(told, "|") != strings.Join(tc.told, "|") {
					t.Fatalf("review-stuck notices written by %q, want %q", told, tc.told)
				}
				if got := issuePhase(t, pool); got != phase.Reviewing {
					t.Fatalf("the issue is in %s, want reviewing", got)
				}
			})
		}
	})

	t.Run("a person's approval decides no round, and that person's request for changes refuses the head", func(t *testing.T) {
		person := func(id int64, state string, minute int) intake.PullRequestReview {
			return intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: id, SubmittedAt: time.Date(2026, 9, 23, 0, minute, 0, 0, time.UTC),
				State: state, CommitID: headOne, Author: "a-writer", AuthorCanWrite: true, Body: "the person's " + state}
		}
		pool := migratedPool(t)
		seedReviewOf(t, pool, headOne, "green")
		apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
		apply("approved", person(7, "approved", 1))
		if got := issuePhase(t, pool); got != phase.Reviewing {
			t.Fatalf("after a person's approval the issue is in %s, want reviewing: only the reviewer's verdict approves a Legion head", got)
		}
		if got, _, _ := storedDecision(t, pool); got != "" {
			t.Fatalf("the round's decision after a person's approval = %q, want none, so nothing writes success", got)
		}
		apply("changes", person(8, "changes_requested", 2))
		if got := issuePhase(t, pool); got != phase.Implementing {
			t.Fatalf("after that person's request for changes the issue is in %s, want implementing", got)
		}
		if state, head, _ := storedDecision(t, pool); state != "changes_requested" || head != headOne {
			t.Fatalf("the round's decision = %q at %q, want changes_requested at %s", state, head, headOne)
		}
	})

	t.Run("a person's request for changes refuses the head the reviewer approved", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnread)
		apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
		apply("verdict", reviewerVerdict("approve", headOne, ""))
		if got := issuePhase(t, pool); got != phase.Reviewing {
			t.Fatalf("after the approve, with no merge state read, the issue is in %s, want reviewing", got)
		}
		apply("changes", intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, ID: 8, SubmittedAt: time.Date(2026, 9, 23, 0, 2, 0, 0, time.UTC),
			State: "changes_requested", CommitID: headOne, Author: "a-writer", AuthorCanWrite: true, Body: "this breaks the parser"})
		if got := issuePhase(t, pool); got != phase.Implementing {
			t.Fatalf("after a person's request for changes the issue is in %s, want implementing: a refusal beats the approval", got)
		}
		task := startTask(t, pool, claim.RoleImplementer)
		if !strings.Contains(task, "this breaks the parser") || strings.Contains(task, reviewerVerdictLabel) {
			t.Fatalf("the implementer's task %q, want the person's request for changes and no reviewer's verdict: the reviewer's approve did not decide the round", task)
		}
	})
}

// decisionBeforeTheVerdict is record.ReviewDecision as the releases before the reviewer's verdict
// declare it, which their read of the phases record decodes the decision column into with
// json.Unmarshal (record's scanPhase), so a key none of them knows is skipped.
type decisionBeforeTheVerdict struct {
	State          string `json:"state"`
	Body           string `json:"body,omitempty"`
	Head           string `json:"head,omitempty"`
	Refused        string `json:"refused,omitempty"`
	ToldStuckHead  string `json:"toldStuckHead,omitempty"`
	ToldStuckState string `json:"toldStuckState,omitempty"`
}

// The decision the reviewer's verdict writes carries testedCommit, a key no release before the
// verdict knows, and a state of a review's spelling, which that release reads: its read of the
// phases record decodes the column leniently, so it reads the approval at its head and skips the
// key. The strict decode beside it is the control: it refuses the same bytes, so the key is there
// to skip.
func TestAReviewersVerdictWithTestedCommitDecodesUnderTheReleaseBeforeIt(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateUnread)
	applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("approve", headTwo, headOne))
	var stored []byte
	if err := pool.QueryRow(context.Background(), "select decision from phases where issue = 'LEGION-208' and role = 'reviewer'").Scan(&stored); err != nil {
		t.Fatalf("read the stored decision: %v", err)
	}
	if !strings.Contains(string(stored), `"testedCommit"`) {
		t.Fatalf("the stored decision %s carries no testedCommit, so this test proves nothing", stored)
	}
	var before decisionBeforeTheVerdict
	if err := json.Unmarshal(stored, &before); err != nil || before.State != "approved" || before.Head != headTwo {
		t.Fatalf("the release before the verdict reads the decision %s as %+v, %v; want approved at %s", stored, before, err, headTwo)
	}
	strict := json.NewDecoder(bytes.NewReader(stored))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&decisionBeforeTheVerdict{}); err == nil {
		t.Fatalf("a strict decode of %s took it whole, want it to refuse testedCommit", stored)
	}
}
