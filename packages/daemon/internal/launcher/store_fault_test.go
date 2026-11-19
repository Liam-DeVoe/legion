package launcher

// A start whose session store did not answer says so in its answer's own field
// (shimwire.SessionStoreUnreachable), which the daemon reads as a store fault: the claim keeps its
// session, is charged nothing, and is started again a minute later (runtime/sandbox's
// startRefusal, supervise's startWaitsForStore). A store did not answer when it refused the dial
// or never answered it, or holds no session table (ompsessions.StoreFault). These are the faults a
// start meets past the resume's own lookup: the generation's proxy to a session database of the
// pod's own, and a Claude Code session's keeper dialing the table and restoring the session's files
// from it. A child that exits on a store that stopped answering it exits as such a fault too. Every
// other failure of a start's store is its configuration's, a refusal the daemon charges, naming it.

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/sessionproxy"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// A generation whose proxy's proof dial the session database refused or never answered, or whose
// resume's lookup through the proxy the database refused once the proof dial had passed, is refused
// as a session store that did not answer, having started nothing. The lookup's own client reads
// that refusal as its connection closing; the proxy recorded the refused dial (Proxy.Fault). The
// next start of the same session, once the database answers, runs.
func TestAProxyThatCannotReachTheSessionDatabaseIsAStoreFaultAndTheNextStartResumes(t *testing.T) {
	for name, fault := range map[string]struct {
		// refuseFrom is the first dial the database refuses, the proof dial being the first.
		refuseFrom int32
		stall      bool
		// names is what the refusal names of the step that met the fault.
		names string
	}{
		"the proof dial is refused":                         {refuseFrom: 1, names: "the session database proxy for generation 3"},
		"the proof dial is not answered":                    {stall: true, names: "the session database proxy for generation 3"},
		"the lookup is refused after the proof dial passed": {refuseFrom: 2, names: "resume session "},
	} {
		t.Run(name, func(t *testing.T) {
			dsn := testdb.Database(t, "legion_launcher_proxy_fault_test")
			createSessionTables(t, dsn)
			database, dialer := sessionDatabaseAt(t, dsn)
			shortenResumeLookup(t)
			refused := refusedDial(t)
			var down atomic.Bool
			var dials atomic.Int32
			down.Store(true)
			dialer.check = func() error {
				if n := dials.Add(1); fault.refuseFrom > 0 && n >= fault.refuseFrom && down.Load() {
					return refused
				}
				return nil
			}
			cfg := Config{SessionDatabase: database, SessionDialer: dialer.build}
			if fault.stall {
				// A grace of 2 s bounds the stalled proof dial at 1 s (proofTimeout), and leaves
				// the next generation's real proof dial the same second.
				dialer.stallAt(silentUpstream(t))
				cfg.StopGrace = 2 * time.Second
			}
			g := newSessionRig(t, cfg)
			sessionFile := filepath.Join(t.TempDir(), "sessions", "2026-10-08T12-00-00-000Z_0007.jsonl")
			importSession(t, dsn, sessionFile)
			marker := filepath.Join(t.TempDir(), "marker")
			start := func(generation uint64) shimwire.LauncherStartResult {
				command := g.childStart(generation, marker, nil)
				command.Env = append(command.Env, ompsessions.StorageVariable+"="+ompsessions.SQLStorage,
					ompsessions.DSNFileVariable+"="+filepath.Join(g.generationDir(generation), ompsessions.DSNFileName))
				command.ResumeFile = sessionFile
				return g.start(command)
			}

			if got := start(3); got.OK || got.Session != shimwire.SessionStoreUnreachable || !strings.Contains(got.Error, fault.names) {
				t.Fatalf("start over a proxy that cannot reach the session database = %#v, want a refusal naming %q as %q", got, fault.names, shimwire.SessionStoreUnreachable)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a generation whose proxy could not reach the session database started its child")
			}
			down.Store(false)
			dialer.stallAt("")
			if got := start(4); !got.OK {
				t.Fatalf("the next start of the same session once the database answers = %#v, want generation 4 to run", got)
			}
			if seen := publishedAll(t, marker); seen.socket != "bound" {
				t.Fatalf("generation 4's child found its session socket %s, want it already bound", seen.socket)
			}
			g.stop(4)
		})
	}
}

// A Claude Code start whose keeper's own dial of the table is refused, or whose restore cannot
// read the session's rows back, is refused as a session store that did not answer, having started
// nothing. The dial is a fresh start's, whose keeper reaches the table itself; the rows that cannot
// be read are ones another transaction holds locked past the start's bound. The next start, once
// the store answers, runs, and a resume's child starts on the transcript the table holds.
func TestAClaudeKeeperThatCannotReachTheTableIsAStoreFaultAndTheNextStartRestores(t *testing.T) {
	for name, fault := range map[string]struct {
		// resume is whether the start resumes the transcript or starts the session fresh.
		resume bool
		// refusal is what the start's refusal names of the step that met the fault.
		refusal func(c *claudeRig) string
		// fail puts the fault in place and returns its end.
		fail func(t *testing.T, c *claudeRig, dsn string) (end func())
	}{
		"the keeper's own dial": {
			refusal: func(*claudeRig) string { return "keep Claude Code session " + claudeSession },
			fail: func(t *testing.T, c *claudeRig, dsn string) func() {
				if err := replaceURLFile(c.dsnFile, refusingURL(t)); err != nil {
					t.Fatal(err)
				}
				return func() {
					if err := replaceURLFile(c.dsnFile, dsn); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		"the restore of the session's files": {
			resume: true,
			refusal: func(c *claudeRig) string {
				return "keep Claude Code session " + claudeSession + ": restore " + c.transcript
			},
			fail: func(t *testing.T, _ *claudeRig, dsn string) func() {
				conn, err := ompsessions.ConnectURL(context.Background(), dsn)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := conn.Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(context.Background(), "LOCK TABLE omp_session_files_parts IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
				return func() {
					_ = tx.Rollback(context.Background())
					_ = conn.Close(context.Background())
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dsn, dsnFile := testdb.DSNFile(t, "legion_launcher_claude_fault_test")
			c := claudeRigOn(t, newSessionRig(t, Config{}), dsnFile)
			shortenResumeLookup(t)
			saved := "{\"n\":1}\n{\"n\":2}\n"
			c.seed(c.transcript, saved, time.Now())

			end := fault.fail(t, c, dsn)
			told := filepath.Join(t.TempDir(), "seen")
			got := c.start(c.command(1, seeing(c.transcript, told), fault.resume))
			end()
			if want := fault.refusal(c); got.OK || got.Session != shimwire.SessionStoreUnreachable || !strings.Contains(got.Error, want) {
				t.Fatalf("a Claude Code start whose store did not answer = %#v, want a refusal naming %q as %q", got, want, shimwire.SessionStoreUnreachable)
			}
			if _, err := os.Stat(told); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a Claude Code start whose store did not answer started its child")
			}

			_ = os.RemoveAll(filepath.Join(c.configDir, "projects"))
			if got := c.start(c.command(2, seeing(c.transcript, told), fault.resume)); !got.OK {
				t.Fatalf("the next start of the same session once the store answers = %#v, want generation 2 to run", got)
			}
			want := "missing"
			if fault.resume {
				want = saved
			}
			if found := seen(t, told); found != want {
				t.Errorf("generation 2's child found the transcript %q at its start, want %q", found, want)
			}
			c.stop(2)
		})
	}
}

// A start whose session store is configured wrong is the start's own failure, which the daemon
// charges, and its refusal names the cause: the store answered, or was never asked. A credential
// that is not there, an environment that names no URL file, a URL file that is not there, a
// password the server refuses, a database that takes no writes (an address that names a replica):
// each is refused plainly, having started nothing, on both harnesses, fresh or resumed. The
// control is a URL file whose store refuses the dial, which is the store's fault.
func TestAStartWhoseSessionStoreIsConfiguredWrongIsChargedNamingItsCause(t *testing.T) {
	dsn := testdb.Database(t, "legion_launcher_config_fault_test")
	createSessionTables(t, dsn)
	shortenResumeLookup(t)
	missing := filepath.Join(t.TempDir(), "absent-dsn")
	wrongPassword := filepath.Join(t.TempDir(), "wrong-password-dsn")
	if err := replaceURLFile(wrongPassword, withPassword(t, dsn, "not-the-password")); err != nil {
		t.Fatal(err)
	}
	refusing := filepath.Join(t.TempDir(), "refusing-dsn")
	if err := replaceURLFile(refusing, refusingURL(t)); err != nil {
		t.Fatal(err)
	}
	readOnly := readOnlyURLFile(t)
	sessionFile := filepath.Join(t.TempDir(), "sessions", "2026-10-08T12-00-00-000Z_0009.jsonl")
	importSession(t, dsn, sessionFile)
	omp := func(dsnFile string) func(t *testing.T) (shimwire.LauncherStartResult, string) {
		return func(t *testing.T) (shimwire.LauncherStartResult, string) {
			g := newRig(t)
			marker := filepath.Join(t.TempDir(), "marker")
			command := g.childStart(3, marker, nil)
			command.Env = append(command.Env, ompsessions.StorageVariable+"="+ompsessions.SQLStorage, ompsessions.DSNFileVariable+"="+dsnFile)
			command.ResumeFile = sessionFile
			return g.start(command), marker
		}
	}
	claude := func(dsnFile string, resume bool) func(t *testing.T) (shimwire.LauncherStartResult, string) {
		return func(t *testing.T) (shimwire.LauncherStartResult, string) {
			c := claudeRigOn(t, newRig(t), testdbFile(t, dsn))
			c.seed(c.transcript, "{\"n\":1}\n", time.Now())
			told := filepath.Join(t.TempDir(), "seen")
			return c.start(c.command(1, seeing(c.transcript, told), resume, ompsessions.DSNFileVariable+"="+dsnFile)), told
		}
	}
	for name, tc := range map[string]struct {
		start func(t *testing.T) (shimwire.LauncherStartResult, string)
		// names is what the refusal must name; storeFault is whether it is the store's.
		names      string
		storeFault bool
	}{
		"an Oh My Pi resume whose proxy's credential is not there": {names: "could not find default credentials",
			start: func(t *testing.T) (shimwire.LauncherStartResult, string) {
				database, _ := sessionDatabaseAt(t, dsn)
				g := newSessionRig(t, Config{SessionDatabase: database, SessionDialer: func(context.Context, string) (sessionproxy.Dialer, error) {
					return nil, errors.New("google: could not find default credentials")
				}})
				marker := filepath.Join(t.TempDir(), "marker")
				command := g.childStart(3, marker, nil)
				command.Env = append(command.Env, ompsessions.StorageVariable+"="+ompsessions.SQLStorage,
					ompsessions.DSNFileVariable+"="+filepath.Join(g.generationDir(3), ompsessions.DSNFileName))
				command.ResumeFile = sessionFile
				return g.start(command), marker
			}},
		"an Oh My Pi resume whose environment names no URL file": {names: ompsessions.DSNFileVariable + " names no file", start: omp("")},
		"an Oh My Pi resume whose URL file is not there":         {names: missing, start: omp(missing)},
		"an Oh My Pi resume whose server refuses the password":   {names: "password authentication failed", start: omp(wrongPassword)},
		"a fresh Claude Code start whose URL file is not there":  {names: missing, start: claude(missing, false)},
		"a Claude Code resume whose environment names no URL file": {names: ompsessions.DSNFileVariable + " names no file",
			start: claude("", true)},
		"a fresh Claude Code start whose server refuses the password": {names: "password authentication failed", start: claude(wrongPassword, false)},
		"a fresh Claude Code start whose database takes no writes":    {names: "read-only transaction", start: claude(readOnly, false)},
		"control: an Oh My Pi resume whose store refuses the dial": {names: "the session store could not be asked", storeFault: true,
			start: omp(refusing)},
		"control: a fresh Claude Code start whose store refuses the dial": {names: "the session store could not be asked", storeFault: true,
			start: claude(refusing, false)},
	} {
		t.Run(name, func(t *testing.T) {
			got, marker := tc.start(t)
			want := shimwire.SessionRefusal("")
			if tc.storeFault {
				want = shimwire.SessionStoreUnreachable
			}
			if got.OK || got.Session != want || !strings.Contains(got.Error, tc.names) {
				t.Fatalf("start = %#v, want a refusal naming %q with session refusal %q", got, tc.names, want)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a refused start started its child")
			}
		})
	}
}

// The pinned Oh My Pi, started on a session database that stops answering it after the start's
// checks passed, exits at once, and its exit says so (shimwire.LauncherExit.Session): under the
// generation's proxy, whose dial of the database for the child's own connection is refused; and
// under a URL file no proxy serves, which the launcher dials itself once the child exits with an
// error, and which refuses it. A child that exits while the store answers exits as itself: Oh My
// Pi under the proxy, whose own connections reach the database; and, under a URL file, a shell in
// Oh My Pi's place that exits with an error or at 0, since what those two controls test is how the
// launcher classifies an exit when its own ask of the store is answered, not what Oh My Pi does.
func TestAnOhMyPiThatExitsOnAStoreThatStoppedAnsweringItExitsAsAStoreFault(t *testing.T) {
	ompBinary := testbin.OMP(t)
	for name, tc := range map[string]struct {
		// proxied is whether the generation has a proxy of its own; refuse is whether the store
		// stops answering once the start's checks have passed.
		proxied, refuse bool
		// shell, when set, is the child in Oh My Pi's place, run by /bin/sh, and code the status
		// it exits with.
		shell string
		code  int
		want  shimwire.SessionRefusal
	}{
		"under the proxy, the child's own connection is refused":               {proxied: true, refuse: true, want: shimwire.SessionStoreUnreachable},
		"control: under the proxy, the store answers and the child ends":       {proxied: true},
		"under a URL file, the store refuses the child and the launcher":       {refuse: true, want: shimwire.SessionStoreUnreachable},
		"control: under a URL file, the store answers a child that fails":      {shell: "exit 3", code: 3},
		"control: under a URL file, the store answers and the child ends at 0": {shell: "exit 0"},
	} {
		t.Run(name, func(t *testing.T) {
			dsn := testdb.Database(t, "legion_launcher_child_store_test")
			createSessionTables(t, dsn)
			home := t.TempDir()
			argv := []string{"/bin/sh", "-c", tc.shell}
			if tc.shell == "" {
				testbin.OMPHome(t, ompBinary, home)
				testbin.WriteOMPProfile(t, home, "providers:\n  operator:\n    baseUrl: http://127.0.0.1:9\n    auth: apiKey\n    api: anthropic-messages\n"+
					"    apiKey: unused\n    models:\n      - id: store-fault-test\n        name: Store fault test\n", "modelRoles:\n  default: operator/store-fault-test\n")
				argv = []string{ompBinary, "--mode", "rpc"}
			}
			var g *rig
			var urlFile string
			var down atomic.Bool
			if tc.proxied {
				database, dialer := sessionDatabaseAt(t, dsn)
				refused := refusedDial(t)
				// The proof dial is the start's check; every dial after it is the child's own.
				var dials atomic.Int32
				dialer.check = func() error {
					if dials.Add(1) > 1 && down.Load() {
						return refused
					}
					return nil
				}
				down.Store(tc.refuse)
				g = newSessionRig(t, Config{SessionDatabase: database, SessionDialer: dialer.build})
				urlFile = filepath.Join(g.generationDir(1), ompsessions.DSNFileName)
			} else {
				g = newRig(t)
				urlFile = filepath.Join(t.TempDir(), "dsn")
				target := dsn
				if tc.refuse {
					target = refusingURL(t)
				}
				if err := replaceURLFile(urlFile, target); err != nil {
					t.Fatal(err)
				}
			}
			env := append(testbin.OMPEnviron(home), ompsessions.StorageVariable+"="+ompsessions.SQLStorage, ompsessions.DSNFileVariable+"="+urlFile,
				"LEGION_WORKSPACE="+filepath.Join(t.TempDir(), "workspace"))
			if got := g.start(shimwire.LauncherStart{ID: "start-1", Generation: 1, Argv: argv, Env: env}); !got.OK {
				t.Fatalf("start = %#v, want the child started", got)
			}
			exit := g.awaitExitWithin(1, 2*time.Minute)
			if exit.Session != tc.want {
				t.Fatalf("the child's exit = %+v, want its session refusal %q\nlauncher log:\n%s", exit, tc.want, g.stderr.String())
			}
			if tc.shell != "" && exit.Code != tc.code {
				t.Fatalf("the shell child %q exited %+v, want status %d", tc.shell, exit, tc.code)
			}
		})
	}
}

// awaitExitWithin reads the launcher's frames until it reports generation's exit, within bound: a
// real Oh My Pi that meets a store which closes on it can take ten seconds and more to give up.
func (g *roleConn) awaitExitWithin(generation uint64, bound time.Duration) shimwire.LauncherExit {
	g.t.Helper()
	deadline := time.Now().Add(bound)
	for g.latest.LastExit == nil || g.latest.LastExit.Generation != generation {
		if err := g.conn.SetReadDeadline(deadline); err != nil {
			g.t.Fatal(err)
		}
		line, err := g.reader.ReadBytes('\n')
		if err != nil {
			g.t.Fatalf("waiting %s for generation %d's exit: %v", bound, generation, err)
		}
		frame, err := shimwire.Decode(line)
		if err != nil {
			g.t.Fatal(err)
		}
		if state, ok := frame.(shimwire.LauncherState); ok {
			g.latest = state
		}
	}
	return *g.latest.LastExit
}

// importSession puts a session at path into the session table dsn names, as an earlier
// generation's Oh My Pi left it.
func importSession(t *testing.T, dsn, path string) {
	t.Helper()
	conn, err := ompsessions.ConnectURL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := ompsessions.Import(context.Background(), conn, path, []byte("{}\n"), time.Now()); err != nil {
		t.Fatal(err)
	}
}

// replaceURLFile replaces the session database URL file at file whole, as a rename does, so the
// launcher never reads half of it.
func replaceURLFile(file, dsn string) error {
	staged := file + ".staged"
	if err := os.WriteFile(staged, []byte(dsn+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(staged, file)
}

// refusingAddress is a TCP address nothing listens on any more, so a dial of it is refused by the
// host, as a database that is down refuses one.
func refusingAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// refusingURL is a session database URL whose server refuses the dial. It names a user and no
// password: the dial is refused before any login.
func refusingURL(t *testing.T) string {
	return "postgres://legion@" + refusingAddress(t) + "/none?sslmode=disable&connect_timeout=1"
}

// refusedDial is the error a real dial of refusingAddress gives, which the fake connector answers
// as the Cloud SQL connector answers an instance that refuses it.
func refusedDial(t *testing.T) error {
	t.Helper()
	conn, err := net.Dial("tcp", refusingAddress(t))
	if err == nil {
		_ = conn.Close()
		t.Fatal("a dial of an address nothing listens on was answered")
	}
	return err
}

// withPassword is dsn with its password replaced by password.
func withPassword(t *testing.T, dsn, password string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil {
		t.Fatalf("%s names no user whose password to replace: %v", dsn, err)
	}
	parsed.User = url.UserPassword(parsed.User.Username(), password)
	return parsed.String()
}

// readOnlyURLFile is a URL file naming a new database that takes no writes, as a replica does:
// every session on it starts read-only, so a write is refused with SQLSTATE 25006.
func readOnlyURLFile(t *testing.T) string {
	t.Helper()
	dsn := testdb.Database(t, "legion_launcher_read_only_test")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := ompsessions.ConnectURL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	name := strings.TrimPrefix(parsed.Path, "/")
	if _, err := conn.Exec(context.Background(), `ALTER DATABASE "`+name+`" SET default_transaction_read_only = on`); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "read-only-dsn")
	if err := replaceURLFile(file, dsn); err != nil {
		t.Fatal(err)
	}
	return file
}

// testdbFile is a URL file naming dsn, for a rig that reads the session table through one.
func testdbFile(t *testing.T, dsn string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "dsn")
	if err := replaceURLFile(file, dsn); err != nil {
		t.Fatal(err)
	}
	return file
}
