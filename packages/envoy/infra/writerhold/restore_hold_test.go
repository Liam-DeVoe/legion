package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// `hold restore --hold <id>` of an id with neither a journal nor a cohort record, after a released
// log entry, which the broker would take under any id, is refused before anything is written: no
// hold of that id left anything to end, so its id on the row would be a typo's, and the operator
// would be told to end a hold that is not there. `hold restore` with no --hold then restores the
// row. The control is restore at 702c7749de: it wrote the bogus id into the row and printed "hold
// <id> was lost in prepare: its cohort record is still in the broker's database".
func TestHoldRestoreOfAnIdNoHoldLeftIsRefused(t *testing.T) {
	h := newHeldStack(t)
	bogus := newHoldID()
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--hold", bogus, "--reason", "a mistyped hold id")
	require.Equal(t, 1, code, "restore of an id no hold left: %s%s", stdout, stderr)
	require.Contains(t, stderr, "no journal and no cohort record")
	require.Contains(t, stderr, "with no --hold")
	require.NotContains(t, stdout, "cohort record is still")
	_, err := h.s.Records.Row(context.Background())
	require.Error(t, err, "the refused restore wrote a row")

	code, _, stderr = h.run(actionRestore, "--reason", "the row with no hold id")
	require.Zero(t, code, "restore with no --hold: %s", stderr)
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseReleased, row.Phase)
	require.Empty(t, row.HoldID)
}

// After an abort from prepare, which deleted the hold's cohort record and left its journal open, a
// row lost and restored with --hold comes back released with the hold's id, and the output says
// what is true: the cohort record is gone, and the journal is still open, which `hold abort` ends.
// The control is restore at 702c7749de, which said the cohort record "is still in the broker's
// database".
func TestHoldRestoreAfterAnAbortFromPrepareSaysTheCohortIsGone(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.PutRecord(ctx, id, "cohort", "cohort", 0, []byte(`{"boundary":1}`))
	require.NoError(t, err)
	code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "abort from prepare")
	require.Zero(t, code, "the abort from prepare: %s", stderr)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--hold", id, "--reason", "the aborted hold's row is gone")
	require.Zero(t, code, "the restore: %s", stderr)
	require.NotContains(t, stdout, "cohort record is still")
	require.Contains(t, stdout, "no cohort record")
	require.Contains(t, stdout, "hold abort --hold "+id)
	h.requireAbortEndsTheReleasedHold(id)
}
