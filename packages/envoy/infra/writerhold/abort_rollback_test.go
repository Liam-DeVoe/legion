package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A hold row rolled back to prepare under a set hold is not aborted from prepare (#4679's review
// round 3, P2-1): with `hold set` done and the row held, a database writer puts the saved signed
// prepare row back (its gen below held's) and deletes held's log row, so the row reads prepare. The
// abort reads the hold's journal before its prepare path, finds hold set's done entry, and refuses,
// naming the step; it writes nothing: no cohort deleted, no row moved, no journal entry. The control
// is the abort as it stood, whose prepare path deleted the cohort and moved the row to released,
// admitting every client while the hold's journal said it was set. That a writer is refused after
// the rollback rests on the routes' floor (#4679's signedrecord.Floor), which refuses the rolled-back
// row: under today's routes the row itself reads prepare, and prepare admits writers.
func TestHoldAbortRefusesARowRolledBackToPrepare(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	prepared := h.rowText()
	h.s.HoldWithoutTheSteps(t, id)
	held := h.row()
	require.Equal(t, natsgrant.OperatorPhaseHeld, held.Phase)

	h.putRowBack(prepared)
	_, err := h.s.Broker.Pool.Exec(ctx, `delete from nats_hold_log where gen = $1`, held.Gen)
	require.NoError(t, err, "the test's database writer deletes held's log row")
	rolled := h.row()
	require.Equal(t, natsgrant.OperatorPhasePrepare, rolled.Phase, "the row reads the rolled-back prepare")
	before, _, err := h.s.Records.Journal(ctx, id)
	require.NoError(t, err)
	text := h.rowText()

	code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "the row reads prepare")
	require.Equal(t, 1, code, "an abort of a set hold whose row reads prepare")
	require.Contains(t, stderr, holdrecord.CommandSet+" is done")
	require.Equal(t, text, h.rowText(), "the refused abort moved the row")
	after, _, err := h.s.Records.Journal(ctx, id)
	require.NoError(t, err)
	require.Equal(t, before.Ops, after.Ops, "the refused abort wrote a journal entry")
}
