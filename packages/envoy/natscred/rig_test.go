package natscred_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/natscallout"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/testnats"
)

// daemonAccount is the service account the rig's service tokens name, listed as a daemon.
const daemonAccount = "system:serviceaccount:example-ns:example-daemon"

// lockedBuffer is the service's log, which its answering goroutines write while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// rig is a hub behind the real auth service, whose broker answers are the test's own.
type rig struct {
	url    string
	server *testnats.Server
	log    *lockedBuffer

	// settings and deps are the auth service's, which startService starts again after a stop.
	settings natscallout.Settings
	deps     natscallout.Deps
	service  *natscallout.Service
}

// newRig starts a callout hub and the auth service in front of it, deciding sessions with
// introspect and services with serviceToken, the one service listed being a daemon on
// daemonAccount.
func newRig(t *testing.T, introspect func(context.Context, string) (oauthwire.Introspection, error),
	serviceToken func(context.Context, string) (string, bool, time.Time, time.Time, error)) *rig {
	t.Helper()
	keys := testnats.NewCalloutKeys(t)
	server, url := testnats.StartWithCalloutServer(t, keys)
	dir := t.TempDir()
	write := func(name, seed string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(seed), 0o600))
		return path
	}
	services, err := natscallout.ParseServices("daemon/-/*=" + daemonAccount)
	require.NoError(t, err)
	log := &lockedBuffer{}
	r := &rig{
		url: url, server: server, log: log,
		settings: natscallout.Settings{
			URLs:     []string{url},
			SeedFile: write("service.seed", keys.ServiceSeed), IssuerSeedFile: write("issuer.seed", keys.IssuerSeed),
			XKeySeedFile: write("xkey.seed", keys.XKeySeed), Services: services,
		},
		deps: natscallout.Deps{Introspect: introspect, ServiceToken: serviceToken, Log: slog.New(slog.NewTextHandler(log, nil))},
	}
	r.startService(t)
	return r
}

// startService starts the auth service in front of the rig's hub, on the hub's keys and the
// rig's answers, closed when the test ends, and returns once the hub sends it each connection.
func (r *rig) startService(t *testing.T) {
	t.Helper()
	service, err := natscallout.Start(t.Context(), r.settings, r.deps)
	require.NoError(t, err)
	t.Cleanup(service.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, service.Ready(ctx))
	r.service = service
}

// stopService stops the auth service, so the hub has nobody to ask about a connection and
// admits none but its own users until startService.
func (r *rig) stopService() { r.service.Close() }

// thumbprint is the RFC 7638 thumbprint of key, which the broker binds a token to.
func thumbprint(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	sum, err := (&jose.JSONWebKey{Key: key.Public()}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(sum)
}

// liveFor answers every token of tokens live for session ses_a, bound to jkt, for 15 minutes.
func liveFor(jkt string, tokens func(string) bool) func(context.Context, string) (oauthwire.Introspection, error) {
	return func(_ context.Context, token string) (oauthwire.Introspection, error) {
		if !tokens(token) {
			return oauthwire.Introspection{}, nil
		}
		return oauthwire.Introspection{
			Active: true, Session: "ses_a", Person: "person@example.com", ClientID: brokertoken.ExchangeClientID,
			ProjectScope: brokertoken.ProjectScopePerson, Thumbprint: jkt, ExpiresAt: time.Now().Add(15 * time.Minute).Unix(),
		}, nil
	}
}

// platformToken is an unsigned JWT naming account, issued now and lapsing at expiry: the stub
// ServiceToken below reads it, and natscred reads its expiry, neither verifying it.
func platformToken(account string, expiry time.Time) string {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return encode(map[string]string{"alg": "none"}) + "." +
		encode(map[string]any{"sub": account, "iat": time.Now().Unix(), "exp": expiry.Unix()}) + ".unsigned"
}

// readPlatformToken is a stub ServiceToken: the account, issue and expiry raw names, unverified.
func readPlatformToken(raw string) (string, bool, time.Time, time.Time, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", false, time.Time{}, time.Time{}, errors.New("no JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false, time.Time{}, time.Time{}, err
	}
	var claims struct {
		Sub      string `json:"sub"`
		Iat, Exp int64
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", false, time.Time{}, time.Time{}, fmt.Errorf("claims: %w", err)
	}
	return claims.Sub, false, time.Unix(claims.Iat, 0), time.Unix(claims.Exp, 0), nil
}
