package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// requireAbortEndsTheReleasedHold runs `hold abort` on hold id, whose row was restored released
// with its hold id while its journal shows prepare, and requires it to end the hold: the abort's
// done entry written, the hold's cohort record deleted, the row still released, and every later
// command refused, since the hold has ended.
func (h *heldStack) requireAbortEndsTheReleasedHold(id string) {
	h.t.Helper()
	ctx := context.Background()
	code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "end the hold whose row came back released")
	require.Zero(h.t, code, "hold abort of the hold restored released: %s", stderr)
	require.Equal(h.t, stateDone, latestState(h.journal(id), holdrecord.CommandAbort), "the abort wrote no done entry")
	cohort, err := h.s.Records.Records(ctx, id, "cohort")
	require.NoError(h.t, err)
	require.Empty(h.t, cohort, "the abort left the hold's cohort record")
	require.Equal(h.t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	code, _, stderr = h.run(actionSet, "--hold", id, "--reason", "a set after the end")
	require.NotZero(h.t, code, "a set was admitted after the abort ended the hold")
	require.Contains(h.t, stderr, id)
}

// `hold restore --hold` sends released first and held only when the route refuses released with
// bad_transition: the route's signed row log decides, not the journal's intent. A row lost while
// the hold was prepare, after `hold set`'s intent and before its move, comes back released with
// the hold's id, since the log's latest entry is the prepare row, and clients are admitted. `hold
// abort` then ends the hold: on a released row whose journal shows prepare the table admits it
// (holdrecord's Request.RowReleased), it deletes the cohort record, and its done entry ends the
// journal. The control is the restore by the journal (9a28b89c60): a hold set entry sent held, so
// the row came back held, and hold set's rerun could not move it from prepare.
func TestHoldARowLostAfterSetsIntentInPrepareIsRestoredReleased(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.PutRecord(ctx, id, "cohort", "cohort", 0, []byte(`{"boundary":1}`))
	require.NoError(t, err)
	_, err = h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, nil)
	require.NoError(t, err, "hold set's intent, with the row prepare")
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--hold", id, "--reason", "lost after set's intent, before its move")
	require.Zero(t, code, "the restore of a row lost in prepare after set's intent: %s", stderr)
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseReleased, row.Phase, "the route's log holds no held row of the hold")
	require.Equal(t, id, row.HoldID)
	require.Contains(t, stdout, "hold abort --hold "+id)
	require.True(t, h.listenerAdmitted(), "a writer was refused under the restored released row")
	h.requireAbortEndsTheReleasedHold(id)
}

// A hold that `hold abort` ended from prepare, after `hold set`'s intent, leaves no abort entry in
// its journal, and its row released with its hold id. Its row lost then comes back released, which
// the route stores after the abort's released entry, and clients are admitted. The control is the
// restore by the journal (9a28b89c60): the set entry sent held, which the route refuses after a
// released entry (bad_transition), so no row came back and every client stayed refused.
func TestHoldAHoldAbortedFromPrepareIsRestoredReleased(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, nil)
	require.NoError(t, err, "hold set's intent, with the row prepare")
	code, stdout, stderr := h.run(actionAbort, "--hold", id, "--reason", "abort from prepare after set's intent")
	require.Zero(t, code, "the abort from prepare: %s", stderr)
	require.Contains(t, stdout, "from prepare")
	h.deleteRow()

	code, _, stderr = h.run(actionRestore, "--hold", id, "--reason", "the aborted hold's row is gone")
	require.Zero(t, code, "the restore of a hold aborted from prepare: %s", stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	require.True(t, h.listenerAdmitted(), "a writer was refused under the restored released row")
}
