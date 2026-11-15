package bus

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

// freshSubjectsClient creates ENVOY_NOTIFICATIONS with this build's subjects, as a deployment of
// this build left it, and connects a client that owns no stream to it, whose own refresh of the
// deployed subjects is an hour away: every read of them the row sees is one
// DeployedStreamCapturesFresh made. It returns the client and a raw JetStream context the row
// plays another build's process with.
func freshSubjectsClient(t *testing.T) (*Client, nats.JetStreamContext, string) {
	t.Helper()
	uri := testnats.URL(t)
	js := rawJetStream(t, uri)
	current := *streamCfg
	current.Replicas = 1
	current.Metadata = map[string]string{generationKey: "1"}
	if _, err := js.AddStream(&current); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	client, err := Connect([]string{uri}, withStreamSubjectsRefresh(time.Hour))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	return client, js, uri
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
	const topic = "notifications.x.y"
	if StreamCaptures(topic) {
		t.Fatalf("this build compiles a subject capturing %s; the row needs a topic only a newer build's subject captures", topic)
	}
	client, js, _ := freshSubjectsClient(t)
	newer := *streamCfg
	newer.Replicas = 1
	newer.Subjects = append(slices.Clone(streamCarriedSubjects), "notifications.x.>")
	newer.Metadata = map[string]string{generationKey: "2"}
	if _, err := js.UpdateStream(&newer); err != nil {
		t.Fatalf("the newer build adds its subject: %v", err)
	}
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
