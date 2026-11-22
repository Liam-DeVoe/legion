package delivery

// The fresh consumer names of the intakes' two durables (fresh_names.go), each row run for
// delivery-events and delivery-jobs through the rig of fresh_harness_test.go: envoy-dispatch
// processes over one Postgres, connecting through the auth service by the names their take-overs
// give each connection. Each race regression fails at the build before the fresh mode, whose
// take-over deletes and creates one fixed name; each compatibility control passes at both. The
// rows named TestForcedOrder… run on both hooked nats-server versions under
// scripts/forced-order.sh as well as here.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	natsgo "github.com/nats-io/nats.go"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// Compatibility control, the fixed mode: a process of the default mode takes its fixed durable
// over, delivers from it, and writes no selection row; with the selection table gone it still
// does, since its take-over reads nothing of the table (only its start and its reconnects read
// the sentinel, CheckNamingMode).
func TestTheFixedModeTakesItsFixedDurableAndReadsNoSelectionTable(t *testing.T) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			pool, _ := deliveryTestPool(t)
			hub := startFreshHub(t)
			hub.createStream(t, 0)
			spy := spyConsumerCalls(t, hub)
			first := startDurableProcess(t, hub, pool, kind, fixedNames)
			_, js := hub.admin(t)
			waitFor(t, 40*time.Second, func() bool {
				info, err := js.ConsumerInfo(bus.Stream, kind.logical)
				return err == nil && info.PushBound
			}, func() string { return "the fixed durable " + kind.logical + " is not delivering" })
			sent := hub.publish(t, kind, 3)
			awaitHandled(t, sent, first)
			if rows := selectionRows(t, pool); rows != 0 {
				t.Fatalf("the fixed mode wrote %d selection rows", rows)
			}
			first.stop()
			if _, err := pool.Exec(t.Context(), `drop table if exists intake_consumers`); err != nil {
				t.Fatalf("drop the selection table: %v", err)
			}
			second := startDurableProcess(t, hub, pool, kind, fixedNames)
			sent = append(sent, hub.publish(t, kind, 3)...)
			awaitHandled(t, sent, first, second)
			created := spy.createdNames()
			if len(created) == 0 {
				t.Fatal("UNKNOWN: the spy saw no consumer create at all, so it shows nothing")
			}
			for _, name := range created {
				if name != kind.logical {
					t.Errorf("the fixed mode created %s, want only its fixed durable %s", name, kind.logical)
				}
			}
		})
	}
}

// The fixed mode refuses to start over a selection table the cutover converted, naming the
// setting, and starts over one the fixed names own: no table, no sentinel, a cutover not run or
// one rolled back. The fresh mode is refused nothing here. The unconverted rows are the control:
// the same check, on a store it must pass, passes.
func TestTheFixedModeRefusesAStoreTheCutoverConverted(t *testing.T) {
	for name, row := range map[string]struct {
		prepare func(t *testing.T, pool *store.Pool)
		mode    consumernames.Mode
		refused bool
	}{
		"fixed over a converted store":       {func(t *testing.T, pool *store.Pool) { writeCutover(t, pool, consumernames.CutoverDone) }, consumernames.ModeFixed, true},
		"the default over a converted store": {func(t *testing.T, pool *store.Pool) { writeCutover(t, pool, consumernames.CutoverDone) }, "", true},
		"fixed with no sentinel":             {func(*testing.T, *store.Pool) {}, consumernames.ModeFixed, false},
		"fixed with no table": {func(t *testing.T, pool *store.Pool) {
			if _, err := pool.Exec(t.Context(), `drop table intake_consumers`); err != nil {
				t.Fatalf("drop the table: %v", err)
			}
		}, consumernames.ModeFixed, false},
		"fixed before the cutover":        {func(t *testing.T, pool *store.Pool) { writeCutover(t, pool, "") }, consumernames.ModeFixed, false},
		"fixed after a rollback":          {func(t *testing.T, pool *store.Pool) { writeCutover(t, pool, consumernames.CutoverRolledBack) }, consumernames.ModeFixed, false},
		"fresh over a converted store":    {func(t *testing.T, pool *store.Pool) { writeCutover(t, pool, consumernames.CutoverDone) }, consumernames.ModeFresh, false},
		"fresh over an unconverted store": {func(*testing.T, *store.Pool) {}, consumernames.ModeFresh, false},
	} {
		t.Run(name, func(t *testing.T) {
			pool, ctx := deliveryTestPool(t)
			row.prepare(t, pool)
			err := CheckNamingMode(ctx, pool, row.mode)
			switch {
			case row.refused && (err == nil || !strings.Contains(err.Error(), "ENVOY_CONSUMER_NAMES=fixed") || !strings.Contains(err.Error(), "ENVOY_CONSUMER_NAMES=fresh")):
				t.Fatalf("CheckNamingMode = %v, want a refusal naming ENVOY_CONSUMER_NAMES=fixed and what to set", err)
			case !row.refused && err != nil:
				t.Fatalf("CheckNamingMode = %v, want the process to start", err)
			}
		})
	}
}

// scriptedMarker is a database whose reads of the cutover row answer, in turn, each of answers:
// an error the Scan returns, or the sentinel's record. Past the last it repeats the last.
type scriptedMarker struct {
	mu      sync.Mutex
	answers []any
	reads   int
}

func (s *scriptedMarker) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer := s.answers[min(s.reads, len(s.answers)-1)]
	s.reads++
	return scriptedRow{ctx: ctx, answer: answer}
}

func (s *scriptedMarker) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

type scriptedRow struct {
	ctx    context.Context
	answer any
}

// errHangs is an answer that waits for the read's context to end, as a database that does not
// answer does.
var errHangs = errors.New("the database does not answer")

func (r scriptedRow) Scan(dest ...any) error {
	switch answer := r.answer.(type) {
	case error:
		if errors.Is(answer, errHangs) {
			<-r.ctx.Done()
			return r.ctx.Err()
		}
		return answer
	case string:
		*dest[0].(*[]byte) = []byte(answer)
		*dest[1].(*int64) = 1
	}
	return nil
}

// A fixed process decides on the marker it read and on nothing else. A missing table or sentinel
// row is no marker, and the process starts. A read Postgres refused (42501), a database it cannot
// reach, one that does not answer, or a row it cannot read -- a row at Dispatch's sentinel key that
// is no sentinel of Dispatch's: another family's, one naming no family, or one under another key --
// it reads again until it can, and never starts unread: the call returns only with the marker read,
// or when its context ends. The control is the same unreachable database answering at last: done
// refuses, and no marker starts.
func TestTheFixedModeReadsItsMarkerBeforeItRuns(t *testing.T) {
	shrink := namingMarkerRetry
	namingMarkerRetry = 10 * time.Millisecond
	t.Cleanup(func() { namingMarkerRetry = shrink })
	unreachable := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	for name, row := range map[string]struct {
		answers []any
		refused bool
	}{
		"a read Postgres refused, then no marker": {[]any{&pgconn.PgError{Code: "42501", Message: "permission denied for table intake_consumers"}, pgx.ErrNoRows}, false},
		"no table":                      {[]any{&pgconn.PgError{Code: "42P01", Message: "relation does not exist"}}, false},
		"no sentinel":                   {[]any{pgx.ErrNoRows}, false},
		"unreachable, then done":        {[]any{unreachable, unreachable, `{"key":"envoy.sentinel/dispatch","family":"dispatch","cutover":"done"}`}, true},
		"not answering, then no marker": {[]any{errHangs, errHangs, pgx.ErrNoRows}, false},
		"a row it cannot read, then one before cutover": {[]any{`{"cutover":`, `{"key":"envoy.sentinel/dispatch","family":"dispatch"}`}, false},
		"a daemon's sentinel, then no marker":           {[]any{`{"key":"envoy.sentinel/dispatch","family":"daemon/p","cutover":"done"}`, pgx.ErrNoRows}, false},
		"the session's sentinel, then no marker":        {[]any{`{"key":"envoy.sentinel/dispatch","family":"session","cutover":"done"}`, pgx.ErrNoRows}, false},
		"a sentinel of no family, then no marker":       {[]any{`{"key":"envoy.sentinel/dispatch","cutover":"done"}`, pgx.ErrNoRows}, false},
		"another key's sentinel, then no marker":        {[]any{`{"key":"envoy.sentinel/session","family":"dispatch","cutover":"done"}`, pgx.ErrNoRows}, false},
	} {
		t.Run(name, func(t *testing.T) {
			db := &scriptedMarker{answers: row.answers}
			err := CheckNamingMode(t.Context(), db, consumernames.ModeFixed)
			if row.refused != (err != nil) {
				t.Fatalf("CheckNamingMode = %v, want refused %t", err, row.refused)
			}
			if want := len(row.answers); db.count() < want {
				t.Fatalf("it read the marker %d times, want at least %d: it decided before it read", db.count(), want)
			}
		})
	}
	t.Run("never answering, it never starts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		db := &scriptedMarker{answers: []any{unreachable}}
		if err := CheckNamingMode(ctx, db, consumernames.ModeFixed); err == nil {
			t.Fatal("CheckNamingMode started a fixed process over a marker it never read")
		}
		if db.count() < 2 {
			t.Fatalf("it read the marker %d times in 300 ms, want it to keep reading", db.count())
		}
	})
}

// readSignal is a database whose every read of a row signals read once its Scan has returned.
type readSignal struct {
	consumernames.PGQuerier
	read chan struct{}
}

func (r readSignal) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return signalledRow{Row: r.PGQuerier.QueryRow(ctx, sql, args...), read: r.read}
}

type signalledRow struct {
	pgx.Row
	read chan struct{}
}

func (r signalledRow) Scan(dest ...any) error {
	defer func() { r.read <- struct{}{} }()
	return r.Row.Scan(dest...)
}

// A fixed process whose database role may not read the cutover row has not read its marker:
// Postgres's refusal of the read (SQLSTATE 42501) is a read error, never no marker, so the start
// reads again until its context ends and never starts, and each take-over refuses before it
// changes anything. Only a missing table or row is no marker. Before the fix the refusal read as no
// marker, and the process ran the fixed names over a store it could not read.
func TestAFixedProcessWhoseRoleMayNotReadTheMarkerNeverRuns(t *testing.T) {
	shrink := namingMarkerRetry
	namingMarkerRetry = 10 * time.Millisecond
	t.Cleanup(func() { namingMarkerRetry = shrink })
	pool, ctx := deliveryTestPool(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	role := fmt.Sprintf("t4_marker_reader_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "create role "+role+" nologin"); err != nil {
		t.Fatalf("create the reading role: %v", err)
	}
	if _, err := conn.Exec(ctx, "set role "+role); err != nil {
		t.Fatalf("take the reading role: %v", err)
	}
	// The connection holds the role, and the start's deadline below can close it mid-read, so it
	// goes back to no pool: it is taken out and closed. The role is dropped through the pool, and a
	// role still there afterwards fails the row, so none outlives it on a long-lived server.
	t.Cleanup(func() {
		_ = conn.Hijack().Close(context.Background())
		if _, err := pool.Exec(context.Background(), "drop role "+role); err != nil {
			t.Errorf("drop the reading role %s: %v", role, err)
		}
		var left int
		if err := pool.QueryRow(context.Background(), "select count(*) from pg_roles where rolname = $1", role).Scan(&left); err != nil || left != 0 {
			t.Errorf("the reading role %s is still there after the row (%d, %v)", role, left, err)
		}
	})
	db := conn.Conn()

	// The role's read is refused, so the rows below test the refusal.
	_, err = readFixedMarker(ctx, db)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "42501" {
		t.Fatalf("the role's read of the cutover row = %v, want refused (42501)", err)
	}
	d := &heldDurable{name: bus.DispatchDeliveryDurable, throttle: &bindThrottle{}}
	d.guardFixedNames(db, func(err error) { t.Errorf("told the store converted over a marker it could not read: %v", err) })
	if d.fixedNamesAllowed(ctx) {
		t.Fatal("a take-over went on over a marker its role may not read")
	}
	// Last: the start's deadline can cut a read in flight, which closes the connection.
	starting, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := CheckNamingMode(starting, db, consumernames.ModeFixed); err == nil {
		t.Fatal("the fixed process started over a marker its role may not read")
	}
}

// A fixed process's read of its marker after a reconnect is retried every namingMarkerRetry until
// it answers: one reconnect whose first read the database fails, and whose next read finds the
// cutover done, makes the process exit, with no second reconnect. Before the fix the watch waited
// for the next reconnect, which may never come.
func TestAFixedProcessRetriesAFailedMarkerReadAfterAReconnect(t *testing.T) {
	shrink := namingMarkerRetry
	namingMarkerRetry = 10 * time.Millisecond
	t.Cleanup(func() { namingMarkerRetry = shrink })
	db := &scriptedMarker{answers: []any{errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"), `{"key":"envoy.sentinel/dispatch","family":"dispatch","cutover":"done"}`}}
	reconnects := make(chan struct{}, 1)
	told := make(chan error, 1)
	go WatchFixedMarker(t.Context(), db, reconnects, func(err error) { told <- err })
	reconnects <- struct{}{}
	select {
	case err := <-told:
		if !strings.Contains(err.Error(), "ENVOY_CONSUMER_NAMES=fixed") {
			t.Fatalf("told %v, want the refusal naming ENVOY_CONSUMER_NAMES=fixed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("one reconnect whose first read failed told the process nothing (%d reads)", db.count())
	}
}

// A fixed intake reads the cutover marker under its consumer lease before its take-over deletes or
// creates anything, so a fixed process the cutover converted the store under never creates its
// fixed durable again, whichever of its connections comes back first. The convert writes the
// marker done and deletes the fixed durable; the intake's own connection then reconnects, and
// neither the database listener nor the publisher signals anything. The intake's next take-over
// reads done, refuses, and tells the process to exit, naming the setting. Before the fix it
// created the fixed name again: for delivery-events, at deliver policy all, every retained event.
func TestAFixedIntakeRefusesAConvertedStoreAtItsTakeOver(t *testing.T) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			pool, _ := deliveryTestPool(t)
			hub := startFreshHub(t)
			hub.createStream(t, 0)
			converted := make(chan error, 16)
			process := startDurableProcess(t, hub, pool, kind, fixedNames, guardedFixed(func(err error) {
				select {
				case converted <- err:
				default:
				}
			}))
			_, js := hub.admin(t)
			waitFor(t, 40*time.Second, func() bool {
				info, err := js.ConsumerInfo(bus.Stream, kind.logical)
				return err == nil && info.PushBound
			}, func() string { return "the fixed durable " + kind.logical + " is not delivering" })
			spy := spyConsumerCalls(t, hub)

			writeCutover(t, pool, consumernames.CutoverDone)
			if err := js.DeleteConsumer(bus.Stream, kind.logical); err != nil {
				t.Fatalf("the convert's delete of %s: %v", kind.logical, err)
			}
			if err := process.held.nats.Conn.ForceReconnect(); err != nil {
				t.Fatalf("reconnect the intake's connection: %v", err)
			}
			waitFor(t, 30*time.Second, func() bool {
				return len(converted) > 0 || slices.Contains(spy.createdNames(), kind.logical)
			}, func() string { return "the intake neither refused nor created anything after the convert" })
			if slices.Contains(spy.createdNames(), kind.logical) {
				t.Fatalf("the fixed intake created %s again over a converted store", kind.logical)
			}
			if err := <-converted; !strings.Contains(err.Error(), "ENVOY_CONSUMER_NAMES=fixed") {
				t.Fatalf("told %v, want the refusal naming ENVOY_CONSUMER_NAMES=fixed", err)
			}
			// Later attempts refuse too: two more of them create nothing.
			time.Sleep(2 * durableBindInterval)
			if slices.Contains(spy.createdNames(), kind.logical) {
				t.Fatalf("a later take-over created %s again over a converted store", kind.logical)
			}
		})
	}
}

// Defence in depth: a fixed process reads its marker again each time its database or NATS
// reconnects, and is told to exit once it reads done. The control: a reconnect before done was
// written, and done written with no reconnect after it, tell it nothing, so it is the reconnect
// after done that reads it. Each read is awaited before the row writes, so no read races a write.
func TestAFixedProcessExitsWhenAReconnectReadsTheCutoverDone(t *testing.T) {
	pool, ctx := deliveryTestPool(t)
	db := readSignal{PGQuerier: pool, read: make(chan struct{}, 1)}
	reconnects := make(chan struct{})
	told := make(chan error, 1)
	go WatchFixedMarker(ctx, db, reconnects, func(err error) { told <- err })
	awaitRead := func() {
		t.Helper()
		select {
		case <-db.read:
		case <-time.After(10 * time.Second):
			t.Fatal("the reconnect read nothing")
		}
	}
	reconnects <- struct{}{}
	awaitRead()
	writeCutover(t, pool, consumernames.CutoverDone)
	select {
	case err := <-told:
		t.Fatalf("told %v with no reconnect since done was written", err)
	case <-time.After(500 * time.Millisecond):
	}
	reconnects <- struct{}{}
	awaitRead()
	select {
	case err := <-told:
		if err == nil || !strings.Contains(err.Error(), "ENVOY_CONSUMER_NAMES=fixed") {
			t.Fatalf("told %v, want the refusal naming ENVOY_CONSUMER_NAMES=fixed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a reconnect after done was written told the process nothing")
	}
}

// freshRig is one row's hub, database and stream, with what the cutover leaves written: on one
// copy, the lone hub through the auth service.
func freshRig(t *testing.T, maxConsumers int) (*freshHub, *store.Pool) {
	t.Helper()
	return freshRigOn(t, hubTopologies[0], maxConsumers)
}

// freshRigOn is freshRig on topology.
func freshRigOn(t *testing.T, topology hubTopology, maxConsumers int) (*freshHub, *store.Pool) {
	t.Helper()
	pool, _ := deliveryTestPool(t)
	hub := topology.start(t)
	hub.createStream(t, maxConsumers)
	readyForFresh(t, pool)
	return hub, pool
}

// onEveryHub runs row on one copy, an R1 cluster and R3: a consumer's store, and so what a
// take-over leaves behind, differ between a lone server and a cluster's Raft-replicated one.
func onEveryHub(t *testing.T, row func(t *testing.T, topology hubTopology)) {
	for _, topology := range hubTopologies {
		t.Run(topology.name, func(t *testing.T) { row(t, topology) })
	}
}

// Race regression, the fixed-name spy: across a holder's stop, a standby's take-over and a second
// change of holder, no consumer name is sent to a create twice, and no fixed name is created or
// deleted, while every message reaches a process. The build before the fresh mode deletes and
// creates its fixed durable at each take-over.
func TestNoConsumerNameIsCreatedTwice(t *testing.T) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			hub, pool := freshRig(t, 0)
			spy := spyConsumerCalls(t, hub)
			first := startDurableProcess(t, hub, pool, kind, freshNames)
			awaitDelivering(t, hub, pool, kind.logical)
			second := startDurableProcess(t, hub, pool, kind, freshNames)
			sent := hub.publish(t, kind, 4)
			awaitHandled(t, sent, first, second)

			first.stop()
			sent = append(sent, hub.publish(t, kind, 4)...)
			awaitHandled(t, sent, first, second)
			third := startDurableProcess(t, hub, pool, kind, freshNames)
			second.stop()
			sent = append(sent, hub.publish(t, kind, 4)...)
			awaitHandled(t, sent, first, second, third)

			spy.requireEachNameCreatedOnce(t)
			if created := spy.createdNames(); len(created) < 3 {
				t.Errorf("creates %v: want one per holder, three at least, or the row changed no holder", created)
			}
		})
	}
}

// Race regression: every reader of the durable reads the record's current. The take-over binds
// the current's physical name at its deliver subject, the check reads the current and writes its
// ack floor as the record's last floor, and a current removed outside the intake is created again
// at the last floor + 1, so the messages published while it was gone arrive, and no first-create
// rule (DeliverNew for the jobs durable) skips them.
func TestEveryReaderFindsTheCurrentName(t *testing.T) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			hub, pool := freshRig(t, 0)
			process := startDurableProcess(t, hub, pool, kind, freshNames)
			current := awaitDelivering(t, hub, pool, kind.logical)
			if !strings.HasPrefix(current, kind.logical+"~") {
				t.Fatalf("the record selects %q, want a fresh generation of %s", current, kind.logical)
			}
			_, js := hub.admin(t)
			info, err := js.ConsumerInfo(bus.Stream, current)
			if err != nil {
				t.Fatalf("read the current %s: %v", current, err)
			}
			if subject := process.deliverySubject(); subject != info.Config.DeliverSubject {
				t.Fatalf("the intake is not subscribed at the current's deliver subject %s", info.Config.DeliverSubject)
			}
			sent := hub.publish(t, kind, 3)
			awaitHandled(t, sent, process)
			// The check writes the current's floor every intakeHoldRenew.
			waitFor(t, 20*time.Second, func() bool {
				floor := selectionRecord(t, pool, kind.logical).LastFloor
				return floor != nil && floor.Name == current && floor.Floor == sent[len(sent)-1] && floor.StreamID != ""
			}, func() string {
				return fmt.Sprintf("the record's last floor is %+v, want %d read from %s", selectionRecord(t, pool, kind.logical).LastFloor, sent[len(sent)-1], current)
			})

			if err := js.DeleteConsumer(bus.Stream, current); err != nil {
				t.Fatalf("delete the current outside the intake: %v", err)
			}
			gone := hub.publish(t, kind, 3)
			waitFor(t, 40*time.Second, func() bool {
				next := currentName(t, pool, kind.logical)
				return next != "" && next != current
			}, func() string { return "no generation replaced the current removed outside the intake" })
			next := awaitDelivering(t, hub, pool, kind.logical)
			created, err := js.ConsumerInfo(bus.Stream, next)
			if err != nil {
				t.Fatalf("read the new current %s: %v", next, err)
			}
			if created.Config.DeliverPolicy != natsgo.DeliverByStartSequencePolicy || created.Config.OptStartSeq != sent[len(sent)-1]+1 {
				t.Errorf("the new current %s starts by %v at %d, want by start sequence at the last floor + 1, %d",
					next, created.Config.DeliverPolicy, created.Config.OptStartSeq, sent[len(sent)-1]+1)
			}
			awaitHandled(t, gone, process)
		})
	}
}

// Race regression, lost selection state: with the table, its sentinel or the durable's row
// missing, or the cutover not run or rolled back, the fresh take-over refuses before it creates
// anything, says so at ERROR naming the durable and why, and never falls back to the fixed name.
// Once the cutover's records are there, the same process delivers, with no restart. The build
// before the fresh mode creates its fixed durable whatever the table holds.
func TestForcedOrderLostSelectionStateRefusesTheTakeOver(t *testing.T) {
	onEveryHub(t, lostSelectionStateRefusesTheTakeOver)
}

func lostSelectionStateRefusesTheTakeOver(t *testing.T, topology hubTopology) {
	for _, kind := range durableKinds {
		for name, row := range map[string]struct {
			lose func(t *testing.T, pool *store.Pool)
			why  string
		}{
			"no table": {func(t *testing.T, pool *store.Pool) {
				if _, err := pool.Exec(t.Context(), `drop table intake_consumers`); err != nil {
					t.Fatalf("drop the table: %v", err)
				}
			}, "selection store is missing"},
			"no sentinel": {func(t *testing.T, pool *store.Pool) {
				if _, err := pool.Exec(t.Context(), `delete from intake_consumers where logical = $1`, dispatchSentinelKey); err != nil {
					t.Fatalf("delete the sentinel: %v", err)
				}
			}, "sentinel is missing"},
			"the cutover not run": {func(t *testing.T, pool *store.Pool) { writeCutover(t, pool, "") }, "the cutover has not run"},
			"the cutover rolled back": {func(t *testing.T, pool *store.Pool) {
				writeCutover(t, pool, consumernames.CutoverRolledBack)
			}, "the cutover has not run"},
			"no row of the durable": {func(t *testing.T, pool *store.Pool) {
				if _, err := pool.Exec(t.Context(), `delete from intake_consumers where logical = $1`, consumernames.PGKey(consumernames.DispatchFamily, kind.logical)); err != nil {
					t.Fatalf("delete the durable's row: %v", err)
				}
			}, "no selection row"},
		} {
			t.Run(kind.logical+"/"+name, func(t *testing.T) {
				records := captureLogs(t)
				hub, pool := freshRigOn(t, topology, 0)
				row.lose(t, pool)
				spy := spyConsumerCalls(t, hub)
				startDurableProcess(t, hub, pool, kind, freshNames)
				hub.publish(t, kind, 2)
				refused := func() bool {
					for _, record := range records() {
						message, _ := record["msg"].(string)
						if record["level"] == "ERROR" && record["name"] == kind.logical && strings.Contains(message, row.why) {
							return true
						}
					}
					return false
				}
				waitFor(t, 30*time.Second, refused, func() string {
					return fmt.Sprintf("no ERROR naming %s and %q", kind.logical, row.why)
				})
				// Two more attempts' worth.
				time.Sleep(2 * durableBindInterval)
				if created := spy.createdNames(); len(created) != 0 {
					t.Fatalf("the refused take-over created %v", created)
				}
				// Control: the spy sees a create, so its empty answer above is no blind spot.
				spy.requireSeesACreate(t, hub)
				if name == "no table" {
					return
				}
				readyAgain(t, pool, kind.logical)
				awaitDelivering(t, hub, pool, kind.logical)
				spy.requireEachNameCreatedOnce(t)
			})
		}
	}
}

// readyAgain writes the cutover's records again over whatever a row left of them.
func readyAgain(t *testing.T, pool *store.Pool, logical string) {
	t.Helper()
	writeCutover(t, pool, consumernames.CutoverDone)
	if _, err := dispatchRecords(t, pool).Write(t.Context(), logical, consumernames.Record{}, 0); err != nil && !errors.Is(err, consumernames.ErrConflict) {
		t.Fatalf("write %s's row: %v", logical, err)
	}
}

// Race regression, every interruption of a take-over: the holder stops, and the standby that
// takes the durable over is killed at one point of its take-over -- after it reserved the
// replace, after it bound its candidate and before the create (a crash between the subscribe and
// the create), after the create answered and before the publish (whose candidate the next holder
// adopts and binds at the candidate's own deliver subject), or after the publish and before the
// predecessor's delete. A third process takes the durable over once the killed one's hold lapses,
// in another process: every message published before and during the interruption reaches a
// process, no name is sent to a create twice, and the record ends with no operation open.
func TestForcedOrderEveryInterruptionOfAFreshTakeOverIsFinishedByTheNextHolder(t *testing.T) {
	onEveryHub(t, everyInterruptionOfAFreshTakeOverIsFinishedByTheNextHolder)
}

func everyInterruptionOfAFreshTakeOverIsFinishedByTheNextHolder(t *testing.T, topology hubTopology) {
	for _, kind := range durableKinds {
		for _, point := range []string{stepReserved, stepSubscribed, stepCreated, stepDelete} {
			t.Run(kind.logical+"/"+point, func(t *testing.T) {
				hub, pool := freshRigOn(t, topology, 0)
				spy := spyConsumerCalls(t, hub)
				holder := startDurableProcess(t, hub, pool, kind, freshNames)
				first := awaitDelivering(t, hub, pool, kind.logical)
				sent := hub.publish(t, kind, 3)
				awaitHandled(t, sent, holder)

				killed := startDurableProcess(t, hub, pool, kind, freshNames, killAt(point))
				holder.stop()
				waitFor(t, 30*time.Second, killed.leases.cut.Load, func() string {
					return "the standby's take-over never reached " + point
				})
				reserved := selectionRecord(t, pool, kind.logical)
				sent = append(sent, hub.publish(t, kind, 3)...)

				next := startDurableProcess(t, hub, pool, kind, freshNames)
				current := awaitDelivering(t, hub, pool, kind.logical)
				awaitHandled(t, sent, holder, killed, next)
				record := selectionRecord(t, pool, kind.logical)
				if record.Op != nil {
					t.Errorf("the record still holds an operation: %+v", record.Op)
				}
				if point == stepCreated {
					// The killed process created its candidate and never published it: the next
					// holder adopted it, bound at the candidate's own deliver subject.
					if reserved.Op == nil || current != reserved.Op.Candidate {
						t.Errorf("the current is %s, want the killed process's candidate adopted (%+v)", current, reserved.Op)
					}
					_, js := hub.admin(t)
					info, err := js.ConsumerInfo(bus.Stream, current)
					if err != nil {
						t.Fatalf("read the adopted candidate: %v", err)
					}
					if subject := next.deliverySubject(); subject != info.Config.DeliverSubject {
						t.Errorf("the next holder is not bound at the adopted candidate's own deliver subject %s", info.Config.DeliverSubject)
					}
				}
				spy.requireEachNameCreatedOnce(t)
				// The first generation is retired and, once the next holder's check has run,
				// deleted.
				_, js := hub.admin(t)
				waitFor(t, 30*time.Second, func() bool {
					_, err := js.ConsumerInfo(bus.Stream, first)
					return errors.Is(err, natsgo.ErrConsumerNotFound)
				}, func() string { return "the first generation " + first + " was never deleted" })
			})
		}
	}
}

// An adopted candidate is held to its durable's own create, by the listener's check
// (bus.Client.ConsumerAdoptable). A take-over killed after its create answered leaves its candidate
// for the next holder's Step 0 to adopt; here that candidate is made again under its name, at its
// start, with one setting changed, before the next holder starts. One with another ack policy,
// which no update changes, is refused: the take-over says why at ERROR, nothing is published, and
// the record keeps the operation open on that candidate. One with the other durable's filter, which
// an update changes, is adopted and brought back to the durable's own filter before the next
// holder binds it, which then delivers the messages published since. Control: at the build before
// this check, the first is adopted and the second delivers on the other filter.
func TestAnAdoptedCandidateIsHeldToItsDurablesCreate(t *testing.T) {
	for i, kind := range durableKinds {
		other := durableKinds[len(durableKinds)-1-i].config().FilterSubject
		for name, row := range map[string]struct {
			change  func(*natsgo.ConsumerConfig)
			adopted bool
		}{
			"another ack policy": {func(c *natsgo.ConsumerConfig) { c.AckPolicy = natsgo.AckAllPolicy }, false},
			"another filter":     {func(c *natsgo.ConsumerConfig) { c.FilterSubject = other }, true},
		} {
			t.Run(kind.logical+"/"+name, func(t *testing.T) {
				records := captureLogs(t)
				hub, pool := freshRig(t, 0)
				holder := startDurableProcess(t, hub, pool, kind, freshNames)
				awaitDelivering(t, hub, pool, kind.logical)
				killed := startDurableProcess(t, hub, pool, kind, freshNames, killAt(stepCreated))
				holder.stop()
				waitFor(t, 30*time.Second, killed.leases.cut.Load, func() string {
					return "the standby's take-over never reached " + stepCreated
				})
				reserved := selectionRecord(t, pool, kind.logical)
				if reserved.Op == nil || reserved.Op.Candidate == "" {
					t.Fatalf("the killed take-over left no candidate: %+v", reserved)
				}
				candidate := reserved.Op.Candidate
				_, js := hub.admin(t)
				made, err := js.ConsumerInfo(bus.Stream, candidate)
				if err != nil {
					t.Fatalf("read the killed process's candidate: %v", err)
				}
				config := made.Config
				config.DeliverSubject = natsgo.NewInbox()
				config.Metadata = bus.OwnMetadata(config.Metadata, 0)
				row.change(&config)
				if err := js.DeleteConsumer(bus.Stream, candidate); err != nil {
					t.Fatalf("delete the candidate to make it again: %v", err)
				}
				if _, err := js.AddConsumer(bus.Stream, &config); err != nil {
					t.Fatalf("make the candidate again with %s: %v", name, err)
				}
				sent := hub.publish(t, kind, 2)
				next := startDurableProcess(t, hub, pool, kind, freshNames)

				if !row.adopted {
					refused := func() bool {
						for _, record := range records() {
							message, _ := record["msg"].(string)
							if record["level"] == "ERROR" && record["name"] == kind.logical &&
								strings.Contains(message, candidate) && strings.Contains(message, "never gives") {
								return true
							}
						}
						return false
					}
					waitFor(t, 60*time.Second, refused, func() string {
						return fmt.Sprintf("no ERROR naming %s's refused candidate %s; record %+v", kind.logical, candidate, selectionRecord(t, pool, kind.logical))
					})
					// Two more attempts' worth.
					time.Sleep(2 * durableBindInterval)
					if record := selectionRecord(t, pool, kind.logical); record.Current == candidate || record.Op == nil || record.Op.Candidate != candidate {
						t.Fatalf("the refused candidate %s was published, or its operation closed: %+v", candidate, record)
					}
					if subject := next.deliverySubject(); subject != "" {
						t.Fatalf("the next holder delivers at %s from a refused candidate", subject)
					}
					return
				}
				if current := awaitDelivering(t, hub, pool, kind.logical); current != candidate {
					t.Fatalf("the current is %s, want the adopted candidate %s", current, candidate)
				}
				info, err := js.ConsumerInfo(bus.Stream, candidate)
				if err != nil {
					t.Fatalf("read the adopted candidate: %v", err)
				}
				if want := kind.config().FilterSubject; info.Config.FilterSubject != want {
					t.Fatalf("the adopted candidate filters %s, want its durable's own %s", info.Config.FilterSubject, want)
				}
				awaitHandled(t, sent, next)
			})
		}
	}
}

// A fresh process whose first take-over fails after its create stops cleanly. The take-over
// subscribes its candidate, creates it, and is handed every message while its handler runs the
// first; then its selection store stops answering, as in a database incident, so the publish of
// the candidate fails, with the hold kept, and the take-over drops the candidate, which hands back
// the messages behind the handler. Its intake then delivers from no generation, so it holds no
// client of its own (heldDurable.nats is nil until a take-over succeeds), and every later attempt
// fails at its first read of the store. The stop that follows confirms the hand-backs on the
// connection they were published on and releases the hold, and the next holder is delivered every
// message. Before the fix the stop flushed the intake's own client, nil here, and the process
// panicked.
func TestAStopAfterAFirstTakeOverFailedPastItsCreateCompletes(t *testing.T) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			hub, pool := freshRig(t, 0)
			created, queued := make(chan string, 1), make(chan struct{})
			var once sync.Once
			process := startDurableProcess(t, hub, pool, kind, freshNames, blockingHandlers(), atStep(func(p *durableProcess, point, name string) error {
				if point != stepCreated {
					return nil
				}
				once.Do(func() {
					created <- name
					select {
					case <-queued:
					case <-time.After(30 * time.Second):
					}
					p.store.cut.Store(true)
				})
				return nil
			}))
			var candidate string
			select {
			case candidate = <-created:
			case <-time.After(30 * time.Second):
				t.Fatal("the first take-over never created its candidate")
			}
			sent := hub.publish(t, kind, 3)
			_, js := hub.admin(t)
			waitFor(t, 20*time.Second, func() bool {
				info, err := js.ConsumerInfo(bus.Stream, candidate)
				return err == nil && process.times(sent[0]) > 0 && info.NumAckPending == len(sent)
			}, func() string { return "the candidate never handed the process every message" })
			close(queued)
			waitFor(t, 20*time.Second, func() bool {
				process.held.mu.Lock()
				defer process.held.mu.Unlock()
				return process.held.delivering != nil && process.held.delivering.handedBack()
			}, func() string { return "the failed take-over never handed its queued messages back" })

			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				process.stop()
			}()
			select {
			case <-stopped:
			case <-time.After(30 * time.Second):
				t.Fatal("the stop did not complete")
			}
			next := startDurableProcess(t, hub, pool, kind, freshNames)
			awaitDelivering(t, hub, pool, kind.logical)
			awaitHandled(t, sent, next)
		})
	}
}

// Race regression, an auth service outage: the auth service stops after the standby reserved its
// replace, so the connection its generations are granted on is never admitted. The take-over
// stops there with nothing deleted: the current stays, and once the service is back the next
// attempt settles the reserved replace and delivers every message published meanwhile. While the
// service is down, no later attempt reserves another candidate: each would be retired unborn, and
// the record's retired list, with a cleanup connection for each of its names, would grow every
// attempt for as long as the outage lasts. On a cluster the outage is every member's: each sends
// its clients to the one service.
func TestForcedOrderACalloutOutageStopsTheTakeOverBeforeAnythingIsDeleted(t *testing.T) {
	onEveryHub(t, aCalloutOutageStopsTheTakeOverBeforeAnythingIsDeleted)
}

func aCalloutOutageStopsTheTakeOverBeforeAnythingIsDeleted(t *testing.T, topology hubTopology) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			hub, pool := freshRigOn(t, topology, 0)
			spy := spyConsumerCalls(t, hub)
			holder := startDurableProcess(t, hub, pool, kind, freshNames)
			first := awaitDelivering(t, hub, pool, kind.logical)
			sent := hub.publish(t, kind, 2)
			awaitHandled(t, sent, holder)

			outage := make(chan struct{})
			var reserved atomic.Int32
			standby := startDurableProcess(t, hub, pool, kind, freshNames, atStep(func(_ *durableProcess, point, _ string) error {
				if point == stepReserved {
					reserved.Add(1)
					select {
					case <-outage:
					default:
						hub.stopService()
						close(outage)
					}
				}
				return nil
			}))
			holder.stop()
			select {
			case <-outage:
			case <-time.After(30 * time.Second):
				t.Fatal("the standby's take-over never reserved its replace")
			}
			sent = append(sent, hub.publish(t, kind, 2)...)
			// Attempts during the outage, four at least: none may delete the current, and none may
			// reserve another candidate.
			time.Sleep(4 * (generationConnectWithin + durableBindInterval))
			if n := reserved.Load(); n != 1 {
				t.Errorf("the standby reserved %d candidates during the outage, want 1", n)
			}
			_, js := hub.admin(t)
			if _, err := js.ConsumerInfo(bus.Stream, first); err != nil {
				t.Fatalf("the current %s is gone during the outage: %v", first, err)
			}
			if record := selectionRecord(t, pool, kind.logical); record.Current != first {
				t.Fatalf("the record selects %s during the outage, want %s still", record.Current, first)
			}

			hub.startService(t)
			current := awaitDelivering(t, hub, pool, kind.logical)
			if current == first {
				t.Fatalf("the standby never took the durable over after the outage")
			}
			awaitHandled(t, sent, holder, standby)
			spy.requireEachNameCreatedOnce(t)
		})
	}
}

// Race regression, two processes given one generation: a standby reserves its replace and binds
// its candidate, then pauses before the create while its hold lapses and its database session
// ends, releasing the consumer lease as a session a network cut off is in the end; the next holder
// settles the replace in another process, and the paused one, released, sends no create of a
// candidate it lost the hold over. Each generation is created at most once, and one current
// delivers.
func TestForcedOrderTwoProcessesGivenOneGenerationCreateItOnce(t *testing.T) {
	onEveryHub(t, twoProcessesGivenOneGenerationCreateItOnce)
}

func twoProcessesGivenOneGenerationCreateItOnce(t *testing.T, topology hubTopology) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			hub, pool := freshRigOn(t, topology, 0)
			spy := spyConsumerCalls(t, hub)
			holder := startDurableProcess(t, hub, pool, kind, freshNames)
			awaitDelivering(t, hub, pool, kind.logical)

			paused, resume := make(chan string, 1), make(chan struct{})
			slow := startDurableProcess(t, hub, pool, kind, freshNames, atStep(func(p *durableProcess, point, name string) error {
				if point != stepSubscribed {
					return nil
				}
				select {
				case paused <- name:
					p.leases.cut.Store(true)
					endLeaseSession(t, pool, consumerLease(kind.logical))
					<-resume
				default:
				}
				return nil
			}))
			// Registered after the process, so it runs first: a row that fails while the process is
			// paused still lets its stop return.
			release := sync.OnceFunc(func() { close(resume) })
			t.Cleanup(release)
			holder.stop()
			var candidate string
			select {
			case candidate = <-paused:
			case <-time.After(30 * time.Second):
				t.Fatal("the standby never bound its candidate")
			}
			sent := hub.publish(t, kind, 3)
			next := startDurableProcess(t, hub, pool, kind, freshNames)
			current := awaitDelivering(t, hub, pool, kind.logical)
			if current == candidate {
				t.Fatalf("the next holder selected the paused process's candidate %s, which was never created", candidate)
			}
			release()
			awaitHandled(t, sent, holder, slow, next)
			time.Sleep(2 * durableBindInterval)
			spy.requireEachNameCreatedOnce(t)
			if slices.Contains(spy.createdNames(), candidate) {
				t.Errorf("the paused process created %s after it lost its hold", candidate)
			}
		})
	}
}

// Compatibility control, the consumer cap: at a full consumer cap a take-over of each durable answers as
// the build before the fresh mode does -- the durable delivers again and its pending messages
// arrive -- and nothing updates the stream's max_consumers.
func TestForcedOrderATakeOverAtAFullCapDeliversWithNoCapUpdate(t *testing.T) {
	onEveryHub(t, aTakeOverAtAFullCapDeliversWithNoCapUpdate)
}

func aTakeOverAtAFullCapDeliversWithNoCapUpdate(t *testing.T, topology hubTopology) {
	for _, kind := range durableKinds {
		t.Run(kind.logical, func(t *testing.T) {
			const capacity = 3
			hub, pool := freshRigOn(t, topology, capacity)
			spy := spyConsumerCalls(t, hub)
			holder := startDurableProcess(t, hub, pool, kind, freshNames, blockingHandlers())
			awaitDelivering(t, hub, pool, kind.logical)
			_, js := hub.admin(t)
			for i := range capacity - 1 {
				if _, err := js.AddConsumer(bus.Stream, &natsgo.ConsumerConfig{Durable: fmt.Sprintf("filler-%d", i), AckPolicy: natsgo.AckExplicitPolicy}); err != nil {
					t.Fatalf("fill the cap: %v", err)
				}
			}
			pending := hub.publish(t, kind, 3)
			waitFor(t, 20*time.Second, func() bool { return holder.times(pending[0]) > 0 }, func() string {
				return "the holder was never handed the first pending message"
			})
			standby := startDurableProcess(t, hub, pool, kind, freshNames)
			holder.stop()
			awaitHandled(t, pending, standby)
			spy.mu.Lock()
			updates := slices.Clone(spy.updates)
			spy.mu.Unlock()
			for _, update := range updates {
				if strings.Contains(update, "max_consumers") {
					t.Errorf("a stream update changed max_consumers: %s", update)
				}
			}
			info, err := js.StreamInfo(bus.Stream)
			if err != nil {
				t.Fatalf("read the stream: %v", err)
			}
			if info.Config.MaxConsumers != capacity {
				t.Errorf("the stream's max_consumers is %d, want %d unchanged", info.Config.MaxConsumers, capacity)
			}
		})
	}
}
