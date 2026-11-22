package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/roles"
	"github.com/sjawhar/envoy/internal/store"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The four rows of this file. TestAForwardWhoseOutcomeComesLateInTheDrainsWaitEndsBeforeIt names no
// symbol the stop context's rounds added, so it builds against the forwarder before them and fails
// there for the reason it names. TestAHeldRetryInItsClaimReadAtTheStopEndsBeforeTheDrainsWait and
// TestAForwardResolvedAgainOnItsWorkerAtTheStopEndsBeforeTheDrainsWait read the claim through the
// delivery configuration's claim store (cfg.claims, added at 106f8d769f), and the first of them
// checks the cut keep's line (roleKeepCutLine, added at 106f8d769f); their controls are the head
// with the delivery off the handler under context.Background() - the hold timer's, then publish's,
// as it was at 106f8d769f for a queued forward resolved again.
// TestAKeepARuleRefusesAtTheStopIsLoggedAsAFailure holds that a keep a rule refuses while the stop
// cancels its caller is logged at ERROR, and its control that one whose failure holds the message
// is the cut line at INFO; it fails at 106f8d769f, which logged both at INFO.

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

// claimReadThatStopsAnswering is a claim store (roles.ClaimStore) that answers from claims until
// stopAnswering is set, and from then on signals entered as a read starts and answers nothing:
// the read waits for its context to end, as a read over a connection whose replies are lost does.
type claimReadThatStopsAnswering struct {
	claims        roles.ClaimStore
	stopAnswering atomic.Bool
	entered       chan struct{}
	enteredOnce   sync.Once
}

func (c *claimReadThatStopsAnswering) RoleClaim(ctx context.Context, role string) (store.RoleClaim, error) {
	if !c.stopAnswering.Load() {
		return c.claims.RoleClaim(ctx, role)
	}
	c.enteredOnce.Do(func() { close(c.entered) })
	<-ctx.Done()
	return store.RoleClaim{}, ctx.Err()
}

// A held message whose retry is in its claim read when the listener stops ends with its own line
// before the drain's wait ends: the stop cuts the read, the message holds and parks, and it ends
// under the stop's deadline. The first forward leaves nothing on the wire, so the message is held
// and resolved again; from then on the claim read answers nothing and the keep after it never
// answers either, and the row stops the listener once the retry is in its read. Before, the
// retry's read and its keep ran under context.Background(), 2 s each, and the message ended after
// the drain's wait.
func TestAHeldRetryInItsClaimReadAtTheStopEndsBeforeTheDrainsWait(t *testing.T) {
	harness := newListenerDeliveryHarness(t)
	const role = "retry-in-its-claim-read"
	claimFor(t, harness, role, "ses_retry_claim_read")
	claims := &claimReadThatStopsAnswering{claims: harness.registry, entered: make(chan struct{})}
	var forwards atomic.Int32
	cfg := harness.config
	cfg.claims = claims
	cfg.keepPublish = keepThatNeverAnswers
	cfg.forwardRole = func(string, contracts.Envelope, string, time.Duration) (func() error, error) {
		if forwards.Add(1) == 1 {
			claims.stopAnswering.Store(true)
			return nil, fmt.Errorf("%w: the window was spent before the publish", bus.ErrNotSent)
		}
		return func() error { return nil }, nil
	}
	cfg = wireTestRoleForwarder(t, cfg)
	forwarder := cfg.roleForwards
	item, msg := laneMessage(t, role, "retry-in-its-claim-read")

	coreNATSDeliveryHandler(cfg)(msg)
	testwait.Receive(t, "the held message's retry to start its claim read", claims.entered)
	forwarder.stopHolding()
	waitEnds := forwarder.keepHeldNow()
	forwarder.settled(t)
	ended := time.Now()

	logs := harness.logs.String()
	if got := forwards.Load(); got != 1 {
		t.Fatalf("the lane made %d forwards, want only the first: the retry was cut in its claim read:\n%s", got, logs)
	}
	// The stop cut the retry's keep after its claim read: that is no failure of the keep's, so it
	// is the INFO line that says the message is held, and no ERROR says the keep failed on a
	// cancelled context. An ordered SIGTERM shutdown logs no ERROR
	// (TestListenerSIGTERMIsAnOrderedShutdown), and a keep the stop cut logged one.
	cut := linesIn(logs, roleKeepCutLine, map[string]any{"event_id": item.EventID})
	failed := 0
	for _, line := range linesIn(logs, roleKeepFailedLine, map[string]any{"event_id": item.EventID}) {
		if text, _ := line["error"].(string); strings.Contains(text, context.Canceled.Error()) {
			failed++
		}
	}
	if len(cut) != 1 || failed != 0 {
		t.Fatalf("%d lines say the stop cut the keep and %d ERROR lines say the keep failed on a cancelled context, want 1 and 0:\n%s", len(cut), failed, logs)
	}
	if got := endsOf(logs, item.EventID); got != 1 {
		t.Fatalf("the message ended %d times, want once: kept, or the line that it was not:\n%s", got, logs)
	}
	if ended.After(waitEnds) {
		t.Fatalf("the message ended %s after the drain's wait ended, want before it: its line comes after the process has gone on to exit:\n%s", ended.Sub(waitEnds), logs)
	}
}

// A queued forward whose resolution grew too old while it waited behind its holder's worker
// (roles.ForwardFenceAge) is resolved again on that worker, off the lane's handler, and when the
// listener stops in that resolution's claim read the message still ends with its own line before
// the drain's wait ends: the worker resolves under the forwarder's stop context, so the stop cuts
// the read, the message holds and parks, and it ends under the stop's deadline. The holder's first
// forward blocks in its publish while the second message queues behind it; the listener's clock
// then moves past the fence, the claim read stops answering and the keep never answers, and the
// first forward is let go, so the worker takes the second and resolves it again. The row stops the
// listener once that read has started. Before, the context of a delivery was chosen by its attempts,
// and this first delivery off the handler read and kept under context.Background(), 2 s each, so
// the message ended 2 s after the drain's wait.
func TestAForwardResolvedAgainOnItsWorkerAtTheStopEndsBeforeTheDrainsWait(t *testing.T) {
	harness := newListenerDeliveryHarness(t)
	const role = "resolved-again-on-its-worker"
	claimFor(t, harness, role, "ses_resolved_again")
	claims := &claimReadThatStopsAnswering{claims: harness.registry, entered: make(chan struct{})}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	entered, release := make(chan struct{}), make(chan struct{})
	var forwards atomic.Int32
	cfg := harness.config
	cfg.claims = claims
	cfg.keepPublish = keepThatNeverAnswers
	cfg.now = func() time.Time { return time.Unix(0, clock.Load()) }
	cfg.forwardRole = func(string, contracts.Envelope, string, time.Duration) (func() error, error) {
		if forwards.Add(1) == 1 {
			close(entered)
			<-release
		}
		return func() error { return nil }, nil
	}
	cfg = wireTestRoleForwarder(t, cfg)
	forwarder := cfg.roleForwards
	handler := coreNATSDeliveryHandler(cfg)
	_, firstMsg := laneMessage(t, role, "resolved-again-first")
	second, secondMsg := laneMessage(t, role, "resolved-again-second")

	handler(firstMsg)
	testwait.Receive(t, "the holder's first forward to start", entered)
	handler(secondMsg)
	clock.Add(int64(roles.ForwardFenceAge + time.Minute))
	claims.stopAnswering.Store(true)
	close(release)
	testwait.Receive(t, "the queued forward's resolution again to start its claim read", claims.entered)
	forwarder.stopHolding()
	waitEnds := forwarder.keepHeldNow()
	forwarder.settled(t)
	ended := time.Now()

	logs := harness.logs.String()
	if got := forwards.Load(); got != 1 {
		t.Fatalf("the lane made %d forwards, want only the first: the second was cut in its resolution:\n%s", got, logs)
	}
	if got := endsOf(logs, second.EventID); got != 1 {
		t.Fatalf("the second message ended %d times, want once: kept, or the line that it was not:\n%s", got, logs)
	}
	if ended.After(waitEnds) {
		t.Fatalf("the second message ended %s after the drain's wait ended, want before it: its line comes after the process has gone on to exit:\n%s", ended.Sub(waitEnds), logs)
	}
}

// A keep whose caller's context the stop cancelled is logged as cut, at INFO, only when its failure
// holds the message (keepFailureHolds): a keep a rule refuses for good in the same instant is
// logged at ERROR, as roleKeepFailedLine, since the caller gives the message up and that line is
// where the refusal's text appears. Control: the same cancelled caller with a failure that holds
// the message is the INFO line.
func TestAKeepARuleRefusesAtTheStopIsLoggedAsAFailure(t *testing.T) {
	for _, row := range []struct {
		name      string
		failure   error
		wantLine  string
		wantLevel string
	}{
		{"a refusal for good", fmt.Errorf("keep: %w", bus.ErrRefused), roleKeepFailedLine, "ERROR"},
		{"control: a failure that holds the message", context.Canceled, roleKeepCutLine, "INFO"},
	} {
		t.Run(row.name, func(t *testing.T) {
			harness := newListenerDeliveryHarness(t)
			cfg := harness.config
			cfg.keepPublish = func(context.Context, string, contracts.Envelope, string) (bus.StoredAck, error) {
				return bus.StoredAck{}, row.failure
			}
			item := listenerTestEnvelope(contracts.RoleTopicPrefix+"refused-at-the-stop", "refused-at-the-stop")
			parent, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := keepRoleMessage(parent, cfg, "refused-at-the-stop", item); err == nil {
				t.Fatal("the keep succeeded, want its failure")
			}
			logs := harness.logs.String()
			lines := linesIn(logs, row.wantLine, map[string]any{"event_id": item.EventID})
			if len(lines) != 1 || lines[0]["level"] != row.wantLevel {
				t.Fatalf("lines %v, want one %q at %s:\n%s", lines, row.wantLine, row.wantLevel, logs)
			}
		})
	}
}
