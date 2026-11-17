//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestAwaitEndsTheFrontBeforeItKills runs the awaiter as e2e-refill.sh's interrupt(restart) does,
// on a stand-in front and a stand-in server, and reads, at the moment the server has exited,
// whether every process of the front had exited already: a front still running then would
// forward to the port the server released (scripts/e2e/lib/held-front.sh).
func TestAwaitEndsTheFrontBeforeItKills(t *testing.T) {
	front, child := startFront(t)
	server := exec.Command("sleep", "60")
	start(t, server)
	fronts := map[string]int{
		"leader " + strconv.Itoa(front.Process.Pid): pidFD(t, front.Process.Pid),
		"child " + strconv.Itoa(child):              pidFD(t, child),
	}
	serverFD := pidFD(t, server.Process.Pid)

	log := filepath.Join(t.TempDir(), "listener.log")
	line := fmt.Sprintf(`{"time":%q,"msg":"session registry bucket was recreated; reading it"}`+"\n",
		time.Now().Format(time.RFC3339Nano))
	if err := os.WriteFile(log, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	// alive receives, the moment the server has exited, the front's processes still running then.
	alive := make(chan []string, 1)
	go func() {
		if exited, err := exitedFD(serverFD, 10*time.Second); !exited {
			alive <- []string{fmt.Sprintf("(the server did not exit within 10 s: %v)", err)}
			return
		}
		var running []string
		for name, fd := range fronts {
			if exited, err := exitedFD(fd, 0); !exited {
				running = append(running, fmt.Sprintf("%s (%v)", name, err))
			}
		}
		alive <- running
	}()
	if err := runAwait([]string{"-log", log, "-match", "session registry bucket was recreated;", "-timeout", "5s",
		"-signal", "KILL", "-pid", strconv.Itoa(server.Process.Pid), "-front", strconv.Itoa(front.Process.Pid)}); err != nil {
		_ = server.Process.Kill()
		<-alive
		t.Fatalf("refillrig await: %v", err)
	}
	if running := <-alive; len(running) > 0 {
		t.Fatalf("the front's processes %s were running once the server had exited", strings.Join(running, ", "))
	}
}

// TestEndFrontLeavesNoStoppedGroup makes endFront's stop wait fail, as it does when the front's
// leader stays in uninterruptible sleep (D) for the whole grace, and then ends the front as
// e2e-refill.sh's stop_front does: SIGTERM to the group, and a wait on the leader. A stopped
// process acts on no signal but SIGKILL and SIGCONT, so a group that endFront left stopped makes
// that wait last for ever.
func TestEndFrontLeavesNoStoppedGroup(t *testing.T) {
	front, child := startFront(t)
	childFD := pidFD(t, child)
	// Each state as /proc gives it, except that a stopped process reads as D.
	neverStopped := func(pid int) byte {
		if state := procState(pid); state != 'T' {
			return state
		}
		return 'D'
	}
	if err := endFront(front.Process.Pid, 200*time.Millisecond, neverStopped); err == nil {
		t.Fatal("endFront succeeded, though its stop wait never saw the leader stop")
	}
	if err := syscall.Kill(-front.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- front.Wait() }()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-front.Process.Pid, syscall.SIGKILL)
		<-waited
		t.Fatal("a wait on the front's leader had not returned 3 s after its group was sent SIGTERM: endFront left the group stopped")
	}
	if exited, err := exitedFD(childFD, 3*time.Second); !exited {
		t.Fatalf("the front's child %d had not exited 3 s after its group was sent SIGTERM (%v)", child, err)
	}
}

// TestChildren reads children from a stand-in /proc. A process has one children file for each
// of its threads; a process with none, as on a kernel built without CONFIG_PROC_CHILDREN, or a
// file that cannot be read, is an error, never an empty list.
func TestChildren(t *testing.T) {
	for _, row := range []struct {
		name  string
		files map[string]string // path under <proc>/7/task, its text; a text of "/" makes a directory
		want  []int
		err   string
	}{
		{name: "two threads", files: map[string]string{"7/children": "12 34 ", "8/children": "56 "}, want: []int{12, 34, 56}},
		{name: "no child", files: map[string]string{"7/children": ""}},
		{name: "no children file", files: map[string]string{"7/stat": "7 (sh) T"}, err: "7/task/*/children"},
		{name: "a file that cannot be read", files: map[string]string{"7/children": "/"}, err: "7/task/7/children"},
	} {
		t.Run(row.name, func(t *testing.T) {
			proc := t.TempDir()
			for path, text := range row.files {
				path = filepath.Join(proc, "7", "task", path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				var err error
				if text == "/" {
					err = os.Mkdir(path, 0o700)
				} else {
					err = os.WriteFile(path, []byte(text), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := children(proc, 7)
			switch {
			case row.err == "" && err != nil:
				t.Fatalf("children: %v", err)
			case row.err != "" && (err == nil || !strings.Contains(err.Error(), row.err)):
				t.Fatalf("children returned %v, %v; want an error naming %s", got, err, row.err)
			case !slices.Equal(got, row.want):
				t.Fatalf("children returned %v; want %v", got, row.want)
			}
		})
	}
}

// startFront starts a stand-in front, a session leader (sh) with one child (sleep), as socat
// forks one process per connection, and returns it and its child's pid.
func startFront(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	front := exec.Command("sh", "-c", `sleep 60 & echo "$!"; wait`)
	front.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := front.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	start(t, front)
	var child int
	if _, err := fmt.Fscan(out, &child); err != nil {
		t.Fatalf("read the front's child pid: %v", err)
	}
	return front, child
}

// start starts cmd and, at the test's end, kills its process group (none, unless cmd leads one)
// and it, and reaps it.
func start(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
}

// pidFD opens a pidfd on pid, which stays on that process even once its pid is reused.
func pidFD(t *testing.T, pid int) int {
	t.Helper()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatalf("pidfd_open %d: %v", pid, err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

// exitedFD reports whether the process of the pidfd has exited within timeout: a pidfd turns
// readable once its process is a zombie or gone. The error is the poll's own, if it failed.
func exitedFD(fd int, timeout time.Duration) (bool, error) {
	for {
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, int(timeout.Milliseconds()))
		if !errors.Is(err, unix.EINTR) {
			return err == nil && n > 0, err
		}
	}
}
