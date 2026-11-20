package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sjawhar/legion/daemon/internal/procgroup"
)

// The commands an operator runs from a terminal whose work outlives a kill of the command alone,
// each of which runs under stoppingOnHangupOrQuit: `legion start`, whose boot gate runs in process
// groups of its own; `legion controller start`, whose probe does; and `legion probe-boot`, whose
// probe Sandbox lives in the cluster until its context ends. A command left out of the watch, or
// one added to it, fails this by name. The hidden commands that also start such work are left
// out on purpose, since nothing runs them from a terminal: `legion workspace-init`'s fetch,
// provision and cache are an issue pod's init containers and a cache pod's container
// (internal/runtime/sandbox's podkind.go and repocache.go), and `legion probe-image` is the
// command of the daemon's probe Sandbox and of the image build (internal/runtime/sandbox's
// probe.go); the kubelet stops each with a SIGTERM, which main's context catches.
func TestTheTerminalCommandsWhoseWorkOutlivesThemRunUnderTheHangupAndQuitWatch(t *testing.T) {
	want := []string{"legion controller start", "legion probe-boot", "legion start"}
	var got []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Annotations[stopsOnHangupOrQuit] == "true" {
			got = append(got, cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(newRoot(io.Discard, io.Discard))
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("the commands under the hang-up and quit watch = %q; want %q", got, want)
	}
}

// hangupWatchHelper is the variable that makes TestHangupOrQuitWatchHelper run: it names the file
// the held stand-in records its pid in, beside the stand-in's sleep in hangupWatchSeconds. Both
// are LEGION_TEST_ names, which TestMain's fixtureenv.MustGuard keeps for the helper.
const (
	hangupWatchHelper  = "LEGION_TEST_HANGUP_WATCH_HELPER"
	hangupWatchSeconds = "LEGION_TEST_HANGUP_WATCH_SECONDS"
)

// TestHangupOrQuitWatchHelper is the process TestAHangupOrQuitStopsTheWorkOfACommandUnderTheWatch
// signals: a command run through stoppingOnHangupOrQuit, as `legion start`, `legion controller
// start` and `legion probe-boot` are, whose body runs a stand-in in a process group of its own
// under the command's context (procgroup.Configure) and waits for it. It exits with the command's
// status, printing nothing.
func TestHangupOrQuitWatchHelper(t *testing.T) {
	pidFile := os.Getenv(hangupWatchHelper)
	if pidFile == "" {
		t.Skip("subprocess helper")
	}
	cmd := &cobra.Command{Use: "held", SilenceErrors: true, SilenceUsage: true}
	stoppingOnHangupOrQuit(cmd, func(ctx context.Context, _ []string) int {
		held := exec.CommandContext(ctx, "sh", "-c", holdsStandIn(pidFile, os.Getenv(hangupWatchSeconds)))
		procgroup.Configure(held)
		if err := held.Run(); err != nil {
			return 1
		}
		return 0
	})
	cmd.SetArgs(nil)
	code := 0
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		var status exitCode
		if !errors.As(err, &status) {
			os.Exit(2)
		}
		code = int(status)
	}
	os.Exit(code)
}

// TestAHangupOrQuitStopsTheWorkOfACommandUnderTheWatch: a SIGHUP, a terminal that closes, or a
// SIGQUIT, its quit key, sent to the terminal's foreground group reaches a command run under
// stoppingOnHangupOrQuit but not the work it runs in a process group of its own; the command
// catches it, stops that work's group, and ends with 128+<signal> having printed nothing. The
// helper is started as the leader of a group of its own and its held stand-in is the work
// (signalWhileHeld). Without the watch the signal's default action killed the command where it
// stood and left the stand-in running, reparented and waited for by nobody.
func TestAHangupOrQuitStopsTheWorkOfACommandUnderTheWatch(t *testing.T) {
	if _, err := os.ReadFile("/proc/self/cmdline"); err != nil {
		t.Fatalf("/proc does not answer (%v): this test reads from it whether the stand-in is still running", err)
	}
	for _, tc := range []struct {
		name    string
		signal  syscall.Signal
		seconds string
	}{
		{"SIGHUP", syscall.SIGHUP, "287"},
		{"SIGQUIT", syscall.SIGQUIT, "286"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "stand-in")
			command := exec.Command(os.Args[0], "-test.run=^TestHangupOrQuitWatchHelper$", "-test.count=1")
			command.Env = append(os.Environ(), hangupWatchHelper+"="+pidFile, hangupWatchSeconds+"="+tc.seconds)
			signalWhileHeld(t, command, "a command under the hang-up and quit watch", pidFile, tc.seconds, "its work", tc.signal)
		})
	}
}
