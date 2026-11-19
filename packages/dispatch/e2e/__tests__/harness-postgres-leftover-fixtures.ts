/** The fixtures of the rows of `harness-postgres-leftover.test.ts`: the children those rows start
 *  and stop, the stacks and the starts of the pool that the children run, Docker's reads and the
 *  removals of what a row made, and the marks, directories and containers that a row plants.
 *  `bun test` collects only `*.test.ts` and its kin, so this module runs nowhere of its own. It
 *  holds no assertion: each check of a row is in the test file.
 *
 *  A child is a process that `launch` starts, records by its pid and its start time, and collects
 *  the output of. A SIGKILL goes only to such a child, while `/proc` still names that process.
 *  Each name a row gives is of this run (`runTag`), and each container a row made is removed by
 *  the id Docker made it under.
 *
 *  A CHILD'S LIMIT IS THE SUM OF WHAT IT WAITS FOR. Each child a row waits for here, a start of the
 *  pool, a stack's start or its stop, a shell's start, is a part of the row's waits (`Part` of
 *  `harness-postgres-removal-fixtures.ts`), whose bound the program sums from the bounds of the
 *  waits inside the child, the start's own included, and the fixture that waits for it charges
 *  the row first (`Row.part`). So no child is killed inside its own bounds, and a row whose limit
 *  does not count a part fails there, naming it. */

import { execFile, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import type { Subprocess } from "bun";
import { harnessListeners } from "../harness-listeners";
import {
  pgBinVariable,
  pgConnectionsVariable,
  pgImageVariable,
  readyMs,
  type StackDatabase,
  type StackPostgres,
  socketDirectory,
  socketFile,
} from "../harness-postgres";
import {
  dockerMs,
  machine,
  removalBounds,
  removeContainer,
} from "../harness-postgres-container-removal";
import {
  identityOf,
  markLabel,
  marksDirectory,
  type Owner,
  runDirectoryPrefix,
  stackOwner,
  startOf,
} from "../harness-postgres-marks";
import { claimWaitMs } from "../harness-postgres-removal";
import {
  boundOf,
  claimEventMs,
  inTurn,
  type Part,
  partOf,
  type Row,
  type Waits,
} from "./harness-postgres-removal-fixtures";
import type { ProviderVariable } from "./postgres-provider";

const execFileAsync = promisify(execFile);

/** What this file's own lines are prefixed with, as every starter in `e2e/` names itself. */
const named = "e2e/__tests__/harness-postgres-leftover-fixtures.ts";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const harnessPostgres = fileURLToPath(new URL("../harness-postgres.ts", import.meta.url));
const harnessPool = fileURLToPath(new URL("../harness-pool.ts", import.meta.url));

/** What the names of this run's stacks and containers hold: random bytes, never the pid, so two
 *  runs of the rows at once, or a run after one whose container stayed, share no name. */
export const runTag = randomBytes(4).toString("hex");

/** A stack that starts its Postgres as the pool starts each stack's, prints where it serves,
 *  and stops it once its stdin closes. A SIGKILL stops nothing. */
const stackScript = `
import { mkdtempSync, readlinkSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { stackPostgres } from ${JSON.stringify(harnessPostgres)};
const dir = mkdtempSync(join(tmpdir(), ${JSON.stringify(runDirectoryPrefix)}));
const database = await stackPostgres({ dir, run: process.env.LEFTOVER_RUN }).start(0);
console.log(JSON.stringify({ dir, url: database.url, address: database.address, pidNamespace: readlinkSync("/proc/self/ns/pid") }));
for await (const _ of Bun.stdin.stream()) {}
await database.stop();
rmSync(dir, { recursive: true, force: true });
`;

/** The pool's start, given no provider: it removes what stacks that are gone left, and then
 *  refuses two stacks without a Postgres, which is the line it prints. */
const startScript = `
import { startStacks } from ${JSON.stringify(harnessPool)};
try {
  await startStacks(2, { env: {} });
  console.log("started");
} catch (error) {
  console.log("refused: " + error.message);
}
`;

/** What the refusal of a start says: the pool's start ran up to its provider check. */
export const refused =
  "refused: 2 Playwright workers run 2 stacks, each with a Postgres of its own";

/** A whole start of the pool, `startStacks`, with the provider of its environment: it removes what
 *  stacks that are gone left, starts one stack's Postgres and the harness a deployed run starts
 *  (the fake Envoy and the plain-HTTP proxy, which `PLAYWRIGHT_BASE_URL` selects, so no Go build
 *  runs), prints the stack's database, and stops it all once its stdin closes. */
const poolScript = `
import { startStacks, stopStacks } from ${JSON.stringify(harnessPool)};
const stacks = await startStacks(1, { env: process.env });
console.log(JSON.stringify({ url: stacks[0]?.env.DATABASE_URL }));
for await (const _ of Bun.stdin.stream()) {}
await stopStacks(false);
`;

/** How long `pg_ctl -w` waits for a server to start or to stop before it gives up: `PGCTLTIMEOUT`,
 *  60 s by default, which the binaries provider leaves unset (e2e/harness-postgres.ts). */
export const pgCtlMs = 60_000;

/** What a `bun` child waits for before its own work: its start, which may take `claimEventMs`
 *  under load (`Waits.children`). */
const bunStart: Waits = { children: 1, reads: 0, bounds: [] };

/** What a start of one stack's Postgres waits for, one wait after another (`start` of
 *  `stackPostgres` of e2e/harness-postgres.ts), for the provider `variable` names. The image
 *  provider's: its `docker create` and its `docker start`, each a call of the daemon that makes or
 *  starts a container, which the provider bounds by nothing, at the fixtures' time for such a call
 *  (`makeMs`), and then the image's initialisation and its server's first answer, which it gives
 *  `readyMs` together. The binaries provider's: the template's `pg_ctl -w start` and `stop` and
 *  the stack's `pg_ctl -w start`, each at `pgCtlMs`, and the template's and the stack's first
 *  answer, each at `readyMs`. Its `initdb`, its `cp -a` of the template, the `psql`s of both and
 *  the image provider's reads of the container's log it bounds by nothing either, and no reading
 *  of them under load is held: they take the room above the part (`partOf`). */
function providerStart(variable: ProviderVariable): Waits {
  return variable === pgImageVariable
    ? { children: 0, reads: 0, makes: 2, bounds: [readyMs] }
    : { children: 0, reads: 0, bounds: [3 * pgCtlMs, 2 * readyMs] };
}

/** What a stop of one stack's Postgres waits for (its `stop`, e2e/harness-postgres.ts), for the
 *  provider `variable` names: the image provider's `docker rm` of its container, which it bounds
 *  by nothing, at the start's own bound for the removal of one container
 *  (`removalBounds.boundMs`), and the binaries provider's `pg_ctl -m immediate -w stop`, at
 *  `pgCtlMs`. */
function providerStop(variable: ProviderVariable): Waits {
  const ms = variable === pgImageVariable ? removalBounds.boundMs : pgCtlMs;
  return { children: 0, reads: 0, bounds: [ms] };
}

/** What a start's removal of what dead stacks left finds of a row's plants, which its waits are
 *  made of (`removalOf`): each container of a dead stack that it removes (`containers`), each mark
 *  of a dead stack in a run directory whose directories it claims (`marks`), and each part of
 *  those that another start claims at the same time, whose claim it waits for (`claims`). */
export interface Found {
  readonly containers?: number;
  readonly marks?: number;
  readonly claims?: number;
}

/** What a start's removal of what dead stacks left (`removeLeftoverPostgres` of
 *  e2e/harness-postgres-removal.ts) waits for, one wait after another, where it finds `found`:
 *  its `docker ps` of the marked containers, which it gives up at `dockerMs`; for each container,
 *  the removal, within `removalBounds.boundMs`, and the holder read before it removes the
 *  container's socket directory; for each mark, the holder read before its claim of each of the
 *  mark's two directories, and the stop of its postmaster, with the reads after it; and, for each
 *  part another start claims, its wait for that claim (`claimWaitMs`). What it finds of other runs
 *  on a Docker daemon or among the processes of a user that runs share is a wait no row counts:
 *  a CI job's daemon and runner are its own, and this lane's runs of the rows give each row a PID
 *  namespace and an owner label of its own. */
function removalOf({ containers = 0, marks = 0, claims = 0 }: Found): Waits {
  return {
    children: 0,
    reads: containers + 2 * marks,
    stops: marks,
    bounds: [dockerMs, containers * removalBounds.boundMs, claims * claimWaitMs],
  };
}

/** What the harness of a deployed run waits for as it starts (`startHarness` of
 *  e2e/harness-start.ts, with `PLAYWRIGHT_BASE_URL` set, as `poolScript` sets it): each listener
 *  a deployed run starts, one after another, each at its own bound (`bootMs` of
 *  e2e/harness-listeners.ts). */
const deployedHarness: Waits = {
  children: 0,
  reads: 0,
  bounds: harnessListeners.filter((listener) => listener.deployed).map(({ bootMs }) => bootMs),
};

/** A start of one stack in a child (`startStack`, `startNamespacedStack`): its `bun` start, and
 *  the provider's start of the stack (`providerStart`). */
export function stackStart(variable: ProviderVariable): Part {
  return partOf("a stack's start", inTurn(bunStart, providerStart(variable)));
}

/** A stop of one stack in a child (`stopStack`): the provider's stop (`providerStop`). */
export function stackStop(variable: ProviderVariable): Part {
  return partOf("a stack's stop", providerStop(variable));
}

/** A start of one stack in the row's own process (`startOwn`): the provider's start
 *  (`providerStart`). */
export function ownStart(variable: ProviderVariable): Part {
  return partOf("a stack's start in the row's own process", providerStart(variable));
}

/** A stop of one stack in the row's own process (`stopOwn`): the provider's stop
 *  (`providerStop`). */
export function ownStop(variable: ProviderVariable): Part {
  return partOf("a stack's stop in the row's own process", providerStop(variable));
}

/** A start of the pool that refuses, as `startScript` runs it (`startPool`): its `bun` start, and
 *  its removal of what dead stacks left, where it finds `found` (`removalOf`). */
export function poolStart(found: Found): Part {
  return partOf("a start of the pool", inTurn(bunStart, removalOf(found)));
}

/** A start of the pool that serves, as `poolScript` runs it (`startServingPool`): its `bun`
 *  start, its removal of what dead stacks left, where it finds `found` (`removalOf`), the
 *  provider's start of its one stack (`providerStart`), and its harness (`deployedHarness`). */
export function servingStart(variable: ProviderVariable, found: Found): Part {
  return partOf(
    "a start of the pool that serves",
    inTurn(bunStart, removalOf(found), providerStart(variable), deployedHarness)
  );
}

/** A stop of a start of the pool that serves (`stopPool`): its `stopStacks`, which stops the
 *  harness, signalling each process and killing what still runs 10 s later (`Supervisor.stop` of
 *  e2e/local-instance-support.ts), each end an event of another process (`claimEventMs`), and
 *  then the provider's stop of the stack (`providerStop`). */
export function poolStop(variable: ProviderVariable): Part {
  return partOf(
    "a stop of the pool",
    inTurn({ children: 0, reads: 0, bounds: [claimEventMs] }, providerStop(variable))
  );
}

/** A shell stack's start (`startShellStack`): its `postgres_mark`, a `bun` start, and its
 *  `docker run`, a call that makes a container (`makeMs`). */
export const shellStackStart = partOf("a shell stack's start", { ...bunStart, makes: 1 });

/** A shell's start of a stack as `scripts/e2e/lib/leftovers.sh` runs it (`shellSweep`): its
 *  `bun` start, and its removal of what dead stacks left, where it finds `found` (`removalOf`). */
export function shellSweepOf(found: Found): Part {
  return partOf("a shell's removal of what dead stacks left", inTurn(bunStart, removalOf(found)));
}

/** The end of a child that the row sent SIGKILL (`endOfKilled`): the kernel's, an event of
 *  another process (`claimEventMs`). */
export const killedEnd = partOf("the end of a child the row killed", {
  children: 0,
  reads: 0,
  bounds: [claimEventMs],
});

/** The wait of the image provider's row for a container it made itself to serve
 *  (`startUnjudged`): the image's initialisation and its server's first answer, as the provider
 *  gives its own container `readyMs` for them. */
export const ownContainerServes = partOf("a container of the row's own that serves", {
  children: 0,
  reads: 0,
  bounds: [readyMs],
});

/** The start of the process `pid`, as `startOf` reads it from `/proc`, or "" where the process is
 *  gone. */
export function startTimeOf(pid: number): string {
  try {
    return startOf(readFileSync(`/proc/${pid}/stat`, "utf8"));
  } catch {
    return "";
  }
}

/** A child this file started, with what it printed so far. */
interface Child {
  readonly process: Subprocess<"pipe", "pipe", "pipe">;
  readonly pid: number;
  /** Its start, read as it spawned, which a signal checks before it is sent. */
  readonly start: string;
  readonly output: { stdout: string; stderr: string };
  /** Settles once both pipes have ended and `output` holds all they carried. */
  readonly drained: Promise<unknown>;
}

/** Reads `stream` to its end, handing each decoded chunk to `into`. */
async function collect(stream: ReadableStream<Uint8Array>, into: (text: string) => void) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return;
    into(decoder.decode(value, { stream: true }));
  }
}

/** Starts `argv` with `env` in `dispatchPackage`, and collects what it prints. */
function launch(argv: readonly string[], env: Record<string, string | undefined>): Child {
  const child = Bun.spawn([...argv], {
    cwd: dispatchPackage,
    env,
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  });
  const output = { stdout: "", stderr: "" };
  const drained = Promise.all([
    collect(child.stdout, (text) => {
      output.stdout += text;
    }),
    collect(child.stderr, (text) => {
      output.stderr += text;
    }),
  ]);
  return { process: child, pid: child.pid, start: startTimeOf(child.pid), output, drained };
}

/** Starts `script` in a `bun` child with `env`. */
function spawn(script: string, env: Record<string, string | undefined>): Child {
  return launch(["bun", "--eval", script], env);
}

/** SIGKILL to `child`, a process this file started, only while `/proc` still names it: the pid
 *  with the start read as it spawned. */
export function kill(child: Child): void {
  if (child.start === "" || startTimeOf(child.pid) !== child.start) return;
  child.process.kill("SIGKILL");
}

/** One wait of this process's rows (`during`): what it waits for, and when it began and when it
 *  ended on the machine's clock (`machine`), `ended` unset while it runs. */
interface Wait {
  readonly what: string;
  readonly began: number;
  ended?: number;
}

/** Every wait of this process's rows so far, in the order they began (`during`). The rows of one
 *  file run one after another, so the waits that ran at a row's deadline are that row's own. */
const waits: Wait[] = [];

/** Waits for `wait()`, which its row names `what`, and records when it began and when it ended
 *  (`waits`), so that the line of a fixture call that the row's deadline finds already passed
 *  names the wait that used that deadline up (`ranOut`). Each wait in this file of a child, of
 *  Docker or of Postgres is one; a row names its other waits so, such as a provider's start of a
 *  stack that the row awaits itself. */
export async function during<T>(what: string, wait: () => Promise<T>): Promise<T> {
  const record: Wait = { what, began: machine.now() };
  waits.push(record);
  try {
    return await wait();
  } finally {
    record.ended = machine.now();
  }
}

/** A part of a row's waits that the row was charged for (`Row.part`): the row, and when the wait
 *  must end by where the row's CI step cut the row's limit, its deadline or, in its `finally`, the
 *  end of its stops. */
interface Charged {
  readonly row: Row;
  readonly by: number;
}

/** Charges `row` for `part` (`Row.part`), before the wait for it. */
function charge(row: Row, part: Part): Charged {
  return { row, by: row.part(part) };
}

/** Waits for `child` to exit, and kills it and throws, naming `what`, where it has not within the
 *  bound of `part`, the part of its row's waits it is, which the caller charged its row for, or by
 *  the time that charge answered where the row's CI step cut its limit (`boundOf`). */
function exited(child: Child, part: Part, what: string, { row, by }: Charged): Promise<number> {
  return during(what, async () => {
    const { ms, named } = boundOf(row, part.ms, by);
    const code = await Promise.race([child.process.exited, sleep(ms).then(() => undefined)]);
    if (code !== undefined) return code;
    kill(child);
    throw new Error(`${what} did not end ${named}: ${child.output.stderr}`);
  });
}

/** Waits for the end of `child`, which the row `row` sent SIGKILL, as a part of the row's waits
 *  (`killedEnd`), and answers its exit status; throws, naming `what`, where it has not ended. */
export function endOfKilled(row: Row, child: Child, what: string): Promise<number> {
  return exited(child, killedEnd, what, charge(row, killedEnd));
}

/** Waits for the first line `child` prints on its stdout, and answers it. Throws, naming `what`
 *  and what the child wrote on stderr, where the child exits before it printed one, and kills the
 *  child and throws where it has printed none within the bound of `part`, the part of its row's
 *  waits it is, which the caller charged its row for, or by the time that charge answered where
 *  the row's CI step cut its limit (`boundOf`). */
function firstLine(child: Child, part: Part, what: string, { row, by }: Charged): Promise<string> {
  return during(what, async () => {
    const { ms, named } = boundOf(row, part.ms, by);
    const deadline = Date.now() + ms;
    while (!child.output.stdout.includes("\n")) {
      const code = await Promise.race([child.process.exited, sleep(200).then(() => undefined)]);
      if (code !== undefined && !child.output.stdout.includes("\n")) {
        await Promise.race([child.drained, sleep(10_000)]);
        throw new Error(`${what} exited ${code} before it served: ${child.output.stderr}`);
      }
      if (Date.now() > deadline) {
        kill(child);
        throw new Error(`${what} did not serve ${named}: ${child.output.stderr}`);
      }
    }
    return child.output.stdout.split("\n")[0] ?? "";
  });
}

/** The environment of a child of a row: this one's, with the row's own `TMPDIR` and the one
 *  provider the row names. */
export function childEnv(tmp: string, provider: { variable: ProviderVariable; value: string }) {
  const env: Record<string, string | undefined> = { ...process.env, TMPDIR: tmp };
  delete env[pgBinVariable];
  delete env[pgImageVariable];
  env[provider.variable] = provider.value;
  env[pgConnectionsVariable] = process.env[pgConnectionsVariable] ?? "100";
  return env;
}

/** One stack's Postgres as its child printed it. */
export interface Stack {
  readonly child: Child;
  readonly dir: string;
  readonly url: string;
  readonly address: string;
  /** The PID namespace the stack runs in, as `/proc/self/ns/pid` names it. */
  readonly pidNamespace: string;
}

/** Starts a stack in a child with `env`, the provider `variable` names and the run name `run`,
 *  for the row `row`, whose limit counts it (`stackStart`), and answers once it serves. */
export async function startStack(
  row: Row,
  env: Record<string, string | undefined>,
  run: string,
  variable: ProviderVariable
): Promise<Stack> {
  const part = stackStart(variable);
  const charged = charge(row, part);
  const child = spawn(stackScript, { ...env, LEFTOVER_RUN: run });
  const printed = JSON.parse(await firstLine(child, part, `stack ${run}`, charged)) as Omit<
    Stack,
    "child"
  >;
  return { child, ...printed };
}

/** Stops `stack`, of the provider `variable` names, the way its own child does, by closing its
 *  stdin, and where that did not end it within its bound (`stackStop`), kills it. The stop begins
 *  before the row `row` is charged for it, so a limit that does not count it leaves no stack
 *  running when its charge fails the row. */
export async function stopStack(
  row: Row,
  stack: Stack | undefined,
  variable: ProviderVariable
): Promise<void> {
  if (stack === undefined) return;
  const part = stackStop(variable);
  stack.child.process.stdin.end();
  const charged = charge(row, part);
  await exited(stack.child, part, `the stack in ${stack.dir}`, charged).catch(() => {});
}

/** Runs the pool's start in a child with `env`, for the row `row`, whose limit counts it as a
 *  start that finds `found` (`poolStart`), and answers its pid and what it printed. */
export async function startPool(
  row: Row,
  env: Record<string, string | undefined>,
  found: Found
): Promise<{ readonly pid: number; readonly stdout: string; readonly stderr: string }> {
  const part = poolStart(found);
  const charged = charge(row, part);
  const child = spawn(startScript, env);
  child.process.stdin.end();
  const code = await exited(child, part, "a start of the pool", charged);
  // The pipes end as it exits; their last chunks may still be on the way to `output`.
  await Promise.race([child.drained, sleep(10_000)]);
  if (code !== 0) throw new Error(`a start of the pool exited ${code}: ${child.output.stderr}`);
  return { pid: child.pid, ...child.output };
}

/** A whole start of the pool (`poolScript`) that serves, with what it printed. */
export interface PoolStart {
  readonly child: Child;
  readonly url: string;
}

/** Starts the pool in a child with `env` and the provider `variable` names, for the row `row`,
 *  whose limit counts it as a start that finds `found` (`servingStart`), and answers once its
 *  stack serves. */
export async function startServingPool(
  row: Row,
  env: Record<string, string | undefined>,
  variable: ProviderVariable,
  found: Found
): Promise<PoolStart> {
  const own: Record<string, string | undefined> = {
    ...env,
    PLAYWRIGHT_BASE_URL: "http://127.0.0.1:9",
  };
  // A run of one stack against a database the caller named starts no Postgres.
  delete own.DATABASE_URL;
  const part = servingStart(variable, found);
  const charged = charge(row, part);
  const child = spawn(poolScript, own);
  const line = await firstLine(child, part, "a start of the pool", charged);
  const { url } = JSON.parse(line) as { url?: string };
  if (url === undefined) throw new Error(`a start of the pool named no database: ${line}`);
  return { child, url };
}

/** Stops a start of the pool, of the provider `variable` names, by closing its stdin, which runs
 *  its `stopStacks`, and kills it where that did not end it within its bound (`poolStop`). The
 *  stop begins before the row `row` is charged for it, as a stack's does (`stopStack`). */
export async function stopPool(
  row: Row,
  start: PoolStart | undefined,
  variable: ProviderVariable
): Promise<void> {
  if (start === undefined) return;
  const part = poolStop(variable);
  start.child.process.stdin.end();
  const charged = charge(row, part);
  await exited(start.child, part, "a stop of the pool", charged).catch(() => {});
}

/** Runs a shell's start of a stack as `scripts/e2e/lib/leftovers.sh`'s callers run it, its
 *  `remove_leftover_postgres`, in a child with `env`, for the row `row`, whose limit counts it as a
 *  removal that finds `found` (`shellSweepOf`), and answers its exit status and its stderr. */
export async function shellSweep(
  row: Row,
  env: Record<string, string | undefined>,
  found: Found
): Promise<{ readonly status: number; readonly stderr: string }> {
  const part = shellSweepOf(found);
  const charged = charge(row, part);
  const child = launch(["bash", "-c", `. "${leftoversLib}"; remove_leftover_postgres`], env);
  child.process.stdin.end();
  const status = await exited(child, part, "a shell's start", charged);
  await Promise.race([child.drained, sleep(10_000)]);
  return { status, stderr: child.output.stderr };
}

/** Starts stack `index` of `provider`, of the provider `variable` names, in this process, for the
 *  row `row`, whose limit counts it (`ownStart`), as a wait of the row's named `what` (`during`).
 *  The provider bounds its own start, so this sets none; but where the row's CI step cut its limit
 *  (`boundOf`), the row gives the start up at its deadline and throws, naming it, and stops the
 *  stack once that start serves after all. */
export async function startOwn(
  row: Row,
  provider: StackPostgres,
  variable: ProviderVariable,
  index: number,
  what: string
): Promise<StackDatabase> {
  const { by } = charge(row, ownStart(variable));
  const { ms, named } = boundOf(row, Number.POSITIVE_INFINITY, by);
  const starting = provider.start(index);
  if (!Number.isFinite(ms)) return during(what, () => starting);
  return during(what, () =>
    Promise.race([
      starting,
      sleep(ms).then(() => {
        void starting.then((database) => database.stop()).catch(() => {});
        throw new Error(`${what} did not end ${named}`);
      }),
    ])
  );
}

/** Stops `database`, which a start in this process of the provider `variable` names served
 *  (`startOwn`), for the row `row`, whose limit counts it (`ownStop`). The stop begins before the
 *  row is charged for it, as a stack's does in a child (`stopStack`). The provider bounds its own
 *  stop; where the row's CI step cut its limit (`boundOf`), the row stops waiting for it at the
 *  end of its stops and throws, naming it, and the stop goes on. */
export async function stopOwn(
  row: Row,
  database: StackDatabase,
  variable: ProviderVariable
): Promise<void> {
  const what = "the stop of the row's own stack";
  const stopping = during(what, () => database.stop());
  // Handled here as well, so a stop that fails after a refused charge is no unhandled rejection.
  stopping.catch(() => {});
  const { by } = charge(row, ownStop(variable));
  const { ms, named } = boundOf(row, Number.POSITIVE_INFINITY, by);
  if (!Number.isFinite(ms)) return stopping;
  const late = sleep(ms).then(() => "late" as const);
  if ((await Promise.race([stopping, late])) === "late") {
    throw new Error(`${what} did not end ${named}`);
  }
}

/** Why this machine refuses a stack a PID namespace of its own, or undefined where it gives one:
 *  that takes a privilege this file borrows with `sudo -n` (`unshare --pid`), since an
 *  unprivileged user namespace is not allowed everywhere. A refusal is one line on stderr, which
 *  names `file` and says that its two rows of such a stack skip. */
export function pidNamespaceRefusal(file: string): string | undefined {
  const probe = spawnSync("sudo", ["-n", "unshare", "--pid", "--fork", "--mount-proc", "true"], {
    encoding: "utf8",
    timeout: 30_000,
  });
  if (probe.status === 0) return undefined;
  const why = (probe.stderr || probe.error?.message || `exit ${probe.status}`)
    .trim()
    .split("\n")[0];
  console.error(
    `${file}: skips the two rows of a stack in a PID namespace of its own: this machine refuses \`sudo -n unshare --pid\` (${why}), the permission a PID namespace of a stack's own takes here`
  );
  return why;
}

/** Starts a stack as `startStack` does, for the row `row`, with `env` and the run name `run`, in a
 *  PID namespace of its own (`unshare --pid`), which it enters through `sudo -n` and leaves for
 *  this user again (`setpriv`). The child is `sudo`'s. Its provider is the binaries provider: the
 *  rows of such a stack are its. */
export async function startNamespacedStack(
  row: Row,
  env: Record<string, string | undefined>,
  run: string
): Promise<Stack> {
  const part = stackStart(pgBinVariable);
  const charged = charge(row, part);
  // `sudo` resets the environment, so the stack's goes through `env -i`.
  const pairs = Object.entries(env)
    .filter((pair): pair is [string, string] => pair[1] !== undefined)
    .map(([key, value]) => `${key}=${value}`);
  const child = launch(
    [
      "sudo",
      "-n",
      "unshare",
      "--pid",
      "--fork",
      "--mount-proc",
      "setpriv",
      `--reuid=${process.getuid?.() ?? -1}`,
      `--regid=${process.getgid?.() ?? -1}`,
      "--init-groups",
      "--",
      "env",
      "-i",
      ...pairs,
      `LEFTOVER_RUN=${run}`,
      process.execPath,
      "--eval",
      stackScript,
    ],
    process.env
  );
  return {
    child,
    ...(JSON.parse(await firstLine(child, part, `stack ${run}`, charged)) as Omit<Stack, "child">),
  };
}

/** How one call of a row's fixtures of Docker or Postgres ended (`untilDeadline`): its exit status
 *  and what it printed. */
interface Called {
  readonly status: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** How a fixture call that its row's time did not let end ended (`untilDeadline`): "late" where
 *  that time ran out while the call ran, which stopped it, and "unstarted" where it had run out
 *  before the call, which then started nothing. */
type Unended = "late" | "unstarted";

/** Runs `program` with `args`, a call of Docker or Postgres that sets up the row `row`, or reads
 *  it, for as long as the row still has time for: until `by`, the row's deadline but for a removal
 *  of what it made (`Row.cleanup`), on the machine's clock, as a start's removal runs its `docker rm`
 *  (`machine` of `e2e/harness-postgres-container-removal.ts`). It sets no time of its own: a shared
 *  daemon took 133 s over one `docker create` on 2026-11-19 while it answered every other call, and
 *  a fixed bound of 120 s gave that call up, and left the container it made. Where `by` has passed
 *  already, it starts nothing: a call with no time at all is no call that ran out its time. The
 *  call is a wait of the row (`during`). Answers how the call ended (`Called`), or how `by` ended
 *  it (`Unended`). */
async function untilDeadline(
  row: Row,
  program: string,
  args: readonly string[],
  by = row.deadline
): Promise<Called | Unended> {
  if (machine.now() >= by) return "unstarted";
  return during(`${program} ${args.join(" ")}`, async () => {
    const stop = new AbortController();
    const ended = new AbortController();
    void machine.sleep(Math.max(0, by - machine.now()), ended.signal).then(() => {
      if (!ended.signal.aborted) stop.abort();
    });
    try {
      const { stdout, stderr } = await execFileAsync(program, args, {
        signal: stop.signal,
        encoding: "utf8",
        maxBuffer: 64 * 1024 * 1024,
      });
      return { status: 0, stdout, stderr };
    } catch (error) {
      if (stop.signal.aborted) return "late";
      const failed = error as { code?: unknown; stdout?: unknown; stderr?: unknown };
      const stderr = typeof failed.stderr === "string" ? failed.stderr : "";
      return {
        status: typeof failed.code === "number" ? failed.code : 1,
        stdout: typeof failed.stdout === "string" ? failed.stdout : "",
        stderr: stderr === "" && error instanceof Error ? error.message : stderr,
      };
    } finally {
      ended.abort();
    }
  });
}

/** `ms` in seconds, to a tenth, as the lines of this file give a time. */
function seconds(ms: number): string {
  return (ms / 1000).toFixed(1);
}

/** What used up the time of the row `row` that ended at `by` (`waits`): the wait of the row that
 *  ran at `by`, the one of them that ended last where several did, with when it began and ended
 *  from the row's start; or, where none ran then, the last that ended before it, after which the
 *  time went in a wait the row does not name. */
function usedUp(row: Row, by: number): string {
  const start = row.deadline - row.ms;
  const mine = waits.filter((wait) => wait.began >= start);
  const endOf = (wait: Wait) => wait.ended ?? Number.POSITIVE_INFINITY;
  const ran = mine
    .filter((wait) => wait.began <= by && endOf(wait) > by)
    .sort((a, b) => endOf(b) - endOf(a))[0];
  if (ran !== undefined) {
    const to = ran.ended === undefined ? "on" : `to ${seconds(ran.ended - start)} s`;
    return `during ${ran.what}, from ${seconds(ran.began - start)} s ${to} of the row`;
  }
  const before = mine.filter((wait) => endOf(wait) <= by).at(-1);
  return before?.ended === undefined
    ? "in a wait the row does not name"
    : `after ${before.what}, which ended at ${seconds(before.ended - start)} s of the row, in a wait the row does not name`;
}

/** The one line, and the error, of a fixture call `call` of `row` that its time did not let end
 *  (`Unended`), where that time ended at `by`: the row's deadline, or its time for its removals
 *  (`Row.cleanup`). A call that ran out that time names the call, the row's time, what set it
 *  where the row's CI step cut it (`Row.cut`), and what the removal of what it made did
 *  (`removed`); a call that the time found already passed names the call, how long before it the
 *  time had run out, and the wait that used it up (`usedUp`). */
function ranOut(row: Row, call: string, how: Unended, by = row.deadline, removed = ""): Error {
  const waits = `the ${row.ms / 1000} s the row waits in all${row.cut === undefined ? "" : `, which ${row.cut} set`}`;
  const time = by === row.deadline ? waits : "the row's time for its removals";
  const line =
    how === "late"
      ? `${named}: ${call} did not end within ${waits}, so the row stopped it${removed}`
      : `${named}: ${call} did not start: ${time} had run out ${seconds(machine.now() - by)} s before it, ${usedUp(row, by)}`;
  console.error(line);
  return new Error(line);
}

/** `psql` of `url` with the one statement `sql`, read within the time `row` has left
 *  (`untilDeadline`): answers how it ended, and throws naming the read where the row's deadline
 *  comes first. */
export async function psql(row: Row, url: string, sql: string): Promise<Called> {
  const read = await untilDeadline(row, "psql", [url, "-XAtc", sql]);
  if (typeof read === "string") throw ranOut(row, `psql ${url} -XAtc '${sql}'`, read);
  return read;
}

/** Whether the Postgres at `url` answers `select 1`, read within the time `row` has left
 *  (`psql`). */
export async function answers(row: Row, url: string): Promise<boolean> {
  const read = await psql(row, url, "select 1");
  return read.status === 0 && read.stdout.trim() === "1";
}

/** Each postmaster that serves a data directory of the binaries provider's run directory `dir`,
 *  by the pid its `postmaster.pid` names and the start `/proc` gives that pid now. */
export function postmastersOf(dir: string): { pid: number; start: string }[] {
  return ["worker-0", "template"].flatMap((stack) => {
    let text: string;
    try {
      text = readFileSync(join(dir, stack, "pgdata", "postmaster.pid"), "utf8");
    } catch {
      return [];
    }
    const pid = Number.parseInt(text, 10);
    const start = startTimeOf(pid);
    return start === "" ? [] : [{ pid, start }];
  });
}

/** Removes what the `docker` call of `args` made, which its row stopped at the row's deadline
 *  (`docker`), by the row's time for its removals (`Row.cleanup`): the container a `create` or a
 *  `run` names (`--name`), as Docker lists it by that name and by its `--label`'s key, each by its
 *  id (`removeContainer`), or the image a `commit` names, its last argument. Answers what it did,
 *  for the call's line. */
async function removeMade(row: Row, args: readonly string[]): Promise<string> {
  const by = row.cleanup;
  const left = () => Math.max(0, by - machine.now());
  if (args[0] === "commit") {
    const image = args.at(-1) ?? "";
    const removal = await untilDeadline(row, "docker", ["image", "rm", "--force", image], by);
    if (typeof removal === "string") {
      return `, and the removal of the image ${image} it made did not ${removal === "late" ? "end" : "start"}`;
    }
    return removal.status === 0
      ? `, and removed the image ${image} it made`
      : `, and left the image ${image} it made: ${removal.stderr.trim()}`;
  }
  const name = args[args.indexOf("--name") + 1];
  if ((args[0] !== "create" && args[0] !== "run") || !args.includes("--name") || !name) return "";
  const filters = ["--filter", `name=^/?${name}$`];
  const label = args.includes("--label") ? args[args.indexOf("--label") + 1]?.split("=")[0] : "";
  if (label) filters.push("--filter", `label=${label}`);
  const listed = await untilDeadline(
    row,
    "docker",
    ["ps", "--all", "--quiet", "--no-trunc", ...filters],
    by
  );
  if (typeof listed === "string" || listed.status !== 0) {
    return `, and could not list the container ${name} it may have made`;
  }
  const ids = listed.stdout.split("\n").filter(Boolean);
  if (ids.length === 0) return `, and it had made no container ${name}`;
  const said: string[] = [];
  for (const id of ids) {
    const removal = await removeContainer(id, `container ${id.slice(0, 12)} (${name})`, {
      answerMs: left(),
      boundMs: left(),
    });
    said.push(
      removal.kind === "failed"
        ? `left container ${id.slice(0, 12)} (${name}) it made: ${removal.reason}`
        : `removed container ${id.slice(0, 12)} (${name}) it made`
    );
  }
  return `, and ${said.join(", and ")}`;
}

/** `docker` with `args`, as a call that sets up or reads the row `row` runs it (`untilDeadline`):
 *  answers what it printed, and throws where it fails, with Docker's reason. A call that makes a
 *  container or an image (`create`, `run`, `commit`) charges the row for it (`Row.made`), whose
 *  limit counts it. Where the row's deadline comes first, the row stops the call, removes what it
 *  made (`removeMade`), and then throws, naming the call and that removal on one line. */
export async function docker(row: Row, ...args: string[]): Promise<string> {
  const call = `docker ${args.join(" ")}`;
  if (args[0] === "create" || args[0] === "run" || args[0] === "commit") row.made(call);
  const ended = await untilDeadline(row, "docker", args);
  // A call that started nothing made nothing.
  if (typeof ended === "string") {
    const removed = ended === "late" ? await removeMade(row, args) : "";
    throw ranOut(row, call, ended, row.deadline, removed);
  }
  if (ended.status !== 0) throw new Error(`${call}: ${ended.stderr}`);
  return ended.stdout;
}

/** Whether Docker has the object `name` of `kind`, read with `docker <kind> inspect` within the
 *  time `row` has left (`untilDeadline`): false only where Docker says it has no such object, and
 *  a throw where the read fails otherwise or the row's deadline comes first, so a row never reads
 *  a failed read as a removal. */
export async function exists(
  row: Row,
  kind: "container" | "volume",
  name: string
): Promise<boolean> {
  const call = `docker ${kind} inspect ${name}`;
  const read = await untilDeadline(row, "docker", [kind, "inspect", name]);
  if (typeof read === "string") throw ranOut(row, call, read);
  if (read.status === 0) return true;
  if (/no such (container|volume|object)/i.test(read.stderr)) return false;
  throw new Error(`${call}: ${read.stderr}`);
}

/** Whether a container of this name exists, running or not (`exists`). */
export function containerExists(row: Row, name: string): Promise<boolean> {
  return exists(row, "container", name);
}

/** The volumes of the container `name`: the image's data volume, which holds its stack's data. */
export async function volumesOf(row: Row, name: string): Promise<string[]> {
  const format = '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}';
  return (await docker(row, "container", "inspect", "--format", format, name))
    .trim()
    .split(/\s+/)
    .filter(Boolean);
}

/** The ids of the containers whose mark names `run`, the run directory of a stack this row
 *  started, as Docker lists them within the time `row` has left, until `by` (`untilDeadline`). A
 *  listing that fails, or that the row's deadline stops, throws, so no row reads it as one that
 *  lists none. */
export async function containersOf(row: Row, run: string, by = row.deadline): Promise<string[]> {
  const args = [
    "ps",
    "--all",
    "--no-trunc",
    "--filter",
    `label=${markLabel}`,
    "--format",
    `{{.ID}} {{.Label "${markLabel}"}}`,
  ];
  const listed = await untilDeadline(row, "docker", args, by);
  if (typeof listed === "string") throw ranOut(row, `docker ${args.join(" ")}`, listed, by);
  if (listed.status !== 0) throw new Error(`docker ${args.join(" ")}: ${listed.stderr}`);
  return listed.stdout.split("\n").flatMap((line) => {
    const space = line.indexOf(" ");
    if (space === -1) return [];
    let mark: unknown;
    try {
      mark = JSON.parse(line.slice(space + 1));
    } catch {
      return [];
    }
    const ofRun = typeof mark === "object" && mark !== null && "run" in mark && mark.run === run;
    return ofRun ? [line.slice(0, space)] : [];
  });
}

/** Removes each container of `ids`, by its id, all at once, each as a start removes one
 *  (`removeContainer`): however long Docker takes over it while it answers each ask, within the
 *  start's own bound, which the row's limit counts once among the waits of its `finally`
 *  (`Waits.ends`). Once each has ended, throws naming each removal that Docker gave up or
 *  refused, with why: one failed removal keeps no other container. A container that is gone
 *  already is no failure, nor is one that another start of the machine removed first. A daemon
 *  that many sessions shared took 17 and 21 minutes over one removal on 2026-11-19, which a
 *  removal with no bound of its own carried past the row's limit. */
export async function removeIds(ids: readonly string[]): Promise<void> {
  const removals = await Promise.all(
    ids.map((id) => {
      const what = `container ${id.slice(0, 12)}`;
      return during(`the removal of ${what}`, () => removeContainer(id, what));
    })
  );
  const failed = removals.flatMap((removal, index) =>
    removal.kind === "failed" ? [`${ids[index]}: ${removal.reason}`] : []
  );
  if (failed.length > 0) {
    throw new Error(`${named}: what the row made is not removed: ${failed.join("; ")}`);
  }
}

/** Removes the image `name`, which a row made, within the start's own bound for the removal of a
 *  container, which the row's limit counts among the waits of its `finally` (`Waits.ends`): Docker
 *  gives a removal no bound of its own. */
export async function removeImage(name: string): Promise<void> {
  const args = ["image", "rm", "--force", name];
  await during(`docker ${args.join(" ")}`, () =>
    machine.docker(args, { signal: new AbortController().signal, timeout: removalBounds.boundMs })
  ).catch((error: unknown) => {
    throw new Error(
      `docker ${args.join(" ")}: ${error instanceof Error ? error.message : String(error)}`
    );
  });
}

/** Removes the container `id`, which a row made and its line names `what`, as a start removes one
 *  (`removeContainer`): however long Docker takes over it while it answers each ask, within the
 *  start's own bound, which the row's limit counts among the waits of its `finally`
 *  (`Waits.ends`). Throws, naming the container and why, where the removal gives it up. */
export async function removeWithin(id: string, what: string): Promise<void> {
  const removal = await during(`the removal of ${what}`, () => removeContainer(id, what));
  if (removal.kind === "failed") {
    throw new Error(`${named}: ${what} is not removed: ${removal.reason}`);
  }
}

/** Removes each container whose mark names `run`, the run directory of a stack the row `row`
 *  started, by the id Docker lists it under, listed by the row's time for its removals
 *  (`Row.cleanup`): a container's name is no one stack's alone. */
export async function removeContainersOf(row: Row, run: string): Promise<void> {
  await removeIds(await containersOf(row, run, row.cleanup));
}

/** Stops the postmaster of the cluster in `data` with `pg_ctl` of `bin`, as a stack's stop does
 *  (`-m immediate -w`), by the row's time for its removals (`Row.cleanup`): `pg_ctl`'s own
 *  wait for the stop, `PGCTLTIMEOUT`, bounds it, and no fixed time of this file's. Throws naming
 *  the stop where that room runs out first. A stop that fails is a postmaster that is gone. */
export async function stopCluster(row: Row, bin: string, data: string): Promise<void> {
  const args = ["-D", data, "-m", "immediate", "-w", "stop"];
  const stopped = await untilDeadline(row, join(bin, "pg_ctl"), args, row.cleanup);
  if (typeof stopped === "string") {
    throw ranOut(row, `${join(bin, "pg_ctl")} ${args.join(" ")}`, stopped, row.cleanup);
  }
}

/** Removes what a stack the row `row` started left, by the paths it recorded for it: its
 *  container, by the id its mark is listed under, or its postmasters, then its directories. */
export async function removeLeft(
  row: Row,
  stack: Stack | undefined,
  provider: { variable: ProviderVariable; value: string }
): Promise<void> {
  if (stack === undefined) return;
  if (provider.variable === pgImageVariable) {
    await removeContainersOf(row, stack.dir);
    rmSync(dirname(stack.address), { recursive: true, force: true });
  } else {
    for (const data of [
      join(stack.dir, "worker-0", "pgdata"),
      join(stack.dir, "template", "pgdata"),
    ]) {
      if (existsSync(join(data, "postmaster.pid"))) await stopCluster(row, provider.value, data);
    }
    rmSync(stack.address, { recursive: true, force: true });
  }
}

/** What `removeLeft` waits for, in ms, for a stack of the provider `variable` names, which a row's
 *  limit counts among the waits of its `finally` (`Waits.ends`): the removal of the stack's
 *  containers at once, each within the start's own bound (`removeIds`), or the stops of its two
 *  postmasters, each within `pgCtlMs` (`stopCluster`). */
export function leftRemovalMs(variable: ProviderVariable): number {
  return variable === pgImageVariable ? removalBounds.boundMs : 2 * pgCtlMs;
}

/** Runs each removal of a row's `finally`, every one even where another fails, and throws naming
 *  each that failed, with what it said. */
export async function removeAll(...removals: (() => unknown)[]): Promise<void> {
  const failed: string[] = [];
  for (const removal of removals) {
    try {
      await removal();
    } catch (error) {
      failed.push(error instanceof Error ? error.message : String(error));
    }
  }
  if (failed.length > 0) throw new Error(`the row's removals failed: ${failed.join("; ")}`);
}

/** The owners a row writes into marks, each built from this process: this process, which runs;
 *  its pid with another start, which is a process that took over the pid of a gone owner; another
 *  boot; another PID namespace; another time namespace; and none, which is a stack's where there
 *  is no /proc. */
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

/** A Postgres of the row's own that serves under a mark the start cannot judge, of another boot,
 *  with the name a start's line names it by and its own stop. */
export interface Unjudged {
  readonly url: string;
  readonly named: string;
  stop(): Promise<void> | void;
}

/** Starts a Postgres that serves under a mark of another boot, for the row `row`. The binaries
 *  provider's is a stack started as `startStack` starts one, whose mark is then written again with
 *  another boot's owner; the start names its mark file. The image provider's is a container of the
 *  row's own, made as the provider makes one, whose label is such a mark from its create on, in a
 *  socket directory of the run directory `run`; the start names it by its id. Its calls of Docker
 *  and Postgres take the time the row has left (`docker`), and its wait for the container to serve
 *  is a part of the row's waits (`ownContainerServes`). */
export async function startUnjudged(
  row: Row,
  env: Record<string, string | undefined>,
  provider: { variable: ProviderVariable; value: string },
  run: string,
  beside: string
): Promise<Unjudged> {
  if (provider.variable === pgBinVariable) {
    const stack = await startStack(row, env, run, pgBinVariable);
    const markFile = join(stack.dir, marksDirectory, "worker-0.json");
    const mark = JSON.parse(readFileSync(markFile, "utf8")) as Record<string, unknown>;
    writeFileSync(markFile, JSON.stringify({ ...mark, owner: owners().otherBoot }));
    return {
      url: stack.url,
      named: markFile,
      stop: async () => {
        await stopStack(row, stack, pgBinVariable);
        await removeLeft(row, stack, provider);
      },
    };
  }
  const { by } = charge(row, ownContainerServes);
  // In the run directory, or in /tmp where a long TMPDIR leaves no room there for the socket's
  // path, as the provider puts each of its own.
  const own = socketDirectory(beside, "pg-9", join("s", socketFile));
  const sockets = join(own, "s");
  mkdirSync(sockets, { mode: 0o700 });
  const name = `dispatch-e2e-pg-${run}-9`;
  const mark = {
    owner: owners().otherBoot,
    run: beside,
    identity: identityOf(beside),
    sockets: own,
  };
  let id: string;
  try {
    id = (
      await docker(
        row,
        "run",
        "--detach",
        "--name",
        name,
        "--label",
        `${markLabel}=${JSON.stringify({ ...mark, container: name })}`,
        "--user",
        `${process.getuid?.()}:${process.getgid?.()}`,
        "--mount",
        `type=bind,source=${sockets},target=/var/run/postgresql`,
        "--env",
        "POSTGRES_HOST_AUTH_METHOD=trust",
        provider.value,
        "-c",
        "listen_addresses="
      )
    ).trim();
  } catch (error) {
    // The socket directory is in /tmp where the row's TMPDIR is long, so the row's own removal of
    // its TMPDIR does not reach it.
    rmSync(own, { recursive: true, force: true });
    throw error;
  }
  const url = `postgres://postgres@/postgres?host=${encodeURIComponent(sockets)}&sslmode=disable`;
  // The image's first server is its initialisation's, which it stops before its last one starts.
  const initialised = async () => {
    const logs = await untilDeadline(row, "docker", ["logs", id]);
    if (typeof logs === "string") throw ranOut(row, `docker logs ${id}`, logs);
    return `${logs.stdout}${logs.stderr}`.includes("init process complete");
  };
  // The container by the id Docker made it under, and then its socket directory, which is in /tmp
  // and not in the row's TMPDIR where that TMPDIR is long.
  const stop = async () => {
    await removeAll(
      () => removeIds([id]),
      () => rmSync(own, { recursive: true, force: true })
    );
  };
  const serves = boundOf(row, ownContainerServes.ms, by);
  const deadline = Date.now() + serves.ms;
  try {
    while (!((await initialised()) && (await answers(row, url)))) {
      if (Date.now() > deadline) {
        throw new Error(`the container ${name} did not serve ${serves.named}`);
      }
      await sleep(500);
    }
  } catch (error) {
    // A read the row's deadline stopped leaves the container this row made too.
    await stop();
    throw error;
  }
  return { url, named: `container ${id.slice(0, 12)}`, stop };
}

/** What a binaries provider's stack left in `run`, as its mark names it: a data directory and a
 *  socket directory, each holding a file, and the mark. No server serves from it, so a start that
 *  judges its owner gone removes the directories and the mark alone. `text` replaces the mark
 *  where a row writes one that is no mark. */
export function leaveCluster(
  run: string,
  index: number,
  bin: string,
  owner: Owner | null,
  options: { sockets?: string; text?: string } = {}
) {
  const data = join(run, `worker-${index}`, "pgdata");
  mkdirSync(data, { recursive: true });
  writeFileSync(join(data, "PG_VERSION"), "18\n");
  const sockets = options.sockets ?? mkdtempSync(join(run, `pg-${index}-`));
  writeFileSync(join(sockets, "socket"), "");
  mkdirSync(join(run, marksDirectory), { recursive: true });
  const mark = join(run, marksDirectory, `worker-${index}.json`);
  writeFileSync(
    mark,
    options.text ??
      JSON.stringify({ owner, run, identity: identityOf(run), sockets, postmaster: { bin, data } })
  );
  return { data, sockets, mark, left: () => [data, sockets, mark].map(existsSync) };
}

/** The name of stack `index`'s container in the run directory `run` a row made: of this run
 *  (`runTag`) and of that directory, so no other row and no other run holds it. */
export function plantedName(run: string, index: number): string {
  return `dispatch-e2e-pg-${basename(run).slice(runDirectoryPrefix.length)}-${runTag}-${index}`;
}

/** A container an image provider's stack left, made and never started, under a name of this
 *  row's own (`plantedName`): a start judges it by its label alone, which is its mark unless
 *  `label` names another. `made` collects the id Docker made it under, for the row's removal. Its
 *  `docker create` takes the time the row `row` has left, which counts it (`docker`). */
export async function leaveContainer(
  row: Row,
  image: string,
  made: string[],
  run: string,
  index: number,
  owner: Owner | null,
  sockets: string,
  label?: string
) {
  const name = plantedName(run, index);
  const mark =
    label ?? JSON.stringify({ owner, run, identity: identityOf(run), sockets, container: name });
  const id = (
    await docker(row, "create", "--name", name, "--label", `${markLabel}=${mark}`, image)
  ).trim();
  made.push(id);
  return { name, id: id.slice(0, 12), sockets };
}

/** The mark file in `run` of stack `index`'s container `container`, whose socket directory is
 *  `sockets`, as the image provider writes it beside the container's label. Answers its path. */
export function leaveContainerMark(
  run: string,
  index: number,
  owner: Owner | null,
  sockets: string,
  container: string
): string {
  mkdirSync(join(run, marksDirectory), { recursive: true });
  const mark = join(run, marksDirectory, `worker-${index}.json`);
  writeFileSync(
    mark,
    JSON.stringify({ owner, run, identity: identityOf(run), sockets, container })
  );
  return mark;
}

/** A container an image provider's stack left, as `leaveContainer` makes one, but with no label
 *  that Docker lists: its mark is a line of `docker ps`'s listing (`listing`) that the stand-in
 *  `docker` of `soleListing` gives the starts under test alone, so no other start of the machine
 *  removes it first. `made` collects the id Docker made it under, for the row's removal. Its
 *  `docker create` takes the time the row `row` has left, which counts it (`docker`). */
export async function leaveUnlistedContainer(
  row: Row,
  image: string,
  made: string[],
  run: string,
  index: number,
  owner: Owner | null
) {
  const name = plantedName(run, index);
  const sockets = mkdtempSync(join(run, `pg-${index}-`));
  const id = (await docker(row, "create", "--name", name, image)).trim();
  made.push(id);
  const mark = { owner, run, identity: identityOf(run), sockets, container: name };
  return {
    name,
    id: id.slice(0, 12),
    fullId: id,
    sockets,
    listing: `${id} ${name} ${JSON.stringify(mark)}`,
  };
}

/** A directory in `tmp` with a stand-in `docker` for the starts under test, answered as `bin`: a
 *  listing of the marks is the real one with each line of `listings` added
 *  (`leaveUnlistedContainer`), and every other call runs the lines of bash `staged` gives for the
 *  real `docker`'s path, with which a row stages a call, and then the real `docker`. */
export function soleListing(
  tmp: string,
  listings: readonly string[],
  staged: (real: string) => readonly string[] = () => []
): { readonly bin: string } {
  const real = Bun.which("docker");
  if (real === null) throw new Error("no docker on PATH");
  const bin = mkdtempSync(join(tmp, "bin-"));
  writeFileSync(join(bin, "listed"), `${listings.join("\n")}\n`);
  writeFileSync(
    join(bin, "docker"),
    [
      "#!/usr/bin/env bash",
      `if [ "$1" = ps ] && [[ " $* " == *" label=${markLabel} "* ]]; then`,
      `  ${JSON.stringify(real)} "$@" || exit`,
      `  exec cat ${JSON.stringify(join(bin, "listed"))}`,
      "fi",
      ...staged(real),
      `exec ${JSON.stringify(real)} "$@"`,
      "",
    ].join("\n")
  );
  chmodSync(join(bin, "docker"), 0o755);
  return { bin };
}

/** A socket directory that no start can remove: it holds a directory of mode 0500 with a file in
 *  it, which its owner cannot unlink. The row gives the mode back before it removes its files. */
export function heldSockets(parent: string, index: number): string {
  const sockets = mkdtempSync(join(parent, `pg-${index}-`));
  mkdirSync(join(sockets, "held"));
  writeFileSync(join(sockets, "held", "socket"), "");
  chmodSync(join(sockets, "held"), 0o500);
  return sockets;
}

/** scripts/e2e/lib/leftovers.sh, through which each shell stack marks its Postgres and removes the
 *  Postgres of each stack that is gone. */
export const leftoversLib = fileURLToPath(
  new URL("../../../../scripts/e2e/lib/leftovers.sh", import.meta.url)
);

/** A shell stack as `scripts/e2e/lib/leftovers.sh`'s callers start one: a bash whose Postgres
 *  container carries the mark `postgres_mark` makes for the shell's own pid, and which then runs
 *  until it is killed. A SIGKILL stops nothing. */
const shellStackScript = `set -euo pipefail
. "$LEFTOVERS_LIB"
fail() { echo "FAIL: $*" >&2; exit 1; }
work=$(mktemp -d)
pg_mark=$(postgres_mark "$work" "$SHELL_PG") || fail "the mark of $SHELL_PG could not be made"
docker run -d --name "$SHELL_PG" --label "$pg_mark" -e POSTGRES_HOST_AUTH_METHOD=trust "$SHELL_IMAGE"
exec sleep infinity
`;

/** A shell stack's Postgres: the shell that owns it and the id of its container. */
export interface ShellStack {
  readonly child: Child;
  readonly id: string;
}

/** Starts a shell stack with `env` whose container is `name`, for the row `row`, whose limit
 *  counts it (`shellStackStart`), and answers once its `docker run` printed the container's id. */
export async function startShellStack(
  row: Row,
  env: Record<string, string | undefined>,
  image: string,
  name: string
): Promise<ShellStack> {
  const charged = charge(row, shellStackStart);
  const child = launch(["bash", "-c", shellStackScript], {
    ...env,
    LEFTOVERS_LIB: leftoversLib,
    SHELL_PG: name,
    SHELL_IMAGE: image,
  });
  return {
    child,
    id: (await firstLine(child, shellStackStart, `the shell stack ${name}`, charged)).trim(),
  };
}
