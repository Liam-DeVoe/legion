package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A delete of a gone session's consumer whose session-consumer connection nats.go has closed, with
// no replacement connected yet, holds no session lock while the replacement is dialled: its first
// consumer call answers the loss at once (bus.ErrConnectionLost, the client's connForRequest), the
// delete releases the session's lock and waits for the connection (relockAfterReconnect), whose
// wait dials the replacement (bus.Reconnected), and it writes no resume record and deletes nothing
// meanwhile. Once the connection is back the delete takes the lock again, records the consumer's
// floor and deletes it, inside its own budget, with no "session consumer delete failed" line.
//
// The order is made certain rather than likely: the proxy refuses every dial from its cut on and the
// connection is closed after the cut (bus.SessionConsumers.CloseConnection), so no replacement can
// connect until the row restores the proxy; the row holds the session's lock until the delete waits
// for it (its second user), so the delete takes the lock from the row, and the lock then has no user
// only once the delete has released it. A delete that dialled under the lock would release it only
// once its own budget ended with its dial still refused, and answer that it deleted nothing.
//
// The control is this row with bus's connForRequest `!conn.IsClosed()` restored, as at b5dc181071:
// the delete dials the replacement through the cut proxy holding the session's lock, logs
// "session consumer delete failed ... context deadline exceeded" when its budget ends, and leaves
// the consumer standing.
func TestADeleteWhoseConnectionIsClosedReleasesTheSessionsLockAndDeletesOnceItIsBack(t *testing.T) {
	rig := newSweepRig(t)
	proxy := testnats.StartProxy(t, testnats.URL(t))
	consumers, err := bus.ConnectSessionConsumers([]string{proxy.URL()}, 0, nil)
	if err != nil {
		t.Fatalf("connect the session consumers through the proxy: %v", err)
	}
	// The rig's own session-consumer connection is replaced by this one, which the rig closes as
	// it closes its own (sessionDelivery.close); the one it replaces is closed here.
	t.Cleanup(rig.deps.consumers.consumers.Close)
	rig.deps.consumers.consumers = consumers
	t.Cleanup(proxy.Restore)
	const sessionID = "ses_delete_closed_connection"
	rig.register(t, sessionID)
	rig.now.advance(sweepLapse)
	rig.requireState(t, sessionID, session.Gone)
	ctx, cancel := context.WithTimeout(context.Background(), testnats.Remaining(t))
	defer cancel()

	proxy.Cut()
	consumers.CloseConnection()
	locks := &rig.deps.consumers.locks
	held := locks.lock(sessionID)
	deleted := make(chan bool, 1)
	go func() { deleted <- rig.deps.consumers.deleteConsumer(ctx, rig.deps, sessionID, nil) }()
	awaitLockUsers(t, locks, sessionID, 2)
	held()
	for lockUsers(locks, sessionID) != 0 {
		if testnats.Remaining(t) <= 0 {
			t.Fatal("the delete still held the session's lock at the test's deadline")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case gone := <-deleted:
		t.Fatalf("the delete answered %t before the connection was back: it released the session's lock only at its end; log: %s", gone, rig.harness.logs.String())
	default:
	}
	rig.requireConsumer(t, sessionID)
	if _, err := rig.harness.client.LastStoredMessage(ctx, bus.SessionResumeSubject(sessionID)); !errors.Is(err, natsgo.ErrMsgNotFound) {
		t.Fatalf("the read of %s's resume record while the delete waited = %v, want none stored", sessionID, err)
	}

	proxy.Restore()
	var gone bool
	select {
	case gone = <-deleted:
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the delete was still waiting at the test's deadline")
	}
	if !gone {
		t.Fatalf("the delete made once the connection was back reported the consumer standing; log: %s", rig.harness.logs.String())
	}
	rig.requireNoConsumer(t, sessionID)
	if _, err := rig.harness.client.LastStoredMessage(ctx, bus.SessionResumeSubject(sessionID)); err != nil {
		t.Fatalf("the read of %s's resume record after the delete = %v, want the record it wrote", sessionID, err)
	}
	if logs := rig.harness.logs.String(); strings.Contains(logs, "session consumer delete failed") {
		t.Fatalf("the delete logged a failure: %s", logs)
	}
}
