import { describe, expect, test } from "bun:test";
import { chmodSync, existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  temporaryDirectories,
  temporaryDirectory,
} from "@legion/test-fixtures/temporary-directories";
import {
  a4,
  answers,
  body,
  failed,
  fullPlan,
  off,
  passed,
  plan,
  retired,
  runLine,
  runsOf,
  script,
} from "./approved-version-news-fixture";
import { stopped } from "./run-verdict-fixture";
import {
  expectNoMissingHelpers,
  nameMissingHelpers,
  scriptAssignments,
  scriptFunctions,
} from "./script-functions";

// approved-version-news.sh's plan of the runs, its count of each arm and its last line, taken from
// the script by name and run over the benchmark's output written as the driver prints it. The
// steps, arms and count of runs are the driver's plan, which the script reads and keeps no copy
// of: its plan check holds head_only to the plan of a full run (check_plan), and its count of each
// arm (part_two_step) reads the arms the plan makes a step in. That count counts only runs that
// reached the step's assertion: a run that stopped at its setup is named and not counted, so it
// never makes the control bite, and a head run that stopped there is no pass. The last line
// (finish, the script's EXIT trap, through lib/run-verdict.sh) reads PASS or MEASURED only for a
// run that reached the script's end: a signal ends a shell with its EXIT trap reading status 0, so
// a killed run must read FAIL. A run that picks its runs reads MEASURED, never PASS.
// approved-version-news-runs.test.ts drives the script's run whole, so its own end mark is the one
// under test.

const assignment = scriptAssignments(script);
const fn = scriptFunctions(script);
const helperGuard = nameMissingHelpers(script);
const dir = temporaryDirectory("approved-version-news-verdicts-test");
const scratch = temporaryDirectories("approved-version-news-verdicts-jj");

let files = 0;
/** A file of the benchmark's output holding LINES, one a line. */
function proofLog(lines: string[]): string {
  const path = join(dir, `proof-${++files}.log`);
  writeFileSync(path, lines.map((line) => `${line}\n`).join(""));
  return path;
}

/** Runs COMMANDS after the script's plan and count settings, its plan and count functions and
 * its note and fail, in its plan check, over the driver's plan of a full run at this change
 * (fullPlan) with no pick: COMMANDS set another plan or a pick. */
function controls(commands: string) {
  const result = Bun.spawnSync([
    "bash",
    "-c",
    `set -euo pipefail
${helperGuard}
${fn("note")}
${fn("fail")}
${assignment("part_two_minutes")}
${assignment("part_two_setup_minutes")}
${assignment("head_only")}
${fn("head_only_why")}
${fn("plan_words")}
${fn("plan_step")}
${fn("plan_runs")}
${fn("check_plan")}
${fn("part_two_deadline")}
${fn("pick_said")}
${fn("part_two_step")}
check=plan full_plan=${quoted(fullPlan)} part_two_plan=${quoted(fullPlan)} pick_step='' pick_arm='' pick_runs=''
${commands}`,
  ]);
  const out = result.stdout.toString();
  const err = result.stderr.toString();
  expectNoMissingHelpers(out, err);
  return { code: result.exitCode, out, err };
}

/** TEXT as one single-quoted word of bash. */
const quoted = (text: string) => `'${text.replaceAll("'", `'\\''`)}'`;
// The driver's plan of a pick of the head arm.
const headPlan = plan(
  [
    ["a4", a4, "head"],
    ["off", off, "head"],
    ["answers", answers, "head"],
  ],
  3
);
const setupFailed =
  "SETUP: the reviewer of AS1-2 did not end its start turn within the run's bound";
const harnessFailed = "HARNESS: the harness failed; its log is above";

/** part_two_step over the lines LINES of the step named WORD on the last line, after PICK, the
 * plan and the pick the script's plan check and arguments check leave ("" for a full run's): the
 * step's part of the last line, the head runs that missed, and the notes it printed. */
function partTwoStep(word: string, lines: string[], pick = "") {
  const proof = proofLog([`=== RUN   ${body}`, ...lines]);
  const run = controls(`${pick}
part_two_step ${JSON.stringify(proof)} ${word}
printf 'said=%s\\nmissed=%s\\n' "$step_said" "$head_missed"`);
  expect(run.err).toBe("");
  const said = /^said=(.*)$/m.exec(run.out)?.[1];
  const missed = /^missed=(.*)$/m.exec(run.out)?.[1];
  return { said, missed, notes: run.out };
}

describe("part two's count of each arm", () => {
  test("does not count a base run that stopped at its setup, and does not bite on it", () => {
    const step = partTwoStep("off", [
      ...runsOf(off, "head", passed, passed, passed),
      runLine(off, "base", 1, passed),
      runLine(off, "base", 2, passed),
      runLine(off, "base", 3, setupFailed),
    ]);
    expect(step.said).toBe(
      "off head 3/3, base 2/2 with 1 not counted (no control: the base arm passed every run that reached its assertion)"
    );
    expect(step.missed).toBe("");
    expect(step.notes).toContain(`   not counted: ${runLine(off, "base", 3, setupFailed)}\n`);
  });

  test("bites on a base run that failed at its assertion, beside one not counted", () => {
    const step = partTwoStep("off", [
      ...runsOf(off, "head", passed, passed, passed),
      runLine(off, "base", 1, passed),
      runLine(off, "base", 2, failed),
      runLine(off, "base", 3, setupFailed),
    ]);
    expect(step.said).toBe("off head 3/3, base 1/2 with 1 not counted (control bites)");
    expect(step.missed).toBe("");
  });

  test("is no control when every base run stopped before its assertion", () => {
    const step = partTwoStep("off", [
      ...runsOf(off, "head", passed, passed, passed),
      runLine(off, "base", 1, setupFailed),
      runLine(off, "base", 2, setupFailed),
      runLine(off, "base", 3, harnessFailed),
    ]);
    expect(step.said).toBe(
      "off head 3/3, base 0/0 with 3 not counted (no control: no base run reached its assertion)"
    );
  });

  test("names a head run that stopped at its setup, which is no pass", () => {
    // The driver stops at the first head run that does not pass, so the base arm never runs.
    const step = partTwoStep("off", [
      runLine(off, "head", 1, passed),
      runLine(off, "head", 2, setupFailed),
    ]);
    expect(step.said).toBe("off head 1/1 with 1 not counted, base 0/0 (base did not run)");
    expect(step.missed).toBe(
      `; off: 2 of 3 head runs ran; ${runLine(off, "head", 2, setupFailed)}`
    );
  });

  for (const { step: retiredStep, word, headOnly } of retired) {
    // A run with no arm picked makes the step in the head arm alone: the last line names the
    // measurement that retired its base arm, and a base line the driver printed for it is not
    // counted, so a base FAIL there would not make a control bite that the full run no longer has.
    test(`counts the head arm of ${word} alone in a full run, and names why its base arm is retired`, () => {
      const step = partTwoStep(word, [
        ...runsOf(retiredStep, "head", passed, passed, passed),
        runLine(retiredStep, "base", 1, failed),
      ]);
      expect(step.said).toBe(`${word} head 3/3 ${headOnly}`);
      expect(step.missed).toBe("");
      expect(step.notes).not.toContain(`${retiredStep} base:`);
    });

    test(`still owes every head run of ${word} in a full run`, () => {
      const step = partTwoStep(word, [
        runLine(retiredStep, "head", 1, passed),
        runLine(retiredStep, "head", 2, setupFailed),
      ]);
      expect(step.said).toBe(`${word} head 1/1 with 1 not counted ${headOnly}`);
      expect(step.missed).toBe(
        `; ${word}: 2 of 3 head runs ran; ${runLine(retiredStep, "head", 2, setupFailed)}`
      );
    });

    // A pick of the retired base arm alone, the command that repeats the measurement: the arm is
    // counted against the pick's own count of runs, no head run is owed, and a base FAIL still
    // bites.
    test(`counts a pick of the base arm of ${word} alone against its own count of runs`, () => {
      const outcomes = [passed, failed, failed, setupFailed, passed];
      const runs = [...outcomes, failed, failed, passed, failed, failed];
      const step = partTwoStep(
        word,
        runsOf(retiredStep, "base", ...runs),
        `pick_arm=base part_two_plan=${quoted(plan([[word, retiredStep, "base"]], 10))}`
      );
      expect(step.said).toBe(`${word} base 3/9 with 1 not counted (control bites)`);
      expect(step.missed).toBe("");
      expect(step.notes).toContain(
        `   ${retiredStep} base: 3 of 9 runs that reached the assertion passed; 1 not counted (SETUP or HARNESS)\n`
      );
    });

    // "head only" is the step in a run that picks no arm, where its base arm's retirement is the
    // reason no base ran. A pick of the head arm leaves out its base arm as it does every step's,
    // and says so in the same words.
    test(`is no control for ${word} too when the pick names the head arm`, () => {
      const step = partTwoStep(
        word,
        runsOf(retiredStep, "head", passed, passed, passed),
        `pick_arm=head part_two_plan=${quoted(headPlan)}`
      );
      expect(step.said).toBe(`${word} head 3/3 (no control: the pick leaves the base arm out)`);
      expect(step.missed).toBe("");
    });
  }

  test("is no control when the pick leaves the base arm out", () => {
    const step = partTwoStep(
      "off",
      runsOf(off, "head", passed, passed, passed),
      `pick_arm=head part_two_plan=${quoted(headPlan)}`
    );
    expect(step.said).toBe("off head 3/3 (no control: the pick leaves the base arm out)");
    expect(step.missed).toBe("");
  });

  // The arms come from the driver's plan, not from head_only: a plan that makes answers in both
  // arms has the script read its base lines and the control bite, though head_only still holds
  // the measurement that retired its base arm, which the plan check (below) refuses first.
  test("reads the arms of each step from the driver's plan", () => {
    const step = partTwoStep(
      "answers",
      [
        ...runsOf(answers, "head", passed, passed, passed),
        runLine(answers, "base", 1, passed),
        runLine(answers, "base", 2, failed),
        runLine(answers, "base", 3, passed),
      ],
      `part_two_plan=${quoted(plan([["answers", answers, "head base"]], 3))}`
    );
    expect(step.said).toBe("answers head 3/3, base 2/3 (control bites)");
    expect(step.missed).toBe("");
  });
});

/** check_plan over the driver's plan of a full run PLAN: its exit status and what it printed on
 * stderr. */
function checkPlan(text: string) {
  const run = controls(`full_plan=${quoted(text)}
check_plan`);
  return { code: run.code, err: run.err };
}

// head_only holds the one fact of part two's steps the driver does not know, the measurement that
// retired a base arm, and the plan check holds it to the driver's plan of a full run: a step the
// plan makes in the head arm alone needs a measurement, and a measurement needs a step the plan
// makes in the head arm alone. Each failure names the step.
describe("the plan check", () => {
  test("takes the driver's plan of a full run at this change", () => {
    expect(checkPlan(fullPlan)).toEqual({ code: 0, err: "" });
  });

  test("fails naming off, which a plan makes in the head arm alone with no measurement", () => {
    expect(checkPlan(headPlan)).toEqual({
      code: 1,
      err: "FAIL plan: the driver's plan of a full run makes off in the head arm alone (head), and head_only holds no measurement that retired its base arm\n",
    });
  });

  test("fails naming a4, whose base arm a plan gives back while head_only holds its measurement", () => {
    const text = plan(
      [
        ["a4", a4, "head base"],
        ["off", off, "head base"],
        ["answers", answers, "head"],
      ],
      3
    );
    expect(checkPlan(text)).toEqual({
      code: 1,
      err: "FAIL plan: the driver's plan of a full run makes a4 in both arms (head base), and head_only holds a measurement that retired its base arm\n",
    });
  });

  test("fails naming answers, a step of head_only the plan does not make", () => {
    const text = plan(
      [
        ["a4", a4, "head"],
        ["off", off, "head base"],
      ],
      3
    );
    expect(checkPlan(text)).toEqual({
      code: 1,
      err: "FAIL plan: head_only holds a measurement that retired the base arm of answers, which the driver's plan of a full run does not make\n",
    });
  });
});

/** part_two_deadline after PICK, the plan of a pick ("" for a full run's). */
function partTwoDeadline(pick = "") {
  const run = controls(`${pick}
part_two_deadline`);
  expect(run.err).toBe("");
  return run.out.trim();
}

// The driver's deadline counts the runs its plan makes: nine minutes a run and twenty more. A full
// run makes a4 and answers three times each in the head arm and off three times in each arm, 12
// runs; one that counted a retired base arm too would allow 15 or 18. A pick of a retired base arm
// counts that arm alone.
describe("part two's deadline", () => {
  test("counts no base run of a4 or answers in a full run", () => {
    expect(partTwoDeadline()).toBe(`${12 * 9 + 20}m`);
  });

  for (const [step, word] of [
    [a4, "a4"],
    [answers, "answers"],
  ] as const) {
    test(`counts the runs of a pick of the base arm of ${word}`, () => {
      expect(partTwoDeadline(`part_two_plan=${quoted(plan([[word, step, "base"]], 10))}`)).toBe(
        `${10 * 9 + 20}m`
      );
    });
  }
});

/** A scratch directory holding a jj that prints SAID for `jj --version`. */
function fakeJJ(said: string) {
  const bin = scratch();
  writeFileSync(join(bin, "jj"), `#!/bin/sh\nprintf '%s\\n' ${JSON.stringify(said)}\n`);
  chmodSync(join(bin, "jj"), 0o755);
  return bin;
}

/** check_jj with PATH holding a jj that prints SAID for `jj --version`, or no jj at all for null,
 * and LEGION_JJ_PATH naming a jj that prints OVERRIDE where it is given: its exit status and what
 * it printed on stderr. */
function checkJJ(said: string | null, override?: string) {
  const bin = said === null ? scratch() : fakeJJ(said);
  const env: Record<string, string> = { PATH: `${bin}:/usr/bin:/bin` };
  if (override !== undefined) {
    env.LEGION_JJ_PATH = join(fakeJJ(override), "jj");
  }
  const result = Bun.spawnSync(
    [
      Bun.which("bash") ?? "bash",
      "-c",
      `set -euo pipefail
${helperGuard}
check=arguments
${fn("fail")}
${fn("check_jj")}
check_jj`,
    ],
    { env }
  );
  const err = result.stderr.toString();
  expectNoMissingHelpers(result.stdout.toString(), err);
  return { code: result.exitCode, err };
}

// Each run boots the whole daemon, whose boot refuses a jj below 0.38, so the arguments check
// fails the run before it builds anything on a jj below that, on one whose version it cannot read
// and on none, and takes 0.38 and every later release, a build's suffix included. It reads the jj
// the daemon runs: LEGION_JJ_PATH where it is set, and the jj on PATH otherwise.
describe("the arguments check's jj", () => {
  for (const said of ["jj 0.38.0", "jj 0.45.1-sami.2026-11-09-abc123", "jj 1.0.0"]) {
    test(`takes ${said}`, () => {
      expect(checkJJ(said)).toEqual({ code: 0, err: "" });
    });
  }

  test("fails on jj 0.37", () => {
    expect(checkJJ("jj 0.37.9")).toEqual({
      code: 1,
      err: "FAIL arguments: jj is jj 0.37, below 0.38, the oldest jj the daemon's boot starts with\n",
    });
  });

  test("takes the jj LEGION_JJ_PATH names where PATH holds none", () => {
    expect(checkJJ(null, "jj 0.38.0")).toEqual({ code: 0, err: "" });
  });

  test("fails on the jj LEGION_JJ_PATH names, whatever the jj on PATH", () => {
    const run = checkJJ("jj 0.45.1", "jj 0.37.9");
    expect(run.code).toBe(1);
    expect(run.err).toContain("is jj 0.37, below 0.38");
  });

  test("fails on a version it cannot read", () => {
    const run = checkJJ("jujutsu, a build of no release");
    expect(run.code).toBe(1);
    expect(run.err).toContain("names no release");
  });

  test("fails where PATH holds no jj", () => {
    const run = checkJJ(null);
    expect(run.code).toBe(1);
    expect(run.err).toContain("FAIL arguments: jj --version failed");
  });
});

/** Runs COMMANDS in a bash whose EXIT trap is the script's finish, with the real
 * lib/run-verdict.sh, over the state finish reads as a run standing in its `sessions` check
 * leaves it, with SAID as what it counted and VERDICT as its verdict, "" for PASS. COMMANDS call
 * run_end where the script reaches its end; approved-version-news-runs.test.ts drives the
 * script's own run_end. TREES are the scratch trees the run made, which finish removes. */
function finishRun(commands: string, verdict = "", said = "", trees = ["", ""]) {
  const result = Bun.spawnSync([
    "bash",
    "-c",
    `set -euo pipefail
${helperGuard}
. ${JSON.stringify(join(import.meta.dir, "run-verdict.sh"))}
${fn("finish")}
work=${quoted(trees[0])} prompts_tree=${quoted(trees[1])} part_two_said=${quoted(said)}
verdict=${quoted(verdict)} version_news_evidence=/evidence version_news_started=$SECONDS check=sessions
trap finish EXIT
${commands}`,
  ]);
  const out = result.stdout.toString();
  const err = result.stderr.toString();
  expectNoMissingHelpers(out, err);
  return { code: result.exitCode, signal: result.signalCode, out, err };
}

// What finish prints for a run that ended before the script's end with status 0:
// lib/run-verdict.sh's line that says so (stopped), then the FAIL line.
const interrupted = (said: string) =>
  new RegExp(
    `^${stopped}\\napproved version news: FAIL at sessions after \\d+s \\(${said}, evidence: /evidence\\)\\n$`
  );
const measured = `a partial run picked by APPROVED_VERSION_NEWS_STEP=a4, leaving out off head, off base, answers head: a4 head 3/3 ${retired[0].headOnly}`;

describe("the last line", () => {
  test("prints no MEASURED for a measurement a SIGTERM ended before its end", () => {
    const run = finishRun("kill -TERM $$\nsleep 5\nrun_end", "MEASURED");
    expect(run.out).toBe("");
    expect(run.err).toMatch(interrupted("the sessions did not run"));
    expect(run.code === 143 || run.signal === "SIGTERM").toBe(true);
  });

  test("prints MEASURED for a measurement that reached its end", () => {
    const run = finishRun("run_end\nexit 0", "MEASURED", measured);
    expect(run.code).toBe(0);
    expect(run.err).toBe("");
    expect(run.out).toMatch(/^approved version news: MEASURED in \d+s \(/);
    expect(run.out).toEndWith(`(${measured}, evidence: /evidence)\n`);
  });

  test("reads FAIL, and exits 1, for an exit 0 the script makes before its end", () => {
    const run = finishRun("exit 0");
    expect(run.code).toBe(1);
    expect(run.out).toBe("");
    expect(run.err).toMatch(interrupted("the sessions did not run"));
  });

  test("removes the scratch trees the run made, whatever its end", () => {
    const trees = [scratch(), scratch()];
    const run = finishRun("exit 0", "", "", trees);
    expect(run.code).toBe(1);
    for (const tree of trees) expect(existsSync(tree)).toBe(false);
  });
});
