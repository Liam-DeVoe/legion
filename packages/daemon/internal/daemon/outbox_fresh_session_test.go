package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The start of a step whose agent takes the claim another agent last ran a step on asks for a fresh
// session (record.SuperviseRequest.FreshSession), and the runner starts the claim in one: a
// suspended claim is resumed by rule with its recorded session forgotten, so the runtime spawns a
// new agent rather than resuming the other agent's session, and a claim that still runs the other
// agent's process is suspended first and then started the same way. The control, the same start
// with no such ask, resumes the recorded session. The fault: a new agent on a held name continues
// the session of the agent that held it before.
func TestAStartAskingAFreshSessionStartsTheClaimInANewSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fresh     bool
		suspended bool
	}{
		{name: "a suspended claim", fresh: true, suspended: true},
		{name: "a claim that runs the other agent's process", fresh: true},
		{name: "control: a start that asks for no fresh session", suspended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
				Tree: "LEGION-208", Title: "Workflow", Phase: phase.Implementing, Generation: 1, Status: "in_progress"}
			putOutboxIssue(t, pool, records, issue)
			token := mustClaimToken(t, issue.Key, claim.RoleImplementer)
			putPhaseRow(t, pool, records, record.PhaseRow{Issue: issue.Key, Role: claim.RoleImplementer, Claim: token, StepTask: "p", StepPlace: 1, Entry: 2})
			sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
			machine, _, err := sup.Create(ctx, supervise.Claim{Token: token, Project: "legion", Tree: issue.Tree, Issue: issue.Key,
				Role: claim.RoleImplementer, State: supervise.StateQueued}, "")
			if err != nil {
				t.Fatalf("create the claim: %v", err)
			}
			if err := machine.Handle(ctx, supervise.RequestSpawn{Claim: token, Exempt: true}); err != nil {
				t.Fatalf("spawn the earlier agent: %v", err)
			}
			launch := machine.Claim().Generation
			if err := machine.Handle(ctx, supervise.RequestRegister{Claim: token, Generation: launch, Session: "ses-earlier", SessionFile: "/tmp/earlier.jsonl"}); err != nil {
				t.Fatalf("register the earlier agent: %v", err)
			}
			if err := machine.Handle(ctx, supervise.RequestReady{Claim: token, Generation: launch, Session: "ses-earlier"}); err != nil {
				t.Fatalf("ready the earlier agent: %v", err)
			}
			if tc.suspended {
				if err := machine.Handle(ctx, supervise.RequestSuspend{Claim: token, By: supervise.SuspendedByDaemon, Reason: "its task was stopped"}); err != nil {
					t.Fatalf("suspend the earlier agent: %v", err)
				}
			}
			runner := &outbox{log: quietLogger(), dispatchProject: "LEGION", pool: pool, records: records, supervisor: sup, notices: &outboxPublisher{},
				trees: outboxTreeStore(t, pool, issue.Tree), tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
				provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
					return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-208"}, nil
				},
			}
			spawns, resumes := len(rt.CallsOf("Spawn")), len(rt.CallsOf("Resume"))

			start := mustOutboxRow(t, issue.Key, record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: claim.RoleImplementer, Phase: phase.Implementing,
				Entry: 3, Task: "Build the manual.", Generation: 1, FreshSession: tc.fresh}, time.Now())
			start.ID = 81
			if err := runner.execute(ctx, start); err != nil {
				t.Fatalf("the start = %v, want it to start the claim", err)
			}
			spawned, resumed := len(rt.CallsOf("Spawn"))-spawns, len(rt.CallsOf("Resume"))-resumes
			if !tc.fresh {
				if resumed != 1 || spawned != 0 {
					t.Fatalf("the start asked %d spawns and %d resumes, want the recorded session resumed once", spawned, resumed)
				}
				return
			}
			if spawned != 1 || resumed != 0 {
				t.Errorf("the start asked %d spawns and %d resumes, want one spawn of a new session", spawned, resumed)
			}
			if got := machine.Claim().Session; got == "ses-earlier" {
				t.Errorf("the claim still records the session %s, want the earlier agent's session forgotten", got)
			}
		})
	}
}
