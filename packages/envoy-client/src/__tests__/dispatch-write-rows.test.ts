// Every keyed write of the client (`#write`, `#writeAnswer`, `#upload`: each POST and PATCH) reads
// its 2xx into the row its callers read (`dispatch-write-rows.ts`) by one rule, which turns on
// whether the 2xx names the call's `Idempotency-Key` back, as Dispatch's keyed layer does on every
// 2xx it gives a keyed write. A 2xx that names it back is Dispatch's: its row is the answer, and a
// body that gives no row is a landing whose answer was lost (`ANSWER_LOST`). A 2xx that names no
// key back and gives the row is the answer of a Dispatch with no keyed layer. Any other 2xx is not
// Dispatch's answer to the call, so the write's outcome is unknown. A route that stores nothing
// made nothing either way. A row check stricter than Dispatch's true answer would turn a good
// answer into a landing, so each method is driven here against the answers its route's operation
// declares in the OpenAPI document the server generates (`test/route-answers.ts` reads it):
// two instances of each 2xx schema, the least one the schema admits (every optional member
// absent, every nullable member null, every list empty) and the most one (every member present,
// none null, every list holding one item), each an answer the route declares. The answers the
// other tests' stubs serve for a keyed write come from `routeAnswer`, which holds each to its
// route's schema.
import { describe, expect, test } from "bun:test";
import {
  type Actor,
  IDEMPOTENCY_ANSWER_NOT_KEPT,
  IDEMPOTENCY_KEY_COMMITTED,
  IDEMPOTENCY_KEY_HEADER,
} from "@legion/contracts";
import { z } from "zod";
import {
  answerErrors,
  answersOf,
  declaredAt,
  instance,
  onRoute,
  requestMembers,
  routeAnswer,
} from "../../test/route-answers";
import { bearerCredential } from "../bearer";
import {
  DISPATCH_ANSWER_LOST,
  DispatchAnswerMissingError,
  DispatchClient,
  type DispatchLandedCode,
  DispatchWriteLandedError,
  writeOutcomeUnknown,
} from "../dispatch-http";
import { type Held, heldBy, rowStores, type SentWrite, WRITE_ROWS } from "../dispatch-write-rows";

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
  /** The row this route's answer is read into, where it is not the method's own (a fallback
   *  route's, whose one source the method answers as its list). */
  readonly answered?: keyof typeof WRITE_ROWS;
}

/** The row the case's answer is read into: the held values of the stand-in, of each "another"
 *  row and of the declaration check are this row's. */
function rowOf(row: WriteRowCase): (typeof WRITE_ROWS)[keyof typeof WRITE_ROWS] {
  return WRITE_ROWS[row.answered ?? row.method];
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
    method: "updateIssueComponents",
    route: "PATCH /api/v1/issues/{key}",
    variant: "writing components",
    call: (client) =>
      client.updateIssue("ACME-1", { components: { mode: "explicit", ids: ["web"] }, actor }),
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
    answered: "syncArchitectureSource",
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

/** Which key a stand-in's 2xx names back: the request's own, as Dispatch's keyed layer does,
 *  none, as a Dispatch with no keyed layer or something in Dispatch's place does, or another. */
type Echo = "this" | "none" | "other";

/** What a request of the client sent, as a row's held values read it (`heldBy`): its path's
 *  segments from `api` on, and its JSON body. */
async function sentBy(request: Request): Promise<SentWrite> {
  const json = request.headers.get("Content-Type")?.startsWith("application/json") === true;
  return {
    path: new URL(request.url).pathname.split("/").slice(1).map(decodeURIComponent),
    body: json ? await request.json() : undefined,
  };
}

/** A Dispatch that answers the case's route with `status` and `body` under `type` (or the body
 *  `body` makes of what the request sent), naming back the key `echo` says (and its fallback's
 *  first route with no route for it), with the headers `extra` adds, recording each request it
 *  was sent. */
function dispatchAnswering(
  row: WriteRowCase,
  status: number,
  type: string,
  body: string | ((sent: SentWrite) => string),
  echo: Echo,
  extra: Readonly<Record<string, string>> = {}
) {
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
    const headers: Record<string, string> = { "Content-Type": type, ...extra };
    const key = request.headers.get(IDEMPOTENCY_KEY_HEADER) ?? "";
    if (echo === "this") headers[IDEMPOTENCY_KEY_HEADER] = key;
    if (echo === "other") headers[IDEMPOTENCY_KEY_HEADER] = `${key}-another`;
    const text = typeof body === "string" ? body : body(await sentBy(request));
    return new Response(text, { status, headers });
  }) as typeof fetch;
  const client = new DispatchClient("http://dispatch.test", bearerCredential("secret"), fetchImpl, {
    sleep: async () => undefined,
  });
  return { sent, client };
}

/** What the call made of the answer: the answer, the landing's code, an outcome it cannot know,
 *  the answer missing from a call that stores nothing, or the error. */
async function outcome(
  row: WriteRowCase,
  status: number,
  type: string,
  body: string | ((sent: SentWrite) => string),
  echo: Echo,
  extra: Readonly<Record<string, string>> = {}
) {
  const { sent, client } = dispatchAnswering(row, status, type, body, echo, extra);
  try {
    return { sent, answered: await row.call(client) };
  } catch (error) {
    if (error instanceof DispatchWriteLandedError) return { sent, landed: error.code };
    if (error instanceof DispatchAnswerMissingError) return { sent, missing: error.code };
    if (writeOutcomeUnknown(error)) return { sent, unknown: true };
    return {
      sent,
      thrown: error instanceof Error ? `${error.name}: ${error.message}` : String(error),
    };
  }
}

/** What a 2xx that is not the call's answer is: for a route that stores, a landing whose answer
 *  was lost where the 2xx is Dispatch's own (it names the key back), and an outcome the call
 *  cannot know where it is not; for a route that stores nothing, a missing answer either way. */
function noAnswer(
  row: WriteRowCase,
  echo: Echo
): { landed?: DispatchLandedCode; unknown?: boolean; missing?: string } {
  if (!rowStores(WRITE_ROWS[row.method])) return { missing: DISPATCH_ANSWER_LOST };
  return echo === "this" ? { landed: DISPATCH_ANSWER_LOST } : { unknown: true };
}

/** The value a call sent that `held` names, or undefined where the call did not set it. */
function sentValue(held: Held, sent: SentWrite): unknown {
  const value =
    "segment" in held
      ? sent.path[held.segment]
      : (sent.body as Record<string, unknown> | undefined)?.[held.member];
  return value === null || value === "" ? undefined : value;
}

/**
 * `answer` with each value the call sent that the case's row holds (`heldBy`) laid at the member
 * that holds it, as Dispatch answers the call's own write; with `other` (one of those values),
 * that one value changed, as another call's answer holds it. A member `answer` holds no object
 * for on the way (absent, null, an empty list) is taken from `most`, the route's most answer, so
 * the answer stays one the route declares.
 */
function holdingSent(
  row: WriteRowCase,
  answer: unknown,
  most: unknown,
  sent: SentWrite,
  other?: Held
): unknown {
  const out = structuredClone(answer);
  for (const held of heldBy(rowOf(row))) {
    const value = sentValue(held, sent);
    if (value === undefined) continue;
    let node = out as Record<string | number, unknown>;
    let model = most as Record<string | number, unknown>;
    const leaf = held.at.at(-1) as string | number;
    for (const step of held.at.slice(0, -1)) {
      if (typeof node[step] !== "object" || node[step] === null || node[step] === undefined) {
        node[step] = structuredClone(model[step]);
      }
      if (Array.isArray(node[step]) && (node[step] as unknown[]).length === 0) {
        node[step] = structuredClone(model[step]);
      }
      node = node[step] as Record<string | number, unknown>;
      model = model[step] as Record<string | number, unknown>;
    }
    const numeric = typeof model[leaf] === "number";
    const changed = held === other;
    node[leaf] = numeric
      ? Number(value) + (changed ? 1 : 0)
      : `${String(value)}${changed ? "-another" : ""}`;
  }
  return out;
}

/** Each path inside `schema` at which an object refuses a member it does not name (a
 *  `z.strictObject`, whose catchall is `never`), against the rule that a row keeps each one. A
 *  kind it does not open is refused, naming it: it could hold such an object. */
function strictPaths(schema: z.core.$ZodType, at: string): string[] {
  // `$ZodTypes` is zod's union of its own types, which a switch on the definition's type narrows.
  const def = (schema as z.core.$ZodTypes)._zod.def;
  switch (def.type) {
    case "object":
      return [
        ...(def.catchall?._zod.def.type === "never" ? [at] : []),
        ...Object.entries(def.shape).flatMap(([name, member]) =>
          strictPaths(member, `${at}.${name}`)
        ),
      ];
    case "array":
      return strictPaths(def.element, `${at}[]`);
    case "union":
      return def.options.flatMap((option) => strictPaths(option, at));
    case "intersection":
      return [...strictPaths(def.left, at), ...strictPaths(def.right, at)];
    case "record":
      return strictPaths(def.valueType, `${at}{}`);
    case "pipe":
      return [...strictPaths(def.in, at), ...strictPaths(def.out, at)];
    case "optional":
    case "nullable":
    case "readonly":
    case "nonoptional":
    case "default":
    case "prefault":
    case "catch":
      return strictPaths(def.innerType, at);
    // A value that holds no member holds no closed object.
    case "string":
    case "number":
    case "boolean":
    case "bigint":
    case "null":
    case "undefined":
    case "unknown":
    case "any":
    case "enum":
    case "literal":
    case "date":
      return [];
    default:
      throw new Error(`strictPaths opens no ${def.type} schema (${at})`);
  }
}

describe("every keyed write reads Dispatch's declared answer as its row, by the key it names back", () => {
  test("the table drives every row of WRITE_ROWS", () => {
    const driven = [...new Set<string>(CASES.map((row) => row.method))].sort();
    expect(driven).toEqual(Object.keys(WRITE_ROWS).sort());
  });

  test("the routes that store nothing are the round trip and the related-item check", () => {
    const stores = Object.entries(WRITE_ROWS).filter(([, row]) => !rowStores(row));
    expect(stores.map(([method]) => method).sort()).toEqual(["markdownRoundTrip", "related"]);
  });

  // A row's held values are what a no-key answer is held to (`rowHolds`): a value the route's
  // request does not take, or a member its answers do not hold, would turn every such answer
  // into an unknown outcome, or hold it to nothing.
  test("each value a row holds is a path parameter or a body member its route's request takes, and a key, id, word or number every answer it is driven with holds", () => {
    const wrong = CASES.flatMap((row) => {
      const segments = (row.route.split(" ")[1] as string).split("/").slice(1);
      const answers = answersOf(row.route).filter(
        (answer) => !(row.never?.status === answer.status && row.never.branch === answer.branch)
      );
      return heldBy(rowOf(row)).flatMap((held) => {
        const taken =
          "segment" in held
            ? /^\{[a-z_]+\}$/.test(segments[held.segment] ?? "")
            : requestMembers(row.route).includes(held.member);
        const holds = answers.every((answer) => {
          const leaf = declaredAt(answer.schema, held.at);
          const value = leaf === undefined ? undefined : instance(leaf, "most");
          return typeof value === "string" || typeof value === "number";
        });
        const named = "segment" in held ? `segment ${held.segment}` : held.member;
        return taken && holds
          ? []
          : [
              `${row.method} (${row.route}) ${named} at ${held.at.join(".")}: taken ${taken}, held ${holds}`,
            ];
      });
    });
    expect(wrong).toEqual([]);
  });

  test("the rows that hold nothing the call sent are the six the table names", () => {
    const none = Object.entries(WRITE_ROWS).filter(([, row]) => heldBy(row).length === 0);
    expect(none.map(([method]) => method).sort()).toEqual([
      "agentMessage",
      "deliveryReply",
      "docEdit",
      "markdownRoundTrip",
      "nameArtifactVersion",
      "related",
    ]);
  });

  // A newer Dispatch may answer a member the row does not name, at any depth, and an older client
  // must still read the answer as its own: a row that refused it would print a landing.
  test("no row refuses a member it does not name, at any depth", () => {
    const strict = Object.entries(WRITE_ROWS).flatMap(([method, row]) => strictPaths(row, method));
    expect(strict).toEqual([]);
  });

  // The walk above opens every kind a row is built of, and reads a value that holds no member as
  // holding no closed object. A kind it does not open (a tuple, a lazy schema) could hold one, so
  // it is refused by name rather than read as clean.
  test("the walk refuses a kind it does not open, naming it", () => {
    for (const [kind, schema] of [
      ["tuple", z.tuple([z.strictObject({ id: z.string() })])],
      ["lazy", z.lazy(() => z.strictObject({ id: z.string() }))],
    ] as const) {
      expect(() => strictPaths(schema, "row"), kind).toThrow(`opens no ${kind} schema (row)`);
    }
    expect(strictPaths(z.looseObject({ id: z.string(), n: z.number().int() }), "row")).toEqual([]);
  });

  test("an issue update whose advance and link carry a member the row does not name is its answer", async () => {
    const [patched] = answersOf("PATCH /api/v1/issues/{key}");
    if (patched === undefined) throw new Error("PATCH /api/v1/issues/{key} declares no 2xx");
    const answered = {
      ...(instance(patched.schema, "most") as Record<string, unknown>),
      advances: [{ id: "ship-the-daemon", inherited: false, title: "Ship the daemon" }],
      external_links: [{ url: "https://example.com/pr/1", kind: "pr", title: "The pull request" }],
    };
    const updateIssue = CASES.find((row) => row.method === "updateIssue");
    if (updateIssue === undefined) throw new Error("the table drives no updateIssue");
    const got = await outcome(
      updateIssue,
      patched.status,
      "application/json",
      JSON.stringify(answered),
      "this"
    );
    expect({ answered: "answered" in got, landed: got.landed, thrown: got.thrown }).toEqual({
      answered: true,
      landed: undefined,
      thrown: undefined,
    });
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
          const got = await outcome(row, answer.status, "application/json", body, "this");
          expect({
            answered: "answered" in got,
            landed: got.landed,
            unknown: got.unknown,
            missing: got.missing,
            thrown: got.thrown,
          }).toEqual({
            answered: true,
            landed: undefined,
            unknown: undefined,
            missing: undefined,
            thrown: undefined,
          });
          const last = got.sent.at(-1)?.split(" ") ?? [];
          expect(last[0] === verb && onRoute(template, last[1] ?? "")).toBe(true);
        });
      }
    }
    const first = answers[0];
    if (first === undefined) throw new Error(`${row.route} declares no 2xx answer`);
    const status = first.status;
    const least = instance(first.schema, "least");
    const most = instance(first.schema, "most");
    /** The least answer holding what the call sent, or another call's (`holdingSent`), and the
     *  ways each one served is not an answer the route declares, read after the call. */
    const holding = (other?: Held) => {
      const refused: string[][] = [];
      const body = (sent: SentWrite): string => {
        const answered = holdingSent(row, least, most, sent, other);
        refused.push(answerErrors(row.route, status, answered));
        return JSON.stringify(answered);
      };
      return { body, refused };
    };
    // Every Dispatch 2xx to a keyed write names its key back (`TestEveryKeyedAnswerNamesItsKey`,
    // packages/envoy/internal/dispatch/api/request_keys_test.go); a Dispatch that runs no keyed
    // layer names none, and its answer is still the call's where it holds what the call sent
    // that the row's answer names back.
    test(`${label}: the least ${status} answer naming no key back is the call's answer`, async () => {
      const served = holding();
      const got = await outcome(row, status, "application/json", served.body, "none");
      expect({
        landed: got.landed,
        unknown: got.unknown,
        missing: got.missing,
        thrown: got.thrown,
        served: served.refused,
      }).toEqual({
        landed: undefined,
        unknown: undefined,
        missing: undefined,
        thrown: undefined,
        served: [[]],
      });
    });
    // One that holds another value than the call sent is another call's answer (a proxy or a
    // cache that answers one write with another's), which says nothing of this write.
    for (const held of heldBy(rowOf(row))) {
      test(`${label}: the least ${status} answer naming no key back, of another ${held.at.join(".")}, is not this call's`, async () => {
        const served = holding(held);
        const got = await outcome(row, status, "application/json", served.body, "none");
        expect({
          landed: got.landed,
          unknown: got.unknown,
          missing: got.missing,
          served: served.refused,
        }).toEqual({ landed: undefined, unknown: true, missing: undefined, served: [[]] });
      });
    }
    test(`${label}: the least ${status} answer naming another key back is not this call's`, async () => {
      const got = await outcome(row, status, "application/json", JSON.stringify(least), "other");
      expect({ landed: got.landed, unknown: got.unknown, missing: got.missing }).toEqual({
        landed: undefined,
        unknown: undefined,
        missing: undefined,
        ...noAnswer(row, "other"),
      });
    });
    // Dispatch keeps an answer up to 1 MiB; a repeat under the key of a larger one is a replayed
    // 2xx reading ANSWER_NOT_KEPT (`keyedAnswerKept`, request_keys.go). A route that stores
    // nothing made nothing, so it is the missing answer there, and a landing only where the route
    // stores.
    const notKept: { landed?: DispatchLandedCode; missing?: string } = rowStores(
      WRITE_ROWS[row.method]
    )
      ? { landed: IDEMPOTENCY_ANSWER_NOT_KEPT }
      : { missing: DISPATCH_ANSWER_LOST };
    test(`${label}: a replayed ${status} reading ANSWER_NOT_KEPT, naming its key back, is ${"landed" in notKept ? "ANSWER_NOT_KEPT" : "no answer"}`, async () => {
      const body = JSON.stringify({ code: IDEMPOTENCY_ANSWER_NOT_KEPT, error: "not kept" });
      const got = await outcome(row, status, "application/json", body, "this", {
        "Idempotency-Replayed": "?1",
      });
      expect({ landed: got.landed, unknown: got.unknown, missing: got.missing }).toEqual({
        landed: undefined,
        unknown: undefined,
        missing: undefined,
        ...notKept,
      });
    });
    // A 409 KEY_COMMITTED that names the key back says Dispatch committed the write under it and
    // kept no answer: a landing where the route stores, and by the same rule as the 2xx path, the
    // missing answer where it stores nothing.
    const committed: { landed?: DispatchLandedCode; missing?: string } = rowStores(
      WRITE_ROWS[row.method]
    )
      ? { landed: IDEMPOTENCY_KEY_COMMITTED }
      : { missing: DISPATCH_ANSWER_LOST };
    test(`${label}: a 409 KEY_COMMITTED naming its key back is ${"landed" in committed ? "KEY_COMMITTED" : "no answer"}`, async () => {
      const body = JSON.stringify({ code: IDEMPOTENCY_KEY_COMMITTED, error: "committed" });
      const got = await outcome(row, 409, "application/json", body, "this");
      expect({ landed: got.landed, unknown: got.unknown, missing: got.missing }).toEqual({
        landed: undefined,
        unknown: undefined,
        missing: undefined,
        ...committed,
      });
    });
    for (const echo of ["this", "none"] as const) {
      const named = echo === "this" ? "naming its key back" : "naming no key back";
      for (const [name, type, body] of [
        ["an HTML page", "text/html", "<html><body>not json</body></html>"],
        ["an empty body", "application/json", ""],
        ["JSON null", "application/json", "null"],
        ...(READS_NOTHING[row.method] ? [] : [["the JSON {}", "application/json", "{}"]]),
      ] as const) {
        const expected = noAnswer(row, echo);
        const reads =
          "landed" in expected ? "ANSWER_LOST" : "missing" in expected ? "no answer" : "unknown";
        test(`${label}: a ${status} with ${name}, ${named}, is ${reads}`, async () => {
          const got = await outcome(row, status, type, body, echo);
          expect({ landed: got.landed, unknown: got.unknown, missing: got.missing }).toEqual({
            landed: undefined,
            unknown: undefined,
            missing: undefined,
            ...expected,
          });
        });
      }
    }
  }
});

describe("a stub's answer names only members its route declares (routeAnswer)", () => {
  test("a member the route does not declare is refused, naming it", () => {
    expect(() =>
      routeAnswer("PATCH /api/v1/issues/{key}", { key: "DSP-1", artifacts: [] })
    ).toThrow("names artifacts, which the route does not declare");
    expect(() =>
      routeAnswer("POST /api/v1/issues/{key}/artifacts", { artifact: { id: "a", sha: "x" } })
    ).toThrow("names artifact.sha, which the route does not declare");
  });

  // A reply route answers the reply it made or the delivery it settled (`oneOf`), so a member of
  // either branch is the route's, and an answer is one branch: a member no branch declares, or
  // members of two branches together, is no answer the route gives.
  test("a member a union's second branch declares is the route's, and a member no branch declares is refused", () => {
    const reply = "POST /api/v1/messages/{id}/reply";
    const delivery = routeAnswer(reply, { message_id: "message-1", state: "accepted" }, 200);
    expect(delivery).toMatchObject({ message_id: "message-1", state: "accepted" });
    expect(answerErrors(reply, 200, delivery)).toEqual([]);
    expect(() => routeAnswer(reply, { message_id: "message-1", sent_to: "x" }, 200)).toThrow(
      "names sent_to, which the route does not declare"
    );
    expect(() => routeAnswer(reply, { message_id: "message-1", body: "Shipped." }, 200)).toThrow(
      "which the route does not declare"
    );
  });
});

describe("the answers the table drives each write against (instance)", () => {
  // The table drives each branch of an answer's union apart (`answersOf` splits the top), and
  // `instance` writes one answer, so a union of two branches below the top would leave one branch
  // untried by every row: it is refused, naming it, and a member that is a value or null is not.
  test("a union of two branches below an answer's top is refused, and a nullable member is not", () => {
    const answer = (target: Record<string, unknown>) => ({
      type: "object",
      required: ["target"],
      properties: { target },
    });
    expect(() =>
      instance(answer({ oneOf: [{ type: "string" }, { type: "integer" }] }), "least")
    ).toThrow("a union of 2 branches below an answer's top");
    expect(instance(answer({ anyOf: [{ type: "string" }, { type: "null" }] }), "most")).toEqual({
      target: "x",
    });
    expect(instance(answer({ anyOf: [{ type: "string" }, { type: "null" }] }), "least")).toEqual({
      target: null,
    });
  });
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
