/** What `e2e/tools/lane-cost.ts` reads out of a run's jobs with no read of GitHub: one caller's
 *  lane, its cost and its per-job seconds, and the refusal of a run whose jobs are of both of
 *  ci.yaml's callers of dispatch-browser.yaml, which a pull request labelled full-ci runs in one
 *  run. */
import { expect, test } from "bun:test";

import { type Job, laneReading, Refusal, type WorkflowRun } from "../tools/lane-cost";

const run: WorkflowRun = {
  id: 45276453365,
  event: "pull_request",
  conclusion: "success",
  head_sha: "77aaf3f3e4",
  run_attempt: 1,
  updated_at: "2026-11-23T22:12:00Z",
};

/** A job of `caller` that concluded success, named `<caller> / <leg>` as GitHub names a called
 *  workflow's job, with its three stamps. */
function job(
  id: number,
  caller: string,
  leg: string,
  [created, started, completed]: readonly [string, string, string],
  runner = "ubuntu-24.04"
): Job {
  return {
    id,
    name: `${caller} / ${leg}`,
    conclusion: "success",
    created_at: `2026-11-23T${created}Z`,
    started_at: `2026-11-23T${started}Z`,
    completed_at: `2026-11-23T${completed}Z`,
    labels: [runner],
  };
}

/** The four jobs one caller's lane runs on the pull-request lane, with ids from `first`:
 *  binaries 120 s, shard (1) 480 s, shard (2) 420 s and merge 30 s, queued 5, 4, 6 and 4 s. */
function lane(caller: string, first: number): Job[] {
  return [
    job(first, caller, "binaries", ["22:00:00", "22:00:05", "22:02:05"]),
    job(first + 1, caller, "shard (1)", ["22:02:06", "22:02:10", "22:10:10"]),
    job(first + 2, caller, "shard (2)", ["22:02:06", "22:02:12", "22:09:12"]),
    job(first + 3, caller, "merge", ["22:10:11", "22:10:15", "22:10:45"], "ubuntu-24.04-arm"),
  ];
}

test("the jobs of one caller read as one lane, each under its name after the caller's", () => {
  // The full lane's caller, skipped on an unlabelled pull request, is listed as one job under
  // its own id; a skipped job is no job of the reading, so it is no second caller either.
  const skippedCaller: Job = {
    id: 9,
    name: "dispatch-browser",
    conclusion: "skipped",
    created_at: "2026-11-23T22:00:00Z",
    started_at: "2026-11-23T22:00:00Z",
    completed_at: "2026-11-23T21:59:59Z",
  };
  expect(laneReading(run, [skippedCaller, ...lane("dispatch-browser-pr", 10)])).toEqual({
    run: "45276453365",
    event: "pull_request",
    head: "77aaf3f3e4",
    attempt: 1,
    conclusion: "success",
    updated_at: "2026-11-23T22:12:00Z",
    jobs: 4,
    per_job: { binaries: 120, "shard (1)": 480, "shard (2)": 420, merge: 30 },
    all_success: true,
    // 120 + 480 + 420 + 30.
    cost: 1050,
    // The binaries job, the slower shard leg and the merge job: 120 + 480 + 30.
    own: 630,
    // 22:00:05Z to 22:10:45Z.
    wall: 640,
    // 5 + 4 + 6 + 4.
    queue: 19,
    merge_job: 13,
    merge_runner: "ubuntu-24.04-arm",
    merge_seconds: 30,
  });
});

test("a run whose jobs are of both callers is refused, naming them, and summed as no lane", () => {
  const both = [...lane("dispatch-browser", 10), ...lane("dispatch-browser-pr", 20)];
  expect(() => laneReading(run, both)).toThrow(Refusal);
  expect(() => laneReading(run, both)).toThrow(
    "run 45276453365: its jobs are of 2 callers of dispatch-browser.yaml, dispatch-browser and " +
      "dispatch-browser-pr, and a reading reads one lane"
  );
});
