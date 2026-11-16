package natscred

import (
	"context"
	"crypto/ecdsa"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/brokerclient"
)

// SessionFrom is Session's credential on a token source of the test's own, which a test of the
// one snapshot per CONNECT rotates between the signature and the token. Sign is its SignatureCB,
// for the control that reads the token again in its TokenHandler.
func SessionFrom(key *ecdsa.PrivateKey, current func(context.Context) (string, error)) (option nats.Option, sign func([]byte) ([]byte, error)) {
	credential := &sessionCredential{key: key, current: current}
	return credential.option(), credential.sign
}

// SessionKeeper is Session's Keeper on a renewal loop of the test's own, which stands in for
// brokerclient.Client.Run, beside a connection that presented the token presented.
func SessionKeeper(run func(context.Context, func(context.Context, *brokerclient.Token) error) error, presented string) Keeper {
	return &sessionCredential{run: run, presented: presented}
}

// PlaceholderJWT is the user JWT a session's CONNECT names.
const PlaceholderJWT = placeholderJWT
