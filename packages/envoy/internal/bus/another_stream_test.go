package bus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// anotherProgramsStream is a stream another program of the deployment keeps beside the
// notifications stream, which no session consumer reads.
const anotherProgramsStream = "ANOTHER_PROGRAMS_STREAM"

// Every stored publish of the bus counts as stored only on the acknowledgement of the
// notifications stream (Stream), the one stream the session consumers read. A newer build's
// notifications stream, which this build leaves as it is, can drop a subject this build stores,
// and then another stream of the account may capture it: that stream's acknowledgement is of a
// copy no consumer reads, so each stored publish answers an error for it, and neither stream holds
// the message. Each kind of stored send is a row: the publish of an envelope on its own topic and
// on a subject the caller names, the cursor write, a window's two sends and its request, and a
// forward. The negative control runs every row first over a notifications stream that captures the
// subject: nil and one copy each, so an error in the second run is the other stream's doing and not
// the row's.
func TestEveryStoredPublishIsStoredOnlyByTheNotificationsStream(t *testing.T) {
	envelope := func(subject string) contracts.Envelope {
		return contracts.Envelope{
			EventID:   "evt-" + subject,
			Source:    "agent",
			Topic:     subject,
			DedupeKey: "agent.stored." + subject,
			IssuedAt:  contracts.NowMillis(),
		}
	}
	publishes := []struct {
		name    string
		publish func(c *Client, subject string) error
	}{
		{"Publish", func(c *Client, subject string) error {
			return c.Publish(envelope(subject))
		}},
		{"PublishStoredAck", func(c *Client, subject string) error {
			_, err := c.PublishStoredAck(context.Background(), subject, envelope(subject), "stored-ack."+subject)
			return err
		}},
		{"PublishCursorAt", func(c *Client, subject string) error {
			_, err := c.PublishCursorAt(context.Background(), subject, []byte(`{"cursor":1}`), 0)
			return err
		}},
		{"a window's PublishAsync", func(c *Client, subject string) error {
			return <-c.WindowUntil(nil).PublishAsync(envelope(subject))
		}},
		{"a window's PublishStoredAsync", func(c *Client, subject string) error {
			return <-c.WindowUntil(nil).PublishStoredAsync(subject, envelope(subject), "stored-async."+subject)
		}},
		{"a window's Request", func(c *Client, subject string) error {
			return <-c.WindowUntil(nil).Request(subject, envelope(subject), RequestOptions{MsgID: "request." + subject, Timeout: 2 * time.Second})
		}},
		{"a forward", func(c *Client, subject string) error {
			forward, err := c.StartRequestStoredTo(subject, envelope(subject), "forward."+subject, 2*time.Second)
			if err != nil {
				return err
			}
			return forward.Wait()
		}},
	}
	agentSubjects := contracts.AgentTopicPrefix + ">"
	if !slices.Contains(streamCarriedSubjects, agentSubjects) {
		t.Fatalf("this build carries no %s to drop", agentSubjects)
	}
	for _, stream := range []struct {
		name     string
		subjects []string
		another  bool
	}{
		{"the notifications stream captures the subject", streamCarriedSubjects, false},
		{"another stream captures the subject the notifications stream dropped", slices.DeleteFunc(slices.Clone(streamCarriedSubjects), func(s string) bool { return s == agentSubjects }), true},
	} {
		t.Run(stream.name, func(t *testing.T) {
			uri := testnats.URL(t)
			js := rawJetStream(t, uri)
			if _, err := js.AddStream(&nats.StreamConfig{
				Name:      Stream,
				Subjects:  stream.subjects,
				Retention: nats.LimitsPolicy,
				Discard:   nats.DiscardNew,
				Storage:   nats.FileStorage,
				Metadata:  map[string]string{StreamIDMetadataKey: "stream-of-a-newer-build", generationKey: strconv.Itoa(streamConfigGeneration + 1)},
			}); err != nil {
				t.Fatalf("the newer build creates %s: %v", Stream, err)
			}
			if stream.another {
				if _, err := js.AddStream(&nats.StreamConfig{Name: anotherProgramsStream, Subjects: []string{agentSubjects}, Storage: nats.FileStorage}); err != nil {
					t.Fatalf("another program creates %s: %v", anotherProgramsStream, err)
				}
			}
			client, err := ConnectOwningStream([]string{uri})
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			t.Cleanup(client.Close)
			for i, p := range publishes {
				subject := fmt.Sprintf("%sses_stored_%d", contracts.AgentTopicPrefix, i)
				started := time.Now()
				err := p.publish(client, subject)
				elapsed := time.Since(started)
				held := storedIn(t, js, Stream, subject)
				elsewhere := uint64(0)
				if stream.another {
					elsewhere = storedIn(t, js, anotherProgramsStream, subject)
				}
				t.Logf("%s on %s: err=%v, %d in %s, %d in %s, after %s", p.name, subject, err, held, Stream, elsewhere, anotherProgramsStream, elapsed)
				if err == nil && held == 0 {
					t.Errorf("%s on %s answered nil and %s holds no copy (%d in %s)", p.name, subject, Stream, elsewhere, anotherProgramsStream)
					continue
				}
				if !stream.another && (err != nil || held != 1) {
					t.Errorf("control: %s on %s = %v with %d copies in %s, want nil and 1", p.name, subject, err, held, Stream)
				}
				if stream.another && (err == nil || held != 0 || elsewhere != 0) {
					t.Errorf("%s on %s = %v with %d copies in %s and %d in %s, want an error and none", p.name, subject, err, held, Stream, elsewhere, anotherProgramsStream)
				}
			}
		})
	}
}

// A stored publish the notifications stream did not take is named ErrStreamLacksSubject, with its
// subject and the generation of the stream the client read, exactly when the server's answer says
// that stream did not take it - no stream answered the subject, or another stream refused a send
// that named the notifications stream - and the client's last read of the deployed stream carries
// no subject capturing it. Every other failure keeps its own error, so the listener answers it in
// its own words: a bus that did not answer, a full stream, and a refusal the read cannot explain,
// a stream that went away after a read that still carried the subject.
func TestAStoredPublishTheDeployedStreamLeavesOutIsNamedWithItsSubjectAndGeneration(t *testing.T) {
	const subject = "notifications.agent.ses_left_out"
	leavesOut := &deployedRead{subjects: []string{"notifications.github.>"}, generation: 7}
	carries := &deployedRead{subjects: []string{"notifications.agent.>"}, generation: 7}
	mismatch := &nats.APIError{Code: 400, ErrorCode: jsStreamNotMatch, Description: "expected stream does not match"}
	full := streamFull(&nats.APIError{Code: 503, ErrorCode: jsStreamStoreFailed, Description: maxBytesExceeded})
	for _, tc := range []struct {
		name     string
		deployed *deployedRead
		err      error
		lacks    bool
	}{
		{"no stream answered and the read leaves the subject out", leavesOut, nats.ErrNoStreamResponse, true},
		{"another stream refused and the read leaves the subject out", leavesOut, mismatch, true},
		{"no stream answered and the read carries the subject", carries, nats.ErrNoStreamResponse, false},
		{"no stream answered and the client has read no stream", nil, nats.ErrNoStreamResponse, false},
		{"the bus did not answer", leavesOut, context.DeadlineExceeded, false},
		{"the stream is full", leavesOut, full, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{}
			if tc.deployed != nil {
				client.deployed.Store(tc.deployed)
			}
			got := client.lacksSubject(subject, tc.err)
			if errors.Is(got, ErrStreamLacksSubject) != tc.lacks {
				t.Fatalf("lacksSubject(%v) = %v; ErrStreamLacksSubject %t, want %t", tc.err, got, !tc.lacks, tc.lacks)
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("lacksSubject(%v) = %v, which no longer matches the server's answer", tc.err, got)
			}
			if tc.lacks && (!strings.Contains(got.Error(), strconv.Quote(subject)) || !strings.Contains(got.Error(), "generation 7")) {
				t.Fatalf("lacksSubject(%v) = %q, want it to name %s and generation 7", tc.err, got, subject)
			}
		})
	}
}

// storedIn is how many messages stream holds on subject.
func storedIn(t *testing.T, js nats.JetStreamContext, stream, subject string) uint64 {
	t.Helper()
	info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: subject})
	if err != nil {
		t.Fatalf("read what %s holds on %s: %v", stream, subject, err)
	}
	return info.State.Subjects[subject]
}
