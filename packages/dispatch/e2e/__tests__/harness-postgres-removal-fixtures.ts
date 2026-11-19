/** The fixtures of the rows of `harness-postgres-removal.test.ts` and
 *  `harness-postgres-holders.test.ts`: the marks, data directories and stand-in postmasters a row
 *  plants, the start's sweep, a start's claim and a start's removal of one container, each run in
 *  a child of the row, the stand-in `docker` of that removal, with its own clock, the end of such a
 *  child, and the stack's harness and the stand-in `pg_ctl` a row kills hard. It holds the program
 *  of each row's own limit too (`Waits`, `rowMs`, `Row`): a row states what it waits for, its
 *  limit is the sum of those waits, and each fixture that starts a child or a holder read charges
 *  the row, which fails where it started more than its limit counts. The rows and what they
 *  require stay in those files. */

import { expect } from "bun:test";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Subprocess } from "bun";

import { type Machine, machine, type RemovalBounds } from "../harness-postgres-container-removal";
import * as marks from "../harness-postgres-marks";
import { type StatReader, statMs, statReader, statStartMs } from "../harness-postgres-processes";
import { stopMs } from "../harness-postgres-removal";
import { type StandIn, standIn } from "./planted-marks";

const { marksDirectory, runDirectoryPrefix, stackOwner } = marks;

/** The owners a mark of a row carries, each built from this process: this process, which runs;
 *  its pid with another start, which is a gone stack; another boot; another PID namespace;
 *  another time namespace; and none, which is a stack's where there is no /proc. */
export function owners() {
  const runs = stackOwner();
  if (runs === null) throw new Error("this machine has no /proc, so no start judges an owner");
  return {
    runs,
    gone: { ...runs, start: "0" },
    otherBoot: { ...runs, boot: "00000000-0000-0000-0000-000000000000" },
    otherNamespace: { ...runs, pidNamespace: "pid:[1]" },
    otherTime: { ...runs, timeNamespace: "time:[1]" },
    noProc: null,
  };
}

/** A row's scratch: its own `TMPDIR`, in `parent`, and a run directory in it named as the pool
 *  names one. */
export function scratch(parent = tmpdir()) {
  const tmp = mkdtempSync(join(parent, "marks-trust-"));
  return { tmp, run: mkdtempSync(join(tmp, runDirectoryPrefix)) };
}

/** Writes the mark of stack `index` in `run`, whose owner is `owner`, naming `data` and `bin`. */
export function writeMark(
  run: string,
  owner: unknown,
  data: string,
  bin: string,
  index = 0
): string {
  const sockets = mkdtempSync(join(run, `pg-${index}-`));
  mkdirSync(join(run, marksDirectory), { recursive: true });
  const file = join(run, marksDirectory, `worker-${index}.json`);
  writeFileSync(
    file,
    JSON.stringify({
      owner,
      run,
      identity: marks.identityOf(run),
      sockets,
      postmaster: { bin, data },
    })
  );
  return file;
}

/** A data directory at `path` holding a file, and a `postmaster.pid` naming `pid` when given. */
export function leaveData(path: string, pid?: number): void {
  mkdirSync(path, { recursive: true });
  writeFileSync(join(path, "PG_VERSION"), "18\n");
  if (pid !== undefined) writeFileSync(join(path, "postmaster.pid"), `${pid}\n${path}\n`);
}

/** Who finds a stand-in postmaster: "its TMPDIR", the row's own starts alone, through the run
 *  directory of their TMPDIR, since its program is `stand-in`, run through a link named
 *  `postgres` (the program a mark's `bin` names); or "every start", whose walk of the kernel's
 *  processes (`servedElsewhere`) reads a program named `postgres`, for a row of a run directory
 *  that no start lists. */
export type Seen = "its TMPDIR" | "every start";

/** Puts a copy of `program` in `bin` as the stand-in postmaster that `seen` names, and answers the
 *  path to run it by, `bin/postgres`. */
function postmasterProgram(bin: string, program: string, seen: Seen): string {
  mkdirSync(bin, { recursive: true });
  const postgres = join(bin, "postgres");
  const copy = seen === "every start" ? postgres : join(bin, "stand-in");
  copyFileSync(program, copy);
  chmodSync(copy, 0o755);
  if (copy !== postgres) symlinkSync(copy, postgres);
  return postgres;
}

/** A stand-in postmaster of the cluster in `data`: a copy of `sleep` in `bin`
 *  (`postmasterProgram`), run with `data` for its working directory, which `data`'s
 *  `postmaster.pid` names. */
export function standInPostmaster(bin: string, data: string, seen: Seen): StandIn {
  const sleep = Bun.which("sleep");
  if (sleep === null) throw new Error("no sleep on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, sleep, seen);
  mkdirSync(data, { recursive: true });
  const postmaster = standIn(data, [postgres, "600"]);
  leaveData(data, postmaster.pid);
  return postmaster;
}

/** A stand-in postmaster of the cluster in `data` that writes one line to `signals` for each
 *  SIGQUIT it gets, and ends once it got one and `go` exists, or after 600 s: a copy of `bash` in
 *  `bin` that only the row's own starts find (`postmasterProgram`), run with `data` for its
 *  working directory, which `data`'s `postmaster.pid` names. */
export function signalCountingPostmaster(
  bin: string,
  data: string,
  signals: string,
  go: string
): StandIn {
  const bash = Bun.which("bash");
  if (bash === null) throw new Error("no bash on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, bash, "its TMPDIR");
  mkdirSync(data, { recursive: true });
  const script = `quit=; trap 'echo QUIT >>"$1"; quit=1' QUIT
end=$((SECONDS + 600))
while [ "$SECONDS" -lt "$end" ]; do
  if [ -n "$quit" ] && [ -e "$2" ]; then exit 0; fi
  sleep 0.05 & wait $!
done`;
  const postmaster = standIn(data, [postgres, "-c", script, "postgres", signals, go]);
  leaveData(data, postmaster.pid);
  return postmaster;
}

/** A FIFO at `path`: a read of it waits, for `read -t` of a stand-in's shell, with no process
 *  forked to wait. */
function fifoAt(path: string): string {
  const made = Bun.spawnSync(["mkfifo", path], { stdout: "ignore", stderr: "pipe" });
  if (made.exitCode !== 0) throw new Error(`mkfifo ${path} failed: ${made.stderr.toString()}`);
  return path;
}

/** A stand-in postmaster of the cluster in `data` whose child outlives it, as the processes of a
 *  postmaster do for a moment after it ends: a copy of `bash` in `bin` that only the row's own
 *  starts find (`postmasterProgram`), run with `data` for its working directory, which `data`'s
 *  `postmaster.pid` names, and a child of it with `data` for its working directory too. On SIGQUIT
 *  the postmaster ends at once; its child runs 0.5 s more, which is longer than a start's poll of
 *  `/proc` and shorter than its wait for the processes of a postmaster it stopped, then writes
 *  `present` to `saw` where `data` still holds its `PG_VERSION`, or `gone`, and ends. Both wait
 *  with `read -t` on a FIFO beside `saw`, so neither forks a process in `data`. */
export function slowReleasingPostmaster(bin: string, data: string, saw: string): StandIn {
  const bash = Bun.which("bash");
  if (bash === null) throw new Error("no bash on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, bash, "its TMPDIR");
  mkdirSync(data, { recursive: true });
  const child = `until [ -e "$1" ]; do read -r -t 0.05 -u 3 _; done
read -r -t 0.5 -u 3 _
if [ -e PG_VERSION ]; then echo present; else echo gone; fi >>"$2"`;
  const script = `exec 3<>"$3"
trap 'echo >"$1"; exit 0' QUIT
"$BASH" -c "$4" child "$1" "$2" &
end=$((SECONDS + 600))
while [ "$SECONDS" -lt "$end" ]; do read -r -t 0.05 -u 3 _; done`;
  const fifo = fifoAt(`${saw}.fifo`);
  const postmaster = standIn(data, [
    postgres,
    "-c",
    script,
    "postgres",
    `${saw}.quit`,
    saw,
    fifo,
    child,
  ]);
  leaveData(data, postmaster.pid);
  return postmaster;
}

const removalModule = fileURLToPath(new URL("../harness-postgres-removal.ts", import.meta.url));
const containerRemovalModule = fileURLToPath(
  new URL("../harness-postgres-container-removal.ts", import.meta.url)
);

/** The start's sweep, as a child of this file runs it: within the start's own bounds of a
 *  container's removal, or the row's (`RemovalBounds`), where `ANSWER_MS` and `BOUND_MS` give them,
 *  and with the start's own reader of where each path a process names leads, or the row's
 *  (`StatReader`), where `STAT_READER` gives it. */
const sweepScript = `
import { removeLeftoverPostgres } from ${JSON.stringify(removalModule)};
const { ANSWER_MS, BOUND_MS, STAT_READER } = process.env;
await removeLeftoverPostgres(
  ANSWER_MS === undefined ? undefined : { answerMs: Number(ANSWER_MS), boundMs: Number(BOUND_MS) },
  STAT_READER === undefined ? undefined : JSON.parse(STAT_READER)
);
`;

/** The environment that gives a start's removal of a container in a child of this file the
 *  row's `bounds`. */
function boundsEnv({ answerMs, boundMs }: RemovalBounds): Record<string, string> {
  return { ANSWER_MS: String(answerMs), BOUND_MS: String(boundMs) };
}

/** A stand-in for the reader a start's holder read runs (`StatReader`): its program reads the
 *  paths from its standard input, separated by NUL, says it is ready, and writes where each path
 *  leads, in turn, as the start's own reader does; but at a path of `never`, or a path under one,
 *  it writes nothing more and waits, as a stat of a path on a mount whose server hangs waits, until
 *  it is killed or the start that ran it ends (its parent pid changes). It sets itself no bound of
 *  its own: one shorter than the row's limit would end the wait in the start's place, and a start
 *  with no bound would pass the row. Its own clock times the start's wait there, which no load on
 *  the start or the row stretches: it writes to `clock`, under its pid, when the kernel forked it,
 *  and then the time it reached that path and the time again every 500 ms until it is killed
 *  (`clockOf`), each in seconds since the machine booted, the one clock the kernel gives both a
 *  process's start and `/proc/uptime` on. Its mount table is this process's, with each mount of
 *  `mounts` added after it, a
 *  mount point and its type, as the row plants a path of `never` on a network mount, and with no
 *  type where `type` is empty, as a line whose type the start cannot read: the file `table`, which
 *  the stand-in writes. */
export function neverAnswering(
  never: readonly string[],
  clock: string,
  table: string,
  mounts: readonly { readonly point: string; readonly type: string }[] = []
): StatReader {
  const own = readFileSync("/proc/self/mountinfo", "utf8").trimEnd();
  writeFileSync(
    table,
    [
      own,
      // The fields a line of /proc/self/mountinfo holds, with the planted point and type.
      ...mounts.map(
        ({ point, type }, index) =>
          `${9000 + index} 1 0:${900 + index} / ${point} rw,relatime shared:${900 + index}${type === "" ? "" : ` - ${type} planted:${point} rw`}`
      ),
      "",
    ].join("\n")
  );
  return {
    mounts: table,
    run: [
      process.execPath,
      "--eval",
      `
const { appendFileSync, readFileSync, statSync, writeSync } = require("node:fs");
const never = ${JSON.stringify(never)};
const write = (what, at) => appendFileSync(${JSON.stringify(clock)}, process.pid + " " + what + " " + at + "\\n");
// When the kernel forked this process, which no load after the fork moves: its start in clock
// ticks since boot, 100 a second. Each tick is on that clock too (/proc/uptime): the epoch's clock
// less the boot time /proc/stat gives, a whole second rounded down and moved by each correction
// of the clock since, would put a fork seconds before a tick that came first.
const uptime = () => Number(readFileSync("/proc/uptime", "utf8").split(" ")[0]);
const ticks = Number(readFileSync("/proc/self/stat", "utf8").slice(readFileSync("/proc/self/stat", "utf8").lastIndexOf(")") + 2).split(" ")[19]);
write("forked", ticks / 100);
const start = process.ppid;
setInterval(() => {
  if (process.ppid !== start) process.exit(1);
}, 1_000);
const paths = readFileSync(0, "utf8").split("\\0");
writeSync(1, "ready\\n");
for (const path of paths) {
  if (never.some((hung) => path === hung || path.startsWith(hung + "/"))) {
    const tick = () => write("tick", uptime());
    tick();
    setInterval(tick, 500);
    break;
  }
  let line = "-";
  try {
    const stats = statSync(path, { bigint: true });
    line = stats.dev + ":" + stats.ino;
  } catch {}
  writeSync(1, line + "\\n");
}
`,
    ],
  };
}

/** What a stand-in reader's clock holds (`neverAnswering`), for each reader that waited on a path
 *  of `never`: its pid, when the kernel forked it, and the times it wrote while it waited, in
 *  seconds since the machine booted. */
export function clockOf(clock: string): { pid: number; forked: number; ticks: number[] }[] {
  const lines = existsSync(clock) ? readFileSync(clock, "utf8").split("\n").filter(Boolean) : [];
  const readers = new Map<number, { pid: number; forked: number; ticks: number[] }>();
  for (const line of lines) {
    const [pid = "", what = "", at = ""] = line.split(" ");
    const reader = readers.get(Number(pid)) ?? { pid: Number(pid), forked: Number.NaN, ticks: [] };
    if (what === "forked") reader.forked = Number(at);
    else reader.ticks.push(Number(at));
    readers.set(reader.pid, reader);
  }
  return [...readers.values()].filter((reader) => reader.ticks.length > 0);
}

/** How long a row waits for one event of another process: a `bun` child's start of its sweep
 *  under load, which took more than 180 s once on a shared Linux machine under a load of 1,217, on
 *  2026-11-17, is the longest of them. */
export const claimEventMs = 240_000;
/** How long a row gives one call of its own fixtures that makes a container or an image with
 *  Docker (`docker` of `harness-postgres-leftover-fixtures.ts`): three times the 133 s that one
 *  `docker create` of a row took on a daemon a shared Linux machine shared, on 2026-11-19, while
 *  that daemon answered every other call, as the fixtures' other bounds are three times their
 *  largest reading. */
export const makeMs = 400_000;

/** The holder reads a start makes after it stopped a postmaster (`removePostmaster` of
 *  `e2e/harness-postgres-removal.ts`): one, and a second where a process still held the directory
 *  then, which a moment of the kernel's decides, so a row counts both. */
const readsAfterStop = 2;

/** What a row waits for, one wait after another, which its own limit is the sum of (`rowMs`):
 *  each `bun` child it starts (`children`), each of which may take `claimEventMs` to start under
 *  load; each holder read that the sweeps of those children make before a claim (`reads`), each of
 *  which starts a reader that may take `statStartMs` to start and then reads within `statMs`
 *  (`e2e/harness-postgres-processes.ts`); each stop of a postmaster those sweeps make (`stops`),
 *  each of which may take `stopMs` and is followed by `readsAfterStop` holder reads; each call of
 *  its fixtures that makes a container or an image (`makes`), each of which may take `makeMs`;
 *  every other bound the row waits out (`bounds`, in ms): the bound of a removal, of an `until`, of
 *  a stack's start; and the bound of each wait of its `finally` before it removes what it made,
 *  such as the stop of a stack it started (`ends`, in ms). Those come after the row's deadline
 *  (`Row.deadline`), which ends every fixture call of the row's own waits: a call that runs to the
 *  deadline then leaves the `finally` the time its stops take, and the removals after them the
 *  room above it (`Row.cleanup`), where a deadline that counted the stops left a removal none, and
 *  the container of a stack the row killed stayed. */
export interface Waits {
  readonly children: number;
  readonly reads: number;
  readonly stops?: number;
  readonly makes?: number;
  readonly bounds: readonly number[];
  readonly ends?: readonly number[];
}

/** The holder reads a row's sweeps may make in all (`Waits`): those before a claim, and those after
 *  each stop. */
function readsOf({ reads, stops = 0 }: Waits): number {
  return reads + stops * readsAfterStop;
}

/** Everything a row waits for, one wait after another (`Waits`), in ms: the limit of a child the
 *  row ends itself, and of an `until` of an event that comes after those waits. */
export function waitsMs(waits: Waits): number {
  const { children, stops = 0, makes = 0, bounds } = waits;
  return (
    children * claimEventMs +
    readsOf(waits) * (statStartMs + statMs) +
    stops * stopMs +
    makes * makeMs +
    bounds.reduce((sum, ms) => sum + ms, 0)
  );
}

/** The room a row's limit leaves above what it waits for (`waitsMs`): the row's own reads of files
 *  and of `/proc` between its waits, its asks of Docker and Postgres, each of which a daemon that
 *  took minutes over a removal answered within a second on 2026-11-18, and the moment between a
 *  bound firing in a child and the row reading what it ended, so that the error of the child or of
 *  the `until`, which names what did not come, ends the row, and not the row's own clock; and the
 *  removal of what a fixture call made that ran out the row's deadline (`Row.deadline`). */
export const rowRoomMs = 60_000;

/** A row's own limit: everything it waits for (`waitsMs`), the waits of its `finally`
 *  (`Waits.ends`), and `rowRoomMs` above them. Every row of the files these fixtures serve that
 *  starts a child states its limit so, and no other way. */
export function rowMs(waits: Waits): number {
  return waitsMs(waits) + (waits.ends ?? []).reduce((sum, ms) => sum + ms, 0) + rowRoomMs;
}

/** What a row started so far, held to what its limit counts (`Waits`): each fixture that starts a
 *  `bun` child charges the row for it (`child`), each sweep charges the holder reads it made, as
 *  the reader it ran counted them (`reads`), and each fixture call that makes a container or an
 *  image charges it for that call (`made`). A row that starts more than its limit counts fails
 *  there, naming what the limit left out, whatever the load: its limit is below what it waits for.
 *  `ms` is `waitsMs` of the row's waits, the limit of a child it ends itself, and `deadline` is
 *  when those waits end, from the row's start, on the machine's clock (`machine` of
 *  `e2e/harness-postgres-container-removal.ts`): a fixture call of Docker or Postgres waits until
 *  then and no longer, so it ends within the row's own limit with a reason that names it.
 *  `cleanup` is when a fixture call that removes what the row made waits until: after the
 *  deadline, the waits of the row's `finally` (`Waits.ends`) and half the room above them
 *  (`rowRoomMs`), so a removal's line, and not the row's own clock, ends the row. */
export interface Row {
  readonly ms: number;
  readonly deadline: number;
  readonly cleanup: number;
  child(what: string): void;
  reads(made: number, what: string): void;
  made(what: string): void;
}

/** The `Row` of a row whose limit is `rowMs(waits)`, which starts now. */
export function rowOf(waits: Waits): Row {
  let children = 0;
  let reads = 0;
  let makes = 0;
  const ms = waitsMs(waits);
  const deadline = machine.now() + ms;
  return {
    ms,
    deadline,
    cleanup: deadline + (waits.ends ?? []).reduce((sum, ms) => sum + ms, 0) + rowRoomMs / 2,
    child: (what) => {
      children += 1;
      if (children > waits.children) {
        throw new Error(
          `the row's limit counts ${waits.children} bun children, and it started ${children}, the last ${what}: its limit is below what it waits for`
        );
      }
    },
    reads: (made, what) => {
      reads += made;
      if (reads > readsOf(waits)) {
        throw new Error(
          `the row's limit counts ${readsOf(waits)} holder reads, and its sweeps made ${reads}, ${made} of them in ${what}: its limit is below what it waits for`
        );
      }
    },
    made: (what) => {
      makes += 1;
      if (makes > (waits.makes ?? 0)) {
        throw new Error(
          `the row's limit counts ${waits.makes ?? 0} calls that make a container or an image, and it made ${makes}, the last ${what}: its limit is below what it waits for`
        );
      }
    },
  };
}

/** The reader a row's sweep runs, the row's own or the start's (`statReader`), with its program
 *  behind a `bash` that writes to `starts`, each time the sweep starts it, the read and the top path
 *  it serves (`HOLDER_READ` of `e2e/harness-postgres-processes.ts`), and then runs it, so that the
 *  row counts the holder reads its sweep made (`Row.reads`); its mount table is the reader's own. */
function countedReader(starts: string, reader: StatReader = statReader): StatReader {
  const bash = Bun.which("bash");
  if (bash === null) throw new Error("no bash on PATH to count the holder reads of a sweep");
  return {
    ...reader,
    run: [bash, "-c", 'printf "%s\\n" "$HOLDER_READ" >>"$0"; exec "$@"', starts, ...reader.run],
  };
}

/** How many readers the sweep that ran `countedReader(starts)` started one after another: for
 *  each of its reads, the most readers one of its top paths started, since the top paths of a
 *  read start theirs at the same time, and a top path starts another only after one it waited on
 *  ran out its bound. A row's `reads` counts these, each of which may take `statStartMs` to start
 *  and `statMs` to read. */
function readsMade(starts: string): number {
  const lines = existsSync(starts) ? readFileSync(starts, "utf8").split("\n").filter(Boolean) : [];
  const perTop = new Map<string, number>();
  for (const line of lines) perTop.set(line, (perTop.get(line) ?? 0) + 1);
  const perRead = new Map<string, number>();
  for (const [line, count] of perTop) {
    const read = line.split(" ")[0] ?? "";
    perRead.set(read, Math.max(perRead.get(read) ?? 0, count));
  }
  return [...perRead.values()].reduce((sum, count) => sum + count, 0);
}

/** A start's sweep running in a child with `tmp` for its `TMPDIR` and `empty`, a directory with no
 *  program in it, for its `PATH`, so no row changes the environment of this process, and with the
 *  row's `reader` where it gives one, counted (`countedReader`): what it printed so far, the lines
 *  it printed once it ended, and the child, for a row to end. It charges `row` for the child and,
 *  once the child ended, for the holder reads its sweep made (`Row`). */
export function startSweep(
  row: Row,
  tmp: string,
  {
    empty = mkdtempSync(join(tmp, "no-programs-")),
    reader,
  }: { empty?: string; reader?: StatReader } = {}
): { stderr(): string; readonly lines: Promise<string[]>; readonly child: Subprocess } {
  const what = `the sweep of ${tmp}`;
  row.child(what);
  const counts = mkdtempSync(join(tmpdir(), "sweep-reads-"));
  const starts = join(counts, "starts");
  const child = Bun.spawn([process.execPath, "--eval", sweepScript], {
    env: { TMPDIR: tmp, PATH: empty, STAT_READER: JSON.stringify(countedReader(starts, reader)) },
    stdout: "ignore",
    stderr: "pipe",
  });
  let stderr = "";
  const read = (async () => {
    const stream = child.stderr.getReader();
    const decoder = new TextDecoder();
    for (let chunk = await stream.read(); !chunk.done; chunk = await stream.read()) {
      stderr += decoder.decode(chunk.value, { stream: true });
    }
  })();
  const lines = (async () => {
    const [code] = await Promise.all([child.exited, read]);
    const made = readsMade(starts);
    rmSync(counts, { recursive: true, force: true });
    row.reads(made, what);
    if (code !== 0) throw new Error(`the sweep exited ${code}: ${stderr}`);
    return stderr.split("\n");
  })();
  return { stderr: () => stderr, lines, child };
}

/** Runs the start's sweep in a child (`startSweep`), and answers the lines it printed. */
export function sweep(row: Row, tmp: string, empty?: string): Promise<string[]> {
  return startSweep(row, tmp, { empty }).lines;
}

/** Waits until `met` holds, and throws naming `what` where it has not after `ms`. What it waits for
 *  is a write of another process (a stand-in's signal line, a child's line), which no event of
 *  this process reports, so it reads again every 50 ms. */
export async function until(met: () => boolean, what: string, ms: number): Promise<void> {
  const deadline = Date.now() + ms;
  while (!met()) {
    if (Date.now() >= deadline) throw new Error(`${what} did not come within ${ms / 1000} s`);
    await Bun.sleep(50);
  }
}

/** A run directory of another `TMPDIR` than the sweep's, with what a stack left there: a stand-in
 *  postmaster of stack 0 serving from its data directory, which every start finds, or only that
 *  data directory where `serving` is false, and the mark of `owner` beside it, or no mark where
 *  `owner` is undefined. */
export function leaveElsewhere(owner?: unknown, serving = true) {
  const other = mkdtempSync(join(tmpdir(), "marks-elsewhere-"));
  const run = mkdtempSync(join(other, runDirectoryPrefix));
  const bin = join(other, "bin");
  const data = join(realpathSync(run), "worker-0", "pgdata");
  if (!serving) leaveData(data);
  const postmaster = serving ? standInPostmaster(bin, data, "every start") : undefined;
  const file = owner === undefined ? undefined : writeMark(run, owner, data, bin);
  return { other, run, bin, data, postmaster, file };
}

/** The mark of a container of the run directory `run`, whose owner is `owner`, beside its socket
 *  directory: what the image provider writes for stack 0. Answers both paths and the name. */
export function leaveContainerMark(run: string, owner: unknown) {
  const sockets = mkdtempSync(join(run, "pg-0-"));
  const container = "dispatch-e2e-pg-no-docker-0";
  mkdirSync(join(run, marksDirectory), { recursive: true });
  const file = join(run, marksDirectory, "worker-0.json");
  writeFileSync(
    file,
    JSON.stringify({ owner, run, identity: marks.identityOf(run), sockets, container })
  );
  return { sockets, container, file };
}

/** A start's claim of `CLAIM_PATH` (`claimForRemoval`) in a child of this file: it prints the
 *  claim, and then waits to be killed, for at most 600 s. No process holds what a row claims so,
 *  so it reads none. */
const claimScript = `
import { claimForRemoval } from ${JSON.stringify(removalModule)};
setTimeout(() => process.exit(1), 600_000);
console.log(JSON.stringify(await claimForRemoval(process.env.CLAIM_PATH, process.getuid(), async () => ({ holders: [], unread: [] }))));
`;

/** SIGKILLs `child`, a child of this file, where it still runs, and waits for its end. */
export async function endChild(child: Subprocess): Promise<void> {
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  await child.exited;
}

/** What `child` printed up to its first line feed, or all it printed where it ended first. */
export async function firstLineOf(child: Subprocess<"ignore", "pipe", "ignore">): Promise<string> {
  let printed = "";
  const reader = child.stdout.getReader();
  const decoder = new TextDecoder();
  while (!printed.includes("\n")) {
    const chunk = await reader.read();
    if (chunk.done) break;
    printed += decoder.decode(chunk.value, { stream: true });
  }
  reader.releaseLock();
  return printed;
}

/** Claims `path` in a child with the real `claimForRemoval`, and answers once the child printed
 *  its claim: the claimed name, which holds its claimer's pid and start, as the judge reads an
 *  owner, and the claimer, which holds the claim until it is killed (`endChild`). It charges `row`
 *  for the child (`Row`). */
export async function claimInChild(
  row: Row,
  path: string
): Promise<{ claimed: string; claimer: Subprocess }> {
  row.child(`the claim of ${path}`);
  const claimer = Bun.spawn([process.execPath, "--eval", claimScript], {
    env: { ...process.env, CLAIM_PATH: path },
    stdout: "pipe",
    stderr: "ignore",
  });
  try {
    const claimed = `${path}.removing-${claimer.pid}-${marks.startOf(readFileSync(`/proc/${claimer.pid}/stat`, "utf8"))}`;
    const printed = await firstLineOf(claimer);
    expect(JSON.parse(printed)).toEqual({ kind: "claimed", path: claimed });
    return { claimed, claimer };
  } catch (error) {
    await endChild(claimer);
    throw error;
  }
}

/** What a start killed between its claim of `path` and its removal leaves: `claimInChild`, and
 *  the claimer SIGKILLed. Answers the claimed name. */
export async function claimAndDie(row: Row, path: string): Promise<string> {
  const { claimed, claimer } = await claimInChild(row, path);
  await endChild(claimer);
  return claimed;
}

const marksModule = fileURLToPath(new URL("../harness-postgres-marks.ts", import.meta.url));

/** A stack's harness, as `e2e/harness-postgres.ts` starts stack 0 of the binaries provider: it
 *  marks its Postgres, makes its data directory and runs `HARNESS_BIN`'s `pg_ctl ... start` on it,
 *  with the arguments the provider gives it, and prints that pg_ctl's pid. Then it waits to be
 *  killed, which is the row's own SIGKILL, for at most 600 s. `HARNESS_DATA`, where it is set, is
 *  the data directory as the stack spells it to `pg_ctl -D`, and `HARNESS_CWD` the directory
 *  that `pg_ctl` runs in: a spelling of its own, which the mark does not hold. */
export const harnessScript = `
import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { markPostgres } from ${JSON.stringify(marksModule)};
setTimeout(() => process.exit(1), 600_000);
const { HARNESS_RUN: run = "", HARNESS_BIN: bin = "", HARNESS_SOCKETS: sockets = "" } = process.env;
const data = join(run, "worker-0", "pgdata");
markPostgres(run, "worker-0", { sockets, postmaster: { bin, data } });
mkdirSync(data, { recursive: true });
writeFileSync(join(data, "PG_VERSION"), "18\\n");
const options = \`-c listen_addresses='' -k \${sockets}\`;
const log = join(run, "worker-0", "postgres.log");
const spelt = process.env.HARNESS_DATA || data;
const ctl = spawn(join(bin, "pg_ctl"), ["-D", spelt, "-l", log, "-o", options, "-w", "start"], {
  cwd: process.env.HARNESS_CWD || undefined,
  stdio: "ignore",
});
console.log(ctl.pid);
`;

/** A stand-in of `pg_ctl -D <data> ... start` in `bin`, beside a stand-in postmaster that only
 *  the row's own starts find (`postmasterProgram`): it waits for `go`, for at most 600 s, and then
 *  starts that postmaster with `<data>` for its working directory, names it in `<data>`'s
 *  `postmaster.pid` and ends, as `pg_ctl -w` ends once its server is up; where `<data>` is gone,
 *  it fails, as `pg_ctl` does. It uses shell builtins and that postmaster alone: it waits with
 *  `read -t` on a FIFO in `bin` (`fifoAt`), so it forks no process while it waits. A wait of
 *  `sleep 0.05` forked a shell that named `<data>` in its arguments until it became `sleep`, and a
 *  start's holder read that met that moment named it as a second holder beside `pg_ctl`: 1 of 20
 *  runs of a row on a shared Linux machine on 2026-11-19, and 2,830 of 301,387 reads of the
 *  stand-in's children in 10 s. */
export function startingPgCtl(bin: string, go: string): void {
  const sleep = Bun.which("sleep");
  if (sleep === null) throw new Error("no sleep on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, sleep, "its TMPDIR");
  const pause = fifoAt(join(bin, "pg_ctl.pause"));
  writeFileSync(
    join(bin, "pg_ctl"),
    `#!/usr/bin/env bash\nend=$((SECONDS + 600))\nexec 3<>${JSON.stringify(pause)}\n` +
      `until [ -e ${JSON.stringify(go)} ] || [ "$SECONDS" -ge "$end" ]; do read -r -t 0.05 -u 3 _; done\n` +
      // The postmaster gets no descriptor of the FIFO.
      "exec 3<&-\n" +
      // A command that a script runs in the background ignores SIGQUIT unless job control is
      // on, and a start's stop of a postmaster sends SIGQUIT first.
      `cd "$2" || exit 1\nset -m\n${JSON.stringify(postgres)} 600 </dev/null >/dev/null 2>&1 &\n` +
      `echo "$!" >postmaster.pid\n`,
    { mode: 0o755 }
  );
}

/** The lines of `lines` that hold `text`. */
export function holding(lines: readonly string[], text: string): string[] {
  return lines.filter((line) => line.includes(text));
}

/** A start's removal of the container `CONTAINER_ID` (`removeContainer`) in a child of this file,
 *  within the row's bounds (`boundsEnv`): it prints how the removal ended, and its own lines on
 *  stderr. */
const removeScript = `
import { removeContainer } from ${JSON.stringify(containerRemovalModule)};
const { CONTAINER_ID: id = "", ANSWER_MS, BOUND_MS } = process.env;
console.log(JSON.stringify(await removeContainer(id, undefined, { answerMs: Number(ANSWER_MS), boundMs: Number(BOUND_MS) })));
`;

/** One entry of a stand-in's own clock: when a call reached it, or when it ended, in seconds since
 *  the epoch (`EPOCHREALTIME`), with the call's arguments. */
export interface Tick {
  readonly at: number;
  readonly event: "start" | "end";
  readonly call: string;
}

/** A stand-in `docker` (`standInDocker`): its directory, the calls it got, its clock, each process
 *  it wrote, by its pid and start, and each process that runs now under its path. */
export interface StandInDocker {
  readonly bin: string;
  calls(): string[];
  clock(): Tick[];
  processes(): { pid: number; start: string }[];
  running(): { pid: number; start: string }[];
}

/** A directory in `tmp` with a stand-in `docker`, which runs no real one, so a row of it acts on
 *  no container of the machine: it writes the arguments of each call it gets as a line of
 *  `calls`, the stat of its own process as a file of `processes`, and, on its own clock, when the
 *  call reached it and when it ended (`Tick`), and then runs the lines of bash `body`. That clock
 *  is the stand-in's, which no load on the start or on the row stretches: a row times the start's
 *  waits on it. A body that execs another program keeps the stand-in's path as its name (`exec -a
 *  "$0"`), so `running` finds it, and writes no end; one that waits in the background (`sleep N &
 *  wait $!`) ends on SIGTERM, which a start's stop of a call sends, writes its end then, and ends
 *  what it waited for. Answers the directory, the calls so far, the clock so far, each process
 *  the stand-in wrote, by its pid and start, and each process that runs now under its path. */
export function standInDocker(tmp: string, body: readonly string[]): StandInDocker {
  const bin = mkdtempSync(join(tmp, "docker-"));
  const calls = join(bin, "calls");
  const clock = join(bin, "clock");
  const processes = join(bin, "processes");
  mkdirSync(processes);
  writeFileSync(
    join(bin, "docker"),
    [
      "#!/usr/bin/env bash",
      `echo "$*" >>${JSON.stringify(calls)}`,
      `cat /proc/$$/stat >${JSON.stringify(processes)}/$$`,
      `printf '%s start %s\\n' "$EPOCHREALTIME" "$*" >>${JSON.stringify(clock)}`,
      `trap 'kill $(jobs -p) 2>/dev/null; printf "%s end %s\\n" "$EPOCHREALTIME" "$*" >>${JSON.stringify(clock)}' EXIT`,
      "trap 'exit 143' TERM",
      ...body,
      "",
    ].join("\n"),
    { mode: 0o755 }
  );
  const ofStandIn = `${join(bin, "docker")}\0`;
  return {
    bin,
    calls: (): string[] => readFileSync(calls, "utf8").trim().split("\n"),
    clock: (): Tick[] =>
      readFileSync(clock, "utf8")
        .split("\n")
        .filter(Boolean)
        .map((line) => {
          const [at = "", event = "", ...call] = line.split(" ");
          return { at: Number(at), event: event === "end" ? "end" : "start", call: call.join(" ") };
        }),
    processes: (): { pid: number; start: string }[] =>
      readdirSync(processes).map((pid) => ({
        pid: Number(pid),
        start: marks.startOf(readFileSync(join(processes, pid), "utf8")),
      })),
    // A stand-in that has not reached its first line yet, or wrote no stat before a signal ended
    // it, is found here all the same: its command line names the stand-in's path.
    running: (): { pid: number; start: string }[] =>
      readdirSync("/proc").flatMap((entry) => {
        if (!/^\d+$/.test(entry)) return [];
        try {
          if (!readFileSync(`/proc/${entry}/cmdline`, "utf8").includes(ofStandIn)) return [];
          return [
            {
              pid: Number(entry),
              start: marks.startOf(readFileSync(`/proc/${entry}/stat`, "utf8")),
            },
          ];
        } catch {
          return [];
        }
      }),
  };
}

/** Runs `script` in a child of this file with `env` over this process's own, and `bin`, a
 *  directory of stand-ins, first on its `PATH`. Answers what it printed on stdout and stderr and
 *  how many seconds it took; kills the child and throws where it has not ended within `ms`, and
 *  where it exits other than 0. */
async function inChild(script: string, bin: string, env: Record<string, string>, ms: number) {
  const started = Date.now();
  const child = Bun.spawn([process.execPath, "--eval", script], {
    env: { ...process.env, ...env, PATH: `${bin}:${process.env.PATH ?? ""}` },
    stdout: "pipe",
    stderr: "pipe",
  });
  const printed = Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
  ]);
  const code = await Promise.race([child.exited, Bun.sleep(ms).then(() => undefined)]);
  if (code === undefined) {
    await endChild(child);
    throw new Error(`the child did not end within ${ms / 1000} s`);
  }
  const [stdout, stderr] = await printed;
  if (code !== 0) throw new Error(`the child exited ${code}: ${stderr}`);
  return { stdout, stderr, seconds: (Date.now() - started) / 1000 };
}

/** Removes the container `id` in a child (`removeScript`) whose `docker` is the one in `bin`,
 *  within `bounds`, and charges `row` for the child. Answers how the removal ended, the lines the
 *  start printed, and how many seconds the child took; kills the child and throws where it has
 *  not ended within everything the row waits for (`Row.ms`). */
export async function removeInChild(row: Row, bin: string, id: string, bounds: RemovalBounds) {
  row.child(`the removal of container ${id.slice(0, 12)}`);
  const { stdout, stderr, seconds } = await inChild(
    removeScript,
    bin,
    { CONTAINER_ID: id, ...boundsEnv(bounds) },
    row.ms
  );
  return { removal: JSON.parse(stdout) as unknown, lines: stderr.split("\n"), seconds };
}

const leftoverFixtures = fileURLToPath(
  new URL("./harness-postgres-leftover-fixtures.ts", import.meta.url)
);

/** One call of the leftover rows' fixture `docker` (`harness-postgres-leftover-fixtures.ts`) in a
 *  child of this file, with the arguments `ARGS` gives, for a row of the waits `WAITS` that starts
 *  with the child (`rowOf`): it prints what the call answered, or the message of what it threw.
 *  The call is the subject of the row that runs the child, so the child's row does not charge it
 *  as a call that makes a container (`Row.made`): it runs to the deadline `WAITS` sets, and not
 *  for `makeMs`. Where `RUN` is set it then runs a row's `finally`, as the leftover rows do: a stop
 *  that takes `STOP_MS`, which the row's `Waits.ends` counts, and then the removal of each container
 *  whose mark names `RUN` (`removeContainersOf`), and prints whether that removal ended or what it
 *  threw. */
const fixtureCallScript = `
import { docker, removeContainersOf } from ${JSON.stringify(leftoverFixtures)};
import { rowOf } from ${JSON.stringify(fileURLToPath(import.meta.url))};
const { ARGS = "[]", RUN, STOP_MS, WAITS = "" } = process.env;
const row = { ...rowOf(JSON.parse(WAITS)), made() {} };
const ended = {};
try {
  ended.stdout = await docker(row, ...JSON.parse(ARGS));
} catch (error) {
  ended.error = error instanceof Error ? error.message : String(error);
}
if (RUN !== undefined) {
  await Bun.sleep(Number(STOP_MS));
  try {
    await removeContainersOf(row, RUN);
    ended.removed = true;
  } catch (error) {
    ended.removal = error instanceof Error ? error.message : String(error);
  }
}
console.log(JSON.stringify(ended));
`;

/** Runs the leftover rows' fixture `docker` with `args` in a child whose `docker` is the one in
 *  `bin` (`fixtureCallScript`), for a row of the waits `waits` that starts with the child, and
 *  then, where `end` is given, that row's `finally`: a stop of `end.stopMs` and the removal of the
 *  containers whose mark names `end.run`. Charges `row` for the child. Answers what the call
 *  answered or threw, what the removal did, and the lines the child printed; kills the child and
 *  throws where it has not ended within everything the row waits for (`Row.ms`). */
export async function fixtureDockerInChild(
  row: Row,
  bin: string,
  args: readonly string[],
  waits: Waits,
  end?: { readonly stopMs: number; readonly run: string }
) {
  row.child(`the fixture's docker ${args.join(" ")}`);
  const { stdout, stderr } = await inChild(
    fixtureCallScript,
    bin,
    {
      WAITS: JSON.stringify(waits),
      ARGS: JSON.stringify(args),
      ...(end === undefined ? {} : { RUN: end.run, STOP_MS: String(end.stopMs) }),
    },
    row.ms
  );
  return {
    ended: JSON.parse(stdout) as {
      stdout?: string;
      error?: string;
      removed?: true;
      removal?: string;
    },
    lines: stderr.split("\n"),
  };
}

/** Runs the start's whole sweep (`sweepScript`) in a child with `tmp` for its `TMPDIR` and `bin`,
 *  a directory holding a stand-in `docker`, first on its `PATH`, within `bounds`, with the start's
 *  own reader, counted (`countedReader`), and charges `row` for the child and for the holder reads
 *  its sweep made. Answers the lines it printed and how many seconds it took; kills the child and
 *  throws where it has not ended within everything the row waits for (`Row.ms`). */
export async function sweepInChild(row: Row, tmp: string, bin: string, bounds: RemovalBounds) {
  const what = `the sweep of ${tmp}`;
  row.child(what);
  const counts = mkdtempSync(join(tmpdir(), "sweep-reads-"));
  const starts = join(counts, "starts");
  try {
    const { stderr, seconds } = await inChild(
      sweepScript,
      bin,
      { TMPDIR: tmp, ...boundsEnv(bounds), STAT_READER: JSON.stringify(countedReader(starts)) },
      row.ms
    );
    row.reads(readsMade(starts), what);
    return { lines: stderr.split("\n"), seconds };
  } finally {
    rmSync(counts, { recursive: true, force: true });
  }
}

/** One call of a row's `docker` (`rowMachine`): its arguments, when it reached the row's clock,
 *  and when and how it ended, in ms of that clock: answered, ended by its `signal`, or ended by
 *  its `timeout`, as `execFile` ends a call. */
export interface RowCall {
  readonly args: string;
  readonly at: number;
  ended?: number;
  how?: "answered" | "aborted" | "timed out";
}

/** How a row's `docker` answers one call (`rowMachine`): `after` ms of the row's clock after the
 *  call reached it, with `stdout` where it exits 0, or with `stderr` where it exits 1; or never, as
 *  Docker never ends a removal that is stuck in the daemon. */
export type RowAnswer =
  | { readonly after: number; readonly stdout?: string; readonly stderr?: string }
  | "never";

/** A machine for a start's removal of a container whose clock the row moves (`Machine`). The clock
 *  starts at 0 and moves only while the removal waits on it, to the next time at which a sleep
 *  ends, an answer comes or a call's `timeout` ends that call, so a row runs the start's own
 *  10-minute bound in a moment of real time, and every time it reads is the removal's own, which
 *  no load stretches. Its `docker` answers each call as `answer` says, and ends it where its
 *  `signal` aborts or its `timeout` passes first, as `execFile` ends one. Answers the machine,
 *  each call so far (`RowCall`), and `drive`, which moves the clock until `done`, the removal,
 *  settles, and throws where the removal waits on nothing that clock moves. */
export function rowMachine(answer: (args: readonly string[]) => RowAnswer) {
  let now = 0;
  // Each time the clock may move to, in the order it was set: the end of a sleep, an answer, or a
  // call's timeout. Two at one time come in that order.
  const due: { readonly at: number; readonly fire: () => void }[] = [];
  const after = (ms: number, fire: () => void): (() => void) => {
    const entry = { at: now + ms, fire };
    due.push(entry);
    return () => {
      const index = due.indexOf(entry);
      if (index !== -1) due.splice(index, 1);
    };
  };
  const calls: RowCall[] = [];
  const machine: Machine = {
    now: () => now,
    sleep: (ms, signal) => {
      const { promise, resolve } = Promise.withResolvers<void>();
      if (signal?.aborted) {
        resolve();
        return promise;
      }
      const cancel = after(ms, resolve);
      signal?.addEventListener(
        "abort",
        () => {
          cancel();
          resolve();
        },
        { once: true }
      );
      return promise;
    },
    docker: (args, { signal, timeout }) => {
      const { promise, resolve, reject } = Promise.withResolvers<{ stdout: string }>();
      const call: RowCall = { args: args.join(" "), at: now };
      calls.push(call);
      const cancels: (() => void)[] = [];
      const end = (how: NonNullable<RowCall["how"]>, settle: () => void): void => {
        if (call.how !== undefined) return;
        call.ended = now;
        call.how = how;
        for (const cancel of cancels) cancel();
        settle();
      };
      // The errors `execFile` throws: an abort's, and a failed command's, with its fields.
      const failed = (stderr: string, fields: object) =>
        Object.assign(new Error(`Command failed: docker ${call.args}\n${stderr}`), {
          stdout: "",
          stderr,
          ...fields,
        });
      const abort = (): void =>
        end("aborted", () =>
          reject(
            Object.assign(new Error("The operation was aborted"), {
              name: "AbortError",
              code: "ABORT_ERR",
            })
          )
        );
      if (signal.aborted) {
        abort();
        return promise;
      }
      signal.addEventListener("abort", abort, { once: true });
      cancels.push(() => signal.removeEventListener("abort", abort));
      if (timeout !== undefined && timeout > 0) {
        cancels.push(
          after(timeout, () =>
            end("timed out", () =>
              reject(failed("", { killed: true, signal: "SIGTERM", code: null }))
            )
          )
        );
      }
      const answered = answer(args);
      if (answered !== "never") {
        cancels.push(
          after(answered.after, () =>
            end("answered", () =>
              answered.stderr === undefined
                ? resolve({ stdout: answered.stdout ?? "" })
                : reject(failed(answered.stderr, { killed: false, code: 1 }))
            )
          )
        );
      }
      return promise;
    },
  };
  async function drive<T>(done: Promise<T>): Promise<T> {
    let settled = false;
    void done.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      }
    );
    for (;;) {
      // What the last move made due runs first: each step of a removal after it is a chain of
      // promise callbacks, which all run before the event loop's next turn.
      const turn = Promise.withResolvers<void>();
      setImmediate(turn.resolve);
      await turn.promise;
      if (settled) return done;
      const next = due.reduce<(typeof due)[number] | undefined>(
        (first, entry) => (first === undefined || entry.at < first.at ? entry : first),
        undefined
      );
      if (next === undefined) {
        throw new Error(
          `the removal waits, at ${now} ms of the row's clock, on nothing that clock moves`
        );
      }
      due.splice(due.indexOf(next), 1);
      now = next.at;
      next.fire();
    }
  }
  return { machine, calls: (): readonly RowCall[] => calls, drive };
}
