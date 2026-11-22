package legion

import (
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
// before recorded (previous_state_id). A write back to that daemon keeps that store recorded, since
// the cursor held none to record. The wake's starting read of the daemon is good and gives the
// cursor that same store, and keeps the recorded store, so the daemon's next events are applied,
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
	if c := cursorOf(t, rig.database, "INFRA"); c.stateID != nil || c.previousStateID == nil || *c.previousStateID != rigStateID {
		t.Fatalf("INFRA's cursor after the write back is %s, want no store and the store %s still recorded", c, rigStateID)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(3)
	reads = wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonStart, true)
	if c := cursorOf(t, rig.database, "INFRA"); c.readOwed || c.lastSeq == nil || *c.lastSeq != 12 || c.stateID == nil || *c.stateID != rigStateID ||
		c.previousStateID == nil || *c.previousStateID != rigStateID {
		t.Fatalf("INFRA's cursor after the good read is %s, want it at the read's 12 with the store %s, nothing owed and that store still recorded", c, rigStateID)
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
// again at the project's next message. INFRA's setting names another daemon, whose starting read
// fails, and then names its first daemon again, which does not answer either: the cursor holds no
// store, owes the read, and keeps the first daemon's store recorded as the store the setting
// named before (previous_state_id), since it held none to record. So the first daemon's own
// messages are of that store, and each is finished unapplied and holds no read. Inside the minute
// none makes a read. Once the read is due, the daemon's next message makes it, as a call no
// message makes would; the read is good and gives the cursor the daemon's store, and the daemon's
// later messages are applied, each in turn.
func TestAWriteBackWhoseFirstReadFailsIsReadAgainAtItsDaemonsNextMessage(t *testing.T) {
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
		c.previousStateID == nil || *c.previousStateID != rigStateID {
		t.Fatalf("INFRA's cursor after the failed read is %s, want the read owed for retry, no store, no holder and the store %s recorded", c, rigStateID)
	}
	before := currentValues(t, rig.database, "INFRA")

	// Inside the minute, the daemon's next message is finished unapplied and makes no read.
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	rig.settled()
	if served := daemon.served(); served != 2 {
		t.Errorf("the daemon served %d reads inside a minute of its failed one, want its first starting read and the failed one", served)
	}
	if after := currentValues(t, rig.database, "INFRA"); !maps.Equal(after, before) {
		t.Errorf("legion_current = %v, want %v: no message of the store the setting named before is applied while the cursor holds none", after, before)
	}

	// A minute on, the daemon answers, and its next message makes the owed read.
	daemon.serves(daemonState("INFRA", 12, strings.Replace(planning("INFRA"), `"planning"`, `"testing"`, 1)))
	rig.clock.advance(ReadRetryAfter + time.Second)
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "testing")))
	rig.settled()
	if served := daemon.served(); served != 3 {
		t.Errorf("the daemon served %d reads, want one more once the read was due", served)
	}
	reads = wholeReadsOf(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonRetry, true)
	if c := cursorOf(t, rig.database, "INFRA"); c.readOwed || c.lastSeq == nil || *c.lastSeq != 12 || c.stateID == nil || *c.stateID != rigStateID || c.readHolder != nil {
		t.Fatalf("INFRA's cursor after the good read is %s, want it at the read's 12 with the store %s, nothing owed and no holder", c, rigStateID)
	}

	rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"testing"`, `"reviewing"`}) {
		t.Errorf("the phase's history is %v, want the first read's planning, the retry's testing and the daemon's event's reviewing", got)
	}
	requireLastSeq(t, rig.database, "INFRA", 13)
	if held := rig.logRecords(heldLine); len(held) != 0 {
		t.Errorf("the messages sent back = %v, want none", held)
	}
}
