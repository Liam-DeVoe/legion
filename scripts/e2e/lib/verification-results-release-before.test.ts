import { beforeAll, describe, expect, test } from "bun:test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { memoryRoot, temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { scriptFunctions } from "./script-functions";

// verification-results.sh's release_before_of_head: the release before the change, which
// older-rows' database is written by, is main at the commit the checkout's branch last merged,
// read from git at each run, so a later merge of main moves it with no edit of the script. The
// fixture is an origin whose main is m1..m4 and whose branch `feature` forked at m1 and merged m2.
const releaseBeforeOfHead = scriptFunctions(join(import.meta.dir, "..", "verification-results.sh"))(
  "release_before_of_head"
);
// The fixture's eight git repositories hold about 380 entries at once (250 files), made and removed
// inside a hook's 5 s: on a disk other programs share, the removal outran that bound (a run on a
// loaded box timed out in its afterAll), so the directory is on a tmpfs where one is (memoryRoot),
// with room for four runs at once.
const dir = temporaryDirectory("verification-results-release-before-test", {
  root: memoryRoot({ need: 4 * 2 ** 20 }),
});
// git with no configuration of the machine's, so a fixture reads the same on every runner.
const gitEnv = {
  PATH: process.env.PATH ?? "/usr/bin:/bin",
  HOME: dir,
  GIT_CONFIG_GLOBAL: "/dev/null",
  GIT_CONFIG_NOSYSTEM: "1",
  GIT_AUTHOR_NAME: "fixture",
  GIT_AUTHOR_EMAIL: "fixture@example.com",
  GIT_COMMITTER_NAME: "fixture",
  GIT_COMMITTER_EMAIL: "fixture@example.com",
};

function git(cwd: string, ...args: string[]): string {
  const run = Bun.spawnSync(["git", ...args], { cwd, env: gitEnv });
  if (run.exitCode !== 0) {
    throw new Error(`git ${args.join(" ")} in ${cwd}: ${run.stderr.toString()}`);
  }
  return run.stdout.toString().trim();
}

function commit(cwd: string, name: string): string {
  Bun.spawnSync(["bash", "-c", `printf '%s\\n' "$1" > "$1.txt"`, "_", name], { cwd });
  git(cwd, "add", `${name}.txt`);
  git(cwd, "commit", "-q", "-m", name);
  return git(cwd, "rev-parse", "HEAD");
}

// Each step runs git, which a loaded machine can take seconds over; bun's 5 s default is no bound
// of what these tests check.
const gitTimeoutMs = 60_000;
let origin = "";
let url = "";
let m2 = "";
let m3 = "";
let m4 = "";
beforeAll(() => {
  origin = join(dir, "origin");
  mkdirSync(origin);
  git(origin, "init", "-q", "-b", "main");
  // A file:// origin answers a fetch by commit and a filtered clone as GitHub does.
  git(origin, "config", "uploadpack.allowFilter", "true");
  git(origin, "config", "uploadpack.allowAnySHA1InWant", "true");
  const m1 = commit(origin, "m1");
  m2 = commit(origin, "m2");
  git(origin, "checkout", "-q", "-b", "feature", m1);
  commit(origin, "f1");
  git(origin, "merge", "-q", "--no-edit", m2);
  git(origin, "checkout", "-q", "main");
  m3 = commit(origin, "m3");
  m4 = commit(origin, "m4");
  url = `file://${origin}`;
}, gitTimeoutMs);

/** Runs release_before_of_head over checkout with a repository of its own: stdout and status. */
function releaseBefore(checkout: string, name: string): { out: string; code: number } {
  const run = Bun.spawnSync(
    [
      "bash",
      "-c",
      `set -euo pipefail\n${releaseBeforeOfHead}\nrelease_before_of_head "$1" "$2"`,
      "_",
      checkout,
      join(dir, `${name}.git`),
    ],
    { env: gitEnv }
  );
  return { out: run.stdout.toString().trim(), code: run.exitCode };
}

describe("release_before_of_head", () => {
  test(
    "a whole checkout of the branch: the commit of main it last merged, not main's tip",
    () => {
      const checkout = join(dir, "whole");
      git(dir, "clone", "-q", "--branch", "feature", url, checkout);
      expect(releaseBefore(checkout, "whole-history")).toEqual({ out: m2, code: 0 });
      expect(m2).not.toBe(m4);
    },
    gitTimeoutMs
  );

  test(
    "a later merge of main, not yet pushed, moves it with no edit",
    () => {
      const checkout = join(dir, "merged");
      git(dir, "clone", "-q", "--branch", "feature", url, checkout);
      git(checkout, "merge", "-q", "--no-edit", m3);
      expect(releaseBefore(checkout, "merged-history")).toEqual({ out: m3, code: 0 });
    },
    gitTimeoutMs
  );

  test(
    "a shallow checkout, as CI's is: HEAD's history is fetched from origin",
    () => {
      const checkout = join(dir, "shallow");
      git(dir, "clone", "-q", "--depth=1", "--branch", "feature", url, checkout);
      expect(git(checkout, "rev-parse", "--is-shallow-repository")).toBe("true");
      expect(releaseBefore(checkout, "shallow-history")).toEqual({ out: m2, code: 0 });
    },
    gitTimeoutMs
  );

  test(
    "a checkout that names no origin prints nothing and fails",
    () => {
      const checkout = join(dir, "no-origin");
      git(dir, "clone", "-q", "--branch", "feature", url, checkout);
      git(checkout, "remote", "remove", "origin");
      const run = releaseBefore(checkout, "no-origin-history");
      expect(run.out).toBe("");
      expect(run.code).not.toBe(0);
    },
    gitTimeoutMs
  );
});
