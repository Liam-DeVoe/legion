//go:build odatrace

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsversion"
)

// The held-write collision at each creation site of the listener's own durable (the plan's first
// forced-order family), on the hooked server (scripts/forced-order.sh): the start's bind and the
// monitor's rebuild, each after the server removed the durable while its state write was held. In
// the fixed mode the durable is created again under its one name, at deliver policy all, and the
// late write gives it the removed one's state: after a restart it holds every message it was
// created to deliver as delivered and pending, and delivers none of them within the ack wait. In
// the fresh mode it gets a name of its own. Each row runs the raw same-name control first.

const (
	durableMessages = 30
	durableAcked    = 20
	// durableWindow is how long a row reads what the durable delivers after the restart: inside the
	// listener's 60 s ack wait (consumerAckWait), so a message the late state marked pending is not
	// delivered again within it.
	durableWindow = 8 * time.Second
	// durableSubject is a subject of the stream every listener durable filters on.
	durableSubject = "notifications.team.forced-durable"
)

// durableForcedRig is a lone hooked server, the listener's client in the build's naming mode, and a
// plain connection of the test's own.
type durableForcedRig struct {
	srv     *testnats.Server
	uri     string
	hooks   *testnats.Hooks
	client  *bus.Client
	conn    *natsgo.Conn
	durable string
}

func newDurableForcedRig(t *testing.T) *durableForcedRig {
	t.Helper()
	srv, uri := testnats.Start(t)
	if refusal := natsversion.Refusal(natsserver.VERSION); refusal != nil {
		// The listener opens its session consumers at its start, and ends it on this server.
		_, err := bus.ConnectSessionConsumers([]string{uri}, 0, nil)
		if err == nil || !strings.Contains(err.Error(), refusal.Error()) {
			t.Fatalf("nats-server %s is refused by natsversion (%v), but the session consumers connected: %v", natsserver.VERSION, refusal, err)
		}
		t.Skipf("the listener refuses nats-server %s at its start, so no durable of it exists to collide: %v", natsserver.VERSION, err)
	}
	conn := testnats.Connect(t, uri)
	t.Cleanup(conn.Close)
	client := forcedListenerClient(t, uri, conn)
	return &durableForcedRig{srv: srv, uri: uri, hooks: testnats.Hook(t), client: client, conn: conn, durable: bus.ListenerDurable("forced-" + id.New())}
}

func (r *durableForcedRig) js(t *testing.T) natsgo.JetStreamContext {
	t.Helper()
	js, err := r.conn.JetStream(natsgo.MaxWait(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// storedFloor is consumer name's ack floor as its o.dat holds it, or 0 while it holds none.
func (r *durableForcedRig) storedFloor(t *testing.T, name string) uint64 {
	t.Helper()
	buf, err := os.ReadFile(filepath.Join(r.srv.StoreDir(), "jetstream", "$G", "streams", bus.Stream, "obs", name, "o.dat"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s's o.dat: %v", name, err)
	}
	state, err := natsserver.MeasureDecodeConsumerState(buf)
	if err != nil {
		return 0
	}
	return state.AckFloor.Stream
}

func (r *durableForcedRig) awaitStoredFloor(t *testing.T, name string, floor uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for r.storedFloor(t, name) != floor {
		if time.Now().After(deadline) {
			t.Fatalf("%s's o.dat did not reach ack floor %d", name, floor)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gen is the generation of consumer name's newest store on the server.
func (r *durableForcedRig) gen(t *testing.T, name string) uint64 {
	t.Helper()
	events := r.hooks.Events(testnats.Point("store.loaded"), testnats.OnServer(r.srv), testnats.Consumer(name))
	if len(events) == 0 {
		t.Fatalf("the hooked server reported no store of %s", name)
	}
	return events[len(events)-1].Gen
}

// holdWrite arms the hold, before its I/O, of store gen's state write that carries ack floor floor.
func (r *durableForcedRig) holdWrite(name string, gen, floor uint64) *testnats.Hold {
	return r.hooks.Hold(testnats.WritePreIO, testnats.OnServer(r.srv), testnats.Consumer(name),
		testnats.Where(fmt.Sprintf("store %d's write of ack floor %d", gen, floor), func(e testnats.Event) bool {
			return e.Gen == gen && e.State != nil && e.State.AckFloor.Stream == floor
		}))
}

// releaseAfterReplacement waits for the first completed state write of a store newer than gen,
// the replacement's own, then releases hold and waits for store gen's held write to finish.
func (r *durableForcedRig) releaseAfterReplacement(t *testing.T, mark int, hold *testnats.Hold, gen uint64) {
	t.Helper()
	r.hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(r.srv), testnats.Stream(bus.Stream), testnats.After(mark),
		testnats.Where(fmt.Sprintf("a store newer than %d", gen), func(e testnats.Event) bool { return e.Gen > gen && e.Err == "" }))
	released := r.hooks.Mark()
	hold.Release()
	landed := r.hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(r.srv), testnats.After(released),
		testnats.Where(fmt.Sprintf("store %d", gen), func(e testnats.Event) bool { return e.Gen == gen }))
	t.Logf("the held write of store %d finished after its release: err %q", gen, landed.Err)
}

// restart stops the server and starts it again on its store, and dials the test's own connection
// again, which does not reconnect.
func (r *durableForcedRig) restart(t *testing.T) {
	t.Helper()
	r.srv.Stop(t)
	r.srv.Start(t)
	r.conn = testnats.Connect(t, r.uri)
	t.Cleanup(r.conn.Close)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := r.js(t).StreamInfo(bus.Stream); err == nil {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("the stream did not answer within 30 s of the restart: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// publish stores n messages on durableSubject and answers their stream sequences.
func (r *durableForcedRig) publish(t *testing.T, n int) []uint64 {
	t.Helper()
	seqs := make([]uint64, n)
	for i := range seqs {
		ack, err := r.client.JS().Publish(durableSubject, fmt.Appendf(nil, "m%d", i+1))
		if err != nil {
			t.Fatalf("publish message %d: %v", i+1, err)
		}
		seqs[i] = ack.Sequence
	}
	return seqs
}

// sameNameControl is every row's control: a raw push durable whose write of its 21st ack is held is
// deleted and created again under the same name, and after a restart it must deliver none of what
// it was created to deliver within the window (the measured M4): otherwise this server does not
// reproduce the collision, and no row's pass means anything.
func (r *durableForcedRig) sameNameControl(t *testing.T) {
	t.Helper()
	js := r.js(t)
	name := "listener-forced-control-" + id.New()
	seqs := r.publish(t, durableMessages)
	config := &natsgo.ConsumerConfig{Durable: name, DeliverSubject: natsgo.NewInbox(), FilterSubject: durableSubject,
		AckPolicy: natsgo.AckExplicitPolicy, AckWait: consumerAckWait, DeliverPolicy: natsgo.DeliverByStartSequencePolicy, OptStartSeq: seqs[0]}
	if _, err := js.AddConsumer(bus.Stream, config); err != nil {
		t.Fatalf("control: create %s: %v", name, err)
	}
	msgs := make(chan *natsgo.Msg, 2*durableMessages)
	sub, err := js.ChanSubscribe(durableSubject, msgs, natsgo.Bind(bus.Stream, name), natsgo.ManualAck())
	if err != nil {
		t.Fatalf("control: bind %s: %v", name, err)
	}
	var got []*natsgo.Msg
	for len(got) < durableMessages {
		select {
		case msg := <-msgs:
			got = append(got, msg)
		case <-time.After(10 * time.Second):
			t.Fatalf("control: %s delivered %d of %d", name, len(got), durableMessages)
		}
	}
	for _, msg := range got[:durableAcked] {
		if err := msg.AckSync(); err != nil {
			t.Fatal(err)
		}
	}
	r.awaitStoredFloor(t, name, seqs[durableAcked-1])
	gen := r.gen(t, name)
	hold := r.holdWrite(name, gen, seqs[durableAcked])
	if err := got[durableAcked].AckSync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("control: held %s", hold.Reached(t))
	_ = sub.Unsubscribe()
	mark := r.hooks.Mark()
	if err := js.DeleteConsumer(bus.Stream, name); err != nil {
		t.Fatalf("control: delete %s: %v", name, err)
	}
	config.DeliverSubject = natsgo.NewInbox()
	if _, err := js.AddConsumer(bus.Stream, config); err != nil {
		t.Fatalf("control: create %s again: %v", name, err)
	}
	r.releaseAfterReplacement(t, mark, hold, gen)
	r.restart(t)
	after := r.read(t, name, config.DeliverSubject)
	if slices.Contains(after, seqs[durableAcked+1]) {
		t.Fatalf("control: the same-name re-creation delivered %v after the restart: the held write did not reach it, so this server does not reproduce the collision", after)
	}
	t.Logf("control: the same-name re-creation delivered %v after the restart, none of %d..%d", after, seqs[durableAcked+1], seqs[durableMessages-1])
}

// read binds consumer name on the test's own connection for durableWindow and answers what it
// delivered, sorted and once each.
func (r *durableForcedRig) read(t *testing.T, name, deliverSubject string) []uint64 {
	t.Helper()
	msgs := make(chan *natsgo.Msg, 4*durableMessages)
	sub, err := r.conn.ChanSubscribe(deliverSubject, msgs)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	var seqs []uint64
	deadline := time.After(durableWindow)
	for {
		select {
		case msg := <-msgs:
			if meta, err := msg.Metadata(); err == nil {
				seqs = append(seqs, meta.Sequence.Stream)
			}
			_ = msg.Ack()
		case <-deadline:
			slices.Sort(seqs)
			return slices.Compact(seqs)
		}
	}
}

// heldDurable binds the rig's durable as the listener does, delivers durableMessages of the
// subject to it, acks the first durableAcked, and holds the write of the next ack. It answers the
// physical name, its store's generation, the hold, and the messages' sequences.
func (r *durableForcedRig) heldDurable(t *testing.T) (string, uint64, *testnats.Hold, []uint64) {
	t.Helper()
	msgs := make(chan *natsgo.Msg, 4*durableMessages)
	sub, err := startListenerSubscription(r.client, r.durable, func(msg *natsgo.Msg) { msgs <- msg })
	if err != nil {
		t.Fatalf("bind %s: %v", r.durable, err)
	}
	physical := forcedDurableName(t, r.client, r.durable)
	seqs := r.publish(t, durableMessages)
	var got []*natsgo.Msg
	for len(got) < durableMessages {
		select {
		case msg := <-msgs:
			if meta, err := msg.Metadata(); err == nil && meta.Sequence.Stream >= seqs[0] {
				got = append(got, msg)
			} else {
				_ = msg.Ack()
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s delivered %d of %d", physical, len(got), durableMessages)
		}
	}
	for _, msg := range got[:durableAcked] {
		if err := msg.AckSync(); err != nil {
			t.Fatal(err)
		}
	}
	r.awaitStoredFloor(t, physical, seqs[durableAcked-1])
	gen := r.gen(t, physical)
	hold := r.holdWrite(physical, gen, seqs[durableAcked])
	if err := got[durableAcked].AckSync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("held %s", hold.Reached(t))
	_ = sub.Unsubscribe()
	return physical, gen, hold, seqs
}

// requireListenerDelivered fails t unless got holds every message after the acked ones: a durable
// created again at deliver policy all, or at its record's start, delivers earlier ones again, which
// the listener's delivery handles, and missing one is what the row refuses.
func requireListenerDelivered(t *testing.T, got, seqs []uint64, what string) {
	t.Helper()
	want := seqs[durableAcked+1:]
	if len(want) == 0 {
		t.Fatalf("%s: the expected message set is empty, so the row judges nothing", what)
	}
	for _, seq := range want {
		if !slices.Contains(got, seq) {
			t.Fatalf("%s delivered %v within the window; want every one of %v", what, got, want)
		}
	}
}

// A listener's durable is a push consumer: it delivers the moment it is created, from the state it
// loaded then, so a row that let it run before a restart would read deliveries the late write never
// touched. Each row therefore holds the replacement's store before it loads any state
// (testnats.StoreCreate), lets the removed durable's held write land first, and only then lets the
// replacement load: of the same name, it loads the removed one's state, every message it was
// created to deliver marked delivered and pending, and delivers none of them within the ack wait;
// of a fresh name, it loads nothing.
func (r *durableForcedRig) createAfterTheHeldWrite(t *testing.T, gen uint64, hold *testnats.Hold, create func() error) {
	t.Helper()
	loading := r.hooks.Hold(testnats.StoreCreate, testnats.OnServer(r.srv), testnats.Stream(bus.Stream),
		testnats.Where(fmt.Sprintf("a store newer than %d", gen), func(e testnats.Event) bool { return e.Gen > gen }))
	done := make(chan error, 1)
	go func() { done <- create() }()
	t.Logf("held the replacement's store before it loads: %s", loading.Reached(t))
	released := r.hooks.Mark()
	hold.Release()
	landed := r.hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(r.srv), testnats.After(released),
		testnats.Where(fmt.Sprintf("store %d", gen), func(e testnats.Event) bool { return e.Gen == gen }))
	t.Logf("the removed durable's held write finished before the replacement loaded: err %q", landed.Err)
	loading.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the create of the replacement: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the create of the replacement did not answer within 30 s of its store's release")
	}
}

// The monitor's rebuild of a durable the server removed (rebuildListenerDependencies): its probe
// finds the durable gone, the bind creates it again, and the bound wait sees it bound. The removed
// durable's held write must not reach what the rebuild created.
func TestForcedOrderARebuildAfterTheDurablesRemovalUnderAHeldWriteDeliversEveryMessage(t *testing.T) {
	r := newDurableForcedRig(t)
	r.sameNameControl(t)
	physical, gen, hold, seqs := r.heldDurable(t)
	if err := r.js(t).DeleteConsumer(bus.Stream, physical); err != nil {
		t.Fatalf("remove %s: %v", physical, err)
	}
	received := make(chan *natsgo.Msg, 4*durableMessages)
	probe := func() error { return durableProbe(context.Background(), r.client, r.durable) }
	r.createAfterTheHeldWrite(t, gen, hold, func() error {
		return rebuildListenerDependencies(context.Background(), r.client, nil, probe, r.durable, func(msg *natsgo.Msg) { received <- msg }, nil)
	})
	got := collect(t, received)
	requireListenerDelivered(t, got, seqs, "the durable the rebuild created")
}

// The start's bind (startListenerSubscription, through bindListenerDurable) of a durable the server
// removed while the listener was away: the removed durable's held write must not reach the one the
// start creates.
func TestForcedOrderAStartAfterTheDurablesRemovalUnderAHeldWriteDeliversEveryMessage(t *testing.T) {
	r := newDurableForcedRig(t)
	r.sameNameControl(t)
	physical, gen, hold, seqs := r.heldDurable(t)
	if err := r.js(t).DeleteConsumer(bus.Stream, physical); err != nil {
		t.Fatalf("remove %s: %v", physical, err)
	}
	received := make(chan *natsgo.Msg, 4*durableMessages)
	r.createAfterTheHeldWrite(t, gen, hold, func() error {
		_, err := startListenerSubscription(r.client, r.durable, func(msg *natsgo.Msg) { received <- msg })
		return err
	})
	got := collect(t, received)
	requireListenerDelivered(t, got, seqs, "the durable the start created")
}

// collect reads what the listener's own subscription delivers for durableWindow, acking each, and
// answers the stream sequences, sorted and once each.
func collect(t *testing.T, received chan *natsgo.Msg) []uint64 {
	t.Helper()
	var seqs []uint64
	deadline := time.After(durableWindow)
	for {
		select {
		case msg := <-received:
			if meta, err := msg.Metadata(); err == nil {
				seqs = append(seqs, meta.Sequence.Stream)
			}
			_ = msg.Ack()
		case <-deadline:
			slices.Sort(seqs)
			return slices.Compact(seqs)
		}
	}
}
