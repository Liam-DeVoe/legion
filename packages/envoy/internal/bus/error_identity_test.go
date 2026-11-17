package bus

import (
	"context"
	"errors"
	"fmt"
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

// A stored write whose acknowledgement the connection's drop lost, the write itself stored, is sent
// again on the new connection only when a second send answers its caller what the first would have
// (sendStored). A publish under a Nats-Msg-Id whose caller reads the duplicate verdict
// (PublishStoredAck) and a cursor write under an expected last sequence (PublishCursorAt) answer
// ErrConnectionLost and are not sent again, so the stream holds the one copy the first send
// stored; their callers read the state again (the cursor, roleKeepLaneCursor.confirm) or answer the
// loss. A publish under a Nats-Msg-Id whose caller reads no verdict (PublishStoredAs) is sent again
// and acknowledged, the stream still holding one copy, and a raw write with no precondition is
// sent again and held twice. Each is sent again within the publish's own bound, publishBound (5 s,
// publishJetStreamOn and publishStoredRaw cap the row's longer context to it). The hold drops the
// connection and lets it reconnect (testnats.ReplyHold.Drop), so a write sent again would be: a cut
// would end the publish's bound first.
//
// The control is the same rows at 7a49d38c1c, which sent every such write again: the message-id
// write whose caller reads the verdict answered the stream's acknowledgement as a duplicate of its
// own first copy, and the cursor write answered ErrCursorMoved, the stream refusing the second send
// because the first had landed.
func TestAStoredWriteWhoseAckTheDropLostIsSentAgainOnlyWhenASecondSendAnswersAlike(t *testing.T) {
	for _, row := range []struct {
		name, subject string
		send          func(context.Context, *Client, string) error
		// lost says the write is sent once and answers ErrConnectionLost; otherwise it is sent
		// again and acknowledged.
		lost   bool
		stored uint64
	}{
		{"a publish under a message id whose caller reads the verdict", contracts.AgentSubject(contracts.NoProject, "ses_lost_stored"), func(ctx context.Context, c *Client, subject string) error {
			item := contracts.Envelope{Topic: subject, Source: "test", DedupeKey: "lost-stored"}
			_, err := c.PublishStoredAck(ctx, subject, item, "lost-stored")
			return err
		}, true, 1},
		{"a publish under a message id whose caller reads no verdict", contracts.AgentSubject(contracts.NoProject, "ses_lost_stored_as"), func(ctx context.Context, c *Client, subject string) error {
			item := contracts.Envelope{Topic: subject, Source: "test", DedupeKey: "lost-stored-as"}
			return c.PublishStoredAs(ctx, subject, item, "lost-stored-as")
		}, false, 1},
		{"a cursor write under an expected last sequence", KeptRoleCursorSubject("lost-cursor"), func(ctx context.Context, c *Client, subject string) error {
			_, err := c.PublishCursorAt(ctx, subject, []byte(`{"kept":7}`), 0)
			if errors.Is(err, ErrCursorMoved) {
				return fmt.Errorf("the cursor write was refused as the cursor having moved, though the first send landed: %w", err)
			}
			return err
		}, true, 1},
		{"a raw write with no precondition", SessionResumeSubject("ses_lost_raw"), func(ctx context.Context, c *Client, subject string) error {
			_, err := c.publishStoredRaw(ctx, subject, []byte(`{"floor":3}`), nil)
			return err
		}, false, 2},
	} {
		t.Run(row.name, func(t *testing.T) {
			client, hold := heldClient(t)
			held := hold.Hold(row.subject)
			answered := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), ownWait(t))
				defer cancel()
				answered <- row.send(ctx, client, row.subject)
			}()
			select {
			case <-held.Withheld():
			case <-time.After(testnats.Remaining(t)):
				t.Fatal("the hold withheld no acknowledgement by the test's deadline")
			}
			dropped := time.Now()
			hold.Drop()
			var err error
			select {
			case err = <-answered:
			case <-time.After(testnats.Remaining(t)):
				t.Fatal("the write whose acknowledgement the drop lost was still waiting at the test's deadline")
			}
			switch {
			case row.lost && !errors.Is(err, ErrConnectionLost):
				t.Fatalf("the write answered %v %s after the drop, want ErrConnectionLost: a write with a precondition is sent once", err, time.Since(dropped))
			case !row.lost && err != nil:
				t.Fatalf("the write answered %v %s after the drop, want it sent again and acknowledged", err, time.Since(dropped))
			}
			t.Logf("the write answered %s after the drop: %v", time.Since(dropped), err)
			if got := storedOn(t, row.subject); got != row.stored {
				t.Fatalf("the stream holds %d messages on %s, want %d", got, row.subject, row.stored)
			}
		})
	}
}

// storedOn is how many messages the stream holds on subject, read on a connection of its own.
func storedOn(t *testing.T, subject string) uint64 {
	t.Helper()
	js, err := testnats.Connect(t, testnats.URL(t)).JetStream()
	if err != nil {
		t.Fatalf("open JetStream on the server: %v", err)
	}
	info, err := js.StreamInfo(Stream, &nats.StreamInfoRequest{SubjectsFilter: subject})
	if err != nil {
		t.Fatalf("read the stream's messages on %s: %v", subject, err)
	}
	return info.State.Subjects[subject]
}
