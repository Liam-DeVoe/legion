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

// probeWith points the stack's operator at the probe credential token: its dial and what the hold
// reads of it.
func (h *heldStack) probeWith(token string) {
	h.op.Probe = func(_ context.Context, url string) error {
		nc, err := nats.Connect(url, nats.UserInfo("service", token), nats.NoReconnect(), nats.Timeout(ProbeTimeout))
		if err == nil {
			nc.Close()
		}
		return err
	}
	h.op.ProbeCredential = func() (ProbeCredential, error) { return ProbeCredentialOf(token) }
}

// crashedSetIntent writes hold set's intent on hold id as a first run whose probe was admitted
// under prepare writes it, with its admitted-probe record of the stack's probe credential, and
// nothing after it: a set killed before its done entry.
func (h *heldStack) crashedSetIntent(id string) {
	h.t.Helper()
	credential, err := ProbeCredentialOf(h.s.ProbeToken())
	require.NoError(h.t, err)
	_, err = h.s.Records.Admit(context.Background(), id, holdrecord.Request{Command: holdrecord.CommandSet}, map[string]string{
		probeFingerprintInput: credential.Fingerprint,
		probeExpiryInput:      credential.Expiry.UTC().Format(time.RFC3339),
	})
	require.NoError(h.t, err, "hold set's intent")
}

// A rerun of `hold set` on a held row runs no probe outside the hold: the row is held. It counts the
// probe's refusal as the hold's only on this hold's own proof that the probe's credential was
// admitted, which the run that set the hold under prepare recorded in its intent: the journal holds
// that record (a), the rerun's credential has its fingerprint (b), and the credential lapses after
// now plus the probe's timeout (c). Otherwise the rerun is UNKNOWN, naming which, and changes
// nothing; with the recorded credential it runs on. The reviewer's case is (b) with a credential the
// hub refuses for its own reason, which under a replica that does not enforce held (decide.go's held
// refusal removed) let a listener in under the held row. The control is the rerun at 599c9838f2,
// which ran no such check: every rerun below exited 0.
func TestHoldSetsRerunInHeldCountsTheProbeOnlyOnItsRecordedCredential(t *testing.T) {
	refused := holdtest.PlatformToken("system:serviceaccount:envoy-operator:another-probe", []string{"envoy-nats"}, time.Now().Add(time.Hour))
	for _, row := range []struct {
		name, why string
		before    func(h *heldStack)
	}{
		{"the recorded credential", "", nil},
		{"a credential the hub refuses for its own reason, another fingerprint", "(b)", func(h *heldStack) {
			require.ErrorIs(h.t, func() error { h.probeWith(refused); return h.op.Probe(context.Background(), h.s.Cluster.URL()) }(),
				nats.ErrAuthorization, "setup: the hub refuses the other credential")
		}},
		{"the recorded credential about to lapse", "(c)", func(h *heldStack) {
			h.op.ProbeCredential = func() (ProbeCredential, error) {
				c, err := ProbeCredentialOf(h.s.ProbeToken())
				c.Expiry = time.Now().Add(ProbeTimeout / 2)
				return c, err
			}
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHeldStack(t)
			h.seedProtectedStreams()
			id := h.stoppedInHeld(holdrecord.WorkflowBucketsCreate)
			h.awaitStreamLeaders()
			if row.before != nil {
				row.before(h)
			}
			code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun")
			if row.why == "" {
				require.Zero(t, code, "the rerun with the recorded credential: %s", stderr)
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
	ctx := context.Background()
	id := h.s.Prepare(t, holdrecord.WorkflowBucketsCreate)
	_, err := h.s.Records.Admit(ctx, id, holdrecord.Request{Command: holdrecord.CommandSet}, nil)
	require.NoError(t, err)
	_, err = h.s.Records.MoveRow(ctx, h.row().Gen, natsgrant.OperatorPhaseHeld, id, "a set that recorded no admitted probe")
	require.NoError(t, err)
	require.NoError(t, h.s.Records.AppendStop(ctx, id, holdrecord.CommandSet, "stopped"))
	h.awaitStreamLeaders()

	code, _, stderr := h.run(actionSet, "--hold", id, "--reason", "the rerun")
	require.Equal(t, 2, code, "the rerun with no admitted-probe record: %s", stderr)
	require.Contains(t, stderr, "(a)")
	require.Equal(t, []string{"hold set intent", "hold set stop"}, h.journalCommands(id))
}
