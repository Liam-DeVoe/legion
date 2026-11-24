package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// failedImplementer is issue's implementer failed under cause with budgets, its session kept:
// the claim a production check's look finds when its checking role failed in an earlier look.
func failedImplementer(t *testing.T, sup *supervisor, issue record.Issue, cause supervise.FailureCause, budgets supervise.Budgets) *supervise.Machine {
	t.Helper()
	token, err := claim.NewToken("legion", issue.Key, claim.RoleImplementer)
	if err != nil {
		t.Fatal(err)
	}
	machine, _, err := sup.Create(context.Background(), supervise.Claim{Token: token, Project: "legion", Tree: issue.Tree, Issue: issue.Key, Role: claim.RoleImplementer, Generation: 1,
		State: supervise.StateFailed, Failure: "the claim failed for the test", FailureCause: cause, FailedAt: time.Now().Add(-time.Hour), Budgets: budgets,
		Session: "ses_implementer", SessionFile: "/sessions/implementer.jsonl"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return machine
}

// checkLookRetry runs the start of look 1 of root's open production check against its
// implementer failed under cause with budgets, and answers the claim after it and the runtime's
// resumes of it. The outbox supervisor's limits are 2 launch failures, 2 prompt failures and 2
// prompt retirements (newOutboxSupervisor).
func checkLookRetry(t *testing.T, cause supervise.FailureCause, budgets supervise.Budgets) (supervise.Claim, int) {
	t.Helper()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	root := checkRoot()
	putOutboxIssue(t, pool, records, root)
	putProductionCheck(t, pool, records, openCheck(root, "a1b2c3d4"))
	sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
	machine := failedImplementer(t, sup, root, cause, budgets)
	runner := &outbox{pool: pool, dispatchProject: "LEGION", records: records, supervisor: sup, tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(), log: quietLogger(), now: time.Now}
	if err := runner.execute(context.Background(), checkStart(t, root, 1)); err != nil {
		t.Fatalf("the look's start against the failed implementer = %v, want it retried", err)
	}
	return machine.Claim(), len(rt.CallsOf("Resume"))
}

// A production check look's start against its checking role's claim failed at a budget's limit is
// the half-open trial of a role that keeps failing: it retries the claim with one attempt left of
// each budget the failure spent, not a fresh budget, so one more failure of the same kind fails the
// claim again and the look ends at once, and every count the failure did not spend stands as the
// failure left it. The launch budget for a failure whose launch failures reached the launch
// failure limit, the death budget for one whose deaths did (measured against the same limit), and
// both prompt counts for one whose prompt retirements ran out. The retry relaunches the recorded
// session, as every retry that asks for no new one does. At main the retry zeroed every budget
// (supervise's retry), so the role was launched a whole budget's worth of times at every look.
func TestCheckLookStartRetriesAFailedClaimWithOneAttemptLeft(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cause  supervise.FailureCause
		failed supervise.Budgets
		want   supervise.Budgets
	}{
		{name: "launch failures at the limit", cause: supervise.CauseDied,
			failed: supervise.Budgets{LaunchFailures: 2}, want: supervise.Budgets{LaunchFailures: 1}},
		{name: "launch failures at the limit, a prompt failure beside them", cause: supervise.CauseNeverRegistered,
			failed: supervise.Budgets{LaunchFailures: 2, PromptFailures: 1}, want: supervise.Budgets{LaunchFailures: 1, PromptFailures: 1}},
		{name: "deaths at the launch failure limit", cause: supervise.CauseDied,
			failed: supervise.Budgets{LaunchFailures: 1, Deaths: 2}, want: supervise.Budgets{LaunchFailures: 1, Deaths: 1}},
		{name: "prompt retirements at their limit", cause: supervise.CausePromptRetiresExhausted,
			failed: supervise.Budgets{PromptFailures: 2, PromptRetires: 2}, want: supervise.Budgets{PromptFailures: 1, PromptRetires: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, resumes := checkLookRetry(t, tc.cause, tc.failed)
			if got.State == supervise.StateFailed || resumes != 1 {
				t.Fatalf("the implementer after the look's start = %s with %d resumes, want it retried on its session once", got.State, resumes)
			}
			if got.Budgets != tc.want {
				t.Fatalf("the implementer's budgets after the look's retry = %+v, want %+v: one attempt left of each budget the failure spent, the rest as it left them", got.Budgets, tc.want)
			}
			if got.Failure != "" || got.FailureCause != "" {
				t.Fatalf("the implementer after the look's retry still records its failure %q (%s)", got.Failure, got.FailureCause)
			}
		})
	}
}

// A failure that spent no budget the claim records is retried by a look's start as every retry
// is, with a fresh run: a provider error that cannot clear, whose counts are the run of refused
// turns the claim keeps apart from its budgets, and a launch the runtime refused in a way no try
// changes (a system prompt past the start frame), which fails the claim with no budget spent. The
// one-attempt rule has nothing to keep of either, and the look after the next failure backs off
// all the same (workflow's TestACheckRoleFailureDoublesTheNextLookInterval).
func TestACheckRoleProviderErrorIsRetriedWithAFreshRunAtTheNextLook(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		cause  supervise.FailureCause
		failed supervise.Budgets
	}{
		{name: "a provider error", cause: supervise.CauseProviderError, failed: supervise.Budgets{LaunchFailures: 1, PromptFailures: 1}},
		{name: "a prompt refused at launch", cause: supervise.CauseLaunchRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, resumes := checkLookRetry(t, tc.cause, tc.failed)
			if got.State == supervise.StateFailed || resumes != 1 {
				t.Fatalf("the implementer after the look's start = %s with %d resumes, want it retried on its session once", got.State, resumes)
			}
			if got.Budgets != (supervise.Budgets{}) {
				t.Fatalf("the implementer's budgets after the look's retry of %s = %+v, want a fresh run", tc.cause, got.Budgets)
			}
		})
	}
}
