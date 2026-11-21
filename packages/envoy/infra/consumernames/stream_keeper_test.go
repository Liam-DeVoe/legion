package consumernames

import (
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	selection "github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The old stream keeper's one listing of consumers, internal/bus stream.go's
// migrateLegacyConsumerFilters, which a start runs when it moves the role lanes off a stream that
// still carries them (migrateRoleLanesOffStream, reached here through bus.EnsureNotificationStream
// on a stream whose subjects are the legacy catch-all notifications.>), updates only a consumer
// whose one filter is exactly notifications.>. With every new consumer's filter on the stream (two
// fresh session consumers, one filtered on its agent subject's forms and one on a single subject;
// a fresh listener durable; Dispatch's two fresh durables; and a daemon's fresh durables, one with
// a filter list and one with a single filter), that start updates none of them: each keeps the
// configuration it was created with. Control: a consumer whose filter is exactly notifications.>
// is updated to the build's filter list.
func TestTheOldStreamKeepersListingUpdatesNoNewConsumer(t *testing.T) {
	_, url := testnats.Start(t)
	conn := testnats.Connect(t, url)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	require.NoError(t, err)
	build := bus.NotificationStreamConfig(1, 0)
	legacy := build
	legacy.Subjects = []string{"notifications.>"}
	_, err = js.AddStream(&legacy)
	require.NoError(t, err)

	agent := bus.SessionConsumerFilters([]string{contracts.LegacyAgentSubject("ses_keeper")})
	require.Greater(t, len(agent), 1, "the agent subject's forms")
	session := func(filters ...string) nats.ConsumerConfig {
		return nats.ConsumerConfig{Durable: selection.FreshName(bus.SessionConsumerNamePrefix), AckPolicy: nats.AckExplicitPolicy,
			FilterSubjects: filters, MaxDeliver: -1, Metadata: map[string]string{bus.SessionIDMetadataKey: "ses_keeper"}}
	}
	fresh := []nats.ConsumerConfig{
		session(agent...),
		session(contracts.LegacyAgentSubject("ses_keeper")),
		{Durable: selection.FreshName(bus.ListenerDurable("hub")), DeliverSubject: nats.NewInbox(), FilterSubjects: bus.StreamSubjects(),
			AckPolicy: nats.AckExplicitPolicy},
		{Durable: selection.FreshName("legion-go-KEEP-dispatch"), FilterSubjects: []string{"notifications.dispatch.issue.>"}, AckPolicy: nats.AckExplicitPolicy},
		{Durable: selection.FreshName("legion-go-KEEP-wake"), FilterSubject: bus.LegionWakeTopic, AckPolicy: nats.AckExplicitPolicy},
	}
	for _, config := range []nats.ConsumerConfig{bus.DispatchDeliveryConsumer(), bus.DispatchJobConsumer()} {
		config.Durable = selection.FreshName(config.Durable)
		fresh = append(fresh, config)
	}
	created := map[string]nats.ConsumerConfig{}
	for _, config := range fresh {
		info, err := js.AddConsumer(bus.Stream, &config)
		require.NoError(t, err, "create %s", config.Durable)
		require.NotEqual(t, "notifications.>", info.Config.FilterSubject, "%s is filtered on the catch-all", config.Durable)
		created[config.Durable] = info.Config
	}
	const catchAll = "legacy-catch-all"
	_, err = js.AddConsumer(bus.Stream, &nats.ConsumerConfig{Durable: catchAll, FilterSubject: "notifications.>", AckPolicy: nats.AckExplicitPolicy})
	require.NoError(t, err)

	_, err = bus.EnsureNotificationStream(t.Context(), conn, js, build)
	require.NoError(t, err)

	for name, before := range created {
		info, err := js.ConsumerInfo(bus.Stream, name)
		require.NoError(t, err)
		require.Equal(t, before, info.Config, "the keeper's listing updated %s", name)
	}
	info, err := js.ConsumerInfo(bus.Stream, catchAll)
	require.NoError(t, err)
	require.Empty(t, info.Config.FilterSubject, "control: the catch-all consumer was not updated")
	require.NotEmpty(t, info.Config.FilterSubjects, "control: the catch-all consumer was not given the build's filters")
	require.NotContains(t, info.Config.FilterSubjects, "notifications.>")
}
