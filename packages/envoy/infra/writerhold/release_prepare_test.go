package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// `hold release` on a row restored released from prepare, under the hold's id with a journal that
// has not ended, is refused naming `hold abort --hold <id>`, the command that ends that hold: no
// release entry is written and the row stays released. `hold abort` then ends it. The control is
// release at 702c7749de: it printed "already released" and exited 0, and the journal never ended.
func TestHoldReleaseOfARowRestoredFromPrepareNamesAbort(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.PutRecord(ctx, id, "cohort", "cohort", 0, []byte(`{"boundary":1}`))
	require.NoError(t, err)
	h.deleteRow()
	code, _, stderr := h.run(actionRestore, "--hold", id, "--reason", "the row is gone in prepare")
	require.Zero(t, code, "the restore: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)

	code, stdout, stderr := h.run(actionRelease, "--hold", id, "--reason", "release the restored row")
	require.Equal(t, 1, code, "release of a row restored from prepare: %s%s", stdout, stderr)
	require.Contains(t, stderr, "hold abort --hold "+id)
	require.NotContains(t, stdout, "already released")
	require.NotContains(t, h.journalCommands(id), holdrecord.CommandRelease+" intent")
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)

	h.requireAbortEndsTheReleasedHold(id)
}
