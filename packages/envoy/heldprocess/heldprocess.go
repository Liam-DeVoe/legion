// Package heldprocess is a test's stand-in for work a command holds running, and the reads that
// tell whether that stand-in still runs: a shell script's end that records the stand-in's pid and
// becomes `sleep <seconds>` under it (Script), and the check of a recorded pid against that
// command line (Recorded, Running), which a pid the kernel has given to another process since does
// not pass. A test signals the command, then reads whether the stand-in is gone.
//
// It is one body for both Go modules of this repository: `packages/daemon`'s `legion` tests and
// `packages/envoy`'s `envoy-mcp` test both hold their work with it, so a fix to how a test proves
// a process gone reaches every test that proves it. It imports the standard library alone, so a
// module that imports it gains no module through it, and it is public, rather than under
// internal/, so the daemon's module can import it through its replace of this one, as it imports
// clitest.
package heldprocess

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Script is the end of a stand-in shell script that holds its work: it records its pid in pidFile,
// written whole by a rename, so a reader never reads half of it, and becomes `sleep <seconds>`
// under that pid, which neither reads its standard input nor ends when that input closes, with a
// command line Running tells from a pid the kernel has given out again. Each test gives its
// stand-in a sleep of its own, held far past any bound of the test.
func Script(pidFile, seconds string) string {
	return "echo $$ >'" + pidFile + ".new'\nmv '" + pidFile + ".new' '" + pidFile + "'\nexec sleep " + seconds + "\n"
}

// Running says whether pid is still the stand-in: a process whose command line is
// `sleep <seconds>`, read from /proc, so a pid the kernel has since given to another process does
// not count.
func Running(pid int, seconds string) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	return strings.Join(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), " ") == "sleep "+seconds
}

// Recorded is the pid a stand-in recorded in pidFile (Script), and whether that pid is still the
// stand-in's `sleep <seconds>` (Running). A file not written yet is no stand-in: pid 0, not
// running.
func Recorded(pidFile, seconds string) (pid int, running bool) {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}
	return pid, Running(pid, seconds)
}

// KillOnCleanup registers a cleanup that kills the stand-in pidFile names if it is still running
// when the test ends, so a failure leaves nothing behind. It reads the file only then, so it is
// registered before whatever starts the stand-in, and holds whichever check fails first. It kills
// only a pid Running says is still the stand-in.
func KillOnCleanup(t testing.TB, pidFile, seconds string) {
	t.Helper()
	t.Cleanup(func() {
		if pid, running := Recorded(pidFile, seconds); running {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}
