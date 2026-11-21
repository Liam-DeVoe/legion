import { beforeAll, describe, expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { expectNoMissingHelpers, nameMissingHelpers, scriptFunctions } from "./script-functions";

// The reviewer's verdict as the stage proofs drive and read it, taken from lib/workflow.sh by
// name: request_changes_as_reviewer has the reviewer pane send `changes` through its handoff
// completion, the waits read the daemon's record of the verdict (its `verdicts` rows, which a
// stand-in db_value answers), assert_reviewer_verdict holds that record to the pull request's head
// and the daemon's state, and assert_legion_verdict reads the check run the daemon writes from it.
// A stand-in gh answers each read from a fixture through the call's own --jq filter, as gh does,
// and logs the path it was asked.
const workflow = join(import.meta.dir, "workflow.sh");
const apps = join(import.meta.dir, "github-apps.sh");
const fn = scriptFunctions(workflow);
const appsFn = scriptFunctions(apps);
const helperGuard = nameMissingHelpers(workflow);
const app = "example-implement[bot]";
const claim = "legion-acme-acme-7-reviewer";
const head = "1111111111111111111111111111111111111111";
const older = "2222222222222222222222222222222222222222";

const dir = temporaryDirectory("reviewer-verdict-test");
const bin = join(dir, "bin");
beforeAll(() => {
  mkdirSync(bin);
  // `gh api PATH [--jq FILTER]`: the pull request, its reviews or its check runs, by PATH, a string
  // result printed raw and anything else as compact JSON, as gh prints its --jq results. The check
  // runs come from GH_RUNS, or from GH_RUNS_LATER once GH_RUNS has been read once, so a row can
  // show a run that lands after the first read.
  writeFileSync(
    join(bin, "gh"),
    `#!/usr/bin/env bash
path= jq=.
shift
while [ $# -gt 0 ]; do
  case $1 in --jq) jq=$2; shift 2 ;; -*) shift ;; *) path=$1; shift ;; esac
done
printf '%s\\n' "$path" >>"$GH_LOG"
case $path in
  */check-runs\\?*)
    [ -z "\${GH_DOWN:-}" ] || exit 1
    fixture=$GH_RUNS
    if [ -n "\${GH_RUNS_LATER:-}" ]; then
      [ -e "$GH_LOG.read" ] && fixture=$GH_RUNS_LATER
      : >"$GH_LOG.read"
    fi ;;
  */reviews) fixture=$GH_REVIEWS ;;
  *) fixture=$GH_PULL ;;
esac
jq -rc "$jq" "$fixture"
`,
    { mode: 0o755 }
  );
});

let fixtures = 0;
const fixture = (value: unknown) => {
  const path = join(dir, `fixture-${++fixtures}.json`);
  writeFileSync(path, JSON.stringify(value));
  return path;
};
/** A check runs answer of GitHub's shape: legion-verdict runs on `head` of the given Apps. */
const runs = (
  ...made: { app: number; status?: string; conclusion?: string | null; sha?: string }[]
) =>
  fixture({
    total_count: made.length,
    check_runs: made.map((r, i) => ({
      id: 1000 + i,
      name: "legion-verdict",
      head_sha: r.sha ?? head,
      status: r.status ?? "completed",
      conclusion: r.conclusion === undefined ? "success" : r.conclusion,
      app: { id: r.app, slug: `app-${r.app}` },
    })),
  });
/** The daemon's state for ACME-7: its pull request at `at` with the round decision `decision`. */
const state = (at: string, decision?: string) =>
  JSON.stringify({
    issues: {
      "ACME-7": { pullRequest: { head: at, ...(decision ? { reviewDecision: decision } : {}) } },
    },
  });
/** The rows reviewer_verdicts reads, oldest first, as db_value prints them. */
const verdicts = (...rows: [string, string, string][]) =>
  rows.map((row) => row.join(" ")).join("\n");

interface Env {
  verdicts?: string;
  state?: string;
  runs?: string;
  runsLater?: string;
  keyCommand?: string;
  down?: boolean;
  bound?: string;
  body?: string;
  reviews?: string;
  note?: string;
}

function run(call: string, env: Env = {}) {
  const sent = join(dir, `sent-${++fixtures}`);
  const log = join(dir, `gh-${fixtures}.log`);
  writeFileSync(log, "");
  const result = Bun.spawnSync(
    [
      "bash",
      "-c",
      `set -Eeuo pipefail
${helperGuard}
repo=acme/widgets pr_number=614 project=ACME
implement_app_id=101 app_login='${app}'
implement_app_key_command=\${KEY_COMMAND:-}
legion_verdict_bound=\${LEGION_VERDICT_BOUND:-0} legion_verdict_poll=0.1
note() { printf '   %s\\n' "$*"; }
fail() { printf 'FAIL %s\\n' "$*" >&2; exit 1; }
send_agent() { printf '%s' "$3" >"$SENT"; }
until_true() { shift 2; "$@"; }
db_value() { printf '%s\\n' "$VERDICTS"; }
daemon_state() { printf '%s\\n' "$STATE"; }
${fn("claim_token")}
${fn("role_app")}
${appsFn("app_key_mode")}
${fn("request_changes_as_reviewer")}
${fn("reviewer_verdicts")}
${fn("reviewer_newest_verdict")}
${fn("reviewer_verdict_recorded")}
${fn("reviewer_approved_head")}
${fn("reviewer_commented")}
${fn("assert_reviewer_verdict")}
${fn("legion_verdict_runs")}
${fn("assert_legion_verdict")}
${call}
`,
    ],
    {
      env: {
        PATH: `${bin}:${process.env.PATH}`,
        SENT: sent,
        GH_LOG: log,
        GH_PULL: fixture({ number: 614, head: { sha: head } }),
        GH_REVIEWS: env.reviews ?? fixture([]),
        GH_RUNS: env.runs ?? runs(),
        GH_RUNS_LATER: env.runsLater ?? "",
        GH_DOWN: env.down ? "1" : "",
        VERDICTS: env.verdicts ?? "",
        STATE: env.state ?? state(head),
        KEY_COMMAND: env.keyCommand ?? "",
        LEGION_VERDICT_BOUND: env.bound ?? "",
        BODY: env.body ?? "",
        NOTE: env.note ?? "",
      },
    }
  );
  let message = "";
  try {
    message = readFileSync(sent, "utf8");
  } catch {}
  const stdout = result.stdout.toString().trim();
  const stderr = result.stderr.toString().trim();
  expectNoMissingHelpers(stdout, stderr);
  return {
    code: result.exitCode,
    stdout,
    stderr,
    message,
    asked: readFileSync(log, "utf8").trim().split("\n").filter(Boolean),
  };
}

const roundBody = "Stage 3 proof review, round 2: append the line `x` to the end of the file.";

describe("request_changes_as_reviewer", () => {
  test("has the reviewer send changes through its completion, its findings a COMMENT review, with no footer asked", () => {
    const { code, stderr, message } = run(`request_changes_as_reviewer ACME-7 1 "$BODY"`, {
      body: roundBody,
      verdicts: verdicts(["changes", head, claim]),
    });
    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(message).toContain(
      `your verdict on pull request #614 in acme/widgets is changes. Post your findings as one COMMENT review whose body is this quoted text: "${roundBody}" Take the round's other steps`
    );
    expect(message).toContain(
      "complete the reviewer handoff with verdict changes on the head you reviewed"
    );
    // Nothing reads a review's footer, and GitHub takes no request for changes from the pull
    // request's author, which the one App is.
    expect(message).not.toContain("footer");
    expect(message).not.toContain("REQUEST_CHANGES");
  });

  test("puts a round's note outside the quoted body", () => {
    const note = "Reply on the bot's thread in your own words.";
    const { code, message } = run(`request_changes_as_reviewer ACME-7 1 "$BODY" "$NOTE"`, {
      body: roundBody,
      note,
      verdicts: verdicts(["changes", head, claim]),
    });
    expect(code).toBe(0);
    expect(message).toContain(`"${roundBody}" ${note} Take the round's other steps`);
  });

  test("waits for the daemon's record of the round's changes from the reviewer's own claim", () => {
    const decided = (rows: string, round = 2) =>
      run(`request_changes_as_reviewer ACME-7 ${round} "$BODY"`, {
        body: roundBody,
        verdicts: rows,
      }).code;
    expect(decided(verdicts(["changes", older, claim], ["changes", head, claim]))).toBe(0);
    // Before the round's count of verdicts, the round's own is not recorded yet.
    expect(decided(verdicts(["changes", head, claim]))).not.toBe(0);
    // A verdict from another claim is no verdict of this reviewer's.
    expect(
      decided(verdicts(["changes", older, claim], ["changes", head, "legion-acme-acme-7-tester"]))
    ).not.toBe(0);
    // The newest verdict is not the round's changes.
    expect(decided(verdicts(["changes", older, claim], ["approve", head, claim]))).not.toBe(0);
    expect(decided("")).not.toBe(0);
  });
});

describe("reviewer_approved_head", () => {
  test("holds for the reviewer's approve, from its claim, on the pull request's current head alone", () => {
    const approved = (rows: string) =>
      run("reviewer_approved_head ACME-7", { verdicts: rows }).code;
    expect(approved(verdicts(["changes", older, claim], ["approve", head, claim]))).toBe(0);
    expect(approved(verdicts(["approve", older, claim]))).not.toBe(0);
    expect(approved(verdicts(["changes", head, claim]))).not.toBe(0);
    expect(approved(verdicts(["approve", head, "legion-acme-acme-7-implementer"]))).not.toBe(0);
  });
});

describe("reviewer_commented", () => {
  test("holds for the App's COMMENT review with a body, never a thread reply or another account's review", () => {
    interface Review {
      id: number;
      user: { login: string };
      state: string;
      body: string;
    }
    const review = (login: string, state: string, body: string): Review => ({
      id: fixtures,
      user: { login },
      state,
      body,
    });
    const commented = (...reviews: Review[]) =>
      run("reviewer_commented", { reviews: fixture(reviews) }).code;
    expect(commented(review(app, "COMMENTED", "findings"))).toBe(0);
    // A reply on a review thread is a COMMENTED review with an empty body.
    expect(commented(review(app, "COMMENTED", ""))).not.toBe(0);
    // GraphQL's bare slug is not the App's REST login, and a user could hold it.
    expect(commented(review(app.replace("[bot]", ""), "COMMENTED", "findings"))).not.toBe(0);
    expect(commented(review(app, "APPROVED", "looks good"))).not.toBe(0);
    expect(commented()).not.toBe(0);
  });
});

describe("assert_reviewer_verdict", () => {
  test("holds the daemon's newest verdict to the pull request's head and the state's decision, and reads no check in token mode", () => {
    const { code, stderr, stdout, asked } = run("assert_reviewer_verdict ACME-7 1 changes", {
      verdicts: verdicts(["changes", head, claim]),
      state: state(head, "changes_requested"),
    });
    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(stdout).toContain(
      `the daemon records ACME-7's reviewer verdict changes at round 1 on ${head}`
    );
    expect(asked.filter((path) => path.includes("check-runs"))).toEqual([]);
  });

  test("fails naming what the daemon recorded when the verdict is of another head, claim or word", () => {
    for (const [rows, shown] of [
      [verdicts(["changes", older, claim]), `changes ${older} ${claim}`],
      [
        verdicts(["changes", head, "legion-acme-acme-7-tester"]),
        `changes ${head} legion-acme-acme-7-tester`,
      ],
      [verdicts(["approve", head, claim]), `approve ${head} ${claim}`],
      ["", "none"],
    ] as const) {
      const { code, stderr } = run("assert_reviewer_verdict ACME-7 1 changes", {
        verdicts: rows,
        state: state(head, "changes_requested"),
      });
      expect(code, rows).toBe(1);
      expect(stderr).toContain(
        `ACME-7's reviewer verdict at round 1 is '${shown}', want 'changes ${head} ${claim}'`
      );
    }
  });

  test("fails when the daemon's state shows another decision for the round", () => {
    const { code, stderr } = run("assert_reviewer_verdict ACME-7 3 approve", {
      verdicts: verdicts(["approve", head, claim]),
      state: state(head, "changes_requested"),
    });
    expect(code).toBe(1);
    expect(stderr).toContain(
      "shows ACME-7's round 3 decision as 'changes_requested', want approved"
    );
  });

  test("in key mode also holds legion-verdict on the head to the verdict's conclusion", () => {
    const keyed = (made: string, verdict: string, decision: string) =>
      run(`assert_reviewer_verdict ACME-7 1 ${verdict}`, {
        verdicts: verdicts([verdict, head, claim]),
        state: state(head, decision),
        runs: made,
        keyCommand: "cat -- /a/key.pem",
      });
    expect(
      keyed(runs({ app: 101, conclusion: "failure" }), "changes", "changes_requested").code
    ).toBe(0);
    expect(keyed(runs({ app: 101, conclusion: "success" }), "approve", "approved").code).toBe(0);
    const wrong = keyed(runs({ app: 101, conclusion: "success" }), "changes", "changes_requested");
    expect(wrong.code).toBe(1);
    expect(wrong.stderr).toContain(
      `legion-verdict on acme/widgets@${head}: want one completed run of App 101 concluding failure, read completed success`
    );
  });
});

describe("assert_legion_verdict", () => {
  test("holds for one completed run of the rig's App with the conclusion, read by check name on the head", () => {
    const { code, stderr, stdout, asked } = run(
      `assert_legion_verdict acme/widgets ${head} success`,
      {
        runs: runs({ app: 101, conclusion: "success" }, { app: 15368, conclusion: "failure" }),
      }
    );
    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(stdout).toContain(
      `legion-verdict on acme/widgets@${head}: one run of App 101, completed success`
    );
    expect(asked).toEqual([
      `repos/acme/widgets/commits/${head}/check-runs?check_name=legion-verdict&filter=all&per_page=100`,
    ]);
  });

  // The control the plan names: a head with no run fails, naming the head.
  test("a head with no run fails, naming the head", () => {
    const { code, stderr } = run(`assert_legion_verdict acme/widgets ${older} success`, {
      runs: runs(),
    });
    expect(code).toBe(1);
    expect(stderr).toBe(
      `FAIL legion-verdict on acme/widgets@${older}: want one completed run of App 101 concluding success, read no run after 0s`
    );
  });

  test("a run of another App, another conclusion, an unfinished run, two runs or no answer fail, naming what was read", () => {
    for (const [made, read, down] of [
      [runs({ app: 15368, conclusion: "success" }), "no run", false],
      [runs({ app: 101, conclusion: "failure" }), "completed failure", false],
      [
        runs({ app: 101, status: "in_progress", conclusion: null }),
        "in_progress with no conclusion",
        false,
      ],
      [runs({ app: 101 }, { app: 101 }), "completed success, completed success", false],
      [runs({ app: 101, sha: older }), "completed success", false],
      [runs(), "no answer from GitHub", true],
    ] as const) {
      const { code, stderr } = run(`assert_legion_verdict acme/widgets ${head} success`, {
        runs: made,
        down,
      });
      expect(code, read).toBe(1);
      expect(stderr).toContain(`concluding success, read ${read} after 0s`);
    }
  });

  test("waits within its bound for a run that lands after the first read", () => {
    const { code, stdout, asked } = run(`assert_legion_verdict acme/widgets ${head} failure`, {
      runs: runs(),
      runsLater: runs({ app: 101, conclusion: "failure" }),
      bound: "30",
    });
    expect(code).toBe(0);
    expect(stdout).toContain("completed failure");
    expect(asked.length).toBe(2);
  });
});
