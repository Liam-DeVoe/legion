/** The signals that end a run a person or a supervisor stops. */
export type StopSignal = "TERM" | "INT" | "HUP";

/**
 * The line lib/run-verdict.sh prints above the FAIL line of a run that ended with status 0 before
 * its end, as a signal or an exit of its own ends it.
 */
export const stopped =
  "the run stopped before its end, by a signal or an exit of its own: its status 0 is no pass";

/**
 * A stand-in command's own check of where to stop the run, as shell: `stop_here POINT` signals its
 * whole process group with STAND_IN_KILL_SIGNAL when STAND_IN_KILL_AT names POINT, as a closed
 * pane (HUP), a Ctrl-C (INT) or timeout's TERM does, and then waits to be ended with it. It waits
 * half a second first, so the shell has started every command of the pipeline the stand-in runs
 * in: a child bash that has forked and not yet run its command still holds the shell's traps, and
 * the signal would run the EXIT trap there as well, a window a real command, which runs for
 * minutes, never stops in. A stand-in that calls it must run in a process group of its own:
 * `inOwnGroup` below runs it that way.
 */
export const stopHere = `stop_here() {
  [ "\${STAND_IN_KILL_AT:-}" = "$1" ] || return 0
  sleep 0.5
  kill -s "$STAND_IN_KILL_SIGNAL" 0
  sleep 5
}`;

/** How a run ended: its exit status or the signal that ended it, and what it printed. */
type GroupRun = { code: number | null; signal: string | null; out: string; err: string };

/**
 * Runs COMMAND (argv) in a session and process group of its own (`setsid -w`), so a stand-in's
 * signal to its group reaches the whole run and nothing of the test's. A run whose output goes
 * through lib/transcript.sh prints its stderr on stdout too.
 */
export function inOwnGroup(command: string[], env: Record<string, string>): GroupRun {
  const result = Bun.spawnSync(["setsid", "-w", ...command], { env });
  return {
    code: result.exitCode,
    signal: result.signalCode ?? null,
    out: result.stdout.toString(),
    err: result.stderr.toString(),
  };
}
