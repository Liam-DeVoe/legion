package launcher

// A child under a URL file, which no proxy of the generation's serves, can fail its first open on a
// store that answers again before the launcher asks it after the exit. The launcher's own record of
// the store from the child's start (storeWitness) still ties that exit to the store, and a child
// that fails while the store answers is still charged.

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

func TestAURLFileStoreThatFailsTheChildsOpenAndAnswersBeforeTheAskIsTheStoresFault(t *testing.T) {
	for name, tc := range map[string]struct {
		down bool
		want shimwire.SessionRefusal
	}{
		"the store fails the child's open and answers again before the ask": {down: true, want: shimwire.SessionStoreUnreachable},
		"control: the store answers and the child fails on its own":         {},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFlappingStore(t, testdb.Database(t, "legion_launcher_witness_test"), tc.down)
			urlFile := filepath.Join(t.TempDir(), "dsn")
			if err := replaceURLFile(urlFile, store.url); err != nil {
				t.Fatal(err)
			}
			g := newRig(t)
			marker := filepath.Join(t.TempDir(), "child")
			command := g.childStart(1, filepath.Join(t.TempDir(), "marker"), nil)
			env := append(command.Env, urlStoreChildEnv+"="+marker,
				ompsessions.StorageVariable+"="+ompsessions.SQLStorage, ompsessions.DSNFileVariable+"="+urlFile)
			if got := g.start(shimwire.LauncherStart{ID: "start-1", Generation: 1,
				Argv: []string{command.Argv[0], "-test.run=^TestLauncherURLStoreChild$"}, Env: env}); !got.OK {
				t.Fatalf("start = %#v, want the child started", got)
			}
			if tc.down {
				// The store answers again once the child's open, and whatever else dialed it from
				// the child's start, met it down: two refusals, or five seconds after the child's
				// open failed when nothing else dialed it. Only then does the child exit.
				awaitFile(t, marker+".failed", 30*time.Second)
				for deadline := time.Now().Add(5 * time.Second); store.refused.Load() < 2 && time.Now().Before(deadline); {
					time.Sleep(20 * time.Millisecond)
				}
				store.down.Store(false)
				if err := os.WriteFile(marker+".recovered", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			exit := g.awaitExitWithin(1, time.Minute)
			if exit.Code != 1 || exit.Session != tc.want {
				t.Fatalf("the child's exit = %+v, want status 1 and session refusal %q\nlauncher log:\n%s", exit, tc.want, g.stderr.String())
			}
		})
	}
}

// urlStoreChildEnv marks the test binary re-executed as a child that opens its session store
// through the URL file its environment names, as Oh My Pi does at its start
// (TestLauncherURLStoreChild); its value is the path its markers are named after.
const urlStoreChildEnv = "LEGION_LAUNCHER_URL_STORE_CHILD"

func init() {
	helperChildVariables = append(helperChildVariables, urlStoreChildEnv)
}

// TestLauncherURLStoreChild is a child that opens its session store through the URL file
// OMP_SESSION_SQL_DSN_FILE names. On a store that fails the open it writes the marker
// urlStoreChildEnv names with ".failed", waits up to ten seconds for ".recovered", and exits 1; on
// a store that answers it exits 1 at once, a failure of its own. Run as a test of its own, outside
// a launcher, it does nothing.
func TestLauncherURLStoreChild(t *testing.T) {
	marker := os.Getenv(urlStoreChildEnv)
	if marker == "" {
		return
	}
	dsn, err := ompsessions.ReadDSN(os.Getenv(ompsessions.DSNFileVariable))
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var conn *pgx.Conn
		conn, err = ompsessions.ConnectURL(ctx, dsn)
		cancel()
		if conn != nil {
			_ = conn.Close(context.Background())
		}
	}
	if err != nil {
		_ = os.WriteFile(marker+".failed", nil, 0o600)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(marker + ".recovered"); err == nil {
				break
			}
		}
	}
	os.Exit(1)
}

// awaitFile waits for path to exist, and fails the test by name past within.
func awaitFile(t *testing.T, path string, within time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s did not appear within %s", path, within)
}

// flappingStore is the database at dsn behind a TCP stand-in on 127.0.0.1: while down is set,
// every connection reaches a server that answers it is starting up (57P03), as a store does while
// it restarts, and is counted in refused; otherwise it reaches the database.
type flappingStore struct {
	url     string
	down    atomic.Bool
	refused atomic.Int32
}

func newFlappingStore(t *testing.T, dsn string, down bool) *flappingStore {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	starting := testdb.Refusing(t, "57P03", "the database system is starting up")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	through := *parsed
	through.Host = listener.Addr().String()
	s := &flappingStore{url: through.String()}
	s.down.Store(down)
	var mu sync.Mutex
	conns := map[net.Conn]struct{}{}
	var wg sync.WaitGroup
	pipe := func(to, from net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(to, from)
		_ = to.Close()
		_ = from.Close()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			target := parsed.Host
			if s.down.Load() {
				target = starting
				s.refused.Add(1)
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			mu.Lock()
			conns[client], conns[server] = struct{}{}, struct{}{}
			mu.Unlock()
			wg.Add(2)
			go pipe(server, client)
			go pipe(client, server)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		for conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return s
}
