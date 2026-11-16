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
		// always has (the service's own), so no nonce means a hub with no auth service: the client
		// was configured for one it is not talking to. The text is the TypeScript client's own.
		return "", errors.New("NATS_AUTH is broker, but the NATS server sent no nonce to sign: it runs no auth service, so unset NATS_AUTH for this hub")
	}
	return signProof(key, oauthwire.ConnectionProofMethod, oauthwire.ConnectionProofTarget, nonce, tokenHash(accessToken))
}
