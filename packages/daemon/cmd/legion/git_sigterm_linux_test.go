package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/testwait"
	"golang.org/x/sys/unix"
)

// SIGTERM ends legion git while it reads a `-F -` message from a standard input that never ends,
// as it ends git alone: the command dies of the signal within seconds, and no commit is made.
func TestGitWrapperEndsOnSIGTERMWhileItReads(t *testing.T) {
	git := fixturegit.CompiledGit(t)
	legion := trailerLegion(t)
	repo := trailerRepo(t, git)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	cmd := exec.Command(legion, "git", "--real", git, "--", "commit", "--allow-empty", "-F", "-")
	cmd.Dir, cmd.Stdin = repo, reader
	cmd.Env = trailerEnv(t, trailerPath(git), "DISPATCH_HOST=omp", "DISPATCH_SESSION_ID=signal-session", "LEGION_ROLE=implementer")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// A command the signal did not end is killed when the test ends, as the wait below gives up.
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	reader.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// The signal must find the read, or the row proves nothing of it: a signal sent before the
	// read ends the command as surely. So the test writes the start of a message, and signals once
	// the pipe holds none of it (TIOCINQ, Linux's FIONREAD, reads a pipe's unread bytes from either
	// end): the command has read it, and reads on for the rest, which never comes.
	if _, err := writer.WriteString("the start of a message"); err != nil {
		t.Fatalf("write the message's start: %v", err)
	}
	testwait.Until(t, "legion git to read the message's start from its standard input", func() (int, bool) {
		unread, err := unix.IoctlGetInt(int(writer.Fd()), unix.TIOCINQ)
		if err != nil {
			t.Fatalf("read the pipe's unread bytes: %v", err)
		}
		return unread, unread == 0
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal legion git: %v", err)
	}
	err = testwait.Receive(t, "legion git to end after SIGTERM", done)
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("legion git ended with %v, want death by SIGTERM: %s", err, errb.String())
	}
	status, _ := exit.Sys().(syscall.WaitStatus)
	if !(status.Signaled() && status.Signal() == syscall.SIGTERM) && exit.ExitCode() != 128+int(syscall.SIGTERM) {
		t.Fatalf("legion git ended with %v, want death by SIGTERM: %s", err, errb.String())
	}
	if code, _, _ := trailerRun(t, repo, trailerEnv(t, trailerPath(git)), "", git, "rev-parse", "--verify", "-q", "HEAD"); code == 0 {
		t.Fatal("a commit was made")
	}
}
