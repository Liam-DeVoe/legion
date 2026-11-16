import { isViewer } from "../refs/actor";
import type { TreeNode } from "./issue-tree";

/**
 * How many entries the Contents list draws for `node`'s branch: each ask, and each comment or
 * message the reader wrote that begins a thread, at any depth. A node drawn under a second
 * parent is counted once, at its own place: a transcluded copy and the branch under it are the
 * same nodes as at the edited place. Conversation's count in the view row is this number
 * (`slots/view-row.tsx`), so the list that draws the entries reads the same rule from here.
 */
export function contentsEntries(node: TreeNode, viewer: string | undefined): number {
  let entries = 0;
  for (const child of node.children) {
    if (child.transcluded) continue;
    const begunByReader =
      (child.type === "comment" || child.type === "message") &&
      node.type !== "comment" &&
      node.type !== "message" &&
      child.author !== undefined &&
      isViewer(child.author, viewer);
    if (child.type === "ask" || begunByReader) entries += 1;
    entries += contentsEntries(child, viewer);
  }
  return entries;
}
