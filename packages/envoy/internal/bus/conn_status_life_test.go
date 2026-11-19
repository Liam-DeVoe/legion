package bus

// Rows for how a request's life ends at a drop (requestLife) and for a close answer read during a
// held handshake (closedUnder).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/testnats"
)

// withLifeListenGate makes a client whose lives' goroutines run gate before they register their
// status listener ("register") and before their first wait ("select") (Client.lifeListenGate).
func withLifeListenGate(gate func(stage string)) ConnectOption {
	return clientOption(func(o *connectOpts) { o.lifeListenGate = gate })
}

// stageHold holds the first life listener goroutine that reaches stage until release.
type stageHold struct {
	stage       string
	once        sync.Once
	held        chan struct{}
	released    chan struct{}
	releaseOnce sync.Once
}

func newStageHold(stage string) *stageHold {
	return &stageHold{stage: stage, held: make(chan struct{}), released: make(chan struct{})}
}

func (h *stageHold) gate(stage string) {
	if stage != h.stage {
		return
	}
	first := false
	h.once.Do(func() { first = true; close(h.held) })
	if first {
		<-h.released
	}
}

func (h *stageHold) release() { h.releaseOnce.Do(func() { close(h.released) }) }

// inFlight starts a request on client whose send waits for its bound context to end, and returns
// the channel its answer arrives on once send has been called.
func inFlight(t *testing.T, ctx context.Context, client *Client) <-chan error {
	t.Helper()
	started := make(chan struct{})
	answered := make(chan error, 1)
	go func() {
		_, err := Request(ctx, client, func(bound context.Context) (struct{}, error) {
			close(started)
			<-bound.Done()
			return struct{}{}, context.Cause(bound)
		})
		answered <- err
	}()
	select {
	case <-started:
	case err := <-answered:
		t.Fatalf("the request answered %v before it was sent", err)
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the request was never sent")
	}
	return answered
}

func requireLostAtDrop(t *testing.T, answered <-chan error, what string) {
	t.Helper()
	select {
	case err := <-answered:
		if !errors.Is(err, ErrConnectionLost) {
			t.Fatalf("%s: the request in flight answered %v, want ErrConnectionLost", what, err)
		}
	case <-time.After(heldAnswerWait):
		t.Fatalf("%s: the request in flight gave no answer within %s: its life outlived the drop", what, heldAnswerWait)
	}
}

// A close answer read on the session consumers' kind of client (withLossAtOnce) while nats.go is in
// a reconnect's handshake answers within the probe's budget: closedUnder reads the connection's
// status by the probe alone on that client. A forced reconnect (the token keeper's) answers the
// pending request ErrConnectionClosed and starts the reconnect at once, and the hub holding the
// CONNECT holds nats.go's lock. The row holds the life's end (withLifeEndGate), so finish reaches
// closedUnder, as it does whenever the request's answer is read before the life has ended.
//
// The control is closedUnder reading conn.Status() before it asks which client it is on: the row
// gives no answer within heldAnswerWait.
func TestACloseAnswerDuringAHeldHandshakeAnswersAtOnce(t *testing.T) {
	gate := newLifeEndHold()
	client, proxy := proxiedClient(t, withLossAtOnce(), withLifeEndGate(gate.gate))
	t.Cleanup(gate.release)
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	_, finish := client.bindRequest(ctx)
	conn := client.Connection()
	hold := proxy.HoldConnectAnswers()
	if err := conn.ForceReconnect(); err != nil {
		t.Fatalf("force a reconnect: %v", err)
	}
	awaitHeld(t, hold)
	select {
	case <-gate.held:
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the forced reconnect never reached the end of the request's life")
	}
	answered := make(chan error, 1)
	go func() { answered <- finish(natsgoClosedAnswer) }()
	select {
	case err := <-answered:
		if !errors.Is(err, ErrConnectionLost) {
			t.Fatalf("the close answer read during the held handshake answered %v, want ErrConnectionLost", err)
		}
	case <-time.After(heldAnswerWait):
		proxy.Restore()
		t.Fatalf("a close answer read during the held handshake gave no answer within %s: closedUnder waited for nats.go's connection lock, which the handshake holds", heldAnswerWait)
	}
	select {
	case <-hold.Ended():
		t.Fatal("the close answer was given only once the held handshake had ended")
	default:
	}
}

// A drop and reconnect that land between the life's begin and its goroutine's registration of its
// status listener end the life: the goroutine wakes at the client's connection events and reads a
// reconnect count other than the one the life began at. A request sent on the dropped socket, whose
// reply the drop took (nats.go fails no pending request at a drop), would otherwise wait out its
// whole deadline. The gate holds the goroutine before it registers, as a goroutine not yet
// scheduled is held on a loaded box.
//
// The control is the goroutine without its reconnect-count check: the request gives no answer
// after the listener registers.
func TestALifeEndsAtADropAndReconnectBeforeItsListenerRegisters(t *testing.T) {
	hold := newStageHold("register")
	client, proxy := proxiedClient(t, withLifeListenGate(hold.gate))
	t.Cleanup(hold.release)
	conn := client.Connection()
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	before := conn.Stats().Reconnects
	answered := inFlight(t, ctx, client)
	select {
	case <-hold.held:
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the life's listener goroutine never started")
	}
	proxy.Cut()
	waitForNatsStatus(t, conn, "reconnecting after the cut", func(s nats.Status) bool { return s != nats.CONNECTED })
	proxy.Restore()
	waitForNatsStatus(t, conn, "connected again", func(s nats.Status) bool { return s == nats.CONNECTED })
	if after := conn.Stats().Reconnects; after <= before {
		t.Fatalf("nats.go's reconnect count is %d, want above %d", after, before)
	}
	hold.release()
	requireLostAtDrop(t, answered, "a drop and reconnect before the listener registered")
}

// A life ends at a drop whose report nats.go took back: sendStatusEvent, sending a second report of
// a status the listener is registered for, first receives from the channel when it holds a value
// and deletes the listener (nats.go:6684-6690). A drop (RECONNECTING) followed by a reconnect
// attempt whose dial succeeds and whose handshake fails (RECONNECTING again, nats.go:3491) - a
// proxy that accepts and closes, a hub refusing the credential - while the life's goroutine has not
// yet received the first report leaves its channel empty and the listener deleted. The goroutine
// also wakes at the client's connection events (ConnectionChanged) and reads the connection down.
// The gate holds the goroutine between its registration and its first wait, as a goroutine not yet
// scheduled is held.
//
// The control is the goroutine waiting on its listener alone: the life outlives the drop.
func TestALifeEndsAtADropWhoseReportNatsGoTookBack(t *testing.T) {
	hold := newStageHold("select")
	client, proxy := proxiedClient(t, withLifeListenGate(hold.gate))
	t.Cleanup(hold.release)
	conn := client.Connection()
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	before := conn.Stats().Reconnects
	answered := inFlight(t, ctx, client)
	select {
	case <-hold.held:
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the life's listener goroutine never reached its wait")
	}
	proxy.Cut()
	// nats.go counts a reconnect once the attempt's dial succeeds (nats.go:3482), and the proxy's
	// cut closes the connection it accepted, so the handshake fails and nats.go reports
	// RECONNECTING again under the same lock hold; Stats waits for that hold.
	deadline := time.Now().Add(testnats.Remaining(t))
	for conn.Stats().Reconnects <= before {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect attempt reached the cut proxy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	proxy.Restore()
	waitForNatsStatus(t, conn, "connected again", func(s nats.Status) bool { return s == nats.CONNECTED })
	hold.release()
	requireLostAtDrop(t, answered, "a drop whose report nats.go took back")
}
