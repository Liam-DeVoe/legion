package brokerclient_test

import (
	"crypto"
	"encoding/base64"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/brokertoken"
)

// The proof the client signs for a NATS connection is the one the auth service checks: it verifies
// with the server's nonce, the token it was signed beside and the key's thumbprint, and with no
// other nonce or token.
func TestConnectionProofIsTheOneTheServiceChecks(t *testing.T) {
	key, err := proof.NewKey()
	require.NoError(t, err)
	sum, err := (&jose.JSONWebKey{Key: key.Public()}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	jkt := base64.RawURLEncoding.EncodeToString(sum)

	signed, err := brokerclient.ConnectionProof(key, "the-access-token", "the-server-nonce")
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, brokertoken.VerifyConnectionProof(signed, "the-access-token", "the-server-nonce", jkt, now))
	require.ErrorIs(t, brokertoken.VerifyConnectionProof(signed, "the-access-token", "another-nonce", jkt, now), brokertoken.ErrProofInvalid)
	require.ErrorIs(t, brokertoken.VerifyConnectionProof(signed, "another-token", "the-server-nonce", jkt, now), brokertoken.ErrProofInvalid)

	again, err := brokerclient.ConnectionProof(key, "the-access-token", "the-server-nonce")
	require.NoError(t, err)
	require.NotEqual(t, signed, again, "each proof names a 'jti' of its own")
}
