/** What is left of a harness whose parent dies without stopping it: nothing.
 *
 *  Every listener the harness starts is the leader of a process group of its own
 *  (`Supervisor`, e2e/local-instance-support.ts), so a stop reaches it and what it starts in turn.
 *  A parent that is killed - SIGKILL, an out-of-memory kill, a crashed runner - runs no stop, and
 *  before this the fakes outlived it: fake-envoy, fake-github, fake-broker, the plain-HTTP proxy
 *  and the dev OIDC issuer were found running hours after the run that started them, holding
 *  memory on a shared machine. So was each stack's Postgres, a container or a cluster the run
 *  started outside any process group of its own. Each row starts the real thing in a parent
 *  process of its own, kills that parent with SIGKILL, and reads what it started: none of it may
 *  survive.
 *
 *  The listeners' harness is a deployed run's (`PLAYWRIGHT_BASE_URL`), which starts the fake Envoy
 *  and the plain-HTTP proxy and no Dispatch server, so the row needs no database and no Go build.
 *  Each is started by `startHarness` exactly as a local run starts it. The Postgres row starts one
 *  stack's server from the provider this run names (`DISPATCH_E2E_PG_IMAGE` or
 *  `DISPATCH_E2E_PG_BIN`, e2e/harness-postgres.ts), as the pool starts each stack's. */
import { afterEach, expect, test } from "bun:test";
import { connect } from "node:net";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

import { harnessVariables, stacksVariable } from "../harness-ports";
import { pgBinVariable, pgConnectionsVariable, pgImageVariable } from "../harness-postgres";

const makeDirectory = temporaryDirectories("harness-orphans");
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const harnessStart = fileURLToPath(new URL("../harness-start.ts", import.meta.url));
const harnessPostgres = fileURLToPath(new URL("../harness-postgres.ts", import.meta.url));

/** A hang guard: a cold `bun` start of two listeners takes a few seconds, and the reaping that
 *  follows the kill is bounded below at 15 s. */
const ROW_MS = 60_000;
/** The Postgres row's hang guard: a container's first start, which pulls nothing here but runs
 *  the image's initdb, and the reaper's removal after the kill. */
const POSTGRES_ROW_MS = 120_000;

/** True while any process of group `pgid` is alive: signal 0 to the group reaches every member. */
function groupAlive(pgid: number): boolean {
  try {
    process.kill(-pgid, 0);
    return true;
  } catch {
    return false;
  }
}

/** Polls `ready` until it holds or `ms` pass: what this waits for is another process's write or
 *  the kernel reaping a killed one, which raise no event here. */
async function eventually(ready: () => boolean | Promise<boolean>, ms: number): Promise<boolean> {
  const deadline = Date.now() + ms;
  while (Date.now() < deadline) {
    if (await ready()) return true;
    await Bun.sleep(50);
  }
  return ready();
}

/** Groups a failing row found alive, killed after it so the row leaks nothing itself. */
const survivors: number[] = [];
afterEach(() => {
  for (const pgid of survivors.splice(0)) {
    try {
      process.kill(-pgid, "SIGKILL");
    } catch {
      // ESRCH: the group is gone.
    }
  }
});

test(
  "a harness whose parent is killed leaves none of its listeners running",
  async () => {
    const pidsFile = join(makeDirectory(), "pids");
    // The parent's environment: this one's, with no port and no stack manifest a lane set, so
    // every listener binds a port the OS chooses, and a deployed run's base URL, which nothing
    // here dials.
    const env: Record<string, string | undefined> = {
      ...process.env,
      PLAYWRIGHT_BASE_URL: "http://127.0.0.1:9",
      [stacksVariable]: undefined,
    };
    for (const variable of Object.values(harnessVariables)) env[variable] = undefined;
    const parent = Bun.spawn(
      [
        "bun",
        "-e",
        `import { writeFileSync } from "node:fs";
         import { startHarness } from ${JSON.stringify(harnessStart)};
         const harness = await startHarness();
         writeFileSync(${JSON.stringify(pidsFile)},
           harness.processes.map((started) => started.child.pid).join("\\n") + "\\n");
         setInterval(() => {}, 1 << 30);`,
      ],
      { cwd: dispatchPackage, env, stderr: "ignore", stdout: "ignore" }
    );
    const written = await eventually(
      async () =>
        /\n$/u.test(
          await Bun.file(pidsFile)
            .text()
            .catch(() => "")
        ),
      ROW_MS / 2
    );
    if (!written) {
      parent.kill("SIGKILL");
      throw new Error("the parent started no harness: it wrote no pids");
    }
    const pids = (await Bun.file(pidsFile).text()).trim().split("\n").map(Number);
    survivors.push(...pids);
    expect(pids.length).toBe(2);
    expect(pids.every(groupAlive)).toBe(true);

    parent.kill("SIGKILL");
    await parent.exited;

    await eventually(() => !pids.some(groupAlive), 15_000);
    expect(pids.filter(groupAlive)).toEqual([]);
  },
  ROW_MS
);

// The binaries provider's clusters are no process group and no container: the reaper stops each
// with its own `pg_ctl` and data directory, and leaves alone one its starter already stopped and
// took back. A `pg_ctl` that records how it was run stands in for the real one, so this row
// holds on any machine, Postgres binaries or none; the Postgres row above runs the real server.
test(
  "a reaper whose starter is killed stops each cluster it still holds, and none it gave back",
  async () => {
    const dir = makeDirectory();
    const calls = join(dir, "calls");
    const pgCtl = join(dir, "pg_ctl");
    await Bun.write(
      pgCtl,
      `#!/usr/bin/env bash\nprintf '%s\\n' "$*" >> ${JSON.stringify(calls)}\n`
    );
    await Bun.spawn(["chmod", "+x", pgCtl]).exited;
    const ready = join(dir, "ready");
    const reaper = fileURLToPath(new URL("../local-instance-support.ts", import.meta.url));
    const parent = Bun.spawn(
      [
        "bun",
        "-e",
        `import { writeFileSync } from "node:fs";
         import { Reaper } from ${JSON.stringify(reaper)};
         const reaper = new Reaper();
         reaper.watch({ cluster: { pgCtl: ${JSON.stringify(pgCtl)}, data: "/data/held" } });
         reaper.watch({ cluster: { pgCtl: ${JSON.stringify(pgCtl)}, data: "/data/given back" } });
         reaper.forget({ cluster: { pgCtl: ${JSON.stringify(pgCtl)}, data: "/data/given back" } });
         writeFileSync(${JSON.stringify(ready)}, "ready\\n");
         setInterval(() => {}, 1 << 30);`,
      ],
      { stderr: "inherit", stdout: "ignore" }
    );
    try {
      expect(await eventually(async () => await Bun.file(ready).exists(), ROW_MS / 2)).toBe(true);
      parent.kill("SIGKILL");
      await parent.exited;
      await eventually(async () => await Bun.file(calls).exists(), 15_000);
      expect((await Bun.file(calls).text()).trim().split("\n")).toEqual([
        "-D /data/held -m immediate -w stop",
      ]);
    } finally {
      parent.kill("SIGKILL");
    }
  },
  ROW_MS
);

/** Whether anything accepts a TCP connection on `port` of this machine's loopback. */
function serving(port: number): Promise<boolean> {
  const { promise, resolve } = Promise.withResolvers<boolean>();
  const socket = connect({ host: "127.0.0.1", port });
  socket.once("connect", () => {
    socket.destroy();
    resolve(true);
  });
  socket.once("error", () => resolve(false));
  return promise;
}

// A stack's Postgres needs the provider a run of the browser suite names; without one this row
// would prove nothing and is skipped, saying so, as e2e/__tests__/harness-stacks.test.ts skips the
// case that needs a database.
const provider =
  (process.env[pgImageVariable] ?? "").trim() !== "" ||
  (process.env[pgBinVariable] ?? "").trim() !== "";
if (!provider) {
  console.warn(
    `Skipped the test in ${import.meta.path} that needs a stack's Postgres. This run names ` +
      `neither ${pgImageVariable} nor ${pgBinVariable}. Name one and run the tests again.`
  );
}

test.skipIf(!provider)(
  "a stack's Postgres whose parent is killed serves no longer",
  async () => {
    const dir = makeDirectory();
    const portFile = join(dir, "port");
    // The connection limit is given rather than read through `go`, which this row has no need of.
    const env = { ...process.env, [pgConnectionsVariable]: "20" };
    const parent = Bun.spawn(
      [
        "bun",
        "-e",
        `import { writeFileSync } from "node:fs";
         import { stackPostgres } from ${JSON.stringify(harnessPostgres)};
         const provider = stackPostgres({ dir: ${JSON.stringify(dir)}, run: "orphans-" + process.pid });
         const stack = await provider.start(0);
         writeFileSync(${JSON.stringify(portFile)}, stack.port + "\\n");
         setInterval(() => {}, 1 << 30);`,
      ],
      { cwd: dispatchPackage, env, stderr: "inherit", stdout: "ignore" }
    );
    try {
      const written = await eventually(
        async () =>
          /\n$/u.test(
            await Bun.file(portFile)
              .text()
              .catch(() => "")
          ),
        POSTGRES_ROW_MS / 2
      );
      if (!written) throw new Error("the parent started no Postgres: it wrote no port");
      const port = Number((await Bun.file(portFile).text()).trim());
      expect(await serving(port)).toBe(true);

      parent.kill("SIGKILL");
      await parent.exited;

      await eventually(async () => !(await serving(port)), 30_000);
      expect(await serving(port)).toBe(false);
    } finally {
      parent.kill("SIGKILL");
      // What a failing row leaves is removed here by the exact name or directory this run's
      // provider gave its one stack (e2e/harness-postgres.ts), so the row leaks nothing itself.
      const bin = (process.env[pgBinVariable] ?? "").trim();
      const remove =
        bin === ""
          ? ["docker", "rm", "--force", "--volumes", `dispatch-e2e-pg-orphans-${parent.pid}-0-1`]
          : [join(bin, "pg_ctl"), "-D", join(dir, "worker-0", "pgdata"), "-m", "immediate", "stop"];
      await Bun.spawn(remove, { stderr: "ignore", stdout: "ignore" }).exited;
    }
  },
  POSTGRES_ROW_MS
);
