package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

const (
	// heldBound is the bound a row's caller gives its call while a handshake is held.
	heldBound = 300 * time.Millisecond
	// heldMargin is what a row allows past heldBound for the call's answer. A read made on the
	// caller's goroutine answers only when the attempt ends, at the client's connect timeout
	// (connectTimeout, 5 s).
	heldMargin = 700 * time.Millisecond
	// waitEnded is the error of a wait for the connection that ctx ended (reconnectWaitEnded).
	waitEnded = "bus: NATS is reconnecting: context deadline exceeded"
)

// heldRow is one call a row makes while a handshake is held: what names it, run makes it and
// returns its error, and want is the text that error must read.
type heldRow struct {
	what string
	run  func() error
	want string
}

// atBound is a row that makes call under a ctx bounded at heldBound. An answer before that bound
// is an error that says so: a call made during a handshake waits for the connection within its
// bound, as a call made between two attempts does.
func atBound(what, want string, call func(context.Context) error) heldRow {
	return heldRow{what: what, want: want, run: func() error {
		began := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), heldBound)
		defer cancel()
		err := call(ctx)
		if took := time.Since(began); took < heldBound {
			return fmt.Errorf("%w, after %s, before its bound of %s: it did not wait for the connection within its bound", err, took, heldBound)
		}
		return err
	}}
}

// answerByName runs rows at once while hold holds conn's handshake. It fails the test unless every
// row answers within heldBound and heldMargin, and within the handshake attempt held when the rows
// began: no later attempt has reached the hold, and nats.go's lock is still held
// (testnats.RequireLockHeld). Each answer must be ErrReconnecting, carry ctx's
// context.DeadlineExceeded, and read its row's want.
func answerByName(t *testing.T, hold *testnats.HandshakeHold, conn *nats.Conn, rows []heldRow) {
	t.Helper()
	calls := make([]func() error, len(rows))
	for i, row := range rows {
		calls[i] = row.run
	}
	attempt := hold.Attempts()
	answers := testnats.AnswersWithin(t, heldBound+heldMargin, "a call during the held handshake", calls...)
	if after := hold.Attempts(); after != attempt {
		t.Fatalf("the calls answered only after %d more handshake attempt(s) had reached the hold: they waited out the attempt held when they began", after-attempt)
	}
	testnats.RequireLockHeld(t, conn)
	for i, err := range answers {
		if !errors.Is(err, ErrReconnecting) || !errors.Is(err, context.DeadlineExceeded) || err.Error() != rows[i].want {
			t.Errorf("%s answered %v during the held handshake, want %q", rows[i].what, err, rows[i].want)
		}
	}
}

// releaseUntilConnected releases hold and waits until nats.go has reconnected conn.
func releaseUntilConnected(t *testing.T, hold *testnats.HandshakeHold, conn *nats.Conn) {
	t.Helper()
	hold.Release()
	testwait.WithinPolling(t, 30*time.Second, 10*time.Millisecond, "the connection to reconnect once the hold was released", conn.IsConnected)
}

// A bucket call that its caller bounds by ctx answers at that bound while nats.go holds its
// connection lock through a reconnect's handshake, and the call never starts. Bounded reads the
// connection's state before it starts the call, and each read waits for that lock. A read made on
// the caller's goroutine held the caller until the handshake attempt ended, at the client's
// connect timeout (connectTimeout, 5 s), long past the caller's bound. The row runs three calls at
// once while one handshake is held, each with a call that counts its starts:
//   - Bounded under a ctx bounded at 300 ms, which answers once that bound has passed, and not
//     before;
//   - Bounded under a ctx that had ended before the call, which answers once ConnLockWait has
//     passed;
//   - endedUnsent, the read Bounded makes for a ctx it finds ended, under that same ctx.
//
// Each answers `bus: NATS is reconnecting: context deadline exceeded`, ErrReconnecting with ctx's
// error, within the 300 ms bound and a margin of 700 ms (answerByName). No call starts, during the
// hold or after its release.
//
// The controls: a call bounded at 300 ms starts and answers before the hold, and again once the
// hold is released and the connection is back, so the answers are the hold's. Every answer comes
// within the handshake attempt held when the calls began. The negative control is Bounded's reads
// as they were, on the caller's goroutine: no call answers within the bound and the margin.
func TestABoundedCallAnswersAtItsBoundDuringAHeldHandshake(t *testing.T) {
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	runs := func(t *testing.T, when string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), heldBound)
		defer cancel()
		ran, answered, err := Bounded(ctx, conn, func() (bool, error) { return true, nil })
		if !ran || !answered || err != nil {
			t.Fatalf("control: a call bounded at %s %s ran (%t) and was answered (%t) with %v, want it run and answered nil", heldBound, when, ran, answered, err)
		}
	}
	runs(t, "before the hold")

	hold.HoldReconnect(t, conn)
	var started atomic.Int64
	call := func() (struct{}, error) {
		started.Add(1)
		return struct{}{}, nil
	}
	unanswered := func(_ struct{}, answered bool, err error) error {
		if answered {
			return fmt.Errorf("an answered call, with %v", err)
		}
		return err
	}
	ended, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	answerByName(t, hold, conn, []heldRow{
		atBound(fmt.Sprintf("Bounded under a ctx bounded at %s", heldBound), waitEnded, func(ctx context.Context) error {
			return unanswered(Bounded(ctx, conn, call))
		}),
		{what: "Bounded under a ctx that had ended", want: waitEnded, run: func() error {
			return unanswered(Bounded(ended, conn, call))
		}},
		{what: "endedUnsent under a ctx that had ended", want: waitEnded, run: func() error {
			return endedUnsent(ended, conn)
		}},
	})
	if n := started.Load(); n != 0 {
		t.Fatalf("%d call(s) made during the held handshake started, want none", n)
	}

	releaseUntilConnected(t, hold, conn)
	runs(t, "once the hold was released")
	if n := started.Load(); n != 0 {
		t.Fatalf("%d call(s) made during the held handshake started after the hold's release, want none", n)
	}
}

// A client's wait for its connection answers at its caller's bound while nats.go holds its
// connection lock through a reconnect's handshake, and so does each read of the connection's state
// that a request or a core publish makes. Each read waits for that lock, and a read made on the
// caller's goroutine held the caller until the handshake attempt ended, at the client's connect
// timeout (connectTimeout, 5 s): a registration's session consumer requests answered seconds past
// their budget. The row runs four calls at once while one handshake is held, each under a ctx
// bounded at 300 ms:
//   - ensureConnWithContext, the wait every send of the client makes first;
//   - LastStoredMessage, a public request through requestConfirmed, whose wait it is;
//   - publishConfirmed, a core publish, whose read of the connection's state before its send waits
//     for the lock, with a publish that counts its calls;
//   - SessionConsumers.streamState, whose wait for a stream read names ctx's end by the connection's
//     state (namedOn): the read it waits for is itself waiting for the connection.
//
// The first three answer `bus: NATS is reconnecting: context deadline exceeded` once their bound
// has passed, and not before; the stream read's wait answers `bus: NATS is reconnecting`, which
// carries ctx's error. Each answers within the 300 ms bound and a margin of 700 ms
// (answerByName), and the publish is never called.
//
// The controls: before the hold and once it is released and the connection is back, the wait
// answers nil, the read of a subject the stream holds nothing on answers nats.ErrMsgNotFound, the
// publish is confirmed, and the stream read answers. Every answer during the hold comes within the
// handshake attempt held when the calls began. The negative control is the reads as they were, on
// the caller's goroutine: no call answers within the bound and the margin.
func TestAClientsWaitForItsConnectionAnswersAtItsBoundDuringAHeldHandshake(t *testing.T) {
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	consumers := sessionConsumersOn(client, 0, nil)
	subject := KeptRoleSubject(fmt.Sprintf("held-handshake-%d", time.Now().UnixNano()))
	var published atomic.Int64
	publish := func(msg *nats.Msg) error {
		published.Add(1)
		return conn.PublishMsg(msg)
	}
	answers := func(t *testing.T, when string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.ensureConnWithContext(ctx); err != nil {
			t.Fatalf("control: the wait for the connection %s answered %v, want nil", when, err)
		}
		if _, err := client.LastStoredMessage(ctx, subject); !errors.Is(err, nats.ErrMsgNotFound) {
			t.Fatalf("control: the read of a subject the stream holds nothing on %s answered %v, want nats.ErrMsgNotFound", when, err)
		}
		if err := client.publishConfirmed(ctx, conn, &nats.Msg{Subject: "held-handshake.control", Data: []byte("{}")}, conn.PublishMsg); err != nil {
			t.Fatalf("control: a core publish %s answered %v, want nil", when, err)
		}
		if _, err := consumers.streamState(ctx); err != nil {
			t.Fatalf("control: a read of the stream's state %s answered %v, want nil", when, err)
		}
	}
	answers(t, "before the hold")

	hold.HoldReconnect(t, conn)
	answerByName(t, hold, conn, []heldRow{
		atBound("ensureConnWithContext", waitEnded, client.ensureConnWithContext),
		atBound("LastStoredMessage", waitEnded, func(ctx context.Context) error {
			_, err := client.LastStoredMessage(ctx, subject)
			return err
		}),
		atBound("publishConfirmed", waitEnded, func(ctx context.Context) error {
			return client.publishConfirmed(ctx, conn, &nats.Msg{Subject: "held-handshake.held", Data: []byte("{}")}, publish)
		}),
		atBound("SessionConsumers.streamState", "bus: "+ErrReconnecting.Error(), func(ctx context.Context) error {
			_, err := consumers.streamState(ctx)
			return err
		}),
	})
	if n := published.Load(); n != 0 {
		t.Fatalf("the core publish made during the held handshake was called %d time(s), want none", n)
	}

	releaseUntilConnected(t, hold, conn)
	answers(t, "once the hold was released")
	if n := published.Load(); n != 0 {
		t.Fatalf("the core publish made during the held handshake was called %d time(s) after the hold's release, want none", n)
	}
}

// A read of the client's connection state holds no lock of the client's while it waits for
// nats.go's connection lock, so a bounded send made beside it during a reconnect's handshake
// answers at its bound. Client.Connected, SubOK and a recovery's subscriptionsHealthy each read the
// connection's status, which waits for nats.go's lock, and each held the client's lock across that
// read. Every send takes the client's lock to read its connection (ensureConnWithContext,
// Connection), so a keeper pass, a /healthz read or a recovery made during a handshake held every
// bounded send until the attempt ended, at the client's connect timeout (connectTimeout, 5 s).
//
// The row starts the three reads while one handshake is held. Once they have been kept waiting for
// nats.go's lock (testnats.RequireLockHeld), it makes three bounded sends at once, each under a ctx
// bounded at 300 ms: ensureConnWithContext, Bounded on the connection Connection reads, and
// LastStoredMessage. Each answers `bus: NATS is reconnecting: context deadline exceeded` once its
// bound has passed, and not before, within the 300 ms bound and a margin of 700 ms (answerByName).
// The Bounded call never starts.
//
// The controls: before the hold the client reads its connection as connected; the three reads are
// still waiting when the sends have answered, so the sends answered beside them; and each read
// answers once the hold is released. The negative control is the three reads as they were, with
// the client's lock held across the status read: no send answers within the bound and the margin.
func TestAClientsStateReadsHoldNoLockOfItsOwnDuringAHeldHandshake(t *testing.T) {
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	if !client.Connected() || !client.subscriptionsHealthy() {
		t.Fatal("control: before the hold the client reads its connection as not live")
	}

	hold.HoldReconnect(t, conn)
	reads := []struct {
		what string
		read func() bool
	}{
		{what: "Connected", read: client.Connected},
		{what: "SubOK", read: client.SubOK},
		{what: "subscriptionsHealthy", read: client.subscriptionsHealthy},
	}
	readAnswers := make(chan string, len(reads))
	for _, r := range reads {
		go func() {
			r.read()
			readAnswers <- r.what
		}()
	}
	stillWaiting := func(when string) {
		t.Helper()
		select {
		case what := <-readAnswers:
			t.Fatalf("control: %s answered %s, while the handshake was held: it did not wait for nats.go's lock", what, when)
		default:
		}
	}
	testnats.RequireLockHeld(t, conn)
	stillWaiting("before the sends began")

	var started atomic.Int64
	subject := KeptRoleSubject(fmt.Sprintf("held-handshake-reads-%d", time.Now().UnixNano()))
	answerByName(t, hold, conn, []heldRow{
		atBound("ensureConnWithContext", waitEnded, client.ensureConnWithContext),
		atBound("Bounded on the connection Connection reads", waitEnded, func(ctx context.Context) error {
			_, answered, err := Bounded(ctx, client.Connection(), func() (struct{}, error) {
				started.Add(1)
				return struct{}{}, nil
			})
			if answered {
				return fmt.Errorf("an answered call, with %v", err)
			}
			return err
		}),
		atBound("LastStoredMessage", waitEnded, func(ctx context.Context) error {
			_, err := client.LastStoredMessage(ctx, subject)
			return err
		}),
	})
	stillWaiting("before the sends had answered")
	if n := started.Load(); n != 0 {
		t.Fatalf("the Bounded call made during the held handshake started %d time(s), want none", n)
	}

	releaseUntilConnected(t, hold, conn)
	for range reads {
		select {
		case <-readAnswers:
		case <-time.After(30 * time.Second):
			t.Fatal("control: a read of the client's connection state gave no answer within 30 s of the hold's release")
		}
	}
}

// A legacy JetStream request whose nats.Context had ended names its error within that context
// during a reconnect's handshake. nats.go answers such a request with ctx's error at once, before
// it takes its connection lock, and the name of that error reads the connection's status (namedOn),
// which waits for that lock. The read was made at once on the caller's goroutine, so the request
// answered only when the handshake attempt ended, at the client's connect timeout
// (connectTimeout, 5 s). A caller whose budget earlier steps spent makes such a request: the
// listener's /healthz read of its durable, a durable's bind wait, and the stream ensure under a run's
// context that the drop ended (requestsUnder). The row makes three at once while one handshake is
// held, each under a ctx that had ended before the call:
//   - StreamInfo through the client's JetStream context with nats.Context, the shape of those
//     callers;
//   - StreamInfo through requestsUnder over that context, which adds the nats.Context itself;
//   - KeyValue.GetFromLeader, the leader read of a key.
//
// Each answers `bus: NATS is reconnecting`, which carries ctx's error, within ConnLockWait of the
// call and so within the 300 ms bound and a margin of 700 ms (answerByName).
//
// The controls: the same three calls under the same ended ctx answer ctx's bare error, which names
// no reconnect, before the hold and once it is released and the connection is back; and the leader
// read under a live ctx answers that the key is not there. The negative control is the name read as
// it was, at once on the caller's goroutine whatever the request's context: no call answers within
// the bound and the margin.
func TestARequestWhoseContextEndedIsNamedWithinItDuringAHeldHandshake(t *testing.T) {
	const key = "never-written"
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	kv, err := EnsureKeyValue(t.Context(), client.Conn, client.JS(), &nats.KeyValueConfig{Bucket: "held-handshake-leader-read"})
	if err != nil {
		t.Fatalf("open the bucket: %v", err)
	}
	ended, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	named := "bus: " + ErrReconnecting.Error()
	rows := []heldRow{
		{what: "StreamInfo with nats.Context", want: named, run: func() error {
			_, err := client.JS().StreamInfo(Stream, nats.Context(ended))
			return err
		}},
		{what: "StreamInfo through requestsUnder", want: named, run: func() error {
			_, err := requestsUnder(ended, client.JS()).StreamInfo(Stream)
			return err
		}},
		{what: "GetFromLeader", want: fmt.Sprintf("read key %s of KV bucket %s from its stream's leader: %s", key, kv.Bucket(), named), run: func() error {
			_, err := kv.GetFromLeader(ended, key)
			return err
		}},
	}
	bare := func(t *testing.T, when string) {
		t.Helper()
		for _, row := range rows {
			if err := row.run(); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrReconnecting) {
				t.Fatalf("control: %s under a ctx that had ended answered %v %s, want ctx's own error, which names no reconnect", row.what, err, when)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := kv.GetFromLeader(ctx, key); !errors.Is(err, nats.ErrKeyNotFound) {
			t.Fatalf("control: GetFromLeader of a key never written %s answered %v, want nats.ErrKeyNotFound", when, err)
		}
	}
	bare(t, "before the hold")

	hold.HoldReconnect(t, conn)
	answerByName(t, hold, conn, rows)

	releaseUntilConnected(t, hold, conn)
	bare(t, "once the hold was released")
}

// A request already waiting for its reply when nats.go begins a reconnect's handshake answers at
// its own bound. Its reply inbox ends with the wait, and an unsubscribe waits for nats.go's
// connection lock, which nats.go holds through the handshake. A wait that ended without a reply
// unsubscribed on the caller's goroutine, so the request answered only when the handshake attempt
// ended, at the client's connect timeout (connectTimeout, 5 s): the listener's connection probe
// (RequestCore), a session consumer's request (requestConfirmed) and a role forward's wait for the
// stream (PendingForward.Wait) each answered past its bound. Such a wait now unsubscribes on a
// goroutine of its own (endReplies).
//
// The row sends the three requests to a subject a silent subscriber takes and never answers, so no
// reply and no no-responders answer comes. Once each has reached the subscriber and the client's
// own flush is answered, so each waits for its reply, it holds the reconnect's handshake. Then the
// probe's and the consumer request's ctx is cancelled, and the forward's window, 3 s from its send,
// runs out during the hold. Each answers within 1 s of its own end, within the attempt held when
// they ended (no later attempt has reached the hold, and nats.go's lock is still held): the probe
// with ctx's error, the consumer request with `bus: NATS is reconnecting`, which carries ctx's
// error, and the forward with ErrForwardNotStored.
//
// The controls: no request answers before its end, so each was waiting for its reply when the hold
// began; and once the hold is released, the connection holds as many subscriptions as before the
// requests, so every unsubscribe left running has landed. The negative control is the unsubscribe
// as it was, on the caller's goroutine: no request answers within 1 s of its end.
func TestARequestWaitingForItsReplyAnswersAtItsBoundWhenAHandshakeIsHeldUnderIt(t *testing.T) {
	const forwardWindow = 3 * time.Second
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	silent := testnats.Connect(t, testnats.URL(t))
	subject := fmt.Sprintf("held-handshake.silent.%d", time.Now().UnixNano())
	arrived := make(chan struct{}, 3)
	if _, err := silent.Subscribe(subject, func(*nats.Msg) { arrived <- struct{}{} }); err != nil {
		t.Fatalf("subscribe the silent subscriber: %v", err)
	}
	if err := silent.Flush(); err != nil {
		t.Fatalf("flush the silent subscriber: %v", err)
	}
	before := conn.NumSubscriptions()

	type answer struct {
		what string
		err  error
		at   time.Time
	}
	answers := make(chan answer, 3)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() {
		_, err := client.RequestCore(ctx, subject, nats.Header{"Envoy-Probe": []string{"held-handshake"}})
		answers <- answer{what: "RequestCore", err: err, at: time.Now()}
	}()
	go func() {
		_, err := client.requestConfirmed(ctx, subject, []byte("{}"))
		answers <- answer{what: "requestConfirmed", err: err, at: time.Now()}
	}()
	forwardEnds := time.Now().Add(forwardWindow)
	forward, err := client.StartRequestStoredTo(subject, contracts.Envelope{EventID: "evt-held-handshake-forward", Topic: subject}, "held-handshake-forward", forwardWindow)
	if err != nil {
		t.Fatalf("control: the forward's send answered %v, want it sent", err)
	}
	go func() {
		err := forward.Wait()
		answers <- answer{what: "PendingForward.Wait", err: err, at: time.Now()}
	}()
	for range 3 {
		select {
		case <-arrived:
		case <-time.After(30 * time.Second):
			t.Fatal("control: a request did not reach the silent subscriber within 30 s")
		}
	}
	// The client's own flush is answered after each request's, so each request now waits for its
	// reply.
	if err := conn.Flush(); err != nil {
		t.Fatalf("control: flush the client's connection: %v", err)
	}
	notYet := func(when string) {
		t.Helper()
		select {
		case got := <-answers:
			t.Fatalf("control: %s answered %v %s, so it was not waiting for its reply when the hold began", got.what, got.err, when)
		default:
		}
	}
	notYet("before the hold")

	hold.HoldReconnect(t, conn)
	notYet("before its end, during the hold")
	attempt := hold.Attempts()
	cancel()
	cancelled := time.Now()
	ends := map[string]time.Time{"RequestCore": cancelled, "requestConfirmed": cancelled, "PendingForward.Wait": forwardEnds}
	guard := time.NewTimer(time.Until(forwardEnds) + heldBound + heldMargin)
	defer guard.Stop()
	got := map[string]answer{}
	for range 3 {
		select {
		case a := <-answers:
			got[a.what] = a
		case <-guard.C:
			t.Fatalf("only %d of 3 requests answered within 1 s of their ends while nats.go's lock was held: the others waited for the lock", len(got))
		}
	}
	if after := hold.Attempts(); after != attempt {
		t.Fatalf("the requests answered only after %d more handshake attempt(s) had reached the hold: they waited out the attempt held when they ended", after-attempt)
	}
	testnats.RequireLockHeld(t, conn)
	for what, end := range ends {
		a := got[what]
		if late := a.at.Sub(end); late > heldBound+heldMargin {
			t.Errorf("%s answered %s after its end, want within %s", what, late, heldBound+heldMargin)
		}
		if a.at.Before(end) {
			t.Errorf("%s answered %v %s before its end", what, a.err, end.Sub(a.at))
		}
	}
	if err := got["RequestCore"].err; !errors.Is(err, context.Canceled) {
		t.Errorf("RequestCore answered %v, want ctx's error", err)
	}
	if err := got["requestConfirmed"].err; !errors.Is(err, ErrReconnecting) || !errors.Is(err, context.Canceled) {
		t.Errorf("requestConfirmed answered %v, want ErrReconnecting with ctx's error", err)
	}
	if err := got["PendingForward.Wait"].err; !errors.Is(err, ErrForwardNotStored) {
		t.Errorf("PendingForward.Wait answered %v, want ErrForwardNotStored", err)
	}

	releaseUntilConnected(t, hold, conn)
	testwait.WithinPolling(t, 30*time.Second, 10*time.Millisecond, "every reply inbox the requests left to unsubscribe to be gone once the hold was released",
		func() bool { return conn.NumSubscriptions() == before })
}

// doneWatch is ctx, reporting the first call of its Done: a readWithin under it calls Done first,
// just before it starts its read.
type doneWatch struct {
	context.Context
	once  sync.Once
	asked chan struct{}
}

func (d *doneWatch) Done() <-chan struct{} {
	d.once.Do(func() { close(d.asked) })
	return d.Context.Done()
}

// A core publish whose ctx ends while its read of the connection's state waits for nats.go's lock
// publishes nothing, even when that read then answers: readWithin gives a read ConnLockWait however
// soon ctx ends, so its answer can come after ctx ended, and the publish read ctx only before the
// read. Its subscribers then received a message its caller was told failed, and the caller's retry
// delivered it twice.
//
// The row holds a reconnect's handshake, so the read waits for nats.go's lock. It starts the
// publish under a ctx that reports the read's start (doneWatch), cancels ctx, then releases the
// hold, so nats.go completes the handshake and lets the lock go within a few milliseconds and the
// read answers. The publish answers `bus: window elapsed before the publish to "<subject>" was
// flushed: context canceled`, and its publish is never called.
//
// The controls: a publish with a live ctx before the hold and once the connection is back is
// confirmed and calls its publish once each; and the answer is not ErrReconnecting, so the read
// answered after ctx ended rather than leaving the publish unsent at ConnLockWait. The negative
// control is publishConfirmed as it was, reading ctx only before the read: it publishes.
func TestAPublishWhoseContextEndsInItsLockWaitPublishesNothing(t *testing.T) {
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	subject := fmt.Sprintf("held-handshake.publish.%d", time.Now().UnixNano())
	var published atomic.Int64
	publish := func(msg *nats.Msg) error {
		published.Add(1)
		return conn.PublishMsg(msg)
	}
	confirmed := func(t *testing.T, when string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		was := published.Load()
		if err := client.publishConfirmed(ctx, conn, &nats.Msg{Subject: subject, Data: []byte("{}")}, publish); err != nil || published.Load() != was+1 {
			t.Fatalf("control: a publish with a live ctx %s answered %v and published %d time(s), want nil and once", when, err, published.Load()-was)
		}
		published.Store(0)
	}
	confirmed(t, "before the hold")

	hold.HoldReconnect(t, conn)
	live, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	watched := &doneWatch{Context: live, asked: make(chan struct{})}
	answered := make(chan error, 1)
	go func() {
		answered <- client.publishConfirmed(watched, conn, &nats.Msg{Subject: subject, Data: []byte("{}")}, publish)
	}()
	select {
	case <-watched.asked:
	case err := <-answered:
		t.Fatalf("control: the publish answered %v before it read the connection's state", err)
	case <-time.After(30 * time.Second):
		t.Fatal("control: the publish did not reach its read of the connection's state within 30 s")
	}
	cancel()
	hold.Release()
	want := fmt.Sprintf("bus: window elapsed before the publish to %q was flushed: context canceled", subject)
	select {
	case err := <-answered:
		if errors.Is(err, ErrReconnecting) {
			t.Fatalf("control: the publish answered %v: its read did not answer within ConnLockWait of the hold's release, so the row did not reach its case", err)
		}
		if err == nil || err.Error() != want || !errors.Is(err, context.Canceled) {
			t.Errorf("the publish whose ctx ended in its lock wait answered %v, want %q", err, want)
		}
	case <-time.After(heldBound + heldMargin):
		t.Fatalf("the publish whose ctx ended in its lock wait gave no answer within %s of the hold's release", heldBound+heldMargin)
	}
	if n := published.Load(); n != 0 {
		t.Fatalf("the publish whose ctx ended in its lock wait was called %d time(s), want none", n)
	}

	testwait.WithinPolling(t, 30*time.Second, 10*time.Millisecond, "the connection to reconnect once the hold was released", conn.IsConnected)
	confirmed(t, "once the hold was released")
}

// A window's request whose read of the connection's state, before its send, meets a reconnect's
// handshake answers at its deadline and sends nothing. The read (observe) takes nats.go's
// connection lock, which nats.go holds through the handshake, and it was made on the caller's
// goroutine with no bound: the request answered only when the attempt ended, at the client's
// connect timeout (connectTimeout, 5 s), whatever its own timeout. Now the read is waited for
// until the request's deadline (window.observeToSend).
//
// The window's pin reads no state of nats.go's, and the inbox's subscribe before the read takes the
// lock too, so a handshake reaches the read only when it begins between the two. The row holds it
// there: the window's requestSubscribed seam holds the reconnect's handshake once the inbox is
// subscribed, and the request, with a timeout of 300 ms, then reads the connection's state. It
// answers `bus: NATS is reconnecting`, ErrUnavailable with its deadline's error, once its 300 ms
// have passed and within a margin of 700 ms, during the attempt held when it read; nothing reaches
// the subject's subscriber, and once the hold is released the inbox the request left is gone.
//
// The controls: before the hold, a request of the same window reaches the subscriber; the lock is
// still held, and no later attempt has reached the hold, when the request answers. The negative
// control is the read as it was, observe on the caller's goroutine (with the seam kept): the
// request answers only when the attempt ends.
func TestAWindowRequestAnswersAtItsDeadlineWhenAHandshakeIsHeldBeforeItsSend(t *testing.T) {
	client, hold := heldHandshakeClient(t)
	conn := client.Connection()
	silent := testnats.Connect(t, testnats.URL(t))
	subject := fmt.Sprintf("held-handshake.window.%d", time.Now().UnixNano())
	arrived := make(chan *nats.Msg, 4)
	if _, err := silent.ChanSubscribe(subject, arrived); err != nil {
		t.Fatalf("subscribe the silent subscriber: %v", err)
	}
	if err := silent.Flush(); err != nil {
		t.Fatalf("flush the silent subscriber: %v", err)
	}
	item := contracts.Envelope{EventID: "evt-held-handshake-window", Topic: subject}
	w := client.WindowUntil(nil).(*window)
	before := conn.NumSubscriptions()
	control, err := w.request(subject, item, RequestOptions{MsgID: "held-handshake-window-control", Timeout: heldBound})
	if err != nil {
		t.Fatalf("control: a request before the hold was refused before it was sent: %v", err)
	}
	select {
	case <-arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("control: a request before the hold did not reach the subscriber within 30 s")
	}
	// Nothing acknowledges a request to a subject the stream does not capture, so it ends at its
	// timeout and its inbox goes.
	if err := control(); !errors.Is(err, ErrNotAcknowledged) {
		t.Fatalf("control: a request before the hold answered %v, want ErrNotAcknowledged", err)
	}
	inboxesGone := func(when string) {
		t.Helper()
		testwait.WithinPolling(t, 30*time.Second, 10*time.Millisecond, "the requests' inboxes to be gone "+when,
			func() bool { return conn.NumSubscriptions() == before })
	}
	inboxesGone("before the hold")

	var (
		attempt int64
		held    time.Time
	)
	holdHere := func() {
		hold.HoldReconnect(t, conn)
		attempt = hold.Attempts()
		held = time.Now()
	}
	w.requestSubscribed.Store(&holdHere)
	wait, err := w.request(subject, item, RequestOptions{MsgID: "held-handshake-window", Timeout: heldBound})
	took := time.Since(held)
	w.requestSubscribed.Store(nil)
	if wait != nil {
		t.Fatal("the request was sent although its read of the connection's state met a held handshake")
	}
	if took > heldBound+heldMargin {
		t.Fatalf("the request answered %s after the handshake was held, want within %s: its read waited for nats.go's lock", took, heldBound+heldMargin)
	}
	if took < heldBound {
		t.Errorf("the request answered %s after the handshake was held, before its deadline of %s", took, heldBound)
	}
	if after := hold.Attempts(); after != attempt {
		t.Fatalf("the request answered only after %d more handshake attempt(s) had reached the hold: it waited out the attempt held when it read", after-attempt)
	}
	testnats.RequireLockHeld(t, conn)
	if !errors.Is(err, ErrReconnecting) || !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) || err.Error() != "bus: "+ErrReconnecting.Error() {
		t.Errorf("the request whose read met a held handshake answered %v, want `bus: NATS is reconnecting` wrapping ErrUnavailable and its deadline's error", err)
	}

	releaseUntilConnected(t, hold, conn)
	inboxesGone("once the hold was released")
	select {
	case msg := <-arrived:
		t.Fatalf("the request whose read met a held handshake reached the subscriber: %s", msg.Data)
	case <-time.After(500 * time.Millisecond):
	}
}
