import { type ReactNode, useMemo } from "react";

import type { Decision, DisplayTree } from "../../api/types";
import { usePlainHeadline } from "../refs/plain-text";
import type { TextSource } from "../refs/text-references";
import { nameBlock, ownTree } from "./decision-text";

/**
 * The reading of a decision's heading, taken from the tree Go read its text into: a decision is
 * named by the first block of its own content that holds any words (`decisionName`, as the
 * server's read of a decision names it), a heading or the paragraph a converted section's heading
 * became, so that block's reading is the heading's, and a view draws it from there rather than
 * parsing the heading's markdown in the page. `undefined` where the answer carried no reading - a
 * pod of the build before this one during a rolling deploy - and the heading is then parsed as
 * every view falls back to parsing, and where no block holds words.
 */
export function decisionHeadingTree(
  decision: Pick<Decision, "text_tree">
): DisplayTree | undefined {
  const tree = ownTree(decision.text_tree);
  const content = tree?.content;
  if (tree === undefined || !Array.isArray(content)) {
    return undefined;
  }
  const name = nameBlock(content as DisplayTree[]);
  return name === undefined ? undefined : { ...tree, content: [name] };
}

/**
 * A decision's heading as plain words, for what it names - a write, a form, a card - and the
 * words of a link to the decision, none of which holds markup (the reading list's
 * `decision-heading-words`): the plain-text projection of the heading's reading
 * (`usePlainHeadline`), with `source` the record the decisions document is. Until the engine has
 * answered, `words` is the heading as written, which is the load state that row accepts, so
 * nothing it names is left with no name. `tree` is the heading's reading, read once per answer
 * of the decisions read, which is when the decision is a new object, for a caller that draws
 * the heading too; `reads` draws nothing, and the caller mounts it wherever it draws.
 */
export function useDecisionHeadingWords(
  decision: Pick<Decision, "heading" | "text_tree">,
  source: TextSource | undefined
): { reads: ReactNode; tree: DisplayTree | undefined; words: string } {
  const headingTree = useMemo(() => decisionHeadingTree(decision), [decision]);
  const { reads, text } = usePlainHeadline(headingTree, decision.heading, {
    source,
    surface: "decision-heading-words",
  });
  return { reads, tree: headingTree, words: text?.text ?? decision.heading };
}
