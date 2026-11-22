package natsauth_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nuid"

	"github.com/sjawhar/legion/daemon/internal/natsauth"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testnkeys"
)

// A consumer create the daemon sends while its connection is cut never reaches the server, not even
// after the reconnect. The guard passed before the cut (the consumer is not there, so the caller
// creates it), and a create nats.go buffered would be flushed on the new connection after the
// release of a hold that refuses the daemon in between, unchecked. Connect sets no reconnect
// buffer, so the publish fails at once. The control is a connection with nats.go's default buffer,
// whose create lands after the reconnect.
func TestAConsumerCreateSentWhileCutNeverReachesTheServer(t *testing.T) {
	upstream := testnats.URL(t)
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
			createWhileCut(t, upstream, testnats.Connect(t, upstream), tc.dial, tc.lands)
		})
	}
}

// The same on the daemon's other dial, ConnectService: the service token's connection, which the
// NATS auth service decides, keeps no reconnect buffer either, so a create sent while it is cut
// fails at once and never lands. A token connection outlasts a refusal (it retries every one), so
// without this a create buffered across the operator's hold would be sent after its release. The
// control is the same row against a ConnectService that keeps nats.go's 8 MiB buffer, run before
// this change: its create lands after the reconnect.
func TestAServiceTokenCreateSentWhileCutNeverReachesTheServer(t *testing.T) {
	s := testnats.StartCallout(t, 0, ">")
	tokenFile := filepath.Join(t.TempDir(), "nats-token")
	writeToken(t, tokenFile, testnkeys.ServiceToken(daemonAccount, time.Now().Add(time.Hour)))
	quick := []nats.Option{nats.ReconnectWait(10 * time.Millisecond), nats.ReconnectJitter(0, 0), natsauth.ReconnectForever()}
	createWhileCut(t, s.URL, s.Connect(t), func(url string) (*nats.Conn, error) {
		return natsauth.ConnectService([]string{url}, tokenFile, t.Name(), quick...)
	}, false)
}

// createWhileCut dials a connection to upstream through a proxy, reads that a consumer is not there
// (the guard), cuts the proxy, sends that consumer's create, restores the proxy and waits for the
// reconnect, then reads through admin whether the create landed: it lands when lands, else the
// create fails at once with nats.ErrReconnectBufExceeded and never reaches the server.
func createWhileCut(t *testing.T, upstream string, admin *nats.Conn, dial func(url string) (*nats.Conn, error), lands bool) {
	t.Helper()
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
	p := dropProxy(t, strings.TrimPrefix(upstream, "nats://"))
	conn, err := dial("nats://" + p.addr)
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
	p.cut()
	waitFor(t, "the connection cut", func() bool { return conn.IsReconnecting() })
	create, cancel := context.WithTimeout(ctx, time.Second)
	_, err = js.CreateConsumer(create, streamName, jetstream.ConsumerConfig{Durable: name, AckPolicy: jetstream.AckExplicitPolicy})
	cancel()
	if err == nil {
		t.Fatal("a create sent while the connection was cut was answered")
	}
	if !lands && !errors.Is(err, nats.ErrReconnectBufExceeded) {
		t.Fatalf("the daemon's create while cut = %v, want it refused at once by the empty reconnect buffer", err)
	}
	p.restore()
	waitFor(t, "the reconnect", conn.IsConnected)
	// A round trip on the new connection: whatever was buffered was written before its answer.
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush the reconnected connection: %v", err)
	}
	landed := func() bool {
		_, err := adminJS.Consumer(ctx, streamName, name)
		return err == nil
	}
	if lands {
		waitFor(t, "the buffered create landing", landed)
		return
	}
	if landed() {
		t.Fatalf("%s exists after the reconnect: the create sent while cut reached the server", name)
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
