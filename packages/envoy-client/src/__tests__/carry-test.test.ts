// `dispatch carry-test` on planted merges: each repository is built here in the shape of the
// reference program's planted cases (a.txt of 40 lines; branch pr changes line 10; main changes
// the line a case names), and each row asserts the exit code and the last line, which is the
// verdict. Rows 11b and 11c build a --filter=blob:none clone, where git cannot read every object
// the tests need: the command must read UNKNOWN there, never carries.
import { describe, expect, test } from "bun:test";
import { execFileSync, spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { runDispatchCli } from "../dispatch-cli";
import { carryTestCommand } from "../dispatch-commands/carry-test";

const directory = temporaryDirectories("carry-test");

const BASE = Array.from({ length: 40 }, (_, at) => `line ${at + 1}`);
const ZERO = "0".repeat(40);

/** The environment the planted repositories are built in: no git configuration of the host. */
function buildEnv(home: string): Record<string, string> {
  return {
    PATH: process.env.PATH ?? "",
    HOME: home,
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_TERMINAL_PROMPT: "0",
    GIT_AUTHOR_NAME: "planted",
    GIT_AUTHOR_EMAIL: "p@example.com",
    GIT_COMMITTER_NAME: "planted",
    GIT_COMMITTER_EMAIL: "p@example.com",
  };
}

/** One planted repository, and git run in it. */
class Planted {
  readonly dir: string;
  readonly env: Record<string, string>;

  constructor(dir: string, env: Record<string, string>) {
    this.dir = dir;
    this.env = env;
  }

  static make(): Planted {
    const root = directory("repo");
    const dir = join(root, "repo");
    mkdirSync(dir);
    const planted = new Planted(dir, buildEnv(root));
    planted.git("init", "-q", "-b", "main");
    return planted;
  }

  /** git in the repository; a command-line hooks path keeps any git wrapper's hooks out. */
  git(...args: string[]): string {
    return execFileSync("git", ["-c", "core.hooksPath=/dev/null", ...args], {
      cwd: this.dir,
      env: this.env,
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"],
    }).trim();
  }

  /** git that may fail, such as a merge that stops on a conflict; answers its exit code. */
  tryGit(...args: string[]): number {
    return (
      spawnSync("git", ["-c", "core.hooksPath=/dev/null", ...args], {
        cwd: this.dir,
        env: this.env,
      }).status ?? -1
    );
  }

  gitInput(input: string, ...args: string[]): string {
    return execFileSync("git", args, {
      cwd: this.dir,
      env: this.env,
      input,
      encoding: "utf8",
    }).trim();
  }

  write(lines: readonly string[], file = "a.txt"): void {
    writeFileSync(join(this.dir, file), lines.map((line) => `${line}\n`).join(""));
  }

  commit(message: string): string {
    this.git("add", "-A");
    this.git("commit", "-q", "-m", message);
    return this.git("rev-parse", "HEAD");
  }

  sha(rev: string): string {
    return this.git("rev-parse", rev);
  }
}

/** a.txt with these lines set (1-based). */
function lines(set: Record<number, string>): string[] {
  const out = [...BASE];
  for (const [at, text] of Object.entries(set)) out[Number(at) - 1] = text;
  return out;
}

/** The base commit, branch pr changing line 10 (the earlier head), then main changing a line. */
function setup(
  prLine: number,
  mainLine: number,
  mainText = "main"
): { repo: Planted; src: string } {
  const repo = Planted.make();
  repo.write(BASE);
  repo.commit("base");
  repo.git("checkout", "-q", "-b", "pr");
  repo.write(lines({ [prLine]: "pr" }));
  const src = repo.commit("pr");
  repo.git("checkout", "-q", "main");
  repo.write(lines({ [mainLine]: mainText }));
  repo.commit("main");
  repo.git("checkout", "-q", "pr");
  return { repo, src };
}

interface Case {
  readonly repo: Planted;
  readonly src: string;
  readonly to: string;
}

function cleanCase(mainLine = 30): Case {
  const { repo, src } = setup(10, mainLine);
  repo.git("merge", "-q", "--no-edit", "main");
  return { repo, src, to: repo.sha("HEAD") };
}

function conflictCase(resolution: readonly string[] = lines({ 10: "pr and main" })): Case {
  const { repo, src } = setup(10, 10);
  expect(repo.tryGit("merge", "-q", "--no-edit", "main")).toBe(1);
  repo.write(resolution);
  return { repo, src, to: repo.commit("resolve") };
}

function adjacentCase(): Case {
  const { repo, src } = setup(10, 11);
  expect(repo.tryGit("merge", "-q", "--no-edit", "main")).toBe(1);
  repo.write(lines({ 10: "pr", 11: "main" }));
  return { repo, src, to: repo.commit("resolve, keeping both lines") };
}

function handCase(): Case {
  const { repo, src } = setup(10, 30);
  repo.git("merge", "-q", "--no-commit", "main");
  repo.write(lines({ 10: "pr", 30: "main", 20: "a hand edit" }));
  return { repo, src, to: repo.commit("merge with a hand edit") };
}

/** A clean merge that also moves a gitlink neither side moved, built from trees, so no checkout
 *  touches the gitlink. */
function gitlinkCase(): Case {
  const repo = Planted.make();
  const tree = (text: readonly string[], link: string): string => {
    const blob = repo.gitInput(
      text.map((line) => `${line}\n`).join(""),
      "hash-object",
      "-w",
      "--stdin"
    );
    return repo.gitInput(`100644 blob ${blob}\ta.txt\n160000 commit ${link}\tdep\n`, "mktree");
  };
  const x = "1".repeat(40);
  const y = "2".repeat(40);
  const base = repo.git("commit-tree", "-m", "base", tree(BASE, x));
  const src = repo.git("commit-tree", "-p", base, "-m", "pr", tree(lines({ 10: "pr" }), x));
  const main = repo.git("commit-tree", "-p", base, "-m", "main", tree(lines({ 30: "main" }), x));
  const to = repo.git(
    "commit-tree",
    "-p",
    src,
    "-p",
    main,
    "-m",
    "merge main, and move the gitlink",
    tree(lines({ 10: "pr", 30: "main" }), y)
  );
  repo.git("update-ref", "refs/heads/main", main);
  repo.git("update-ref", "refs/heads/pr", to);
  return { repo, src, to };
}

/** The source of a --filter=blob:none clone: pr changes a.txt line 10, main changes b.txt line 30,
 *  and the merge also sets a.txt line 20, so the merge test needs no blob and the patch test does. */
function promisorSource(): Case {
  const repo = Planted.make();
  repo.git("config", "uploadpack.allowFilter", "true");
  repo.git("config", "uploadpack.allowAnySHA1InWant", "true");
  repo.write(BASE);
  repo.write(BASE, "b.txt");
  repo.commit("base");
  repo.git("checkout", "-q", "-b", "pr");
  repo.write(lines({ 10: "pr" }));
  const src = repo.commit("pr");
  repo.git("checkout", "-q", "main");
  repo.write(lines({ 30: "main" }), "b.txt");
  repo.commit("main");
  repo.git("checkout", "-q", "pr");
  repo.git("merge", "-q", "--no-commit", "main");
  repo.write(lines({ 10: "pr", 20: "a hand edit" }));
  return { repo, src, to: repo.commit("merge with a hand edit") };
}

/** A --filter=blob:none clone of `source`, as a lane or a person clones: it holds every commit and
 *  tree, no blob, and origin/main but no local main. */
function partialClone(source: Planted): Planted {
  const root = directory("part");
  const dir = join(root, "clone");
  execFileSync(
    "git",
    ["clone", "-q", "--no-checkout", "--filter=blob:none", `file://${source.dir}`, dir],
    {
      env: source.env,
      stdio: "pipe",
    }
  );
  return new Planted(dir, source.env);
}

interface Run {
  readonly code: number;
  readonly lines: string[];
  readonly last: string;
}

/** The caller's environment: no git configuration of the host, and a TMPDIR of the test's own. */
function callerEnv(extra: Record<string, string> = {}): Record<string, string> {
  return {
    PATH: process.env.PATH ?? "",
    HOME: directory("home"),
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_NOSYSTEM: "1",
    TMPDIR: directory("tmp"),
    ...extra,
  };
}

async function carry(
  cwd: string,
  from: string,
  to: string,
  base: string,
  env: Record<string, string> = callerEnv()
): Promise<Run> {
  const out: string[] = [];
  const code = await carryTestCommand(["--from", from, "--to", to, "--base", base], {
    print: (text) => out.push(...text.replace(/\n$/, "").split("\n")),
    cwd,
    env,
  });
  return { code, lines: out, last: out.at(-1) ?? "" };
}

const short = (sha: string): string => sha.slice(0, 12);
const patchIds = (run: Run): string[] =>
  run.lines.filter((line) => line.startsWith("patch-id at "));
const ids = (run: Run): string[] => patchIds(run).map((line) => line.split(": ")[1] ?? "");
const carries = (c: Case): string =>
  `carries: no merge between ${short(c.src)} and ${short(c.to)} resolved a conflict, and the pull request's diff reads the same at both heads`;
const diffChanged = (c: Case): string =>
  `runs again: the pull request's diff changed between ${short(c.src)} and ${short(c.to)}`;
const conflict = (merge: string): string => `runs again: merge ${short(merge)} resolved a conflict`;

/** The patch-id of the pull request's diff, by plain git in the clone with this environment: the
 *  control that a setting reaches git when nothing keeps it out. */
function plainPatchId(repo: Planted, env: Record<string, string>, range: string): string {
  const diff = execFileSync("git", ["diff", "--binary", "-M", "-U0", range], {
    cwd: repo.dir,
    env,
  });
  return execFileSync("git", ["patch-id", "--verbatim"], { cwd: repo.dir, env, input: diff })
    .toString()
    .split(" ")[0] as string;
}

describe("dispatch carry-test", () => {
  test("a clean merge of the base whose diff stays the same carries", async () => {
    const c = cleanCase(30);
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(0);
    expect(run.lines).toContain(`merge ${c.to}: clean`);
    expect(patchIds(run)).toHaveLength(2);
    expect(new Set(ids(run)).size).toBe(1);
    expect(run.last).toBe(carries(c));
  });

  test("a near-line clean merge carries, since the patch test reads no context lines", async () => {
    const c = cleanCase(13);
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(0);
    expect(run.last).toBe(carries(c));
  });

  test("a merge that resolved a conflict runs again", async () => {
    const c = conflictCase();
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.lines).toContain(`merge ${c.to}: conflict`);
    expect(patchIds(run)).toEqual([]);
    expect(run.last).toBe(conflict(c.to));
  });

  test("an adjacent-line conflict runs again although its patch-ids stay the same", async () => {
    const c = adjacentCase();
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.lines).toContain(`merge ${c.to}: conflict`);
    expect(patchIds(run)).toEqual([]);
    expect(run.last).toBe(conflict(c.to));
  });

  test("a clean merge with a hand edit runs again", async () => {
    const c = handCase();
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.lines).toContain(`merge ${c.to}: clean`);
    expect(run.last).toBe(diffChanged(c));
  });

  test("a clean merge that moves a gitlink neither side moved runs again", async () => {
    const c = gitlinkCase();
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.last).toBe(diffChanged(c));
  });

  test("a push of the branch's own change before a clean merge runs again", async () => {
    const { repo, src } = setup(10, 30);
    repo.write(lines({ 10: "pr", 2: "a text push" }));
    repo.commit("text push");
    repo.git("merge", "-q", "--no-edit", "main");
    const c = { repo, src, to: repo.sha("HEAD") };
    const run = await carry(repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.last).toBe(diffChanged(c));
  });

  test("a merge of another branch runs again", async () => {
    const repo = Planted.make();
    repo.write(BASE);
    repo.commit("base");
    repo.git("checkout", "-q", "-b", "other");
    repo.write(lines({ 35: "other" }));
    repo.commit("other branch");
    repo.git("checkout", "-q", "main");
    repo.git("checkout", "-q", "-b", "pr");
    repo.write(lines({ 10: "pr" }));
    const src = repo.commit("pr");
    repo.git("merge", "-q", "--no-edit", "other");
    const c = { repo, src, to: repo.sha("HEAD") };
    const run = await carry(repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.last).toBe(diffChanged(c));
  });

  test("two heads with no merge between them print no merge and exit 4", async () => {
    const { repo, src } = setup(10, 30);
    repo.write(lines({ 10: "pr", 2: "a text push" }));
    const to = repo.commit("text push");
    const run = await carry(repo.dir, src, to, "main");
    expect(run.code).toBe(4);
    expect(run.lines).toEqual([
      `no merge: no merge commit lies between ${short(src)} and ${short(to)}, so the push rule decides`,
    ]);
  });

  test("an earlier head off the new head's first-parent path is UNKNOWN", async () => {
    const c = cleanCase(30);
    const run = await carry(c.repo.dir, c.repo.sha("main"), c.to, "main");
    expect(run.code).toBe(3);
    expect(run.last).toStartWith("UNKNOWN: ");
    expect(run.last).toContain("first-parent path");
  });

  test("a merge of three parents is UNKNOWN", async () => {
    const repo = Planted.make();
    repo.write(BASE);
    repo.commit("base");
    for (const [branch, at] of [
      ["one", 30],
      ["two", 35],
    ] as const) {
      repo.git("checkout", "-q", "-b", branch, "main");
      repo.write(lines({ [at]: branch }));
      repo.commit(branch);
    }
    repo.git("checkout", "-q", "-b", "pr", "main");
    repo.write(lines({ 10: "pr" }));
    const src = repo.commit("pr");
    repo.git("merge", "-q", "--no-edit", "one", "two");
    const to = repo.sha("HEAD");
    expect(repo.git("rev-list", "--parents", "-n", "1", to).split(" ")).toHaveLength(4);
    const run = await carry(repo.dir, src, to, "main");
    expect(run.code).toBe(3);
    expect(run.last).toStartWith("UNKNOWN: ");
    expect(run.last).toContain("3 parents");
  });

  test("a commit the clone lacks is UNKNOWN, and its temporary directory is gone", async () => {
    const c = cleanCase(30);
    const env = callerEnv();
    const run = await carry(c.repo.dir, ZERO, c.to, "main", env);
    expect(run.code).toBe(3);
    expect(run.last).toBe(
      `UNKNOWN: git rev-parse exited 128: fatal: Needed a single revision; the clone may lack commit ${ZERO}`
    );
    expect(readdirSync(env.TMPDIR as string)).toEqual([]);
  });

  test("in a --filter=blob:none clone, a hand-edited clean merge is UNKNOWN, never carries", async () => {
    const source = promisorSource();
    const part = partialClone(source.repo);
    const main = source.repo.sha("main");
    const env = callerEnv();
    const run = await carry(part.dir, source.src, source.to, main, env);
    expect(run.code).toBe(3);
    expect(run.last).toStartWith("UNKNOWN: git diff exited 128: ");
    expect(readdirSync(env.TMPDIR as string)).toEqual([]);
    // Control: the full repository answers, so only the missing blobs made the clone UNKNOWN.
    const full = await carry(source.repo.dir, source.src, source.to, main);
    expect(full.code).toBe(1);
    expect(full.last).toBe(diffChanged(source));
  });

  test("in a --filter=blob:none clone, a conflict merge whose merge test cannot read its blobs is UNKNOWN", async () => {
    // Case 3's shape, with the conflict resolved to main's a.txt: the new head's patch then holds
    // no change to a.txt, so the prefetch below never fetches main's blob of it.
    const c = conflictCase(lines({ 10: "main" }));
    c.repo.git("config", "uploadpack.allowFilter", "true");
    c.repo.git("config", "uploadpack.allowAnySHA1InWant", "true");
    const part = partialClone(c.repo);
    const main = c.repo.sha("main");
    part.git("diff", `${main}...${c.src}`);
    part.git("diff", `${main}...${c.to}`);
    const run = await carry(part.dir, c.src, c.to, main);
    expect(run.code).toBe(3);
    expect(run.last).toStartWith("UNKNOWN: git merge-tree exited 128: ");
  });

  test("a git whose merge-tree refuses --write-tree is UNKNOWN, naming the exit and its first stderr line", async () => {
    // A git older than 2.38 exits 129 with "unknown option" for merge-tree --write-tree. This git on
    // PATH answers merge-tree so and runs the real git for every other subcommand.
    const c = cleanCase(30);
    const execPath = execFileSync("git", ["--exec-path"], { encoding: "utf8" }).trim();
    const realGit = existsSync(join(execPath, "git"))
      ? join(execPath, "git")
      : (Bun.which("git") as string);
    const bin = directory("old-git");
    writeFileSync(
      join(bin, "git"),
      [
        "#!/bin/sh",
        'if [ "$1" = merge-tree ]; then',
        "  printf '%s\\n' \"error: unknown option 'write-tree'\" 'usage: git merge-tree [--trivial-merge] <base-tree> <branch1> <branch2>' >&2",
        "  exit 129",
        "fi",
        `exec '${realGit}' "$@"`,
        "",
      ].join("\n")
    );
    chmodSync(join(bin, "git"), 0o755);
    const env = callerEnv({ PATH: `${bin}:${process.env.PATH ?? ""}` });
    // Control: the git on that PATH exits 129 for merge-tree --write-tree.
    const direct = spawnSync("git", ["merge-tree", "--write-tree", c.src, c.repo.sha("main")], {
      cwd: c.repo.dir,
      env,
    });
    expect(direct.status).toBe(129);
    const run = await carry(c.repo.dir, c.src, c.to, "main", env);
    expect(run.code).toBe(3);
    expect(run.last).toStartWith(
      "UNKNOWN: git merge-tree exited 129: error: unknown option 'write-tree'"
    );
    expect(run.lines.some((line) => line.startsWith("patch-id at "))).toBe(false);
    expect(readdirSync(env.TMPDIR as string)).toEqual([]);
  });

  test("git settings in the environment, the clone's config and the home config change no answer", async () => {
    const c = gitlinkCase();
    const range = (head: string): string => `main...${head}`;
    const ignore = {
      GIT_CONFIG_COUNT: "1",
      GIT_CONFIG_KEY_0: "diff.ignoreSubmodules",
      GIT_CONFIG_VALUE_0: "all",
    };
    const log = {
      GIT_CONFIG_COUNT: "1",
      GIT_CONFIG_KEY_0: "diff.submodule",
      GIT_CONFIG_VALUE_0: "log",
    };
    for (const setting of [ignore, log]) {
      const run = await carry(c.repo.dir, c.src, c.to, "main", callerEnv(setting));
      expect(run.code).toBe(1);
      expect(run.last).toBe(diffChanged(c));
      const env = { ...c.repo.env, ...setting };
      expect(plainPatchId(c.repo, env, range(c.src))).toBe(plainPatchId(c.repo, env, range(c.to)));
    }
    // The home config: a .gitconfig in HOME, which GIT_CONFIG_GLOBAL no longer hides.
    const home = directory("gitconfig-home");
    writeFileSync(join(home, ".gitconfig"), "[diff]\n\tignoreSubmodules = all\n");
    const homeEnv = callerEnv({ HOME: home });
    delete homeEnv.GIT_CONFIG_GLOBAL;
    const byHome = await carry(c.repo.dir, c.src, c.to, "main", homeEnv);
    expect(byHome.code).toBe(1);
    expect(byHome.last).toBe(diffChanged(c));
    const plainHome = { ...c.repo.env, HOME: home };
    delete (plainHome as Record<string, string | undefined>).GIT_CONFIG_GLOBAL;
    expect(plainPatchId(c.repo, plainHome, range(c.src))).toBe(
      plainPatchId(c.repo, plainHome, range(c.to))
    );
    // The clone's own config.
    c.repo.git("config", "diff.ignoreSubmodules", "all");
    const byClone = await carry(c.repo.dir, c.src, c.to, "main");
    expect(byClone.code).toBe(1);
    expect(byClone.last).toBe(diffChanged(c));
    expect(plainPatchId(c.repo, c.repo.env, range(c.src))).toBe(
      plainPatchId(c.repo, c.repo.env, range(c.to))
    );
  });

  test("diff.context=7 and diff.noprefix=true in the environment change no patch-id", async () => {
    const c = cleanCase(13);
    const settings = {
      GIT_CONFIG_COUNT: "2",
      GIT_CONFIG_KEY_0: "diff.context",
      GIT_CONFIG_VALUE_0: "7",
      GIT_CONFIG_KEY_1: "diff.noprefix",
      GIT_CONFIG_VALUE_1: "true",
    };
    const plain = await carry(c.repo.dir, c.src, c.to, "main");
    const set = await carry(c.repo.dir, c.src, c.to, "main", callerEnv(settings));
    expect(set.code).toBe(0);
    expect(ids(set)).toEqual(ids(plain));
    expect(ids(set)).toHaveLength(2);
    // Control: plain git under the same settings reads another id.
    const range = `main...${c.to}`;
    expect(plainPatchId(c.repo, { ...c.repo.env, ...settings }, range)).not.toBe(
      plainPatchId(c.repo, c.repo.env, range)
    );
  });

  test("a merge=union attribute in the clone's attribute file hides no conflict", async () => {
    const c = conflictCase();
    writeFileSync(join(c.repo.dir, ".git", "info", "attributes"), "* merge=union\n");
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(1);
    expect(run.lines).toContain(`merge ${c.to}: conflict`);
    expect(run.last).toBe(conflict(c.to));
    // Control: git merge-tree in the clone reads the attribute and finds no conflict.
    expect(c.repo.tryGit("merge-tree", "--write-tree", c.src, c.repo.sha("main"))).toBe(0);
  });

  test("the temporary repository is removed after each run, a refusal included", async () => {
    const tmp = directory("tmp-each");
    const env = callerEnv({ TMPDIR: tmp });
    const clean = cleanCase(30);
    expect((await carry(clean.repo.dir, clean.src, clean.to, "main", env)).code).toBe(0);
    expect(readdirSync(tmp)).toEqual([]);
    const conflicted = conflictCase();
    expect(
      (await carry(conflicted.repo.dir, conflicted.src, conflicted.to, "main", env)).code
    ).toBe(1);
    expect(readdirSync(tmp)).toEqual([]);
    expect((await carry(clean.repo.dir, ZERO, clean.to, "main", env)).code).toBe(3);
    expect(readdirSync(tmp)).toEqual([]);
    const source = promisorSource();
    const part = partialClone(source.repo);
    expect((await carry(part.dir, source.src, source.to, source.repo.sha("main"), env)).code).toBe(
      3
    );
    expect(readdirSync(tmp)).toEqual([]);
  });

  test("--help prints the usage and exits 0, and a missing flag exits 2 and sends nothing", async () => {
    // A git on PATH that leaves a mark if it runs: a refused call runs no process at all.
    const bin = directory("bin");
    const mark = join(bin, "ran");
    writeFileSync(join(bin, "git"), `#!/bin/sh\ntouch '${mark}'\nexit 0\n`);
    chmodSync(join(bin, "git"), 0o755);
    const env = callerEnv({ PATH: bin });
    const out: string[] = [];
    const io = { print: (text: string) => out.push(text), cwd: bin, env };
    expect(await carryTestCommand(["--help"], io)).toBe(0);
    expect(out.join("\n")).toStartWith(
      "Usage: dispatch carry-test --from <commit> --to <commit> --base <commit-ish>"
    );
    out.length = 0;
    expect(await carryTestCommand(["--from", "a", "--to", "b"], io)).toBe(2);
    expect(out.join("\n")).toStartWith("dispatch carry-test was not called: ");
    expect(out.join("\n")).toContain("--base");
    expect(existsSync(mark)).toBe(false);
    expect(readdirSync(env.TMPDIR as string)).toEqual([]);
  });

  test("a base that moved on after the merge gives the same verdict", async () => {
    const c = cleanCase(30);
    c.repo.git("checkout", "-q", "main");
    c.repo.write(lines({ 30: "main", 35: "later" }));
    c.repo.commit("main moves on");
    const run = await carry(c.repo.dir, c.src, c.to, "main");
    expect(run.code).toBe(0);
    expect(run.last).toBe(carries(c));
  });

  test("a base that does not hold the merge's second parent reads changed", async () => {
    const c = cleanCase(30);
    const run = await carry(c.repo.dir, c.src, c.to, c.repo.sha("main~1"));
    expect(run.code).toBe(1);
    expect(run.last).toBe(diffChanged(c));
  });

  test("dispatch carry-test --help through the CLI prints the usage", async () => {
    const out: string[] = [];
    const code = await runDispatchCli(["carry-test", "--help"], callerEnv(), {
      stdout: (text) => out.push(text),
      stderr: (text) => out.push(text),
      readText: () => "",
      cwd: directory("cli"),
    });
    expect(code).toBe(0);
    expect(out.join("")).toStartWith(
      "Usage: dispatch carry-test --from <commit> --to <commit> --base <commit-ish>"
    );
  });
});
