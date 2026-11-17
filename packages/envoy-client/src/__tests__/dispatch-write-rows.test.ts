// Every keyed write of the client (`#write`, `#writeAnswer`, `#upload`: each POST and PATCH) reads
// its 2xx into the row its callers read (`dispatch-write-rows.ts`), and an answer that gives no row
// is a landing whose answer was lost (`ANSWER_LOST`). A row check stricter than Dispatch's true
// answer would turn a good answer into that landing, so each method is driven here against the
// answers its route's operation declares in the OpenAPI document the server generates
// (`dispatch-route-answers.ts` reads it): two instances of each 2xx schema, the least one the
// schema admits (every optional member absent, every nullable member null, every list empty) and
// the most one (every member present, none null, every list holding one item), each an answer the
// route declares. Each must come back as the call's answer, and a body that is no row (an HTML
// page, an empty body, `{}`, `null`) must be `ANSWER_LOST`. The answers the other tests' stubs
// serve for a keyed write come from `routeAnswer`, which holds each to its route's schema.
import { describe, expect, test } from "bun:test";
import type { Actor } from "@legion/contracts";
import { bearerCredential } from "../bearer";
import { DISPATCH_ANSWER_LOST, DispatchClient, DispatchWriteLandedError } from "../dispatch-http";
import { WRITE_ROWS } from "../dispatch-write-rows";
import { answerErrors, answersOf, instance, onRoute, routeAnswer } from "./dispatch-route-answers";

const actor = { kind: "session", id: "session-rows" } as Actor;
const content = { name: "notes.md", content: "# Notes\n", actor };

interface WriteRowCase {
  /** The `DispatchClient` method, the key of its row in `WRITE_ROWS`. */
  readonly method: keyof typeof WRITE_ROWS;
  /** The operation of the OpenAPI document the method sends. */
  readonly route: string;
  readonly call: (client: DispatchClient) => Promise<unknown>;
  /** Branches of a status's answer this method's request is never answered with, and why. */
  readonly never?: { readonly status: number; readonly branch: number; readonly why: string };
  /** An operation the client sends first, answered with no route, so the call falls back. */
  readonly noRouteFirst?: string;
  /** What tells this case from another of the same method and route. */
  readonly variant?: string;
}

/** The methods no caller reads the answer of, whose row is any object: `{}` is their row. */
const READS_NOTHING: Partial<Record<WriteRowCase["method"], true>> = {
  deliveryReply: true,
  nameArtifactVersion: true,
};

const CASES: readonly WriteRowCase[] = [
  {
    method: "issue",
    route: "POST /api/v1/issues",
    call: (client) => client.issue({ project: "ACME", title: "A title", actor }),
  },
  {
    method: "markdownRoundTrip",
    route: "POST /api/v1/markdown/round-trip",
    call: (client) => client.markdownRoundTrip("# A heading\n"),
  },
  {
    method: "related",
    route: "POST /api/v1/projects/{key}/related",
    call: (client) => client.related("ACME", { title: "A title" }),
  },
  {
    method: "updateIssue",
    route: "PATCH /api/v1/issues/{key}",
    call: (client) => client.updateIssue("ACME-1", { status: "todo", actor }),
  },
  {
    method: "claimIssue",
    route: "POST /api/v1/issues/{key}/claim",
    call: (client) => client.claimIssue("ACME-1", { actor }),
  },
  {
    method: "ask",
    route: "POST /api/v1/issues/{key}/asks",
    call: (client) => client.ask("ACME-1", { question: "Which?", actor }),
  },
  {
    method: "createProject",
    route: "POST /api/v1/projects",
    call: (client) => client.createProject({ key: "ACME", name: "Acme" }),
  },
  {
    method: "syncArchitectureSources",
    route: "POST /api/v1/projects/{key}/architecture-sources/sync",
    call: (client) => client.syncArchitectureSources("ACME"),
  },
  {
    method: "syncArchitectureSources",
    route: "POST /api/v1/projects/{key}/architecture-source/sync",
    noRouteFirst: "POST /api/v1/projects/{key}/architecture-sources/sync",
    call: (client) => client.syncArchitectureSources("ACME"),
  },
  {
    method: "syncArchitectureSource",
    route: "POST /api/v1/projects/{key}/architecture-sources/{owner}/{repo}/sync",
    call: (client) => client.syncArchitectureSource("ACME", "acme/arch"),
  },
  {
    method: "resolveAsk",
    route: "POST /api/v1/asks/{id}/resolve",
    call: (client) => client.resolveAsk("ask-1", { kind: "resolved", reason: "done", actor }),
  },
  {
    method: "requestApproval",
    route: "POST /api/v1/artifacts/{id}/approval-requests",
    call: (client) => client.requestApproval("doc-1", { actor, summary: "Approve it" }),
  },
  {
    method: "planApprove",
    route: "POST /api/v1/artifacts/{id}/plan-reviews",
    call: (client) => client.planApprove("doc-1", { actor, version: 2 }),
  },
  {
    method: "reviewArtifact",
    route: "POST /api/v1/artifacts/{id}/versions/{number}/reviews",
    call: (client) => client.reviewArtifact("doc-1", 2, { state: "approved" }),
  },
  {
    method: "recordResult",
    route: "POST /api/v1/issues/{key}/results",
    call: (client) =>
      client.recordResult("ACME-1", {
        actor,
        check: "probe",
        role: "tester",
        verdict: "pass",
        commit: "c8".repeat(20),
      } as Parameters<DispatchClient["recordResult"]>[1]),
  },
  {
    method: "retractResult",
    route: "POST /api/v1/results/{id}/retract",
    call: (client) => client.retractResult("result-1", { actor, reason: "wrong commit" }),
  },
  {
    method: "recordSpecCheckResult",
    route: "POST /api/v1/projects/{key}/results",
    call: (client) =>
      client.recordSpecCheckResult("ACME", {
        commit: "c8".repeat(20),
        check: "probe",
        verdict: "pass",
      }),
  },
  {
    method: "editAsk",
    route: "PATCH /api/v1/asks/{id}",
    call: (client) => client.editAsk("ask-1", { question: "Which one?", actor }),
  },
  {
    method: "comment",
    route: "POST /api/v1/issues/{key}/comments",
    call: (client) => client.comment("ACME-1", { body: "A comment", actor }),
  },
  {
    method: "artifactAsk",
    route: "POST /api/v1/artifacts/{id}/asks",
    call: (client) => client.artifactAsk("doc-1", { question: "Which?", actor }),
  },
  {
    method: "suggest",
    route: "POST /api/v1/issues/{key}/comments",
    call: (client) =>
      client.suggest("ACME-1", {
        anchor: { artifact: "doc-1", quote: "old" },
        replace_with: "new",
        actor,
      }),
  },
  {
    method: "artifactComment",
    route: "POST /api/v1/artifacts/{id}/comments",
    call: (client) => client.artifactComment("doc-1", { body: "A comment", actor }),
  },
  {
    method: "artifactSuggest",
    route: "POST /api/v1/artifacts/{id}/comments",
    call: (client) =>
      client.artifactSuggest("doc-1", {
        anchor: { artifact: "doc-1", quote: "old" },
        replace_with: "new",
        actor,
      }),
  },
  {
    method: "message",
    route: "POST /api/v1/issues/{key}/messages",
    call: (client) => client.message("ACME-1", { body: "A message", actor }),
  },
  {
    method: "agentMessage",
    route: "POST /api/v1/agents/{session_id}/messages",
    call: (client) => client.agentMessage("session-2", { body: "Hello", delivery: "queue" }),
  },
  {
    method: "messageReply",
    route: "POST /api/v1/messages/{id}/reply",
    call: (client) =>
      client.messageReply("message-1", { body: "A reply", attempt: 1, actor }, { followUp: true }),
    never: {
      status: 200,
      branch: 1,
      why: "the delivery attempt is the answer to a reply that names a reason and no body (deliveryReply); messageReply always sends a body",
    },
  },
  {
    method: "deliveryReply",
    route: "POST /api/v1/messages/{id}/reply",
    call: (client) =>
      client.deliveryReply("message", "message-1", { reason: "busy", attempt: 1, actor }),
  },
  {
    method: "deliveryReply",
    route: "POST /api/v1/comments/{id}/reply",
    call: (client) =>
      client.deliveryReply("comment", "comment-1", { reason: "busy", attempt: 1, actor }),
  },
  {
    method: "acceptMessageDelivery",
    route: "POST /api/v1/messages/{id}/deliveries/{attempt}/accept",
    call: (client) => client.acceptMessageDelivery("message-1", 1, { actor }),
  },
  {
    method: "docEdit",
    route: "POST /api/v1/artifacts/{id}/edits",
    call: (client) =>
      client.docEdit("doc-1", { ops: [{ op: "insert", markdown: "More.", after: "end" }], actor }),
  },
  {
    method: "nameArtifactVersion",
    route: "POST /api/v1/artifacts/{id}/versions",
    call: (client) => client.nameArtifactVersion("doc-1", { summary: "Named", actor }),
  },
  {
    method: "resolveComment",
    route: "POST /api/v1/comments/{id}/resolve",
    call: (client) => client.resolveComment("comment-1", actor),
  },
  {
    method: "writeHandoff",
    route: "POST /api/v1/issues/{key}/handoffs/{phase}",
    call: (client) => client.writeHandoff("ACME-1", "implement", { actor, body: "{}" }),
  },
  {
    method: "projectArtifact",
    route: "POST /api/v1/projects/{key}/artifacts",
    call: (client) => client.projectArtifact("ACME", content),
  },
  {
    method: "projectArtifact",
    route: "POST /api/v1/projects/{key}/artifacts",
    variant: "a file, as multipart",
    call: (client) =>
      client.projectArtifact("ACME", {
        name: "chart.png",
        file: new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }),
        actor,
      }),
  },
  {
    method: "agentArtifact",
    route: "POST /api/v1/agents/{session_id}/artifacts",
    call: (client) => client.agentArtifact("session-rows", content),
  },
  {
    method: "artifact",
    route: "POST /api/v1/issues/{key}/artifacts",
    call: (client) => client.artifact("ACME-1", content),
  },
];

/** A Dispatch that answers the case's route with `status` and `body` under `type` (and its
 *  fallback's first route with no route for it), recording each request it was sent. */
function dispatchAnswering(row: WriteRowCase, status: number, type: string, body: string) {
  const sent: string[] = [];
  const fetchImpl = (async (input: Request | string | URL, init?: RequestInit) => {
    const request = new Request(input, init);
    const path = new URL(request.url).pathname;
    sent.push(`${request.method} ${path}`);
    const [, firstPath] = (row.noRouteFirst ?? " ").split(" ");
    if (row.noRouteFirst !== undefined && onRoute(firstPath as string, path)) {
      return Response.json(
        { code: "NOT_FOUND", error: `no route for ${request.method} ${path}` },
        { status: 404 }
      );
    }
    return new Response(body, { status, headers: { "Content-Type": type } });
  }) as typeof fetch;
  const client = new DispatchClient("http://dispatch.test", bearerCredential("secret"), fetchImpl, {
    sleep: async () => undefined,
  });
  return { sent, client };
}

/** What the call made of the answer: the answer, or the landing's code, or the error. */
async function outcome(row: WriteRowCase, status: number, type: string, body: string) {
  const { sent, client } = dispatchAnswering(row, status, type, body);
  try {
    return { sent, answered: await row.call(client) };
  } catch (error) {
    if (error instanceof DispatchWriteLandedError) return { sent, landed: error.code };
    return {
      sent,
      thrown: error instanceof Error ? `${error.name}: ${error.message}` : String(error),
    };
  }
}

describe("every keyed write reads Dispatch's declared answer as its row, and anything else as ANSWER_LOST", () => {
  test("the table drives every row of WRITE_ROWS", () => {
    const driven = [...new Set<string>(CASES.map((row) => row.method))].sort();
    expect(driven).toEqual(Object.keys(WRITE_ROWS).sort());
  });

  for (const row of CASES) {
    const label = `${row.method} (${row.route}${row.variant === undefined ? "" : `, ${row.variant}`})`;
    const [verb, template] = row.route.split(" ") as [string, string];
    const answers = answersOf(row.route).filter(
      (answer) => !(row.never?.status === answer.status && row.never.branch === answer.branch)
    );
    for (const answer of answers) {
      for (const extent of ["least", "most"] as const) {
        const which = `${extent} ${answer.status} answer${answer.branch === 0 ? "" : ` (branch ${answer.branch})`}`;
        test(`${label}: the ${which} is the call's answer`, async () => {
          const answered = instance(answer.schema, extent);
          expect(answerErrors(row.route, answer.status, answered)).toEqual([]);
          const body = JSON.stringify(answered);
          const got = await outcome(row, answer.status, "application/json", body);
          expect({ landed: got.landed, thrown: got.thrown }).toEqual({
            landed: undefined,
            thrown: undefined,
          });
          const last = got.sent.at(-1)?.split(" ") ?? [];
          expect(last[0] === verb && onRoute(template, last[1] ?? "")).toBe(true);
        });
      }
    }
    const status = answers[0]?.status ?? 201;
    for (const [name, type, body] of [
      ["an HTML page", "text/html", "<html><body>not json</body></html>"],
      ["an empty body", "application/json", ""],
      ["JSON null", "application/json", "null"],
      ...(READS_NOTHING[row.method] ? [] : [["the JSON {}", "application/json", "{}"]]),
    ] as const) {
      test(`${label}: a ${status} with ${name} is ANSWER_LOST`, async () => {
        expect((await outcome(row, status, type, body)).landed).toBe(DISPATCH_ANSWER_LOST);
      });
    }
  }
});

describe("a stub's answer to a keyed write is one its route declares (routeAnswer)", () => {
  const comments = "POST /api/v1/issues/{key}/comments";

  test("the stub's members are laid over the route's least answer, which its row reads", () => {
    const answer = routeAnswer(comments, { id: "comment-1", body: "Shipped." });
    expect(answer).toMatchObject({ id: "comment-1", body: "Shipped.", issue_key: null });
    expect(answerErrors(comments, 201, answer)).toEqual([]);
    expect(WRITE_ROWS.comment.safeParse(answer).success).toBe(true);
    const upload = routeAnswer("POST /api/v1/issues/{key}/artifacts", {
      artifact: { id: "artifact-1", slug: "shot-png" },
    });
    expect(upload).toMatchObject({
      artifact: { id: "artifact-1", slug: "shot-png", name: "x" },
      version: { number: 1 },
    });
  });

  test("a member in a form its route does not answer is refused, naming it", () => {
    expect(() =>
      routeAnswer("PATCH /api/v1/issues/{key}", { key: "DSP-1", components: [] })
    ).toThrow("/components must be object");
    expect(() => routeAnswer(comments, { id: 1 })).toThrow("/id must be string");
  });

  test("a route that declares two 2xx statuses needs the one served", () => {
    expect(() => routeAnswer("POST /api/v1/issues", { key: "DSP-1" })).toThrow(
      "declares the 2xx statuses 200, 201"
    );
    expect(routeAnswer("POST /api/v1/issues", { key: "DSP-1" }, 201)).toMatchObject({
      key: "DSP-1",
    });
  });
});
