package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testTemplate is a template with no migration: a copy of template1, which is all a test of how a
// clone is made needs.
var testTemplate = NewTemplate(DispatchServer, "testdb", "func TestMain(m *testing.M) { os.Exit(testTemplate.Main(m.Run)) }",
	func(context.Context, string) error { return nil })

func TestMain(m *testing.M) { os.Exit(testTemplate.Main(m.Run)) }

// Clone copies the template by copying its files (Template says why). The test reads the
// write-ahead log of the one backend that runs a clone (pg_stat_get_backend_wal, Postgres 18), so
// what other sessions on the server write does not count.
func TestACloneWritesTheLogOfAFileCopyNotOfTheTemplate(t *testing.T) {
	testTemplate.Clone(t) // makes the template, or skips the test
	ctx := context.Background()
	conn, err := testTemplate.admin.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire an admin connection: %v", err)
	}
	defer conn.Release()
	var templateBytes int64
	if err := conn.QueryRow(ctx, "select pg_database_size($1)", testTemplate.name).Scan(&templateBytes); err != nil {
		t.Fatalf("read the template's size: %v", err)
	}

	suffix, err := randomSuffix()
	if err != nil {
		t.Fatalf("%v", err)
	}
	name := "testdb_test_" + suffix
	before := backendWALBytes(t, conn)
	if _, err := conn.Exec(ctx, testTemplate.cloneStatement(name)); err != nil {
		t.Fatalf("clone the template: %v", err)
	}
	testTemplate.drop(name)
	written := backendWALBytes(t, conn) - before

	if written > templateBytes/100 {
		t.Fatalf("one clone wrote %d bytes of write-ahead log, more than a hundredth of the %d-byte template: it went through the log", written, templateBytes)
	}
	t.Logf("one clone wrote %d bytes of write-ahead log; the template holds %d bytes", written, templateBytes)
}

// backendWALBytes is the write-ahead log conn's backend has written. pg_stat_get_backend_wal
// reads the counters the backend last flushed, and the forced flush runs as its own statement ends.
func backendWALBytes(t *testing.T, conn *pgxpool.Conn) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, "select pg_stat_force_next_flush()"); err != nil {
		t.Fatalf("force the backend's statistics flush: %v", err)
	}
	var written int64
	if err := conn.QueryRow(ctx, "select wal_bytes::bigint from pg_stat_get_backend_wal(pg_backend_pid())").Scan(&written); err != nil {
		t.Fatalf("read the backend's write-ahead log: %v", err)
	}
	return written
}
