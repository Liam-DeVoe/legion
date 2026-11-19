package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// heldAnswerWait is how long a row waits for an answer that must come while a handshake is held
// (testnats.Proxy.HoldConnectAnswers). The hold lasts until the row restores it, and the client's
// connect timeout is above the row's own deadline (WithConnectTimeout), so an answer that waits for
// nats.go's connection lock never comes while it lasts; one read off the record comes in
// microseconds. The wait bounds a row that fails, not the answer of one that passes.
const heldAnswerWait = 10 * time.Second

// proxiedClient is a client of its own through a proxy of its own, whose dials and handshakes
// nats.go's own timeout does not end before the test's deadline (WithConnectTimeout), so a held
// handshake lasts until the row restores the proxy. The proxy is restored before the client
// closes, since nats.go's Close waits for its connection lock, which a held handshake holds.
func proxiedClient(t *testing.T, options ...ConnectOption) (*Client, *testnats.Proxy) {
	t.Helper()
	proxy := testnats.StartProxy(t, testnats.URL(t))
	client, err := Connect([]string{proxy.URL()}, append([]ConnectOption{WithConnectTimeout(testnats.Remaining(t))}, options...)...)
	if err != nil {
		t.Fatalf("connect through the proxy: %v", err)
	}
	t.Cleanup(client.Close)
	t.Cleanup(proxy.Restore)
	return client, proxy
}

// recordOf is the record of the status of client's connection now (Client.connStatus).
func recordOf(client *Client) *connStatus {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.statusLocked()
}

// waitForNatsStatus waits until nats.go's own status of conn satisfies want, reading it as any
// caller does, under nats.go's lock.
func waitForNatsStatus(t *testing.T, conn *nats.Conn, what string, want func(nats.Status) bool) {
	t.Helper()
	deadline := time.Now().Add(testnats.Remaining(t))
	for !want(conn.Status()) {
		if time.Now().After(deadline) {
			t.Fatalf("nats.go's status of the connection was still %v at the test's deadline, want %s", conn.Status(), what)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitHeld waits until hold holds a client's handshake.
func awaitHeld(t *testing.T, hold *testnats.ConnectHold) {
	t.Helper()
	select {
	case <-hold.Held():
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("no reconnect's CONNECT reached the server through the hold by the test's deadline")
	}
}

// awaitRecord waits until the record reads a status want accepts, waking at each change it
// applies, and returns that status and its connected run. The record trails nats.go's report by
// the moment its goroutine takes to apply it (connStatus), so a row waits for that, not a time.
func awaitRecord(t *testing.T, record *connStatus, what string, want func(nats.Status) bool) (nats.Status, *connEpoch) {
	t.Helper()
	deadline := time.After(testnats.Remaining(t))
	for {
		record.mu.Lock()
		status, epoch, changed := record.statusLocked(), record.epoch, record.changed
		record.mu.Unlock()
		if want(status) {
			return status, epoch
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("the record still read %v at the test's deadline, want %s", status, what)
		}
	}
}

// The record of a connection's status (connStatus) follows each change nats.go reports, and is
// read without nats.go's connection lock: the drop, a reconnect's handshake that the hub holds
// midway, the reconnect (a new connected run), and the close. During the held handshake the record
// is read once the hold holds the CONNECT and before the row restores the proxy, so a read that
// waited for nats.go's lock would answer only after the restore, and connected.
//
// The control is in the row: nats.go's own IsConnected, asked once the handshake is held, answers
// only once the row has restored the proxy, and answers true, the handshake's end; asked without
// the lock it would have answered false at once, as the record does.
func TestTheConnectionStatusRecordReadsEachChangeWithoutNatsGosLock(t *testing.T) {
	client, proxy := proxiedClient(t)
	conn := client.Connection()
	record := recordOf(client)
	_, first := awaitRecord(t, record, "CONNECTED", func(s nats.Status) bool { return s == nats.CONNECTED })
	if first == nil {
		t.Fatal("the record reads a connected client CONNECTED with no connected run")
	}

	proxy.Cut()
	if _, epoch := awaitRecord(t, record, "down after the cut", func(s nats.Status) bool { return !statusUp(s) }); epoch != nil {
		t.Fatalf("after the drop the record reads a connected run %v, want none", epoch)
	}
	select {
	case <-first.down:
	default:
		t.Fatal("after the drop the record's first connected run had not ended")
	}

	hold := proxy.HoldConnectAnswers()
	awaitHeld(t, hold)
	locked := make(chan bool, 1)
	go func() { locked <- conn.IsConnected() }()
	status, epoch := record.read()
	if statusUp(status) || epoch != nil {
		t.Fatalf("during the held handshake the record reads %v (connected run %v), want it down", status, epoch)
	}
	select {
	case answer := <-locked:
		t.Fatalf("nats.go's IsConnected answered %t during the held handshake: the hold did not hold nats.go's lock, so the row shows nothing", answer)
	default:
	}

	proxy.Restore()
	select {
	case answer := <-locked:
		if !answer {
			t.Fatal("nats.go's IsConnected, asked during the held handshake, answered false: it did not wait for the handshake's end")
		}
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("nats.go's IsConnected had not answered by the test's deadline, after the hold ended")
	}
	_, second := awaitRecord(t, record, "CONNECTED after the reconnect", func(s nats.Status) bool { return s == nats.CONNECTED })
	if second == nil || second == first {
		t.Fatalf("after the reconnect the record reads the connected run %v (the first %v), want a new one", second, first)
	}

	client.Close()
	if _, epoch := awaitRecord(t, record, "CLOSED", func(s nats.Status) bool { return s == nats.CLOSED }); epoch != nil {
		t.Fatalf("after the close the record reads a connected run %v, want none", epoch)
	}
	select {
	case <-second.down:
	default:
		t.Fatal("after the close the record's connected run had not ended")
	}
}

// holdRecord keeps the record of client's connection from applying anything its sources tell it
// (connStatus.hold) until the test ends, and returns the record. A row reads nats.go and the record
// apart with it: what nats.go has done, the record has not yet read.
func holdRecord(t *testing.T, client *Client) *connStatus {
	t.Helper()
	record := recordOf(client)
	hold := make(chan struct{})
	record.mu.Lock()
	record.hold = hold
	record.mu.Unlock()
	t.Cleanup(func() { close(hold) })
	return record
}

// closeUnderHeldRecord cuts the proxy, so the client's recovery cannot replace the connection, and
// closes the connection with the record held, and fails unless the record still reads it connected
// while nats.go reads it closed.
func closeUnderHeldRecord(t *testing.T, client *Client, proxy *testnats.Proxy) *nats.Conn {
	t.Helper()
	record := holdRecord(t, client)
	conn := client.Connection()
	proxy.Cut()
	conn.Close()
	if status, _ := record.read(); status != nats.CONNECTED || !conn.IsClosed() {
		t.Fatalf("with the record held, the record reads %v and nats.go %v, want CONNECTED and CLOSED", status, conn.Status())
	}
	return conn
}

// A request made once nats.go has closed the connection, while the record of its status still
// reads it connected, is never sent when no life of the connection runs: a request that would
// begin a life confirms the connection in nats.go's own state (requestLife, upNow), which nats.go
// set CLOSED before Close returned, so Request answers ErrConnectionLost, unsent, without calling
// send. The row holds the record (holdRecord), so it reads connected however fast it would follow.
//
// The control is this row with requestLife beginning a life on the record's word, without upNow:
// the request is sent, and send is called.
func TestARequestMadeAfterNatsGoClosedTheConnectionIsNeverSent(t *testing.T) {
	client, proxy := proxiedClient(t)
	client.mu.Lock()
	end := client.takeLifeLocked()
	client.mu.Unlock()
	client.endLifeNow(end)
	closeUnderHeldRecord(t, client, proxy)
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	sends := 0
	_, err := Request(ctx, client, func(context.Context) (struct{}, error) {
		sends++
		return struct{}{}, nil
	})
	if !errors.Is(err, ErrConnectionLost) || !requestUnsent(err) || sends != 0 {
		t.Fatalf("a request made after nats.go closed the connection answered %v (unsent: %t) after %d sends, want ErrConnectionLost, unsent, and no send", err, requestUnsent(err), sends)
	}
}

// A request made on a life already running, once nats.go has closed the connection and while the
// record still reads it connected, is handed to nats.go, which refuses it as closed before it
// writes anything (a publish on a closed connection answers nats.ErrConnectionClosed under
// nats.go's lock), and its answer is ErrConnectionLost: the close answer of a connection nats.go
// reads closed is lost (closedUnder, which confirms the record's connected in nats.go's state). It
// is not marked unsent, since a close answer does not say whether the request went out before the
// close (requestLife says what that costs a stored publish). The row holds the record.
//
// The control is this row with closedUnder taking the record's connected without nats.go's state:
// the answer is nats.go's "connection closed", which a caller reads as lasting.
func TestARequestOnARunningLifeAfterNatsGoClosedTheConnectionIsLost(t *testing.T) {
	client, proxy := proxiedClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	if _, err := Request(ctx, client, func(context.Context) (struct{}, error) { return struct{}{}, nil }); err != nil {
		t.Fatalf("the request that begins the life answered %v", err)
	}
	conn := closeUnderHeldRecord(t, client, proxy)
	var sent error
	_, err := Request(ctx, client, func(context.Context) (struct{}, error) {
		sent = conn.Publish("conn.status.after.close", nil)
		return struct{}{}, sent
	})
	if !errors.Is(sent, nats.ErrConnectionClosed) {
		t.Fatalf("nats.go answered the publish on its closed connection %v, want %v: it wrote it", sent, nats.ErrConnectionClosed)
	}
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("a request on a running life after nats.go closed the connection answered %v, want ErrConnectionLost", err)
	}
}

// natsgoClosedAnswer is the answer nats.go gives a request its connection's close or forced
// reconnect cut off (clearPendingRequestCalls closes the request's reply channel).
var natsgoClosedAnswer = nats.ErrConnectionClosed

// A request that a forced reconnect cut off is lost however late its answer is read. The keeper of
// a service's token reconnects with ForceReconnect to present a new token (natscred), which answers
// every pending request nats.ErrConnectionClosed and reconnects in place; a request whose answer
// is read only once the connection is connected again, and before the end of its life reached it,
// read the connection connected and was answered nats.go's close, which every caller reads as
// lasting. The request's life began at nats.go's reconnect count then (requestLife), the forced
// reconnect counted one more before it reported the connection connected, and closedUnder reads
// the count, so the answer is ErrConnectionLost.
//
// The order is made certain: the gate holds the life's end (withLifeEndGate) until the row has
// read the answer, and the row reads it only once nats.go and the record of the connection's status
// both read it connected again, so the status alone says nothing of the forced reconnect.
//
// The control is this row with closedUnder not comparing the reconnect count (bindRequest's finish
// passing nil): the answer is nats.go's "connection closed".
func TestARequestAForcedReconnectCutOffIsLostOnceTheConnectionIsBack(t *testing.T) {
	gate := newLifeEndHold()
	client, err := Connect([]string{testnats.URL(t)}, withLifeEndGate(gate.gate))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	t.Cleanup(gate.release)
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	_, finish := client.bindRequest(ctx)
	conn := client.Connection()
	if err := conn.ForceReconnect(); err != nil {
		t.Fatalf("force a reconnect: %v", err)
	}
	select {
	case <-gate.held:
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the forced reconnect never reached the end of the request's life")
	}
	waitForNatsStatus(t, conn, "connected again after the forced reconnect", func(s nats.Status) bool { return s == nats.CONNECTED })
	awaitRecord(t, recordOf(client), "CONNECTED again after the forced reconnect", func(s nats.Status) bool { return s == nats.CONNECTED })
	got := finish(natsgoClosedAnswer)
	gate.release()
	if !errors.Is(got, ErrConnectionLost) {
		t.Fatalf("a request a forced reconnect answered %v, read once the connection was connected again and before its life ended, answered %v, want ErrConnectionLost", natsgoClosedAnswer, got)
	}
}

// The same answer read while the connection is still connected and has not reconnected since the
// request's life began is a handle's of a connection the client replaced, and stays nats.go's: a
// caller reads it as lasting rather than retrying it for ever. With the reconnect count not
// compared (the control above), this row still passes; with every close answer read as lost, it
// fails.
func TestACloseAnswerInTheRequestsOwnRunStaysNatsGos(t *testing.T) {
	client, err := Connect([]string{testnats.URL(t)})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	_, finish := client.bindRequest(ctx)
	if got := finish(natsgoClosedAnswer); errors.Is(got, ErrConnectionLost) || !errors.Is(got, nats.ErrConnectionClosed) {
		t.Fatalf("a close answer read in the request's own connected run answered %v, want nats.go's %v", got, natsgoClosedAnswer)
	}
}

// A request through Request made while the hub holds the handshake of its connection's reconnect
// is not sent, and answers ErrConnectionLost at once, unsent, while the handshake is still held, on
// any client: its life is read off the record of the connection's status under the client's mutex
// (requestLife), which waits for no lock of nats.go's, where a read of nats.go's own state there
// waited for the whole handshake and held the client's mutex, and with it every caller of the
// client, that long.
//
// The control is this row with requestLife reading conn.IsConnected() under the client's mutex in
// place of the record: the request gives no answer while the handshake is held, and the row fails
// at heldAnswerWait.
func TestARequestDuringAHeldHandshakeAnswersAtOnce(t *testing.T) {
	client, proxy := proxiedClient(t)
	proxy.Cut()
	hold := proxy.HoldConnectAnswers()
	awaitHeld(t, hold)
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	sends := 0
	answered := make(chan error, 1)
	go func() {
		_, err := Request(ctx, client, func(context.Context) (struct{}, error) {
			sends++
			return struct{}{}, nil
		})
		answered <- err
	}()
	var err error
	select {
	case err = <-answered:
	case <-time.After(heldAnswerWait):
		proxy.Restore()
		t.Fatalf("a request made during the held handshake gave no answer within %s: it waited for nats.go's connection lock, which the handshake holds", heldAnswerWait)
	}
	if !errors.Is(err, ErrConnectionLost) || !requestUnsent(err) || sends != 0 {
		t.Fatalf("a request made during the held handshake answered %v (unsent: %t) after %d sends, want ErrConnectionLost, unsent, and no send", err, requestUnsent(err), sends)
	}
	select {
	case <-hold.Ended():
		t.Fatal("the request answered only once the held handshake had ended")
	default:
	}
}

// A session consumer call made while the hub holds the handshake of its connection's reconnect
// answers ErrConnectionLost at once, unsent, while the handshake is still held: its caller holds the
// session's lock, the shared stream read or a relay's restart turn, and releases it before it waits
// for the connection (Reconnected). nats.go holds its connection lock through the whole handshake
// (doReconnect, nats.go:3458-3485), as a hub whose auth service has not answered the CONNECT holds
// it for its authorization timeout; the call reads the record of the connection's status
// (connForRequest, requestLife), which waits for no lock of nats.go's. Each kind of request the
// client's calls make under a session's lock or a relay's turn has a row, as in lossAtOnceRow. Once
// the proxy is restored the handshake ends, Reconnected reports the connection back, and the call
// answers.
//
// The control is this row with connForRequest reading conn.IsConnected() in place of the record:
// the first call, the stream read, gives no answer while the handshake is held, and the row fails
// at heldAnswerWait.
func TestASessionConsumerCallDuringAHeldHandshakeAnswersAtOnce(t *testing.T) {
	const sessionID = "ses_held_handshake"
	url := testnats.URL(t)
	owner, err := ConnectOwningStream([]string{url}, WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the stream's owner: %v", err)
	}
	t.Cleanup(owner.Close)
	proxy := testnats.StartProxy(t, url)
	consumers, err := ConnectSessionConsumers([]string{proxy.URL()}, 0, nil, WithConnectTimeout(testnats.Remaining(t)))
	if err != nil {
		t.Fatalf("connect the session consumers through the proxy: %v", err)
	}
	t.Cleanup(consumers.Close)
	filter := []string{contracts.AgentSubject(contracts.NoProject, sessionID)}
	ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
	defer cancel()
	if _, err := consumers.Ensure(ctx, sessionID, filter, 0); err != nil {
		t.Fatalf("create the session's consumer: %v", err)
	}

	proxy.Cut()
	hold := proxy.HoldConnectAnswers()
	awaitHeld(t, hold)
	calls := []struct {
		name string
		call func(context.Context) error
	}{
		{"the stream read", func(ctx context.Context) error { _, err := consumers.StreamID(ctx); return err }},
		{"a consumer create or update", func(ctx context.Context) error { _, err := consumers.Ensure(ctx, sessionID, filter, 0); return err }},
		{"a filter write", func(ctx context.Context) error { return consumers.UpdateFilter(ctx, sessionID, filter) }},
		{"a consumer list", func(ctx context.Context) error { _, err := consumers.ListSessionConsumers(ctx); return err }},
		{"a pull", func(ctx context.Context) error {
			pull, err := consumers.Pull(ctx, sessionID)
			if err == nil {
				pull.Stop()
			}
			return err
		}},
		{"a consumer delete", func(ctx context.Context) error { _, err := consumers.Delete(ctx, sessionID, nil); return err }},
	}
	var lost error
	for _, call := range calls {
		answered := make(chan error, 1)
		go func() { answered <- call.call(ctx) }()
		var err error
		select {
		case err = <-answered:
		case <-time.After(heldAnswerWait):
			proxy.Restore()
			t.Fatalf("%s made during the held handshake gave no answer within %s: it waited for nats.go's connection lock, which the handshake holds", call.name, heldAnswerWait)
		}
		var answer *connectionLost
		if !errors.As(err, &answer) || !answer.unsent || answer.cause != nil {
			t.Fatalf("%s made during the held handshake answered %v, want ErrConnectionLost at once, unsent", call.name, err)
		}
		select {
		case <-hold.Ended():
			t.Fatalf("%s answered only once the held handshake had ended", call.name)
		default:
		}
		lost = err
	}

	proxy.Restore()
	if !Reconnected(ctx, lost) {
		t.Fatal("Reconnected did not report the connection back once the hold ended")
	}
	if id, err := consumers.StreamID(ctx); err != nil || id == "" {
		t.Fatalf("the stream read made once the connection was back answered %q, %v", id, err)
	}
}
