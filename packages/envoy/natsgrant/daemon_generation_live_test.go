//go:build natslive

package natsgrant_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
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
	hub := startLiveHub(t, generationGrants(user, natsgrant.Scope{Projects: []string{"PUB"}, Identity: identity}))
	url := hub.url
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := hub.js.Publish(ctx, contracts.GithubSubject("acme", "pub", "pr.1"), []byte("github acme/pub pr.1"))
	require.NoError(t, err)

	const filter = "notifications.github.>"
	// Generations in NUID form, 22 letters and digits, as a daemon's fresh names carry.
	const own, other = "legion-go-PUB-github~RFuDfoOI3HLgXUdIZvZnZx", "legion-go-PUB-github~RFuDfoOI3HLgXUdIZvZsCP"
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

	const foreign = "legion-go-PRIV-github~RFuDfoOI3HLgXUdIZvZnZx"
	elsewhere := connectAs(t, url, user, natsgo.Name(natsgrant.DaemonGenerationConnection(foreign)), natsgo.CustomInboxPrefix(natsgrant.InboxPrefix(identity)))
	refusedCreate(elsewhere, foreign)
}

// generationGrants admits user with WithinProjects(Daemon(), scope) held to the connection's own
// name, as the auth service hands a daemon's connection name into its scope (Scope.Connection),
// and refuses any other user.
func generationGrants(user string, scope natsgrant.Scope) grantResolver {
	return func(request *jwt.AuthorizationRequestClaims) (natsgrant.Permissions, error) {
		if request.ConnectOptions.Username != user {
			return natsgrant.Permissions{}, errors.New("no grant for " + request.ConnectOptions.Username)
		}
		held := scope
		held.Connection = request.ConnectOptions.Name
		return natsgrant.WithinProjects(natsgrant.Daemon(), held)
	}
}
