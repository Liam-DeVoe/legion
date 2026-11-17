// The process group a command runs in, and what its caller does with the group at the end: an
// envoy.json `!` command (`fileValue`, ./envoy-config), a person's `!command` answered through
// Dispatch (pi-envoy's typed-shell), AskGate's dry run (pi-envoy's askgate-core) and jj's
// attribution read (pi-shared's jj-attribution). Each starts its command detached, in a group of
// its own, so only a kill of the group reaches every process the command started. Both host
// plugins depend on this package, so the one copy lives here.

/** Kills every process of the group `pid` leads, if any is left. */
export function killGroup(pid: number): void {
  try {
    process.kill(-pid, "SIGKILL");
  } catch {
    // The group is already gone.
  }
}

/** The cleanups to run as this process exits, each until the release its registration returned. */
const atExit = new Set<() => void>();
let listening = false;

/**
 * Runs `cleanup` if this process exits before the returned release is called. A command's group
 * is its own, so nothing but a kill ends it, and a group left behind when the process goes is a
 * command nobody stops. It runs for every way out that fires `exit` (the event loop running dry,
 * `process.exit`), and not for a signal the process does not handle. `cleanup` must be
 * synchronous, as an `exit` listener is. One listener serves every registration.
 */
export function onExit(cleanup: () => void): () => void {
  if (!listening) {
    listening = true;
    process.on("exit", () => {
      for (const run of atExit) run();
    });
  }
  atExit.add(cleanup);
  return () => {
    atExit.delete(cleanup);
  };
}
