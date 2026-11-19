package store

import (
	"context"
	"errors"
	"os"
	"slices"
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
// (registrationLockTimeout), spelled again here for the span the publish is given below and for
// the timing lane's reading of the refusal.
const registrationGateBound = 5 * time.Second

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
// lane's (TestAnEarlierBuildsRegistrationIsRefusedWithinItsBoundOnTheClock).
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
	published := make(chan error, 1)
	go func() { published <- arriveOn(ctx, publishConn, publish, uuid.NewString()) }()
	awaitGateWait(t, s, publishConn.Conn().PgConn().PID(), published, publish.name)
	testwait.Quiet(t, publish.name+" ending while the transaction it waits for is under way", published, registrationGateBound+time.Second,
		"the publish's verdict is that no bound of its own ends it at the registration's lock_timeout, and Postgres shows no other "+
			"session's lock_timeout: no state, no synctest bubble (real Postgres) and no later action tells a wait with no bound "+
			"from one whose bound has not come yet, so the registration's bound and a second more pass first")
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
