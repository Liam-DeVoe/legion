package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/cipher/natscallout"
	"github.com/sjawhar/envoy/internal/testnats"
)

// The listener beside Dispatch holds Dispatch's credential, so its grant is exactly the one the
// hub's auth service decides for Dispatch (natsgrant.Dispatch): its own durable's fixed name, the
// selection bucket's lookup, its leader read and the write of its own record's key, and each fresh
// generation of its durable on a connection named for it (listener-intake/<machine id>/<generation>).
// In the fresh mode, after the cutover's conversion, it starts on that grant alone: it binds a fresh
// generation of its durable and is delivered the stream's messages on it.
//
// The control is the same row on a grant without the bucket's lookup and leader read, which the
// start refuses: run it by removing the two entries from natsgrant's listenerSelection (an -overlay
// of natsgrant.go); the row then fails at its start.
func TestTheListenerBesideDispatchStartsOnDispatchsGrantInTheFreshMode(t *testing.T) {
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
		Services:       []natscallout.ServiceEntry{{Kind: natscallout.KindDispatch, MachineID: machine, Account: account, AllProjects: true}},
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

	// The operator's create-bucket and the cutover's conversion: the bucket, its sentinel done.
	admin, err := testnats.ConnectSeed(t, url, adminSeed)
	if err != nil {
		t.Fatalf("connect the admin: %v", err)
	}
	t.Cleanup(admin.Close)
	adminJS, err := jetstream.New(admin)
	if err != nil {
		t.Fatalf("the admin's JetStream: %v", err)
	}
	if _, err := adminJS.CreateKeyValue(t.Context(), consumernames.SessionBucketConfig(1)); err != nil {
		t.Fatalf("create the selection bucket: %v", err)
	}
	records := consumernames.NewKVStore(consumernames.SessionBucket, consumernames.ConnRequester{Conn: admin})
	if _, err := records.WriteSentinel(t.Context(), consumernames.StoreState{Cutover: consumernames.CutoverDone}, 0); err != nil {
		t.Fatalf("write the sentinel: %v", err)
	}

	token := write("dispatch.token", "dispatch-token")
	client, err := bus.ConnectOwningStream([]string{url},
		bus.WithReplicas(1),
		bus.WithName("envoy-listener"),
		bus.WithConsumerNames(consumernames.ModeFresh),
		bus.WithEnvironment(func(key string) (string, bool) {
			if key == bus.ServiceTokenFileEnvVar {
				return token, true
			}
			return "", false
		}))
	if err != nil {
		t.Fatalf("connect the listener as Dispatch: %v", err)
	}
	t.Cleanup(client.Close)

	durable := bus.ListenerDurable(machine)
	delivered := make(chan *natsgo.Msg, 4)
	sub, err := startListenerSubscription(client, durable, func(msg *natsgo.Msg) {
		_ = msg.Ack()
		delivered <- msg
	})
	if err != nil {
		t.Fatalf("the listener beside Dispatch did not start its durable: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := awaitDurableBound(t.Context(), client, durable, 100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatalf("the durable is not bound: %v", err)
	}
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
	case <-time.After(15 * time.Second):
		t.Fatalf("the fresh generation %s delivered nothing", record.Current)
	}
}
