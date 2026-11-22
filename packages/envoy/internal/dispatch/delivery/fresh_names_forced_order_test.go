//go:build odatrace

package delivery

// The held state write of a take-over's predecessor, for the intakes' two durables, on the hooked
// nats-server (scripts/forced-order.sh): nats-server answers a consumer's delete while its state
// writer can still write, so a write held across the delete, released after the successor is
// created, lands where the predecessor's name points. The build before the fresh mode creates its
// successor under that same name, so the late write lands in the successor's file and the
// successor, once its store restarts, has skipped a message nobody acknowledged on it. A fresh
// name sends the late write to a directory no consumer reads.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// ackFloorAt takes the state writes whose ack floor is seq.
func ackFloorAt(seq uint64) testnats.Match {
	return testnats.Where(fmt.Sprintf("ack floor %d", seq), func(e testnats.Event) bool {
		return e.State != nil && e.State.AckFloor.Stream == seq
	})
}

// heldServer is the hub's server whose store takes the held write: the lone hub; the cluster member
// the stream's R1 copy is placed on; or, at R3, a replica that does not lead the stream, so that
// its leading the consumer after its restart (leadOn) is a step the row takes, as the plan's held
// write asks of "the affected replica".
func heldServer(t *testing.T, hub *freshHub) *testnats.Server {
	t.Helper()
	if hub.cluster == nil {
		return hub.server
	}
	_, js := hub.admin(t)
	info, err := js.StreamInfo(bus.Stream)
	if err != nil || info.Cluster == nil {
		t.Fatalf("read where the stream is placed: %v", err)
	}
	name := info.Cluster.Leader
	if hub.replicas > 1 {
		name = ""
		for _, replica := range info.Cluster.Replicas {
			if replica.Current && !replica.Offline {
				name = replica.Name
				break
			}
		}
		if name == "" {
			t.Fatalf("the stream led by %q has no current replica that does not lead it: %+v", info.Cluster.Leader, info.Cluster.Replicas)
		}
	}
	member := hub.cluster.Member(name)
	if member < 0 {
		t.Fatalf("the stream's copy on %q is no member", name)
	}
	return hub.cluster.Servers[member]
}

// leadOn makes server lead consumer, on a hub whose consumers have more than one copy: it asks the
// consumer's leader to step down until server is the one that leads, and answers why it could not
// ("" when server leads). On one copy there is nothing to move. 2.10.29 takes no preferred leader
// in a step-down (its request must be empty), so each step down hands the consumer to another
// replica the server picks, and the row asks again until it picks server. A replica that is not
// current cannot be picked, and a member just restarted can lag for a while under load (on 2.10.29
// at a load average of about 830, 2026-11-21, twenty asks never handed the consumer to it, and
// 2026-11-22 a restarted replica was still not current 60 s on), so each ask waits until server's
// replica is current. Just after a restart a step-down can also be answered by a member that does
// not hold the consumer yet ("consumer not found"); that answer, or none, or a leader that stays,
// is asked again, within the same 20 asks. A run whose restarted replica cannot be made to lead
// reads no state that replica's file holds, so it judges nothing: the caller runs again.
func leadOn(t *testing.T, hub *freshHub, consumer string, server *testnats.Server) string {
	t.Helper()
	if hub.replicas < 2 {
		return ""
	}
	conn, js := hub.admin(t)
	want := server.Name()
	cluster := func() *natsgo.ClusterInfo {
		info, err := js.ConsumerInfo(bus.Stream, consumer)
		if err != nil {
			return nil
		}
		return info.Cluster
	}
	leader := func() string {
		if c := cluster(); c != nil {
			return c.Leader
		}
		return ""
	}
	current := func() bool {
		c := cluster()
		if c == nil {
			return false
		}
		for _, replica := range c.Replicas {
			if replica.Name == want {
				return replica.Current && !replica.Offline
			}
		}
		return c.Leader == want
	}
	within := func(d time.Duration, ok func() bool) bool {
		for deadline := time.Now().Add(d); ; time.Sleep(100 * time.Millisecond) {
			if ok() {
				return true
			}
			if time.Now().After(deadline) {
				return false
			}
		}
	}
	var refused string
	var seen []string
	for range 20 {
		var now string
		if !within(30*time.Second, func() bool { now = leader(); return now != "" }) {
			return "the consumer " + consumer + " had no leader for 30 s"
		}
		if now == want {
			return ""
		}
		seen = append(seen, now)
		if !within(60*time.Second, current) {
			return fmt.Sprintf("the replica of %s on %s was not current 60 s after its restart (leaders seen %v)", consumer, want, seen)
		}
		reply, err := conn.Request("$JS.API.CONSUMER.LEADER.STEPDOWN."+bus.Stream+"."+consumer, nil, 10*time.Second)
		switch {
		case err != nil:
			refused = err.Error()
			time.Sleep(time.Second)
			continue
		case !strings.Contains(string(reply.Data), `"success":true`):
			refused = string(reply.Data)
			time.Sleep(time.Second)
			continue
		}
		if !within(15*time.Second, func() bool { next := leader(); return next != "" && next != now }) {
			refused = "the leader " + now + " stayed after a step-down answered success"
		}
	}
	return fmt.Sprintf("%s never led the consumer %s in 20 asks (leaders seen %v; the last refused: %q)", want, consumer, seen, refused)
}

// restartStore stops and starts server, keeping its store, and waits for the hub to serve again.
func restartStore(t *testing.T, hub *freshHub, server *testnats.Server) {
	t.Helper()
	server.Stop(t)
	server.Start(t)
	if hub.cluster != nil {
		hub.cluster.AwaitReady(t)
	}
}

// ackThrough acknowledges, on the gone holder's subscription, each message up to stream sequence
// last, and answers the next one it is handed, unacknowledged.
func ackThrough(t *testing.T, sub *natsgo.Subscription, last uint64) *natsgo.Msg {
	t.Helper()
	for {
		msg, err := sub.NextMsg(10 * time.Second)
		if err != nil {
			t.Fatalf("the gone holder was handed nothing more: %v", err)
		}
		meta, err := msg.Metadata()
		if err != nil {
			t.Fatalf("read a delivery's metadata: %v", err)
		}
		if meta.Sequence.Stream > last {
			return msg
		}
		if err := msg.AckSync(); err != nil {
			t.Fatalf("acknowledge %d: %v", meta.Sequence.Stream, err)
		}
	}
}

// Race regression, a held write at the take-over of each durable, on one copy, an R1 cluster and
// R3. The gone holder has acknowledged 1 to 20, and the take-over reads that floor and so starts
// its successor at 21. Its next request -- the successor's create, or the old build's delete of its
// fixed name -- is held where the server takes it while the gone holder acknowledges 21 late, and
// the state write carrying that floor is held before its I/O on one server (heldServer: at R3 a
// replica that does not lead). Released in turn, after the successor's own first write, then that
// server restarted and made to lead the successor (leadOn), the successor must still owe 21:
// nobody acknowledged 21 on it. Control: a same-name re-creation on the same server, under the
// same holds, comes back from the restart, led by that server, with 21 acknowledged.
//
// At R3 a run can judge nothing, on either build, and then the row runs again on a new hub, at
// most heldWriteAttempts times, and fails if none judged. The held replica can write the
// same-name consumer's file again after the late write and before the restart stops it, replacing
// the late write (overwritten; on the build before, 3 of 13 runs of delivery-jobs at R3 on 2.15.1,
// 2026-11-21, came back with ack floor 0), so the restart reads the later write; or the restarted
// replica cannot be made to lead (leadOn), so the floor read is another replica's.
func TestForcedOrderAPredecessorsHeldWriteReachesNoSuccessor(t *testing.T) {
	for _, topology := range hubTopologies {
		for _, kind := range durableKinds {
			t.Run(topology.name+"/"+kind.logical, func(t *testing.T) {
				for attempt := range heldWriteAttempts {
					judged := false
					t.Run(fmt.Sprintf("attempt %d", attempt+1), func(t *testing.T) {
						judged = predecessorsHeldWriteReachesNoSuccessor(t, topology, kind)
					})
					if judged || t.Failed() {
						return
					}
				}
				t.Fatalf("none of %d runs judged anything: each run's \"judged nothing\" line says why", heldWriteAttempts)
			})
		}
	}
}

// heldWriteAttempts bounds the runs of the held-write row on one hub and durable.
const heldWriteAttempts = 5

// predecessorsHeldWriteReachesNoSuccessor is one run of the held-write row on a new hub of
// topology, for kind's durable: its control, then the take-over. It answers whether the run
// judged: false when either one's restart judged nothing (unjudged).
func predecessorsHeldWriteReachesNoSuccessor(t *testing.T, topology hubTopology, kind durableKind) bool {
	hub, pool := freshRigOn(t, topology, 0)
	hooks := testnats.Hook(t)

	controlJudged := false
	if !t.Run("control: a same-name re-creation", func(t *testing.T) {
		controlJudged = sameNameRecreationLoses(t, hub, hooks, kind)
	}) || !controlJudged {
		return false
	}

	old, gone := seedPreviousHolder(t, hub, pool, kind)
	sent := hub.publish(t, kind, 30)
	late := ackThrough(t, gone, sent[19])
	if meta, _ := late.Metadata(); meta.Sequence.Stream != sent[20] {
		t.Fatalf("the gone holder's next message is %d, want %d", meta.Sequence.Stream, sent[20])
	}
	server := heldServer(t, hub)
	request := holdTheSuccessor(t, hub, hooks, kind, old)
	process := startDurableProcess(t, hub, pool, kind, freshNames, blockingHandlers())
	request.reached(t)

	write := hooks.Hold(testnats.WritePreIO, testnats.OnServer(server), testnats.Consumer(old), ackFloorAt(sent[20]))
	if err := late.Ack(); err != nil {
		t.Fatalf("the gone holder's late acknowledgement: %v", err)
	}
	write.Reached(t)
	mark := hooks.Mark()
	request.release()

	var successor string
	waitFor(t, 30*time.Second, func() bool {
		successor = successorOf(t, pool, kind.logical, old)
		if successor == "" {
			// The old build makes its successor under the same name.
			if len(hooks.Events(testnats.StoreCreate, testnats.Consumer(old), testnats.After(mark))) > 0 {
				successor = old
			}
		}
		return successor != "" && len(hooks.Events(testnats.Point("write.done"), testnats.OnServer(server), testnats.Consumer(successor), testnats.After(mark))) > 0
	}, func() string { return "the take-over made no successor with a write of its own" })
	// The late write must be the last write the successor's name gets on server before the
	// restart, as a predecessor's write that lands after its successor's state stops changing is:
	// with the handlers held, the successor stops once it has delivered every message, or its
	// ack-pending bound, and has written that state. Released earlier, the late write can be
	// overwritten by the successor's next write, and the row then passes on the build before the
	// fresh mode by chance (a probe of that build's delivery-jobs run at 2026-11-21T06:22Z: the
	// late write, floor 51, landed after the successor's last write, delivered 60, and the
	// successor came back with floor 51).
	_, settling := hub.admin(t)
	waitFor(t, 30*time.Second, func() bool {
		info, err := settling.ConsumerInfo(bus.Stream, successor)
		if err != nil || (info.NumPending != 0 && info.NumAckPending < info.Config.MaxAckPending) {
			return false
		}
		for _, e := range hooks.Events(testnats.Point("write.done"), testnats.OnServer(server), testnats.Consumer(successor), testnats.After(mark)) {
			if e.State != nil && e.State.Delivered.Stream == info.Delivered.Stream {
				return true
			}
		}
		return false
	}, func() string { return "the successor " + successor + " never wrote a settled state" })

	released := hooks.Mark()
	write.Release()
	lateWrite := hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(server), testnats.Consumer(old), testnats.After(released))
	restartStore(t, hub, server)
	if why := unjudged(t, hub, hooks, server, lateWrite, successor, mark); why != "" {
		t.Logf("this run judged nothing: %s", why)
		process.unblock()
		return false
	}

	_, js := hub.admin(t)
	var info *natsgo.ConsumerInfo
	waitFor(t, 30*time.Second, func() bool {
		var err error
		info, err = js.ConsumerInfo(bus.Stream, successor)
		return err == nil
	}, func() string { return "the successor " + successor + " did not come back from the restart" })
	t.Logf("after the restart: %s led by %s, the restarted server: ack floor %d, delivered %d; the late write went to %s", successor, server.Name(), info.AckFloor.Stream, info.Delivered.Stream, lateWrite.Path)
	if info.AckFloor.Stream >= sent[20] {
		t.Fatalf("the successor %s came back from the restart with ack floor %d: message %d, which nobody acknowledged on it, is skipped",
			successor, info.AckFloor.Stream, sent[20])
	}
	// The handlers' acknowledgements must reach the successor, not a connection still
	// reconnecting after the restart, where they would wait out the ack wait.
	waitFor(t, 30*time.Second, func() bool {
		info, err := js.ConsumerInfo(bus.Stream, successor)
		return err == nil && info.PushBound
	}, func() string { return "the successor " + successor + " is not bound again after the restart" })
	process.unblock()
	awaitHandled(t, sent[20:], process)
	return true
}

// unjudged answers why a run, after the held server's restart, judges nothing on consumer, and ""
// when it judges: the late write was replaced in consumer's file before the server stopped
// (overwritten), or the restarted server could not be made to lead consumer (leadOn).
func unjudged(t *testing.T, hub *freshHub, hooks *testnats.Hooks, server *testnats.Server, late testnats.Event, consumer string, mark int) string {
	t.Helper()
	if overwritten(t, hooks, server, late, consumer, mark) {
		return fmt.Sprintf("a later write to %s's file, %s, replaced the late write before the server stopped", consumer, late.Path)
	}
	return leadOn(t, hub, consumer, server)
}

// overwritten reports whether the late write (late, a write.done on server) landed in consumer's
// file and a later write to that same file, before server's shutdown, replaced it. consumer's file
// is the one its writes on server after mark went to. A late write to any other file -- the
// predecessor's own, under a fresh name -- replaces nothing of consumer's, and a late write that
// was the file's last before the shutdown is what the restart reads, so both runs judge; only a
// replaced one judged nothing, since the restart then reads the later write.
func overwritten(t *testing.T, hooks *testnats.Hooks, server *testnats.Server, late testnats.Event, consumer string, mark int) bool {
	t.Helper()
	own := hooks.Events(testnats.Point("write.done"), testnats.OnServer(server), testnats.Consumer(consumer), testnats.After(mark))
	if len(own) == 0 {
		t.Fatalf("UNKNOWN: %s took no write on the held server, so which file is its cannot be told", consumer)
	}
	if own[0].Path != late.Path {
		return false
	}
	stops := hooks.Events(testnats.ServerShutdown, testnats.OnServer(server), testnats.After(late.N))
	if len(stops) == 0 {
		t.Fatalf("UNKNOWN: the held server's restart reported no shutdown after the late write, so what its file held at the stop cannot be told")
	}
	replaced := hooks.Events(testnats.Point("write.done"), testnats.OnServer(server), testnats.After(late.N), testnats.Where("a later write to the late write's file before the shutdown", func(e testnats.Event) bool {
		return e.Path == late.Path && e.N < stops[0].N
	}))
	return len(replaced) > 0
}

// heldSuccessor is the hold, on every server it can reach, of the take-over's step that makes the
// successor: reached once each is.
type heldSuccessor []*testnats.Hold

func (h heldSuccessor) reached(t *testing.T) {
	t.Helper()
	for _, hold := range h {
		hold.Reached(t)
	}
}

func (h heldSuccessor) release() {
	for _, hold := range h {
		hold.Release()
	}
}

// holdTheSuccessor holds the take-over's next step after its read of old: on a lone server the
// request that creates a fresh generation, or the build before's delete of its fixed name, where
// the server takes it; on a cluster, where a create is answered through the meta layer, the fresh
// generation's assignment on every member before it applies it. The meta layer has no hold point
// for a removal, so on a cluster the build before's delete of its fixed name (old is the fixed
// name: the at-main rig seeds no other) is held where the API takes the request, as on a lone
// server: on 2.15.1 at every member's worker; on 2.10.29, which reports the point only on the
// requesting client's own server, inline in that client's read loop (testnats.APIWorker), by one
// hold on whichever member that is.
func holdTheSuccessor(t *testing.T, hub *freshHub, hooks *testnats.Hooks, kind durableKind, old string) heldSuccessor {
	t.Helper()
	request := testnats.Where("the take-over's create or delete after its read", func(e testnats.Event) bool {
		return e.Subject == "$JS.API.CONSUMER.DELETE."+bus.Stream+"."+old ||
			strings.HasPrefix(e.Subject, "$JS.API.CONSUMER.CREATE."+bus.Stream+"."+kind.logical+"~")
	})
	if hub.cluster == nil || (old == kind.logical && natsserver.VERSION == "2.10.29") {
		return heldSuccessor{hooks.Hold(testnats.APIWorker, request)}
	}
	holds := make(heldSuccessor, len(hub.cluster.Servers))
	if old == kind.logical {
		for i, srv := range hub.cluster.Servers {
			holds[i] = hooks.Hold(testnats.APIWorker, testnats.OnServer(srv), request)
		}
		return holds
	}
	successor := testnats.Where("the take-over's fresh generation", func(e testnats.Event) bool {
		return strings.HasPrefix(e.Consumer, kind.logical+"~") && e.Consumer != old
	})
	for i, srv := range hub.cluster.Servers {
		holds[i] = hooks.Hold(testnats.MetaApply, testnats.OnServer(srv), testnats.Stream(bus.Stream), successor)
	}
	return holds
}

// sameNameRecreationLoses is the control of the held-write row: on the hub's server, consumer C
// acknowledged to 20, its write of floor 21 held, C deleted and created again at 21 under its own
// name, its first write done, the held write released and the store restarted -- and C comes back
// with 21 acknowledged. It shows the holds reach the order that loses a message, so the
// treatment's clean result is no blind spot. It answers whether it judged: false when its restart
// judged nothing (unjudged), as the row's own run can.
func sameNameRecreationLoses(t *testing.T, hub *freshHub, hooks *testnats.Hooks, kind durableKind) bool {
	t.Helper()
	conn, js := hub.admin(t)
	name := "control-" + kind.logical
	config := natsgo.ConsumerConfig{Durable: name, DeliverSubject: natsgo.NewInbox(), FilterSubject: kind.config().FilterSubject, AckPolicy: natsgo.AckExplicitPolicy}
	sub, err := conn.SubscribeSync(config.DeliverSubject)
	if err != nil {
		t.Fatalf("subscribe the control's deliver subject: %v", err)
	}
	if _, err := js.AddConsumer(bus.Stream, &config); err != nil {
		t.Fatalf("create the control consumer: %v", err)
	}
	sent := hub.publish(t, kind, 30)
	late := ackThrough(t, sub, sent[19])
	server := heldServer(t, hub)
	write := hooks.Hold(testnats.WritePreIO, testnats.OnServer(server), testnats.Consumer(name), ackFloorAt(sent[20]))
	if err := late.Ack(); err != nil {
		t.Fatalf("the late acknowledgement: %v", err)
	}
	write.Reached(t)
	if err := js.DeleteConsumer(bus.Stream, name); err != nil {
		t.Fatalf("delete the control consumer: %v", err)
	}
	// On R3 the delete's answer can come before the consumer's removal is applied, and a create of
	// the same name then is refused as a name in use (2.15.1, 2026-11-22: "consumer name already in
	// use"), so the create waits until a read answers that there is no such consumer.
	waitFor(t, 30*time.Second, func() bool {
		_, err := js.ConsumerInfo(bus.Stream, name)
		return errors.Is(err, natsgo.ErrConsumerNotFound)
	}, func() string { return "the control consumer " + name + " is still there 30 s after its delete" })
	mark := hooks.Mark()
	again := startingAt(config, sent[20])
	again.DeliverSubject = natsgo.NewInbox()
	if _, err := js.AddConsumer(bus.Stream, &again); err != nil {
		t.Fatalf("create the control consumer again: %v", err)
	}
	hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(server), testnats.Consumer(name), testnats.After(mark))
	released := hooks.Mark()
	write.Release()
	lateWrite := hooks.Await(t, testnats.Point("write.done"), testnats.OnServer(server), testnats.Consumer(name), testnats.After(released))
	restartStore(t, hub, server)
	// The restart closed the admin's connection, which does not reconnect: read on a new one.
	_, js = hub.admin(t)
	if why := unjudged(t, hub, hooks, server, lateWrite, name, mark); why != "" {
		t.Logf("control: this run judged nothing: %s", why)
		if err := js.DeleteConsumer(bus.Stream, name); err != nil {
			t.Fatalf("delete the control consumer: %v", err)
		}
		return false
	}
	var info *natsgo.ConsumerInfo
	waitFor(t, 30*time.Second, func() bool {
		info, err = js.ConsumerInfo(bus.Stream, name)
		return err == nil
	}, func() string { return fmt.Sprintf("the control consumer did not come back from the restart: %v", err) })
	if info.AckFloor.Stream < sent[20] {
		t.Fatalf("control: the same-name re-creation came back with ack floor %d, below %d: the holds did not reach the order that loses a message", info.AckFloor.Stream, sent[20])
	}
	if err := js.DeleteConsumer(bus.Stream, name); err != nil {
		t.Fatalf("delete the control consumer: %v", err)
	}
	return true
}
