import { describe, expect, test } from "bun:test";
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import { dirname, join, relative } from "node:path";
import { skipWithout } from "@legion/test-fixtures/host-tools";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import {
  a4,
  answers,
  driverTest,
  failed,
  fullPlan,
  off,
  passed,
  plan,
  retired,
  runsOf,
  script,
} from "./approved-version-news-fixture";
import {
  inOwnGroup,
  type KillPoint,
  liveDaemonStandIns,
  type StopSignal,
  stopped,
} from "./live-daemon-proof-fixture";
import { scriptAssignments, sourcedLibraries } from "./script-functions";

// approved-version-news.sh's runs of part one and of both parts, driven whole over
// lib/live-daemon-proof-fixture.ts's stand-ins, so the script's own end mark is the one under
// test: its run_end, its finish and the real lib/run-verdict.sh print the last line.
// approved-version-news-verdicts.test.ts runs the script's controls, its plan check and its last
// line by name.

const assignment = scriptAssignments(script);
const scratch = temporaryDirectories("approved-version-news-run-test");
const withoutSetsid = skipWithout("setsid");
const withoutGit = skipWithout("git", "tar");

// Part one's steps and the checks each makes, read from the script's own part_one_checks, so the
// stand-in driver below prints what the script counts.
const partOneChecks = assignment("part_one_checks")
  .slice("part_one_checks=(".length, -")".length)
  .trim()
  .split(/\s+/)
  .map((entry) => {
    const [step, checks] = entry.split(":");
    return { step, checks: Number(checks) };
  });
const expectedChecks = partOneChecks.reduce((sum, { checks }) => sum + checks, 0);
// What part one's driver prints when every step held: each step's checks and its ok line.
const partOneDriver = [
  `=== RUN   ${driverTest}`,
  ...partOneChecks.flatMap(({ step, checks }) => [
    `== ${step}`,
    ...Array.from({ length: checks }, (_, n) => `   assert ${step}: check ${n + 1} held`),
    `ok ${step} (1s)`,
  ]),
  `--- PASS: ${driverTest} (8.00s)`,
  "PASS",
];

/** Runs the script with --part one over lib/live-daemon-proof-fixture.ts's stand-ins, stopped at
 * KILL when given, in a process group of its own. Every line, its stderr's included, is in `out`:
 * the script's transcript sends both there. */
function partOneRun(kill?: { at: KillPoint; signal: StopSignal }) {
  const dir = scratch();
  const env = liveDaemonStandIns(dir, { driverOutput: partOneDriver, kill });
  env.APPROVED_VERSION_NEWS_EVIDENCE_DIR = join(dir, "evidence");
  return inOwnGroup(["bash", script, "--part", "one"], env);
}

// The script's run and its two ends, driven whole: part one's run is lib/live-daemon-proof.sh's
// over stand-ins, and the script's own run_end, its own finish and the real lib print the last
// line. A run a signal ends in part one reads FAIL at part-one; moving the script's run_end above
// part one makes it print PASS. A signal to the group ends part one's subshell too, and the two
// shells print their FAIL lines in either order.
describe.skipIf(withoutSetsid)("the script's run of part one", () => {
  test("reads each step of part_one_checks and the checks it makes", () => {
    expect(partOneChecks.length).toBeGreaterThan(0);
    for (const { step, checks } of partOneChecks) {
      expect(step).toMatch(/^[a-z0-9-]+$/);
      expect(checks).toBeGreaterThan(0);
    }
  });

  for (const signal of ["TERM", "HUP", "INT"] as const) {
    test(`reads FAIL at part-one for a run a group SIG${signal} ended in part one's driver`, () => {
      const run = partOneRun({ at: "proof", signal });
      expect(run.out).toContain("approved version news part one e2e: FAIL at proof after ");
      expect(run.out).not.toContain("e2e: PASS");
      expect(run.out).not.toContain("control bites");
      expect(run.out).toMatch(
        new RegExp(
          `\\napproved version news e2e: FAIL at (part-one|assertions) after \\d+s \\(assertions: \\d+/${expectedChecks}; part one alone, part two did not run, evidence: .*\\)\\n`
        )
      );
      expect(run.code === 0 && run.signal === null).toBe(false);
    });
  }

  test("reads FAIL at part-one for a run a group SIGTERM ended in part one's setup", () => {
    const run = partOneRun({ at: "build", signal: "TERM" });
    expect(run.out).toContain("approved version news part one e2e: FAIL at driver after ");
    expect(run.out).not.toContain("e2e: PASS");
    expect(run.out).toContain(`${stopped}\n`);
    expect(run.out).toMatch(
      new RegExp(
        `\\napproved version news e2e: FAIL at part-one after \\d+s \\(assertions: 0/${expectedChecks}; part one alone, part two did not run, evidence: .*\\)\\n`
      )
    );
    expect(run.code === 143 || run.signal === "SIGTERM").toBe(true);
  });

  test("reads PASS for a run of part one that reached its end", () => {
    const run = partOneRun();
    expect(run.code).toBe(0);
    expect(run.out).toContain("approved version news part one e2e: PASS in ");
    expect(run.out).not.toContain("FAIL");
    expect(run.out).toMatch(
      new RegExp(
        `\\napproved version news e2e: PASS in \\d+s \\(assertions: ${expectedChecks}/${expectedChecks}; part one alone, part two did not run\\)\\n$`
      )
    );
  });
});

const sessionsTest = "TestRealSessionsTellAndAnswerANewVersionLive";
// The model key the run's key file holds: its first 8 characters are in nothing the run prints.
const modelKey = "Kq81mvXz-a-model-key-of-the-fixture";
// The seconds a token has left when the script's refresher mints the next one, read from the
// script, so the stand-in `curl` below mints tokens the refresher replaces a few seconds on.
const keepSeconds = Number(assignment("gateway_keep_seconds").split("=")[1]);
// The header of every identity token the stand-in `curl` mints, base64url as a JWT's is.
const tokenHeader = Buffer.from(
  JSON.stringify({ alg: "RS256", kid: "fixture-signing-key", typ: "JWT" })
).toString("base64url");
// An identity token the stand-in `curl` minted: its header, a payload and a signature naming the
// mint's number in the run.
const mintedToken = new RegExp(`^${tokenHeader}\\.[A-Za-z0-9_-]+\\.fixtureSignatureOfMint(\\d+)$`);

/**
 * Where a run of both parts takes part two's model key from (bothPartsRun's KEY): the key file
 * that holds `modelKey` unless `keyFile` is false, which names a file that does not exist; the
 * audience in APPROVED_VERSION_NEWS_GATEWAY_AUDIENCE when `audience` names one; and, with `leak`,
 * a stand-in driver that writes the key it was given into the daemon's log in the evidence
 * directory.
 */
type KeyRun = { keyFile?: boolean; audience?: string; leak?: boolean };

/**
 * Runs the script, both parts, with the variables PICK sets, over lib/live-daemon-proof-fixture.ts's
 * stand-ins, from a scratch checkout of its own: the script and the libraries it sources at the
 * paths they have here, in a git repository whose one commit holds the base's prompt files and is
 * origin/main, so that commit is the base. The stand-in driver prints part one's output for part
 * one's test; under -part-two-plan it prints PLANS.full when the run picks nothing and PLANS.pick
 * when it picks; and for part two's test it prints PART_TWO's run lines, the count of model calls
 * and the test's ENDED, a FAIL for a driver that stopped at a head run, with that run's status.
 * Part two's test also keeps in `seen/` of the run's directory what it was given as its key file:
 * the file's path, its mode and its text as the test starts, and, for a run that mints the pod's
 * identity token, its text once the script's refresher has written another token into it. The
 * stand-in `curl` answers the metadata server's identity route with a token whose `exp` claim is
 * `keepSeconds` and 4 seconds on, and lists each call's arguments in `curl-calls`. KEY says where
 * the run takes its model key from (KeyRun). Every line, its stderr's included, is in `out`, the
 * script's own last line is `last`, and `dir` is the run's directory.
 */
function bothPartsRun(
  pick: Record<string, string>,
  plans: { full: string; pick?: string },
  partTwo: string[],
  ended: "PASS" | "FAIL" = "PASS",
  key: KeyRun = {}
) {
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
  const env = liveDaemonStandIns(dir, { driverOutput: partOneDriver });
  for (const name of [
    "APPROVED_VERSION_NEWS_STEP",
    "APPROVED_VERSION_NEWS_ARM",
    "APPROVED_VERSION_NEWS_RUNS",
    "APPROVED_VERSION_NEWS_GATEWAY_AUDIENCE",
    "GIT_DIR",
    "GIT_WORK_TREE",
    "GIT_INDEX_FILE",
  ]) {
    delete env[name];
  }
  for (const args of [
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
        ...args,
      ],
      { env }
    );
    if (git.exitCode !== 0) throw new Error(`git ${args.join(" ")}: ${git.stderr.toString()}`);
  }
  writeFileSync(join(dir, "full-plan"), `${plans.full}\n`);
  writeFileSync(join(dir, "pick-plan"), `${plans.pick ?? plans.full}\n`);
  const output = [
    `=== RUN   ${sessionsTest}`,
    ...partTwo,
    `model calls: ${partTwo.length} in all, over ${partTwo.length} runs`,
    `--- ${ended}: ${sessionsTest} (100.00s)`,
    ended,
  ];
  writeFileSync(join(dir, "part-two-output"), output.map((line) => `${line}\n`).join(""));
  writeFileSync(
    join(dir, "driver"),
    `#!/usr/bin/env bash
case " $* " in
  *" -part-two-plan "*)
    if [ -n "\${APPROVED_VERSION_NEWS_STEP:-}\${APPROVED_VERSION_NEWS_ARM:-}\${APPROVED_VERSION_NEWS_RUNS:-}" ]; then
      cat "$LIVE_DAEMON_STAND_IN_DIR/pick-plan"
    else
      cat "$LIVE_DAEMON_STAND_IN_DIR/full-plan"
    fi
    echo PASS
    ;;
  *" ^${sessionsTest}$ "*)
    key=$APPROVED_VERSION_NEWS_MODEL_KEY_FILE
    seen=$LIVE_DAEMON_STAND_IN_DIR/seen
    mkdir -p "$seen"
    printf '%s\\n' "$key" >"$seen/path"
    stat -c %a "$key" >"$seen/mode"
    cp "$key" "$seen/first"
    if [ -n "\${STAND_IN_AWAIT_REFRESH:-}" ]; then
      for _ in $(seq 1 300); do
        [ "$(<"$key")" = "$(<"$seen/first")" ] || break
        sleep 0.1
      done
      cp "$key" "$seen/later"
    fi
    [ -z "\${STAND_IN_LEAK:-}" ] || cat "$key" >>"$LEGION_E2E_DAEMON_LOG"
    cat "$LIVE_DAEMON_STAND_IN_DIR/part-two-output"
    [ ${ended} = PASS ]
    ;;
  *) cat "$LIVE_DAEMON_STAND_IN_DIR/driver-output" ;;
esac
`
  );
  chmodSync(join(dir, "driver"), 0o755);
  writeFileSync(join(dir, "omp"), "#!/bin/sh\nexit 0\n");
  chmodSync(join(dir, "omp"), 0o755);
  writeFileSync(join(dir, "model-key"), `${modelKey}\n`);
  writeFileSync(
    join(dir, "bin", "curl"),
    `#!/usr/bin/env bash
printf '%s\\n' "$*" >>"$LIVE_DAEMON_STAND_IN_DIR/curl-calls"
n=$(($(cat "$LIVE_DAEMON_STAND_IN_DIR/minted" 2>/dev/null || echo 0) + 1))
echo "$n" >"$LIVE_DAEMON_STAND_IN_DIR/minted"
payload=$(printf '{"aud":"model-gateway-gce","exp":%d,"n":%d}' "$(($(date +%s) + ${keepSeconds + 4}))" "$n" |
  base64 -w0 | tr '+/' '-_' | tr -d '=')
printf '%s.%s.%s' '${tokenHeader}' "$payload" "fixtureSignatureOfMint$n"
`
  );
  chmodSync(join(dir, "bin", "curl"), 0o755);
  Object.assign(env, pick, {
    PATH: `${env.PATH}:${dirname(process.execPath)}`,
    LEGION_OMP_PATH: join(dir, "omp"),
    ANTHROPIC_BASE_URL: "http://127.0.0.1:1",
    APPROVED_VERSION_NEWS_MODEL_KEY_FILE: join(dir, key.keyFile === false ? "no-key" : "model-key"),
    APPROVED_VERSION_NEWS_EVIDENCE_DIR: join(dir, "evidence"),
  });
  if (key.audience !== undefined) env.APPROVED_VERSION_NEWS_GATEWAY_AUDIENCE = key.audience;
  // A run that mints the token waits in part two's test for the refresher's next one.
  if (key.keyFile === false && key.audience !== undefined) env.STAND_IN_AWAIT_REFRESH = "1";
  if (key.leak) env.STAND_IN_LEAK = "1";
  const result = Bun.spawnSync(
    ["bash", join(checkout, "scripts", "e2e", "approved-version-news.sh")],
    {
      env,
    }
  );
  const out = result.stdout.toString() + result.stderr.toString();
  return { code: result.exitCode, out, last: out.trimEnd().split("\n").at(-1) ?? "", dir };
}

const a4Head = retired[0].headOnly;
const answersHead = retired[1].headOnly;
const noControl = "(no control: the base arm passed every run that reached its assertion)";
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

// The script's run of both parts, driven whole over stand-ins: its plan check builds the one
// driver both parts run and reads the stand-in driver's plan, part one's and part two's runs are
// lib/live-daemon-proof.sh's, and the script's own finish and the real lib print the last line. A
// full run reads PASS. A run that sets any of
// APPROVED_VERSION_NEWS_ARM, APPROVED_VERSION_NEWS_STEP and APPROVED_VERSION_NEWS_RUNS is a
// measurement: its last line reads MEASURED, never PASS, names the pick, each step and arm of the
// full run's plan it left out and its count of runs where it is not three, and exits 0; a head run
// of the pick that did not pass still reads FAIL. The counts and the checks follow the driver's
// plan, and a pick the driver refuses is refused in its words.
describe.skipIf(withoutGit)("the script's run of both parts", () => {
  test("reads PASS for a full run, which picks nothing", () => {
    const run = bothPartsRun({}, { full: fullPlan }, [
      ...runsOf(a4, "head", passed, passed, passed),
      ...runsOf(off, "head", passed, passed, passed),
      ...runsOf(off, "base", failed, failed, failed),
      ...runsOf(answers, "head", passed, passed, passed),
    ]);
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: PASS in \d+s \(/);
    expect(run.last).toEndWith(
      `(assertions: ${expectedChecks}/${expectedChecks}; part two: a4 head 3/3 ${a4Head}; off head 3/3, base 0/3 (control bites); answers head 3/3 ${answersHead}; model calls: 12)`
    );
    // The plan check's build is the run's one: each part's driver check takes it and builds none.
    expect(run.out.match(/^ {3}built .+\.test from /gm)).toHaveLength(1);
    expect(run.out.match(/^ {3}the caller built .+; this check builds nothing$/gm)).toHaveLength(2);
  }, 60_000);

  test("reads MEASURED, not PASS, for APPROVED_VERSION_NEWS_ARM=base, naming each head arm it left out", () => {
    const run = bothPartsRun(
      { APPROVED_VERSION_NEWS_ARM: "base" },
      { full: fullPlan, pick: basePlan },
      [
        ...runsOf(a4, "base", passed, passed, passed),
        ...runsOf(off, "base", failed, failed, failed),
        ...runsOf(answers, "base", passed, passed, passed),
      ]
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: MEASURED in \d+s \(/);
    expect(run.last).toContain(
      `(assertions: ${expectedChecks}/${expectedChecks}; part two: a partial run picked by APPROVED_VERSION_NEWS_ARM=base, leaving out a4 head, off head, answers head: a4 base 3/3 ${noControl}; off base 0/3 (control bites); answers base 3/3 ${noControl}; model calls: 9, evidence: `
    );
  }, 60_000);

  test("reads MEASURED, not PASS, for APPROVED_VERSION_NEWS_STEP=a4, naming each step and arm it left out", () => {
    const run = bothPartsRun(
      { APPROVED_VERSION_NEWS_STEP: "a4" },
      { full: fullPlan, pick: plan([["a4", a4, "head"]], 3) },
      runsOf(a4, "head", passed, passed, passed)
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: MEASURED in \d+s \(/);
    expect(run.last).toContain(
      `(assertions: ${expectedChecks}/${expectedChecks}; part two: a partial run picked by APPROVED_VERSION_NEWS_STEP=a4, leaving out off head, off base, answers head: a4 head 3/3 ${a4Head}; model calls: 3, evidence: `
    );
  }, 60_000);

  test("reads MEASURED, not PASS, for APPROVED_VERSION_NEWS_RUNS=1, naming its count of runs", () => {
    const run = bothPartsRun(
      { APPROVED_VERSION_NEWS_RUNS: "1" },
      { full: fullPlan, pick: onePlan },
      [
        ...runsOf(a4, "head", passed),
        ...runsOf(off, "head", passed),
        ...runsOf(off, "base", failed),
        ...runsOf(answers, "head", passed),
      ]
    );
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: MEASURED in \d+s \(/);
    expect(run.last).toContain(
      `(assertions: ${expectedChecks}/${expectedChecks}; part two: a partial run picked by APPROVED_VERSION_NEWS_RUNS=1, with 1 run of each arm in place of 3: a4 head 1/1 ${a4Head}; off head 1/1, base 0/1 (control bites); answers head 1/1 ${answersHead}; model calls: 4, evidence: `
    );
  }, 60_000);

  test("reads FAIL for a pick whose head run did not pass", () => {
    const run = bothPartsRun(
      { APPROVED_VERSION_NEWS_STEP: "off" },
      { full: fullPlan, pick: plan([["off", off, "head base"]], 3) },
      runsOf(off, "head", passed, failed),
      "FAIL"
    );
    expect(run.code).toBe(1);
    expect(run.out).not.toContain("approved version news e2e: MEASURED");
    expect(run.last).toMatch(/^approved version news e2e: FAIL at part-two after \d+s \(/);
    expect(run.last).toContain(
      "part two: a partial run picked by APPROVED_VERSION_NEWS_STEP=off, leaving out a4 head, answers head: off head 1/2, base 0/0 (base did not run); model calls: 2, evidence: "
    );
  }, 60_000);

  // The script keeps no table of part two's steps: a step the driver's plan adds is counted in
  // each arm the plan makes it in, and a plan that gives a4 its base arm back, while head_only
  // still holds the measurement that retired it, fails the run at the plan check, naming a4.
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
    const run = bothPartsRun({}, { full: withExtra }, [
      ...runsOf(a4, "head", passed, passed, passed),
      ...runsOf(off, "head", passed, passed, passed),
      ...runsOf(off, "base", failed, failed, failed),
      ...runsOf(answers, "head", passed, passed, passed),
      ...runsOf(extra, "head", passed, passed, passed),
      ...runsOf(extra, "base", passed, failed, passed),
    ]);
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: PASS in \d+s \(/);
    expect(run.last).toContain(
      `; answers head 3/3 ${answersHead}; extra head 3/3, base 2/3 (control bites); model calls: 18)`
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
    const run = bothPartsRun({}, { full: back }, [
      ...runsOf(a4, "head", passed, passed, passed),
      ...runsOf(a4, "base", failed, failed, failed),
    ]);
    expect(run.code).toBe(1);
    expect(run.out).toContain(
      "FAIL plan: the driver's plan of a full run makes a4 in both arms (head base), and head_only holds a measurement that retired its base arm\n"
    );
    expect(run.last).toMatch(/^approved version news e2e: FAIL at plan after \d+s \(/);
  }, 60_000);

  test("refuses with exit 2, in the driver's words, a pick the driver refuses", () => {
    const refusal =
      'plan refused: APPROVED_VERSION_NEWS_STEP="a5" names no step of part two: it is one of a4, off, answers';
    const run = bothPartsRun(
      { APPROVED_VERSION_NEWS_STEP: "a5" },
      { full: fullPlan, pick: refusal },
      []
    );
    expect(run.code).toBe(2);
    expect(run.out).toContain(
      `FAIL plan: part two's driver refuses the pick: ${refusal.slice("plan refused: ".length)}\n`
    );
    expect(run.last).toMatch(/^approved version news e2e: FAIL at plan after \d+s \(/);
  }, 60_000);
});

// A full run of both parts again, for where part two takes its model key from. Where the key file
// holds a key, the run takes it, an audience named beside it or not, and mints nothing. Where it
// holds none and APPROVED_VERSION_NEWS_GATEWAY_AUDIENCE names an audience, the run mints the
// pod's identity token from the stand-in `curl` as from the metadata server, into a key file of
// its own scratch, mode 600, which the driver is pointed at, which the refresher writes a later
// token into while part two runs, and which the run removes; no token is printed, and the
// key-leak check reads each token's header. With neither, the run is refused before anything is
// built.
describe.skipIf(withoutGit)("part two's model key", () => {
  const fullRun = [
    ...runsOf(a4, "head", passed, passed, passed),
    ...runsOf(off, "head", passed, passed, passed),
    ...runsOf(off, "base", failed, failed, failed),
    ...runsOf(answers, "head", passed, passed, passed),
  ];
  const identityRoute =
    "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity?audience=model-gateway-gce&format=full";
  const seen = (dir: string, name: string) => readFileSync(join(dir, "seen", name), "utf8");

  test("is the key file's key where the file holds one, with an audience beside it, and nothing is minted", () => {
    const run = bothPartsRun({}, { full: fullPlan }, fullRun, "PASS", {
      audience: "model-gateway-gce",
    });
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: PASS in \d+s \(/);
    expect(seen(run.dir, "path")).toBe(`${join(run.dir, "model-key")}\n`);
    expect(seen(run.dir, "first")).toBe(`${modelKey}\n`);
    expect(existsSync(join(run.dir, "curl-calls"))).toBe(false);
    expect(run.out).toContain(
      `, model key file ${join(run.dir, "model-key")} (read by the driver, never printed)\n`
    );
    expect(run.out).not.toContain("== gateway-token");
    expect(run.out).toContain("   the key's first 8 characters occur 0 times in ");
  }, 60_000);

  test("is the pod's identity token where the key file holds none, minted again while part two runs", () => {
    const run = bothPartsRun({}, { full: fullPlan }, fullRun, "PASS", {
      keyFile: false,
      audience: "model-gateway-gce",
    });
    expect(run.code).toBe(0);
    expect(run.last).toMatch(/^approved version news e2e: PASS in \d+s \(/);
    const calls = readFileSync(join(run.dir, "curl-calls"), "utf8").trimEnd().split("\n");
    expect(calls.length).toBeGreaterThanOrEqual(2);
    for (const call of calls) {
      expect(call).toBe(`-sSf --retry 3 -H Metadata-Flavor: Google ${identityRoute}`);
    }
    // The driver is pointed at a key file of the run's own scratch, mode 600, which the run removes.
    const path = seen(run.dir, "path").trimEnd();
    expect(path).toStartWith(join(run.dir, "tmp", "legion-e2e-approved-version-news-gateway."));
    expect(seen(run.dir, "mode")).toBe("600\n");
    expect(existsSync(dirname(path))).toBe(false);
    // The file holds a minted token as part two's driver starts, and a later one while it runs.
    const first = seen(run.dir, "first").match(mintedToken);
    const later = seen(run.dir, "later").match(mintedToken);
    expect(first).not.toBeNull();
    expect(later).not.toBeNull();
    expect(Number(later?.[1])).toBeGreaterThan(Number(first?.[1]));
    expect(run.out).toContain("\n== gateway-token\n");
    expect(run.out).toMatch(
      /^ {3}the identity token for the audience model-gateway-gce was minted again at \S+ into \S+ \(not printed\), with \d+ seconds left$/m
    );
    expect(run.out).toContain(
      "   the headers of the identity tokens the run minted occur 0 times in "
    );
    expect(run.out).not.toContain(tokenHeader);
    expect(run.out).not.toContain("fixtureSignatureOfMint");
  }, 60_000);

  test("fails at key-leak where a token the run minted is in the evidence directory", () => {
    const run = bothPartsRun({}, { full: fullPlan }, fullRun, "PASS", {
      keyFile: false,
      audience: "model-gateway-gce",
      leak: true,
    });
    expect(run.code).toBe(1);
    expect(run.out).toContain(
      "FAIL key-leak: the header of an identity token the run minted is in the evidence directory 1 times\n"
    );
    expect(run.last).toMatch(/^approved version news e2e: FAIL at key-leak after \d+s \(/);
    expect(run.out).not.toContain("fixtureSignatureOfMint");
  }, 60_000);

  test("refuses with exit 2, before anything is built, a run with no key in the key file and no audience", () => {
    const run = bothPartsRun({}, { full: fullPlan }, fullRun, "PASS", { keyFile: false });
    expect(run.code).toBe(2);
    expect(run.out).toContain(
      `FAIL arguments: the model key file ${join(run.dir, "no-key")} is not a readable file with a key in it, and APPROVED_VERSION_NEWS_GATEWAY_AUDIENCE names no audience of the pod's identity token to run on in its place: `
    );
    expect(run.out).not.toContain("== plan");
    expect(existsSync(join(run.dir, "curl-calls"))).toBe(false);
    expect(run.last).toMatch(/^approved version news e2e: FAIL at arguments after \d+s \(/);
  }, 60_000);
});
