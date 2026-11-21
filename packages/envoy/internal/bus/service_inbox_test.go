package bus_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cipher/natscallout"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natscred"
)

// startServiceCallout starts a server whose auth service admits each entry of services by the
// unsigned platform token its client presents (unsignedToken), with static beside it as users of
// the server's own, and returns its client URL.
func startServiceCallout(t *testing.T, services string, static ...testnats.StaticUser) string {
	t.Helper()
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys, static...)
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
		return path
	}
	entries, err := natscallout.ParseServices(services)
	require.NoError(t, err)
	service, err := natscallout.Start(t.Context(), natscallout.Settings{
		URLs: []string{url}, SeedFile: write("service.seed", keys.ServiceSeed),
		IssuerSeedFile: write("issuer.seed", keys.IssuerSeed), XKeySeedFile: write("xkey.seed", keys.XKeySeed), Services: entries,
	}, natscallout.Deps{
		ServiceToken: func(_ context.Context, raw string) (string, bool, time.Time, time.Time, error) {
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
	return url
}

// A client connecting with a service's platform token asks under the reply prefix its token's
// account names (natscred.InboxPrefix), says which account that is, and gets answers there: the
// auth service grants that prefix in place of _INBOX.>, so a JetStream request, answered on the
// connection's response inbox, is answered only when the client uses it.
func TestAServiceConnectionAsksUnderItsOwnInboxPrefix(t *testing.T) {
	url := startServiceCallout(t, "listener/example-hub/*="+serviceTokenAccount)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(unsignedToken(time.Now().Add(50*time.Minute))), 0o600))
	environment := map[string]string{bus.ServiceTokenFileEnvVar: tokenFile, bus.AllowRemoteEnvVar: "1"}
	client, err := bus.Connect([]string{url}, bus.WithEnvironment(func(name string) (string, bool) {
		value, set := environment[name]
		return value, set
	}))
	require.NoError(t, err)
	defer client.Close()

	require.Equal(t, serviceTokenAccount, client.ServiceIdentity())
	inbox := client.Connection().NewInbox()
	require.True(t, strings.HasPrefix(inbox, natscred.InboxPrefix(serviceTokenAccount)+"."),
		"the connection's inbox %s is not under its account's prefix %s", inbox, natscred.InboxPrefix(serviceTokenAccount))
	_, err = client.JS().AccountInfo()
	require.NoError(t, err, "a JetStream request got no answer on the connection's own prefix")
}

// A service connection's stored requests, a window's Request and StartRequestStoredTo, are
// answered: each takes the stream's acknowledgement on an inbox it subscribes itself, which must
// lie under the connection's own prefix, the one inbox space the auth service grants it.
func TestAServiceConnectionsStoredRequestsAreAnswered(t *testing.T) {
	adminSeed, adminKey := testnats.User(t)
	url := startServiceCallout(t, "listener/example-hub/*="+serviceTokenAccount,
		testnats.StaticUser{NKey: adminKey, Publish: []string{">"}, Subscribe: []string{">"}})
	admin, err := testnats.ConnectSeed(t, url, adminSeed)
	require.NoError(t, err)
	defer admin.Close()
	js, err := admin.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&natsgo.StreamConfig{Name: "STORED_REQUESTS", Subjects: []string{"notifications.example.>"}})
	require.NoError(t, err)

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(unsignedToken(time.Now().Add(50*time.Minute))), 0o600))
	environment := map[string]string{bus.ServiceTokenFileEnvVar: tokenFile, bus.AllowRemoteEnvVar: "1"}
	client, err := bus.Connect([]string{url}, bus.WithEnvironment(func(name string) (string, bool) {
		value, set := environment[name]
		return value, set
	}))
	require.NoError(t, err)
	defer client.Close()

	item := contracts.Envelope{Payload: "stored"}
	forward, err := client.StartRequestStoredTo("notifications.example.forward", item, "forward-1", 5*time.Second)
	require.NoError(t, err)
	require.NoError(t, forward.Wait(), "StartRequestStoredTo's acknowledgement never reached the service")
	window := client.WindowUntil(func() time.Time { return time.Now().Add(time.Minute) })
	require.NoError(t, <-window.Request("notifications.example.window", item, bus.RequestOptions{MsgID: "window-1", Timeout: 5 * time.Second}),
		"a window's Request's acknowledgement never reached the service")
}

// A client connecting with a seed, or with no credential, names no service account and keeps
// nats.go's own inbox.
func TestASeedConnectionNamesNoServiceIdentity(t *testing.T) {
	unsetNkeyEnvironment(t)
	_, url := testnats.Start(t)
	client, err := bus.Connect([]string{url}, bus.WithEnvironment(func(name string) (string, bool) {
		if name == bus.AllowRemoteEnvVar {
			return "1", true
		}
		return "", false
	}))
	require.NoError(t, err)
	defer client.Close()
	require.Empty(t, client.ServiceIdentity())
	require.True(t, strings.HasPrefix(client.Connection().NewInbox(), "_INBOX."))
}
