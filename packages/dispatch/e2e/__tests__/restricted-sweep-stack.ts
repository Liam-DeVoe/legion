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
 *  - `base` serves the build of `DISPATCH_E2E_SWEEP_BASE`, by default `noReadCheckCommit`, and
 *    sweeps TGT once. That build has no read check, so bob reads every project.
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

/** The merge base of cybermonklabs/legion#4177, which adds the read check: the newest build that
 *  has the access lists and restricted members it reads, and no read check. Once the check is on
 *  main, any commit from before it serves, named in `DISPATCH_E2E_SWEEP_BASE`. */
const noReadCheckCommit = "a6ab11bf56fd6b7150d9028b8cdbcec8e2d33059";

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
  readonly code: number | null;
  readonly stdout: string;
  readonly stderr: string;
}

/** What the child prints: each sweep, and for `head` the server's route probes. */
export interface StackRuns {
  readonly target: SweepRun;
  readonly empty?: SweepRun;
  readonly probes?: string;
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
async function fixture(port: string | undefined, path: string, body: unknown): Promise<void> {
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

/** Writes the projects, people and objects every sweep of this file reads, and answers bob's
 *  cookie and carol's personal token. TGT holds an object of every kind a route names: an issue
 *  whose spec has blocks, a child issue, a comment of carol's and one of an agent's, an ask, a
 *  message, a handoff, a document of the issue and one of the project, a repository, a priority
 *  the issue advances, a broadcast to its agent and a merged pull request. OWN holds bob's own
 *  issue and document. EMPTY holds nothing. */
async function seed(env: Readonly<Record<string, string>>): Promise<{
  readonly cookie: string;
  readonly token: string;
}> {
  const origin = `http://127.0.0.1:${env.DISPATCH_E2E_PORT}`;
  const database = env.DATABASE_URL ?? "";
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
  await fixture(env.FAKE_GITHUB_PORT, "/__fixture/repos", {
    "acme/tgt-repo": { contents: "read", installation_id: 71 },
  });
  await fixture(env.FAKE_ENVOY_PORT, "/__fixture/sessions", [
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
  await sqlAt(database, [
    "insert into delivery_pull_requests (repo, number, title, url, author, created_at, merged_at, issue_key) " +
      "values ('acme/tgt-repo', 7, 'The change of TGT-1', 'https://github.com/acme/tgt-repo/pull/7', " +
      "'octocat', now() - interval '3 hours', now() - interval '1 hour', 'TGT-1')",
  ]);
  const minted = (await send(origin, asCarol, "POST", "/api/v1/me/agent-tokens", 201, {
    name: "restricted-sweep",
  })) as { token?: unknown };
  if (typeof minted.token !== "string") throw new Error("the token mint answered no token");
  return { cookie, token: minted.token };
}

/** One run of the sweep against `origin` for `project`, unpaced and with a 5 s window, in an
 *  environment of these variables alone. */
async function sweep(
  origin: string,
  project: string,
  credentials: { readonly cookie: string; readonly token: string }
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

/** Builds the server of `ref` from its own tree, which `git archive` writes under `scratch`, and
 *  answers the binary's path. */
function buildBase(ref: string, scratch: string): string {
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
      ref,
      tree,
    ],
    `the tree of ${ref} (DISPATCH_E2E_SWEEP_BASE names another commit, one this clone holds)`
  );
  const binary = join(scratch, "envoy-dispatch");
  run(
    ["go", "-C", join(tree, "packages/envoy"), "build", "-o", binary, "./cmd/dispatch"],
    `the server of ${ref}`
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
    if (which === "base") {
      process.env.DISPATCH_E2E_SERVER_BIN = buildBase(
        process.env.DISPATCH_E2E_SWEEP_BASE || noReadCheckCommit,
        scratch
      );
    }
    const [stack] = await startStacks(1);
    try {
      if (stack === undefined) throw new Error("the harness started no stack");
      const credentials = await seed(stack.env);
      const origin = `http://127.0.0.1:${stack.env.DISPATCH_E2E_PORT}`;
      const runs: StackRuns =
        which === "head"
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
          : { target: await sweep(origin, "TGT", credentials) };
      console.log(JSON.stringify(runs));
    } finally {
      await stopStacks(false);
    }
  } finally {
    rmSync(scratch, { force: true, recursive: true });
  }
}
