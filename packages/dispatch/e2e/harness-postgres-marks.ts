/** The mark each stack's Postgres carries, which names the process that started it, and the
 *  sweep with which the next start of a stack removes each Postgres whose process is gone.
 *
 *  A stack killed hard (SIGKILL) runs no stop, so its Postgres outlives it: the binaries
 *  provider's postmaster is a daemon of `pg_ctl`, the image provider's container runs detached,
 *  and neither the stops of `e2e/harness-postgres.ts` nor the pool's teardown
 *  (`e2e/harness-pool.ts`) runs. So each Postgres is marked before it can outlive its stack, and
 *  each start of a stack (`startStacks`) first removes every Postgres whose mark names a process
 *  that is gone.
 *
 *  WHAT A MARK IS. One JSON record (`Mark`). Its owner is the process that started the Postgres:
 *  its pid, its start time (field 22 of `/proc/<pid>/stat`, in clock ticks since boot), the boot
 *  id (`/proc/sys/kernel/random/boot_id`), its PID namespace (the link `/proc/self/ns/pid`), its
 *  time namespace (the link `/proc/self/ns/time`) and its user id. The rest names what the
 *  Postgres leaves: the run directory, as the stack spells it and as its one spelling
 *  (`identityOf`), the socket directory, and the postmaster's data directory and the directory of
 *  its binaries, or the container's name. Each Postgres has the record as a file in
 *  `postgres-marks/` of the run directory, written before its socket directory holds anything
 *  and before its data directory or its container exists, and written through a rename, so it is
 *  whole or absent. A container also carries the record as its label `markLabel`, which the
 *  `docker create` that makes it sets, so it is marked from the moment it exists, and a start
 *  finds it whatever directory its stack ran in. A shell stack (`scripts/e2e/lib/leftovers.sh`)
 *  marks its Postgres container the same way, through `e2e/postgres-mark.ts`: the owner is its
 *  driver, the shell whose death ends the run, the run directory is its work directory, and the
 *  label is the whole mark, with no socket directory, since that container serves on a port
 *  Docker publishes. A label is the mark of the container whose name it names alone: `docker
 *  commit` copies a container's labels into its image, and each container made from that image
 *  carries them.
 *
 *  ONE SPELLING OF A RUN DIRECTORY. A stack may spell its run directory through a link, as a
 *  `TMPDIR` of its user's that is a link to a scratch disk. The mark holds that directory with
 *  every link in it resolved (`identityOf`), read once, where the mark is written; a start lists
 *  the run directories of its own `TMPDIR` resolved the same way; and both sweeps act on what a
 *  mark names under that one spelling (`markedPaths`), never on another. So the container sweep
 *  and the run directory sweep read one stack's paths alike, and each check of `whyUntrusted`
 *  reads the directories a removal acts on, with no link in them.
 *
 *  WHERE A START LOOKS. Three places: each run directory of its own `tmpdir()`; each container
 *  Docker lists with the label; and each other run directory that the kernel shows a postmaster of
 *  this user serving from (`servedElsewhere`), which is how most leftovers are found on a machine
 *  where each session has a `TMPDIR` of its own, and how a run directory someone renamed, or the
 *  stack of another PID namespace, is found. A run directory of another `TMPDIR` whose postmaster
 *  has already ended holds no running server, and no start finds it.
 *
 *  WHEN AN OWNER IS GONE. Only for a mark of this process's own boot, PID namespace, time
 *  namespace and user, which is where `/proc/<pid>` names the owner's process, where its start
 *  reads as the owner read it, and where a process of the owner's user is never hidden from this
 *  one: no process has that pid, or the process that has it started at another time, or it is a
 *  zombie, which runs no more. A mark of another boot (another machine on a shared Docker daemon,
 *  or this one before it restarted), of another PID namespace (a container of its own), of
 *  another time namespace or of another user is left, since its owner cannot be seen, or its
 *  start read, from here: the kernel adds the reader's own boot-time offset to the start it shows
 *  (`unshare --time`), so a reader in another time namespace reads another start for a process
 *  that still runs. The boot id is what tells two machines apart: the first PID namespace has the
 *  same inode on every Linux host. A stack that runs where there is no `/proc` writes a mark with
 *  no owner, and no start judges it; a start where there is no `/proc` judges no mark. Both are
 *  left.
 *
 *  WHO CAN HAVE WRITTEN A MARK. A start acts as the user who starts the stack, and the run
 *  directories it reads are in `TMPDIR`, which is `/tmp` by default, where every user may make an
 *  entry. So a start reads a mark, and acts on a path it names, only where no other user can have
 *  put either there (`whyUntrusted`): every component of the path, from `/` down, is read with
 *  `lstat`, which follows no link, and is a directory (the mark itself a file), never a link;
 *  each belongs to root or this user, and the run directory, the mark and every directory a start
 *  removes belong to this user; and none that another user can reach may be written by another
 *  user, but for a directory with the sticky bit above them, as `/tmp` is, which keeps every user
 *  from renaming or removing another's entries. Under a directory of this user's that no other
 *  user may enter (mode 0700, as `mkdtemp` makes a run directory), no other user reaches anything,
 *  so below it only links and owners are read. A directory of a guessed name that another user
 *  made first in `/tmp` is that user's, so it is left; `mkdtemp` picks another name rather than
 *  take it. A container's label is written by whoever can reach the Docker daemon, which on a
 *  machine is root or a user Docker treats as root; the path it names is held to the same rules.
 *
 *  WHAT A START REMOVES. Exactly what a mark of a gone owner names, and only where every check
 *  above held: the container by the id under which Docker lists its label; the postmaster, by the
 *  pid that the start verified itself; and then the data and socket directories the mark names,
 *  each of them in the run directory or, for a socket directory, in `/tmp`. A name only finds a
 *  candidate (a run directory's prefix, a container's label key); the mark decides. A mark whose
 *  paths are not the shapes the providers make is not read. A start runs no program that a mark
 *  names: the mark's `bin` is only compared with the program a process runs. The postmaster is
 *  the pid of its `postmaster.pid`, where the kernel shows that process running as this user,
 *  with that data directory for its working directory and the mark's `postgres` for its program,
 *  with the same start before and after those reads; a start sends it SIGQUIT, the immediate
 *  shutdown that `pg_ctl -m immediate stop` asks for, and SIGKILL where it still runs a while
 *  later, each only while `/proc` names that process by its pid and its start. A pid that a stale
 *  file names and another process now has, with another working directory, gets no signal, and
 *  only the directories go; one whose working directory is that data directory but which is not
 *  this user's or runs another program is left, since a directory is never removed under a
 *  process. A data directory goes only once its postmaster is gone. A container's socket
 *  directory and its mark file go only with the container or after it: the container sweep
 *  removes a container and then its socket directory, and the run directory sweep removes them
 *  for a container Docker no longer lists, never under one that it still lists. The rest of a
 *  dead run's directory, its logs, stays, as a run that keeps its logs leaves them
 *  (`e2e/harness-pool.ts`).
 *
 *  WHAT A START LEAVES, AND SAYS SO. A mark whose owner lives is left without a word. Every other
 *  mark it leaves is named on one line per kind: what it cannot read (another user's run
 *  directory of mode 0700, a file that is not a mark, a label that is the mark of another
 *  container, a `docker ps` that failed), what it does not trust (a check of `whyUntrusted`,
 *  named), and what it cannot judge (another boot, PID namespace, time namespace or user, or no
 *  `/proc`).
 *  A line that says `removed` names only what that start itself removed and is gone. A removal
 *  that fails, or a postmaster that fails a check, is one line, keeps its mark for the next
 *  start, and never fails the start. Two starts that find one dead mark at once both try, and the
 *  line of each says what it did: a directory is renamed to a name of the start's own before it
 *  is removed, so one start wins and the other says that another start removed it first; a
 *  container that another start removes is named so, with what Docker said (its `docker rm`
 *  prints no id, or Docker refuses it while the other removal is in progress, and then the start
 *  waits until Docker no longer lists that container).
 *
 *  THE WINDOWS NO MARK COVERS. A stack killed between the `mkdtemp` of a socket directory and the
 *  rename of its mark, calls with no await between them, leaves that empty directory and at most
 *  a `.partial` file of the mark, which no start reads. A stack killed while its `pg_ctl start`,
 *  `docker create` or `docker start` is still running leaves that child running: a start in the
 *  moment before the postmaster writes `postmaster.pid`, or before Docker makes the container,
 *  finds neither and removes the directories, and a postmaster that comes up after that may run
 *  on with its data directory gone and no mark that names it. A container that comes up after
 *  that carries its label for the next start. A pid that a dead postmaster leaves can be taken by
 *  another process between the checks of that process and the signal, which only pid reuse
 *  within those milliseconds allows. A start killed while it removes a directory leaves what is
 *  left of it under the name it renamed it to (`<path>.removing-<pid>-<hex>`), which no mark
 *  names. */
import { execFile } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  closeSync,
  constants,
  lstatSync,
  mkdirSync,
  openSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  realpathSync,
  renameSync,
  rmSync,
  type Stats,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, isAbsolute, join, normalize, relative } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

/** What this module's own lines are prefixed with, as every starter in `e2e/` names itself. */
const named = "e2e/harness-postgres-marks.ts";

/** What the name of a run's own temporary directory starts with: the pool makes it with
 *  `mkdtemp`, and a start reads the marks of each directory of `tmpdir()` so named. */
export const runDirectoryPrefix = "dispatch-e2e-run-";
/** The directory of a run directory that holds one mark file per Postgres. */
export const marksDirectory = "postgres-marks";
/** The container label that holds the mark of an image provider's container. */
export const markLabel = "dev.legion.dispatch-e2e.postgres-mark";
/** The directory a long `TMPDIR` falls back to, as each module's `internal/testtmp` does; a
 *  socket directory is in the run directory or here (`socketDirectory` of
 *  `e2e/harness-postgres.ts`). */
export const shortTemporaryDirectory = "/tmp";

/** How long a `docker ps` or a `docker rm` may take, and how long a start waits for a removal of
 *  another start's that Docker says is in progress. A daemon that hangs would otherwise hold every
 *  start; one that answers lists or removes a container in well under a second. */
const dockerMs = 60_000;
/** How often a start asks Docker whether a container another start removes is still listed. */
const dockerPollMs = 200;
/** How long a dead stack's postmaster may take to end after SIGQUIT: as long as `pg_ctl`'s own
 *  wait for a stop, `PGCTLTIMEOUT`, 60 s by default. A postmaster that gets it ends its children,
 *  kills one that has not ended within 5 s, and exits. */
const postmasterQuitMs = 60_000;
/** How long it may take to end after SIGKILL, which ends it the next time it runs: past this the
 *  kernel holds it, and the start goes on with one line. */
const postmasterKillMs = 10_000;
/** How often a start reads `/proc` while it waits for a postmaster to end. */
const postmasterPollMs = 50;

/** The process that started a Postgres, as `/proc` names it in one boot, PID namespace and time
 *  namespace. */
export interface Owner {
  readonly pid: number;
  /** Field 22 of `/proc/<pid>/stat`, the process's start in clock ticks since boot, as the
   *  owner's own time namespace shifts it. */
  readonly start: string;
  /** `/proc/sys/kernel/random/boot_id`. */
  readonly boot: string;
  /** The target of `/proc/self/ns/pid`, such as `pid:[4026531836]`. */
  readonly pidNamespace: string;
  /** The target of `/proc/self/ns/time`, such as `time:[4026531834]`, or "" on a kernel with no
   *  time namespaces, where every process reads one clock. */
  readonly timeNamespace: string;
  readonly uid: number;
}

/** One Postgres of a stack: the process that started it, null where that process's machine has
 *  no `/proc`, and what the Postgres leaves when that process ends without stopping it. */
export interface Mark {
  readonly owner: Owner | null;
  /** The run's own temporary directory, the one the pool hands `stackPostgres`, or a shell
   *  stack's work directory. */
  readonly run: string;
  /** `run` as `identityOf` read it when the mark was written: the one spelling of the run
   *  directory that every sweep acts on (`markedPaths`). */
  readonly identity: string;
  /** The directory of its unix socket, in `run` or in `shortTemporaryDirectory`. Every postmaster
   *  has one; a container of a shell stack, which serves on a port Docker publishes, has none
   *  (`e2e/postgres-mark.ts`). */
  readonly sockets?: string;
  /** The binaries provider's cluster: the data directory and the directory of the binaries that
   *  run it, whose `postgres` a start compares with the program the postmaster runs. No start runs
   *  anything in it. */
  readonly postmaster?: { readonly bin: string; readonly data: string };
  /** The image provider's or a shell stack's container, by the name its `docker create` gave
   *  it. */
  readonly container?: string;
}

/** The fields of `stat`, the text of a `/proc/<pid>/stat`, from the third on, the state first.
 *  They are split after the last `)`, the one that ends the command name, since that name may
 *  hold spaces and parentheses of its own, so field N of proc(5) is at index N - 3. */
function statFields(stat: string): string[] {
  return stat.slice(stat.lastIndexOf(")") + 2).split(" ");
}

/** Field 22 of `stat`, the text of a `/proc/<pid>/stat`: the process's start in clock ticks since
 *  boot. */
export function startOf(stat: string): string {
  return statFields(stat)[22 - 3] ?? "";
}

/** The `code` of an error a call of `node:fs` threw. */
function codeOf(error: unknown): string | undefined {
  return (error as NodeJS.ErrnoException).code;
}

/** The first line of what an error says, for a log line. */
function firstLine(error: unknown): string {
  return (error instanceof Error ? error.message : String(error)).split("\n")[0] ?? "";
}

/** This process as an owner, read once: undefined until the first read, null where there is no
 *  owner to read. */
let thisOwner: Owner | null | undefined;

/** This process as the owner of the Postgres it starts, or null where it cannot be told: where
 *  there is no `/proc` (macOS), and where `/proc` gives this process another pid than
 *  `process.pid`, which is a `/proc` of another PID namespace than its own. */
export function stackOwner(): Owner | null {
  if (thisOwner === undefined) thisOwner = readOwner("self", process.pid);
  return thisOwner;
}

/** The process `pid` of this PID namespace as an owner, or null where it cannot be told: this
 *  process as `stackOwner` reads it, or another, such as the driver of a shell stack, which names
 *  itself by its pid (`e2e/postgres-mark.ts`). */
export function processOwner(pid: number): Owner | null {
  if (pid === process.pid) return stackOwner();
  // `/proc` names that pid as this process's own PID namespace does only where it names this
  // process by `process.pid`.
  if (stackOwner() === null) return null;
  return readOwner(String(pid), pid);
}

/** The owner `/proc/<entry>` names, where its pid is `pid`: "self" for this process. */
function readOwner(entry: string, pid: number): Owner | null {
  const uid = process.getuid?.();
  if (uid === undefined) return null;
  try {
    const stat = readFileSync(`/proc/${entry}/stat`, "utf8");
    if (Number.parseInt(stat, 10) !== pid) return null;
    return {
      pid,
      start: startOf(stat),
      boot: readFileSync("/proc/sys/kernel/random/boot_id", "utf8").trim(),
      pidNamespace: readlinkSync(`/proc/${entry}/ns/pid`),
      timeNamespace: timeNamespaceOf(entry),
      uid: entry === "self" ? uid : lstatSync(`/proc/${entry}`).uid,
    };
  } catch {
    return null;
  }
}

/** The target of `/proc/<entry>/ns/time`, or "" where the kernel has no time namespaces (Linux
 *  before 5.6), which no process then can be in. */
function timeNamespaceOf(entry: string): string {
  try {
    return readlinkSync(`/proc/${entry}/ns/time`);
  } catch (error) {
    if (codeOf(error) === "ENOENT") return "";
    throw error;
  }
}

/** The one spelling of the directory `path`, with every link in it resolved: the spelling a mark
 *  holds its run directory by, read once where the mark is written (`markPostgres`), and the one
 *  a start lists the run directories of its `TMPDIR` by. Throws where `path` cannot be resolved. */
export function identityOf(path: string): string {
  return realpathSync(path);
}

/** Writes the mark of one Postgres of the run in `run`, under `name`, and returns it. The caller
 *  writes it before the Postgres can outlive this process: before `pg_ctl start`, and before the
 *  `docker create` that carries it as `labelOf`. */
export function markPostgres(
  run: string,
  name: string,
  names: Pick<Mark, "sockets" | "postmaster" | "container">
): Mark {
  const mark: Mark = { owner: stackOwner(), run, identity: identityOf(run), ...names };
  const directory = join(run, marksDirectory);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const file = join(directory, `${name}.json`);
  // Written beside and renamed into place, so a start reads a whole mark or none: a mark cut off
  // mid-write would be one that no start can read, and its Postgres would stay.
  const partial = `${file}.${process.pid}.partial`;
  writeFileSync(partial, JSON.stringify(mark), { mode: 0o600 });
  renameSync(partial, file);
  return mark;
}

/** The `--label` value that carries `mark` on its container. */
export function labelOf(mark: Mark): string {
  return `${markLabel}=${JSON.stringify(mark)}`;
}

/** The mark of the container `container` of a shell stack whose run directory is `run` and whose
 *  owner is `owner`, its driver: a container that serves on a port Docker publishes, with no socket
 *  directory, carried as its label alone (`e2e/postgres-mark.ts`). */
export function containerMark(owner: Owner | null, run: string, container: string): Mark {
  return { owner, run, identity: identityOf(run), container };
}

/** The name `socketDirectory` gives a socket directory: `pg-`, the stack's index or `template`,
 *  `-`, and the six characters `mkdtemp` adds. */
const socketDirectoryName = /^pg-(?:\d+|template)-[A-Za-z0-9]{6}$/;
/** Where a binaries provider's data directory is in its run directory. */
const dataDirectory = /^(?:worker-\d+|template)\/pgdata$/;
/** A container name as Docker takes one. */
export const containerName = /^[A-Za-z0-9][A-Za-z0-9_.-]*$/;

/** Whether `path` is absolute, written with no `.`, `..`, doubled or trailing `/`, and not `/`. */
export function plainPath(path: unknown): path is string {
  return typeof path === "string" && isAbsolute(path) && normalize(path) === path && path !== "/";
}

/** Why another user could have put what is at `path`, or anything above it, where it is, or why a
 *  link leads through it, or undefined where no other user could and no link does. `lstat` reads
 *  each component from `/` down, following no link (`lstatSync`, or a test's own). Each component
 *  is a directory, and `path` itself one or a file as `kind` says; each belongs to root or the
 *  user `uid`, and `path` to that user; none above a directory that no other user may enter
 *  (one of `uid`'s with no permission for its group or others) may be written by its group or
 *  others, but for a directory with the sticky bit above `path`, whose entries only their owners
 *  may rename or remove. Where a component is gone there is nothing there to act on, and the
 *  answer is undefined too. */
export function whyUntrusted(
  path: string,
  uid: number,
  kind: "directory" | "file",
  lstat: (path: string) => Stats = lstatSync
): string | undefined {
  if (!plainPath(path)) return `${path} is not an absolute path written plain`;
  const names = path.split("/").slice(1);
  let closed = false;
  for (let depth = 0; depth <= names.length; depth++) {
    const component = depth === 0 ? "/" : `/${names.slice(0, depth).join("/")}`;
    const last = depth === names.length;
    let stat: Stats;
    try {
      stat = lstat(component);
    } catch (error) {
      if (codeOf(error) === "ENOENT") return undefined;
      return `${component} cannot be read (${codeOf(error) ?? firstLine(error)})`;
    }
    if (stat.isSymbolicLink()) return `${component} is a symbolic link`;
    const what = last ? kind : "directory";
    if (!(what === "file" ? stat.isFile() : stat.isDirectory())) {
      return `${component} is not a ${what}`;
    }
    if (stat.uid !== uid && (last || closed || stat.uid !== 0)) {
      return `${component} belongs to user ${stat.uid}`;
    }
    const othersWrite = (stat.mode & 0o022) !== 0;
    const sticky = (stat.mode & 0o1000) !== 0;
    if (!closed && othersWrite && (last || !sticky)) {
      return `${component} is writable by other users`;
    }
    if (stat.uid === uid && stat.isDirectory() && (stat.mode & 0o077) === 0) closed = true;
  }
  return undefined;
}

/** The text of the file `path`, opened with `O_NOFOLLOW`, so a link put in its place is not read
 *  through. */
function readUnlinked(path: string): string {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    return readFileSync(descriptor, "utf8");
  } finally {
    closeSync(descriptor);
  }
}

/** `value` as an owner, null where it is null, or undefined where it is neither. */
function ownerOf(value: unknown): Owner | null | undefined {
  if (value === null) return null;
  if (typeof value !== "object" || value === undefined) return undefined;
  const { pid, start, boot, pidNamespace, timeNamespace, uid } = value as Record<string, unknown>;
  if (
    typeof pid !== "number" ||
    !Number.isSafeInteger(pid) ||
    pid <= 0 ||
    typeof start !== "string" ||
    !/^\d+$/.test(start) ||
    typeof boot !== "string" ||
    boot === "" ||
    typeof pidNamespace !== "string" ||
    pidNamespace === "" ||
    typeof timeNamespace !== "string" ||
    typeof uid !== "number" ||
    !Number.isSafeInteger(uid) ||
    uid < 0
  ) {
    return undefined;
  }
  return { pid, start, boot, pidNamespace, timeNamespace, uid };
}

/** `text` as a mark, or undefined where it is not one the providers write: JSON with an owner or
 *  null, a run directory and its one spelling, which share their last name, a socket directory in
 *  that run directory or in /tmp under the name `socketDirectory` gives one, and exactly one of a
 *  postmaster whose data directory is a stack's or the template's in that run directory, and a
 *  container name. A container of a shell stack names no socket directory; every postmaster names
 *  one. Every path is absolute and plain. */
function markOf(text: string): Mark | undefined {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof value !== "object" || value === null) return undefined;
  const { owner, run, identity, sockets, postmaster, container } = value as Record<string, unknown>;
  const judged = ownerOf(owner);
  if (judged === undefined || !plainPath(run)) return undefined;
  if (!plainPath(identity) || basename(identity) !== basename(run)) return undefined;
  if ((postmaster === undefined) === (container === undefined)) return undefined;
  if (sockets !== undefined || postmaster !== undefined) {
    if (!plainPath(sockets)) return undefined;
    if (dirname(sockets) !== run && dirname(sockets) !== shortTemporaryDirectory) return undefined;
    if (!socketDirectoryName.test(basename(sockets))) return undefined;
  }
  if (postmaster !== undefined) {
    if (typeof postmaster !== "object" || postmaster === null) return undefined;
    const { bin, data } = postmaster as Record<string, unknown>;
    if (!plainPath(bin) || !plainPath(data) || !dataDirectory.test(relative(run, data))) {
      return undefined;
    }
    return { owner: judged, run, identity, sockets: sockets as string, postmaster: { bin, data } };
  }
  if (typeof container !== "string" || !containerName.test(container)) return undefined;
  return sockets === undefined
    ? { owner: judged, run, identity, container }
    : { owner: judged, run, identity, sockets: sockets as string, container };
}

/** The directories `mark` names as both sweeps act on them: under its run directory's one
 *  spelling (`identity`), and a socket directory in /tmp as the mark names it. */
function markedPaths(mark: Mark): { readonly sockets?: string; readonly data?: string } {
  const sockets =
    mark.sockets === undefined || dirname(mark.sockets) !== mark.run
      ? mark.sockets
      : join(mark.identity, basename(mark.sockets));
  if (mark.postmaster === undefined) return { sockets };
  return { sockets, data: join(mark.identity, relative(mark.run, mark.postmaster.data)) };
}

/** What this process can tell of the owner of a mark: that it lives; that it is gone, with its
 *  pid and the user whose directories its Postgres left; or why it cannot tell. */
type Verdict =
  | { readonly kind: "lives" }
  | { readonly kind: "gone"; readonly pid: number; readonly uid: number }
  | { readonly kind: "unjudged"; readonly why: string };

/** Whether `owner`, the owner of a mark, still runs, as `self`, this process, can see it. */
function judge(owner: Owner | null, self: Owner | null): Verdict {
  if (owner === null) return { kind: "unjudged", why: "its stack ran where there is no /proc" };
  if (self === null) return { kind: "unjudged", why: "this machine has no /proc" };
  if (owner.boot !== self.boot) return { kind: "unjudged", why: `boot ${owner.boot}` };
  if (owner.pidNamespace !== self.pidNamespace) {
    return { kind: "unjudged", why: `PID namespace ${owner.pidNamespace}` };
  }
  // The start the kernel shows is shifted by the reader's own time namespace, so the owner's
  // reading of its start and this process's are of one clock only in one time namespace.
  if (owner.timeNamespace !== self.timeNamespace) {
    return { kind: "unjudged", why: `time namespace ${owner.timeNamespace || "(none)"}` };
  }
  if (owner.uid !== self.uid) return { kind: "unjudged", why: `user ${owner.uid}` };
  const gone: Verdict = { kind: "gone", pid: owner.pid, uid: owner.uid };
  let stat: string;
  try {
    stat = readFileSync(`/proc/${owner.pid}/stat`, "utf8");
  } catch (error) {
    if (codeOf(error) === "ENOENT") return gone;
    return { kind: "unjudged", why: `its process could not be read: ${firstLine(error)}` };
  }
  return running(stat, owner.start) ? { kind: "lives" } : gone;
}

/** Whether `stat`, the text of a `/proc/<pid>/stat`, is of a process that started at `start` and
 *  is not a zombie: a process with the pid that started at another time took the pid over, and a
 *  zombie runs no more. */
function running(stat: string, start: string): boolean {
  const fields = statFields(stat);
  return fields[22 - 3] === start && fields[0] !== "Z" && fields[0] !== "X";
}

/** Whether the process `pid` that started at `start` still runs, read from `/proc`. */
function stillRuns(pid: number, start: string): boolean {
  try {
    return running(readFileSync(`/proc/${pid}/stat`, "utf8"), start);
  } catch (error) {
    if (codeOf(error) === "ENOENT") return false;
    throw error;
  }
}

/** A postmaster as `postmasterOf` verified it: its pid, and its start in clock ticks since boot. */
interface Postmaster {
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
function postmasterOf(data: string, bin: string, uid: number): Postmaster | undefined {
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

/** The data directory of each postmaster that still serves among the marks of the run in `run`:
 *  a stop that failed left it running. Each is read under the mark's one spelling (`markedPaths`),
 *  as a start's sweep reads it. A mark that cannot be read, and a postmaster that cannot be told,
 *  count as serving, since removing the directory under a server is the fault. */
export function servingPostmasters(run: string): string[] {
  let names: string[];
  try {
    names = readdirSync(join(run, marksDirectory));
  } catch (error) {
    if (codeOf(error) === "ENOENT") return [];
    return [`${join(run, marksDirectory)} (${firstLine(error)})`];
  }
  const serving: string[] = [];
  for (const name of names.filter((entry) => entry.endsWith(".json"))) {
    try {
      const mark = markOf(readFileSync(join(run, marksDirectory, name), "utf8"));
      const { data } = mark === undefined ? {} : markedPaths(mark);
      if (
        mark?.postmaster !== undefined &&
        data !== undefined &&
        postmasterOf(data, mark.postmaster.bin, process.getuid?.() ?? -1) !== undefined
      ) {
        serving.push(data);
      }
    } catch (error) {
      serving.push(`${join(run, marksDirectory, name)} (${firstLine(error)})`);
    }
  }
  return serving;
}

/** What `removeDirectory` did: removed the directory; found it gone before it looked; or saw it
 *  and then lost it to another start, which removed it first. */
type Removal = "removed" | "absent" | "lost";

/** Removes the directory `path` where it is a directory of the user `uid`, and nothing where it
 *  is gone. It renames the directory to a name of its own in the same parent first, and removes it
 *  under that name: of two starts that both saw it, one rename wins, and the other finds it gone
 *  ("lost"), so only one says that it removed it. Throws, naming `path`, where it is anything
 *  else, since a file, a link or another user's directory is nothing a stack made, and where the
 *  removal fails, which puts the directory back under `path` for the next start. */
function removeDirectory(path: string, uid: number): Removal {
  let stat: Stats;
  try {
    stat = lstatSync(path);
  } catch (error) {
    if (codeOf(error) === "ENOENT") return "absent";
    throw error;
  }
  if (!stat.isDirectory() || stat.uid !== uid) {
    throw new Error(`${path} is not a directory of user ${uid}, so it is left`);
  }
  const claimed = `${path}.removing-${process.pid}-${randomBytes(4).toString("hex")}`;
  try {
    renameSync(path, claimed);
  } catch (error) {
    if (codeOf(error) === "ENOENT") return "lost";
    throw new Error(`${path} could not be removed: ${firstLine(error)}`);
  }
  try {
    rmSync(claimed, { recursive: true, force: true });
  } catch (error) {
    try {
      renameSync(claimed, path);
    } catch {
      // What stays under the claimed name is a part of a dead stack that no mark names any more.
    }
    throw new Error(`${path} could not be removed: ${firstLine(error)}`);
  }
  return "removed";
}

/** Ends `postmaster`, the process `postmasterOf` verified, by its pid alone: SIGQUIT, and SIGKILL
 *  where it still runs after `postmasterQuitMs`, each only while `/proc` still names that process
 *  by its pid and its start. Throws where it still runs after both. */
async function stopPostmaster({ pid, start }: Postmaster, data: string): Promise<void> {
  for (const [signal, ms] of [
    ["SIGQUIT", postmasterQuitMs],
    ["SIGKILL", postmasterKillMs],
  ] as const) {
    if (!stillRuns(pid, start)) return;
    try {
      process.kill(pid, signal);
    } catch (error) {
      if (codeOf(error) !== "ESRCH") throw error;
    }
    const deadline = Date.now() + ms;
    while (stillRuns(pid, start) && Date.now() < deadline) await sleep(postmasterPollMs);
  }
  if (stillRuns(pid, start)) {
    throw new Error(`the postmaster ${pid} of ${data} still runs after SIGQUIT and SIGKILL`);
  }
}

/** Ends the postmaster of `data`, where one still serves it, and then removes `data`. Answers
 *  whether this call found a postmaster and stopped it, and what came of the directory. Throws,
 *  leaving `data`, where the postmaster fails a check of `postmasterOf` or still runs after its
 *  stop. A postmaster that another start ended first is no failure: what counts is whether it is
 *  gone. */
async function removePostmaster(
  bin: string,
  data: string,
  uid: number
): Promise<{ readonly stopped: boolean; readonly removal: Removal }> {
  const postmaster = postmasterOf(data, bin, uid);
  if (postmaster !== undefined) await stopPostmaster(postmaster, data);
  return { stopped: postmaster !== undefined, removal: removeDirectory(data, uid) };
}

/** `phrases` as one list for a line: "a", "a and b", "a, b and c". */
function listOf(phrases: readonly string[]): string {
  return phrases.length < 2
    ? (phrases[0] ?? "")
    : `${phrases.slice(0, -1).join(", ")} and ${phrases.at(-1)}`;
}

/** What a sweep leaves and says so: one list per kind, each of which is one line at its end. */
interface Left {
  readonly unread: string[];
  readonly untrusted: string[];
  readonly unjudged: string[];
}

/** What the container sweep leaves the run directory sweep to read: the name of each container
 *  Docker still lists with a mark label after the sweep, and of each one the sweep removed. */
interface Containers {
  readonly listed: ReadonlySet<string>;
  readonly removed: ReadonlySet<string>;
}

/** What the daemon said of a `docker` call that failed: the first line of its standard error,
 *  which is Docker's own reason, or the error's own first line where it wrote none. */
function dockerSaid(error: unknown): string {
  const stderr =
    typeof error === "object" && error !== null && "stderr" in error ? error.stderr : undefined;
  const said = typeof stderr === "string" ? (stderr.trim().split("\n")[0] ?? "") : "";
  return said === "" ? firstLine(error) : said;
}

/** Whether Docker still lists the container `id`, removed or not; true where it cannot tell. */
async function stillListed(id: string): Promise<boolean> {
  try {
    const { stdout } = await execFileAsync(
      "docker",
      ["ps", "--all", "--quiet", "--no-trunc", "--filter", `id=${id}`],
      { timeout: dockerMs }
    );
    return stdout.trim() !== "";
  } catch {
    return true;
  }
}

/** How a `docker rm` of a container ended: this start removed it; another start removed it,
 *  with what Docker said of it; or it is not removed, with Docker's reason. */
type ContainerRemoval =
  | { readonly kind: "removed" }
  | { readonly kind: "elsewhere"; readonly reason: string }
  | { readonly kind: "failed"; readonly reason: string };

/** Removes the container `id` with its anonymous volumes. `docker rm --force` prints each
 *  container it removed and nothing for one that is already gone, which another start removed
 *  first. Where another start's removal of it is in progress, Docker refuses this one; the start
 *  then waits, for at most `dockerMs`, until Docker no longer lists the container, so what it says
 *  of it is what is true when it returns. */
async function removeContainer(id: string): Promise<ContainerRemoval> {
  try {
    const { stdout } = await execFileAsync("docker", ["rm", "--force", "--volumes", id], {
      timeout: dockerMs,
    });
    if (stdout.trim() !== "") return { kind: "removed" };
    return {
      kind: "elsewhere",
      reason: "docker rm printed no id, as for a container that is gone",
    };
  } catch (error) {
    const reason = dockerSaid(error);
    if (!/removal of container \S+ is already in progress/.test(reason)) {
      return { kind: "failed", reason };
    }
    const deadline = Date.now() + dockerMs;
    while (Date.now() < deadline) {
      if (!(await stillListed(id))) return { kind: "elsewhere", reason };
      await sleep(dockerPollMs);
    }
    return {
      kind: "failed",
      reason: `${reason}, and Docker still lists it ${dockerMs / 1000} s later`,
    };
  }
}

/** Removes every container whose label marks a Postgres of a stack that is gone, with its socket
 *  directory, and answers what Docker lists then, or undefined where `docker ps` failed. A machine
 *  with no `docker` has no container to look for, and lists none. */
async function removeContainers(self: Owner | null, left: Left): Promise<Containers | undefined> {
  const listed = new Set<string>();
  const removed = new Set<string>();
  let lines: string;
  try {
    ({ stdout: lines } = await execFileAsync(
      "docker",
      [
        "ps",
        "--all",
        "--no-trunc",
        "--filter",
        `label=${markLabel}`,
        "--format",
        `{{.ID}} {{.Names}} {{.Label "${markLabel}"}}`,
      ],
      { timeout: dockerMs }
    ));
  } catch (error) {
    if (codeOf(error) === "ENOENT") return { listed, removed };
    left.unread.push(`the containers, since docker ps failed: ${firstLine(error)}`);
    return undefined;
  }
  for (const line of lines.split("\n")) {
    if (line.trim() === "") continue;
    // An id and a name hold no space; the label, a path of a mark among it, may.
    const [id = "", name = "", ...label] = line.split(" ");
    listed.add(name);
    const what = `container ${id.slice(0, 12)}`;
    const mark = markOf(label.join(" "));
    if (mark?.container === undefined) {
      left.unread.push(`${what} (its label is no mark)`);
      continue;
    }
    // `docker commit` copies a container's labels into its image, and every container made from
    // that image carries them: a label is the mark of the container whose name it names alone.
    if (mark.container !== name) {
      left.unread.push(`${what} (its label is the mark of ${mark.container}, not of ${name})`);
      continue;
    }
    const verdict = judge(mark.owner, self);
    if (verdict.kind === "lives") continue;
    if (verdict.kind === "unjudged") {
      left.unjudged.push(`${what} (${verdict.why})`);
      continue;
    }
    // The label is Docker's, but the socket directory it names is acted on as any path is, under
    // the one spelling the run directory sweep acts on too.
    const { sockets } = markedPaths(mark);
    const why = sockets === undefined ? undefined : whyUntrusted(sockets, verdict.uid, "directory");
    if (why !== undefined) {
      left.untrusted.push(`${what} (${why})`);
      continue;
    }
    const by = `stack ${verdict.pid}`;
    const removal = await removeContainer(id);
    if (removal.kind === "failed") {
      console.error(
        `${named}: ${what} (${mark.container}), which ${by} left, is not removed: ${removal.reason}`
      );
      continue;
    }
    listed.delete(name);
    if (removal.kind === "elsewhere") {
      // Its socket directory is the run directory sweep's, which reads the mark of a container
      // Docker no longer lists.
      console.error(
        `${named}: ${what} (${mark.container}), which ${by} left, another start removed first: ${removal.reason}`
      );
      continue;
    }
    removed.add(name);
    try {
      const andSockets =
        sockets !== undefined && removeDirectory(sockets, verdict.uid) === "removed"
          ? ` and ${sockets}`
          : "";
      console.error(
        `${named}: removed ${what} (${mark.container})${andSockets}, which ${by} left when it ` +
          `ended without stopping ${andSockets === "" ? "it" : "them"}`
      );
    } catch (error) {
      console.error(
        `${named}: removed ${what} (${mark.container}), which ${by} left when it ended without ` +
          `stopping it; its socket directory is not removed: ${firstLine(error)}`
      );
    }
  }
  return { listed, removed };
}

/** Whether nothing is at `path`, read with `lstat`. */
function absent(path: string): boolean {
  try {
    lstatSync(path);
    return false;
  } catch (error) {
    if (codeOf(error) === "ENOENT") return true;
    throw error;
  }
}

/** How a postmaster's working directory, its data directory, names the run directory it serves
 *  from: the run directory, then its stack's place in it. A run directory someone renamed after
 *  its stack started has another name than the pool gives one, and is found all the same. */
const servedFrom = /^(.+)\/(?:worker-\d+|template)\/pgdata$/;

/** A postmaster the kernel shows serving from a run directory that this start's listing of its
 *  TMPDIR does not read. */
interface Served {
  readonly run: string;
  readonly data: string;
  readonly pid: number;
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
function servedElsewhere(real: string, uid: number): Served[] {
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
      if (basename(readlinkSync(`/proc/${pid}/exe`)) !== "postgres") continue;
      const data = readlinkSync(`/proc/${pid}/cwd`);
      const run = servedFrom.exec(data)?.[1];
      if (run === undefined) continue;
      if (dirname(run) === real && basename(run).startsWith(runDirectoryPrefix)) continue;
      const status = readFileSync(`/proc/${pid}/status`, "utf8");
      const own = /^NSpid:.*?(\d+)[ \t]*$/m.exec(status)?.[1] ?? pid;
      if (Number.parseInt(readUnlinked(join(data, "postmaster.pid")), 10) === Number(own)) {
        served.push({ run, data, pid: Number(pid) });
      }
    } catch {
      // A process that ended, or whose links this user may not read, serves from nothing here.
    }
  }
  return served.sort((first, second) => first.data.localeCompare(second.data));
}

/** Removes, for each mark in the run directory `run` whose stack is gone, what the mark names
 *  outside a container, and then the mark: the postmaster and its data directory, and the socket
 *  directory. A container's mark is read only once its container is gone (`containers`): the
 *  container sweep judges, removes or names a container Docker lists, and its socket directory
 *  goes with it. It reads a run directory, a mark and a path a mark names only where
 *  `whyUntrusted` finds no other user could have put it there, and acts on the paths a mark names
 *  only where the mark's one spelling of its run directory is the run directory it checked
 *  (`markedPaths`). Answers the data directory of each postmaster mark it read, or undefined
 *  where it named the run directory itself on a line. */
async function sweepRun(
  run: string,
  uid: number,
  self: Owner | null,
  containers: Containers | undefined,
  left: Left
): Promise<ReadonlySet<string> | undefined> {
  const untrusted = whyUntrusted(run, uid, "directory");
  if (untrusted !== undefined) {
    left.untrusted.push(`${run} (${untrusted})`);
    return undefined;
  }
  const read = new Set<string>();
  const marks = join(run, marksDirectory);
  let names: string[];
  try {
    names = readdirSync(marks);
  } catch (error) {
    // A run directory with no marks started no Postgres, or is no directory at all.
    if (codeOf(error) === "ENOENT" || codeOf(error) === "ENOTDIR") return read;
    left.unread.push(`${run} (${codeOf(error) ?? firstLine(error)})`);
    return undefined;
  }
  for (const name of names.filter((file) => file.endsWith(".json")).sort()) {
    const file = join(marks, name);
    const written = whyUntrusted(file, uid, "file");
    if (written !== undefined) {
      left.untrusted.push(`${file} (${written})`);
      continue;
    }
    let mark: Mark | undefined;
    try {
      mark = markOf(readUnlinked(file));
    } catch (error) {
      // A mark another start removed after this one listed it is no longer there to read.
      if (codeOf(error) === "ENOENT") continue;
      left.unread.push(`${file} (${codeOf(error) ?? firstLine(error)})`);
      continue;
    }
    if (mark === undefined || mark.identity !== run) {
      left.unread.push(`${file} (no mark of this run directory)`);
      continue;
    }
    // The paths the mark names, under the run directory checked above: markOf holds the data
    // directory to its stack's place in the run and the socket directory to the run or /tmp.
    const { sockets, data } = markedPaths(mark);
    if (data !== undefined) read.add(data);
    if (mark.container !== undefined) {
      if (containers === undefined) {
        left.unread.push(`${file} (its container ${mark.container}, since docker ps failed)`);
        continue;
      }
      // A container Docker still lists is the container sweep's, which named it if it left it.
      if (containers.listed.has(mark.container)) continue;
      // One the container sweep removed went with its socket directory, which that sweep named
      // where it could not remove it: the mark goes with the directory, or stays for the next
      // start.
      if (containers.removed.has(mark.container)) {
        if (sockets === undefined || absent(sockets)) rmSync(file, { force: true });
        continue;
      }
    }
    const verdict = judge(mark.owner, self);
    if (verdict.kind === "lives") continue;
    if (verdict.kind === "unjudged") {
      left.unjudged.push(`${file} (${verdict.why})`);
      continue;
    }
    const why = [data, sockets]
      .map((path) =>
        path === undefined ? undefined : whyUntrusted(path, verdict.uid, "directory")
      )
      .find((reason) => reason !== undefined);
    if (why !== undefined) {
      left.untrusted.push(`${file} (${why})`);
      continue;
    }
    const what =
      data !== undefined
        ? `the postmaster of ${data}, that directory and ${sockets}`
        : sockets === undefined
          ? `the mark of container ${mark.container}`
          : `${sockets} of container ${mark.container}`;
    const by = `stack ${verdict.pid}`;
    // Each part this start removed itself, which its line names, and each part another start
    // removed between this start's look and its own removal (`removeDirectory`): a line says
    // "removed" of nothing another start removed.
    const done: string[] = [];
    const lost: string[] = [];
    try {
      if (mark.postmaster !== undefined && data !== undefined) {
        const { stopped, removal } = await removePostmaster(mark.postmaster.bin, data, verdict.uid);
        if (removal === "removed")
          done.push(...(stopped ? [`the postmaster of ${data}`, "that directory"] : [data]));
        else if (removal === "lost")
          lost.push(stopped ? `the postmaster of ${data} and that directory` : data);
        else if (stopped) done.push(`the postmaster of ${data}`);
      }
      if (sockets !== undefined) {
        const removal = removeDirectory(sockets, verdict.uid);
        if (removal === "removed") done.push(sockets);
        else if (removal === "lost") lost.push(sockets);
      }
      rmSync(file, { force: true });
    } catch (error) {
      console.error(
        done.length === 0
          ? `${named}: ${what}, which ${by} left, is not removed: ${firstLine(error)}`
          : `${named}: removed ${listOf(done)}, which ${by} left, and the rest is not removed: ${firstLine(error)}`
      );
      continue;
    }
    const elsewhere = lost.length === 0 ? "" : `; another start removed ${listOf(lost)} first`;
    const container =
      mark.container === undefined
        ? ""
        : `; its container ${mark.container} is gone, as Docker lists no such container`;
    if (done.length > 0) {
      console.error(
        `${named}: removed ${listOf(done)}, which ${by} left when it ended without stopping them${elsewhere}${container}`
      );
    } else if (lost.length > 0) {
      console.error(`${named}: ${listOf(lost)}, which ${by} left, another start removed first`);
    }
  }
  return read;
}

/** Sweeps each run directory of `parent`, this start's TMPDIR, that the pool named, and each other
 *  run directory that a postmaster of this user serves from (`servedElsewhere`), by the same steps
 *  (`sweepRun`), and names each such postmaster that no mark of its run directory names. */
async function removeRunDirectories(
  parent: string,
  self: Owner | null,
  containers: Containers | undefined,
  left: Left
): Promise<void> {
  const uid = process.getuid?.() ?? -1;
  let real: string;
  let entries: string[];
  try {
    // TMPDIR may be spelt through a link of its user's: the run directories are listed under its
    // one spelling, which a mark holds its own run directory by, and `whyUntrusted` then reads
    // each component of it.
    real = identityOf(parent);
    entries = readdirSync(real);
  } catch (error) {
    left.unread.push(`${parent} (${firstLine(error)})`);
    return;
  }
  for (const entry of entries.filter((name) => name.startsWith(runDirectoryPrefix)).sort()) {
    await sweepRun(join(real, entry), uid, self, containers, left);
  }
  // The kernel shows a working directory with no link in it, the one spelling marks hold.
  const served = servedElsewhere(real, uid);
  for (const run of new Set(served.map((postmaster) => postmaster.run))) {
    const read = await sweepRun(run, uid, self, containers, left);
    if (read === undefined) continue;
    for (const postmaster of served.filter((each) => each.run === run && !read.has(each.data))) {
      left.unjudged.push(
        `${postmaster.data} (postmaster ${postmaster.pid} serves it from a run directory this start does not list, and no mark there names it)`
      );
    }
  }
}

/** Removes each Postgres that a stack which is gone left on this machine: every container that
 *  Docker lists with a mark, every mark in a run directory of `tmpdir()`, and every mark in a run
 *  directory elsewhere that a postmaster of this user serves from. Leaves every other one, and
 *  names what it left on one line per kind. Never throws: a removal that fails is one line, and
 *  the start goes on. */
export async function removeLeftoverPostgres(): Promise<void> {
  const self = stackOwner();
  const left: Left = { unread: [], untrusted: [], unjudged: [] };
  try {
    // The containers first, so a container goes before the socket directory it serves in.
    const containers = await removeContainers(self, left);
    await removeRunDirectories(tmpdir(), self, containers, left);
  } catch (error) {
    console.error(
      `${named}: the Postgres of stacks that are gone is not looked for: ${firstLine(error)}`
    );
  }
  if (left.unread.length > 0) {
    console.error(`${named}: left what it cannot read: ${left.unread.join(", ")}`);
  }
  if (left.untrusted.length > 0) {
    console.error(
      `${named}: left what another user could have written, or a link leads out of: ${left.untrusted.join(", ")}`
    );
  }
  if (left.unjudged.length > 0) {
    console.error(
      `${named}: left the Postgres of stacks whose process it cannot see: ${left.unjudged.join(", ")}`
    );
  }
}
