import {
  matchQuery,
  type Query,
  type QueryClient,
  type QueryFilters,
  type QueryStatus,
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
import { type RefreshTarget, refreshQueries } from "../../../api/query-refresh";
import type { IssueDetails } from "../../../api/types";
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

/** Every view of the tree a read asks for (`?view=`): a role document's (`TreeFilter`), and the
 *  decisions view, the issue's decisions wherever they are written. That view plays no role, so
 *  `blocksOf` takes no such name. */
export type TreeView = TreeFilter | "decisions";

/** The views of the tree the filters read, one for each filter, in this order: Spec's,
 *  Verification's and Plan's role documents, and the issue's decisions wherever they are
 *  written (`?view=decisions`). */
export const TREE_VIEWS: readonly TreeView[] = ["spec", "verification", "plan", "decisions"];

/** The tree read's key. It is not under `["issue", key]`, so a write that invalidates the
 *  issue's key by prefix does not reach it, and `subscribeTreeFreshness` is the one path that
 *  refreshes it: a page that reads the tree alone after a write of its own invalidates this key,
 *  `exact`, since every view's key is under it. */
export function issueTreeKey(key: string): readonly ["issue-tree", string] {
  return ["issue-tree", key];
}

/** One view's key: under the tree's, and refreshed by `subscribeTreeFreshness` alone too. */
export function viewKey(key: string, view: TreeView): readonly ["issue-tree", string, TreeView] {
  return ["issue-tree", key, view];
}

/** Drops every view of issue `key` the cache holds (`resetQueries`), so a held one is gone and
 *  the one an observer has enabled is read again: what a new load of the issue does, so it draws
 *  no view older than its own tree. A write never drops them (`subscribeTreeFreshness`). */
export function dropViews(queryClient: QueryClient, key: string): void {
  for (const view of TREE_VIEWS) {
    void queryClient.resetQueries({ exact: true, queryKey: viewKey(key, view) });
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
  view: TreeView | undefined,
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
 * Window focus reads a query again only while its last answer failed, so a read that failed
 * recovers on the open page while a healthy one stays quiet. It is a copy of the private
 * `refetchFailedOnFocus` of `features/refs/reference-target.ts`, until that file exports it and
 * this one imports it from there.
 */
export const refetchFailedOnFocus = (query: {
  readonly state: { readonly status: QueryStatus };
}) => (query.state.status === "error" ? "always" : false);

/**
 * How the page's reads are kept fresh: by the events that can change them, never by age, so a
 * slot the stage mounts again past the app's 30 s `staleTime` sends no read; only a read whose
 * last answer failed is read again on focus. The tree read and its views take it, and so does
 * every read of `useIssueTree` that an event refreshes. It is a copy of `EVENT_FRESH`, which a
 * later commit of the code this branch is built on adds to `useIssueTree.ts`; the two become one
 * export once that commit is under this branch.
 */
export const REFRESHED_BY_EVENTS = {
  refetchOnWindowFocus: refetchFailedOnFocus,
  staleTime: Number.POSITIVE_INFINITY,
} as const;

/** The issue's whole tree, one query over every page (`readTree`). */
export const issueTreeQuery = (key: string) =>
  queryOptions({
    ...REFRESHED_BY_EVENTS,
    queryKey: issueTreeKey(key),
    queryFn: ({ signal }) => readTree(key, undefined, signal),
  });

/** One view of the issue's tree, one query over every page (`readTree`). A plain query rather
 *  than one per page, so an observer that is never enabled can watch a view a slot read. */
export const viewQuery = (key: string, view: TreeView) =>
  queryOptions({
    ...REFRESHED_BY_EVENTS,
    queryKey: viewKey(key, view),
    queryFn: ({ signal }) => readTree(key, view, signal),
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
  /** Whether the read answered every page. Until then a typed block - the spec's summary among
   *  them, which the head waits on before it says a spec has none (`IssueThreeThings.tsx`) - may
   *  be missing because it has not arrived rather than because the issue holds none, and a read
   *  that failed has not answered either: the page cannot say what a read it could not make
   *  holds. */
  readonly blocksRead: boolean;
  /** The typed blocks of the document that plays `role` (`blocksOfRole`), so a view reads its
   *  blocks here and costs no request of its own; the same value for a role until the answer or
   *  the read's state changes. */
  readonly blocksOf: (role: TreeFilter) => TreeBlocksRead;
  /** Whether the read failed with nothing held: refused before it first answered, its retries
   *  spent. A refetch that fails over a held answer is no such failure, since the page still
   *  draws what it holds. */
  readonly failed: boolean;
  /** Whether the read has neither answered nor failed yet: what holds back a read that waits on
   *  every other read of the page, as the batched name read does (`useIssueTree`). */
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
      failed: isError && answer === undefined,
      pending: isPending,
      refetch,
    }),
    [answer, blocksOf, isError, isPending, isSuccess, refetch]
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
 * issue (`["issue", key, …]`, `["asks", key, …]`, `["comments", key, …]` and
 * `["issue-messages", key, …]`) has changed when it is:
 *
 * - invalidated: a page write's `invalidateQueries`, and an event's own refresh
 *   (`api/query-refresh.ts`' `owe`), whose targets name `["issue", key]` for every event of the
 *   issue, a Keep's included (`api/event-refresh.ts`);
 * - written by `setQueryData`: a status change writes that way (TanStack's `success` action with
 *   `manual`), so such a write refreshes the tree while the live feed is down too;
 * - answered, by a read that began after the tree's last read began, with data other than it
 *   held when that tree read began (`answeredNewer`). TanStack reports an invalidation only when
 *   a query turns invalid, so a write whose invalidation lands while the query is still invalid -
 *   its read for an earlier write still out, or its last read failed - raises no event, and the
 *   read made for it, which the app's client sends once the one out settles, is how that write
 *   shows. A read that answers what the query held, as one made again for its age does, changes
 *   nothing, and a read that began before the tree's is one the tree read answers already.
 *
 * On any of them it reads the tree again and marks every held view stale, the decisions view
 * among them, which reads the open view again while each view keeps the answer it holds, and
 * drawn, until its new one lands. Every change of one turn is one refresh, made in the microtask
 * after it.
 *
 * All of it goes through the app's refresh scheduler (`refreshQueries`), which never cancels a
 * read in flight - a walk of many pages is not started over by each event of a burst - reads a
 * query that is out once more when it settles, and starts one at most once a second. So a change
 * while the tree's first read is out, or after that read failed, is one more read once it
 * settles; a write and the event it raises read the tree once or twice, never more. A read that
 * began in the turn the change was seen in is not asked for again, since it answers the change
 * already, as a refresh of every query the page holds reads the tree and the open view itself.
 * The tree's and the views' own keys are no query of the issue, and what the refresh's own calls
 * raise is ignored (`calling`), so a refresh never starts another.
 *
 * It is one subscription for each client and issue, which outlives the stage that asks for it:
 * kept while a stage holds it, and after, for as long as the cache holds the issue's tree. A
 * change made while no stage of the issue is mounted - the event of another person's write while
 * the reader is elsewhere - marks the tree stale, as the scheduler marks any query nothing draws,
 * so the tree is read again when the reader comes back; and a refresh that lands after the stage
 * went sends nothing. Answers the release of this stage's hold.
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

/** The subscription of issue `key`, entered in `byKey` until it ends. */
function watchTree(
  queryClient: QueryClient,
  key: string,
  byKey: Map<string, TreeFreshness>
): TreeFreshness {
  const cache = queryClient.getQueryCache();
  const tree: RefreshTarget = { exact: true, queryKey: issueTreeKey(key) };
  const issueQueries: readonly QueryFilters[] = [
    { queryKey: ["issue", key] },
    { queryKey: ["asks", key] },
    { queryKey: ["comments", key] },
    { queryKey: ["issue-messages", key] },
  ];
  const views: readonly RefreshTarget[] = TREE_VIEWS.map((view) => ({
    exact: true,
    queryKey: viewKey(key, view),
  }));
  let calling = false;
  let queued = false;
  let stopped = false;
  // The order reads began in, as this subscription saw them begin: the tree's last read, and
  // each query of the issue's last read, where one the refresh itself sent began with the tree's.
  let reads = 0;
  let treeBegan = 0;
  const began = new Map<string, number>();
  // The data each query of the issue held when the tree's last read began.
  let seen = new Map<string, unknown>();
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
  const answeredNewer = (query: Query): boolean => {
    const hash = query.queryHash;
    return (
      (began.get(hash) ?? 0) > treeBegan && seen.has(hash) && seen.get(hash) !== query.state.data
    );
  };
  const refresh = (): void => {
    queued = false;
    if (stopped) return;
    // A read begun in the turn the change was seen in answers it already, since what a turn
    // hears of changed before the turn began and the read reaches the server after it: a
    // whole-cache refresh reads the tree and the open view itself, and they are not asked again.
    const targets = [tree, ...views].filter((target) => {
      const query = cache.find(target);
      return query === undefined || beganIn.get(query.queryHash) !== changedIn;
    });
    calling = true;
    try {
      refreshQueries(queryClient, targets);
    } finally {
      calling = false;
    }
  };
  const unsubscribe = cache.subscribe((event) => {
    const { query } = event;
    if (event.type === "removed") {
      began.delete(query.queryHash);
      beganIn.delete(query.queryHash);
      if (matchQuery(tree, query)) watcher.release();
      return;
    }
    if (event.type !== "updated") return;
    const { action } = event;
    if (matchQuery({ queryKey: issueTreeKey(key) }, query)) {
      if (action.type !== "fetch") return;
      reads += 1;
      beganIn.set(query.queryHash, turnNow());
      if (!matchQuery(tree, query)) return;
      treeBegan = reads;
      seen = new Map();
      for (const filters of issueQueries) {
        for (const one of cache.findAll(filters)) {
          if (one.state.data !== undefined) seen.set(one.queryHash, one.state.data);
        }
      }
      return;
    }
    if (!issueQueries.some((filters) => matchQuery(filters, query))) return;
    if (action.type === "fetch") {
      reads += 1;
      began.set(query.queryHash, calling ? 0 : reads);
      beganIn.set(query.queryHash, turnNow());
      return;
    }
    if (calling) return;
    const changed =
      action.type === "invalidate" ||
      (action.type === "success" && (action.manual === true || answeredNewer(query)));
    if (!changed) return;
    if (queued) return;
    queued = true;
    changedIn = turnNow();
    queueMicrotask(refresh);
  });
  const watcher: TreeFreshness = {
    holders: 0,
    release: () => {
      if (stopped || watcher.holders > 0 || cache.find(tree) !== undefined) return;
      stopped = true;
      unsubscribe();
      byKey.delete(key);
    },
  };
  byKey.set(key, watcher);
  return watcher;
}
