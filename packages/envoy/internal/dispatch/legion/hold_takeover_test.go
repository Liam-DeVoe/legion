package legion

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestATakeOverDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes: R3 bounds the time a
// held message holds the durable's ack floor at about 11 minutes, 11 reads and 10 hand-backs, and
// a lease take-over in the middle of the count does not lengthen it, however many take-overs there
// are. After INFRA's good starting read its daemon stops answering, as in
// TestAHeldReadThatTimesOutHoldsTheFloorAboutElevenMinutes: each read waits out the client's
// window, and the daemon moves the intake's clock RequestTimeout on at each read. A gap's message
// holds the read. After each step's hand-backs the intake stops and a second intake takes the
// durable over, as in TestAHoldsHandBacksAreCountedAcrossATakeOver. The message is acknowledged
// after stateMaxHolds hand-backs in all; on the intake's clock it held the floor for less than 11
// reads of RequestTimeout and a minute.
//
// At 88db4653 the take-over's own read of the owed project (takenOver, SettingsChanged,
// startingPoint, due since the last read began RequestTimeout before) runs on the project's runner
// ahead of the held message's redelivery. With a daemon that times out, that read adds one
// RequestTimeout to the hold, so the floor is held 12 reads long, and each further take-over adds
// one more. The tester measured the same on the real programs: two Dispatch processes, a daemon
// that never answers and a SIGTERM after four hand-backs held the floor 12m12s from the publish.
// R3 says a reason that arrives while a read is in flight or owed joins it, so the take-over's
// reason joins the held message's read and makes no read of its own.
func TestATakeOverDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes(t *testing.T) {
	for _, test := range []struct {
		name string
		// takeOvers lists, for each take-over, how many hand-backs the intakes have made in all
		// at the least before it.
		takeOvers []int
	}{
		{"one take-over", []int{4}},
		{"two take-overs", []int{3, 6}},
	} {
		t.Run(test.name, func(t *testing.T) {
			const failures = stateMaxHolds + 1
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
			var before []int
			for _, at := range test.takeOvers {
				rig.waitFor(time.Minute, func() bool { return len(rig.logRecords(heldLine)) >= at }, func() string {
					return fmt.Sprintf("the message went back %d times, want %d before take-over %d", len(rig.logRecords(heldLine)), at, len(before)+1)
				})
				rig.stop()
				before = append(before, len(rig.logRecords(heldLine)))
				rig.start()
			}

			var info *natsgo.ConsumerInfo
			rig.waitFor(2*time.Minute, func() bool {
				info = rig.consumerInfo()
				return info.AckFloor.Stream >= holder && info.NumAckPending == 0 && info.NumPending == 0
			}, func() string {
				return fmt.Sprintf("the message that holds INFRA's read is not acknowledged after %d hand-backs, %v of them before each take-over: %+v",
					len(rig.logRecords(heldLine)), before, info)
			})
			held := rig.clock.now().Sub(from)
			if got := rig.heldValues(holder, "handBack"); len(got) != stateMaxHolds {
				t.Errorf("the message went back %d times in all, %v of them before each take-over, want %d", len(got), before, stateMaxHolds)
			}
			reads := time.Duration(failures) * RequestTimeout
			if held >= reads+time.Minute {
				t.Errorf("on the intake's clock the message held the ack floor for %s across %d take-overs, want less than the %s of its %d reads and a minute: INFRA's daemon served %d reads that timed out, %d more than the message's own; each take-over's read of the owed project ran ahead of the held message's redelivery",
					held.Round(time.Second), len(test.takeOvers), reads, failures, daemon.served()-1, daemon.served()-1-failures)
			}
		})
	}
}

// TestASettingWrittenDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes: R3 bounds the
// time a held message holds the durable's ack floor at about 11 minutes, 11 reads and 10
// hand-backs, and a write of the project's own setting in the middle of the count does not start
// the count again. After INFRA's good starting read its daemon stops answering, as in
// TestAHeldReadThatTimesOutHoldsTheFloorAboutElevenMinutes, and a gap's message holds the read.
// After four hand-backs a person writes INFRA's setting again: the same daemon, or another one
// that does not answer either. R3 has the write read the daemon it names at once (startingPoint,
// ReadReasonStart), so the write adds that one read to the hold and no more. The message is
// acknowledged after stateMaxHolds hand-backs in all; on the intake's clock it held the floor for
// less than its 11 reads, the write's own read and a minute.
//
// At f77fd4c7 each write of the setting deletes the project's cursor (settingWritten), and the
// count of the hold's hand-backs with it (read_holds), so the message's next redelivery holds the
// read again from 1: the tester measured hand-backs 1 to 4 and then 1 to 10, 14 in all, and the
// floor held 16m17s. Each further write of the setting starts the count again.
func TestASettingWrittenDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes(t *testing.T) {
	for _, test := range []struct {
		name string
		// another names another daemon, which stalls as INFRA's does, in the setting written.
		another bool
	}{
		{"the same daemon", false},
		{"another daemon that does not answer", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const failures = stateMaxHolds + 1
			const before = 4
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

			stall := func() { rig.clock.advance(RequestTimeout) }
			daemon.stalls(stall)
			from := rig.clock.now()
			rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "merging")))
			holder := rig.lastStreamSeq()
			rig.waitFor(time.Minute, func() bool { return len(rig.logRecords(heldLine)) >= before }, func() string {
				return fmt.Sprintf("the message went back %d times, want %d before the setting's write", len(rig.logRecords(heldLine)), before)
			})
			named := daemon
			if test.another {
				named = newFakeDaemon(t, "")
				named.stalls(stall)
			}
			if _, err := PutSettings(context.Background(), rig.database.Pool, "INFRA", named.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
				t.Fatalf("write INFRA's setting again: %v", err)
			}
			rig.wake <- struct{}{}

			var info *natsgo.ConsumerInfo
			rig.waitFor(2*time.Minute, func() bool {
				info = rig.consumerInfo()
				return info.AckFloor.Stream >= holder && info.NumAckPending == 0 && info.NumPending == 0
			}, func() string {
				return fmt.Sprintf("the message that holds INFRA's read is not acknowledged after %d hand-backs: %+v", len(rig.logRecords(heldLine)), info)
			})
			held := rig.clock.now().Sub(from)
			handBacks := rig.heldValues(holder, "handBack")
			reads := time.Duration(failures+1) * RequestTimeout
			if len(handBacks) > stateMaxHolds || held >= reads+time.Minute {
				t.Errorf("the message went back %d times, counted %v, and held the ack floor for %s on the intake's clock across one write of the setting, want at most %d hand-backs and less than %s, its %d reads, the write's own read and a minute: the write deleted the cursor and its count of the hold's hand-backs",
					len(handBacks), handBacks, held.Round(time.Second), stateMaxHolds, reads+time.Minute, failures)
			}
		})
	}
}
