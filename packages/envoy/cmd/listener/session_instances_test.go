package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
)

// twoProcessRig is one listener's routes and session relay over a real NATS server the test
// starts (newRelayRig), and one session id that two processes register under, each with a client
// instance id of its own: process A first, process B after it.
type twoProcessRig struct {
	*relayRig
	sessionID string
	role      string
	instanceA string
	instanceB string
}

func newTwoProcessRig(t *testing.T, row string) *twoProcessRig {
	t.Helper()
	rig := newRelayRig(t)
	// The publish route records each holder it confirmed a role send for, as main wires it.
	rig.deps.confirmed = newConfirmedRecord(t)
	run := id.New()
	return &twoProcessRig{
		relayRig:  rig,
		sessionID: "ses_two_processes_" + row + "_" + run,
		role:      "two-processes-" + row + "-" + run,
		instanceA: "process-a-" + run,
		instanceB: "process-b-" + run,
	}
}

func (r *twoProcessRig) send(method, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	r.mux.ServeHTTP(recorder, request)
	return recorder
}

// register is the registration a process sends at every heartbeat: consumer delivery at the epoch
// of its own NATS connection, and its own client instance id.
func (r *twoProcessRig) register(t *testing.T, instance, epoch string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"session_id":         r.sessionID,
		"self_subscribed":    true,
		"topics":             []string{},
		"delivery":           map[string]any{"consumer": true, "epoch": epoch},
		"client_instance_id": instance,
	})
	if err != nil {
		t.Fatalf("encode the registration of %s by %s: %v", r.sessionID, instance, err)
	}
	return r.send(http.MethodPost, "/v1/interests/subscribe", string(body))
}

// registered fails the test unless the registration of instance is answered 200.
func (r *twoProcessRig) registered(t *testing.T, instance, epoch string) {
	t.Helper()
	if recorder := r.register(t, instance, epoch); recorder.Code != http.StatusOK {
		t.Fatalf("the registration of %s by %s: status %d %s, want 200", r.sessionID, instance, recorder.Code, recorder.Body.String())
	}
}

// claim is the soft claim of the rig's role a process makes under the session id, naming itself.
func (r *twoProcessRig) claim(instance string) *httptest.ResponseRecorder {
	return r.send(http.MethodPost, "/v1/roles/set",
		fmt.Sprintf(`{"session_id":%q,"role":%q,"soft":true,"client_instance_id":%q}`, r.sessionID, r.role, instance))
}

// claimedBy is the process the role's stored claim names, read from the bucket itself. The claim
// must be held under the rig's session id.
func (r *twoProcessRig) claimedBy(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	claim, err := r.harness.registry.RoleClaim(ctx, r.role)
	if err != nil {
		t.Fatalf("read the claim of %s: %v", r.role, err)
	}
	if claim.HolderSessionID != r.sessionID {
		t.Fatalf("the claim of %s is held by %q, want %s", r.role, claim.HolderSessionID, r.sessionID)
	}
	return claim.ClientInstanceID
}

// warnedConflicts is every WARN line naming the session that also names instance.
func (r *twoProcessRig) warnedConflicts(instance string) []map[string]any {
	var found []map[string]any
	for _, record := range warnLinesNaming(r.harness.logs.String(), r.sessionID) {
		for _, value := range record {
			if value == instance {
				found = append(found, record)
				break
			}
		}
	}
	return found
}

// TestTwoProcessesUnderOneSessionDoNotTakeItsRelayInTurns is check l13 of dispatch://INFRA-3589
// (R15). When a registration names a session id whose live record carries another client
// instance id, the newer registration takes the session and its relay, so a process that
// restarts after a crash with a new instance id registers at once and takes its role back at
// once. From then on the older instance is refused 409, naming the session and the newer
// instance, with one WARN line naming both; it gets no relay and takes no role claim. A clean
// hand-off removes the older registration first, and the registration after it is refused nothing.
//
// The relay subject is the session's own, so any process that holds it open receives what the
// listener relays there. What moves the relay is a registration: one with a new epoch restarts the
// session's consumer at its ack floor, which sends again every frame the session has not finished.
// So the rows read the relay off the listener: the session's entry keeps the newer instance and its
// epoch, a frame that instance holds is not sent again when the older instance registers, and what
// is stored after reaches the newer instance's stand-in and is acknowledged on its answer. The
// stand-ins here are bare NATS subscribers, not the plugin: that a refused plugin drops the frames
// it receives on the shared subject, and takes the relay back at its next accepted registration,
// is envoy-client's own test ("a superseded process drops the relay until a registration of its
// own is accepted again", consumer-delivery.test.ts).
func TestTwoProcessesUnderOneSessionDoNotTakeItsRelayInTurns(t *testing.T) {
	t.Run("an older process registering again is refused and the relay stays with the newer one", func(t *testing.T) {
		rig := newTwoProcessRig(t, "a")
		agent := contracts.AgentSubject(rig.sessionID)
		roleTopic := contracts.RoleTopicPrefix + rig.role

		rig.registered(t, rig.instanceA, "epoch-a")
		if recorder := rig.claim(rig.instanceA); recorder.Code != http.StatusOK {
			t.Fatalf("A's claim of %s: status %d %s, want 200", rig.role, recorder.Code, recorder.Body.String())
		}

		// B's process holds the session's relay subject on a connection of its own. It answers
		// every frame done, except one it holds, as a session still writing a message does.
		var holdNext atomic.Bool
		standIn := startSessionStandIn(t, rig.url, rig.sessionID, func(frame relayedFrame) []string {
			if holdNext.CompareAndSwap(true, false) {
				return []string{relayReplyReceived}
			}
			return answerDone(frame)
		})
		rig.registered(t, rig.instanceB, "epoch-b")
		// The relay restarts for B's new epoch. A first message, written and acknowledged, shows
		// the restart is behind it: a copy the run it replaced had sent is sent once more, and is
		// passed over here.
		first, firstSeq := rig.storeEvent(t, agent)
		requireFrames(t, standIn.collect(t, 1, 10*time.Second), first)
		awaitAcked(t, rig.consumers, rig.sessionID, firstSeq)
		settle := time.After(500 * time.Millisecond)
	settling:
		for {
			select {
			case frame := <-standIn.frames:
				if frame.item.EventID != first.EventID {
					t.Fatalf("a frame of %s reached the session before anything else was stored", frame.item.EventID)
				}
			case <-settle:
				break settling
			}
		}
		holdNext.Store(true)
		held, heldSeq := rig.storeEvent(t, agent)
		heldFrame := standIn.collect(t, 1, 10*time.Second)[0]
		requireFrames(t, []relayedFrame{heldFrame}, held)

		refused := rig.register(t, rig.instanceA, "epoch-a")
		if refused.Code != http.StatusConflict {
			t.Fatalf("A's registration after B's: status %d %s, want 409", refused.Code, refused.Body.String())
		}
		var answer struct {
			Error            string `json:"error"`
			Reason           string `json:"reason"`
			ClientInstanceID string `json:"client_instance_id"`
		}
		if err := json.Unmarshal(refused.Body.Bytes(), &answer); err != nil {
			t.Fatalf("decode the refusal %s: %v", refused.Body.String(), err)
		}
		if answer.Reason != "superseded_instance" || answer.ClientInstanceID != rig.instanceB ||
			!strings.Contains(answer.Error, rig.sessionID) || !strings.Contains(answer.Error, rig.instanceB) {
			t.Fatalf("the refusal is %s, want reason superseded_instance naming %s and the newer instance %s", refused.Body.String(), rig.sessionID, rig.instanceB)
		}
		// A refused process registers again at every heartbeat.
		if again := rig.register(t, rig.instanceA, "epoch-a"); again.Code != http.StatusConflict {
			t.Fatalf("A's next registration: status %d %s, want 409", again.Code, again.Body.String())
		}

		entry, err := rig.harness.sessions.Get(rig.sessionID)
		if err != nil {
			t.Fatalf("read the entry of %s: %v", rig.sessionID, err)
		}
		if entry.ClientInstanceID != rig.instanceB || entry.Epoch != "epoch-b" {
			t.Fatalf("the entry names instance %q at epoch %q, want B's %s at epoch-b", entry.ClientInstanceID, entry.Epoch, rig.instanceB)
		}
		// No restart for A's epoch: the frame B holds is not sent again.
		standIn.none(t, 1500*time.Millisecond)
		standIn.reply(heldFrame, relayReplyDone)
		awaitAcked(t, rig.consumers, rig.sessionID, heldSeq)

		// The older process takes no role claim, and B, the newer one, takes the role A held.
		if recorder := rig.claim(rig.instanceA); recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"holder":"`+rig.sessionID+`"`) {
			t.Fatalf("A's claim of %s after B's registration: status %d %s, want 409 naming %s", rig.role, recorder.Code, recorder.Body.String(), rig.sessionID)
		}
		if got := rig.claimedBy(t); got != rig.instanceA {
			t.Fatalf("the claim names %q after A's refused claim, want A's %s still", got, rig.instanceA)
		}
		if recorder := rig.claim(rig.instanceB); recorder.Code != http.StatusOK {
			t.Fatalf("B's claim of %s: status %d %s, want 200", rig.role, recorder.Code, recorder.Body.String())
		}
		if got := rig.claimedBy(t); got != rig.instanceB {
			t.Fatalf("the claim names %q after B's claim, want B's %s", got, rig.instanceB)
		}

		// A role send reaches B and is confirmed by B's answer.
		published := rig.send(http.MethodPost, "/v1/messages/publish",
			`{"topic":"`+roleTopic+`","message":"a message for the role's holder","source":"agent","source_session":"ses_role_sender_`+rig.instanceB+`"}`)
		if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"holder":"`+rig.sessionID+`"`) {
			t.Fatalf("the role send: status %d %s, want 200 naming holder %s", published.Code, published.Body.String(), rig.sessionID)
		}
		roleFrame := standIn.collect(t, 1, 10*time.Second)[0]
		if roleFrame.item.Topic != roleTopic {
			t.Fatalf("the frame after the role send carries topic %s, want %s", roleFrame.item.Topic, roleTopic)
		}
		awaitAcked(t, rig.consumers, rig.sessionID, roleFrame.seq)

		warned := rig.warnedConflicts(rig.instanceA)
		if len(warned) != 1 {
			t.Fatalf("%d WARN lines name %s's older instance %s, want one for the conflict however often it retried: %v", len(warned), rig.sessionID, rig.instanceA, warned)
		}
		if len(rig.warnedConflicts(rig.instanceB)) != 1 {
			t.Fatalf("the WARN line %v does not name the newer instance %s", warned[0], rig.instanceB)
		}
	})

	t.Run("a process restarting after a crash takes the relay and the role at once", func(t *testing.T) {
		rig := newTwoProcessRig(t, "b")
		agent := contracts.AgentSubject(rig.sessionID)

		rig.registered(t, rig.instanceA, "epoch-a")
		if recorder := rig.claim(rig.instanceA); recorder.Code != http.StatusOK {
			t.Fatalf("A's claim of %s: status %d %s, want 200", rig.role, recorder.Code, recorder.Body.String())
		}
		// A stops with no clean stop: no release and no deregistration. Its record and its claim's
		// renewal are both well inside the live window when B starts.
		standIn := startSessionStandIn(t, rig.url, rig.sessionID, answerDone)
		rig.registered(t, rig.instanceB, "epoch-b")
		stored, seq := rig.storeEvent(t, agent)
		requireFrames(t, standIn.collect(t, 1, 10*time.Second), stored)
		awaitAcked(t, rig.consumers, rig.sessionID, seq)

		if recorder := rig.claim(rig.instanceB); recorder.Code != http.StatusOK {
			t.Fatalf("B's claim of the role A held, inside A's live window: status %d %s, want 200 at once", recorder.Code, recorder.Body.String())
		}
		if got := rig.claimedBy(t); got != rig.instanceB {
			t.Fatalf("the claim names %q after B's claim, want B's %s", got, rig.instanceB)
		}
	})

	t.Run("a hand-off that removes the older registration first is refused nothing", func(t *testing.T) {
		rig := newTwoProcessRig(t, "c")
		agent := contracts.AgentSubject(rig.sessionID)

		rig.registered(t, rig.instanceA, "epoch-a")
		removed := rig.send(http.MethodDelete, "/v1/sessions/"+rig.sessionID+"?client_instance_id="+rig.instanceA, "")
		if removed.Code != http.StatusOK || strings.TrimSpace(removed.Body.String()) != `{"deleted":true}` {
			t.Fatalf("A's deregistration: status %d %s, want 200 {\"deleted\":true}", removed.Code, removed.Body.String())
		}
		standIn := startSessionStandIn(t, rig.url, rig.sessionID, answerDone)
		rig.registered(t, rig.instanceB, "epoch-b")
		stored, seq := rig.storeEvent(t, agent)
		requireFrames(t, standIn.collect(t, 1, 10*time.Second), stored)
		awaitAcked(t, rig.consumers, rig.sessionID, seq)

		if warned := rig.warnedConflicts(rig.instanceA); len(warned) != 0 {
			t.Fatalf("%d WARN lines name A's instance %s after a clean hand-off, want none: %v", len(warned), rig.instanceA, warned)
		}
	})
}
