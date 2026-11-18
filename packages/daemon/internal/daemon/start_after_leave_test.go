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

// The start of a child's tester step passes its fences, and while it provisions the workspace,
// before it stores its new claim, the child leaves the workflow at done and the leave commits. The
// leave read the issue's claims before the new one existed, so no close names it. The start reads
// the issue again after it stores the claim, finds it gone from the workflow, and retires the claim,
// which has run nothing: the runtime is asked no spawn, and the tree's cleanup does not wait for
// it.
func TestAStartBetweenItsReadAndItsCreateWhenTheLeaveCommitsRetiresItsClaim(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	parent := "LEGION-208"
	root := record.Issue{Key: parent, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
		Tree: parent, Title: "Workflow", Phase: phase.Implementing, Generation: 1, Status: "in_progress"}
	child := record.Issue{Key: "LEGION-209", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
		Tree: parent, Parent: &parent, Title: "Child", Phase: phase.Testing, Generation: 1, Status: "testing"}
	putOutboxIssue(t, pool, records, root)
	putOutboxIssue(t, pool, records, child)
	sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
	provisioning, released := make(chan struct{}), make(chan struct{})
	runner := &outbox{log: quietLogger(), dispatchProject: "LEGION", pool: pool, records: records, supervisor: sup, notices: &outboxPublisher{},
		trees: outboxTreeStore(t, pool, parent), tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
		provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
			close(provisioning)
			<-released
			return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-209"}, nil
		},
	}
	token := mustClaimToken(t, child.Key, claim.RoleTester)

	start := mustOutboxRow(t, child.Key, record.SuperviseRequest{Op: "start", Tree: parent, Role: claim.RoleTester, Phase: phase.Testing,
		Task: "Test it.", Generation: 1}, time.Now())
	start.ID = 80
	done := make(chan error, 1)
	go func() { done <- runner.execute(ctx, start) }()
	<-provisioning
	// The child's leave at done commits while the start provisions.
	if _, err := pool.Exec(ctx, `update issues set status = 'done', phase = 'done' where key = $1`, child.Key); err != nil {
		t.Fatalf("leave the child: %v", err)
	}
	close(released)
	if err := <-done; err != nil {
		t.Fatalf("the start: %v", err)
	}

	machine, found := sup.Machine(token)
	if !found {
		t.Fatalf("the start stored no claim %s: the case's premise", token)
	}
	if got := machine.Claim().State; got != supervise.StateRetired {
		t.Fatalf("the start's new claim of an issue that left is %s, want retired", got)
	}
	if spawns := rt.CallsOf("Spawn"); len(spawns) != 0 {
		t.Fatalf("the runtime was asked %d spawns, want none", len(spawns))
	}
}
