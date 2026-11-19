package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
)

// A standby that takes over a publisher hold whose row lapsed ends every database session of the
// process the row named, before its first batch, and logs each: a transaction that process held
// open is rolled back, so the lock it held is free, and its next statement answers SQLSTATE
// 57P01. Sessions of any other process are left alone, one whose name holds the stopped process's
// identity with one character more among them, and so are the standby's own.
func TestAStandbyThatReplacesALapsedHoldEndsThePreviousHoldersSessionsAlone(t *testing.T) {
	var logged lockedBuffer
	logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
	ctx := context.Background()
	stopped := storetest.Open(t)
	identity := stopped.Pool.Identity()
	if _, err := stopped.Pool.Exec(ctx, `insert into duty_holds (name, holder, token, expires_at) values ($1, $2, 'stopped', now() - interval '1 second')`,
		publisherHold, identity); err != nil {
		t.Fatalf("leave the stopped process's lapsed row: %v", err)
	}
	tx, err := stopped.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const key = int64(73548291)
	var holderPID int32
	if err := tx.QueryRow(ctx, `select pg_backend_pid() from (select pg_advisory_xact_lock($1)) locked`, key).Scan(&holderPID); err != nil {
		t.Fatalf("take a lock in the stopped process's transaction: %v", err)
	}
	planted := map[string]*pgx.Conn{}
	for _, name := range []string{"dispatch " + uuid.NewString(), "dispatch " + identity.String() + "0"} {
		config, err := pgx.ParseConfig(stopped.Pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		config.RuntimeParams["application_name"] = name
		conn, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatalf("open the session %q: %v", name, err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		planted[name] = conn
	}

	standbyStore := storetest.SecondProcess(t, stopped)
	standby := New(Deps{
		Store: standbyStore, Instance: standbyStore.Pool.Identity(), Publisher: &recordingPublisher{},
		Broker: events.NewBroker(), Listen: standbyStore.Pool.Listener,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { standby.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, outboxHangGuard, "the standby taking the lapsed publisher hold", standby.Publishing)

	var one int
	if err := tx.QueryRow(ctx, `select 1`).Scan(&one); !store.SessionEnded(err) {
		t.Errorf("the stopped process's transaction answered %v after the take, want SQLSTATE 57P01", err)
	}
	// The backend gives its locks up as it exits, a moment after pg_terminate_backend signalled it.
	waitFor(t, 10*time.Second, "the stopped process's lock free after the take", func() bool {
		var free bool
		if err := standbyStore.Pool.QueryRow(ctx, `select pg_try_advisory_xact_lock($1)`, key).Scan(&free); err != nil {
			t.Fatalf("try the stopped process's lock: %v", err)
		}
		return free
	})
	if !strings.Contains(logged.String(), fmt.Sprintf("previous=%s pid=%d ", identity, holderPID)) {
		t.Errorf("the standby logged no end of the stopped process's transaction (backend %d):\n%s", holderPID, logged.String())
	}
	for name, conn := range planted {
		if err := conn.QueryRow(ctx, `select 1`).Scan(&one); err != nil {
			t.Errorf("the session %q of another process no longer answers: %v", name, err)
		}
	}
	if err := standbyStore.Pool.QueryRow(ctx, `select 1`).Scan(&one); err != nil {
		t.Errorf("the standby's own pool no longer answers: %v", err)
	}
	if !standby.Publishing() {
		t.Error("the standby stopped publishing")
	}
}
