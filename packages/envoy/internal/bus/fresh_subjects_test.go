package bus

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// freshSubjectsClient creates ENVOY_NOTIFICATIONS with this build's subjects, as a deployment of
// this build left it, and connects a client that owns no stream to it, whose own refresh of the
// deployed subjects is an hour away: every read of them the row sees after the connect's is one
// the row makes itself or a judgement or publish of the row makes. It returns the client and a raw
// JetStream context the row plays another build's process with.
func freshSubjectsClient(t *testing.T) (*Client, nats.JetStreamContext, string) {
	t.Helper()
	uri := testnats.URL(t)
	client, js := freshSubjectsClientVia(t, uri, uri)
	return client, js, uri
}

// freshSubjectsClientVia is freshSubjectsClient on the server at uri, whose client dials dial
// instead: uri itself, or a relay to it.
func freshSubjectsClientVia(t *testing.T, uri, dial string) (*Client, nats.JetStreamContext) {
	t.Helper()
	js := rawJetStream(t, uri)
	current := *streamCfg
	current.Replicas = 1
	current.Metadata = map[string]string{generationKey: "1"}
	if _, err := js.AddStream(&current); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	client, err := Connect([]string{dial}, withStreamSubjectsRefresh(time.Hour))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	return client, js
}

// streamInfoReads subscribes a connection of the row's own to the subject a read of the stream's
// subjects is sent on (streamInfoSubject) and returns a func that answers how many such reads
// reached the server since it last answered: the wire's count, whatever sent them.
func streamInfoReads(t *testing.T, uri string) func() int {
	t.Helper()
	watcher, err := nats.Connect(uri, nats.Name("stream-info-watcher"))
	if err != nil {
		t.Fatalf("connect the watcher: %v", err)
	}
	t.Cleanup(watcher.Close)
	reads, err := watcher.SubscribeSync(streamInfoSubject)
	if err != nil {
		t.Fatalf("subscribe to %s: %v", streamInfoSubject, err)
	}
	if err := watcher.Flush(); err != nil {
		t.Fatalf("flush the watcher's subscription: %v", err)
	}
	return func() int {
		t.Helper()
		// The server routes a request to every subscriber of its subject before the request's
		// answer, and writes the flush's PONG after it, so every read answered by now is pending.
		if err := watcher.Flush(); err != nil {
			t.Fatalf("flush the watcher: %v", err)
		}
		pending, _, err := reads.Pending()
		if err != nil {
			t.Fatalf("count the reads: %v", err)
		}
		for range pending {
			if _, err := reads.NextMsg(time.Second); err != nil {
				t.Fatalf("take a counted read: %v", err)
			}
		}
		return pending
	}
}

// judgeAtOnce has n goroutines judge topic at the same moment and returns each answer and how long
// the burst took, from its release to its last answer.
func judgeAtOnce(client *Client, topic string, n int) ([]bool, time.Duration) {
	release := make(chan struct{})
	answers := make([]bool, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-release
			answers[i] = client.DeployedStreamCapturesFresh(context.Background(), topic)
		})
	}
	began := time.Now()
	close(release)
	wg.Wait()
	return answers, time.Since(began)
}

// A burst of judgements of a topic no stream captures, all within streamSubjectsRereadGap, costs
// the server one read of the deployed stream's subjects, which every judgement of the burst shares,
// and a judgement after them within the gap costs none; the first past the gap reads again. A
// topic the stream captures is answered with no read at all.
func TestABurstOfUncapturedJudgementsReadsTheDeployedSubjectsOnce(t *testing.T) {
	const (
		uncaptured = "notifications.nostream.burst"
		captured   = "notifications.team.burst"
		burst      = 20
	)
	if StreamCaptures(uncaptured) {
		t.Fatalf("this build compiles a subject capturing %s; the row needs a topic no stream captures", uncaptured)
	}
	client, _, uri := freshSubjectsClient(t)
	reads := streamInfoReads(t, uri)

	answers, _ := judgeAtOnce(client, captured, burst)
	if slices.Contains(answers, false) {
		t.Fatalf("judgements of %s answered %v, want all captured", captured, answers)
	}
	if n := reads(); n != 0 {
		t.Fatalf("%d judgements of %s, which the stream captures, sent %d reads of the stream's subjects, want none", burst, captured, n)
	}

	answers, took := judgeAtOnce(client, uncaptured, burst)
	ended := time.Now()
	if slices.Contains(answers, true) {
		t.Fatalf("judgements of %s answered %v, want none captured", uncaptured, answers)
	}
	if took >= streamSubjectsRereadGap {
		t.Fatalf("the burst of %d judgements took %s, not inside %s; the row measures a burst inside the gap", burst, took, streamSubjectsRereadGap)
	}
	if n := reads(); n != 1 {
		t.Fatalf("%d judgements of %s at once, in %s, sent %d reads of the stream's subjects, want 1", burst, uncaptured, took.Round(time.Microsecond), n)
	}
	t.Logf("%d judgements of %s at once took %s and sent one read of the stream's subjects", burst, uncaptured, took.Round(time.Microsecond))

	for range burst {
		if client.DeployedStreamCapturesFresh(context.Background(), uncaptured) {
			t.Fatalf("a judgement of %s answered captured", uncaptured)
		}
	}
	if since := time.Since(ended); since >= streamSubjectsRereadGap {
		t.Fatalf("the judgements after the burst ended %s after it, not inside %s", since, streamSubjectsRereadGap)
	}
	if n := reads(); n != 0 {
		t.Fatalf("%d judgements of %s within %s of the last read sent %d reads, want none", burst, uncaptured, streamSubjectsRereadGap, n)
	}

	time.Sleep(time.Until(ended.Add(streamSubjectsRereadGap)))
	client.DeployedStreamCapturesFresh(context.Background(), uncaptured)
	if n := reads(); n != 1 {
		t.Fatalf("a judgement of %s past %s after the last read sent %d reads, want 1", uncaptured, streamSubjectsRereadGap, n)
	}
}

// A judgement of a topic only a subject added since the client last read the stream's subjects
// captures - a newer build's start adding its own during a roll - reads them again and answers
// captured, and keeps what it read, so DeployedStreamCaptures answers captured from then on too.
func TestAnUncapturedJudgementReadsASubjectAddedSinceTheLastRead(t *testing.T) {
	const topic = newerTopic
	requireOnlyNewerCaptures(t)
	client, js, _ := freshSubjectsClient(t)
	addNewerSubject(t, js)
	if client.DeployedStreamCaptures(topic) {
		t.Fatalf("the client judged %s captured before reading the stream's subjects again; the row needs a stale read", topic)
	}
	if !client.DeployedStreamCapturesFresh(context.Background(), topic) {
		t.Fatalf("DeployedStreamCapturesFresh(%s) = false after the stream added notifications.x.>", topic)
	}
	if !client.DeployedStreamCaptures(topic) {
		t.Fatalf("DeployedStreamCaptures(%s) = false after the fresh read: the read was not kept", topic)
	}
}

const (
	// newerSubject is the subject a newer build's start adds to the stream during a roll
	// (addNewerSubject).
	newerSubject = "notifications.x.>"
	// newerTopic is a topic only newerSubject captures (requireOnlyNewerCaptures).
	newerTopic = "notifications.x.y"
)

// requireOnlyNewerCaptures fails the row when a subject this build compiles captures newerTopic:
// the row needs a topic only a newer build's subject captures.
func requireOnlyNewerCaptures(t *testing.T) {
	t.Helper()
	if StreamCaptures(newerTopic) {
		t.Fatalf("this build compiles a subject capturing %s; the row needs a topic only a newer build's subject captures", newerTopic)
	}
}

// addNewerSubject plays a newer build's start during a roll: it adds newerSubject to the stream
// freshSubjectsClient created, through js.
func addNewerSubject(t *testing.T, js nats.JetStreamContext) {
	t.Helper()
	newer := *streamCfg
	newer.Replicas = 1
	newer.Subjects = append(slices.Clone(streamCarriedSubjects), newerSubject)
	newer.Metadata = map[string]string{generationKey: "2"}
	if _, err := js.UpdateStream(&newer); err != nil {
		t.Fatalf("the newer build adds its subject: %v", err)
	}
}

// A read of the deployed stream's subjects that started before another and ends after it keeps
// nothing over it. R1, the periodic read (followDeployedSubjects' readDeployedSubjects), reads the
// subjects and is held once it has them and before it keeps them (beforeSubjectsKept); a newer
// build adds its subject; R2, a judgement of a topic only that subject captures
// (DeployedStreamCapturesFresh), reads the subjects again and keeps them; R1 then ends. The client
// keeps R2's read, with the newer build's subject, and judges the topic captured.
//
// Before a read kept its answer only over one that started before it (keepSubjects), each read
// kept its answer when it ended, so R1 put back the subjects as they stood before the newer
// build's start, and the client judged the topic uncaptured until its next read.
func TestASubjectsReadThatEndsAfterALaterOneKeepsTheLaterOnesSubjects(t *testing.T) {
	const topic = newerTopic
	requireOnlyNewerCaptures(t)
	client, js, _ := freshSubjectsClient(t)
	conn := client.ConnectedConn()
	if conn == nil {
		t.Fatal("the client is not connected")
	}
	held, release := make(chan struct{}), make(chan struct{})
	releaseR1 := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseR1)
	var holding atomic.Bool
	holding.Store(true)
	client.beforeSubjectsKept = func() {
		if holding.CompareAndSwap(true, false) {
			close(held)
			<-release
		}
	}
	r1 := make(chan struct{})
	go func() {
		defer close(r1)
		client.readDeployedSubjects(conn, streamSubjectsRead)
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the periodic read of the stream's subjects did not reach the moment before it keeps them")
	}

	addNewerSubject(t, js)
	if !client.DeployedStreamCapturesFresh(context.Background(), topic) {
		t.Fatalf("DeployedStreamCapturesFresh(%s) = false after the newer build added notifications.x.>", topic)
	}
	r2 := client.deployedSubjects.Load()
	if !slices.Contains(r2.subjects, newerSubject) {
		t.Fatalf("the fresh read kept %v, without notifications.x.>", r2.subjects)
	}

	releaseR1()
	select {
	case <-r1:
	case <-time.After(10 * time.Second):
		t.Fatal("the periodic read did not end once released")
	}
	if kept := client.deployedSubjects.Load(); kept != r2 {
		t.Fatalf("after the periodic read that started first ended last, the client keeps %v (read %d), not the fresh read's %v (read %d)",
			kept.subjects, kept.seq, r2.subjects, r2.seq)
	}
	if !client.DeployedStreamCaptures(topic) {
		t.Fatalf("DeployedStreamCaptures(%s) = false after the periodic read that started first ended last", topic)
	}
}

// A read of the deployed stream's subjects that starts after the read the client keeps has ended
// replaces it: no read is ever discarded as older than one that started before it. R1 reads the
// subjects and keeps them; a newer build adds its subject; R2, started after R1 ended, keeps the
// subjects with it.
func TestASubjectsReadThatStartsAfterTheKeptOneReplacesIt(t *testing.T) {
	const topic = newerTopic
	requireOnlyNewerCaptures(t)
	client, js, _ := freshSubjectsClient(t)
	conn := client.ConnectedConn()
	if conn == nil {
		t.Fatal("the client is not connected")
	}
	if !client.readDeployedSubjects(conn, streamSubjectsRead) {
		t.Fatal("the first read of the stream's subjects was refused")
	}
	r1 := client.deployedSubjects.Load()
	if slices.Contains(r1.subjects, newerSubject) {
		t.Fatalf("the first read kept %v before the newer build added notifications.x.>", r1.subjects)
	}

	addNewerSubject(t, js)
	if !client.readDeployedSubjects(conn, streamSubjectsRead) {
		t.Fatal("the second read of the stream's subjects was refused")
	}
	r2 := client.deployedSubjects.Load()
	if r2 == r1 || !slices.Contains(r2.subjects, newerSubject) {
		t.Fatalf("the read that started after the kept one (read %d) ended keeps %v (read %d), without notifications.x.>",
			r1.seq, r2.subjects, r2.seq)
	}
	if !client.DeployedStreamCaptures(topic) {
		t.Fatalf("DeployedStreamCaptures(%s) = false after a read that started after the kept one", topic)
	}
}

// A publish on a topic only a subject added since the client last read the stream's subjects
// captures - a newer build's start adding its own during a roll - goes through JetStream: with no
// read of the subjects in flight, the publish judges its transport on a fresh read of them
// (PublishReportingDuplicateWithin, PublishTakesJetStream), not on the subjects the client holds,
// which the row proves stale right before the publish, with no read of them since the newer build
// added its subject. The stream stores the envelope under its Nats-Msg-Id, and keeps one copy of a
// repeat of the publish, which it reports a duplicate.
//
// Before a publish judged a topic core only on a fresh read, it sent this one over core NATS on the
// stale subjects (UsesCoreTransport): the stream stored that copy too, as it stores any publish on
// a subject it captures, but under no Nats-Msg-Id and with no acknowledgement, so the repeat was
// stored again and neither store was confirmed to the publisher.
func TestAPublishJudgesItsTransportOnAFreshReadOfTheSubjects(t *testing.T) {
	const topic = newerTopic
	requireOnlyNewerCaptures(t)
	client, js, uri := freshSubjectsClient(t)
	reads := streamInfoReads(t, uri)
	addNewerSubject(t, js)
	kept := client.deployedSubjects.Load()
	if kept == nil {
		t.Fatal("the client holds no read of the stream's subjects; the row needs the one it made at its connect")
	}
	if slices.Contains(kept.subjects, newerSubject) || !client.UsesCoreTransport(topic) {
		t.Fatalf("the client holds %v and judges %s core %t before the publish; the row needs the subjects it read at its connect, without notifications.x.>",
			kept.subjects, topic, client.UsesCoreTransport(topic))
	}
	if n := reads(); n != 0 {
		t.Fatalf("the stream's subjects were read %d times since the newer build added notifications.x.>; the row needs none before the publish", n)
	}

	envelope := contracts.Envelope{
		EventID:        "evt-fresh-transport",
		Source:         "dispatch",
		Topic:          topic,
		DedupeKey:      "dispatch.fresh-transport.1",
		IssuedAt:       contracts.NowMillis(),
		PayloadSummary: "a topic only a subject added since the client's last read captures",
		TraceID:        "trace-fresh-transport",
	}
	publish := func() bool {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), publishBound)
		defer cancel()
		duplicate, err := client.PublishReportingDuplicateWithin(ctx, envelope)
		if err != nil {
			t.Fatalf("publish on %s: %v", topic, err)
		}
		return duplicate
	}
	if publish() {
		t.Fatalf("the first publish on %s reported a duplicate", topic)
	}
	msgID := ForwardMsgID(envelope.DedupeKey, topic)
	stored, err := js.GetLastMsg(Stream, topic)
	if err != nil {
		t.Fatalf("the stream holds no message on %s after a publish that answered success (%v): no acknowledgement confirmed the store, as none does for a publish sent over core NATS", topic, err)
	}
	if got := stored.Header.Get(nats.MsgIdHdr); got != msgID {
		t.Fatalf("the stream stored the publish on %s under Nats-Msg-Id %q, want %q: the publish went over core NATS on the client's stale subjects, which carries no MsgId and draws no acknowledgement, not through JetStream",
			topic, got, msgID)
	}
	if !publish() {
		t.Fatalf("the repeat of the publish on %s reported no duplicate of Nats-Msg-Id %q", topic, msgID)
	}
	info, err := js.StreamInfo(Stream, &nats.StreamInfoRequest{SubjectsFilter: topic})
	if err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	if got := info.State.Subjects[topic]; got != 1 {
		t.Fatalf("the stream holds %d messages on %s after a publish and its repeat, want 1", got, topic)
	}
}

// A publish's transport answers on the subjects the client holds while a read of them is in flight
// and unanswered, and does not wait for that read (PublishTakesJetStream): a judgement that loses a
// message for good (DeployedStreamCapturesFresh) of a topic no stream captures starts a read, which
// the relay leaves unanswered for its whole firstStreamSubjectsRead (2 s); a publish of that topic
// then returns within 100 ms, sending no read of its own, while that judgement still waits.
//
// Before the transport answered on the subjects held during a read in flight, it joined that read
// as every other fresh judgement does, so a core publish of a topic no stream captures - a delivery
// exception on a github, dispatch, slack or legion lane - waited the read out, up to 2 s.
func TestAPublishsTransportDoesNotWaitOnAnUnansweredSubjectsRead(t *testing.T) {
	const (
		uncaptured = "notifications.nostream.inflight"
		bound      = 100 * time.Millisecond
	)
	if StreamCaptures(uncaptured) {
		t.Fatalf("this build compiles a subject capturing %s; the row needs a topic no stream captures", uncaptured)
	}
	uri := testnats.URL(t)
	relay := testnats.StartUnansweredRelayOn(t, uri, streamInfoSubject)
	client, _ := freshSubjectsClientVia(t, uri, relay.URL())
	relay.Arm(1)
	waiting := make(chan bool, 1)
	go func() { waiting <- client.DeployedStreamCapturesFresh(context.Background(), uncaptured) }()
	awaitRelaySent(t, relay, 1)

	envelope := contracts.Envelope{
		EventID:        "evt-transport-in-flight",
		Source:         "dispatch",
		Topic:          uncaptured,
		DedupeKey:      "dispatch.transport-in-flight.1",
		IssuedAt:       contracts.NowMillis(),
		PayloadSummary: "a topic no stream captures, published while a read of the subjects goes unanswered",
		TraceID:        "trace-transport-in-flight",
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishBound)
	defer cancel()
	began := time.Now()
	if _, err := client.PublishReportingDuplicateWithin(ctx, envelope); err != nil {
		t.Fatalf("publish on %s: %v", uncaptured, err)
	}
	if took := time.Since(began); took > bound {
		t.Fatalf("the publish on %s took %s, over %s: its transport waited on the unanswered read of the stream's subjects in flight", uncaptured, took.Round(time.Millisecond), bound)
	}
	select {
	case answer := <-waiting:
		t.Fatalf("the judgement that started the read answered %t before the publish returned; the row needs that read still in flight", answer)
	default:
	}
	if sent := relay.Sent(); sent != 1 {
		t.Fatalf("the stream's subjects were read %d times, want only the one in flight: the publish's transport read them again", sent)
	}
	select {
	case answer := <-waiting:
		if answer {
			t.Fatalf("the judgement of %s whose read went unanswered answered captured", uncaptured)
		}
	case <-time.After(2 * firstStreamSubjectsRead):
		t.Fatalf("the judgement of %s did not end within twice the read's own bound of %s", uncaptured, firstStreamSubjectsRead)
	}
}

// An ensure keeps the subjects as it leaves them over a read of them that started inside it and
// was answered before its write landed. The client's ensure reads the stream, which lacks
// notifications.x.>, and is about to send the update that adds it; just before it does
// (interleavedJetStream.beforeUpdate), R, a read of the client's, runs to its end and keeps the
// subjects without it; the update lands and the ensure ends. The client keeps the subjects the
// ensure left, notifications.x.> among them, read back once its write has landed (ensureStream),
// and judges a topic under it captured.
//
// Before the ensure kept that read-back, it kept the list it wrote numbered as the ensure started,
// before R did, so R's older list, kept first, stood over it, and the client judged a topic under
// the subject its own ensure had added uncaptured until its next read.
func TestAnEnsureKeepsItsSubjectsOverAReadAnsweredBeforeItsUpdate(t *testing.T) {
	const topic = newerTopic
	requireOnlyNewerCaptures(t)
	client, _, _ := freshSubjectsClient(t)
	conn := client.ConnectedConn()
	if conn == nil {
		t.Fatal("the client is not connected")
	}
	cfg := parseConnectOptions([]ConnectOption(nil)).streamConfig()
	cfg.Subjects = append(slices.Clone(streamCarriedSubjects), newerSubject)
	client.streamConfig = &cfg
	var r *subjectsRead
	interleaved := &interleavedJetStream{JetStreamContext: client.JS(), beforeUpdate: func() {
		if !client.readDeployedSubjects(conn, streamSubjectsRead) {
			t.Fatal("the read inside the ensure was refused")
		}
		r = client.deployedSubjects.Load()
	}}
	if err := client.ensureStream(interleaved, capsAnUnsetStream); err != nil {
		t.Fatalf("ensure the stream with notifications.x.>: %v", err)
	}
	if r == nil {
		t.Fatal("the ensure sent no update; the row needs one that adds notifications.x.>")
	}
	if slices.Contains(r.subjects, newerSubject) {
		t.Fatalf("the read inside the ensure kept %v, with notifications.x.> before the ensure's update added it", r.subjects)
	}
	kept := client.deployedSubjects.Load()
	if kept == r || !slices.Contains(kept.subjects, newerSubject) {
		t.Fatalf("after the ensure that added notifications.x.> ended, the client keeps %v (read %d), the read answered before the ensure's update, not the subjects the ensure left",
			kept.subjects, kept.seq)
	}
	if !client.DeployedStreamCaptures(topic) {
		t.Fatalf("DeployedStreamCaptures(%s) = false after the ensure that added notifications.x.>", topic)
	}
}
