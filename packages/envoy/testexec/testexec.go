// Package testexec writes the files a test then runs: a fake command, a stand-in script, a stub on
// PATH. A test that writes such a file with os.WriteFile can see the run fail with ETXTBSY ("text
// file busy") whenever another goroutine of the test process forks while the file is open for
// writing, as a test running beside it (t.Parallel) does each time it starts a command: the child
// holds a copy of the write descriptor from the fork until its own exec closes it, and the kernel
// refuses to exec a file some process holds open for writing. WriteFile writes it with
// programfile.Write, the writer of every program file in the code a deployment runs too, which
// keeps every fork of the process out of that span, so no process the test binary starts can hold
// the file open when it runs. ForkBeside is the forking a test of such a write runs beside it.
// Unguarded is the check the modules' text checks hold their code and their tests to.
package testexec

import (
	"bytes"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
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
// such a test would then measure nothing.
func ForkBeside(t testing.TB, n int) {
	t.Helper()
	forker, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("no true command to fork beside the test: %v", err)
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
