package tmux

// A session folder the tmux runtime cannot look in is a session store that did not answer, not a
// launch failure: the claim's machine keeps the session, charges nothing, and starts the same
// session again every minute until the folder answers (supervise's startWaitsForStore), as it does
// for a session database the Kubernetes launcher cannot reach.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/runtime/fake"
	"github.com/sjawhar/legion/daemon/internal/supervise"
	"github.com/sjawhar/legion/daemon/internal/supervise/supervisetest"
)

// The resume of a session a folder the runtime may not read could hold starts nothing and is
// runtime.ErrSessionStore: the claim waits launching, its session kept and no launch failure
// charged, and nothing is tried again before the minute is out. Once the folder answers, the
// minute's try resumes that same session, still charging nothing. The folder is the recorded
// session's own, or, for a session carried from where it was recorded, a folder of this daemon's
// own user that the search for it cannot list: nothing says the session is not in it.
func TestASessionFolderThatCannotBeReadIsWaitedOutAndTheSameSessionResumed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	const name = "2026-10-20T10-00-00-000Z_ses-abc.jsonl"
	for label, tc := range map[string]struct {
		// recorded is the claim's session file under sessions, and resumed the file a resume
		// continues it from once the folder answers; folder is the folder the runtime cannot read.
		recorded, resumed, folder string
	}{
		"the recorded session's own folder": {
			recorded: filepath.Join("--legion-workspace--", name), resumed: filepath.Join("--legion-workspace--", name),
			folder: "--legion-workspace--",
		},
		"a folder the search for a carried session cannot list": {
			recorded: filepath.Join("--pod-workspace--", name), resumed: filepath.Join("--legion-workspace--", name),
			folder: "--legion-workspace--",
		},
	} {
		t.Run(label, func(t *testing.T) {
			conns := fake.NewConns()
			f := newFakeServerRuntimeWith(t, staticCredential, nil, func(o *Options) {
				o.Conns = conns
				o.Environ = append(o.Environ, "HOME="+filepath.Join(o.StateDir, "home"))
			})
			spec := f.spec(t, claim.RoleTester)
			sessions := filepath.Join(t.TempDir(), "sessions")
			recorded, resumed, folder := filepath.Join(sessions, tc.recorded), filepath.Join(sessions, tc.resumed), filepath.Join(sessions, tc.folder)
			writeSession(t, resumed, time.Now())
			if err := os.Chmod(folder, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(folder, 0o700) })

			clock := &storeFaultClock{now: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
			suspended := supervise.Claim{Token: spec.Claim, Project: spec.Project, Tree: spec.Tree, Issue: spec.Issue, Role: spec.Role,
				Generation: 1, State: supervise.StateSuspended, SuspendedBy: supervise.SuspendedByOperator,
				Session: "ses-abc", SessionFile: recorded}
			m := storeFaultMachine(t, f, conns, spec, clock, suspended)

			err := m.Handle(context.Background(), supervise.RequestResume{Claim: spec.Claim})
			m.Wait()
			c := m.Claim()
			if !errors.Is(err, runtime.ErrSessionStore) || c.State != supervise.StateLaunching || c.Budgets.LaunchFailures != 0 ||
				c.Session != "ses-abc" || c.SessionFile != recorded {
				t.Fatalf("the resume over an unreadable session folder answered %v and left the claim %s with %d launch failures and session %q at %q, want runtime.ErrSessionStore, launching, none charged, ses-abc at %s kept",
					err, c.State, c.Budgets.LaunchFailures, c.Session, c.SessionFile, recorded)
			}
			if opened(f.argvs) {
				t.Fatalf("a resume whose session folder could not be read opened a pane: %q", f.argvs)
			}

			if err := os.Chmod(folder, 0o700); err != nil {
				t.Fatal(err)
			}
			clock.advance(m, time.Minute-time.Second)
			if opened(f.argvs) {
				t.Fatalf("the claim was started again before its minute was out: %q", f.argvs)
			}
			clock.advance(m, time.Second)
			if !opened(f.argvs) {
				t.Fatal("the minute's try opened no pane once the session folder answered")
			}
			if !slices.ContainsFunc(f.argvs, func(argv []string) bool {
				return verb(argv) == "new-window" && slices.ContainsFunc(argv, func(arg string) bool { return strings.Contains(arg, resumed) })
			}) {
				t.Errorf("the minute's try opened a pane that resumes no %s: %q", resumed, f.argvs)
			}
			c = m.Claim()
			if c.Locator == nil || c.Budgets.LaunchFailures != 0 || c.Session != "ses-abc" || c.SessionFile != recorded {
				t.Errorf("after the minute's try the claim is %+v, want it started on ses-abc, recorded at %s, with no launch failure charged", c, recorded)
			}
		})
	}
}

// storeFaultMachine is a claim's machine over f, the tmux runtime of a fake tmux server, from c as
// the store holds it: an in-memory store, spec as every launch's, and clock as its time.
func storeFaultMachine(t *testing.T, f *fakeServerRuntime, conns *fake.Conns, spec runtime.SpawnSpec, clock *storeFaultClock, c supervise.Claim) *supervise.Machine {
	t.Helper()
	m, err := supervise.NewMachine(context.Background(), supervise.Deps{
		Runtime: f.Runtime, Conns: conns, Store: &storeFaultStore{claim: c}, Specs: storeFaultSpecs{spec}, Clock: clock,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Limits: supervise.Limits{LaunchFailures: 3, PromptFailures: 1, PromptRetires: 1},
		Timeouts: supervise.Timeouts{
			Boot: time.Hour, RegistrationIntervals: 1, RPC: time.Hour, Probe: time.Hour, Stop: time.Hour, LaunchBackoff: time.Millisecond,
		},
		PluginContract: 24,
		RefreshGate:    supervise.NewRefreshGate(0),
	}, c)
	if err != nil {
		t.Fatalf("new machine: %v", err)
	}
	return m
}

// storeFaultSpecs builds every launch from one spec.
type storeFaultSpecs struct{ spec runtime.SpawnSpec }

func (s storeFaultSpecs) SpawnSpec(context.Context, supervise.Claim) (runtime.SpawnSpec, error) {
	return s.spec, nil
}

// storeFaultClock is time that moves only when the test advances it, firing each timer that falls
// due, in order, on the test's goroutine.
type storeFaultClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*storeFaultTimer
}

type storeFaultTimer struct {
	at            time.Time
	f             func()
	stopped, done bool
}

func (c *storeFaultClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *storeFaultClock) AfterFunc(d time.Duration, f func()) supervise.Cancel {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &storeFaultTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, timer)
	return storeFaultCancel{c, timer}
}

// Sleep returns at once: a launch's backoff costs the test nothing.
func (c *storeFaultClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// advance moves time forward by d, firing what falls due on the way, and waits for every decision
// a firing started.
func (c *storeFaultClock) advance(m *supervise.Machine, d time.Duration) {
	c.mu.Lock()
	until := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *storeFaultTimer
		for _, timer := range c.timers {
			if !timer.stopped && !timer.done && !timer.at.After(until) && (next == nil || timer.at.Before(next.at)) {
				next = timer
			}
		}
		if next == nil {
			c.now = until
			c.mu.Unlock()
			m.Wait()
			return
		}
		next.done, c.now = true, next.at
		c.mu.Unlock()
		next.f()
		m.Wait()
	}
}

type storeFaultCancel struct {
	clock *storeFaultClock
	timer *storeFaultTimer
}

func (s storeFaultCancel) Stop() bool {
	s.clock.mu.Lock()
	defer s.clock.mu.Unlock()
	if s.timer.stopped || s.timer.done {
		return false
	}
	s.timer.stopped = true
	return true
}

// storeFaultStore is supervise.Store over one claim, in memory.
type storeFaultStore struct {
	mu    sync.Mutex
	claim supervise.Claim
	supervisetest.Holds
}

func (s *storeFaultStore) AdmitClaim(_ context.Context, c supervise.Claim) (supervise.Claim, error) {
	return c, s.PutClaim(context.Background(), c)
}

func (*storeFaultStore) CheckLaunch(context.Context, supervise.Claim) error { return nil }

func (s *storeFaultStore) PutClaim(_ context.Context, c supervise.Claim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claim = c
	return nil
}

func (s *storeFaultStore) ReleaseHolds(ctx context.Context, c supervise.Claim, holds []supervise.Hold) ([]supervise.Hold, error) {
	return s.Holds.ReleaseWith(ctx, s.PutClaim, c, holds)
}

func (*storeFaultStore) PutDelivery(context.Context, claim.Token, supervise.Delivery) error {
	return nil
}

func (s *storeFaultStore) PutClaimAndDelivery(ctx context.Context, c supervise.Claim, _ supervise.Delivery) error {
	return s.PutClaim(ctx, c)
}

func (s *storeFaultStore) RetireDelivery(ctx context.Context, c supervise.Claim, _ string) error {
	return s.PutClaim(ctx, c)
}

func (s *storeFaultStore) PutClaimStep(_ context.Context, token claim.Token, step supervise.StepRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claim.Token != token {
		return false, nil
	}
	s.claim.Step = step
	return true, nil
}
