package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/id"
	"github.com/sjawhar/envoy/internal/logging"
	"github.com/sjawhar/envoy/internal/testnats"
)

// Which pod of a machine relays its sessions' consumers is the receive term's to say and nothing
// else's: the term elects this pod when it begins (durableReceiver.begin) and stands it down when
// it ends (durableReceiver.end, sessionRelays.setOwned). The self-health monitor's durable probe
// and its rebuild, which judge whether the durable is bound to this listener's subscription, say
// nothing about it, and the two rows below are why.
//
// A second writer that went by the binding - the probe clearing the flag on an unbound durable,
// the rebuild setting it after a resubscribe - reads a state the term does not have: a resubscribe
// reports success for a pod with no term at all (durableReceiver.resubscribe), so a lease lost
// between the two would leave a standby relaying every session's consumer beside the real holder,
// each pull and each ack-floor restart racing the other's; and an unbound probe inside a live term
// that something else repaired would leave the holder relaying nothing for the rest of it.

// startRelayingReceiverRig is startReceiverRig for a pod whose term elects it the relayer of its
// machine's session consumers, as main hands its receiver the listener's relays. The set is a bare
// one, with no session to relay: these rows are about which pod is elected, not about what it
// then pulls, and sessionRelays.setOwned is the whole of the election. The lease polls slowly as a
// standby, so a pod that gives its lease up does not take it back while the row reads it.
func startRelayingReceiverRig(t *testing.T, uri, machineID, instance string, handle durableHandler) (*receiverRig, *sessionRelays) {
	t.Helper()
	leaseTiming := testLeaseTiming()
	leaseTiming.standbyPoll = 3 * time.Second
	rig := newReceiverRig(t, uri, machineID, instance, handle, testReceiveTiming(), leaseTiming)
	relays := &sessionRelays{logger: logging.New("relay-ownership " + instance)}
	rig.receiver.relays = relays
	rig.lease.Start(context.Background())
	rig.receiver.Start()
	return rig, relays
}

// A lease lost between an unbound probe and the resubscribe that follows it leaves the pod not
// relaying: its term is over, and the resubscribe that reports success for a pod with no term
// elects nobody.
func TestALeaseLostBetweenAnUnboundProbeAndTheResubscribeLeavesThePodNotRelaying(t *testing.T) {
	uri := testnats.URL(t)
	const machineID = "box-relay-ownership-lost"
	handle, _ := recordingHandler()
	rig, relays := startRelayingReceiverRig(t, uri, machineID, "pod-a", handle)
	rig.awaitState(t, receiveActive, 10*time.Second)
	if !relays.owned.Load() {
		t.Fatal("the pod receiving the durable relays none of its machine's session consumers")
	}

	// The holder's probe reads the durable unbound: it was recreated server-side, so its deliver
	// subject is no longer this pod's subscription's.
	recreateListenerDurable(t, rig.client, rig.consumer)
	if err := rig.receiver.probe(context.Background()); !errors.Is(err, errDurableUnbound) {
		t.Fatalf("the probe of a recreated durable = %v, want errDurableUnbound", err)
	}

	// The term ends before the resubscribe runs, as it does when the lease is released because
	// the receive connection is down.
	rig.lease.GiveUp(rig.lease.Status().Term)
	waitFor(t, 10*time.Second, "pod-a to give the lease up", func() bool { return !rig.lease.Holding() })
	rig.awaitState(t, receiveStandby, 10*time.Second)
	if relays.owned.Load() {
		t.Fatal("the pod goes on relaying after its term ended")
	}

	// The rebuild runs on, with nothing left to subscribe.
	if err := rig.receiver.resubscribe(context.Background()); err != nil {
		t.Fatalf("the rebuild's resubscribe on a pod with no term = %v, want nil", err)
	}
	probe := func() error { return rig.receiver.probe(context.Background()) }
	if err := rebuildListenerDependencies(context.Background(), rig.client, nil, probe, rig.receiver.resubscribe); err != nil {
		t.Fatalf("the self-health rebuild on a pod with no term: %v", err)
	}
	if relays.owned.Load() {
		t.Fatal("a pod with no term relays its machine's session consumers: it pulls every session's consumer beside the pod that holds the lease, and each ack-floor restart deletes the other's")
	}
}

// An unbound probe inside a live term leaves the holder relaying, whether or not a rebuild
// resubscribes after it: the durable's binding is repaired by the receive loop as well (repair),
// and a rebuild whose probe then finds the durable bound returns without resubscribing. Neither
// says anything about the term, which is what elects this pod.
func TestAnUnboundProbeInsideALiveTermLeavesTheHolderRelaying(t *testing.T) {
	uri := testnats.URL(t)
	const machineID = "box-relay-ownership-live"
	handle, _ := recordingHandler()
	rig, relays := startRelayingReceiverRig(t, uri, machineID, "pod-a", handle)
	rig.awaitState(t, receiveActive, 10*time.Second)

	recreateListenerDurable(t, rig.client, rig.consumer)
	if err := rig.receiver.probe(context.Background()); !errors.Is(err, errDurableUnbound) {
		t.Fatalf("the probe of a recreated durable = %v, want errDurableUnbound", err)
	}
	if !relays.owned.Load() {
		t.Fatal("an unbound probe stopped the relays of a pod whose term is running")
	}

	// The receive loop repairs the binding itself, so the rebuild's own probe finds the durable
	// bound again and returns without a resubscribe of its own.
	if err := rig.receiver.resubscribe(context.Background()); err != nil {
		t.Fatalf("the repair's resubscribe: %v", err)
	}
	probe := func() error { return rig.receiver.probe(context.Background()) }
	if err := rebuildListenerDependencies(context.Background(), rig.client, nil, probe, rig.receiver.resubscribe); err != nil {
		t.Fatalf("the self-health rebuild of a repaired durable: %v", err)
	}
	if !relays.owned.Load() {
		t.Fatal("the holder relays nothing for the rest of its term after an unbound probe its own repair answered")
	}
	if !rig.lease.Holding() || rig.receiver.report().State != receiveActive {
		t.Fatalf("pod-a is no longer the holder receiving: lease held %v, state %s", rig.lease.Holding(), rig.receiver.report().State)
	}
}

// A relay acts on a session's consumer only while this pod's receive term is current and no other
// pod can hold the lease yet. The rows below hold the two windows in which the term had ended and
// this pod still relayed: a term that ended before its receiving began, and a lease that ended
// the term before the receive loop heard it. Each lease is on a fake clock, as the lease's own
// rows are, and is stepped by hand.

// stepLeaseDown has l step down as a holder does whose renewal finds another pod's record: pod-d
// writes the record over l's, and l's next round reads it (lease.stepDown). It reports false when
// l holds the lease after that round.
func stepLeaseDown(t *testing.T, kv bus.KeyValue, l *lease) bool {
	t.Helper()
	putLease(t, kv, `{"holder":"pod-d","boot":"00000000000000dd"}`)
	l.step()
	return !l.Holding()
}

// A term that ended after the receive loop prepared the durable and before it began receiving
// elects this pod the relayer of nothing: begin hands the relays the term's context, and they take
// the term only while that context is live (sessionRelays.setOwned), so no relay starts and none
// restarts a session's consumer on a pod whose lease another pod has taken. The relay set here has
// a session on consumer delivery, whose relay a term that took it would start at once.
func TestATermEndedBetweenPrepareAndBeginStartsNoRelayRun(t *testing.T) {
	relayRig := newRelayRigUnowned(t, relayPauseFirst, relayPauseMax)
	relayRig.subscribe(t, consumerRegistration("ses_term_ended_before_begin_"+id.New(), "epoch-1"))
	var restarts atomic.Int32
	relayRig.deps.consumers.afterStoredRowRead = func() { restarts.Add(1) }

	handle, _ := recordingHandler()
	pod := newReceiverRig(t, relayRig.url, leaseTestMachine, "pod-a", handle, testReceiveTiming(), testLeaseTiming())
	clock := newFakeLeaseClock()
	held := newLease(pod.lease.kv, leaseTestMachine, "pod-a", productionLeaseConfig(), clock.now, logging.NewWithWriter(leaseTestMachine, pod.logs))
	pod.receiver.lease = held
	pod.receiver.relays = relayRig.relays
	held.step()
	status := held.Status()
	if !status.Holding {
		t.Fatalf("pod-a did not take the lease: %+v", status)
	}
	info, err := ensureListenerDurable(context.Background(), pod.client, pod.consumer)
	if err != nil {
		t.Fatalf("create the durable: %v", err)
	}

	// The durable is prepared. Then another pod takes the record, the lease steps down at its next
	// round, and the receive loop's watcher ends the term's context, all before begin.
	termCtx, endTerm := context.WithCancel(context.Background())
	if !stepLeaseDown(t, pod.lease.kv, held) {
		t.Fatal("pod-a's lease did not step down for the record another pod wrote")
	}
	endTerm()
	if err := pod.receiver.begin(termCtx, info.Config.DeliverSubject, 0, status.Term); err != nil {
		t.Fatalf("subscribe to the durable's deliver subject: %v", err)
	}
	t.Cleanup(func() { pod.receiver.end(context.Background()) })

	if relayRig.relays.owned.Load() {
		t.Fatal("a term that ended before its receiving began elected this pod the relayer of its machine's session consumers: its relays restart each session's consumer on a pod whose lease another pod has taken")
	}
	time.Sleep(time.Second)
	if n := restarts.Load(); n != 0 {
		t.Fatalf("%d relay restarts of a session's consumer began for a term that ended before its receiving began, want none", n)
	}
}

// A relay run that reaches its restart after the lease stepped down does not restart the
// session's consumer: the lease ends the term a moment before the receive loop hears it and stops
// the relays (durableReceiver.end), and the restart checks the term just before its RestartAtFloor
// (sessionRelays.termContext). Here the step-down lands between the restart's read of the session's
// row and that call, with the term's context still live, as the receive loop has not heard it.
func TestARelayRestartAfterTheLeaseSteppedDownDoesNotRestartTheConsumer(t *testing.T) {
	rig, run := newRestartableRun(t, "ses_restart_after_step_down_")
	kv := leaseBucket(t)
	clock := newFakeLeaseClock()
	held := testLease(kv, "pod-a", productionLeaseConfig(), clock.now)
	held.step()
	status := held.Status()
	if !status.Holding {
		t.Fatalf("pod-a did not take the lease: %+v", status)
	}
	rig.relays.term = &relayTerm{ctx: context.Background(), untilLoss: func() time.Duration { return held.UntilLoss(status.Term) }}
	steppedDown := false
	rig.deps.consumers.afterStoredRowRead = func() {
		if !steppedDown {
			steppedDown = stepLeaseDown(t, kv, held)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, pull, outcome := rig.relays.restart(ctx, run)
	rig.deps.consumers.afterStoredRowRead = nil
	if !steppedDown {
		t.Fatal("pod-a's lease did not step down inside the restart")
	}
	if pull != nil {
		pull.Stop()
		t.Fatal("a relay run that reached its restart after this pod's lease stepped down restarted the session's consumer and pulled it: two pods can restart one session's consumer at once")
	}
	if at := run.restartedAt.Load(); at != 0 {
		t.Fatalf("the restart recorded a restart at mark %d after the lease stepped down, want none", at)
	}
	if outcome != relayRetry {
		t.Fatalf("the restart's outcome is %d, want relayRetry (%d): the relay tries again while the session is still this listener's, and the term's end stops it", outcome, relayRetry)
	}
}

// A restart that starts just before the lease's loss deadline is cut off at it: the restart sends
// RestartAtFloor on a context that ends at that deadline (sessionRelays.termContext), the moment
// another pod may take the lease, so it cannot act on the consumer after that. The
// session-consumer connection goes through a proxy that the restart's read of the session's row
// freezes, so the call waits with no answer until its context ends.
func TestARelayRestartStartedJustBeforeTheLeasesLossDeadlineIsCutOffAtIt(t *testing.T) {
	server := testnats.URL(t)
	proxy := testnats.StartProxy(t, server)
	client, err := bus.ConnectOwningStream([]string{server}, bus.WithReplicas(1))
	if err != nil {
		t.Fatalf("connect the listener: %v", err)
	}
	t.Cleanup(client.Close)
	rig := newRelayRigUnownedOn(t, client, proxy.URL(), relayPauseFirst, relayPauseMax)
	t.Cleanup(proxy.Restore)
	sessionID := "ses_restart_at_loss_deadline_" + id.New()
	rig.subscribe(t, consumerRegistration(sessionID, "epoch-1"))

	kv := leaseBucket(t)
	clock := newFakeLeaseClock()
	held := testLease(kv, "pod-a", productionLeaseConfig(), clock.now)
	held.step()
	status := held.Status()
	if !status.Holding {
		t.Fatalf("pod-a did not take the lease: %+v", status)
	}
	// The holder's validity runs to its last renewal's start + ttl - margin: left of it remains.
	const left = 500 * time.Millisecond
	clock.advance(leaseTTL - leaseMargin - left)
	if got := held.UntilLoss(status.Term); got != left {
		t.Fatalf("the lease's loss deadline is %s away, want %s", got, left)
	}
	rig.relays.term = &relayTerm{ctx: context.Background(), untilLoss: func() time.Duration { return held.UntilLoss(status.Term) }}
	var frozeAt time.Time
	var freeze sync.Once
	rig.deps.consumers.afterStoredRowRead = func() {
		freeze.Do(func() {
			proxy.Freeze()
			frozeAt = time.Now()
		})
	}

	type restarted struct {
		pull    *bus.SessionPull
		outcome relayOutcome
		at      time.Time
	}
	done := make(chan restarted, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*sessionConsumerCallTimeout)
		defer cancel()
		_, pull, outcome := rig.relays.restart(ctx, &relayRun{sessionID: sessionID})
		done <- restarted{pull: pull, outcome: outcome, at: time.Now()}
	}()
	const slack = time.Second
	select {
	case r := <-done:
		proxy.Restore()
		if r.pull != nil {
			r.pull.Stop()
			t.Fatal("a restart whose call had no answer before the lease's loss deadline pulled the session's consumer")
		}
		ran := r.at.Sub(frozeAt)
		if ran < left || ran > left+slack {
			t.Fatalf("a restart started %s before the lease's loss deadline ended %s after it started, want at that deadline: its context did not end there, so it acts on the consumer once another pod may hold the lease", left, ran)
		}
		if r.outcome != relayRetry {
			t.Fatalf("the restart's outcome is %d, want relayRetry (%d)", r.outcome, relayRetry)
		}
	case <-time.After(left + 2*slack):
		proxy.Restore()
		<-done
		t.Fatalf("a restart started %s before the lease's loss deadline was still running %s after it: its context did not end there, so it acts on the consumer once another pod may hold the lease", left, 2*slack)
	}
}
