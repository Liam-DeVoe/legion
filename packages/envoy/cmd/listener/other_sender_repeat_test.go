// What a caller learns by repeating another sender's key: that the key is taken, and not what the
// other sender wrote.

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
)

// TestARepeatByAnotherSenderLearnsTheKeyIsTakenAndNotTheText is check c-other-sender-repeat.
// Session A publishes to a team topic, sends to a live session, and publishes to a role whose
// holder is live, each under its own key K and with words of its own. Session B then sends the
// same request under K with words of its own: once naming its own source_session, and once with a
// secrets-broker token that proves B as well. Each answer is 200 with duplicate and the stored
// copy's event_id and times, and carries no payload, payload_summary, message or source_session,
// and none of A's words. A third row stores a copy that names no session and repeats it with no
// session and a token that proves B: the token's session is not the copy's, so it learns no more.
//
// The control is A's own repeat under K, which gets the whole stored copy.
func TestARepeatByAnotherSenderLearnsTheKeyIsTakenAndNotTheText(t *testing.T) {
	rig := newSenderLimitRig(t)
	const alice, bob = "ses_other_sender_alice", "ses_other_sender_bob"

	type route struct {
		name string
		// path and fields are the request's route and the body fields that name where it goes.
		path   string
		fields map[string]any
		// stored is the one copy the stream holds for the request, as the route answers it.
		stored func(t *testing.T) contracts.Envelope
	}
	// routes is the three routes, each to a topic, a target or a role of its own.
	routes := func(t *testing.T) []route {
		topic := "notifications.team.other-sender-" + id.New()
		target := "ses_other_sender_target_" + id.New()
		rig.live(t, target, nil)
		holder := "ses_other_sender_holder_" + id.New()
		role := "other-sender-" + id.New()
		claimRoleFor(t, rig.deps.registry, rig.deps.sessions, holder, role, nil)
		return []route{
			{"topic publish", "/v1/messages/publish", map[string]any{"topic": topic}, func(t *testing.T) contracts.Envelope {
				return heldCopy(t, rig.harness.client, topic)
			}},
			{"send", "/v1/messages/send", map[string]any{"target_session": target}, func(t *testing.T) contracts.Envelope {
				return heldCopy(t, rig.harness.client, contracts.LegacyAgentSubject(target))
			}},
			{"role publish", "/v1/messages/publish", map[string]any{"topic": contracts.RoleTopicPrefix + role}, func(t *testing.T) contracts.Envelope {
				return heldForward(t, rig.harness.client, holder)
			}},
		}
	}
	// request is r's request under key, from session (none when empty), with words, and with a
	// broker token proving token when token is not empty.
	request := func(t *testing.T, r route, key, session, words, token string) heldAnswer {
		t.Helper()
		body := map[string]any{"message": words, "idempotency_key": key}
		for field, value := range r.fields {
			body[field] = value
		}
		if session != "" {
			body["source_session"] = session
		}
		req := postJSON(r.path, messageJSON(t, body, nil))
		if token != "" {
			req = req.WithContext(withBrokerCaller(req.Context(), brokertoken.Caller{Session: token}))
		}
		return held(t, serveLimited(t, rig.mux, rig.harness.logs.String, req))
	}
	// learnsOnlyTheKey fails unless answer is 200 with duplicate, kept's event_id and times, and
	// none of what kept's sender wrote or who it is.
	learnsOnlyTheKey := func(t *testing.T, what string, answer heldAnswer, kept contracts.Envelope, words string) {
		t.Helper()
		answer.requireStatus(t, what, http.StatusOK)
		if !answer.Duplicate {
			t.Fatalf("%s answered %s, want duplicate", what, answer.raw)
		}
		got := answer.envelope
		if got.EventID != kept.EventID || got.IssuedAt != kept.IssuedAt {
			t.Fatalf("%s answered event %s issued_at %d, want the stored copy's %s and %d", what, got.EventID, got.IssuedAt, kept.EventID, kept.IssuedAt)
		}
		if got.Payload != "" || got.PayloadSummary != "" || got.SourceSession != "" || got.Sender != nil || strings.Contains(answer.raw, words) {
			t.Fatalf("%s answered %s, which carries what the other sender wrote or who it is", what, answer.raw)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(answer.raw), &fields); err != nil {
			t.Fatalf("%s: decode %s: %v", what, answer.raw, err)
		}
		if _, ok := fields["message"]; ok {
			t.Fatalf("%s answered %s, which carries a message", what, answer.raw)
		}
	}

	t.Run("control: the same sender's repeat gets the whole stored copy", func(t *testing.T) {
		for _, r := range routes(t) {
			t.Run(r.name, func(t *testing.T) {
				key := uuid.NewString()
				words := "alice's own words " + id.New()
				request(t, r, key, alice, words, "").requireStatus(t, "A's "+r.name, http.StatusOK)
				kept := r.stored(t)
				if !strings.Contains(kept.PayloadSummary+kept.Payload, words) {
					t.Fatalf("the stream holds %+v for A's %s, want A's words in it", kept, r.name)
				}
				request(t, r, key, alice, "alice again", "").requireHeld(t, "A's repeated "+r.name, kept, true)
			})
		}
	})

	for _, row := range []struct {
		name, token string
	}{
		{"case: B naming its own session learns the key is taken and not A's text", ""},
		{"case: B with a token that proves B learns the key is taken and not A's text", bob},
	} {
		t.Run(row.name, func(t *testing.T) {
			for _, r := range routes(t) {
				t.Run(r.name, func(t *testing.T) {
					key := uuid.NewString()
					words := "alice's own words " + id.New()
					request(t, r, key, alice, words, "").requireStatus(t, "A's "+r.name, http.StatusOK)
					learnsOnlyTheKey(t, "B's repeat of A's "+r.name, request(t, r, key, bob, "bob's words", row.token), r.stored(t), words)
				})
			}
		})
	}

	t.Run("case: a token that proves B reads no more of a copy that names no session", func(t *testing.T) {
		for _, r := range routes(t) {
			t.Run(r.name, func(t *testing.T) {
				key := uuid.NewString()
				words := "sessionless words " + id.New()
				request(t, r, key, "", words, "").requireStatus(t, "the sessionless "+r.name, http.StatusOK)
				learnsOnlyTheKey(t, "B's tokened repeat of the sessionless "+r.name, request(t, r, key, "", "bob's words", bob), r.stored(t), words)
			})
		}
	})
}
