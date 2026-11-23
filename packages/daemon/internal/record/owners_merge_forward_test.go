package record

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// A merge forward is an act as the implementer, so while the record holds one asked of the owner
// that has not ended, the owner's slot acts as the implementer, whatever place the owner works or
// does not work: at the reviewer's step of `implementer:owner reviewer`, where another agent
// reviews; at the owner's acceptance; in merging. A hold ends that, as it ends every act of the
// owner's. With no merge forward asked of the owner the rule is the step's rule, which these
// controls hold: the reviewer's step of another agent acts as nothing, the owner's acceptance as
// acceptance.
func TestTheOwnersSlotActsAsTheImplementerForAMergeForwardAskedOfIt(t *testing.T) {
	t.Parallel()
	reviewing := Issue{Key: "LEGION-320", Phase: phase.Reviewing, PlaceRole: claim.RoleReviewer, StepRole: claim.RoleReviewer}
	accepting := Issue{Key: "LEGION-321", Phase: phase.Accepting, PlaceRole: claim.RoleArchitect, StepRole: claim.RoleAcceptance}
	merging := Issue{Key: "LEGION-322", Phase: phase.Merging, PlaceRole: claim.RoleMerger}
	held := Issue{Key: "LEGION-323", Phase: phase.Held, Hold: &Hold{From: phase.Reviewing, Reason: HoldEscalated}, PlaceRole: claim.RoleReviewer, StepRole: claim.RoleReviewer}
	for _, tc := range []struct {
		name         string
		issue        Issue
		mergeForward bool
		want         claim.Role
		acting       bool
	}{
		{name: "the reviewer's step of another agent", issue: reviewing, mergeForward: true, want: claim.RoleImplementer, acting: true},
		{name: "the owner's acceptance", issue: accepting, mergeForward: true, want: claim.RoleImplementer, acting: true},
		{name: "merging", issue: merging, mergeForward: true, want: claim.RoleImplementer, acting: true},
		{name: "held from reviewing", issue: held, mergeForward: true},
		{name: "control: the reviewer's step of another agent, no merge forward", issue: reviewing},
		{name: "control: the owner's acceptance, no merge forward", issue: accepting, want: claim.RoleAcceptance, acting: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if role, acting := OwnerActsAs(tc.issue, nil, tc.mergeForward); role != tc.want || acting != tc.acting {
				t.Fatalf("OwnerActsAs(%s in %s, merge forward %t) = (%q, %t), want (%q, %t)", tc.issue.Key, tc.issue.Phase, tc.mergeForward, role, acting, tc.want, tc.acting)
			}
		})
	}
}

// The record holds a merge forward asked of the owner that has not ended while the issue is in the
// workflow and held by nothing, its tree does not linger, its pull request is open and holds an ask
// that has not landed, and the implementer's phases row runs a step of the plan under the owner's
// claim, the claim the ask reached (record.WorkerClaim). Each end of the ask answers false: the
// landing, the clear of the ask, the close and the merge of the pull request, a hold, a stopped task,
// the issue's leave and its tree's linger. The control: the implementer's row of a fresh
// implementer, whose ask went to that implementer's own claim.
func TestTheRecordHoldsAMergeForwardAskedOfTheOwnerUntilItEnds(t *testing.T) {
	t.Parallel()
	issue := issueFixture("LEGION-330")
	issue.Phase = phase.Reviewing
	owner, err := OwnerClaim(issue)
	if err != nil {
		t.Fatalf("the owner of %s: %v", issue.Key, err)
	}
	project, err := claim.ProjectToken(issue.Project)
	if err != nil {
		t.Fatalf("the project of %s: %v", issue.Key, err)
	}
	fresh, err := claim.NewToken(project, issue.Key, claim.RoleImplementer)
	if err != nil {
		t.Fatalf("the implementer of %s: %v", issue.Key, err)
	}
	asked := func() *PullRequest {
		return &PullRequest{Issue: issue.Key, State: PullRequestOpen, HeadSHA: "c0de", MergeForward: &MergeForward{Head: "c0de", Base: "main", Phase: phase.Reviewing,
			Why: ConflictCheckNotGreen}}
	}
	ownersRow := []PhaseRow{{Issue: issue.Key, Role: claim.RoleImplementer, Claim: owner, StepTask: "Step 1 of 2: implementer:owner."}}
	freshRow := []PhaseRow{{Issue: issue.Key, Role: claim.RoleImplementer, Claim: fresh, StepTask: "Step 1 of 2: implementer."}}
	landed, cleared, closed, merged := asked(), asked(), asked(), asked()
	landed.MergeForward.Landed = "d00d"
	cleared.MergeForward = nil
	closed.State = PullRequestClosed
	merged.State = PullRequestMerged
	heldIssue, stopped, left := issue, issue, issue
	heldIssue.Phase, heldIssue.Hold = phase.Held, &Hold{From: phase.Reviewing, Reason: HoldEscalated}
	stopped.Phase, stopped.Hold = phase.Held, &Hold{From: phase.Reviewing, Reason: HoldTaskStopped}
	left.Phase, left.Status = phase.Done, "done"
	for _, tc := range []struct {
		name    string
		issue   Issue
		lingers bool
		pr      *PullRequest
		rows    []PhaseRow
		want    bool
	}{
		{name: "asked of the owner, open", issue: issue, pr: asked(), rows: ownersRow, want: true},
		{name: "landed", issue: issue, pr: landed, rows: ownersRow},
		{name: "cleared", issue: issue, pr: cleared, rows: ownersRow},
		{name: "the pull request closed", issue: issue, pr: closed, rows: ownersRow},
		{name: "the pull request merged", issue: issue, pr: merged, rows: ownersRow},
		{name: "no pull request", issue: issue, rows: ownersRow},
		{name: "held", issue: heldIssue, pr: asked(), rows: ownersRow},
		{name: "its task stopped", issue: stopped, pr: asked(), rows: ownersRow},
		{name: "out of the workflow", issue: left, pr: asked(), rows: ownersRow},
		{name: "its tree lingers", issue: issue, lingers: true, pr: asked(), rows: ownersRow},
		{name: "control: asked of a fresh implementer", issue: issue, pr: asked(), rows: freshRow},
		{name: "control: no implementer's row", issue: issue, pr: asked()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := OwnersMergeForward(tc.issue, tc.lingers, tc.pr, tc.rows); got != tc.want {
				t.Fatalf("OwnersMergeForward(%s) = %t, want %t", tc.name, got, tc.want)
			}
		})
	}
}
