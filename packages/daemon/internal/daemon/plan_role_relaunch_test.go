package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	stepplan "github.com/sjawhar/legion/daemon/internal/plan"
	"github.com/sjawhar/legion/daemon/internal/prompts"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/store"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// relaunchProber is the role the plan of stepStartKey defines in the tests of a changed role.
const relaunchProber claim.Role = "prober"

// seedProberVersion records version v of the plan of stepStartKey at generation 2: one task whose
// one step is relaunchProber, of type and model and prompt as given, running at that step, with
// the prober's phases row on entry v, the entry the start of that version's step carries.
func seedProberVersion(t *testing.T, pool *store.Pool, records record.Store, v int, typ, model, prompt string) {
	t.Helper()
	ctx := context.Background()
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		version := record.PlanVersion{Issue: stepStartKey, Generation: 2, Version: v, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
			GivenBy: mustClaimToken(t, stepStartKey, claim.RoleArchitect), GivenAt: time.Now().UTC()}
		task := record.PlanTask{Issue: stepStartKey, Generation: 2, Task: "a", PlanVersion: v, BlockText: "Probe the widget.", State: record.TaskWaiting}
		steps := []record.PlanStep{{Issue: stepStartKey, Generation: 2, Task: "a", Position: 1, Role: string(relaunchProber), Type: stepplan.Type(typ)}}
		if err := records.PutPlan(ctx, tx, version, []record.PlanTask{task}, steps); err != nil {
			return err
		}
		if err := records.PutPlanRoles(ctx, tx, stepStartKey, 2, v, []record.PlanRole{{Issue: stepStartKey, Generation: 2, Role: string(relaunchProber),
			PlanVersion: v, Type: typ, Model: model, Prompt: prompt}}); err != nil {
			return err
		}
		if err := records.MoveTask(ctx, tx, stepStartKey, 2, "a", record.TaskPlace{Kind: record.PlaceStep, Position: 1}, record.TaskRunning); err != nil {
			return err
		}
		return records.PutPhase(ctx, tx, record.PhaseRow{Issue: stepStartKey, Role: relaunchProber, Entry: int64(v),
			EntryTask: "Probe it. Issue: LEGION-208. Phase: step.", EntryRaisedAt: time.Now()})
	}); err != nil {
		t.Fatalf("seed version %d of the plan of %s: %v", v, stepStartKey, err)
	}
}

// poolPlanRole reads the role of an issue's plan in force from pool, as the daemon's reader does.
func poolPlanRole(pool *store.Pool, records record.Store) func(context.Context, string, claim.Role) (record.PlanRole, bool, error) {
	return func(ctx context.Context, issue string, role claim.Role) (record.PlanRole, bool, error) {
		var defined record.PlanRole
		var found bool
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			var err error
			defined, found, err = records.PlanRoleOf(ctx, tx, issue, string(role))
			return err
		})
		return defined, found, err
	}
}

// Version 1 of the plan defines prober (type testing, model m-a, prompt P1), whose step's start
// launches its claim with P1 and m-a; the prober's turn ends and its process stays up, idle. The
// owner then gives version 2, which changes the role block (type implementation, model m-b,
// prompt P2), and the task's step of prober starts again. The claim still runs the process
// launched with P1 and m-a, so the start ends that process with its session kept and the claim is
// relaunched on its session with P2 and m-b: no task of version 2 goes to a process of version 1,
// whose next mint would hand it the App of the new type. The control is a version 2 that changes
// no part of the role: the start hands the running process its task and relaunches nothing.
//
// Before the fix the start found a live claim of the same role and handed it the task, and the
// prober went on as P1 on m-a.
func TestAStartAfterAChangedRoleBlockRelaunchesTheRolesLiveClaim(t *testing.T) {
	const p1, p2 = "# Prober\n\nProbe the change, first wording.", "# Prober\n\nProbe the change, second wording."
	for _, tc := range []struct {
		name               string
		typ, model, prompt string
		relaunched         bool
	}{
		{"version 2 changes the role's type, model and prompt", "implementation", "m-b", p2, true},
		{"the control: version 2 leaves the role as it was", "testing", "m-a", p1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := isolatedOutboxPool(t)
			records := record.NewStore()
			putOutboxIssue(t, pool, records, record.Issue{Key: stepStartKey, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets",
				Project: "LEGION", Tree: stepStartKey, Title: "Workflow", Phase: phase.Step, Generation: 2, Status: "in_progress", Rank: "U", LastDispatchSeq: 1})
			openTree(t, pool, "LEGION", stepStartKey)
			holdSlot(t, pool, stepStartKey)
			seedProberVersion(t, pool, records, 1, "testing", "m-a", p1)
			composer, err := prompts.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
			sup.deps.Specs = specs{stateDir: t.TempDir(), project: "legion", prompts: composer, planRole: poolPlanRole(pool, records)}
			token := mustClaimToken(t, stepStartKey, relaunchProber)
			conn := fake.NewConn()
			sup.deps.Conns.(*fake.Conns).Register(token, conn)
			runner := &outbox{log: quietLogger(), pool: pool, dispatchProject: "LEGION", records: records, supervisor: sup,
				tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(), now: time.Now,
				provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
					return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-208"}, nil
				},
			}

			first := mustOutboxRow(t, stepStartKey, record.SuperviseRequest{Op: "start", Tree: stepStartKey, Role: relaunchProber, Generation: 2,
				Phase: phase.Step, Task: "Probe it. Issue: LEGION-208. Phase: step.", Entry: 1}, time.Now())
			first.ID = 95
			if err := runner.execute(ctx, first); err != nil {
				t.Fatalf("the prober's start of version 1: %v", err)
			}
			machine, found := sup.Machine(token)
			if !found {
				t.Fatal("the prober's start of version 1 created no claim")
			}
			machine.Wait()
			launches := append(rt.CallsOf("Spawn"), rt.CallsOf("Resume")...)
			if len(launches) != 1 || launches[0].Spec.Model != "m-a" || !composedHolds(t, launches[0].Spec, p1) {
				t.Fatalf("the launches of version 1 = %d, the first on %q; want one on m-a with P1: the case's premise", len(launches), modelOf(launches))
			}
			generation := machine.Claim().Generation
			for _, ev := range []supervise.Event{
				supervise.StreamHello{Claim: token, Generation: generation},
				supervise.RequestRegister{Claim: token, Generation: generation, Session: "ses-prober", SessionFile: "/tmp/prober.jsonl", CapabilityHash: []byte("prober")},
				supervise.RequestReady{Claim: token, Generation: generation, Session: "ses-prober"},
			} {
				if err := machine.Handle(ctx, ev); err != nil {
					t.Fatalf("bring the prober's agent up: handle %T: %v", ev, err)
				}
			}
			machine.Wait()
			if pending := machine.Claim().Pending; pending != nil {
				for _, ev := range []supervise.Event{
					supervise.StreamTurnStart{Claim: token, DeliveryID: pending.ID},
					supervise.StreamTurnEnd{Claim: token},
					supervise.StreamPromptResult{Claim: token, DeliveryID: pending.ID, AgentInvoked: true},
				} {
					if err := machine.Handle(ctx, ev); err != nil {
						t.Fatalf("the prober's turn of version 1: handle %T: %v", ev, err)
					}
				}
			}
			machine.Wait()
			if held := machine.Claim(); held.RunsNothing() || held.Pending != nil || held.State == supervise.StateWorking {
				t.Fatalf("the prober after its turn of version 1 is %s holding %+v, want its process up, idle, with no task: the case's premise", held.State, held.Pending)
			}

			seedProberVersion(t, pool, records, 2, tc.typ, tc.model, tc.prompt)
			second := mustOutboxRow(t, stepStartKey, record.SuperviseRequest{Op: "start", Tree: stepStartKey, Role: relaunchProber, Generation: 2,
				Phase: phase.Step, Task: "Probe it again. Issue: LEGION-208. Phase: step.", Entry: 2}, time.Now())
			second.ID = 96
			if err := runner.execute(ctx, second); err != nil {
				t.Fatalf("the prober's start of version 2: %v", err)
			}
			machine.Wait()
			relaunches := append(rt.CallsOf("Spawn"), rt.CallsOf("Resume")...)[1:]
			if !tc.relaunched {
				if len(relaunches) != 0 {
					t.Fatalf("a version that leaves the role as it was relaunched the prober %d times (on %q), want none", len(relaunches), modelOf(relaunches))
				}
				return
			}
			if len(relaunches) != 1 {
				t.Fatalf("the prober's start of version 2 relaunched its claim %d times, want once: its process runs P1 on m-a, which version 2 changed", len(relaunches))
			}
			spec := relaunches[0].Spec
			if spec.Model != "m-b" || !composedHolds(t, spec, p2) || composedHolds(t, spec, p1) || spec.ResumeSessionFile != "/tmp/prober.jsonl" {
				t.Fatalf("the prober was relaunched on %q, its prompt holding P2 %t and P1 %t, on the session %q; want m-b with P2 alone, on its own session /tmp/prober.jsonl",
					spec.Model, composedHolds(t, spec, p2), composedHolds(t, spec, p1), spec.ResumeSessionFile)
			}
		})
	}
}

// composedHolds says whether the prompt spec composes holds text.
func composedHolds(t *testing.T, spec runtime.SpawnSpec, text string) bool {
	t.Helper()
	composed, err := runtime.ComposePrompt(spec.Prompt)
	if err != nil {
		t.Fatalf("compose the prompt of %s: %v", spec.Claim, err)
	}
	return strings.Contains(composed, text)
}

// modelOf is the model of each launch, in order.
func modelOf(launches []fake.Call) []string {
	models := make([]string, len(launches))
	for i, launch := range launches {
		models[i] = launch.Spec.Model
	}
	return models
}
