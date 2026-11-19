import { useQueries } from "@tanstack/react-query";

import { api } from "../../api/client";
import { artifactBlocksQuery, issueCoverageQuery, issueResultsQuery } from "../../api/queries";
import type { IssueDetails } from "../../api/types";
import { issueDecisionsQuery } from "../doc/spec-marks";
import { refetchFailedOnFocus } from "../refs/reference-target";
import type { IssueTreeReads, TreeNode } from "./issue-tree";
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

/** What the outline's reads give (`useOutlineReads`). Each field is one value for as long as the
 *  reads say the same; the object holding them is new on every render. */
export interface OutlineReads {
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
 * say what it would have held. The conversation's reads are kept too, which tell a turn the
 * outline leaves out (`useAddressFallback`).
 */
export function useOutlineReads(issue: IssueDetails): OutlineReads {
  const { blocksHeld, root } = useIssueTree(issue);
  const key = issue.key;
  const documents = useQueries({
    combine: (answers) => answers.every((answer) => !answer.isPending),
    // The keys and the freshness `useIssueTree` reads each document's blocks under, so these
    // cost no request of their own.
    queries: treeDocumentIds(issue).map((id) => ({ ...artifactBlocksQuery(id), ...EVENT_FRESH })),
  });
  const { reads, settled } = useQueries({
    combine: (answers): { reads: OutlineReads["reads"]; settled: boolean } => {
      const [, , , asks, comments, messages] = answers;
      return {
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
  return { reads, readsHeld: reads.held && blocksHeld, readsSettled: settled && documents, root };
}
