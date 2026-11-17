import type { Comment, Decision } from "../../api/types";
import { compareTimestamps } from "../../lib/timestamps";
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
 * A decision is a node of the tree read, which the read places, and
 * it places its Change too: a comment anchored inside the decision's words hangs under its node.
 * So a decision's Changes are the comments a person wrote that the read hangs under it, made
 * since it last changed and was last kept, and the newest is the change it waits on
 * (`decisionNodeChanges`); no quote is matched for it. The decision then waits on its agent, not
 * on the reader: it says it was changed, and it leaves the review list and every count of what
 * waits on the reader. Once the agent edits the decision, every change is older than its words
 * and the decision waits for review again.
 *
 * A Change is written as a comment anchored to the decision's own words (`DecisionActions.tsx`):
 * the decision node's document, and the words of its first paragraph (`decision-nodes.ts`'
 * `decisionQuote`).
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

/** Whether a comment made at `at` is newer than the decision node's words and its last Keep. */
function after(at: string, row: Partial<Pick<Decision, "changed_at" | "kept">>): boolean {
  const since = [row.changed_at, row.kept?.kept_at].filter(
    (moment): moment is string => typeof moment === "string"
  );
  return since.every((moment) => compareTimestamps(at, moment) > 0);
}
