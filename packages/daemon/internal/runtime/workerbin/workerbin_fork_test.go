package workerbin

import (
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sjawhar/envoy/testexec"
)

// The scripts Install writes run at once however many goroutines of the process start commands
// beside the install, and an agent runs them by name off its PATH. A child forked while a script
// was open for writing holds it open until its own exec, and the kernel refuses to run a file some
// process holds open for writing (ETXTBSY, "text file busy"), whatever name the rename gave it.
// Install's callers start no command beside it today: a tmux daemon installs the scripts at its
// boot before it starts supervision, the NATS connection or its API, and `legion controller
// start` while nothing else of it starts one. Eight goroutines that start true without a pause
// stand in here for a process that does, while this test installs again and again, over the
// install before, and runs each script as soon as the install returns.
func TestTheScriptsInstallWritesRunWhileTheProcessForks(t *testing.T) {
	legion, err := exec.LookPath("true")
	if err != nil {
		t.Fatalf("no true command for the scripts to exec: %v", err)
	}
	testexec.ForkBeside(t)
	root := t.TempDir()
	const installs = 150
	busy := 0
	var first error
	for range installs {
		if err := Install(root, legion, ""); err != nil {
			t.Fatalf("Install: %v", err)
		}
		for _, name := range []string{"legion", "gh", "git"} {
			err := exec.Command(filepath.Join(LauncherDir(root), name)).Run()
			switch {
			case errors.Is(err, syscall.ETXTBSY):
				busy++
				if first == nil {
					first = err
				}
			case err != nil:
				t.Fatalf("run the installed %s: %v", name, err)
			}
		}
	}
	if busy != 0 {
		t.Errorf("%d of %d runs of the installed scripts were refused as busy while the process forked; the first: %v", busy, 3*installs, first)
	}
}
