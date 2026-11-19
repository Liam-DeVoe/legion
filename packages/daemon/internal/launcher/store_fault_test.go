package launcher

// A start whose session store did not answer says so in its answer's own field
// (shimwire.SessionStoreUnreachable), which the daemon reads as a store fault: the claim keeps its
// session, is charged nothing, and is started again a minute later (runtime/sandbox's
// startRefusal, supervise's startWaitsForStore). These are the faults of the store a start meets
// past the resume's own lookup: the generation's proxy to a session database of the pod's own, and
// a Claude Code session's keeper reconnecting to the table and restoring the session's files from
// it. Each start below meets the fault once, and the daemon's next start of the same session, once
// the store answers, resumes it.

import (
	"context"
	"errors"
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
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
)

// A generation whose proxy cannot reach the session database, because its connector cannot be
// built or its proof dial is refused or never answered, is refused as a session store that did not
// answer, having started nothing. The next start of the same session, once the database answers,
// runs.
func TestAProxyThatCannotReachTheSessionDatabaseIsAStoreFaultAndTheNextStartResumes(t *testing.T) {
	for name, fault := range map[string]struct {
		build, dial, stall bool
	}{
		"the connector cannot be built":  {build: true},
		"the proof dial is refused":      {dial: true},
		"the proof dial is not answered": {stall: true},
	} {
		t.Run(name, func(t *testing.T) {
			dsn := testdb.Database(t, "legion_launcher_proxy_fault_test")
			database, dialer := sessionDatabaseAt(t, dsn)
			shortenResumeLookup(t)
			var down atomic.Bool
			down.Store(true)
			dialer.check = func() error {
				if fault.dial && down.Load() {
					return errors.New("dial tcp 10.0.0.3:3307: connect: connection refused")
				}
				return nil
			}
			build := func(ctx context.Context, account string) (sessionproxy.Dialer, error) {
				if fault.build && down.Load() {
					return nil, errors.New("google: could not find default credentials")
				}
				return dialer.build(ctx, account)
			}
			cfg := Config{SessionDatabase: database, SessionDialer: build}
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

			if got := start(3); got.OK || got.Session != shimwire.SessionStoreUnreachable || !strings.Contains(got.Error, "generation 3") {
				t.Fatalf("start over a proxy that cannot reach the session database = %#v, want a refusal of generation 3 as %q", got, shimwire.SessionStoreUnreachable)
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

// A Claude Code resume whose keeper cannot ready the table after the resume's lookup found the
// session, or cannot read the session's rows back to restore its files, is refused as a session
// store that did not answer, having started nothing. The table that cannot take the keeper's
// writes is a database that answers only reads, as a primary does while it fails over; the rows
// that cannot be read are ones another transaction holds locked past the start's bound. The next
// start of the same session, once the store answers, restores the transcript the table holds and
// starts its child on it.
func TestAClaudeKeeperThatCannotReachTheTableIsAStoreFaultAndTheNextStartRestores(t *testing.T) {
	for name, fault := range map[string]struct {
		// refusal is what the start's refusal names of the step that met the fault.
		refusal func(c *claudeRig) string
		// fail puts the fault in place and returns its end.
		fail func(t *testing.T, c *claudeRig, dsn string) (end func())
	}{
		"the keeper's reconnection": {
			refusal: func(*claudeRig) string { return "keep Claude Code session " + claudeSession },
			fail: func(t *testing.T, c *claudeRig, dsn string) func() {
				readOnly, err := url.Parse(dsn)
				if err != nil {
					t.Fatal(err)
				}
				// pgx hands Postgres a + in the URL's options as it stands, so the space is %20.
				query := readOnly.Query()
				query.Set("options", "-c default_transaction_read_only=on")
				readOnly.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
				replaceURLFile(t, c.dsnFile, readOnly.String())
				return func() { replaceURLFile(t, c.dsnFile, dsn) }
			},
		},
		"the restore of the session's files": {
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
			got := c.start(c.command(1, seeing(c.transcript, told), true))
			end()
			if want := fault.refusal(c); got.OK || got.Session != shimwire.SessionStoreUnreachable || !strings.Contains(got.Error, want) {
				t.Fatalf("a Claude Code resume whose store did not answer = %#v, want a refusal naming %q as %q", got, want, shimwire.SessionStoreUnreachable)
			}
			if _, err := os.Stat(told); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("a Claude Code resume whose store did not answer started its child")
			}

			_ = os.RemoveAll(filepath.Join(c.configDir, "projects"))
			if got := c.start(c.command(2, seeing(c.transcript, told), true)); !got.OK {
				t.Fatalf("the next start of the same session once the store answers = %#v, want generation 2 to run", got)
			}
			if found := seen(t, told); found != saved {
				t.Errorf("generation 2's child found the transcript %q at its start, want the %q the table holds", found, saved)
			}
			c.stop(2)
		})
	}
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
func replaceURLFile(t *testing.T, file, dsn string) {
	t.Helper()
	staged := file + ".staged"
	if err := os.WriteFile(staged, []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, file); err != nil {
		t.Fatal(err)
	}
}
