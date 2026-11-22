package workflow

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/store"
)

// testerPassAt is LEGION-208's tester row as its pass at commit leaves it.
func testerPassAt(commit string) record.PhaseRow {
	return record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim", Verdict: "pass", HandoffCommit: commit,
		HandoffRecord: "test-1", HandoffVersion: 1, Summary: "tested"}
}

// reviewedRow is LEGION-208's reviewer row as the reviewer's approve of headOne, carrying nothing,
// left it when it ended the round under reviewClaim.
func reviewedRow() record.PhaseRow {
	return record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleReviewer, Claim: reviewClaim, Verdict: "approve", HandoffCommit: headOne,
		HandoffRecord: "review-1", HandoffVersion: 1, Summary: "reviewed", CompletedAt: time.Date(2026, 9, 22, 23, 0, 0, 0, time.UTC),
		Decision: &record.ReviewDecision{State: "approved", Body: "reviewed", Head: headOne}}
}

// seedPastReview seeds LEGION-208 standing in at past its review: the tester passed headOne, the
// reviewer's approve of headOne ended the round, and a push since moved pull request #42 to
// headTwo.
func seedPastReview(t *testing.T, at phase.Phase, status string) *pgxpool.Pool {
	t.Helper()
	pool := migratedPool(t)
	standsIn(t, pool, at, status)
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", HeadSHA: headTwo, Failing: []string{}})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedPhase(t, pool, testerPassAt(headOne))
	seedPhase(t, pool, reviewedRow())
	seedClaimSession(t, pool, reviewClaim, "ses-reviewer")
	return pool
}

// pastReviewVerdict is the recorded reviewer's verdict past review at commit, carrying the tester's
// pass at tested ("" for none), as the review-verdict route queues it (api's reviewVerdict).
func pastReviewVerdict(verdict, commit, tested string) intake.ReviewVerdict {
	return intake.ReviewVerdict{Issue: "LEGION-208", Claim: reviewClaim, Verdict: verdict, Commit: commit, TestedCommit: tested,
		Summary: "read the push"}
}

// writingEngine is testEngine with a writer of legion-verdict that keeps the head of each write the
// engine asks for (Config.WriteVerdict), once the fact's transaction commits.
func writingEngine(asked *[]string) *Engine {
	engine := testEngine(config.DesignGateRootIssues, nil)
	engine.cfg.WriteVerdict = func(_, _ string, _ int, head string) { *asked = append(*asked, head) }
	return engine
}

// recordedVerdict is the verdict the store holds on #42 at head, the one the writer of
// legion-verdict reads, and whether it holds one.
func recordedVerdict(t *testing.T, pool *pgxpool.Pool, head string) (store.Verdict, bool) {
	t.Helper()
	var given store.Verdict
	var found bool
	seedRecord(t, pool, func(tx pgx.Tx) (err error) {
		given, found, err = store.VerdictTx(t.Context(), tx, ghrepo.MustParse("sjawhar/legion"), 42, head)
		return err
	})
	return given, found
}

// handoffFields is what of a phase row a completion writes and a verdict past review does not.
func handoffFields(row record.PhaseRow) []any {
	return []any{row.Claim, row.HandoffCommit, row.HandoffRecord, row.HandoffVersion, row.Verdict, row.Summary, row.CompletedAt.UTC()}
}

// The review-verdict route queues one intake.ReviewVerdict (api's reviewVerdict), and Engine.Apply
// applies it: the round's decision at the call's head, with the testedCommit it carries, on the
// reviewer's row and as the store's verdict there, and the write of legion-verdict asked at that
// head on the fact's commit. Without its case in Engine.apply's switch, the fact falls to the
// default, which records nothing.
func TestReviewVerdictFactIsRouted(t *testing.T) {
	pool := seedPastReview(t, phase.Retro, "retro")
	var asked []string
	applyRefusingNothing(t, pool, writingEngine(&asked), pastReviewVerdict("approve", headTwo, headOne))
	if state, head, tested := storedDecision(t, pool); state != "approved" || head != headTwo || tested != headOne {
		t.Fatalf("the round's decision = %q at %q carrying %q, want approved at %s carrying the tester's pass at %s", state, head, tested, headTwo, headOne)
	}
	given, found := recordedVerdict(t, pool, headTwo)
	if !found || given.Verdict != store.VerdictApprove || given.TestedCommit != headOne || given.Issue != "LEGION-208" || given.Session != string(reviewClaim) {
		t.Fatalf("the store's verdict at %s = %+v (found %t), want the reviewer's approve carrying %s", headTwo, given, found, headOne)
	}
	if !slices.Equal(asked, []string{headTwo}) {
		t.Fatalf("the engine asked for writes on %v, want one on %s", asked, headTwo)
	}
}

// R4, R5 and C57: past review, the recorded reviewer's verdict at the current head is the round's
// decision there and writes no handoff. It moves no phase, except an approve whose testedCommit
// does not name the commit of the tester's recorded pass: that one carries no pass, and the issue
// goes back to testing, with the tester's pass cleared, so nothing writes success on the head.
func TestAReviewVerdictPastReviewMovesTheIssueOnlyWhereItCarriesNoPass(t *testing.T) {
	for _, stand := range []struct {
		at     phase.Phase
		status string
	}{{phase.Accepting, "needs_review"}, {phase.Retro, "retro"}, {phase.Merging, "retro"}} {
		for _, tc := range []struct {
			name           string
			verdict        string
			tested         string
			state          string
			backToTesting  bool
			reasonNamesWhy string
		}{
			{name: "an approve carrying the tester's pass", verdict: "approve", tested: headOne, state: "approved"},
			{name: "a changes", verdict: "changes", state: "changes_requested"},
			{name: "an approve naming no testedCommit", verdict: "approve", state: "approved", backToTesting: true,
				reasonNamesWhy: "names no testedCommit"},
			{name: "an approve naming a commit the tester did not pass", verdict: "approve", tested: headEarlier, state: "approved",
				backToTesting: true, reasonNamesWhy: "names testedCommit " + headEarlier},
		} {
			t.Run(string(stand.at)+", "+tc.name, func(t *testing.T) {
				pool := seedPastReview(t, stand.at, stand.status)
				var asked []string
				applyRefusingNothing(t, pool, writingEngine(&asked), pastReviewVerdict(tc.verdict, headTwo, tc.tested))
				if state, head, tested := storedDecision(t, pool); state != tc.state || head != headTwo || tested != tc.tested {
					t.Fatalf("the round's decision = %q at %q carrying %q, want %s at %s carrying %q", state, head, tested, tc.state, headTwo, tc.tested)
				}
				if got, want := handoffFields(reviewerRow(t, pool)), handoffFields(reviewedRow()); !slices.Equal(got, want) {
					t.Fatalf("the reviewer's handoff = %v, want it as its completion left it, %v: the verdict writes no handoff", got, want)
				}
				if given, found := recordedVerdict(t, pool, headTwo); !found || string(given.Verdict) != tc.verdict || given.TestedCommit != tc.tested {
					t.Fatalf("the store's verdict at %s = %+v (found %t), want %s carrying %q", headTwo, given, found, tc.verdict, tc.tested)
				}
				if !slices.Equal(asked, []string{headTwo}) {
					t.Fatalf("the engine asked for writes on %v, want one on %s", asked, headTwo)
				}
				tester := roleRow(t, pool, claim.RoleTester)
				if !tc.backToTesting {
					if got := issuePhase(t, pool); got != stand.at {
						t.Fatalf("after the verdict the issue is in %s, want %s: a verdict past review moves no phase", got, stand.at)
					}
					if tester.Verdict != "pass" || tester.HandoffCommit != headOne {
						t.Fatalf("the tester's row = %+v, want its pass at %s kept", tester, headOne)
					}
					return
				}
				if got := issuePhase(t, pool); got != phase.Testing {
					t.Fatalf("after the verdict the issue is in %s, want testing: the approve carries no tester's pass to %s", got, headTwo)
				}
				if tester.Verdict != "" || tester.HandoffCommit != "" {
					t.Fatalf("the tester's row = %+v, want its pass cleared, so nothing writes success on %s", tester, headTwo)
				}
				task := startTask(t, pool, claim.RoleTester)
				for _, want := range []string{tc.reasonNamesWhy, "the tester's recorded pass is at " + headOne, "test " + headTwo} {
					if !strings.Contains(task, want) {
						t.Fatalf("the tester's task %q, want it to say %q", task, want)
					}
				}
				if want := "Legion moved this issue from " + string(stand.at) + " back to testing at the reviewer's request: the reviewer's approve of " + headTwo; !strings.HasPrefix(movePost(t, pool), want) {
					t.Fatalf("the move posts %q on the issue, want it to open %q", movePost(t, pool), want)
				}
			})
		}
	}
}

// The route reads the record before the fact's transaction, so the workflow holds the verdict to
// the record again under the fact's lock: the round's recorded reviewer, an issue past its review
// and the current head; and a lingering tree records nothing. Each refusal records nothing and asks
// for no write.
func TestAReviewVerdictTheRecordNoLongerTakesIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seed   func(t *testing.T, pool *pgxpool.Pool)
		fact   intake.ReviewVerdict
		status int
		code   string
	}{
		{name: "a head the pull request has moved off", fact: pastReviewVerdict("approve", headOne, ""), status: 409, code: "VERDICT_HEAD_NOT_CURRENT"},
		{name: "another reviewer claim", fact: func() intake.ReviewVerdict {
			fact := pastReviewVerdict("approve", headTwo, headOne)
			fact.Claim = "another-reviewer-claim"
			return fact
		}(), status: 403, code: "VERDICT_CALLER_FORBIDDEN"},
		{name: "an issue sent back to testing", seed: func(t *testing.T, pool *pgxpool.Pool) { standsIn(t, pool, phase.Testing, "testing") },
			fact: pastReviewVerdict("approve", headTwo, headOne), status: 409, code: "VERDICT_NOT_PAST_REVIEW"},
		{name: "a lingering tree", seed: func(t *testing.T, pool *pgxpool.Pool) {
			until := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
				Tree: "LEGION-208", Project: "LEGION", Title: "root", Phase: phase.Retro, Generation: 1, Status: "retro", Rank: "U", LingerUntil: &until})
		}, fact: pastReviewVerdict("approve", headTwo, headOne), status: 409, code: "TREE_LINGERING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := seedPastReview(t, phase.Retro, "retro")
			if tc.seed != nil {
				tc.seed(t, pool)
			}
			var asked []string
			result := applyFacts(t, pool, writingEngine(&asked))("verdict", tc.fact)
			if result.Refusal == nil || result.Refusal.Code != tc.code || result.Refusal.Status != tc.status ||
				!strings.Contains(result.Refusal.Message, "nothing was recorded") && !strings.Contains(result.Refusal.Message, "changed nothing") {
				t.Fatalf("the verdict was answered %+v, want %d %s saying nothing was recorded", result.Refusal, tc.status, tc.code)
			}
			if state, head, _ := storedDecision(t, pool); state != "approved" || head != headOne {
				t.Fatalf("the round's decision = %q at %q, want the approve of %s as it stood", state, head, headOne)
			}
			if _, found := recordedVerdict(t, pool, tc.fact.Commit); found {
				t.Fatalf("the store holds a verdict at %s, want none: the refusal records nothing", tc.fact.Commit)
			}
			if len(asked) != 0 {
				t.Fatalf("the engine asked for writes on %v, want none", asked)
			}
		})
	}
}

// A held issue moves only when its hold ends, so the recorded reviewer's verdict past review taken
// while it is held is recorded at its head and moves nothing then. When the hold ends - the
// architect's retry, or the operator's restart of the phase's worker - the workflow applies that
// verdict itself, with no second call: an approve at the head the pull request still stands at that
// carries no tester's pass sends the issue back to testing, and says so in the log. A verdict that
// carries the pass, a changes, and a verdict at a head the pull request has since moved off leave
// the issue in the phase it is released to.
func TestAVerdictTakenWhileHeldIsAppliedWhenTheHoldEnds(t *testing.T) {
	retry := intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision}
	restart := intake.RestartPhase{Issue: "LEGION-208", Role: claim.RoleImplementer}
	for _, tc := range []struct {
		name     string
		verdict  intake.ReviewVerdict
		pushed   bool
		ends     intake.Fact
		want     phase.Phase
		decision string
	}{
		{name: "an approve naming no testedCommit, then the architect's retry", verdict: pastReviewVerdict("approve", headTwo, ""), ends: retry,
			want: phase.Testing, decision: "approved"},
		{name: "an approve naming a commit the tester did not pass, then the operator's restart", verdict: pastReviewVerdict("approve", headTwo, headEarlier),
			ends: restart, want: phase.Testing, decision: "approved"},
		{name: "an approve carrying the tester's pass, then the architect's retry", verdict: pastReviewVerdict("approve", headTwo, headOne), ends: retry,
			want: phase.Retro, decision: "approved"},
		{name: "a changes, then the architect's retry", verdict: pastReviewVerdict("changes", headTwo, ""), ends: retry,
			want: phase.Retro, decision: "changes_requested"},
		{name: "an approve naming no testedCommit, a push, then the architect's retry", verdict: pastReviewVerdict("approve", headTwo, ""), pushed: true,
			ends: retry, want: phase.Retro, decision: "approved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := seedPastReview(t, phase.Retro, "retro")
			holdFromRetro(t, pool)
			var logs bytes.Buffer
			engine := testEngine(config.DesignGateRootIssues, slog.New(slog.NewTextHandler(&logs, nil)))
			var asked []string
			engine.cfg.WriteVerdict = func(_, _ string, _ int, head string) { asked = append(asked, head) }

			applyRefusingNothing(t, pool, engine, tc.verdict)
			if got := issuePhase(t, pool); got != phase.Held {
				t.Fatalf("after the verdict the issue is in %s, want held: a held issue moves only when its hold ends", got)
			}
			if state, head, _ := storedDecision(t, pool); state != tc.decision || head != headTwo {
				t.Fatalf("the round's decision = %q at %q, want %s at %s", state, head, tc.decision, headTwo)
			}
			if given, found := recordedVerdict(t, pool, headTwo); !found || given.Verdict != store.VerdictWord(tc.verdict.Verdict) {
				t.Fatalf("the store's verdict at %s = %+v (found %t), want the reviewer's %s", headTwo, given, found, tc.verdict.Verdict)
			}
			if !slices.Equal(asked, []string{headTwo}) {
				t.Fatalf("the engine asked for writes on %v, want one on %s", asked, headTwo)
			}
			if tc.pushed {
				applyRefusingNothing(t, pool, engine, intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42,
					Branch: "legion/LEGION-208", Base: "main", Before: headTwo, HeadSHA: headThree})
			}

			applyRefusingNothing(t, pool, engine, tc.ends)
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("after the hold ended the issue is in %s, want %s", got, tc.want)
			}
			applied := strings.Contains(logs.String(), heldVerdictApplied)
			if tc.want != phase.Testing {
				if applied {
					t.Fatalf("the log says %q, want no verdict applied at the hold's end", heldVerdictApplied)
				}
				return
			}
			if tester := roleRow(t, pool, claim.RoleTester); tester.Verdict != "" || tester.HandoffCommit != "" {
				t.Fatalf("the tester's row = %+v, want its pass cleared", tester)
			}
			if task := startTask(t, pool, claim.RoleTester); !strings.Contains(task, "test "+headTwo) {
				t.Fatalf("the tester's task %q, want it to name the head to test, %s", task, headTwo)
			}
			if want := "Legion moved this issue from retro back to testing at the reviewer's request: the reviewer's approve of " + headTwo; !strings.HasPrefix(movePost(t, pool), want) {
				t.Fatalf("the move posts %q on the issue, want it to open %q", movePost(t, pool), want)
			}
			if !applied {
				t.Fatalf("the log reads %q, want it to say %q", logs.String(), heldVerdictApplied)
			}
		})
	}
}

// holdFromRetro holds LEGION-208 from retro, as its implementer's failed claim does.
func holdFromRetro(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
		Tree: "LEGION-208", Project: "LEGION", Title: "root", Phase: phase.Held, Hold: &record.Hold{From: phase.Retro}, Generation: 1,
		Status: "retro", Rank: "U"})
}

// seedReviewingAt seeds LEGION-208 in reviewing with its pull request at head, which GitHub reads
// mergeable and clean, and the tester's pass at headOne, its first round not yet decided.
func seedReviewingAt(t *testing.T, head string) *pgxpool.Pool {
	t.Helper()
	pool := migratedPool(t)
	standsIn(t, pool, phase.Reviewing, "needs_review")
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", HeadSHA: head, Failing: []string{},
		Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateClean})
	seedRoundOneRows(t, pool)
	seedPhase(t, pool, testerPassAt(headOne))
	return pool
}

// R4 and C2: in reviewing, the reviewer's approve of a head the tester did not pass counts the
// tester's pass only where its testedCommit names the commit of that pass. One that names none, or
// another commit, carries no pass: the issue goes back to testing, at the reviewer's request, with
// the tester's pass cleared. An approve of the head the tester passed needs no carry, and a changes
// is the round's refusal, which sends the work to the implementer as before.
func TestTheReviewersApproveInReviewingCarriesOnlyTheTestersRecordedPass(t *testing.T) {
	for _, tc := range []struct {
		name    string
		head    string
		verdict string
		tested  string
		want    phase.Phase
	}{
		{name: "an approve of the head the tester passed", head: headOne, verdict: "approve", want: phase.Accepting},
		{name: "an approve carrying the tester's pass to a pushed head", head: headTwo, verdict: "approve", tested: headOne, want: phase.Accepting},
		{name: "an approve of a pushed head naming no testedCommit", head: headTwo, verdict: "approve", want: phase.Testing},
		{name: "an approve of a pushed head naming a commit the tester did not pass", head: headTwo, verdict: "approve", tested: headEarlier, want: phase.Testing},
		{name: "a changes of a pushed head", head: headTwo, verdict: "changes", want: phase.Implementing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := seedReviewingAt(t, tc.head)
			applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), reviewerVerdict(tc.verdict, tc.head, tc.tested))
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("after the reviewer's %s of %s carrying %q the issue is in %s, want %s", tc.verdict, tc.head, tc.tested, got, tc.want)
			}
			if _, head, tested := storedDecision(t, pool); tc.want != phase.Testing && (head != tc.head || tested != tc.tested) {
				t.Fatalf("the round's decision is at %q carrying %q, want at %s carrying %q", head, tested, tc.head, tc.tested)
			}
			if given, found := recordedVerdict(t, pool, tc.head); !found || string(given.Verdict) != tc.verdict || given.TestedCommit != tc.tested {
				t.Fatalf("the store's verdict at %s = %+v (found %t), want the reviewer's %s carrying %q", tc.head, given, found, tc.verdict, tc.tested)
			}
			if tc.want != phase.Testing {
				return
			}
			if tester := roleRow(t, pool, claim.RoleTester); tester.Verdict != "" {
				t.Fatalf("the tester's row = %+v, want its pass cleared", tester)
			}
			if task := startTask(t, pool, claim.RoleTester); !strings.Contains(task, "test "+tc.head) {
				t.Fatalf("the tester's task %q, want it to name the head to test, %s", task, tc.head)
			}
			if want := "Legion moved this issue from reviewing back to testing at the reviewer's request: the reviewer's approve of " + tc.head; !strings.HasPrefix(movePost(t, pool), want) {
				t.Fatalf("the move posts %q on the issue, want it to open %q", movePost(t, pool), want)
			}
		})
	}
}

// movePost is what the status write of LEGION-208's move posts on the issue before its status
// (moveReason), "" for none.
func movePost(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var posted string
	if err := pool.QueryRow(t.Context(), `select coalesce(max(payload->>'reason'), '') from outbox
		where kind = 'dispatch_status' and issue = 'LEGION-208'`).Scan(&posted); err != nil {
		t.Fatalf("read the status write: %v", err)
	}
	return posted
}

// An ask past review (record.MergeForwardReview) is delivered while the issue stands past its
// review and its pull request stands at the head the ask is about, whether a merge forward or any
// other push moved it there; a later push, which asks again, or a move out of the phases past
// review ends it undelivered. The other steps of a merge forward keep their rule: an ask with no
// merge forward recorded is no longer delivered.
func TestAnAskPastReviewIsDeliveredWhileItsHeadStands(t *testing.T) {
	left := func(t *testing.T, pool *pgxpool.Pool, step record.MergeForwardStep, head, landed string) string {
		t.Helper()
		var why string
		seedRecord(t, pool, func(tx pgx.Tx) (err error) {
			_, _, why, err = MergeForwardAt(t.Context(), record.NewStore(), tx, "LEGION-208", step, head, landed)
			return err
		})
		return why
	}
	pool := seedPastReview(t, phase.Accepting, "needs_review")
	if why := left(t, pool, record.MergeForwardReview, headOne, headTwo); why != "" {
		t.Fatalf("the ask of a verdict at %s is not delivered (%s), want it delivered", headTwo, why)
	}
	if why := left(t, pool, record.MergeForwardAsk, headTwo, ""); why != "the record holds no merge forward" {
		t.Fatalf("an ask of a merge forward the record does not hold is left as %q, want the merge forward's rule", why)
	}
	applyRefusingNothing(t, pool, testEngine(config.DesignGateRootIssues, nil), intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", Before: headTwo, HeadSHA: headThree})
	if why := left(t, pool, record.MergeForwardReview, headOne, headTwo); !strings.Contains(why, headThree) {
		t.Fatalf("the ask at %s after a push to %s is left as %q, want it undelivered, naming %s", headTwo, headThree, why, headThree)
	}
	standsIn(t, pool, phase.Testing, "testing")
	if why := left(t, pool, record.MergeForwardReview, headTwo, headThree); !strings.Contains(why, "past its review") {
		t.Fatalf("the ask at %s once the issue went back to testing is left as %q, want it undelivered", headThree, why)
	}
}

// verdictAsks is every ask of a verdict past review LEGION-208's outbox holds (the merge-forward
// review rows), in the order written.
func verdictAsks(t *testing.T, pool *pgxpool.Pool) []record.MergeForwardMessage {
	t.Helper()
	asks := []record.MergeForwardMessage{}
	for _, message := range mergeForwardMessages(t, pool) {
		if message.Step == record.MergeForwardReview {
			asks = append(asks, message)
		}
	}
	return asks
}

// Spec R5: each push past review asks the round's recorded reviewer for its verdict at the new
// head, from the head the reviewer last gave its verdict on, so two pushes before the reviewer
// answers each ask about the whole range the reviewer has not judged: from headOne, where its
// approve stands, to headTwo and then to headThree, and never from headTwo, which no verdict
// named. Once the reviewer's verdict names headThree, the next push asks from headThree.
func TestAnAskPastReviewNamesTheHeadOfTheReviewersLastVerdict(t *testing.T) {
	pool := seedPastReview(t, phase.Retro, "retro")
	engine := testEngine(config.DesignGateRootIssues, nil)
	push := func(from, to string) {
		t.Helper()
		applyRefusingNothing(t, pool, engine, intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42,
			Branch: "legion/LEGION-208", Base: "main", Before: from, HeadSHA: to})
	}
	ask := func(from, to string) record.MergeForwardMessage {
		return record.MergeForwardMessage{Role: claim.RoleReviewer, Step: record.MergeForwardReview, Head: from, Landed: to, Base: "main",
			Phase: phase.Retro, PullRequest: 42}
	}
	push(headTwo, headThree)
	if got, want := verdictAsks(t, pool), []record.MergeForwardMessage{ask(headOne, headThree)}; !slices.Equal(got, want) {
		t.Fatalf("the second push asked %+v, want %+v: the range from the head of the reviewer's last verdict", got, want)
	}
	applyRefusingNothing(t, pool, engine, pastReviewVerdict("approve", headThree, headOne))
	push(headThree, headEarlier)
	if got, want := verdictAsks(t, pool), []record.MergeForwardMessage{ask(headOne, headThree), ask(headThree, headEarlier)}; !slices.Equal(got, want) {
		t.Fatalf("the push after the verdict at %s asked %+v, want %+v", headThree, got, want)
	}
}

// Spec R5: the merger merges a head that moved past review only once legion-verdict at that head
// is success. So the merger started in merging is told so whenever the head it starts at is not
// the head of the reviewer's last verdict, a push of any kind having moved it there, and is told
// nothing of it once the reviewer's verdict names that head.
func TestTheMergerWaitsForTheVerdictAtAHeadAnyPushMovedPastReview(t *testing.T) {
	const waits = " only once legion-verdict at that head is success."
	for _, tc := range []struct {
		name    string
		verdict bool
	}{{name: "a push past review the reviewer has not judged"}, {name: "the reviewer's verdict at the pushed head", verdict: true}} {
		t.Run(tc.name, func(t *testing.T) {
			pool := seedPastReview(t, phase.Retro, "retro")
			engine := testEngine(config.DesignGateRootIssues, nil)
			if tc.verdict {
				applyRefusingNothing(t, pool, engine, pastReviewVerdict("approve", headTwo, headOne))
			}
			applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 1, Issue: "LEGION-208", Role: claim.RoleImplementer,
				Claim: "implement-claim", Summary: "retro done", Commit: headTwo})
			if got := issuePhase(t, pool); got != phase.Merging {
				t.Fatalf("after retro's completion the issue is in %s, want merging", got)
			}
			task := startTask(t, pool, claim.RoleMerger)
			if told := strings.Contains(task, "merge head "+headTwo+waits); told == tc.verdict {
				t.Fatalf("the merger's task reads %q; told to wait for legion-verdict at %s = %t, want %t", task, headTwo, told, !tc.verdict)
			}
			if !tc.verdict && !strings.Contains(task, "the reviewer's verdict at head "+headOne) {
				t.Fatalf("the merger's task reads %q, want it to name %s, the head of the reviewer's last verdict", task, headOne)
			}
		})
	}
}

// A round held from reviewing can become stuck on GitHub's merge state while held: the reviewer
// approved the head, and the read GitHub answers `blocked` for it lands during the hold. The retry
// that ends the hold is then the fact that tells the architect, and its review-stuck notice names
// the round's facts as its summary: the head, the reviewer, and how long ago the reviewer gave its
// verdict, never an empty summary.
func TestARetriedRoundStuckOnItsMergeStateTellsTheRoundsFacts(t *testing.T) {
	pool := migratedPool(t)
	seedMergeStateRound(t, pool, record.MergeStateUnread)
	apply := applyFacts(t, pool, testEngine(config.DesignGateRootIssues, nil))
	apply("verdict", reviewerVerdict("approve", headOne, ""))
	apply("failed", intake.ClaimFailed{Issue: "LEGION-208", Role: claim.RoleReviewer})
	apply("blocked", intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: headOne,
		Mergeable: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked})
	if told := reviewStuckNotices(t, pool); len(told) != 0 {
		t.Fatalf("review-stuck notices before the retry = %+v, want none: the round was held", told)
	}
	later := applyFacts(t, pool, mergingEngineAt(time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)))
	later("retry", intake.RetryOrEscalate{Issue: "LEGION-208", Decision: intake.RetryDecision})
	told := reviewStuckNotices(t, pool)
	if len(told) != 1 {
		t.Fatalf("review-stuck notices after the retry = %+v, want one", told)
	}
	for _, want := range []string{"head " + headOne, "reviewer " + string(reviewClaim), "2h0m ago"} {
		if !strings.Contains(told[0].Summary, want) {
			t.Fatalf("the notice's summary is %q, want it to name %q", told[0].Summary, want)
		}
	}
}
