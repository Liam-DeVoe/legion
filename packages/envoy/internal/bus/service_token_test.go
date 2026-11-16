package bus_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/natscallout"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/natscred"
)

const serviceTokenAccount = "system:serviceaccount:example-ns:example-listener"

// unsignedToken is a platform token naming serviceTokenAccount and lapsing at expiry, which the
// test's ServiceToken reads unverified.
func unsignedToken(expiry time.Time) string {
	encode := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return encode(map[string]string{"alg": "none"}) + "." +
		encode(map[string]any{"sub": serviceTokenAccount, "iat": time.Now().Unix(), "exp": expiry.Unix()}) + ".unsigned"
}

// A bus connection presenting a service's token file moves to the later token the file holds,
// natscred.ServiceRenewBefore ahead of the one it connected with, by one reconnect, and stays up.
func TestAServiceTokenClientReconnectsBeforeItsTokenLapses(t *testing.T) {
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
		return path
	}
	var mu sync.Mutex
	var presented []string
	services, err := natscallout.ParseServices("listener/example-hub/*=" + serviceTokenAccount)
	require.NoError(t, err)
	service, err := natscallout.Start(t.Context(), natscallout.Settings{
		URLs: []string{url}, SeedFile: write("service.seed", keys.ServiceSeed),
		IssuerSeedFile: write("issuer.seed", keys.IssuerSeed), XKeySeedFile: write("xkey.seed", keys.XKeySeed), Services: services,
	}, natscallout.Deps{
		ServiceToken: func(_ context.Context, raw string) (string, bool, time.Time, time.Time, error) {
			mu.Lock()
			presented = append(presented, raw)
			mu.Unlock()
			parts := strings.Split(raw, ".")
			payload, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				return "", false, time.Time{}, time.Time{}, err
			}
			var claims struct {
				Sub      string
				Iat, Exp int64
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				return "", false, time.Time{}, time.Time{}, err
			}
			return claims.Sub, false, time.Unix(claims.Iat, 0), time.Unix(claims.Exp, 0), nil
		},
		Log: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(service.Close)
	ready, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, service.Ready(ready))
	count := func(token string) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, each := range presented {
			if each == token {
				n++
			}
		}
		return n
	}

	first := unsignedToken(time.Now().Add(natscred.ServiceRenewBefore + 2*time.Second))
	tokenFile := write("token", first)
	environment := map[string]string{bus.ServiceTokenFileEnvVar: tokenFile, bus.AllowRemoteEnvVar: "1"}
	nc, err := bus.Dial("service-token-test", []string{url}, func(name string) (string, bool) {
		value, set := environment[name]
		return value, set
	})
	require.NoError(t, err)
	defer nc.Close()
	require.True(t, nc.IsConnected())

	later := unsignedToken(time.Now().Add(50 * time.Minute))
	require.NoError(t, os.WriteFile(tokenFile, []byte(later), 0o600))
	testwait.WithinPolling(t, 10*time.Second, 50*time.Millisecond, "the reconnect with the later token", func() bool {
		return count(later) == 1
	})
	testwait.Eventually(t, "the connection back up", nc.IsConnected)
	require.Never(t, func() bool { return count(later) > 1 }, 3*time.Second, 100*time.Millisecond, "the client reconnected more than once")
	require.Equal(t, 1, count(first))
}
