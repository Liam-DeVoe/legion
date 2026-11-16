package brokerclient

import (
	"crypto/ecdsa"
	"errors"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
)

// ConnectionProof is the proof of key a session presents to connect to NATS with accessToken, a
// broker access token bound to key: the RFC 9449-shaped proof the auth service checks
// (brokertoken.VerifyConnectionProof), naming the connection's method and target
// (oauthwire.ConnectionProofMethod, oauthwire.ConnectionProofTarget), the token's hash (ath) and
// nonce, the nonce the server sent this one connection. A proof is good for that connection
// alone: another connection is sent another nonce.
func ConnectionProof(key *ecdsa.PrivateKey, accessToken, nonce string) (string, error) {
	switch {
	case key == nil:
		return "", errors.New("brokerclient: no key to prove for a NATS connection")
	case accessToken == "":
		return "", errors.New("brokerclient: no access token to prove a NATS connection for")
	case nonce == "":
		// A server sends a nonce whenever it has an nkey user, which a hub behind the auth service
		// always has (the service's own), so no nonce means a hub with no auth service: the caller
		// chose a session's credential for a hub that takes none. No Go setting chooses it: its one
		// caller in this repository is natsprobe, which presents a session's credential unless a
		// flag names another, so the error names those flags.
		return "", errors.New("brokerclient: the NATS server sent no nonce to sign, so it runs no auth service and takes no session's broker token; connect to it with the credential it does take (natsprobe: --no-credential, --service-token-file or --service-google-audience)")
	}
	return signProof(key, oauthwire.ConnectionProofMethod, oauthwire.ConnectionProofTarget, nonce, tokenHash(accessToken))
}
