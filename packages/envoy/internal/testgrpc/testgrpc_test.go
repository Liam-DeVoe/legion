package testgrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// TestTheStopWaitsOnNoPeerInItsHandshake: a peer that connects to the stand-in's port and sends
// nothing, as another program's client that reached the port does, holds no stop. Serve's stop
// returns with that peer still connected, and closes its connection, though the server's own
// connection timeout, an hour here, holds grpc's Stop alone that long. A call of the test's own is
// served as before. The peer stays silent only once the server has begun its handshake with it -
// the server's HTTP/2 settings, which it sends before it reads the client's preface - so the stop
// finds the peer in that handshake, where Stop waits on it, rather than not yet accepted.
func TestTheStopWaitsOnNoPeerInItsHandshake(t *testing.T) {
	server := grpc.NewServer(grpc.ConnectionTimeout(time.Hour))
	grpc_health_v1.RegisterHealthServer(server, health.NewServer())
	addr, stop, err := serve(server)
	if err != nil {
		t.Fatal(err)
	}
	// The stop runs again at the test's end, whatever failed before it ran: by then the deferred
	// closes below have ended the silent peer, so even Stop alone returns.
	t.Cleanup(stop)

	client, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	answer, err := grpc_health_v1.NewHealthClient(client).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || answer.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("the stand-in answered the test's own call %v, %v; want SERVING", answer, err)
	}

	peer, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := peer.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(peer, make([]byte, 1)); err != nil {
		t.Fatalf("the stand-in never began its handshake with the silent peer: %v", err)
	}

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Minute):
		t.Fatal("the stand-in's stop had not returned a minute after it began, with a silent peer in its handshake")
	}
	if err := peer.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, peer); errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("the stand-in's stop left the silent peer's connection open")
	}
}
