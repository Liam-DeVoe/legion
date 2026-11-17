/** The next start of a stack removes the Postgres of a stack killed hard, and leaves the Postgres
 *  of a stack that still runs (pull request #4236).
 *
 *  Requirement R3 of that pull request: "The next start of a test stack removes each Postgres
 *  that a dead stack left. It removes no Postgres of a stack that still runs."
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
 *  start time, and stops itself. In the first row of each provider, two of them start one stack's
 *  Postgres each, as the pool starts each stack's, in a run directory named as the pool names its
 *  own, under a `TMPDIR` of the row's own. The other rows write marks of their own instead, each
 *  naming directories or a container the row made, with owners built from this process. The start
 *  every row reads is `startStacks` itself, given no provider: it removes what it finds, then
 *  refuses to start two stacks with no Postgres, which is how a row runs the pool's start without
 *  the minutes of a whole harness. A SIGKILL goes only to a child this file started, while
 *  `/proc` still names that process.
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
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import type { Subprocess } from "bun";

import {
  pgBinVariable,
  pgConnectionsVariable,
  pgImageVariable,
  type StackDatabase,
  stackPostgres,
} from "../harness-postgres";
import {
  markLabel,
  marksDirectory,
  type Owner,
  runDirectoryPrefix,
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

/** How long a one-shot child of this file, such as a start of the pool, may take before the row
 *  kills it and fails. */
const childMs = 180_000;
/** How long a stack may take to print where its Postgres serves: the provider's own readiness
 *  wait gives up at 120 s, and an initdb under load comes before it. */
const stackMs = 300_000;

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

/** Whether a container of this name exists, running or not. */
function containerExists(name: string): boolean {
  return spawnSync("docker", ["container", "inspect", name], { timeout: 120_000 }).status === 0;
}

/** Stops `stack` the way its own child does, by closing its stdin, and where that did not end it
 *  in time, kills it. */
async function stopStack(stack: Stack | undefined): Promise<void> {
  if (stack === undefined) return;
  stack.child.process.stdin.end();
  await exited(stack.child, childMs, `the stack in ${stack.dir}`).catch(() => {});
}

/** Removes each container whose mark names `run`, the run directory of a stack this row started,
 *  by the id Docker lists it under: a container's name is no one stack's alone. */
function removeContainersOf(run: string): void {
  const listed = docker(
    "ps",
    "--all",
    "--no-trunc",
    "--filter",
    `label=${markLabel}`,
    "--format",
    `{{.ID}} {{.Label "${markLabel}"}}`
  );
  for (const line of listed.split("\n")) {
    const space = line.indexOf(" ");
    if (space === -1) continue;
    let mark: unknown;
    try {
      mark = JSON.parse(line.slice(space + 1));
    } catch {
      continue;
    }
    if (typeof mark === "object" && mark !== null && "run" in mark && mark.run === run) {
      docker("rm", "--force", "--volumes", line.slice(0, space));
    }
  }
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
      const runs = { killed: `leftover-${process.pid}-a`, kept: `leftover-${process.pid}-b` };
      const containers = {
        killed: `dispatch-e2e-pg-${runs.killed}-0`,
        kept: `dispatch-e2e-pg-${runs.kept}-0`,
      };
      let killed: Stack | undefined;
      let kept: Stack | undefined;
      let hostile: HostileMark | undefined;
      try {
        // Two marks beside the stacks, in a run directory of the row's TMPDIR named as the pool
        // names one: another boot's, which the start cannot judge, and one that names a program
        // of the planter's and a directory outside its run.
        const beside = mkdtempSync(join(tmp, runDirectoryPrefix));
        hostile = plantHostileMark(beside, tmp, owners().gone);
        const unjudged = leaveCluster(
          beside,
          1,
          mkdtempSync(join(tmp, "no-bin-")),
          owners().otherBoot
        );
        [killed, kept] = await Promise.all([
          startStack(env, runs.killed),
          startStack(env, runs.kept),
        ]);
        expect([answers(killed.url), answers(kept.url)]).toEqual([true, true]);

        kill(killed.child);
        expect(await exited(killed.child, childMs, "the killed stack")).toBe(137);
        // The kill ran no stop: the killed stack's Postgres still serves.
        expect(answers(killed.url)).toBe(true);

        const third = await startPool(env);
        expect(third.stdout).toContain(refused);

        // The killed stack's Postgres is gone, with its data and its socket directory.
        expect(answers(killed.url)).toBe(false);
        if (provider.variable === pgImageVariable) {
          expect(containerExists(containers.killed)).toBe(false);
          expect(existsSync(dirname(killed.address))).toBe(false);
        } else {
          expect(existsSync(join(killed.dir, "worker-0", "pgdata"))).toBe(false);
          expect(existsSync(join(killed.dir, "template", "pgdata"))).toBe(false);
          expect(existsSync(killed.address)).toBe(false);
        }
        // The running stack's Postgres still serves, with its data and its socket directory.
        expect(answers(kept.url)).toBe(true);
        if (provider.variable === pgImageVariable) {
          expect(containerExists(containers.kept)).toBe(true);
        } else {
          expect(existsSync(join(kept.dir, "worker-0", "pgdata"))).toBe(true);
        }
        expect(existsSync(kept.address)).toBe(true);
        // The mark the start cannot judge stays with its directories, and the start names it in
        // one line; the start ran no program that a mark names.
        expect(unjudged.left()).toEqual([true, true, true]);
        expect(linesWith(third.stderr, `${unjudged.mark} (boot `)).toHaveLength(1);
        expect(hostile.kept()).toEqual(hostileMarkKept);
      } finally {
        if (killed !== undefined) kill(killed.child);
        hostile?.live.stop();
        await stopStack(kept);
        removeLeft(killed, named);
        removeLeft(kept, named);
        rmSync(tmp, { recursive: true, force: true });
      }
    },
    // The row drives three stacks it starts and stops itself: two Postgres starts at once, each
    // an initdb or a container's create and start, and a readiness wait that gives up at 120 s,
    // then the third start, which stops a postmaster or removes a container, and the stops. On a
    // shared Linux machine under a load of about 300, on 2026-11-17, the image row read 4.4 to
    // 10.6 s and the binaries row 2.1 to 3.0 s; 600 s leaves both 120 s waits room under a
    // heavier load.
    600_000
  );
}

/** The owners a row writes into marks, each built from this process: this process, which runs;
 *  its pid with another start, which is a process that took over the pid of a gone owner; another
 *  boot; another PID namespace; and none, which is a stack's where there is no /proc. */
function owners() {
  const runs = stackOwner();
  if (runs === null) throw new Error("this machine has no /proc, so no start judges an owner");
  return {
    runs,
    gone: { ...runs, start: "0" },
    otherBoot: { ...runs, boot: "00000000-0000-0000-0000-000000000000" },
    otherNamespace: { ...runs, pidNamespace: "pid:[1]" },
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
    options.text ?? JSON.stringify({ owner, run, sockets, postmaster: { bin, data } })
  );
  return { data, sockets, mark, left: () => [data, sockets, mark].map(existsSync) };
}

/** A container an image provider's stack left, made and never started, under a name of this
 *  row's process: a start judges it by its label alone, which is its mark unless `label` names
 *  another. `made` collects the id Docker made it under, for the row's removal. */
function leaveContainer(
  image: string,
  made: string[],
  run: string,
  index: number,
  owner: Owner | null,
  sockets: string,
  label?: string
) {
  const name = `dispatch-e2e-pg-leftover-${process.pid}-${index}`;
  const mark = label ?? JSON.stringify({ owner, run, sockets, container: name });
  const id = docker("create", "--name", name, "--label", `${markLabel}=${mark}`, image).trim();
  made.push(id);
  return { name, id: id.slice(0, 12), sockets };
}

/** Removes every container a row made, by the ids Docker made them under. */
function removeMade(made: readonly string[]): void {
  for (const id of made) {
    if (containerExists(id)) docker("rm", "--force", "--volumes", id);
  }
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
  "binaries provider: a start leaves the mark of a stack that runs, of another boot or PID namespace or of no /proc, and a mark it cannot read, and names each one it left",
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
      // Another user's run directory reads as this one does: its mode keeps this process out.
      mkdirSync(join(closed, marksDirectory));
      chmodSync(closed, 0o000);

      const start = await startPool(childEnv(tmp, { variable: pgBinVariable, value: bin }));
      expect(start.stdout).toContain(refused);

      // The one mark whose stack is gone goes, with the directories it names.
      expect(gone.left()).toEqual([false, false, false]);
      // Every other one stays, with its directories.
      for (const kept of [runs, otherBoot, otherNamespace, noProc, notAMark, outside]) {
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
      expect(linesWith(start.stderr, `removed the postmaster of ${gone.data}`)).toHaveLength(1);
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
    const run = `leftover-${process.pid}-shared`;
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
  "image provider: a start leaves the container of a stack that runs, of another boot or PID namespace or of no /proc, and one whose label it cannot read, and names each one it left",
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

      const start = await startPool(childEnv(tmp, { variable: pgImageVariable, value: image }));
      expect(start.stdout).toContain(refused);

      // The one container whose stack is gone goes, with its socket directory.
      expect([containerExists(gone.name), existsSync(gone.sockets)]).toEqual([false, false]);
      // Every other one stays, with its socket directory.
      for (const kept of [runs, otherBoot, otherNamespace, noProc, notAMark]) {
        expect({
          name: kept.name,
          left: [containerExists(kept.name), existsSync(kept.sockets)],
        }).toEqual({ name: kept.name, left: [true, true] });
      }
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
      expect(linesWith(start.stderr, `removed container ${gone.id}`)).toHaveLength(1);
      expect(start.stderr).not.toContain(runs.id);
    } finally {
      removeMade(made);
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
        // Each start names each dead container at most once: the removal that failed, or the
        // `docker rm` of a container the other start removed first.
        expect(linesWith(start.stderr, failing.id).length).toBeLessThanOrEqual(1);
        expect(linesWith(start.stderr, gone.id).length).toBeLessThanOrEqual(1);
        expect(start.stderr).not.toContain(runs.id);
      }
      // The start that removed the container names the socket directory it could not remove.
      const failed = starts.flatMap((start) => linesWith(start.stderr, failing.id));
      expect(failed.some((line) => line.includes(`is not removed: ${held}`))).toBe(true);
      expect([containerExists(failing.name), existsSync(failing.sockets)]).toEqual([false, true]);
      expect([containerExists(gone.name), existsSync(gone.sockets)]).toEqual([false, false]);
      expect([containerExists(runs.name), existsSync(runs.sockets)]).toEqual([true, true]);
    } finally {
      removeMade(made);
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
