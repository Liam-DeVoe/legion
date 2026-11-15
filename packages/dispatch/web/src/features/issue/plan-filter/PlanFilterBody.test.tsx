import { afterEach, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { MarginProvider } from "../../margin/margin-context";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { useKeymapScope } from "../../shell/keymap";
import type { NodeChip, ParentLine, TreeNode } from "../issue-tree";
import { EMPTY_STATE_LINES, type NodeType } from "../page-words";
import { blockNodeId } from "../tree-drafts";
import { PLAN_WORDS, PlanFilterBody } from "./PlanFilterBody";

/**
 * What the Plan filter's body draws (dispatch://INFRA-1196 R26): the tasks as ordered steps,
 * each showing what it needs and the statements it serves, and a cycle as one line that lists
 * the tasks in it.
 *
 * The nodes are built as `presets.ts`'s `filterView` leaves them for the Plan filter: each task
 * a row of its own, the statement it serves its parent line, and any further statement a chip
 * naming what it is also under.
 */

const KEY = "CORE-1";
const PLAN = "plan-md";
const SPEC = "spec-md";

/** happy-dom answers every media query against its own window; the tree reads one to tell the
 *  width it is read at (`IssueTree.tsx`'s `ZOOMED_TREE_QUERY`), so a test sets it. */
function setViewport(width: number): void {
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

const originalMatchMedia = window.matchMedia;
afterEach(() => {
  window.matchMedia = originalMatchMedia;
});

function taskRef(blockId: string): string {
  return `dispatch://${KEY}/artifact/${PLAN}#b-${blockId}`;
}

function chip(ref: string, name: string): NodeChip {
  return { answered: true, gone: false, name, ref };
}

/** One statement, as the line a task's row draws for the parent the filter draws as no row. */
function serves(blockId: string, line: string): ParentLine {
  return { id: blockNodeId(SPEC, blockId), line, type: "requirement" };
}

/** One plan task, by its block id: what it needs, by their block ids, and the statement it
 *  serves, where the plan gives it one. */
function task(
  blockId: string,
  {
    needs = [],
    parentLine,
    type = "plan_task",
  }: { needs?: readonly string[]; parentLine?: ParentLine; type?: NodeType } = {}
): TreeNode {
  return {
    alsoUnder: [],
    children: [],
    chips: needs.map((needed) => chip(taskRef(needed), `Task ${needed}`)),
    facts: [],
    gaps: [],
    held: 0,
    id: blockNodeId(PLAN, blockId),
    ...(parentLine === undefined ? {} : { parentLine }),
    question: false,
    reference: taskRef(blockId),
    replaced: false,
    text: `Task ${blockId}`,
    transcluded: false,
    type,
    waiting: false,
  };
}

function issue(children: readonly TreeNode[]): TreeNode {
  return {
    alsoUnder: [],
    children,
    chips: [],
    facts: [],
    gaps: [],
    held: children.length,
    id: KEY,
    question: false,
    replaced: false,
    text: `${KEY} The issue page draws one ordered body`,
    transcluded: false,
    type: "issue",
    waiting: false,
  };
}

/** The page's own `issue` scope, which every one of the tree's keys is registered in. */
function IssueScope(): ReactNode {
  useKeymapScope("issue");
  return null;
}

function renderBody(root: TreeNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <IssueScope />
            <PlanFilterBody
              artifacts={new Map()}
              context={new Set()}
              emptyLine={EMPTY_STATE_LINES.plan}
              filtering
              focusReveal={false}
              holdsEscape={false}
              isClosed={false}
              onEditing={() => {}}
              onReport={() => {}}
              reveal={undefined}
              root={root}
              rootRow={false}
            />
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

/** The ids of the rows the body drew, in the order it drew them. */
function rowIds(container: HTMLElement): (string | null)[] {
  return [...container.querySelectorAll("[data-node-row]")].map((row) =>
    row.getAttribute("data-node-id")
  );
}

test("the steps are drawn after the tasks they need, each with what it needs and what it serves", () => {
  setViewport(1440);
  // Written in the plan as t3, t1, t2: t3 needs t1 and t2 needs t3, so the body draws t1, t3,
  // t2, and each row carries the chip naming what it needs and the line naming what it serves.
  const view = renderBody(
    issue([
      task("t3", { needs: ["t1"], parentLine: serves("r2", "The outline draws one body") }),
      task("t1", { parentLine: serves("r1", "The page draws the conversation") }),
      task("t2", { needs: ["t3"], parentLine: serves("r1", "The page draws the conversation") }),
    ])
  );
  try {
    expect(rowIds(view.container)).toEqual([
      blockNodeId(PLAN, "t1"),
      blockNodeId(PLAN, "t3"),
      blockNodeId(PLAN, "t2"),
    ]);

    const step = view.container.querySelector<HTMLElement>(
      `[data-node-row][data-node-id="${blockNodeId(PLAN, "t3")}"]`
    );
    expect(step?.textContent).toContain("Task t1");
    const line = step?.querySelector("[data-node-parent-line]");
    expect(line?.getAttribute("data-parent-id")).toBe(blockNodeId(SPEC, "r2"));
    expect(line?.textContent).toContain("The outline draws one body");
  } finally {
    view.unmount();
  }
});

test("tasks that need each other are one line that lists them, and no step is drawn for them", () => {
  setViewport(1440);
  const view = renderBody(
    issue([
      task("t1", { parentLine: serves("r1", "The page draws the conversation") }),
      task("t4", { needs: ["t5"] }),
      task("t5", { needs: ["t4"] }),
      task("t6", { needs: ["t1"] }),
    ])
  );
  try {
    expect(rowIds(view.container)).toEqual([blockNodeId(PLAN, "t1"), blockNodeId(PLAN, "t6")]);

    const lines = [...view.container.querySelectorAll("[data-plan-cycle]")];
    expect(lines).toHaveLength(1);
    expect(lines[0]?.textContent).toContain(PLAN_WORDS.cycle);
    expect(lines[0]?.textContent).toContain("Task t4");
    expect(lines[0]?.textContent).toContain("Task t5");

    // Each task in the line is the link a reader follows to that task.
    const links = [...(lines[0]?.querySelectorAll("a") ?? [])];
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      `/issues/${KEY}/artifacts/${PLAN}#b-t4`,
      `/issues/${KEY}/artifacts/${PLAN}#b-t5`,
    ]);
  } finally {
    view.unmount();
  }
});

test("a plan whose every task is in a cycle says so, and still draws the one tree", () => {
  setViewport(1440);
  const view = renderBody(issue([task("t1", { needs: ["t2"] }), task("t2", { needs: ["t1"] })]));
  try {
    expect(rowIds(view.container)).toEqual([]);
    expect(view.container.querySelectorAll("[data-plan-cycle]")).toHaveLength(1);
    expect(view.container.querySelectorAll('[role="tree"]')).toHaveLength(1);
    expect(view.container.textContent).toContain(PLAN_WORDS.cycles_only);
    expect(view.container.textContent).not.toContain(EMPTY_STATE_LINES.plan);
  } finally {
    view.unmount();
  }
});

test("a plan with no tasks says so, in the one line the page has for it", () => {
  setViewport(1440);
  const view = renderBody(issue([]));
  try {
    expect(view.container.querySelectorAll("[data-plan-cycle]")).toHaveLength(0);
    expect(view.container.textContent).toContain(EMPTY_STATE_LINES.plan);
  } finally {
    view.unmount();
  }
});

test("a task whose needs names a task the plan does not hold keeps the plan's own order", () => {
  setViewport(1440);
  const view = renderBody(
    issue([
      task("t2", { needs: ["t9"], parentLine: serves("r1", "The page draws the conversation") }),
      task("t1"),
    ])
  );
  try {
    expect(rowIds(view.container)).toEqual([blockNodeId(PLAN, "t2"), blockNodeId(PLAN, "t1")]);
    expect(view.container.querySelectorAll("[data-plan-cycle]")).toHaveLength(0);
  } finally {
    view.unmount();
  }
});
