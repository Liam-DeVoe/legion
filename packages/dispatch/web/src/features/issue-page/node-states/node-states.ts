import { overtakenLineOf } from "../issue-address";
import type { NodeChip, NodeLife, ParentLine, TreeNode } from "../issue-tree";
import { isOvertaken, isProposed, type OpenedBranches } from "../tree-fold";

/**
 * A node's life and who may read it, on the issue's tree (dispatch://INFRA-4063 R1, R2, R5, R7).
 * Pure, over the tree `issue-tree.ts` assembles, which holds neither: the tree read answers each
 * node's state and visibility by its id, and `stampStates` puts them on every placement of that
 * node and on every chip and parent line that names it, and closes each overtaken node with
 * every node under it, so the rows, the fold and the counts all read one tree.
 *
 * `issue-tree.ts` passes the tree it assembles through this module, so this module imports no
 * value of that one, which Biome's `noImportCycles` would refuse: its caller hands it `heldBy`,
 * the folded count that counts a parent's overtaken children as one row, and a node's life is
 * read through `tree-fold.ts`'s predicates, since that module imports no value at all.
 */

/** What the tree read says of one node beside its kind: its life, where it has one, and whether
 *  it is reserved for its author, who is the reader. A live or settled node that every reader
 *  may see has neither. */
export interface NodeStates {
  readonly life?: NodeLife;
  readonly onlyYou?: true;
}

/** The marks `from` holds, each only where it holds one, so whatever is given them carries no
 *  key it has no value for; undefined where it holds none. */
function marksOf(from: NodeStates | undefined): NodeStates | undefined {
  if (from?.life === undefined && from?.onlyYou === undefined) return undefined;
  return {
    ...(from.life === undefined ? {} : { life: from.life }),
    ...(from.onlyYou === undefined ? {} : { onlyYou: from.onlyYou }),
  };
}

/** The marks of each node of `root` that has a reference and a mark, as `marksAt` reads them,
 *  by that reference: what a chip naming the node carries. */
function marksByReference(
  root: TreeNode,
  marksAt: (node: TreeNode) => NodeStates | undefined
): ReadonlyMap<string, NodeStates> {
  const found = new Map<string, NodeStates>();
  const walk = (node: TreeNode): void => {
    const marks = marksAt(node);
    if (node.reference !== undefined && marks !== undefined) found.set(node.reference, marks);
    for (const child of node.children) walk(child);
  };
  walk(root);
  return found;
}

/**
 * `root` with the states the tree read gives its nodes, by node id (R1). Each node takes the
 * marks its entry holds and no other: a live or settled node has none. A node that is overtaken
 * is `closed`, and so is every node under it (R5). A proposed node and a closed one draw no gap,
 * the page's own included (R2, R5): a gap says what the issue's work still lacks, and neither
 * is part of that work. Each chip, in `chips` and in `alsoUnder`, carries the marks of the node
 * whose reference it names, and a parent line those of the node it names, so a node is marked
 * wherever it is drawn (R2, R7). Every node's `held` is counted again by `heldBy` over its new
 * children, which counts a parent's overtaken children as the one row that folds them.
 *
 * With no states at all it answers `root` itself.
 */
export function stampStates(
  root: TreeNode,
  states: ReadonlyMap<string, NodeStates>,
  heldBy: (children: readonly TreeNode[]) => number
): TreeNode {
  if (states.size === 0) return root;
  const byReference = marksByReference(root, (node) => marksOf(states.get(node.id)));
  const chipWith = (chip: NodeChip): NodeChip => {
    const marks = byReference.get(chip.ref);
    return marks === undefined ? chip : { ...chip, ...marks };
  };
  const lineWith = (line: ParentLine): ParentLine => {
    const marks = marksOf(states.get(line.id));
    return marks === undefined ? line : { ...line, ...marks };
  };
  const stamp = (node: TreeNode, under: boolean): TreeNode => {
    const own: TreeNode = { ...node, ...marksOf(states.get(node.id)) };
    const closed = under || isOvertaken(own);
    const children = own.children.map((child) => stamp(child, closed));
    return {
      ...own,
      alsoUnder: own.alsoUnder.map(chipWith),
      children,
      chips: own.chips.map(chipWith),
      ...(closed ? { closed: true } : {}),
      gaps: closed || isProposed(own) ? [] : own.gaps,
      held: heldBy(children),
      ...(own.parentLine === undefined ? {} : { parentLine: lineWith(own.parentLine) }),
    };
  };
  return stamp(root, false);
}

/**
 * What no count of what waits on the reader counts (dispatch://INFRA-4063 R3): for each node at
 * or under an overtaken one, its reference where it has one and, for an ask, its id, since the
 * header's count matches an open ask by its id and a decision by its reference. An ask that is
 * a block is a node by its block's id, so the ask's own id (its `source`) is in beside it.
 */
export function overtakenRefs(root: TreeNode): ReadonlySet<string> {
  const refs = new Set<string>();
  const walk = (node: TreeNode): void => {
    if (node.closed === true) {
      if (node.reference !== undefined) refs.add(node.reference);
      if (node.type === "ask") {
        refs.add(node.id);
        if (node.source?.kind === "ask") refs.add(node.source.id);
      }
    }
    for (const child of node.children) walk(child);
  };
  walk(root);
  return refs;
}

/** The marks of each node of `root` that has a reference and a mark, by that reference: what
 *  a place naming a node by its address draws beside it (R2, R7). */
export function statesByReference(root: TreeNode): ReadonlyMap<string, NodeStates> {
  return marksByReference(root, marksOf);
}

/** The node whose line of overtaken children must be open for the node at the end of `path` to
 *  be drawn (R6, `issue-address.ts`'s `overtakenLineOf`): the parent of the first overtaken
 *  node the path runs through; undefined where it runs through none. */
export function lineFor(path: readonly TreeNode[] | undefined): string | undefined {
  return overtakenLineOf(path);
}

/**
 * `opened` with the line of every overtaken node `path` runs through opened (R6), so the node
 * at its end is drawn as a row: an address, a chip, a Contents entry, `q` or `w` that names an
 * overtaken node or a node under one opens the line that holds it. An overtaken node under
 * another is behind two lines, and both open. `opened` itself where the path runs through no
 * overtaken node.
 */
export function withLine(
  opened: OpenedBranches,
  path: readonly TreeNode[] | undefined
): OpenedBranches {
  const line = lineFor(path);
  if (line === undefined || path === undefined) return opened;
  // The rest of the path from the line's own node, which may stand above another line.
  const below = path.slice(path.findIndex((node) => node.id === line) + 1);
  return withLine({ ...opened, overtaken: new Set(opened.overtaken).add(line) }, below);
}

/** The id of each node of `root` that holds an overtaken child, in reading order: each node a
 *  line of overtaken nodes is drawn under (`tree-fold.ts`'s `OvertakenRow`, R5). Under the words
 *  filter the view holds only its matches and the chain above each, so each of these lines in
 *  that view holds a match, which the outline draws open (R6). */
export function linesIn(root: TreeNode): readonly string[] {
  const parents: string[] = [];
  const walk = (node: TreeNode): void => {
    if (node.children.some(isOvertaken)) parents.push(node.id);
    for (const child of node.children) walk(child);
  };
  walk(root);
  return parents;
}
