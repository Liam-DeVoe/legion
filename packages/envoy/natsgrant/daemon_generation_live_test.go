//go:build natslive

package natsgrant_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// The live half of the generation grant (daemon_generation_test.go), on the image the hub runs
// behind an auth callout that answers as the auth service will once it hands a daemon's connection
// name into the scope: WithinProjects(Daemon(), {PUB, its identity, the connection's name}). A
// connection named for one generation of PUB's GitHub consumer creates that fresh name on its
// filter, reads it, pulls a message and acknowledges it; on the same connection the server refuses
// another generation's create. The controls: a connection with no generation's name creates the
// fixed name and is refused the fresh one, and one named for another project's generation is
// refused it.
func TestTheDaemonsGenerationConnectionReachesThatGenerationAloneOnTheHub(t *testing.T) {
	const user, identity = "pub-daemon", "legion-daemon-PUB"
	keys := testnats.NewCalloutKeys(t)
	keys.XKeySeed, keys.XKeyPublic = "", ""
	adminSeed, adminPublic := testnats.User(t)
	url := testnats.StartImageWithCallout(t, keys, testnats.StaticUser{NKey: adminPublic, Publish: []string{">"}, Subscribe: []string{">"}})
	serveGenerationCallout(t, url, keys, user, natsgrant.Scope{Projects: []string{"PUB"}, Identity: identity})

	admin, err := testnats.ConnectSeed(t, url, adminSeed)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	adminJS, err := jetstream.New(admin)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err = adminJS.CreateStream(ctx, jetstream.StreamConfig{Name: bus.Stream, Subjects: bus.StreamCarriedSubjects()})
	require.NoError(t, err)
	_, err = adminJS.Publish(ctx, contracts.GithubSubject("acme", "pub", "pr.1"), []byte("github acme/pub pr.1"))
	require.NoError(t, err)

	const filter = "notifications.github.>"
	own := consumernames.FreshName("legion-go-PUB-github")
	other := consumernames.FreshName("legion-go-PUB-github")
	createOf := func(name string) string {
		return "$JS.API.CONSUMER.CREATE." + bus.Stream + "." + name + "." + filter
	}
	refusedCreate := func(client *liveClient, name string) {
		t.Helper()
		js, err := jetstream.New(client.conn)
		require.NoError(t, err)
		refusal, cancel := context.WithTimeout(ctx, liveRefusalWait)
		defer cancel()
		_, err = js.CreateOrUpdateConsumer(refusal, bus.Stream, jetstream.ConsumerConfig{Durable: name, FilterSubject: filter, AckPolicy: jetstream.AckExplicitPolicy})
		require.Error(t, err, "the create of %s answered", name)
		client.requireRefused(t, "publish", createOf(name))
	}

	generation := connectAs(t, url, user, natsgo.Name(natsgrant.DaemonGenerationConnection(own)), natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(identity)))
	js, err := jetstream.New(generation.conn)
	require.NoError(t, err)
	consumer := openConsumer(ctx, t, js, own, filter)
	batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(liveWait))
	require.NoError(t, err)
	pulled := 0
	for msg := range batch.Messages() {
		require.NoError(t, msg.DoubleAck(ctx), "acknowledge on the generation's connection")
		pulled++
	}
	require.NoError(t, batch.Error())
	require.Equal(t, 1, pulled, "the generation's consumer delivers the GitHub event")
	generation.requireNotRefused(t, createOf(own))
	refusedCreate(generation, other)

	fixed := connectAs(t, url, user, natsgo.Name("legion-daemon PUB"), natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(identity)))
	fixedJS, err := jetstream.New(fixed.conn)
	require.NoError(t, err)
	openConsumer(ctx, t, fixedJS, "legion-go-PUB-github", filter)
	refusedCreate(fixed, own)

	foreign := consumernames.FreshName("legion-go-PRIV-github")
	elsewhere := connectAs(t, url, user, natsgo.Name(natsgrant.DaemonGenerationConnection(foreign)), natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(identity)))
	refusedCreate(elsewhere, foreign)
}

// serveGenerationCallout answers the server's authorization requests for user with
// WithinProjects(Daemon(), scope) held to the connection's own name, as the auth service hands a
// daemon's connection name into its scope (Scope.Connection); any other user is refused.
func serveGenerationCallout(t *testing.T, url string, keys testnats.CalloutKeys, user string, scope natsgrant.Scope) {
	t.Helper()
	issuer, err := nkeys.FromSeed([]byte(keys.IssuerSeed))
	require.NoError(t, err)
	conn, err := testnats.ConnectSeed(t, url, keys.ServiceSeed)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	_, err = conn.Subscribe("$SYS.REQ.USER.AUTH", func(msg *natsgo.Msg) {
		request, err := jwt.DecodeAuthorizationRequestClaims(string(msg.Data))
		if err != nil {
			return
		}
		response := jwt.NewAuthorizationResponseClaims(request.UserNkey)
		response.Audience = request.Server.ID
		if request.ConnectOptions.Username != user {
			response.Error = "no grant for " + request.ConnectOptions.Username
		} else {
			held := scope
			held.Connection = request.ConnectOptions.Name
			grant, err := natsgrant.WithinProjects(natsgrant.Daemon(), held)
			if err != nil {
				response.Error = err.Error()
			} else {
				claims := jwt.NewUserClaims(request.UserNkey)
				claims.Audience = "$G"
				claims.Name = user
				claims.Expires = time.Now().Add(time.Hour).Unix()
				claims.Pub.Allow.Add(grant.Publish...)
				claims.Sub.Allow.Add(grant.Subscribe...)
				if response.Jwt, err = claims.Encode(issuer); err != nil {
					response.Error = err.Error()
				}
			}
		}
		encoded, err := response.Encode(issuer)
		if err != nil {
			return
		}
		_ = msg.Respond([]byte(encoded))
	})
	require.NoError(t, err)
	require.NoError(t, conn.Flush())
}
