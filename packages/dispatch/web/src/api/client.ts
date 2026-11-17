import {
  AUTH_NO_IDENTITY,
  answerNotKept,
  DISPATCH_LOGIN_HEADER,
  IDEMPOTENCY_ANSWER_NOT_KEPT,
  IDEMPOTENCY_KEY_COMMITTED,
  IDEMPOTENCY_KEY_HEADER,
  IDEMPOTENCY_KEY_IN_FLIGHT,
  IDEMPOTENCY_REPLAYED_HEADER,
  type IdempotencyLandedCode,
  NO_ROUTE_MESSAGE_PREFIX,
  NO_ROUTE_ROLLING_RELEASE_MESSAGE,
  retryNoRoute,
  type Sleep,
  sleep,
} from "@legion/contracts";

import {
  type Answer,
  type Body,
  type FormBody,
  type Json200,
  type Op,
  type PathParams,
  type PathsOf,
  type Query,
  type QueryParams,
  type RequestBody,
  repoParams,
  routeUrl,
} from "./paths";
import { reachable, routeOf, unreachable } from "./reachability";

import type {
  AgentMarkV2Input,
  AnswerAskStateInput,
  ArchitectureSource,
  ArtifactText,
  ArtifactVersionContent,
  ArtifactVersionText,
  AskWithThread,
  AuthenticatedUser,
  CreateArtifactInput,
  CredentialPendingRow,
  DeclineAskStateInput,
  DispatchEvent,
  DuplicateCandidate,
  IDCandidate,
  InboxRow,
  IssueDetails,
  IssueMarkV2Input,
  Marks,
  MarkV2,
  TargetCandidate,
} from "./types";

export type FetchImplementation = (
  input: RequestInfo | URL,
  init?: RequestInit
) => Promise<Response>;

/**
 * How an upload reaches Dispatch: `init` (its body a multipart form) sent to `path`, answered with
 * Dispatch's response, and `progress` called as each piece of the body goes up - with how much of
 * it has gone (`sent`), when the transport knows - and as the answer comes back. The browser's
 * `fetch` reports no upload progress, so a large file on a slow line would look silent for as long
 * as it uploads; `xhrUpload` is the one that reports it.
 */
export type UploadImplementation = (
  path: string,
  init: RequestInit,
  progress: (sent?: UploadProgress) => void
) => Promise<Response>;

/** How much of an upload's body has gone up, in bytes, out of how many. */
export interface UploadProgress {
  readonly loaded: number;
  readonly total: number;
}

/** The page of a list route, as `readWholeList` reads each one: its rows, how many the route holds
 *  in all (`null` on the one route that counts none), and the limit the page was answered under. */
interface ListPage<T> {
  readonly items: readonly T[] | null;
  readonly limit: number;
  readonly total: number | null;
}

/** Every GET route whose answer is a page of a list: `{items, total, limit, offset}`, with what
 *  stands beside them. */
type ListPattern = {
  [P in PathsOf<"get">]: Json200<Op<P, "get">> extends { items: unknown; limit: number }
    ? P
    : never;
}[PathsOf<"get">];

/** One row of a list route's page. */
type ItemOf<P extends ListPattern> =
  Json200<Op<P, "get">> extends { items: readonly (infer T)[] | null } ? T : never;

/** A list route's answer read whole: the first page's members with every row of every page. */
export type WholeList<P extends ListPattern> = Omit<Json200<Op<P, "get">>, "items"> & {
  readonly items: ItemOf<P>[];
};

/** One item a refusal lists for the caller to choose between: the items an ambiguous id prefix
 *  names (`ID_AMBIGUOUS`), the issues a duplicate check found, or the positions an anchored
 *  quote resolved at. Each refusal's own `code` says which of the three its list holds. */
export type ErrorCandidate = IDCandidate | DuplicateCandidate | TargetCandidate;

interface ErrorPayload {
  code?: string;
  error?: string;
  state?: MarkV2;
  candidates?: readonly ErrorCandidate[];
}

export class ApiError extends Error {
  readonly code: string | undefined;
  readonly status: number;
  /** The mark Dispatch holds, which a mark's write it refused `409 STATE_STALE` answers beside
   *  the refusal (`MarkStaleV2`), so the page writes again over it. */
  readonly state: MarkV2 | undefined;
  /** What the refusal listed for the caller to choose between, read by the page that shows the
   *  choice; the shape of each entry follows `code` (`ErrorCandidate`). Undefined for a refusal
   *  that listed nothing, which is almost all of them. */
  readonly candidates: readonly ErrorCandidate[] | undefined;
  /** How long the server asked the caller to wait before trying again (`Retry-After` in
   *  delta-seconds, as Dispatch sends with a 503 refusing excess load), in milliseconds;
   *  undefined when the response carried none. */
  readonly retryAfterMs: number | undefined;

  constructor(status: number, payload: ErrorPayload, retryAfterMs?: number) {
    super(payload.error ?? `Dispatch request failed (${status})`);
    this.name = "ApiError";
    this.code = payload.code;
    this.status = status;
    this.state = payload.state;
    this.candidates = payload.candidates;
    this.retryAfterMs = retryAfterMs;
  }
}

/** A request the browser could not get an answer to at all: the connection failed or dropped
 *  (`fetch`'s and XHR's own error). It is no refusal - the request may have reached Dispatch - so
 *  a read is asked again by the queries' policy and a write again under its key. */
export class NetworkError extends Error {
  readonly path: string;

  constructor(path: string, cause: unknown) {
    super("Dispatch could not be reached", { cause });
    this.name = "NetworkError";
    this.path = path;
  }
}

/** A 2xx whose body is not JSON - a proxy's page, a body cut short - so it says nothing of what
 *  Dispatch did. The write is asked again under its key, and Dispatch answers the repeat with the
 *  first's answer. */
export class UnreadableAnswerError extends Error {
  readonly status: number;

  constructor(status: number, cause: unknown) {
    super(`Dispatch answered ${status} with a body that is not JSON`, { cause });
    this.name = "UnreadableAnswerError";
    this.status = status;
  }
}

/** The kinds `GET /api/v2/asks?source=<kind>:<id>` reads a reference source as: a document
 *  version, an ask, a comment or an issue message. The route takes the source as one string, so
 *  the document types no member these could be read from. */
export type ReferenceSourceKind = "artifact" | "ask" | "comment" | "message";

/** How long the page lets Dispatch go silent on a request - no response headers yet, no piece of
 *  its body, and for an upload no piece of the request body sent either - before it gives the
 *  request up (`RequestTimeoutError`). A read Dispatch never answers, or stops answering halfway,
 *  would otherwise hold its page loading for good; given up, it is retried by the queries' shared
 *  policy, as a dropped connection is. A body that keeps moving, either way, goes on however long
 *  it takes: the deadline is on the silence, not the transfer. */
export const REQUEST_DEADLINE_MS = 30_000;

/** A request Dispatch went silent on for its deadline, which the page then aborted. It is no
 *  refusal: the request may still have reached Dispatch, so only a read, or a write made again
 *  under its key (`writeUnderKey`), is asked again. */
export class RequestTimeoutError extends Error {
  readonly path: string;

  constructor(path: string, deadlineMs: number) {
    super(`Dispatch did not answer within ${deadlineMs / 1000} seconds`);
    this.name = "RequestTimeoutError";
    this.path = path;
  }
}

/** How a caller bounds one attempt of a request: how long Dispatch may go silent on it (`null` for
 *  no limit; left out, `REQUEST_DEADLINE_MS` for a GET, and no limit for any other method). */
export interface RequestBounds {
  readonly deadlineMs?: number | null;
}

/** Every method a write goes as: the one list `WriteRequest.method` and the check of a kept write
 *  (`write-storage.ts`) are both read from. Every one of them goes under its key (`send`), a `PUT`
 *  and a `DELETE` as well as a `POST` and a `PATCH`: `/api/v2` keeps every write's answer under
 *  its key, so a repeat of any of them is answered with what the first did. */
export const WRITE_METHODS = ["DELETE", "PATCH", "POST", "PUT"] as const;

export type WriteMethod = (typeof WRITE_METHODS)[number];

/** What a request is answered with, for the type checker alone: no request carries it. */
declare const answeredWith: unique symbol;

/** One write as it goes to Dispatch: its method, its path and its body - JSON, a multipart form
 *  (an upload), or none - and, for the type checker, what Dispatch answers it with (`T`): a
 *  request's builder names it (`issuePatchRequest` answers an `Issue`), so a write made from its
 *  request alone (`WriteSpec.request`) is answered as its client method's is. A kept write is
 *  this, made again after a reload (`send`). */
export interface WriteRequest<T = unknown> {
  readonly method: WriteMethod;
  readonly path: string;
  readonly body?: unknown;
  readonly [answeredWith]?: T;
}

/** One attempt of a write, as the one write path (`Writes.run`) hands it over from the press it
 *  belongs to: the key that names the person's action - minted when the person acts, the same on
 *  every attempt of it, what the store keeps it under, and sent as `Idempotency-Key` on every
 *  write - the login of the person who pressed it (`Dispatch-Login`, sent on every write, so
 *  Dispatch refuses it under anyone else's session), the attempt's silence deadline, `sent`, told
 *  the request as it goes out, for a caller that keeps it to make again, and `progress`, told how
 *  much of an upload's body has gone up. Every write method takes these, so a write made outside
 *  the store does not type-check. */
export interface WriteBounds extends RequestBounds {
  readonly key: string;
  readonly login?: string;
  readonly sent?: (request: WriteRequest) => void;
  readonly progress?: (loaded: number, total: number) => void;
}

/** A write Dispatch had already made, with no answer it can give for it (`code`): the first
 *  answer was too large for it to keep, so the repeat came back with the first one's status and
 *  no resource (`IDEMPOTENCY_ANSWER_NOT_KEPT`), or the answer was lost before Dispatch stored it,
 *  so the repeat came back `409 KEY_COMMITTED` (`isKeyCommitted`). The write landed; its caller
 *  reads what it wrote again, and shows no refusal. */
export class AnswerNotKeptError extends Error {
  readonly code: IdempotencyLandedCode;
  readonly status: number;

  constructor(code: IdempotencyLandedCode, status: number, message: string) {
    super(message);
    this.name = "AnswerNotKeptError";
    this.code = code;
    this.status = status;
  }
}

/** Whether Dispatch is still running the first request under this write's key: nothing ran, and
 *  the write is asked again under the same key. */
export function isKeyInFlight(error: unknown): error is ApiError {
  return (
    error instanceof ApiError && error.status === 409 && error.code === IDEMPOTENCY_KEY_IN_FLIGHT
  );
}

/** Whether Dispatch made this write's key's write already, and lost the answer before it stored
 *  it (`409 KEY_COMMITTED`, even after a 5xx the first attempt was answered): the write landed, and
 *  Dispatch will not run it again. */
export function isKeyCommitted(error: unknown): error is ApiError {
  return (
    error instanceof ApiError && error.status === 409 && error.code === IDEMPOTENCY_KEY_COMMITTED
  );
}

// A 401 means "not signed in" and should never be retried or confused with a transient
// network/5xx failure, which the caller should retry and eventually surface with a Retry
// affordance instead of bouncing the user to the sign-in page.
export function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

/** Whether `error` is the sign-in check's `503 EVENT_HEAD_UNAVAILABLE`: Dispatch has no event id
 *  it can vouch for as final yet, which a process just restarted while an event write from before
 *  it is still open answers until that write ends. The page asks again until it answers
 *  (`queryRetry`); nothing about the session is wrong. */
export function isEventHeadUnavailable(error: unknown): boolean {
  return (
    error instanceof ApiError && error.status === 503 && error.code === "EVENT_HEAD_UNAVAILABLE"
  );
}

/** Whether `error` is Dispatch's own answer that the caller is signed in to nobody
 *  (`401 AUTH_NO_IDENTITY`), rather than a 401 from something in front of Dispatch - an
 *  authenticating proxy refusing the request, which says nothing about the Dispatch session. A
 *  sign-out's landing turns on the difference: Dispatch's 401 means the session it was ending is
 *  already gone, and a proxy's means the sign-out never reached Dispatch at all.
 */
export function isNoIdentity(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401 && error.code === AUTH_NO_IDENTITY;
}

// The architecture TREE read answers this 404 for a project with no source — definitive like an
// auth outcome, since retrying changes nothing. The project's source list answers `[]` instead:
// the question it asks has "no source" as an ordinary answer, and a 404 made every reader of an
// ordinary project page log a failed request.
export function isSourceNotFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 && error.code === "SOURCE_NOT_FOUND";
}

// A document that no longer exists cannot be restored, so its browser-held edits are discarded
// when the admission read confirms this exact server response. Other 404s remain retryable.
export function isArtifactNotFound(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 && error.code === "ARTIFACT_NOT_FOUND";
}

// A Dispatch with no secrets broker configured (no DISPATCH_AGENT_SECRETS_URL) answers every
// credential route but the pending list with this 404 — the same class as the architecture-source
// 404 above it: retrying changes nothing. The pending list, which every page reads, answers `null`
// instead (`getCredentialPending`).
export function isCredentialFeatureOff(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 && error.code === "FEATURE_OFF";
}

// The delivery timeline answers this 404 until someone sets its configuration: definitive like the
// two above, since retrying changes nothing until the settings form is saved.
export function isDeliveryNotConfigured(error: unknown): boolean {
  return (
    error instanceof ApiError && error.status === 404 && error.code === "DELIVERY_NOT_CONFIGURED"
  );
}

// A stored document outside the Proof schema is a repairable state, not a transient failure:
// retrying the read cannot repair it, while opening its live editor could let the browser rewrite it.
export function isDocumentSchemaError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && error.code === "DOC_SCHEMA";
}

// The server refusing a send because the document does not hold the mark it names
// (`docs.VerifyMark`): the margin answers it by writing the mark again over the same words.
export function isAnchorMissing(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && error.code === "ANCHOR_MISSING";
}

// The server refusing a write because its text passes a bound Dispatch holds it to: one block
// too long, too many blocks, or an ask's question and options too long together. The refusal
// names what passed which bound, so a page shows those words rather than wording of its own.
export function isCapExceeded(error: unknown): error is ApiError {
  return error instanceof ApiError && error.code === "CAP_EXCEEDED";
}

// The server refusing a write because its text holds a decision block, which nobody can answer
// where it stands (`itemblocks.DecisionBlock`, INFRA-1201). The refusal names the block and says
// where the question belongs, so a page shows those words rather than wording of its own; a
// retry of the same text is refused again, as it is for a bound.
export function isDecisionBlockInBody(error: unknown): error is ApiError {
  return error instanceof ApiError && error.code === "DECISION_BLOCK_IN_BODY";
}

// A document whose stored history cannot load at all: the state a rebuild from its latest saved
// version repairs, and retrying the read cannot.
export function isDocumentUnloadable(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && error.code === "DOCUMENT_UNLOADABLE";
}

// A stored document holding an element this Dispatch process's build cannot read: a newer build
// wrote it while two builds serve one database during a release. It is not a repairable state, and
// a replacement would delete the element. A later read can reach a build that reads it, so the read
// is retried like any transient failure.
export function isDocumentNewerContent(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && error.code === "DOC_NEWER_CONTENT";
}

/** The code `/api/v2` answers a path no route of it serves with (`404`). */
const NO_ROUTE_CODE = "NO_ROUTE";

// Whether `error` is a Dispatch process's answer that it has no route for the request: `404
// NO_ROUTE`, how `/api/v2` says it, or `404 NOT_FOUND` whose message begins
// NO_ROUTE_MESSAGE_PREFIX, how a process of a release before `/api/v2` says it of every path it
// does not serve - an `/api/v2` path among them. While a release rolls out, two builds of Dispatch
// answer the page, and the older one answers this way for a route only the newer one serves. No
// handler ran, so the request is safe to send again whatever its method. A `404 NOT_FOUND` for a
// resource the server does not know says "not found" instead.
export function isNoRoute(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    error.status === 404 &&
    (error.code === NO_ROUTE_CODE ||
      (error.code === "NOT_FOUND" && error.message.startsWith(NO_ROUTE_MESSAGE_PREFIX)))
  );
}

/** What every view says while Envoy's agent list refills: the server's own `ENVOY_REFILLING`
 *  message, shown wherever a view would otherwise say an agent is absent. */
export const AGENT_LIST_REFILLING = "The agent list is refilling. Try again in a moment.";

// The agents list and the subscriber reads answer this 503 while the Envoy listener refills its
// copy of the session registry: the list is not known yet, so nothing on it may be read as gone.
// It is transient, so the shared retry policy keeps retrying it like any other 5xx.
export function isAgentListRefilling(error: unknown): boolean {
  return error instanceof ApiError && error.status === 503 && error.code === "ENVOY_REFILLING";
}

// The retry policy every query in the app shares: an auth outcome (401), a missing architecture
// source, an unconfigured credential broker or delivery timeline, a stored document outside
// Proof's schema, or a stored history that cannot load is definitive and retrying it changes
// nothing; any other failure (dropped connection, a read past its deadline, 5xx, a document
// holding content this build cannot read yet) is worth a couple of automatic attempts before
// surfacing a Retry affordance to the user.
export function isRetryableQueryError(error: unknown): boolean {
  return (
    !isUnauthorized(error) &&
    !isSourceNotFound(error) &&
    !isCredentialFeatureOff(error) &&
    !isDeliveryNotConfigured(error) &&
    !isDocumentSchemaError(error) &&
    !isDocumentUnloadable(error)
  );
}

/** The codes of the two `503`s Dispatch answers with a `Retry-After` it means: its database is
 *  too busy to take the request now (`DATABASE_BUSY`), or the request ran past the time Dispatch
 *  gives one (`REQUEST_TIMEOUT`). Any other `503` - a gateway's, or Dispatch's own without one of
 *  these codes - names no wait the page can trust, and keeps the page's own backoff. */
const BUSY_CODES: ReadonlySet<string> = new Set(["DATABASE_BUSY", "REQUEST_TIMEOUT"]);

/** How long Dispatch asked to be left before this request is asked again, when it was too busy
 *  for it (`BUSY_CODES`) and said so with a `Retry-After`; `undefined` for anything else. A
 *  write's retry waits it out (`features/writes/keyed-write.ts`). */
export function busyRetryAfterMs(error: unknown): number | undefined {
  if (!(error instanceof ApiError) || error.status !== 503) return undefined;
  return error.code !== undefined && BUSY_CODES.has(error.code) ? error.retryAfterMs : undefined;
}

// The reason to show a reader for a failed write: the server's own message when it answered
// with one, that it did not answer in time when it did not, otherwise the caller's fallback line
// (the request never reached the server, or died in transit).
export function apiErrorMessage(error: unknown, fallback: string): string {
  return error instanceof ApiError || error instanceof RequestTimeoutError
    ? error.message
    : fallback;
}

// Whether the server refused with one of `codes`: a refusal the same request meets every time,
// which its caller shows without a Retry.
export function apiErrorCodeIn(error: unknown, codes: ReadonlySet<string>): error is ApiError {
  return error instanceof ApiError && error.code !== undefined && codes.has(error.code);
}

/** Who an uploaded file belongs to: an issue, a project (its documents), or an agent's
 *  conversation, named by the agent's session id (direct messages on the Agents page). */
export type ArtifactOwner = { issue: string } | { project: string } | { session: string };

export type CreateArtifactReviewInput = Body<"/api/v2/artifacts/{id}/reviews", "post">;

/** What a decline carries: the person's own note, which is optional. */
export type DeclineAskInput = Omit<DeclineAskStateInput, "state">;

/** What an answer carries: the options picked, the text, and the times the answer was read at. */
export type AnswerAskInput = Omit<AnswerAskStateInput, "state">;

/** Whether `error` is the `410` a deployment answers on every `/api/v1` path once its end of
 *  `/api/v1` has passed (`API_V1_ENDED`). The page itself calls `/api/v2` alone; the one request
 *  that can meet it is the replay of a write a build older than this one kept (`Writes.resume`),
 *  which goes to the path it was first sent to. */
export function isApiV1Ended(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 410;
}

/** Every write verb, as the document names a route's operation; the request carries the method
 *  it uppercases to, which is `WRITE_METHODS`' member of the same name. */
type WriteVerb = Lowercase<WriteMethod>;

/**
 * One write as it goes to Dispatch, named by the route pattern the document holds it under: the
 * URL built from `params` (`routeUrl`, which throws for a parameter the pattern does not hold or
 * has no value for), the body the document gives that route, and - for the type checker alone -
 * the body the document says it is answered with. Nothing here restates a wire shape.
 */
export function writeRequest<M extends WriteVerb, P extends PathsOf<M>>(
  verb: M,
  pattern: P,
  params: PathParams<Op<P, M>>,
  body?: RequestBody<Op<P, M>> | FormBody<Op<P, M>>
): WriteRequest<Answer<Op<P, M>>> {
  return {
    ...(body === undefined ? {} : { body }),
    method: verb.toUpperCase() as WriteMethod,
    path: routeUrl(pattern, params),
  };
}

// The requests of the writes that wait their turn on a line (`WriteSpec.request`), each the exact
// request its client method sends, answered as that method is: a surface names it at the press,
// and the write is made from it alone (`api.send`), so a write waiting behind another is kept as
// the very request that goes out, and nothing of it runs before its turn.

/** `PATCH /api/v2/issues/{key}`: one change of an issue's fields (`patchIssue`). */
export function issuePatchRequest(key: string, input: Body<"/api/v2/issues/{key}", "patch">) {
  return writeRequest("patch", "/api/v2/issues/{key}", { key }, input);
}

/** `POST /api/v2/agents/{session_id}/messages`: a person's message to one session
 *  (`createAgentMessage`). */
export function agentMessageRequest(
  sessionID: string,
  input: Body<"/api/v2/agents/{session_id}/messages", "post">
) {
  return writeRequest(
    "post",
    "/api/v2/agents/{session_id}/messages",
    { session_id: sessionID },
    input
  );
}

/** One of the person's marks, `PATCH /api/v2/me/marks/{kind}/{id}`: the body is the fields of
 *  `kind`'s mark it changes, and Dispatch answers the mark as it stands after the write. */
function markRequest(kind: MarkV2["kind"], id: string, input: MarkInput) {
  return writeRequest("patch", "/api/v2/me/marks/{kind}/{id}", { id, kind }, input);
}

/** What one mark's write carries: the fields of the kind its path names. */
type MarkInput = Body<"/api/v2/me/marks/{kind}/{id}", "patch">;

/** The person's snooze of an ask, `PATCH /api/v2/me/marks/ask/{id}` (`snoozeAsk`): an RFC 3339
 *  time folds the Inbox row away until then, and `null` brings it back now. */
export function askSnoozeRequest(id: string, snoozedUntil: string | null) {
  return markRequest("ask", id, { snoozed_until: snoozedUntil });
}

/** A thread's read mark, `PATCH /api/v2/me/marks/thread/{id}` (`markThreadRead`). */
export function threadReadRequest(threadId: string, readThrough: string) {
  return markRequest("thread", threadId, { read_through: readThrough });
}

/** `PATCH /api/v2/me/marks`: the Inbox's **Mark all read** (`set: {read_through}`), its
 *  **Dismiss** and the **Move to Inbox** that puts a thread back. One request names either the
 *  marks it writes (`items`, each `{kind, id, ...}` as its own mark's write takes it) or the
 *  narrowing the list is under (`filter`) with what to set on every thread it narrows to, as of
 *  that list read's own `as_of`; naming both is refused. */
export function marksRequest(body: Body<"/api/v2/me/marks", "patch">) {
  return writeRequest("patch", "/api/v2/me/marks", {}, body);
}

/** The person's last read version of a document, `PATCH /api/v2/me/marks/document/{id}`
 *  (`putDocumentRead`). */
export function documentReadRequest(id: string, version: number) {
  return markRequest("document", id, { read_version: version });
}

/** The person's state of an issue, `PATCH /api/v2/me/marks/issue/{key}` (`putIssueState`): its
 *  pin, its read mark and its pinned items, under the next `seq`. */
export function issueStateRequest(key: string, input: IssueMarkV2Input) {
  return markRequest("issue", key, input);
}

/** The person's state of a session, `PATCH /api/v2/me/marks/agent/{session_id}`
 *  (`putAgentState`) - a Clear, a read mark, or the pin the rail's Agents list is drawn from
 *  (`pinned`). */
export function agentStateRequest(sessionID: string, input: AgentMarkV2Input) {
  return markRequest("agent", sessionID, input);
}

/** The same-origin route of one stored version's bytes, addressed by the document's id, so an
 *  `<img>` loads it with the reader's own cookie. A version never changes, so the server lets
 *  the browser keep it. `download` is a link's: the server answers no-cache there, and a
 *  document as its Markdown file; a picture keeps the year-long cache. */
export function artifactVersionPath(
  id: string,
  version: number,
  { download = false }: { readonly download?: boolean } = {}
): string {
  return routeUrl(
    "/api/v2/artifacts/{id}/versions/{number}" satisfies PathsOf<"get">,
    { id, number: String(version) },
    download ? { download: true } : undefined
  );
}

/** What an upload carries: the JSON body the document gives the three artifact routes for text
 *  written in the browser and for a title typed with nothing under it, and a multipart form - the
 *  body those same routes take as `multipart/form-data` - for a file picked, pasted or dropped.
 *  A create carrying neither a file nor content makes an empty document, so it goes as JSON with
 *  the name alone. */
function artifactUploadBody(
  input: CreateArtifactInput
): Body<"/api/v2/issues/{key}/artifacts", "post"> | FormData {
  if (input.file === undefined) {
    return { actor: input.actor, content: input.content, name: input.name, summary: input.summary };
  }
  const form = new FormData();
  form.set("name", input.name);
  if (input.summary !== undefined) {
    form.set("summary", input.summary);
  }
  if (input.actor !== undefined) {
    form.set("actor", JSON.stringify(input.actor));
  }
  form.set("file", input.file);
  return form;
}

/** The wait `Retry-After` names, in milliseconds: its delta-seconds form, which is the one
 *  Dispatch sends. An HTTP-date (Dispatch never sends one) or any other value is no delay. */
function retryAfterMs(response: Response): number | undefined {
  const retryAfter = response.headers.get("Retry-After")?.trim();
  return retryAfter !== undefined && /^\d+$/.test(retryAfter)
    ? Number(retryAfter) * 1_000
    : undefined;
}

async function errorFrom(response: Response): Promise<ApiError> {
  const payload = (await response.json().catch(() => ({}))) as ErrorPayload;
  return new ApiError(response.status, payload, retryAfterMs(response));
}

/** The statuses whose response carries no body, which `new Response` refuses one for. */
const NULL_BODY_STATUSES: ReadonlySet<number> = new Set([101, 204, 205, 304]);

/** `XMLHttpRequest`'s response headers (`getAllResponseHeaders`), as `Headers`. */
function headersOf(xhr: XMLHttpRequest): Headers {
  const headers = new Headers();
  for (const line of xhr
    .getAllResponseHeaders()
    .trim()
    .split(/[\r\n]+/u)) {
    const colon = line.indexOf(":");
    if (colon > 0) headers.append(line.slice(0, colon).trim(), line.slice(colon + 1).trim());
  }
  return headers;
}

/**
 * An upload through `XMLHttpRequest`, the one transport whose upload progress every engine the
 * suite runs reports (`upload.onprogress`), so a file on a slow line is never mistaken for a
 * silent Dispatch: `progress` runs as each piece of the body goes up, as the response's headers
 * arrive, and as each piece of the answer does. The request fails as `fetch` does - a `TypeError`
 * for a connection that failed, the signal's reason once `init.signal` aborts.
 */
export function xhrUpload(
  makeXhr: () => XMLHttpRequest = () => new XMLHttpRequest()
): UploadImplementation {
  return (path, init, progress) => {
    const { promise, reject, resolve } = Promise.withResolvers<Response>();
    const xhr = makeXhr();
    const signal = init.signal ?? undefined;
    const aborted = () => xhr.abort();
    xhr.open(init.method ?? "POST", path);
    xhr.withCredentials = init.credentials === "include";
    for (const [name, value] of new Headers(init.headers)) xhr.setRequestHeader(name, value);
    xhr.upload.onprogress = (event) =>
      progress(
        event?.lengthComputable === true ? { loaded: event.loaded, total: event.total } : undefined
      );
    xhr.onprogress = () => progress();
    xhr.onreadystatechange = () => {
      if (xhr.readyState === xhr.HEADERS_RECEIVED) progress();
    };
    xhr.onload = () => {
      signal?.removeEventListener("abort", aborted);
      resolve(
        new Response(NULL_BODY_STATUSES.has(xhr.status) ? null : xhr.responseText, {
          headers: headersOf(xhr),
          status: xhr.status,
          statusText: xhr.statusText,
        })
      );
    };
    xhr.onerror = () => {
      signal?.removeEventListener("abort", aborted);
      reject(new TypeError("Failed to upload"));
    };
    xhr.onabort = () => reject(signal?.reason ?? new DOMException("Aborted", "AbortError"));
    if (signal?.aborted === true) {
      reject(signal.reason);
      return promise;
    }
    signal?.addEventListener("abort", aborted, { once: true });
    xhr.send(init.body as XMLHttpRequestBodyInit | null | undefined);
    return promise;
  };
}

/** `response` with its body watched: `arrived` runs as each piece of it does, for a reader that
 *  bounds the silence between them (`exchange`'s idle deadline) rather than the download. A
 *  response with no body - a 204, a 304 - is handed back as it is: Chromium and WebKit give one
 *  an empty stream all the same, and refuse a new 204 Response built around it. */
function watchBody(response: Response, arrived: () => void): Response {
  const body = response.body;
  if (body === null || NULL_BODY_STATUSES.has(response.status)) return response;
  const pieces = body.getReader();
  const watched = new ReadableStream<Uint8Array>({
    async pull(toReader) {
      const piece = await pieces.read();
      if (piece.done) {
        toReader.close();
        return;
      }
      arrived();
      toReader.enqueue(piece.value);
    },
    cancel: (reason) => pieces.cancel(reason),
  });
  return new Response(watched, {
    headers: response.headers,
    status: response.status,
    statusText: response.statusText,
  });
}

export class DispatchApiClient {
  /** `upload` is the transport of a multipart form; left out, it goes by `fetchImpl`, which
   *  reports no upload progress, so an upload's deadline then runs from its send. `wait` is how
   *  a no-route retry waits (`answered`). */
  constructor(
    private readonly fetchImpl: FetchImplementation,
    private readonly upload?: UploadImplementation,
    private readonly wait: Sleep = sleep
  ) {}

  /** One request to Dispatch, answered with what `read` makes of its response. It is held to a
   *  deadline on Dispatch's silence - `deadlineMs`, or `REQUEST_DEADLINE_MS` for a GET that names
   *  none, and none for any other method that names none - that starts over as the request's body
   *  goes up (an upload's), as the response's headers arrive and as each piece of its body does,
   *  `read` included: a transfer that keeps moving goes on however slowly, either way, and one
   *  that stops fails as headers that never come do. A caller's `init.signal` still aborts the
   *  request, as itself. */
  private async exchange<T>(
    path: string,
    init: RequestInit,
    deadlineMs: number | null | undefined,
    read: (response: Response) => Promise<T>,
    uploaded?: (loaded: number, total: number) => void
  ): Promise<T> {
    const limit =
      deadlineMs !== undefined
        ? deadlineMs
        : (init.method ?? "GET").toUpperCase() === "GET"
          ? REQUEST_DEADLINE_MS
          : null;
    if (limit === null) return read(await this.answered(path, init, undefined, uploaded));
    const deadline = new AbortController();
    let timer = setTimeout(() => deadline.abort(), limit);
    const caller = init.signal ?? undefined;
    const arrived = () => {
      clearTimeout(timer);
      timer = setTimeout(() => deadline.abort(), limit);
    };
    try {
      const signal =
        caller === undefined ? deadline.signal : AbortSignal.any([caller, deadline.signal]);
      return await read(await this.answered(path, { ...init, signal }, arrived, uploaded));
    } catch (error) {
      // An answer Dispatch gave stays its answer and the caller's abort stays the caller's: only
      // what the deadline cut short is a timeout.
      if (deadline.signal.aborted && !(error instanceof ApiError) && caller?.aborted !== true) {
        // Dispatch went silent on it: its route cannot be reached, as far as the page knows.
        unreachable(routeOf(init.method, path));
        throw new RequestTimeoutError(path, limit);
      }
      throw error;
    } finally {
      clearTimeout(timer);
    }
  }

  /** Dispatch's response to one request, or its refusal as an `ApiError`; a connection that
   *  failed is a `NetworkError`, and says Dispatch cannot be reached on its route - its method
   *  and path (`unreachable`). `arrived`, for a request held to a deadline, runs as the request's
   *  body goes up (an upload's), as the response's headers arrive and as each piece of its body
   *  does, the body of a refusal included; `uploaded` is told how much of an upload's body has gone
   *  up. Any answer of Dispatch's own - one below 500, which no gateway gives for a Dispatch that
   *  is down - says it can be reached on that route (`reachable`), which matters only once it
   *  could not be.
   *
   *  A no-route answer is sent again after each of `NO_ROUTE_RETRY_DELAYS_MS`, with the same
   *  `init`, so the same body: while a release rolls out, the next attempt may reach the build
   *  that serves the route. After the last attempt it throws
   *  `NO_ROUTE_ROLLING_RELEASE_MESSAGE`. An abort of `init.signal` during a wait - the caller's,
   *  or the deadline's - rejects at once with the abort's reason and sends nothing more. */
  private answered(
    path: string,
    init: RequestInit,
    arrived?: () => void,
    uploaded?: (loaded: number, total: number) => void
  ): Promise<Response> {
    // Every read and write of the page funnels through here, so this one line asks Dispatch for
    // the display tree of every stored text it answers with (`Dispatch-Trees`); no other client
    // sends it, and no other client's bytes change.
    const sent = {
      credentials: "same-origin",
      ...init,
      headers: [...new Headers(init.headers), ["Dispatch-Trees", "1"]],
    } satisfies RequestInit;
    const progress = (part?: UploadProgress) => {
      arrived?.();
      if (part !== undefined) uploaded?.(part.loaded, part.total);
    };
    const attempt = async (): Promise<Response> => {
      let answer: Response;
      try {
        answer =
          sent.body instanceof FormData && this.upload !== undefined
            ? await this.upload(path, sent, progress)
            : await this.fetchImpl(path, sent);
      } catch (error) {
        // An abort is the deadline's or the caller's (`exchange`), never the network's.
        if (init.signal?.aborted === true) throw error;
        unreachable(routeOf(init.method, path));
        throw new NetworkError(path, error);
      }
      if (answer.status < 500) reachable("answer", routeOf(init.method, path));
      arrived?.();
      const response = arrived === undefined ? answer : watchBody(answer, arrived);
      if (!response.ok) throw await errorFrom(response);
      return response;
    };
    return retryNoRoute(attempt, isNoRoute, {
      exhausted: () =>
        new ApiError(404, { code: "NOT_FOUND", error: NO_ROUTE_ROLLING_RELEASE_MESSAGE }),
      signal: init.signal,
      sleep: this.wait,
    });
  }

  /** The response itself, for a caller that reads its body: the deadline holds until the
   *  response arrives, and the caller reads the body on its own time. */
  private response(
    path: string,
    init: RequestInit = {},
    deadlineMs?: number | null
  ): Promise<Response> {
    return this.exchange(path, init, deadlineMs, async (response) => response);
  }

  private json<T>(path: string, init: RequestInit = {}, deadlineMs?: number | null): Promise<T> {
    return this.exchange(path, init, deadlineMs, (response) => response.json() as Promise<T>);
  }

  /**
   * The one path every write to Dispatch takes. Every write - a `POST`, a `PATCH`, a `PUT` and a
   * `DELETE` - goes under `bounds.key`, the `Idempotency-Key` that names the person's action, so
   * a repeat of it - a retry after a dropped answer, a reload resuming it - is answered with what
   * the first did rather than done twice. The key is the caller's, minted when the person acts
   * (`Writes.run`), never here; so is `bounds.login` (`Dispatch-Login`), the person who pressed
   * it. A JSON body goes as JSON and a multipart form as itself, through the upload transport;
   * `bounds.sent` is told the request as it goes out. Answers what Dispatch answered (nothing for
   * an empty body), as the request's builder names it (`WriteRequest`), or rejects with its
   * refusal; a 2xx whose body is not JSON rejects with `UnreadableAnswerError`, a 2xx whose body
   * stops part way with `NetworkError`, and a replay of an answer Dispatch did not keep with
   * `AnswerNotKeptError`.
   */
  send<T>(
    request: WriteRequest<T>,
    { deadlineMs, key, login, progress, sent }: WriteBounds
  ): Promise<T> {
    const { body, method, path } = request;
    const form = body instanceof FormData ? body : undefined;
    const json = body === undefined || form !== undefined ? undefined : JSON.stringify(body);
    const init: RequestInit = {
      ...(form !== undefined ? { body: form } : json !== undefined ? { body: json } : {}),
      headers: {
        [IDEMPOTENCY_KEY_HEADER]: key,
        ...(login === undefined ? {} : { [DISPATCH_LOGIN_HEADER]: login }),
        ...(json === undefined ? {} : { "Content-Type": "application/json" }),
      },
      method,
    };
    sent?.(request);
    const read = async (response: Response): Promise<T> => {
      let text: string;
      try {
        text = await response.text();
      } catch (error) {
        // The connection dropped as the answer's body came down: the page knows nothing of what
        // Dispatch did, as for one that never answered, so the write is made again under its key.
        throw new NetworkError(path, error);
      }
      let answer: unknown;
      try {
        answer = text === "" ? undefined : JSON.parse(text);
      } catch (error) {
        throw new UnreadableAnswerError(response.status, error);
      }
      if (
        answerNotKept(response.status, response.headers.has(IDEMPOTENCY_REPLAYED_HEADER), answer)
      ) {
        const reason = Reflect.get(answer as object, "error");
        throw new AnswerNotKeptError(
          IDEMPOTENCY_ANSWER_NOT_KEPT,
          response.status,
          typeof reason === "string" ? reason : "Dispatch did not keep this write's answer"
        );
      }
      return answer as T;
    };
    return this.exchange(path, init, deadlineMs, read, progress);
  }

  /** One read of a route the document holds, named by its own path pattern: `params` fills the
   *  pattern's `{name}`s and `query` its query parameters, and what it answers is the document's
   *  `200` body for that route, the display trees Dispatch adds to every stored text it answers
   *  this client with (`Dispatch-Trees`, in `answered`) included: the route table names each
   *  shaped read, so the trees are part of the body the document holds. A read states a response
   *  shape of its own only where the document cannot hold it - `getIssueEvents`, whose events'
   *  payloads it types, and the reads an `include` makes a member present in, each saying beside
   *  it what that is. */
  private async read<P extends PathsOf<"get">>(
    pattern: P,
    params: PathParams<Op<P, "get">>,
    query?: QueryParams<Op<P, "get">>,
    init: RequestInit = {},
    deadlineMs?: number | null
  ): Promise<Json200<Op<P, "get">>> {
    return this.json<Json200<Op<P, "get">>>(routeUrl(pattern, params, query), init, deadlineMs);
  }

  /**
   * Every row of a list route, read a page at a time: each page asks for no `limit`, so it is the
   * route's own default (50 for most lists), and the next one starts where the rows read so far
   * end (`offset`), until the rows read are the `total` the route counts or a page comes back
   * short of its limit - which is also how the one route that counts no total ends. Answers the
   * first page's members beside the rows - its counts, and the `as_of` it was read at, which no
   * later page's rows can be older than - with every page's rows in the route's own order.
   */
  private async readWholeList<P extends ListPattern>(
    pattern: P,
    params: PathParams<Op<P, "get">>,
    query?: Omit<QueryParams<Op<P, "get">>, "limit" | "offset">,
    init: RequestInit = {}
  ): Promise<WholeList<P>> {
    const items: ItemOf<P>[] = [];
    let first: Json200<Op<P, "get">> | undefined;
    for (;;) {
      const offset = items.length === 0 ? {} : { offset: items.length };
      const page = await this.read(
        pattern,
        params,
        { ...(query as object | undefined), ...offset } as QueryParams<Op<P, "get">>,
        init
      );
      first ??= page;
      const { items: rows, limit, total } = page as ListPage<ItemOf<P>>;
      items.push(...(rows ?? []));
      const short = rows === null || rows.length === 0 || rows.length < limit;
      if (short || (total !== null && items.length >= total)) {
        return { ...(first as object), items } as WholeList<P>;
      }
    }
  }

  private async post<P extends PathsOf<"post">>(
    pattern: P,
    params: PathParams<Op<P, "post">>,
    body: RequestBody<Op<P, "post">> | FormBody<Op<P, "post">>,
    bounds: WriteBounds
  ): Promise<Answer<Op<P, "post">>> {
    return this.send<Answer<Op<P, "post">>>(writeRequest("post", pattern, params, body), bounds);
  }

  private async put<P extends PathsOf<"put">>(
    pattern: P,
    params: PathParams<Op<P, "put">>,
    body: RequestBody<Op<P, "put">>,
    bounds: WriteBounds
  ): Promise<Answer<Op<P, "put">>> {
    return this.send<Answer<Op<P, "put">>>(writeRequest("put", pattern, params, body), bounds);
  }

  private async patch<P extends PathsOf<"patch">>(
    pattern: P,
    params: PathParams<Op<P, "patch">>,
    body: RequestBody<Op<P, "patch">>,
    bounds: WriteBounds
  ): Promise<Answer<Op<P, "patch">>> {
    return this.send<Answer<Op<P, "patch">>>(writeRequest("patch", pattern, params, body), bounds);
  }

  private async remove<P extends PathsOf<"delete">>(
    pattern: P,
    params: PathParams<Op<P, "delete">>,
    bounds: WriteBounds
  ): Promise<Answer<Op<P, "delete">>> {
    return this.send<Answer<Op<P, "delete">>>(writeRequest("delete", pattern, params), bounds);
  }

  async listProjects() {
    return (await this.readWholeList("/api/v2/projects", {})).items;
  }

  /** Answers `CreatedProject`: the project as created, with the default assignee the create
   *  stored, which the project list leaves out (`listProjectDefaultAssignees` is the one read of
   *  every project's default). */
  createProject(input: Body<"/api/v2/projects", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/projects", {}, input, bounds);
  }

  /** Every repository mapped to a project (`GET /api/v2/repositories`). */
  async listRepoProjects() {
    return (await this.readWholeList("/api/v2/repositories", {})).items;
  }

  async putRepoProject(
    repo: string,
    input: Body<"/api/v2/repositories/{owner}/{repo}", "put">,
    bounds: WriteBounds
  ) {
    return this.put("/api/v2/repositories/{owner}/{repo}", repoParams(repo), input, bounds);
  }

  async deleteRepoProject(repo: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/repositories/{owner}/{repo}", repoParams(repo), bounds);
  }

  /** Every project with the person it defaults to (`GET /api/v2/projects?include=
   *  default_assignee`): each one names somebody, so none is left out. Needs the `person`
   *  scope. */
  async listProjectDefaultAssignees() {
    return (await this.readWholeList("/api/v2/projects", {}, { include: ["default_assignee"] }))
      .items;
  }

  /** Moves the person a project's unowned work lands on, and the person a question on an issue
   *  nobody holds reaches (`PATCH /api/v2/projects/{key}`, `default_assignee`), answering the
   *  project with it. A project always names somebody: a blank assignee is refused
   *  `400 INVALID_PROJECT`, so nothing clears a default. Needs the `person` scope, and a login
   *  nobody has signed in as is refused `400 ASSIGNEE_NOT_ALLOWED`. */
  putProjectDefaultAssignee(project: string, assignee: string, bounds: WriteBounds) {
    return this.patch(
      "/api/v2/projects/{key}",
      { key: project },
      { default_assignee: assignee },
      bounds
    );
  }

  /** Every project's architecture sources, by project and then in the order each was added:
   *  `/api/v2` lists them one project at a time, so this reads the projects, then each one's
   *  list. */
  async listArchitectureSources(): Promise<ArchitectureSource[]> {
    const projects = await this.listProjects();
    const lists = await Promise.all(
      projects.map(({ key }) => this.listProjectArchitectureSources(key))
    );
    return lists.flat();
  }

  /** The project's sources, one per repository, in the order they were added; `[]` when it has
   *  none: having none is the common answer, not a failure. */
  async listProjectArchitectureSources(project: string) {
    return (
      await this.readWholeList("/api/v2/projects/{key}/architecture-sources", { key: project })
    ).items;
  }

  /** Adds `repo` (`owner/name`) as a source of the project, or changes the branch of the source
   *  it already is. */
  async putArchitectureSource(
    project: string,
    repo: string,
    input: Body<"/api/v2/projects/{key}/architecture-sources/{owner}/{repo}", "put">,
    bounds: WriteBounds
  ) {
    return this.put(
      "/api/v2/projects/{key}/architecture-sources/{owner}/{repo}",
      { key: project, ...repoParams(repo) },
      input,
      bounds
    );
  }

  /** Imports one source's model now (`POST /api/v2/projects/{key}/architecture-syncs` naming
   *  that one repository), answering the source as the sync left it: its `last_error` says
   *  whether the import failed, an access failure among them. */
  async syncArchitectureSource(project: string, repo: string, bounds: WriteBounds) {
    const synced = await this.post(
      "/api/v2/projects/{key}/architecture-syncs",
      { key: project },
      { repos: [repo] },
      bounds
    );
    return synced.items?.find((source) => source.repo === repo);
  }

  async deleteArchitectureSource(
    project: string,
    repo: string,
    bounds: WriteBounds
  ): Promise<void> {
    await this.remove(
      "/api/v2/projects/{key}/architecture-sources/{owner}/{repo}",
      { key: project, ...repoParams(repo) },
      bounds
    );
  }

  /** The project's component tree with the work attached; 404 SOURCE_NOT_FOUND without a source. */
  getArchitecture(project: string) {
    return this.read("/api/v2/projects/{key}/architecture", { key: project });
  }

  async listAgentTokens() {
    return (await this.readWholeList("/api/v2/me/agent-tokens", {})).items;
  }

  createAgentToken(input: Body<"/api/v2/me/agent-tokens", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/me/agent-tokens", {}, input, bounds);
  }

  /** `PATCH /api/v2/me/agent-tokens/{id}`: the whole set of scopes the token is to hold, which
   *  replaces the set it holds now, answering the updated token. A scope the caller does not hold
   *  itself is refused, as it is at the mint. */
  setAgentTokenScopes(
    id: string,
    scopes: NonNullable<Body<"/api/v2/me/agent-tokens/{id}", "patch">>["scopes"],
    bounds: WriteBounds
  ) {
    return this.patch("/api/v2/me/agent-tokens/{id}", { id }, { scopes }, bounds);
  }

  async revokeAgentToken(id: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/me/agent-tokens/{id}", { id }, bounds);
  }

  /** Everyone who has signed in, sorted by email: the assignee picker's options. */
  async listUsers() {
    return (await this.readWholeList("/api/v2/users", {})).items;
  }

  /** The issue list's rows, every page of them (`GET /api/v2/issues`): `project`, repeatable,
   *  keeps those projects' issues, and with none every project's. */
  async listIssues(query: Omit<Query<"/api/v2/issues", "get">, "limit" | "offset"> = {}) {
    return (await this.readWholeList("/api/v2/issues", {}, query)).items;
  }

  /** `GET /api/v2/search-results`. `project` (repeatable), `status`, `author` (an actor id: a
   *  person's login or a session id) and `from`/`to` (RFC 3339, on the row's update) each narrow
   *  every leg of the search rather than striking rows out of the page it answers.
   *  `number_project` narrows nothing: it is the project of the page the search was made from,
   *  which a query that is a bare issue number is read in, and a request naming `project` too
   *  reads the number in that project instead. `keywords_only` runs the keyword leg alone and
   *  makes no embedding call, whatever embedder the deployment configured, which is what the bar
   *  sends behind a keystroke; a request leaving it out searches by meaning too wherever an
   *  embedder is configured. The bar searches while a person types, so it passes the `signal` of
   *  the search it is still interested in: an aborted request ends the route's own work with it,
   *  the embedding a search by meaning had asked for included. */
  search(
    query: string,
    options: Omit<Query<"/api/v2/search-results", "get">, "q"> = {},
    init: { signal?: AbortSignal } = {}
  ) {
    return this.read("/api/v2/search-results", {}, { q: query, ...options }, init);
  }

  /**
   * `POST /api/v2/projects/{key}/related-searches`: the related-item check over a title, which
   * writes nothing. The create dialog asks while a person types, so a caller passes the `signal`
   * of the check it is still interested in and a superseded one is aborted rather than raced. It
   * asks for no other project's questions (`include=other_projects`), which the dialog does not
   * show. A computation is no write, so it goes outside the write path and under no key: a key
   * would have Dispatch keep the answer of every keystroke's check for a day.
   */
  relatedItems(project: string, title: string, options: { signal?: AbortSignal } = {}) {
    const body: Body<"/api/v2/projects/{key}/related-searches", "post"> = { title };
    return this.json<Answer<Op<"/api/v2/projects/{key}/related-searches", "post">>>(
      routeUrl("/api/v2/projects/{key}/related-searches" satisfies PathsOf<"post">, {
        key: project,
      }),
      {
        body: JSON.stringify(body),
        headers: { "Content-Type": "application/json" },
        method: "POST",
        signal: options.signal,
      },
      REQUEST_DEADLINE_MS
    );
  }

  createIssue(input: Body<"/api/v2/issues", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/issues", {}, input, bounds);
  }

  /** One issue with every detail the issue page draws: its open asks, its children, its
   *  progress, its design gates, where its route reaches (`reach`) and how many items cite it.
   *  The route answers each only as `include` asks for it, so this one read names the answer
   *  they make present (`IssueDetails`). */
  async getIssue(key: string): Promise<IssueDetails> {
    return (await this.read(
      "/api/v2/issues/{key}",
      { key },
      { include: [...ISSUE_DETAILS] }
    )) as IssueDetails;
  }

  /** What a reference to the issue or one of its documents shows: title, status and each
   *  document's name, kind and latest version (`artifacts`), the issue's own read with nothing
   *  included. */
  getIssueSummary(key: string) {
    return this.read("/api/v2/issues/{key}", { key });
  }

  async patchIssue(key: string, input: Body<"/api/v2/issues/{key}", "patch">, bounds: WriteBounds) {
    return this.send(issuePatchRequest(key, input), bounds);
  }

  /** `DELETE /api/v2/issues/{key}/claim`: release the claim. A human may release any claim;
   *  the issue's status does not move. */
  releaseIssueClaim(key: string, bounds: WriteBounds) {
    return this.remove("/api/v2/issues/{key}/claim", { key }, bounds);
  }

  /** `GET /api/v2/issues/{key}/events`: a page of the issue's event log. The document types every
   *  event's `payload` as `unknown`, because Go carries it as the bytes it was written as and no
   *  OpenAPI schema can say which payload belongs to which `type`; `DispatchEvent` is that union
   *  over the same bytes, so this one page's rows are named here rather than read. */
  async getIssueEvents(
    key: string,
    query: Query<"/api/v2/issues/{key}/events", "get"> = {}
  ): Promise<DispatchEvent[]> {
    const page = await this.json<
      Omit<Json200<Op<"/api/v2/issues/{key}/events", "get">>, "items"> & {
        items: DispatchEvent[] | null;
      }
    >(routeUrl("/api/v2/issues/{key}/events" satisfies PathsOf<"get">, { key }, query));
    return page.items ?? [];
  }

  /** `GET /api/v2/issues/{key}/checks`, every page: the checks of the issue's Verification
   *  document in document order, each with the commits and branches its results are compared
   *  with, whether a pass of it stands, and every result recorded against it from any issue,
   *  newest first, each with where its commit stands as the server judged it against the
   *  repository. */
  getIssueResults(key: string) {
    return this.readWholeList("/api/v2/issues/{key}/checks", { key });
  }

  /** `GET /api/v2/issues/{key}/coverage`: what each requirement of the issue's spec lacks. The
   *  server decides the gaps - a requirement with no plan task is no gap until the issue has a
   *  plan document - so a reader draws its answer rather than judging again. */
  getIssueCoverage(key: string) {
    return this.read("/api/v2/issues/{key}/coverage", { key });
  }

  /** `GET /api/v2/priorities`, every page: every live priority in rank order, each with the work
   *  that advances it, and the two faults the page draws beside them - the open work under no
   *  priority and the work parked under one, each with its issues (`include=fault_issues`). A
   *  priority belongs to no project, so this reads the whole deployment; `projects` counts only
   *  the work of those projects. */
  listPriorities(projects: readonly string[] = []) {
    return this.readWholeList(
      "/api/v2/priorities",
      {},
      { include: ["fault_issues"], ...(projects.length ? { project: [...projects] } : {}) }
    );
  }

  /** The two fault counts alone (`GET /api/v2/priorities?limit=0`): the work under no priority
   *  and the work parked under one, with no row and no issue list. */
  getPrioritySummary(projects: readonly string[] = []) {
    return this.read(
      "/api/v2/priorities",
      {},
      { limit: 0, ...(projects.length ? { project: [...projects] } : {}) }
    );
  }

  /** `POST /api/v2/priorities`: one new priority, which takes the last place of the order. Its
   *  id defaults to its title as a slug. */
  createPriority(input: Body<"/api/v2/priorities", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/priorities", {}, input, bounds);
  }

  /** `PATCH /api/v2/priorities/{id}`: the priority's title, description or owner, and its place
   *  in the one order (`rank: {before, after}`, the priorities it lands between, as the issue
   *  board's own drag names them). Each field left out is left alone. */
  patchPriority(id: string, input: Body<"/api/v2/priorities/{id}", "patch">, bounds: WriteBounds) {
    return this.patch("/api/v2/priorities/{id}", { id }, input, bounds);
  }

  /** `GET /api/v2/priorities/{id}/acts`, every page: the priority's own history, newest first -
   *  each write of its title, description, owner, order, state and visibility, and each
   *  `advances` relation an issue asserted or retracted on it, with the actor who stated it and
   *  when. A priority that was deleted still answers its history, the delete its newest act. */
  async getPriorityHistory(id: string) {
    return (await this.readWholeList("/api/v2/priorities/{id}/acts", { id })).items;
  }

  /** Every open ask the viewer can see, as the Inbox lists them (`GET /api/v2/asks`, every page):
   *  each with its thread and the viewer's snooze of it (`include=thread,mark`), `project`
   *  narrowing them to one project's. The route answers the thread only as `include` asks for
   *  it, so this read names the row it makes present (`InboxRow`). */
  async getInbox(project?: string): Promise<InboxRow[]> {
    const asks = await this.readWholeList(
      "/api/v2/asks",
      {},
      { include: ["thread", "mark"], ...(project === undefined ? {} : { project: [project] }) }
    );
    return asks.items as InboxRow[];
  }

  /** Folds this inbox row away for the caller until `snoozedUntil` (RFC 3339): the ask's mark
   *  (`askSnoozeRequest`). Needs the `person` scope, like the inbox itself. */
  async snoozeAsk(id: string, snoozedUntil: string, bounds: WriteBounds) {
    return this.send(askSnoozeRequest(id, snoozedUntil), bounds);
  }

  /** Brings the row back now: the ask's mark with no snooze. A row that is not snoozed answers
   *  the same mark. */
  async unsnoozeAsk(id: string, bounds: WriteBounds) {
    return this.send(askSnoozeRequest(id, null), bounds);
  }

  /** `PATCH /api/v2/asks/{id}` with `asked_of`: hand an open ask to `askedOf`, the email of
   *  somebody who has signed in, so the ask waits on them wherever its issue goes and their Inbox
   *  lists it. Naming the assignee of the ask's own issue empties the stored field, which hands
   *  the ask back to whoever holds that issue. Needs the `person` scope: handing a question on is
   *  one person asking another. */
  handAskTo(id: string, askedOf: string, bounds: WriteBounds) {
    return this.patch("/api/v2/asks/{id}", { id }, { asked_of: askedOf }, bounds);
  }

  async listAgents() {
    return (await this.readWholeList("/api/v2/agents", {})).items;
  }

  createAsk(key: string, input: Body<"/api/v2/issues/{key}/asks", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/issues/{key}/asks", { key }, input, bounds);
  }

  /** `GET /api/v2/issues/{key}/asks`, every page: the issue's asks with their newest reply and
   *  no thread, for a reader that renders the ask itself. */
  async listIssueAsks(key: string, state: "all" | "open" | "answered" = "all") {
    return (await this.readWholeList("/api/v2/issues/{key}/asks", { key }, { state })).items;
  }

  /** The same rows carrying each ask's thread (`include=thread`), so only a reader that draws the
   *  threads asks for them. The route answers the thread only as `include` asks for it, so this
   *  read names the row it makes present (`AskWithThread`). */
  async listIssueAskThreads(
    key: string,
    state: "all" | "open" | "answered" = "all"
  ): Promise<AskWithThread[]> {
    const asks = await this.readWholeList(
      "/api/v2/issues/{key}/asks",
      { key },
      { include: ["thread"], state }
    );
    return asks.items as AskWithThread[];
  }

  /** `PUT /api/v2/asks/{id}/state` to `answered`: the options picked and the text, under the
   *  times the answer and the question were read at. */
  answerAsk(id: string, input: AnswerAskInput, bounds: WriteBounds) {
    return this.put("/api/v2/asks/{id}/state", { id }, { ...input, state: "answered" }, bounds);
  }

  /** `PUT /api/v2/asks/{id}/state` to `declined`: close an open question with no answer, keeping
   *  the person's note as its reason. Needs the `person` scope, as an answer does; an approval
   *  request is refused `409 ASK_NOT_DECLINABLE`. */
  declineAsk(id: string, input: DeclineAskInput, bounds: WriteBounds) {
    return this.put("/api/v2/asks/{id}/state", { id }, { ...input, state: "declined" }, bounds);
  }

  /** One ask with its thread - replies, rewordings, answers, followers, claimant and route
   *  (`include=thread`). The route answers the thread only as `include` asks for it, so this
   *  read names the answer it makes present (`AskWithThread`). */
  async getAsk(id: string): Promise<AskWithThread> {
    return (await this.read("/api/v2/asks/{id}", { id }, { include: ["thread"] })) as AskWithThread;
  }

  /** `GET /api/v2/me/answers`: the signed-in person's answers and ask replies, newest first,
   *  one page at a time. Needs the `person` scope. */
  listMyAnswers(page: Query<"/api/v2/me/answers", "get">) {
    return this.read("/api/v2/me/answers", {}, page);
  }

  /** `GET /api/v2/me/threads`, one page: the threads the signed-in person is reading, newest
   *  write first. `kind` is `reply` alone by default, both kinds for the Inbox's own rows;
   *  `state` is `unread` (the default), `inbox` - every row of their Inbox, read or not - or
   *  `dismissed`, newest dismissal first, whose next page starts `before` the last row's
   *  `dismissed_at,thread_id`; `issue` or `artifact` narrows to one owner. Needs the `person`
   *  scope. */
  listMyThreads(page: Query<"/api/v2/me/threads", "get">) {
    return this.read("/api/v2/me/threads", {}, page);
  }

  /** Every row `listMyThreads` answers under `query`, every page of them, as of the first page's
   *  `as_of`. */
  listEveryMyThread(query: Omit<Query<"/api/v2/me/threads", "get">, "limit" | "offset">) {
    return this.readWholeList("/api/v2/me/threads", {}, query);
  }

  /** Moves the signed-in person's read mark on one thread forward to `readThrough` (RFC 3339,
   *  the newest reply shown), whether or not it moved. */
  async markThreadRead(threadId: string, readThrough: string, bounds: WriteBounds): Promise<void> {
    await this.send(threadReadRequest(threadId, readThrough), bounds);
  }

  async removeAskFollower(id: string, sessionId: string, bounds: WriteBounds): Promise<void> {
    await this.remove(
      "/api/v2/asks/{id}/followers/{session_id}",
      { id, session_id: sessionId },
      bounds
    );
  }

  async listComments(key: string, artifact?: string) {
    return (
      await this.readWholeList(
        "/api/v2/issues/{key}/comments",
        { key },
        artifact === undefined ? {} : { artifact }
      )
    ).items;
  }

  createComment(
    key: string,
    input: Body<"/api/v2/issues/{key}/comments", "post">,
    bounds: WriteBounds
  ) {
    return this.post("/api/v2/issues/{key}/comments", { key }, input, bounds);
  }

  /** One comment, its own fields at the top level, with its replies. */
  getComment(id: string) {
    return this.read("/api/v2/comments/{id}", { id });
  }

  /** `PUT /api/v2/comments/{id}/state` to `resolved`. */
  resolveComment(id: string, bounds: WriteBounds) {
    return this.put("/api/v2/comments/{id}/state", { id }, { state: "resolved" }, bounds);
  }

  /** `PUT /api/v2/comments/{id}/state` to `open`: a root comment resolved before is open again. */
  reopenComment(id: string, bounds: WriteBounds) {
    return this.put("/api/v2/comments/{id}/state", { id }, { state: "open" }, bounds);
  }

  editComment(id: string, input: Body<"/api/v2/comments/{id}", "patch">, bounds: WriteBounds) {
    return this.patch("/api/v2/comments/{id}", { id }, input, bounds);
  }

  /** The comment's current blocks, each with its span in the stored body, and its revision
   *  (`include=blocks`): what the editor carries block ids from (`editComment`'s `blocks` and
   *  `base_revision`). The route answers `blocks` only as `include` asks for it, so a read that
   *  asked and got none is a refusal of the route's own shape. */
  async commentBlocks(id: string) {
    const comment = await this.read("/api/v2/comments/{id}", { id }, { include: ["blocks"] });
    if (comment.blocks === undefined) throw new TypeError(`comment ${id} answered no blocks`);
    return comment.blocks;
  }

  /** `PUT /api/v2/comments/{id}/state` to `accepted`: the suggestion's replacement is applied. */
  acceptComment(id: string, bounds: WriteBounds) {
    return this.put("/api/v2/comments/{id}/state", { id }, { state: "accepted" }, bounds);
  }

  /** `PUT /api/v2/comments/{id}/state` to `rejected`. */
  rejectComment(id: string, bounds: WriteBounds) {
    return this.put("/api/v2/comments/{id}/state", { id }, { state: "rejected" }, bounds);
  }

  createCommentDelivery(
    id: string,
    target: string | undefined,
    delivery: "btw" | "aside" | "steer",
    bounds: WriteBounds
  ) {
    return this.post(
      "/api/v2/comments/{id}/deliveries",
      { id },
      { delivery, ...(target === undefined ? {} : { target }) },
      bounds
    );
  }

  createMessage(
    key: string,
    input: Body<"/api/v2/issues/{key}/messages", "post">,
    bounds: WriteBounds
  ) {
    return this.post("/api/v2/issues/{key}/messages", { key }, input, bounds);
  }

  /** A person's message to one session. */
  async createAgentMessage(
    sessionID: string,
    input: Body<"/api/v2/agents/{session_id}/messages", "post">,
    bounds: WriteBounds
  ) {
    return this.send(agentMessageRequest(sessionID, input), bounds);
  }

  /** Every conversation of one session, every page: the direct messages and issue messages sent
   *  to it, each with its replies. */
  async listAgentMessages(sessionID: string) {
    return (
      await this.readWholeList("/api/v2/agents/{session_id}/messages", { session_id: sessionID })
    ).items;
  }

  /** A person's message to many sessions. */
  createBroadcast(input: Body<"/api/v2/broadcasts", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/broadcasts", {}, input, bounds);
  }

  async listBroadcasts() {
    return (await this.readWholeList("/api/v2/broadcasts", {})).items;
  }

  getBroadcast(id: string) {
    return this.read("/api/v2/broadcasts/{id}", { id });
  }

  createMessageDelivery(id: string, delivery: "btw" | "aside" | "steer", bounds: WriteBounds) {
    return this.post("/api/v2/messages/{id}/deliveries", { id }, { delivery }, bounds);
  }

  /** One message, its own fields at the top level, with its thread's replies. */
  getMessage(id: string) {
    return this.read("/api/v2/messages/{id}", { id });
  }

  /** `GET /api/v2/issues/{key}/messages`, every page: every root message of the issue, oldest
   *  first, each with its deliveries and its replies. */
  async listIssueMessages(key: string) {
    return (await this.readWholeList("/api/v2/issues/{key}/messages", { key })).items;
  }

  /** `GET /api/v2/issues/{key}/handoffs`, every page: every phase handoff of the issue, newest
   *  first, without its body. */
  async listHandoffs(key: string) {
    return (await this.readWholeList("/api/v2/issues/{key}/handoffs", { key })).items;
  }

  /** `GET /api/v2/handoffs/{id}`: one handoff with the body its session wrote. */
  getHandoff(id: string) {
    return this.read("/api/v2/handoffs/{id}", { id });
  }

  /** An issue's documents, every page, each row with its newest version's time and size and its
   *  count of versions (`latest_version_at`, `latest_version_size`, `version_count`). */
  async listArtifacts(key: string) {
    return (await this.readWholeList("/api/v2/issues/{key}/artifacts", { key })).items;
  }

  /** A project's documents, every page, each row as `listArtifacts` reads one: `unlinked` keeps
   *  those of no issue (`issue=none`). */
  async listProjectArtifacts(key: string, unlinked?: boolean) {
    return (
      await this.readWholeList(
        "/api/v2/projects/{key}/artifacts",
        { key },
        unlinked === true ? { issue: "none" } : {}
      )
    ).items;
  }

  uploadArtifact(owner: ArtifactOwner, input: CreateArtifactInput, bounds: WriteBounds) {
    const body = artifactUploadBody(input);
    if ("issue" in owner) {
      return this.post("/api/v2/issues/{key}/artifacts", { key: owner.issue }, body, bounds);
    }
    if ("project" in owner) {
      return this.post("/api/v2/projects/{key}/artifacts", { key: owner.project }, body, bounds);
    }
    return this.post(
      "/api/v2/agents/{session_id}/artifacts",
      { session_id: owner.session },
      body,
      bounds
    );
  }

  /** Corrects a document's title. The write carries the name alone: the slug, the reference key
   *  and the role do not move, so every citation of the document holds. */
  renameArtifact(id: string, input: Body<"/api/v2/artifacts/{id}", "patch">, bounds: WriteBounds) {
    return this.patch("/api/v2/artifacts/{id}", { id }, input, bounds);
  }

  /** Removes a document of an issue that is still untouched. Dispatch refuses one that holds
   *  anything, in a sentence that names which test it failed. */
  async deleteArtifact(id: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/artifacts/{id}", { id }, bounds);
  }

  getArtifact(id: string) {
    return this.read("/api/v2/artifacts/{id}", { id });
  }

  /** Every version of a document (`GET /api/v2/artifacts/{id}/versions`, every page): a
   *  document's own read carries only its `latest_version`. */
  async listArtifactVersions(id: string) {
    return (await this.readWholeList("/api/v2/artifacts/{id}/versions", { id })).items;
  }

  /** The document of a project that `slug` names, linked to no issue (`issue=none`). The route
   *  lists the rows the slug names - none or one - so a slug that names none is refused here as
   *  the document's own read refuses an id that names none, `404 ARTIFACT_NOT_FOUND`. */
  async getProjectArtifact(key: string, slug: string) {
    const [document] =
      (await this.read("/api/v2/projects/{key}/artifacts", { key }, { issue: "none", slug }))
        .items ?? [];
    if (document === undefined) throw artifactNotFound(`${key}/${slug}`);
    return document;
  }

  /** An artifact of an agent's conversation, by its session and slug: the one row the session's
   *  list holds for it, or `404 ARTIFACT_NOT_FOUND` as `getProjectArtifact` answers. */
  async getAgentArtifact(sessionId: string, slug: string) {
    const [artifact] =
      (
        await this.read(
          "/api/v2/agents/{session_id}/artifacts",
          { session_id: sessionId },
          { slug }
        )
      ).items ?? [];
    if (artifact === undefined) throw artifactNotFound(`agent/${sessionId}/${slug}`);
    return artifact;
  }

  /** A document's live text: its markdown, the token a write of it goes under and the version
   *  it stands on, as members of the document's own read (`include=text`). The route answers the
   *  text only as `include` asks for it, so this read names the answer it makes present
   *  (`ArtifactText`). */
  async getArtifactText(id: string): Promise<ArtifactText> {
    return (await this.read(
      "/api/v2/artifacts/{id}",
      { id },
      { include: ["text"] }
    )) as ArtifactText;
  }

  rebuildArtifact(id: string, markdown: string | undefined, bounds: WriteBounds) {
    return this.post(
      "/api/v2/artifacts/{id}/rebuilds",
      { id },
      markdown === undefined ? {} : { markdown },
      bounds
    );
  }

  /** A document's live blocks (`include=blocks`). The route answers `blocks` only as `include`
   *  asks for it, so a read that asked and got none is a refusal of the route's own shape. */
  async getArtifactBlocks(id: string) {
    const artifact = await this.read("/api/v2/artifacts/{id}", { id }, { include: ["blocks"] });
    if (artifact.blocks === undefined) throw new TypeError(`document ${id} answered no blocks`);
    return artifact.blocks;
  }

  /** An issue's decision records, every page: its decisions.md (`document`), each decision with
   *  the spec blocks it shaped, and the open questions its spec holds (`include=
   *  open_questions`). */
  getIssueDecisions(key: string) {
    return this.readWholeList(
      "/api/v2/issues/{key}/decisions",
      { key },
      { include: ["open_questions"] }
    );
  }

  /** Record that the caller agrees with the decision the heading block `blockId` of the issue's
   *  decisions.md opens, as the text they read (`PUT .../decisions/{block_id}/state` to `kept`):
   *  `textSha256` is the decision's own `text_sha256`, and a decision that changed since is
   *  `409 DECISION_CHANGED`. */
  keepDecision(key: string, blockId: string, textSha256: string, bounds: WriteBounds) {
    return this.put(
      "/api/v2/issues/{key}/decisions/{block_id}/state",
      { block_id: blockId, key },
      { state: "kept", text_sha256: textSha256 },
      bounds
    );
  }

  /** The caller's mark of a document: the last version they read (`read_version`), absent when
   *  they have read none. */
  getDocumentRead(id: string) {
    return this.read("/api/v2/me/marks/{kind}/{id}", { id, kind: "document" });
  }

  /** Record the caller's last read version of a document: the Spec tab marks each block that
   *  changed since it. */
  async putDocumentRead(id: string, version: number, bounds: WriteBounds) {
    return this.send(documentReadRequest(id, version), bounds);
  }

  /** One version's bytes. A large file on a slow line can take longer than any read deadline, so
   *  this GET carries none. */
  async getArtifactVersion(id: string, version: number): Promise<ArtifactVersionContent> {
    const response = await this.response(artifactVersionPath(id, version), {}, null);
    const mime = response.headers.get("Content-Type") ?? "application/octet-stream";

    if (mime.includes("application/json")) {
      return (await response.json()) as ArtifactVersionText;
    }

    return { blob: await response.blob(), mime, version };
  }

  createArtifactVersion(
    id: string,
    input: Body<"/api/v2/artifacts/{id}/versions", "post">,
    bounds: WriteBounds
  ) {
    return this.post("/api/v2/artifacts/{id}/versions", { id }, input, bounds);
  }

  async listArtifactReviews(id: string) {
    return (await this.readWholeList("/api/v2/artifacts/{id}/reviews", { id })).items;
  }

  createArtifactReview(id: string, input: CreateArtifactReviewInput, bounds: WriteBounds) {
    return this.post("/api/v2/artifacts/{id}/reviews", { id }, input, bounds);
  }

  async listArtifactAsks(id: string, state: "all" | "open" | "answered" = "all") {
    return (await this.readWholeList("/api/v2/artifacts/{id}/asks", { id }, { state })).items;
  }

  createArtifactAsk(
    id: string,
    input: Body<"/api/v2/artifacts/{id}/asks", "post">,
    bounds: WriteBounds
  ) {
    return this.post("/api/v2/artifacts/{id}/asks", { id }, input, bounds);
  }

  async listArtifactComments(id: string) {
    return (await this.readWholeList("/api/v2/artifacts/{id}/comments", { id })).items;
  }

  createArtifactComment(
    id: string,
    input: Body<"/api/v2/artifacts/{id}/comments", "post">,
    bounds: WriteBounds
  ) {
    return this.post("/api/v2/artifacts/{id}/comments", { id }, input, bounds);
  }

  async getArtifactSubscribers(id: string) {
    return (await this.readWholeList("/api/v2/artifacts/{id}/subscribers", { id })).items;
  }

  async unsubscribeArtifactSession(
    id: string,
    sessionId: string,
    bounds: WriteBounds
  ): Promise<void> {
    await this.remove(
      "/api/v2/artifacts/{id}/subscribers/{session_id}",
      { id, session_id: sessionId },
      bounds
    );
  }

  /** The issue's reference closure, every page: each member and whether the closure was cut
   *  short (`truncated`). */
  getIssueReferences(key: string) {
    return this.readWholeList("/api/v2/issues/{key}/references", { key });
  }

  /** Everyone who worked the issue, running or not, every page: people (`kind: "user"`) and
   *  agent sessions (`kind: "session"`) merged with the listener's Legion agent registry -
   *  `legion_registry: "unavailable"` when it did not answer. */
  getIssueCollaborators(key: string) {
    return this.readWholeList("/api/v2/issues/{key}/collaborators", { key });
  }

  /** The edges at a node, every page, with the node beside them; `kind` keeps those of one kind
   *  - `part_of` lists a document's blocks, which a document's read otherwise leaves out. */
  getReferences(reference: string, options: { kind?: string } = {}) {
    return this.readWholeList(
      "/api/v2/references",
      {},
      { to: reference, ...(options.kind === undefined ? {} : { kind: [options.kind] }) }
    );
  }

  /** The question map of the document one reference source's text belongs to
   *  (`GET /api/v2/asks?source=<kind>:<id>`, every page): one ask row for every question block
   *  that document's latest version declares, with the ask settled for it (for a copied block,
   *  the ask it was copied from), the document's id beside them. `kind` is the source's own kind
   *  - artifact, ask, comment or message - and `id` its id. A source whose text belongs to no
   *  document is 404. */
  getReferenceQuestions(kind: ReferenceSourceKind, id: string) {
    return this.readWholeList("/api/v2/asks", {}, { source: `${kind}:${id}` });
  }

  /** One deploy-repository run with every job it ran, for the drill-down's job lists. */
  getDeliveryRun(id: number) {
    return this.read("/api/v2/deploy-runs/{id}", { id: String(id) });
  }

  /** The delivery timeline's configuration record, or `null` until someone sets it. */
  getDeliverySettings() {
    return this.read("/api/v2/settings/delivery", {});
  }

  /** Replaces the delivery timeline's configuration (human callers only). The server first proves
   *  the GitHub App can read `deploy_repo`, answering `409 DELIVERY_SETTINGS_ACCESS` when not. */
  putDeliverySettings(input: Body<"/api/v2/settings/delivery", "put">, bounds: WriteBounds) {
    return this.put("/api/v2/settings/delivery", {}, input, bounds);
  }

  /** Creates a Delivery session group: its sessions' pull requests take it as their Parent agent.
   *  A session another group holds is `409 SESSION_IN_GROUP`; a name another group has, or that is
   *  the title of a session outside the group, is `409` as well, the message naming which. */
  createDeliverySessionGroup(input: Body<"/api/v2/session-groups", "post">, bounds: WriteBounds) {
    return this.post("/api/v2/session-groups", {}, input, bounds);
  }

  /** Renames a session group, adds sessions to it or removes sessions from it. */
  updateDeliverySessionGroup(
    id: string,
    input: Body<"/api/v2/session-groups/{id}", "patch">,
    bounds: WriteBounds
  ) {
    return this.patch("/api/v2/session-groups/{id}", { id }, input, bounds);
  }

  /** Deletes a session group; its sessions show as themselves again. */
  async deleteDeliverySessionGroup(id: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/session-groups/{id}", { id }, bounds);
  }

  async getIssueSubscribers(key: string) {
    return (await this.readWholeList("/api/v2/issues/{key}/subscribers", { key })).items;
  }

  async unsubscribeIssueSession(
    key: string,
    sessionId: string,
    bounds: WriteBounds
  ): Promise<void> {
    await this.remove(
      "/api/v2/issues/{key}/subscribers/{session_id}",
      { key, session_id: sessionId },
      bounds
    );
  }

  /** The person's marks of every issue they have written one on (`GET /api/v2/me/marks?kind=
   *  issue`, every page), by issue key: the page reads one issue's state by its key. */
  async getMyState(): Promise<Marks> {
    return marksById(await this.readWholeList("/api/v2/me/marks", {}, { kind: "issue" }));
  }

  async putIssueState(key: string, input: IssueMarkV2Input, bounds: WriteBounds) {
    return this.send(issueStateRequest(key, input), bounds);
  }

  /** The person's marks of every session they have cleared, read, pinned or hold an unread reply
   *  from (`GET /api/v2/me/marks?kind=agent`, every page), by session id. */
  async getMyAgentState(): Promise<Marks> {
    return marksById(await this.readWholeList("/api/v2/me/marks", {}, { kind: "agent" }));
  }

  async putAgentState(sessionID: string, input: AgentMarkV2Input, bounds: WriteBounds) {
    return this.send(agentStateRequest(sessionID, input), bounds);
  }

  /** The signed-in person. `/auth/*` sits outside the `/api/v2` route table the document is
   *  rendered from, so this one read names its path and its answer itself. */
  whoAmI(): Promise<AuthenticatedUser> {
    return this.json<AuthenticatedUser>("/auth/whoami");
  }

  /** Ends this browser's session. `/auth/logout` sits outside Dispatch's keyed layer and is the
   *  same however often it is made, so it goes with no key. */
  logout(): Promise<{ ok: true }> {
    return this.json<{ ok: true }>("/auth/logout", { method: "POST" });
  }

  /** A read through Dispatch's GitHub proxy; it makes no write. The proxy carries any GitHub path
   *  through, so the route table holds no route for what it answers. */
  githubRest(path: string, init: { readonly signal?: AbortSignal } = {}): Promise<Response> {
    return this.response(`/api/github/rest/${path.replace(/^\/+/, "")}`, init);
  }

  /** `GET /api/v2/credential-requests`, every page: every request the viewer may decide - the
   *  ones naming the viewer, and the ones any signed-in person may decide - as the Inbox's
   *  credential-requests section lists them, each secret request with the secrets broker's page
   *  where it is decided. `null` is a Dispatch with no secrets broker, which refuses the read
   *  `404 FEATURE_OFF` as it refuses every credential route: no broker is an ordinary answer for
   *  the list every page reads, not a failure. */
  async getCredentialPending(): Promise<CredentialPendingRow[] | null> {
    try {
      return (await this.readWholeList("/api/v2/credential-requests", {})).items;
    } catch (error) {
      if (isCredentialFeatureOff(error)) return null;
      throw error;
    }
  }

  /** Approve a machine login as the signed-in viewer with the code the viewer typed
   *  (`PUT /api/v2/credential-requests/{id}/state` to `approved`): Dispatch names the viewer's
   *  own login as the approver, and the broker decides whether that login may. A secret request
   *  is decided only on the secrets broker's own page, so the broker refuses it here with 403
   *  `DECIDE_ON_BROKER_PAGE`. */
  approveCredentialRecord(recordId: string, code: string | undefined, bounds: WriteBounds) {
    return this.put(
      "/api/v2/credential-requests/{id}/state",
      { id: recordId },
      { state: "approved", ...(code === undefined ? {} : { code }) },
      bounds
    );
  }

  /** Deny a machine login as the signed-in viewer with the code the viewer typed. */
  denyCredentialRecord(recordId: string, code: string | undefined, bounds: WriteBounds) {
    return this.put(
      "/api/v2/credential-requests/{id}/state",
      { id: recordId },
      { state: "denied", ...(code === undefined ? {} : { code }) },
      bounds
    );
  }

  /** A machine (`launcher_credential`) record is only ever selected through this route: the code
   *  the operator types in is what proves which pending login they mean, and deciding it takes
   *  the same code again. */
  lookupMachineCredential(code: string, bounds: WriteBounds) {
    return this.post("/api/v2/machine-login-lookups", {}, { code }, bounds);
  }

  /** `GET /api/v2/credential-grants`, every page: every live grant of the viewer's sessions,
   *  automatic or approved, and every grant the viewer approved. */
  async getCredentialGrants() {
    return (await this.readWholeList("/api/v2/credential-grants", {})).items;
  }

  /** Revoke a grant as the signed-in viewer, its approver or its enrollment's operator. The
   *  operator's revoke of an automatic grant also ends that session's other automatic grants of
   *  those secrets and makes it ask before it gets them again. */
  async revokeCredentialGrant(grantId: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/credential-grants/{id}", { id: grantId }, bounds);
  }

  /** `GET /api/v2/machine-logins`, every page: the machine logins the viewer may revoke, their
   *  own machines' and every service's, whoever approved it, that are unexpired or expired with
   *  a session still running. */
  async getMachineLogins() {
    return (await this.readWholeList("/api/v2/machine-logins", {})).items;
  }

  /** Revoke a machine login, expired or not: a service's, or one of the viewer's machines'.
   *  Dispatch names the viewer as the person revoking, and every session the login enrolled ends
   *  with it. */
  async revokeMachineLogin(credentialId: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/machine-logins/{id}", { id: credentialId }, bounds);
  }

  /** `GET /api/v2/me/signins`, every page: the viewer's own sign-ins on their machines
   *  (`agent-secrets login`), one per box key they have not revoked. Dispatch names the viewer,
   *  so nobody reads another person's. */
  async getSignIns() {
    return (await this.readWholeList("/api/v2/me/signins", {})).items;
  }

  /** End one of the viewer's own sign-ins: that machine trades no token for them again and the
   *  access tokens already minted under it end with it, while their sign-ins on other machines
   *  keep working. */
  async revokeSignIn(signInId: string, bounds: WriteBounds): Promise<void> {
    await this.remove("/api/v2/me/signins/{id}", { id: signInId }, bounds);
  }

  /** `GET /api/v2/settings/credential-broker`: what the credential pages may offer against this
   *  deployment's secrets broker, which Dispatch relays from the broker's own RFC 8414 metadata
   *  because a browser reaches no broker. */
  getCredentialCapabilities() {
    return this.read("/api/v2/settings/credential-broker", {});
  }

  /** `GET /api/v2/settings/new-issue`: the label an issue the bar files from a person's typed
   *  words carries and the session it is routed to, as the deployment configured them; each
   *  `""` where it configured none. */
  getNewIssueSettings() {
    return this.read("/api/v2/settings/new-issue", {});
  }
}

/** What the issue page reads of an issue beyond its own fields (`getIssue`). */
const ISSUE_DETAILS = [
  "open_asks",
  "children",
  "progress",
  "design_gates",
  "reach",
  "referenced_by_count",
] as const satisfies readonly NonNullable<
  Query<"/api/v2/issues/{key}", "get">["include"]
>[number][];

/** The person's marks of one kind, a list's rows, by the id each names. */
function marksById(marks: { readonly items: readonly MarkV2[] }): Marks {
  return Object.fromEntries(marks.items.map((mark) => [mark.id, mark]));
}

/** The refusal a slug read gives for a slug that names no document: what the document's own read
 *  answers for an id that names none. */
function artifactNotFound(name: string): ApiError {
  return new ApiError(404, { code: "ARTIFACT_NOT_FOUND", error: `artifact ${name} not found` });
}

/** A client over `fetchImpl`. The page's own (`api`) sends its uploads through `XMLHttpRequest`
 *  (`xhrUpload`), so their progress counts against the silence deadline; one given a `fetchImpl`
 *  of its own sends them through it unless it is given an `upload` too. */
export function createApiClient(
  fetchImpl?: FetchImplementation,
  upload?: UploadImplementation,
  wait: Sleep = sleep
): DispatchApiClient {
  return fetchImpl === undefined
    ? new DispatchApiClient((...args) => fetch(...args), upload ?? xhrUpload(), wait)
    : new DispatchApiClient(fetchImpl, upload, wait);
}

export const api = createApiClient();
