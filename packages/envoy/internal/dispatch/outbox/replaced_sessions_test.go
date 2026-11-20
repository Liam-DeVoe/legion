package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/dispatch/duty"
	"github.com/sjawhar/envoy/internal/dispatch/events"
	"github.com/sjawhar/envoy/internal/dispatch/model"
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

// lateTakeOverLine is the line tryHold logs when it takes the hold over without having ended the
// previous holder's sessions by the take-over's budget: the late take-over, by name.
const lateTakeOverLine = "dispatch outbox: late take-over of the publisher hold: the previous holder's sessions were not ended by the take-over's budget"

// sentBatchLine is the line scan logs for each batch that sent anything.
const sentBatchLine = "dispatch outbox: sent batch"

// The causes tryHold names on the late take-over's line.
const (
	lateCauseTakeAfterBudget = "the take came after the budget"
	lateCauseEndingTimedOut  = "the ending of its sessions did not answer by the budget"
)

// A standby that takes over a publisher hold whose row lapsed ends every database session of the
// process the row named and logs each: a transaction that process held open is rolled back, so
// the lock it held is free, and its next statement answers SQLSTATE 57P01. Sessions of any other
// process are left alone, one whose name holds the stopped process's identity with one character
// more among them, and so are the standby's own. A row that lapses once the standby is up leaves
// the take all of lapseToServe, and the sessions end before the standby becomes the publisher. A
// row that lapsed longer ago than that leaves the take nothing of the six seconds: the standby
// logs its take-over late, with that cause, becomes the publisher at once, and ends the sessions
// beside its first batch, so after its took line.
func TestAStandbyThatReplacesALapsedHoldEndsThePreviousHoldersSessionsAlone(t *testing.T) {
	for _, row := range []struct {
		name string
		// lapsedAgo is how long before the standby starts the row lapsed, or zero for a row that
		// lapses once the standby listens (lapseOnceListening).
		lapsedAgo  time.Duration
		endedFirst bool
		lateCause  string
	}{
		{"a row that lapsed just now", 0, true, ""},
		{"a row that lapsed longer ago than the take-over leaves", 2 * lapseToServe, false, lateCauseTakeAfterBudget},
	} {
		t.Run(row.name, func(t *testing.T) {
			var logged lockedBuffer
			logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
			ctx := context.Background()
			stopped := storetest.Open(t)
			identity := stopped.Pool.Identity()
			if row.lapsedAgo > 0 {
				if _, err := stopped.Pool.Exec(ctx, `insert into duty_holds (name, holder, token, expires_at) values ($1, $2, 'stopped', now() - make_interval(secs => $3))`,
					publisherHold, identity, row.lapsedAgo.Seconds()); err != nil {
					t.Fatalf("leave the stopped process's lapsed row: %v", err)
				}
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
			lapse := func() {}
			if row.lapsedAgo == 0 {
				lapse = lapseOnceListening(t, stopped, standby)
			}
			runCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { standby.Run(runCtx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })
			lapse()
			waitFor(t, outboxHangGuard, "the standby ending the lapsed holder's sessions and becoming the publisher", func() bool {
				text := logged.String()
				_, replacedAt := logLine(text, replacedSessionsLine)
				_, tookAt := logLine(text, tookHoldLine)
				return replacedAt >= 0 && tookAt >= 0
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
			_, replacedAt := logLine(text, replacedSessionsLine)
			_, tookAt := logLine(text, tookHoldLine)
			if endedFirst := replacedAt < tookAt; endedFirst != row.endedFirst {
				t.Errorf("the sessions were ended before the standby became the publisher: %t, want %t:\n%s", endedFirst, row.endedFirst, text)
			}
			late, _ := logLine(text, lateTakeOverLine)
			if cause := logAttribute(late, "cause"); cause != row.lateCause {
				t.Errorf("the standby logged its take-over late with the cause %q, want %q:\n%s", cause, row.lateCause, text)
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

// lapseOnceListening leaves a row of the publisher hold for the stopped process that lasts an
// hour, and returns the call that lapses it once standby's LISTEN connection is up and wakes the
// standby as a release would (duty.HoldReleasedChannel). The standby then takes the row at its
// lapse, so what the take leaves of lapseToServe does not depend on how long the standby took to
// start, which a loaded runner can stretch past lapseToServe. It must be called before the
// standby's Run starts, and the call it returns after.
func lapseOnceListening(t *testing.T, stopped *store.Store, standby *Outbox) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := stopped.Pool.Exec(ctx, `insert into duty_holds (name, holder, token, expires_at) values ($1, $2, 'stopped', now() + interval '1 hour')`,
		publisherHold, stopped.Pool.Identity()); err != nil {
		t.Fatalf("leave the stopped process's row: %v", err)
	}
	listening := make(chan struct{})
	var once sync.Once
	standby.afterListening = func() { once.Do(func() { close(listening) }) }
	return func() {
		t.Helper()
		select {
		case <-listening:
		case <-time.After(outboxHangGuard):
			t.Fatalf("the standby's LISTEN connection did not come up within %v", outboxHangGuard)
		}
		if _, err := stopped.Pool.Exec(ctx, `update duty_holds set expires_at = now() where name = $1`, publisherHold); err != nil {
			t.Fatalf("lapse the stopped process's row: %v", err)
		}
		if _, err := stopped.Pool.Exec(ctx, `select pg_notify($1, $2)`, duty.HoldReleasedChannel, publisherHold); err != nil {
			t.Fatalf("wake the standby: %v", err)
		}
	}
}

// heldEnding holds every statement that ends sessions (store.Pool.EndSessionsOf) for hold, or
// until its context ends, whichever comes first, as a statement queued behind a slow database
// would, and records when each began, the deadline its context carried, and how it ended.
type heldEnding struct {
	hold time.Duration
	mu   sync.Mutex
	held []heldStatement
}

// heldStatement is one statement heldEnding held. cut is its context's error when the context
// ended it, and nil when it waited the hold out.
type heldStatement struct {
	began, ended, deadline time.Time
	cut                    error
}

func (s heldStatement) String() string {
	deadline := "none"
	if !s.deadline.IsZero() {
		deadline = s.deadline.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("{began %s, ended %s, deadline %s, cut %v}",
		s.began.Format(time.RFC3339Nano), s.ended.Format(time.RFC3339Nano), deadline, s.cut)
}

func (h *heldEnding) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(data.SQL, "pg_terminate_backend") {
		return ctx
	}
	deadline, _ := ctx.Deadline()
	began := time.Now()
	var cut error
	select {
	case <-ctx.Done():
		cut = ctx.Err()
	case <-time.After(h.hold):
	}
	h.mu.Lock()
	h.held = append(h.held, heldStatement{began: began, ended: time.Now(), deadline: deadline, cut: cut})
	h.mu.Unlock()
	return ctx
}

func (h *heldEnding) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (h *heldEnding) statements() []heldStatement {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]heldStatement(nil), h.held...)
}

// A standby whose ending of the replaced holder's sessions does not answer gives it the
// take-over's budget and no more - lapseToServe after the row's lapse, what the six seconds
// leave - and takes over at the budget anyway: it logs the late take-over by name, with its cause,
// then stores the hold, logs that it took it, and starts its first batch at once. The sessions it
// could not end are ended beside that batch, which their ending holds none of. The ending is held
// 3 s, longer than lapseToServe and shorter than recordTimeout, and one event waits to be
// published.
//
// The first, bounded try's statement carries the budget itself as its context's deadline, read
// in the statement, so the check of that bound needs no margin: a bound a nanosecond past the
// budget fails it. The try must end by that deadline. The took line's start and the first batch's
// start must follow the try's end, and the batch's must come before the second try, given
// recordTimeout, waits its 3 s out and ends the sessions.
func TestAStandbyWhoseEndingOfTheOldSessionsIsSlowPublishesWithinWhatTheTakeOverLeft(t *testing.T) {
	var logged lockedBuffer
	logtest.SetDefault(t, slog.NewTextHandler(&logged, nil))
	ctx := context.Background()
	stopped := storetest.Open(t)
	seedIssue(t, stopped, "T-1", nil)
	appendEvent(t, stopped, events.NewBroker(), model.Event{
		IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: model.Message{ID: "message-waiting", IssueKey: new("T-1"), Body: "waiting for the new publisher"},
	})
	slow := &heldEnding{hold: 3 * time.Second}
	standbyStore := tracedStore(t, storetest.SecondProcess(t, stopped), slow)
	standby := New(Deps{
		Store: standbyStore, Publisher: &recordingPublisher{},
		Broker: events.NewBroker(), Listen: standbyStore.Pool.Listener,
	})
	lapse := lapseOnceListening(t, stopped, standby)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { standby.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	lapse()
	waitFor(t, outboxHangGuard, "the standby ending the lapsed holder's sessions and sending its first batch", func() bool {
		text := logged.String()
		return strings.Contains(text, replacedSessionsLine) && strings.Contains(text, sentBatchLine)
	})
	hold := standby.hold.Load()
	if hold == nil {
		t.Fatal("the standby holds no publisher hold")
	}
	budget := hold.ReplacedLapse().Add(lapseToServe)
	held := slow.statements()
	text := logged.String()
	if len(held) != 2 {
		t.Fatalf("the standby ended the sessions in %d statements (%+v), want a first try cut at the budget and a second that waited the hold out:\n%s", len(held), held, text)
	}
	first, second := held[0], held[1]
	t.Logf("the ending statements: %+v; the budget fell %v after the first began", held, budget.Sub(first.began).Round(time.Millisecond))
	if !first.deadline.Equal(budget) {
		t.Errorf("the first try was given until %s, %v from the take-over's budget %s (ReplacedLapse + lapseToServe), want the budget itself",
			first.deadline.Format(time.RFC3339Nano), first.deadline.Sub(budget), budget.Format(time.RFC3339Nano))
	}
	if !errors.Is(first.cut, context.DeadlineExceeded) {
		t.Errorf("the first try ended with %v, want cut by its deadline", first.cut)
	}
	if second.cut != nil {
		t.Errorf("the second try was cut (%v), want it to wait the 3 s hold out", second.cut)
	}

	late, lateAt := logLine(text, lateTakeOverLine)
	took, tookAt := logLine(text, tookHoldLine)
	if lateAt < 0 || tookAt < 0 || lateAt > tookAt {
		t.Errorf("the late take-over's line came at %d and the took line at %d, want the late line logged, before the took line:\n%s", lateAt, tookAt, text)
	}
	if cause := logAttribute(late, "cause"); cause != lateCauseEndingTimedOut {
		t.Errorf("the late take-over's cause is %q, want %q:\n%s", cause, lateCauseEndingTimedOut, text)
	}
	started := logMoment(t, took, "started")
	if !started.After(first.ended) {
		t.Errorf("the standby began to publish at %s, %v before its bounded ending ended at %s",
			started.Format(time.RFC3339Nano), first.ended.Sub(started), first.ended.Format(time.RFC3339Nano))
	}
	batch, _ := logLine(text, sentBatchLine)
	batchStarted := logMoment(t, batch, "started")
	t.Logf("the first try ended %v past the budget; the standby began to publish %v after it, and its first batch started %v after that",
		first.ended.Sub(budget).Round(time.Millisecond), started.Sub(first.ended).Round(time.Millisecond), batchStarted.Sub(started).Round(time.Millisecond))
	if !batchStarted.Before(second.ended) {
		t.Errorf("the first batch started at %s, %v after the took line and not before the ending beside it answered at %s: the ending held the batch back",
			batchStarted.Format(time.RFC3339Nano), batchStarted.Sub(started).Round(time.Millisecond), second.ended.Format(time.RFC3339Nano))
	}
}

// logLine returns the first line of text, a slog text handler's output, whose msg is message, and
// its index among the lines, or "" and -1 when there is none.
func logLine(text, message string) (string, int) {
	quoted := "msg=" + strconv.Quote(message)
	index := 0
	for line := range strings.Lines(text) {
		if strings.Contains(line, quoted+" ") || strings.HasSuffix(strings.TrimSuffix(line, "\n"), quoted) {
			return strings.TrimSuffix(line, "\n"), index
		}
		index++
	}
	return "", -1
}

// logAttributeValue is one key=value of a slog text line, its value quoted or bare.
var logAttributeValue = regexp.MustCompile(`(\w+)=("(?:[^"\\]|\\.)*"|\S*)`)

// logAttribute is the value of key on line, unquoted, or "" when the line has none.
func logAttribute(line, key string) string {
	for _, match := range logAttributeValue.FindAllStringSubmatch(line, -1) {
		if match[1] != key {
			continue
		}
		if unquoted, err := strconv.Unquote(match[2]); err == nil {
			return unquoted
		}
		return match[2]
	}
	return ""
}

// logMoment is the moment key names on line, which must carry it.
func logMoment(t *testing.T, line, key string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, logAttribute(line, key))
	if err != nil {
		t.Fatalf("read the %s of the line %q: %v", key, line, err)
	}
	return at
}
