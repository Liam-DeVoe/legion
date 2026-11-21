package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A hold row lost in the middle of a hold is not bootstrapped back released: with the row held and
// then deleted from Postgres (the log keeps its gens), `hold restore --bootstrap` is refused,
// naming the log's next gen, writes no row, and a writer stays refused, since the row cannot be
// read; so is `hold restore` with no hold id, whose released row the route refuses after held's
// entry. `hold restore --hold` of the hold's own id is the way back: the row returns held, and the
// writer is refused under it. The control is the bootstrap as it stood at 36e8cf250e (RestoreRow at
// gen 1 released, following the 409's restore_gen), which wrote "gen 4, phase released".
func TestHoldABootstrapAfterALossMidHoldIsRefused(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.s.HoldWithoutTheSteps(t, id)
	_, err := h.s.Broker.Pool.Exec(ctx, `delete from nats_hold`)
	require.NoError(t, err, "the test's admin deletes the row")
	rows := func() int {
		t.Helper()
		var n int
		require.NoError(t, h.s.Broker.Pool.QueryRow(ctx, `select count(*) from nats_hold`).Scan(&n))
		return n
	}
	code, _, stderr := h.run(actionRestore, "--bootstrap", "--reason", "the row is gone")
	require.Equal(t, 1, code, "a bootstrap after a loss mid-hold")
	require.Contains(t, stderr, "the log's next gen is", "the refusal names the log's next gen")
	require.Contains(t, stderr, "--hold", "the refusal names the way back")
	require.Zero(t, rows(), "the refused bootstrap wrote a row")
	_, err = connectListener(h.s)
	require.Error(t, err, "a writer was admitted after the refused bootstrap")
	code, _, stderr = h.run(actionRestore, "--reason", "the row is gone, restored with no hold id")
	require.Equal(t, 1, code, "a released restore after a held entry")
	require.Contains(t, stderr, "--hold")
	require.Zero(t, rows(), "the refused released restore wrote a row")

	code, _, _ = h.run(actionRestore, "--hold", id, "--reason", "the row is gone")
	require.Zero(t, code, "the restore of the hold's own row")
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseHeld, row.Phase)
	require.Equal(t, id, row.HoldID)
	_, err = connectListener(h.s)
	require.Error(t, err, "a writer was admitted under the restored held row")
}

// A hold row lost outside any hold is restored released, and clients are admitted: (a) after a
// completed hold, `hold restore --hold` of that ended hold reads its journal and sends released at
// the log's next gen; (b) where the log has entries and no hold was ever set, `hold restore` with
// no hold id does the same. `--bootstrap`, for an empty log only, is refused on both. The control is
// `hold restore --hold` as it stood (f0017ccb76 to b57d84c147), which sent held for the ended hold
// and was refused (RestoreRow's ErrAlreadyDone), leaving no row, so every client but the operator
// was refused hold_unknown.
func TestHoldARowLostOutsideAHoldIsRestoredReleased(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	gone := func() {
		t.Helper()
		_, err := h.s.Broker.Pool.Exec(ctx, `delete from nats_hold`)
		require.NoError(t, err, "the test's admin deletes the row")
		require.False(t, h.listenerAdmitted(), "a writer was admitted with no row")
	}
	requireReleased := func(what string) {
		t.Helper()
		row := h.row()
		require.Equal(t, natsgrant.OperatorPhaseReleased, row.Phase, what)
		require.Empty(t, row.HoldID, what)
		require.True(t, h.listenerAdmitted(), "%s: a writer was refused after the released row came back", what)
	}

	// (b) The log holds the first row alone: no hold was ever set.
	gone()
	code, _, _ := h.run(actionRestore, "--bootstrap", "--reason", "the log has entries")
	require.Equal(t, 1, code, "--bootstrap into a log with entries")
	code, _, stderr := h.run(actionRestore, "--reason", "the row is gone, no hold was set")
	require.Zero(t, code, "a released restore where no hold was set: %s", stderr)
	requireReleased("no hold was set")

	// (a) A completed hold.
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.s.HoldWithoutTheSteps(t, id)
	h.s.Release(t, id)
	gone()
	code, stdout, stderr := h.run(actionRestore, "--hold", id, "--reason", "the row is gone after the hold")
	require.Zero(t, code, "a restore of an ended hold's row: %s", stderr)
	require.Contains(t, stdout, "phase released")
	requireReleased("after the completed hold")
}

// A hold row lost in prepare, before `hold set`'s intent, is restored released with the hold's id,
// since the route stores no prepare row and its log's latest entry is the prepare row; the output
// names the cohort record that stays and the next step, `hold abort` or a new `hold prepare`. `hold
// abort` then ends the hold (requireAbortEndsTheReleasedHold). The control is the restore and the
// abort as they stood at 5fd43c72eb: the row came back with no hold id, so the abort was refused
// ("released under hold , not <id>") and the journal never ended.
func TestHoldARowLostInPrepareIsRestoredReleased(t *testing.T) {
	h := newHeldStack(t)
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.PutRecord(ctx, id, "cohort", "cohort", 0, []byte(`{"boundary":1}`))
	require.NoError(t, err)
	_, err = h.s.Broker.Pool.Exec(ctx, `delete from nats_hold`)
	require.NoError(t, err)
	code, stdout, stderr := h.run(actionRestore, "--hold", id, "--reason", "the row is gone in prepare")
	require.Zero(t, code, "a restore of a hold lost in prepare: %s", stderr)
	require.Contains(t, stdout, "phase released")
	require.Contains(t, stdout, "cohort record is still")
	require.Contains(t, stdout, "hold abort --hold "+id)
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseReleased, row.Phase)
	require.Equal(t, id, row.HoldID)
	h.requireAbortEndsTheReleasedHold(id)
}
