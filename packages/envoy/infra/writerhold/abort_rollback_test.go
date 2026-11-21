package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A hold row rolled back to prepare under a set hold is not aborted from prepare, and writers stay
// held (#4679's review round 3, P2-1): with `hold set` done and the row held, a listener is refused
// held, so the auth service's view has read held's gen; then a database writer puts the saved
// signed prepare row back (its gen below held's) and deletes held's log row, so the row reads
// prepare. Writers stay held: the view's floor refuses the older row (hold_unknown), and a listener
// is refused. The abort is refused and writes nothing, twice: through the routes that served held,
// whose row floor refuses the older row, and through routes restarted with no floor, where the
// abort reads the hold's journal before its prepare path, finds hold set's done entry and refuses,
// naming the step. The control is the abort as it stood, whose prepare path deleted the cohort and
// moved the row to released, admitting every client while the journal said the hold was set (red at
// 36e8cf250e: exit 0).
func TestHoldAbortRefusesARowRolledBackToPrepare(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	prepared := h.rowText()
	h.s.HoldWithoutTheSteps(t, id)
	held := h.row()
	require.Equal(t, natsgrant.OperatorPhaseHeld, held.Phase)
	require.False(t, h.listenerAdmitted(), "a listener was admitted under held")

	h.putRowBack(prepared)
	_, err := h.s.Broker.Pool.Exec(ctx, `delete from nats_hold_log where gen = $1`, held.Gen)
	require.NoError(t, err, "the test's database writer deletes held's log row")
	require.False(t, h.listenerAdmitted(), "a writer was admitted over the rolled-back prepare row")
	before, _, err := h.s.Records.Journal(ctx, id)
	require.NoError(t, err)
	text := h.rowText()

	code, _, stderr := h.run(actionAbort, "--hold", id, "--reason", "the row reads prepare")
	require.NotZero(t, code, "an abort over the rolled-back row, through the routes that served held")
	t.Logf("through the routes with a floor: %s", stderr)
	require.Equal(t, text, h.rowText(), "the refused abort moved the row")

	h.s.Broker.Restart()
	rolled, err := h.s.Records.Row(ctx)
	require.NoError(t, err, "the restarted routes read the rolled-back row")
	require.Equal(t, natsgrant.OperatorPhasePrepare, rolled.Phase, "the row reads the rolled-back prepare")
	code, _, stderr = h.run(actionAbort, "--hold", id, "--reason", "the row reads prepare")
	require.Equal(t, 1, code, "an abort of a set hold whose row reads prepare")
	require.Contains(t, stderr, holdrecord.CommandSet+" is done")
	require.Equal(t, text, h.rowText(), "the refused abort moved the row")
	after, _, err := h.s.Records.Journal(ctx, id)
	require.NoError(t, err)
	require.Equal(t, before.Ops, after.Ops, "the refused abort wrote a journal entry")
	require.False(t, h.listenerAdmitted(), "a writer was admitted after the refused abort")
}
