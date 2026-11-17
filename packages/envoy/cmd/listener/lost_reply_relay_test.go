package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// heldListener is a listener's /v1 routes and session consumers on a server of their own, with
// every NATS connection of the listener through a hold (testnats.ReplyHold), and the server's own
// URL for the stand-ins and reads a test makes beside it.
func heldListener(t *testing.T) (*sessionConsumerRig, *testnats.ReplyHold, string) {
	t.Helper()
	url := testnats.URL(t)
	hold := testnats.StartReplyHold(t, url)
	client, err := bus.ConnectOwningStream([]string{hold.URL()}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener through the hold: %v", err)
	}
	t.Cleanup(client.Close)
	return newSessionConsumerRigOn(t, client, hold.URL()), hold, url
}

// A relay restart reads the session's interest row under the session's lock (sessionRelays.restart),
// and a read whose reply the connection's drop lost gives that lock up within 100 ms of the drop:
// a registration of the session, which takes the same lock, is answered 200 within its budget, and
// the relay restarts the session's consumer on the new connection and delivers a message stored
// after the drop within 3 s of it. This is the session-lock half of the held-reconnect harness the
// fix was measured with, where 7 of 30 registrations after a held reconnect answered 503 at main.
//
// The control is the same row at main a97cd59397 (and 44a1f1fb97): the restart holds the lock
// until its read's bound, sessionConsumerCallTimeout (5 s), ends, so the lock is still held 2 s
// after the drop and the row fails there.
func TestARelayRestartWhoseInterestReadTheDropCutOffGivesTheSessionUpAndRestarts(t *testing.T) {
	rig, hold, url := heldListener(t)
	relays, err := startSessionRelays(rig.deps.consumers, rig.harness.config)
	if err != nil {
		t.Fatalf("start the session relays: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		relays.stop(ctx)
	})
	const sessionID = "ses_lost_restart"
	// Registered while the listener holds no durable binding, so no relay restarts the
	// session's consumer until the hold is in place.
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))
	standIn := startSessionStandIn(t, url, sessionID, answerDone)
	// The interest row read is a KV direct get, which sends no flush behind it: it waits for its
	// reply from the moment the hold withholds it.
	held := hold.Hold("$JS.API.DIRECT.GET.KV_" + bus.InterestBucket + ".")
	relays.setOwned(true)
	select {
	case <-held.Withheld():
	case <-time.After(5 * time.Second):
		t.Fatal("the relay restart read no interest row within 5 s of gaining the binding")
	}

	// The restart holds the session's lock over the read whose reply the hold keeps from it; a
	// registration of the session takes the same lock.
	locked := make(chan time.Time, 1)
	go func() {
		unlock := rig.deps.consumers.locks.lock(sessionID)
		locked <- time.Now()
		unlock()
	}()
	dropped := time.Now()
	hold.Drop()
	select {
	case at := <-locked:
		if took := at.Sub(dropped); took > 100*time.Millisecond {
			t.Fatalf("the session's lock was given up %s after the drop, want within 100 ms", took)
		} else {
			t.Logf("the session's lock was given up %s after the drop", took)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the relay restart still held the session's lock 2 s after the drop cut its read off")
	}

	recorder := httptest.NewRecorder()
	sent := time.Now()
	rig.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/interests/subscribe", strings.NewReader(consumerRegistration(sessionID, "epoch-1"))))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the registration sent after the drop answered %d %s after %s, want 200", recorder.Code, recorder.Body.String(), time.Since(sent))
	}
	t.Logf("the registration sent after the drop answered 200 in %s", time.Since(sent))

	// The relay restarts on the new connection: a message stored now reaches the session.
	js, err := testnats.Connect(t, url).JetStream()
	if err != nil {
		t.Fatalf("open JetStream on the server: %v", err)
	}
	item := listenerTestEnvelope(contracts.AgentSubject(sessionID), "lost-restart")
	if _, err := js.Publish(item.Topic, marshalListenerEnvelope(t, item)); err != nil {
		t.Fatalf("store a message for the session: %v", err)
	}
	frame := standIn.collect(t, 1, time.Until(dropped.Add(3*time.Second)))[0]
	if frame.item.EventID != item.EventID {
		t.Fatalf("the relay delivered %s, want the message stored after the drop, %s", frame.item.EventID, item.EventID)
	}
	t.Logf("the relay delivered the message stored after the drop %s after the drop", frame.arrived.Sub(dropped))
}

// A registration confirms its session's consumer once it has written the session's entry
// (sessionDelivery.confirmRegistered), under the session's lock: it reads the session's record and
// row and creates the consumer again where a delete took it away. A confirm whose record read the
// connection's drop cut off is made again on the new connection, so a consumer deleted between
// the registration's consumer call and its confirm is there once the registration has answered.
//
// The control is the same row at main a97cd59397 (and 44a1f1fb97): the confirm's read waits for
// its budget, sessionConsumerCallTimeout (5 s), fails, and repairs nothing, so the registration
// answers with the session holding no consumer.
func TestARegistrationsConfirmWhoseReadTheDropCutOffIsMadeAgainOnTheNewConnection(t *testing.T) {
	rig, hold, url := heldListener(t)
	const sessionID = "ses_lost_confirm"
	name, ok := bus.SessionConsumerName(sessionID)
	if !ok {
		t.Fatalf("%s names no session consumer", sessionID)
	}
	js, err := testnats.Connect(t, url).JetStream()
	if err != nil {
		t.Fatalf("open JetStream on the server: %v", err)
	}
	// The first confirm pass takes the consumer away, as a delete on another listener does
	// between this registration's consumer call and its entry write, and holds the reply to the
	// session record read the pass makes next.
	var held atomic.Pointer[testnats.HeldReplies]
	var first atomic.Bool
	rig.deps.consumers.beforeRepairPlan = func() {
		if !first.CompareAndSwap(false, true) {
			return
		}
		if err := js.DeleteConsumer(bus.Stream, name); err != nil {
			t.Errorf("delete the session's consumer under the confirm: %v", err)
		}
		held.Store(hold.Hold("$JS.API.DIRECT.GET.KV_" + bus.SessionBucket + "."))
	}
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		rig.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/interests/subscribe", strings.NewReader(consumerRegistration(sessionID, "epoch-1"))))
		answered <- recorder
	}()
	deadline := time.Now().Add(5 * time.Second)
	for held.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the registration's confirm made no pass within 5 s")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-held.Load().Withheld():
	case <-time.After(5 * time.Second):
		t.Fatal("the confirm read no session record within 5 s")
	}
	dropped := time.Now()
	hold.Drop()
	var recorder *httptest.ResponseRecorder
	select {
	case recorder = <-answered:
	case <-time.After(8 * time.Second):
		t.Fatal("the registration did not answer within 8 s of the drop")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("the registration answered %d %s, want 200", recorder.Code, recorder.Body.String())
	}
	t.Logf("the registration answered 200 %s after the drop", time.Since(dropped))
	if _, err := js.ConsumerInfo(bus.Stream, name); err != nil {
		t.Fatalf("the session holds no consumer once its registration answered: %v", err)
	}
}

// refusalWindow is how long the hub refuses the listener's connection in the row below: past the
// registration's own budget (sendRequestBudget, 3.5 s), so the registrations sent inside the
// window are answered 503 and the one answered 200 is one the admit let through.
const refusalWindow = 4 * time.Second

// A hub that refuses the listener's connection for a while and then admits it again has the
// listener answer a registration 200 within one reconnect wait of the admit: the requests a
// registration retries on the new connection (bus.Reconnected) wait for the reconnect event
// itself, with no backoff of their own, so they add nothing to nats.go's own reconnect wait
// (connect.go's options: one second, no backoff). The hub's refusal is the proxy refusing every
// dial (testnats.Proxy.Cut), as a hub whose auth service refuses each connect is to a client that
// retries in place. Registrations are sent back to back, each once the one before it answered,
// as the held-reconnect harness sends them in timing mode.
//
// What this does not measure is the plugins' own reconnect to the hub, which a broker-token session
// makes on its broker client's jittered redial backoff, capped at 20 s: that is the session's link,
// and its registration reaches the listener over HTTP, not over it.
func TestARegistrationAfterARefusalWindowAnswersWithinAReconnectWaitOfTheAdmit(t *testing.T) {
	url := testnats.URL(t)
	proxy := testnats.StartProxy(t, url)
	client, err := bus.ConnectOwningStream([]string{proxy.URL()}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener through the proxy: %v", err)
	}
	t.Cleanup(client.Close)
	rig := newSessionConsumerRigOn(t, client, proxy.URL())
	const sessionID = "ses_refusal_window"
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))

	proxy.Cut()
	for client.Connected() {
		time.Sleep(time.Millisecond)
	}
	type answer struct {
		status int
		at     time.Time
	}
	answers := make(chan answer, 64)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			recorder := httptest.NewRecorder()
			rig.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/interests/subscribe", strings.NewReader(consumerRegistration(sessionID, "epoch-1"))))
			select {
			case answers <- answer{status: recorder.Code, at: time.Now()}:
			case <-stop:
				return
			}
			if recorder.Code == http.StatusOK {
				return
			}
		}
	}()
	time.Sleep(refusalWindow)
	admitted := time.Now()
	proxy.Restore()
	refused := 0
	for {
		select {
		case got := <-answers:
			if got.status != http.StatusOK {
				refused++
				continue
			}
			took := got.at.Sub(admitted)
			t.Logf("the first registration answered 200 %s after the admit; %d answered otherwise before it", took, refused)
			if took < 0 {
				t.Fatalf("a registration answered 200 %s before the admit, inside the refusal window", -took)
			}
			if took > 2500*time.Millisecond {
				t.Fatalf("the first registration answered 200 %s after the admit, want within one reconnect wait (1 s) and a registration's round trips (2.5 s)", took)
			}
			return
		case <-time.After(10 * time.Second):
			t.Fatalf("no registration answered 200 within 10 s of the admit; %d answered otherwise", refused)
		}
	}
}
