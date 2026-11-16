package natscallout

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/broker/machine"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/broker/proof"
	"github.com/sjawhar/envoy/internal/brokerclient"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// session is one session's key, the access token the broker bound to it and the broker's answer
// about that token, as the auth service's Introspect would read it.
type session struct {
	id     string
	key    *ecdsa.PrivateKey
	token  string
	answer oauthwire.Introspection
}

func newSession(t testing.TB, id string, life time.Duration) *session {
	t.Helper()
	key, err := proof.NewKey()
	require.NoError(t, err)
	sum, err := (&jose.JSONWebKey{Key: key.Public()}).Thumbprint(crypto.SHA256)
	require.NoError(t, err)
	return &session{
		id: id, key: key, token: "access-token-of-" + id,
		answer: oauthwire.Introspection{
			Active: true, Session: id, Person: "person@example.com",
			ClientID: brokertoken.ExchangeClientID, ProjectScope: brokertoken.ProjectScopePerson,
			Thumbprint: base64.RawURLEncoding.EncodeToString(sum),
			ExpiresAt:  time.Now().Add(life).Unix(),
		},
	}
}

// request is the authorization request the server sends for this session connecting with nonce:
// its token in the CONNECT's token field and its proof over nonce in the signature field, which
// nats.go base64url-encodes.
func (s *session) request(t testing.TB, nonce string) Request {
	t.Helper()
	signed, err := brokerclient.ConnectionProof(s.key, s.token, nonce)
	require.NoError(t, err)
	return Request{
		UserNkey: "UUSER", ServerID: "NSERVER", Nonce: nonce, Token: s.token,
		Sig: base64.RawURLEncoding.EncodeToString([]byte(signed)), Host: "127.0.0.1",
	}
}

// introspector answers each session's token with its answer, and every other token not live.
func introspector(sessions ...*session) func(context.Context, string) (oauthwire.Introspection, error) {
	return func(_ context.Context, token string) (oauthwire.Introspection, error) {
		for _, s := range sessions {
			if s.token == token {
				return s.answer, nil
			}
		}
		return oauthwire.Introspection{}, nil
	}
}

// platformToken is a service's platform identity token as the stub ServiceToken reads it: the
// account it names and its life.
type platformToken struct {
	account        string
	google         bool
	issued, expiry time.Time
}

// serviceTokens verifies each raw token tokens names, and refuses every other.
func serviceTokens(tokens map[string]platformToken) func(context.Context, string) (string, bool, time.Time, time.Time, error) {
	return func(_ context.Context, raw string) (string, bool, time.Time, time.Time, error) {
		token, ok := tokens[raw]
		if !ok {
			return "", false, time.Time{}, time.Time{}, errors.New("the token did not verify")
		}
		return token.account, token.google, token.issued, token.expiry, nil
	}
}

func serviceRequest(raw string) Request {
	return Request{UserNkey: "UUSER", ServerID: "NSERVER", Nonce: "nonce", User: natscred.ServiceUser, Password: raw, Host: "10.0.0.7"}
}

var exampleServices = []ServiceEntry{
	{Kind: KindDispatch, MachineID: "example-dispatch", Account: "system:serviceaccount:dispatch:dispatch", AllProjects: true},
	{Kind: KindListener, MachineID: "example-hub", Account: listenerAccount, AllProjects: true},
	{Kind: KindDaemon, Account: daemonAccount, AllProjects: true},
	{Kind: KindMeasure, Account: measureAccount, Google: true, AllProjects: true},
	{Kind: KindDaemon, Account: "system:serviceaccount:example-ns:held-daemon"},
}

// A session's live token and a proof over this connection's nonce are answered with a user of
// that session's own: its delivery channel and agent stream's control subject to subscribe, its
// reply inboxes and its frames to publish, named session/<id>, expiring with the token.
func TestASessionTokenGetsItsOwnChannel(t *testing.T) {
	a := newSession(t, "ses_a", 15*time.Minute)
	decision, err := Decide(t.Context(), Deps{Introspect: introspector(a)}, nil, a.request(t, "nonce-of-this-connection"))
	require.NoError(t, err)
	want, err := natsgrant.Session("ses_a")
	require.NoError(t, err)
	require.Equal(t, want, decision.Grant)
	require.Equal(t, "session/ses_a", decision.Name)
	require.Equal(t, time.Unix(a.answer.ExpiresAt, 0), decision.Expires)
	require.Contains(t, decision.Grant.Subscribe, "notifications.envoy.deliver.ses_a")
	require.NotContains(t, decision.Grant.Subscribe, "notifications.envoy.deliver.*")
}

// A service's platform token, from an account the deployment lists, is answered with its kind's
// grant from natsgrant, Dispatch's and a listener's with the private-projects bucket beside it
// (scopeGrant), named <kind>/<machine id or account>, expiring with the token.
func TestAServiceTokenGetsItsKindsGrant(t *testing.T) {
	now := time.Now()
	tokens := map[string]platformToken{
		"dispatch-token": {account: "system:serviceaccount:dispatch:dispatch", issued: now, expiry: now.Add(time.Hour)},
		"listener-token": {account: listenerAccount, issued: now, expiry: now.Add(time.Hour)},
		"daemon-token":   {account: daemonAccount, issued: now, expiry: now.Add(30 * time.Minute)},
		"measure-token":  {account: measureAccount, google: true, issued: now, expiry: now.Add(time.Hour)},
	}
	deps := Deps{ServiceToken: serviceTokens(tokens)}
	for raw, want := range map[string]Decision{
		"dispatch-token": {Name: "dispatch/example-dispatch", Grant: dispatchCalloutGrant("example-dispatch"), Expires: now.Add(time.Hour)},
		"listener-token": {Name: "listener/example-hub", Grant: listenerCalloutGrant("example-hub"), Expires: now.Add(time.Hour)},
		"daemon-token":   {Name: "daemon/" + daemonAccount, Grant: natsgrant.Daemon(), Expires: now.Add(30 * time.Minute)},
		"measure-token":  {Name: "measure/" + measureAccount, Grant: natsgrant.Measure(), Expires: now.Add(time.Hour)},
	} {
		t.Run(raw, func(t *testing.T) {
			decision, err := Decide(t.Context(), deps, exampleServices, serviceRequest(raw))
			require.NoError(t, err)
			require.Equal(t, want, decision)
		})
	}
}

// Every refusal is one line naming why, and carries nothing of the credential.
func TestRefusals(t *testing.T) {
	now := time.Now()
	a := newSession(t, "ses_a", 15*time.Minute)
	expired := newSession(t, "ses_expired", 15*time.Minute)
	expired.answer.Active = false
	dotted := newSession(t, "a.b", 15*time.Minute)
	other := newSession(t, "ses_other", 15*time.Minute)
	otherKey := *a
	otherKey.key = other.key
	tokens := map[string]platformToken{
		"unlisted-token":  {account: "system:serviceaccount:example-ns:stranger", issued: now, expiry: now.Add(time.Hour)},
		"two-hours-token": {account: daemonAccount, issued: now, expiry: now.Add(2 * time.Hour)},
		"held-token":      {account: "system:serviceaccount:example-ns:held-daemon", issued: now, expiry: now.Add(time.Hour)},
	}
	deps := Deps{Introspect: introspector(a, expired, dotted), ServiceToken: serviceTokens(tokens)}
	unreachable := Deps{Introspect: func(context.Context, string) (oauthwire.Introspection, error) {
		return oauthwire.Introspection{}, errors.New("dial tcp: connection refused")
	}}
	badSig := a.request(t, "nonce")
	badSig.Sig = "%%%"
	for _, row := range []struct {
		name   string
		deps   Deps
		req    Request
		kind   string
		reason string
	}{
		{"no credential", deps, Request{UserNkey: "UUSER", ServerID: "NSERVER"}, credentialNone, "no credential this hub admits"},
		{"a user and no password", deps, Request{UserNkey: "UUSER", User: natscred.ServiceUser}, credentialNone, "no credential this hub admits"},
		{"a token and no proof", deps, Request{UserNkey: "UUSER", Token: a.token}, credentialNone, "no credential this hub admits"},
		{"a token that is not live", deps, expired.request(t, "nonce"), credentialSession, "token not live"},
		{"a proof of another key", deps, otherKey.request(t, "nonce"), credentialSession, "key mismatch"},
		{"a proof that is no base64url", deps, badSig, credentialSession, "proof invalid: jws"},
		{"a session id that is not one subject token", deps, dotted.request(t, "nonce"), credentialSession, "session id is not one subject token"},
		{"a broker that does not answer", unreachable, a.request(t, "nonce"), credentialSession, "broker unreachable"},
		{"a service token that does not verify", deps, serviceRequest("forged"), credentialService, "service token not verified"},
		{"an unlisted service", deps, serviceRequest("unlisted-token"), credentialService, "service not listed: system:serviceaccount:example-ns:stranger"},
		{"a two-hour platform token", deps, serviceRequest("two-hours-token"), credentialService, "token lives longer than an hour"},
		{"a service held to projects this broker cannot read", deps, serviceRequest("held-token"), credentialService, "held to exchange projects, which this broker cannot read: service daemon/system:serviceaccount:example-ns:held-daemon"},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := Decide(t.Context(), row.deps, exampleServices, row.req)
			var refused *Refusal
			require.True(t, errors.As(err, &refused), "%v", err)
			require.Equal(t, row.kind, refused.Kind)
			require.True(t, strings.HasPrefix(refused.Reason, row.reason), "%q does not begin %q", refused.Reason, row.reason)
			require.NotContains(t, refused.Reason, "\n")
			for _, secret := range []string{row.req.Token, row.req.Sig, row.req.Password} {
				if secret != "" {
					require.NotContains(t, refused.Reason, secret)
				}
			}
		})
	}
	require.Equal(t, machine.MaxTokenLoginLifetime, time.Hour, "the refusal's text names the bound")
}

// The user the service answers with expires when the credential it was issued for does, read
// from the encoded answer the server decodes: a session's token's expiry and a service's
// platform token's.
func TestTheUserExpiresWithItsCredential(t *testing.T) {
	issuer, err := nkeys.CreateAccount()
	require.NoError(t, err)
	user, err := nkeys.CreateUser()
	require.NoError(t, err)
	userKey, err := user.PublicKey()
	require.NoError(t, err)
	server, err := nkeys.CreateServer()
	require.NoError(t, err)
	serverID, err := server.PublicKey()
	require.NoError(t, err)

	now := time.Now()
	a := newSession(t, "ses_a", 7*time.Minute)
	tokens := map[string]platformToken{"daemon-token": {account: daemonAccount, issued: now, expiry: now.Add(40 * time.Minute)}}
	deps := Deps{Introspect: introspector(a), ServiceToken: serviceTokens(tokens)}
	for name, row := range map[string]struct {
		req    Request
		expiry int64
	}{
		"session": {a.request(t, "nonce"), a.answer.ExpiresAt},
		"service": {serviceRequest("daemon-token"), now.Add(40 * time.Minute).Unix()},
	} {
		t.Run(name, func(t *testing.T) {
			row.req.UserNkey, row.req.ServerID = userKey, serverID
			decision, err := Decide(t.Context(), deps, exampleServices, row.req)
			require.NoError(t, err)
			encoded, err := encodeAnswer(issuer, row.req, decision, nil)
			require.NoError(t, err)
			response, err := jwt.DecodeAuthorizationResponseClaims(encoded)
			require.NoError(t, err)
			require.Equal(t, serverID, response.Audience)
			admitted, err := jwt.DecodeUserClaims(response.Jwt)
			require.NoError(t, err)
			require.Equal(t, row.expiry, admitted.Expires, "the user expires at %s, its credential at %s",
				time.Unix(admitted.Expires, 0), time.Unix(row.expiry, 0))
			require.Equal(t, globalAccount, admitted.Audience)
			require.Equal(t, decision.Name, admitted.Name)
			require.Equal(t, userKey, admitted.Subject)
		})
	}
}

// A proof signs the nonce of the connection it was made for, so one captured from that
// connection's CONNECT and presented on another, which the server sends another nonce, is
// refused: the replay store the HTTP check keeps is not needed here.
func TestAProofCapturedFromOneConnectionIsRefusedOnAnother(t *testing.T) {
	a := newSession(t, "ses_a", 15*time.Minute)
	deps := Deps{Introspect: introspector(a)}
	first := a.request(t, "nonce-of-the-first-connection")
	_, err := Decide(t.Context(), deps, nil, first)
	require.NoError(t, err)

	replayed := first
	replayed.Nonce = "nonce-of-the-second-connection"
	_, err = Decide(t.Context(), deps, nil, replayed)
	var refused *Refusal
	require.True(t, errors.As(err, &refused), "the second connection, presenting the first's proof, was admitted: %v", err)
	require.Equal(t, "proof invalid: nonce", refused.Reason)
}

// Whatever branch decided it, an answer whose grant names a system subject is refused: the server
// takes from a callout client only the publish of $SYS.REQ.USER.AUTH, so a grant naming one would
// hand a connection a subscribe to the authorization requests. The control row decides the same
// request without the guard and is admitted, which is the row that fails if the guard goes.
func TestAGrantNamingASystemSubjectIsRefused(t *testing.T) {
	a := newSession(t, "ses_a", 15*time.Minute)
	deps := Deps{Introspect: introspector(a)}
	naming := grantSource{
		session: func(string) (natsgrant.Permissions, error) {
			return natsgrant.Permissions{Publish: []string{"_INBOX.>"}, Subscribe: []string{"$SYS.REQ.USER.AUTH"}}, nil
		},
		service: serviceGrant,
	}
	_, err := decide(t.Context(), deps, nil, a.request(t, "nonce"), naming)
	var refused *Refusal
	require.True(t, errors.As(err, &refused), "%v", err)
	require.Equal(t, "grant names a system subject: $SYS.REQ.USER.AUTH", refused.Reason)

	unguarded, err := decideCredential(t.Context(), deps, nil, a.request(t, "nonce"), naming)
	require.NoError(t, err, "the control: without the guard the same request is admitted")
	require.Contains(t, unguarded.Grant.Subscribe, "$SYS.REQ.USER.AUTH")
}
