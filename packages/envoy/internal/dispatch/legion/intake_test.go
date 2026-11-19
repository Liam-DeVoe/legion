package legion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/dispatch/delivery"
	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testnats"
	"github.com/sjawhar/envoy/internal/testwait"
)

// intakeRig is one test's intake: its database, the NATS client that publishes as the daemon's
// listener does, the lease pool reservation the intake takes its durable's hold on, the settings
// wake, the intake's clock and the log every intake of the test writes.
type intakeRig struct {
	t         *testing.T
	database  *store.Store
	publisher *bus.Client
	leases    delivery.DurableLeases
	wake      chan struct{}
	clock     *intakeClock
	logs      *logtest.Buffer
	// run is the daemon run the rig's messages name unless a test names another.
	run string
	// published numbers the rig's messages, so each has an id and a dedupe key of its own.
	published int

	// intake is the intake running now, stop stops it, and subject is the deliver subject its
	// take-over bound the durable to.
	intake  *Intake
	stop    func()
	subject string
}

// intakeClock is the intake's clock (Intake.now): the wall clock, moved forward by what the test
// has advanced it.
type intakeClock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *intakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *intakeClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += by
}

// newIntakeRig opens the test's database and NATS account, and sends the default logger to a
// buffer the test reads. It starts no intake (start).
func newIntakeRig(t *testing.T) *intakeRig {
	t.Helper()
	logs := &logtest.Buffer{}
	logtest.SetDefault(t, slog.NewJSONHandler(logs, nil))
	database := storetest.Open(t)
	publisher, err := bus.ConnectOwningStream([]string{testnats.URL(t)})
	if err != nil {
		t.Fatalf("connect the publisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	callers, err := database.Pool.Leases()
	if err != nil {
		t.Fatalf("open the lease pool: %v", err)
	}
	leases, err := ReserveLeases(callers)
	if err != nil {
		t.Fatalf("reserve the intake's callers of the lease pool: %v", err)
	}
	return &intakeRig{
		t: t, database: database, publisher: publisher, leases: leases, wake: make(chan struct{}, 1),
		clock: &intakeClock{}, logs: logs, run: "5f0c7a3e-2b9d-4e61-8c47-1d3a9b6e2f80",
	}
}

// start runs an intake on a NATS client of its own until the test ends or stop is called, and
// waits for its take-over to have bound the durable on a deliver subject of its own.
func (r *intakeRig) start() {
	r.t.Helper()
	r.startOn(r.clock.now)
}

// startOn is start with now as the intake's clock rather than the rig's.
func (r *intakeRig) startOn(now func() time.Time) {
	r.t.Helper()
	client, err := bus.ConnectOwningStream([]string{testnats.URL(r.t)})
	if err != nil {
		r.t.Fatalf("connect the intake: %v", err)
	}
	r.t.Cleanup(client.Close)
	intake := NewIntake(client, r.database.Pool, r.leases, r.wake)
	intake.now = now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		intake.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	r.t.Cleanup(stop)
	previous := r.subject
	var info *natsgo.ConsumerInfo
	r.waitFor(20*time.Second, func() bool {
		info, err = r.publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchLegionStateDurable)
		return err == nil && info.PushBound && info.Config.DeliverSubject != previous
	}, func() string { return fmt.Sprintf("the intake never bound its durable: %+v, %v", info, err) })
	r.intake, r.stop, r.subject = intake, stop, info.Config.DeliverSubject
}

// waitFor polls done until it holds, and fails the test with failure's message when within passes.
func (r *intakeRig) waitFor(within time.Duration, done func() bool, failure func() string) {
	r.t.Helper()
	if !testwait.Holds(within, 25*time.Millisecond, done) {
		r.t.Fatal(failure())
	}
}

// settled waits for the intake to have acknowledged every message the stream holds: it
// acknowledges a message once its batch has committed, or once it was skipped or dropped.
func (r *intakeRig) settled() {
	r.t.Helper()
	stream, err := r.publisher.JS().StreamInfo(bus.Stream)
	if err != nil {
		r.t.Fatalf("read the stream: %v", err)
	}
	last := stream.State.LastSeq
	var info *natsgo.ConsumerInfo
	r.waitFor(20*time.Second, func() bool {
		info, err = r.publisher.JS().ConsumerInfo(bus.Stream, bus.DispatchLegionStateDurable)
		return err == nil && info.AckFloor.Stream >= last && info.NumAckPending == 0 && info.NumPending == 0
	}, func() string {
		return fmt.Sprintf("the intake has not acknowledged through stream sequence %d: %+v, %v", last, info, err)
	})
}

// awaitWholeReads waits for want whole reads of any project to be recorded.
func (r *intakeRig) awaitWholeReads(want int) {
	r.t.Helper()
	got := 0
	r.waitFor(20*time.Second, func() bool {
		if err := r.database.Pool.QueryRow(context.Background(),
			`select count(*) from legion_reads where kind = 'read'`).Scan(&got); err != nil {
			r.t.Fatalf("count the whole reads: %v", err)
		}
		return got >= want
	}, func() string { return fmt.Sprintf("%d whole reads recorded, want %d", got, want) })
}

// publish publishes events as one message of project's state lane, at the wall clock's moment,
// under the rig's run, and answers that moment.
func (r *intakeRig) publish(project string, events ...map[string]any) time.Time {
	r.t.Helper()
	at := time.Now().UTC().Truncate(time.Microsecond)
	r.publishOf(project, r.run, at, events...)
	return at
}

// publishOf publishes events as the daemon's outbox publishes one committed transaction, through
// the listener's publish route: an envelope on notifications.legion.<token>.state whose payload is
// the state message (packages/contracts/fixtures/daemon-api/state-events.json holds such
// envelopes).
func (r *intakeRig) publishOf(project, run string, at time.Time, events ...map[string]any) {
	r.t.Helper()
	first, last := events[0]["seq"].(int), events[len(events)-1]["seq"].(int)
	payload, err := json.Marshal(map[string]any{
		"project": project, "run": run, "firstSeq": first, "seq": last,
		"at": at.Format("2006-01-02T15:04:05.000000Z"), "events": events,
	})
	if err != nil {
		r.t.Fatalf("encode the state message: %v", err)
	}
	r.published++
	id := fmt.Sprintf("%s-%d-%d", r.t.Name(), r.published, time.Now().UnixNano())
	if err := r.publisher.Publish(contracts.Envelope{
		EventID: id, Source: "agent", SourceEventID: id,
		Topic:          contracts.LegionStateSubject(strings.ToLower(project)),
		DedupeKey:      "legion-outbox:" + id,
		IssuedAt:       time.Now().UnixMilli(),
		PayloadSummary: fmt.Sprintf("%s state %d-%d", project, first, last),
		Payload:        string(payload),
		TraceID:        id,
	}); err != nil {
		r.t.Fatalf("publish the state message: %v", err)
	}
}

// change is a change event of issue naming each change.
func change(seq int, issue string, changes ...map[string]any) map[string]any {
	return map[string]any{"seq": seq, "kind": "change", "issue": issue, "changes": changes}
}

// moved is one field's change to value.
func moved(field string, value any) map[string]any {
	return map[string]any{"field": field, "old": nil, "new": value}
}

// claimMoved is one role's claim changing to state.
func claimMoved(role string, state any) map[string]any {
	return map[string]any{"field": "claim", "role": role, "old": nil, "new": state}
}

// admissionMoved is an admission event carrying block whole, cap and held included.
func admissionMoved(seq int, block map[string]any) map[string]any {
	return map[string]any{"seq": seq, "kind": "admission", "admission": block}
}

// booted is a boot event whose settings are INFRA's: the project, a dispatch_url and admissionCap
// as admission_cap.
func booted(seq int, migrated bool, admissionCap int) map[string]any {
	return map[string]any{"seq": seq, "kind": "boot", "migrated": migrated, "settings": map[string]any{
		"project": "INFRA", "dispatch_url": "http://dispatch.internal.example", "admission_cap": admissionCap,
	}}
}

// daemonState is a state document of project's daemon whose daemon block serves stateSeq, whose
// settings carry a dispatch_url, and whose issues are the members given.
func daemonState(project string, stateSeq int, issues string) string {
	return fmt.Sprintf(`{"daemon":{"project":%[1]q,"schemaVersion":20261118164019,"stateSeq":%[2]d},
		"settings":{"project":%[1]q,"dispatch_url":"http://dispatch.internal.example","admission_cap":2},
		"admission":{"cap":2,"active":["%[1]s-1"],"waiting":[],"trees":["%[1]s-1"],"blocked":[],"held":[]},
		"issues":{%[3]s}}`, project, stateSeq, issues)
}

// planning is issue <project>-1 of a daemonState, planning in slot 0 under its own tree, its
// architect and its planner working.
func planning(project string) string {
	return fmt.Sprintf(`"%[1]s-1":{"key":"%[1]s-1","phase":"planning","tree":"%[1]s-1","status":"doing",
		"slot":{"index":0,"admittedAt":"2026-11-18T16:00:00Z"},
		"architect":{"session":"s-0","state":"working"},
		"workers":{"planner":{"claim":{"session":"s-1","state":"working"},"rounds":1}}}`, project)
}

// currentValues is project's legion_current, "<issue>/<field>" to the value's text.
func currentValues(t *testing.T, database *store.Store, project string) map[string]string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(),
		`select issue_key || '/' || field, value::text from legion_current where project = $1`, project)
	if err != nil {
		t.Fatalf("read legion_current: %v", err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var field, value string
		if err := rows.Scan(&field, &value); err != nil {
			t.Fatalf("scan legion_current: %v", err)
		}
		values[field] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read legion_current: %v", err)
	}
	return values
}

// history is one field's values in legion_observations, oldest first, each as its JSON text.
func history(t *testing.T, database *store.Store, project, issue, field string) []string {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select value::text from legion_observations
		where project = $1 and issue_key = $2 and field = $3 order by observed_at
	`, project, issue, field)
	if err != nil {
		t.Fatalf("read the history of %s/%s: %v", issue, field, err)
	}
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the history of %s/%s: %v", issue, field, err)
	}
	return values
}

// readRecord is one legion_reads row with the columns of migration 20261118212431.
type readRecord struct {
	kind   string
	reason *string
	ok     bool
	error  *string
	bytes  int
	readAt time.Time
}

func (r readRecord) String() string {
	reason, failure := "", ""
	if r.reason != nil {
		reason = *r.reason
	}
	if r.error != nil {
		failure = *r.error
	}
	return fmt.Sprintf("{%s reason=%q ok=%v error=%q bytes=%d at=%s}", r.kind, reason, r.ok, failure, r.bytes, r.readAt.Format(time.RFC3339Nano))
}

// readRecords is project's legion_reads, oldest first.
func readRecords(t *testing.T, database *store.Store, project string) []readRecord {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select kind, reason, ok, error, bytes, read_at from legion_reads where project = $1 order by id
	`, project)
	if err != nil {
		t.Fatalf("read legion_reads: %v", err)
	}
	defer rows.Close()
	var listed []readRecord
	for rows.Next() {
		var row readRecord
		if err := rows.Scan(&row.kind, &row.reason, &row.ok, &row.error, &row.bytes, &row.readAt); err != nil {
			t.Fatalf("scan legion_reads: %v", err)
		}
		listed = append(listed, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read legion_reads: %v", err)
	}
	return listed
}

// requireWholeRead fails unless row is a whole read made for reason, good or failed as ok says.
func requireWholeRead(t *testing.T, row readRecord, reason string, ok bool) {
	t.Helper()
	if row.kind != readKindRead || row.reason == nil || *row.reason != reason || row.ok != ok {
		t.Fatalf("legion_reads row = %s, want a whole read (kind %q) for reason %q with ok %v", row, readKindRead, reason, ok)
	}
}

// cursorRow is project's legion_event_cursors row.
type cursorRow struct {
	lastSeq  *int64
	readOwed bool
	run      *string
}

func cursorOf(t *testing.T, database *store.Store, project string) cursorRow {
	t.Helper()
	var row cursorRow
	if err := database.Pool.QueryRow(context.Background(),
		`select last_seq, read_owed, run from legion_event_cursors where project = $1`, project).Scan(&row.lastSeq, &row.readOwed, &row.run); err != nil {
		t.Fatalf("read the cursor of %s: %v", project, err)
	}
	return row
}

func requireLastSeq(t *testing.T, database *store.Store, project string, want int64) {
	t.Helper()
	if got := cursorOf(t, database, project).lastSeq; got == nil || *got != want {
		t.Fatalf("the cursor of %s is at %v, want %d", project, got, want)
	}
}

// logRecords is every record the rig's intakes logged with msg, in order.
func (r *intakeRig) logRecords(msg string) []map[string]any {
	var records []map[string]any
	for line := range strings.Lines(r.logs.String()) {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) == nil && record["msg"] == msg {
			records = append(records, record)
		}
	}
	return records
}

// awaitLogRecords waits for at least want records the rig's intakes logged with msg, and answers
// every one logged by then. The intake logs a whole read after the read's transaction has
// committed and its announcement has gone out (finishWhole), and a dropped message after its
// acknowledgement (deliver): what a test waits on before it reads a log line - the read's
// legion_reads row, the durable's ack floor - can be in place a moment before the line is.
func (r *intakeRig) awaitLogRecords(msg string, want int) []map[string]any {
	r.t.Helper()
	var records []map[string]any
	r.waitFor(5*time.Second, func() bool {
		records = r.logRecords(msg)
		return len(records) >= want
	}, func() string { return fmt.Sprintf("logged %d %q lines %v, want %d", len(records), msg, records, want) })
	return records
}

// awaitStartingRead waits for the take-over's whole read of project's daemon, and fails unless it
// is a good read for ReadReasonStart, in legion_reads and in its log line.
func (r *intakeRig) awaitStartingRead(project string) {
	r.t.Helper()
	r.awaitWholeReads(1)
	reads := readRecords(r.t, r.database, project)
	if len(reads) == 0 {
		r.t.Fatalf("no whole read of %s is recorded", project)
	}
	requireWholeRead(r.t, reads[0], ReadReasonStart, true)
	lines := r.awaitLogRecords(wholeReadLine, 1)
	if line := lines[0]; line["level"] != "INFO" || line["project"] != project || line["reason"] != ReadReasonStart || line["ok"] != true {
		r.t.Fatalf("the starting read's line = %v, want INFO for project %s, reason %s and ok", line, project, ReadReasonStart)
	}
}

// requireWholeReadLines fails unless the whole reads logged are, in order, one per reason given,
// each at INFO naming its project, its reason, its stateSeq, its bytes and its duration.
func (r *intakeRig) requireWholeReadLines(project string, reasons ...string) []map[string]any {
	r.t.Helper()
	lines := r.awaitLogRecords(wholeReadLine, len(reasons))
	if len(lines) != len(reasons) {
		r.t.Fatalf("logged %d whole reads %v, want %d for %v", len(lines), lines, len(reasons), reasons)
	}
	for index, line := range lines {
		if line["level"] != "INFO" || line["project"] != project || line["reason"] != reasons[index] {
			r.t.Fatalf("whole read line %d = %v, want INFO for project %s and reason %s", index, line, project, reasons[index])
		}
		for _, attribute := range []string{"stateSeq", "bytes", "duration"} {
			if _, present := line[attribute]; !present {
				r.t.Fatalf("whole read line %d = %v, want it to name %s", index, line, attribute)
			}
		}
	}
	return lines
}

// TestAnEventLeavesEveryFieldItDoesNotNameAsItWas: a whole read observes every field of the state
// and writes `null` for a field it no longer carries; an event names only what moved, and every
// field it does not name keeps the value Dispatch observed last, `null` row or not.
func TestAnEventLeavesEveryFieldItDoesNotNameAsItWas(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	before := currentValues(t, rig.database, "INFRA")
	for _, field := range []string{"/admission", "/settings", "INFRA-1/phase", "INFRA-1/tree", "INFRA-1/slot", "INFRA-1/claim.architect", "INFRA-1/claim.planner"} {
		if _, observed := before[field]; !observed {
			t.Fatalf("the starting read observed %v, want %s among them", before, field)
		}
	}
	rows := len(observations(t, rig.database, "INFRA"))

	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	rig.publish("INFRA", change(12, "INFRA-1", claimMoved("planner", nil), claimMoved("implementer", "launching")))
	rig.publish("INFRA", admissionMoved(13, map[string]any{"cap": 3, "active": []string{"INFRA-1"}, "waiting": []string{}, "held": []string{}}))
	rig.settled()

	want := maps.Clone(before)
	want["INFRA-1/phase"] = `"implementing"`
	want["INFRA-1/claim.planner"] = `null`
	want["INFRA-1/claim.implementer"] = `"launching"`
	want["/admission"] = `{"cap": 3, "active": ["INFRA-1"]}`
	if after := currentValues(t, rig.database, "INFRA"); !maps.Equal(after, want) {
		t.Errorf("legion_current = %v, want %v: each event moves the fields it names and leaves the tree, the slot, the architect's claim and the settings as the read left them", after, want)
	}
	if written := len(observations(t, rig.database, "INFRA")) - rows; written != 4 {
		t.Errorf("the three messages wrote %d rows, want 4: one per field they name", written)
	}
	if served := daemon.served(); served != 1 {
		t.Errorf("the daemon served %d reads, want the starting one alone", served)
	}
}

// TestEachAppliedBatchRecordsAReadAtItsNewestEvent: each message the intake applies records one
// legion_reads row of kind `events`, at the message's `at` (the time its events committed, which
// they share), with the bytes it carried and no reason, and its observations are observed at that
// moment.
func TestEachAppliedBatchRecordsAReadAtItsNewestEvent(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	first := rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")), change(12, "INFRA-1", moved("slot", nil)))
	rig.settled()
	second := rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "testing")))
	rig.settled()

	reads := readRecords(t, rig.database, "INFRA")
	if len(reads) != 3 {
		t.Fatalf("legion_reads = %v, want the starting read and one row per message", reads)
	}
	requireWholeRead(t, reads[0], ReadReasonStart, true)
	for index, at := range []time.Time{first, second} {
		row := reads[index+1]
		if row.kind != readKindEvents || row.reason != nil || !row.ok || row.error != nil || row.bytes == 0 || !row.readAt.Equal(at) {
			t.Errorf("legion_reads row %d = %s, want an `events` row at %s with its bytes and no reason", index+1, row, at.Format(time.RFC3339Nano))
		}
	}
	var observedAt time.Time
	if err := rig.database.Pool.QueryRow(t.Context(), `
		select observed_at from legion_current where project = 'INFRA' and issue_key = 'INFRA-1' and field = 'slot'
	`).Scan(&observedAt); err != nil {
		t.Fatalf("read the slot's newest row: %v", err)
	}
	if !observedAt.Equal(first) {
		t.Errorf("the slot released at %s is observed at %s, want the message's at", first.Format(time.RFC3339Nano), observedAt.Format(time.RFC3339Nano))
	}
	requireLastSeq(t, rig.database, "INFRA", 13)
}

// TestEachAppliedBatchWakesTheProjectsBoard: each batch the intake applies is announced on
// StateChannel with its project, which is what wakes an open Legion board (api/delivery_live_hub.go),
// as a whole read is.
func TestEachAppliedBatchWakesTheProjectsBoard(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	announced := announcements(t, rig.database, "INFRA")

	rig.start()
	announced("the starting read")
	rig.awaitStartingRead("INFRA")
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	announced("a batch of state events")
	rig.publish("INFRA", change(12, "INFRA-1", claimMoved("planner", "idle")))
	announced("a second batch")
}

// TestAStoppedIntakeReplaysWhatItMissed: the messages published while no intake ran stay with the
// durable, and the next intake's take-over creates it again at its ack floor, so they are applied
// in order and nothing reads the daemon again: the cursor is the starting point.
func TestAStoppedIntakeReplaysWhatItMissed(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	rig.settled()

	rig.stop()
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "testing")))
	rig.publish("INFRA", change(13, "INFRA-1", claimMoved("tester", "working")))
	if phase := currentValues(t, rig.database, "INFRA")["INFRA-1/phase"]; phase != `"implementing"` {
		t.Fatalf("the phase is %s with no intake running, want the implementing the running one applied", phase)
	}

	rig.start()
	rig.settled()
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"implementing"`, `"testing"`}) {
		t.Errorf("the phase's history is %v, want the read's, then each message's in order", got)
	}
	if claim := currentValues(t, rig.database, "INFRA")["INFRA-1/claim.tester"]; claim != `"working"` {
		t.Errorf("the tester's claim is %q, want the missed message's working", claim)
	}
	requireLastSeq(t, rig.database, "INFRA", 13)
	if served := daemon.served(); served != 1 {
		t.Errorf("the daemon served %d reads, want the starting one alone: the second take-over has the cursor to start from", served)
	}
	rig.requireWholeReadLines("INFRA", ReadReasonStart)
}

// TestAGapTheStreamNoLongerHoldsMakesOneWholeRead: a message whose first sequence is past the
// cursor's next one carries events the stream no longer holds, so the project is read whole once,
// the cursor takes the read's stateSeq, and the messages after it are applied with no other read.
func TestAGapTheStreamNoLongerHoldsMakesOneWholeRead(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	// Sequences 11 to 13 never reach Dispatch; the daemon's state holds them.
	daemon.serves(daemonState("INFRA", 15, strings.Replace(planning("INFRA"), `"planning"`, `"reviewing"`, 1)))
	rig.publish("INFRA", change(14, "INFRA-1", moved("phase", "testing")), change(15, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()

	if served := daemon.served(); served != 2 {
		t.Fatalf("the daemon served %d reads, want the starting one and one for the gap", served)
	}
	reads := readRecords(t, rig.database, "INFRA")
	if len(reads) != 2 {
		t.Fatalf("legion_reads = %v, want the starting read and the gap's, and no batch: the message is at or below the read's sequence", reads)
	}
	requireWholeRead(t, reads[1], ReadReasonGap, true)
	requireLastSeq(t, rig.database, "INFRA", 15)
	lines := rig.requireWholeReadLines("INFRA", ReadReasonStart, ReadReasonGap)
	if lines[1]["stateSeq"] != float64(15) {
		t.Errorf("the gap's read logged stateSeq %v, want 15", lines[1]["stateSeq"])
	}

	rig.publish("INFRA", change(16, "INFRA-1", moved("phase", "accepting")))
	rig.settled()
	if phase := currentValues(t, rig.database, "INFRA")["INFRA-1/phase"]; phase != `"accepting"` {
		t.Errorf("the phase after the gap is %s, want the next message's accepting", phase)
	}
	if served := daemon.served(); served != 2 {
		t.Errorf("the daemon served %d reads, want no more once the gap was read", served)
	}
}

// TestAWholeReadAfterEventsAheadOfDispatchsClockIsTheNewestState: an event's rows are observed at
// the daemon's commit time, and the daemon's clock can run ahead of Dispatch's. A whole read made
// after such events is the newer state all the same, so each row it writes, a `null` for a field
// it no longer carries included, lands after the field's newest row, and legion_current (what
// ReadCurrent answers) holds the read's values rather than the older events'. Every row the read
// writes is at one moment, 1 µs after the project's newest row, though some of its fields have
// older newest rows than others: that one moment is how the metrics tell an issue a read dropped
// from a slot released (metrics' slotSpans). The read's own legion_reads row keeps the read's time.
func TestAWholeReadAfterEventsAheadOfDispatchsClockIsTheNewestState(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	// The daemon's clock is an hour ahead of Dispatch's. Message 12 commits a second after 11, so
	// the architect's claim has a newer row than the phase and the planner's claim.
	ahead := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	rig.publishOf("INFRA", rig.run, ahead,
		change(11, "INFRA-1", moved("phase", "implementing"), claimMoved("planner", "idle")))
	latest := ahead.Add(time.Second)
	rig.publishOf("INFRA", rig.run, latest, change(12, "INFRA-1", claimMoved("architect", "idle")))
	rig.settled()
	if current := currentValues(t, rig.database, "INFRA"); current["INFRA-1/phase"] != `"implementing"` ||
		current["INFRA-1/claim.planner"] != `"idle"` || current["INFRA-1/claim.architect"] != `"idle"` {
		t.Fatalf("legion_current after the events = %v, want their implementing and idle claims", current)
	}

	// Sequence 13 never reaches Dispatch. The daemon's state has moved the phase on, dropped the
	// planner's claim and put the architect back to work, which the gap's whole read finds.
	daemon.serves(daemonState("INFRA", 15, `"INFRA-1":{"key":"INFRA-1","phase":"reviewing","tree":"INFRA-1",
		"slot":{"index":0},"architect":{"session":"s-0","state":"working"}}`))
	rig.publishOf("INFRA", rig.run, latest.Add(time.Second),
		change(14, "INFRA-1", moved("phase", "testing")), change(15, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()
	reads := readRecords(t, rig.database, "INFRA")
	requireWholeRead(t, reads[len(reads)-1], ReadReasonGap, true)
	if readAt := reads[len(reads)-1].readAt; !readAt.Before(ahead) {
		t.Fatalf("the gap's read is recorded at %s, want its own time on Dispatch's clock, before the events' %s",
			readAt.Format(time.RFC3339Nano), ahead.Format(time.RFC3339Nano))
	}

	current, err := ReadCurrent(t.Context(), rig.database.Pool, "INFRA")
	if err != nil {
		t.Fatalf("read the current state: %v", err)
	}
	newest := map[string]string{}
	for _, one := range current.Newest {
		newest[one.IssueKey+"/"+one.Field] = string(one.Value)
	}
	if phase := newest["INFRA-1/phase"]; phase != `"reviewing"` {
		t.Errorf("ReadCurrent's phase is %s, want the whole read's reviewing: the read is newer than the events whatever their clock says", phase)
	}
	if claim, held := newest["INFRA-1/claim.planner"]; held {
		t.Errorf("ReadCurrent holds the planner's claim %s, want none: the whole read no longer carries it", claim)
	}
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"implementing"`, `"reviewing"`}) {
		t.Errorf("the phase's history is %v, want the starting read's, the event's, then the gap's read's", got)
	}

	// The read wrote three rows - the phase, the planner's claim it no longer carries and the
	// architect's claim - and every one of them is 1 µs after message 12's, the project's newest
	// row, though the phase's and the planner's own newest rows were a second older.
	written := map[string]time.Time{}
	for _, row := range observedRows(t, rig.database, "INFRA") {
		if row.at.After(latest) {
			written[row.issue+"/"+row.field] = row.at
		}
	}
	moment := latest.Add(time.Microsecond)
	if len(written) != 3 {
		t.Errorf("the gap's read wrote %v after the events, want the phase and the two claims", written)
	}
	for field, at := range written {
		if !at.Equal(moment) {
			t.Errorf("the gap's read wrote %s at %s, want every row of the read at %s, 1 µs after the project's newest row",
				field, at.Format(time.RFC3339Nano), moment.Format(time.RFC3339Nano))
		}
	}
}

// TestABootThatDoesNotFollowMakesOneWholeRead: a boot event whose sequence does not follow the
// cursor, one at or below it under a run the cursor has not followed (a daemon whose counter went
// back), and one that applied a migration each read the project whole once; a boot that follows
// reads nothing, and neither does a boot replayed under the run the cursor followed last.
func TestABootThatDoesNotFollowMakesOneWholeRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		// before is published and applied first; boot is the boot's message.
		before func(rig *intakeRig)
		boot   func(rig *intakeRig)
		// stateSeq is what the daemon serves at the boot's read, and reason the read's reason, ""
		// for none.
		stateSeq int
		reason   string
	}{
		{
			name:     "a boot past the next sequence",
			boot:     func(rig *intakeRig) { rig.publish("INFRA", booted(13, false, 2)) },
			stateSeq: 13, reason: ReadReasonBoot,
		},
		{
			name: "a boot at or below the cursor under another run",
			before: func(rig *intakeRig) {
				rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
			},
			boot: func(rig *intakeRig) {
				rig.publishOf("INFRA", "a7d1c0e2-6b8f-4c3a-9e51-0f2b8d4c6a19", time.Now().UTC().Truncate(time.Microsecond), booted(4, false, 2))
			},
			stateSeq: 4, reason: ReadReasonBoot,
		},
		{
			name:     "a boot that applied a migration",
			boot:     func(rig *intakeRig) { rig.publish("INFRA", booted(11, true, 2)) },
			stateSeq: 11, reason: ReadReasonMigrated,
		},
		{
			name:     "a boot that follows",
			boot:     func(rig *intakeRig) { rig.publish("INFRA", booted(11, false, 3)) },
			stateSeq: 11,
		},
		{
			// The boot and a change after it are applied under the rig's run, and the stream
			// delivers the boot again: at or below the cursor, under the cursor's own run.
			name: "a boot replayed under the cursor's own run",
			before: func(rig *intakeRig) {
				rig.publish("INFRA", booted(11, false, 3))
				rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "implementing")))
			},
			boot:     func(rig *intakeRig) { rig.publish("INFRA", booted(11, false, 3)) },
			stateSeq: 12,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newIntakeRig(t)
			daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
			seedLegionProject(t, rig.database, "INFRA", daemon.URL)
			rig.start()
			rig.awaitStartingRead("INFRA")
			if tc.before != nil {
				tc.before(rig)
				rig.settled()
			}
			daemon.serves(daemonState("INFRA", tc.stateSeq, planning("INFRA")))
			tc.boot(rig)
			rig.settled()

			reads := readRecords(t, rig.database, "INFRA")
			wholeReads := 0
			for _, row := range reads {
				if row.kind == readKindRead {
					wholeReads++
				}
			}
			if tc.reason == "" {
				if served := daemon.served(); served != 1 || wholeReads != 1 {
					t.Fatalf("the daemon served %d reads and legion_reads holds %v, want the starting read alone: the boot follows", served, reads)
				}
				if settings := currentValues(t, rig.database, "INFRA")["/settings"]; !strings.Contains(settings, `"admission_cap": 3`) {
					t.Errorf("the settings are %s, want the boot's", settings)
				}
				requireLastSeq(t, rig.database, "INFRA", int64(tc.stateSeq))
				rig.requireWholeReadLines("INFRA", ReadReasonStart)
				return
			}
			if served := daemon.served(); served != 2 || wholeReads != 2 {
				t.Fatalf("the daemon served %d reads and legion_reads holds %v, want the starting read and one for the boot", served, reads)
			}
			requireWholeRead(t, reads[len(reads)-1], tc.reason, true)
			requireLastSeq(t, rig.database, "INFRA", int64(tc.stateSeq))
			lines := rig.requireWholeReadLines("INFRA", ReadReasonStart, tc.reason)
			if lines[1]["stateSeq"] != float64(tc.stateSeq) {
				t.Errorf("the boot's read logged stateSeq %v, want %d", lines[1]["stateSeq"], tc.stateSeq)
			}
		})
	}
}

// TestAnEventAtOrBelowTheReadsSequenceWritesNothing: the whole read's stateSeq says every event at
// or below it is in the state it read, so a message the read already holds writes no observation,
// no legion_reads row and no move of the cursor.
func TestAnEventAtOrBelowTheReadsSequenceWritesNothing(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	rows := observations(t, rig.database, "INFRA")

	rig.publish("INFRA", change(9, "INFRA-1", moved("phase", "admitted")), change(10, "INFRA-1", moved("phase", "planning"), moved("slot", nil)))
	rig.publish("INFRA", change(10, "INFRA-1", moved("phase", "implementing")))
	rig.settled()

	if after := observations(t, rig.database, "INFRA"); !slices.Equal(after, rows) {
		t.Errorf("observations = %v, want the starting read's %v alone", after, rows)
	}
	if reads := readRecords(t, rig.database, "INFRA"); len(reads) != 1 {
		t.Errorf("legion_reads = %v, want the starting read alone", reads)
	}
	requireLastSeq(t, rig.database, "INFRA", 10)
	if served := daemon.served(); served != 1 {
		t.Errorf("the daemon served %d reads, want the starting one alone", served)
	}
}

// TestAProjectWithoutAStartingPointIsReadOnceAndNothingReadsItAgain: a project with a setting and
// no cursor is read whole once at each of the three moments it has none - the durable's first
// creation, a new setting, a PUT of the same URL (the settings wake) - and nothing else reads a
// daemon: a run of events, a wake that changed nothing, all apply with no read.
func TestAProjectWithoutAStartingPointIsReadOnceAndNothingReadsItAgain(t *testing.T) {
	rig := newIntakeRig(t)
	infra := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	linux := newFakeDaemon(t, daemonState("LINUX", 20, planning("LINUX")))
	seedLegionProject(t, rig.database, "INFRA", infra.URL)
	seedProject(t, rig.database, "LINUX")
	actor := model.Actor{Kind: "user", ID: "alice@example.com"}

	// The durable's first creation.
	rig.start()
	rig.awaitStartingRead("INFRA")
	for seq := 11; seq <= 13; seq++ {
		rig.publish("INFRA", change(seq, "INFRA-1", moved("slot", seq-10)))
	}
	rig.settled()

	// A new setting, which the settings wake hands the intake.
	if _, err := PutSettings(t.Context(), rig.database.Pool, "LINUX", linux.URL, actor); err != nil {
		t.Fatalf("set LINUX's daemon: %v", err)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(2)
	rig.publish("LINUX", change(21, "LINUX-1", moved("phase", "implementing")))
	rig.publish("LINUX", change(22, "LINUX-1", moved("phase", "testing")))
	rig.settled()

	// A PUT of the same URL forgets INFRA's starting point. The daemon's state now holds the
	// three events the intake applied, as a daemon's would.
	infra.serves(daemonState("INFRA", 13, planning("INFRA")))
	if _, err := PutSettings(t.Context(), rig.database.Pool, "INFRA", infra.URL, actor); err != nil {
		t.Fatalf("set INFRA's daemon again: %v", err)
	}
	rig.wake <- struct{}{}
	rig.awaitWholeReads(3)

	// A settings change that leaves both cursors, then more events: no read. The check is queued
	// on each project's runner before the messages reach it, so the settle covers it.
	rig.intake.SettingsChanged(t.Context())
	rig.publish("INFRA", change(14, "INFRA-1", moved("phase", "implementing")))
	rig.publish("LINUX", change(23, "LINUX-1", moved("phase", "reviewing")))
	rig.settled()

	if served := infra.served(); served != 2 {
		t.Errorf("INFRA's daemon served %d reads, want two: the first creation's and the PUT's", served)
	}
	if served := linux.served(); served != 1 {
		t.Errorf("LINUX's daemon served %d reads, want one: its new setting's", served)
	}
	infraReads, linuxReads := readRecords(t, rig.database, "INFRA"), readRecords(t, rig.database, "LINUX")
	var wholeReads []readRecord
	for _, row := range slices.Concat(infraReads, linuxReads) {
		if row.kind == readKindRead {
			wholeReads = append(wholeReads, row)
		}
	}
	if len(wholeReads) != 3 {
		t.Fatalf("whole reads = %v, want three", wholeReads)
	}
	for _, row := range wholeReads {
		requireWholeRead(t, row, ReadReasonStart, true)
	}
	if phase := currentValues(t, rig.database, "LINUX")["LINUX-1/phase"]; phase != `"reviewing"` {
		t.Errorf("LINUX's phase is %s, want its last message's reviewing", phase)
	}
	requireLastSeq(t, rig.database, "INFRA", 14)
	requireLastSeq(t, rig.database, "LINUX", 23)
	lines := rig.awaitLogRecords(wholeReadLine, 3)
	var projects []string
	for _, line := range lines {
		if line["reason"] != ReadReasonStart || line["level"] != "INFO" {
			t.Errorf("whole read line %v, want INFO with reason %s", line, ReadReasonStart)
		}
		projects = append(projects, fmt.Sprint(line["project"]))
	}
	if want := []string{"INFRA", "LINUX", "INFRA"}; !slices.Equal(projects, want) {
		t.Errorf("whole reads logged for %v, want %v", projects, want)
	}
}

// TestAFailedWholeReadIsTriedAgainAtTheNextMessageAndNoSooner: a starting read the daemon answers
// with a state that is no starting point, or with another project's, records a failed read naming
// why and owes the read; a state of the project's own still records its observations. The
// messages that arrive meanwhile are applied and the cursor follows them; neither the next message
// nor a settings change reads again inside ReadRetryAfter, and the first message after it does.
func TestAFailedWholeReadIsTriedAgainAtTheNextMessageAndNoSooner(t *testing.T) {
	issue := planning("INFRA")
	for _, tc := range []struct {
		name     string
		state    string
		status   int
		failure  string
		observed bool
	}{
		{
			name: "a state with no stateSeq",
			state: `{"daemon":{"project":"INFRA","schemaVersion":20261117000000},
				"settings":{"dispatch_url":"http://dispatch.internal.example"},"issues":{` + issue + `}}`,
			failure: ErrNoStateSeq.Error(), observed: true,
		},
		{
			name:    "a state whose stateSeq is 0",
			state:   daemonState("INFRA", 0, issue),
			failure: ErrStateSeqZero.Error(), observed: true,
		},
		{
			name:    "a state whose settings carry no dispatch_url",
			state:   strings.Replace(daemonState("INFRA", 4, issue), `"dispatch_url":"http://dispatch.internal.example",`, ``, 1),
			failure: ErrNoDispatchURL.Error(), observed: true,
		},
		{
			name:    "a state of another project",
			state:   strings.Replace(daemonState("INFRA", 4, issue), `"daemon":{"project":"INFRA"`, `"daemon":{"project":"LINUX"`, 1),
			failure: ErrStateOfAnotherProject.Error(),
		},
		{
			name:    "a daemon that refuses",
			status:  http.StatusServiceUnavailable,
			failure: ErrUnavailable.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newIntakeRig(t)
			daemon := newFakeDaemon(t, tc.state)
			if tc.status != 0 {
				daemon.refuses(tc.status)
			}
			seedLegionProject(t, rig.database, "INFRA", daemon.URL)
			rig.start()
			rig.awaitWholeReads(1)

			reads := readRecords(t, rig.database, "INFRA")
			requireWholeRead(t, reads[0], ReadReasonStart, false)
			if reads[0].error == nil || !strings.Contains(*reads[0].error, tc.failure) {
				t.Fatalf("the failed read's error is %v, want it to name %q", reads[0].error, tc.failure)
			}
			phase, observed := currentValues(t, rig.database, "INFRA")["INFRA-1/phase"]
			if observed != tc.observed || (observed && phase != `"planning"`) {
				t.Fatalf("after the failed read the phase is %q (observed %v), want observed %v", phase, observed, tc.observed)
			}
			if current := cursorOf(t, rig.database, "INFRA"); !current.readOwed || current.lastSeq != nil {
				t.Fatalf("the cursor after the failed read is %+v, want a read owed and no sequence", current)
			}
			line := rig.requireWholeReadLines("INFRA", ReadReasonStart)[0]
			if line["ok"] != false || !strings.Contains(fmt.Sprint(line["error"]), tc.failure) {
				t.Fatalf("the failed read's line is %v, want ok false and its error", line)
			}

			// Inside ReadRetryAfter, the next message and a settings change read nothing, and
			// the message is applied with the cursor following it.
			rig.clock.advance(ReadRetryAfter / 2)
			rig.publish("INFRA", change(5, "INFRA-1", moved("phase", "implementing")))
			rig.settled()
			rig.intake.SettingsChanged(t.Context())
			rig.publish("INFRA", change(6, "INFRA-1", claimMoved("planner", "idle")))
			rig.settled()
			if served := daemon.served(); served != 1 {
				t.Fatalf("the daemon served %d reads inside a minute of the failed one, want none more", served)
			}
			if phase := currentValues(t, rig.database, "INFRA")["INFRA-1/phase"]; phase != `"implementing"` {
				t.Fatalf("the phase is %s while the read is owed, want the message's implementing", phase)
			}
			if current := cursorOf(t, rig.database, "INFRA"); !current.readOwed || current.lastSeq == nil || *current.lastSeq != 6 {
				t.Fatalf("the cursor is %+v, want it at 6 with the read still owed", current)
			}

			// Past ReadRetryAfter, the next message reads the daemon again, and this time the
			// read is good: the cursor takes its stateSeq and owes nothing.
			daemon.serves(daemonState("INFRA", 7, strings.Replace(issue, `"planning"`, `"testing"`, 1)))
			rig.clock.advance(ReadRetryAfter/2 + time.Second)
			rig.publish("INFRA", change(7, "INFRA-1", moved("phase", "testing")))
			rig.settled()
			if served := daemon.served(); served != 2 {
				t.Fatalf("the daemon served %d reads, want one more once the minute had passed", served)
			}
			reads = readRecords(t, rig.database, "INFRA")
			requireWholeRead(t, reads[len(reads)-1], ReadReasonRetry, true)
			if current := cursorOf(t, rig.database, "INFRA"); current.readOwed || current.lastSeq == nil || *current.lastSeq != 7 {
				t.Fatalf("the cursor after the good read is %+v, want it at the read's 7 and nothing owed", current)
			}
			rig.requireWholeReadLines("INFRA", ReadReasonStart, ReadReasonRetry)
		})
	}
}

// TestAMessageForAProjectWithNoSettingIsDropped: a message of a project no legion_settings row
// names is acknowledged and writes nothing, and the drop is logged once per project per
// dropLogEvery: LINUX's second and third messages log nothing, and its first message after
// dropLogEvery has passed on the intake's clock logs again. The intake then remembers only the
// projects it logged in the last dropLogEvery, so GHOST, logged before it, is forgotten.
func TestAMessageForAProjectWithNoSettingIsDropped(t *testing.T) {
	rig := newIntakeRig(t)
	seedProject(t, rig.database, "LINUX")
	rig.start()
	rig.publish("LINUX", change(1, "LINUX-1", moved("phase", "planning")))
	// A project Dispatch does not hold at all.
	rig.publish("GHOST", change(1, "GHOST-1", moved("phase", "planning")))
	rig.publish("LINUX", change(2, "LINUX-1", moved("phase", "implementing")))
	rig.publish("LINUX", change(3, "LINUX-1", moved("phase", "testing")))
	rig.settled()

	for _, project := range []string{"LINUX", "GHOST"} {
		if rows := observations(t, rig.database, project); len(rows) != 0 {
			t.Errorf("%s's observations = %v, want none", project, rows)
		}
		if reads := readRecords(t, rig.database, project); len(reads) != 0 {
			t.Errorf("%s's legion_reads = %v, want none", project, reads)
		}
	}
	var cursors int
	if err := rig.database.Pool.QueryRow(t.Context(), `select count(*) from legion_event_cursors`).Scan(&cursors); err != nil {
		t.Fatalf("count the cursors: %v", err)
	}
	if cursors != 0 {
		t.Errorf("%d cursors, want none", cursors)
	}

	// Each drop is logged after its message is acknowledged. LINUX's runner takes its messages in
	// turn, so once the third is acknowledged the first two have logged what they log.
	logged := func() map[string]int {
		counts := map[string]int{}
		for _, line := range rig.logRecords(droppedLine) {
			counts[fmt.Sprint(line["project"])]++
		}
		return counts
	}
	var counts map[string]int
	rig.waitFor(5*time.Second, func() bool {
		counts = logged()
		return counts["LINUX"] > 0 && counts["GHOST"] > 0
	}, func() string { return fmt.Sprintf("drops logged %v, want LINUX and GHOST", counts) })
	if want := map[string]int{"LINUX": 1, "GHOST": 1}; !maps.Equal(counts, want) {
		t.Fatalf("drops logged %v, want %v: one line per project inside dropLogEvery", counts, want)
	}

	rig.clock.advance(dropLogEvery)
	rig.publish("LINUX", change(4, "LINUX-1", moved("phase", "reviewing")))
	rig.settled()
	rig.waitFor(5*time.Second, func() bool {
		counts = logged()
		return counts["LINUX"] >= 2
	}, func() string {
		return fmt.Sprintf("drops logged %v, want a second LINUX line once dropLogEvery passed", counts)
	})
	if want := map[string]int{"LINUX": 2, "GHOST": 1}; !maps.Equal(counts, want) {
		t.Errorf("drops logged %v, want %v", counts, want)
	}
	rig.intake.mu.Lock()
	remembered := slices.Sorted(maps.Keys(rig.intake.dropped))
	rig.intake.mu.Unlock()
	if !slices.Equal(remembered, []string{"LINUX"}) {
		t.Errorf("the intake remembers the drops of %v, want LINUX alone: GHOST's entry is older than dropLogEvery", remembered)
	}
}

// TestTheIntakePrunesTheReadsAtMostOnceAnHour: the intake prunes legion_reads (PruneReads) after a
// batch it applied, and then not again until pruneEvery has passed on its own clock. A read past
// ReadRetention written after a prune stays through the batches inside pruneEvery, and goes at the
// first batch after it.
func TestTheIntakePrunesTheReadsAtMostOnceAnHour(t *testing.T) {
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")
	aged := func() int64 {
		t.Helper()
		var id int64
		if err := rig.database.Pool.QueryRow(t.Context(), `
			insert into legion_reads (project, read_at, ok, bytes) values ('INFRA', $1, true, 1) returning id
		`, time.Now().Add(-ReadRetention-time.Hour)).Scan(&id); err != nil {
			t.Fatalf("write a read past the retention: %v", err)
		}
		return id
	}
	held := func(id int64) bool {
		t.Helper()
		var exists bool
		if err := rig.database.Pool.QueryRow(t.Context(),
			`select exists (select 1 from legion_reads where id = $1)`, id).Scan(&exists); err != nil {
			t.Fatalf("read legion_reads: %v", err)
		}
		return exists
	}
	prunedBy := func(id int64, what string) {
		t.Helper()
		rig.waitFor(10*time.Second, func() bool { return !held(id) }, func() string {
			return fmt.Sprintf("the read past the retention is still held after %s, want it pruned", what)
		})
	}

	first := aged()
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	rig.settled()
	prunedBy(first, "the first batch")

	// The runner takes INFRA's messages in turn and prunes after a batch is acknowledged, so once
	// 13 is acknowledged, 12's prune has been decided.
	second := aged()
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "testing")))
	rig.publish("INFRA", change(13, "INFRA-1", moved("phase", "reviewing")))
	rig.settled()
	if !held(second) {
		t.Fatal("a batch inside pruneEvery of the last prune pruned again, want no prune until pruneEvery has passed")
	}

	rig.clock.advance(pruneEvery)
	rig.publish("INFRA", change(14, "INFRA-1", moved("phase", "accepting")))
	rig.settled()
	prunedBy(second, "the first batch past pruneEvery")
}

// TestAnApplyThatFailsIsRetriedBeforeAnyLaterMessage: a batch the database refuses is tried again
// in place, and the project's later message waits behind it, so once the database takes the
// writes again both are applied in sequence order and the history holds each value the daemon
// committed.
func TestAnApplyThatFailsIsRetriedBeforeAnyLaterMessage(t *testing.T) {
	first, last := applyFirstRetry, applyLastRetry
	applyFirstRetry, applyLastRetry = 20*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { applyFirstRetry, applyLastRetry = first, last })
	rig := newIntakeRig(t)
	daemon := newFakeDaemon(t, daemonState("INFRA", 10, planning("INFRA")))
	seedLegionProject(t, rig.database, "INFRA", daemon.URL)
	rig.start()
	rig.awaitStartingRead("INFRA")

	if _, err := rig.database.Pool.Exec(t.Context(), `
		create function refuse_legion_events() returns trigger language plpgsql as $$
		begin
			raise exception 'the test refuses every batch of state events';
		end;
		$$;
		create trigger refuse_legion_events before insert on legion_reads
			for each row when (new.kind = 'events') execute function refuse_legion_events();
	`); err != nil {
		t.Fatalf("refuse the batches: %v", err)
	}
	rig.publish("INFRA", change(11, "INFRA-1", moved("phase", "implementing")))
	rig.publish("INFRA", change(12, "INFRA-1", moved("phase", "testing")))
	retried := func() int {
		count := 0
		for _, line := range rig.logRecords("legion intake: a database step failed; trying it again") {
			if line["project"] == "INFRA" {
				count++
			}
		}
		return count
	}
	rig.waitFor(20*time.Second, func() bool { return retried() >= 3 }, func() string {
		return fmt.Sprintf("the refused batch was tried %d times, want it tried again in place", retried())
	})
	if phase := currentValues(t, rig.database, "INFRA")["INFRA-1/phase"]; phase != `"planning"` {
		t.Fatalf("the phase is %s while the batches are refused, want the read's planning", phase)
	}
	requireLastSeq(t, rig.database, "INFRA", 10)

	if _, err := rig.database.Pool.Exec(t.Context(), `drop trigger refuse_legion_events on legion_reads`); err != nil {
		t.Fatalf("take the batches again: %v", err)
	}
	rig.settled()
	if got := history(t, rig.database, "INFRA", "INFRA-1", FieldPhase); !slices.Equal(got, []string{`"planning"`, `"implementing"`, `"testing"`}) {
		t.Errorf("the phase's history is %v, want the read's planning, then 11's implementing, then 12's testing", got)
	}
	requireLastSeq(t, rig.database, "INFRA", 12)
	events := 0
	for _, row := range readRecords(t, rig.database, "INFRA") {
		if row.kind == readKindEvents {
			events++
		}
	}
	if events != 2 {
		t.Errorf("%d batches recorded, want the two messages once each", events)
	}
}

// goldenStep is one step of the state lane's golden: what the daemon did; the one message its lane
// published for it, with the run, the last sequence and the commit time its payload names; and the
// state document the daemon served after it.
type goldenStep struct {
	name     string
	envelope contracts.Envelope
	run      string
	seq      int64
	at       time.Time
	state    json.RawMessage
}

// readStateEventsGolden reads the Legion daemon's golden of its state lane, state-events.json,
// which the daemon's own TestStateEventsGolden writes
// (packages/daemon/internal/daemon/state_events_golden_test.go): a daemon of project LEGION
// driven step by step over a seeded store, each step with every message its state lane published,
// as the Envoy listener's publish route stores the envelope, and the document GET /legion/v1/state
// served once they were published. It fails unless each step is one message and a state whose
// stateSeq is that message's last sequence: one committed transaction, and the state a whole read
// made right after it reads.
func readStateEventsGolden(t *testing.T) []goldenStep {
	t.Helper()
	raw, err := os.ReadFile(daemonAPIFixture("state-events.json"))
	if err != nil {
		t.Fatalf("read the daemon's state events golden: %v", err)
	}
	var golden struct {
		Steps []struct {
			Name     string               `json:"name"`
			Messages []contracts.Envelope `json:"messages"`
			State    json.RawMessage      `json:"state"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("decode the daemon's state events golden: %v", err)
	}
	if len(golden.Steps) == 0 {
		t.Fatal("the daemon's state events golden holds no step, so there is nothing to compare")
	}
	steps := make([]goldenStep, 0, len(golden.Steps))
	for _, step := range golden.Steps {
		if len(step.Messages) != 1 {
			t.Fatalf("step %q holds %d messages, want one: a step's state is compared with the rows of one committed transaction", step.Name, len(step.Messages))
		}
		var message struct {
			Run string    `json:"run"`
			Seq int64     `json:"seq"`
			At  time.Time `json:"at"`
		}
		if err := json.Unmarshal([]byte(step.Messages[0].Payload), &message); err != nil {
			t.Fatalf("decode the message of step %q: %v", step.Name, err)
		}
		var state struct {
			Daemon struct {
				StateSeq int64 `json:"stateSeq"`
			} `json:"daemon"`
		}
		if err := json.Unmarshal(step.State, &state); err != nil {
			t.Fatalf("decode the state of step %q: %v", step.Name, err)
		}
		if state.Daemon.StateSeq != message.Seq {
			t.Fatalf("the state of step %q serves stateSeq %d and its message ends at %d: the state must hold the events up to the message's last and none after it",
				step.Name, state.Daemon.StateSeq, message.Seq)
		}
		steps = append(steps, goldenStep{
			name: step.Name, envelope: step.Messages[0], run: message.Run, seq: message.Seq, at: message.At, state: step.State,
		})
	}
	return steps
}

// observedRow is one legion_observations row: its issue and field, its value as jsonb writes it,
// and when it was observed.
type observedRow struct {
	issue, field, value string
	at                  time.Time
}

func (r observedRow) String() string {
	return fmt.Sprintf("%s/%s=%s@%s", r.issue, r.field, r.value, r.at.UTC().Format(time.RFC3339Nano))
}

// observedRows is project's legion_observations, by issue, field and time.
func observedRows(t *testing.T, database *store.Store, project string) []observedRow {
	t.Helper()
	rows, err := database.Pool.Query(context.Background(), `
		select issue_key, field, value::text, observed_at from legion_observations
		where project = $1 order by issue_key, field, observed_at
	`, project)
	if err != nil {
		t.Fatalf("read the observations of %s: %v", project, err)
	}
	listed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (observedRow, error) {
		var observed observedRow
		err := row.Scan(&observed.issue, &observed.field, &observed.value, &observed.at)
		return observed, err
	})
	if err != nil {
		t.Fatalf("read the observations of %s: %v", project, err)
	}
	return listed
}

// rowsOutside is each of rows that others does not hold: the same issue, field, value and time.
func rowsOutside(rows, others []observedRow) []observedRow {
	held := make(map[string]bool, len(others))
	for _, row := range others {
		held[row.String()] = true
	}
	var outside []observedRow
	for _, row := range rows {
		if !held[row.String()] {
			outside = append(outside, row)
		}
	}
	return outside
}

// currentDifference is each issue and field whose legion_current row differs between two
// projects' currentValues, in key order.
func currentDifference(events, reads map[string]string) []string {
	keys := slices.Collect(maps.Keys(events))
	for key := range reads {
		if _, held := events[key]; !held {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	text := func(values map[string]string, key string) string {
		if value, held := values[key]; held {
			return value
		}
		return "no row"
	}
	var differing []string
	for _, key := range keys {
		if fromEvents, fromReads := text(events, key), text(reads, key); fromEvents != fromReads {
			differing = append(differing, fmt.Sprintf("%s: %s from the events, %s from the reads", key, fromEvents, fromReads))
		}
	}
	return differing
}

// orNone is *value's text, or "none" for nil.
func orNone[T any](value *T) string {
	if value == nil {
		return "none"
	}
	return fmt.Sprint(*value)
}

// TestEventsWriteTheRowsAWholeReadOfTheSameStatesWrites: the rows the daemon's state events write
// are the rows a whole read of the same states writes. The daemon's own golden of its state lane
// (readStateEventsGolden) is applied twice over. Project LEGION, the golden's own, takes each
// step's message through the intake from a real NATS stream, published as the daemon's listener
// stored it; project READS records each step's state document as a whole read records it
// (RecordRead). After each step the two hold the same legion_current, `null` rows included, and
// the same history: a row of LEGION only where READS has one, with the same value, at the same
// moment, which is the message's `at`.
//
// Both start from the golden's first state: the intake's starting read of LEGION's daemon is served
// it, and READS records it. The intake's clock stands at the commit time of the step it applies
// (its message's `at`), the moment READS records the step's state at, so the starting read is
// stamped at the boot's commit. A batch's row is stamped 1 µs after its field's newest row, and a
// whole read's rows 1 µs after the project's newest row, where that is later than their own time
// (recordEvents, recordObservations), but each step of the golden commits a minute after the one
// before, so that rule moves no row of either project here: every row a step writes is at its
// message's `at`, and the test fails on one that is not.
//
// The boot's message is at the starting read's stateSeq, and a whole read names no run, so the
// intake cannot tell it from a daemon whose counter went back and reads the daemon whole once more
// (stateMessage.unfollowed); the daemon still serves the first state, so that read writes nothing.
// From the second step on the daemon is never read again: each step's rows are its message's.
func TestEventsWriteTheRowsAWholeReadOfTheSameStatesWrites(t *testing.T) {
	const events, reads = "LEGION", "READS"
	steps := readStateEventsGolden(t)
	rig := newIntakeRig(t)
	var clock atomic.Pointer[time.Time]
	clock.Store(&steps[0].at)
	daemon := newFakeDaemon(t, string(steps[0].state))
	seedLegionProject(t, rig.database, events, daemon.URL)
	rig.startOn(func() time.Time { return *clock.Load() })
	rig.awaitStartingRead(events)
	// READS has a setting too, written once the take-over has read the settings, and nothing
	// queues another starting point (no take-over, no settings wake), so its daemon is never read
	// and its rows are RecordRead's alone.
	readsDaemon := newFakeDaemon(t, `{}`)
	seedLegionProject(t, rig.database, reads, readsDaemon.URL)

	wholeReads := func() []string {
		var reasons []string
		for _, line := range rig.logRecords(wholeReadLine) {
			if line["project"] != events {
				continue
			}
			if line["ok"] != true {
				t.Fatalf("a whole read of %s failed: %v", events, line)
			}
			reasons = append(reasons, fmt.Sprint(line["reason"]))
		}
		return reasons
	}
	var before []observedRow
	var firstReads []string
	var firstServed, fromEvents int
	for index, step := range steps {
		name := fmt.Sprintf("step %d (%s)", index+1, step.name)
		clock.Store(&step.at)
		daemon.serves(string(step.state))
		if err := rig.publisher.Publish(step.envelope); err != nil {
			t.Fatalf("%s: publish its message: %v", name, err)
		}
		rig.settled()
		var state State
		if err := json.Unmarshal(step.state, &state); err != nil {
			t.Fatalf("%s: decode its state: %v", name, err)
		}
		if _, err := RecordRead(t.Context(), rig.database.Pool, reads, step.at, len(step.state), state.Observations()); err != nil {
			t.Fatalf("%s: record its state as a whole read: %v", name, err)
		}

		if differing := currentDifference(currentValues(t, rig.database, events), currentValues(t, rig.database, reads)); len(differing) > 0 {
			t.Fatalf("%s: legion_current differs between the events and the reads:\n%s", name, strings.Join(differing, "\n"))
		}
		eventsRows, readsRows := observedRows(t, rig.database, events), observedRows(t, rig.database, reads)
		if onlyEvents, onlyReads := rowsOutside(eventsRows, readsRows), rowsOutside(readsRows, eventsRows); len(onlyEvents)+len(onlyReads) > 0 {
			t.Fatalf("%s: the histories differ\nrows only the events wrote: %v\nrows only the reads wrote: %v", name, onlyEvents, onlyReads)
		}
		written := rowsOutside(eventsRows, before)
		for _, row := range written {
			if !row.at.Equal(step.at) {
				t.Errorf("%s: the row %s is not at the message's at %s", name, row, step.at.Format(time.RFC3339Nano))
			}
		}
		before = eventsRows
		current := cursorOf(t, rig.database, events)
		if current.lastSeq == nil || *current.lastSeq != step.seq || current.readOwed {
			t.Fatalf("%s: the cursor is at %s with a read owed %v, want it at %d with none owed", name, orNone(current.lastSeq), current.readOwed, step.seq)
		}

		if index == 0 {
			firstReads, firstServed = wholeReads(), daemon.served()
			t.Logf("%s: seq %d at %s; the whole reads %v wrote %d rows", name, step.seq, step.at.Format(time.RFC3339Nano), firstReads, len(written))
			continue
		}
		if served, reasons := daemon.served(), wholeReads(); served != firstServed || !slices.Equal(reasons, firstReads) {
			t.Fatalf("%s: the daemon served %d reads and the whole reads logged are %v, want the first step's %d and %v: a step's rows must be its message's",
				name, served, reasons, firstServed, firstReads)
		}
		var batches int
		if err := rig.database.Pool.QueryRow(t.Context(), `
			select count(*) from legion_reads where project = $1 and kind = $2 and read_at = $3
		`, events, readKindEvents, step.at).Scan(&batches); err != nil {
			t.Fatalf("%s: count its batches: %v", name, err)
		}
		if batches != 1 {
			t.Fatalf("%s: %d batches of events are recorded at its message's at, want one", name, batches)
		}
		if current.run == nil || *current.run != step.run {
			t.Fatalf("%s: the cursor follows the run %s, want the message's %s", name, orNone(current.run), step.run)
		}
		fromEvents += len(written)
		t.Logf("%s: seq %d at %s; its message wrote %d rows", name, step.seq, step.at.Format(time.RFC3339Nano), len(written))
	}

	if fromEvents == 0 {
		t.Error("the messages wrote no row, so nothing past the starting point was compared")
	}
	if !slices.Contains(slices.Collect(maps.Values(currentValues(t, rig.database, events))), "null") {
		t.Error("legion_current holds no null row, so no null row was compared")
	}
	if served := readsDaemon.served(); served != 0 {
		t.Errorf("the daemon of %s served %d reads, want none: its rows must be RecordRead's alone", reads, served)
	}
	var cursors int
	if err := rig.database.Pool.QueryRow(t.Context(), `select count(*) from legion_event_cursors where project = $1`, reads).Scan(&cursors); err != nil {
		t.Fatalf("count the cursors of %s: %v", reads, err)
	}
	if cursors != 0 {
		t.Errorf("%s has a cursor, want none: the intake never touched it", reads)
	}
}
