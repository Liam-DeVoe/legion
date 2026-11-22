package leakguard

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exitedNoReap blocks until the process pid has exited, leaving it unreaped (WNOWAIT), so that
// the exec.Cmd that started it still reads its status in Wait. It answers true once pid has
// exited, and false where waitid cannot wait for it at all.
func exitedNoReap(pid int, _ <-chan struct{}) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err == nil
		}
	}
}
