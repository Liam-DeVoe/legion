import { expect, type Mock, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderResult, render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { api } from "../../../api/client";
import type { Decision } from "../../../api/types";
import type { TreeNode } from "../issue-tree";
import { CONTROL_LABELS, EMPTY_STATE_LINES, GAP_WORDS } from "../page-words";
import { presetView } from "../presets";
import { SPEC_WORDS, SpecFilterBody } from "./SpecFilterBody";
import {
  answeredAsk,
  decision,
  decisionNode,
  decisionsRead,
  issueDetails,
  issueTree,
  KEY,
  node,
  READER,
  shaped,
  statement,
  statementRef,
} from "./spec-fixtures";

/**
 * What the Spec filter's body draws, read as a reader reads it (dispatch://INFRA-1196 R5, R17,
 * R22, R35, the approved picture `page-v3-spec-1440.png`).
 *
 * Which decision shaped which statement is read in `spec-decided-by.test.ts`; these rows are
 * about what the page says and what a reader can reach: the legend's words, the two columns'
 * headings, the words of each cell and of each statement's own line, and the link to the whole
 * of a decision. Where the two columns stand side by side and where they stack is the
 * browser's own layout, which no DOM of a unit test lays out, so the widths are read by the
 * browser suite.
 */

/** The body over `tree`, with `made` as the issue's decisions read. */
function drawBody(
  tree: TreeNode,
  made: readonly Decision[] = [],
  { context = new Set<string>(), reader = READER }: { context?: Set<string>; reader?: string } = {}
): { read: Mock<typeof api.getIssueDecisions>; view: RenderResult } {
  const answer = decisionsRead(made);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["decisions", KEY], answer);
  const read = spyOn(api, "getIssueDecisions").mockResolvedValue(answer);
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <SpecFilterBody
          context={context}
          issue={issueDetails()}
          reader={reader}
          root={tree}
          view={presetView(tree, "spec", "").root}
        />
      </MemoryRouter>
    </QueryClientProvider>
  );
  return { read, view };
}

/** The text of every element matching `selector`, in the order they are drawn. */
function textsOf(view: RenderResult, selector: string): string[] {
  return [...view.container.querySelectorAll(selector)].map((element) =>
    (element.textContent ?? "").trim()
  );
}

// R17: the legend names each mark in the words R17 chose. A statement that carries a check is
// covered, never proven: "a statement that has a check is not a statement that is true".
test("the legend names the four marks, and none of them reads proven", () => {
  const { read, view } = drawBody(issueTree([statement("r1")]));
  try {
    expect(
      [...view.container.querySelectorAll("[data-spec-legend-mark]")].map((mark) => [
        mark.getAttribute("data-spec-legend-mark"),
        mark.textContent,
      ])
    ).toEqual([
      ["covered", "covered"],
      ["failing", "failing"],
      ["not_run", "not run"],
      ["no_check", "no check"],
    ]);
    expect(view.container.querySelector("[data-spec-legend]")?.textContent).not.toContain("proven");
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R22: the page draws Statement where it drew Requirement.
test("the two columns are headed Decided by and Statement, never Requirement", () => {
  const { read, view } = drawBody(issueTree([statement("r1")]));
  try {
    expect(textsOf(view, "[data-spec-head] span")).toEqual(["Decided by", "Statement"]);
    expect(view.container.textContent).not.toContain("Requirement");
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R5: the decision stands once beside the statements it shaped, headed by the words it came
// from, with who decided it, when, what they chose, and a link to the whole answer.
test("a person's decision stands once beside its statements, in their own words", () => {
  const ask = answeredAsk("ask-1", {
    selected: ["Changes needed"],
    text: "The node types are all, like, completely so you have to click. And I can't write.",
  });
  const { read, view } = drawBody(
    issueTree([ask, statement("r1"), statement("r2"), decisionNode("d1")]),
    [
      decision({
        asks: ["ask-1"],
        block_id: "d1",
        kind: "person",
        links: [shaped(statementRef("r1")), shaped(statementRef("r2"))],
      }),
    ]
  );
  try {
    const cells = [...view.container.querySelectorAll("[data-spec-cell]")];
    expect(cells).toHaveLength(1);
    expect(cells[0]?.getAttribute("data-said")).toBe("you");
    expect(cells[0]?.getAttribute("data-spec-statements")).toBe("spec#b-r1 spec#b-r2");
    expect(textsOf(view, "[data-spec-said]")).toEqual([SPEC_WORDS.said_you]);
    expect(textsOf(view, "[data-spec-quote]")).toEqual([
      "“The node types are all, like, completely so you have to click.”",
    ]);
    const source = view.container.querySelector("[data-spec-source]")?.textContent ?? "";
    expect(source).toContain("Does the built page work for you?");
    expect(source).toContain(READER);
    expect(source).toContain("14 Nov");
    expect(source).toContain(`${SPEC_WORDS.chose} Changes needed`);
    const whole = view.container.querySelector("[data-spec-whole]");
    expect(whole?.textContent?.trim()).toBe(`${SPEC_WORDS.whole_answer} ›`);
    expect(whole?.getAttribute("href")).toBe(`/issues/${KEY}/asks/ask-1`);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R5: one line on each statement's own row, with no focus and no hover: the words the decision
// came from, who decided and when.
test("each statement carries one line naming the decision that shaped it", () => {
  const ask = answeredAsk("ask-1", { text: "Put the decisions beside the statements. Please." });
  const { read, view } = drawBody(
    issueTree([ask, statement("r1"), statement("r2"), decisionNode("d1")]),
    [
      decision({
        asks: ["ask-1"],
        block_id: "d1",
        kind: "person",
        links: [shaped(statementRef("r1")), shaped(statementRef("r2"))],
      }),
    ]
  );
  try {
    const lines = textsOf(view, "[data-spec-because]");
    expect(lines).toHaveLength(2);
    for (const line of lines) {
      expect(line).toContain(SPEC_WORDS.because);
      expect(line).toContain("“Put the decisions beside the statements.”");
      expect(line).toContain(READER);
      expect(line).toContain("14 Nov");
    }
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R35: a statement with no decision beside it is drawn as such, which is what makes a missing
// tie visible.
test("a statement no decision shaped says so where its decision would stand", () => {
  const { read, view } = drawBody(issueTree([statement("r1")]));
  try {
    expect(textsOf(view, "[data-spec-nothing]")).toEqual([SPEC_WORDS.nothing_one]);
    expect(textsOf(view, "[data-spec-because]")).toEqual([]);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// Two statements no decision shaped stand under one cell, whose words agree with their count.
test("several statements no decision shaped say so in words that agree with their count", () => {
  const { read, view } = drawBody(issueTree([statement("r1"), statement("r2")]));
  try {
    expect(textsOf(view, "[data-spec-nothing]")).toEqual([SPEC_WORDS.nothing_many]);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R5: an agent's decision draws its text and the one word the model holds for how sure it was;
// R2: wherever a decision is drawn, one waiting for review says so.
test("an agent's decision draws its own words, how sure it was and how it stands", () => {
  const made = decisionNode("d1", {
    state: "waiting_review",
    text: "One batched read names every address.",
    unsure: true,
    waiting: true,
  });
  const { read, view } = drawBody(issueTree([statement("r1"), made]), [
    decision({
      block_id: "d1",
      kind: "agent",
      links: [shaped(statementRef("r1"))],
      text: "One batched read names every address.",
      unsure: true,
      waiting_review: true,
    }),
  ]);
  try {
    const cell = view.container.querySelector("[data-spec-cell]");
    expect(cell?.getAttribute("data-said")).toBe("agent");
    expect(cell?.className).toContain("border-dashed");
    expect(textsOf(view, "[data-spec-said]")).toEqual([SPEC_WORDS.said_agent]);
    expect(textsOf(view, "[data-spec-quote]")).toEqual(["“One batched read names every address.”"]);
    const source = view.container.querySelector("[data-spec-source]")?.textContent ?? "";
    expect(source).toContain("Waiting for your review");
    expect(source).toContain("Unsure");
    expect(view.container.querySelector("[data-spec-whole]")?.textContent?.trim()).toBe(
      `${SPEC_WORDS.whole_decision} ›`
    );
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R5: where the answered question is the statement's own parent, its parent line and its
// decision line are one line, so the question is drawn once.
test("the question a statement hangs under is drawn once, as the group's heading", () => {
  const { read, view } = drawBody(
    issueTree([
      answeredAsk("ask-1", { text: "Keep the memo in the pane. It reads better there." }, [
        statement("r1"),
      ]),
    ])
  );
  try {
    const heading = view.container.querySelector("[data-spec-group-heading]")?.textContent ?? "";
    expect(heading).toContain("question");
    expect(heading).toContain("Does the built page work for you?");
    const source = view.container.querySelector("[data-spec-source]")?.textContent ?? "";
    expect(source).not.toContain("Does the built page work for you?");
    expect(textsOf(view, "[data-spec-quote]")).toEqual(["“Keep the memo in the pane.”"]);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R17, R6: each statement carries its mark at the right of its row, by a shape with a name of
// its own, so the mark is never colour alone.
test("each statement carries its mark, by a shape with its own name", () => {
  const check = (id: string, state: string): TreeNode =>
    node({ id, state: state as TreeNode["state"], type: "check" });
  const { read, view } = drawBody(
    issueTree([
      statement("r1", { children: [check("c1", "passed")] }),
      statement("r2", { children: [check("c2", "failed")] }),
      statement("r3", { children: [check("c3", "no-result")] }),
      statement("r4", { counts: { checks: 0, planTasks: 0 }, gaps: ["no_check"] }),
    ])
  );
  try {
    expect(
      [...view.container.querySelectorAll("[data-spec-statement]")].map((row) => [
        row.getAttribute("data-spec-mark"),
        row.querySelector("svg[data-glyph]")?.getAttribute("aria-label"),
      ])
    ).toEqual([
      ["covered", "Covered"],
      ["failing", "Failing"],
      ["not_run", "Not run"],
      ["no_check", "No check"],
    ]);
    // No two of the four shapes are one shape.
    const paths = [...view.container.querySelectorAll("[data-spec-statement] svg path")].map(
      (path) => path.getAttribute("d")
    );
    expect(new Set(paths).size).toBe(4);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// Ruling 1: a drawn row draws its node's words in full, and a statement's own words carry its
// number where the spec wrote one.
test("a statement's words are drawn in full", () => {
  const words =
    "**R1.** Every node's words are on the page without opening it; an agent's long words " +
    "show their first six lines and the rest one key away, and his words are never cut.";
  const { read, view } = drawBody(issueTree([statement("r1", { text: words })]));
  try {
    expect(view.container.querySelector("[data-node-words]")?.textContent).toBe(words);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R2: a row the filter draws only to place another is drawn faint and marked as context.
test("a statement drawn as context is marked as such", () => {
  const { read, view } = drawBody(issueTree([statement("r1")]), [], {
    context: new Set(["spec#b-r1"]),
  });
  try {
    expect(
      view.container.querySelector("[data-spec-statement]")?.hasAttribute("data-node-context")
    ).toBe(true);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// A statement of this body is a row of the page: the readers that walk the outline's rows find
// it by the attributes every row carries (`e2e/issue-page.ts`'s `drawnRows` and `nodeRow`),
// which is what c-presets compares a filter's drawn ids with.
test("a statement is drawn as a row of the page, with the id and kind every row carries", () => {
  const { read, view } = drawBody(issueTree([statement("r1")]));
  try {
    const row = view.container.querySelector("[data-node-row]");
    expect(row?.getAttribute("data-node-id")).toBe("spec#b-r1");
    expect(row?.getAttribute("data-node-type")).toBe("requirement");
    expect(row?.getAttribute("aria-level")).toBe("1");
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// An issue whose spec holds no statement says so, in the words the outline says it in.
test("an issue with no statements says so in the page's own line", () => {
  const { read, view } = drawBody(issueTree([]));
  try {
    expect(view.container.querySelector("[data-spec-empty]")?.textContent).toBe(
      EMPTY_STATE_LINES.spec
    );
    expect(view.container.querySelectorAll("[data-spec-statement]")).toHaveLength(0);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// The body is the outline the page draws under the Spec filter, so it is one tree, named as
// every other filter's outline is: every browser row reads the page by that name
// (`e2e/issue-layout.e2e.ts`'s `expectIssuePageDrawn`). Its items are each statement and the
// decision beside it, which is what the focus moves between (R5's `w`).
test("the body draws one tree, named as the outline is, of the statements and their decisions", () => {
  const { read, view } = drawBody(issueTree([statement("r1"), decisionNode("d1")]), [
    decision({ block_id: "d1", kind: "agent", links: [shaped(statementRef("r1"))] }),
  ]);
  try {
    const tree = view.container.querySelector('[role="tree"]');
    expect(tree?.getAttribute("aria-label")).toBe(CONTROL_LABELS.tree);
    expect(
      [...view.container.querySelectorAll('[role="treeitem"]')].map((item) =>
        item.hasAttribute("data-spec-cell") ? "cell" : item.getAttribute("data-node-id")
      )
    ).toEqual(["cell", "spec#b-r1"]);
  } finally {
    view.unmount();
    read.mockRestore();
  }
});

// R10, R17: a gap the mark does not already say is drawn under the statement's words, in the
// few words the page knows it by; the one for a statement no check serves is the mark at the
// right of the row, and is not said twice.
test("a gap the mark does not say is drawn under the statement, and no check is not said twice", () => {
  const { read, view } = drawBody(
    issueTree([
      statement("r1", { gaps: ["shaped_disagreement"] }),
      statement("r2", { counts: { checks: 0, planTasks: 0 }, gaps: ["no_check"] }),
    ])
  );
  try {
    const rows = [...view.container.querySelectorAll("[data-spec-statement]")];
    expect(
      [...(rows[0]?.querySelectorAll("[data-gap-kind]") ?? [])].map((gap) => [
        gap.getAttribute("data-gap-kind"),
        gap.textContent,
      ])
    ).toEqual([["shaped_disagreement", GAP_WORDS.shaped_disagreement]]);
    expect(rows[1]?.querySelectorAll("[data-gap-kind]")).toHaveLength(0);
    expect(rows[1]?.getAttribute("data-spec-mark")).toBe("no_check");
  } finally {
    view.unmount();
    read.mockRestore();
  }
});
