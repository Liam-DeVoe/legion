package admit

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/workflow"
)

// The task a parent of these tests held when it entered awaiting_children, and the reason its
// move wrote.
const (
	acHeldTask   = "Continue LEGION-300. Issue: LEGION-300. Phase: implementing."
	acHeldReason = "a review asked for changes, while these Dispatch children of LEGION-300 were open: LEGION-301 (backlog)"
)

// The controller's tick names a parent waiting in awaiting_children under `blocked` with its open
// children, as the state does (dispatch://INFRA-3320 R11): both read blockers.Blocked over the
// one judgement of the pass (blockers.ChildRows), so what the controller is told and what
// `legion state` shows agree. A child already done is no part of the row.
func TestTheTickNamesAWaitingParentBlockedByItsOpenChildren(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	spWaiting(t, pool, spParent, phase.Implementing, acHeldTask, acHeldReason, nil)
	seedMirrorChild(t, pool, spChild, "backlog", spParent)
	seedMirrorChild(t, pool, "LEGION-302", "done", spParent)

	var held []record.HeldIssue
	var blocked []record.BlockedIssue
	inTx(t, pool, func(tx pgx.Tx) {
		var err error
		if held, blocked, _, err = admission.tickRows(context.Background(), tx); err != nil {
			t.Fatalf("read the tick's rows: %v", err)
		}
	})
	if want := []record.BlockedIssue{{Issue: spParent, BlockedBy: []string{spChild}}}; !reflect.DeepEqual(blocked, want) {
		t.Fatalf("the tick's blocked list = %+v, want %+v: the waiting parent with its one open child", blocked, want)
	}
	want := record.HeldIssue{Issue: spParent, Reason: record.HeldAwaitingChildren, HeldBy: []string{spChild}}
	if !slices.ContainsFunc(held, func(row record.HeldIssue) bool { return reflect.DeepEqual(row, want) }) {
		t.Fatalf("the tick's held list = %+v, want %+v beside it", held, want)
	}
}

// The repair of a role behind the entry its phases row names (owedEntryRoles, which the entry
// sweep and every admission pass send through resumeSlottedWorkers) starts no role of a parent
// waiting in awaiting_children (dispatch://INFRA-3320 R11): that phase has no worker
// (workflow.OpenPhaseRole), so the selection passes over it. The parent here even holds a slot
// row, which puts it among the repair's candidates, and its implementer's claim stands behind the
// entry its phases row names. The control is the same record in implementing, whose implementer
// the sweep sends that entry's work.
func TestTheEntrySweepStartsNoRoleOfAWaitingParent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     phase.Phase
		starts int
	}{
		{name: "in implementing, the control", at: phase.Implementing, starts: 1},
		{name: "in awaiting_children", at: phase.AwaitingChildren},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			admission := quiet(t, 1)
			spSeedParent(t, pool, spParent, tc.at, true, func(issue *record.Issue) {
				if tc.at == phase.AwaitingChildren {
					waited := fixedNow.Add(-2 * time.Hour)
					issue.ChildrenWait = &record.ChildrenWait{Start: phase.Implementing, Task: acHeldTask, Reason: acHeldReason, Since: waited}
					issue.ForwardMergeOwed, issue.ChildrenWaitedAt = true, &waited
				}
			})
			token := putWorkerClaim(t, pool, spParent, spParent, claim.RoleImplementer, 1, supervise.StateIdle, "")
			markGivenEntry(t, pool, token, seededEntry-1)
			spSeedPhase(t, pool, record.PhaseRow{Issue: spParent, Role: claim.RoleImplementer, Claim: token, Entry: seededEntry,
				EntryTask: seededEntryTask(spParent), EntryRaisedAt: fixedNow.Add(-time.Hour)})

			var owes bool
			inTx(t, pool, func(tx pgx.Tx) {
				var err error
				if owes, err = admission.OwesEntryWork(context.Background(), tx); err != nil {
					t.Fatalf("ask whether any role is owed its entry's work: %v", err)
				}
			})
			if owes != (tc.starts > 0) {
				t.Fatalf("OwesEntryWork = %t, want %t", owes, tc.starts > 0)
			}
			applySweep(t, pool, admission, "sweep")

			if starts := convergeStarts(t, pool, spParent); len(starts) != tc.starts {
				t.Fatalf("the start rows of %s in %s = %+v, want %d", spParent, tc.at, starts, tc.starts)
			}
		})
	}
}

// awaitingRun is a quiet project whose root LEGION-950 waits in awaiting_children, under the cap
// of four the stalled-tree tests take, with its architect idle and a controller registered to be
// told, measured at the deployment's own bound.
func awaitingRun(t *testing.T) (*pgxpool.Pool, *Admission) {
	t.Helper()
	pool := migratedPool(t)
	admission := quiet(t, 4)
	admission.UseStalledTreeNotice(deploymentBound(t, ""))
	registerController(t, pool)
	spWaiting(t, pool, "LEGION-950", phase.Implementing, "Continue LEGION-950. Issue: LEGION-950. Phase: implementing.",
		"a review asked for changes, while these Dispatch children of LEGION-950 were open: LEGION-951", nil)
	seedMirror(t, pool, "LEGION-950", "in_progress")
	seedClaim(t, claimStore(t, pool), "LEGION-950", "LEGION-950", claim.RoleArchitect, supervise.StateIdle)
	return pool, admission
}

// The stalled-tree measure reads a parent in awaiting_children as it reads a parent whose own first
// phase waits on its children (dispatch://INFRA-3320 R13; dispatch://INFRA-3036's rule, which
// lineHolds.waitsOnWorkOutsideItsTree states): a wait on children the daemon has released is a
// wait by design whatever the line does, and a wait on a child nobody released is a wait by
// design only while the project's line can move, since releasing that child is the tree's own
// work. Each project is the waiting root alone, quiet past the bound, measured over three passes.
//
// The line moves in the third case because a claim of another tree is at work (lineCanMove's
// first term): that tree is not told, being at work, and the waiting tree is not told either,
// which is the case a child filed and not released meets in a project whose work goes on.
func TestTheMeasureReadsAWaitingParentAsAFirstPhaseChildrenWait(t *testing.T) {
	const (
		root  = "LEGION-950"
		child = "LEGION-951"
		other = "OTHER-9"
	)

	t.Run("a child nobody released, the line stopped", func(t *testing.T) {
		pool, admission := awaitingRun(t)
		seedMirrorChild(t, pool, child, "backlog", root)
		for pass, roots := range waitPasses(t, pool, admission, nil, root) {
			if want := []string{root}; !reflect.DeepEqual(roots, want) {
				t.Fatalf("after pass %d the controller was told of %v, want %v: a wait on a child nobody released is the tree's own work", pass+1, roots, want)
			}
		}
	})

	t.Run("a child nobody released, the line moving", func(t *testing.T) {
		pool, admission := awaitingRun(t)
		seedMirrorChild(t, pool, child, "backlog", root)
		const working = "LEGION-960"
		seedQuietRoot(t, pool, working, "B", phase.Implementing)
		seedMirror(t, pool, working, "in_progress")
		seedClaim(t, claimStore(t, pool), working, working, claim.RoleImplementer, supervise.StateWorking)
		for pass, roots := range waitPasses(t, pool, admission, nil, root, working) {
			if roots != nil {
				t.Fatalf("pass %d told the controller of %v, want nothing: %s waits by design while %s's work goes on", pass+1, roots, root, working)
			}
		}
	})

	t.Run("a child the daemon released, the line stopped", func(t *testing.T) {
		pool, admission := awaitingRun(t)
		// The released child: recorded in the tree, in implementing, and itself waiting on an
		// issue of another project, so it gave its own slot back and nothing of the project can
		// move. Neither wait is the tree's own work.
		seedQuietChild(t, pool, root, child, "A1", phase.Implementing, "in_progress")
		takeSlot(t, pool, child)
		seedMirrorChild(t, pool, child, "in_progress", root, other)
		seedMirrorIn(t, pool, "OTHER", other, "in_progress")
		seedClaim(t, claimStore(t, pool), root, child, claim.RoleImplementer, supervise.StateIdle)
		for pass, roots := range waitPasses(t, pool, admission, []string{child}, root, child) {
			if roots != nil {
				t.Fatalf("pass %d told the controller of %v, want nothing: %s waits on a child the daemon released, and that child waits on %s",
					pass+1, roots, root, other)
			}
		}
		if got := issue(t, pool, root); got.Phase != phase.AwaitingChildren {
			t.Fatalf("%s after the passes = %s, want it waiting still on its released child", root, got.Phase)
		}
	})
}

// A linger's hand-over carries a child waiting in awaiting_children whole (dispatch://INFRA-3320
// R13): the readmission's orphan branch keeps the issue row, so the child becomes a root of its own
// at a new generation still in awaiting_children, with the start it held, the forward merge it
// owes and the moment it last waited, its pull request, its phases rows and its old root's design
// gate. Its promotion as a queued root opens its tree, writes it in_progress, takes no slot —
// awaiting_children keeps none, so the cap does not bind it (capLets), and the cap here is full —
// and starts its architect alone: no worker of a waiting parent starts (startMidPhaseMembers).
// Once its own child is done, the next pass takes it back to the start it held, whose worker's
// start waits in the line for the slot the cap has not got.
func TestALingersHandOverCarriesAWaitingChildWhole(t *testing.T) {
	const (
		root       = "LEGION-208"
		child      = "LEGION-209"
		grandchild = "LEGION-210"
		artifact   = "7d1e3a5c-9b2f-4c6e-8a40-3f5d7b9e1c26"
		task       = "Continue LEGION-209. Issue: LEGION-209. Phase: implementing."
		reason     = "a review asked for changes, while these Dispatch children of LEGION-209 were open: LEGION-210 (backlog)"
	)
	pool := migratedPool(t)
	admission := newAdmission(t, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine := workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories,
		Steps: config.DesignGateRootIssues.Steps(), Linger: time.Hour, Clock: func() time.Time { return fixedNow }}, nil)
	seedStarted(t, pool, spFiller, phase.Implementing, nil)
	until := fixedNow.Add(time.Hour)
	waited := fixedNow.Add(-2 * time.Hour)
	held := record.ChildrenWait{Start: phase.Implementing, Task: task, Reason: reason, Since: waited}
	parent := root
	inTx(t, pool, func(tx pgx.Tx) {
		records, ctx := record.NewStore(), context.Background()
		for _, put := range []error{
			records.PutIssue(ctx, tx, record.Issue{Key: root, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root,
				Project: testProject, Title: root, Phase: phase.Done, Generation: 1, Status: "done", LingerUntil: &until}),
			records.PutIssue(ctx, tx, record.Issue{Key: child, Repo: "acme/widgets", DefaultBranch: "main", GitHubRepo: "acme/widgets", Tree: root,
				Project: testProject, Title: child, Parent: &parent, Phase: phase.AwaitingChildren, Generation: 1, Status: "in_progress", Rank: "B",
				LastDispatchSeq: 1, ChildrenWait: &held, ForwardMergeOwed: true, ChildrenWaitedAt: &waited}),
			records.PutGate(ctx, tx, record.DesignGate{Issue: root, ArtifactID: artifact, LatestVersion: 2, ApprovedVersion: new(2)}),
			records.PutPullRequest(ctx, tx, record.PullRequest{Issue: child, Repo: "acme/widgets", Number: 42, Branch: "legion/LEGION-209",
				HeadSHA: "head-42", Generation: 1, State: record.PullRequestOpen}),
			records.PutPhase(ctx, tx, record.PhaseRow{Issue: child, Role: claim.RoleImplementer, Claim: "implementer", Entry: 2,
				EntryTask: task, EntryRaisedAt: waited.Add(-time.Hour)}),
			records.PutHandover(ctx, tx, record.Handover{Issue: child, Tree: root, Generation: 1}),
		} {
			if put != nil {
				t.Fatalf("seed: %v", put)
			}
		}
	})
	seedMirrorChild(t, pool, grandchild, "backlog", child)

	apply(t, pool, admission, "handover", intake.DispatchIssue{Key: child, ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
		Status: "todo", Title: child, Parent: root, Rank: "B", HandedOver: true, DaemonActor: true}, engine)

	got := issue(t, pool, child)
	if got.Tree != child || got.Generation != 2 || got.Phase != phase.AwaitingChildren || !sameHeldStart(got.ChildrenWait, &held) {
		t.Fatalf("the handed-over child = tree %s, generation %d, phase %s, held start %+v; want its own tree at generation 2, waiting still with %+v",
			got.Tree, got.Generation, got.Phase, got.ChildrenWait, held)
	}
	if !got.ForwardMergeOwed || got.ChildrenWaitedAt == nil || !got.ChildrenWaitedAt.Equal(waited) {
		t.Fatalf("the handed-over child = forward merge owed %t, waited at %v; want both carried", got.ForwardMergeOwed, got.ChildrenWaitedAt)
	}
	// Promoted as any handed-over root is: in_progress, its tree open and the mark of its arrival
	// gone, holding no slot under the full cap, with its architect started and no worker.
	if got.Status != "in_progress" || got.HandedOverFrom != "" {
		t.Fatalf("the handed-over child = status %s, handed over from %q; want it promoted in_progress", got.Status, got.HandedOverFrom)
	}
	if holdsSlot(t, pool, child) {
		t.Fatalf("slots = %+v, want none for %s: awaiting_children keeps no slot", slots(t, pool), child)
	}
	if architectStarts(t, pool, child) != 1 {
		t.Fatalf("architect starts of %s = %d, want one", child, architectStarts(t, pool, child))
	}
	for _, start := range startsOf(t, pool, child) {
		if start.role != claim.RoleArchitect {
			t.Fatalf("starts of %s = %+v, want the architect's alone: no worker of a waiting parent starts", child, startsOf(t, pool, child))
		}
	}
	written := false
	for _, effect := range effects(t, pool) {
		if effect.issue == child && reflect.DeepEqual(effect.payload, record.OrdinaryStatusWrite("in_progress", "todo", "")) {
			written = true
		}
	}
	if !written {
		t.Fatalf("outbox effects = %+v, want %s written in_progress at its promotion", effects(t, pool), child)
	}
	inTx(t, pool, func(tx pgx.Tx) {
		records, ctx := record.NewStore(), context.Background()
		pr, err := records.PullRequest(ctx, tx, child)
		if err != nil || pr == nil || pr.HeadSHA != "head-42" || pr.Generation != 2 {
			t.Fatalf("carried pull request = %#v, %v; want head-42 at generation 2", pr, err)
		}
		gates, err := records.GatesOf(ctx, tx, child)
		if gate := record.SpecGate(gates); err != nil || gate == nil || gate.ArtifactID != artifact {
			t.Fatalf("carried gate = %#v, %v", gates, err)
		}
	})
	if row := spPhaseRow(t, pool, child, claim.RoleImplementer); row.Entry != 2 || row.EntryTask != task {
		t.Fatalf("the implementer's phases row = %+v, want it carried at entry 2", row)
	}

	// Its own child is done: the next pass takes it back to the start it held.
	spMirror(t, pool, grandchild, "done", child, 2)
	spPass(t, pool, admission)

	back := issue(t, pool, child)
	if back.Phase != phase.Implementing || back.ChildrenWait != nil {
		t.Fatalf("the carried root after its child is done = phase %s, held start %+v; want implementing with nothing held", back.Phase, back.ChildrenWait)
	}
	starts := startsOf(t, pool, child)
	implementer := slices.IndexFunc(starts, func(start midPhaseStart) bool { return start.role == claim.RoleImplementer })
	if implementer < 0 || !starts[implementer].needsSlot || starts[implementer].claimable ||
		!strings.HasPrefix(starts[implementer].task, task) {
		t.Fatalf("starts of %s = %+v, want the implementer's on the held task, waiting for a slot under the full cap", child, starts)
	}
}

// A root that waited in awaiting_children and that a person parked lingers at done with nothing
// held (workflow's leave), and a person's todo then re-admits it as a new generation at admitted
// with no held start: the start it held went with the generation
// that held it. Its first phase waits on its still open child under the children-first rule, as
// any re-admitted root's does.
func TestARootReadmittedFromTheBacklogHoldsNoStart(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	engine := workflow.New(record.NewStore(), workflow.Config{Project: testProject, Repositories: testRepositories,
		Steps: config.DesignGateRootIssues.Steps(), Linger: time.Hour, Clock: func() time.Time { return fixedNow }}, nil)
	spWaiting(t, pool, spParent, phase.Implementing, acHeldTask, acHeldReason, func(issue *record.Issue) { issue.LastDispatchSeq = 1 })
	seedMirrorChild(t, pool, spChild, "backlog", spParent)

	apply(t, pool, admission, "parked", intake.DispatchIssue{Key: spParent, ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
		Status: "backlog", Title: spParent, Rank: spParent, HandedOver: true}, engine)
	if parked := issue(t, pool, spParent); parked.Phase != phase.Done || parked.ChildrenWait != nil || parked.LingerUntil == nil {
		t.Fatalf("the parked root = phase %s, held start %+v, linger %v; want done, nothing held, lingering", parked.Phase, parked.ChildrenWait, parked.LingerUntil)
	}

	apply(t, pool, admission, "readmitted", intake.DispatchIssue{Key: spParent, ResolvedRepo: "acme/widgets", Seq: 3, Type: "issue.updated",
		Status: "todo", Title: spParent, Rank: spParent, HandedOver: true}, engine)

	got := issue(t, pool, spParent)
	if got.Generation != 2 || got.Phase != phase.Admitted || got.ChildrenWait != nil {
		t.Fatalf("the re-admitted root = generation %d, phase %s, held start %+v; want generation 2 at admitted holding nothing",
			got.Generation, got.Phase, got.ChildrenWait)
	}
}

// The re-admission's own clear of the held start, read where it is written (readmit's non-orphan
// branch): a root whose record still stands in awaiting_children when its todo re-admits it — a
// row no departure of this release leaves, since the leave writes the phase done and takes the
// held start with it — starts its new generation at admitted with nothing held, rather than
// failing the record's shape, which refuses a held start beside any other phase.
func TestAReadmissionClearsTheHeldStart(t *testing.T) {
	pool := migratedPool(t)
	admission := quiet(t, 2)
	until := fixedNow.Add(time.Hour)
	spWaiting(t, pool, spParent, phase.Testing, "Continue LEGION-300. Issue: LEGION-300. Phase: testing.", acHeldReason, func(issue *record.Issue) {
		issue.Status, issue.LingerUntil, issue.LastDispatchSeq = "backlog", &until, 1
	})

	apply(t, pool, admission, "readmitted", intake.DispatchIssue{Key: spParent, ResolvedRepo: "acme/widgets", Seq: 2, Type: "issue.updated",
		Status: "todo", Title: spParent, Rank: spParent, HandedOver: true}, engineStub{})

	got := issue(t, pool, spParent)
	if got.Generation != 2 || got.Phase != phase.Admitted || got.ChildrenWait != nil {
		t.Fatalf("the re-admitted root = generation %d, phase %s, held start %+v; want generation 2 at admitted holding nothing",
			got.Generation, got.Phase, got.ChildrenWait)
	}
}

// A daemon restart between the move into awaiting_children and the return changes nothing
// (dispatch://INFRA-3320 R13): the held start is the issue row's own (record.Issue.ChildrenWait),
// not anything the daemon holds in memory. An admission and an engine built anew over the same
// database, as a restart builds them, read the parent back exactly as the move wrote it, and the
// first pass after the child is done takes it back to the start it held with the task it held.
func TestARestartBetweenTheMoveAndTheReturnChangesNothing(t *testing.T) {
	pool := migratedPool(t)
	given := "Continue LEGION-300. Issue: LEGION-300. Phase: implementing. Reason: a review asked for changes."
	spSeedParent(t, pool, spParent, phase.Implementing, true, nil)
	seedPullRequest(t, pool, spParent, 42, record.PullRequestOpen)
	spSeedPhase(t, pool, record.PhaseRow{Issue: spParent, Role: claim.RoleImplementer, Claim: "implementer", Entry: 3,
		EntryTask: given, EntryRaisedAt: fixedNow.Add(-time.Hour)})
	seedMirrorChild(t, pool, spChild, "backlog", spParent)

	spPass(t, pool, quiet(t, 2))
	moved := issue(t, pool, spParent)
	if moved.Phase != phase.AwaitingChildren || moved.ChildrenWait == nil || moved.ChildrenWait.Task != given {
		t.Fatalf("the parent after the move = phase %s, held start %+v; want awaiting_children holding implementing on %q", moved.Phase, moved.ChildrenWait, given)
	}

	// The restart: nothing of the first admission or its engine is used again, and the record is
	// all the restarted one reads.
	restarted := quiet(t, 2)
	spPass(t, pool, restarted)
	if again := issue(t, pool, spParent); again.Phase != phase.AwaitingChildren || !sameHeldStart(again.ChildrenWait, moved.ChildrenWait) ||
		!again.ForwardMergeOwed || again.ChildrenWaitedAt == nil || !again.ChildrenWaitedAt.Equal(*moved.ChildrenWaitedAt) {
		t.Fatalf("the parent after the restarted daemon's first pass = %+v, want it waiting still as the move wrote it, %+v", again, moved)
	}

	spMirror(t, pool, spChild, "done", spParent, 2)
	spPass(t, pool, restarted)
	back := issue(t, pool, spParent)
	if back.Phase != phase.Implementing || back.ChildrenWait != nil {
		t.Fatalf("the parent after its child is done = phase %s, held start %+v; want implementing with nothing held", back.Phase, back.ChildrenWait)
	}
	starts := startsOf(t, pool, spParent)
	if len(starts) != 1 || starts[0].role != claim.RoleImplementer || !strings.HasPrefix(starts[0].task, given) {
		t.Fatalf("starts of %s = %+v, want the implementer's on the task it held", spParent, starts)
	}
}

// sameHeldStart says whether a and b are one held start, its moment compared as an instant.
func sameHeldStart(a, b *record.ChildrenWait) bool {
	return a != nil && b != nil && a.Start == b.Start && a.Task == b.Task && a.Reason == b.Reason && a.Since.Equal(b.Since)
}
