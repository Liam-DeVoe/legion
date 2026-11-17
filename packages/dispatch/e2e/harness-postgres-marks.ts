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
 *  id (`/proc/sys/kernel/random/boot_id`), its PID namespace (the link `/proc/self/ns/pid`) and
 *  its user id. The rest names what the Postgres leaves: the run directory, the socket directory,
 *  and the postmaster's data directory and the binaries that stop it, or the container's name.
 *  Each Postgres has the record as a file in `postgres-marks/` of the run directory, written
 *  before its socket directory holds anything and before its data directory or its container
 *  exists, and written through a rename, so it is whole or absent. A container also carries the
 *  record as its label `markLabel`, which the `docker run` that makes it sets, so it is marked
 *  from the moment it exists, and a start finds it whatever directory its stack ran in.
 *
 *  WHEN AN OWNER IS GONE. Only for a mark of this process's own boot, PID namespace and user,
 *  which is where `/proc/<pid>` names the owner's process and where a process of the owner's
 *  user is never hidden from this one: no process has that pid, or the process that has it
 *  started at another time, or it is a zombie, which runs no more. A mark of another boot (another
 *  machine on a shared Docker daemon, or this one before it restarted), of another PID namespace
 *  (a container of its own) or of another user is left, since its owner cannot be seen from here.
 *  The boot id is what tells two machines apart: the first PID namespace has the same inode on
 *  every Linux host. A stack that runs where there is no `/proc` writes a mark with no owner,
 *  and no start judges it; a start where there is no `/proc` judges no mark. Both are left.
 *
 *  WHAT A START REMOVES. Exactly what a mark of a gone owner names: the container by the id under
 *  which Docker lists its label, the postmaster by `pg_ctl` on the data directory the mark names,
 *  and then the data and socket directories the mark names. A name only finds a candidate (a run
 *  directory's prefix, a container's label key); the mark decides. A mark whose paths are not the
 *  shapes the providers make is not read, and a directory that is not this user's own is not
 *  removed. The postmaster is stopped only where the pid of its `postmaster.pid` belongs to a
 *  process whose working directory is that data directory, so a pid that a stale file names and
 *  another process now has gets no signal; where no such process runs, only the directories go.
 *  A data directory goes only once its postmaster is gone. The rest of a dead run's directory, its
 *  logs, stays, as a run that keeps its logs leaves them (`e2e/harness-pool.ts`).
 *
 *  WHAT A START LEAVES, AND SAYS SO. A mark whose owner lives is left without a word. Every other
 *  mark it leaves is named on one line per kind: what it cannot read (another user's run
 *  directory of mode 0700, a file that is not a mark, a `docker ps` that failed), and what it
 *  cannot judge (another boot, PID namespace or user, or no `/proc`). A removal that fails is one
 *  line, keeps its mark for the next start, and never fails the start: two starts that find one
 *  dead mark at once both try, and the second one's `docker rm` of a container already gone is
 *  that line.
 *
 *  THE WINDOWS NO MARK COVERS. A stack killed between the `mkdtemp` of a socket directory and the
 *  rename of its mark, calls with no await between them, leaves that empty directory and at most
 *  a `.partial` file of the mark, which no start reads. A stack killed while its `pg_ctl start` or
 *  `docker run` is still running leaves that child running: a start in the moment before the
 *  postmaster writes `postmaster.pid`, or before Docker makes the container, finds neither and
 *  removes the directories, and a postmaster that comes up after that may run on with its data
 *  directory gone and no mark that names it. A container that comes up after that carries its
 *  label for the next start. A pid that a dead postmaster leaves can be taken by another process
 *  between the check of its working directory and the signal `pg_ctl` sends, which only pid reuse
 *  within those milliseconds allows. */
import { execFile } from "node:child_process";
import {
  lstatSync,
  mkdirSync,
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

/** How long a `docker ps` or a `docker rm` may take. A daemon that hangs would otherwise hold
 *  every start; one that answers lists or removes a container in well under a second. */
const dockerMs = 60_000;
/** How long the `pg_ctl -w stop` of a dead stack's postmaster may take: `pg_ctl`'s own wait is
 *  `PGCTLTIMEOUT`, 60 s by default, and this ends a `pg_ctl` that never returns. */
const pgCtlMs = 90_000;

/** The process that started a Postgres, as `/proc` names it in one boot and PID namespace. */
export interface Owner {
  readonly pid: number;
  /** Field 22 of `/proc/<pid>/stat`, the process's start in clock ticks since boot. */
  readonly start: string;
  /** `/proc/sys/kernel/random/boot_id`. */
  readonly boot: string;
  /** The target of `/proc/self/ns/pid`, such as `pid:[4026531836]`. */
  readonly pidNamespace: string;
  readonly uid: number;
}

/** One Postgres of a stack: the process that started it, null where that process's machine has
 *  no `/proc`, and what the Postgres leaves when that process ends without stopping it. */
export interface Mark {
  readonly owner: Owner | null;
  /** The run's own temporary directory, the one the pool hands `stackPostgres`. */
  readonly run: string;
  /** The directory of its unix socket, in `run` or in `shortTemporaryDirectory`. */
  readonly sockets: string;
  /** The binaries provider's cluster: the data directory and the directory of the `pg_ctl` that
   *  stops it. */
  readonly postmaster?: { readonly bin: string; readonly data: string };
  /** The image provider's container, by the name its `docker run` gave it. */
  readonly container?: string;
}

/** The fields of `stat`, the text of a `/proc/<pid>/stat`, from the third on, the state first.
 *  They are split after the last `)`, the one that ends the command name, since that name may
 *  hold spaces and parentheses of its own, so field N of proc(5) is at index N - 3. */
function statFields(stat: string): string[] {
  return stat.slice(stat.lastIndexOf(")") + 2).split(" ");
}

/** Field 22 of a `/proc/<pid>/stat`, the process's start in clock ticks since boot. */
function startOf(stat: string): string {
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
  if (thisOwner === undefined) thisOwner = readOwner();
  return thisOwner;
}

function readOwner(): Owner | null {
  const uid = process.getuid?.();
  if (uid === undefined) return null;
  try {
    const stat = readFileSync("/proc/self/stat", "utf8");
    if (Number.parseInt(stat, 10) !== process.pid) return null;
    return {
      pid: process.pid,
      start: startOf(stat),
      boot: readFileSync("/proc/sys/kernel/random/boot_id", "utf8").trim(),
      pidNamespace: readlinkSync("/proc/self/ns/pid"),
      uid,
    };
  } catch {
    return null;
  }
}

/** Writes the mark of one Postgres of the run in `run`, under `name`, and returns it. The caller
 *  writes it before the Postgres can outlive this process: before `pg_ctl start`, and before the
 *  `docker run` that carries it as `labelOf`. */
export function markPostgres(
  run: string,
  name: string,
  names: Pick<Mark, "sockets" | "postmaster" | "container">
): Mark {
  const mark: Mark = { owner: stackOwner(), run, ...names };
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

/** The name `socketDirectory` gives a socket directory: `pg-`, the stack's index or `template`,
 *  `-`, and the six characters `mkdtemp` adds. */
const socketDirectoryName = /^pg-(?:\d+|template)-[A-Za-z0-9]{6}$/;
/** Where a binaries provider's data directory is in its run directory. */
const dataDirectory = /^(?:worker-\d+|template)\/pgdata$/;
/** A container name as Docker takes one. */
const containerName = /^[A-Za-z0-9][A-Za-z0-9_.-]*$/;

/** Whether `path` is absolute, written with no `.`, `..`, doubled or trailing `/`, and not `/`. */
function plainPath(path: unknown): path is string {
  return typeof path === "string" && isAbsolute(path) && normalize(path) === path && path !== "/";
}

/** `value` as an owner, null where it is null, or undefined where it is neither. */
function ownerOf(value: unknown): Owner | null | undefined {
  if (value === null) return null;
  if (typeof value !== "object" || value === undefined) return undefined;
  const { pid, start, boot, pidNamespace, uid } = value as Record<string, unknown>;
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
    typeof uid !== "number" ||
    !Number.isSafeInteger(uid) ||
    uid < 0
  ) {
    return undefined;
  }
  return { pid, start, boot, pidNamespace, uid };
}

/** `text` as a mark, or undefined where it is not one the providers write: JSON with an owner or
 *  null, a run directory, a socket directory in that run directory or in /tmp under the name
 *  `socketDirectory` gives one, and exactly one of a postmaster whose data directory is a stack's
 *  or the template's in that run directory, and a container name. Every path is absolute and
 *  plain. */
export function markOf(text: string): Mark | undefined {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return undefined;
  }
  if (typeof value !== "object" || value === null) return undefined;
  const { owner, run, sockets, postmaster, container } = value as Record<string, unknown>;
  const judged = ownerOf(owner);
  if (judged === undefined || !plainPath(run) || !plainPath(sockets)) return undefined;
  if (dirname(sockets) !== run && dirname(sockets) !== shortTemporaryDirectory) return undefined;
  if (!socketDirectoryName.test(basename(sockets))) return undefined;
  if ((postmaster === undefined) === (container === undefined)) return undefined;
  if (postmaster !== undefined) {
    if (typeof postmaster !== "object" || postmaster === null) return undefined;
    const { bin, data } = postmaster as Record<string, unknown>;
    if (!plainPath(bin) || !plainPath(data) || !dataDirectory.test(relative(run, data))) {
      return undefined;
    }
    return { owner: judged, run, sockets, postmaster: { bin, data } };
  }
  if (typeof container !== "string" || !containerName.test(container)) return undefined;
  return { owner: judged, run, sockets, container };
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

/** The postmaster that serves the cluster in `data`, or undefined where none does: the pid its
 *  `postmaster.pid` names, where the process with that pid has `data` for its working directory,
 *  which a postmaster's is from its start (Postgres's `ChangeToDataDir`), and its start, read
 *  before and after that check, is the same both times. A `postmaster.pid` that a postmaster
 *  killed hard left, whose pid another process has since, names no postmaster here. Throws where
 *  it cannot tell. */
export function postmasterOf(
  data: string
): { readonly pid: number; readonly start: string } | undefined {
  let pidFile: string;
  try {
    pidFile = readFileSync(join(data, "postmaster.pid"), "utf8");
  } catch (error) {
    if (codeOf(error) === "ENOENT" || codeOf(error) === "ENOTDIR") return undefined;
    throw error;
  }
  const pid = Number.parseInt(pidFile, 10);
  if (!Number.isSafeInteger(pid) || pid <= 0) return undefined;
  try {
    const before = startOf(readFileSync(`/proc/${pid}/stat`, "utf8"));
    const cwd = readlinkSync(`/proc/${pid}/cwd`);
    const after = readFileSync(`/proc/${pid}/stat`, "utf8");
    if (cwd !== realpathSync(data) || !running(after, before)) return undefined;
    return { pid, start: before };
  } catch (error) {
    if (codeOf(error) === "ENOENT") return undefined;
    throw error;
  }
}

/** The data directory of each postmaster that still serves among the marks of the run in `run`:
 *  a stop that failed left it running. A mark that cannot be read, and a postmaster that cannot
 *  be told, count as serving, since removing the directory under a server is the fault. */
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
      if (mark?.postmaster !== undefined && postmasterOf(mark.postmaster.data) !== undefined) {
        serving.push(mark.postmaster.data);
      }
    } catch (error) {
      serving.push(`${join(run, marksDirectory, name)} (${firstLine(error)})`);
    }
  }
  return serving;
}

/** Removes the directory `path` where it is a directory of the user `uid`, and nothing where it
 *  is gone. Throws, naming `path`, where it is anything else, since a file, a link or another
 *  user's directory is nothing a stack made, and where the removal fails. */
function removeDirectory(path: string, uid: number): void {
  let stat: Stats;
  try {
    stat = lstatSync(path);
  } catch (error) {
    if (codeOf(error) === "ENOENT") return;
    throw error;
  }
  if (!stat.isDirectory() || stat.uid !== uid) {
    throw new Error(`${path} is not a directory of user ${uid}, so it is left`);
  }
  try {
    rmSync(path, { recursive: true, force: true });
  } catch (error) {
    throw new Error(`${path} could not be removed: ${firstLine(error)}`);
  }
}

/** Stops the postmaster of `data`, where one still serves it, with the `pg_ctl` the dead stack
 *  stopped it with, and then removes `data`. Throws, leaving `data`, where the postmaster still
 *  runs after the stop. A `pg_ctl` that fails because another start stopped it first is no
 *  failure: what counts is whether the postmaster is gone. */
async function removePostmaster(bin: string, data: string, uid: number): Promise<void> {
  const postmaster = postmasterOf(data);
  if (postmaster !== undefined) {
    const stop = await execFileAsync(
      join(bin, "pg_ctl"),
      ["-D", data, "-m", "immediate", "-w", "stop"],
      {
        timeout: pgCtlMs,
      }
    ).then(
      () => undefined,
      (error: unknown) => error
    );
    if (stillRuns(postmaster.pid, postmaster.start)) {
      throw new Error(
        `the postmaster ${postmaster.pid} of ${data} still runs after pg_ctl stop` +
          (stop === undefined ? "" : `: ${firstLine(stop)}`)
      );
    }
  }
  removeDirectory(data, uid);
}

/** What a sweep leaves and says so: one list per kind, each of which is one line at its end. */
interface Left {
  readonly unread: string[];
  readonly unjudged: string[];
}

/** Removes every container whose label marks a Postgres of a stack that is gone, with its socket
 *  directory. A machine with no `docker` has no container to look for. */
async function removeContainers(self: Owner | null, left: Left): Promise<void> {
  let listed: string;
  try {
    ({ stdout: listed } = await execFileAsync(
      "docker",
      [
        "ps",
        "--all",
        "--no-trunc",
        "--filter",
        `label=${markLabel}`,
        "--format",
        `{{.ID}} {{.Label "${markLabel}"}}`,
      ],
      { timeout: dockerMs }
    ));
  } catch (error) {
    if (codeOf(error) === "ENOENT") return;
    left.unread.push(`the containers, since docker ps failed: ${firstLine(error)}`);
    return;
  }
  for (const line of listed.split("\n")) {
    if (line.trim() === "") continue;
    const space = line.indexOf(" ");
    const id = space === -1 ? line : line.slice(0, space);
    const what = `container ${id.slice(0, 12)}`;
    const mark = space === -1 ? undefined : markOf(line.slice(space + 1));
    if (mark?.container === undefined) {
      left.unread.push(`${what} (its label is no mark)`);
      continue;
    }
    const verdict = judge(mark.owner, self);
    if (verdict.kind === "lives") continue;
    if (verdict.kind === "unjudged") {
      left.unjudged.push(`${what} (${verdict.why})`);
      continue;
    }
    const by = `stack ${verdict.pid}`;
    try {
      await execFileAsync("docker", ["rm", "--force", "--volumes", id], { timeout: dockerMs });
      removeDirectory(mark.sockets, verdict.uid);
      console.error(
        `${named}: removed ${what} (${mark.container}) and ${mark.sockets}, which ${by} left ` +
          "when it ended without stopping them"
      );
    } catch (error) {
      console.error(
        `${named}: ${what} (${mark.container}), which ${by} left, is not removed: ${firstLine(error)}`
      );
    }
  }
}

/** Whether `first` and `second` name one directory, however each is spelt. */
function sameDirectory(first: string, second: string): boolean {
  if (first === second) return true;
  try {
    return realpathSync(first) === realpathSync(second);
  } catch {
    return false;
  }
}

/** Removes, for each mark in a run directory of `parent` whose stack is gone, what the mark names
 *  outside a container, and then the mark: the postmaster and its data directory, and the socket
 *  directory. */
async function removeRunDirectories(parent: string, self: Owner | null, left: Left): Promise<void> {
  let entries: string[];
  try {
    entries = readdirSync(parent);
  } catch (error) {
    left.unread.push(`${parent} (${firstLine(error)})`);
    return;
  }
  for (const entry of entries.filter((name) => name.startsWith(runDirectoryPrefix)).sort()) {
    const run = join(parent, entry);
    const marks = join(run, marksDirectory);
    let names: string[];
    try {
      names = readdirSync(marks);
    } catch (error) {
      // A run directory with no marks started no Postgres, or is no directory at all.
      if (codeOf(error) === "ENOENT" || codeOf(error) === "ENOTDIR") continue;
      left.unread.push(`${run} (${codeOf(error) ?? firstLine(error)})`);
      continue;
    }
    for (const name of names.filter((file) => file.endsWith(".json")).sort()) {
      const file = join(marks, name);
      let mark: Mark | undefined;
      try {
        mark = markOf(readFileSync(file, "utf8"));
      } catch (error) {
        // A mark another start removed after this one listed it is no longer there to read.
        if (codeOf(error) === "ENOENT") continue;
        left.unread.push(`${file} (${codeOf(error) ?? firstLine(error)})`);
        continue;
      }
      if (mark === undefined || !sameDirectory(mark.run, run)) {
        left.unread.push(`${file} (no mark of this run directory)`);
        continue;
      }
      const verdict = judge(mark.owner, self);
      if (verdict.kind === "lives") continue;
      if (verdict.kind === "unjudged") {
        left.unjudged.push(`${file} (${verdict.why})`);
        continue;
      }
      const what =
        mark.postmaster === undefined
          ? `${mark.sockets} of container ${mark.container}`
          : `the postmaster of ${mark.postmaster.data}, that directory and ${mark.sockets}`;
      const by = `stack ${verdict.pid}`;
      try {
        if (mark.postmaster !== undefined) {
          await removePostmaster(mark.postmaster.bin, mark.postmaster.data, verdict.uid);
        }
        removeDirectory(mark.sockets, verdict.uid);
        rmSync(file, { force: true });
        console.error(
          `${named}: removed ${what}, which ${by} left when it ended without stopping them`
        );
      } catch (error) {
        console.error(`${named}: ${what}, which ${by} left, is not removed: ${firstLine(error)}`);
      }
    }
  }
}

/** Removes each Postgres that a stack which is gone left on this machine: every container that
 *  Docker lists with a mark, and every mark in a run directory of `tmpdir()`. Leaves every other
 *  one, and names what it left on one line per kind. Never throws: a removal that fails is one
 *  line, and the start goes on. */
export async function removeLeftoverPostgres(): Promise<void> {
  const self = stackOwner();
  const left: Left = { unread: [], unjudged: [] };
  try {
    // The containers first, so a container goes before the socket directory it serves in.
    await removeContainers(self, left);
    await removeRunDirectories(tmpdir(), self, left);
  } catch (error) {
    console.error(
      `${named}: the Postgres of stacks that are gone is not looked for: ${firstLine(error)}`
    );
  }
  if (left.unread.length > 0) {
    console.error(`${named}: left what it cannot read: ${left.unread.join(", ")}`);
  }
  if (left.unjudged.length > 0) {
    console.error(
      `${named}: left the Postgres of stacks whose process it cannot see: ${left.unjudged.join(", ")}`
    );
  }
}
