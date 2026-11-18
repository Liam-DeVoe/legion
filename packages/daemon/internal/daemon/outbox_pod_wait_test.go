package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"github.com/sjawhar/legion/daemon/internal/wait"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// podRolesRuntime is the fake runtime with an issue pod built before some roles of the
// deployment: lacks names each such role with the uid of the pod (runtime.IssuePodRoles).
type podRolesRuntime struct {
	*fake.Runtime
	lacks map[claim.Role]string
}

func (r podRolesRuntime) PodLacksRole(_ string, role claim.Role) (string, bool) {
	pod, lacks := r.lacks[role]
	return pod, lacks
}

// podWaitRunner is the outbox over a real PostgreSQL with the issue LEGION-208 stored and the
// supervisor's runtime answering that its pod lacks the roles of lacks.
func podWaitRunner(t *testing.T, lacks map[claim.Role]string) (*outbox, *supervisor, *fake.Runtime, record.Issue) {
	t.Helper()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION", Tree: "LEGION-208", Title: "Workflow", Phase: phase.Implementing, Generation: 1, Status: "in_progress"}
	putOutboxIssue(t, pool, records, issue)
	sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
	sup.deps.Runtime = podRolesRuntime{Runtime: rt, lacks: lacks}
	runner := &outbox{log: quietLogger(), pool: pool, dispatchProject: "LEGION", records: records, supervisor: sup, tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
		provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
			return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-208"}, nil
		},
	}
	return runner, sup, rt, issue
}

// A start of a role that the issue's pod was built before waits on its row and makes no claim:
// the launch, not the pod's round or its release, would otherwise decide the pod, and a launch
// into it would replace it with the issue's disk (ruling 18). Where the pod holds the role, the
// same row creates the claim and spawns it.
func TestAStartOfARoleThePodLacksWaitsAndMakesNoClaim(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lacks map[claim.Role]string
	}{
		{"the pod lacks the role", map[claim.Role]string{"checker": "pod-1"}},
		{"the pod holds the role", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, sup, rt, issue := podWaitRunner(t, tc.lacks)
			row := mustOutboxRow(t, issue.Key, record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: "checker", Task: "Check it.", Generation: issue.Generation}, time.Now())
			row.ID = 93
			token, err := claim.NewToken("legion", issue.Key, "checker")
			if err != nil {
				t.Fatal(err)
			}

			err = runner.execute(context.Background(), row)
			_, found := sup.Machine(token)
			if tc.lacks == nil {
				if err != nil || !found {
					t.Fatalf("the start = %v (claim made %t), want it to create the checker's claim", err, found)
				}
				testwait.WithinPolling(t, 5*time.Second, 10*time.Millisecond, "the checker's spawn", func() bool { return len(rt.CallsOf("Spawn")) == 1 })
				return
			}
			if !errors.Is(err, wait.ErrWaiting) || !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
				t.Errorf("the start = %v, want a wait that is runtime.ErrAwaitsPodUpdate", err)
			}
			if err == nil || !strings.HasPrefix(err.Error(), pods.StartWaitsForPodPrefix) {
				t.Errorf("the start's text = %v, want it to begin %q", err, pods.StartWaitsForPodPrefix)
			}
			if found {
				t.Errorf("the start made the checker's claim, want none while it waits")
			}
			if spawns, resumes := rt.CallsOf("Spawn"), rt.CallsOf("Resume"); len(spawns)+len(resumes) > 0 {
				t.Errorf("the runtime was asked %d spawns and %d resumes, want none", len(spawns), len(resumes))
			}
		})
	}
}
