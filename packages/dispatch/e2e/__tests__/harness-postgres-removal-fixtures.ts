/** The fixtures of the rows of `harness-postgres-removal.test.ts`: the marks, data directories and
 *  stand-in postmasters a row plants, the start's sweep and a start's claim, each run in a child
 *  of the row, the end of such a child, and the stack's harness and the stand-in `pg_ctl` a row
 *  kills hard. The rows and what they require stay in that file. */

import { expect } from "bun:test";
import {
  chmodSync,
  copyFileSync,
  mkdirSync,
  mkdtempSync,
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

const removalModule = fileURLToPath(new URL("../harness-postgres-removal.ts", import.meta.url));

/** The start's sweep, as a child of this file runs it. */
const sweepScript = `
import { removeLeftoverPostgres } from ${JSON.stringify(removalModule)};
await removeLeftoverPostgres();
`;

/** A start's sweep running in a child with `tmp` for its `TMPDIR` and `empty`, a directory with no
 *  program in it, for its `PATH`, so no row changes the environment of this process: what it
 *  printed so far, and the lines it printed once it ended. */
export function startSweep(
  tmp: string,
  empty = mkdtempSync(join(tmp, "no-programs-"))
): { stderr(): string; readonly lines: Promise<string[]> } {
  const child = Bun.spawn([process.execPath, "--eval", sweepScript], {
    env: { TMPDIR: tmp, PATH: empty },
    stdout: "ignore",
    stderr: "pipe",
  });
  let stderr = "";
  const read = (async () => {
    const reader = child.stderr.getReader();
    const decoder = new TextDecoder();
    for (let chunk = await reader.read(); !chunk.done; chunk = await reader.read()) {
      stderr += decoder.decode(chunk.value, { stream: true });
    }
  })();
  const lines = (async () => {
    const [code] = await Promise.all([child.exited, read]);
    if (code !== 0) throw new Error(`the sweep exited ${code}: ${stderr}`);
    return stderr.split("\n");
  })();
  return { stderr: () => stderr, lines };
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
 *  claim, and then waits to be killed, for at most 600 s. */
const claimScript = `
import { claimForRemoval } from ${JSON.stringify(removalModule)};
setTimeout(() => process.exit(1), 600_000);
console.log(JSON.stringify(await claimForRemoval(process.env.CLAIM_PATH, process.getuid())));
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
 *  killed, which is the row's own SIGKILL, for at most 600 s. */
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
const ctl = spawn(join(bin, "pg_ctl"), ["-D", data, "-l", log, "-o", options, "-w", "start"], {
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
