import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { bearerCredential } from "../bearer";
import { runDispatchCli } from "../dispatch-cli";
import { resetAdviceMemory } from "../dispatch-execute";
import { DispatchClient, forgetKeyedLayerMemory } from "../dispatch-http";

// A write whose path names a text Dispatch's reviewer holds, which has not posted (an issue, an
// ask, a comment or a message: `heldTargets` in packages/envoy/internal/dispatch/api/
// held_replay.go), is kept beside that text and runs right after it posts. Dispatch answers it
// 202 with `HeldDeferred`, `{id, held}` (`holdRequest`), and not with the route's own shape. The
// commands read that body as their own payload: `dispatch result` threw reading
// `recorded.result.verdict`, and `dispatch artifact` and `dispatch issue-update` printed fields
// the body does not have. Each row drives the `dispatch` command against a stub Dispatch that
// answers so, as `dispatch-review.test.ts` does; the last rows read the client alone.

const makeDirectory = temporaryDirectories("kept-write");

/** The Dispatch bearer each command is handed. */
const TOKEN = fixtureSecret("dsp");

/** The id Dispatch keeps the write under, and the held issue it runs after. */
const KEPT_ID = "6d1f0b2e-8c4a-4e57-9a31-0f2b7c5d9e14";
const HELD_ISSUE = "DSP-1";
const POSTING = { state: "posting", deadline: null, waits_on: HELD_ISSUE };
const KEPT = { id: KEPT_ID, held: POSTING };

/** What a command prints for its write Dispatch kept. */
const KEPT_LINE =
  `Kept until held text ${HELD_ISSUE} posts: Dispatch keeps this write as ${KEPT_ID} and runs ` +
  `it right after that text posts. \`dispatch held ${KEPT_ID}\` reads it.`;

const ISSUE = {
  key: HELD_ISSUE,
  title: "Native workspace",
  status: "todo",
  labels: [],
  priority: null,
  assignee: null,
  route: null,
  parent: null,
  blocked_by: [],
  components: [],
  repo: null,
  resolved_repo: null,
  closed_at: null,
  external_links: [],
  advances: [],
  advances_revision: 0,
  artifacts: [],
  open_asks: [],
};

const ASK_ID = "d236c23c-240b-4854-b6fe-cbae3b6868a7";

interface Recorded {
  readonly method: string;
  readonly path: string;
}

const requests: Recorded[] = [];

const kept = () => Response.json(KEPT, { status: 202 });

const server = Bun.serve({
  port: 0,
  async fetch(request) {
    const { pathname } = new URL(request.url);
    const type = request.headers.get("Content-Type") ?? "";
    const text = await request.text();
    const body: Record<string, unknown> =
      text === "" || !type.startsWith("application/json") ? {} : JSON.parse(text);
    requests.push({ method: request.method, path: pathname });
    const route = `${request.method} ${pathname}`;
    switch (route) {
      case "GET /api/v1/issues/DSP-1":
        return Response.json(ISSUE);
      case "GET /api/v1/issues/DSP-1/artifacts":
        return Response.json([]);
      case "POST /api/v1/markdown/round-trip":
        return Response.json({ markdown: body.markdown });
      case "POST /api/v1/issues/DSP-1/results":
      case "POST /api/v1/issues/DSP-1/artifacts":
      case "PATCH /api/v1/issues/DSP-1":
      case "DELETE /api/v1/issues/DSP-1/claim":
        return kept();
      // A message and an ask on a held issue are held beside it under ids of their own, answered
      // 201 with the route's whole shape and `held` (`reviewWrite`), never kept as above.
      case "POST /api/v1/issues/DSP-1/messages":
        return Response.json(
          { id: "message-7", issue_key: HELD_ISSUE, body: body.body, held: POSTING },
          { status: 201 }
        );
      case "POST /api/v1/issues/DSP-1/asks":
        return Response.json(
          {
            id: ASK_ID,
            issue_key: HELD_ISSUE,
            author: { kind: "session", id: "ses-1" },
            question: body.question,
            options: [],
            multiple: false,
            urgency: "med",
            anchor: null,
            state: "open",
            answer: null,
            created_at: "2026-11-20T09:00:00Z",
            held: POSTING,
          },
          { status: 201 }
        );
    }
    return Response.json({ error: `unexpected ${route}` }, { status: 500 });
  },
});

afterAll(() => server.stop(true));

let stateDir = "";
beforeEach(() => {
  requests.length = 0;
  stateDir = makeDirectory();
  resetAdviceMemory();
  forgetKeyedLayerMemory();
});

async function run(argv: readonly string[]): Promise<{
  code: number;
  stdout: string;
  stderr: string;
}> {
  let stdout = "";
  let stderr = "";
  const code = await runDispatchCli(
    argv,
    {
      HOME: stateDir,
      DISPATCH_URL: `http://127.0.0.1:${server.port}`,
      DISPATCH_TOKEN: TOKEN,
      DISPATCH_STATE_DIR: stateDir,
      DISPATCH_HOST: "omp",
      DISPATCH_SESSION_ID: "ses-1",
    },
    {
      stdout: (text) => {
        stdout += text;
      },
      stderr: (text) => {
        stderr += text;
      },
      readText: (path) => readFileSync(path, "utf-8"),
      cwd: stateDir,
    }
  );
  return { code, stdout, stderr };
}

/** The writes this run sent, each as `<METHOD> <path>`. */
function writes(): string[] {
  return requests
    .filter(({ method }) => method !== "GET")
    .map(({ method, path }) => `${method} ${path}`);
}

describe("a write Dispatch keeps beside a held text", () => {
  test("dispatch result prints the kept line and exits 0", async () => {
    const { code, stdout, stderr } = await run([
      "result",
      "--issue",
      HELD_ISSUE,
      "--check",
      "k1",
      "--role",
      "implementer",
      "--verdict",
      "pass",
      "--commit",
      "1".repeat(40),
    ]);

    expect(stderr).toBe("");
    expect(stdout).toBe(`${KEPT_LINE}\n`);
    expect(code).toBe(0);
    expect(writes()).toEqual(["POST /api/v1/issues/DSP-1/results"]);
  });

  test("dispatch artifact's upload to an issue prints the kept line and exits 0", async () => {
    const { code, stdout, stderr } = await run([
      "artifact",
      "--issue",
      HELD_ISSUE,
      "--name",
      "design.md",
      "--content",
      "# Design\n",
    ]);

    expect(stderr).toBe("");
    expect(stdout).toBe(`${KEPT_LINE}\n`);
    expect(code).toBe(0);
    expect(writes()).toEqual([
      "POST /api/v1/markdown/round-trip",
      "POST /api/v1/issues/DSP-1/artifacts",
    ]);
  });

  test("dispatch issue-update prints the kept line and exits 0", async () => {
    const { code, stdout, stderr } = await run([
      "issue-update",
      "--issue",
      HELD_ISSUE,
      "--status",
      "in_progress",
    ]);

    expect(stderr).toBe("");
    expect(stdout).toBe(`${KEPT_LINE}\n`);
    expect(code).toBe(0);
    expect(writes()).toEqual(["PATCH /api/v1/issues/DSP-1"]);
  });

  // The closing reason posts first, and on a held issue it is held beside it; the close is then
  // kept. Both are told, the reason first, in the order they were written.
  test("dispatch issue-update names its held closing reason before the kept close, and exits 0", async () => {
    const { code, stdout, stderr } = await run([
      "issue-update",
      "--issue",
      HELD_ISSUE,
      "--status",
      "done",
      "--reason",
      "Shipped in the native workspace release.",
    ]);

    expect(stderr).toBe("");
    expect(stdout).toBe(
      [
        "Held until held text DSP-1 posts: message message-7 " +
          "(dispatch://DSP-1/message/message-7), the closing reason of DSP-1. No person sees it " +
          "until it posts. It posts after held text DSP-1. `dispatch held message-7` reads it.",
        KEPT_LINE,
        "",
      ].join("\n")
    );
    expect(code).toBe(0);
    expect(writes()).toEqual(["POST /api/v1/issues/DSP-1/messages", "PATCH /api/v1/issues/DSP-1"]);
  });

  // A DELETE goes through the client's path for a PUT or a DELETE, not the write loop of a POST
  // or a PATCH: Dispatch keeps it all the same.
  test("dispatch claim --release prints the kept line and exits 0", async () => {
    const { code, stdout, stderr } = await run(["claim", "--issue", HELD_ISSUE, "--release"]);

    expect(stderr).toBe("");
    expect(stdout).toBe(`${KEPT_LINE}\n`);
    expect(code).toBe(0);
    expect(writes()).toEqual(["DELETE /api/v1/issues/DSP-1/claim"]);
  });

  // A picture is uploaded before the call's own write, which needs the picture's address: kept,
  // the picture has none yet, so the call's own write is not sent, and the call says so.
  test("a picture upload Dispatch keeps fails the call before its own write, saying it is kept", async () => {
    writeFileSync(
      join(stateDir, "shot.png"),
      new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
    );

    const { code, stdout } = await run([
      "message",
      "--issue",
      HELD_ISSUE,
      "--body",
      "Look.",
      "--image",
      "shot.png",
    ]);

    expect(stdout).toBe(
      `images: shot.png was not uploaded yet: POST http://127.0.0.1:${server.port}/api/v1/issues/DSP-1/artifacts ` +
        `was kept as ${KEPT_ID}, and Dispatch runs it right after held text DSP-1 posts. Nothing was posted.\n`
    );
    expect(code).toBe(1);
    expect(writes()).toEqual(["POST /api/v1/issues/DSP-1/artifacts"]);
  });

  // The reviewer's own hold of an ask is another answer: 201 with the ask's whole shape and
  // `held`, though `held` names the same text it waits on. The command prints its held line.
  test("a 201 held ask still prints its own held line", async () => {
    const { code, stdout, stderr } = await run([
      "ask",
      "--issue",
      HELD_ISSUE,
      "--question",
      "Ship the audit?",
    ]);

    expect(stderr).toBe("");
    expect(stdout).toStartWith(
      `Held until held text DSP-1 posts: ask ${ASK_ID} on DSP-1 (urgency med). No person sees it ` +
        `until it posts. It posts after held text DSP-1. \`dispatch held ${ASK_ID}\` reads it. ` +
        "The question: Ship the audit?\n"
    );
    expect(stdout).not.toContain("Kept");
    expect(code).toBe(0);
    expect(writes()).toEqual(["POST /api/v1/issues/DSP-1/asks"]);
  });
});

describe("the client's reading of a kept write", () => {
  /** A client whose every request Dispatch answers with `status` and `body`. */
  function answering(status: number, body: unknown): DispatchClient {
    const fetchImpl = (async () => Response.json(body, { status })) as unknown as typeof fetch;
    return new DispatchClient("http://dispatch.test", bearerCredential(TOKEN), fetchImpl);
  }

  const result = {
    actor: { kind: "session", id: "ses-1" },
    check: "k1",
    role: "implementer",
    verdict: "pass",
    commit: "1".repeat(40),
  } as const;

  test("throws the kept write's method, URL, id and held state, and no payload", async () => {
    const error = await answering(202, KEPT)
      .recordResult(HELD_ISSUE, result)
      .catch((thrown: unknown) => thrown);

    expect(error).toMatchObject({
      name: "DispatchWriteKept",
      method: "POST",
      url: "http://dispatch.test/api/v1/issues/DSP-1/results",
      id: KEPT_ID,
      held: POSTING,
    });
  });

  // Only that exact answer is a kept write: 202 with the two keys alone and a `waits_on`.
  test("reads any other answer as the route's payload", async () => {
    for (const [status, body] of [
      [201, KEPT],
      [202, { ...KEPT, line: "- Check k1" }],
      [202, { id: KEPT_ID, held: { state: "posting", deadline: null } }],
      [202, { id: KEPT_ID, held: { ...POSTING, waits_on: "" } }],
    ] as const) {
      expect<unknown>(await answering(status, body).recordResult(HELD_ISSUE, result)).toEqual(body);
    }
    // A read writes nothing, so nothing of it is kept.
    expect<unknown>(await answering(202, KEPT).getIssue(HELD_ISSUE)).toEqual(KEPT);
  });
});
