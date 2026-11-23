package workflow

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/config"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// heldParentNeverReleased is a Dispatch child of LEGION-208 its architect filed and never
// released: the daemon keeps no record of it, only its mirror of what Dispatch shows.
const heldParentNeverReleased = "LEGION-211"

// retroHeldStarts is the sentence every retro-held notice, every refusal of a release of the hold
// and every reason beside a todo set back on a held root says what starts the retro with
// (intake.HeldParentRetroStarts), written out so the words are held here.
const retroHeldStarts = "Legion starts the retro by itself once nothing holds it any longer: no issue beneath it is still in the workflow, " +
	"no root that rests on its approval is still in the workflow, and no Dispatch child of it is open."

// reviewingWithoutAcceptance walks key, a root at admitted with its open pull request, through the
// plan `implementer reviewer` to its reviewing at step 2.
func reviewingWithoutAcceptance(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string) {
	t.Helper()
	seedStepsPlan(t, pool, key, 1, "implementer", "reviewer")
	move(t, pool, engine, key, TriggerGateOpened, "", record.PhaseRow{})
	move(t, pool, engine, key, TriggerImplementationReady, "", record.PhaseRow{})
	assertWalksAt(t, pool, engine, key, phase.Reviewing, record.TaskPlace{Kind: record.PlaceStep, Position: 2})
}

// approvesReviewOf settles key's review round as approved, as settleRound moves it (approveReview's
// move, for a key of the test's choosing).
func approvesReviewOf(t *testing.T, pool *pgxpool.Pool, engine *Engine, key string) {
	t.Helper()
	row := roleRowOf(t, pool, engine, key, claim.RoleReviewer)
	row.HandoffRecord, row.HandoffVersion, row.HandoffCommit, row.Summary = "review-record", 1, "c0ffee", "approved"
	onStored(t, pool, engine, key, func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
		if err := engine.store.PutPhase(ctx, tx, row); err != nil {
			return err
		}
		pr, err := engine.store.PullRequest(ctx, tx, key)
		if err != nil {
			return err
		}
		return engine.transition(ctx, tx, issue, TriggerReviewApproved, "", row, pr, "")
	})
}

// heldBeforeItsRetro fails unless key stands held before its retro for its children
// (record.HoldForChildren) at its task's retro place, accepted by nothing (record.Accepted), with
// no retro started; why is the step the test reads it after.
func heldBeforeItsRetro(t *testing.T, pool *pgxpool.Pool, engine *Engine, key, why string) {
	t.Helper()
	held := issueOf(t, pool, engine, key)
	if held.Phase != phase.Held || held.Hold == nil || *held.Hold != (record.Hold{From: phase.Retro, Reason: record.HoldForChildren}) {
		t.Fatalf("%s %s stands in %s with hold %+v, want held from retro for its children", key, why, held.Phase, held.Hold)
	}
	if record.Accepted(held) {
		t.Fatalf("%s %s reads as accepted, want it accepted by nothing while it is held before its retro", key, why)
	}
	if task := taskOf(t, pool, engine, key); task.State != record.TaskRunning || task.Place != (record.TaskPlace{Kind: record.PlaceRetro}) {
		t.Fatalf("task a of %s %s is %s at %+v, want running at its retro", key, why, task.State, task.Place)
	}
	if got := startsIn(t, pool, key, claim.RoleImplementer, phase.Retro); len(got) != 0 {
		t.Fatalf("%s %s has the retro's starts %+v, want none", key, why, got)
	}
}

// retryRefusedWith applies the architect's retry of key's hold and fails unless it is refused 409
// HELD_FOR_CHILDREN with message.
func retryRefusedWith(t *testing.T, pool *pgxpool.Pool, engine *Engine, key, message string) {
	t.Helper()
	result := applyFacts(t, pool, engine)("retry-"+key, intake.RetryOrEscalate{Issue: key, Decision: intake.RetryDecision, Reason: "try again"})
	if want := (&intake.Refusal{Status: 409, Code: "HELD_FOR_CHILDREN", Message: message}); !reflect.DeepEqual(result.Refusal, want) {
		t.Fatalf("the retry of %s = %+v, want %+v", key, result.Refusal, want)
	}
}

// A parent whose plan has no acceptance step passes its last step while a root of another tree
// rests on its approval (dispatch://INFRA-3320 R8): P (LEGION-100) walks `implementer reviewer`,
// and R (LEGION-208), a view root of its own tree, has its spec step waiting on P's open approval
// request. The checks of the issues under an ancestor prove its requirements together, so P is
// accepted only after R, which no sign-off reads on such a plan. So the reviewer's pass holds P
// before its retro: held from retro for its children, accepted by nothing, R's wait on P's request
// standing, and P's architect told once which root holds it, in the words of the sign-off's
// refusal. A retry of the hold is refused naming R. R's leave of the workflow ends the hold with
// nobody acting: P's retro starts in the fact of the leave, which writes no issue of P's tree.
func TestARootRestingOnAParentHoldsItsRetroUntilTheRootLeaves(t *testing.T) {
	pool := migratedPool(t)
	engine := gatedStepEngine(config.DesignGateRootIssues, nil)
	seedIssue(t, pool, record.Issue{Key: basisAncestor, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
		Tree: basisAncestor, Project: "LEGION", Title: "ancestor", Phase: phase.Admitted, Generation: 1, Status: "in_progress", Rank: "T", LastDispatchSeq: 1})
	seedTreeLifecycle(t, pool, basisAncestor)
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: basisAncestor, Repo: "sjawhar/legion", Number: 41,
		Branch: "legion/" + basisAncestor, HeadSHA: "head", Failing: []string{}})
	reviewingWithoutAcceptance(t, pool, engine, basisAncestor)
	// R, a view on P, admitted in its own tree as seedGatedTree records it, its spec step waiting
	// on P's approval request.
	seedIssue(t, pool, record.Issue{Key: gatedRoot, Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
		Tree: gatedRoot, Project: "LEGION", Title: "root", Phase: phase.Admitted, Generation: 1, Status: "in_progress", Rank: "U",
		HandedOver: true, LastDispatchSeq: 1, ViewOf: basisAncestor})
	seedSlot(t, pool, record.Slot{Issue: gatedRoot, Index: 0, AdmittedAt: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)})
	seedTreeLifecycle(t, pool, gatedRoot)
	seedArchitectClaim(t, pool, gatedRoot)
	waiting := pendingBasis()
	waiting.RegisteredAt = basisEngineNow
	seedGate(t, pool, record.PendOnBasis(record.DesignGate{Issue: gatedRoot, ArtifactID: gateDoc, LatestVersion: 4}, *waiting, "ask-100"))

	approvesReviewOf(t, pool, engine, basisAncestor)

	heldBeforeItsRetro(t, pool, engine, basisAncestor, "after the reviewer's pass")
	if gate := gatesOf(t, pool, gatedRoot)[record.DesignStepSpec]; !gate.PendingOnBasis() || gate.Basis.Pending.Ask != "ask-100" {
		t.Fatalf("R's spec step after P's hold = %#v, want it still waiting on ask-100: nothing accepted P", gate)
	}
	if ended := noticesOfKind(t, pool, gatedRoot, "basis-ended"); len(ended) != 0 {
		t.Fatalf("R was told %+v, want no basis-ended notice: its wait on P stands", ended)
	}
	want := []record.Notice{{Kind: "retro-held", Role: claim.RoleImplementer, Phase: phase.Retro, Reason: "LEGION-100 passed its last step, and Legion holds it before its retro " +
		"while these roots that rest on its approval are still in the workflow: LEGION-208 (its spec step, in admitted). " +
		"The design-gate step of each rests on an approval of LEGION-100, so LEGION-100 is accepted after them. " +
		"Each is a tree of its own: its own architect, or a person, moves it. " + retroHeldStarts}}
	if got := noticesOf(t, pool, basisAncestor, "retro-held"); !reflect.DeepEqual(got, want) {
		t.Fatalf("the retro-held notices of P = %+v, want %+v", got, want)
	}
	retryRefusedWith(t, pool, engine, basisAncestor, "LEGION-100 waits before its retro while these roots that rest on its approval are still in the workflow: "+
		"LEGION-208 (its spec step, in admitted). "+retroHeldStarts+" This retry changed nothing.")

	closeIssue(t, pool, engine, gatedRoot, "", "done", 20)

	assertWalksAt(t, pool, engine, basisAncestor, phase.Retro, record.TaskPlace{Kind: record.PlaceRetro})
	if got := issueOf(t, pool, engine, basisAncestor); got.Hold != nil || !record.Accepted(got) {
		t.Fatalf("P after R left = %s with hold %+v, want its retro started and P accepted", got.Phase, got.Hold)
	}
	if starts := startsIn(t, pool, basisAncestor, claim.RoleImplementer, phase.Retro); len(starts) != 1 {
		t.Fatalf("R's leave wrote %d starts of P's retro, want one", len(starts))
	}
	if got := noticesOf(t, pool, basisAncestor, "retro-held"); len(got) != 1 {
		t.Fatalf("P was told %d retro-held notices in all, want the one of its hold", len(got))
	}
}

// A parent whose plan has no acceptance step passes its last step while a Dispatch child of it
// that its architect filed and never released is open (dispatch://INFRA-3320 R8, record.ChildOpen):
// the child has no record, so nothing beneath the parent is in the workflow, and only the mirror of
// Dispatch shows it. A parent is accepted after its children, so the reviewer's pass holds it
// before its retro, accepted by nothing, its architect told once which child holds it, and a retry
// is refused naming it. The admission pass that releases it once Dispatch closes the child is
// admit's TestAParentHeldForAChildNeverReleasedStartsItsRetroWhenDispatchClosesIt. The child is
// filed after the review began: before it, an open child would have held the start of the review
// itself (childrenHolding).
func TestAChildNeverReleasedHoldsAParentBeforeItsRetro(t *testing.T) {
	pool := migratedPool(t)
	engine, _ := loggedEngine()
	stepRoot(t, pool, phase.Admitted)
	reviewingWithoutAcceptance(t, pool, engine, "LEGION-208")
	seedMirrorChild(t, pool, heldParentNeverReleased, "backlog", "LEGION-208")

	approveReview(t, pool, engine)

	heldBeforeItsRetro(t, pool, engine, "LEGION-208", "after the reviewer's pass")
	want := []record.Notice{{Kind: "retro-held", Role: claim.RoleImplementer, Phase: phase.Retro, Reason: "LEGION-208 passed its last step, and Legion holds it before its retro " +
		"while these Dispatch children of it are open: LEGION-211 (backlog). " +
		"A parent is accepted after its children, a child that runs as a tree of its own included: run each one to done, releasing one that was never released. " +
		"A child that runs as a tree of its own is moved by its own architect or a person. " + retroHeldStarts + " " +
		record.StatusNotTakenYet + " " + record.EndChildRule}}
	if got := noticesOf(t, pool, "LEGION-208", "retro-held"); !reflect.DeepEqual(got, want) {
		t.Fatalf("the retro-held notices = %+v, want %+v", got, want)
	}
	retryRefusedWith(t, pool, engine, "LEGION-208", "LEGION-208 waits before its retro while these Dispatch children of it are open: LEGION-211 (backlog). "+
		retroHeldStarts+" This retry changed nothing.")
}
