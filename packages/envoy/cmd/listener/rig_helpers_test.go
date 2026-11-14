package main

// The rig several of this package's tests share: a bus client of the test's own, the envelopes it
// publishes through it, the wait for a durable to have settled, the wait for a started listener
// to list a session as registered through a machine, and the wait for a product bound to stand
// armed on a test's fake clock. They are here, in a file that tests nothing itself, because the
// tests that use them are spread across this package and no one of those files owns them.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/testwait"
)

// connectPublisher opens a bus client of the test's own, apart from the listener's: a publish
// through it reaches NATS as one from any session holding its own connection does, core NATS for a
// topic the stream does not store and JetStream for one it does (bus.Client.Publish).
func connectPublisher(t *testing.T, uri string) *bus.Client {
	t.Helper()
	client, err := bus.Connect([]string{uri}, bus.WithName("listener-test-publisher"))
	if err != nil {
		t.Fatalf("connect the publisher: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// newEnvelope is a valid envelope from source on topic carrying message, with ids of its own. Its
// dedupe key keeps the prefix it was written with: role_lane_test.go compares a role forward's
// dedupe key against that prefix, so the two are one choice.
func newEnvelope(source, topic, message string) contracts.Envelope {
	eventID := "evt-" + id.New()
	return contracts.Envelope{
		EventID:        eventID,
		Source:         source,
		SourceEventID:  eventID,
		Topic:          topic,
		DedupeKey:      "live-topics-test." + eventID,
		IssuedAt:       contracts.NowMillis(),
		PayloadSummary: message,
		Payload:        message,
		TraceID:        "trace-" + eventID,
	}
}

// publish publishes item through client and flushes, so the server holds it when publish returns.
func publish(t *testing.T, client *bus.Client, item contracts.Envelope) {
	t.Helper()
	if err := client.Publish(item); err != nil {
		t.Fatalf("publish %s: %v", item.Topic, err)
	}
	if err := client.Conn.Flush(); err != nil {
		t.Fatalf("flush the publish of %s: %v", item.Topic, err)
	}
}

// awaitDurableState waits for durable to hold pending messages for an ack with every message
// through floor acked.
func awaitDurableState(t *testing.T, client *bus.Client, durable string, pending int, floor uint64) {
	t.Helper()
	testwait.Until(t, fmt.Sprintf("the durable %s to hold %d pending ack with everything through %d acked", durable, pending, floor),
		func() (string, bool) {
			info, err := client.JS().ConsumerInfo(bus.Stream, durable)
			if err != nil {
				t.Fatalf("read the durable %s: %v", durable, err)
			}
			return fmt.Sprintf("%d pending ack, ack floor %d", info.NumAckPending, info.AckFloor.Stream),
				info.NumAckPending == pending && info.AckFloor.Stream >= floor
		})
}

// awaitSessionMachine waits for listener's GET /v1/sessions to list sessionID as registered
// through machineID, so a delivery through this listener reads that session entry.
func (p *listenerProcess) awaitSessionMachine(t *testing.T, sessionID, machineID string) {
	t.Helper()
	testwait.Until(t, fmt.Sprintf("the listener on port %d to list %s as registered through %s", p.port, sessionID, machineID),
		func() (string, bool) {
			status, body := callListener(t, p.port, http.MethodGet, "/v1/sessions", "")
			var listed []struct {
				SessionID string `json:"session_id"`
				MachineID string `json:"machine_id"`
			}
			if status != http.StatusOK || json.Unmarshal([]byte(body), &listed) != nil {
				return fmt.Sprintf("%d %s", status, body), false
			}
			for _, row := range listed {
				if row.SessionID == sessionID && row.MachineID == machineID {
					return body, true
				}
			}
			return body, false
		})
}

// awaitArmed waits until clk holds a timer whose deadline matches, and returns the deadlines it
// saw pending then. It names the one product bound a test is about: a fake clock carries every
// bound the code arms on it, so a test that means one of them waits for that bound's own deadline
// rather than for a count of timers.
func awaitArmed(t *testing.T, clk *clocktest.Fake, what string, matches func(time.Time) bool) []time.Time {
	t.Helper()
	return testwait.Until(t, what, func() ([]time.Time, bool) {
		armed := clk.Armed()
		return armed, slices.ContainsFunc(armed, matches)
	})
}

// armedAt matches a timer armed for exactly want, the deadline a test computes from the moment its
// fake stood at when the code was started and the bound's length.
func armedAt(want time.Time) func(time.Time) bool {
	return func(deadline time.Time) bool { return deadline.Equal(want) }
}
