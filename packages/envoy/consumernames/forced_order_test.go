//go:build odatrace

package consumernames

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/internal/testnats"
)

// The held-write collision through the protocol itself, on the hooked server
// (scripts/forced-order.sh): a family of this file's own (jsFamily, KVStore) whose consumer's state
// write is held while Names replaces it, or deletes it and creates it again. The package has no
// build before this one to run the rows against, so each row's control is that build's order run
// raw on the same server first: a delete and a create of one name under the same held write, which
// must lose the acknowledged state's messages.

const (
	protocolMessages = 30
	protocolAcked    = 20
	protocolWindow   = 8 * time.Second
)

// protocolRig is a lone hooked server with testStream and the selection bucket.
type protocolRig struct {
	srv   *testnats.Server
	uri   string
	hooks *testnats.Hooks
	live  *liveStore
}

func newProtocolRig(t *testing.T) *protocolRig {
	t.Helper()
	srv, uri := testnats.Start(t)
	// No boundary: a boundary would start each first create no later than it, before the row's own
	// messages.
	live := newLiveStore(t, uri, &StoreState{Cutover: CutoverDone})
	return &protocolRig{srv: srv, uri: uri, hooks: testnats.Hook(t), live: live}
}

func (r *protocolRig) publish(t *testing.T, subject string) []uint64 {
	t.Helper()
	seqs := make([]uint64, protocolMessages)
	for i := range seqs {
		ack, err := r.live.js.Publish(context.Background(), subject, fmt.Appendf(nil, "m%d", i+1))
		if err != nil {
			t.Fatal(err)
		}
		seqs[i] = ack.Sequence
	}
	return seqs
}

func (r *protocolRig) storedFloor(t *testing.T, name string) uint64 {
	t.Helper()
	buf, err := os.ReadFile(filepath.Join(r.srv.StoreDir(), "jetstream", "$G", "streams", testStream, "obs", name, "o.dat"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	state, err := natsserver.MeasureDecodeConsumerState(buf)
	if err != nil {
		return 0
	}
	return state.AckFloor.Stream
}

// heldConsumer fetches every message of consumer name, acks the first protocolAcked, waits for its
// o.dat to hold that floor, and holds, before its I/O, the write of the next ack. It answers the
// store's generation and the hold.
func (r *protocolRig) heldConsumer(t *testing.T, name string, seqs []uint64) (uint64, *testnats.Hold) {
	t.Helper()
	ctx := context.Background()
	consumer, err := r.live.js.Consumer(ctx, testStream, name)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := consumer.Fetch(protocolMessages, jetstream.FetchMaxWait(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []jetstream.Msg
	for msg := range batch.Messages() {
		msgs = append(msgs, msg)
	}
	if len(msgs) != protocolMessages {
		t.Fatalf("%s delivered %d of %d", name, len(msgs), protocolMessages)
	}
	for _, msg := range msgs[:protocolAcked] {
		if err := msg.DoubleAck(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for r.storedFloor(t, name) != seqs[protocolAcked-1] {
		if time.Now().After(deadline) {
			t.Fatalf("%s's o.dat did not reach ack floor %d", name, seqs[protocolAcked-1])
		}
		time.Sleep(20 * time.Millisecond)
	}
	events := r.hooks.Events(testnats.Point("store.loaded"), testnats.OnServer(r.srv), testnats.Consumer(name))
	if len(events) == 0 {
		t.Fatalf("the hooked server reported no store of %s", name)
	}
	gen := events[len(events)-1].Gen
	hold := r.hooks.Hold(testnats.WritePreIO, testnats.OnServer(r.srv), testnats.Consumer(name),
		testnats.Where(fmt.Sprintf("store %d's write of ack floor %d", gen, seqs[protocolAcked]), func(e testnats.Event) bool {
			return e.Gen == gen && e.State != nil && e.State.AckFloor.Stream == seqs[protocolAcked]
		}))
	if err := msgs[protocolAcked].DoubleAck(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("held %s", hold.Reached(t))
	return gen, hold
}

// releaseAndRestart waits for the replacement's own first state write (a store newer than gen),
// releases hold, waits for the held write to finish, and restarts the server on its store.
func (r *protocolRig) releaseAndRestart(t *testing.T, mark int, hold *testnats.Hold, gen uint64) {
	t.Helper()
	r.hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(r.srv), testnats.Stream(testStream), testnats.After(mark),
		testnats.Where(fmt.Sprintf("a store newer than %d", gen), func(e testnats.Event) bool { return e.Gen > gen && e.Err == "" }))
	released := r.hooks.Mark()
	hold.Release()
	r.hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(r.srv), testnats.After(released),
		testnats.Where(fmt.Sprintf("store %d", gen), func(e testnats.Event) bool { return e.Gen == gen }))
	r.srv.Stop(t)
	r.srv.Start(t)
}

// deliveredAfterRestart reads consumer name on a fresh connection for protocolWindow, acking each,
// and answers the stream sequences, sorted and once each.
func (r *protocolRig) deliveredAfterRestart(t *testing.T, name string) []uint64 {
	t.Helper()
	conn := testnats.Connect(t, r.uri)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocolWindow)
	defer cancel()
	var seqs []uint64
	for ctx.Err() == nil {
		consumer, err := js.Consumer(ctx, testStream, name)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		batch, err := consumer.Fetch(protocolMessages, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			continue
		}
		for msg := range batch.Messages() {
			if meta, err := msg.Metadata(); err == nil {
				seqs = append(seqs, meta.Sequence.Stream)
			}
			_ = msg.Ack()
		}
	}
	slices.Sort(seqs)
	return slices.Compact(seqs)
}

// sameNameControl runs the order of the build before this package raw: a delete and a create of one
// name at the sequence after the floor, under the held write. After the restart the consumer must
// deliver none of what it was created to deliver within the window.
func (r *protocolRig) sameNameControl(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	subject := "names.control"
	seqs := r.publish(t, subject)
	name := "control"
	config := jetstream.ConsumerConfig{Durable: name, AckPolicy: jetstream.AckExplicitPolicy, AckWait: time.Minute, FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: seqs[0]}
	if _, err := r.live.js.CreateConsumer(ctx, testStream, config); err != nil {
		t.Fatal(err)
	}
	gen, hold := r.heldConsumer(t, name, seqs)
	mark := r.hooks.Mark()
	if err := r.live.js.DeleteConsumer(ctx, testStream, name); err != nil {
		t.Fatal(err)
	}
	config.OptStartSeq = seqs[protocolAcked+1]
	if _, err := r.live.js.CreateConsumer(ctx, testStream, config); err != nil {
		t.Fatal(err)
	}
	r.releaseAndRestart(t, mark, hold, gen)
	got := r.deliveredAfterRestart(t, name)
	if slices.Contains(got, seqs[protocolAcked+1]) {
		t.Fatalf("control: the same-name re-creation delivered %v after the restart: the held write did not reach it, so this server does not reproduce the collision", got)
	}
	t.Logf("control: the same-name re-creation delivered %v after the restart, none of %d..%d", got, seqs[protocolAcked+1], seqs[protocolMessages-1])
	// The restart closed the store's connection, which does not reconnect: the row goes on with a new one.
	r.live = reconnectLiveStore(t, r.uri)
}

// reconnectLiveStore is a liveStore on a new connection to a server that already holds the stream
// and the bucket.
func reconnectLiveStore(t *testing.T, uri string) *liveStore {
	t.Helper()
	conn := testnats.Connect(t, uri)
	t.Cleanup(conn.Close)
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	return &liveStore{conn: conn, js: js, store: NewKVStore(SessionBucket, ConnRequester{conn}), family: jsFamily{js}}
}

func requireEvery(t *testing.T, got, want []uint64, what string) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: the expected message set is empty, so the row judges nothing", what)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s delivered %v after the restart; want every message it was created to deliver, %v", what, got, want)
	}
}

// Replace through the protocol under the predecessor's held write: the replacement, a name never
// made before, delivers every message after the predecessor's floor.
func TestForcedOrderAReplacementThroughTheProtocolUnderAHeldWriteDeliversEveryMessage(t *testing.T) {
	r := newProtocolRig(t)
	r.sameNameControl(t)
	ctx := context.Background()
	seqs := r.publish(t, "names.replace")
	names := r.live.names("p1")
	sel, _, err := names.Ensure(ctx, "ses-replace", CreateRequest{Create: r.live.family.create("ses-replace"), First: first(seqs[0])})
	if err != nil {
		t.Fatal(err)
	}
	gen, hold := r.heldConsumer(t, sel.Record.Current, seqs)
	sel, err = names.Select(ctx, "ses-replace", nil)
	if err != nil {
		t.Fatal(err)
	}
	mark := r.hooks.Mark()
	replaced, err := names.Replace(ctx, sel, seqs[protocolAcked+1], r.live.family.create("ses-replace"))
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	r.releaseAndRestart(t, mark, hold, gen)
	requireEvery(t, r.deliveredAfterRestart(t, replaced.Record.Current), seqs[protocolAcked+1:], "the replacement")
}

// Delete through the protocol, then Ensure, under the deleted consumer's held write: the consumer
// created again at the start the delete saved delivers every message after the floor.
func TestForcedOrderADeleteThenEnsureThroughTheProtocolUnderAHeldWriteDeliversEveryMessage(t *testing.T) {
	r := newProtocolRig(t)
	r.sameNameControl(t)
	ctx := context.Background()
	seqs := r.publish(t, "names.delete")
	names := r.live.names("p1")
	sel, _, err := names.Ensure(ctx, "ses-delete", CreateRequest{Create: r.live.family.create("ses-delete"), First: first(seqs[0])})
	if err != nil {
		t.Fatal(err)
	}
	gen, hold := r.heldConsumer(t, sel.Record.Current, seqs)
	floor, err := names.Delete(ctx, "ses-delete", nil)
	if err != nil || floor != seqs[protocolAcked] {
		t.Fatalf("Delete = %d, %v; want the floor %d", floor, err, seqs[protocolAcked])
	}
	mark := r.hooks.Mark()
	again, _, err := names.Ensure(ctx, "ses-delete", CreateRequest{Create: r.live.family.create("ses-delete"), First: first(seqs[0])})
	if err != nil {
		t.Fatal(err)
	}
	r.releaseAndRestart(t, mark, hold, gen)
	requireEvery(t, r.deliveredAfterRestart(t, again.Record.Current), seqs[protocolAcked+1:], "the consumer created again")
}
