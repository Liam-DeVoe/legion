/** The one way the e2e code and its rows signal a process group: `signalGroup`. It imports
 *  nothing, so `e2e/local-instance-support.ts` and `e2e/tools/unit-repeat.ts` both import it
 *  without a cycle. */

/** The refusal `signalGroup` throws. A caller whose catch reads every other error as a group
 *  that has already ended (ESRCH) throws this one on, so a refusal is never read as that. */
export class RefusedGroup extends Error {}

/** Sends `signal` to every process of the group `leader` leads: `process.kill(-leader, signal)`.
 *  A `leader` that is not an integer above 1 is refused with a RefusedGroup naming it, and gets
 *  no signal. kill reads group 0 as the group of the process that calls it, which under a test
 *  runner holds the runner too, and group 1 as -1, every process this one may signal; a negative
 *  `leader` would name the one process of its absolute value. On gVisor the group of a process
 *  that had just exited once read as 0. The errors `process.kill` throws, ESRCH among them, are
 *  the caller's, as they were before. */
export function signalGroup(leader: number, signal: NodeJS.Signals | number): void {
  if (!Number.isInteger(leader) || leader <= 1) {
    throw new RefusedGroup(
      `refusing to signal process group '${leader}': a group is an integer above 1, since kill ` +
        "reads group 0 as the group it is called from and group 1 as -1, every process it may signal"
    );
  }
  process.kill(-leader, signal);
}
