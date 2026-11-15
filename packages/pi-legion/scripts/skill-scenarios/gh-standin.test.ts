import { afterEach, expect, test } from "bun:test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

const standin = path.join(import.meta.dir, "gh-standin.ts");
const runDirectory = temporaryDirectories("skill-scenarios-gh-standin");
const stops: Array<() => void> = [];
afterEach(() => {
  for (const stop of stops.splice(0)) stop();
});

/** The fields this file reads of a call the stand-in recorded to calls.jsonl. */
interface Call {
  argv: string[];
  route: string | null;
  exit: number;
  published?: { status: number; body: string } | { error: string };
}

/** A run directory of a test's own, holding the routes the stand-in answers from. The environment
 * it reads is the world's own, so a row that publishes names its listener and its token file after
 * the directory exists. Each call is spawned rather than run in this process: the stand-in is a
 * process per call, and a test serving its publish needs its own loop free while the call runs. */
function world(routes: unknown[]) {
  const run = runDirectory();
  const env: Record<string, string> = { SKILL_SCENARIO_RUN: run };
  writeFileSync(path.join(run, "fixtures.json"), JSON.stringify({ routes }));
  /** One run of the stand-in with `argv`: a `gh` call, or the rig's `--check` of the file. */
  async function standinRun(...argv: string[]) {
    const child = Bun.spawn(["bun", standin, ...argv], {
      env: { ...process.env, ...env },
      stdout: "pipe",
      stderr: "pipe",
    });
    const [stdout, stderr, exit] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ]);
    return { stdout, stderr, exit };
  }
  return {
    run,
    env,
    /** One `gh` call through the stand-in, as the scenario's PATH makes it. */
    gh: (...args: string[]) => standinRun("gh", ...args),
    /** The parse rig.sh's worker_world runs over fixtures.json before any agent boots. */
    check: () => standinRun("--check"),
    /** Whether the stand-in recorded any call at all. */
    recorded: () => existsSync(path.join(run, "calls.jsonl")),
    calls(): Call[] {
      return readFileSync(path.join(run, "calls.jsonl"), "utf8")
        .split("\n")
        .filter((line) => line !== "")
        .map((line) => JSON.parse(line) as Call);
    },
    fixtures(): { routes: { match: string; answered?: number }[] } {
      return JSON.parse(readFileSync(path.join(run, "fixtures.json"), "utf8"));
    },
  };
}

/** A listener of the test's own: it records every request that reached it and answers `status`
 * with `body`. */
function listener(status = 200, body: unknown = { ok: true }) {
  const received: {
    method: string;
    pathname: string;
    authorization: string | null;
    body: unknown;
  }[] = [];
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: async (request) => {
      received.push({
        method: request.method,
        pathname: new URL(request.url).pathname,
        authorization: request.headers.get("authorization"),
        body: await request.json(),
      });
      return Response.json(body, { status });
    },
  });
  const stop = () => server.stop(true);
  stops.push(stop);
  return { url: `http://127.0.0.1:${server.port}`, received, stop };
}

/** The token file the stand-in reads the bearer from, as rig.sh's services write it. */
function tokenFile(run: string, token: string): string {
  const file = path.join(run, "envoy-token");
  writeFileSync(file, `${token}\n`);
  return file;
}

/** The head's checks before a re-run and after it: the one row a re-run world turns on. */
const RED = { stdout: "test\tfail\t20s\thttps://example.invalid/2", exit: 1 };
const GREEN = { stdout: "test\tpass\t20s\thttps://example.invalid/2" };
const RERUN = "✓ Requested rerun of failed jobs";

const HEAD = "9f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3";
/** The settlement a re-run's route publishes: a cistore.Summary of the green head. */
const SUMMARY = {
  kind: "checks",
  repo: "example/widgets",
  number: "1000",
  sha: HEAD,
  check_runs: [
    { name: "lint", id: 920_001 },
    { name: "test", id: 920_002 },
  ],
  generation: 2,
  snapshot: "f3a1c0d2",
  failed: { count: 0, checks: [] },
  running: { count: 0, checks: [] },
  passed: { count: 2, checks: ["lint", "test"] },
  queued: { count: 0, checks: [] },
  cancelled: { count: 0, checks: [] },
  skipped: { count: 0, checks: [] },
  failing_checks: [],
};
/** The envelope a fixture writes for that settlement: every field of it is the fixture's own. */
const ENVELOPE = {
  topic: "notifications.github.example.widgets.pr.1000.checks",
  source: "github",
  message: `checks settled on example/widgets#1000 @ ${HEAD.slice(0, 7)}: 2 passed, 0 failed, 0 cancelled, 0 skipped`,
  payload: JSON.stringify(SUMMARY),
};

test("a route's replies answer the calls matching it in order", async () => {
  const { gh } = world([{ match: "^gh pr checks", replies: [RED, GREEN] }]);

  const red = await gh("pr", "checks");
  const green = await gh("pr", "checks");

  expect([red.stdout, red.exit]).toEqual([`${RED.stdout}\n`, 1]);
  expect([green.stdout, green.exit]).toEqual([`${GREEN.stdout}\n`, 0]);
});

test("a call past the end of a route's replies takes the last reply again", async () => {
  const { gh, calls, fixtures } = world([{ match: "^gh pr checks", replies: [RED, GREEN] }]);

  await gh("pr", "checks");
  await gh("pr", "checks");
  const third = await gh("pr", "checks");

  expect([third.stdout, third.exit]).toEqual([`${GREEN.stdout}\n`, 0]);
  expect(calls().map((call) => call.exit)).toEqual([1, 0, 0]);
  // The stand-in is a fresh process per call, so the count of calls the route has answered is the
  // fixture file's.
  expect(fixtures().routes[0]?.answered).toBe(3);
});

test("a route that follows another advances on that route's calls, never on its own", async () => {
  const { gh, fixtures } = world([
    { match: "^gh pr checks", replies: [RED, GREEN], follows: "^gh run rerun" },
    { match: "^gh pr view", replies: [RED, GREEN] },
    { match: "^gh run rerun", stdout: RERUN },
  ]);

  const read = [await gh("pr", "checks"), await gh("pr", "checks"), await gh("pr", "checks")];
  // The route beside it follows nothing, so it advances on its own calls, as every route did.
  const own = [await gh("pr", "view"), await gh("pr", "view")];
  await gh("run", "rerun", "17420", "--failed");
  const pressed = await gh("pr", "checks");

  expect(read.map((call) => [call.stdout, call.exit])).toEqual([
    [`${RED.stdout}\n`, 1],
    [`${RED.stdout}\n`, 1],
    [`${RED.stdout}\n`, 1],
  ]);
  expect(own.map((call) => call.stdout)).toEqual([`${RED.stdout}\n`, `${GREEN.stdout}\n`]);
  expect([pressed.stdout, pressed.exit]).toEqual([`${GREEN.stdout}\n`, 0]);
  // The sequence is read from `answered` of the route it follows, never from its own, so the four
  // calls of the route that follows count nothing: nothing reads that count, and a write of it
  // beside the re-run could put back the re-run's count as it stood before the re-run.
  expect(fixtures().routes.map((route) => route.answered)).toEqual([undefined, 2, 1]);
});

/** How many times each row below runs its calls at once: the lost update they pin was measured
 * at 6 of 20 trials of one re-run beside three reads of the head. */
const TRIALS = 20;

test("reads of the head made beside the re-run never lose the re-run's count", async () => {
  const seen: unknown[] = [];
  for (let trial = 0; trial < TRIALS; trial += 1) {
    const { gh, fixtures } = world([
      { match: "^gh pr checks", replies: [RED, GREEN], follows: "^gh run rerun" },
      { match: "^gh run rerun", stdout: RERUN },
    ]);

    await Promise.all([
      gh("run", "rerun", "17420", "--failed"),
      gh("pr", "checks"),
      gh("pr", "checks"),
      gh("pr", "checks"),
    ]);
    const after = await gh("pr", "checks");

    seen.push({ rerun: fixtures().routes[1]?.answered, after: [after.stdout, after.exit] });
  }
  expect(seen).toEqual(
    Array.from({ length: TRIALS }, () => ({ rerun: 1, after: [`${GREEN.stdout}\n`, 0] }))
  );
}, 60_000);

test("calls of one counted route made at once each take a reply of their own and each count", async () => {
  const calls = 6;
  const seen: unknown[] = [];
  for (let trial = 0; trial < TRIALS; trial += 1) {
    const { gh, fixtures } = world([{ match: "^gh pr view", replies: [RED, GREEN] }]);

    const answers = await Promise.all(Array.from({ length: calls }, () => gh("pr", "view")));

    seen.push({
      red: answers.filter((answer) => answer.exit === 1).length,
      answered: fixtures().routes[0]?.answered,
    });
  }
  // One call takes the first reply and every other one the last, as one call after another would.
  expect(seen).toEqual(Array.from({ length: TRIALS }, () => ({ red: 1, answered: calls })));
}, 60_000);

test("a follows that names no route's match is refused, naming it, and answers no call", async () => {
  const typo = world([
    { match: "^gh pr checks", replies: [RED, GREEN], follows: "^gh run rerunn" },
    { match: "^gh run rerun", stdout: RERUN },
  ]);
  const refusal =
    `gh-standin: ${typo.run}/fixtures.json is refused:\n` +
    `✖ route "^gh pr checks" follows "^gh run rerunn", which is no route's match\n` +
    "  → at routes[0].follows\n";

  const read = await typo.gh("pr", "checks");
  const rerun = await typo.gh("run", "rerun", "17420", "--failed");
  const checked = await typo.check();

  expect(read).toEqual({ stdout: "", stderr: refusal, exit: 2 });
  expect(rerun).toEqual({ stdout: "", stderr: refusal, exit: 2 });
  expect(checked).toEqual({ stdout: "", stderr: refusal, exit: 2 });
  expect(typo.recorded()).toBe(false);

  // The same file with the follows spelled as the route's match: the check passes and records
  // nothing, and the read answers.
  const spelled = world([
    { match: "^gh pr checks", replies: [RED, GREEN], follows: "^gh run rerun" },
    { match: "^gh run rerun", stdout: RERUN },
  ]);
  expect(await spelled.check()).toEqual({ stdout: "", stderr: "", exit: 0 });
  expect(spelled.recorded()).toBe(false);
  expect((await spelled.gh("pr", "checks")).exit).toBe(1);
});

test("a spent route falls through to the next route that matches, and to the 404 when none does", async () => {
  const { gh, calls } = world([
    { match: "^gh run rerun \\d+ --failed", stdout: RERUN, spends: 1 },
    { match: "^gh run rerun", stderr: "gh: the head's re-run is spent\n", exit: 1 },
  ]);

  const first = await gh("run", "rerun", "17420", "--failed");
  const second = await gh("run", "rerun", "17420", "--failed");

  expect(first.stdout).toBe(`${RERUN}\n`);
  expect([second.stderr, second.exit]).toEqual(["gh: the head's re-run is spent\n", 1]);
  expect(calls().map((call) => call.route)).toEqual([
    "^gh run rerun \\d+ --failed",
    "^gh run rerun",
  ]);

  const alone = world([{ match: "^gh pr checks", stdout: GREEN.stdout, spends: 1 }]);
  expect((await alone.gh("pr", "checks")).stdout).toBe(`${GREEN.stdout}\n`);
  const spent = await alone.gh("pr", "checks");

  expect([spent.stderr, spent.exit]).toEqual(["gh: Not Found (HTTP 404)\n", 1]);
  expect(alone.calls().map((call) => call.route)).toEqual(["^gh pr checks", null]);
});

test("the re-run and the failed-log read are each recorded with the route that answered them", async () => {
  const log = "--- FAIL: TestSettlesTheHead (0.12s)";
  const { gh, calls } = world([
    { match: "^gh run rerun \\d+ --failed", stdout: RERUN },
    { match: "^gh run view \\d+ --log-failed", stdout: log },
  ]);

  await gh("run", "rerun", "17420", "--failed");
  await gh("run", "view", "17420", "--log-failed");

  expect(
    calls().map((call) => ({ argv: call.argv.join(" "), route: call.route, exit: call.exit }))
  ).toEqual([
    { argv: "run rerun 17420 --failed", route: "^gh run rerun \\d+ --failed", exit: 0 },
    { argv: "run view 17420 --log-failed", route: "^gh run view \\d+ --log-failed", exit: 0 },
  ]);
});

test("a route's publish posts the envelope the fixture wrote, with the bearer, to the listener", async () => {
  const envoy = listener();
  const { env, gh, calls, run } = world([
    { match: "^gh run rerun \\d+ --failed", stdout: RERUN, publish: ENVELOPE },
  ]);
  env.ENVOY_URL = envoy.url;
  env.ENVOY_TOKEN_FILE = tokenFile(run, "rig-listener-bearer");

  const result = await gh("run", "rerun", "17420", "--failed");

  expect([result.stdout, result.exit]).toEqual([`${RERUN}\n`, 0]);
  expect(envoy.received).toEqual([
    {
      method: "POST",
      pathname: "/v1/messages/publish",
      authorization: "Bearer rig-listener-bearer",
      body: ENVELOPE,
    },
  ]);
  expect(calls()[0]?.published).toEqual({ status: 200, body: JSON.stringify({ ok: true }) });
});

test("a publish the listener refuses, or that reaches nothing, leaves the call's exit status alone", async () => {
  const refusing = listener(400, { error: "message is required", expected: ["message"] });
  const refused = world([{ match: "^gh run rerun", stdout: RERUN, publish: ENVELOPE }]);
  refused.env.ENVOY_URL = refusing.url;
  refused.env.ENVOY_TOKEN_FILE = tokenFile(refused.run, "rig-listener-bearer");

  const answered = await refused.gh("run", "rerun", "17420", "--failed");

  expect([answered.stdout, answered.exit]).toEqual([`${RERUN}\n`, 0]);
  expect(refused.calls()[0]?.published).toEqual({
    status: 400,
    body: JSON.stringify({ error: "message is required", expected: ["message"] }),
  });

  // A listener that is gone: the publish fails outright, and the call still exits as its route says.
  const unreachable = world([{ match: "^gh run rerun", stdout: "", exit: 2, publish: ENVELOPE }]);
  // The dead address rig.sh's own probe uses: a listener bound and stopped here would be stopped
  // twice by the afterEach, and its freed port could be taken by another process mid-test.
  unreachable.env.ENVOY_URL = "http://127.0.0.1:1";
  unreachable.env.ENVOY_TOKEN_FILE = tokenFile(unreachable.run, "rig-listener-bearer");

  expect((await unreachable.gh("run", "rerun", "17420", "--failed")).exit).toBe(2);
  expect(unreachable.calls()[0]?.published).toHaveProperty("error");
});
