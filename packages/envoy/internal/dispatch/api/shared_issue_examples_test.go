package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"testing"
)

// sharedIssueExamples is where the shared issue event examples are: the envelopes the outbox's
// TestTheBuiltIssueEnvelopesAreTheSharedFixtures builds, which the daemon's intake and
// packages/contracts read.
const sharedIssueExamples = "../../../../daemon/internal/intake/testdata/dispatch/"

// The claim and release handlers write what the shared issue event examples say they write. The
// outbox's test builds each example's event from a hand-made model.Event, so a handler that started
// writing another reason, claim or actor would change no example and fail none of the tests that
// read them. Here each write the examples show runs through its real handler - a first claim, a
// forced takeover under a person's token, a takeover of a session that ended, the holder's release
// and a person's close - and the event it records must carry its example's type, reason, claim and
// previous claim, as null or not, and notify mark; where the test's credential is the one the
// example's write used, its actor must have the example's keys, kind and proven mark too. So a
// handler's reason renamed fails here by name.
func TestTheClaimHandlersWriteWhatTheSharedIssueExamplesHold(t *testing.T) {
	live := &liveRegistry{}
	live.set("session-one", "session-two")
	handler, _, _ := claimHandler(t, live)
	key := claimIssueKey(t, handler, "todo")
	path := "/api/v1/issues/" + key
	recorded := 0
	// requireLike reads the claim or release event the write before it recorded, the one more
	// than the issue held before, and holds it to the example file.
	requireLike := func(write, file string, sameCredential bool) {
		t.Helper()
		response := dispatchRequest(t, handler, http.MethodGet, path+"/events", nil, "alice")
		if response.Code != http.StatusOK {
			t.Fatalf("read the issue's events after %s: status=%d body=%s", write, response.Code, response.Body.String())
		}
		var claims []map[string]json.RawMessage
		for _, event := range decodeBody[[]map[string]json.RawMessage](t, response) {
			if kind := string(event["type"]); kind == `"issue.claimed"` || kind == `"issue.released"` {
				claims = append(claims, event)
			}
		}
		if len(claims) != recorded+1 {
			t.Fatalf("%s recorded %d claim and release events, want one more than the %d before it", write, len(claims), recorded)
		}
		recorded++
		got, want := claims[len(claims)-1], sharedIssueExample(t, file)
		var gotPayload, wantPayload map[string]json.RawMessage
		for _, part := range []struct {
			raw  json.RawMessage
			into *map[string]json.RawMessage
		}{{got["payload"], &gotPayload}, {want["payload"], &wantPayload}} {
			if err := json.Unmarshal(part.raw, part.into); err != nil {
				t.Fatalf("decode a payload of %s: %v", write, err)
			}
		}
		for _, field := range []struct {
			name      string
			got, want json.RawMessage
		}{
			{"type", got["type"], want["type"]},
			{"notify", got["notify"], want["notify"]},
			{"reason", gotPayload["reason"], wantPayload["reason"]},
		} {
			if string(field.got) != string(field.want) {
				t.Errorf("%s recorded %s %s, and %s holds %s", write, field.name, field.got, file, field.want)
			}
		}
		for _, claim := range []string{"claim", "previous_claim"} {
			gotHeld, wantHeld := heldClaim(gotPayload[claim]), heldClaim(wantPayload[claim])
			if gotHeld != wantHeld {
				t.Errorf("%s recorded %s %s, and %s holds %s", write, claim, gotHeld, file, wantHeld)
			}
		}
		if !sameCredential {
			return
		}
		var gotActor, wantActor map[string]json.RawMessage
		if err := json.Unmarshal(got["actor"], &gotActor); err != nil {
			t.Fatalf("decode the actor %s recorded: %v", write, err)
		}
		if err := json.Unmarshal(want["actor"], &wantActor); err != nil {
			t.Fatalf("decode %s's actor: %v", file, err)
		}
		gotKeys, wantKeys := slices.Sorted(maps.Keys(gotActor)), slices.Sorted(maps.Keys(wantActor))
		if !slices.Equal(gotKeys, wantKeys) || string(gotActor["kind"]) != string(wantActor["kind"]) ||
			string(gotActor["proven"]) != string(wantActor["proven"]) {
			t.Errorf("%s recorded the actor %s, and %s holds %s: want its keys %v, kind and proven mark",
				write, got["actor"], file, want["actor"], wantKeys)
		}
	}

	if response := bearerRequest(t, handler, http.MethodPost, path+"/claim", map[string]any{
		"actor": claimActorBody("session-one", "Implementer"),
	}); response.Code != http.StatusOK {
		t.Fatalf("first claim: status=%d body=%s", response.Code, response.Body.String())
	}
	requireLike("a first claim", "issue-claimed-first.json", false)

	// A person's personal token takes the live session's claim with force, for a session outside
	// tmux its body names: the actor is that session, with the token's person as its owner.
	token := mintPersonalToken(t, handler, "alice", "takeover", nil)
	if response := agentRequest(t, handler, http.MethodPost, path+"/claim", map[string]any{
		"actor": map[string]any{"kind": "session", "id": "session-forced", "origin": map[string]any{
			"host": "worker-9", "machine": "worker-9", "cwd": "/work/widgets",
		}},
		"force": true,
	}, token.Token); response.Code != http.StatusOK {
		t.Fatalf("forced takeover: status=%d body=%s", response.Code, response.Body.String())
	}
	requireLike("a forced takeover under a person's token", "issue-claimed-forced.json", true)

	// The forced holder's session is not among the live ones, so another takes it without force.
	if response := bearerRequest(t, handler, http.MethodPost, path+"/claim", map[string]any{
		"actor": claimActorBody("session-two", "Second agent"),
	}); response.Code != http.StatusOK {
		t.Fatalf("takeover of a session that ended: status=%d body=%s", response.Code, response.Body.String())
	}
	requireLike("a takeover of a session that ended", "issue-claimed-every-field.json", false)

	if response := bearerRequest(t, handler, http.MethodDelete, path+"/claim", map[string]any{
		"actor": claimActorBody("session-two", "Second agent"),
	}); response.Code != http.StatusOK {
		t.Fatalf("the holder's release: status=%d body=%s", response.Code, response.Body.String())
	}
	requireLike("the holder's release", "issue-released-null-claim.json", false)

	if response := bearerRequest(t, handler, http.MethodPost, path+"/claim", map[string]any{
		"actor": claimActorBody("session-two", "Second agent"),
	}); response.Code != http.StatusOK {
		t.Fatalf("claim again: status=%d body=%s", response.Code, response.Body.String())
	}
	recorded++
	// A person at a browser closes the issue, which releases the claim.
	if response := dispatchRequest(t, handler, http.MethodPatch, path, map[string]string{"status": "done"}, "alice"); response.Code != http.StatusOK {
		t.Fatalf("a person's close: status=%d body=%s", response.Code, response.Body.String())
	}
	requireLike("a person's close", "issue-released-closed.json", true)
}

// sharedIssueExample is the event a shared issue event example's envelope carries.
func sharedIssueExample(t *testing.T, file string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(sharedIssueExamples + file)
	if err != nil {
		t.Fatalf("read the shared example: %v", err)
	}
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode the shared example %s: %v", file, err)
	}
	var event map[string]json.RawMessage
	if err := json.Unmarshal([]byte(envelope.Payload), &event); err != nil {
		t.Fatalf("decode the event of the shared example %s: %v", file, err)
	}
	return event
}

// heldClaim says how a payload holds a claim: absent, null, or a claim.
func heldClaim(raw json.RawMessage) string {
	switch {
	case raw == nil:
		return "absent"
	case string(raw) == "null":
		return "null"
	}
	return "a claim"
}
