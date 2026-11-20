package outbox

import (
	"context"
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
	"github.com/sjawhar/envoy/testdeadline"
)

// statementStall is how long a test holds a statement of the publisher hold's holder back once
// it waits behind another session's lock: longer than the five seconds the outbox's records, its
// claim and its release each once gave themselves, so a bound of that size ends the statement
// while the lock stands.
const statementStall = 7 * time.Second

// The fragments of the statements the tests below hold back, as pg_stat_activity shows each.
const (
	batchRecordStatement    = "update events e set"
	fallbackClaimStatement  = "update comment_fallback_copies set next_attempt_at = now() + $2"
	fallbackRecordStatement = "update comment_fallback_copies c set"
	holdReleaseStatement    = "delete from duty_holds where name = $1 and holder = $2"
)

// lockedFor holds a lock from a transaction of its own on database until release or the test's
// end: lock is the statement that takes it.
func lockedFor(t *testing.T, ctx context.Context, database *store.Store, lock string, args ...any) (release func()) {
	t.Helper()
	tx, err := database.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the transaction holding the lock: %v", err)
	}
	if _, err := tx.Exec(ctx, lock, args...); err != nil {
		t.Fatalf("%s: %v", lock, err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := tx.Rollback(context.WithoutCancel(ctx)); err != nil {
				t.Errorf("release the lock: %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// outlastStatementStall waits until a statement holding fragment waits on a lock, holds it there
// for statementStall more, and then releases the lock. A call that returns first, as done closing
// tells, fails the test at once, by name: its statement could not answer while the lock stood, so
// a bound of its own ended it. The stall is counted from the moment the statement is seen
// waiting, so the statement has waited at least that long however late it began.
func outlastStatementStall(t *testing.T, ctx context.Context, database *store.Store, fragment, what string, done <-chan struct{}, release func()) {
	t.Helper()
	began := time.Now()
	var blocked time.Time
	for {
		select {
		case <-done:
			t.Fatalf("%s returned %s after it began, while another session's lock its statement needs still stood: a bound of its own ended it",
				what, time.Since(began).Round(time.Millisecond))
		default:
		}
		if blocked.IsZero() {
			var waiting int
			if err := database.Pool.QueryRow(ctx, `
				select count(*) from pg_stat_activity
				where datname = current_database() and wait_event_type = 'Lock' and strpos(query, $1) > 0
			`, fragment).Scan(&waiting); err != nil {
				t.Fatalf("read whether %s waits on the lock: %v", what, err)
			}
			if waiting > 0 {
				blocked = time.Now()
				t.Logf("%s waits behind the lock %s after it began", what, blocked.Sub(began).Round(time.Millisecond))
			}
		} else if time.Since(blocked) >= statementStall {
			release()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// lostAt is a publisher hold that reports itself lost once lose is called, its loss deadline then
// at.
type lostAt struct {
	at   time.Time
	lost chan struct{}
}

func (h *lostAt) Deadline() time.Time     { return h.at }
func (h *lostAt) Lost() <-chan struct{}   { return h.lost }
func (h *lostAt) lose(deadline time.Time) { h.at = deadline; close(h.lost) }

// A statement only the publisher hold's holder sends (untilTheHoldLapses) is given ctx's values
// and not its end, waits while the hold stands, and ends when the hold's row lapses at the
// earliest once the hold is lost: its loss deadline plus Margin, and not before.
func TestAHoldersStatementEndsWhenTheHoldsRowMayHaveLapsed(t *testing.T) {
	type key struct{}
	parent, stop := context.WithCancel(context.WithValue(context.Background(), key{}, "the caller's"))
	hold := &lostAt{at: time.Now().Add(time.Hour), lost: make(chan struct{})}
	ctx, cancel := untilTheHoldLapses(parent, hold)
	defer cancel()
	if ctx.Value(key{}) != "the caller's" {
		t.Fatal("the statement's context lost its caller's values")
	}
	if _, has := ctx.Deadline(); has {
		t.Fatal("the statement's context has a deadline of its own while the hold stands")
	}
	stop()
	time.Sleep(100 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatalf("the caller's end ended the statement's context (%v), which a stopping process still writes on", ctx.Err())
	}

	// A hold that reports itself lost has pulled its deadline back to the loss, or had passed it.
	deadline := time.Now().Add(-time.Second)
	hold.lose(deadline)
	lapse := deadline.Add(publisherHoldOptions.Margin)
	select {
	case <-ctx.Done():
		if ended := time.Now(); ended.Before(lapse) {
			t.Fatalf("the statement's context ended %s before the hold's row may have lapsed (loss deadline plus Margin)", lapse.Sub(ended))
		}
	case <-time.After(time.Until(lapse) + time.Minute):
		t.Fatalf("the statement's context was still live a minute after the hold's row may have lapsed")
	}
}

// A batch's record (recordBatch) waits behind another session's lock on the event it settles for
// as long as the publisher hold stands, past the five seconds it once had, and marks the event
// published once the lock goes.
func TestABatchsRecordWaitsBehindALockWhileTheHoldStands(t *testing.T) {
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	database := storetest.Open(t)
	broker := events.NewBroker()
	seedIssue(t, database, "T-1", nil)
	event := appendEvent(t, database, broker, model.Event{
		IssueKey: new("T-1"), Type: "message.created", Actor: model.Actor{Kind: "user", ID: "alice"},
		Payload: model.MessageEventPayload{Message: model.Message{
			ID: "8d7e2a4c-1f3b-4e5a-9c6d-0b1a2c3d4e5f", IssueKey: new("T-1"), Body: "a message",
		}},
	})
	deps := Deps{Store: database, Publisher: &recordingPublisher{}, Broker: broker}
	holder := holding(t, deps).instance
	release := lockedFor(t, ctx, database, `select 1 from events where id = $1 for update`, event.ID)

	done := make(chan struct{})
	var result batchResult
	var err error
	go func() {
		defer close(done)
		result, err = scanBatch(context.Background(), deps, unfenced{}, holder)
	}()
	outlastStatementStall(t, ctx, database, batchRecordStatement, "the batch's record", done, release)
	<-done
	if err != nil {
		t.Fatalf("the batch, its record held back behind the lock and then let go: %v", err)
	}
	if result.published != 1 || publishedAt(t, database, event.ID) == nil {
		t.Fatalf("the batch counted %d events published and the event's published_at is %v, want the event published",
			result.published, publishedAt(t, database, event.ID))
	}
}

// A fallback pass's claim (claimFallbackCopies) and its record (recordFallbackCopies) each wait
// behind another session's lock on the fallback rows for as long as the publisher hold stands,
// past the five seconds each once had, and the pass publishes the held copy and settles it once
// the lock goes.
func TestAFallbackPassWaitsBehindALockWhileTheHoldStands(t *testing.T) {
	const sessionTopic = "notifications.agent.s1"
	for _, tc := range []struct {
		name      string
		statement string
		what      string
		// lock takes the lock: before the pass, or as the pass sends the copy, after its claim.
		lockFirst bool
	}{
		{name: "the claim", statement: fallbackClaimStatement, what: "the fallback pass's claim", lockFirst: true},
		{name: "the record", statement: fallbackRecordStatement, what: "the fallback pass's record"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := testdeadline.Answered(t, context.Background())
			defer cancel()
			passCtx := store.WithTransactionTracking(context.Background())
			database := storetest.Open(t)
			publisher := &takeoverPublisher{topic: sessionTopic}
			deps := Deps{Store: database, Publisher: publisher, Broker: events.NewBroker()}
			commentID := seedHeldFallbackCopy(passCtx, t, deps)
			holder := holdingProcess(t, database)
			var release func()
			if tc.lockFirst {
				release = lockedFor(t, ctx, database, `lock table comment_fallback_copies in exclusive mode`)
				publisher.takeOver = func() {}
			} else {
				// The pass sends the copy only after its claim has committed, so the row lock
				// taken here holds back the record alone. It runs on the pass's goroutine, so it
				// keeps what it took for the test's goroutine rather than failing the test there.
				var mu sync.Mutex
				var held pgx.Tx
				var lockErr error
				publisher.takeOver = func() {
					mu.Lock()
					defer mu.Unlock()
					held, lockErr = database.Pool.Begin(ctx)
					if lockErr == nil {
						_, lockErr = held.Exec(ctx, `select 1 from comment_fallback_copies where comment_id = $1 and destination = $2 for update`, commentID, sessionTopic)
					}
				}
				release = func() {
					mu.Lock()
					defer mu.Unlock()
					if lockErr != nil {
						t.Fatalf("lock the held copy's row as the pass sent it: %v", lockErr)
					}
					if held != nil {
						_ = held.Rollback(context.WithoutCancel(ctx))
					}
				}
				t.Cleanup(func() {
					mu.Lock()
					defer mu.Unlock()
					if held != nil {
						_ = held.Rollback(context.WithoutCancel(ctx))
					}
				})
			}

			done := make(chan struct{})
			var failed bool
			go func() {
				defer close(done)
				_, failed = publishFallbackCopies(passCtx, deps, unfenced{}, holder)
			}()
			outlastStatementStall(t, ctx, database, tc.statement, tc.what, done, release)
			<-done
			if failed {
				t.Fatal("the pass reports its claim failed once the lock went")
			}
			if row := readHeldCopy(t, database, commentID, sessionTopic); !row.settled || !row.published {
				t.Fatalf("the held copy is settled=%v published=%v, want it published and settled once the lock went", row.settled, row.published)
			}
		})
	}
}

// The publisher hold's release (releaseHold) waits behind another session's lock on its row until
// the row lapses on its own, past the five seconds it once had, and deletes the row once the lock
// goes, so a standby takes the name at once rather than at the lapse.
func TestTheHoldsReleaseWaitsBehindALockUntilItsRowLapses(t *testing.T) {
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	database := storetest.Open(t)
	// A hold of the test's own whose row lives a minute past each renewal: the release's bound is
	// that lapse, and a renewal the lock holds back leaves the loss deadline where it was.
	hold, err := duty.TryHold(ctx, database.Pool, publisherHold, uuid.New(),
		duty.HoldOptions{TTL: time.Minute, Renew: time.Second, Margin: publisherHoldOptions.Margin})
	if err != nil || hold == nil {
		t.Fatalf("take the publisher hold: %v, %v", hold, err)
	}
	release := lockedFor(t, ctx, database, `lock table duty_holds in exclusive mode`)

	done := make(chan struct{})
	var released error
	go func() {
		defer close(done)
		released = releaseHold(context.Background(), hold)
	}()
	outlastStatementStall(t, ctx, database, holdReleaseStatement, "the hold's release", done, release)
	<-done
	if released != nil {
		t.Fatalf("the release, held back behind the lock and then let go: %v", released)
	}
	var rows int
	if err := database.Pool.QueryRow(ctx, `select count(*) from duty_holds where name = $1`, publisherHold).Scan(&rows); err != nil {
		t.Fatalf("read the hold's row: %v", err)
	}
	if rows != 0 {
		t.Fatal("the hold's row is still there after its release, want it deleted")
	}
}
