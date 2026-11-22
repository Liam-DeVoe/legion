package intake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/legion/daemon/internal/natsauth"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// The daemon's boot waits out a NATS cut while it opens its consumers. Every daemon connection
// keeps no reconnect buffer (natsauth.Connect), so a request OpenConsumers makes while its
// connection is cut fails at once with nats.ErrReconnectBufExceeded. The boot's probe retries what
// natsauth.Unreachable calls transient and exits on anything else. The connection is cut after the
// server's version is read, as OpenConsumers asks for the stream (cutOnStream). The cut's error is
// transient, and the same OpenConsumers completes once the connection is back. The control is
// natsauth.Unreachable before this row, which did not count the empty buffer's refusal, so the boot
// exited on a cut.
func TestABootWaitsOutACutWhileItOpensTheConsumers(t *testing.T) {
	r := newCutRig(t)
	cutting := &cutOnStream{JetStream: r.js, cut: r.cut}
	if _, err := OpenConsumers(context.Background(), cutting, r.pool, r.spec); err == nil {
		t.Fatal("OpenConsumers on a cut connection opened the consumers")
	} else if !natsauth.Unreachable(err) {
		t.Fatalf("OpenConsumers while cut = %v, which natsauth.Unreachable does not call transient: the boot would exit rather than wait", err)
	}
	if !cutting.done {
		t.Fatal("OpenConsumers never asked for the stream, so the row cut nothing")
	}
	r.restoreAndOpen(t)
}

// The same holds for a cut just before OpenConsumers reads the server's version, its first call on
// the connection: the read waits for the connection to report a version, and at its deadline
// answers that the connection is not connected (natsversion.ErrNotConnected). natsauth.Unreachable
// calls that transient, so the boot waits rather than exits, and the same OpenConsumers completes
// once the connection is back. The attempt here is bounded at 5 seconds, as the boot's own attempt
// is bounded. The control is the code before this row, whose bare deadline error Unreachable
// refused.
func TestABootWaitsOutACutBeforeItReadsTheServersVersion(t *testing.T) {
	r := newCutRig(t)
	r.cut()
	attempt, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := OpenConsumers(attempt, r.js, r.pool, r.spec)
	cancel()
	if err == nil {
		t.Fatal("OpenConsumers on a cut connection opened the consumers")
	}
	if !natsauth.Unreachable(err) {
		t.Fatalf("OpenConsumers cut before the version read = %v, which natsauth.Unreachable does not call transient: the boot would exit rather than wait", err)
	}
	r.restoreAndOpen(t)
}

// cutRig is a daemon's NATS connection through a proxy the row can cut (testnats.NewCutProxy), the
// rig's Postgres, and a spec on the fixed names.
type cutRig struct {
	pool *pgxpool.Pool
	conn *nats.Conn
	js   jetstream.JetStream
	p    *testnats.CutProxy
	spec ConsumerSpec
	// cut cuts the proxy and waits for the connection to see it.
	cut func()
}

func newCutRig(t *testing.T) *cutRig {
	t.Helper()
	r := &cutRig{pool: migratedPool(t), spec: consumerSpec(&lockedBuffer{})}
	admin := testnats.JetStream(t)
	testnats.CreateStream(t, admin, jetstream.StreamConfig{Name: notificationStream, Subjects: []string{"notifications.>"}})
	r.p = testnats.NewCutProxy(t, strings.TrimPrefix(admin.Conn().ConnectedUrl(), "nats://"))
	conn, err := natsauth.Connect([]string{"nats://" + r.p.Addr}, "", t.Name(),
		nats.ReconnectWait(10*time.Millisecond), nats.ReconnectJitter(0, 0), natsauth.ReconnectForever())
	if err != nil {
		t.Fatalf("connect through the proxy: %v", err)
	}
	t.Cleanup(conn.Close)
	if r.js, err = jetstream.New(conn); err != nil {
		t.Fatal(err)
	}
	r.conn = conn
	r.cut = func() {
		r.p.Cut()
		testwait.Eventually(t, "the connection cut", conn.IsReconnecting)
	}
	return r
}

// restoreAndOpen restores the proxy, waits for the reconnect, and opens the consumers on the same
// connection, failing t when they do not open.
func (r *cutRig) restoreAndOpen(t *testing.T) {
	t.Helper()
	r.p.Restore()
	testwait.Eventually(t, "the reconnect", r.conn.IsConnected)
	consumers, err := OpenConsumers(context.Background(), r.js, r.pool, r.spec)
	if err != nil {
		t.Fatalf("OpenConsumers after the reconnect: %v", err)
	}
	consumers.Close()
}

// cutOnStream is js, which runs cut once, at the first stream lookup, before it asks the server.
type cutOnStream struct {
	jetstream.JetStream
	cut  func()
	done bool
}

func (c *cutOnStream) Stream(ctx context.Context, name string) (jetstream.Stream, error) {
	if !c.done {
		c.done = true
		c.cut()
	}
	return c.JetStream.Stream(ctx, name)
}
