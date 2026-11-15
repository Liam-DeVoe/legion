import type { Actor, Decision, IssueDecisions } from "../../../api/types";
import { compareTimestamps, isTimestamp } from "../../../lib/timestamps";
import { blockFragment } from "../../refs/block-reference";
import { buildIssuePath, parseNodeReference } from "../../refs/routes";
import {
  firstLineOf,
  nodeWords,
  type ParentLine,
  type TreeAsk,
  type TreeNode,
} from "../issue-tree";
import { freeze } from "../page-structure";

/**
 * The words the Decisions filter draws of its own (dispatch://INFRA-1196 R2, R22, R35).
 * Integration moves them into `page-words.ts`. None is a sentence about the page or about
 * Dispatch, and no sentence explains the four parts: their headings are the whole of it.
 */
export const DECISIONS_WORDS = freeze({
  /** The fourth part's heading, which is the control that unfolds it, after the count of the
   *  decisions it holds; the words agree with the count (R2: "folded at the foot, the agents'
   *  decisions that no statement rests on"). */
  agents_one: "agent decision that no statement rests on",
  agents_many: "agent decisions that no statement rests on",
  /** After the name of the person who declined a question, so the row reads as a question that
   *  person declined and never as an answer (R2). */
  declined: "declined to answer",
  /** Before the statements a decision's `shaped` ties name (the picture's "informs R4 R21"). */
  informs: "informs",
  /** The link from an entry to the node that holds it, in the conversation. */
  in_conversation: "in the conversation",
  /** The headings of the first three parts, in the order the filter draws them (R2). */
  parts: {
    open_questions: "Open questions",
    review: "Waiting for your review",
    yours: "Your decisions",
  },
  /** The accessible name of each state's shape: the state and the kind it stands on, as every
   *  other state's shape is named (`GLYPH_NAMES`). */
  shapes: {
    kept: "Kept decision",
    replaced: "Replaced decision",
    waiting_review: "Waiting for review decision",
  },
  /** How an agent's decision stands, wherever it is drawn (R2): one that waits for a person's
   *  review, one a person kept, which is followed by that person's name, and a replaced one. */
  states: {
    kept: "Kept by",
    replaced: "Replaced",
    waiting_review: "Waiting for review",
  },
} as const);

/**
 * The Decisions filter's four parts, read from the tree, the decisions read and the issue's
 * asks (dispatch://INFRA-1196 R2, R9, R35).
 *
 * The filter is one list in four parts, each under its own heading and in this order (R2):
 *
 * 1. **The open questions.** Every ask of the issue that is still open, those whose turn is the
 *    reader's first, each group in the order the outline draws them, which is the order the
 *    next-question key walks.
 * 2. **The decisions that wait for the reader's review** (R35): each agent decision that waits
 *    and that a `shaped` tie names a statement of this issue for, newest first. The list is
 *    read from the ties alone (`inReviewList`): a decision that waits and is tied to nothing,
 *    or to a node that is no statement, is no entry of it.
 * 3. **The reader's own decisions**, newest first: each closed ask by the time it closed - an
 *    answered one is the person's decision itself (dispatch://INFRA-1150 R1), and a declined
 *    one reads as a question the person declined, never as an answer - and each agent decision
 *    a statement rests on that a person kept, by the time it was kept.
 * 4. **The agents' decisions that no statement rests on**, folded at the foot: an agent
 *    decision no `shaped` tie names a statement for, and a replaced one, which no statement
 *    stands on any longer (R5: "A replaced decision draws no line").
 *
 * What the filter draws none of is an agent's reading of a person's answer (R2, R9: "No
 * restatement of a person's answer appears anywhere in the view"). Today's nearest thing to a
 * `reading` node is a section of `decisions.md` the server marks a person's because its blocks
 * name an ask (`Decision.kind`, `api/decisions.go`): where that ask is an answered ask of this
 * issue, the section restates the answer, the ask's own row is the decision, and the section is
 * drawn under the ask in the outline and nowhere in this filter (`isReading`).
 *
 * This module is pure. It holds no React and makes no request, so the body
 * (`DecisionsFilterBody.tsx`), the count of what waits on the reader and the unit tests read
 * one model.
 *
 * ## What today's reads hold, and what they do not
 *
 * - **The `shaped` tie** is a link from a block of a decision to a block of the spec
 *   (`Decision.links`), which is the edge as today's read carries it; the tuple is
 *   dispatch://INFRA-1150's. A tie counts where its far end is a statement of this issue, read
 *   from the tree.
 * - **When a decision was made** is the time its words last changed (`Decision.changed_at`);
 *   the decisions read holds no other time for one.
 * - **An agent decision's author, its level of uncertainty, its reason and its readings** are
 *   dispatch://INFRA-1150's: the read holds no author and one yes or no, `unsure`, which the
 *   row draws as the one word it has.
 */

/** The four parts, in the order the filter draws them (R2). */
export const DECISIONS_PART_ORDER = ["open_questions", "review", "yours", "agents"] as const;

/** One part of the filter, by its own token, which is its `data-decisions-part`. */
export type DecisionsPart = (typeof DECISIONS_PART_ORDER)[number];

/** How an agent's decision stands (R2), by the token its word is declared under. */
export type DecisionStanding = keyof typeof DECISIONS_WORDS.states;

/** One statement a decision's `shaped` tie names, as the decision's row draws it. */
export interface ShapedStatement {
  /** The statement's node id. */
  readonly id: string;
  /** Its first line, as R1 cuts a row's: the page draws one line for it here. */
  readonly line: string;
  /** Its address in the conversation, where it has one. */
  readonly path?: string;
  /** Whether the decision changed after it first named the statement and the statement has not
   *  changed since (`DecisionLink.stale`). */
  readonly stale: boolean;
}

/** What every entry holds. */
interface EntryBase {
  /** The moment the entry is ordered by, where the model holds one: when an ask closed, or was
   *  asked while it is open, when a decision was kept, and otherwise when it was made. */
  readonly at?: string;
  /** The node, as the whole tree holds it at the place it is edited. */
  readonly node: TreeNode;
  /** The node's parent in the conversation as one line on its row (R2), since no row of this
   *  filter stands under another; absent for a node that hangs under the issue itself. */
  readonly parent?: ParentLine;
  /** The address of the node in the conversation, which the entry links to. */
  readonly path?: string;
}

/** One ask of the filter: an open question, or a closed one. */
export interface AskEntry extends EntryBase {
  /** The person who declined the question, with the note they wrote where they wrote one;
   *  absent for every ask that was not declined. */
  readonly declined?: { readonly by: Actor; readonly note?: string };
  readonly kind: "ask";
}

/** One decision of the filter. */
export interface DecisionEntry extends EntryBase {
  /** The person who kept it (`DecisionKeep.kept_by`), where it stands kept. */
  readonly keptBy?: Actor;
  readonly kind: "decision";
  /** The statements of this issue its `shaped` ties name, each once, in the ties' own order. */
  readonly shapes: readonly ShapedStatement[];
  /** How it stands, where the model gives it one of the three states R2 names. */
  readonly standing?: DecisionStanding;
}

/** One entry of the filter. */
export type DecisionsEntry = AskEntry | DecisionEntry;

/** The four parts, each in the order it is drawn. */
export interface DecisionsParts {
  readonly agents: readonly DecisionEntry[];
  readonly open_questions: readonly AskEntry[];
  readonly review: readonly DecisionEntry[];
  readonly yours: readonly DecisionsEntry[];
}

/** What the model reads of an ask beside its node: how it was closed. */
export type DecisionsAsk = Pick<TreeAsk, "id" | "resolution">;

/**
 * Whether `decision` is an entry of the review list (R35): an agent's decision that waits for a
 * person's review - neither kept as it reads now nor replaced, which is the server's
 * `waiting_review` (dispatch://INFRA-1150 R2) - and that a `shaped` tie names a statement of
 * this issue for. `isStatement` answers whether an address names one.
 *
 * It is the one rule that decides the list, so the count of what waits on the reader (R13), the
 * filter's "to review" signal and the node's own `waiting` read it too and cannot disagree with
 * the list.
 */
export function inReviewList(
  decision: Pick<Decision, "links" | "waiting_review">,
  isStatement: (reference: string) => boolean
): boolean {
  return (
    decision.waiting_review &&
    (decision.links ?? []).some((link) => link.to.ref !== undefined && isStatement(link.to.ref))
  );
}

/**
 * The address of `node` in the conversation: an ask's own, and for a block of a document the
 * issue's own address with the block as its fragment, which opens the outline on that block's
 * node (`issue-address.ts`). A decision's own address opens `decisions.md` at its heading,
 * which is the document and not the conversation.
 */
export function conversationPath(node: Pick<TreeNode, "reference">): string | undefined {
  const route = node.reference === undefined ? undefined : parseNodeReference(node.reference);
  if (route === undefined || !("key" in route)) return undefined;
  if (route.kind === "ask" || route.kind === "comment" || route.kind === "message") {
    return buildIssuePath({ id: route.id, key: route.key, kind: route.kind });
  }
  const block = "block" in route ? route.block : undefined;
  return block === undefined
    ? undefined
    : `${buildIssuePath({ key: route.key, kind: "issue" })}${blockFragment(block.id)}`;
}

/**
 * The first entry that waits on the reader, in the filter's order: an open question whose turn
 * is the reader's, else a decision of the review list. It is what the header's count of what
 * waits puts the focus on (R13).
 */
export function firstWaiting(parts: DecisionsParts): string | undefined {
  return (
    parts.open_questions.find((entry) => entry.node.waiting)?.node.id ?? parts.review[0]?.node.id
  );
}

/**
 * The filter's four parts.
 *
 * `root` is the whole tree, which holds each ask and each decision with its words and each
 * statement a tie may name; `view` is the tree as the Decisions filter leaves it
 * (`presetView`), whose nodes are the ones the filter by words kept and the one the address
 * names. `nodeIdOf` is how this issue addresses a node (`tree-drafts.ts`'s `treeContext`), so
 * a tie resolves to the node it names however its address is written.
 */
export function decisionsParts({
  asks,
  decisions,
  nodeIdOf,
  root,
  view,
}: {
  /** The issue's asks, for how each closed one was closed. */
  readonly asks: readonly DecisionsAsk[] | undefined;
  readonly decisions: IssueDecisions | undefined;
  readonly nodeIdOf: (reference: string) => string | undefined;
  /** The whole tree. */
  readonly root: TreeNode;
  /** The tree as the Decisions filter leaves it; the whole tree where none is given. */
  readonly view?: TreeNode;
}): DecisionsParts {
  const placed = placements(root);
  const kept = view === undefined ? undefined : idsOf(view);
  const rows = new Map((decisions?.decisions ?? []).map((row) => [row.ref, row]));
  const closings = new Map((asks ?? []).map((ask) => [ask.id, ask.resolution]));
  const isStatement = (reference: string): boolean =>
    placed.get(nodeIdOf(reference) ?? "")?.node.type === "requirement";
  // Each answered ask by its own id, which is how a decision names the question it restates
  // (`Decision.asks`): an ask the server indexed from a block of a document is drawn as that
  // block, so its node id is not its id.
  const answered = new Set<string>();
  for (const { node } of placed.values()) {
    const id = askIdOf(node);
    if (id !== undefined && node.answer !== undefined) answered.add(id);
  }

  const open: AskEntry[] = [];
  const review: DecisionEntry[] = [];
  const yours: DecisionsEntry[] = [];
  const agents: DecisionEntry[] = [];
  for (const { node, parent } of placed.values()) {
    if (kept !== undefined && !kept.has(node.id)) continue;
    const base = {
      node,
      ...(parent === undefined ? {} : { parent }),
      ...pathOf(node),
    };
    if (node.type === "ask") {
      const closing = closings.get(askIdOf(node) ?? "");
      // `question` is the ask's own `state === "open"` (`tree-drafts.ts`'s `addAsks`).
      if (node.question) {
        open.push({ ...base, ...timeOf(node.at), kind: "ask" });
        continue;
      }
      const note = closing?.reason ?? "";
      yours.push({
        ...base,
        ...timeOf(node.answer?.at ?? closing?.at ?? node.at),
        ...(node.state !== "declined" || closing === undefined
          ? {}
          : { declined: { by: closing.actor, ...(note === "" ? {} : { note }) } }),
        kind: "ask",
      });
      continue;
    }
    if (node.type !== "decision") continue;
    const row = rows.get(node.reference ?? "");
    if (row === undefined) {
      // A decision the decisions read holds no row for, which is a tree one read ahead of it.
      agents.push({ ...base, ...timeOf(node.at), kind: "decision", shapes: [] });
      continue;
    }
    if (isReading(row, answered)) continue;
    const shapes = shapesOf(row, nodeIdOf, placed);
    const standing = standingOf(row);
    const entry: DecisionEntry = {
      ...base,
      ...timeOf(row.changed_at ?? node.at),
      ...(standing !== "kept" || row.kept === null
        ? {}
        : { keptBy: { id: row.kept.kept_by, kind: "user" } }),
      kind: "decision",
      shapes,
      ...(standing === undefined ? {} : { standing }),
    };
    if (row.kind !== "agent") {
      // A section the server marks a person's that restates no answer of this issue: it names
      // an ask the tree does not hold, or one nobody answered. The server's word for whose it
      // is stands, since no other read says (dispatch://INFRA-1150's `decided_by`).
      yours.push(entry);
    } else if (row.replaced) {
      agents.push(entry);
    } else if (inReviewList(row, isStatement)) {
      review.push(entry);
    } else if (shapes.length === 0) {
      agents.push(entry);
    } else {
      // An agent's decision a statement rests on that does not wait: a person kept it as it
      // reads now, and it is ordered by the time they did.
      yours.push({ ...entry, ...timeOf(row.kept?.kept_at ?? row.changed_at ?? node.at) });
    }
  }
  return {
    agents: newestFirst(agents),
    // The questions whose turn is the reader's stand first, and each group keeps the order the
    // outline draws it in.
    open_questions: [
      ...open.filter((entry) => entry.node.waiting),
      ...open.filter((entry) => !entry.node.waiting),
    ],
    review: newestFirst(review),
    yours: newestFirst(yours),
  };
}

/** A node at the place it is edited, with its parent as the one line a row draws for it. */
interface Placement {
  readonly node: TreeNode;
  readonly parent?: ParentLine;
}

/** Every node of the tree by its id, in reading order, at its first placement, which is where it
 *  is edited (`TreeNode.transcluded`), with the parent it hangs under there. The issue itself is
 *  no parent a row names: the page is the issue. */
function placements(root: TreeNode): ReadonlyMap<string, Placement> {
  const placed = new Map<string, Placement>();
  const walk = (node: TreeNode, above: TreeNode | undefined): void => {
    if (!placed.has(node.id)) {
      placed.set(node.id, {
        node,
        ...(above === undefined || above === root
          ? {}
          : {
              parent: {
                id: above.id,
                line: above.text === "" ? above.id : firstLineOf(nodeWords(above)).line,
                type: above.type,
              },
            }),
      });
    }
    for (const child of node.children) walk(child, node);
  };
  walk(root, undefined);
  return placed;
}

/** The id of every node `view` holds. */
function idsOf(view: TreeNode): ReadonlySet<string> {
  const ids = new Set<string>();
  const walk = (node: TreeNode): void => {
    ids.add(node.id);
    for (const child of node.children) walk(child);
  };
  walk(view);
  return ids;
}

/** The ask a node is, by the ask's own id; undefined for every other node. */
function askIdOf(node: TreeNode): string | undefined {
  if (node.type !== "ask" || node.reference === undefined) return undefined;
  const route = parseNodeReference(node.reference);
  return route?.kind === "ask" ? route.id : undefined;
}

/** `at` as an entry's time, where it is a time the page can order by. */
function timeOf(at: string | null | undefined): { readonly at?: string } {
  return at == null || !isTimestamp(at) ? {} : { at };
}

/** The entry's address in the conversation, where its node has one. */
function pathOf(node: TreeNode): { readonly path?: string } {
  const path = conversationPath(node);
  return path === undefined ? {} : { path };
}

/**
 * Whether `row` is an agent's reading of a person's answer, which the filter draws none of
 * (R2, R9): a section the server marks a person's - its blocks name an ask - where one of those
 * asks is an answered ask of this issue. The ask's own row is that decision, in the person's
 * words.
 */
function isReading(row: Decision, answered: ReadonlySet<string>): boolean {
  return row.kind === "person" && (row.asks ?? []).some((ask) => answered.has(ask));
}

/** How a decision stands, in the three states R2 names, read as the server reads them
 *  (dispatch://INFRA-1150 R2): replaced first, then waiting, then kept as it reads now. A
 *  decision in none of them draws no state. */
function standingOf(row: Decision): DecisionStanding | undefined {
  if (row.replaced) return "replaced";
  if (row.waiting_review) return "waiting_review";
  return row.kept === null ? undefined : "kept";
}

/** The statements of this issue `row`'s ties name, each once, in the ties' own order. */
function shapesOf(
  row: Decision,
  nodeIdOf: (reference: string) => string | undefined,
  placed: ReadonlyMap<string, Placement>
): readonly ShapedStatement[] {
  const shapes = new Map<string, ShapedStatement>();
  for (const link of row.links ?? []) {
    const statement =
      link.to.ref === undefined ? undefined : placed.get(nodeIdOf(link.to.ref) ?? "");
    if (statement === undefined || statement.node.type !== "requirement") continue;
    const node = statement.node;
    const held = shapes.get(node.id);
    shapes.set(node.id, {
      id: node.id,
      line: node.text === "" ? node.id : firstLineOf(nodeWords(node)).line,
      ...pathOf(node),
      stale: link.stale || held?.stale === true,
    });
  }
  return [...shapes.values()];
}

/** `entries` with the newest first. An entry the model holds no time for stands last, and two
 *  entries of one instant keep the order the outline draws them in, so the same reads always
 *  draw the same list. */
function newestFirst<Entry extends EntryBase>(entries: readonly Entry[]): readonly Entry[] {
  return entries
    .map((entry, index) => ({ entry, index }))
    .sort((one, other) => {
      const left = one.entry.at;
      const right = other.entry.at;
      if (left === undefined || right === undefined) {
        return left === right ? one.index - other.index : left === undefined ? 1 : -1;
      }
      return compareTimestamps(right, left) || one.index - other.index;
    })
    .map(({ entry }) => entry);
}
