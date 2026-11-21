package consumernames

import (
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold/holdtest"
	"github.com/sjawhar/envoy/internal/bus"
)

// The stack admits each program a mixed-build row starts beside the listeners: an envoy-dispatch
// as the Dispatch entry, a Legion daemon as the daemon entry, and a session through its own token
// and proof (holdtest.Stack's DispatchCredential, DaemonCredential and SessionCredential), so a row
// can run Dispatch and a daemon on it and read what a listener relays to a session. A session
// receives on its own relay subject what a listener publishes there, and its user may not
// subscribe another session's. Control: a Dispatch machine the stack does not list is refused.
func TestTheMixedBuildStackAdmitsDispatchADaemonAndASession(t *testing.T) {
	h := newStack(t)
	url := h.s.Cluster.URL()

	dispatch, err := h.s.Connect(url, h.s.DispatchCredential(holdtest.DispatchMachines[0]))
	require.NoError(t, err, "Dispatch's entry")
	require.NoError(t, dispatch.Flush())
	daemon, err := h.s.Connect(url, h.s.DaemonCredential())
	require.NoError(t, err, "the daemon's entry")
	require.NoError(t, daemon.Flush())
	_, err = h.s.Connect(url, h.s.DispatchCredential("unlisted-dispatch"), nats.MaxReconnects(0))
	require.Error(t, err, "control: a Dispatch machine the stack does not list was admitted")

	refused := make(chan error, 4)
	session, err := h.s.Connect(url, h.s.SessionCredential("mixed-session"), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		refused <- err
	}))
	require.NoError(t, err, "a session's token and proof")
	own, err := session.SubscribeSync(bus.DeliverSubject("mixed-session"))
	require.NoError(t, err)
	_, err = session.SubscribeSync(bus.DeliverSubject("another-session"))
	require.NoError(t, err)
	require.NoError(t, session.Flush())

	listener, err := h.s.Connect(url, h.s.ListenerCredential(holdtest.ListenerMachines[0]))
	require.NoError(t, err)
	require.NoError(t, listener.Publish(bus.DeliverSubject("mixed-session"), []byte("a relay frame")))
	require.NoError(t, listener.Flush())
	msg, err := own.NextMsg(10 * time.Second)
	require.NoError(t, err, "the session's own relay subject")
	require.Equal(t, "a relay frame", string(msg.Data))
	select {
	case err := <-refused:
		require.True(t, errors.Is(err, nats.ErrPermissionViolation), "the subscribe of another session's relay subject was answered %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("a session's user subscribed another session's relay subject unrefused")
	}
}
