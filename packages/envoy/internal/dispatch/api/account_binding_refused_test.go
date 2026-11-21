package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// A personal token the account binding refuses that outlived its address's recording is revoked
// once the binding holds, with everything it could have minted, and the broker is told, against a
// real broker. At carol's address account-a holds two tokens, one recording account-a and one
// recording no account, and account-b replaces account-a while no build without the binding is in
// the fleet and with no sign-in of this build, so nothing is revoked and no change is kept: a
// change the binding never caught up, as one made before it shipped, or through a process the fleet
// did not see. Once the binding holds, both are refused (tokenAccountReplaced, by the account the
// first records and by the time the second was made) but neither is revoked.
//
// Then a build without the binding is in the fleet. Both tokens act, as that build lets them, and
// each mints a token that records no account and was made after account-b's recording, which no
// arm of tokenAccountReplaced refuses; the one recording account-a passes an approve's re-read,
// which names no account then, and the broker makes a machine login recording no approving account,
// as it makes one from that approve; and at dave's address a token from before its recording mints
// a token too, then revokes itself, so no refused token of dave is live. A pass of the revocation
// loop then writes nothing and asks for the next within accountBindingCatchUpPoll, since the
// binding is off and an address has something to catch up.
//
// Once that build is gone and its mark has aged out, one pass revokes the two refused tokens and
// the two tokens they minted, and dave's minted token, leaves account-b's own token recording
// account-b, owes the broker account-a's revocation, naming account-b, and pays it: the broker
// refuses account-a and ends the machine login, which records no approving account. The pass asks
// for no poll, since nothing is left to catch up. A later build without the binding in the fleet
// then admits none of those tokens, and the token recording account-a mints nothing.
//
// Controls, run by hand: with CatchUpAccountBinding's refused-token sweep removed (the RefusedLive
// branch never taken, and AccountBindingCatchUpAddresses' second arm removed), the pass while off
// asks for no poll and the pass that holds revokes nothing and owes nothing, and every assertion
// after it fails; with the sweep reading only live refused tokens (its `t.revoked_at >
// people.sub_recorded_at` arm removed), dave's minted token stays live.
func TestARefusedTokenThatOutlivedItsRecordingIsRevokedWithWhatItMintedOnceTheBindingHolds(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	r.served.deps.SignInIssuer = recoveryIssuer
	mux := http.NewServeMux()
	served := register(mux, t.Context(), r.served.deps)
	t.Cleanup(func() { waitBackgroundOf(t, served) })
	const carol, dave = "carol-outlived@example.com", "dave-outlived@example.com"
	for _, email := range []string{carol, dave} {
		if err := r.people.SignInSubject(ctx, email, "account-a", "refresh-a", model.MemberFull); err != nil {
			t.Fatal(err)
		}
	}
	recordsA, idRecordsA := storeTokenOf(t, r.dispatch, carol, "carol's token recording account-a", "account-a")
	recordsNone, idRecordsNone := storeTokenOf(t, r.dispatch, carol, "carol's token recording no account", "")
	daveNone, idDaveNone := storeTokenOf(t, r.dispatch, dave, "dave's token recording no account", "")
	for _, email := range []string{carol, dave} {
		if _, err := r.dispatch.Pool.Exec(ctx, `update people set sub = 'account-b' where email = $1`, email); err != nil {
			t.Fatalf("record account-b for %s: %v", email, err)
		}
	}
	var kept, owed int
	if err := r.dispatch.Pool.QueryRow(ctx, `select (select count(*) from account_binding_replacements), (select count(*) from broker_revocations)`).Scan(&kept, &owed); err != nil || kept != 0 || owed != 0 {
		t.Fatalf("setup: after account-b replaced account-a, %d changes are kept and %d revocations owed, %v; want none of either", kept, owed, err)
	}
	holderB, idHolderB := storeTokenOf(t, r.dispatch, carol, "carol's token recording account-b", "account-b")

	use := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		return agentRequest(t, mux, http.MethodGet, "/api/v1/whoami", nil, token)
	}
	refused := func(response *httptest.ResponseRecorder) bool {
		return response.Code == http.StatusUnauthorized && strings.Contains(response.Body.String(), "TOKEN_REVOKED")
	}
	mint := func(token, name string) (plaintext, id string, response *httptest.ResponseRecorder) {
		t.Helper()
		response = agentRequest(t, mux, http.MethodPost, "/api/v1/me/agent-tokens", map[string]any{"name": name}, token)
		if response.Code != http.StatusCreated {
			return "", "", response
		}
		var made struct {
			Token string `json:"token"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &made); err != nil || made.Token == "" || made.ID == "" {
			t.Fatalf("read the token %s: %v: %s", name, err, response.Body.String())
		}
		return made.Token, made.ID, response
	}
	revoked := func(id string) bool {
		t.Helper()
		var gone bool
		if err := r.dispatch.Pool.QueryRow(ctx, `select revoked_at is not null from agent_tokens where id = $1::uuid`, id).Scan(&gone); err != nil {
			t.Fatal(err)
		}
		return gone
	}
	for name, token := range map[string]string{"carol's token recording account-a": recordsA, "carol's token recording no account": recordsNone, "dave's token recording no account": daveNone} {
		if response := use(token); !refused(response) {
			t.Fatalf("setup: with the binding holding, %s answers %d %s; want 401 TOKEN_REVOKED", name, response.Code, response.Body.String())
		}
	}

	// A build without the binding in the fleet.
	registerUnboundProcess(t, r.dispatch)
	childOfA, idChildOfA, minted := mint(recordsA, "minted by the token recording account-a")
	if childOfA == "" {
		t.Fatalf("beside a build without the binding, the token recording account-a minted %d %s; want 201", minted.Code, minted.Body.String())
	}
	childOfNone, idChildOfNone, minted := mint(recordsNone, "minted by the token recording no account")
	if childOfNone == "" {
		t.Fatalf("beside a build without the binding, the token recording no account minted %d %s; want 201", minted.Code, minted.Body.String())
	}
	childOfDave, idChildOfDave, minted := mint(daveNone, "minted by dave's token")
	if childOfDave == "" {
		t.Fatalf("beside a build without the binding, dave's token minted %d %s; want 201", minted.Code, minted.Body.String())
	}
	if response := agentRequest(t, mux, http.MethodDelete, "/api/v1/me/agent-tokens/"+idDaveNone, nil, daveNone); response.Code >= 300 {
		t.Fatalf("dave's token revoking itself answered %d %s; want it revoked", response.Code, response.Body.String())
	}
	owner := carol
	standing := model.Actor{Kind: "session", Owner: &owner, ID: carol, Act: &model.ActorAct{TokenID: idRecordsA}}
	if subject, _, err := served.approverStanding(httptest.NewRequest(http.MethodPost, "/api/v1/credential-requests/x/approve", nil), standing); err != nil || subject != "" {
		t.Fatalf("beside a build without the binding, an approve's re-read of the token recording account-a answered %q, %v; want it to stand naming no account", subject, err)
	}
	login := testerR26PoolLogin(t, r, carol)
	if next := served.revokeOwedBrokerCredentials(ctx); next.IsZero() || time.Until(next) > accountBindingCatchUpPoll {
		t.Errorf("a pass while the binding is off asked for the next at %v; want one within %s, since an address has something to catch up", next, accountBindingCatchUpPoll)
	}
	if revoked(idRecordsA) || revoked(idChildOfA) || !testerR26LoginLive(t, r, login) {
		t.Fatalf("a pass while the binding is off revoked a token or ended the machine login; want nothing written")
	}

	// The build leaves and its mark ages out; one pass catches up.
	clearUnboundFleet(t, r.dispatch)
	if next := served.revokeOwedBrokerCredentials(ctx); !next.IsZero() {
		t.Errorf("the pass that caught up and paid asked for the next at %v; want none, since nothing is left to catch up", next)
	}
	tokens := map[string]struct{ plaintext, id string }{
		"carol's token recording account-a":                                {recordsA, idRecordsA},
		"carol's token recording no account":                               {recordsNone, idRecordsNone},
		"the token the token recording account-a minted while it was off":  {childOfA, idChildOfA},
		"the token the token recording no account minted while it was off": {childOfNone, idChildOfNone},
		"the token dave's token minted before it revoked itself":           {childOfDave, idChildOfDave},
	}
	for name, token := range tokens {
		if !revoked(token.id) {
			t.Errorf("once the binding holds, the pass left %s live; want it revoked", name)
		}
		if response := use(token.plaintext); !refused(response) {
			t.Errorf("once the binding holds, %s answers %d %s; want 401 TOKEN_REVOKED", name, response.Code, response.Body.String())
		}
	}
	if response := use(holderB); revoked(idHolderB) || response.Code != http.StatusOK {
		t.Errorf("control: once the binding holds, carol's token recording account-b is revoked %t and answers %d %s; want it live and 200", revoked(idHolderB), response.Code, response.Body.String())
	}
	if !r.refused(t, carol, "account-a") || testerR26LoginLive(t, r, login) {
		t.Errorf("once the binding holds, the pass left the broker refusing account-a at %s %t and the machine login recording no approving account live %t; want account-a refused and the login ended",
			carol, r.refused(t, carol, "account-a"), testerR26LoginLive(t, r, login))
	}
	if owing, err := r.revocations.Owing(ctx, carol); err != nil || len(owing) != 0 {
		t.Errorf("after the pass, %s owes %+v, %v; want account-a's revocation paid", carol, owing, err)
	}

	// A later build without the binding in the fleet admits none of them.
	registerUnboundProcess(t, r.dispatch)
	for name, token := range tokens {
		if response := use(token.plaintext); !refused(response) {
			t.Errorf("beside a later build without the binding, %s answers %d %s; want 401 TOKEN_REVOKED: a revoked token acts on no build", name, response.Code, response.Body.String())
		}
	}
	if plaintext, _, response := mint(recordsA, "minted by the token recording account-a in a later overlap"); plaintext != "" || !refused(response) {
		t.Errorf("beside a later build without the binding, the token recording account-a minted %d %s; want 401 TOKEN_REVOKED", response.Code, response.Body.String())
	}
}
