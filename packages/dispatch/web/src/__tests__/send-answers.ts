import { type Mock, mock, spyOn } from "bun:test";

import { api, type WriteBounds, type WriteMethod, type WriteRequest } from "../api/client";
import type { Result } from "../api/paths";
import type {
  AgentMarkV2Input,
  CreateAgentMessageInput,
  IssueMarkV2Input,
  MarksPatchV2Input,
  MarkUpdatesV2,
  MarkV2,
  PatchIssueV2Input,
} from "../api/types";

/** What a test answers the writes made from their request alone with (`answerSends`). */
export interface SendAnswers {
  /** Has `fn` answer every attempt of `request`, and hands `request` back. */
  readonly answer: <R extends WriteRequest>(
    request: R,
    fn: (bounds: WriteBounds) => Promise<unknown>
  ) => R;
  /** Every request answered here, in order. */
  readonly calls: readonly WriteRequest[];
  /** Puts `api.send` back. */
  readonly restore: () => void;
}

/**
 * Answers the writes a test makes from their request alone (`WriteSpec.request`, every plain write
 * on a line): each attempt of such a write is `api.send(request)` with the very request it named,
 * so `answer(request, fn)` has `fn` answer every attempt of it, telling `sent` the request first as
 * the client does. A request the test named no answer for goes through the real `api.send`.
 */
export function answerSends(): SendAnswers {
  const answers = new WeakMap<WriteRequest, (bounds: WriteBounds) => Promise<unknown>>();
  const calls: WriteRequest[] = [];
  const real = api.send.bind(api);
  const spy = spyOn(api, "send").mockImplementation(((
    request: WriteRequest,
    bounds: WriteBounds
  ) => {
    const answer = answers.get(request);
    if (answer === undefined) return real(request, bounds);
    calls.push(request);
    bounds.sent?.(request);
    return answer(bounds);
  }) as typeof api.send);
  return {
    answer: (request, fn) => {
      answers.set(request, fn);
      return request;
    },
    calls,
    restore: () => spy.mockRestore(),
  };
}

/** A route's answer, called with the segments its path's `*`s matched, the body and the bounds. */
type RouteAnswer = (...args: never[]) => Promise<unknown>;

/** The same answer as the router calls it, with what one request carries. */
type RouteCall = (...args: unknown[]) => Promise<unknown>;

/** One route a test answers: its method, its path with each `*` one segment, and the answer. */
interface Route {
  readonly method: WriteMethod;
  readonly path: RegExp;
  readonly answer: RouteCall;
}

/** The routes tests answer now, newest first, and the one spy on `api.send` that reads them. */
const routes: Route[] = [];
let routing: { restore: () => void } | undefined;

/**
 * Answers every write to Dispatch whose request is `method` on `path` - each `*` in it one path
 * segment - with a mock of its own, however the page made it: from its request alone (a plain write
 * on a line), or through the client method that builds the same request. The mock is called with
 * each segment the `*`s matched (decoded), the request's body and the attempt's bounds - its key
 * among them - so a test asserts the request a write sent, its method and path by the route it
 * answered on and its body and key by the call (`toHaveBeenCalledWith("CORE-1", { priority: 2 },
 * keyedAttempt)`). The newest route that matches answers; any other request goes to the real
 * `api.send`. A route answers until its test ends (`resetRouteAnswers`); `mockRestore` only
 * takes its answers back.
 */
export function answerRoute<F extends RouteAnswer>(method: WriteMethod, path: string): Mock<F> {
  const pattern = new RegExp(
    `^${path
      .split("*")
      .map((part) => part.replace(/[.?+^$|()[\]{}\\]/gu, "\\$&"))
      .join("([^/?]+)")}$`,
    "u"
  );
  // Unanswered until the test says how: the route's answer type is the test's to name.
  const unanswered = (async () => undefined) as unknown as F;
  const answer = mock<F>(unanswered);
  // The router hands each answer what one request carries, whatever the route's own signature.
  const call = answer as unknown as RouteCall;
  const route: Route = { answer: call, method, path: pattern };
  routes.unshift(route);
  if (routing === undefined) {
    const real = api.send.bind(api);
    const spy = spyOn(api, "send").mockImplementation(((
      request: WriteRequest,
      bounds: WriteBounds
    ) => {
      for (const { answer: answered, method: on, path: at } of routes) {
        const matched = request.method === on ? at.exec(request.path) : null;
        if (matched === null) continue;
        bounds.sent?.(request);
        return answered(...matched.slice(1).map(decodeURIComponent), request.body, bounds);
      }
      return real(request, bounds);
    }) as typeof api.send);
    routing = { restore: () => spy.mockRestore() };
  }
  return answer;
}

/** `PATCH /api/v2/issues/{key}` (`issuePatchRequest`): called with the key, the change and the
 *  bounds. */
export const answerIssuePatches = () =>
  answerRoute<
    (
      key: string,
      input: PatchIssueV2Input,
      bounds: WriteBounds
    ) => Promise<Result<"/api/v2/issues/{key}", "patch">>
  >("PATCH", "/api/v2/issues/*");

/** `PATCH /api/v2/me/marks/issue/{key}` (`issueStateRequest`): the person's mark of one issue. */
export const answerIssueStates = () =>
  answerRoute<(key: string, input: IssueMarkV2Input, bounds: WriteBounds) => Promise<MarkV2>>(
    "PATCH",
    "/api/v2/me/marks/issue/*"
  );

/** `PATCH /api/v2/me/marks/agent/{session_id}` (`agentStateRequest`): the person's mark of one
 *  session. */
export const answerAgentStates = () =>
  answerRoute<(session: string, input: AgentMarkV2Input, bounds: WriteBounds) => Promise<MarkV2>>(
    "PATCH",
    "/api/v2/me/marks/agent/*"
  );

/** `PATCH /api/v2/me/marks/thread/{id}` (`threadReadRequest`): a thread's read mark. */
export const answerThreadReads = () =>
  answerRoute<
    (id: string, body: { read_through: string }, bounds: WriteBounds) => Promise<unknown>
  >("PATCH", "/api/v2/me/marks/thread/*");

/** `PATCH /api/v2/me/marks` (`marksRequest`): the Inbox's Mark all read, its Dismiss and its Move
 *  to Inbox, called with the body - the marks it names (`items`) or the narrowing it is under
 *  (`filter`), with what it sets (`set`) - and the bounds. */
export const answerMarksPatches = () =>
  answerRoute<(body: MarksPatchV2Input, bounds: WriteBounds) => Promise<MarkUpdatesV2>>(
    "PATCH",
    "/api/v2/me/marks"
  );

/** `PATCH /api/v2/me/marks/ask/{id}` (`askSnoozeRequest`): the person's snooze of one ask. */
export const answerAskSnoozes = () =>
  answerRoute<
    (id: string, body: { snoozed_until: string | null }, bounds: WriteBounds) => Promise<MarkV2>
  >("PATCH", "/api/v2/me/marks/ask/*");

/** `POST /api/v2/agents/{session_id}/messages` (`agentMessageRequest`). */
export const answerAgentMessages = () =>
  answerRoute<
    (
      session: string,
      input: CreateAgentMessageInput,
      bounds: WriteBounds
    ) => Promise<Result<"/api/v2/agents/{session_id}/messages", "post">>
  >("POST", "/api/v2/agents/*/messages");

/** Lets every route a test answered go, and puts `api.send` back: the unit-test preload, after
 *  each test. */
export function resetRouteAnswers(): void {
  routes.length = 0;
  routing?.restore();
  routing = undefined;
}
