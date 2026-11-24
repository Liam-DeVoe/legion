package classify

import (
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// While a fix of the task runs beside its review, an approval that stands on the head GitHub
// reports `blocked` waits on nothing: the check that blocks it is the fix's to make pass, and the
// task's end waits for every check a CI job runs. So the round does not wait on the merge state, and
// the state served for it shows no wait. Every other state GitHub would not merge from keeps the
// wait, since the fix does not cause it, and with no fix `blocked` waits as before.
func TestARunningFixEndsTheWaitOfABlockedApproval(t *testing.T) {
	reviewer := record.PhaseRow{Decision: &record.ReviewDecision{State: "approved", Head: "x"}}
	for _, tc := range []struct {
		state record.MergeState
		fix   int
		waits bool
	}{
		{record.MergeStateBlocked, 1, false},
		{record.MergeStateBlocked, 0, true},
		{record.MergeStateBehind, 1, true},
		{record.MergeStateDirty, 1, true},
		{record.MergeStateDraft, 1, true},
		{record.MergeStateUnknown, 1, true},
		{record.MergeStateClean, 1, false},
	} {
		issue := record.Issue{Phase: phase.Reviewing, RunningFix: tc.fix}
		pr := &record.PullRequest{State: record.PullRequestOpen, HeadSHA: "x", MergeState: tc.state}
		if got := RoundWaitsOnMergeState(issue, reviewer, pr); got != tc.waits {
			t.Errorf("an approval at merge state %q with running fix %d waits = %v, want %v", tc.state, tc.fix, got, tc.waits)
		}
	}
}
