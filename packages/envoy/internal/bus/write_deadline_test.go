package bus

import (
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testnats"
)

// A client made with WithWriteDeadline closes within its deadline and a second while a write to a
// socket that takes no byte holds nats.go's connection lock. The link freezes and the write stays
// parked in its socket (testnats.HandshakeHold.HoldWriteLock), and the link is never thawed: only
// the deadline ends the write, and Close, which waits for the lock, then takes it. Without the
// option the write would hold the lock, and Close with it, for nats.go's default minute.
func TestAClientWithAWriteDeadlineClosesWithinItWhileAWriteIsFrozen(t *testing.T) {
	const deadline = 2 * time.Second
	hold := testnats.StartHandshakeHold(t, testnats.URL(t))
	client, err := ConnectOwningStream([]string{hold.URL()}, WithName(t.Name()), WithWriteDeadline(deadline))
	if err != nil {
		t.Fatalf("connect through the hold: %v", err)
	}
	t.Cleanup(client.Close)
	conn := client.Connection()
	writeHeld := time.Now()
	hold.HoldWriteLock(t, conn)
	testnats.RequireLockHeld(t, conn)

	closing := time.Now()
	closed := make(chan struct{})
	go func() {
		client.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(deadline + time.Second):
		t.Fatalf("Close did not return within the write deadline (%s) and a second, while a write was frozen: the deadline did not end the write", deadline)
	}
	if !conn.IsClosed() {
		t.Fatal("Close returned, but the connection has not closed")
	}
	t.Logf("Close returned %s after its call and %s after the frozen write began", time.Since(closing).Round(time.Millisecond), time.Since(writeHeld).Round(time.Millisecond))
}
