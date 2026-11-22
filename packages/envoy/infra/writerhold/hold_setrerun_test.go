package writerhold

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// stoppedInHeld prepares a hold of w and runs `hold set` on it with a probe the hub always admits:
// the set passes its first probe, writes its intent, moves the row to held, and stops at step 2,
// where the probe must be refused. The row is held and set's latest entry is its stop.
func (h *heldStack) stoppedInHeld(w holdrecord.Workflow) string {
	h.t.Helper()
	id := h.prepared(w)
	real := h.op.ProbeDial
	h.op.ProbeDial = func(context.Context, string, string) error { return nil }
	code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "a set whose probe the hub admits")
	h.op.ProbeDial = real
	require.Equal(h.t, 1, code, "hold set with an admitted probe")
	require.Contains(h.t, stderr, "admitted the hold's probe")
	require.Equal(h.t, natsgrant.OperatorPhaseHeld, h.row().Phase)
	require.Equal(h.t, []string{"hold set intent", "hold set stop"}, h.journalCommands(id))
	return id
}

// `hold set`'s rerun after its own stop, with the row held, is the way on the plan names ("hold
// set's refusal in held needs its rerun"). The rerun goes through holdrecord.Admit: it takes the
// one-command lock and writes a new intent before it restarts anything, and its done entry follows
// that intent. A held command given --hold of such a hold reruns hold set the same way, then runs.
// The control is the rerun as it stood: it ran steps 2 to 6 unadmitted, restarting every server
// with no intent, and failed at its done entry ("hold set's latest entry is \"stop\"").
func TestHoldSetsRerunAfterItsStopIsAdmitted(t *testing.T) {
	t.Run("hold set --hold", func(t *testing.T) {
		h := newHeldStack(t)
		h.seedProtectedStreams()
		id := h.stoppedInHeld(holdrecord.WorkflowBucketsCreate)
		h.awaitStreamLeaders()
		code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun")
		require.Zero(t, code, "hold set's rerun after its stop: %s", stderr)
		require.Equal(t, []string{"hold set intent", "hold set stop", "hold set intent", "hold set done"}, h.journalCommands(id))
		code, _, stderr = h.run(actionRelease, "--hold", id, "--reason", "nothing changed")
		require.Zero(t, code, "release after the rerun: %s", stderr)
	})
	t.Run("a held command given --hold", func(t *testing.T) {
		h := newHeldStack(t)
		h.seedProtectedStreams()
		id := h.stoppedInHeld(holdrecord.WorkflowStreamsUpdate)
		h.awaitStreamLeaders()
		code, _, stderr := h.streamsRun(nil, "update", "--hold", id, "--reason", "the update after set's stop")
		require.Zero(t, code, "streams update --hold after hold set's stop: %s", stderr)
		require.Equal(t, []string{"hold set intent", "hold set stop", "hold set intent", "hold set done",
			CommandStreamsUpdate + " intent", CommandStreamsUpdate + " done"}, h.journalCommands(id))
	})
}

// The independent tester's form of the same rerun: `hold set` refused after its row move by a
// restart past its limit, then run again, is done; its control, the rerun after a crash (its intent
// pending, the row held), was done before the fix too. Before the fix the rerun after the stop
// exited 1 with set's latest entry still "stop".
func TestHoldSetsRerunAfterARestartPastItsLimit(t *testing.T) {
	for _, row := range []struct {
		name  string
		crash bool
	}{{"the control: after a crash", true}, {"after its stop", false}} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			h.seedProtectedStreams()
			ctx := context.Background()
			var id string
			if row.crash {
				id = h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
				h.crashedSetIntent(id)
				_, err := h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseHeld, id, "hold set's move, then its crash")
				require.NoError(t, err)
			} else {
				h.op.RestartLimit = time.Millisecond
				code, _, _ := h.run(actionSet, "--workflow", string(holdrecord.WorkflowBucketsCreate), "--reason", "a set that stops at its restart")
				h.op.RestartLimit = 3 * time.Minute
				require.NotZero(t, code)
				id = h.row().HoldID
				require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
				require.Equal(t, holdrecord.StateStop, latestState(h.journal(id), holdrecord.CommandSet))
			}
			h.awaitStreamLeaders()
			code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun")
			require.Zero(t, code, "hold set's rerun (%s): %s", row.name, stderr)
			require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandSet))
			code, _, stderr = h.run(actionRelease, "--hold", id, "--reason", "nothing changed")
			require.Zero(t, code, "release after the rerun: %s", stderr)
		})
	}
}
