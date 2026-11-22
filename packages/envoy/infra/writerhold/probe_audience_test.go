package writerhold

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// The admitted-probe record keeps the probe's audiences as a JSON array, so an audience that holds
// a space comes back from the journal as it went in: a rewrite of the recorded identity, for the
// audience "envoy nats" beside "other", counts as the hold's refusal, and a token for "envoy" alone
// still does not. Round 5's Minor 3; control: db91740cb6's record, which joined the audiences with
// spaces and split them on read, so the rewrite was refused (b), its audience read back as three.
func TestTheProbeRecordKeepsAnAudienceThatHoldsASpace(t *testing.T) {
	ctx := context.Background()
	issuer := oidctest.New(t)
	key := issuer.PublishKey(t, "probe-key")
	audience := []string{"envoy nats", "other"}
	mint := func(aud []string, life time.Duration) string {
		return issuer.Mint(t, key, map[string]any{"iss": issuer.URL(), "sub": "probe", "aud": aud,
			"iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(life).Unix()})
	}
	recorded := mint(audience, time.Hour)
	credential, err := ProbeCredentialOf(ctx, recorded)
	require.NoError(t, err)
	require.Equal(t, audience, credential.Audience)
	journal := holdrecord.Done{Ops: []holdrecord.Operation{
		{Command: holdrecord.CommandSet, State: holdrecord.StateIntent, Inputs: probeRecordInputs(credential)},
	}}
	current := mint(audience, 2*time.Hour)
	op := &Operator{
		ProbeToken: func() (string, error) { return current, nil },
		probeDial:  func(context.Context, string, string) error { return fmt.Errorf("nats: %w", nats.ErrAuthorization) },
	}
	require.NoError(t, op.useProbeRecord(journal))
	require.Equal(t, audience, op.probe.Audience, "the record's audiences read back")
	require.ErrorIs(t, op.Probe(ctx, "nats://hub"), nats.ErrAuthorization, "the refusal of a rewrite of the recorded identity")

	current = mint([]string{"envoy", "other"}, 2*time.Hour)
	err = op.Probe(ctx, "nats://hub")
	require.False(t, errors.Is(err, nats.ErrAuthorization), "a refusal of a token for another audience was answered as the hold's: %v", err)
	var unknown *Unknown
	require.ErrorAs(t, err, &unknown)
	require.ErrorContains(t, err, "(b)")
}
