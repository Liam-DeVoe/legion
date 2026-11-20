package testproc

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// plant makes a /proc of files under a directory of the test's own, each path relative to it
// holding its text, and has the package read it for the rest of the test.
func plant(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for path, text := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := root
	root = dir
	t.Cleanup(func() { root = before })
	return dir
}

// A child that a thread other than the leader forked is listed in that thread's children file
// alone, so a read of the leader's file misses it.
func TestChildrenReadsTheChildrenFileOfEveryThread(t *testing.T) {
	plant(t, map[string]string{
		"100/task/100/children": "",
		"100/task/101/children": "200 201 ",
		"100/task/102/children": "202",
	})
	got, err := Children(100)
	if err != nil || !slices.Equal(got, []int{200, 201, 202}) {
		t.Fatalf("Children(100) = %v, %v; want [200 201 202], the children each thread's own file lists: a read of the leader's file alone answers none", got, err)
	}
}

// A process whose threads have no children file, as on a kernel built without them, has children
// this package cannot read, which must not read as a process with no child.
func TestChildrenRefusesAProcessWithNoChildrenFile(t *testing.T) {
	plant(t, map[string]string{"100/task/100/stat": "100 (p) S 1 100 100"})
	if got, err := Children(100); err == nil {
		t.Fatalf("Children(100) = %v and no error, though no thread of it has a children file: a process whose children cannot be read reads as one with none", got)
	}
}

// A process /proc has no entry for has been reaped, which the error says.
func TestChildrenOfAReapedProcessIsNotExist(t *testing.T) {
	plant(t, map[string]string{"1/task/1/children": ""})
	if got, err := Children(100); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Children of a pid with no entry = %v, %v; want an error wrapping fs.ErrNotExist", got, err)
	}
}

// Descendants reads every level below the process, through every thread of each, and passes over
// a child that ended between its parent's read and its own.
func TestDescendantsWalksTheWholeTreeBelowTheProcess(t *testing.T) {
	plant(t, map[string]string{
		"1/task/1/children": "2",
		"2/task/2/children": "3 7",
		"2/task/5/children": "4",
		"3/task/3/children": "6",
		"4/task/4/children": "",
		"6/task/6/children": "",
	})
	got, err := Descendants(1)
	if err != nil || !slices.Equal(got, []int{2, 3, 7, 4, 6}) {
		t.Fatalf("Descendants(1) = %v, %v; want [2 3 7 4 6]: every level, breadth first, the gone 7 listed but not read", got, err)
	}
}

// A process has ended when /proc has no entry for it or its state is Z or X, whatever its command
// name holds; any other state is a process that runs.
func TestEndedJudgesAProcessByItsState(t *testing.T) {
	plant(t, map[string]string{
		"10/stat": "10 (a) Z (b) S 1 10 10",
		"11/stat": "11 (sh) Z 1 11 11",
		"12/stat": "12 (sh) X 1 12 12",
		"13/stat": "13 (sh) S 1 13 13",
		"14/stat": "14 (sh) R 1 14 14",
		"15/stat": "15 (sh) D 1 15 15",
		"16/stat": "16 (a) Z) S 1 16 16",
	})
	for pid, want := range map[int]bool{10: false, 11: true, 12: true, 13: false, 14: false, 15: false, 16: false, 17: true} {
		if got, err := Ended(pid); err != nil || got != want {
			t.Errorf("Ended(%d) = %t, %v; want %t (a zombie or a dead process has ended, a reaped one has no entry, any other state runs, and the state follows the last ')')", pid, got, err, want)
		}
	}
}

// Matching held to a context ends with it when one read of the walk never answers, as a read of a
// FIFO no writer opens does, and its error names the bound by the context's cause.
func TestMatchingEndsWithItsContextWhenAReadNeverAnswers(t *testing.T) {
	dir := plant(t, map[string]string{"10/cmdline": "sleep\x00" + "30\x00"})
	fifo := filepath.Join(dir, "11", "cmdline")
	if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Opening the FIFO's writing end without waiting for a reader, then closing it, gives the
	// read left blocked there its end of file, so the walk's goroutine ends with the test.
	t.Cleanup(func() {
		if writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = writer.Close()
		}
	})
	const bound = 2 * time.Second
	ctx, cancel := context.WithTimeoutCause(context.Background(), bound, errors.New("this test's bound"))
	defer cancel()
	began := time.Now()
	type answer struct {
		found []Process
		err   error
	}
	answered := make(chan answer, 1)
	go func() {
		found, err := Matching(ctx, func(Process) bool { return true })
		answered <- answer{found, err}
	}()
	select {
	case got := <-answered:
		want := "the walk of every process of " + dir + " had not ended: this test's bound"
		if got.err == nil || got.err.Error() != want {
			t.Fatalf("Matching = %v, %v; want the error %q", got.found, got.err, want)
		}
		if took := time.Since(began); took < bound {
			t.Fatalf("Matching answered after %s, before its %s bound, though its walk never ended", took, bound)
		}
	case <-time.After(bound + 30*time.Second):
		t.Fatalf("Matching was still walking %s after its context's %s deadline: a read of %s that never answers held it past its bound", 30*time.Second, bound, fifo)
	}
}

// Matching answers each process whose command line match holds for, and passes over what is no
// process.
func TestMatchingAnswersTheProcessesItsMatchHoldsFor(t *testing.T) {
	plant(t, map[string]string{
		"10/cmdline":   "git\x00remote-https\x00http://127.0.0.1:1/x\x00",
		"11/cmdline":   "sleep\x00" + "30\x00",
		"12/cmdline":   "git\x00clone\x00http://127.0.0.1:1/x\x00",
		"self/cmdline": "git\x00http://127.0.0.1:1/x\x00",
	})
	found, err := Matching(context.Background(), func(p Process) bool { return strings.Contains(p.CommandLine(), "http://127.0.0.1:1/x") })
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, p := range found {
		pids = append(pids, p.PID)
	}
	if slices.Sort(pids); !slices.Equal(pids, []int{10, 12}) || found[0].CommandLine() == "" {
		t.Fatalf("Matching found %v, want pids 10 and 12 with their command lines", found)
	}
}

// On the real /proc: a child that a Go process forks from a thread other than its leader is found
// by Children, which the leader's own children file does not list; Descendants finds a shell's
// children below it; and Ended reads a child this process has not reaped, a zombie, as ended, a
// live one as running, and a reaped one as ended.
func TestTheHelpersOnTheRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Fatalf("this test reads the real /proc, which this machine does not have: %v", err)
	}
	root = "/proc"
	t.Run("a child forked from a thread other than the leader", func(t *testing.T) {
		child := startFromAnotherThread(t)
		leader, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getpid()), "task", strconv.Itoa(os.Getpid()), "children"))
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(strings.Fields(string(leader)), strconv.Itoa(child)) {
			t.Fatalf("the leader thread's children file lists %d, so this test could not show a child only another thread's file lists", child)
		}
		got, err := Children(os.Getpid())
		if err != nil || !slices.Contains(got, child) {
			t.Fatalf("Children(%d) = %v, %v; want it to hold %d, which a thread other than the leader forked", os.Getpid(), got, err, child)
		}
	})
	t.Run("a shell's children", func(t *testing.T) {
		shell := exec.Command("sh", "-c", "sleep 30 & sleep 30 & wait")
		if err := shell.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { killTree(shell) })
		var below []int
		testwait.Eventually(t, "the shell's two sleeps", func() bool {
			var err error
			below, err = Descendants(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			children, err := Children(shell.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			return len(children) == 2
		})
		if !slices.Contains(below, shell.Process.Pid) {
			t.Fatalf("Descendants of this process = %v, want it to hold the shell %d", below, shell.Process.Pid)
		}
	})
	t.Run("a zombie, a live process and a reaped one", func(t *testing.T) {
		exited := exec.Command("true")
		if err := exited.Start(); err != nil {
			t.Fatal(err)
		}
		testwait.Eventually(t, "the unreaped `true` to read as ended", func() bool {
			ended, err := Ended(exited.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			return ended
		})
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(exited.Process.Pid))); err != nil {
			t.Fatalf("the unreaped `true` %d has no /proc entry (%v), so this test could not show a zombie read as ended", exited.Process.Pid, err)
		}
		_ = exited.Wait()
		if ended, err := Ended(exited.Process.Pid); err != nil || !ended {
			t.Fatalf("Ended of the reaped `true` = %t, %v; want true", ended, err)
		}
		live := exec.Command("sleep", "30")
		if err := live.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
		if ended, err := Ended(live.Process.Pid); err != nil || ended {
			t.Fatalf("Ended of a live sleep = %t, %v; want false", ended, err)
		}
	})
}

// startFromAnotherThread starts a `sleep 30` from an OS thread of this process that is not its
// leader, kills it when t ends, and answers its pid.
func startFromAnotherThread(t *testing.T) int {
	t.Helper()
	started := make(chan *exec.Cmd, 1)
	failed := make(chan error, 1)
	var start func()
	start = func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if syscall.Gettid() == os.Getpid() {
			// This goroutine holds the leader thread, so one started now runs on another.
			done := make(chan struct{})
			go func() { start(); close(done) }()
			<-done
			return
		}
		child := exec.Command("sleep", "30")
		if err := child.Start(); err != nil {
			failed <- err
			return
		}
		started <- child
	}
	go start()
	select {
	case child := <-started:
		t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
		return child.Process.Pid
	case err := <-failed:
		t.Fatal(err)
	}
	return 0
}

// killTree kills cmd's children and then cmd, and reaps cmd.
func killTree(cmd *exec.Cmd) {
	if children, err := Children(cmd.Process.Pid); err == nil {
		for _, child := range children {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
