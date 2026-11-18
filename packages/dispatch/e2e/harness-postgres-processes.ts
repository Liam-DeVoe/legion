/** What the kernel shows of the processes of this user, as the sweep of
 *  `e2e/harness-postgres-removal.ts` reads them before it acts on what a dead stack's mark names:
 *  the postmaster that serves a data directory (`postmasterOf`), each process that still holds a
 *  directory (`holdersOf`), and each postmaster that serves from a run directory the sweep's own
 *  listing does not read (`servedElsewhere`). Every read is of `/proc` and of the paths it names;
 *  none signals, renames or removes anything. The paths that the processes name are read in a
 *  child, each within a bound (`identities`), since one on a mount whose server hangs never
 *  answers. */
import { spawn } from "node:child_process";
import { lstatSync, readdirSync, readFileSync, readlinkSync, realpathSync } from "node:fs";
import { basename, dirname, isAbsolute, join } from "node:path";

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

/** The paths that `argument`, one argument of a process, can name, each as the process spells it:
 *  each piece between its spaces, `=`, `:`, `,` and quotes, as `postgres -D <data>`, `pg_ctl`'s
 *  `-o "-k <sockets>"` and Docker's `--mount type=bind,source=<sockets>,…` write it, but an
 *  option, and the value an option of one letter holds glued to it, as `-D<data>` writes it. */
function pathsIn(argument: string): string[] {
  return argument.split(/[\s=:,'"]+/).flatMap((piece) => {
    if (!piece.startsWith("-")) return piece === "" ? [] : [piece];
    const glued = /^-[A-Za-z](.+)$/.exec(piece)?.[1];
    return glued === undefined ? [] : [glued];
  });
}

/** How long one path that a process names may go unanswered while a start reads whether it leads
 *  into a directory (`holdersOf`): a stat of a path on a local file system answers within
 *  microseconds, and one on a network or FUSE mount whose server hangs never answers, and would
 *  hold the start in the kernel. Past this the start cannot judge that process, and leaves the
 *  directory. */
export const statMs = 10_000;
/** How long the child that reads those paths (`statScript`) may take to start: a `bun` child took
 *  more than 180 s once to start its sweep on a shared Linux machine under a load of 1,217, on
 *  2026-11-17. */
const statStartMs = 240_000;

/** The child that reads where each path leads: `process.execPath`, Bun or Node, reads the paths
 *  from its standard input, separated by NUL, says it is ready, and then writes one line for each
 *  path in turn as soon as it has read it: its device and inode, or `-` where it names nothing. */
const statScript = `
const { readFileSync, statSync, writeSync } = require("node:fs");
const paths = readFileSync(0, "utf8").split("\\0");
writeSync(1, "ready\\n");
for (const path of paths) {
  let line = "-";
  try {
    const stats = statSync(path, { bigint: true });
    line = stats.dev + ":" + stats.ino;
  } catch {}
  writeSync(1, line + "\\n");
}
`;

/** Each path that answered no stat within `statMs` in this process: it is not asked again, since
 *  a mount that hangs holds each later stat of it too. */
const unanswered = new Set<string>();

/** What one child (`statScript`) read of the paths it was given, in their order: whether it
 *  started, and where each path it read leads, "<dev>:<ino>", or null where it names nothing. */
interface ChildRead {
  readonly started: boolean;
  readonly read: readonly (string | null)[];
}

/** What one child (`statScript`) read of `paths` before it ended or was ended (`ChildRead`). It is
 *  ended where it has not started within `statStartMs`, or has read no next path within `statMs`,
 *  and nothing waits for a child that the kernel still holds after SIGKILL. */
function statInChild(paths: readonly string[]): Promise<ChildRead> {
  const { promise, resolve } = Promise.withResolvers<ChildRead>();
  const child = spawn(process.execPath, ["-e", statScript], { stdio: ["pipe", "pipe", "ignore"] });
  const read: (string | null)[] = [];
  let started = false;
  let pending = "";
  let done = false;
  let timer: Timer | undefined;
  const end = (): void => {
    if (done) return;
    done = true;
    clearTimeout(timer);
    child.kill("SIGKILL");
    child.stdin?.destroy();
    child.stdout?.destroy();
    child.unref();
    resolve({ started, read });
  };
  timer = setTimeout(end, statStartMs);
  child.stdout?.setEncoding("utf8");
  child.stdout?.on("data", (chunk: string) => {
    pending += chunk;
    for (let at = pending.indexOf("\n"); at !== -1; at = pending.indexOf("\n")) {
      const line = pending.slice(0, at);
      pending = pending.slice(at + 1);
      if (started) read.push(line === "-" ? null : line);
      else started = line === "ready";
      clearTimeout(timer);
      timer = setTimeout(end, statMs);
    }
    if (read.length === paths.length) end();
  });
  child.on("error", end);
  child.on("close", end);
  // A child that ends before it read all of its input closes its end of the pipe.
  child.stdin?.on("error", () => undefined);
  child.stdin?.end(paths.join("\0"));
  return promise;
}

/** Where each of `paths` leads, as children of this process read them (`statInChild`): its device
 *  and inode, "<dev>:<ino>", or null where it names nothing. A path that answered no stat within
 *  `statMs`, here or earlier in this process (`unanswered`), has no entry; each read after one is
 *  read by a child of its own. A child that did not start leaves the paths it was given without
 *  an entry too, and they are asked again at the next read. */
async function identities(paths: readonly string[]): Promise<Map<string, string | null>> {
  const found = new Map<string, string | null>();
  let rest = paths.filter((path) => !unanswered.has(path));
  while (rest.length > 0) {
    const { started, read } = await statInChild(rest);
    for (const [at, identity] of read.entries()) found.set(rest[at] ?? "", identity);
    const hung = rest[read.length];
    if (!started || hung === undefined) break;
    unanswered.add(hung);
    rest = rest.slice(read.length + 1).filter((path) => !unanswered.has(path));
  }
  return found;
}

/** What `holdersOf` read of a directory: each process that holds it, "<program> <pid>", and each
 *  process it cannot judge, since a path that process names (`path`) answered no stat within
 *  `statMs`. */
export interface Holding {
  readonly holders: readonly string[];
  readonly unread: readonly { readonly process: string; readonly path: string }[];
}

/** Each process of the user `uid` that still holds `directory`, a directory of a dead stack's
 *  mark, as the kernel shows it: its working directory, a file it holds open, or a path one of its
 *  arguments names (`pathsIn`), read from its working directory where it is relative, is that
 *  directory or in it. A holder is judged by the directory itself, its device and its inode, and
 *  by no spelling of it: a `pg_ctl` that a stack started under a TMPDIR spelt through a link, with
 *  a `-D` relative to where it runs or with a doubled `/`, holds it all the same. `spellings`, the
 *  paths the mark names it by, are read in the arguments as they are written too (`names`), for a
 *  spelling that no longer leads to it, such as a link removed since. A process the stack started
 *  that its death did not end holds a directory so: a `pg_ctl start` or an `initdb` still running,
 *  a `docker create` that mounts the socket directory, the postmaster such a `pg_ctl` is starting.
 *  A stack is not dead while one of them runs, so a start removes no directory it holds.
 *  `postmaster` is the pid of the postmaster `postmasterOf` verified, which this start stops
 *  itself, and neither it nor a process it started (its parent pid) is counted. A process that
 *  ends while it is read, or whose links this user may not read, holds nothing here, and nothing
 *  holds a directory that is gone. `/proc` is read here; where each path leads is read in a child,
 *  each path within `statMs` (`identities`), and a process none of whose paths leads into the
 *  directory, but one of whose paths did not answer, is one the start cannot judge (`unread`).
 *  Answers each holder and each process it cannot judge, in pid order (`Holding`). */
export async function holdersOf(
  directory: string,
  spellings: readonly string[],
  uid: number,
  postmaster?: number
): Promise<Holding> {
  let target: string;
  try {
    const stats = lstatSync(directory, { bigint: true });
    target = `${stats.dev}:${stats.ino}`;
  } catch {
    // A directory gone, or one whose identity this user cannot read, has no holder to read here;
    // the rename or the removal that follows says what it is.
    return { holders: [], unread: [] };
  }
  // What each process names, from `/proc` alone: whether an argument spells the directory as the
  // mark does, and each path it names, its working directory first.
  const named: { readonly process: string; readonly spelt: boolean; readonly paths: string[] }[] =
    [];
  for (const pid of readdirSync("/proc").filter((entry) => /^\d+$/.test(entry))) {
    try {
      if (Number(pid) === postmaster || lstatSync(`/proc/${pid}`).uid !== uid) continue;
      const parent = /^PPid:\s+(\d+)$/m.exec(readFileSync(`/proc/${pid}/status`, "utf8"))?.[1];
      if (postmaster !== undefined && Number(parent) === postmaster) continue;
      const cwd = readlinkSync(`/proc/${pid}/cwd`);
      const args = readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0");
      const open = readdirSync(`/proc/${pid}/fd`).flatMap((fd) => {
        try {
          const path = readlinkSync(`/proc/${pid}/fd/${fd}`);
          return isAbsolute(path) ? [path] : [];
        } catch {
          return [];
        }
      });
      named.push({
        process: `${basename(readlinkSync(`/proc/${pid}/exe`))} ${pid}`,
        spelt: args.some((argument) => spellings.some((path) => names(argument, path))),
        paths: [
          cwd,
          ...args.flatMap(pathsIn).map((path) => (isAbsolute(path) ? path : `${cwd}/${path}`)),
          ...open,
        ],
      });
    } catch {
      // A process that ended, or whose links this user may not read, holds nothing here.
    }
  }
  // Each path and each directory above it, read in one child: a path that names nothing, such as
  // a file a process will write, is in its parent.
  const every = new Set<string>();
  for (const { paths } of named) {
    for (let path of paths) {
      while (!every.has(path)) {
        every.add(path);
        const parent = dirname(path);
        if (parent === path) break;
        path = parent;
      }
    }
  }
  const leads = await identities([...every]);
  // Whether `path` is `directory` or in it, or undefined where a stat of it or of a directory
  // above it did not answer and none above it is the directory.
  const known = new Map<string, boolean | undefined>();
  const inside = (path: string): boolean | undefined => {
    if (known.has(path)) return known.get(path);
    const parent = dirname(path);
    const above = parent === path ? false : inside(parent);
    const self = leads.has(path) ? leads.get(path) === target : undefined;
    const answer =
      self === true || above === true
        ? true
        : self === false && above === false
          ? false
          : undefined;
    known.set(path, answer);
    return answer;
  };
  const holders: string[] = [];
  const unread: { process: string; path: string }[] = [];
  for (const { process, spelt, paths } of named) {
    const judged = paths.map(inside);
    if (spelt || judged.includes(true)) {
      holders.push(process);
      continue;
    }
    const path = paths[judged.indexOf(undefined)];
    if (path !== undefined) unread.push({ process, path });
  }
  return { holders, unread };
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
