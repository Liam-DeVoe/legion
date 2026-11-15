import { parseNodeReference } from "../../refs/routes";
import type { TreeNode } from "../issue-tree";

/**
 * The plan's tasks as ordered steps (dispatch://INFRA-1196 R26).
 *
 * A step stands after every task in its `needs`. The order is stable: where the `needs` leave
 * two tasks free to stand in either order, the plan's own order decides, so a plan already
 * written in an order its `needs` allow is drawn in that order and a reader who knows the plan
 * document recognises it.
 *
 * Tasks that need each other, directly or through the rest, are one cycle: no order can put any
 * of them after every task it needs, so they are one group, which the page draws as one line
 * that lists them (R26). Everything that waits on a cycle keeps its place after it.
 *
 * Today's reads hold no position of their own. `needs` is a `block[]` attribute of the plan's
 * own blocks (`packages/envoy/internal/dispatch/pmdoc/schema/blocks.json:85-89`), which the
 * tree draws as a chip on the task's row (`tree-drafts.ts`'s `placeBlocks`), and the position
 * the plan read gives each task (R26, dispatch://INFRA-1253 R11) waits on that read. So the
 * order is worked out here, from the edges the model does hold, and a task whose `needs` names a
 * task this plan does not hold waits on nothing: its row says the target is gone or names an
 * address nothing answers to (`NodeRow.tsx`'s `Chip`), and the order is the plan's own.
 *
 * The module is pure: it holds no React, reads no model of its own and makes no request.
 */

/** One step of the plan: a task the order puts after every task in its `needs`. */
export interface PlanStep {
  readonly kind: "step";
  readonly task: TreeNode;
}

/** The tasks of one cycle, in the plan's own order. The page draws them as one line that lists
 *  them, and none of them is a step, because no step's place can be given to any of them. */
export interface PlanCycle {
  readonly kind: "cycle";
  readonly tasks: readonly TreeNode[];
}

/** One place in the plan's order: a step, or a cycle the page draws as one line. */
export type PlanEntry = PlanCycle | PlanStep;

/** The document part of a block node's id, `<slug>#b-<block id>` (`tree-drafts.ts`'s
 *  `blockNodeId`), and `""` for a node that is no block of a document. */
function documentOf(id: string): string {
  const at = id.lastIndexOf("#b-");
  return at === -1 ? "" : id.slice(0, at);
}

/**
 * The id of the node `reference` points at, as this issue addresses it, or undefined where the
 * reference names no block at all.
 *
 * `needs` points at a plan task of the task's own document (`blocks.json`: `targets:
 * ["plan_task"]`, `scope: "document"`), so the block id the reference carries is looked up in
 * `task`'s own document. The reference's own document part is not read, because the issue's
 * primary document is spelled `spec` in a reference and by its slug in a node's id, and one
 * block id is unique inside one document alone.
 */
function neededId(task: TreeNode, reference: string): string | undefined {
  const route = parseNodeReference(reference);
  const block = route !== undefined && "block" in route ? route.block : undefined;
  return block === undefined ? undefined : `${documentOf(task.id)}#b-${block.id}`;
}

/** Puts `group` into `ready`, which is kept in the plan's own order: the next group taken is the
 *  one holding the earliest task, so two groups free to stand in either order keep that order. */
function readyFor(ready: number[], group: number): void {
  const at = ready.findIndex((held) => held > group);
  ready.splice(at === -1 ? ready.length : at, 0, group);
}

/**
 * `nodes` as the plan's order draws them: each a step of its own, or, for tasks that need each
 * other, one cycle holding them all. `nodes` are the rows the Plan filter drew, in the plan's
 * own order, and a node that is no plan task needs nothing and is needed by nothing, so it keeps
 * the place the plan gave it.
 */
export function planOrder(nodes: readonly TreeNode[]): readonly PlanEntry[] {
  const at = new Map<string, number>();
  nodes.forEach((node, index) => {
    if (!at.has(node.id)) at.set(node.id, index);
  });
  // What each node needs, by index. A plan task's chips are the `needs` its block holds
  // (`tree-drafts.ts`'s `placeBlocks`), so a chip naming a plan task of the same document is one
  // of them; a chip naming anything else - a document the task's own words cite, a task this
  // plan does not hold - is no edge, and puts the task under no order.
  const needs = nodes.map((node) =>
    node.type !== "plan_task"
      ? []
      : node.chips.flatMap((chip) => {
          const needed = neededId(node, chip.ref);
          const index = needed === undefined ? undefined : at.get(needed);
          return index === undefined || nodes[index]?.type !== "plan_task" ? [] : [index];
        })
  );
  // Every task each task needs, through the rest as well as directly, which is what tells a
  // cycle from a chain: a task is in a cycle with every task it needs that needs it back.
  const reaches = nodes.map((_node, from) => {
    const found = new Set<number>();
    const walking = [...(needs[from] ?? [])];
    while (walking.length > 0) {
      const next = walking.pop();
      if (next === undefined || found.has(next)) continue;
      found.add(next);
      walking.push(...(needs[next] ?? []));
    }
    return found;
  });
  // The group each task is in, named by the earliest task in it: a task on its own, or every
  // task of one cycle, which all answer to the same name because needing each other both ways
  // holds between every pair of them.
  const groupOf = nodes.map((_node, index) => {
    let earliest = index;
    for (const other of reaches[index] ?? []) {
      if (other < earliest && reaches[other]?.has(index) === true) earliest = other;
    }
    return earliest;
  });
  const members = new Map<number, number[]>();
  groupOf.forEach((group, index) => {
    const held = members.get(group);
    if (held === undefined) members.set(group, [index]);
    else held.push(index);
  });
  // Which groups each group waits on, and which wait on it. An edge inside one group is no
  // wait: that is what being one group means.
  const waitsOn = new Map<number, Set<number>>();
  const holdsUp = new Map<number, Set<number>>();
  needs.forEach((needed, index) => {
    const group = groupOf[index] ?? index;
    for (const one of needed) {
      const before = groupOf[one] ?? one;
      if (before === group) continue;
      const waiting = waitsOn.get(group) ?? new Set<number>();
      waiting.add(before);
      waitsOn.set(group, waiting);
      const holding = holdsUp.get(before) ?? new Set<number>();
      holding.add(group);
      holdsUp.set(before, holding);
    }
  });
  const ready = [...members.keys()]
    .filter((group) => (waitsOn.get(group)?.size ?? 0) === 0)
    .sort((one, other) => one - other);
  const entries: PlanEntry[] = [];
  while (ready.length > 0) {
    const group = ready.shift();
    if (group === undefined) break;
    const tasks = (members.get(group) ?? []).flatMap((index) => {
      const node = nodes[index];
      return node === undefined ? [] : [node];
    });
    const alone = tasks.length === 1 && (needs[group] ?? []).every((one) => one !== group);
    const first = tasks[0];
    if (alone && first !== undefined) entries.push({ kind: "step", task: first });
    else entries.push({ kind: "cycle", tasks });
    for (const next of holdsUp.get(group) ?? []) {
      const waiting = waitsOn.get(next);
      if (waiting === undefined) continue;
      waiting.delete(group);
      if (waiting.size === 0) readyFor(ready, next);
    }
  }
  return entries;
}
