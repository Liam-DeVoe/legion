import { replaceEqualDeep } from "@tanstack/react-query";
import { createContext, useContext } from "react";

import type { DisplayTree } from "../../api/types";
import { wordsOf } from "../refs/tree-fragment";
import { nodeWords, type TreeNode } from "./issue-tree";

/**
 * How a write a row makes tells the tree that draws the row that the reader acted on the row's
 * node: Keep calls it as its write lands, before the page draws the write's answer, with the
 * node's own `dispatch://` address (`TreeNode.reference`, which is the decision's `ref`). The tree
 * notes that node, and a read that then drops its row is the reader's own doing, so the focus
 * goes on to the nearest row rather than to the page's body (`IssueTree.tsx`'s `ownAct`). Outside
 * a tree nothing hears it. It is a file of its own: the tree imports the row's writes
 * (`DecisionActions.tsx`), so it cannot live in the tree's file, and the tree, not any one row,
 * is what gives it. An edit in a row's editor is noted by the tree itself, with the words the
 * edit left (`editWords`, `drawnWords`).
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
 *  tree keeps its note of a Keep while each tree it draws draws the noted node so
 *  (`IssueTree.tsx`'s `ownAct`). */
export function sameOwnFields(before: TreeNode, after: TreeNode): boolean {
  const own = { ...before, children: [], held: 0 };
  return replaceEqualDeep(own, { ...after, children: [], held: 0 }) === own;
}

/**
 * The words of a block as an edit leaves them and as a read draws them, by one rule: the block's
 * text, each text run joined to the one before it and a space wherever anything else stands
 * between two (`refs/tree-fragment.ts`'s `wordsOf`), with each run of white space made one space
 * and none left at either end. The block is the JSON of a ProseMirror node of the documents' one
 * schema: the editor's own block as `Node.toJSON` writes it, which the editor's hold reports an
 * edit with (`node-caret.ts`'s `holdEditsToBlock`), and the server's reading of the markdown the
 * block is stored as (`DisplayTree`, `drawnWords`). Both hold the words of a link and the address
 * of a `dispatch://` reference as they are stored: the editor draws a reference's title over the
 * address it holds (`@legion/proof-editor`'s `reference-decorations.ts`), and no drawing is in
 * either. No mark splits a word, as two marks may split a run on one side alone: the editor holds
 * the reader's own letters under an authorship mark (`proofAuthored`) the stored markdown does
 * not write, and an address typed out bare under no mark, which the server's reading links.
 */
export function editWords(block: DisplayTree): string {
  return flatWords(wordsOf(block));
}

/**
 * The words a tree draws `node` with, by the rule an edit's are read by (`editWords`), for the
 * block an edit of the node's row holds (`useNodeEditing.ts`): the node's words as a reader reads
 * them (`issue-tree.ts`'s `nodeWords`), the server's reading of them (`TreeNode.tree`), which for
 * a block of a document is the reading of the block's whole stored text. A node whose read sent
 * no reading - this page asks every read for one (`api/client.ts`'s `Dispatch-Trees`) - is read
 * from its words as that read holds them (`TreeNode.text`). A decision's reading is the reading
 * of its section, whose first block is the decision's own heading, the block its row edits
 * (`lineReading`), so a decision whose read sent one is drawn with that heading's words. The
 * issue's words join its key to its title, which no edit leaves, but the outline draws no row of
 * the issue, so no edit notes it.
 */
export function drawnWords(node: TreeNode): string {
  const content = node.type === "decision" ? node.tree?.content : undefined;
  const heading: DisplayTree | undefined = Array.isArray(content) ? content[0] : undefined;
  return heading === undefined ? flatWords(nodeWords(node)) : editWords(heading);
}

/** `words` with each run of white space made one space and none left at either end. */
function flatWords(words: string): string {
  return words.trim().replace(/\s+/g, " ");
}
