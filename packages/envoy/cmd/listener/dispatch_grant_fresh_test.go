// These rows run the hub's auth service (natscallout), whose package reaches the whole cipher
// tree; the server-change soak (serverchange) neither needs them nor should rebuild on every
// change to that tree, so it leaves them out. Every untagged go test of the package runs them.

//go:build !serverchange

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cipher/config"
	"github.com/sjawhar/envoy/internal/cipher/natscallout"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The listener beside Dispatch holds Dispatch's credential, so its grant is exactly the one the
// hub's auth service decides for Dispatch (natsgrant.Dispatch): its own durable's fixed name, the
// selection bucket's leader read and the write of its own record's key, and each fresh generation
// of its durable on a connection named for it (listener-intake/<machine id>/<generation>).
// In the fresh mode, after the cutover's conversion, it starts on that grant alone: it binds a fresh
// generation of its durable, its subscription is live (what /healthz reads), and it is delivered the
// stream's messages on it and acknowledges them.
//
// Two controls, each an -overlay that makes the row fail: the grant without the bucket's leader
// read (natsgrant's listenerSelection), which the start refuses; and the durable bound on the
// listener's own connection rather than its generation's (startListenerSubscription), whose
// acknowledgement Dispatch's grant refuses.
func TestTheListenerBesideDispatchStartsOnDispatchsGrantInTheFreshMode(t *testing.T) {
	hub := startDispatchGrantHub(t)
	client, err := bus.ConnectOwningStream([]string{hub.url},
		bus.WithReplicas(1),
		bus.WithName("envoy-listener"),
		bus.WithConsumerNames(consumernames.ModeFresh),
		bus.WithEnvironment(hub.environment))
	if err != nil {
		t.Fatalf("connect the listener as Dispatch: %v", err)
	}
	t.Cleanup(client.Close)

	durable := bus.ListenerDurable(hub.machine)
	delivered := make(chan *natsgo.Msg, 4)
	acked := make(chan error, 4)
	sub, err := startListenerSubscription(client, durable, func(msg *natsgo.Msg) {
		acked <- msg.AckSync()
		delivered <- msg
	})
	if err != nil {
		t.Fatalf("the listener beside Dispatch did not start its durable: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := awaitDurableBound(t.Context(), client, durable, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatalf("the durable is not bound: %v", err)
	}
	if !durableSubOK(client, durable) {
		t.Fatal("the durable's subscription is not live, which /healthz reads")
	}
	records, admin := hub.records, hub.admin
	record, _, err := records.Read(t.Context(), consumernames.DurableKey(durable))
	if err != nil {
		t.Fatalf("read the durable's record: %v", err)
	}
	if !strings.HasPrefix(record.Current, durable+"~") {
		t.Fatalf("the durable's record selects %q, want a fresh generation of %s", record.Current, durable)
	}

	subject := bus.NotificationTopicPrefix + "github.example.issue"
	publisher, err := admin.JetStream()
	if err != nil {
		t.Fatalf("the admin's publisher: %v", err)
	}
	if _, err := publisher.Publish(subject, []byte(`{}`)); err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}
	select {
	case msg := <-delivered:
		if msg.Subject != subject {
			t.Fatalf("delivered %s, want %s", msg.Subject, subject)
		}
		if err := <-acked; err != nil {
			t.Fatalf("the acknowledgement of %s on the generation's connection: %v", subject, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the fresh generation %s delivered nothing", record.Current)
	}
}

// The window's dependency on this grant, run whole: the listener beside Dispatch, in the fixed mode,
// on exactly Dispatch's grant as the hub's auth service decides it (natsgrant.Dispatch), over a
// selection bucket whose sentinel says the cutover is done, as the convert leaves it. Its start
// reads that sentinel through the bucket's leader read, which Dispatch's grant holds, and refuses
// (bus.ErrConvertedStore, naming ENVOY_CONSUMER_NAMES); its durable is then never created,
// corrected or bound (bus.ErrMarkerUnread), so listener-<machine id> stays absent. The control, an
// -overlay that takes the bucket's leader read out of Dispatch's grant (natsgrant's
// listenerSelection), fails the row: the refused read counts as no marker, the fixed start goes on,
// and the start recreates listener-<machine id> over the converted bucket.
func TestTheListenerBesideDispatchRefusesAConvertedBucketInTheFixedMode(t *testing.T) {
	hub := startDispatchGrantHub(t)
	client, err := bus.ConnectOwningStream([]string{hub.url},
		bus.WithReplicas(1),
		bus.WithName("envoy-listener"),
		bus.WithEnvironment(hub.environment))
	if err != nil {
		t.Fatalf("connect the listener as Dispatch: %v", err)
	}
	t.Cleanup(client.Close)
	if mode := client.ConsumerNames(); mode != consumernames.ModeFixed {
		t.Fatalf("the listener runs %s names, want the default fixed mode", mode)
	}
	durable := bus.ListenerDurable(hub.machine)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	logger := logging.NewWithWriter("test", io.Discard)
	started := guardFixedNaming(ctx, false, []gatedConnection{{name: "durable", connection: client}}, logger, 100*time.Millisecond, nil)
	if !errors.Is(started, bus.ErrConvertedStore) {
		sub, err := startListenerSubscription(client, durable, func(*natsgo.Msg) {})
		if err == nil {
			_ = sub.Unsubscribe()
		}
		t.Fatalf("the fixed start over a converted bucket on Dispatch's grant = %v, want bus.ErrConvertedStore; its durable's start then answered %v", started, err)
	}
	if _, err := startListenerSubscription(client, durable, func(*natsgo.Msg) {}); !errors.Is(err, bus.ErrMarkerUnread) {
		t.Fatalf("the durable's start after the refused start = %v, want bus.ErrMarkerUnread", err)
	}
	js, err := hub.admin.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.ConsumerInfo(bus.Stream, durable); !errors.Is(err, natsgo.ErrConsumerNotFound) {
		t.Fatalf("%s after the refused start: %v, want no such consumer", durable, err)
	}
}

// dispatchGrantHub is a hub whose auth service grants Dispatch's service account Dispatch's
// grant (natsgrant.Dispatch) for machine, with the selection bucket the operator creates and its
// sentinel done, as the cutover's conversion leaves it, and an admin user beside it.
type dispatchGrantHub struct {
	url, machine string
	admin        *natsgo.Conn
	records      *consumernames.KVStore
	// environment is the environment a client connecting as Dispatch reads its token file from.
	environment func(string) (string, bool)
}

func startDispatchGrantHub(t *testing.T) *dispatchGrantHub {
	t.Helper()
	const machine = "example-dispatch"
	const account = "system:serviceaccount:dispatch:dispatch"
	keys := testnats.NewCalloutKeys(t)
	adminSeed, adminKey := testnats.User(t)
	_, url := testnats.StartWithCalloutServer(t, keys, testnats.StaticUser{NKey: adminKey, Publish: []string{">"}, Subscribe: []string{">"}})
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	service, err := natscallout.Start(context.Background(), natscallout.Settings{
		URLs:           []string{url},
		SeedFile:       write("service.seed", keys.ServiceSeed),
		IssuerSeedFile: write("issuer.seed", keys.IssuerSeed),
		XKeySeedFile:   write("xkey.seed", keys.XKeySeed),
		Services:       []config.NATSService{{Kind: config.NATSDispatch, MachineID: machine, Account: account, AllProjects: true}},
	}, natscallout.Deps{ServiceToken: func(_ context.Context, raw string) (string, bool, time.Time, time.Time, error) {
		if raw != "dispatch-token" {
			return "", false, time.Time{}, time.Time{}, errors.New("the token did not verify")
		}
		now := time.Now()
		return account, false, now, now.Add(time.Hour), nil
	}})
	if err != nil {
		t.Fatalf("start the auth service: %v", err)
	}
	t.Cleanup(service.Close)
	ready, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.Ready(ready); err != nil {
		t.Fatalf("the auth service never subscribed: %v", err)
	}
	admin, err := testnats.ConnectSeed(t, url, adminSeed)
	if err != nil {
		t.Fatalf("connect the admin: %v", err)
	}
	t.Cleanup(admin.Close)
	// The create's bound is the hub's own, not nats.go's 5 s default for a context with no deadline
	// (jetstream's DefaultTimeout), which one server's stream create overran at a box load of 2,500.
	create, cancelCreate := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelCreate()
	records, err := consumernames.CreateSessionBucket(create, admin, 1, consumernames.StoreState{Cutover: consumernames.CutoverDone})
	if err != nil {
		t.Fatalf("create the selection bucket: %v", err)
	}
	token := write("dispatch.token", "dispatch-token")
	return &dispatchGrantHub{url: url, machine: machine, admin: admin, records: records,
		environment: func(key string) (string, bool) {
			if key == bus.ServiceTokenFileEnvVar {
				return token, true
			}
			return "", false
		}}
}

// connect opens a connection as Dispatch, named name, and fails t unless the hub admits it.
func (h *dispatchGrantHub) connect(t *testing.T, name string) *natsgo.Conn {
	t.Helper()
	client, err := bus.Connect([]string{h.url}, bus.WithName(name), bus.WithEnvironment(h.environment))
	if err != nil {
		t.Fatalf("connect %s as Dispatch: %v", name, err)
	}
	t.Cleanup(client.Close)
	return client.Connection()
}

// Two writers on exactly Dispatch's grant -- two processes of the listener beside Dispatch, as a
// standby pod beside the active one makes possible -- race on the listener's own selection record:
// each round both write at one revision and the store takes one, refusing the other as a conflict.
// The grant has no direct get of the bucket, so the store reads only from the stream's leader and
// writes only on the key's expected last sequence. The control is nats.go's KV on the same grant,
// which the store does not use: its handle's lookup of the bucket asks the stream's info, which
// Dispatch's grant refuses (bus.ErrPublishDenied), so it cannot even bind the bucket, where the
// admin's full grant binds it and kvs.Create recreates a deleted key.
func TestTwoWritersOnDispatchsGrantRaceOnTheListenersRecord(t *testing.T) {
	hub := startDispatchGrantHub(t)
	ctx := t.Context()
	key := consumernames.DurableKey(bus.ListenerDurable(hub.machine))
	first := consumernames.NewKVStore(consumernames.ConnRequester{Conn: hub.connect(t, "dispatch")})
	second := consumernames.NewKVStore(consumernames.ConnRequester{Conn: hub.connect(t, "dispatch")})
	if _, err := first.Write(ctx, key, consumernames.Record{Start: 1}, 0); err != nil {
		t.Fatalf("the first write on Dispatch's grant: %v", err)
	}
	for round := range 20 {
		_, revision, err := first.Read(ctx, key)
		if err != nil {
			t.Fatalf("round %d: the leader read on Dispatch's grant: %v", round, err)
		}
		results := make(chan error, 2)
		for _, store := range []*consumernames.KVStore{first, second} {
			go func() {
				_, err := store.Write(ctx, key, consumernames.Record{Start: uint64(round + 2)}, revision)
				results <- err
			}()
		}
		var wrote, conflicts int
		for range 2 {
			switch err := <-results; {
			case err == nil:
				wrote++
			case errors.Is(err, consumernames.ErrConflict):
				conflicts++
			default:
				t.Fatalf("round %d: a racing write answered %v, want a write or ErrConflict", round, err)
			}
		}
		if wrote != 1 || conflicts != 1 {
			t.Fatalf("round %d: %d writes and %d conflicts, want one of each", round, wrote, conflicts)
		}
	}
	// Another machine's key is refused on the same grant.
	other := consumernames.DurableKey(bus.ListenerDurable("m2"))
	if _, err := first.Write(ctx, other, consumernames.Record{Start: 1}, 0); err == nil || errors.Is(err, consumernames.ErrConflict) {
		t.Fatalf("a write of another machine's key on Dispatch's grant = %v, want it refused", err)
	}

	// Control: nats.go's KV over a deleted key, on the admin's full grant and on Dispatch's, each
	// writing a well-formed record of the key, so only the grant can refuse it.
	record, err := json.Marshal(consumernames.Record{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	adminJS, err := jetstream.New(hub.admin)
	if err != nil {
		t.Fatal(err)
	}
	adminKV, err := adminJS.KeyValue(ctx, consumernames.SessionBucket)
	if err != nil {
		t.Fatal(err)
	}
	if err := adminKV.Delete(ctx, key); err != nil {
		t.Fatalf("delete the record: %v", err)
	}
	if _, err := adminKV.Create(ctx, key, record); err != nil {
		t.Fatalf("control: kvs.Create over a deleted key on a full grant = %v, want the key recreated", err)
	}
	if err := adminKV.Delete(ctx, key); err != nil {
		t.Fatalf("delete the record again: %v", err)
	}
	dispatch, err := bus.Connect([]string{hub.url}, bus.WithName("dispatch"), bus.WithEnvironment(hub.environment))
	if err != nil {
		t.Fatalf("connect as Dispatch: %v", err)
	}
	t.Cleanup(dispatch.Close)
	info := "$JS.API.STREAM.INFO.KV_" + consumernames.SessionBucket
	if _, err := dispatch.RequestCore(ctx, info, dispatch.Connection().NewInbox(), nil); !errors.Is(err, bus.ErrPublishDenied) {
		t.Fatalf("the bucket's stream info on Dispatch's grant = %v, want bus.ErrPublishDenied", err)
	}
	js, err := jetstream.New(dispatch.Connection())
	if err != nil {
		t.Fatal(err)
	}
	bindCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if kv, err := js.KeyValue(bindCtx, consumernames.SessionBucket); err == nil {
		if _, err := kv.Create(bindCtx, key, record); err == nil {
			t.Fatal("nats.go's KV recreated a deleted key on Dispatch's grant")
		}
		t.Fatal("nats.go's KV bound the bucket on Dispatch's grant: its lookup's stream info was not refused")
	}
	if _, _, err := first.Read(ctx, key); !errors.Is(err, consumernames.ErrRecordDeleted) {
		t.Fatalf("the store's read of the deleted record on Dispatch's grant = %v, want ErrRecordDeleted", err)
	}
}
