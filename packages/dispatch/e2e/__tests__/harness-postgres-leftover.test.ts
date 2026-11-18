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
 *  process that is gone, and leaves every other one (e2e/harness-postgres-removal.ts). Many stacks
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
 *  controls are what show those are left.
 *
 *  THE FIXTURES. The children, the stacks and the starts of the pool they run, Docker's reads and
 *  removals, and what the rows plant are in `harness-postgres-leftover-fixtures.ts` beside this
 *  file, which holds no assertion. This file holds the rows and each of their checks. */

import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
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

import {
  pgBinVariable,
  pgImageVariable,
  type StackDatabase,
  stackPostgres,
} from "../harness-postgres";
import {
  identityOf,
  markLabel,
  marksDirectory,
  type Owner,
  runDirectoryPrefix,
  shortTemporaryDirectory as shortBase,
} from "../harness-postgres-marks";
import {
  answers,
  childEnv,
  childMs,
  containerExists,
  containersOf,
  docker,
  exists,
  exited,
  heldSockets,
  kill,
  leaveCluster,
  leaveContainer,
  leaveContainerMark,
  leaveUnlistedContainer,
  leftoversLib,
  owners,
  type PoolStart,
  pidNamespaceRefusal,
  plantedName,
  postmastersOf,
  refused,
  removeAll,
  removeContainersOf,
  removeIds,
  removeImage,
  removeLeft,
  runTag,
  type ShellStack,
  type Stack,
  soleListing,
  stackMs,
  startNamespacedStack,
  startPool,
  startServingPool,
  startShellStack,
  startStack,
  startTimeOf,
  startUnjudged,
  stopPool,
  stopStack,
  type Unjudged,
  volumesOf,
} from "./harness-postgres-leftover-fixtures";
import { type HostileMark, hostileMarkKept, plantHostileMark } from "./planted-marks";
import { requirePostgres, skipsWithoutPostgres } from "./postgres-provider";

const file = "e2e/__tests__/harness-postgres-leftover.test.ts";

const skipImage = skipsWithoutPostgres(file, [pgImageVariable]);
const skipBinaries = skipsWithoutPostgres(file, [pgBinVariable]);

/** The lines of a start's stderr that hold `text`. */
function linesWith(stderr: string, text: string): string[] {
  return stderr.split("\n").filter((line) => line.includes(text));
}

/** The lines of a start's stderr that name the container `container`, by name or id, and say how
 *  its removal ended. Each says that this start removed it, or that another start removed it
 *  first, or, for a container that another start removed, that this start removed the socket
 *  directory it served in, which is the run directory sweep's once Docker lists no such container
 *  (e2e/harness-postgres-removal.ts). At most one says that this start removed the container. A
 *  start names no container another start removed before it listed them. A removal that a shared
 *  daemon takes a minute over has one line more, that the start waits for it, which is not
 *  among them. */
function removalLines(stderr: string, container: string): string[] {
  const lines = linesWith(stderr, container);
  const waits = lines.filter((line) => line.includes(": waits for Docker's removal of container "));
  expect(waits.length, stderr).toBeLessThanOrEqual(1);
  const ended = lines.filter((line) => !waits.includes(line));
  for (const line of ended) {
    expect(line).toMatch(
      /: removed container |another start removed first|its container \S+ is gone, as Docker lists no such container/
    );
  }
  expect(
    ended.filter((line) => line.includes(": removed container ")).length,
    stderr
  ).toBeLessThanOrEqual(1);
  return ended;
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
        // The binaries provider's postmasters of the killed stack, by pid and start: a failed
        // connection reads the same whether the postmaster ended or only its socket directory went.
        const killedPostmasters =
          provider.variable === pgImageVariable ? [] : postmastersOf(killed.dir);
        if (provider.variable !== pgImageVariable)
          expect(killedPostmasters.length).toBeGreaterThan(0);

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
          expect(killedPostmasters.filter(({ pid, start }) => startTimeOf(pid) === start)).toEqual(
            []
          );
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
      for (const dir of dirs) await removeContainersOf(dir);
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
      await removeIds(made);
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
      // 20,000 files in the dead mark's data directory keep the first start that removes it busy
      // long enough for the second start to find the directory still there: the race of the row.
      for (let dir = 0; dir < 200; dir++) {
        mkdirSync(join(gone.data, `d${dir}`));
        for (let file = 0; file < 100; file++) {
          writeFileSync(join(gone.data, `d${dir}`, `f${file}`), "");
        }
      }

      const env = childEnv(tmp, { variable: pgBinVariable, value: bin });
      const starts = await Promise.all([startPool(env), startPool(env)]);

      for (const start of starts) {
        expect(start.stdout).toContain(refused);
        // Each start names the removal that fails on one line, and the dead mark both found at
        // most once, beside a line that says it waits for the other start's claim of a part.
        const told = start.stderr
          .split("\n")
          .filter((line) => !line.includes(": waits for another start's claim of "))
          .join("\n");
        expect(linesWith(told, failing.data)).toHaveLength(1);
        expect(linesWith(told, "is not removed")).toHaveLength(1);
        expect(linesWith(told, gone.data).length).toBeLessThanOrEqual(1);
        expect([runs.data, runs.mark].filter((path) => start.stderr.includes(path))).toEqual([]);
      }
      // Together the two starts say "removed" once of each directory of the dead mark: one start
      // renamed it first and removed it, and the other says that another start removed it. A line
      // names what its start removed in the list between ": removed " and ", which", and what
      // another start removed first after that list, or on a line with no such list.
      for (const path of [gone.data, gone.sockets]) {
        const removed = starts.flatMap((start) =>
          start.stderr
            .split("\n")
            .filter((line) => /: removed (.*?), which /.exec(line)?.[1]?.includes(path) === true)
        );
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
  // The row writes 20,000 files and drives two starts of the pool at once, each a `bun` child that
  // reads three marks and removes what it finds dead. It read 0.6 to 0.9 s on a shared Linux
  // machine under a load of about 300 to 400, on 2026-11-17; 300 s leaves a cold `bun` and a slow
  // disk room.
  300_000
);

// A developer may rename the run directory of a stack killed hard, to keep its logs. Its postmaster
// runs on from the new name, and the marks there name the old one, so no start can trust them: the
// next start leaves that Postgres and names it on a line, whether the new name is one the pool
// gives (its listing of TMPDIR reads it) or not (the kernel shows the postmaster serving from it,
// `servedElsewhere` of e2e/harness-postgres-removal.ts).
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

/** Why this machine refuses a stack a PID namespace of its own, or undefined where it gives one
 *  (`pidNamespaceRefusal`). Read only where the binaries rows run. */
const pidNamespaceRefused = skipBinaries ? undefined : pidNamespaceRefusal(file);

// A stack in a PID namespace of its own (a sandbox, `unshare --pid`) writes a mark whose owner a
// start cannot judge, and its postmaster's `postmaster.pid` holds the pid that namespace gives it,
// while the start's `/proc` shows the same process under another pid. The start finds a run
// directory of another TMPDIR only through `servedElsewhere` (e2e/harness-postgres-removal.ts),
// which compares `postmaster.pid` with the last pid of the process's `NSpid` line, the pid of its
// own namespace. The first row, the stack under the start's TMPDIR, is the control: the start
// reads that run directory by its listing.
for (const where of ["the start's TMPDIR", "a TMPDIR of its own"] as const) {
  test.skipIf(skipBinaries || pidNamespaceRefused !== undefined)(
    `binaries provider: a start leaves the Postgres of a stack that runs in another PID namespace under ${where}, and names it in one line`,
    async () => {
      const named = requirePostgres([pgBinVariable]);
      const startTmp = mkdtempSync(join(shortBase, "pn-start-"));
      const stackTmp =
        where === "a TMPDIR of its own" ? mkdtempSync(join(shortBase, "pn-stack-")) : startTmp;
      let stack: Stack | undefined;
      try {
        // The child is `sudo`'s, so the row stops it by closing its stdin, never by a signal.
        const started = await startNamespacedStack(childEnv(stackTmp, named), `pidns-${runTag}`);
        stack = started;
        expect(started.pidNamespace).not.toBe(readlinkSync("/proc/self/ns/pid"));
        expect(answers(started.url)).toBe(true);

        const start = await startPool(childEnv(startTmp, named));
        expect(start.stdout).toContain(refused);

        // The stack still runs, so its Postgres stays and answers, and exactly one line names it:
        // the line of what the start cannot judge, by its PID namespace.
        expect(answers(started.url)).toBe(true);
        const naming = linesWith(start.stderr, identityOf(started.dir));
        expect({ naming, stderr: start.stderr }).toEqual({
          naming: [
            expect.stringContaining(
              "e2e/harness-postgres-removal.ts: left the Postgres of stacks whose process it cannot see:"
            ),
          ],
          stderr: start.stderr,
        });
        expect(naming[0]).toContain(`(PID namespace ${started.pidNamespace})`);
      } finally {
        if (stack !== undefined) {
          stack.child.process.stdin.end();
          await exited(stack.child, childMs, "the stack in a PID namespace of its own").catch(
            () => {}
          );
        }
        rmSync(startTmp, { recursive: true, force: true });
        if (stackTmp !== startTmp) rmSync(stackTmp, { recursive: true, force: true });
      }
    },
    // One stack's start and readiness wait of at most `stackMs`, one start of the pool waited for
    // at most `childMs`, and the stack's stop, at most `childMs`.
    stackMs + 2 * childMs
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
      await removeIds(made);
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
      await removeIds(made.splice(0));
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
        () => committed && removeImage(copy),
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
    try {
      // The row's two dead containers carry no label that Docker lists: the stand-in `docker`
      // adds their marks to the listing of the start under test alone, so no other start of the
      // machine removes them before that start meets the removals the row stages.
      const inProgress = leaveUnlistedContainer(image, made, run, 0, who.gone);
      const goneFirst = leaveUnlistedContainer(image, made, run, 1, who.gone);
      // Another start's removal, as the start under test meets it: its `docker` refuses each
      // removal of the first container as Docker refuses one while another is in progress, for as
      // long as the real `docker` still lists it, and removes it a second after the first
      // refusal; and it removes the second container itself just before the real `docker rm` of
      // it, which then finds it gone. Every other call is the real `docker`'s.
      const other = join(tmp, "other-removal");
      const { bin } = soleListing(tmp, [inProgress.listing, goneFirst.listing], (real) => [
        `if [ "$1" = rm ] && [[ " $* " == *" ${inProgress.fullId} "* ]] &&`,
        `  [ -n "$(${JSON.stringify(real)} ps --all --quiet --no-trunc --filter id=${inProgress.fullId})" ]; then`,
        `  if mkdir ${JSON.stringify(other)} 2>/dev/null; then`,
        `    ( sleep 1; exec ${JSON.stringify(real)} rm --force --volumes ${inProgress.fullId} >/dev/null 2>&1 ) &`,
        "  fi",
        `  echo "Error response from daemon: removal of container ${inProgress.fullId} is already in progress" >&2`,
        "  exit 1",
        "fi",
        `if [ "$1" = rm ] && [[ " $* " == *" ${goneFirst.fullId} "* ]]; then`,
        `  ${JSON.stringify(real)} rm --force --volumes ${goneFirst.fullId} >/dev/null`,
        "fi",
      ]);
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

// Docker's own removal is the claim of a container: of two `docker rm`s of one container, Docker
// answers one with the removal and the other with a refusal while it is in progress or with no id
// once it is gone, and the start reads that answer (`removeContainer` of
// e2e/harness-postgres-removal.ts).
test.skipIf(skipImage)(
  "image provider: of two starts at once on one dead container, one says it removed it and the other that another start removed it first",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const tmp = mkdtempSync(join(tmpdir(), "leftover-"));
    const run = mkdtempSync(join(tmp, runDirectoryPrefix));
    const made: string[] = [];
    try {
      // The dead container carries no label Docker lists: the stand-in `docker` lists its mark to
      // the two starts under test alone, so no other start of the machine removes it first.
      const gone = leaveUnlistedContainer(image, made, run, 0, owners().gone);
      const env = childEnv(tmp, { variable: pgImageVariable, value: image });
      env.PATH = `${soleListing(tmp, [gone.listing]).bin}:${env.PATH ?? ""}`;

      const starts = await Promise.all([startPool(env), startPool(env)]);

      expect([containerExists(gone.name), existsSync(gone.sockets)]).toEqual([false, false]);
      const lines = starts.flatMap((start) => removalLines(start.stderr, gone.id));
      expect(lines.filter((line) => line.includes(`: removed container ${gone.id}`))).toHaveLength(
        1
      );
      expect(lines.filter((line) => line.includes("another start removed first"))).toHaveLength(1);
    } finally {
      await removeAll(
        () => removeIds(made),
        () => rmSync(tmp, { recursive: true, force: true })
      );
    }
  },
  // The row drives one `docker create` and two starts of the pool at once, each waited for at most
  // `childMs`, and the removal; 900 s holds the two starts at their bound and a slow Docker daemon.
  900_000
);

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
      // The precondition: each shell stack's container exists under the id its `docker run`
      // printed, so its absence after a start is that start's removal.
      expect(
        shells.map((each) => /^[0-9a-f]{64}$/.test(each.id) && exists("container", each.id))
      ).toEqual([true, true, true]);
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

// A container's mark is what names its socket directory to the next start once the container is
// gone, so a start that removed the container keeps the mark while it could not remove that
// directory (`sweepRun` of e2e/harness-postgres-removal.ts). Here the directory holds a directory
// of mode 0500 with a file in it, which no removal of this user's can empty.
test.skipIf(skipImage)(
  "image provider: a start that removes a dead stack's container and cannot remove its socket directory keeps the mark that names that directory, and says so in one line",
  async () => {
    const named = requirePostgres([pgImageVariable]);
    const tmp = mkdtempSync(join(shortBase, "lk-"));
    const env = childEnv(tmp, named);
    let stack: Stack | undefined;
    let stuck: string | undefined;
    try {
      stack = await startStack(env, `keep-${runTag}`);
      const sockets = dirname(stack.address);
      const mark = join(stack.dir, marksDirectory, "worker-0.json");
      expect([existsSync(mark), containersOf(stack.dir).length]).toEqual([true, 1]);
      stuck = join(sockets, "stuck");
      mkdirSync(stuck);
      writeFileSync(join(stuck, "file"), "");
      chmodSync(stuck, 0o500);

      kill(stack.child);
      expect(await exited(stack.child, childMs, "the killed stack")).toBe(137);

      const third = await startPool(env);
      expect(third.stdout).toContain(refused);
      expect({
        containers: containersOf(stack.dir),
        sockets: existsSync(sockets),
        mark: existsSync(mark),
        named: linesWith(third.stderr, sockets).length,
      }).toEqual({ containers: [], sockets: true, mark: true, named: 1 });
    } finally {
      if (stack !== undefined) kill(stack.child);
      if (stuck !== undefined && existsSync(stuck)) chmodSync(stuck, 0o700);
      await removeAll(
        () => removeLeft(stack, named),
        () => rmSync(tmp, { recursive: true, force: true })
      );
    }
  },
  // As the rows above: one stack's start and readiness wait of at most `stackMs`, its kill, and
  // one start of the pool, waited for at most `childMs`.
  1_500_000
);
