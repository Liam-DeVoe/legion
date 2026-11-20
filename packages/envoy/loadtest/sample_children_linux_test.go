//go:build linux

package main

import (
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
)

// childrenOf reads a process's children from the children file of each of its threads: a child
// that a thread other than the leader forked is listed in that thread's file alone, so a read of
// the leader's file misses it, and a walk of every process's stat finds it only by reading the
// whole machine. A shell's two children are found too, as the postmaster's backends are.
func TestChildrenOfReadsTheChildrenFileOfEveryThread(t *testing.T) {
	child := startFromAnotherThread(t)
	leader, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getpid()), "task", strconv.Itoa(os.Getpid()), "children"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(strings.Fields(string(leader)), strconv.Itoa(child)) {
		t.Fatalf("the leader thread's children file lists %d, so this test could not show a child only another thread's file lists", child)
	}
	children, err := childrenOf(os.Getpid())
	if err != nil || !slices.Contains(children, child) {
		t.Fatalf("childrenOf(%d) = %v, %v; want it to hold %d, which a thread other than the leader forked", os.Getpid(), children, err, child)
	}

	shell := exec.Command("sh", "-c", "sleep 30 & sleep 30 & wait")
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if children, err := childrenOf(shell.Process.Pid); err == nil {
			for _, child := range children {
				_ = syscall.Kill(child, syscall.SIGKILL)
			}
		}
		_ = shell.Process.Kill()
		_ = shell.Wait()
	})
	deadline := time.Now().Add(time.Minute)
	for {
		children, err := childrenOf(shell.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if len(children) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("childrenOf the shell %d = %v a minute after it started its two sleeps, want both", shell.Process.Pid, children)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
