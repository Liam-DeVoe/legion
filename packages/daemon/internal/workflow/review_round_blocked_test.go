package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/classify"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// githubFake is GitHub as the real engine reaches it for LEGION-208's pull request #42 through the
// one call the daemon makes on the commit of a fact for it here: a read of the head's merge state
// (Config.ReadMergeState). The engine asks for it on the commit, and the fake answers once the
// fact's transaction is over, as the daemon's reader runs after it (serve), each answer applied as
// the daemon's read applies GitHub's answer (intake.PullRequestMergeability).
type githubFake struct {
	t      *testing.T
	pool   *pgxpool.Pool
	engine *Engine
	// mergeable, state and requested are GitHub's answer for the head the branch stands at:
	// requested is how many review requests it holds open on the pull request.
	mergeable record.Mergeability
	state     record.MergeState
	requested int
	// reads is how many reads the engine has asked for that GitHub has not answered.
	reads int
	// answered is each state GitHub answered a read with, in order, and at the moment of the last
	// answer: each read is a minute after the one before, so the record takes every one of them
	// (StateReadAt).
	answered []record.MergeState
	at       time.Time
}

// newGitHubFake is the fake GitHub of pool's LEGION-208, wired into engine as its reader of the
// merge state. GitHub answers MERGEABLE and state until a test says otherwise.
func newGitHubFake(t *testing.T, pool *pgxpool.Pool, engine *Engine, state record.MergeState) *githubFake {
	g := &githubFake{t: t, pool: pool, engine: engine, mergeable: record.MergeabilityMergeable, state: state,
		at: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC)}
	engine.cfg.ReadMergeState = func(string) { g.reads++ }
	return g
}

// apply applies fact through the engine, fails the test on a refusal, and then serves the reads
// the fact's commit asked GitHub for.
func (g *githubFake) apply(id string, fact intake.Fact) {
	g.t.Helper()
	if result := applyFacts(g.t, g.pool, g.engine)(id, fact); result.Refusal != nil {
		g.t.Fatalf("%s was refused: %+v", id, result.Refusal)
	}
	g.serve()
}

// serve answers each read asked for, until nothing the engine asked is left: a read that GitHub
// answers is a fact of its own, which can ask again.
func (g *githubFake) serve() {
	g.t.Helper()
	for g.reads > 0 {
		g.reads--
		g.read()
	}
}

// read is one read of #42 answered by GitHub, a minute after the read before it, about the head
// the branch stands at, applied as the daemon applies its read.
func (g *githubFake) read() {
	g.t.Helper()
	head := recordedPR(g.t, g.pool, "LEGION-208").HeadSHA
	g.at = g.at.Add(conflictConfirmWait)
	g.answered = append(g.answered, g.state)
	if result := applyFacts(g.t, g.pool, g.engine)("read-"+g.at.Format(time.RFC3339), intake.PullRequestMergeability{
		Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: head, Mergeable: g.mergeable, MergeState: g.state,
		ReviewsRequested: g.requested, ReadAt: g.at}); result.Refusal != nil {
		g.t.Fatalf("GitHub's answer %s for %s was refused: %+v", g.state, head, result.Refusal)
	}
}

// waitsOnGitHub is whether LEGION-208's round waits on GitHub's answer for its head as the record
// holds it now: the rule the state build reads for the issue's mergeWait
// (classify.RoundWaitsOnMergeState, projection's mergeWaitView), so it is what `legion state`
// shows.
func waitsOnGitHub(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	pr := recordedPR(t, pool, "LEGION-208")
	return classify.RoundWaitsOnMergeState(*issueRecord(t, pool, "LEGION-208"), reviewerRow(t, pool), &pr)
}

// standsUntold fails the test unless LEGION-208 stands in want and no review-stuck notice has gone
// out.
func standsUntold(t *testing.T, pool *pgxpool.Pool, want phase.Phase, why string) {
	t.Helper()
	if got := issuePhase(t, pool); got != want {
		t.Fatalf("LEGION-208 is in %s, want %s: %s", got, want, why)
	}
	if told := reviewStuckNotices(t, pool); len(told) != 0 {
		t.Fatalf("the architect was told the round was stuck %+v, want nothing: %s", told, why)
	}
}

// waits fails the test unless LEGION-208's round waits in reviewing on GitHub, untold, and the
// issue's view would carry the wait.
func waits(t *testing.T, pool *pgxpool.Pool, why string) {
	t.Helper()
	standsUntold(t, pool, phase.Reviewing, why)
	if !waitsOnGitHub(t, pool) {
		t.Fatalf("the round waits on no answer of GitHub's, want the wait the state shows: %s", why)
	}
}

// GitHub's merge state is an answer to the reader, not GitHub's verdict on the merge, and `blocked`
// is the one state the daemon cannot read past: it covers a rule about the reader (a token without
// the workflows permission reads `blocked` for every pull request that changes
// .github/workflows/, dispatch://INFRA-4192/comment/1c89885e-5e89-42f7-8122-9cdc697d0ccd), a check
// suite of another pull request on the same commit that holds a required check `expected`, and
// rules the daemon reads nothing of. The merger's enqueue is the call GitHub answers about the
// merge itself, accepted or refused with its reason. So an approval that stands on the head ends
// the round on `blocked` with the head MERGEABLE and no review request open as it does on a state
// GitHub would merge from, once GitHub has reported a check run on that head and every one of
// them has concluded (classify.ChecksConcludedOnHead), whatever each concluded to, and the merger
// answers the state at merging. The round still waits while GitHub has given no answer, unread or
// `unknown`, while a check run of the head is still running, and while GitHub holds a review
// request open, which is told once; `behind` and `draft` keep the stuck round and its notice, told
// once.
func TestAnApprovalOfABlockedHeadWhoseChecksConcludedMovesToAccepting(t *testing.T) {
	// rig is LEGION-208 in reviewing, its pull request #42 at headOne with CI settled there and no
	// merge state read (seedMergeStateRound), applied through the real engine and the fake GitHub.
	rig := func(t *testing.T, state record.MergeState) (*pgxpool.Pool, *githubFake) {
		t.Helper()
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnread)
		return pool, newGitHubFake(t, pool, testEngine(config.DesignGateRootIssues, nil), state)
	}
	approve := intake.PullRequestReview{Repo: "sjawhar/legion", Number: 42, State: "approved", CommitID: headOne,
		HeadSHA: headOne, Author: testReviewApp, Body: reviewBody("ship it", headOne)}

	t.Run("GitHub answers blocked once every check run on the head concluded", func(t *testing.T) {
		pool, github := rig(t, record.MergeStateBlocked)
		github.apply("approve", approve)
		if len(github.answered) != 1 || github.answered[0] != record.MergeStateBlocked {
			t.Fatalf("GitHub answered %v, want the one read the approval asked for, answered blocked", github.answered)
		}
		standsUntold(t, pool, phase.Accepting, "the approval stands on a blocked, mergeable head whose checks concluded")
	})

	// A head approved at the head, MERGEABLE, with no review request, whose every check run
	// concluded, a cancelled run and a failed one of another pull request's check suite on the
	// same commit among them: GitHub answers `blocked` to every writer and refuses the enqueue with
	// `Required status check "gate" is expected`, though the head's own required check passed.
	// Its round ends, and the merger meets that refusal.
	t.Run("a blocked head whose runs concluded, cancelled and failed among them, with no review asked for", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnread)
		seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
			Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne, Failing: []string{"required"}, Cancelled: []string{"tests"},
			CheckedHead: headOne, CheckRuns: []record.AttemptRun{
				{Name: "gate", ID: 1, Head: headOne, Conclusion: record.RunSucceeded},
				{Name: "required", ID: 2, Head: headOne, Conclusion: record.RunFailed},
				{Name: "tests", ID: 3, Head: headOne, Conclusion: record.RunCancelled},
				{Name: "workflow-size", ID: 4, Head: headOne, Conclusion: record.RunSucceeded}}})
		if _, err := pool.Exec(t.Context(), "update pull_requests set mergeability = '', merge_state = '' where issue = 'LEGION-208'"); err != nil {
			t.Fatalf("clear GitHub's answer on the seeded pull request: %v", err)
		}
		github := newGitHubFake(t, pool, testEngine(config.DesignGateRootIssues, nil), record.MergeStateBlocked)
		github.apply("approve", approve)
		if len(github.answered) != 1 || github.answered[0] != record.MergeStateBlocked {
			t.Fatalf("GitHub answered %v, want the one read the approval asked for, answered blocked", github.answered)
		}
		standsUntold(t, pool, phase.Accepting, "every check run of the head concluded, a failed and a cancelled one among them, and no review is asked for")
		if waitsOnGitHub(t, pool) {
			t.Fatalf("the round still waits on GitHub after it ended, want no wait on the issue's view")
		}
	})

	t.Run("GitHub holds a review request open on a blocked head whose runs concluded", func(t *testing.T) {
		pool, github := rig(t, record.MergeStateBlocked)
		github.requested = 1
		github.apply("approve", approve)
		if got := issuePhase(t, pool); got != phase.Reviewing {
			t.Fatalf("LEGION-208 is in %s, want reviewing: a review GitHub asked for is not given yet", got)
		}
		if !waitsOnGitHub(t, pool) {
			t.Fatalf("the round waits on no answer of GitHub's, want the wait the state shows while a review is asked for")
		}
		told := reviewStuckNotices(t, pool)
		if len(told) != 1 || !strings.Contains(told[0].Reason, "GitHub reports that head blocked rather than one it would merge") ||
			!strings.Contains(told[0].Reason, "GitHub holds 1 review request(s) open on the pull request") {
			t.Fatalf("review-stuck notices %+v, want one naming blocked and the open review request", told)
		}
		if stuck := stuckRoleMessages(t, pool); len(stuck) != 1 {
			t.Fatalf("the reviewer was told %d times %+v, want once", len(stuck), stuck)
		}
		// A second read with the request still open tells nobody again.
		github.read()
		if again := reviewStuckNotices(t, pool); len(again) != 1 || issuePhase(t, pool) != phase.Reviewing {
			t.Fatalf("after a second read the notices are %+v in %s, want the one notice and reviewing", again, issuePhase(t, pool))
		}
		// The requested review is given and GitHub holds no request open: the next read ends the
		// round on the same `blocked`.
		github.requested = 0
		github.read()
		if got := issuePhase(t, pool); got != phase.Accepting {
			t.Fatalf("LEGION-208 is in %s, want accepting: no review is asked for any more and every check run concluded", got)
		}
		if again := reviewStuckNotices(t, pool); len(again) != 1 {
			t.Fatalf("the architect was told %d times (%+v), want the one notice of the review request", len(again), again)
		}
	})

	t.Run("GitHub's answer unread", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnread)
		engine := testEngine(config.DesignGateRootIssues, nil)
		github := newGitHubFake(t, pool, engine, record.MergeStateBlocked)
		if result := applyFacts(t, pool, engine)("approve", approve); result.Refusal != nil {
			t.Fatalf("the approval was refused: %+v", result.Refusal)
		}
		if github.reads != 1 {
			t.Fatalf("the approval asked GitHub for %d reads, want one: the round waits on GitHub's answer", github.reads)
		}
		waits(t, pool, "GitHub has answered nothing for the head")
		github.serve()
		standsUntold(t, pool, phase.Accepting, "GitHub answered blocked for a mergeable head whose checks concluded")
	})

	t.Run("GitHub answers unknown", func(t *testing.T) {
		pool, github := rig(t, record.MergeStateUnknown)
		github.apply("approve", approve)
		waits(t, pool, "GitHub is still computing the merge state")
		github.state = record.MergeStateBlocked
		github.read()
		standsUntold(t, pool, phase.Accepting, "the read after unknown answered blocked")
	})

	t.Run("a check run of the head is still running", func(t *testing.T) {
		pool := migratedPool(t)
		seedMergeStateRound(t, pool, record.MergeStateUnread)
		// CI settled the head before the reviewer's push; the head the approval stands on has no
		// settlement of its own, which is what a CI run still going there leaves.
		seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
			Number: 42, Branch: "legion/LEGION-208", HeadSHA: headOne, Failing: []string{}, Cancelled: []string{},
			Mergeability: record.MergeabilityMergeable, MergeState: record.MergeStateBlocked,
			CheckedHead: headEarlier, CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 1, Head: headEarlier, Conclusion: record.RunSucceeded}}})
		github := newGitHubFake(t, pool, testEngine(config.DesignGateRootIssues, nil), record.MergeStateBlocked)
		github.apply("approve", approve)
		if len(github.answered) != 1 {
			t.Fatalf("GitHub answered %v, want the one read the approval asked for", github.answered)
		}
		waits(t, pool, "GitHub answers blocked while a check run of the head runs")
		// The last check concludes: the settlement asks for a read, and GitHub still answers blocked.
		github.apply("settled", intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: headOne, Generation: 1, Snapshot: "settled",
			CheckRuns: []record.AttemptRun{{Name: "unit tests", ID: 2}}, Failing: []string{}, Cancelled: []string{}})
		if len(github.answered) != 2 || github.answered[1] != record.MergeStateBlocked {
			t.Fatalf("GitHub answered %v, want a second read, asked by the settlement, answered blocked", github.answered)
		}
		standsUntold(t, pool, phase.Accepting, "every check run of the head has concluded and GitHub answers blocked")
	})

	for _, state := range []record.MergeState{record.MergeStateBehind, record.MergeStateDraft} {
		t.Run("GitHub answers "+string(state), func(t *testing.T) {
			pool, github := rig(t, state)
			github.apply("approve", approve)
			if got := issuePhase(t, pool); got != phase.Reviewing {
				t.Fatalf("LEGION-208 is in %s, want reviewing: GitHub will not merge a head it answers %s", got, state)
			}
			told := reviewStuckNotices(t, pool)
			if len(told) != 1 || !strings.Contains(told[0].Reason, "GitHub reports that head "+string(state)+" rather than one it would merge") {
				t.Fatalf("review-stuck notices %+v, want one naming %s (stuckMergeState)", told, state)
			}
			if stuck := stuckRoleMessages(t, pool); len(stuck) != 1 {
				t.Fatalf("the reviewer was told %d times %+v, want once", len(stuck), stuck)
			}
			if !waitsOnGitHub(t, pool) {
				t.Fatalf("the stuck round waits on no answer of GitHub's, want the wait the state shows")
			}
			github.read()
			if again := reviewStuckNotices(t, pool); len(again) != 1 || issuePhase(t, pool) != phase.Reviewing {
				t.Fatalf("after a second %s read the notices are %+v in %s, want the one notice and reviewing", state, again, issuePhase(t, pool))
			}
		})
	}
}
