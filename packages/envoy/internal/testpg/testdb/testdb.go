// Package testdb is the half of internal/testpg that needs no container runtime: DatabaseOn makes
// an empty Postgres database of a test's own on a server the test already holds. It imports no
// testcontainers package, so a test helper every Postgres-backed test links (the broker's
// storetest among them) does not pay testcontainers' package init, which walks every process on
// the machine before any test runs. internal/testpg keeps the container path and calls this.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/testwait"
)

// DatabaseOn creates a database of this test's own on the server adminURL names, drops it when the
// test ends, and returns adminURL with its path replaced by that database and its query kept. The
// name is prefix, an underscore and eight random bytes in hex: it is random because every test of
// every package sharing one server takes a name there.
//
// Both statements run on adminURL itself, which is exact for every caller because a caller passes
// the URL its own statements run on, and that URL and the result differ only in the path.
//
// A failure names the server's host and the database, never adminURL: a DSN carries a credential,
// and a test here prints none (docs/solutions/testing/a-proof-script-never-prints-a-dsn-a-userinfo-scrubber-leaks-the-query-and-keyword-password-forms-INFRA-229.md).
// This function reads no environment variable and starts no container, so its failures can name
// neither.
// Every connect to adminURL here, and to the URL it returns, waits until the test binary's deadline
// less testwait.Margin (WithConnectDeadline), whatever connect_timeout adminURL names.
func DatabaseOn(t testing.TB, adminURL, prefix string) string {
	t.Helper()
	adminURL = WithConnectDeadline(t, adminURL)
	server, err := url.Parse(adminURL)
	if err != nil {
		// url.Parse's own error quotes the URL it was given, so only the error it wraps is printed.
		t.Fatalf("parse the admin URL of the Postgres server: %v", errors.Unwrap(err))
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	name := prefix + "_" + hex.EncodeToString(suffix[:])
	// A copy of template1's files: created the default way, through the write-ahead log, an empty
	// database costs 4.4 MB of log (docs/testing.md, "A database a Go test creates").
	adminExec(t, adminURL, server.Host, "create database "+name+" strategy file_copy")
	// `with (force)` ends every backend attached to the database, which the drop needs because a
	// database a connection still holds cannot be dropped. That covers a connection the test left
	// open, so one leaked pool cannot keep a database on a shared server for the rest of the run.
	// `if exists`, because a cleanup the test registered later runs first and may have dropped
	// the database already: the cleanup of a role the test created there drops the database before
	// the role, which nothing in the database then holds (internal/dispatch/store's
	// dropRoleAfterTestDatabase).
	t.Cleanup(func() { adminExec(t, adminURL, server.Host, "drop database if exists "+name+" with (force)") })
	return databaseURL(*server, name)
}

// databaseURL is server's URL with its path replaced by database, its query kept.
func databaseURL(server url.URL, database string) string {
	server.Path = "/" + database
	return server.String()
}

// WithConnectDeadline returns databaseURL with connect_timeout set to the whole seconds left until
// t's deadline, the test binary's own (go test's -timeout), less testwait.Margin, rounded down and
// at least 1, in place of any connect_timeout the URL names. pgx bounds the whole of a new
// connection (the dial, the authentication and the server's startup answer) by connect_timeout,
// through pgx.Connect and every pool; a URL that names none gets two minutes. On a loaded machine
// Postgres can take longer than that to answer a new connection, and a test then failed on its
// connect, whatever it checked (rows-md:101: TestAMachineExchangeOfAnEndedCredentialIsRefused
// failed after 120.68 s). With the binary's deadline less the margin as the bound, a slow server
// only slows a test, and one that never answers fails it on pgx's own error with the margin left
// for the failure and the test's cleanups, as a testwait wait gives up, before go test's alarm
// would end the binary with neither. A shorter connect_timeout in the URL would make the test
// depend on load again, so it is replaced. Where t names no deadline (-timeout 0, or a testing.TB
// with no Deadline method) it is the longest connect_timeout pgx holds. A failure prints only what
// url.Parse wraps, since the URL carries a credential.
func WithConnectDeadline(t testing.TB, databaseURL string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse the URL of the test database: %v", errors.Unwrap(err))
	}
	query := parsed.Query()
	query.Set("connect_timeout", connectTimeout(t))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// connectTimeout is the connect_timeout WithConnectDeadline sets for t, in whole seconds: the time
// left until t's deadline less testwait.Margin, rounded down, and 1 where that is under a second
// (pgx reads 0 as no bound at all).
func connectTimeout(t testing.TB) string {
	unbounded := strconv.FormatInt(math.MaxInt64/int64(time.Second), 10)
	named, ok := t.(interface{ Deadline() (time.Time, bool) })
	if !ok {
		return unbounded
	}
	deadline, bounded := named.Deadline()
	if !bounded {
		return unbounded
	}
	return strconv.FormatInt(max(1, int64((time.Until(deadline)-testwait.Margin)/time.Second)), 10)
}

// adminExec runs one statement over a connection of its own to adminURL: neither create database
// nor drop database may run inside a transaction or against the database it names. host is that
// server's host, printed in place of adminURL, which carries a credential.
func adminExec(t testing.TB, adminURL, host, statement string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to the Postgres server at %s: %v", host, err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}
