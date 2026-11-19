import { expect, setDefaultTimeout, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// Each row runs the stand-in as a bun process of its own, a start a loaded machine stretches: the
// file took 0.24 s at a load of about 350, and the rig's other process rows have passed 5 s at
// heavier loads, so each test and hook gets one minute, set before the hooks the reservations below
// register.
setDefaultTimeout(60_000);

// Each row's run directory, reserved here (the fixture's hook makes it before the rows run).
const runs = {
  broken: temporaryDirectory("gh-standin-broken", { real: true }),
  sound: temporaryDirectory("gh-standin-sound", { real: true }),
};
const review = "^gh api .*pulls/702/reviews";

/** Runs the stand-in as a run's `gh`, with one route that answers a review's post. */
function post(run: string, body: string) {
  mkdirSync(run, { recursive: true });
  writeFileSync(
    path.join(run, "fixtures.json"),
    JSON.stringify({ routes: [{ match: review, stdout: { id: 7002 } }] })
  );
  writeFileSync(path.join(run, "review.json"), body);
  const result = Bun.spawnSync(
    [
      "bun",
      path.join(import.meta.dir, "gh-standin.ts"),
      "gh",
      "api",
      "--method",
      "POST",
      "repos/example/widgets/pulls/702/reviews",
      "--input",
      path.join(run, "review.json"),
    ],
    { env: { ...process.env, SKILL_SCENARIO_RUN: run } }
  );
  const calls = readFileSync(path.join(run, "calls.jsonl"), "utf8").trim().split("\n");
  return {
    code: result.exitCode,
    stdout: result.stdout.toString(),
    stderr: result.stderr.toString(),
    call: JSON.parse(calls.at(-1) ?? "{}"),
  };
}

// Catches: a stand-in that answers a review whose JSON GitHub cannot parse as posted, so an agent
// that wrote its body with an unescaped quote believes it approved.
test("a body that is not JSON gets GitHub's 400, exits 1 and is recorded so", () => {
  const { code, stdout, stderr, call } = post(
    runs.broken,
    '{"event":"APPROVE","body":"Run `"+"`"+`widgets --help`"}'
  );
  expect({ code, stdout, stderr }).toEqual({
    code: 1,
    stdout: "",
    stderr: "gh: Problems parsing JSON (HTTP 400)\n",
  });
  expect(call.exit).toBe(1);
});

// Catches: a check that refuses every body, so no review ever posts.
test("a body that is JSON gets the route's answer", () => {
  const { code, stdout, call } = post(runs.sound, '{"event":"APPROVE","body":"ok"}');
  expect({ code, stdout }).toEqual({ code: 0, stdout: '{"id":7002}\n' });
  expect(call.route).toBe(review);
});
