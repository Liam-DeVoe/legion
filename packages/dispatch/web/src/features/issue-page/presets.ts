import type { DisplayTree } from "../../api/types";
import { wordsOf } from "../refs/tree-fragment";
import {
  drawnLine,
  firstLineOf,
  heldBy,
  lineWords,
  nodeWords,
  type ParentLine,
  type TreeNode,
} from "./issue-tree";
import {
  type FILTER_SIGNAL_WORDS,
  NODE_TYPE_NAMES,
  NODE_TYPES,
  type NodeType,
  type Preset,
} from "./page-words";
import type { TreeAnswer, TreeAnswerNode } from "./tree-read/tree-read";

/**
 * The page with no filter on, and the four filters, as views of the one tree
 * (dispatch://INFRA-1196 R1, R2).
 *
 * With no filter on - Conversation - the view is the whole tree, folded to the frontier by the
 * outline (`tree-fold.ts`). The words filter (`?q=`) narrows it to the nodes whose words hold
 * what is typed together with the chain above each, which is drawn faint and marked as context
 * (`data-node-context`), so a match is read where it stands in the conversation.
 *
 * A filter draws as rows its own kinds and no other (R2): Spec the requirements; Verification
 * each requirement as context with the checks under it and their results; Plan the plan tasks;
 * Decisions the asks and the decisions. The issue itself is no row of a filter. Every other
 * parent of a drawn node - an ask, a comment, a message, a requirement under Plan - is one line
 * on its child's row (`TreeNode.parentLine`), never a row: a drawn node whose parent is no row
 * is drawn where that parent stood, under the nearest drawn node above it, or at the top. A
 * check serves several requirements and Verification draws it under each, as the outline does;
 * every other filter draws a node once, at its own place, since a plan task drawn under each
 * requirement it serves would be one step drawn several times (R26).
 *
 * Spec, Verification and Plan are each drawn from the issue's view read of that filter once the
 * page holds it (`TreeNode.views` on the issue's node, dispatch://INFRA-4062 R4,
 * `heldFilterView`): the view's query names the nodes, and each is drawn as the tree's node of
 * that id, so a row reads as it does in the outline. Until the view answers, and for Decisions
 * until its view read stands in the decisions read's place (INFRA-1253's t16c), a filter is read
 * off the tree `issue-tree.ts` builds: the kinds are R2's, and the context and the parent's line
 * are this module's.
 *
 * A node the address names outranks the filter: an address that names one node and then draws a
 * page without it would not have resolved at all (R20), so `named` is kept whatever the filter
 * draws, and counted as its kind is.
 *
 * This module is pure. It holds no React and makes no request: the rows (`IssueTree.tsx`), the
 * signals the filter row draws (`IssuePresets.tsx`) and the unit tests all read the same model.
 */

/** One of the four filters: every control of the row but Conversation, which turns them off. */
export type Filter = Exclude<Preset, "conversation">;

/** Whether `preset` is a filter rather than the page with no filter on. */
export function isFilter(preset: Preset): preset is Filter {
  return preset !== "conversation";
}

/** The kinds each filter draws as rows of its own (R2). */
const ROW_KINDS: Readonly<Record<Filter, readonly NodeType[]>> = {
  decisions: ["ask", "decision"],
  plan: ["plan_task"],
  spec: ["requirement"],
  verification: ["check", "result"],
};

/** The kinds a filter draws as context: rows that place its own, faint and marked, and drawn
 *  whether or not anything of the filter's sits under them (R2's "each requirement as
 *  context"). */
const CONTEXT_KINDS: Readonly<Record<Filter, readonly NodeType[]>> = {
  decisions: [],
  plan: [],
  spec: [],
  verification: ["requirement"],
};

/** The main kind each filter counts (R2): the criteria and requirements for Spec, the checks
 *  for Verification, the plan tasks for Plan, the asks and decisions for Decisions. */
const COUNTED_KINDS: Readonly<Record<Filter, readonly NodeType[]>> = {
  decisions: ["ask", "decision"],
  plan: ["plan_task"],
  spec: ["requirement"],
  verification: ["check"],
};

/** No node named by the address, which is every address but a deep link's. */
const EMPTY: ReadonlySet<string> = new Set();

/** What the view draws over the tree it was given. */
export interface PresetView {
  /** The ids of the rows drawn as context: with no filter on, the chain above a match of the
   *  words filter; under Verification, each requirement. */
  readonly context: ReadonlySet<string>;
  /** Whether this view is filtered at all: false for the page with no filter on and no words
   *  typed, whose fold is the frontier's alone (R1). While one is, every branch the view kept
   *  is drawn open (`IssueTree.tsx`): a filter exists to show what it matched, and the fold may
   *  not hide it. */
  readonly filtering: boolean;
  /** The decisions and constraints of the view read each row carries as a line of its own, by
   *  the row's id: never rows (R4). Empty where the view is read off the tree. */
  readonly lines: ReadonlyMap<string, readonly PresetLine[]>;
  /** How many distinct nodes of the view's main kind it draws: a check drawn under two
   *  requirements is one node, so this is what the control's own count says. */
  readonly matches: number;
  /** The tree as the view leaves it. Under a filter its children are the filter's top rows. */
  readonly root: TreeNode;
  /** Whether the issue itself is the outline's first row: with no filter on, and never under a
   *  filter, which draws its own kinds as rows and no other (R2). */
  readonly rootRow: boolean;
}

/** A node of a view read drawn as a line of a row rather than as a row (R4): a decision whose
 *  `shaped` tuple names the row's node, or a constraint the row's node's `constrained_by` tuple
 *  names. Parts U and W draw them. */
export interface PresetLine {
  /** The node's id in the view read. */
  readonly id: string;
  /** The node's own words, and the server's reading of them where the read sent one. */
  readonly text: string;
  readonly tree?: DisplayTree;
  readonly type: "constraint" | "decision";
}

/** No line on any row: a view read off the tree. */
const NO_LINES: ReadonlyMap<string, readonly PresetLine[]> = new Map();

/** Whether `node`'s words hold `wanted`, which is already lowercased; `""` holds everywhere.
 *  The words are the ones a reader reads (`nodeWords`, R1, R4's "the nodes whose words hold
 *  them"), never the stored source: `**bold** text` is found by `bold text` and not by `**`. */
function holds(node: TreeNode, wanted: string): boolean {
  return wanted === "" || nodeWords(node).toLowerCase().includes(wanted);
}

/**
 * The tree as `preset`, `query` and the address's own node leave it. With no filter on the root
 * is always drawn, because an issue whose words filter matches nothing still has to say so
 * somewhere, and it is context whenever nothing matched it.
 *
 * `memo` is the spec's summary callout, which the head draws as the issue's three sentences
 * (R8) and which is no row of the conversation (c-tree: "The memo callout is not a row of the
 * outline"), unless an address names it.
 */
export function presetView(
  root: TreeNode,
  preset: Preset,
  query: string,
  named: ReadonlySet<string> = EMPTY,
  memo?: string
): PresetView {
  const wanted = query.trim().toLowerCase();
  if (!isFilter(preset)) return conversationView(root, wanted, named, memo);
  // Decisions filters the whole tree: `decisions-entries.ts` reads each decision's members.
  if (preset === "decisions") return filterView(root, preset, wanted, named);
  const held = root.views?.[preset];
  return held === undefined
    ? filterView(root, preset, wanted, named)
    : heldFilterView(root, preset, held, wanted, named);
}

/** The page with no filter on: the whole tree but the memo, or under a words filter each match
 *  with the chain above it as context. */
function conversationView(
  root: TreeNode,
  wanted: string,
  named: ReadonlySet<string>,
  memo: string | undefined
): PresetView {
  const context = new Set<string>();
  const matched = new Set<string>();
  const prune = (node: TreeNode): TreeNode | undefined => {
    if (node.id === memo && !named.has(node.id)) return undefined;
    const children = node.children
      .map(prune)
      .filter((child): child is TreeNode => child !== undefined);
    const own = named.has(node.id) || holds(node, wanted);
    if (!own && children.length === 0) return undefined;
    if (own) matched.add(node.id);
    else context.add(node.id);
    return { ...node, children, held: heldBy(children) };
  };
  const filtering = wanted !== "";
  const pruned = prune(root);
  if (pruned !== undefined) {
    return {
      context,
      filtering,
      lines: NO_LINES,
      matches: matched.size,
      root: pruned,
      rootRow: true,
    };
  }
  // Nothing under the issue matched, so the view is the issue alone, drawn as context.
  context.add(root.id);
  return {
    context,
    filtering,
    lines: NO_LINES,
    matches: 0,
    root: { ...root, children: [], held: 0 },
    rootRow: true,
  };
}

/** One filter over the tree: its kinds as rows, each other parent a line on its child's row. */
function filterView(
  root: TreeNode,
  filter: Filter,
  wanted: string,
  named: ReadonlySet<string>
): PresetView {
  const rows = ROW_KINDS[filter];
  const contextKinds = CONTEXT_KINDS[filter];
  const counted = COUNTED_KINDS[filter];
  // Verification draws a check under every requirement it serves, as the outline does; every
  // other filter draws a node at its own place alone.
  const everyPlacement = filter === "verification";
  const context = new Set<string>();
  const matched = new Set<string>();
  // The rows `node` gives the view, and whether `node` is itself one of them: either the node
  // with its own rows under it, or, where it is no row, the rows of its branch lifted to where
  // it stood.
  const lift = (node: TreeNode): { readonly drawn: boolean; readonly rows: TreeNode[] } => {
    if (!everyPlacement && node.transcluded) return { drawn: false, rows: [] };
    const parts = node.children.map(lift);
    const own = named.has(node.id) || (rows.includes(node.type) && holds(node, wanted));
    const placing =
      !own &&
      contextKinds.includes(node.type) &&
      (wanted === "" || holds(node, wanted) || parts.some((part) => part.rows.length > 0));
    const drawn = own || placing;
    const line = drawn || !parts.some((part) => part.drawn) ? undefined : lineOfParent(node);
    const below = parts.flatMap((part) =>
      part.drawn && line !== undefined
        ? part.rows.map((row) => ({ ...row, parentLine: line }))
        : part.rows
    );
    if (!drawn) return { drawn: false, rows: below };
    if (own && counted.includes(node.type)) matched.add(node.id);
    if (placing) context.add(node.id);
    return { drawn: true, rows: [{ ...node, children: below, held: heldBy(below) }] };
  };
  // The issue is no row of a filter, and no row's parent line: the page is the issue.
  const top = root.children.flatMap((child) => lift(child).rows);
  return {
    context,
    filtering: true,
    lines: NO_LINES,
    matches: matched.size,
    root: { ...root, children: top, held: heldBy(top) },
    rootRow: false,
  };
}

/**
 * One filter drawn from its view read (`TreeNode.views`, R4): the nodes the view's query took of
 * the filter's row kinds, each drawn as the tree's node of that id, so a row reads as it does in
 * the outline, under the row the read places it under, else at the top. Verification draws each
 * statement the view holds as context, with each check under each statement it serves and each
 * result under its check. A node the tree holds no node for - a criterion, a node of an issue
 * under this one - is no row, and neither is the memo, a callout. A decision and a constraint
 * are never rows: each is a line of the row its tie names (`PresetView.lines`). The words filter
 * keeps the rows whose words hold it, as `filterView` keeps them, and a node the address names is
 * drawn whatever the filter, from the view where it holds the node and from the tree where not.
 */
function heldFilterView(
  root: TreeNode,
  filter: Exclude<Filter, "decisions">,
  answer: TreeAnswer,
  wanted: string,
  named: ReadonlySet<string>
): PresetView {
  // Each node of the tree by the id the read gives it, its first placement, and each placement
  // by that id and the node it stands under; each node by its own id, with its parent.
  const byRead = new Map<string, TreeNode>();
  const placements = new Map<string, TreeNode>();
  const byId = new Map<string, { readonly node: TreeNode; readonly parent: TreeNode }>();
  const index = (parent: TreeNode): void => {
    for (const child of parent.children) {
      if (child.readId !== undefined) {
        if (!byRead.has(child.readId)) byRead.set(child.readId, child);
        placements.set(`${child.readId} ${parent.id}`, child);
      }
      if (!byId.has(child.id)) byId.set(child.id, { node: child, parent });
      index(child);
    }
  };
  index(root);
  const kinds: readonly string[] = ROW_KINDS[filter];
  const contextKinds: readonly string[] = CONTEXT_KINDS[filter];
  const rows = answer.nodes.filter(
    (node) =>
      byRead.has(node.id) && (node.mark === "query" ? kinds : contextKinds).includes(node.supertag)
  );
  const rowIds = new Set(rows.map((node) => node.id));
  // Where each row stands: under its parent where that is a row, and under Verification a check
  // under each statement its `serves` tuples name too.
  const homes = new Map<string, string[]>();
  for (const node of rows) {
    if (node.parent !== null && rowIds.has(node.parent)) homes.set(node.id, [node.parent]);
  }
  for (const tuple of filter === "verification" ? answer.tuples : []) {
    const held = homes.get(tuple.from) ?? [];
    if (tuple.kind === "serves" && rowIds.has(tuple.to) && !held.includes(tuple.to)) {
      homes.set(tuple.from, [...held, tuple.to]);
    }
  }
  const below = new Map<string, TreeAnswerNode[]>();
  const top: TreeAnswerNode[] = [];
  for (const node of rows) {
    const at = homes.get(node.id) ?? [];
    if (at.length === 0) top.push(node);
    for (const home of at) below.set(home, [...(below.get(home) ?? []), node]);
  }
  const counted: readonly NodeType[] = COUNTED_KINDS[filter];
  const context = new Set<string>();
  const matched = new Set<string>();
  const drawnIds = new Set<string>();
  // As `filterView`'s `lift`: the node with its own rows under it, or, where it is no row, the
  // rows of its branch at its place, each carrying it as its parent's line.
  const draw = (
    node: TreeAnswerNode,
    home: TreeNode | undefined,
    above: ReadonlySet<string>
  ): { readonly drawn: boolean; readonly rows: TreeNode[] } => {
    const tree =
      (home === undefined ? undefined : placements.get(`${node.id} ${home.id}`)) ??
      byRead.get(node.id);
    if (tree === undefined || above.has(node.id)) return { drawn: false, rows: [] };
    const inner = new Set([...above, node.id]);
    const parts = (below.get(node.id) ?? []).map((child) => draw(child, tree, inner));
    const own = named.has(tree.id) || (node.mark === "query" && holds(tree, wanted));
    const placing =
      !own &&
      node.mark !== "query" &&
      (wanted === "" || holds(tree, wanted) || parts.some((part) => part.rows.length > 0));
    const drawn = own || placing;
    const line = drawn || !parts.some((part) => part.drawn) ? undefined : lineOfParent(tree);
    const kept = parts.flatMap((part) =>
      part.drawn && line !== undefined
        ? part.rows.map((row) => ({ ...row, parentLine: line }))
        : part.rows
    );
    if (!drawn) return { drawn: false, rows: kept };
    if (own && counted.includes(tree.type)) matched.add(tree.id);
    if (placing) context.add(tree.id);
    drawnIds.add(tree.id);
    return { drawn: true, rows: [{ ...tree, children: kept, held: heldBy(kept) }] };
  };
  const drawnTop = top.flatMap((node) => {
    const part = draw(node, undefined, new Set());
    const line = part.drawn ? viewParentLine(node, root, byRead) : undefined;
    return line === undefined ? part.rows : part.rows.map((row) => ({ ...row, parentLine: line }));
  });
  // A node the address names that the view does not draw, from the tree, at the top.
  const addressed = [...named].flatMap((id) => {
    const found = drawnIds.has(id) ? undefined : byId.get(id);
    if (found === undefined) return [];
    if (counted.includes(found.node.type)) matched.add(id);
    const line = found.parent === root ? undefined : lineOfParent(found.parent);
    return [
      {
        ...found.node,
        children: [],
        held: 0,
        ...(line === undefined ? {} : { parentLine: line }),
      },
    ];
  });
  const children = [...drawnTop, ...addressed];
  return {
    context,
    filtering: true,
    lines: viewLines(answer, byRead, drawnIds),
    matches: matched.size,
    root: { ...root, children, held: heldBy(children) },
    rootRow: false,
  };
}

/**
 * The line a row the view draws at the top carries for its parent in the view read, where that
 * parent is no row: the tree's node of that id, drawn as `lineOfParent` draws it, where the tree
 * holds it, and else the parent's id, kind and first line the view read answers beside the node
 * (`parent_line`), for a kind the page has a word for. The issue is no row's parent line.
 */
function viewParentLine(
  node: TreeAnswerNode,
  root: TreeNode,
  byRead: ReadonlyMap<string, TreeNode>
): ParentLine | undefined {
  if (node.parent === null || node.parent === root.id) return undefined;
  const parent = byRead.get(node.parent);
  if (parent !== undefined) return lineOfParent(parent);
  const read = node.parent_line;
  const types: readonly string[] = NODE_TYPES;
  if (read === undefined || read.supertag === "issue" || !types.includes(read.supertag)) {
    return undefined;
  }
  return {
    id: read.id,
    line: read.line,
    type: read.supertag as NodeType,
  };
}

/** The decisions and constraints of `answer` as lines of the rows the view draws, by each row's
 *  id: a decision on the row its `shaped` tuple names, a constraint on the row whose
 *  `constrained_by` tuple names it, each once. */
function viewLines(
  answer: TreeAnswer,
  byRead: ReadonlyMap<string, TreeNode>,
  drawn: ReadonlySet<string>
): ReadonlyMap<string, readonly PresetLine[]> {
  const nodes = new Map(answer.nodes.map((node) => [node.id, node]));
  const lines = new Map<string, PresetLine[]>();
  const add = (rowId: string, node: TreeAnswerNode | undefined, type: PresetLine["type"]) => {
    const row = byRead.get(rowId)?.id;
    if (node?.supertag !== type || row === undefined || !drawn.has(row)) return;
    const held = lines.get(row) ?? [];
    if (held.some((line) => line.id === node.id)) return;
    const line: PresetLine = {
      id: node.id,
      text: node.text,
      ...(node.text_tree === undefined ? {} : { tree: node.text_tree }),
      type,
    };
    lines.set(row, [...held, line]);
  };
  for (const tuple of answer.tuples) {
    if (tuple.kind === "shaped") add(tuple.to, nodes.get(tuple.from), "decision");
    if (tuple.kind === "constrained_by") add(tuple.from, nodes.get(tuple.to), "constraint");
  }
  return lines;
}

/**
 * The line a node that is no row of a filter is drawn as on the row of each child the filter
 * draws (`TreeNode.parentLine`): cut by R1's first-line rule from the words of the node's first
 * block (`lineWords`), so a heading is never run into the paragraph after it, and drawn as a
 * row's first line is (`drawnLine`): an ellipsis only where the cut ended inside a sentence. A
 * node with no words - no text, or a reading none of whose blocks holds a word - is named by the
 * page's word for its kind (`NODE_TYPE_NAMES`), never by its id, which is no name a reader reads
 * (R22).
 */
function lineOfParent(node: TreeNode): ParentLine {
  return {
    id: node.id,
    line: parentLineOf(node),
    // The parent's own address, which makes its line the link a reader follows to it (R18, and
    // R26: a step's row reaches the statement it serves).
    ...(node.reference === undefined ? {} : { reference: node.reference }),
    type: node.type,
  };
}

/** The one line a node that is no row of a filter is drawn as on its child's row: R1's first
 *  line of its first block's words (`lineWords`), and the page's word for its kind where those
 *  hold none (R22). */
function parentLineOf(node: TreeNode): string {
  const words = lineWords(node);
  return words === "" ? NODE_TYPE_NAMES[node.type] : drawnLine(firstLineOf(words));
}

/** What one control of the filter row draws after its name (R2): its count, then, where the
 *  picture draws one, the noun the count is in or what of it waits on the reader. */
export interface PresetSignal {
  /** How many nodes of the view's main kind it draws (R2), or for Conversation how many entries
   *  the Contents list holds, as the prototype's signal counts them. */
  readonly count: number;
  /** The part of the count that asks something of the reader, with the word it is drawn with:
   *  the checks that failed, the decisions waiting for the reader's review. */
  readonly flag?: { readonly count: number; readonly word: keyof typeof FILTER_SIGNAL_WORDS };
  /** The noun the count is drawn in, where the picture draws one ("13 steps"). */
  readonly unit?: keyof typeof FILTER_SIGNAL_WORDS;
}

/** The distinct nodes of `type` the view draws that `counts` holds for. */
function countOf(root: TreeNode, type: NodeType, counts: (node: TreeNode) => boolean): number {
  const found = new Set<string>();
  const walk = (node: TreeNode): void => {
    if (node.type === type && counts(node)) found.add(node.id);
    for (const child of node.children) walk(child);
  };
  walk(root);
  return found.size;
}

/** The decisions of the view that restate a person's answer: the sections of `decisions.md`
 *  the tree hangs under the answered question they name. The Decisions filter draws that
 *  answer as the decision and the section as no entry (R2, R9, `decisions-filter/
 *  decisions-parts.ts`), so its count leaves them out. */
function restatedAnswers(root: TreeNode): number {
  const found = new Set<string>();
  const walk = (node: TreeNode): void => {
    for (const child of node.children) {
      if (node.type === "ask" && node.answer !== undefined && child.type === "decision") {
        found.add(child.id);
      }
      walk(child);
    }
  };
  walk(root);
  return found.size;
}

/** The signal each filter's control draws under `query`, which its count follows. */
export function filterSignals(
  root: TreeNode,
  query: string
): Readonly<Record<Filter, PresetSignal>> {
  const view = (filter: Filter) => presetView(root, filter, query);
  // Once a filter's view read is held, its count is that read's own nodes of its main kind whose
  // words hold the query (R4); until then, what the view read off the tree draws.
  const count = (filter: Exclude<Filter, "decisions">): number => {
    const held = root.views?.[filter];
    if (held === undefined) return view(filter).matches;
    const kinds: readonly string[] = COUNTED_KINDS[filter];
    const wanted = query.trim().toLowerCase();
    const words = (node: TreeAnswerNode): string =>
      node.text_tree === undefined ? node.text : wordsOf(node.text_tree);
    return new Set(
      held.nodes.flatMap((node) =>
        node.mark === "query" &&
        kinds.includes(node.supertag) &&
        (wanted === "" || words(node).toLowerCase().includes(wanted))
          ? [node.id]
          : []
      )
    ).size;
  };
  const verification = view("verification");
  const decisions = view("decisions");
  // A check's state is the page's one reading of it at the commit the page names
  // (`tree-drafts.ts`'s `checkStanding`), so this flag and the Verification body count the same
  // checks as failed.
  const failing = countOf(verification.root, "check", (node) => node.state === "failed");
  // The decisions of the review list (R35): an agent's decision that waits and that a link ties
  // to a statement of this issue, which is what the tree marks `waiting` (`decision-review.ts`'s
  // `inReviewList`), so the signal, the list and the header's count cannot disagree.
  const toReview = countOf(decisions.root, "decision", (node) => node.waiting);
  return {
    decisions: {
      count: decisions.matches - restatedAnswers(decisions.root),
      ...(toReview === 0 ? {} : { flag: { count: toReview, word: "to_review" } }),
    },
    plan: { count: count("plan"), unit: "steps" },
    spec: { count: count("spec") },
    verification: {
      count: count("verification"),
      ...(failing === 0 ? {} : { flag: { count: failing, word: "failing" } }),
    },
  };
}
