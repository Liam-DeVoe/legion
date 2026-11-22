package intake

import (
	"context"
	"strings"
	"testing"
	"time"

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
	pool := migratedPool(t)
	admin := testnats.JetStream(t)
	testnats.CreateStream(t, admin, jetstream.StreamConfig{Name: notificationStream, Subjects: []string{"notifications.>"}})
	p := testnats.NewCutProxy(t, strings.TrimPrefix(admin.Conn().ConnectedUrl(), "nats://"))
	conn, err := natsauth.Connect([]string{"nats://" + p.Addr}, "", t.Name(),
		nats.ReconnectWait(10*time.Millisecond), nats.ReconnectJitter(0, 0), natsauth.ReconnectForever())
	if err != nil {
		t.Fatalf("connect through the proxy: %v", err)
	}
	defer conn.Close()
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	spec := consumerSpec(&lockedBuffer{})

	cutting := &cutOnStream{JetStream: js, cut: func() {
		p.Cut()
		testwait.Eventually(t, "the connection cut", conn.IsReconnecting)
	}}
	if _, err := OpenConsumers(context.Background(), cutting, pool, spec); err == nil {
		t.Fatal("OpenConsumers on a cut connection opened the consumers")
	} else if !natsauth.Unreachable(err) {
		t.Fatalf("OpenConsumers while cut = %v, which natsauth.Unreachable does not call transient: the boot would exit rather than wait", err)
	}
	if !cutting.done {
		t.Fatal("OpenConsumers never asked for the stream, so the row cut nothing")
	}
	p.Restore()
	testwait.Eventually(t, "the reconnect", conn.IsConnected)
	consumers, err := OpenConsumers(context.Background(), js, pool, spec)
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
