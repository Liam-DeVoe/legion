import {
  matchQuery,
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
import { refreshQueries } from "../../../api/query-refresh";
import type { IssueDetails } from "../../../api/types";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { blocksOfRole, mergeTreePages, type TreeBlock } from "./tree-nodes";

/**
 * The issue's tree read (`GET /api/v1/issues/{key}/tree`, dispatch://INFRA-1253 R8): the one
 * file of the issue page that names the route. Every other file reads the answer through the
 * names below, so the day the answer's shape moves onto the generated contract (INFRA-4062's
 * task t11) only these aliases change.
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
 *  issue's key by prefix does not reach it, and `subscribeTreeFreshness` is the one path that
 *  refreshes it: a page that reads the tree alone after a write of its own invalidates this key,
 *  `exact`, since every view's key is under it. */
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
 *  the one an observer has enabled is read again. */
export function dropViews(queryClient: QueryClient, key: string): void {
  for (const filter of TREE_FILTERS) {
    void queryClient.resetQueries({ exact: true, queryKey: viewKey(key, filter) });
  }
}

/**
 * Every page of the tree read, or of one view, asked for in turn: the first, then each next page
 * the page before names (`cursor`), and the pages merged once the last answers, so the tree is
 * built once and never from part of the walk. A page that fails fails the read, and a read made
 * again - a refetch, a retry - starts from the first page, since a cursor names a place in the
 * walk as it stood when the page that gave it was answered. A read TanStack gives up (`signal`:
 * a reset, the last observer gone) ends its page in flight and asks for no next one.
 */
async function readTree(
  key: string,
  view: TreeFilter | undefined,
  signal: AbortSignal
): Promise<TreeAnswer> {
  const first = await api.getIssueTree(key, { view }, { signal });
  const pages: [TreeAnswer, ...TreeAnswer[]] = [first];
  let cursor = first.cursor;
  while (cursor !== null) {
    signal.throwIfAborted();
    const page = await api.getIssueTree(key, { cursor, view }, { signal });
    pages.push(page);
    cursor = page.cursor;
  }
  return mergeTreePages(pages);
}

/**
 * Window focus reads a query again only while its last answer failed, so a read that failed
 * recovers on the open page while a healthy one stays quiet: a local copy of the private
 * `refetchFailedOnFocus` of `features/refs/reference-target.ts`, until part F's export of it
 * (F's spec R12) is under T's base, when T imports F's (dispatch://INFRA-1196 decision 94).
 */
export const refetchFailedOnFocus = (query: {
  readonly state: { readonly status: QueryStatus };
}) => (query.state.status === "error" ? "always" : false);

/**
 * How the page's reads are kept fresh (dispatch://INFRA-1196 decision 180): by the events that
 * can change them, never by age, so a slot the stage mounts again past the app's 30 s
 * `staleTime` sends no read; only a read whose last answer failed is read again on focus. The
 * tree read and its views take it, and so does every read of `useIssueTree` that an event
 * refreshes.
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
export const viewQuery = (key: string, filter: TreeFilter) =>
  queryOptions({
    ...REFRESHED_BY_EVENTS,
    queryKey: viewKey(key, filter),
    queryFn: ({ signal }) => readTree(key, filter, signal),
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
   *  on `blocksRead` would blank, since that failure turns it false (dispatch://INFRA-1196
   *  decision 98). Both are false before the first answer and after a first read that fails. */
  readonly blocksHeld: boolean;
  /** Whether the read answered every page. Until then a typed block - the spec's summary among
   *  them, which the head waits on before it says a spec has none (`IssueThreeThings.tsx`) - may
   *  be missing because it has not arrived rather than because the issue holds none, and a read
   *  that failed has not answered either: the page cannot say what a read it could not make
   *  holds. */
  readonly blocksRead: boolean;
  /** The typed blocks of the document that plays `role` (`blocksOfRole`, dispatch://INFRA-4062
   *  R5), so a view reads its blocks here and costs no request of its own; the same value for a
   *  role until the answer or the read's state changes. */
  readonly blocksOf: (role: TreeFilter) => TreeBlocksRead;
  /** Whether the read has neither answered nor failed yet: what holds back a read that waits on
   *  every other read of the page, as the batched name read does (`useIssueTree`). */
  readonly pending: boolean;
}

/**
 * The tree read alone, for the issue page's model (`useIssueTree`) and for a surface that needs
 * the blocks of a role document and none of the model's other reads. Each value it answers keeps
 * its identity while the read's answer and state stand, so a caller's memo built on one is not
 * built again on every render.
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
    () => ({
      plan: blocksOfRole(answer, "plan", issue),
      spec: blocksOfRole(answer, "spec", issue),
      verification: blocksOfRole(answer, "verification", issue),
    }),
    [answer, issue]
  );
  const reads = useMemo(
    (): Readonly<Record<TreeFilter, TreeBlocksRead>> => ({
      plan: { data: blocks.plan, isError, isSuccess, refetch },
      spec: { data: blocks.spec, isError, isSuccess, refetch },
      verification: { data: blocks.verification, isError, isSuccess, refetch },
    }),
    [blocks, isError, isSuccess, refetch]
  );
  return useMemo(
    () => ({
      answer,
      blocksHeld: answer !== undefined,
      blocksRead: isSuccess,
      blocksOf: (role: TreeFilter) => reads[role],
      pending: isPending,
    }),
    [answer, isPending, isSuccess, reads]
  );
}

/**
 * Keeps the tree read and its views as fresh as the issue's other reads, which no write
 * reaches directly: their keys are not under `["issue", key]` (`issueTreeKey`). Whenever a query
 * of the issue (`["issue", key, …]`, `["asks", key, …]`, `["comments", key, …]`,
 * `["issue-messages", key, …]` and the decisions read's key) is invalidated - a page write's
 * `invalidateQueries`, and an event's own refresh (`api/query-refresh.ts`' `owe`) - or written
 * by `setQueryData` - a Keep and a status change write that way (TanStack's `success` action
 * with `manual`), so such a write refreshes the tree while the live feed is down too - it reads
 * the tree again, drops every view, so a held one is gone and the one a slot has on is read
 * again, and reads the decisions again where something observes them. Every such change of one
 * turn is one refresh, made in the microtask after it.
 *
 * The tree and the decisions are read again through the app's refresh scheduler
 * (`refreshQueries`), which never cancels a read in flight - a walk of many pages is not started
 * over by each event of a burst - and starts one at most once a second; so a write and the event
 * it raises read the tree once or twice, never more. Where the decisions read's own invalidation
 * was one of the turn's changes, its read is already out or owed, and is not asked for again.
 *
 * The tree's and the views' own keys are no query of the issue, and what its own calls raise is
 * ignored (`calling`), so a refresh never starts another. Nothing is refreshed before the tree
 * read first holds data: the reads that land with the first load are not changes, and a refetch
 * then would only start the first read over. Answers the unsubscribe.
 */
export function subscribeTreeFreshness(queryClient: QueryClient, key: string): () => void {
  const decisionsKey = issueDecisionsQuery(key, true).queryKey;
  const decisions: QueryFilters = { exact: true, queryKey: decisionsKey };
  const issueQueries: readonly QueryFilters[] = [
    { queryKey: ["issue", key] },
    { queryKey: ["asks", key] },
    { queryKey: ["comments", key] },
    { queryKey: ["issue-messages", key] },
    decisions,
  ];
  let calling = false;
  let queued = false;
  let stopped = false;
  // Whether the decisions read's own invalidation is one of this turn's changes.
  let decisionsInvalidated = false;
  const refresh = (): void => {
    queued = false;
    const decisionsOwed = decisionsInvalidated;
    decisionsInvalidated = false;
    if (stopped) return;
    calling = true;
    try {
      refreshQueries(queryClient, [
        { exact: true, queryKey: issueTreeKey(key) },
        ...(decisionsOwed ? [] : [{ exact: true, queryKey: decisionsKey }]),
      ]);
      dropViews(queryClient, key);
    } finally {
      calling = false;
    }
  };
  const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
    if (calling || event.type !== "updated") return;
    const { action, query } = event;
    const changed =
      action.type === "invalidate" || (action.type === "success" && action.manual === true);
    if (!changed || !issueQueries.some((filters) => matchQuery(filters, query))) return;
    if (queryClient.getQueryData(issueTreeKey(key)) === undefined) return;
    if (action.type === "invalidate" && matchQuery(decisions, query)) decisionsInvalidated = true;
    if (queued) return;
    queued = true;
    queueMicrotask(refresh);
  });
  return () => {
    stopped = true;
    unsubscribe();
  };
}
