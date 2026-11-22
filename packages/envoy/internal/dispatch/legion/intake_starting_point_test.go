package legion

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testwait"
)

// TestTheWholeReadLineNamesTheSequenceUnderDispatchsOwnHandler: each whole read is logged with its
// reason and the daemon.stateSeq it read (wholeReadLine), and cmd/dispatch logs through a text
// handler (main.go's slog.NewTextHandler), not the JSON handler the other tests here install. The
// line a person reads in Dispatch's log names the sequence as a number.
func TestTheWholeReadLineNamesTheSequenceUnderDispatchsOwnHandler(t *testing.T) {
	logs := &logtest.Buffer{}
	logtest.SetDefault(t, slog.NewTextHandler(logs, nil))
	daemon := newFakeDaemon(t, daemonState("INFRA", 15, planning("INFRA")))
	database, setting := configured(t, daemon.URL)
	if err := ReadWhole(t.Context(), database.Pool, setting, ReadReasonStart); err != nil {
		t.Fatalf("read INFRA's daemon whole: %v", err)
	}
	line := logs.Lines(wholeReadLine)
	if !strings.Contains(line, "stateSeq=15 ") {
		t.Errorf("the whole read's line under Dispatch's text handler is %q, want it to name stateSeq=15", strings.TrimSpace(line))
	}
}

// TestAMessageOfADaemonTheSettingNoLongerNamesLeavesTheNamedDaemonsEventsApplied: R3 reads a
// project whole when its Legion setting names another daemon, so that the observations follow
// the daemon the setting names from that read on. A message the daemon named before published
// while the setting still named it reaches Dispatch after the move, as one does that was in
// flight, held back by a take-over, or redelivered. That daemon's counter is its own, far above
// the new daemon's, whose database is new. The message is no event of the daemon the setting names
// and writes nothing that daemon did, and the events the named daemon publishes after it are
// applied, each one in turn, with no event skipped.
func TestAMessageOfADaemonTheSettingNoLongerNamesLeavesTheNamedDaemonsEventsApplied(t *testing.T) {
	rig := newIntakeRig(t)
	previous := newFakeDaemon(t, daemonState("INFRA", 2000000, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", previous.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	requireLastSeq(t, rig.database, "INFRA", 2000000)

	// The setting moves to a daemon on a database of its own, five events in.
	named := newFakeDaemon(t, daemonState("INFRA", 5, strings.Replace(planning("INFRA"), `"planning"`, `"implementing"`, 1)))
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", named.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
		t.Fatalf("point INFRA at the named daemon: %v", err)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(2)
	requireLastSeq(t, rig.database, "INFRA", 5)

	// A message of the daemon the setting no longer names, under that daemon's run.
	rig.publishOf("INFRA", "0b6f2d1e-7c4a-4e93-a8d5-3f1b9e6c2a70", time.Now().UTC().Truncate(time.Microsecond),
		change(2000001, "INFRA-1", moved("phase", "merging")))
	rig.settled()
	// The named daemon's next events.
	rig.publish("INFRA", change(6, "INFRA-1", moved("phase", "testing")))
	rig.publish("INFRA", change(7, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()

	if phase := currentValues(t, rig.database, "INFRA")["INFRA-1/phase"]; phase != `"reviewing"` {
		t.Errorf("INFRA-1's phase is %s, want the named daemon's newest event's reviewing: its events after the other daemon's message were skipped", phase)
	}
	for _, value := range history(t, rig.database, "INFRA", "INFRA-1", FieldPhase) {
		if value == `"merging"` {
			t.Errorf("INFRA-1's phase history %v holds merging, which only the daemon the setting no longer names wrote", history(t, rig.database, "INFRA", "INFRA-1", FieldPhase))
			break
		}
	}
	requireLastSeq(t, rig.database, "INFRA", 7)
}

// TestAWholeReadOfADaemonThatRunsNoWorkflowNamesThatReason: R2's last sentence. A daemon with no
// dispatch_url runs no outbox, so it never commits a state row and serves daemon.stateSeq 0, as
// the daemon's state route does for a project with no state row (records' StateSeq). Dispatch
// records each whole read of such a daemon as a failed read that names that reason: its settings
// carry no dispatch_url, so it runs no workflow.
func TestAWholeReadOfADaemonThatRunsNoWorkflowNamesThatReason(t *testing.T) {
	rig := newIntakeRig(t)
	noWorkflow := strings.Replace(daemonState("INFRA", 0, planning("INFRA")), `"dispatch_url":"http://dispatch.internal.example",`, ``, 1)
	daemon := newFakeDaemon(t, noWorkflow)
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitWholeReads(1)

	reads := readRecords(t, rig.database, "INFRA")
	requireWholeRead(t, reads[0], ReadReasonStart, false)
	if reads[0].error == nil || !strings.Contains(*reads[0].error, ErrNoDispatchURL.Error()) {
		t.Errorf("the whole read of a daemon that runs no workflow recorded the error %v, want it to name that reason: %q", orNone(reads[0].error), ErrNoDispatchURL.Error())
	}
}

// TestAFailedWholeReadSetsNoRun: only a good whole read takes the run that served it as the
// cursor's run (recordWhole). A read that fails, here one answered with another project's state
// served by another run, keeps the run of the cursor's last good read: a cursor that took the
// failed read's run would take a later boot of that run, at or below the cursor, for one it already
// holds, and read nothing where a daemon whose counter went back needs a whole read.
func TestAFailedWholeReadSetsNoRun(t *testing.T) {
	const otherRun = "9e3c5a71-2f4d-4b86-a0c9-6d1e8b7f3a52"
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	// A gap whose read the daemon refuses: the read is owed under the rig's run, and the message
	// that called for it holds the read. The refused read lasts a minute on the intake's clock
	// (lagsNext), so the read is due again once it has failed, and the message goes back to the
	// stream for holdAtLeast alone. The read its redelivery makes waits for retry, so the cursor is
	// checked as the refused read left it.
	other := strings.Replace(daemonState("INFRA", 25, planning("INFRA")), rigRun, otherRun, 1)
	other = strings.Replace(other, `"daemon":{"project":"INFRA"`, `"daemon":{"project":"LINUX"`, 1)
	daemon.serves(other)
	daemon.refusesFor(1, http.StatusServiceUnavailable)
	retry := make(chan struct{})
	release := sync.OnceFunc(func() { close(retry) })
	t.Cleanup(release)
	daemon.lagsNext(func() {
		rig.clock.advance(ReadRetryAfter)
		daemon.lagsNext(func() { <-retry })
	})
	rig.publish("INFRA", change(20, "INFRA-1", moved("phase", "implementing")))
	rig.settledHolding(1)
	if c := cursorOf(t, rig.database, "INFRA"); !c.readOwed || c.run == nil || *c.run != rigRun {
		t.Fatalf("after the refused gap read the cursor is %+v under the run %s, want a read owed under %s", c, orNone(c.run), rigRun)
	}

	// The owed read, which the message's redelivery makes a minute after the refused read began,
	// is answered with another project's state, which another run serves: a failed read.
	release()
	rig.awaitWholeReads(3)

	reads := readRecords(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonRetry, false)
	if c := cursorOf(t, rig.database, "INFRA"); c.run == nil || *c.run != rigRun || !c.readOwed {
		t.Errorf("after a failed read served by %s the cursor follows the run %s with a read owed %v, want %s, its last good read's, and the read still owed",
			otherRun, orNone(c.run), c.readOwed, rigRun)
	}
}

// TestATakeOverReadsAProjectWhoseOwedReadNoMessageHolds: each take-over queues every configured
// project's starting point (takenOver, SettingsChanged, startingPoint). Where a message holds the
// project's owed read, that call joins the read and makes none of its own
// (TestATakeOverDuringAHeldReadThatTimesOutKeepsTheFloorAboutElevenMinutes); where no message holds
// it, the take-over reads. A project whose read is owed and due and that no message holds is read
// at the take-over for retry: after a failed starting read, which no message makes, and after the
// message that held the read went back stateMaxHolds times and was let go. A project with no
// cursor, here one whose setting was written while no settings wake reached the intake, is read
// whole at the take-over (ReadReasonStart). Each is read once at the take-over, and the good read
// leaves its cursor at the read's stateSeq with nothing owed and no holder. A project whose cursor
// owes nothing, after a good starting read, is not read at the take-over: R3 reads a project whole
// only for the reasons it names.
func TestATakeOverReadsAProjectWhoseOwedReadNoMessageHolds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		project string
		// reason is the reason of the take-over's read, "" for a project the take-over does not
		// read.
		reason string
		// before brings project to what the take-over finds, from the first intake's start on,
		// and answers project's daemon and how many whole reads of every project are recorded by
		// then.
		before func(t *testing.T, rig *intakeRig) (daemon *fakeDaemon, reads int)
	}{
		{
			name: "a read owed since a failed starting read", project: "INFRA", reason: ReadReasonRetry,
			before: func(t *testing.T, rig *intakeRig) (*fakeDaemon, int) {
				daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
				daemon.refuses(http.StatusServiceUnavailable)
				seedLegionProject(t, rig.database, "INFRA", daemon.URL)
				rig.start()
				rig.awaitWholeReads(1)
				requireWholeRead(t, wholeReadsOf(t, rig.database, "INFRA")[0], ReadReasonStart, false)
				requireOwedWithNoHolder(t, rig, "INFRA")
				return daemon, 1
			},
		},
		{
			name: "a read owed since its holder was let go", project: "INFRA", reason: ReadReasonRetry,
			before: func(t *testing.T, rig *intakeRig) (*fakeDaemon, int) {
				// As in TestAHeldReadThatKeepsFailingLetsItsMessageGoAfterTenHandBacks: a gap's
				// message holds the read, the daemon refuses each read the message's deliveries
				// make, and the message is acknowledged after stateMaxHolds hand-backs.
				daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
				seedLegionProject(t, rig.database, "INFRA", daemon.URL)
				rig.start()
				rig.awaitStartingRead("INFRA")
				daemon.refuses(http.StatusServiceUnavailable)
				rig.clock.jumps(ReadRetryAfter - holdAtLeast)
				rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "merging")))
				holder := rig.lastStreamSeq()
				rig.waitFor(time.Minute, func() bool {
					info := rig.consumerInfo()
					return info.AckFloor.Stream >= holder && info.NumAckPending == 0 && info.NumPending == 0
				}, func() string {
					return fmt.Sprintf("the message that holds INFRA's read is not acknowledged after %d hand-backs", len(rig.logRecords(heldLine)))
				})
				rig.clock.jumps(0)
				if released := rig.awaitLogRecords(releasedLine, 1); len(released) != 1 || released[0]["streamSeq"] != float64(holder) {
					t.Fatalf("the release lines = %v, want one naming the message at %d", released, holder)
				}
				requireOwedWithNoHolder(t, rig, "INFRA")
				return daemon, stateMaxHolds + 2
			},
		},
		{
			name: "a project with no cursor", project: "LINUX", reason: ReadReasonStart,
			before: func(t *testing.T, rig *intakeRig) (*fakeDaemon, int) {
				infra := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
				seedLegionProject(t, rig.database, "INFRA", infra.URL)
				rig.start()
				rig.awaitStartingRead("INFRA")
				linux := newFakeDaemon(t, daemonState("LINUX", 20, planning("LINUX")))
				seedLegionProject(t, rig.database, "LINUX", linux.URL)
				return linux, 1
			},
		},
		{
			name: "a project that owes nothing", project: "INFRA", reason: "",
			before: func(t *testing.T, rig *intakeRig) (*fakeDaemon, int) {
				daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
				seedLegionProject(t, rig.database, "INFRA", daemon.URL)
				rig.start()
				rig.awaitStartingRead("INFRA")
				if c := cursorOf(t, rig.database, "INFRA"); c.readOwed || c.lastSeq == nil || *c.lastSeq != 10 {
					t.Fatalf("INFRA's cursor after its good starting read is %s, want it at 10 with nothing owed", c)
				}
				return daemon, 1
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newIntakeRig(t)
			daemon, reads := tc.before(t, rig)

			// A minute on, so that an owed read is due, a second intake takes the durable over.
			served := daemon.served()
			daemon.serves(daemonState(tc.project, 30, planning(tc.project)))
			rig.clock.advance(ReadRetryAfter + time.Second)
			rig.stop()
			rig.start()
			if tc.reason == "" {
				if testwait.Holds(3*time.Second, 25*time.Millisecond, func() bool { return daemon.served() > served }) {
					t.Errorf("%s's daemon served %d reads at the take-over, want none: its cursor owes no read", tc.project, daemon.served()-served)
				}
				if c := cursorOf(t, rig.database, tc.project); c.readOwed || c.lastSeq == nil || *c.lastSeq != 10 {
					t.Errorf("after the take-over %s's cursor is %s, want it at its starting read's 10 with nothing owed", tc.project, c)
				}
				return
			}
			rig.awaitWholeReads(reads + 1)

			got := wholeReadsOf(t, rig.database, tc.project)
			requireWholeRead(t, got[len(got)-1], tc.reason, true)
			if n := daemon.served(); n != served+1 {
				t.Errorf("%s's daemon served %d reads at the take-over, want 1", tc.project, n-served)
			}
			if c := cursorOf(t, rig.database, tc.project); c.readOwed || c.readHolder != nil || c.readHolds != 0 || c.lastSeq == nil || *c.lastSeq != 30 {
				t.Errorf("after the take-over's read %s's cursor is %s, want it at the read's 30 with nothing owed and no holder", tc.project, c)
			}
		})
	}
}

// requireOwedWithNoHolder fails unless project's cursor owes a read for retry alone and names no
// message as its holder, with no hand-back counted.
func requireOwedWithNoHolder(t *testing.T, rig *intakeRig, project string) {
	t.Helper()
	if c := cursorOf(t, rig.database, project); !slices.Equal(c.readReasons, []string{ReadReasonRetry}) || c.readHolder != nil || c.readHolds != 0 {
		t.Fatalf("%s's cursor is %s, want the read owed for retry, no holder and no hand-back counted", project, c)
	}
}
