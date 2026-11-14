package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/envoy"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// INFRA-626: a session the Envoy listener knows and that is not running is a recipient, not a
// refusal. The listener stores the message for it and hands it over when it runs again, and
// Dispatch records that attempt `queued`. A session the listener knows nothing about is refused
// exactly as before, by the same text.

// fakeSession is one session the queueing listener knows. stopped is a session the listener knows
// by its known-session record alone, with no registry entry: GET /v1/registry/<id> answers it 200
// with state "stopped" only when the include list names stopped, and 404 otherwise, as the real
// route answers. crashed is a session whose registry entry still stands while its plugin has gone:
// the registry reads it live, and the send's probe finds nobody, so the send answers it stopped.
type fakeSession struct {
	title        string
	capabilities []string
	stopped      bool
	crashed      bool
}

// queueingListener is a fake Envoy listener that knows some sessions as running and some as
// stopped, answers the registry's include list as the real one does, and records every registry
// read and every send so a test can say what Dispatch asked for.
type queueingListener struct {
	url      string
	sessions map[string]fakeSession
	mu       sync.Mutex
	sends    []map[string]any
	// stoppedReads counts the registry reads that asked for stopped sessions too.
	stoppedReads int
	// registryOnlyReads counts the registry reads that did not.
	registryOnlyReads int
}

const stoppedLastSeen = 1762000000000

func newQueueingListener(t *testing.T, sessions map[string]fakeSession) *queueingListener {
	t.Helper()
	listener := &queueingListener{sessions: sessions}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions":
			listener.writeSessions(w)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/registry/"):
			listener.writeRegistry(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/send":
			listener.writeSend(t, w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found"}`))
		}
	}))
	t.Cleanup(server.Close)
	listener.url = server.URL
	return listener
}

// liveRow is a session the registry holds an entry for, as GET /v1/sessions and
// GET /v1/registry/<id> answer it.
func liveRow(id string, session fakeSession) map[string]any {
	return map[string]any{
		"session_id": id, "title": session.title, "machine_id": "m1", "host": "m1",
		"capabilities": session.capabilities, "topics": []string{}, "roles": []string{},
		"last_seen": stoppedLastSeen, "state": envoy.StateLive,
	}
}

// writeSessions answers the registry's list, which never holds a stopped session.
func (l *queueingListener) writeSessions(w http.ResponseWriter) {
	rows := []map[string]any{}
	for id, session := range l.sessions {
		if !session.stopped {
			rows = append(rows, liveRow(id, session))
		}
	}
	_ = json.NewEncoder(w).Encode(rows)
}

func (l *queueingListener) writeRegistry(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/v1/registry/")
	withStopped := slices.Contains(strings.Split(r.URL.Query().Get("include"), ","), envoy.StateStopped)
	l.mu.Lock()
	if withStopped {
		l.stoppedReads++
	} else {
		l.registryOnlyReads++
	}
	l.mu.Unlock()
	session, known := l.sessions[sessionID]
	if !known || (session.stopped && !withStopped) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no live session ` + sessionID + `","state":"gone"}`))
		return
	}
	if !session.stopped {
		_ = json.NewEncoder(w).Encode(liveRow(sessionID, session))
		return
	}
	// The stopped shape carries no port, host, machine, dir or roles, as the record holds none.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_id": sessionID, "state": envoy.StateStopped, "last_seen": stoppedLastSeen,
		"title": session.title, "capabilities": session.capabilities, "topics": []string{},
	})
}

func (l *queueingListener) writeSend(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var request map[string]any
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("decode a send: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	target, _ := request["target_session"].(string)
	session, known := l.sessions[target]
	if !known {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no live session ` + target + `","state":"gone"}`))
		return
	}
	stopped := session.stopped || session.crashed
	if stopped && request["connected_only"] == true {
		// The listener stores nothing for a connected_only send to a stopped target, so this
		// send is not recorded either.
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"the session is stopped"}`))
		return
	}
	l.mu.Lock()
	l.sends = append(l.sends, request)
	l.mu.Unlock()
	// recipient_state is the registry's own age reading, which a session known only by its record
	// reads as gone; state is the probe's, which wins.
	answer := map[string]any{
		"event_id": "envelope-" + target, "recipient": target,
		"recipient_state": envoy.StateLive, "state": envoy.SendStateConnected,
	}
	if stopped {
		answer["state"], answer["last_seen"] = envoy.SendStateStopped, stoppedLastSeen
	}
	if session.stopped {
		answer["recipient_state"] = envoy.StateGone
	}
	_ = json.NewEncoder(w).Encode(answer)
}

// sentTo is every send the listener took for target, in order.
func (l *queueingListener) sentTo(target string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	took := []map[string]any{}
	for _, send := range l.sends {
		if send["target_session"] == target {
			took = append(took, send)
		}
	}
	return took
}

// reads is how many registry reads asked for stopped sessions, and how many did not.
func (l *queueingListener) reads() (stopped, registryOnly int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stoppedReads, l.registryOnlyReads
}

// stoppedAndLive is the listener every test below runs against: s1 stopped but known, s2
// running, and crashed, whose registry entry is still in place while its process has gone.
func stoppedAndLive(t *testing.T) *queueingListener {
	t.Helper()
	modes := []string{"aside", "btw", "steer"}
	return newQueueingListener(t, map[string]fakeSession{
		"s1":      {title: "planner", capabilities: modes, stopped: true},
		"s2":      {title: "tester", capabilities: modes},
		"crashed": {title: "crashed", capabilities: modes, crashed: true},
	})
}

// A person's direct message to a stopped but known session reaches the listener, which stores it
// for that session, and the attempt is recorded queued - never failed "no live session", which is
// what Dispatch recorded before this change and what told the person their message was thrown
// away. The receipt the conversation renders carries the same word.
func TestDirectMessageToAStoppedSessionIsQueued(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)

	created := postAgentMessage(t, handler, "s1", map[string]any{
		"body": "Status when you are back?", "delivery": "aside",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("direct message to a stopped session: status=%d body=%s", created.Code, created.Body.String())
	}
	message := decodeBody[model.Message](t, created)
	if len(message.Deliveries) != 1 || message.Deliveries[0].State != "queued" ||
		message.Deliveries[0].Error != nil || message.Deliveries[0].SessionID != "s1" {
		t.Fatalf("the answer's deliveries = %#v, want one queued attempt pinned to s1", message.Deliveries)
	}
	if attempts := messageAttempts(t, database, message.ID); len(attempts) != 1 ||
		attempts[0].State != "queued" || attempts[0].EnvelopeID == nil {
		t.Fatalf("the stored attempts = %#v, want one queued attempt carrying its envelope", attempts)
	}
	if sends := listener.sentTo("s1"); len(sends) != 1 {
		t.Fatalf("the listener took %d sends for s1, want exactly one", len(sends))
	} else if _, asked := sends[0]["connected_only"]; asked {
		t.Fatalf("an ordinary direct message asked for a connected session: %#v", sends[0])
	}
	if receipts := messageDeliveryReceiptRows(t, database, message.ID); len(receipts) != 1 ||
		receipts[0].State != "queued" {
		t.Fatalf("the delivery receipts = %#v, want one carrying queued", receipts)
	}
	// The registry-only read is what an issue claim's holder liveness, the route roster and role
	// resolution ask for; a delivery asks for the stopped sessions too, or it would never find
	// this session at all.
	if stopped, _ := listener.reads(); stopped == 0 {
		t.Fatalf("the delivery made no registry read that asks for stopped sessions")
	}
}

// A mention of a stopped but known session is queued for it too, on one send: the mention's own.
// The create answers with the attempt pending, pinned to the session, and the mention sender
// settles it behind the answer.
func TestMentionOfAStoppedSessionIsQueuedOnOneSend(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)
	issue := createInteractionIssue(t, handler, "QUE", "A mention of a stopped session", "before")

	comment := decodeMentionedComment(t, postMentionedComment(t, handler, issue.Key, map[string]any{
		"body":     "@s1 pick this up when you are back",
		"mentions": []map[string]string{{"target": "session:s1"}},
	}))
	if len(comment.Deliveries) != 1 || comment.Deliveries[0].SessionID == nil || *comment.Deliveries[0].SessionID != "s1" {
		t.Fatalf("the mention's deliveries = %#v, want one attempt pinned to s1", comment.Deliveries)
	}
	attempts := settledCommentDeliveries(t, database, comment.ID)
	if len(attempts) != 1 || attempts[0].State != "queued" || attempts[0].Error != nil ||
		attempts[0].SessionID == nil || *attempts[0].SessionID != "s1" || attempts[0].EnvelopeID == nil {
		t.Fatalf("the mention's settled attempts = %#v, want one queued attempt pinned to s1 carrying its envelope", attempts)
	}
	if sends := listener.sentTo("s1"); len(sends) != 1 {
		t.Fatalf("the listener took %d sends for s1, want exactly one", len(sends))
	}
}

// A broadcast that names a stopped but known session beside a running one reaches both: the
// stopped one is a recipient with a message and a queued attempt of its own, and it is in no
// exclusion list.
func TestBroadcastQueuesForAStoppedRecipientAndExcludesItFromNothing(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, _ := newTargetedMessageHandler(t, listener.url)

	response := postBroadcast(t, handler, map[string]any{
		"body": "Stand down and report.", "delivery": "steer", "session_ids": []string{"s1", "s2"},
	}, "alice")
	if response.Code != http.StatusCreated {
		t.Fatalf("broadcast: status=%d body=%s", response.Code, response.Body.String())
	}
	created := decodeBody[broadcastResponse](t, response)
	if len(created.Excluded) != 0 {
		t.Fatalf("excluded = %#v, want nothing excluded: both sessions are known", created.Excluded)
	}
	// A broadcast answers once it is committed, each recipient's delivery behind the answer.
	settled := awaitBroadcastDeliveries(t, handler, created.ID)
	states := map[string]string{}
	for _, recipient := range settled.Recipients {
		if len(recipient.Message.Deliveries) != 1 {
			t.Fatalf("%s's attempts = %#v, want one", recipient.SessionID, recipient.Message.Deliveries)
		}
		states[recipient.SessionID] = recipient.Message.Deliveries[0].State
	}
	if len(states) != 2 || states["s1"] != "queued" || states["s2"] != "sent" {
		t.Fatalf("recipient states = %#v, want s1 queued and s2 sent", states)
	}
	if sends := listener.sentTo("s1"); len(sends) != 1 {
		t.Fatalf("the listener took %d sends for the stopped recipient, want one", len(sends))
	}
}

// The send's own answer decides, not the registry read before it: a session whose entry was still
// in place when Dispatch resolved it, and whose process had gone by the send, is queued too.
func TestASendAnsweredStoppedIsQueuedThoughTheRegistryReadFoundItLive(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)

	created := postAgentMessage(t, handler, "crashed", map[string]any{
		"body": "Are you still there?", "delivery": "aside",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("direct message to a crashed session: status=%d body=%s", created.Code, created.Body.String())
	}
	message := decodeBody[model.Message](t, created)
	if attempts := messageAttempts(t, database, message.ID); len(attempts) != 1 || attempts[0].State != "queued" {
		t.Fatalf("the stored attempts = %#v, want one queued attempt", attempts)
	}
}

// A session Envoy knows nothing about is refused exactly as it always was, by the text
// retryableDeliveryFailure and the dashboard both key on, and is still excluded from a broadcast
// with the reason they key on there.
func TestASessionTheListenerDoesNotKnowIsStillRefusedByTheSameText(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)

	created := postAgentMessage(t, handler, "ghost", map[string]any{
		"body": "Anyone there?", "delivery": "aside",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("direct message to an unknown session: status=%d body=%s", created.Code, created.Body.String())
	}
	message := decodeBody[model.Message](t, created)
	attempts := messageAttempts(t, database, message.ID)
	if len(attempts) != 1 || attempts[0].State != "failed" || attempts[0].Error == nil ||
		*attempts[0].Error != "no live session ghost" {
		t.Fatalf("the stored attempts = %#v, want one failed attempt reading \"no live session ghost\"", attempts)
	}
	if sends := listener.sentTo("ghost"); len(sends) != 0 {
		t.Fatalf("the listener took %d sends for a session it does not know, want none", len(sends))
	}

	response := postBroadcast(t, handler, map[string]any{
		"body": "Stand down.", "delivery": "steer", "session_ids": []string{"s1", "ghost"},
	}, "alice")
	if response.Code != http.StatusCreated {
		t.Fatalf("broadcast: status=%d body=%s", response.Code, response.Body.String())
	}
	broadcast := decodeBody[broadcastResponse](t, response)
	if len(broadcast.Excluded) != 1 || broadcast.Excluded[0].SessionID != "ghost" ||
		broadcast.Excluded[0].Reason != "no live session" {
		t.Fatalf("excluded = %#v, want ghost alone, for no live session", broadcast.Excluded)
	}
	if len(broadcast.Recipients) != 1 || broadcast.Recipients[0].SessionID != "s1" {
		t.Fatalf("recipients = %#v, want the stopped but known session alone", broadcast.Recipients)
	}
}

// A queued attempt is the session's turn to take: the listener handed it the message, so the
// accept route must not answer ACCEPT_FAILED, which it answers for an attempt the person was told
// had failed.
func TestASessionAcceptsAQueuedAttemptAsItsTurn(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)

	created := postAgentMessage(t, handler, "s1", map[string]any{
		"body": "Take this as your turn.", "delivery": "aside",
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("direct message to a stopped session: status=%d body=%s", created.Code, created.Body.String())
	}
	message := decodeBody[model.Message](t, created)
	if attempts := messageAttempts(t, database, message.ID); len(attempts) != 1 || attempts[0].State != "queued" {
		t.Fatalf("the stored attempts = %#v, want one queued attempt to accept", attempts)
	}

	accepted := acceptDelivery(t, handler, message.ID, 1, "s1")
	if accepted.Code != http.StatusOK {
		t.Fatalf("accept a queued attempt: status=%d code=%s body=%s",
			accepted.Code, responseCode(t, accepted), accepted.Body.String())
	}
	took := decodeBody[acceptedAttempt](t, accepted)
	if took.State != "queued" || took.AcceptedAs == nil || *took.AcceptedAs != acceptedAsUserTurn ||
		took.Body != "Take this as your turn." {
		t.Fatalf("the accepted attempt = %#v, want the queued attempt taken as a user turn", took)
	}
}

// The delivery sweep resumes and settles the attempts a dead sender left pending. A queued
// attempt is not one of them - the listener took its message - so a pass leaves it as it is, even
// on a message whose issue has closed, which is what the sweep settles failed.
func TestTheSweepLeavesAQueuedAttemptAlone(t *testing.T) {
	listener := stoppedAndLive(t)
	_, database, _, served := newRegisteredTestServer(t, testServerOptions{envoyURL: listener.url})
	ctx := context.Background()
	var messageID string
	if err := database.Pool.QueryRow(ctx, `
		insert into messages (author, body, target)
		values ('{"kind":"user","id":"alice"}', 'Queued and left alone.', 'session:s1')
		returning id::text
	`).Scan(&messageID); err != nil {
		t.Fatalf("seed the message: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into message_deliveries (message_id, attempt, delivery, session_id, state, envelope_id,
		                                claimed_at, created_at, requested_by)
		values ($1::uuid, 1, 'steer', 's1', 'queued', 'envelope-s1',
		        now() - interval '10 minutes', now() - interval '10 minutes', '{"kind":"user","id":"alice"}')
	`, messageID); err != nil {
		t.Fatalf("seed the queued attempt: %v", err)
	}

	if err := served.resumeOwedDeliveries(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	attempts := messageAttempts(t, database, messageID)
	if len(attempts) != 1 || attempts[0].State != "queued" || attempts[0].Error != nil {
		t.Fatalf("after the sweep the attempts = %#v, want the one queued attempt untouched", attempts)
	}
	if sends := listener.sentTo("s1"); len(sends) != 0 {
		t.Fatalf("the sweep sent %d frames for a queued attempt, want none", len(sends))
	}
}

// An attempt whose sender died between the commit and the settle is resumed by the delivery
// sweep, which asks for a connected session only if the send that opened the attempt did. So the
// switch's own attempt, resumed against a stopped target, is settled failed and nothing is
// stored for the session, while the same attempt without the flag is queued. The flag lives on
// the row (message_deliveries.connected_only), because the sweep never saw the request.
func TestTheSweepResumesAStrandedSwitchAsConnectedOnly(t *testing.T) {
	for _, test := range []struct {
		name          string
		connectedOnly bool
		wantState     string
		wantSends     int
	}{
		{name: "a stranded switch", connectedOnly: true, wantState: "failed", wantSends: 0},
		{name: "a stranded ordinary send", connectedOnly: false, wantState: "queued", wantSends: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener := stoppedAndLive(t)
			_, database, _, served := newRegisteredTestServer(t, testServerOptions{envoyURL: listener.url})
			ctx := context.Background()
			var messageID string
			if err := database.Pool.QueryRow(ctx, `
				insert into messages (author, body, target)
				values ('{"kind":"user","id":"alice"}', '/model claude-opus', 'session:s1')
				returning id::text
			`).Scan(&messageID); err != nil {
				t.Fatalf("seed the message: %v", err)
			}
			// The attempt its own request committed and never settled: pending, claimed ten
			// minutes ago, which is what the sweep takes.
			if _, err := database.Pool.Exec(ctx, `
				insert into message_deliveries (message_id, attempt, delivery, session_id, state,
				                                claimed_at, created_at, requested_by, connected_only)
				values ($1::uuid, 1, 'steer', 's1', 'pending',
				        now() - interval '10 minutes', now() - interval '10 minutes',
				        '{"kind":"user","id":"alice"}', $2)
			`, messageID, model.ConnectedOnlyColumn(test.connectedOnly)); err != nil {
				t.Fatalf("strand the attempt: %v", err)
			}

			if err := served.resumeOwedDeliveries(ctx); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			attempts := messageAttempts(t, database, messageID)
			if len(attempts) != 1 || attempts[0].State != test.wantState ||
				attempts[0].ConnectedOnly != test.connectedOnly {
				t.Fatalf("the resumed attempt = %#v, want one %s attempt carrying connected_only=%v",
					attempts, test.wantState, test.connectedOnly)
			}
			if test.wantState == "failed" && (attempts[0].Error == nil || *attempts[0].Error != "the session is stopped") {
				t.Fatalf("the refused switch's error = %v, want \"the session is stopped\"", attempts[0].Error)
			}
			sends := listener.sentTo("s1")
			if len(sends) != test.wantSends {
				t.Fatalf("the listener stored %d sends, want %d", len(sends), test.wantSends)
			}
			if test.wantSends == 1 && sends[0]["connected_only"] == true {
				t.Fatalf("an ordinary resumed send asked for a connected session: %#v", sends[0])
			}
		})
	}
}

// The Agents page's model and effort switch keeps its refusal: it asks the listener for a session
// that is running now (connected_only), and the 409 it meets settles the attempt failed with the
// listener's own reason. Nothing is stored for the session, so nothing applies at an unknown
// later time.
func TestTheModelSwitchToAStoppedSessionIsRefusedAndQueuesNothing(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)

	created := postAgentMessage(t, handler, "s1", map[string]any{
		"body": "/model claude-opus", "delivery": "steer", "connected_only": true,
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("the switch: status=%d body=%s", created.Code, created.Body.String())
	}
	message := decodeBody[model.Message](t, created)
	attempts := messageAttempts(t, database, message.ID)
	if len(attempts) != 1 || attempts[0].State != "failed" || attempts[0].Error == nil ||
		*attempts[0].Error != "the session is stopped" {
		t.Fatalf("the switch's attempts = %#v, want one failed attempt reading \"the session is stopped\"", attempts)
	}
	if sends := listener.sentTo("s1"); len(sends) != 0 {
		t.Fatalf("the listener stored %d sends for the refused switch, want none", len(sends))
	}

	// The same switch to a running session is sent, under the same flag, and recorded sent.
	live := postAgentMessage(t, handler, "s2", map[string]any{
		"body": "/model claude-opus", "delivery": "steer", "connected_only": true,
	}, "alice")
	if live.Code != http.StatusCreated {
		t.Fatalf("the switch to a running session: status=%d body=%s", live.Code, live.Body.String())
	}
	running := decodeBody[model.Message](t, live)
	if attempts := messageAttempts(t, database, running.ID); len(attempts) != 1 || attempts[0].State != "sent" {
		t.Fatalf("the running session's attempts = %#v, want one sent attempt", attempts)
	}
	sends := listener.sentTo("s2")
	if len(sends) != 1 || sends[0]["connected_only"] != true {
		t.Fatalf("the switch's send = %#v, want one carrying connected_only", sends)
	}
}

// A refused switch keeps its refusal on every later attempt of it, whichever button the person
// presses on the agent card: Retry sends it in the mode it was written in, Use BTW instead sends
// it in another, and both ask the listener for a session that is running now, because the
// message asked for one. Each attempt is settled failed with the listener's own reason and the
// listener stores nothing, so no `/model` line waits for the session to run again.
func TestARetryOfARefusedSwitchIsRefusedAgainAndQueuesNothing(t *testing.T) {
	listener := stoppedAndLive(t)
	handler, database := newTargetedMessageHandler(t, listener.url)

	created := postAgentMessage(t, handler, "s1", map[string]any{
		"body": "/model claude-opus", "delivery": "steer", "connected_only": true,
	}, "alice")
	if created.Code != http.StatusCreated {
		t.Fatalf("the switch: status=%d body=%s", created.Code, created.Body.String())
	}
	message := decodeBody[model.Message](t, created)

	for _, press := range []struct {
		name     string
		delivery string
	}{
		{name: "Retry", delivery: "steer"},
		{name: "Use BTW instead", delivery: "btw"},
	} {
		again := dispatchRequest(t, handler, http.MethodPost, "/api/v1/messages/"+message.ID+"/deliveries",
			map[string]any{"delivery": press.delivery}, "alice")
		if again.Code != http.StatusCreated {
			t.Fatalf("%s: status=%d body=%s", press.name, again.Code, again.Body.String())
		}
		attempt := decodeBody[model.MessageDelivery](t, again)
		if attempt.State != "failed" || attempt.Error == nil || *attempt.Error != "the session is stopped" {
			t.Fatalf("%s answered %#v, want a failed attempt reading \"the session is stopped\"", press.name, attempt)
		}
	}

	attempts := messageAttempts(t, database, message.ID)
	if len(attempts) != 3 {
		t.Fatalf("the switch's attempts = %#v, want three", attempts)
	}
	for _, attempt := range attempts {
		if attempt.State != "failed" || !attempt.ConnectedOnly ||
			attempt.Error == nil || *attempt.Error != "the session is stopped" {
			t.Fatalf("attempt %d = %#v, want it failed, carrying connected_only, reading \"the session is stopped\"",
				attempt.Attempt, attempt)
		}
	}
	if sends := listener.sentTo("s1"); len(sends) != 0 {
		t.Fatalf("the listener stored %d sends for the refused switch and its retries, want none", len(sends))
	}
}

// The claim holder's liveness read stays the registry's own: a stopped session holds no claim, so
// it must read as no holder at all and the read must not ask for stopped sessions. A delivery's
// read of the same session, beside it, finds it.
func TestTheClaimHoldersLivenessReadNeverAsksForStoppedSessions(t *testing.T) {
	listener := stoppedAndLive(t)
	_, _, deps := newTestServer(t, testServerOptions{envoyURL: listener.url})
	direct := directServer(deps)
	ctx := context.Background()

	liveness, err := direct.fetchHolderLiveness(ctx, model.IssueClaim{Actor: model.Actor{Kind: "session", ID: "s1"}})
	if err != nil {
		t.Fatalf("read the holder's liveness: %v", err)
	}
	if !liveness.fetched || liveness.holder != nil {
		t.Fatalf("liveness of a stopped holder = %#v, want fetched with no holder", liveness)
	}
	stopped, registryOnly := listener.reads()
	if stopped != 0 {
		t.Fatalf("the holder's liveness made %d registry reads asking for stopped sessions, want none", stopped)
	}
	if registryOnly != 1 {
		t.Fatalf("the holder's liveness made %d registry-only reads, want one", registryOnly)
	}

	// The same session, resolved for a delivery, is found: the two reads differ only in what
	// they ask for.
	sessionID, _, resolveError := direct.resolveDeliveryTarget(ctx, model.Route{Kind: "session", ID: "s1"}, "steer")
	if sessionID != "s1" || resolveError != "" {
		t.Fatalf("a delivery resolved s1 as (%q, %q), want it resolved with no error", sessionID, resolveError)
	}
}
