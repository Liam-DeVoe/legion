package writerhold

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// A hold whose `hold release` was killed between its row move and its done entry has a verified
// `released` entry naming it in the row's log, which ends it for the restore route (#4679 round 7),
// though its journal holds no end. A later hold prepared after it, whose row is lost in `prepare`,
// is restored `released` with its own id by `hold restore --hold`, and `hold abort` then ends it.
// Control: the route before round 7 (this branch at 0328052c46) took only the log's latest entry as
// a released one, refused `released` over the killed release's open journal, and the command sent
// `held` in its place, so the prepared hold came back held though its set never ran.
func TestHoldARowLostInPrepareAfterAKilledReleaseIsRestoredReleased(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	ctx := context.Background()
	first := h.s.Prepare(t, holdrecord.WorkflowStreamsUpdate)
	h.s.HoldWithoutTheSteps(t, first)
	_, err := h.s.Records.Admit(ctx, first, holdrecord.Request{Command: holdrecord.CommandRelease}, nil)
	require.NoError(t, err)
	_, err = h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseReleased, first, "the release, killed after its PUT")
	require.NoError(t, err)
	require.False(t, h.journal(first).Complete, "the killed release completed its journal")

	second := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.deleteRow()

	code, stdout, stderr := h.run(actionRestore, "--hold", second, "--reason", "a row lost in prepare after a killed release")
	require.Zero(t, code, "restore of the prepared hold: %s%s", stdout, stderr)
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhaseReleased, row.Phase, "the prepared hold came back %s", row.Phase)
	require.Equal(t, second, row.HoldID)
	require.Contains(t, stdout, "hold "+second+" has not ended")

	code, _, stderr = h.run(actionAbort, "--hold", second, "--reason", "end the prepared hold")
	require.Zero(t, code, "abort of the prepared hold restored released: %s", stderr)
	require.True(t, h.journal(second).Complete, "the abort did not end the prepared hold")
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
}
