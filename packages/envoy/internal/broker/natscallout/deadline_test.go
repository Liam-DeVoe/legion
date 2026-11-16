package natscallout

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
)

// raisedBudget is what the control raises decideBudget to: past the two seconds a Go client's
// connect allows, so the same blocking stub makes that client fail rather than be refused.
const raisedBudget = 2500 * time.Millisecond

// A client's connect allows two seconds (nats.go sets one deadline of Opts.Timeout over the whole
// handshake, processConnectInit; the server gives up at its own AUTH_TIMEOUT, also two seconds),
// so every decision is answered inside the budget: a stub that blocks for three seconds is
// refused with the budget's reason, naming the step, inside two seconds, and the server logs no
// authorization timeout. A stub that answers in 100 ms is admitted.
//
// The control is what makes the budget mean anything: raise decideBudget to 2.5 s with the same
// blocking stub, and the client fails its connect rather than being refused. Without it a shorter
// budget would look like caution; with it, an answer the budget would have stopped is shown to
// cost the connection.
func TestTheServiceAnswersInsideTheGoClientsConnectDeadline(t *testing.T) {
	keys := testnats.NewCalloutKeys(t)
	server, url := testnats.StartWithCalloutServer(t, keys)
	account := "system:serviceaccount:example-ns:example-daemon"

	// blockingFor is a ServiceToken that takes wait, as a verifier waiting on a key set would,
	// and answers a live token of the listed account after it.
	blockingFor := func(wait time.Duration) Deps {
		return Deps{ServiceToken: func(ctx context.Context, _ string) (string, bool, time.Time, time.Time, error) {
			select {
			case <-ctx.Done():
				return "", false, time.Time{}, time.Time{}, ctx.Err()
			case <-time.After(wait):
			}
			now := time.Now()
			return account, false, now, now.Add(time.Hour), nil
		}}
	}
	// connectService connects as that daemon, as a service with a platform token does, and
	// answers how long the connect took.
	connectService := func(t *testing.T) (error, time.Duration) {
		t.Helper()
		token := unsignedServiceToken(account, time.Hour)
		began := time.Now()
		nc, err := nats.Connect(url, nats.UserInfo("service", token), nats.NoReconnect())
		took := time.Since(began)
		if err == nil {
			t.Cleanup(nc.Close)
		}
		return err, took
	}

	t.Run("a verifier that answers in 100ms", func(t *testing.T) {
		service := startService(t, url, keys, blockingFor(100*time.Millisecond), Decide)
		t.Cleanup(service.Close)
		err, took := connectService(t)
		require.NoError(t, err, "a connection decided in 100 ms was not admitted (%s)", took)
	})

	t.Run("a verifier that blocks for 3s is refused inside the client's deadline", func(t *testing.T) {
		service := startService(t, url, keys, blockingFor(3*time.Second), Decide)
		t.Cleanup(service.Close)
		before := len(server.Log())
		err, took := connectService(t)
		require.ErrorIs(t, err, nats.ErrAuthorization,
			"the client failed its connect after %s rather than being refused", took)
		require.Less(t, took, 2*time.Second, "the refusal reached the client after %s, past its connect's own deadline", took)
		require.NotContains(t, server.Log()[before:], "not received in time",
			"the server gave up on the service rather than reading its refusal")
	})

	t.Run("control: the same stub with the budget raised to 2.5s", func(t *testing.T) {
		was := decideBudget
		decideBudget = raisedBudget
		t.Cleanup(func() { decideBudget = was })
		service := startService(t, url, keys, blockingFor(3*time.Second), Decide)
		t.Cleanup(service.Close)
		err, took := connectService(t)
		require.Error(t, err, "a client admitted with the budget at %s: the budget would be arbitrary", raisedBudget)
		require.NotErrorIs(t, err, nats.ErrAuthorization,
			"with the budget at %s the client was refused after %s, not failed: the control proves nothing", raisedBudget, took)
	})
}

// unsignedServiceToken is a platform identity token naming account and lapsing after life, which
// the stubs above read without verifying.
func unsignedServiceToken(account string, life time.Duration) string {
	encode := func(value any) string {
		raw, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	now := time.Now()
	return strings.Join([]string{
		encode(map[string]string{"alg": "none"}),
		encode(map[string]any{"sub": account, "iat": now.Unix(), "exp": now.Add(life).Unix()}),
		"unsigned",
	}, ".")
}
