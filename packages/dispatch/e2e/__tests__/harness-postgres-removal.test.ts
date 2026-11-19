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
 *  of another run on the machine; the rows of a container's removal run a start's removal of one
 *  container (`removeContainer`), or its whole sweep, in a child instead, with a stand-in `docker`
 *  that runs no real one. They need Linux (`/proc`).
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

import { expect, spyOn, test } from "bun:test";
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
import { fileURLToPath } from "node:url";
import type { Subprocess } from "bun";
import ts from "typescript";

import { removalBounds, removeContainer } from "../harness-postgres-container-removal";
import * as marks from "../harness-postgres-marks";
import { claimWaitMs, servingPostmasters, stopPostmaster } from "../harness-postgres-removal";
import {
  claimAndDie,
  claimEventMs,
  claimInChild,
  endChild,
  fixtureDockerInChild,
  holding,
  leaveContainerMark,
  leaveData,
  leaveElsewhere,
  owners,
  type Part,
  partOf,
  removeInChild,
  rowMachine,
  rowMs,
  rowOf,
  rowRoomMs,
  type StandInDocker,
  scratch,
  signalCountingPostmaster,
  standInDocker,
  standInPostmaster,
  startSweep,
  sweep,
  sweepInChild,
  type Tick,
  until,
  type Waits,
  waitsMs,
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

/** What a row waits for that runs one start's sweep, which claims nothing, so it reads no holder,
 *  and waits for nothing else (`Waits`). */
const oneSweep: Waits = { children: 1, reads: 0, bounds: [] };

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a mark whose postmaster.bin is the planter's and whose stack directory is a link out of the run is left: its program never runs, the live process stays, the directory outside stays",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp, run } = scratch();
    const hostile = plantHostileMark(run, tmp, owners().gone);
    try {
      const lines = await sweep(row, tmp);

      expect(hostile.kept()).toEqual(hostileMarkKept);
      expect(holding(lines, `${hostile.file} (${hostile.link} is a symbolic link)`)).toHaveLength(
        1
      );
    } finally {
      hostile.live.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: the start runs no program a mark names: a postmaster.pid that names a process which is no postgres of the mark's binaries leaves the mark, and that process runs on",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp, run } = scratch();
    const marker = join(tmp, "the-planted-program-ran");
    const data = join(run, "worker-0", "pgdata");
    mkdirSync(data, { recursive: true });
    const live = standIn(data);
    try {
      leaveData(data, live.pid);
      const file = writeMark(run, owners().gone, data, plantedBin(tmp, marker));

      const lines = await sweep(row, tmp);

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
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a link in any component of what a mark names is never followed: a stack directory that links out of the run leaves the directory it leads to",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp, run } = scratch();
    const outside = join(tmp, "outside");
    try {
      leaveData(join(outside, "pgdata"));
      symlinkSync(outside, join(run, "worker-0"));
      const noBin = mkdtempSync(join(tmp, "no-bin-"));
      const file = writeMark(run, owners().gone, join(run, "worker-0", "pgdata"), noBin);

      const lines = await sweep(row, tmp);

      expect(existsSync(join(outside, "pgdata", "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      expect(holding(lines, `${file} (${join(run, "worker-0")} is a symbolic link)`)).toHaveLength(
        1
      );
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a run directory that is a link is never read: the marks of the directory it leads to stay, with what they name",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp } = scratch();
    const elsewhere = mkdtempSync(join(tmp, "elsewhere-"));
    const link = join(tmp, `${runDirectoryPrefix}link`);
    try {
      const data = join(elsewhere, "worker-0", "pgdata");
      leaveData(data);
      const file = writeMark(elsewhere, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
      symlinkSync(elsewhere, link);

      const lines = await sweep(row, tmp);

      expect(existsSync(join(data, "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      expect(holding(lines, `${link} (${link} is a symbolic link)`)).toHaveLength(1);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a run directory that another user may write is never read, in a TMPDIR shared and sticky as /tmp is: its gone stack's directories stay",
  async () => {
    const row = rowOf(oneSweep);
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

      const lines = await sweep(row, tmp);

      expect(existsSync(join(data, "PG_VERSION"))).toBe(true);
      expect(existsSync(file)).toBe(true);
      expect(holding(lines, `${run} (${run} is writable by other users)`)).toHaveLength(1);
    } finally {
      chmodSync(run, 0o700);
      chmodSync(tmp, 0o700);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a mark of another boot, of another PID namespace, of another time namespace and of a stack where there is no /proc is each left with its directories, and each is named with its case on the one line of what the start cannot judge",
  async () => {
    const row = rowOf(oneSweep);
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

      const lines = await sweep(row, tmp);

      const unjudged = holding(lines, "left the Postgres of stacks whose process it cannot see:");
      expect(unjudged).toHaveLength(1);
      for (const { data, why, file } of cases) {
        expect(unjudged[0]).toContain(`${file} (${why})`);
        expect([existsSync(join(data, "PG_VERSION")), existsSync(file)]).toEqual([true, true]);
      }
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

/** What a row waits for that runs one start's sweep, which removes a dead stack's data and socket
 *  directories that no postmaster serves: two holder reads, one before each claim (`Waits`). */
const sweepRemoving: Waits = { children: 1, reads: 2, bounds: [] };

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a TMPDIR spelt through a link of its user's is read under its one spelling: the gone stack's directories go, each named under that spelling, and no line says a link was refused",
  async () => {
    const row = rowOf(sweepRemoving);
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

      const lines = await sweep(row, link);

      expect([existsSync(data), existsSync(file)]).toEqual([false, false]);
      expect(
        holding(lines, `removed ${join(real, "worker-0", "pgdata")} and ${real}/pg-0-`)
      ).toHaveLength(1);
      expect(holding(lines, "is a symbolic link")).toEqual([]);
    } finally {
      rmSync(link, { force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep and its two
  // holder reads.
  rowMs(sweepRemoving)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a mark whose one spelling of its run directory is another directory's is no mark of the run directory it is in: it is left with what it names, and named",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp, run } = scratch();
    try {
      const data = join(run, "worker-0", "pgdata");
      leaveData(data);
      const file = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
      // The mark of a run directory of the same name elsewhere, copied into this one.
      const elsewhere = join(mkdtempSync(join(tmp, "elsewhere-")), basename(run));
      const written = JSON.parse(readFileSync(file, "utf8")) as Record<string, unknown>;
      writeFileSync(file, JSON.stringify({ ...written, identity: elsewhere }));

      const lines = await sweep(row, tmp);

      expect([existsSync(join(data, "PG_VERSION")), existsSync(file)]).toEqual([true, true]);
      expect(holding(lines, `${file} (no mark of this run directory)`)).toHaveLength(1);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
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

/** What a row waits for that runs one start's sweep of a stack elsewhere whose postmaster serves,
 *  and then for what another start may still be removing of it (`settleMs`): the sweep claims the
 *  data directory, stops the postmaster and claims the socket directory, a holder read before each
 *  claim, and one stop, with its reads after it (`Waits`). */
const sweepStopping: Waits = { children: 1, reads: 2, stops: 1, bounds: [settleMs] };

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a stack under another TMPDIR that the kernel shows serving and whose stack is gone is removed by the same steps: its postmaster ends, its data and socket directories and its mark go",
  async () => {
    const row = rowOf(sweepStopping);
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().gone);
    try {
      await removedByAStart(await sweep(row, tmp), left);
    } finally {
      left.postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep, its holder reads
  // and its stop of a postmaster, and then `settleMs`.
  rowMs(sweepStopping)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start whose TMPDIR no longer exists still removes a stack under another TMPDIR that the kernel shows serving and whose stack is gone, and names the TMPDIR it cannot read on one line",
  async () => {
    const row = rowOf(sweepStopping);
    const { tmp } = scratch();
    const programs = mkdtempSync(join(tmp, "no-programs-"));
    const gone = join(tmp, "tmpdir-gone");
    const left = leaveElsewhere(owners().gone);
    try {
      const lines = await sweep(row, gone, programs);

      await removedByAStart(lines, left);
      expect(holding(lines, `left what it cannot read: ${gone} (ENOENT`)).toHaveLength(1);
    } finally {
      left.postmaster?.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep, its holder reads
  // and its stop of a postmaster, and then `settleMs`.
  rowMs(sweepStopping)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start with no docker on its PATH leaves the mark of a dead stack's container, with its socket directory, and names it: it cannot tell whether that container still runs",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp, run } = scratch();
    try {
      const { sockets, container, file } = leaveContainerMark(run, owners().gone);

      const lines = await sweep(row, tmp);

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
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start with no docker on its PATH leaves the mark of a live stack's container without a word: it judges the owner before it reads Docker",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp, run } = scratch();
    try {
      const { sockets, file } = leaveContainerMark(run, owners().runs);

      const lines = await sweep(row, tmp);

      expect([existsSync(sockets), existsSync(file)]).toEqual([true, true]);
      expect(holding(lines, run)).toEqual([]);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

/** The id of the container the next rows remove, which their stand-in `docker` alone answers
 *  for: it names no container of the machine. */
const standInId = "0".repeat(64);
/** The bounds of a start's removal of a container in the next rows (`RemovalBounds`). One ask of
 *  Docker may go unanswered for 10 s: a stand-in `docker`, a bash script, answers in well under a
 *  second, so only a load that held one such script for 10 s could fail a row by it. The start
 *  waits 90 s in all: longer than the 60 s a start once bounded its `docker rm` by, so a start
 *  that bounds it so again gives the removal up before the bound, and the row that waits for the
 *  bound fails. A fixed bound of any length past 90 s never fires within these rows: one short of
 *  the start's own 600 s fails the row that runs the start's own bounds on a clock the row moves
 *  (`rowMachine`), and one on the real clock fails the row that reads the removal's source. */
const rowBounds = { answerMs: 10_000, boundMs: 90_000 };
/** What a row waits for that runs one start's removal of a container in a child: the child, the
 *  removal's bound, and the end of each `docker` the start ran (`Waits`). */
const oneRemoval: Waits = { children: 1, reads: 0, bounds: [rowBounds.boundMs, claimEventMs] };
/** How long the next row's stand-in `docker rm` takes: three times as long as one ask may go
 *  unanswered, so a start that bounds its removal by that time gives it up long before. */
const removalSeconds = (3 * rowBounds.answerMs) / 1000;
/** A stand-in `docker ps` of one container that lists it, as Docker lists a container while it
 *  removes it: `docker ps --all --quiet --no-trunc --filter id=<id>` prints the id. */
const listsIt = `  ps) echo "\${6#id=}" ;;`;
/** A stand-in `docker rm` that Docker refuses, as it refuses a removal while another removal of
 *  the container is in progress. */
const inProgress =
  '  rm) echo "Error response from daemon: removal of container $4 is already in progress" >&2; exit 1 ;;';

/** The lines of `lines` that say the start waits for its removal of the container `id`. */
function waitLines(lines: readonly string[], id: string): string[] {
  return holding(lines, `: waits for Docker's removal of container ${id.slice(0, 12)}`);
}

/** The room a row gives the stand-in's clock (`Tick`) on each side of a bound, in seconds: the
 *  start of one `bash` stand-in, which is when a call reaches that clock, and one timer of the
 *  start's that fires late, each under load. It is less than the 90 s of `rowBounds`, so a bound
 *  of three times the row's (300 s for 90 s) or a fixed bound past the row's falls outside it. */
const clockRoomS = 60;

/** The seconds from the stand-in's first `docker rm` to the start of its last `docker ps` ask of
 *  one container, on the stand-in's own clock: how long the start waited on its removal, as the
 *  stand-in saw it, which no load on the start or the row stretches. */
function askedFor(ticks: readonly Tick[]): number {
  const rm = ticks.find((tick) => tick.event === "start" && tick.call.startsWith("rm "));
  const asks = ticks.filter(
    (tick) => tick.event === "start" && tick.call.startsWith("ps --all --quiet")
  );
  return (asks.at(-1)?.at ?? Number.NaN) - (rm?.at ?? Number.NaN);
}

/** Requires that the start waited on its removal until `boundMs`, on the stand-in's clock
 *  (`askedFor`): its last ask came within `clockRoomS` past the bound, and no sooner than one
 *  pause between two asks, 5 s, and `clockRoomS` before it. */
function expectEndedAtBound(ticks: readonly Tick[], boundMs: number): void {
  const waited = askedFor(ticks);
  expect(waited).toBeGreaterThan(boundMs / 1000 - 5 - clockRoomS);
  expect(waited).toBeLessThan(boundMs / 1000 + clockRoomS);
}

/** Kills each `docker` stand-in a row's start ran that still runs, by its pid and start. */
function endStandIns(docker: StandInDocker): void {
  for (const { pid, start } of [...docker.processes(), ...docker.running()]) {
    if (marks.stillRuns(pid, start)) process.kill(pid, "SIGKILL");
  }
}

// A daemon that many sessions share took 74 s and 91 s over one `docker rm` while it answered
// every other call within a second. The start waits while Docker answers each ask, says so once
// the removal has run as long as one ask may go unanswered, and says it removed the container
// once Docker ends the removal.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start waits for its removal of a dead stack's container as long as Docker takes over it while Docker answers, says so once it has waited as long as one ask may take, and then says it removed it",
  async () => {
    const row = rowOf(oneRemoval);
    const { tmp } = scratch();
    try {
      const docker = standInDocker(tmp, [
        'case "$1" in',
        `  rm) sleep ${removalSeconds}; echo "$4" ;;`,
        listsIt,
        "esac",
      ]);

      const { removal, lines, seconds } = await removeInChild(
        row,
        docker.bin,
        standInId,
        rowBounds
      );

      expect(removal).toEqual({ kind: "removed" });
      expect(seconds).toBeGreaterThan(removalSeconds);
      expect(docker.calls().filter((call) => call.startsWith("rm "))).toEqual([
        `rm --force --volumes ${standInId}`,
      ]);
      expect(waitLines(lines, standInId)).toHaveLength(1);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child and its removal.
  rowMs(oneRemoval)
);

// A daemon that hangs answers neither the removal nor the asks beside it: the start gives the
// removal up at the first ask that goes unanswered, and stops both calls it made.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start gives up its removal of a dead stack's container where Docker answers no ask while it runs, says so, and leaves no docker it started running",
  async () => {
    const row = rowOf(oneRemoval);
    const { tmp } = scratch();
    const docker = standInDocker(tmp, ['exec -a "$0" sleep 600']);
    try {
      const { removal } = await removeInChild(row, docker.bin, standInId, rowBounds);

      expect(removal).toEqual({
        kind: "failed",
        reason: "Docker answered no docker ps within 10 s while this start removed it",
      });
      // Each `docker` the start ran ends with the start's removal: the stand-in's `sleep`, which
      // keeps its path for a name, runs on where no signal reached it.
      await until(
        () => docker.running().length === 0,
        "the end of each docker the start ran",
        claimEventMs
      );
    } finally {
      endStandIns(docker);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child and its removal,
  // and then the end of each `docker` it ran.
  rowMs(oneRemoval)
);

// A `docker ps` that exits other than 0, as one gives where the daemon cannot be reached, is no
// answer: the start gives its removal up at that ask and says why, and does not wait on.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start reads a docker ps that fails as no answer: it gives up its removal of a dead stack's container at that ask, says why, and leaves no docker it started running",
  async () => {
    const row = rowOf(oneRemoval);
    const { tmp } = scratch();
    const refused =
      "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?";
    const docker = standInDocker(tmp, [
      'case "$1" in',
      '  rm) exec -a "$0" sleep 600 ;;',
      `  ps) echo ${JSON.stringify(refused)} >&2; exit 1 ;;`,
      "esac",
    ]);
    try {
      const { removal } = await removeInChild(row, docker.bin, standInId, rowBounds);

      expect(removal).toEqual({
        kind: "failed",
        reason: `docker ps failed (${refused}) while this start removed it`,
      });
      await until(
        () => docker.running().length === 0,
        "the end of each docker the start ran",
        claimEventMs
      );
    } finally {
      endStandIns(docker);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child and its removal,
  // and then the end of each `docker` it ran.
  rowMs(oneRemoval)
);

/** The bounds of the next rows (`RemovalBounds`): an ask may go unanswered three minutes, and the
 *  removal waits 10 s in all, so a stand-in `docker ps` that answers after 150 s answers within
 *  the ask's own limit and long after the bound. */
const slowAskBounds = { answerMs: 180_000, boundMs: 10_000 };
/** What one of the next rows waits for (`Waits`): one child, whose removal may run to the ask's
 *  limit where the bound does not cut it short, and then the end of each `docker` it ran. */
const slowAsk: Waits = { children: 1, reads: 0, bounds: [slowAskBounds.answerMs, claimEventMs] };

// The bound is a deadline: an ask that is still out when the bound comes ends there, and does not
// carry the removal on to its own limit. The stand-in's `docker ps` answers after 150 s, within the
// ask's 180 s, and the bound is 10 s. Its clock times the start's end, the last end of a call it
// ran: a start that gave the ask its own limit ends at the answer, 150 s after the bound.
for (const docker of [
  { removal: "never ends", rm: '  rm) exec -a "$0" sleep 600 ;;' },
  { removal: "is refused as already in progress", rm: inProgress },
] as const) {
  test.skipIf(!onLinux)(
    `v-no-leftover-postgres: a start whose removal of a dead stack's container ${docker.removal} ends it at its bound while its ask of Docker is still out: no ask carries the removal past its bound`,
    async () => {
      const row = rowOf(slowAsk);
      const { tmp } = scratch();
      const standIn = standInDocker(tmp, [
        'case "$1" in',
        docker.rm,
        `  ps) sleep 150 & wait $!; echo "\${6#id=}" ;;`,
        "esac",
      ]);
      try {
        const { removal } = await removeInChild(row, standIn.bin, standInId, slowAskBounds);

        expect(removal).toEqual({
          kind: "failed",
          reason:
            docker.rm === inProgress
              ? `Error response from daemon: removal of container ${standInId} is already in progress, and Docker had not answered whether it still lists it when the 10 s bound of this start's removal came`
              : "Docker did not end the removal within 10 s, and it had not answered the last ask when that bound came",
        });
        await until(
          () => standIn.running().length === 0,
          "the end of each docker the start ran",
          claimEventMs
        );
        const ticks = standIn.clock();
        const began = ticks.find((tick) => tick.call.startsWith("rm "))?.at ?? Number.NaN;
        const ended = Math.max(
          ...ticks.filter((tick) => tick.event === "end").map((tick) => tick.at)
        );
        expect(ended - began).toBeLessThan(slowAskBounds.boundMs / 1000 + clockRoomS);
      } finally {
        endStandIns(standIn);
        rmSync(tmp, { recursive: true, force: true });
      }
    },
    // What the row waits for, summed by the fixtures' program (`rowMs`): one child and its
    // removal, up to the ask's own limit, and then the end of each `docker` it ran.
    rowMs(slowAsk)
  );
}

// Another start's removal of the container, which Docker still runs: Docker refuses this start's
// removal, and the start sends none again. It asks whether Docker still lists the container, and
// says another start removed it once Docker lists it no more. The stand-in lists it for three
// asks, which take 15 s and more.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start whose removal of a dead stack's container Docker refuses while another removal of it is in progress sends no docker rm again, waits for that removal while Docker answers, says so, and then says another start removed it",
  async () => {
    const row = rowOf(oneRemoval);
    const { tmp } = scratch();
    try {
      const docker = standInDocker(tmp, [
        'case "$1" in',
        inProgress,
        `  ps) if [ "$(grep -c "^ps " "$(dirname "$0")/calls")" -le 3 ]; then echo "\${6#id=}"; fi ;;`,
        "  inspect) echo removing ;;",
        "esac",
      ]);

      const { removal, lines } = await removeInChild(row, docker.bin, standInId, rowBounds);

      expect(removal).toEqual({
        kind: "elsewhere",
        reason: `Error response from daemon: removal of container ${standInId} is already in progress`,
      });
      const calls = docker.calls();
      expect(calls.filter((call) => call.startsWith("rm "))).toHaveLength(1);
      expect(calls.filter((call) => call.startsWith("ps "))).toHaveLength(4);
      expect(waitLines(lines, standInId)).toHaveLength(1);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child and its removal.
  rowMs(oneRemoval)
);

// Docker refuses a second removal from the moment the first marks the container's removal in
// progress, and goes on refusing while that removal kills the container: moby v28.5.2 marks it at
// `daemon/delete.go:40-44`, before `:91-101` kills a container that runs, through a kill that
// containerd is given with no deadline (`daemon/kill.go:111`), and `docker inspect` reads the
// container as `running` until its process has ended (`container/state.go:121-136`). So a start
// that another start's stuck removal holds gets "already in progress" and `running` for as long as
// the daemon takes. It sends no `docker rm` again, says that it waits once the removal has run as
// long as one ask may take, and gives it up at its bound, on one line that names the container.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start whose removal of a dead stack's container Docker refuses as already in progress while docker inspect reads the container running sends no docker rm again, says that it waits, and gives the removal up at its bound",
  async () => {
    const row = rowOf(oneRemoval);
    const { tmp } = scratch();
    const docker = standInDocker(tmp, [
      'case "$1" in',
      inProgress,
      listsIt,
      "  inspect) echo running ;;",
      "esac",
    ]);
    try {
      const { removal, lines } = await removeInChild(row, docker.bin, standInId, rowBounds);

      expect(removal).toEqual({
        kind: "failed",
        reason: `Error response from daemon: removal of container ${standInId} is already in progress, and Docker still lists it 90 s after this start's removal began`,
      });
      expect(docker.calls().filter((call) => call.startsWith("rm "))).toHaveLength(1);
      expect(waitLines(lines, standInId)).toHaveLength(1);
      expectEndedAtBound(docker.clock(), rowBounds.boundMs);
    } finally {
      endStandIns(docker);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child and its removal.
  rowMs(oneRemoval)
);

// The start's own bounds: a removal that Docker never ends, while it answers each ask at once, runs
// the 600 s of the start's bound and no less, and says that it waits at 60 s. The row runs it on a
// clock it moves (`rowMachine`), which moves only while the removal waits on it, so the 600 s take
// a moment of real time, and each time the row reads is the removal's own, which no load
// stretches. A fixed limit of the removal short of 600 s, on that clock or as the `timeout` of its
// `docker rm`, ends the removal sooner, with another reason; a limit on the real clock is the next
// row's.
test("v-no-leftover-postgres: a start whose removal of a dead stack's container Docker never ends, within the start's own bounds, says that it waits at 60 s and gives the removal up at 600 s and no sooner, on a clock the row moves", async () => {
  const docker = rowMachine((args) =>
    args[0] === "ps" ? { after: 0, stdout: `${standInId}\n` } : "never"
  );
  const said: [number, string][] = [];
  const lines = spyOn(console, "error").mockImplementation((...words: unknown[]) => {
    said.push([docker.machine.now(), words.join(" ")]);
  });
  try {
    const removal = await docker.drive(
      removeContainer(standInId, undefined, removalBounds, docker.machine)
    );

    expect(removal).toEqual({
      kind: "failed",
      reason: "Docker did not end the removal within 600 s, while it answered each ask",
    });
    expect(said).toEqual([
      [
        60_000,
        expect.stringContaining(
          `: waits for Docker's removal of container ${standInId.slice(0, 12)}: the removal has run 60 s and Docker answers each ask, so the start waits for it, at most 600 s in all`
        ),
      ],
    ]);
    const calls = docker.calls();
    expect(calls.filter((call) => call.args.startsWith("rm "))).toEqual([
      { args: `rm --force --volumes ${standInId}`, at: 0, ended: 600_000, how: "aborted" },
    ]);
    // One ask every 5 s from the first pause to the bound, each answered at once.
    expect(calls.filter((call) => call.args.startsWith("ps ")).map((call) => call.at)).toEqual(
      Array.from({ length: 119 }, (_, index) => (index + 1) * 5_000)
    );
  } finally {
    lines.mockRestore();
  }
});

// Another start's removal that Docker never ends, within the start's own bounds: Docker refuses
// this start's `docker rm` as already in progress, and lists the container at each ask, as moby
// does while the other removal's kill of a container that runs waits on containerd (the row of
// `docker inspect` reading `running` above names the source lines). On a clock the row moves
// (`rowMachine`), the start sends no `docker rm` again, asks every 5 s, says that it waits at 60 s
// and gives the container up at 600 s and no sooner. A wait of the other removal with no bound, a
// second `docker rm`, no line, or a bound of any other length fails it.
test("v-no-leftover-postgres: a start whose docker rm of a dead stack's container Docker refuses as already in progress, while Docker lists it at every ask, within the start's own bounds, sends no docker rm again, says that it waits at 60 s and gives the container up at 600 s and no sooner, on a clock the row moves", async () => {
  const refusal = `Error response from daemon: removal of container ${standInId} is already in progress`;
  const docker = rowMachine((args) =>
    args[0] === "rm" ? { after: 0, stderr: `${refusal}\n` } : { after: 0, stdout: `${standInId}\n` }
  );
  const said: [number, string][] = [];
  const lines = spyOn(console, "error").mockImplementation((...words: unknown[]) => {
    said.push([docker.machine.now(), words.join(" ")]);
  });
  try {
    const removal = await docker.drive(
      removeContainer(standInId, undefined, removalBounds, docker.machine)
    );

    expect(removal).toEqual({
      kind: "failed",
      reason: `${refusal}, and Docker still lists it 600 s after this start's removal began`,
    });
    expect(said).toEqual([
      [
        60_000,
        expect.stringContaining(
          `: waits for Docker's removal of container ${standInId.slice(0, 12)}: the removal has run 60 s and Docker answers each ask, so the start waits for it, at most 600 s in all`
        ),
      ],
    ]);
    const calls = docker.calls();
    expect(calls.filter((call) => call.args.startsWith("rm "))).toEqual([
      { args: `rm --force --volumes ${standInId}`, at: 0, ended: 0, how: "answered" },
    ]);
    // One ask every 5 s from the first pause to the bound, each answered at once.
    expect(calls.filter((call) => call.args.startsWith("ps ")).map((call) => call.at)).toEqual(
      Array.from({ length: 119 }, (_, index) => (index + 1) * 5_000)
    );
  } finally {
    lines.mockRestore();
  }
});

/** Each place in `text`, the source of the container removal module, that reads a clock or runs a
 *  program other than through the machine's own (`machine`), and each number in that machine, as
 *  "<line>:<column> <text>": a limit the removal set on the real clock, which the row on the row's
 *  clock cannot see, is one of them. The names it reads are the globals that read the time or wait
 *  (`Date`, `performance`, `setTimeout`, `setInterval`, `setImmediate`, `Bun`, `process`),
 *  `AbortSignal.timeout`, and every name an import of `node:timers`, `node:timers/promises` or
 *  `node:child_process` binds. */
function offTheMachine(text: string): string[] {
  const source = ts.createSourceFile("removal.ts", text, ts.ScriptTarget.Latest, true);
  const real = new Set([
    "Date",
    "performance",
    "setTimeout",
    "setInterval",
    "setImmediate",
    "Bun",
    "process",
  ]);
  let machine: ts.Node | undefined;
  for (const statement of source.statements) {
    if (
      ts.isImportDeclaration(statement) &&
      ts.isStringLiteral(statement.moduleSpecifier) &&
      /^(node:)?(timers|timers\/promises|child_process)$/.test(statement.moduleSpecifier.text)
    ) {
      const clause = statement.importClause;
      if (clause?.name !== undefined) real.add(clause.name.text);
      const bindings = clause?.namedBindings;
      if (bindings !== undefined && ts.isNamespaceImport(bindings)) real.add(bindings.name.text);
      if (bindings !== undefined && ts.isNamedImports(bindings)) {
        for (const element of bindings.elements) real.add(element.name.text);
      }
    }
    if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name) && declaration.name.text === "machine") {
          machine = declaration;
        }
      }
    }
  }
  const found: string[] = machine === undefined ? ["no declaration of machine"] : [];
  const inMachine = (node: ts.Node) =>
    machine !== undefined && node.pos >= machine.pos && node.end <= machine.end;
  const place = (node: ts.Node) => {
    const { line, character } = source.getLineAndCharacterOfPosition(node.getStart(source));
    return `${line + 1}:${character + 1} ${node.getText(source)}`;
  };
  const visit = (node: ts.Node): void => {
    if (ts.isImportDeclaration(node)) return;
    if (ts.isIdentifier(node) && real.has(node.text) && !inMachine(node)) {
      const { parent } = node;
      // A name after a dot, or the name of a property or a method, is no global and no import.
      const property =
        (ts.isPropertyAccessExpression(parent) && parent.name === node) ||
        ((ts.isPropertyAssignment(parent) ||
          ts.isPropertySignature(parent) ||
          ts.isMethodSignature(parent)) &&
          parent.name === node);
      if (!property) found.push(place(node));
    }
    if (
      ts.isPropertyAccessExpression(node) &&
      node.name.text === "timeout" &&
      node.expression.getText(source) === "AbortSignal"
    ) {
      found.push(place(node));
    }
    if (ts.isNumericLiteral(node) && inMachine(node)) found.push(place(node));
    ts.forEachChild(node, visit);
  };
  ts.forEachChild(source, visit);
  return found;
}

// The previous row sees a limit of the removal on the row's clock; one on the real clock never
// fires in its moment of real time. So this row reads the removal module's source: it reads the
// time, waits and runs `docker` only through its machine, and the machine's own sets no number.
// Its controls are such limits, planted in copies of the source, which it must name.
test("v-no-leftover-postgres: a start's removal of a dead stack's container reads the time, waits and runs docker only through its machine, which sets no limit of its own", () => {
  const text = readFileSync(
    fileURLToPath(new URL("../harness-postgres-container-removal.ts", import.meta.url)),
    "utf8"
  );

  expect(offTheMachine(text)).toEqual([]);
  expect(offTheMachine(`${text}\nsetTimeout(() => undefined, 120_000);\n`)).toEqual([
    expect.stringMatching(/^\d+:1 setTimeout$/),
  ]);
  expect(
    offTheMachine(`${text}\nconst limit = AbortSignal.any([AbortSignal.timeout(120_000)]);\n`)
  ).toEqual([expect.stringMatching(/^\d+:\d+ AbortSignal\.timeout$/)]);
  const capped = text.replace("sleep(ms, undefined,", "sleep(Math.min(ms, 120_000), undefined,");
  expect(capped).not.toBe(text);
  expect(offTheMachine(capped)).toEqual([expect.stringMatching(/^\d+:\d+ 120_000$/)]);
});

/** The rows of `source`, a test file these fixtures serve, named `file`, whose limit is not the
 *  program's sum of the waits they count, one line each, with the row's line and what it states:
 *  a row whose body counts its waits (`rowOf(<waits>)`) and states anything but `rowMs` of those
 *  same waits, a number above all, and a row that states `rowMs` of waits its body does not count.
 *  The row's charge (`Row`) holds each fixture to the waits the row counts; this holds the row's
 *  own clock, which Bun runs and no fixture sees, to the same waits. A row is a call of `test`, or
 *  of what `test` reaches through dotted members and calls (`test.skipIf(<x>)`), whose second
 *  argument is its body and whose third is its limit. */
function limitsNotSummed(file: string, source: string): string[] {
  const parsed = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true);
  const isRow = (callee: ts.Expression): boolean =>
    ts.isIdentifier(callee)
      ? callee.text === "test"
      : (ts.isPropertyAccessExpression(callee) || ts.isCallExpression(callee)) &&
        isRow(callee.expression);
  // The argument of a call of `name`, as the file writes it, where `node` is one.
  const argumentOf = (node: ts.Node | undefined, name: string) =>
    node !== undefined &&
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === name
      ? (node.arguments[0]?.getText(parsed) ?? "")
      : undefined;
  const found: string[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isCallExpression(node) && isRow(node.expression) && node.arguments.length >= 2) {
      const [, body, limit] = node.arguments;
      const counted: string[] = [];
      const count = (inner: ts.Node): void => {
        const waits = argumentOf(inner, "rowOf");
        if (waits !== undefined) counted.push(waits);
        ts.forEachChild(inner, count);
      };
      if (body !== undefined) count(body);
      const summed = argumentOf(limit, "rowMs");
      const stated = limit?.getText(parsed) ?? "no limit";
      const line = parsed.getLineAndCharacterOfPosition(node.getStart(parsed)).line + 1;
      if (counted.length > 0 && counted.some((waits) => waits !== summed)) {
        found.push(`${file}:${line}: counts rowOf(${counted.join("), rowOf(")}), states ${stated}`);
      } else if (counted.length === 0 && summed !== undefined) {
        found.push(`${file}:${line}: states ${stated}, counts no rowOf(${summed})`);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(parsed);
  return found;
}

/** The test files whose rows count their waits with these fixtures (`rowOf`). */
const summedFiles = [
  "harness-postgres-holders.test.ts",
  "harness-postgres-image-mount.test.ts",
  "harness-postgres-image-port.test.ts",
  "harness-postgres-image-ready.test.ts",
  "harness-postgres-leftover.test.ts",
  "harness-postgres-marks.test.ts",
  "harness-postgres-removal.test.ts",
];

// A row's limit is Bun's clock, which no fixture sees: a limit written as a number beside a row
// that counts its waits fires inside the bounds of what the row waits for, as a start of the pool
// given a fixed 600 s did on 2026-11-19 while its removal of one container could wait 600 s. So
// each row of these files states `rowMs` of the waits it counts. The controls are rows planted
// in a copy of a file: one that counts its waits and states a number, and one that states the sum
// of waits it does not count, each of which the check must name.
test("every row that counts its waits states its limit as the program's sum of those waits", () => {
  for (const name of summedFiles) {
    const text = readFileSync(fileURLToPath(new URL(`./${name}`, import.meta.url)), "utf8");
    expect({ name, found: limitsNotSummed(name, text) }).toEqual({ name, found: [] });
  }
  const planted = [
    "const waits: Waits = { children: 1, reads: 0, bounds: [] };",
    'test.skipIf(false)("a number", async () => {\n  rowOf(waits);\n}, 600_000);',
    'test("another\'s sum", async () => {\n  rowOf(waits);\n}, rowMs(other));',
    'test("a sum it does not count", async () => {}, rowMs(waits));',
    'test("the program\'s", async () => {\n  rowOf(waits);\n}, rowMs(waits));',
  ].join("\n");
  expect(limitsNotSummed("planted.test.ts", planted)).toEqual([
    "planted.test.ts:2: counts rowOf(waits), states 600_000",
    "planted.test.ts:5: counts rowOf(waits), states rowMs(other)",
    "planted.test.ts:8: states rowMs(waits), counts no rowOf(waits)",
  ]);
});

/** What the charge of a row whose limit counts `counted` says of its waits for `waited`, one after
 *  another (`Row.part`): "counted" where the limit counts each of them, and else the refusal of
 *  the first it does not. The row of the next row's own, which waits for nothing. */
function chargeOf(counted: Waits, ...waited: readonly Part[]): string {
  const row = rowOf(counted);
  try {
    for (const part of waited) row.part(part);
    return "counted";
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

// A fixture that waits for a part (`Part`), such as a start of the pool, charges its row before
// the wait, and a row whose limit counts no such part with a bound as long fails there, naming it,
// whatever the load. Parts waited for at once count once, at the longest, and each counted part
// serves one wait.
test("a row whose limit counts a part below the bound of the part a fixture waits for fails there, naming it", () => {
  const short = partOf("a start of the pool", { children: 1, reads: 0, bounds: [] });
  const long = partOf("a start of the pool", { children: 1, reads: 1, bounds: [] });
  const atOnce: Waits = { children: 0, reads: 0, bounds: [], parts: [{ atOnce: [long, short] }] };
  expect(long.ms).toBeGreaterThan(short.ms);
  expect(chargeOf({ children: 0, reads: 0, bounds: [], parts: [short] }, long)).toBe(
    `the row's limit counts ${short.ms / 1000} s for a start of the pool it has not waited for yet, and it waits for a start of the pool for up to ${long.ms / 1000} s: its limit is below what it waits for`
  );
  expect(chargeOf({ children: 0, reads: 0, bounds: [long.ms] }, short)).toBe(
    `the row's limit counts nothing for a start of the pool it has not waited for yet, and it waits for a start of the pool for up to ${short.ms / 1000} s: its limit is below what it waits for`
  );
  expect(chargeOf(atOnce, short, long)).toBe("counted");
  expect(chargeOf(atOnce, short, long, short)).toContain("its limit is below what it waits for");
  expect(rowMs(atOnce)).toBe(long.ms + rowRoomMs);
});

/** How long the next row's fixture call may run before the row's deadline: the row's own, short
 *  so the row takes seconds. A fixture call waits for the time its row has left, which the program
 *  of a row's limit counts (`makeMs`), and no fixed time of its own. */
const ranOutMs = 5_000;
/** What the next row waits for (`Waits`): one child, the fixture call's wait to its deadline, and
 *  the room above that deadline in which it removes what the call made (`rowRoomMs`). */
const ranOut: Waits = { children: 1, reads: 0, bounds: [ranOutMs, rowRoomMs] };

// A daemon that many sessions share took 133 s over one `docker create` of the leftover rows on
// 2026-11-19, while it answered every other call; the fixture then gave the create up at a fixed
// 120 s, and the container Docker made stayed, Created. A fixture call of Docker now waits for
// the time its row has left (`docker` of harness-postgres-leftover-fixtures.ts), and one that
// runs out that time names the call on one line, removes what it made, by the name and the label
// it gave, and fails the row with that line. The stand-in `docker` makes the container and never
// answers the create; it lists the container by that name and label while it has it, and its
// `docker rm` removes it.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a fixture's docker create that Docker never answers ends at the row's deadline, names the call on one line, removes the container it made by its name and label, and fails the row with that line",
  async () => {
    const row = rowOf(ranOut);
    const { tmp } = scratch();
    const name = `dispatch-e2e-pg-ran-out-${process.pid}`;
    const label = "dev.legion.dispatch-e2e.ran-out";
    try {
      const docker = standInDocker(tmp, [
        'made="$(dirname "$0")/made"',
        'case "$1" in',
        `  create) : >"$made"; exec -a "$0" sleep 600 ;;`,
        `  ps) if [ -e "$made" ] && [[ " $* " == *" name=^/?${name}$ "* ]] && [[ " $* " == *" label=${label} "* ]]; then echo ${standInId}; fi ;;`,
        `  rm) rm -f "$made"; echo "$4" ;;`,
        "esac",
      ]);
      const args = ["create", "--name", name, "--label", `${label}=1`, "postgres"];

      const { ended, lines } = await fixtureDockerInChild(row, docker.bin, args, {
        children: 0,
        reads: 0,
        bounds: [ranOutMs],
      });

      const line = `e2e/__tests__/harness-postgres-leftover-fixtures.ts: docker ${args.join(" ")} did not end within the ${ranOutMs / 1000} s the row waits in all, so the row stopped it, and removed container ${standInId.slice(0, 12)} (${name}) it made`;
      expect(ended).toEqual({ error: line });
      expect(holding(lines, ": docker create ")).toEqual([line]);
      expect(existsSync(join(docker.bin, "made"))).toBe(false);
      // The calls but the removal's own asks of whether Docker still lists the container, which a
      // `docker rm` that load holds past one pause makes (`removeContainer`).
      expect(
        docker
          .calls()
          .filter((call) => !call.startsWith("ps --all --quiet --no-trunc --filter id="))
      ).toEqual([
        args.join(" "),
        `ps --all --quiet --no-trunc --filter name=^/?${name}$ --filter label=${label}`,
        `rm --force --volumes ${standInId}`,
      ]);
      // On the stand-in's clock: the row stopped the create at its deadline, and no sooner.
      const ticks = docker.clock();
      const created = ticks.find((tick) => tick.call.startsWith("create "))?.at ?? Number.NaN;
      const listed = ticks.find((tick) => tick.call.startsWith("ps "))?.at ?? Number.NaN;
      expect(listed - created).toBeGreaterThan(ranOutMs / 1000 - 1);
      expect(listed - created).toBeLessThan(ranOutMs / 1000 + clockRoomS);
      await until(
        () => docker.running().length === 0,
        "the end of the docker create the row stopped",
        claimEventMs
      );
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child, the fixture
  // call's wait to its deadline and the removal of what the call made.
  rowMs(ranOut)
);

/** How long the next row's child waits, a wait its row names, before its fixture call: past the
 *  row's deadline (`ranOutMs`), by more than the room one start of a stand-in takes under load. */
const usedUpMs = ranOutMs + 3_000;
/** What the next row waits for (`Waits`): one child, and in it the named wait past the deadline
 *  and the fixture call after it, which starts nothing. */
const usedUp: Waits = { children: 1, reads: 0, bounds: [usedUpMs] };

// The image-ready row ran out its deadline on 2026-11-19 in its removal of the builder of its
// image, which Docker took 17 and 21 minutes over while its daemon stalled. The row's next fixture
// call, a `psql`, then started after the deadline, was stopped at once, and its line said that the
// `psql` "did not end within" the row's time, naming a call that had had no time at all. A fixture
// call that its row's deadline finds already passed now starts nothing, and its line says so and
// names the wait during which the deadline passed (`during` of harness-postgres-leftover-fixtures.ts).
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a fixture's docker call that its row's deadline finds already passed starts nothing, says the deadline had passed before it, and names the wait that used it up",
  async () => {
    const row = rowOf(usedUp);
    const { tmp } = scratch();
    try {
      const docker = standInDocker(tmp, ["echo answered"]);
      const args = ["ps", "--all", "--quiet"];
      const what = "the row's own wait past its deadline";

      const { ended, lines } = await fixtureDockerInChild(
        row,
        docker.bin,
        args,
        { children: 0, reads: 0, bounds: [ranOutMs] },
        { before: { what, ms: usedUpMs } }
      );

      const line = new RegExp(
        `^e2e/__tests__/harness-postgres-leftover-fixtures\\.ts: docker ${args.join(" ")} did not start: the ${ranOutMs / 1000} s the row waits in all had run out \\d+\\.\\d s before it, during ${what}, from \\d+\\.\\d s to \\d+\\.\\d s of the row$`
      );
      expect(ended).toEqual({ error: expect.stringMatching(line) });
      expect(holding(lines, ": docker ps ")).toEqual([expect.stringMatching(line)]);
      // The call started nothing: the stand-in `docker` got no call.
      expect(existsSync(join(docker.bin, "calls"))).toBe(false);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child, and in it the
  // named wait past the deadline.
  rowMs(usedUp)
);

/** How long the next row's `finally` takes over its stop, as a leftover row's stop of a stack it
 *  started does: more than the half of `rowRoomMs` that a removal of what the row made has above
 *  the row's deadline, so the removal after it has time left only where the deadline did not count
 *  the stop (`Waits.ends`). */
const endStopMs = rowRoomMs / 2 + 5_000;
/** The bound the next row's `finally` gives that stop (`Waits.ends`), with room above the time it
 *  takes for a slow start of the child's sleep. */
const endBoundMs = endStopMs + 10_000;
/** What the next row's child waits for (`Waits`): the fixture call's wait to its deadline, and, in
 *  its `finally`, the stop, before the removal of what the row made. */
const ranOutThenStop: Waits = { children: 0, reads: 0, bounds: [ranOutMs], ends: [endBoundMs] };
/** What the next row waits for (`Waits`): the child, and everything the child's row waits for. */
const ranOutInChild: Waits = { ...ranOutThenStop, children: 1 };

// The image row of the leftovers ran out its deadline on 2026-11-19 with a create that Docker
// never answered, and its `finally` then stopped a stack it started, for about 30 s, before it
// removed the container of the stack it had killed: the deadline had counted that stop, so the
// removal's listing had no time left, and that stack's container stayed, with a mark no later
// start could judge. A row's deadline now ends its own waits alone, and its `finally` has the time
// its stops take after it (`Waits.ends`), and then half the room above them for its removals
// (`Row.cleanup`). The child's create never ends; its `finally` stops for `endStopMs`, and then
// lists the containers whose mark names the row's run and removes each.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a row whose fixture call runs out the row's deadline still stops what it started in its finally, and then lists and removes the containers of its stacks",
  async () => {
    const row = rowOf(ranOutInChild);
    const { tmp, run } = scratch();
    const name = `dispatch-e2e-pg-ran-out-stop-${process.pid}`;
    const label = "dev.legion.dispatch-e2e.ran-out";
    const killedId = "1".repeat(64);
    const mark = JSON.stringify({ run });
    try {
      const docker = standInDocker(tmp, [
        'made="$(dirname "$0")/made"',
        'case "$1" in',
        `  create) : >"$made"; exec -a "$0" sleep 600 ;;`,
        `  ps) if [[ " $* " == *" --format "* ]]; then printf '%s %s\\n' ${killedId} ${JSON.stringify(mark)}; elif [ -e "$made" ]; then echo ${standInId}; fi ;;`,
        `  rm) rm -f "$made"; echo "$4" ;;`,
        "esac",
      ]);
      const args = ["create", "--name", name, "--label", `${label}=1`, "postgres"];

      const { ended } = await fixtureDockerInChild(row, docker.bin, args, ranOutThenStop, {
        end: { stopMs: endStopMs, run },
      });

      expect(ended).toEqual({
        error: expect.stringContaining(`docker ${args.join(" ")} did not end within the `),
        removed: true,
      });
      const calls = docker.calls();
      expect(calls).toContain(
        `ps --all --no-trunc --filter label=${marks.markLabel} --format {{.ID}} {{.Label "${marks.markLabel}"}}`
      );
      expect(calls).toContain(`rm --force --volumes ${killedId}`);
      // On the stand-in's clock: the listing came after the deadline and the stop, past the half
      // of the room a removal had above the deadline where the deadline counted the stop.
      const ticks = docker.clock();
      const created = ticks.find((tick) => tick.call.startsWith("create "))?.at ?? Number.NaN;
      const listed =
        ticks.find((tick) => tick.call.startsWith("ps ") && tick.call.includes("--format"))?.at ??
        Number.NaN;
      expect(listed - created).toBeGreaterThan((ranOutMs + endStopMs) / 1000 - 1);
      await until(
        () => docker.running().length === 0,
        "the end of the docker create the row stopped",
        claimEventMs
      );
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one child, the fixture
  // call's wait to its deadline, the stop of its finally, and the removal of what the row made.
  rowMs(ranOutInChild)
);

/** What the next rows wait for (`Waits`): one sweep in a child, the bound of its removal of the
 *  first container, three holder reads (the next container's socket directory, and the run
 *  directory's data and socket directories), and then the end of each `docker` it ran. */
const sweepOfTwo: Waits = { children: 1, reads: 3, bounds: [rowBounds.boundMs, claimEventMs] };

/** Two containers a sweep lists, each with the mark of a stack that is gone, in the listing file
 *  `listing` of `tmp`, and a data directory with its mark in `run`: the first container's removal
 *  is the row's case, and the second, which the start removes, shows that the sweep goes on. */
function twoContainers(tmp: string, run: string) {
  const gone = owners().gone;
  const stuck = { id: "1".repeat(64), name: "dispatch-e2e-pg-stuck-0" };
  const next = { id: "2".repeat(64), name: "dispatch-e2e-pg-next-1" };
  const listing = join(tmp, "listing");
  writeFileSync(
    listing,
    [stuck, next]
      .map(
        ({ id, name }) => `${id} ${name} ${JSON.stringify(marks.containerMark(gone, run, name))}\n`
      )
      .join("")
  );
  const data = join(run, "worker-0", "pgdata");
  leaveData(data);
  const file = writeMark(run, gone, data, mkdtempSync(join(tmp, "no-bin-")));
  const lists = `  ps) if [[ " $* " == *" --format "* ]]; then cat ${JSON.stringify(listing)}; else echo "\${6#id=}"; fi ;;`;
  return { by: `which stack ${gone.pid} left`, stuck, next, data, file, lists };
}

// A removal that Docker never ends while it answers each ask, as of a container whose removal is
// stuck in the daemon: the start says that it waits, gives the removal up at its bound on one line
// that names the container, and goes on, to the next container and the run directories.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start whose removal of a dead stack's container Docker never ends, while Docker answers, says that it waits, gives the removal up at its bound on one line that names the container, and goes on to the next container and the run directories",
  async () => {
    const row = rowOf(sweepOfTwo);
    const { tmp, run } = scratch();
    const { by, stuck, next, data, file, lists } = twoContainers(tmp, run);
    const docker = standInDocker(tmp, [
      'case "$1" in',
      lists,
      `  rm) if [ "$4" = ${stuck.id} ]; then exec -a "$0" sleep 600; fi; echo "$4" ;;`,
      "esac",
    ]);
    try {
      const { lines } = await sweepInChild(row, tmp, docker.bin, rowBounds);

      expect(waitLines(lines, stuck.id)).toEqual([
        expect.stringContaining(`container ${stuck.id.slice(0, 12)} (${stuck.name}), ${by}:`),
      ]);
      expect(holding(lines, `container ${stuck.id.slice(0, 12)} (`)).toContainEqual(
        expect.stringContaining(
          `: container ${stuck.id.slice(0, 12)} (${stuck.name}), ${by}, is not removed: Docker did not end the removal within 90 s, while it answered each ask`
        )
      );
      expect(
        holding(lines, `removed container ${next.id.slice(0, 12)} (${next.name})`)
      ).toHaveLength(1);
      const real = marks.identityOf(run);
      expect([existsSync(data), existsSync(file)]).toEqual([false, false]);
      expect(
        holding(lines, `removed ${join(real, "worker-0", "pgdata")} and ${real}/pg-0-`)
      ).toHaveLength(1);
      expect(
        docker.calls().filter((call) => call === `rm --force --volumes ${stuck.id}`)
      ).toHaveLength(1);
      expectEndedAtBound(docker.clock(), rowBounds.boundMs);
      await until(
        () => docker.running().length === 0,
        "the end of each docker the start ran",
        claimEventMs
      );
    } finally {
      endStandIns(docker);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep, its removal of
  // the stuck container and its three holder reads, and then the end of each `docker` it ran.
  rowMs(sweepOfTwo)
);

// Every answer of Docker about the first container is that another removal of it is in progress,
// and `docker inspect` reads it as `removing`: the start sends no `docker rm` of it again, says that
// it waits, gives it up at its bound on one line that names the container, and goes on to the next
// container and the run directories.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start whose every removal of a dead stack's container Docker refuses as already in progress says that it waits, gives the container up at its bound on one line that names it, and goes on to the next container and the run directories",
  async () => {
    const row = rowOf(sweepOfTwo);
    const { tmp, run } = scratch();
    const { by, stuck, next, data, file, lists } = twoContainers(tmp, run);
    const docker = standInDocker(tmp, [
      'case "$1" in',
      lists,
      `  rm) if [ "$4" = ${stuck.id} ]; then echo "Error response from daemon: removal of container $4 is already in progress" >&2; exit 1; fi; echo "$4" ;;`,
      "  inspect) echo removing ;;",
      "esac",
    ]);
    try {
      const { lines } = await sweepInChild(row, tmp, docker.bin, rowBounds);

      expect(waitLines(lines, stuck.id)).toHaveLength(1);
      expect(holding(lines, `container ${stuck.id.slice(0, 12)} (`)).toContainEqual(
        expect.stringContaining(
          `: container ${stuck.id.slice(0, 12)} (${stuck.name}), ${by}, is not removed: Error response from daemon: removal of container ${stuck.id} is already in progress, and Docker still lists it 90 s after this start's removal began`
        )
      );
      expect(
        docker.calls().filter((call) => call === `rm --force --volumes ${stuck.id}`)
      ).toHaveLength(1);
      expect(
        holding(lines, `removed container ${next.id.slice(0, 12)} (${next.name})`)
      ).toHaveLength(1);
      expect([existsSync(data), existsSync(file)]).toEqual([false, false]);
      expectEndedAtBound(docker.clock(), rowBounds.boundMs);
    } finally {
      endStandIns(docker);
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep, its wait for the
  // first container's bound and its three holder reads, and then the end of each `docker` it ran.
  rowMs(sweepOfTwo)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a stack under another TMPDIR whose stack runs is not touched, and the start prints nothing of it",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().runs);
    try {
      const lines = await sweep(row, tmp);

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
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a postmaster of this user under a run directory of another TMPDIR that no mark names is left and named",
  async () => {
    const row = rowOf(oneSweep);
    const { tmp } = scratch();
    const left = leaveElsewhere();
    try {
      const lines = await sweep(row, tmp);

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
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): one sweep.
  rowMs(oneSweep)
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

/** What the next row waits for (`Waits`): two sweeps, each a child; the first's holder reads of
 *  the data directory before its claim, and of the socket directory, and the second's of the
 *  socket directory, where it gets there first; the first's stop of the postmaster, with its reads
 *  after it; and the second's wait for the first's claim. */
const twoOnOne: Waits = { children: 2, reads: 3, stops: 1, bounds: [claimWaitMs] };

// Two starts at once on one dead mark whose postmaster still serves. The first claims the data
// directory (`claimForRemoval`) and signals the postmaster, which then runs on until the row says
// go; the second starts only after that signal, so it finds the claim held, and says that it waits
// for it. Only then does the postmaster end.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: of two starts on one dead mark whose postmaster serves, the one that claims it sends the one signal and says it removed it, and the other waits for that claim and says another start removed it",
  async () => {
    const row = rowOf(twoOnOne);
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
      const first = startSweep(row, tmp);
      // The signal comes after the first sweep's start and its holder read before the claim.
      await until(
        () => signalled().length > 0,
        "the first start's signal",
        waitsMs({ children: 1, reads: 1, bounds: [] })
      );
      const second = startSweep(row, tmp);
      await until(
        () =>
          second.stderr().includes(`waits for another start's claim of ${data}`) ||
          signalled().length > 1,
        "the second start's wait for the first start's claim, or its own signal",
        waitsMs({ children: 1, reads: 1, bounds: [] })
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
  // What the row waits for, summed by the fixtures' program (`rowMs`): two sweeps, their four
  // holder reads, the first's stop and the second's wait for its claim.
  rowMs(twoOnOne)
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

/** What the next row waits for (`Waits`): a claimer, a child that prints its claim, and then one
 *  sweep, a child that claims the data directory again, stops its postmaster and removes the
 *  socket directory, with a holder read before each claim, and its stop, with its reads after it. */
const claimThenSweep: Waits = { children: 2, reads: 2, stops: 1, bounds: [] };

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: what a start killed between its claim and its removal left, the next start claims again: the postmaster that serves from the claimed directory ends, and the directory goes",
  async () => {
    const row = rowOf(claimThenSweep);
    const { tmp, run } = scratch();
    const bin = join(tmp, "bin");
    const data = join(run, "worker-0", "pgdata");
    const postmaster = standInPostmaster(bin, data, "its TMPDIR");
    const mark = writeMark(run, owners().gone, data, bin);
    try {
      const claimed = await claimAndDie(row, data);
      expect([existsSync(data), existsSync(claimed), postmaster.alive()]).toEqual([
        false,
        true,
        true,
      ]);

      const lines = await sweep(row, tmp);

      expect(postmaster.alive()).toBe(false);
      expect([existsSync(claimed), existsSync(mark)]).toEqual([false, false]);
      expect(holding(lines, `removed the postmaster of ${data}`)).toHaveLength(1);
    } finally {
      postmaster.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // What the row waits for, summed by the fixtures' program (`rowMs`): a claimer, and one sweep
  // with its holder reads and its stop of a postmaster.
  rowMs(claimThenSweep)
);

/** What the next row waits for (`Waits`): `claimThenSweep`, and then what another start of the
 *  machine may still be removing of the plant (`settleMs`). */
const claimThenSweepElsewhere: Waits = { children: 2, reads: 2, stops: 1, bounds: [settleMs] };

// The claim comes before the postmaster here: until a postmaster serves from it, no start but
// this row's own reads the run directory, so the claimed directory is there for the row to see.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: what a start killed between its claim and its removal left in a run directory of another TMPDIR, a start of this TMPDIR finds by the claimed name the kernel shows: the postmaster ends, the directory and the mark go, and one line says so",
  async () => {
    const row = rowOf(claimThenSweepElsewhere);
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().gone, false);
    let postmaster: StandIn | undefined;
    try {
      const claimed = await claimAndDie(row, left.data);
      expect([existsSync(left.data), existsSync(claimed)]).toEqual([false, true]);
      // The kernel names the postmaster's working directory by the claim's name.
      postmaster = standInPostmaster(left.bin, claimed, "every start");

      const lines = await sweep(row, tmp);

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
  // What the row waits for, summed by the fixtures' program (`rowMs`): a claimer, one sweep with
  // its holder reads and its stop of a postmaster, and then `settleMs`.
  rowMs(claimThenSweepElsewhere)
);

/** What the next row waits for (`Waits`): a claimer and two sweeps, each a child; each sweep's
 *  holder reads of the stacks it removes, a read before each claim, two claims a stack, and one
 *  more, where the first start meets the second's socket directory before the second claimed it;
 *  each sweep's wait for the claim the claimer holds; and the stops of both postmasters, each with
 *  its reads after it. */
const claimAndTwo: Waits = {
  children: 3,
  reads: 5,
  stops: 2,
  bounds: [claimWaitMs, claimWaitMs, settleMs],
};

// Two starts at once. The first, Z, lists two postmasters of run directories it does not list,
// and finds the data directory of the first claimed by a claimer that still runs, so it waits for
// that claim. Meanwhile the second, X, removes the stack of the second run directory, which is in
// its own TMPDIR. Once the claimer is killed, Z goes on to that second run directory, whose mark
// and postmaster X removed, so that postmaster is no Postgres Z left. Each postmaster serves only
// once the claim is made, as in the row above, so no other start can take the held stack first.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start names as left only a postmaster that still runs once it has read its run directory: one it listed that another start removed meanwhile it does not name",
  async () => {
    const row = rowOf(claimAndTwo);
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
      const claim = await claimInChild(row, held.data);
      claimer = claim.claimer;
      heldPostmaster = standInPostmaster(held.bin, claim.claimed, "every start");
      otherPostmaster = standInPostmaster(other.bin, other.data, "every start");
      const first = startSweep(row, startTmp);
      // The first start's wait comes after its start, and the first stack it reads is the held one.
      await until(
        () => first.stderr().includes(`waits for another start's claim of ${held.data}`),
        "the first start's wait for the claim the claimer holds",
        waitsMs({ children: 1, reads: 0, bounds: [] })
      );
      const second = startSweep(row, otherTmp);
      const otherServes = otherPostmaster;
      // The second start's stop comes after its start and its holder read before the claim.
      await until(
        () => !otherServes.alive(),
        "the second start's stop",
        waitsMs({ children: 1, reads: 1, bounds: [] })
      );
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
  // What the row waits for, summed by the fixtures' program (`rowMs`): a claimer and two sweeps,
  // with their holder reads, their waits for the claim and their stops of the two postmasters.
  rowMs(claimAndTwo)
);
