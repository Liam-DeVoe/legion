package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A prepared hold whose journal was never written has a way out: startHold moves the row to prepare,
// then inserts the journal, and when the second write does not land its error names `hold abort
// --hold <id>`. The abort finds no journal for the hold, which means no `hold set` entry, so it takes
// the prepare path: it deletes any cohort record and moves the row back to released under the hold
// id, and a new hold can be prepared. The control is the abort as it stood: it read the journal
// first and exited 1 ("has no done record"), the row stayed prepare, and every later prepare was
// refused.
func TestHoldAbortOfAPreparedHoldWithNoJournal(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := newHoldID()
	_, err := h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhasePrepare, id, "startHold's move; its journal insert did not land")
	require.NoError(t, err)
	_, _, err = h.s.Records.Journal(ctx, id)
	require.Error(t, err, "the hold has a journal")

	code, stdout, stderr := h.run(actionAbort, "--hold", id, "--reason", "the way out startHold names")
	require.Zero(t, code, "abort of a prepared hold with no journal: %s", stderr)
	require.Contains(t, stdout, "aborted hold "+id+" from prepare")
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseReleased, row.Phase)
	require.Equal(t, id, row.HoldID)
	h.prepared(holdrecord.WorkflowBucketsCreate)
}
