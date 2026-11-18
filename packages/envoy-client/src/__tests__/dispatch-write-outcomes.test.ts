// packages/envoy-client/src/__tests__/dispatch-write-outcomes.test.ts
//
// Every way a Dispatch write can end, against the rule the client states: once an attempt may have
// landed, only a 2xx or a refusal the keyed layer judged for the write's key (the key echoed back)
// settles the write; every other end is `DispatchWriteUnsettledError` carrying the key. Before
// anything may have landed, a write that ends is told the event's own error. A redirect is never
// followed. Every attempt of the write carries one key.
//
// The rule is written here on its own (`rule`), not read from the client's table, and the client
// is driven through its public surface (`DispatchClient.comment`) with a scripted Dispatch, so the
// same file runs against a client that has no table at all. Two passes:
// - every (state, event) pair: each state reached through each event that reaches it, then every
//   event kind, then an event whose ending says which state the write was in;
// - every sequence of events up to the client's limits (its attempts and its time, the
//   rolling-release schedule, one dead-token re-send), over one event of each kind of step, with
//   the concrete event of each kind rotated along the path, and every ending after each prefix.
// A run whose client asks for an attempt past its script is answered 201, and the pass goes on with
// every event in that place; a run whose client stops is checked against the rule.

import { beforeEach, expect, test } from "bun:test";
import { TradeRetryingError } from "@legion/broker-client/token-errors";
import {
  type CommentWriteResponse,
  IDEMPOTENCY_KEY_COMMITTED,
  IDEMPOTENCY_KEY_IN_FLIGHT,
  IDEMPOTENCY_KEY_REUSED,
} from "@legion/contracts";
import type { RequestCredential } from "../bearer";
import {
  DispatchClient,
  DispatchServiceError,
  DispatchWriteLandedError,
  type DispatchWriteRetry,
  DispatchWriteUnsettledError,
  forgetKeyedLayerMemory,
} from "../dispatch-http";

const BASE = "http://dispatch.test";
/** Where every redirect points: another origin. */
const ELSEWHERE = "http://elsewhere.test/api/v1/issues/DSP-1/comments";

const actor = { kind: "session" as const, id: "session-1" };
const comment = { body: "Shipped.", actor };
const posted = {
  comment: { id: "comment-1", body: "Shipped." },
} as unknown as CommentWriteResponse;

/** What a step of a write met, by how the rule reads it. */
type Role =
  | "applied"
  | "judged"
  | "landed"
  | "unjudged"
  | "redirect"
  | "may-land"
  | "no-land"
  | "dead-token"
  | "no-route"
  | "no-credential"
  | "deadline-in-flight"
  | "cancelled-in-flight"
  | "deadline"
  | "cancelled";

/** One run of the client: its script and what the scripted Dispatch saw. */
interface Run {
  readonly script: readonly Kind[];
  cursor: number;
  readonly consumed: Kind[];
  /** Whether the client asked for an attempt past its script. */
  wantedMore: boolean;
  /** How many times a redirect was followed. */
  followed: number;
  /** The Idempotency-Key of each attempt. */
  readonly keys: (string | undefined)[];
  clock: number;
  readonly controller: AbortController;
  /** A step the script could not place: the harness's own fault, never the client's. */
  misplaced?: string;
}

interface Kind {
  readonly name: string;
  readonly role: Role;
  /** Where it happens: an attempt's answer or failure, a credential's presentation, or a wait (a
   *  pause, the rolling-release wait, or the presentation that comes first). */
  readonly at: "attempt" | "credential" | "wait";
  /** The status of the refusal it is, for an answer. */
  readonly status?: number;
  readonly respond?: (key: string, init: RequestInit, run: Run) => Response | Promise<Response>;
}

function json(body: unknown, status: number, key?: string, headers: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": "application/json",
      ...(key === undefined ? {} : { "Idempotency-Key": key }),
      ...headers,
    },
  });
}

const refusal = (status: number, code: string, error: string, key?: string) =>
  json({ code, error }, status, key);

function page(status: number, statusText: string): Response {
  return new Response(`<html><body><h1>${status} ${statusText}</h1></body></html>`, {
    status,
    statusText,
    headers: { "Content-Type": "text/html" },
  });
}

/** An answer that never comes: the request fails with its signal's reason once that aborts. */
function hang(signal: AbortSignal | null | undefined): Promise<Response> {
  const { promise, reject } = Promise.withResolvers<Response>();
  if (signal?.aborted) reject(signal.reason);
  else signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
  return promise;
}

const timedOut = () => new DOMException("The operation timed out.", "TimeoutError");
const stopped = () => new DOMException("The operation was aborted.", "AbortError");

/** A 3xx to another origin. Asked not to follow (`redirect: "manual"`), fetch hands it on. Left to
 *  follow, it does what Bun does: a 301, 302 or 303 sends the request again as a GET, a 307 or 308
 *  sends the POST again, and either answers as the other origin does. */
function redirect(status: number, init: RequestInit, run: Run): Response {
  if (init.redirect === "manual") {
    return new Response("moved", { status, headers: { Location: ELSEWHERE } });
  }
  run.followed += 1;
  return json(posted, status === 307 || status === 308 ? 201 : 200);
}

const KINDS: readonly Kind[] = [
  { name: "201", role: "applied", at: "attempt", respond: (key) => json(posted, 201, key) },
  {
    name: "422 KEY_REUSED",
    role: "judged",
    at: "attempt",
    status: 422,
    respond: (key) =>
      refusal(422, IDEMPOTENCY_KEY_REUSED, "this key was used for another request", key),
  },
  {
    name: "400 judged",
    role: "judged",
    at: "attempt",
    status: 400,
    respond: (key) => refusal(400, "INVALID_REQUEST", "body is required", key),
  },
  {
    name: "409 KEY_COMMITTED",
    role: "landed",
    at: "attempt",
    respond: (key) => refusal(409, IDEMPOTENCY_KEY_COMMITTED, "this key's write landed", key),
  },
  {
    name: "401 auth",
    role: "unjudged",
    at: "attempt",
    status: 401,
    respond: () => refusal(401, "UNAUTHORIZED", "a token is required"),
  },
  {
    name: "403 READ_ONLY_TOKEN",
    role: "unjudged",
    at: "attempt",
    status: 403,
    respond: () => refusal(403, "READ_ONLY_TOKEN", "this token may only read"),
  },
  {
    name: "403 page",
    role: "unjudged",
    at: "attempt",
    status: 403,
    respond: () => page(403, "Forbidden"),
  },
  {
    name: "404 page",
    role: "unjudged",
    at: "attempt",
    status: 404,
    respond: () => page(404, "Not Found"),
  },
  ...[301, 302, 303, 307, 308].map(
    (status): Kind => ({
      name: `${status} redirect`,
      role: "redirect",
      at: "attempt",
      status,
      respond: (_key, init, run) => redirect(status, init, run),
    })
  ),
  {
    name: "304",
    role: "redirect",
    at: "attempt",
    status: 304,
    respond: () => new Response(null, { status: 304 }),
  },
  {
    name: "503 BROKER_UNAVAILABLE",
    role: "may-land",
    at: "attempt",
    respond: () =>
      refusal(
        503,
        "BROKER_UNAVAILABLE",
        "the secrets broker cannot be reached; try again in a minute"
      ),
  },
  {
    name: "503 echoed",
    role: "may-land",
    at: "attempt",
    respond: (key) => refusal(503, "UNAVAILABLE", "the database is restarting", key),
  },
  { name: "502 page", role: "may-land", at: "attempt", respond: () => page(502, "Bad Gateway") },
  {
    name: "409 KEY_IN_FLIGHT",
    role: "may-land",
    at: "attempt",
    respond: (key) =>
      refusal(
        409,
        IDEMPOTENCY_KEY_IN_FLIGHT,
        "an earlier request with this key is still running",
        key
      ),
  },
  { name: "silence", role: "may-land", at: "attempt", respond: (_key, init) => hang(init.signal) },
  {
    name: "reset",
    role: "may-land",
    at: "attempt",
    respond: () => {
      throw Object.assign(new TypeError("The socket connection was closed"), {
        code: "ECONNRESET",
      });
    },
  },
  {
    name: "429 echoed",
    role: "no-land",
    at: "attempt",
    status: 429,
    respond: (key) => refusal(429, "RATE_LIMITED", "too many requests", key),
  },
  {
    name: "408 page",
    role: "no-land",
    at: "attempt",
    status: 408,
    respond: () => page(408, "Request Timeout"),
  },
  {
    name: "refused",
    role: "no-land",
    at: "attempt",
    respond: () => {
      throw Object.assign(new TypeError("Unable to connect"), { code: "ECONNREFUSED" });
    },
  },
  {
    name: "dead token",
    role: "dead-token",
    at: "attempt",
    status: 401,
    respond: () =>
      refusal(
        401,
        "BROKER_TOKEN_NOT_ACTIVE",
        "the secrets broker does not hold this token as live"
      ),
  },
  {
    name: "no route",
    role: "no-route",
    at: "attempt",
    status: 404,
    respond: () => refusal(404, "NOT_FOUND", "no route for POST /api/v1/issues/DSP-1/comments"),
  },
  {
    name: "deadline in flight",
    role: "deadline-in-flight",
    at: "attempt",
    respond: (_key, init, run) => {
      run.controller.abort(timedOut());
      return hang(init.signal);
    },
  },
  {
    name: "stop in flight",
    role: "cancelled-in-flight",
    at: "attempt",
    respond: (_key, init, run) => {
      run.controller.abort(stopped());
      return hang(init.signal);
    },
  },
  { name: "no credential", role: "no-credential", at: "credential" },
  { name: "deadline", role: "deadline", at: "wait" },
  { name: "stop", role: "cancelled", at: "wait" },
];

const kind = (name: string): Kind => {
  const found = KINDS.find((one) => one.name === name);
  if (found === undefined) throw new Error(`no kind ${name}`);
  return found;
};

/** Takes the script's next step where it is a wait, ending the run's signal as it says. */
function waitStep(run: Run): unknown {
  const next = run.script[run.cursor];
  if (next?.at !== "wait") return undefined;
  run.cursor += 1;
  run.consumed.push(next);
  const reason = next.role === "deadline" ? timedOut() : stopped();
  run.controller.abort(reason);
  return reason;
}

/** Drives one write through `script`. */
async function drive(script: readonly Kind[]): Promise<{ run: Run; error?: unknown }> {
  forgetKeyedLayerMemory();
  const run: Run = {
    script,
    cursor: 0,
    consumed: [],
    wantedMore: false,
    followed: 0,
    keys: [],
    clock: 0,
    controller: new AbortController(),
  };
  const credential: RequestCredential = {
    scheme: "DPoP",
    headers: async (_method, url) => {
      if (url.endsWith("/healthz")) return { Authorization: "DPoP live" };
      const waited = waitStep(run);
      if (waited !== undefined) throw waited;
      const next = run.script[run.cursor];
      if (next?.at === "credential") {
        run.cursor += 1;
        run.consumed.push(next);
        throw new TradeRetryingError("the secrets broker answered 503 temporarily_unavailable");
      }
      return { Authorization: "DPoP live" };
    },
    stale: () => {},
    secret: () => "live",
  };
  const fetchImpl = async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    if (url.endsWith("/healthz")) return json({ status: "ok", idempotency_keys: true }, 200);
    const headers = (init.headers ?? {}) as Record<string, string>;
    const key = headers["Idempotency-Key"];
    run.keys.push(key);
    const next = run.script[run.cursor];
    if (next === undefined) {
      run.wantedMore = true;
      return json(posted, 201, key);
    }
    if (next.at !== "attempt") {
      run.misplaced = `${next.name} reached an attempt`;
      throw new Error(run.misplaced);
    }
    run.cursor += 1;
    run.consumed.push(next);
    return await (next.respond as NonNullable<Kind["respond"]>)(key ?? "", init, run);
  };
  const writeRetry: DispatchWriteRetry = {
    // Two attempts per round on the write's pacing, a third while Dispatch is still running one,
    // all within the client's time; the clock moves only by the pauses.
    pausesMs: [1],
    withinMs: 1,
    silenceMs: 1,
    inFlightPauseMs: 1,
    inFlightWithinMs: 2,
    pause: async (ms) => {
      const waited = waitStep(run);
      if (waited !== undefined) throw waited;
      run.clock += ms;
    },
    now: () => run.clock,
  };
  const client = new DispatchClient(BASE, credential, fetchImpl as typeof fetch, {
    writeRetry,
    signal: run.controller.signal,
    sleep: async () => {
      const waited = waitStep(run);
      if (waited !== undefined) throw waited;
    },
  });
  try {
    await client.comment("DSP-1", comment);
    return { run };
  } catch (error) {
    return { run, error };
  }
}

/** How the rule says a write ends. */
type Ending =
  | { readonly as: "resolved" }
  | { readonly as: "refusal"; readonly status: number }
  | { readonly as: "landed" }
  | { readonly as: "unsettled" }
  | { readonly as: "credential" }
  | { readonly as: "unreachable" }
  | { readonly as: "deadline" }
  | { readonly as: "cancelled" };

const UNSETTLED: Ending = { as: "unsettled" };

/**
 * The rule, on its own: how a write that met `events` ends, and the index of the event after which
 * it must ask no more (undefined where its last event asks again, so the client may ask again or
 * stop for want of attempts or time).
 */
function rule(events: readonly Kind[]): { ending: Ending; endsAt?: number } {
  let landed = false;
  let deadTokens = 0;
  for (const [index, event] of events.entries()) {
    let ending: Ending | undefined;
    const refused: Ending = { as: "refusal", status: event.status ?? 0 };
    switch (event.role) {
      case "applied":
        ending = { as: "resolved" };
        break;
      case "judged":
        ending = refused;
        break;
      case "landed":
        ending = { as: "landed" };
        break;
      case "unjudged":
      case "redirect":
        ending = landed ? UNSETTLED : refused;
        break;
      case "may-land":
        landed = true;
        break;
      case "no-land":
      case "no-route":
        break;
      case "dead-token":
        deadTokens += 1;
        if (deadTokens > 1) ending = landed ? UNSETTLED : refused;
        break;
      case "no-credential":
        if (!landed) ending = { as: "credential" };
        break;
      case "deadline-in-flight":
      case "cancelled-in-flight":
        ending = UNSETTLED;
        break;
      case "deadline":
        ending = landed ? UNSETTLED : { as: "deadline" };
        break;
      case "cancelled":
        ending = landed ? UNSETTLED : { as: "cancelled" };
        break;
    }
    if (ending !== undefined) return { ending, endsAt: index };
  }
  // The client stopped asking after an event that asks again: no attempt or time was left.
  if (landed) return { ending: UNSETTLED };
  const last = events.at(-1);
  if (last?.name === "refused") return { ending: { as: "unreachable" } };
  return { ending: { as: "refusal", status: last?.status ?? 0 } };
}

/** Why `error` (or a resolved write, where it is undefined) is not `ending`; undefined where it is. */
function mismatch(
  ending: Ending,
  error: unknown,
  keys: readonly (string | undefined)[]
): string | undefined {
  const got =
    error === undefined
      ? "resolved"
      : `${(error as Error).name ?? typeof error}: ${String((error as Error).message ?? error).slice(0, 140)}`;
  const unsettled = error instanceof DispatchWriteUnsettledError;
  switch (ending.as) {
    case "resolved":
      return error === undefined ? undefined : `want resolved, got ${got}`;
    case "unsettled":
      if (!unsettled) return `want DispatchWriteUnsettledError, got ${got}`;
      return error.key === keys[0]
        ? undefined
        : `the unsettled error carries key ${error.key}, not ${keys[0]}`;
    case "refusal":
      return !unsettled && error instanceof DispatchServiceError && error.status === ending.status
        ? undefined
        : `want a ${ending.status} refusal, got ${got}`;
    case "landed":
      return error instanceof DispatchWriteLandedError
        ? undefined
        : `want DispatchWriteLandedError, got ${got}`;
    case "credential":
      return error instanceof TradeRetryingError
        ? undefined
        : `want the credential's error, got ${got}`;
    case "unreachable":
      return !unsettled && String((error as Error)?.message).includes("is unreachable")
        ? undefined
        : `want "unreachable", got ${got}`;
    case "deadline":
      return (error as Error)?.name === "TimeoutError"
        ? undefined
        : `want the deadline's TimeoutError, got ${got}`;
    case "cancelled":
      return (error as Error)?.name === "AbortError"
        ? undefined
        : `want the stop's AbortError, got ${got}`;
  }
}

/** What one run broke of the rule, if anything. */
async function judge(script: readonly Kind[]): Promise<{ run: Run; findings: string[] }> {
  const { run, error } = await drive(script);
  const path = run.consumed.map((one) => one.name).join(" > ");
  const findings: string[] = [];
  if (run.misplaced !== undefined) findings.push(`[${path}] harness: ${run.misplaced}`);
  if (run.followed > 0) findings.push(`[${path}] followed a redirect`);
  if (new Set(run.keys).size > 1)
    findings.push(`[${path}] the attempts carried ${new Set(run.keys).size} keys`);
  const verdict = rule(run.consumed);
  if (
    verdict.endsAt !== undefined &&
    (verdict.endsAt < run.consumed.length - 1 || run.wantedMore)
  ) {
    findings.push(`[${path}] asked again after ${run.consumed[verdict.endsAt]?.name}`);
  } else if (!run.wantedMore) {
    const wrong = mismatch(verdict.ending, error, run.keys);
    if (wrong !== undefined) findings.push(`[${path}] ${wrong}`);
  }
  return { run, findings };
}

/** The events that reach each state: none, and each that keeps nothing landed, for
 *  `nothing-sent`; each that may land, for `may-have-landed`. */
const STATE_PREFIXES: readonly (readonly Kind[])[] = [
  [],
  ...KINDS.filter((one) => ["no-land", "dead-token", "no-route"].includes(one.role)).map((one) => [
    one,
  ]),
  ...KINDS.filter((one) => one.role === "may-land").map((one) => [one]),
];

/** Endings that say which state the write was in when they came. */
const STATE_PROBES: readonly Kind[] = [kind("401 auth"), kind("302 redirect"), kind("deadline")];

beforeEach(forgetKeyedLayerMemory);

test("every (state, event) pair ends as the rule says", async () => {
  const findings = new Set<string>();
  let runs = 0;
  for (const prefix of STATE_PREFIXES) {
    for (const event of KINDS) {
      for (const probe of [undefined, ...STATE_PROBES]) {
        const script = probe === undefined ? [...prefix, event] : [...prefix, event, probe];
        const { findings: found } = await judge(script);
        runs += 1;
        for (const one of found) findings.add(one);
      }
    }
  }
  expect(runs).toBeGreaterThan(1_000);
  expect([...findings].slice(0, 40)).toEqual([]);
}, 120_000);

/** The events of each kind of step that asks again, rotated along a path so each appears. */
const ASKING_AGAIN: readonly (readonly Kind[])[] = [
  KINDS.filter((one) => one.role === "may-land"),
  KINDS.filter((one) => one.role === "no-land"),
  [kind("dead token")],
  [kind("no route")],
  [kind("no credential")],
];

/** One ending of each role. */
const ENDINGS: readonly Kind[] = [
  kind("201"),
  kind("422 KEY_REUSED"),
  kind("409 KEY_COMMITTED"),
  kind("401 auth"),
  kind("403 page"),
  kind("308 redirect"),
  kind("304"),
  kind("deadline in flight"),
  kind("stop in flight"),
  kind("deadline"),
  kind("stop"),
];

test("every sequence of events up to the client's limits ends as the rule says", async () => {
  const findings = new Set<string>();
  let runs = 0;
  let longest = 0;
  const explore = async (prefix: readonly Kind[]): Promise<void> => {
    const { run, findings: found } = await judge(prefix);
    runs += 1;
    longest = Math.max(longest, run.consumed.length);
    for (const one of found) findings.add(one);
    if (!run.wantedMore || found.length > 0) return;
    for (const [index, choices] of ASKING_AGAIN.entries()) {
      const pick = choices[(prefix.length + index) % choices.length] as Kind;
      await explore([...prefix, pick]);
    }
    for (const ending of ENDINGS) {
      const { findings: endFound } = await judge([...prefix, ending]);
      runs += 1;
      for (const one of endFound) findings.add(one);
    }
  };
  await explore([]);
  expect(runs).toBeGreaterThan(1_000);
  expect(longest).toBeGreaterThan(5);
  expect([...findings].slice(0, 40)).toEqual([]);
}, 600_000);
