package main

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A handover of a role's kept lane made while the hub holds the handshake of the listener's main
// client's reconnect gives the role's guard up within the probe's budget, while the handshake is
// still held: each pass asks the main client first whether its connection is up
// (bus.Client.LostNow), the handover returns the loss, and drainKeptRole waits for the connection
// with the guard let go (bus.Reconnected), so another handover of the role, or the sweep's test of
// it, is not held behind a handshake. Once the proxy is restored the handover runs again under the
// guard and ends.
//
// The control is the handover's pass made without asking the main client first: its lane read
// waits for the reconnect with the guard held, and the row fails at heldHandshakeWait.
func TestAHandoverDuringAHeldMainClientHandshakeGivesTheRolesGuardUp(t *testing.T) {
	proxy := testnats.StartProxy(t, testnats.URL(t))
	client, err := bus.ConnectOwningStream([]string{proxy.URL()}, bus.WithReplicas(1), bus.WithConnectTimeout(testnats.Remaining(t)))
	if err != nil {
		t.Fatalf("connect the main client through the proxy: %v", err)
	}
	t.Cleanup(client.Close)
	t.Cleanup(proxy.Restore)
	harness := newListenerDeliveryHarnessOn(t, client)
	cfg := harness.config
	const role = "kept-held-handshake"

	proxy.Cut()
	hold := proxy.HoldConnectAnswers()
	select {
	case <-hold.Held():
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("no reconnect's CONNECT of the main client reached the server through the hold by the test's deadline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testnats.Remaining(t))
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- drainKeptRole(ctx, cfg, role, time.Now) }()

	// First the handover takes the guard: a test that took it first would find it free whatever
	// the handover does. The handover holds it for at least the probe's budget, which 5 ms polls
	// see.
	taken := false
	for deadline := time.Now().Add(5 * time.Second); !taken; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			proxy.Restore()
			t.Fatalf("the handover had not taken role %s's guard 5 s after it began, so the row shows nothing", role)
		}
		if release, free := cfg.roleKeepDrains.tryHold(role); free {
			release()
		} else {
			taken = true
		}
	}
	free := false
	for deadline := time.Now().Add(heldHandshakeWait); !free; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			proxy.Restore()
			t.Fatalf("the handover still held role %s's guard %s into the main client's held handshake: it waited for that client's connection with the guard held", role, heldHandshakeWait)
		}
		if release, taken := cfg.roleKeepDrains.tryHold(role); taken {
			release()
			free = true
		}
	}
	select {
	case <-hold.Ended():
		t.Fatal("the handover gave the role's guard up only once the main client's held handshake had ended")
	default:
	}

	proxy.Restore()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("the handover made once the main client was back answered %v", err)
		}
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the handover had not ended by the test's deadline, though the main client was back")
	}
}
