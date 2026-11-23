//go:build unix

package cmdtest

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// removeBuildsOfEndedProcesses removes each directory of builds (processDirPrefix) in the
// temporary directory that this user owns and whose process no longer runs. A directory it cannot
// read or remove is left: it is someone else's to remove, or the next run's.
func removeBuildsOfEndedProcesses() {
	root := os.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, ok := processOfBuildsDir(entry.Name())
		if !ok || !entry.IsDir() || pid == os.Getpid() || processRuns(pid) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}

// processOfBuildsDir is the pid a directory of builds named name was made by, and whether name is
// such a directory's name: processDirPrefix, the pid, a dash.
func processOfBuildsDir(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, processDirPrefix)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(digits)
	return pid, err == nil && pid > 0
}

// processRuns reports whether a process of pid runs: one of another user, which may not be
// signalled, runs too.
func processRuns(pid int) bool {
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
