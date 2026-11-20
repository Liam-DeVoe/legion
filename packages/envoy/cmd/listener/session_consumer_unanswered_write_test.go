package main

// A consumer create or update whose caller stopped waiting for its answer can still land, and it
// can land after a later read or write of the same connection: nats-server works its JetStream
// API requests on a pool of workers, so that later call can be answered first
// (bus.UnansweredWrite). Each case here holds such a write in the listener's own TCP relay past
// its caller's budget, lets the fence behind it run, and then forwards the write, in order, ahead
// of the calls behind it. The consumer must end on the row the bucket holds once the work the
// listener left behind the write has run (awaitSettled): its handler waits for the write's answer
// and judges the consumer again after it (sessionDelivery.unansweredWrite). The first row of each
// table holds the call for less than the budget, so it is answered, and is the control that the
// check can pass.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// unansweredWriteHold is how long a case holds the write whose answer it loses: past the
// caller's own budget (sessionConsumerCallTimeout), so the caller stops waiting for it.
const unansweredWriteHold = sessionConsumerCallTimeout + 500*time.Millisecond

// newRelayedConsumerRig is a rig whose session-consumer connection goes through a TCP relay of
// the test's own (startListenerRelay), while its bucket reads go to the server directly: the
// relay can then hold one consumer call and nothing the fence reads from the bucket.
func newRelayedConsumerRig(t *testing.T) (*sessionConsumerRig, *listenerRelay) {
	t.Helper()
	url := testnats.URL(t)
	relay := startListenerRelay(t, url)
	client, err := bus.ConnectOwningStream([]string{url}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener: %v", err)
	}
	t.Cleanup(client.Close)
	return newSessionConsumerRigOn(t, client, relay.relayed), relay
}

// A registration whose consumer call loses its answer is answered 503, and the settle behind it
// holds the filter to the row the bucket holds (registerSession). The call is still on its way
// when the settle reads the consumer, and lands after it: without a judge after that landing the
// consumer keeps the plan the registration never stored, and, where another listener stored a
// newer row meanwhile, lacks the topic that writer was answered 200 for.
func TestASettleAfterARegistrationWhoseConsumerCallLostItsAnswerLeavesNoUnstoredPlan(t *testing.T) {
	for _, c := range []struct {
		name  string
		hold  time.Duration
		newer bool
	}{
		{"answered", time.Second, false},
		{"answer-lost", unansweredWriteHold, false},
		{"answer-lost-under-a-newer-row", unansweredWriteHold, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, relay := newRelayedConsumerRig(t)
			b := newSessionConsumerRig(t)
			sessionID := "ses_unanswered_settle_" + strings.ReplaceAll(c.name, "-", "_")
			const stored = "notifications.dispatch.issue.UNANSWEREDSTORED.>"
			const planned = "notifications.dispatch.issue.UNANSWEREDPLANNED.>"
			const newer = "notifications.dispatch.issue.UNANSWEREDNEWER.>"
			b.subscribe(t, consumerRegistration(sessionID, "epoch-1", stored))
			held := make(chan struct{})
			relay.pause("$JS.API.CONSUMER.CREATE.", 1, func() {
				if c.newer {
					// Under the held call: another listener stores a newer row.
					b.subscribe(t, consumerRegistration(sessionID, "epoch-1", stored, newer))
				}
				time.Sleep(c.hold)
				close(held)
			})
			recorder := httptest.NewRecorder()
			a.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/interests/subscribe",
				strings.NewReader(consumerRegistration(sessionID, "epoch-1", planned))))
			want := http.StatusServiceUnavailable
			if c.hold < sessionConsumerCallTimeout {
				want = http.StatusOK
			}
			if recorder.Code != want {
				t.Fatalf("the registration answered %d, want %d: %s", recorder.Code, want, recorder.Body.String())
			}
			<-held
			awaitSettled(a)
			requireFilteredOnStoredRow(t, a, sessionID)
			if c.newer {
				b.store(t, "notifications.dispatch.issue.UNANSWEREDNEWER.comment")
				b.awaitPending(t, sessionID, 1)
			}
		})
	}
}

// A relay restart whose own write loses its answer settles the consumer on the newest row: the
// restart's corrections read the consumer while that write is still on its way, and it lands
// after them carrying the row the restart read, under which another listener stored a newer one.
func TestARelayRestartWhoseWriteLostItsAnswerSettlesOnTheNewestRow(t *testing.T) {
	for _, c := range []struct {
		name string
		hold time.Duration
	}{
		{"answered", time.Second},
		{"answer-lost", unansweredWriteHold},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, relay := newRelayedConsumerRig(t)
			relays, err := startSessionRelays(a.deps.consumers, a.harness.config)
			if err != nil {
				t.Fatalf("start the relays: %v", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				relays.stop(ctx)
			})
			b := newSessionConsumerRig(t)
			sessionID := "ses_unanswered_restart_" + strings.ReplaceAll(c.name, "-", "_")
			const kept = "notifications.dispatch.issue.UNANSWEREDKEPT.>"
			const added = "notifications.dispatch.issue.UNANSWEREDADD.>"
			a.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept))

			held := make(chan struct{})
			reads := 0
			a.deps.consumers.afterStoredRowRead = func() {
				reads++
				if reads > 1 {
					return
				}
				// Under the restart's read: another listener stores a newer row and sets its
				// filter, and the restart's own write of the older row is held at the relay.
				b.subscribe(t, consumerRegistration(sessionID, "epoch-1", kept, added))
				relay.pause("$JS.API.CONSUMER.CREATE.", 1, func() {
					time.Sleep(c.hold)
					close(held)
				})
			}
			_, pull, _ := relays.restart(context.Background(), &relayRun{sessionID: sessionID})
			if pull != nil {
				pull.Stop()
			}
			<-held
			awaitSettled(a)
			a.deps.consumers.afterStoredRowRead = nil
			requireFilters(t, b, sessionID, kept, added)
			b.store(t, "notifications.dispatch.issue.UNANSWEREDADD.comment")
			b.awaitPending(t, sessionID, 1)
		})
	}
}
