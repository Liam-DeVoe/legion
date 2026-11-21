package consumernames

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/writerhold"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/holdrecord"
)

// fullCapConsumers is how many fixed consumers fill the row's stream's max_consumers.
const fullCapConsumers = 8

// fullCapSeedStreams makes every protected stream hold set's preflight reads by name but the
// selection bucket, which the cutover declares absent until create-bucket: the notification
// stream at max_consumers maxConsumers (0: the server's default) and each other bucket, under a
// streams-update hold of the operator's own, released with nothing else changed (writerhold's
// hold_set_test.go seedProtectedStreams, on one copy).
func fullCapSeedStreams(t *testing.T, h *stack, maxConsumers int) {
	t.Helper()
	ctx := context.Background()
	id := h.heldHold(holdrecord.WorkflowStreamsUpdate)
	nc, js := h.operator()
	_, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: bus.Stream, Subjects: bus.StreamCarriedSubjects(),
		MaxConsumers: maxConsumers, Storage: jetstream.FileStorage})
	require.NoError(t, err)
	for _, bucket := range append([]string{bus.LegionAgentBucket, bus.DPoPProofBucket, bus.WebhookRedeliveryBucket}, bus.CopiedBuckets()...) {
		_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, err, "create bucket %s", bucket)
	}
	nc.Close()
	require.NoError(t, h.release(id))
}

// With ENVOY_NOTIFICATIONS' consumer cap full of fixed consumers, each with a message it has not
// acknowledged, the window runs through the real hold to convert's final listing: hold prepare's
// row and journal, consumer-names prepare, hold set's own steps (writerhold.Set: the preflight,
// the probe, the writer census, the restart and proof of every declared server, the meta barrier
// and the report), create-bucket, and convert, whose own barrier is a stream and takes no consumer
// slot; after the release every pending message arrives on the fresh names, and no stream update
// changed max_consumers. Control, run first: plan v4's barrier, an unnamed consumer, is refused at
// the full cap with err_code 10026, so a hold built on it stops before convert. It fails when the
// hold or convert needs a consumer slot, or a pending message is lost.
func TestAFullCapWindowReachesConversion(t *testing.T) {
	fullCapWindowRow(t)
}

// fullCapWindowRow is TestAFullCapWindowReachesConversion's row, which forced-order.sh also runs on
// nats-server 2.10.29 (TestForcedOrderAFullCapWindowReachesConversion).
func fullCapWindowRow(t *testing.T) {
	h := newStack(t)
	ctx := context.Background()
	fullCapSeedStreams(t, h, fullCapConsumers)
	rig := &convertRig{stack: h}
	rig.connectWriter()
	pending := map[string]uint64{}
	for i := range fullCapConsumers {
		session := fmt.Sprintf("cap%d", i)
		subject := "notifications.agent." + session
		seqs := rig.publish(subject, 2)
		rig.fixed("s-"+session, subject, time.Hour)
		consumer, err := rig.writer.Consumer(ctx, notifications, "s-"+session)
		require.NoError(t, err)
		require.NoError(t, rig.ackUpTo(consumer, seqs[0]))
		pending["s-"+session] = seqs[1]
	}
	_, err := rig.writer.CreateConsumer(ctx, notifications, jetstream.ConsumerConfig{AckPolicy: jetstream.AckExplicitPolicy, InactiveThreshold: time.Minute})
	var api *jetstream.APIError
	require.True(t, errors.As(err, &api) && api.ErrorCode == errConsumerLimit, "control: an unnamed consumer at the full cap answered %v, not err_code %d", err, errConsumerLimit)

	id := h.prepared(holdrecord.WorkflowCutover)
	code, _, stderr := h.run(actionPrepare, "--hold", id)
	require.Zero(t, code, stderr)
	rig.closeWriter()
	reading, err := writerhold.Set(ctx, h.records(), h.op, id, "the full-cap window", nil)
	require.NoError(t, err, "hold set")
	require.NotEmpty(t, reading.Servers)
	require.Equal(t, "done", latestEntry(h.journal(id), holdrecord.CommandSet))
	code, _, stderr = h.run(actionCreateBucket, "--hold", id)
	require.Zero(t, code, stderr)
	code, stdout, stderr := h.run(actionConvert, "--hold", id)
	require.Zero(t, code, stderr)
	require.Contains(t, stdout, "the final listing holds no fixed name")
	require.Empty(t, rig.fixedNames())
	nc, js := h.operator()
	stream, err := js.Stream(ctx, bus.Stream)
	require.NoError(t, err)
	require.Equal(t, fullCapConsumers, stream.CachedInfo().Config.MaxConsumers, "a stream update changed max_consumers")
	nc.Close()

	fresh := map[string]string{}
	for name := range pending {
		m, _ := classify(name)
		fresh[name] = rig.kvRecord(m.Logical).Current
	}
	rig.censusDone(id)
	require.NoError(t, h.release(id))
	rig.connectWriter()
	for name, seq := range pending {
		require.Equal(t, []uint64{seq}, rig.consume(fresh[name], 5, 0), "%s's pending message on %s", name, fresh[name])
	}
}
