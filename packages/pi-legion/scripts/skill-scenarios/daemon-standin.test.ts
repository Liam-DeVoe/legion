import { expect, test } from "bun:test";
import { cpSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { type LegionRole, roleToken } from "@legion/contracts";
import { LegionActivityRequest, LegionStateResponse } from "@legion/contracts/legion-api";
import { skipWithout } from "@legion/test-fixtures/host-tools";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { z } from "zod";
import { claimRefusal, countDefault, labelDaemon, notCurrentPhase, refusalIn } from "./daemon-pane";
import { bundled, spawnChecked, writeGhSelectors } from "./test-programs";

/** The daemon stand-in and the gh stand-in the rows start, each a bundle built from its source
 * for this suite, so no start transpiles it again (test-programs.ts). */
const DAEMON_STANDIN = bundled(path.join(import.meta.dir, "daemon-standin.ts"));
const GH_STANDIN = bundled(path.join(import.meta.dir, "gh-standin.ts"));

/** This checkout, the one the head label is built from, whose daemon a stand-in reads out of the
 * run's built-from by default. */
const CHECKOUT = path.resolve(import.meta.dir, "../../../..");

// The stand-in asks the daemon's own functions for the phase its role works, which `go run`
// compiles, so a host with no go runs none of this.
const skip = skipWithout("go");

// The run directory, reserved at module scope: `temporaryDirectory` registers the hook that
// makes it, and a hook cannot be registered from inside a test.
const work = temporaryDirectory("skill-scenarios-standin");

/** The repository rig.sh's worker world is on and the branch its pull request is open against
 * (its `worker_repo` and `worker_base`, which fixture_write writes into every world.json): the
 * rows below start the stand-in on a world of those, so the record they read is the one a run's
 * agents read. That rig.sh hands the stand-in the run's directory rather than either name, and
 * so can name neither a second time, is rig.test.ts's row "the stand-in is given the run's
 * project, the run's own directory and the phase". */
const WORLD_REPO = "example/widgets";
const WORLD_BASE = "main";
/** The secret the stand-in's registration answers (daemon-standin.ts's SESSION_SECRET), which a
 * claim's later requests carry. */
const SECRET = "M4QZ7KWT2HXR5NBJ6YCPVD3LGE";

/** The handoff word of each role's phase (phase.go's `Handoff`), which the record a role's
 * completion names is of. */
const HANDOFF_WORDS: Record<string, string> = {
  planner: "plan",
  implementer: "implement",
  tester: "test",
  reviewer: "review",
};

/** The run's own Dispatch as the daemon stand-in reads a handoff record from it: `GET
 * /api/v1/handoffs/{id}` answered from `records`, as Dispatch answers it, and Dispatch's own 404
 * HANDOFF_NOT_FOUND for an id it holds none of (dispatch/api's `getHandoff`), behind the bearer
 * in `token`. A request with another bearer is Dispatch's 401. */
function dispatchOf(records: Record<string, object>, token: string) {
  return Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    fetch(request) {
      if (request.headers.get("authorization") !== `Bearer ${token}`)
        return Response.json({ code: "UNAUTHORIZED", error: "no bearer" }, { status: 401 });
      const id = /^\/api\/v1\/handoffs\/([^/]+)$/.exec(new URL(request.url).pathname)?.[1];
      const record = id === undefined ? undefined : records[decodeURIComponent(id)];
      return record === undefined
        ? Response.json(
            { code: "HANDOFF_NOT_FOUND", error: `handoff ${id} not found` },
            { status: 404 }
          )
        : Response.json(record);
    },
  });
}

/** The stand-in, started on a port the kernel picks, with its own log and boot token file, on a
 * run directory holding `world`, which defaults to the world rig.sh builds, and in `phase` when
 * the caller names one. `completed` is the roles the run's world says completed their phase with
 * a commit (rig.sh's worker_boot COMPLETED); `checkout` is the checkout the run's built-from
 * names, whose daemon the stand-in answers as, its routes and how it takes a completion among
 * the rest, this one by default and none at all for "". Nothing of the run directory's name is
 * read: the daemon a run's label runs is its checkout's, never the label's. `records` is the
 * handoff records the run's own Dispatch holds (`dispatchOf`), by id: by default `h-review`,
 * the one `complete` names, version 1 of the issue's handoff of this role's phase. It prints
 * `listening on http://127.0.0.1:<port>` once the socket is open, which is what this waits for. */
let started = 0;
async function standin(
  role: string,
  startIn = "",
  world = { repo: WORLD_REPO, base: WORLD_BASE },
  {
    completed = "",
    checkout = CHECKOUT,
    records = {
      "h-review": {
        id: "h-review",
        issue_key: "LWEVAL-1",
        phase: HANDOFF_WORDS[role] ?? "",
        version: 1,
        author: { kind: "session", id: "01a0f2cc" },
      },
    } as Record<string, object>,
  } = {}
) {
  // One directory per start, so a second stand-in of the same role writes its own log: a test
  // that reads the log reads its own run's lines and no earlier run's.
  started += 1;
  const dir = path.join(work, `${role}-${started}`);
  mkdirSync(dir, { recursive: true });
  const token = path.join(dir, "boot-token");
  writeFileSync(token, "rig-boot-token\n");
  const bearer = path.join(dir, "dispatch-token");
  writeFileSync(bearer, "the-run's-dispatch-token\n");
  const dispatch = dispatchOf(records, "the-run's-dispatch-token");
  // The world the run keeps, in the shape fixture_write writes it: the stand-in reads the
  // repository its record names, and the branch it gives as the default, out of this file.
  writeFileSync(
    path.join(dir, "world.json"),
    JSON.stringify({
      key: "LWEVAL-1",
      branch: "legion/LWEVAL-1",
      pr: 1001,
      head: "0".repeat(40),
      ...world,
    })
  );
  // The label's built-from, as cmd_run copies it into the run: the checkout whose daemon's routes
  // the stand-in answers.
  if (checkout !== "") writeFileSync(path.join(dir, "built-from"), `checkout ${checkout}\n`);
  const log = path.join(dir, "daemon.log");
  const child = Bun.spawn(
    ["bun", DAEMON_STANDIN, "0", log, token, "skills1", "LWEVAL-1", dir, role, startIn, completed],
    {
      stdout: "pipe",
      stderr: "pipe",
      env: {
        ...process.env,
        DISPATCH_URL: `http://127.0.0.1:${dispatch.port}`,
        DISPATCH_TOKEN_FILE: bearer,
      },
    }
  );
  const reader = child.stdout.getReader();
  let printed = "";
  while (!printed.includes("listening on")) {
    const { value, done } = await reader.read();
    if (done) throw new Error(`the stand-in exited before it listened: ${printed}`);
    printed += new TextDecoder().decode(value);
  }
  const url = /listening on (http:\/\/127\.0\.0\.1:\d+)/.exec(printed)?.[1];
  if (url === undefined) throw new Error(`no address in ${printed}`);
  /** The state body as the stand-in serves it, read through no schema: what a label's own
   * contract is handed, which is the thing a row below reads with two of them. */
  const body = async (): Promise<unknown> => await (await fetch(`${url}/legion/v1/state`)).json();
  const record = async () => {
    const state = LegionStateResponse.parse(await body());
    return { issue: state.issues["LWEVAL-1"], repositories: state.repositories };
  };
  const phase = async () => (await record()).issue?.phase;
  /** The settings the state read serves, as the body carries them: read before this checkout's
   * schema, which keeps unknown keys, so a key an older contract requires is visible here. */
  const settings = async () =>
    z.looseObject({ settings: z.record(z.string(), z.unknown()) }).parse(await body()).settings;
  /** `POST /legion/v1/handoff/complete` on a grant minted for it, naming a handoff record when
   * `handoff` and naming none otherwise, which is the completion of a worker that has written
   * no record; `extra` is any key a label's own contract sends beside them. */
  const complete = async (handoff: boolean, extra: Record<string, unknown> = {}) => {
    const minted = await (
      await fetch(`${url}/legion/v1/grants`, {
        method: "POST",
        body: JSON.stringify({
          sessionId: "01a0f2cc",
          secret: SECRET,
          tree: "LWEVAL-1",
          issue: "LWEVAL-1",
        }),
      })
    ).json();
    const grant = z.object({ grantId: z.string() }).parse(minted);
    return await fetch(`${url}/legion/v1/handoff/complete`, {
      method: "POST",
      body: JSON.stringify({
        grantId: grant.grantId,
        summary: "the round is in",
        verdict: "pass",
        ...(handoff ? { handoffId: "h-review", handoffVersion: 1 } : {}),
        ...extra,
      }),
    });
  };
  /** A POST of `payload` to the stand-in's `route`. */
  const post = (route: string, payload: unknown) =>
    fetch(`${url}${route}`, { method: "POST", body: JSON.stringify(payload) });
  /** Every line of this stand-in's log, parsed, in the order it wrote them. */
  const logged = () =>
    readFileSync(log, "utf8")
      .split("\n")
      .filter((line) => line !== "")
      .map((line) => JSON.parse(line) as Record<string, unknown>);
  const stop = () => {
    child.kill();
    dispatch.stop(true);
  };
  return { dir, body, phase, record, complete, post, settings, logged, stop };
}

/** Runs git in `dir` under an identity of its own and no configuration of the host's. */
function git(dir: string, ...args: string[]): string {
  const result = spawnChecked(
    ["git", "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-C", dir, ...args],
    { env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null" } }
  );
  if (result.exitCode !== 0) throw new Error(`git ${args.join(" ")}: ${result.stderr.toString()}`);
  return result.stdout.toString().trim();
}

/** The pull request of the run under `dir`, in the shapes rig.sh's fixture_write writes it: a
 * bare remote whose issue branch is at the head, no review on it, the create route every world
 * answers a review with, and the merge state GitHub reports while the pull request is at that
 * head, `state` (`CLEAN` unless the row names another), every other head being `CLEAN`
 * (gh-call.ts's `mergeStateOn`), with the gh selectors worker_boot writes beside them
 * (`writeGhSelectors`). `push` puts a commit above the tip and returns it;
 * `approve` and `requestChanges` submit a review of a head through the gh stand-in itself, so the
 * review the daemon stand-in reads is the one the world answered the call with. */
function pullRequest(dir: string, state = "CLEAN") {
  const work = path.join(dir, "work");
  mkdirSync(work);
  git(work, "init", "-q", "-b", "legion/LWEVAL-1");
  writeFileSync(path.join(work, "greet.ts"), "export const greet = () => 1;\n");
  git(work, "add", "-A");
  git(work, "commit", "-qm", "the approved head");
  const head = git(work, "rev-parse", "HEAD");
  git(dir, "clone", "-q", "--bare", work, path.join(dir, "remote.git"));
  writeFileSync(
    path.join(dir, "fixtures.json"),
    JSON.stringify({
      mergeStates: { [head]: state },
      routes: [
        {
          match: "^gh api .*pulls/1001/reviews",
          methods: ["POST"],
          stdout: {
            id: 6001,
            html_url: "https://example.invalid/pull/1001#pullrequestreview-6001",
          },
        },
        { match: "^gh api .*pulls/1001/(reviews|comments)", stdout: [] },
      ],
    })
  );
  writeGhSelectors(dir);
  const review = (event: string, commit: string) => {
    const body = path.join(dir, `review-${event}.json`);
    writeFileSync(body, JSON.stringify({ commit_id: commit, event, body: "the round's review" }));
    const call = spawnChecked(
      [
        "bun",
        GH_STANDIN,
        "gh",
        "api",
        "--method",
        "POST",
        "repos/example/widgets/pulls/1001/reviews",
        "--input",
        body,
      ],
      { env: { ...process.env, GH_DATA_DIR: dir } }
    );
    if (call.exitCode !== 0) throw new Error(`the review was refused: ${call.stderr.toString()}`);
  };
  return {
    head,
    push: () => {
      writeFileSync(path.join(work, "docs.md"), `the text fix ${Math.random()}\n`);
      git(work, "add", "-A");
      git(work, "commit", "-qm", "docs: the text fix");
      git(work, "push", "-q", path.join(dir, "remote.git"), "legion/LWEVAL-1");
      return git(work, "rev-parse", "HEAD");
    },
    approve: (commit = head) => review("APPROVE", commit),
    requestChanges: (commit = head) => review("REQUEST_CHANGES", commit),
  };
}

// Catches: a stand-in that moves the issue to `accepting` on the reviewer's completion. Main's
// engine ends the round on the review, not the completion (review.go's `reviewRound`,
// review_test.go's "after the review alone"), so a completed round with no review on the pull
// request is still `reviewing`. The stand-in that moved on any accepted completion
// (`daemon-standin.ts`, round 14) hid the case review 5887659447's finding 3 is about.
test.skipIf(skip)(
  "an accepted completion with no approval leaves the issue in reviewing",
  async () => {
    const daemon = await standin("reviewer");
    try {
      pullRequest(daemon.dir);
      expect(await daemon.phase()).toBe("reviewing");
      expect((await daemon.complete(true)).status).toBe(200);
      expect(await daemon.phase()).toBe("reviewing");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that ends the round on the approval whatever GitHub reports for the head.
// The engine moves an approval only on a merge state the issue may advance on
// (`record.MergeState.Mergeable`), and waits in `reviewing` on every other: `BLOCKED`, which a
// check the base requires holds the head in while it runs — the normal case while CI runs at the
// approved head — and `DIRTY`, a head that conflicts with its base.
test.skipIf(skip)(
  "an approval of a head GitHub reports BLOCKED or DIRTY leaves the issue in reviewing",
  async () => {
    for (const state of ["BLOCKED", "DIRTY"]) {
      const daemon = await standin("reviewer");
      try {
        pullRequest(daemon.dir, state).approve();
        expect((await daemon.complete(true)).status).toBe(200);
        expect([state, await daemon.phase()]).toEqual([state, "reviewing"]);
      } finally {
        daemon.stop();
      }
    }
  },
  600_000
);

// Catches: a stand-in whose record never leaves `reviewing`, where a reviewer that reads it
// after an approval the engine would have taken reads a round that never ended; and one that
// takes a narrower set of states than `Mergeable` does. An approval of the head GitHub reports
// `CLEAN`, `UNSTABLE` or `HAS_HOOKS` ends the round with no completion at all.
test.skipIf(skip)(
  "an approval of a head GitHub reports CLEAN, UNSTABLE or HAS_HOOKS moves the issue to accepting",
  async () => {
    for (const state of ["CLEAN", "UNSTABLE", "HAS_HOOKS"]) {
      const daemon = await standin("reviewer");
      try {
        pullRequest(daemon.dir, state).approve();
        expect([state, await daemon.phase()]).toEqual([state, "accepting"]);
      } finally {
        daemon.stop();
      }
    }
  },
  600_000
);

// Catches: a stand-in that reads a change request as no decision, or as one of the head alone.
// `reviewRound` ends the round rejected on a change request whatever head it names and whatever
// the merge state, and the issue goes back to `implementing` (`TriggerReviewRejected`), which
// reviewer-text-findings' defect case reaches in `reviewing`; an approval after it moves nothing.
test.skipIf(skip)(
  "a change request sends the issue back to implementing, and an approval after it moves nothing",
  async () => {
    const daemon = await standin("reviewer");
    try {
      const pull = pullRequest(daemon.dir);
      pull.requestChanges("0".repeat(40));
      expect(await daemon.phase()).toBe("implementing");
      pull.approve();
      expect(await daemon.phase()).toBe("implementing");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that reads an approval of any head as an approval of the pull request's.
// A push while the record is `reviewing` leaves the approval on a head that is no longer the
// pull request's, which sets it aside (`classify.ApprovalSetAside`, `stuckApprovedOtherCode`),
// and only an approval of the new head ends the round. Once the record is in `accepting` a push
// lands outside any round and moves nothing (review_test.go's "the approval of the head, then a
// push"). The approved head is `BLOCKED` and the fix head `CLEAN`, each by the world's own merge
// state for that head.
test.skipIf(skip)(
  "a push after the approval leaves the issue in reviewing until the new head is approved",
  async () => {
    const daemon = await standin("implementer", "reviewing");
    try {
      const pull = pullRequest(daemon.dir, "BLOCKED");
      pull.approve();
      expect(await daemon.phase()).toBe("reviewing");
      const fixed = pull.push();
      expect(await daemon.phase()).toBe("reviewing");
      pull.approve(fixed);
      expect(await daemon.phase()).toBe("accepting");
      pull.push();
      expect(await daemon.phase()).toBe("accepting");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// GitHub holds a head whose base requires conversation resolution `BLOCKED` while a review thread
// is open and reports it `CLEAN` once the last is resolved, and main's engine ends the round on
// an approval that stands only then. Catches: a daemon stand-in that moves on a merge state frozen
// into the world while the gh stand-in answers the threads as the run left them — the approval of
// threads-closed's text-fixes head waited in `reviewing` for ever, however many threads the
// implementer resolved — and one that reads another state than the one the gh stand-in answers
// `mergeStateStatus` with (gh-threads.ts's `mergeStateOf`, which both read). The resolve is the
// recorded reviewer-text-findings-text-only-head-1/calls.jsonl:29, made through the gh stand-in.
test.skipIf(skip)(
  "an approval waits in reviewing while a thread its base requires resolved is open, and moves once the run resolves it",
  async () => {
    const daemon = await standin("reviewer");
    try {
      const pull = pullRequest(daemon.dir);
      const file = path.join(daemon.dir, "fixtures.json");
      const fixtures = JSON.parse(readFileSync(file, "utf8"));
      fixtures.routes.push(
        {
          match: "^gh api .*/branches/main/protection",
          stdout: { required_conversation_resolution: { enabled: true } },
        },
        {
          match: "^gh api .*graphql",
          graph: {
            query: {
              repository: {
                pullRequest: {
                  reviewThreads: [
                    {
                      id: "PRRT_kwDOrig0001",
                      isResolved: false,
                      comments: [{ databaseId: 7001, state: "SUBMITTED", body: "a wording nit" }],
                    },
                  ],
                },
              },
            },
            mutation: { resolveReviewThread: {} },
          },
        }
      );
      writeFileSync(file, JSON.stringify(fixtures));
      pull.approve();
      expect(await daemon.phase()).toBe("reviewing");
      const resolve = spawnChecked(
        [
          "bun",
          GH_STANDIN,
          "gh",
          "api",
          "graphql",
          "-f",
          "query=mutation($id:ID!){resolveReviewThread(input:{threadId:$id}){thread{isResolved}}}",
          "-F",
          "id=PRRT_kwDOrig0001",
        ],
        { env: { ...process.env, GH_DATA_DIR: daemon.dir } }
      );
      expect(resolve.stdout.toString().trim()).toBe(
        JSON.stringify({ data: { resolveReviewThread: { thread: { isResolved: true } } } })
      );
      expect(await daemon.phase()).toBe("accepting");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that takes the request keys of this checkout's contract alone. An older
// label, main `ff2a10ef` (the base label of 2026-11-12), sends `ready` on every completion, and
// #2539 took it out of this checkout's strict request, so every base completion of 2026-11-12 was
// answered 400 `unrecognized_keys` and that arm measured a world of its own (review 5887659447,
// finding 1). A key no label sends is still refused, as the daemon refuses an undecodable body.
test.skipIf(skip)(
  "a completion carrying an older label's ready is answered as that label's daemon answered it",
  async () => {
    const daemon = await standin("reviewer");
    try {
      expect((await daemon.complete(true, { ready: true })).status).toBe(200);
      expect((await daemon.complete(true, { pushed: true })).status).toBe(400);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a log whose refusal lines carry no code, where a reader cannot tell the daemon's own
// refusal of a run's completion from a body the stand-in could not take at all (score.ts's
// `unscored`, which makes the second a rig error and keeps the first a reading of the run).
test.skipIf(skip)(
  "a refusal's log line carries its code, and a body the stand-in cannot take carries none",
  async () => {
    const daemon = await standin("reviewer");
    try {
      await daemon.complete(false);
      await daemon.complete(true, { pushed: true });
      const completions = daemon
        .logged()
        .filter((line) => line.path === "/legion/v1/handoff/complete");
      expect(completions).toMatchObject([
        { status: 400, code: "HANDOFF_RECORD_REQUIRED", role: "reviewer" },
        { status: 400, role: "reviewer" },
      ]);
      expect(completions[1]).not.toHaveProperty("code");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that leaves the role off a line. Health and the scorer key a completion by
// the role its line names and by nothing else, and fail closed on a completion line that names
// none (completion-answers.ts's `keyOf`), so every line of a run the stand-in served would read
// as a broken world. Every line but the log's opening one names the role this stand-in serves.
test.skipIf(skip)(
  "every line the stand-in logs names the role it serves",
  async () => {
    const daemon = await standin("implementer", "reviewing");
    try {
      await daemon.body();
      await daemon.complete(false);
      await daemon.complete(true);
      const served = daemon.logged().filter((line) => !("opened" in line));
      expect(served.length).toBeGreaterThanOrEqual(3);
      expect(served.filter((line) => line.role !== "implementer")).toEqual([]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that accepts a completion naming no record for a phase that ends with
// one. The daemon refuses it 400 HANDOFF_RECORD_REQUIRED and names the `dispatch handoff-write`
// that cures it (packages/daemon/internal/api/handoff.go), and the skill has the worker write
// the record and complete again: a 200 here teaches an agent that a phase ends without the
// record the next role reads.
test.skipIf(skip)(
  "a completion of a phase that ends with a record, naming none, is refused as the daemon refuses it",
  async () => {
    const daemon = await standin("reviewer");
    try {
      const refused = await daemon.complete(false);
      expect(refused.status).toBe(400);
      expect(await refused.json()).toMatchObject({
        code: "HANDOFF_RECORD_REQUIRED",
        error: expect.stringContaining("dispatch handoff-write --issue LWEVAL-1 --phase review"),
      });
      // Nothing moved: the round has not ended, so the issue stands in `reviewing`.
      expect(await daemon.phase()).toBe("reviewing");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that moves every phase to `accepting`, which would tell a tester its own
// phase had ended. Only the review round ends in `accepting`; every other phase's completion
// ends the run, and no scenario reads the record after it.
test.skipIf(skip)(
  "a completion of another phase leaves the issue where it was",
  async () => {
    const daemon = await standin("tester");
    try {
      expect(await daemon.phase()).toBe("testing");
      expect((await daemon.complete(true)).status).toBe(200);
      expect(await daemon.phase()).toBe("testing");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that serves the settings of this checkout's contract alone, which breaks
// every label whose contract requires a key this one dropped — silently, in that label alone
// (the measurement that found it is told once, at `daemon-standin.ts`'s `settings` line).
test.skipIf(skip)(
  "the state read serves every settings key a label's contract requires",
  async () => {
    const daemon = await standin("reviewer");
    try {
      // `max_fix_attempts` is main ff2a10ef's (contract 58, dropped by 59) and
      // `worker_boot_timeout_seconds` is this checkout's; both are positive integers in each.
      expect(await daemon.settings()).toMatchObject({
        max_fix_attempts: expect.any(Number),
        worker_boot_timeout_seconds: expect.any(Number),
      });
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a state body only this checkout's contract describes. The row above reads two keys
// by name, which is a reader's memory of what the base label requires; this one hands the body
// to that label's own schema. A label's agents load the plugin of their own checkout, so a body
// the head's schema takes and the base's refuses breaks the base arm alone and silently — every
// state read of all 30 base runs of 2026-11-09 was refused `a body the daemon API contract does
// not describe`, and that arm measured a world of its own.
//
// The base label's schema is read from its checkout's own
// `packages/contracts/src/legion-api.ts`, under the directory SKILL_SCENARIOS_BASE_CHECKOUT
// names — the checkout `rig.sh profile base <checkout>` builds that label from. With none named
// there is no second contract on this host to read, and the row says what it wants and skips,
// as a row of this rig does for a program its host has not got.
const baseCheckout = process.env.SKILL_SCENARIOS_BASE_CHECKOUT;
if (baseCheckout === undefined)
  console.warn(
    `Skipped the base label's contract row in ${import.meta.path}. ` +
      "SKILL_SCENARIOS_BASE_CHECKOUT names no checkout to read its " +
      "packages/contracts/src/legion-api.ts from. Name the checkout `rig.sh profile base` was " +
      "built from and run the tests again."
  );
/** A schema as a reader of the served body uses it, which is all this needs of another
 * checkout's zod: the answer, and the sentence a refusal carries. */
type BodyReader = {
  safeParse: (body: unknown) => { success: boolean; error?: { message: string } };
};
test.skipIf(skip || baseCheckout === undefined)(
  "the state body is described by the base label's own contract as well as this checkout's",
  async () => {
    const contract = path.join(baseCheckout ?? "", "packages", "contracts", "src", "legion-api.ts");
    // No static import reaches it: the module is another checkout's, at a path this process is
    // handed at run time.
    const loaded: unknown = await import(contract);
    if (typeof loaded !== "object" || loaded === null || !("LegionStateResponse" in loaded))
      throw new Error(`${contract} exports no LegionStateResponse`);
    /** That checkout's own schema object, built by its own zod and read here for its answer
     * alone, which no type of this checkout's zod describes. */
    const base = loaded.LegionStateResponse as BodyReader;
    const daemon = await standin("reviewer");
    try {
      const served = await daemon.body();
      // This checkout's reader first, so a body neither contract describes is read as that
      // rather than as the base label's refusal.
      const here = LegionStateResponse.safeParse(served);
      expect(here.success ? "described" : here.error.message).toBe("described");
      const there = base.safeParse(served);
      expect(there.success ? "described" : (there.error?.message ?? "refused")).toBe("described");
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that serves the role's own phase whatever the run is, so the loop after an
// approval, which runs in `accepting` where the daemon starts no phase, reads `implementing` to
// its implementer and `reviewing` to its reviewer. Each then reads a record saying it holds a
// phase it does not hold, and the step the rule gives for `accepting` is unreachable
// (review-threads.md step 1; rig.sh's run_threads_closed boots both halves there).
test.skipIf(skip)(
  "a boot names the phase the issue stands in, for either half of the loop after an approval",
  async () => {
    for (const role of ["implementer", "reviewer"]) {
      const daemon = await standin(role, "accepting");
      try {
        expect(await daemon.phase()).toBe("accepting");
      } finally {
        daemon.stop();
      }
    }
  },
  600_000
);

/** The daemon's own refusal of `role`'s late completion while the issue stands in `phase`, as the
 * daemon of `checkout` (this one by default) writes it, read out of its source by the reader the
 * stand-in answers from (daemon-pane.ts's `notCurrentPhase`); daemon-pane.test.ts holds that
 * reading to what the daemon's own source writes, so no copy of the sentence lives in this rig. */
function lateRefusal(role: string, phase: string, checkout = CHECKOUT) {
  const refusal = notCurrentPhase(checkout, role, phase, "LWEVAL-1");
  expect(refusal).toBeDefined();
  return { code: refusal?.code, error: refusal?.message };
}

// Catches: a stand-in that takes a completion from threads-closed's implementer, whose
// implementing round the run's world says already stands (rig.sh's run_threads_closed passes
// COMPLETED), and one that refuses it with an answer the daemon does not give. Either engine
// refuses a late role whose row holds a completion 409 HANDOFF_NOT_CURRENT_PHASE (this checkout's
// `leftItsPhase`, `row.Completed()`; an older daemon's `RoleFor(issue.Phase) != fact.Role`, as
// `olderCheckout` holds it), with that daemon's own sentence for the phase the issue stands in,
// which the skill has the worker quote to its architect. The route's earlier HANDOFF_NO_RUN fence
// is not what a rig half meets: each half is a live registered worker serving generation 1.
test.skipIf(skip)(
  "threads-closed's implementer, whose round stands, is refused as the daemon refuses it",
  async () => {
    for (const [checkout, phase] of [
      [CHECKOUT, "accepting"],
      [CHECKOUT, "reviewing"],
      [olderCheckout(), "accepting"],
    ] as const) {
      const daemon = await standin("implementer", phase, undefined, {
        checkout,
        completed: "implementer",
      });
      try {
        const refused = await daemon.complete(false, { commit: "0".repeat(40) });
        expect([checkout, phase, refused.status]).toEqual([checkout, phase, 409]);
        expect(await refused.json()).toEqual(lateRefusal("implementer", phase, checkout));
        // Nothing moved: the issue stands where it stood, as the refusal says.
        expect(await daemon.phase()).toBe(phase);
      } finally {
        daemon.stop();
      }
    }
  },
  600_000
);

// Catches: a stand-in that reads "the implementer has completed" off the phase it was booted in
// rather than off what the run's world says. On the head label an implementer whose row holds no
// completion is one `leftItsPhase` takes, and its completion is then of the implementing
// phase it left, whose record it must name: engine.go refuses one naming none 409
// HANDOFF_RECORD_REQUIRED through `refused`.
test.skipIf(skip)(
  "on the head label, a late implementer the world gives no completed round must name its record",
  async () => {
    const daemon = await standin("implementer", "reviewing");
    try {
      const refused = await daemon.complete(false, { commit: "0".repeat(40) });
      expect(refused.status).toBe(409);
      expect(await refused.json()).toMatchObject({
        code: "HANDOFF_RECORD_REQUIRED",
        error: expect.stringContaining(
          "the implementer's completion of phase implementing of LWEVAL-1 names no handoff record"
        ),
      });
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that decides "record required" from the phase the role was booted in
// rather than the phase the issue stands in now, which answered every completion after a
// reviewer's own REQUEST_CHANGES 400 HANDOFF_RECORD_REQUIRED (the batch at a61c46a7). After the
// round sends the issue to `implementing`, the route holds the reviewer to the record of
// `reviewing`, the phase it left (`phase.Of`), only when the completion names one. This
// checkout's engine then takes the reviewer's late completion (`leftItsPhase`), as one of
// `reviewing`, whose record it must name: naming none is refused 409 HANDOFF_RECORD_REQUIRED by
// the engine, never 400 by the route; naming one is taken and moves nothing. A taken completion
// completes the reviewer's row, so a second late completion is refused 409
// HANDOFF_NOT_CURRENT_PHASE.
test.skipIf(skip)(
  "on the head label, a reviewer's completion after its change request is answered as leftItsPhase answers it",
  async () => {
    const daemon = await standin("reviewer");
    try {
      pullRequest(daemon.dir).requestChanges();
      expect(await daemon.phase()).toBe("implementing");
      const unnamed = await daemon.complete(false);
      expect(unnamed.status).toBe(409);
      expect(await unnamed.json()).toMatchObject({
        code: "HANDOFF_RECORD_REQUIRED",
        error: expect.stringContaining(
          "the reviewer's completion of phase reviewing of LWEVAL-1 names no handoff record: write it with `dispatch handoff-write --issue LWEVAL-1 --phase review --body-file <file>`"
        ),
      });
      expect((await daemon.complete(true, { commit: "0".repeat(40) })).status).toBe(200);
      expect(await daemon.phase()).toBe("implementing");
      const again = await daemon.complete(true, { commit: "0".repeat(40) });
      expect(again.status).toBe(409);
      expect(await again.json()).toEqual(lateRefusal("reviewer", "implementing"));
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a head stand-in answering a late reviewer in `accepting` with `ff2a10ef`'s route,
// which held the completion to the record of the phase the issue stands in: `accepting` writes
// none, so a completion naming the review record was refused 409 HANDOFF_RECORD_MISMATCH. This
// checkout's route holds it to `reviewing`, the phase the reviewer left (`phase.Of`), and reads
// the record back (`recordBacked`'s `namesRecord`), so the engine takes it 200 and moves
// nothing. One naming none passes the route and the engine holds it to the record of
// `reviewing`: 409 HANDOFF_RECORD_REQUIRED, never MISMATCH. A taken one completes the row, so a
// repeat is refused 409 HANDOFF_NOT_CURRENT_PHASE.
test.skipIf(skip)(
  "on the head label, a reviewer's completion after an approval that moved the issue is answered as the daemon answers it",
  async () => {
    const daemon = await standin("reviewer");
    try {
      pullRequest(daemon.dir).approve();
      expect(await daemon.phase()).toBe("accepting");
      const unnamed = await daemon.complete(false);
      expect(unnamed.status).toBe(409);
      expect(await unnamed.json()).toMatchObject({
        code: "HANDOFF_RECORD_REQUIRED",
        error: expect.stringContaining(
          "the reviewer's completion of phase reviewing of LWEVAL-1 names no handoff record"
        ),
      });
      const named = await daemon.complete(true, { commit: "0".repeat(40) });
      expect(named.status).toBe(200);
      expect(await daemon.phase()).toBe("accepting");
      const again = await daemon.complete(true, { commit: "0".repeat(40) });
      expect(again.status).toBe(409);
      expect(await again.json()).toEqual(lateRefusal("reviewer", "accepting"));
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a head stand-in that takes a record named for a phase that writes none. This
// checkout's route still refuses it 409 HANDOFF_RECORD_MISMATCH: the merger's merging, the phase
// its completion is of, ends with no handoff record.
test.skipIf(skip)(
  "on the head label, a completion naming a record of a phase that writes none is refused record-mismatch",
  async () => {
    const daemon = await standin("merger");
    try {
      const named = await daemon.complete(true, { commit: "0".repeat(40) });
      expect(named.status).toBe(409);
      expect(await named.json()).toMatchObject({
        code: "HANDOFF_RECORD_MISMATCH",
        error: expect.stringContaining("phase merging of LWEVAL-1 ends with no handoff record"),
      });
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that takes a record-naming completion and leaves the row open, as the
// engine before main's `row.Completed()` read a row holding a record and no commit; and a
// completion's log line that does not say which phase it was answered on, which is how a person
// reading the run tells the late completion from the reviewer's own. The plugin's completion
// here names a record and no commit, so the late one is a repeat the engine refuses.
test.skipIf(skip)(
  "a completion's log line names the phase it was answered on, and a taken record completes the row",
  async () => {
    const daemon = await standin("reviewer");
    try {
      expect((await daemon.complete(true)).status).toBe(200);
      pullRequest(daemon.dir).approve();
      expect((await daemon.complete(true)).status).toBe(409);
      expect(
        daemon
          .logged()
          .filter((line) => line.path === "/legion/v1/handoff/complete")
          .map((line) => [line.status, line.code, line.phase])
      ).toEqual([
        [200, undefined, "reviewing"],
        [409, "HANDOFF_NOT_CURRENT_PHASE", "accepting"],
      ]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in of an older daemon answered as this checkout's daemon answers, which a
// table keyed by the label's name did once the base label moved to this checkout's daemon. An
// older engine (main `ff2a10ef`'s, as `olderCheckout` holds it) has no `leftItsPhase`: its
// `handoff` refuses every completion from a role that does not run the issue's phase 409
// HANDOFF_NOT_CURRENT_PHASE, whatever record it names, once its route has let it through. That
// route holds a completion to the record of the phase the issue stands in, so in `accepting` it
// refuses a named record 409 HANDOFF_RECORD_MISMATCH first. Both are read out of the checkout the
// run's built-from names (daemon-pane.ts's `labelDaemon`).
test.skipIf(skip)(
  "on an older daemon, a reviewer's late completion is refused HANDOFF_NOT_CURRENT_PHASE",
  async () => {
    const checkout = olderCheckout();
    const rejected = await standin("reviewer", "", undefined, { checkout });
    try {
      pullRequest(rejected.dir).requestChanges();
      expect(await rejected.phase()).toBe("implementing");
      for (const handoff of [false, true]) {
        const refused = await rejected.complete(handoff);
        expect([handoff, refused.status]).toEqual([handoff, 409]);
        expect(await refused.json()).toEqual(lateRefusal("reviewer", "implementing", checkout));
      }
    } finally {
      rejected.stop();
    }
    const approved = await standin("reviewer", "", undefined, { checkout });
    try {
      pullRequest(approved.dir).approve();
      expect(await approved.phase()).toBe("accepting");
      const unnamed = await approved.complete(false);
      expect(unnamed.status).toBe(409);
      expect(await unnamed.json()).toEqual(lateRefusal("reviewer", "accepting", checkout));
      const named = await approved.complete(true);
      expect(named.status).toBe(409);
      expect(await named.json()).toMatchObject({ code: "HANDOFF_RECORD_MISMATCH" });
    } finally {
      approved.stop();
    }
  },
  600_000
);

// Catches: a stand-in with no `claims/activity` route, which answers the report the agents'
// plugin posts after each turn 404 where the daemon answers it 204 (`s.activity` in
// packages/daemon/internal/api/claims.go). The daemon authenticates the report by the claim's
// secret, as it does `claims/ready`, and refuses a wrong one with claim.InvalidSecret.
test.skipIf(skip)(
  "a registered claim's activity report is answered as the daemon answers it",
  async () => {
    const daemon = await standin("implementer");
    try {
      const registered = z.object({ claimToken: z.string(), secret: z.string() }).parse(
        await (
          await daemon.post("/legion/v1/claims/register", {
            bootToken: "rig-boot-token",
            sessionId: "01a0f2cc",
          })
        ).json()
      );
      const report = LegionActivityRequest.parse({
        claimToken: registered.claimToken,
        sessionId: "01a0f2cc",
        secret: registered.secret,
        activity: {
          at: "2026-11-13T18:00:00.000Z",
          backgroundWork: { running: false, kinds: [] },
          phase: { nextReminderAt: null },
        },
      });
      const answered = await daemon.post("/legion/v1/claims/activity", report);
      expect([answered.status, await answered.text()]).toEqual([204, ""]);
      const wrong = await daemon.post("/legion/v1/claims/activity", {
        ...report,
        secret: "not-the-secret",
      });
      expect([wrong.status, await wrong.json()]).toEqual([
        403,
        { error: "Invalid session secret" },
      ]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that answers a run whose daemon's completion shapes it cannot read as some
// other daemon would. How the run's daemon takes a completion is read out of the checkout its
// built-from names (daemon-pane.ts's `labelDaemon`), never out of the label's name, and a checkout
// whose route is in neither shape fails the boot, naming the file. The checkout holds a daemon's
// API source and engine alone, which is all the boot reads before it.
test.skipIf(skip)(
  "a run whose daemon takes a completion in neither shape fails the boot, naming the file",
  () => {
    const dir = path.join(work, "reviewer-neither-shape");
    const internal = path.join(work, "neither-shape", "packages", "daemon", "internal");
    mkdirSync(path.join(internal, "api"), { recursive: true });
    mkdirSync(path.join(internal, "workflow"), { recursive: true });
    writeFileSync(
      path.join(internal, "api", "server.go"),
      'package api\n\nfunc routes() {\n\tmux.HandleFunc("GET /legion/v1/state", s.state)\n}\n'
    );
    writeFileSync(
      path.join(internal, "api", "handoff.go"),
      "package api\n\nfunc (s *server) handoffComplete() {\n\trecordBacked := writer == grant.Role\n}\n"
    );
    writeFileSync(
      path.join(internal, "workflow", "engine.go"),
      'package workflow\n\nfunc (e *Engine) handoff() {\n\tif RoleFor(issue.Phase) != fact.Role {\n\t\treturn refused("HANDOFF_NOT_CURRENT_PHASE", "no")\n\t}\n}\n'
    );
    mkdirSync(dir, { recursive: true });
    writeFileSync(path.join(dir, "boot-token"), "rig-boot-token\n");
    writeFileSync(path.join(dir, "built-from"), `checkout ${path.join(work, "neither-shape")}\n`);
    writeFileSync(
      path.join(dir, "world.json"),
      JSON.stringify({
        key: "LWEVAL-1",
        branch: "legion/LWEVAL-1",
        pr: 1001,
        head: "0".repeat(40),
        repo: WORLD_REPO,
        base: WORLD_BASE,
      })
    );
    const boot = spawnChecked([
      "bun",
      DAEMON_STANDIN,
      "0",
      path.join(dir, "daemon.log"),
      path.join(dir, "boot-token"),
      "skills1",
      "LWEVAL-1",
      dir,
      "reviewer",
    ]);
    expect(boot.exitCode).toBe(2);
    expect(boot.stderr.toString()).toContain(
      `daemon-standin: the daemon fact \`completion\` of ${path.join(work, "neither-shape")} could not be read`
    );
    expect(boot.stderr.toString()).toContain(
      `${path.join(internal, "api", "handoff.go")} holds neither`
    );
  },
  600_000
);

/** The sentence the older daemon (`olderCheckout`) refuses a late completion with: words of its
 * own, so a stand-in that read this checkout's daemon for that label answers with another. */
const OLDER_SENTENCE =
  "an older daemon says the %s may not finish phase %s of %s; this completion changed nothing";
let older: string | undefined;
/** A second checkout of this one's daemon module, standing in for an older daemon, main
 * `ff2a10ef`'s among them: a copy, so the daemon-pane.go reads that compile the daemon's own
 * functions run in it as they do in any label's checkout, and changed where an older daemon
 * differs: it registers no `claims/turn-outcome` and no `claims/activity`; its engine's `handoff`
 * refuses every late role with `if RoleFor(issue.Phase) != fact.Role { return
 * refused("HANDOFF_NOT_CURRENT_PHASE", fmt.Sprintf(OLDER_SENTENCE, …)) }` and asks no
 * `leftItsPhase` (the `refused` shape); and its completion route holds a completion's record to
 * the phase the issue stands in (`phase.Handoff(issue.Phase)`, the `issue` shape). Made once for
 * the file. */
function olderCheckout(): string {
  if (older !== undefined) return older;
  const checkout = path.join(work, "older-checkout");
  const daemon = path.join(checkout, "packages", "daemon");
  cpSync(path.join(CHECKOUT, "packages", "daemon"), daemon, { recursive: true });
  // The module's go.mod replaces the envoy module with its sibling directory (`../envoy`).
  symlinkSync(path.join(CHECKOUT, "packages", "envoy"), path.join(checkout, "packages", "envoy"));
  const server = path.join(daemon, "internal", "api", "server.go");
  const registered = readFileSync(server, "utf8");
  const fewer = registered
    .split("\n")
    .filter(
      (line) =>
        !line.includes('"POST /legion/v1/claims/turn-outcome"') &&
        !line.includes('"POST /legion/v1/claims/activity"')
    )
    .join("\n");
  expect(registered.length - fewer.length).toBeGreaterThan(0);
  writeFileSync(server, fewer);
  const engine = path.join(daemon, "internal", "workflow", "engine.go");
  const taking = readFileSync(engine, "utf8");
  const refusing = taking.replace(
    /\treported := phase\.Of\(fact\.Role, issue\.Phase\)\n\tcurrent := reported == issue\.Phase\n\tif !current \{\n[\s\S]*?\n\t\}\n/,
    (block) =>
      `\tif RoleFor(issue.Phase) != fact.Role {\n\t\treturn refused("HANDOFF_NOT_CURRENT_PHASE", fmt.Sprintf("${OLDER_SENTENCE}", fact.Role, issue.Phase, issue.Key)), nil\n\t}\n${block.slice(0, block.indexOf("\tif !current {"))}`
  );
  expect(refusing).not.toBe(taking);
  writeFileSync(engine, refusing);
  const route = path.join(daemon, "internal", "api", "handoff.go");
  const left = readFileSync(route, "utf8");
  const ofIssue = left.replace(
    '\tof := phase.Of(grant.Role, issue.Phase)\n\tif of == "" {\n\t\tof = issue.Phase\n\t}\n\tword, writer, handoffPhase := phase.Handoff(of)\n\tnamesRecord := req.HandoffID != "" || req.HandoffVersion != 0\n\trecordBacked := handoffPhase && writer == grant.Role && (of == issue.Phase || namesRecord)\n',
    '\tof := issue.Phase\n\tword, writer, handoffPhase := phase.Handoff(issue.Phase)\n\tnamesRecord := req.HandoffID != "" || req.HandoffVersion != 0\n\trecordBacked := handoffPhase && writer == grant.Role\n'
  );
  expect(ofIssue).not.toBe(left);
  writeFileSync(route, ofIssue);
  expect(labelDaemon(checkout)).toEqual({ lateRole: "refused", recordRoute: "issue" });
  older = checkout;
  return checkout;
}

/** A report of one turn under the claim this stand-in serves, for the role `role` (the plugin's
 * `agent_end` hook), with `extra` beside it or over it. */
function turn(role: LegionRole, sequence: number, outcome: string, extra: object = {}) {
  return {
    claimToken: roleToken("skills1", "LWEVAL-1", role),
    sessionId: "01a0f2cc",
    secret: SECRET,
    generation: 1,
    sequence,
    outcome,
    ...extra,
  };
}

// Catches: a head stand-in with no `claims/turn-outcome`, which answered every turn this
// checkout's plugin reports 404 (all twenty head runs of the kept batch logged one), and one that
// counts the turns otherwise than the daemon does. The daemon keeps one report per (generation,
// sequence), so a repeat replaces its report and is counted once (supervise's
// `recordTurnOutcome`), answers the run of refused turns the reports end with (`RefusedRun`), and
// fails the claim when that run reaches the launch failure limit, which the rig runs at its
// default: read here out of the daemon's config source, as the stand-in reads it.
test.skipIf(skip)(
  "on the head label, a turn outcome is answered with the run of refused turns the daemon counts",
  async () => {
    const limit = countDefault(CHECKOUT, "launch_failure_limit");
    expect(limit).toBeGreaterThan(2);
    const daemon = await standin("reviewer");
    try {
      const report = async (sequence: number, outcome: string) => {
        const answer = await daemon.post(
          "/legion/v1/claims/turn-outcome",
          turn("reviewer", sequence, outcome)
        );
        return [answer.status, await answer.json()];
      };
      expect(await report(1, "clean")).toEqual([200, { refusedRun: 0, failed: false }]);
      expect(await report(2, "refused")).toEqual([200, { refusedRun: 1, failed: false }]);
      expect(await report(3, "refused")).toEqual([200, { refusedRun: 2, failed: false }]);
      expect(await report(3, "refused")).toEqual([200, { refusedRun: 2, failed: false }]);
      for (let sequence = 4; sequence < limit + 1; sequence += 1)
        expect(await report(sequence, "refused")).toEqual([
          200,
          { refusedRun: sequence - 1, failed: false },
        ]);
      expect(await report(limit + 1, "refused")).toEqual([
        200,
        { refusedRun: limit, failed: true },
      ]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a turn outcome taken that the daemon's route refuses: a sequence or a generation of 0,
// an outcome neither clean nor refused, and a caller named neither way or both ways are 400; a
// secret or a boot token that is not the claim's is 403; and a report under the launch's boot
// token for another generation than the launch's is 409 (`turnOutcome` in
// packages/daemon/internal/api/claims.go).
test.skipIf(skip)(
  "a turn outcome the daemon's route refuses is refused as it refuses it",
  async () => {
    const daemon = await standin("reviewer");
    try {
      const boot = { generation: 1, sequence: 1, outcome: "clean" };
      const answers: [string, number][] = [];
      for (const [name, body] of [
        ["no sequence", turn("reviewer", 0, "clean")],
        ["no generation", turn("reviewer", 1, "clean", { generation: 0 })],
        ["another outcome", turn("reviewer", 1, "maybe")],
        ["neither caller", boot],
        ["both callers", turn("reviewer", 1, "clean", { bootToken: "rig-boot-token" })],
        ["another secret", turn("reviewer", 1, "clean", { secret: "not-it" })],
        ["another boot token", { ...boot, bootToken: "not-it" }],
        ["another generation", { ...boot, generation: 2, bootToken: "rig-boot-token" }],
        ["the launch's boot token", { ...boot, bootToken: "rig-boot-token" }],
      ] as const)
        answers.push([name, (await daemon.post("/legion/v1/claims/turn-outcome", body)).status]);
      expect(answers).toEqual([
        ["no sequence", 400],
        ["no generation", 400],
        ["another outcome", 400],
        ["neither caller", 400],
        ["both callers", 400],
        ["another secret", 403],
        ["another boot token", 403],
        ["another generation", 409],
        ["the launch's boot token", 200],
      ]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that answers a route because it has a case for it rather than because the
// run's label's daemon registers it. An older daemon, main `ff2a10ef`, registers no
// `claims/turn-outcome`, so its runs' plugin never posts one and a daemon of its would answer one
// 404; a daemon registering no such route, read out of its own source, gets that 404 here, and
// the routes it does register are answered.
test.skipIf(skip)(
  "a route the label's daemon does not register is answered 404, whatever the stand-in has a case for",
  async () => {
    const daemon = await standin("reviewer", "", undefined, { checkout: olderCheckout() });
    try {
      expect(
        (await daemon.post("/legion/v1/claims/turn-outcome", turn("reviewer", 1, "clean"))).status
      ).toBe(404);
      expect((await daemon.post("/legion/v1/claims/activity", { secret: SECRET })).status).toBe(
        404
      );
      expect(await daemon.phase()).toBe("reviewing");
      expect(
        daemon
          .logged()
          .filter((line) => line.status === 404)
          .map((line) => [line.method, line.path])
      ).toEqual([
        ["POST", "/legion/v1/claims/turn-outcome"],
        ["POST", "/legion/v1/claims/activity"],
      ]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that goes on taking a claim's reports after the daemon failed it. The
// daemon fails the claim when its run of refused turns reaches the launch failure limit, and
// every write of a failed claim clears the capability its secret authenticated (supervise's
// `NormalizeClaim`), so each route that authenticates the secret refuses it `InvalidSecret`
// from then on: the plugin's next turn outcome, its activity report and its grant. The sentence
// is the daemon's own claim refusal, read out of its claim source. The failing report's log line
// says it failed the claim, which health reads.
test.skipIf(skip)(
  "once a turn outcome fails the claim, its secret authenticates nothing",
  async () => {
    const limit = countDefault(CHECKOUT, "launch_failure_limit");
    const invalid = claimRefusal(CHECKOUT, "InvalidSecret");
    const daemon = await standin("reviewer");
    try {
      for (let sequence = 1; sequence <= limit; sequence += 1)
        await daemon.post("/legion/v1/claims/turn-outcome", turn("reviewer", sequence, "refused"));
      const answers = [];
      for (const [route, body] of [
        ["/legion/v1/claims/turn-outcome", turn("reviewer", limit + 1, "clean")],
        ["/legion/v1/claims/activity", { secret: SECRET }],
        [
          "/legion/v1/grants",
          { sessionId: "01a0f2cc", secret: SECRET, tree: "LWEVAL-1", issue: "LWEVAL-1" },
        ],
      ] as const) {
        const answer = await daemon.post(route, body);
        answers.push([route, answer.status, await answer.json()]);
      }
      expect(answers).toEqual(
        ["/legion/v1/claims/turn-outcome", "/legion/v1/claims/activity", "/legion/v1/grants"].map(
          (route) => [route, invalid.status, { error: invalid.message }]
        )
      );
      expect(
        daemon
          .logged()
          .filter((line) => line.path === "/legion/v1/claims/turn-outcome")
          .map((line) => [line.status, line.failed])
      ).toEqual([
        ...Array.from({ length: limit - 1 }, () => [200, false]),
        [200, true],
        [invalid.status, undefined],
      ]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in of an older daemon that answers a late completion with this checkout's
// daemon's sentence rather than its own. The sentence, the phase table the boot reads and how the
// daemon takes a completion come out of the checkout the run's built-from names, here a daemon
// that words the refusal in `OLDER_SENTENCE`.
test.skipIf(skip)(
  "a late completion is refused in the sentence of the label's own daemon",
  async () => {
    const checkout = olderCheckout();
    const daemon = await standin("reviewer", "", undefined, { checkout });
    try {
      pullRequest(daemon.dir).requestChanges();
      expect(await daemon.phase()).toBe("implementing");
      const refused = await daemon.complete(true);
      expect(refused.status).toBe(409);
      const filled = ["reviewer", "implementing", "LWEVAL-1"].reduce(
        (text, value) => text.replace(/%s/, value),
        OLDER_SENTENCE
      );
      expect(await refused.json()).toEqual({ code: "HANDOFF_NOT_CURRENT_PHASE", error: filled });
      expect(lateRefusal("reviewer", "implementing", checkout).error).toBe(filled);
      expect(lateRefusal("reviewer", "implementing").error).not.toBe(filled);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches the same against an older daemon's own source, main `ff2a10ef`'s (2026-11-07), where a
// checkout of it is on the host (SKILL_SCENARIOS_OLDER_CHECKOUT names it; the base label was once
// built from it, and is built from main at the head's newest forward merge now, so the base
// label's checkout is no checkout of it): its engine writes the refusal as
// `refused("HANDOFF_NOT_CURRENT_PHASE", fmt.Sprintf(<format>, fact.Role, issue.Phase,
// issue.Key))`, its own source reads as the older shapes, and the stand-in answers in that
// format filled.
const olderHost = process.env.SKILL_SCENARIOS_OLDER_CHECKOUT;
if (olderHost === undefined)
  console.warn(
    `Skipped the older daemon's own-sentence row in ${import.meta.path}. ` +
      "SKILL_SCENARIOS_OLDER_CHECKOUT names no checkout of main ff2a10ef to read its " +
      "packages/daemon from. Name one and run the tests again."
  );
test.skipIf(skip || olderHost === undefined)(
  "an older daemon's late completion is refused in that daemon's own sentence",
  async () => {
    const checkout = olderHost ?? "";
    const engine = readFileSync(
      path.join(checkout, "packages", "daemon", "internal", "workflow", "engine.go"),
      "utf8"
    );
    const [, format = ""] =
      /refused\("HANDOFF_NOT_CURRENT_PHASE", fmt\.Sprintf\("((?:[^"\\]|\\.)*)", fact\.Role, issue\.Phase, issue\.Key\)/.exec(
        engine
      ) ?? [];
    expect(format).not.toBe("");
    expect(labelDaemon(checkout)).toEqual({ lateRole: "refused", recordRoute: "issue" });
    const daemon = await standin("reviewer", "", undefined, { checkout });
    try {
      pullRequest(daemon.dir).requestChanges();
      const refused = await daemon.complete(true);
      expect(refused.status).toBe(409);
      expect(await refused.json()).toEqual({
        code: "HANDOFF_NOT_CURRENT_PHASE",
        error: ["reviewer", "implementing", "LWEVAL-1"].reduce(
          (text, value) => text.replace(/%s/, value),
          format
        ),
      });
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in with no `claims/release-records`, which both labels' daemons register and
// the tool's `read_release_records` posts, and one answering it otherwise than `releaseRecords`:
// the body's contract, then the claim's secret, then every release, resume and hold since the
// time named, of which the rig's world has none, as three empty lists.
test.skipIf(skip)(
  "the release records are answered as the daemon answers them in a world that released no pod",
  async () => {
    const daemon = await standin("reviewer");
    try {
      const records = (body: object) =>
        daemon.post("/legion/v1/claims/release-records", {
          claimToken: roleToken("skills1", "LWEVAL-1", "reviewer"),
          secret: SECRET,
          since: "2026-11-01T00:00:00Z",
          ...body,
        });
      const read = await records({});
      expect([read.status, await read.json()]).toEqual([
        200,
        { releases: [], resumes: [], holds: [] },
      ]);
      expect((await records({ since: "yesterday" })).status).toBe(400);
      expect((await records({ secret: "not-it" })).status).toBe(403);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a stand-in that answers a run whose daemon it cannot read the routes of, as some other
// daemon would answer it. The routes are read out of the checkout the run's built-from names, and
// a run with none fails the boot.
test.skipIf(skip)(
  "a run whose built-from names no checkout fails the boot",
  () => {
    const dir = path.join(work, "reviewer-head-900");
    mkdirSync(dir, { recursive: true });
    writeFileSync(path.join(dir, "boot-token"), "rig-boot-token\n");
    writeFileSync(
      path.join(dir, "world.json"),
      JSON.stringify({
        key: "LWEVAL-1",
        branch: "legion/LWEVAL-1",
        pr: 1001,
        head: "0".repeat(40),
        repo: WORLD_REPO,
        base: WORLD_BASE,
      })
    );
    const boot = spawnChecked([
      "bun",
      DAEMON_STANDIN,
      "0",
      path.join(dir, "daemon.log"),
      path.join(dir, "boot-token"),
      "skills1",
      "LWEVAL-1",
      dir,
      "reviewer",
    ]);
    expect(boot.exitCode).toBe(2);
    expect(boot.stderr.toString()).toContain("built-from names no checkout");
  },
  600_000
);

// Catches: a stand-in whose record names a repository or a default branch the run's world has
// not got. Agents read the record for the repository their `gh` calls name and for the branch
// their work goes above, so a second name is a second world, which cost recorded runs their
// review (rig.sh's worker world comment holds that story). Both are the world's own, read
// out of the run's world.json: the branch here is not `main`, so a stand-in that wrote the
// rig's usual default rather than reading the world fails this row.
test.skipIf(skip)(
  "the record names the repository and the default branch of the run's own world",
  async () => {
    const daemon = await standin("reviewer", "", { repo: "widgets-org/widgets", base: "trunk" });
    try {
      const { issue, repositories } = await daemon.record();
      expect(issue?.repo).toBe("widgets-org/widgets");
      expect(issue?.defaultBranch).toBe("trunk");
      expect(repositories.mapped).toEqual([{ repo: "widgets-org/widgets", default: true }]);
      expect(repositories.owners).toEqual(["widgets-org"]);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// And the world rig.sh builds is served as it stands, so no row above reads a world of this
// file's own making.
test.skipIf(skip)(
  "the record names rig.sh's own worker world",
  async () => {
    const daemon = await standin("reviewer");
    try {
      const { issue } = await daemon.record();
      expect(issue?.repo).toBe(WORLD_REPO);
      expect(issue?.defaultBranch).toBe(WORLD_BASE);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// Catches: a log nothing writes until the agent's first call, where a run whose agent never
// reached the daemon leaves no daemon.log and scores as the rig error of a record nothing
// wrote, rather than as the run that moved nothing (score.ts's `unscored`). The line is the
// stand-in's own, written when it starts listening, as record-publishes.ts writes its
// `recording` line, and it is the record's first line, before any answer of the run's.
test.skipIf(skip)(
  "the log opens with the stand-in's own line, written before any request is answered",
  async () => {
    const daemon = await standin("reviewer");
    try {
      expect(daemon.logged()).toMatchObject([{ opened: "daemon.log" }]);
      expect(await daemon.phase()).toBe("reviewing");
      const after = daemon.logged();
      expect(after[0]).toMatchObject({ opened: "daemon.log" });
      expect(after[1]).toMatchObject({ path: "/legion/v1/state", status: 200 });
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// What daemon-pane.go's `refusal not-current-phase` prints is two lines, the code and then the
// daemon's message, and the stand-in answers a completion of a phase its role does not run with
// exactly that pair or refuses to boot (`refusalAtBoot`). Catches: a printout half read taken
// for a refusal — no message, an empty code, or a message that runs onto a third line, each a
// sentence the daemon never sends — which no row that starts the stand-in on today's daemon can
// reach, since that daemon prints the two lines. No go is needed: this reads the printout alone.
test("a refusal printout is the code and the message on two lines of their own, or no refusal", () => {
  const message = "issue LWEVAL-1 is in accepting, which implementer does not run";
  expect(refusalIn(`HANDOFF_NOT_CURRENT_PHASE\n${message}\n`)).toEqual({
    code: "HANDOFF_NOT_CURRENT_PHASE",
    message,
  });
  for (const printed of [
    "",
    "HANDOFF_NOT_CURRENT_PHASE\n",
    "HANDOFF_NOT_CURRENT_PHASE\n\n",
    `\n${message}\n`,
    `HANDOFF_NOT_CURRENT_PHASE\n${message}`,
    `HANDOFF_NOT_CURRENT_PHASE\n${message}\nand a third line\n`,
  ])
    expect([printed, refusalIn(printed)]).toEqual([printed, undefined]);
});

// The daemon reads the handoff record a completion names back from Dispatch and holds it to the
// issue, the phase's handoff word and the version the completion reports before it records
// anything (packages/daemon/internal/api/handoff.go's `handoffRecord`). Catches: a stand-in that
// takes every named record as found — the success it gave a completion naming a record Dispatch
// has not got, or one of another issue, phase or version — and one whose refusal changed what a
// later completion is answered: each refused completion records nothing, so the one naming the
// round's own record is still taken.
test.skipIf(skip)(
  "a completion naming a record Dispatch has not got, or another issue's, phase's or version's, is refused as the daemon refuses it",
  async () => {
    const record = (issue: string, phase: string, version: number) => ({
      issue_key: issue,
      phase,
      version,
      author: { kind: "session", id: "01a0f2cc" },
    });
    const daemon = await standin("reviewer", "", undefined, {
      records: {
        "h-review": record("LWEVAL-1", "review", 1),
        "h-other-issue": record("LWEVAL-2", "review", 1),
        "h-plan": record("LWEVAL-1", "plan", 1),
      },
    });
    try {
      const read = "`dispatch handoff-read --issue LWEVAL-1 --phase review`";
      const gone = await daemon.complete(true, { handoffId: "h-gone" });
      expect([gone.status, await gone.json()]).toEqual([
        404,
        {
          code: "HANDOFF_RECORD_NOT_FOUND",
          error: `Dispatch has no handoff record h-gone (HANDOFF_NOT_FOUND: handoff h-gone not found): name the record ${read} shows, or write one with \`dispatch handoff-write --issue LWEVAL-1 --phase review --body-file <file>\`, then complete again; this completion changed nothing`,
        },
      ]);
      for (const [id, version, held] of [
        ["h-other-issue", 1, "version 1 of LWEVAL-2's review handoff"],
        ["h-plan", 1, "version 1 of LWEVAL-1's plan handoff"],
        ["h-review", 2, "version 1 of LWEVAL-1's review handoff"],
      ] as const) {
        const named = await daemon.complete(true, { handoffId: id, handoffVersion: version });
        expect([id, named.status, await named.json()]).toEqual([
          id,
          409,
          {
            code: "HANDOFF_RECORD_MISMATCH",
            error: `handoff record ${id} is ${held}, and this completion is of LWEVAL-1's review handoff at version ${version}: name the record ${read} shows; this completion changed nothing`,
          },
        ]);
      }
      expect((await daemon.complete(true)).status).toBe(200);
    } finally {
      daemon.stop();
    }
  },
  600_000
);

// The workflow takes a phase worker's move back only to a phase before the one the issue stands
// in, and only from the role running that phase, and refuses every other move 409
// BACKWARD_REFUSED in its own sentence (packages/daemon/internal/workflow/engine.go's
// `backward` and `backwardRefusal`); a move it takes moves the issue there. Catches: a stand-in
// that answers `{}` for any `to` — a move forward, to the phase the issue stands in, to
// `admitted`, or by a role not running the phase — and so tells the agent the issue moved when
// the daemon moved nothing; and one that answers a move it takes `{}` and moves nothing, so
// every later read of the record still shows the phase the move left.
test.skipIf(skip)(
  "a backward move the workflow does not serve is refused BACKWARD_REFUSED, and one it serves moves the issue",
  async () => {
    /** A move of the issue back to `to` through the stand-in that `post` reaches, on a grant
     * minted for it, as the tool's `request_backward_move` makes one. */
    const move = async (
      post: (route: string, payload: unknown) => Promise<Response>,
      to: string
    ) => {
      const minted = await (
        await post("/legion/v1/grants", {
          sessionId: "01a0f2cc",
          secret: SECRET,
          tree: "LWEVAL-1",
          issue: "LWEVAL-1",
        })
      ).json();
      const { grantId } = z.object({ grantId: z.string() }).parse(minted);
      const moved = await post("/legion/v1/phase/backward", {
        grantId,
        to,
        reason: "the fix needs code",
      });
      return [moved.status, await moved.json()];
    };
    const refused = (why: string) => [409, { code: "BACKWARD_REFUSED", error: why }];
    const implementer = await standin("implementer", "reviewing");
    try {
      expect(await move(implementer.post, "implementing")).toEqual(
        refused(
          "a backward move is the role running the issue's phase: reviewing of LWEVAL-1 is the reviewer's, so the implementer's move to implementing changed nothing"
        )
      );
      expect(await implementer.phase()).toBe("reviewing");
    } finally {
      implementer.stop();
    }
    const reviewer = await standin("reviewer");
    try {
      for (const to of ["accepting", "reviewing", "admitted"])
        expect(await move(reviewer.post, to)).toEqual(
          refused(
            `a backward move reaches an earlier phase, and no backward row of reviewing serves ${to}; the reviewer's move of LWEVAL-1 changed nothing`
          )
        );
      expect(await reviewer.phase()).toBe("reviewing");
      expect(await move(reviewer.post, "implementing")).toEqual([200, {}]);
      expect(await reviewer.phase()).toBe("implementing");
    } finally {
      reviewer.stop();
    }
  },
  600_000
);
