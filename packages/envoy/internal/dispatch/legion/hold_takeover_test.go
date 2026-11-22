package legion

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// TestATakeOverDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes: R3 bounds the time a
// held message holds the durable's ack floor at about 11 minutes, 11 reads and 10 hand-backs, and
// a lease take-over in the middle of the count does not lengthen it. After INFRA's good starting
// read its daemon stops answering, as in TestAHeldReadThatTimesOutHoldsTheFloorAboutElevenMinutes:
// each read waits out the client's window, and the daemon moves the intake's clock RequestTimeout
// on at each read. A gap's message holds the read. After at least four hand-backs the intake stops
// and a second intake takes the durable over, as in TestAHoldsHandBacksAreCountedAcrossATakeOver.
// The message is acknowledged after stateMaxHolds hand-backs in all; on the intake's clock it held
// the floor for less than 11 reads of RequestTimeout and a minute.
//
// At 88db4653 the take-over's own read of the owed project (takenOver, SettingsChanged,
// startingPoint, due since the last read began RequestTimeout before) runs on the project's runner
// ahead of the held message's redelivery. With a daemon that times out, that read adds one
// RequestTimeout to the hold, so the floor is held 12 reads long, and each further take-over adds
// one more. The tester measured the same on the real programs: two Dispatch processes, a daemon
// that never answers and a SIGTERM after four hand-backs held the floor 12m12s from the publish.
func TestATakeOverDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes(t *testing.T) {
	const failures = stateMaxHolds + 1
	// before is how many hand-backs the first intake makes at the least before it stops.
	const before = 4
	// window is the client's window in the rig, as in
	// TestAHeldReadThatTimesOutHoldsTheFloorAboutElevenMinutes.
	const window = 200 * time.Millisecond
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = window
	shared := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = shared
		transport.CloseIdleConnections()
	})
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	daemon.stalls(func() { rig.clock.advance(RequestTimeout) })
	from := rig.clock.now()
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "merging")))
	holder := rig.lastStreamSeq()
	rig.waitFor(time.Minute, func() bool { return len(rig.logRecords(heldLine)) >= before }, func() string {
		return fmt.Sprintf("the message went back %d times, want %d before the take-over", len(rig.logRecords(heldLine)), before)
	})
	rig.stop()
	first := len(rig.logRecords(heldLine))
	rig.start()

	var info *natsgo.ConsumerInfo
	rig.waitFor(2*time.Minute, func() bool {
		info = rig.consumerInfo()
		return info.AckFloor.Stream >= holder && info.NumAckPending == 0 && info.NumPending == 0
	}, func() string {
		return fmt.Sprintf("the message that holds INFRA's read is not acknowledged after %d hand-backs, %d of them before the take-over: %+v",
			len(rig.logRecords(heldLine)), first, info)
	})
	held := rig.clock.now().Sub(from)
	if got := rig.heldValues(holder, "handBack"); len(got) != stateMaxHolds {
		t.Errorf("the message went back %d times in all, %d of them before the take-over, want %d", len(got), first, stateMaxHolds)
	}
	reads := time.Duration(failures) * RequestTimeout
	if held >= reads+time.Minute {
		t.Errorf("on the intake's clock the message held the ack floor for %s across one take-over, want less than the %s of its %d reads and a minute: INFRA's daemon served %d reads that timed out, %d more than the message's own; the take-over's read of the owed project ran ahead of the held message's redelivery",
			held.Round(time.Second), reads, failures, daemon.served()-1, daemon.served()-1-failures)
	}
}
