import {
  matchQuery,
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
import type { ArtifactBlock, IssueDetails } from "../../../api/types";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { blocksOfRole } from "./tree-nodes";

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

const TREE_FILTERS: readonly TreeFilter[] = ["spec", "verification", "plan"];

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

/**
 * The pages of one read as one answer: every page's nodes and tuples in the order the pages
 * came, so the walk's order holds across them, and what describes the issue rather than a page
 * (`converted`, `tree`, `view_of`, `documents`) from the first. The answer has no next page.
 * One page is its own answer, which is every issue the page size was set above.
 */
export function mergeTreePages(pages: readonly [TreeAnswer, ...TreeAnswer[]]): TreeAnswer {
  const [first] = pages;
  if (pages.length === 1) return first;
  return {
    ...first,
    cursor: null,
    nodes: pages.flatMap((page) => page.nodes),
    tuples: pages.flatMap((page) => page.tuples),
  };
}

/**
 * Every page of the tree read, or of one view, asked for in turn: the first, then each next page
 * the page before names (`cursor`), and the pages merged once the last answers, so the tree is
 * built once and never from part of the walk. A page that fails fails the read, and a read made
 * again - a refetch, a retry - starts from the first page, since a cursor names a place in the
 * walk as it stood when the page that gave it was answered.
 */
async function readTree(key: string, view?: TreeFilter): Promise<TreeAnswer> {
  const first = await api.getIssueTree(key, { view });
  const pages: [TreeAnswer, ...TreeAnswer[]] = [first];
  let cursor = first.cursor;
  while (cursor !== null) {
    const page = await api.getIssueTree(key, { cursor, view });
    pages.push(page);
    cursor = page.cursor;
  }
  return mergeTreePages(pages);
}

/** The issue's whole tree, one query over every page (`readTree`). */
export const issueTreeQuery = (key: string) =>
  queryOptions({
    queryKey: issueTreeKey(key),
    queryFn: () => readTree(key),
  });

/** One view of the issue's tree, one query over every page (`readTree`). A plain query rather
 *  than one per page, so an observer that is never enabled can watch a view a slot read. */
export const viewQuery = (key: string, filter: TreeFilter) =>
  queryOptions({
    queryKey: viewKey(key, filter),
    queryFn: () => readTree(key, filter),
  });

/** One role's blocks from the tree read, answered as the blocks read of that role's document
 *  answered them (`artifactBlocksQuery`), so a caller's lines that read these members stay as
 *  they are. The state is the tree read's own. */
export interface TreeBlocksRead {
  readonly data: ArtifactBlock[] | undefined;
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
  /** Whether the read answered every page. Until then a typed block may be missing because it
   *  has not arrived rather than because the issue holds none, and a read that failed has not
   *  answered either: the page cannot say what a read it could not make holds. */
  readonly blocksRead: boolean;
  /** The typed blocks of the document that plays `role` (`blocksOfRole`), the same value for a
   *  role until the answer or the read's state changes. */
  readonly blocksOf: (role: TreeFilter) => TreeBlocksRead;
}

/**
 * The tree read alone, for the issue page's model (`useIssueTree`) and for a surface that needs
 * the blocks of a role document and none of the model's other reads. Each value it answers keeps
 * its identity while the read's answer and state stand, so a caller's memo built on one is not
 * built again on every render.
 */
export function useTreeRead(issue: IssueDetails): TreeRead {
  const { data: answer, isError, isSuccess, refetch } = useQuery(issueTreeQuery(issue.key));
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
    }),
    [answer, isSuccess, reads]
  );
}

/**
 * Keeps the tree read and its views as fresh as the issue's other reads, which no write
 * reaches directly: their keys are not under `["issue", key]` (`issueTreeKey`). Whenever a query
 * of the issue (`["issue", key, …]`, `["asks", key, …]`, `["comments", key, …]`,
 * `["issue-messages", key, …]` and the decisions read's key) is invalidated - a page write's
 * `invalidateQueries`, and an event's own refresh (`api/query-refresh.ts`' `owe`) - or written
 * by `setQueryData` - a Keep and a status change write that way (TanStack's `success` action
 * with `manual`) - it reads the tree again, drops every view, so a held one is gone and the one
 * a slot has on is read again, and invalidates the decisions read, so it is read again where
 * something observes it. Every such change of one turn is one refresh, made in the microtask
 * after it.
 *
 * The tree's and the views' own keys are no query of the issue, and what its own calls raise is
 * ignored (`calling`), so a refresh never starts another. Nothing is refreshed before the tree
 * read first holds data: the reads that land with the first load are not changes, and a refetch
 * then would only start the first read over. Answers the unsubscribe.
 */
export function subscribeTreeFreshness(queryClient: QueryClient, key: string): () => void {
  const decisionsKey = issueDecisionsQuery(key, true).queryKey;
  const issueQueries: readonly QueryFilters[] = [
    { queryKey: ["issue", key] },
    { queryKey: ["asks", key] },
    { queryKey: ["comments", key] },
    { queryKey: ["issue-messages", key] },
    { exact: true, queryKey: decisionsKey },
  ];
  let calling = false;
  let queued = false;
  let stopped = false;
  const refresh = (): void => {
    queued = false;
    if (stopped) return;
    calling = true;
    try {
      void queryClient.invalidateQueries({ exact: true, queryKey: issueTreeKey(key) });
      for (const filter of TREE_FILTERS) {
        void queryClient.resetQueries({ exact: true, queryKey: viewKey(key, filter) });
      }
      void queryClient.invalidateQueries({ exact: true, queryKey: decisionsKey });
    } finally {
      calling = false;
    }
  };
  const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
    if (calling || queued || event.type !== "updated") return;
    const { action, query } = event;
    const changed =
      action.type === "invalidate" || (action.type === "success" && action.manual === true);
    if (!changed || !issueQueries.some((filters) => matchQuery(filters, query))) return;
    if (queryClient.getQueryData(issueTreeKey(key)) === undefined) return;
    queued = true;
    queueMicrotask(refresh);
  });
  return () => {
    stopped = true;
    unsubscribe();
  };
}
