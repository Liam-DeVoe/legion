package workflow

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
)

// asksToActOnCI are the words of a message that hands its reader a decision on CI: to re-run a
// check, wait for it or fix the code, or to read the checks before deciding. The tester reads no
// CI, so no message the daemon sends the worker of a phase may ask it any of them; the implementer
// learns of a failed check a CI job runs from its own message (tellCIFailure), and the reviewer and
// the architect read CI's state as their prompts say.
var asksToActOnCI = []string{"decide", "re-run", "rerun", "wait for", "fix the code", "Read the checks"}

// A CI settlement tells the architect, the worker of the issue's phase and the implementer what CI
// reported and that nothing moved on it, and asks none of them anything, in every phase a worker
// holds; and the task of a released role that the settlement wakes asks nothing either.
func TestACISettlementAsksNoRoleToActOnIt(t *testing.T) {
	for _, tc := range []struct {
		from   phase.Phase
		status string
		worker claim.Role
	}{
		{phase.Implementing, "in_progress", claim.RoleImplementer},
		{phase.Testing, "testing", claim.RoleTester},
		{phase.Reviewing, "needs_review", claim.RoleReviewer},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			pool := migratedPool(t)
			seedIssue(t, pool, record.Issue{Key: "LEGION-208", Repo: "sjawhar/legion", DefaultBranch: "main", GitHubRepo: "sjawhar/legion", Tree: "LEGION-208", Project: "LEGION", Title: "root",
				Phase: tc.from, Generation: 1, Status: tc.status, Rank: "U"})
			seedPR(t, pool, record.PullRequest{State: record.PullRequestOpen, Issue: "LEGION-208", Repo: "sjawhar/legion",
				Number: 42, Branch: "legion/LEGION-208", HeadSHA: "head"})
			seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: claim.RoleImplementer, Claim: "implement-claim"})
			if tc.worker != claim.RoleImplementer {
				seedPhase(t, pool, record.PhaseRow{Issue: "LEGION-208", Role: tc.worker, Claim: claim.Token(string(tc.worker) + "-claim")})
			}
			applyRefusingNothing(t, pool, mergingEngine(), settledAt("head", 1, "unit"))
			rows := issueOutboxOf(t, pool, "LEGION-208", "role_message")
			if len(rows) < 2 {
				t.Fatalf("the settlement wrote %d role messages, want one to each role it tells", len(rows))
			}
			for _, row := range rows {
				body := string(row.payload)
				if !strings.Contains(body, `\"unit\" failed`) || !strings.Contains(body, "Nothing moved on it") {
					t.Fatalf("role message %s, want what CI reported and that nothing moved", body)
				}
				for _, ask := range asksToActOnCI {
					if strings.Contains(body, ask) {
						t.Errorf("role message %s asks its reader %q", body, ask)
					}
				}
			}
		})
	}
	for _, stop := range []WakeStop{StoppedByRelease, StoppedByPause} {
		task := WakeTask(pods.WakeChecks, "head failed unit", stop)
		if !strings.Contains(task, "head failed unit") {
			t.Fatalf("the wake task of a settlement is %q, want it to name the settlement", task)
		}
		for _, ask := range asksToActOnCI {
			if strings.Contains(task, ask) {
				t.Errorf("the wake task of a settlement %q asks its reader %q", task, ask)
			}
		}
	}
}
