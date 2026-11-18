import {
  focusManager,
  matchQuery,
  type Query,
  type QueryClient,
  type QueryFilters,
  queryOptions,
  replaceEqualDeep,
  useQuery,
} from "@tanstack/react-query";
import { useMemo } from "react";

import {
  api,
  type IssueTreeAnswer,
  type IssueTreeNode,
  type IssueTreeTuple,
} from "../../../api/client";
import { liveFeedUp, onLiveFeedBack } from "../../../api/live-socket";
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
 *  the one a filter watches is read again. It is for any caller after which a held view could be
 *  older than the tree the page draws, so that no view is drawn older than its tree.
 *  `subscribeTreeFreshness` drops none: it refreshes each held view through `refreshQueries`,
 *  and the view keeps what it draws until its new answer lands. */
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
 *
 * The first page waits until no write is out on `client` (`writesSettle`), since a tree read
 * made while a write is out reads a tree Dispatch may not have changed yet. A write the client
 * holds names no issue, so any write out holds the read of every issue's tree and views, with the
 * live feed up or down: a read begun meanwhile - the whole-cache refresh the write store makes for
 * a write that lands with no answer, a filter pressed - is one request, sent as the last write out
 * settles, and one given up while it waits sends nothing.
 */
async function readTree(
  client: QueryClient,
  key: string,
  view: TreeFilter | undefined,
  signal: AbortSignal
): Promise<TreeAnswer> {
  if (client.isMutating() > 0) await writesSettle(client, signal);
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

/** Resolves once the last write out on `client` settles (`isMutating`), as the client's mutation
 *  cache tells it; rejects with the read's abort reason where TanStack gives the read up first
 *  (`signal`). A read with no write out does not wait for it, so it sends its first page in the
 *  same turn it begins in. */
function writesSettle(client: QueryClient, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  const settled = Promise.withResolvers<void>();
  const stop = (): void => {
    unsubscribe();
    signal.removeEventListener("abort", abort);
  };
  const abort = (): void => {
    stop();
    settled.reject(signal.reason);
  };
  const unsubscribe = client.getMutationCache().subscribe(() => {
    if (client.isMutating() > 0) return;
    stop();
    settled.resolve();
  });
  signal.addEventListener("abort", abort);
  return settled.promise;
}

/**
 * How the page's reads are kept fresh: by the events that can change them, never by age, so a
 * slot the stage mounts again past the app's 30 s `staleTime` sends no read; only a read whose
 * last answer failed is read again on focus (`refetchFailedOnFocus`), so a read that failed
 * recovers on the open page while a healthy one stays quiet. Every read of `useIssueTree` that an
 * event refreshes takes it, which names it `EVENT_FRESH`; the tree read and its views take it,
 * with the changes `TREE_FRESH` names.
 */
export const REFRESHED_BY_EVENTS = {
  refetchOnWindowFocus: refetchFailedOnFocus,
  staleTime: Number.POSITIVE_INFINITY,
} as const;

/** The tree and view queries the page's one subscription asked for again since their last read
 *  began (`subscribeTreeFreshness`): each of its asks records every tree and view query it asks
 *  for - its refresh after a change of the issue, and its ask for a refused read on the live feed
 *  coming back or a window focus while the feed is down - and the query's next read clears the
 *  record as it begins. TanStack keeps no such record: a refused read is marked stale by its
 *  refusal already, and marking it stale again records nothing new. Held as plain objects, since
 *  each query is typed by its own key and answer. */
const askedAgain = new WeakSet<object>();

/** Whether a new observer of the tree or of a view may read it (`TREE_FRESH`): where its last
 *  read was not refused, or where the page's subscription asked for it again since that read
 *  began (`askedAgain`). It takes the query by its state, so the tree's query and each view's,
 *  each typed by its own answer, take the one predicate. */
function mayReadOnMount(query: Pick<Query, "state">): boolean {
  return query.state.status !== "error" || askedAgain.has(query);
}

/** How the tree read and its views are kept fresh: as `REFRESHED_BY_EVENTS`, except that none of
 *  TanStack's own reads asks again for a read Dispatch refused. A refused read, whether it holds
 *  an answer or not, stays refused until the reader's Retry, an event of the issue, the live feed
 *  coming back, or a window focus while the feed is down: the page's one subscription asks for it
 *  again on each of those but the Retry (`subscribeTreeFreshness`). So no window focus reads one
 *  of TanStack's own accord, nor does the browser saying the network is back, which it may say
 *  while the feed never went; and a new observer - a slot drawn, a surface opened, a filter
 *  pressed, the reader coming back to the issue - reads a refused one only where the
 *  subscription asked for it again since its last read began (`mayReadOnMount`, as both
 *  `retryOnMount`, for a read that holds no answer, and `refetchOnMount`, for one that holds an
 *  answer), so the reader coming back reads a refused tree only where it was asked for again.
 *  The views are owed besides at each load of the issue, answered or refused: a load drops them
 *  (`dropViews`), so each is read once, the open one at the load and another when its filter is
 *  next on. A read that holds an answer which a write or an event marked stale while nothing
 *  observed it is read when something next observes it, and a read never made is made. */
const TREE_FRESH = {
  ...REFRESHED_BY_EVENTS,
  refetchOnMount: mayReadOnMount,
  refetchOnReconnect: false,
  refetchOnWindowFocus: false,
  retryOnMount: mayReadOnMount,
} as const;

/** The issue's whole tree, one query over every page (`readTree`). */
export const issueTreeQuery = (key: string) =>
  queryOptions({
    ...TREE_FRESH,
    queryKey: issueTreeKey(key),
    queryFn: ({ client, signal }) => readTree(client, key, undefined, signal),
  });

/** One view of the issue's tree, one query over every page (`readTree`). A plain query rather
 *  than one per page, so an observer that is never enabled can watch a view a slot read. A filter
 *  turned on (`ViewReadProvider`) reads its view as any new observer of the tree's reads does
 *  (`TREE_FRESH`): where the view was never read, where a write or an event marked its answer
 *  stale, and, where its read was refused, only where the page's subscription asked for it again
 *  since that read began. A filter turned off and on again asks for nothing, so it reads a
 *  refused view no more. */
export const viewQuery = (key: string, filter: TreeFilter) =>
  queryOptions({
    ...TREE_FRESH,
    queryKey: viewKey(key, filter),
    queryFn: ({ client, signal }) => readTree(client, key, filter, signal),
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
 *   (`api/query-refresh.ts`' `owe`), but while a write is out only where nothing observes the
 *   query (below);
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
 * No refresh begins while a write is out (`isMutating`), with the live feed up or down, nor does
 * a tree or view read send (`readTree`): no event says when a write has reached Dispatch, and a
 * tree read made then reads a tree Dispatch may not have changed yet. A write the client holds
 * names no issue, so any write out holds the refresh of every issue. While the writes are out an
 * invalidation is no change by itself: a query's next answer is the change, where it differs from
 * the data the query held when the first of the writes went out - an invalidation of a query
 * nothing observes, which is not read again, is one, since no answer can say. So a write Dispatch
 * refuses, whose rollback puts the comments back and reads them again, reads no tree, while an
 * event of the issue landing meanwhile, whose refresh answers a new comment, reads it once. Every
 * change heard while the writes are out is the one refresh, run once the last of them settles,
 * answered or refused (`unmutate`), after a focus's read of a refused tree or view (below) the
 * writes held; it reads again no tree or view read the writes held, which sends as they settle
 * and so answers every change they held, nor one the focus's read began. So the margin's resolve
 * with the feed down reads the tree once, after Dispatch answered, and a write that lands with
 * no answer, whose write store refreshes every query while it is still out (`write-store.ts`),
 * reads the tree and the open view once, as the last write out settles.
 *
 * A window focus reads the tree and its views only while the live feed is down (`liveFeedUp`):
 * a refused one is read again then, or, where a write is out, once the last write out settles
 * while the feed is still down, and a read of the issue the focus sends may change it as above.
 * While the feed is up its events say what changed, so a focus reads neither, and what the reads
 * of the issue a focus sends answer is no change. The feed coming back after a gap
 * (`onLiveFeedBack`) with the gap replayed whole reads a refused tree or view again at once,
 * since no replayed event need name the issue; after an incomplete replay the event feed's
 * refresh of every query reads each one something observes, and the subscription asks for none
 * of those, which would read it twice, but only for a refused one nothing observes, which that
 * refresh marks stale and no more. A tree or view read out as a focus or a whole replay asks for
 * every refused one is owed: where it then ends refused it is asked for once more, where it
 * answers nothing more is, and either way it is owed no more.
 *
 * A refused tree or view read, its retries spent, with or without an answer held, so stays
 * refused until the reader's Retry, an event of the issue, the feed coming back or a focus while
 * the feed is down. Each ask of the subscription records every tree and view query it asks for
 * as asked for again (`askedAgain`) until its next read begins, and a new observer reads a
 * refused one only where it is so recorded (`TREE_FRESH`): a refused one nothing observes as the
 * subscription asks for it is read when something next observes it, and no slot drawn, surface
 * opened or filter pressed reads one otherwise.
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
 * holds for the decisions read it asks for where the scheduler would hold that read back - its
 * last read began under a second ago, or one is out: the refresh marks it stale at once and asks
 * for it once the scheduler begins it at once, so it begins inside the refresh's own call, and a
 * change heard meanwhile joins that one held ask. The tree read is held so too where the
 * scheduler would hold it back while a read of the issue the scheduler owes and holds back is
 * still to begin - observed, marked invalid, answered and none out, due a second after its last
 * read began - as the comments read an event marked invalid is when its last read began under a
 * second ago; and it waits until every such read has begun. Such a read pays the change the tree
 * read pays, and begun after the tree's, while still marked invalid, it would count as newer and
 * read the tree a third time for one write. A read of the tree or of the decisions anyone else
 * begins first - a Keep's, a stage's as the reader comes back - answers that ask, as it pays a
 * refresh the scheduler owes, and is no read of the refresh's own; and the ask goes to the
 * scheduler, which marks the read stale where nothing draws it, as the reader leaves.
 *
 * It is one subscription for each client and issue, which outlives the stage that asks for it:
 * kept while a stage holds it, and after, for as long as the cache holds the issue's tree. A
 * change made while no stage of the issue is mounted - the event of another person's write while
 * the reader is elsewhere - marks the tree stale, as the scheduler marks any query nothing draws,
 * so the tree is read again when the reader comes back, a refused one too, since the refresh
 * asked for it again; and a refresh that lands after the stage went sends nothing. Answers the
 * release of this stage's hold.
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
  // What each query of the issue holds now, by its hash.
  const issueData = (): Map<string, unknown> => {
    const data = new Map<string, unknown>();
    for (const filters of issueQueries) {
      for (const one of cache.findAll(filters)) {
        if (one.state.data !== undefined) data.set(one.queryHash, one.state.data);
      }
    }
    return data;
  };
  let calling = false;
  let queued = false;
  let stopped = false;
  // Whether the decisions read's own invalidation is one of this turn's changes, or came while a
  // write was out, for the refresh held back for the writes; forgotten as they settle where no
  // refresh was held.
  let decisionsInvalidated = false;
  // When the tree read and each read of the issue last began, as this subscription records it:
  // once the cache has told every listener that the read began, the scheduler among them, so
  // this record is never earlier than the scheduler's own, whichever of the two hears of the read
  // first.
  const startedAt = new Map<string, number>();
  // Records that the read of `hash` began, in a microtask, so every listener has heard it began.
  const recordStart = (hash: string): void => {
    queueMicrotask(() => {
      startedAt.set(hash, Date.now());
    });
  };
  // The tree read and the decisions read a refresh asked for while the scheduler would hold them
  // back, each until the refresh asks for it once the scheduler begins it at once (`timer`), or a
  // read of it begins. `since` is the moment from which the tree's waits for the reads of the
  // issue the scheduler owes (`owedDues`).
  const heldAsks = new Map<RefreshTarget, { since: number; timer: number | undefined }>();
  // Whether a window focus heard while the live feed was up may still be beginning its reads:
  // TanStack begins them a few microtasks after it tells its listeners (`QueryClient.mount`), so
  // a read begun before the next task is the focus's.
  let focusing = false;
  // Whether a write is out on the client, as its mutation cache last told this subscription
  // (`unmutate`): the hold, from the first write going out to the last settling. A write the
  // client holds names no issue, so any write out holds the subscription of every issue.
  let writing = queryClient.isMutating() > 0;
  // Whether a refresh was held back for a write out (`refresh`): it runs once the last write out
  // settles (`unmutate`).
  let heldBack = false;
  // The data each query of the issue held when the first write of the hold went out, and the
  // queries whose next answer is judged against it (`answeredNewer`): each one read or
  // invalidated while a write was out, until that answer lands, which may be after the writes
  // settle.
  let heldData = writing ? issueData() : new Map<string, unknown>();
  const judged = new Set<string>();
  // The tree and view reads begun while a write was out: each sends its first page only once the
  // last write out settles (`readTree`).
  const waiting = new Set<string>();
  // The tree and view reads out when a window focus while the live feed was down, or the feed back
  // with its gap replayed whole, asked for every refused one (`askRefused`): each is asked for
  // once more where it ends refused.
  const owedOnRefusal = new Set<string>();
  // Whether a window focus while the live feed was down came while a write was out: its ask for
  // a refused tree or view runs once the last write out settles (`unmutate`).
  let refusedHeld = false;
  // The turn of the event loop each read last began in, numbered as this subscription sees
  // turns, and the turn the refresh was queued in: its first change's, or, for a refresh held
  // back, the one the last write out settled in, where every read the writes held sends.
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
  // A read or an invalidation while a write was out is judged against the data the query held
  // when the first write of the hold went out, as deep as the answer goes: a rollback that puts
  // the data back by `setQueryData` keeps no object of what it puts back, and a read answering
  // the same is then no change.
  const answeredNewer = (query: Query): boolean => {
    const hash = query.queryHash;
    if ((began.get(hash) ?? 0) <= treeBegan) return false;
    let held: unknown;
    let now: unknown = query.state.data;
    if (judged.has(hash)) {
      if (!heldData.has(hash)) return readWhileInvalid.has(hash);
      held = heldData.get(hash);
      now = replaceEqualDeep(held, now);
    } else {
      if (readWhileInvalid.has(hash)) return true;
      if (!seen.has(hash)) return false;
      held = seen.get(hash);
    }
    return matchQuery(issueRead, query) ? !sameActs(held, now) : held !== now;
  };
  // Asks the scheduler for `targets`, each tree and view query among them first recorded as asked
  // for again (`askedAgain`), so a new observer may read it though its last read was refused. The
  // read that answers the ask clears the record as it begins: the scheduler's own where something
  // observes the query, and where nothing does, the one its next observer sends, since the
  // scheduler only marks stale a query nothing reads. What the call raises is the subscription's
  // own (`calling`): a read the scheduler begins in it, and the stale mark it puts on one it holds
  // back.
  const ask = (targets: readonly RefreshTarget[]): void => {
    for (const target of targets) {
      const query = target === tree || views.includes(target) ? cache.find(target) : undefined;
      if (query !== undefined) askedAgain.add(query);
    }
    calling = true;
    try {
      refreshQueries(queryClient, targets);
    } finally {
      calling = false;
    }
  };
  // The moment the scheduler begins a read of `query` at once: a second after its last read
  // began. For a read that began before this subscription did, its last answer, which came after
  // it began, stands in for that start.
  const dueOf = (query: Query): number =>
    (startedAt.get(query.queryHash) ??
      Math.max(query.state.dataUpdatedAt, query.state.errorUpdatedAt)) + MIN_REFETCH_INTERVAL_MS;
  // The moments the reads of the issue the scheduler owes are due at, each at or after `since`: a
  // query of the issue something observes, marked invalid, answered, with no read out, which the
  // scheduler holds back until a second after its last read began. A query in error is owed
  // nothing: nothing reads it until what reads it again comes. One due before `since` and not
  // begun is no read the scheduler holds back, since it begins at once a read that is due. The
  // decisions read this subscription holds itself is none either: asked for inside its own call,
  // it never counts as newer.
  const owedDues = (since: number): number[] =>
    issueQueries.flatMap((filters) =>
      cache.findAll(filters).flatMap((one) => {
        const { fetchStatus, isInvalidated, status } = one.state;
        const owed =
          one.isActive() &&
          isInvalidated &&
          fetchStatus === "idle" &&
          status === "success" &&
          !(heldAsks.has(decisions) && matchQuery(decisions, one));
        const due = dueOf(one);
        return owed && due >= since ? [due] : [];
      })
    );
  // Asks for a held read once the scheduler begins it at once, so the read begins inside this
  // subscription's own call; never while a read of it is out. The tree's waits besides until
  // every read of the issue the scheduler owes has begun: one that began after the tree's would
  // count as newer (`answeredNewer`), though the tree read answers the change it pays. Where one
  // due by the moment the timer fires has not begun yet - its timer is due in the same
  // millisecond - the ask waits once more, now counting only what is due from then.
  const arm = (target: RefreshTarget): void => {
    const heldAsk = heldAsks.get(target);
    if (heldAsk === undefined || heldAsk.timer !== undefined) return;
    const query = cache.find(target);
    if (query?.state.fetchStatus !== "idle") return;
    const owed = target === tree ? owedDues(heldAsk.since) : [];
    heldAsk.timer = window.setTimeout(
      () => {
        heldAsk.timer = undefined;
        if (heldAsks.get(target) !== heldAsk) return;
        const read = cache.find(target);
        if (
          read !== undefined &&
          (Date.now() < dueOf(read) || (target === tree && owedDues(heldAsk.since).length > 0))
        ) {
          heldAsk.since = Date.now();
          arm(target);
          return;
        }
        heldAsks.delete(target);
        ask([target]);
      },
      Math.max(0, Math.max(dueOf(query), ...owed) - Date.now())
    );
  };
  // Lets a held read go: a read of it began, which answers the refresh's ask whoever began it, or
  // the reader left, and the scheduler takes the ask (`hand`).
  const drop = (target: RefreshTarget, hand: boolean): void => {
    const heldAsk = heldAsks.get(target);
    if (heldAsk === undefined) return;
    window.clearTimeout(heldAsk.timer);
    heldAsks.delete(target);
    if (hand) ask([target]);
  };
  const refresh = (): void => {
    queued = false;
    if (stopped) return;
    // No event says when a write has reached Dispatch, and a tree read made while one is out
    // reads a tree Dispatch may not have changed yet, so no refresh begins until the last write
    // out settles, with the live feed up or down. A write the client holds names no issue, so
    // any write out holds the refresh back. It is held before it takes the decisions read's own
    // invalidation, which so still counts, and every change heard meanwhile is the one refresh
    // it runs.
    if (writing) {
      heldBack = true;
      return;
    }
    const decisionsOwed = decisionsInvalidated;
    decisionsInvalidated = false;
    // A read begun in the turn the change was seen in answers it already, since what a turn
    // hears of changed before the turn began and the read reaches the server after it: a
    // whole-cache refresh reads the tree and the open view itself, and they are not asked again.
    // A tree or view read the writes held counts as begun in the turn they settled in, which is
    // the held refresh's (`unmutate`).
    const targets = [tree, ...(decisionsOwed ? [] : [decisions]), ...views].filter((target) => {
      const query = cache.find(target);
      return query === undefined || beganIn.get(query.queryHash) !== changedIn;
    });
    // The tree read and the decisions read the scheduler would hold back - the last read began
    // under a second ago, or one is out - are held here instead: marked stale now, as the
    // scheduler marks a read it owes, and asked for once the scheduler begins them at once
    // (`arm`). The tree's is held so only where a read of the issue the scheduler owes is still
    // to begin, which is what its wait is for; elsewhere the scheduler holds it as it would. A
    // change heard while one is held joins that one held ask.
    const holds = [tree, decisions].filter((target) => {
      const read = targets.includes(target) ? cache.find(target) : undefined;
      if (read === undefined || !read.isActive()) return false;
      if (heldAsks.has(target)) return true;
      const heldByScheduler = read.state.fetchStatus !== "idle" || Date.now() < dueOf(read);
      return heldByScheduler && (target !== tree || owedDues(Date.now()).length > 0);
    });
    ask(targets.filter((target) => !holds.includes(target)));
    for (const target of holds) {
      if (!heldAsks.has(target)) heldAsks.set(target, { since: Date.now(), timer: undefined });
      calling = true;
      try {
        cache.find(target)?.invalidate();
      } finally {
        calling = false;
      }
      arm(target);
    }
  };
  // Queues one refresh for a change seen now, which every change of this turn joins.
  const queue = (): void => {
    if (queued) return;
    queued = true;
    changedIn = turnNow();
    queueMicrotask(refresh);
  };
  const unsubscribe = cache.subscribe((event) => {
    const { query } = event;
    const hash = query.queryHash;
    if (event.type === "removed") {
      began.delete(hash);
      beganIn.delete(hash);
      readWhileInvalid.delete(hash);
      startedAt.delete(hash);
      judged.delete(hash);
      heldData.delete(hash);
      waiting.delete(hash);
      owedOnRefusal.delete(hash);
      if (matchQuery(tree, query)) watcher.release();
      return;
    }
    if (event.type !== "updated") return;
    const { action } = event;
    if (matchQuery({ queryKey: issueTreeKey(key) }, query)) {
      if (action.type !== "fetch") {
        // A read a focus or the feed's return found out has ended: where it ended refused it is
        // asked for once more, outside this notification, since the cache is still dispatching
        // the action that ended it.
        if (owedOnRefusal.has(hash) && query.state.fetchStatus === "idle") {
          owedOnRefusal.delete(hash);
          const target = [tree, ...views].find((one) => matchQuery(one, query));
          if (action.type === "error" && target !== undefined) {
            queueMicrotask(() => {
              if (!stopped) ask([target]);
            });
          }
        }
        // The held tree read is asked for once no read of it is out.
        if (heldAsks.has(tree) && matchQuery(tree, query)) arm(tree);
        return;
      }
      // A read begins, which answers whatever asked for the tree or the view again. One begun
      // while a write is out, as `readTree` finds the writes, sends once the last of them settles.
      askedAgain.delete(query);
      const now = turnNow();
      beganIn.set(hash, now);
      if (queryClient.isMutating() > 0) waiting.add(hash);
      else waiting.delete(hash);
      if (!matchQuery(tree, query)) return;
      recordStart(hash);
      // A read of the tree begun since a refresh held its ask answers that ask, whoever began it.
      if (!calling) drop(tree, false);
      treeBegan = now;
      seen = issueData();
      return;
    }
    if (!issueQueries.some((filters) => matchQuery(filters, query))) return;
    const isDecisions = matchQuery(decisions, query);
    if (action.type === "fetch") {
      recordStart(hash);
      // A read of the decisions begun since a refresh asked for it answers that ask, whoever
      // began it, as it pays a refresh the scheduler owes; it is no read of the refresh's own.
      if (isDecisions && !calling) drop(decisions, false);
      const own = calling || focusing;
      const now = turnNow();
      began.set(hash, own ? 0 : now);
      beganIn.set(hash, now);
      // The fetch has not cleared the mark: only the answer's success does.
      if (!own && query.state.isInvalidated) readWhileInvalid.add(hash);
      else readWhileInvalid.delete(hash);
      if (writing) judged.add(hash);
      return;
    }
    // The held decisions read is asked for once no read of it is out.
    if (isDecisions) arm(decisions);
    if (calling || (action.type !== "invalidate" && action.type !== "success")) return;
    let changed: boolean;
    if (action.type === "invalidate") {
      // While a write is out an invalidation is no change by itself: the query's next answer
      // says (`answeredNewer`), but for a query nothing observes, which is not read again. The
      // decisions read's own invalidation leaves its read out or owed either way.
      if (isDecisions) decisionsInvalidated = true;
      if (writing) judged.add(hash);
      changed = !writing || !query.isActive();
    } else {
      // A write the page put in the cache reads the tree only while the live feed is down: while
      // it is up, the write's event does.
      changed = action.manual === true ? !liveFeedUp(queryClient) : answeredNewer(query);
      if (action.manual !== true) judged.delete(hash);
    }
    if (changed) queue();
  });
  // Asks again for the tree and each view whose read was refused and is not out: a window focus
  // while the live feed is down does, once no write is out, and the feed coming back with its gap
  // replayed whole. A refused view whose filter is off then is read when its filter is next on,
  // since the ask records it as asked for again (`ask`). A read out then is owed
  // (`owedOnRefusal`), and asked for once more where it ends refused.
  const askRefused = (): void => {
    const refused: RefreshTarget[] = [];
    for (const target of [tree, ...views]) {
      const query = cache.find(target);
      if (query === undefined) continue;
      if (query.state.fetchStatus !== "idle") owedOnRefusal.add(query.queryHash);
      else if (query.state.status === "error") refused.push(target);
    }
    if (refused.length > 0) ask(refused);
  };
  // A window focus while the live feed is down reads a refused tree or view again, or, while a
  // write is out, once the last write out settles (`refusedHeld`), since a read made before then
  // reads a tree Dispatch has not changed yet; while the feed is up it reads neither, and what
  // the reads of the issue it begins answer is no change.
  const unfocus = focusManager.subscribe((focused) => {
    if (!focused || stopped) return;
    if (liveFeedUp(queryClient)) {
      focusing = true;
      setTimeout(() => {
        focusing = false;
      }, 0);
      return;
    }
    if (writing) refusedHeld = true;
    else askRefused();
  });
  // The live feed coming back after a gap with the gap replayed whole reads a refused tree or
  // view again: no replayed event need name the issue. After an incomplete replay the event feed's
  // refresh of every query reads each refused one something observes, and an ask here as well
  // would read it twice; one nothing observes that refresh only marks stale, so the subscription
  // asks for it, which reads nothing and records it as asked for again (`ask`) for its next
  // observer, as no change of the issue does while a write is out.
  const unfeed = onLiveFeedBack(queryClient, (replayComplete) => {
    if (replayComplete) {
      askRefused();
      return;
    }
    const unobserved = [tree, ...views].filter((target) => {
      const query = cache.find(target);
      return query?.state.status === "error" && !query.isActive();
    });
    if (unobserved.length > 0) ask(unobserved);
  });
  // The hold: the first write going out while none was begins it, with the data each query of
  // the issue then holds (`heldData`); the last write out settling, answered or refused, ends it.
  // Each tree and view read the writes held sends its first page then (`readTree`), so it counts
  // as begun in this turn: the held refresh asks for none of them again, and a read of the issue
  // begun while the writes were out counts as older than the tree's. What was held back runs
  // then: a focus's ask first, then the refresh, which asks for no read the ask began in this
  // same turn (`changedIn`), so no read is asked twice. What was heard while the writes were out
  // is still to be read. A refresh a change queued that has not run yet runs as the held one, in
  // this turn, and so reads again none of the reads the writes held. The focus's ask runs only
  // where the live feed is still down: where it came back meanwhile, its return read a refused
  // tree or view already - at once after a whole replay, through the event feed's refresh of
  // every query after an incomplete one - and a focus while the feed is up reads none.
  const unmutate = queryClient.getMutationCache().subscribe(() => {
    const out = queryClient.isMutating() > 0;
    if (out === writing) return;
    writing = out;
    if (writing) {
      heldData = issueData();
      return;
    }
    const settled = turnNow();
    for (const hash of waiting) {
      const query = cache.get(hash);
      if (query === undefined || query.state.fetchStatus === "idle") continue;
      beganIn.set(hash, settled);
      if (!matchQuery(tree, query)) continue;
      treeBegan = settled;
      seen = issueData();
    }
    waiting.clear();
    for (const hash of heldData.keys()) {
      if (!judged.has(hash)) heldData.delete(hash);
    }
    if (refusedHeld) {
      refusedHeld = false;
      if (!liveFeedUp(queryClient)) askRefused();
    }
    if (heldBack || queued) {
      heldBack = false;
      queue();
      changedIn = settled;
    } else {
      decisionsInvalidated = false;
    }
  });
  const watcher: TreeFreshness = {
    holders: 0,
    release: () => {
      if (stopped) return;
      // The reader left: each read held for a refresh goes to the scheduler.
      if (watcher.holders === 0) for (const target of [...heldAsks.keys()]) drop(target, true);
      if (watcher.holders > 0 || cache.find(tree) !== undefined) return;
      stopped = true;
      unsubscribe();
      unfocus();
      unfeed();
      unmutate();
      byKey.delete(key);
    },
  };
  byKey.set(key, watcher);
  return watcher;
}
