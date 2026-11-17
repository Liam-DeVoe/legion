package testnats

import (
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// A hold reports a reply withheld once the server's answer to a held request reaches it, and a
// flush only once the PONG of a PING the client sent after that request has reached the client:
// a request that sent no PING is reported withheld and never flushed. That is what lets a test
// drop the connection while a flushing request waits for its reply, rather than while it is still
// in its flush, which nats.go ends at the drop by itself (processOpErr clears the pending flushes).
//
// The control is the hold at 2b8a5d7cbb, whose one channel closed 50 ms after the withheld reply
// whether a PONG followed it or not: the same request is reported ready with no PING sent.
func TestAReplyHoldReportsAFlushOnlyOnceItsPongReachesTheClient(t *testing.T) {
	url := URL(t)
	responder := Connect(t, url)
	if _, err := responder.Subscribe("hold.test.request", func(m *natsgo.Msg) { _ = m.Respond([]byte("answer")) }); err != nil {
		t.Fatalf("subscribe the responder: %v", err)
	}
	if err := responder.Flush(); err != nil {
		t.Fatalf("flush the responder's subscription: %v", err)
	}
	hold := StartReplyHold(t, url)
	client := Connect(t, hold.URL())
	inbox := natsgo.NewInbox()
	replies, err := client.SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe the reply inbox: %v", err)
	}
	held := hold.Hold("hold.test.")
	// A publish with no flush behind it: nats.go's flusher writes it, and sends no PING.
	if err := client.PublishRequest("hold.test.request", inbox, []byte("question")); err != nil {
		t.Fatalf("send the request: %v", err)
	}
	select {
	case <-held.Withheld():
	case <-time.After(5 * time.Second):
		t.Fatal("the hold did not withhold the reply within 5 s")
	}
	select {
	case <-held.Flushed():
		t.Fatal("the hold reported a flush the client never sent")
	case <-time.After(200 * time.Millisecond):
	}
	if err := client.Flush(); err != nil {
		t.Fatalf("flush the client: %v", err)
	}
	select {
	case <-held.Flushed():
	case <-time.After(5 * time.Second):
		t.Fatal("the hold did not report the flush within 5 s of its PONG")
	}
	if msg, err := replies.NextMsg(100 * time.Millisecond); err == nil {
		t.Fatalf("the held reply reached the client: %q", msg.Data)
	}
}
