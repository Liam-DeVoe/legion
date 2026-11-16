import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// What a CI job's `dispatch result --issue` prints goes into its runner's log, which reads a line
// that starts with `::`, and a `##[` anywhere, as a workflow command. Each line it prints stays one
// line, by runnerlog.Printable's rule (runner-log.ts), when the check id or Dispatch's refusal holds
// a line break or `::error`. The lines of a job's log that `dispatch results --issue` prints follow
// the same rule: each line of the log stays a line of its own, and none is a workflow command. The
// real entry runs as a child process against stubs of the runner, the broker and Dispatch.

const SHA = "0123456789abcdef0123456789abcdef01234567";
const entry = join(import.meta.dir, "..", "..", "bin", "dispatch.ts");
let answer: () => Response = () => Response.json({}, { status: 500 });

const runner = Bun.serve({
  port: 0,
  hostname: "127.0.0.1",
  fetch: () => Response.json({ count: 1, value: "job.oidc.token" }),
});
const broker = Bun.serve({
  port: 0,
  hostname: "127.0.0.1",
  // The broker's answer of a trade, built as dispatch-cli.test.ts builds it.
  fetch: () =>
    Response.json(
      Object.fromEntries([
        ["access_token", "broker-results-token"],
        ["issued_token_type", "urn:ietf:params:oauth:token-type:access_token"],
        ["token_type", "DPoP"],
        ["expires_in", 600],
      ])
    ),
});
const dispatch = Bun.serve({ port: 0, hostname: "127.0.0.1", fetch: () => answer() });
afterAll(() => {
  for (const stub of [runner, broker, dispatch]) stub.stop(true);
});

/** The lines the entry prints, run with `env` beside Dispatch's stub and a fresh home. */
async function run(
  argv: readonly string[],
  env: Readonly<Record<string, string>>
): Promise<string[]> {
  const home = mkdtempSync(join(tmpdir(), "ci-job-one-line-"));
  const child = Bun.spawn([process.execPath, entry, ...argv], {
    cwd: home,
    env: {
      PATH: process.env.PATH ?? "",
      HOME: home,
      DISPATCH_STATE_DIR: home,
      DISPATCH_URL: `http://127.0.0.1:${dispatch.port}`,
      ...env,
    },
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return `${stdout}${stderr}`.replace(/\n$/, "").split("\n");
}

/** A CI job's own identity: the runner's token endpoint and the broker. */
const asJob = (): Record<string, string> => ({
  AGENT_SECRETS_URL: `http://127.0.0.1:${broker.port}`,
  AGENT_SECRETS_MACHINE_IDENTITY: "github-actions",
  AGENT_SECRETS_MACHINE_AUDIENCE: "example-audience",
  ACTIONS_ID_TOKEN_REQUEST_URL: `http://127.0.0.1:${runner.port}/_apis/idtoken?api-version=2.0`,
  ACTIONS_ID_TOKEN_REQUEST_TOKEN: "runner-request-bearer",
});

const record = (check: string) => [
  "result",
  "--issue",
  "ACME-1",
  "--check",
  check,
  "--verdict",
  "pass",
  "--commit",
  SHA,
];

/** No printed line is a workflow command. */
function noWorkflowCommand(printed: readonly string[]): void {
  expect(
    printed.filter((line) => line.trimStart().startsWith("::") || line.includes("##["))
  ).toEqual([]);
}

describe("dispatch result --issue as a CI job: each printed line stays one line", () => {
  test("a check id that holds a line break and ::error, which Dispatch's refusal names", async () => {
    const check = "c1\n::error title=x::injected";
    answer = () =>
      Response.json(
        {
          code: "UNKNOWN_CHECK",
          error: `${check} is no check of this issue's Verification document or of one above it`,
        },
        { status: 400 }
      );
    const printed = await run(record(check), asJob());
    noWorkflowCommand(printed);
    expect(printed).toHaveLength(1);
  });

  test("a refusal of Dispatch whose text holds a line break", async () => {
    answer = () =>
      Response.json(
        { code: "CHECK_NOT_THIS_JOBS", error: "check c1 names CI job x\n::warning::y" },
        { status: 409 }
      );
    const printed = await run(record("c1"), asJob());
    noWorkflowCommand(printed);
    expect(printed).toHaveLength(1);
  });

  test("the control: a refusal with no line break prints one line", async () => {
    answer = () =>
      Response.json(
        { code: "CHECK_NOT_THIS_JOBS", error: "check c1 names CI job x" },
        { status: 409 }
      );
    const printed = await run(record("c1"), asJob());
    expect(printed).toEqual(["CHECK_NOT_THIS_JOBS: check c1 names CI job x"]);
  });
});

describe("dispatch results --issue: each line of a job's log stays a line, and none is a workflow command", () => {
  /** The read of ACME-1, whose check c1 CI proves holds one result its job recorded. */
  const read = (observed: string) => ({
    issue: "ACME-1",
    verification_artifact_id: "artifact-1",
    checks: [
      {
        block_id: "c1",
        ref: "dispatch://ACME-1/artifact/verification-md#b-c1",
        issue_key: "ACME-1",
        artifact_id: "artifact-1",
        text: "The server builds at the head.",
        when: "before_merge",
        surface: "",
        run: "",
        negative_control: "A build whose server does not build.",
        ci_check: "go-checks",
        ci: {
          run: "go-checks",
          state: "no_result",
          case: "no_head",
          detail: "Dispatch holds no open or merged pull request head of ACME-1.",
          line: "- Check c1 (before_merge): CI run go-checks has no result (no_head)",
        },
        version: 1,
        sha256: "digest",
        results: [],
        recorded_results: [
          {
            id: "result-1",
            issue_key: "ACME-1",
            check_issue_key: "ACME-1",
            check_artifact_id: "artifact-1",
            check_block_id: "c1",
            check_version: 1,
            check_sha256: "digest",
            check_words_moved: false,
            role: "ci",
            verdict: "fail",
            commit_sha: SHA,
            commit_stands: { answer: "other_commit" },
            branch: "legion/ACME-1",
            surface: "",
            run: "go-checks",
            observed,
            negative_control: "A build whose server does not build.",
            failed: 0,
            skipped: 0,
            excluded: 0,
            screenshots: [],
            session: "ci-results:example",
            actor: { kind: "session", id: "ci-results:example" },
            created_at: "2026-11-16T10:00:00Z",
          },
        ],
        compared_with: [],
        pass_stands: { answer: "no" },
      },
    ],
  });
  const asSession = {
    DISPATCH_HOST: "omp",
    DISPATCH_SESSION_ID: "one-line-reader",
    DISPATCH_TOKEN: "session-token",
  };

  test("a job's log holding ::error and ##[ prints neither at the start of a line", async () => {
    answer = () =>
      Response.json(read("--- FAIL: TestX\n::error file=a.go,line=1::boom\n##[group]x"));
    const printed = await run(["results", "--issue", "ACME-1"], asSession);
    noWorkflowCommand(printed);
    expect(printed).toContain("\\x3a:error file=a.go,line=1::boom");
  });

  test("the control: a job's log of two plain lines prints both, the second on a line of its own", async () => {
    answer = () => Response.json(read("--- FAIL: TestX\nsecond line"));
    const printed = await run(["results", "--issue", "ACME-1"], asSession);
    expect(printed).toContain("    Reading: --- FAIL: TestX");
    expect(printed).toContain("second line");
  });
});
