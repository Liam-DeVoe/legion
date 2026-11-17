import { expect, test } from "bun:test";

import { heldBy, type NodeChip, type ParentLine, type TreeNode } from "../issue-tree";
import type { NodeType } from "../page-words";
import { type NodeStates, overtakenRefs, stampStates, statesByReference } from "./node-states";

/**
 * A node's life and who may read it, put on a tree built by hand (dispatch://INFRA-4063 R1, R2,
 * R5, R7, `dispatch://INFRA-4063/artifact/plan-md#b-t3`): the states arrive by node id, and every
 * place that draws a node, and every count over the tree, reads them from the one tree.
 */

const KEY = "CORE-1";

function node(
  id: string,
  type: NodeType,
  children: readonly TreeNode[] = [],
  extra: Partial<TreeNode> = {}
): TreeNode {
  return {
    alsoUnder: [],
    children,
    chips: [],
    facts: [],
    gaps: [],
    held: heldBy(children),
    id,
    question: false,
    replaced: false,
    text: id,
    transcluded: false,
    type,
    waiting: false,
    ...extra,
  };
}

/** A chip naming `ref`, which the batched reference read answered. */
function chip(ref: string): NodeChip {
  return { answered: true, gone: false, name: ref, ref };
}

/** The node `id` under `root`, wherever it sits. */
function nodeOf(root: TreeNode, id: string): TreeNode | undefined {
  if (root.id === id) return root;
  for (const child of root.children) {
    const found = nodeOf(child, id);
    if (found !== undefined) return found;
  }
  return undefined;
}

/** `root` with `states` put on it, by node id, as the page puts the tree read's on it. */
function stamped(root: TreeNode, states: Readonly<Record<string, NodeStates>>): TreeNode {
  return stampStates(root, new Map(Object.entries(states)), heldBy);
}

// R5: the negative control's assertion. An overtaken node and every node under it, at any
// depth, draw no gap, though each drew one before; a live sibling keeps its own.
test("no node at or under an overtaken one draws a gap", () => {
  const root = node(KEY, "issue", [
    node(
      "spec#b-r1",
      "requirement",
      [
        node(
          "verification-md#b-c1",
          "check",
          [node("result-1", "result", [], { gaps: ["no_check"] })],
          { gaps: ["serves_not_requirement"] }
        ),
        node("plan-md#b-t1", "plan_task", [], { gaps: ["nobody_asked"] }),
      ],
      { gaps: ["no_check"] }
    ),
    node("spec#b-r2", "requirement", [], { gaps: ["no_check"] }),
  ]);
  const tree = stamped(root, { "spec#b-r1": { life: "overtaken" } });

  expect(nodeOf(root, "spec#b-r1")?.gaps).toEqual(["no_check"]);
  expect(nodeOf(root, "plan-md#b-t1")?.gaps).toEqual(["nobody_asked"]);
  expect(nodeOf(tree, "spec#b-r1")?.gaps).toEqual([]);
  expect(nodeOf(tree, "verification-md#b-c1")?.gaps).toEqual([]);
  expect(nodeOf(tree, "result-1")?.gaps).toEqual([]);
  expect(nodeOf(tree, "plan-md#b-t1")?.gaps).toEqual([]);
  for (const id of ["spec#b-r1", "verification-md#b-c1", "result-1", "plan-md#b-t1"]) {
    expect(nodeOf(tree, id)?.closed, id).toBe(true);
  }
  // Only the overtaken node is itself overtaken; what sits under it is closed by it.
  expect(nodeOf(tree, "spec#b-r1")?.life).toBe("overtaken");
  expect("life" in (nodeOf(tree, "plan-md#b-t1") ?? {})).toBe(false);
  expect(nodeOf(tree, "spec#b-r2")).toStrictEqual(nodeOf(root, "spec#b-r2"));
});

// R1: a node's marks are its entry's and no other. A settled node's entry holds none, and a
// live node visible to everyone has no entry at all: neither carries a mark or is closed, and
// each is the node it was before, key for key.
test("a live or settled node has no mark", () => {
  const root = node(KEY, "issue", [
    node("spec#b-r1", "requirement", [node("verification-md#b-c1", "check")], {
      gaps: ["no_check"],
    }),
    node("spec#b-r2", "requirement", [], { gaps: ["no_check"] }),
    node("spec#b-r3", "requirement"),
  ]);
  const tree = stamped(root, { "spec#b-r1": {}, "spec#b-r3": { life: "proposed" } });

  for (const id of ["spec#b-r1", "verification-md#b-c1", "spec#b-r2"]) {
    const found = nodeOf(tree, id);
    expect(
      ["closed", "life", "onlyYou"].filter((key) => key in (found ?? {})),
      id
    ).toEqual([]);
    expect(found, id).toStrictEqual(nodeOf(root, id));
  }
});

// R2: a proposed node draws no gap, the page's own included: a plan task that serves nothing
// draws no "No reason given", a check citing no statement none either, and a statement two
// sources disagree on none. A proposed node closes nothing under it, and a live one keeps its gap.
test("a proposed node draws no gap the page made", () => {
  const root = node(KEY, "issue", [
    node("plan-md#b-t1", "plan_task", [], { gaps: ["nobody_asked"] }),
    node("verification-md#b-c1", "check", [], { gaps: ["serves_not_requirement"] }),
    node("spec#b-r1", "requirement", [], { gaps: ["shaped_disagreement"] }),
    node("plan-md#b-t2", "plan_task", [], { gaps: ["nobody_asked"] }),
  ]);
  const proposed = ["plan-md#b-t1", "verification-md#b-c1", "spec#b-r1"];
  const tree = stamped(
    root,
    Object.fromEntries(proposed.map((id) => [id, { life: "proposed" } as const]))
  );

  for (const id of proposed) {
    expect(nodeOf(tree, id)?.life, id).toBe("proposed");
    expect(nodeOf(tree, id)?.gaps, id).toEqual([]);
    expect("closed" in (nodeOf(tree, id) ?? {}), id).toBe(false);
  }
  expect(nodeOf(tree, "plan-md#b-t2")?.gaps).toEqual(["nobody_asked"]);
});

// R2, R7: a chip names a node by its reference and a parent line by its id, and each carries
// that node's marks, in `chips` and in `alsoUnder` alike; one naming an unmarked node carries
// none. A parent line's node need not be in the tree at all: a view draws it as no row.
test("a chip and a parent line carry the marks of the node they name", () => {
  const marked = `dispatch://${KEY}/spec#b-r7`;
  const plain = `dispatch://${KEY}/spec#b-r8`;
  const line = (id: string): ParentLine => ({ id, line: `${id}.`, type: "ask" });
  const root = node(KEY, "issue", [
    node("spec#b-r7", "requirement", [], { reference: marked }),
    node("spec#b-r8", "requirement", [], { reference: plain }),
    node("decisions-md#b-d1", "decision", [], {
      alsoUnder: [chip(marked)],
      chips: [chip(marked), chip(plain)],
    }),
    node("verification-md#b-c1", "check", [], { parentLine: line("ask-1") }),
    node("verification-md#b-c2", "check", [], { parentLine: line("ask-2") }),
  ]);
  const tree = stamped(root, {
    "ask-1": { life: "proposed" },
    "spec#b-r7": { life: "proposed", onlyYou: true },
  });

  const decision = nodeOf(tree, "decisions-md#b-d1");
  expect(decision?.chips).toStrictEqual([
    { ...chip(marked), life: "proposed", onlyYou: true },
    chip(plain),
  ]);
  expect(decision?.alsoUnder).toStrictEqual([{ ...chip(marked), life: "proposed", onlyYou: true }]);
  expect(nodeOf(tree, "verification-md#b-c1")?.parentLine).toStrictEqual({
    ...line("ask-1"),
    life: "proposed",
  });
  expect(nodeOf(tree, "verification-md#b-c2")?.parentLine).toStrictEqual(line("ask-2"));
  expect([...statesByReference(tree)]).toStrictEqual([
    [marked, { life: "proposed", onlyYou: true }],
  ]);
});

test("a tree given no states is answered as it is", () => {
  const root = node(KEY, "issue", [node("spec#b-r1", "requirement", [], { gaps: ["no_check"] })]);
  expect(stampStates(root, new Map(), heldBy)).toBe(root);
});

// R5: a parent's overtaken children fold to one line, so its folded count counts that line as
// one row and nothing under it, while each overtaken node still counts what it holds itself.
test("a parent's overtaken children are one row of its held count", () => {
  const root = node(KEY, "issue", [
    node("spec#b-r1", "requirement", [node("verification-md#b-c1", "check")]),
    node("spec#b-r2", "requirement", [
      node("verification-md#b-c2", "check"),
      node("plan-md#b-t2", "plan_task"),
    ]),
    node("spec#b-r3", "requirement", [node("verification-md#b-c3", "check")]),
  ]);
  expect(root.held).toBe(7);
  const tree = stamped(root, {
    "spec#b-r2": { life: "overtaken" },
    "spec#b-r3": { life: "overtaken" },
  });

  expect(tree.held).toBe(3);
  expect(nodeOf(tree, "spec#b-r2")?.held).toBe(2);
  expect(stamped(root, { "spec#b-r3": { life: "overtaken" } }).held).toBe(6);
});

// R3: the header's count of what waits on the reader matches an open ask by its id and a
// decision by its reference, so the set it leaves out holds both for every node at or under an
// overtaken one, an ask that is a block by its own id beside its node's, and nothing else.
test("what waits leaves out each closed node's reference and each closed ask's id", () => {
  const decisions = `dispatch://${KEY}/artifact/decisions-md`;
  const root = node(KEY, "issue", [
    node(
      "decisions-md#b-d1",
      "decision",
      [
        node("ask-1", "ask", [], {
          question: true,
          reference: `dispatch://${KEY}/ask/ask-1`,
          source: { id: "ask-1", kind: "ask" },
        }),
        node("spec#b-q2", "ask", [], { question: true, source: { id: "ask-2", kind: "ask" } }),
      ],
      { reference: `${decisions}#b-d1`, waiting: true }
    ),
    node("decisions-md#b-d2", "decision", [], { reference: `${decisions}#b-d2`, waiting: true }),
    node("ask-3", "ask", [], { question: true, reference: `dispatch://${KEY}/ask/ask-3` }),
  ]);

  expect(
    [...overtakenRefs(stamped(root, { "decisions-md#b-d1": { life: "overtaken" } }))].sort()
  ).toEqual(
    [`${decisions}#b-d1`, `dispatch://${KEY}/ask/ask-1`, "ask-1", "ask-2", "spec#b-q2"].sort()
  );
  expect(overtakenRefs(root).size).toBe(0);
});
