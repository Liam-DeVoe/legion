// packages/envoy-client/src/dispatch-commands/carry-test.ts
//
// `dispatch carry-test --from <commit> --to <commit> --base <commit-ish>`: the two merge tests that
// decide whether a pass of a check carries across a merge of the base, as GitHub decides a stale
// approval. For each merge commit on the first-parent path from --to back to --from,
// `git merge-tree --write-tree` on its two parents says whether the merge resolved a conflict.
// Where none did, the patch-id of the pull request's diff from the base (`git diff --binary -M
// -U0 <base>...<head> | git patch-id --verbatim`, no context lines) must read the same at both
// heads. The last line it prints is the verdict; docs/carry-test.md says each output and exit.
//
// Like `dispatch login`, it is no Dispatch tool: it reads no host, no session and no envoy.json,
// and sends nothing. It reads the clone it runs in and writes only its own temporary directory.
// There it makes a new bare repository that borrows the clone's objects (`objects/info/alternates`)
// and runs every test in it with an environment of five names, so no git setting of the caller's
// environment, home, system or clone, and no attribute file, changes an answer. That repository
// fetches nothing: an object the clone lacks (a --filter=blob:none clone) fails the git process
// that needs it, and every git exit outside the ones expected reads UNKNOWN, never a verdict.

import { type SpawnSyncReturns, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

/** The exit codes: the verdicts, a usage error, and UNKNOWN. */
const CARRIES = 0;
const RUNS_AGAIN = 1;
const USAGE = 2;
const UNKNOWN = 3;
const NO_MERGE = 4;

/** Room for a long first-parent list or a large diff; past it the process reads UNKNOWN. */
const MAX_OUTPUT = 1 << 30;

/** `dispatch carry-test --help`. */
const CARRY_TEST_HELP = [
  "Usage: dispatch carry-test --from <commit> --to <commit> --base <commit-ish>",
  "",
  "Runs the two merge tests that decide whether a pass of a check carries from an earlier head to a new head across a merge of the base. For each merge commit on the first-parent path from --to back to --from, git merge-tree --write-tree on its two parents finds whether the merge resolved a conflict. Where no merge did, the patch-id of the pull request's diff from the base, with no context lines, must read the same at both heads.",
  "",
  "It prints one line per merge on that path, oldest first (merge <sha>: clean, or merge <sha>: conflict); then, where no merge resolved a conflict, the patch-id at each head; then the verdict on its last line: carries, runs again, no merge (no merge commit lies between the heads, so the push rule decides), or UNKNOWN (git could not answer, such as in a clone that lacks an object the tests read).",
  "",
  "Flags:",
  "  --from <commit>",
  "      The head of the earlier pass.",
  "  --to <commit>",
  "      The new head.",
  "  --base <commit-ish>",
  "      The base branch as fetched, such as origin/main.",
  "",
  "Exit codes: 0 carries, 1 runs again, 2 a usage error, 3 UNKNOWN, 4 no merge. The last line is the verdict.",
  "",
  "Run it in a git clone or worktree of the pull request's repository. It runs local git only, in a new bare repository under TMPDIR that borrows the clone's objects, with no git setting of the environment, home, system or clone, and it removes that repository on every exit. It reads no Dispatch host, session or envoy.json, and sends nothing.",
  "",
  "Example: dispatch carry-test --from 6abd5ac2d938 --to 90768ceb0004 --base origin/main",
  "",
].join("\n");

/** What the command writes to and runs in. */
export interface CarryTestIo {
  /** Writes one line of output. */
  print(text: string): void;
  /** The clone or worktree the command reads. */
  readonly cwd: string;
  /** The caller's environment: the two reads in the clone run in it, and every git process takes
   *  its PATH from it, never from `process.env`. */
  readonly env: Readonly<Record<string, string>>;
}

/** The flags `argv` sets, or the problem with them. */
type ParsedCarryTest =
  | { readonly kind: "help" }
  | { readonly kind: "test"; readonly from: string; readonly to: string; readonly base: string }
  | { readonly kind: "refused"; readonly problem: string };

const FLAGS = ["--from", "--to", "--base"] as const;
type Flag = (typeof FLAGS)[number];

function parseCarryTest(argv: readonly string[]): ParsedCarryTest {
  const values = new Map<Flag, string>();
  for (let at = 0; at < argv.length; at += 1) {
    const word = argv[at] as string;
    if (word === "--help" || word === "-h") return { kind: "help" };
    const flag = FLAGS.find((name) => word === name || word.startsWith(`${name}=`));
    if (flag === undefined)
      return { kind: "refused", problem: `unknown argument ${JSON.stringify(word)}` };
    let value: string | undefined;
    if (word === flag) {
      value = argv[at + 1];
      at += 1;
    } else {
      value = word.slice(flag.length + 1);
    }
    if (value === undefined || value === "")
      return { kind: "refused", problem: `${flag} needs a commit` };
    if (values.has(flag)) return { kind: "refused", problem: `${flag} is given twice` };
    values.set(flag, value);
  }
  const missing = FLAGS.filter((flag) => !values.has(flag));
  if (missing.length > 0)
    return { kind: "refused", problem: `${missing.join(", ")} must be given` };
  return {
    kind: "test",
    from: values.get("--from") as string,
    to: values.get("--to") as string,
    base: values.get("--base") as string,
  };
}

/** A verdict the command cannot give: the line it prints after `UNKNOWN: `. */
class Unknown extends Error {}

/** The first line of a process's stderr with text on it. */
function firstStderrLine(run: SpawnSyncReturns<Buffer>): string {
  const line = (run.stderr?.toString("utf8") ?? "").split("\n").find((text) => text.trim() !== "");
  return line?.trim() ?? "(nothing on stderr)";
}

/**
 * Runs one git process and answers it once its exit is one of `expected`; any other exit, a
 * process that could not start, or output past MAX_OUTPUT is UNKNOWN, naming the subcommand.
 */
function git(
  args: readonly string[],
  where: { readonly cwd: string; readonly env: Readonly<Record<string, string>> },
  expected: readonly number[],
  input?: Buffer
): SpawnSyncReturns<Buffer> {
  const run = spawnSync("git", args, {
    cwd: where.cwd,
    env: where.env,
    input,
    maxBuffer: MAX_OUTPUT,
  });
  const name = args.find((arg) => !arg.startsWith("-")) ?? "git";
  if (run.error !== undefined) throw new Unknown(`git ${name} could not run: ${run.error.message}`);
  if (run.status === null || !expected.includes(run.status)) {
    throw new Unknown(`git ${name} exited ${run.status ?? run.signal}: ${firstStderrLine(run)}`);
  }
  return run;
}

const text = (run: SpawnSyncReturns<Buffer>): string => run.stdout.toString("utf8").trim();
const short = (sha: string): string => sha.slice(0, 12);

/** The patch-id line of one head: `none (empty diff)` only where git diff exits 0 and prints
 *  nothing, and UNKNOWN where it prints a diff and git patch-id prints no id. git diff and git
 *  patch-id run as two processes, so each exit is read. */
function patchId(
  head: string,
  base: string,
  bare: { cwd: string; env: Record<string, string> }
): string {
  const diff = git(["diff", "--binary", "-M", "-U0", `${base}...${head}`], bare, [0]);
  if (diff.stdout.length === 0) return "none (empty diff)";
  const id = text(git(["patch-id", "--verbatim"], bare, [0], diff.stdout)).split(/\s+/)[0] ?? "";
  if (!/^[0-9a-f]{40,64}$/.test(id)) {
    throw new Unknown(
      `git patch-id printed no id for the ${diff.stdout.length}-byte diff at ${short(head)}`
    );
  }
  return id;
}

/**
 * Runs `dispatch carry-test` with `argv` (the words after `carry-test`) in the clone at `io.cwd`,
 * and answers its exit code: 0 carries, 1 runs again, 2 a usage error, 3 UNKNOWN, 4 no merge.
 */
export async function carryTestCommand(argv: readonly string[], io: CarryTestIo): Promise<number> {
  const parsed = parseCarryTest(argv);
  if (parsed.kind === "help") {
    io.print(CARRY_TEST_HELP);
    return CARRIES;
  }
  if (parsed.kind === "refused") {
    io.print(
      `dispatch carry-test was not called: ${parsed.problem}. Run dispatch carry-test --help.`
    );
    return USAGE;
  }
  let dir: string | undefined;
  try {
    // The two reads in the caller's clone, with the caller's environment.
    const clone = { cwd: io.cwd, env: io.env };
    const resolve = (commit: string): string => {
      const run = spawnSync("git", ["rev-parse", "--verify", `${commit}^{commit}`], {
        cwd: clone.cwd,
        env: clone.env,
      });
      if (run.error !== undefined)
        throw new Unknown(`git rev-parse could not run: ${run.error.message}`);
      if (run.status !== 0) {
        throw new Unknown(
          `git rev-parse exited ${run.status ?? run.signal}: ${firstStderrLine(run)}; the clone may lack commit ${commit}`
        );
      }
      return text(run);
    };
    const from = resolve(parsed.from);
    const to = resolve(parsed.to);
    const base = resolve(parsed.base);
    const common = text(
      git(["rev-parse", "--path-format=absolute", "--git-common-dir"], clone, [0])
    );

    // The new bare repository, which borrows the clone's objects and reads no setting but its own.
    dir = mkdtempSync(join(io.env.TMPDIR || tmpdir(), "carry-test-"));
    const bare = {
      cwd: dir,
      env: {
        PATH: io.env.PATH ?? "",
        HOME: dir,
        GIT_CONFIG_NOSYSTEM: "1",
        GIT_ATTR_NOSYSTEM: "1",
        GIT_DIR: dir,
      },
    };
    git(["init", "-q", "--bare", "--template="], bare, [0]);
    mkdirSync(join(dir, "objects", "info"), { recursive: true });
    writeFileSync(join(dir, "objects", "info", "alternates"), `${common}/objects\n`);

    // The first-parent path from the new head back to the earlier one.
    const firstParents = text(git(["rev-list", "--first-parent", to], bare, [0])).split("\n");
    const at = firstParents.indexOf(from);
    if (at < 0) {
      throw new Unknown(
        `${short(from)} is not on the first-parent path of ${short(to)}, as after a force-push or a rebase, so neither test applies`
      );
    }
    const merges: { sha: string; parents: string[] }[] = [];
    for (const sha of firstParents.slice(0, at).reverse()) {
      const parents = text(git(["rev-list", "--parents", "-n", "1", sha], bare, [0]))
        .split(/\s+/)
        .slice(1);
      if (parents.length > 2) {
        throw new Unknown(
          `merge ${short(sha)} has ${parents.length} parents, and the merge test reads two`
        );
      }
      if (parents.length === 2) merges.push({ sha, parents });
    }
    if (merges.length === 0) {
      io.print(
        `no merge: no merge commit lies between ${short(from)} and ${short(to)}, so the push rule decides`
      );
      return NO_MERGE;
    }

    // The merge test, on each merge's two parents, oldest first.
    let conflicted: string | undefined;
    for (const { sha, parents } of merges) {
      const run = spawnSync("git", ["merge-tree", "--write-tree", ...parents], {
        cwd: bare.cwd,
        env: bare.env,
        maxBuffer: MAX_OUTPUT,
      });
      if (run.error !== undefined)
        throw new Unknown(`git merge-tree could not run: ${run.error.message}`);
      if (run.status === 129 && /unknown option/.test(run.stderr.toString("utf8"))) {
        throw new Unknown("git merge-tree --write-tree needs git 2.38 or newer");
      }
      if (run.status !== 0 && run.status !== 1) {
        throw new Unknown(
          `git merge-tree exited ${run.status ?? run.signal}: ${firstStderrLine(run)}`
        );
      }
      io.print(`merge ${sha}: ${run.status === 0 ? "clean" : "conflict"}`);
      if (run.status === 1) conflicted ??= sha;
    }
    if (conflicted !== undefined) {
      io.print(`runs again: merge ${short(conflicted)} resolved a conflict`);
      return RUNS_AGAIN;
    }

    // The patch test, at each head.
    const before = patchId(from, base, bare);
    io.print(`patch-id at ${from}: ${before}`);
    const after = patchId(to, base, bare);
    io.print(`patch-id at ${to}: ${after}`);
    if (before !== after) {
      io.print(
        `runs again: the pull request's diff changed between ${short(from)} and ${short(to)}`
      );
      return RUNS_AGAIN;
    }
    io.print(
      `carries: no merge between ${short(from)} and ${short(to)} resolved a conflict, and the pull request's diff reads the same at both heads`
    );
    return CARRIES;
  } catch (error) {
    if (!(error instanceof Unknown)) throw error;
    io.print(`UNKNOWN: ${error.message}`);
    return UNKNOWN;
  } finally {
    if (dir !== undefined) rmSync(dir, { recursive: true, force: true });
  }
}
