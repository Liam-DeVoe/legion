// The process group a command runs in, and how it ends: an envoy.json `!` command (`fileValue`,
// ./envoy-config), a person's `!command` answered through Dispatch (pi-envoy's typed-shell),
// AskGate's dry run (pi-envoy's askgate-core) and jj's attribution read (pi-shared's
// jj-attribution). Each starts its command detached, in a group of its own, so only a kill of the
// group reaches every process the command started. Both host plugins depend on this package, so
// the one copy lives here.

/** Kills every process of the group `pid` leads, if any is left. */
export function killGroup(pid: number): void {
  try {
    process.kill(-pid, "SIGKILL");
  } catch {
    // The group is already gone.
  }
}

/**
 * The shell script `groupWatchedBy` runs. It moves its stdin, the pipe this process holds, to fd 3,
 * starts the watch in the background (it reads fd 3 until the pipe's last writer is gone, then
 * kills the group whose leader is the command's own pid, `-$$`, itself included), and replaces
 * itself with the caller's argv (`"$0" "$@"`), which reads /dev/null and holds no end of the pipe.
 * A caller that did not spawn it detached leaves the command in the caller's own group, which
 * `-$$` does not name: the watch then kills nothing rather than the caller. The watch writes
 * nowhere, so it holds none of the command's output open.
 */
const WATCH =
  'exec 3<&0; (while read -r line <&3; do :; done; kill -s KILL -- -$$) </dev/null >/dev/null 2>&1 & exec "$0" "$@" </dev/null 3<&-';

/**
 * The argv that runs `argv` in a process group that ends with this process, however this process
 * ends: its own exit, a signal it handles by ending through `process.reallyExit` (as Oh My Pi's
 * SIGINT, SIGTERM, SIGHUP and fatal-error handlers do, which skips the `exit` event), or a SIGKILL
 * nothing can handle. The process need not run anything as it goes: the kernel closes the pipe it
 * held, and the watch in the group sees that.
 *
 * Spawn it detached, with `stdin: "pipe"`, and keep the returned subprocess's stdin open while the
 * command may run; close it (`end()`) once the caller is done with the group, after its own
 * `killGroup`. `argv` replaces the watching shell under the same pid, so `exited`, the exit status
 * and `killGroup` are the command's own; `argv` reads /dev/null. A first word the shell cannot run
 * exits 127 rather than failing the spawn. Linux and macOS: `/bin/sh` and its `read` and `kill`
 * builtins.
 */
export function groupWatchedBy(argv: readonly string[]): string[] {
  return ["/bin/sh", "-c", WATCH, ...argv];
}
