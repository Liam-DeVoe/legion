// askgate-cases.txt, the catalog of the askgate scenario's cases, and the readers that take each
// case from it: rig.sh's batch and run, and score.ts's run names.
//
//   bun test askgate-cases.test.ts

import { expect, setDefaultTimeout, test } from "bun:test";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { spawnWithin } from "@legion/test-fixtures/row-clock";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { copyOfThisDirectory, rigFunctions } from "./catalog-copy";
import { fileRowLimit, RIG_STEP, SCRIPT_RUN } from "./test-limit";

// The row runs two rig.sh steps and one bun script as processes of their own.
setDefaultTimeout(fileRowLimit(RIG_STEP, RIG_STEP, SCRIPT_RUN));

const makeDirectory = temporaryDirectories("askgate-catalog");

// Catches: a reader that keeps a list of the askgate cases of its own, so a case added to the
// catalog alone is refused by rig.sh's batch or run, or read by score.ts as another case's run.
// The added case's name begins with another case's (upload), so a pattern that tries the shorter
// name first reads its run as upload's, of the label twice-head.
test("a case added to the askgate catalog alone reaches rig.sh and score.ts", () => {
  const copy = copyOfThisDirectory(makeDirectory("tree"));
  const tree = path.resolve(copy, "../../../..");
  appendFileSync(path.join(copy, "askgate-cases.txt"), "upload-twice\n");
  const env = {
    PATH: process.env.PATH ?? "",
    HOME: makeDirectory("home"),
    SKILL_SCENARIOS_ASKGATE_CASE: "upload-twice",
    SKILL_SCENARIOS_PARALLEL: "1",
  };
  const functions = rigFunctions(copy);
  const bash = (part: string, lines: string[], args: string[]) => {
    const ran = spawnWithin(
      { ...RIG_STEP, part },
      ["bash", "-c", [functions, ...lines].join("\n"), "-", ...args],
      { env }
    );
    return `exit=${ran.exitCode} ${ran.stderr.toString()}`;
  };

  // A batch of one run, whose run line a stand-in rig.sh records.
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
      `here=$1 work=$2 root=${tree}`,
      "cmd_batch askgate 1 head",
    ],
    [here, makeDirectory("batch")]
  );
  let runs = "no run line";
  try {
    runs = readFileSync(path.join(here, "runs"), "utf8");
  } catch {}
  // A run of the added case, its run function stubbed.
  const work = makeDirectory("run");
  mkdirSync(path.join(work, "services"));
  writeFileSync(path.join(work, "services", "env"), "");
  const run = bash(
    "rig.sh cmd_run",
    [
      'load_label() { P=$work/profiles/0000000000000001; mkdir -p "$P"; echo "checkout /c/$1" >"$P/built-from"; }',
      "on_exit() { :; }",
      `run_askgate() { echo "$SKILL_SCENARIOS_ASKGATE_CASE" >"$R/ran"; echo exit=0 >"$R/out.txt"; }`,
      `work=$1 maps=$1/maps services_env=$1/services/env root=${tree} hide_state_file=''`,
      "cmd_run askgate head 1 1 1700000000",
    ],
    [work]
  );
  let ranWith = "no run of the name askgate-upload-twice-head-1";
  try {
    ranWith = readFileSync(path.join(work, "maps/runs/askgate-upload-twice-head-1/ran"), "utf8");
  } catch {}

  // score.ts's reading of two runs' names, from the copy.
  const probe = path.join(copy, "catalog-probe.ts");
  writeFileSync(
    probe,
    [
      'import { CASED_RUNS } from "./score";',
      "console.log(JSON.stringify(",
      '  ["askgate-upload-twice-head-1", "askgate-upload-head-1"].map(',
      "    (name) => CASED_RUNS.askgate?.exec(name)?.slice(1)",
      "  )",
      "));",
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

  expect({ batch, runs, run, ranWith, read: JSON.parse(probed.stdout.toString()) }).toEqual({
    batch: "exit=0 ",
    runs: "run askgate head 1 1 1700000000\n",
    run: "exit=0 ",
    ranWith: "upload-twice\n",
    read: [
      ["upload-twice", "head"],
      ["upload", "head"],
    ],
  });
});
