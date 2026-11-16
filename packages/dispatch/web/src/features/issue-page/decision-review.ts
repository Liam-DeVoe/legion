import type { Artifact, Comment, Decision, DisplayTree, IssueDecisions } from "../../api/types";
import { compareTimestamps } from "../../lib/timestamps";
import { parseNodeReference } from "../refs/routes";
import { wordsOf } from "../refs/tree-fragment";

/**
 * Which of an issue's decisions wait for the reader's review, and which a reader asked to
 * change (dispatch://INFRA-1196 R7, R13, R35).
 *
 * This module is pure and imports nothing of the tree, so the tree (`tree-drafts.ts`), the
 * Decisions filter (`decisions-filter/decisions-parts.ts`) and the header's count of what waits
 * on the reader (`waiting.ts`) read one rule each and cannot disagree.
 *
 * A Change is a comment of the issue anchored to the decision's own words in the decisions
 * document (`DecisionActions.tsx`), which is the write the old Decisions tab made; the decisions
 * read holds no state for it. So the page reads it from the comments it already reads: every
 * comment a person anchored to a decision's words since that decision last changed is a change
 * they asked for, and is drawn under it (`decisionChangeComments`); the newest is the change the
 * decision waits on (`decisionChanges`). The decision then waits on its agent, not on the
 * reader: it says it was changed, and it leaves the review list and every count of what waits on
 * the reader. Once the agent edits the decision, every change is older than its words and the
 * decision waits for review again.
 */

/** The fields of a comment this module reads. */
export type ChangeComment = Pick<Comment, "anchor" | "author" | "body" | "created_at" | "id">;

/** The fields of a decision this module reads. */
type ChangedDecision = Pick<
  Decision,
  "changed_at" | "kept" | "ref" | "replaced" | "text" | "text_tree"
>;

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
  decision: Pick<Decision, "links" | "waiting_review">,
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
 * The id of the document the decisions read names (`IssueDecisions.document`), among the
 * issue's documents: the read names it by its `dispatch://` address, and a Change's anchor names
 * the document it is anchored in by its id (`Anchor.artifact_id`). Undefined for an issue with
 * no decisions document, and while the issue read carries none of that address.
 */
export function decisionsDocumentId(
  artifacts: readonly Pick<Artifact, "id" | "slug">[] | null | undefined,
  decisions: Pick<IssueDecisions, "document"> | undefined
): string | undefined {
  const route = decisions?.document == null ? undefined : parseNodeReference(decisions.document);
  if (route?.kind !== "artifact") return undefined;
  return artifacts?.find((artifact) => artifact.slug === route.slug)?.id;
}

/**
 * Each change a person asked of a decision, by the comment's id, to the decision's own address
 * (`Decision.ref`): every comment a person anchored to the decision's words in the decisions
 * document, made after the decision last changed and after it was last kept. A decision that
 * was replaced takes no change: nothing waits on it any longer. Comments are matched by their
 * anchor's quote, which is the decision's text as the server matches quotes (`pmdoc.FindQuote`),
 * so a quote of a decision's words names that decision and no other, and by the document their
 * anchor names, which is the issue's document at the read's address (`decisionsDocumentId`).
 * Every comment it holds is drawn under its decision; one older than the decision's words is no
 * change and is not held.
 */
export function decisionChangeComments(
  decisions: Pick<IssueDecisions, "decisions" | "document"> | undefined,
  comments: readonly ChangeComment[] | undefined,
  artifacts: readonly Pick<Artifact, "id" | "slug">[] | null | undefined
): ReadonlyMap<string, string> {
  const changes = new Map<string, string>();
  const rows = (decisions?.decisions ?? []).filter((row) => !row.replaced);
  const document = decisionsDocumentId(artifacts, decisions);
  if (rows.length === 0 || document === undefined) return changes;
  // A decision's whole words as a quote is matched against them: its reading where the read
  // carried one, and its stored text otherwise.
  const words = new Map(
    rows.map((row) => [
      row.ref,
      flat(row.text_tree === undefined ? row.text : wordsOf(row.text_tree as DisplayTree)),
    ])
  );
  for (const comment of comments ?? []) {
    const anchor = comment.anchor;
    if (anchor === null || anchor.artifact_id !== document || comment.author.kind !== "user") {
      continue;
    }
    const quote = flat(anchor.quote);
    if (quote === "") continue;
    const row = rows.find((one) => words.get(one.ref)?.includes(quote) === true);
    if (row !== undefined && after(comment.created_at, row)) changes.set(comment.id, row.ref);
  }
  return changes;
}

/**
 * The change each decision waits on, by the decision's own address (`Decision.ref`): the newest
 * of the changes a person asked of it (`decisionChangeComments`, which `placed` holds where the
 * caller has read it already). A decision with none is not held.
 */
export function decisionChanges(
  decisions: Pick<IssueDecisions, "decisions" | "document"> | undefined,
  comments: readonly ChangeComment[] | undefined,
  artifacts: readonly Pick<Artifact, "id" | "slug">[] | null | undefined,
  placed: ReadonlyMap<string, string> = decisionChangeComments(decisions, comments, artifacts)
): ReadonlyMap<string, ChangeComment> {
  const changes = new Map<string, ChangeComment>();
  for (const comment of comments ?? []) {
    const ref = placed.get(comment.id);
    if (ref === undefined) continue;
    const held = changes.get(ref);
    if (held === undefined || compareTimestamps(comment.created_at, held.created_at) > 0) {
      changes.set(ref, comment);
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
 * carried no reading - a pod of the build before during a rolling deploy - leaves the first
 * paragraph's own markdown, which is all the browser has. Undefined for a decision whose heading
 * is all it holds.
 */
export function anchorQuote(decision: Pick<Decision, "text" | "text_tree">): string | undefined {
  const content = decision.text_tree?.content;
  if (!Array.isArray(content)) {
    return firstParagraph(decision.text);
  }
  const first = (content as DisplayTree[]).find((node) => node.type !== "heading");
  return first === undefined ? undefined : wordsOf(first);
}

/** The decision's first paragraph below its heading, as markdown: the quote of an answer that
 *  carried no reading of the decision's text. Undefined for a decision whose heading is all it
 *  holds. */
function firstParagraph(text: string): string | undefined {
  return text
    .split(/\n\s*\n/)
    .map((paragraph) => paragraph.trim())
    .find((paragraph) => paragraph !== "" && !paragraph.startsWith("#"));
}

/** `text` with every run of white space one space, so a quote and the words it was taken from
 *  are compared as words. */
function flat(text: string): string {
  return text.replace(/\s+/gu, " ").trim();
}

/** Whether a comment made at `at` is newer than the decision's words and its last Keep. */
function after(at: string, row: ChangedDecision): boolean {
  const since = [row.changed_at, row.kept?.kept_at].filter(
    (moment): moment is string => typeof moment === "string"
  );
  return since.every((moment) => compareTimestamps(at, moment) > 0);
}
