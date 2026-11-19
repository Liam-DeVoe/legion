package sandbox

// A session store that stops answering a Kubernetes start, at its proof dial or on the pinned Oh
// My Pi's own connection after the start's checks passed, is waited out a minute at a time: the
// claim keeps its session, is charged nothing, and the next minute's try, once the store answers,
// launches the same session. The test drives the whole chain: a claim's machine on a clock of the
// test's own, this runtime over the rig's fake cluster, a real `legion launcher` in each pod the
// rig makes (launcher.Run), its real session proxy to the test's Postgres, and the real Oh My Pi.
// The one stand-in is the pod's file system: the relay between the runtime and the launcher points
// the start's argv and environment at the pinned binary and the launcher's own directory where the
// runtime names a pod's paths.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/launcher"
	"github.com/sjawhar/legion/daemon/internal/ompsessions"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/sessionproxy"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/supervise/supervisetest"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/testlog"
	"github.com/sjawhar/legion/daemon/internal/testpg/testdb"
	"github.com/sjawhar/legion/daemon/internal/testtmp"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

func TestAKubernetesStartWhoseSessionStoreStopsAnsweringIsWaitedOutAMinuteAndResumesTheSameSession(t *testing.T) {
	omp := testbin.OMP(t)
	for name, tc := range map[string]struct {
		// fault puts the store's fault in place on d before the first start.
		fault func(d *storeDialer)
		// childExits is whether the fault is the child's own: the start succeeds, and the child
		// exits on the store.
		childExits bool
		// stopGrace is the launcher's, which bounds its proof dial at half of it.
		stopGrace time.Duration
	}{
		"the proof dial is refused":                         {fault: func(d *storeDialer) { d.refuseFrom.Store(1) }},
		"the proof dial is not answered":                    {fault: func(d *storeDialer) { d.stall.Store(true) }, stopGrace: 2 * time.Second},
		"the lookup is refused after the proof dial":        {fault: func(d *storeDialer) { d.refuseFrom.Store(2) }},
		"the child's own connection is refused":             {fault: func(d *storeDialer) { d.refuseFrom.Store(3) }, childExits: true},
		"control: the child exits with the store answering": {fault: func(*storeDialer) {}, childExits: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStoreFaultRig(t, omp, tc.stopGrace)
			tc.fault(s.dialer)
			control := strings.HasPrefix(name, "control:")

			err := s.m.Handle(s.g.ctx, supervise.RequestResume{Claim: s.spec.Claim})
			s.m.Wait()
			if tc.childExits {
				if err != nil {
					t.Fatalf("the resume answered %v, want the child started", err)
				}
				loc := s.m.Claim().Locator
				if loc == nil {
					t.Fatal("the resume recorded no process")
				}
				obs := s.awaitExit(t, *loc)
				if obs.StoreFault == control {
					t.Fatalf("the child's exit was observed %+v, want StoreFault %t", obs, !control)
				}
				err = s.m.Handle(s.g.ctx, supervise.RuntimeObservation{Observation: obs})
				s.m.Wait()
			}
			c := s.m.Claim()
			if control {
				if c.Budgets.LaunchFailures != 1 || len(s.faults()) != 0 {
					t.Fatalf("a child that exited with the store answering left the claim %s with %d launch failures and %d store faults told, want the death charged once and no store fault",
						c.State, c.Budgets.LaunchFailures, len(s.faults()))
				}
				return
			}
			if !errors.Is(err, runtime.ErrSessionStore) && !tc.childExits {
				t.Fatalf("the start over a store that did not answer answered %v, want runtime.ErrSessionStore", err)
			}
			if c.State != supervise.StateLaunching || c.Locator != nil || c.Budgets.LaunchFailures != 0 ||
				c.Session != storeFaultSession || c.SessionFile != storeFaultSessionFile || len(s.faults()) != 1 {
				t.Fatalf("after the store fault the claim is %s, locator %v, %d launch failures, session %q at %q, %d store faults told; want launching with no process, nothing charged, %s at %s kept, the fault told once",
					c.State, c.Locator, c.Budgets.LaunchFailures, c.Session, c.SessionFile, len(s.faults()), storeFaultSession, storeFaultSessionFile)
			}
			starts := s.relay.startCount()

			s.dialer.answer()
			s.clock.Advance(s.m, time.Minute-time.Second)
			if got := s.relay.startCount(); got != starts {
				t.Fatalf("the claim was started again before its minute was out: %d starts, had %d", got, starts)
			}
			s.clock.Advance(s.m, time.Second)
			c = s.m.Claim()
			if c.Locator == nil || c.Budgets.LaunchFailures != 0 || c.Session != storeFaultSession {
				t.Fatalf("after the minute's try the claim is %s, locator %v, %d launch failures, session %q; want it launched on %s with nothing charged",
					c.State, c.Locator, c.Budgets.LaunchFailures, c.Session, storeFaultSession)
			}
			if last := s.relay.lastStart(); last.ResumeFile != storeFaultSessionFile {
				t.Errorf("the minute's start resumes %q, want the same session %s", last.ResumeFile, storeFaultSessionFile)
			}
		})
	}
}

// A Kubernetes start whose session store is configured wrong, a proxy whose credential is not
// there, is charged as the launch failure it is, naming the credential, until the claim's launch
// budget runs out, and no store fault is told.
func TestAKubernetesStartWhoseProxyCredentialIsNotThereIsChargedNamingIt(t *testing.T) {
	omp := testbin.OMP(t)
	s := newStoreFaultRig(t, omp, 0, func(cfg *launcher.Config) {
		cfg.SessionDialer = func(context.Context, string) (sessionproxy.Dialer, error) {
			return nil, errors.New("google: could not find default credentials")
		}
	})
	err := s.m.Handle(s.g.ctx, supervise.RequestResume{Claim: s.spec.Claim})
	s.m.Wait()
	c := s.m.Claim()
	if errors.Is(err, runtime.ErrSessionStore) || c.State != supervise.StateFailed || c.Budgets.LaunchFailures != 3 ||
		!strings.Contains(c.LastStartFailure, "could not find default credentials") || len(s.faults()) != 0 {
		t.Fatalf("the resume answered %v and left the claim %s with %d launch failures, last start failure %q, %d store faults told; want it failed after 3 charged launch failures naming the credential, no store fault",
			err, c.State, c.Budgets.LaunchFailures, c.LastStartFailure, len(s.faults()))
	}
}

// The session the store-fault tests' claim records, and the file it is recorded under: any key
// the table holds, since the relay starts the real Oh My Pi fresh beside the launcher's own
// lookup of it.
const (
	storeFaultSession     = "ses-tester"
	storeFaultSessionFile = "/legion/scratch/state/tester/.omp/profiles/legion/agent/sessions/--legion-workspaces-legion-208--/2026-11-19T12-00-00-000Z_ses-tester.jsonl"
)

// storeFaultRig is one suspended Oh My Pi claim of the rig's worker, its machine on a clock of the
// test's own, and the real launcher its pods run.
type storeFaultRig struct {
	g      *rig
	spec   runtime.SpawnSpec
	m      *supervise.Machine
	clock  *supervisetest.Clock
	dialer *storeDialer
	relay  *realLauncher

	mu   sync.Mutex
	told []string
}

func (s *storeFaultRig) faults() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.told)
}

func newStoreFaultRig(t *testing.T, omp string, stopGrace time.Duration, edits ...func(*launcher.Config)) *storeFaultRig {
	t.Helper()
	dsn := testdb.Database(t, "legion_sandbox_store_fault_test")
	database, address := sessionDatabaseOf(t, dsn)
	seedSession(t, dsn, storeFaultSessionFile)
	home := t.TempDir()
	testbin.OMPHome(t, omp, home)
	testbin.WriteOMPProfile(t, home, "providers:\n  operator:\n    baseUrl: http://127.0.0.1:9\n    auth: apiKey\n    api: anthropic-messages\n"+
		"    apiKey: unused\n    models:\n      - id: store-fault-test\n        name: Store fault test\n", "modelRoles:\n  default: operator/store-fault-test\n")

	g := newRig(t, nil, withOptions(func(o *Options) {
		o.SessionStore = ompsessions.Store{Kind: ompsessions.StoreCloudSQLIAM, Database: database}
		// A real launcher's start reaches the real database and the real Oh My Pi, which a
		// loaded machine is slow to: the runtime's wait on it is not this test's subject.
		o.BootTimeout = 2 * time.Minute
	}))
	spec := workerSpec(t)
	dialer := &storeDialer{address: address}
	cfg := launcher.Config{StopGrace: 10 * time.Second, SessionDatabase: database, SessionDialer: dialer.build}
	if stopGrace > 0 {
		cfg.StopGrace = stopGrace
	}
	for _, edit := range edits {
		edit(&cfg)
	}
	relay := g.realLauncher(t, spec.Claim, cfg, func(l *realLauncher, start shimwire.LauncherStart) shimwire.LauncherStart {
		dsnFile := filepath.Join(l.privateDir(), shimwire.GenerationDir(start.Generation), ompsessions.DSNFileName)
		if !slices.Contains(start.Env, ompsessions.StorageVariable+"="+ompsessions.SQLStorage) ||
			!slices.ContainsFunc(start.Env, func(pair string) bool {
				return strings.HasPrefix(pair, ompsessions.DSNFileVariable+"=") && strings.HasSuffix(pair, "/"+shimwire.GenerationDir(start.Generation)+"/"+ompsessions.DSNFileName)
			}) {
			t.Errorf("the start of generation %d names no SQL storage at its generation's URL file: %q", start.Generation, start.Env)
		}
		start.Argv = []string{omp, "--mode", "rpc"}
		start.Env = append(testbin.OMPEnviron(home), ompsessions.StorageVariable+"="+ompsessions.SQLStorage,
			ompsessions.DSNFileVariable+"="+dsnFile, "LEGION_WORKSPACE="+filepath.Join(home, "workspace"))
		return start
	})
	s := &storeFaultRig{g: g, spec: spec, clock: supervisetest.NewClock(time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)), dialer: dialer, relay: relay}
	suspended := supervise.Claim{Token: spec.Claim, Project: spec.Project, Tree: spec.Tree, Issue: spec.Issue, Role: spec.Role,
		Generation: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByOperator,
		Session: storeFaultSession, SessionFile: storeFaultSessionFile}
	s.m, _, _ = clockedSandboxMachine(t, g, spec, suspended, io.Discard, s.clock, func(_ supervise.Claim, detail string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.told = append(s.told, detail)
	})
	g.launcher(spec.Claim)
	return s
}

// awaitExit is the runtime's observation of the process loc records once its launcher reports that
// process's exit: a real Oh My Pi that a store closes on can take thirty seconds to give up.
func (s *storeFaultRig) awaitExit(t *testing.T, loc runtime.Locator) runtime.Observation {
	t.Helper()
	if !testwait.Holds(2*time.Minute, testwait.Poll, func() bool {
		state, connected := s.g.r.launchers.state(s.spec.Claim, loc.Sandbox.PodUID)
		return connected && state.LastExit != nil && state.LastExit.Generation == loc.Sandbox.Generation
	}) {
		t.Fatalf("generation %d never exited; the launcher's log:\n%s", loc.Sandbox.Generation, s.relay.log())
	}
	obs, err := s.g.r.Probe(s.g.ctx, loc)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return obs
}

// sessionDatabaseOf is the test Postgres at dsn as the cloudsql-iam store's database a launcher
// proxies to, with the address its upstream dialer reaches it at. The login is the test database's
// own, whose password goes into the environment as libpq's PGPASSWORD, where the launcher's own
// client and the real Oh My Pi's both read it: the URL the launcher builds for the proxy's socket
// carries none, as an IAM login's does not.
func sessionDatabaseOf(t *testing.T, dsn string) (ompsessions.Database, string) {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	t.Setenv("PGPASSWORD", password)
	address := parsed.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "5432")
	}
	return ompsessions.Database{Instance: "example-project:us-central1:example-instance", Name: strings.TrimPrefix(parsed.Path, "/"),
		User: parsed.User.Username()}, address
}

// seedSession makes Oh My Pi's tables at dsn and puts a session at path in them, as an earlier
// generation left it.
func seedSession(t *testing.T, dsn, path string) {
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

// storeDialer stands in for the Cloud SQL connector: each dial reaches the test's Postgres at
// address, unless refuseFrom names a dial from which on every dial is refused by the host, or stall
// holds every dial until its context ends, as an instance that never answers does. The dials are
// counted from 1: a resume's are the proof dial, the launcher's own lookup, then the child's.
type storeDialer struct {
	address    string
	refuseFrom atomic.Int32
	stall      atomic.Bool
	dials      atomic.Int32
}

func (d *storeDialer) Dial(ctx context.Context, _ string) (net.Conn, error) {
	n := d.dials.Add(1)
	if d.stall.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if from := d.refuseFrom.Load(); from > 0 && n >= from {
		return refusedDial()
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", d.address)
}

func (*storeDialer) Close() error { return nil }

func (d *storeDialer) build(context.Context, string) (sessionproxy.Dialer, error) { return d, nil }

// answer has every dial from now on reach the database.
func (d *storeDialer) answer() {
	d.refuseFrom.Store(0)
	d.stall.Store(false)
}

// refusedDial is a real dial of a TCP address nothing listens on any more, which the host refuses.
func refusedDial() (net.Conn, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	_ = listener.Close()
	conn, err := net.Dial("tcp", address)
	if err == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("a dial of %s, where nothing listens, was answered", address)
	}
	return nil, err
}

// realLauncher is a real `legion launcher` (launcher.Run) for one role, started for every pod the
// rig's controller makes for its issue, and the relay between this runtime and it, through which
// the runtime's launcher_start frames pass rewritten (rewrite) and every other frame as it is.
type realLauncher struct {
	t       *testing.T
	g       *rig
	token   claim.Token
	cfg     launcher.Config
	rewrite func(*realLauncher, shimwire.LauncherStart) shimwire.LauncherStart
	root    string
	logs    *testlog.Buffer

	mu      sync.Mutex
	next    int
	private string
	stop    context.CancelFunc
	done    chan struct{}
	starts  []shimwire.LauncherStart
}

// realLauncher has every pod of token's issue run a real launcher for token's role, configured as
// cfg, whose start frames pass through rewrite.
func (g *rig) realLauncher(t *testing.T, token claim.Token, cfg launcher.Config,
	rewrite func(*realLauncher, shimwire.LauncherStart) shimwire.LauncherStart) *realLauncher {
	t.Helper()
	// Each launcher's private directory holds its generations' session sockets, whose paths a unix
	// socket bounds (docs/testing.md): the root is short enough for the longest of them.
	root := testtmp.Dir(t, filepath.Join("99", shimwire.GenerationDir(999), ompsessions.SocketName))
	l := &realLauncher{t: t, g: g, token: token, cfg: cfg, rewrite: rewrite, root: root, logs: &testlog.Buffer{}}
	g.commandsMu.Lock()
	if g.real == nil {
		g.real = map[claim.Token]func(*corev1.Pod){}
	}
	g.real[token] = l.podReady
	g.commandsMu.Unlock()
	t.Cleanup(l.end)
	return l
}

// podReady starts the launcher of pod, ending the previous pod's: a pod's launcher is its own.
func (l *realLauncher) podReady(pod *corev1.Pod) {
	l.end()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(l.logs, "listen for the launcher of pod %s: %v\n", pod.Name, err)
		return
	}
	l.mu.Lock()
	l.next++
	private := filepath.Join(l.root, fmt.Sprint(l.next))
	l.mu.Unlock()
	if err := os.MkdirAll(private, 0o700); err != nil {
		fmt.Fprintf(l.logs, "make the launcher's private directory: %v\n", err)
		_ = listener.Close()
		return
	}
	cfg := l.cfg
	cfg.Connect, cfg.Token, cfg.Sandbox, cfg.Role = "tcp://"+listener.Addr().String(), "launcher-token", pod.Name, string(l.role())
	cfg.PodUID, cfg.PrivateDir, cfg.Stdout, cfg.Stderr = string(pod.UID), private, l.logs, l.logs
	ctx, cancel := context.WithCancel(l.g.ctx)
	done := make(chan struct{})
	l.mu.Lock()
	l.private, l.stop, l.done = private, cancel, done
	l.mu.Unlock()
	go func() {
		defer close(done)
		defer listener.Close()
		go l.accept(listener, pod)
		if err := launcher.Run(ctx, cfg); err != nil && ctx.Err() == nil {
			fmt.Fprintf(l.logs, "launcher of pod %s: %v\n", pod.Name, err)
		}
	}()
}

func (l *realLauncher) role() claim.Role {
	_, role, _ := l.token.Cut()
	return role
}

// accept takes each connection the launcher dials, answers its hello as the worker stream's
// listener does once the runtime's resolver accepts it, and relays it to the runtime.
func (l *realLauncher) accept(listener net.Listener, pod *corev1.Pod) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		reader := bufio.NewReader(conn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			_ = conn.Close()
			continue
		}
		frame, err := shimwire.Decode(line)
		hello, ok := frame.(shimwire.LauncherHello)
		if err != nil || !ok {
			_ = conn.Close()
			continue
		}
		toLauncher := shimwire.NewWriter(conn)
		if err := toLauncher.WriteFrame(shimwire.LauncherHelloAck{}); err != nil {
			_ = conn.Close()
			continue
		}
		session := l.g.r.launchers.accept(l.token, launcherIssue(l.token), pod.CreationTimestamp.Time, hello)
		daemonEnd, relayEnd := net.Pipe()
		go session.ServeLauncher(daemonEnd, bufio.NewReader(daemonEnd), shimwire.NewWriter(daemonEnd))
		go func() {
			_, _ = io.Copy(relayEnd, reader)
			_ = relayEnd.Close()
		}()
		go func() {
			defer conn.Close()
			fromDaemon := bufio.NewReader(relayEnd)
			for {
				line, err := fromDaemon.ReadBytes('\n')
				if err != nil {
					return
				}
				frame, err := shimwire.Decode(line)
				if err != nil {
					return
				}
				if start, ok := frame.(shimwire.LauncherStart); ok {
					l.mu.Lock()
					l.starts = append(l.starts, start)
					l.mu.Unlock()
					frame = l.rewrite(l, start)
				}
				if err := toLauncher.WriteFrame(frame); err != nil {
					return
				}
			}
		}()
	}
}

// end stops the running launcher, if any, and waits for it.
func (l *realLauncher) end() {
	l.mu.Lock()
	stop, done := l.stop, l.done
	l.stop, l.done = nil, nil
	l.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

func (l *realLauncher) privateDir() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.private
}

// startCount is how many launcher_start frames the runtime sent, and lastStart the last of them
// as the runtime sent it.
func (l *realLauncher) startCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.starts)
}

func (l *realLauncher) lastStart() shimwire.LauncherStart {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.starts) == 0 {
		return shimwire.LauncherStart{}
	}
	return l.starts[len(l.starts)-1]
}

func (l *realLauncher) log() string { return l.logs.String() }
