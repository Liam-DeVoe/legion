import { existsSync, lstatSync, mkdtempSync, symlinkSync } from "node:fs";
import { join } from "node:path";

// A proc root of a test's own processes, for code under test that reads `${proc_root:-/proc}`
// (procs_naming and so the run's sweep, listening_holders, refuse_leftovers, and
// parked-decision-devbox.sh's stop_agent_daemons): a directory holding a link to the real
// /proc/<pid> of each process the test names, so that code reads those processes' own files and
// signals their real pids, and its work is the test's processes rather than the host's. A harness
// shell makes the same root for itself with one `ln -s` an entry.

/** A fresh proc root under `dir`: a link to /proc/<pid> for each of `pids` still running. */
export function procRootOf(dir: string, pids: readonly number[]): string {
  return linkProcesses(mkdtempSync(join(dir, "proc-")), pids);
}

/**
 * Gives the proc root `root` a link to /proc/<pid> for each of `pids` still running that it holds
 * no entry for yet, and answers `root`: how a row gives its own processes to a root a hook made
 * ahead of it, as one holding the force's entries (lib/arg-max-force.ts).
 */
export function linkProcesses(root: string, pids: readonly number[]): string {
  for (const pid of new Set(pids)) {
    const link = join(root, String(pid));
    if (existsSync(`/proc/${pid}`) && lstatSync(link, { throwIfNoEntry: false }) === undefined) {
      symlinkSync(`/proc/${pid}`, link);
    }
  }
  return root;
}

/**
 * Bun's own limit of a row that names none, in milliseconds.
 */
export const bunRowLimit = 5_000;

/**
 * What a row keeps of its limit for what it does after its walk, in milliseconds: reading the
 * walk's answer and asserting on it, a few milliseconds of work on a quiet machine, with room for
 * a loaded one. A walk held to the row's limit less this ends, naming its bound, before the row's
 * limit ends the row with no word of the walk.
 */
export const walkReserve = 1_000;

/**
 * The environment entry that holds a walk of every process (`proc_walk_deadline`,
 * lib/process-tree.sh) to its row's own limit: LIMIT milliseconds from STARTED, a `Date.now()`
 * read as the row began, less walkReserve, in seconds since the epoch. The walk then gets what is
 * left of the row's limit however slow the machine is, rather than a bound of its own measured on
 * a quieter one.
 */
export function walkDeadlineEnv(
  started: number,
  limit = bunRowLimit
): { readonly proc_walk_deadline: string } {
  return { proc_walk_deadline: ((started + limit - walkReserve) / 1000).toFixed(6) };
}
