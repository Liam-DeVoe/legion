/** The fixtures of the rows of `harness-postgres-removal.test.ts` and
 *  `harness-postgres-holders.test.ts`: the marks, data directories and stand-in postmasters a row
 *  plants, the start's sweep, a start's claim and a start's removal of one container, each run in
 *  a child of the row, the stand-in `docker` of that removal, with its own clock, the end of such a
 *  child, and the stack's harness and the stand-in `pg_ctl` a row kills hard. It holds the program
 *  of each row's own limit too (`Waits`, `Part`, `rowMs`, `Row`): a row states what it waits for,
 *  its limit is the sum of those waits, a wait that waits for several things itself is a part
 *  whose bound is the sum of its own, and each fixture that starts a child or a holder read, or
 *  waits for a part, charges the row, which fails where it started more than its limit counts.
 *  The rows and what they require stay in those files. */

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
import {
  claimInfix,
  type StatReader,
  statMs,
  statReader,
  statStartMs,
} from "../harness-postgres-processes";
import { stopMs } from "../harness-postgres-removal";
import {
  parentLives,
  type StandIn,
  standIn,
  untilParentEnds,
  untilThisEnds,
} from "./planted-marks";
import { stepEnds, stepEndsVariable } from "./step-end";

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

/** The directory every row of the importing test file plants in, and the one its sweeps look in
 *  for run directories elsewhere (`servedUnder` of `removeLeftoverPostgres`): one of this file's
 *  own, made on its first use, and again after the file's `afterAll` removed it (`--rerun-each`
 *  runs the file's hooks each round), read as the kernel spells it. A sweep reads every postmaster
 *  of this user that serves from a run directory, so where it read the machine's, the rows of two
 *  files the lane runs at once (`bun test --parallel`, a fresh module for each file) removed each
 *  other's dead stacks, said so on lines their rows did not expect, and made holder reads their
 *  limits did not count: 13 failures in CI's repeat lane on 2026-11-20. */
let plants: string | undefined;
export function plantsRoot(): string {
  if (plants === undefined || !existsSync(plants)) {
    plants = realpathSync(mkdtempSync(join(tmpdir(), "marks-rows-")));
  }
  return plants;
}

/** A row's scratch: its own `TMPDIR`, in `parent`, and a run directory in it named as the pool
 *  names one. */
export function scratch(parent = plantsRoot()) {
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

/** A stand-in postmaster of the cluster in `data`: a copy of `tail` in `bin`
 *  (`postmasterProgram`) that lives until this process ends (`untilThisEnds`), run with `data` for
 *  its working directory, which `data`'s `postmaster.pid` names. */
export function standInPostmaster(bin: string, data: string, seen: Seen): StandIn {
  const [tail = "", ...until] = untilThisEnds();
  const postgres = postmasterProgram(bin, tail, seen);
  mkdirSync(data, { recursive: true });
  const postmaster = standIn(data, [postgres, ...until]);
  leaveData(data, postmaster.pid);
  return postmaster;
}

/** A stand-in postmaster of the cluster in `data` that writes one line to `signals` for each
 *  SIGQUIT it gets, and ends once it got one and `go` exists, or once this process, its parent,
 *  ended (`parentLives`): a copy of `bash` in `bin` that only the row's own starts find
 *  (`postmasterProgram`), run with `data` for its working directory, which `data`'s
 *  `postmaster.pid` names. */
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
while ${parentLives}; do
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
 *  with `read -t` on a FIFO beside `saw`, so neither forks a process in `data`. The postmaster
 *  lives until this process, its parent, ends, and its child, where no SIGQUIT came, until the
 *  postmaster ends (`parentLives`). */
export function slowReleasingPostmaster(bin: string, data: string, saw: string): StandIn {
  const bash = Bun.which("bash");
  if (bash === null) throw new Error("no bash on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, bash, "its TMPDIR");
  mkdirSync(data, { recursive: true });
  // The child reads "$1" again once its parent ended, since the postmaster's SIGQUIT writes it
  // just before the postmaster ends.
  const child = `until [ -e "$1" ]; do
  ${parentLives} || [ -e "$1" ] || exit 0
  read -r -t 0.05 -u 3 _
done
read -r -t 0.5 -u 3 _
if [ -e PG_VERSION ]; then echo present; else echo gone; fi >>"$2"`;
  const script = `exec 3<>"$3"
trap 'echo >"$1"; exit 0' QUIT
"$BASH" -c "$4" child "$1" "$2" &
while ${parentLives}; do read -r -t 0.05 -u 3 _; done`;
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
 *  container's removal, or the row's (`RemovalBounds`), where `ANSWER_MS`, `BOUND_MS` and
 *  `WATCH_MS` give them, with the start's own reader of where each path a process names leads, or
 *  the row's (`StatReader`), where `STAT_READER` gives it, and looking elsewhere only in
 *  `SERVED_UNDER`, the file's own directory (`plantsRoot`). */
const sweepScript = `
import { removeLeftoverPostgres } from ${JSON.stringify(removalModule)};
const { ANSWER_MS, BOUND_MS, WATCH_MS, STAT_READER, SERVED_UNDER } = process.env;
await removeLeftoverPostgres(
  ANSWER_MS === undefined
    ? undefined
    : { answerMs: Number(ANSWER_MS), boundMs: Number(BOUND_MS), watchMs: Number(WATCH_MS) },
  STAT_READER === undefined ? undefined : JSON.parse(STAT_READER),
  SERVED_UNDER
);
`;

/** The environment that gives a start's removal of a container in a child of this file the
 *  row's `bounds`. */
function boundsEnv({ answerMs, boundMs, watchMs }: RemovalBounds): Record<string, string> {
  return { ANSWER_MS: String(answerMs), BOUND_MS: String(boundMs), WATCH_MS: String(watchMs) };
}

/** A stand-in for the reader a start's holder read runs (`StatReader`): its program reads the
 *  paths from its standard input, separated by NUL, says it is ready, and writes where each path
 *  leads, in turn, as the start's own reader does; but at a path of `never`, or a path under one,
 *  it writes nothing more and waits, as a stat of a path on a mount whose server hangs waits, until
 *  it is killed or the start that ran it ends (its parent pid changes). It sets itself no bound of
 *  its own: one shorter than the row's limit would end the wait in the start's place, and a start
 *  with no bound would pass the row. Its own clock times the start's wait there, which no load on
 *  the start or the row stretches: it writes to `clock`, under its pid, when the kernel forked it,
 *  with the read and the group of paths it serves (`HOLDER_READ`, "<read> <top>"), and then the
 *  time it reached that path and the time again every 500 ms until it is killed
 *  (`clockOf`), each in seconds since the machine booted, the one clock the kernel gives both a
 *  process's start and `/proc/uptime` on. Its mount table is this process's, with each mount of
 *  `mounts` added after it, a mount point and its type, as the row plants a path of `never` on a
 *  network mount, and with no type where `type` is empty, as a line whose type the start cannot
 *  read: the file `table`, which the stand-in writes. The start waits on one path `bound` ms
 *  (`StatReader.statMs`): the row's own, a few seconds, so the row sees that wait end in that time
 *  and not in the start's own `statMs`. */
export function neverAnswering(
  never: readonly string[],
  clock: string,
  table: string,
  bound: number,
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
    statMs: bound,
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
write("forked", ticks / 100 + " " + (process.env.HOLDER_READ ?? ""));
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

/** A stand-in for the reader a start's holder read runs (`StatReader`), with this process's mount
 *  table and the start's own bound on one path (`statMs`), which it never runs out, as it answers
 *  each path at once: it reads where each path leads as the start's own reader does, but each path
 *  that is `to` or under it as the same path under `from`: what the kernel answers once `from` has
 *  been renamed to `to`, as another start's claim of a directory renames it (`claimForRemoval` of
 *  `e2e/harness-postgres-removal.ts`). A row so runs a holder read that looked at `from` before
 *  that rename and reads what the processes name after it, an order no row can time: the start
 *  looks at the directory and reads `/proc` with no `await` between them. */
export function readsAfterRename(from: string, to: string): StatReader {
  return {
    mounts: "/proc/self/mountinfo",
    statMs,
    run: [
      process.execPath,
      "--eval",
      `
const { readFileSync, statSync, writeSync } = require("node:fs");
const from = ${JSON.stringify(from)};
const to = ${JSON.stringify(to)};
const paths = readFileSync(0, "utf8").split("\\0");
writeSync(1, "ready\\n");
for (const path of paths) {
  const read = path === to || path.startsWith(to + "/") ? from + path.slice(to.length) : path;
  let line = "-";
  try {
    const stats = statSync(read, { bigint: true });
    line = stats.dev + ":" + stats.ino;
  } catch {}
  writeSync(1, line + "\\n");
}
`,
    ],
  };
}

/** Another start's removal of `CLAIM_PATH` under its claim, as a child of this file runs it: the
 *  claim's name, `<path>.removing-<pid>-<start>` with the child's own pid and start
 *  (`claimInfix`), is a directory the child makes with a directory `d0` in it, and the child holds
 *  `d0` open, as `rmSync` holds each directory it empties, until it is killed, or until this
 *  process, its parent, ends (`untilParentEnds`). The path comes in the environment, as a start's
 *  own arguments name none. */
const removingScript = `
const { mkdirSync, openSync, readFileSync } = require("node:fs");
${untilParentEnds}
const stat = readFileSync("/proc/self/stat", "utf8");
const start = stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19];
const claimed = process.env.CLAIM_PATH + ${JSON.stringify(claimInfix)} + process.pid + "-" + start;
mkdirSync(claimed + "/d0", { recursive: true });
globalThis.held = openSync(claimed + "/d0", "r");
console.log(claimed);
`;

/** Another start's removal of `path` under its claim, in a child of this file (`removingScript`).
 *  `path` keeps its name, so the row's sweep reads it as it was before that claim
 *  (`readsAfterRename`). Answers once the child printed its claim, and charges `row` for the child
 *  (`Row`). */
export async function removingInChild(
  row: Row,
  path: string
): Promise<{ claimed: string; claimer: Subprocess }> {
  const what = `the removal of ${path} under another start's claim`;
  row.child(what);
  const claimer = Bun.spawn([process.execPath, "--eval", removingScript], {
    env: { ...process.env, CLAIM_PATH: path },
    stdout: "pipe",
    stderr: "ignore",
  });
  try {
    const claimed = `${path}${claimInfix}${claimer.pid}-${marks.startOf(readFileSync(`/proc/${claimer.pid}/stat`, "utf8"))}`;
    expect((await firstLineOf(row, claimer, what)).trim()).toBe(claimed);
    return { claimed, claimer };
  } catch (error) {
    await endChild(claimer);
    throw error;
  }
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
 *  Docker (`docker` of `harness-postgres-leftover-fixtures.ts`), and one call of the image
 *  provider's that makes or starts a container, which the provider bounds by nothing
 *  (`providerStart` of that file): three times the 133 s that one `docker create` of a row took on
 *  a daemon a shared Linux machine shared, on 2026-11-19, while that daemon answered every other
 *  call, as the fixtures' other bounds are three times their largest reading. */
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
 *  each wait that waits for several things itself (`parts`, `Part`), such as a start of the pool
 *  in a child, at the bound the program sums for it, the longest of those it waits for at once
 *  (`AtOnce`); every other bound the row waits out (`bounds`, in ms): the bound of a removal, of
 *  an `until`, of the row's own writes; and the bound of each wait of its `finally` before it
 *  removes what it made, such as the stop of a stack it started (`ends`, in ms, or a `Part`).
 *  Those come after the row's deadline (`Row.deadline`), which ends every fixture call of the
 *  row's own waits: a call that runs to the deadline then leaves the `finally` the time its stops
 *  take, and the removals after them the room above it (`Row.cleanup`), where a deadline that
 *  counted the stops left a removal none, and the container of a stack the row killed stayed. The
 *  room above all of them is `rowRoomMs`, or the row's own (`roomMs`), a few seconds, for a row
 *  that sees that room run out in real time, so it takes seconds and not the room's minute. */
export interface Waits {
  readonly children: number;
  readonly reads: number;
  readonly stops?: number;
  readonly makes?: number;
  readonly bounds: readonly number[];
  readonly parts?: readonly (Part | AtOnce)[];
  readonly ends?: readonly (number | Part)[];
  readonly roomMs?: number;
}

/** What only `partOf` puts in a `Part`, so that no row and no fixture writes a part, or its bound,
 *  by hand: a part written as an object of its own is a compiler error. */
const summed: unique symbol = Symbol("partOf");

/** One wait of a row that waits for several things itself, and whose bound the program sums from
 *  them: a start of the pool, a stack's start or its stop, a shell's start, the end of a child the
 *  row killed, each a child that `harness-postgres-leftover-fixtures.ts` ends at that bound, or a
 *  start of the provider that a row runs in its own process. `what` is its kind, which the row's
 *  charge reads (`Row.part`), and `ms` its bound (`partOf`): what it waits for, one wait after
 *  another (`Waits`), the waits of its own end, and `rowRoomMs` above them, the same program as a
 *  row's own limit (`rowMs`). So a part's bound is above the bound of every wait inside it, and
 *  moves with each of them. A start of the pool that a row gave a fixed 600 s, while the start's
 *  removal of one container may wait 600 s itself, was killed inside its own bounds on a shared
 *  Linux machine on 2026-11-19. */
export interface Part {
  readonly what: string;
  readonly ms: number;
  readonly [summed]: true;
}

/** The `Part` of the kind `what` that waits for `waits`. */
export function partOf(what: string, waits: Waits): Part {
  return { what, ms: rowMs(waits), [summed]: true };
}

/** Parts a row starts at once and waits for together, of which the longest counts (`Waits`). */
export interface AtOnce {
  readonly atOnce: readonly Part[];
}

/** The waits of `all`, one after another, as one `Waits`: their counts added, and their bounds,
 *  parts and ends one list each, in turn. */
export function inTurn(...all: readonly Waits[]): Waits {
  const count = (of: (waits: Waits) => number | undefined) =>
    all.reduce((sum, waits) => sum + (of(waits) ?? 0), 0);
  return {
    children: count((waits) => waits.children),
    reads: count((waits) => waits.reads),
    stops: count((waits) => waits.stops),
    makes: count((waits) => waits.makes),
    bounds: all.flatMap((waits) => waits.bounds),
    parts: all.flatMap((waits) => waits.parts ?? []),
    ends: all.flatMap((waits) => waits.ends ?? []),
  };
}

/** The holder reads a row's sweeps may make in all (`Waits`): those before a claim, and those after
 *  each stop. */
function readsOf({ reads, stops = 0 }: Waits): number {
  return reads + stops * readsAfterStop;
}

/** Everything a row waits for, one wait after another (`Waits`), in ms: the limit of a child the
 *  row ends itself, and of an `until` of an event that comes after those waits. */
export function waitsMs(waits: Waits): number {
  const { children, stops = 0, makes = 0, bounds, parts = [] } = waits;
  return (
    children * claimEventMs +
    readsOf(waits) * (statStartMs + statMs) +
    stops * stopMs +
    makes * makeMs +
    bounds.reduce((sum, ms) => sum + ms, 0) +
    // Of parts the row waits for at once, the longest.
    parts.reduce(
      (sum, part) =>
        sum + ("atOnce" in part ? Math.max(0, ...part.atOnce.map((each) => each.ms)) : part.ms),
      0
    )
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
 *  (`Waits.ends`), and its room above them, `rowRoomMs` or its own (`Waits.roomMs`). Every row of
 *  the files these fixtures serve that counts its waits (`rowOf`) states its limit so, and no
 *  other way, which a row of `harness-postgres-removal.test.ts` reads from their source
 *  (`limitsNotSummed`). */
export function rowMs(waits: Waits): number {
  const ends = (waits.ends ?? []).reduce<number>(
    (sum, end) => sum + (typeof end === "number" ? end : end.ms),
    0
  );
  return waitsMs(waits) + ends + (waits.roomMs ?? rowRoomMs);
}

/** What a row started so far, held to what its limit counts (`Waits`): each fixture that starts a
 *  `bun` child charges the row for it (`child`), each sweep charges the holder reads it made, as
 *  the reader it ran counted them (`reads`), each fixture call that makes a container or an image
 *  charges it for that call (`made`), and each fixture that waits for a part charges it for that
 *  part before the wait (`part`). A row that starts more than its limit counts fails there, naming
 *  what the limit left out, whatever the load: its limit is below what it waits for. A part counts
 *  where the row's waits hold one of its kind whose bound reaches the part's, before the deadline
 *  (`Waits.parts`) or in the `finally` (`Waits.ends`), and each counted part serves one wait; the
 *  charge answers when that wait must end by where the row's step cut its limit (`cut`): the
 *  deadline, or `stops` for a wait of the `finally`.
 *  `ms` is how long the row's waits may take, `waitsMs` of them, and `deadline` is
 *  when those waits end, from the row's start, on the machine's clock (`machine` of
 *  `e2e/harness-postgres-container-removal.ts`): a fixture call of Docker or Postgres waits until
 *  then and no longer, so it ends within the row's own limit with a reason that names it.
 *  `stops` is when the waits of the row's `finally` (`Waits.ends`), such as the stop of a stack
 *  it started, end, and `cleanup` when a fixture call that removes what the row made waits until:
 *  after them, and before half the room above them (`rowRoomMs`, or `Waits.roomMs`), so a
 *  removal's line, and not the row's own clock, ends the row. `end` is when the row's own limit
 *  ends it (`rowMs` from its start), until which each stand-in the row needs alive lives
 *  (`lifeOf`).
 *  Where the CI step that runs the row ends before that (`stepEnds`), the step's end is the row's
 *  end, and `cut` says so: the deadline is that end less the row's own time for its `finally`
 *  (its stops, and the room above them in which it removes what it made), each scaled by what the
 *  step leaves of the row's own limit, so the row's waits and its `finally` share that time in the
 *  proportion of their bounds. Every wait of the row then ends by its share (`boundOf`), naming
 *  what it waited for and the step's end, so that a row whose parts hang ends inside its step
 *  with its own reason and still removes what it made, and not by the step's own clock. A run on
 *  a machine of one's own sets no such end, and its rows keep their own limits. */
export interface Row {
  readonly ms: number;
  readonly deadline: number;
  readonly stops: number;
  readonly cleanup: number;
  readonly end: number;
  readonly cut: string | undefined;
  child(what: string): void;
  reads(made: number, what: string): void;
  made(what: string): void;
  part(part: Part): number;
}

/** How long a stand-in process that the row `row` needs alive lives, in whole seconds, rounded
 *  up: until the row ends (`Row.end`), so that no wait of the row, inside its bounds, outlives it,
 *  and it ends by itself where the row did not end it. A stand-in a row gave a fixed 600 s died
 *  inside the row's waits on a shared Linux machine on 2026-11-19, when a start of the pool the
 *  row waited for took 620 s inside its own bounds. A stand-in whose parent outlives the row waits
 *  for its parent's end instead (`untilThisEnds`, `untilParentEnds` of `planted-marks.ts`): this is
 *  for one that must outlive the process that started it, as a `docker` call that hangs in its
 *  daemon outlives the start that ran it (`neverEnds`). */
export function lifeOf(row: Row): number {
  return Math.max(1, Math.ceil((row.end - machine.now()) / 1000));
}

/** The line of a stand-in `docker`'s body (`standInDocker`) for a call that Docker never ends: it
 *  becomes `sleep`, with the stand-in's path for its name (`exec -a "$0"`), so `running` finds it,
 *  until the row `row` ends (`lifeOf`), and so outlives the start that ran it, as a `docker` that
 *  hangs in its daemon does, and a row reads whether that start stopped it. */
export function neverEnds(row: Row): string {
  return `exec -a "$0" sleep ${lifeOf(row)}`;
}

/** How long a wait of the row `row` whose own bound is `ms` may run from now, and how a wait that
 *  ran that time out says so: its own bound, or, where the end of the row's CI step cut the row's
 *  limit (`Row.cut`), the time left until `by`, the row's deadline, or `Row.stops` for a wait of
 *  the row's `finally`, where that is shorter. A bound of `Infinity` is a wait that sets none of
 *  its own. */
export function boundOf(row: Row, ms: number, by = row.deadline): { ms: number; named: string } {
  const left = by - machine.now();
  if (row.cut === undefined || left >= ms) return { ms, named: `within ${ms / 1000} s` };
  const own = Number.isFinite(ms) ? `, before its own ${ms / 1000} s` : "";
  return {
    ms: Math.max(0, left),
    named: `by the row's deadline, ${Math.max(0, left) / 1000} s after this wait began, which ${row.cut} set${own}`,
  };
}

/** `ms` scaled by `limit / own`, the share of a row's own limit `own` that its CI step leaves it,
 *  in whole ms, rounded down exactly: the product first and then one division. A share taken first
 *  (`ms * (limit / own)`) rounds twice, so where the exact share is a whole number of ms it can fall
 *  a hair below it and floor one ms short: a run of the step-cut row of
 *  `harness-postgres-removal.test.ts` read 437 for an exact 438 on 2026-11-19. Each operand is a
 *  whole number of ms, and their product lies far below 2^53, so it is exact; the one division then
 *  errs by less than one part in 2^52 of its quotient, while a quotient that is not a whole number
 *  lies at least 1/`own` from every whole number, so it floors as the exact quotient does. */
function shareOf(ms: number, limit: number, own: number): number {
  return Math.floor((ms * limit) / own);
}

/** The `Row` of a row whose limit is `rowMs(waits)`, or the time its CI step leaves
 *  (`stepEnds`) where that is shorter, which starts now. */
export function rowOf(waits: Waits): Row {
  let children = 0;
  let reads = 0;
  let makes = 0;
  // Each part the limit counts, before the deadline or in the `finally`, until a wait uses it.
  const counted = [
    ...(waits.parts ?? [])
      .flatMap((part) => ("atOnce" in part ? part.atOnce : [part]))
      .map((part) => ({ part, inFinally: false })),
    ...(waits.ends ?? [])
      .filter((end): end is Part => typeof end !== "number")
      .map((part) => ({ part, inFinally: true })),
  ];
  const start = machine.now();
  const own = rowMs(waits);
  const room = waits.roomMs ?? rowRoomMs;
  const step = stepEnds();
  const limit = step === undefined ? own : Math.max(0, Math.min(own, step - start));
  // What the step leaves of the row's own limit: all of it where the step leaves it all, as off CI.
  const share = limit / own;
  const deadline = start + shareOf(waitsMs(waits), limit, own);
  // The waits of the `finally` after the deadline: its stops, then the room for its removals.
  const stops = deadline + shareOf(own - waitsMs(waits) - room, limit, own);
  const cut =
    step !== undefined && limit < own
      ? `the end of the CI step that runs it, at ${new Date(step).toISOString()} (${stepEndsVariable}), ${limit / 1000} s after the row began and before its own ${own / 1000} s`
      : undefined;
  return {
    ms: deadline - start,
    deadline,
    stops,
    // Half the room after the stops: the row's limit less half its share of the room.
    cleanup: start + limit - (room * share) / 2,
    end: start + limit,
    cut,
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
    part: (part) => {
      // The least counted bound that holds the part, so that a longer one stays for a longer wait.
      const fits = counted
        .filter((each) => each.part.what === part.what && each.part.ms >= part.ms)
        .sort((a, b) => a.part.ms - b.part.ms)[0];
      if (fits === undefined) {
        const left = counted
          .filter((each) => each.part.what === part.what)
          .map((each) => each.part.ms / 1000);
        throw new Error(
          `the row's limit counts ${left.length === 0 ? "nothing" : `${left.join(" s, ")} s`} for ${part.what} it has not waited for yet, and it waits for ${part.what} for up to ${part.ms / 1000} s: its limit is below what it waits for`
        );
      }
      counted.splice(counted.indexOf(fits), 1);
      return fits.inFinally ? stops : deadline;
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
 *  once the child ended, for the holder reads its sweep made (`Row`). The sweep sets no bound of
 *  its own, but where its CI step cut the row's limit (`Row.cut`) the row ends it at the row's
 *  deadline and throws, naming it (`boundOf`). */
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
    env: {
      TMPDIR: tmp,
      PATH: empty,
      STAT_READER: JSON.stringify(countedReader(starts, reader)),
      SERVED_UNDER: plantsRoot(),
    },
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
    const { ms, named } = boundOf(row, Number.POSITIVE_INFINITY);
    const code = await (Number.isFinite(ms)
      ? Promise.race([child.exited, Bun.sleep(ms).then(() => undefined)])
      : child.exited);
    if (code === undefined) await endChild(child);
    await read;
    const made = readsMade(starts);
    rmSync(counts, { recursive: true, force: true });
    row.reads(made, what);
    if (code === undefined) throw new Error(`${what} did not end ${named}: ${stderr}`);
    if (code !== 0) throw new Error(`the sweep exited ${code}: ${stderr}`);
    return stderr.split("\n");
  })();
  return { stderr: () => stderr, lines, child };
}

/** Runs the start's sweep in a child (`startSweep`), and answers the lines it printed. */
export function sweep(row: Row, tmp: string, empty?: string): Promise<string[]> {
  return startSweep(row, tmp, { empty }).lines;
}

/** Waits until `met` holds, and throws naming `what` where it has not after `ms`, or by the row's
 *  deadline where its CI step cut the row's limit (`boundOf`). What it waits for is a write of
 *  another process (a stand-in's signal line, a child's line), which no event of this process
 *  reports, so it reads again every 50 ms. */
export async function until(row: Row, met: () => boolean, what: string, ms: number): Promise<void> {
  const bound = boundOf(row, ms);
  const deadline = Date.now() + bound.ms;
  while (!met()) {
    if (Date.now() >= deadline) throw new Error(`${what} did not come ${bound.named}`);
    await Bun.sleep(50);
  }
}

/** A run directory of another `TMPDIR` than the sweep's, in the file's own directory
 *  (`plantsRoot`), with what a stack left there: a stand-in postmaster of stack 0 serving from its
 *  data directory, which every start finds, or only that data directory where `serving` is false,
 *  and the mark of `owner` beside it, or no mark where `owner` is undefined. */
export function leaveElsewhere(owner?: unknown, serving = true) {
  const other = mkdtempSync(join(plantsRoot(), "marks-elsewhere-"));
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
 *  claim, and then waits to be killed, or for the end of this process, its parent
 *  (`untilParentEnds`). No process holds what a row claims so, so it reads none. */
const claimScript = `
import { claimForRemoval } from ${JSON.stringify(removalModule)};
${untilParentEnds}
console.log(JSON.stringify(await claimForRemoval(process.env.CLAIM_PATH, process.getuid(), async () => ({ holders: [], unread: [] }))));
`;

/** SIGKILLs `child`, a child of this file, where it still runs, and waits for its end. */
export async function endChild(child: Subprocess): Promise<void> {
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  await child.exited;
}

/** What `child` printed up to its first line feed, or all it printed where it ended first. It
 *  waits with no bound of its own, but where its CI step cut the row's limit (`Row.cut`) it throws
 *  at the row's deadline, naming `what`, the child it waits for (`boundOf`). */
export async function firstLineOf(
  row: Row,
  child: Subprocess<"ignore", "pipe", "ignore">,
  what: string
): Promise<string> {
  const { ms, named } = boundOf(row, Number.POSITIVE_INFINITY);
  const ends = Date.now() + ms;
  let printed = "";
  const reader = child.stdout.getReader();
  const decoder = new TextDecoder();
  try {
    while (!printed.includes("\n")) {
      const chunk = await (Number.isFinite(ms)
        ? Promise.race([
            reader.read(),
            Bun.sleep(Math.max(0, ends - Date.now())).then(() => "late" as const),
          ])
        : reader.read());
      if (chunk === "late") throw new Error(`the first line of ${what} did not come ${named}`);
      if (chunk.done) break;
      printed += decoder.decode(chunk.value, { stream: true });
    }
  } finally {
    reader.releaseLock();
  }
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
  const what = `the claim of ${path}`;
  row.child(what);
  const claimer = Bun.spawn([process.execPath, "--eval", claimScript], {
    env: { ...process.env, CLAIM_PATH: path },
    stdout: "pipe",
    stderr: "ignore",
  });
  try {
    const claimed = `${path}.removing-${claimer.pid}-${marks.startOf(readFileSync(`/proc/${claimer.pid}/stat`, "utf8"))}`;
    const printed = await firstLineOf(row, claimer, what);
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
 *  killed, which is the row's own SIGKILL, or for the end of this process, its parent
 *  (`untilParentEnds`). `HARNESS_DATA`, where it is set, is the data directory as the stack
 *  spells it to `pg_ctl -D`, and `HARNESS_CWD` the directory that `pg_ctl` runs in: a spelling of
 *  its own, which the mark does not hold. */
export const harnessScript = `
import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { markPostgres } from ${JSON.stringify(marksModule)};
${untilParentEnds}
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
 *  the row's own starts find (`postmasterProgram`): it waits for `go`, and then starts that
 *  postmaster with `<data>` for its working directory, names it in `<data>`'s `postmaster.pid` and
 *  ends, as `pg_ctl -w` ends once its server is up; where `<data>` is gone, it fails, as `pg_ctl`
 *  does. It uses shell builtins and that postmaster alone: it waits with `read -t` on a FIFO in
 *  `bin` (`fifoAt`), so it forks no process while it waits. A wait of `sleep 0.05` forked a shell
 *  that named `<data>` in its arguments until it became `sleep`, and a start's holder read that
 *  met that moment named it as a second holder beside `pg_ctl`: 1 of 20 runs of a row on a shared
 *  Linux machine on 2026-11-19, and 2,830 of 301,387 reads of the stand-in's children in 10 s.
 *  The stack that runs it dies first, as a stack killed hard does, so it outlives its parent, and
 *  so does the postmaster it starts: both end on their own once `life` seconds have passed, the
 *  time the row that runs them has left (`lifeOf`); a `pg_ctl` that `go` did not reach by then
 *  starts nothing. */
export function startingPgCtl(bin: string, go: string, life: number): void {
  const sleep = Bun.which("sleep");
  if (sleep === null) throw new Error("no sleep on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, sleep, "its TMPDIR");
  const pause = fifoAt(join(bin, "pg_ctl.pause"));
  writeFileSync(
    join(bin, "pg_ctl"),
    `#!/usr/bin/env bash\nend=$((SECONDS + ${life}))\nexec 3<>${JSON.stringify(pause)}\n` +
      `until [ -e ${JSON.stringify(go)} ] || [ "$SECONDS" -ge "$end" ]; do read -r -t 0.05 -u 3 _; done\n` +
      `[ -e ${JSON.stringify(go)} ] || exit 1\n` +
      // The postmaster gets no descriptor of the FIFO.
      "exec 3<&-\n" +
      // A command that a script runs in the background ignores SIGQUIT unless job control is
      // on, and a start's stop of a postmaster sends SIGQUIT first.
      `cd "$2" || exit 1\nset -m\n${JSON.stringify(postgres)} "$((end > SECONDS ? end - SECONDS : 1))" </dev/null >/dev/null 2>&1 &\n` +
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
const { CONTAINER_ID: id = "", ANSWER_MS, BOUND_MS, WATCH_MS } = process.env;
const bounds = { answerMs: Number(ANSWER_MS), boundMs: Number(BOUND_MS), watchMs: Number(WATCH_MS) };
console.log(JSON.stringify(await removeContainer(id, undefined, bounds)));
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
 *  how many seconds it took; kills the child and throws where it has not ended within everything
 *  `row` waits for (`Row.ms`), or by its deadline where its CI step cut its limit (`boundOf`), and
 *  where it exits other than 0. */
async function inChild(script: string, bin: string, env: Record<string, string>, row: Row) {
  const started = Date.now();
  const { ms, named } = boundOf(row, row.ms);
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
    throw new Error(`the child did not end ${named}`);
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
    row
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
 *  for `makeMs`. Where `BEFORE` is set (`{ what, ms }`), the row first waits `ms`, a wait it names
 *  `what` (`during`), as a row awaits a start of its own. Where `RUN` is set it then runs a row's
 *  `finally`, as the leftover rows do: a stop that takes `STOP_MS`, which the row's `Waits.ends`
 *  counts, and then the removal of each container whose mark names `RUN` (`removeContainersOf`),
 *  and prints whether that removal ended or what it threw. */
const fixtureCallScript = `
import { docker, during, removeContainersOf } from ${JSON.stringify(leftoverFixtures)};
import { rowOf } from ${JSON.stringify(fileURLToPath(import.meta.url))};
const { ARGS = "[]", BEFORE, RUN, STOP_MS, WAITS = "" } = process.env;
const row = { ...rowOf(JSON.parse(WAITS)), made() {} };
const ended = {};
try {
  if (BEFORE !== undefined) {
    const { what, ms } = JSON.parse(BEFORE);
    await during(what, () => Bun.sleep(ms));
  }
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
 *  `bin` (`fixtureCallScript`), for a row of the waits `waits` that starts with the child: first,
 *  where `before` is given, a wait of `before.ms` that the row names `before.what`; then the call;
 *  and then, where `end` is given, that row's `finally`: a stop of `end.stopMs` and the removal of
 *  the containers whose mark names `end.run`. Charges `row` for the child. Answers what the call
 *  answered or threw, what the removal did, and the lines the child printed; kills the child and
 *  throws where it has not ended within everything the row waits for (`Row.ms`). */
export async function fixtureDockerInChild(
  row: Row,
  bin: string,
  args: readonly string[],
  waits: Waits,
  {
    before,
    end,
  }: {
    readonly before?: { readonly what: string; readonly ms: number };
    readonly end?: { readonly stopMs: number; readonly run: string };
  } = {}
) {
  row.child(`the fixture's docker ${args.join(" ")}`);
  const { stdout, stderr } = await inChild(
    fixtureCallScript,
    bin,
    {
      WAITS: JSON.stringify(waits),
      ARGS: JSON.stringify(args),
      ...(before === undefined ? {} : { BEFORE: JSON.stringify(before) }),
      ...(end === undefined ? {} : { RUN: end.run, STOP_MS: String(end.stopMs) }),
    },
    row
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
      {
        TMPDIR: tmp,
        ...boundsEnv(bounds),
        STAT_READER: JSON.stringify(countedReader(starts)),
        SERVED_UNDER: plantsRoot(),
      },
      row
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
