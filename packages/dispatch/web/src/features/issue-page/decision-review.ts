import type { Comment, Decision, DisplayTree } from "../../api/types";
import { compareTimestamps } from "../../lib/timestamps";
import { wordsOf } from "../refs/tree-fragment";
import type { DecisionNode } from "./decision-nodes/decision-nodes";
import type { TreeAnswer, TreeAnswerNode } from "./tree-read/tree-read";

/**
 * Which of an issue's decisions wait for the reader's review, and which a reader asked to
 * change (dispatch://INFRA-1196 R7, R13, R35).
 *
 * This module is pure and imports no code of the tree, only the types of a decision node and of
 * the tree read, so the tree (`tree-read/tree-nodes.ts`), the Decisions filter
 * (`decisions-filter/decisions-parts.ts`) and the header's count of what waits on the reader
 * (`waiting.ts`) read one rule each and cannot disagree.
 *
 * A decision is a node of the tree read (dispatch://INFRA-1253 R8), which the read places, and
 * it places its Change too: a comment anchored inside the decision's words hangs under its node.
 * So a decision's Changes are the comments a person wrote that the read hangs under it, made
 * since it last changed and was last kept, and the newest is the change it waits on
 * (`decisionNodeChanges`); no quote is matched for it. The decision then waits on its agent, not
 * on the reader: it says it was changed, and it leaves the review list and every count of what
 * waits on the reader. Once the agent edits the decision, every change is older than its words
 * and the decision waits for review again.
 *
 * A Change is written as a comment anchored to the decision's own words (`DecisionActions.tsx`);
 * `changeAnchor` gives the quote and the occurrence it is written with, counted over the
 * decisions document's own reading, so it lands on the decision's first block below its heading
 * though an earlier decision, or the document's text above its first decision, holds the same
 * words.
 */

/** The fields of a Change the Decisions filter reads (`decisionsParts`). */
export type ChangeComment = Pick<Comment, "author" | "body" | "created_at" | "id">;

/**
 * Whether `decision` is an entry of the review list (R35): an agent's decision that waits for a
 * person's review - neither kept as it reads now nor replaced, which is the server's
 * `waiting_review` (dispatch://INFRA-1150 R2) - that a link ties to a statement of this issue,
 * and that the reader has not asked to change since it last changed (`changed`).
 * `isStatement` answers whether an address names a statement.
 *
 * It is the one rule that decides the list, so the count of what waits on the reader (R13), the
 * filter's "to review" signal and the node's own `waiting` read it too and cannot disagree with
 * the list.
 */
export function inReviewList(
  decision: Pick<DecisionNode, "links" | "waiting_review">,
  isStatement: (reference: string) => boolean,
  changed = false
): boolean {
  return (
    !changed &&
    decision.waiting_review &&
    (decision.links ?? []).some((link) => link.to.ref !== undefined && isStatement(link.to.ref))
  );
}

/**
 * Each Change of a decision node, by the comment's id, to the node's id: every comment a person
 * wrote that the read hangs under a decision node that was not replaced, made after the decision
 * last changed and after it was last kept. An agent's comment under it is no Change, and neither
 * is one older than its words, which asked for a change the words have since had.
 */
function nodeChangeComments(tree: TreeAnswer): Map<string, string> {
  const decisions = new Map<string, TreeAnswerNode>();
  for (const node of tree.nodes) {
    if (node.supertag === "decision" && node.replaced !== true) decisions.set(node.id, node);
  }
  const changes = new Map<string, string>();
  for (const node of tree.nodes) {
    const decision = node.parent === null ? undefined : decisions.get(node.parent);
    if (
      decision !== undefined &&
      node.supertag === "comment" &&
      node.author?.kind === "user" &&
      after(node.made_at, decision)
    ) {
      changes.set(node.id, decision.id);
    }
  }
  return changes;
}

/**
 * The Change each decision node waits on, by the node's id: the newest of the Changes the read
 * hangs under it (`nodeChangeComments`). The outline (`tree-read/tree-nodes.ts`), the Decisions
 * filter (`decisions-filter/decisions-entries.ts`) and the header's count (`waiting.ts`) read this
 * one rule.
 */
export function decisionNodeChanges(answer: TreeAnswer): ReadonlyMap<string, TreeAnswerNode> {
  const nodes = new Map(answer.nodes.map((node) => [node.id, node]));
  const changes = new Map<string, TreeAnswerNode>();
  for (const [id, decision] of nodeChangeComments(answer)) {
    const comment = nodes.get(id);
    const held = changes.get(decision);
    if (
      comment !== undefined &&
      (held === undefined || compareTimestamps(comment.made_at, held.made_at) > 0)
    ) {
      changes.set(decision, comment);
    }
  }
  return changes;
}

/**
 * The words of the decision's first block below its heading, which is what a change is anchored
 * to: the server matches an anchor quote against the document's own text (`pmdoc.FindQuote`),
 * never its markdown, so a list or a hard break quoted as markdown would anchor on nothing. They
 * are read from the tree Go read the decision's text into (`Decision.text_tree`), whose
 * top-level blocks are the paragraphs the markdown would have been split into; an answer that
 * carried no reading - a pod of the build before during a rolling deploy - or a reading that
 * holds no blocks leaves the first paragraph's own markdown, which is all the browser has.
 * Undefined for a decision whose heading is all it holds.
 */
export function anchorQuote(decision: Pick<Decision, "text" | "text_tree">): string | undefined {
  const tree = decision.text_tree;
  if (tree === undefined || !Array.isArray(tree.content)) {
    return firstParagraph(decision.text);
  }
  const block = (tree.content as DisplayTree[]).find((node) => node.type !== "heading");
  return block === undefined ? undefined : wordsOf(block);
}

/**
 * The anchor a Change of a decision is written with: the quote (`anchorQuote`) and its zero-based
 * occurrence in the decisions document's text. The occurrence is absent where the document's
 * reading is not in hand, or does not line up with the decisions read (`changeAnchor`). A write
 * without it is anchored by the server where the quote stands once in the document, and refused
 * (409) where it stands more than once, rather than landed on the wrong place.
 */
export interface ChangeAnchor {
  readonly occurrence?: number;
  readonly quote: string;
}

/** The heading of the section that holds the replaced decisions (`replacedHeading`,
 *  `api/decisions.go`): its `###` sections are the replaced decisions, and it is none itself. */
const REPLACED_HEADING = "Replaced";

/**
 * The anchor a Change of `decision` is written with: the quote `anchorQuote` gives, and which of
 * that quote's places in the decisions document is the decision's own first block below its
 * heading. Two decisions can open with the same sentence, and the server refuses a quote that
 * matches more than one place unless the write names its zero-based occurrence (409, "matches 2
 * places; pass a zero-based occurrence"); so a Change names it (decision 92 of
 * dispatch://INFRA-1196). Undefined for a decision whose heading is all it holds.
 *
 * The server counts the quote's places over the whole decisions document (`docs.MarkQuote`,
 * `pmdoc.FindQuote`; decision 92 as amended), so this counts over it too. The text above the
 * first decision (the document's title, an intro), the `## Replaced` heading and a `#` section are
 * in no row of the decisions read, so a count over the rows missed a match there, and the Change
 * then landed outside every decision. The blocks read (`ArtifactBlock`) cannot serve either: it
 * holds each block's id, type and markdown range, and no text. So the caller passes `document`,
 * the decisions document's own reading: the `markdown_tree` of its text read
 * (`api.getArtifactText`), read once when the reader sends the Change. `decisions` are the
 * decisions read's rows, replaced ones included, in document order.
 *
 * The document's text is every text node's text joined, with one space wherever two text runs are
 * not adjacent, as between two blocks or two list items (`buildFlattenedText`): the words of the
 * reading (`wordsOf`). A quote's matches are every exact match in that text, overlapping ones
 * included, in document order (`findAll`), and the occurrence picks one of them: it is the number
 * of matches that start before the decision's first block. A match that starts in the heading and
 * runs into the block counts too, since the server lists it first. Where the block's words start
 * is read off the nodes above it, never found by searching for the quote.
 *
 * The decision's heading is found among the reading's top-level nodes by the server's section
 * rule (`decisionSections`, `isDecisionHeading`), walking them in order. A heading closes an open
 * section whose heading's level is its own or deeper; a `#` or `##` heading says whether what
 * follows is under `## Replaced`; and a heading opens a decision where none is open, at `##`
 * outside `## Replaced` and at `###` under it. The decision's heading is the one at the
 * decision's own index in `decisions` (by `block_id`). The reading lines up with the read only
 * where that heading's words are the decision's heading, and the node right after it is a block,
 * no heading, whose words are the quote. Otherwise, and where `document` is undefined, the anchor
 * carries no occurrence (`ChangeAnchor`). That is so for a heading the server reads with markup
 * or closing hashes, for a sub-heading right below the decision's heading, and for a decision
 * read with no reading whose first paragraph holds markup.
 *
 * One assumption remains: the reading's text runs are the stored document's, which the server
 * makes its display reading from.
 */
export function changeAnchor(
  decision: Pick<Decision, "block_id" | "heading" | "text" | "text_tree">,
  decisions: readonly Pick<Decision, "block_id">[] | null | undefined,
  document: DisplayTree | undefined
): ChangeAnchor | undefined {
  const quote = anchorQuote(decision);
  if (quote === undefined) return undefined;
  if (document === undefined || !Array.isArray(document.content)) return { quote };
  const nodes = document.content as DisplayTree[];
  const index = (decisions ?? []).findIndex((row) => row.block_id === decision.block_id);
  const heading = index === -1 ? undefined : decisionHeadings(nodes)[index];
  const block = heading === undefined ? undefined : nodes[heading + 1];
  if (
    heading === undefined ||
    wordsOf(nodes[heading]).trim() !== decision.heading.trim() ||
    block === undefined ||
    block.type === "heading" ||
    wordsOf(block) !== quote
  ) {
    return { quote };
  }
  // The block's words start one space after the words of the nodes above it, where those hold
  // any: `wordsOf` writes that space between two blocks, as the server does.
  const above = wordsOf({ ...document, content: nodes.slice(0, heading + 1) });
  const start = above === "" ? 0 : above.length + 1;
  return { occurrence: matchesBefore(wordsOf(document), quote, start), quote };
}

/** Where each decision's heading stands among the top-level nodes of the decisions document's
 *  reading, in document order, by the server's section rule (`changeAnchor`). */
function decisionHeadings(nodes: readonly DisplayTree[]): number[] {
  const headings: number[] = [];
  // The level of the open decision's heading, and 0 while no decision is open.
  let open = 0;
  let replaced = false;
  for (const [index, node] of nodes.entries()) {
    const level = headingLevel(node);
    if (level === 0) continue;
    if (level <= open) open = 0;
    if (level <= 2) replaced = level === 2 && wordsOf(node).trim() === REPLACED_HEADING;
    if (open === 0 && level === (replaced ? 3 : 2)) {
      open = level;
      headings.push(index);
    }
  }
  return headings;
}

/** A node's heading level (its `attrs.level`), and 0 for a node that is no heading, as the server
 *  reads a block that is none (`headingOf`). */
function headingLevel(node: DisplayTree): number {
  if (node.type !== "heading") return 0;
  const attrs = node.attrs;
  const level =
    typeof attrs === "object" && attrs !== null && "level" in attrs ? attrs.level : undefined;
  return typeof level === "number" ? level : 0;
}

/** The decision's first paragraph below its heading, as markdown: the block of a decision read
 *  with no reading (`anchorQuote`). Undefined for a decision whose heading is all it holds. */
function firstParagraph(text: string): string | undefined {
  // A blank line holds nothing once trimmed, so it is never the paragraph.
  for (const piece of text.split(/\n\s*\n/)) {
    const paragraph = piece.trim();
    if (paragraph !== "" && !paragraph.startsWith("#")) return paragraph;
  }
  return undefined;
}

/** How many places of `text` hold `quote` and start before `end`, overlapping ones included, as
 *  the server lists a quote's matches (`pmdoc.findAll`); an empty quote matches nowhere, as
 *  there. */
function matchesBefore(text: string, quote: string, end: number): number {
  if (quote === "") return 0;
  let count = 0;
  for (let at = text.indexOf(quote); at !== -1 && at < end; at = text.indexOf(quote, at + 1)) {
    count += 1;
  }
  return count;
}

/** Whether a comment made at `at` is newer than the decision node's words and its last Keep. */
function after(at: string, row: Partial<Pick<Decision, "changed_at" | "kept">>): boolean {
  const since = [row.changed_at, row.kept?.kept_at].filter(
    (moment): moment is string => typeof moment === "string"
  );
  return since.every((moment) => compareTimestamps(at, moment) > 0);
}
