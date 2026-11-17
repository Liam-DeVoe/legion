/** The next start of a stack removes the Postgres of a stack killed hard, and leaves the Postgres
 *  of a stack that still runs (pull request #4236).
 *
 *  Requirement R3 of that pull request: "The next start of a test stack removes each Postgres of
 *  a stack that it can show dead. It removes no Postgres of a stack that still runs. It leaves a
 *  Postgres whose stack it cannot judge, and it names that Postgres in one line."
 *
 *  A stack killed with SIGKILL runs no stop, so its Postgres serves on: the image provider's
 *  container and the binaries provider's postmaster, each with its data and socket directories.
 *  Each start of a stack (`startStacks`) now first removes every Postgres whose mark names a
 *  process that is gone, and leaves every other one (e2e/harness-postgres-marks.ts). Many stacks
 *  run at once on a shared machine, so the removal of a running stack's Postgres is the worst
 *  fault this code can have: every row holds the Postgres or the mark of a running stack beside
 *  what it expects removed, and requires that it stays.
 *
 *  THE STACKS ARE CHILDREN. Each stack is a process this file starts, records by its pid and its
 *  start time, and stops itself. In the first row of each provider, stacks start one Postgres
 *  each, as the pool starts each stack's, in run directories named as the pool names its own,
 *  under a `TMPDIR` of the row's own, and the third start is a whole start of the pool
 *  (`startStacks`) with the row's provider and the harness a deployed run starts. The other rows
 *  write marks of their own instead, each naming directories or a container the row made, with
 *  owners built from this process. The start those rows read is `startStacks` itself, given no
 *  provider: it removes what it finds, then refuses to start two stacks with no Postgres, which is
 *  how a row runs the pool's start without the minutes of a whole harness. The shell stacks' rows
 *  start a shell that marks its Postgres container as `scripts/e2e/lib/leftovers.sh` does. A
 *  SIGKILL goes only to a child this file started, while `/proc` still names that process.
 *
 *  OTHER STARTS ON THE MACHINE. Every start of a stack lists every container Docker lists with a
 *  mark, and runs of other sessions start stacks at the same time. So a container a row plants
 *  dead may be removed by another start first: a row reads its own start's line about it, which
 *  says this start removed it, or says that another start did, or is absent where the other start
 *  removed it before this start listed it, and it holds R3's outcome, the container gone, in each
 *  case. Every name a row gives is of this run (`runTag`), and every row removes what it made by
 *  the id Docker made it under, in its `finally`, reporting each removal that fails with Docker's
 *  reason, so no row's leftover fails a later row.
 *
 *  THE PROVIDERS. Each row names its provider in its title and needs that provider's variable
 *  alone, so a CI step that gives one provider selects its rows with `bun test -t`. The image
 *  provider's rows need `DISPATCH_E2E_PG_IMAGE` and a local Docker daemon; the binaries
 *  provider's need `DISPATCH_E2E_PG_BIN` naming a Postgres installation with pgvector, and a user
 *  other than root, whom no mode keeps out. Outside CI a row whose variable is unset skips and
 *  says so; in CI it fails (`./postgres-provider`). The image rows read every container that
 *  Docker lists with a mark, a running stack's of another run included, and the row's live
 *  controls are what show those are left. */

import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readlinkSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import type { Subprocess } from "bun";

import {
  pgBinVariable,
  pgConnectionsVariable,
  pgImageVariable,
  type StackDatabase,
  socketDirectory,
  socketFile,
  stackPostgres,
} from "../harness-postgres";
import {
  identityOf,
  markLabel,
  marksDirectory,
  type Owner,
  runDirectoryPrefix,
  shortTemporaryDirectory as shortBase,
  stackOwner,
  startOf,
} from "../harness-postgres-marks";
import { type HostileMark, hostileMarkKept, plantHostileMark } from "./planted-marks";
import { type ProviderVariable, requirePostgres, skipsWithoutPostgres } from "./postgres-provider";

const file = "e2e/__tests__/harness-postgres-leftover.test.ts";
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const harnessPostgres = fileURLToPath(new URL("../harness-postgres.ts", import.meta.url));
const harnessPool = fileURLToPath(new URL("../harness-pool.ts", import.meta.url));

const skipImage = skipsWithoutPostgres(file, [pgImageVariable]);
const skipBinaries = skipsWithoutPostgres(file, [pgBinVariable]);

/** What the names of this run's stacks and containers hold: random bytes, never the pid, so two
 *  runs of this file at once, or a run after one whose container stayed, share no name. */
const runTag = randomBytes(4).toString("hex");

/** A stack that starts its Postgres as the pool starts each stack's, prints where it serves,
 *  and stops it once its stdin closes. A SIGKILL stops nothing. */
const stackScript = `
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { stackPostgres } from ${JSON.stringify(harnessPostgres)};
const dir = mkdtempSync(join(tmpdir(), ${JSON.stringify(runDirectoryPrefix)}));
const database = await stackPostgres({ dir, run: process.env.LEFTOVER_RUN }).start(0);
console.log(JSON.stringify({ dir, url: database.url, address: database.address }));
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
const refused = "refused: 2 Playwright workers run 2 stacks, each with a Postgres of its own";

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

/** How long a one-shot child of this file, such as a start of the pool, may take before the row
 *  kills it and fails: a stuck child's bound, at least three times the largest reading. A start
 *  of the pool read 0.04 to 0.09 s under a load of about 300, and more than 180 s under a load of
 *  1,217, on one shared Linux machine on 2026-11-17. */
const childMs = 600_000;
/** How long a stack may take to print where its Postgres serves: the provider's own readiness
 *  wait gives up at 120 s, and an initdb or a container's start under load comes before it; at
 *  least three times the largest reading, as `childMs` is. */
const stackMs = 600_000;

/** The start of the process `pid`, as `startOf` reads it from `/proc`, or "" where the process is
 *  gone. */
function startTimeOf(pid: number): string {
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

function spawn(script: string, env: Record<string, string | undefined>): Child {
  const child = Bun.spawn(["bun", "--eval", script], {
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

/** SIGKILL to `child`, a process this file started, only while `/proc` still names it: the pid
 *  with the start read as it spawned. */
function kill(child: Child): void {
  if (child.start === "" || startTimeOf(child.pid) !== child.start) return;
  child.process.kill("SIGKILL");
}

/** Waits for `child` to exit, and kills it and throws where it has not within `ms`. */
async function exited(child: Child, ms: number, what: string): Promise<number> {
  const code = await Promise.race([child.process.exited, sleep(ms).then(() => undefined)]);
  if (code !== undefined) return code;
  kill(child);
  throw new Error(`${what} did not end within ${ms / 1000} s: ${child.output.stderr}`);
}

/** The environment of a child of a row: this one's, with the row's own `TMPDIR` and the one
 *  provider the row names. */
function childEnv(tmp: string, provider: { variable: ProviderVariable; value: string }) {
  const env: Record<string, string | undefined> = { ...process.env, TMPDIR: tmp };
  delete env[pgBinVariable];
  delete env[pgImageVariable];
  env[provider.variable] = provider.value;
  env[pgConnectionsVariable] = process.env[pgConnectionsVariable] ?? "100";
  return env;
}

/** One stack's Postgres as its child printed it. */
interface Stack {
  readonly child: Child;
  readonly dir: string;
  readonly url: string;
  readonly address: string;
}

async function startStack(env: Record<string, string | undefined>, run: string): Promise<Stack> {
  const child = spawn(stackScript, { ...env, LEFTOVER_RUN: run });
  const deadline = Date.now() + stackMs;
  while (!child.output.stdout.includes("\n")) {
    const code = await Promise.race([child.process.exited, sleep(200).then(() => undefined)]);
    if (code !== undefined && !child.output.stdout.includes("\n")) {
      await Promise.race([child.drained, sleep(10_000)]);
      throw new Error(`stack ${run} exited ${code} before it served: ${child.output.stderr}`);
    }
    if (Date.now() > deadline) {
      kill(child);
      throw new Error(
        `stack ${run} did not serve within ${stackMs / 1000} s: ${child.output.stderr}`
      );
    }
  }
  const printed = JSON.parse(child.output.stdout.split("\n")[0] ?? "") as {
    dir: string;
    url: string;
    address: string;
  };
  return { child, ...printed };
}

/** Runs the pool's start in a child with `env`, and answers what it printed. */
async function startPool(env: Record<string, string | undefined>): Promise<Child["output"]> {
  const child = spawn(startScript, env);
  child.process.stdin.end();
  const code = await exited(child, childMs, "a start of the pool");
  // The pipes end as it exits; their last chunks may still be on the way to `output`.
  await Promise.race([child.drained, sleep(10_000)]);
  if (code !== 0) throw new Error(`a start of the pool exited ${code}: ${child.output.stderr}`);
  return child.output;
}

/** A whole start of the pool (`poolScript`) that serves, with what it printed. */
interface PoolStart {
  readonly child: Child;
  readonly url: string;
}

/** Starts the pool in a child with `env` and its provider, and answers once its stack serves. */
async function startServingPool(env: Record<string, string | undefined>): Promise<PoolStart> {
  const own: Record<string, string | undefined> = {
    ...env,
    PLAYWRIGHT_BASE_URL: "http://127.0.0.1:9",
  };
  // A run of one stack against a database the caller named starts no Postgres.
  delete own.DATABASE_URL;
  const child = spawn(poolScript, own);
  const deadline = Date.now() + childMs;
  while (!child.output.stdout.includes("\n")) {
    const code = await Promise.race([child.process.exited, sleep(200).then(() => undefined)]);
    if (code !== undefined && !child.output.stdout.includes("\n")) {
      await Promise.race([child.drained, sleep(10_000)]);
      throw new Error(
        `a start of the pool exited ${code} before it served: ${child.output.stderr}`
      );
    }
    if (Date.now() > deadline) {
      kill(child);
      throw new Error(
        `a start of the pool did not serve within ${childMs / 1000} s: ${child.output.stderr}`
      );
    }
  }
  const { url } = JSON.parse(child.output.stdout.split("\n")[0] ?? "") as { url?: string };
  if (url === undefined)
    throw new Error(`a start of the pool named no database: ${child.output.stdout}`);
  return { child, url };
}

/** Stops a start of the pool by closing its stdin, which runs its `stopStacks`, and kills it
 *  where that did not end it in time. */
async function stopPool(start: PoolStart | undefined): Promise<void> {
  if (start === undefined) return;
  start.child.process.stdin.end();
  await exited(start.child, childMs, "a start of the pool").catch(() => {});
}

/** Whether the Postgres at `url` answers `select 1`. */
function answers(url: string): boolean {
  const psql = spawnSync("psql", [url, "-XAtc", "select 1"], { encoding: "utf8", timeout: 30_000 });
  return psql.status === 0 && psql.stdout.trim() === "1";
}

/** `docker` with `args`, which throws where it fails. */
function docker(...args: string[]): string {
  const run = spawnSync("docker", args, { encoding: "utf8", timeout: 120_000 });
  if (run.status !== 0) throw new Error(`docker ${args.join(" ")}: ${run.stderr}`);
  return run.stdout;
}

/** Whether Docker has the object `name` of `kind`, read with `docker <kind> inspect`: false only
 *  where Docker says it has no such object, and a throw where the read fails otherwise, so a row
 *  never reads a failed read as a removal. */
function exists(kind: "container" | "volume", name: string): boolean {
  const run = spawnSync("docker", [kind, "inspect", name], { encoding: "utf8", timeout: 120_000 });
  if (run.status === 0) return true;
  if (/no such (container|volume|object)/i.test(run.stderr)) return false;
  throw new Error(`docker ${kind} inspect ${name}: ${run.stderr || run.error?.message}`);
}

/** Whether a container of this name exists, running or not. */
function containerExists(name: string): boolean {
  return exists("container", name);
}

/** The volumes of the container `name`: the image's data volume, which holds its stack's data. */
function volumesOf(name: string): string[] {
  const format = '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}';
  return docker("container", "inspect", "--format", format, name)
    .trim()
    .split(/\s+/)
    .filter(Boolean);
}

/** Stops `stack` the way its own child does, by closing its stdin, and where that did not end it
 *  in time, kills it. */
async function stopStack(stack: Stack | undefined): Promise<void> {
  if (stack === undefined) return;
  stack.child.process.stdin.end();
  await exited(stack.child, childMs, `the stack in ${stack.dir}`).catch(() => {});
}

/** The ids of the containers whose mark names `run`, the run directory of a stack this row
 *  started, as Docker lists them. A listing that fails throws (`docker`), so no row reads it as
 *  one that lists none. */
function containersOf(run: string): string[] {
  const listed = docker(
    "ps",
    "--all",
    "--no-trunc",
    "--filter",
    `label=${markLabel}`,
    "--format",
    `{{.ID}} {{.Label "${markLabel}"}}`
  );
  return listed.split("\n").flatMap((line) => {
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

/** How long a row's removal waits for a removal of the same container that another start of the
 *  machine has in progress, the bound a start itself waits it out with (`dockerMs` of
 *  e2e/harness-postgres-marks.ts): a daemon that answers removes a container in well under a
 *  second. */
const removalInProgressMs = 60_000;

/** Removes each container of `ids`, by its id, and once it has tried them all throws naming each
 *  removal that failed, with what Docker said: one failed removal keeps no other container. A
 *  container that is gone already is no failure (`docker rm --force`), nor is one that another
 *  start of the machine is removing, once Docker no longer lists it. */
function removeIds(ids: readonly string[]): void {
  const failed: string[] = [];
  for (const id of ids) {
    const run = spawnSync("docker", ["rm", "--force", "--volumes", id], {
      encoding: "utf8",
      timeout: 120_000,
    });
    if (run.status === 0) continue;
    const said = run.stderr.trim() || run.error?.message || `exit ${run.status}`;
    if (/removal of container \S+ is already in progress/.test(said)) {
      const deadline = Date.now() + removalInProgressMs;
      while (containerExists(id) && Date.now() < deadline) Bun.sleepSync(200);
      if (!containerExists(id)) continue;
    }
    failed.push(`${id}: ${said}`);
  }
  if (failed.length > 0)
    throw new Error(`docker rm --force --volumes failed: ${failed.join("; ")}`);
}

/** Removes each container whose mark names `run`, the run directory of a stack this row started,
 *  by the id Docker lists it under: a container's name is no one stack's alone. */
function removeContainersOf(run: string): void {
  removeIds(containersOf(run));
}

/** The lines of a start's stderr that name the container `container`, by name or id. Each says
 *  that this start removed it, or that another start removed it first, or, for a container that
 *  another start removed, that this start removed the socket directory it served in, which is the
 *  run directory sweep's once Docker lists no such container (e2e/harness-postgres-marks.ts). At
 *  most one says that this start removed the container. A start names no container another start
 *  removed before it listed them. */
function removalLines(stderr: string, container: string): string[] {
  const lines = linesWith(stderr, container);
  for (const line of lines) {
    expect(line).toMatch(
      /: removed container |another start removed first|its container \S+ is gone, as Docker lists no such container/
    );
  }
  expect(
    lines.filter((line) => line.includes(": removed container ")).length,
    stderr
  ).toBeLessThanOrEqual(1);
  return lines;
}

/** Removes what a stack this row started left, by the paths it recorded for it: its container,
 *  by the id its mark is listed under, or its postmasters, then its directories. */
function removeLeft(
  stack: Stack | undefined,
  provider: { variable: ProviderVariable; value: string }
) {
  if (stack === undefined) return;
  if (provider.variable === pgImageVariable) {
    removeContainersOf(stack.dir);
    rmSync(dirname(stack.address), { recursive: true, force: true });
  } else {
    for (const data of [
      join(stack.dir, "worker-0", "pgdata"),
      join(stack.dir, "template", "pgdata"),
    ]) {
      if (existsSync(join(data, "postmaster.pid"))) {
        spawnSync(join(provider.value, "pg_ctl"), ["-D", data, "-m", "immediate", "-w", "stop"], {
          timeout: 120_000,
        });
      }
    }
    rmSync(stack.address, { recursive: true, force: true });
  }
}

for (const provider of [
  { title: "image provider", variable: pgImageVariable, skip: skipImage },
  { title: "binaries provider", variable: pgBinVariable, skip: skipBinaries },
] as const) {
  test.skipIf(provider.skip)(
    `${provider.title}: v-no-leftover-postgres: the next start removes the Postgres of a stack killed hard, with its data and socket directories, leaves the Postgres of a stack that runs, leaves and names one whose stack it cannot judge, and runs no program a mark names`,
    async () => {
      const named = requirePostgres([provider.variable]);
      const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
      const env = childEnv(tmp, named);
      const runs = {
        killed: `leftover-${runTag}-a`,
        kept: `leftover-${runTag}-b`,
        unjudged: `leftover-${runTag}-c`,
      };
      const containers = {
        killed: `dispatch-e2e-pg-${runs.killed}-0`,
        kept: `dispatch-e2e-pg-${runs.kept}-0`,
      };
      let killed: Stack | undefined;
      let kept: Stack | undefined;
      let unjudged: Unjudged | undefined;
      let hostile: HostileMark | undefined;
      let third: PoolStart | undefined;
      try {
        // Two marks beside the stacks: one on a Postgres that serves, of another boot, which the
        // start cannot judge, and one, in a run directory of the row's TMPDIR named as the pool
        // names one, that names a program of the planter's and a directory outside its run.
        const beside = mkdtempSync(join(tmp, runDirectoryPrefix));
        hostile = plantHostileMark(beside, tmp, owners().gone);
        // Each stack that started is recorded before a start that failed is thrown, so the
        // `finally` stops it and removes what it made.
        const [a, b, c] = await Promise.allSettled([
          startStack(env, runs.killed),
          startStack(env, runs.kept),
          startUnjudged(env, named, runs.unjudged, beside),
        ]);
        killed = a.status === "fulfilled" ? a.value : undefined;
        kept = b.status === "fulfilled" ? b.value : undefined;
        unjudged = c.status === "fulfilled" ? c.value : undefined;
        if (killed === undefined || kept === undefined || unjudged === undefined) {
          throw [a, b, c].flatMap((result) =>
            result.status === "rejected" ? [result.reason] : []
          )[0];
        }
        expect([answers(killed.url), answers(kept.url), answers(unjudged.url)]).toEqual([
          true,
          true,
          true,
        ]);
        // The image's data volume, where the killed stack's data is; the binaries provider's is
        // its data directory.
        const killedVolumes =
          provider.variable === pgImageVariable ? volumesOf(containers.killed) : [];
        if (provider.variable === pgImageVariable) expect(killedVolumes.length).toBeGreaterThan(0);

        kill(killed.child);
        expect(await exited(killed.child, childMs, "the killed stack")).toBe(137);
        // The kill ran no stop: the killed stack's Postgres still serves.
        expect(answers(killed.url)).toBe(true);

        third = await startServingPool(env);
        const lines = third.child.output.stderr;

        // The killed stack's Postgres is gone, with its data and its socket directory.
        expect(answers(killed.url)).toBe(false);
        if (provider.variable === pgImageVariable) {
          expect(containerExists(containers.killed)).toBe(false);
          expect(killedVolumes.filter((volume) => exists("volume", volume))).toEqual([]);
          expect(existsSync(dirname(killed.address))).toBe(false);
          // Each line that names it says what this start did of it (`removalLines`): another
          // start of the machine may have removed the container first.
          removalLines(lines, containers.killed);
        } else {
          expect(existsSync(join(killed.dir, "worker-0", "pgdata"))).toBe(false);
          expect(existsSync(join(killed.dir, "template", "pgdata"))).toBe(false);
          expect(existsSync(killed.address)).toBe(false);
        }
        // The three stacks serve: the one that runs, with its data and its socket directory, the
        // one whose stack the start cannot judge, and the third start's own.
        expect([answers(kept.url), answers(unjudged.url), answers(third.url)]).toEqual([
          true,
          true,
          true,
        ]);
        if (provider.variable === pgImageVariable) {
          expect(containerExists(containers.kept)).toBe(true);
        } else {
          expect(existsSync(join(kept.dir, "worker-0", "pgdata"))).toBe(true);
        }
        expect(existsSync(kept.address)).toBe(true);
        // The start names the Postgres it cannot judge in one line; it ran no program that a mark
        // names.
        expect(linesWith(lines, `${unjudged.named} (boot `)).toHaveLength(1);
        expect(hostile.kept()).toEqual(hostileMarkKept);
      } finally {
        if (killed !== undefined) kill(killed.child);
        hostile?.live.stop();
        await stopPool(third);
        await stopStack(kept);
        await removeAll(
          () => unjudged?.stop(),
          () => removeLeft(killed, named),
          () => removeLeft(kept, named),
          () => rmSync(tmp, { recursive: true, force: true })
        );
      }
    },
    // The row drives four starts of a Postgres it starts and stops itself, three at once, each an
    // initdb or a container's start and a readiness wait of at most `stackMs`, then the third
    // start, a whole start of the pool waited for at most `childMs`, which stops a postmaster or
    // removes a container before it starts its own, and the stops, each of at most `childMs`. On
    // a shared Linux machine under a load of about 300, on 2026-11-17, the image row read 4.4 to
    // 10.6 s and the binaries row 2.1 to 3.0 s with two starts at once; 2,400 s holds the three
    // waits and the stops at their bounds.
    2_400_000
  );
}

/** A Postgres of the row's own that serves under a mark the start cannot judge, of another boot,
 *  with the name a start's line names it by and its own stop. */
interface Unjudged {
  readonly url: string;
  readonly named: string;
  stop(): Promise<void> | void;
}

/** Starts a Postgres that serves under a mark of another boot. The binaries provider's is a stack
 *  started as `startStack` starts one, whose mark is then written again with another boot's
 *  owner; the start names its mark file. The image provider's is a container of the row's own,
 *  made as the provider makes one, whose label is such a mark from its create on, in a socket
 *  directory of the run directory `run`; the start names it by its id. */
async function startUnjudged(
  env: Record<string, string | undefined>,
  provider: { variable: ProviderVariable; value: string },
  run: string,
  beside: string
): Promise<Unjudged> {
  if (provider.variable === pgBinVariable) {
    const stack = await startStack(env, run);
    const markFile = join(stack.dir, marksDirectory, "worker-0.json");
    const mark = JSON.parse(readFileSync(markFile, "utf8")) as Record<string, unknown>;
    writeFileSync(markFile, JSON.stringify({ ...mark, owner: owners().otherBoot }));
    return {
      url: stack.url,
      named: markFile,
      stop: async () => {
        await stopStack(stack);
        removeLeft(stack, provider);
      },
    };
  }
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
  const id = docker(
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
  ).trim();
  const url = `postgres://postgres@/postgres?host=${encodeURIComponent(sockets)}&sslmode=disable`;
  // The image's first server is its initialisation's, which it stops before its last one starts.
  const initialised = () => {
    const logs = spawnSync("docker", ["logs", id], { encoding: "utf8", timeout: 120_000 });
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
  const deadline = Date.now() + stackMs;
  while (!(initialised() && answers(url))) {
    if (Date.now() > deadline) {
      await stop();
      throw new Error(`the container ${name} did not serve within ${stackMs / 1000} s`);
    }
    await sleep(500);
  }
  return { url, named: `container ${id.slice(0, 12)}`, stop };
}

/** Runs each removal of a row's `finally`, every one even where another fails, and throws naming
 *  each that failed, with what it said. */
async function removeAll(...removals: (() => unknown)[]): Promise<void> {
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
function owners() {
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

/** What a binaries provider's stack left in `run`, as its mark names it: a data directory and a
 *  socket directory, each holding a file, and the mark. No server serves from it, so a start that
 *  judges its owner gone removes the directories and the mark alone. `text` replaces the mark
 *  where a row writes one that is no mark. */
function leaveCluster(
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
function plantedName(run: string, index: number): string {
  return `dispatch-e2e-pg-${basename(run).slice(runDirectoryPrefix.length)}-${runTag}-${index}`;
}

/** A container an image provider's stack left, made and never started, under a name of this
 *  row's own (`plantedName`): a start judges it by its label alone, which is its mark unless
 *  `label` names another. `made` collects the id Docker made it under, for the row's removal. */
function leaveContainer(
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
  const id = docker("create", "--name", name, "--label", `${markLabel}=${mark}`, image).trim();
  made.push(id);
  return { name, id: id.slice(0, 12), sockets };
}

/** The mark file in `run` of stack `index`'s container `container`, whose socket directory is
 *  `sockets`, as the image provider writes it beside the container's label. Answers its path. */
function leaveContainerMark(
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

/** A socket directory that no start can remove: it holds a directory of mode 0500 with a file in
 *  it, which its owner cannot unlink. The row gives the mode back before it removes its files. */
function heldSockets(parent: string, index: number): string {
  const sockets = mkdtempSync(join(parent, `pg-${index}-`));
  mkdirSync(join(sockets, "held"));
  writeFileSync(join(sockets, "held", "socket"), "");
  chmodSync(join(sockets, "held"), 0o500);
  return sockets;
}

/** The lines of a start's stderr that hold `text`. */
function linesWith(stderr: string, text: string): string[] {
  return stderr.split("\n").filter((line) => line.includes(text));
}

test.skipIf(skipBinaries)(
  "binaries provider: a start leaves the mark of a stack that runs, of another boot, PID namespace or time namespace or of no /proc, and a mark it cannot read, and names each one it left",
  async () => {
    const { value: bin } = requirePostgres([pgBinVariable]);
    const who = owners();
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    const closed = mkdtempSync(join(tmp, runDirectoryPrefix));
    try {
      const runs = leaveCluster(run, 0, bin, who.runs);
      const otherBoot = leaveCluster(run, 1, bin, who.otherBoot);
      const otherNamespace = leaveCluster(run, 2, bin, who.otherNamespace);
      const gone = leaveCluster(run, 3, bin, who.gone);
      const noProc = leaveCluster(run, 4, bin, who.noProc);
      const notAMark = leaveCluster(run, 5, bin, who.gone, { text: "{" });
      // A gone owner's mark whose socket directory is neither in its run directory nor in /tmp.
      const outside = leaveCluster(run, 6, bin, who.gone, {
        sockets: mkdtempSync(join(tmp, "pg-6-")),
      });
      const otherTime = leaveCluster(run, 7, bin, who.otherTime);
      // Another user's run directory reads as this one does: its mode keeps this process out.
      mkdirSync(join(closed, marksDirectory));
      chmodSync(closed, 0o000);

      const start = await startPool(childEnv(tmp, { variable: pgBinVariable, value: bin }));
      expect(start.stdout).toContain(refused);

      // The one mark whose stack is gone goes, with the directories it names.
      expect(gone.left()).toEqual([false, false, false]);
      // Every other one stays, with its directories.
      for (const kept of [runs, otherBoot, otherNamespace, noProc, notAMark, outside, otherTime]) {
        expect({ mark: kept.mark, left: kept.left() }).toEqual({
          mark: kept.mark,
          left: [true, true, true],
        });
      }
      const unread = linesWith(start.stderr, "left what it cannot read:");
      expect(unread).toHaveLength(1);
      expect(unread[0]).toContain(`${notAMark.mark} (no mark of this run directory)`);
      expect(unread[0]).toContain(`${outside.mark} (no mark of this run directory)`);
      expect(unread[0]).toContain(`${closed} (EACCES)`);
      const unjudged = linesWith(start.stderr, "left the Postgres of stacks whose process");
      expect(unjudged).toHaveLength(1);
      expect(unjudged[0]).toContain(`${otherBoot.mark} (boot ${who.otherBoot.boot})`);
      expect(unjudged[0]).toContain(`${otherNamespace.mark} (PID namespace pid:[1])`);
      expect(unjudged[0]).toContain(`${noProc.mark} (its stack ran where there is no /proc)`);
      expect(unjudged[0]).toContain(`${otherTime.mark} (time namespace time:[1])`);
      // No postmaster serves the gone mark's data directory, and the line names only what went.
      expect(
        linesWith(start.stderr, `removed ${gone.data} and ${gone.sockets}, which`)
      ).toHaveLength(1);
      expect([runs.data, runs.mark].filter((path) => start.stderr.includes(path))).toEqual([]);
    } finally {
      chmodSync(closed, 0o700);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row drives one start of the pool, a `bun` child that reads the row's marks and removes
  // one data and one socket directory. It read 0.04 to 0.08 s on a shared Linux machine under a
  // load of about 300, on 2026-11-17; 300 s leaves a cold `bun` and a slow disk room.
  300_000
);

test.skipIf(skipImage)(
  "image provider: a stack whose start fails removes no container of another stack, though both stacks have one run name, as two stacks of one pid in two PID namespaces once had",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const env = childEnv(tmp, { variable: pgImageVariable, value: image });
    // One run name for both stacks, as the pool's pid made it for two pools of one pid: the
    // second stack's container then has the first's name, so its `docker create` fails.
    const run = `leftover-${runTag}-shared`;
    const dirs: string[] = [];
    let first: StackDatabase | undefined;
    try {
      const start = (prefix: string) => {
        const dir = mkdtempSync(join(tmp, prefix));
        dirs.push(dir);
        const provider = stackPostgres({ dir, run, env });
        if (provider === undefined) throw new Error(`${pgImageVariable} named no provider`);
        return provider.start(0);
      };
      first = await start("a-");
      expect(answers(first.url)).toBe(true);

      let failed = "";
      await start("b-").then(
        () => {},
        (error: unknown) => {
          failed = error instanceof Error ? error.message : String(error);
        }
      );
      expect(failed).toContain("did not start");

      // The failed start removed nothing of the first stack's: its container still serves.
      expect(answers(first.url)).toBe(true);
    } finally {
      await first?.stop().catch(() => {});
      for (const dir of dirs) removeContainersOf(dir);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row drives two starts of the image provider in this process, the first a container's
  // create and start and a readiness wait that gives up at 120 s, the second a `docker create`
  // refused at once, and the first's stop; 300 s leaves the wait and a slow Docker daemon room.
  300_000
);

test.skipIf(skipImage)(
  "image provider: a start leaves the container of a stack that runs, of another boot, PID namespace or time namespace or of no /proc, and one whose label it cannot read, and names each one it left; the run directory sweep removes the socket directory of a container that is gone, and of no container Docker lists",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const who = owners();
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    // The socket directory of the container whose stack is gone is in /tmp, where a long
    // `TMPDIR` puts it.
    const goneSockets = mkdtempSync(join("/tmp", "pg-3-"));
    const made: string[] = [];
    try {
      const leave = (index: number, owner: Owner | null, label?: string) =>
        leaveContainer(
          image,
          made,
          run,
          index,
          owner,
          mkdtempSync(join(run, `pg-${index}-`)),
          label
        );
      const runs = leave(0, who.runs);
      const otherBoot = leave(1, who.otherBoot);
      const otherNamespace = leave(2, who.otherNamespace);
      const gone = leaveContainer(image, made, run, 3, who.gone, goneSockets);
      const noProc = leave(4, who.noProc);
      const notAMark = leave(5, who.gone, "{");
      const otherTime = leave(6, who.otherTime);
      // The run directory's marks of two containers: the one whose label is no mark, which Docker
      // still lists, and one Docker no longer lists, both of a gone stack.
      const notAMarkFile = leaveContainerMark(run, 5, who.gone, notAMark.sockets, notAMark.name);
      const goneEarlier = mkdtempSync(join(run, "pg-7-"));
      const goneEarlierName = plantedName(run, 7);
      const goneEarlierFile = leaveContainerMark(run, 7, who.gone, goneEarlier, goneEarlierName);

      const start = await startPool(childEnv(tmp, { variable: pgImageVariable, value: image }));
      expect(start.stdout).toContain(refused);

      // The one container whose stack is gone goes, with its socket directory, removed by this
      // start or by another start on the machine, whose line this start's line names.
      expect([containerExists(gone.name), existsSync(gone.sockets)]).toEqual([false, false]);
      removalLines(start.stderr, gone.id);
      // Every other one stays, with its socket directory.
      for (const kept of [runs, otherBoot, otherNamespace, noProc, notAMark, otherTime]) {
        expect({
          name: kept.name,
          left: [containerExists(kept.name), existsSync(kept.sockets)],
        }).toEqual({ name: kept.name, left: [true, true] });
      }
      expect(existsSync(notAMarkFile)).toBe(true);
      expect([existsSync(goneEarlier), existsSync(goneEarlierFile)]).toEqual([false, false]);
      const goneEarlierLines = linesWith(start.stderr, `removed ${goneEarlier}, which`);
      expect(goneEarlierLines).toHaveLength(1);
      expect(goneEarlierLines[0]).toContain(`its container ${goneEarlierName} is gone`);
      const unread = linesWith(start.stderr, "left what it cannot read:");
      expect(unread).toHaveLength(1);
      expect(unread[0]).toContain(`container ${notAMark.id} (its label is no mark)`);
      const unjudged = linesWith(start.stderr, "left the Postgres of stacks whose process");
      expect(unjudged).toHaveLength(1);
      expect(unjudged[0]).toContain(`container ${otherBoot.id} (boot ${who.otherBoot.boot})`);
      expect(unjudged[0]).toContain(`container ${otherNamespace.id} (PID namespace pid:[1])`);
      expect(unjudged[0]).toContain(
        `container ${noProc.id} (its stack ran where there is no /proc)`
      );
      expect(unjudged[0]).toContain(`container ${otherTime.id} (time namespace time:[1])`);
      expect(start.stderr).not.toContain(runs.id);
    } finally {
      removeIds(made);
      rmSync(goneSockets, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row drives six `docker create`s, one start of the pool, which lists the containers and
  // removes one, and the removal of the rest. It read 3.1 to 5.4 s on a shared Linux machine
  // under a load of about 300, on 2026-11-17; 300 s leaves a slow Docker daemon room.
  300_000
);

test.skipIf(skipBinaries)(
  "binaries provider: a removal that fails is one line and fails no start, and two starts at once that find one dead mark both go on",
  async () => {
    const { value: bin } = requirePostgres([pgBinVariable]);
    const who = owners();
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    const held = heldSockets(run, 1);
    try {
      const runs = leaveCluster(run, 0, bin, who.runs);
      const failing = leaveCluster(run, 1, bin, who.gone, { sockets: held });
      const gone = leaveCluster(run, 2, bin, who.gone);

      const env = childEnv(tmp, { variable: pgBinVariable, value: bin });
      const starts = await Promise.all([startPool(env), startPool(env)]);

      for (const start of starts) {
        expect(start.stdout).toContain(refused);
        // Each start names the removal that fails on one line, and the dead mark both found at
        // most once.
        expect(linesWith(start.stderr, failing.data)).toHaveLength(1);
        expect(linesWith(start.stderr, "is not removed")).toHaveLength(1);
        expect(linesWith(start.stderr, gone.data).length).toBeLessThanOrEqual(1);
        expect([runs.data, runs.mark].filter((path) => start.stderr.includes(path))).toEqual([]);
      }
      // Together the two starts say "removed" once of each directory of the dead mark: one start
      // renamed it first and removed it, and the other says that another start removed it.
      for (const path of [gone.data, gone.sockets]) {
        const removed = starts.flatMap((start) => linesWith(start.stderr, `removed ${path}`));
        expect({ path, removed: removed.length }).toEqual({ path, removed: 1 });
      }
      // The removal that failed keeps its mark for the next start; the other leftover is gone.
      expect([existsSync(failing.mark), existsSync(failing.sockets)]).toEqual([true, true]);
      expect(gone.left()).toEqual([false, false, false]);
      expect(runs.left()).toEqual([true, true, true]);
    } finally {
      chmodSync(join(held, "held"), 0o700);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row drives two starts of the pool at once, each a `bun` child that reads three marks and
  // removes what it finds dead. It read 0.04 to 0.09 s on a shared Linux machine under a load of
  // about 300, on 2026-11-17; 300 s leaves a cold `bun` and a slow disk room.
  300_000
);

// A developer may rename the run directory of a stack killed hard, to keep its logs. Its postmaster
// runs on from the new name, and the marks there name the old one, so no start can trust them: the
// next start leaves that Postgres and names it on a line, whether the new name is one the pool
// gives (its listing of TMPDIR reads it) or not (the kernel shows the postmaster serving from it,
// `servedElsewhere` of e2e/harness-postgres-marks.ts).
for (const renamed of ["kept-run", `${runDirectoryPrefix}kept`] as const) {
  test.skipIf(skipBinaries)(
    `binaries provider: the next start leaves and names the Postgres of a stack killed hard whose run directory was renamed to ${renamed}`,
    async () => {
      const named = requirePostgres([pgBinVariable]);
      const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
      const env = childEnv(tmp, named);
      const moved = join(identityOf(tmp), renamed);
      let stack: Stack | undefined;
      let postmaster: { pid: number; start: string } | undefined;
      try {
        stack = await startStack(env, `renamed-${runTag}`);
        kill(stack.child);
        expect(await exited(stack.child, childMs, "the killed stack")).toBe(137);
        renameSync(stack.dir, moved);
        // The precondition: the dead stack's postmaster runs on, from the renamed directory.
        const data = join(moved, "worker-0", "pgdata");
        const pid = Number.parseInt(readFileSync(join(data, "postmaster.pid"), "utf8"), 10);
        postmaster = { pid, start: startTimeOf(pid) };
        expect(readlinkSync(`/proc/${pid}/cwd`)).toBe(data);

        const start = await startPool(env);
        expect(start.stdout).toContain(refused);
        // R3: no start can show whose it is, so it stays, by its pid and its start (its socket
        // moved with the directory, out of the URL its stack printed), and a line names it.
        expect(startTimeOf(pid)).toBe(postmaster.start);
        expect(linesWith(start.stderr, moved).length, start.stderr).toBeGreaterThan(0);
      } finally {
        if (stack !== undefined) kill(stack.child);
        if (postmaster !== undefined && startTimeOf(postmaster.pid) === postmaster.start) {
          spawnSync(
            join(named.value, "pg_ctl"),
            ["-D", join(moved, "worker-0", "pgdata"), "-m", "immediate", "-w", "stop"],
            { timeout: 120_000 }
          );
        }
        rmSync(tmp, { recursive: true, force: true });
      }
    },
    // The row drives one stack's Postgres start, an initdb and a readiness wait of at most
    // `stackMs`, its kill, one start of the pool, waited for at most `childMs`, and the stop of
    // the postmaster it left; 1,500 s holds both waits at their bounds.
    1_500_000
  );
}

test.skipIf(skipImage)(
  "image provider: a removal that fails is one line and fails no start, and two starts at once that find one dead container both go on",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const who = owners();
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    const held = heldSockets(run, 1);
    const made: string[] = [];
    try {
      const runs = leaveContainer(image, made, run, 0, who.runs, mkdtempSync(join(run, "pg-0-")));
      const failing = leaveContainer(image, made, run, 1, who.gone, held);
      const gone = leaveContainer(image, made, run, 2, who.gone, mkdtempSync(join(run, "pg-2-")));

      const env = childEnv(tmp, { variable: pgImageVariable, value: image });
      const starts = await Promise.all([startPool(env), startPool(env)]);

      for (const start of starts) {
        expect(start.stdout).toContain(refused);
        // Each start names each dead container at most once: as the one it removed, or the one
        // whose socket directory it could not remove, or as one another start removed first.
        removalLines(start.stderr, failing.id);
        removalLines(start.stderr, gone.id);
        expect(start.stderr).not.toContain(runs.id);
      }
      // Together the two starts say "removed" of the dead container at most once: another start
      // on the machine may have removed it before both.
      expect(
        starts.flatMap((start) => linesWith(start.stderr, `removed container ${gone.id}`)).length
      ).toBeLessThanOrEqual(1);
      // A start that removed the container names the socket directory it could not remove.
      const failed = starts.flatMap((start) =>
        linesWith(start.stderr, `removed container ${failing.id}`)
      );
      for (const line of failed) expect(line).toContain(`is not removed: ${held}`);
      expect([containerExists(failing.name), existsSync(failing.sockets)]).toEqual([false, true]);
      expect([containerExists(gone.name), existsSync(gone.sockets)]).toEqual([false, false]);
      expect([containerExists(runs.name), existsSync(runs.sockets)]).toEqual([true, true]);
    } finally {
      removeIds(made);
      chmodSync(join(held, "held"), 0o700);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row drives three `docker create`s, two starts of the pool at once, each of which lists
  // the containers and removes what it finds dead, and the removal of the rest. It read 1.3 to
  // 2.0 s on a shared Linux machine under a load of about 300, on 2026-11-17; 300 s leaves a slow
  // Docker daemon room.
  300_000
);

test.skipIf(skipImage)(
  "image provider: a container made from an image committed from a dead stack's container carries that stack's label, and a start leaves it and names it",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    const made: string[] = [];
    const copy = `dispatch-e2e-pg-copy-${runTag}`;
    let committed = false;
    try {
      // `docker commit` copies the dead stack's label into the image, and each container made from
      // that image carries it. The dead stack's own container goes first, so the copy is the only
      // container that carries its mark.
      const dead = leaveContainer(
        image,
        made,
        run,
        0,
        owners().gone,
        mkdtempSync(join(run, "pg-0-"))
      );
      docker("commit", dead.id, copy);
      committed = true;
      removeIds(made.splice(0));
      const name = plantedName(run, 1);
      const id = docker("create", "--name", name, copy).trim();
      made.push(id);

      const start = await startPool(childEnv(tmp, { variable: pgImageVariable, value: image }));
      expect(start.stdout).toContain(refused);

      expect(containerExists(name)).toBe(true);
      const unread = linesWith(start.stderr, "left what it cannot read:");
      expect(unread).toHaveLength(1);
      expect(unread[0]).toContain(
        `container ${id.slice(0, 12)} (its label is the mark of ${dead.name}, not of ${name})`
      );
      expect(linesWith(start.stderr, `removed container ${id.slice(0, 12)}`)).toEqual([]);
    } finally {
      await removeAll(
        () => removeIds(made),
        () => committed && docker("image", "rm", "--force", copy),
        () => rmSync(tmp, { recursive: true, force: true })
      );
    }
  },
  // The row drives two `docker create`s, one `docker commit`, one start of the pool, waited for at
  // most `childMs`, which lists the containers and leaves the copy, and the removals; 900 s holds
  // the start at its bound and a slow Docker daemon.
  900_000
);

test.skipIf(skipImage)(
  "image provider: a start whose removal another start won says so, with what Docker said, and says it removed neither container",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const who = owners();
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    const made: string[] = [];
    const real = Bun.which("docker");
    if (real === null) throw new Error("no docker on PATH");
    try {
      // The row's two dead containers carry no label that Docker lists: the stand-in `docker`
      // below adds their marks to the listing of the start under test alone, so no other start
      // of the machine removes them before that start meets the removals the row stages.
      const listed: string[] = [];
      const plant = (index: number) => {
        const name = plantedName(run, index);
        const sockets = mkdtempSync(join(run, `pg-${index}-`));
        const id = docker("create", "--name", name, image).trim();
        made.push(id);
        const mark = { owner: who.gone, run, identity: identityOf(run), sockets, container: name };
        listed.push(`${id} ${name} ${JSON.stringify(mark)}`);
        return { name, id: id.slice(0, 12) };
      };
      const inProgress = plant(0);
      const goneFirst = plant(1);
      const [inProgressId = "", goneFirstId = ""] = made;
      // Another start's removal, as the start under test meets it: its `docker` removes the first
      // container a second after it answers Docker's refusal of a removal in progress, and
      // removes the second itself just before the real `docker rm` of it, which then finds it
      // gone. Every other call is the real `docker`'s.
      const bin = mkdtempSync(join(tmp, "bin-"));
      writeFileSync(join(bin, "listed"), `${listed.join("\n")}\n`);
      writeFileSync(
        join(bin, "docker"),
        [
          "#!/usr/bin/env bash",
          `if [ "$1" = ps ] && [[ " $* " == *" label=${markLabel} "* ]]; then`,
          `  ${JSON.stringify(real)} "$@" || exit`,
          `  exec cat ${JSON.stringify(join(bin, "listed"))}`,
          "fi",
          `if [ "$1" = rm ] && [[ " $* " == *" ${inProgressId} "* ]]; then`,
          `  ( sleep 1; exec ${JSON.stringify(real)} rm --force --volumes ${inProgressId} >/dev/null 2>&1 ) &`,
          `  echo "Error response from daemon: removal of container ${inProgressId} is already in progress" >&2`,
          "  exit 1",
          "fi",
          `if [ "$1" = rm ] && [[ " $* " == *" ${goneFirstId} "* ]]; then`,
          `  ${JSON.stringify(real)} rm --force --volumes ${goneFirstId} >/dev/null`,
          "fi",
          `exec ${JSON.stringify(real)} "$@"`,
          "",
        ].join("\n")
      );
      chmodSync(join(bin, "docker"), 0o755);
      const env = childEnv(tmp, { variable: pgImageVariable, value: image });
      env.PATH = `${bin}:${env.PATH ?? ""}`;

      const start = await startPool(env);
      expect(start.stdout).toContain(refused);

      // The start returned once Docker listed neither container, and said of each that another
      // start removed it first, with what Docker said, and of neither that it removed it.
      expect([containerExists(inProgress.name), containerExists(goneFirst.name)]).toEqual([
        false,
        false,
      ]);
      const inProgressLines = removalLines(start.stderr, inProgress.id);
      expect(inProgressLines).toHaveLength(1);
      expect(inProgressLines[0]).toContain(
        "another start removed first: Error response from daemon: removal of container"
      );
      expect(inProgressLines[0]).toContain("is already in progress");
      const goneFirstLines = removalLines(start.stderr, goneFirst.id);
      expect(goneFirstLines).toHaveLength(1);
      expect(goneFirstLines[0]).toContain("another start removed first: docker rm printed no id");
      expect(linesWith(start.stderr, "removed container")).toEqual([]);
    } finally {
      await removeAll(
        () => removeIds(made),
        () => rmSync(tmp, { recursive: true, force: true })
      );
    }
  },
  // The row drives two `docker create`s, one start of the pool, waited for at most `childMs`,
  // whose wait for the first container's removal takes a second, and the removals; 900 s holds the
  // start at its bound and a slow Docker daemon.
  900_000
);

/** scripts/e2e/lib/leftovers.sh, through which each shell stack marks its Postgres and removes the
 *  Postgres of each stack that is gone. */
const leftoversLib = fileURLToPath(
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
interface ShellStack {
  readonly child: Child;
  readonly id: string;
}

/** Starts a shell stack with `env` whose container is `name`, and answers once its `docker run`
 *  printed the container's id. */
async function startShellStack(
  env: Record<string, string | undefined>,
  image: string,
  name: string
): Promise<ShellStack> {
  const child = Bun.spawn(["bash", "-c", shellStackScript], {
    env: { ...env, LEFTOVERS_LIB: leftoversLib, SHELL_PG: name, SHELL_IMAGE: image },
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
  const shell: Child = {
    process: child,
    pid: child.pid,
    start: startTimeOf(child.pid),
    output,
    drained,
  };
  const deadline = Date.now() + stackMs;
  while (!output.stdout.includes("\n")) {
    const code = await Promise.race([child.exited, sleep(200).then(() => undefined)]);
    if (code !== undefined)
      throw new Error(`the shell stack ${name} exited ${code}: ${output.stderr}`);
    if (Date.now() > deadline) {
      kill(shell);
      throw new Error(`the shell stack ${name} started no container within ${stackMs / 1000} s`);
    }
  }
  return { child: shell, id: output.stdout.trim() };
}

test.skipIf(skipImage)(
  "image provider: a shell stack's Postgres carries the one mark: a dead one goes at the next start of a shell stack and at the next start of the pool, one whose shell runs stays, and one the start cannot judge stays and is named",
  async () => {
    const named = requirePostgres([pgImageVariable]);
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const env = childEnv(tmp, named);
    const names = {
      shellGone: `legion-e2e-shell-pg-${runTag}-a`,
      poolGone: `legion-e2e-shell-pg-${runTag}-b`,
      runs: `legion-e2e-shell-pg-${runTag}-c`,
      unjudged: `legion-e2e-shell-pg-${runTag}-d`,
    };
    const shells: ShellStack[] = [];
    const made: string[] = [];
    try {
      // Each shell stack that started is recorded before a start that failed is thrown, so the
      // `finally` kills its shell and removes its container.
      const results = await Promise.allSettled([
        startShellStack(env, named.value, names.shellGone),
        startShellStack(env, named.value, names.poolGone),
        startShellStack(env, named.value, names.runs),
      ]);
      for (const result of results) {
        if (result.status === "fulfilled") {
          shells.push(result.value);
          made.push(result.value.id);
        }
      }
      const [shellGone, poolGone, runs] = shells;
      if (shells.length < results.length || !shellGone || !poolGone || !runs) {
        throw results.flatMap((result) => (result.status === "rejected" ? [result.reason] : []))[0];
      }
      // A shell stack's mark of another boot, which no start can judge.
      const work = mkdtempSync(join(tmp, "shell-work-"));
      const mark = {
        owner: owners().otherBoot,
        run: work,
        identity: identityOf(work),
        container: names.unjudged,
      };
      const unjudged = docker(
        "run",
        "--detach",
        "--name",
        names.unjudged,
        "--label",
        `${markLabel}=${JSON.stringify(mark)}`,
        "--env",
        "POSTGRES_HOST_AUTH_METHOD=trust",
        named.value
      ).trim();
      made.push(unjudged);
      const short = (id: string) => id.slice(0, 12);

      // The next start of a shell stack, as each runs it before its own Postgres.
      kill(shellGone.child);
      expect(await exited(shellGone.child, childMs, "the killed shell stack")).toBe(137);
      const shellStart = spawnSync(
        "bash",
        ["-c", `. "${leftoversLib}"; remove_leftover_postgres`],
        {
          env,
          encoding: "utf8",
          timeout: childMs,
        }
      );
      expect(shellStart.status, shellStart.stderr).toBe(0);
      expect(containerExists(names.shellGone)).toBe(false);
      removalLines(shellStart.stderr, short(shellGone.id));
      expect([containerExists(names.runs), containerExists(names.unjudged)]).toEqual([true, true]);
      expect(linesWith(shellStart.stderr, `container ${short(unjudged)} (boot `)).toHaveLength(1);
      expect(shellStart.stderr).not.toContain(short(runs.id));

      // The next start of the pool.
      kill(poolGone.child);
      expect(await exited(poolGone.child, childMs, "the killed shell stack")).toBe(137);
      const poolStart = await startPool(env);
      expect(poolStart.stdout).toContain(refused);
      expect(containerExists(names.poolGone)).toBe(false);
      removalLines(poolStart.stderr, short(poolGone.id));
      expect([containerExists(names.runs), containerExists(names.unjudged)]).toEqual([true, true]);
      expect(linesWith(poolStart.stderr, `container ${short(unjudged)} (boot `)).toHaveLength(1);
      expect(poolStart.stderr).not.toContain(short(runs.id));
    } finally {
      for (const shell of shells) kill(shell.child);
      await removeAll(
        () => removeIds(made),
        () => rmSync(tmp, { recursive: true, force: true })
      );
    }
  },
  // The row drives three shell stacks at once, each a `bun` mark and a container's start, waited
  // for at most `stackMs`, one `docker run`, two kills, one shell start and one start of the pool,
  // each waited for at most `childMs`, and the removals; 2,400 s holds those waits at their
  // bounds.
  2_400_000
);

// A developer's TMPDIR may be spelt through a link of their own, as a scratch disk linked in. The
// image provider's label then names a socket directory whose path holds that link, and both sweeps
// must read it under the one spelling the mark holds (`identityOf`, `markedPaths` of
// e2e/harness-postgres-marks.ts): the killed stack's container goes, and no line says it was left
// for a link. The second row, with TMPDIR spelt plain, shows the row can pass.
for (const spelt of ["through a link", "plain"] as const) {
  test.skipIf(skipImage)(
    `image provider: the next start removes the container of a stack killed hard whose TMPDIR is spelt ${spelt}`,
    async () => {
      const named = requirePostgres([pgImageVariable]);
      // A short base, as a developer's ~/tmp or CI's RUNNER_TEMP is, so the socket directory stays
      // in the run directory (`socketDirectory` of e2e/harness-postgres.ts moves it to /tmp when
      // the socket path would pass 107 bytes, and then no link is in the label's path).
      const real = mkdtempSync(join(shortBase, "ll-"));
      const tmp = spelt === "plain" ? real : join(dirname(real), `${basename(real)}-link`);
      if (spelt !== "plain") symlinkSync(real, tmp);
      const env = childEnv(tmp, named);
      let stack: Stack | undefined;
      try {
        stack = await startStack(env, `link-${runTag}-${spelt === "plain" ? "plain" : "link"}`);
        expect(containersOf(stack.dir)).toHaveLength(1);
        // The precondition of the row: the label's socket directory is in the run directory.
        expect(dirname(dirname(stack.address))).toBe(stack.dir);

        kill(stack.child);
        expect(await exited(stack.child, childMs, "the killed stack")).toBe(137);

        const third = await startPool(env);
        expect(third.stdout).toContain(refused);
        // R3: the stack's owner is dead, and the kernel shows it so; its container must go, and
        // no line of the start says it was left for a link.
        expect({
          containers: containersOf(stack.dir),
          linkLines: linesWith(third.stderr, "is a symbolic link"),
        }).toEqual({ containers: [], linkLines: [] });
      } finally {
        if (stack !== undefined) kill(stack.child);
        await removeAll(
          () => removeLeft(stack, named),
          () => rmSync(real, { recursive: true, force: true }),
          () => tmp !== real && rmSync(tmp, { force: true })
        );
      }
    },
    // The row drives one stack's Postgres start, a container's create and start and a readiness
    // wait of at most `stackMs`, its kill, and one start of the pool, waited for at most
    // `childMs`, which removes the container. It read about 20 s under a load of about 300 on a
    // shared Linux machine, on 2026-11-17; 1,500 s holds both waits at their bounds.
    1_500_000
  );
}
