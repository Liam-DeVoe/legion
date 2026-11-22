package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// The places of LEGION-208's review steps on today's line, the task default (todaysSteps): the
// reviewer's step, and the owner's acceptance.
const (
	reviewerStep   = 4
	acceptanceStep = 5
)

// architectClaim is the claim of LEGION-208's architect, the agent of its owner's acceptance: the
// issue is its tree's root, so its own architect owns it (acceptingAgent).
const architectClaim claim.Token = "legion-legion-legion-208-architect"

// seedStepAgent records agent as the agent of the latest start of the step at place of LEGION-208's
// task default, as the daemon's start writes it (startStep), and ends that run where ended: the
// step has passed.
func seedStepAgent(t *testing.T, pool *pgxpool.Pool, place int, agent claim.Token, ended bool) {
	t.Helper()
	at := record.StepPlace{Task: defaultTask, Kind: record.PlaceStep, Place: place}
	started := time.Date(2026, 9, 22, 22, place, 0, 0, time.UTC)
	seedRecord(t, pool, func(tx pgx.Tx) error {
		records := record.NewStore()
		if _, err := records.RaiseStepStarts(t.Context(), tx, "LEGION-208", at, func(int64) record.StepRecord {
			return record.StepRecord{Task: at.Task, Step: at.Place, Kind: at.Kind, Agent: string(agent), StartedAt: &started}
		}); err != nil || !ended {
			return err
		}
		_, _, err := records.EndStepStart(t.Context(), tx, "LEGION-208", at)
		return err
	})
}

// stepVerdictOf is the verdict the store holds of the step at place of LEGION-208's task default on
// #42 at head (store.StepVerdictTx), and whether it holds one.
func stepVerdictOf(t *testing.T, pool *pgxpool.Pool, head string, place int) (store.Verdict, bool) {
	t.Helper()
	var given store.Verdict
	var found bool
	seedRecord(t, pool, func(tx pgx.Tx) (err error) {
		given, found, err = store.StepVerdictTx(t.Context(), tx, ghrepo.MustParse("sjawhar/legion"), 42, head, defaultTask, place)
		return err
	})
	return given, found
}

// R5 and C3: the daemon takes a review step's verdict only from the claim the step's record names
// as its agent (record.StepStart.Agent, which the start writes from stepClaim), and records it as
// that step's verdict at the commit it names: the reviewer's completion as the verdict of the
// reviewer's step, and the owner's acceptance as its sign-off's approve, or its move back's changes,
// at the head the pull request stands at. A completion of the reviewer's step from a claim the
// step's record does not name is refused VERDICT_CALLER_FORBIDDEN, left unspent, and records
// nothing.
func TestReviewStepVerdictFromItsOwnClaim(t *testing.T) {
	t.Run("the reviewer's verdict is its step's, under its claim", func(t *testing.T) {
		pool := migratedPool(t)
		seedReviewOf(t, pool, headOne, "green")
		applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("approve", headOne))
		given, found := stepVerdictOf(t, pool, headOne, reviewerStep)
		if !found || given.Verdict != store.VerdictApprove || given.Session != string(reviewClaim) || given.Issue != "LEGION-208" {
			t.Fatalf("the verdict of the reviewer's step at %s = %+v (found %t), want the reviewer's approve under %s", headOne, given, found, reviewClaim)
		}
		if other, found := stepVerdictOf(t, pool, headOne, acceptanceStep); found {
			t.Fatalf("the owner's acceptance holds the verdict %+v at %s, want none: only the reviewer gave one", other, headOne)
		}
	})

	t.Run("a verdict from a claim the step's record does not name is refused", func(t *testing.T) {
		const another claim.Token = "legion-legion-legion-209-reviewer"
		pool := migratedPool(t)
		seedReviewOf(t, pool, headOne, "green")
		seedStepAgent(t, pool, reviewerStep, another, false)
		result := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("verdict", reviewerVerdict("approve", headOne))
		if result.Refusal == nil || result.Refusal.Code != "VERDICT_CALLER_FORBIDDEN" || result.Refusal.Status != 403 || !result.Unspent {
			t.Fatalf("the completion of a claim the step's record does not name = %+v (unspent %t), want 403 VERDICT_CALLER_FORBIDDEN, unspent", result.Refusal, result.Unspent)
		}
		for _, want := range []string{string(reviewClaim), string(another), "nothing was recorded"} {
			if !strings.Contains(result.Refusal.Message, want) {
				t.Fatalf("the refusal says %q, want it to say %q", result.Refusal.Message, want)
			}
		}
		if state, _ := storedDecision(t, pool); state != "" {
			t.Fatalf("the round's decision = %q, want none: the refusal records nothing", state)
		}
		if given, found := stepVerdictOf(t, pool, headOne, reviewerStep); found {
			t.Fatalf("the store holds the verdict %+v, want none: the refusal records nothing", given)
		}
		if row := reviewerRow(t, pool); row.Completed() || issuePhase(t, pool) != phase.Reviewing {
			t.Fatalf("after the refusal the reviewer's row is %+v and the issue is in %s, want no completion and reviewing", row, issuePhase(t, pool))
		}
	})

	for _, tc := range []struct {
		name  string
		send  func(t *testing.T, pool *pgxpool.Pool, engine *Engine) intake.Fact
		word  store.VerdictWord
		after phase.Phase
	}{
		{name: "the owner's sign-off is its approve at the head", word: store.VerdictApprove, after: phase.Retro,
			send: func(t *testing.T, pool *pgxpool.Pool, engine *Engine) intake.Fact {
				return rootsSignOff(t, pool, engine, "LEGION-208")
			}},
		{name: "the owner's move back is its changes at the head", word: store.VerdictChanges, after: phase.Implementing,
			send: func(t *testing.T, pool *pgxpool.Pool, engine *Engine) intake.Fact {
				return rootsMoveBack(t, pool, engine, "LEGION-208", phase.Implementing, "the evidence does not cover the migration")
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedAccepting(t, pool)
			engine := testEngine(config.DesignGateRootIssues, nil)
			applyRefusingNothing(t, pool, engine, tc.send(t, pool, engine))
			if got := issuePhase(t, pool); got != tc.after {
				t.Fatalf("after the owner's call the issue is in %s, want %s", got, tc.after)
			}
			given, found := stepVerdictOf(t, pool, "head", acceptanceStep)
			if !found || given.Verdict != tc.word || given.Session != string(architectClaim) || given.Issue != "LEGION-208" {
				t.Fatalf("the verdict of the owner's acceptance at head = %+v (found %t), want the architect's %s", given, found, tc.word)
			}
			if other, found := stepVerdictOf(t, pool, "head", reviewerStep); found {
				t.Fatalf("the reviewer's step holds the verdict %+v, want none: the owner's call is its own step's", other)
			}
		})
	}
}

// seedAcceptingAfterAMergeForward is seedAccepting with pull request #42 first at headOne and a
// merge forward asked of it, which then lands at headTwo: the head the owner may have read is not
// the head the pull request stands at now.
func seedAcceptingAfterAMergeForward(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	seedAccepting(t, pool)
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", HeadSHA: headOne, Failing: []string{},
		MergeForward: &record.MergeForward{Head: headOne, Base: "main", Phase: phase.Accepting, Why: record.ConflictMergeNext,
			AskedAt: time.Date(2026, 9, 22, 23, 40, 0, 0, time.UTC)}})
	applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", Before: headOne, HeadSHA: headTwo})
	if pr := recordedPR(t, pool, "LEGION-208"); pr.HeadSHA != headTwo || pr.MergeForward == nil || pr.MergeForward.Landed != headTwo {
		t.Fatalf("the pull request = %+v, want the merge forward landed at %s: this test proves nothing without it", pr, headTwo)
	}
}

// R5: the owner's acceptance is its approve, or its changes, at the head it names. sign_off and the
// architect's move back out of accepting name a commit, and the daemon takes either only where that
// commit is the pull request's current head, where it records the acceptance step's verdict. One
// that names an older head, as an owner that read the pull request before a merge forward landed
// would, is refused VERDICT_HEAD_NOT_CURRENT: the issue stays in accepting and no verdict of the
// acceptance is recorded at either head.
func TestTheOwnersAcceptanceIsAtTheHeadItNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		call  func(t *testing.T, pool *pgxpool.Pool, engine *Engine, commit string) intake.Fact
		word  store.VerdictWord
		after phase.Phase
	}{
		{name: "sign_off", word: store.VerdictApprove, after: phase.Retro,
			call: func(t *testing.T, pool *pgxpool.Pool, engine *Engine, commit string) intake.Fact {
				fact := rootsSignOff(t, pool, engine, "LEGION-208")
				fact.Commit = commit
				return fact
			}},
		{name: "the move back", word: store.VerdictChanges, after: phase.Implementing,
			call: func(t *testing.T, pool *pgxpool.Pool, engine *Engine, commit string) intake.Fact {
				fact := rootsMoveBack(t, pool, engine, "LEGION-208", phase.Implementing, "the evidence does not cover the migration")
				fact.Commit = commit
				return fact
			}},
	} {
		t.Run(tc.name+" at the current head records its verdict there", func(t *testing.T) {
			pool := migratedPool(t)
			seedAcceptingAfterAMergeForward(t, pool)
			engine := testEngine(config.DesignGateRootIssues, nil)
			applyRefusingNothing(t, pool, engine, tc.call(t, pool, engine, headTwo))
			if got := issuePhase(t, pool); got != tc.after {
				t.Fatalf("after the owner's %s at %s the issue is in %s, want %s", tc.name, headTwo, got, tc.after)
			}
			given, found := stepVerdictOf(t, pool, headTwo, acceptanceStep)
			if !found || given.Verdict != tc.word || given.Session != string(architectClaim) {
				t.Fatalf("the acceptance's verdict at %s = %+v (found %t), want the architect's %s", headTwo, given, found, tc.word)
			}
		})
		t.Run(tc.name+" naming an older head after a merge forward is refused", func(t *testing.T) {
			pool := migratedPool(t)
			seedAcceptingAfterAMergeForward(t, pool)
			engine := testEngine(config.DesignGateRootIssues, nil)
			result := applyFacts(t, pool, engine)("stale-"+tc.name, tc.call(t, pool, engine, headOne))
			if result.Refusal == nil || result.Refusal.Code != "VERDICT_HEAD_NOT_CURRENT" || result.Refusal.Status != 409 {
				t.Fatalf("the owner's %s naming %s = %+v, want 409 VERDICT_HEAD_NOT_CURRENT", tc.name, headOne, result.Refusal)
			}
			for _, want := range []string{headOne, headTwo, "nothing was recorded"} {
				if !strings.Contains(result.Refusal.Message, want) {
					t.Fatalf("the refusal says %q, want it to say %q", result.Refusal.Message, want)
				}
			}
			if got := issuePhase(t, pool); got != phase.Accepting {
				t.Fatalf("after the refused %s the issue is in %s, want accepting", tc.name, got)
			}
			for _, head := range []string{headOne, headTwo} {
				if given, found := stepVerdictOf(t, pool, head, acceptanceStep); found {
					t.Fatalf("after the refused %s the acceptance holds the verdict %+v at %s, want none", tc.name, given, head)
				}
			}
		})
	}
}

// reviewerVerdict is LEGION-208's reviewer completing its round with verdict at commit, as the
// completion route queues it (api/handoff.go).
func reviewerVerdict(verdict, commit string) intake.HandoffComplete {
	return intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim,
		Summary: "reviewed", Verdict: verdict, Commit: commit, HandoffRecord: "review-1", HandoffVersion: 1}
}

// storedDecision is LEGION-208's reviewer's decision as the database holds it, each key "" where
// the row holds none: read from the JSON itself, since a build before the reviewer's verdict reads
// that same JSON (record.ReviewDecision).
func storedDecision(t *testing.T, pool *pgxpool.Pool) (state, head string) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `select coalesce(decision->>'state', ''), coalesce(decision->>'head', '')
		from phases where issue = 'LEGION-208' and role = 'reviewer'`).Scan(&state, &head); err != nil {
		t.Fatalf("read the reviewer's stored decision: %v", err)
	}
	return state, head
}

// The reviewer's verdict decides its round: its completion carries approve or changes at the head
// it reviewed (api/handoff.go), and the daemon writes it as the round's decision at that commit.
// No GitHub review written with the one App's token decides a round, and on a Legion head a
// person's approval decides none either, while a person's request for changes still refuses the
// head. A round is stuck for want of a decision only when its reviewer completed with no verdict.
func TestReviewRoundFromTheReviewersVerdict(t *testing.T) {
	t.Run("an approve at the head decides the round at that commit", func(t *testing.T) {
		pool := migratedPool(t)
		seedReviewOf(t, pool, headOne, "green")
		applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("approve", headOne))
		if got := issuePhase(t, pool); got != phase.Accepting {
			t.Fatalf("after the reviewer's approve of the clean head the issue is in %s, want accepting", got)
		}
		if state, head := storedDecision(t, pool); state != "approved" || head != headOne {
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
		applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict("changes", headOne))
		if got := issuePhase(t, pool); got != phase.Implementing {
			t.Fatalf("after the reviewer's changes the issue is in %s, want implementing", got)
		}
		if state, head := storedDecision(t, pool); state != "changes_requested" || head != headOne {
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
				apply("verdict", reviewerVerdict("approve", headTwo))
				if got := issuePhase(t, pool); got != phase.Reviewing {
					t.Fatalf("after the approve of %s while the record holds %s the issue is in %s, want reviewing", headTwo, headOne, got)
				}
				if state, head := storedDecision(t, pool); state != "approved" || head != headTwo {
					t.Fatalf("the round's decision = %q at %q, want approved at %s", state, head, headTwo)
				}
				apply("sync", intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: tc.arrives})
				apply("read", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: tc.arrives,
					Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
				if got := issuePhase(t, pool); got != tc.want {
					t.Fatalf("after %s arrived, read clean, the issue is in %s, want %s with no further verdict", tc.arrives, got, tc.want)
				}
				if state, head := storedDecision(t, pool); tc.want == phase.Reviewing && (state != "approved" || head != headTwo) {
					t.Fatalf("the round's decision = %q at %q, want the approval of %s kept", state, head, headTwo)
				}
			})
		}
	})

	t.Run("a retry ends a round the reviewer approved before its hold", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnknown)
		apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
		apply("verdict", reviewerVerdict("approve", headOne))
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
				stale := reviewerVerdict("changes", tc.commit)
				stale.Entry = 1
				result := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))("stale", stale)
				if got := refusalCode(result); tc.refused && got != "HANDOFF_STALE_ENTRY" || !tc.refused && got != "" {
					t.Fatalf("the completion of entry 1 at %s = %+v, want refused %v", tc.commit, result.Refusal, tc.refused)
				}
				state, _ := storedDecision(t, pool)
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
				if got, _ := storedDecision(t, pool); got != "" {
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
				applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict(tc.verdict, commit), comment)
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
		if got, _ := storedDecision(t, pool); got != "" {
			t.Fatalf("the round's decision after a person's approval = %q, want none, so nothing writes success", got)
		}
		apply("changes", person(8, "changes_requested", 2))
		if got := issuePhase(t, pool); got != phase.Implementing {
			t.Fatalf("after that person's request for changes the issue is in %s, want implementing", got)
		}
		if state, head := storedDecision(t, pool); state != "changes_requested" || head != headOne {
			t.Fatalf("the round's decision = %q at %q, want changes_requested at %s", state, head, headOne)
		}
	})

	t.Run("a person's request for changes refuses the head the reviewer approved", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnread)
		apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
		apply("verdict", reviewerVerdict("approve", headOne))
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

// decisionWithAKeyNoBuildReads is a reviewer's decision that holds a key no field of
// record.ReviewDecision names, as an unreleased build that kept one more key wrote it.
const decisionWithAKeyNoBuildReads = `{"state": "approved", "head": "` + headTwo + `", "keyNoBuildReads": "` + headOne + `"}`

// The record decodes the decision column leniently (record's scanPhase reads it with
// json.Unmarshal), so a decision that holds a key no field names reads back as its state and its
// head, and nothing refuses the row. The strict decode beside it is the control: it refuses the
// same bytes, so the key is there to skip.
func TestADecisionWithAKeyNoBuildReadsReadsBack(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateUnread)
	if _, err := pool.Exec(context.Background(), "update phases set decision = $1::jsonb where issue = 'LEGION-208' and role = 'reviewer'",
		decisionWithAKeyNoBuildReads); err != nil {
		t.Fatalf("write the decision: %v", err)
	}
	if got := reviewerRow(t, pool).Decision; got == nil || got.State != "approved" || got.Head != headTwo {
		t.Fatalf("the record reads the decision %s as %+v; want approved at %s", decisionWithAKeyNoBuildReads, got, headTwo)
	}
	strict := json.NewDecoder(bytes.NewReader([]byte(decisionWithAKeyNoBuildReads)))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&record.ReviewDecision{}); err == nil {
		t.Fatalf("a strict decode of %s took it whole, want it to refuse the key no field names", decisionWithAKeyNoBuildReads)
	}
}
