/** e2e/tools/unit-repeat.ts on the junit files real Bun writes (INFRA-1511).
 *
 *  Each row writes tiny test files into a directory of its own and runs Bun over them there,
 *  with `process.execPath`, an explicit `--seed`, `--rerun-each` and Bun's junit reporter. Then
 *  it hands what Bun left behind to the tool's pure functions. The directory is outside
 *  packages/dispatch, so the package's bunfig.toml does not reach that run. HOME is the same
 *  directory, so no global bunfig of the host reaches it either. Two rows run the tool itself
 *  there instead: on an `--out` the tool must refuse before it starts Bun, and on a copy of the
 *  package whose one row never ends, which the tool is stopped on. One row reads no Bun at all:
 *  it holds the budgets of .github/workflows/dispatch-unit-repeat.yaml to the measurements beside
 *  them.
 *
 *  On each rerun Bun 1.4.4 runs the test file's own top-level code again, so a counter declared
 *  there starts again at 0. A module the file imports keeps its state between reruns (measured
 *  for INFRA-1511's plan), and so does `globalThis`. Each scratch file here is a single file with
 *  no module of its own to import, so a row that must fail on a later run counts its runs on
 *  `globalThis`.
 *
 *  Each row says which lie of a weekly result it catches.
 */

import { expect, test } from "bun:test";
import { copyFileSync, mkdirSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

import {
  annotations,
  junitAt,
  type LaneRun,
  markdown,
  parseArguments,
  type Report,
  type RowlessKind,
  runningFiles,
  type Stop,
  summarise,
} from "../tools/unit-repeat";
import { rowLimit, runBounded } from "./bounded-subprocess";

const scratch = temporaryDirectories("unit-repeat");

const tool = fileURLToPath(new URL("../tools/unit-repeat.ts", import.meta.url));

const SEED = 4242;

/** Passes its first run and fails every later one. */
const failsAfterFirstRun = `import { describe, expect, test } from "bun:test";

const counter = globalThis as { flakyRuns?: number };

describe("outer", () => {
  test("fails after its first run", () => {
    counter.flakyRuns = (counter.flakyRuns ?? 0) + 1;
    expect(counter.flakyRuns).toBe(1);
  });
  test("passes", () => {});
});
`;

const passes = `import { test } from "bun:test";

test("passes one", () => {});
test("passes two", () => {});
test.skip("is skipped", () => {});
`;

interface Spawned {
  readonly code: number;
  readonly output: string;
}

/** Runs Bun in `directory` with `args`, a bounded child under the short bound, the bound of one
 *  command that starts nothing: a `bun test` over a few tiny files, with no `--parallel`, runs in
 *  one process and takes a fraction of a second. */
async function spawnBun(
  directory: string,
  args: readonly string[],
  environment: Readonly<Record<string, string>> = {}
): Promise<Spawned> {
  const { code, stderr, stdout } = await runBounded({
    argv: [process.execPath, ...args],
    bound: "short",
    cwd: directory,
    env: { HOME: directory, PATH: process.env.PATH ?? "", TMPDIR: tmpdir(), ...environment },
    from: import.meta.url,
  });
  return { code, output: `${stdout}${stderr}` };
}

/** Writes `files` into a fresh directory and runs `bun test` there as a lane run would. */
async function laneRun(
  files: Readonly<Record<string, string>>,
  reruns = 3,
  environment: Readonly<Record<string, string>> = {}
): Promise<LaneRun> {
  const directory = scratch();
  for (const [name, text] of Object.entries(files)) await Bun.write(join(directory, name), text);
  const junit = join(directory, "junit.xml");
  const started = performance.now();
  const { code, output } = await spawnBun(
    directory,
    [
      "test",
      `--seed=${SEED}`,
      `--rerun-each=${reruns}`,
      "--reporter=junit",
      `--reporter-outfile=${junit}`,
    ],
    environment
  );
  return {
    lane: "harness",
    target: "e2e/__tests__",
    seed: SEED,
    reruns,
    wallMs: performance.now() - started,
    exitCode: code,
    signal: null,
    junit: junitAt(junit),
    log: output,
    environment,
    // Bun runs here without runLane, which is what reads /proc/meminfo, so there is no reading.
    memory: [],
    stop: null,
  };
}

function kinds(report: Report): RowlessKind[] {
  return report.rowlessFailures.map((failure) => failure.kind);
}

test(
  "a row that fails only on its reruns is named with its count, the seed and its command",
  async () => {
    // The lie: a row that fails 2 of 3 runs goes unnamed, or is named without the seed and the
    // rerun that bring it back, so no one can reproduce it. The run carries GitHub's variables,
    // so Bun writes the `<properties>` it writes in the weekly job.
    const github = {
      GITHUB_ACTIONS: "true",
      GITHUB_EVENT_NAME: "schedule",
      GITHUB_REPOSITORY: "cybermonklabs/legion",
      GITHUB_RUN_ATTEMPT: "1",
      GITHUB_RUN_ID: "77",
      GITHUB_SERVER_URL: "https://github.com",
      GITHUB_SHA: "0123abcd",
    };
    const run = await laneRun(
      { "flaky.test.ts": failsAfterFirstRun, "steady.test.ts": passes },
      3,
      github
    );
    expect("text" in run.junit && run.junit.text.includes("<properties>")).toBe(true);
    const report = summarise(run);
    const reproduce = `bun run test:harness flaky.test.ts --seed=${SEED} --rerun-each=2`;
    expect(report.failingRows).toEqual([
      {
        file: "flaky.test.ts",
        name: "outer > fails after its first run",
        fails: 2,
        runs: 3,
        firstFailedRun: 2,
        message: expect.stringContaining("Received: 2"),
        reproduce,
      },
    ]);
    // Two files; two rows and three rows, one of them skipped. Each ran three times.
    expect(report.oneRun).toEqual({ files: 2, rows: 5, skippedRows: 1 });
    expect(report.testcases).toEqual({ passed: 10, failed: 2, skipped: 3 });
    expect(report.rowlessFailures).toEqual([]);
    expect(report.exitCode).toBe(1);
    expect(report.github.url).toBe("https://github.com/cybermonklabs/legion/actions/runs/77");

    const text = markdown(report);
    expect(text).toContain(`- Seed: \`${SEED}\``);
    expect(text).toContain(reproduce);
    expect(text).toContain(
      "[run 77, attempt 1](https://github.com/cybermonklabs/legion/actions/runs/77)"
    );
    expect(text).not.toContain("steady.test.ts");
    const errors = annotations(report).filter((line) => line.startsWith("::error "));
    expect(errors).toHaveLength(1);
    expect(errors[0]).toStartWith(
      "::error file=packages/dispatch/flaky.test.ts,title=Flaky unit test::" +
        `outer > fails after its first run failed 2/3 runs (seed ${SEED}): `
    );
  },
  rowLimit("short")
);

test(
  "a row that fails on every run is named 3 of 3, with a command that runs the file once",
  async () => {
    // The lie: a row that fails every run is called flaky, and its command asks for reruns it
    // does not need.
    const report = summarise(
      await laneRun({
        "broken.test.ts": `import { test } from "bun:test";\n\ntest("always fails", () => {\n  throw new Error("always");\n});\n`,
      })
    );
    expect(report.failingRows).toEqual([
      {
        file: "broken.test.ts",
        name: "always fails",
        fails: 3,
        runs: 3,
        firstFailedRun: 1,
        message: "always",
        reproduce: `bun run test:harness broken.test.ts --seed=${SEED}`,
      },
    ]);
    expect(report.exitCode).toBe(1);
    expect(annotations(report)[0]).toStartWith(
      "::error file=packages/dispatch/broken.test.ts,title=Failing unit test::" +
        `always fails failed 3/3 runs (seed ${SEED}): always`
    );
  },
  rowLimit("short")
);

test(
  "a junit file with no testcase is refused as a failure that belongs to no row",
  async () => {
    // The lie: a run that tested nothing reads as a clean week. Bun 1.4.4 writes no junit file
    // when no testcase ran, so this one is a real file with its suites taken out.
    const run = await laneRun({ "steady.test.ts": passes });
    if (!("text" in run.junit)) throw new Error(`Bun wrote no junit file:\n${run.log}`);
    const [declaration, root] = run.junit.text.split("\n");
    expect(root).toStartWith("<testsuites ");
    const empty = { ...run, junit: { text: `${declaration}\n${root}\n</testsuites>\n` } };
    expect(run.exitCode).toBe(0);
    const report = summarise(empty);
    expect(kinds(report)).toEqual(["no-testcase"]);
    expect(report.failingRows).toEqual([]);
    expect(report.exitCode).toBe(1);
  },
  rowLimit("short")
);

test(
  "a missing junit file is refused as a failure that belongs to no row",
  async () => {
    // The lie: Bun runs no test, exits 0 and writes no junit file, and the report reads that as
    // no failure.
    const run = await laneRun({
      "empty.test.ts": `import { describe } from "bun:test";\n\ndescribe("holds no test", () => {});\n`,
    });
    expect(run.exitCode).toBe(0);
    expect("unreadable" in run.junit).toBe(true);
    const report = summarise(run);
    expect(kinds(report)).toEqual(["junit-unreadable"]);
    expect(report.exitCode).toBe(1);
  },
  rowLimit("short")
);

test(
  "a non-zero exit of Bun with no failing testcase is a failure that belongs to no row",
  async () => {
    // The lie: a file that fails to load leaves no testcase in the junit file. Every testcase
    // there passed, so the report would read as clean although Bun failed.
    const run = await laneRun({
      "steady.test.ts": passes,
      "throws-on-load.test.ts": `import { test } from "bun:test";\n\ntest("never runs", () => {});\nthrow new Error("the file fails to load");\n`,
    });
    expect(run.exitCode).not.toBe(0);
    const report = summarise(run);
    expect(report.failingRows).toEqual([]);
    expect(kinds(report)).toEqual(["bun-exit", "bun-errors"]);
    expect(report.bunLogTail.join("\n")).toContain("the file fails to load");
    expect(report.exitCode).toBe(1);
    const errors = annotations(report).filter((line) => line.startsWith("::error "));
    expect(errors).toHaveLength(2);
    expect(errors[0]).toStartWith(
      "::error title=Failure outside any test::Bun exited with status 1"
    );
  },
  rowLimit("short")
);

test(
  "a file whose rows did not all run every time is a failure that belongs to no row",
  async () => {
    // The lie: a row that runs on fewer reruns than asked reads as passing all of them.
    const run = await laneRun({
      "first-load-only.test.ts": `import { test } from "bun:test";\n\nconst counter = globalThis as { loads?: number };\ncounter.loads = (counter.loads ?? 0) + 1;\ntest("always here", () => {});\nif (counter.loads === 1) test("only on the first run", () => {});\n`,
    });
    expect(run.exitCode).toBe(0);
    const report = summarise(run);
    expect(kinds(report)).toEqual(["uneven-file"]);
    expect(report.exitCode).toBe(1);
  },
  rowLimit("short")
);

test(
  "a row whose name holds &, < and a double quote is reported by its real name",
  async () => {
    // The lie: the report names a row as the junit file escapes it, which no one can search for.
    const report = summarise(
      await laneRun(
        {
          "names.test.ts": `import { describe, test } from "bun:test";\n\ndescribe("a <describe>", () => {\n  test('keeps & < and " in its name', () => {\n    throw new Error('bad & <x> "y"');\n  });\n});\n`,
        },
        1
      )
    );
    expect(report.failingRows.map(({ name, message }) => ({ name, message }))).toEqual([
      { name: 'a <describe> > keeps & < and " in its name', message: 'bad & <x> "y"' },
    ]);
    expect(markdown(report)).toContain('`a <describe> > keeps & < and " in its name`');
  },
  rowLimit("short")
);

test(
  "a row that throws an error with no message is reported with the error and the line that threw it",
  async () => {
    // The lie: Bun writes no message for an error that has none, and writes its text and stack
    // only as the failure's body. A report that reads only the message names the row and says
    // nothing of what failed or where.
    const report = summarise(
      await laneRun(
        {
          "no-message.test.ts": `import { test } from "bun:test";\n\ntest("throws an error with no message", () => {\n  throw new Error("");\n});\n`,
        },
        1
      )
    );
    expect(report.failingRows).toEqual([
      {
        file: "no-message.test.ts",
        name: "throws an error with no message",
        fails: 1,
        runs: 1,
        firstFailedRun: 1,
        message: expect.stringMatching(/^Error\n\s+at no-message\.test\.ts:4:\d+$/),
        reproduce: `bun run test:harness no-message.test.ts --seed=${SEED}`,
      },
    ]);
    expect(report.rowlessFailures).toEqual([]);
  },
  rowLimit("short")
);

test(
  "the annotation of a row escapes the newline and the percent sign of its message",
  async () => {
    // The lie: GitHub cuts an annotation at its first raw newline, and reads a raw % as the start
    // of an escape, so the message it shows is not the failure's.
    const report = summarise(
      await laneRun(
        {
          "lines.test.ts": `import { test } from "bun:test";\n\ntest("fails on two lines", () => {\n  throw new Error("first line\\nsecond line, 100% sure");\n});\n`,
        },
        1
      )
    );
    const [error] = annotations(report);
    expect(error).toBe(
      "::error file=packages/dispatch/lines.test.ts,title=Failing unit test::" +
        `fails on two lines failed 1/1 runs (seed ${SEED}): first line%0Asecond line, 100%25 sure`
    );
    expect(annotations(report).every((line) => !line.includes("\n"))).toBe(true);
  },
  rowLimit("short")
);

test(
  "past GitHub's 10 errors a step, the failure outside any test comes first and the last error counts the rows left out",
  async () => {
    // The lie: GitHub keeps 10 error annotations a step and drops the rest with no word. A
    // failure outside any test printed after the rows is dropped with them, and the rows past the
    // tenth error vanish from the run's page.
    const run = await laneRun({ "steady.test.ts": passes }, 1);
    const report: Report = {
      ...summarise(run),
      failingRows: Array.from({ length: 12 }, (_, index) => ({
        file: "broken.test.ts",
        name: `row ${index + 1}`,
        fails: 1,
        runs: 1,
        firstFailedRun: 1,
        message: "broken",
        reproduce: `bun run test:harness broken.test.ts --seed=${SEED}`,
      })),
      rowlessFailures: [{ kind: "bun-errors", text: "Bun counted 1 error outside any test." }],
    };
    expect(annotations(report)).toEqual([
      `::error title=Failure outside any test::Bun counted 1 error outside any test. (seed ${SEED})`,
      ...[1, 2, 3, 4, 5, 6, 7, 8].map(
        (count) =>
          "::error file=packages/dispatch/broken.test.ts,title=Failing unit test::" +
          `row ${count} failed 1/1 runs (seed ${SEED}): broken`
      ),
      "::error title=More failures in report.md::" +
        `report.md lists 4 more failing rows: GitHub keeps 10 errors a step (seed ${SEED}).`,
      expect.stringMatching(/^::notice /),
    ]);
  },
  rowLimit("short")
);

test(
  "the report gives the machine's memory from its readings, and says when there were none",
  async () => {
    // The lie: a run that took no reading shows 0 MiB, which reads as a measurement. Or the
    // report takes the first or the last reading, not the lowest MemAvailable, and shows less
    // than the most memory in use.
    const run = await laneRun({ "steady.test.ts": passes }, 1);
    const gibibyte = 1024 * 1024; // in KiB, as /proc/meminfo counts
    const measured = summarise({
      ...run,
      memory: [
        { memTotalKiB: 16 * gibibyte, memAvailableKiB: 12 * gibibyte },
        { memTotalKiB: 16 * gibibyte, memAvailableKiB: 6 * gibibyte },
        { memTotalKiB: 16 * gibibyte, memAvailableKiB: 10 * gibibyte },
      ],
    });
    expect(measured.machineMemory).toEqual({
      memTotalMiB: 16384,
      lowestAvailableMiB: 6144,
      mostInUseMiB: 10240,
      samples: 3,
    });
    const reading =
      "MemTotal 16384 MiB, lowest MemAvailable 6144 MiB, so 10240 MiB in use at the most, " +
      "from 3 readings of /proc/meminfo, one at the start and then one every 5 s";
    expect(markdown(measured)).toContain(
      `- Machine memory, which counts every process on the machine and not Bun's alone: ${reading}`
    );
    expect(annotations(measured).at(-1)).toContain(reading);

    const unmeasured = summarise({ ...run, memory: [] });
    expect(unmeasured.machineMemory).toBeNull();
    for (const text of [markdown(unmeasured), annotations(unmeasured).at(-1) ?? ""]) {
      expect(text).toContain("not measured, because /proc/meminfo could not be read");
      expect(text).not.toMatch(/\b0 MiB/);
    }
  },
  rowLimit("short")
);

test(
  "every seed the tool accepts, Bun accepts, and the tool refuses the first one past them",
  async () => {
    // The lie: the tool draws a seed Bun refuses, so the weekly run stops before any test.
    const options = (seed: string) =>
      parseArguments(["--lane", "web", "--out", "out", "--seed", seed], () => 0);
    expect(options("0")).toEqual({ options: { lane: "web", out: "out", reruns: 20, seed: 0 } });
    expect(options("4294967295")).toMatchObject({ options: { seed: 4294967295 } });
    for (const refused of ["4294967296", "-1", "1.5", "0x10", ""]) {
      expect(options(refused)).toHaveProperty("problem");
    }
    expect(parseArguments(["--lane", "web", "--out", "out"], () => 7)).toMatchObject({
      options: { seed: 7 },
    });

    const directory = scratch();
    await Bun.write(join(directory, "steady.test.ts"), passes);
    for (const seed of ["0", "4294967295"]) {
      expect((await spawnBun(directory, ["test", `--seed=${seed}`])).code).toBe(0);
    }
    expect((await spawnBun(directory, ["test", "--seed=4294967296"])).code).not.toBe(0);
  },
  // The three runs of Bun, one after another, each a bounded child under the short bound.
  rowLimit("short", "short", "short")
);

test(
  "an --out that names a file is refused as a bad command line, and the file is left as it was",
  async () => {
    // The lie: the tool throws on an --out that names a file and exits 1, the status of a run
    // whose rows failed, although no test ran. Or it writes over the file it was given.
    const directory = scratch();
    const file = join(directory, "a-file");
    const text = "this file is no directory\n";
    await Bun.write(file, text);
    const { code, output } = await spawnBun(directory, [
      tool,
      "--lane",
      "web",
      "--reruns",
      "1",
      "--seed",
      "1",
      "--out",
      file,
    ]);
    expect(code).toBe(2);
    expect(output).toContain(`unit-repeat: --out ${file} cannot be the output directory: EEXIST`);
    expect(output).toContain("usage: bun run test:repeat --lane <web|web-cold|harness>");
    expect(output).not.toContain("seed=1 lane=web");
    expect(await Bun.file(file).text()).toBe(text);
  },
  rowLimit("short")
);

/** One line of a progress file, as e2e/tools/unit-repeat-progress.ts writes it. */
function progressLine(fields: Readonly<Record<string, string | number>>): string {
  return JSON.stringify(fields);
}

test(
  "a stop names each file a worker was in, with its run and the row it was in, and passes over a finished file",
  () => {
    // The lie: a leg stopped at its deadline reads as one nameless timeout, or names a file every
    // run of which had ended, or names the row in hand by the wrong place in the seed's order.
    const lines = [
      // Worker 1 ran a.test.ts's first run to its end, then began the second run's rows: row 2 of
      // run 2 is in hand.
      progressLine({ event: "load", load: "a", at: 1_000, file: "a.test.ts", pid: 1 }),
      progressLine({ event: "run", load: "a", at: 1_001 }),
      progressLine({ event: "start", load: "a", at: 1_002 }),
      progressLine({ event: "end", load: "a", at: 1_003 }),
      progressLine({ event: "done", load: "a", at: 1_004 }),
      progressLine({ event: "run", load: "a", at: 1_005 }),
      progressLine({ event: "start", load: "a", at: 1_006 }),
      progressLine({ event: "end", load: "a", at: 1_007 }),
      progressLine({ event: "start", load: "a", at: 4_000 }),
      // Worker 2 finished b.test.ts's two runs and loaded c.test.ts, whose first row ended: a
      // hook or the code between rows holds it.
      progressLine({ event: "load", load: "b", at: 1_000, file: "b.test.ts", pid: 2 }),
      progressLine({ event: "run", load: "b", at: 1_001 }),
      progressLine({ event: "done", load: "b", at: 1_002 }),
      progressLine({ event: "run", load: "b", at: 1_003 }),
      progressLine({ event: "done", load: "b", at: 1_004 }),
      progressLine({ event: "load", load: "c", at: 2_000, file: "c.test.ts", pid: 2 }),
      progressLine({ event: "run", load: "c", at: 2_001 }),
      progressLine({ event: "start", load: "c", at: 2_002 }),
      progressLine({ event: "end", load: "c", at: 7_000 }),
      // Worker 3 finished d.test.ts alone: no file of its is running.
      progressLine({ event: "load", load: "d", at: 1_000, file: "d.test.ts", pid: 3 }),
      progressLine({ event: "run", load: "d", at: 1_001 }),
      progressLine({ event: "done", load: "d", at: 1_002 }),
      progressLine({ event: "run", load: "d", at: 1_003 }),
      progressLine({ event: "done", load: "d", at: 1_004 }),
      // Worker 4 is in e.test.ts's own code: no run of it has begun.
      progressLine({ event: "load", load: "e", at: 9_000, file: "e.test.ts", pid: 4 }),
      // Worker 5 died in f.test.ts's first row before the stop, a crash of its own: Bun reports
      // that file, and the stop names no file of a worker that was not alive at it.
      progressLine({ event: "load", load: "f", at: 1_000, file: "f.test.ts", pid: 5 }),
      progressLine({ event: "run", load: "f", at: 1_001 }),
      progressLine({ event: "start", load: "f", at: 1_002 }),
      // The last line of a file read while a worker wrote it can be cut short.
      '{"event":"sta',
    ];
    const stop: Stop = {
      signal: "SIGTERM",
      at: 10_000,
      afterMs: 9_500,
      progress: { text: lines.join("\n") },
      alive: [1, 2, 3, 4],
      names: {
        "a.test.ts": { names: ["a one", "outer > a two", "a three"] },
        "c.test.ts": { unreadable: "the run that reads its rows' names exited 1" },
      },
    };
    const lane = { reruns: 2, seed: SEED, script: "test:harness" };
    expect(runningFiles(stop, lane)).toEqual([
      {
        file: "a.test.ts",
        run: 2,
        place: "row",
        rowsBegun: 2,
        rowsRunning: 1,
        rowName: "outer > a two",
        nameUnread: null,
        quietSeconds: 6,
        reproduce: `bun run test:harness a.test.ts --seed=${SEED} --rerun-each=2`,
      },
      {
        file: "c.test.ts",
        run: 1,
        place: "between-rows",
        rowsBegun: 1,
        rowsRunning: 0,
        rowName: null,
        nameUnread: "the run that reads its rows' names exited 1",
        quietSeconds: 3,
        reproduce: `bun run test:harness c.test.ts --seed=${SEED}`,
      },
      {
        file: "e.test.ts",
        run: 1,
        place: "loading",
        rowsBegun: 0,
        rowsRunning: 0,
        rowName: null,
        nameUnread: null,
        quietSeconds: 1,
        reproduce: `bun run test:harness e.test.ts --seed=${SEED}`,
      },
    ]);

    // The report of that stop: Bun wrote no junit file and exited 143, which the stop explains,
    // so the one failure outside any test is the stop, and each running file has an error of its
    // own, the row's name in it.
    const report = summarise({
      lane: "harness",
      target: "e2e/__tests__",
      seed: SEED,
      reruns: 2,
      wallMs: 9_600,
      exitCode: 143,
      signal: null,
      junit: { unreadable: "Bun wrote no junit file: junit.xml is missing." },
      log: "",
      environment: {},
      memory: [],
      stop,
    });
    expect(kinds(report)).toEqual(["stopped"]);
    expect(report.exitCode).toBe(1);
    const errors = annotations(report).filter((line) => line.startsWith("::error "));
    expect(errors).toEqual([
      expect.stringContaining("The leg was stopped by SIGTERM after 9.5 s, before Bun ended"),
      "::error file=packages/dispatch/a.test.ts,title=Running when the leg was stopped::a.test.ts: " +
        'in row 2, "outer > a two", of run 2 of 2, 6.0 s since it began, when SIGTERM stopped ' +
        `the leg after 9.5 s (seed ${SEED}). Run it again from packages/dispatch: ` +
        `bun run test:harness a.test.ts --seed=${SEED} --rerun-each=2`,
      expect.stringContaining(
        "c.test.ts: in run 1 of 2, after row 1, whose name was not read (the run that reads its " +
          "rows' names exited 1), ended"
      ),
      expect.stringContaining("e.test.ts: in run 1 of 2, before it began"),
    ]);
    expect(markdown(report)).toContain("### The leg was stopped while 3 test files were running");

    // Bun's junit file after a stop: a `(worker crashed)` testcase says Bun had not taken in that
    // file's results when the stop came. It is named as such, and no failing row is made of it.
    const crashed = (file: string) =>
      `  <testsuite name="${file}" file="${file}" tests="1" assertions="0" failures="1" skipped="0" time="1" hostname="runner">\n` +
      `    <testcase name="(worker crashed)" classname="" time="0" file="${file}" assertions="0">\n` +
      `      <failure type="Error" message="worker process crashed before reporting results" />\n` +
      "    </testcase>\n  </testsuite>\n";
    const withJunit = summarise({
      lane: "harness",
      target: "e2e/__tests__",
      seed: SEED,
      reruns: 2,
      wallMs: 9_600,
      exitCode: 143,
      signal: null,
      junit: {
        text:
          '<?xml version="1.0" encoding="UTF-8"?>\n' +
          '<testsuites name="bun test" tests="2" assertions="0" failures="2" skipped="0" time="1">\n' +
          `${crashed("a.test.ts")}${crashed("f.test.ts")}</testsuites>\n`,
      },
      log: "",
      environment: {},
      memory: [],
      stop,
    });
    expect(withJunit.failingRows).toEqual([]);
    expect(withJunit.stop?.unreported).toEqual(["a.test.ts", "f.test.ts"]);
    expect(kinds(withJunit)).toEqual(["stopped"]);
    expect(withJunit.rowlessFailures[0].text).toEndWith(
      " Bun had not reported the results of 2 files when the stop came: a.test.ts, f.test.ts."
    );
  },
  rowLimit()
);

test(
  "a leg stopped while a row never ends names that row and exits 1",
  async () => {
    // The lie: the leg that reaches its deadline is ended from outside and names nothing, as
    // three web-cold legs did at their two-minute budget. Here the tool itself, on a copy of the
    // package whose one row spins without yielding, gets the SIGTERM `timeout` sends at the
    // deadline, once that row has begun.
    const directory = scratch();
    const tools = join(directory, "e2e", "tools");
    const rows = join(directory, "e2e", "__tests__");
    mkdirSync(tools, { recursive: true });
    mkdirSync(rows, { recursive: true });
    for (const name of ["unit-repeat.ts", "unit-repeat-progress.ts"]) {
      copyFileSync(fileURLToPath(new URL(`../tools/${name}`, import.meta.url)), join(tools, name));
    }
    await Bun.write(
      join(directory, "package.json"),
      `${JSON.stringify({ name: "stopped-leg", private: true, scripts: { "test:harness": "bun test --parallel --timeout 60000" } })}\n`
    );
    const mark = join(directory, "began");
    await Bun.write(join(rows, "steady.test.ts"), passes);
    await Bun.write(
      join(rows, "spins.test.ts"),
      `import { test } from "bun:test";\nimport { writeFileSync } from "node:fs";\n\n` +
        `test("before the spin", () => {});\n` +
        `test("spins for ever", () => {\n  writeFileSync(${JSON.stringify(mark)}, "");\n  for (;;) {}\n});\n` +
        `test("after the spin", () => {});\n`
    );
    const out = join(directory, "out");
    // The driver starts the tool, waits for the planted row to write its mark (or for the tool
    // to end first), and then sends the tool alone the SIGTERM the step's `timeout` sends.
    const driver =
      '"$@" & tool=$!; until [ -e "$MARK" ] || ! kill -0 "$tool" 2>/dev/null; do sleep 0.05; done; ' +
      'kill -TERM "$tool" 2>/dev/null; wait "$tool"';
    const { code, stdout } = await runBounded({
      argv: [
        "bash",
        "-c",
        driver,
        "driver",
        process.execPath,
        join(tools, "unit-repeat.ts"),
        "--lane",
        "harness",
        "--reruns",
        "2",
        "--seed",
        String(SEED),
        "--out",
        out,
      ],
      bound: "short",
      cwd: directory,
      env: {
        HOME: directory,
        PATH: process.env.PATH ?? "",
        TMPDIR: tmpdir(),
        GITHUB_ACTIONS: "true",
        MARK: mark,
      },
      from: import.meta.url,
    });
    expect(code).toBe(1);
    const report: Report = JSON.parse(readFileSync(join(out, "report.json"), "utf8"));
    expect(report.stop?.signal).toBe("SIGTERM");
    expect(report.stop?.running).toEqual([
      expect.objectContaining({
        file: "e2e/__tests__/spins.test.ts",
        run: 1,
        place: "row",
        rowName: "spins for ever",
      }),
    ]);
    // Bun's own `(worker crashed)` testcase for the file the stop ended says the stop came before
    // its results: the report names the file so, and makes no failing row of it.
    expect(report.failingRows).toEqual([]);
    expect(report.stop?.unreported).toContain("e2e/__tests__/spins.test.ts");
    expect(kinds(report)).toEqual(["stopped"]);
    expect(stdout).toMatch(
      /^::error file=packages\/dispatch\/e2e\/__tests__\/spins\.test\.ts,title=Running when the leg was stopped::e2e\/__tests__\/spins\.test\.ts: in row [1-3], "spins for ever", of run 1 of 2, /m
    );
    // The record of where each file was stays beside the report.
    expect(readFileSync(join(out, "progress.jsonl"), "utf8")).toContain('"event":"start"');
  },
  rowLimit("short")
);

/** What the budget check reads of one leg of the matrix. */
interface MeasuredLeg {
  readonly lane: string;
  readonly slowest: number;
  readonly otherSteps: number;
  readonly deadline: number;
  readonly stepMinutes: number;
  readonly jobMinutes: number;
}

/** The margin of every budget over what it was measured at: twice the slowest run. */
const MARGIN = 2;
/** The seconds the step's `timeout --kill-after` gives the tool to report after the deadline. */
const KILL_AFTER = 60;

function mapping(value: unknown): Readonly<Record<string, unknown>> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? Object.fromEntries(Object.entries(value))
    : undefined;
}

/** Every way the budgets of the workflow text `text` fall short of the measurements beside them,
 *  and the legs it read. A list of no problem over no leg is a check that read nothing, which
 *  the caller refuses. */
function budgetProblems(text: string): { problems: string[]; legs: MeasuredLeg[] } {
  const problems: string[] = [];
  const legs: MeasuredLeg[] = [];
  const job = mapping(mapping(mapping(Bun.YAML.parse(text))?.jobs)?.repeat);
  if (job === undefined) return { problems: ["the workflow has no job named repeat"], legs };
  if (job["timeout-minutes"] !== `\${{ matrix.job_minutes }}`) {
    problems.push("the job's timeout-minutes is not the matrix's job_minutes");
  }
  const steps = Array.isArray(job.steps) ? job.steps.map(mapping) : [];
  const step = steps.find(
    (each) => typeof each?.name === "string" && each.name.startsWith("Run the lane")
  );
  if (step?.["timeout-minutes"] !== `\${{ matrix.step_minutes }}`) {
    problems.push("the test step's timeout-minutes is not the matrix's step_minutes");
  }
  const run = typeof step?.run === "string" ? step.run : "";
  if (
    !run.includes(
      `timeout --foreground --signal=TERM --kill-after=${KILL_AFTER} \${{ matrix.deadline_seconds }} bun run test:repeat `
    )
  ) {
    problems.push(
      `the test step does not run test:repeat under timeout at the matrix's deadline_seconds with --kill-after=${KILL_AFTER}`
    );
  }
  const include = mapping(mapping(job.strategy)?.matrix)?.include;
  for (const entry of Array.isArray(include) ? include.map(mapping) : []) {
    const number = (key: string) => (typeof entry?.[key] === "number" ? entry[key] : Number.NaN);
    const leg = {
      lane: String(entry?.lane),
      slowest: number("slowest_seconds"),
      otherSteps: number("other_steps_seconds"),
      deadline: number("deadline_seconds"),
      stepMinutes: number("step_minutes"),
      jobMinutes: number("job_minutes"),
    };
    legs.push(leg);
    if (Object.values(leg).some((value) => Number.isNaN(value))) {
      problems.push(`${leg.lane}: a measurement or a budget is missing or is not a number`);
      continue;
    }
    if (leg.deadline < MARGIN * leg.slowest) {
      problems.push(
        `${leg.lane}: deadline_seconds ${leg.deadline} is under ${MARGIN} x the slowest ${leg.slowest} s`
      );
    }
    if (leg.stepMinutes * 60 < leg.deadline + KILL_AFTER + 60) {
      problems.push(
        `${leg.lane}: step_minutes ${leg.stepMinutes} is under the deadline, the ${KILL_AFTER} s kill-after and 60 s`
      );
    }
    if (leg.jobMinutes * 60 < leg.stepMinutes * 60 + MARGIN * leg.otherSteps) {
      problems.push(
        `${leg.lane}: job_minutes ${leg.jobMinutes} is under step_minutes and ${MARGIN} x the other steps' ${leg.otherSteps} s`
      );
    }
  }
  return { problems, legs };
}

test(
  "each budget of the repeat workflow is at least its measured slowest time times the margin",
  () => {
    // The lie: a budget set below what the leg takes on main's suite ends a leg that was passing,
    // as the 16-minute web and the two-minute web-cold budgets did. The workflow writes each
    // lane's measurement beside its budgets, and .github/ci-notes/dispatch-unit-repeat.md says
    // where each was read.
    const text = readFileSync(
      fileURLToPath(
        new URL("../../../../.github/workflows/dispatch-unit-repeat.yaml", import.meta.url)
      ),
      "utf8"
    );
    const { problems, legs } = budgetProblems(text);
    expect(legs.map((leg) => leg.lane)).toEqual(["web", "web-cold", "harness"]);
    expect(problems).toEqual([]);

    // The controls: each budget moved below its rule, each in a copy of the text, fails by name.
    const planted = (from: string, to: string) => {
      expect(text).toContain(from);
      return budgetProblems(text.replace(from, to)).problems;
    };
    expect(planted("deadline_seconds: 2280", "deadline_seconds: 2200")).toEqual([
      "web: deadline_seconds 2200 is under 2 x the slowest 1133 s",
    ]);
    expect(planted("slowest_seconds: 1439", "slowest_seconds: 1500")).toEqual([
      "harness: deadline_seconds 2880 is under 2 x the slowest 1500 s",
    ]);
    expect(planted("step_minutes: 7 ", "step_minutes: 6 ")).toEqual([
      "web-cold: step_minutes 6 is under the deadline, the 60 s kill-after and 60 s",
    ]);
    expect(planted("job_minutes: 58 ", "job_minutes: 57 ")).toEqual([
      "harness: job_minutes 57 is under step_minutes and 2 x the other steps' 229 s",
    ]);
    expect(
      planted(`--kill-after=60 \${{ matrix.deadline_seconds }}`, "--kill-after=60 600")
    ).toEqual([
      "the test step does not run test:repeat under timeout at the matrix's deadline_seconds with --kill-after=60",
    ]);
    expect(planted(`timeout-minutes: \${{ matrix.job_minutes }}`, "timeout-minutes: 60")).toEqual([
      "the job's timeout-minutes is not the matrix's job_minutes",
    ]);
  },
  rowLimit()
);
