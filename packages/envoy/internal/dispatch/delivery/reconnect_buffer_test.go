package delivery

import (
	"errors"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/natsgrant"
)

// A consumer create or delete Dispatch sends while its connection is cut never reaches the
// server: a call whose guard or gate passed before the cut, and whose request then failed, must not
// be sent after the reconnect, on a connection nothing checks again. nats.go holds a publish made
// while a connection is down, up to ReconnectBufSize (8 MB by default), and sends it once the
// connection is back; every bus connection turns that buffer off (internal/bus, options), so the
// publish fails at once. The row cuts the network under two of Dispatch's connections as it makes
// them -- a fixed intake's, whose guard read no converted marker (fixedNamesAllowed), and a fresh
// generation's, whose gate read the cutover done (consumernames.Admit) -- and while it is cut sends
// on each the take-over's create (createDurable), the create request itself (an upsert, which
// createDurable sends after its lookup) and the delete of an existing consumer the connection's
// grant may delete. Each is refused at once, and once the network is back none has reached the
// server: the consumers to create are absent and the ones to delete are there. The hub stays up,
// so its JetStream answers whatever arrives after the reconnect.
//
// The control is the same create request and delete from a connection with nats.go's default
// buffer and a grant of every subject, cut and restored with the others: both reach the server
// after the reconnect, so the row's answers for Dispatch's connections can fail.
func TestAConsumerCallSentWhileDispatchsConnectionIsCutNeverReachesTheServer(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	hub := startFreshHub(t)
	hub.createStream(t, 0)
	network := testnats.StartProxy(t, hub.url)
	_, js := hub.admin(t)
	const generation, oldGeneration = "GenCutA1b2C3", "GenOldA1b2C3"
	events, jobs := bus.DispatchDeliveryDurable, bus.DispatchJobDurable
	pushed := func(config natsgo.ConsumerConfig, name string) *natsgo.ConsumerConfig {
		config.Durable, config.DeliverSubject = name, natsgo.NewInbox()
		return &config
	}
	// What the deletes act on: the jobs durable's fixed name, the old generation the fresh
	// connection replaces, and the control's.
	for _, name := range []string{jobs, events + "~" + oldGeneration, jobs + "~ControlOld"} {
		if _, err := js.AddConsumer(bus.Stream, pushed(jobConsumerConfig(), name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	guard := &heldDurable{name: events, throttle: &bindThrottle{}}
	guard.guardFixedNames(pool, nil)
	if !guard.fixedNamesAllowed(ctx) {
		t.Fatal("the fixed intake's guard refused a store with no marker")
	}
	writeCutover(t, pool, consumernames.CutoverDone)
	state, err := dispatchRecords(t, pool).State(ctx)
	if err != nil || consumernames.Admit(state) != nil {
		t.Fatalf("the fresh gate did not admit the cutover done: %+v, %v", state, err)
	}

	// connect connects as Dispatch connects (freshHub.connectTo), through the network the row cuts.
	connect := func(name string) *bus.Client {
		t.Helper()
		client, err := hub.connectTo([]string{network.URL()}, name)
		if err != nil {
			t.Fatalf("connect %s: %v", name, err)
		}
		t.Cleanup(client.Close)
		if err := client.AwaitAdmitted(ctx, name, 10*time.Second); err != nil {
			t.Fatalf("admit %s: %v", name, err)
		}
		return client
	}
	intake := connect(intakeConnectionName)
	fresh := connect(natsgrant.DispatchIntakeConnection(events, generation, oldGeneration))
	reconnected := make(chan struct{}, 1)
	control, err := testnats.ConnectSeed(t, network.URL(), hub.adminSeed,
		func(o *natsgo.Options) error { o.AllowReconnect = true; return nil },
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(50*time.Millisecond),
		natsgo.ReconnectHandler(func(*natsgo.Conn) {
			select {
			case reconnected <- struct{}{}:
			default:
			}
		}))
	if err != nil {
		t.Fatalf("connect the control: %v", err)
	}
	t.Cleanup(control.Close)
	if control.Opts.ReconnectBufSize != natsgo.DefaultReconnectBufSize {
		t.Fatalf("control: its reconnect buffer is %d, want nats.go's default", control.Opts.ReconnectBufSize)
	}
	controlJS, err := control.JetStream()
	if err != nil {
		t.Fatalf("the control's JetStream: %v", err)
	}

	network.Cut()
	waitFor(t, 20*time.Second, func() bool {
		return !intake.Connected() && !fresh.Connected() && control.Status() != natsgo.CONNECTED
	}, func() string { return "the connections never saw the network cut" })

	refusedAtOnce := func(what string, call func() error) {
		t.Helper()
		started := time.Now()
		if err := call(); !errors.Is(err, natsgo.ErrReconnectBufExceeded) {
			t.Errorf("%s while cut answered %v, want nats: outbound buffer limit exceeded", what, err)
		}
		if waited := time.Since(started); waited > 2*time.Second {
			t.Errorf("%s while cut took %s, want a refusal at once", what, waited)
		}
	}
	for _, side := range []struct {
		client          *bus.Client
		create, deleted string
	}{
		{intake, events, jobs},
		{fresh, events + "~" + generation, events + "~" + oldGeneration},
	} {
		refusedAtOnce("the take-over's create of "+side.create, func() error {
			_, err := createDurable(side.client.JS(), pushed(deliveryConsumerConfig(), side.create))
			return err
		})
		refusedAtOnce("the create request for "+side.create, func() error {
			_, err := side.client.JS().UpdateConsumer(bus.Stream, pushed(deliveryConsumerConfig(), side.create))
			return err
		})
		refusedAtOnce("the delete of "+side.deleted, func() error {
			return side.client.JS().DeleteConsumer(bus.Stream, side.deleted)
		})
	}
	if _, err := controlJS.UpdateConsumer(bus.Stream, pushed(jobConsumerConfig(), jobs+"~ControlNew"), natsgo.MaxWait(time.Second)); err == nil {
		t.Fatal("control: the default buffer's create was answered while the network was cut")
	}
	if err := controlJS.DeleteConsumer(bus.Stream, jobs+"~ControlOld", natsgo.MaxWait(time.Second)); err == nil {
		t.Fatal("control: the default buffer's delete was answered while the network was cut")
	}

	network.Restore()
	select {
	case <-reconnected:
	case <-time.After(30 * time.Second):
		t.Fatal("control: the default buffer's connection never reconnected")
	}
	waitFor(t, 30*time.Second, func() bool { return intake.Connected() && fresh.Connected() }, func() string {
		return "Dispatch's connections never reconnected"
	})
	for _, conn := range []*natsgo.Conn{intake.Connection(), fresh.Connection(), control} {
		if err := conn.FlushTimeout(5 * time.Second); err != nil {
			t.Fatalf("flush %s after the reconnect: %v", conn.Opts.Name, err)
		}
	}

	exists := func(name string) (bool, error) {
		_, err := js.ConsumerInfo(bus.Stream, name)
		if errors.Is(err, natsgo.ErrConsumerNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	waitFor(t, 10*time.Second, func() bool {
		created, _ := exists(jobs + "~ControlNew")
		kept, _ := exists(jobs + "~ControlOld")
		return created && !kept
	}, func() string {
		return "control: the default buffer's create and delete never reached the server, so the row's answers show nothing"
	})
	for name, want := range map[string]bool{
		events:                       false,
		events + "~" + generation:    false,
		jobs:                         true,
		events + "~" + oldGeneration: true,
	} {
		if got, err := exists(name); err != nil || got != want {
			t.Errorf("%s after the reconnect: present %t (%v), want %t: a call sent while cut reached the server", name, got, err, want)
		}
	}
}
