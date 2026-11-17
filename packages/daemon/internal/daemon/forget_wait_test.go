package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// The daemon's own retries of a claim in a new session meet the hold's refusal of that new session
// for as long as the Envoy listener names the recorded session, which another claim holds, as the
// claim's role holder (supervise.ErrForgetWaitsForRole, review round 18 of #4220). That clears on
// its own once the role is freed, so it is a wait, not a fault: the controller keeper logs it once
// at Info with the reason and returns nothing its loop would log at Error, however often it
// retries, and a start row answers it as a wait (wait.ErrWaiting), which settle logs as one.
//
// The controls (the pull request records the runs): the keeper returning the refusal as it did,
// which its loop logs at Error at every retry; and the start row answering it as a plain failure.
func TestARetryHeldForItsRoleIsAWait(t *testing.T) {
	const taken = "ses-taken"
	refusedTwice := []supervise.TurnOutcome{{Generation: 1, Sequence: 1, Refused: true}, {Generation: 1, Sequence: 2, Refused: true}}
	// shared is a supervisor whose session taken another claim, legion-943's implementer,
	// recorded first, and whose listener names taken as the holder of every role.
	shared := func(t *testing.T) *supervisor {
		t.Helper()
		sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
		if _, err := sup.deps.Store.(*outboxClaimStore).HoldSessions(context.Background(), "legion",
			mustClaimToken(t, "LEGION-943", claim.RoleImplementer), supervise.RecordedHolds(taken, "")); err != nil {
			t.Fatalf("hold %s for the claim that recorded it first: %v", taken, err)
		}
		sup.deps.RoleHolder = func(context.Context, string) (string, string, error) { return taken, "live", nil }
		return sup
	}

	t.Run("the controller keeper", func(t *testing.T) {
		sup := shared(t)
		token := claim.ControllerToken("legion")
		if _, _, err := sup.Create(context.Background(), supervise.Claim{Token: token, Project: "legion", Role: claim.RoleController,
			Generation: 1, State: supervise.StateFailed, Session: taken, SessionFile: "/tmp/controller.jsonl", TurnOutcomes: refusedTwice}, ""); err != nil {
			t.Fatalf("create the failed controller: %v", err)
		}
		var logs syncBuffer
		keeper := newControllerKeeper(context.Background(), sup, "legion", time.Minute, slog.New(slog.NewJSONHandler(&logs, nil)))
		clock := time.Now()
		keeper.now = func() time.Time { return clock }
		for i := range 4 {
			if err := keeper.keep(); err != nil {
				t.Fatalf("keep %d returned %v, which the keeper's loop logs at Error: the retry only waits for the listener to free the role", i, err)
			}
			clock = clock.Add(time.Hour)
		}
		if n := strings.Count(logs.String(), "waits for the Envoy listener to free its role"); n != 1 {
			t.Fatalf("the keeper logged the wait %d times over three retries, want once:\n%s", n, logs.String())
		}
		if !strings.Contains(logs.String(), `"level":"INFO","msg":"controller: the retry of the daemon's controller waits`) {
			t.Fatalf("the wait is not logged at Info:\n%s", logs.String())
		}
		if m, _ := sup.Machine(token); m.Claim().Session != taken {
			t.Fatalf("the controller records %q, want %s kept until its role is free", m.Claim().Session, taken)
		}
	})

	t.Run("a start row", func(t *testing.T) {
		pool := isolatedOutboxPool(t)
		records := record.NewStore()
		issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Tree: "LEGION-208", Title: "Workflow", Phase: phase.Testing, Generation: 1, Status: "testing"}
		putOutboxIssue(t, pool, records, issue)
		sup := shared(t)
		tester := mustClaimToken(t, issue.Key, claim.RoleTester)
		if _, _, err := sup.Create(context.Background(), supervise.Claim{Token: tester, Project: "legion", Tree: issue.Tree, Issue: issue.Key, Role: claim.RoleTester,
			Generation: 1, State: supervise.StateFailed, Session: taken, SessionFile: "/tmp/tester.jsonl"}, ""); err != nil {
			t.Fatalf("create the failed tester: %v", err)
		}
		clock := time.Now()
		runner := &outbox{log: quietLogger(), pool: pool, dispatchProject: "LEGION", records: records, supervisor: sup, project: "legion",
			now: func() time.Time { return clock }}
		row := mustOutboxRow(t, issue.Key, record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: claim.RoleTester,
			Generation: 1, Phase: phase.Testing, Task: "Test it.", FreshSession: true}, clock)
		err := runner.execute(context.Background(), row)
		if !errors.Is(err, wait.ErrWaiting) || !errors.Is(err, supervise.ErrForgetWaitsForRole) {
			t.Fatalf("the start row = %v, want a wait on the hold's refusal", err)
		}
		if m, _ := sup.Machine(tester); m.Claim().Session != taken {
			t.Fatalf("the tester records %q, want %s kept until its role is free", m.Claim().Session, taken)
		}
	})
}
