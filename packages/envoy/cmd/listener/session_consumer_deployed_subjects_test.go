package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/session"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A topic under a subject only a newer build's stream carries (notifications.x.>, which this build
// does not compile) is registered through this build's listener with no warning, and the session's
// consumer filters on it, so a message stored on it while the two builds run is the consumer's to
// deliver. The registration route and the consumer filter judge the one deployed stream: a route
// that stored the topic and a filter that left it out would answer the session 200 for a topic it
// is never delivered. A topic no stream captures is still left out of the registration with the
// warning, and out of the filter.
func TestSessionConsumerFiltersOnATopicOnlyTheDeployedStreamCaptures(t *testing.T) {
	const (
		sessionID  = "ses_deployed_subject"
		newer      = "notifications.x.>"
		captured   = "notifications.x.y"
		uncaptured = "notifications.lxspec.probe.all"
	)
	for _, topic := range []string{captured, uncaptured} {
		if bus.StreamCaptures(topic) {
			t.Fatalf("this build compiles a subject capturing %s; the test needs one it does not", topic)
		}
	}
	url := testnats.URL(t)
	deployNewerBuildsStream(t, url, newer)
	client, err := bus.ConnectOwningStream([]string{url}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener: %v", err)
	}
	t.Cleanup(client.Close)
	if client.ReachesNoSession(captured) {
		t.Fatalf("the listener's client judges %s on the subjects it compiles, not on the deployed stream's", captured)
	}
	rig := newSessionConsumerRigOn(t, client, url)

	raw := rig.post(t, "/v1/interests/subscribe", `{"session_id":"`+sessionID+`","self_subscribed":true,
		"topics":["`+captured+`","`+uncaptured+`"],"delivery":{"consumer":true,"epoch":"e"}}`)
	var answer interestSubscribeAnswer
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		t.Fatalf("decode the registration's answer %s: %v", raw, err)
	}
	if want := []string{contracts.UncapturedTopicRefusal(uncaptured, sessionID)}; !slices.Equal(answer.Warnings, want) {
		t.Fatalf("warnings = %q, want only %q: %s is captured by the deployed stream and %s by no stream", answer.Warnings, want, captured, uncaptured)
	}
	if !slices.Contains(answer.Topics, captured) || slices.Contains(answer.Topics, uncaptured) {
		t.Fatalf("answered topics = %v, want %s among them and %s not", answer.Topics, captured, uncaptured)
	}
	want := []string{"notifications.agent.*." + sessionID, contracts.LegacyAgentSubject(sessionID), captured}
	if got := rig.filters(t, sessionID); !slices.Equal(got, want) {
		t.Fatalf("consumer filters %v, want %v: the filter must carry the topic the registration stored, and leave out the one it did not", got, want)
	}

	rig.store(t, captured)
	rig.awaitPending(t, sessionID, 1)
}

// A fan-out of a message on a topic only the deployed stream captures, to a session on consumer
// delivery that follows it, is that session's own consumer's to relay: the consumer filters on the
// topic (sessionConsumerFilter), so the fan-out counts the session skipped, records nothing for
// it, and publishes no delivery exception. A fan-out that judged the topic on the subjects this
// build compiles would read the session as one nothing reaches and report a failure for a message
// its consumer delivers.
//
// The row hands the message to the fan-out through the core handler, which no production path
// does for such a topic today: the listener's own durable filters on the subjects this build
// compiles (durable.go, startListenerSubscription, bus.StreamSubjects), so on an older build a
// topic only a newer build's subjects capture never reaches the fan-out, and the session's
// consumer filter is what delivers it during a roll. The row stands for a fan-out that does see
// such a message, and holds that it judges it as the consumer filter does.
func TestFanoutLeavesATopicOnlyTheDeployedStreamCapturesToTheSessionsConsumer(t *testing.T) {
	const (
		sessionID = "ses_deployed_fanout"
		topic     = "notifications.x.y"
	)
	if bus.StreamCaptures(topic) {
		t.Fatalf("this build compiles a subject capturing %s; the test needs one it does not", topic)
	}
	harness := newListenerDeliveryHarnessOn(t, connectToNewerBuildsStream(t, "notifications.x.>"))
	harness.registerConsumerTarget(t, sessionID, topic, nil)
	if _, state := harness.sessions.Lookup(sessionID); state != session.Live {
		t.Fatalf("session %s reads %s before the delivery, want live", sessionID, state)
	}
	item := listenerTestEnvelope(topic, "fanout-deployed-subject")
	probe := exceptionProbe(t, harness.client)

	harness.coreHandler(&natsgo.Msg{Data: marshalListenerEnvelope(t, item)})
	if err := harness.client.Conn.Flush(); err != nil {
		t.Fatalf("flush what the fan-out published: %v", err)
	}

	body := metricsBody(t, harness)
	if got := counterValue(t, body, deliveredSeries("skipped")); got != 1 {
		t.Fatalf("%s = %d, want 1 - the session's own consumer holds %s:\n%s", deliveredSeries("skipped"), got, topic, body)
	}
	if got := counterValue(t, body, deliveredSeries("failed")); got != -1 {
		t.Fatalf("%s = %d, want no series at all - the session's consumer relays the message:\n%s", deliveredSeries("failed"), got, body)
	}
	noException(t, probe)
	if harness.config.dedupeCache.Seen(item.DedupeKey, sessionID) || harness.config.attemptCache.Seen(item.DedupeKey, sessionID) {
		t.Fatalf("the fan-out recorded %s, which it handed the message to nobody for", sessionID)
	}
}

// A delivery exception on a lane only the deployed stream captures is stored under its one-copy
// message id (exception.<reason>.<origin>), so the stream keeps one copy however many listeners
// raise it for the same stored message. Judged on the subjects this build compiles, the lane would
// be published under each exception's own dedupe key, and every listener's report would be kept.
func TestADeliveryExceptionOnALaneOnlyTheDeployedStreamCapturesIsStoredOnce(t *testing.T) {
	const (
		topic  = "notifications.x.y"
		lane   = bus.ExceptionTopicPrefix + topic
		reason = "delivery_failed"
		origin = "origin-1"
	)
	if bus.StreamCaptures(lane) {
		t.Fatalf("this build compiles a subject capturing %s; the test needs one it does not", lane)
	}
	client := connectToNewerBuildsStream(t, bus.ExceptionTopicPrefix+"notifications.x.>")
	item := listenerTestEnvelope(topic, "exception-deployed-lane")
	// Two listeners raise the exception for the one stored message, each from its own durable.
	for range 2 {
		if err := publishDeliveryException(client, item, reason, "ses_deployed_exception", origin); err != nil {
			t.Fatalf("publish the delivery exception: %v", err)
		}
	}
	js := client.JS()
	info, err := js.StreamInfo(bus.Stream, &natsgo.StreamInfoRequest{SubjectsFilter: lane})
	if err != nil {
		t.Fatalf("read %s: %v", bus.Stream, err)
	}
	if got := info.State.Subjects[lane]; got != 1 {
		t.Fatalf("the stream holds %d exceptions on %s, want 1", got, lane)
	}
	stored, err := js.GetLastMsg(bus.Stream, lane)
	if err != nil {
		t.Fatalf("read the exception on %s: %v", lane, err)
	}
	if got, want := stored.Header.Get(natsgo.MsgIdHdr), "exception."+reason+"."+origin; got != want {
		t.Fatalf("stored message id = %q, want %q", got, want)
	}
}

// A delivery exception on a lane this build stores is stored under its one-copy message id also
// while the listener's last read of the deployed stream's subjects left that lane out: the stream
// may have captured it again since that read, and the publish takes JetStream all the same, since
// this build stores the lane (bus.Client.UsesCoreTransport). Judged on the deployed subjects
// alone, the lane would be published under each exception's own dedupe key, and every listener's
// report of the one stored message would be kept.
func TestADeliveryExceptionOnALaneThisBuildStoresIsStoredOnceWhateverTheListenerLastRead(t *testing.T) {
	const (
		reason = "delivery_failed"
		origin = "origin-1"
	)
	topic := contracts.LegacyAgentSubject("ses_stale_exception")
	lane := bus.ExceptionTopicPrefix + topic
	if !bus.StreamCaptures(lane) {
		t.Fatalf("this build compiles no subject capturing %s; the test needs one it does", lane)
	}
	var dropped, restored []string
	for _, carried := range bus.StreamCarriedSubjects() {
		if bus.SubjectContains(carried, lane) {
			restored = append(restored, carried)
			continue
		}
		dropped = append(dropped, carried)
	}
	url := testnats.URL(t)
	deployNewerBuildsStreamOf(t, url, dropped)
	client, err := bus.ConnectOwningStream([]string{url}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener: %v", err)
	}
	t.Cleanup(client.Close)
	// The newer build puts the lane back after the listener read the subjects.
	js := client.JS()
	info, err := js.StreamInfo(bus.Stream)
	if err != nil {
		t.Fatalf("read %s: %v", bus.Stream, err)
	}
	config := info.Config
	config.Subjects = append(config.Subjects, restored...)
	if _, err := js.UpdateStream(&config); err != nil {
		t.Fatalf("the newer build puts %v back on %s: %v", restored, bus.Stream, err)
	}
	item := listenerTestEnvelope(topic, "exception-stale-lane")
	// Two listeners raise the exception for the one stored message, each from its own durable.
	for range 2 {
		if err := publishDeliveryException(client, item, reason, "ses_stale_exception", origin); err != nil {
			t.Fatalf("publish the delivery exception: %v", err)
		}
	}
	if client.DeployedStreamCaptures(lane) {
		t.Fatalf("the listener has read the subjects again, so this test no longer covers a read that left %s out", lane)
	}
	info, err = js.StreamInfo(bus.Stream, &natsgo.StreamInfoRequest{SubjectsFilter: lane})
	if err != nil {
		t.Fatalf("read %s: %v", bus.Stream, err)
	}
	if got := info.State.Subjects[lane]; got != 1 {
		t.Fatalf("the stream holds %d exceptions on %s, want 1", got, lane)
	}
	stored, err := js.GetLastMsg(bus.Stream, lane)
	if err != nil {
		t.Fatalf("read the exception on %s: %v", lane, err)
	}
	if got, want := stored.Header.Get(natsgo.MsgIdHdr), "exception."+reason+"."+origin; got != want {
		t.Fatalf("stored message id = %q, want %q", got, want)
	}
}

// A send to a stopped session is answered 200 only once the stream holds the message, also while
// the deployed stream is a newer build's, which this build leaves as it is, that captures no subject
// of the session's: the send is then refused with the bus's 503, nothing is stored, and the sender
// keeps the message to send again. A stopped answer tells the sender the message is kept for the
// session, which reads only what its own consumer holds, so a 200 over a publish that stored nothing
// - over core NATS, the transport the deployed subjects alone would choose - would lose the message
// under an answer that says it was kept. The negative control runs first: the same send over a
// newer build's stream that still captures the subject is the 200 stopped answer with one copy
// stored, so the refusal is the missing subject's doing and not the rig's.
func TestASendIsAnsweredStoppedOnlyOnceTheStreamHoldsIt(t *testing.T) {
	const target = "ses_deployed_stream_drops_agent"
	subject := contracts.LegacyAgentSubject(target)
	if !bus.StreamCaptures(subject) {
		t.Fatalf("this build compiles no subject capturing %s; the test needs one it does", subject)
	}
	var dropped []string
	for _, carried := range bus.StreamCarriedSubjects() {
		if !bus.SubjectContains(carried, subject) {
			dropped = append(dropped, carried)
		}
	}
	for _, tc := range []struct {
		name     string
		subjects []string
		status   int
		stored   uint64
	}{
		{"the deployed stream captures the subject", bus.StreamCarriedSubjects(), http.StatusOK, 1},
		{"the deployed stream captures no subject of it", dropped, http.StatusServiceUnavailable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := testnats.URL(t)
			deployNewerBuildsStreamOf(t, url, tc.subjects)
			client, err := bus.ConnectOwningStream([]string{url}, bus.WithReplicas(1))
			if err != nil {
				t.Fatalf("connect the listener: %v", err)
			}
			t.Cleanup(client.Close)
			if got, want := client.DeployedStreamCaptures(subject), tc.stored == 1; got != want {
				t.Fatalf("the listener reads the deployed stream as capturing %s: %t, want %t", subject, got, want)
			}
			rig := newKnownSessionRigOn(t, client, url)
			// An entry recording consumer delivery whose relay subject nothing holds, so the probe
			// reads the session stopped.
			if err := rig.deps.sessions.Put(target, session.SessionEntry{SelfSubscribed: true, MachineID: "test-machine", Delivery: session.DeliveryConsumer}); err != nil {
				t.Fatalf("write the session entry of %s: %v", target, err)
			}

			answer := rig.send(t, target, nil)

			stored := rig.storedOn(t, subject)
			t.Logf("status %d, %d copies on %s, body %s", answer.status, stored, subject, answer.raw)
			if answer.status == http.StatusOK && stored == 0 {
				t.Fatalf("the send was answered 200 (%s) and the stream holds no copy on %s", answer.raw, subject)
			}
			if answer.status != tc.status || stored != tc.stored {
				t.Fatalf("status %d, body %s, %d copies on %s; want %d and %d", answer.status, answer.raw, stored, subject, tc.status, tc.stored)
			}
			if answer.status == http.StatusOK && answer.State != sessionStateStopped {
				t.Fatalf("state %q, want %s: nothing holds the session's relay subject", answer.State, sessionStateStopped)
			}
			if answer.status != http.StatusOK && !strings.HasPrefix(answer.Error, busUnansweredPrefix) {
				t.Fatalf("the refusal reads %q, want it to open with %q", answer.Error, busUnansweredPrefix)
			}
		})
	}
}

// connectToNewerBuildsStream starts a server, creates on it the stream a newer build carrying
// extra leaves (deployNewerBuildsStream), and connects this build's listener client to it, which
// owns the stream and leaves it as it is.
func connectToNewerBuildsStream(t *testing.T, extra ...string) *bus.Client {
	t.Helper()
	url := testnats.URL(t)
	deployNewerBuildsStream(t, url, extra...)
	client, err := bus.ConnectOwningStream([]string{url}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// deployNewerBuildsStream creates the notifications stream on the server url names as a newer
// build leaves it: every subject this build carries and extra (deployNewerBuildsStreamOf).
func deployNewerBuildsStream(t *testing.T, url string, extra ...string) {
	t.Helper()
	deployNewerBuildsStreamOf(t, url, append(bus.StreamCarriedSubjects(), extra...))
}

// deployNewerBuildsStreamOf creates the notifications stream on the server url names as a newer
// build carrying subjects leaves it, under a generation above any this build stamps, so a start of
// this build that owns the stream leaves it as it is, a subject of this build's the newer one left
// out included. It carries the stream's identity, which a consumer registration answers with.
func deployNewerBuildsStreamOf(t *testing.T, url string, subjects []string) {
	t.Helper()
	conn := testnats.Connect(t, url)
	defer conn.Close()
	js, err := conn.JetStream()
	if err != nil {
		t.Fatalf("open JetStream to create the newer build's stream: %v", err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{
		Name:      bus.Stream,
		Subjects:  subjects,
		Retention: natsgo.LimitsPolicy,
		Discard:   natsgo.DiscardNew,
		Storage:   natsgo.FileStorage,
		// The generation key is spelled out, as every build reads it: bus keeps its own unexported.
		Metadata: map[string]string{bus.StreamIDMetadataKey: id.New(), "dispatch.stream-generation": "1000000"},
	}); err != nil {
		t.Fatalf("the newer build creates %s: %v", bus.Stream, err)
	}
}
