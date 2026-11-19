// Package bustest holds the deployed stream the start tests of envoy-listener and envoy-dispatch
// both begin from, the check each makes of what a start left of it, the drift table each start
// corrects, the fill loop the full-stream tests begin from and the narrowing a test makes when it
// takes a subject out of the stream. The bus's own tests cannot import it (it imports the bus) and
// keep their own copies: the fixture deployedClusterStream, the drift table in
// internal/bus/stream_replicas_test.go and the fill loops in that file and in
// internal/bus/publish_not_stored_test.go. Every test outside package bus takes them from here.
package bustest

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

const (
	// StreamMaxBytes is the deployed stream's byte limit, one every member of a test cluster can
	// reserve.
	StreamMaxBytes = 8 << 20
	// DeployedStreamID is the deployed stream's id (bus.StreamIDMetadataKey).
	DeployedStreamID = "deployed-id"
)

// DeployedThreeCopyStream is the stream as a three-copy deployment with a byte limit
// (StreamMaxBytes) and an id (DeployedStreamID) holds it, discarding old messages as a stream an
// earlier build created does, changed by change.
func DeployedThreeCopyStream(change func(*natsgo.StreamConfig)) natsgo.StreamConfig {
	deployed := natsgo.StreamConfig{
		Name:       bus.Stream,
		Subjects:   bus.StreamCarriedSubjects(),
		Retention:  natsgo.LimitsPolicy,
		MaxAge:     contracts.DeliveryDuplicateWindow,
		Duplicates: contracts.DeliveryDuplicateWindow,
		Discard:    natsgo.DiscardOld,
		Storage:    natsgo.FileStorage,
		Replicas:   3,
		MaxBytes:   StreamMaxBytes,
		Metadata:   map[string]string{bus.StreamIDMetadataKey: DeployedStreamID},
	}
	change(&deployed)
	return deployed
}

// RequireKeptThreeCopyStream fails the test unless after, the DeployedThreeCopyStream after a
// start, holds three copies, the byte limit and every key of before's metadata, its id among them,
// with every subject this binary compiles in and its age, its duplicate window and discard
// corrected to the binary's: a full stream refuses new messages (DiscardNew). A start adds keys of
// its own (the stream's generation), so a key before does not carry is no failure.
func RequireKeptThreeCopyStream(t testing.TB, before natsgo.StreamConfig, after *natsgo.StreamInfo) {
	t.Helper()
	got := after.Config
	if got.Replicas != 3 || testnats.Copies(after.Cluster) != 3 {
		t.Errorf("stream replicas = %d with %d copies, want 3", got.Replicas, testnats.Copies(after.Cluster))
	}
	if got.MaxBytes != StreamMaxBytes || got.Discard != natsgo.DiscardNew {
		t.Errorf("stream byte limit = %d discarding %v, want %d discarding new", got.MaxBytes, got.Discard, StreamMaxBytes)
	}
	kept := true
	for key, value := range before.Metadata {
		kept = kept && got.Metadata[key] == value
	}
	if !kept || got.Metadata[bus.StreamIDMetadataKey] != DeployedStreamID {
		t.Errorf("stream metadata = %v, want the deployed %v kept in it", got.Metadata, before.Metadata)
	}
	if got.MaxAge != contracts.DeliveryDuplicateWindow || got.Duplicates != contracts.DeliveryDuplicateWindow {
		t.Errorf("stream age = %s and duplicate window = %s, want %s", got.MaxAge, got.Duplicates, contracts.DeliveryDuplicateWindow)
	}
	for _, subject := range bus.StreamCarriedSubjects() {
		if !slices.Contains(got.Subjects, subject) {
			t.Errorf("stream subjects = %v, want %s among them", got.Subjects, subject)
		}
	}
}

// StreamDrift is one difference between a deployed stream and the stream a start compiles in, and
// the change that makes a DeployedThreeCopyStream hold it.
type StreamDrift struct {
	Name   string
	Change func(*natsgo.StreamConfig)
}

// StreamDrifts are the three drifts a start must correct: a subject the stream lacks, an age and a
// duplicate window of one minute, and no duplicate window at all. The start tests of
// envoy-listener and envoy-dispatch (cmd/listener/stream_replicas_test.go,
// cmd/dispatch/stream_replicas_test.go) both range over this table. Each call returns a new slice,
// so a caller that changes one entry changes no other caller's run.
func StreamDrifts() []StreamDrift {
	return []StreamDrift{
		{"a changed subject list", func(c *natsgo.StreamConfig) { c.Subjects = c.Subjects[:len(c.Subjects)-1] }},
		{"a one-minute age", func(c *natsgo.StreamConfig) { c.MaxAge, c.Duplicates = time.Minute, time.Minute }},
		{"no duplicate window", func(c *natsgo.StreamConfig) { c.Duplicates = 0 }},
	}
}

// RequireFirstStartAtOneCopyAndNoSecondWrite runs start twice against the single NATS server at
// url and fails the test unless the first start creates each stream in streams, each at one copy,
// and the second start writes nothing to JetStream's API. start runs one whole start and returns
// when that start has done its work. The watch of the API opens before the first start, so a write
// of either start is seen.
//
// The default-settings start tests of envoy-listener and envoy-dispatch
// (cmd/listener/stream_replicas_test.go, cmd/dispatch/stream_replicas_test.go) both call it, each
// with the streams its own binary opens. A second start that creates or updates anything is the
// failure both tests are for: it reconciles a stream it should leave as it is, and a reconcile of
// a deployed stream can lower it.
func RequireFirstStartAtOneCopyAndNoSecondWrite(t testing.TB, url string, streams []string, start func()) {
	t.Helper()
	writes := testnats.WatchStreamWrites(t, url)

	start()
	created := make([]string, 0, len(streams))
	for _, stream := range streams {
		created = append(created, "$JS.API.STREAM.CREATE."+stream)
	}
	if got := writes.Take(t); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(created))) {
		t.Fatalf("the first start wrote %v, want a create of each of %v", got, streams)
	}
	conn := testnats.Connect(t, url)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("open JetStream: %v", err)
	}
	for _, stream := range streams {
		info, err := js.StreamInfo(stream)
		if err != nil {
			t.Fatalf("read %s: %v", stream, err)
		}
		if info.Config.Replicas != 1 {
			t.Errorf("%s replicas = %d, want 1", stream, info.Config.Replicas)
		}
	}

	start()
	if got := writes.Take(t); len(got) != 0 {
		t.Fatalf("the second start wrote %v, want nothing", got)
	}
}

// FillStream publishes messages of a kibibyte each through client, on subjects the stream captures
// (notifications.github.fill.<n>), until the stream refuses one because it is full
// (bus.ErrStreamFull). The test must start the client on a stream whose byte limit is small
// (bus.WithStreamMaxBytes), or the fill never ends: most bounds the publishes, and a run that
// reaches it fails the test. A publish that fails for any other reason fails the test too.
//
// internal/webhook's two full-stream tests call it: the one whose delivery is answered 503
// (TestADeliveryToAFullStreamIsAnswered503) and the one whose delivery is stored on a redelivery
// once the stream has room (TestAGitHubDeliveryTheFullStreamRefusedIsStoredOnARedeliveryOnceTheStreamHasRoom).
// The bus's own tests keep their copies of this loop, because package bus cannot import this
// package.
func FillStream(t testing.TB, client *bus.Client, most int) {
	t.Helper()
	payload := strings.Repeat("x", 1<<10)
	for filled := 0; ; filled++ {
		if filled == most {
			t.Fatalf("the stream took %d messages of a kibibyte, want it full within %d", filled, most)
		}
		err := client.Publish(contracts.Envelope{
			EventID:        fmt.Sprintf("fill-%d", filled),
			Source:         "github",
			Topic:          fmt.Sprintf("notifications.github.fill.%d", filled),
			DedupeKey:      fmt.Sprintf("fill.%d", filled),
			PayloadSummary: payload,
		})
		if err == nil {
			continue
		}
		if !errors.Is(err, bus.ErrStreamFull) {
			t.Fatalf("fill publish %d = %v, want nil or bus.ErrStreamFull", filled, err)
		}
		return
	}
}

// streamGenerationKey is the stream metadata key that holds the generation of the stream's
// settings, spelled out as every build reads it: bus keeps its own unexported.
// narrowedGeneration is the generation NarrowStream stamps there: one no build reaches.
const (
	streamGenerationKey = "dispatch.stream-generation"
	narrowedGeneration  = "1000000"
)

// NarrowStream takes each subject of config that drop matches out of the stream js reaches, puts
// each of add in, and stamps the stream with a generation no build reaches (narrowedGeneration).
// It then reads the stream back, and fails the test unless every dropped subject is gone, every
// added one is there and the stamp holds, so a test never runs on a narrowing that did not take.
// It returns the configuration it wrote; a test lets the stream capture again by writing config
// back.
//
// The stamp is what keeps the narrowing while the test runs. Every client that owns the stream
// (bus.ConnectOwningStream) ensures it at its start and after every reconnect, which puts back
// each subject of its binary's that the stream lacks, and keeps it every 30 s, which puts back each
// one no subject of the stream overlaps. Both leave a stream a newer build stamped exactly as it
// is, and to them a narrowed stream is one. Without the stamp a narrowing held only until the
// next reconnect or keeper pass of any owner of the stream: TestABackedOffEventHoldsBackOnlyItsDestination
// failed on a dev box when the keeper put the agent subjects back a second before the outbox
// published the event the test had refused, and a hub that stops answering for 2 to 3 s makes the
// listener and Dispatch's server reconnect, since they ping it every second.
//
// It fails the test when drop matches no subject.
func NarrowStream(t testing.TB, js natsgo.JetStreamContext, config natsgo.StreamConfig, drop func(subject string) bool, add ...string) natsgo.StreamConfig {
	t.Helper()
	narrowed := config
	narrowed.Subjects = nil
	var dropped []string
	for _, subject := range config.Subjects {
		if drop(subject) {
			dropped = append(dropped, subject)
			continue
		}
		narrowed.Subjects = append(narrowed.Subjects, subject)
	}
	if len(dropped) == 0 {
		t.Fatalf("take a subject out of the stream's %v: none is one to take out", config.Subjects)
	}
	narrowed.Subjects = append(narrowed.Subjects, add...)
	narrowed.Metadata = maps.Clone(config.Metadata)
	if narrowed.Metadata == nil {
		narrowed.Metadata = map[string]string{}
	}
	narrowed.Metadata[streamGenerationKey] = narrowedGeneration
	if _, err := js.UpdateStream(&narrowed); err != nil {
		t.Fatalf("take %v out of the stream: %v", dropped, err)
	}
	info, err := js.StreamInfo(config.Name)
	if err != nil {
		t.Fatalf("read the stream back after taking %v out of it: %v", dropped, err)
	}
	for _, subject := range dropped {
		if slices.Contains(info.Config.Subjects, subject) {
			t.Fatalf("the stream still captures %s after the narrowing: %v", subject, info.Config.Subjects)
		}
	}
	for _, subject := range add {
		if !slices.Contains(info.Config.Subjects, subject) {
			t.Fatalf("the stream does not capture %s after the narrowing: %v", subject, info.Config.Subjects)
		}
	}
	if got := info.Config.Metadata[streamGenerationKey]; got != narrowedGeneration {
		t.Fatalf("the narrowed stream carries generation %q, want %s, which every owner of the stream leaves as it is", got, narrowedGeneration)
	}
	return narrowed
}
