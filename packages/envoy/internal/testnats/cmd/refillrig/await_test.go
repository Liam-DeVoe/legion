//go:build linux

package main

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

	"golang.org/x/sys/unix"
)

// TestAwaitEndsTheFrontBeforeItKills runs the awaiter as e2e-refill.sh's interrupt(restart) does,
// on a stand-in front (a session leader with one child, as socat forks one per connection) and a
// stand-in server, and reads, at the moment the server has exited, whether every process of the
// front had exited already: a front still running then would forward to the port the server
// released (scripts/e2e/lib/held-front.sh).
func TestAwaitEndsTheFrontBeforeItKills(t *testing.T) {
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child")
	front := exec.Command("sh", "-c", `sleep 60 & echo "$!" >"$1"; wait`, "sh", childFile)
	front.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	start(t, front, true)
	server := exec.Command("sleep", "60")
	start(t, server, false)
	child := readPID(t, childFile)
	fronts := map[string]int{
		"leader " + strconv.Itoa(front.Process.Pid): pidFD(t, front.Process.Pid),
		"child " + strconv.Itoa(child):              pidFD(t, child),
	}
	serverFD := pidFD(t, server.Process.Pid)

	log := filepath.Join(dir, "listener.log")
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

// start starts cmd and, at the test's end, kills it (its whole process group when group is set)
// and reaps it.
func start(t *testing.T, cmd *exec.Cmd, group bool) {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if group {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if text, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(text))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s within 10 s", path)
	return 0
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
