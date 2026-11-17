import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { api } from "../../../api/client";
import { roleDocument } from "../../../api/issue-cache";
import { artifactBlocksQuery } from "../../../api/queries";
import type { IssueDetails } from "../../../api/types";
import { compareTimestamps } from "../../../lib/timestamps";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { wordsOf } from "../../refs/tree-fragment";
import { decisionChanges } from "../decision-review";
import type { DocumentBlocks, TreeNode } from "../issue-tree";
import { treeContext } from "../tree-drafts";
import { nodesInOrder } from "../tree-keys";
import { type DecisionsEntry, type DecisionsParts, decisionsParts } from "./decisions-parts";

/**
 * The one seam between today's reads and the Decisions view: it turns the decisions read, the
 * issue's asks, its comments and the decisions document's blocks into the view's parts, so no
 * other file of the view names a read. Once the server's decisions model gives the view its
 * entries, that model's read replaces this module and leaves the view as it is.
 *
 * The reads are the ones the page's tree is built from: the three reads the tree reads, under
 * the keys `useIssueTree.ts` reads them by, and the decisions document's blocks under the tree's
 * own key for them (`artifactBlocksQuery`, for the document `roleDocument` names), so the view
 * adds no request of its own. The blocks are why a Change stands under the decision whose
 * section holds its anchor (`decision-review.ts`): the quote alone cannot say which, since two
 * decisions can hold the same words. Until the blocks answer, a Change stands under the first
 * decision whose words hold its quote. A read that holds no decisions, or that has not answered
 * yet, gives no decision and no error: the view draws the questions alone.
 *
 * The view draws four parts. A decision another person made is no part of its own: it stands
 * under the reader's decisions, with that person's name on its row, so the model's `others` is
 * put in `yours`, newest first, and `others` is left empty.
 *
 * The seam says too whether those reads hold their answers (`readsHeld`), which the outline's
 * keys (`useTreeKeymap.ts`) and the body's empty line read. Until they do, a view with no
 * question or no decision to go to may be one whose questions and decisions have not arrived,
 * so `q` and `w` with nowhere to go say nothing rather than that there is none, and the body
 * draws no line that the issue holds nothing, as the outline does (`slots/body.tsx`).
 */

/** The Decisions view's parts, and whether the reads they come from hold their answers. */
interface DecisionsEntries extends DecisionsParts {
  /** Whether the decisions read, the asks, the comments and, where the issue has a decisions
   *  document, that document's blocks each hold an answer. A read that failed holds none. */
  readonly readsHeld: boolean;
}

/** The Decisions view's parts, from the issue, the reader's login, the whole tree and the tree
 *  as the Decisions view leaves it (`presetView`). */
export function useDecisionsEntries({
  issue,
  reader,
  root,
  view,
}: {
  issue: IssueDetails;
  /** Who is reading, which tells their own decisions from other people's. */
  reader: string | undefined;
  /** The whole tree, which holds each ask and each decision and each statement a tie names. */
  root: TreeNode;
  /** The tree as the Decisions view leaves it. */
  view: TreeNode;
}): DecisionsEntries {
  const decisions = useQuery(issueDecisionsQuery(issue.key, true)).data;
  const asks = useQuery({
    queryFn: () => api.listIssueAsks(issue.key, "all"),
    queryKey: ["asks", issue.key],
  }).data;
  // The issue's comments, which hold the changes a person asked of a decision
  // (`decision-review.ts`), under the key the tree reads them by.
  const comments = useQuery({
    queryFn: () => api.listComments(issue.key),
    queryKey: ["comments", issue.key],
  }).data;
  // The blocks of the decisions document, which name the section each Change's anchor stands
  // in and so the decision it changes, under the key the tree reads them by. An issue with no
  // decisions document reads none.
  const documentId = roleDocument(issue, "decisions")?.id;
  const blocks = useQuery({
    ...artifactBlocksQuery(documentId ?? ""),
    enabled: documentId !== undefined,
  }).data;
  // An issue with no decisions document has no blocks to wait for.
  const readsHeld =
    decisions !== undefined &&
    asks !== undefined &&
    comments !== undefined &&
    (documentId === undefined || blocks !== undefined);
  const documents = useMemo<readonly DocumentBlocks[] | undefined>(
    () =>
      documentId === undefined || blocks === undefined
        ? undefined
        : [{ artifactId: documentId, blocks }],
    [documentId, blocks]
  );
  const changes = useMemo(
    () => decisionChanges(decisions, comments, issue.artifacts, documents),
    [decisions, comments, issue.artifacts, documents]
  );
  // How this issue addresses a node, which is the one rule the tree was built with, so a
  // `shaped` tie resolves to the node it names (`tree-drafts.ts`). `nodeIdOf` names no block by
  // a line, so the line rule it is handed is the reading's words, as G's tests hand it.
  const nodeIdOf = useMemo(() => treeContext({ issue }, wordsOf).nodeIdOf, [issue]);
  return useMemo(() => {
    const parts = decisionsParts({
      asks,
      changes,
      decisions,
      issueKey: issue.key,
      nodeIdOf,
      reader,
      root,
      view,
    });
    return {
      ...parts,
      others: [],
      readsHeld,
      yours:
        parts.others.length === 0
          ? parts.yours
          : newestFirst([...parts.yours, ...parts.others], root),
    };
  }, [asks, changes, decisions, issue.key, nodeIdOf, reader, readsHeld, root, view]);
}

/**
 * `entries` with the newest first, in the order G's model keeps each part
 * (`decisions-parts.ts`'s `newestFirst`), so the reader's decisions and another person's read as
 * one part: an entry the model holds no time for stands last, and two entries of one moment
 * stand in the order their nodes first stand in the page's reading order (`nodesInOrder`),
 * which is the order the outline draws them in. The same reads always draw the same list.
 */
function newestFirst(
  entries: readonly DecisionsEntry[],
  root: TreeNode
): readonly DecisionsEntry[] {
  // Each node's place in the reading order; a node drawn under two parents is one node, so its
  // first placement is its place.
  const place = new Map<string, number>();
  for (const node of nodesInOrder(root)) if (!place.has(node.id)) place.set(node.id, place.size);
  return [...entries].sort((one, other) => {
    const left = one.at;
    const right = other.at;
    if (left !== undefined && right !== undefined) {
      const newer = compareTimestamps(right, left);
      if (newer !== 0) return newer;
    } else if (left !== right) {
      return left === undefined ? 1 : -1;
    }
    // Every entry's node is a node of `root`, which the model read its entries from.
    return (place.get(one.node.id) ?? place.size) - (place.get(other.node.id) ?? place.size);
  });
}
