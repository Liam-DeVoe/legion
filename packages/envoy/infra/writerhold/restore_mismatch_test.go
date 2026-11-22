package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// `hold restore` with no --hold after a prepare entry, which the broker refuses 409 hold_mismatch
// (a released row after a prepare entry must name that entry's hold), says so and names --hold,
// with nothing written. The control is restore at 702c7749de, which printed the route's raw error.
func TestHoldRestoreWithNoHoldAfterAPrepareNamesTheHold(t *testing.T) {
	h := newHeldStack(t)
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--reason", "no --hold after a prepare")
	require.Equal(t, 1, code, "restore with no --hold after a prepare entry: %s%s", stdout, stderr)
	require.Contains(t, stderr, "a prepare row")
	require.Contains(t, stderr, "hold restore --hold <its hold id>")
	_, err := h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")

	code, _, stderr = h.run(actionRestore, "--hold", id, "--reason", "the prepared hold named")
	require.Zero(t, code, "restore naming the prepared hold: %s", stderr)
	require.Equal(t, id, h.row().HoldID)
}
