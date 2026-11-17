/** Each stack's Postgres server, from one of the two providers a caller names.
 *
 *  A stack is a whole harness of its own (`e2e/harness-pool.ts`), and `e2e/seed.ts` truncates its
 *  database before every row, so two stacks cannot share one: the pool starts a server per stack
 *  and hands it to that stack's Dispatch servers and psql as `DATABASE_URL`.
 *
 *  - `DISPATCH_E2E_PG_IMAGE` is a `docker run` of the image digest the caller names: a
 *    developer's path on a machine with Docker and no Postgres binaries, and the `visual` job's
 *    of `.github/workflows/dispatch-browser.yaml`, which runs on the runner with only its
 *    Playwright command in a container, so the binaries of an image it does not run in are no
 *    route for it.
 *  - `DISPATCH_E2E_PG_BIN` is a directory holding `initdb` and `pg_ctl`, which is the `shard`
 *    and `alone` jobs' path and an issue pod's: those two jobs of that workflow run in a
 *    container that carries PostgreSQL 18 with `vector`, where there are Postgres binaries and
 *    no Docker socket, and they name that installation's directory.
 *
 *  Both keep the stack's data under the run's own temporary directory, which the pool makes and
 *  removes (`e2e/harness-pool.ts`), and neither gives the server a TCP port, so no other program
 *  can take a port between its choice and the server's bind: each server listens on no TCP port
 *  (`listen_addresses` empty) and serves on a unix socket alone, in a directory of mode 0700 that
 *  this process's user owns (`socketDirectory`). The image provider mounts that directory into
 *  its container at the image's socket directory and runs the container as this process's user,
 *  so the socket the server makes there is that user's too. That socket answers on the host only
 *  where the container runs on the host's own kernel, which is Linux with a local Docker daemon:
 *  under Docker Desktop the server runs in the desktop's virtual machine, and under a remote
 *  `DOCKER_HOST` the mount is a directory of that other machine (neither one measured). The
 *  server's dev sign-in refuses a `DATABASE_URL` whose host is neither loopback nor a unix socket
 *  (`e2e/run-server.sh`), and a unix socket is one.
 *
 *  Each Postgres is marked with the process that starts it before it can outlive that process: a
 *  file in the run directory, and the image provider's container also as its label
 *  (`e2e/harness-postgres-marks.ts`). A stack killed hard (SIGKILL) runs none of the stops here,
 *  and the next start of a stack removes each Postgres whose mark names a process that is gone.
 *
 *  The binaries provider runs `initdb` once per run and copies the data directory it made per
 *  stack, because `initdb` is the cost that grows with the stacks: measured in an issue pod, from
 *  nothing to every stack answering `select 1`, `initdb` per stack took 3.7-4.1 s at 2 stacks and
 *  7.1-8.3 s at 4, against 4.5-6.0 s and 5.9-6.2 s for one `initdb` and a copy per stack, which
 *  is flat in the stacks because only the copies grow. The template carries the stack's database
 *  and its pgvector extension, which migration 0072 needs, and not the migrations: the server
 *  applies those at boot, as it does today against a fresh database, and a template the same pod
 *  measured with them applied cost 5.7-6.2 s at 2 stacks and 7.7-8.7 s at 4 — more than it saves,
 *  since the 1.0 s of migrations it takes out of a stack's boot runs in every stack at once. */
import { execFile } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { labelOf, markPostgres, shortTemporaryDirectory } from "./harness-postgres-marks";
import { Interrupted } from "./local-instance-support";
import { sqlAt } from "./psql";

const execFileAsync = promisify(execFile);

/** A `docker run` of this image serves one stack. */
export const pgImageVariable = "DISPATCH_E2E_PG_IMAGE";
/** This directory's `initdb` and `pg_ctl` serve every stack. */
export const pgBinVariable = "DISPATCH_E2E_PG_BIN";
/** The connections a stack's server must allow beyond its superuser reserve, as
 *  `connectionsArgs` prints them, given by a caller that cannot run `go`. */
export const pgConnectionsVariable = "DISPATCH_E2E_PG_CONNECTIONS";

/** The database every stack's server holds, named the same in each: the stacks are separate
 *  clusters, and nothing outside a stack reads it. */
const databaseName = "dispatch_e2e";
/** packages/envoy, the Go module `connectionsArgs` runs in. */
const envoyDirectory = fileURLToPath(new URL("../../envoy", import.meta.url));
/** `go` with these prints `testlimits.ServerConnections`
 *  (packages/envoy/internal/dispatch/testlimits): the connections a Dispatch test server must
 *  allow beyond its superuser reserve. */
const connectionsArgs = ["run", "./internal/dispatch/store/cmd/connections"];
/** Postgres's default `superuser_reserved_connections`. The harness does not set it, so each
 *  stack's server keeps this many slots for superusers, and the suite's connections get none. */
const superuserReserved = 3;
/** A stack's connection limit: `testlimits.ServerConnections` plus `superuserReserved`. It comes
 *  from the Go code, so the harness holds no count of a Dispatch process's pool. A server that
 *  allows fewer does not fail where the size is wrong. It fails later, as a 53300 refusal inside
 *  a room, a lease or a socket, and nothing in that failure names the cause. The first stack
 *  that starts reads it, once per run.
 *
 *  `DISPATCH_E2E_PG_CONNECTIONS`, when set, is that number. Every job of
 *  `.github/workflows/dispatch-browser.yaml` that starts stacks here sets it from a step that
 *  runs `connectionsArgs` before its browser step: the `shard` and `alone` jobs run in a
 *  container with no Go, so their `binaries` job runs it, and the `visual` job runs it in a step
 *  of its own, outside its browser step's budget. Unset, which is a developer's or an issue
 *  pod's run, where Go is installed, the harness runs `go` with `connectionsArgs` itself. */
let maxConnections: Promise<number> | undefined;
async function readMaxConnections(env: NodeJS.ProcessEnv): Promise<number> {
  const given = env[pgConnectionsVariable];
  if (given !== undefined) {
    const server = positiveWholeNumber(given);
    if (server === undefined) {
      throw new Error(
        `The Dispatch e2e harness cannot start: ${pgConnectionsVariable}=${JSON.stringify(given)} ` +
          "is not a positive whole number of connections."
      );
    }
    return server + superuserReserved;
  }
  const named = `\`go ${connectionsArgs.join(" ")}\` in ${envoyDirectory}`;
  let stdout: string;
  try {
    ({ stdout } = await execFileAsync("go", connectionsArgs, { cwd: envoyDirectory }));
  } catch (error) {
    throw new Error(
      `The Dispatch e2e harness cannot start: ${named} failed: ` +
        `${error instanceof Error ? error.message : String(error)}`
    );
  }
  const server = positiveWholeNumber(stdout);
  if (server === undefined) {
    throw new Error(
      `The Dispatch e2e harness cannot start: ${named} printed ${JSON.stringify(stdout)}, ` +
        "not a positive whole number of connections."
    );
  }
  return server + superuserReserved;
}
/** `text` as a positive whole number, one optional trailing newline allowed; else undefined. */
function positiveWholeNumber(text: string): number | undefined {
  const printed = /^([1-9][0-9]*)\n?$/.exec(text)?.[1];
  const value = printed === undefined ? Number.NaN : Number(printed);
  return Number.isSafeInteger(value) ? value : undefined;
}
/** A stack's server. Its data dies with the run and its database is 46 MB with a working set of
 *  one scenario's rows, so each setting here gives up something only a database that outlives
 *  its run can use. What each costs if the database were real is on its own line. Measured in an
 *  issue pod over two stacks and 58 rows of `inbox` and `search`, none of them moves the reset's
 *  own median (51.5 ms before, 52.0 ms after): they take work off a stack's server that this
 *  pod's two cores were not short of, and the runner's four are. */
const settings = [
  // Crash safety. Nothing here survives the run, so a cluster that dies loses nothing anyone
  // reads: a real database would lose committed transactions (`synchronous_commit`) or the
  // whole cluster (`fsync`, `full_page_writes`) to a power cut.
  "fsync=off",
  "full_page_writes=off",
  "synchronous_commit=off",
  // The buffer pool, against the image's 128MB. 64MB holds the whole 46 MB stack database, so
  // nothing this cluster reads can be evicted, and four stacks' pools are 256MB smaller. A real
  // database would read its working set back from the operating system on every eviction.
  "shared_buffers=64MB",
  // Expression JIT. Compiling an expression costs a tenth of a second of pure CPU and earns it
  // back only over a long scan; no table here is long enough, so a plan whose estimate passes
  // `jit_above_cost` is an estimate that is wrong. A real database loses that on its analytics.
  "jit=off",
  // Parallel query. At this size no plan is expected to take a worker, and this makes that
  // certain: one gather on one stack is four more processes on a runner whose cores are the
  // bound. A real database loses parallel scans, joins and aggregates.
  "max_parallel_workers_per_gather=0",
  // WAL. `minimal` is the least this cluster can write and still recover its own crash, and it
  // writes none at all for a bulk load into a relation the same transaction made or truncated.
  // It rules out replication and point-in-time recovery, which every real deployment is built
  // on, and so needs `max_wal_senders=0`.
  "wal_level=minimal",
  "max_wal_senders=0",
  // Checkpoints. A stack writes far too little WAL to reach `max_wal_size`, so its only
  // checkpoints are the timed ones - one every five minutes per stack, each writing every dirty
  // buffer - and a timer longer than any run leaves none. A real database would face a recovery
  // that replays an hour of WAL, after a checkpoint that wrote all of it at once.
  "checkpoint_timeout=60min",
  "max_wal_size=4GB",
];
/** Every setting a stack's server starts with: its connection limit, then `settings`. */
async function serverSettings(env: NodeJS.ProcessEnv): Promise<string[]> {
  maxConnections ??= readMaxConnections(env);
  return [`max_connections=${await maxConnections}`, ...settings];
}
/** How long a stack's server may take to answer `select 1`. */
const readyMs = 120_000;

/** One stack's Postgres. */
export interface StackDatabase {
  /** The `DATABASE_URL` this stack's servers and psql use. */
  readonly url: string;
  /** Where it serves, which the harness logs and no other stack shares: the directory of its
   *  unix socket. */
  readonly address: string;
  stop(): Promise<void>;
}

/** The provider a run starts every stack's Postgres from. */
export interface StackPostgres {
  /** What a log line and a refusal call it. */
  readonly name: string;
  /** One stack's server. Everything it leaves behind is under the run's own directory, which the
   *  pool removes (`e2e/harness-pool.ts`), or a container the returned `stop` removes, and is
   *  named by its mark (`e2e/harness-postgres-marks.ts`), which goes with the run's directory.
   *  `signal` is the pool's own stop: a Ctrl-C while this waits for the server to answer ends the
   *  wait as `Interrupted` rather than as a start that failed. */
  start(index: number, signal?: AbortSignal): Promise<StackDatabase>;
}

/** One stack's server, with a stop under way reported as the interruption it is: psql rejects a
 *  statement the signal killed with an abort of its own, and a run a Ctrl-C stopped reads as
 *  interrupted rather than as a Postgres that would not start. */
async function stackOf(
  signal: AbortSignal | undefined,
  started: () => Promise<StackDatabase>
): Promise<StackDatabase> {
  try {
    return await started();
  } catch (error) {
    if (signal?.aborted === true) throw new Interrupted();
    throw error;
  }
}

/** Waits until `url` answers `select 1`: a server that is up has read its data directory, run
 *  its own recovery and opened its port. `deadline`, a `Date.now()` reading, is `readyMs` from
 *  now unless a caller that waited for something else first shares its own. */
async function ready(
  url: string,
  what: string,
  signal?: AbortSignal,
  deadline = Date.now() + readyMs
): Promise<void> {
  for (;;) {
    try {
      await sqlAt(url, ["select 1"], signal);
      return;
    } catch (error) {
      // The psql above was killed by the signal, or the one before it was: a stopping run waits
      // out none of the deadline that is left.
      if (signal?.aborted === true) throw new Interrupted();
      if (Date.now() > deadline) {
        throw new Error(
          `The Dispatch e2e harness cannot start: ${what} did not answer within ` +
            `${readyMs / 1000} s: ${error instanceof Error ? error.message : String(error)}`
        );
      }
      await sleep(200);
    }
  }
}

/** Postgres's socket file in its socket directory, for the port every stack's server keeps, 5432. */
const socketFile = ".s.PGSQL.5432";
/** The longest unix socket path the kernel takes: Linux's `sun_path` holds 108 bytes with the
 *  terminating zero (docs/testing.md, "Unix sockets in Go tests"), and libpq refuses a longer
 *  one. */
const socketPathLimit = 107;
/** The character no socket directory may hold: psql (libpq) and the Dispatch server (pgx) read
 *  the `host` of a `DATABASE_URL` as a list split at it, and Postgres reads `-k`
 *  (`unix_socket_directories`) so too. */
const hostListSeparator = ",";

/** A fresh directory of mode 0700, which this process's user owns (`mkdtemp`), for one server's
 *  unix socket: in `dir`, the run's own directory, when `longest`, the longest path the caller
 *  puts in it, written relative to it, still fits `socketPathLimit` there and `dir` holds no
 *  `hostListSeparator`, and else in /tmp, as on a machine whose `TMPDIR` is long. Only this
 *  process's user and root can reach a socket under it, which is why each server trusts every
 *  connection. The caller removes it. A run killed hard (SIGKILL) removes nothing, and one made
 *  in /tmp is then outside the run's own directory, so the mark of its server names it for the
 *  next start to remove (`e2e/harness-postgres-marks.ts`). */
function socketDirectory(dir: string, name: string, longest: string): string {
  for (const base of [dir, shortTemporaryDirectory]) {
    if (base.includes(hostListSeparator)) continue;
    // mkdtemp appends six characters to the prefix it is given.
    if (Buffer.byteLength(join(base, `${name}-XXXXXX`, longest)) <= socketPathLimit) {
      return mkdtempSync(join(base, `${name}-`));
    }
  }
  throw new Error(
    `The Dispatch e2e harness cannot start: no directory holds a unix socket path of ` +
      `${socketPathLimit} bytes or less, with no "${hostListSeparator}" in it, for ${name}, ` +
      `neither ${dir} nor ${shortTemporaryDirectory}.`
  );
}

/** Starts the cluster in `data`, with its log in `dir` and its one socket a unix socket in
 *  `sockets`: it listens on no TCP port (`listen_addresses` empty), so it takes no port of the
 *  machine, and its `DATABASE_URL` is `socketUrl(sockets, …)`. Returns or throws with no
 *  postmaster of `data` left running. */
async function startCluster(
  bin: string,
  data: string,
  dir: string,
  sockets: string,
  what: string,
  env: NodeJS.ProcessEnv
): Promise<void> {
  const started = await serverSettings(env);
  const options = ["-c listen_addresses=''", `-k ${sockets}`, ...started.map((s) => `-c ${s}`)];
  try {
    await execFileAsync(join(bin, "pg_ctl"), [
      "-D",
      data,
      "-l",
      join(dir, "postgres.log"),
      "-o",
      options.join(" "),
      "-w",
      "start",
    ]);
  } catch (error) {
    // `pg_ctl -w start` gives up its own wait after `PGCTLTIMEOUT` and exits non-zero, but the
    // postmaster it started goes on running and holding `postmaster.pid`: a throw here would
    // leave a server running while the pool removes the data directory under it
    // (`e2e/harness-pool.ts`). A stop that fails is a server that never started, which is why the
    // failure the caller sees stays the start's.
    await execFileAsync(join(bin, "pg_ctl"), ["-D", data, "-m", "immediate", "-w", "stop"]).catch(
      () => {}
    );
    throw new Error(
      `The Dispatch e2e harness cannot start: ${what} did not start (${join(dir, "postgres.log")}): ` +
        `${error instanceof Error ? error.message : String(error)}`
    );
  }
}

/** The `DATABASE_URL` of `database` on the cluster whose unix socket is in `sockets`. */
function socketUrl(sockets: string, database: string): string {
  return `postgres://postgres@/${database}?host=${encodeURIComponent(sockets)}&sslmode=disable`;
}

function binariesProvider(dir: string, bin: string, env: NodeJS.ProcessEnv): StackPostgres {
  for (const tool of ["initdb", "pg_ctl"]) {
    if (!existsSync(join(bin, tool))) {
      throw new Error(
        `The Dispatch e2e harness cannot start: ${pgBinVariable}=${bin} holds no ${tool}. It ` +
          "must name the directory of a Postgres installation, the one holding initdb and pg_ctl."
      );
    }
  }
  /** The one `initdb` of the run, with the stack database and its pgvector extension, copied per
   *  stack. Started once however many stacks ask for it. */
  let template: Promise<string> | undefined;
  const makeTemplate = async (signal?: AbortSignal): Promise<string> => {
    const templateDir = join(dir, "template");
    const data = join(templateDir, "pgdata");
    const sockets = socketDirectory(dir, "pg-template", socketFile);
    // The mark comes before the data directory and the postmaster exist, so a stack killed hard
    // from here on leaves nothing the next start cannot name (`e2e/harness-postgres-marks.ts`).
    markPostgres(dir, "template", { sockets, postmaster: { bin, data } });
    try {
      mkdirSync(templateDir, { recursive: true });
      await execFileAsync(join(bin, "initdb"), [
        "-D",
        data,
        "-U",
        "postgres",
        "--auth=trust",
        "--no-sync",
        "-E",
        "UTF8",
      ]);
      await startCluster(bin, data, templateDir, sockets, "the stacks' Postgres template", env);
    } catch (error) {
      rmSync(sockets, { recursive: true, force: true });
      throw error;
    }
    try {
      const admin = socketUrl(sockets, "postgres");
      await ready(admin, "the stacks' Postgres template", signal);
      await sqlAt(admin, [`create database ${databaseName}`], signal);
      await sqlAt(socketUrl(sockets, databaseName), ["create extension vector"], signal);
    } finally {
      // Whatever happened above: the postmaster is up from `startCluster` on, and a throw that
      // left it running would leave it serving while the pool removes the data directory under it.
      try {
        await execFileAsync(join(bin, "pg_ctl"), ["-D", data, "-m", "fast", "-w", "stop"]);
      } finally {
        rmSync(sockets, { recursive: true, force: true });
      }
    }
    return data;
  };
  return {
    name: `the Postgres binaries in ${bin}`,
    start: (index, signal) =>
      stackOf(signal, async (): Promise<StackDatabase> => {
        template ??= makeTemplate(signal);
        const stackDir = join(dir, `worker-${index}`);
        const data = join(stackDir, "pgdata");
        const what = `stack ${index}'s Postgres`;
        const sockets = socketDirectory(dir, `pg-${index}`, socketFile);
        // Marked before its data directory and its postmaster exist, as the template is.
        markPostgres(dir, `worker-${index}`, { sockets, postmaster: { bin, data } });
        const stop = async (): Promise<void> => {
          try {
            await execFileAsync(join(bin, "pg_ctl"), ["-D", data, "-m", "immediate", "-w", "stop"]);
          } finally {
            rmSync(sockets, { recursive: true, force: true });
          }
        };
        try {
          mkdirSync(stackDir, { recursive: true });
          // `cp -a` keeps the 0700 a data directory's server refuses to start without.
          await execFileAsync("cp", ["-a", await template, data]);
          await startCluster(bin, data, stackDir, sockets, what, env);
        } catch (error) {
          rmSync(sockets, { recursive: true, force: true });
          throw error;
        }
        const url = socketUrl(sockets, databaseName);
        try {
          await ready(url, what, signal);
        } catch (error) {
          // The postmaster is up from `startCluster` on, and a wait the run's signal or the
          // deadline ended returns no `StackDatabase`, so no caller holds a stop for it: the
          // pool would delete this data directory under a server still serving.
          // A stop that fails here is a server that is already gone, which is why the failure
          // the caller sees is the wait's.
          await stop().catch(() => {});
          throw error;
        }
        return { address: sockets, url, stop };
      }),
  };
}

/** The container's own last lines, for a refusal that would otherwise say only that nothing
 *  answered: a server that exits at once leaves its reason there and nowhere else. */
async function containerLog(container: string): Promise<string> {
  try {
    const { stdout, stderr } = await execFileAsync("docker", ["logs", "--tail", "20", container]);
    const lines = `${stdout}${stderr}`.trim();
    return lines === "" ? "it logged nothing" : lines;
  } catch (error) {
    return `its log could not be read: ${error instanceof Error ? error.message : String(error)}`;
  }
}

/** The lines the image's entrypoint (docker-library/postgres's docker-entrypoint.sh) prints just
 *  before it starts the final server: after an initialisation, whose temporary server it has
 *  stopped by then, and where the data directory held a cluster already, which a provider's
 *  fresh container never has but an image of a caller's own may. */
const initialisedLines = [
  "PostgreSQL init process complete; ready for start up.",
  "PostgreSQL Database directory appears to contain a database; Skipping initialization",
];

/** Waits until the log of `container` holds one of `initialisedLines`, and throws naming them
 *  where `deadline`, a `Date.now()` reading, comes first. A fresh cluster's initialisation runs
 *  a temporary server on the same unix socket as the final one, which makes the stack's database
 *  and then stops, so a `select 1` that answers before that line may answer from a server that
 *  is about to go. */
async function initialised(
  container: string,
  what: string,
  signal: AbortSignal | undefined,
  deadline: number
): Promise<void> {
  for (;;) {
    const log = await execFileAsync("docker", ["logs", container], { signal }).then(
      ({ stdout, stderr }) => `${stdout}${stderr}`,
      () => ""
    );
    if (signal?.aborted === true) throw new Interrupted();
    if (initialisedLines.some((line) => log.includes(line))) return;
    if (Date.now() > deadline) {
      throw new Error(
        `The Dispatch e2e harness cannot start: ${what} did not finish the image's ` +
          `initialisation within ${readyMs / 1000} s: its log holds neither ` +
          initialisedLines.map((line) => `"${line}"`).join(" nor ")
      );
    }
    await sleep(200);
  }
}

/** The `--mount` value that binds the host directory `source` at `target` in a container.
 *  Docker reads that value as one CSV record, so `source=` is a quoted field with each `"`
 *  doubled: a `,` would end an unquoted field, and a `"` in one is refused. `--volume
 *  <source>:<target>` is no route, since it splits at each `:`, and a run directory that holds
 *  one failed with `invalid mode: /var/run/postgresql`. */
function bindMount(source: string, target: string): string {
  return `type=bind,"source=${source.replaceAll('"', '""')}",target=${target}`;
}

function imageProvider(
  image: string,
  dir: string,
  run: string,
  env: NodeJS.ProcessEnv
): StackPostgres {
  const uid = process.getuid?.();
  const gid = process.getgid?.();
  if (uid === undefined || gid === undefined) {
    throw new Error(
      `The Dispatch e2e harness cannot start: ${pgImageVariable} runs each container as this ` +
        "process's user, and this platform names no user id."
    );
  }
  return {
    name: `the Postgres image ${image}`,
    start: (index, signal) =>
      stackOf(signal, async (): Promise<StackDatabase> => {
        const what = `stack ${index}'s Postgres`;
        const started = await serverSettings(env);
        const container = `dispatch-e2e-pg-${run}-${index}`;
        // The image's entrypoint makes its socket directory mode 3775 (`chmod 03775
        // /var/run/postgresql` in docker-library/postgres's docker-entrypoint.sh), and through a
        // bind mount that is the host directory's own mode. So the mount is a child of the 0700
        // directory `socketDirectory` makes, which keeps every other user out whatever mode the
        // child is given.
        const own = socketDirectory(dir, `pg-${index}`, join("s", socketFile));
        // The mark comes before anything is in the socket directory and before the container
        // exists, and the `docker run` below sets it as the container's label, so the container
        // is marked from the moment it exists (`e2e/harness-postgres-marks.ts`).
        const mark = markPostgres(dir, `worker-${index}`, { sockets: own, container });
        const sockets = join(own, "s");
        mkdirSync(sockets, { mode: 0o700 });
        const removeSockets = (): void => rmSync(own, { recursive: true, force: true });
        const remove = async (): Promise<void> => {
          await execFileAsync("docker", ["rm", "--force", "--volumes", container]).catch(() => {});
          removeSockets();
        };
        try {
          await execFileAsync("docker", [
            "run",
            "--detach",
            "--name",
            container,
            "--label",
            labelOf(mark),
            // No port is published: the server listens on no TCP port (`listen_addresses` empty,
            // the last setting below) and serves on its unix socket alone, which the mount puts
            // in `sockets`. Docker's allocator would choose a published port, not the kernel,
            // and it fails the run when another program holds the ten ports after its last
            // choice. The container runs as this process's user, so the socket is that user's
            // and so is everything the run removes; the entrypoint takes an unknown user
            // (its nss_wrapper), and the data directory under the image's 1777 volume.
            "--user",
            `${uid}:${gid}`,
            "--mount",
            bindMount(sockets, "/var/run/postgresql"),
            // Trust, as the binaries provider's initdb: only this process's user and root can
            // reach the socket (`socketDirectory`), and the cluster is the stack's own and dies
            // with its container.
            "--env",
            "POSTGRES_HOST_AUTH_METHOD=trust",
            "--env",
            `POSTGRES_DB=${databaseName}`,
            "--env",
            "POSTGRES_INITDB_ARGS=--no-sync",
            // Nothing is mounted at the data directory, so the image places it itself and it
            // dies with the container, which is what a stack's data is meant to do. Two mounts
            // were tried and refused: a bind mount of a directory this process made, which the
            // image's own user may neither write nor chown, and a tmpfs at
            // /var/lib/postgresql/data, which this image's Postgres 18 entrypoint reports as
            // "an unused mount/volume" and then exits, since 18 wants one mount a level up
            // (docker-library/postgres#1259).
            image,
            ...started.flatMap((setting) => ["-c", setting]),
            "-c",
            "listen_addresses=",
          ]);
        } catch (error) {
          const said = await containerLog(container);
          await remove();
          throw new Error(
            `The Dispatch e2e harness cannot start: ${what} did not start from ${image}: ` +
              `${error instanceof Error ? error.message : String(error)}; the container said: ${said}`
          );
        }
        const url = socketUrl(sockets, databaseName);
        try {
          // One deadline for both waits, so the image's initialisation and the final server's
          // start take no more together than `ready` gives a server alone.
          const deadline = Date.now() + readyMs;
          await initialised(container, what, signal, deadline);
          await ready(url, what, signal, deadline);
          // The image carries the extension; the database the image made still has to install it,
          // as migration 0072 needs it.
          await sqlAt(url, ["create extension if not exists vector"], signal);
        } catch (error) {
          // The server inside never answered. Its own last lines are read before it is removed.
          const said = await containerLog(container);
          await remove();
          throw new Error(
            `The Dispatch e2e harness cannot start: ${what} came up from ${image} in ${sockets} ` +
              `and then ${error instanceof Error ? error.message : String(error)}; ` +
              `the container said: ${said}`
          );
        }
        return {
          address: sockets,
          url,
          stop: async () => {
            try {
              await execFileAsync("docker", ["rm", "--force", "--volumes", container]);
            } finally {
              removeSockets();
            }
          },
        };
      }),
  };
}

/** The provider this run starts its stacks' Postgres from, or undefined when the caller named
 *  none, which is a run against the `DATABASE_URL` it was given. */
export function stackPostgres(options: {
  /** The run's own temporary directory, which it removes when it ends. */
  readonly dir: string;
  /** What names this run's containers apart from another run's on one machine. */
  readonly run: string;
  readonly env?: NodeJS.ProcessEnv;
}): StackPostgres | undefined {
  const env = options.env ?? process.env;
  const image = (env[pgImageVariable] ?? "").trim();
  const bin = (env[pgBinVariable] ?? "").trim();
  if (image !== "" && bin !== "") {
    throw new Error(
      `${pgImageVariable} and ${pgBinVariable} both name a Postgres for this run's stacks. Set ` +
        "one of them: the image is a `docker run` per stack, the directory a server per stack " +
        "from its initdb and pg_ctl."
    );
  }
  if (image !== "") return imageProvider(image, options.dir, options.run, env);
  if (bin !== "") return binariesProvider(options.dir, bin, env);
  return undefined;
}
