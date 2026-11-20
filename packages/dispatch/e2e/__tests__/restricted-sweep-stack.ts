/** `bun e2e/__tests__/restricted-sweep-stack.ts <head | base>`, from packages/dispatch: one stack of
 *  the e2e harness (`e2e/harness-pool.ts`, whose Dispatch server `e2e/run-server.sh` starts),
 *  seeded through its own routes, and `e2e/acceptance/restricted-sweep.sh` run against it as bob, a
 *  restricted member whom OWN's access list names and TGT's does not. It prints one JSON line,
 *  what each run answered, and stops the stack however it ends. `restricted-sweep.test.ts` runs it.
 *
 *  - `head` serves this checkout's build. It sweeps TGT, whose objects the seed below holds, and
 *    EMPTY, a project with no issue, which the full member finds nothing of; and it reads the
 *    server's own route probes (`go run ./internal/dispatch/api/cmd/routeprobes`), which the test
 *    holds the sweep's kinds to.
 *  - `base` serves a build of the newest commit with no read check (`noReadCheckBase`), and sweeps
 *    TGT once. That build has no read check, so bob reads every project.
 *
 *  Every write goes through the server's routes as a person or an agent makes it, but two the
 *  stack has no route for: bob's member kind, which the outside sign-in writes and this stack does
 *  not have, and a merged pull request of TGT's repository, which the delivery reconcile writes
 *  from GitHub. Both are written as those writers write them (`restricted-member.e2e.ts` and
 *  `acceptance/project-access.sh` write the same two). */

import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { startStacks, stopStacks } from "../harness-pool";
import { sqlAt } from "../psql";

/** The read check's own table (`readclass` of every route, `read_classes.go`): a tree without it
 *  has no read check. */
const readCheckFile = "packages/envoy/internal/dispatch/api/read_classes.go";
/** The migrations that give a build restricted members, which write `member_kind`. */
const migrations = "packages/envoy/internal/dispatch/store/migrations/*.up.sql";

const bob = "bob@outside.example";
const carol = "carol@example.com";
/** The agent bearer `e2e/run-server.sh` pins. */
const sharedToken = "e2e-token";
/** The session the seed's agent writes name, and the one the fake Envoy lists. */
const worker = "ses-tgt-worker";
/** How long one sweep may run before it is stopped: a run against the local stack takes about 40 s
 *  with no pacing and a 5 s window. */
const sweepMs = 600_000;

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const repositoryRoot = fileURLToPath(new URL("../../../..", import.meta.url));
const sweepScript = join(dispatchPackage, "e2e/acceptance/restricted-sweep.sh");

/** What one sweep answered. */
export interface SweepRun {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** What each child prints, by the build it serves: `head` its sweeps of TGT and EMPTY and the
 *  server's route probes as `routeprobes` printed them, `base` the commit it built and its sweep
 *  of TGT. */
export interface StackRuns {
  readonly head: { readonly target: SweepRun; readonly empty: SweepRun; readonly probes: string };
  readonly base: { readonly base: string; readonly target: SweepRun };
}

/** Whether `value` is a sweep's answer as `SweepRun` spells it. */
function isSweepRun(value: unknown): value is SweepRun {
  return (
    typeof value === "object" &&
    value !== null &&
    "code" in value &&
    typeof value.code === "number" &&
    "stdout" in value &&
    typeof value.stdout === "string" &&
    "stderr" in value &&
    typeof value.stderr === "string"
  );
}

/** Whether `value` is what the child of `which` prints. */
function isRuns<Which extends keyof StackRuns>(
  which: Which,
  value: unknown
): value is StackRuns[Which] {
  if (typeof value !== "object" || value === null) return false;
  if (!("target" in value) || !isSweepRun(value.target)) return false;
  return which === "head"
    ? "empty" in value &&
        isSweepRun(value.empty) &&
        "probes" in value &&
        typeof value.probes === "string"
    : "base" in value && typeof value.base === "string";
}

/** `line`, the last line the child of `which` printed, read back as what that child prints, or
 *  undefined where it is not that: no JSON, or JSON of another shape. */
export function printedRuns<Which extends keyof StackRuns>(
  which: Which,
  line: string
): StackRuns[Which] | undefined {
  let read: unknown;
  try {
    read = JSON.parse(line);
  } catch {
    return undefined;
  }
  return isRuns(which, read) ? read : undefined;
}

/** `git -C repository ARGS`: its exit status and its output. */
function git(repository: string, ...args: string[]): { code: number; out: string; err: string } {
  const done = Bun.spawnSync(["git", "-C", repository, ...args], {
    stderr: "pipe",
    stdout: "pipe",
  });
  return {
    code: done.exitCode,
    err: done.stderr.toString().trim(),
    out: done.stdout.toString().trim(),
  };
}

/** The commit a build with no read check is made from, in `repository`, for this checkout's HEAD.
 *
 *  `override` (DISPATCH_E2E_SWEEP_BASE) names it outright. Otherwise it is read from git, never
 *  written here, so a squash merge that rewrites the history below HEAD strands no commit: it is
 *  the merge base of HEAD and `branch` (DISPATCH_E2E_SWEEP_BASE_BRANCH, the branch the read
 *  check's pull request targets, `origin/main` once the check is on main); and where that merge
 *  base holds the read check already, as main does once the check's pull request has merged, it is
 *  the first parent of the commit of `branch`'s first-parent line that added it, which a squash
 *  merge makes the build just before the check. Whichever it is must hold restricted members (a
 *  migration that writes `member_kind`) and no read check, or this throws, naming it and the two
 *  variables: a base with the check would pass the sweep, and one without restricted members
 *  would refuse the seed, so neither is the control. */
export function noReadCheckBase(
  repository: string,
  override: string | undefined,
  branch: string
): string {
  const variables =
    "Name the build in DISPATCH_E2E_SWEEP_BASE, or the branch the read check's pull request targets in DISPATCH_E2E_SWEEP_BASE_BRANCH";
  const commitOf = (ref: string): string => {
    const read = git(repository, "rev-parse", "--verify", "--quiet", `${ref}^{commit}`);
    if (read.code !== 0 || read.out === "")
      throw new Error(`${ref} names no commit this clone holds. ${variables}.`);
    return read.out;
  };
  const holdsReadCheck = (commit: string) =>
    git(repository, "cat-file", "-e", `${commit}:${readCheckFile}`).code === 0;
  let commit: string;
  let how: string;
  if (override !== undefined && override !== "") {
    commit = commitOf(override);
    how = `DISPATCH_E2E_SWEEP_BASE's ${override}`;
  } else {
    const bases = git(repository, "merge-base", "--all", "HEAD", commitOf(branch));
    const found = bases.code === 0 ? bases.out.split("\n").filter((line) => line !== "") : [];
    const only = found.length === 1 ? found[0] : undefined;
    if (only === undefined) {
      throw new Error(
        `HEAD and ${branch} have ${found.length} merge bases (${found.join(", ") || bases.err}), so no one build is the base. ${variables}.`
      );
    }
    commit = only;
    how = `the merge base of HEAD and ${branch}`;
    if (holdsReadCheck(commit)) {
      const added = git(
        repository,
        "log",
        "--first-parent",
        "--diff-filter=A",
        "--format=%H",
        commit,
        "--",
        readCheckFile
      )
        .out.split("\n")
        .filter((line) => line !== "")
        .at(0);
      if (added === undefined)
        throw new Error(
          `${how}, ${commit}, holds ${readCheckFile}, and no commit of its first-parent line added it. ${variables}.`
        );
      commit = commitOf(`${added}^1`);
      how = `the first parent of ${added}, which added the read check on ${branch}'s first-parent line`;
    }
  }
  if (holdsReadCheck(commit)) {
    throw new Error(
      `${how}, ${commit}, holds the read check (${readCheckFile}), so its build is no control. ${variables}.`
    );
  }
  if (git(repository, "grep", "-l", "-e", "member_kind", commit, "--", migrations).code !== 0) {
    throw new Error(
      `${how}, ${commit}, holds no restricted members (no migration writes member_kind), so the seed cannot make bob one. ${variables}.`
    );
  }
  return commit;
}

type Credential = { readonly cookie: string } | { readonly bearer: string };

/** One request to `origin` as `credential`: a person's cookie with the origin's Origin, as the
 *  dashboard sends it, or a bearer. It refuses any status but `want`, naming what was sent. */
async function send(
  origin: string,
  credential: Credential,
  method: string,
  path: string,
  want: number,
  body?: unknown
): Promise<unknown> {
  const headers: Record<string, string> =
    "cookie" in credential
      ? { Cookie: credential.cookie, Origin: origin }
      : { Authorization: `Bearer ${credential.bearer}` };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(`${origin}${path}`, {
    body: body === undefined ? undefined : JSON.stringify(body),
    headers,
    method,
  });
  const text = await response.text();
  if (response.status !== want) {
    throw new Error(`${method} ${path} answered ${response.status}, want ${want}: ${text}`);
  }
  return text === "" ? undefined : JSON.parse(text);
}

/** Puts `body` at a fake's fixture route, as its own tests seed it. */
async function fixture(port: string, path: string, body: unknown): Promise<void> {
  const response = await fetch(`http://127.0.0.1:${port}${path}`, {
    body: JSON.stringify(body),
    headers: { "Content-Type": "application/json" },
    method: "PUT",
  });
  if (!response.ok)
    throw new Error(`PUT ${path} of the fake on ${port} answered ${response.status}`);
}

/** The session cookie the dev sign-in route mints for `login`. */
async function signIn(origin: string, login: string): Promise<string> {
  const response = await fetch(`${origin}/auth/_dev/signin?login=${encodeURIComponent(login)}`, {
    redirect: "manual",
  });
  const cookie = response.headers
    .getSetCookie()
    .map((line) => line.split(";")[0] ?? "")
    .find((pair) => pair.startsWith("dsession="));
  if (cookie === undefined) {
    throw new Error(
      `the dev sign-in of ${login} answered ${response.status} with no session cookie`
    );
  }
  return cookie;
}

/** The variable `name` of a stack's `env`; it throws, naming it, where the stack carries none,
 *  rather than address a port or a database the stack does not name. */
function stackVariable(env: Readonly<Record<string, string | undefined>>, name: string): string {
  const value = env[name];
  if (value === undefined || value === "") throw new Error(`the stack carries no ${name}`);
  return value;
}

/** Writes, through the stack's server at `origin`, the projects, people and objects every sweep
 *  of this file reads, and answers bob's cookie, carol's personal token and the worker's
 *  conversation file. TGT holds an object of every kind a route names: an issue whose spec has
 *  blocks, a child issue, a comment of carol's and one of an agent's, an ask, a message, a
 *  handoff, a document of the issue and one of the project, a repository, a priority the issue
 *  advances, a broadcast to its agent, a merged pull request and a deploy run, and a file of its
 *  agent's conversation. OWN holds bob's own issue and document. EMPTY holds nothing. */
async function seed(
  origin: string,
  env: Readonly<Record<string, string | undefined>>
): Promise<{
  readonly cookie: string;
  readonly token: string;
  readonly conversationFile: string;
}> {
  const database = stackVariable(env, "DATABASE_URL");
  const asCarol = { cookie: await signIn(origin, carol) };
  const agent = { bearer: sharedToken };
  const actor = { kind: "session", id: worker };
  await signIn(origin, bob);
  await sqlAt(database, [`update people set member_kind = 'restricted' where email = '${bob}'`]);
  const cookie = await signIn(origin, bob);

  for (const key of ["OWN", "TGT", "EMPTY"]) {
    await send(origin, asCarol, "POST", "/api/v1/projects", 201, { key, name: `Project ${key}` });
  }
  await send(
    origin,
    asCarol,
    "PUT",
    `/api/v1/projects/OWN/access/people/${encodeURIComponent(bob)}`,
    200
  );
  await fixture(stackVariable(env, "FAKE_GITHUB_PORT"), "/__fixture/repos", {
    "acme/tgt-repo": { contents: "read", installation_id: 71 },
  });
  await fixture(stackVariable(env, "FAKE_ENVOY_PORT"), "/__fixture/sessions", [
    {
      session_id: worker,
      title: "Works on TGT",
      projects: ["TGT"],
      capabilities: ["aside", "btw", "steer"],
    },
  ]);

  const spec = "# Spec\n\nThe first paragraph.\n";
  await send(origin, asCarol, "POST", "/api/v1/issues", 201, {
    project: "TGT",
    title: "Chart the target reef",
    spec,
  });
  await send(origin, asCarol, "POST", "/api/v1/issues", 201, {
    project: "OWN",
    title: "Chart the own reef",
    spec,
  });
  await send(origin, asCarol, "POST", "/api/v1/issues", 201, {
    project: "TGT",
    title: "A child of the target reef",
    parent: "TGT-1",
  });
  await send(origin, asCarol, "POST", "/api/v1/issues/TGT-1/comments", 201, { body: "A remark." });
  await send(origin, agent, "POST", "/api/v1/issues/TGT-1/comments", 201, {
    body: "An agent remark.",
    actor,
  });
  await send(origin, asCarol, "POST", "/api/v1/issues/TGT-1/asks", 201, {
    question: "Is this the right plan?",
  });
  await send(origin, asCarol, "POST", "/api/v1/issues/TGT-1/messages", 201, {
    body: "A message from carol.",
  });
  await send(origin, agent, "POST", "/api/v1/issues/TGT-1/handoffs/plan", 201, {
    body: JSON.stringify({ next: "seeded" }),
    actor,
  });
  await send(origin, asCarol, "POST", "/api/v1/issues/TGT-1/artifacts", 201, {
    name: "notes.md",
    content: "# Notes\n\nCarol's notes.\n",
  });
  await send(origin, asCarol, "POST", "/api/v1/projects/TGT/artifacts", 201, {
    name: "Playbook.md",
    content: "# Playbook\n\nHow we work.\n",
  });
  // bob's own document, which the document socket's control opens.
  await send(origin, asCarol, "POST", "/api/v1/projects/OWN/artifacts", 201, {
    name: "Guide.md",
    content: "# Guide\n\nHow bob works.\n",
  });
  await send(origin, asCarol, "PATCH", "/api/v1/issues/TGT-1", 200, { repo: "acme/tgt-repo" });
  await send(origin, asCarol, "POST", "/api/v1/priorities", 201, {
    id: "tgt-work",
    title: "The target work",
  });
  await send(origin, asCarol, "PATCH", "/api/v1/issues/TGT-1", 200, { advances: ["tgt-work"] });
  await send(origin, asCarol, "POST", "/api/v1/broadcasts", 201, {
    body: "A broadcast to the worker.",
    delivery: "btw",
    session_ids: [worker],
  });
  await send(origin, asCarol, "PUT", "/api/v1/settings/delivery", 200, {
    deploy_repo: "acme/tgt-repo",
    deploy_workflow_path: ".github/workflows/deploy.yml",
    production_job_names: ["release"],
    pr_checks_workflow_path: ".github/workflows/ci.yml",
    population_authors: ["octocat"],
  });
  // A merged pull request of TGT's repository and a deploy run of it, which the delivery
  // reconcile writes from GitHub and this stack never reaches, written as it writes them.
  await sqlAt(database, [
    "insert into delivery_pull_requests (repo, number, title, url, author, created_at, merged_at, issue_key) " +
      "values ('acme/tgt-repo', 7, 'The change of TGT-1', 'https://github.com/acme/tgt-repo/pull/7', " +
      "'octocat', now() - interval '3 hours', now() - interval '1 hour', 'TGT-1')",
    "insert into delivery_runs (repo, run_id, kind, head_sha, head_commit_at, started_at, created_at, " +
      "completed_at, conclusion, url, head_branch, event) values ('acme/tgt-repo', 900, 'deploy', " +
      "'1111111111111111111111111111111111111111', now() - interval '50 minutes', now() - interval '50 minutes', " +
      "now() - interval '50 minutes', now() - interval '45 minutes', 'success', " +
      "'https://github.com/acme/tgt-repo/actions/runs/900', 'main', 'push')",
    "insert into delivery_run_jobs (repo, run_id, name, started_at, completed_at, conclusion) values " +
      "('acme/tgt-repo', 900, 'release', now() - interval '50 minutes', now() - interval '46 minutes', 'success')",
  ]);
  // A file of the worker's conversation, sent as the agent writes one, which no route lists.
  const form = new FormData();
  form.append("name", "reef-shot.png");
  form.append("actor", JSON.stringify(actor));
  form.append(
    "file",
    new Blob([Buffer.from("\x89PNG\r\n\x1a\nrestricted-sweep")], { type: "image/png" }),
    "reef-shot.png"
  );
  const uploaded = await fetch(`${origin}/api/v1/agents/${worker}/artifacts`, {
    body: form,
    headers: { Authorization: `Bearer ${sharedToken}` },
    method: "POST",
  });
  const file: unknown = await uploaded.json();
  const slug =
    typeof file === "object" &&
    file !== null &&
    "artifact" in file &&
    typeof file.artifact === "object" &&
    file.artifact !== null &&
    "slug" in file.artifact
      ? file.artifact.slug
      : undefined;
  if (uploaded.status !== 201 || typeof slug !== "string") {
    throw new Error(
      `the worker's conversation file answered ${uploaded.status}: ${JSON.stringify(file)}`
    );
  }
  const minted = await send(origin, asCarol, "POST", "/api/v1/me/agent-tokens", 201, {
    name: "restricted-sweep",
  });
  const token =
    typeof minted === "object" && minted !== null && "token" in minted ? minted.token : undefined;
  if (typeof token !== "string") throw new Error("the token mint answered no token");
  return { cookie, token, conversationFile: `${worker}/${slug}` };
}

/** One run of the sweep against `origin` for `project`, unpaced and with a 5 s window, in an
 *  environment of these variables alone. */
async function sweep(
  origin: string,
  project: string,
  credentials: {
    readonly cookie: string;
    readonly token: string;
    readonly conversationFile: string;
  }
): Promise<SweepRun> {
  const child = Bun.spawn(["bash", sweepScript], {
    cwd: dispatchPackage,
    env: {
      PATH: process.env.PATH ?? "",
      HOME: process.env.HOME ?? "",
      TMPDIR: tmpdir(),
      SWEEP_ORIGIN: origin,
      SWEEP_PROJECT: project,
      SWEEP_WRITE_ISSUE: `${project}-1`,
      SWEEP_FULL_TOKEN: credentials.token,
      SWEEP_RESTRICTED_COOKIE: credentials.cookie,
      SWEEP_LIVE_SECONDS: "5",
      SWEEP_CONVERSATION_FILE: credentials.conversationFile,
      SWEEP_PACE_SECONDS: "0",
    },
    stderr: "pipe",
    stdout: "pipe",
  });
  const timer = setTimeout(() => child.kill("SIGKILL"), sweepMs);
  try {
    const [stdout, stderr, code] = await Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ]);
    return { code, stderr, stdout };
  } finally {
    clearTimeout(timer);
  }
}

/** Runs `argv` to its end, and throws naming it and its output unless it exits 0. */
function run(argv: readonly string[], what: string): string {
  const done = Bun.spawnSync([...argv], { stderr: "pipe", stdout: "pipe" });
  if (done.exitCode !== 0) {
    throw new Error(
      `${what}: ${argv.join(" ")} exited ${done.exitCode}: ${done.stderr.toString()}`
    );
  }
  return done.stdout.toString();
}

/** Builds the server of `commit` from its own tree, which `git archive` writes under `scratch`,
 *  and answers the binary's path. */
function buildBase(commit: string, scratch: string): string {
  const tree = join(scratch, "base");
  mkdirSync(tree);
  run(
    [
      "bash",
      "-o",
      "pipefail",
      "-c",
      'git -C "$1" archive "$2" packages/envoy | tar -x -C "$3"',
      "bash",
      repositoryRoot,
      commit,
      tree,
    ],
    `the tree of ${commit}`
  );
  const binary = join(scratch, "envoy-dispatch");
  run(
    ["go", "-C", join(tree, "packages/envoy"), "build", "-o", binary, "./cmd/dispatch"],
    `the server of ${commit}`
  );
  return binary;
}

if (import.meta.main) {
  const which = Bun.argv[2];
  if (which !== "head" && which !== "base") {
    console.error("usage: bun e2e/__tests__/restricted-sweep-stack.ts <head | base>");
    process.exit(2);
  }
  const scratch = mkdtempSync(join(tmpdir(), "restricted-sweep-stack-"));
  try {
    // An empty dashboard: no row here loads a page, and the checkout need not have built one.
    process.env.DISPATCH_E2E_SERVER_WEB_DIST = join(scratch, "web");
    mkdirSync(process.env.DISPATCH_E2E_SERVER_WEB_DIST);
    const base =
      which === "base"
        ? noReadCheckBase(
            repositoryRoot,
            process.env.DISPATCH_E2E_SWEEP_BASE,
            process.env.DISPATCH_E2E_SWEEP_BASE_BRANCH || "origin/main"
          )
        : undefined;
    if (base !== undefined) process.env.DISPATCH_E2E_SERVER_BIN = buildBase(base, scratch);
    const [stack] = await startStacks(1);
    try {
      if (stack === undefined) throw new Error("the harness started no stack");
      const origin = `http://127.0.0.1:${stackVariable(stack.env, "DISPATCH_E2E_PORT")}`;
      const credentials = await seed(origin, stack.env);
      // `base` names the commit the `base` child built, and is undefined for `head` alone.
      const runs: StackRuns[keyof StackRuns] =
        base === undefined
          ? {
              target: await sweep(origin, "TGT", credentials),
              empty: await sweep(origin, "EMPTY", credentials),
              probes: run(
                [
                  "go",
                  "-C",
                  join(repositoryRoot, "packages/envoy"),
                  "run",
                  "./internal/dispatch/api/cmd/routeprobes",
                ],
                "the route probes"
              ),
            }
          : { base, target: await sweep(origin, "TGT", credentials) };
      console.log(JSON.stringify(runs));
    } finally {
      await stopStacks(false);
    }
  } finally {
    rmSync(scratch, { force: true, recursive: true });
  }
}
