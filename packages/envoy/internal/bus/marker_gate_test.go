package bus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/testnats"
)

// A fixed-mode process that started over a store the cutover had not converted, and was cut off
// while something changed the selection bucket's sentinel, makes no consumer call at its reconnect
// until a read of the marker on the new connection says the store is still not converted: a
// sentinel it cannot read (the server's STREAM.INFO answer stored over it through a request's
// reply subject, the deep review's reproduction) keeps every call refused with ErrMarkerUnread and
// today's fixed name uncreated, and a done marker stops the process and creates nothing. The
// control is today's reconnect check (the hook alone, gating nothing), under which the same
// reconnect over the unreadable sentinel creates s-<id>; the liveness row is a sentinel that still
// says not converted, which opens the gate again on the new connection.
func TestAFixedModeReconnectMakesNoConsumerCallUntilItReadsTheMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		// gate installs the marker gate; false runs today's reconnect check instead.
		gate bool
		// cutOff is what changes the sentinel while the process is cut off.
		cutOff func(t *testing.T, conn *natsgo.Conn)
		// created says whether the reconnect ends with today's fixed name on the server.
		created   bool
		converted bool
	}{
		{name: "control: today's check, the sentinel replaced by a STREAM.INFO answer", cutOff: storeStreamInfoOverTheSentinel, created: true},
		{name: "the sentinel replaced by a STREAM.INFO answer", gate: true, cutOff: storeStreamInfoOverTheSentinel},
		{name: "the cutover's done marker", gate: true, cutOff: markTheCutoverDone, converted: true},
		{name: "liveness: a sentinel that still says not converted", gate: true, cutOff: func(*testing.T, *natsgo.Conn) {}, created: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, uri := testnats.Start(t)
			ctx := context.Background()
			admin := testnats.Connect(t, uri)
			t.Cleanup(admin.Close)
			if _, err := consumernames.CreateSessionBucket(ctx, admin, 1, consumernames.StoreState{Boundary: 1}); err != nil {
				t.Fatal(err)
			}
			owner, err := ConnectOwningStream([]string{uri})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(owner.Close)
			consumers, err := ConnectSessionConsumers([]string{uri}, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(consumers.Close)
			converted := make(chan error, 16)
			if tc.gate {
				consumers.GateOnMarker(func(err error) { converted <- err })
			} else {
				todaysReconnectCheck(consumers, func(err error) { converted <- err })
			}
			if err := consumers.ReadMarker(ctx); err != nil {
				t.Fatalf("the start's read of a store the cutover has not converted = %v", err)
			}
			const before = "ses_before_the_cut"
			if _, err := consumers.Ensure(ctx, before, []string{contracts.LegacyAgentSubject(before)}, 0); err != nil {
				t.Fatalf("a registration after the start's read: %v", err)
			}

			tc.cutOff(t, admin)
			srv.Stop(t)
			srv.Start(t)
			awaitConnected(t, consumers.client)

			const probe = "ses_after_the_cut"
			subjects := []string{contracts.LegacyAgentSubject(probe)}
			var refusedUnread bool
			for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
				callCtx, cancel := context.WithTimeout(ctx, time.Second)
				start, err := consumers.Ensure(callCtx, probe, subjects, 0)
				cancel()
				refusedUnread = refusedUnread || errors.Is(err, ErrMarkerUnread)
				if err == nil && start.Created {
					break
				}
			}
			check := testnats.Connect(t, uri)
			t.Cleanup(check.Close)
			js, err := jetstream.New(check)
			if err != nil {
				t.Fatal(err)
			}
			_, infoErr := js.Consumer(ctx, Stream, sessionConsumerPrefix+probe)
			if held := infoErr == nil; held != tc.created {
				t.Fatalf("after the reconnect the server holds %s%s: %t (%v); want %t", sessionConsumerPrefix, probe, held, infoErr, tc.created)
			}
			if tc.gate && !tc.created && !refusedUnread {
				t.Fatal("no call after the reconnect was refused by the marker gate")
			}
			select {
			case err := <-converted:
				if !tc.converted || !errors.Is(err, ErrConvertedStore) {
					t.Fatalf("the reconnect stopped the process: %v; want a stop %t", err, tc.converted)
				}
			default:
				if tc.converted {
					t.Fatal("a reconnect over the done marker did not stop the process")
				}
			}
			if tc.gate && !tc.created && !tc.converted {
				if _, hookErr := consumers.client.HookStall(); hookErr == nil || !errors.Is(hookErr, consumernames.ErrNoSentinel) {
					t.Fatalf("the reconnect's marker read = %v; want it failed on the unreadable sentinel, and retried", hookErr)
				}
			}
		})
	}
}

// todaysReconnectCheck is the fixed mode's reconnect check as it stood before the marker gate
// (WatchConversion at 300bee77fd): each connection reads the marker and calls converted on done,
// and a failed read fails the hook's run, but no consumer call waits for any of it.
func todaysReconnectCheck(s *SessionConsumers, converted func(error)) {
	s.client.AddReconnectHook(func(ctx context.Context, _ *natsgo.Conn) error {
		err := s.ReadMarker(ctx)
		switch {
		case err == nil, errors.Is(err, ErrPublishDenied):
			return nil
		case errors.Is(err, ErrConvertedStore):
			converted(err)
			return nil
		}
		return err
	})
}

// storeStreamInfoOverTheSentinel has the server store its STREAM.INFO answer for the selection
// bucket's stream under the sentinel's key, through a request's reply subject, and waits until the
// sentinel reads as missing.
func storeStreamInfoOverTheSentinel(t *testing.T, conn *natsgo.Conn) {
	t.Helper()
	if err := conn.PublishRequest("$JS.API.STREAM.INFO.KV_"+consumernames.SessionBucket, "$KV."+consumernames.SessionBucket+"."+consumernames.SentinelKey, nil); err != nil {
		t.Fatal(err)
	}
	store := consumernames.NewKVStore(consumernames.SessionBucket, consumernames.ConnRequester{Conn: conn})
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := store.State(context.Background())
		if errors.Is(err, consumernames.ErrNoSentinel) && strings.Contains(err.Error(), "unknown field") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stored answer never made the sentinel unreadable: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// markTheCutoverDone writes the selection bucket's sentinel with the cutover's done marker, as the
// cutover's convert does.
func markTheCutoverDone(t *testing.T, conn *natsgo.Conn) {
	t.Helper()
	ctx := context.Background()
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.KeyValue(ctx, consumernames.SessionBucket)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := kv.Get(ctx, consumernames.SentinelKey)
	if err != nil {
		t.Fatal(err)
	}
	store := consumernames.NewKVStore(consumernames.SessionBucket, consumernames.ConnRequester{Conn: conn})
	state, err := store.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Cutover = consumernames.CutoverDone
	if _, err := store.WriteSentinel(ctx, state, entry.Revision()); err != nil {
		t.Fatal(err)
	}
}

// awaitConnected waits until client's connection is connected again after a restart.
func awaitConnected(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if conn := client.Connection(); conn != nil && conn.Status() == natsgo.CONNECTED {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the client did not connect again within 30 s of the restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
