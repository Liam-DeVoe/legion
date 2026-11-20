package oauth_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/broker/enroll"
	"github.com/sjawhar/envoy/internal/broker/oauth"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
)

// credentialOn inserts a live launcher credential bound to key, as a device login's approval
// mints one, and answers its id: the credential a machine login's token names, whose key is the
// key of that machine's machine.json.
func (f *fixture) credentialOn(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := f.pool.Exec(context.Background(), `insert into launcher_credentials (id, operator, host, key_thumbprint, public_jwk, expires_at)
		values ($1, $2, 'example-host', $3, $4, now() + interval '30 days')`, id, person, thumbprint(t, key), publicJWK(t, key)); err != nil {
		t.Fatalf("insert launcher_credentials: %v", err)
	}
	return id
}

// publicJWK is key's public half as a JWK, as a launcher credential records it.
func publicJWK(t *testing.T, key *ecdsa.PrivateKey) json.RawMessage {
	t.Helper()
	raw, err := jose.JSONWebKey{Key: &key.PublicKey}.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// tokenLogin is a Kubernetes service's token login proving key, under a subject of its own, as
// POST /v1/launcher-credentials/token records one (enroll.TokenLogin).
func tokenLogin(t *testing.T, key *ecdsa.PrivateKey) enroll.TokenLogin {
	t.Helper()
	name := "example-launcher-" + uuid.NewString()[:8]
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	return enroll.TokenLogin{
		Subject: "system:serviceaccount:example-ns:" + name, Service: "example-launcher", Host: "example-ns/" + name,
		TokenExpires: expires, Thumbprint: thumbprint(t, key), JWK: publicJWK(t, key), Expires: expires,
	}
}

// credentialsOn is how many launcher credentials, live or ended, are bound to key.
func (f *fixture) credentialsOn(t *testing.T, key *ecdsa.PrivateKey) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `select count(*) from launcher_credentials where key_thumbprint = $1`, thumbprint(t, key)).Scan(&n); err != nil {
		t.Fatalf("count the credentials of a key: %v", err)
	}
	return n
}

// readTokensOn is how many access tokens holding read, live or ended, are bound to key, in either
// table.
func (f *fixture) readTokensOn(t *testing.T, key *ecdsa.PrivateKey) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `select (select count(*) from oauth_narrowed_access_tokens where jwk_thumbprint = $1)
		+ (select count(*) from oauth_access_tokens where request #>> '{session,jwk_thumbprint}' = $1 and request #>> '{session,extra,project_scope}' = 'none')`,
		thumbprint(t, key)).Scan(&n); err != nil {
		t.Fatalf("count the read tokens of a key: %v", err)
	}
	return n
}

// endReadTokens ends every read token bound to key, in either table: revoked, as a revocation
// deactivates one, or expired. A read token of oauth_access_tokens is a read entry's, whose
// project scope is none, as readTokensOn counts it.
func (f *fixture) endReadTokens(t *testing.T, key *ecdsa.PrivateKey, revoked bool) {
	t.Helper()
	set := `active = false`
	if !revoked {
		set = `expires_at = now() - interval '1 second'`
	}
	for _, statement := range []string{
		`update oauth_narrowed_access_tokens set ` + set + ` where jwk_thumbprint = $1`,
		`update oauth_access_tokens set ` + set + ` where request #>> '{session,jwk_thumbprint}' = $1 and request #>> '{session,extra,project_scope}' = 'none'`,
	} {
		if _, err := f.pool.Exec(context.Background(), statement, thumbprint(t, key)); err != nil {
			t.Fatalf("end the read tokens of a key: %v", err)
		}
	}
}

// keyRuleRefusal is what the store's refusal of a read token on a key a live launcher credential
// is bound to says, in each of its two sentences (oauthstore's launcherKeyRefusal).
const keyRuleRefusal = "signs launcher proofs"

// launcherPath is one of the paths that write a launcher credential's key, tried with a key: it
// answers whether a credential was minted, and fails the test unless a refusal is the key rule's.
// The two enroll writers must refuse with enroll.ErrKeyBindsReadToken (keyTaken), and the two
// trades with the exchange's refusal of a key another live machine login holds (traded).
type launcherPath struct {
	name string
	mint func(t *testing.T, f *signInFixture, key *ecdsa.PrivateKey) bool
}

// keyTaken fails the test unless err is the refusal the two writers of a launcher credential's key
// give a key a live read token is bound to, ErrKeyBindsReadToken, which each caller answers as it
// answers a key another live credential holds.
func keyTaken(t *testing.T, err error) bool {
	t.Helper()
	switch {
	case err == nil:
		return true
	case errors.Is(err, enroll.ErrKeyBindsReadToken):
		return false
	}
	t.Fatalf("the credential was refused with %v, want enroll.ErrKeyBindsReadToken", err)
	return false
}

// traded answers whether a trade that asked for the key's launcher credential minted one, and
// fails the test unless a refusal is the exchange's for a key another live credential holds.
func traded(t *testing.T, status int, body map[string]any) bool {
	t.Helper()
	if status == http.StatusOK {
		if id, _ := body[oauthwire.ExtraCredentialID].(string); id == "" {
			t.Fatalf("the trade answered %v, which names no credential", body)
		}
		return true
	}
	refusedSaying(t, status, body, http.StatusBadRequest, "invalid_grant", "Another live machine login holds the key this proof was signed with.")
	return false
}

// launcherPaths is every path that writes a launcher credential's key: a device login's approval
// (the credential its approval mints, enroll.Service.MintLauncherCredentialTx in the approval's
// transaction, machine.Service.ApplyDecision), a token login (POST
// /v1/launcher-credentials/token, enroll.Service.IssueTokenLogin), a Google machine's trade that
// asks for its credential, and a person's trade, both through the exchange's credential step.
var launcherPaths = []launcherPath{
	{"a device login approved for the key", func(t *testing.T, f *signInFixture, key *ecdsa.PrivateKey) bool {
		ctx := context.Background()
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		_, err = f.enrollments().MintLauncherCredentialTx(ctx, tx, person, "example-host", thumbprint(t, key), publicJWK(t, key), "", time.Now().Add(time.Hour))
		if err == nil {
			err = tx.Commit(ctx)
		}
		return keyTaken(t, err)
	}},
	{"a token login with the key", func(t *testing.T, f *signInFixture, key *ecdsa.PrivateKey) bool {
		_, err := f.enrollments().IssueTokenLogin(context.Background(), tokenLogin(t, key))
		return keyTaken(t, err)
	}},
	{"a Google machine's trade asking for its credential with the key", func(t *testing.T, f *signInFixture, key *ecdsa.PrivateKey) bool {
		status, body := f.exchange(t, machineForm(t, f, "box-session-key"), key)
		return traded(t, status, body)
	}},
	{"a person's trade proving the key", func(t *testing.T, f *signInFixture, key *ecdsa.PrivateKey) bool {
		status, body := f.exchange(t, f.personTrade(t, "ada-box-key"), key)
		return traded(t, status, body)
	}},
}

// TestNoKeyIsBothAReadTokensAndALauncherCredentials holds the two-sided key rule: no key is both a
// live read token's and a live launcher credential's, a read token being any token granted read,
// a narrowed one or the read entry's own.
//
// Every read trade whose proof key can hold a launcher credential, tried binding that key with no
// holder proof, is refused and stores no token: a person's sign-in with its box key, a machine
// login with machine.json's key, a Google machine with the key its earlier trade put a credential
// on, a pod whose key holds a token login's credential, and the read entry's pod with such a key,
// asking for read and asking for no scope. The person's sign-in is refused for sending no holder
// proof, which its read trade sends whatever its box key holds; every other trade is refused by the
// key rule. The same trade with a holder proof, or with a key that holds no credential, mints.
//
// With a live read token bound to a key, of either kind, every path that writes a launcher
// credential's key is refused, and no credential is bound to the key: a device login's approval
// and a token login by enroll.ErrKeyBindsReadToken, and the two trades by the exchange's refusal
// of a key another live machine login holds. Once the read token is revoked, and with another key
// once its read token has expired, each mints.
//
// Two transactions run at once, one storing a read token bound to a key and one minting a
// credential on it, end with exactly one of them committed, fifty times over.
func TestNoKeyIsBothAReadTokensAndALauncherCredentials(t *testing.T) {
	f := newNarrowingFixture(t, func(*oauth.Config) {})
	ctx := context.Background()

	withTokenLogin := func(t *testing.T) *ecdsa.PrivateKey {
		key := newKey(t)
		if _, err := f.enrollments().IssueTokenLogin(ctx, tokenLogin(t, key)); err != nil {
			t.Fatalf("IssueTokenLogin: %v", err)
		}
		return key
	}
	for _, c := range []struct {
		name string
		// trade is the read trade, proved with a key a live launcher credential is bound to.
		trade func(t *testing.T) narrowingTrade
		scope string
		// says is what the refusal's description holds: the key rule's sentence, but for a
		// person's sign-in, whose read trade with no holder proof is refused before the key rule
		// is read (it must send one whatever its box key holds).
		says string
	}{
		{"a person's sign-in, with its box key", func(t *testing.T) narrowingTrade {
			return narrowingRows["person-sign-in"].trade(t, f)
		}, oauthwire.ScopeRead, "A person's read trade sends a DPoP-Holder proof"},
		{"a machine login, with machine.json's key", func(t *testing.T) narrowingTrade {
			machine := newKey(t)
			subject := f.devBoxTokenOf(t, machine, person, f.credentialOn(t, machine))
			return narrowingTrade{form: exchangeForm(subject, oauthwire.SubjectTokenTypeAccessToken, narrowedDevBoxSession), key: machine}
		}, oauthwire.ScopeRead, keyRuleRefusal},
		{"a Google machine, with the key its earlier trade put a credential on", func(t *testing.T) narrowingTrade {
			tr := narrowingRows["google-identity-no-person"].trade(t, f)
			status, body := f.send(t, tr, "", "")
			if !traded(t, status, body) {
				t.Fatal("the machine's earlier trade minted no credential")
			}
			tr.form = exchangeForm(f.machineToken(t, signInMachine, machineAudience, true), oauthwire.SubjectTokenTypeGoogle, narrowedFleetSession)
			return tr
		}, oauthwire.ScopeRead, keyRuleRefusal},
		{"a pod whose key holds a token login's credential", func(t *testing.T) narrowingTrade {
			tr := narrowingRows["pod-entry"].trade(t, f)
			tr.key = withTokenLogin(t)
			return tr
		}, oauthwire.ScopeRead, keyRuleRefusal},
		{"the read entry's pod asking for read, with such a key", func(t *testing.T) narrowingTrade {
			return narrowingTrade{form: f.readPodForm(t), key: withTokenLogin(t), session: readSession}
		}, oauthwire.ScopeRead, keyRuleRefusal},
		{"the read entry's pod asking for no scope, with such a key", func(t *testing.T) narrowingTrade {
			return narrowingTrade{form: f.readPodForm(t), key: withTokenLogin(t), session: readSession}
		}, "", keyRuleRefusal},
	} {
		t.Run("a read trade of "+c.name+" binding that key is refused", func(t *testing.T) {
			tr := c.trade(t)
			tr.holder = nil
			before := f.tokenRows(t)
			status, body := f.send(t, tr, c.scope, "")
			refusedSaying(t, status, body, http.StatusBadRequest, "invalid_request", c.says)
			if after := f.tokenRows(t); after != before {
				t.Errorf("the refused trade left %d access tokens, want the %d there were", after, before)
			}
			if bound := f.readTokensOn(t, tr.key); bound != 0 {
				t.Errorf("%d read tokens are bound to the key a launcher credential holds", bound)
			}
			// With a holder key, or for the read entry's trade asking for no scope, which may
			// send no holder proof, with a key that holds no credential, the same trade mints.
			again := c.trade(t)
			if c.scope == "" {
				again.key = newKey(t)
			} else {
				again.holder = newKey(t)
			}
			status, body = f.send(t, again, c.scope, "")
			if answer := f.introspected(t, accessTokenOf(t, status, body)); answer.Thumbprint != thumbprint(t, again.readKey()) {
				t.Errorf("the trade with a key of its own introspects bound to %q, want %q", answer.Thumbprint, thumbprint(t, again.readKey()))
			}
		})
	}

	readTokens := []struct {
		name string
		bind func(t *testing.T, key *ecdsa.PrivateKey)
	}{
		{"a narrowed token", func(t *testing.T, key *ecdsa.PrivateKey) {
			tr := narrowingRows["pod-entry"].trade(t, f)
			tr.key = key
			status, body := f.send(t, tr, oauthwire.ScopeRead, "")
			accessTokenOf(t, status, body)
		}},
		{"the read entry's token", func(t *testing.T, key *ecdsa.PrivateKey) {
			status, body := f.send(t, narrowingTrade{form: f.readPodForm(t), key: key}, "", "")
			accessTokenOf(t, status, body)
		}},
	}
	for _, token := range readTokens {
		for _, path := range launcherPaths {
			t.Run(path.name+", with "+token.name+" live on the key, is refused", func(t *testing.T) {
				key := newKey(t)
				token.bind(t, key)
				if path.mint(t, f, key) {
					t.Fatalf("%s minted a launcher credential on a key %s is bound to", path.name, token.name)
				}
				if bound := f.credentialsOn(t, key); bound != 0 {
					t.Fatalf("launcher_credentials holds %d rows on the key %s is bound to, want none", bound, token.name)
				}
				f.endReadTokens(t, key, true)
				if !path.mint(t, f, key) {
					t.Fatalf("%s minted nothing once %s on the key was revoked", path.name, token.name)
				}
				expired := newKey(t)
				token.bind(t, expired)
				f.endReadTokens(t, expired, false)
				if !path.mint(t, f, expired) {
					t.Fatalf("%s minted nothing on a key whose %s has expired", path.name, token.name)
				}
			})
		}
	}

	t.Run("a read token and a credential stored at once on one key end with one of them", func(t *testing.T) {
		for i := range 50 {
			key := newKey(t)
			form := f.readPodForm(t)
			form.Set("scope", oauthwire.ScopeRead)
			headers := http.Header{"DPoP": {dpopProof(t, key, f.url+"/token")}}
			login := tokenLogin(t, key)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var answer tokenAnswer
			var minted error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				answer = postTokenWith(f.url+"/token", form, headers)
			}()
			go func() {
				defer wg.Done()
				<-start
				_, minted = f.enrollments().IssueTokenLogin(ctx, login)
			}()
			close(start)
			wg.Wait()
			if answer.err != nil {
				t.Fatalf("round %d: the read trade: %v", i, answer.err)
			}
			if minted != nil && !errors.Is(minted, enroll.ErrKeyBindsReadToken) {
				t.Fatalf("round %d: the token login failed with %v, which is not the key rule's refusal", i, minted)
			}
			tokenStored, credentialStored := answer.status == http.StatusOK, minted == nil
			if tokenStored == credentialStored {
				t.Fatalf("round %d: the read trade answered %d %v and the token login %v: want exactly one of them stored", i, answer.status, answer.body, minted)
			}
			if description, _ := answer.body["error_description"].(string); !tokenStored &&
				(answer.status != http.StatusBadRequest || answer.body["error"] != "invalid_request" || !strings.Contains(description, keyRuleRefusal)) {
				t.Fatalf("round %d: the read trade answered %d %v, want 200 or the key rule's 400 invalid_request", i, answer.status, answer.body)
			}
			if got := f.readTokensOn(t, key) + f.credentialsOn(t, key); got != 1 {
				t.Fatalf("round %d: the key holds %d read tokens and credentials together, want 1", i, got)
			}
		}
		t.Log("50 rounds each stored exactly one of the read token and the credential")
	})
}
