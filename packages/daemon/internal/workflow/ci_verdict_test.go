package workflow

import (
	"maps"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// ciVerdictRow is one state of a check and what the rule of a CI check reads of it.
type ciVerdictRow struct {
	name  string
	state record.CheckState
	held  bool
	want  ciState
}

// ciVerdictRows is every case of the rule of a CI check: each case of a job that has not ended at
// the head waits, a job that passed passes, and a job that ended with any other answer, a word
// Dispatch writes that the daemon does not know among them, fails. A check that no CI job runs
// has no CI verdict, and a check with no state held is not known to be one.
func ciVerdictRows() []ciVerdictRow {
	state := func(answer, reason, ciCase string) record.CheckState {
		return record.CheckState{Check: "k1", State: answer, Reason: reason, CICase: ciCase, Roles: []string{"ci"}}
	}
	rows := []ciVerdictRow{}
	for _, ciCase := range []string{"absent", "not_completed", "not_started", "no_head", "github_unreadable"} {
		rows = append(rows, ciVerdictRow{ciCase + " with no", state("no", "no_result", ciCase), true, ciRunning})
	}
	rows = append(rows,
		ciVerdictRow{"not_completed with yes", state("yes", "", "not_completed"), true, ciRunning},
		ciVerdictRow{"success with yes", state("yes", "", "success"), true, ciPassed},
	)
	for _, ciCase := range []string{"failure", "timed_out", "cancelled", "skipped", "neutral"} {
		rows = append(rows,
			ciVerdictRow{ciCase + " with no", state("no", "fail", ciCase), true, ciFailed},
			ciVerdictRow{ciCase + " with unknown", state("unknown", "pass_and_fail", ciCase), true, ciFailed},
		)
	}
	return append(rows,
		ciVerdictRow{"job_recorded with yes", state("yes", "", "job_recorded"), true, ciPassed},
		ciVerdictRow{"job_recorded with no", state("no", "fail", "job_recorded"), true, ciFailed},
		ciVerdictRow{"an unknown word startup_failure with unknown", state("unknown", "pass_and_fail", "startup_failure"), true, ciFailed},
		ciVerdictRow{"no ci_case: no CI job runs the check", state("no", "fail", ""), true, ciNotCI},
		ciVerdictRow{"no state held", record.CheckState{}, false, ciUnknownKind},
	)
}

// The rule of a check that a CI job runs reads each state the daemon holds, one row a case. The
// control is the same table under a copy of the rule that does not wait on not_completed: its row
// with no then reads ciFailed and its row with yes ciPassed, so the table fails, which the
// subtest asserts.
func TestCIVerdictOfEachCase(t *testing.T) {
	for _, row := range ciVerdictRows() {
		t.Run(row.name, func(t *testing.T) {
			if got := ciVerdict(row.state, row.held); got != row.want {
				t.Fatalf("ciVerdict(%+v, %t) = %d, want %d", row.state, row.held, got, row.want)
			}
		})
	}

	t.Run("the control: a rule that does not wait on not_completed fails the table", func(t *testing.T) {
		waits := slices.DeleteFunc(slices.Clone(ciWaitCases), func(ciCase string) bool { return ciCase == "not_completed" })
		if len(waits) != len(ciWaitCases)-1 {
			t.Fatalf("the wait cases %v hold no not_completed to take out", ciWaitCases)
		}
		missed := map[string]ciState{}
		for _, row := range ciVerdictRows() {
			if got := ciVerdictWaiting(row.state, row.held, waits); got != row.want {
				missed[row.name] = got
			}
		}
		if want := map[string]ciState{"not_completed with no": ciFailed, "not_completed with yes": ciPassed}; !maps.Equal(missed, want) {
			t.Fatalf("the rows the copy misses, with what each reads = %v, want %v", missed, want)
		}
	})
}
