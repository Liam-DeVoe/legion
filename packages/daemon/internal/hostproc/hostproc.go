// Package hostproc keeps a test's result off the processes of the machine it runs on. The /proc of
// a machine lists every process of every user on it, so a test whose code under test judges by what
// it finds there passes or fails by what else the machine runs: the launcher's git lock sweep keeps
// every lock while a git or jj runs whose open files it cannot read, and on a machine several users
// share another user's git is one such process.
//
// Each scanner of this module that lists processes calls Check, or lists through PIDs, with the root
// it resolved, at its own boundary: the launcher's sweep and its reaper (internal/launcher), the
// memory guard, its guest watch and their common scan (internal/memoryguard), and every test helper
// that lists processes itself. In a test binary the call ends the binary when that root is a /proc
// and the scanner has neither a root of the test's own nor a Reason. A test gives a scanner a root
// of its own (a directory of links to the processes it started, a directory it wrote, a fake pod),
// or a Reason that says why what the scanner reads there depends on no process the test did not
// start. A Reason belongs to the one scanner it is given to: there is no switch that lets a test, or
// a process, read the machine's /proc everywhere.
//
// Outside a test binary Check does nothing: a launcher in a pod reads its container's /proc because
// that is its job.
//
// What this sees is what a test runs. A listing that no test reaches is checked the first time one
// does. A listing that reaches /proc by a route that calls no scanner of this module (a program in
// another language, a raw read of the directory) is out of this package's sight; CI runs every test
// binary of this module in a PID namespace of its own, whose /proc lists that binary's processes and
// none of the machine's (.github/scripts/private_proc.py), which is what holds those routes.
package hostproc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

// Reason is why one scanner may read a /proc of the machine in a test: what it reads there that no
// process the test did not start can change. The zero Reason permits nothing.
type Reason struct {
	why string
}

// Because is the Reason why. A why of nothing but spaces is no reason, and Check refuses it as it
// refuses none.
func Because(why string) Reason {
	return Reason{why: strings.TrimSpace(why)}
}

// String is the reason's text, "" for none.
func (r Reason) String() string {
	return r.why
}

// Refusal starts the line a refusal writes to standard error, before the stack of the goroutine
// that reached it.
const Refusal = "hostproc: refused:"

// RefusalStatus is the exit status of a test binary a refusal ended.
const RefusalStatus = 86

// exit ends the binary after a refusal. It is os.Exit, which a test of this package replaces to see
// a refusal without ending its own binary.
var exit = os.Exit

// inTest says whether this binary is a test binary: testing.Testing, which a test of this package
// replaces to see what Check does in a binary that is none.
var inTest = testing.Testing

// Check ends a test binary whose scanner would list root, the /proc of the machine, with no reason.
// root is the scanner's own resolved root, read where the scanner reads it: an explicit "/proc" is
// the machine's, as are "/proc/self/.." and a link to it, since what is checked is whether root is
// a mount of proc at all (isProc), not how its path is spelled.
//
// Two listings of a proc mount pass: one the scanner holds a Reason for, and one of the /proc of a
// PID namespace whose first process this one is, which a test makes for itself (runAsPIDOne in
// internal/launcher, role-init in a role's namespaces). There the listing shows that namespace's
// processes alone, every one of them started by this process or by what it started, and its self
// link names pid 1.
//
// A refusal is the end of the binary, from whatever goroutine reaches it: a scanner's loop may
// log and pass over an error, and a background goroutine has no test to fail, so neither an error
// nor a panic a caller might recover is enough. It writes Refusal, the scanner, the root and the
// stack of the goroutine that reached it, and exits with RefusalStatus.
func Check(scanner, root string, reason Reason) {
	if !inTest() || reason.why != "" || !isProc(root) || namespaceOwn(root) {
		return
	}
	fmt.Fprintf(os.Stderr, "%s %s would list %s, a /proc of the machine, in a test that gave it neither a /proc of its "+
		"own nor a reason (hostproc.Because): the processes of the machine are no test's to judge by\n%s",
		Refusal, scanner, root, debug.Stack())
	exit(RefusalStatus)
}

// namespaceOwn says whether root is the /proc of a PID namespace this process is the first process
// of: this process is pid 1, and the proc mounted at root names pid 1 as itself. A /proc of an
// outer namespace names this process by its pid there, which is never 1.
func namespaceOwn(root string) bool {
	if os.Getpid() != 1 {
		return false
	}
	self, err := os.Readlink(filepath.Join(root, "self"))
	return err == nil && self == "1"
}

// PIDs is every process the /proc at root names, in the order os.ReadDir answers them, once Check
// has let scanner list it: /proc names a process by its decimal pid, so a numeric entry is a process
// and nothing else there is one. The error is os.ReadDir's own, for each caller to word as its own
// failure, and each keeps whatever further filtering it does of the pids.
func PIDs(scanner, root string, reason Reason) ([]int, error) {
	Check(scanner, root, reason)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
