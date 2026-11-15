import type { Actor, Decision, IssueDecisions } from "../../../api/types";
import { parseNodeReference } from "../../refs/routes";
import { firstLineOf, nodeWords, type TreeNode } from "../issue-tree";
import type { NodeState, NodeType } from "../page-words";

/**
 * What decided each statement of the Spec filter, and the mark each statement carries
 * (dispatch://INFRA-1196 R5, R17, R22, R35).
 *
 * The Spec filter draws two columns: the statements, and beside each run of statements one
 * decision shaped, that decision in the words it came from (R5). This module is that reading of
 * the tree, and nothing else: it is pure, holds no React and makes no request, so the body
 * (`SpecFilterBody.tsx`) and the unit tests read one model.
 *
 * What a statement's decision is, in the order the rules are read:
 *
 * 1. **A `shaped` tie.** A decision whose link names the statement shaped it
 *    (`Decision.links[].to`, dispatch://INFRA-1150 R1). The first such decision heads the
 *    statement's run; every other one is named at the end of the statement's own line, since
 *    R5 draws "the decisions that informed it" and a statement may rest on several. A replaced
 *    decision draws no line at all (R5).
 * 2. **The answered question it hangs under.** A statement that hangs under an answered ask
 *    carries that ask's line with no tuple, "since that decision is its parent" (R5).
 * 3. **Nothing.** A statement no decision shaped is drawn as such, which is what makes a
 *    missing tie visible (R35).
 *
 * What a person's decision quotes is the person's own answer, never a restatement of it
 * (dispatch://INFRA-1150 R4): the answer's first sentence word for word (`firstSentenceOf`),
 * or, where they wrote none, the options they chose. What an agent's decision quotes is its own
 * text, with the one word the model holds for how sure it was (`Decision.unsure`).
 *
 * Two parts of R5 that today's reads do not hold, which the body draws nothing in the place of:
 *
 * - **"Changed since".** Whether a statement's own text changed after the decision needs the
 *   statement's last change beside the tie, and no read of this page carries one: the coverage
 *   read holds a requirement's text and no time, `ArtifactBlock` holds none, and
 *   `DecisionLink.stale` is the opposite question - the decision changed and the block did not
 *   (`features/doc/spec-marks.ts`). It is the tree read's `changed_at` (dispatch://INFRA-1253
 *   R8).
 * - **An agent's level of uncertainty and its reason.** The decisions read answers one boolean,
 *   `unsure` (dispatch://INFRA-1150); the level and the reason arrive with it.
 */

/** The four marks a statement carries at the right of its row, which the legend above the
 *  columns names (R17): a statement every check of passed, one a check failed, one whose checks
 *  have not all passed and none failed, and one no check serves. A statement that carries a
 *  check is drawn as covered and never as proven, because a statement that has a check is not a
 *  statement that is true (R17). */
export type SpecMark = "covered" | "failing" | "no_check" | "not_run";

/** Whose words a run of statements came from, which the column's heading says: the reader's own,
 *  another person's, or an agent's. */
export type SpecSaid = "agent" | "person" | "you";

/** The decision that shaped a run of statements, as the left column draws it (R5). */
export interface SpecDecision {
  /** When it was decided: the person's answer's time, or the decision's last change. */
  readonly at?: string;
  /** Who decided it, where the model holds an author. */
  readonly by?: Actor;
  /** The options a person chose, in the ask's own words; empty where they chose none. */
  readonly chose: readonly string[];
  /** The node that holds the whole of it, which the link to it names: the answered ask whose
   *  answer this is, or the decision. */
  readonly id: string;
  /** The question the words answer, its first line as R1 cuts a row's; absent where the words
   *  answer no question. */
  readonly question?: string;
  /** The words the decision came from: a person's answer's first sentence, or the options they
   *  chose where they wrote nothing, or an agent's decision's own text. */
  readonly quote: string;
  /** The node's own `dispatch://` address, which the link to the whole of it follows. */
  readonly reference?: string;
  readonly said: SpecSaid;
  /** How the decision stands with the reader, where the model gives it a state. */
  readonly state?: NodeState;
  /** What the node is: the ask whose answer this is, or the decision. */
  readonly type: NodeType;
  /** Whether the model marks this decision unsure (`Decision.unsure`). */
  readonly unsure: boolean;
}

/** One statement of the Spec filter, as its row draws it. */
export interface SpecStatement {
  /** Every other decision that shaped it, beyond the one its run is headed by (R5). */
  readonly alsoShapedBy: readonly SpecDecision[];
  readonly mark: SpecMark;
  /** The node as the filter's view left it, whose words the row draws in full. */
  readonly node: TreeNode;
}

/** One run of statements that one decision shaped, which the bracket in the left column ties
 *  together (R5: "the same decision stands in a column beside the statements it shaped"). */
export interface SpecRun {
  /** The decision that shaped every statement of the run; absent where none did (R35). */
  readonly decision?: SpecDecision;
  readonly statements: readonly SpecStatement[];
}

/** The node a group of statements hangs under, which the filter draws as no row of its own
 *  (R2): the question that was answered, or the comment they were written under. */
export interface SpecParent {
  readonly at?: string;
  readonly by?: Actor;
  readonly id: string;
  /** Its first line, as R1 cuts a row's (`TreeNode.parentLine`). */
  readonly line: string;
  readonly type: NodeType;
}

/** The statements under one parent, in the order the filter draws them. */
export interface SpecGroup {
  /** The node they hang under; absent for the statements that hang under the issue itself. */
  readonly parent?: SpecParent;
  readonly runs: readonly SpecRun[];
}

/**
 * The Spec filter's body, read from the tree and the decisions.
 *
 * `view` is the Spec filter's own view of the tree (`presetView`), whose rows are the
 * statements in the order the page draws them; `root` is the whole tree, which holds the asks,
 * the decisions and each statement's checks, none of which the Spec filter draws as rows (R2).
 * `nodeIdOf` is how this issue addresses a node, which is the one rule the tree is built with
 * (`tree-drafts.ts`'s `treeContext`), so a tie resolves to the node it names however its
 * address is written.
 */
export function specDecidedBy({
  decisions,
  nodeIdOf,
  reader,
  root,
  view,
}: {
  readonly decisions: IssueDecisions | undefined;
  readonly nodeIdOf: (reference: string) => string | undefined;
  /** Who is reading, so a decision of theirs reads as their own words (R34). */
  readonly reader: string | undefined;
  /** The whole tree. */
  readonly root: TreeNode;
  /** The tree as the Spec filter leaves it. */
  readonly view: TreeNode;
}): readonly SpecGroup[] {
  const nodes = nodesById(root);
  const asks = asksById(nodes);
  const shaped = shapedBy({ asks, decisions, nodeIdOf, nodes, reader });
  const groups: {
    parent?: SpecParent;
    runs: { decision?: SpecDecision; statements: SpecStatement[] }[];
  }[] = [];
  for (const node of statementsOf(view)) {
    const ties = shaped.get(node.id) ?? [];
    const parent = parentOf(node, nodes);
    const decision = ties.at(0) ?? answerUnder(parent, nodes, reader);
    const statement: SpecStatement = {
      alsoShapedBy: ties.slice(1),
      mark: markOf(node, nodes),
      node,
    };
    const group = groups.at(-1);
    if (group === undefined || group.parent?.id !== parent?.id) {
      groups.push({
        ...(parent === undefined ? {} : { parent }),
        runs: [{ ...(decision === undefined ? {} : { decision }), statements: [statement] }],
      });
      continue;
    }
    const run = group.runs.at(-1);
    if (run === undefined || run.decision?.id !== decision?.id) {
      group.runs.push({
        ...(decision === undefined ? {} : { decision }),
        statements: [statement],
      });
      continue;
    }
    run.statements.push(statement);
  }
  return groups;
}

/**
 * The first sentence of `text`, word for word: everything up to the first full stop, question
 * mark or exclamation mark followed by a space or the end of the text, and the whole text where
 * it holds none, with no limit of characters (R5). Line breaks inside it are spaces, because
 * the line it is drawn on is one line.
 *
 * It is not `firstLineOf`, which cuts a row's first line at 100 characters (R1): a person's
 * answer is quoted word for word, and a sentence of theirs is as long as they wrote it.
 */
export function firstSentenceOf(text: string): string {
  const whole = text.replace(/\s+/gu, " ").trim();
  return /^.*?[.!?](?=\s|$)/u.exec(whole)?.[0] ?? whole;
}

/** Every node of the tree by its id, the first placement of a node drawn under more than one
 *  parent, which is where it is edited (`TreeNode.transcluded`). */
function nodesById(root: TreeNode): ReadonlyMap<string, TreeNode> {
  const nodes = new Map<string, TreeNode>();
  const walk = (node: TreeNode): void => {
    if (!nodes.has(node.id)) nodes.set(node.id, node);
    for (const child of node.children) walk(child);
  };
  walk(root);
  return nodes;
}

/** Each ask of the tree by the id a decision names it with (`Decision.asks`), which is the ask's
 *  own id and not the id of the row it is drawn as: an ask the server indexed from a block of a
 *  document is drawn as that block (`tree-drafts.ts`'s `addAsks`). */
function asksById(nodes: ReadonlyMap<string, TreeNode>): ReadonlyMap<string, TreeNode> {
  const asks = new Map<string, TreeNode>();
  for (const node of nodes.values()) {
    if (node.type !== "ask" || node.reference === undefined) continue;
    const route = parseNodeReference(node.reference);
    if (route?.kind === "ask") asks.set(route.id, node);
  }
  return asks;
}

/** Every row the Spec filter draws, in the order it draws them. */
function statementsOf(view: TreeNode): readonly TreeNode[] {
  const rows: TreeNode[] = [];
  const walk = (node: TreeNode): void => {
    rows.push(node);
    for (const child of node.children) walk(child);
  };
  for (const child of view.children) walk(child);
  return rows;
}

/** The decisions that shaped each statement, by the statement's node id, in the order the
 *  decisions read answers them. */
function shapedBy({
  asks,
  decisions,
  nodeIdOf,
  nodes,
  reader,
}: {
  asks: ReadonlyMap<string, TreeNode>;
  decisions: IssueDecisions | undefined;
  nodeIdOf: (reference: string) => string | undefined;
  nodes: ReadonlyMap<string, TreeNode>;
  reader: string | undefined;
}): ReadonlyMap<string, readonly SpecDecision[]> {
  const shaped = new Map<string, SpecDecision[]>();
  for (const decision of decisions?.decisions ?? []) {
    // A replaced decision draws no line (R5): it is drawn where it sits in the outline, and a
    // statement it shaped is no longer standing on it.
    if (decision.replaced) continue;
    const links = decision.links ?? [];
    if (links.length === 0) continue;
    const made = decisionOf(decision, { asks, nodeIdOf, nodes, reader });
    for (const link of links) {
      const id = link.to.ref === undefined ? undefined : nodeIdOf(link.to.ref);
      if (id === undefined) continue;
      const held = shaped.get(id);
      if (held === undefined) shaped.set(id, [made]);
      else held.push(made);
    }
  }
  return shaped;
}

/** One decision as the column draws it: a person's by the answer they gave, an agent's by its
 *  own words. */
function decisionOf(
  decision: Decision,
  {
    asks,
    nodeIdOf,
    nodes,
    reader,
  }: {
    asks: ReadonlyMap<string, TreeNode>;
    nodeIdOf: (reference: string) => string | undefined;
    nodes: ReadonlyMap<string, TreeNode>;
    reader: string | undefined;
  }
): SpecDecision {
  const node = nodes.get(nodeIdOf(decision.ref) ?? "");
  // A person's decision is their answer to a question (dispatch://INFRA-1150 R3), so the words
  // the column quotes are the answer's own and never the decision's restatement of them (R4).
  const answered = (decision.asks ?? [])
    .map((ask) => asks.get(ask))
    .find((ask) => ask?.answer !== undefined);
  if (decision.kind === "person" && answered?.answer !== undefined) {
    return answerDecision(answered, reader);
  }
  const at = node?.at ?? decision.changed_at ?? undefined;
  return {
    ...(at === undefined ? {} : { at }),
    ...(node?.author === undefined ? {} : { by: node.author }),
    chose: [],
    id: node?.id ?? decision.ref,
    quote: node === undefined ? decision.text : nodeWords(node),
    reference: decision.ref,
    said:
      decision.kind === "person" ? (isReader(node?.author, reader) ? "you" : "person") : "agent",
    ...(node?.state === undefined ? {} : { state: node.state }),
    type: "decision",
    unsure: decision.unsure,
  };
}

/** A person's answer to `ask` as the column draws it: their first sentence word for word, or
 *  the options they chose where they wrote none (R5). */
function answerDecision(ask: TreeNode, reader: string | undefined): SpecDecision {
  const answer = ask.answer;
  if (answer === undefined) throw new Error("an answered ask with no answer");
  const written = firstSentenceOf(answer.text);
  return {
    at: answer.at,
    by: answer.by,
    chose: answer.selected,
    id: ask.id,
    question: firstLineOf(nodeWords(ask)).line,
    quote: written === "" ? answer.selected.join(", ") : written,
    ...(ask.reference === undefined ? {} : { reference: ask.reference }),
    said: isReader(answer.by, reader) ? "you" : "person",
    type: "ask",
    unsure: false,
  };
}

/** The question a statement hangs under, where that question was answered: its answer is the
 *  decision that shaped the statement, with no tuple (R5). */
function answerUnder(
  parent: SpecParent | undefined,
  nodes: ReadonlyMap<string, TreeNode>,
  reader: string | undefined
): SpecDecision | undefined {
  if (parent === undefined) return undefined;
  const node = nodes.get(parent.id);
  if (node === undefined || node.type !== "ask" || node.answer === undefined) return undefined;
  return answerDecision(node, reader);
}

/** The node a statement hangs under, where the filter draws it as no row (R2), with who made it
 *  and when, which its line says. */
function parentOf(node: TreeNode, nodes: ReadonlyMap<string, TreeNode>): SpecParent | undefined {
  const line = node.parentLine;
  if (line === undefined) return undefined;
  const parent = nodes.get(line.id);
  return {
    ...(parent?.at === undefined ? {} : { at: parent.at }),
    ...(parent?.author === undefined ? {} : { by: parent.author }),
    id: line.id,
    line: line.line,
    type: line.type,
  };
}

/**
 * The mark a statement carries, over the checks that serve it (R17).
 *
 * The checks are the statement's own children in the whole tree, where a check sits under each
 * requirement its `serves` names (R4); under the Spec filter the statement has no check under
 * it, because Spec draws the statements and no other kind (R2). That a statement has no check
 * at all is the coverage read's own gap, which is the one rule the page draws "No check serves
 * this statement" by (`GAP_LINES.no_check`, R10), with the coverage read's count beside it for
 * a read that carries the count and no gap line.
 *
 * A statement the coverage read counts a check for whose check the tree does not hold - a
 * child issue's check, which this issue's results read does not answer for - is "not run": the
 * page has no result of its own to draw, and it will not say a check passed that it cannot
 * read.
 */
function markOf(node: TreeNode, nodes: ReadonlyMap<string, TreeNode>): SpecMark {
  const checks = (nodes.get(node.id) ?? node).children.filter((child) => child.type === "check");
  if (node.gaps.includes("no_check") || (node.counts?.checks === 0 && checks.length === 0)) {
    return "no_check";
  }
  if (checks.some((check) => check.state === "failed")) return "failing";
  if (checks.length > 0 && checks.every((check) => check.state === "passed")) return "covered";
  return "not_run";
}

/** Whether `actor` is the reader writing as themselves: a person signed in as `reader`, and not
 *  an agent acting for them, whose words are the agent's (R9). It is `TreeRows.tsx`'s rule, which
 *  is not exported there; integration gives the two one copy. */
function isReader(actor: Actor | undefined, reader: string | undefined): boolean {
  return (
    reader !== undefined &&
    actor?.kind === "user" &&
    actor.id === reader &&
    (actor.act?.session ?? "") === ""
  );
}
