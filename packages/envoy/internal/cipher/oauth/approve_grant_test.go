// approve_grant_test.go drives the token exchange's approve grant: a person's sign-in traded for the
// approve capability's airlock placeholder, never for a token that can approve. The fixture is
// person_signin_test.go's.
package oauth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/oauth"
	"github.com/sjawhar/envoy/internal/cipher/oauthstore"
	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
	"github.com/sjawhar/envoy/internal/cipher/placeholder"
)

// approveSettings is a broker serving the approve capability: its proxy, its authority, the one
// host its dpop block binds, and the digest every grant is made under.
func approveSettings() *oauth.ApproveSettings {
	return &oauth.ApproveSettings{
		Name: "DISPATCH_APPROVE_TOKEN", Word: "approve", EntryDigest: strings.Repeat("ab", 32),
		ProxyURL: "http://airlock.test:3128", CACertificate: "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n", Host: "dispatch.test",
	}
}

// placeholderForm is the sign-in trade asking for the airlock placeholder.
func (f *signInFixture) placeholderForm(t *testing.T, session string) map[string][]string {
	t.Helper()
	form := signInForm(f.personToken(t, person, deviceAudience, true, signInDomain), f.machineToken(t, signInMachine, machineAudience, true), session)
	form.Set("requested_token_type", oauthwire.TokenTypeAirlockPlaceholder)
	return form
}

// TestASignInIsTradedForAnApprovePlaceholderAndNeverAToken: a person's sign-in that asks for the
// airlock placeholder is answered the placeholder of a fresh approve grant - no access token, the
// placeholder type, N_A, at most 900 seconds, the grant's id and the airlock release naming the
// capability's one host - and the placeholder is no token introspection reads as live. The grant
// it wrote is live by that placeholder, holds the scope approve alone, the person, the session and
// the capability's entry digest, and the token the broker mints for it introspects as an
// approve-only token: the project scope person and the scope approve, bound to the pod's key. Once
// the sign-in's credential is revoked, the grant is no longer live.
func TestASignInIsTradedForAnApprovePlaceholderAndNeverAToken(t *testing.T) {
	f := newSignInFixtureWith(t, func(c *oauth.Config) { c.Approve = approveSettings() })
	key := newKey(t)
	before := f.count(t, "oauth_access_tokens")
	status, body := f.exchange(t, f.placeholderForm(t, "ada-box-1"), key)
	held, _ := body["access_token"].(string)
	if status != http.StatusOK || body["issued_token_type"] != oauthwire.TokenTypeAirlockPlaceholder || body["token_type"] != "N_A" ||
		!strings.HasPrefix(held, placeholder.MintedPrefix("approve")) {
		t.Fatalf("the placeholder trade = %d %v; want 200, the placeholder type, N_A and an approve placeholder", status, body)
	}
	if seconds, _ := body["expires_in"].(float64); seconds <= 0 || seconds > 900 {
		t.Errorf("expires_in = %v; want at most 900 seconds", body["expires_in"])
	}
	release, _ := body[oauthwire.ExtraAirlock].(map[string]any)
	names, _ := release["names"].(map[string]any)
	name, _ := names["DISPATCH_APPROVE_TOKEN"].(map[string]any)
	if release["proxy_password"] != held || name["value"] != held || release["proxy_url"] != "http://airlock.test:3128" {
		t.Errorf("the airlock release = %v; want the placeholder as the proxy password and DISPATCH_APPROVE_TOKEN's value", release)
	}
	if after := f.count(t, "oauth_access_tokens"); after != before {
		t.Fatalf("the trade stored %d access tokens; want none", after-before)
	}
	if answer, err := f.provider.Introspect(context.Background(), held); err != nil || answer.Active {
		t.Fatalf("the placeholder introspects as %+v, %v; want not live", answer, err)
	}

	grants := oauthstore.ApproveGrants{Store: oauthstore.New(f.pool)}
	grantID, _ := body[oauthwire.ExtraGrantID].(string)
	grant, err := grants.MintedByID(context.Background(), strings.TrimPrefix(grantID, "approve:"))
	if err != nil {
		t.Fatalf("the grant %s: %v", grantID, err)
	}
	if byHash, err := grants.MintedByPlaceholder(context.Background(), grant.PlaceholderSHA256); err != nil || byHash.ID != grant.ID {
		t.Fatalf("the grant by its placeholder's hash = %+v, %v", byHash, err)
	}
	if len(grant.Scopes) != 1 || grant.Scopes[0] != "approve" || grant.Session != "ada-box-1" || grant.Extra[oauth.ClaimPerson] != person ||
		grant.EntryDigest != approveSettings().EntryDigest {
		t.Fatalf("the grant = %+v; want the scope approve, session ada-box-1, %s and the entry digest", grant, person)
	}
	pod := newKey(t)
	minted, err := f.provider.MintGrantToken(context.Background(), grant, thumbprint(t, pod))
	if err != nil {
		t.Fatal(err)
	}
	answer, err := f.provider.Introspect(context.Background(), minted)
	if err != nil || !answer.Active || answer.Scope != "approve" || answer.ProjectScope != oauthwire.ProjectScopePerson ||
		answer.Person != person || answer.Session != "ada-box-1" || len(answer.Projects) != 0 || answer.Thumbprint != thumbprint(t, pod) {
		t.Fatalf("the minted token introspects as %+v, %v; want an approve-only token of %s bound to the pod's key", answer, err, person)
	}

	if _, err := f.pool.Exec(context.Background(), `update launcher_credentials set revoked_at = now() where id = (select credential_id from approve_grants where id = $1)`, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.MintedByID(context.Background(), grant.ID); !errors.Is(err, oauthstore.ErrMintedGrantNotLive) {
		t.Fatalf("the grant after its sign-in was revoked = %v; want ErrMintedGrantNotLive", err)
	}
}

// TestOnlyASignInIsTradedForTheApprovePlaceholder: the placeholder is refused invalid_request, with
// nothing written, to a broker that serves no approve capability and to a trade that is no
// person's sign-in - a dev box's machine token trade asking for it.
func TestOnlyASignInIsTradedForTheApprovePlaceholder(t *testing.T) {
	plain := newSignInFixture(t)
	status, body := plain.exchange(t, plain.placeholderForm(t, "ada-box-2"), newKey(t))
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("the placeholder trade on a broker serving no approve capability = %d %v; want 400 invalid_request", status, body)
	}
	f := newSignInFixtureWith(t, func(c *oauth.Config) { c.Approve = approveSettings() })
	key := newKey(t)
	form := exchangeForm(f.devBoxToken(t, key, person), oauthwire.SubjectTokenTypeAccessToken, "ada-box-3")
	form.Set("requested_token_type", oauthwire.TokenTypeAirlockPlaceholder)
	status, body = f.exchange(t, form, key)
	if status != http.StatusBadRequest || body["error"] != "invalid_request" {
		t.Fatalf("a machine token's placeholder trade = %d %v; want 400 invalid_request", status, body)
	}
	if n := f.count(t, "approve_grants"); n != 0 {
		t.Fatalf("the refused trades wrote %d approve grants; want none", n)
	}
}
