/** Rows of the check `v-no-leftover-postgres` (requirement R3 of pull request #4236) for a dead
 *  stack whose directories a process of its user still holds: "It removes no Postgres of a stack
 *  that still runs." A stack killed hard while its `pg_ctl start`, `initdb` or `docker create`
 *  runs leaves that child running, so a start leaves each directory such a process holds, with
 *  its mark, names it in one line, and never renames it, and the next start after that process
 *  ended removes it. Each row holds a directory one way: by an argument of a `pg_ctl`, however
 *  the stack spelt it, by a working directory alone, by an open file alone, by a mount of the
 *  socket directory, and by a postmaster's process that outlives it a moment. The fixtures and
 *  the way the rows run the start's sweep are those of `harness-postgres-removal.test.ts`
 *  (`harness-postgres-removal-fixtures.ts`), under a TMPDIR of each row's own. */

import { expect, test } from "bun:test";
import {
  closeSync,
  existsSync,
  mkdtempSync,
  openSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, join } from "node:path";

import * as marks from "../harness-postgres-marks";
import {
  claimEventMs,
  endChild,
  firstLineOf,
  harnessScript,
  holding,
  leaveData,
  owners,
  scratch,
  slowReleasingPostmaster,
  startingPgCtl,
  sweep,
  until,
  writeMark,
} from "./harness-postgres-removal-fixtures";
import { type StandIn, standIn } from "./planted-marks";

const { marksDirectory } = marks;

const onLinux = process.platform === "linux";

/** How a stack spells its directories to its `pg_ctl` in the rows below: as the pool makes them;
 *  under a TMPDIR spelt through a link of its user's, which the README allows, and under such a
 *  link that is gone when the next start sweeps, so that the spelling no longer leads to the
 *  directory and only the mark's own spellings name it; with a `-D` relative to the directory
 *  `pg_ctl` runs in; and with a doubled `/`. The mark holds its run directory's one spelling and
 *  the spellings the stack gave it, and a start judges a holder by the directory an argument
 *  names, not by its spelling. `spell` answers the `-D` argument and the directory `pg_ctl` runs
 *  in, for the run directory `run` as the stack spells it. */
const pgCtlSpellings: readonly {
  readonly title: string;
  readonly link: "none" | "kept" | "gone";
  readonly spell: (run: string) => {
    readonly HARNESS_DATA?: string;
    readonly HARNESS_CWD?: string;
  };
}[] = [
  {
    title:
      "v-no-leftover-postgres: a start leaves the Postgres of a stack killed while its pg_ctl start runs, and names it in one line, since that pg_ctl still holds its directories; the next start after that pg_ctl ended removes the postmaster it started",
    link: "none",
    spell: () => ({}),
  },
  {
    title:
      "v-no-leftover-postgres: under a TMPDIR spelt through a link, a start leaves the Postgres of a stack killed while its pg_ctl start runs, and names it in one line, though that pg_ctl names its directories through the link; the next start after that pg_ctl ended removes the postmaster it started",
    link: "kept",
    spell: () => ({}),
  },
  {
    title:
      "v-no-leftover-postgres: under a TMPDIR spelt through a link that is gone when the start sweeps, a start leaves the Postgres of a stack killed while its pg_ctl start runs, and names it in one line, by the spelling its mark holds; the next start after that pg_ctl ended removes the postmaster it started",
    link: "gone",
    spell: () => ({}),
  },
  {
    title:
      "v-no-leftover-postgres: a start leaves the Postgres of a stack killed while its pg_ctl start runs with a -D relative to its working directory, and names it in one line; the next start after that pg_ctl ended removes the postmaster it started",
    link: "none",
    spell: (run) => ({ HARNESS_DATA: "pgdata", HARNESS_CWD: join(run, "worker-0") }),
  },
  {
    title:
      "v-no-leftover-postgres: a start leaves the Postgres of a stack killed while its pg_ctl start runs with a -D spelt with a doubled /, and names it in one line; the next start after that pg_ctl ended removes the postmaster it started",
    link: "none",
    spell: (run) => ({ HARNESS_DATA: `${run}//worker-0/pgdata` }),
  },
];

// A stack killed hard while its `pg_ctl start` runs leaves that pg_ctl running, and the
// postmaster it then starts. A start in between finds no postmaster.pid yet: it must leave the
// directories that pg_ctl names, with the mark, and name them, and the next start after that
// pg_ctl ended removes the postmaster it started.
for (const { title, link, spell } of pgCtlSpellings) {
  test.skipIf(!onLinux)(
    title,
    async () => {
      const { tmp, run: own } = scratch();
      // The TMPDIR the stack and the starts are given, and the run directory as the stack spells
      // it there; the lines name the run directory's one spelling.
      const given = link === "none" ? tmp : `${tmp}-link`;
      if (link !== "none") symlinkSync(tmp, given);
      const run = join(given, basename(own));
      const bin = join(tmp, "bin");
      const go = join(tmp, "go");
      const sockets = mkdtempSync(join(run, "pg-0-"));
      const data = join(run, "worker-0", "pgdata");
      const [named, namedSockets] = [join(own, "worker-0", "pgdata"), join(own, basename(sockets))];
      const mark = join(own, marksDirectory, "worker-0.json");
      const spelt = spell(run);
      startingPgCtl(bin, go);
      const harness = Bun.spawn([process.execPath, "--eval", harnessScript], {
        env: {
          ...process.env,
          HARNESS_RUN: run,
          HARNESS_BIN: bin,
          HARNESS_SOCKETS: sockets,
          ...spelt,
        },
        stdout: "pipe",
        stderr: "ignore",
      });
      let pgCtl: { pid: number; start: string } | undefined;
      let postmaster: { pid: number; start: string } | undefined;
      const startOfPid = (pid: number) => marks.startOf(readFileSync(`/proc/${pid}/stat`, "utf8"));
      try {
        const pid = Number(await firstLineOf(harness));
        // The harness's child runs pg_ctl once it has its arguments, which name the data
        // directory as the stack spells it.
        const argument = spelt.HARNESS_DATA ?? data;
        await until(
          () => readFileSync(`/proc/${pid}/cmdline`, "utf8").split("\0").includes(argument),
          "the start of the stack's pg_ctl",
          claimEventMs
        );
        pgCtl = { pid, start: startOfPid(pid) };
        // The harness dies as a stack killed hard does: its pg_ctl runs on.
        await endChild(harness);
        expect(marks.stillRuns(pgCtl.pid, pgCtl.start)).toBe(true);
        // A link that is gone leads nowhere: the first start lists the TMPDIR it led to.
        if (link === "gone") rmSync(given);

        const first = await sweep(link === "gone" ? tmp : given);

        expect([existsSync(named), existsSync(namedSockets), existsSync(mark)]).toEqual([
          true,
          true,
          true,
        ]);
        expect(holding(first, "removed")).toEqual([]);
        const left = holding(first, mark);
        expect(left).toEqual([expect.stringContaining(`${mark} (${named}, which `)]);
        expect(left[0]).toContain(` ${pgCtl.pid} still holds)`);

        // The link again, for the pg_ctl, which goes into its data directory as it spells it.
        if (link === "gone") symlinkSync(tmp, given);

        writeFileSync(go, "");
        const ended = pgCtl;
        await until(
          () => !marks.stillRuns(ended.pid, ended.start),
          "the end of the stack's pg_ctl",
          claimEventMs
        );
        const served = Number.parseInt(readFileSync(join(data, "postmaster.pid"), "utf8"), 10);
        postmaster = { pid: served, start: startOfPid(served) };
        const second = await sweep(given);

        expect(marks.stillRuns(postmaster.pid, postmaster.start)).toBe(false);
        expect([existsSync(data), existsSync(sockets), existsSync(mark)]).toEqual([
          false,
          false,
          false,
        ]);
        expect(
          holding(second, `removed the postmaster of ${named}, that directory and ${namedSockets}`)
        ).toHaveLength(1);
      } finally {
        await endChild(harness);
        for (const each of [pgCtl, postmaster]) {
          if (each !== undefined && marks.stillRuns(each.pid, each.start)) {
            process.kill(each.pid, "SIGKILL");
          }
        }
        if (link !== "none") rmSync(given, { force: true });
        rmSync(tmp, { recursive: true, force: true });
      }
    },
    // The row waits for a harness and two starts' sweeps, each a `bun` child, and for two events
    // of a pg_ctl between them, each for at most `claimEventMs`; the sweeps end in under a second.
    600_000
  );
}

/** The socket directory that the mark `file` names, as the stack spelt it. */
function socketsOf(file: string): string {
  const sockets = marks.markOf(readFileSync(file, "utf8"))?.sockets;
  if (sockets === undefined) throw new Error(`${file} is no mark that names a socket directory`);
  return sockets;
}

/** When the entries of the directory `path` last changed: a rename of one of them, even one that
 *  is undone, changes it. */
function entriesChangedAt(path: string): bigint {
  return statSync(path, { bigint: true }).mtimeNs;
}

// A process of this user whose working directory is a dead stack's data directory holds it, though
// no argument and no open file of it names that directory: a `postgres` that `pg_ctl` started and
// that has not yet written its postmaster.pid is so. The start reads it before it would rename the
// directory, so the holder never has its directory under another name.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start leaves a dead stack's data directory that a process of this user holds by its working directory alone, and its mark, never renames it, and names it in one line; the next start after that process ended removes both",
  async () => {
    const { tmp, run } = scratch();
    const data = join(run, "worker-0", "pgdata");
    leaveData(data);
    const mark = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
    const sockets = socketsOf(mark);
    const holder = standIn(data);
    try {
      const before = entriesChangedAt(dirname(data));
      const first = await sweep(tmp);

      expect([existsSync(data), existsSync(mark)]).toEqual([true, true]);
      expect(entriesChangedAt(dirname(data))).toBe(before);
      expect(holding(first, "removed")).toEqual([]);
      expect(holding(first, `${mark} (${data}, which `)).toEqual([
        expect.stringContaining(` ${holder.pid} still holds)`),
      ]);

      holder.stop();
      await until(() => !holder.alive(), "the end of the holder", claimEventMs);
      const second = await sweep(tmp);

      expect([existsSync(data), existsSync(mark)]).toEqual([false, false]);
      expect(holding(second, `removed ${data} and ${sockets}`)).toHaveLength(1);
    } finally {
      holder.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for two starts' sweeps, each a `bun` child, and for the holder's end between
  // them, for at most `claimEventMs`.
  600_000
);

// A process of this user that holds a file of a dead stack's data directory open holds that
// directory, though its working directory and its arguments are elsewhere.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start leaves a dead stack's data directory in which a process of this user holds a file open, and its mark, and names it in one line; the next start after that process ended removes both",
  async () => {
    const { tmp, run } = scratch();
    const data = join(run, "worker-0", "pgdata");
    leaveData(data);
    const mark = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
    const sockets = socketsOf(mark);
    const opened = openSync(join(data, "PG_VERSION"), "r");
    let holder: StandIn | undefined;
    try {
      holder = standIn(tmp, ["sleep", "600"], opened);
      closeSync(opened);

      const first = await sweep(tmp);

      expect([existsSync(data), existsSync(mark)]).toEqual([true, true]);
      expect(holding(first, "removed")).toEqual([]);
      expect(holding(first, `${mark} (${data}, which `)).toEqual([
        expect.stringContaining(` ${holder.pid} still holds)`),
      ]);

      const ended = holder;
      ended.stop();
      await until(() => !ended.alive(), "the end of the holder", claimEventMs);
      const second = await sweep(tmp);

      expect([existsSync(data), existsSync(mark)]).toEqual([false, false]);
      expect(holding(second, `removed ${data} and ${sockets}`)).toHaveLength(1);
    } finally {
      holder?.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for two starts' sweeps, each a `bun` child, and for the holder's end between
  // them, for at most `claimEventMs`.
  600_000
);

// The check before the removal of each socket directory, which the container sweep's removal
// shares: a process whose argument mounts the socket directory, as `docker create` writes it,
// holds it.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start leaves a dead stack's socket directory that an argument of a process of this user mounts, as docker create writes it, and its mark, never renames it, and names it in one line; the next start after that process ended removes both",
  async () => {
    const { tmp, run } = scratch();
    const data = join(run, "worker-0", "pgdata");
    leaveData(data);
    const mark = writeMark(run, owners().gone, data, mkdtempSync(join(tmp, "no-bin-")));
    const sockets = socketsOf(mark);
    const mount = `type=bind,source=${sockets},target=/var/run/postgresql`;
    const holder = standIn(tmp, [
      process.execPath,
      "--eval",
      "setTimeout(() => {}, 600_000)",
      "--mount",
      mount,
    ]);
    try {
      await until(
        () => readFileSync(`/proc/${holder.pid}/cmdline`, "utf8").includes(mount),
        "the holder's arguments",
        claimEventMs
      );

      const before = entriesChangedAt(run);
      const first = await sweep(tmp);

      expect([existsSync(sockets), existsSync(mark)]).toEqual([true, true]);
      expect(entriesChangedAt(run)).toBe(before);
      expect(holding(first, `${mark} (${sockets}, which `)).toEqual([
        expect.stringContaining(` ${holder.pid} still holds)`),
      ]);

      holder.stop();
      await until(() => !holder.alive(), "the end of the holder", claimEventMs);
      const second = await sweep(tmp);

      expect([existsSync(sockets), existsSync(mark)]).toEqual([false, false]);
      expect(holding(second, `removed ${sockets}`)).toHaveLength(1);
    } finally {
      holder.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for two starts' sweeps and a holder, each a `bun` child, and for the holder's
  // arguments and its end between them, each for at most `claimEventMs`.
  600_000
);

// The wait after a stop: a postmaster's processes outlive it a moment, and the directory stays
// until the last of them has ended.
test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start that stops a dead stack's postmaster removes its data directory only once the postmaster's last process, which outlives it a moment, has ended",
  async () => {
    const { tmp, run } = scratch();
    const bin = join(tmp, "bin");
    const saw = join(tmp, "saw");
    const data = join(run, "worker-0", "pgdata");
    const postmaster = slowReleasingPostmaster(bin, data, saw);
    const mark = writeMark(run, owners().gone, data, bin);
    const sockets = socketsOf(mark);
    try {
      const lines = await sweep(tmp);
      await until(
        () => existsSync(saw) && readFileSync(saw, "utf8") !== "",
        "the last process's look at the data directory",
        claimEventMs
      );

      expect(readFileSync(saw, "utf8").split("\n").filter(Boolean)).toEqual(["present"]);
      expect(postmaster.alive()).toBe(false);
      expect([existsSync(data), existsSync(sockets), existsSync(mark)]).toEqual([
        false,
        false,
        false,
      ]);
      expect(
        holding(lines, `removed the postmaster of ${data}, that directory and ${sockets}`)
      ).toHaveLength(1);
    } finally {
      postmaster.stop();
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for one start's sweep, a `bun` child, and for the last process's look, for at
  // most `claimEventMs`.
  600_000
);
