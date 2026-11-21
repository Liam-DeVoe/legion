package ompsessions

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// poolVariable names the PgBouncer that TestEveryPoolPinsItsSearchPath's pooled leg reaches the
// test server through: a postgres:// URL of a PgBouncer 1.25.2 in transaction mode in front of
// the server LEGION_TEST_PG_DSN names, serving any database by a wildcard entry.
const poolVariable = "TEST_PGBOUNCER_DSN"

// noPool is TestEveryPoolPinsItsSearchPath's failure when poolVariable is unset.
const noPool = poolVariable + " is unset, so the pooled leg has no pool to run through. The session database's pods reach it " +
	"through a PgBouncer pool in transaction mode, which refuses search_path as a startup parameter and carries no " +
	"setting from one transaction to the next, so a pin a direct connection alone holds would pass here and fail " +
	"there: the leg fails rather than skips. Start PgBouncer 1.25.2 in transaction mode in front of the server " +
	"LEGION_TEST_PG_DSN names, as the step \"Start PgBouncer in front of the Postgres service\" of " +
	".github/workflows/ci-job-daemon-go.yaml does, and set " + poolVariable + " to its postgres:// URL for a login that " +
	"may connect to any database there; the test names the database it makes in place of the URL's."

// pinnedPath is search_path as every transaction of the package must read it: pg_catalog, and
// the session's own temporary schema, which a name of a function or an operator never reaches.
// No schema another login can create in is on it.
const pinnedPath = "pg_catalog, pg_temp"

// transaction is inTransaction's shape: what the pin check runs its read in.
type transaction func(ctx context.Context, conn *pgx.Conn, access pgx.TxAccessMode, fn func(pgx.Tx) error) error

// unpinned is a transaction helper without the pin, the control TestEveryPoolPinsItsSearchPath
// must catch.
func unpinned(ctx context.Context, conn *pgx.Conn, access pgx.TxAccessMode, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: access}, fn)
}

// startupPinned opens dsn with search_path as a startup parameter: a pin a direct connection
// holds for its whole session, and a pool in transaction mode refuses.
func startupPinned(ctx context.Context, dsn string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.RuntimeParams["search_path"] = pinnedPath
	return pgx.ConnectConfig(ctx, config)
}

// pinFailure is what is wrong with a client's pin on the database dsn names, "" for nothing. The
// client opens its connection with open, and a read-only and a read-write transaction run through
// within must each read pinnedPath as search_path inside it, where a statement of the package
// runs: outside a transaction a client that pins each one reads the default.
func pinFailure(ctx context.Context, dsn string, open func(context.Context, string) (*pgx.Conn, error), within transaction) string {
	conn, err := open(ctx, dsn)
	if err != nil {
		return fmt.Sprintf("the client cannot connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, access := range []pgx.TxAccessMode{pgx.ReadOnly, pgx.ReadWrite} {
		var path string
		if err := within(ctx, conn, access, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT pg_catalog.current_setting('search_path')").Scan(&path)
		}); err != nil {
			return fmt.Sprintf("a %s transaction cannot read search_path: %v", access, err)
		}
		if path != pinnedPath {
			return fmt.Sprintf("a %s transaction reads search_path %q, want %q", access, path, pinnedPath)
		}
	}
	return ""
}

// throughThePool is pool, the URL poolVariable holds, naming the database that direct names in
// place of its own. A refusal names neither URL, since each carries a password.
func throughThePool(t *testing.T, pool, direct string) string {
	t.Helper()
	database, err := pgconn.ParseConfig(direct)
	if err != nil {
		t.Fatalf("read the test database's URL: %v", err)
	}
	asked, err := pgconn.ParseConfig(pool)
	if err != nil {
		t.Fatalf("%s is no URL pgx reads", poolVariable)
	}
	parsed, err := url.Parse(pool)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("%s is no postgres:// URL", poolVariable)
	}
	parsed.Path = "/" + database.Database
	rewritten := parsed.String()
	read, err := pgconn.ParseConfig(rewritten)
	if err != nil || read.Host != asked.Host || read.Port != asked.Port || read.User != asked.User || read.Database != database.Database {
		t.Fatalf("%s with the test database's name in it is no longer the same pool and login", poolVariable)
	}
	return rewritten
}

// Every transaction of the package reads search_path as pinnedPath, both on a direct
// connection and through a PgBouncer 1.25.2 in transaction mode, the pool the pods reach the
// session database through: inTransaction's first statement is SET LOCAL search_path =
// pg_catalog, pg_temp. TestNoStatementRunsOutsideTheHelper holds every statement to that helper,
// so this read covers each. Its controls, each through both legs: the same helper without the
// pin, which reads the server's default and must fail naming it; a client that sets search_path
// as a startup parameter, which holds on a direct connection and which the pool refuses, so it
// must fail the pooled leg naming the refusal; and the package's own client, which passes both.
func TestEveryPoolPinsItsSearchPath(t *testing.T) {
	pool := os.Getenv(poolVariable)
	if pool == "" {
		t.Fatal(noPool)
	}
	ctx := context.Background()
	direct := testdb.Database(t, "legion_ompsessions_pin")
	legs := []struct{ name, dsn string }{{"direct", direct}, {"through the pool", throughThePool(t, pool, direct)}}
	for _, client := range []struct {
		name   string
		open   func(context.Context, string) (*pgx.Conn, error)
		within transaction
		// fails is, by leg, what the check must name in its failure, and a leg it holds
		// nothing for one the client must pass.
		fails map[string]string
	}{
		{"the package's client (ConnectURL, inTransaction)", ConnectURL, inTransaction, nil},
		{"control: the helper without the pin", ConnectURL, unpinned, map[string]string{
			"direct":           "transaction reads search_path",
			"through the pool": "transaction reads search_path",
		}},
		{"control: search_path as a startup parameter", startupPinned, unpinned, map[string]string{
			"through the pool": "unsupported startup parameter",
		}},
	} {
		for _, leg := range legs {
			failure := pinFailure(ctx, leg.dsn, client.open, client.within)
			switch want, fails := client.fails[leg.name]; {
			case !fails && failure != "":
				t.Errorf("%s, %s: %s", client.name, leg.name, failure)
			case fails && !strings.Contains(failure, want):
				t.Errorf("%s, %s: the check found %q, want a failure naming %q", client.name, leg.name, failure, want)
			default:
				t.Logf("%s, %s: %q", client.name, leg.name, failure)
			}
		}
	}
}
