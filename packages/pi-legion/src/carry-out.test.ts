import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import {
  copyFileSync,
  cpSync,
  linkSync,
  mkdirSync,
  mkdtempSync,
  realpathSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { skipWithout } from "@legion/test-fixtures/host-tools";

// skills/legion-controller/scripts/carry-out.sh is the controller's step 2 of docs/kubernetes.md,
// "Carrying unpushed work out of an issue's pod": it rebuilds what an issue pod's workspace holds
// and GitHub lacks into a git bundle, from the two tar archives step 1 copies out of the pod, in a
// namespace of its own (skills/legion-controller/references/operations.md, "A pod delete with
// nothing lost"). These cases build real archives, the way step 1 writes them, from a clone and a
// worktree laid out as an issue pod's are, and run the script on them: one good pair, and one
// archive or workspace for each refusal.
const script = resolve(import.meta.dir, "../../../skills/legion-controller/scripts/carry-out.sh");
const key = "ACME-1";
const namespace = [
  "--user",
  "--map-root-user",
  "--net",
  "--mount",
  "--pid",
  "--fork",
  "--mount-proc",
];

/** Why this host cannot run the script, or undefined where it can. Read once, at module scope. */
function unshareRefusal(): string | undefined {
  if (skipWithout("git", "tar", "unshare", "mkfifo")) return "a program it needs is missing";
  const probe = Bun.spawnSync(["unshare", ...namespace, "true"], { stderr: "pipe" });
  if (probe.exitCode === 0) return undefined;
  const reason = `unshare ${namespace.join(" ")} is refused on this host: ${probe.stderr.toString().trim()}`;
  console.warn(
    `Skipped the tests in ${import.meta.path}. ${reason}. carry-out.sh rebuilds only inside that namespace.`
  );
  return reason;
}
const skipped = unshareRefusal() !== undefined;

let root: string;
let ws: string;
let clone: string;
let goodGit: string;
let goodTree: string;
let cases = 0;

/** The environment every git the fixture runs gets: no configuration of the host's. */
function gitEnvironment(): Record<string, string> {
  const environment: Record<string, string> = {};
  for (const [name, value] of Object.entries(process.env)) {
    if (value !== undefined && name !== "CARRY_OUT_STAGE") environment[name] = value;
  }
  return {
    ...environment,
    HOME: join(root, "home"),
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_AUTHOR_NAME: "Legion Test",
    GIT_AUTHOR_EMAIL: "legion-test@example.invalid",
    GIT_COMMITTER_NAME: "Legion Test",
    GIT_COMMITTER_EMAIL: "legion-test@example.invalid",
  };
}

/** Runs a command to completion and returns its stdout, failing the test on a non-zero exit. */
function run(command: string[], cwd = root): string {
  const result = Bun.spawnSync(command, { cwd, env: gitEnvironment(), stderr: "pipe" });
  if (result.exitCode !== 0) {
    throw new Error(`${command.join(" ")} exited ${result.exitCode}: ${result.stderr.toString()}`);
  }
  return result.stdout.toString();
}

/** A fresh directory under the fixture's root, for one case's files. */
function caseDirectory(): string {
  cases += 1;
  const directory = join(root, `case-${cases}`);
  mkdirSync(directory);
  return directory;
}

/** Step 1's git archive: the clone's refs, worktree records and objects, from `gitDirectory`. */
function gitArchive(gitDirectory: string, archive: string): void {
  run([
    "sh",
    "-c",
    'set -e; cd -P "$1"; tar --hard-dereference -cf - refs worktrees $(test -f packed-refs && echo packed-refs) objects >"$2"',
    "sh",
    gitDirectory,
    archive,
  ]);
}

/** Step 1's tree archive: the workspace's files, every .git and .jj left out. */
function treeArchive(workspace: string, archive: string): void {
  run([
    "sh",
    "-c",
    'set -e; cd -P "$1"; tar --hard-dereference --ignore-case --exclude=.git --exclude=.jj --exclude=./.codegraph -cf - . >"$2"',
    "sh",
    workspace,
    archive,
  ]);
}

/** A copy of `archive` with the entries `names` of `directory` appended, as they are on disk. */
function appended(archive: string, directory: string, ...names: string[]): string {
  const copy = join(caseDirectory(), "appended.tar");
  copyFileSync(archive, copy);
  run(["tar", "-rf", copy, "-C", directory, ...names]);
  return copy;
}

type Outcome = { exitCode: number; stdout: string; stderr: string; bundle: string };

/** Runs carry-out.sh on one pair of archives, with its bundle in a fresh directory. */
function carryOut(gitTar: string, treeTar: string, workspace = ws): Outcome {
  const bundle = join(caseDirectory(), `${key}.bundle`);
  const result = Bun.spawnSync(["sh", script, key, workspace, gitTar, treeTar, bundle], {
    cwd: root,
    env: gitEnvironment(),
    stderr: "pipe",
  });
  return {
    exitCode: result.exitCode,
    stdout: result.stdout.toString(),
    stderr: result.stderr.toString(),
    bundle,
  };
}

/** Holds a refusal: exit 1 and one `refused:` line that says `why`. */
function expectRefused(outcome: Outcome, why: string): void {
  expect(outcome.exitCode).toBe(1);
  const lines = outcome.stderr.split("\n").filter((line) => line.startsWith("refused: "));
  expect(lines).toHaveLength(1);
  expect(lines[0]).toContain(why);
}

describe.skipIf(skipped)("carry-out.sh", () => {
  beforeAll(() => {
    root = realpathSync(mkdtempSync(join(tmpdir(), "carry-out-")));
    mkdirSync(join(root, "home"));
    // GitHub's side: one commit on main, which the clone fetches as origin/main.
    const origin = join(root, "origin");
    run(["git", "init", "-q", "-b", "main", origin]);
    writeFileSync(join(origin, "notes.txt"), "first\n");
    run(["git", "-C", origin, "add", "notes.txt"]);
    run(["git", "-C", origin, "commit", "-q", "-m", "first"]);
    // The pod's side: the shared clone, and the issue's workspace as a worktree of it.
    clone = join(root, "legion/repos/github.com/o/r");
    mkdirSync(join(root, "legion/repos/github.com/o"), { recursive: true });
    mkdirSync(join(root, "legion/workspaces/o/r"), { recursive: true });
    run(["git", "clone", "-q", origin, clone]);
    ws = join(root, "legion/workspaces/o/r/acme-1");
    run(["git", "-C", clone, "worktree", "add", "-q", "-b", `legion/${key}`, ws]);
    // A commit no push carried, then the HEAD jj leaves detached on it, then edits in no commit.
    writeFileSync(join(ws, "notes.txt"), "first\nunpushed\n");
    run(["git", "-C", ws, "commit", "-q", "-am", "unpushed work"]);
    run(["git", "-C", ws, "checkout", "-q", "--detach"]);
    writeFileSync(join(ws, "notes.txt"), "first\nunpushed\nedited\n");
    writeFileSync(join(ws, "new.txt"), "a file no commit holds\n");
    mkdirSync(join(root, "archives"));
    goodGit = join(root, "archives/git.tar");
    goodTree = join(root, "archives/tree.tar");
    gitArchive(join(clone, ".git"), goodGit);
    treeArchive(ws, goodTree);
  });

  afterAll(() => {
    if (root !== undefined) rmSync(root, { recursive: true, force: true });
  });

  test("a good pair of archives becomes a bundle of the unpushed commit and the edits", () => {
    const outcome = carryOut(goodGit, goodTree);
    expect(outcome.stderr).not.toContain("refused:");
    expect(outcome.exitCode).toBe(0);
    expect(outcome.stdout).toContain("unpushed commits of other branches, not in the bundle:");

    const verify = join(caseDirectory(), "verify");
    run(["git", "clone", "-q", join(root, "origin"), verify]);
    run([
      "git",
      "-C",
      verify,
      "fetch",
      "-q",
      outcome.bundle,
      `rescue-${key}:refs/remotes/rescue/${key}`,
    ]);
    const added = run(["git", "-C", verify, "log", "--format=%s", `origin/main..rescue/${key}`]);
    expect(added.split("\n").filter((line) => line !== "")).toEqual([
      `Edits left in ${key}'s workspace`,
      "unpushed work",
    ]);
    expect(run(["git", "-C", verify, "show", `rescue/${key}:notes.txt`])).toBe(
      "first\nunpushed\nedited\n"
    );
    expect(run(["git", "-C", verify, "show", `rescue/${key}:new.txt`])).toBe(
      "a file no commit holds\n"
    );
  }, 60_000);

  test("it refuses a git archive holding a symlink", () => {
    const directory = caseDirectory();
    symlinkSync("/etc/passwd", join(directory, "link"));
    expectRefused(carryOut(appended(goodGit, directory, "link"), goodTree), "holds l");
  }, 60_000);

  test("it refuses a git archive holding a fifo", () => {
    const directory = caseDirectory();
    run(["mkfifo", join(directory, "fifo")]);
    expectRefused(carryOut(appended(goodGit, directory, "fifo"), goodTree), "holds p");
  }, 60_000);

  test("it refuses a git archive holding a device", () => {
    expectRefused(carryOut(appended(goodGit, "/", "dev/null"), goodTree), "holds c");
  }, 60_000);

  test("it refuses a git archive holding a hard link", () => {
    const directory = caseDirectory();
    writeFileSync(join(directory, "one"), "one\n");
    linkSync(join(directory, "one"), join(directory, "two"));
    expectRefused(carryOut(appended(goodGit, directory, "one", "two"), goodTree), "holds h");
  }, 60_000);

  test("it refuses a tree archive holding a fifo", () => {
    const directory = caseDirectory();
    run(["mkfifo", join(directory, "fifo")]);
    expectRefused(carryOut(goodGit, appended(goodTree, directory, "fifo")), "holds p");
  }, 60_000);

  test("it refuses a tree archive holding a symlink to an absolute target", () => {
    const directory = caseDirectory();
    symlinkSync("/etc/passwd", join(directory, "absolute"));
    expectRefused(
      carryOut(goodGit, appended(goodTree, directory, "absolute")),
      "holds a symlink whose target is absolute"
    );
  }, 60_000);

  test("it refuses a tree archive holding a symlink whose target holds ..", () => {
    const directory = caseDirectory();
    symlinkSync("../../outside", join(directory, "climbing"));
    expectRefused(
      carryOut(goodGit, appended(goodTree, directory, "climbing")),
      "holds a symlink whose target holds .."
    );
  }, 60_000);

  test("it refuses a tree archive holding an entry under a .git directory", () => {
    const directory = caseDirectory();
    mkdirSync(join(directory, "sub/.git"), { recursive: true });
    writeFileSync(join(directory, "sub/.git/config"), "[core]\n\tfsmonitor = /bin/true\n");
    expectRefused(
      carryOut(goodGit, appended(goodTree, directory, "sub")),
      "holds an entry through .git or .jj: sub/.git/"
    );
  }, 60_000);

  test("it refuses a tree archive holding an entry under a .jj directory in any case", () => {
    const directory = caseDirectory();
    mkdirSync(join(directory, ".JJ"));
    writeFileSync(join(directory, ".JJ/repo"), "/elsewhere\n");
    expectRefused(
      carryOut(goodGit, appended(goodTree, directory, ".JJ")),
      "holds an entry through .git or .jj: .JJ/"
    );
  }, 60_000);

  test("it refuses a workspace no worktree record names", () => {
    const other = join(root, "legion/workspaces/o/r/acme-2");
    expectRefused(carryOut(goodGit, goodTree, other), `no worktree record names ${other}`);
  }, 60_000);

  test("it refuses a worktree record whose HEAD is not a commit id", () => {
    const stage = join(caseDirectory(), "git");
    mkdirSync(stage);
    for (const name of ["refs", "worktrees", "objects", "packed-refs"]) {
      cpSync(join(clone, ".git", name), join(stage, name), { recursive: true });
    }
    writeFileSync(join(stage, "worktrees/acme-1/HEAD"), `ref: refs/heads/legion/${key}\n`);
    const gitTar = join(caseDirectory(), "git.tar");
    gitArchive(stage, gitTar);
    expectRefused(carryOut(gitTar, goodTree), "worktrees/acme-1/HEAD is not a commit id");
  }, 60_000);

  test("it refuses to rebuild outside its own namespace", () => {
    const result = Bun.spawnSync(
      ["sh", script, key, ws, goodGit, goodTree, join(caseDirectory(), "out.bundle"), "scratch"],
      { cwd: root, env: { ...gitEnvironment(), CARRY_OUT_STAGE: "rebuild" }, stderr: "pipe" }
    );
    expectRefused(
      {
        exitCode: result.exitCode,
        stdout: result.stdout.toString(),
        stderr: result.stderr.toString(),
        bundle: "",
      },
      "not process 1 of its own PID namespace"
    );
  }, 60_000);
});
