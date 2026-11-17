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
 *  of another run on the machine. They need Linux (`/proc`). A mark another user wrote needs a
 *  second user, which a test run has none of, so the rows of `whyUntrusted` read owners from a
 *  stubbed `lstat`;
 *  the rows with real files plant what one user can: a link, a program, a directory others may
 *  write. A stand-in process is one this file starts and records by its pid and start, and stops
 *  only while `/proc` still names it; a stand-in postmaster is a copy of `sleep` named `postgres`. */

import { expect, test } from "bun:test";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  realpathSync,
  rmSync,
  type Stats,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import * as marks from "../harness-postgres-marks";
import {
  hostileMarkKept,
  plantedBin,
  plantHostileMark,
  type StandIn,
  standIn,
} from "./planted-marks";

const { marksDirectory, runDirectoryPrefix, stackOwner } = marks;

const onLinux = process.platform === "linux";

/** The owners a mark of a row carries, each built from this process: this process, which runs;
 *  its pid with another start, which is a gone stack; another boot; another PID namespace; and
 *  none, which is a stack's where there is no /proc. */
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

/** A row's scratch: its own `TMPDIR`, in `parent`, and a run directory in it named as the pool
 *  names one. */
function scratch(parent = tmpdir()) {
  const tmp = mkdtempSync(join(parent, "marks-trust-"));
  return { tmp, run: mkdtempSync(join(tmp, runDirectoryPrefix)) };
}

/** Writes the mark of stack `index` in `run`, whose owner is `owner`, naming `data` and `bin`. */
function writeMark(run: string, owner: unknown, data: string, bin: string, index = 0): string {
  const sockets = mkdtempSync(join(run, `pg-${index}-`));
  mkdirSync(join(run, marksDirectory), { recursive: true });
  const file = join(run, marksDirectory, `worker-${index}.json`);
  writeFileSync(file, JSON.stringify({ owner, run, sockets, postmaster: { bin, data } }));
  return file;
}

/** A data directory at `path` holding a file, and a `postmaster.pid` naming `pid` when given. */
function leaveData(path: string, pid?: number): void {
  mkdirSync(path, { recursive: true });
  writeFileSync(join(path, "PG_VERSION"), "18\n");
  if (pid !== undefined) writeFileSync(join(path, "postmaster.pid"), `${pid}\n${path}\n`);
}

/** A stand-in postmaster of the cluster in `data`: a copy of `sleep` named `postgres` in `bin`,
 *  run with `data` for its working directory, which `data`'s `postmaster.pid` names. */
function standInPostmaster(bin: string, data: string): StandIn {
  const sleep = Bun.which("sleep");
  if (sleep === null) throw new Error("no sleep on PATH to stand in for a postmaster");
  mkdirSync(bin, { recursive: true });
  copyFileSync(sleep, join(bin, "postgres"));
  chmodSync(join(bin, "postgres"), 0o755);
  mkdirSync(data, { recursive: true });
  const postmaster = standIn(data, [join(bin, "postgres"), "600"]);
  leaveData(data, postmaster.pid);
  return postmaster;
}

/** The start's sweep, as a child of this file runs it. */
const sweepScript = `
import { removeLeftoverPostgres } from ${JSON.stringify(fileURLToPath(new URL("../harness-postgres-marks.ts", import.meta.url)))};
await removeLeftoverPostgres();
`;

/** Runs the start's sweep in a child with `tmp` for its `TMPDIR` and no program on its `PATH`, so
 *  no row changes the environment of this process, and answers the lines it printed. */
async function sweep(tmp: string): Promise<string[]> {
  const empty = mkdtempSync(join(tmp, "no-programs-"));
  const child = Bun.spawn([process.execPath, "--eval", sweepScript], {
    env: { TMPDIR: tmp, PATH: empty },
    stdout: "ignore",
    stderr: "pipe",
  });
  const [stderr, code] = await Promise.all([new Response(child.stderr).text(), child.exited]);
  if (code !== 0) throw new Error(`the sweep exited ${code}: ${stderr}`);
  return stderr.split("\n");
}

/** The lines of `lines` that hold `text`. */
function holding(lines: readonly string[], text: string): string[] {
  return lines.filter((line) => line.includes(text));
}

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
  "v-no-leftover-postgres: a mark of another boot, of another PID namespace and of a stack where there is no /proc is each left with its directories, and each is named with its case on the one line of what the start cannot judge",
  async () => {
    const who = owners();
    const { tmp, run } = scratch();
    try {
      const bin = mkdtempSync(join(tmp, "no-bin-"));
      const cases = [
        { owner: who.otherBoot, why: `boot ${who.otherBoot.boot}` },
        { owner: who.otherNamespace, why: "PID namespace pid:[1]" },
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

/** A run directory of another `TMPDIR` than the sweep's, with what a stack left there: a stand-in
 *  postmaster of stack 0 serving from its data directory, and the mark of `owner` beside it, or no
 *  mark where `owner` is undefined. */
function leaveElsewhere(owner?: unknown) {
  const other = mkdtempSync(join(tmpdir(), "marks-elsewhere-"));
  const run = mkdtempSync(join(other, runDirectoryPrefix));
  const bin = join(other, "bin");
  const data = join(realpathSync(run), "worker-0", "pgdata");
  const postmaster = standInPostmaster(bin, data);
  const file = owner === undefined ? undefined : writeMark(run, owner, data, bin);
  return { other, run, data, postmaster, file };
}

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a stack under another TMPDIR that the kernel shows serving and whose stack is gone is removed by the same steps: its postmaster ends, its data and socket directories and its mark go",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().gone);
    try {
      const lines = await sweep(tmp);

      expect(left.postmaster.alive()).toBe(false);
      expect(existsSync(left.data)).toBe(false);
      expect(left.file !== undefined && existsSync(left.file)).toBe(false);
      expect(holding(lines, `removed the postmaster of ${left.data}`)).toHaveLength(1);
    } finally {
      left.postmaster.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a stack under another TMPDIR whose stack runs is not touched, and the start prints nothing of it",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().runs);
    try {
      const lines = await sweep(tmp);

      expect(left.postmaster.alive()).toBe(true);
      expect([
        existsSync(join(left.data, "PG_VERSION")),
        left.file !== undefined && existsSync(left.file),
      ]).toEqual([true, true]);
      expect(holding(lines, left.run)).toEqual([]);
    } finally {
      left.postmaster.stop();
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

      expect(left.postmaster.alive()).toBe(true);
      expect(existsSync(join(left.data, "PG_VERSION"))).toBe(true);
      expect(
        holding(
          lines,
          `${left.data} (postmaster ${left.postmaster.pid} serves it from a run directory of another TMPDIR, and no mark there names it)`
        )
      ).toHaveLength(1);
    } finally {
      left.postmaster.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

/** A stubbed `lstat` over `entries`, path to what it says; any other path is gone. */
function stubbedLstat(
  entries: Record<string, { kind: "directory" | "file" | "link"; uid: number; mode: number }>
) {
  return (path: string): Stats => {
    const entry = entries[path];
    if (entry === undefined) {
      throw Object.assign(new Error(`ENOENT: ${path}`), { code: "ENOENT" });
    }
    // The fields `whyUntrusted` reads, which is all a stub of the kernel's answer needs to hold.
    const stat = {
      uid: entry.uid,
      mode: entry.mode,
      isDirectory: () => entry.kind === "directory",
      isFile: () => entry.kind === "file",
      isSymbolicLink: () => entry.kind === "link",
    } as Stats;
    return stat;
  };
}

const me = 1000;
const other = 4242;
const root = { kind: "directory", uid: 0, mode: 0o755 } as const;
const sharedTmp = { kind: "directory", uid: 0, mode: 0o1777 } as const;

test("v-no-leftover-postgres: whyUntrusted: in /tmp, whose sticky bit lets another user make a directory of a guessed name first, a run directory of another user is untrusted, and one of this user's is trusted", () => {
  const theirs = stubbedLstat({
    "/": root,
    "/tmp": sharedTmp,
    "/tmp/dispatch-e2e-run-abcdef": { kind: "directory", uid: other, mode: 0o700 },
  });
  expect(marks.whyUntrusted("/tmp/dispatch-e2e-run-abcdef", me, "directory", theirs)).toBe(
    `/tmp/dispatch-e2e-run-abcdef belongs to user ${other}`
  );
  const mine = stubbedLstat({
    "/": root,
    "/tmp": sharedTmp,
    "/tmp/dispatch-e2e-run-abcdef": { kind: "directory", uid: me, mode: 0o700 },
  });
  expect(marks.whyUntrusted("/tmp/dispatch-e2e-run-abcdef", me, "directory", mine)).toBeUndefined();
});

test("v-no-leftover-postgres: whyUntrusted: a mark file of another user is untrusted, even in a run directory of this user's", () => {
  const file = "/tmp/dispatch-e2e-run-abcdef/postgres-marks/worker-0.json";
  const lstat = stubbedLstat({
    "/": root,
    "/tmp": sharedTmp,
    "/tmp/dispatch-e2e-run-abcdef": { kind: "directory", uid: me, mode: 0o700 },
    "/tmp/dispatch-e2e-run-abcdef/postgres-marks": { kind: "directory", uid: me, mode: 0o700 },
    [file]: { kind: "file", uid: other, mode: 0o600 },
  });
  expect(marks.whyUntrusted(file, me, "file", lstat)).toBe(`${file} belongs to user ${other}`);
});

test("v-no-leftover-postgres: whyUntrusted: a directory above the path that another user owns, or that others may write without a sticky bit, or that is a link, makes the path untrusted", () => {
  const leaf = { kind: "directory", uid: me, mode: 0o700 } as const;
  const above = (entry: { kind: "directory" | "file" | "link"; uid: number; mode: number }) =>
    marks.whyUntrusted(
      "/srv/run",
      me,
      "directory",
      stubbedLstat({ "/": root, "/srv": entry, "/srv/run": leaf })
    );
  expect(above({ kind: "directory", uid: other, mode: 0o755 })).toBe(
    `/srv belongs to user ${other}`
  );
  expect(above({ kind: "directory", uid: 0, mode: 0o777 })).toBe("/srv is writable by other users");
  expect(above({ kind: "link", uid: me, mode: 0o777 })).toBe("/srv is a symbolic link");
  expect(above({ kind: "directory", uid: me, mode: 0o755 })).toBeUndefined();
});
