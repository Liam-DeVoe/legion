package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sjawhar/envoy/internal/testwait"
)

// An application_name is the base, one space and the identity, with the base cut on a rune
// boundary so the whole fits the 63 bytes Postgres keeps and the identity is whole at its end.
func TestAnApplicationNameEndsWithTheWholeIdentity(t *testing.T) {
	identity := uuid.New()
	for _, row := range []struct{ name, base, want string }{
		{"a short base is kept", "dispatch-rooms", "dispatch-rooms " + identity.String()},
		{"a long base is cut to 26 bytes", strings.Repeat("b", 40), strings.Repeat("b", 26) + " " + identity.String()},
		{"a base cut inside a rune loses the rune", strings.Repeat("b", 25) + "é" + "tail", strings.Repeat("b", 25) + " " + identity.String()},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := applicationName(row.base, identity)
			if got != row.want || len(got) > maxApplicationName || !utf8.ValidString(got) {
				t.Fatalf("applicationName(%q) = %q (%d bytes), want %q", row.base, got, len(got), row.want)
			}
		})
	}
	if got := applicationName("dispatch-rooms", uuid.Nil); got != "dispatch-rooms" {
		t.Fatalf("applicationName with no identity = %q, want the base alone", got)
	}
}

// Every connection a store opens names its identity at the end of its application_name: the
// shared pool's, under the connection string's name or "dispatch", and each separate pool's,
// under its own name or the shared pool's. A connection string's own name is kept before it.
func TestEveryConnectionOfAStoreNamesItsIdentity(t *testing.T) {
	ctx := context.Background()
	for _, row := range []struct{ name, url, base string }{
		{"no name in the connection string", testDatabaseURL(t), defaultApplicationName},
		{"a name in the connection string", withParam(testDatabaseURL(t), "application_name", "dispatch-previous-build"), "dispatch-previous-build"},
	} {
		t.Run(row.name, func(t *testing.T) {
			database, err := Open(ctx, row.url)
			if err != nil {
				t.Fatalf("open the store: %v", err)
			}
			t.Cleanup(database.Pool.Close)
			identity := database.Pool.Identity()
			if identity == uuid.Nil {
				t.Fatal("the store has no identity")
			}
			suffix := " " + identity.String()
			nameOf := func(q interface {
				QueryRow(context.Context, string, ...any) pgx.Row
			}) string {
				t.Helper()
				var name string
				if err := q.QueryRow(ctx, `select current_setting('application_name')`).Scan(&name); err != nil {
					t.Fatalf("read the application_name: %v", err)
				}
				return name
			}
			rooms, err := database.Pool.Rooms()
			if err != nil {
				t.Fatal(err)
			}
			appends, err := database.Pool.RoomAppends()
			if err != nil {
				t.Fatal(err)
			}
			compaction, err := database.Pool.Compaction()
			if err != nil {
				t.Fatal(err)
			}
			outbox, err := database.Pool.Outbox()
			if err != nil {
				t.Fatal(err)
			}
			textTrees, err := database.Pool.TextTrees()
			if err != nil {
				t.Fatal(err)
			}
			horizon, err := database.Pool.Horizon()
			if err != nil {
				t.Fatal(err)
			}
			listener, err := database.Pool.Listener(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close(ctx)
			dedicated, err := database.Pool.Dedicated(ctx, "dispatch-listener "+identity.String())
			if err != nil {
				t.Fatal(err)
			}
			defer dedicated.Close(ctx)
			for _, named := range []struct{ what, got, want string }{
				{"shared", nameOf(database.Pool), row.base + suffix},
				{"rooms", nameOf(rooms), roomsApplicationName + suffix},
				{"room appends", nameOf(appends), roomAppendsApplicationName + suffix},
				{"compaction", nameOf(compaction), compactionApplicationName + suffix},
				{"outbox", nameOf(outbox), row.base + suffix},
				{"display tree fill", nameOf(textTrees), row.base + suffix},
				{"event horizon", nameOf(horizon), row.base + suffix},
				{"listener", nameOf(listener), row.base + suffix},
				{"dedicated", nameOf(dedicated), "dispatch-listener" + suffix},
			} {
				if named.got != named.want {
					t.Errorf("the %s pool's connection is named %q, want %q", named.what, named.got, named.want)
				}
			}
			if outbox.Identity() != identity || textTrees.Identity() != identity {
				t.Errorf("the outbox and display tree pools name %s and %s, want the store's %s", outbox.Identity(), textTrees.Identity(), identity)
			}
			if again, err := Open(ctx, row.url); err != nil {
				t.Fatal(err)
			} else {
				defer again.Pool.Close()
				if again.Pool.Identity() == identity {
					t.Errorf("a second Open minted the identity %s again", identity)
				}
			}
		})
	}
}

// withParam is url with one more query parameter.
func withParam(url, key, value string) string {
	if strings.Contains(url, "?") {
		return url + "&" + key + "=" + value
	}
	return url + "?" + key + "=" + value
}

// EndSessionsOf ends every session of one identity and nothing else: a store's own sessions,
// a transaction among them holding an advisory lock, which it frees, and a session of no store
// that names the identity; never a session of another store, nor two that differ from the
// identity's name only at its end - one with a character more, one with a character less. The
// ended transaction's next statement answers SQLSTATE 57P01, which SessionEnded reads.
func TestEndSessionsOfEndsTheSessionsOfOneIdentityAlone(t *testing.T) {
	ctx := context.Background()
	ending := openTestStore(t)
	ended := openTestStore(t)
	identity := ended.Pool.Identity().String()

	tx, err := ended.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the ended store's transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	const key = int64(91827364)
	var holderPID int32
	if err := tx.QueryRow(ctx, `select pg_backend_pid() from (select pg_advisory_xact_lock($1)) locked`, key).Scan(&holderPID); err != nil {
		t.Fatalf("take the lock in the ended store's transaction: %v", err)
	}
	planted := map[string]*pgx.Conn{}
	for _, name := range []string{
		"psql " + identity,                // a session of no store naming the identity: ended
		"dispatch " + identity + "0",      // one character more: kept
		"dispatch " + identity[:35],       // one character less: kept
		"dispatch " + uuid.NewString(),    // another identity: kept
		"dispatch-planted" + identity[1:], // no space before a 35-byte tail of it: kept
	} {
		config, err := pgx.ParseConfig(testDatabaseURL(t))
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

	if _, err := ending.Pool.EndSessionsOf(ctx, uuid.Nil); err == nil {
		t.Fatal("EndSessionsOf the nil identity ended something, want a refusal")
	}
	sessions, err := ending.Pool.EndSessionsOf(ctx, ended.Pool.Identity())
	if err != nil {
		t.Fatalf("end the sessions of %s: %v", identity, err)
	}
	names := map[string]bool{}
	pids := map[int32]bool{}
	for _, session := range sessions {
		if !strings.HasSuffix(session.ApplicationName, " "+identity) {
			t.Errorf("EndSessionsOf(%s) ended %d, named %q", identity, session.PID, session.ApplicationName)
		}
		names[session.ApplicationName], pids[session.PID] = true, true
	}
	if !pids[holderPID] {
		t.Errorf("EndSessionsOf(%s) did not end the transaction's backend %d: it ended %+v", identity, holderPID, sessions)
	}
	if !names["psql "+identity] {
		t.Errorf("EndSessionsOf(%s) did not end the session of no store that names it: it ended %+v", identity, sessions)
	}

	// pg_terminate_backend answers once the backend is signalled, and the backend gives its locks
	// up as it exits, so the lock is tried until it is free, for as long as the test binary's
	// deadline leaves less a second for the pools' close. Each try is a transaction-level lock,
	// given back as its own statement ends.
	testwait.UntilDeadline(t, time.Second, 50*time.Millisecond,
		fmt.Sprintf("backend %d, whose session EndSessionsOf ended, to give up its advisory lock %d", holderPID, key), func() bool {
			var taken bool
			if err := ending.Pool.QueryRow(ctx, `select pg_try_advisory_xact_lock($1)`, key).Scan(&taken); err != nil {
				t.Fatalf("try the ended transaction's lock: %v", err)
			}
			return taken
		})
	var one int
	err = tx.QueryRow(ctx, `select 1`).Scan(&one)
	if !SessionEnded(err) {
		t.Errorf("the ended transaction's next statement answered %v, want SQLSTATE 57P01", err)
	}
	for name, conn := range planted {
		if name == "psql "+identity {
			continue
		}
		if err := conn.QueryRow(ctx, `select 1`).Scan(&one); err != nil {
			t.Errorf("the session %q no longer answers: %v", name, err)
		}
	}
	// A pool whose connection was ended dials a fresh one for its next caller.
	if err := ended.Pool.QueryRow(ctx, `select 1`).Scan(&one); err != nil && !SessionEnded(err) {
		t.Errorf("the ended store's next read answered %v", err)
	}
	if SessionEnded(errors.New("57P01")) || SessionEnded(nil) {
		t.Error("SessionEnded reads an error that is no Postgres error as an ended session")
	}
}

// A store whose sessions were all ended while idle in its pools fails at most the first statement
// handed one of them: that statement finds its session ended (SessionEnded), and the store's pools
// then drop every connection they hold, so each later statement runs on a connection dialled
// afresh. The four connections are ended while the test holds them, released at once and used
// within the second after which pgxpool pings an idle connection before it hands it out, so
// without the drop each of them would fail a statement of its own.
func TestAStoreWhoseSessionsWereEndedFailsOneStatementAndDialsAfresh(t *testing.T) {
	ctx := context.Background()
	ended := openTestStore(t)
	ending := openTestStore(t)
	const held = 4
	conns := make([]*pgxpool.Conn, 0, held)
	pids := make([]int32, 0, held)
	for range held {
		conn, err := ended.Pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire a connection: %v", err)
		}
		var pid int32
		if err := conn.QueryRow(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatalf("read the connection's backend: %v", err)
		}
		conns, pids = append(conns, conn), append(pids, pid)
	}
	sessions, err := ending.Pool.EndSessionsOf(ctx, ended.Pool.Identity())
	if err != nil || len(sessions) < held {
		t.Fatalf("end the store's sessions: %d ended, %v; want at least %d", len(sessions), err, held)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var live int
		if err := ending.Pool.QueryRow(ctx, `select count(*) from pg_stat_activity where pid = any($1)`, pids).Scan(&live); err != nil {
			t.Fatalf("read the ended backends: %v", err)
		}
		if live == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of the store's backends %v still live 10 s after they were ended", live, pids)
		}
	}
	for _, conn := range conns {
		conn.Release()
	}
	released := time.Now()
	var failed []error
	for range held + 1 {
		var one int
		if err := ended.Pool.QueryRow(ctx, `select 1`).Scan(&one); err != nil {
			failed = append(failed, err)
		}
	}
	t.Logf("%d statements after the release, %v after it: %d failed", held+1, time.Since(released).Round(time.Millisecond), len(failed))
	if len(failed) > 1 || (len(failed) == 1 && !SessionEnded(failed[0])) {
		t.Fatalf("after its sessions were ended, the store failed %d of %d statements (%v); want at most the first, with its session ended", len(failed), held+1, failed)
	}
}
