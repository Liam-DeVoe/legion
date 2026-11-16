package natscred_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/natscred"
)

// presented records each service token the auth service was presented, in order.
type presented struct {
	mu     sync.Mutex
	tokens []string
}

func (p *presented) serviceToken(_ context.Context, raw string) (string, bool, time.Time, time.Time, error) {
	p.mu.Lock()
	p.tokens = append(p.tokens, raw)
	p.mu.Unlock()
	return readPlatformToken(raw)
}

func (p *presented) count(token string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, each := range p.tokens {
		if each == token {
			n++
		}
	}
	return n
}

// A service's keeper reads the expiry of the token its connection presented and, ServiceRenewBefore
// ahead of it, reconnects once with the later token its file holds by then: the auth service is
// presented the later token once, and the connection stays up.
func TestServiceKeeperReconnectsBeforeExpiry(t *testing.T) {
	seen := &presented{}
	rig := newRig(t, nil, seen.serviceToken)
	file := filepath.Join(t.TempDir(), "token")
	first := platformToken(daemonAccount, time.Now().Add(natscred.ServiceRenewBefore+2*time.Second))
	require.NoError(t, os.WriteFile(file, []byte(first+"\n"), 0o600))

	option, keeper := natscred.Service(natscred.ServiceFile(file))
	nc, err := nats.Connect(rig.url, option, nats.Timeout(10*time.Second))
	require.NoError(t, err, "the service's token was refused:\n%s", rig.log.String())
	defer nc.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go keeper.Keep(ctx, nc)

	later := platformToken(daemonAccount, time.Now().Add(50*time.Minute))
	require.NoError(t, os.WriteFile(file, []byte(later+"\n"), 0o600))
	testwait.WithinPolling(t, 10*time.Second, 50*time.Millisecond, "the keeper's reconnect with the later token", func() bool {
		return seen.count(later) == 1
	})
	require.Eventually(t, nc.IsConnected, 5*time.Second, 50*time.Millisecond)
	require.Never(t, func() bool { return seen.count(later) > 1 }, 3*time.Second, 100*time.Millisecond,
		"the keeper reconnected again with a token it had already presented")
	require.Equal(t, 1, seen.count(first))
}

// A service whose token the auth service refuses keeps trying, and is admitted once the service
// answers: here the first two decisions refuse it. The control is the same client without
// IgnoreAuthErrorAbort, which nats.go closes after the same refusal twice.
func TestServiceCredentialRetriesAfterRefusals(t *testing.T) {
	var asked atomic.Int64
	refuseTwice := func(_ context.Context, raw string) (string, bool, time.Time, time.Time, error) {
		if asked.Add(1) <= 2 {
			return "", false, time.Time{}, time.Time{}, errors.New("the broker could not verify the token yet")
		}
		return readPlatformToken(raw)
	}
	rig := newRig(t, nil, refuseTwice)
	file := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(file, []byte(platformToken(daemonAccount, time.Now().Add(time.Hour))), 0o600))
	retrying := []nats.Option{nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1), nats.ReconnectWait(50 * time.Millisecond), nats.Timeout(5 * time.Second)}

	option, _ := natscred.Service(natscred.ServiceFile(file))
	nc, err := nats.Connect(rig.url, append(retrying, option)...)
	require.NoError(t, err)
	defer nc.Close()
	testwait.WithinPolling(t, 20*time.Second, 50*time.Millisecond, "the service's connection after two refusals", nc.IsConnected)
	require.GreaterOrEqual(t, asked.Load(), int64(3))

	t.Run("control: without IgnoreAuthErrorAbort", func(t *testing.T) {
		asked.Store(0)
		reader := natscred.ServiceFile(file)
		closed := make(chan struct{})
		control, err := nats.Connect(rig.url, append(retrying,
			nats.UserInfoHandler(func() (string, string) { token, _ := reader.Read(); return natscred.ServiceUser, token }),
			nats.ClosedHandler(func(*nats.Conn) { close(closed) }))...)
		require.NoError(t, err)
		defer control.Close()
		select {
		case <-closed:
		case <-time.After(20 * time.Second):
			t.Fatalf("the control kept trying after the same refusal twice; connected: %t", control.IsConnected())
		}
		require.False(t, control.IsConnected())
	})
}

// Identity names the account a platform token is for, unverified: a Kubernetes token's subject.
func TestIdentityNamesTheTokensAccount(t *testing.T) {
	account, err := natscred.Identity(platformToken(daemonAccount, time.Now().Add(time.Hour)))
	require.NoError(t, err)
	require.Equal(t, daemonAccount, account)
	_, err = natscred.Identity("not-a-jwt")
	require.Error(t, err)
}
