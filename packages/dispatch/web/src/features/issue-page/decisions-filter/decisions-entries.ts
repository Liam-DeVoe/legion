import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { api } from "../../../api/client";
import type { IssueDetails } from "../../../api/types";
import { compareTimestamps } from "../../../lib/timestamps";
import { type DecisionNode, decisionNodes } from "../decision-nodes/decision-nodes";
import { type ChangeComment, decisionNodeChanges } from "../decision-review";
import type { TreeNode } from "../issue-tree";
import { treeContext } from "../tree-drafts";
import { nodesInOrder } from "../tree-keys";
import { issueTreeQuery, type TreeAnswer } from "../tree-read/tree-read";
import { type DecisionsEntry, type DecisionsParts, decisionsParts } from "./decisions-parts";

/**
 * The one seam between the page's reads and the Decisions view (dispatch://INFRA-3937 R8): it
 * turns the decisions view, the tree read and the issue's asks into the view's
 * parts, so no other file of the view names a read. The part that draws the model's own entries
 * (dispatch://INFRA-1150's `entries`) replaces this module and leaves the view as it is.
 *
 * The decisions are the decision nodes of the decisions view (`?view=decisions`), the issue's
 * decisions wherever they are written, which the page holds on the issue's node
 * (`TreeNode.views`, dispatch://INFRA-4097's plan, choice 6), each read as every reader of a
 * decision reads it (`decisionNodes`). A decision's Change is the comment the tree read hangs
 * under its node (`decision-review.ts`' `decisionNodeChanges`), so no quote is matched for it. A
 * view that holds no decision gives none, and no error. Until the view has answered - it is read
 * the first time the filter is on in a load (`ViewReadProvider`), and again after a Keep
 * (`DecisionActions.tsx`) and after every change of the issue (`subscribeTreeFreshness`), each of
 * which drops the answer it held - and wherever that read fails, the decisions are the decision
 * nodes of the tree read the page holds, which answers the same decision nodes with the same
 * members: so every decision stands in its part with its state, never in the folded foot for
 * want of a row, and the focus stays with it.
 *
 * The reads are the ones the page's tree is built from, under the keys `useIssueTree.ts` reads
 * them by: the tree read and the issue's asks, each watched and never sent, so the view adds no
 * request of its own.
 *
 * The view draws four parts (dispatch://INFRA-1196 R2). A decision another person made is no
 * part of its own: it stands under the reader's decisions, with that person's name on its row,
 * so G's `others` is put in `yours`, newest first, and `others` is left empty.
 */

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
  /** The whole tree, which holds each ask and each decision and each statement a tie names, and
   *  on the issue's node the decisions view the page holds (`TreeNode.views`). */
  root: TreeNode;
  /** The tree as the Decisions view leaves it. */
  view: TreeNode;
}): DecisionsParts {
  // The tree read, which holds each Change under its decision, and the decisions until the view
  // answers, watched by an observer that is never enabled: the page's own read fetches it
  // (`useIssueTree.ts`), and the view sends none.
  const tree = useQuery({ ...issueTreeQuery(issue.key), enabled: false }).data;
  // The issue's asks, which say which answered question is a person's decision
  // (`decisionNodes`), watched the same way: the page's tree reads them under this key.
  const asks = useQuery({
    enabled: false,
    queryFn: () => api.listIssueAsks(issue.key, "all"),
    queryKey: ["asks", issue.key],
  }).data;
  // The decisions view the page holds, and the tree read wherever the view holds no answer.
  const held = root.views?.decisions ?? tree;
  const decisions = useMemo(
    () => (held === undefined ? undefined : decisionNodes(held, { asks, issue })),
    [asks, held, issue]
  );
  const changes = useMemo(() => changesOf(tree, decisions ?? []), [decisions, tree]);
  // How this issue addresses a node, which is the one rule the tree was built with, so a
  // `shaped` tie resolves to the node it names (`tree-drafts.ts`).
  const nodeIdOf = useMemo(() => treeContext({ issue }).nodeIdOf, [issue]);
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
      yours:
        parts.others.length === 0
          ? parts.yours
          : newestFirst([...parts.yours, ...parts.others], root),
    };
  }, [asks, changes, decisions, issue.key, nodeIdOf, reader, root, view]);
}

/**
 * The Change each of `decisions` waits on, by the decision's own address (`Decision.ref`), as
 * the parts read one (`decisionsParts`' `changes`): the comment the tree read hangs under the
 * decision's node that `decisionNodeChanges` gives it, with its words, its author and the time
 * it was made. The decisions are the decisions view's, or the tree read's, and the Changes the
 * tree read's, which answer one node by the same id. None while the tree read has not answered.
 */
export function changesOf(
  tree: TreeAnswer | undefined,
  decisions: readonly DecisionNode[]
): ReadonlyMap<string, ChangeComment> {
  const changes = new Map<string, ChangeComment>();
  if (tree === undefined) return changes;
  const asked = decisionNodeChanges(tree);
  for (const decision of decisions) {
    const comment = asked.get(decision.node.id);
    // A Change is a person's comment (`decisionNodeChanges`), so it always has an author.
    if (comment === undefined || comment.author === null) continue;
    changes.set(decision.ref, {
      author: comment.author,
      body: comment.text,
      created_at: comment.made_at,
      id: comment.id,
    });
  }
  return changes;
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
