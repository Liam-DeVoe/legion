package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// movingRecords is the record store of a start a move back overtakes: once the start's fences
// have read the issue's phases rows, its next read of an issue (the tree root's, the outbox's
// root) first commits move, as a fact that lands between the fences and the claim's re-role would.
type movingRecords struct {
	record.Store
	phasesRead, moved bool
	move              func()
}

func (s *movingRecords) Phases(ctx context.Context, tx pgx.Tx, issue string) ([]record.PhaseRow, error) {
	s.phasesRead = true
	return s.Store.Phases(ctx, tx, issue)
}

func (s *movingRecords) Issue(ctx context.Context, tx pgx.Tx, key string) (*record.Issue, error) {
	if s.phasesRead && !s.moved && s.move != nil {
		s.moved = true
		s.move()
	}
	return s.Store.Issue(ctx, tx, key)
}

// Key a's agent works the implementer's step and then the tester's in one claim, which holds the
// implementer's role and runs nothing, and the start of the tester's entry would give it the
// tester's role and resume it with the tester's task. A move back to implementing that commits
// after the start's fences read the record and before the re-role — the tester's fail, which
// raises the implementer's entry on the same agent — leaves the step the start was for: the
// claim keeps the implementer's role, nothing resumes it as the tester, and the row finishes
// without acting, since the move back's own start gives the claim its role. The re-role judges
// the step again under the machine's lock (supervise.Machine.Rerole). The control is the same
// start with no move back: the claim takes the tester's role and is resumed on its session.
func TestAStartsReroleForAStepTheIssueLeftIsNotMade(t *testing.T) {
	for _, tc := range []struct {
		name     string
		moveBack bool
	}{
		{name: "control: nothing moves the issue while the start runs"},
		{name: "a move back commits between the start's fences and its re-role", moveBack: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
				Tree: "LEGION-208", Title: "Workflow", Phase: phase.Testing, Generation: 1, Status: "testing"}
			putOutboxIssue(t, pool, records, issue)
			keyed := mustClaimToken(t, issue.Key, claim.RoleImplementer)
			putPhaseRow(t, pool, records, record.PhaseRow{Issue: issue.Key, Role: claim.RoleImplementer, Claim: keyed, StepTask: "t", StepPlace: 1, Entry: 1})
			putPhaseRow(t, pool, records, record.PhaseRow{Issue: issue.Key, Role: claim.RoleTester, Claim: keyed, StepTask: "t", StepPlace: 2, Entry: 2})
			sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
			machine, _, err := sup.Create(ctx, supervise.Claim{Token: keyed, Project: "legion", Tree: issue.Tree, Issue: issue.Key,
				Role: claim.RoleImplementer, State: supervise.StateQueued}, "")
			if err != nil {
				t.Fatalf("create the claim of key a: %v", err)
			}
			if err := machine.Handle(ctx, supervise.RequestSpawn{Claim: keyed, Exempt: true}); err != nil {
				t.Fatalf("spawn key a's agent: %v", err)
			}
			launch := machine.Claim().Generation
			if err := machine.Handle(ctx, supervise.RequestRegister{Claim: keyed, Generation: launch, Session: "ses-a", SessionFile: "/tmp/a.jsonl"}); err != nil {
				t.Fatalf("register key a's agent: %v", err)
			}
			if err := machine.Handle(ctx, supervise.RequestReady{Claim: keyed, Generation: launch, Session: "ses-a"}); err != nil {
				t.Fatalf("ready key a's agent: %v", err)
			}
			if err := machine.Handle(ctx, supervise.RequestSuspend{Claim: keyed, By: supervise.SuspendedByDaemon, Reason: "its implementer's step ended"}); err != nil {
				t.Fatalf("suspend key a's agent: %v", err)
			}
			if got := machine.Claim().State; got != supervise.StateSuspended {
				t.Fatalf("key a's claim is %s, want suspended: the case's premise", got)
			}
			moving := &movingRecords{Store: records}
			if tc.moveBack {
				moving.move = func() {
					back := issue
					back.Phase, back.Status = phase.Implementing, "in_progress"
					putOutboxIssue(t, pool, records, back)
					putPhaseRow(t, pool, records, record.PhaseRow{Issue: issue.Key, Role: claim.RoleImplementer, Claim: keyed, StepTask: "t", StepPlace: 1, Entry: 3})
				}
			}
			runner := &outbox{log: quietLogger(), dispatchProject: "LEGION", pool: pool, records: moving, supervisor: sup, notices: &outboxPublisher{},
				trees: outboxTreeStore(t, pool, issue.Tree), tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
				provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
					return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-208"}, nil
				},
			}
			resumes := len(rt.CallsOf("Resume"))

			start := mustOutboxRow(t, issue.Key, record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: claim.RoleTester, Phase: phase.Testing,
				Entry: 2, Task: "Test it.", Generation: 1}, time.Now())
			start.ID = 71
			if err := runner.execute(ctx, start); err != nil {
				t.Fatalf("the tester's start: %v", err)
			}
			if tc.moveBack && !moving.moved {
				t.Fatal("the move back never landed inside the start: the case saw no window")
			}
			calls := rt.CallsOf("Resume")[resumes:]
			if !tc.moveBack {
				if got := machine.Claim().Role; got != claim.RoleTester || len(calls) != 1 || calls[0].Spec.Role != claim.RoleTester {
					t.Fatalf("with no move back the claim holds %s and was relaunched as %+v, want the tester, resumed once", got, calls)
				}
				return
			}
			if got := machine.Claim().Role; got != claim.RoleImplementer {
				t.Errorf("after a move back overtook the tester's start the claim holds the role %s, want the implementer it holds for the step the issue stands in", got)
			}
			if len(calls) != 0 {
				t.Errorf("after a move back overtook the tester's start the claim was relaunched as %+v, want no relaunch for a step the issue left", calls)
			}
			if got := machine.Claim().State; got != supervise.StateSuspended {
				t.Errorf("after a move back overtook the tester's start the claim is %s, want it suspended, running nothing", got)
			}
		})
	}
}
