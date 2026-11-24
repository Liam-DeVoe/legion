package workflow

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// askedAtReview is LEGION-208 at the reviewer's step of words, the steps of task a, with the merge
// forward of its pull request #42 asked in reviewing and not landed, of the claim the implementer's
// phases row names, the agent that did step 1 (record.MergeForward.Agent).
func askedAtReview(t *testing.T, words ...string) (*pgxpool.Pool, *Engine, record.Issue) {
	t.Helper()
	pool := migratedPool(t)
	engine := returnEngine()
	root := stepRoot(t, pool, phase.Admitted)
	owner := claim.Token(treeRoleToken(t, root.Key, claim.RoleArchitect))
	seedStepsPlan(t, pool, root.Key, root.Generation, words...)
	move(t, pool, engine, root.Key, TriggerGateOpened, record.TaskPlace{}, record.PhaseRow{})
	if words[0] == "implementer:owner" {
		if result := applyFacts(t, pool, engine)("step-1", ownersReport(root.Key, owner, claim.RoleImplementer, 1, "implement-LEGION-208")); result.Refusal != nil {
			t.Fatalf("the owner's report of step 1 was refused: %+v", result.Refusal)
		}
	} else {
		move(t, pool, engine, root.Key, TriggerImplementationReady, record.TaskPlace{}, record.PhaseRow{})
	}
	assertWalksAt(t, pool, engine, root.Key, phase.Reviewing, stepPlace(2))
	pr := recordedPR(t, pool, root.Key)
	agent := storedPhaseRow(t, pool, root.Key, claim.RoleImplementer).Claim
	if agent == "" {
		t.Fatalf("%s holds no implementer's phases row to name the agent asked: this fixture proves nothing", root.Key)
	}
	pr.MergeForward = &record.MergeForward{Head: pr.HeadSHA, Base: "main", Phase: phase.Reviewing, Agent: agent, Why: record.ConflictCheckNotGreen, AskedAt: stepClock}
	seedPR(t, pool, pr)
	return pool, engine, issueOf(t, pool, engine, root.Key)
}

// Every end of a merge forward asked of the owner writes the owner's slot back to its own App, so
// no path leaves the slot on the implement App the ask gave it (record.OwnersMergeForward names the
// ends, and the daemon's ActsAs reads them). On `implementer:owner reviewer` at the reviewer's step,
// with the ask open, each end queues one supervise slot_credential of the owner's claim: the merge
// forward's landing, the move back into implementing that clears the ask, the close and the merge of
// the pull request, the owner's stop of the task, the hold a failed reviewer's claim leaves, and the
// issue's leave of the workflow.
//
// The control: on `implementer reviewer` the ask went to the implementer's own claim, so its
// landing and the pull request's close queue no write of the owner's slot.
func TestEveryEndOfAMergeForwardAskedOfTheOwnerGivesItsSlotItsOwnApp(t *testing.T) {
	ends := map[string]func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue){
		"the landing": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			pr := recordedPR(t, pool, issue.Key)
			applyRefusingNothing(t, pool, engine, intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208", Base: "main",
				HeadSHA: "head-forward", Before: pr.HeadSHA, Sender: "legion-implementer[bot]"})
		},
		"a move back into implementing": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			move(t, pool, engine, issue.Key, TriggerReviewRejected, record.TaskPlace{}, record.PhaseRow{})
		},
		"the pull request's close": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			applyRefusingNothing(t, pool, engine, intake.PullRequestClosed{Repo: "sjawhar/legion", Number: 42})
		},
		"the pull request's merge": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			applyRefusingNothing(t, pool, engine, intake.PullRequestMerged{Repo: "sjawhar/legion", Number: 42, MergeSHA: "merge"})
		},
		"the owner's stop of the task": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			if result := applyFacts(t, pool, engine)("stop", ownersStop(t, issue, "a")); result.Refusal != nil {
				t.Fatalf("the owner's stop was refused: %+v", result.Refusal)
			}
		},
		"a hold": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			applyRefusingNothing(t, pool, engine, intake.ClaimFailed{Issue: issue.Key, Role: claim.RoleReviewer})
		},
		"the issue's leave": func(t *testing.T, pool *pgxpool.Pool, engine *Engine, issue record.Issue) {
			applyRefusingNothing(t, pool, engine, intake.DispatchIssue{Key: issue.Key, ResolvedRepo: "sjawhar/legion", Seq: 2, Type: "issue.updated",
				Status: "backlog", Title: "root", Rank: "U"})
		},
	}
	for _, tc := range []struct {
		name   string
		steps  []string
		end    string
		queued int
	}{
		{name: "the landing", steps: []string{"implementer:owner", "reviewer"}, end: "the landing", queued: 1},
		{name: "a move back into implementing", steps: []string{"implementer:owner", "reviewer"}, end: "a move back into implementing", queued: 1},
		{name: "the pull request's close", steps: []string{"implementer:owner", "reviewer"}, end: "the pull request's close", queued: 1},
		{name: "the pull request's merge", steps: []string{"implementer:owner", "reviewer"}, end: "the pull request's merge", queued: 1},
		{name: "the owner's stop of the task", steps: []string{"implementer:owner", "reviewer"}, end: "the owner's stop of the task", queued: 1},
		{name: "a hold", steps: []string{"implementer:owner", "reviewer"}, end: "a hold", queued: 1},
		{name: "the issue's leave", steps: []string{"implementer:owner", "reviewer"}, end: "the issue's leave", queued: 1},
		{name: "control: the landing of an ask of a fresh implementer", steps: []string{"implementer", "reviewer"}, end: "the landing"},
		{name: "control: the close of an ask of a fresh implementer", steps: []string{"implementer", "reviewer"}, end: "the pull request's close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, engine, issue := askedAtReview(t, tc.steps...)
			owner := claim.Token(treeRoleToken(t, issue.Key, claim.RoleArchitect))
			before := superviseRowsOf(t, pool, issue.Key, "slot_credential")
			if pr := recordedPR(t, pool, issue.Key); pr.MergeForward == nil || pr.MergeForward.Landed != "" || pr.State != record.PullRequestOpen ||
				issue.Hold != nil || !record.InWorkflow(issue) {
				t.Fatalf("%s before %s stands in %s with the pull request %+v, want an open ask: this case proves nothing", issue.Key, tc.end, issue.Phase, pr)
			}
			ends[tc.end](t, pool, engine, issue)
			if pr := recordedPR(t, pool, issue.Key); record.OwnersMergeForward(issueOf(t, pool, engine, issue.Key), &pr) {
				t.Fatalf("%s left the merge forward %+v open, want it ended: this case proves nothing", tc.end, pr.MergeForward)
			}
			rows := superviseRowsOf(t, pool, issue.Key, "slot_credential")
			if len(rows) != len(before)+tc.queued {
				t.Fatalf("%s queued the slot_credential rows %+v over %+v, want %d more", tc.end, rows, before, tc.queued)
			}
			for _, row := range rows[len(before):] {
				if row.Claim != owner || row.Role != claim.RoleArchitect || row.Tree != issue.Key || row.Generation != issue.Generation {
					t.Fatalf("the slot_credential row %+v, want one of the owner %s, the architect, in the tree %s at generation %d", row, owner, issue.Key, issue.Generation)
				}
			}
		})
	}
}

// A hold ends a merge forward asked of the owner and writes the owner's slot back to its own App,
// but the pull request still holds the ask, so the hold's retry opens it again
// (record.OwnersMergeForward): the owner's slot acts as the implementer once more, and the owner was
// told of the ask before the hold. On `implementer:owner reviewer` at the reviewer's step, with the
// ask open, the hold a failed reviewer's claim leaves and the architect's retry of it queue one
// merge-forward ask of the owner's claim at the head of the record's ask. That ask's delivery has the
// runtime write the slot for the implement App and tells the owner only once the slot holds it (the
// daemon's ownerSlotActs); without it nothing writes the slot until its next refresh or launch.
//
// The controls: on `implementer reviewer` the ask went to the implementer's own claim, whose slot no
// hold writes, so the retry asks nothing again; and an ask of the owner at that head still queued at
// the retry is the one that writes the slot and tells the owner, so none is queued behind it.
func TestARetriedHoldAsksTheOwnerAgainForTheMergeForwardItsPullRequestHolds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		steps  []string
		queued bool
		asked  int
	}{
		{name: "the owner's ask, told before the hold", steps: []string{"implementer:owner", "reviewer"}, asked: 1},
		{name: "control: the ask of a fresh implementer", steps: []string{"implementer", "reviewer"}},
		{name: "control: the owner's ask still queued at the retry", steps: []string{"implementer:owner", "reviewer"}, queued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, engine, issue := askedAtReview(t, tc.steps...)
			owner := claim.Token(treeRoleToken(t, issue.Key, claim.RoleArchitect))
			pr := recordedPR(t, pool, issue.Key)
			asked := *pr.MergeForward
			want := record.MergeForwardMessage{Role: claim.RoleImplementer, Claim: owner, Step: record.MergeForwardAsk, Head: asked.Head, Base: asked.Base,
				Phase: asked.Phase, PullRequest: pr.Number, Why: asked.Why}
			if tc.queued {
				seedRecord(t, pool, func(tx pgx.Tx) error {
					row, err := record.NewOutboxRow(issue.Key, want, stepClock)
					if err != nil {
						return err
					}
					return record.NewStore().Enqueue(t.Context(), tx, row)
				})
			}
			applyRefusingNothing(t, pool, engine, intake.ClaimFailed{Issue: issue.Key, Role: claim.RoleReviewer})
			if held := issueOf(t, pool, engine, issue.Key); held.Hold == nil {
				t.Fatalf("%s stands in %s after its reviewer's claim failed, want it held: this case proves nothing", issue.Key, held.Phase)
			}
			before := mergeForwardMessages(t, pool)
			applyRefusingNothing(t, pool, engine, intake.RetryOrEscalate{Issue: issue.Key, Decision: intake.RetryDecision, Reason: "try again"})
			retried := issueOf(t, pool, engine, issue.Key)
			if retried.Hold != nil || !record.InWorkflow(retried) {
				t.Fatalf("%s stands in %s after the retry, want it released from its hold: this case proves nothing", issue.Key, retried.Phase)
			}
			reopened := recordedPR(t, pool, issue.Key)
			if owns := record.OwnersMergeForward(retried, &reopened); owns != (tc.steps[0] == "implementer:owner") {
				t.Fatalf("after the retry the owner's merge forward is open: %t, with the pull request's %+v: this case proves nothing", owns, reopened.MergeForward)
			}
			messages := mergeForwardMessages(t, pool)
			added := messages[len(before):]
			if len(added) != tc.asked {
				t.Fatalf("the retry queued the merge-forward messages %+v over %+v, want %d", added, before, tc.asked)
			}
			for _, message := range added {
				if message != want {
					t.Fatalf("the retry queued the merge-forward message %+v, want the owner's ask %+v", message, want)
				}
			}
		})
	}
}
