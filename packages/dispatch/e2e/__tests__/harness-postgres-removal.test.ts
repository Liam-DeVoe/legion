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
 *  second user, which a test run has none of, so the rows of `whyUntrusted`
 *  (`harness-postgres-marks.test.ts`) read owners from a stubbed `lstat`; these rows plant what
 *  one user can: a link, a program, a directory others may write. A stand-in process is one this file starts and records by its pid
 *  and start, and stops only while `/proc` still names it; a stand-in postmaster is a copy of
 *  `sleep` named `postgres`. */

import { expect, test } from "bun:test";
import {
  chmodSync,
  copyFileSync,
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

import * as marks from "../harness-postgres-marks";
import { servingPostmasters, stopPostmaster } from "../harness-postgres-removal";
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
 *  its pid with another start, which is a gone stack; another boot; another PID namespace;
 *  another time namespace; and none, which is a stack's where there is no /proc. */
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
  writeFileSync(
    file,
    JSON.stringify({
      owner,
      run,
      identity: marks.identityOf(run),
      sockets,
      postmaster: { bin, data },
    })
  );
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

/** A stand-in postmaster of the cluster in `data` that writes one line to `signals` for each
 *  SIGQUIT it gets, and ends once it got one and `go` exists, or after 600 s: a copy of `bash`
 *  named `postgres` in `bin`, run with `data` for its working directory, which `data`'s
 *  `postmaster.pid` names. */
function signalCountingPostmaster(bin: string, data: string, signals: string, go: string): StandIn {
  const bash = Bun.which("bash");
  if (bash === null) throw new Error("no bash on PATH to stand in for a postmaster");
  mkdirSync(bin, { recursive: true });
  copyFileSync(bash, join(bin, "postgres"));
  chmodSync(join(bin, "postgres"), 0o755);
  mkdirSync(data, { recursive: true });
  const script = `quit=; trap 'echo QUIT >>"$1"; quit=1' QUIT
end=$((SECONDS + 600))
while [ "$SECONDS" -lt "$end" ]; do
  if [ -n "$quit" ] && [ -e "$2" ]; then exit 0; fi
  sleep 0.05 & wait $!
done`;
  const postmaster = standIn(data, [join(bin, "postgres"), "-c", script, "postgres", signals, go]);
  leaveData(data, postmaster.pid);
  return postmaster;
}

const removalModule = fileURLToPath(new URL("../harness-postgres-removal.ts", import.meta.url));

/** The start's sweep, as a child of this file runs it. */
const sweepScript = `
import { removeLeftoverPostgres } from ${JSON.stringify(removalModule)};
await removeLeftoverPostgres();
`;

/** A start's sweep running in a child with `tmp` for its `TMPDIR` and `empty`, a directory with no
 *  program in it, for its `PATH`, so no row changes the environment of this process: what it
 *  printed so far, and the lines it printed once it ended. */
function startSweep(
  tmp: string,
  empty = mkdtempSync(join(tmp, "no-programs-"))
): { stderr(): string; readonly lines: Promise<string[]> } {
  const child = Bun.spawn([process.execPath, "--eval", sweepScript], {
    env: { TMPDIR: tmp, PATH: empty },
    stdout: "ignore",
    stderr: "pipe",
  });
  let stderr = "";
  const read = (async () => {
    const reader = child.stderr.getReader();
    const decoder = new TextDecoder();
    for (let chunk = await reader.read(); !chunk.done; chunk = await reader.read()) {
      stderr += decoder.decode(chunk.value, { stream: true });
    }
  })();
  const lines = (async () => {
    const [code] = await Promise.all([child.exited, read]);
    if (code !== 0) throw new Error(`the sweep exited ${code}: ${stderr}`);
    return stderr.split("\n");
  })();
  return { stderr: () => stderr, lines };
}

/** Runs the start's sweep in a child (`startSweep`), and answers the lines it printed. */
function sweep(tmp: string, empty?: string): Promise<string[]> {
  return startSweep(tmp, empty).lines;
}

/** Waits until `met` holds, and throws naming `what` where it has not after `ms`. What it waits for
 *  is a write of another process (a stand-in's signal line, a child's line), which no event of
 *  this process reports, so it reads again every 50 ms. */
async function until(met: () => boolean, what: string, ms: number): Promise<void> {
  const deadline = Date.now() + ms;
  while (!met()) {
    if (Date.now() >= deadline) throw new Error(`${what} did not come within ${ms / 1000} s`);
    await Bun.sleep(50);
  }
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
  "v-no-leftover-postgres: a start whose TMPDIR no longer exists still removes a stack under another TMPDIR that the kernel shows serving and whose stack is gone, and names the TMPDIR it cannot read on one line",
  async () => {
    const { tmp } = scratch();
    const programs = mkdtempSync(join(tmp, "no-programs-"));
    const gone = join(tmp, "tmpdir-gone");
    const left = leaveElsewhere(owners().gone);
    try {
      const lines = await sweep(gone, programs);

      expect(left.postmaster.alive()).toBe(false);
      expect(existsSync(left.data)).toBe(false);
      expect(holding(lines, `removed the postmaster of ${left.data}`)).toHaveLength(1);
      expect(holding(lines, `left what it cannot read: ${gone} (ENOENT`)).toHaveLength(1);
    } finally {
      left.postmaster.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  }
);

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: a start with no docker on its PATH leaves the mark of a dead stack's container, with its socket directory, and names it: it cannot tell whether that container still runs",
  async () => {
    const { tmp, run } = scratch();
    try {
      const sockets = mkdtempSync(join(run, "pg-0-"));
      const container = "dispatch-e2e-pg-no-docker-0";
      mkdirSync(join(run, marksDirectory), { recursive: true });
      const file = join(run, marksDirectory, "worker-0.json");
      writeFileSync(
        file,
        JSON.stringify({
          owner: owners().gone,
          run,
          identity: marks.identityOf(run),
          sockets,
          container,
        })
      );

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
          `${left.data} (postmaster ${left.postmaster.pid} serves it from a run directory this start does not list, and no mark there names it)`
        )
      ).toHaveLength(1);
    } finally {
      left.postmaster.stop();
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
      postmaster = standInPostmaster(join(tmp, "bin"), served);
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

/** How long a row of the claim waits for one event of another process: a `bun` child's start of
 *  its sweep under load, which took more than 180 s once on a shared Linux machine under a load of
 *  1,217, on 2026-11-17, is the longest of them. */
const claimEventMs = 240_000;

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

/** A start's claim of `CLAIM_PATH` (`claimForRemoval`) in a child of this file: it prints the
 *  claim, and then waits to be killed, for at most 600 s. */
const claimScript = `
import { claimForRemoval } from ${JSON.stringify(removalModule)};
setTimeout(() => process.exit(1), 600_000);
console.log(JSON.stringify(await claimForRemoval(process.env.CLAIM_PATH, process.getuid())));
`;

/** Claims `path` in a child with the real `claimForRemoval`, and SIGKILLs the child once it printed
 *  its claim: what a start killed between its claim and its removal leaves. Answers the claimed
 *  name, which holds its claimer's pid and start, as the judge reads an owner. */
async function claimAndDie(path: string): Promise<string> {
  const claimer = Bun.spawn([process.execPath, "--eval", claimScript], {
    env: { ...process.env, CLAIM_PATH: path },
    stdout: "pipe",
    stderr: "ignore",
  });
  try {
    const claimed = `${path}.removing-${claimer.pid}-${marks.startOf(readFileSync(`/proc/${claimer.pid}/stat`, "utf8"))}`;
    let printed = "";
    const reader = claimer.stdout.getReader();
    const decoder = new TextDecoder();
    while (!printed.includes("\n")) {
      const chunk = await reader.read();
      if (chunk.done) break;
      printed += decoder.decode(chunk.value, { stream: true });
    }
    expect(JSON.parse(printed)).toEqual({ kind: "claimed", path: claimed });
    return claimed;
  } finally {
    if (claimer.exitCode === null && claimer.signalCode === null) claimer.kill("SIGKILL");
    await claimer.exited;
  }
}

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: what a start killed between its claim and its removal left, the next start claims again: the postmaster that serves from the claimed directory ends, and the directory goes",
  async () => {
    const { tmp, run } = scratch();
    const bin = join(tmp, "bin");
    const data = join(run, "worker-0", "pgdata");
    const postmaster = standInPostmaster(bin, data);
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

test.skipIf(!onLinux)(
  "v-no-leftover-postgres: what a start killed between its claim and its removal left in a run directory of another TMPDIR, a start of this TMPDIR finds by the claimed name the kernel shows: the postmaster ends, the directory and the mark go, and one line says so",
  async () => {
    const { tmp } = scratch();
    const left = leaveElsewhere(owners().gone);
    try {
      const claimed = await claimAndDie(left.data);
      // The kernel names the postmaster's working directory by the claim's name now.
      expect([existsSync(left.data), existsSync(claimed), left.postmaster.alive()]).toEqual([
        false,
        true,
        true,
      ]);

      const lines = await sweep(tmp);

      expect(left.postmaster.alive()).toBe(false);
      expect([existsSync(claimed), left.file !== undefined && existsSync(left.file)]).toEqual([
        false,
        false,
      ]);
      // One line names that stack, and it says the start removed it: none says it was left. The
      // start prints the run directory's one spelling, the one its data directory is written in.
      expect(holding(lines, dirname(dirname(left.data)))).toEqual([
        expect.stringContaining(`removed the postmaster of ${left.data}`),
      ]);
    } finally {
      left.postmaster.stop();
      rmSync(left.other, { recursive: true, force: true });
      rmSync(tmp, { recursive: true, force: true });
    }
  },
  // The row waits for a claimer, a `bun` child, to print its claim, and for one start's sweep,
  // another `bun` child; a stuck child waits out its own 600 s at most.
  600_000
);
