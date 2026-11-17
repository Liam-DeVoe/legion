import { afterEach, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { MarginProvider } from "../../margin/margin-context";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { useKeymapScope } from "../../shell/keymap";
import { FocusedNodeProvider } from "../focused-node";
import { IssueTree } from "../IssueTree";
import { overtakenLineOf } from "../issue-address";
import { heldBy, type NodeChip, type ParentLine, type TreeNode } from "../issue-tree";
import { KEY_REPORTS } from "../page-structure";
import type { NodeType } from "../page-words";
import { NOTHING_OPENED, type OpenedBranches, type OvertakenRow, pathTo } from "../tree-fold";
import { NodeMarks } from "./NodeMarks";
import {
  linesIn,
  marksAt,
  type NodeStates,
  overtakenRefs,
  stampStates,
  statesByReference,
  withLine,
} from "./node-states";
import { OvertakenLine } from "./OvertakenLine";

/**
 * A node's life and who may read it, put on a tree built by hand (dispatch://INFRA-4063 R1, R2,
 * R5, R7, `dispatch://INFRA-4063/artifact/plan-md#b-t3`): the states arrive by node id, and every
 * place that draws a node, and every count over the tree, reads them from the one tree. The
 * lines a path opens, and the marks and the line as a row draws them, are read here too
 * (`dispatch://INFRA-4063/artifact/plan-md#b-t6`), and so are the tree's keys and its opening
 * on a line, over `IssueTree` drawn under the page's keymap (`renderTree`); the rows of a whole
 * page are the browser's.
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

// R3, R6: a node at or under an overtaken one waits on nobody, since a later node took its
// place: the page opens on none of them and draws none in the waiting tone. An open question
// among them is still a question, which `q` reaches; a live one keeps waiting.
test("no node at or under an overtaken one waits on the reader", () => {
  const root = node(KEY, "issue", [
    node("ask-1", "ask", [], { question: true, waiting: true }),
    node("spec#b-r1", "requirement", [node("ask-2", "ask", [], { question: true, waiting: true })]),
    node("ask-3", "ask", [], { question: true, waiting: true }),
  ]);
  const tree = stamped(root, {
    "ask-1": { life: "overtaken" },
    "spec#b-r1": { life: "overtaken" },
  });

  for (const id of ["ask-1", "ask-2"]) {
    expect(nodeOf(tree, id)?.waiting, id).toBe(false);
    expect(nodeOf(tree, id)?.question, id).toBe(true);
  }
  expect(nodeOf(tree, "ask-3")).toStrictEqual(nodeOf(root, "ask-3"));
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

// R2, R7: a chip names a node by its reference, whatever version it pins, and a parent line by
// its id, and each carries that node's marks, in `chips` and in `alsoUnder` alike; one naming an
// unmarked node carries none. A parent line's node need not be in the tree at all: a view draws
// it as no row.
test("a chip and a parent line carry the marks of the node they name", () => {
  const marked = `dispatch://${KEY}/spec#b-r7`;
  const pinned = `${marked}@v3`;
  const plain = `dispatch://${KEY}/spec#b-r8`;
  const line = (id: string): ParentLine => ({ id, line: `${id}.`, type: "ask" });
  const root = node(KEY, "issue", [
    node("spec#b-r7", "requirement", [], { reference: marked }),
    node("spec#b-r8", "requirement", [], { reference: plain }),
    node("decisions-md#b-d1", "decision", [], {
      alsoUnder: [chip(marked)],
      chips: [chip(marked), chip(pinned), chip(plain)],
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
    { ...chip(pinned), life: "proposed", onlyYou: true },
    chip(plain),
  ]);
  expect(decision?.alsoUnder).toStrictEqual([{ ...chip(marked), life: "proposed", onlyYou: true }]);
  expect(nodeOf(tree, "verification-md#b-c1")?.parentLine).toStrictEqual({
    ...line("ask-1"),
    life: "proposed",
  });
  expect(nodeOf(tree, "verification-md#b-c2")?.parentLine).toStrictEqual(line("ask-2"));
  const states = statesByReference(tree);
  expect([...states]).toStrictEqual([[marked, { life: "proposed", onlyYou: true }]]);
  expect(marksAt(states, pinned)).toStrictEqual({ life: "proposed", onlyYou: true });
  expect(marksAt(states, marked)).toStrictEqual({ life: "proposed", onlyYou: true });
  expect(marksAt(states, `${plain}@v2`)).toBeUndefined();
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

// R6: an address, a chip, a Contents entry, `q` or `w` that names an overtaken node, or a node
// under one, opens the line that holds it - each line, where an overtaken node stands under
// another - and a path through no overtaken node leaves the fold as it was.
test("a path opens the line of each overtaken node it runs through, and none for a path through none", () => {
  const deep = node("comment-2", "comment", [], { closed: true });
  const inner = node("verification-md#b-c1", "check", [deep], { closed: true, life: "overtaken" });
  const outer = node("spec#b-r8", "requirement", [inner], { closed: true, life: "overtaken" });
  const root = node(KEY, "issue", [
    node("spec", "document", [node("spec#b-r7", "requirement"), outer]),
  ]);
  const opened: OpenedBranches = { ...NOTHING_OPENED, overtaken: new Set(["elsewhere"]) };

  expect(overtakenLineOf(pathTo(root, "comment-2"))).toBe("spec");
  const both = withLine(opened, pathTo(root, "comment-2"));
  expect([...(both.overtaken ?? [])]).toEqual(["elsewhere", "spec", "spec#b-r8"]);
  expect(both.branches).toBe(opened.branches);
  expect(both.closed).toBe(opened.closed);
  expect(both.deeper).toBe(opened.deeper);
  expect([...(withLine(NOTHING_OPENED, pathTo(root, "spec#b-r8")).overtaken ?? [])]).toEqual([
    "spec",
  ]);
  // The set the reader's own lines are held in is a new one, never the one they had.
  expect(opened.overtaken).toEqual(new Set(["elsewhere"]));

  expect(overtakenLineOf(pathTo(root, "spec#b-r7"))).toBeUndefined();
  expect(withLine(opened, pathTo(root, "spec#b-r7"))).toBe(opened);
  expect(withLine(opened, undefined)).toBe(opened);
});

// R6: under the words filter each line of the view holds a match, so each is drawn open: the
// lines are the nodes that hold an overtaken child, at any depth, and no other.
test("each node that holds an overtaken child holds a line, in reading order", () => {
  const root = node(KEY, "issue", [
    node("spec", "document", [
      node("spec#b-r8", "requirement", [node("verification-md#b-c1", "check")], {
        life: "overtaken",
      }),
      node("spec#b-r9", "requirement", [
        node("verification-md#b-c2", "check", [], { life: "overtaken" }),
      ]),
    ]),
    node("plan-md", "document", [node("plan-md#b-t1", "plan_task", [], { life: "proposed" })]),
  ]);

  expect(linesIn(root)).toEqual(["spec", "spec#b-r9"]);
  expect(linesIn(node(KEY, "issue"))).toEqual([]);
});

// R2, R7, R9: a node's marks are its words and its shapes, its life first, in every place that
// draws it; a node that has none draws nothing at all, not even an empty box.
test("a proposed node is marked Proposed with its shape, and a node with no mark draws none", () => {
  const proposed = render(createElement(NodeMarks, { marks: { life: "proposed" } }));
  try {
    const marks = proposed.container.querySelectorAll('[data-node-mark="proposed"]');
    expect(marks).toHaveLength(1);
    expect(marks[0]?.textContent).toBe("Proposed");
    expect(marks[0]?.querySelector("svg")).toHaveAttribute("data-glyph", "mark-proposed");
    expect(proposed.container.querySelectorAll("[data-node-mark]")).toHaveLength(1);
  } finally {
    proposed.unmount();
  }

  const both = render(createElement(NodeMarks, { marks: { life: "overtaken", onlyYou: true } }));
  try {
    const marks = [...both.container.querySelectorAll<HTMLElement>("[data-node-mark]")];
    expect(marks.map((mark) => mark.dataset.nodeMark)).toEqual(["overtaken", "only_you"]);
    expect(marks.map((mark) => mark.textContent)).toEqual(["Overtaken", "Only you"]);
  } finally {
    both.unmount();
  }

  const none = render(createElement(NodeMarks, { compact: true, marks: {} }));
  try {
    expect(none.container.innerHTML).toBe("");
  } finally {
    none.unmount();
  }
});

// R5: a parent's overtaken children fold to one line after its other children, which says how
// many it holds in one text node, takes the focus with `aria-expanded`, and opens and closes in
// place. It is no line of a deeper branch and holds no Unfold, so nothing that opens every
// branch opens it.
test("a parent's overtaken children fold to one line that a press opens and closes", async () => {
  const of = node("spec", "document", [
    node("spec#b-r8", "requirement", [], { life: "overtaken" }),
    node("spec#b-r9", "requirement", [], { life: "overtaken" }),
  ]);
  const pressed: [string, boolean][] = [];
  const lineOf = (open: boolean) =>
    createElement(OvertakenLine, {
      onOvertaken: (id: string, opening: boolean) => pressed.push([id, opening]),
      row: { held: 2, level: 2, of, open, trail: [KEY] } satisfies OvertakenRow,
      tabStop: false,
    });
  const user = userEvent.setup();
  const view = render(lineOf(false));
  try {
    const line = view.getByRole("treeitem");
    expect(line).toHaveAttribute("aria-expanded", "false");
    expect(line).toHaveAttribute("aria-level", "2");
    expect(line).toHaveAttribute("data-overtaken-line", "");
    expect(line).toHaveAttribute("tabindex", "-1");
    const texts: string[] = [];
    const walker = document.createTreeWalker(line, NodeFilter.SHOW_TEXT);
    for (let text = walker.nextNode(); text !== null; text = walker.nextNode()) {
      texts.push(text.textContent ?? "");
    }
    expect(texts).toEqual(["2 overtaken"]);
    expect(view.container.querySelector("[data-branch-more]")).toBeNull();
    expect(view.queryByRole("button")).toBeNull();

    await user.click(line);
    expect(pressed).toEqual([["spec", true]]);
    view.rerender(lineOf(true));
    expect(view.getByRole("treeitem")).toHaveAttribute("aria-expanded", "true");
    await user.click(view.getByRole("treeitem"));
    expect(pressed).toEqual([
      ["spec", true],
      ["spec", false],
    ]);
  } finally {
    view.unmount();
  }
});

/** The page's own `issue` scope, which every one of the tree's keys is registered in. */
function IssueScope(): ReactNode {
  useKeymapScope("issue");
  return null;
}

const originalMatchMedia = window.matchMedia;
afterEach(() => {
  window.matchMedia = originalMatchMedia;
});

/** happy-dom answers every media query against its own 1024 px window: this answers them for
 *  one 1440 px wide, where the tree draws its rows whole rather than one node at a time
 *  (`IssueTree.tsx`'s `ZOOMED_TREE_QUERY`), as `IssueTree.test.tsx`'s `setViewport` does. */
function readWide(): void {
  const width = 1440;
  window.matchMedia = ((query: string) => {
    const max = /max-width:\s*([\d.]+)px/.exec(query)?.[1];
    const min = /min-width:\s*([\d.]+)rem/.exec(query)?.[1];
    const matched =
      max !== undefined ? width <= Number(max) : min !== undefined && width >= Number(min) * 16;
    return {
      addEventListener: () => {},
      addListener: () => {},
      dispatchEvent: () => true,
      matches: query.startsWith("not all and ") ? !matched : matched,
      media: query,
      onchange: null,
      removeEventListener: () => {},
      removeListener: () => {},
    } as MediaQueryList;
  }) as typeof window.matchMedia;
}

/** `root` drawn by the page's tree, under the page's keymap and the providers it reads, as
 *  `IssueTree.test.tsx`'s `renderTree` draws it: no address, no filter, no document. */
function renderTree(root: TreeNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  return render(
    createElement(
      MemoryRouter,
      null,
      createElement(
        QueryClientProvider,
        { client },
        createElement(
          KeymapProvider,
          null,
          createElement(
            MarginProvider,
            null,
            createElement(
              FocusedNodeProvider,
              null,
              createElement(IssueScope),
              createElement(IssueTree, {
                artifacts: new Map(),
                context: new Set<string>(),
                emptyLine: "Nothing here.",
                filtering: false,
                focusReveal: false,
                holdsEscape: false,
                isClosed: false,
                onEditing: () => {},
                reveal: undefined,
                root,
                rootRow: true,
              })
            )
          )
        )
      )
    )
  );
}

/** The row the tree draws for the node `id`, or null where it draws none. */
function rowOf(container: HTMLElement, id: string): HTMLElement | null {
  return container.querySelector<HTMLElement>(`[data-node-row][data-node-id="${id}"]`);
}

/** The id of the node whose row holds the focus, or undefined where none does. */
function focusedRow(container: HTMLElement): string | undefined {
  const active = document.activeElement;
  return [...container.querySelectorAll<HTMLElement>("[data-node-row]")].find((row) =>
    row.contains(active)
  )?.dataset.nodeId;
}

/** The one line of overtaken nodes the tree draws. */
function theLine(container: HTMLElement): HTMLElement {
  const line = container.querySelector<HTMLElement>("[data-overtaken-line]");
  if (line === null) throw new Error("the tree draws no line of overtaken nodes");
  return line;
}

/** An issue whose first row is a comment, and whose second comment a later one took the place
 *  of, so the tree draws the comment and then the line its overtaken sibling folds to. */
function commentAndLine(): TreeNode {
  return stamped(node(KEY, "issue", [node("comment-1", "comment"), node("comment-2", "comment")]), {
    "comment-2": { life: "overtaken" },
  });
}

// R5, R9: the line takes the focus as a row does, so it is the tree's one tab stop once the
// reader is on it: Shift+Tab comes back to it, and the row they left takes no Tab of its own.
test("the overtaken line is the tree's tab stop once the reader is on it", async () => {
  readWide();
  const user = userEvent.setup();
  const view = renderTree(commentAndLine());
  try {
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    expect(rowOf(view.container, "comment-1")).toHaveAttribute("tabindex", "0");
    const line = theLine(view.container);
    expect(line).toHaveAttribute("tabindex", "-1");

    await user.keyboard("j");
    await waitFor(() => expect(document.activeElement).toBe(line));
    await waitFor(() => expect(line).toHaveAttribute("tabindex", "0"));
    expect(rowOf(view.container, "comment-1")).toHaveAttribute("tabindex", "-1");
  } finally {
    view.unmount();
  }
});

// R5, R9: a key pressed on the line that answers in words - `w` there has no decision behind
// it, since the line stands for no node - is drawn at the line, and the next move lets it go.
test("a key's report on the overtaken line is drawn at the line", async () => {
  readWide();
  const user = userEvent.setup();
  const view = renderTree(commentAndLine());
  try {
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    const line = theLine(view.container);
    await user.keyboard("j");
    await waitFor(() => expect(document.activeElement).toBe(line));

    await user.keyboard("w");
    await waitFor(() =>
      expect(line.querySelector("[data-row-report]")?.textContent).toBe(KEY_REPORTS.no_decision)
    );
    expect(view.container.querySelectorAll("[data-row-report]")).toHaveLength(1);

    await user.keyboard("k");
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    expect(view.container.querySelector("[data-row-report]")).toBeNull();
  } finally {
    view.unmount();
  }
});

// R5: the cycle key's last state draws every branch, and a line the reader opens there opens:
// the lines are no branch, so that state keeps the ones the reader opened.
test("in the cycle's last state, Enter on the overtaken line opens it", async () => {
  readWide();
  const user = userEvent.setup();
  const view = renderTree(commentAndLine());
  try {
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    await user.keyboard("z");
    await user.keyboard("z");
    const line = theLine(view.container);
    await user.keyboard("j");
    await waitFor(() => expect(document.activeElement).toBe(line));
    expect(line).toHaveAttribute("aria-expanded", "false");
    expect(rowOf(view.container, "comment-2")).toBeNull();

    await user.keyboard("{Enter}");
    await waitFor(() => expect(line).toHaveAttribute("aria-expanded", "true"));
    expect(rowOf(view.container, "comment-2")).not.toBeNull();
  } finally {
    view.unmount();
  }
});

// R3, R5: an overtaken question that waited on the reader waits no more, so the page opens on
// the outline's first row, with the line that holds the question closed.
test("the page does not open on an overtaken question", async () => {
  readWide();
  const view = renderTree(
    stamped(
      node(KEY, "issue", [
        node("comment-1", "comment"),
        node("ask-1", "ask", [], { question: true, state: "open", waiting: true }),
      ]),
      { "ask-1": { life: "overtaken" } }
    )
  );
  try {
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    expect(theLine(view.container)).toHaveAttribute("aria-expanded", "false");
    expect(rowOf(view.container, "ask-1")).toBeNull();
    expect(focusedRow(view.container)).not.toBe("ask-1");
  } finally {
    view.unmount();
  }
});

// R6: `q` and `w` take the reader to an overtaken question and an overtaken decision, which are
// no rows while their line is closed: each key opens the line and focuses the node's row.
test("`q` and `w` reach an overtaken question and decision", async () => {
  readWide();
  const user = userEvent.setup();
  const requirement = `dispatch://${KEY}/spec#b-r1`;
  const view = renderTree(
    stamped(
      node(KEY, "issue", [
        node("comment-1", "comment"),
        node("spec#b-r1", "requirement", [], { reference: requirement }),
        node("ask-1", "ask", [], { question: true, state: "open" }),
        node("decisions-md#b-d1", "decision", [], { chips: [chip(requirement)] }),
      ]),
      { "ask-1": { life: "overtaken" }, "decisions-md#b-d1": { life: "overtaken" } }
    )
  );
  try {
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    const line = theLine(view.container);
    expect(line).toHaveAttribute("aria-expanded", "false");
    expect(rowOf(view.container, "ask-1")).toBeNull();

    await user.keyboard("q");
    await waitFor(() => expect(focusedRow(view.container)).toBe("ask-1"));
    expect(line).toHaveAttribute("aria-expanded", "true");

    // The line closed again, from the line itself, so `w` finds its decision no row.
    await user.keyboard("k");
    await waitFor(() => expect(document.activeElement).toBe(line));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(line).toHaveAttribute("aria-expanded", "false"));
    expect(rowOf(view.container, "decisions-md#b-d1")).toBeNull();
    await user.keyboard("k");
    await waitFor(() => expect(focusedRow(view.container)).toBe("spec#b-r1"));

    await user.keyboard("w");
    await waitFor(() => expect(focusedRow(view.container)).toBe("decisions-md#b-d1"));
    expect(line).toHaveAttribute("aria-expanded", "true");
  } finally {
    view.unmount();
  }
});
