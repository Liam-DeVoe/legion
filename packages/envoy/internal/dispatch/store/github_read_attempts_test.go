package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/envoy/internal/dispatch/githubreads"
	"github.com/sjawhar/envoy/internal/logtest"
	"github.com/sjawhar/envoy/testdeadline"
)

// The record keeps one row for each read, which every attempt overwrites with its own values: a
// failed attempt's cause, then a succeeded attempt's none, at a later time.
func TestGitHubReadAttemptsKeepOneRowPerReadWithTheNewestValues(t *testing.T) {
	ctx := context.Background()
	attempts := NewGitHubReadAttempts(migratedEmptyStore(t).Pool)
	if rows, err := attempts.All(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("an empty record reads %v, %v; want no row", rows, err)
	}

	cause := "GET https://api.github.com/app/hook/deliveries: status 503: unavailable"
	if err := attempts.Record(ctx, githubreads.WebhookRedelivery, false, cause); err != nil {
		t.Fatalf("record the failed attempt: %v", err)
	}
	if err := attempts.Record(ctx, githubreads.CIReconcile, true, ""); err != nil {
		t.Fatalf("record the CI reconcile's attempt: %v", err)
	}
	failed := readAttempts(t, attempts)
	if got := failed[githubreads.WebhookRedelivery]; got.Succeeded || got.Cause != cause || got.At.IsZero() {
		t.Fatalf("the failed attempt reads %+v, want failed with its cause and a time", got)
	}

	if err := attempts.Record(ctx, githubreads.WebhookRedelivery, true, "a cause a succeeded attempt does not keep"); err != nil {
		t.Fatalf("record the succeeded attempt: %v", err)
	}
	newest := readAttempts(t, attempts)
	if len(newest) != 2 {
		t.Fatalf("the record holds %d rows (%v), want one for each of the two reads", len(newest), newest)
	}
	got := newest[githubreads.WebhookRedelivery]
	if !got.Succeeded || got.Cause != "" || got.At.Before(failed[githubreads.WebhookRedelivery].At) {
		t.Fatalf("the newest attempt reads %+v, want succeeded with no cause, no earlier than %s", got, failed[githubreads.WebhookRedelivery].At)
	}
	if other := newest[githubreads.CIReconcile]; !other.Succeeded || other.Cause != "" {
		t.Fatalf("the other read's row reads %+v, want it as it was recorded", other)
	}
}

// readAttempts reads the record keyed by read, failing on a read that holds two rows.
func readAttempts(t *testing.T, attempts *GitHubReadAttempts) map[githubreads.Read]githubreads.Recorded {
	t.Helper()
	rows, err := attempts.All(context.Background())
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	keyed := map[githubreads.Read]githubreads.Recorded{}
	for _, row := range rows {
		if _, twice := keyed[row.Read]; twice {
			t.Fatalf("the record holds two rows for %s: %v", row.Read, rows)
		}
		keyed[row.Read] = row
	}
	return keyed
}

// The table holds a row to what GET /healthz may answer: one of the four reads, a cause of at most
// 1,024 bytes, a cause exactly when the attempt failed.
func TestTheGitHubReadAttemptsTableRefusesARowHealthCannotAnswer(t *testing.T) {
	ctx := context.Background()
	database := migratedEmptyStore(t)
	cases := []struct {
		name       string
		read       string
		succeeded  bool
		cause      *string
		constraint string
	}{{
		name:       "a cause over 1,024 bytes",
		read:       string(githubreads.DeliveryReconcile),
		cause:      new(strings.Repeat("a", githubreads.CauseLimit+1)),
		constraint: "github_read_attempts_cause_fits",
	}, {
		// U+FFFD is three bytes, so 342 of them are 1,026 bytes in 342 characters.
		name:       "a cause over 1,024 bytes in fewer characters",
		read:       string(githubreads.DeliveryReconcile),
		cause:      new(strings.Repeat("\uFFFD", 342)),
		constraint: "github_read_attempts_cause_fits",
	}, {
		name:       "a succeeded attempt with a cause",
		read:       string(githubreads.ArchitectureImport),
		succeeded:  true,
		cause:      new("GET https://api.github.com/repos/acme/web: status 502: bad gateway"),
		constraint: "github_read_attempts_cause_when_failed",
	}, {
		name:       "a failed attempt without a cause",
		read:       string(githubreads.ArchitectureImport),
		constraint: "github_read_attempts_cause_when_failed",
	}, {
		name:       "a read that is none of the four",
		read:       "intake",
		succeeded:  true,
		constraint: "github_read_attempts_read_check",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := database.Pool.Exec(ctx, `insert into github_read_attempts (read, at, succeeded, cause) values ($1, now(), $2, $3)`,
				tc.read, tc.succeeded, tc.cause)
			var refusal *pgconn.PgError
			if !errors.As(err, &refusal) || refusal.Code != "23514" || refusal.ConstraintName != tc.constraint {
				t.Fatalf("insert error = %v, want SQLSTATE 23514 naming %s", err, tc.constraint)
			}
		})
	}
	// The cause at the limit is kept.
	if err := NewGitHubReadAttempts(database.Pool).Record(ctx, githubreads.DeliveryReconcile, false, strings.Repeat("a", githubreads.CauseLimit)); err != nil {
		t.Fatalf("record a cause of exactly %d bytes: %v", githubreads.CauseLimit, err)
	}
}

// Each write of a row notifies githubreads.Channel with the read's name, the insert and the
// update alike, so every process refreshes its copy whichever process recorded the attempt. The
// listen, the writes and the waits for each notification are given what the test's deadline leaves
// (testdeadline.Answered): a notification that never comes fails the test naming the write.
func TestRecordingAGitHubReadAttemptNotifiesItsChannel(t *testing.T) {
	ctx, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	database := migratedEmptyStore(t)
	listener := connectForTest(t, ctx, database)
	if _, err := listener.Exec(ctx, "listen "+githubreads.Channel); err != nil {
		t.Fatalf("listen on %s: %v", githubreads.Channel, err)
	}
	attempts := NewGitHubReadAttempts(database.Pool)
	for _, write := range []struct {
		name      string
		read      githubreads.Read
		succeeded bool
		cause     string
	}{
		{"the first attempt of a read", githubreads.CIReconcile, false, "GraphQL: status 502"},
		{"the next attempt of the same read", githubreads.CIReconcile, true, ""},
		{"the first attempt of another read", githubreads.ArchitectureImport, true, ""},
	} {
		if err := attempts.Record(ctx, write.read, write.succeeded, write.cause); err != nil {
			t.Fatalf("%s: record: %v", write.name, err)
		}
		notification, err := listener.WaitForNotification(ctx)
		if err != nil || notification.Channel != githubreads.Channel || notification.Payload != string(write.read) {
			t.Fatalf("%s: notification = %+v, %v; want %s with payload %s", write.name, notification, err, githubreads.Channel, write.read)
		}
	}
}

// gitHubReadAttemptsStall is how long a stall holds back a statement of GitHubReadAttempts once it
// waits: longer than the five seconds each statement, and each of its callers, once gave itself,
// so a bound of that size ends the statement while the stall stands.
const gitHubReadAttemptsStall = 7 * time.Second

// gitHubReadAttemptsLoopPeriod is the period of three of the four loops that record an attempt
// (architecture.SyncInterval, delivery.ReconcileInterval, cireconcile.Interval), which
// githubreads.Finish gives its write.
const gitHubReadAttemptsLoopPeriod = 5 * time.Minute

// waitingOnGitHubReadAttempts counts this database's lock requests on github_read_attempts that
// wait, not granted: the statements a stall holds back.
const waitingOnGitHubReadAttempts = `select count(*) from pg_locks
	where database = (select oid from pg_database where datname = current_database())
	  and relation = 'github_read_attempts'::regclass and not granted`

// stalledCall is what one call behind a stall returned, and how long after the scene began.
type stalledCall struct {
	err  error
	took time.Duration
}

// stallGitHubReadAttempts holds github_read_attempts in ACCESS EXCLUSIVE mode from a session of
// its own, which every statement of GitHubReadAttempts waits behind, until release or the test's
// end: a database that answers, but late.
func stallGitHubReadAttempts(t *testing.T, ctx context.Context, database *Store) (release func()) {
	t.Helper()
	holder := connectForTest(t, ctx, database)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the stall's transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, "lock table github_read_attempts in access exclusive mode"); err != nil {
		t.Fatalf("lock github_read_attempts for the stall: %v", err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			end, cancel := testdeadline.Answered(t, context.Background())
			defer cancel()
			if err := tx.Rollback(end); err != nil {
				t.Errorf("end the stall: %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// outlastStall waits until every call of calls waits behind the stall, holds them there for
// gitHubReadAttemptsStall more, and releases the stall. A call that returns first fails the test
// at once, by name: no statement can answer while the stall stands, so a call that returned was
// ended by a bound of its own. The stall is counted from the moment every call is seen waiting,
// so each call has waited at least that long since it began, however late it began.
func outlastStall(t *testing.T, ctx context.Context, database *Store, start time.Time, release func(), calls map[string]chan stalledCall) {
	t.Helper()
	var blocked time.Time
	for {
		for name, done := range calls {
			if len(done) > 0 {
				returned := <-done
				t.Fatalf("%s returned %v %s after it began, while another session's lock its statement needs still stood: a bound of its own ended it", name, returned.err, returned.took.Round(time.Millisecond))
			}
		}
		if blocked.IsZero() {
			var waiting int
			if err := database.Pool.QueryRow(ctx, waitingOnGitHubReadAttempts).Scan(&waiting); err != nil {
				t.Fatalf("count the statements waiting behind the stall: %v", err)
			}
			if waiting >= len(calls) {
				blocked = time.Now()
				t.Logf("%d statements wait behind the stall %s after the scene began", waiting, blocked.Sub(start).Round(time.Millisecond))
			}
		} else if time.Since(blocked) >= gitHubReadAttemptsStall {
			release()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// loggedError is the first ERROR line logs holds, as an error with its message and its error
// attribute, or nil.
func loggedError(logs *logtest.Recorder) error {
	for _, record := range logs.Records() {
		if record.Level != slog.LevelError {
			continue
		}
		var cause string
		record.Attrs(func(attribute slog.Attr) bool {
			if attribute.Key == "error" {
				cause = attribute.Value.String()
			}
			return true
		})
		return fmt.Errorf("%s: %s", record.Message, cause)
	}
	return nil
}

// Record and All take their caller's context and add no bound of their own. Behind another
// session's lock that the statement needs, each waits past the five seconds it once gave itself
// and answers once the lock goes; a caller whose deadline passes first ends each at that
// deadline, and not before.
func TestGitHubReadAttemptsWaitAsLongAsTheirCallerAllows(t *testing.T) {
	setup, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	database := migratedEmptyStore(t)
	attempts := NewGitHubReadAttempts(database.Pool)
	scene := func(ctx context.Context, start time.Time) map[string]chan stalledCall {
		recorded, read := make(chan stalledCall, 1), make(chan stalledCall, 1)
		go func() {
			err := attempts.Record(ctx, githubreads.CIReconcile, true, "")
			recorded <- stalledCall{err, time.Since(start)}
		}()
		go func() {
			_, err := attempts.All(ctx)
			read <- stalledCall{err, time.Since(start)}
		}()
		return map[string]chan stalledCall{"Record": recorded, "All": read}
	}

	t.Run("a stall shorter than the caller's deadline", func(t *testing.T) {
		release := stallGitHubReadAttempts(t, setup, database)
		start := time.Now()
		calls := scene(setup, start)
		outlastStall(t, setup, database, start, release, calls)
		for name, done := range calls {
			if returned := <-done; returned.err != nil {
				t.Errorf("%s, which waited behind the stall, failed %s after it began, once the stall was over: %v", name, returned.took.Round(time.Millisecond), returned.err)
			}
		}
	})

	t.Run("a caller's deadline shorter than the stall", func(t *testing.T) {
		stallGitHubReadAttempts(t, setup, database)
		// The caller's own deadline is this scene's subject, so it is a literal: longer than the
		// five seconds the calls once gave themselves, and shorter than a stall that lasts until
		// they return.
		const callerDeadline = 9 * time.Second
		start := time.Now()
		ctx, cancelCall := context.WithTimeout(setup, callerDeadline)
		defer cancelCall()
		for name, done := range scene(ctx, start) {
			returned := <-done
			if !errors.Is(returned.err, context.DeadlineExceeded) && !pgconn.Timeout(returned.err) {
				t.Errorf("%s behind a stall that outlasts its caller's deadline returned %v, want the caller's deadline exceeded", name, returned.err)
			}
			if returned.took < callerDeadline {
				t.Errorf("%s ended %s after it began, before its caller's deadline of %s: a bound of its own ended it", name, returned.took.Round(time.Millisecond), callerDeadline)
			}
		}
	})
}

// The record's two callers give a database that answers late what they guard leaves them, not a
// bound of their own: githubreads.Finish's write of a pass's outcome waits behind another
// session's lock within one period of its loop and records the attempt once the lock goes, and
// the copy's load (githubreads.Copy.Run) waits as long as Run's context lives and then holds the
// record.
func TestTheRecordsCallersWaitForADatabaseThatAnswersLate(t *testing.T) {
	setup, cancel := testdeadline.Answered(t, context.Background())
	defer cancel()
	database := migratedEmptyStore(t)
	attempts := NewGitHubReadAttempts(database.Pool)
	release := stallGitHubReadAttempts(t, setup, database)
	start := time.Now()

	finishLogs := &logtest.Recorder{}
	finished := make(chan stalledCall, 1)
	go func() {
		pass, attempt := githubreads.Begin(context.Background())
		githubreads.NoteSent(pass)
		githubreads.Finish(context.Background(), githubreads.WebhookRedelivery, attempt, attempts.Record, gitHubReadAttemptsLoopPeriod, slog.New(finishLogs))
		finished <- stalledCall{loggedError(finishLogs), time.Since(start)}
	}()

	var copied githubreads.Copy
	running, stop := context.WithCancel(context.Background())
	loaded := make(chan stalledCall, 1)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		copied.Run(running, func(ctx context.Context) ([]githubreads.Recorded, error) {
			rows, err := attempts.All(ctx)
			loaded <- stalledCall{err, time.Since(start)}
			return rows, err
		}, nil, slog.New(&logtest.Recorder{}))
	}()
	defer func() {
		stop()
		<-ran
	}()

	calls := map[string]chan stalledCall{"githubreads.Finish's write": finished, "the copy's load": loaded}
	outlastStall(t, setup, database, start, release, calls)
	for name, done := range calls {
		if returned := <-done; returned.err != nil {
			t.Fatalf("%s, which waited behind the stall, failed %s after it began, once the stall was over: %v", name, returned.took.Round(time.Millisecond), returned.err)
		}
	}
	if _, ok := copied.Rows(); !ok {
		t.Fatal("the copy holds nothing after its load answered")
	}
	if got := readAttempts(t, attempts)[githubreads.WebhookRedelivery]; !got.Succeeded || got.At.IsZero() {
		t.Fatalf("the record of the pass Finish wrote behind the stall reads %+v, want it succeeded", got)
	}
}
