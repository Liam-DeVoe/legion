package natsversion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A connection that is reconnecting reports no server version ((*nats.Conn).ConnectedServerVersion
// answers "" while it is not connected), and that is not an unknown server: ConnectedRefusal waits
// for the reconnect and reads the version of the server it reached, so a daemon whose connection
// drops at the moment of the check boots once the server is back rather than ending its boot on
// "a version that cannot be read". The control is a connection that does not reconnect within the
// wait: it is refused for that, never for the version it could not read.
func TestConnectedRefusalWaitsOutAReconnect(t *testing.T) {
	server, uri := testnats.Start(t)
	conn, err := nats.Connect(uri, nats.Name(t.Name()), nats.MaxReconnects(-1), nats.ReconnectWait(50*time.Millisecond))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)

	server.Stop(t)
	deadline := time.Now().Add(5 * time.Second)
	for conn.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if conn.IsConnected() {
		t.Fatal("the connection still reports connected 5s after its server stopped")
	}
	if version := conn.ConnectedServerVersion(); version != "" {
		t.Fatalf("a reconnecting connection reports version %q, want none: this test needs the reading the boot met", version)
	}

	t.Run("a reconnect within the wait reads the server it reached", func(t *testing.T) {
		restarted := make(chan struct{})
		go func() {
			time.Sleep(300 * time.Millisecond)
			server.Start(t)
			close(restarted)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := ConnectedRefusal(ctx, conn)
		<-restarted
		if err != nil {
			t.Fatalf("ConnectedRefusal across a reconnect = %v, want nil for the in-process v2.15.1", err)
		}
	})

	t.Run("no reconnect within the wait is refused as a lost connection, not as a version", func(t *testing.T) {
		server.Stop(t)
		for conn.IsConnected() {
			time.Sleep(10 * time.Millisecond)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		err := ConnectedRefusal(ctx, conn)
		if err == nil {
			t.Fatal("ConnectedRefusal with no server = nil, want a refusal")
		}
		if strings.Contains(err.Error(), "cannot be read") || !strings.Contains(err.Error(), "not connected") {
			t.Fatalf("ConnectedRefusal with no server = %q, want it to say the connection is not connected, not that a version cannot be read", err)
		}
		server.Start(t)
	})
}
