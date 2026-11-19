package bus

import (
	"testing"
	"time"
)

// A Close or a Drain made while the hub holds the handshake of the client's reconnect leaves the
// client's lock free while nats.go makes it wait: nats.go's Close and Stats wait for nats.go's
// connection lock, which the handshake holds, and the client calls them with its own lock let go
// (Close, Drain), so every caller of that lock - a probe, Connection, a request's life - is answered
// meanwhile, rather than waiting for the handshake behind a shutdown.
//
// The controls are Close calling nats.go's Close under the client's lock, and Drain reading Stats
// under it: Connection gives no answer while the handshake is held, and the row fails at
// heldAnswerWait.
func TestAStopDuringAHeldHandshakeLeavesTheClientsLockFree(t *testing.T) {
	for _, stop := range []struct {
		name string
		stop func(*Client)
	}{
		{"Close", func(c *Client) { c.Close() }},
		{"Drain", func(c *Client) { _ = c.Drain(heldAnswerWait) }},
	} {
		t.Run(stop.name, func(t *testing.T) {
			client, proxy := proxiedClient(t)
			proxy.Cut()
			hold := proxy.HoldConnectAnswers()
			awaitHeld(t, hold)
			stopped := make(chan struct{})
			go func() {
				stop.stop(client)
				close(stopped)
			}()
			// Give the stop the time to reach nats.go's lock.
			time.Sleep(50 * time.Millisecond)
			answered := make(chan struct{})
			go func() {
				client.Connection()
				close(answered)
			}()
			select {
			case <-answered:
			case <-time.After(heldAnswerWait):
				proxy.Restore()
				t.Fatalf("Connection gave no answer within %s while %s waited for the held handshake: the stop held the client's lock", heldAnswerWait, stop.name)
			}
			select {
			case <-hold.Ended():
				t.Fatalf("Connection answered only once the held handshake had ended")
			default:
			}
			proxy.Restore()
			select {
			case <-stopped:
			case <-time.After(heldAnswerWait):
				t.Fatalf("%s had not returned %s after the hold ended", stop.name, heldAnswerWait)
			}
		})
	}
}
