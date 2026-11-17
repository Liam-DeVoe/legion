//go:build odatrace

package hooktest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/sjawhar/envoy/internal/testnats"
)

// Each test below holds the hooked server at one hold point and shows the hold is real: the
// point is reached, what the server was about to do there does not happen while it is held, and
// it happens once the hold is released. Each runs its control first, the same action unheld, so
// the reading that sees nothing while the hold parks the server is one that does see the action
// land. Removing the point from the hooked server (forced-order.sh -drop <point>) fails the test,
// at the wait for the hold to be reached.

// held is how long a test watches for what a hold keeps from happening, and reachWithin how long
// it waits for what the release lets happen.
const (
	held        = 500 * time.Millisecond
	reachWithin = 30 * time.Second
)

// heldConsumer is a lone server's stream HOLD with three messages, and C, a durable pull consumer
// of it whose acks a test holds on their way to C's state file.
type heldConsumer struct {
	srv   *testnats.Server
	hooks *testnats.Hooks
	sub   *natsgo.Subscription
}

func newHeldConsumer(t *testing.T) *heldConsumer {
	t.Helper()
	srv, uri := testnats.Start(t)
	hooks := testnats.Hook(t)
	conn := testnats.Connect(t, uri)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(natsgo.MaxWait(reachWithin))
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: "HOLD", Subjects: []string{"hold.>"}}); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	for i := range 3 {
		if _, err := js.Publish(fmt.Sprintf("hold.%d", i+1), nil); err != nil {
			t.Fatalf("publish message %d: %v", i+1, err)
		}
	}
	if _, err := js.AddConsumer("HOLD", &natsgo.ConsumerConfig{Durable: "C", AckPolicy: natsgo.AckExplicitPolicy, AckWait: time.Minute}); err != nil {
		t.Fatalf("create the consumer: %v", err)
	}
	sub, err := js.PullSubscribe("", "C", natsgo.Bind("HOLD", "C"))
	if err != nil {
		t.Fatalf("bind the consumer: %v", err)
	}
	return &heldConsumer{srv: srv, hooks: hooks, sub: sub}
}

// ackNext fetches C's next message and acks it, waiting for the server to take the ack.
func (c *heldConsumer) ackNext(t *testing.T) {
	t.Helper()
	msgs, err := c.sub.Fetch(1, natsgo.MaxWait(5*time.Second))
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch C's next message: %d messages, %v", len(msgs), err)
	}
	if err := msgs[0].AckSync(natsgo.AckWait(5 * time.Second)); err != nil {
		t.Fatalf("ack C's message: %v", err)
	}
}

// state is C's state as the file named file in its directory holds it, or nil while there is no
// such file or it does not decode (v2.10.29 writes o.dat in place, so a read can meet it half
// written).
func (c *heldConsumer) state(t *testing.T, file string) *natsserver.ConsumerState {
	t.Helper()
	buf, err := os.ReadFile(filepath.Join(c.srv.StoreDir(), "jetstream", "$G", "streams", "HOLD", "obs", "C", file))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read C's %s: %v", file, err)
	}
	state, err := natsserver.MeasureDecodeConsumerState(buf)
	if err != nil {
		return nil
	}
	return state
}

// awaitFloor waits for C's file to hold the ack floor floor.
func (c *heldConsumer) awaitFloor(t *testing.T, file string, floor uint64) {
	t.Helper()
	deadline := time.Now().Add(reachWithin)
	for {
		state := c.state(t, file)
		if state != nil && state.AckFloor.Stream == floor {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("C's %s did not reach ack floor %d within %s: %+v", file, floor, reachWithin, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// staysBelow watches C's file for held, and fails when it reaches the ack floor floor.
func (c *heldConsumer) staysBelow(t *testing.T, file string, floor uint64) {
	t.Helper()
	for end := time.Now().Add(held); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if state := c.state(t, file); state != nil && state.AckFloor.Stream >= floor {
			t.Fatalf("C's %s reached ack floor %d while the write was held: %+v", file, state.AckFloor.Stream, state)
		}
	}
}

// ackFloor takes the events carrying a consumer state whose ack floor is floor.
func ackFloor(floor uint64) testnats.Match {
	return testnats.Where(fmt.Sprintf("ack floor %d", floor), func(e testnats.Event) bool {
		return e.State != nil && e.State.AckFloor.Stream == floor
	})
}

func TestForcedOrderHoldsAStateWriteBeforeItsIO(t *testing.T) {
	c := newHeldConsumer(t)
	// Control: an unheld ack reaches o.dat, so the reading below sees a write land.
	c.ackNext(t)
	c.awaitFloor(t, "o.dat", 1)

	hold := c.hooks.Hold(testnats.WritePreIO, testnats.OnServer(c.srv), testnats.Consumer("C"), ackFloor(2))
	c.ackNext(t)
	t.Logf("held %s", hold.Reached(t))
	c.staysBelow(t, "o.dat", 2)
	hold.Release()
	c.awaitFloor(t, "o.dat", 2)
}

func TestForcedOrderHoldsAStateWriteBeforeItsRename(t *testing.T) {
	c := newHeldConsumer(t)
	c.ackNext(t)
	c.awaitFloor(t, "o.dat", 1)

	if ok, why := testnats.WritePreRename.Exists(); !ok {
		// nats-server v2.10.29 has no rename to hold before: it writes o.dat itself. Show that,
		// so a later version that brings the rename back fails here until its point is added.
		mark := c.hooks.Mark()
		c.ackNext(t)
		c.awaitFloor(t, "o.dat", 2)
		c.hooks.Await(t, testnats.Point("write.written"), testnats.OnServer(c.srv), testnats.Consumer("C"), ackFloor(2), testnats.After(mark))
		if state := c.state(t, "o.dat.tmp"); state != nil {
			t.Fatalf("nats-server %s left o.dat.tmp, holding %+v: it writes through a temporary file after all", natsserver.VERSION, state)
		}
		t.Logf("nats-server %s has no %s: %s", natsserver.VERSION, testnats.WritePreRename, why)
		return
	}
	hold := c.hooks.Hold(testnats.WritePreRename, testnats.OnServer(c.srv), testnats.Consumer("C"), ackFloor(2))
	c.ackNext(t)
	t.Logf("held %s", hold.Reached(t))
	if state := c.state(t, "o.dat.tmp"); state == nil || state.AckFloor.Stream != 2 {
		t.Fatalf("o.dat.tmp holds %+v while the rename is held, want the new state, ack floor 2", state)
	}
	c.staysBelow(t, "o.dat", 2)
	hold.Release()
	c.awaitFloor(t, "o.dat", 2)
}

func TestForcedOrderHoldsAFlushWithItsStateEncoded(t *testing.T) {
	c := newHeldConsumer(t)
	c.ackNext(t)
	c.awaitFloor(t, "o.dat", 1)

	hold := c.hooks.Hold(testnats.FlushEncoded, testnats.OnServer(c.srv), testnats.Consumer("C"), ackFloor(2))
	c.ackNext(t)
	t.Logf("held %s", hold.Reached(t))
	c.staysBelow(t, "o.dat", 2)
	hold.Release()
	c.awaitFloor(t, "o.dat", 2)
}

// answer runs request on a goroutine of its own and hands back its error.
func answer(request func() error) <-chan error {
	done := make(chan error, 1)
	go func() { done <- request() }()
	return done
}

// answered fails t unless done answers nil within reachWithin.
func answered(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(reachWithin):
		t.Fatalf("%s: no answer within %s", what, reachWithin)
	}
}

// unanswered fails t when done answers within held.
func unanswered(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("%s answered while held (%v)", what, err)
	case <-time.After(held):
	}
}

// heldStream is a lone server with the stream HOLD, and a JetStream client of it.
func heldStream(t *testing.T) (*testnats.Server, *testnats.Hooks, *natsgo.Conn, natsgo.JetStreamContext) {
	t.Helper()
	srv, uri := testnats.Start(t)
	hooks := testnats.Hook(t)
	conn := testnats.Connect(t, uri)
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(natsgo.MaxWait(reachWithin))
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: "HOLD", Subjects: []string{"hold.>"}}); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	return srv, hooks, conn, js
}

func TestForcedOrderHoldsAConsumerStoreBeforeItLoads(t *testing.T) {
	srv, hooks, _, js := heldStream(t)
	create := func(name string) <-chan error {
		return answer(func() error {
			_, err := js.AddConsumer("HOLD", &natsgo.ConsumerConfig{Durable: name, AckPolicy: natsgo.AckExplicitPolicy})
			return err
		})
	}
	answered(t, create("M"), "the unheld create of M")

	hold := hooks.Hold(testnats.StoreCreate, testnats.OnServer(srv), testnats.Consumer("N"))
	done := create("N")
	t.Logf("held %s", hold.Reached(t))
	unanswered(t, done, "the create of N")
	hold.Release()
	answered(t, done, "the create of N")
}

func TestForcedOrderHoldsAnAPIRequestBeforeItIsServed(t *testing.T) {
	srv, hooks, conn, _ := heldStream(t)
	const subject = "$JS.API.STREAM.INFO.HOLD"
	info := func() <-chan error {
		return answer(func() error {
			_, err := conn.Request(subject, nil, reachWithin)
			return err
		})
	}
	answered(t, info(), "the unheld stream info")

	// A lone server's clients are in $G; the request reaches the API in the system account, so
	// the account matched here is the one the request's own header names.
	hold := hooks.Hold(testnats.APIWorker, testnats.OnServer(srv), testnats.InAccount("$G"), testnats.Subject(subject))
	done := info()
	t.Logf("held %s", hold.Reached(t))
	unanswered(t, done, "the stream info")
	hold.Release()
	answered(t, done, "the stream info")
}

// stopWithin bounds the wait for a Stop of a server one of whose goroutines a hold parks. A stop
// that does not release the hold waits for that goroutine for ever, so the bound is what turns
// the hang into this test's failure.
const stopWithin = 20 * time.Second

// A server's shutdown releases the hold that parks one of its goroutines before it waits for its
// goroutines, so a Stop returns with an operation held: the held request then ends, answered or
// failed. Once the server is down, a hold on it parks again. Its control is the run without the
// release (forced-order.sh -drop server.shutdown): the Stop waits for the parked goroutine and the
// test fails at stopWithin.
func TestForcedOrderAStopReleasesTheHoldOnItsServer(t *testing.T) {
	srv, hooks, conn, _ := heldStream(t)
	const subject = "$JS.API.STREAM.INFO.HOLD"
	request := func(conn *natsgo.Conn) <-chan error {
		return answer(func() error {
			_, err := conn.Request(subject, nil, reachWithin)
			return err
		})
	}
	answered(t, request(conn), "the unheld stream info")

	hold := hooks.Hold(testnats.APIWorker, testnats.OnServer(srv), testnats.InAccount("$G"), testnats.Subject(subject))
	done := request(conn)
	t.Logf("held %s", hold.Reached(t))
	stopped := make(chan struct{})
	go func() {
		srv.Stop(t)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(stopWithin):
		t.Fatalf("the server's Stop did not return within %s while a hold parked one of its goroutines", stopWithin)
	}
	if !hold.Released() {
		t.Fatal("the Stop returned and the hold is not released")
	}
	hooks.Await(t, testnats.ServerDown, testnats.OnServer(srv))
	select {
	case err := <-done:
		t.Logf("the held request ended with the stop: %v", err)
	case <-time.After(reachWithin):
		t.Fatalf("the held request did not end within %s of the stop", reachWithin)
	}

	// The server is down, so the hook parks its goroutines again once it is started.
	srv.Start(t)
	again := testnats.Connect(t, srv.URL())
	t.Cleanup(again.Close)
	second := hooks.Hold(testnats.APIWorker, testnats.OnServer(srv), testnats.InAccount("$G"), testnats.Subject(subject),
		testnats.After(hooks.Mark()))
	done = request(again)
	t.Logf("held after the restart %s", second.Reached(t))
	unanswered(t, done, "the stream info after the restart")
	second.Release()
	answered(t, done, "the stream info after the restart")
}

func TestForcedOrderHoldsAPublishBeforeItIsMatched(t *testing.T) {
	srv, uri := testnats.Start(t)
	hooks := testnats.Hook(t)
	publisher, err := testnats.ConnectSeed(t, uri, "", natsgo.Name("publisher"))
	if err != nil {
		t.Fatalf("connect the publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	subscriber := testnats.Connect(t, uri)
	t.Cleanup(subscriber.Close)
	sub, err := subscriber.SubscribeSync("hold.pub")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := subscriber.Flush(); err != nil {
		t.Fatalf("flush the subscription: %v", err)
	}
	receive := func(within time.Duration) (string, error) {
		msg, err := sub.NextMsg(within)
		if err != nil {
			return "", err
		}
		return string(msg.Data), nil
	}
	if err := publisher.Publish("hold.pub", []byte("first")); err != nil {
		t.Fatalf("publish the unheld message: %v", err)
	}
	if got, err := receive(5 * time.Second); err != nil || got != "first" {
		t.Fatalf("the unheld publish delivered %q, %v; want first", got, err)
	}

	hold := hooks.Hold(testnats.ClientPublish, testnats.OnServer(srv), testnats.Subject("hold.pub"), testnats.ClientNamed("publisher"))
	// No flush: the publisher's read loop is the goroutine the hold parks, so a PING after the
	// publish would wait for the release too.
	if err := publisher.Publish("hold.pub", []byte("second")); err != nil {
		t.Fatalf("publish the held message: %v", err)
	}
	t.Logf("held %s", hold.Reached(t))
	if got, err := receive(held); err == nil {
		t.Fatalf("the held publish was delivered (%q) while held", got)
	}
	hold.Release()
	if got, err := receive(reachWithin); err != nil || got != "second" {
		t.Fatalf("the released publish delivered %q, %v; want second", got, err)
	}
}

func TestForcedOrderHoldsACommittedEntryBeforeItsLeaderStoresIt(t *testing.T) {
	cluster := testnats.StartCluster(t, 3)
	hooks := testnats.Hook(t)
	conn := testnats.Connect(t, cluster.URL())
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(natsgo.MaxWait(reachWithin))
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	if _, err := js.AddStream(&natsgo.StreamConfig{Name: "APPLY", Subjects: []string{"apply.>"}, Replicas: 3}); err != nil {
		t.Fatalf("create the stream: %v", err)
	}
	// Control: an unheld publish is acknowledged, once the stream's leader has stored it.
	if _, err := js.Publish("apply.first", nil); err != nil {
		t.Fatalf("the unheld publish: %v", err)
	}
	info, err := js.StreamInfo("APPLY")
	if err != nil || info.Cluster == nil || cluster.Member(info.Cluster.Leader) < 0 {
		t.Fatalf("the stream's leader: %+v, %v", info, err)
	}
	leader := cluster.Servers[cluster.Member(info.Cluster.Leader)]

	hold := hooks.Hold(testnats.StreamApply, testnats.OnServer(leader), testnats.Stream("APPLY"), testnats.Subject("apply.second"))
	ack, err := js.PublishAsync("apply.second", nil)
	if err != nil {
		t.Fatalf("publish the held message: %v", err)
	}
	reached := hold.Reached(t)
	t.Logf("held %s", reached)
	if reached.Server != leader.Name() || reached.Seq != 2 {
		t.Fatalf("held %s, want stream sequence 2 on the leader, %s", reached, leader.Name())
	}
	select {
	case got := <-ack.Ok():
		t.Fatalf("the publish was acknowledged (sequence %d) while its leader's apply was held", got.Sequence)
	case err := <-ack.Err():
		t.Fatalf("the held publish failed: %v", err)
	case <-time.After(held):
	}
	hold.Release()
	select {
	case got := <-ack.Ok():
		if got.Sequence != 2 {
			t.Fatalf("the released publish was acknowledged at sequence %d, want 2", got.Sequence)
		}
	case err := <-ack.Err():
		t.Fatalf("the released publish failed: %v", err)
	case <-time.After(reachWithin):
		t.Fatalf("the released publish was not acknowledged within %s", reachWithin)
	}
}
