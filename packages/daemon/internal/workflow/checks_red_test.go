package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/texts"
)

// A CI settlement moves no issue, in any phase (dispatch://INFRA-714/spec R1): a failed check is
// information the worker holding the phase decides on - re-run, wait, or fix - and not the
// daemon's to read the semantics of. What the settlement says is told instead, as one ci-settled
// message per role to three roles: the worker holding the issue's phase, the implementer, whose
// code the checks ran on, and the architect that owns the issue, here the root's, since the issue
// is the tree's root. Each message names each check that did not pass with its conclusion, counts
// the passed ones, and goes at the urgency the settlement's rule gives its reader in that phase.
// A role named twice - the implementer holding the phase, the architect holding admitted or
// accepting - is told once, and a phase no worker holds names none. Nothing else happens: no
// phase moves, no architect hears checks-red, no worker is started, and no return is counted.
func TestASettlementMovesNothingAndTellsTheThreeRoles(t *testing.T) {
	for _, tc := range []struct {
		from   phase.Phase
		status string
		// roles are the roles told, in the order tellChecks addresses them.
		roles []claim.Role
	}{
		{phase.Admitted, "in_progress", []claim.Role{claim.RoleArchitect, claim.RoleImplementer}},
		{phase.Implementing, "in_progress", []claim.Role{claim.RoleArchitect, claim.RoleImplementer}},
		{phase.Testing, "testing", []claim.Role{claim.RoleArchitect, claim.RoleTester, claim.RoleImplementer}},
		{phase.Reviewing, "needs_review", []claim.Role{claim.RoleArchitect, claim.RoleReviewer, claim.RoleImplementer}},
		{phase.Accepting, "needs_review", []claim.Role{claim.RoleArchitect, claim.RoleImplementer}},
		{phase.Retro, "retro", []claim.Role{claim.RoleArchitect, claim.RoleImplementer}},
		{phase.Merging, "retro", []claim.Role{claim.RoleArchitect, claim.RoleMerger, claim.RoleImplementer}},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			pool := migratedPool(t)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
				Phase: tc.from, Generation: 1, Status: tc.status, Rank: "U"})
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
			if role := RoleFor(tc.from); role != "" && role != claim.RoleImplementer {
				seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: role, Claim: claim.Token(string(role) + "-claim")})
			}
			applyRefusingNothing(t, pool, mergingEngine(), settledAt("head", 1, "unit"))

			if got := issuePhase(t, pool); got != tc.from {
				t.Fatalf("after the settlement the issue is in %s, want it left in %s: a CI result moves nothing", got, tc.from)
			}
			if got := noticeKinds(t, pool, "LEGION-208"); len(got) != 0 {
				t.Fatalf("the architect was told %v, want nothing but the role message", got)
			}
			assertOutboxCount(t, pool, "supervise", 0)
			assertOutboxCount(t, pool, "dispatch_status", 0)
			if pass := implementerPass(t, pool); pass != 0 {
				t.Fatalf("pass = %d, want none counted: nothing went back", pass)
			}
			assertRoleMessages(t, pool, "LEGION-208", settledTold(t, tc.roles, tc.from, "head", 1, failed("unit")))
		})
	}
}

// The message is the whole of what the daemon knows of the head's CI, so a settlement that leaves
// it the same says nothing new and tells nobody again: the listener redelivers a settlement, and a
// re-run that ends as the run before it did settles the same head with the same conclusions. A
// settlement that changes a conclusion is told, so a check flapping red, green and red again tells
// each change - and moves nothing any of the three times, where the rule this replaced sent the
// work back on the first red and could not take it back.
func TestASettlementThatSaysNothingNewTellsNobodyAgain(t *testing.T) {
	pool := migratedPool(t)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim"})
	engine := mergingEngine()
	messages := []roleTold{}
	testers := []claim.Role{claim.RoleArchitect, claim.RoleTester, claim.RoleImplementer}
	for i, step := range []struct {
		fact intake.Fact
		// told is what the roles are told, nil when the settlement says nothing new.
		told []roleTold
	}{
		{settledAt("head", 1, "unit"), settledTold(t, testers, phase.Testing, "head", 1, failed("unit"))},
		{settledAt("head", 2, "unit"), nil},
		{settledAt("head", 3), settledTold(t, testers, phase.Testing, "head", 2)},
		{settledAt("head", 4, "unit"), settledTold(t, testers, phase.Testing, "head", 1, failed("unit"))},
	} {
		applyRefusingNothing(t, pool, engine, step.fact)
		messages = append(messages, step.told...)
		if got := issuePhase(t, pool); got != phase.Testing {
			t.Fatalf("after settlement %d the issue is in %s, want testing", i+1, got)
		}
		assertRoleMessages(t, pool, "LEGION-208", messages)
	}
}

// A check GitHub produced no run for - an aggregate job whose needs: never ran, so it never
// started - reaches the three roles like any other conclusion, and moves nothing, because nothing
// classifies it (dispatch://INFRA-622). The settlement reports its runs and names that check as
// failed; the message names it beside the checks that ran, and the worker holding the phase
// decides what it means.
func TestACheckGitHubNeverRanIsToldLikeAnyOtherConclusion(t *testing.T) {
	pool := migratedPool(t)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim"})
	applyRefusingNothing(t, pool, mergingEngine(), intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: "head",
		CheckRuns: []record.AttemptRun{{Name: "unit", ID: 1}}, Generation: 1, Snapshot: "never-ran",
		Failing: []string{"pr-checks-result"}})

	if got := issuePhase(t, pool); got != phase.Testing {
		t.Fatalf("after the settlement the issue is in %s, want testing: nothing classifies a conclusion", got)
	}
	if got := noticeKinds(t, pool, "LEGION-208"); len(got) != 0 {
		t.Fatalf("the architect was told %v, want nothing but the role message", got)
	}
	assertOutboxCount(t, pool, "supervise", 0)
	assertRoleMessages(t, pool, "LEGION-208", settledTold(t, []claim.Role{claim.RoleArchitect, claim.RoleTester, claim.RoleImplementer},
		phase.Testing, "head", 1, failed("pr-checks-result")))
}

// Linger holds a member of a closed tree where it stood (record.TreeLingers): every role of the
// tree is suspended there, so a settlement that reaches a lingering tree's issue is recorded and
// nobody is told - a message to a role nobody holds reaches nobody, and the tree is done.
func TestASettlementOfALingeringTreeTellsNobody(t *testing.T) {
	pool := migratedPool(t)
	until := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Done, Generation: 1, Status: "done", Rank: "U", LingerUntil: &until})
	parent := "LEGION-208"
	seedIssue(t, pool, record.Issue{Key: "LEGION-209", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Parent: &parent,
		Project: "LEGION", Title: "child", Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "V"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-209", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-209", HeadSHA: "head"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleImplementer, Claim: "implement-claim"})
	applyRefusingNothing(t, pool, mergingEngine(), settledAt("head", 1, "unit"))

	assertRoleMessages(t, pool, "LEGION-209", nil)
	if checked := recordedPR(t, pool, "LEGION-209").CheckedHead; checked != "head" {
		t.Fatalf("the settlement stands for %q, want it recorded at head while the tree lingers", checked)
	}
}

// A settlement of a commit the head's settlement is not - an earlier head the branch has left -
// stands for no head, so there is nothing to tell: the daemon knows nothing of the head's CI, and
// a message naming another commit's checks would be read as the head's.
func TestASettlementThatStandsForNoHeadTellsNobody(t *testing.T) {
	pool := migratedPool(t)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	applyRefusingNothing(t, pool, mergingEngine(), settledAt("another-commit", 1, "unit"))

	assertRoleMessages(t, pool, "LEGION-208", nil)
	if got := issuePhase(t, pool); got != phase.Testing {
		t.Fatalf("the issue is in %s, want testing", got)
	}
}

// A push recorded after its head arrived completes the chain of the move that left the code head
// behind, and so does the head arriving over a push already known. Either way nothing moves and
// nobody is told again: the roles were told what the settlement said when it arrived, and it goes
// on standing for the code head it names. The rule this replaced sent the work back from whatever
// phase the move completed in.
func TestAPushThatCompletesItsMoveMovesNothingAndTellsNobody(t *testing.T) {
	for _, tc := range []struct {
		name string
		// head is the pull request's recorded head before the fact, and fact the one that
		// completes the move.
		head string
		fact intake.Fact
	}{
		{"the push arrives after its head", "head", pushedFromTo("code", "head", "legion-implementer[bot]")},
		{"the head arrives over a push already known", "code", syncedTo("head")},
	} {
		for _, from := range []phase.Phase{phase.Testing, phase.Merging} {
			t.Run(tc.name+", in "+string(from), func(t *testing.T) {
				pool := migratedPool(t)
				status := map[phase.Phase]string{phase.Testing: "testing", phase.Merging: "retro"}[from]
				seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
					Phase: from, Generation: 1, Status: status, Rank: "U"})
				pr := record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
					Number: 42, Branch: "legion/LEGION-208", HeadSHA: tc.head, CheckedHead: "code",
					CheckRuns: []record.AttemptRun{{Name: "unit", ID: 1}}, Failing: []string{"unit"}}
				if tc.head == "code" {
					pr.Pushes = []record.ClassifiedPush{{SHA: "head", Before: "code"}}
				}
				seedPR(t, pool, pr)
				seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
				applyRefusingNothing(t, pool, mergingEngine(), tc.fact)

				if got := recordedPR(t, pool, "LEGION-208"); got.HeadSHA != "head" || got.CheckedHead != "code" {
					t.Fatalf("after the move the head is %q and its settlement is of %q, want head above the code head's settlement", got.HeadSHA, got.CheckedHead)
				}
				if got := issuePhase(t, pool); got != from {
					t.Fatalf("after the move the issue is in %s, want it left in %s", got, from)
				}
				assertRoleMessages(t, pool, "LEGION-208", nil)
				if got := noticeKinds(t, pool, "LEGION-208"); len(got) != 0 {
					t.Fatalf("the architect was told %v, want nothing", got)
				}
				assertOutboxCount(t, pool, "supervise", 0)
			})
		}
	}
}

// A reopen starts the pull request's CI settlement afresh (classify.Reopen): the settlement
// recorded before the close is of a head GitHub has run nothing on since, so the daemon knows
// nothing of the reopened head's CI until one arrives. The first settlement after the reopen is
// therefore told, even when it says what the settlement before the close said.
func TestAReopenStartsTheCheckSettlementAfreshAndTellsItAgain(t *testing.T) {
	pool := migratedPool(t)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Testing, Generation: 1, Status: "testing", Rank: "U"})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head", HeadUpdatedAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleTester, Claim: "test-claim"})
	engine := mergingEngine()
	applyRefusingNothing(t, pool, engine, settledAt("head", 1, "unit"))
	applyRefusingNothing(t, pool, engine,
		intake.PullRequestClosed{Repo: "sjawhar/legion", Number: 42, HeadSHA: "head", UpdatedAt: time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)},
		intake.PullRequestOpened{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head",
			UpdatedAt: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC), Reopened: true},
		settledAt("head", 2, "unit"))

	once := settledTold(t, []claim.Role{claim.RoleArchitect, claim.RoleTester, claim.RoleImplementer}, phase.Testing, "head", 1, failed("unit"))
	assertRoleMessages(t, pool, "LEGION-208", append(once, once...))
}

// The implementer pushes and completes without waiting for CI, so a check can settle on its head
// while the issue is still in implementing. The completion reads none of it: the issue goes on to
// testing whatever the settlement said, since a CI result moves nothing and the roles were told
// what it said when it arrived. The rule this replaced read the settlement here and sent the work
// straight back to implementing rather than through a tester round.
func TestACompletionHandsOnASettledHeadWithoutReadingIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failing []string
	}{
		{"a check failed", []string{"unit"}},
		{"every check passed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
				Phase: phase.Implementing, Generation: 1, Status: "in_progress", Rank: "U"})
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
			engine := testEngine(config.DesignGateRootIssues, nil)
			applyRefusingNothing(t, pool, engine, settledAt("head", 1, tc.failing...))
			if got := issuePhase(t, pool); got != phase.Implementing {
				t.Fatalf("after the settlement the issue is in %s, want implementing", got)
			}
			applyRefusingNothing(t, pool, engine, intake.HandoffComplete{Generation: 1, Issue: "LEGION-208",
				Role: claim.RoleImplementer, Claim: "implement-claim", Commit: "head", Summary: "implemented",
				HandoffRecord: "implement-1", HandoffVersion: 1})

			if got := issuePhase(t, pool); got != phase.Testing {
				t.Fatalf("after the implementer's completion the issue is in %s, want testing", got)
			}
			if got := noticeKinds(t, pool, "LEGION-208"); containsNotice(got, "checks-red") {
				t.Fatalf("notices %v, want no checks-red", got)
			}
			if pass := implementerPass(t, pool); pass != 0 {
				t.Fatalf("pass = %d, want none: nothing went back", pass)
			}
		})
	}
}

// A head that conflicts with its base sends its issue back to implementing from whatever later
// phase it stands in: GitHub computes no merge ref for a conflicting head, so it runs no checks on
// it at all and no CI result will ever arrive for it, and only the implementer merging the base
// forward clears it. The daemon's own read of GitHub's mergeability (workflow's mergeability) is
// the only path to it, since GitHub sends no event when it recomputes one. The move names the
// base the implementer must merge forward and counts the return as a pass, so the implementer's
// completion of the merge is told from its completion of the pass before. GitHub reports
// mergeability UNKNOWN until it has computed it - that
// is "not yet known", never a conflict - so an unknown or a mergeable read moves nothing, and
// neither does a conflict read in implementing, where the work it needs already is.
func TestAConflictingHeadSendsTheIssueBackToImplementingFromEveryLaterPhase(t *testing.T) {
	for _, tc := range []struct {
		name      string
		from      phase.Phase
		status    string
		mergeable record.Mergeability
		want      phase.Phase
	}{
		{"conflicting in testing", phase.Testing, "testing", record.MergeabilityConflicting, phase.Implementing},
		{"conflicting in reviewing", phase.Reviewing, "needs_review", record.MergeabilityConflicting, phase.Implementing},
		{"conflicting in accepting", phase.Accepting, "needs_review", record.MergeabilityConflicting, phase.Implementing},
		{"conflicting in retro", phase.Retro, "retro", record.MergeabilityConflicting, phase.Implementing},
		{"conflicting in merging", phase.Merging, "retro", record.MergeabilityConflicting, phase.Implementing},
		{"conflicting in implementing: the work it needs is already there", phase.Implementing, "in_progress", record.MergeabilityConflicting, phase.Implementing},
		{"unknown in merging: GitHub has not computed it yet", phase.Merging, "retro", record.MergeabilityUnknown, phase.Merging},
		{"mergeable in merging", phase.Merging, "retro", record.MergeabilityMergeable, phase.Merging},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
				Phase: tc.from, Generation: 1, Status: tc.status, Rank: "U"})
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
			applyRefusingNothing(t, pool, mergingEngine(),
				intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: "head", Mergeable: tc.mergeable})
			if got := issuePhase(t, pool); got != tc.want {
				t.Fatalf("after the mergeability read the issue is in %s, want %s", got, tc.want)
			}
			if tc.from == tc.want {
				assertOutboxCount(t, pool, "supervise", 0)
				if pass := implementerPass(t, pool); pass != 0 {
					t.Fatalf("pass = %d, want none counted: nothing went back", pass)
				}
				return
			}
			want := "the head conflicts with main: GitHub runs no checks on it; merge main forward"
			red := checksRedNotice(t, pool)
			var status, posted string
			if err := pool.QueryRow(context.Background(),
				"select payload->>'status', coalesce(payload->>'reason', '') from outbox where kind = 'dispatch_status'").Scan(&status, &posted); err != nil {
				t.Fatalf("read the status write: %v", err)
			}
			if task := implementerTask(t, pool); !strings.Contains(task, want) || status != "in_progress" {
				t.Fatalf("task %q, status %q; want the task to say %q and the issue in_progress", task, status, want)
			}
			if red.Phase != tc.from || red.ConflictsWith != "main" || red.Head != "head" || red.PullRequest != "sjawhar/legion#42" ||
				red.Issue != "LEGION-208" || red.Role != "" || red.Reason != "" {
				t.Fatalf("the checks-red notice is %+v, want LEGION-208 from %s with head head conflicting with main on sjawhar/legion#42, no role and no reason", red, tc.from)
			}
			// The move is the daemon's own, so no worker's act on Dispatch says why the issue went
			// back: the status write posts the reason on the issue first, naming the phase it left,
			// the pull request and head that conflict, and the base.
			wantPosted := fmt.Sprintf("Legion moved this issue from %s back to implementing, because pull request #42 at head head conflicts with main. "+
				"GitHub runs no checks on a head that conflicts with its base, so the implementer merges main forward.", tc.from)
			if posted != wantPosted {
				t.Fatalf("the status write posts %q on the issue, want %q", posted, wantPosted)
			}
			// The return is a pass and no review round: the implementer answers a conflict with a
			// forward merge nobody asked it for, so it costs the issue nothing against
			// review_round_cap.
			if pass, rounds := implementerPass(t, pool), implementerRounds(t, pool); pass != 1 || rounds != 0 {
				t.Fatalf("pass = %d, rounds = %d; want the return to implementing counted as a pass and as no round against review_round_cap", pass, rounds)
			}
		})
	}
}

// The handler decides on every CONFLICTING read, whether or not the record already said so: the
// read that found the conflict first may have run while the issue stood in a phase the trigger has
// no row for, and deciding only on a read that changes the stored value would leave that conflict
// unacted on for good - every later read of the same CONFLICTING answer "changes" nothing. What
// stops the repeat from acting twice is the move itself: implementing, where the conflict sends
// the issue, has no row for the trigger, so the same read again changes nothing and tells nobody
// a second time.
func TestEveryConflictingReadDecidesAndTheMoveItselfStopsTheRepeat(t *testing.T) {
	pool := migratedPool(t)
	engine := testEngine(config.DesignGateRootIssues, nil)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
		Phase: phase.Merging, Generation: 1, Status: "retro", Rank: "U"})
	seedGate(t, pool, record.DesignGate{Issue: "LEGION-208", ArtifactID: "artifact-208", LatestVersion: 1, ApprovedVersion: new(1)})
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
		Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
	seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleMerger, Claim: "merger-claim"})

	conflict := intake.PullRequestMergeability{Repo: "sjawhar/legion", Number: 42, Base: "main", HeadSHA: "head", Mergeable: record.MergeabilityConflicting}
	applyRefusingNothing(t, pool, engine, conflict)
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the conflicting read the issue is in %s, want implementing: the merger cannot merge a head GitHub will not", got)
	}

	applyRefusingNothing(t, pool, engine, conflict)
	if got := issuePhase(t, pool); got != phase.Implementing {
		t.Fatalf("after the same conflicting read again the issue is in %s, want implementing", got)
	}
	var notices int
	if err := pool.QueryRow(context.Background(), "select count(*) from outbox where kind = 'notice' and payload->>'kind' = 'checks-red'").Scan(&notices); err != nil {
		t.Fatalf("count the architect's checks-red notices: %v", err)
	}
	if red := checksRedNotice(t, pool); notices != 1 || red.ConflictsWith != "main" || red.Head != "head" {
		t.Fatalf("checks-red notices = %d, the notice %+v; want exactly one of head head conflicting with main", notices, red)
	}
	if pass := implementerPass(t, pool); pass != 1 {
		t.Fatalf("pass = %d, want the one return to implementing counted once", pass)
	}
}

// settledAt is a CI settlement of head reporting the two check runs build and unit, generation
// numbering it and its fence, with the checks named failing.
func settledAt(head string, generation int64, failing ...string) intake.Fact {
	return settledRunsAt(head, generation, []record.AttemptRun{{Name: "build", ID: generation}, {Name: "unit", ID: generation}}, failing...)
}

// settledRunsAt is a CI settlement of head reporting runs, generation numbering it among that
// head's own settlements, with the checks named failing. A check the settlement does not report
// at all is one it says nothing about: still running, or never seen there.
func settledRunsAt(head string, generation int64, runs []record.AttemptRun, failing ...string) intake.Fact {
	return intake.PullRequestChecks{Repo: "sjawhar/legion", Number: 42, HeadSHA: head, CheckRuns: runs,
		Generation: generation, Snapshot: head + "-" + strconv.FormatInt(generation, 10),
		Failing: append([]string{}, failing...)}
}

// codeHeadRuns is the runs of a code head's own CI at attempt: the two checks of a push, and
// pr-title, which GitHub runs for the pull request rather than for the code.
func codeHeadRuns(attempt int64) []record.AttemptRun {
	return []record.AttemptRun{{Name: "build", ID: attempt}, {Name: "pr-title", ID: attempt}, {Name: "unit", ID: attempt}}
}

// syncedTo is GitHub's synchronize of the pull request to head.
func syncedTo(head string) intake.Fact {
	return intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", HeadSHA: head}
}

// pushedFromTo is the branch push that replaced from with to, by pusher.
func pushedFromTo(from, to, pusher string) intake.Fact {
	return intake.Push{Repo: "sjawhar/legion", Branch: "legion/LEGION-208", Before: from, After: to, Pusher: pusher}
}

// failed is check, failed, as a ci-settled message names a check that did not pass.
func failed(check string) texts.CheckConclusion {
	return texts.CheckConclusion{Check: check, Conclusion: string(record.RunFailed)}
}

// roleTold is one role message a test expects: the role topic it goes to, the urgency it goes
// at, and the record it carries.
type roleTold struct {
	Role    string
	Urgency texts.Urgency
	Record  any
}

// settledTold is the ci-settled message of a settlement of LEGION-208's pull request 42 at head,
// in phase at, to each of roles at the urgency the settlement's rule gives it there: passed checks
// passed and the checks of red did not (workflow's tellChecks).
func settledTold(t *testing.T, roles []claim.Role, at phase.Phase, head string, passed int, red ...texts.CheckConclusion) []roleTold {
	t.Helper()
	told := make([]roleTold, 0, len(roles))
	for _, role := range roles {
		told = append(told, roleTold{Role: roleToken(t, role), Urgency: texts.UrgencyOf(texts.CISettled, role, texts.Variant{Phase: at}),
			Record: texts.CISettledRecord{RoleHead: texts.RoleHead{Kind: texts.CISettled, Issue: "LEGION-208"}, PullRequest: "sjawhar/legion#42",
				Head: head, Phase: at, NotPassed: red, Passed: passed, ListPassed: "gh pr checks 42 --repo sjawhar/legion"}})
	}
	return told
}

// roleToken is the Envoy role token of LEGION-208's role, the address a role message carries.
func roleToken(t *testing.T, role claim.Role) string {
	t.Helper()
	token, err := claim.NewToken("legion", "LEGION-208", role)
	if err != nil {
		t.Fatalf("role token of %s: %v", role, err)
	}
	return string(token)
}

// assertRoleMessages holds the role messages the outbox has queued for issue, in order, to want:
// each goes to its role topic at its urgency and carries its kind's record, field for field.
func assertRoleMessages(t *testing.T, pool *pgxpool.Pool, issue string, want []roleTold) {
	t.Helper()
	got := roleMessageRows(t, pool, issue)
	if len(got) != len(want) {
		t.Fatalf("%d role messages %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i, message := range got {
		rec, err := message.Record()
		if err != nil {
			t.Fatalf("role message %d carries no record: %v", i+1, err)
		}
		told := roleTold{Role: message.Role, Urgency: message.Urgency, Record: reflect.ValueOf(rec).Elem().Interface()}
		if !reflect.DeepEqual(told, want[i]) {
			t.Fatalf("role message %d is %+v, want %+v", i+1, told, want[i])
		}
	}
}

// roleMessageRows is every role message the outbox has queued for issue, in order.
func roleMessageRows(t *testing.T, pool *pgxpool.Pool, issue string) []record.RoleMessage {
	t.Helper()
	rows, err := pool.Query(t.Context(), "select payload from outbox where kind = 'role_message' and issue = $1 order by id", issue)
	if err != nil {
		t.Fatalf("read the role messages: %v", err)
	}
	defer rows.Close()
	var got []record.RoleMessage
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan a role message: %v", err)
		}
		var message record.RoleMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatalf("decode a role message %s: %v", payload, err)
		}
		got = append(got, message)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the role messages: %v", err)
	}
	return got
}

// checksRedNotice is the architect's checks-red notice the outbox holds.
func checksRedNotice(t *testing.T, pool *pgxpool.Pool) record.Notice {
	t.Helper()
	var payload []byte
	if err := pool.QueryRow(context.Background(), "select payload from outbox where kind = 'notice' and payload->>'kind' = 'checks-red'").Scan(&payload); err != nil {
		t.Fatalf("read the architect's checks-red notice: %v", err)
	}
	var notice record.Notice
	if err := json.Unmarshal(payload, &notice); err != nil {
		t.Fatalf("decode the checks-red notice %s: %v", payload, err)
	}
	return notice
}

// phaseStartKeys is every key a phase worker's start payload carries
// (record.SuperviseRequest): the operation, the issue's tree, the role, its task, the issue
// generation, the phase the start serves and the phase entry that task is of. The list is
// asserted whole rather than key by key, so a start that grows a field nobody meant it to carry —
// a role to wait for before it acts, say — fails here.
const phaseStartKeys = "entry,generation,needsSlot,op,phase,role,task,tree"

// implementerStart is the task of the implementer's start the outbox holds, the one row a phase
// worker's start is (workflow.StartOrOwe); the task tells it from a start admission writes for a
// role it brings back beside a worker, which carries none. That row carries phaseStartKeys and
// nothing else; needsSlot is among them, because the delivery of a worker's task is the one part
// of a transition that waits, for the issue's admission slot.
func implementerStart(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var task, keys string
	if err := pool.QueryRow(context.Background(), `select payload->>'task',
		(select string_agg(key, ',' order by key) from jsonb_object_keys(payload) as key)
		from outbox where kind = 'supervise' and payload->>'op' = 'start' and coalesce(payload->>'task', '') <> '' and payload->>'role' = 'implementer'`).Scan(&task, &keys); err != nil {
		t.Fatalf("read the implementer's start: %v", err)
	}
	if keys != phaseStartKeys {
		t.Fatalf("the implementer's start carries %s, want %s", keys, phaseStartKeys)
	}
	return task
}

// applyRefusingNothing applies each fact as its own event, and fails the test on a refusal.
func applyRefusingNothing(t *testing.T, pool *pgxpool.Pool, engine *Engine, facts ...intake.Fact) {
	t.Helper()
	apply := applyFacts(t, pool, engine)
	for _, fact := range facts {
		if result := apply(t.Name()+randomSuffix(t), fact); result.Refusal != nil {
			t.Fatalf("apply %T = %+v", fact, result.Refusal)
		}
	}
}

// implementerTask is the task the implementer was last started with, "" when it was not started.
func implementerTask(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	return startTask(t, pool, claim.RoleImplementer)
}

// startTask is the task role was last started with, "" when it was not started.
func startTask(t *testing.T, pool *pgxpool.Pool, role claim.Role) string {
	t.Helper()
	var task string
	if err := pool.QueryRow(context.Background(), `select coalesce(max(payload->>'task'), '') from outbox
		where kind = 'supervise' and payload->>'op' = 'start' and payload->>'role' = $1`, string(role)).Scan(&task); err != nil {
		t.Fatalf("read the %s's start: %v", role, err)
	}
	return task
}

// implementerPass is the implementer's count of the issue's moves back into an earlier phase.
func implementerPass(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var pass int
	if err := pool.QueryRow(context.Background(), "select pass from phases where issue = 'LEGION-208' and role = 'implementer'").Scan(&pass); err != nil {
		t.Fatalf("read the implementer's pass: %v", err)
	}
	return pass
}

// implementerRounds is the implementer's count of the review rounds a review ended by requesting
// changes, which review_round_cap bounds.
func implementerRounds(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var rounds int
	if err := pool.QueryRow(context.Background(), "select rounds from phases where issue = 'LEGION-208' and role = 'implementer'").Scan(&rounds); err != nil {
		t.Fatalf("read the implementer's rounds: %v", err)
	}
	return rounds
}

// recordedPR is issue's pull request as the store reads it back.
func recordedPR(t *testing.T, pool *pgxpool.Pool, issue string) record.PullRequest {
	t.Helper()
	var pr *record.PullRequest
	seedRecord(t, pool, func(tx pgx.Tx) (err error) {
		pr, err = record.NewStore().PullRequest(t.Context(), tx, issue)
		return err
	})
	if pr == nil {
		t.Fatalf("%s has no pull request recorded", issue)
	}
	return *pr
}
