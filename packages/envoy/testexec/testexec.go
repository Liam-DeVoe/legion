// Package testexec writes the files a test then runs: a fake command, a stand-in script, a stub on
// PATH. A test that writes such a file with os.WriteFile can see the run fail with ETXTBSY ("text
// file busy") whenever another goroutine of the test process forks while the file is open for
// writing, as a test running beside it (t.Parallel) does each time it starts a command: the child
// holds a copy of the write descriptor from the fork until its own exec closes it, and the kernel
// refuses to exec a file some process holds open for writing. Once internal/daemon's tests ran
// beside each other, a gate test failed so under load
// (TestTheSessionStorageProbeClassifiesWhatOhMyPiSaid/refuses, one run in 660). WriteFile keeps
// every fork of the process out of that span, so no process the test binary starts can hold the
// file open when it runs. Unguarded is the check the modules' text checks hold their tests to, and
// its doc comment lists the forms it names and the forms it does not see. docs/testing.md, "A file
// a test runs", states the rule.
package testexec

import (
	"os"
	"syscall"
)

// WriteFile is os.WriteFile for a file a test will run. It holds syscall.ForkLock for reading while
// the file is open for writing: os/exec, and every other fork of a Go process, takes that lock for
// writing around the fork, so no fork of this process falls inside the write and no child holds the
// file's write descriptor when the file is run.
func WriteFile(name string, data []byte, perm os.FileMode) error {
	_ = syscall.ForkLock // CONTROL ONLY: the fork lock taken out
	return os.WriteFile(name, data, perm)
}
