package natscallout

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// emptyDirections are the two grants that open a direction: one with no publish allow list and
// one with no subscribe allow list. Either would admit a user who may publish, or subscribe, to
// every subject (natsgrant.OpenDirection).
var emptyDirections = map[string]natsgrant.Permissions{
	"publish":   {Subscribe: []string{"_INBOX.>"}},
	"subscribe": {Publish: []string{"_INBOX.>"}},
}

// A grant with an empty allow list in either direction is refused at both guards, naming the
// direction: decide's, whatever produced the grant, and encodeAnswer's, which every answer
// passes, so a producer that bypassed decide could not reach the server with it either.
func TestAGrantWithAnEmptyDirectionIsRefused(t *testing.T) {
	issuer, err := nkeys.CreateAccount()
	require.NoError(t, err)
	user, err := nkeys.CreateUser()
	require.NoError(t, err)
	userKey, err := user.PublicKey()
	require.NoError(t, err)
	for direction, grant := range emptyDirections {
		want := "grant opens every subject: its " + direction + " allow list is empty"
		t.Run(direction+": decide", func(t *testing.T) {
			a := newSession(t, "ses_a", 15*time.Minute)
			_, err := decide(t.Context(), Deps{Introspect: introspector(a)}, nil, a.request(t, "nonce"), grantSource{
				session: func(string) (natsgrant.Permissions, error) { return grant, nil },
				service: serviceGrant,
			})
			var refused *Refusal
			require.ErrorAs(t, err, &refused)
			require.Equal(t, want, refused.Reason)
		})
		t.Run(direction+": encodeAnswer", func(t *testing.T) {
			encoded, err := encodeAnswer(issuer, Request{UserNkey: userKey, ServerID: "NSERVER"},
				Decision{Name: "session/ses_a", Grant: grant, Expires: time.Now().Add(time.Hour)}, nil)
			require.NoError(t, err)
			response, err := jwt.DecodeAuthorizationResponseClaims(encoded)
			require.NoError(t, err)
			require.Equal(t, want, response.Error)
			require.Empty(t, response.Jwt, "a user was encoded for a grant that opens its %s direction", direction)
		})
	}
	t.Run("every grant natsgrant builds closes both directions", func(t *testing.T) {
		session, err := natsgrant.Session("ses_a")
		require.NoError(t, err)
		for name, grant := range map[string]natsgrant.Permissions{
			"Session": session, "Daemon": natsgrant.Daemon(), "Measure": natsgrant.Measure(),
			"Listener": natsgrant.Listener("example-hub"), "Dispatch": natsgrant.Dispatch("example-dispatch"),
			"Host": natsgrant.Host("example-host-a"), "ListenerHost": natsgrant.ListenerHost("example-hub"),
		} {
			direction, open := natsgrant.OpenDirection(grant)
			require.False(t, open, "%s opens its %s direction", name, direction)
		}
	})
}

// On a running server, a user answered with an empty publish allow list may publish to every
// subject — which is why the guard exists — and the service refuses that grant. The control runs
// first: a stand-in service holding the service's keys answers with exactly that user, and a
// message the user publishes on a subject no grant of natsgrant names reaches a subscriber. Then
// the real service, deciding with decide's guard bypassed so encodeAnswer's is the one standing,
// refuses the same grant.
func TestAnEmptyAllowListIsRefused(t *testing.T) {
	requireAnEmptyPublishListRefused(t, func(keys testnats.CalloutKeys, static ...testnats.StaticUser) string {
		return testnats.StartWithCallout(t, keys, static...)
	})
}

func requireAnEmptyPublishListRefused(t *testing.T, start func(testnats.CalloutKeys, ...testnats.StaticUser) string) {
	t.Helper()
	keys := testnats.NewCalloutKeys(t)
	readerSeed, readerKey := testnats.User(t)
	url := start(keys, testnats.StaticUser{NKey: readerKey, Publish: []string{"_INBOX.>"}, Subscribe: []string{"secret.>"}})
	reader, err := testnats.ConnectSeed(t, url, readerSeed)
	require.NoError(t, err)
	defer reader.Close()
	secret, err := reader.SubscribeSync("secret.>")
	require.NoError(t, err)
	require.NoError(t, reader.Flush())

	t.Run("control: the server lets an empty publish list publish anywhere", func(t *testing.T) {
		standIn := openPublishService(t, url, keys)
		defer standIn.Close()
		nc, err := nats.Connect(url, nats.Token("anything"), nats.NoReconnect(), nats.Timeout(5*time.Second))
		require.NoError(t, err, "the stand-in's empty-publish user was not admitted")
		defer nc.Close()
		require.NoError(t, nc.Publish("secret.read", []byte("read")))
		require.NoError(t, nc.Flush())
		msg, err := secret.NextMsg(5 * time.Second)
		require.NoError(t, err, "the empty-publish user's message never arrived: the server restricted it, so the guard guards nothing")
		require.Equal(t, "read", string(msg.Data))
	})

	t.Run("the service refuses the same grant", func(t *testing.T) {
		a := newSession(t, "ses_a", 15*time.Minute)
		bypassingDecide := func(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
			return decideCredential(ctx, deps, services, req, grantSource{
				session: func(string) (natsgrant.Permissions, error) { return emptyDirections["publish"], nil },
				service: serviceGrant,
			})
		}
		service := startService(t, url, keys, Deps{Introspect: introspector(a)}, bypassingDecide)
		t.Cleanup(service.Close)
		_, err := connectSession(t, url, a, nil)
		require.ErrorIs(t, err, nats.ErrAuthorization, "the service admitted a user that may publish to every subject")
		if msg, err := secret.NextMsg(500 * time.Millisecond); err == nil {
			t.Fatalf("a message reached the subscriber after the refusal: %q", msg.Data)
		}
	})
}

// openPublishService answers every authorization request with a user whose publish allow list is
// empty and whose subscribe allow list is its reply inboxes, signed with the service's own keys,
// so the server reads it as the service's answer: the grant the guard refuses, delivered past it.
func openPublishService(t *testing.T, url string, keys testnats.CalloutKeys) *nats.Conn {
	t.Helper()
	issuer, err := nkeys.FromSeed([]byte(keys.IssuerSeed))
	require.NoError(t, err)
	xkey, err := nkeys.FromSeed([]byte(keys.XKeySeed))
	require.NoError(t, err)
	conn, err := testnats.ConnectSeed(t, url, keys.ServiceSeed)
	require.NoError(t, err)
	_, err = conn.QueueSubscribe(authSubject, queueGroup, func(msg *nats.Msg) {
		serverXKey := msg.Header.Get(serverXKeyHeader)
		data := msg.Data
		if serverXKey != "" {
			opened, err := xkey.Open(data, serverXKey)
			if err != nil {
				return
			}
			data = opened
		}
		claims, err := jwt.DecodeAuthorizationRequestClaims(string(data))
		if err != nil {
			return
		}
		user := jwt.NewUserClaims(claims.UserNkey)
		user.Audience = globalAccount
		user.Sub.Allow.Add("_INBOX.>")
		userJWT, err := user.Encode(issuer)
		if err != nil {
			return
		}
		response := jwt.NewAuthorizationResponseClaims(claims.UserNkey)
		response.Audience = claims.Server.ID
		response.Jwt = userJWT
		encoded, err := response.Encode(issuer)
		if err != nil {
			return
		}
		out := []byte(encoded)
		if serverXKey != "" {
			if out, err = xkey.Seal(out, serverXKey); err != nil {
				return
			}
		}
		_ = msg.Respond(out)
	})
	require.NoError(t, err)
	require.NoError(t, conn.Flush())
	return conn
}
