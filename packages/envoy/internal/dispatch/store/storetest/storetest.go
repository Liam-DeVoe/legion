// Package storetest gives each Postgres-backed Dispatch test its own fully migrated database, and
// waits for backends to queue behind a lock a test holds (WaitForLockWaiters). It is an ordinary
// package rather than a _test.go file so every package whose tests need a Dispatch database can
// import it; Go cannot share test-only code across packages.
//
// Open clones each database from a template this test process migrates once
// (internal/testpg/testdb's Template, which says why a clone copies files and why its drop runs in
// the background), on the server DISPATCH_TEST_DATABASE_URL names. OpenFrom clones one of the
// templates a caller's own migrate makes instead, each once per key by the first test that asks for
// it, and Empty is a database no migration has touched. A test never gets the database that URL
// names, and no function here returns that URL.
package storetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store"
	"github.com/sjawhar/envoy/internal/testpg/testdb"
)

// template is the database every Open clones: an empty one carrying every Dispatch migration and
// nothing else, so a clone keeps every table setting the product's database has, autovacuum
// included. A test whose answer follows the planner's choice analyzes the table itself once its
// rows are in (search_meaning_test.go, search_index_test.go, docs' block_index_test.go), so the
// plan it meets is the one the product's statistics call for. OpenFrom's templates are made beside
// it, on the same server, and Main drops them with it.
var template = testdb.NewTemplate(testdb.DispatchServer, "dispatch",
	"func TestMain(m *testing.M) { os.Exit(storetest.Main(m.Run)) }",
	func(ctx context.Context, databaseURL string) error {
		database, err := store.Open(ctx, databaseURL)
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}
		defer database.Pool.Close()
		return database.Migrate(ctx)
	})

// Open returns a store on a new database carrying every migration, dropped after t ends. It skips
// t when DISPATCH_TEST_DATABASE_URL is unset.
func Open(t testing.TB) *store.Store {
	t.Helper()
	return openClone(t, template.Clone(t))
}

// OpenFrom is Open for a database migrate brings to its schema instead of this tree's runner
// alone: it returns a store on a new database cloned from the template of key, dropped after t
// ends. The template is made once per key in the test binary, by the first test that asks: a new
// empty database whose connection string migrate is given, which migrate migrates and leaves with
// no connection open (testdb's Template.CloneFrom). A template whose migrate failed, or ended its
// test, fails every test that asks for it again. It skips t when DISPATCH_TEST_DATABASE_URL is
// unset.
func OpenFrom(t testing.TB, key string, migrate func(databaseURL string) error) *store.Store {
	t.Helper()
	return openClone(t, template.CloneFrom(t, key, func(_ context.Context, databaseURL string) error {
		return migrate(databaseURL)
	}))
}

// openClone opens a store on the clone databaseURL names, closed before the clone's drop runs.
func openClone(t testing.TB, databaseURL string) *store.Store {
	t.Helper()
	database, err := store.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open isolated database: %v", err)
	}
	t.Cleanup(database.Pool.Close)
	return database
}

// Empty returns the connection string of a new database that holds nothing, not even the
// version table, dropped after t ends: for a test that migrates it itself, as the two-build
// suite has the builds it runs migrate theirs, in production's order. It makes no template
// (testdb's Server.Database). It skips t when DISPATCH_TEST_DATABASE_URL is unset.
func Empty(t testing.TB) string {
	t.Helper()
	return testdb.DispatchServer.Database(t, "dispatch_test")
}

// Main runs the package's tests through run, waits for their databases to drop, then drops every
// template they were cloned from (Open's and each OpenFrom key's) and OtherDatabase's database. A
// package whose tests call Open or OpenFrom uses it as its TestMain:
// func TestMain(m *testing.M) { os.Exit(storetest.Main(m.Run)) }. It takes the run function so it
// composes with another fixture's Main:
// os.Exit(storetest.Main(func() int { return testnats.Main(m.Run) })).
func Main(run func() int) int {
	return template.Main(run)
}

// OtherDatabase is the URL of a second database of the binary's own on the same server, empty and
// shared by the binary's tests, which Main drops. It is for a test that holds a transaction in
// another database of the instance, as a job in another database does; it writes nothing there.
// It skips t when DISPATCH_TEST_DATABASE_URL is unset.
func OtherDatabase(t testing.TB) string {
	t.Helper()
	return template.Other(t)
}

// Available reports whether DISPATCH_TEST_DATABASE_URL is set: whether Open would give a database
// rather than skip. It is for a test that takes its databases another way, such as in a child
// process of its own binary, and skips where Open would.
func Available() bool {
	return testdb.DispatchServer.Available()
}
