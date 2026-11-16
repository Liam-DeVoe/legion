//go:build linux

package store

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/sys/unix"
)

// poolSizeChildEnv makes this test binary, started again by
// TestOpensPoolSizeIsTheSameOnEveryMachine, open a store and print its pool's size beside the CPU
// count and GOMAXPROCS it ran with.
const poolSizeChildEnv = "LEGION_STORE_POOL_SIZE_CHILD"

// poolSizeLine is what the child prints.
var poolSizeLine = regexp.MustCompile(`numcpu=(\d+) gomaxprocs=(\d+) maxconns=(\d+)`)

// Open's pool holds poolConns connections whatever machine it runs on. pgxpool's default,
// max(4, runtime.NumCPU()), made it the node's CPU count. The row starts this test binary again
// twice: once on one CPU of this machine with GOMAXPROCS 1, and once on all of them with
// GOMAXPROCS 64. NumCPU is read from the process's CPU affinity when it starts, so the first child
// is started from a thread whose affinity is that one CPU, which the child inherits. Each child
// prints the NumCPU it saw, which is the row's control that the two runs really saw different
// machines, and its pool's MaxConns, which must be poolConns both times.
func TestOpensPoolSizeIsTheSameOnEveryMachine(t *testing.T) {
	if os.Getenv(poolSizeChildEnv) != "" {
		s, err := Open(t.Context(), testDSN(t))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer s.Close()
		fmt.Printf("numcpu=%d gomaxprocs=%d maxconns=%d\n", runtime.NumCPU(), runtime.GOMAXPROCS(0), s.pool.Config().MaxConns)
		return
	}
	testDSN(t)
	oneCPU, one, oneMax := poolSizeChild(t, true, 1)
	allCPUs, _, allMax := poolSizeChild(t, false, 64)
	if oneCPU != 1 || allCPUs <= 1 {
		t.Fatalf("the children saw NumCPU %d and %d, want 1 and this machine's %d: the affinity did not reach them, so the row proves nothing", oneCPU, allCPUs, runtime.NumCPU())
	}
	if one != 1 {
		t.Fatalf("the one-CPU child ran with GOMAXPROCS %d, want 1", one)
	}
	if oneMax != poolConns || allMax != poolConns {
		t.Fatalf("Open's pool holds %d connections on 1 CPU and %d on %d CPUs, want %d on both", oneMax, allMax, allCPUs, poolConns)
	}
}

// poolSizeChild runs TestOpensPoolSizeIsTheSameOnEveryMachine's child, on CPU 0 alone when oneCPU
// is set, with GOMAXPROCS gomaxprocs, and answers the NumCPU, GOMAXPROCS and MaxConns it printed.
func poolSizeChild(t *testing.T, oneCPU bool, gomaxprocs int) (numCPU, procs, maxConns int) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestOpensPoolSizeIsTheSameOnEveryMachine$", "-test.count=1")
	child.Env = append(os.Environ(), poolSizeChildEnv+"=1", "GOMAXPROCS="+strconv.Itoa(gomaxprocs))
	var out []byte
	var err error
	func() {
		// The child inherits the affinity of the thread that starts it, so the start runs on a
		// locked thread whose affinity is set for it alone, and that thread is restored before it
		// is unlocked.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if oneCPU {
			var was, one unix.CPUSet
			if err := unix.SchedGetaffinity(0, &was); err != nil {
				t.Fatalf("read this thread's affinity: %v", err)
			}
			one.Set(0)
			if err := unix.SchedSetaffinity(0, &one); err != nil {
				t.Fatalf("set this thread's affinity to CPU 0: %v", err)
			}
			defer func() {
				if err := unix.SchedSetaffinity(0, &was); err != nil {
					t.Errorf("restore this thread's affinity: %v", err)
				}
			}()
		}
		out, err = child.CombinedOutput()
	}()
	if err != nil {
		t.Fatalf("the child: %v\n%s", err, out)
	}
	found := poolSizeLine.FindSubmatch(out)
	if found == nil {
		t.Fatalf("the child printed no pool size:\n%s", out)
	}
	numCPU, _ = strconv.Atoi(string(found[1]))
	procs, _ = strconv.Atoi(string(found[2]))
	maxConns, _ = strconv.Atoi(string(found[3]))
	return numCPU, procs, maxConns
}

// withSetting is dsn with one more connection setting, in dsn's own form: a URL's query, or the
// next quoted pair of a keyword/value string.
func withSetting(t *testing.T, dsn, key, value string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse the test database's URL: %v", err)
		}
		query := u.Query()
		query.Set(key, value)
		u.RawQuery = query.Encode()
		return u.String()
	}
	return dsn + " " + key + "=" + quoted(value)
}

// quoted is value as a keyword/value string writes it: in single quotes, with each backslash and
// single quote escaped.
func quoted(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

// asKeywords is the connection dsn names, as a keyword/value string, whichever form dsn has: so a
// row can write a setting no URL can carry the same way, a quoted value. A connection pgx would
// make without TLS keeps sslmode=disable.
func asKeywords(t *testing.T, dsn string) string {
	t.Helper()
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the test database's connection string: %v", err)
	}
	pairs := []string{
		"host=" + quoted(config.Host), "port=" + strconv.Itoa(int(config.Port)), "user=" + quoted(config.User),
		"password=" + quoted(config.Password), "dbname=" + quoted(config.Database),
	}
	if config.TLSConfig == nil {
		pairs = append(pairs, "sslmode=disable")
	}
	for key, value := range config.RuntimeParams {
		pairs = append(pairs, key+"="+quoted(value))
	}
	return strings.Join(pairs, " ")
}

// poolServiceFile writes a pg_service.conf whose service poolsize names pool_max_conns=7, and
// points PGSERVICEFILE at it for t.
func poolServiceFile(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(path, []byte("[poolsize]\npool_max_conns=7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", path)
}

// Settings that name pool_max_conns keep their own number, as pgx reads them: in a URL, in a
// keyword/value string, and in the service file a `service=` entry names. Text inside a quoted
// value names nothing, and settings that name no size get poolConns. The deployed daemons name 32
// in their DSN; a deployment that needs another number writes it there.
func TestADSNsPoolMaxConnsOutranksTheStoresSize(t *testing.T) {
	poolServiceFile(t)
	for _, tc := range []struct {
		name, dsn string
		names     bool
	}{
		{"a postgres URL that names it", "postgres://u@h/db?sslmode=disable&pool_max_conns=7", true},
		{"a postgresql URL that names it", "postgresql://u@h/db?pool_max_conns=7", true},
		{"a URL that does not", "postgres://u@h/db?sslmode=disable", false},
		{"a URL that names another pool key", "postgres://u@h/db?pool_min_conns=2", false},
		{"a keyword/value string that names it", "host=h dbname=db pool_max_conns=7", true},
		{"a keyword/value string with spaces around the =", "host=h pool_max_conns = 7", true},
		{"a keyword/value string that does not", "host=h dbname=db", false},
		{"the key inside a quoted value", "host=localhost application_name='worker pool_max_conns=7'", false},
		{"a service whose file names it", "host=h service=poolsize", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := namesPoolMaxConns(tc.dsn); got != tc.names {
				t.Errorf("namesPoolMaxConns(%q) = %t, want %t", tc.dsn, got, tc.names)
			}
		})
	}

	database := testDSN(t)
	keywords := asKeywords(t, database)
	for _, tc := range []struct {
		name, dsn string
		want      int32
	}{
		{"the test database as given, naming no size", database, poolConns},
		{"the test database as given, naming 7", withSetting(t, database, poolMaxConnsKey, "7"), 7},
		{"a keyword/value string naming 7", withSetting(t, keywords, poolMaxConnsKey, "7"), 7},
		{"the key inside a quoted value", withSetting(t, keywords, "application_name", "worker pool_max_conns=7"), poolConns},
		{"a service whose file names 7", withSetting(t, keywords, "service", "poolsize"), 7},
	} {
		t.Run("Open, "+tc.name, func(t *testing.T) {
			s, err := Open(t.Context(), tc.dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer s.Close()
			if got := s.pool.Config().MaxConns; got != tc.want {
				t.Errorf("Open's pool holds %d connections, want %d", got, tc.want)
			}
		})
	}
}
