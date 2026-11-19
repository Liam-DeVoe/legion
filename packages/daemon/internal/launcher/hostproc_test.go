package launcher

import (
	"os"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/hostproc"
	"github.com/sjawhar/legion/daemon/internal/hostproc/hostproctest"
)

// sweepScanner and reaperScanner are the names hostproc's refusal gives the launcher's two
// listings of a /proc.
const (
	sweepScanner  = "the launcher's git lock sweep"
	reaperScanner = "the launcher's reaper"
)

// Each listing of a /proc the launcher makes refuses the machine's in a test that gave it no /proc
// of its own: the sweep's holder read, the reaper's pass and role-init's pass. A test process is
// never PID 1 of the machine's PID namespace, so none of them is the namespace's own listing.
func TestEachLauncherListingRefusesTheMachinesProc(t *testing.T) {
	discard := func(string, ...any) {}
	t.Run("the git lock sweep, built as the launcher builds it", func(t *testing.T) {
		hostproctest.Refused(t, sweepScanner, func() {
			_, _ = newGitLockSweep(discard).holders(nil)
		})
	})
	t.Run("the reaper's pass", func(t *testing.T) {
		hostproctest.Refused(t, reaperScanner, func() { _, _ = (&pidGuard{}).reap() })
	})
	t.Run("the reaper's signal of the namespace", func(t *testing.T) {
		hostproctest.Refused(t, reaperScanner, func() {
			g := &pidGuard{pids: map[int]bool{os.Getpid(): true}}
			_ = g.signalNamespace(0)
		})
	})
	t.Run("role-init's pass", func(t *testing.T) {
		hostproctest.Refused(t, "role-init's reaper", func() { _, _ = reapNamespace() })
	})
	// The control: the same sweep and reaper over a /proc of the test's own list it.
	t.Run("the same listings over a /proc of the test's own", func(t *testing.T) {
		hostproctest.Allowed(t, func() {
			useGitLockProc(t, procRoot(t, os.Getpid()))
			if _, err := newGitLockSweep(discard).holders(nil); err != nil {
				t.Fatal(err)
			}
			if _, err := (&pidGuard{proc: procRoot(t, os.Getpid())}).reap(); err != nil {
				t.Fatal(err)
			}
		})
	})
}

// A sweep is judged by the /proc it was built with. Pointing the package's default at a /proc of
// the test's own after the sweep is built does not give that sweep one, and pointing it back at the
// machine's after a sweep was built over the test's own takes nothing from that sweep.
func TestASweepIsJudgedByTheRootItWasBuiltWith(t *testing.T) {
	discard := func(string, ...any) {}
	t.Run("a default changed after the build", func(t *testing.T) {
		hostproctest.Refused(t, sweepScanner, func() {
			s := newGitLockSweep(discard)
			useGitLockProc(t, procRoot(t, os.Getpid()))
			_, _ = s.holders(nil)
		})
	})
	t.Run("a default changed back after the build", func(t *testing.T) {
		hostproctest.Allowed(t, func() {
			useGitLockProc(t, procRoot(t, os.Getpid()))
			s := newGitLockSweep(discard)
			useGitLockProc(t, "/proc")
			if _, err := s.holders(nil); err != nil {
				t.Fatal(err)
			}
		})
	})
}

// A reason is the one sweep's it is given to: a second sweep of the machine's /proc in the same test,
// before or after the reasoned one, is refused, and so is the second of two sibling subtests whose
// first holds a reason, whichever of the two names sorts first.
func TestASweepsReasonAuthorizesNoOtherSweep(t *testing.T) {
	reasoned := func() *gitLockSweep {
		return &gitLockSweep{proc: "/proc", hostProc: hostproc.Because("the row reads its own pid alone")}
	}
	bare := func() *gitLockSweep { return &gitLockSweep{proc: "/proc"} }
	t.Run("the reasoned sweep first", func(t *testing.T) {
		hostproctest.Refused(t, sweepScanner, func() {
			_, _ = reasoned().holders(nil)
			_, _ = bare().holders(nil)
		})
	})
	t.Run("the reasoned sweep second", func(t *testing.T) {
		hostproctest.Refused(t, sweepScanner, func() {
			_, _ = bare().holders(nil)
			_, _ = reasoned().holders(nil)
		})
	})
	for _, names := range [][2]string{{"a", "b"}, {"b", "a"}} {
		for _, reasonedFirst := range []bool{true, false} {
			first, second := names[0], names[1]
			t.Run("siblings "+first+" then "+second+", reasoned first "+map[bool]string{true: "yes", false: "no"}[reasonedFirst], func(t *testing.T) {
				hostproctest.Refused(t, sweepScanner, func() {
					t.Run(first, func(*testing.T) {
						if reasonedFirst {
							_, _ = reasoned().holders(nil)
						} else {
							_, _ = bare().holders(nil)
						}
					})
					t.Run(second, func(*testing.T) {
						if reasonedFirst {
							_, _ = bare().holders(nil)
						} else {
							_, _ = reasoned().holders(nil)
						}
					})
				})
			})
		}
	}
}

// The refusal holds in every helper child this package re-executes its test binary as. TestMain
// returns before its own guard for each of them (helperChildVariables), and the refusal needs no
// setup there: it is every scanner's own, in any test binary.
func TestAHelperChildIsRefusedTheMachinesProc(t *testing.T) {
	for _, marker := range helperChildVariables {
		t.Run(marker, func(t *testing.T) {
			t.Setenv(marker, "1")
			hostproctest.Refused(t, sweepScanner, func() {
				if os.Getenv(marker) != "1" {
					t.Fatalf("the child holds no %s, so TestMain did not take its helper branch", marker)
				}
				_, _ = (&gitLockSweep{proc: "/proc"}).holders(nil)
			})
		})
	}
}
