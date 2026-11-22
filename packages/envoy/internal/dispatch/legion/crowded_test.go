package legion

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/testwait"
)

// lastSeqOf is project's cursor's last_seq, 0 for none.
func lastSeqOf(t *testing.T, rig *intakeRig, project string) int64 {
	t.Helper()
	var last int64
	if err := rig.database.Pool.QueryRow(context.Background(),
		`select coalesce(max(last_seq), 0) from legion_event_cursors where project = $1`, project).Scan(&last); err != nil {
		t.Fatalf("read the cursor of %s: %v", project, err)
	}
	return last
}

// TestFifteenProjectsInSlowReadsLeaveTheWindowAMessageOfAnotherProject: R3 holds back no project
// behind another project's read, and each project that owes a read holds at most one message of
// the consumer's window. A project keeps at most stateMaxWaiting messages waiting on its runner,
// the one in a read included; past that, one message holds the read it owes and goes back to the
// stream, and the others are acknowledged (judgeCrowded). So a project in a slow read holds
// stateMaxWaiting+1 messages of the durable's window of stateMaxAckPending, 17 of 256, and fifteen
// such projects hold 255. Fifteen projects each make a gap read that their daemon holds, and each
// publishes stateMaxWaiting+2 more messages while that read is held. A message of a sixteenth
// project is then applied in its usual time, and no slow project's daemon is asked for another
// read.
func TestFifteenProjectsInSlowReadsLeaveTheWindowAMessageOfAnotherProject(t *testing.T) {
	const slow = 15
	const usual = 3 * time.Second
	window := slow * (stateMaxWaiting + 1)
	rig := newIntakeRig(t)
	projects := make([]string, slow)
	daemons := make([]*fakeDaemon, slow)
	for i := range slow {
		projects[i] = fmt.Sprintf("SLOW%02d", i+1)
		daemons[i] = newFakeDaemon(t, daemonState(projects[i], 10, planning(projects[i])))
		seedLegionProject(t, rig.database, projects[i], daemons[i].URL)
	}
	quick := newFakeDaemon(t, daemonState("QUICK", 10, planning("QUICK")))
	seedLegionProject(t, rig.database, "QUICK", quick.URL)
	rig.start()
	rig.awaitWholeReads(slow + 1)

	releases := make([]func(), slow)
	for i, daemon := range daemons {
		releases[i] = daemon.holds(t)
	}
	for _, project := range projects {
		rig.publish(project, change(12, project+"-1", moved("phase", "testing")))
	}
	rig.waitFor(30*time.Second, func() bool {
		return !slices.ContainsFunc(daemons, func(daemon *fakeDaemon) bool { return daemon.served() < 2 })
	}, func() string { return "a slow project's gap read never began" })
	for _, project := range projects {
		for seq := 13; seq < 13+stateMaxWaiting+2; seq++ {
			rig.publish(project, change(seq, project+"-1", moved("phase", "testing")))
		}
	}
	rig.waitFor(30*time.Second, func() bool { return rig.consumerInfo().NumAckPending >= window }, func() string {
		return fmt.Sprintf("the slow projects' messages did not reach the intake: %+v", rig.consumerInfo())
	})

	published := time.Now()
	rig.publish("QUICK", change(11, "QUICK-1", moved("phase", "implementing")))
	if !testwait.Holds(usual, 25*time.Millisecond, func() bool { return lastSeqOf(t, rig, "QUICK") >= 11 }) {
		t.Errorf("QUICK's message was not applied within %s of its publish while fifteen projects were in slow reads: the window is full: %+v",
			usual, rig.consumerInfo())
	} else {
		t.Logf("QUICK's message was applied %s after its publish", time.Since(published).Round(time.Millisecond))
	}
	if held := rig.consumerInfo().NumAckPending; held != window {
		t.Errorf("the slow projects hold %d messages of the window, want %d: %d waiting on each runner and one held each", held, window, stateMaxWaiting)
	}
	if holds := rig.logRecords(heldLine); len(holds) != slow {
		t.Errorf("%d messages went back to the stream, want one for each slow project: %v", len(holds), holds)
	}
	for i, daemon := range daemons {
		if served := daemon.served(); served != 2 {
			t.Errorf("%s's daemon served %d reads, want its starting read and its gap read alone", projects[i], served)
		}
	}
	for _, release := range releases {
		release()
	}
}

// TestAProjectPastTheCapDuringAGoodReadThatGoesQuietIsReadWithinTheMinute: R3 keeps a project that
// goes quiet at most one minute behind while its reads succeed. INFRA's gap read at 12 is in flight,
// held by its daemon, which serves 30. Near the end of that read's minute on the intake's clock,
// INFRA publishes 15 messages at or below 30, which wait on INFRA's runner behind the read, and
// then 31 to 33, past the read's snapshot and past the cap of stateMaxWaiting messages: each owes
// a gap read, which the read in flight does not clear (recordWhole), the first of them holds it and
// goes back to the stream until the minute ends, and the other two join it and are acknowledged.
// The read succeeds at 30, and INFRA goes quiet. Within the time left of the minute and a few
// seconds, a gap read is made, and INFRA-1's phase reaches 33's reviewing.
func TestAProjectPastTheCapDuringAGoodReadThatGoesQuietIsReadWithinTheMinute(t *testing.T) {
	// left is what is left of the read's minute when the messages past the cap arrive.
	const left = 5 * time.Second
	phase := func(phase string) string {
		return strings.Replace(planning("INFRA"), `"planning"`, fmt.Sprintf("%q", phase), 1)
	}
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	release := daemon.holds(t)
	daemon.serves(daemonState("INFRA", 30, phase("implementing")))
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "implementing")))
	rig.waitFor(20*time.Second, func() bool { return daemon.served() >= 2 }, func() string {
		return "INFRA's gap read never began"
	})
	// The read in flight serves the state at 30, which it took as it began; any later read serves 33.
	daemon.serves(daemonState("INFRA", 33, phase("reviewing")))
	rig.clock.advance(ReadRetryAfter - left)
	for seq := 13; seq < 13+stateMaxWaiting-1; seq++ {
		rig.publish("INFRA", change(seq, "INFRA-1", moved("phase", "implementing")))
	}
	for seq, past := range []string{"testing", "merging", "reviewing"} {
		rig.publish("INFRA", change(31+seq, "INFRA-1", moved("phase", past)))
	}
	rig.waitFor(20*time.Second, func() bool {
		info := rig.consumerInfo()
		return info.NumPending == 0 && info.NumAckPending == stateMaxWaiting+1
	}, func() string {
		return fmt.Sprintf("INFRA holds %+v, want %d messages waiting on its runner and one held", rig.consumerInfo(), stateMaxWaiting)
	})

	release()
	read := time.Now()
	if !testwait.Holds(left+15*time.Second, 100*time.Millisecond, func() bool {
		return currentValues(t, rig.database, "INFRA")["INFRA-1/phase"] == `"reviewing"`
	}) {
		t.Fatalf("%s after the read at 30, with INFRA quiet, INFRA-1's phase is %s and INFRA's cursor is %s: the events past the cap were never read",
			time.Since(read).Round(time.Second), currentValues(t, rig.database, "INFRA")["INFRA-1/phase"], cursorOf(t, rig.database, "INFRA"))
	}
	rig.awaitWholeReads(3)
	reads := wholeReadsOf(t, rig.database, "INFRA")
	if len(reads) != 3 {
		t.Fatalf("INFRA's whole reads are %v, want the starting read, the gap read at 30 and one more", reads)
	}
	requireWholeRead(t, reads[1], ReadReasonGap, true)
	requireWholeRead(t, reads[2], ReadReasonGap, true)
	if testwait.Holds(2*time.Second, 25*time.Millisecond, func() bool { return daemon.served() > 3 }) {
		t.Errorf("INFRA's daemon served %d reads, want the starting read and two gap reads", daemon.served())
	}
}

// TestMessagesPastTheCapOfAProjectWithNoCursorHoldOneMessageAndAreReadOnTheRunner: row 8.1 for a
// project whose setting was written while no settings wake reached the intake, so it has no cursor
// yet. Its first message makes the starting read on the project's runner, and INFRA's daemon holds
// the answer, a state at 30. While that read is held, near the end of its minute on the intake's
// clock, INFRA publishes 15 more messages at or below 30, which wait on the runner behind it, and
// then 31 to 33, past the read's snapshot and past the cap of stateMaxWaiting. Those make no read
// beside the runner: the project is given a cursor that follows no daemon, which records the gap
// read they owe, the first of them holds that read and the other two are acknowledged, so INFRA
// holds stateMaxWaiting+1 messages of the window. Once the starting read lands, the gap read is
// made on the runner too, and INFRA-1's phase reaches 33's reviewing.
func TestMessagesPastTheCapOfAProjectWithNoCursorHoldOneMessageAndAreReadOnTheRunner(t *testing.T) {
	// left is what is left of the starting read's minute when the messages past the cap arrive.
	const left = 5 * time.Second
	phase := func(phase string) string {
		return strings.Replace(planning("INFRA"), `"planning"`, fmt.Sprintf("%q", phase), 1)
	}
	rig := newIntakeRig(t)
	rig.start()
	daemon := newFakeDaemon(t, daemonState("INFRA", 30, phase("implementing")))
	release := daemon.holds(t)
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	var cursors int
	if err := rig.database.Pool.QueryRow(t.Context(), `select count(*) from legion_event_cursors where project = 'INFRA'`).Scan(&cursors); err != nil || cursors != 0 {
		t.Fatalf("INFRA has %d cursors before its first message (%v), want none", cursors, err)
	}

	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	rig.waitFor(20*time.Second, func() bool { return daemon.served() >= 1 }, func() string {
		return "INFRA's first message never made the starting read"
	})
	// The read in flight serves the state at 30, which it took as it began; any later read serves 33.
	daemon.serves(daemonState("INFRA", 33, phase("reviewing")))
	rig.clock.advance(ReadRetryAfter - left)
	for seq := 12; seq < 12+stateMaxWaiting-1; seq++ {
		rig.publish("INFRA", change(seq, "INFRA-1", moved("phase", "implementing")))
	}
	for seq, past := range []string{"testing", "merging", "reviewing"} {
		rig.publish("INFRA", change(31+seq, "INFRA-1", moved("phase", past)))
	}
	rig.waitFor(20*time.Second, func() bool {
		info := rig.consumerInfo()
		return info.NumPending == 0 && info.NumAckPending == stateMaxWaiting+1
	}, func() string {
		return fmt.Sprintf("INFRA holds %+v and %d messages went back, want %d messages waiting on its runner and one held",
			rig.consumerInfo(), len(rig.logRecords(heldLine)), stateMaxWaiting)
	})
	if served := daemon.served(); served != 1 {
		t.Errorf("INFRA's daemon served %d reads while its starting read was held, want that read alone: a message past the cap made a read beside the runner", served)
	}
	if holds := rig.logRecords(heldLine); len(holds) != 1 || holds[0]["seq"] != float64(31) || holds[0]["reason"] != ReadReasonGap {
		t.Errorf("the messages sent back = %v, want the first message past the cap, at 31, once, holding the gap read", holds)
	}
	if c := cursorOf(t, rig.database, "INFRA"); !slices.Equal(c.readReasons, []string{ReadReasonGap}) || c.readHolder == nil || c.readHolds != 1 ||
		c.lastSeq != nil || c.stateID != nil {
		t.Errorf("INFRA's cursor while its starting read is held is %s, want one that follows no daemon, owes the gap read and names its holder once", c)
	}
	if drops := rig.awaitLogRecords(crowdedLine, 1); drops[0]["project"] != "INFRA" || drops[0]["reason"] != ReadReasonGap {
		t.Errorf("the drops logged = %v, want one naming INFRA and the gap", drops)
	}

	release()
	rig.waitFor(left+15*time.Second, func() bool {
		return currentValues(t, rig.database, "INFRA")["INFRA-1/phase"] == `"reviewing"`
	}, func() string {
		return fmt.Sprintf("after the starting read at 30 INFRA-1's phase is %s and INFRA's cursor is %s: the events past the cap were never read",
			currentValues(t, rig.database, "INFRA")["INFRA-1/phase"], cursorOf(t, rig.database, "INFRA"))
	})
	rig.settled()
	reads := wholeReadsOf(t, rig.database, "INFRA")
	if len(reads) != 2 {
		t.Fatalf("INFRA's whole reads are %v, want the starting read and the gap read", reads)
	}
	requireWholeRead(t, reads[0], ReadReasonStart, true)
	requireWholeRead(t, reads[1], ReadReasonGap, true)
	if testwait.Holds(2*time.Second, 25*time.Millisecond, func() bool { return daemon.served() > 2 }) {
		t.Errorf("INFRA's daemon served %d reads, want the starting read and the gap read", daemon.served())
	}
	if c := cursorOf(t, rig.database, "INFRA"); c.readOwed || c.readHolder != nil || c.lastSeq == nil || *c.lastSeq != 33 {
		t.Errorf("INFRA's cursor is %s, want it at the gap read's 33 with nothing owed and no holder", c)
	}
}
