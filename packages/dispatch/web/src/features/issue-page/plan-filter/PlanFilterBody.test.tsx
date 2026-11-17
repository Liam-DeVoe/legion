import { afterEach, beforeAll, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type RenderResult, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";

import type { Artifact } from "../../../api/types";
import { MarginProvider } from "../../margin/margin-context";
import { warmMarkdownRenderer } from "../../refs/markdown-engine";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { useKeymapScope } from "../../shell/keymap";
import { FocusedNodeProvider, useFocusedNode } from "../focused-node";
import {
  firstLineOf,
  type NodeChip,
  nodeWords,
  type ParentLine,
  type TreeNode,
} from "../issue-tree";
import {
  BLOCK_ATTRIBUTE_LABELS,
  EMPTY_STATE_LINES,
  NODE_TYPE_NAMES,
  type NodeType,
  PLAN_WORDS,
} from "../page-words";
import { blockNodeId } from "../tree-drafts";
import { PlanFilterBody } from "./PlanFilterBody";

/**
 * What the Plan filter's body draws (dispatch://INFRA-1196 R26, ruling 5 of build-rulings-md):
 * it opens on the chart of time - one bar per step, after every step it needs, each row saying
 * what it needs, that it holds no size and what it serves - and one control switches to the
 * ordered steps, which are the outline's own rows; a cycle is one line that lists its tasks.
 *
 * The nodes are built as `presets.ts`'s `filterView` leaves them for the Plan filter: each task
 * a row of its own, the statement it serves its parent line, and any further statement a chip
 * naming what it is also under; and each `needs` chip is named as part F's view names it, from
 * the task it points at (`issue`), since the view draws F's names and names no edge itself
 * (decision 66 of dispatch://INFRA-1196, dispatch://INFRA-3938 R3 and R4).
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

// The steps are G's rows, whose chips draw nothing until the markdown engine has answered
// (`NodeRow.tsx`'s `Chip`): loaded once before the tests, as the row's own tests load it, so a
// reading of a chip under Steps reads the chip and not the engine's first load.
beforeAll(async () => {
  await warmMarkdownRenderer();
});

function taskRef(blockId: string): string {
  return `dispatch://${KEY}/artifact/${PLAN}#b-${blockId}`;
}

/** The chip the tree draws for one `needs` pointer of a task's block (`tree-drafts.ts`'s
 *  `placeBlocks`), under the attribute's own word; `issue` names it from the task it points at,
 *  once a test has built every task. */
function need(blockId: string): NodeChip {
  return {
    answered: true,
    gone: false,
    label: BLOCK_ATTRIBUTE_LABELS.needs,
    name: "",
    ref: taskRef(blockId),
  };
}

/** `chip` named as part F's view names a `needs` chip (decision 66 of dispatch://INFRA-1196):
 *  by the needed task's first line, by the page's word for its kind where that task holds no
 *  words ("plan task", dispatch://INFRA-3938 R3), and as gone where the plan holds no such task,
 *  which is how the reference read answers for a block a write removed (`tree-drafts.ts`'s
 *  `chipOf`). */
function namedBy(chip: NodeChip, needed: TreeNode | undefined): NodeChip {
  if (needed === undefined) return { ...chip, gone: true, name: "" };
  const line = firstLineOf(nodeWords(needed)).line;
  return { ...chip, name: line === "" ? NODE_TYPE_NAMES[needed.type] : line };
}

/** One statement, as the line a task's row draws for the parent the filter draws as no row. */
function serves(blockId: string, line: string): ParentLine {
  return {
    id: blockNodeId(SPEC, blockId),
    line,
    reference: `dispatch://${KEY}/spec#b-${blockId}`,
    type: "requirement",
  };
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
    chips: needs.map(need),
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

/** The issue's root over a test's tasks, each `needs` chip named from the task it points at
 *  among them (`namedBy`), as part F's view hands the tree over. */
function issue(tasks: readonly TreeNode[]): TreeNode {
  const byReference = new Map(tasks.map((one) => [one.reference, one]));
  const children = tasks.map((one) => ({
    ...one,
    chips: one.chips.map((chip) =>
      chip.label === BLOCK_ATTRIBUTE_LABELS.needs ? namedBy(chip, byReference.get(chip.ref)) : chip
    ),
  }));
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

/** The page's own `issue` scope, which every key of the body is registered in. */
function IssueScope(): ReactNode {
  useKeymapScope("issue");
  return null;
}

/** What the page would hold of the reader's place: the address, and the node Details draws. */
function Place(): ReactNode {
  const { hash, pathname } = useLocation();
  const focused = useFocusedNode();
  return <output data-focused={focused?.id ?? ""} data-path={`${pathname}${hash}`} data-place="" />;
}

/** The plan document, as the issue's read names it. */
const PLAN_ARTIFACT = "artifact-plan";

/** Draws the body over `root`, the view's tasks. `plan` is the whole plan's tasks, which the
 *  order and the chart's places are worked out from (`slots/view-plan.tsx`, dispatch://INFRA-3938
 *  R4): the view's own tasks unless a case draws the view a words filter leaves. `reveal` is the
 *  chain down to the node the address names. `written` is the block ids of the plan document in
 *  the order they are written, as the page's own blocks read holds them (`useIssueTree.ts`);
 *  where a case gives none the page holds no document, as on a tree drawn before that read
 *  answered. */
function renderBody(
  root: TreeNode,
  {
    plan = root.children,
    reveal,
    written,
  }: {
    plan?: readonly TreeNode[];
    reveal?: readonly TreeNode[];
    written?: readonly string[];
  } = {}
): RenderResult {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  const artifacts = new Map<string, Artifact>();
  if (written !== undefined) {
    artifacts.set(PLAN, { id: PLAN_ARTIFACT, slug: PLAN } as Artifact);
    client.setQueryData(
      ["artifact", PLAN_ARTIFACT, "blocks"],
      written.map((id) => ({ id }))
    );
  }
  return render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <FocusedNodeProvider>
              <IssueScope />
              <Place />
              <PlanFilterBody
                artifacts={artifacts}
                context={new Set()}
                emptyLine={EMPTY_STATE_LINES.plan}
                filtering
                focusReveal
                holdsEscape={false}
                isClosed={false}
                onEditing={() => {}}
                plan={plan}
                reveal={reveal}
                root={root}
                rootRow={false}
              />
            </FocusedNodeProvider>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

const D = (blockId: string): string => blockNodeId(PLAN, blockId);

/** The ids of the rows the body drew, in the order it drew them: in the chart a cycle's row too,
 *  by the task it stands for. */
function rowIds(container: HTMLElement): (string | null)[] {
  return [...container.querySelectorAll("[data-node-row]")].map((row) =>
    row.getAttribute("data-node-id")
  );
}

function rowOf(view: RenderResult, blockId: string): HTMLElement {
  const row = view.container.querySelector<HTMLElement>(
    `[data-node-row][data-node-id="${D(blockId)}"]`
  );
  if (row === null) throw new Error(`the body draws no row for ${blockId}`);
  return row;
}

/** Which row holds the focus, by its node's id: for a cycle's row, the task it stands for. */
function focusedRow(): string | null {
  return document.activeElement?.getAttribute("data-node-id") ?? null;
}

async function showSteps(view: RenderResult): Promise<void> {
  await userEvent.click(view.getByRole("button", { name: PLAN_WORDS.steps }));
}

// Ruling 5: "The Plan filter opens on the chart, and R26's control switches to the ordered
// steps." R26: "one bar per task, starting after every task it needs has ended ... each bar
// labelled with the task's first line ... a task with no size is a short bar marked No size".
test("the plan opens on the chart: one bar per step, each after every step it needs", () => {
  setViewport(1440);
  // Written in the plan as t3, t1, t2: t3 needs t1 and t2 needs t3.
  const whole = issue([
    task("t3", { needs: ["t1"], parentLine: serves("r2", "The outline draws one body") }),
    task("t1", { parentLine: serves("r1", "The page draws the conversation") }),
    task("t2", { needs: ["t3"], parentLine: serves("r1", "The page draws the conversation") }),
  ]);
  const view = renderBody(whole);
  let start: string | null = null;
  try {
    expect(view.container.querySelectorAll("[data-plan-chart]")).toHaveLength(1);
    expect(view.getByRole("button", { name: PLAN_WORDS.chart }).getAttribute("aria-pressed")).toBe(
      "true"
    );
    expect(view.getByRole("button", { name: PLAN_WORDS.steps }).getAttribute("aria-pressed")).toBe(
      "false"
    );
    expect(rowIds(view.container)).toEqual([D("t1"), D("t3"), D("t2")]);
    // One bar per step, and each starts one unit after the step it needs: today's model holds
    // no size, so a bar is one unit long and its row says so.
    expect(
      ["t1", "t3", "t2"].map((id) => [
        rowOf(view, id).getAttribute("data-plan-start"),
        rowOf(view, id).querySelectorAll("[data-plan-bar]").length,
        rowOf(view, id).querySelector("[data-plan-no-size]")?.textContent,
        rowOf(view, id).querySelector("[data-plan-bar]")?.getAttribute("style"),
      ])
    ).toEqual([
      ["0", 1, PLAN_WORDS.no_size, expect.stringContaining("left: 0%")],
      ["1", 1, PLAN_WORDS.no_size, expect.stringContaining("left: 33.3")],
      ["2", 1, PLAN_WORDS.no_size, expect.stringContaining("left: 66.6")],
    ]);
    // The row is named by what it draws, which begins with the task's first line, and says
    // what the step needs and the statement it serves, as a link to that statement.
    const step = rowOf(view, "t3");
    expect(step.querySelector("[data-node-words]")?.textContent).toBe("Task t3");
    expect(
      [...step.querySelectorAll("[data-plan-need]")].map((one) => [
        one.getAttribute("data-plan-need"),
        one.textContent,
      ])
    ).toEqual([[D("t1"), "Task t1"]]);
    expect(
      [...step.querySelectorAll("[data-plan-serve]")].map((one) => [
        one.textContent,
        one.getAttribute("href"),
      ])
    ).toEqual([["The outline draws one body", `/issues/${KEY}/spec#b-r2`]]);
    expect(rowOf(view, "t1").querySelectorAll("[data-plan-need]")).toHaveLength(0);
    // The chart is the one tree of the page, and one row of it is in the tab order (R9).
    expect(view.container.querySelectorAll('[role="tree"]')).toHaveLength(1);
    expect(
      [...view.container.querySelectorAll('[role="treeitem"][tabindex="0"]')].map((row) =>
        row.getAttribute("data-node-id")
      )
    ).toEqual([D("t1")]);
    // A curve for each need between two bars.
    expect(view.container.querySelectorAll("[data-plan-link]")).toHaveLength(2);
    start = rowOf(view, "t2").getAttribute("data-plan-start");
  } finally {
    view.unmount();
  }
  // The words filter leaves the view the last step alone (dispatch://INFRA-3938 R4, decision 3
  // of its plan): the chart draws that step alone, where it stands on the whole plan, and names
  // after "needs" the step it needs, which it does not draw; no curve joins it to a bar that is
  // not drawn.
  const filtered = renderBody(
    { ...whole, children: whole.children.filter((one) => one.id === D("t2")), held: 1 },
    { plan: whole.children }
  );
  try {
    expect(rowIds(filtered.container)).toEqual([D("t2")]);
    const last = rowOf(filtered, "t2");
    expect(last.getAttribute("data-plan-start")).toBe(start);
    expect(last.querySelector("[data-plan-bar]")?.getAttribute("style")).toContain("left: 66.6");
    expect(
      [...last.querySelectorAll("[data-plan-need]")].map((one) => [
        one.getAttribute("data-plan-need"),
        one.textContent,
      ])
    ).toEqual([[D("t3"), "Task t3"]]);
    expect(filtered.container.querySelectorAll("[data-plan-link]")).toHaveLength(0);
  } finally {
    filtered.unmount();
  }
  // A need whose chip part F's names read has not answered yet (`answered: false`, no name; the
  // reviewer's case of decision 103 of dispatch://INFRA-1196): the row names no need and draws no
  // "needs" with nothing after it, and the curves still join the bars, whatever the names.
  const unanswered = renderBody({
    ...whole,
    children: whole.children.map((one) => ({
      ...one,
      chips: one.chips.map((chip) => ({ ...chip, answered: false, name: "" })),
    })),
  });
  try {
    for (const blockId of ["t3", "t2"]) {
      const row = rowOf(unanswered, blockId);
      expect(row.querySelectorAll("[data-plan-need]")).toHaveLength(0);
      expect(row.querySelector("[data-plan-facts]")?.textContent).not.toContain(PLAN_WORDS.needs);
    }
    expect(unanswered.container.querySelectorAll("[data-plan-link]")).toHaveLength(2);
  } finally {
    unanswered.unmount();
  }
  // A first line `firstLineOf` cuts inside its sentence is drawn with part F's ellipsis, on the
  // bar's row and in a cycle's line, as G's row draws the same task under "Steps" (`drawnLine`,
  // ruling 1 of dispatch://INFRA-1196; dispatch://INFRA-3938 R4 and R6).
  const long = (blockId: string): string =>
    `Task ${blockId} ${"carries its one sentence on ".repeat(5)}to its end`;
  const cut = (blockId: string): string => {
    const first = firstLineOf(long(blockId));
    expect(first.cut).toBe(true);
    return `${first.line}…`;
  };
  const cutView = renderBody(
    issue([
      { ...task("t1"), text: long("t1") },
      { ...task("t2", { needs: ["t3"] }), text: long("t2") },
      task("t3", { needs: ["t2"] }),
    ])
  );
  try {
    expect(rowOf(cutView, "t1").querySelector("[data-node-words]")?.textContent).toBe(cut("t1"));
    expect(
      [...cutView.container.querySelectorAll("[data-plan-cycle] a")].map((one) => one.textContent)
    ).toEqual([cut("t2"), "Task t3"]);
  } finally {
    cutView.unmount();
  }
});

// R26: the chart's bars are "reached in order with the keyboard". The keys are the outline's.
test("the keys reach the bars in order, and the focused step's facts go to Details", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderBody(
    issue([
      task("t1", { parentLine: serves("r1", "The page draws the conversation") }),
      task("t2", { needs: ["t1"] }),
      task("t3", { needs: ["t2"] }),
    ])
  );
  try {
    const place = view.container.querySelector("[data-place]");
    act(() => rowOf(view, "t1").focus());
    expect(place?.getAttribute("data-focused")).toBe(D("t1"));
    await user.keyboard("{ArrowDown}");
    expect(focusedRow()).toBe(D("t2"));
    expect(place?.getAttribute("data-focused")).toBe(D("t2"));
    await user.keyboard("j");
    expect(focusedRow()).toBe(D("t3"));
    await user.keyboard("j");
    expect(focusedRow()).toBe(D("t3"));
    await user.keyboard("k");
    expect(focusedRow()).toBe(D("t2"));
    await user.keyboard("{Home}");
    expect(focusedRow()).toBe(D("t1"));
    await user.keyboard("{End}");
    expect(focusedRow()).toBe(D("t3"));
    // The tab stop follows the reader, so Tab back into the chart lands where they stood.
    expect(
      [...view.container.querySelectorAll('[role="treeitem"][tabindex="0"]')].map((row) =>
        row.getAttribute("data-node-id")
      )
    ).toEqual([D("t3")]);
    // `f` reaches the statement a step serves, which is in no tab order of its own.
    await user.keyboard("{Home}f");
    expect(document.activeElement?.getAttribute("href")).toBe(`/issues/${KEY}/spec#b-r1`);
    // Escape gives the row back from its link, and leaves the chart from the row, as the
    // outline's Escape does (dispatch://INFRA-3938 R7).
    await user.keyboard("{Escape}");
    expect(focusedRow()).toBe(D("t1"));
    await user.keyboard("{Escape}");
    expect(document.activeElement).toBe(document.body);
    // Tab from the page itself enters the chart at its one tab stop, the row the reader last
    // stood on, as Tab enters the outline (`useTreeKeymap.ts`'s `tree-enter`): after Escape the
    // same Tab goes back to the row under either drawing (dispatch://INFRA-3938 R6 and R7).
    await user.keyboard("{Tab}");
    expect(focusedRow()).toBe(D("t1"));
    await user.keyboard("{End}{Escape}");
    expect(document.activeElement).toBe(document.body);
    await user.keyboard("{Tab}");
    expect(focusedRow()).toBe(D("t3"));
    // A focus inside a row takes that row, as a pointer going down on a statement's link does
    // while another row holds the focus: Details and the one tab stop go with it
    // (dispatch://INFRA-3938 R7).
    act(() => rowOf(view, "t2").focus());
    expect(place?.getAttribute("data-focused")).toBe(D("t2"));
    const link = rowOf(view, "t1").querySelector<HTMLElement>("a[href]");
    expect(link?.getAttribute("href")).toBe(`/issues/${KEY}/spec#b-r1`);
    act(() => link?.focus());
    expect(document.activeElement).toBe(link);
    expect(place?.getAttribute("data-focused")).toBe(D("t1"));
    expect(
      [...view.container.querySelectorAll('[role="treeitem"][tabindex="0"]')].map((row) =>
        row.getAttribute("data-node-id")
      )
    ).toEqual([D("t1")]);
    // Enter on a step goes to it in the conversation, where it is edited.
    act(() => rowOf(view, "t2").focus());
    await user.keyboard("{Enter}");
    expect(place?.getAttribute("data-path")).toBe(`/issues/${KEY}#b-t2`);
  } finally {
    view.unmount();
  }
});

// R19: an address that names a step lands on it, in the view the filter opens on.
test("an address naming a step puts the focus on its bar's row", () => {
  setViewport(1440);
  const named = task("t2", { needs: ["t1"] });
  const root = issue([task("t1"), named]);
  const view = renderBody(root, { reveal: [root, named] });
  try {
    expect(focusedRow()).toBe(D("t2"));
  } finally {
    view.unmount();
  }
});

test("the control switches to the ordered steps, which are the outline's own rows", async () => {
  setViewport(1440);
  // The address names the step t1.
  const named = task("t1", { parentLine: serves("r1", "The page draws the conversation") });
  const root = issue([
    task("t3", { needs: ["t1"], parentLine: serves("r2", "The outline draws one body") }),
    named,
    task("t2", { needs: ["t3"], parentLine: serves("r1", "The page draws the conversation") }),
  ]);
  const view = renderBody(root, { reveal: [root, named] });
  try {
    // The address's step takes the focus once for the whole view: a press on the switch is the
    // reader's, and leaves the focus on the button pressed, since neither drawing takes the
    // address's step again (dispatch://INFRA-3938 R6 and R7).
    expect(focusedRow()).toBe(D("t1"));
    await showSteps(view);
    expect(document.activeElement).toBe(view.getByRole("button", { name: PLAN_WORDS.steps }));
    expect(view.container.querySelectorAll("[data-plan-chart]")).toHaveLength(0);
    expect(view.getByRole("button", { name: PLAN_WORDS.steps }).getAttribute("aria-pressed")).toBe(
      "true"
    );
    expect(rowIds(view.container)).toEqual([D("t1"), D("t3"), D("t2")]);
    const step = rowOf(view, "t3");
    expect(step.textContent).toContain("Task t1");
    const line = step.querySelector("[data-node-parent-line]");
    expect(line?.getAttribute("data-parent-id")).toBe(blockNodeId(SPEC, "r2"));
    expect(line?.textContent).toContain("The outline draws one body");
    expect(view.container.querySelectorAll('[role="tree"]')).toHaveLength(1);
    // And back to the chart.
    await userEvent.click(view.getByRole("button", { name: PLAN_WORDS.chart }));
    expect(view.container.querySelectorAll("[data-plan-chart]")).toHaveLength(1);
    expect(document.activeElement).toBe(view.getByRole("button", { name: PLAN_WORDS.chart }));
  } finally {
    view.unmount();
  }
});

// R26: "A cycle is one line that lists the tasks in it." The reviewer's finding on the place:
// the cycle stands where the order puts it, after the step its tasks need and before the step
// that waits on one of them, and an address naming one of its tasks lands on a row.
test("tasks that need each other are one line at their place, and rows at that place as steps", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const second = task("t5", { needs: ["t4"] });
  const root = issue([
    task("t6", { needs: ["t5"] }),
    task("t4", { needs: ["t5", "t1"] }),
    second,
    task("t1", { parentLine: serves("r1", "The page draws the conversation") }),
  ]);
  // The address names the cycle's second task.
  const view = renderBody(root, { reveal: [root, second] });
  try {
    // The chart: t1, then the cycle's one line, then the step that waits on it, two units in.
    const drawn = [
      ...view.container.querySelectorAll(
        "[data-plan-chart] :is([data-plan-bar-row], [data-plan-cycle])"
      ),
    ].map((row) => row.getAttribute("data-node-id") ?? "cycle");
    expect(drawn).toEqual([D("t1"), "cycle", D("t6")]);
    expect(rowOf(view, "t6").getAttribute("data-plan-start")).toBe("2");
    expect(rowOf(view, "t6").querySelector("[data-plan-need]")?.textContent).toBe("Task t5");
    const lines = [...view.container.querySelectorAll("[data-plan-cycle]")];
    expect(lines).toHaveLength(1);
    expect(lines[0]?.textContent).toContain(PLAN_WORDS.cycle);
    // Each task in the line is the link a reader follows to that task.
    expect(
      [...(lines[0]?.querySelectorAll("a") ?? [])].map((link) => [
        link.textContent,
        link.getAttribute("href"),
      ])
    ).toEqual([
      ["Task t4", `/issues/${KEY}/artifacts/${PLAN}#b-t4`],
      ["Task t5", `/issues/${KEY}/artifacts/${PLAN}#b-t5`],
    ]);
    // The address puts the focus on the cycle's row, which stands for the task it names in
    // Details, and the arrows go from it to the steps on either side of it, as they pass over it
    // (decision 2 of dispatch://INFRA-3938's plan, R7).
    const cycle = view.container.querySelector<HTMLElement>("[data-plan-cycle-row]");
    expect(document.activeElement).toBe(cycle);
    expect(cycle?.getAttribute("data-plan-cycle-row")).toBe(`${D("t4")} ${D("t5")}`);
    expect(focusedRow()).toBe(D("t5"));
    expect(view.container.querySelector("[data-place]")?.getAttribute("data-focused")).toBe(
      D("t5")
    );
    await user.keyboard("j");
    expect(focusedRow()).toBe(D("t6"));
    act(() => cycle?.focus());
    await user.keyboard("k");
    expect(focusedRow()).toBe(D("t1"));
    // A focus inside the cycle's row, on one of its task links, takes the focus for the row from
    // the step that held it, as a focus inside a step's row does: the row is the one tab stop and
    // Details draws the task the row stands for, the one the address names, not the link's
    // (`CycleRow`, dispatch://INFRA-3938 R7).
    const place = view.container.querySelector("[data-place]");
    expect(place?.getAttribute("data-focused")).toBe(D("t1"));
    const link = cycle?.querySelector<HTMLElement>("a[href]") ?? null;
    expect(link?.getAttribute("href")).toBe(`/issues/${KEY}/artifacts/${PLAN}#b-t4`);
    act(() => link?.focus());
    expect(document.activeElement).toBe(link);
    expect(
      [...view.container.querySelectorAll('[role="treeitem"][tabindex="0"]')].map((row) =>
        row.getAttribute("data-plan-cycle-row")
      )
    ).toEqual([`${D("t4")} ${D("t5")}`]);
    expect(place?.getAttribute("data-focused")).toBe(D("t5"));

    // The steps: the same line, and the cycle's tasks as rows where the order puts the cycle.
    await showSteps(view);
    expect(rowIds(view.container)).toEqual([D("t1"), D("t4"), D("t5"), D("t6")]);
    expect(view.container.querySelectorAll("[data-plan-cycle]")).toHaveLength(1);
  } finally {
    view.unmount();
  }
});

test("a plan whose every task is in a cycle draws the line, and its tasks as steps", async () => {
  setViewport(1440);
  const view = renderBody(issue([task("t1", { needs: ["t2"] }), task("t2", { needs: ["t1"] })]));
  try {
    expect(view.container.querySelectorAll("[data-plan-bar-row]")).toHaveLength(0);
    expect(view.container.querySelectorAll("[data-plan-cycle]")).toHaveLength(1);
    expect(view.container.querySelectorAll('[role="tree"]')).toHaveLength(1);
    // The chart draws no bar, so the cycle's row is its one Tab stop (dispatch://INFRA-3938 R7).
    expect(
      [...view.container.querySelectorAll('[role="treeitem"][tabindex="0"]')].map((row) =>
        row.getAttribute("data-plan-cycle-row")
      )
    ).toEqual([`${D("t1")} ${D("t2")}`]);
    expect(view.container.textContent).not.toContain(EMPTY_STATE_LINES.plan);
    await showSteps(view);
    expect(rowIds(view.container)).toEqual([D("t1"), D("t2")]);
    expect(view.container.textContent).not.toContain(EMPTY_STATE_LINES.plan);
  } finally {
    view.unmount();
  }
});

test("a plan with no tasks says so in the one line the page has for it, and draws no control", () => {
  setViewport(1440);
  const view = renderBody(issue([]));
  try {
    expect(view.container.querySelectorAll("[data-plan-switch]")).toHaveLength(0);
    expect(view.container.querySelectorAll("[data-plan-chart]")).toHaveLength(0);
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
    expect(rowIds(view.container)).toEqual([D("t2"), D("t1")]);
    expect(["t2", "t1"].map((id) => rowOf(view, id).getAttribute("data-plan-start"))).toEqual([
      "0",
      "0",
    ]);
    expect(view.container.querySelectorAll("[data-plan-need]")).toHaveLength(0);
    expect(view.container.querySelectorAll("[data-plan-cycle]")).toHaveLength(0);
  } finally {
    view.unmount();
  }
});

// The tester's F19: the filter's view hands the tasks over grouped by the statement each one
// serves, which is the spec's order. Where the `needs` leave two tasks free to stand in either
// order, the order the plan is written in decides, and each task is drawn once.
test("tasks free to stand in either order keep the order the plan is written in", () => {
  setViewport(1440);
  // The view's order is t3, t1, t2 (by the statements they serve); the plan is written t1, t2,
  // t3, and t3 needs t1.
  const view = renderBody(
    issue([task("t3", { needs: ["t1"] }), task("t1"), task("t2"), task("t1")]),
    { written: ["t1", "t2", "t3"] }
  );
  try {
    expect(rowIds(view.container)).toEqual([D("t1"), D("t2"), D("t3")]);
    expect(["t1", "t2", "t3"].map((id) => rowOf(view, id).getAttribute("data-plan-start"))).toEqual(
      ["0", "0", "1"]
    );
  } finally {
    view.unmount();
  }
});

// R22, and R3 of dispatch://INFRA-3938: a task that holds no words is named by the page's word
// for its kind wherever the view names it - its own row, the line of a cycle it is in and the
// needs of a task that waits on it - and never by its id, which is nobody's word.
test("a task with no words is named by its kind, never by its id", async () => {
  setViewport(1440);
  const blank = (blockId: string, needs: readonly string[] = []): TreeNode => ({
    ...task(blockId, { needs }),
    text: "",
  });
  const view = renderBody(
    issue([
      blank("t1"),
      task("t2", { needs: ["t1"] }),
      blank("t3", ["t4"]),
      task("t4", { needs: ["t3"] }),
    ])
  );
  try {
    const kind = NODE_TYPE_NAMES.plan_task;
    expect(rowOf(view, "t1").querySelector("[data-node-words]")?.textContent).toBe(kind);
    expect(rowOf(view, "t2").querySelector("[data-plan-need]")?.textContent).toBe(kind);
    const cycles = [...view.container.querySelectorAll("[data-plan-cycle]")];
    expect(cycles).toHaveLength(1);
    expect([...(cycles[0]?.querySelectorAll("a") ?? [])].map((link) => link.textContent)).toEqual([
      kind,
      "Task t4",
    ]);
    for (const id of ["t1", "t3"]) expect(view.container.textContent).not.toContain(D(id));
    // Under "Steps" the row that waits on it names it after "needs" by the same word: F's name
    // of its needs chip, read on the chip, since G's row draws the kind's tag "plan task" on
    // every task row.
    await showSteps(view);
    expect(rowOf(view, "t2").textContent).toContain(kind);
    expect(rowOf(view, "t2").querySelector("[data-node-chip] [data-chip-name]")?.textContent).toBe(
      kind
    );
    for (const id of ["t1", "t3"]) expect(view.container.textContent).not.toContain(D(id));
  } finally {
    view.unmount();
  }
});

// Decision 103 of dispatch://INFRA-1196 (decision 66 stands): K names no need from its own node.
// Part F's view hands a `needs` chip over with no name while the names read is out, and, at the
// head the tester read on the preview of INFRA-1196, where the server's name of the target ends
// inside its directive's opening line (task 25, which needs task 24: 11 needs on one line). While
// a chip has no name the row draws no need for it, and "· needs" only when it draws a need: the
// row drew "· needs" and an empty need then, which names no task at all.
test("a need part F has not named draws no empty need and no '· needs' with nothing after it", () => {
  setViewport(1440);
  // t3 needs t1 and t2, t4 needs t1 alone; part F has named t2 and not t1.
  const root = issue([
    task("t1"),
    task("t2"),
    task("t3", { needs: ["t1", "t2"] }),
    task("t4", { needs: ["t1"] }),
  ]);
  const unnamed: TreeNode = {
    ...root,
    children: root.children.map((one) => ({
      ...one,
      chips: one.chips.map((chip) => (chip.ref.endsWith("#b-t1") ? { ...chip, name: "" } : chip)),
    })),
  };
  const view = renderBody(unnamed);
  try {
    const [both, alone] = ["t3", "t4"].map((blockId) => rowOf(view, blockId));
    expect(
      [...(both?.querySelectorAll("[data-plan-need]") ?? [])].map((one) => [
        one.getAttribute("data-plan-need"),
        one.textContent,
      ])
    ).toEqual([[D("t2"), "Task t2"]]);
    expect(both?.querySelector("[data-plan-facts]")?.textContent).toContain(PLAN_WORDS.needs);
    expect(alone?.querySelectorAll("[data-plan-need]")).toHaveLength(0);
    expect(alone?.querySelector("[data-plan-facts]")?.textContent).not.toContain(PLAN_WORDS.needs);
  } finally {
    view.unmount();
  }
});
