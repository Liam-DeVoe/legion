//go:build linux

// packages/envoy/cmd/cipher/secret_gcp_linux.go
//
// How gcloud's process group ends with this command on Linux: under a supervisor.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// gcloudSupervisorEnv marks a run of this binary as the supervisor of one gcloud call
// (superviseGcloud, runGcloudSupervisor): its arguments are gcloud's path and gcloud's own.
const gcloudSupervisorEnv = "AGENT_SECRETS_GCLOUD_SUPERVISOR"

// superviseGcloud makes cmd, a gcloud command killGroupOnCancel sets up, start this binary as
// gcloud's supervisor, with gcloud's path and arguments for its own: the supervisor leads gcloud's
// process group, starts gcloud in it, and ends with gcloud's own exit (runGcloudSupervisor). Where
// this command ends by a SIGKILL, which runs none of its code, the kernel sends the supervisor the
// parent-death signal it starts with (gcloudProcAttr), and the supervisor kills the whole group,
// gcloud and whatever gcloud started. gcloud itself cannot carry that signal: the kernel clears it
// at the exec of a setuid or file-capability launcher, such as a snap install's /snap/bin/gcloud
// (snap-confine), and the supervisor, this same binary, has neither. The group is the supervisor's,
// so the kills at a deadline, at a signal this command handles and after the wait (killGroupOnCancel,
// killRunningGcloud, killGroupLeft) reach gcloud as before. A gcloud that cannot be found is left
// as it is, so Start answers that, as without a supervisor; so is a binary that cannot name its
// own path, whose gcloud then gets the parent-death signal itself.
func superviseGcloud(cmd *exec.Cmd) {
	if cmd.Err != nil {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Args = append([]string{self, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = self
	cmd.Env = append(env[:len(env):len(env)], gcloudSupervisorEnv+"=1")
}

// gcloudProcAttr starts gcloud's supervisor (superviseGcloud) leading a process group of its own
// (killGroupOnCancel), with SIGTERM as its parent-death signal, which the supervisor handles by
// killing the group. The signal follows the thread that started the supervisor, not this process,
// so runRecorded keeps that thread from Start until the supervisor has been waited for.
func gcloudProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

// A run of this binary as a gcloud supervisor does that and nothing else, before any command: in
// the agent-secrets binary and in its test binary alike, since neither runs main or TestMain first.
func init() {
	if os.Getenv(gcloudSupervisorEnv) == "" {
		return
	}
	os.Exit(runGcloudSupervisor(os.Args[1:]))
}

// runGcloudSupervisor is a gcloud supervisor's whole run (superviseGcloud): it starts args, gcloud's
// path and its arguments, in its own process group with its own standard input, output and error,
// and waits for it. On SIGTERM, the parent-death signal its parent left it, it kills its group with
// SIGKILL, itself included. Otherwise it ends as gcloud ended: with gcloud's exit status, or by the
// signal that ended gcloud. gcloud does not inherit gcloudSupervisorEnv.
func runGcloudSupervisor(args []string) int {
	parentGone := make(chan os.Signal, 1)
	signal.Notify(parentGone, syscall.SIGTERM)
	_ = os.Unsetenv(gcloudSupervisorEnv)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "agent-secrets: a gcloud supervisor needs gcloud's path")
		return 2
	}
	gcloud := exec.Command(args[0], args[1:]...)
	gcloud.Stdin, gcloud.Stdout, gcloud.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := gcloud.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "agent-secrets: start gcloud: %v\n", err)
		return 127
	}
	waited := make(chan struct{})
	go func() {
		_ = gcloud.Wait()
		close(waited)
	}()
	select {
	case <-parentGone:
		_ = syscall.Kill(0, syscall.SIGKILL)
		select {}
	case <-waited:
	}
	status, _ := gcloud.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() {
		return gcloud.ProcessState.ExitCode()
	}
	signal.Reset(status.Signal())
	_ = syscall.Kill(os.Getpid(), status.Signal())
	return 128 + int(status.Signal())
}
