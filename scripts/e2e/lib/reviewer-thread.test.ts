import { beforeAll, describe, expect, test } from "bun:test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { scriptFunctions } from "./script-functions";

// review_threads, reviewer_thread and the thread checks, taken from lib/workflow.sh by name and run
// against a stubbed gh api graphql answer. Every role acts as the one App, and GitHub takes no
// review but a COMMENT from the pull request's author, which that App is: reviewer_thread must
// pick the one thread the App's COMMENT review opened, never one another account opened, and the
// checks can tell the App's comments from another account's but not one role's from another's. The
// App's login is the one read_app_logins would have read from GitHub; GraphQL names its bot by the
// bare slug.
const fn = scriptFunctions(join(import.meta.dir, "workflow.sh"));
const app = "example-implement";

const dir = temporaryDirectory("reviewer-thread-test");
const bin = join(dir, "bin");
beforeAll(() => {
  mkdirSync(bin);
  // The stub answers every `gh api graphql …` call with the fixture GH_GRAPHQL_FIXTURE names,
  // whatever variables or query text the call carries: review_threads reads only the response
  // body.
  writeFileSync(join(bin, "gh"), '#!/bin/sh\ncat "$GH_GRAPHQL_FIXTURE"\n', { mode: 0o755 });
});

// threadsFixture writes a reviewThreads GraphQL page: THREADS is an array of
// { author, state, reviewState } for each thread's one comment.
function threadsFixture(
  threads: { author: string; state: string; reviewState: string | null }[]
): string {
  const path = join(dir, `fixture-${Math.random().toString(36).slice(2)}.json`);
  const nodes = threads.map((t) => ({
    id: `T_${t.author}_${t.reviewState ?? "none"}_${Math.random().toString(36).slice(2, 6)}`,
    isResolved: false,
    comments: {
      pageInfo: { hasNextPage: false },
      nodes: [
        {
          author: { login: t.author },
          body: "a finding",
          state: t.state,
          pullRequestReview: t.reviewState === null ? null : { state: t.reviewState },
        },
      ],
    },
  }));
  writeFileSync(
    path,
    JSON.stringify({
      data: {
        repository: {
          pullRequest: {
            reviewThreads: { pageInfo: { hasNextPage: false }, nodes },
          },
        },
      },
    })
  );
  return path;
}

function run(call: string, fixture = threadsFixture([])) {
  const result = Bun.spawnSync(
    [
      "bash",
      "-c",
      `set -Eeuo pipefail
repo=sjawhar/legion-smoke pr_number=1
app_login='${app}[bot]'
${fn("role_app")}
${fn("role_app_slug")}
${fn("review_threads")}
${fn("reviewer_thread")}
${fn("thread_resolved_by")}
${fn("thread_resolved_with_reply")}
${fn("bot_thread_resolved")}
${call}
`,
    ],
    { env: { PATH: `${bin}:${process.env.PATH}`, GH_GRAPHQL_FIXTURE: fixture } }
  );
  return {
    code: result.exitCode,
    stdout: result.stdout.toString().trim(),
    stderr: result.stderr.toString().trim(),
  };
}

describe("reviewer_thread", () => {
  test("picks the thread the App's COMMENT review opened, not another account's", () => {
    const fixture = threadsFixture([
      { author: "another-review", state: "SUBMITTED", reviewState: "COMMENTED" },
      { author: app, state: "SUBMITTED", reviewState: "COMMENTED" },
    ]);
    const { code, stdout, stderr } = run("reviewer_thread", fixture);
    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(stdout).toStartWith(`T_${app}_COMMENTED_`);
  });

  test("fails naming the count when the App opened two threads", () => {
    const fixture = threadsFixture([
      { author: app, state: "SUBMITTED", reviewState: "COMMENTED" },
      { author: app, state: "SUBMITTED", reviewState: "COMMENTED" },
    ]);
    const { code, stdout, stderr } = run("reviewer_thread", fixture);
    expect(code).not.toBe(0);
    expect(stdout + stderr).toContain(
      "the Legion reviewer opened 2 review threads, want exactly one"
    );
  });

  test("fails naming zero when only another account opened a thread, or the App's comment is in no submitted COMMENT review", () => {
    for (const threads of [
      [{ author: "another-review", state: "SUBMITTED", reviewState: "CHANGES_REQUESTED" }],
      [{ author: app, state: "PENDING", reviewState: "PENDING" }],
      [],
    ]) {
      const { code, stdout, stderr } = run("reviewer_thread", threadsFixture(threads));
      expect(code).not.toBe(0);
      expect(stdout + stderr).toContain("the Legion reviewer opened 0 review threads");
    }
  });

  test("fails saying so when read_app_logins has not run", () => {
    const fixture = threadsFixture([{ author: app, state: "SUBMITTED", reviewState: "COMMENTED" }]);
    const { code, stderr } = run("app_login=\nreviewer_thread", fixture);
    expect(code).not.toBe(0);
    expect(stderr).toContain("role_app: no App login: read_app_logins has not run");
  });
});

describe("the thread checks", () => {
  /** A review_thread record: resolved or not, its comments by author, all submitted. */
  const record = (isResolved: boolean, ...authors: string[]) => {
    const path = join(dir, `record-${Math.random().toString(36).slice(2)}.json`);
    writeFileSync(
      path,
      JSON.stringify({
        id: "T_1",
        isResolved,
        comments: authors.map((author) => ({ author, body: "a reply", state: "SUBMITTED" })),
      })
    );
    return path;
  };

  test("thread_resolved_by holds for a resolved thread whose newest comment is the App's, and no other", () => {
    expect(run(`thread_resolved_by ${record(true, app, app)}`).code).toBe(0);
    expect(run(`thread_resolved_by ${record(false, app, app)}`).code).not.toBe(0);
    expect(run(`thread_resolved_by ${record(true, app, "sjawhar-agent")}`).code).not.toBe(0);
  });

  test("thread_resolved_with_reply holds for a resolved thread with a reply of the App's after its opening", () => {
    expect(run(`thread_resolved_with_reply ${record(true, "sjawhar-agent", app)}`).code).toBe(0);
    expect(run(`thread_resolved_with_reply ${record(false, "sjawhar-agent", app)}`).code).not.toBe(
      0
    );
    expect(run(`thread_resolved_with_reply ${record(true, app, "sjawhar-agent")}`).code).not.toBe(
      0
    );
  });

  test("bot_thread_resolved counts the App's replies, so a second role's answer is one more than the first's", () => {
    // bot_thread_replies prints the thread's isResolved, then each reply's author and first line.
    const replies = (resolved: boolean, ...authors: string[]) =>
      `bot_thread_replies() { printf '%s\\n' ${resolved} ${authors.map((a) => `'${a}\tanswered'`).join(" ")}; }\n`;
    expect(run(`${replies(true, app)}bot_thread_resolved C_1`).code).toBe(0);
    expect(run(`${replies(true, app)}bot_thread_resolved C_1 2`).code).not.toBe(0);
    expect(run(`${replies(true, app, app)}bot_thread_resolved C_1 2`).code).toBe(0);
    expect(run(`${replies(false, app, app)}bot_thread_resolved C_1`).code).not.toBe(0);
    expect(run(`${replies(true, "sjawhar-agent")}bot_thread_resolved C_1`).code).not.toBe(0);
  });
});
