// The pull request's review threads as GitHub holds them during a run, and the merge state they
// leave the pull request in: the world's own threads (rig.sh's fixture_write writes them into the
// world's GraphQL graph), then the effect of every call the run's record holds, replayed in the
// order the run made them. GitHub keeps what a run did, so a thread the run opened is listed, a
// resolution it made reads `isResolved: true`, a reply it posted is its thread's last comment, and
// a base that requires conversation resolution holds the pull request `BLOCKED` until the last
// thread is resolved. The gh stand-in answers every read and write of a thread from this state
// (gh-standin.ts), and the daemon stand-in moves the record on the merge state it leaves
// (daemon-standin.ts's `roundOutcome`), so the world an agent was answered from and the world the
// record moves on are one world, read by one replay. A module of its own, as gh-call.ts is: both
// stand-ins import it, and the gh stand-in is a program that answers a call when it is loaded.
import { readFileSync } from "node:fs";
import {
  endpointPath,
  FIELD_FLAGS,
  FILE_FIELD_FLAGS,
  flagValue,
  isPost,
  mergeStateOn,
  type Route,
  reviewsRoute,
  routeFor,
  sentBody,
  submitsReview,
  valueAt,
  type World,
} from "./gh-call";
import { argumentsOf, isObject, type Operation, parseGraphql } from "./gh-routes";

/** A JSON object of the world's, as GitHub's schema names it: a review thread, a review comment,
 * the answer of a call. */
export type Json = Record<string, unknown>;

/** One call as the gh stand-in records it (its `exit`): the call it is answering now and each call
 * the run's record holds are read by the same readers (`graphqlOf`, `threadCall`), so what a call
 * did to the world is what it is answered with and what every later read of the record finds.
 * `handler` is what answered it (gh-standin.ts's `handler`), which a record from before the
 * stand-in kept it does not hold. */
export interface Recorded {
  readonly at?: string;
  readonly as?: string;
  readonly argv: readonly string[];
  readonly files?: Record<string, string> | undefined;
  readonly stdin?: string | undefined;
  readonly route?: string | null;
  readonly review?: { state: string; head: string } | undefined;
  readonly handler?: string;
  readonly exit?: number;
}

/** Every call of the record at `record`, oldest first: the gh stand-in's own `<run>/calls.jsonl`,
 * whose first line is the opening line that stand-in wrote before any call of the run was
 * answered and which is no call of an agent's, so a line with no `argv` is passed over. */
export function recordedCalls(record: string): Recorded[] {
  return readFileSync(record, "utf8")
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line) as Partial<Recorded>)
    .flatMap((call) => (Array.isArray(call.argv) ? [{ ...call, argv: call.argv }] : []));
}

/** The GraphQL operation a `gh api graphql` call sends, and its variables: the query its `query`
 * field or its `--input` body holds, read by the parser the sweep of the role texts reads them
 * with (gh-routes.ts), and every other field as a variable — a `-F` typed as gh types one (`true`,
 * `false`, `null`, an integer, the text of an `@file`), a `-f` as written — or an `--input`'s own
 * `variables`. `operation` is undefined for a query that is no GraphQL document. */
export function graphqlOf(call: Recorded): {
  operation: Operation | undefined;
  variables: Record<string, unknown>;
} {
  const { argv } = call;
  const sources = call.files ?? {};
  if (flagValue(argv, ["--input"]) !== undefined) {
    const sent = sentBody(call) ?? {};
    return {
      operation: typeof sent.query === "string" ? parseGraphql(sent.query) : undefined,
      variables: isObject(sent.variables) ? sent.variables : {},
    };
  }
  let query = "";
  const variables: Record<string, unknown> = {};
  for (const index of argv.keys()) {
    const pair = valueAt(argv, index, FIELD_FLAGS);
    if (pair === undefined) continue;
    const equals = pair.indexOf("=");
    const name = equals < 0 ? pair : pair.slice(0, equals);
    const text = equals < 0 ? "" : pair.slice(equals + 1);
    let value: unknown = text;
    if (valueAt(argv, index, FILE_FIELD_FLAGS) !== undefined) {
      if (text.startsWith("@")) value = sources[text.slice(1)] ?? "";
      else if (text === "true" || text === "false") value = text === "true";
      else if (text === "null") value = null;
      else if (/^-?\d+$/.test(text)) value = Number(text);
    }
    if (name === "query") query = String(value);
    else variables[name] = value;
  }
  return { operation: parseGraphql(query), variables };
}

/** GitHub's 404, for a read of something the world has not got. */
export const NOT_FOUND = "gh: Not Found (HTTP 404)\n";
/** The pages of GitHub's REST reference that name the two thread writes whose 404 the world
 * answers for a comment it has not got, which GitHub's 404 body names as its
 * `documentation_url` (gh-standin.ts's `notFoundBody`). */
const REPLY_DOCS =
  "https://docs.github.com/rest/pulls/comments#create-a-reply-for-a-review-comment";
const COMMENT_DOCS =
  "https://docs.github.com/rest/pulls/comments#create-a-review-comment-for-a-pull-request";
/** The create of a review comment, or of a reply, with no body, and the create of a comment that
 * opens a thread naming no `commit_id`, no `path`, or no `line` where it is no comment on the
 * whole file (`subject_type: file`) and names no `position`: the fields GitHub's "Create a review
 * comment for a pull request" requires. GitHub answers both 422; each sentence is the stand-in's
 * own, as its other 422 sentences are, since GitHub's exact wording is not measured here. */
const COMMENT_BODY =
  "gh: Unprocessable Entity (HTTP 422)\nA review comment is created with a body.\n";
const COMMENT_FIELDS =
  "gh: Unprocessable Entity (HTTP 422)\nA review comment that opens a thread names its commit_id, its path and its line.\n";

/** One route of a world's `fixtures.json`, as far as the threads read it: a route's answer, and
 * the graph the world's GraphQL route holds. */
export type ThreadRoute = Route & { readonly graph?: unknown };
/** What the threads are read against: the world's routes and its world.json, undefined for a run
 * that builds no world, whose calls write no thread. */
export interface ThreadWorld {
  readonly routes: readonly ThreadRoute[];
  readonly world: World | undefined;
}
/** The login the run's token acts as: the world's answer to `gh api user`. */
export function worldLogin(routes: readonly ThreadRoute[]): string {
  const user = routeFor(routes, ["api", "user"])?.stdout;
  return isObject(user) && typeof user.login === "string" ? user.login : "";
}
/** The pull request as the world's `gh pr view` route holds it, or an empty object. */
export function heldPullRequest(routes: readonly ThreadRoute[]): Json {
  const held = routeFor(routes, ["pr", "view"])?.stdout;
  return isObject(held) ? held : {};
}
/** The object the route a recorded call was matched to answers with, which the record names
 * (`route`, the route's own regex), or undefined when it answered no object. */
export function routeAnswer(
  routes: readonly ThreadRoute[],
  match: string | null | undefined
): Json | undefined {
  if (match === null || match === undefined) return undefined;
  const stdout = routes.find((candidate) => candidate.match === match)?.stdout;
  return isObject(stdout) ? stdout : undefined;
}

/** The node ids GitHub gives the review threads, the review comments and the reviews the run
 * makes: each type's own prefix, then a tail that names this repository, then the comment's (or
 * the review's) databaseId. */
const THREAD_NODE = "PRRT_kwDOAAAAAc4AAD";
const COMMENT_NODE = "PRRC_kwDOAAAAAc4AAC";
export const REVIEW_NODE = "PRR_kwDOAAAAAc4AAE";
/** The databaseId of the first review comment the run makes, where the world seeds none above
 * it: rig.sh's fixture_write numbers the comments of the world's own threads from 7001. */
const FIRST_COMMENT = 9001;
/** The pull request's review threads as they stand (`threadState`), each in the shape GitHub's
 * GraphQL answers a `PullRequestReviewThread` in, with its comments as `PullRequestReviewComment`s,
 * and the databaseId the next comment the run makes is given. */
export interface Threads {
  readonly list: Json[];
  next: number;
}
/** A number GitHub reads out of a field: a JSON number, or the digits a `-f` field sends as text,
 * which gh sends as a string and GitHub reads as the number it names. */
function numberOf(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isInteger(value)) return value;
  return typeof value === "string" && /^\d+$/.test(value) ? Number(value) : undefined;
}
/** The comments of `thread`, oldest first. */
export function commentsOf(thread: Json): Json[] {
  return Array.isArray(thread.comments) ? thread.comments.filter(isObject) : [];
}
/** The commit a review comment of the world stands on. */
function commitOf(comment: Json): string {
  return isObject(comment.commit) ? String(comment.commit.oid ?? "") : "";
}
/** The world's own review threads, as its graph holds them (rig.sh's fixture_write: each in
 * GitHub's GraphQL shape, each comment carrying its databaseId, its REST comment's id), copied,
 * so the calls replayed over them change no route of the world. */
function seededThreads(routes: readonly ThreadRoute[]): Json[] {
  const graph = routes.find((one) => one.graph !== undefined)?.graph;
  const query = isObject(graph) && isObject(graph.query) ? graph.query : {};
  const repository = isObject(query.repository) ? query.repository : {};
  const pullRequest = isObject(repository.pullRequest) ? repository.pullRequest : {};
  const threads = pullRequest.reviewThreads;
  return Array.isArray(threads) ? structuredClone(threads.filter(isObject)) : [];
}
/** The pull request's review threads as GitHub holds them after `calls`: the world's own
 * (`seededThreads`), then the effect of each call (`threadCall`), in the order the run made them.
 * Measured in round 24 of INFRA-764: with the threads the fixture froze, every reviewer that
 * resolved a thread or opened its own read the listing back unchanged and wrote that the world
 * was a mock (reviewer-text-findings-text-only-head-1, -4 and -5 among 29 of 36 runs whose agents
 * named the test). Each call is replayed by the reader that answered it, so what a call was
 * answered with and what a later read finds are one state. */
export function threadState(at: ThreadWorld, calls: readonly Recorded[]): Threads {
  const list = seededThreads(at.routes);
  const seeded = list.flatMap(commentsOf).map((comment) => numberOf(comment.databaseId) ?? 0);
  const state: Threads = { list, next: Math.max(FIRST_COMMENT - 1, ...seeded) + 1 };
  for (const call of calls) threadCall(at, state, call);
  return state;
}
/** A review comment the run makes, as GitHub's GraphQL answers one: its two ids, its author (the
 * login the run's token acts as), where it stands, when it was made, the review it belongs to,
 * the comment it replies to and the commit it is on. A comment of a pending review is pending
 * until that review is submitted. */
function newComment(
  at: ThreadWorld,
  state: Threads,
  made: {
    body: string;
    path: unknown;
    line: number | null;
    commit: string;
    review: number | undefined;
    replyTo: Json | undefined;
    at: string;
    pending: boolean;
  }
): Json {
  const databaseId = state.next++;
  const url = String(heldPullRequest(at.routes).url ?? "");
  return {
    __typename: "PullRequestReviewComment",
    id: `${COMMENT_NODE}${databaseId}`,
    databaseId,
    fullDatabaseId: String(databaseId),
    body: made.body,
    author: { login: worldLogin(at.routes) },
    authorAssociation: "NONE",
    path: made.path,
    line: made.line,
    originalLine: made.line,
    url: `${url}#discussion_r${databaseId}`,
    createdAt: made.at,
    updatedAt: made.at,
    publishedAt: made.at,
    state: made.pending ? "PENDING" : "SUBMITTED",
    outdated: false,
    isMinimized: false,
    replyTo:
      made.replyTo === undefined
        ? null
        : { id: made.replyTo.id, databaseId: made.replyTo.databaseId },
    commit: { oid: made.commit },
    originalCommit: { oid: made.commit },
    pullRequestReview:
      made.review === undefined
        ? null
        : { id: `${REVIEW_NODE}${made.review}`, databaseId: made.review },
  };
}
/** The thread `comment` opens, unresolved, on the line and the side it was made on. */
function newThread(comment: Json, side: unknown, startLine: number | null): Json {
  return {
    __typename: "PullRequestReviewThread",
    id: `${THREAD_NODE}${String(comment.databaseId)}`,
    isResolved: false,
    isOutdated: false,
    isCollapsed: false,
    path: comment.path,
    line: comment.line,
    originalLine: comment.line,
    startLine,
    originalStartLine: startLine,
    diffSide: typeof side === "string" ? side : "RIGHT",
    startDiffSide: null,
    subjectType: comment.line === null ? "FILE" : "LINE",
    resolvedBy: null,
    viewerCanResolve: true,
    viewerCanUnresolve: false,
    viewerCanReply: true,
    comments: [comment],
  };
}
/** The reply `body` the run posts in `thread`: the thread's last comment from now on, on the line
 * and the commit the thread stands on, replying to the thread's first comment whichever comment
 * of the thread the call named. GitHub files a reply to a reply in the reply's own thread and
 * names the thread's first comment as the one it replies to (`in_reply_to_id`, GraphQL's
 * `replyTo`): measured on 2026-11-17 at 17:28Z on cybermonklabs/legion#2827, `POST …/pulls/2827/
 * comments/4505026306/replies`, 4505026306 itself a reply to 4504802154, answered exit 0 with a
 * comment whose `in_reply_to_id` was 4504802154. The docs' "Replies to replies are not supported"
 * is no refusal GitHub gives, and a world that refused one gave the agent a refusal production
 * does not. The `in_reply_to` of `POST …/pulls/<n>/comments` is read the same way, which is not
 * measured. */
function replyIn(at: ThreadWorld, state: Threads, thread: Json, body: string, made: string): Json {
  const first = commentsOf(thread)[0];
  const comment = newComment(at, state, {
    body,
    path: thread.path,
    line: numberOf(thread.line) ?? null,
    commit: first === undefined ? "" : commitOf(first),
    review: undefined,
    replyTo: first,
    at: made,
    pending: false,
  });
  thread.comments = [...commentsOf(thread), comment];
  return comment;
}
/** The thread holding the review comment whose databaseId is `id`, and that comment. */
function threadOfComment(
  state: Threads,
  id: number | undefined
): { thread: Json; comment: Json } | undefined {
  for (const thread of state.list) {
    const comment = commentsOf(thread).find((one) => numberOf(one.databaseId) === id);
    if (comment !== undefined) return { thread, comment };
  }
  return undefined;
}
/** The answer of one field of a GraphQL mutation on a thread: the payload of the write it made;
 * null with GitHub's NOT_FOUND where the thread it names is none of the world's, whose type and
 * path are GitHub's and whose sentence is the stand-in's own, since GitHub's exact wording is not
 * measured here; or undefined for a field this state does not answer, which the world's graph
 * answers. */
export interface FieldAnswer {
  readonly alias: string;
  readonly payload: Json | null | undefined;
  readonly error?: { type: string; path: string[]; message: string };
}
/** What a call on the review threads was answered with: a REST comment it made, a refusal, or
 * one answer per field of a GraphQL mutation. */
export type ThreadAnswer =
  | { readonly rest: Json }
  | { readonly refused: string; readonly documentation?: string }
  | { readonly fields: readonly FieldAnswer[] };
/** Applies `call` to `state` and says what GitHub answered it, or undefined for a call that
 * writes no thread (and for every call of a run with no world). Each call is read as GitHub
 * takes it:
 *   - a review create the world answered with a review (the record's `review`): each inline
 *     comment it sent opens a thread, the review's own, pending while the review is;
 *   - the submit of that review: its pending comments are submitted;
 *   - `POST …/pulls/<n>/comments`: a reply where it names `in_reply_to`, and a thread of its own
 *     otherwise, which takes a `commit_id`, a `path` and a `line` (or `subject_type: file`, or a
 *     `position`), each refused 422 without what it takes (COMMENT_BODY, COMMENT_FIELDS);
 *   - `POST …/pulls/<n>/comments/<id>/replies`: a reply in the thread of the comment `<id>`,
 *     which GitHub answers 404 where no thread holds it; a reply to a reply is filed in that
 *     thread, replying to its first comment (`replyIn`);
 *   - `resolveReviewThread` and `addPullRequestReviewThreadReply`: the thread the input names,
 *     resolved or carrying the reply, or GitHub's NOT_FOUND where no thread of the world is it.
 * A call no route answered (the record's `route` null) did nothing, and neither did a GraphQL
 * call GitHub refuses whole for a variable it declares and does not send. */
export function threadCall(
  at: ThreadWorld,
  state: Threads,
  call: Recorded
): ThreadAnswer | undefined {
  const { world } = at;
  if (world === undefined || (call.as ?? "gh") !== "gh" || call.argv[0] !== "api") return undefined;
  const { argv } = call;
  const { repo, pr } = world;
  const made = call.at ?? "";
  const endpoint = endpointPath(argv);
  if (call.review !== undefined) {
    const submits = argv.map((arg) => submitsReview(arg, repo)).find((id) => id !== undefined);
    if (submits !== undefined) {
      for (const comment of state.list.flatMap(commentsOf))
        if (
          comment.state === "PENDING" &&
          isObject(comment.pullRequestReview) &&
          String(comment.pullRequestReview.databaseId) === submits
        )
          comment.state = "SUBMITTED";
      return undefined;
    }
    if (!isPost(argv) || !argv.some((arg) => reviewsRoute(arg, repo))) return undefined;
    const review = numberOf(routeAnswer(at.routes, call.route)?.id);
    const sent = sentBody(call) ?? {};
    for (const one of Array.isArray(sent.comments) ? sent.comments.filter(isObject) : []) {
      const comment = newComment(at, state, {
        body: typeof one.body === "string" ? one.body : "",
        path: one.path,
        line: numberOf(one.line) ?? null,
        commit: call.review.head,
        review,
        replyTo: undefined,
        at: made,
        pending: call.review.state === "PENDING",
      });
      state.list.push(newThread(comment, one.side, numberOf(one.start_line) ?? null));
    }
    return undefined;
  }
  if (call.route === null) return undefined;
  const pulls = `repos/${repo}/pulls/${pr}`;
  if (isPost(argv) && endpoint === `${pulls}/comments`) {
    const sent = sentBody(call) ?? {};
    const body = typeof sent.body === "string" ? sent.body : "";
    if (body === "") return { refused: COMMENT_BODY };
    if (sent.in_reply_to !== undefined && sent.in_reply_to !== null) {
      const found = threadOfComment(state, numberOf(sent.in_reply_to));
      if (found === undefined) return { refused: NOT_FOUND, documentation: COMMENT_DOCS };
      const reply = replyIn(at, state, found.thread, body, made);
      return { rest: restComment(world, found.thread, reply) };
    }
    const commit = typeof sent.commit_id === "string" ? sent.commit_id : "";
    const path = typeof sent.path === "string" ? sent.path : "";
    const line = numberOf(sent.line) ?? null;
    if (
      commit === "" ||
      path === "" ||
      (line === null && sent.subject_type !== "file" && sent.position === undefined)
    )
      return { refused: COMMENT_FIELDS };
    const comment = newComment(at, state, {
      body,
      path,
      line,
      commit,
      review: undefined,
      replyTo: undefined,
      at: made,
      pending: false,
    });
    const thread = newThread(comment, sent.side, numberOf(sent.start_line) ?? null);
    state.list.push(thread);
    return { rest: restComment(world, thread, comment) };
  }
  const replied = /^repos\/(.+)\/pulls\/(\d+)\/comments\/(\d+)\/replies$/.exec(endpoint);
  if (isPost(argv) && replied?.[1] === repo && Number(replied[2]) === pr) {
    const found = threadOfComment(state, Number(replied[3]));
    if (found === undefined) return { refused: NOT_FOUND, documentation: REPLY_DOCS };
    const sent = sentBody(call) ?? {};
    const body = typeof sent.body === "string" ? sent.body : "";
    if (body === "") return { refused: COMMENT_BODY };
    const reply = replyIn(at, state, found.thread, body, made);
    return { rest: restComment(world, found.thread, reply) };
  }
  if (endpoint !== "graphql") return undefined;
  const { operation, variables } = graphqlOf(call);
  if (operation?.type !== "mutation") return undefined;
  if (
    operation.variables.some(
      (one) =>
        one.type.endsWith("!") &&
        (variables[one.name] === undefined || variables[one.name] === null)
    )
  )
    return undefined;
  const fields: FieldAnswer[] = [];
  for (const selection of operation.selections) {
    if (selection.kind !== "field") continue;
    const alias = selection.alias ?? selection.name;
    const input = argumentsOf(selection, variables).input;
    const given = isObject(input) ? input : {};
    const resolves = selection.name === "resolveReviewThread";
    if (!resolves && selection.name !== "addPullRequestReviewThreadReply") {
      fields.push({ alias, payload: undefined });
      continue;
    }
    const named = resolves ? given.threadId : given.pullRequestReviewThreadId;
    const thread = state.list.find((one) => one.id === named);
    if (thread === undefined) {
      fields.push({
        alias,
        payload: null,
        error: {
          type: "NOT_FOUND",
          path: [alias],
          message: `Could not resolve to a node with the global id of '${String(named)}'.`,
        },
      });
      continue;
    }
    const clientMutationId = given.clientMutationId ?? null;
    if (resolves) {
      Object.assign(thread, {
        isResolved: true,
        isCollapsed: true,
        resolvedBy: { login: worldLogin(at.routes) },
        viewerCanResolve: false,
        viewerCanUnresolve: true,
      });
      fields.push({ alias, payload: { clientMutationId, thread } });
    } else {
      const body = typeof given.body === "string" ? given.body : "";
      fields.push({
        alias,
        payload: { clientMutationId, comment: replyIn(at, state, thread, body, made) },
      });
    }
  }
  return { fields };
}
/** A review comment of `thread` as GitHub's REST API answers it, read off the comment GitHub's
 * GraphQL holds (`newComment`, and the world's own): its id is the comment's databaseId, which is
 * what the REST reply route names, and a reply names the comment it replies to
 * (`in_reply_to_id`). */
export function restComment(world: World | undefined, thread: Json, comment: Json): Json {
  const api = `https://api.github.com/repos/${world?.repo ?? ""}/pulls`;
  const id = numberOf(comment.databaseId);
  const self = `${api}/comments/${id}`;
  const pull = `${api}/${world?.pr}`;
  const author = isObject(comment.author) ? String(comment.author.login ?? "") : "";
  const replyTo = isObject(comment.replyTo) ? numberOf(comment.replyTo.databaseId) : undefined;
  return {
    url: self,
    pull_request_review_id: isObject(comment.pullRequestReview)
      ? (numberOf(comment.pullRequestReview.databaseId) ?? null)
      : null,
    id,
    node_id: comment.id,
    path: comment.path,
    commit_id: commitOf(comment),
    original_commit_id: isObject(comment.originalCommit)
      ? comment.originalCommit.oid
      : commitOf(comment),
    user: { login: author, type: author.endsWith("[bot]") ? "Bot" : "User" },
    body: comment.body,
    created_at: comment.createdAt,
    updated_at: comment.updatedAt,
    html_url: comment.url,
    pull_request_url: pull,
    author_association: comment.authorAssociation ?? "NONE",
    _links: { self: { href: self }, html: { href: comment.url }, pull_request: { href: pull } },
    start_line: thread.startLine ?? null,
    original_start_line: thread.originalStartLine ?? null,
    start_side: null,
    line: comment.line ?? null,
    original_line: comment.originalLine ?? null,
    side: thread.diffSide ?? "RIGHT",
    subject_type: thread.subjectType === "FILE" ? "file" : "line",
    ...(replyTo === undefined ? {} : { in_reply_to_id: replyTo }),
  };
}

/** The merge states a ruleset that requires review threads resolved holds `BLOCKED`: the states
 * of a pull request GitHub would otherwise merge. A conflict (`DIRTY`), a base the head is behind
 * (`BEHIND`) and a state GitHub has not computed (`UNKNOWN`) are each the world's own. */
const MERGEABLE_BUT_FOR_THREADS = ["CLEAN", "UNSTABLE", "HAS_HOOKS"];
/** The merge state GitHub reports for the pull request at `head`, with its threads `threads`: the
 * world's own state for that head (`mergeStates`, gh-call.ts's `mergeStateOn`), except that a
 * base whose rules require review threads resolved (the world's answer to the rules in force on
 * its base, `rules/branches/<base>`: a `pull_request` rule with
 * `required_review_thread_resolution`) holds a pull request it would otherwise merge `BLOCKED`
 * while any thread with a comment GitHub has published (no pending review's alone) is unresolved.
 * The base is read where GitHub enforces it, its rules, since a ruleset alone protects it (rig.sh's
 * base_ruleset_routes) and a read of the classic protection's fields finds none. GitHub moves a
 * head so protected from `BLOCKED` to `CLEAN` when the last such thread is resolved, and a world
 * whose state never moved told every agent that resolving a thread changes nothing; a base that
 * requires no resolution is moved by no thread. */
export function mergeStateOf(
  at: ThreadWorld,
  threads: Threads,
  mergeStates: unknown,
  head: string
): string {
  const own = mergeStateOn(mergeStates, head);
  const { world } = at;
  if (world === undefined || !MERGEABLE_BUT_FOR_THREADS.includes(own)) return own;
  const rules = routeFor(at.routes, [
    "api",
    `repos/${world.repo}/rules/branches/${world.base}`,
  ])?.stdout;
  const requires =
    Array.isArray(rules) &&
    rules.some(
      (rule) =>
        isObject(rule) &&
        rule.type === "pull_request" &&
        isObject(rule.parameters) &&
        rule.parameters.required_review_thread_resolution === true
    );
  const open = threads.list.some(
    (thread) =>
      thread.isResolved !== true &&
      commentsOf(thread).some((comment) => comment.state !== "PENDING")
  );
  return requires && open ? "BLOCKED" : own;
}
