import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import type { IssueSteps, StepRecord } from "@legion/contracts";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import {
  type FakeDispatchOptions,
  type FakeDispatchServer,
  fakeDispatchFetch,
  startFakeDispatch,
} from "../../test/fake-dispatch";
import { bearerCredential } from "../bearer";
import { runDispatchCli } from "../dispatch-cli";
import { executeDispatchTool } from "../dispatch-execute";
import { forgetKeyedLayerMemory } from "../dispatch-http";

// `dispatch steps --issue <KEY>`: the steps that ran on an issue, as Dispatch's
// `GET /api/v1/issues/{key}/steps` answers them, one header line and one line per run. The
// format cases drive `executeDispatchTool` over the shared fake Dispatch (`test/fake-dispatch.ts`)
// as a fetch, so each reads the text and the details a host is handed; the command-line cases
// drive the real CLI against that fake on a loopback port, and read its exit code and output.

/** The implementer's run of LEG-1's step 2: ended, with its model and its report. */
const implementer: StepRecord = {
  id: "3f0c8a52-6d1e-5b7a-9c2d-4e8f1a6b7c90",
  issue: "LEG-1",
  task: "default",
  step: 2,
  kind: "step",
  role: "implementer",
  type: "implementation",
  agent: "legion-leg-leg-1-implementer",
  session: "ses-implementer",
  model: "anthropic/claude-opus-5",
  generation: 1,
  attempt: 1,
  started_at: "2026-11-16T09:00:00Z",
  ended_at: "2026-11-16T09:40:00Z",
  result: "passed",
  returned_to: null,
  report: "9b2e4d6f-8a1c-4e3b-a5d7-c9f1e3b5d7a9",
};

/** The tester's run of step 3, still running: no end, no result, and no model answered yet. */
const tester: StepRecord = {
  id: "5a1d9c3e-7b2f-5d8a-8e4c-6f0a2b4c6d81",
  issue: "LEG-1",
  task: "default",
  step: 3,
  kind: "step",
  role: "tester",
  type: "testing",
  agent: "legion-leg-leg-1-tester",
  session: "ses-tester",
  model: "",
  generation: 1,
  attempt: 1,
  started_at: "2026-11-16T10:00:00Z",
  ended_at: null,
  result: "",
  returned_to: null,
  report: null,
};

/** A merge GitHub already made: no merger started, so no agent, no model and no start. */
const mergedByGitHub: StepRecord = {
  id: "7c3e1a5b-9d4f-5a2c-b6e8-0a2c4e6f8a03",
  issue: "LEG-1",
  task: "default",
  step: 0,
  kind: "merge",
  role: "merger",
  type: "",
  agent: "",
  session: "",
  model: "",
  generation: 1,
  attempt: 1,
  started_at: null,
  ended_at: "2026-11-16T12:00:00Z",
  result: "passed",
  returned_to: null,
  report: null,
};

/** A second run of a review of another task, sent back to step 1 of that task. */
const docsReview: StepRecord = {
  id: "1e5a3c7d-2b6f-5e9a-8c1d-3f5a7b9c1d25",
  issue: "LEG-1",
  task: "docs",
  step: 2,
  kind: "step",
  role: "reviewer",
  type: "review",
  agent: "legion-leg-leg-1-reviewer",
  session: "ses-reviewer",
  model: "openai/gpt-6.1-sol",
  generation: 1,
  attempt: 2,
  started_at: "2026-11-16T11:00:00Z",
  ended_at: "2026-11-16T11:20:00Z",
  result: "failed",
  returned_to: 1,
  report: null,
};

/** A merger's run that failed and sent its task back to place 0, the retro, with the report its
 *  completion named: a destination of 0 is a place, not the absence of one. */
const mergeSentBack: StepRecord = {
  id: "9d4b2f6a-3c8e-5f1b-a7d9-2e4c6a8b0d47",
  issue: "LEG-1",
  task: "default",
  step: 0,
  kind: "merge",
  role: "merger",
  type: "",
  agent: "legion-leg-leg-1-merger",
  session: "ses-merger",
  model: "anthropic/claude-opus-5",
  generation: 1,
  attempt: 1,
  started_at: "2026-11-16T12:00:00Z",
  ended_at: "2026-11-16T12:10:00Z",
  result: "failed",
  returned_to: 0,
  report: "2c4e6a8b-0d1f-4a3c-9e5b-7d9f1b3d5f79",
};

const config = {
  enabled: true,
  url: "http://dispatch.test",
  credential: bearerCredential("secret"),
  error: null,
};

/** The one route the command reads. */
const STEPS_PATH = "/api/v1/issues/LEG-1/steps";

/** `dispatch steps --issue LEG-1` over a fake Dispatch whose steps route answers `answer`: the
 *  result, and each request the fake saw as `<method> <pathname>`. */
async function readSteps(answer: unknown) {
  const dispatch = fakeDispatchFetch({ routes: { [STEPS_PATH]: answer } });
  const result = await executeDispatchTool({
    tool: "dispatch_steps",
    args: { issue: "LEG-1" },
    cwd: "/workspace",
    host: "omp",
    sessionId: "session-1",
    config,
    env: {},
    exec: async (file, args) => {
      throw new Error(`unexpected command: ${file} ${args.join(" ")}`);
    },
    fetchImpl: dispatch.fetchImpl,
  });
  return {
    result,
    requests: dispatch.calls.map((call) => `${call.method} ${call.url.pathname}`),
  };
}

describe("dispatch steps", () => {
  test("prints a header, then one line per run, a running step running", async () => {
    const answer: IssueSteps = {
      issue: "LEG-1",
      tasks: [{ task: "default", steps: [implementer, tester] }],
    };

    const { result, requests } = await readSteps(answer);

    expect(requests).toEqual([`GET ${STEPS_PATH}`]);
    expect(result.text).toBe(
      [
        "LEG-1: 2 steps in 1 tasks",
        "default 2 step implementer implementation attempt 1 legion-leg-leg-1-implementer anthropic/claude-opus-5 2026-11-16T09:00:00Z 2026-11-16T09:40:00Z passed",
        "default 3 step tester testing attempt 1 legion-leg-leg-1-tester - 2026-11-16T10:00:00Z running running",
      ].join("\n")
    );
    expect(result.details).toEqual({ issue: "LEG-1", tasks: answer.tasks });
  });

  test("a field with no value prints -, the start of a run with none among them", async () => {
    const answer: IssueSteps = {
      issue: "LEG-1",
      tasks: [{ task: "default", steps: [implementer, mergedByGitHub] }],
    };

    const { result } = await readSteps(answer);

    expect(result.text.split("\n")[2]).toBe(
      "default 0 merge merger - attempt 1 - - - 2026-11-16T12:00:00Z passed"
    );
  });

  test("each task's runs print together, in the order the read lists the tasks", async () => {
    const answer: IssueSteps = {
      issue: "LEG-1",
      tasks: [
        { task: "default", steps: [implementer, tester] },
        { task: "docs", steps: [docsReview] },
      ],
    };

    const { result } = await readSteps(answer);

    expect(result.text).toBe(
      [
        "LEG-1: 3 steps in 2 tasks",
        "default 2 step implementer implementation attempt 1 legion-leg-leg-1-implementer anthropic/claude-opus-5 2026-11-16T09:00:00Z 2026-11-16T09:40:00Z passed",
        "default 3 step tester testing attempt 1 legion-leg-leg-1-tester - 2026-11-16T10:00:00Z running running",
        "docs 2 step reviewer review attempt 2 legion-leg-leg-1-reviewer openai/gpt-6.1-sol 2026-11-16T11:00:00Z 2026-11-16T11:20:00Z failed",
      ].join("\n")
    );
  });

  // The text leaves out where a run sent its task back and the report it named; the details hold
  // every field of every record as Dispatch answered it.
  test("the details hold each record whole, a destination of 0 and a report, and neither", async () => {
    const answer: IssueSteps = {
      issue: "LEG-1",
      tasks: [
        { task: "default", steps: [tester, mergeSentBack] },
        { task: "docs", steps: [docsReview] },
      ],
    };

    const { result } = await readSteps(answer);

    expect(result.details).toEqual({ issue: "LEG-1", tasks: answer.tasks });
  });

  // Dispatch answers an issue it does not hold, and one no step ran on, with no task; the
  // generated type also lets the list be null, which reads as the empty list.
  test("an issue with no step prints 0 steps in 0 tasks", async () => {
    for (const answer of [
      { issue: "LEG-1", tasks: [] },
      { issue: "LEG-1", tasks: null },
    ]) {
      const { result } = await readSteps(answer);

      expect(result.text, JSON.stringify(answer)).toBe("LEG-1: 0 steps in 0 tasks");
      expect(result.details, JSON.stringify(answer)).toEqual({ issue: "LEG-1", tasks: [] });
    }
  });
});

describe("dispatch steps on the command line", () => {
  const makeDirectory = temporaryDirectories("dispatch-steps");
  // Each row starts the fake Dispatch it reads, and this hook stops it. A server started in this
  // body would start under a filter that selects no row here, and no hook would stop it.
  const servers: FakeDispatchServer<unknown>[] = [];
  afterEach(() => {
    for (const server of servers.splice(0)) server.stop();
  });

  let stateDir = "";
  beforeEach(() => {
    stateDir = makeDirectory();
    forgetKeyedLayerMemory();
  });

  /** A fake Dispatch on a loopback port of its own, answering as `options` say, which the hook
   *  above stops when the row ends. */
  function dispatchServing(options: FakeDispatchOptions): FakeDispatchServer<unknown> {
    const server = startFakeDispatch(options);
    servers.push(server);
    return server;
  }

  async function run(
    server: FakeDispatchServer<unknown>,
    argv: readonly string[]
  ): Promise<{ code: number; stdout: string }> {
    let stdout = "";
    const code = await runDispatchCli(
      argv,
      {
        HOME: stateDir,
        DISPATCH_HOST: "omp",
        DISPATCH_SESSION_ID: "ses-1",
        DISPATCH_URL: server.url,
        DISPATCH_TOKEN: "test-token",
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

  test("--issue and a bare key read the same steps and print them, exit 0", async () => {
    const server = dispatchServing({
      routes: {
        [STEPS_PATH]: {
          issue: "LEG-1",
          tasks: [{ task: "default", steps: [implementer, tester] }],
        },
      },
    });
    for (const argv of [
      ["steps", "--issue", "LEG-1"],
      ["steps", "LEG-1"],
    ]) {
      const before = server.reads.length;

      const { code, stdout } = await run(server, argv);

      expect(code, argv.join(" ")).toBe(0);
      // The run read the steps route once and asked Dispatch nothing else.
      expect(server.reads.slice(before).map((url) => url.pathname)).toEqual([STEPS_PATH]);
      expect(stdout).toContain(
        [
          "LEG-1: 2 steps in 1 tasks",
          "default 2 step implementer implementation attempt 1 legion-leg-leg-1-implementer anthropic/claude-opus-5 2026-11-16T09:00:00Z 2026-11-16T09:40:00Z passed",
          "default 3 step tester testing attempt 1 legion-leg-leg-1-tester - 2026-11-16T10:00:00Z running running",
        ].join("\n")
      );
    }
  });

  // Only Dispatch's 200 reads as an issue with no step. Its own refusal of the read, and a page
  // something in its place answered, are the read's failure: each exits 1 with the refusal.
  test("an issue with no step exits 0 and prints 0 steps in 0 tasks", async () => {
    const server = dispatchServing({ routes: { [STEPS_PATH]: { issue: "LEG-1", tasks: [] } } });

    const { code, stdout } = await run(server, ["steps", "--issue", "LEG-1"]);

    expect(code).toBe(0);
    expect(stdout).toContain("LEG-1: 0 steps in 0 tasks");
  });

  test("a refusal of the read exits 1 with the refusal, and prints no steps", async () => {
    const refusals: readonly {
      readonly status: string;
      readonly answer: () => Response;
      readonly printed: (url: string) => string;
    }[] = [
      {
        status: "403",
        answer: () =>
          Response.json(
            { code: "PROJECT_SCOPE", error: "project is outside this credential's scope" },
            { status: 403 }
          ),
        printed: () => "PROJECT_SCOPE: project is outside this credential's scope",
      },
      {
        status: "404",
        answer: () =>
          Response.json({ code: "ISSUE_NOT_FOUND", error: "no issue LEG-1" }, { status: 404 }),
        printed: () => "ISSUE_NOT_FOUND: no issue LEG-1",
      },
      {
        status: "500",
        answer: () =>
          Response.json({ code: "INTERNAL", error: "internal server error" }, { status: 500 }),
        printed: () => "INTERNAL: internal server error",
      },
      {
        status: "502 page",
        answer: () =>
          new Response("<html><body><h1>502 Bad Gateway</h1></body></html>", {
            status: 502,
            statusText: "Bad Gateway",
            headers: { "Content-Type": "text/html" },
          }),
        printed: (url) =>
          `HTTP_502: GET ${url}${STEPS_PATH} answered 502 Bad Gateway with a body that is not ` +
          `Dispatch's error JSON ("502 Bad Gateway"), which looks like a proxy or gateway page ` +
          "rather than Dispatch's own answer, so a retry may succeed.",
      },
    ];
    for (const { status, answer, printed } of refusals) {
      const server = dispatchServing({ handler: answer });

      const { code, stdout } = await run(server, ["steps", "--issue", "LEG-1"]);

      expect(code, status).toBe(1);
      // The one request was the steps read, and the refusal is its answer.
      expect(
        server.reads.map((url) => url.pathname),
        status
      ).toEqual([STEPS_PATH]);
      expect(stdout, status).toContain(printed(server.url));
      expect(stdout, status).not.toContain("steps in");
    }
  });
});
