/** The fixtures of the rows of `harness-postgres-removal.test.ts` and
 *  `harness-postgres-holders.test.ts`: the marks, data directories and stand-in postmasters a row
 *  plants, the start's sweep, a start's claim and a start's removal of one container, each run in
 *  a child of the row, the stand-in `docker` of that removal, the end of such a child, and the
 *  stack's harness and the stand-in `pg_ctl` a row kills hard. The rows and what they require stay
 *  in those files. */

import { expect } from "bun:test";
import {
  chmodSync,
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Subprocess } from "bun";

import * as marks from "../harness-postgres-marks";
import type { StatReader } from "../harness-postgres-processes";
import type { RemovalBounds } from "../harness-postgres-removal";
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

/** A stand-in for the program a start's holder read runs (`StatReader`): it reads the paths from
 *  its standard input, separated by NUL, says it is ready, and writes where each path leads, in
 *  turn, as the start's own reader does; but at `never` it writes nothing more and waits, as a stat
 *  of a path on a mount whose server hangs waits, until it is killed or the start that ran it
 *  ends (its parent pid changes). It sets itself no bound of its own: one shorter than the row's
 *  limit would end the wait in the start's place, and a start with no bound would pass the row. */
export function neverAnswering(never: string): StatReader {
  return [
    process.execPath,
    "--eval",
    `
const { readFileSync, statSync, writeSync } = require("node:fs");
const start = process.ppid;
setInterval(() => {
  if (process.ppid !== start) process.exit(1);
}, 1_000);
const paths = readFileSync(0, "utf8").split("\\0");
writeSync(1, "ready\\n");
for (const path of paths) {
  if (path === ${JSON.stringify(never)}) break;
  let line = "-";
  try {
    const stats = statSync(path, { bigint: true });
    line = stats.dev + ":" + stats.ino;
  } catch {}
  writeSync(1, line + "\\n");
}
`,
  ];
}

/** A start's sweep running in a child with `tmp` for its `TMPDIR` and `empty`, a directory with no
 *  program in it, for its `PATH`, so no row changes the environment of this process, and with the
 *  row's `reader` where it gives one: what it printed so far, the lines it printed once it ended,
 *  and the child, for a row to end. */
export function startSweep(
  tmp: string,
  empty = mkdtempSync(join(tmp, "no-programs-")),
  reader?: StatReader
): { stderr(): string; readonly lines: Promise<string[]>; readonly child: Subprocess } {
  const child = Bun.spawn([process.execPath, "--eval", sweepScript], {
    env: {
      TMPDIR: tmp,
      PATH: empty,
      ...(reader === undefined ? {} : { STAT_READER: JSON.stringify(reader) }),
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
    const [code] = await Promise.all([child.exited, read]);
    if (code !== 0) throw new Error(`the sweep exited ${code}: ${stderr}`);
    return stderr.split("\n");
  })();
  return { stderr: () => stderr, lines, child };
}

/** Runs the start's sweep in a child (`startSweep`), and answers the lines it printed. */
export function sweep(tmp: string, empty?: string): Promise<string[]> {
  return startSweep(tmp, empty).lines;
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
 *  owner, and the claimer, which holds the claim until it is killed (`endChild`). */
export async function claimInChild(
  path: string
): Promise<{ claimed: string; claimer: Subprocess }> {
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
export async function claimAndDie(path: string): Promise<string> {
  const { claimed, claimer } = await claimInChild(path);
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
 *  it fails, as `pg_ctl` does. It uses shell builtins and that postmaster alone. */
export function startingPgCtl(bin: string, go: string): void {
  const sleep = Bun.which("sleep");
  if (sleep === null) throw new Error("no sleep on PATH to stand in for a postmaster");
  const postgres = postmasterProgram(bin, sleep, "its TMPDIR");
  writeFileSync(
    join(bin, "pg_ctl"),
    `#!/usr/bin/env bash\nend=$((SECONDS + 600))\n` +
      `until [ -e ${JSON.stringify(go)} ] || [ "$SECONDS" -ge "$end" ]; do sleep 0.05; done\n` +
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

/** How long a row waits for one event of another process: a `bun` child's start of its sweep
 *  under load, which took more than 180 s once on a shared Linux machine under a load of 1,217, on
 *  2026-11-17, is the longest of them. */
export const claimEventMs = 240_000;

/** A start's removal of the container `CONTAINER_ID` (`removeContainer`) in a child of this file,
 *  within the row's bounds (`boundsEnv`): it prints how the removal ended, and its own lines on
 *  stderr. */
const removeScript = `
import { removeContainer } from ${JSON.stringify(removalModule)};
const { CONTAINER_ID: id = "", ANSWER_MS, BOUND_MS } = process.env;
console.log(JSON.stringify(await removeContainer(id, undefined, { answerMs: Number(ANSWER_MS), boundMs: Number(BOUND_MS) })));
`;

/** A directory in `tmp` with a stand-in `docker`, which runs no real one, so a row of it acts on
 *  no container of the machine: it writes the arguments of each call it gets as a line of
 *  `calls`, and the stat of its own process as a file of `processes`, and then runs the lines of
 *  bash `body`. A body that execs another program keeps the stand-in's path as its name
 *  (`exec -a "$0"`), so `running` finds it. Answers the directory, the calls so far, each process
 *  the stand-in wrote, by its pid and start, and each process that runs now under its path. */
export function standInDocker(tmp: string, body: readonly string[]) {
  const bin = mkdtempSync(join(tmp, "docker-"));
  const calls = join(bin, "calls");
  const processes = join(bin, "processes");
  mkdirSync(processes);
  writeFileSync(
    join(bin, "docker"),
    [
      "#!/usr/bin/env bash",
      `echo "$*" >>${JSON.stringify(calls)}`,
      `cat /proc/$$/stat >${JSON.stringify(processes)}/$$`,
      ...body,
      "",
    ].join("\n"),
    { mode: 0o755 }
  );
  const ofStandIn = `${join(bin, "docker")}\0`;
  return {
    bin,
    calls: (): string[] => readFileSync(calls, "utf8").trim().split("\n"),
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
 *  within `bounds`. Answers how the removal ended, the lines the start printed, and how many
 *  seconds the child took; kills the child and throws where it has not ended within `ms`. */
export async function removeInChild(bin: string, id: string, bounds: RemovalBounds, ms: number) {
  const { stdout, stderr, seconds } = await inChild(
    removeScript,
    bin,
    { CONTAINER_ID: id, ...boundsEnv(bounds) },
    ms
  );
  return { removal: JSON.parse(stdout) as unknown, lines: stderr.split("\n"), seconds };
}

/** Runs the start's whole sweep (`sweepScript`) in a child with `tmp` for its `TMPDIR` and `bin`,
 *  a directory holding a stand-in `docker`, first on its `PATH`, within `bounds`. Answers the lines
 *  it printed and how many seconds it took; kills the child and throws where it has not ended
 *  within `ms`. */
export async function sweepInChild(tmp: string, bin: string, bounds: RemovalBounds, ms: number) {
  const { stderr, seconds } = await inChild(
    sweepScript,
    bin,
    { TMPDIR: tmp, ...boundsEnv(bounds) },
    ms
  );
  return { lines: stderr.split("\n"), seconds };
}
