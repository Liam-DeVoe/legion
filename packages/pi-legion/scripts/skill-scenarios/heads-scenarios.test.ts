// heads-scenarios.txt, the catalog of the heads scenarios' worlds, and the readers that take each
// world from it: rig.sh's batch and run, heads-world.ts's validation, seed.ts's usage and
// score.ts's run names.
//
//   bun test heads-scenarios.test.ts

import { expect, setDefaultTimeout, test } from "bun:test";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { spawnWithin } from "@legion/test-fixtures/row-clock";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { copyOfThisDirectory, rigFunctions } from "./catalog-copy";
import { HEADS_SCENARIOS, isHeadsScenario } from "./heads-scenarios";
import { fileRowLimit, RIG_STEP, SCRIPT_RUN } from "./test-limit";

// The reach row runs two rig.sh steps and two bun scripts as processes of their own.
setDefaultTimeout(fileRowLimit(RIG_STEP, RIG_STEP, SCRIPT_RUN, SCRIPT_RUN));

const makeDirectory = temporaryDirectories("heads-catalog");

test("the heads scenarios have one canonical membership", () => {
  expect(HEADS_SCENARIOS).toEqual([
    "reviewer-own-head",
    "reviewer-pass-and-fail",
    "architect-pass-and-fail",
    "ci-pass-and-fail",
  ]);
  expect(isHeadsScenario("reviewer-own-head")).toBe(true);
  expect(isHeadsScenario("measure-before-ask")).toBe(false);
});

// Catches: a reader that keeps a list of the worlds of its own, so a world added to the catalog
// alone is missing from rig.sh's batch, refused by its run, by heads-world.ts or by seed.ts's
// usage, or read by score.ts as another world's run. The added case's name begins another case's
// of its scenario (no-verdict, no-verdict-fail), so a pattern that tries the shorter name first
// reads no-verdict-fail's runs as no-verdict's.
test("a world added to the catalog alone reaches rig.sh, heads-world.ts, seed.ts and score.ts", () => {
  const copy = copyOfThisDirectory(makeDirectory("tree"));
  const tree = path.resolve(copy, "../../../..");
  appendFileSync(path.join(copy, "heads-scenarios.txt"), "architect-pass-and-fail no-verdict\n");
  const env = { PATH: process.env.PATH ?? "", HOME: makeDirectory("home") };
  const functions = rigFunctions(copy);
  const bash = (part: string, lines: string[], args: string[]) => {
    const ran = spawnWithin(
      { ...RIG_STEP, part },
      ["bash", "-c", [functions, ...lines].join("\n"), "-", ...args],
      { env: { ...env, SKILL_SCENARIOS_PARALLEL: "1" } }
    );
    return `exit=${ran.exitCode} ${ran.stderr.toString()}`;
  };

  // A batch of one run, one at a time (SKILL_SCENARIOS_PARALLEL=1), so its run lines keep the
  // order it hands them to `rig.sh run` in, which a stand-in rig.sh records.
  const here = makeDirectory("here");
  writeFileSync(path.join(here, "rig.sh"), `printf '%s\\n' "$*" >>"${here}/runs"\n`);
  const batch = bash(
    "rig.sh cmd_batch",
    [
      "load_label() { :; }",
      'lock_services() { S=$work/services; mkdir -p "$S"; }',
      "on_exit() { :; }",
      "services_up() { :; }",
      "date() { echo 1700000000; }",
      "go() { :; }",
      `here=$1 work=$2 root=${tree}`,
      "cmd_batch architect-pass-and-fail 1 head",
    ],
    [here, makeDirectory("batch")]
  );
  // A run of the added world, its run function stubbed.
  const work = makeDirectory("run");
  mkdirSync(path.join(work, "services"));
  writeFileSync(path.join(work, "services", "env"), "");
  const run = bash(
    "rig.sh cmd_run",
    [
      'load_label() { P=$work/profiles/0000000000000001; mkdir -p "$P"; echo "checkout /c/$1" >"$P/built-from"; }',
      "on_exit() { :; }",
      `run_architect_pass_and_fail() { printf '%s\\n' "$*" >"$R/ran"; echo exit=0 >"$R/out.txt"; }`,
      `work=$1 maps=$1/maps services_env=$1/services/env root=${tree} hide_state_file=''`,
      "cmd_run architect-pass-and-fail head 1 4 1700000000 no-verdict",
    ],
    [work]
  );
  const ranFile = path.join(work, "maps/runs/architect-pass-and-fail-no-verdict-head-1/ran");
  let ranWith = "no run of the name architect-pass-and-fail-no-verdict-head-1";
  try {
    ranWith = readFileSync(ranFile, "utf8");
  } catch {}

  // heads-world.ts's world and score.ts's reading of three runs' names, from the copy.
  const probe = path.join(copy, "catalog-probe.ts");
  writeFileSync(
    probe,
    [
      'import { headsWorld } from "./heads-world";',
      'import { CASED_RUNS } from "./score";',
      'const read = (name: string) => CASED_RUNS["architect-pass-and-fail"]?.exec(name)?.slice(1);',
      "console.log(JSON.stringify({",
      '  world: headsWorld("architect-pass-and-fail", "no-verdict") ?? null,',
      "  runs: [",
      '    "architect-pass-and-fail-no-verdict-head-1",',
      '    "architect-pass-and-fail-no-verdict-fail-head-1",',
      '    "architect-pass-and-fail-head-1",',
      "  ].map(read),",
      "}));",
    ].join("\n")
  );
  const probed = spawnWithin(
    { ...SCRIPT_RUN, part: "catalog-probe.ts" },
    [process.execPath, probe],
    { cwd: copy, env }
  );
  expect({ code: probed.exitCode, stderr: probed.stderr.toString() }).toEqual({
    code: 0,
    stderr: "",
  });
  const usage = spawnWithin({ ...SCRIPT_RUN, part: "seed.ts" }, [process.execPath, "seed.ts"], {
    cwd: copy,
    env,
  }).stderr.toString();

  expect({
    batch,
    runs: readFileSync(path.join(here, "runs"), "utf8"),
    run,
    ranWith,
    probed: JSON.parse(probed.stdout.toString()),
    usage: usage.includes(
      "architect-pass-and-fail [no-verdict-fail|no-verdict-pass|no-verdict]|ci-pass-and-fail"
    ),
  }).toEqual({
    batch: "exit=0 ",
    runs: ["", " no-verdict-fail", " no-verdict-pass", " no-verdict"]
      .map((which, at) => `run architect-pass-and-fail head 1 ${at + 1} 1700000000${which}\n`)
      .join(""),
    run: "exit=0 ",
    ranWith: "4 1700000000 no-verdict\n",
    probed: {
      world: "architect-pass-and-fail-no-verdict",
      runs: [
        ["no-verdict", "head"],
        ["no-verdict-fail", "head"],
        [null, "head"],
      ],
    },
    usage: true,
  });
});
