import { afterAll, beforeEach, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { runDispatchCli } from "../dispatch-cli";
import { fieldFlag } from "../dispatch-command";
import { resetAdviceMemory } from "../dispatch-execute";

// `needs` is the one spelling of an issue's waits (dispatch://INFRA-1098 R27): Dispatch reads
// needs, refuses a request that names blocked_by and no needs by name, and serves a request that
// names both from needs alone. The CLI's issue commands therefore send needs alone, and no request
// they build carries a blocked_by member, while the flag a person types stays `--blocked-by`. This
// fake Dispatch records every request a run makes, so the test reads the bodies the CLI built
// rather than what a server made of them.

interface Recorded {
  readonly method: string;
  readonly path: string;
  readonly body: unknown;
}

const requests: Recorded[] = [];
const makeDirectory = temporaryDirectories("dispatch-needs-spelling");

/** DSP-1 as this fake answers it: its waits under needs, the one member Dispatch answers them in. */
function issueBody(needs: readonly string[]) {
  return {
    key: "DSP-1",
    project: "DSP",
    title: "Native workspace",
    status: "in_progress",
    priority: null,
    assignee: null,
    claim: null,
    labels: [],
    components: { mode: "inherit", ids: [], inherited_from: null, unknown: [] },
    route: null,
    parent: null,
    needs: [...needs],
    duplicate_of: null,
    filed_from: null,
    external_links: [],
    artifacts: [],
    open_asks: [],
    repo: null,
    resolved_repo: null,
    last_seq: 0,
    advances: [],
    advances_revision: 0,
  };
}

/** The needs the last create or PATCH named, which the fake answers the issue with. */
let answered: readonly string[] = [];

const server = Bun.serve({
  port: 0,
  async fetch(request) {
    const { pathname } = new URL(request.url);
    const text = await request.text();
    const body = text === "" ? undefined : JSON.parse(text);
    requests.push({ method: request.method, path: pathname, body });
    if (Array.isArray(body?.needs)) answered = body.needs;
    if (pathname === "/api/v1/issues" && request.method === "POST") {
      return Response.json(
        { ...issueBody(answered), key: "DSP-12", title: body.title },
        { status: 201 }
      );
    }
    if (
      pathname === "/api/v1/issues/DSP-1" &&
      (request.method === "PATCH" || request.method === "GET")
    ) {
      return Response.json(issueBody(answered));
    }
    return Response.json({ error: `unexpected ${request.method} ${pathname}` }, { status: 500 });
  },
});

afterAll(() => server.stop(true));

let stateDir = "";
beforeEach(() => {
  requests.length = 0;
  answered = [];
  stateDir = makeDirectory();
  resetAdviceMemory();
});

async function run(argv: readonly string[]): Promise<{ code: number; stdout: string }> {
  let stdout = "";
  const code = await runDispatchCli(
    argv,
    {
      HOME: stateDir,
      DISPATCH_HOST: "omp",
      DISPATCH_SESSION_ID: "ses-1",
      DISPATCH_URL: `http://127.0.0.1:${server.port}`,
      DISPATCH_TOKEN: fixtureSecret("dispatch"),
      DISPATCH_STATE_DIR: stateDir,
    },
    {
      stdout: (text) => {
        stdout += text;
      },
      stderr: () => {},
      readText: (path) => readFileSync(path, "utf-8"),
      cwd: stateDir,
    }
  );
  return { code, stdout };
}

/** The one request of `method` to `path` the run made. */
function sent(method: string, path: string): Record<string, unknown> {
  const matching = requests.filter((request) => request.method === method && request.path === path);
  expect(matching, `${method} ${path}`).toHaveLength(1);
  return (matching[0] as Recorded).body as Record<string, unknown>;
}

/** Whether `value` holds a member named blocked_by at any depth. */
function carriesBlockedBy(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(carriesBlockedBy);
  if (value === null || typeof value !== "object") return false;
  return Object.entries(value).some(
    ([member, inner]) => member === "blocked_by" || carriesBlockedBy(inner)
  );
}

/** Fails naming each request the run made that carries a blocked_by member, after checking the
 *  search finds one planted in a copy of the first request, so a search that cannot find is no
 *  pass. */
function expectNoRequestCarriesBlockedBy(): void {
  expect(requests.length).toBeGreaterThan(0);
  const planted = { ...(requests[0] as Recorded), body: { blocked_by: ["DSP-9"] } };
  expect(carriesBlockedBy(planted.body)).toBe(true);
  const carrying = requests
    .filter((request) => carriesBlockedBy(request.body))
    .map((request) => `${request.method} ${request.path} ${JSON.stringify(request.body)}`);
  expect(carrying).toEqual([]);
}

describe("the dispatch command's issue commands send needs alone", () => {
  test("the flag a person types stays --blocked-by on both issue commands", () => {
    for (const tool of ["dispatch_issue", "dispatch_issue_update"] as const) {
      expect(fieldFlag(tool, "blocked_by"), tool).toBe("--blocked-by");
    }
  });

  test("dispatch issue sends the waits as needs and no blocked_by", async () => {
    const { code } = await run([
      "issue",
      "--project",
      "DSP",
      "--title",
      "Waits on two",
      "--blocked-by",
      "DSP-2",
      "--blocked-by",
      "OTHER-3",
    ]);

    expect(code).toBe(0);
    const body = sent("POST", "/api/v1/issues");
    expect(body.needs).toEqual(["DSP-2", "OTHER-3"]);
    expect(body).not.toHaveProperty("blocked_by");
    expectNoRequestCarriesBlockedBy();
  });

  test("dispatch issue-update sends the replacement list as needs and no blocked_by", async () => {
    const { code, stdout } = await run([
      "issue-update",
      "--issue",
      "DSP-1",
      "--blocked-by",
      "DSP-2",
      "--blocked-by",
      "OTHER-3",
    ]);

    expect(code).toBe(0);
    const body = sent("PATCH", "/api/v1/issues/DSP-1");
    expect(body.needs).toEqual(["DSP-2", "OTHER-3"]);
    expect(body).not.toHaveProperty("blocked_by");
    expect(stdout).toContain("blocked by DSP-2, OTHER-3");
    expectNoRequestCarriesBlockedBy();
  });

  test("dispatch issue-update --clear-blocked-by clears the waits as an empty needs", async () => {
    const { code, stdout } = await run(["issue-update", "--issue", "DSP-1", "--clear-blocked-by"]);

    expect(code).toBe(0);
    const body = sent("PATCH", "/api/v1/issues/DSP-1");
    expect(body.needs).toEqual([]);
    expect(body).not.toHaveProperty("blocked_by");
    expect(stdout).toContain("blocked_by cleared");
    expectNoRequestCarriesBlockedBy();
  });

  test("a call that names no waits sends neither member", async () => {
    const { code } = await run(["issue-update", "--issue", "DSP-1", "--title", "Renamed"]);

    expect(code).toBe(0);
    const body = sent("PATCH", "/api/v1/issues/DSP-1");
    expect(body).not.toHaveProperty("needs");
    expect(body).not.toHaveProperty("blocked_by");
    expectNoRequestCarriesBlockedBy();
  });
});
