/** What the kernel shows of the processes of this user, as the sweep of
 *  `e2e/harness-postgres-removal.ts` reads them before it acts on what a dead stack's mark names:
 *  the postmaster that serves a data directory (`postmasterOf`), each process that still holds a
 *  directory (`holdersOf`), and each postmaster that serves from a run directory the sweep's own
 *  listing does not read (`servedElsewhere`). Every read is of `/proc`; none signals, renames or
 *  removes anything. */
import { lstatSync, readdirSync, readFileSync, readlinkSync, realpathSync } from "node:fs";
import { basename, dirname, join } from "node:path";

import {
  codeOf,
  readUnlinked,
  runDirectoryPrefix,
  running,
  startOf,
} from "./harness-postgres-marks";

/** A postmaster as `postmasterOf` verified it: its pid, and its start in clock ticks since boot. */
export interface Postmaster {
  readonly pid: number;
  readonly start: string;
}

/** The postmaster that serves the cluster in `data`, or undefined where none does, as the kernel
 *  shows it: the pid that `postmaster.pid` names, where the process with that pid has `data` for
 *  its working directory, which a postmaster's is from its start (Postgres's `ChangeToDataDir`),
 *  and its start, read before and after the checks, is the same both times. `data` is the one
 *  spelling a mark's paths are acted on under (`markedPaths`), with no link in it, which is how
 *  the kernel names a working directory. A `postmaster.pid` that a postmaster killed hard left,
 *  whose pid another process has since, names no postmaster here. Throws, naming the check, where
 *  that process runs as another user than `uid` or runs another program than `bin`'s `postgres`,
 *  and where it cannot tell. */
export function postmasterOf(data: string, bin: string, uid: number): Postmaster | undefined {
  let pidFile: string;
  try {
    pidFile = readUnlinked(join(data, "postmaster.pid"));
  } catch (error) {
    if (codeOf(error) === "ENOENT" || codeOf(error) === "ENOTDIR") return undefined;
    throw error;
  }
  const pid = Number.parseInt(pidFile, 10);
  if (!Number.isSafeInteger(pid) || pid <= 0) return undefined;
  const names = `the postmaster.pid of ${data} names pid ${pid}`;
  try {
    const before = startOf(readFileSync(`/proc/${pid}/stat`, "utf8"));
    if (readlinkSync(`/proc/${pid}/cwd`) !== data) return undefined;
    // The real, effective, saved and file system user of the process, in that order.
    const users = /^Uid:\s+(.+)$/m.exec(readFileSync(`/proc/${pid}/status`, "utf8"))?.[1];
    if (users?.split(/\s+/).some((user) => user !== String(uid)) !== false) {
      throw new Error(`${names}, which runs as user ${users ?? "unknown"}, not ${uid} alone`);
    }
    const program = readlinkSync(`/proc/${pid}/exe`);
    let postgres = join(bin, "postgres");
    try {
      postgres = realpathSync(postgres);
    } catch {
      // A postgres that is gone is the program of no process, so the comparison below fails.
    }
    if (program !== postgres) throw new Error(`${names}, which runs ${program}, not ${postgres}`);
    if (!running(readFileSync(`/proc/${pid}/stat`, "utf8"), before)) return undefined;
    return { pid, start: before };
  } catch (error) {
    if (codeOf(error) === "ENOENT") return undefined;
    throw error;
  }
}

/** Whether `argument`, one argument of a process, names `path` whole: after its start or a space,
 *  a `=`, a `:`, a `,` or a quote, and before its end, a `/` or one of those, as `postgres -D
 *  <data>`, `pg_ctl`'s `-o "-k <sockets>"` and Docker's `--mount type=bind,source=<sockets>,…`
 *  write it. */
function names(argument: string, path: string): boolean {
  for (let at = argument.indexOf(path); at !== -1; at = argument.indexOf(path, at + 1)) {
    const before = argument[at - 1] ?? "";
    const after = argument[at + path.length] ?? "";
    if (/^[\s=:,'"]?$/.test(before) && /^[\s=:,'"/]?$/.test(after)) return true;
  }
  return false;
}

/** Each process of the user `uid` that still holds one of `paths`, the directories of a dead
 *  stack's mark, as the kernel shows it: its working directory, or a file it holds open, is that
 *  directory or in it, or one of its arguments names it (`names`). A process the stack started
 *  that its death did not end holds them so: a `pg_ctl start` or an `initdb` still running, a
 *  `docker create` that mounts the socket directory, the postmaster such a `pg_ctl` is starting.
 *  A stack is not dead while one of them runs, so a start removes no directory it holds.
 *  `postmaster` is the pid of the postmaster `postmasterOf` verified, which this start stops
 *  itself, and neither it nor a process it started (its parent pid) is counted. A process that
 *  ends while it is read, or whose links this user may not read, holds nothing here. Answers
 *  "<program> <pid>" for each holder, in pid order. */
export function holdersOf(paths: readonly string[], uid: number, postmaster?: number): string[] {
  const within = (target: string) =>
    paths.some((path) => target === path || target.startsWith(`${path}/`));
  const holders: string[] = [];
  for (const pid of readdirSync("/proc").filter((entry) => /^\d+$/.test(entry))) {
    try {
      if (Number(pid) === postmaster || lstatSync(`/proc/${pid}`).uid !== uid) continue;
      const parent = /^PPid:\s+(\d+)$/m.exec(readFileSync(`/proc/${pid}/status`, "utf8"))?.[1];
      if (postmaster !== undefined && Number(parent) === postmaster) continue;
      const holds =
        within(readlinkSync(`/proc/${pid}/cwd`)) ||
        readFileSync(`/proc/${pid}/cmdline`, "utf8")
          .split("\0")
          .some((argument) => paths.some((path) => names(argument, path))) ||
        readdirSync(`/proc/${pid}/fd`).some((fd) => {
          try {
            return within(readlinkSync(`/proc/${pid}/fd/${fd}`));
          } catch {
            return false;
          }
        });
      if (holds) holders.push(`${basename(readlinkSync(`/proc/${pid}/exe`))} ${pid}`);
    } catch {
      // A process that ended, or whose links this user may not read, holds nothing here.
    }
  }
  return holders;
}

/** How a postmaster's working directory, its data directory, names the run directory it serves
 *  from: the run directory, then its stack's place in it. A run directory someone renamed after
 *  its stack started has another name than the pool gives one, and is found all the same. A data
 *  directory a start claimed (`claimForRemoval` of `e2e/harness-postgres-removal.ts`) and that
 *  start's death left claimed has the claim's name, `pgdata.removing-<pid>-<start>`, which the
 *  kernel shows as the postmaster's working directory, and is found under the name its mark
 *  holds, without the claim. */
const servedFrom = /^(.+)\/(?:worker-\d+|template)\/pgdata(?:\.removing-\d+-\d+)?$/;
/** The claim a claimed data directory's name ends in (`claimInfix` and `claimForRemoval` of
 *  `e2e/harness-postgres-removal.ts`). */
const claimed = /\.removing-\d+-\d+$/;

/** A postmaster the kernel shows serving from a run directory that this start's listing of its
 *  TMPDIR does not read, with its pid and its start as the listing read them. */
export interface Served extends Postmaster {
  readonly run: string;
  readonly data: string;
}

/** Each postmaster of the user `uid` that serves from a run directory that the listing of `real`,
 *  this start's TMPDIR, does not read, as the kernel shows it: a process of that user (the owner of
 *  `/proc/<pid>`) whose program is a `postgres`, whose working directory has the shape of a
 *  stack's data directory in a run directory (`servedFrom`), and which that directory's
 *  `postmaster.pid` names, as it names a postmaster and none of its children. That file holds
 *  the pid the postmaster's own PID namespace gives it, the last of its `NSpid`, which is its pid
 *  here where it runs in this start's PID namespace. The run directories of `real` that the pool
 *  named are the listing's; one of another name there, a run directory renamed after its stack
 *  started, is found here. That shape only finds a candidate: the mark in that run directory, its
 *  owner and the postmaster's own `/proc` facts decide (`sweepRun`). A process that ends while it
 *  is read is not listed. */
export function servedElsewhere(real: string, uid: number): Served[] {
  let pids: string[];
  try {
    pids = readdirSync("/proc").filter((entry) => /^\d+$/.test(entry));
  } catch {
    return [];
  }
  const served: Served[] = [];
  for (const pid of pids) {
    try {
      if (lstatSync(`/proc/${pid}`).uid !== uid) continue;
      const start = startOf(readFileSync(`/proc/${pid}/stat`, "utf8"));
      if (basename(readlinkSync(`/proc/${pid}/exe`)) !== "postgres") continue;
      const data = readlinkSync(`/proc/${pid}/cwd`);
      const run = servedFrom.exec(data)?.[1];
      if (run === undefined) continue;
      if (dirname(run) === real && basename(run).startsWith(runDirectoryPrefix)) continue;
      const status = readFileSync(`/proc/${pid}/status`, "utf8");
      const own = /^NSpid:.*?(\d+)[ \t]*$/m.exec(status)?.[1] ?? pid;
      if (Number.parseInt(readUnlinked(join(data, "postmaster.pid")), 10) === Number(own)) {
        served.push({ run, data: data.replace(claimed, ""), pid: Number(pid), start });
      }
    } catch {
      // A process that ended, or whose links this user may not read, serves from nothing here.
    }
  }
  return served.sort((first, second) => first.data.localeCompare(second.data));
}
