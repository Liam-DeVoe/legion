import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { runDispatchCli } from "../dispatch-cli";
import { resetAdviceMemory } from "../dispatch-execute";
import { forgetKeyedLayerMemory } from "../dispatch-http";

// `dispatch issue-update --clear-parent` takes an issue out from under its parent: the command
// derives the flag from the nullable `parent` field of the spec, the executor sends the JSON null
// the server reads as "no parent", and the report says the parent was cleared. One fake Dispatch
// records what the real CLI sent, as `dispatch-asked-of.test.ts` does for `--clear-assignee`.

const makeDirectory = temporaryDirectories("dispatch-clear-parent");

interface Recorded {
  readonly method: string;
  readonly path: string;
  readonly body: unknown;
}

const requests: Recorded[] = [];

/** DSP-1 as `GET /api/v1/issues/DSP-1` reads it: open, under DSP-9. */
const ISSUE = {
  key: "DSP-1",
  title: "Native workspace",
  status: "in_progress",
  labels: [],
  priority: null,
  assignee: null,
  route: null,
  parent: "DSP-9",
  blocked_by: [],
  components: [],
  repo: null,
  resolved_repo: null,
  closed_at: null,
  external_links: [],
  artifacts: [],
};

const server = Bun.serve({
  port: 0,
  async fetch(request) {
    const { pathname } = new URL(request.url);
    const text = await request.text();
    const body: unknown = text === "" ? undefined : JSON.parse(text);
    requests.push({ method: request.method, path: pathname, body });
    if (pathname === "/api/v1/issues/DSP-1" && request.method === "GET") {
      return Response.json(ISSUE);
    }
    if (pathname === "/api/v1/issues/DSP-1" && request.method === "PATCH") {
      // The issue as the server stores it: the parent the body names, the old one when it
      // names none.
      const sent = (body ?? {}) as { parent?: string | null };
      return Response.json({ ...ISSUE, parent: "parent" in sent ? sent.parent : ISSUE.parent });
    }
    return Response.json({ error: `unexpected ${request.method} ${pathname}` }, { status: 500 });
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
      DISPATCH_TOKEN: "test-token",
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
      readText: () => "",
      cwd: stateDir,
    }
  );
  return { code, stdout, stderr };
}

/** What the recorder saw, as `METHOD path`. */
function calls(): string[] {
  return requests.map((recorded) => `${recorded.method} ${recorded.path}`);
}

/** The body of the one PATCH this run sent, as the server received it. */
function patchBody(): Record<string, unknown> {
  const patch = requests.find((recorded) => recorded.method === "PATCH");
  if (patch === undefined) throw new Error("no PATCH was sent");
  return patch.body as Record<string, unknown>;
}

describe("dispatch issue-update --clear-parent", () => {
  test("sends parent null and reports the parent cleared", async () => {
    const { code, stdout, stderr } = await run([
      "issue-update",
      "--issue",
      "DSP-1",
      "--clear-parent",
    ]);

    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(calls()).toEqual(["GET /api/v1/issues/DSP-1", "PATCH /api/v1/issues/DSP-1"]);
    const body = patchBody();
    expect(Object.keys(body).sort()).toEqual(["actor", "parent"]);
    expect(body.parent).toBeNull();
    expect(stdout).toContain("DSP-1: parent cleared");
  });

  test("--parent still sends the key and reports it", async () => {
    const { code, stdout, stderr } = await run([
      "issue-update",
      "--issue",
      "DSP-1",
      "--parent",
      "DSP-2",
    ]);

    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(patchBody().parent).toBe("DSP-2");
    expect(stdout).toContain("DSP-1: parent -> DSP-2");
  });

  test("is refused beside --parent, before any request", async () => {
    const { code, stdout } = await run([
      "issue-update",
      "--issue",
      "DSP-1",
      "--parent",
      "DSP-2",
      "--clear-parent",
    ]);

    expect(code).toBe(2);
    expect(stdout).toStartWith(
      "dispatch issue-update was not called: 1 problem\n- --parent and --clear-parent both set parent"
    );
    expect(calls()).toEqual([]);
  });

  test("--help says how to clear the parent", async () => {
    const { code, stdout, stderr } = await run(["issue-update", "--help"]);

    expect(stderr).toBe("");
    expect(code).toBe(0);
    expect(stdout).toContain("set or clear its parent (--clear-parent clears it)");
    expect(stdout).toContain("--clear-parent");
    expect(stdout).toContain("Sends parent as null.");
    expect(calls()).toEqual([]);
  });
});
