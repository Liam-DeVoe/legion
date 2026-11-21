package workflow

import (
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/intake"
	"github.com/sjawhar/legion/daemon/internal/pods"
	"github.com/sjawhar/legion/daemon/internal/record"
	"github.com/sjawhar/legion/daemon/internal/verdict"
)

// The daemon's own legion-verdict is no CI a role reads or waits on (spec R4, "the daemon's own
// write wakes no role"): a checks settlement that holds only that run, or only a commit status of
// that name, wakes no released pod, and one that holds it beside a CI check wakes the pod as that
// check alone would, naming nothing of the daemon's own check. That holds wherever the settlement
// names the check: among its runs, among the checks it says failed or were cancelled with no run
// of it listed, or among its commit statuses.
func TestLegionVerdictAloneInACheckSettlementWakesNoRole(t *testing.T) {
	t.Run("the daemon's own check alone", func(t *testing.T) {
		rig := newWakeRig(t)
		only := settlement("head1", verdict.CheckName)
		only.CheckRuns = []record.AttemptRun{{Name: verdict.CheckName, ID: 9}}
		rig.apply("verdict-only", only)
		if starts := wakeStarts(t, rig.pool); len(starts) != 0 {
			t.Fatalf("supervise start rows = %+v, want no wake for the daemon's own check", starts)
		}
		if resumes := wakeResumes(t, rig.pool); len(resumes) != 0 {
			t.Fatalf("pod resumes = %v, want none", resumes)
		}
	})
	t.Run("beside a CI check", func(t *testing.T) {
		rig := newWakeRig(t)
		both := settlement("head1", verdict.CheckName)
		both.CheckRuns = append(both.CheckRuns, record.AttemptRun{Name: verdict.CheckName, ID: 9})
		rig.apply("verdict-beside-build", both)
		oneWake(t, rig.pool, claim.RoleImplementer, pods.WakeChecks, "head1 passed every check")
		if task := wakeStarts(t, rig.pool)[0].Task; strings.Contains(task, verdict.CheckName) {
			t.Fatalf("the woken role's task = %q, want no %s: the daemon's own check is no CI", task, verdict.CheckName)
		}
	})
	for _, tc := range []struct {
		name  string
		named func(*intake.PullRequestChecks)
	}{
		{"named failed beside a CI check, with no run of it", func(fact *intake.PullRequestChecks) { fact.Failing = []string{verdict.CheckName} }},
		{"named cancelled beside a CI check, with no run of it", func(fact *intake.PullRequestChecks) { fact.Cancelled = []string{verdict.CheckName} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWakeRig(t)
			fact := settlement("head1")
			tc.named(&fact)
			rig.apply("verdict-named-only", fact)
			oneWake(t, rig.pool, claim.RoleImplementer, pods.WakeChecks, "head1 passed every check")
			if task := wakeStarts(t, rig.pool)[0].Task; strings.Contains(task, verdict.CheckName) {
				t.Fatalf("the woken role's task = %q, want no %s: the daemon's own check is no CI", task, verdict.CheckName)
			}
		})
	}
	t.Run("a commit status of that name alone", func(t *testing.T) {
		rig := newWakeRig(t)
		only := settlement("head1")
		only.CheckRuns = nil
		only.Statuses = []record.CommitStatus{{Context: verdict.CheckName, State: record.CommitStatusFailure}}
		rig.apply("verdict-status-only", only)
		if starts := wakeStarts(t, rig.pool); len(starts) != 0 {
			t.Fatalf("supervise start rows = %+v, want no wake for the daemon's own status", starts)
		}
	})
}
