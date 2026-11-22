package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/store"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// These rows name no symbol this change's stop context added, so each builds against the forwarder
// before it and fails there for the reason it names.

// keepThatNeverAnswers is a keep whose publish NATS never answers: it waits for its context to
// end and fails with the context's error, as a keep over a connection whose replies are lost does.
func keepThatNeverAnswers(ctx context.Context, _ string, _ contracts.Envelope, _ string) (bus.StoredAck, error) {
	<-ctx.Done()
	return bus.StoredAck{}, ctx.Err()
}

// endsOf counts the lines that end the held message eventID: kept on its role's kept lane, or the
// ERROR that it was not.
func endsOf(logs, eventID string) int {
	return countLogged(logs, keptLine, eventID) + countLogged(logs, roleHeldUnkeptLine, eventID)
}

// A forward whose outcome comes late in the drain's wait - its window ends 100 ms before that wait
// does, with no acknowledgement - ends with its own line before the wait ends. The listener's
// read of the deployed stream, which can end such a forward for good, is not made once the
// listener has stopped, so the message holds, parks and ends under the stop's deadline at once.
// The read goes through a proxy whose server replies are frozen, so a read that is made takes its
// whole window. Before, finish read the stream for a fresh 2 s after the forward's own wait, and
// the message's end came after the drain's wait had ended and the process had gone on to exit.
func TestAForwardWhoseOutcomeComesLateInTheDrainsWaitEndsBeforeIt(t *testing.T) {
	harness := newListenerDeliveryHarness(t)
	const role = "outcome-late-in-the-drain"
	claimFor(t, harness, role, "ses_outcome_late")
	proxy := testnats.StartProxy(t, harness.client.Conn.ConnectedUrl())
	slow, err := bus.ConnectOwningStream([]string{proxy.URL()}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect through the proxy: %v", err)
	}
	t.Cleanup(slow.Close)
	entered := make(chan struct{})
	waitUntil := make(chan time.Time, 1)
	var forwards atomic.Int32
	cfg := harness.config
	cfg.client = slow
	cfg.keepPublish = keepThatNeverAnswers
	cfg.forwardRole = func(string, contracts.Envelope, string, time.Duration) (func() error, error) {
		if forwards.Add(1) > 1 {
			return func() error { return nil }, nil
		}
		return func() error {
			close(entered)
			time.Sleep(time.Until(<-waitUntil))
			return fmt.Errorf("%w: %w", bus.ErrForwardNotStored, natsgo.ErrTimeout)
		}, nil
	}
	cfg = wireTestRoleForwarder(t, cfg)
	forwarder := cfg.roleForwards
	item, msg := laneMessage(t, role, "outcome-late-in-the-drain")

	coreNATSDeliveryHandler(cfg)(msg)
	<-entered
	proxy.FreezeServerReplies()
	forwarder.stopHolding()
	waitEnds := forwarder.keepHeldNow()
	waitUntil <- waitEnds.Add(-100 * time.Millisecond)
	forwarder.settled(t)
	ended := time.Now()

	logs := harness.logs.String()
	if got := endsOf(logs, item.EventID); got != 1 {
		t.Fatalf("the message ended %d times, want once: kept, or the line that it was not:\n%s", got, logs)
	}
	if ended.After(waitEnds) {
		t.Fatalf("the message ended %s after the drain's wait ended, want before it: its line comes after the process has gone on to exit:\n%s", ended.Sub(waitEnds), logs)
	}
}

// A held message whose retry is in its claim read when the listener stops ends with its own line
// before the drain's wait ends: the stop cuts the read, the message holds and parks, and it ends
// under the stop's deadline. The claim is read through a proxy whose server replies are frozen
// after the first forward, which left nothing on the wire, so the retry's read waits for its
// whole window; the keep after it never answers either. The row stops the listener once the
// retry's read has gone out. Before, the retry's read and its keep ran under
// context.Background(), 2 s each, and the message ended after the drain's wait.
func TestAHeldRetryInItsClaimReadAtTheStopEndsBeforeTheDrainsWait(t *testing.T) {
	harness := newListenerDeliveryHarness(t)
	const role = "retry-in-its-claim-read"
	claimFor(t, harness, role, "ses_retry_claim_read")
	proxy := testnats.StartProxy(t, harness.client.Conn.ConnectedUrl())
	slowConn := testnats.Connect(t, proxy.URL())
	slowRegistry, err := store.Open(t.Context(), slowConn, store.WithReplicas(1))
	if err != nil {
		t.Fatalf("open the registry the retry reads the claim through: %v", err)
	}
	var sentBeforeTheRetry atomic.Uint64
	var forwards atomic.Int32
	cfg := harness.config
	cfg.registry = slowRegistry
	cfg.keepPublish = keepThatNeverAnswers
	cfg.forwardRole = func(string, contracts.Envelope, string, time.Duration) (func() error, error) {
		if forwards.Add(1) == 1 {
			proxy.FreezeServerReplies()
			sentBeforeTheRetry.Store(slowConn.Stats().OutMsgs)
			return nil, fmt.Errorf("%w: the window was spent before the publish", bus.ErrNotSent)
		}
		return func() error { return nil }, nil
	}
	cfg = wireTestRoleForwarder(t, cfg)
	forwarder := cfg.roleForwards
	item, msg := laneMessage(t, role, "retry-in-its-claim-read")

	coreNATSDeliveryHandler(cfg)(msg)
	testwait.WithinPolling(t, 20*time.Second, time.Millisecond, "the retry's claim read to go out",
		func() bool { return forwards.Load() == 1 && slowConn.Stats().OutMsgs > sentBeforeTheRetry.Load() })
	forwarder.stopHolding()
	waitEnds := forwarder.keepHeldNow()
	forwarder.settled(t)
	ended := time.Now()

	logs := harness.logs.String()
	if got := forwards.Load(); got != 1 {
		t.Fatalf("the lane made %d forwards, want only the first: the retry was cut in its claim read:\n%s", got, logs)
	}
	if got := endsOf(logs, item.EventID); got != 1 {
		t.Fatalf("the message ended %d times, want once: kept, or the line that it was not:\n%s", got, logs)
	}
	if ended.After(waitEnds) {
		t.Fatalf("the message ended %s after the drain's wait ended, want before it: its line comes after the process has gone on to exit:\n%s", ended.Sub(waitEnds), logs)
	}
}
