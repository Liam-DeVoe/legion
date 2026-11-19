/** The fixtures of the rows of `harness-postgres-leftover.test.ts`: the children those rows start
 *  and stop, the stacks and the starts of the pool that the children run, Docker's reads and the
 *  removals of what a row made, and the marks, directories and containers that a row plants.
 *  `bun test` collects only `*.test.ts` and its kin, so this module runs nowhere of its own. It
 *  holds no assertion: each check of a row is in the test file.
 *
 *  A child is a process that `launch` starts, records by its pid and its start time, and collects
 *  the output of. A SIGKILL goes only to such a child, while `/proc` still names that process.
 *  Each name a row gives is of this run (`runTag`), and each container a row made is removed by
 *  the id Docker made it under. */

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
import {
  pgBinVariable,
  pgConnectionsVariable,
  pgImageVariable,
  socketDirectory,
  socketFile,
} from "../harness-postgres";
import { machine, removeContainer } from "../harness-postgres-container-removal";
import {
  identityOf,
  markLabel,
  marksDirectory,
  type Owner,
  runDirectoryPrefix,
  stackOwner,
  startOf,
} from "../harness-postgres-marks";
import type { Row } from "./harness-postgres-removal-fixtures";
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

/** How long a one-shot child of this file, such as a start of the pool, may take before the row
 *  kills it and fails: a stuck child's bound, at least three times the largest reading. A start
 *  of the pool read 0.04 to 0.09 s under a load of about 300, and more than 180 s under a load of
 *  1,217, on one shared Linux machine on 2026-11-17. */
export const childMs = 600_000;
/** How long a stack may take to print where its Postgres serves: the provider's own readiness
 *  wait gives up at 120 s, and an initdb or a container's start under load comes before it; at
 *  least three times the largest reading, as `childMs` is. */
export const stackMs = 600_000;

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

/** Waits for `child` to exit, and kills it and throws where it has not within `ms`. */
export async function exited(child: Child, ms: number, what: string): Promise<number> {
  const code = await Promise.race([child.process.exited, sleep(ms).then(() => undefined)]);
  if (code !== undefined) return code;
  kill(child);
  throw new Error(`${what} did not end within ${ms / 1000} s: ${child.output.stderr}`);
}

/** Waits for the first line `child` prints on its stdout, and answers it. Throws, naming `what`
 *  and what the child wrote on stderr, where the child exits before it printed one, and kills the
 *  child and throws where it has printed none within `ms`. */
async function firstLine(child: Child, ms: number, what: string): Promise<string> {
  const deadline = Date.now() + ms;
  while (!child.output.stdout.includes("\n")) {
    const code = await Promise.race([child.process.exited, sleep(200).then(() => undefined)]);
    if (code !== undefined && !child.output.stdout.includes("\n")) {
      await Promise.race([child.drained, sleep(10_000)]);
      throw new Error(`${what} exited ${code} before it served: ${child.output.stderr}`);
    }
    if (Date.now() > deadline) {
      kill(child);
      throw new Error(`${what} did not serve within ${ms / 1000} s: ${child.output.stderr}`);
    }
  }
  return child.output.stdout.split("\n")[0] ?? "";
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

export async function startStack(
  env: Record<string, string | undefined>,
  run: string
): Promise<Stack> {
  const child = spawn(stackScript, { ...env, LEFTOVER_RUN: run });
  const printed = JSON.parse(await firstLine(child, stackMs, `stack ${run}`)) as Omit<
    Stack,
    "child"
  >;
  return { child, ...printed };
}

/** Stops `stack` the way its own child does, by closing its stdin, and where that did not end it
 *  in time, kills it. */
export async function stopStack(stack: Stack | undefined): Promise<void> {
  if (stack === undefined) return;
  stack.child.process.stdin.end();
  await exited(stack.child, childMs, `the stack in ${stack.dir}`).catch(() => {});
}

/** Runs the pool's start in a child with `env`, and answers its pid and what it printed. */
export async function startPool(
  env: Record<string, string | undefined>
): Promise<{ readonly pid: number; readonly stdout: string; readonly stderr: string }> {
  const child = spawn(startScript, env);
  child.process.stdin.end();
  const code = await exited(child, childMs, "a start of the pool");
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

/** Starts the pool in a child with `env` and its provider, and answers once its stack serves. */
export async function startServingPool(
  env: Record<string, string | undefined>
): Promise<PoolStart> {
  const own: Record<string, string | undefined> = {
    ...env,
    PLAYWRIGHT_BASE_URL: "http://127.0.0.1:9",
  };
  // A run of one stack against a database the caller named starts no Postgres.
  delete own.DATABASE_URL;
  const child = spawn(poolScript, own);
  const line = await firstLine(child, childMs, "a start of the pool");
  const { url } = JSON.parse(line) as { url?: string };
  if (url === undefined) throw new Error(`a start of the pool named no database: ${line}`);
  return { child, url };
}

/** Stops a start of the pool by closing its stdin, which runs its `stopStacks`, and kills it
 *  where that did not end it in time. */
export async function stopPool(start: PoolStart | undefined): Promise<void> {
  if (start === undefined) return;
  start.child.process.stdin.end();
  await exited(start.child, childMs, "a start of the pool").catch(() => {});
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

/** Starts a stack as `startStack` does, with `env` and the run name `run`, in a PID namespace of
 *  its own (`unshare --pid`), which it enters through `sudo -n` and leaves for this user again
 *  (`setpriv`). The child is `sudo`'s. */
export async function startNamespacedStack(
  env: Record<string, string | undefined>,
  run: string
): Promise<Stack> {
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
    ...(JSON.parse(await firstLine(child, stackMs, `stack ${run}`)) as Omit<Stack, "child">),
  };
}

/** How one call of a row's fixtures of Docker or Postgres ended (`untilDeadline`): its exit status
 *  and what it printed. */
interface Called {
  readonly status: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** Runs `program` with `args`, a call of Docker or Postgres that sets up the row `row`, or reads
 *  it, for as long as the row still has time for: until `by`, the row's deadline but for a removal
 *  of what it made (`Row.cleanup`), on the machine's clock, as a start's removal runs its `docker rm`
 *  (`machine` of `e2e/harness-postgres-container-removal.ts`). It sets no time of its own: a shared
 *  daemon took 133 s over one `docker create` on 2026-11-19 while it answered every other call, and
 *  a fixed bound of 120 s gave that call up, and left the container it made. Answers how the call
 *  ended (`Called`), or "late" where `by` came first, which stopped it. */
async function untilDeadline(
  row: Row,
  program: string,
  args: readonly string[],
  by = row.deadline
): Promise<Called | "late"> {
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
}

/** The one line, and the error, of a fixture call `call` of `row` that ran out the row's
 *  deadline: it names the call, the row's time, and what the removal of what it made did
 *  (`removed`). */
function ranOut(row: Row, call: string, removed = ""): Error {
  const line = `${named}: ${call} did not end within the ${row.ms / 1000} s the row waits in all, so the row stopped it${removed}`;
  console.error(line);
  return new Error(line);
}

/** `psql` of `url` with the one statement `sql`, read within the time `row` has left
 *  (`untilDeadline`): answers how it ended, and throws naming the read where the row's deadline
 *  comes first. */
export async function psql(row: Row, url: string, sql: string): Promise<Called> {
  const read = await untilDeadline(row, "psql", [url, "-XAtc", sql]);
  if (read === "late") throw ranOut(row, `psql ${url} -XAtc '${sql}'`);
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
    if (removal === "late") return `, and the removal of the image ${image} it made did not end`;
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
  if (listed === "late" || listed.status !== 0) {
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
  if (ended === "late") throw ranOut(row, call, await removeMade(row, args));
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
  if (read === "late") throw ranOut(row, call);
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
  if (listed === "late") throw ranOut(row, `docker ${args.join(" ")}`);
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

/** Runs a `docker` removal, of a container or an image, for as long as Docker takes over it, and
 *  answers its exit status and what it wrote on standard error. A daemon that many sessions share
 *  can take minutes over one removal while it answers every other call at once (`dockerMs` of
 *  e2e/harness-postgres-container-removal.ts), so a removal has no bound of its own: the bound of the row
 *  that runs it is what ends one that hangs. */
async function dockerRemoval(args: readonly string[]): Promise<{ status: number; said: string }> {
  const run = Bun.spawn(["docker", ...args], { stdout: "ignore", stderr: "pipe" });
  const [status, stderr] = await Promise.all([run.exited, new Response(run.stderr).text()]);
  return { status, said: stderr.trim() || `exit ${status}` };
}

/** Removes each container of `ids`, by its id, and once it has tried them all throws naming each
 *  removal that failed, with what Docker said: one failed removal keeps no other container. A
 *  container that is gone already is no failure (`docker rm --force`), nor is one that another
 *  start of the machine is removing: while that removal is in progress Docker refuses this one,
 *  and the row asks again until Docker removes it or finds it gone, as a start does. */
export async function removeIds(ids: readonly string[]): Promise<void> {
  const failed: string[] = [];
  for (const id of ids) {
    for (;;) {
      const { status, said } = await dockerRemoval(["rm", "--force", "--volumes", id]);
      if (status === 0) break;
      if (!/removal of container \S+ is already in progress/.test(said)) {
        failed.push(`${id}: ${said}`);
        break;
      }
      await sleep(200);
    }
  }
  if (failed.length > 0)
    throw new Error(`docker rm --force --volumes failed: ${failed.join("; ")}`);
}

/** Removes the image `name`, which a row made, however long Docker takes over it. */
export async function removeImage(name: string): Promise<void> {
  const { status, said } = await dockerRemoval(["image", "rm", "--force", name]);
  if (status !== 0) throw new Error(`docker image rm --force ${name}: ${said}`);
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
  if (stopped === "late") throw ranOut(row, `${join(bin, "pg_ctl")} ${args.join(" ")}`);
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
 *  and Postgres take the time the row has left (`docker`). */
export async function startUnjudged(
  row: Row,
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
        await removeLeft(row, stack, provider);
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
    if (logs === "late") throw ranOut(row, `docker logs ${id}`);
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
  try {
    while (!((await initialised()) && (await answers(row, url)))) {
      if (Date.now() > deadline) {
        throw new Error(`the container ${name} did not serve within ${stackMs / 1000} s`);
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

/** Starts a shell stack with `env` whose container is `name`, and answers once its `docker run`
 *  printed the container's id. */
export async function startShellStack(
  env: Record<string, string | undefined>,
  image: string,
  name: string
): Promise<ShellStack> {
  const child = launch(["bash", "-c", shellStackScript], {
    ...env,
    LEFTOVERS_LIB: leftoversLib,
    SHELL_PG: name,
    SHELL_IMAGE: image,
  });
  return { child, id: (await firstLine(child, stackMs, `the shell stack ${name}`)).trim() };
}
