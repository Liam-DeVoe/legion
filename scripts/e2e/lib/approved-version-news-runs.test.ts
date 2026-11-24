import { expect, test } from "bun:test";
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { hostGate } from "@legion/test-fixtures/host-tools";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import {
  a4,
  answers,
  bench,
  failed,
  fullPlan,
  modelKey,
  type NewsRun,
  newsStandIns,
  off,
  passed,
  plan,
  retired,
  runLine,
  runsOf,
  script,
} from "./approved-version-news-fixture";
import { inOwnGroup } from "./run-verdict-fixture";
import { sourcedLibraries } from "./script-functions";

// approved-version-news.sh's run, driven whole over lib/approved-version-news-fixture.ts's
// stand-ins from a scratch checkout whose one commit holds the base's prompt files and is
// origin/main, so that commit is the base: its plan check reads the stand-in benchmark's plan,
// its sessions' run is the stand-in's, and the script's own finish and the real
// lib/run-verdict.sh print the last line. A full run reads PASS. A run that sets any of
// APPROVED_VERSION_NEWS_ARM, APPROVED_VERSION_NEWS_STEP and APPROVED_VERSION_NEWS_RUNS is a
// measurement: its last line reads MEASURED, never PASS, names the pick, each step and arm of the
// full run's plan it left out and its count of runs where it is not three, and exits 0; a head run
// of the pick that did not pass still reads FAIL. The counts and the checks follow the driver's
// plan, a pick the driver refuses is refused in its words, and a run a signal ends reads FAIL.
// approved-version-news-verdicts.test.ts runs the script's plan check, its counts and its last
// line by name.

const scratch = temporaryDirectories("approved-version-news-run-test");
// Read once, at module scope: a host without git, tar or setsid skips the script's runs naming
// what it lacks, and under GITHUB_ACTIONS fails each by its own name (hostGate).
const host = hostGate({ programs: ["git", "tar", "setsid"] });

/**
 * Runs the script with the variables PICK sets, and ARGS, over the stand-ins RUN asks for, from a
 * scratch checkout of its own: the script and the libraries it sources at the paths they have
 * here, in a git repository whose one commit holds the base's prompt files and is origin/main. It
 * runs in a process group of its own, so a stand-in's signal reaches the whole run. Every line,
 * its stderr's included, is in `out` (the script's transcript sends both there), its last line is
 * `last`, and `calls` holds the stand-in go's record of each call.
 */
function newsRun(pick: Record<string, string>, run: NewsRun, args: string[] = []) {
  const dir = scratch();
  const checkout = join(dir, "checkout");
  for (const path of sourcedLibraries(script)) {
    const to = join(checkout, "scripts", "e2e", relative(dirname(script), path));
    mkdirSync(dirname(to), { recursive: true });
    copyFileSync(path, to);
  }
  for (const prompt of ["roles/reviewer.md", "go/worker-common.md"]) {
    const to = join(checkout, "packages", "daemon", "internal", "prompts", prompt);
    mkdirSync(dirname(to), { recursive: true });
    writeFileSync(to, "a base prompt\n");
  }
  const env = { ...newsStandIns(dir, run), ...pick };
  for (const gitArgs of [
    ["init", "-q"],
    ["add", "."],
    ["commit", "-q", "--no-gpg-sign", "-m", "the base"],
    ["update-ref", "refs/remotes/origin/main", "HEAD"],
  ]) {
    const git = Bun.spawnSync(
      [
        "git",
        "-C",
        checkout,
        "-c",
        "user.name=fixture",
        "-c",
        "user.email=fixture@example.invalid",
        ...gitArgs,
      ],
      { env }
    );
    if (git.exitCode !== 0) throw new Error(`git ${gitArgs.join(" ")}: ${git.stderr.toString()}`);
  }
  const ended = inOwnGroup(
    ["bash", join(checkout, "scripts", "e2e", "approved-version-news.sh"), ...args],
    env
  );
  const out = ended.out + ended.err;
  let calls: string[] = [];
  try {
    calls = readFileSync(join(dir, "go-calls"), "utf8").trimEnd().split("\n");
  } catch {
    // The run ended before the plan check ran the benchmark.
  }
  return { ...ended, out, last: out.trimEnd().split("\n").at(-1) ?? "", calls };
}

const a4Head = retired[0].headOnly;
const answersHead = retired[1].headOnly;
const noControl = "(no control: the base arm passed every run that reached its assertion)";
// A full run's runs, each passing in the head arm, and off's base arm failing at its assertion.
const fullRuns = [
  ...runsOf(a4, "head", passed, passed, passed),
  ...runsOf(off, "head", passed, passed, passed),
  ...runsOf(off, "base", failed, failed, failed),
  ...runsOf(answers, "head", passed, passed, passed),
  "model calls: 12 in all, over 12 runs",
];
// The driver's plan of each pick below, as pickSessions makes it from the plan of a full run.
const basePlan = plan(
  [
    ["a4", a4, "base"],
    ["off", off, "base"],
    ["answers", answers, "base"],
  ],
  3
);
const onePlan = plan(
  [
    ["a4", a4, "head"],
    ["off", off, "head base"],
    ["answers", answers, "head"],
  ],
  1
);

host.describe("the script's run", () => {
  // A full run reads PASS. Its plan check reads the benchmark's plan with no database, and the run
  // hands the benchmark the base commit's prompt files, that commit, a log directory in the
  // evidence directory and the key file, once, under the deadline the plan makes: nine minutes for
  // each of its 12 runs and twenty more.
  test("reads PASS for a full run, which picks nothing, and runs the benchmark once, with the base's prompts and the plan's deadline", () => {
    const run = newsRun({}, { plans: { full: fullPlan }, sessions: fullRuns });
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news: PASS in \d+s \(/);
    expect(run.last).toContain(
      `(a4 head 3/3 ${a4Head}; off head 3/3, base 0/3 (control bites); answers head 3/3 ${answersHead}; model calls: 12, evidence: `
    );
    const [planCall, sessionsCall, ...more] = run.calls;
    expect(more).toEqual([]);
    expect(planCall).toContain(
      `-run ^$ -bench ^${bench}$ -benchtime=1x ./internal/daemon -args -part-two-plan | dsn=unset`
    );
    expect(sessionsCall).toContain(
      `-timeout ${12 * 9 + 20}m -run ^$ -bench ^${bench}$ -benchtime=1x ./internal/daemon | prompt=a base prompt | commit=`
    );
    expect(sessionsCall).toMatch(/ \| logs=.*\/evidence\/part-two\.[^/ ]+ \| key=.*\/model-key$/);
  }, 60_000);

  test("reads MEASURED, not PASS, for APPROVED_VERSION_NEWS_ARM=base, naming each head arm it left out", () => {
    const run = newsRun(
      { APPROVED_VERSION_NEWS_ARM: "base" },
      {
        plans: { full: fullPlan, pick: basePlan },
        sessions: [
          ...runsOf(a4, "base", passed, passed, passed),
          ...runsOf(off, "base", failed, failed, failed),
          ...runsOf(answers, "base", passed, passed, passed),
          "model calls: 9 in all, over 9 runs",
        ],
      }
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news: MEASURED in \d+s \(/);
    expect(run.last).toContain(
      `(a partial run picked by APPROVED_VERSION_NEWS_ARM=base, leaving out a4 head, off head, answers head: a4 base 3/3 ${noControl}; off base 0/3 (control bites); answers base 3/3 ${noControl}; model calls: 9, evidence: `
    );
  }, 60_000);

  test("reads MEASURED, not PASS, for APPROVED_VERSION_NEWS_STEP=a4, naming each step and arm it left out", () => {
    const run = newsRun(
      { APPROVED_VERSION_NEWS_STEP: "a4" },
      {
        plans: { full: fullPlan, pick: plan([["a4", a4, "head"]], 3) },
        sessions: [
          ...runsOf(a4, "head", passed, passed, passed),
          "model calls: 3 in all, over 3 runs",
        ],
      }
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news: MEASURED in \d+s \(/);
    expect(run.last).toContain(
      `(a partial run picked by APPROVED_VERSION_NEWS_STEP=a4, leaving out off head, off base, answers head: a4 head 3/3 ${a4Head}; model calls: 3, evidence: `
    );
  }, 60_000);

  test("reads MEASURED, not PASS, for APPROVED_VERSION_NEWS_RUNS=1, naming its count of runs", () => {
    const run = newsRun(
      { APPROVED_VERSION_NEWS_RUNS: "1" },
      {
        plans: { full: fullPlan, pick: onePlan },
        sessions: [
          ...runsOf(a4, "head", passed),
          ...runsOf(off, "head", passed),
          ...runsOf(off, "base", failed),
          ...runsOf(answers, "head", passed),
          "model calls: 4 in all, over 4 runs",
        ],
      }
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news: MEASURED in \d+s \(/);
    expect(run.last).toContain(
      `(a partial run picked by APPROVED_VERSION_NEWS_RUNS=1, with 1 run of each arm in place of 3: a4 head 1/1 ${a4Head}; off head 1/1, base 0/1 (control bites); answers head 1/1 ${answersHead}; model calls: 4, evidence: `
    );
    expect(run.calls.at(-1)).toContain(` -timeout ${4 * 9 + 20}m `);
  }, 60_000);

  test("reads FAIL for a pick whose head run did not pass", () => {
    const run = newsRun(
      { APPROVED_VERSION_NEWS_STEP: "off" },
      {
        plans: { full: fullPlan, pick: plan([["off", off, "head base"]], 3) },
        sessions: [...runsOf(off, "head", passed, failed), "model calls: 2 in all, over 2 runs"],
        ended: "FAIL",
      }
    );
    expect(run.code).toBe(1);
    expect(run.out).not.toContain("approved version news: MEASURED");
    expect(run.out).toContain(
      `FAIL counts: the head arm did not reach its assertion and pass in every run: off: 2 of 3 head runs ran; ${runLine(off, "head", 2, failed)}\n`
    );
    expect(run.last).toMatch(/^approved version news: FAIL at counts after \d+s \(/);
    expect(run.last).toContain(
      "(a partial run picked by APPROVED_VERSION_NEWS_STEP=off, leaving out a4 head, answers head: off head 1/2, base 0/0 (base did not run); model calls: 2, evidence: "
    );
  }, 60_000);

  // A benchmark whose body skipped, as it does for a prerequisite the driver reads, passes as a
  // benchmark: the run reads FAIL, naming each head run that did not run.
  test("reads FAIL for a benchmark whose body skipped", () => {
    const run = newsRun({}, { plans: { full: fullPlan }, sessions: [], ended: "SKIP" });
    expect(run.code).toBe(1);
    expect(run.out).toContain(
      "FAIL counts: the head arm did not reach its assertion and pass in every run: a4: 0 of 3 head runs ran; off: 0 of 3 head runs ran; answers: 0 of 3 head runs ran\n"
    );
    expect(run.last).toMatch(/^approved version news: FAIL at counts after \d+s \(/);
  }, 60_000);

  // The sessions ran before the key-leak check, so its last line says they ran and were not
  // counted, never that they did not run.
  test("fails at the key-leak check where the evidence holds the key's first 8 characters", () => {
    const run = newsRun(
      {},
      { plans: { full: fullPlan }, sessions: [...fullRuns, `   evidence: ${modelKey}`] }
    );
    expect(run.code).toBe(1);
    expect(run.out).toContain(
      "FAIL key-leak: the model key's first 8 characters are in the evidence directory "
    );
    expect(run.last).not.toContain(modelKey.slice(0, 8));
    expect(run.last).toMatch(
      /^approved version news: FAIL at key-leak after \d+s \(the sessions ran, and the run stopped before it counted them, evidence: /
    );
  }, 60_000);

  // The script keeps no table of the steps: a step the driver's plan adds is counted in each arm
  // the plan makes it in, and a plan that gives a4 its base arm back, while head_only still holds
  // the measurement that retired it, fails the run at the plan check, naming a4.
  test("counts each arm of a step the driver's plan adds", () => {
    const extra = "extra-a-step-the-driver-added";
    const withExtra = plan(
      [
        ["a4", a4, "head"],
        ["off", off, "head base"],
        ["answers", answers, "head"],
        ["extra", extra, "head base"],
      ],
      3
    );
    const run = newsRun(
      {},
      {
        plans: { full: withExtra },
        sessions: [
          ...fullRuns.slice(0, -1),
          ...runsOf(extra, "head", passed, passed, passed),
          ...runsOf(extra, "base", passed, failed, passed),
          "model calls: 18 in all, over 18 runs",
        ],
      }
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news: PASS in \d+s \(/);
    expect(run.last).toContain(
      `; answers head 3/3 ${answersHead}; extra head 3/3, base 2/3 (control bites); model calls: 18, evidence: `
    );
  }, 60_000);

  test("fails at the plan check, naming a4, where the driver's plan gives a4 its base arm back", () => {
    const back = plan(
      [
        ["a4", a4, "head base"],
        ["off", off, "head base"],
        ["answers", answers, "head"],
      ],
      3
    );
    const run = newsRun({}, { plans: { full: back }, sessions: [] });
    expect(run.code).toBe(1);
    expect(run.out).toContain(
      "FAIL plan: the driver's plan of a full run makes a4 in both arms (head base), and head_only holds a measurement that retired its base arm\n"
    );
    expect(run.last).toMatch(/^approved version news: FAIL at plan after \d+s \(/);
    expect(run.calls).toHaveLength(1);
  }, 60_000);

  test("refuses with exit 2, in the driver's words, a pick the driver refuses", () => {
    const refusal =
      'plan refused: APPROVED_VERSION_NEWS_STEP="a5" names no step of part two: it is one of a4, off, answers';
    const run = newsRun(
      { APPROVED_VERSION_NEWS_STEP: "a5" },
      { plans: { full: fullPlan, pick: refusal }, sessions: [] }
    );
    expect(run.code).toBe(2);
    expect(run.out).toContain(
      `FAIL plan: the driver refuses the pick: ${refusal.slice("plan refused: ".length)}\n`
    );
    expect(run.last).toMatch(/^approved version news: FAIL at plan after \d+s \(/);
  }, 60_000);

  test("refuses an argument with exit 2, before it runs the benchmark", () => {
    const run = newsRun({}, { plans: { full: fullPlan }, sessions: fullRuns }, ["--part", "one"]);
    expect(run.code).toBe(2);
    expect(run.out).toContain(
      "FAIL arguments: this script takes no argument, and was given '--part one': "
    );
    expect(run.calls).toEqual([]);
  }, 60_000);

  for (const signal of ["TERM", "HUP", "INT"] as const) {
    test(`reads FAIL at sessions for a run a group SIG${signal} ended in the sessions' run`, () => {
      const run = newsRun(
        {},
        { plans: { full: fullPlan }, sessions: fullRuns, kill: { at: "sessions", signal } }
      );
      expect(run.out).not.toContain("approved version news: PASS");
      expect(run.out).toMatch(
        /\napproved version news: FAIL at sessions after \d+s \(the sessions did not run, evidence: .*\)\n/
      );
      expect(run.code === 0 && run.signal === null).toBe(false);
    }, 60_000);
  }

  test("reads FAIL at plan for a run a group SIGTERM ended in the plan check", () => {
    const run = newsRun(
      {},
      { plans: { full: fullPlan }, sessions: fullRuns, kill: { at: "plan", signal: "TERM" } }
    );
    expect(run.out).not.toContain("approved version news: PASS");
    expect(run.out).toMatch(/\napproved version news: FAIL at plan after \d+s \(/);
    expect(run.code === 143 || run.signal === "SIGTERM").toBe(true);
  }, 60_000);
});
