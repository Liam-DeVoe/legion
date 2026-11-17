import { afterEach, beforeAll, expect, test } from "bun:test";
import {
  appendFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { scriptFunctions } from "../../../../scripts/e2e/lib/script-functions";
import { DAEMON_MODULE, labelGhSelectors, labelGhTable } from "./daemon-pane";
import { readWorld, sentBody } from "./gh-call";
import {
  commandsIn,
  routeOf,
  sweep,
  templateMatches,
  WORLD_ROUTES,
  worldRouteOf,
} from "./gh-routes";
import { type GhSelectors, type GhTable, MODELLED } from "./gh-selectors";
import {
  ghUndeclared,
  guardRefusal,
  loadRun,
  offWorldAllowed,
  offWorldReach,
  workflowUses,
} from "./health";
import { REMOVED_NAMES, WORLD_PROJECT } from "./run-names";
import { bundled, spawnChecked, writeGhSelectors } from "./test-programs";

// GitHub answers the pull request's head as the commit its branch points at, and the worker skill
// has every pushing role read that head after its own push and stop when it is not its commit. A
// stand-in that answers the fixture's first commit for ever stalls every such role: measured on
// 2026-11-06, two runs of the loop that follows an approval pushed their text fixes, read the
// head, found the fixture's
// commit and ended with a WAITING line instead of completing. So the stand-in reads the tip of the
// run's own bare remote at the call, and serves the pull request's diff from it too.
// The rows spawn the stand-in's bundle, built from gh-standin.ts for this suite (test-programs.ts
// says why: a spawn of the source transpiles it on every call).
const STANDIN = bundled(path.join(import.meta.dir, "gh-standin.ts"));
const made: string[] = [];
/** rig.sh's own functions and top-level assignments, read out of the script: the routes every
 * world shares are fixture_write's, built here by running it, as rig.test.ts builds its worlds,
 * so no copy of them lives in this file to drift from the script. */
const RIG = path.join(import.meta.dir, "rig.sh");
const fn = scriptFunctions(RIG);
/** rig.sh's own top-level assignment of `name`, the line as the script writes it. */
function assignment(name: string): string {
  const found = new RegExp(`^${name}=.*$`, "m").exec(readFileSync(RIG, "utf8"));
  if (found === null) throw new Error(`rig.sh assigns no ${name} at the start of a line`);
  return found[0];
}
/** The login every answer of a world names a user by (rig.sh's `world_login`). */
const WORLD_LOGIN = assignment("world_login").replace(/^world_login='(.*)'$/, "$1");

afterEach(() => {
  for (const dir of made.splice(0)) rmSync(dir, { recursive: true, force: true });
});

function git(dir: string, ...args: string[]): string {
  const result = spawnChecked(
    ["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-C", dir, ...args],
    { env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null" } }
  );
  if (result.exitCode !== 0) throw new Error(`git ${args.join(" ")}: ${result.stderr.toString()}`);
  return result.stdout.toString().trim();
}

/** The gh owners directory a worker pane names (daemon-pane.ts's `LEGION_GH_OWNERS_DIR`, the
 * claim's gh bundle under the run's state directory), which the Legion gh wrapper's refusal of an
 * owner outside the project names. */
const PANE_OWNERS = "/run/state/secrets/LWEVAL-1-reviewer-gh";
/** The environment the stand-in answers a call under in the run whose directory is `dir`
 * (GH_DATA_DIR), as the pane of the role `by` names (LEGION_ROLE): this process's own without
 * the role and the GH_REPO a Legion pod's environment may carry, so a call records `by` and no
 * other role and reads no repository of the pod's, with the pane's gh owners directory
 * (PANE_OWNERS) in place of the pod's, and `vars` over it (the GH_REPO a pane names). */
function callEnv(
  dir: string,
  by?: string,
  vars: Record<string, string> = {}
): Record<string, string | undefined> {
  const own = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => name !== "LEGION_ROLE" && name !== "GH_REPO")
  );
  return {
    ...own,
    GH_DATA_DIR: dir,
    LEGION_GH_OWNERS_DIR: PANE_OWNERS,
    ...(by === undefined ? {} : { LEGION_ROLE: by }),
    ...vars,
  };
}

/** The run directory every `runDir` is a copy of: the routes rig.sh's fixture_write writes for
 * every world, run here against a pull request whose head is the first of two commits and whose
 * body is "a body", the world.json it writes, a bare remote of its own whose issue branch holds
 * those two commits, and the gh selectors of this checkout worker_boot writes into a run
 * (`writeGhSelectors`).
 * Nothing fixture_write writes names the directory, so a copy is the run it would have written
 * there. It is built once, in the hook below, because building it is a dozen git and bash
 * processes, which a row that built its own spent before its first call of the stand-in:
 * measured on 2026-11-15 on a 2-CPU pod running two suites of this directory at once, a build
 * took a median of 471 ms (933 ms at most) and a copy 130 ms (154 ms). Each row copies it, so no
 * row reads what another wrote; the property each row checks is the one it checked on a
 * directory of its own. */
const template = temporaryDirectory("gh-standin-template");
const heads = { first: "", second: "" };
beforeAll(() => {
  const work = path.join(template, "work");
  mkdirSync(work);
  git(work, "init", "-q", "-b", "legion/LWEVAL-1");
  writeFileSync(path.join(work, "greet.ts"), "export const greet = () => 1;\n");
  git(work, "add", "-A");
  git(work, "commit", "-qm", "the approved head");
  heads.first = git(work, "rev-parse", "HEAD");
  writeFileSync(path.join(work, "docs.md"), "the text fix\n");
  git(work, "add", "-A");
  git(work, "commit", "-qm", "docs: the text fix");
  heads.second = git(work, "rev-parse", "HEAD");
  git(work, "update-ref", "refs/heads/legion/LWEVAL-1", heads.first);
  const written = spawnChecked(
    [
      "bash",
      "-c",
      [
        fn("fixture_write"),
        fn("concluded_checks"),
        "R=$1 worker_key=LWEVAL-1 worker_repo=example/widgets worker_base=main",
        assignment("world_login"),
        'C1=$2 body="a body" worker_pr=1001 worker_pr_url=https://example.invalid/pull/1001',
        "fixture_write '[]' '\"\"' '[]'",
      ].join("\n"),
      "-",
      template,
      heads.first,
    ],
    { env: { ...process.env } }
  );
  if (written.exitCode !== 0) throw new Error(`fixture_write: ${written.stderr.toString()}`);
  git(template, "clone", "-q", "--bare", work, path.join(template, "remote.git"));
  git(path.join(template, "remote.git"), "update-ref", "refs/heads/legion/LWEVAL-1", heads.first);
  writeGhSelectors(template);
  // Reading the selectors runs gh-selectors.go once, a compile of a few standard packages.
}, 60_000);

/** A run directory of its own, a copy of `template`. With `fixture` it is the run of a scenario
 * that builds a worker world: the world.json fixture_write writes and the bare remote. Without
 * it, it is the run of a scenario that builds none, which has neither — rig.sh writes the world
 * and opens the remote together (worker_fixture), so no run of this rig has one and not the
 * other. No world routes `gh pr diff` (rig.sh's round_world): the diff is the run's own
 * remote's. No world routes the submit of a review either: the stand-in answers that from what
 * the run has already done (gh-standin.ts's reviewSubmission). */
function runDir(options: { fixture: boolean }): { dir: string; first: string; second: string } {
  const dir = mkdtempSync(path.join(tmpdir(), "gh-standin-"));
  made.push(dir);
  cpSync(template, dir, { recursive: true });
  if (!options.fixture) {
    rmSync(path.join(dir, "remote.git"), { recursive: true });
    rmSync(path.join(dir, "world.json"));
  }
  return { dir, ...heads };
}

const headOf = (dir: string) =>
  spawnChecked(
    ["bun", STANDIN, "gh", "pr", "view", "1001", "--json", "headRefOid", "--jq", ".headRefOid"],
    {
      env: callEnv(dir),
    }
  )
    .stdout.toString()
    .trim();

/** `gh api --method POST <route>` with `rest` after it, in the run under `dir`: the create of a
 * review and the submit of a pending one, as the reviewer's prompt makes them. */
const apiPost = (dir: string, route: string, ...rest: string[]) =>
  spawnChecked(["bun", STANDIN, "gh", "api", "--method", "POST", route, ...rest], {
    env: callEnv(dir),
  });
const REVIEWS = "repos/example/widgets/pulls/1001/reviews";
/** The create of a pending review: a body naming no `event`, which is what leaves one. */
function createPending(dir: string, review: object): void {
  const body = path.join(dir, "pending.json");
  writeFileSync(body, JSON.stringify(review));
  expect(apiPost(dir, REVIEWS, "--input", body).exitCode).toBe(0);
}

// Catches: a world that invents an approval at no head. GitHub's `POST …/pulls/<n>/reviews/<id>/
// events` submits a review that is still pending: an already-submitted id answers 422, and a
// pending one answers the review carrying the head it was left at. The rig's create route matched
// that path too and answered `{id, html_url}` — a submission with no `commit_id` — and the score,
// which reads the last submission as the one that stands, failed a run whose reviewer had
// approved correctly at the head its push made (the text-fixes head arm of 2026-11-11, one run of
// fifty-six). The artefact moves a measurement both ways: it failed that run, and in the defect
// control it passes one, since a reviewer that approved wrongly and then sent `REQUEST_CHANGES`
// to this endpoint had that change request answered as a submission and read as the verdict that
// stands — the one verdict that control passes.
test("a submit of a review that is no longer pending is refused, as GitHub refuses it", () => {
  const { dir, first } = runDir({ fixture: true });
  const body = path.join(dir, "review.json");
  writeFileSync(body, JSON.stringify({ commit_id: first, event: "APPROVE", body: "approved" }));
  expect(apiPost(dir, REVIEWS, "--input", body).exitCode).toBe(0);
  const again = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(again.exitCode).toBe(1);
  expect(again.stderr.toString()).toContain("422");
  expect(again.stdout.toString()).not.toContain("6001");
});

// Catches: a stand-in that refuses every submit, including the one GitHub answers; one that asks
// for a review id no world of this rig gives, since review_world answers a create 6001 and
// approved_world 5002; and an answer carrying the state the review had rather than the state it
// was submitted in. A review created with no `event` is pending, and the submit of that one
// answers the review itself, carrying the head it was created at and the state GitHub leaves it
// in — which is what keeps the two-step route an agent may take readable as the submission it is.
test("a submit of the run's pending review answers its id, its head and the state submitted", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=REQUEST_CHANGES");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "CHANGES_REQUESTED",
  });
});

// Catches: a stand-in that leaves a review pending after the call that submitted it, so a round
// that submitted the same review twice is answered 200 twice and the score reads two
// submissions where GitHub recorded one. A submitted review is no longer pending.
test("a second submit of the review this run submitted is refused", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  expect(apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE").exitCode).toBe(0);
  const again = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(again.exitCode).toBe(1);
  expect(again.stderr.toString()).toContain("Can not submit a review that is not pending.");
});

// Catches: a create's body read one way here and another by the score, which is how a create the
// score counted as a pending review left this stand-in refusing the call that submitted it. One
// parser reads both (score.ts's `sentBody`), so a body written as fields leaves the pending
// review a body written into a file does; and a create naming no `commit_id` is pending at the
// pull request's own head, which is the head GitHub creates a review at.
test("a create's fields leave a pending review, and one naming no head takes the pull request's", () => {
  const written = runDir({ fixture: true });
  expect(apiPost(written.dir, REVIEWS, "-f", `commit_id=${written.first}`).exitCode).toBe(0);
  const submit = apiPost(written.dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toMatchObject({ commit_id: written.first });
  const headless = runDir({ fixture: true });
  createPending(headless.dir, { body: "two findings" });
  const atHead = apiPost(headless.dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(atHead.exitCode).toBe(0);
  expect(JSON.parse(atHead.stdout.toString())).toMatchObject({ commit_id: headless.first });
});

// `gh api --help`: with `--input`, the field flags go to the query string, so the body of this
// create is the file alone and `-f event=APPROVE` is a query parameter. GitHub leaves the
// review pending, and so does this stand-in. Catches: a reader that merges the two, which left
// no pending review here and refused the call that submitted it 422, while the score read the
// same call as an approval — a round told its review was pending by GitHub and submitted by the
// row that scored it.
test("a create's --input is its whole body, and the fields beside it are the query string", () => {
  const { dir, first } = runDir({ fixture: true });
  const body = path.join(dir, "pending.json");
  writeFileSync(body, JSON.stringify({ commit_id: first, body: "two findings" }));
  const created = apiPost(dir, REVIEWS, "--input", body, "-f", "event=APPROVE");
  expect(created.exitCode).toBe(0);
  expect(JSON.parse(created.stdout.toString())).toMatchObject({ state: "PENDING" });
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "APPROVED",
  });
});

// `--input=<file>` is the same flag written against its value, which gh takes as it takes
// `--input <file>`. Catches: a recorder that reads its flags by exact match, which stored the
// file of the second spelling and not of the first, so the body reader found nothing under the
// name the call gave and read this create as one that named no head — a pending review at the
// pull request's own tip rather than at the commit the body names, and a submit answered with
// an approval at a head the reviewer never approved.
test("a create whose --input is written against the flag is read from that file too", () => {
  const { dir, second } = runDir({ fixture: true });
  const body = path.join(dir, "pending.json");
  // A commit the pull request's branch does not point at, so a create read as one that named no
  // head is answered the tip instead and this row fails.
  writeFileSync(body, JSON.stringify({ commit_id: second, body: "two findings" }));
  expect(apiPost(dir, REVIEWS, `--input=${body}`).exitCode).toBe(0);
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toEqual({
    id: 6001,
    commit_id: second,
    state: "APPROVED",
  });
});

// A create that sends no body at all creates a review with nothing in it on GitHub, which is a
// pending review, and the call that submits it is answered 200. Catches: a stand-in that reads
// a bodyless create as no create, which recorded no pending review for it and refused that
// submit 422 — a refusal GitHub does not make, which is the same defect as an invented success.
test("a create with no body leaves a pending review, at the pull request's own head", () => {
  const { dir, first } = runDir({ fixture: true });
  const created = apiPost(dir, REVIEWS);
  expect(created.exitCode).toBe(0);
  // And the create is answered with that review as GitHub holds it: pending, at the head the
  // pull request was at, which the body named none of.
  expect(JSON.parse(created.stdout.toString())).toMatchObject({
    id: 6001,
    state: "PENDING",
    commit_id: first,
  });
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "APPROVED",
  });
});

// What GitHub answers a create with is the review it created, carrying the state it is in, the
// head it was created at and its body: `PENDING` where the create named no event, and the state
// the event leaves it in where it named one; `""` for a create that sent no body. Catches: a
// world's frozen `{id, html_url}` answered as it stands, which told a reviewer nothing about
// whether its approval had been submitted — measured in the threads-closed head arm, where a round
// read that answer back and submitted a second time — and a create answered without its body,
// which left a reviewer reading its own footer back on null and exiting 1
// (round17-forty/reviewer-text-findings-defect-head-4/calls.jsonl:29).
test("a create is answered with the review's state and the head it was created at", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const pending = apiPost(dir, REVIEWS, "-f", `commit_id=${second}`);
  expect(JSON.parse(pending.stdout.toString())).toEqual({
    id: 6001,
    html_url: "https://example.invalid/pull/1001#pullrequestreview-6001",
    user: { login: WORLD_LOGIN },
    state: "PENDING",
    commit_id: second,
    body: "",
  });
  const approved = apiPost(dir, REVIEWS, "-f", `commit_id=${first}`, "-f", "event=APPROVE");
  expect(JSON.parse(approved.stdout.toString())).toMatchObject({
    state: "APPROVED",
    commit_id: first,
  });
});

// Catches: a reader of the run's record that takes its opening line for a call. The record a
// submit reads is never empty — `openRecord` writes `{at, opened}` into it before any call of
// the run is answered — and that line carries no `argv` at all: a reader of `call.argv[0]` with
// no fallback throws a TypeError on it, which is a stack trace where the agent expected GitHub's
// own refusal. A run whose only line is that one has created no review, so it has none pending,
// and the submit is refused with the 422 and nothing else on stderr.
test("a submit that is the run's first gh call is refused, not thrown", () => {
  const { dir } = runDir({ fixture: true });
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(1);
  expect(submit.stderr.toString()).toBe(
    "gh: Unprocessable Entity (HTTP 422)\nCan not submit a review that is not pending.\n"
  );
  const recorded = readFileSync(path.join(dir, "calls.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line) as Record<string, unknown>);
  expect(recorded[0]).toMatchObject({ opened: "calls.jsonl" });
  expect(recorded[0]).not.toHaveProperty("argv");
  expect(recorded).toHaveLength(2);
});

/** `gh <args…>` in the run under `dir`, with its status, its stdout and its stderr: the rows
 * below read a refusal's own text as well as the answer. */
const ghCall = (dir: string, ...args: string[]) => {
  const made = spawnChecked(["bun", STANDIN, "gh", ...args], {
    env: callEnv(dir),
  });
  return {
    code: made.exitCode,
    out: made.stdout.toString().trim(),
    err: made.stderr.toString(),
  };
};

// The rows below are the stand-in's own behaviour, varied in a run directory this file writes.
// rig.test.ts asks the same questions of the worlds rig.sh builds, through their real routes in
// the order the stand-in matches them, and neither row is a copy of the other: a refusal that
// lives here can still be answered ahead of by a world's route, which is how this endpoint came
// to be answered `{id:6001}` with no head on it.
// Catches: a stand-in that answers whatever id a submit names once the run has a review
// pending, which would hand a round that submitted another review an approval at the head the
// pending one was created at — a review GitHub never wrote. The id is the one the create's own
// answer carried, and GitHub answers every other 422.
test("a submit of an id that is not the pending review's is refused", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const other = apiPost(dir, `${REVIEWS}/5003/events`, "-f", "event=APPROVE");
  expect(other.exitCode).toBe(1);
  expect(other.stderr.toString()).toContain("Can not submit a review that is not pending.");
  expect(other.stdout.toString()).toBe("");
  // And the review the run did leave pending is still pending after that refusal.
  const submitted = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submitted.exitCode).toBe(0);
  expect(JSON.parse(submitted.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "APPROVED",
  });
});

// GitHub keeps one pending review per user and pull request, and refuses the create of a second
// while one stands: 422, "User can only have one pending review per pull request". Catches: a
// stand-in that takes the second create and makes it the pending review, which answered a round
// that read the head again and created a second review 200, and then submitted the review
// GitHub never created, at the head the second create named. The first stays the pending one
// until it is submitted, and once it is the run has none and may create another.
test("a second pending create while one stands is refused, and the first stays the pending one", () => {
  const { dir, first, second } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const again = ghCall(
    dir,
    "api",
    "--method",
    "POST",
    REVIEWS,
    "-f",
    `commit_id=${second}`,
    "-f",
    "body=a second reading"
  );
  expect(again).toEqual({
    code: 1,
    out: "",
    err: "gh: Unprocessable Entity (HTTP 422)\nUser can only have one pending review per pull request\n",
  });
  const submitted = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=REQUEST_CHANGES");
  expect(submitted.exitCode).toBe(0);
  expect(JSON.parse(submitted.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "CHANGES_REQUESTED",
  });
  expect(apiPost(dir, REVIEWS, "-f", `commit_id=${second}`).exitCode).toBe(0);
});

/** The stand-in's own sentences for the two creates GitHub refuses 422 on their body alone
 * (gh-standin.ts's CREATE_EVENT and CREATE_BODY). */
const CREATE_EVENT =
  "gh: Unprocessable Entity (HTTP 422)\nA review is created with the event APPROVE, REQUEST_CHANGES or COMMENT, or with none to leave it pending.\n";
const CREATE_BODY =
  "gh: Unprocessable Entity (HTTP 422)\nA review with the event REQUEST_CHANGES or COMMENT is created with a body.\n";

// GitHub's create takes the events APPROVE, REQUEST_CHANGES and COMMENT, or none, and requires a
// body for the second and third (gh-call.ts's `createdState`). Catches: a stand-in that answers
// any of these 200 with a review — an `approve` or a `PENDING` answered `{id, html_url}` and exit
// 0, where GitHub answers 422 — and one that leaves a pending review behind a refused create, so
// the submit after it was answered with a review GitHub never had. An APPROVE needs no body and a
// COMMENT with one is taken. An event that is no string — a number, sent in an `--input` file —
// and one GitHub has no name for (`DISMISS`) are refused as `approve` is, and none of them leaves
// the pending review a create with no event leaves.
test("a create GitHub refuses is refused 422, and leaves no review", () => {
  const { dir, first } = runDir({ fixture: true });
  const body = path.join(dir, "changes.json");
  writeFileSync(body, JSON.stringify({ commit_id: first, event: "REQUEST_CHANGES" }));
  const numbered = path.join(dir, "numbered.json");
  writeFileSync(numbered, JSON.stringify({ commit_id: first, event: 1 }));
  const refused = [
    [["-f", "event=approve", "-f", "body=looks good"], CREATE_EVENT],
    [["-f", "event=PENDING"], CREATE_EVENT],
    [["-f", "event=DISMISS"], CREATE_EVENT],
    [["--input", numbered], CREATE_EVENT],
    [["-f", "event=REQUEST_CHANGES"], CREATE_BODY],
    [["-f", "event=COMMENT", "-f", "body="], CREATE_BODY],
    [["--input", body], CREATE_BODY],
  ] as const;
  for (const [fields, refusal] of refused)
    expect([fields, ghCall(dir, "api", "--method", "POST", REVIEWS, ...fields)]).toEqual([
      fields,
      { code: 1, out: "", err: refusal },
    ]);
  expect(recordedCalls(dir).map((call) => call.review)).toEqual(
    Array(refused.length).fill(undefined)
  );
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(1);
  expect(submit.stderr.toString()).toContain("Can not submit a review that is not pending.");
  const commented = ghCall(dir, "api", REVIEWS, "-f", "event=COMMENT", "-f", "body=a reading");
  expect([commented.code, JSON.parse(commented.out).state]).toEqual([0, "COMMENTED"]);
  const approved = ghCall(dir, "api", REVIEWS, "-f", "event=APPROVE");
  expect([approved.code, JSON.parse(approved.out).state]).toEqual([0, "APPROVED"]);
});

// GitHub's create reads a blank event as none: "by leaving this blank, you set the review action
// state to PENDING". Catches: an `event: ""` read as an event GitHub does not take — the stand-in
// answered it with no state and left no pending review, so the submit after it was refused 422,
// while the score read the same create as pending (gh-call.ts's `createdState`, which both read).
test("a create whose event is empty is pending, as one naming none is", () => {
  const { dir, first } = runDir({ fixture: true });
  const created = ghCall(dir, "api", REVIEWS, "-f", "event=", "-f", `commit_id=${first}`);
  expect(created.code).toBe(0);
  expect(JSON.parse(created.out)).toMatchObject({ state: "PENDING", commit_id: first });
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "APPROVED",
  });
});

// GitHub answers `GET …/pulls/<n>/reviews/<id>` with that review as it stands. Catches: the read
// of a review the run created answered by the routes — the list of reviews, or `[]` — which a
// round reading its own review back with `--jq '{id,state,commit_id}'` got as `[]` and exit 1
// (`text-only-head-3` and `defect-base-3` of review 5887659447); and a read that answers the
// state the review was created in after it was submitted. The user and the URL are the world's
// answer to the create; the state, the head and the body are the run's. An id this run did not
// create, and the world does not hold, is GitHub's 404.
test("a GET of a review this run created answers that review as it stands", () => {
  const { dir, first } = runDir({ fixture: true });
  const read = () =>
    ghCall(dir, "api", `${REVIEWS}/6001`, "--jq", "{id,state,commit_id,user:.user.login,body}");
  createPending(dir, { commit_id: first, body: "two findings" });
  expect(JSON.parse(read().out)).toEqual({
    id: 6001,
    state: "PENDING",
    commit_id: first,
    user: WORLD_LOGIN,
    body: "two findings",
  });
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE", "-f", "body=ok");
  expect(submit.exitCode).toBe(0);
  expect([read().code, JSON.parse(read().out)]).toEqual([
    0,
    { id: 6001, state: "APPROVED", commit_id: first, user: WORLD_LOGIN, body: "ok" },
  ]);
  expect(ghCall(dir, "api", `${REVIEWS}/5001`)).toEqual({
    code: 1,
    out: "",
    err: "gh: Not Found (HTTP 404)\n",
  });
});

// gh reads a `-F key=@path` field's value out of the file at `path`, and the review GitHub keeps
// carries that text. Catches: a body reader that keeps `@path` as the value — the review read
// back would carry the file's name as its body.
test("a create's -F body=@file carries the file's text", () => {
  const { dir, first } = runDir({ fixture: true });
  const notes = path.join(dir, "notes.md");
  writeFileSync(notes, "two findings\n");
  const created = ghCall(dir, "api", REVIEWS, "-f", `commit_id=${first}`, "-F", `body=@${notes}`);
  expect(created.code).toBe(0);
  expect(ghCall(dir, "api", `${REVIEWS}/6001`, "--jq", ".body")).toEqual({
    code: 0,
    out: "two findings",
    err: "",
  });
});

// gh sends `key[]=value`, `key[sub]=value` and `key[][sub]=value` fields as the nested body
// `gh api --help` describes, and a `-F key=@path` field as the text of the file at `path` (its
// stdin for `@-`), which the stand-in records beside the call. This is the reader both the
// stand-in and the score read a create's body with (gh-call.ts's `sentBody`). Catches: a
// reader that keeps `comments[][path]` as a flat key, which scored a review whose inline
// comments were written as fields `doc=false workflow=false`, and one that reads `@path` as the
// value. A `-f`/`--raw-field` value is sent as written, a file the record does not hold stays
// as written too, and a value holding `=` is everything after the first one: a reader that cut
// it at the second sent `a` for `sum=a=b`.
test("a body written as fields is gh's: nested keys, arrays of objects, and -F key=@file as the file's text", () => {
  const comments = [
    "-f",
    "comments[][path]=docs/cli.md",
    "-f",
    "comments[][line]=5",
    "--raw-field",
    "comments[][body]=false sentence",
    "--field",
    "comments[][path]=.github/workflows/greet.yml",
    "-f",
    "comments[][body]=wrong trigger",
  ];
  expect(
    sentBody({
      argv: [
        "api",
        REVIEWS,
        ...comments,
        "-F",
        "body=@notes.md",
        "-f",
        "labels[]=a",
        "-f",
        "labels[]=b",
        "-f",
        "empty[]",
        "-f",
        "nested[key]=v",
        "-f",
        "raw=@notes.md",
        "-F",
        "gone=@gone.md",
        "-f",
        "sum=a=b",
      ],
      files: { "notes.md": "two findings\n" },
    })
  ).toEqual({
    comments: [
      { path: "docs/cli.md", line: "5", body: "false sentence" },
      { path: ".github/workflows/greet.yml", body: "wrong trigger" },
    ],
    body: "two findings\n",
    labels: ["a", "b"],
    empty: [],
    nested: { key: "v" },
    raw: "@notes.md",
    gone: "@gone.md",
    sum: "a=b",
  });
  expect(sentBody({ argv: ["api", REVIEWS, "-F", "body=@-"], stdin: "from stdin" })).toEqual({
    body: "from stdin",
  });
});

// GitHub answers `PATCH repos/<o>/<r>/pulls/<n>` with the pull request as the PATCH left it.
// Catches: the generic write route's `{id, html_url}` answered to it, which 23 recorded PATCH
// calls read `.body` back from as null and one round exited 1 on (review 5887659447, finding 6);
// and an answer carrying the body from before the edit, since the routes are read before it.
test("a PATCH of the pull request's body answers the pull request, carrying the body it left", () => {
  const { dir, first } = runDir({ fixture: true });
  const pull = "repos/example/widgets/pulls/1001";
  const patched = ghCall(dir, "api", "-X", "PATCH", pull, "-f", "body=the fixed body");
  expect(patched.code).toBe(0);
  expect(JSON.parse(patched.out)).toMatchObject({
    number: 1001,
    headRefOid: first,
    body: "the fixed body",
  });
  const input = path.join(dir, "body.json");
  writeFileSync(input, JSON.stringify({ body: "from a file" }));
  expect(ghCall(dir, "api", "--method=patch", pull, "--input", input, "--jq", ".body")).toEqual({
    code: 0,
    out: "from a file",
    err: "",
  });
  expect(ghCall(dir, "pr", "view", "1001", "--json", "body", "--jq", ".body").out).toBe(
    "from a file"
  );
});

// Every body edit gh takes leaves the pull request that body, and the call's record carries it
// as `body`, which is what the score reads the round's body from (score.ts's threadsClosed takes
// the last call that kept one): the PATCH pr-body.md prescribes, `-F body=@body.md`, among them.
// Each spelling is a run of its own, so a spelling the stand-in reads as no edit leaves the body
// the world began with. The text holds an `=`, which a field's value keeps whole. Catches: the
// PATCH read under another verb, or only with or only without the leading slash; a `-F` or
// `--field` value of `@path` kept as the text `@path`; a `-f` one read out of the file it names,
// where gh sends it as written; `--input` read in one spelling of the flag; and each `gh pr
// edit` flag dropped, or read in one spelling of it — `--body-file` read as `--body` with the
// text `-file` among them. Any of these leaves a round that followed the rule with no fixed
// body, and its threads line fails.
test("every body edit gh takes leaves the pull request that body, and the record carries it", () => {
  const text = "the body after the edit, where a=b";
  const pull = "repos/example/widgets/pulls/1001";
  for (const [name, edit, wanted] of [
    ["the prescribed PATCH", (md) => ["api", "--method", "PATCH", pull, "-F", `body=@${md}`], text],
    [
      "-X PATCH, leading slash",
      () => ["api", "-X", "PATCH", `/${pull}`, "-f", `body=${text}`],
      text,
    ],
    ["--method=patch --input", (_, json) => ["api", "--method=patch", pull, "--input", json], text],
    ["--input=", (_, json) => ["api", "--method", "PATCH", pull, `--input=${json}`], text],
    ["--field @", (md) => ["api", "-X", "PATCH", pull, "--field", `body=@${md}`], text],
    ["--field= @", (md) => ["api", "-X", "PATCH", pull, `--field=body=@${md}`], text],
    ["-f @ as written", (md) => ["api", "-X", "PATCH", pull, "-f", `body=@${md}`], "@"],
    ["pr edit --body", () => ["pr", "edit", "1001", "--body", text], text],
    ["pr edit --body=", () => ["pr", "edit", "1001", `--body=${text}`], text],
    ["pr edit -b", () => ["pr", "edit", "1001", "-b", text], text],
    ["pr edit -b=", () => ["pr", "edit", "1001", `-b=${text}`], text],
    ["pr edit -F", (md) => ["pr", "edit", "1001", "-F", md], text],
    ["pr edit --body-file", (md) => ["pr", "edit", "1001", "--body-file", md], text],
    ["pr edit --body-file=", (md) => ["pr", "edit", "1001", `--body-file=${md}`], text],
  ] as [string, (md: string, json: string) => string[], string][]) {
    const { dir } = runDir({ fixture: true });
    const md = path.join(dir, "body.md");
    writeFileSync(md, text);
    const json = path.join(dir, "body.json");
    writeFileSync(json, JSON.stringify({ body: text }));
    const kept = wanted === "@" ? `@${md}` : wanted;
    const edited = ghCall(dir, ...edit(md, json));
    const read = ghCall(dir, "pr", "view", "1001", "--json", "body", "--jq", ".body");
    expect([name, edited.code, read.out, recordedCalls(dir)[0]?.body]).toEqual([
      name,
      0,
      kept,
      kept,
    ]);
  }
  // Fourteen runs, each a world and two stand-in calls: longer than bun's five-second default.
}, 60_000);

// A `body` field on a call that is no PATCH of the pull request edits nothing: a POST of that
// path is another call, and GitHub keeps the body. Catches: the edit read off the path and the
// field whatever the verb, which would leave a round that posted a comment there reading its
// comment back as the pull request's body.
test("a body field on a POST of the pull request edits no body", () => {
  const { dir } = runDir({ fixture: true });
  const posted = ghCall(
    dir,
    "api",
    "-X",
    "POST",
    "repos/example/widgets/pulls/1001",
    "-f",
    "body=x"
  );
  expect(posted.code).toBe(0);
  expect(ghCall(dir, "pr", "view", "1001", "--json", "body", "--jq", ".body").out).toBe("a body");
  expect(recordedCalls(dir)[0]).not.toHaveProperty("body");
});

// gh hands a comment's or a review's text over in a file with `--body-file` too, and the record
// keeps that file's text beside the call, which is where the score reads what a comment says
// (score.ts's `carriesField`). Catches: a recorder that stores the file of `--input` alone, or
// reads `--body-file` in one spelling of it, so a comment written into a file reads as a call
// that said nothing.
test("a comment's --body-file is recorded with the file's text, in either spelling", () => {
  for (const flag of [
    (file: string) => ["--body-file", file],
    (file: string) => [`--body-file=${file}`],
  ]) {
    const { dir } = runDir({ fixture: true });
    const file = path.join(dir, "comment.md");
    writeFileSync(file, "the reply, which names the fix");
    expect(ghCall(dir, "pr", "comment", "1001", ...flag(file)).code).toBe(0);
    expect(recordedCalls(dir)[0]?.files).toEqual({ [file]: "the reply, which names the fix" });
  }
});

// The verb decides what the call is, in every spelling gh takes it in and by the reader the
// score reads the same call with (score.ts's `isPost`): a call naming GET is a read of that
// path whatever its fields say, so it submits nothing, and GitHub has no read there, so it is
// GitHub's 404; and the review it did not submit is still pending for the call that does, which
// is written here with the leading slash `gh api` takes either way. Catches: a GET answered as
// the submission of the pending review, which gh never sent and GitHub never answers; a GET read
// as the submit that leaves no pending review behind it, where the submit after it would be
// refused 422; and a submit read as none for its leading slash, which the generic POST route
// would then answer `{id:6001}` with no head on it — the approval at no head this endpoint exists
// to stop.
test("a GET of the submit route submits nothing, and a submit with a leading slash submits", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  for (const verb of [["-X", "GET"], ["--method=GET"], ["-XGET"]]) {
    const read = ghCall(dir, "api", ...verb, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
    expect(read.code).toBe(1);
    expect(read.out).toBe("");
    expect(read.err).toBe("gh: Not Found (HTTP 404)\n");
  }
  const submitted = ghCall(
    dir,
    "api",
    "--method",
    "POST",
    `/${REVIEWS}/6001/events`,
    "-f",
    "event=APPROVE"
  );
  expect(submitted.code).toBe(0);
  expect(JSON.parse(submitted.out)).toEqual({ id: 6001, commit_id: first, state: "APPROVED" });
});
// The submit written `-XPOST`, the spelling gh takes with the verb against the flag. Catches: a
// verb read in the spellings a row happened to use, where a reviewer that submitted its review
// this way would be answered from the routes — `{id:6001}` with no head on it — and leave the
// review pending behind the approval it had just made.
test("a submit written -XPOST is the submit --method POST is", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const submitted = ghCall(dir, "api", "-XPOST", `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submitted.code).toBe(0);
  expect(JSON.parse(submitted.out)).toEqual({ id: 6001, commit_id: first, state: "APPROVED" });
});
// A call on the submit path that gh does not send as a POST submits nothing: `-X PUT` names
// another verb, and a call with no method and no field is the GET gh sends by default. The first
// falls to the route every write falls to, and the second is GitHub's 404, since GitHub has no
// read there; the review stays pending for the call that does submit it. Catches: a verb read as
// "anything but GET", which would answer the PUT as the round's submission, and one read as "a
// POST unless it says otherwise", which would answer the bare call that way.
test("a PUT of the submit route and a bare one submit nothing", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const put = ghCall(dir, "api", "-X", "PUT", `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(put.code).toBe(0);
  expect(JSON.parse(put.out)).toEqual({
    id: 6001,
    html_url: "https://example.invalid/pull/1001#issuecomment-6001",
  });
  const bare = ghCall(dir, "api", `${REVIEWS}/6001/events`);
  expect(bare.code).toBe(1);
  expect(bare.err).toBe("gh: Not Found (HTTP 404)\n");
  const submitted = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submitted.exitCode).toBe(0);
  expect(JSON.parse(submitted.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "APPROVED",
  });
});
// GitHub takes `event` on a submit and refuses the call without one, and refuses an event it
// does not know; the review stays pending either way, since nothing was submitted. Catches: a
// stand-in that leaves both to the routes, where the route every write falls to answered
// `{id:6001}` and exit 0 — a round told its review was submitted when GitHub had refused the
// call — and one that reads the body of a submit that sent none at all, which threw a
// TypeError in place of an answer. The third call is a COMMENT submit, the event whose state
// is neither of the two verdicts.
test("a submit with no event, or an event GitHub does not take, is refused and leaves the review pending", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const refusal =
    "gh: Unprocessable Entity (HTTP 422)\nA review is submitted with the event APPROVE, REQUEST_CHANGES or COMMENT.\n";
  const bodyless = ghCall(dir, "api", "--method", "POST", `${REVIEWS}/6001/events`);
  expect(bodyless.code).toBe(1);
  expect(bodyless.err).toBe(refusal);
  expect(bodyless.out).toBe("");
  const unknown = ghCall(
    dir,
    "api",
    "--method",
    "POST",
    `${REVIEWS}/6001/events`,
    "-f",
    "event=DISMISS"
  );
  expect(unknown.code).toBe(1);
  expect(unknown.err).toBe(refusal);
  // Each refused call is recorded, as every call the stand-in answers is.
  expect(
    readFileSync(path.join(dir, "calls.jsonl"), "utf8")
      .split("\n")
      .filter((line) => line !== "")
  ).toHaveLength(4);
  const commented = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=COMMENT");
  expect(commented.exitCode).toBe(0);
  expect(JSON.parse(commented.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "COMMENTED",
  });
});
// Catches: a stand-in that reads the status a submit was recorded with for whether GitHub took
// it. `--jq` runs over the answer this stand-in had already given, so a filter that fails
// leaves the call recorded with exit 1 and the review submitted all the same; a reader of that
// status left it pending, and the next submit of the same review was answered 200 a second
// time, where GitHub answers 422.
test("a submit whose --jq failed submitted the review all the same", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  const submitted = ghCall(
    dir,
    "api",
    "--method",
    "POST",
    `${REVIEWS}/6001/events`,
    "-f",
    "event=APPROVE",
    "--jq",
    ".[0]"
  );
  expect(submitted.code).toBe(1);
  expect(submitted.err).toContain("Cannot index object with number");
  const again = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(again.exitCode).toBe(1);
  expect(again.stderr.toString()).toContain("Can not submit a review that is not pending.");
});
// The review endpoints are the run's own repository's, as the score's readers are. Catches: a
// stand-in that reads a create of `other/widgets` as this run's pending review, which would
// answer the submit of a review this world never got with a review object and 200, and one that
// answers a submit on another repository's pull request from this run's record. Production's gh
// never reaches GitHub for either: the Legion gh wrapper refuses an owner outside the project,
// exit 2, and so does the world's.
test("a create and a submit on another repository's pull request are no review of this run", () => {
  const { dir, first } = runDir({ fixture: true });
  const other = "repos/other/widgets/pulls/1001/reviews";
  expect(apiPost(dir, other, "-f", `commit_id=${first}`).exitCode).toBe(2);
  const elsewhere = apiPost(dir, `${other}/6001/events`, "-f", "event=APPROVE");
  expect([elsewhere.exitCode, elsewhere.stdout.toString()]).toEqual([2, ""]);
  expect(elsewhere.stderr.toString()).toContain('"other" is not an owner of this project');
  const ours = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(ours.exitCode).toBe(1);
  expect(ours.stderr.toString()).toContain("Can not submit a review that is not pending.");
});
// `gh pr list --open` is a call of the run, not rig.sh asking for the record's opening line.
// That ask is `gh --open`, the flag alone after the program word the wrapper inserts, and a
// stand-in that took the flag wherever it fell — or at the end of a call of any length —
// answered this listing with nothing, recorded no call, and left the score reading a role that
// looked for its pull request as one that made no call at all.
test("gh pr list --open is a call of the run and no ask for the opening line", () => {
  const { dir, first } = runDir({ fixture: true });
  const listed = ghCall(dir, "pr", "list", "--open");
  expect(listed.code).toBe(0);
  // The listing itself, read before it is parsed, so a stand-in that answered nothing fails on
  // this assertion rather than on the parse.
  expect(listed.out).toStartWith("[");
  expect(JSON.parse(listed.out)).toMatchObject([
    { number: 1001, headRefOid: first, head: { sha: first }, body: "a body" },
  ]);
  const recorded = readFileSync(path.join(dir, "calls.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line) as Record<string, unknown>);
  expect(recorded).toHaveLength(2);
  expect(recorded[1]).toMatchObject({ argv: ["pr", "list", "--open"], exit: 0 });
});
// Catches: a stand-in that answers the fixture's commit after the role pushed, so every pushing
// role reads a head that is not its own and stops.
test("the pull request's head is the tip of the run's own remote", () => {
  const { dir, first, second } = runDir({ fixture: true });
  expect(headOf(dir)).toBe(first);
  git(path.join(dir, "remote.git"), "update-ref", "refs/heads/legion/LWEVAL-1", second);
  expect(headOf(dir)).toBe(second);
});

// Catches: a stand-in that refuses or empties the answer for a run with no worker fixture of its
// own, which every scenario that builds no world is.
test("a run with no worker fixture keeps the route's own head", () => {
  const { dir, first } = runDir({ fixture: false });
  expect(headOf(dir)).toBe(first);
});

// A run with no world has no review of its own, so the submit of one is no submit this stand-in
// answers from its record: it falls to the routes, as every call of such a run does. Catches: a
// stand-in that refuses it 422 as the submit of a review that is not pending, which reads a run
// that holds no world as one that holds a world with no review in it.
test("a run with no worker fixture leaves the submit of a review to the routes", () => {
  const { dir } = runDir({ fixture: false });
  expect(
    ghCall(dir, "api", "--method", "POST", `${REVIEWS}/6001/events`, "-f", "event=APPROVE")
  ).toEqual({
    code: 0,
    out: JSON.stringify({
      id: 6001,
      html_url: "https://example.invalid/pull/1001#issuecomment-6001",
    }),
    err: "",
  });
});

/** What the stand-in answers for `gh <args...>` in run directory `dir`, as `by` if the run names
 * an agent. */
const answer = (dir: string, args: string[], by?: string) =>
  spawnChecked(["bun", STANDIN, "gh", ...args], {
    env: callEnv(dir, by),
  })
    .stdout.toString()
    .trim();

// Catches: a stand-in that answers the diff the fixture froze after another role pushed to the
// pull request, so a reviewer reading the fix the implementer pushed sees the change without it
// and resolves nothing. The run's own remote is the fact, as it is for the head.
test("the pull request's diff is the diff of the run's own remote", () => {
  const { dir, second } = runDir({ fixture: true });
  const remote = path.join(dir, "remote.git");
  git(
    remote,
    "update-ref",
    "refs/heads/main",
    git(remote, "rev-parse", "refs/heads/legion/LWEVAL-1")
  );
  expect(answer(dir, ["pr", "diff", "1001"])).toBe("");
  git(remote, "update-ref", "refs/heads/legion/LWEVAL-1", second);
  expect(answer(dir, ["pr", "diff", "1001"])).toContain("+the text fix");
  expect(answer(dir, ["pr", "diff", "1001", "--name-only"])).toBe("docs.md");
});

// Catches: a stand-in that reads a world.json it cannot parse as a run with no world of its own.
// The two readings serve different answers: a run that builds no world is served the fixture's
// frozen head, which is right for it and is silence for a run whose world the rig wrote wrongly
// — a pushing role would read a head that is not its own and stop, and a reviewer would read the
// diff of a branch that is not the world's. So only a missing file is "no world", and a file
// that does not parse exits with an error naming it. The two worlds below are the ones that
// exercise `base` and `repo`, which rig.sh's fixture_write writes into every world and this
// schema defaults for neither.
test("a world.json that does not parse exits with an error, and one with no base or no repo too", () => {
  for (const text of [
    "{not json",
    JSON.stringify({ repo: "example/widgets", branch: "legion/LWEVAL-1" }),
    JSON.stringify({ branch: "legion/LWEVAL-1", base: "main" }),
  ]) {
    const { dir } = runDir({ fixture: true });
    writeFileSync(path.join(dir, "world.json"), text);
    const call = spawnChecked(["bun", STANDIN, "gh", "pr", "view", "1001"], {
      env: callEnv(dir),
    });
    expect(call.exitCode).toBe(2);
    expect(call.stderr.toString()).toContain("world.json is no world");
  }
});

// Catches: a stand-in that records no agent, so a run that boots one agent per role cannot tell
// which role made a call and the score reads one role's work as the other's. The line before
// the two calls is the one the record was opened with, which records no call at all.
test("each call records the agent the run named, and none when it names one", () => {
  const { dir } = runDir({ fixture: true });
  answer(dir, ["pr", "view", "1001"], "implementer");
  answer(dir, ["pr", "view", "1001"]);
  const recorded = readFileSync(path.join(dir, "calls.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line));
  expect(recorded[0]).toMatchObject({ opened: "calls.jsonl" });
  expect(recorded.slice(1).map((call) => call.by)).toEqual(["implementer", undefined]);
});

// The gh a run's PATH resolves to runs a bundle of this stand-in (rig.sh's standins: $R/lib/gh.js,
// the run's directory set in its first line), a file the world shows and an agent can `cat`, and
// it carried the rig's names: the two variables it took its run and its agent's role in
// (SKILL_SCENARIO_RUN, SKILL_SCENARIO_AGENT) and its own name in its refusals (`gh-standin`). So
// it takes its run in GH_DATA_DIR, a variable of gh's own shape, and its role in LEGION_ROLE,
// which production names every pane's role in, and refuses as `gh`. The route table's name,
// fixtures.json, stays: the world shows $R, so `ls` shows the file by name whatever the bundle
// says, and health.ts and score.ts read it under that name in every recorded run. Catches: a
// bundle that names the rig, one that finds no run with nothing in its caller's environment, a
// call that records a role its pane did not name, and a refusal in the rig's words.
test("the gh a run's PATH resolves to names none of the rig's words, and finds its run and role", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "gh-bundle-"));
  made.push(dir);
  const built = spawnChecked(
    [
      "bash",
      "-c",
      [fn("fail"), fn("standins"), `here='${import.meta.dir}'`, "R=$1", "standins"].join("\n"),
      "-",
      dir,
    ],
    { env: process.env }
  );
  expect([built.exitCode, built.stderr.toString()]).toEqual([0, ""]);
  // The run's own path, which the banner sets, is the world's to name (rig.sh's world_paths), so
  // it is masked: what is read is what the bundle says of its own.
  const bundle = readFileSync(path.join(dir, "lib", "gh.js"), "utf8").replaceAll(dir, "<run>");
  expect(bundle).toContain('\nprocess.env.GH_DATA_DIR = "<run>";\n');
  const words = [
    /skill.?scenario/gi,
    /stand.?in/gi,
    ...REMOVED_NAMES.map(
      (name) => new RegExp(`(?<![a-z0-9])${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`, "gi")
    ),
  ];
  expect(words.flatMap((word) => bundle.match(word) ?? [])).toEqual([]);
  const gh = (role: string | undefined, ...args: string[]) =>
    spawnChecked([path.join(dir, "bin", "gh"), ...args], {
      env: { PATH: process.env.PATH ?? "", ...(role === undefined ? {} : { LEGION_ROLE: role }) },
    });
  expect(gh(undefined, "--open").exitCode).toBe(0);
  const asked = gh("reviewer", "api", "user");
  expect([asked.exitCode, asked.stderr.toString()]).toEqual([1, "gh: Not Found (HTTP 404)\n"]);
  gh(undefined, "api", "user");
  const recorded = readFileSync(path.join(dir, "calls.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line));
  expect(recorded.map((call) => [call.opened, call.by, call.argv])).toEqual([
    ["calls.jsonl", undefined, undefined],
    [undefined, "reviewer", ["api", "user"]],
    [undefined, undefined, ["api", "user"]],
  ]);
  const unset = spawnChecked(["bun", STANDIN, "gh", "api", "user"], {
    env: { PATH: process.env.PATH ?? "" },
  });
  expect([unset.exitCode, unset.stderr.toString()]).toEqual([2, "gh: GH_DATA_DIR is unset\n"]);
}, 60_000);

// Catches: a call that names github.com, gh's own default host and the host the world plays,
// refused where GitHub answers it as it answers the bare call (`gh api --hostname github.com
// user` matched no route, since the world's routes name the bare call): `--hostname` before or
// after the endpoint, and a `-R` of github.com's in its `HOST/OWNER/REPO` and URL forms. And the
// record keeping the call as the world answered it, so its readers read one repository.
test("a call that names github.com is answered as the bare call, wherever the flag stands", () => {
  const { dir } = runDir({ fixture: true });
  const user = answer(dir, ["api", "user"]);
  expect(JSON.parse(user)).toEqual({ login: "widgets-reviewer[bot]" });
  expect(answer(dir, ["api", "--hostname", "github.com", "user"])).toBe(user);
  expect(answer(dir, ["api", "user", "--hostname=github.com"])).toBe(user);
  const view = answer(dir, ["pr", "view", "1001", "--json", "number", "--jq", ".number"]);
  expect(view).toBe("1001");
  for (const repo of [
    ["-R", "github.com/example/widgets"],
    ["--repo=https://github.com/example/widgets"],
  ])
    expect(
      answer(dir, ["pr", "view", "1001", ...repo, "--json", "number", "--jq", ".number"])
    ).toBe(view);
  expect(recordedCalls(dir).map((call) => call.argv)).toContainEqual([
    "pr",
    "view",
    "1001",
    "-R",
    "example/widgets",
    "--json",
    "number",
    "--jq",
    ".number",
  ]);
});

/** Every call line of the record under `dir`, the opening line left out. */
const recordedCalls = (dir: string) =>
  readFileSync(path.join(dir, "calls.jsonl"), "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line) as Record<string, unknown>)
    .filter((line) => !("opened" in line));

/** The Legion gh wrapper's refusal of a call naming one repository of an owner outside the
 * project, `repository`, and no other, under PANE_OWNERS: the leak guard's setup error over the
 * wrapper's refusal of the guard's read of that repository's visibility (gh-selectors.ts's
 * `ownerRefusal`), as it printed it on a Legion pod on 2026-11-16. */
const repositoryRefused = (repository: string) =>
  `legion gh: LEAK CHECK SETUP ERROR: the gh call: the visibility of ${repository} could not be read: gh api repos/${repository} exited 2: ${ownerAloneRefused(repository.split("/")[0] ?? "")}`;
/** The wrapper's refusal of a call naming `owner`, outside the project, alone: its own lookup of
 * the owner's gh directory, which the guard leaves it with no repository to read, as it printed it
 * on a Legion pod on 2026-11-16, under PANE_OWNERS. */
function ownerAloneRefused(owner: string): string {
  return `legion gh: "${owner}" is not an owner of this project: ${PANE_OWNERS} holds no gh directory for it\n`;
}
/** The read a reviewer of the rehearsal of 2026-11-16 made to check the commit the world's
 * workflow pins actions/checkout to, which the world answered `Not Found (HTTP 404)` from no
 * route and production's wrapper refuses, exit 2. */
const TAG_READ = ["api", "repos/actions/checkout/git/refs/tags/v7.0.1"];
/** The four calls the tester measured through the real wrapper on this pod at 18:59Z on
 * 2026-11-16, which the world answered GitHub's 404 where production exits 2, each with the line
 * production printed: one repository named by its `<repository>` argument and by `-R`, and the
 * owner alone, by `--owner` (the refusal before any visibility read, so no LEAK CHECK prefix) and
 * by the `<owner>` argument. */
const MEASURED: readonly (readonly [argv: string[], line: string])[] = [
  [["repo", "view", "actions/checkout", "--json", "name"], repositoryRefused("actions/checkout")],
  [["release", "view", "v7.0.1", "-R", "actions/checkout"], repositoryRefused("actions/checkout")],
  [
    ["search", "repos", "checkout", "--owner", "actions", "--limit", "1"],
    ownerAloneRefused("actions"),
  ],
  [["repo", "list", "actions"], ownerAloneRefused("actions")],
];

/** Which of the wrapper's two lines each kind of form of naming an owner gets, and the value that
 * names `actions` through it: a form naming one of its repositories gets the guard's line
 * (`repositoryRefused`), one naming the owner alone the wrapper's own lookup's
 * (`ownerAloneRefused`); a kind naming no owner (an argument of no selector's kind, a host, an
 * option naming nothing) gets none. A query names either by its qualifier. Every kind
 * gh-selectors.ts models has a row, and a kind with none fails the pin. */
const LINES_OF: Readonly<
  Record<"slot" | "option", Readonly<Record<string, readonly (readonly [string, string])[]>>>
> = {
  slot: {
    "": [],
    repository: [["actions/checkout", repositoryRefused("actions/checkout")]],
    "second-repository": [["actions/checkout", repositoryRefused("actions/checkout")]],
    owner: [["actions", ownerAloneRefused("actions")]],
    url: [["https://github.com/actions/checkout/pull/1", repositoryRefused("actions/checkout")]],
    query: [
      ["repo:actions/checkout", repositoryRefused("actions/checkout")],
      ["owner:actions", ownerAloneRefused("actions")],
    ],
    number: [["actions/checkout#1", repositoryRefused("actions/checkout")]],
    "new-repository": [["actions/checkout", repositoryRefused("actions/checkout")]],
  },
  option: {
    repository: [["actions/checkout", repositoryRefused("actions/checkout")]],
    "org-repositories": [["actions/checkout", repositoryRefused("actions/checkout")]],
    owner: [["actions", ownerAloneRefused("actions")]],
    "owner/name": [["actions/core", ownerAloneRefused("actions")]],
    url: [["https://github.com/actions/checkout/issues/1", repositoryRefused("actions/checkout")]],
    host: [],
    nothing: [],
  },
};

/** One form of the pin: the table's entry, the call naming `actions` through it, and the line. */
interface PinCase {
  readonly form: string;
  readonly argv: readonly string[];
  readonly line: string;
}

/** The pin's calls of a label, generated from its wrapper's own `table` rather than listed here:
 * for each entry (an argument name of ghSlotKinds or a leaf's own, an option of ghOptionKinds)
 * whose kind names an owner (LINES_OF), the first leaf of the label's gh (`selectors`, by path)
 * whose help spells it in that kind, called with the value naming `actions` through it and
 * nothing else — an argument after neutral ones (`1`) where the usage places it later — and the
 * line that kind gets. A number names a repository only where the leaf reads OWNER/REPO#N. An
 * entry no leaf of the label's help spells (a spelling of an older gh's the table holds too) is
 * `unreached`: no call of the label's gh can name it. */
function pinCases(
  table: GhTable,
  selectors: GhSelectors
): { cases: PinCase[]; unreached: string[] } {
  const cases: PinCase[] = [];
  const unreached: string[] = [];
  const leaves = Object.entries(selectors.leaves).sort(([a], [b]) => (a < b ? -1 : 1));
  const slotForm = (form: string, name: string, kind: string, only?: string) => {
    const lines = LINES_OF.slot[kind];
    if (lines === undefined) throw new Error(`the pin has no line for the argument kind ${kind}`);
    if (lines.length === 0) return;
    for (const [leaf, { slots, readsRepositoryNumber }] of leaves) {
      if (only !== undefined && leaf !== only) continue;
      const at = slots.findIndex(
        (slot) =>
          slot.names.includes(name) &&
          slot.unknown === "" &&
          (kind === "number" ? readsRepositoryNumber : slot.kind === kind)
      );
      const neutral = slots
        .slice(0, Math.max(at, 0))
        .every(
          (slot) => !slot.many && slot.unknown === "" && ["", "number", "url"].includes(slot.kind)
        );
      if (at < 0 || !neutral) continue;
      for (const [value, line] of lines)
        cases.push({ form, argv: [...leaf.split(" "), ...Array(at).fill("1"), value], line });
      return;
    }
    unreached.push(form);
  };
  for (const [name, { kind }] of Object.entries(table.slots))
    slotForm(`ghSlotKinds ${name} (${kind || "none"})`, name, kind);
  for (const [leaf, quirk] of Object.entries(table.quirks))
    for (const [name, { kind }] of Object.entries(quirk.slots))
      slotForm(`ghLeafQuirks ${leaf} ${name} (${kind})`, name, kind, leaf);
  for (const [key, kind] of Object.entries(table.options)) {
    const lines = LINES_OF.option[kind];
    if (lines === undefined) throw new Error(`the pin has no line for the option kind ${kind}`);
    if (lines.length === 0) continue;
    const words = key.split(" ");
    const at = words.findIndex((word) => word.startsWith("--"));
    const only = words.slice(0, at).join(" ");
    const name = (words[at] ?? "").slice(2);
    const placeholder = words.slice(at + 1).join(" ");
    const found = leaves.find(
      ([leaf, { flags }]) =>
        (only === "" ? table.options[`${leaf} ${key}`] === undefined : leaf === only) &&
        flags[name]?.placeholder === placeholder &&
        flags[name]?.selects === kind &&
        flags[name]?.uncertain === false
    );
    if (found === undefined) unreached.push(`ghOptionKinds ${key} (${kind})`);
    else
      for (const [value, line] of lines)
        cases.push({
          form: `ghOptionKinds ${key} (${kind})`,
          argv: [...found[0].split(" "), `--${name}`, value],
          line,
        });
  }
  return { cases, unreached };
}

/** `gh <args...>` asked of the stand-in in the run under `dir` as the role `by`, under `vars`: its
 * status, its stdout and its stderr. */
function asked(
  dir: string,
  by: string | undefined,
  args: readonly string[],
  vars: Record<string, string> = {}
) {
  const ran = spawnChecked(["bun", STANDIN, "gh", ...args], { env: callEnv(dir, by, vars) });
  return [ran.exitCode, ran.stdout.toString(), ran.stderr.toString()];
}

// Catches: a world that answers a call naming another owner from its routes (the 404 of no route,
// measured in the rehearsal and again by the tester at 18:59Z on 2026-11-16) where production's
// Legion gh wrapper refuses it before gh runs, exit 2, naming the owner; one that does so for a
// form of naming it the wrapper's selector table holds and the world does not read (the
// `<repository>` and `<owner>` arguments and `--owner` the tester met); one that answers a form
// with the other of the wrapper's two lines; and one whose forms are a copy here rather than the
// label's own table, which a form the wrapper learns later would leave behind. The pin runs every
// form the table of this checkout's wrapper holds, and the four measured calls among them.
test("every form the wrapper's selector table holds, naming an owner outside the project, is refused exit 2 with the wrapper's line for it", () => {
  const table = labelGhTable(CHECKOUT);
  const selectors = labelGhSelectors(CHECKOUT);
  expect(Object.keys(LINES_OF.slot).sort()).toEqual([...MODELLED.slot].sort());
  expect(Object.keys(LINES_OF.option).sort()).toEqual([...MODELLED.option].sort());
  const { cases, unreached } = pinCases(table, selectors);
  // Each kind naming an owner is a form some leaf of this checkout's gh spells.
  const named = (type: "slot" | "option") =>
    Object.entries(LINES_OF[type])
      .filter(([, lines]) => lines.length > 0)
      .map(([kind]) => kind)
      .sort();
  const reached = (type: "slot" | "option") =>
    [
      ...new Set(
        cases
          .filter((one) => one.form.startsWith("ghOption") === (type === "option"))
          .map((one) => /\(([^)]*)\)$/.exec(one.form)?.[1] ?? "")
      ),
    ].sort();
  expect(reached("slot")).toEqual(named("slot"));
  expect(reached("option")).toEqual(named("option"));
  expect(cases.length).toBeGreaterThanOrEqual(30);
  // A leaf's own argument kind is always one its own help spells.
  expect(unreached.filter((form) => form.startsWith("ghLeafQuirks"))).toEqual([]);
  const { dir } = runDir({ fixture: true });
  const all = [...cases, ...MEASURED.map(([argv, line]) => ({ form: "measured", argv, line }))];
  expect(all.map(({ form, argv }) => [form, argv.join(" "), asked(dir, "reviewer", argv)])).toEqual(
    all.map(({ form, argv, line }) => [form, argv.join(" "), [2, "", line]])
  );
}, 180_000);

// Catches: a refusal for one role and not another; a refusal read from text a call sends (a body
// naming another owner's URL); one of the world's own owner in another case; a pane naming no
// owners directory answered as one that names it; and a world refusing for a run with no world.
test("a call naming an owner outside the world's project is refused as the Legion gh wrapper refuses it, exit 2, for every role", () => {
  const { dir } = runDir({ fixture: true });
  for (const by of ["reviewer", "implementer", "tester", "planner", "merger", undefined])
    expect(asked(dir, by, TAG_READ)).toEqual([2, "", repositoryRefused("actions/checkout")]);
  for (const args of [
    ["api", "/repos/Actions/Checkout/contents/README.md"],
    ["api", "https://api.github.com/repos/actions/checkout/tags"],
    ["pr", "view", "1", "-R", "actions/checkout"],
    ["pr", "view", "1", "--repo=github.com/actions/checkout"],
    ["pr", "view", "https://github.com/actions/checkout/pull/1"],
  ])
    expect(asked(dir, "reviewer", args)).toEqual([2, "", repositoryRefused("actions/checkout")]);
  expect(asked(dir, "reviewer", ["api", "repos/oven-sh/setup-bun/git/refs/tags/v2.2.0"])).toEqual([
    2,
    "",
    repositoryRefused("oven-sh/setup-bun"),
  ]);
  expect(asked(dir, "reviewer", ["api", "orgs/actions/repos"])).toEqual([
    2,
    "",
    ownerAloneRefused("actions"),
  ]);
  expect(
    recordedCalls(dir)
      .slice(0, 6)
      .map((call) => [call.by, call.exit])
  ).toEqual([
    ["reviewer", 2],
    ["implementer", 2],
    ["tester", 2],
    ["planner", 2],
    ["merger", 2],
    [undefined, 2],
  ]);
  const unset = spawnChecked(["bun", STANDIN, "gh", ...TAG_READ], {
    env: Object.fromEntries(
      Object.entries(callEnv(dir, "reviewer")).filter(([name]) => name !== "LEGION_GH_OWNERS_DIR")
    ),
  });
  expect([unset.exitCode, unset.stderr.toString()]).toEqual([
    2,
    "legion gh: LEGION_GH_OWNERS_DIR is unset: a Legion pane or pod sets it, and nothing else runs this gh\n",
  ]);
  // The world's own owner in another case, and an owner's URL in the text a call sends, are no
  // such call.
  for (const args of [
    ["api", "repos/EXAMPLE/widgets/pulls/1001", "--jq", ".number"],
    ["pr", "view", "1001", "-R", "Example/Widgets", "--json", "number", "--jq", ".number"],
    ["pr", "comment", "1001", "--body", "https://github.com/actions/checkout"],
  ]) {
    const [status, , err] = asked(dir, "reviewer", args);
    expect([args.join(" "), status === 2 || String(err).includes("is not an owner")]).toEqual([
      args.join(" "),
      false,
    ]);
  }
  // A run with no world names no project, and its calls are answered from the routes.
  const bare = runDir({ fixture: false }).dir;
  expect(asked(bare, undefined, TAG_READ)[0]).toBe(1);
}, 60_000);

// Catches: a world that reads no GH_REPO where the wrapper reads it, so a call naming another owner
// beside the repository gh resolves from the pane's GH_REPO (the world's) gets the one-owner line
// where production names both owners; and one reading it where the wrapper does not, which would
// give the measured `--owner` call the two-owner line.
test("a call naming an owner outside the project beside the pane's GH_REPO is refused naming both, where the wrapper reads GH_REPO", () => {
  const { dir } = runDir({ fixture: true });
  const pane = { GH_REPO: "example/widgets" };
  expect(asked(dir, "reviewer", ["issue", "transfer", "3", "actions/checkout"], pane)).toEqual([
    2,
    "",
    "legion gh: the command names more than one owner, actions (the repository actions/checkout) and example (GH_REPO example/widgets), so it sends no token; name one repository\n",
  ]);
  expect(
    asked(
      dir,
      "reviewer",
      ["issue", "edit", "5", "--add-blocked-by", "https://github.com/actions/checkout/issues/9"],
      pane
    )
  ).toEqual([
    2,
    "",
    "legion gh: the command names more than one owner, actions (--add-blocked-by https://github.com/actions/checkout/issues/9) and example (GH_REPO example/widgets), so it sends no token; name one repository\n",
  ]);
  for (const [argv, line] of MEASURED)
    expect(asked(dir, "reviewer", argv, pane)).toEqual([2, "", line]);
}, 60_000);

// Catches: a world that answers a call of a world whose gh selectors are not there as though it
// named no other owner, which is the rig having lost the label's table.
test("a world whose gh selectors are not there answers no call, naming the file", () => {
  const { dir } = runDir({ fixture: true });
  rmSync(path.join(dir, "gh-selectors.json"));
  const [status, , err] = asked(dir, "reviewer", ["pr", "view", "1001"]);
  expect([status, err]).toEqual([
    2,
    `gh: ${dir}/gh-selectors.json is not there, so no owner of this world's project is known (rig.sh's worker_boot writes it)\n`,
  ]);
  writeFileSync(path.join(dir, "gh-selectors.json"), "{not json");
  const [refused, , why] = asked(dir, "reviewer", ["pr", "view", "1001"]);
  expect([
    refused,
    String(why).startsWith(`gh: ${dir}/gh-selectors.json cannot be read: `),
  ]).toEqual([2, true]);
});

/** A checkout holding only what a label's gh selectors are read from — the wrapper's package,
 * internal/ghconfig and the wrapper's saved gh help — copied from this one, with `edit` applied to
 * the wrapper's gh_help.go: a later wrapper's own source. */
function substituteCheckout(edit: (source: string) => string): string {
  const checkout = mkdtempSync(path.join(tmpdir(), "gh-selectors-label-"));
  made.push(checkout);
  for (const dir of ["cmd/legion", "internal/ghconfig"]) {
    const from = path.join(DAEMON_MODULE, dir);
    const to = path.join(checkout, "packages", "daemon", dir);
    mkdirSync(to, { recursive: true });
    for (const name of readdirSync(from))
      if (name.endsWith(".go") && !name.endsWith("_test.go"))
        cpSync(path.join(from, name), path.join(to, name));
  }
  const legion = path.join(checkout, "packages", "daemon", "cmd", "legion");
  cpSync(path.join(DAEMON_MODULE, "cmd", "legion", "testdata"), path.join(legion, "testdata"), {
    recursive: true,
  });
  const help = path.join(legion, "gh_help.go");
  writeFileSync(help, edit(readFileSync(help, "utf8")));
  return checkout;
}

/** `source` with `entry` added as the first entry of its map literal `name`. */
function withEntry(source: string, name: string, entry: string): string {
  const opening = new RegExp(`^var ${name} = map\\[string\\][A-Za-z]+\\{\\n`, "m");
  if (!opening.test(source)) throw new Error(`gh_help.go declares no ${name} map literal`);
  return source.replace(opening, (found) => `${found}\t${entry}\n`);
}

// Catches: a world that reads a later wrapper's selector form it does not model as no form, which
// answers a call through it otherwise than production with nothing saying so; and one that reads
// a label's forms out of any checkout but that label's own. Each label's table is read from its
// own checkout, so a later wrapper's new kind of option, of argument or of quirk fails that label's
// read, by which services_up fails the batch (label_facts) and worker_boot the run, naming it, and
// the pin of that label with it, while a new entry of a kind the world models is read with no
// edit and is a case of that label's pin.
test("a selector form a label's wrapper holds that the world does not model fails that label's read and its pin, naming it", () => {
  const same = substituteCheckout((source) => source);
  expect(labelGhSelectors(same)).toEqual(labelGhSelectors(CHECKOUT));
  for (const [edit, refusal] of [
    [
      (source: string) => withEntry(source, "ghOptionKinds", '"--mirror string": "mirror",'),
      `the wrapper's table holds the option kind "mirror" (ghOptionKinds --mirror string), which the world's gh does not model (gh-selectors.ts's MODELLED)`,
    ],
    [
      (source: string) => withEntry(source, "ghSlotKinds", '"mirror": {kind: "mirror"},'),
      `the wrapper's table holds the argument kind "mirror" (ghSlotKinds mirror), which the world's gh does not model (gh-selectors.ts's MODELLED)`,
    ],
    [
      (source: string) => withEntry(source, "ghLeafQuirks", '"pr view": {readsMirror: true},'),
      `the wrapper's table holds the quirk "readsMirror" (ghLeafQuirks pr view), which the world's gh does not model (gh-selectors.ts's MODELLED)`,
    ],
    [
      (source: string) =>
        source.replace(
          "var ghKindRank = map[string]int{",
          'var ghKindRank = map[string]int{"mirror": 8, '
        ),
      `the wrapper's table holds the argument kind "mirror" (ghKindRank), which the world's gh does not model (gh-selectors.ts's MODELLED)`,
    ],
  ] as const) {
    const later = substituteCheckout(edit);
    expect(() => labelGhSelectors(later)).toThrow(refusal);
    const file = path.join(later, "gh-selectors.json");
    const written = spawnChecked(
      ["bun", path.join(import.meta.dir, "daemon-pane.ts"), "gh-selectors", later, file],
      { env: { ...process.env } }
    );
    expect([written.exitCode, written.stderr.toString().includes(refusal)]).toEqual([1, true]);
    expect(existsSync(file)).toBe(false);
  }
  // A new entry of a kind the world models is a form of that label's pin, and of no other's.
  const learned = substituteCheckout((source) =>
    withEntry(source, "ghOptionKinds", '"repo view --branch string": "repository",')
  );
  const form = "ghOptionKinds repo view --branch string (repository)";
  const ofLabel = (checkout: string) =>
    pinCases(labelGhTable(checkout), labelGhSelectors(checkout)).cases.filter(
      (one) => one.form === form
    );
  expect(ofLabel(learned)).toEqual([
    {
      form,
      argv: ["repo", "view", "--branch", "actions/checkout"],
      line: repositoryRefused("actions/checkout"),
    },
  ]);
  expect(ofLabel(CHECKOUT)).toEqual([]);
}, 120_000);

// Catches: a health that reads the wrapper's refusal of an owner outside the project as a refusal
// the scenario does not declare, for any role of any worker world and any form the table holds, so
// every reviewer that checks an action's pin is unhealthy; one that takes that call answered with
// any other status (the world's old 404) as production's answer; one that reads the owners of a
// call otherwise than the world refused it by; and one that judges a refused call of a run whose
// label's table cannot be read as any refusal at all. The refusal is judged by its exit status,
// whichever of the wrapper's lines it was, as the tester's reader judges it.
test("health declares the wrapper's refusal of an owner outside the project for every role, by its exit 2", () => {
  const { dir } = runDir({ fixture: true });
  for (const by of ["reviewer", "implementer", "tester"])
    for (const [argv] of MEASURED) asked(dir, by, argv);
  expect(recordedCalls(dir).map((call) => [call.by, call.exit])).toEqual(
    ["reviewer", "implementer", "tester"].flatMap((by) => MEASURED.map(() => [by, 2]))
  );
  for (const scenario of ["tester-proof", "reviewer-text-findings", "threads-closed"])
    expect(ghUndeclared({ dir, scenario })).toBeUndefined();
  appendFileSync(
    path.join(dir, "calls.jsonl"),
    `${JSON.stringify({ at: "t", as: "gh", by: "implementer", argv: ["repo", "list", "actions"], files: {}, route: null, exit: 1 })}\n`
  );
  expect(ghUndeclared({ dir, scenario: "threads-closed" })).toBe(
    "gh refusal: calls.jsonl:14, `gh repo list actions` of the implementer, exited 1: the refusal of an owner outside the project with another status than the Legion gh wrapper's 2, which threads-closed does not declare for that role"
  );
  // Selectors that cannot be read leave which refusal each was unknown; a run holding none was
  // answered by a world that refused no owner by them, and its calls are judged by their routes.
  writeFileSync(path.join(dir, "gh-selectors.json"), "{not json");
  expect(ghUndeclared({ dir, scenario: "threads-closed" })).toStartWith(
    `gh refusal unknown: calls.jsonl:2, \`gh repo view actions/checkout --json name\` of the reviewer, exited 2, and the selectors its world refused an owner outside the project by cannot be read: ${dir}/gh-selectors.json cannot be read: `
  );
  rmSync(path.join(dir, "gh-selectors.json"));
  expect(ghUndeclared({ dir, scenario: "threads-closed" })).toBe(
    "gh refusal: calls.jsonl:2, `gh repo view actions/checkout --json name` of the reviewer, exited 2: gh: Not Found (HTTP 404) (no route of the world answers it), which threads-closed does not declare for that role"
  );
}, 60_000);

/** A run directory whose remote holds, at world.json's seeded head, the world's workflow as rig.sh
 * writes it (fixture_round_files), so the repositories its `uses:` lines name and the commits they
 * pin are the fixture's; and whose world.json names the world as rig.sh's fixture_write does: its
 * repository and base (rig.sh's worker_repo and worker_base), the first issue of the world's
 * project (run-names.ts's WORLD_PROJECT) and that issue's branch. */
function workflowRun(): string {
  const dir = mkdtempSync(path.join(tmpdir(), "gh-standin-workflow-"));
  made.push(dir);
  const key = `${WORLD_PROJECT}-1`;
  const src = path.join(dir, "src");
  mkdirSync(path.join(src, "docs"), { recursive: true });
  git(src, "init", "-q", "-b", `legion/${key}`);
  const wrote = spawnChecked([
    "bash",
    "-c",
    [fn("fixture_round_files"), 'src=$1 fixture_round_files "A line." ""'].join("\n"),
    "-",
    src,
  ]);
  if (wrote.exitCode !== 0) throw new Error(`fixture_round_files: ${wrote.stderr.toString()}`);
  git(src, "add", "-A");
  git(src, "commit", "-qm", "the seeded head");
  const head = git(src, "rev-parse", "HEAD");
  git(dir, "clone", "-q", "--bare", src, "remote.git");
  const value = (name: string) => assignment(name).slice(`${name}=`.length);
  writeFileSync(
    path.join(dir, "world.json"),
    JSON.stringify({
      key,
      repo: value("worker_repo"),
      pr: 1001,
      branch: `legion/${key}`,
      base: value("worker_base"),
      head,
    })
  );
  return dir;
}

// Catches: a health that holds a GET of a repository the world's own workflow names to the
// world's hosts, where production reaches GitHub for it (the rehearsal's `curl` read of the tag
// actions/checkout is pinned to), and a HEAD of it, which reads no more; and one that allows
// wider: another repository, another host, a method other than GET or HEAD, a body, or a git
// that fetches, clones or pushes. The set is the workflow's, read from the run's remote at the
// seeded head.
test("rule 13 allows a GET or a HEAD with no body of a repository the world's workflow names on GitHub's hosts, and nothing wider", () => {
  const dir = workflowRun();
  const allowed = offWorldAllowed({ dir, label: "head" });
  expect([...allowed.uses.repositories]).toEqual(["actions/checkout", "oven-sh/setup-bun"]);
  const loopback = new Set(["127.0.0.1", "localhost", "::1"]);
  const reach = (tool: string, args: Record<string, unknown>) =>
    offWorldReach(
      [{ file: "sessions/reviewer/a.jsonl", tool, args, at: 1 }],
      loopback,
      [dir],
      allowed
    );
  const bash = (command: string) => reach("bash", { command });
  for (const command of [
    "curl -sS https://api.github.com/repos/actions/checkout/git/refs/tags/v7.0.1",
    "curl -sSL -X GET 'https://api.github.com/repos/oven-sh/setup-bun/git/refs/tags/v2.2.0' | jq -r .object.sha",
    "curl -fsSL https://github.com/actions/checkout/releases/tag/v7.0.1",
    "curl -fsSL https://raw.githubusercontent.com/actions/checkout/v7.0.1/action.yml",
    "wget -qO- https://codeload.github.com/oven-sh/setup-bun/tar.gz/refs/tags/v2.2.0",
    "curl -s api.github.com/repos/Actions/Checkout/tags",
    "curl -sSI https://api.github.com/repos/actions/checkout/git/refs/tags/v7.0.1",
    "curl --head https://github.com/oven-sh/setup-bun/releases/tag/v2.2.0",
    "curl -s -X HEAD https://api.github.com/repos/actions/checkout",
    "wget --spider https://github.com/actions/checkout/releases/tag/v7.0.1",
  ])
    expect([command, bash(command)]).toEqual([command, undefined]);
  for (const method of [undefined, "GET", "HEAD"])
    expect(
      reach("fetch", {
        url: "https://api.github.com/repos/actions/checkout/git/refs/tags/v7.0.1",
        ...(method === undefined ? {} : { method }),
      })
    ).toBeUndefined();
  const named = "actions/checkout, oven-sh/setup-bun";
  const off = (host: string, url: string) =>
    `off-world network: a bash call in sessions/reviewer/a.jsonl reaches ${host} (${url}), no host of the run's world (127.0.0.1, localhost, ::1)${
      host === "example.com"
        ? ""
        : `, and no GET or HEAD with no body or ls-remote of a repository the world's workflow names (${named})`
    }`;
  const tag = "https://api.github.com/repos/actions/checkout/git/refs/tags/v7.0.1";
  for (const command of [
    `curl -X POST ${tag}`,
    `curl -sS --request=PATCH ${tag}`,
    `curl -sS -d '{}' ${tag}`,
    `curl -sSI -d x ${tag}`,
    `curl -X DELETE ${tag}`,
    `wget --post-data=x ${tag}`,
  ])
    expect([command, bash(command)]).toEqual([command, off("api.github.com", tag)]);
  const cache = "https://api.github.com/repos/actions/cache/git/refs/tags/v4";
  expect(bash(`curl -sS ${cache}`)).toBe(off("api.github.com", cache));
  const elsewhere = "https://example.com/repos/actions/checkout/git/refs/tags/v7.0.1";
  expect(bash(`curl -sS ${elsewhere}`)).toBe(off("example.com", elsewhere));
  expect(
    reach("fetch", { url: tag, method: "POST" })?.startsWith(
      "off-world network: a fetch call in sessions/reviewer/a.jsonl reaches api.github.com"
    )
  ).toBe(true);
  // git's read of a repository's refs is allowed and its fetch, clone and push are not: measured
  // on 2026-11-16, `git ls-remote` sends the GET of info/refs and one `ls-refs` POST, while a
  // fetch posts again for the objects (health.ts's `refsRead`).
  const repository = "https://github.com/actions/checkout.git";
  expect(bash(`git ls-remote ${repository} refs/tags/v7.0.1`)).toBeUndefined();
  expect(bash("git -c protocol.version=0 ls-remote https://github.com/actions/checkout")).toBe(
    undefined
  );
  for (const command of [
    `git fetch --depth 1 ${repository} refs/tags/v7.0.1`,
    `git clone ${repository}`,
    `git push ${repository} main`,
  ])
    expect([command, bash(command)]).toEqual([command, off("github.com", repository)]);
  expect(bash("git ls-remote https://github.com/actions/cache.git")).toBe(
    off("github.com", "https://github.com/actions/cache.git")
  );
  // A run whose world holds no workflow allows none.
  const none = workflowUses(runDir({ fixture: true }).dir);
  expect([none.repositories.size, none.commits.size]).toEqual([0, 0]);
});

// The two calls rule 13 failed in the preflight at 08d2ba6b, byte for byte as the reviewers'
// subagents made them (QuintessentialSnipe in run e117b6918e11, PanickyTermite in b4a28e5c7cfc):
// each GETs the tags of the two actions the world's workflow pins through a `for` loop's variable,
// the first through assignments its `%%` and `#` trim too, which the architect ruled part of the
// world.
const SNIPE = [
  'for ref in "actions/checkout:v7.0.1:3d3c42e5aac5ba805825da76410c181273ba90b1" "oven-sh/setup-bun:v2.2.0:0c5077e51419868618aeaa5fe8019c62421857d6"; do',
  `  repo=\${ref%%:*}; rest=\${ref#*:}; tag=\${rest%%:*}; sha=\${rest#*:};`,
  '  echo "== $repo $tag claimed=$sha";',
  `  curl -fsSL "https://api.github.com/repos/$repo/git/refs/tags/$tag" 2>/dev/null | grep -E '"sha"|"type"|"url"' | head;`,
  '  echo "--- resolve annotated if present";',
  "done",
].join("\n");
const TERMITE = `for repo in actions/checkout oven-sh/setup-bun; do echo "=== $repo ==="; curl -s "https://api.github.com/repos/$repo/tags?per_page=100" 2>/dev/null | grep -E '"name"|"sha"' | head -20; done 2>&1 | head -60`;

// Catches: a variable a `for` loop over literal words or a command of assignments alone sets to a
// literal left unread, so a GET of a repository the world's workflow names through one fails the
// run (the two recorded calls; a `${…}`; a loop in a loop); one word of a loop's list judged for
// all of them, in either order; and a variable read wider than the shell sets it: a loop over
// what is not literal words (a command's output, a glob, a variable, a quoted substitution); a
// word the shell reads as more than itself (a quote in it); a value from a command's output or of
// a variable the reader does not hold, even after a literal; one that `read` or `export` sets; a
// prefix assignment, whose `$repo` the shell expands before it assigns; one after `&&`, inside a
// bracket or an `if`; a loop's variable after its loop (its value from before the loop kept), and
// what its body set; a pattern of more than literal characters and `*`. And a call that holds
// what may set any variable unread: `source`, `eval` (bare or after `command`), a `declare` given
// an expansion or `-i`, a name an option's letters run into (`read -arepo`), a variable the shell
// sets itself (REPLY), IFS, a comment; and a here-document, whose body is read at its place in the
// call's text.
test("rule 13 reads a variable a literal for loop or a literal assignment sets, every value of it, and no other", () => {
  const dir = workflowRun();
  const allowed = offWorldAllowed({ dir, label: "head" });
  const loopback = new Set(["127.0.0.1", "localhost", "::1"]);
  const file = "sessions/reviewer/a.jsonl";
  const bash = (command: string) =>
    offWorldReach([{ file, tool: "bash", args: { command }, at: 1 }], loopback, [dir], allowed);
  const api = "https://api.github.com/repos";
  const off = (url: string) =>
    `off-world network: a bash call in ${file} reaches api.github.com (${url}), no host of the run's world (127.0.0.1, localhost, ::1), and no GET or HEAD with no body or ls-remote of a repository the world's workflow names (actions/checkout, oven-sh/setup-bun)`;
  const unread = off(`${api}/$repo`);
  const unreadTags = off(`${api}/$repo/tags`);
  const heredoc = `python3 - <<'EOF'\nimport urllib.request; urllib.request.urlopen('https://example.com/x')\nEOF`;
  const rows: [command: string, reason: string | undefined][] = [
    [SNIPE, undefined],
    [TERMITE, undefined],
    [`repo=actions/checkout; curl -s ${api}/$repo`, undefined],
    [
      `repo=oven-sh/setup-bun\ncurl -sI "https://github.com/\${repo}/releases/tag/v2.2.0"`,
      undefined,
    ],
    [
      `for owner in actions; do for name in checkout; do curl -s "${api}/$owner/$name/tags"; done; done`,
      undefined,
    ],
    [
      `for repo in actions/checkout evil/repo; do curl -s "${api}/$repo/tags"; done`,
      off(`${api}/evil/repo/tags`),
    ],
    [
      `for repo in evil/repo actions/checkout; do curl -s "${api}/$repo/tags"; done`,
      off(`${api}/evil/repo/tags`),
    ],
    [`repo=evil/repo; curl -s ${api}/$repo`, off(`${api}/evil/repo`)],
    [
      `for repo in actions/checkout oven-sh/setup-bun; do curl -s -X POST "${api}/$repo/issues"; done`,
      off(`${api}/actions/checkout/issues`),
    ],
    [`for repo in $(cat repos); do curl -s "${api}/$repo/tags"; done`, unreadTags],
    [`for repo in *; do curl -s "${api}/$repo/tags"; done`, unreadTags],
    [`for repo in $REPOS; do curl -s "${api}/$repo/tags"; done`, unreadTags],
    [`for repo in actions/checkout "$(cat f)"; do curl -s "${api}/$repo/tags"; done`, unreadTags],
    [`for repo in "actions/checkout'"; do curl -s "${api}/$repo"; done`, unread],
    [`repo=$(cat f); curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; repo=$(cat f); curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; repo=$REPO; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; read repo < f; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; export repo=evil/fork; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout curl -s ${api}/$repo`, unread],
    [`repo=evil/fork; false && repo=actions/checkout; curl -s ${api}/$repo`, unread],
    [`repo=evil/fork; true; (true; repo=actions/checkout; true); curl -s ${api}/$repo`, unread],
    [
      `repo=evil/fork; if false; then true; repo=actions/checkout; fi; curl -s ${api}/$repo`,
      unread,
    ],
    [`for repo in actions/checkout; do :; done; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; for repo in evil/fork; do :; done; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; for r in x; do repo=evil/fork; done; curl -s ${api}/$repo`, unread],
    [`v=actions/checkout; curl -s ${api}/\${v%[t]}`, off(`${api}/\${v%[t]}`)],
    [`v=actions/checkout; curl -s ${api}/\${v%?t}`, off(`${api}/\${v%?t}`)],
    [`repo=actions/checkout; source ./env.sh; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; c=po; eval "re$c=evil/fork"; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; c=po; command eval "re$c=evil/fork"; curl -s ${api}/$repo`, unread],
    [`n=re; repo=actions/checkout; declare "\${n}po=evil/fork"; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; declare -i n; n=0,repo=1; curl -s ${api}/$repo`, unread],
    [`repo=actions/checkout; read -arepo < f; curl -s ${api}/$repo`, unread],
    [`REPLY=actions/checkout; read -r < /dev/null; curl -s ${api}/$REPLY`, off(`${api}/$REPLY`)],
    [`IFS=/; for repo in actions/checkout; do curl -s ${api}/$repo; done`, unread],
    [`repo=evil/fork\n# ; repo=actions/checkout\ncurl -s ${api}/$repo`, unread],
    [
      `for repo in actions/checkout oven-sh/setup-bun; do echo $repo; done; ${heredoc}`,
      `off-world network: a bash call in ${file} reaches example.com (https://example.com/x) in a here-document python3 runs, no host of the run's world (127.0.0.1, localhost, ::1)`,
    ],
  ];
  expect(rows.map(([command]) => [command, bash(command)])).toEqual(rows);
});

// Catches: a health that calls every web search off the world, where a production reviewer may
// search for an action the world's own workflow pins, which tells nothing of the scenario: in the
// rehearsal of 2026-11-16 a subagent of the reviewer searched `actions/checkout 3d3c42e5… v7.0.1`
// and `oven-sh/setup-bun commit 0c5077e5… v2.2.0`, and rule 13 failed the run for it. And one
// that allows wider: a query about anything else; a commit named by 6 of its digits, which name
// none; another repository whose name starts as one of the workflow's does; a word of the world's
// own beside an action or a commit the workflow pins; or a word of the measurement, by the
// definition rule 17 reads an agent's sentence by. The repositories, the commits and the world's
// words are the run's own, read from its remote and its world.json, never written here.
test("rule 13 allows a web search about what the world's workflow pins, holding no word of the world's own or of the measurement, and no other", () => {
  const dir = workflowRun();
  const allowed = offWorldAllowed({ dir, label: "head" });
  expect([...allowed.uses.commits]).toEqual([
    "3d3c42e5aac5ba805825da76410c181273ba90b1",
    "0c5077e51419868618aeaa5fe8019c62421857d6",
  ]);
  const file = "sessions/reviewer/sub/agent.jsonl";
  const loopback = new Set(["127.0.0.1", "localhost", "::1"]);
  const search = (query: string, world = allowed) =>
    offWorldReach([{ file, tool: "web_search", args: { query }, at: 1 }], loopback, [dir], world);
  const searched = `off-world network: a web_search call in ${file} searches the web, which no host of the run's world serves`;
  const unrelated = `${searched}, and its query names no repository the world's workflow names nor a commit it pins (actions/checkout, oven-sh/setup-bun)`;
  const own = (word: string) =>
    `${searched}, and its query names "${word}", a word of the world's own`;
  const measured = (word: string) =>
    `${searched}, and its query names "${word}", a word of the measurement`;
  const rows: [string, string | undefined][] = [
    ["actions/checkout 3d3c42e5aac5ba805825da76410c181273ba90b1 v7.0.1", undefined],
    ["oven-sh/setup-bun commit 0c5077e51419868618aeaa5fe8019c62421857d6 v2.2.0", undefined],
    ["actions/checkout v7.0.1 head commit", undefined],
    ["actions/checkout defect", undefined],
    ["actions/checkout base ref", undefined],
    ["actions/checkout main", undefined],
    ["actions/checkout@3d3c42e", undefined],
    ["release notes of Actions/Checkout.", undefined],
    ["commit 3d3c42e", undefined],
    ["commit 3d3c42", unrelated],
    ["actions/checkout-evil v7.0.1", unrelated],
    ["actions/checkout.js v7.0.1", unrelated],
    ["legion review threads", unrelated],
    ["text-only head", unrelated],
    ["actions/checkout text-only head", measured("text-only")],
    ["actions/checkout defect head", measured("defect head")],
    ["actions/checkout head defect", measured("head defect")],
    ["actions/checkout threads-closed", measured("threads-closed")],
    ["actions/checkout skill-scenarios", measured("skill-scenario")],
    ["actions/checkout fernbrook/widgets", own("fernbrook/widgets")],
    ["oven-sh/setup-bun widgets", own("widgets")],
    ["actions/checkout WID-1", own("wid-1")],
    ["actions/checkout WID", own("wid")],
    ["3d3c42e5aac5ba805825da76410c181273ba90b1 fernbrook", own("fernbrook")],
    ["3d3c42e5aac5ba805825da76410c181273ba90b1 WID-1", own("wid-1")],
  ];
  expect(rows.map(([query]) => [query, search(query)])).toEqual(rows);
  // A run whose world holds no workflow allows no search, with the reason every search had before.
  const none = offWorldAllowed({ dir: runDir({ fixture: true }).dir, label: "head" });
  expect(search("actions/checkout 3d3c42e5aac5ba805825da76410c181273ba90b1 v7.0.1", none)).toBe(
    searched
  );
});

// Catches: a health whose push-guard rule takes the Legion gh wrapper's refusal of an owner
// outside the project for the push guard's own, since production's line carries `LEAK CHECK
// SETUP ERROR` after its `legion gh: ` prefix: every reviewer that read an action's tag would be
// unhealthy for a refusal it met as production gives it. The line is the stand-in's own, shown to
// the agent as its tool's result, and its call is in the record; the push-guard rule stays silent
// on it, rule 7 declares it, and the guard's own line at the start of a line still fires.
test("the wrapper's owner refusal a tool result shows is no push-guard refusal, and rule 7 declares it", () => {
  const { dir } = runDir({ fixture: true });
  const refused = spawnChecked(["bun", STANDIN, "gh", ...TAG_READ], {
    env: callEnv(dir, "reviewer"),
  });
  const line = refused.stderr.toString();
  expect([refused.exitCode, line]).toEqual([2, repositoryRefused("actions/checkout")]);
  const transcript = (result: string) => {
    mkdirSync(path.join(dir, "sessions", "reviewer"), { recursive: true });
    writeFileSync(
      path.join(dir, "sessions", "reviewer", "session.jsonl"),
      [
        {
          type: "message",
          timestamp: "2026-11-16T18:00:00.000Z",
          message: {
            role: "assistant",
            content: [
              {
                type: "toolCall",
                name: "bash",
                arguments: { command: `gh ${TAG_READ.join(" ")}` },
              },
            ],
          },
        },
        {
          type: "message",
          timestamp: "2026-11-16T18:00:01.000Z",
          message: {
            role: "toolResult",
            toolName: "bash",
            content: [{ type: "text", text: result }],
          },
        },
      ]
        .map((entry) => `${JSON.stringify(entry)}\n`)
        .join("")
    );
    return loadRun(dir, "reviewer-text-findings", "head");
  };
  const shown = transcript(`${line}\n[exit 2]`);
  expect(shown.results.map((result) => result.text)).toEqual([`${line}\n[exit 2]`]);
  expect(guardRefusal(shown)).toBeUndefined();
  expect(ghUndeclared(shown)).toBeUndefined();
  const guard =
    "LEAK CHECK SETUP ERROR: the gh call: the visibility of actions/checkout could not be read";
  expect(guardRefusal(transcript(guard))).toBe(
    `push guard: sessions/reviewer/session.jsonl holds the guard's refusal "${guard}"`
  );
}, 60_000);

// Catches: a record that leaves the daemon stand-in to read the run's reviews a second way. It
// moves the issue on a review of the head (daemon-standin.ts's `roundOutcome`),
// and reading the calls again itself would be a third reader of one call beside this stand-in
// and the score; so each call that left a review records the state and the head this stand-in
// answered it with — a create, the submit of a pending one, a `gh pr review` the world answered —
// and no other call records one: a `gh pr review` no route answered (gh's 404, no review on
// GitHub) and a review of another repository's pull request among them.
test("each call that leaves a review records the state and head it was answered with, and no other call does", () => {
  const { dir, first } = runDir({ fixture: true });
  // The world's own `gh pr review` route, set aside for the first call, which no route answers.
  const file = path.join(dir, "fixtures.json");
  const fixtures = JSON.parse(readFileSync(file, "utf8")) as { routes: { match: string }[] };
  const reviewing = fixtures.routes.findIndex((route) => route.match.includes("review|"));
  const [shared] = fixtures.routes.splice(reviewing, 1);
  expect(shared?.match).toBe("^gh pr (edit|comment|review|ready)");
  writeFileSync(file, JSON.stringify(fixtures));
  answer(dir, ["pr", "review", "1001", "--approve"]);
  fixtures.routes.splice(reviewing, 0, shared ?? { match: "" });
  writeFileSync(file, JSON.stringify(fixtures));
  const body = path.join(dir, "review.json");
  writeFileSync(body, JSON.stringify({ commit_id: first, event: "APPROVE", body: "approved" }));
  expect(apiPost(dir, REVIEWS, "--input", body).exitCode).toBe(0);
  createPending(dir, { commit_id: first, body: "two findings" });
  expect(apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=REQUEST_CHANGES").exitCode).toBe(0);
  answer(dir, ["pr", "review", "1001", "--approve"]);
  answer(dir, ["pr", "review", "1001", "-R", "other/widgets", "--approve"]);
  answer(dir, ["pr", "view", "1001"]);
  expect(recordedCalls(dir).map((call) => call.review)).toEqual([
    undefined,
    { state: "APPROVED", head: first },
    { state: "PENDING", head: first },
    { state: "CHANGES_REQUESTED", head: first },
    { state: "APPROVED", head: first },
    undefined,
    undefined,
  ]);
});

// Catches: checks frozen into the world, which read green on a head whose check the world's
// check-runs route says is still running, and a running check that follows the pull request
// onto the next head. Every reader of the head's checks is answered from the world's check-runs
// answer for the head the run's remote is at: `gh pr checks`, which gh exits 8 on while a check
// is pending, and the pull request's `statusCheckRollup`. Its merge state is the world's for
// that head (`mergeStates`), in gh's spelling and the REST one.
test("the head's checks are the world's check runs on the remote's tip, for gh pr checks and the rollup", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const fixtures = JSON.parse(readFileSync(path.join(dir, "fixtures.json"), "utf8"));
  const run = (name: string, status: string) => ({
    name,
    status,
    conclusion: status === "completed" ? "success" : null,
    html_url: `https://example.invalid/${name}`,
  });
  fixtures.routes.unshift(
    {
      match: `^gh api .*commits/${first.slice(0, 7)}[0-9a-f]*/check-runs`,
      stdout: { check_runs: [run("test", "in_progress"), run("lint", "completed")] },
    },
    {
      match: "^gh api .*check-runs",
      stdout: { check_runs: [run("test", "completed"), run("lint", "completed")] },
    }
  );
  fixtures.mergeStates = { [first]: "BLOCKED" };
  fixtures.routes = fixtures.routes.map((route: { match: string; stdout?: unknown }) =>
    route.match === "^gh pr view" || route.match === "^gh api .*pulls/1001"
      ? {
          ...route,
          stdout: {
            ...(route.stdout as object),
            statusCheckRollup: [],
            mergeStateStatus: "CLEAN",
            mergeable_state: "clean",
          },
        }
      : route
  );
  writeFileSync(path.join(dir, "fixtures.json"), JSON.stringify(fixtures));
  const checks = () =>
    spawnChecked(["bun", STANDIN, "gh", "pr", "checks", "1001"], {
      env: callEnv(dir),
    });
  const merge = () => [
    answer(dir, ["pr", "view", "1001", "--json", "mergeStateStatus", "--jq", ".mergeStateStatus"]),
    answer(dir, ["api", "repos/example/widgets/pulls/1001", "--jq", ".mergeable_state"]),
  ];
  const rollup = () =>
    answer(dir, [
      "pr",
      "view",
      "1001",
      "--json",
      "statusCheckRollup",
      "--jq",
      '[.statusCheckRollup[].status] | join(",")',
    ]);
  const pending = checks();
  expect([pending.exitCode, pending.stdout.toString()]).toEqual([
    8,
    "test\tpending\t0s\thttps://example.invalid/test\nlint\tpass\t0s\thttps://example.invalid/lint\n",
  ]);
  expect(rollup()).toBe("IN_PROGRESS,COMPLETED");
  expect(merge()).toEqual(["BLOCKED", "blocked"]);
  git(path.join(dir, "remote.git"), "update-ref", "refs/heads/legion/LWEVAL-1", second);
  expect(checks().exitCode).toBe(0);
  expect(rollup()).toBe("COMPLETED,COMPLETED");
  expect(merge()).toEqual(["CLEAN", "clean"]);
});

// Catches: a record nothing opens, where a run whose agents called no gh and no bun leaves no
// calls.jsonl and scores as the rig error of a record nothing wrote rather than as the run that
// called nothing (score.ts's `unscored`). rig.sh's worker_boot asks for that line with `--open`
// through the pane's own gh, whose wrapper inserts the program word (`gh-standin.ts gh --open`),
// before its agent runs, as it starts the publish recorder, which writes its own `recording`
// line; a second ask adds nothing, since the record is already open.
test("--open opens the record with one line and records no call", () => {
  const { dir } = runDir({ fixture: true });
  const open = () =>
    spawnChecked(["bun", STANDIN, "gh", "--open"], {
      env: callEnv(dir),
    });
  const recorded = () =>
    readFileSync(path.join(dir, "calls.jsonl"), "utf8")
      .split("\n")
      .filter((line) => line !== "")
      .map((line) => JSON.parse(line));
  expect(open().exitCode).toBe(0);
  expect(recorded()).toMatchObject([{ opened: "calls.jsonl" }]);
  expect(recorded()[0]).not.toHaveProperty("argv");
  expect(open().exitCode).toBe(0);
  expect(recorded()).toHaveLength(1);
  // The call after it is recorded under that line, and opens nothing of its own.
  answer(dir, ["pr", "view", "1001"]);
  expect(recorded()).toHaveLength(2);
  expect(recorded()[1]).toMatchObject({ as: "gh", argv: ["pr", "view", "1001"], exit: 0 });
});

// Catches: a stand-in with no answer for `gh api repos/<o>/<r>/compare/<a>...<b>`, which the rule
// has the implementer run to show its push changes no code that runs (review-threads.md step 2).
// The real GitHub API answers that form — measured on 2026-11-10, `gh api
// repos/cybermonklabs/legion/compare/756789f8...main --jq '{status,ahead_by,behind_by}'` exited 0
// — while this stand-in answered `gh: Not Found (HTTP 404)`, and the implementer's comparison
// failed in all 10 text-fixes head runs of that batch. The two heads a comparison names are the
// run's own, so it is read from the run's remote and no route serves it.
test("a comparison of two commits is read from the run's own remote", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const compared = answer(dir, [
    "api",
    `repos/example/widgets/compare/${first}...${second}`,
    "--jq",
    "[.status, .ahead_by, .behind_by, (.files | length), .files[0].filename] | @tsv",
  ]);
  expect(compared).toBe(["ahead", 1, 0, 1, "docs.md"].join("\t"));
  // The other way round is the same comparison read from the other side, as GitHub reads it.
  expect(
    answer(dir, [
      "api",
      `repos/example/widgets/compare/${second}...${first}`,
      "--jq",
      "[.status, .ahead_by, .behind_by, (.files | length)] | @tsv",
    ])
  ).toBe(["behind", 0, 1, 0].join("\t"));
  // A comparison of one commit with itself changes nothing, which is what the fix's own
  // comparison looks like when the implementer names the same head twice.
  expect(
    answer(dir, [
      "api",
      `repos/example/widgets/compare/${first}...${first}`,
      "--jq",
      "[.status, .ahead_by, .behind_by, (.files | length)] | @tsv",
    ])
  ).toBe(["identical", 0, 0, 0].join("\t"));
});

// Catches: a comparison answered out of the run's world for refs the remote has not got, which
// would hand an agent counts of a comparison nobody made. GitHub answers 404 for a comparison of
// commits a repository does not hold, and so does the stand-in, through the routes.
test("a comparison the run's remote cannot resolve is 404, as GitHub answers it", () => {
  const { dir } = runDir({ fixture: true });
  const call = spawnChecked(
    ["bun", STANDIN, "gh", "api", "repos/example/widgets/compare/deadbeef...cafed00d"],
    { env: callEnv(dir) }
  );
  expect(call.exitCode).toBe(1);
  expect(call.stderr.toString()).toContain("gh: Not Found (HTTP 404)");
});

// Catches: a comparison read off the path alone, whatever verb the call names. The comparison
// is GitHub's read of two refs, and a call that writes to that path is another call: it falls
// to the routes, where the route every write falls to answers it, and a stand-in that answered
// it with the two refs' diff would tell a caller a write it never made had succeeded. The verb
// is read here as the score reads it on the same call (score.ts's `methodOf`), so each spelling
// gh takes is that write: written against the flag, in lower case, and as a field with no
// method at all, which gh sends as a POST; and a PATCH and a PUT, the other two verbs the route
// every write falls to answers (rig.sh's fixture_write), so a comparison answered for every verb
// but POST fails here too.
test("a POST to the compare path is no comparison", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const range = `repos/example/widgets/compare/${first}...${second}`;
  expect(answer(dir, ["api", range, "--jq", ".status"])).toBe("ahead");
  for (const verb of [
    ["--method", "POST"],
    ["--method=post"],
    ["-X", "PATCH"],
    ["-X", "PUT"],
    ["-XPOST"],
    ["-f", "per_page=100"],
  ]) {
    const posted = ghCall(dir, "api", ...verb, range);
    expect(posted.code).toBe(0);
    expect(JSON.parse(posted.out)).toEqual({
      id: 6001,
      html_url: "https://example.invalid/pull/1001#issuecomment-6001",
    });
  }
});

// Catches: a comparison whose files carry no `patch`, which is the field a reviewer reads the
// change out of. It was absent through the batch of 2026-11-10 — a reviewer that asked for the
// patch got `null` and one fell back to `jj file show`, working around the rig on the read step
// 3 of review-threads.md rests on. GitHub's `patch` is the file's hunks alone, with no
// `diff --git` or `index` header above them.
test("each file of a comparison carries the hunks of its own diff", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const patch = answer(dir, [
    "api",
    `repos/example/widgets/compare/${first}...${second}`,
    "--jq",
    ".files[0].patch",
  ]);
  expect(patch).toStartWith("@@ -0,0 +1 @@");
  expect(patch).toContain("+the text fix");
});

// Catches: a stand-in with no answer for the diff media type, which the API documents for this
// same comparison and which a reviewer asks for when it wants the change rather than the
// counts. The answer is the diff itself, never the JSON. Catches too a stand-in that knows one
// spelling of that type: GitHub answers `application/vnd.github.diff`, which is what its
// current documentation prints, as it answers the versioned `application/vnd.github.v3.diff`,
// so a reviewer asking in the shorter one would be answered the counts it did not ask for.
test("a comparison asked for as a diff is the unified diff, in either spelling of the type", () => {
  const { dir, first, second } = runDir({ fixture: true });
  for (const type of ["application/vnd.github.v3.diff", "application/vnd.github.diff"]) {
    const diff = answer(dir, [
      "api",
      "-H",
      `Accept: ${type}`,
      `repos/example/widgets/compare/${first}...${second}`,
    ]);
    expect(diff).toStartWith("diff --git a/docs.md b/docs.md");
    expect(diff).toContain("+the text fix");
  }
});

// Catches: a compare ref handed to `git` as it was written. A ref is argv, and `git` reads a
// leading `-` as an option wherever it falls: measured on 2026-11-11, `git -C <bare> diff
// --no-renames "--output=<path>...<sha>"` created the file `<path>...<sha>` while it parsed
// its options and only then failed for want of a work tree, so the ref wrote a file of the
// caller's choosing. The diff media type is the call that reaches `git diff` first. The
// stand-in refuses such a ref instead, and the call falls to the routes, whose 404 is GitHub's
// answer for a ref it has not got. Either side of the comparison is read that way: the base is
// the side that starts the argument `git` sees and so the side that writes the file, and the
// head is refused beside it rather than left to whatever `git` makes of a range ending in an
// option.
test("a compare ref that reads as an option is refused, not run, on either side", () => {
  const { dir, first } = runDir({ fixture: true });
  for (const [name, refs] of [
    ["base", (option: string) => `${option}...${first}`],
    ["head", (option: string) => `${first}...${option}`],
  ] as [string, (option: string) => string][]) {
    const written = path.join(dir, `ref-wrote-this-${name}`);
    const call = spawnChecked(
      [
        "bun",
        STANDIN,
        "gh",
        "api",
        "-H",
        "Accept: application/vnd.github.v3.diff",
        `repos/example/widgets/compare/${refs(`--output=${written}`)}`,
      ],
      { env: callEnv(dir) }
    );
    expect(call.exitCode).toBe(1);
    expect(call.stderr.toString()).toContain("gh: Not Found (HTTP 404)");
    expect(readdirSync(dir).filter((entry) => entry.startsWith("ref-wrote-this"))).toEqual([]);
  }
});

// And the head half of that guard, through the call that reads the counts rather than the diff.
// Catches: the head left unchecked on the ground that only a base can be read as an option,
// which `git diff` makes true and `git rev-list` does not: the counts are read with `rev-list
// --count ${head}..${base}`, which puts the head first, and `git rev-list --count
// "--output=pwned..main"` exits 129 and creates the file `pwned..main` — measured on a scratch
// repository on 2026-11-12. So a compare of `main...--output=<path>` with no media type writes
// `<path>..main`, which is the file write the guard's own comment says it prevents. The call is
// refused before any `git` runs, and falls to the routes, whose 404 is GitHub's answer for a
// ref it has not got.
test("a compare head that reads as an option writes no file through the counts either", () => {
  const { dir } = runDir({ fixture: true });
  const written = path.join(dir, "counts-wrote-this");
  const call = spawnChecked(
    ["bun", STANDIN, "gh", "api", `repos/example/widgets/compare/main...--output=${written}`],
    { env: callEnv(dir) }
  );
  expect(call.exitCode).toBe(1);
  expect(call.stderr.toString()).toContain("gh: Not Found (HTTP 404)");
  expect(readdirSync(dir).filter((entry) => entry.startsWith("counts-wrote-this"))).toEqual([]);
});

// Catches: a stand-in that answers a listing with the fixture's own object untouched. `gh pr
// list` answers a list of the objects `gh pr view` answers one of (rig.sh's fixture_write), and
// the worker skill has a role with no pull request number find it that way, so a role that
// listed the pull request after a fix push read the head the fixture froze — the very head the
// rule has it compare its push against — and a listing after a body edit read the body before
// it. Every rule over an answer's object is a rule over a listed one.
test("a listing's elements each carry the run's own head, the kept body and the chosen fields", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const listed = (fields: string) => answer(dir, ["pr", "list", "--json", fields, "--jq", "@json"]);
  expect(JSON.parse(listed("number,headRefOid"))).toEqual([{ number: 1001, headRefOid: first }]);
  git(path.join(dir, "remote.git"), "update-ref", "refs/heads/legion/LWEVAL-1", second);
  expect(JSON.parse(listed("headRefOid"))).toEqual([{ headRefOid: second }]);
  answer(dir, ["pr", "edit", "1001", "--body", "the edited body"]);
  expect(JSON.parse(listed("body"))).toEqual([{ body: "the edited body" }]);
});

/** The stderr and the exit the routes a POST falls to carry, as a world writes a refusal for the
 * call it wrote a route for (rig.sh's fixture_write gives the base-protection route GitHub's own
 * 404 and exit 1). The routes are chosen by the verbs they answer (`methods`), which is how
 * fixture_write names a POST route: selected by the text of their regex, no route of a world
 * ever got the refusal, and the row below could not fail. */
function routeRefusal(dir: string, text: string): void {
  const file = path.join(dir, "fixtures.json");
  const fixtures = JSON.parse(readFileSync(file, "utf8")) as {
    routes: { match: string; methods?: string[]; stderr?: string; exit?: number }[];
  };
  const posts = fixtures.routes.filter((route) => route.methods?.includes("POST"));
  expect(posts.map((route) => route.match)).toEqual([
    "^gh api .*pulls/1001/reviews([ ?]|$)",
    "^gh api ",
  ]);
  for (const route of posts) Object.assign(route, { stderr: text, exit: 1 });
  writeFileSync(file, JSON.stringify(fixtures));
}

// Catches: a stand-in that prints the matched route's stderr beside its own answer to a submit,
// or exits as that route does. The submit of a pending review is answered from what this run
// has already done and not from a route, so the text and the status of whatever route the call
// fell to — the generic POST, written for the calls that create things — belong to another
// call: a reviewer that submitted its pending review would read a refusal on stderr beside the
// review object, or an exit of 1, and read the approval it had just made as a call that failed.
test("a submit answered from the run's own record prints no route's stderr and exits 0", () => {
  const { dir, first } = runDir({ fixture: true });
  createPending(dir, { commit_id: first, body: "two findings" });
  routeRefusal(dir, "gh: Unprocessable Entity (HTTP 422)\nthe route's own refusal\n");
  const submit = ghCall(
    dir,
    "api",
    "--method",
    "POST",
    `${REVIEWS}/6001/events`,
    "-f",
    "event=APPROVE"
  );
  expect(submit.code).toBe(0);
  expect(submit.err).toBe("");
  expect(JSON.parse(submit.out)).toEqual({ id: 6001, commit_id: first, state: "APPROVED" });
});

// Catches: a stand-in that reads a create's body out of a file alone. `gh api --input -` takes
// the body on stdin, which is how an agent sends one it built in the same command, often as the
// several lines a formatted JSON object is written over; the stand-in reads it there, records
// what it read (`stdin`) and reads the create through the score's own `sentBody`. One blind to
// stdin leaves that create no pending review, and the call that submits it is refused 422 — the
// round's own approval answered as a submission GitHub never took.
test("a create whose body is on stdin as JSON lines leaves a pending review", () => {
  const { dir, first } = runDir({ fixture: true });
  const created = spawnChecked(
    ["bun", STANDIN, "gh", "api", "--method", "POST", REVIEWS, "--input", "-"],
    {
      env: callEnv(dir),
      stdin: new TextEncoder().encode(
        `${JSON.stringify({ commit_id: first, body: "two findings" }, null, 2)}\n`
      ),
    }
  );
  expect(created.exitCode).toBe(0);
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(0);
  expect(JSON.parse(submit.stdout.toString())).toEqual({
    id: 6001,
    commit_id: first,
    state: "APPROVED",
  });
});

// Catches: a create route matched on a path a call merely starts with. The create of a review
// is `…/pulls/<n>/reviews` and nothing below it: `…/pulls/<n>/reviews/<id>/comments` posts a
// comment under a review of its own, which creates no review and leaves none pending. A
// stand-in that read it as a create would answer the next submit with a review object at the
// head that comment was posted at — the approval at a head nobody approved that this endpoint
// exists to stop.
test("a POST under a review's own path is no create of a pending review", () => {
  const { dir } = runDir({ fixture: true });
  const comment = apiPost(dir, `${REVIEWS}/5002/comments`, "-f", "body=the sentence still stands");
  expect(comment.exitCode).toBe(0);
  const submit = apiPost(dir, `${REVIEWS}/6001/events`, "-f", "event=APPROVE");
  expect(submit.exitCode).toBe(1);
  expect(submit.stderr.toString()).toContain("Can not submit a review that is not pending.");
  expect(submit.stdout.toString()).toBe("");
});

// Catches: a comparison whose files all carry the whole range's diff as their `patch`. GitHub's
// `patch` is that file's own hunks, and the reviewer reads the change file by file: a push of
// two files whose every entry held both files' hunks would have it read a change to a file it
// is not looking at, and the one-file comparison above cannot tell the two readings apart.
test("each file of a comparison carries its own hunks and no other file's", () => {
  const { dir, first } = runDir({ fixture: true });
  const work = path.join(dir, "two-files");
  git(dir, "clone", "-q", path.join(dir, "remote.git"), work);
  writeFileSync(path.join(work, "docs.md"), "the text fix\n");
  writeFileSync(path.join(work, "greet.ts"), "export const greet = () => 2;\n");
  git(work, "add", "-A");
  git(work, "commit", "-qm", "docs: a fix in each file");
  const both = git(work, "rev-parse", "HEAD");
  git(work, "push", "-q", "origin", `HEAD:refs/heads/legion/LWEVAL-1`);
  const patches = JSON.parse(
    answer(dir, [
      "api",
      `repos/example/widgets/compare/${first}...${both}`,
      "--jq",
      "[.files[] | {filename, patch}] | @json",
    ])
  ) as { filename: string; patch: string }[];
  expect(patches.map((file) => file.filename)).toEqual(["docs.md", "greet.ts"]);
  expect(patches[0]?.patch).toStartWith("@@ -0,0 +1 @@");
  expect(patches[0]?.patch).toContain("+the text fix");
  expect(patches[0]?.patch).not.toContain("greet");
  expect(patches[1]?.patch).toContain("+export const greet = () => 2;");
  expect(patches[1]?.patch).not.toContain("the text fix");
});

// Catches: an answer whose `headRefOid` is the run's own head while the `head.sha` beside it
// is still the fixture's. `gh api repos/<o>/<r>/pulls/<n>` answers the REST shape, where the
// head is `head.sha`, and rig.sh's worlds carry both fields on the one object: a pushing role
// reading `.head.sha` after its push — which the worker skill has every one of them do — would
// read the commit before its own and stop, as two runs of 2026-11-06 did on `headRefOid`.
test("a pull request read through the api carries the run's own head in head.sha too", () => {
  const { dir, first, second } = runDir({ fixture: true });
  const sha = () => answer(dir, ["api", "repos/example/widgets/pulls/1001", "--jq", ".head.sha"]);
  expect(sha()).toBe(first);
  git(path.join(dir, "remote.git"), "update-ref", "refs/heads/legion/LWEVAL-1", second);
  expect(sha()).toBe(second);
});

/** The top-level values of rig.sh a world's functions read: the repository's own developer, the
 * two Apps the roles act as, the login the run's token acts as and the round's two findings. */
const WORLD_VALUES = [
  "world_author_name",
  "world_author_email",
  "world_implement_app",
  "world_implement_app_id",
  "world_review_app",
  "world_review_app_id",
  "world_login",
  "text_finding_doc_line",
  "text_finding_trigger",
];
/** WORLD (with GUARD) built for real under a run directory of its own by rig.sh's own world
 * functions, as rig.test.ts builds its worlds, with `fixture_cli` stubbed, whose heredoc holds a
 * line that is `}` alone, which `scriptFunctions` reads as the end of the function (the stub
 * writes the two files that step writes), and `fixture_workspace`, which clones the issue
 * workspace with jj and which no gh answer reads, stubbed unless `workspace` asks for it, and
 * this checkout's gh selectors written in as worker_boot writes a label's (`writeGhSelectors`).
 * `env` is the operator's own environment the build runs in, over this process's. */
function worldDir(
  world: string,
  guard = "",
  { workspace = false, env = {} }: { workspace?: boolean; env?: Record<string, string> } = {}
): string {
  const dir = mkdtempSync(path.join(tmpdir(), `gh-standin-${world}-`));
  made.push(dir);
  const steps = [
    "fixture_open",
    "fixture_git",
    "fixture_docs_before",
    "fixture_first_commit",
    "fixture_round_files",
    "fixture_head_commit",
    "fixture_implement",
    "fixture_test_handoff",
    "fixture_checks",
    "fixture_body",
    "fixture_write",
    "concluded_checks",
    "resolution_protection_routes",
    "round_world",
    "role_identity",
    "role_env",
    ...(workspace ? ["fixture_workspace"] : []),
    world,
  ];
  const built = spawnChecked(
    [
      "bash",
      "-c",
      [
        fn("fail"),
        ...steps.map(fn),
        'fixture_cli() { printf "export const greet = 1;\\n" >"$src/greet.ts"; : >"$src/greet.test.ts"; }',
        ...(workspace ? [] : ["fixture_workspace() { :; }"]),
        "R=$1 worker_key=LWEVAL-1 worker_repo=example/widgets worker_base=main",
        ...WORLD_VALUES.map(assignment),
        `${world} 1 100 "$2"`,
      ].join("\n"),
      "-",
      dir,
      guard,
    ],
    {
      env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1", ...env },
    }
  );
  if (built.exitCode !== 0) throw new Error(`${world}: ${built.stderr.toString()}`);
  writeGhSelectors(dir);
  return dir;
}
/** The pull request's head the world under `dir` was built at, read by the one schema of its
 * world.json (gh-call.ts's `readWorld`). */
function builtHead(dir: string): string {
  const read = readWorld(dir);
  if (!("world" in read)) throw new Error(read.why);
  return read.world.head;
}
/** Pushes a commit that writes `text` into `file` over the issue branch's tip to the run's own
 * remote, as a role pushes its fix, so the remote's own hook logs the push; the commit. */
function pushFix(dir: string, file: string, text: string): string {
  const work = path.join(dir, "fix-work");
  git(dir, "clone", "-q", "-b", "legion/LWEVAL-1", path.join(dir, "remote.git"), work);
  writeFileSync(path.join(work, file), text);
  git(work, "add", "-A");
  git(work, "commit", "-qm", "docs: the text fix");
  git(work, "push", "-q", "origin", "legion/LWEVAL-1");
  return git(work, "rev-parse", "HEAD");
}
/** `gh api graphql` with QUERY and the three variables a role sends with the threads query
 * (`-F o=example -F r=widgets -F n=1001`), then REST. */
const threadsQuery = (query: string, ...rest: string[]) => [
  "api",
  "graphql",
  "-f",
  `query=${query}`,
  "-F",
  "o=example",
  "-F",
  "r=widgets",
  "-F",
  "n=1001",
  ...rest,
];

// GitHub keeps what a run did: a thread a review's inline comment opens is the pull request's
// from then on, a resolution GitHub answered `true` reads back resolved, and a reply is its
// thread's last comment. Measured in INFRA-764 round 24 (dispatch://INFRA-764/artifact/
// infra-764-r24-names-scan-partial-txt): reviewers that resolved a thread or opened their own
// read the listing back as the fixture froze it — "the mock's graphql `reviewThreads` only
// surfaces the seeded style-bot thread and doesn't persist my reply/resolution or new threads"
// (reviewer-text-findings-text-only-head-1), "doesn't reflect my new threads or the resolve
// (which returned `true`)" (-head-4) — and named the world a mock in 29 of 36 runs. Every call
// is the recorded shape: the create of reviewer-text-findings-text-only-head-1/calls.jsonl:21
// and its listing at :22, the resolve at :29, and threads-closed-text-fixes-head-1/calls.jsonl's
// listing at :33, reply at :9 and comment read at :3. Catches: the world's threads answered as
// the fixture froze them, a resolve answered `true` and read back unresolved, a reply answered
// with an id and found under no thread, and a resolve of a thread the world has not got
// answered `true`, where GitHub answers NOT_FOUND.
test("GitHub keeps the threads: one a review opens is listed, a resolve reads back resolved, a reply is its thread's last comment", () => {
  const dir = worldDir("review_world");
  const head = builtHead(dir);
  const doc = 'The doc says the CLI "prints the name exactly as it was given, spaces and all,"';
  const trigger = 'The comment says the workflow runs "on every push to main"';
  const input = path.join(dir, "body.json");
  writeFileSync(
    input,
    JSON.stringify({
      commit_id: head,
      event: "APPROVE",
      body: `Reviewed PR #1001 at head ${head}.`,
      comments: [
        {
          path: "docs/cli.md",
          line: 5,
          side: "RIGHT",
          start_line: null,
          position: null,
          body: doc,
        },
        {
          path: ".github/workflows/greet.yml",
          line: 1,
          side: "RIGHT",
          start_line: null,
          position: null,
          body: trigger,
        },
      ],
    })
  );
  const create = ghCall(
    dir,
    ...["api", "--method", "POST", "repos/example/widgets/pulls/1001/reviews", "--input", input],
    ...["--jq", "{id:.id,state:.state,commit_id:.commit_id}"]
  );
  expect([create.code, JSON.parse(create.out)]).toEqual([
    0,
    { id: 6001, state: "APPROVED", commit_id: head },
  ]);
  const listing = ghCall(
    dir,
    ...threadsQuery(
      "query($o:String!,$r:String!,$n:Int!,$endCursor:String){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor} nodes{id isResolved path line comments(first:1){nodes{author{login} body}}}}}}}"
    ).toSpliced(2, 0, "--paginate")
  );
  const node = (id: string, file: string, line: number, login: string, body: unknown) => ({
    id,
    isResolved: false,
    path: file,
    line,
    comments: { nodes: [{ author: { login }, body }] },
  });
  expect([listing.code, JSON.parse(listing.out)]).toEqual([
    0,
    {
      data: {
        repository: {
          pullRequest: {
            reviewThreads: {
              pageInfo: { hasNextPage: false, endCursor: expect.any(String) },
              nodes: [
                node(
                  "PRRT_kwDOrig0001",
                  "greet.test.ts",
                  4,
                  "style-bot[bot]",
                  expect.stringContaining("greets by name")
                ),
                node("PRRT_kwDOAAAAAc4AAD9001", "docs/cli.md", 5, WORLD_LOGIN, doc),
                node(
                  "PRRT_kwDOAAAAAc4AAD9002",
                  ".github/workflows/greet.yml",
                  1,
                  WORLD_LOGIN,
                  trigger
                ),
              ],
            },
          },
        },
      },
    },
  ]);
  const resolve = (id: string) =>
    ghCall(
      dir,
      "api",
      "graphql",
      "-f",
      "query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}",
      "-F",
      `id=${id}`
    );
  expect(resolve("PRRT_kwDOAAAAAc4AAD9001")).toEqual({
    code: 0,
    out: JSON.stringify({ data: { resolveReviewThread: { thread: { isResolved: true } } } }),
    err: "",
  });
  const states = () =>
    ghCall(
      dir,
      ...threadsQuery(
        "query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100){nodes{id isResolved}}}}}",
        "--jq",
        ".data.repository.pullRequest.reviewThreads.nodes[] | [.id, .isResolved] | @tsv"
      )
    ).out;
  expect(states()).toBe(
    "PRRT_kwDOrig0001\tfalse\nPRRT_kwDOAAAAAc4AAD9001\ttrue\nPRRT_kwDOAAAAAc4AAD9002\tfalse"
  );
  const fixed = `Fixed in ${head}: the workflow comment now names only the pull_request trigger the file carries.`;
  const reply = ghCall(
    dir,
    "api",
    "graphql",
    "-f",
    "query=mutation($t:ID!,$b:String!){addPullRequestReviewThreadReply(input:{pullRequestReviewThreadId:$t,body:$b}){comment{id}}}",
    "-F",
    "t=PRRT_kwDOAAAAAc4AAD9002",
    "-F",
    `b=${fixed}`,
    "--jq",
    ".data.addPullRequestReviewThreadReply.comment.id"
  );
  expect(reply).toEqual({ code: 0, out: "PRRC_kwDOAAAAAc4AAC9003", err: "" });
  const last = ghCall(
    dir,
    ...threadsQuery(
      "query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100){nodes{id isResolved comments(last:1){nodes{author{login} body}}}}}}}",
      "--jq",
      ".data.repository.pullRequest.reviewThreads.nodes[2] | [.id, .comments.nodes[0].author.login, .comments.nodes[0].body] | @tsv"
    )
  );
  expect(last.out).toBe(`PRRT_kwDOAAAAAc4AAD9002\t${WORLD_LOGIN}\t${fixed}`);
  // A thread the world has not got is GitHub's NOT_FOUND, and resolves nothing.
  expect(resolve("PRRT_kwDOfix001")).toEqual({
    code: 1,
    out: JSON.stringify({
      data: { resolveReviewThread: null },
      errors: [
        {
          type: "NOT_FOUND",
          path: ["resolveReviewThread"],
          message: "Could not resolve to a node with the global id of 'PRRT_kwDOfix001'.",
        },
      ],
    }),
    err: "gh: Could not resolve to a node with the global id of 'PRRT_kwDOfix001'.\n",
  });
  expect(states()).toBe(
    "PRRT_kwDOrig0001\tfalse\nPRRT_kwDOAAAAAc4AAD9001\ttrue\nPRRT_kwDOAAAAAc4AAD9002\tfalse"
  );
}, 60_000);

// A review thread GitHub holds is made of review comments, each with the `databaseId` that is its
// id in the REST API, and the REST reply route answers a reply to one and files it in that
// thread. Measured in INFRA-764 round 24: "The thread comment has no databaseId, path, or line,
// so it looks like a synthetic seeded thread rather than a real review comment, which means the
// REST reply route won't work" (reviewer-text-findings-defect-base-1), and "REST shows no
// comments (synthetic rig thread)" (-defect-base-2). The REST listing is the recorded
// threads-closed-text-fixes-base-1/calls.jsonl:44 and reviewer-text-findings-text-only-head-1/
// calls.jsonl:26; the threads read is reviewer-text-findings-text-only-head-1/calls.jsonl:22's
// with the fields that sentence names; the reply is GitHub's REST form of it, which no recorded
// run could make for want of the id. Catches: a seeded thread whose comment carries no
// databaseId, a REST listing empty of the world's own thread, a REST reply answered with an id
// of no comment and filed in no thread, and a reply to a comment GitHub has not got answered 200.
test("a thread's comment carries its databaseId and its REST comment, and a REST reply lands in its thread", () => {
  const dir = worldDir("review_world");
  const head = builtHead(dir);
  const comments = () =>
    ghCall(
      dir,
      "api",
      "repos/example/widgets/pulls/1001/comments",
      "--jq",
      ".[] | [.id, .path, .line, .user.login, .in_reply_to_id] | @tsv"
    );
  expect(comments()).toEqual({ code: 0, out: "7001\tgreet.test.ts\t4\tstyle-bot[bot]", err: "" });
  const ids = ghCall(
    dir,
    ...threadsQuery(
      "query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100){nodes{id isResolved path line comments(first:20){nodes{databaseId path line author{login} body}}}}}}}",
      "--jq",
      ".data.repository.pullRequest.reviewThreads.nodes[] | .comments.nodes[] | [.databaseId, .path, .line] | @tsv"
    )
  );
  expect(ids.out).toBe("7001\tgreet.test.ts\t4");
  const answer = "Not a defect: `greets by name` and `trims the name` name different cases.";
  const reply = ghCall(
    dir,
    ...["api", "--method", "POST", "repos/example/widgets/pulls/1001/comments/7001/replies"],
    ...["-f", `body=${answer}`, "--jq", "[.id, .in_reply_to_id, .path, .line, .commit_id, .body]"]
  );
  expect([reply.code, JSON.parse(reply.out)]).toEqual([
    0,
    [9001, 7001, "greet.test.ts", 4, head, answer],
  ]);
  expect(comments().out).toBe(
    `7001\tgreet.test.ts\t4\tstyle-bot[bot]\t\n9001\tgreet.test.ts\t4\t${WORLD_LOGIN}\t7001`
  );
  const thread = ghCall(
    dir,
    ...threadsQuery(
      "query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100){nodes{id comments(last:1){totalCount nodes{databaseId author{login} body}}}}}}}",
      "--jq",
      ".data.repository.pullRequest.reviewThreads.nodes[0].comments | [.totalCount, .nodes[0].databaseId, .nodes[0].body] | @tsv"
    )
  );
  expect(thread.out).toBe(`2\t9001\t${answer}`);
  // The review the bot's comment belongs to is the pull request's, as GitHub lists it.
  expect(
    ghCall(dir, "api", "repos/example/widgets/pulls/1001/reviews/4001/comments", "--jq", ".[].id")
      .out
  ).toBe("7001");
  // A comment GitHub has not got takes no reply: 404, and nothing is filed.
  const nowhere = ghCall(
    dir,
    ...["api", "--method", "POST", "repos/example/widgets/pulls/1001/comments/7/replies"],
    ...["-f", "body=fixed"]
  );
  expect(nowhere).toEqual({ code: 1, out: "", err: "gh: Not Found (HTTP 404)\n" });
  expect(comments().out.split("\n")).toHaveLength(2);
}, 60_000);

// GitHub answers `gh api repos/<o>/<r>/contents/<path>?ref=<sha>` with the file at that commit:
// its own bytes under the raw media type, and GitHub's JSON of it otherwise. The first call is
// the tester's, word for word but the head (INFRA-764 round 24,
// not-counted-r24/threads-closed-text-fixes-head-3/calls.jsonl:16): the reviewer re-read the
// fixed file at the implementer's head, was refused 404 by a world that answered no contents
// read, and wrote "Content API 404s are just the eval sandbox"; that refusal voided 2 of the 7
// launches of the arm. Catches: the read refused, a file read from anywhere but the run's own
// remote at that commit (the fix push changes it), a raw read given a newline the file has not
// got, JSON whose content or sha is not the blob's, a ref or a path the remote has not got
// answered 200, and a route the world serves with no evidence in the table (gh-routes.ts).
test("the contents read answers the file at the commit from the run's own remote, raw or as GitHub's JSON", () => {
  const dir = worldDir("approved_world");
  const head = builtHead(dir);
  const raw = (sha: string, file = "greet.ts") => [
    "api",
    `repos/example/widgets/contents/${file}?ref=${sha}`,
    "-H",
    "Accept: application/vnd.github.raw",
  ];
  const bytes = (sha: string, file = "greet.ts") =>
    spawnChecked([
      "git",
      "-C",
      path.join(dir, "remote.git"),
      "show",
      `${sha}:${file}`,
    ]).stdout.toString();
  const read = (argv: string[]) => {
    const made = spawnChecked(["bun", STANDIN, "gh", ...argv], {
      env: callEnv(dir),
    });
    return { code: made.exitCode, out: made.stdout.toString(), err: made.stderr.toString() };
  };
  expect(read(raw(head))).toEqual({ code: 0, out: bytes(head), err: "" });
  const fix = pushFix(dir, "greet.ts", "export const greet = (name: string) => name.trim();");
  expect(read(raw(fix))).toEqual({
    code: 0,
    out: "export const greet = (name: string) => name.trim();",
    err: "",
  });
  expect(read(raw(head)).out).toBe(bytes(head));
  expect(JSON.parse(read(raw(fix).slice(0, 2)).out)).toMatchObject({
    type: "file",
    encoding: "base64",
    name: "greet.ts",
    path: "greet.ts",
    sha: git(path.join(dir, "remote.git"), "rev-parse", `${fix}:greet.ts`),
    size: 51,
    download_url: `https://raw.githubusercontent.com/example/widgets/${fix}/greet.ts`,
    content: expect.stringMatching(/^([A-Za-z0-9+/=]{1,60}\n)+$/),
  });
  expect(read([...raw(fix).slice(0, 2), "--jq", '.content | gsub("\\n"; "") | @base64d']).out).toBe(
    `${bytes(fix)}\n`
  );
  // A directory is the list of what it holds; the branch, the default branch with no ref.
  expect(
    read(["api", "repos/example/widgets/contents/docs?ref=legion/LWEVAL-1", "--jq", ".[].path"])
  ).toEqual({ code: 0, out: "docs/cli.md\n", err: "" });
  const base = git(path.join(dir, "remote.git"), "rev-parse", "main:docs/cli.md");
  expect(base).not.toBe(git(path.join(dir, "remote.git"), "rev-parse", `${head}:docs/cli.md`));
  expect(
    read(["api", "repos/example/widgets/contents/docs/cli.md", "--jq", ".sha"]).out.trim()
  ).toBe(base);
  expect(read(raw("0123456789abcdef0123456789abcdef01234567"))).toEqual({
    code: 1,
    out: "",
    err: "gh: No commit found for the ref 0123456789abcdef0123456789abcdef01234567 (HTTP 404)\n",
  });
  expect(read(raw(fix, "nothing.ts"))).toEqual({
    code: 1,
    out: "",
    err: "gh: Not Found (HTTP 404)\n",
  });
  const key = routeOf(raw(head))?.key ?? "";
  expect(worldRouteOf(key)).toMatchObject({
    route: "GET repos/{owner}/{repo}/contents/{path+}",
    evidence: { run: "not-counted-r24/threads-closed-text-fixes-head-3/calls.jsonl:16" },
  });
}, 60_000);

// GitHub reads a contents call that names no `ref` on the repository's default branch, and
// `--jq .content` prints the base64 of the file the caller then decodes. Measured by the tester's
// cross-check at 19e19657: of forty runs, two were unhealthy on a refused contents read, the
// second `gh api repos/example/widgets/contents/.github/workflows/greet.yml --jq .content`, no
// `ref` at all. The world's workflow is the change's own, on the issue branch and not yet on the
// default branch, so that call is GitHub's 404 there, as it would be on GitHub, and the same read
// at the head (`?ref=<head>`) answers the workflow; a file the default branch holds answers that
// branch's bytes, not the head's. Catches: a no-ref read refused, or answered from the head or
// from no branch at all; and a `content` that is not the base64 of the file.
test("a contents read with no ref answers the default branch's file, and .content is its base64", () => {
  const dir = worldDir("approved_world");
  const head = builtHead(dir);
  const remote = path.join(dir, "remote.git");
  const content = (endpoint: string) =>
    ghCall(dir, "api", `repos/example/widgets/contents/${endpoint}`, "--jq", ".content");
  const decoded = (read: { out: string }) => Buffer.from(read.out, "base64").toString();
  const shown = (ref: string, file: string) =>
    spawnChecked(["git", "-C", remote, "show", `${ref}:${file}`]).stdout.toString();
  const workflow = content(".github/workflows/greet.yml");
  expect(workflow).toEqual({ code: 1, out: "", err: "gh: Not Found (HTTP 404)\n" });
  const atHead = content(`.github/workflows/greet.yml?ref=${head}`);
  expect([atHead.code, decoded(atHead)]).toEqual([0, shown(head, ".github/workflows/greet.yml")]);
  const docs = content("docs/cli.md");
  expect([docs.code, decoded(docs)]).toEqual([0, shown("main", "docs/cli.md")]);
  expect(decoded(docs)).not.toBe(shown(head, "docs/cli.md"));
}, 60_000);

// GitHub holds a head whose base requires conversation resolution `BLOCKED` while any review
// thread is open, and reports it `CLEAN` once the run resolves the last one; a thread the run
// opens holds it again. A base that requires no resolution is moved by no thread. Catches: a
// merge state frozen into the world, which read `BLOCKED` however many threads a run resolved —
// the one state of this change's subject a run can observe, an approval left with threads open
// and an implementer that resolves them — and one that blocks a base that requires nothing. The
// reads are the merge gate's (`gh pr view <n> --repo <o>/<r> --json mergeable,mergeStateStatus`),
// the REST `mergeable_state` and merger.md's GraphQL query; the resolve is the recorded
// reviewer-text-findings-text-only-head-1/calls.jsonl:29. The daemon stand-in moves the record on
// the same state (daemon-standin.test.ts, "an approval waits in reviewing while a thread …").
test("the merge state follows the threads: BLOCKED until the last one the base requires resolved is", () => {
  const states = (dir: string) => [
    ghCall(
      dir,
      ...["pr", "view", "1001", "--repo", "example/widgets"],
      ...["--json", "mergeable,mergeStateStatus", "--jq", ".mergeStateStatus"]
    ).out,
    ghCall(dir, "api", "repos/example/widgets/pulls/1001", "--jq", ".mergeable_state").out,
    ghCall(
      dir,
      "api",
      "graphql",
      "-f",
      "query=query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){mergeStateStatus}}}",
      "-f",
      "owner=example",
      "-f",
      "name=widgets",
      "-F",
      "number=1001",
      "--jq",
      ".data.repository.pullRequest.mergeStateStatus"
    ).out,
  ];
  const resolve = (dir: string, id: string) =>
    ghCall(
      dir,
      "api",
      "graphql",
      "-f",
      "query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}",
      "-F",
      `id=${id}`
    ).code;
  const comment = (dir: string) => {
    const input = path.join(dir, "comment-review.json");
    writeFileSync(
      input,
      JSON.stringify({
        commit_id: builtHead(dir),
        event: "COMMENT",
        body: "one note",
        comments: [{ path: "docs/cli.md", line: 5, side: "RIGHT", body: "a line note" }],
      })
    );
    return ghCall(
      dir,
      ...["api", "--method", "POST", "repos/example/widgets/pulls/1001/reviews", "--input", input]
    ).code;
  };
  const blocked = ["BLOCKED", "blocked", "BLOCKED"];
  const clean = ["CLEAN", "clean", "CLEAN"];
  const guarded = worldDir("review_world");
  expect(states(guarded)).toEqual(blocked);
  expect(resolve(guarded, "PRRT_kwDOrig0001")).toBe(0);
  expect(states(guarded)).toEqual(clean);
  expect(comment(guarded)).toBe(0);
  expect(states(guarded)).toEqual(blocked);
  expect(resolve(guarded, "PRRT_kwDOAAAAAc4AAD9001")).toBe(0);
  expect(states(guarded)).toEqual(clean);
  // A base that requires no resolution: its open thread holds nothing, nor does a new one.
  const open = worldDir("approved_world", "dropped");
  expect(states(open)).toEqual(clean);
  expect(comment(open)).toBe(0);
  expect(states(open)).toEqual(clean);
}, 120_000);

// GitHub answers the base branch's protection with the checks it requires, and a head's check
// runs as runs of that head. Measured in INFRA-764 round 24: "Head checks green (`test`, `lint`
// both success); no required-check config in this rig" (reviewer-text-findings-text-only-base-3),
// "the rig doesn't require any specific checks" (-defect-base-3), and a reviewer that read its own
// push's checks four times to see "whether CI goes green, since a local rig might never report
// checks back" (-defect-base-4; reviewer-text-findings-defect-base-1/calls.jsonl:23 to :26 in
// the preflight). The reads are the merge gate's, as recorded at
// reviewer-text-findings-defect-base-1/calls.jsonl:18, :20 and :21. Catches: a base that names
// no required check, at either read, in a world whose base requires nothing else; and check
// runs that name no head, sit on no page of GitHub's and ran ten months before the commit they
// report on, read at the world's head and at the head a push made.
test("the base requires the head's checks, and the head's checks report on that head as GitHub reports them", () => {
  const rules = [
    "api",
    "--paginate",
    "repos/example/widgets/rules/branches/main",
    "--jq",
    '.[] | select(.type=="required_status_checks") | .parameters.required_status_checks[].context',
  ];
  const protection = [
    "api",
    "repos/example/widgets/branches/main",
    "--jq",
    ".protection.required_status_checks.checks[].context",
  ];
  for (const [world, guard] of [
    ["tester_proof_world", ""],
    ["approved_world", "dropped"],
    ["review_world", ""],
  ] as const) {
    const dir = worldDir(world, guard);
    expect([world, ghCall(dir, ...rules), ghCall(dir, ...protection)]).toEqual([
      world,
      { code: 0, out: "", err: "" },
      { code: 0, out: "test\nlint", err: "" },
    ]);
    expect(
      ghCall(
        dir,
        "api",
        "repos/example/widgets/branches/main/protection",
        "--jq",
        "[.required_status_checks.contexts[], .required_conversation_resolution.enabled] | @json"
      ).out
    ).toBe(JSON.stringify(["test", "lint", world === "review_world"]));
  }
  const dir = worldDir("approved_world", "dropped");
  const head = builtHead(dir);
  const runsOf = (sha: string) =>
    ghCall(
      dir,
      ...["api", `repos/example/widgets/commits/${sha}/check-runs`, "--paginate"],
      ...["--jq", ".check_runs[] | [.name, .status, .conclusion] | @tsv"]
    );
  // Each run as GitHub reports it: on the commit read, on GitHub's page of it, with the app's
  // own link, started no earlier than the commit was made and ended after it started.
  const reportOf = (sha: string) => {
    const made = git(path.join(dir, "remote.git"), "show", "-s", "--format=%ct", sha);
    const facts = `[.check_runs[] | [.head_sha, .html_url, .details_url, (.started_at | fromdateiso8601) >= ${made}, (.completed_at | fromdateiso8601) > (.started_at | fromdateiso8601), .app.slug, .app.id]]`;
    const read = ghCall(
      dir,
      "api",
      `repos/example/widgets/commits/${sha}/check-runs`,
      "--jq",
      facts
    );
    return [read.code, JSON.parse(read.out)];
  };
  const page = expect.stringMatching(/^https:\/\/github\.com\/example\/widgets\/runs\/\d+$/);
  for (const sha of [head, pushFix(dir, "docs/cli.md", "the text fix\n")]) {
    expect(runsOf(sha)).toEqual({
      code: 0,
      out: "test\tcompleted\tsuccess\nlint\tcompleted\tsuccess",
      err: "",
    });
    expect(reportOf(sha)).toEqual([
      0,
      [
        [sha, page, "https://example.invalid/2", true, true, "github-actions", 15368],
        [sha, page, "https://example.invalid/1", true, true, "github-actions", 15368],
      ],
    ]);
  }
}, 120_000);

/** rig.sh's own top-level value of `name`, as its assignment line writes it, unquoted. */
const rigValue = (name: string) =>
  assignment(name)
    .replace(/^[^=]+=/, "")
    .replace(/^'(.*)'$/, "$1");
/** The identity production's daemon gives a pane acting as the `implement` or the `review` App
 * (appauth.go's `botIdentity`): the App's bot login, and the noreply address of its bot user,
 * read off rig.sh's two Apps. */
function botOf(app: "implement" | "review"): string {
  const slug = rigValue(`world_${app}_app`);
  return `${slug}[bot] <${rigValue(`world_${app}_app_id`)}+${slug}[bot]@users.noreply.github.com>`;
}
/** The identity of the operator a run is started by, in the six variables a Legion pod carries
 * its own App in (runtime.go's `GitIdentity.Env`): a run that carried it into the world's history
 * would hand every agent the identity of the pod that ran the rig. */
const OPERATOR = Object.fromEntries(
  ["JJ_USER", "GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"]
    .map((name) => [name, "operator-app[bot]"])
    .concat(
      ["JJ_EMAIL", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"].map((name) => [
        name,
        "1+operator-app[bot]@users.noreply.github.com",
      ])
    )
);

// Production gives each role its App's bot identity, `<slug>[bot]` and
// `<id>+<slug>[bot]@users.noreply.github.com` (packages/daemon/internal/appauth's `AppRoleFor` and
// `botIdentity`), so the implementer's commit on an issue branch is the implement App's, and the
// default branch's history is the repository's own. Measured in INFRA-764 round 24: every seeded
// commit and every pane wrote as one developer, and agents read that as "the harness looks like
// it's using robin.hale as the default committer" (preflight-r24/reviewer-text-findings-defect-
// base-1); and round 24's preflight seeded its commits as the pod's own App, the operator's
// GIT_AUTHOR_* winning over the world's `-c user.name`. Catches: a seeded commit of one author,
// the implementer's commit not the implement App's, and the operator's identity in the world's
// history.
test("a seeded commit carries the identity of the role that made it, and none of the operator's", () => {
  const dir = worldDir("approved_world", "", { env: OPERATOR });
  const author = `${rigValue("world_author_name")} <${rigValue("world_author_email")}>`;
  expect(
    git(path.join(dir, "remote.git"), "log", "--all", "--format=%s|%an <%ae>|%cn <%ce>").split("\n")
  ).toEqual([
    `feat: greet(), a greeting helper for the widgets CLI (LWEVAL-1)|${botOf("implement")}|${botOf("implement")}`,
    `init|${author}|${author}`,
  ]);
}, 60_000);

// The brainstorm run's to-do CLI is a repository of its own developer's, and its one commit
// carries that developer, given as fixture_git gives a world's: round 24's preflight seeded the
// widgets worlds as the pod's own App, the operator's GIT_AUTHOR_* winning over `-c user.name`,
// and run_brainstorm committed the same way. Catches: the operator's identity in the to-do CLI's
// history, as author or as committer.
test("the brainstorm repository's commit carries its developer, and none of the operator's", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "gh-standin-brainstorm-"));
  made.push(dir);
  mkdirSync(path.join(dir, "cwd"));
  const built = spawnChecked(
    [
      "bash",
      "-c",
      [
        fn("run_brainstorm"),
        "own_dispatch() { :; }",
        "dispatch_session() { :; }",
        ...["world_author_name", "world_author_email"].map(assignment),
        "R=$1",
        "run_brainstorm brainstorm-1",
      ].join("\n"),
      "-",
      dir,
    ],
    { env: { ...process.env, ...OPERATOR, GIT_CONFIG_GLOBAL: "/dev/null" } }
  );
  const author = `${rigValue("world_author_name")} <${rigValue("world_author_email")}>`;
  expect([
    built.exitCode,
    built.stderr.toString(),
    git(path.join(dir, "cwd"), "log", "--format=%s|%an <%ae>|%cn <%ce>"),
  ]).toEqual([0, "", `A tiny to-do CLI|${author}|${author}`]);
}, 60_000);

// A pane's commit carries its role's App, author and committer alike, with jj and with git, as
// production's runtime gives a pane the six variables of its App (runtime.go's
// `GitIdentity.Env`) and adopts the shared workspace's working copy for each role at its start
// (packages/daemon/internal/shim's `runAdoption`: an undescribed working copy takes the role's
// author, a described one is kept and the role starts a new one). jj sets a change's author when
// it makes the change, so without the adoption the reviewer's prose commit carried the author of
// the change the implementer's commit left. Catches: every role one identity, a role's commit
// authored by the role before it or by the fixture, the operator's identity, a git commit of the
// pane under any other identity, and a described working copy rewritten to the next role.
test("a pane's commit carries its role's App as author and committer, and two roles differ", () => {
  const dir = worldDir("approved_world", "", { workspace: true, env: OPERATOR });
  writeFileSync(path.join(dir, "jj.toml"), '[ui]\npaginate = "never"\n');
  const shown =
    'author.name() ++ " <" ++ author.email() ++ ">|" ++ committer.name() ++ " <" ++ committer.email() ++ ">"';
  const pane = spawnChecked(
    [
      "bash",
      "-c",
      [
        ...["role_identity", "role_env", "adopt_workspace"].map(fn),
        ...WORLD_VALUES.map(assignment),
        "R=$1",
        // biome-ignore lint/suspicious/noTemplateCurlyInString: bash's own array expansion
        'as_role() { local identity; mapfile -t identity < <(role_env "$1"); shift; (cd "$R/ws" && env "${identity[@]}" JJ_CONFIG="$R/jj.toml" "$@"); }',
        `show() { as_role "$1" jj log --no-graph -r "$2" -T '${shown} ++ "\\n"'; }`,
        'commit() { adopt_workspace "$1" && printf "%s\\n" "$1" >"$R/ws/$1.md" && as_role "$1" jj commit -m "docs: $1" >/dev/null 2>&1 && show "$1" @-; }',
        "commit implementer && commit reviewer",
        'as_role reviewer jj describe -m "wip" >/dev/null 2>&1 && adopt_workspace implementer && show implementer @- && show implementer @',
        'git clone -q "$R/remote.git" "$R/git-pane" && mapfile -t identity < <(role_env tester)',
        // biome-ignore lint/suspicious/noTemplateCurlyInString: bash's own array expansion
        'env "${identity[@]}" git -C "$R/git-pane" commit --allow-empty -qm "a tester note" && git -C "$R/git-pane" log -1 --format="%an <%ae>|%cn <%ce>"',
      ].join("\n"),
      "-",
      dir,
    ],
    { env: { ...process.env, ...OPERATOR, GIT_CONFIG_GLOBAL: "/dev/null" } }
  );
  const implementer = `${botOf("implement")}|${botOf("implement")}`;
  const reviewer = `${botOf("review")}|${botOf("review")}`;
  expect(botOf("implement")).not.toBe(botOf("review"));
  expect([pane.exitCode, pane.stdout.toString().trim().split("\n")]).toEqual([
    0,
    [implementer, reviewer, reviewer, `${botOf("implement")}|${botOf("implement")}`, reviewer],
  ]);
}, 60_000);

// The routes a world answers are every gh read either label's role texts name, and every route a
// recorded run called (gh-routes.ts's WORLD_ROUTES). The sweep reads the texts at test time, from
// this checkout and from the base label's (SKILL_SCENARIOS_BASE_CHECKOUT, the checkout
// `rig.sh profile base` was built from), so a read main adds to a text fails here until the world
// answers it. rig.test.ts pins the other half: every route of the table answered in every world,
// and every route a world writes reached by a route of the table.
const CHECKOUT = path.resolve(import.meta.dir, "../../../..");
const baseCheckout = process.env.SKILL_SCENARIOS_BASE_CHECKOUT;
if (baseCheckout === undefined)
  console.warn(
    `Skipped the base label's role-text rows in ${import.meta.path}. ` +
      "SKILL_SCENARIOS_BASE_CHECKOUT names no checkout to sweep. Name the checkout `rig.sh profile " +
      "base` was built from and run the tests again."
  );
const checkouts: [string, string][] = [
  ["head", CHECKOUT],
  ...(baseCheckout === undefined ? [] : ([["base", baseCheckout]] as [string, string][])),
];

// Catches a sweep that misses a spelling a text writes a call in, so the route that text names
// goes unpinned: `--paginate`, `-X`/`--method`, a query string, each placeholder form
// (`<o>/<r>`, `{owner}/{repo}`, `<n>`, `$base_path`, `<approved sha>...<new head>`), a code span
// that breaks across lines, a command continued with a backslash, a gh call inside `$(…)`, a
// GraphQL query and mutation, `gh pr` and `gh run`; and one that reads a bare `gh api graphql` in
// prose, which sends no query, as a route.
test("the sweep reads every spelling a role text writes a gh call in", () => {
  const text = [
    "List them: `gh api --paginate repos/<o>/<r>/pulls/<n>/reviews --jq '.[] | .state'`.",
    "gh api -X POST repos/{owner}/{repo}/git/refs -f ref=refs/heads/x -f sha=<commit>",
    "Edit with `gh api --method PATCH repos/<owner>/<repo>/pulls/<n> -F body=@body.md`.",
    "Runs: (`gh api 'repos/<o>/<r>/actions/runs?head_sha=<headRefOid>&per_page=100' --jq '.workflow_runs[]'`)",
    "```bash",
    "base=$(gh pr view <n> -R <o>/<r> --json baseRefName -q .baseRefName) && \\",
    "  gh api --paginate \"repos/<o>/<r>/rules/branches/$base_path\" --jq '.[].type' && \\",
    "  gh api --method GET \\",
    "    \"repos/<o>/<r>/branches/$base_path\" --jq '.protection'",
    "```",
    "Read the push (`gh api repos/<o>/<r>/compare/<approved sha>...<new head>`), then",
    "the body (`gh api",
    "repos/{owner}/{repo}/pulls/{number} --jq .body`, edit).",
    "gh api graphql --paginate -f query='query($o:String!,$endCursor:String){repository(owner:$o,name:\"w\"){pullRequest(number:1){reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor} nodes{id}}}}}' -F o=x",
    "gh api graphql -f query='mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}' -F id=<id>",
    "Watch with `gh pr checks --watch`, `gh run view` or `gh run watch`; `gh run rerun <run-id> --failed`.",
    "Verify thread state in `gh api graphql`, never from that section.",
  ].join("\n");
  const keys = commandsIn(text).map(({ argv, line }) => [line, routeOf(argv)?.key ?? null]);
  expect(keys).toEqual([
    [1, "GET repos/{}/{}/pulls/{}/reviews"],
    [2, "POST repos/{}/{}/git/refs"],
    [3, "PATCH repos/{}/{}/pulls/{}"],
    [4, "GET repos/{}/{}/actions/runs"],
    [6, "GH pr view"],
    [7, "GET repos/{}/{}/rules/branches/{}"],
    [8, "GET repos/{}/{}/branches/{}"],
    [11, "GET repos/{}/{}/compare/{}"],
    [12, "GET repos/{}/{}/pulls/{}"],
    [14, "GRAPHQL query repository.pullRequest.reviewThreads"],
    [15, "GRAPHQL mutation resolveReviewThread"],
    [16, "GH pr checks"],
    [16, "GH run view"],
    [16, "GH run watch"],
    [16, "GH run rerun"],
    [17, null],
  ]);
  expect(routeOf(commandsIn(text)[0]?.argv ?? [])?.kind).toBe("read");
  expect(routeOf(commandsIn(text)[1]?.argv ?? [])?.kind).toBe("write");
});

// The merge gate's own reads and the thread listing are the ones a reviewer makes on every head.
// Catches a sweep that reads none of them, which pins nothing of the reads round 17 lost runs on.
test("the sweep finds the merge gate's reads and the review-thread query", () => {
  const { named } = sweep(CHECKOUT);
  const of = (file: string) =>
    new Set(named.filter((one) => one.file.endsWith(file)).map(({ key }) => key));
  expect([...of("skills/legion-worker/references/merge-gate.md")]).toEqual(
    expect.arrayContaining([
      "GET repos/{}/{}/commits/{}/status",
      "GET repos/{}/{}/actions/runs",
      "GET repos/{}/{}/actions/runs/{}/jobs",
      "GET repos/{}/{}/commits/{}/check-runs",
      "GET repos/{}/{}/rules/branches/{}",
      "GET repos/{}/{}/branches/{}",
      "GET repos/{}/{}/compare/{}",
      "GET repos/{}/{}",
    ])
  );
  expect([...of("skills/legion-worker/references/review-threads.md")]).toEqual(
    expect.arrayContaining([
      "GRAPHQL query repository.pullRequest.reviewThreads",
      "GRAPHQL mutation resolveReviewThread",
    ])
  );
});

// The both-ways pin's text side, against each checkout. Catches a read a role text names that
// the world does not answer — a refusal production never gives, which voids the run that meets
// it — and a text whose GraphQL query the sweep cannot read, which would pass unpinned.
for (const [label, root] of [
  ["head", CHECKOUT],
  ["base", baseCheckout ?? ""],
] as const)
  test.skipIf(label === "base" && baseCheckout === undefined)(
    `every gh read the ${label} label's role texts name is a route the world answers`,
    () => {
      const { named, unread } = sweep(root);
      expect(unread).toEqual([]);
      expect(named.filter(({ kind }) => kind === "read").length).toBeGreaterThan(0);
      expect(
        named
          .filter(({ kind, key }) => kind === "read" && worldRouteOf(key) === undefined)
          .map(({ key, file, line }) => `${key} (${file}:${line})`)
      ).toEqual([]);
    }
  );

// The both-ways pin's world side. Catches a route the world answers that neither label's texts
// name and that carries no evidence: a recorded run that called it
// (`<batch>/<run>/calls.jsonl:<line>`, with its reason), or why GitHub serves it to a role.
test("every route the world answers is named by a role text or declared with its evidence", () => {
  const named = checkouts.flatMap(([, root]) => sweep(root).named.map(({ key }) => key));
  expect(
    WORLD_ROUTES.filter(
      (row) =>
        row.evidence === undefined &&
        !named.some((key) => worldRouteOf(key) === row && templateMatches(row.route, key))
    ).map(({ route }) => route)
  ).toEqual([]);
  for (const { route, evidence } of WORLD_ROUTES) {
    if (evidence === undefined) continue;
    expect([route, "run" in evidence ? evidence.run : "world"]).toEqual([
      route,
      expect.stringMatching(/^(?:world|[\w.-]+\/[\w.-]+\/calls\.jsonl:\d+)$/),
    ]);
    expect([route, "run" in evidence ? evidence.reason : evidence.world]).toEqual([
      route,
      expect.stringMatching(/\w/),
    ]);
  }
});
