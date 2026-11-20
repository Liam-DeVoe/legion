import {
  notifyManager,
  type QueryFilters,
  type QueryObserverResult,
  useIsFetching,
  useQueries,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useMemo, useSyncExternalStore } from "react";

import { api } from "../../api/client";
import { primarySpec, roleDocument } from "../../api/issue-cache";
import {
  artifactBlocksQuery,
  issueCoverageQuery,
  issueResultsQuery,
  referenceTargetsQuery,
} from "../../api/queries";
import type { IssueDetails } from "../../api/types";
import { issueDecisionsQuery } from "../doc/spec-marks";
import { refetchFailedOnFocus } from "../refs/reference-target";
import type { IssueTreeReads, TreeNode } from "./issue-tree";
import { ANSWER_LINES } from "./page-words";
import { issueAsksQuery, treeDocumentIds, useIssueTree } from "./useIssueTree";

/** What each read the outline waits on takes: a copy of the tree's own rule for its reads,
 *  `useIssueTree.ts`'s `EVENT_FRESH`, which this file is to import once that file exports it.
 *  Events keep a read fresh and its age never does, so the outline's slot mounted again on a
 *  press of a view more than 30 s after the load reads none of them again, and window focus
 *  fetches again only a read in error. */
const EVENT_FRESH = {
  refetchOnWindowFocus: refetchFailedOnFocus,
  staleTime: Number.POSITIVE_INFINITY,
} as const;

/** One read the outline's rows or their names come from that stands failed, as the outline
 *  draws it: one line over the tree (`FailedReadLine`), with a Retry that asks that read alone
 *  again. */
export interface FailedRead {
  /** How many times the read has failed (TanStack Query's `errorUpdateCount`), which keys the
   *  line's alert, so a second failure is said again. */
  readonly failure: number;
  /** Tells the read from every other read of the outline: its line's React key. */
  readonly key: string;
  /** What the outline says of the read, in the page's words (`ANSWER_LINES`). */
  readonly message: string;
  /** Whether rows come from the read, as they come from every read here but the names': the
   *  page's first focus goes to the first such line where one stands failed as the reads
   *  settle (`IssueTree`'s opening), and the names place no row. */
  readonly placesRows: boolean;
  /** Asks that read alone again. */
  readonly retry: () => void;
  /** Whether the read runs again (TanStack Query's `isFetching`). */
  readonly retrying: boolean;
}

/** What the outline's reads give (`useOutlineReads`). Each field is one value for as long as the
 *  reads say the same; the object holding them is new on every render. */
export interface OutlineReads {
  /** Each read the tree's rows or their names come from that stands failed, in the order the
   *  reads are given: the six reads below, each document's blocks (`treeDocumentIds`), and the
   *  names last, as the tree reads them last (`useIssueTree`). Empty while none does. */
  readonly failed: readonly FailedRead[];
  /** The reads of the issue's conversation the tree's turns come from, each undefined until it
   *  has answered, and whether each of the six reads the tree's other nodes come from - the
   *  coverage, the decisions, the results, the asks, the comments and the messages - holds its
   *  answer (`held`). */
  readonly reads: {
    readonly asks: IssueTreeReads["asks"];
    readonly comments: IssueTreeReads["comments"];
    readonly held: boolean;
    readonly messages: IssueTreeReads["messages"];
  };
  /** Whether every read the tree's nodes come from holds its answer: `reads.held`, and every
   *  document the tree reads holding its blocks (`IssueTreeView.blocksHeld`). */
  readonly readsHeld: boolean;
  /** Whether every read the tree's rows come from has answered, with its data or with its
   *  failure: the six reads here and every document's blocks. It is what the page's first focus
   *  waits on, since until then a read still out can draw rows above the row it would land on,
   *  and a failed read answers too, where `readsHeld` would wait for ever. */
  readonly readsSettled: boolean;
  /** The whole tree (`useIssueTree`). */
  readonly root: TreeNode;
}

/** No read stands failed: the same empty list on every render, so the tree's lines and its
 *  opening, which read it, see one value; the tree's own where the page gives it none. */
export const NO_FAILED_READS: readonly FailedRead[] = [];

/** The key each of the six reads' lines is drawn under and what it says, in the order
 *  `useOutlineReads` gives the reads. The decisions read says what the Spec filter says of it. */
const SIX_READS = [
  ["coverage", ANSWER_LINES.coverage_unreadable],
  ["decisions", ANSWER_LINES.decisions_unreadable],
  ["results", ANSWER_LINES.results_unreadable],
  ["asks", ANSWER_LINES.asks_unreadable],
  ["comments", ANSWER_LINES.comments_unreadable],
  ["messages", ANSWER_LINES.messages_unreadable],
] as const;

/** `answer`'s line, under `key` and in the words `message`, where its read stands failed, and
 *  none where it does not. Its Retry is the read's own `refetch`, which asks that read alone
 *  again and is one function for as long as the read is observed, so a line that says the same
 *  is the same value. */
function failedLine(
  answer: QueryObserverResult<unknown>,
  key: string,
  message: string
): FailedRead[] {
  return answer.isError
    ? [
        {
          failure: answer.errorUpdateCount,
          key,
          message,
          placesRows: true,
          retry: answer.refetch,
          retrying: answer.isFetching,
        },
      ]
    : [];
}

/** What the outline says where the blocks of the document `id` could not be read: the document
 *  named by the part the tree reads it for (`treeDocumentIds`), the first of them where one
 *  document plays two. */
function blocksUnreadable(issue: IssueDetails, id: string): string {
  if (primarySpec(issue)?.id === id) return ANSWER_LINES.spec_unreadable;
  if (roleDocument(issue, "plan")?.id === id) return ANSWER_LINES.plan_unreadable;
  if (roleDocument(issue, "verification")?.id === id) return ANSWER_LINES.verification_unreadable;
  return ANSWER_LINES.decisions_document_unreadable;
}

/**
 * The tree's names reads that stand failed: every names read of the page shares its key's head
 * (`referenceTargetsQuery`), and the tree's own - the batched one and the one more
 * (`useIssueTree`) - are the only ones a mounted caller observes, which are the active ones.
 * Their keys are the sets of addresses the tree holds, which only `useIssueTree` reads, so they
 * are reached by that head: an older set no caller observes any more is not asked again.
 */
const FAILED_NAMES: QueryFilters = {
  predicate: (query) => query.state.status === "error",
  queryKey: [referenceTargetsQuery([]).queryKey[0]],
  type: "active",
};

/**
 * The names' line where a names read stands failed (`namesFailed`, `IssueTreeView`), and none
 * where none does. How many times the reads in error have failed and whether one runs again are
 * read from the cache, as `useIsFetching` reads it, since the tree observes neither; its Retry
 * asks those reads again and no other.
 */
function useNamesLine(namesFailed: boolean): readonly FailedRead[] {
  const client = useQueryClient();
  const cache = client.getQueryCache();
  const failure = useSyncExternalStore(
    useCallback(
      (changed: () => void) => cache.subscribe(notifyManager.batchCalls(changed)),
      [cache]
    ),
    () => cache.findAll(FAILED_NAMES).reduce((sum, query) => sum + query.state.errorUpdateCount, 0)
  );
  const retrying = useIsFetching(FAILED_NAMES) > 0;
  const retry = useCallback(() => void client.refetchQueries(FAILED_NAMES), [client]);
  return useMemo(
    () =>
      namesFailed
        ? [
            {
              failure,
              key: "names",
              message: ANSWER_LINES.names_unreadable,
              placesRows: false,
              retry,
              retrying,
            },
          ]
        : NO_FAILED_READS,
    [failure, namesFailed, retry, retrying]
  );
}

/**
 * The issue's tree and the reads the outline waits on, for whatever draws or lands on the
 * outline: one place for those reads and how they are kept fresh, so every caller reads each
 * of them under the one key and with the one freshness, and two callers on one page cost no
 * request more than one.
 *
 * Whether every read the tree's nodes come from holds its answer (`readsHeld`): the issue's
 * children come with the issue, the documents' blocks are `blocksHeld`, and every other node
 * comes from one of the six reads here, read under the keys `useIssueTree` reads them by, so
 * they cost no request of their own. A read that failed holds nothing either: the page cannot
 * say what it would have held, so it says that the read failed (`failed`). The conversation's
 * reads are kept too, which tell a turn the outline leaves out (`useAddressFallback`).
 */
export function useOutlineReads(issue: IssueDetails): OutlineReads {
  const { blocksHeld, namesFailed, root } = useIssueTree(issue);
  const key = issue.key;
  const documentIds = treeDocumentIds(issue);
  const documents = useQueries({
    combine: (answers): { failed: FailedRead[]; settled: boolean } => ({
      failed: answers.flatMap((answer, index) => {
        const id = documentIds[index] ?? "";
        return failedLine(answer, `blocks:${id}`, blocksUnreadable(issue, id));
      }),
      settled: answers.every((answer) => !answer.isPending),
    }),
    // The keys and the freshness `useIssueTree` reads each document's blocks under, so these
    // cost no request of their own.
    queries: documentIds.map((id) => ({ ...artifactBlocksQuery(id), ...EVENT_FRESH })),
  });
  const own = useQueries({
    combine: (
      answers
    ): { failed: FailedRead[]; reads: OutlineReads["reads"]; settled: boolean } => {
      const [, , , asks, comments, messages] = answers;
      return {
        failed: answers.flatMap((answer, index) => {
          const [line, message] = SIX_READS[index] ?? ["", ""];
          return failedLine(answer, line, message);
        }),
        reads: {
          asks: asks.data,
          comments: comments.data,
          held: answers.every((answer) => answer.data !== undefined),
          messages: messages.data,
        },
        settled: answers.every((answer) => !answer.isPending),
      };
    },
    queries: [
      { ...issueCoverageQuery(key), ...EVENT_FRESH },
      { ...issueDecisionsQuery(key, true), ...EVENT_FRESH },
      { ...issueResultsQuery(key), ...EVENT_FRESH },
      { ...issueAsksQuery(key), ...EVENT_FRESH },
      { queryFn: () => api.listComments(key), queryKey: ["comments", key], ...EVENT_FRESH },
      {
        queryFn: () => api.listIssueMessages(key),
        queryKey: ["issue-messages", key],
        ...EVENT_FRESH,
      },
    ],
  });
  const names = useNamesLine(namesFailed);
  const failed = useMemo(
    () =>
      own.failed.length + documents.failed.length + names.length === 0
        ? NO_FAILED_READS
        : [...own.failed, ...documents.failed, ...names],
    [documents.failed, names, own.failed]
  );
  return {
    failed,
    reads: own.reads,
    readsHeld: own.reads.held && blocksHeld,
    readsSettled: own.settled && documents.settled,
    root,
  };
}
