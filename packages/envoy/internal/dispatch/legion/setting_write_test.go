package legion

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestAMessageOfTheStoreASettingNamedBeforeMakesNoStartingRead: R3 applies no message of the
// daemon a setting named before, once the setting names another. A write of INFRA's setting names
// another daemon, and the daemon named before publishes its next event after the write and before
// the write's wake reaches the intake. The write left the cursor following no daemon, so INFRA has
// no starting point, and recorded the store the cursor held (previous_state_id): the message is of
// that store, so it is finished at its first delivery, unapplied, makes no starting read and holds
// none, and its drop is logged. The wake then reads the named daemon whole, and the named daemon's
// own events are applied, each in turn.
func TestAMessageOfTheStoreASettingNamedBeforeMakesNoStartingRead(t *testing.T) {
	const namedRun, namedStore = "8d2b6f41-3e7a-4c95-b0d8-1a5f9c3e7b26", "4b9e1c7a-6f2d-4a83-9e5b-0c7d3f8a1e64"
	rig := newIntakeRig(t)
	previous := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", previous.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	implementing := strings.Replace(planning("INFRA"), `"planning"`, `"implementing"`, 1)
	named := newFakeDaemon(t, strings.Replace(strings.Replace(daemonState("INFRA", 5, implementing), rigStateID, namedStore, 1), rigRun, namedRun, 1))
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", named.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
		t.Fatalf("point INFRA at the named daemon: %v", err)
	}
	if c := cursorOf(t, rig.database, "INFRA"); c.lastSeq != nil || c.stateID != nil || c.readOwed || c.readHolder != nil ||
		c.previousStateID == nil || *c.previousStateID != rigStateID {
		t.Fatalf("INFRA's cursor after the write is %s, want no sequence, no store, nothing owed, no holder and the store %s it held recorded", c, rigStateID)
	}

	// The daemon named before publishes its next event, under its own run and store.
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "merging")))
	rig.settled()
	if served := named.served(); served != 0 {
		t.Errorf("the named daemon served %d reads before the wake, want none: a message of the daemon the setting named before makes no starting read", served)
	}
	if served := previous.served(); served != 1 {
		t.Errorf("the daemon named before served %d reads, want its starting one alone", served)
	}
	if reads := wholeReadsOf(t, rig.database, "INFRA"); len(reads) != 1 {
		t.Errorf("whole reads = %v, want the first daemon's starting read alone", reads)
	}
	if drops := rig.awaitLogRecords(forgottenLine, 1); len(drops) != 1 || drops[0]["project"] != "INFRA" || drops[0]["stateId"] != rigStateID || drops[0]["seq"] != float64(11) {
		t.Errorf("the drops logged = %v, want one naming INFRA, the message's store and its sequence", drops)
	}
	if held := rig.logRecords(heldLine); len(held) != 0 {
		t.Errorf("the messages sent back = %v, want none", held)
	}

	// The wake reads the named daemon whole, and its events follow that read.
	rig.wake <- struct{}{}
	rig.awaitWholeReads(2)
	reads := wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, true)
	rig.publishIn("INFRA", namedRun, namedStore, time.Now().UTC().Truncate(time.Microsecond), false, change(6, "INFRA-1", moved("phase", "testing")))
	rig.publishIn("INFRA", namedRun, namedStore, time.Now().UTC().Truncate(time.Microsecond), false, change(7, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"implementing"`, `"testing"`, `"reviewing"`}) {
		t.Errorf("the phase's history is %v, want the first daemon's planning, the named daemon's read's implementing and its events' testing and reviewing, and no merging", got)
	}
	requireLastSeq(t, rig.database, "INFRA", 7)
	if served := named.served(); served != 1 {
		t.Errorf("the named daemon served %d reads, want the wake's alone", served)
	}
}

// TestAWriteBackToTheDaemonOfTheStoreASettingNamedBeforeAppliesItsMessages: R3 applies no message
// of the daemon a setting named before while the setting names another, and applies the messages of
// a daemon the setting names once a good read has read it. INFRA's setting names another daemon,
// whose starting read fails, so the cursor holds no store and keeps the store of the daemon named
// before recorded, with the URL it followed (previous_state_id, previous_state_url). A write back
// to that URL names that daemon again, so it clears the record. The wake's starting read of the
// daemon is good and gives the cursor that same store, so the daemon's next events are applied,
// each in turn, as every message of the cursor's store is, and none is dropped.
func TestAWriteBackToTheDaemonOfTheStoreASettingNamedBeforeAppliesItsMessages(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	actor := model.Actor{Kind: "user", ID: "alice@example.com"}

	other := newFakeDaemon(t, "")
	other.refuses(http.StatusServiceUnavailable)
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", other.URL, actor); err != nil {
		t.Fatalf("point INFRA at the other daemon: %v", err)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(2)
	reads := wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, false)

	// The setting names the first daemon again, which has moved on meanwhile.
	daemon.serves(daemonState("INFRA", 12, strings.Replace(planning("INFRA"), `"planning"`, `"implementing"`, 1)))
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", daemon.URL, actor); err != nil {
		t.Fatalf("point INFRA back at its first daemon: %v", err)
	}
	if c := cursorOf(t, rig.database, "INFRA"); c.stateID != nil || c.previousStateID != nil || c.previousStateURL != nil {
		t.Fatalf("INFRA's cursor after the write back is %s, want no store and no store recorded", c)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(3)
	reads = wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, true)
	if c := cursorOf(t, rig.database, "INFRA"); c.readOwed || c.lastSeq == nil || *c.lastSeq != 12 || c.stateID == nil || *c.stateID != rigStateID ||
		c.previousStateID != nil {
		t.Fatalf("INFRA's cursor after the good read is %s, want it at the read's 12 with the store %s, nothing owed and no store recorded", c, rigStateID)
	}

	rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "testing")))
	rig.publish("INFRA", change(14, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"implementing"`, `"testing"`, `"reviewing"`}) {
		t.Errorf("the phase's history is %v, want the reads' planning and implementing, then the daemon's events' testing and reviewing", got)
	}
	requireLastSeq(t, rig.database, "INFRA", 14)
	if drops := rig.logRecords(forgottenLine); len(drops) != 0 {
		t.Errorf("the drops logged = %v, want none: the daemon the setting names again is read and followed", drops)
	}
	if served := daemon.served(); served != 2 {
		t.Errorf("the daemon served %d reads, want its first starting read and the write back's", served)
	}
}

// TestAWriteBackWhoseFirstReadFailsIsReadAgainAtItsDaemonsNextMessage: R3 tries a failed read
// again at the project's next message, and a write back to the daemon a setting named before names
// that daemon again. INFRA's setting names another daemon, whose starting read fails, so the cursor
// keeps the first daemon's store recorded with the URL it followed (previous_state_id,
// previous_state_url). Then the setting names the first daemon again, which does not answer
// either. The write back clears the record, so the cursor holds no store, owes the read for retry
// and records no store. The first daemon's own messages are then the setting's daemon's, as every
// message is: its next message arrives left before the read is due, holds the owed read and goes
// back to the stream until the read is due, and its redelivery makes the read. The read is good
// and gives the cursor the daemon's store, and the daemon's later messages are applied, each in
// turn. None of its messages is dropped as one of the daemon the setting named before.
func TestAWriteBackWhoseFirstReadFailsIsReadAgainAtItsDaemonsNextMessage(t *testing.T) {
	const left = 3 * time.Second
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	actor := model.Actor{Kind: "user", ID: "alice@example.com"}

	other := newFakeDaemon(t, "")
	other.refuses(http.StatusServiceUnavailable)
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", other.URL, actor); err != nil {
		t.Fatalf("point INFRA at the other daemon: %v", err)
	}
	if c := cursorOf(t, rig.database, "INFRA"); c.previousStateID == nil || *c.previousStateID != rigStateID ||
		c.previousStateURL == nil || *c.previousStateURL != daemon.URL {
		t.Fatalf("INFRA's cursor after the write naming another daemon is %s, want the store %s recorded with the URL %s it followed", c, rigStateID, daemon.URL)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(2)
	reads := wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, false)

	// The setting names the first daemon again, which does not answer its starting read.
	daemon.refuses(http.StatusServiceUnavailable)
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", daemon.URL, actor); err != nil {
		t.Fatalf("point INFRA back at its first daemon: %v", err)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(3)
	reads = wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, false)
	if c := cursorOf(t, rig.database, "INFRA"); !slices.Equal(c.readReasons, []string{ReadReasonRetry}) || c.stateID != nil || c.readHolder != nil ||
		c.previousStateID != nil || c.previousStateURL != nil {
		t.Errorf("INFRA's cursor after the failed read is %s, want the read owed for retry, no store, no holder and no store recorded", c)
	}
	before := currentValues(t, rig.database, "INFRA")

	// left before the read is due, the daemon's next message holds the owed read: it goes back to
	// the stream, unapplied, and makes no read yet.
	daemon.serves(daemonState("INFRA", 12, strings.Replace(planning("INFRA"), `"planning"`, `"testing"`, 1)))
	rig.clock.advance(ReadRetryAfter - left)
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	holder := rig.lastStreamSeq()
	rig.settledHolding(1)
	if served := daemon.served(); served != 2 {
		t.Errorf("the daemon served %d reads before the read was due, want its first starting read and the failed one", served)
	}
	if after := currentValues(t, rig.database, "INFRA"); !maps.Equal(after, before) {
		t.Errorf("legion_current = %v, want %v: no message is applied while the cursor holds no store", after, before)
	}

	// Its redelivery, once the read is due, makes the owed read.
	rig.settled()
	if served := daemon.served(); served != 3 {
		t.Errorf("the daemon served %d reads, want one more once the read was due", served)
	}
	reads = wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonRetry, true)
	if got := rig.heldValues(holder, "handBack"); !slices.Equal(got, oneTo(1)) {
		t.Errorf("the cursor counted the hand-backs %v of the daemon's message, want 1: it held the read once", got)
	}
	if c := cursorOf(t, rig.database, "INFRA"); c.readOwed || c.lastSeq == nil || *c.lastSeq != 12 || c.stateID == nil || *c.stateID != rigStateID ||
		c.readHolder != nil || c.readHolds != 0 {
		t.Fatalf("INFRA's cursor after the good read is %s, want it at the read's 12 with the store %s, nothing owed and no holder", c, rigStateID)
	}

	rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"testing"`, `"reviewing"`}) {
		t.Errorf("the phase's history is %v, want the first read's planning, the retry's testing and the daemon's event's reviewing", got)
	}
	requireLastSeq(t, rig.database, "INFRA", 13)
	if drops := rig.logRecords(forgottenLine); len(drops) != 0 {
		t.Errorf("the drops logged = %v, want none: the daemon the setting names again is not the daemon it named before", drops)
	}
}

// TestAMessageKeyedByAnOldMemoryOfAForgottenStoreGoesBackToItsProjectsRunner: the intake
// remembers the store each project's cursor drops (Intake.forgotten), reads it again at each
// settings change and take-over, and keys each message of that store beside the project's runner.
// That memory can be out of date. Here INFRA's setting names another URL, whose starting read
// fails, so the intake remembers the store INFRA's cursor followed as one it drops. Then a whole
// read made outside the intake (ReadWhole) finds the daemon behind that URL is the daemon the
// setting named before, in its own store, and gives the cursor that store. The intake still
// remembers the store as dropped, so the daemon's next message is keyed beside the runner, where
// the cursor shows the store is not dropped now. The message goes back to the stream once, and
// that is logged (notForgottenLine); its next delivery runs on the project's runner, which applies
// it with no read.
func TestAMessageKeyedByAnOldMemoryOfAForgottenStoreGoesBackToItsProjectsRunner(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	// INFRA's setting names another URL, whose starting read fails: the intake remembers the store
	// INFRA's cursor followed as one the cursor drops.
	alias := newFakeDaemon(t, "")
	alias.refuses(http.StatusServiceUnavailable)
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", alias.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
		t.Fatalf("point INFRA at another URL: %v", err)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(2)
	reads := wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, false)

	// The daemon named before answers behind that URL, and a whole read made outside the intake
	// gives the cursor its store.
	alias.serves(daemonState("INFRA", 12, strings.Replace(planning("INFRA"), `"planning"`, `"implementing"`, 1)))
	if err := ReadWhole(t.Context(), rig.database.Pool, Settings{Project: "INFRA", StateURL: alias.URL}, ReadReasonStart); err != nil {
		t.Fatalf("read INFRA's daemon whole outside the intake: %v", err)
	}
	if c := cursorOf(t, rig.database, "INFRA"); c.lastSeq == nil || *c.lastSeq != 12 || c.stateID == nil || *c.stateID != rigStateID ||
		c.previousStateID == nil || *c.previousStateID != rigStateID {
		t.Fatalf("INFRA's cursor after the read outside the intake is %s, want it at the read's 12 in the store %s, which is the store recorded", c, rigStateID)
	}

	rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "testing")))
	rig.settled()
	lines := rig.awaitLogRecords(notForgottenLine, 1)
	if len(lines) != 1 || lines[0]["level"] != "INFO" || lines[0]["project"] != "INFRA" || lines[0]["stateId"] != rigStateID || lines[0]["seq"] != float64(13) {
		t.Errorf("the hand-backs logged = %v, want one at INFO naming INFRA, the message's store and its sequence 13", lines)
	}
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"implementing"`, `"testing"`}) {
		t.Errorf("the phase's history is %v, want the first read's planning, the read outside the intake's implementing and the daemon's event's testing", got)
	}
	requireLastSeq(t, rig.database, "INFRA", 13)
	if served := alias.served(); served != 2 {
		t.Errorf("the daemon behind the URL served %d reads, want the failed starting read and the read outside the intake", served)
	}
	if drops := rig.logRecords(forgottenLine); len(drops) != 0 {
		t.Errorf("the drops logged = %v, want none: the cursor holds the message's store", drops)
	}
	if held := rig.logRecords(heldLine); len(held) != 0 {
		t.Errorf("the messages sent back = %v, want none", held)
	}
}

// TestATakeOverFinishesAMessageOfTheStoreASettingNamedBeforeBesideAReadOfItsProject: an intake that
// takes the durable over reads the stores the projects' cursors drop as it queues each project's
// starting point (SettingsChanged), so it too finishes a message of such a store at once, beside
// the project's runner, while that runner makes a read. INFRA's setting names another daemon,
// which never answers: each read of it waits out the client's window, readWindow, and moves the
// intake's clock RequestTimeout on, as in
// TestASettingNamingAnotherDaemonFinishesTheOldDaemonsMessagesWithNoRead. Once the write's starting
// read has failed, the intake stops and a second intake takes the durable over. Its take-over reads
// the named daemon again, since the failed read is due, and that read holds INFRA's runner for
// readWindow. The daemon named before publishes meanwhile: its message is finished unapplied
// within finishedWithin of its publish, and makes no read.
func TestATakeOverFinishesAMessageOfTheStoreASettingNamedBeforeBesideAReadOfItsProject(t *testing.T) {
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

	named := newFakeDaemon(t, "")
	named.stalls(func() { rig.clock.advance(RequestTimeout) })
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", named.URL, model.Actor{Kind: "user", ID: "alice@example.com"}); err != nil {
		t.Fatalf("point INFRA at another daemon: %v", err)
	}
	rig.wake <- struct{}{}
	rig.waitFor(time.Minute, func() bool { return len(wholeReadsOf(t, rig.database, "INFRA")) >= 2 }, func() string {
		return fmt.Sprintf("the write's starting read of the named daemon is not recorded: %v", wholeReadsOf(t, rig.database, "INFRA"))
	})

	rig.stop()
	rig.start()
	rig.waitFor(time.Minute, func() bool { return named.served() >= 2 }, func() string {
		return fmt.Sprintf("the take-over did not read the named daemon again: it served %d reads", named.served())
	})
	published := time.Now()
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "merging")))
	rig.settled()
	if took := time.Since(published); took >= finishedWithin {
		t.Errorf("the message of the daemon the setting named before was finished %s after its publish, want less than %s: it waited behind the take-over's read of %s",
			took.Round(time.Millisecond), finishedWithin, readWindow)
	}
	if served := named.served(); served != 2 {
		t.Errorf("the named daemon served %d reads, want the write's and the take-over's: the message of the daemon named before made a read", served)
	}
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); slices.Contains(got, `"merging"`) {
		t.Errorf("the phase's history is %v: the message of the daemon the setting named before was applied", got)
	}
}
