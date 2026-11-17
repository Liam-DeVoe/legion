package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/notify"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// A keyed agent works the tester's step in the claim it started on the implementer's step, and the
// tester's phases row names that claim. Each row the workflow writes for the tester then reaches
// that one agent: the start delivers to its claim rather than starting a second session of the
// key, the step's stop suspends it, and the end of the phase is published on its topic. An update
// round's ask names the claim the sweep asked and is published where that claim records the round,
// also where that claim is not the one the record gives the role: a fresh tester an earlier
// version of the plan started still holds its claim in the pod. An ask the release before wrote
// names no claim, and reaches the claim the record gives the role.
func TestARowOfATesterStepReachesItsAgent(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	issue := record.Issue{Key: "LEGION-208", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Project: "LEGION",
		Tree: "LEGION-208", Title: "Workflow", Phase: phase.Testing, Generation: 1, Status: "testing"}
	putOutboxIssue(t, pool, records, issue)
	keyed := mustClaimToken(t, issue.Key, claim.RoleImplementer)
	own := mustClaimToken(t, issue.Key, claim.RoleTester)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, row := range []record.PhaseRow{
			{Issue: issue.Key, Role: claim.RoleImplementer, Claim: keyed, StepTask: "t", StepPlace: 1, Entry: 1},
			{Issue: issue.Key, Role: claim.RoleTester, Claim: keyed, StepTask: "t", StepPlace: 2, Entry: 2},
		} {
			if err := records.PutPhase(ctx, tx, row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write the phases rows of key a's two steps: %v", err)
	}
	sup, _ := newOutboxSupervisor(t, "legion", t.TempDir())
	machine, _, err := sup.Create(ctx, supervise.Claim{Token: keyed, Project: "legion", Tree: issue.Tree, Issue: issue.Key,
		Role: claim.RoleImplementer, State: supervise.StateQueued, UpdateRound: "r1"}, "")
	if err != nil {
		t.Fatalf("create the claim of key a: %v", err)
	}
	publisher := &outboxPublisher{}
	runner := &outbox{log: quietLogger(), dispatchProject: "LEGION", pool: pool, records: records, supervisor: sup, notices: publisher,
		trees: outboxTreeStore(t, pool, issue.Tree), tokens: outboxTokens{}, project: "legion", stateDir: t.TempDir(),
		provision: func(context.Context, workspace.Request) (workspace.Workspace, error) {
			return workspace.Workspace{Dir: t.TempDir(), Bookmark: "legion/LEGION-208"}, nil
		},
	}
	topic := notify.RoleTopicPrefix + string(keyed)

	start := mustOutboxRow(t, issue.Key, record.SuperviseRequest{Op: "start", Tree: issue.Tree, Role: claim.RoleTester, Phase: phase.Testing,
		Entry: 2, Task: "Test it.", Generation: 1}, time.Now())
	start.ID = 57
	if err := runner.execute(ctx, start); err != nil {
		t.Fatalf("the tester's start: %v", err)
	}
	if _, found := sup.Machine(own); found {
		t.Fatalf("the tester's start made a claim %s, want it delivered to key a's claim %s", own, keyed)
	}
	if got := machine.Claim(); got.Pending == nil || got.Pending.ID != "outbox:57" {
		t.Fatalf("key a's claim after the tester's start = %s holding %+v, want the start's task outbox:57", got.State, got.Pending)
	}

	launch := machine.Claim().Generation
	if err := machine.Handle(ctx, supervise.RequestRegister{Claim: keyed, Generation: launch, Session: "ses-a", SessionFile: "/tmp/a.jsonl"}); err != nil {
		t.Fatalf("register key a's agent: %v", err)
	}
	if err := machine.Handle(ctx, supervise.RequestReady{Claim: keyed, Generation: launch, Session: "ses-a"}); err != nil {
		t.Fatalf("ready key a's agent: %v", err)
	}
	stop := mustOutboxRow(t, issue.Key, record.SuperviseRequest{Op: "step_stop", Tree: issue.Tree, Role: claim.RoleTester, Generation: 1}, time.Now())
	stop.ID = 58
	if err := runner.execute(ctx, stop); err != nil {
		t.Fatalf("the tester's step stop: %v", err)
	}
	if got := machine.Claim().State; got != supervise.StateSuspended {
		t.Fatalf("key a's claim after the tester's step stop = %s, want suspended", got)
	}

	ended := mustOutboxRow(t, issue.Key, record.PhaseEnded{Role: claim.RoleTester, From: phase.Testing, To: phase.Implementing,
		Reason: "the head conflicts with main"}, time.Now())
	ended.ID = 59
	if err := runner.execute(ctx, ended); err != nil {
		t.Fatalf("the end of the tester's phase: %v", err)
	}
	if published := publisher.publishes(); len(published) != 1 || published[0].topic != topic {
		t.Fatalf("the end of the tester's phase was published as %+v, want once on %s", published, topic)
	}

	ask := mustOutboxRow(t, issue.Key, record.UpdateAsk{Role: claim.RoleTester, Claim: keyed, Round: "r1", Text: "Push your work."}, time.Now())
	ask.ID = 60
	if err := runner.execute(ctx, ask); err != nil {
		t.Fatalf("the update ask: %v", err)
	}
	if published := publisher.publishes(); len(published) != 2 || published[1].topic != topic || published[1].message != "Push your work." {
		t.Fatalf("the update ask was published as %+v, want it on %s", published, topic)
	}

	// A fresh tester of an earlier version of the plan still holds its own claim in the pod, and
	// the round's sweep asks it too: the ask names it, and reaches it rather than key a's agent.
	if _, _, err := sup.Create(ctx, supervise.Claim{Token: own, Project: "legion", Tree: issue.Tree, Issue: issue.Key,
		Role: claim.RoleTester, State: supervise.StateQueued, UpdateRound: "r1"}, ""); err != nil {
		t.Fatalf("create the earlier version's tester claim: %v", err)
	}
	stale := mustOutboxRow(t, issue.Key, record.UpdateAsk{Role: claim.RoleTester, Claim: own, Round: "r1", Text: "Push yours too."}, time.Now())
	stale.ID = 61
	if err := runner.execute(ctx, stale); err != nil {
		t.Fatalf("the update ask of the earlier version's tester: %v", err)
	}
	if published := publisher.publishes(); len(published) != 3 || published[2].topic != notify.RoleTopicPrefix+string(own) || published[2].message != "Push yours too." {
		t.Fatalf("the update ask of the earlier version's tester was published as %+v, want it on %s", published, notify.RoleTopicPrefix+string(own))
	}

	before := record.OutboxRow{ID: 62, Issue: issue.Key, Kind: record.OutboxKindUpdateAsk, NextAt: time.Now(),
		Payload: json.RawMessage(`{"role":"tester","round":"r1","text":"Push your work again."}`)}
	if err := runner.execute(ctx, before); err != nil {
		t.Fatalf("the update ask of the release before: %v", err)
	}
	if published := publisher.publishes(); len(published) != 4 || published[3].topic != topic || published[3].message != "Push your work again." {
		t.Fatalf("the update ask of the release before was published as %+v, want it on %s", published, topic)
	}
}
