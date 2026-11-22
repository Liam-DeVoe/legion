package natsauth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nuid"

	"github.com/sjawhar/legion/daemon/internal/natsauth"
	"github.com/sjawhar/legion/daemon/internal/testnats"
)

// A consumer create the daemon sends while its connection is cut never reaches the server, not even
// after the reconnect. The guard passed before the cut (the consumer is not there, so the caller
// creates it), and a create nats.go buffered would be flushed on the new connection after the
// release of a hold that refuses the daemon in between, unchecked. Connect sets no reconnect
// buffer, so the publish fails at once. The control is a connection with nats.go's default buffer,
// whose create lands after the reconnect.
func TestAConsumerCreateSentWhileCutNeverReachesTheServer(t *testing.T) {
	upstream := testnats.URL(t)
	admin := testnats.Connect(t, upstream)
	adminJS, err := jetstream.New(admin)
	if err != nil {
		t.Fatal(err)
	}
	streamName := "CUT_" + nuid.Next()
	ctx := context.Background()
	if _, err := adminJS.CreateStream(ctx, jetstream.StreamConfig{Name: streamName, Subjects: []string{"cut." + streamName + ".>"}}); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	t.Cleanup(func() { _ = adminJS.DeleteStream(context.Background(), streamName) })
	quick := []nats.Option{nats.ReconnectWait(10 * time.Millisecond), nats.ReconnectJitter(0, 0)}

	for _, tc := range []struct {
		name  string
		dial  func(url string) (*nats.Conn, error)
		lands bool
	}{
		{name: "the daemon's connection", dial: func(url string) (*nats.Conn, error) {
			return natsauth.Connect([]string{url}, "", t.Name(), append(quick, natsauth.ReconnectForever())...)
		}},
		{name: "control: nats.go's default reconnect buffer", lands: true, dial: func(url string) (*nats.Conn, error) {
			return nats.Connect(url, append(quick, nats.Name(t.Name()), nats.MaxReconnects(-1))...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testnats.NewCutProxy(t, strings.TrimPrefix(upstream, "nats://"))
			conn, err := tc.dial("nats://" + p.Addr)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer conn.Close()
			js, err := jetstream.New(conn)
			if err != nil {
				t.Fatal(err)
			}
			name := "cut-" + nuid.Next()
			// The guard: the consumer is not there, so the caller goes on to create it.
			if _, err := js.Consumer(ctx, streamName, name); !errors.Is(err, jetstream.ErrConsumerNotFound) {
				t.Fatalf("the guard's read of %s = %v, want not found", name, err)
			}
			p.Cut()
			waitFor(t, "the connection cut", func() bool { return conn.IsReconnecting() })
			create, cancel := context.WithTimeout(ctx, time.Second)
			_, err = js.CreateConsumer(create, streamName, jetstream.ConsumerConfig{Durable: name, AckPolicy: jetstream.AckExplicitPolicy})
			cancel()
			if err == nil {
				t.Fatal("a create sent while the connection was cut was answered")
			}
			if !tc.lands && !errors.Is(err, nats.ErrReconnectBufExceeded) {
				t.Fatalf("the daemon's create while cut = %v, want it refused at once by the empty reconnect buffer", err)
			}
			p.Restore()
			waitFor(t, "the reconnect", conn.IsConnected)
			// A round trip on the new connection: whatever was buffered was written before its answer.
			if err := conn.Flush(); err != nil {
				t.Fatalf("flush the reconnected connection: %v", err)
			}
			landed := func() bool {
				_, err := adminJS.Consumer(ctx, streamName, name)
				return err == nil
			}
			if tc.lands {
				waitFor(t, "the buffered create landing", landed)
				return
			}
			if landed() {
				t.Fatalf("%s exists after the reconnect: the create sent while cut reached the server", name)
			}
		})
	}
}

// waitFor polls condition for up to ten seconds, failing t naming what it waited for.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
