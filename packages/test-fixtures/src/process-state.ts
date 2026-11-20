import { readdirSync, readFileSync } from "node:fs";

/**
 * Whether a process a test started has ended, judged by the process's own state rather than by
 * whether anything has reaped it.
 *
 * A process that is no child of the test process, as one whose own parent has exited, is reaped
 * by whatever the kernel hands it to. Where that is the test process itself, as where `bun test`
 * runs as PID 1 of its PID namespace, nothing ever reaps it: Bun reaps only the children it
 * started. Its zombie stays in the process table, and signal 0 still reaches it, so a test that
 * waits for signal 0 to fail waits for ever there. The state letter of its `/proc/<pid>/stat`
 * says it has ended all the same.
 *
 * Each reads one process's own entry, never the whole process table, so no other process of the
 * machine can hold the read.
 */

/** The state letter of process `pid`, field 3 of its `/proc/<pid>/stat` (proc(5)): `R`, `S`, `D`,
 *  `Z`, `X` and the rest. It is read after the last `)`, the one that ends the command name, since
 *  that name may hold spaces and parentheses of its own. Undefined where `/proc` lists no such
 *  process, as for a process that has been reaped, or on a machine with no `/proc`. */
export function processState(pid: number): string | undefined {
  let stat: string;
  try {
    stat = readFileSync(`/proc/${pid}/stat`, "utf8");
  } catch {
    return undefined;
  }
  return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[0];
}

/** The threads of process `pid` its `/proc/<pid>/task` directory lists, by thread id. Undefined
 *  where `/proc` lists no such process. The kernel lists a task directory from the process's own
 *  thread list, taking no lock another process can hold, so the read cannot be held the way a read
 *  of a process's `stat` or `environ` can. */
export function processThreads(pid: number): string[] | undefined {
  try {
    return readdirSync(`/proc/${pid}/task`);
  } catch {
    return undefined;
  }
}

/** Whether process `pid` has ended: `/proc` lists it dead (`X`), or a zombie (`Z`) whose task
 *  directory lists no thread but its main one, or lists it not at all and signal 0 answers that no
 *  such process exists (`ESRCH`), which also answers on a machine with no `/proc`. A process whose
 *  main thread exited while another thread runs on reads `Z` too, and is still there: it still
 *  holds its files, its ports and its locks. A signal 0 refused for any other reason, as `EPERM`
 *  for another user's process, reaches a process that runs. `stateOf` reads the state letter and
 *  `threadsOf` the task directory: `processState` and `processThreads` by default, and stand-ins in
 *  this module's own rows, which hold the signal 0 answer where `/proc` lists nothing.
 *
 *  `pid` is a positive integer, or this throws: `process.kill(0, 0)` signals the caller's own
 *  process group and `process.kill(NaN, 0)` throws, so a pid file read wrong would otherwise read
 *  as a process that runs, or as one that ended. */
export function processEnded(
  pid: number,
  stateOf: (pid: number) => string | undefined = processState,
  threadsOf: (pid: number) => string[] | undefined = processThreads
): boolean {
  if (!Number.isSafeInteger(pid) || pid <= 0) {
    throw new RangeError(`${pid} is no process id: a pid is a positive integer`);
  }
  const state = stateOf(pid);
  if (state === "X") return true;
  if (state === "Z") return (threadsOf(pid) ?? []).every((thread) => thread === String(pid));
  if (state !== undefined) return false;
  try {
    process.kill(pid, 0);
    return false;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "ESRCH";
  }
}
