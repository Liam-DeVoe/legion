//go:build natslive

package core_test

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
)

// serverDefaultAuthTimeout is nats-server's authorization timeout, in seconds, when the
// configuration sets none (v2.15.1 server/const.go AUTH_TIMEOUT).
const serverDefaultAuthTimeout = 2

// calloutAnswerBudget is how soon the auth service must answer: the budget core/nats_callout.go and
// the README state, half a second inside the client's first PING.
const calloutAnswerBudget = 1500 * time.Millisecond

// renderedTimeout is the authorization timeout, in seconds, that text sets, or nats-server's
// default when it sets none, as the callout block does.
func renderedTimeout(t *testing.T, text string) int {
	t.Helper()
	match := regexp.MustCompile(`(?m)^  timeout: (\d+)$`).FindStringSubmatch(text)
	if match == nil {
		return serverDefaultAuthTimeout
	}
	seconds, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return seconds
}

// restartWithAuthServiceDelay is calloutRig.restart with an auth service that holds each answer for
// delay before it gives it.
func (r *calloutRig) restartWithAuthServiceDelay(t *testing.T, ctr hubContainer, config string, delay time.Duration) string {
	t.Helper()
	ctx := context.Background()
	stop := 10 * time.Second
	require.NoError(t, ctr.Stop(ctx, &stop))
	require.NoError(t, ctr.CopyToContainer(ctx, []byte(config), "/etc/nats/nats-server.conf", 0o644))
	require.NoError(t, ctr.Start(ctx))
	uri, err := ctr.ConnectionString(ctx)
	require.NoError(t, err)
	var conn *natsgo.Conn
	require.Eventually(t, func() bool {
		conn, err = testnats.ConnectSeed(t, uri, r.serviceSeed)
		return err == nil
	}, 30*time.Second, 100*time.Millisecond, "the auth service's user could not connect to the restarted hub")
	t.Cleanup(conn.Close)
	_, err = conn.Subscribe("$SYS.REQ.USER.AUTH", func(m *natsgo.Msg) {
		time.Sleep(delay)
		r.answer(t, m)
	})
	require.NoError(t, err)
	require.NoError(t, conn.Flush())
	return uri
}

// TestACalloutAnswerInsideTheRenderedTimeoutAdmitsTheClient (found in the acceptance of
// dispatch://INFRA-3884, cybermonklabs/legion#4118 at e7c174fa1b): a nats.go client is the client
// every Go service of this repository connects with, and it is admitted only if the auth service's
// answer reaches it inside the time the block lets the service take. Measured at e7c174fa1b, which
// rendered `timeout: 5`, on testnats.HubImage (nats-server 2.15.1) with nats.go v1.53.1: an answer
// after 3.5 s was inside that timeout, but the client had already failed at 2.1 s with
// "nats: expected 'PONG', got 'PING'": the server sends a client its first PING after
// firstClientPingInterval (2 s, plus up to 20%: server/client.go setFirstPingTimer), also while
// the client's CONNECT still waits for the callout, and nats.go's handshake reads anything but
// PONG as an error. nats-server 2.15.1 reads no configuration key that lengthens that first ping
// (DisableShortFirstPing is `json:"-"` in server/opts.go). So the block renders no timeout, the
// server's default of 2 s stands, and the service's budget is 1.5 s.
//
// Each row's answer comes inside both: 0.5 s before the rendered timeout (1.5 s at the default),
// and at the stated budget. The first row is the guard against a longer timeout: a block that
// rendered `timeout: 3` or more would put that answer at 2.5 s or later, past the first PING, and
// the row fails. The control that the rig would admit a client at all is an answer at 0.5 s, which
// e7c174fa1b admitted while it refused the 3.5 s one.
func TestACalloutAnswerInsideTheRenderedTimeoutAdmitsTheClient(t *testing.T) {
	rig := newCalloutRig(t, false)
	text := rig.render(t, true)
	timeout := renderedTimeout(t, text)
	for _, row := range []struct {
		name  string
		delay time.Duration
	}{
		{"0.5 s before the rendered timeout", time.Duration(timeout)*time.Second - 500*time.Millisecond},
		{"at the stated budget", calloutAnswerBudget},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctr, _ := rig.startHub(t)
			uri := rig.restartWithAuthServiceDelay(t, ctr, text, row.delay)
			before := rig.requests.Load()
			start := time.Now()
			conn, err := testnats.ConnectSeed(t, uri, "", natsgo.Token(sessionToken), natsgo.Timeout(time.Duration(timeout+5)*time.Second))
			require.NoError(t, err, "the auth service answered after %s, inside the rendered timeout of %d s, and the client was refused after %s",
				row.delay, timeout, time.Since(start).Round(100*time.Millisecond))
			conn.Close()
			rig.requireRequests(t, before+1, "the client was sent to the callout")
		})
	}
}
