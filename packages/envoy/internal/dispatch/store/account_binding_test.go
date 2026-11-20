package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The account binding's fleet (AccountBindingHolds, migration 20261120222130_account_binding_fleet):
// a build without the binding is seen as the owner lock sees a build before it, from its dashboard
// publish, its registration and the mark either stamps, but by account_binding rather than
// owner_lock_protocol, so a build at owner lock protocol 1 that lacks the binding counts too. These
// tests write such a build's rows as it writes them: its columns named, account_binding left out.

// accountBindingFleetVersion is migration 20261120222130_account_binding_fleet's version.
const accountBindingFleetVersion = 20261120222130

// registerUnboundProcess writes a server_processes row as a process of a build without the
// binding leaves it, at owner lock protocol ownerLock (0 leaves that column out too, as a build
// before the owner lock does), with a heartbeat age old, so its insert stamps the binding's mark
// (account_binding_legacy_registration).
func registerUnboundProcess(t *testing.T, s *Store, ownerLock int, age time.Duration) {
	t.Helper()
	var err error
	if ownerLock == 0 {
		_, err = s.Pool.Exec(context.Background(), `
			insert into server_processes (instance, signing_keys, heartbeat_at)
			values (gen_random_uuid(), '{fingerprint}', now() - make_interval(secs => $1))`, age.Seconds())
	} else {
		_, err = s.Pool.Exec(context.Background(), `
			insert into server_processes (instance, signing_keys, heartbeat_at, owner_lock_protocol)
			values (gen_random_uuid(), '{fingerprint}', now() - make_interval(secs => $1), $2)`, age.Seconds(), ownerLock)
	}
	if err != nil {
		t.Fatalf("register a process without the binding at owner lock protocol %d: %v", ownerLock, err)
	}
}

// registerBoundProcess writes a server_processes row as a process of this build leaves it.
func registerBoundProcess(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), `
		insert into server_processes (instance, signing_keys, owner_lock_protocol, account_binding)
		values (gen_random_uuid(), '{fingerprint}', $1, $2)`, OwnerLockProtocol, AccountBindingProtocol); err != nil {
		t.Fatalf("register a process of this build: %v", err)
	}
}

// publishUnbound writes a dashboard_builds row as the boot of a build at owner lock protocol 1
// without the binding publishes it (legion-envoy-v32.20.2's dashboard.Publish), so its insert
// stamps the binding's mark (account_binding_legacy_arrival) and not the owner lock's.
func publishUnbound(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), `
		insert into dashboard_builds (build, entry, commit, owner_lock_protocol) values ($1, '', 'unbound0build', 1)
		on conflict (build) do update set last_started_at = now()`, fmt.Sprintf("unbound-%d", time.Now().UnixNano())); err != nil {
		t.Fatalf("publish as a build without the binding: %v", err)
	}
}

// ageBindingMark moves the binding's mark age into the past.
func ageBindingMark(t *testing.T, s *Store, age time.Duration) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(),
		`update account_binding_legacy_mark set seen_at = now() - make_interval(secs => $1)`, age.Seconds()); err != nil {
		t.Fatalf("age the binding's mark: %v", err)
	}
}

// hasBindingMark reports whether account_binding_legacy_mark holds its row.
func hasBindingMark(t *testing.T, s *Store) bool {
	t.Helper()
	var marked bool
	if err := s.Pool.QueryRow(context.Background(), `select exists (select from account_binding_legacy_mark)`).Scan(&marked); err != nil {
		t.Fatalf("read the binding's mark: %v", err)
	}
	return marked
}

// bindingHolds reads AccountBindingHolds in a transaction of its own, and the owner lock's own
// fleet read beside it (public.owner_lock_fleet), which a control compares.
func bindingHolds(t *testing.T, s *Store) (holds, ownerLockLegacy bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if holds, err = AccountBindingHolds(ctx, tx); err != nil {
		t.Fatal(err)
	}
	legacy, mark := AccountBindingWindows()
	if err := tx.QueryRow(ctx, `select public.owner_lock_fleet($1, $2)`, legacy, mark).Scan(&ownerLockLegacy); err != nil {
		t.Fatal(err)
	}
	return holds, ownerLockLegacy
}

// The binding holds while no build without it is in the fleet: none registered, or only this
// build's processes, a process without it silent past the legacy window, or its mark past the
// mark's window. It does not hold while such a process heartbeated within the legacy window,
// whatever its owner lock protocol, or while its mark is younger than the mark's window, a
// dashboard publish's included: the window from that publish to the registration, in which a boot
// writes before it registers, is what server_processes alone misses.
//
// Control, in the test: for a build at owner lock protocol 1 without the binding, the owner lock's
// own fleet read answers that no earlier build is in the fleet, so a gate on that fence alone
// would start the binding beside it.
func TestTheAccountBindingHoldsOnlyWhileNoBuildWithoutItIsInTheFleet(t *testing.T) {
	s := migratedEmptyStore(t)
	for _, state := range []struct {
		name           string
		seed           func(t *testing.T)
		holds          bool
		ownerLockBlind bool
	}{
		{name: "no process registered", seed: func(*testing.T) {}, holds: true},
		{name: "a live process of this build", seed: func(t *testing.T) { registerBoundProcess(t, s) }, holds: true},
		{name: "a live process at owner lock protocol 1 without the binding", ownerLockBlind: true,
			seed: func(t *testing.T) { registerUnboundProcess(t, s, 1, 0) }},
		{name: "a live process before the owner lock", seed: func(t *testing.T) { registerUnboundProcess(t, s, 0, 0) }},
		{name: "a process without the binding whose heartbeat stopped within the legacy window", ownerLockBlind: true, seed: func(t *testing.T) {
			registerUnboundProcess(t, s, 1, registryLiveWindow+time.Minute)
			ageBindingMark(t, s, OwnerLockMarkWindow+time.Minute)
		}},
		{name: "a process without the binding whose heartbeat stopped before the legacy window", holds: true, seed: func(t *testing.T) {
			registerUnboundProcess(t, s, 1, OwnerLockLegacyWindow+time.Minute)
			ageBindingMark(t, s, OwnerLockMarkWindow+time.Minute)
		}},
		{name: "a dashboard publish without the binding just now, before its registration", ownerLockBlind: true,
			seed: func(t *testing.T) { publishUnbound(t, s) }},
		{name: "a dashboard publish without the binding older than the legacy window, within the mark's", ownerLockBlind: true, seed: func(t *testing.T) {
			publishUnbound(t, s)
			ageBindingMark(t, s, OwnerLockLegacyWindow+time.Minute)
		}},
		{name: "a dashboard publish without the binding before the mark's window", holds: true, seed: func(t *testing.T) {
			publishUnbound(t, s)
			ageBindingMark(t, s, OwnerLockMarkWindow+time.Minute)
		}},
	} {
		t.Run(state.name, func(t *testing.T) {
			t.Cleanup(func() {
				_, _ = s.Pool.Exec(context.Background(), `delete from server_processes; delete from dashboard_builds;
					delete from account_binding_legacy_mark; delete from owner_lock_legacy_mark`)
			})
			state.seed(t)
			holds, ownerLockLegacy := bindingHolds(t, s)
			if holds != state.holds {
				t.Errorf("AccountBindingHolds = %t, want %t", holds, state.holds)
			}
			if state.ownerLockBlind && ownerLockLegacy {
				t.Errorf("control: the owner lock's fleet read finds an earlier build here; want it blind to a build at owner lock protocol 1, the case only the binding's fence sees")
			}
		})
	}
}

// The migration stamps the binding's mark at the newest dashboard publish already in
// dashboard_builds when that is within the mark's window, since that publish came before the
// trigger, and every row there reads account_binding 0 then; a publish older than the window, and
// none at all, which is every test database, leave no mark.
func TestTheMigrationMarksABuildWithoutTheBindingThatPublishedBeforeIt(t *testing.T) {
	for _, c := range []struct {
		name    string
		publish bool
		age     time.Duration
		marked  bool
	}{
		{name: "a publish just before the migration", publish: true, marked: true},
		{name: "a publish inside the mark's window", publish: true, age: OwnerLockMarkWindow - time.Minute, marked: true},
		{name: "a publish before the mark's window", publish: true, age: OwnerLockMarkWindow + time.Minute},
		{name: "no publish before the migration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s := openEmptyTestStore(t)
			if err := s.migrate(ctx, migrationsThrough(t, accountBindingFleetVersion-1)); err != nil {
				t.Fatalf("migrate through %d: %v", accountBindingFleetVersion-1, err)
			}
			if c.publish {
				if _, err := s.Pool.Exec(ctx, `
					insert into dashboard_builds (build, entry, commit, owner_lock_protocol, last_started_at)
					values ('unbound', '/assets/index-unbound.js', 'unbound0build', 1, now() - make_interval(secs => $1))`, c.age.Seconds()); err != nil {
					t.Fatalf("publish as a build without the binding: %v", err)
				}
			}
			if err := s.migrate(ctx, migrationFiles); err != nil {
				t.Fatalf("migrate from %d: %v", accountBindingFleetVersion, err)
			}
			if marked := hasBindingMark(t, s); marked != c.marked {
				t.Errorf("after the migration the binding's mark is stamped: %t, want %t", marked, c.marked)
			}
			if holds, _ := bindingHolds(t, s); holds == c.marked {
				t.Errorf("after the migration AccountBindingHolds = %t, want %t", holds, !c.marked)
			}
		})
	}
}

// bindingGateWaiter reports whether backend pid waits for the binding's gate exclusively, as an
// arrival of a build without the binding takes it.
func bindingGateWaiter(t *testing.T, s *Store, pid uint32) bool {
	t.Helper()
	var waiting bool
	if err := s.Pool.QueryRow(context.Background(), `
		select exists (select from pg_locks
			where locktype = 'advisory' and database = (select oid from pg_database where datname = current_database())
			  and classid::bigint = $1 and objid::bigint = $2 and objsubid = 2 and pid = $3
			  and mode = 'ExclusiveLock' and not granted)`,
		OwnerLockGateNamespace, AccountBindingGateKey, pid).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	return waiting
}

// bindingGateHeld reports whether backend pid holds the binding's gate, in any mode.
func bindingGateHeld(t *testing.T, s *Store, pid uint32) bool {
	t.Helper()
	var held bool
	if err := s.Pool.QueryRow(context.Background(), `
		select exists (select from pg_locks
			where locktype = 'advisory' and database = (select oid from pg_database where datname = current_database())
			  and classid::bigint = $1 and objid::bigint = $2 and objsubid = 2 and pid = $3 and granted)`,
		OwnerLockGateNamespace, AccountBindingGateKey, pid).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}

// sendUnboundArrival runs insert, an arrival of a build without the binding, on conn, a
// connection of its own, and delivers its error once it ends.
func sendUnboundArrival(conn *pgx.Conn, insert string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Exec(context.Background(), insert)
		done <- err
	}()
	return done
}

// A transaction that found the binding holding keeps its gate shared to its end, so a build
// without the binding that arrives meanwhile waits for it: its dashboard publish's insert queues
// on the gate, uncommitted, and stamps no mark until that transaction ends; a transaction that
// asks while the arrival waits finds the binding not holding, since its try of the gate fails
// behind the queued arrival. Once the first transaction commits, the arrival commits and its mark
// keeps the binding off. A registration's wait is bounded at 2 seconds, then refused 55P03. A
// transaction that finds a build without the binding in the fleet holds no gate at all.
func TestAnArrivalWithoutTheBindingWaitsForATransactionThatFoundItHolding(t *testing.T) {
	s := migratedEmptyStore(t)
	ctx := context.Background()
	holder, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if holds, err := AccountBindingHolds(ctx, holder); err != nil || !holds {
		t.Fatalf("AccountBindingHolds on an empty fleet = %t, %v; want true", holds, err)
	}
	if !bindingGateHeld(t, s, holder.Conn().PgConn().PID()) {
		t.Fatalf("a transaction that found the binding holding holds no binding gate; want it held shared to its end")
	}

	conn, err := pgx.ConnectConfig(ctx, s.Pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	published := sendUnboundArrival(conn, `insert into dashboard_builds (build, entry, commit, owner_lock_protocol) values ('unbound', '', 'unbound0build', 1)`)
	for deadline := time.Now().Add(10 * time.Second); !bindingGateWaiter(t, s, conn.PgConn().PID()); time.Sleep(5 * time.Millisecond) {
		select {
		case err := <-published:
			t.Fatalf("the publish without the binding ended (%v) while a transaction that found the binding holding was open; want it to wait for the gate", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the publish without the binding did not wait for the binding's gate within 10s")
		}
	}
	if hasBindingMark(t, s) {
		t.Error("the binding's mark is in while its arrival waits for the gate; want it uncommitted")
	}
	if holds, _ := bindingHolds(t, s); holds {
		t.Error("a transaction that asks while an arrival waits for the gate finds the binding holding; want its try to fail and answer that a build without it is in the fleet")
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("the publish without the binding, once the holder committed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the publish without the binding still waited 10s after the holder committed")
	}
	if !hasBindingMark(t, s) {
		t.Error("the publish without the binding stamped no mark")
	}
	besides, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer besides.Rollback(ctx)
	if holds, err := AccountBindingHolds(ctx, besides); err != nil || holds {
		t.Errorf("AccountBindingHolds after the arrival = %t, %v; want false", holds, err)
	}
	if bindingGateHeld(t, s, besides.Conn().PgConn().PID()) {
		t.Error("a transaction that found a build without the binding in the fleet holds the binding's gate; want none")
	}
	if err := besides.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// The registration's bound, on a fleet with the mark aged out and a holder again.
	ageBindingMark(t, s, OwnerLockMarkWindow+time.Minute)
	holder, err = s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if holds, err := AccountBindingHolds(ctx, holder); err != nil || !holds {
		t.Fatalf("AccountBindingHolds with the mark aged out = %t, %v; want true", holds, err)
	}
	started := time.Now()
	registered := sendUnboundArrival(conn, `insert into server_processes (instance, signing_keys, owner_lock_protocol) values (gen_random_uuid(), '{fingerprint}', 1)`)
	select {
	case err := <-registered:
		var pgErr *pgconn.PgError
		waited := time.Since(started)
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Errorf("the registration without the binding behind a holder answered %v; want 55P03 lock_not_available", err)
		}
		if waited < 1500*time.Millisecond || waited > 4500*time.Millisecond {
			t.Errorf("the registration without the binding was refused after %s; want about its 2 s bound", waited.Round(time.Millisecond))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the registration without the binding still waited 20s behind a holder; want it refused at its 2 s bound")
	}
}

// A sign-in beside a build without the binding records the account and moves the recording time,
// as the database's triggers do for any write of sub, and revokes and owes nothing, as that
// build's sign-in does; once no such build is in the fleet, the next replacement revokes the
// address's tokens and owes the replaced account's revocation (the control, in the test, that the
// rows it reads can show the binding's writes).
func TestASignInBesideABuildWithoutTheBindingRevokesAndOwesNothing(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	people := brokerPeople(database)
	const email = "binding-fleet@example.com"
	signInAll(t, people, email, "account-a")
	tokenA := mintToken(t, database.Pool, email, "token-a", "account-a")
	tokenNone := mintToken(t, database.Pool, email, "token-none", "")
	recordedAt := func() time.Time {
		t.Helper()
		var at time.Time
		if err := database.Pool.QueryRow(ctx, `select sub_recorded_at from people where email = $1`, email).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	owed := func() int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(ctx, `select count(*) from broker_revocations where email = $1`, email).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	first := recordedAt()

	registerUnboundProcess(t, database, 1, 0)
	signInAll(t, people, email, "account-b")
	var sub string
	if err := database.Pool.QueryRow(ctx, `select sub from people where email = $1`, email).Scan(&sub); err != nil || sub != "account-b" {
		t.Fatalf("people records %q, %v after account-b's sign-in; want account-b", sub, err)
	}
	if !recordedAt().After(first) {
		t.Errorf("account-b's sign-in beside a build without the binding left the recording time at %s; want it moved", first)
	}
	for name, id := range map[string]string{"token-a": tokenA, "token-none": tokenNone} {
		if _, revoked := tokenState(t, database.Pool, id); revoked {
			t.Errorf("account-b's sign-in beside a build without the binding revoked %s; want nothing revoked", name)
		}
	}
	if n := owed(); n != 0 {
		t.Errorf("account-b's sign-in beside a build without the binding owes %d broker revocations; want none", n)
	}

	if _, err := database.Pool.Exec(ctx, `delete from server_processes`); err != nil {
		t.Fatal(err)
	}
	ageBindingMark(t, database, OwnerLockMarkWindow+time.Minute)
	signInAll(t, people, email, "account-c")
	for name, id := range map[string]string{"token-a": tokenA, "token-none": tokenNone} {
		if _, revoked := tokenState(t, database.Pool, id); !revoked {
			t.Errorf("control: account-c's sign-in with no build without the binding in the fleet left %s live; want it revoked", name)
		}
	}
	if n := owed(); n != 1 {
		t.Errorf("control: account-c's sign-in with no build without the binding in the fleet owes %d broker revocations; want account-b's", n)
	}
}
