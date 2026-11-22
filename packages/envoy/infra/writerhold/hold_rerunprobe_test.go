package writerhold

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// probeWith points the stack's operator at token: what it reads of the probe's credential file, so
// what each probe dial presents.
func (h *heldStack) probeWith(token string) {
	h.op.ProbeToken = func() (string, error) { return token, nil }
}

// stackProbeRecord is the admitted-probe record of the stack's probe token, as `hold set`'s first
// run writes it in its intent, its expiry replaced by expiry when that is not zero.
func (h *heldStack) stackProbeRecord(expiry time.Time) map[string]string {
	h.t.Helper()
	credential, err := ProbeCredentialOf(context.Background(), h.s.ProbeToken())
	require.NoError(h.t, err)
	if !expiry.IsZero() {
		credential.Expiry = expiry
	}
	return probeRecordInputs(credential)
}

// crashedSetIntent writes hold set's intent on hold id as a first run whose probe was admitted
// under prepare writes it, with its admitted-probe record of the stack's probe token, and nothing
// after it: a set killed before its done entry.
func (h *heldStack) crashedSetIntent(id string) {
	h.t.Helper()
	_, err := h.s.Records.Admit(context.Background(), id, holdrecord.Request{Command: holdrecord.CommandSet}, h.stackProbeRecord(time.Time{}))
	require.NoError(h.t, err, "hold set's intent")
}

// stoppedSetWith prepares a buckets-create hold, writes hold set's intent with inputs, moves the row
// to held and writes set's stop: a set that stopped after its move, whose intent holds inputs.
func (h *heldStack) stoppedSetWith(inputs map[string]string) string {
	h.t.Helper()
	ctx := context.Background()
	id := h.s.Prepare(h.t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, inputs)
	require.NoError(h.t, err)
	_, err = h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseHeld, id, "a set that stopped after its move")
	require.NoError(h.t, err)
	require.NoError(h.t, h.s.Records.AppendStop(ctx, id, holdrecord.CommandSet, "stopped"))
	return id
}

// A rerun of `hold set` on a held row runs no probe outside the hold: the row is held. It counts the
// probe's refusal as the hold's only through Operator.Probe's check against the admitted-probe
// record the run that set the hold under prepare wrote in its intent, and makes the same check
// before it writes anything: the journal holds that record (a), the token the probe presents hashes
// to the recorded sha256 (b), and the recorded expiry is after now plus the probe's timeout (c).
// Otherwise the rerun is UNKNOWN, naming which, and changes nothing; with the recorded token it runs
// on. The reviewers' cases are (b): a credential the hub refuses for its own reason (round 3), and
// a token with the recorded account and audience that the hub refuses (round 4), each of which
// under a replica that does not enforce held (decide.go's held refusal removed) let a listener in
// under the held row. Controls: the rerun at 599c9838f2, which ran no such check (every rerun below
// exited 0), and at 870ce0c500, whose check read the token's account and audience alone (the
// round-4 token's rerun exited 0).
func TestHoldSetsRerunInHeldCountsTheProbeOnlyOnItsRecordedCredential(t *testing.T) {
	refused := holdtest.PlatformToken("system:serviceaccount:envoy-operator:another-probe", []string{"envoy-nats"}, time.Now().Add(time.Hour))
	stopped := func(h *heldStack) string { return h.stoppedInHeld(holdrecord.WorkflowBucketsCreate) }
	stoppedWith := func(token string) func(h *heldStack) string {
		return func(h *heldStack) string {
			id := stopped(h)
			h.probeWith(token)
			require.ErrorIs(h.t, h.probe(h.s.Cluster.URL()), nats.ErrAuthorization, "setup: the hub refuses the other token")
			return id
		}
	}
	for _, row := range []struct {
		name, why string
		hold      func(h *heldStack) string
	}{
		{"the recorded token", "", stopped},
		{"a credential the hub refuses for its own reason", "(b)", stoppedWith(refused)},
		{"the recorded account and audience, a token the hub refuses", "(b)", stoppedWith(otherProbeToken)},
		{"the recorded token about to lapse", "(c)", func(h *heldStack) string {
			return h.stoppedSetWith(h.stackProbeRecord(time.Now().Add(ProbeTimeout / 2)))
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			h.seedProtectedStreams()
			id := row.hold(h)
			h.awaitStreamLeaders()
			code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun")
			if row.why == "" {
				require.Zero(t, code, "the rerun with the recorded token: %s", stderr)
				return
			}
			require.Equal(t, 2, code, "the rerun %s: %s", row.name, stderr)
			require.Contains(t, stderr, row.why)
			require.Equal(t, []string{"hold set intent", "hold set stop"}, h.journalCommands(id), "the refused rerun wrote an entry")
			require.Equal(t, natsgrant.OperatorPhaseHeld, h.row().Phase)
		})
	}
}

// (a): a held hold whose journal holds no admitted-probe record (a set whose intent was written by a
// process that recorded none) is UNKNOWN on its rerun. The control is 599c9838f2's rerun, which
// exited 0.
func TestHoldSetsRerunInHeldWithNoAdmittedProbeRecordIsUnknown(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	id := h.stoppedSetWith(nil)
	h.awaitStreamLeaders()

	code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun")
	require.Equal(t, 2, code, "the rerun with no admitted-probe record: %s", stderr)
	require.Contains(t, stderr, "(a)")
	require.Equal(t, []string{"hold set intent", "hold set stop"}, h.journalCommands(id))
}
