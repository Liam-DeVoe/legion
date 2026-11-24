// Package testexec writes the files a test then runs: a fake command, a stand-in script, a stub on
// PATH. A test that writes such a file with os.WriteFile can see the run fail with ETXTBSY ("text
// file busy") whenever another goroutine of the test process forks while the file is open for
// writing, as a test running beside it (t.Parallel) does each time it starts a command: the child
// holds a copy of the write descriptor from the fork until its own exec closes it, and the kernel
// refuses to exec a file some process holds open for writing. WriteFile writes it with
// programfile.Write, the writer of every program file in the code a deployment runs too, which
// keeps every fork of the process out of that span, so no process the test binary starts can hold
// the file open when it runs. ForkBeside is the forking a test of such a write runs beside it, on
// a kernel that refuses such a run at all. Unguarded is the check the modules' text checks hold
// their code and their tests to.
package testexec

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/programfile"
)

// WriteFile is os.WriteFile for a file a test will run: programfile.Write with os.O_TRUNC, so a
// file already at name is written over, as os.WriteFile writes over one, and given perm exactly.
func WriteFile(name string, data []byte, perm os.FileMode) error {
	return programfile.Write(name, os.O_TRUNC, perm, bytes.NewReader(data))
}

// ForkBeside starts n goroutines that each start true and start it again as soon as it exits,
// until the test ends: the forks that the code a test calls meets in a process whose other
// goroutines start commands, as a daemon's do and as tests running beside each other do. A test
// that writes a file and then runs it beside them meets ETXTBSY whenever a child forked inside the
// write still holds the file open at the run. A machine with no true command fails the test, since
// such a test would then measure nothing. So would a machine where the run cannot meet ETXTBSY, so
// ForkBeside first runs a program it holds open for writing itself (busyRunVerdict says what it
// does with the answer): a kernel that runs it, as gVisor does, skips the test, naming that, except
// under GITHUB_ACTIONS, where it fails, since CI's runners refuse such a run and one that does not
// is a broken runner, never a green job with the proof gone from it; any other answer, such as a
// TMPDIR mounted noexec, fails the test, naming the answer.
func ForkBeside(t testing.TB, n int) {
	t.Helper()
	forker, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("no true command to fork beside the test: %v", err)
	}
	switch skip, fail := busyRunVerdict(runHeldOpen(t), os.Getenv("GITHUB_ACTIONS") == "true"); {
	case fail != "":
		t.Fatal(fail)
	case skip != "":
		t.Skip(skip)
	}
	var stop atomic.Bool
	var forks sync.WaitGroup
	for range n {
		forks.Go(func() {
			for !stop.Load() {
				_ = exec.Command(forker).Run()
			}
		})
	}
	t.Cleanup(func() {
		stop.Store(true)
		forks.Wait()
	})
}

// busyRunVerdict is what ForkBeside does with answer, the error of a run of a program the process
// held open for writing, where ci says GITHUB_ACTIONS is set: run the test (both empty), skip it,
// or fail it, each saying what the run answered.
func busyRunVerdict(answer error, ci bool) (skip, fail string) {
	const (
		ran   = "this kernel ran a program this process held open for writing rather than refuse it with ETXTBSY, "
		blind = "so no run of this test can meet the fault it looks for"
	)
	switch {
	case errors.Is(answer, syscall.ETXTBSY):
		return "", ""
	case answer == nil && ci:
		return "", ran + blind + "; under GITHUB_ACTIONS that is a broken runner, not a test to skip"
	case answer == nil:
		return ran + "as gVisor does, " + blind, ""
	default:
		return "", "a run of a program this process held open for writing answered " + answer.Error() +
			", neither ETXTBSY nor a run, " + blind
	}
}

// runHeldOpen writes a program, opens it again for writing, runs it while it is open and answers
// the run's error: ETXTBSY on a kernel that refuses to run a file some process holds open for
// writing, as Linux does every time, and nil on one that runs it.
func runHeldOpen(t testing.TB) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "held-open")
	if err := programfile.Write(path, os.O_EXCL, 0o700, strings.NewReader("#!/bin/sh\nexit 0\n")); err != nil {
		t.Fatalf("write the program the kernel's refusal is read from: %v", err)
	}
	held, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("hold the program open for writing: %v", err)
	}
	defer held.Close()
	return exec.Command(path).Run()
}
