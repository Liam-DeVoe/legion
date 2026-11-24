package workflow

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// sent is what a merge-forward message asks and of whom: its step, the role it is addressed to,
// the claim it reaches, and the step of the message it stands in for (record.MergeForwardMessage).
type sent struct {
	step   record.MergeForwardStep
	role   claim.Role
	claim  claim.Token
	covers record.MergeForwardStep
}

// sentOf is each message as sent, in the order written.
func sentOf(messages []record.MergeForwardMessage) []sent {
	out := make([]sent, len(messages))
	for i, message := range messages {
		out[i] = sent{step: message.Step, role: message.Role, claim: message.Claim, covers: message.Covers}
	}
	return out
}

// sortedSent is sent by claim and then by step: what each claim gets, whatever order the rows of
// two claims were written in.
func sortedSent(messages []sent) []sent {
	sorted := slices.Clone(messages)
	slices.SortFunc(sorted, func(a, b sent) int {
		return cmp.Or(cmp.Compare(a.claim, b.claim), cmp.Compare(a.step, b.step))
	})
	return sorted
}

// standsAtStep is LEGION-208, a tree root, standing in at at step place of the plan whose steps
// are words, with each step's phases row naming the claim agents gives its place (agents[0] for
// step 1, and so on), and its pull request #42 at head-42, whose merge forward was asked in at of
// pusher (record.MergeForward.Agent) and has not landed.
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
	steps := standingSteps(words)
	for i, role := range steps {
		seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: role, Claim: agents[i], StepTask: "a", StepPlace: i + 1, Entry: 1})
	}
	seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion", Number: 42,
		Branch: "legion/LEGION-208", Base: "main", HeadSHA: "head-42", Failing: []string{},
		MergeForward: &record.MergeForward{Head: "head-42", Base: "main", Phase: at, Agent: pusher, Role: steps[0], Why: record.ConflictCheckNotGreen, AskedAt: stepClock}})
	return pool
}

// standingSteps is the role of each step of words.
func standingSteps(words []string) []claim.Role {
	roles := make([]claim.Role, len(words))
	for i, word := range words {
		role, _, _ := strings.Cut(word, ":")
		roles[i] = claim.Role(role)
	}
	return roles
}

// One claim gets one message for each event of a merge forward, the one that asks the most of it,
// and that message carries what the message it stands in for asked (record.MergeForwardCovers).
// The landing of a merge forward in accepting asks a review of the merge where the task has a
// reviewer step, and tells the worker of the step the issue stands at that the merge landed. Each
// pair of those recipients one claim can hold is a case:
//   - the reviewer is the worker of the step the issue stands at, the owner or one keyed agent: it
//     gets the review alone, which covers the landed, so it is still told to go on with its step at
//     the new head;
//   - that claim also pushed the merge: the review alone, which covers nothing, since a landed is
//     never asked of the agent that pushed the merge;
//   - the worker of the step pushed the merge and the reviewer is another agent: the review to the
//     reviewer, and no landed;
//   - the reviewer pushed the merge and the worker is another agent: the review to the reviewer,
//     which a push never skips, and the landed to the worker.
//
// The record says a review was asked (record.MergeForward.ReviewAsked) only where a review row was
// queued. The controls: every recipient another agent, and a task with no reviewer step, which is
// asked no review and records none.
func TestOneClaimGetsTheReviewOfAMergeThatLandedPastReviewAndItCoversTheLanded(t *testing.T) {
	owner := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	implementer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleImplementer))
	reviewer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleReviewer))
	acceptance := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleAcceptance))
	// keyed is the one agent of the key x, named for its first step's role (record.NameAgents).
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
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: owner, covers: record.MergeForwardLanded}}},
		{name: "one keyed agent reviews and accepts", words: []string{"implementer", "reviewer:x", "acceptance:x"},
			agents: []claim.Token{implementer, keyed, keyed}, pusher: implementer, review: true,
			want: []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: keyed, covers: record.MergeForwardLanded}}},
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
			if got := sentOf(mergeForwardMessages(t, pool)); !reflect.DeepEqual(sortedSent(got), sortedSent(tc.want)) {
				t.Fatalf("the landing sent %+v, want %+v", got, tc.want)
			}
			landed := recordedPR(t, pool, "LEGION-208").MergeForward
			if landed == nil || landed.Landed != "head-forward" || landed.ReviewAsked != tc.review {
				t.Fatalf("the record holds the merge forward %+v, want it landed at head-forward with a review asked: %t", landed, tc.review)
			}
		})
	}
}

// The agent asked to merge that also works the step the issue stands at gets the ask alone, which
// covers the hold: on `implementer:owner reviewer:owner` in reviewing, the owner is asked, and the
// ask carries that its step waits for the merge and goes on at the head the merge leaves. It is
// asked once: mergeForward queues the ask before it writes the record, whose write opens the
// owner's merge forward and would ask the owner again (askOwnerAgain) but for the ask already
// queued. The control: on `implementer reviewer:owner` the fresh implementer is asked, and the
// owner held.
func TestTheAgentAskedToMergeThatWorksTheStepGetsTheAskAndItCoversTheHold(t *testing.T) {
	owner := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	implementer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleImplementer))
	for _, tc := range []struct {
		name   string
		words  []string
		agents []claim.Token
		want   []sent
	}{
		{name: "the owner implements and reviews", words: []string{"implementer:owner", "reviewer:owner"}, agents: []claim.Token{owner, owner},
			want: []sent{{step: record.MergeForwardAsk, role: claim.RoleImplementer, claim: owner, covers: record.MergeForwardHold}}},
		{name: "control: a fresh implementer", words: []string{"implementer", "reviewer:owner"}, agents: []claim.Token{implementer, owner},
			want: []sent{{step: record.MergeForwardAsk, role: claim.RoleImplementer, claim: implementer},
				{step: record.MergeForwardHold, role: claim.RoleReviewer, claim: owner}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := standsAtStep(t, phase.Reviewing, 2, tc.words, tc.agents, "")
			seedRecord(t, pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), "update pull_requests set merge_forward = null where issue = 'LEGION-208'")
				return err
			})
			engine := returnEngine()
			onStored(t, pool, engine, "LEGION-208", func(ctx context.Context, tx pgx.Tx, issue record.Issue) error {
				stored, err := engine.store.PullRequest(ctx, tx, issue.Key)
				if err != nil {
					return err
				}
				return engine.mergeForward(ctx, tx, issue, stored, record.ConflictCheckNotGreen)
			})
			if got := sentOf(mergeForwardMessages(t, pool)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("the merge forward sent %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A covering message stands in for the one it covers whichever of the two its sender addresses
// first (addressMergeForward): a review addressed after a landed to the same claim replaces that
// landed, in its place, and carries it in Covers, as a review addressed before the landed does. The
// callers address the covering message first today, so only this test drives the other order. The
// control is a landed to another claim, which is sent whole, in the order given, beside the review.
func TestACoveringMessageStandsInForTheOneItCoversInEitherOrder(t *testing.T) {
	owner := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleArchitect))
	reviewer := claim.Token(treeRoleToken(t, "LEGION-208", claim.RoleReviewer))
	landed := func(to claim.Token) mergeForwardSend {
		return mergeForwardSend{step: record.MergeForwardLanded, to: roleRecipient{issue: "LEGION-208", role: claim.RoleAcceptance, row: "LEGION-208", claim: to}}
	}
	review := mergeForwardSend{step: record.MergeForwardReview, to: roleRecipient{issue: "LEGION-208", role: claim.RoleReviewer, row: "LEGION-208", claim: owner}}
	covering := []sent{{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: owner, covers: record.MergeForwardLanded}}
	for _, tc := range []struct {
		name  string
		sends []mergeForwardSend
		want  []sent
	}{
		{name: "the review first", sends: []mergeForwardSend{review, landed(owner)}, want: covering},
		{name: "the landed first", sends: []mergeForwardSend{landed(owner), review}, want: covering},
		{name: "control: the landed to another claim", sends: []mergeForwardSend{landed(reviewer), review},
			want: []sent{{step: record.MergeForwardLanded, role: claim.RoleAcceptance, claim: reviewer},
				{step: record.MergeForwardReview, role: claim.RoleReviewer, claim: owner}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := migratedPool(t)
			engine := returnEngine()
			var got []record.MergeForwardMessage
			applyInTx(t, pool, engine, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				got, err = engine.addressMergeForward(ctx, tx, "LEGION", tc.sends, nil)
				return err
			})
			if !reflect.DeepEqual(sentOf(got), tc.want) {
				t.Fatalf("the event sent %+v, want %+v", sentOf(got), tc.want)
			}
		})
	}
}
