import { replaceEqualDeep } from "@tanstack/react-query";
import { createContext, useContext } from "react";

import type { TreeNode } from "./issue-tree";

/**
 * How a write a row makes tells the tree that draws the row that the reader acted on the row's
 * node: Keep calls it as its write lands, before the page draws the write's answer, with the
 * node's own `dispatch://` address (`TreeNode.reference`, which is the decision's `ref`). The tree
 * notes that node, and a read that then drops its row is the reader's own doing, so the focus
 * goes on to the nearest row rather than to the page's body (`IssueTree.tsx`'s `ownAct`). Outside
 * a tree nothing hears it. It is a file of its own: the tree imports the row's writes
 * (`DecisionActions.tsx`), so it cannot live in the tree's file, and the tree, not any one row,
 * is what gives it.
 */
export const NoteOwnAct = createContext<(reference: string) => void>(() => {});

/** What a row's write calls as it lands, with the address of the node it acted on. */
export function useNoteOwnAct(): (reference: string) => void {
  return useContext(NoteOwnAct);
}

/** Whether `after` draws the node `before` drew as it was, by its own fields: what it holds under
 *  it - its children, and their count (`held`) - is left out. Each read builds the whole tree
 *  anew, so the fields are compared by value, as TanStack Query compares two answers of a read
 *  (`replaceEqualDeep` gives back its first value where the second holds the same values). The
 *  tree keeps its note of the reader's act while each tree it draws draws the noted node so
 *  (`IssueTree.tsx`'s `ownAct`). */
export function sameOwnFields(before: TreeNode, after: TreeNode): boolean {
  const own = { ...before, children: [], held: 0 };
  return replaceEqualDeep(own, { ...after, children: [], held: 0 }) === own;
}
