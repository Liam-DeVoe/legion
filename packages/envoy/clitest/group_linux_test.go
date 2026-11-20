//go:build linux

package clitest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// leaveASleepBehind is the run these tests make: a shell that leaves a `sleep 30` of its group
// behind, so signal 0 to the group reaches a live process and the run reads the process table for
// one, writes the group's id and the sleep's pid to $2, and, with a state in $3, plants a stat of a
// member of its group in that state under the planted table $1 before it exits 0.
const leaveASleepBehind = `sleep 30 >/dev/null 2>&1 &
printf '%s %s\n' "$$" "$!" > "$2"
if [ -n "$3" ]; then
	mkdir "$1/$$" && printf '%s (sh) %s 1 %s %s 0 -1\n' "$$" "$3" "$$" "$$" > "$1/$$/stat"
fi
exit 0`

// groupBound is the run's bound in these tests: long enough for a shell to start and exit on a
// loaded machine, so that a run ends at it only because of what it waits for after the exit.
const groupBound = 10 * time.Second

// watchdogMargin is how long past groupBound a test waits for a run before it calls the run held:
// a run that ends at its bound returns within a scheduling delay of it, and one a read holds never
// returns.
const watchdogMargin = 30 * time.Second

// A run's binary that leaves a process behind is waited for, after it exits, by reads of the
// process table for a live member of its group. Each read is judged by the member's state, and a
// read that never answers, as one of a FIFO that no writer opens does, holds the run no longer
// than its bound: the run ends at runTimeout, kills its group, and its report names the bound and
// the walk that had not ended. A member that runs is reported as one at the bound, as the last
// walk that ended read it; a zombie member, a dead one, or no member of the group means the group
// has ended, so the run ends at once.
func TestARunWaitsForItsGroupByEachMembersStateWithinItsBound(t *testing.T) {
	before := runTimeout
	runTimeout = groupBound
	t.Cleanup(func() { runTimeout = before })
	for _, tc := range []struct {
		name  string
		state string
		fifo  bool
		want  string // the report's failed text, with ROOT for the planted table
	}{
		{name: "a stat that never answers", fifo: true,
			want: "exited, but at the run's bound (runTimeout), 10s after it began, no walk of ROOT for a live process of its group had ended"},
		{name: "a member that runs", state: "S",
			want: "exited, but at the run's bound (runTimeout), 10s after it began, a process it started was still running when the process table was last read"},
		{name: "a zombie member", state: "Z"},
		{name: "a dead member", state: "X"},
		{name: "no member", state: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, scratch := t.TempDir(), t.TempDir()
			if tc.fifo {
				stat := plantFIFOStat(t, root, "4242")
				// Opening the FIFO's writing end without waiting for a reader, then closing it,
				// gives a read blocked there its end of file, so the walk left behind ends with
				// the test.
				t.Cleanup(func() {
					if writer, err := os.OpenFile(stat, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
						_ = writer.Close()
					}
				})
			}
			before := procRoot
			procRoot = root
			t.Cleanup(func() { procRoot = before })
			groupFile := filepath.Join(scratch, "group")
			h := shellHarness(t)
			began := time.Now()
			ran := make(chan result, 1)
			go func() { ran <- h.run("-c", leaveASleepBehind, "sh", root, groupFile, tc.state) }()
			var r result
			select {
			case r = <-ran:
			case <-time.After(groupBound + watchdogMargin):
				t.Fatalf("the run was still waiting %s after it began, %s past its bound (runTimeout): a read of the process table at %s held it",
					time.Since(began).Round(time.Millisecond), watchdogMargin, root)
			}
			took := time.Since(began)
			group, sleep := killGroupAtCleanup(t, groupFile)
			want := strings.ReplaceAll(tc.want, "ROOT", root)
			if r.failed != want {
				t.Fatalf("the run's report says %q, want %q%s", r.failed, want, r.output())
			}
			if want == "" {
				if r.code != 0 {
					t.Fatalf("the run exited %d, want 0%s", r.code, r.output())
				}
				if took >= groupBound {
					t.Fatalf("the run took %s, its whole bound, though the planted table holds no live member of its group", took.Round(time.Millisecond))
				}
				return
			}
			// The run that ends at its bound kills what its group still holds, as its report says:
			// the sleep the shell left there ends, which its own stat shows, as a zombie where
			// nothing reaps it.
			deadline := time.Now().Add(watchdogMargin)
			for !endedByState(t, sleep) {
				if time.Now().After(deadline) {
					t.Fatalf("the sleep %d of group %d still runs %s after its run ended at its bound, which kills the group", sleep, group, watchdogMargin)
				}
				time.Sleep(groupPoll)
			}
		})
	}
}

// A process the run's binary leaves behind that exits is a member that has ended, whether its
// parent reaps it or not: where the namespace's PID 1 is a `go test`, which reaps nothing, its
// zombie stays, and the real /proc shows it in state Z. The run then ends well within its bound.
func TestARunCountsALeftBehindProcessThatExitedAsEnded(t *testing.T) {
	before := runTimeout
	runTimeout = groupBound
	t.Cleanup(func() { runTimeout = before })
	h := shellHarness(t)
	began := time.Now()
	r := h.run("-c", `sleep 0.2 >/dev/null 2>&1 & exit 0`)
	if took := time.Since(began); r.failed != "" || r.code != 0 || took >= groupBound {
		t.Fatalf("the run took %s, exit %d, report %q; want exit 0 well within its %s bound%s", took.Round(time.Millisecond), r.code, r.failed, groupBound, r.output())
	}
}

// shellHarness is a harness whose binary is sh, with the environment and directory a run gets.
func shellHarness(t *testing.T) *harness {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return &harness{
		bin:    sh,
		name:   "sh",
		dir:    home,
		env:    []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir()},
		server: NewRecorder(t),
	}
}

// plantFIFOStat makes root/pid/stat a FIFO no writer opens, a stat whose read never answers, and
// answers its path.
func plantFIFOStat(t *testing.T, root, pid string) string {
	t.Helper()
	if err := os.Mkdir(filepath.Join(root, pid), 0o700); err != nil {
		t.Fatal(err)
	}
	stat := filepath.Join(root, pid, "stat")
	if err := syscall.Mkfifo(stat, 0o600); err != nil {
		t.Fatal(err)
	}
	return stat
}

// killGroupAtCleanup reads the group id and the sleep's pid the run's shell wrote to path, kills
// that group when t ends, so the sleep it left behind does not outlive the test, and answers both.
func killGroupAtCleanup(t *testing.T, path string) (group, sleep int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the run's shell wrote no group id: %v", err)
	}
	if _, err := fmt.Sscanf(string(data), "%d %d", &group, &sleep); err != nil || group <= 1 || sleep <= 1 {
		t.Fatalf("the run's shell wrote %q as its group id and its sleep's pid (%v)", data, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
	return group, sleep
}

// endedByState reports whether pid has ended, by its own stat: no entry, or state Z or X.
func endedByState(t *testing.T, pid int) bool {
	t.Helper()
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return true
	}
	if err != nil {
		t.Fatalf("read the stat of pid %d: %v", pid, err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X")
}
