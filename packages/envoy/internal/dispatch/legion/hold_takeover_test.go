package legion

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/testwait"
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
// hand-backs, and no sequence of writes of the project's own setting lengthens it. After INFRA's
// good starting read its daemon stops answering, as in
// TestAHeldReadThatTimesOutHoldsTheFloorAboutElevenMinutes, and a gap's message holds the read.
// During the count a person writes INFRA's setting:
//   - naming the same daemon, once or three times. R3 says a project has at most one whole read in
//     flight and at most one owed, and a reason that arrives while one is in flight or owed joins
//     it. So each write's read joins the held read, as a take-over's does, and the count goes on:
//     the message is acknowledged after stateMaxHolds hand-backs in all, and on the intake's clock
//     it held the floor for less than its 11 reads and a minute.
//   - naming another daemon, which does not answer either. R3 applies no message of the daemon the
//     setting named before once the setting names another, so the held message, the old daemon's,
//     is finished at its next delivery after the write, unapplied, and goes back no more: its
//     delay is holdAtLeast in the rig, so the floor is released within seconds of the write. The
//     new daemon's starting read is made at once, and no message holds it.
//
// At f77fd4c7 each write of the setting deletes the project's cursor (settingWritten), and the
// count of the hold's hand-backs with it (read_holds), so the message's next redelivery holds the
// read again from 1: the tester measured hand-backs 1 to 4 and then 1 to 10, 14 in all, and the
// floor held 16m17s. Each further write of the setting starts the count again.
func TestASettingWrittenDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes(t *testing.T) {
	for _, test := range []struct {
		name string
		// writes lists, for each write of the setting, how many hand-backs the message has made
		// at the least before it.
		writes []int
		// another names another daemon, which stalls as INFRA's does, in the setting written.
		another bool
	}{
		{"the same daemon", []int{4}, false},
		{"the same daemon three times", []int{2, 4, 6}, false},
		{"another daemon that does not answer", []int{4}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const failures = stateMaxHolds + 1
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
			named := daemon
			if test.another {
				named = newFakeDaemon(t, "")
				named.stalls(stall)
			}
			var wroteAt time.Time
			var atWrite int
			for i, at := range test.writes {
				rig.waitFor(time.Minute, func() bool { return len(rig.logRecords(heldLine)) >= at }, func() string {
					return fmt.Sprintf("the message went back %d times, want %d before write %d of the setting", len(rig.logRecords(heldLine)), at, i+1)
				})
				atWrite, wroteAt = len(rig.logRecords(heldLine)), time.Now()
				if _, err := PutSettings(context.Background(), rig.database.Pool, "INFRA", named.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
					t.Fatalf("write INFRA's setting again: %v", err)
				}
				rig.wake <- struct{}{}
			}

			var info *natsgo.ConsumerInfo
			acknowledged := func() bool {
				info = rig.consumerInfo()
				return info.AckFloor.Stream >= holder && info.NumAckPending == 0 && info.NumPending == 0
			}
			if test.another {
				const within = 10 * time.Second
				if !testwait.Holds(within, 25*time.Millisecond, acknowledged) {
					t.Fatalf("the message of the daemon the setting named before was not finished within %s of the write that named another daemon, %d hand-backs in all, %d at the write: %+v",
						within, len(rig.logRecords(heldLine)), atWrite, info)
				}
				if after := len(rig.heldValues(holder, "handBack")) - atWrite; after > 1 {
					t.Errorf("the message of the daemon the setting named before went back %d more times after the write that named another daemon, want at most the one in flight at the write", after)
				}
				if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); slices.Contains(got, `"merging"`) {
					t.Errorf("the phase's history is %v: the message of the daemon the setting named before was applied", got)
				}
				if named.served() < 1 {
					t.Errorf("the daemon the setting names was never read")
				}
				t.Logf("finished %s after the write, %d hand-backs in all", time.Since(wroteAt).Round(time.Millisecond), len(rig.logRecords(heldLine)))
				return
			}
			rig.waitFor(2*time.Minute, acknowledged, func() string {
				return fmt.Sprintf("the message that holds INFRA's read is not acknowledged after %d hand-backs: %+v", len(rig.logRecords(heldLine)), info)
			})
			held := rig.clock.now().Sub(from)
			handBacks := rig.heldValues(holder, "handBack")
			reads := time.Duration(failures) * RequestTimeout
			if len(handBacks) > stateMaxHolds || held >= reads+time.Minute {
				t.Errorf("the message went back %d times, counted %v, and held the ack floor for %s on the intake's clock across %d write(s) of the setting, want at most %d hand-backs and less than %s, its %d reads and a minute: each write's read joins the held read",
					len(handBacks), handBacks, held.Round(time.Second), len(test.writes), stateMaxHolds, reads+time.Minute, failures)
			}
		})
	}
}

// TestASettingNamingAnotherDaemonFinishesTheOldDaemonsMessagesWithNoRead: R3 applies no message
// of the daemon a setting named before once it names another, and a failed starting read is one
// "which no message holds". So, after a write that names another daemon, the old daemon's held
// message is finished unapplied at its next redelivery, once the write's starting read of the new
// daemon, which it waits behind on the project's runner, has ended: within one RequestTimeout and a
// second of the write. It makes no read of its own or of the new daemon, and holds none. Once the
// new daemon's failed starting read is due again, a later message of the old daemon's store is
// acknowledged unapplied the same way, within finishedWithin of its publish: it waits on no read
// and holds none. Until a read of the new URL has succeeded, Dispatch cannot know whether the URL
// names another store or the same store behind a new URL, so that message calls for the owed
// read, which the project's runner makes after the message is acknowledged, and it calls for no
// more than that one read.
//
// The rig's client window is readWindow, long enough to tell a message that waits behind a read,
// or makes one, from one that does neither. INFRA's daemon stops answering after its good starting
// read, and a gap's message holds the owed read. After two hand-backs a person writes INFRA's
// setting naming another daemon, which does not answer either. The old message's next delivery
// comes holdAtLeast after its last hand-back and waits behind the write's starting read, which
// waits out readWindow, so it is finished within readWindow and a second of the write, and the new
// daemon has served the write's own starting read alone.
//
// At 3e6a6784 the old message's redelivery waits on the project's runner behind the write's
// starting read of the new daemon, then makes the new daemon's read owed for retry
// (outcomeForgotten calls readFor as a call no message makes), and is finished only after it: on
// the real programs 2m0s after the write. A later message of the old store makes that read too
// once it is due, before it is acknowledged.
func TestASettingNamingAnotherDaemonFinishesTheOldDaemonsMessagesWithNoRead(t *testing.T) {
	const readWindow = 5 * time.Second
	const finishedWithin = 3 * time.Second
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = readWindow
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
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "merging")))
	holder := rig.lastStreamSeq()
	rig.waitFor(2*time.Minute, func() bool { return len(rig.logRecords(heldLine)) >= 2 }, func() string {
		return fmt.Sprintf("the message went back %d times, want 2 before the write", len(rig.logRecords(heldLine)))
	})
	named := newFakeDaemon(t, "")
	named.stalls(stall)
	wroteAt := time.Now()
	if _, err := PutSettings(context.Background(), rig.database.Pool, "INFRA", named.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
		t.Fatalf("point INFRA at another daemon: %v", err)
	}
	rig.wake <- struct{}{}
	finishedThrough := func(seq uint64) func() bool {
		return func() bool {
			info := rig.consumerInfo()
			return info.AckFloor.Stream >= seq && info.NumAckPending == 0 && info.NumPending == 0
		}
	}
	rig.waitFor(time.Minute, finishedThrough(holder), func() string {
		return fmt.Sprintf("the old daemon's held message is not finished a minute after the write: %+v", rig.consumerInfo())
	})
	if took := time.Since(wroteAt); took >= readWindow+time.Second {
		t.Errorf("the old daemon's held message was finished %s after the write naming another daemon, want less than %s, the write's starting read of %s and a second: it waited behind more than that read, or made one",
			took.Round(time.Millisecond), readWindow+time.Second, readWindow)
	}
	if served := named.served(); served != 1 {
		t.Errorf("the daemon the setting names served %d reads by the time the old daemon's message was finished, want 1, the write's own starting read: the old daemon's message made a read", served)
	}

	// The new daemon's failed starting read is due again a minute on. A later message of the old
	// daemon's store is acknowledged unapplied within finishedWithin of its publish, and at most
	// one read of the new daemon follows it while the new URL is unverified.
	rig.waitFor(time.Minute, func() bool { return cursorOf(t, rig.database, "INFRA").readOwed }, func() string {
		return fmt.Sprintf("the new daemon's starting read has not failed a minute after the write: INFRA's cursor is %s", cursorOf(t, rig.database, "INFRA"))
	})
	if c := cursorOf(t, rig.database, "INFRA"); !slices.Equal(c.readReasons, []string{ReadReasonRetry}) || c.stateID != nil || c.readHolder != nil {
		t.Fatalf("INFRA's cursor after the new daemon's failed starting read is %s, want the read owed for retry, no store and no holder", c)
	}
	rig.clock.advance(ReadRetryAfter + time.Second)
	before := named.served()
	published := time.Now()
	rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "testing")))
	later := rig.lastStreamSeq()
	rig.waitFor(time.Minute, finishedThrough(later), func() string {
		return fmt.Sprintf("the later message of the old daemon's store is not finished: %+v", rig.consumerInfo())
	})
	if took := time.Since(published); took >= finishedWithin {
		t.Errorf("the later message of the old daemon's store was finished %s after its publish, want less than %s: it waited behind a read, or made one, of %s",
			took.Round(time.Millisecond), finishedWithin, readWindow)
	}
	if testwait.Holds(readWindow+2*time.Second, 25*time.Millisecond, func() bool { return named.served() > before+1 }) {
		t.Errorf("the daemon the setting names served %d reads after the later message of the old daemon's store, want %d at the most: that message called for more than the one owed read", named.served(), before+1)
	}
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); slices.Contains(got, `"merging"`) || slices.Contains(got, `"testing"`) {
		t.Errorf("the phase's history is %v: a message of the daemon the setting named before was applied", got)
	}
}

// TestAnEventOfTheSameStoreDuringTheReadThatVerifiesANewURLIsApplied: R3 applies every event of
// the daemon its setting names, and a project that goes quiet is at most one minute behind while
// its reads succeed. A setting written to a new URL that serves the same store (the daemon moved)
// is unverified until a read of that URL succeeds. While that read is in flight, the daemon
// commits and publishes an event past the read's snapshot. The read then lands, verifies the
// store, and the project goes quiet. The event is applied within about a minute of the read.
//
// INFRA's daemon is read whole at 10. The setting names a second URL that serves INFRA's own
// store at 11, and the test holds that answer. While it is held, the daemon publishes 12. The test
// lets the answer go, and the read verifies the store at 11. Then nothing is published: on the
// intake's clock a minute passes, and INFRA-1's phase reaches 12's testing within 75 s.
//
// At 86ded796 the intake takes 12 as a message of the store the setting named before, since no
// read of the new URL has succeeded, and acknowledges it unapplied beside the project's runner
// while the read is in flight. The read verifies the store at 11 and clears the record, and
// nothing applies 12 or owes the read that would: INFRA stays at 11 until its next message.
func TestAnEventOfTheSameStoreDuringTheReadThatVerifiesANewURLIsApplied(t *testing.T) {
	implementing := strings.Replace(planning("INFRA"), `"planning"`, `"implementing"`, 1)
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	relocated := newFakeDaemon(t, daemonState("INFRA", 11, implementing))
	release := relocated.holds(t)
	if _, err := PutSettings(context.Background(), rig.database.Pool, "INFRA", relocated.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
		t.Fatalf("point INFRA at the URL its daemon moved to: %v", err)
	}
	rig.wake <- struct{}{}
	rig.waitFor(20*time.Second, func() bool { return relocated.served() >= 1 }, func() string {
		return "the write's starting read of the URL the daemon moved to never began"
	})

	// The event past the read's snapshot reaches the intake while the read is held.
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "testing")))
	rig.waitFor(20*time.Second, func() bool { return rig.consumerInfo().NumPending == 0 }, func() string {
		return fmt.Sprintf("the message at 12 was not delivered while the read was held: %+v", rig.consumerInfo())
	})
	time.Sleep(time.Second)
	release()
	rig.awaitWholeReads(2)
	if c := cursorOf(t, rig.database, "INFRA"); c.stateID == nil || *c.stateID != rigStateID {
		t.Fatalf("control: INFRA's cursor after the read of the URL the daemon moved to is %s, want the store %s it serves", c, rigStateID)
	}

	rig.clock.advance(ReadRetryAfter + time.Second)
	if !testwait.Holds(75*time.Second, 200*time.Millisecond, func() bool {
		return slices.Contains(history(t, rig.database, "INFRA", "INFRA-1", FieldPhase), `"testing"`)
	}) {
		t.Errorf("75 s after the read that verified the store, with the project quiet, INFRA-1's phase history is %v and INFRA's cursor is %s: the event at 12, published past the read's snapshot while it was in flight, was never applied and nothing owes the read that would",
			history(t, rig.database, "INFRA", "INFRA-1", FieldPhase), cursorOf(t, rig.database, "INFRA"))
	}
}
