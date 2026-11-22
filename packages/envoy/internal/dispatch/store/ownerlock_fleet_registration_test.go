package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/benchtime"
	"github.com/sjawhar/envoy/internal/testwait"
)

// The owner lock beside an earlier build (ownerlock_fleet_test.go): the bound on an earlier
// build's registration's wait for the owner lock gate.

// ───── an earlier build's registration and its bound on the gate ───────────

// registrationGateBound bounds an earlier build's registration's wait for the owner lock gate:
// owner_lock_legacy_registration's lock_timeout, which every run reads off the function itself
// (registrationLockTimeout), spelled again here for the timing lane's reading of the refusal.
const registrationGateBound = 5 * time.Second

// pastRegistrationBound is how far past registrationGateBound the row requires a dashboard
// publish to be still waiting for the gate, on the database's clock: room for the statement's
// start before its trigger asks for the gate, and for a bound of the publish's own at the
// registration's 5 seconds to have ended its wait by then.
const pastRegistrationBound = time.Second

// registrationLockTimeout is the setting owner_lock_legacy_registration runs under (pg_proc's
// proconfig), as Postgres holds it: what bounds the registration's wait, and the one place the
// migration (20261115200014_owner_lock_legacy_gate) sets it.
func registrationLockTimeout(t *testing.T, s *Store) []string {
	t.Helper()
	var config []string
	if err := s.Pool.QueryRow(context.Background(), `
		select coalesce(proconfig, '{}') from pg_proc where proname = 'owner_lock_legacy_registration'
	`).Scan(&config); err != nil {
		t.Fatalf("read owner_lock_legacy_registration's settings: %v", err)
	}
	return config
}

// An earlier build's registration waits for the owner lock gate no longer than its lock_timeout. It
// holds the registry's lock while it waits, and every process's heartbeat takes that lock too, so
// behind a transaction of this build that holds the gate shared for longer, the registration
// fails with 55P03 lock_not_available, Postgres's answer at that bound, stamps no mark, and lets
// go of the registry's lock: a heartbeat of this build queued on it goes through. A dashboard
// publish keeps waiting past the bound, since the earlier build bounds its publish itself, and
// goes through once that transaction commits. Without the bound the registration waits for that
// transaction as long as the publish does, and the heartbeat with it, which the shared wait's end
// fails by name. The bound itself is the function's lock_timeout of 5 seconds, read off pg_proc,
// so a migration that raised it fails here; how long the refusal takes on the clock is the timing
// lane's (TestAnEarlierBuildsRegistrationIsRefusedWithinItsBoundOnTheClock). That the publish
// waits past the bound is read on the database's clock, where the product's bounds run: its
// backend still waits for the gate once its insert has run registrationGateBound and
// pastRegistrationBound, so a bound set anywhere on its path (its session, the gate's trigger
// function, or a function that one calls) ends the publish first and fails the row by name.
func TestAnEarlierBuildsRegistrationWaitsForTheGateNoLongerThanItsBound(t *testing.T) {
	s, owners := openFleetStore(t)
	ctx := context.Background()
	if config := registrationLockTimeout(t, s); !slices.Contains(config, "lock_timeout=5s") {
		t.Fatalf("owner_lock_legacy_registration runs under %q, want lock_timeout=5s, the bound its registration waits for the gate", config)
	}
	mine := registerFleetProcess(t, s, OwnerLockProtocol, 0)
	holder, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if err := LockOwner(ctx, holder, owners.issueA); err != nil {
		t.Fatal(err)
	}
	if gate, granted := gateLock(t, s, holder.Conn().PgConn().PID()); gate != "ShareLock" || !granted {
		t.Fatalf("the transaction in flight holds the gate as %q (granted %t), want it held shared", gate, granted)
	}

	registration, publish := earlierBuildArrivals[1], earlierBuildArrivals[0]
	registrationConn, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer registrationConn.Release()
	registered := make(chan error, 1)
	go func() { registered <- arriveOn(ctx, registrationConn, registration, uuid.NewString()) }()
	awaitGateWait(t, s, registrationConn.Conn().PgConn().PID(), registered, registration.name)
	beat := make(chan error, 1)
	go func() { beat <- heartbeatAsThisBuild(ctx, s, mine) }()
	err = testwait.Receive(t, registration.name+" behind a transaction holding the gate to be refused at its lock_timeout", registered)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("%s behind a transaction holding the gate past the bound ended with %v, want 55P03 lock_not_available", registration.name, err)
	}
	t.Logf("%s was refused: %s", registration.name, pgErr.Message)
	if err := testwait.Receive(t, "this build's heartbeat to go through once "+registration.name+" was refused", beat); err != nil {
		t.Fatalf("this build's heartbeat after %s was refused: %v", registration.name, err)
	}
	if hasLegacyMark(t, s) {
		t.Errorf("%s was refused and the earlier build's mark is stamped, want none", registration.name)
	}

	publishConn, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer publishConn.Release()
	// No bound of the publish's own ends its wait at the registration's lock_timeout, read as
	// state where such a bound would stand: its session runs under no lock_timeout or
	// statement_timeout, and the gate's trigger function it waits in (owner_lock_legacy_arrival)
	// sets neither, in its settings or in its body, as owner_lock_legacy_registration's settings
	// set its own (registrationLockTimeout).
	var sessionLockTimeout, sessionStatementTimeout string
	if err := publishConn.QueryRow(ctx, `select current_setting('lock_timeout'), current_setting('statement_timeout')`).Scan(&sessionLockTimeout, &sessionStatementTimeout); err != nil {
		t.Fatalf("read %s's session timeouts: %v", publish.name, err)
	}
	if sessionLockTimeout != "0" || sessionStatementTimeout != "0" {
		t.Fatalf("%s's session runs under lock_timeout %s and statement_timeout %s, want neither: either ends its wait for the gate", publish.name, sessionLockTimeout, sessionStatementTimeout)
	}
	var arrivalConfig []string
	var arrivalBody string
	if err := s.Pool.QueryRow(ctx, `
		select coalesce(proconfig, '{}'), prosrc from pg_proc where proname = 'owner_lock_legacy_arrival'
	`).Scan(&arrivalConfig, &arrivalBody); err != nil {
		t.Fatalf("read owner_lock_legacy_arrival's settings: %v", err)
	}
	if len(arrivalConfig) != 0 || strings.Contains(arrivalBody, "lock_timeout") || strings.Contains(arrivalBody, "statement_timeout") {
		t.Fatalf("owner_lock_legacy_arrival, the gate %s waits in, runs under settings %v and a body that sets a timeout (%t), want no bound of its own",
			publish.name, arrivalConfig, strings.Contains(arrivalBody, "_timeout"))
	}
	published := make(chan error, 1)
	go func() { published <- arriveOn(ctx, publishConn, publish, uuid.NewString()) }()
	publishPID := publishConn.Conn().PgConn().PID()
	awaitGateWait(t, s, publishPID, published, publish.name)
	// The publish still waits for the gate past the registration's bound, read on the database's
	// clock in one statement: its backend waits on a Lock, its gate request is not granted, and its
	// insert, the statement whose trigger waits, started registrationGateBound and
	// pastRegistrationBound before the database's now(). A publish that ends first, as one a bound
	// on its path cuts off ends, fails the row naming its error. No clock of the test process is read.
	want := registrationGateBound + pastRegistrationBound
	testwait.Until(t, publish.name+" to still wait for the gate "+want.String()+" into its insert, on the database's clock", func() (string, bool) {
		select {
		case err := <-published:
			t.Fatalf("%s ended (%v) while a transaction held the gate shared, before it had waited %s on the database's clock; want it to keep waiting past the registration's bound",
				publish.name, err, want)
		default:
		}
		var waitEventType string
		var waitedMicros int64
		var gateWaiting bool
		if err := s.Pool.QueryRow(ctx, `
			select coalesce(a.wait_event_type, ''), (extract(epoch from now() - a.query_start) * 1000000)::bigint,
			  exists (select 1 from pg_locks l
			    where l.pid = a.pid and l.locktype = 'advisory' and not l.granted and l.mode = 'ExclusiveLock'
			      and l.database = (select oid from pg_database where datname = current_database())
			      and l.classid::bigint = $2 and l.objid::bigint = $3 and l.objsubid = 2)
			from pg_stat_activity a where a.pid = $1`,
			publishPID, OwnerLockGateNamespace, OwnerLockGateKey).Scan(&waitEventType, &waitedMicros, &gateWaiting); err != nil {
			t.Fatalf("read %s's wait on the database: %v", publish.name, err)
		}
		waited := time.Duration(waitedMicros) * time.Microsecond
		return fmt.Sprintf("wait event type %q, waiting for the gate %t, %s into its statement", waitEventType, gateWaiting, waited),
			waitEventType == "Lock" && gateWaiting && waited >= want
	})
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := testwait.Receive(t, publish.name+" to end once the transaction it waited for committed", published); err != nil {
		t.Fatalf("%s after the transaction it waited for committed: %v", publish.name, err)
	}
	if !hasLegacyMark(t, s) {
		t.Errorf("%s went through and the earlier build's mark is not stamped", publish.name)
	}
}

// The registration's refusal on the clock, main's reading of the bound: behind a transaction that
// holds the gate, the refusal comes within registrationGateBound and three seconds more of the
// registration's arrival. Its time is the machine's as much as the product's, so it is the timing
// lane's (benchtime.TimingEnv), which CI never runs; every run reads the bound off the function
// itself (TestAnEarlierBuildsRegistrationWaitsForTheGateNoLongerThanItsBound).
func TestAnEarlierBuildsRegistrationIsRefusedWithinItsBoundOnTheClock(t *testing.T) {
	benchtime.Timing(t, os.Getenv(benchtime.TimingEnv))
	s, owners := openFleetStore(t)
	ctx := context.Background()
	holder, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if err := LockOwner(ctx, holder, owners.issueA); err != nil {
		t.Fatal(err)
	}
	registration := earlierBuildArrivals[1]
	registrationConn, err := s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer registrationConn.Release()
	registered := make(chan error, 1)
	began := time.Now()
	go func() { registered <- arriveOn(ctx, registrationConn, registration, uuid.NewString()) }()
	err = testwait.Receive(t, registration.name+" behind a transaction holding the gate to be refused at its lock_timeout", registered)
	took := time.Since(began)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("%s behind a transaction holding the gate ended with %v after %s, want 55P03 lock_not_available", registration.name, err, took)
	}
	if took > registrationGateBound+3*time.Second {
		t.Errorf("%s was refused %s after it started, past its bound of %s and three seconds more", registration.name, took.Round(time.Millisecond), registrationGateBound)
	}
	t.Logf("%s was refused after %s: %s", registration.name, took.Round(time.Millisecond), pgErr.Message)
}
