import {
  focusManager,
  matchQuery,
  type Query,
  type QueryClient,
  type QueryFilters,
  queryOptions,
  useQuery,
} from "@tanstack/react-query";
import { useMemo } from "react";

import {
  api,
  type IssueTreeAnswer,
  type IssueTreeNode,
  type IssueTreeTuple,
} from "../../../api/client";
import { liveFeedUp } from "../../../api/live-socket";
import {
  MIN_REFETCH_INTERVAL_MS,
  type RefreshTarget,
  refreshQueries,
} from "../../../api/query-refresh";
import type { IssueDetails } from "../../../api/types";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { refetchFailedOnFocus } from "../../refs/reference-target";
import { blocksOfRole, mergeTreePages, type TreeBlock } from "./tree-nodes";

/**
 * The issue's tree read (`GET /api/v1/issues/{key}/tree`), which the model serves: the one file
 * of the issue page that names the route. Every other file reads the answer through the names
 * below, so the day the answer's shape moves onto the generated contract only these aliases
 * change.
 */
export type TreeAnswer = IssueTreeAnswer;
export type TreeAnswerNode = IssueTreeNode;
export type TreeTuple = IssueTreeTuple;

/** The views of the tree a filter reads (`?view=`). Each is the query of the document of the
 *  same role, so the name says which role's blocks a caller asks for as well (`blocksOfRole`). */
export type TreeFilter = "spec" | "verification" | "plan";

/** The filters that read a view of the tree, in this order. Decisions reads none: its view is
 *  the decisions read (`issueDecisionsQuery`), which the load reads already. */
export const TREE_FILTERS: readonly TreeFilter[] = ["spec", "verification", "plan"];

/** The tree read's key. It is not under `["issue", key]`, so a write that invalidates the
 *  issue's key by prefix does not reach it: `subscribeTreeFreshness` refreshes it, `exact` since
 *  every view's key is under it, after a write or an event changed a query of the issue, and a
 *  write of the page may refresh it by this key itself, through `refreshQueries`. */
export function issueTreeKey(key: string): readonly ["issue-tree", string] {
  return ["issue-tree", key];
}

/** One view's key: under the tree's, and refreshed by `subscribeTreeFreshness` alone too. */
export function viewKey(
  key: string,
  filter: TreeFilter
): readonly ["issue-tree", string, TreeFilter] {
  return ["issue-tree", key, filter];
}

/** Drops every view of issue `key` the cache holds (`resetQueries`), so a held one is gone and
 *  the one an observer has enabled is read again. A new load of the issue calls it, and so does a
 *  write of the page that refreshes the tree by its key itself, so neither draws a view older
 *  than its tree. `subscribeTreeFreshness` drops none: it refreshes each held view through
 *  `refreshQueries`, and the view keeps what it draws until its new answer lands. */
export function dropViews(queryClient: QueryClient, key: string): void {
  for (const filter of TREE_FILTERS) {
    void queryClient.resetQueries({ exact: true, queryKey: viewKey(key, filter) });
  }
}

/**
 * Every page of the tree read, or of one view, asked for in turn: the first, then each next page
 * the page before names (`cursor`), and the pages merged once the last answers, so the tree is
 * built once and never from part of the walk. A page that names no next page is the last,
 * whether its cursor is null, left out of the body or empty, as a server that drops an empty
 * field writes it. A cursor the walk has asked for already ends it there with the pages read, so
 * a server that names one page again cannot hold the walk in a loop. A page that fails fails the
 * read, and a read made again - a refetch, a retry - starts from the first page, since a cursor
 * names a place in the walk as it stood when the page that gave it was answered. A read TanStack
 * gives up (`signal`: a reset, the last observer gone) ends its page in flight and asks for no
 * next one.
 */
async function readTree(
  key: string,
  view: TreeFilter | undefined,
  signal: AbortSignal
): Promise<TreeAnswer> {
  const first = await api.getIssueTree(key, { view }, { signal });
  const pages: [TreeAnswer, ...TreeAnswer[]] = [first];
  const asked = new Set<string>();
  let cursor: string | null | undefined = first.cursor;
  while (typeof cursor === "string" && cursor !== "" && !asked.has(cursor)) {
    asked.add(cursor);
    signal.throwIfAborted();
    const page = await api.getIssueTree(key, { cursor, view }, { signal });
    pages.push(page);
    cursor = page.cursor;
  }
  return mergeTreePages(pages);
}

/**
 * How the page's reads are kept fresh: by the events that can change them, never by age, so a
 * slot the stage mounts again past the app's 30 s `staleTime` sends no read; only a read whose
 * last answer failed is read again on focus (`refetchFailedOnFocus`), so a read that failed
 * recovers on the open page while a healthy one stays quiet. Every read of `useIssueTree` that an
 * event refreshes takes it, which names it `EVENT_FRESH`; the tree read and its views take it but
 * for the focus (`TREE_FRESH`).
 */
export const REFRESHED_BY_EVENTS = {
  refetchOnWindowFocus: refetchFailedOnFocus,
  staleTime: Number.POSITIVE_INFINITY,
} as const;

/** How the tree read and its views are kept fresh: as `REFRESHED_BY_EVENTS`, except that nothing
 *  of TanStack's own reads either again. A window focus does not: the page's one subscription
 *  reads a refused one again on a focus, and only while the live feed is down
 *  (`subscribeTreeFreshness`); while it is up, its events say what changed, and a refused read is
 *  not asked again on every focus. Nor does a new observer of a refused read (`retryOnMount`) - a
 *  slot drawn again, a filter pressed - but for a view that a refresh asked for again since its
 *  last read began (`viewQuery`); nor the browser saying the network is back
 *  (`refetchOnReconnect`), which it may say while the feed never went: a feed that went comes
 *  back with a refresh of every query. So after its own retries a refused read is read again only
 *  on the reader's Retry, an event of the issue, the feed coming back, or a focus while the feed
 *  is down, and a refused view whose filter is off then is read when its filter is next on. */
const TREE_FRESH = {
  ...REFRESHED_BY_EVENTS,
  refetchOnReconnect: false,
  refetchOnWindowFocus: false,
  retryOnMount: false,
} as const;

/** The issue's whole tree, one query over every page (`readTree`). */
export const issueTreeQuery = (key: string) =>
  queryOptions({
    ...TREE_FRESH,
    queryKey: issueTreeKey(key),
    queryFn: ({ signal }) => readTree(key, undefined, signal),
  });

/** The view queries a refresh of the subscription asked for again while they held no answer
 *  (`watchTree`), each until its next read begins. TanStack keeps no such record: a refused read
 *  is marked stale by its refusal already, and marking it stale again records nothing new. Held
 *  as plain objects, since each view's query is typed by its own key and answer. */
const askedAgain = new WeakSet<object>();

/** One view of the issue's tree, one query over every page (`readTree`). A plain query rather
 *  than one per page, so an observer that is never enabled can watch a view a slot read. A filter
 *  turned on (`ViewReadProvider`) reads its view where the view was never read, where a write or
 *  an event marked its answer stale, and, where its read was refused, only where a refresh asked
 *  for it again since that read began (`askedAgain`): a write or an event of the issue, the live
 *  feed coming back, a focus while the feed is down. A filter turned off and on again is none of
 *  those, so it reads a refused view no more. */
export const viewQuery = (key: string, filter: TreeFilter) =>
  queryOptions({
    ...TREE_FRESH,
    queryKey: viewKey(key, filter),
    queryFn: ({ signal }) => readTree(key, filter, signal),
    retryOnMount: (query) => askedAgain.has(query),
  });

/** One role's blocks from the tree read, answered as the blocks read of that role's document
 *  answered them (`artifactBlocksQuery`), so a caller's lines that read these members stay as
 *  they are. The state is the tree read's own. */
export interface TreeBlocksRead {
  readonly data: TreeBlock[] | undefined;
  readonly isError: boolean;
  readonly isSuccess: boolean;
  readonly refetch: () => Promise<unknown>;
}

/** The tree read, and how far it has got. */
export interface TreeRead {
  /** Every page merged, once the last has answered. */
  readonly answer: TreeAnswer | undefined;
  /** Whether the read holds an answer, which is what a view that draws from the blocks it holds
   *  waits on. A refetch that fails over an answer already held keeps it held - TanStack keeps a
   *  query's data through a failed refetch - so the view keeps the text it drew, which waiting
   *  on `blocksRead` would blank, since that failure turns it false. Both are false before the
   *  first answer and after a first read that fails. */
  readonly blocksHeld: boolean;
  /** Whether the read answered every page with success. A read that fails, a first one or a
   *  refetch, turns it false, even where an answer is still held. It is what a view reads that
   *  must tell "not read yet" from "holds none": until it is true a typed block - the spec's
   *  summary among them, which the head waits on before it says a spec has none
   *  (`IssueThreeThings.tsx`) - may be missing because it has not arrived rather than because
   *  the issue holds none. A view that draws from the blocks it holds reads `blocksHeld`
   *  instead. */
  readonly blocksRead: boolean;
  /** The typed blocks of the document that plays `role` (`blocksOfRole`), so a view reads its
   *  blocks here and costs no request of its own; the same value for a role until the answer or
   *  the read's state changes. */
  readonly blocksOf: (role: TreeFilter) => TreeBlocksRead;
  /** Whether the read was refused with no answer held, its retries spent, and holds none yet:
   *  from that refusal until a read answers, the reads asked again in between included, whoever
   *  asked them - the reader's Retry, an event, the live feed coming back, a focus while the
   *  feed is down. TanStack puts a query that holds no answer back to `pending` while it is read
   *  again, so this reads its count of refusals instead, which only a reset of the query clears.
   *  A refetch that fails over a held answer is no such failure, since the page still draws what
   *  it holds. */
  readonly failed: boolean;
  /** Whether the read holds no answer and has not settled since its last read began: its first
   *  read is out, or a read asked again after a refusal is, while `failed` holds too. It is what
   *  holds back a read that waits on every other read of the page, as the batched name read does
   *  (`useIssueTree`). */
  readonly pending: boolean;
  /** Reads the tree again, every page from the first: what a Retry of a failed read asks. */
  readonly refetch: () => Promise<unknown>;
}

/**
 * The tree read alone, for the issue page's model (`useIssueTree`) and for a surface that needs
 * the blocks of a role document, or how the read stands, and none of the model's other reads.
 * Each value it answers keeps its identity while the read's answer and state stand, so a
 * caller's memo built on one is not built again on every render; a role's blocks are made the
 * first time a caller asks for them, so a caller that reads none costs none.
 */
export function useTreeRead(issue: IssueDetails): TreeRead {
  const {
    data: answer,
    errorUpdateCount,
    isError,
    isPending,
    isSuccess,
    refetch,
  } = useQuery(issueTreeQuery(issue.key));
  const blocks = useMemo(
    () => byRole((role) => blocksOfRole(answer, role, issue)),
    [answer, issue]
  );
  const blocksOf = useMemo(
    () => byRole((role): TreeBlocksRead => ({ data: blocks(role), isError, isSuccess, refetch })),
    [blocks, isError, isSuccess, refetch]
  );
  return useMemo(
    () => ({
      answer,
      blocksHeld: answer !== undefined,
      blocksRead: isSuccess,
      blocksOf,
      failed: answer === undefined && errorUpdateCount > 0,
      pending: isPending,
      refetch,
    }),
    [answer, blocksOf, errorUpdateCount, isPending, isSuccess, refetch]
  );
}

/** `make`'s value for each role, made on the first ask for that role and the same value on
 *  every ask after. */
function byRole<Value>(make: (role: TreeFilter) => Value): (role: TreeFilter) => Value {
  const made = new Map<TreeFilter, { readonly value: Value }>();
  return (role) => {
    let held = made.get(role);
    if (held === undefined) {
      held = { value: make(role) };
      made.set(role, held);
    }
    return held.value;
  };
}

/**
 * Keeps the tree read and its views as fresh as the issue's other reads, which no write
 * reaches directly: their keys are not under `["issue", key]` (`issueTreeKey`). A query of the
 * issue (`["issue", key, …]`, `["asks", key, …]`, `["comments", key, …]`,
 * `["issue-messages", key, …]` and the decisions read's key) has changed when it is:
 *
 * - invalidated: a page write's `invalidateQueries`, and an event's own refresh
 *   (`api/query-refresh.ts`' `owe`);
 * - written by `setQueryData` while the live feed is down: a Keep and a status change write that
 *   way (TanStack's `success` action with `manual`), so such a write refreshes the tree with no
 *   event to do it. While the feed is up the write's event refreshes the tree, so a write that
 *   puts its answer in the cache before Dispatch holds it - the margin's resolve does as it is
 *   pressed, and invalidates the comments once Dispatch answers - reads the tree for that answer
 *   and for its event, twice at most, and never before Dispatch holds it;
 * - answered by a read that began in a turn after the tree's last read began (`answeredNewer`),
 *   where the query was marked invalid as that read began, or the answer is other than the data
 *   it held when that tree read began - the issue read's other in a field someone's act moves
 *   (`UNACTED_ISSUE_FIELDS`). TanStack reports an invalidation only when a query turns invalid,
 *   so a write whose invalidation lands while the query is invalid already - its last read
 *   failed, a refresh it owes (`owe`) has not begun, its read for an earlier write is still out -
 *   raises no event, and the read made for it, which the app's client sends at once or once the
 *   one out settles, is how that write shows, whatever it answers: where the query's first read
 *   failed there is nothing held to compare it with. A read of a valid query that answers what it
 *   held, as one made again for its age does, changes nothing; nor does any read that began
 *   before the tree's or in the same turn, which the tree read answers already, as the reads a
 *   page coming back to the issue sends, or a refresh of every query the page holds, begin
 *   together.
 *
 * On any of them it reads the tree again, marks every held view stale, which reads the open view
 * again while each view keeps the answer it holds, and drawn, until its new one lands, and reads
 * the decisions again where something observes them. Every change of one turn is one refresh,
 * made in the microtask after it.
 *
 * A window focus reads the tree and its views only while the live feed is down (`liveFeedUp`):
 * a refused one is read again then, and a read of the issue the focus sends may change it as
 * above. While the feed is up its events say what changed, so a focus reads neither, and what
 * the reads of the issue a focus sends answer is no change. A refused tree read, its retries
 * spent, so stays refused until the reader's Retry, an event of the issue, the feed coming back
 * (its refresh of every query after a gap) or a focus while the feed is down.
 *
 * All of it goes through the app's refresh scheduler (`refreshQueries`), which never cancels a
 * read in flight - a walk of many pages is not started over by each event of a burst - reads a
 * query that is out once more when it settles, and starts one at most once a second. So the
 * subscription starts no tree read while one is out, and owes one at most after it: a change
 * while the tree's first read is out, or after that read failed, is one more read once it
 * settles; a write and the event it raises read the tree once or twice, never more. Where the
 * decisions read's own invalidation was one of the turn's changes, its read is already out or
 * owed, and is not asked for again; nor is a read that began in the turn the change was seen
 * in, which answers it already, as a refresh of every query the page holds reads the tree and
 * the open view itself. The tree's and the views' own keys are no query of the issue, and what
 * the refresh's own calls raise is ignored (`calling`), so a refresh never starts another. That
 * holds for the decisions read it asks for too where the scheduler would hold that read back -
 * its last read began under a second ago, or one is out: the refresh marks it stale at once and
 * asks for it once the scheduler begins it at once, so it begins inside the refresh's own call.
 * A read of the decisions anyone else begins first - a Keep's, a stage's as the reader comes
 * back - answers that ask, as it pays a refresh the scheduler owes, and is no read of the
 * refresh's own; and the ask goes to the scheduler, which marks the read stale where nothing
 * draws it, as the reader leaves.
 *
 * It is one subscription for each client and issue, which outlives the stage that asks for it:
 * kept while a stage holds it, and after, for as long as the cache holds the issue's tree. A
 * change made while no stage of the issue is mounted - the event of another person's write while
 * the reader is elsewhere - marks the tree stale, as the scheduler marks any query nothing draws,
 * so the tree is read again when the reader comes back; a refused tree, which TanStack reads
 * again for no new observer (`TREE_FRESH`), is read for what the stage reads again of the
 * issue's reads the change marked stale (`answeredNewer`); and a refresh that lands after the
 * stage went sends nothing. Answers the release of this stage's hold.
 */
export function subscribeTreeFreshness(queryClient: QueryClient, key: string): () => void {
  let byKey = freshness.get(queryClient);
  if (byKey === undefined) {
    byKey = new Map();
    freshness.set(queryClient, byKey);
  }
  const held = byKey.get(key) ?? watchTree(queryClient, key, byKey);
  held.holders += 1;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    held.holders -= 1;
    held.release();
  };
}

/** One issue's subscription (`subscribeTreeFreshness`), and how many stages hold it. */
interface TreeFreshness {
  holders: number;
  /** Ends the subscription where no stage holds it and the cache holds no tree of the issue. */
  readonly release: () => void;
}

/** Each client's subscriptions, by the issue's key. */
const freshness = new WeakMap<QueryClient, Map<string, TreeFreshness>>();

/**
 * The fields of the issue read (`IssueDetails`) that move with no act of anyone: its claim
 * holder's liveness (`holder_state`, `last_seen`, `takeable_at`) and its route's reach
 * (`route_holder`, `route_status`, `route_status_reason`), which Dispatch resolves on each read
 * from its copy of the listener's session list, keeps in no table and puts on no event (the
 * contract's `IssueClaimLiveness` and `IssueRouteReach`). An answer of the issue read that moved in
 * these alone says nothing the tree read answers, so it reads the tree no more.
 */
const UNACTED_ISSUE_FIELDS: Partial<Record<keyof IssueDetails, true>> = {
  holder_state: true,
  last_seen: true,
  route_holder: true,
  route_status: true,
  route_status_reason: true,
  takeable_at: true,
};

/** Whether two answers of the issue read differ in no field but `UNACTED_ISSUE_FIELDS`. A field an
 *  answer did not change keeps the object of the answer it replaces (TanStack's structural
 *  sharing, which `issueQuery` keeps), so each field is compared by identity. */
function sameActs(held: unknown, now: unknown): boolean {
  if (held === now) return true;
  if (typeof held !== "object" || held === null || typeof now !== "object" || now === null) {
    return false;
  }
  const before = held as Record<string, unknown>;
  const after = now as Record<string, unknown>;
  const moved = (field: string): boolean =>
    !Object.hasOwn(UNACTED_ISSUE_FIELDS, field) && before[field] !== after[field];
  return !Object.keys(before).some(moved) && !Object.keys(after).some(moved);
}

/** The subscription of issue `key`, entered in `byKey` until it ends. */
function watchTree(
  queryClient: QueryClient,
  key: string,
  byKey: Map<string, TreeFreshness>
): TreeFreshness {
  const cache = queryClient.getQueryCache();
  const tree: RefreshTarget = { exact: true, queryKey: issueTreeKey(key) };
  const decisions: RefreshTarget = {
    exact: true,
    queryKey: issueDecisionsQuery(key, true).queryKey,
  };
  const issueRead: QueryFilters = { exact: true, queryKey: ["issue", key] };
  const issueQueries: readonly QueryFilters[] = [
    { queryKey: ["issue", key] },
    { queryKey: ["asks", key] },
    { queryKey: ["comments", key] },
    { queryKey: ["issue-messages", key] },
    decisions,
  ];
  const views: readonly RefreshTarget[] = TREE_FILTERS.map((filter) => ({
    exact: true,
    queryKey: viewKey(key, filter),
  }));
  let calling = false;
  let queued = false;
  let stopped = false;
  // Whether the decisions read's own invalidation is one of this turn's changes.
  let decisionsInvalidated = false;
  // When the decisions read last began, as this subscription records it: once the cache has told
  // every listener that the read began, the scheduler among them, so this record is never earlier
  // than the scheduler's own, whichever of the two hears of the read first.
  let decisionsBegan: number | undefined;
  // The decisions read a refresh asked for while the scheduler would hold it back, until the
  // refresh asks for it once the scheduler begins it at once (`timer`), or a read of it begins.
  let decisionsHeld: { timer: number | undefined } | undefined;
  // Whether a window focus heard while the live feed was up may still be beginning its reads:
  // TanStack begins them a few microtasks after it tells its listeners (`QueryClient.mount`), so
  // a read begun before the next task is the focus's.
  let focusing = false;
  // The turn of the event loop each read last began in, numbered as this subscription sees
  // turns, and the turn the refresh's first change was seen in.
  let turn = 0;
  let turnOpen = false;
  let changedIn = 0;
  const beganIn = new Map<string, number>();
  const turnNow = (): number => {
    if (!turnOpen) {
      turnOpen = true;
      turn += 1;
      queueMicrotask(() => {
        turnOpen = false;
      });
    }
    return turn;
  };
  // The turn the tree's last read began in, and the data each query of the issue held then.
  let treeBegan = 0;
  let seen = new Map<string, unknown>();
  // The turn each query of the issue's last read began in, or 0 for one whose answer the tree's
  // read answers already: a read the refresh began, or a focus while the live feed was up.
  const began = new Map<string, number>();
  // The queries of the issue whose last read, not the refresh's own, began while the query was
  // marked invalid: by its last read's failure, or by an invalidation the subscription heard or
  // one TanStack reports as nothing - a write's that lands while the query is invalid already,
  // after a failed read or an owed refresh (`owe`) - which that read may be the only sign of.
  const readWhileInvalid = new Set<string>();
  const answeredNewer = (query: Query): boolean => {
    const hash = query.queryHash;
    if ((began.get(hash) ?? 0) <= treeBegan) return false;
    if (readWhileInvalid.has(hash)) return true;
    if (!seen.has(hash)) return false;
    const held = seen.get(hash);
    return matchQuery(issueRead, query)
      ? !sameActs(held, query.state.data)
      : held !== query.state.data;
  };
  // Records each view of `targets` that holds no answer as asked for again (`askedAgain`), before
  // the scheduler is asked. The read that answers the ask clears the record as it begins: the
  // scheduler's own where the filter is on, and where it is off, the one its filter sends when it
  // is next on, since the scheduler only marks stale a view nothing reads.
  const markAskedAgain = (targets: readonly RefreshTarget[]): void => {
    for (const target of targets) {
      const query = views.includes(target) ? cache.find(target) : undefined;
      if (query !== undefined && query.state.data === undefined) askedAgain.add(query);
    }
  };
  // Asks the scheduler for `targets`. What the call raises is the subscription's own (`calling`):
  // a read the scheduler begins in it, and the stale mark it puts on one it holds back.
  const ask = (targets: readonly RefreshTarget[]): void => {
    markAskedAgain(targets);
    calling = true;
    try {
      refreshQueries(queryClient, targets);
    } finally {
      calling = false;
    }
  };
  // The moment the scheduler begins the decisions read at once: a second after its last read
  // began. For a read that began before this subscription did, its last answer, which came after
  // it began, stands in for that start.
  const decisionsDue = (query: Query): number =>
    (decisionsBegan ?? Math.max(query.state.dataUpdatedAt, query.state.errorUpdatedAt)) +
    MIN_REFETCH_INTERVAL_MS;
  // Asks for the held decisions read once the scheduler begins it at once, so the read begins
  // inside this subscription's own call; never while a read of it is out.
  const armDecisions = (): void => {
    const held = decisionsHeld;
    const query = cache.find(decisions);
    if (held === undefined || held.timer !== undefined || query?.state.fetchStatus !== "idle") {
      return;
    }
    held.timer = window.setTimeout(
      () => {
        held.timer = undefined;
        if (decisionsHeld !== held) return;
        const read = cache.find(decisions);
        if (read !== undefined && Date.now() < decisionsDue(read)) {
          armDecisions();
          return;
        }
        decisionsHeld = undefined;
        ask([decisions]);
      },
      Math.max(0, decisionsDue(query) - Date.now())
    );
  };
  // Lets the held decisions read go: a read of it began, which answers the refresh's ask whoever
  // began it, or the reader left, and the scheduler takes the ask (`hand`).
  const dropDecisions = (hand: boolean): void => {
    const held = decisionsHeld;
    if (held === undefined) return;
    window.clearTimeout(held.timer);
    decisionsHeld = undefined;
    if (hand) ask([decisions]);
  };
  const refresh = (): void => {
    queued = false;
    const decisionsOwed = decisionsInvalidated;
    decisionsInvalidated = false;
    if (stopped) return;
    // A read begun in the turn the change was seen in answers it already, since what a turn
    // hears of changed before the turn began and the read reaches the server after it: a
    // whole-cache refresh reads the tree and the open view itself, and they are not asked again.
    const targets = [tree, ...(decisionsOwed ? [] : [decisions]), ...views].filter((target) => {
      const query = cache.find(target);
      return query === undefined || beganIn.get(query.queryHash) !== changedIn;
    });
    // The decisions read the scheduler would hold back - its last read began under a second ago,
    // or one is out - is held here instead: marked stale now, as the scheduler marks a read it
    // owes, and asked for once the scheduler begins it at once (`armDecisions`).
    const read = targets.includes(decisions) ? cache.find(decisions) : undefined;
    if (
      read === undefined ||
      !read.isActive() ||
      (decisionsHeld === undefined &&
        read.state.fetchStatus === "idle" &&
        Date.now() >= decisionsDue(read))
    ) {
      ask(targets);
      return;
    }
    decisionsHeld ??= { timer: undefined };
    ask(targets.filter((target) => target !== decisions));
    calling = true;
    try {
      read.invalidate();
    } finally {
      calling = false;
    }
    armDecisions();
  };
  const unsubscribe = cache.subscribe((event) => {
    const { query } = event;
    const hash = query.queryHash;
    if (event.type === "removed") {
      began.delete(hash);
      beganIn.delete(hash);
      readWhileInvalid.delete(hash);
      if (matchQuery(tree, query)) watcher.release();
      return;
    }
    if (event.type !== "updated") return;
    const { action } = event;
    if (matchQuery({ queryKey: issueTreeKey(key) }, query)) {
      if (action.type !== "fetch") return;
      // A read begins, which answers whatever asked for the view again.
      askedAgain.delete(query);
      const now = turnNow();
      beganIn.set(hash, now);
      if (!matchQuery(tree, query)) return;
      treeBegan = now;
      seen = new Map();
      for (const filters of issueQueries) {
        for (const one of cache.findAll(filters)) {
          if (one.state.data !== undefined) seen.set(one.queryHash, one.state.data);
        }
      }
      return;
    }
    if (!issueQueries.some((filters) => matchQuery(filters, query))) return;
    const isDecisions = matchQuery(decisions, query);
    if (action.type === "fetch") {
      if (isDecisions) {
        queueMicrotask(() => {
          decisionsBegan = Date.now();
        });
        // A read of the decisions begun since a refresh asked for it answers that ask, whoever
        // began it, as it pays a refresh the scheduler owes; it is no read of the refresh's own.
        if (!calling) dropDecisions(false);
      }
      const own = calling || focusing;
      const now = turnNow();
      began.set(hash, own ? 0 : now);
      beganIn.set(hash, now);
      // The fetch has not cleared the mark: only the answer's success does.
      if (!own && query.state.isInvalidated) readWhileInvalid.add(hash);
      else readWhileInvalid.delete(hash);
      return;
    }
    // The held decisions read is asked for once no read of it is out.
    if (isDecisions) armDecisions();
    // A write the page put in the cache reads the tree only while the live feed is down: while it
    // is up, the write's event does.
    const changed =
      !calling &&
      (action.type === "invalidate" ||
        (action.type === "success" &&
          (action.manual === true ? !liveFeedUp(queryClient) : answeredNewer(query))));
    if (!changed) return;
    if (action.type === "invalidate" && matchQuery(decisions, query)) decisionsInvalidated = true;
    if (queued) return;
    queued = true;
    changedIn = turnNow();
    queueMicrotask(refresh);
  });
  // A window focus while the live feed is down reads a refused tree or view again; while it is
  // up it reads neither, and what the reads of the issue it begins answer is no change.
  const unfocus = focusManager.subscribe((focused) => {
    if (!focused || stopped) return;
    if (liveFeedUp(queryClient)) {
      focusing = true;
      setTimeout(() => {
        focusing = false;
      }, 0);
      return;
    }
    const refused = [tree, ...views].filter((target) => {
      const query = cache.find(target);
      return query?.state.status === "error" && query.state.fetchStatus === "idle";
    });
    if (refused.length > 0) {
      markAskedAgain(refused);
      refreshQueries(queryClient, refused);
    }
  });
  const watcher: TreeFreshness = {
    holders: 0,
    release: () => {
      if (stopped) return;
      // The reader left: the decisions read held for a refresh goes to the scheduler.
      if (watcher.holders === 0) dropDecisions(true);
      if (watcher.holders > 0 || cache.find(tree) !== undefined) return;
      stopped = true;
      unsubscribe();
      unfocus();
      byKey.delete(key);
    },
  };
  byKey.set(key, watcher);
  return watcher;
}
