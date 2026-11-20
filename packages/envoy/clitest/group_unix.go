//go:build unix

package clitest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// groupPoll is how often waitGroup asks whether a run's process group still has a process. The
// processes a binary leaves behind are not the harness's children, so no wait reports their exit.
const groupPoll = 10 * time.Millisecond

// procRoot is the process table groupEnded reads: /proc, or a directory a test plants in its place.
var procRoot = "/proc"

// inOwnGroup makes cmd the leader of a process group of its own, which every process it starts
// joins unless that process leaves it, and kills the whole group when the run's context ends.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone
	}
}

// waitGroup waits until every process of the group cmd leads has ended and answers nil, or, when
// ctx, the run's bound, ends first, kills what is left and answers what the run knew of its group
// then, worded to follow "exited, but": a process of the group still ran when the process table
// was last read, or no walk of the table for one had ended at all. Call it once cmd.Wait has
// returned.
//
// The harness knows no member of the group but its leader, which has exited, so each poll walks
// the process table (groupEnded). A read there cannot be interrupted once it blocks, as a read of
// a stat that never answers does, so each walk runs on a goroutine of its own and the wait selects
// on its answer and on ctx. A walk ctx outlasts is left behind, blocked in its read, with a
// buffered channel it can still answer into once the read returns.
func waitGroup(ctx context.Context, cmd *exec.Cmd) error {
	group, root := cmd.Process.Pid, procRoot
	running := errors.New("a process it started was still running when the process table was last read")
	tick := time.NewTicker(groupPoll)
	defer tick.Stop()
	for answered := false; ; answered = true {
		walk := make(chan bool, 1)
		go func() { walk <- groupEnded(root, group) }()
		select {
		case ended := <-walk:
			if ended {
				return nil
			}
		case <-ctx.Done():
			_ = syscall.Kill(-group, syscall.SIGKILL)
			if answered {
				return running
			}
			return fmt.Errorf("no walk of %s for a live process of its group had ended", root)
		}
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-group, syscall.SIGKILL)
			return running
		case <-tick.C:
		}
	}
}

// groupEnded reports whether every process of group pgid has ended, by each member's state in the
// process table at root. Signal 0 to the group reaches every member, a zombie included: a process
// the binary left behind is an orphan once the binary exits, and its zombie waits for whatever the
// kernel handed it to. Where that never reaps it, as where the namespace's PID 1 is a `go test`
// with no init above it, the zombie stays and signal 0 still reaches the group. So while it does,
// the table is read for a member that is not dead (Z or X); where it cannot be read, as on a
// system with no /proc, the group counts as running.
func groupEnded(root string, pgid int) bool {
	if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		return true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	want := []byte(strconv.Itoa(pgid))
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile(root + "/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// The fields after the command name, which ends at the last ')': state, parent, group.
		fields := bytes.Fields(stat[bytes.LastIndexByte(stat, ')')+1:])
		if len(fields) < 3 || !bytes.Equal(fields[2], want) {
			continue
		}
		if state := fields[0]; !bytes.Equal(state, []byte("Z")) && !bytes.Equal(state, []byte("X")) {
			return false
		}
	}
	return true
}
