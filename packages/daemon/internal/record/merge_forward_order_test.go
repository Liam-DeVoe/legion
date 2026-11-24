package record

import (
	"maps"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
)

// Every step of a merge forward has its place in the one order of a merge forward's messages to
// one agent (mergeForwardOrder), once, so a step added later is placed before any sender addresses
// it; and each cover names a step of the same event that comes after it, so the message one claim
// keeps is the one that asks the more of it. The list of steps is mergeForwardRoles', which the
// validation of a row reads.
func TestEveryMergeForwardStepHasItsPlaceInTheOrderOfOneAgentsMessages(t *testing.T) {
	t.Parallel()
	steps := slices.Sorted(maps.Keys(mergeForwardRoles))
	placed := slices.Sorted(slices.Values(mergeForwardOrder))
	if !slices.Equal(steps, placed) {
		t.Fatalf("the order of one agent's merge-forward messages places %v, want each step of %v once", mergeForwardOrder, steps)
	}
	for step, covered := range mergeForwardCovered {
		if MergeForwardOrder(step) < 0 || MergeForwardOrder(covered) <= MergeForwardOrder(step) {
			t.Fatalf("%s covers %s, want a step placed after it (%v)", step, covered, mergeForwardOrder)
		}
		if !MergeForwardCovers(step, covered) || MergeForwardCovers(covered, step) {
			t.Fatalf("MergeForwardCovers(%s, %s) is not the one way the cover goes", step, covered)
		}
	}
}

// A message that covers another is the one message its claim gets for the event, so it carries
// what the one it covers asked: the review that covers the landed tells the reviewer, which also
// works the step the issue stands at, to go on with that step at the new head after its review, the
// one thing only the landed said; and the ask that covers the hold tells the agent asked, which also
// works that step, that the step waits for the merge. So each covering message's text is another
// than the same message's with no cover, and its cover is the one field that makes it so. The words
// are not compared: they are the agent's to read.
func TestACoveringMergeForwardMessageSaysWhatItCovers(t *testing.T) {
	t.Parallel()
	pr := PullRequest{Repo: "sjawhar/legion", MergeForward: &MergeForward{Head: "c0de", Base: "main", Phase: phase.Accepting, Landed: "d00d"}}
	for _, message := range []MergeForwardMessage{
		{Role: claim.RoleReviewer, Step: MergeForwardReview, Head: "c0de", Landed: "d00d", Base: "main", Phase: phase.Accepting, PullRequest: 42, Covers: MergeForwardLanded},
		{Role: claim.RoleImplementer, Step: MergeForwardAsk, Head: "c0de", Base: "main", Phase: phase.Reviewing, PullRequest: 42, Why: ConflictCheckNotGreen, Covers: MergeForwardHold},
	} {
		bare := message
		bare.Covers = ""
		if message.Message("LEGION-208", pr) == bare.Message("LEGION-208", pr) {
			t.Fatalf("the %s that covers a %s sends the text of the %s that covers nothing, want it to say what it covers", message.Step, message.Covers, message.Step)
		}
	}
}
