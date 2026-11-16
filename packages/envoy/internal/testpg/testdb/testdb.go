// Package testdb is the half of internal/testpg that needs no container runtime, and is, with
// internal/testpg and the two storetest packages it serves, the only code of Envoy's tests that
// reads a variable naming a Postgres server (Server). A test never gets the database that
// variable's URL names, so two test binaries sharing one server see none of each other's rows.
//
// Server.Database is an empty database on the server a variable names, which the caller migrates
// itself, skipping the test when the variable is unset. DatabaseOn is the helper for a test that
// already holds the URL of the server it runs on: it reads no variable and starts nothing.
// WithParameter sets a query parameter on any of these URLs. Template is a migrated database a
// binary makes once and copies for each test; Dispatch's and the broker's storetest packages give
// out its clones as migrated stores, and a test of either store that wants one calls its storetest
// instead.
//
// It imports no testcontainers package, so a test helper every Postgres-backed test links (the
// broker's storetest among them) does not pay testcontainers' package init, which walks every
// process on the machine before any test runs. internal/testpg keeps the container path and calls
// this.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
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
func DatabaseOn(t testing.TB, adminURL, prefix string) string {
	t.Helper()
	server, err := url.Parse(adminURL)
	if err != nil {
		// url.Parse's own error quotes the URL it was given, so only the error it wraps is printed.
		t.Fatalf("parse the admin URL of the Postgres server: %v", errors.Unwrap(err))
	}
	suffix, err := randomSuffix()
	if err != nil {
		t.Fatalf("%v", err)
	}
	name := prefix + "_" + suffix
	// A copy of template1's files: created the default way, through the write-ahead log, an empty
	// database costs 4.4 MB of log (docs/testing.md, "A database a Go test creates").
	adminExec(t, adminURL, server.Host, "create database "+name+" strategy file_copy")
	// `with (force)` ends every backend attached to the database, which the drop needs because a
	// database a connection still holds cannot be dropped. That covers a connection the test left
	// open, so one leaked pool cannot keep a database on a shared server for the rest of the run.
	t.Cleanup(func() { adminExec(t, adminURL, server.Host, "drop database "+name+" with (force)") })
	return databaseURL(*server, name)
}

// databaseURL is server's URL with its path replaced by database, its query kept.
func databaseURL(server url.URL, database string) string {
	server.Path = "/" + database
	return server.String()
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

// WithParameter returns databaseURL with its query parameter key set to value. A test that has a
// connection ask for a setting adds it here rather than appending "&key=value", which names a
// parameter only when the URL already carries a query: DatabaseOn keeps the server URL's query, so
// on a URL that has none the suffix runs on into the database's name. A failure prints only what
// url.Parse wraps, since the URL carries a credential (DatabaseOn).
func WithParameter(t testing.TB, databaseURL, key, value string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse the URL of the test database: %v", errors.Unwrap(err))
	}
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// randomSuffix is eight random bytes in hex, the part of a database's name that keeps it apart
// from every other test's on a shared server.
func randomSuffix() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("random database name: %w", err)
	}
	return hex.EncodeToString(suffix[:]), nil
}
