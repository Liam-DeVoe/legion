package workflow

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// This is the red form of the round's test at the pull request's head: the same cases with no
// column for what a message stands in for (record.MergeForwardMessage.Covers), which the head
// does not have.

// sent is what a merge-forward message asks and of whom: its step, the role it is addressed to,
// and the claim it reaches.
type sent struct {
	step  record.MergeForwardStep
	role  claim.Role
	claim claim.Token
}

// sentOf is each message as sent, in the order written.
func sentOf(messages []record.MergeForwardMessage) []sent {
	out := make([]sent, len(messages))
	for i, message := range messages {
		out[i] = sent{step: message.Step, role: message.Role, claim: message.Claim}
	}
	return out
}

// standsAtStep is LEGION-208, a tree root, standing in at at step place of the plan whose steps
// are words, with each step's phases row naming the claim agents gives its place, and its pull
// request #42 at head-42, whose merge forward was asked in at of pusher and has not landed.
func standsAtStep(t *testing.T, at phase.Phase, place int, words []string, agents []claim.Token, pusher claim.Token) *pgxpool.Pool {
	t.Helper()
	pool := migratedPool(t)
	seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion",
		Tree: "LEGION-208", Project: "LEGION", Title: "root", Phase: at, Generation: 4, Status: "in_progress", Rank: "U", LastDispatchSeq: 1})
	seedSlot(t, pool, record.Slot{Issue: "LEGION-208", Index: 0})
	seedStepsPlan(t, pool, "LEGION-208", 4, words...)
	seedRecord(t, pool, func(tx pgx.Tx) error {
		return record.NewStore().MoveTask(t.Context(), tx, "LEGION-208", 4, "a", record.TaskPlace{Kind: record.PlaceStep, Position: place}, record.TaskRunning)
	})
	for i, word := range words {
		role, _, _ := strings.Cut(word, ":")
		seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.Role(role), Claim: agents[i], StepTask: "a", StepPlace: i + 1, Entry: 1})
	}
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", HeadSHA: "head-42", Failing: []string{},
		MergeForward: &record.MergeForward{Head: "head-42", Base: "main", Phase: at, Agent: pusher, Why: record.ConflictCheckNotGreen, AskedAt: stepClock}})
	return pool
}

func TestOneClaimGetsTheReviewOfAMergeThatLandedPastReviewAndItCoversTheLanded(t *testing.T) {
	owner := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	implementer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleImplementer))
	reviewer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleReviewer))
	acceptance := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleAcceptance))
	keyed := agentToken(t, "reviewer")
	for _, tc := range []struct {
		name   string
		words  []string
		agents []claim.Token
		pusher claim.Token
		want   []sent
		review bool
	}{
		{name: "the owner reviews and accepts", words: []string{"implementer", "reviewer:owner", "acceptance:owner"},
			agents: []claim.Token{implementer, owner, owner}, pusher: implementer, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: owner}}},
		{name: "one keyed agent reviews and accepts", words: []string{"implementer", "reviewer:x", "acceptance:x"},
			agents: []claim.Token{implementer, keyed, keyed}, pusher: implementer, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: keyed}}},
		{name: "the owner implements, reviews and accepts", words: []string{"implementer:owner", "reviewer:owner", "acceptance:owner"},
			agents: []claim.Token{owner, owner, owner}, pusher: owner, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: owner}}},
		{name: "the owner implements and accepts", words: []string{"implementer:owner", "reviewer", "acceptance:owner"},
			agents: []claim.Token{owner, reviewer, owner}, pusher: owner, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: reviewer}}},
		{name: "the owner implements and reviews", words: []string{"implementer:owner", "reviewer:owner", "acceptance"},
			agents: []claim.Token{owner, owner, acceptance}, pusher: owner, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: owner},
				{step: record.MergeForwardLanded, role: claim.RoleAcceptance, claim: acceptance}}},
		{name: "control: every recipient another agent", words: []string{"implementer", "reviewer", "acceptance"},
			agents: []claim.Token{implementer, reviewer, acceptance}, pusher: implementer, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: reviewer},
				{step: record.MergeForwardLanded, role: claim.RoleAcceptance, claim: acceptance}}},
		{name: "control: no reviewer step", words: []string{"implementer", "acceptance:owner"},
			agents: []claim.Token{implementer, owner}, pusher: implementer,
			want: []sent{{step: record.MergeForwardLanded, role: claim.RoleAcceptance, claim: owner}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := standsAtStep(t, phase.Accepting, len(tc.words), tc.words, tc.agents, tc.pusher)
			engine := returnEngine()
			applyRefusingNothing(t, pool, engine, intake.PullRequestSynchronized{Repo: "sjawhar/legion", Number: 42, Branch: "legion/LEGION-208",
				Base: "main", HeadSHA: "head-forward", Before: "head-42", Sender: "legion-implementer[bot]"})
			if got := sentOf(mergeForwardMessages(t, pool)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("the landing sent %+v, want %+v", got, tc.want)
			}
			landed := recordedPR(t, pool, "LEGION-208").MergeForward
			if landed == nil || landed.Landed != "head-forward" || landed.ReviewAsked != tc.review {
				t.Errorf("the record holds the merge forward %+v, want it landed at head-forward with a review asked: %t", landed, tc.review)
			}
		})
	}
}
