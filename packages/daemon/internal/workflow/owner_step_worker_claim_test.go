package workflow

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// The owner does a step of its own plan whatever the step's phases row names: its owner-step and
// its report are the owner's (record.OwnerClaim). The row can name the architect above, which the
// start writes there while the issue's own architect holds no role (ownerAgent). A message to the
// worker of that step reaches the owner too. LEGION-209, a child at the reviewer's step of
// `implementer reviewer:owner`, whose reviewer row names LEGION-208's architect: the merge forward
// holds LEGION-209's own architect, and the landing tells it.
//
// The control: the same child whose reviewer row names its own architect, as a start writes it
// while that architect holds its role.
func TestAMessageToTheOwnersStepReachesTheOwnerWhateverItsRowNames(t *testing.T) {
	childOwner := claim.Token(treeRoleToken(t, "LEGION-209", claim.RoleArchitect))
	above := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	implementer := claim.Token(treeRoleToken(t, "LEGION-209", claim.RoleImplementer))
	for _, tc := range []struct {
		name string
		row  claim.Token
	}{
		{name: "the row names the architect above", row: above},
		{name: "control: the row names the owner", row: childOwner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
				Tree: "LEGION-208", Project: "LEGION", Title: "root", Phase: phase.Implementing, Generation: 4, Status: "in_progress", Rank: "U",
				LastDispatchSeq: 1})
			seedSlot(t, pool, record.Slot{Issue: "LEGION-208", Index: 0})
			seedIssue(t, pool, record.Issue{Key: "LEGION-209", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
				Tree: "LEGION-208", Project: "LEGION", Title: "LEGION-209", Parent: new("LEGION-208"), Phase: phase.Reviewing, Generation: 4,
				Status: "in_progress", Rank: "V", LastDispatchSeq: 1})
			seedStepsPlan(t, pool, "LEGION-209", 4, "implementer", "reviewer:owner")
			seedRecord(t, pool, func(tx pgx.Tx) error {
				return record.NewStore().MoveTask(t.Context(), tx, "LEGION-209", 4, "a", record.TaskPlace{Kind: record.PlaceStep, Position: 2}, record.TaskRunning)
			})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleImplementer, Claim: implementer, StepTask: "a", StepPlace: 1, Entry: 1})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-209", Role: claim.RoleReviewer, Claim: tc.row, StepTask: "a", StepPlace: 2, Entry: 1})
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-209", Repo: "sjawhar/legion", Number: 43,
				Branch: "legion/LEGION-209", Base: "main", HeadSHA: "head-43", Failing: []string{}})
			engine := returnEngine()
			onStored(t, pool, engine, "LEGION-209", func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
				stored, err := engine.store.PullRequest(ctx, tx, issue.Key)
				if err != nil {
					return err
				}
				return engine.mergeForward(ctx, tx, issue, stored, record.ConflictCheckNotGreen)
			})
			applyRefusingNothing(t, pool, engine, intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 43, Branch: "legion/LEGION-209",
				Base: "main", HeadSHA: "head-forward", Before: "head-43", Sender: "legion-implementer[bot]"})
			var held, landed []claim.Token
			for _, message := range mergeForwardMessages(t, pool) {
				switch message.Step {
				case record.MergeForwardHold:
					held = append(held, message.Claim)
				case record.MergeForwardLanded:
					landed = append(landed, message.Claim)
				}
			}
			if len(held) != 1 || held[0] != childOwner || len(landed) != 1 || landed[0] != childOwner {
				t.Fatalf("the hold reached %v and the landed %v, want each to reach the owner %s alone", held, landed, childOwner)
			}
		})
	}
}
