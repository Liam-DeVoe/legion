package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/internal/testwait"
)

// replacedSessionsLine is the line endReplacedSessions logs once it has ended the previous
// holder's sessions.
const replacedSessionsLine = "dispatch outbox: replaced the publisher hold's lapsed row and ended its holder's sessions"

// tookHoldLine is the line publish logs as this process becomes the publisher.
const tookHoldLine = "dispatch outbox: took the publisher hold"

// A standby that takes over a publisher hold whose row lapsed ends every database session of the
// process the row named, before its first batch, and logs each: a transaction that process held
// open is rolled back, so the lock it held is free, and its next statement answers SQLSTATE
// 57P01. Sessions of any other process are left alone, one whose name holds the stopped process's
// identity with one character more among them, and so are the standby's own. A row that lapsed
// just now leaves the take most of lapseToServe, and the sessions end before the standby becomes
// the publisher; a row that lapsed longer ago than that leaves the take nothing of the six
// seconds, and the sessions end once the standby publishes, still before its first scan.
func TestAStandbyThatReplacesALapsedHoldEndsThePreviousHoldersSessionsAlone(t *testing.T) {
	for _, row := range []struct {
		name         string
		lapsedAgo    time.Duration
		endedFirst   bool
		deferredLine bool
	}{
		{"a row that lapsed just now", 0, true, false},
		{"a row that lapsed longer ago than the take-over leaves", 2 * lapseToServe, false, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			var logged lockedBuffer
			logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
			ctx := context.Background()
			stopped := storetest.Open(t)
			identity := stopped.Pool.Identity()
			if _, err := stopped.Pool.Exec(ctx, `insert into duty_holds (name, holder, token, expires_at) values ($1, $2, 'stopped', now() - make_interval(secs => $3))`,
				publisherHold, identity, row.lapsedAgo.Seconds()); err != nil {
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
				Store: standbyStore, Publisher: &recordingPublisher{},
				Broker: events.NewBroker(), Listen: standbyStore.Pool.Listener,
			})
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { standby.Run(runCtx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			waitFor(t, outboxHangGuard, "the standby ending the lapsed holder's sessions", func() bool {
				return strings.Contains(logged.String(), replacedSessionsLine)
			})

			var one int
			if err := tx.QueryRow(ctx, `select 1`).Scan(&one); !store.SessionEnded(err) {
				t.Errorf("the stopped process's transaction answered %v after the take, want SQLSTATE 57P01", err)
			}
			// The backend gives its locks up as it exits, a moment after pg_terminate_backend
			// signalled it, so the lock is tried until it is free, for as long as the test binary's
			// deadline leaves less the cleanup's stop of the standby, whose release of the hold
			// recordTimeout bounds.
			testwait.UntilDeadline(t, recordTimeout+time.Second, 50*time.Millisecond,
				fmt.Sprintf("backend %d, whose session the standby ended, to give up its advisory lock %d", holderPID, key), func() bool {
					var free bool
					if err := standbyStore.Pool.QueryRow(ctx, `select pg_try_advisory_xact_lock($1)`, key).Scan(&free); err != nil {
						t.Fatalf("try the stopped process's lock: %v", err)
					}
					return free
				})
			text := logged.String()
			if !strings.Contains(text, fmt.Sprintf("previous=%s pid=%d ", identity, holderPID)) {
				t.Errorf("the standby logged no end of the stopped process's transaction (backend %d):\n%s", holderPID, text)
			}
			if endedFirst := strings.Index(text, replacedSessionsLine) < strings.Index(text, tookHoldLine); endedFirst != row.endedFirst {
				t.Errorf("the sessions were ended before the standby became the publisher: %t, want %t:\n%s", endedFirst, row.endedFirst, text)
			}
			if deferred := strings.Contains(text, "were not ended within what the take-over's six seconds left"); deferred != row.deferredLine {
				t.Errorf("the standby logged that the take left no time for the ending: %t, want %t:\n%s", deferred, row.deferredLine, text)
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
		})
	}
}

// heldEnding holds every statement that ends sessions (store.Pool.EndSessionsOf) for hold, or
// until its context ends, whichever comes first, as a statement queued behind a slow database
// would, and records when each began and how it ended.
type heldEnding struct {
	hold time.Duration
	mu   sync.Mutex
	held []heldStatement
}

type heldStatement struct {
	began, ended time.Time
	cut          bool
}

func (h *heldEnding) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(data.SQL, "pg_terminate_backend") {
		return ctx
	}
	began := time.Now()
	cut := false
	select {
	case <-ctx.Done():
		cut = true
	case <-time.After(h.hold):
	}
	h.mu.Lock()
	h.held = append(h.held, heldStatement{began: began, ended: time.Now(), cut: cut})
	h.mu.Unlock()
	return ctx
}

func (h *heldEnding) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (h *heldEnding) statements() []heldStatement {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]heldStatement(nil), h.held...)
}

// A standby whose ending of the replaced holder's sessions does not answer spends on it no more
// than what the take-over's six seconds left after the row's lapse (lapseToServe), and becomes
// the publisher while that ending is still held; the sessions are then ended once it publishes,
// before its first scan. The ending is held 3 s, longer than lapseToServe and shorter than
// recordTimeout: the first, bounded try is cut where the bound falls, the hold's took line is
// logged before the held try would have answered, and the second try, given recordTimeout,
// waits the hold out and ends the sessions.
func TestAStandbyWhoseEndingOfTheOldSessionsIsSlowPublishesWithinWhatTheTakeOverLeft(t *testing.T) {
	var logged lockedBuffer
	logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
	ctx := context.Background()
	stopped := storetest.Open(t)
	identity := stopped.Pool.Identity()
	if _, err := stopped.Pool.Exec(ctx, `insert into duty_holds (name, holder, token, expires_at) values ($1, $2, 'stopped', now())`,
		publisherHold, identity); err != nil {
		t.Fatalf("leave the stopped process's lapsed row: %v", err)
	}
	slow := &heldEnding{hold: 3 * time.Second}
	standbyStore := tracedStore(t, storetest.SecondProcess(t, stopped), slow)
	standby := New(Deps{
		Store: standbyStore, Publisher: &recordingPublisher{},
		Broker: events.NewBroker(), Listen: standbyStore.Pool.Listener,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { standby.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, outboxHangGuard, "the standby ending the lapsed holder's sessions", func() bool {
		return strings.Contains(logged.String(), replacedSessionsLine)
	})
	hold := standby.hold.Load()
	if hold == nil {
		t.Fatal("the standby holds no publisher hold")
	}
	deadline := hold.ReplacedLapse().Add(lapseToServe)
	held := slow.statements()
	text := logged.String()
	if len(held) != 2 || !held[0].cut || held[1].cut {
		t.Fatalf("the standby ended the sessions in %+v, want a first try cut at its bound and a second that waited the hold out:\n%s", held, text)
	}
	t.Logf("the ending statements: %+v; the bound fell %v after the first began", held, deadline.Sub(held[0].began).Round(time.Millisecond))
	if held[0].ended.Before(deadline) {
		t.Errorf("the first try was cut %v before its bound, ReplacedLapse + lapseToServe", deadline.Sub(held[0].ended).Round(time.Millisecond))
	}
	if !(strings.Index(text, tookHoldLine) < strings.Index(text, replacedSessionsLine)) {
		t.Errorf("the standby became the publisher after the slow ending answered, want before:\n%s", text)
	}
	if !held[1].began.After(held[0].ended) {
		t.Errorf("the second try began %v, before the first was cut at %v", held[1].began, held[0].ended)
	}
}
