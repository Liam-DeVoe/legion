package writerhold

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/natsgrant"
)

// refusedCredentialProbe dials url with a credential the auth service refuses for its own reason,
// no listed service owning it, as it refuses an expired probe token or a probe entry missing from
// BROKER_NATS_SERVICES, in every phase of the row.
func refusedCredentialProbe(_ context.Context, url string) error {
	nc, err := nats.Connect(url, nats.UserInfo("service", "not-a-listed-token"), nats.NoReconnect(), nats.Timeout(10*time.Second))
	if err == nil {
		nc.Close()
	}
	return err
}

// `hold set` runs its probe once outside the hold, with the row still prepare, and requires it
// admitted (plan §3a step 2: "The probe's first run, outside any hold, must connect, so a refusal in
// a hold cannot come from a bad credential"). A probe credential the hub refuses outside the hold
// stops `hold set` there, naming the probe's error, before its intent and its row move: the row
// stays prepare and the journal holds no `hold set` entry. The control is `hold set` before the
// first run: every later probe step reads the credential's own refusal as the hold's, so it
// passed, and under an auth service whose held refusal is removed (decide.go overlay) a listener
// was admitted under held.
func TestHoldTheProbesFirstRunMustConnect(t *testing.T) {
	h := newHeldStack(t)
	h.seedProtectedStreams()
	require.NoError(t, h.probe(h.s.Cluster.URL()), "the stack's own probe is refused outside a hold")
	require.ErrorIs(t, refusedCredentialProbe(context.Background(), h.s.Cluster.URL()), nats.ErrAuthorization,
		"the bad credential is admitted outside a hold")
	h.op.Probe = refusedCredentialProbe

	code, _, stderr := h.run(actionSet, "--workflow", string(holdrecord.WorkflowBucketsCreate), "--reason", "a probe whose credential cannot connect")
	require.Equal(t, 1, code, "hold set with a probe credential the hub refuses outside the hold")
	require.Contains(t, stderr, "probe: ")
	require.Contains(t, strings.ToLower(stderr), "authorization violation", "the refusal does not name the probe's error")
	row := h.row()
	require.Equal(t, natsgrant.OperatorPhasePrepare, row.Phase, "hold set moved the row past prepare")
	require.Empty(t, h.journalCommands(row.HoldID), "hold set wrote a journal entry")
}
