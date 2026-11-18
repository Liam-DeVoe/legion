/** Rows of the check `v-no-leftover-postgres` (requirement R3 of pull request #4236): "The next
 *  start of a test stack removes each Postgres of a stack that it can show dead. It removes no
 *  Postgres of a stack that still runs. It leaves a Postgres whose stack it cannot judge, and it
 *  names that Postgres in one line." The rows of `harness-postgres-leftover.test.ts` start real
 *  stacks under each provider; these plant marks and stand-in processes and need no Postgres.
 *
 *  A start reads the marks of every run directory of `TMPDIR`, which is `/tmp` by default, and of
 *  every run directory the kernel shows a postmaster of its user serving from, and acts as the user
 *  who starts the stack. So the first rows plant what a hostile mark can hold and require that the
 *  sweep leaves it, names it on one line, and acts on none of it: the program the mark names never
 *  runs (it would write a marker file), the live process its `postmaster.pid` names keeps running,
 *  and the directory a link leads to stays. The next rows pin the line of each case the start
 *  cannot judge, and the run directory of another `TMPDIR`: a dead stack's is removed, a live
 *  stack's is left without a word, and a postmaster that no mark names is left and named.
 *
 *  The rows run the start's own sweep, `removeLeftoverPostgres`, in a child of this file, under a
 *  `TMPDIR` of the row's own and a `PATH` that holds no `docker`, so the sweep reads no container
 *  of another run on the machine; two run a start's removal of one container (`removeContainer`)
 *  in a child instead, with a stand-in `docker` that runs no real one. They need Linux (`/proc`).
 *  A mark another user wrote needs a second user, which a test run has none of, so the rows of
 *  `whyUntrusted` (`harness-postgres-marks.test.ts`) read owners from a stubbed `lstat`; these
 *  rows plant what one user can: a link, a program, a directory others may write. A stand-in
 *  process is one this file starts and records by its pid and start, and stops only while `/proc`
 *  still names it; a stand-in postmaster is a copy of `sleep` that serves from the data directory
 *  its `postmaster.pid` names.
 *
 *  ANOTHER START BESIDE A ROW. A dead stack that a row plants is one every start must remove, and
 *  any start of this user may run beside the row: another session's on the machine, or
 *  `harness-stacks.test.ts`'s in the same `bun test --parallel` run. So each row plants its
 *  stacks under a TMPDIR of its own, which no other start lists, and its stand-in postmasters run
 *  a program that is not named `postgres` (`Seen`), which no other start's walk of the kernel's
 *  processes reads: only the row's own starts find them. The rows of a run directory of another
 *  TMPDIR, which a start finds by that walk alone, plant a `postgres` that every start finds, and
 *  so decide on what any start did with the plant (`removedByAStart`). */

import { expect, test } from "bun:test";
import {
  chmodSync,
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
import { basename, dirname, join } from "node:path";
import type { Subprocess } from "bun";

import * as marks from "../harness-postgres-marks";
import { servingPostmasters, stopPostmaster } from "../harness-postgres-removal";
import {
  claimAndDie,
  claimEventMs,
  claimInChild,
  endChild,
  holding,
  leaveContainerMark,
  leaveData,
  leaveElsewhere,
  owners,
  removeInChild,
  scratch,
  signalCountingPostmaster,
  standInDocker,
  standInPostmaster,
  startSweep,
  sweep,
  until,
  writeMark,
} from "./harness-postgres-removal-fixtures";
import {
  hostileMarkKept,
  plantedBin,
  plantHostileMark,
  type StandIn,
  standIn,
} from "./planted-marks";

const { runDirectoryPrefix } = marks;

const onLinux = process.platform === "linux";

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a mark whose postmaster.bin is the planter's and whose stack directory is a link out of the run is left: its program never runs, the live process stays, the directory outside stays",
  async () => {
    const { tmp, run } = scratch();
    const hostile = plantHostileMark(run, tmp, owners().gone);
    try {
      const lines = await sweep(tmp);

      expect(hostile.kept()).toEqual(hostileMarkKept);
      expect(holding(lines, `${hostile.file} (${hostile.link} is a symbolic link)`)).toHaveLength(
        1
      );
    } finally {
      hostile.live.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: the start runs no program a mark names: a postmaster.pid that names a process which is no postgres of the mark's binaries leaves the mark, and that process runs on",
  async () => {
    const { tmp, run } = scratch();
    const marker = join(tmp, "the-planted-program-ran");
    const data = join(run, "worker-0", "pgdata");
    mkdirSync(data, { recursive: true });
    const live = standIn(data);
    try {
      leaveData(data, live.pid);
      const file = writeMark(run, owners().gone, data, plantedBin(tmp, marker));

      const lines = await sweep(tmp);

      expect(existsSync(marker)).toBe(false);
      expect(live.alive()).toBe(true);
      expect(existsSync(join(data, "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      const left = holding(lines, `names pid ${live.pid}`);
      expect(left).toHaveLength(1);
      expect(left[0]).toContain(data);
      expect(left[0]).toContain("is not removed");
    } finally {
      live.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a link in any component of what a mark names is never followed: a stack directory that links out of the run leaves the directory it leads to",
  async () => {
    const { tmp, run } = scratch();
    const outside = join(tmp, "outside");
    try {
      leaveData(join(outside, "pgdata"));
      symlinkSync(outside, join(run, "worker-0"));
      const noBin = mkdtempSync(join(tmp, "no-bin-"));
      const file = writeMark(run, owners().gone, join(run, "worker-0", "pgdata"), noBin);

      const lines = await sweep(tmp);

      expect(existsSync(join(outside, "pgdata", "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      expect(holding(lines, `${file} (${join(run, "worker-0")} is a symbolic link)`)).toHaveLength(
        1
      );
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a run directory that is a link is never read: the marks of the directory it leads to stay, with what they name",
  async () => {
    const { tmp } = scratch();
    const elsewhere = mkdtempSync(join(tmp, "elsewhere-"));
    const link = join(tmp, `${runDirectoryPrefix}link`);
    try {
      const data = join(elsewhere, "worker-0", "pgdata");
      leaveData(data);
      const file = writeMark(elsewhere, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
      symlinkSync(elsewhere, link);

      const lines = await sweep(tmp);

      expect(existsSync(join(data, "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      expect(holding(lines, `${link} (${link} is a symbolic link)`)).toHaveLength(1);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a run directory that another user may write is never read, in a TMPDIR shared and sticky as /tmp is: its gone stack's directories stay",
  async () => {
    // Built in /tmp itself: a TMPDIR anywhere under a directory of this user's that no other user
    // may enter (a session's own, mode 0700) is one where no other user reaches a run directory.
    const { tmp, run } = scratch("/tmp");
    try {
      const data = join(run, "worker-0", "pgdata");
      leaveData(data);
      const file = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
      // /tmp, the default TMPDIR, is root's of mode 1777, which every user may write and whose
      // sticky bit keeps each from renaming another's entries; the row's TMPDIR is made the same.
      chmodSync(tmp, 0o1777);
      chmodSync(run, 0o777);

      const lines = await sweep(tmp);

      expect(existsSync(join(data, "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      expect(holding(lines, `${run} (${run} is writable by other users)`)).toHaveLength(1);
    } finally {
      chmodSync(run, 0o700);
      chmodSync(tmp, 0o700);
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a mark of another boot, of another PID namespace, of another time namespace and of a stack where there is no /proc is each left with its directories, and each is named with its case on the one line of what the start cannot judge",
  async () => {
    const who = owners();
    const { tmp, run } = scratch();
    try {
      const bin = mkdtempSync(join(tmp, "no-bin-"));
      const cases = [
        { owner: who.otherBoot, why: `boot ${who.otherBoot.boot}` },
        { owner: who.otherNamespace, why: "PID namespace pid:[1]" },
        { owner: who.otherTime, why: "time namespace time:[1]" },
        { owner: who.noProc, why: "its stack ran where there is no /proc" },
      ].map(({ owner, why }, index) => {
        const data = join(run, `worker-${index}`, "pgdata");
        leaveData(data);
        return { data, why, file: writeMark(run, owner, data, bin, index) };
      });

      const lines = await sweep(tmp);

      const unjudged = holding(lines, "left the Postgres of stacks whose process it cannot see:");
      expect(unjudged).toHaveLength(1);
      for (const { data, why, file } of cases) {
        expect(unjudged[0]).toContain(`${file} (${why})`);
        expect([existsSync(join(data, "PG_VERSION")), existsSync(file)]).toEqual([true, true]);
      }
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a TMPDIR spelt through a link of its user's is read under its one spelling: the gone stack's directories go, each named under that spelling, and no line says a link was refused",
  async () => {
    const { tmp } = scratch();
    const link = `${tmp}-link`;
    try {
      symlinkSync(tmp, link);
      // The run directory as a stack under that TMPDIR spells it, through the link.
      const run = mkdtempSync(join(link, runDirectoryPrefix));
      const data = join(run, "worker-0", "pgdata");
      leaveData(data);
      const file = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
      const real = marks.identityOf(run);
      expect(real.startsWith(`${tmp}/`)).toBe(true);

      const lines = await sweep(link);

      expect([existsSync(data), existsSync(file)]).toEqual([false, false]);
      expect(
        holding(lines, `removed ${join(real, "worker-0", "pgdata")} and ${real}/pg-0-`)
      ).toHaveLength(1);
      expect(holding(lines, "is a symbolic link")).toEqual([]);
    } finally {
      rmSync(link, { force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a mark whose one spelling of its run directory is another directory's is no mark of the run directory it is in: it is left with what it names, and named",
  async () => {
    const { tmp, run } = scratch();
    try {
      const data = join(run, "worker-0", "pgdata");
      leaveData(data);
      const file = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
      // The mark of a run directory of the same name elsewhere, copied into this one.
      const elsewhere = join(mkdtempSync(join(tmp, "elsewhere-")), basename(run));
      const written = JSON.parse(readFileSync(file, "utf8")) as Record<string, unknown>;
      writeFileSync(file, JSON.stringify({ ...written, identity: elsewhere }));

      const lines = await sweep(tmp);

      expect([existsSync(join(data, "PG_VERSION")), existsSync(file)]).toEqual([true, true]);
      expect(holding(lines, `${file} (no mark of this run directory)`)).toHaveLength(1);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

/** How long another start's removal of a plant may still take once the row's own start ended: a
 *  start that found the plant claimed waited for that claim, so only the last steps of the other
 *  start's removal, its stop of a stand-in and a few file removals, are left. */
const settleMs = 60_000;

/** What any start did with a plant that every start finds, `postmaster` serving from `data`, or
 *  from `claimed` where a dead start claimed `data`, with its mark `file` (`leaveElsewhere`),
 *  whose stack is gone: R3 asks each start to remove it, and the row's own start or another may
 *  be the one. Waits until the postmaster has ended and `data`, `claimed` and `file` are gone, and
 *  requires of `lines`, what one start printed, that each line naming `named` says this start
 *  removed the postmaster of `data`, or that another start removed it first, or that this start
 *  waits for another start's claim of it, and that at most one says this start removed it: none
 *  says it left it. */
async function removedByAStart(
  lines: readonly string[],
  plant: {
    readonly postmaster?: StandIn;
    readonly data: string;
    readonly claimed?: string;
    readonly file?: string;
  },
  named = plant.data
): Promise<void> {
  await until(
    () =>
      plant.postmaster?.alive() !== true &&
      [plant.data, plant.claimed, plant.file].every(
        (path) => path === undefined || !existsSync(path)
      ),
    "the end of the plant's postmaster, data directory and mark",
    settleMs
  );
  const removed = `removed the postmaster of ${plant.data}`;
  const naming = holding(lines, named);
  const said = ["another start removed", "waits for another start's claim", removed];
  expect(naming.filter((line) => !said.some((words) => line.includes(words)))).toEqual([]);
  expect(holding(naming, removed).length).toBeLessThanOrEqual(1);
}

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a stack under another TMPDIR that the kernel shows serving and whose stack is gone is removed by the same steps: its postmaster ends, its data and socket directories and its mark go",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().gone);
    try {
      await removedByAStart(await sweep(tmp), left);
    } finally {
      left.postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for one start's sweep, a `bun` child, and then at most `settleMs` for what
  // another start of the machine may still be removing of its plant.
  300_000
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start whose TMPDIR no longer exists still removes a stack under another TMPDIR that the kernel shows serving and whose stack is gone, and names the TMPDIR it cannot read on one line",
  async () => {
    const { tmp } = scratch();
    const programs = mkdtempSync(join(tmp, "no-programs-"));
    const gone = join(tmp, "tmpdir-gone");
    const left = leaveElsewhere(owners().gone);
    try {
      const lines = await sweep(gone, programs);

      await removedByAStart(lines, left);
      expect(holding(lines, `left what it cannot read: ${gone} (ENOENT`)).toHaveLength(1);
    } finally {
      left.postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for one start's sweep, a `bun` child, and then at most `settleMs` for what
  // another start of the machine may still be removing of its plant.
  300_000
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start with no docker on its PATH leaves the mark of a dead stack's container, with its socket directory, and names it: it cannot tell whether that container still runs",
  async () => {
    const { tmp, run } = scratch();
    try {
      const { sockets, container, file } = leaveContainerMark(run, owners().gone);

      const lines = await sweep(tmp);

      expect([existsSync(sockets), existsSync(file)]).toEqual([true, true]);
      expect(
        holding(
          lines,
          `${file} (its container ${container}, since this start could not list Docker's containers)`
        )
      ).toHaveLength(1);
      expect(holding(lines, "removed")).toEqual([]);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start with no docker on its PATH leaves the mark of a live stack's container without a word: it judges the owner before it reads Docker",
  async () => {
    const { tmp, run } = scratch();
    try {
      const { sockets, file } = leaveContainerMark(run, owners().runs);

      const lines = await sweep(tmp);

      expect([existsSync(sockets), existsSync(file)]).toEqual([true, true]);
      expect(holding(lines, run)).toEqual([]);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

/** The id of the container the next two rows remove, which their stand-in `docker` alone answers
 *  for: it names no container of the machine. */
const standInId = "0".repeat(64);
/** How long the next two rows let one ask of Docker go unanswered, in place of a start's 60 s: on
 *  a shared Linux machine under a load of about 900, on 2026-11-18, the stand-in `docker` of an
 *  ask took more than 1 s to start, and an ask it answers must never run out here. */
const answerMs = 10_000;
/** How long the next row's stand-in `docker rm` takes: longer than one ask may go unanswered, and
 *  than the 5 s a start waits before its first ask of whether Docker answers. */
const removalSeconds = 12;

// A daemon that many sessions share took 74 s over one `docker rm` while it answered every other
// call within a second. The removal here takes longer than one ask may go unanswered.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start waits for its removal of a dead stack's container as long as Docker takes over it while Docker answers, and then says it removed it",
  async () => {
    const { tmp } = scratch();
    try {
      const docker = standInDocker(tmp, [
        'case "$1" in',
        `  rm) sleep ${removalSeconds}; echo "$4" ;;`,
        "  ps) ;;",
        "esac",
      ]);

      const { removal, seconds } = await removeInChild(
        docker.bin,
        standInId,
        answerMs,
        claimEventMs
      );

      expect(removal).toEqual({ kind: "removed" });
      expect(seconds).toBeGreaterThan(removalSeconds);
      // It asked whether Docker answers while its removal ran, and Docker did.
      expect(docker.calls()[0]).toBe(`rm --force --volumes ${standInId}`);
      expect(docker.calls()).toContain(`ps --all --quiet --no-trunc --filter id=${standInId}`);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // One `bun` child, which waits at most `claimEventMs` to start under load and then the 12 s of
  // the stand-in's removal; a stuck child is killed at `claimEventMs`.
  600_000
);

// A daemon that hangs answers neither the removal nor the asks beside it: the start gives the
// removal up at the first ask that goes unanswered, and stops both calls it made. That ask may
// be stopped before its stand-in writes its call, so the row requires no line of it.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start gives up its removal of a dead stack's container only where Docker answers no ask while it runs, says so, and leaves no docker it started running",
  async () => {
    const { tmp } = scratch();
    const docker = standInDocker(tmp, ["exec sleep 600"]);
    try {
      const { removal } = await removeInChild(docker.bin, standInId, answerMs, claimEventMs);

      expect(removal).toEqual({
        kind: "failed",
        reason: "Docker answered no docker ps within 10 s while this start removed it",
      });
      const [first, ...asks] = docker.calls();
      expect(first).toBe(`rm --force --volumes ${standInId}`);
      expect(asks.length).toBeLessThanOrEqual(1);
      for (const ask of asks) {
        expect(ask).toBe(`ps --all --quiet --no-trunc --filter id=${standInId}`);
      }
      // Each `docker` the start ran ends with the start's removal: the stand-in's `sleep` runs on
      // where no signal reached it.
      await until(
        () => docker.processes().every(({ pid, start }) => !marks.stillRuns(pid, start)),
        "the end of each docker the start ran",
        claimEventMs
      );
    } finally {
      for (const { pid, start } of docker.processes()) {
        if (marks.stillRuns(pid, start)) process.kill(pid, "SIGKILL");
      }
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // One `bun` child, which waits at most `claimEventMs` to start under load and then the 5 s
  // before its ask and the 10 s that ask may go unanswered, and then the end of each `docker`.
  600_000
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a stack under another TMPDIR whose stack runs is not touched, and the start prints nothing of it",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().runs);
    try {
      const lines = await sweep(tmp);

      expect(left.postmaster?.alive()).toBe(true);
      expect([
        existsSync(join(left.data, "PG_VERSION")),
        left.file !== undefined && existsSync(left.file),
      ]).toEqual([true, true]);
      expect(holding(lines, left.run)).toEqual([]);
    } finally {
      left.postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a postmaster of this user under a run directory of another TMPDIR that no mark names is left and named",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere();
    try {
      const lines = await sweep(tmp);

      expect(left.postmaster?.alive()).toBe(true);
      expect(existsSync(join(left.data, "PG_VERSION"))).toBe(true);
      expect(
        holding(
          lines,
          `${left.data} (postmaster ${left.postmaster?.pid} serves it from a run directory this start does not list, and no mark there names it)`
        )
      ).toHaveLength(1);
    } finally {
      left.postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: where the target of a TMPDIR link changed between the write of a mark and the teardown, the teardown reads the stack's postmaster under the mark's one spelling, the path the sweep acts on",
  () => {
    const { tmp } = scratch();
    const link = `${tmp}-link`;
    const moved = `${tmp}-moved`;
    let postmaster: StandIn | undefined;
    try {
      const first = join(tmp, "first");
      mkdirSync(first);
      symlinkSync(first, link);
      // The run directory as its stack spells it, through the link; its postmaster serves from the
      // one spelling, which the mark holds.
      const run = mkdtempSync(join(link, runDirectoryPrefix));
      const identity = marks.identityOf(run);
      const served = join(identity, "worker-0", "pgdata");
      postmaster = standInPostmaster(join(tmp, "bin"), served, "its TMPDIR");
      writeMark(run, owners().runs, join(run, "worker-0", "pgdata"), join(tmp, "bin"));
      // The link now leads to a copy of that run directory, marks and all, elsewhere.
      mkdirSync(moved);
      Bun.spawnSync(["cp", "-a", identity, join(moved, basename(run))]);
      rmSync(link);
      symlinkSync(moved, link);

      expect(servingPostmasters(run)).toEqual([served]);
    } finally {
      postmaster?.stop();
      rmSync(link, { force: true });
      rmSync(moved, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

// Two starts at once on one dead mark whose postmaster still serves. The first claims the data
// directory (`claimForRemoval`) and signals the postmaster, which then runs on until the row says
// go; the second starts only after that signal, so it finds the claim held, and says that it waits
// for it. Only then does the postmaster end.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: of two starts on one dead mark whose postmaster serves, the one that claims it sends the one signal and says it removed it, and the other waits for that claim and says another start removed it",
  async () => {
    const { tmp, run } = scratch();
    const bin = join(tmp, "bin");
    const signals = join(tmp, "signals");
    const go = join(tmp, "go");
    const data = join(run, "worker-0", "pgdata");
    const postmaster = signalCountingPostmaster(bin, data, signals, go);
    const mark = writeMark(run, owners().gone, data, bin);
    const signalled = () =>
      existsSync(signals) ? readFileSync(signals, "utf8").split("\n").filter(Boolean) : [];
    try {
      const first = startSweep(tmp);
      await until(() => signalled().length > 0, "the first start's signal", claimEventMs);
      const second = startSweep(tmp);
      await until(
        () =>
          second.stderr().includes(`waits for another start's claim of ${data}`) ||
          signalled().length > 1,
        "the second start's wait for the first start's claim, or its own signal",
        claimEventMs
      );
      writeFileSync(go, "");
      const [firstLines, secondLines] = await Promise.all([first.lines, second.lines]);

      expect(signalled()).toEqual(["QUIT"]);
      expect(postmaster.alive()).toBe(false);
      expect(holding(firstLines, `removed the postmaster of ${data}`)).toHaveLength(1);
      expect(holding(secondLines, `removed the postmaster of ${data}`)).toEqual([]);
      expect(
        holding(secondLines, data).filter((line) => line.includes("another start removed"))
      ).toHaveLength(1);
      expect([existsSync(data), existsSync(mark)]).toEqual([false, false]);
      expect(readdirSync(dirname(data)).filter((name) => name.startsWith("pgdata"))).toEqual([]);
    } finally {
      postmaster.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for two starts' sweeps, each a `bun` child, and for the two events between them,
  // each for at most `claimEventMs`; the two sweeps then end in well under a second.
  600_000
);

// The postmaster `postmasterOf` verified may end by itself before the start's first signal. No
// `await` is between that read and the signal, so no row can time an end into that window: this
// row asks `stopPostmaster` of a process that ended already, which is what the window gives it. It
// must answer that no signal of its reached that process, which keeps "removed the postmaster"
// off the start's line; of a process that runs, that its signal did, and the process ends.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start says it stopped a postmaster only where its own signal reached it: a postmaster that ended before the signal got none",
  async () => {
    const sleeper = () => {
      const child = Bun.spawn(["sleep", "600"], { stdout: "ignore", stderr: "ignore" });
      return { child, start: marks.startOf(readFileSync(`/proc/${child.pid}/stat`, "utf8")) };
    };
    const ended = sleeper();
    const runs = sleeper();
    try {
      ended.child.kill("SIGKILL");
      await ended.child.exited;
      expect(await stopPostmaster({ pid: ended.child.pid, start: ended.start }, "ended")).toBe(
        false
      );
      expect(await stopPostmaster({ pid: runs.child.pid, start: runs.start }, "runs")).toBe(true);
      expect(marks.stillRuns(runs.child.pid, runs.start)).toBe(false);
    } finally {
      for (const { child } of [ended, runs]) {
        if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
      }
    }
  },
  // The stop polls `/proc` every 50 ms until the sleep that got SIGQUIT has ended, which takes
  // well under a second; 60 s holds a machine under heavy load.
  60_000
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: what a start killed between its claim and its removal left, the next start claims again: the postmaster that serves from the claimed directory ends, and the directory goes",
  async () => {
    const { tmp, run } = scratch();
    const bin = join(tmp, "bin");
    const data = join(run, "worker-0", "pgdata");
    const postmaster = standInPostmaster(bin, data, "its TMPDIR");
    const mark = writeMark(run, owners().gone, data, bin);
    try {
      const claimed = await claimAndDie(data);
      expect([existsSync(data), existsSync(claimed), postmaster.alive()]).toEqual([
        false,
        true,
        true,
      ]);

      const lines = await sweep(tmp);

      expect(postmaster.alive()).toBe(false);
      expect([existsSync(claimed), existsSync(mark)]).toEqual([false, false]);
      expect(holding(lines, `removed the postmaster of ${data}`)).toHaveLength(1);
    } finally {
      postmaster.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for a claimer, a `bun` child, to print its claim, and for one start's sweep,
  // another `bun` child; a stuck child waits out its own 600 s at most.
  600_000
);

// The claim comes before the postmaster here: until a postmaster serves from it, no start but
// this row's own reads the run directory, so the claimed directory is there for the row to see.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: what a start killed between its claim and its removal left in a run directory of another TMPDIR, a start of this TMPDIR finds by the claimed name the kernel shows: the postmaster ends, the directory and the mark go, and one line says so",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().gone, false);
    let postmaster: StandIn | undefined;
    try {
      const claimed = await claimAndDie(left.data);
      expect([existsSync(left.data), existsSync(claimed)]).toEqual([false, true]);
      // The kernel names the postmaster's working directory by the claim's name.
      postmaster = standInPostmaster(left.bin, claimed, "every start");

      const lines = await sweep(tmp);

      // A line that names that stack says a start removed it: none says it was left. The start
      // prints the run directory's one spelling, the one its data directory is written in.
      await removedByAStart(
        lines,
        { postmaster, data: left.data, claimed, file: left.file },
        dirname(dirname(left.data))
      );
    } finally {
      postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for a claimer, a `bun` child, to print its claim, and for one start's sweep,
  // another `bun` child, and then at most `settleMs`; a stuck child waits out its own 600 s.
  600_000
);

// Two starts at once. The first, Z, lists two postmasters of run directories it does not list,
// and finds the data directory of the first claimed by a claimer that still runs, so it waits for
// that claim. Meanwhile the second, X, removes the stack of the second run directory, which is in
// its own TMPDIR. Once the claimer is killed, Z goes on to that second run directory, whose mark
// and postmaster X removed, so that postmaster is no Postgres Z left. Each postmaster serves only
// once the claim is made, as in the row above, so no other start can take the held stack first.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start names as left only a postmaster that still runs once it has read its run directory: one it listed that another start removed meanwhile it does not name",
  async () => {
    const parent = mkdtempSync(join(tmpdir(), "marks-two-starts-"));
    // `servedElsewhere` lists by data directory, so the held stack, in `a-held`, comes first.
    const [heldTmp, otherTmp, startTmp] = ["a-held", "b-other", "c-start"].map((name) => {
      mkdirSync(join(parent, name));
      return join(parent, name);
    }) as [string, string, string];
    const plant = (tmp: string) => {
      const run = mkdtempSync(join(tmp, runDirectoryPrefix));
      const data = join(realpathSync(run), "worker-0", "pgdata");
      leaveData(data);
      const bin = join(tmp, "bin");
      return { data, bin, file: writeMark(run, owners().gone, data, bin) };
    };
    const held = plant(heldTmp);
    const other = plant(otherTmp);
    let claimer: Subprocess | undefined;
    let heldPostmaster: StandIn | undefined;
    let otherPostmaster: StandIn | undefined;
    try {
      const claim = await claimInChild(held.data);
      claimer = claim.claimer;
      heldPostmaster = standInPostmaster(held.bin, claim.claimed, "every start");
      otherPostmaster = standInPostmaster(other.bin, other.data, "every start");
      const first = startSweep(startTmp);
      await until(
        () => first.stderr().includes(`waits for another start's claim of ${held.data}`),
        "the first start's wait for the claim the claimer holds",
        claimEventMs
      );
      const second = startSweep(otherTmp);
      const otherServes = otherPostmaster;
      await until(() => !otherServes.alive(), "the second start's stop", claimEventMs);
      await endChild(claimer);
      const [firstLines, secondLines] = await Promise.all([first.lines, second.lines]);

      // The line a start names an unmarked postmaster on, which the first start must not write.
      const unmarked = "serves it from a run directory this start does not list";
      expect(holding(holding(firstLines, other.data), unmarked)).toEqual([]);
      for (const lines of [firstLines, secondLines]) {
        await removedByAStart(lines, {
          ...held,
          claimed: claim.claimed,
          postmaster: heldPostmaster,
        });
        await removedByAStart(lines, { ...other, postmaster: otherPostmaster });
      }
    } finally {
      if (claimer !== undefined) await endChild(claimer);
      heldPostmaster?.stop();
      otherPostmaster?.stop();
      rmSync(parent, { recursive: true, force: true });
    }
  },
  // The row waits for a claimer and two starts' sweeps, each a `bun` child, and for two events
  // between them, each for at most `claimEventMs`; the sweeps then end in well under a second.
  600_000
);
