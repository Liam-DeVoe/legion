package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/wait"
)

// slotCalls is every call of the fake runtime that wrote or read token's slot, as "<method>
// <role>", in order; a write, which names no role since the runtime judges it, is its method alone.
func slotCalls(rt *fake.Runtime, token claim.Token) []string {
	var calls []string
	for _, call := range rt.Calls() {
		if (call.Method == "WriteSlotCredential" || call.Method == "SlotHoldsCredential") && call.Claim == token {
			calls = append(calls, strings.TrimSpace(call.Method+" "+string(call.Role)))
		}
	}
	return calls
}

// The owner is told its step once its slot holds the step's App. LEGION-1 stands at step 1 of
// `implementer:owner reviewer`, which its owner, LEGION-1's own architect, does:
//
//  1. While the runtime answers that the slot's gh files do not hold the implementer's App yet,
//     the notice owner-step of step 1 is not published, and its row waits as a notice held for its
//     architect; the runtime was asked to write the slot for the implementer, then asked whether
//     it holds it.
//  2. A pod whose launcher cannot say (runtime.ErrAwaitsPodUpdate) holds the row the same way.
//  3. Once the runtime answers that the slot holds it, the notice is published once, to the
//     owner's role topic.
//  4. A supervise slot_credential of the owner's claim asks the runtime to write the slot back to
//     the owner's own App when the row runs: no write while step 1 runs, whose owner-step writes
//     that step's App, and one once the task stands at step 2, the reviewer's step, which the
//     runtime makes for the App it judges for the slot; it waits for nothing.
//  5. A notice owner-step of step 1 that runs after the task moved to step 2 finishes unpublished,
//     with one log line, and asks the runtime nothing.
func TestAnOwnerStepIsToldOnceItsSlotHoldsTheStepsApp(t *testing.T) {
	ctx := context.Background()
	pool := isolatedOutboxPool(t)
	records := record.NewStore()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	owner := mustClaimToken(t, "LEGION-1", claim.RoleArchitect)
	issue := record.Issue{Key: "LEGION-1", Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: "LEGION-1", Project: "LEGION",
		Title: "LEGION-1", Phase: phase.Implementing, Generation: 1, Status: "in_progress", DispatchStatus: "in_progress", Rank: "A", LastDispatchSeq: 1}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		version := record.PlanVersion{Issue: "LEGION-1", Generation: 1, Version: 1, ArtifactID: "plan-artifact", Source: record.PlanSourceOwner,
			GivenBy: owner, GivenAt: now}
		task := record.PlanTask{Issue: "LEGION-1", Generation: 1, Task: "a", PlanVersion: 1, BlockText: "Build the widget.",
			State: record.TaskRunning, Place: record.TaskPlace{Kind: record.PlaceStep, Position: 1}}
		steps := []record.PlanStep{
			{Issue: "LEGION-1", Generation: 1, Task: "a", Position: 1, Role: "implementer", Type: "implementation", AgentKey: "owner"},
			{Issue: "LEGION-1", Generation: 1, Task: "a", Position: 2, Role: "reviewer", Type: "review"},
		}
		if err := records.PutIssue(ctx, tx, issue); err != nil {
			return err
		}
		return records.PutPlan(ctx, tx, version, []record.PlanTask{task}, steps)
	}); err != nil {
		t.Fatalf("seed LEGION-1 at its owner's implementer step: %v", err)
	}
	sup, rt := newOutboxSupervisor(t, "legion", t.TempDir())
	architectClaim(t, sup, "LEGION-1", supervise.StateWorking)
	publisher := &holderPublisher{}
	var logged syncBuffer
	runner := &outbox{log: slog.New(slog.NewJSONHandler(&logged, nil)), pool: pool, dispatchProject: "LEGION", records: records, notices: publisher,
		supervisor: sup, project: "legion", now: time.Now}
	published := func() int {
		_, delivered := publisher.snapshot()
		return len(delivered)
	}
	told := leasedOutboxRow(t, pool, records, mustOutboxRow(t, "LEGION-1", record.Notice{Kind: record.OwnerStepNotice, Role: claim.RoleImplementer,
		Phase: phase.Implementing, Reason: "Step 1 of 2: implementer:owner. Build the widget."}, now))
	again := func() {
		t.Helper()
		told.LeaseToken = leaseOutboxRow(t, pool, told.ID)
	}

	t.Run("1 a slot that does not hold the step's App yet", func(t *testing.T) {
		rt.ScriptSlotHolds(runtime.ErrSlotCredentialPending)
		err := runner.execute(ctx, told)
		if !errors.Is(err, errNoticeWaits) || !errors.Is(err, wait.ErrWaiting) || !errors.Is(err, runtime.ErrSlotCredentialPending) {
			t.Fatalf("the notice while the slot does not hold the implementer's App = %v, want a notice held for its architect", err)
		}
		if n := published(); n != 0 {
			t.Fatalf("the notice was published %d times while the slot does not hold the step's App, want none", n)
		}
		if got, want := slotCalls(rt, owner), []string{"WriteSlotCredential", "SlotHoldsCredential implementer"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("the runtime was asked %q of the owner's slot, want %q", got, want)
		}
	})

	t.Run("2 a pod whose launcher cannot say", func(t *testing.T) {
		rt.ScriptSlotHolds(runtime.ErrAwaitsPodUpdate)
		again()
		if err := runner.execute(ctx, told); !errors.Is(err, errNoticeWaits) || !errors.Is(err, runtime.ErrAwaitsPodUpdate) {
			t.Fatalf("the notice of a pod whose launcher cannot say = %v, want a notice held for its architect", err)
		}
		if n := published(); n != 0 {
			t.Fatalf("the notice was published %d times while the pod's launcher cannot say, want none", n)
		}
	})

	t.Run("3 a slot that holds the step's App", func(t *testing.T) {
		rt.ScriptSlotHolds(nil)
		again()
		if err := runner.execute(ctx, told); err != nil {
			t.Fatalf("the notice once the slot holds the implementer's App = %v", err)
		}
		_, delivered := publisher.snapshot()
		if len(delivered) != 1 || delivered[0].topic != architectTopic(t, "LEGION-1") {
			t.Fatalf("the notice publishes %+v, want one to the owner's role topic", delivered)
		}
		// The runner deletes a row its executor finished, so no later notice waits behind it.
		if _, err := pool.Exec(ctx, "delete from outbox where id = $1", told.ID); err != nil {
			t.Fatalf("finish the published row: %v", err)
		}
	})

	slotCredential := func(t *testing.T, writes bool) {
		t.Helper()
		before := len(slotCalls(rt, owner))
		row := leasedOutboxRow(t, pool, records, mustOutboxRow(t, "LEGION-1", record.SuperviseRequest{Op: "slot_credential", Tree: "LEGION-1",
			Role: claim.RoleArchitect, Claim: owner, Generation: 1}, now))
		if err := runner.execute(ctx, row); err != nil {
			t.Fatalf("the slot_credential row = %v", err)
		}
		if _, err := pool.Exec(ctx, "delete from outbox where id = $1", row.ID); err != nil {
			t.Fatalf("finish the slot_credential row: %v", err)
		}
		got := slotCalls(rt, owner)[before:]
		if !writes && len(got) != 0 || writes && (len(got) != 1 || got[0] != "WriteSlotCredential") {
			t.Fatalf("the slot_credential row asked the runtime %q of the owner's slot, want one write: %t", got, writes)
		}
	}
	t.Run("4 a slot_credential row writes the owner's own App back", func(t *testing.T) {
		slotCredential(t, false)
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := records.MoveTask(ctx, tx, "LEGION-1", 1, "a", record.TaskPlace{Kind: record.PlaceStep, Position: 2}, record.TaskRunning); err != nil {
				return err
			}
			moved := issue
			moved.Phase = phase.Reviewing
			return records.PutIssue(ctx, tx, moved)
		}); err != nil {
			t.Fatalf("move LEGION-1 to its reviewer's step: %v", err)
		}
		slotCredential(t, true)
	})

	t.Run("5 a notice of a step that no longer runs", func(t *testing.T) {
		before := len(slotCalls(rt, owner))
		stale := leasedOutboxRow(t, pool, records, mustOutboxRow(t, "LEGION-1", record.Notice{Kind: record.OwnerStepNotice, Role: claim.RoleImplementer,
			Phase: phase.Implementing, Reason: "Step 1 of 2: implementer:owner. Build the widget."}, now))
		if err := runner.execute(ctx, stale); err != nil {
			t.Fatalf("the notice of step 1 at step 2 = %v, want it finished", err)
		}
		if n := published(); n != 1 {
			t.Fatalf("the notices published = %d after the stale one, want the one of case 3 alone", n)
		}
		if got := slotCalls(rt, owner)[before:]; len(got) != 0 {
			t.Fatalf("the stale notice asked the runtime %q of the owner's slot, want nothing", got)
		}
		lines := staleLines(t, logged.String())
		if len(lines) != 1 {
			t.Fatalf("the log holds %d lines of the stale notice, want one:\n%s", len(lines), logged.String())
		}
		// The notice names no run, so the executor judges it by the role the owner's slot acts as,
		// which it read, and the line says what it read.
		if slot := lines[0]["slot"]; slot != string(claim.RoleArchitect) {
			t.Fatalf("the stale notice's line names the slot %v, want the architect it read:\n%s", slot, logged.String())
		}
	})
}
