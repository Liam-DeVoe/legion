package writerhold

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// anotherProbeToken is a probe credential of an identity other than the stack's probe: no check of
// this hold's admitted-probe record takes it, whether it compares the whole token or the identity
// the token names.
var anotherProbeToken = holdtest.PlatformToken("system:serviceaccount:envoy-operator:another-probe", []string{"envoy-nats"}, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC))

// `hold set` killed after its intent and its row move leaves its intent pending on a held row. Its
// rerun checks the probe's credential against the hold's admitted-probe record before anything
// else, and a credential the record does not take stops it UNKNOWN (exit 2). That exit writes set's
// stop, which ends the pending intent, so `hold abort` is admitted and releases the row. The
// reviewer's case (round 4, H-1 shape A): the rerun exited 2 with no stop, and abort and release
// were refused for good, "another command's intent is pending: hold set". Control: 9ac771ce3d,
// where the rerun returned before its stop.
func TestHoldSetsRerunRefusedByTheProbeCheckWritesItsStop(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	h.awaitStreamLeaders()
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	h.crashedSetIntent(id)
	_, err := h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseHeld, id, "hold set's move, then its crash")
	require.NoError(t, err)
	h.probeWith(anotherProbeToken)

	code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the crash rerun")
	require.Equal(t, 2, code, "the rerun with a credential the record does not take: %s", stderr)
	require.Contains(t, stderr, "UNKNOWN")
	require.Equal(t, []string{"hold set intent", "hold set stop"}, h.journalCommands(id), "the rerun's exit did not end set's pending intent")

	code, stdout, stderr := h.run(actionAbort, "--hold", id, "--reason", "end the hold set could not finish")
	require.Zero(t, code, "abort after set's stop: %s%s", stdout, stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandAbort))
}

// `hold prove` killed after its intent leaves that intent pending. Its rerun's exits before its own
// admission, the probe check among them, write prove's stop, which ends the pending intent, so
// `hold abort` is admitted (the stop lifts abort's exclusion of prove) and releases the row: no
// target was restored, so every claim is on its step-1 disk. The reviewer's case (round 4, H-1,
// prove's shape A, by code read): the check returned before its admission with no stop, so abort
// was refused busy for good. Control: 9ac771ce3d, where prove's check returned before any stop.
func TestHoldAProveRefusedByTheProbeCheckWritesItsStop(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	id := h.hubRestoreHold()
	_, err := h.s.Records.Admit(context.Background(), id, holdrecord.Request{Command: holdrecord.CommandProve}, nil)
	require.NoError(t, err, "setup: a prove killed after its intent")
	h.probeWith(anotherProbeToken)

	code, _, stderr := h.run(actionProve, "--hold", id, "--restored", holdtest.Claim(0)+"=pvc-"+holdtest.Pod(0))
	require.Equal(t, 2, code, "the prove's rerun with a credential the record does not take: %s", stderr)
	require.Contains(t, stderr, "UNKNOWN")
	require.Equal(t, holdrecord.StateStop, latestState(h.journal(id), holdrecord.CommandProve), "the rerun's exit did not end prove's pending intent")

	code, stdout, stderr := h.run(actionAbort, "--hold", id, "--reason", "end the hold the prove could not prove")
	require.Zero(t, code, "abort after prove's stop: %s%s", stdout, stderr)
	require.Equal(t, natsgrant.OperatorPhaseReleased, h.row().Phase)
	require.Equal(t, holdrecord.StateDone, latestState(h.journal(id), holdrecord.CommandAbort))
}
