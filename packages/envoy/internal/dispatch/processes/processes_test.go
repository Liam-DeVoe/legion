package processes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/clock"
	"github.com/sjawhar/envoy/internal/clock/clocktest"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/testpg/testdb"
	"github.com/sjawhar/envoy/internal/testwait"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m.Run)) }

// fingerprints is the fingerprint list a process holding setting (DISPATCH_SIGNING_KEY's form)
// registers with.
func fingerprints(t *testing.T, setting string) []string {
	t.Helper()
	keys, err := auth.ParseSigningKeys(setting)
	if err != nil {
		t.Fatalf("parse %q: %v", setting, err)
	}
	return keys.Fingerprints()
}

// registerAnswer is what a registration running on a goroutine of its own answered.
type registerAnswer struct {
	registration *Registration
	err          error
}

// register registers a process holding setting and deregisters it when the test ends, unless the
// test has.
func register(t *testing.T, database *store.Store, commit, setting string) (*Registration, error) {
	t.Helper()
	return registerOn(t, database, clock.Real(), commit, setting)
}

// registerOn is register with the registration's own waits -- its wait for admission and its
// heartbeat -- on c, so a test moves them rather than shortening the registry's intervals
// (RegisterOn). Whether a row is live is the database's clock either way, which a test reaches
// by ageing the row (ageHeartbeat).
func registerOn(t *testing.T, database *store.Store, c clock.Clock, commit, setting string) (*Registration, error) {
	t.Helper()
	registration, err := RegisterOn(context.Background(), c, database.Pool, Instance{
		Commit: commit, DashboardBuild: "build-" + commit, SigningKeys: fingerprints(t, setting),
	})
	if err == nil {
		t.Cleanup(func() { _ = registration.Deregister(context.Background()) })
	}
	return registration, err
}

// mustRegister is register for a process that must be accepted.
func mustRegister(t *testing.T, database *store.Store, commit, setting string) *Registration {
	t.Helper()
	registration, err := register(t, database, commit, setting)
	if err != nil {
		t.Fatalf("register a process holding %q: %v", setting, err)
	}
	return registration
}

// mustRegisterOn is registerOn for a process that must be accepted.
func mustRegisterOn(t *testing.T, database *store.Store, c clock.Clock, commit, setting string) *Registration {
	t.Helper()
	registration, err := registerOn(t, database, c, commit, setting)
	if err != nil {
		t.Fatalf("register a process holding %q: %v", setting, err)
	}
	return registration
}

// execer is what ageHeartbeat writes through: the store's pool, or a transaction the test holds.
type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ageHeartbeat sets the row's heartbeat to age before the database's now(), the clock the
// registry measures by; a negative age puts it that far ahead. Through a transaction, now() is
// that transaction's start, so rows aged and read in one transaction are judged at one moment.
func ageHeartbeat(t *testing.T, db execer, id string, age time.Duration) {
	t.Helper()
	tag, err := db.Exec(context.Background(),
		`update server_processes set heartbeat_at = now() - make_interval(secs => $2) where instance = $1`, id, age.Seconds())
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("age process %s's heartbeat by %v: %v (%d rows)", id, age, err, tag.RowsAffected())
	}
}

// heldLiveFor is how far ahead of the database's now() a test puts the heartbeat of a row whose
// liveness a verdict needs but is not what that verdict is about: an hour, longer than any run, so
// the row counts as live at every read of the registry however long the machine takes, until the
// test ages it again or a heartbeat stamps it with now().
const heldLiveFor = time.Hour

// holdLive puts id's heartbeat heldLiveFor ahead of the database's now() (ageHeartbeat).
func holdLive(t *testing.T, database *store.Store, id string) {
	t.Helper()
	ageHeartbeat(t, database.Pool, id, -heldLiveFor)
}

// liveIDs is the instance id of every live process.
func liveIDs(t *testing.T, database *store.Store) []string {
	t.Helper()
	return liveIDsIn(t, database.Pool)
}

// liveIDsIn is liveIDs read through db: the pool, or a transaction whose start is the now() Live
// measures every row by.
func liveIDsIn(t *testing.T, db Querier) []string {
	t.Helper()
	live, err := Live(context.Background(), db)
	if err != nil {
		t.Fatalf("read the live processes: %v", err)
	}
	ids := make([]string, 0, len(live))
	for _, p := range live {
		ids = append(ids, p.ID)
	}
	return ids
}

// heartbeatOf is id's heartbeat_at as its row holds it, and whether the registry holds the row.
func heartbeatOf(t *testing.T, database *store.Store, id string) (time.Time, bool) {
	t.Helper()
	var at time.Time
	err := database.Pool.QueryRow(context.Background(),
		`select heartbeat_at from server_processes where instance = $1`, id).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false
	}
	if err != nil {
		t.Fatalf("read process %s's heartbeat: %v", id, err)
	}
	return at, true
}

// waitForStamp waits until id's row holds a heartbeat stamped at or after since, a moment of the
// database's clock the test read before it brought a heartbeat. The beat stamps the row with its
// own transaction's now(), which began after since, so a heartbeat at or past since is that beat's,
// and it dated the row live as it wrote it, however much time has passed by this read. The
// reading names a row that is gone, or a heartbeat that predates since.
func waitForStamp(t *testing.T, database *store.Store, id string, since time.Time, what string) {
	t.Helper()
	testwait.Until(t, what, func() (string, bool) {
		at, exists := heartbeatOf(t, database, id)
		switch {
		case !exists:
			return "no row for " + id, false
		case at.Before(since):
			return fmt.Sprintf("a heartbeat of %s, before %s", at.Format(time.RFC3339Nano), since.Format(time.RFC3339Nano)), false
		}
		return "a heartbeat of " + at.Format(time.RFC3339Nano), true
	})
}

// rowExists reports whether the registry holds a row for id, live or not.
func rowExists(t *testing.T, database *store.Store, id string) bool {
	t.Helper()
	var exists bool
	if err := database.Pool.QueryRow(context.Background(),
		`select exists (select 1 from server_processes where instance = $1)`, id).Scan(&exists); err != nil {
		t.Fatalf("read process %s's row: %v", id, err)
	}
	return exists
}

// A registered process is live and reads back as it registered: its build, its dashboard and its
// key fingerprints, current first.
func TestRegisterRecordsTheProcessAsLive(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	// Live is read in a transaction begun before the registration, and judges every row by that
	// transaction's now(), a moment before Register wrote this one: the row reads live however long
	// the machine takes before the read, while a heartbeat Register dated LiveWindow or more before
	// that moment would not.
	before, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the transaction Live is read in: %v", err)
	}
	defer func() { _ = before.Rollback(context.Background()) }()
	registration := mustRegister(t, database, "c1", "K1 K0")

	live, err := Live(ctx, before)
	if err != nil {
		t.Fatalf("read the live processes: %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("live processes = %+v, want the one registered", live)
	}
	got := live[0]
	if got.ID != registration.ID || got.Commit != "c1" || got.DashboardBuild != "build-c1" ||
		!slices.Equal(got.SigningKeys, fingerprints(t, "K1 K0")) {
		t.Errorf("live process = %+v, want id %s, commit c1, dashboard build-c1 and the fingerprints of K1 then K0", got, registration.ID)
	}
	if got.StartedAt.IsZero() || got.HeartbeatAt.Before(got.StartedAt) {
		t.Errorf("live process started %v with heartbeat %v, want both set by the database", got.StartedAt, got.HeartbeatAt)
	}
}

// liveProcess is id's row as Live reads it.
func liveProcess(t *testing.T, database *store.Store, id string) Process {
	t.Helper()
	live, err := Live(context.Background(), database.Pool)
	if err != nil {
		t.Fatalf("read the live processes: %v", err)
	}
	for _, p := range live {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("process %s is not live; live = %+v", id, live)
	return Process{}
}

// The four fleet columns - the schema version and creatable version the process's documents
// read and write, its embedder's identity and whether it stores files in a bucket - read back as
// the process registered them, and again after a heartbeat writes a pruned row back.
func TestTheFleetColumnsRoundTripThroughRegisterAndLive(t *testing.T) {
	database := storetest.Open(t)
	// The registration's heartbeat runs on this clock, so the beat that writes the pruned row
	// back comes where the test moves it.
	fake := clocktest.New(time.Now())
	registration, err := RegisterOn(context.Background(), fake, database.Pool, Instance{
		Commit: "c1", DashboardBuild: "build-c1", SigningKeys: fingerprints(t, "K0"),
		SchemaVersion: 3, SchemaCreatable: 2, EmbedderIdentity: "azure:example.openai.azure.com/embed", FileStore: true,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = registration.Deregister(context.Background()) })
	want := func(what string, got Process) {
		t.Helper()
		if got.SchemaVersion != 3 || got.SchemaCreatable != 2 || got.EmbedderIdentity != "azure:example.openai.azure.com/embed" ||
			!got.FileStore || got.PredatesFleetColumns() {
			t.Errorf("%s = %+v, want schema version 3, creatable 2, the Azure identity and a file store", what, got)
		}
	}
	// Live's reading of the columns is this row's subject, and whether the row is live is not, so
	// the row is held live for each read (holdLive): the registered row, and the row the heartbeat
	// writes back once the beat's stamp is in (waitForStamp).
	holdLive(t, database, registration.ID)
	want("the registered row", liveProcess(t, database, registration.ID))

	underRegistryLock(t, database, func(tx pgx.Tx) { prunedRow(t, tx, registration.ID) })
	fake.AwaitArmed(t, "the heartbeat", 1)
	since := testdb.Now(t, database.Pool)
	fake.Advance(HeartbeatInterval)
	waitForStamp(t, database, registration.ID, since, "the heartbeat to write a pruned row back")
	holdLive(t, database, registration.ID)
	want("the row written again after a prune", liveProcess(t, database, registration.ID))
}

// A refresh writes the schema numbers the process serves now into its row, where every process's
// socket admission reads them, and a row written again after a prune carries the refreshed numbers
// rather than the ones the process registered with. A refresh to the numbers it last wrote writes
// nothing: the process's own record of its row is what it compares with, not the row.
func TestARefreshWritesTheSchemaNumbersTheProcessServesNow(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	// The registration's heartbeat runs on this clock, so the beat that writes the pruned row
	// back comes where the test moves it.
	fake := clocktest.New(time.Now())
	registration, err := RegisterOn(ctx, fake, database.Pool, Instance{
		Commit: "c1", SigningKeys: fingerprints(t, "K0"), SchemaVersion: 3, SchemaCreatable: 3,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = registration.Deregister(context.Background()) })
	// The numbers are read through Live, as every process's socket admission reads them, and the
	// row is held live for each read (holdLive): whether it is live is not this row's subject.
	numbers := func(what string, version, creatable int) {
		t.Helper()
		if got := liveProcess(t, database, registration.ID); got.SchemaVersion != version || got.SchemaCreatable != creatable {
			t.Errorf("%s holds schema version %d and creatable %d, want %d and %d", what, got.SchemaVersion, got.SchemaCreatable, version, creatable)
		}
	}
	holdLive(t, database, registration.ID)

	if err := registration.RefreshSchema(ctx, func() (int, int) { return 4, 3 }); err != nil {
		t.Fatalf("refresh the schema numbers: %v", err)
	}
	numbers("the row after a refresh to version 4", 4, 3)

	underRegistryLock(t, database, func(tx pgx.Tx) { prunedRow(t, tx, registration.ID) })
	fake.AwaitArmed(t, "the heartbeat", 1)
	since := testdb.Now(t, database.Pool)
	fake.Advance(HeartbeatInterval)
	waitForStamp(t, database, registration.ID, since, "the heartbeat to write back a row pruned after a refresh")
	holdLive(t, database, registration.ID)
	numbers("the row written again after a prune", 4, 3)

	// A refresh to the numbers it last wrote writes nothing, so a row changed behind the
	// registration keeps what it was changed to.
	underRegistryLock(t, database, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `update server_processes set schema_version = 9 where instance = $1`, registration.ID); err != nil {
			t.Fatalf("change the row behind the registration: %v", err)
		}
	})
	if err := registration.RefreshSchema(ctx, func() (int, int) { return 4, 3 }); err != nil {
		t.Fatalf("refresh the schema numbers again: %v", err)
	}
	numbers("the row a refresh to the numbers it last wrote leaves alone", 9, 3)
}

// A row written by a build that predates the fleet columns carries none of them, so each holds its
// default: it reads as a process that predates them, with no schema numbers, no embedder identity
// and no file store.
func TestARowAtTheDefaultsPredatesTheFleetColumns(t *testing.T) {
	database := storetest.Open(t)
	var id string
	if err := database.Pool.QueryRow(context.Background(), `
		insert into server_processes (instance, commit, dashboard_build, signing_keys)
		values (gen_random_uuid(), 'c0', 'build-c0', $1)
		returning instance::text`, fingerprints(t, "K0")).Scan(&id); err != nil {
		t.Fatalf("insert a row as a build that predates the fleet columns writes it: %v", err)
	}
	// Live is what reads the row; whether it is live is not this row's subject, so it is held live
	// for the read (holdLive), which writes nothing but its heartbeat.
	holdLive(t, database, id)
	got := liveProcess(t, database, id)
	if !got.PredatesFleetColumns() || got.SchemaVersion != 0 || got.SchemaCreatable != 0 || got.EmbedderIdentity != "" || got.FileStore {
		t.Errorf("a row at the defaults = %+v, want one that predates the fleet columns: 0, 0, no identity, no file store", got)
	}
}

// Live is measured on the database's clock: a heartbeat 14 seconds before the database's now() is
// live, one 16 seconds before it is not, whatever this process's clock says.
func TestLiveCountsAHeartbeatWithinFifteenSecondsOfTheDatabaseClock(t *testing.T) {
	// The registrations' clock, which nothing moves: no heartbeat of theirs stamps a row the
	// test has aged.
	held := clocktest.New(time.Now())
	database := storetest.Open(t)
	fresh := mustRegisterOn(t, database, held, "c1", "K0")
	stale := mustRegisterOn(t, database, held, "c1", "K0")
	// Both rows are aged, and Live reads them, in one transaction, whose now() is the one moment
	// the three statements measure by: the 14 s row is 14 s old at the read however long the
	// machine takes between the statements.
	tx, err := database.Pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin the transaction the rows are aged and read in: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	ageHeartbeat(t, tx, fresh.ID, 14*time.Second)
	ageHeartbeat(t, tx, stale.ID, 16*time.Second)

	if got := liveIDsIn(t, tx); !slices.Equal(got, []string{fresh.ID}) {
		t.Errorf("live = %v, want only the process whose heartbeat is 14 s old (%s), not the 16 s one (%s)", got, fresh.ID, stale.ID)
	}
}

// The heartbeat keeps a registered process live: a row the database dates past the live window
// is live again within a heartbeat, since no live process disagrees with its key set.
func TestTheHeartbeatKeepsARegisteredProcessLive(t *testing.T) {
	database := storetest.Open(t)
	fake := clocktest.New(time.Now())
	registration := mustRegisterOn(t, database, fake, "c1", "K0")
	ageHeartbeat(t, database.Pool, registration.ID, time.Hour)

	// The heartbeat is armed one interval out on the registration's clock; moving the clock
	// there is the beat, and nothing else makes one.
	armed := fake.AwaitArmed(t, "the heartbeat", 1)
	if len(armed) != 1 {
		t.Fatalf("the registration armed %v, want its heartbeat alone", armed)
	}
	// The beat's stamp is told by a moment of the database's clock read before the beat: a
	// heartbeat at or past it is the beat's, which dated the row live as it wrote it, however long
	// the machine takes before the read (waitForStamp).
	since := testdb.Now(t, database.Pool)
	fake.Set(armed[0])
	waitForStamp(t, database, registration.ID, since, "one heartbeat to stamp a row aged past the live window live again")
}

// Deregister deletes the row and stops the heartbeat, so the process is neither live nor written
// again.
func TestDeregisterDeletesTheRow(t *testing.T) {
	database := storetest.Open(t)
	fake := clocktest.New(time.Now())
	registration := mustRegisterOn(t, database, fake, "c1", "K0")
	if err := registration.Deregister(context.Background()); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	// Deregister waits for the heartbeat to return before it deletes the row, so no beat can
	// write it again: the registration holds no timer on its clock any more.
	if armed := fake.Armed(); len(armed) != 0 {
		t.Errorf("the deregistered process left %v armed on its clock, want no heartbeat", armed)
	}
	if rowExists(t, database, registration.ID) {
		t.Error("the deregistered process's row is still in the registry")
	}
	if got := liveIDs(t, database); len(got) != 0 {
		t.Errorf("live = %v after the only process deregistered, want none", got)
	}
}

// The prune loop deletes the rows whose heartbeat the database dates more than ten minutes ago,
// and keeps every newer one, live or not.
//
// The ages are measured from the pass's own now(), not from a moment before it, so no verdict
// leans on time not passing between the two. The aged rows are first put past PruneAge and
// locked by a transaction of the test's, so the pass's delete, which finds both by the snapshot it
// reads, waits on that lock; the test then dates each against the delete's now() (its
// transaction's start, pg_stat_activity's xact_start) and commits, and Postgres judges each row
// the delete waited on again, by what the test wrote, before it deletes it.
func TestPruneRemovesRowsOlderThanTenMinutesAndKeepsNewerOnes(t *testing.T) {
	database := storetest.Open(t)
	leases := storetest.Leases(t, database.Pool)
	// The registrations' clock, which nothing moves: no heartbeat of theirs stamps a row the
	// test has aged. The prune loop's own clock is a second one, so moving it to the loop's
	// tick brings a pass and no beat.
	held := clocktest.New(time.Now())
	old := mustRegisterOn(t, database, held, "c1", "K0")
	recent := mustRegisterOn(t, database, held, "c1", "K0")
	live := mustRegisterOn(t, database, held, "c1", "K0")
	holder, err := database.Pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin the transaction holding the aged rows: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	for _, id := range []string{old.ID, recent.ID} {
		ageHeartbeat(t, database.Pool, id, PruneAge+time.Minute)
		if _, err := holder.Exec(context.Background(), `select 1 from server_processes where instance = $1 for update`, id); err != nil {
			t.Fatalf("lock process %s's row: %v", id, err)
		}
	}

	pruning := clocktest.New(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPruneOn(ctx, pruning, database.Pool, leases)
	}()
	defer func() {
		cancel()
		testwait.Receive(t, "RunPruneOn to return once its context ended", done)
	}()
	tick := pruning.AwaitArmed(t, "the prune loop's first tick", 1)
	pruning.Set(tick[0])
	storetest.WaitForLockWaiters(t, holder, 1)
	var pass time.Time
	if err := holder.QueryRow(context.Background(), `
		select a.xact_start from pg_locks l join pg_stat_activity a on a.pid = l.pid
		where not l.granted and pg_backend_pid() = any(pg_blocking_pids(l.pid))`).Scan(&pass); err != nil {
		t.Fatalf("read when the prune's delete, queued on the aged rows, began: %v", err)
	}
	for _, row := range []struct {
		id  string
		age time.Duration
	}{{old.ID, PruneAge + time.Second}, {recent.ID, PruneAge - time.Minute}} {
		if _, err := holder.Exec(context.Background(), `update server_processes set heartbeat_at = $2 where instance = $1`,
			row.id, pass.Add(-row.age)); err != nil {
			t.Fatalf("date process %s's heartbeat %v before the prune's now(): %v", row.id, row.age, err)
		}
	}
	if err := holder.Commit(context.Background()); err != nil {
		t.Fatalf("let the prune's delete go: %v", err)
	}
	testwait.Eventually(t, "the prune to delete the row whose heartbeat is ten minutes and a second old", func() bool {
		return !rowExists(t, database, old.ID)
	})
	if !rowExists(t, database, recent.ID) {
		t.Error("the prune loop deleted a row whose heartbeat is nine minutes old")
	}
	if !rowExists(t, database, live.ID) {
		t.Error("the prune loop deleted a live process's row")
	}
}

// A live process refuses a newcomer whose key set does not share its own both ways, each way on
// its own, and the refusal records nothing and says what to do. Key sets that overlap with the
// current keys swapped, which is the middle of a rotation, are accepted, and so is a key set no
// live process shares when the only other row is a process gone. A peer that refuses stays live
// throughout, so its refusal stands through Register's wait for it to lapse.
func TestRegisterRefusesAKeySetALivePeerDoesNotShare(t *testing.T) {
	for _, c := range []struct {
		name, peer, mine string
		stale            bool
		refused          bool
		says             string
	}{
		{name: "the peer's current key is not one of mine", peer: "K0 K1", mine: "K1", refused: true,
			says: "signs its cookies with a key this process does not hold"},
		{name: "my current key is not one of the peer's", peer: "K0", mine: "K1 K0", refused: true,
			says: "does not hold the key this process would sign its cookies with"},
		{name: "keys of their own", peer: "K0", mine: "K9", refused: true,
			says: "signs its cookies with a key this process does not hold"},
		{name: "the rotation's middle: K1 K0 beside K0 K1", peer: "K1 K0", mine: "K0 K1"},
		{name: "one key set", peer: "K0", mine: "K0"},
		{name: "another key, but the peer is gone", peer: "K0", mine: "K9", stale: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			database := storetest.Open(t)
			// The peer heartbeats on a clock of its own, which nothing moves, so no beat of its
			// stamps the row the test ages or holds live; the newcomer waits on the other.
			held := clocktest.New(time.Now())
			fake := clocktest.New(time.Now())
			peer := mustRegisterOn(t, database, held, "c1", c.peer)
			switch {
			case c.stale:
				// A gone process does not heartbeat back into Live: its clock never moves here.
				ageHeartbeat(t, database.Pool, peer.ID, LiveWindow+time.Second)
			case c.refused:
				// A refusing peer is live at every try of the newcomer's, however long the
				// machine takes before them (holdLive).
				holdLive(t, database, peer.ID)
			}
			answered := make(chan registerAnswer, 1)
			go func() {
				registration, err := registerOn(t, database, fake, "c2", c.mine)
				answered <- registerAnswer{registration: registration, err: err}
			}()
			if c.refused {
				// The wait for the peer's row to lapse runs on this clock, and the row is live
				// on the database's clock throughout, so the wait ends in the refusal where the
				// test moves it past LiveWindow and one HeartbeatInterval.
				fake.AwaitArmed(t, "Register's retry gap", 1)
				fake.Advance(LiveWindow + HeartbeatInterval)
			}
			got := testwait.Receive(t, "the registration's answer beside the peer", answered)
			registration, err := got.registration, got.err
			if !c.refused {
				if err != nil {
					t.Fatalf("a process holding %q beside a peer holding %q was refused: %v", c.mine, c.peer, err)
				}
				if !rowExists(t, database, registration.ID) {
					t.Errorf("the accepted process has no row")
				}
				return
			}
			if !errors.Is(err, ErrKeySetMismatch) {
				t.Fatalf("a process holding %q beside a live peer holding %q registered with %v, want ErrKeySetMismatch", c.mine, c.peer, err)
			}
			for _, part := range []string{peer.ID, "commit c1", c.says, "DISPATCH_SIGNING_KEY", "add the new key second"} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("the refusal %q does not say %q", err, part)
				}
			}
			var rows int
			if err := database.Pool.QueryRow(context.Background(), `select count(*) from server_processes`).Scan(&rows); err != nil {
				t.Fatalf("count the registry's rows: %v", err)
			}
			if rows != 1 {
				t.Errorf("the registry holds %d rows after the refusal, want the peer's alone", rows)
			}
		})
	}
}

// Two processes with keys of their own booting at once are serialised: the second reads the
// first's row and is refused, so exactly one of them registers, and the refusal stands through the
// second's wait for that row to lapse. Both are held at their first read of the registry by a lock
// this test takes on the table, and released together, which is the moment two reads without the
// registry's own lock would both find the registry empty.
//
// Booting at once is the two transactions beginning within LiveWindow of each other: each
// registration judges the rows it reads by its own transaction's now(), its start (admit, Live),
// and the first one's row is dated with that one's. So the row reads both starts from the
// database while the two are held (registerTwoAtOnce), and an attempt whose transactions the
// machine began LiveWindow or more apart, in which the second may lawfully count the first as
// gone, is made again on a database of its own.
func TestTwoRegistrationsAtOnceWithKeysOfTheirOwnAreSerialised(t *testing.T) {
	for attempt := 1; ; attempt++ {
		pair := registerTwoAtOnce(t)
		if pair.begunApart >= LiveWindow {
			t.Logf("attempt %d: the two registrations' transactions began %s apart, LiveWindow (%s) or more, so the second may lawfully have counted the first as gone; trying again on a new database",
				attempt, pair.begunApart, LiveWindow)
			continue
		}
		if !errors.Is(pair.second.err, ErrKeySetMismatch) {
			t.Fatalf("the second registration with keys of their own answered %v once its wait was over, want ErrKeySetMismatch: exactly one of the two is refused", pair.second.err)
		}
		if got := liveIDs(t, pair.database); !slices.Equal(got, []string{pair.first.registration.ID}) {
			t.Errorf("live = %v, want the one registration accepted, %s", got, pair.first.registration.ID)
		}
		return
	}
}

// twoAtOnce is one attempt of TestTwoRegistrationsAtOnceWithKeysOfTheirOwnAreSerialised: its
// database, how far apart the two registrations' transactions began, and what each answered, the
// first to answer first.
type twoAtOnce struct {
	database      *store.Store
	begunApart    time.Duration
	first, second registerAnswer
}

// registerTwoAtOnce runs two registrations with keys of their own, K0 and K1, at once on a
// database of its own, held at their first read of the registry until both have queued there.
// Each waits and heartbeats on a clock of its own. The first to answer must be the one admitted,
// since the other answers only once the test has moved its clock: the admitted one's row is put
// an hour ahead (holdLive) and its clock never moved, so it is live through the other's whole
// wait however long the machine takes, and the test then moves the other's clock past
// LiveWindow and one HeartbeatInterval, which ends that wait.
func registerTwoAtOnce(t *testing.T) twoAtOnce {
	t.Helper()
	database := storetest.Open(t)
	gate, err := database.Pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("open the transaction holding the registry: %v", err)
	}
	defer func() { _ = gate.Rollback(context.Background()) }()
	if _, err := gate.Exec(context.Background(), `lock table server_processes in access exclusive mode`); err != nil {
		t.Fatalf("hold the registry: %v", err)
	}

	clocks := []*clocktest.Fake{clocktest.New(time.Now()), clocktest.New(time.Now())}
	type answer struct {
		index int
		registerAnswer
	}
	answers := make(chan answer, 2)
	for i, setting := range []string{"K0", "K1"} {
		go func() {
			registration, err := registerOn(t, database, clocks[i], "c1", setting)
			answers <- answer{index: i, registerAnswer: registerAnswer{registration: registration, err: err}}
		}()
	}
	storetest.WaitForLockWaiters(t, gate, 2)
	// Both transactions have begun by now: one holds the registry's lock and waits on the table,
	// the other waits on that lock. pg_stat_activity is read once, through the gate's connection.
	var queued int
	var apart float64
	if err := gate.QueryRow(context.Background(), `
		select count(*), coalesce(extract(epoch from max(xact_start) - min(xact_start)), 0)
		from pg_stat_activity
		where datname = current_database() and wait_event_type = 'Lock'`).Scan(&queued, &apart); err != nil {
		t.Fatalf("read when the two registrations' transactions began: %v", err)
	}
	if queued != 2 {
		t.Fatalf("%d backends wait on a lock while the registry is held, want the two registrations", queued)
	}
	if err := gate.Rollback(context.Background()); err != nil {
		t.Fatalf("release the registry: %v", err)
	}

	first := testwait.Receive(t, "the first of the two registrations to answer", answers)
	if first.err != nil {
		t.Fatalf("the first registration to answer answered %v, want acceptance: the other answers only once the test moves its clock", first.err)
	}
	holdLive(t, database, first.registration.ID)
	waiting := clocks[1-first.index]
	waiting.AwaitArmed(t, "the other registration's retry gap", 1)
	waiting.Advance(LiveWindow + HeartbeatInterval)
	second := testwait.Receive(t, "the other registration's answer once its wait was over", answers)
	return twoAtOnce{
		database:   database,
		begunApart: time.Duration(apart * float64(time.Second)),
		first:      first.registerAnswer,
		second:     second.registerAnswer,
	}
}

// A process killed without deregistering leaves a row that counts as live until LiveWindow after
// its last heartbeat, and a newcomer whose key set it does not share - a dev sign-in server
// restarted after a kill, whose key is minted per process - is refused beside it at first.
// Register waits for the row to lapse instead of refusing at once, and admits the newcomer then.
func TestARefusalBesideARowThatStopsHeartbeatingIsAdmittedOnceTheRowLapses(t *testing.T) {
	database := storetest.Open(t)
	fake := clocktest.New(time.Now())
	var dead string
	// The dead row is written an hour ahead of the database's clock (livePeer), so it counts as
	// live until the test ages it below, however much time passes before then; and the newcomer's
	// row, once admitted, is held live the same way for the read that ends the row (holdLive), so
	// no verdict of this row depends on LiveWindow not passing.
	underRegistryLock(t, database, func(tx pgx.Tx) { dead = livePeer(t, tx, "K9") })

	// The newcomer is refused while the dead row still counts as live, and waits: its retry gap
	// is armed on this clock. The row then lapses on the database's clock, which this test
	// ages it against, and the retry the test brings admits the newcomer.
	answered := make(chan registerAnswer, 1)
	go func() {
		registration, err := registerOn(t, database, fake, "c2", "K0")
		answered <- registerAnswer{registration: registration, err: err}
	}()
	fake.AwaitArmed(t, "Register's retry gap", 1)
	// A newcomer admitted at once would arm its heartbeat's gap (Registration.heartbeat) here
	// instead, for the same deadline, so the timer alone does not tell the two apart. Admission
	// commits the newcomer's row (tryRegister) before RegisterOn starts that heartbeat, so the
	// dead row alone is live while the newcomer is refused and waits.
	live := liveIDs(t, database)
	if !slices.Contains(live, dead) {
		t.Fatalf("live = %v once a timer was armed on the newcomer's clock: the dead row %s lapsed before the test aged it, so the refusal this row reads was not tested", live, dead)
	}
	if !slices.Equal(live, []string{dead}) {
		t.Fatalf("live = %v once a timer was armed on the newcomer's clock, want the dead row %s alone: the newcomer was admitted beside it at once rather than refused and left waiting for it to lapse", live, dead)
	}
	ageHeartbeat(t, database.Pool, dead, LiveWindow+time.Second)
	fake.Advance(HeartbeatInterval)
	got := testwait.Receive(t, "the newcomer's answer once the dead row lapsed", answered)
	registration, err := got.registration, got.err
	if err != nil {
		t.Fatalf("a process beside a row that stopped heartbeating was refused: %v", err)
	}
	holdLive(t, database, registration.ID)
	if got := liveIDs(t, database); !slices.Equal(got, []string{registration.ID}) {
		t.Errorf("live = %v, want the admitted process %s alone, the dead row %s lapsed", got, registration.ID, dead)
	}
}

// Beside a peer that keeps heartbeating, Register's wait ends in the refusal: the peer's row stays
// live past LiveWindow and one HeartbeatInterval after the first refusal, and nothing is recorded.
//
// The wait runs on a frozen clock of its own, which this test moves by Set, so what the wait is
// worth is read off that clock rather than off the machine's: it answers its refusal once that
// clock has passed LiveWindow and one HeartbeatInterval, and nothing sooner. A clock whose Now
// drifted with the wall clock would re-arm the retry gap just past the moment Set moves to, and
// the gap would never fire. The peer heartbeats once on a second clock the test beats by hand,
// and its row is then put an hour ahead of the database's clock (holdLive), so it is live
// throughout however long the machine takes over the wait.
func TestARefusalBesideAPeerThatKeepsHeartbeatingStandsAfterTheWait(t *testing.T) {
	database := storetest.Open(t)
	beating := clocktest.New(time.Now())
	peer := mustRegisterOn(t, database, beating, "c1", "K9")
	beating.AwaitArmed(t, "the peer's heartbeat", 1)
	since := testdb.Now(t, database.Pool)
	beating.Advance(HeartbeatInterval)
	waitForStamp(t, database, peer.ID, since, "the peer's heartbeat to stamp its row")
	holdLive(t, database, peer.ID)

	waiting := clocktest.New(time.Date(2026, time.November, 5, 3, 0, 0, 0, time.UTC))
	began := waiting.Now()
	refused := make(chan error, 1)
	go func() {
		_, err := registerOn(t, database, waiting, "c2", "K0")
		refused <- err
	}()
	// Each retry arms the gap to the next, and the registration's clock holds that gap alone:
	// a live window in, the wait has answered nothing.
	waiting.AwaitArmed(t, "Register's first retry gap", 1)
	waiting.Set(began.Add(LiveWindow))
	waiting.AwaitArmed(t, "Register's retry gap a live window in", 1)
	select {
	case err := <-refused:
		t.Fatalf("Register answered %v a live window in, want it waiting until LiveWindow and one HeartbeatInterval have passed", err)
	default:
	}

	waiting.Set(began.Add(LiveWindow + HeartbeatInterval))
	err := testwait.Receive(t, "Register's refusal once its wait for the peer to lapse is over", refused)
	if !errors.Is(err, ErrKeySetMismatch) || !strings.Contains(err.Error(), peer.ID) {
		t.Fatalf("a process beside a heartbeating peer with another key registered with %v, want ErrKeySetMismatch naming %s", err, peer.ID)
	}
	if got := liveIDs(t, database); !slices.Equal(got, []string{peer.ID}) {
		t.Errorf("live = %v after the refusal, want the peer %s alone", got, peer.ID)
	}
}

// Register's wait ends with its context: a process stopped (SIGTERM) while it waits for a refusing
// peer to lapse stops then, registered nowhere, not once the wait is over.
func TestRegistersWaitEndsWithItsContext(t *testing.T) {
	database := storetest.Open(t)
	fake := clocktest.New(time.Now())
	peer := mustRegisterOn(t, database, fake, "c1", "K9")
	// The peer must be live at the newcomer's first try, which it refuses, and at the read below,
	// however long the machine takes before them (holdLive); nothing moves its clock.
	holdLive(t, database, peer.ID)

	ctx, stop := context.WithCancel(context.Background())
	answered := make(chan error, 1)
	go func() {
		_, err := RegisterOn(ctx, fake, database.Pool, Instance{Commit: "c2", SigningKeys: fingerprints(t, "K0")})
		answered <- err
	}()
	// The wait is in its gap between retries, far from the moment it would give up, when the
	// process is stopped: nothing but the cancellation can answer it. The peer heartbeats on the
	// same fake, so its heartbeat timer is one of the two armed, and Register's gap is the other.
	fake.AwaitArmed(t, "Register's retry gap, beside the peer's heartbeat", 2)
	stop()
	if err := testwait.Receive(t, "Register's answer to its context ending", answered); !errors.Is(err, context.Canceled) {
		t.Fatalf("Register beside a refusing peer, its context ended, returned %v; want the context's error", err)
	}
	if got := liveIDs(t, database); !slices.Equal(got, []string{peer.ID}) {
		t.Errorf("live = %v, want the peer %s alone", got, peer.ID)
	}
}

// underRegistryLock runs change in one transaction holding the registry's lock, so no heartbeat
// lands between its statements: a row aged and a peer added there are what the next heartbeat
// finds together.
func underRegistryLock(t *testing.T, database *store.Store, change func(tx pgx.Tx)) {
	t.Helper()
	tx, err := database.Pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("open the transaction holding the registry's lock: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `select pg_advisory_xact_lock($1)`, registryLock); err != nil {
		t.Fatalf("take the registry's lock: %v", err)
	}
	change(tx)
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit the registry change: %v", err)
	}
}

// lapse dates the row's heartbeat past the live window, as a process frozen or cut from the
// database for that long leaves it.
func lapse(t *testing.T, tx pgx.Tx, id string) {
	t.Helper()
	ageHeartbeat(t, tx, id, LiveWindow+time.Second)
}

// prunedRow deletes the row, as the prune loop does once its heartbeat is PruneAge old.
func prunedRow(t *testing.T, tx pgx.Tx, id string) {
	t.Helper()
	tag, err := tx.Exec(context.Background(), `delete from server_processes where instance = $1`, id)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("delete process %s's row: %v (%d rows)", id, err, tag.RowsAffected())
	}
}

// livePeer writes the row of a live process holding setting straight into the registry, past
// Register's check, as a process registered while ours had dropped out of Live leaves it: with
// this build's owner lock protocol, as Register writes it. Its heartbeat is heldLiveFor ahead of
// the database's now(), so it counts as live at every read however long the machine takes, until
// the test ages it.
func livePeer(t *testing.T, tx pgx.Tx, setting string) string {
	t.Helper()
	var id string
	if err := tx.QueryRow(context.Background(), `
		insert into server_processes (instance, commit, dashboard_build, signing_keys, owner_lock_protocol, heartbeat_at)
		values (gen_random_uuid(), 'c9', 'build-c9', $1, $2, now() + make_interval(secs => $3))
		returning instance::text`, fingerprints(t, setting), store.OwnerLockProtocol, heldLiveFor.Seconds()).Scan(&id); err != nil {
		t.Fatalf("add a live peer holding %q: %v", setting, err)
	}
	return id
}

// requireNotLost fails when the registration's heartbeat has reported a refusal.
func requireNotLost(t *testing.T, registration *Registration, what string) {
	t.Helper()
	select {
	case err := <-registration.Lost():
		t.Fatalf("%s reported a refusal: %v", what, err)
	default:
	}
}

// requireRefusal waits for the registration's heartbeat to report a refusal naming the peer and
// the rotation advice, and requires that nothing made the process live again. The heartbeat has
// returned by the time Lost answers (heartbeat), so no beat of it can write the row after this
// read: the refusal is the last thing that registration does.
func requireRefusal(t *testing.T, database *store.Store, registration *Registration, peer, what string) {
	t.Helper()
	refusal := testwait.Receive(t, what+"'s refusal, reported by its heartbeat", registration.Lost())
	if !errors.Is(refusal, ErrKeySetMismatch) {
		t.Fatalf("%s reported %v, want ErrKeySetMismatch", what, refusal)
	}
	for _, part := range []string{peer, "commit c9", "DISPATCH_SIGNING_KEY", "add the new key second"} {
		if !strings.Contains(refusal.Error(), part) {
			t.Errorf("the refusal %q does not say %q", refusal, part)
		}
	}
	if got := liveIDs(t, database); slices.Contains(got, registration.ID) || !slices.Equal(got, []string{peer}) {
		t.Errorf("live = %v after %s was refused, want the peer %s alone", got, what, peer)
	}
}

// A process whose heartbeat lapsed beside a process registered meanwhile with a key set it does
// not share is refused by its next heartbeat, which reports it (Lost) and records nothing; beside a
// process that shares its keys it rejoins, and its row is the one it registered.
func TestALapsedHeartbeatChecksTheKeysAgainBeforeTheProcessCountsAsLive(t *testing.T) {
	for _, c := range []struct {
		name, peer string
		refused    bool
	}{
		{name: "beside a peer with a key of its own", peer: "K9", refused: true},
		{name: "beside a peer that does not hold my current key", peer: "K0", refused: true},
		{name: "beside a peer with the same key set", peer: "K1 K0"},
		{name: "beside a peer in the rotation's middle", peer: "K0 K1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			database := storetest.Open(t)
			fake := clocktest.New(time.Now())
			registration := mustRegisterOn(t, database, fake, "c1", "K1 K0")
			var peer string
			underRegistryLock(t, database, func(tx pgx.Tx) {
				lapse(t, tx, registration.ID)
				peer = livePeer(t, tx, c.peer)
			})
			// The next beat is the one that checks the keys again: it comes where the test
			// moves the clock and nowhere else.
			fake.AwaitArmed(t, "the heartbeat", 1)
			since := testdb.Now(t, database.Pool)
			fake.Advance(HeartbeatInterval)
			if c.refused {
				requireRefusal(t, database, registration, peer, "a lapsed process beside a peer holding "+c.peer)
				if !rowExists(t, database, registration.ID) {
					t.Error("the refused process's lapsed row is gone; the refusal must record nothing")
				}
				return
			}
			waitForStamp(t, database, registration.ID, since, "a lapsed process beside a peer holding "+c.peer+" to rejoin")
			requireNotLost(t, registration, "a lapsed process beside a peer holding "+c.peer)
			// The rejoined row is read through Live, held live for the read (holdLive), so a
			// row that is not there fails the read rather than passing over it.
			holdLive(t, database, registration.ID)
			if p := liveProcess(t, database, registration.ID); p.Commit != "c1" || !slices.Equal(p.SigningKeys, fingerprints(t, "K1 K0")) {
				t.Errorf("the rejoined row = %+v, want commit c1 and the fingerprints of K1 then K0", p)
			}
		})
	}
}

// A process whose row was pruned is written again by its next heartbeat, as it registered, when no
// live process disagrees with its key set; beside one that does, the heartbeat reports the refusal
// and writes nothing.
func TestAPrunedRowIsWrittenAgainUnlessALivePeerDoesNotShareTheKeys(t *testing.T) {
	t.Run("no live peer disagrees", func(t *testing.T) {
		database := storetest.Open(t)
		fake := clocktest.New(time.Now())
		registration := mustRegisterOn(t, database, fake, "c1", "K1 K0")
		underRegistryLock(t, database, func(tx pgx.Tx) {
			prunedRow(t, tx, registration.ID)
			livePeer(t, tx, "K0 K1")
		})
		fake.AwaitArmed(t, "the heartbeat", 1)
		since := testdb.Now(t, database.Pool)
		fake.Advance(HeartbeatInterval)
		waitForStamp(t, database, registration.ID, since, "the heartbeat to write a pruned row back")
		requireNotLost(t, registration, "a process whose row was pruned")
		// The row written again is read through Live, held live for the read (holdLive), so a
		// row that is not there fails the read rather than passing over it.
		holdLive(t, database, registration.ID)
		if p := liveProcess(t, database, registration.ID); p.Commit != "c1" || p.DashboardBuild != "build-c1" ||
			!slices.Equal(p.SigningKeys, fingerprints(t, "K1 K0")) {
			t.Errorf("the row written again = %+v, want commit c1, dashboard build-c1 and the fingerprints of K1 then K0", p)
		}
	})
	t.Run("a live peer does not share the keys", func(t *testing.T) {
		database := storetest.Open(t)
		fake := clocktest.New(time.Now())
		registration := mustRegisterOn(t, database, fake, "c1", "K1 K0")
		var peer string
		underRegistryLock(t, database, func(tx pgx.Tx) {
			prunedRow(t, tx, registration.ID)
			peer = livePeer(t, tx, "K9")
		})
		fake.AwaitArmed(t, "the heartbeat", 1)
		fake.Advance(HeartbeatInterval)
		requireRefusal(t, database, registration, peer, "a process whose row was pruned, beside a peer with a key of its own")
		if rowExists(t, database, registration.ID) {
			t.Error("the refused process's row was written again")
		}
	})
}

// A live row's heartbeat stamps it and checks nothing: the keys were checked when the process
// registered, and every process registered since checked its own against them. A peer that
// disagrees, written past Register's check, refuses nothing while this process's row stays live.
//
// The row is held live before each beat (holdLive): a beat checks nothing only while the row it
// stamps is live, and a row the machine let lapse between two beats would be checked again
// (renew), which is another row's subject. Each beat's stamp is then told from the held heartbeat
// by its value: at or after a moment of the database's clock read before the beat, and before
// the held heartbeat, which is an hour ahead.
func TestALiveRowsHeartbeatChecksNoPeer(t *testing.T) {
	database := storetest.Open(t)
	fake := clocktest.New(time.Now())
	registration := mustRegisterOn(t, database, fake, "c1", "K1 K0")
	underRegistryLock(t, database, func(tx pgx.Tx) { livePeer(t, tx, "K9") })

	// Three heartbeats stamped after the peer was written, each one the test's own move of the
	// registration's clock.
	for beat := 1; beat <= 3; beat++ {
		holdLive(t, database, registration.ID)
		held, _ := heartbeatOf(t, database, registration.ID)
		fake.AwaitArmed(t, "the heartbeat", 1)
		since := testdb.Now(t, database.Pool)
		fake.Advance(HeartbeatInterval)
		testwait.Until(t, fmt.Sprintf("heartbeat %d to stamp the row", beat), func() (string, bool) {
			at, exists := heartbeatOf(t, database, registration.ID)
			if !exists {
				return "no row for " + registration.ID, false
			}
			reading := fmt.Sprintf("a heartbeat of %s, held at %s, the beat brought after %s",
				at.Format(time.RFC3339Nano), held.Format(time.RFC3339Nano), since.Format(time.RFC3339Nano))
			return reading, !at.Before(since) && at.Before(held)
		})
	}
	requireNotLost(t, registration, "a live process beside a disagreeing peer")
}

// A schema refresh and a heartbeat that has to rejoin, running at once, both finish. The two
// want the same two things: the registry pool's one connection and the registration's lock.
// renew takes the connection for its transaction and then the lock to read the instance it
// rejoins with, so a refresh that held the lock while it waited for the connection would wait
// on the heartbeat that waits on it, and neither would move until the refresh's own context ran
// out - with the heartbeat sitting on the registry's advisory lock all the while, so every
// other process misses a beat.
//
// The interleaving is arranged, not raced for: the refresh is held inside the function that
// reads the numbers, which it calls under the lock, and the heartbeat is started and given the
// pool's connection before the refresh is let go.
func TestARefreshAndALapsedHeartbeatAtOnceBothFinish(t *testing.T) {
	database := storetest.Open(t)
	// The registration's heartbeat runs on a clock nothing moves, so no beat of its own runs
	// before its goroutine is stopped below.
	registration := mustRegisterOn(t, database, clocktest.New(time.Now()), "c1", "K0")
	// The heartbeat's own goroutine is stopped, so the two runs below are the only callers of
	// the registry's pool and the test drives renew itself. Deregister stops it again, which
	// costs nothing, and closes the pool.
	registration.stop()
	testwait.Receive(t, "the registration's heartbeat goroutine to stop", registration.stopped)
	// A row still live is stamped and nothing else is read: the heartbeat reads the instance
	// only where its row dropped out of Live and it has to rejoin (renew).
	ageHeartbeat(t, database.Pool, registration.ID, LiveWindow+time.Second)

	// Each call runs within the shared wait's bound (testwait.Context), so the pair that waits on
	// each other ends rather than hanging the test, and each wait below names the run that did not
	// return.
	ctx := testwait.Context(t)
	reading, release := make(chan struct{}), make(chan struct{})
	refreshed := make(chan error, 1)
	go func() {
		refreshed <- registration.RefreshSchema(ctx, func() (int, int) {
			close(reading)
			<-release
			return 7, 7
		})
	}()
	testwait.Receive(t, "the refresh to reach its read of the numbers, under the registration's lock", reading)
	renewed := make(chan error, 1)
	go func() {
		refusal, err := registration.renew(ctx)
		renewed <- errors.Join(refusal, err)
	}()
	// What the heartbeat holds before it asks for the other: the pool's one connection, which
	// it takes for its transaction. Where the refresh already holds it, this reads so at once,
	// which is the order that cannot deadlock.
	waitForConnectionHeld(t, registration)
	close(release)

	for _, run := range []struct {
		name   string
		result <-chan error
	}{{"the refresh", refreshed}, {"the heartbeat", renewed}} {
		// A refresh and a heartbeat that take the registry's connection and the registration's
		// lock in two orders wait on each other for good, and this wait then gives up naming the
		// run that did not finish.
		if err := testwait.Receive(t, run.name+" to finish beside the other once both were let go", run.result); err != nil {
			t.Fatalf("%s run beside the other: %v", run.name, err)
		}
	}
	// The numbers are read through Live, the row held live for the read (holdLive): whether the
	// rejoined row is still live is not this row's subject.
	holdLive(t, database, registration.ID)
	if got := liveProcess(t, database, registration.ID); got.SchemaVersion != 7 || got.SchemaCreatable != 7 {
		t.Errorf("the row after both ran holds schema version %d and creatable %d, want 7 and 7", got.SchemaVersion, got.SchemaCreatable)
	}
}

// waitForConnectionHeld waits until the registry's pool has its one connection out, which is
// what renew holds from the moment its transaction begins.
func waitForConnectionHeld(t *testing.T, registration *Registration) {
	t.Helper()
	testwait.Until(t, "the registry's one connection to be taken by either run", func() (int32, bool) {
		acquired := registration.pool.Stat().AcquiredConns()
		return acquired, acquired > 0
	})
}

// A process registers at this build's owner lock protocol (store.OwnerLockProtocol), which no
// Instance carries, so neither a registration that names nothing but its build and keys nor the
// heartbeat that writes its pruned row again is an earlier build's arrival: the row reads 1, no
// earlier build's mark is stamped (owner_lock_legacy_mark), and the owner lock's fleet read
// (public.owner_lock_fleet) finds no earlier build in the fleet. A row at the column's default, 0,
// would fire owner_lock_legacy_registration and put every owner lock of the database beside an
// earlier build for store.OwnerLockMarkWindow (migration 20261115200014).
func TestARegistrationOfThisBuildMarksNoEarlierBuild(t *testing.T) {
	database := storetest.Open(t)
	// The registration's heartbeat runs on this clock, so the beat that writes the pruned row
	// back comes where the test moves it.
	fake := clocktest.New(time.Now())
	registration, err := RegisterOn(context.Background(), fake, database.Pool, Instance{Commit: "c1", SigningKeys: fingerprints(t, "K0")})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { _ = registration.Deregister(context.Background()) })
	noEarlierBuild := func(what string) {
		t.Helper()
		var protocol int
		var marked, legacy bool
		if err := database.Pool.QueryRow(context.Background(), `
			select (select owner_lock_protocol from server_processes where instance = $1),
				exists (select from owner_lock_legacy_mark),
				public.owner_lock_fleet($2, $3)`,
			registration.ID, store.OwnerLockLegacyWindow.Seconds(), store.OwnerLockMarkWindow.Seconds(),
		).Scan(&protocol, &marked, &legacy); err != nil {
			t.Fatalf("read %s's owner lock protocol and the owner lock's fleet: %v", what, err)
		}
		if protocol != store.OwnerLockProtocol || marked || legacy {
			t.Errorf("%s: owner_lock_protocol %d, an earlier build's mark stamped %v, an earlier build in the owner lock's fleet %v; want %d, no mark and no earlier build",
				what, protocol, marked, legacy, store.OwnerLockProtocol)
		}
	}
	noEarlierBuild("the registered row")

	underRegistryLock(t, database, func(tx pgx.Tx) { prunedRow(t, tx, registration.ID) })
	fake.AwaitArmed(t, "the heartbeat", 1)
	since := testdb.Now(t, database.Pool)
	fake.Advance(HeartbeatInterval)
	waitForStamp(t, database, registration.ID, since, "the heartbeat to write a pruned row back")
	requireNotLost(t, registration, "a process whose row was pruned")
	noEarlierBuild("the row written again after a prune")
}

// The owner lock counts a row of an earlier build in the fleet for store.OwnerLockLegacyWindow
// after its last heartbeat, which its documents state as until the row is pruned: the two are one
// duration, declared in two packages only because the store cannot import this one.
func TestTheOwnerLockLegacyWindowIsThePruneAge(t *testing.T) {
	if store.OwnerLockLegacyWindow != PruneAge {
		t.Errorf("store.OwnerLockLegacyWindow is %s and PruneAge %s; want them equal, so a row of an earlier build counts in the owner lock's fleet until it is pruned",
			store.OwnerLockLegacyWindow, PruneAge)
	}
}
