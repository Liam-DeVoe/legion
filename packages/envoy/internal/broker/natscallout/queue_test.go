package natscallout

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// withDecisionSlots runs the rest of the test with concurrentDecisions set to n, so a test can make
// requests queue for a slot without sending sixty-four of them.
func withDecisionSlots(t *testing.T, n int) {
	t.Helper()
	was := concurrentDecisions
	concurrentDecisions = n
	t.Cleanup(func() { concurrentDecisions = was })
}

// slowIntrospect answers each session's token after wait, giving up when ctx ends, as a broker
// whose database is slow does.
func slowIntrospect(wait time.Duration, sessions ...*session) Deps {
	answer := introspector(sessions...)
	return Deps{Introspect: func(ctx context.Context, token string) (oauthwire.Introspection, error) {
		select {
		case <-ctx.Done():
			return oauthwire.Introspection{}, ctx.Err()
		case <-time.After(wait):
		}
		return answer(ctx, token)
	}}
}

// connectAll connects every session at once and answers each one's error and how long its connect
// took, in the order of sessions.
func connectAll(t *testing.T, url string, sessions []*session) ([]error, []time.Duration) {
	t.Helper()
	errs := make([]error, len(sessions))
	took := make([]time.Duration, len(sessions))
	var wait sync.WaitGroup
	for index, s := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			began := time.Now()
			nc, err := connectSession(t, url, s, nil)
			took[index], errs[index] = time.Since(began), err
			if err == nil {
				nc.Close()
			}
		}()
	}
	wait.Wait()
	return errs, took
}

func sessionsNamed(t *testing.T, prefix string, n int) []*session {
	t.Helper()
	sessions := make([]*session, n)
	for index := range sessions {
		sessions[index] = newSession(t, fmt.Sprintf("%s_%d", prefix, index), 15*time.Minute)
	}
	return sessions
}

// The budget counts from when the request arrives, not from when a decision slot frees up: a
// request that waited for a slot gets what is left of its budget, and one already past it is
// refused at once, so every client hears inside the budget, admitted or refused, and none waits
// out the server's own timeout. With one slot and a 700 ms broker, the third request of three
// waits 1.4 s for its slot: counted from its slot it would be answered at 2.1 s, after the server
// had given up on it.
func TestTheBudgetCountsTheTimeARequestWaitsForASlot(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	sessions := sessionsNamed(t, "ses_queued", 3)
	startService(t, url, keys, slowIntrospect(700*time.Millisecond, sessions...), Decide)

	errs, took := connectAll(t, url, sessions)
	admitted := 0
	for index := range sessions {
		require.Less(t, took[index], decideBudget+400*time.Millisecond,
			"session %d heard after %s (err %v): its budget was counted from its slot, not from its arrival", index, took[index], errs[index])
		if errs[index] == nil {
			admitted++
		}
	}
	require.GreaterOrEqual(t, admitted, 2, "the requests that fit their budget were not admitted: %v", errs)
}

// A request waiting for a decision slot is refused when its own budget is spent, whatever the
// slot's holder is doing: one slot is held by a decision that ignores its context and runs three
// seconds, as a broker call that does not honour cancellation would, and a request arriving behind
// it is refused inside the budget instead of waiting for the slot. Without the wait's deadline it
// would wait the three seconds, past the two its client's connect allows.
func TestAWaitingRequestIsRefusedWhenItsBudgetIsSpent(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	stuck, waiting := newSession(t, "ses_stuck", 15*time.Minute), newSession(t, "ses_waiting", 15*time.Minute)
	deps := Deps{Introspect: introspector(stuck, waiting)}
	startService(t, url, keys, deps, func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
		if req.Token == stuck.token {
			time.Sleep(3 * time.Second)
		}
		return Decide(ctx, deps, services, req)
	})

	go func() { _, _ = connectSession(t, url, stuck, nil) }()
	time.Sleep(200 * time.Millisecond)
	began := time.Now()
	_, err := connectSession(t, url, waiting, nil)
	took := time.Since(began)
	require.ErrorIs(t, err, nats.ErrAuthorization, "the request behind a stuck slot was not refused")
	require.Less(t, took, decideBudget+400*time.Millisecond,
		"the request behind a stuck slot was heard after %s: it waited for the slot past its own budget", took)
}

// A request whose wait for a slot spent its budget is refused naming the wait, not the step its
// decision would have reached. One slot is held almost to its own deadline by a decision that
// sleeps and then introspects; the request behind it gets the slot with tens of milliseconds left.
// Started, its 200 ms introspection would be cancelled and named; it is refused naming the wait
// instead, and the broker is never asked about its token.
func TestABudgetSpentWaitingForASlotNamesTheWait(t *testing.T) {
	withDecisionSlots(t, 1)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	holder, waiter := newSession(t, "ses_holder", 15*time.Minute), newSession(t, "ses_waiter", 15*time.Minute)
	var asked atomic.Int32
	deps := slowIntrospect(200*time.Millisecond, holder, waiter)
	introspect := deps.Introspect
	deps.Introspect = func(ctx context.Context, token string) (oauthwire.Introspection, error) {
		if token == waiter.token {
			asked.Add(1)
		}
		return introspect(ctx, token)
	}
	log := &lockedLog{}
	deps.Log = slog.New(slog.NewTextHandler(log, nil))
	startService(t, url, keys, deps, func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
		if req.Token == holder.token {
			time.Sleep(decideBudget - 100*time.Millisecond)
		}
		return Decide(ctx, deps, services, req)
	})

	go func() { _, _ = connectSession(t, url, holder, nil) }()
	time.Sleep(30 * time.Millisecond)
	_, err := connectSession(t, url, waiter, nil)
	require.ErrorIs(t, err, nats.ErrAuthorization)
	testwait.WithinPolling(t, 5*time.Second, 20*time.Millisecond, "both refusals in the broker's log", func() bool {
		return strings.Count(log.String(), "refused a NATS connection") >= 2
	})
	require.Contains(t, log.String(), "budget at the wait for a decision slot", "no refusal named the wait:\n%s", log.String())
	require.Zero(t, asked.Load(), "the broker was asked about a token whose wait had spent its budget:\n%s", log.String())
}

// lockedLog is a log the service's answering goroutines write while a test reads it.
type lockedLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// holdDeliveries blocks every delivery of a request to the replica target names, once it names
// one, until the returned release runs, and reports on entered each delivery that reached the
// hold. Other replicas' deliveries pass.
func holdDeliveries(t *testing.T) (target *atomic.Pointer[Service], entered <-chan struct{}, release func()) {
	t.Helper()
	target = &atomic.Pointer[Service]{}
	reached := make(chan struct{}, 64)
	gate := make(chan struct{})
	holdDelivery = func(s *Service) {
		if target.Load() != s {
			return
		}
		reached <- struct{}{}
		<-gate
	}
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	// Registered before the service, so it runs after the service's own Close: no delivery is
	// left to read the hook when it is cleared.
	t.Cleanup(func() { release(); holdDelivery = nil })
	return target, reached, release
}

// A replica that stops answers every request the hub had already routed to it: it drains its
// subscription rather than dropping what it holds. Delivery is held, so of three requests one is
// at the hold and two wait in the subscription's own buffer when the replica is closed; the hold
// is released only after Close has begun. A replica that unsubscribed instead would drop the two
// buffered ones, whose clients would then wait out the hub's authorization timeout.
func TestAClosingReplicaAnswersTheRequestsItHolds(t *testing.T) {
	target, entered, release := holdDeliveries(t)
	keys := testnats.NewCalloutKeys(t)
	url := testnats.StartWithCallout(t, keys)
	sessions := sessionsNamed(t, "ses_held", 3)
	service := startService(t, url, keys, Deps{Introspect: introspector(sessions...)}, Decide)
	target.Store(service)

	results := make(chan []error, 1)
	go func() {
		errs, _ := connectAll(t, url, sessions)
		results <- errs
	}()
	<-entered
	// Pending counts the request at the hold too: nats.go takes a message off the subscription's
	// pending count only once its callback returns.
	testwait.WithinPolling(t, 5*time.Second, 10*time.Millisecond, "all three requests held by the subscription", func() bool {
		pending, _, err := service.sub.Pending()
		return err == nil && pending == len(sessions)
	})
	closed := make(chan struct{})
	go func() {
		service.Close()
		close(closed)
	}()
	time.Sleep(200 * time.Millisecond)
	release()
	<-closed
	errs := <-results
	for index, err := range errs {
		require.NoError(t, err, "session %d was refused: the closing replica dropped the request it held", index)
	}
}

// A replica closed while its connection is reconnecting does not come back into the queue group
// when the connection does. The fault this closes needs the drain to outlast the reconnect, so the
// closing replica holds a request when the hub stops, and that request's decision takes a second
// once the hold is released after the hub returns. A replica that drained there would leave its
// subscription among the connection's own, which nats.go sends again on the reconnect, and drop
// it on the client's side only once the held request is delivered: for the second its decision
// then takes, the hub routes requests to a replica with no subscriber. One that unsubscribes is
// out of the group before the hub returns, so every connection after the release is admitted at
// once by the other replica.
func TestAReplicaClosedWhileReconnectingStaysOutOfTheGroup(t *testing.T) {
	target, entered, release := holdDeliveries(t)
	keys := testnats.NewCalloutKeys(t)
	server, url := testnats.StartWithCalloutServer(t, keys)
	probes := sessionsNamed(t, "ses_probe", 20)
	sessions := sessionsNamed(t, "ses_after", 20)
	deps := Deps{Introspect: introspector(append(probes, sessions...)...)}
	slow := func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
		time.Sleep(time.Second)
		return Decide(ctx, deps, services, req)
	}
	closing := startService(t, url, keys, deps, slow)
	staying := startService(t, url, keys, deps, Decide)
	target.Store(closing)

	// Connect until the hub routes one request to the closing replica, which holds it.
	for _, s := range probes {
		go func() { _, _ = connectSession(t, url, s, nil) }()
		select {
		case <-entered:
		case <-time.After(300 * time.Millisecond):
			continue
		}
		break
	}
	pending, _, err := closing.sub.Pending()
	require.NoError(t, err)
	require.Positive(t, pending, "the closing replica was routed no request to hold")

	server.Stop(t)
	testwait.WithinPolling(t, 10*time.Second, 10*time.Millisecond, "both replicas reconnecting", func() bool {
		return !closing.conn.IsConnected() && !staying.conn.IsConnected()
	})
	closed := make(chan struct{})
	go func() {
		closing.Close()
		close(closed)
	}()
	time.Sleep(100 * time.Millisecond)
	server.Start(t)
	ready, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, staying.Ready(ready))
	time.Sleep(200 * time.Millisecond) // the closing replica's own reconnect
	release()

	for _, s := range sessions {
		began := time.Now()
		_, err := connectSession(t, url, s, nil)
		require.NoError(t, err, "%s was refused after the hub returned: the closing replica took its request", s.id)
		require.Less(t, time.Since(began), time.Second, "%s waited %s to be admitted", s.id, time.Since(began))
	}
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the closing replica's Close never returned")
	}
}
