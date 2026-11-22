package writerhold

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// The minter script writes each allowance its run was given (--service-agent, --rbac-only-member,
// --fleet none) under the report's `allowances` ({flag, value}), since each lets a principal or a
// reading pass. The preflight reads them: `hold set` prints each one to the operator and records
// each in its done entry (minters_allowance.<i>, "<flag> <value>"), and refuses on none, since an
// allowance is the operator's deliberate choice. The control is the reader as it stood at
// 3ed1552d15: it decoded the report strictly with no `allowances` field, so the report was UNKNOWN
// ("unknown field \"allowances\"") and the set refused, printing and recording nothing.
func TestHoldTheMinterReportsAllowancesArePrintedAndRecorded(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	const agent = "service-12345@gcp-sa-cloudbuild.iam.gserviceaccount.com"
	report, err := json.Marshal(map[string]any{
		"run_at": time.Now().UTC().Format(time.RFC3339), "findings": []any{}, "unknown": []any{},
		"allowances": []map[string]string{{"flag": "--service-agent", "value": agent}, {"flag": "--fleet", "value": "none"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.report, report, 0o600))

	code, stdout, stderr := h.run(actionSet, "--workflow", string(holdrecord.WorkflowBucketsCreate), "--reason", "a minter run given two allowances")
	require.Zero(t, code, "hold set with a report of two allowances: %s", stderr)
	require.Contains(t, stdout, "the minter report's run was given --service-agent "+agent)
	require.Contains(t, stdout, "the minter report's run was given --fleet none")
	done, ok := h.journal(h.row().HoldID).Latest(holdrecord.CommandSet, holdrecord.StateDone)
	require.True(t, ok, "hold set wrote no done entry")
	require.Equal(t, "--service-agent "+agent, done.Outputs["minters_allowance.0"])
	require.Equal(t, "--fleet none", done.Outputs["minters_allowance.1"])
}

// A held command given --hold of a hold `hold prepare` made sets that hold itself (RunHeld), and
// that set checks the minter report as `hold set` does, so its done entry records each allowance
// too: every way a hold is set records them. The control is RunHeld at 75031d8b8a, which ran no
// preflight for a prepared hold and so recorded no minters_allowance.<i>.
func TestHoldAHeldCommandSettingAPreparedHoldRecordsTheAllowances(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	report, err := json.Marshal(map[string]any{
		"run_at": time.Now().UTC().Format(time.RFC3339), "findings": []any{}, "unknown": []any{},
		"allowances": []map[string]string{{"flag": "--fleet", "value": "none"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.report, report, 0o600))

	code, stdout, stderr := h.run(actionPrepare, "--workflow", string(holdrecord.WorkflowStreamsUpdate), "--reason", "prepared for streams update")
	require.Zero(t, code, "hold prepare: %s", stderr)
	require.Contains(t, stdout, "the minter report's run was given --fleet none")
	id := h.row().HoldID
	h.awaitStreamLeaders()

	code, stdout, stderr = h.streamsRun(nil, "update", "--hold", id, "--reason", "set by the held command")
	require.Zero(t, code, "streams update --hold of the prepared hold: %s", stderr)
	require.Contains(t, stdout, "the minter report's run was given --fleet none")
	done, ok := h.journal(id).Latest(holdrecord.CommandSet, holdrecord.StateDone)
	require.True(t, ok, "the held command's set wrote no done entry")
	require.Equal(t, "--fleet none", done.Outputs["minters_allowance.0"])
}

// An allowance the preflight cannot read whole, or a report with no `allowances` list, is UNKNOWN,
// as a finding or an unknown step it cannot read is: the hold never runs on a report whose
// allowances it did not see.
func TestHoldAMinterAllowanceThatDoesNotReadIsUnknown(t *testing.T) {
	fresh := time.Now().UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name, what string
		report     map[string]any
	}{
		{"no allowances list", "lacks its findings, its unknown list or its allowances",
			map[string]any{"run_at": fresh, "findings": []any{}, "unknown": []any{}}},
		{"an allowance with no value", "allowances[1] lacks its value",
			map[string]any{"run_at": fresh, "findings": []any{}, "unknown": []any{},
				"allowances": []map[string]string{{"flag": "--fleet", "value": "none"}, {"flag": "--service-agent"}}}},
		{"an allowance with a field the hold does not read", "decode the minter report",
			map[string]any{"run_at": fresh, "findings": []any{}, "unknown": []any{},
				"allowances": []map[string]string{{"flag": "--fleet", "value": "none", "why": "x"}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, _, err := readMinterReport(minterReportFile(t, c.report))
			requireUnknown(t, err, StepPreflight, c.what)
		})
	}
}
