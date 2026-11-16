import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { api } from "../../../api/client";
import type { IssueDetails } from "../../../api/types";
import { compareTimestamps } from "../../../lib/timestamps";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { decisionChanges } from "../decision-review";
import type { TreeNode } from "../issue-tree";
import { treeContext } from "../tree-drafts";
import { type DecisionsEntry, type DecisionsParts, decisionsParts } from "./decisions-parts";

/**
 * The one seam between today's reads and the Decisions view (dispatch://INFRA-3937 R8): it turns
 * the decisions read, the issue's asks and its comments into the view's parts, so no other file
 * of the view names a read. The part that draws the model's own entries
 * (dispatch://INFRA-1150's `entries`) replaces this module and leaves the view as it is.
 *
 * The reads are the ones the page's tree is built from, under the keys `useIssueTree.ts` reads
 * them by, so the view costs no request of its own. A read that holds no decisions, or that has
 * not answered yet, gives no decision and no error: the view draws the questions alone.
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
  /** The whole tree, which holds each ask and each decision and each statement a tie names. */
  root: TreeNode;
  /** The tree as the Decisions view leaves it. */
  view: TreeNode;
}): DecisionsParts {
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
  const changes = useMemo(
    () => decisionChanges(decisions, comments, issue.artifacts),
    [decisions, comments, issue.artifacts]
  );
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
 * `entries` with the newest first, in the order G's model keeps each part
 * (`decisions-parts.ts`'s `newestFirst`), so the reader's decisions and another person's read as
 * one part: an entry the model holds no time for stands last, and two entries of one moment
 * stand in the order their nodes first stand in a depth-first walk of `root`, which is the order
 * the outline draws them in. The same reads always draw the same list.
 */
function newestFirst(
  entries: readonly DecisionsEntry[],
  root: TreeNode
): readonly DecisionsEntry[] {
  const place = new Map<string, number>();
  const walk = (node: TreeNode): void => {
    if (!place.has(node.id)) place.set(node.id, place.size);
    for (const child of node.children) walk(child);
  };
  walk(root);
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
