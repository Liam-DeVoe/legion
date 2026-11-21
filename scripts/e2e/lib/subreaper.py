#!/usr/bin/env python3
"""subreaper.py: run a stage proof as a child subreaper, so every process it leaves behind stays below it.

usage:
  subreaper.py CMD [ARG]...

Marks this process a child subreaper (prctl PR_SET_CHILD_SUBREAPER, which execve keeps), sets
LEGION_E2E_SUBREAPER to its pid, and executes CMD in its own process, so the pid stays the same and
CMD, a stage proof's bash (lib/subreaper.sh runs it so), is the subreaper. A process the proof
starts that ends and leaves a child behind, a daemon's double fork or an ssh-agent's, then gives
that child to the proof's bash, or to a subreaper below it, and never to pid 1 or to a subreaper
above it: every process of the run is below the proof's shell for its whole life. So the run's walk
(procs_naming, in lib/process-tree.sh) can tell another session's process from one of the run's by
its line of parents when it cannot read its environment (run_descendant), and a process of this
user that another session left under pid 1 during the run does not fail the run's teardown. Bash
reaps a child it adopted when it exits, as it reaps its own.

Where the kernel refuses the mark, it says so on stderr, sets LEGION_E2E_SUBREAPER to `PID
refused`, which lib/subreaper.sh reads as done and procs_naming as no subreaper, and executes CMD
all the same. A CMD that cannot be executed exits 127 (not found) or 126.
"""

import ctypes
import os
import sys

PR_SET_CHILD_SUBREAPER = 36


def main() -> None:
    if len(sys.argv) < 2:
        sys.exit("usage: subreaper.py CMD [ARG]...")
    pid = os.getpid()
    libc = ctypes.CDLL(None, use_errno=True)
    if libc.prctl(PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) == 0:
        os.environ["LEGION_E2E_SUBREAPER"] = str(pid)
    else:
        error = ctypes.get_errno()
        os.environ["LEGION_E2E_SUBREAPER"] = f"{pid} refused"
        print(
            f"subreaper.py: prctl(PR_SET_CHILD_SUBREAPER) failed: {os.strerror(error)}; running {sys.argv[1]} without it",
            file=sys.stderr,
        )
    try:
        os.execvp(sys.argv[1], sys.argv[1:])
    except FileNotFoundError as error:
        print(f"subreaper.py: {sys.argv[1]}: {error.strerror}", file=sys.stderr)
        sys.exit(127)
    except OSError as error:
        print(f"subreaper.py: {sys.argv[1]}: {error.strerror}", file=sys.stderr)
        sys.exit(126)


if __name__ == "__main__":
    main()
