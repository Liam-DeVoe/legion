import { beforeAll, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { FinishedIssue, PlanRecord, ProjectPlans } from "@legion/contracts";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { bearerCredential } from "../../../packages/envoy-client/src/bearer";
import { executeDispatchTool } from "../../../packages/envoy-client/src/dispatch-execute";

// plan-ratings-read.sh (the read of a deployed issue's plan and ratings after the merge) against a
// fake `dispatch` on PATH: it logs its arguments, prints FAKE_DISPATCH_OUT as the real command's
// answer would print, and exits FAKE_DISPATCH_EXIT. The whole answer below is what `dispatch plans`
// (packages/envoy-client/src/dispatch-commands/plans.ts) prints of typed finished issues, rendered
// by that command itself, so a line the command prints and the script cannot read fails here: a
// header, then one finished issue per line, newest close first. Bun drops the answer's types when
// it runs this file, so packages/envoy-client's typecheck, whose tsconfig includes it, holds the
// answer to the contract's types.
const script = join(import.meta.dir, "..", "plan-ratings-read.sh");
const dir = temporaryDirectory("plan-ratings-read-test");

const project = "INFRA";
const key = "INFRA-42";
const defaultSteps = ["planner", "implementer", "tester", "reviewer", "acceptance:owner"];
const plan = `plan default (1 tasks: ${defaultSteps.join(" ")})`;
const owner = "plan owner (2 tasks: implementer reviewer)";

/** The plan of issue's first run: its owner's, of these tasks' steps, or the default one. */
function planOf(issue: string, tasks?: string[][]): PlanRecord {
  const id = `${issue}-plan`;
  return tasks === undefined
    ? {
        id,
        issue,
        generation: 1,
        source: "default",
        artifact_id: null,
        version: null,
        tasks: [{ id: "default", steps: defaultSteps }],
      }
    : {
        id,
        issue,
        generation: 1,
        source: "owner",
        artifact_id: `${issue}-plan-document`,
        version: 2,
        tasks: tasks.map((steps, place) => ({ id: `task-${place + 1}`, steps })),
      };
}

/**
 * A finished issue as Dispatch lists it: what the script reads of it, and the rest an answer
 * Dispatch can give. Its records end a minute before its close and start seconds before that
 * (duration_seconds is the whole seconds from started_at to ended_at, as Dispatch reckons it), and
 * each retro ends inside that span, a minute after the one before. An issue of unknown time
 * (seconds null) has no start and no end.
 */
function finished(
  issue: string,
  closed: string,
  plan: PlanRecord | null,
  ratings: [number, string][],
  seconds: number | null,
  failed: number
): FinishedIssue {
  const end = Date.parse(closed) - 60_000;
  const start = seconds === null ? null : end - seconds * 1000;
  const at = (moment: number) => new Date(moment).toISOString();
  return {
    issue,
    title: `The work of ${issue}`,
    closed_at: closed,
    plan,
    ratings: ratings.map(([rating, reason], place) => ({
      rating,
      reason,
      step_id: `${issue}-retro-${place + 1}`,
      ended_at: start === null ? null : at(start + (place + 1) * 60_000),
    })),
    started_at: start === null ? null : at(start),
    ended_at: start === null ? null : at(end),
    duration_seconds: seconds,
    failed_steps: failed,
    experiment: null,
    variant: null,
  };
}

// A whole answer: an issue its owner planned in two tasks, rated once; an issue rated twice whose
// second reason holds a quotation mark and a line break; the issue rated once; an issue whose
// owner's plan has a first task with no steps, which `dispatch plans` prints as `-`; and an issue
// finished before the release that rates each plan, which lists no plan, no rating and no time.
const answer: ProjectPlans = {
  project,
  issues: [
    finished(
      "INFRA-44",
      "2026-11-20T13:00:00Z",
      planOf("INFRA-44", [["implementer", "reviewer"], ["implementer:docs"]]),
      [[3, "Two tasks were one too many."]],
      3720,
      0
    ),
    finished(
      "INFRA-43",
      "2026-11-20T12:00:00Z",
      planOf("INFRA-43"),
      [
        [2, "The plan had no test step."],
        [5, 'It said "ship it"\nand it shipped.'],
      ],
      3300,
      0
    ),
    finished(
      key,
      "2026-11-20T11:20:00.123456Z",
      planOf(key),
      [[4, "The plan fit the work; the review sent it back once."]],
      8400,
      1
    ),
    finished("INFRA-30", "2026-11-19T10:00:00Z", planOf("INFRA-30", [[]]), [], 300, 0),
    finished("INFRA-7", "2026-11-01T08:00:00Z", null, [], null, 0),
  ],
};

/** The whole answer's lines as `dispatch plans` printed them, and the line of the issue judged. */
let whole: string[] = [];
let line42 = "";
beforeAll(async () => {
  writeFileSync(
    join(dir, "dispatch"),
    `#!/usr/bin/env bash
printf '%s\\n' "$*" >>"$FAKE_DISPATCH_LOG"
printf '%s' "$FAKE_DISPATCH_OUT"
exit "\${FAKE_DISPATCH_EXIT:-0}"
`,
    { mode: 0o755 }
  );
  const rendered = await executeDispatchTool({
    tool: "dispatch_plans",
    args: { project },
    cwd: dir,
    host: "omp",
    sessionId: "session-1",
    config: { url: "http://dispatch.test", credential: bearerCredential("secret") },
    env: {},
    exec: async (file, argv) => {
      throw new Error(`unexpected command: ${file} ${argv.join(" ")}`);
    },
    fetchImpl: (async () => Response.json(answer)) as unknown as typeof fetch,
  });
  whole = rendered.text.split("\n");
  line42 = whole.find((line) => line.startsWith(`${key} `)) ?? "";
});

/**
 * lines as the fake `dispatch` prints them, one per line and newline-ended: the whole answer by
 * default.
 */
function printed(lines: string[] = whole): string {
  return `${lines.join("\n")}\n`;
}

// changed is whole with the line that matches pick replaced by its edit.
function changed(pick: RegExp, edit: (line: string) => string): string {
  return printed(whole.map((line) => (pick.test(line) ? edit(line) : line)));
}

let runs = 0;
function read(out: string, args: string[] = [project, key], exit = 0) {
  const log = join(dir, `calls-${++runs}`);
  writeFileSync(log, "");
  const result = Bun.spawnSync(["bash", script, ...args], {
    env: {
      PATH: `${dir}:/usr/bin:/bin`,
      HOME: dir,
      FAKE_DISPATCH_LOG: log,
      FAKE_DISPATCH_OUT: out,
      FAKE_DISPATCH_EXIT: String(exit),
    },
  });
  return {
    code: result.exitCode,
    stdout: result.stdout.toString(),
    stderr: result.stderr.toString(),
    calls: readFileSync(log, "utf8"),
  };
}

describe("plan-ratings-read.sh", () => {
  test("a whole answer passes, exit 0, read once with dispatch plans --project, naming the rating and its reason", () => {
    const { code, stdout, calls } = read(printed());
    expect(calls).toBe(`plans --project ${project}\n`);
    expect(stdout).toContain(whole[0]);
    expect(stdout).toContain(
      `PASS: ${key}: plan default (1 tasks: planner implementer tester reviewer acceptance:owner), ratings 4/5 "The plan fit the work; the review sent it back once.", time 2h20m, failed steps 1`
    );
    expect(code).toBe(0);
  });

  test("an issue rated twice, a reason with a quotation mark and a line break among them, passes", () => {
    const { code, stdout } = read(printed(), [project, "INFRA-43"]);
    expect(stdout).toContain(
      `PASS: INFRA-43: ${plan}, ratings 2/5 "The plan had no test step.", 5/5 "It said \\"ship it\\"\\nand it shipped.", time 0h55m, failed steps 0`
    );
    expect(code).toBe(0);
  });

  test("an issue its owner planned passes, exit 0, naming the owner's plan, among a plan whose first task has no steps", () => {
    const { code, stdout } = read(printed(), [project, "INFRA-44"]);
    expect(stdout).toContain(
      `PASS: INFRA-44: ${owner}, ratings 3/5 "Two tasks were one too many.", time 1h2m, failed steps 0`
    );
    expect(code).toBe(0);
  });

  test("an issue with no plan and no rating fails, exit 1, one line per fault, and its unknown time is none of them", () => {
    const { code, stdout } = read(printed(), [project, "INFRA-7"]);
    expect(stdout).toContain("FAIL: INFRA-7 lists no plan: INFRA-7 closed");
    expect(stdout).toContain("FAIL: INFRA-7 lists no rating: INFRA-7 closed");
    expect(stdout).not.toContain("lists no time");
    expect(stdout).not.toContain("PASS");
    expect(code).toBe(1);
  });

  test("a plan with no rating fails, exit 1, naming it", () => {
    const { code, stdout } = read(
      changed(/^INFRA-42 /, (line) => line.replace(/ratings .* time/, "ratings none time"))
    );
    expect(stdout).toContain(`FAIL: ${key} lists no rating: ${key} closed`);
    expect(stdout).not.toContain("lists no plan");
    expect(stdout).not.toContain("PASS");
    expect(code).toBe(1);
  });

  test("a rating with an empty reason fails, exit 1, naming it", () => {
    const { code, stdout } = read(
      changed(/^INFRA-42 /, (line) => line.replace(/4\/5 "[^"]*"/, '4/5 ""'))
    );
    expect(stdout).toContain(`FAIL: ${key} lists a rating with no reason: ${key} closed`);
    expect(code).toBe(1);
  });

  // `dispatch plans` prints `time -` for an issue whose time Dispatch does not know: no record has
  // a start, none has an end, or the latest end is before the earliest start (a run whose start
  // never reached Dispatch, and a later one whose end has not yet). That says nothing of the plan
  // or its ratings, so the issue is judged by those, and the verdict says its time is unknown.
  test("a plan and a rating whose time Dispatch does not know pass, exit 0, saying the time is unknown", () => {
    const { code, stdout } = read(
      changed(/^INFRA-42 /, (line) => line.replace("time 2h20m", "time -"))
    );
    expect(stdout).toContain(
      `PASS: ${key}: ${plan}, ratings 4/5 "The plan fit the work; the review sent it back once.", time unknown, failed steps 1`
    );
    expect(stdout).not.toContain("FAIL");
    expect(code).toBe(0);
  });

  test("a read that lists finished issues and not the issue fails, exit 1, naming it", () => {
    const lines = whole.slice(1).filter((line) => !line.startsWith(`${key} `));
    const { code, stdout } = read(
      printed([`${project}: ${lines.length} finished issues`, ...lines])
    );
    expect(stdout).toContain(
      `FAIL: the read lists 4 finished issues of ${project} and no line of ${key}`
    );
    expect(stdout).not.toContain("PASS");
    expect(code).toBe(1);
  });

  test("a read that lists the issue twice, its header counting both, fails, exit 1, naming it", () => {
    const lines = [...whole.slice(1), line42];
    const { code, stdout } = read(
      printed([`${project}: ${lines.length} finished issues`, ...lines])
    );
    expect(stdout).not.toContain("UNKNOWN");
    expect(stdout).toContain(`FAIL: the read lists ${key} 2 times`);
    expect(stdout).not.toContain("PASS");
    expect(code).toBe(1);
  });

  test("a read that lists no finished issue at all is UNKNOWN, exit 2, never a pass", () => {
    const { code, stdout } = read(`${project}: 0 finished issues\n`);
    expect(stdout).toContain(`UNKNOWN: dispatch plans lists no finished issue of ${project}`);
    expect(stdout).not.toContain("PASS");
    expect(code).toBe(2);
  });

  test("a read that fails is UNKNOWN, exit 2, with what the command said", () => {
    const { code, stdout } = read(
      "Dispatch at https://dispatch.example is unreachable: ECONNREFUSED\n",
      [project, key],
      1
    );
    expect(stdout).toContain(
      `UNKNOWN: dispatch plans --project ${project} exited 1, so nothing was read`
    );
    expect(stdout).toContain("is unreachable: ECONNREFUSED");
    expect(code).toBe(2);
  });

  test("an answer that is no read of the project is UNKNOWN, exit 2", () => {
    const header = read(printed(["OTHER: 3 finished issues", ...whole.slice(1)]));
    expect(header.stdout).toContain(
      `UNKNOWN: the first line of the answer is no header of ${project}`
    );
    expect(header.code).toBe(2);
    const short = read(printed([`${project}: 6 finished issues`, ...whole.slice(1)]));
    expect(short.stdout).toContain(
      "UNKNOWN: the header says 6 finished issues and 5 lines of issues follow it"
    );
    expect(short.code).toBe(2);
    const torn = read(
      changed(/^INFRA-43 /, (line) => line.replace(/, 5\/5 .*" time/, ', 5/5 "torn time'))
    );
    expect(torn.stdout).toContain("UNKNOWN: the line is no finished issue: INFRA-43 closed");
    expect(torn.stdout).not.toContain("PASS");
    expect(torn.code).toBe(2);
  });

  test("a wrong count of arguments, a malformed key or an issue of another project is refused, exit 2, reading nothing", () => {
    for (const args of [
      [],
      [project],
      [project, key, "INFRA-43"],
      ["infra", key],
      [project, "infra-42"],
      [project, "OTHER-42"],
    ]) {
      const { code, stderr, calls } = read(printed(), args);
      expect(stderr).toContain("usage: scripts/e2e/plan-ratings-read.sh <PROJECT> <ISSUE>");
      expect(calls).toBe("");
      expect(code).toBe(2);
    }
  });
});
