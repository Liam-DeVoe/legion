package main

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/testnats"
)

// laneOnHarness opens a role lane over harness's server whose handler is handler, as main.go opens
// the listener's (bus.ConnectRoleLane), and returns a roleLane over it and forwards, for its drain.
func laneOnHarness(t *testing.T, harness listenerDeliveryHarness, handler natsgo.MsgHandler, forwards *roleForwarder) roleLane {
	t.Helper()
	client, _, err := bus.ConnectRoleLane([]string{harness.client.Conn.ConnectedUrl()}, "test-machine", handler)
	if err != nil {
		t.Fatalf("open the role lane: %v", err)
	}
	t.Cleanup(client.Close)
	return roleLane{client: client, forwards: forwards, stopWatch: func() {}}
}

// The listener's stop keeps every role message its lane took or still holds while every forward
// slot is held. NATS refuses every forward, so three slots fill with three held messages and the
// handler waits for a slot with a fourth, while the lane's subscription holds six more. The drain
// stops the forwarder before it drains the lane: the handler's wait ends, each message, held or
// not, is kept on the role's kept lane under held_at_shutdown, and the drain takes the six the
// subscription holds, none lost.
func TestRoleLaneDrainKeepsEveryMessageWhileEverySlotIsHeld(t *testing.T) {
	harness := newListenerDeliveryHarness(t)
	const (
		role     = "drain-every-slot-held"
		limit    = 3
		messages = 10
	)
	if err := harness.sessions.Put("ses_drain_every_slot", session.SessionEntry{MachineID: "test-machine", SelfSubscribed: true}); err != nil {
		t.Fatalf("register holder: %v", err)
	}
	if _, err := harness.registry.SetRole("ses_drain_every_slot", "test-machine", role, false); err != nil {
		t.Fatalf("claim role: %v", err)
	}
	cfg := harness.config
	cfg.forwardRole = inlineForward(func(string, contracts.Envelope, string, time.Duration) error {
		return errors.New("nats: connection closed")
	})
	forwarder := newRoleForwarderLimited(cfg, limit)
	cfg.roleForwards = forwarder
	lane := laneOnHarness(t, harness, coreNATSDeliveryHandler(cfg), forwarder)
	publisher := testnats.Connect(t, harness.client.Conn.ConnectedUrl())
	t.Cleanup(publisher.Close)
	published := make([]contracts.Envelope, messages)
	for i := range published {
		published[i] = publishRoleMessage(t, publisher, role, fmt.Sprintf("drained %d", i))
	}
	if err := publisher.Flush(); err != nil {
		t.Fatalf("flush the role messages: %v", err)
	}
	waitFor(t, 20*time.Second, fmt.Sprintf("%d messages held and the handler waiting with the next", limit), func() bool {
		logs := harness.logs.String()
		return strings.Count(logs, `"msg":"`+roleHeldLine+`"`) >= limit &&
			countLogged(logs, "listener received", published[limit].EventID) == 1
	})
	if taken := len(linesIn(harness.logs.String(), "listener received", nil)); taken != limit+1 {
		t.Fatalf("the handler took %d messages before the stop, want %d: the rest must still wait in the lane's subscription", taken, limit+1)
	}

	lane.drain(harness.config.logger, 3*time.Second)

	logs := harness.logs.String()
	if strings.Contains(logs, roleLaneUndrainedLine) || strings.Contains(logs, roleHeldUnkeptLine) {
		t.Fatalf("the drain lost role messages:\n%s", logs)
	}
	for i, item := range published {
		kept := linesIn(logs, "listener kept role message for the role's next holder", map[string]any{"role": role, "event_id": item.EventID})
		if len(kept) != 1 || kept[0]["reason"] != string(roleKeptAtShutdown) {
			t.Fatalf("message %d %s: kept lines %v, want one naming %s:\n%s", i, item.EventID, kept, roleKeptAtShutdown, logs)
		}
	}
	if stored := storedOnSubjects(t, harness.client, bus.KeptRoleSubject(">"), role); stored != messages {
		t.Fatalf("the role's kept lane holds %d copies, want %d", stored, messages)
	}
}

// A drain that runs out with role messages still in the lane's subscription logs at ERROR how many
// its handler had not finished, the one it is on included (nats.go's Subscription.Pending): the
// close drops each one it had not taken, and the lane held the only copy.
func TestRoleLaneDrainLogsTheMessagesItLeavesUntaken(t *testing.T) {
	harness := newListenerDeliveryHarness(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var taken atomic.Int32
	lane := laneOnHarness(t, harness, func(*natsgo.Msg) {
		taken.Add(1)
		<-release
	}, newRoleForwarderLimited(harness.config, 1))
	publisher := testnats.Connect(t, harness.client.Conn.ConnectedUrl())
	t.Cleanup(publisher.Close)
	const messages = 5
	for i := range messages {
		publishRoleMessage(t, publisher, "drain-untaken", fmt.Sprintf("untaken %d", i))
	}
	if err := publisher.Flush(); err != nil {
		t.Fatalf("flush the role messages: %v", err)
	}
	waitFor(t, 20*time.Second, "the handler to block on the first message", func() bool { return taken.Load() == 1 })
	waitFor(t, 20*time.Second, "the lane's subscription to hold the rest", func() bool {
		return lane.client.Conn.Stats().InMsgs >= messages
	})

	lane.drain(harness.config.logger, time.Second)

	undrained := linesIn(harness.logs.String(), roleLaneUndrainedLine, nil)
	if len(undrained) != 1 || undrained[0]["pending"] != float64(messages) {
		t.Fatalf("the drain's ERROR lines are %v, want one naming %d pending:\n%s", undrained, messages, harness.logs.String())
	}
}
