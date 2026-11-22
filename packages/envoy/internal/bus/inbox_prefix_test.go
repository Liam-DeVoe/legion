package bus_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// testInboxPrefix is the inbox prefix of this file's clients, and the one subject family their
// user may subscribe.
const testInboxPrefix = "_INBOX_bus_test"

// A client given an inbox prefix names every inbox under it, on its own connection and on the
// session consumers', and takes JetStream's replies there, under a user that may subscribe that
// prefix and no other inbox. The same user's client without the prefix asks and is answered
// nothing: its replies come under _INBOX., which the server refuses it.
func TestAClientWithAnInboxPrefixTakesItsRepliesUnderIt(t *testing.T) {
	seed, public := testnats.User(t)
	_, uri := testnats.StartConfig(t, testnats.SubscribeGrantConfig(public, testInboxPrefix+".>"), []string{"-js"}, false)
	t.Setenv("NATS_NKEY_SEED", seed)
	ask := func(t *testing.T, client *bus.Client) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := client.Connection().RequestWithContext(ctx, "$JS.API.INFO", nil)
		return err
	}

	t.Run("with the prefix", func(t *testing.T) {
		client, err := bus.Connect([]string{uri}, bus.WithInboxPrefix(testInboxPrefix))
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(client.Close)
		if inbox := client.Connection().NewInbox(); !strings.HasPrefix(inbox, testInboxPrefix+".") {
			t.Fatalf("the connection's inbox is %q, want one under %s", inbox, testInboxPrefix)
		}
		if err := ask(t, client); err != nil {
			t.Fatalf("JetStream's account info under the prefix: %v", err)
		}
		consumers, err := bus.ConnectSessionConsumers([]string{uri}, 0, nil, bus.WithInboxPrefix(testInboxPrefix))
		if err != nil {
			t.Fatalf("connect the session consumers: %v", err)
		}
		t.Cleanup(consumers.Close)
		if inbox := consumers.NewInbox(); !strings.HasPrefix(inbox, testInboxPrefix+".") {
			t.Fatalf("the session consumers' inbox is %q, want one under %s", inbox, testInboxPrefix)
		}
		conn, err := bus.Dial("inbox-prefix-dial", []string{uri}, os.LookupEnv, bus.WithInboxPrefix(testInboxPrefix))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(conn.Close)
		if inbox := conn.NewInbox(); !strings.HasPrefix(inbox, testInboxPrefix+".") {
			t.Fatalf("the dialled connection's inbox is %q, want one under %s", inbox, testInboxPrefix)
		}
	})
	t.Run("without it", func(t *testing.T) {
		client, err := bus.Connect([]string{uri})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(client.Close)
		if err := ask(t, client); err == nil {
			t.Fatal("a client whose replies come under _INBOX. was answered by a server that refuses it that subscription")
		}
	})
}

// Every request a client built WithInboxPrefix makes names its reply inbox under the prefix: a
// stored forward's (StartRequestStoredTo) and a window's (Sender.Request) alike. A client whose
// grant subscribes only prefix.> is otherwise refused the inbox subscription of each.
//
// Control: put either site back on the package's nats.NewInbox(), which is always _INBOX.; its
// request's reply reads _INBOX. and its row fails.
func TestEveryRequestOfAPrefixedClientRepliesUnderThePrefix(t *testing.T) {
	const prefix = "_INBOX_prefix-test"
	url := testnats.URL(t)
	client, err := bus.ConnectOwningStream([]string{url}, bus.WithInboxPrefix(prefix))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(client.Close)
	watcher, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("connect the watcher: %v", err)
	}
	t.Cleanup(watcher.Close)
	replies := make(chan *natsgo.Msg, 8)
	if _, err := watcher.ChanSubscribe("test.uncaptured.inbox-prefix.>", replies); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := watcher.Flush(); err != nil {
		t.Fatalf("flush the watcher: %v", err)
	}
	item := contracts.Envelope{EventID: "evt-inbox-prefix", Source: "dispatch", SourceEventID: "src-inbox-prefix", Topic: "notifications.role.inbox-prefix", DedupeKey: "inbox-prefix", IssuedAt: contracts.NowMillis()}

	const stored = "test.uncaptured.inbox-prefix.stored"
	forward, err := client.StartRequestStoredTo(stored, item, bus.ForwardMsgID(item.DedupeKey, stored), time.Second)
	if err != nil {
		t.Fatalf("StartRequestStoredTo: %v", err)
	}
	t.Cleanup(func() { _ = forward.Wait() })
	if reply := testwait.Receive(t, "the stored forward's request", replies).Reply; !strings.HasPrefix(reply, prefix+".") {
		t.Fatalf("the stored forward's reply inbox is %q, not under %s", reply, prefix)
	}

	const windowed = "test.uncaptured.inbox-prefix.window"
	answer := client.WindowUntil(nil).Request(windowed, item, bus.RequestOptions{MsgID: bus.ForwardMsgID(item.DedupeKey, windowed), Timeout: time.Second})
	t.Cleanup(func() { <-answer })
	if reply := testwait.Receive(t, "the window's request", replies).Reply; !strings.HasPrefix(reply, prefix+".") {
		t.Fatalf("the window's request's reply inbox is %q, not under %s", reply, prefix)
	}
}
