package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/session"
)

// A registration that says its session reads its own agent subject (delivery.own_reader) is
// answered mode own_reader with nothing else, writes an entry that records own-reader delivery,
// and gives the session no consumer: the Legion daemon's provider-errors sender registers so, and
// reads its agent subject from a durable of its own.
func TestAnOwnReaderRegistrationIsAnsweredOwnReaderAndGivesNoConsumer(t *testing.T) {
	rig := newSessionConsumerRig(t)
	const sessionID = "legion-daemon-ownreader"

	answer := rig.subscribe(t, `{"session_id":"legion-daemon-ownreader","topics":[],"title":"Legion daemon","delivery":{"own_reader":true}}`)

	if answer.Delivery == nil || answer.Delivery.Mode != "own_reader" || answer.Delivery.Subject != "" || answer.Delivery.Gap != nil || answer.Delivery.ConsumerStart != 0 {
		t.Fatalf("answer %s, want delivery mode own_reader and nothing else", answer.raw)
	}
	if entry := rig.entry(t, sessionID); entry.Delivery != session.DeliveryOwnReader || entry.Epoch != "" || entry.ConsumerStart != 0 {
		t.Fatalf("session entry %+v, want own-reader delivery with no epoch and no consumer start", entry)
	}
	rig.requireNoConsumer(t, sessionID)
	// A heartbeat registers the same way and changes none of it.
	rig.subscribe(t, `{"session_id":"legion-daemon-ownreader","topics":[],"title":"Legion daemon","delivery":{"own_reader":true}}`)
	if entry := rig.entry(t, sessionID); entry.Delivery != session.DeliveryOwnReader {
		t.Fatalf("session entry after the heartbeat %+v, want own-reader delivery", entry)
	}
	rig.requireNoConsumer(t, sessionID)
}

// An own reader asks for no consumer beside its own reader and follows no topic, since its reader
// reads its own agent subject alone; a session id that is not one subject token names no agent
// subject of its own. Each is refused 409 with nothing written.
func TestAnOwnReaderRegistrationThatAsksForMoreIsRefused(t *testing.T) {
	rig := newSessionConsumerRig(t)
	for _, tc := range []struct {
		name, sessionID, body, reason string
	}{
		{"a consumer as well", "ses_own_and_consumer",
			`{"session_id":"ses_own_and_consumer","topics":[],"delivery":{"own_reader":true,"consumer":true,"epoch":"e"}}`, deliveryReasonOwnReaderConflict},
		{"a followed topic", "ses_own_with_topic",
			`{"session_id":"ses_own_with_topic","topics":["notifications.team.deploy"],"delivery":{"own_reader":true}}`, deliveryReasonOwnReaderConflict},
		{"a session id with a dot", "ses.own.dotted",
			`{"session_id":"ses.own.dotted","topics":[],"delivery":{"own_reader":true}}`, deliveryReasonInvalidSessionID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig.refusedSubscribe(t, tc.body).requireRefused(t, http.StatusConflict, tc.reason)
			rig.requireNothingWritten(t, tc.sessionID)
		})
	}
}

// A message stored on an own reader's agent subject is its own reader's to take: the listener's
// durable delivery acknowledges it as skipped, with no deferral, no give-up and no agent wake
// report naming the session. The control is the same message to a session that never registered,
// on the same listener: it is held for the window, given up, and reported as an agent wake, which
// is what a dead letter to the Legion daemon's sender would cost at every drop without the
// registration.
func TestAMessageToAnOwnReaderIsTakenWithNoWake(t *testing.T) {
	const (
		reader     = "legion-daemon-reads-own"
		unrecorded = "legion-daemon-never-registered"
	)
	rig := newSessionConsumerRig(t)
	harness := rig.harness
	rig.subscribe(t, `{"session_id":"`+reader+`","topics":[],"title":"Legion daemon","delivery":{"own_reader":true}}`)

	wakes, err := harness.client.Conn.SubscribeSync(bus.LegionWakeTopic)
	if err != nil {
		t.Fatalf("subscribe to %s: %v", bus.LegionWakeTopic, err)
	}
	t.Cleanup(func() { _ = wakes.Unsubscribe() })
	if err := harness.client.Conn.Flush(); err != nil {
		t.Fatalf("flush the wake subscription: %v", err)
	}
	lifetime, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	cfg := harness.config
	cfg.unplaceableRetryWindow = oneDeferralWindow
	cfg.wakes = newWakeReporter(lifetime, harness.client)

	letter := func(sessionID string) contracts.Envelope {
		item := listenerTestEnvelope(contracts.LegacyAgentSubject(sessionID), "envoy.dead-letter.a-role."+sessionID)
		item.Source = "envoy"
		item.DeadLetter = "expired"
		return item
	}
	startEnvelopeDeliveryThrough(t, harness, jetStreamDeliveryHandler(cfg), reader, letter(reader), 0)
	startEnvelopeDeliveryThrough(t, harness, jetStreamDeliveryHandler(cfg), unrecorded, letter(unrecorded), 0)

	waitFor(t, 30*time.Second, "the unregistered session's message to be given up", func() bool {
		return len(recordsFor(deliveryRecords(t, harness.logs.String(), "listener agent delivery given up"), unrecorded)) > 0
	})
	waitFor(t, 10*time.Second, "the own reader's message to be counted skipped", func() bool {
		return counterValue(t, metricsBody(t, harness), deliveredSeries("skipped")) >= 1
	})
	logs := harness.logs.String()
	for _, line := range []string{"listener agent delivery deferred", "listener agent delivery given up", "listener agent delivery failed"} {
		if got := recordsFor(deliveryRecords(t, logs, line), reader); len(got) != 0 {
			t.Fatalf("%d %q lines for the own reader %s, want none:\n%s", len(got), line, reader, logs)
		}
	}
	if got := counterValue(t, metricsBody(t, harness), deliveredSeries("skipped")); got != 1 {
		t.Fatalf("%s = %d, want the own reader's one message", deliveredSeries("skipped"), got)
	}
	if got := len(recordsFor(deliveryRecords(t, logs, "listener agent delivery deferred"), unrecorded)); got != 1 {
		t.Fatalf("control: %d deferral lines for %s, want the one the window buys:\n%s", got, unrecorded, logs)
	}
	reported := map[string]int{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && reported[unrecorded] == 0 {
		message, err := wakes.NextMsg(time.Second)
		if err != nil {
			continue
		}
		report := wakeReportIn(t, message)
		for _, sessionID := range []string{reader, unrecorded} {
			if wakeNamed(report, wakeKindAgent, sessionID) {
				reported[sessionID]++
			}
		}
	}
	reported[reader] += agentWakesFor(t, wakes, reader, 2*time.Second, time.Second)
	if reported[reader] != 0 || reported[unrecorded] != 1 {
		t.Fatalf("agent wake reports %v, want none for the own reader %s and one for the control %s:\n%s", reported, reader, unrecorded, logs)
	}
}
