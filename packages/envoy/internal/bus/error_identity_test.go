package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// closedNotLost reports whether err is nats.ErrConnectionClosed and not ErrConnectionLost.
func closedNotLost(err error) bool {
	return errors.Is(err, nats.ErrConnectionClosed) && !errors.Is(err, ErrConnectionLost)
}

// A request a client's stop cut off, and one made on a client already stopped, answer
// nats.ErrConnectionClosed, which their callers read as lasting: cistore's write retry gives up on
// it at once (kvErrorLasts), and sendStored takes its closed branch. ErrConnectionLost is a running
// client's, whose caller waits for a reconnect, which a stopped client never makes; a running
// client's loss still answers it (TestARequestWhoseReplyTheDropLostFailsAtTheDrop).
//
// The control is the same row at 2b8a5d7cbb: every request answered ErrConnectionLost, so a CI
// record write on a stopped client spent its whole budget retrying.
func TestARequestOnAStoppedClientAnswersConnectionClosed(t *testing.T) {
	client, hold := heldClient(t)
	kv := lostReplyBucket(t, client)
	held := hold.Hold("$JS.API.DIRECT.GET.KV_envoy_lost_reply")
	answered := make(chan error, 1)
	go func() {
		_, err := kv.Get("key")
		answered <- err
	}()
	select {
	case <-held.Withheld():
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the hold withheld no reply by the test's deadline")
	}
	client.Close()
	select {
	case err := <-answered:
		if !closedNotLost(err) {
			t.Fatalf("the read the client's stop cut off answered %v, want nats.ErrConnectionClosed and not ErrConnectionLost", err)
		}
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the read the client's stop cut off was still waiting at the test's deadline")
	}
	if _, err := kv.Get("key"); !closedNotLost(err) {
		t.Fatalf("a KV read on the stopped client answered %v, want nats.ErrConnectionClosed and not ErrConnectionLost", err)
	}
	if _, err := client.JS().StreamInfo(Stream); !closedNotLost(err) {
		t.Fatalf("a JetStream call on the stopped client answered %v, want nats.ErrConnectionClosed and not ErrConnectionLost", err)
	}
}

// A stored publish whose acknowledgement the connection's drop lost is sent again once the
// connection is back, inside the publish's own bound (sendStored): publishBound, 5 s, to which the
// publish caps the row's longer context (PublishStoredAck through publishJetStreamOn). The caller
// is answered the stream's acknowledgement, as a duplicate of the copy the first send stored under
// the same Nats-Msg-Id, rather than ErrConnectionLost.
//
// The control is the same row at 2b8a5d7cbb: the client's JetStream context answered the loss
// ErrConnectionLost, which sendStored's retry, reading nats.ErrConnectionClosed alone, passed over,
// so the publish failed with the loss.
func TestAStoredPublishWhoseAckTheDropLostIsSentAgainOnTheNewConnection(t *testing.T) {
	client, hold := heldClient(t)
	// An agent subject, which the stream stores, so the reply the hold withholds is the stream's
	// acknowledgement of the first send.
	subject := contracts.AgentSubject(contracts.NoProject, "ses_lost_stored")
	item := contracts.Envelope{Topic: subject, Source: "test", DedupeKey: "lost-stored"}
	held := hold.Hold(subject)
	type answer struct {
		ack StoredAck
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
		defer cancel()
		ack, err := client.PublishStoredAck(ctx, subject, item, "lost-stored")
		answered <- answer{ack, err}
	}()
	select {
	case <-held.Withheld():
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the hold withheld no acknowledgement by the test's deadline")
	}
	dropped := time.Now()
	hold.Drop()
	select {
	case got := <-answered:
		if got.err != nil {
			t.Fatalf("the publish whose acknowledgement the drop lost answered %v %s after the drop, want it sent again and acknowledged", got.err, time.Since(dropped))
		}
		if !got.ack.Duplicate {
			t.Fatalf("the publish sent again was stored as a new copy at %d, want it acknowledged as the duplicate of the first send's", got.ack.Sequence)
		}
		t.Logf("the publish was sent again and acknowledged %s after the drop, a duplicate of seq %d", time.Since(dropped), got.ack.Sequence)
	case <-time.After(testnats.Remaining(t)):
		t.Fatal("the publish whose acknowledgement the drop lost was still waiting at the test's deadline")
	}
}
