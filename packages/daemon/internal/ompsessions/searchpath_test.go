package ompsessions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// poolVariable names the PgBouncer that TestEveryPoolPinsItsSearchPath's pooled leg reaches the
// test server through: a postgres:// URL of a PgBouncer 1.25.2 in transaction mode in front of
// the server LEGION_TEST_PG_DSN names, serving any database by a wildcard entry. Its name has the
// LEGION_TEST_ prefix of every test input of this module (fixtureenv keeps them by it).
const poolVariable = "LEGION_TEST_PGBOUNCER_DSN"

// noPool is what TestEveryPoolPinsItsSearchPath says when LEGION_TEST_PG_DSN names a server and
// poolVariable is unset: a skip outside CI, and a failure on GitHub Actions, where the daemon-go
// job starts the pool, so a skip there would take the pooled leg out of the one run that holds it.
const noPool = poolVariable + " is unset, so the pooled leg has no pool to run through. The session database's pods reach it " +
	"through a PgBouncer pool in transaction mode, which refuses search_path as a startup parameter and carries no " +
	"setting from one transaction to the next, so a pin a direct connection alone holds would pass here and fail " +
	"there. Start PgBouncer 1.25.2 in transaction mode in front of the server LEGION_TEST_PG_DSN names, as CI's " +
	"daemon-go job does, and set " + poolVariable + " to what it prints (it needs Docker):\n" +
	"  " + poolVariable + "=$(packages/daemon/scripts/test-pgbouncer.sh --port 6433)"

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
	parsed, err := url.Parse(pool)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatalf("%s is no postgres:// URL", poolVariable)
	}
	parsed.Path = "/" + database.Database
	rewritten := parsed.String()
	// A dbname query parameter would override the path.
	if read, err := pgconn.ParseConfig(rewritten); err != nil || read.Database != database.Database {
		t.Fatalf("%s with the test database's name as its path does not name that database", poolVariable)
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
	// The database first: a host with no LEGION_TEST_PG_DSN skips here, as every test that needs
	// a database does. Where there is one and no pool, the test skips outside CI and fails on
	// GitHub Actions, the module's rule for an input CI provides (testbin's binary).
	direct := testdb.Database(t, "legion_ompsessions_pin")
	pool := os.Getenv(poolVariable)
	if pool == "" {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal(noPool)
		}
		t.Skip(noPool)
	}
	ctx := context.Background()
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

// TestEveryPoolPinsItsSearchPath, run in a test binary of its own, skips where there is no
// database to test against, as every test of the module that needs one does; skips naming the
// pool's variable where there is a database and no pool outside CI; and fails there under
// GITHUB_ACTIONS, where a skip would take the pooled leg out of the one run that must hold it.
// The child is this test binary, run with that test alone, LEGION_TEST_PG_DSN, the pool's
// variable and GITHUB_ACTIONS taken out of its environment, and the case's own settings put
// back. Each case looks for the first line of the message it wants, since `go test -v` indents
// the lines after it. The two cases with a database need one themselves, and skip without it.
func TestThePinCheckSkipsWithoutADatabaseAndFailsWithoutAPool(t *testing.T) {
	const pinTest = "TestEveryPoolPinsItsSearchPath"
	firstLine := func(message string) string {
		line, _, _ := strings.Cut(message, "\n")
		return line
	}
	run := func(t *testing.T, set ...string) (string, error) {
		t.Helper()
		child := exec.Command(os.Args[0], "-test.run=^"+pinTest+"$", "-test.count=1", "-test.v")
		child.Env = append(slices.DeleteFunc(os.Environ(), func(entry string) bool {
			name, _, _ := strings.Cut(entry, "=")
			return slices.Contains([]string{"LEGION_TEST_PG_DSN", poolVariable, "GITHUB_ACTIONS"}, name)
		}), set...)
		out, err := child.CombinedOutput()
		return string(out), err
	}
	t.Run("no database", func(t *testing.T) {
		out, err := run(t)
		if err != nil || !strings.Contains(out, "--- SKIP: "+pinTest) || !strings.Contains(out, firstLine(testdb.SkipMessage)) {
			t.Errorf("with neither LEGION_TEST_PG_DSN nor %s the run = %v; want it to skip naming LEGION_TEST_PG_DSN:\n%s", poolVariable, err, out)
		}
	})
	database := os.Getenv("LEGION_TEST_PG_DSN")
	t.Run("a database and no pool outside CI", func(t *testing.T) {
		if database == "" {
			t.Skip(testdb.SkipMessage)
		}
		out, err := run(t, "LEGION_TEST_PG_DSN="+database)
		if err != nil || !strings.Contains(out, "--- SKIP: "+pinTest) || !strings.Contains(out, firstLine(noPool)) {
			t.Errorf("with LEGION_TEST_PG_DSN and no %s outside CI the run = %v; want it to skip naming %s:\n%s", poolVariable, err, poolVariable, out)
		}
	})
	t.Run("a database and no pool on GitHub Actions", func(t *testing.T) {
		if database == "" {
			t.Skip(testdb.SkipMessage)
		}
		out, err := run(t, "LEGION_TEST_PG_DSN="+database, "GITHUB_ACTIONS=true")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || !strings.Contains(out, "--- FAIL: "+pinTest) || !strings.Contains(out, firstLine(noPool)) {
			t.Errorf("with LEGION_TEST_PG_DSN, no %s and GITHUB_ACTIONS=true the run = %v; want it to fail naming %s:\n%s", poolVariable, err, poolVariable, out)
		}
	})
}
