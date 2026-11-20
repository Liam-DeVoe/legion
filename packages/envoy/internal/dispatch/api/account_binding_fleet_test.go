package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Every check of the account binding holds only while no build without the binding is in the
// fleet (store.AccountBindingHolds, migration 20261120222130_account_binding_fleet): such a build
// checks no account, so beside it a replaced account's credential acts there whatever this build
// does, and this build does as that build does. Account A's credentials at carol's address, which
// people records account B for, are admitted while a process at owner lock protocol 1 without the
// binding is registered: A's personal token at its use, an approve's re-read of it (naming no
// account), its mint (whose token records none), a broker token recording A at its admission, and
// the membership answer's holder for A. Once that process is gone and its mark has aged out, each
// is refused as the binding refuses it.
//
// The second half is the control, in the test: the same checks on the same rows refuse once the
// fleet holds the binding, so the first half's admissions are the fence's, not checks that never
// refuse.
func TestAReplacedAccountsCredentialsActOnlyWhileABuildWithoutTheBindingIsInTheFleet(t *testing.T) {
	const google = "https://accounts.google.com"
	handler, database, deps := newTestServer(t, testServerOptions{signInIssuer: google})
	ctx := context.Background()
	const carol = "carol@example.com"
	seedPeople(t, database, carol)
	plaintext, hash, err := newAgentToken()
	if err != nil {
		t.Fatal(err)
	}
	var idA string
	if err := database.Pool.QueryRow(ctx, `
		insert into agent_tokens (owner, name, token_hash, prefix, scopes, subject)
		values ($1, 'the token of account-a', $2, $3, $4, 'account-a') returning id::text`,
		carol, hash[:], plaintext[len("dsp_"):len("dsp_")+agentTokenPrefixLength], []string{model.ScopePerson, model.ScopeApprove}).Scan(&idA); err != nil {
		t.Fatalf("store account-a's token: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `update people set sub = 'account-b' where email = $1`, carol); err != nil {
		t.Fatalf("record account-b for carol: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into server_processes (instance, commit, signing_keys, owner_lock_protocol)
		values (gen_random_uuid(), 'unbound0build', '{fingerprint}', 1)`); err != nil {
		t.Fatalf("register a process without the binding: %v", err)
	}

	served := directServer(deps)
	owner := carol
	tokenActor := model.Actor{Kind: "session", Owner: &owner, ID: carol, Act: &model.ActorAct{TokenID: idA}}
	brokerCaller := brokertoken.Caller{Person: carol, AccountIssuer: google, AccountSubject: "account-a", CredentialCreatedAt: time.Now().Add(-time.Hour)}
	check := func(fleet string, held bool) {
		t.Helper()
		if response := agentRequest(t, handler, http.MethodGet, "/api/v1/whoami", nil, plaintext); held != (response.Code == http.StatusUnauthorized) {
			t.Errorf("%s: account-a's personal token answers %d %s; want 401 %t", fleet, response.Code, response.Body.String(), held)
		}
		subject, _, err := served.approverStanding(httptest.NewRequest(http.MethodPost, "/api/v1/credential-requests/x/approve", nil), tokenActor)
		switch {
		case held && err == nil:
			t.Errorf("%s: an approve's re-read of account-a's token stood; want it refused", fleet)
		case held:
			wantAPIError(t, err, http.StatusUnauthorized, "TOKEN_REVOKED", fleet+": an approve's re-read of account-a's token")
		case err != nil || subject != "":
			t.Errorf("%s: an approve's re-read of account-a's token answered %q, %v; want it to stand naming no account, as a build without the binding relays it", fleet, subject, err)
		}
		minted := agentRequest(t, handler, http.MethodPost, "/api/v1/me/agent-tokens", map[string]any{"name": "minted " + fleet}, plaintext)
		switch {
		case held && minted.Code != http.StatusUnauthorized:
			t.Errorf("%s: account-a's token minted %d %s; want 401 TOKEN_REVOKED", fleet, minted.Code, minted.Body.String())
		case !held && minted.Code != http.StatusCreated:
			t.Errorf("%s: account-a's token minted %d %s; want 201", fleet, minted.Code, minted.Body.String())
		case !held:
			var recorded *string
			if err := database.Pool.QueryRow(ctx, `select subject from agent_tokens where owner = $1 and name = $2`, carol, "minted "+fleet).Scan(&recorded); err != nil {
				t.Fatal(err)
			}
			if recorded != nil {
				t.Errorf("%s: the token account-a's token minted records %q; want no account", fleet, *recorded)
			}
		}
		if _, err := served.brokerPersonAdmitted(ctx, brokerCaller); held != (err != nil) {
			t.Errorf("%s: a broker token recording account-a was admitted with error %v; want refused %t", fleet, err, held)
		}
		holder, err := served.accountHolder(ctx, database.Pool, accessQuestion{person: carol, issuer: google, subject: "account-a", describes: true})
		if err != nil || holder == nil || *holder == held {
			t.Errorf("%s: the membership answer's holder for account-a is %v, %v; want %t", fleet, holder, err, !held)
		}
	}
	check("beside a process without the binding", false)

	if _, err := database.Pool.Exec(ctx, `delete from server_processes`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Pool.Exec(ctx, `update account_binding_legacy_mark set seen_at = now() - make_interval(secs => $1)`,
		(store.OwnerLockMarkWindow + time.Minute).Seconds()); err != nil {
		t.Fatal(err)
	}
	check("once no build without the binding is in the fleet", true)
}

// A revocation is owed for account-a once account-b's sign-in replaces it. Then account-a takes
// the address back through a build without the account binding, whose sign-in writes `sub` and
// settles nothing (legion-envoy-v32.20.2's store.PgPeopleStore.SignInSubject, its update spelled
// here with the row's sealed refresh token kept, since the test holds no key of that build to seal
// one). A pass of this build then settles the revocation and sends nothing: account-a holds the
// address, and its broker sign-in on a machine stays live and unrefused.
//
// Control, run by hand: with the pass's SettleHeld step removed, the pass revokes account-a at the
// broker (it is refused and its sign-in ended).
func TestARevocationOfAnAccountThatTookItsAddressBackThroughABuildWithoutTheBindingIsSettledNotPaid(t *testing.T) {
	ctx := context.Background()
	r := newRecoveryRig(t)
	const email = "takeback-unbound@example.com"
	if err := r.people.SignInSubject(ctx, email, "account-a", "refresh-a", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	machine := r.signInMachine(t, email, "account-a", strings.Repeat("t", 43))
	if err := r.people.SignInSubject(ctx, email, "account-b", "refresh-b", model.MemberFull); err != nil {
		t.Fatal(err)
	}
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 1 || owing[0].Subject != "account-a" {
		t.Fatalf("after account-b's sign-in, %s owes %+v, %v; want account-a's revocation", email, owing, err)
	}
	if _, err := r.dispatch.Pool.Exec(ctx, `
		update people
		set signed_in_at = now(), confirmed_at = now(), sub = $2
		where email = $1`, email, "account-a"); err != nil {
		t.Fatalf("account-a's sign-in through a build without the binding: %v", err)
	}
	r.served.revokeOwedBrokerCredentials(ctx)
	if r.refused(t, email, "account-a") || !r.machineSignInLive(t, machine) {
		t.Errorf("after the pass, the broker refuses account-a at %s %t and its sign-in on a machine is live %t; want account-a, which holds the address, neither refused nor ended",
			email, r.refused(t, email, "account-a"), r.machineSignInLive(t, machine))
	}
	if owing, err := r.revocations.Owing(ctx, email); err != nil || len(owing) != 0 {
		t.Errorf("after the pass, %s owes %+v, %v; want account-a's revocation settled", email, owing, err)
	}
}
