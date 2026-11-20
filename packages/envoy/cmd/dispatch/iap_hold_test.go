package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/identity"
	"github.com/sjawhar/envoy/internal/dispatch/identity/directorytest"
	"github.com/sjawhar/envoy/internal/dispatch/routes"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// The IAP hold through the real router (R9, c-iap-directory): Dispatch under DISPATCH_IDENTITY=iap
// as main builds it (requestIdentityFor, standingFor, appContextOptions), on Postgres, naming its
// person from assertions signed by a key set served here in IAP's place, and checking people's
// Workspace accounts through the Directory reader against Google hosts served here (directorytest).

// iapPerson is the person iapAssertion's assertions name, lowercased as Dispatch records them.
const iapPerson = "a.person@example.com"

// iapHoldRig is one IAP Dispatch: its router, its database and people store, the Membership the
// router's Standing and the membership pass share, and the fake Google its Directory reader reaches.
type iapHoldRig struct {
	t          *testing.T
	keys       *oidctest.Issuer
	signing    *oidctest.Key
	google     *directorytest.Google
	database   *store.Store
	people     testPeople
	membership *identity.Membership
	handler    http.Handler
}

func newIAPHoldRig(t *testing.T) *iapHoldRig {
	t.Helper()
	keys, signing := iapKeySet(t)
	r := &iapHoldRig{t: t, keys: keys, signing: signing, google: directorytest.ServeGoogle(t), database: storetest.Open(t)}
	boot := iapBoot(t, keys, nil)
	boot.DirectoryOptions = r.google.ClientOptions()
	r.people = testPeople{PgPeopleStore: store.NewPgPeopleStore(r.database.Pool, testSigningKey, nil), pool: r.database.Pool}
	built := routes.AppContextOptions{
		Lifetime: t.Context(), SigningKey: testSigningKey, People: r.people, Store: r.database,
		Sessions: store.NewPgSessionStore(r.database.Pool),
	}
	rule, err := signInRule(boot, accessLister(built.Store))
	if err != nil {
		t.Fatal(err)
	}
	built.Identity, r.membership, err = requestIdentityFor(t.Context(), boot, built.SigningKey, built.People, built.Sessions, built.SignIn, rule)
	if err != nil {
		t.Fatalf("build the IAP identity: %v", err)
	}
	built.Standing = standingFor(rule, r.membership)
	app, err := routes.BuildAppContext(appContextOptions(boot, rule, built))
	if err != nil {
		t.Fatalf("build the router: %v", err)
	}
	handler, background := routes.New(app)
	t.Cleanup(background.Wait)
	r.handler = handler
	return r
}

// browser is a request from iapPerson's browser through IAP: a fresh assertion IAP signed, and,
// for a write, the dashboard's own origin.
func (r *iapHoldRig) browser(method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	request := httptest.NewRequest(method, "http://dispatch.test"+path, strings.NewReader(body))
	request.Header.Set("X-Goog-IAP-JWT-Assertion", iapAssertion(r.t, r.keys, r.signing, testIAPAudience, "example.com"))
	if method != http.MethodGet {
		request.Header.Set("Origin", "http://dispatch.test")
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, request)
	return response
}

// mintToken mints a dsp_ token on iapPerson's browser, as the agent tokens page does under IAP.
func (r *iapHoldRig) mintToken() string {
	r.t.Helper()
	minted := r.browser(http.MethodPost, "/api/v1/me/agent-tokens", `{"name":"an agent of mine"}`)
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &token); minted.Code != http.StatusCreated || err != nil || token.Token == "" {
		r.t.Fatalf("POST /api/v1/me/agent-tokens through IAP answered %d %s, want 201 with a token", minted.Code, minted.Body.String())
	}
	return token.Token
}

// use is GET /api/v1/whoami with token, as an agent sends it.
func (r *iapHoldRig) use(token string) *httptest.ResponseRecorder {
	return bearerRequest(r.handler, http.MethodGet, "/api/v1/whoami", "", token)
}

// directory makes the fake Directory answer iapPerson's own account, suspended as suspended says.
func (r *iapHoldRig) directory(suspended bool) {
	r.google.SetUser(iapPerson, directorytest.User{ID: "u-person", PrimaryEmail: iapPerson, Suspended: suspended})
}

// pass makes iapPerson's row due a Directory check, dating its Directory columns two hours back,
// and runs one membership pass over the router's Membership, as the maintenance sweep runs it.
func (r *iapHoldRig) pass() {
	r.t.Helper()
	if r.membership == nil {
		r.t.Fatal("requestIdentityFor built no Membership under IAP identity, so no membership pass runs")
	}
	r.exec(`update people set directory_confirmed_at = directory_confirmed_at - interval '2 hours',
		directory_checked_at = directory_checked_at - interval '2 hours', not_held_until = not_held_until - interval '2 hours',
		held_since = held_since - interval '2 hours' where email = $1`, iapPerson)
	if err := (identity.MembershipPass{Membership: r.membership, People: r.people.PgPeopleStore}).Run(context.Background()); err != nil {
		r.t.Fatalf("the membership pass: %v", err)
	}
}

func (r *iapHoldRig) exec(statement string, args ...any) {
	r.t.Helper()
	if _, err := r.database.Pool.Exec(context.Background(), statement, args...); err != nil {
		r.t.Fatalf("%s: %v", statement, err)
	}
}

// heldSince is iapPerson's held_since, nil for none.
func (r *iapHoldRig) heldSince() *time.Time {
	r.t.Helper()
	var at *time.Time
	if err := r.database.Pool.QueryRow(context.Background(), `select held_since from people where email = $1`, iapPerson).Scan(&at); err != nil {
		r.t.Fatal(err)
	}
	return at
}

// wantAnswer fails the test unless response is status with code (none for a 200), saying text
// when it is not empty.
func wantAnswer(t *testing.T, what string, response *httptest.ResponseRecorder, status int, code, text string) {
	t.Helper()
	var refusal struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &refusal)
	if response.Code != status || (status != http.StatusOK && refusal.Code != code) || (text != "" && refusal.Error != text) {
		t.Fatalf("%s answered %d %s, want %d %s %q", what, response.Code, strings.TrimSpace(response.Body.String()), status, code, text)
	}
}

// The IAP refusals of a personal token (api.personalTokenHeld): a Directory hold in the
// Directory's words, and an owner no Active answer confirmed in 24 hours unavailable.
const (
	iapHeldText        = "Google's Workspace Directory no longer confirms this token's owner; the token works again once it does"
	iapUnavailableText = "whether this token's owner may still act could not be read; try again in a minute"
)

// TestIAPReleaseNeedsNoAssertion (c-iap-directory): under IAP a held person's dsp_ token is served
// again after the membership pass's Active outcome, with no request from the person. The person
// mints a token through IAP, the Directory answers their account active, then suspended, then
// active again; between the last two passes the person sends nothing.
//
// Control: a pass that skips held rows; the hold stays, and the token is still refused.
func TestIAPReleaseNeedsNoAssertion(t *testing.T) {
	r := newIAPHoldRig(t)
	r.directory(false)
	token := r.mintToken()
	wantAnswer(t, "the person's token, their account active", r.use(token), http.StatusOK, "", "")
	// The reader the boot built signs through DISPATCH_DIRECTORY_ACCOUNT for DISPATCH_DIRECTORY_ADMIN.
	if signs := r.google.Signs(); len(signs) == 0 || signs[0].Principal != directoryCheckSigner || signs[0].Subject != directoryCheckAdmin {
		t.Fatalf("the token's Directory check asked signJwt %+v; want it on %s for %s, the boot's two settings", signs, directoryCheckSigner, directoryCheckAdmin)
	}

	r.directory(true)
	r.pass()
	wantAnswer(t, "the person's token once the pass finds their account suspended", r.use(token), http.StatusUnauthorized, "MEMBERSHIP_HELD", iapHeldText)

	r.directory(false)
	r.pass()
	wantAnswer(t, "the person's token once the pass finds their account active again, with no request from them", r.use(token), http.StatusOK, "", "")
	if held := r.heldSince(); held != nil {
		t.Fatalf("the person's row is held since %s after the Active outcome; want released", held)
	}
}

// TestIAPAssertionAloneReleasesNothing (c-iap-directory): under IAP a valid assertion of a held
// person, on a read and on a write, releases nothing: IAP's sign-in records the person and nothing
// more, so the hold Directory recorded stands and the person's dsp_ token stays refused.
//
// Control: let an IAP assertion release a hold (an IAP sign-in that clears held_since); the token
// is served after the assertions, and the test fails.
func TestIAPAssertionAloneReleasesNothing(t *testing.T) {
	r := newIAPHoldRig(t)
	r.directory(false)
	token := r.mintToken()
	r.directory(true)
	r.pass()
	held := r.heldSince()
	if held == nil {
		t.Fatal("the pass did not hold the person whose account is suspended")
	}
	wantAnswer(t, "the held person's token", r.use(token), http.StatusUnauthorized, "MEMBERSHIP_HELD", iapHeldText)

	if whoami := r.browser(http.MethodGet, "/auth/whoami", ""); whoami.Code != http.StatusOK {
		t.Fatalf("GET /auth/whoami with the held person's valid assertion answered %d %s, want 200: IAP admits them", whoami.Code, whoami.Body.String())
	}
	r.browser(http.MethodPost, "/api/v1/me/agent-tokens", `{"name":"another agent"}`)
	if after := r.heldSince(); after == nil || !after.Equal(*held) {
		t.Fatalf("the held person's row after two valid assertions is held since %v; want %s, as before", after, held)
	}
	wantAnswer(t, "the held person's token after their valid assertions", r.use(token), http.StatusUnauthorized, "MEMBERSHIP_HELD", iapHeldText)
}

// TestIAPPersonalTokenFollowsTheDirectory (c-iap-directory, K11): under IAP a personal token's owner
// has no refresh token, so the token asks Standing at every use (api.personalTokenHeld), and its
// Directory deadline decides it while the Directory fails: served just before not_held_until, 503
// IDENTITY_UNAVAILABLE at it and after it, never 401 MEMBERSHIP_HELD. A hold the Directory recorded
// is 401 MEMBERSHIP_HELD in the Directory's words, also after a valid IAP assertion.
//
// Control: put the fast path for an owner with no stored refresh token back ahead of the read of
// Standing; the token is served at and after its deadline, and the test fails.
func TestIAPPersonalTokenFollowsTheDirectory(t *testing.T) {
	r := newIAPHoldRig(t)
	r.directory(false)
	token := r.mintToken()
	wantAnswer(t, "the token, its owner's account active", r.use(token), http.StatusOK, "", "")
	var stored bool
	if err := r.database.Pool.QueryRow(context.Background(), `select refresh_token is not null from people where email = $1`, iapPerson).Scan(&stored); err != nil || stored {
		t.Fatalf("the IAP owner's row stores a refresh token %t (%v); want none", stored, err)
	}

	// The Directory fails from here on, and the owner's last Active answer is two hours old.
	r.google.AnswerUser(iapPerson, directorytest.Answer{Status: http.StatusInternalServerError, Body: `{"error":{"code":500}}`})
	deadline := func(at string) {
		r.exec(`update people set directory_confirmed_at = now() - interval '2 hours', not_held_until = `+at+` where email = $1`, iapPerson)
	}
	deadline(`clock_timestamp() + interval '30 seconds'`)
	wantAnswer(t, "the token just before its owner's deadline", r.use(token), http.StatusOK, "", "")
	deadline(`clock_timestamp()`)
	wantAnswer(t, "the token at its owner's deadline", r.use(token), http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", iapUnavailableText)
	deadline(`now() - interval '1 minute'`)
	wantAnswer(t, "the token after its owner's deadline", r.use(token), http.StatusServiceUnavailable, "IDENTITY_UNAVAILABLE", iapUnavailableText)

	r.directory(true)
	r.pass()
	if whoami := r.browser(http.MethodGet, "/auth/whoami", ""); whoami.Code != http.StatusOK {
		t.Fatalf("GET /auth/whoami with the held owner's valid assertion answered %d, want 200", whoami.Code)
	}
	wantAnswer(t, "the token of an owner the Directory holds, after their valid assertion", r.use(token), http.StatusUnauthorized, "MEMBERSHIP_HELD", iapHeldText)
}

// TestForgetDirectoryUserCommand: `envoy-dispatch forget-directory-user <address>` forgets the
// Directory user id the address's row stores, for the address in any case and spacing, prints that
// it did, and exits 0; it changes nothing else of the row. Run again, or for an address no row
// holds, it prints that nothing changed and exits 1. No address, a blank one, or two are usage
// errors (2), and a database it cannot open is 1.
func TestForgetDirectoryUserCommand(t *testing.T) {
	database := storetest.Open(t)
	people := store.NewPgPeopleStore(database.Pool, testSigningKey, nil)
	ctx := context.Background()
	if err := people.Record(ctx, iapPerson); err != nil {
		t.Fatal(err)
	}
	if _, err := people.RecordDirectoryActive(ctx, iapPerson, "u-person"); err != nil {
		t.Fatal(err)
	}
	if err := people.RecordDirectoryHeld(ctx, iapPerson); err != nil {
		t.Fatal(err)
	}
	type columns struct {
		confirmed, deadline, held *time.Time
		id                        *string
	}
	read := func() columns {
		t.Helper()
		var row columns
		if err := database.Pool.QueryRow(ctx, `select directory_confirmed_at, not_held_until, held_since, directory_user_id from people where email = $1`, iapPerson).
			Scan(&row.confirmed, &row.deadline, &row.held, &row.id); err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := read()
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		env := envGetter(map[string]string{"DATABASE_URL": database.Pool.Config().ConnString()})
		code := runSubcommand(ctx, append([]string{"forget-directory-user"}, args...), env, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, stdout, stderr := run("  A.Person@Example.com "); code != 0 || !strings.Contains(stdout, "forget-directory-user: "+iapPerson+" no longer stores a Directory user id") {
		t.Fatalf("forget-directory-user of the stored address: exit %d, stdout %q, stderr %q; want 0 saying it forgot the id", code, stdout, stderr)
	}
	after := read()
	if after.id != nil || !after.confirmed.Equal(*before.confirmed) || !after.deadline.Equal(*before.deadline) || after.held == nil || !after.held.Equal(*before.held) {
		t.Fatalf("after forget-directory-user the row holds id %v, confirmed %v, deadline %v, held %v; want no id and the rest as before (%v, %v, %v)",
			after.id, after.confirmed, after.deadline, after.held, before.confirmed, before.deadline, before.held)
	}
	for _, again := range []string{iapPerson, "nobody@example.com"} {
		if code, stdout, stderr := run(again); code != 1 || !strings.Contains(stderr, "stores no Directory user id; nothing changed") || stdout != "" {
			t.Errorf("forget-directory-user %s with no stored id: exit %d, stdout %q, stderr %q; want 1 saying nothing changed", again, code, stdout, stderr)
		}
	}
	for _, args := range [][]string{nil, {" "}, {iapPerson, "other@example.com"}} {
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, "forget-directory-user <address>") {
			t.Errorf("forget-directory-user %q: exit %d, stderr %q; want 2 with the usage", args, code, stderr)
		}
	}
	var stdout, stderr bytes.Buffer
	unreachable := envGetter(map[string]string{"DATABASE_URL": "postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"})
	if code := runSubcommand(ctx, []string{"forget-directory-user", iapPerson}, unreachable, &stdout, &stderr); code != 1 {
		t.Errorf("forget-directory-user with an unreachable database: exit %d, stderr %q; want 1", code, stderr.String())
	}
}
