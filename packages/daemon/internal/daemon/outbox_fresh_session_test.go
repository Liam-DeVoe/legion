package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The agents of two steps in the identities a start names them by (plan.Agent.ID): the agent of
// step 1 of task a, which has no key, whose session the claim holds, and the agent of the key b,
// which a later version gives the same name.
const (
	earlierAgent = "step:1:a"
	laterAgent   = "key:b"
)

// agentStartRig is one claim of LEGION-208's implementer whose session, ses-earlier, is the
// session of earlierAgent, and the outbox runner that executes the starts of the claim.
type agentStartRig struct {
	t       *testing.T
	ctx     context.Context
	issue   record.Issue
	token   claim.Token
	machine *supervise.Machine
	rt      *fake.Runtime
	runner  *outbox
}

// newAgentStartRig spawns the claim for earlierAgent, which records that agent as the agent of
// the session the claim begins, and brings it up to ready in ses-earlier; suspended, the claim is
// then suspended by the daemon, as the stop of its task suspends it.
func newAgentStartRig(t *testing.T, suspended bool) *agentStartRig {
	t.Helper()
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
	r := &agentStartRig{t: t, ctx: ctx, issue: issue, token: token, machine: machine, rt: rt,
		runner: &outbox{log: quietLogger(), dispatchProject: "LEGION", pool: pool, records: records, supervisor: sup, notices: &outboxPublisher{},
			trees: outboxTreeStore(t, pool, issue.Tree), tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
			provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
				return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-208"}, nil
			},
		}}
	if err := machine.Handle(ctx, supervise.RequestSpawn{Claim: token, Exempt: true, Start: supervise.StartWork{Agent: earlierAgent}}); err != nil {
		t.Fatalf("spawn the earlier agent: %v", err)
	}
	r.comesUp("ses-earlier")
	if suspended {
		if err := machine.Handle(ctx, supervise.RequestSuspend{Claim: token, By: supervise.SuspendedByDaemon, Reason: "its task was stopped"}); err != nil {
			t.Fatalf("suspend the earlier agent: %v", err)
		}
	}
	if got := machine.Claim().SessionAgent; got != earlierAgent {
		t.Fatalf("the claim spawned for %s records the agent %q of its session, want %s", earlierAgent, got, earlierAgent)
	}
	return r
}

// comesUp is the process of the claim's launch registering in session and saying it is ready, as
// the plugin does.
func (r *agentStartRig) comesUp(session string) {
	r.t.Helper()
	launch := r.machine.Claim().Generation
	if err := r.machine.Handle(r.ctx, supervise.RequestRegister{Claim: r.token, Generation: launch, Session: session, SessionFile: "/tmp/" + session + ".jsonl"}); err != nil {
		r.t.Fatalf("register %s: %v", session, err)
	}
	if err := r.machine.Handle(r.ctx, supervise.RequestReady{Claim: r.token, Generation: launch, Session: session}); err != nil {
		r.t.Fatalf("ready %s: %v", session, err)
	}
}

// start is the start row id of step 1 of the plan in force, for agent, at generation, as the
// workflow's entry queues it.
func (r *agentStartRig) start(id int64, agent string, generation uint64) record.OutboxRow {
	r.t.Helper()
	row := mustOutboxRow(r.t, r.issue.Key, record.SuperviseRequest{Op: "start", Tree: r.issue.Tree, Role: claim.RoleImplementer, Phase: phase.Implementing,
		Entry: 3, Task: "Build the manual.", Generation: generation, Agent: agent}, time.Now())
	row.ID = id
	return row
}

// launches is how many spawns and resumes the runtime has made so far.
func (r *agentStartRig) launches() (spawns, resumes int) {
	return len(r.rt.CallsOf("Spawn")), len(r.rt.CallsOf("Resume"))
}

// The start of a step whose agent is another than the one whose session its claim records
// (supervise.Claim.SessionAgent) starts the claim in a new session: a suspended claim is resumed
// by rule with that session left behind, so the runtime spawns the new agent rather than resuming
// the other agent's session, and a claim that still runs the other agent's process is suspended
// first and then started the same way. The claim then records the new agent as the agent of its
// session. The control, a start of the agent whose session it holds, resumes that session. The
// fault: a new agent on a held name continues the session of the agent that held it before.
func TestAStartForAnotherAgentStartsTheClaimInANewSession(t *testing.T) {
	for _, tc := range []struct {
		name      string
		agent     string
		suspended bool
		fresh     bool
	}{
		{name: "a suspended claim", agent: laterAgent, suspended: true, fresh: true},
		{name: "a claim that runs the other agent's process", agent: laterAgent, fresh: true},
		{name: "control: a start of the agent whose session the claim holds", agent: earlierAgent, suspended: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentStartRig(t, tc.suspended)
			spawns, resumes := r.launches()

			if err := r.runner.execute(r.ctx, r.start(81, tc.agent, 1)); err != nil {
				t.Fatalf("the start = %v, want it to start the claim", err)
			}
			nowSpawns, nowResumes := r.launches()
			spawned, resumed := nowSpawns-spawns, nowResumes-resumes
			if got := r.machine.Claim().SessionAgent; got != tc.agent {
				t.Errorf("the claim records the agent %q of its session after the start for %s, want %s", got, tc.agent, tc.agent)
			}
			if !tc.fresh {
				if resumed != 1 || spawned != 0 {
					t.Fatalf("the start asked %d spawns and %d resumes, want the recorded session resumed once", spawned, resumed)
				}
				return
			}
			if spawned != 1 || resumed != 0 {
				t.Errorf("the start asked %d spawns and %d resumes, want one spawn of a new session", spawned, resumed)
			}
			if got := r.machine.Claim().Session; got == "ses-earlier" {
				t.Errorf("the claim still records the session %s, want the earlier agent's session forgotten", got)
			}
		})
	}
}

// A start row runs again after it acted — a settle or a ready that failed, a crash before the row
// finished — and a run of the row that started a new agent in a new session finds the claim's
// session the new agent's (supervise.Claim.SessionAgent), so it asks nothing of the claim: it does
// not suspend the new agent's process and does not forget its session. The fault, the control: a
// run that read only the row's own ask for a fresh session suspended the new agent and forgot its
// session, so a third session started.
func TestAStartRunAgainAfterItsFreshLaunchKeepsTheNewSession(t *testing.T) {
	r := newAgentStartRig(t, true)
	start := r.start(81, laterAgent, 1)
	if err := r.runner.execute(r.ctx, start); err != nil {
		t.Fatalf("the start = %v, want it to start the claim in a new session", err)
	}
	r.comesUp("ses-later")
	spawns, resumes := r.launches()

	if err := r.runner.execute(r.ctx, start); err != nil {
		t.Fatalf("the start run again = %v, want it to finish with the claim as it stands", err)
	}

	got := r.machine.Claim()
	if got.State != supervise.StateReady || got.Session != "ses-later" || got.SessionAgent != laterAgent {
		t.Fatalf("after the row ran again the claim is %s in session %q of agent %q, want it ready in ses-later, %s's",
			got.State, got.Session, got.SessionAgent, laterAgent)
	}
	if nowSpawns, nowResumes := r.launches(); nowSpawns != spawns || nowResumes != resumes {
		t.Fatalf("the row run again made %d spawns and %d resumes, want none", nowSpawns-spawns, nowResumes-resumes)
	}
}

// A start for a new agent that finished without acting — here one of a run the issue has left,
// which the generation fence finishes — leaves the claim as it stood: its session is still the
// other agent's, and says so (supervise.Claim.SessionAgent), so the next start of the new agent's
// step starts it in a new session. The fault: the record of whose session the claim holds moved
// at a start that started nothing, so the next start resumed the other agent's session.
func TestAFreshStartThatFinishedWithoutActingLeavesTheNextStartFresh(t *testing.T) {
	r := newAgentStartRig(t, true)
	spawns, resumes := r.launches()

	if err := r.runner.execute(r.ctx, r.start(81, laterAgent, 2)); err != nil {
		t.Fatalf("the start of a run the issue has left = %v, want it finished without acting", err)
	}
	if got := r.machine.Claim(); got.Session != "ses-earlier" || got.SessionAgent != earlierAgent || got.State != supervise.StateSuspended {
		t.Fatalf("after the start that finished without acting the claim is %s in session %q of agent %q, want it suspended in ses-earlier, %s's",
			got.State, got.Session, got.SessionAgent, earlierAgent)
	}
	if nowSpawns, nowResumes := r.launches(); nowSpawns != spawns || nowResumes != resumes {
		t.Fatalf("the start that finished without acting made %d spawns and %d resumes, want none", nowSpawns-spawns, nowResumes-resumes)
	}

	if err := r.runner.execute(r.ctx, r.start(82, laterAgent, 1)); err != nil {
		t.Fatalf("the next start = %v, want it to start the claim", err)
	}
	if nowSpawns, nowResumes := r.launches(); nowSpawns-spawns != 1 || nowResumes != resumes {
		t.Fatalf("the next start of the new agent made %d spawns and %d resumes, want one spawn of a new session", nowSpawns-spawns, nowResumes-resumes)
	}
	if got := r.machine.Claim(); got.Session == "ses-earlier" || got.SessionAgent != laterAgent {
		t.Fatalf("after the next start the claim records session %q of agent %q, want the earlier session left and %s recorded", got.Session, got.SessionAgent, laterAgent)
	}
}
