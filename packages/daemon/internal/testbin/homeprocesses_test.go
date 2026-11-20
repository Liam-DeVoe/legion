package testbin

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Oh My Pi's daemon broker leads a session of its own and writes under the HOME it was started
// with as it shuts down, making again each directory a removal took away; the browser it runs
// ends on SIGTERM. This stands in for both with two shells: one that leads its own session and
// writes a record under its HOME without pause, making its directories as it goes, and one that
// takes no SIGTERM. The test that starts them ends, and each must have gone and its HOME been
// removed by then. A third process, under another HOME, must still run.
func TestTheEndOfATestEndsWhatStillRunsUnderItsOMPHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the processes under a HOME are read from /proc")
	}
	stranger := startUnder(t, t, t.TempDir(), "while :; do sleep 1; done")
	var home string
	var writer, deaf *underHome
	passed := t.Run("a HOME a detached writer and a process deaf to SIGTERM still run under", func(sub *testing.T) {
		home = sub.TempDir()
		stopAtEnd(sub, home, time.Second)
		record := filepath.Join(home, "run", "daemons", "scope", "daemons", "browser", "meta.json")
		writer = startUnder(sub, t, home, `while :; do mkdir -p "$(dirname "$RECORD")"; : > "$RECORD"; done`, "RECORD="+record)
		deaf = startUnder(sub, t, home, "trap '' TERM; while :; do sleep 1; done")
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(record); err == nil {
				break
			}
			if time.Now().After(deadline) {
				sub.Fatalf("the writer under %s wrote no %s within 10s", home, record)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	if !passed {
		t.Errorf("the test that ran under %s failed at its end", home)
	}
	for _, p := range []*underHome{writer, deaf} {
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			t.Errorf("the %s (pid %d) still runs under %s after the test that started it ended", p.name, p.cmd.Process.Pid, home)
		}
	}
	if _, err := os.Stat(home); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there after the test that made it ended (%v), want it removed", home, err)
	}
	select {
	case <-stranger.done:
		t.Errorf("the process under another HOME (pid %d) ended with the test, want it left running", stranger.cmd.Process.Pid)
	default:
	}
}

// underHome is one shell a test started under a HOME, and the channel its Wait closes.
type underHome struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
}

// startUnder starts script under sh -c with HOME=home, leading a session of its own, as Oh My Pi
// starts its daemon broker. Its kill is owner's cleanup, which runs after the cleanups of the test
// t that started it, so it never ends a process before the cleanup under test has had its turn,
// and a run whose subject leaves the process running leaves nothing behind.
func startUnder(t, owner *testing.T, home, script string, env ...string) *underHome {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %q under %s: %v", script, home, err)
	}
	p := &underHome{name: "shell " + script, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	owner.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	})
	return p
}
