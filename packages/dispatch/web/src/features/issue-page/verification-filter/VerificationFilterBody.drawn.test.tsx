import { beforeAll, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type RenderResult, render, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";

import { displayTree } from "../../../__tests__/display-trees";
import type { CheckBlock } from "../../../api/types";
import { warmMarkdownRenderer } from "../../refs/markdown-engine";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { appKeymap } from "../../shell/keymap";
import type { TreeNode } from "../issue-tree";
import { NoTooltips } from "../NoTooltips";
import {
  ANSWER_LINES,
  BLOCK_ATTRIBUTE_LABELS,
  CHECK_WHEN_WORDS,
  type CheckState,
  CONTROL_LABELS,
  EMPTY_STATE_LINES,
  GAP_LINES,
  GLYPH_TOKENS,
  NODE_FACT_LABELS,
  NODE_STATE_WORDS,
  NODE_TYPE_NAMES,
  RESULT_STANDS_WORDS,
  RESULT_VERDICT_WORDS,
  sentenceCase,
  VERIFICATION_WORDS,
} from "../page-words";
import { presetView } from "../presets";
import { shortCommit } from "./statement-state";
import {
  VerificationFilterBody,
  type VerificationRead,
  type VerificationReads,
} from "./VerificationFilterBody";
import {
  COMMIT,
  check,
  checkNode,
  ISSUE_KEY,
  node,
  OTHER_COMMIT,
  result,
  statementNode,
  verificationView,
} from "./verification-fixtures";

/**
 * What the Verification filter's body draws, read as a reader reads it
 * (dispatch://INFRA-1196 R17, R2, R10, R22, and the approved picture
 * `page-v3-verification-1440.png`).
 *
 * How the model reads the issue's results is `statement-state.test.ts`; these rows are about
 * what the page says: the covered count, the one commit, the word and the shape of each state,
 * the gap on a statement no check serves, and the facts a check's body opens. Where the three
 * columns stand side by side and where they stack is the browser's own layout, which no DOM of
 * a unit test lays out, so the widths are read by the browser suite (c-checks-readable).
 */

// A check's words are drawn from the results read's reading of them, which `displayTree` reads
// with the page's own engine.
beforeAll(async () => {
  await warmMarkdownRenderer();
});

/** What a case gives the body beside its statements and its checks: the words the reader typed
 *  (`query`), which the view is filtered by (`presetView`), the nodes an address named, `whole`
 *  where a case gives it, and how the three reads stand (`reads`), which is all of them
 *  answered where a case gives nothing else. */
interface DrawOptions {
  readonly named?: readonly string[];
  readonly query?: string;
  readonly reads?: VerificationReads;
  readonly whole?: TreeNode;
}

/** A query client for one drawn body, which answers nothing on its own. */
function testClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
}

/** The body over the statements given, with `checks` as the issue's results read, drawn from
 *  the Verification filter's own view: the tree it leaves and the rows it marks as context. It
 *  is drawn inside `NoTooltips`, as the slot draws it, so an author is named as the page names
 *  one, and it says what the slot says where it has nothing to draw: the page's line for no
 *  checks with no query typed, and its line for no match under one. A case that draws the body
 *  again with other reads passes it to `rerender` with the same `client`. */
function bodyOf(
  statements: readonly TreeNode[],
  checks: readonly CheckBlock[],
  { named = [], query = "", reads = { state: "answered" }, whole }: DrawOptions = {},
  client: QueryClient = testClient()
): ReactElement {
  const issue = node({ children: statements, id: ISSUE_KEY, text: "An issue", type: "issue" });
  const view = presetView(issue, "verification", query, new Set(named));
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <NoTooltips>
          <VerificationFilterBody
            checks={checks}
            context={view.context}
            emptyLine={query === "" ? EMPTY_STATE_LINES.verification : EMPTY_STATE_LINES.no_match}
            reads={reads}
            view={view.root}
            {...(whole === undefined ? {} : { whole })}
          />
        </NoTooltips>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

/** The body `bodyOf` builds, drawn. */
function drawBody(
  statements: readonly TreeNode[],
  checks: readonly CheckBlock[],
  options: DrawOptions = {}
): RenderResult {
  return render(bodyOf(statements, checks, options));
}

/** The text of every element matching `selector`, in the order they are drawn. */
function textsOf(view: RenderResult, selector: string): string[] {
  return [...view.container.querySelectorAll(selector)].map((element) =>
    (element.textContent ?? "").replace(/\s+/gu, " ").trim()
  );
}

/** What the page says in its own words: the text of each `[data-page-chrome]` element with
 *  every `[data-author-text]` descendant taken out, which is how `page-words.ts` says a reader of
 *  the page's words reads them, and what c-plain-words reads word by word. */
function chromeTexts(view: RenderResult): string[] {
  return [...view.container.querySelectorAll("[data-page-chrome]")]
    .map((element) => {
      const copy = element.cloneNode(true) as Element;
      for (const authored of copy.querySelectorAll("[data-author-text]")) authored.remove();
      return (copy.textContent ?? "").replace(/\s+/gu, " ").trim();
    })
    .filter((text) => text !== "");
}

/** A check that passed at the commit the page names. */
function passing(blockId: string): CheckBlock {
  return check({
    block_id: blockId,
    pass_stands: { answer: "yes" },
    results: [result({ check_block_id: blockId, id: `result-${blockId}` })],
  });
}

/** A check that failed at that commit. */
function failing(blockId: string): CheckBlock {
  return check({
    block_id: blockId,
    pass_stands: { answer: "no" },
    results: [
      result({ check_block_id: blockId, failed: 1, id: `result-${blockId}`, verdict: "fail" }),
    ],
  });
}

/** Each key a handler held back, in the order pressed, from now until `stop` is called. */
function holdKeys(): { held: string[]; stop: () => void } {
  const held: string[] = [];
  const hold = (event: KeyboardEvent) => {
    if (event.defaultPrevented) held.push(event.key);
  };
  document.addEventListener("keydown", hold);
  return { held, stop: () => document.removeEventListener("keydown", hold) };
}

/** A check's words with a link and a reference in them. */
const LINKED_WORDS = "Read [the runbook](https://example.com/runbook) and dispatch://CORE-2 first.";

/** The node of a check whose words are `LINKED_WORDS`, with the results read's reading of them
 *  (`text_tree`), which `checkNode` leaves out. */
function linkedCheckNode(block: CheckBlock): TreeNode {
  return { ...checkNode(block), tree: displayTree(LINKED_WORDS) };
}

// R17: "How many statements carry a check is drawn as covered, and no count, legend or heading
// reads proven or met for coverage", and "The page names one commit for the issue's results ...
// drawn once where the results are read".
test("the summary counts the statements that carry a check as covered and names one commit", () => {
  const met = passing("c-one");
  const unrun = check({ block_id: "c-two", pass_stands: { answer: "no" }, results: [] });
  const view = drawBody(
    [
      statementNode("r1", "The page draws the checks.", [checkNode(met)]),
      statementNode("r2", "The page draws their results.", [checkNode(unrun)]),
      statementNode("r3", "Nothing serves this one.", []),
    ],
    [met, unrun]
  );
  try {
    expect(textsOf(view, "[data-verification-covered]")).toEqual([
      `2 ${VERIFICATION_WORDS.of} 3 ${VERIFICATION_WORDS.covered}`,
    ]);
    expect(VERIFICATION_WORDS.covered).toBe("covered");
    const summary = view.container.querySelector("[data-verification-summary]");
    expect(summary?.textContent).not.toContain("proven");
    expect(textsOf(view, "[data-verification-commit]")).toEqual([
      `${VERIFICATION_WORDS.commit} ${shortCommit(COMMIT)}`,
    ]);
    // One commit for the issue's results, and no result draws one of its own on its line.
    expect(
      [...view.container.querySelectorAll("[data-verification-result]")].filter((drawn) =>
        (drawn.textContent ?? "").includes(shortCommit(COMMIT))
      )
    ).toEqual([]);
  } finally {
    view.unmount();
  }
});

// Part V's R3 ("No result draws a commit on its own line") and the root's R17 ("The page names
// one commit for the issue's results ... drawn once"), with a check's facts open: each result's
// facts open with the check's, and they draw what the result recorded but its commit. No result
// draws a commit anywhere but the summary's one line, neither the commit the page names nor
// another a result ran at, whole or in the ten characters the summary draws.
test("with a check's facts open, no result draws its commit anywhere but the summary", async () => {
  const user = userEvent.setup();
  const busy = check({
    block_id: "c-one",
    results: [
      result({ commit_sha: OTHER_COMMIT, created_at: "2026-11-13T09:00:00Z", id: "older" }),
      result({ created_at: "2026-11-14T09:00:00Z", id: "newest" }),
    ],
  });
  const view = drawBody([statementNode("r1", "The page draws it.", [checkNode(busy)])], [busy]);
  try {
    // The earlier result drawn too, and the check's facts opened by the focus on its words.
    await user.click(view.container.querySelector("[data-verification-earlier]") as Element);
    act(() =>
      view.container.querySelector<HTMLElement>("[data-verification-check-words]")?.focus()
    );
    expect(
      view.container.querySelector("[data-verification-check]")?.getAttribute("aria-expanded")
    ).toBe("true");
    // Each result's facts are drawn, its verdict among them.
    expect(
      [...view.container.querySelectorAll("[data-verification-result]")].map((drawn) => [
        drawn.getAttribute("data-node-id"),
        (drawn.querySelector("[data-node-body]")?.textContent ?? "").includes(
          RESULT_VERDICT_WORDS.pass
        ),
      ])
    ).toEqual([
      ["newest", true],
      ["older", true],
    ]);
    expect(textsOf(view, "[data-verification-commit]")).toEqual([
      `${VERIFICATION_WORDS.commit} ${shortCommit(COMMIT)}`,
    ]);
    // Everything the body draws but the summary's commit line.
    const body = view.container.querySelector("[data-verification-body]")?.cloneNode(true);
    expect(body).toBeInstanceOf(Element);
    for (const line of (body as Element).querySelectorAll("[data-verification-commit]")) {
      line.remove();
    }
    const rest = body?.textContent ?? "";
    for (const sha of [COMMIT, OTHER_COMMIT]) {
      expect(rest, `a result draws the commit ${sha}`).not.toContain(shortCommit(sha));
      expect(rest, `a result draws the commit ${sha}`).not.toContain(sha);
    }
  } finally {
    view.unmount();
  }
});

// R17: "Each of the seven states a check can be in is told apart by a word and a shape".
test("each of the seven states a check can be in is drawn by its own word and shape", () => {
  const withdrawn = {
    actor: { id: "tester", kind: "session" as const },
    created_at: "2026-11-15T10:00:00Z",
    reason: "The run was wrong.",
  };
  const blocks: readonly CheckBlock[] = [
    passing("c-passed"),
    failing("c-failed"),
    check({ block_id: "c-no-result", pass_stands: { answer: "no" }, results: [] }),
    check({
      block_id: "c-older",
      pass_stands: { answer: "no" },
      results: [result({ check_block_id: "c-older", check_words_moved: true, id: "result-older" })],
    }),
    check({
      block_id: "c-retracted",
      pass_stands: { answer: "no" },
      results: [
        result({ check_block_id: "c-retracted", id: "result-retracted", retracted: withdrawn }),
      ],
    }),
    check({
      block_id: "c-unmarked",
      pass_stands: { answer: "unknown" },
      results: [
        result({
          check_block_id: "c-unmarked",
          commit_stands: { answer: "unknown" },
          id: "result-unmarked",
        }),
      ],
    }),
    check({
      block_id: "c-unreadable",
      pass_stands: undefined as unknown as never,
      results: [result({ check_block_id: "c-unreadable", id: "result-unreadable" })],
    }),
  ];
  const view = drawBody(
    [statementNode("r1", "The page draws the checks.", blocks.map(checkNode))],
    blocks
  );
  try {
    const drawn = [...view.container.querySelectorAll("[data-verification-check]")].map((card) => ({
      shape: card.querySelector("svg[data-glyph]")?.getAttribute("data-glyph"),
      state: card.getAttribute("data-node-state"),
      word: card.querySelector("[data-node-state]")?.textContent,
    }));
    expect(drawn.map((held) => held.state)).toEqual([
      "passed",
      "failed",
      "no-result",
      "older",
      "retracted",
      "unmarked",
      "unreadable",
    ]);
    expect(drawn.map((held) => held.word)).toEqual([
      NODE_STATE_WORDS.check.passed,
      NODE_STATE_WORDS.check.failed,
      NODE_STATE_WORDS.check["no-result"],
      NODE_STATE_WORDS.check.older,
      NODE_STATE_WORDS.check.retracted,
      NODE_STATE_WORDS.check.unmarked,
      NODE_STATE_WORDS.check.unreadable,
    ]);
    // Every one of the seven is told from the other six by a shape as well as by a word.
    expect(drawn.map((held) => held.shape)).toEqual(
      drawn.map((held) => GLYPH_TOKENS.states.check[held.state as CheckState])
    );
    expect(new Set(drawn.map((held) => held.shape)).size).toBe(7);
    expect(new Set(drawn.map((held) => held.word)).size).toBe(7);
  } finally {
    view.unmount();
  }
});

// R17: Met where every check of it passed at the commit the page names, Not met where one
// failed there, Unknown otherwise - here a pass recorded against another commit.
test("a statement reads Met, Not met or Unknown by how its checks stand at that commit", () => {
  const met = passing("c-one");
  const failed = failing("c-two");
  const elsewhere = check({
    block_id: "c-three",
    pass_stands: { answer: "yes" },
    results: [result({ check_block_id: "c-three", commit_sha: OTHER_COMMIT, id: "result-three" })],
  });
  const view = drawBody(
    [
      statementNode("r1", "Every check of this one passed.", [checkNode(met)]),
      statementNode("r2", "One check of this one failed.", [checkNode(failed)]),
      statementNode("r3", "This one was proved somewhere else.", [checkNode(elsewhere)]),
    ],
    [met, failed, elsewhere]
  );
  try {
    expect(
      [...view.container.querySelectorAll("[data-verification-card]")].map((card) => [
        card.getAttribute("data-statement-state"),
        card.querySelector("[data-statement-state]")?.textContent,
      ])
    ).toEqual([
      ["met", VERIFICATION_WORDS.states.met],
      ["not_met", VERIFICATION_WORDS.states.not_met],
      ["unknown", VERIFICATION_WORDS.states.unknown],
    ]);
    expect([VERIFICATION_WORDS.states.met, VERIFICATION_WORDS.states.not_met]).toEqual([
      "Met",
      "Not met",
    ]);
    // The check of the third says why: its result was recorded against another commit.
    expect(textsOf(view, "[data-verification-check] [data-node-state]").at(-1)).toBe(
      NODE_STATE_WORDS.check.older
    );
  } finally {
    view.unmount();
  }
});

// R10: a statement no check serves carries its gap, where the picture draws "No check proves
// this". The page says it in its own words and names the kind of gap it is.
test("a statement no check serves carries its gap in full", () => {
  const served = passing("c-one");
  const view = drawBody(
    [
      statementNode("r1", "The page draws it.", [checkNode(served)]),
      {
        ...statementNode("r2", "Nothing serves this one.", []),
        gaps: ["no_check"],
      },
    ],
    [served]
  );
  try {
    expect(
      [...view.container.querySelectorAll("[data-gap]")].map((gap) => [
        gap.getAttribute("data-gap-kind"),
        gap.textContent,
      ])
    ).toEqual([["no_check", GAP_LINES.no_check]]);
    expect(GAP_LINES.no_check).toBe("No check serves this statement.");
  } finally {
    view.unmount();
  }
});

// Part V's R9 and departures 2 and 10: where the coverage read counts more checks for a
// statement than the view with no query draws under it, the rest serve it from an issue under
// this one. The statement is covered, carries no gap, and draws one line after its checks with
// how many. A words query that leaves out the statement's own check neither calls that check
// "elsewhere" nor changes how the statement stands, what the summary counts or what the chips
// count and narrow by.
test("a statement that a child's check covers draws one line of the checks drawn elsewhere", async () => {
  const user = userEvent.setup();
  const own = passing("c-one");
  const partly = {
    ...statementNode("r1", "One check of this issue serves it.", [checkNode(own)]),
    counts: { checks: 2, planTasks: 0 },
  };
  const childOnly = {
    ...statementNode("r2", "Only the checks of an issue under this one serve it.", []),
    counts: { checks: 3, planTasks: 0 },
  };
  /** Each line of the checks drawn elsewhere: the statement whose card holds it, what it reads,
   *  its count as text of the reader's own, and whether its words are the page's. */
  const linesOf = (drawn: RenderResult) =>
    [...drawn.container.querySelectorAll("[data-verification-elsewhere]")].map((line) => [
      line.closest("[data-verification-card]")?.getAttribute("data-node-id"),
      (line.textContent ?? "").replace(/\s+/gu, " ").trim(),
      line.querySelector("[data-author-text]")?.textContent,
      line.hasAttribute("data-page-chrome"),
    ]);

  const unfiltered = drawBody([partly, childOnly], [own]);
  try {
    expect(linesOf(unfiltered)).toEqual([
      [partly.id, `1 ${VERIFICATION_WORDS.elsewhere_one}`, "1", true],
      [childOnly.id, `3 ${VERIFICATION_WORDS.elsewhere_many}`, "3", true],
    ]);
    expect([VERIFICATION_WORDS.elsewhere_one, VERIFICATION_WORDS.elsewhere_many]).toEqual([
      "more check elsewhere",
      "more checks elsewhere",
    ]);
    // The line stands after the statement's group of checks, inside its card.
    const line = unfiltered.container.querySelector("[data-verification-elsewhere]");
    expect(line?.previousElementSibling?.getAttribute("role")).toBe("group");
    // A statement only a child's checks serve carries no gap, and the covered count holds it.
    expect(unfiltered.container.querySelectorAll("[data-gap]")).toHaveLength(0);
    expect(textsOf(unfiltered, "[data-verification-covered]")).toEqual([
      `2 ${VERIFICATION_WORDS.of} 2 ${VERIFICATION_WORDS.covered}`,
    ]);

    // The query's words are the first statement's and no check's, so it leaves that statement
    // with no check drawn under it, and the second statement out.
    const filtered = drawBody([partly, childOnly], [own], {
      query: "this issue serves",
      whole: verificationView([partly, childOnly]),
    });
    try {
      expect(filtered.container.querySelectorAll("[data-verification-check]")).toHaveLength(0);
      expect(
        [...filtered.container.querySelectorAll("[data-verification-card]")].map((card) =>
          card.getAttribute("data-node-id")
        )
      ).toEqual([partly.id]);
      // Its own check is still not "elsewhere".
      expect(linesOf(filtered)).toEqual([
        [partly.id, `1 ${VERIFICATION_WORDS.elsewhere_one}`, "1", true],
      ]);
      /** How the first statement stands: its state, its word, its shape and its card's edge. */
      const standing = (drawn: RenderResult) => {
        const card = drawn.container.querySelector("[data-verification-card]");
        return [
          card?.getAttribute("data-statement-state"),
          card?.querySelector("[data-statement-state]")?.textContent,
          card?.querySelector("svg[data-glyph]")?.getAttribute("data-glyph"),
          card?.classList.contains("border-l-page-failed"),
        ];
      };
      expect(standing(unfiltered)).toEqual([
        "met",
        VERIFICATION_WORDS.states.met,
        GLYPH_TOKENS.statements.met,
        false,
      ]);
      expect(standing(filtered)).toEqual(standing(unfiltered));
      // The summary and the chips are the issue's, as they are with no query.
      for (const selector of [
        "[data-verification-covered]",
        "[data-verification-legend]",
        "[data-verification-check-legend]",
        "[data-verification-chip]",
      ]) {
        expect(textsOf(filtered, selector)).toEqual(textsOf(unfiltered, selector));
      }
      // The bar's fills, each by its width, in both.
      const [filteredBar, unfilteredBar] = [filtered, unfiltered].map((drawn) =>
        [...drawn.container.querySelectorAll("[data-verification-bar] > i")].map((fill) =>
          fill.getAttribute("style")
        )
      );
      expect(filteredBar).toEqual(unfilteredBar);
      expect(textsOf(filtered, "[data-verification-covered]")).toEqual([
        `2 ${VERIFICATION_WORDS.of} 2 ${VERIFICATION_WORDS.covered}`,
      ]);
      expect(textsOf(filtered, "[data-verification-chip]")).toEqual([
        `${VERIFICATION_WORDS.all} 2`,
        `${VERIFICATION_WORDS.states.met} 1`,
        `${VERIFICATION_WORDS.states.not_met} 0`,
        `${VERIFICATION_WORDS.states.unknown} 1`,
      ]);
      // The chip Met narrows by the state the statement reads, so it draws it.
      await user.click(
        filtered.container.querySelector('[data-verification-chip="met"]') as Element
      );
      expect(
        [...filtered.container.querySelectorAll("[data-verification-card]")].map((card) =>
          card.getAttribute("data-node-id")
        )
      ).toEqual([partly.id]);
    } finally {
      filtered.unmount();
    }
  } finally {
    unfiltered.unmount();
  }

  // A count at or below the checks the view draws under the statement draws no line, nor does a
  // statement the coverage read counts nothing for; and until the reads have answered no line is
  // drawn at all.
  const two = passing("c-two");
  const even = {
    ...statementNode("r3", "Its one check is all the coverage read counts.", [checkNode(own)]),
    counts: { checks: 1, planTasks: 0 },
  };
  const fewer = {
    ...statementNode("r4", "The coverage read counts fewer checks than are drawn.", [
      checkNode(own),
      checkNode(two),
    ]),
    counts: { checks: 1, planTasks: 0 },
  };
  const uncounted = statementNode("r5", "The coverage read counts nothing for it.", [
    checkNode(two),
  ]);
  const none = drawBody([even, fewer, uncounted], [own, two]);
  try {
    expect(none.container.querySelectorAll("[data-verification-card]")).toHaveLength(3);
    expect(linesOf(none)).toEqual([]);
  } finally {
    none.unmount();
  }
  const waiting = drawBody([partly, childOnly], [own], { reads: { state: "waiting" } });
  try {
    expect(linesOf(waiting)).toEqual([]);
  } finally {
    waiting.unmount();
  }
});

// R17: "a command renders as code with a copy control ... and `when` reads as words", and R15:
// a check draws all four of its attributes and a result what it recorded. The body opens where
// the reader's focus reaches the check and closes when the focus leaves it; a press keeps it
// open once the focus has gone, and a press on an open one closes it (R6). Part V's departure
// 5: the check's row says whether it is open, and its words, a tab stop with no role, take the
// press by a pointer, Enter or Space, as its latest result's cell takes a pointer's.
test("a check's facts open where the reader's focus is: when as words, the run as a command", async () => {
  const user = userEvent.setup();
  const shot = "dispatch://CORE-1/artifact/screenshot-png@v1";
  const seeded = check({
    block_id: "c-one",
    pass_stands: { answer: "yes" },
    results: [result({ id: "result-one", screenshots: [shot] })],
  });
  const view = drawBody([statementNode("r1", "The page draws it.", [checkNode(seeded)])], [seeded]);
  try {
    expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(0);
    const card = view.container.querySelector("[data-verification-check]");
    const words = view.container.querySelector<HTMLElement>("[data-verification-check-words]");
    expect(words).not.toBeNull();
    expect([
      words?.tabIndex,
      words?.hasAttribute("role"),
      words?.hasAttribute("aria-expanded"),
    ]).toEqual([0, false, false]);
    expect(card?.getAttribute("aria-expanded")).toBe("false");
    act(() => words?.focus());
    expect(card?.getAttribute("aria-expanded")).toBe("true");

    expect((card?.textContent ?? "").includes(CHECK_WHEN_WORDS.before_merge)).toBe(true);
    expect(card?.textContent).not.toContain("before_merge");
    const commands = [...view.container.querySelectorAll("[data-node-command]")];
    expect(commands.length).toBeGreaterThan(0);
    expect(commands.map((command) => command.tagName)).toEqual(commands.map(() => "CODE"));
    expect(
      view.container.querySelectorAll(`[aria-label="${CONTROL_LABELS.copy_command}"]`).length
    ).toBe(commands.length);
    // The screenshot is drawn as a picture: the opened facts hold an `img` for it. Whether its
    // bytes load is row 1's to read in the browser (`e2e/issue-page-v.e2e.ts`).
    expect(view.container.querySelectorAll("[data-node-body] img").length).toBeGreaterThan(0);

    // The focus leaving it closes what the focus opened.
    act(() => words?.blur());
    expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(0);
    expect(card?.getAttribute("aria-expanded")).toBe("false");
    // The reader pressed it open, so it stays open with the focus elsewhere.
    await user.click(words as Element);
    act(() => words?.blur());
    expect(view.container.querySelectorAll("[data-node-body]").length).toBeGreaterThan(0);
    // And a press on an open one closes it, which is what the control says it does.
    await user.click(words as Element);
    expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(0);
    expect(card?.getAttribute("aria-expanded")).toBe("false");

    // Enter and Space on the words are the same press, the focus on them, and Space scrolls
    // nothing: each key's own default is held back.
    expect(document.activeElement).toBe(words);
    const { held, stop } = holdKeys();
    try {
      await user.keyboard("{Enter}");
      expect(card?.getAttribute("aria-expanded")).toBe("true");
      expect(view.container.querySelectorAll("[data-node-body]").length).toBeGreaterThan(0);
      await user.keyboard(" ");
      expect(card?.getAttribute("aria-expanded")).toBe("false");
      expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(0);
    } finally {
      stop();
    }
    expect(held).toEqual(["Enter", " "]);

    // A pointer's press on the latest result's cell opens the facts, and they stay open once
    // the focus that press brought has gone.
    act(() => words?.blur());
    expect(card?.getAttribute("aria-expanded")).toBe("false");
    await user.click(view.container.querySelector("[data-verification-result]") as Element);
    act(() => (document.activeElement as HTMLElement | null)?.blur());
    expect(card?.getAttribute("aria-expanded")).toBe("true");
    expect(view.container.querySelectorAll("[data-node-body]").length).toBeGreaterThan(0);
  } finally {
    view.unmount();
  }
});

// R2 and the architect's ruling on rows: a statement is drawn as context, as the filter's own
// view marks it, with its words and the node its words came from as one line on its row.
test("a statement is marked as context, with its words and the node they came from", () => {
  const served = passing("c-one");
  const statement = statementNode(
    "r1",
    "The reader reads every word of this statement, which runs past a hundred characters so that nothing about it is cut.",
    [checkNode(served)]
  );
  const askedBy = node({
    children: [statement],
    id: "comment-1",
    text: "Where do I see what proves this?",
    type: "comment",
  });
  const view = drawBody([askedBy], [served]);
  try {
    const card = view.container.querySelector("[data-verification-card]");
    expect(card?.hasAttribute("data-node-context")).toBe(true);
    expect(card?.getAttribute("data-node-id")).toBe(statement.id);
    expect(card?.querySelector("[data-node-words]")?.textContent).toBe(statement.text);
    expect([
      card?.querySelector("[data-node-parent-line]")?.getAttribute("data-parent-type"),
      card?.querySelector("[data-node-parent-line]")?.textContent,
    ]).toEqual(["comment", `${NODE_TYPE_NAMES.comment}${askedBy.text}`]);
  } finally {
    view.unmount();
  }
});

// R18 and part V's R8: a statement's parent line is the link a reader follows to the node its
// words came from where that node has an address, as the outline's parent line is
// (`NodeRow.tsx`'s `ParentLink`), and the link is a tab stop, since the tree's keys move between
// rows and reach no link inside one. A parent with no address is named by its words alone.
test("a statement's parent line links the node it came from where that node has an address", () => {
  const served = passing("c-one");
  const other = passing("c-two");
  const linked = statementNode("r1", "The reader reaches the comment this came from.", [
    checkNode(served),
  ]);
  const unlinked = statementNode("r2", "The reader reads where this came from.", [
    checkNode(other),
  ]);
  const addressed = node({
    children: [linked],
    id: "comment-1",
    reference: `dispatch://${ISSUE_KEY}/comment/comment-1`,
    text: "Where do I see what proves this?",
    type: "comment",
  });
  const unaddressed = node({
    children: [unlinked],
    id: "comment-2",
    text: "And where do I see this one?",
    type: "comment",
  });
  const view = drawBody([addressed, unaddressed], [served, other]);
  try {
    const withAddress = view.container.querySelector(
      `[data-verification-card][data-node-id="${linked.id}"] [data-node-parent-line]`
    );
    expect([
      withAddress?.getAttribute("data-parent-id"),
      withAddress?.getAttribute("data-parent-type"),
      withAddress?.textContent,
    ]).toEqual([addressed.id, "comment", `${NODE_TYPE_NAMES.comment}${addressed.text}`]);
    expect(
      [...(withAddress?.querySelectorAll<HTMLAnchorElement>("a") ?? [])].map((link) => [
        link.getAttribute("href"),
        link.tabIndex,
        link.textContent,
        link.hasAttribute("data-author-text"),
      ])
    ).toEqual([[`/issues/${ISSUE_KEY}/comments/comment-1`, 0, addressed.text, true]]);

    const withoutAddress = view.container.querySelector(
      `[data-verification-card][data-node-id="${unlinked.id}"] [data-node-parent-line]`
    );
    expect([
      withoutAddress?.getAttribute("data-parent-id"),
      withoutAddress?.getAttribute("data-parent-type"),
      withoutAddress?.textContent,
    ]).toEqual([unaddressed.id, "comment", `${NODE_TYPE_NAMES.comment}${unaddressed.text}`]);
    expect(withoutAddress?.querySelectorAll("a")).toHaveLength(0);
  } finally {
    view.unmount();
  }
});

// Ruling 6: the body draws the newest result of each check; its earlier results are one line
// with their count, which opens them in place, and the result the check's state stands on is
// marked wherever it is drawn.
test("a check draws its newest result, folds the rest behind their count, and marks the one that stands", async () => {
  const user = userEvent.setup();
  const busy = check({
    block_id: "c-one",
    pass_stands: { answer: "yes" },
    results: [
      result({ created_at: "2026-11-12T09:00:00Z", id: "oldest", observed: "38 passed" }),
      result({ created_at: "2026-11-14T09:00:00Z", id: "newest", observed: "41 passed" }),
      result({ created_at: "2026-11-13T09:00:00Z", id: "middle", observed: "40 passed" }),
    ],
  });
  const view = drawBody([statementNode("r1", "The page draws it.", [checkNode(busy)])], [busy]);
  try {
    const drawn = () =>
      [...view.container.querySelectorAll("[data-verification-result]")].map((one) => [
        one.getAttribute("data-node-id"),
        one.hasAttribute("data-result-stands"),
      ]);
    expect(drawn()).toEqual([["newest", true]]);
    expect(
      view.container
        .querySelector("[data-result-stands]")
        ?.textContent?.startsWith(VERIFICATION_WORDS.stands)
    ).toBe(true);
    const earlier = view.container.querySelector("[data-verification-earlier]");
    expect(earlier?.textContent?.replace(/\s+/gu, " ")).toBe(
      `2 ${VERIFICATION_WORDS.earlier_many}`
    );
    expect(earlier?.getAttribute("aria-expanded")).toBe("false");
    await user.click(earlier as Element);
    expect(earlier?.getAttribute("aria-expanded")).toBe("true");
    expect(drawn()).toEqual([
      ["newest", true],
      ["middle", false],
      ["oldest", false],
    ]);
  } finally {
    view.unmount();
  }
});

// Part V's R8, and the reviewer's finding on pull request #4221 at the row's key: the results
// read answers again on every result recorded or withdrawn while the reader is in the view
// (`api/event-refresh.ts`), so a check's newest result changes under the reader. Its row under a
// statement is the same row after it: the focus on its words stays on the same element, the
// facts that focus opened stay open, and so do the earlier results the reader opened.
test("a check under a statement keeps the reader's focus and its open earlier results when its newest result changes", async () => {
  const user = userEvent.setup();
  const earlier = result({ created_at: "2026-11-13T09:00:00Z", id: "earlier" });
  const newest = result({ created_at: "2026-11-14T09:00:00Z", id: "newest" });
  const landed = result({
    created_at: "2026-11-15T09:00:00Z",
    id: "landed",
    observed: "42 passed",
  });
  const before = check({ block_id: "c-one", results: [earlier, newest] });
  const after = check({ block_id: "c-one", results: [earlier, newest, landed] });
  // One client for both reads, as the page keeps one while its reads answer again.
  const client = testClient();
  const drawn = (block: CheckBlock) =>
    bodyOf([statementNode("r1", "The page draws it.", [checkNode(block)])], [block], {}, client);
  const view = render(drawn(before));
  try {
    /** The check's row as the reader sees it: whether its facts and its earlier results are
     *  open, and the results it draws, by their ids. */
    const standing = () => {
      const row = view.container.querySelector("[data-verification-check]");
      return {
        earlier: row?.querySelector("[data-verification-earlier]")?.getAttribute("aria-expanded"),
        opened: row?.getAttribute("aria-expanded"),
        results: [...(row?.querySelectorAll("[data-verification-result]") ?? [])].map((one) =>
          one.getAttribute("data-node-id")
        ),
      };
    };
    await user.click(view.container.querySelector("[data-verification-earlier]") as Element);
    const words = view.container.querySelector<HTMLElement>("[data-verification-check-words]");
    act(() => words?.focus());
    expect(document.activeElement).toBe(words);
    expect(standing()).toEqual({
      earlier: "true",
      opened: "true",
      results: ["newest", "earlier"],
    });

    view.rerender(drawn(after));
    // The same element of the check's words, still in the page, still holds the focus.
    expect(view.container.querySelector("[data-verification-check-words]")).toBe(words);
    expect(words?.isConnected).toBe(true);
    expect(document.activeElement).toBe(words);
    expect(standing()).toEqual({
      earlier: "true",
      opened: "true",
      results: ["landed", "newest", "earlier"],
    });
  } finally {
    view.unmount();
  }
});

// R17, R6: the seven states a check can be in are named once over the table, each by its word
// and its shape with how many checks stand in it, so no shape of the table goes unnamed.
test("the summary names all seven states a check can be in, with how many checks stand in each", () => {
  const met = passing("c-one");
  const failed = failing("c-two");
  const view = drawBody(
    [
      statementNode("r1", "Every check of this one passed.", [checkNode(met)]),
      // One check under two statements is one check.
      statementNode("r2", "One check of this one failed.", [checkNode(failed), checkNode(met)]),
    ],
    [met, failed]
  );
  try {
    expect(
      [...view.container.querySelectorAll("[data-check-legend-state]")].map((one) => [
        one.getAttribute("data-check-legend-state"),
        one.querySelector("svg[data-glyph]")?.getAttribute("data-glyph"),
        (one.textContent ?? "").replace(/\s+/gu, " ").trim(),
      ])
    ).toEqual(
      (Object.keys(NODE_STATE_WORDS.check) as CheckState[]).map((state) => [
        state,
        GLYPH_TOKENS.states.check[state],
        `${state === "passed" || state === "failed" ? 1 : 0}${NODE_STATE_WORDS.check[state]}`,
      ])
    );
  } finally {
    view.unmount();
  }
});

// The chips of the picture, in the words R17 gives the three states: pressing one draws the
// statements in that state alone, pressing it again draws them all.
test("a chip draws the statements in one state alone, and pressing it again draws them all", async () => {
  const user = userEvent.setup();
  const met = passing("c-one");
  const failed = failing("c-two");
  const view = drawBody(
    [
      statementNode("r1", "Every check of this one passed.", [checkNode(met)]),
      statementNode("r2", "One check of this one failed.", [checkNode(failed)]),
    ],
    [met, failed]
  );
  try {
    expect(textsOf(view, "[data-verification-chip]")).toEqual([
      `${VERIFICATION_WORDS.all} 2`,
      `${VERIFICATION_WORDS.states.met} 1`,
      `${VERIFICATION_WORDS.states.not_met} 1`,
      `${VERIFICATION_WORDS.states.unknown} 0`,
    ]);
    const metChip = view.container.querySelector('[data-verification-chip="met"]');
    await user.click(metChip as Element);
    expect(
      [...view.container.querySelectorAll("[data-verification-card]")].map((card) =>
        card.getAttribute("data-statement-state")
      )
    ).toEqual(["met"]);
    expect(metChip?.getAttribute("aria-pressed")).toBe("true");

    await user.click(metChip as Element);
    expect(view.container.querySelectorAll("[data-verification-card]")).toHaveLength(2);
    expect(metChip?.getAttribute("aria-pressed")).toBe("false");
  } finally {
    view.unmount();
  }
});

// Part V's departure 4, the reviewer's finding at the checkpoint: no chip is held back from the
// reader. The keyboard reaches a chip whose state holds no statement, as it reaches every chip,
// and its press draws the page's line for that state rather than a table with nothing in it.
test("a chip whose state holds no statement draws the page's line for it", async () => {
  const user = userEvent.setup();
  const met = passing("c-one");
  const view = drawBody(
    [statementNode("r1", "Every check of this one passed.", [checkNode(met)])],
    [met]
  );
  try {
    const unknown = view.container.querySelector<HTMLButtonElement>(
      '[data-verification-chip="unknown"]'
    );
    expect(textsOf(view, '[data-verification-chip="unknown"]')).toEqual([
      `${VERIFICATION_WORDS.states.unknown} 0`,
    ]);
    expect(view.container.querySelectorAll("[data-verification-chip][disabled]")).toHaveLength(0);
    expect(view.container.querySelectorAll("[data-verification-none]")).toHaveLength(0);

    // From the chip All, Tab reaches Met, then Not met, which holds no statement either, then
    // Unknown; Enter presses it.
    act(() => view.container.querySelector<HTMLElement>('[data-verification-chip="all"]')?.focus());
    await user.tab();
    await user.tab();
    await user.tab();
    expect(document.activeElement).toBe(unknown);
    await user.keyboard("{Enter}");
    expect(unknown?.getAttribute("aria-pressed")).toBe("true");
    expect(textsOf(view, "[data-verification-none]")).toEqual([
      EMPTY_STATE_LINES.verification_none,
    ]);
    expect(view.container.querySelectorAll("[data-verification-card]")).toHaveLength(0);

    // A second press draws every statement again, and the line goes.
    await user.click(unknown as Element);
    expect(unknown?.getAttribute("aria-pressed")).toBe("false");
    expect(view.container.querySelectorAll("[data-verification-card]")).toHaveLength(1);
    expect(view.container.querySelectorAll("[data-verification-none]")).toHaveLength(0);
  } finally {
    view.unmount();
  }
});

// R22: every word the page draws of its own comes from a declared list, and no identifier,
// slug or schema token is drawn where a word belongs.
test("every word the body draws of its own comes from the page's declared lists", () => {
  const seeded = passing("c-one");
  const view = drawBody(
    [
      statementNode("r1", "The page draws it.", [checkNode(seeded)]),
      { ...statementNode("r2", "Nothing serves this one.", []), gaps: ["no_check"] },
    ],
    [seeded]
  );
  try {
    const declared = new Set<string>();
    for (const words of [
      Object.values(VERIFICATION_WORDS).flatMap((word) =>
        typeof word === "string" ? [word] : Object.values(word)
      ),
      Object.values(NODE_STATE_WORDS.check),
      Object.values(NODE_TYPE_NAMES),
      Object.values(GAP_LINES),
      Object.values(CONTROL_LABELS),
      Object.values(NODE_FACT_LABELS),
      Object.values(BLOCK_ATTRIBUTE_LABELS),
      Object.values(CHECK_WHEN_WORDS),
      Object.values(RESULT_VERDICT_WORDS),
      Object.values(RESULT_STANDS_WORDS),
      Object.values(EMPTY_STATE_LINES),
    ]) {
      for (const word of words) {
        if (typeof word !== "string") continue;
        declared.add(word);
        declared.add(sentenceCase(word));
      }
    }
    // The page's own punctuation, which names nothing and says nothing.
    for (const mark of ["·", "—", ",", ""]) declared.add(mark);
    const undeclared = chromeTexts(view).filter(
      (drawn) => !declared.has(drawn.replace(/\s*·\s*$/u, "").trim())
    );
    expect(undeclared).toEqual([]);
  } finally {
    view.unmount();
  }
});

// A body with nothing to draw says so in the page's own line for it, and says nothing about
// the page (R22).
test("an issue with no checks and no statements draws the page's own empty line", () => {
  const view = drawBody([], []);
  try {
    expect(textsOf(view, "[data-verification-body]")).toEqual([EMPTY_STATE_LINES.verification]);
  } finally {
    view.unmount();
  }
});

// The architect's decision 81 and part V's departure 10: a query that matches nothing keeps the
// issue's row, which says how the whole issue stands as it does with no query - the covered
// count, the bar, the legend of the seven states and the chips - and the table says under its
// headings, in the slot's line for no match, that the query left nothing, as it says a chip's
// state holds no statement. An issue with nothing at all draws that one line alone, in the tree,
// with no row (part V's R1).
test("a query that matches nothing keeps the summary of the whole issue and says so under it", () => {
  const met = passing("c-one");
  const failed = failing("c-two");
  const loose = passing("c-loose");
  const under = [
    statementNode("r1", "Every check of this one passed.", [checkNode(met)]),
    statementNode("r2", "One check of this one failed.", [checkNode(failed)]),
    statementNode("r3", "Nothing serves this one.", []),
    checkNode(loose),
  ];
  const checks = [met, failed, loose];
  const unfiltered = drawBody(under, checks);
  const filtered = drawBody(under, checks, {
    query: "zebra quartz",
    whole: verificationView(under),
  });
  try {
    // Nothing of the issue holds the query's words, so no card and no row of a check is drawn.
    expect(
      filtered.container.querySelectorAll(
        "[data-verification-card], [data-verification-check], [data-verification-other]"
      )
    ).toHaveLength(0);
    // The issue's row is still the tree's first row and its one tab stop.
    const issueRow = filtered.container.querySelector('[role="tree"] > [data-verification-issue]');
    expect(issueRow?.getAttribute("tabindex")).toBe("0");
    expect(issueRow?.querySelector("[data-verification-summary]")).not.toBeNull();
    // Its summary, its legend of seven and its chips are the whole issue's, as with no query.
    for (const selector of [
      "[data-verification-covered]",
      "[data-verification-legend]",
      "[data-verification-check-legend]",
      "[data-verification-commit]",
      "[data-verification-chip]",
    ]) {
      expect(textsOf(filtered, selector), selector).toEqual(textsOf(unfiltered, selector));
    }
    const fills = (drawn: RenderResult) =>
      [...drawn.container.querySelectorAll("[data-verification-bar] > i")].map((fill) =>
        fill.getAttribute("style")
      );
    expect(fills(filtered)).toEqual(fills(unfiltered));
    expect(textsOf(filtered, "[data-verification-covered]")).toEqual([
      `2 ${VERIFICATION_WORDS.of} 3 ${VERIFICATION_WORDS.covered}`,
    ]);
    expect(filtered.container.querySelectorAll("[data-check-legend-state]")).toHaveLength(7);
    expect(textsOf(filtered, "[data-verification-chip]")).toEqual([
      `${VERIFICATION_WORDS.all} 3`,
      `${VERIFICATION_WORDS.states.met} 1`,
      `${VERIFICATION_WORDS.states.not_met} 1`,
      `${VERIFICATION_WORDS.states.unknown} 1`,
    ]);
    // The line is drawn once, the innermost element holding it, in the table under its
    // headings.
    const line = EMPTY_STATE_LINES.no_match;
    const lines = [...filtered.container.querySelectorAll("[data-verification-body] *")].filter(
      (element) =>
        element.textContent === line &&
        ![...element.children].some((child) => child.textContent === line)
    );
    expect(lines).toHaveLength(1);
    expect(lines[0]?.closest("[data-verification-table]")).not.toBeNull();
    expect(lines[0]?.previousElementSibling?.hasAttribute("data-verification-head")).toBe(true);
    expect(unfiltered.container.textContent).not.toContain(line);
  } finally {
    filtered.unmount();
    unfiltered.unmount();
  }

  const empty = drawBody([], [], { query: "zebra quartz" });
  try {
    expect(empty.container.querySelectorAll("[data-node-row]")).toHaveLength(0);
    expect(empty.container.querySelectorAll("[data-verification-table]")).toHaveLength(0);
    expect(textsOf(empty, '[data-verification-body] > [role="tree"]')).toEqual([
      EMPTY_STATE_LINES.no_match,
    ]);
  } finally {
    empty.unmount();
  }
});

// The reviewer's finding on pull request #4221 at the check column, and part V's departure 10: a
// query whose words a result holds, and neither its check's nor its statement's, keeps the result
// alone, which the filter lifts under the statement with the check as its parent line
// (`presets.ts`). The body draws that result with its check, read from the issue's reading, under
// the statement and as a row like any other, so what the query kept is on screen. Where it is an
// earlier result of its check, the check's earlier results are drawn open: from the row's first
// drawing, and when the query comes to keep it while the row is already drawn.
test("a query that keeps only a result draws it with its check under its statement", () => {
  const failure = "Timed out waiting for the zebra column.";
  /** Each card the body draws: its statement, and each check under it with its row's id and
   *  the words of each result it draws, in order. */
  const cardsOf = (drawn: RenderResult) =>
    [...drawn.container.querySelectorAll("[data-verification-card]")].map((card) => [
      card.getAttribute("data-node-id"),
      [...card.querySelectorAll("[data-verification-check]")].map((row) => [
        row.getAttribute("data-node-id"),
        row.getAttribute("data-verification-row"),
        [...row.querySelectorAll("[data-verification-result] [data-node-words]")].map(
          (words) => words.textContent
        ),
      ]),
    ]);
  /** The id of each row of the tree, in the order they stand. */
  const rowsOf = (drawn: RenderResult) =>
    [...drawn.container.querySelectorAll("[data-node-row]")].map((row) =>
      row.getAttribute("data-verification-row")
    );

  // The query keeps the check's latest result.
  const latest = check({
    block_id: "c-one",
    results: [result({ check_block_id: "c-one", id: "result-latest", observed: failure })],
  });
  const statement = statementNode("r1", "The page draws it.", [checkNode(latest)]);
  const latestRow = `${statement.id} ${checkNode(latest).id}`;
  const kept = drawBody([statement], [latest], {
    query: "zebra",
    whole: verificationView([statement]),
  });
  try {
    expect(cardsOf(kept)).toEqual([[statement.id, [[checkNode(latest).id, latestRow, [failure]]]]]);
    expect(rowsOf(kept)).toEqual([ISSUE_KEY, statement.id, latestRow]);
    expect(kept.container.querySelectorAll("[data-verification-loose]")).toHaveLength(0);
    expect(kept.container.querySelectorAll("[data-verification-none]")).toHaveLength(0);
  } finally {
    kept.unmount();
  }

  // The query keeps an earlier result: the check draws its latest, and its earlier results open.
  const twice = check({
    block_id: "c-two",
    results: [
      result({
        check_block_id: "c-two",
        created_at: "2026-11-13T09:00:00Z",
        id: "result-earlier",
        observed: failure,
      }),
      result({ check_block_id: "c-two", created_at: "2026-11-14T09:00:00Z", id: "result-newest" }),
    ],
  });
  const served = statementNode("r2", "The page draws it again.", [checkNode(twice)]);
  const twiceRow = `${served.id} ${checkNode(twice).id}`;
  const drawnEarlier = [[served.id, [[checkNode(twice).id, twiceRow, ["41 passed", failure]]]]];
  const earlier = drawBody([served], [twice], {
    query: "zebra",
    whole: verificationView([served]),
  });
  try {
    expect(cardsOf(earlier)).toEqual(drawnEarlier);
    expect(
      earlier.container.querySelector("[data-verification-earlier]")?.getAttribute("aria-expanded")
    ).toBe("true");
  } finally {
    earlier.unmount();
  }

  // The reader types the query while the check's row is drawn: the row stays the same element,
  // and the query that comes to keep its earlier result alone opens its earlier results.
  const client = testClient();
  const typing = render(bodyOf([served], [twice], {}, client));
  try {
    const row = typing.container.querySelector("[data-verification-check]");
    const earlierOf = () =>
      row?.querySelector("[data-verification-earlier]")?.getAttribute("aria-expanded");
    expect(earlierOf()).toBe("false");
    expect(cardsOf(typing)).toEqual([
      [served.id, [[checkNode(twice).id, twiceRow, ["41 passed"]]]],
    ]);
    typing.rerender(
      bodyOf([served], [twice], { query: "zebra", whole: verificationView([served]) }, client)
    );
    expect(typing.container.querySelector("[data-verification-check]")).toBe(row);
    expect(earlierOf()).toBe("true");
    expect(cardsOf(typing)).toEqual(drawnEarlier);
  } finally {
    typing.unmount();
  }
});

// R19, R20: a check whose `serves` names no statement is a row of the filter, and so is a node
// an address named, which outranks the filter. The body draws both, so nothing the filter kept
// is lost.
test("a check that serves no statement, and a node an address named, are drawn", () => {
  const loose = passing("c-loose");
  const named = node({ id: "comment-1", text: "A comment an address named.", type: "comment" });
  const view = drawBody(
    [statementNode("r1", "The page draws it.", []), checkNode(loose), named],
    [loose],
    {
      named: [named.id],
    }
  );
  try {
    // The check's own words, and under its state the words its latest result observed.
    expect(textsOf(view, "[data-verification-loose] [data-node-words]")).toEqual([
      "The page draws c-loose.",
      "41 passed",
    ]);
    expect(textsOf(view, "[data-verification-loose] span[data-node-state]")).toEqual([
      NODE_STATE_WORDS.check.passed,
    ]);
    expect(
      [...view.container.querySelectorAll("[data-verification-other]")].map((drawn) => [
        drawn.getAttribute("data-node-type"),
        drawn.querySelector("[data-node-words]")?.textContent,
      ])
    ).toEqual([["comment", named.text]]);
  } finally {
    view.unmount();
  }
});

// Part V's R8 and R1: the view keeps one tree, which holds everything it draws. Its first row
// is the issue's own, holding the summary and the chips, and it is the tree's one tab stop, so
// `t k` leaves the view's top on screen and the next Tab on the chip All. An issue with nothing
// to draw draws no row, and while a read is out the tree holds nothing at all and the body
// nothing but the tree.
test("the view's tree holds the issue's row first, and only it is a tab stop", () => {
  const met = passing("c-one");
  const loose = passing("c-loose");
  const named = node({ id: "comment-1", text: "A comment an address named.", type: "comment" });
  const view = drawBody(
    [
      statementNode("r1", "The page draws it.", [checkNode(met)]),
      statementNode("r2", "Nothing serves this one.", []),
      checkNode(loose),
      named,
    ],
    [met, loose],
    { named: [named.id] }
  );
  try {
    const body = view.container.querySelector("[data-verification-body]");
    const trees = view.container.querySelectorAll('[role="tree"]');
    expect(trees).toHaveLength(1);
    const tree = trees[0];
    expect(body?.childElementCount).toBe(1);
    expect(body?.firstElementChild).toBe(tree);
    expect(tree?.getAttribute("aria-label")).toBe(CONTROL_LABELS.tree);
    expect(tree?.getAttribute("tabindex")).toBe("-1");

    const rows = [...view.container.querySelectorAll('[role="treeitem"]')];
    const issueRow = rows[0];
    expect(tree?.firstElementChild).toBe(issueRow);
    expect(issueRow?.hasAttribute("data-verification-issue")).toBe(true);
    expect(issueRow?.hasAttribute("data-node-row")).toBe(true);
    expect(issueRow?.getAttribute("data-node-id")).toBe(ISSUE_KEY);
    expect(issueRow?.getAttribute("aria-level")).toBe("1");
    expect(issueRow?.getAttribute("tabindex")).toBe("0");
    expect(issueRow?.querySelector("[data-verification-summary]")).not.toBeNull();
    expect(issueRow?.querySelector("[data-verification-chips]")).not.toBeNull();
    // Every other row, in the order the page draws them: each statement and its checks, the
    // check that serves no statement, and the node the address named. None is a tab stop.
    expect(
      rows
        .slice(1)
        .map((row) => [
          row.getAttribute("data-node-type"),
          row.getAttribute("aria-level"),
          row.getAttribute("tabindex"),
          row.hasAttribute("data-node-row"),
        ])
    ).toEqual([
      ["requirement", "1", "-1", true],
      ["check", "2", "-1", true],
      ["requirement", "1", "-1", true],
      ["check", "1", "-1", true],
      ["comment", "1", "-1", true],
    ]);
    // A statement's checks are one group of rows under it, drawn open; a statement no check
    // serves holds no group and says nothing of being open.
    const served = rows[1];
    expect(served?.querySelectorAll('[role="group"]')).toHaveLength(1);
    expect(served?.querySelector('[role="group"] > [role="treeitem"]')).toBe(rows[2] ?? null);
    expect(served?.getAttribute("aria-expanded")).toBe("true");
    expect(rows[3]?.querySelectorAll('[role="group"]')).toHaveLength(0);
    expect(rows[3]?.hasAttribute("aria-expanded")).toBe(false);
    // The outline's own mark is no mark of this view's rows.
    expect(view.container.querySelectorAll("[data-row-key]")).toHaveLength(0);
  } finally {
    view.unmount();
  }

  const empty = drawBody([], []);
  try {
    expect(empty.container.querySelectorAll("[data-node-row]")).toHaveLength(0);
    expect(textsOf(empty, '[data-verification-body] > [role="tree"]')).toEqual([
      EMPTY_STATE_LINES.verification,
    ]);
  } finally {
    empty.unmount();
  }

  const waiting = drawBody([statementNode("r1", "The page draws it.", [checkNode(met)])], [met], {
    reads: { state: "waiting" },
  });
  try {
    const tree = waiting.container.querySelector('[data-verification-body] > [role="tree"]');
    expect(tree).not.toBeNull();
    expect(tree?.childElementCount).toBe(0);
    expect(textsOf(waiting, "[data-verification-body]")).toEqual([""]);
    expect(waiting.container.querySelectorAll("[data-node-row]")).toHaveLength(0);
    expect(waiting.container.querySelector("[data-verification-body]")?.childElementCount).toBe(1);
    expect(waiting.container.querySelectorAll("[data-verification-unreadable]")).toHaveLength(0);
  } finally {
    waiting.unmount();
  }
});

// The architect's decisions 117 and 126, and the reviewer's finding on pull request #4221 at the
// slot's reads: a read refused before it first answers holds nothing to draw from, so the body
// says so over its tree, one line for each such read in the page's words for it with a Retry
// that asks that read alone again. It draws no row, since a statement no blocks read placed its
// check under, or a check no results read says how it stands, would say what is not so, and it
// keeps its tree with nothing in it, as while a read is out (part V's R8). No line stands inside
// the tree, whose own structure an alert would break.
test("a read that fails before it first answers says so with a Retry, for each of the view's three reads", async () => {
  const user = userEvent.setup();
  const met = passing("c-one");
  const statements = [statementNode("r1", "The page draws it.", [checkNode(met)])];
  // The words decision 126 names: the Spec view's two, and the checks for the blocks read.
  const words: Readonly<Record<VerificationRead, string>> = {
    blocks: ANSWER_LINES.checks_unreadable,
    coverage: ANSWER_LINES.statements_unreadable,
    results: ANSWER_LINES.results_unreadable,
  };
  expect([words.coverage, words.results, words.blocks]).toEqual([
    "Could not load the statements.",
    "Could not load the results.",
    "Could not load the checks.",
  ]);
  /** Each failed-read line the body draws, in order: the read it names, its words and the name
   *  of each of its buttons. */
  const linesOf = (drawn: RenderResult) =>
    [...drawn.container.querySelectorAll<HTMLElement>("[data-verification-unreadable]")].map(
      (line) => [
        line.getAttribute("data-verification-unreadable"),
        within(line).getByRole("alert").querySelector("p")?.textContent,
        within(line)
          .getAllByRole("button")
          .map((button) => button.textContent),
      ]
    );
  /** What the body holds beside its lines: how many cards, rows and trees it draws, and its last
   *  element, under every line, which is its one tree: its role, how many children it holds,
   *  and whether a line stands inside it. */
  const treeOf = (drawn: RenderResult) => {
    const last = drawn.container.querySelector("[data-verification-body]")?.lastElementChild;
    return {
      cards: drawn.container.querySelectorAll("[data-verification-card]").length,
      children: last?.childElementCount,
      lineInside: last?.querySelector('[role="alert"]') !== null,
      role: last?.getAttribute("role"),
      rows: drawn.container.querySelectorAll("[data-node-row]").length,
      trees: drawn.container.querySelectorAll('[role="tree"]').length,
    };
  };
  const emptyTree = { cards: 0, children: 0, lineInside: false, role: "tree", rows: 0, trees: 1 };

  for (const read of ["coverage", "results", "blocks"] as const) {
    const retried: VerificationRead[] = [];
    const view = drawBody(statements, [met], {
      reads: { failed: [read], onRetry: (again) => retried.push(again), state: "failed" },
    });
    try {
      expect(linesOf(view)).toEqual([[read, words[read], ["Retry"]]]);
      expect(treeOf(view)).toEqual(emptyTree);
      await user.click(within(view.container).getByRole("button", { name: "Retry" }));
      expect(retried).toEqual([read]);
    } finally {
      view.unmount();
    }
  }

  // All three failed: three lines in the order coverage, results, blocks, whatever order the
  // reads are given in, and each Retry asks its own read and no other.
  const retried: VerificationRead[] = [];
  const all = drawBody(statements, [met], {
    reads: {
      failed: ["blocks", "coverage", "results"],
      onRetry: (again) => retried.push(again),
      state: "failed",
    },
  });
  try {
    expect(linesOf(all)).toEqual([
      ["coverage", words.coverage, ["Retry"]],
      ["results", words.results, ["Retry"]],
      ["blocks", words.blocks, ["Retry"]],
    ]);
    expect(treeOf(all)).toEqual(emptyTree);
    // Each line's own Retry, pressed in another order than the lines stand in.
    const [coverage, results, blocks] = [
      ...all.container.querySelectorAll<HTMLElement>("[data-verification-unreadable]"),
    ].map((line) => within(line).getByRole("button", { name: "Retry" }));
    await user.click(blocks);
    await user.click(coverage);
    await user.click(results);
    expect(retried).toEqual(["blocks", "coverage", "results"]);
  } finally {
    all.unmount();
  }
});

// Part V's R7 and R9: an agent that recorded a result is named by its role's word, as the
// outline's rows name one (`agent-role.ts`), and its session's whole title is drawn nowhere in
// the view, its opened facts included; a person is named by their login, and a result nobody
// recorded by the page's own words for it.
test("an agent that recorded a result is named by its role's word", async () => {
  const user = userEvent.setup();
  const byAgent = check({
    block_id: "c-agent",
    results: [
      result({
        actor: {
          id: "session-9",
          kind: "session",
          origin: { session_title: "Legion tester · INFRA-1" },
        },
        check_block_id: "c-agent",
        id: "by-agent",
      }),
    ],
  });
  const byPerson = check({
    block_id: "c-person",
    results: [
      result({ actor: { id: "alice", kind: "user" }, check_block_id: "c-person", id: "by-person" }),
    ],
  });
  const byNobody = check({
    block_id: "c-nobody",
    results: [result({ actor: undefined, check_block_id: "c-nobody", id: "by-nobody" })],
  });
  const view = drawBody(
    [
      statementNode("r1", "Three checks serve it.", [
        checkNode(byAgent),
        checkNode(byPerson),
        checkNode(byNobody),
      ]),
    ],
    [byAgent, byPerson, byNobody]
  );
  try {
    expect(
      Object.fromEntries(
        [...view.container.querySelectorAll("[data-verification-result]")].map((drawn) => [
          drawn.getAttribute("data-node-id"),
          drawn.querySelector("[data-node-author]")?.textContent,
        ])
      )
    ).toEqual({
      "by-agent": "tester",
      "by-nobody": CONTROL_LABELS.unknown_author,
      "by-person": "alice",
    });
    // With every check's facts open too, nothing of the title is drawn: not the issue it names,
    // nor the middle dot that set that issue apart from the role.
    for (const words of view.container.querySelectorAll("[data-verification-check-words]")) {
      await user.click(words);
    }
    expect(
      [...view.container.querySelectorAll("[data-verification-check]")].map((row) =>
        row.getAttribute("aria-expanded")
      )
    ).toEqual(["true", "true", "true"]);
    expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(6);
    const drawn = view.container.querySelector("[data-verification-body]")?.textContent ?? "";
    expect(drawn).not.toContain("INFRA-1");
    expect(drawn).not.toContain("Legion");
    expect(textsOf(view, "[data-node-author]").filter((name) => name.includes("·"))).toEqual([]);
  } finally {
    view.unmount();
  }
});

// Part V's R5 and R8: a check's words are drawn whole through `RowText` from the results read's
// reading of them (`text_tree`), so a link in them is a link and a reference is named, never
// spelled as its address. The words are no button, so no control holds a link, and a press on
// a link is the link's own: it neither opens nor shuts the check's facts.
test("a check's words draw their links from the results read's tree", async () => {
  const user = userEvent.setup();
  const linked = check({ block_id: "c-one", text: LINKED_WORDS });
  const view = drawBody(
    [statementNode("r1", "The page draws it.", [linkedCheckNode(linked)])],
    [linked]
  );
  try {
    const card = view.container.querySelector("[data-verification-check]");
    const words = view.container.querySelector("[data-verification-check-words]");
    const runbook = await waitFor(() => {
      const found = words?.querySelector<HTMLElement>('a[href="https://example.com/runbook"]');
      expect(found ?? null).not.toBeNull();
      expect(words?.querySelector('a[data-row-reference="dispatch://CORE-2"]')).not.toBeNull();
      return found as HTMLElement;
    });
    expect(view.container.textContent).not.toContain("dispatch://");
    expect(view.container.querySelectorAll("button a")).toHaveLength(0);

    // The page follows no link in this test.
    view.container.addEventListener("click", (event) => event.preventDefault());
    await user.click(runbook);
    act(() => runbook.blur());
    expect(card?.getAttribute("aria-expanded")).toBe("false");
    expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(0);
  } finally {
    view.unmount();
  }
});

// Part V's departure 7 and R22, the tester's F18, and the architect's decisions 46, 66 and 83: a
// check that serves two statements is drawn under each and names the other by its first words,
// read from that statement's own node, which the view already holds: the words of the server's
// reading of its text (`nodeWords`), cut at a word within 32 characters, with no mark the author
// typed and no markdown pass over them, so words that open on "1." keep it. The chip's stored
// excerpt (`name`) is never what it draws. A statement that reads as no words, one whose words
// are a picture alone, is named by nothing, with no comma for it, as is one the view holds no
// statement for; and a check whose only other statement is such a one draws no "Also under" at
// all.
test("a check under two statements names the other by its words with no mark of markdown", () => {
  const twice = passing("c-one");
  const thrice = passing("c-two");
  const pictured = passing("c-three");
  const counted = passing("c-four");
  // Each statement with the coverage read's reading of its words (`text_tree`). The numbered
  // one is stored as a store holds words that open on a number, so its reading keeps the "1."
  // as words; a markdown pass over those words reads them as a list and drops it.
  const read = (statement: TreeNode): TreeNode => ({
    ...statement,
    tree: displayTree(statement.text),
  });
  const plain = read(statementNode("r1", "The page draws it."));
  const marked = read(statementNode("r2", "**Do** `x` now, then the rest of it, in order."));
  const picture = read(statementNode("r3", "![](https://example.com/shot.png)"));
  const numbered = read(statementNode("r4", "1\\. Stand the page up first."));
  // A statement of no card of this view, which a check may still name.
  const stranger = read(statementNode("r9", "A statement this view does not draw."));
  // What the reference read names every target by, which is no name "Also under" draws.
  const excerpt = "A stored excerpt that no line draws.";
  // A check's row under one statement, naming the others it serves as the tree names them.
  const under = (block: CheckBlock, others: readonly TreeNode[]): TreeNode => ({
    ...checkNode(block),
    alsoUnder: others.map((other) => ({
      answered: true,
      gone: false,
      name: excerpt,
      ref: other.reference ?? "",
    })),
  });
  const view = drawBody(
    [
      {
        ...plain,
        children: [
          under(twice, [marked]),
          under(thrice, [picture, marked, stranger]),
          under(pictured, [picture]),
          under(counted, [numbered]),
        ],
      },
      { ...marked, children: [under(twice, [plain]), under(thrice, [plain, picture])] },
      { ...picture, children: [under(thrice, [plain, marked]), under(pictured, [plain])] },
      { ...numbered, children: [under(counted, [plain])] },
    ],
    [twice, thrice, pictured, counted]
  );
  try {
    // The trail of a check's row under a statement, and the words of each target it names.
    const trailOf = (statement: TreeNode, block: CheckBlock) => {
      const trails = view.container.querySelectorAll(
        `[data-verification-card][data-node-id="${statement.id}"] [data-verification-check][data-node-id="${checkNode(block).id}"] [data-node-trail]`
      );
      return [...trails].map((trail) => ({
        names: [...trail.querySelectorAll("[data-node-chip]")].map((chip) =>
          (chip.textContent ?? "").replace(/^, /u, "")
        ),
        text: trail.textContent,
      }));
    };
    const [first] = trailOf(plain, twice);
    const named = first?.names[0] ?? "";
    expect(first?.names).toHaveLength(1);
    expect(named, `"Also under" names r2 by no words: "${named}"`).not.toBe("");
    expect(named, `"Also under" draws a backtick: "${named}"`).not.toContain("`");
    expect(named, `"Also under" draws an asterisk: "${named}"`).not.toContain("*");
    expect(
      named.length,
      `"Also under" names r2 by more than 32 characters: "${named}"`
    ).toBeLessThanOrEqual(32);
    expect(named).toBe("Do x now, then the rest of it…");
    expect(trailOf(plain, twice)).toEqual([
      { names: [named], text: `${CONTROL_LABELS.also_under} ${named}` },
    ]);
    expect(trailOf(marked, twice)).toEqual([
      { names: [plain.text], text: `${CONTROL_LABELS.also_under} ${plain.text}` },
    ]);
    // The picture and the statement the view does not draw name nothing, and no comma stands
    // for either.
    expect(trailOf(plain, thrice)).toEqual([
      { names: [named], text: `${CONTROL_LABELS.also_under} ${named}` },
    ]);
    expect(trailOf(marked, thrice)).toEqual([
      { names: [plain.text], text: `${CONTROL_LABELS.also_under} ${plain.text}` },
    ]);
    expect(trailOf(picture, thrice)).toEqual([
      {
        names: [plain.text, named],
        text: `${CONTROL_LABELS.also_under} ${plain.text}, ${named}`,
      },
    ]);
    // A check whose only other statement is the picture says it is under nothing else.
    expect(trailOf(plain, pictured)).toEqual([]);
    expect(trailOf(picture, pictured)).toEqual([
      { names: [plain.text], text: `${CONTROL_LABELS.also_under} ${plain.text}` },
    ]);
    // Words that open on a number keep it (decision 66's "1. Stand").
    expect(trailOf(plain, counted)).toEqual([
      {
        names: ["1. Stand the page up first."],
        text: `${CONTROL_LABELS.also_under} 1. Stand the page up first.`,
      },
    ]);
    expect(trailOf(numbered, counted)).toEqual([
      { names: [plain.text], text: `${CONTROL_LABELS.also_under} ${plain.text}` },
    ]);
    const drawn = view.container.querySelector("[data-verification-body]")?.textContent ?? "";
    expect(drawn).not.toContain(excerpt);
    // A statement's bare `dispatch://` address is drawn as its short form by F's words functions
    // (decision 95); once that F is under this part, a statement citing an address comes back
    // here with an assertion that its name reads no `dispatch://`.
  } finally {
    view.unmount();
  }
});

/** Lays every element out `height` px tall until the answer is called, as
 *  `NodeRow.drawn.test.tsx` lays a row out: no DOM of a unit test lays anything out, and a
 *  statement's cut reads the words' own boxes (`CutWords`, through `NodeRow.tsx`'s
 *  `measureClamp`). */
function layOut(height: number): () => void {
  const own = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get: () => height,
  });
  const box = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
    new DOMRect(0, 0, 600, height)
  );
  return () => {
    if (own === undefined) Reflect.deleteProperty(HTMLElement.prototype, "scrollHeight");
    else Object.defineProperty(HTMLElement.prototype, "scrollHeight", own);
    box.mockRestore();
  };
}

// Part V's R8: the keyboard reaches each control of the view by Tab: each chip, the "more"
// control of a cut statement, each check's words, each link in a check's words, each link in the
// words of a node an address named, the line of earlier results and, in the opened facts, the
// copy control of a command. This reads that each of them holds tab index 0, so none is out of
// the tab order; the order Tab takes them in is read in the browser, where row 4 of
// `e2e/issue-page-v.e2e.ts` walks it. The view passes `tabStops` to G's `CutWords`, `NodeFacts`
// and `RowText`, so none of them takes its controls out of the tab order. In document order the
// issue's row comes before the chip All, so the Tab after `t k` reaches the first chip.
test("each control of the view is a tab stop", async () => {
  const user = userEvent.setup();
  const linked = check({
    block_id: "c-one",
    results: [
      result({ created_at: "2026-11-13T09:00:00Z", id: "earlier" }),
      result({ created_at: "2026-11-14T09:00:00Z", id: "newest" }),
    ],
    text: LINKED_WORDS,
  });
  const plain = passing("c-two");
  // A comment an address named, whose words hold a link: the view draws it as a row of its own
  // (R20), and the tree's keys reach no link inside a row.
  const noted = "See [the notes](https://example.com/notes) first.";
  const named = node({ id: "comment-1", text: noted, tree: displayTree(noted), type: "comment" });
  // Sixty words over 400 lines of 24 px: six lines are drawn and 59 words are held back, more
  // than the twenty a cut must hide, so the statement is cut and draws its "more" control.
  const long = Array.from({ length: 60 }, (_unused, at) => `word${at + 1}`).join(" ");
  const restore = layOut(9600);
  try {
    const view = drawBody(
      [statementNode("r1", long, [linkedCheckNode(linked), checkNode(plain)]), named],
      [linked, plain],
      { named: [named.id] }
    );
    try {
      // In document order the issue's row, the tree's one tab stop, comes before the chip All.
      expect(
        [...view.container.querySelectorAll("[data-verification-issue], [data-verification-chip]")]
          .slice(0, 2)
          .map((drawn) =>
            drawn.hasAttribute("data-verification-issue")
              ? "issue"
              : drawn.getAttribute("data-verification-chip")
          )
      ).toEqual(["issue", "all"]);

      const chips = [...view.container.querySelectorAll<HTMLElement>("[data-verification-chip]")];
      expect(chips).toHaveLength(4);
      const more = view.container.querySelector<HTMLElement>(
        "[data-verification-card] [data-node-more]"
      );
      expect(more?.textContent?.startsWith(CONTROL_LABELS.read_more)).toBe(true);
      const words = [
        ...view.container.querySelectorAll<HTMLElement>("[data-verification-check-words]"),
      ];
      expect(words).toHaveLength(2);

      // The reader presses the first check's facts open, so they stay drawn wherever the focus
      // goes next.
      await user.click(words[0] as Element);
      act(() => words[0]?.blur());
      expect(
        [...view.container.querySelectorAll("[data-verification-check]")].map((row) =>
          row.getAttribute("aria-expanded")
        )
      ).toEqual(["true", "false"]);
      const links = await waitFor(() => {
        expect(
          words[0]?.querySelector('a[href="https://example.com/runbook"]') ?? null
        ).not.toBeNull();
        expect(words[0]?.querySelector('a[data-row-reference="dispatch://CORE-2"]')).not.toBeNull();
        return [
          ...view.container.querySelectorAll<HTMLElement>("[data-verification-check-words] a"),
        ];
      });
      expect(links).toHaveLength(2);
      const otherLinks = [
        ...view.container.querySelectorAll<HTMLElement>("[data-verification-other] a"),
      ];
      expect(otherLinks.map((link) => link.getAttribute("href"))).toEqual([
        "https://example.com/notes",
      ]);
      const copies = [
        ...view.container.querySelectorAll<HTMLElement>(
          `[aria-label="${CONTROL_LABELS.copy_command}"]`
        ),
      ];
      expect(copies.length).toBeGreaterThan(0);
      const earlier = [
        ...view.container.querySelectorAll<HTMLElement>("[data-verification-earlier]"),
      ];
      expect(earlier).toHaveLength(1);

      // Each control by its name, so a control out of the tab order is named where it fails.
      const controls: [string, HTMLElement | null][] = [
        ...chips.map((chip): [string, HTMLElement] => [
          `chip ${chip.getAttribute("data-verification-chip")}`,
          chip,
        ]),
        ["more", more],
        ...words.map((one, at): [string, HTMLElement] => [`words of check ${at + 1}`, one]),
        ...links.map((link): [string, HTMLElement] => [`link ${link.getAttribute("href")}`, link]),
        ...otherLinks.map((link): [string, HTMLElement] => [
          `link of the named row ${link.getAttribute("href")}`,
          link,
        ]),
        ...copies.map((copy, at): [string, HTMLElement] => [`copy control ${at + 1}`, copy]),
        ...earlier.map((line): [string, HTMLElement] => ["earlier results", line]),
      ];
      expect(controls.map(([name, control]) => [name, control?.tabIndex])).toEqual(
        controls.map(([name]) => [name, 0])
      );
    } finally {
      view.unmount();
    }
  } finally {
    restore();
  }
});

// Part V's R8 and departure 3, and the tester's F21 and F22: the view's tree answers the
// outline's keys by a handler of its own. `j`, `k`, the arrows, Home and End move the focus from
// the row it is on, or from the row that holds the control it is on, and the tab stop moves with
// it; Enter on a row presses its checks' facts, `o` keeps them drawn, and a pointer opens them
// from a check's latest result, from its words and from its statement's words. The tree holds
// back each key it acts on and no other, and a key that goes on with a chord the reader began is
// the page's keymap's, so `t k` from inside the view reaches it.
test("the view's tree answers the outline's keys, and a check's facts open from its words, its result and its statement", async () => {
  const user = userEvent.setup();
  const one = passing("c-one");
  const two = passing("c-two");
  const loose = passing("c-loose");
  const served = statementNode("r1", "The page draws it.", [checkNode(one), checkNode(two)]);
  const unserved = statementNode("r2", "Nothing serves this one.", []);
  const view = drawBody([served, unserved, checkNode(loose)], [one, two, loose]);
  // Each key the view held back, in the order pressed.
  const { held, stop } = holdKeys();
  try {
    const rows = [...view.container.querySelectorAll<HTMLElement>('[role="treeitem"]')];
    // The rows in the order the page draws them: the issue's, each statement followed by its
    // checks, and the check that serves no statement.
    expect(rows.map((row) => row.getAttribute("data-node-id"))).toEqual([
      ISSUE_KEY,
      served.id,
      checkNode(one).id,
      checkNode(two).id,
      unserved.id,
      checkNode(loose).id,
    ]);
    const names = ["issue", "r1", "c-one", "c-two", "r2", "c-loose"];
    /** The row the focus is on, and each row that holds the tree's tab stop, by their names. */
    const standing = () => ({
      focus: names[rows.indexOf(document.activeElement as HTMLElement)],
      stop: rows.flatMap((row, place) =>
        row.getAttribute("tabindex") === "0" ? names[place] : []
      ),
    });
    /** Whether each check's facts are drawn, as its row says. */
    const opened = () =>
      [rows[2], rows[3], rows[5]].map((row) => row?.getAttribute("aria-expanded"));

    act(() => rows[0]?.focus());
    expect(standing()).toEqual({ focus: "issue", stop: ["issue"] });
    await user.keyboard("j");
    expect(standing()).toEqual({ focus: "r1", stop: ["r1"] });
    await user.keyboard("{ArrowDown}");
    expect(standing()).toEqual({ focus: "c-one", stop: ["c-one"] });
    await user.keyboard("k");
    expect(standing()).toEqual({ focus: "r1", stop: ["r1"] });
    await user.keyboard("{ArrowUp}");
    expect(standing()).toEqual({ focus: "issue", stop: ["issue"] });
    await user.keyboard("{End}");
    expect(standing()).toEqual({ focus: "c-loose", stop: ["c-loose"] });
    await user.keyboard("{Home}");
    expect(standing()).toEqual({ focus: "issue", stop: ["issue"] });
    // Right steps into a statement's checks and Left back out to the statement; on any other
    // row neither moves, and neither is held back there.
    await user.keyboard("{ArrowRight}");
    expect(standing()).toEqual({ focus: "issue", stop: ["issue"] });
    await user.keyboard("j{ArrowRight}");
    expect(standing()).toEqual({ focus: "c-one", stop: ["c-one"] });
    await user.keyboard("{ArrowLeft}");
    expect(standing()).toEqual({ focus: "r1", stop: ["r1"] });
    await user.keyboard("{End}{ArrowLeft}");
    expect(standing()).toEqual({ focus: "c-loose", stop: ["c-loose"] });
    await user.keyboard("k{ArrowRight}");
    expect(standing()).toEqual({ focus: "r2", stop: ["r2"] });
    // A key with Ctrl, Alt or Meta held is no key of the tree's.
    await user.keyboard("{Control>}j{/Control}{Alt>}k{/Alt}{Meta>}{Home}{/Meta}");
    expect(standing()).toEqual({ focus: "r2", stop: ["r2"] });
    // A control inside a row moves the reader as the row does: a chip is the issue's, and a
    // check's words are the check's.
    act(() => view.container.querySelector<HTMLElement>('[data-verification-chip="all"]')?.focus());
    await user.keyboard("j");
    expect(standing()).toEqual({ focus: "r1", stop: ["r1"] });
    act(() => rows[2]?.querySelector<HTMLElement>("[data-verification-check-words]")?.focus());
    await user.keyboard("j");
    expect(standing()).toEqual({ focus: "c-two", stop: ["c-two"] });
    expect(held).toEqual([
      "j",
      "ArrowDown",
      "k",
      "ArrowUp",
      "End",
      "Home",
      "j",
      "ArrowRight",
      "ArrowLeft",
      "End",
      "k",
      "j",
      "j",
    ]);
    held.length = 0;

    // On a check's row the focus drew its facts; Enter shuts them and a second Enter opens them,
    // which keeps them drawn once the focus moves on.
    await user.keyboard("k");
    expect(standing().focus).toBe("c-one");
    expect(opened()).toEqual(["true", "false", "false"]);
    await user.keyboard("{Enter}");
    expect(opened()).toEqual(["false", "false", "false"]);
    await user.keyboard("{Enter}");
    expect(opened()).toEqual(["true", "false", "false"]);
    await user.keyboard("j");
    expect(standing().focus).toBe("c-two");
    expect(opened()).toEqual(["true", "true", "false"]);
    // `o` keeps what the focus drew once the focus moves on, and a second `o` lets it go.
    await user.keyboard("oj");
    expect(standing().focus).toBe("r2");
    expect(opened()).toEqual(["true", "true", "false"]);
    await user.keyboard("ko");
    expect(opened()).toEqual(["true", "true", "false"]);
    await user.keyboard("j");
    expect(opened()).toEqual(["true", "false", "false"]);
    // Enter on a statement's row opens the facts of each of its checks, and where all of them
    // are drawn a second Enter shuts them.
    await user.keyboard("{Home}j");
    expect(standing().focus).toBe("r1");
    await user.keyboard("{Enter}");
    expect(opened()).toEqual(["true", "true", "false"]);
    await user.keyboard("{Enter}");
    expect(opened()).toEqual(["false", "false", "false"]);
    expect(view.container.querySelectorAll("[data-node-body]")).toHaveLength(0);
    // Enter and `o` on the issue's row press nothing, and are not held back.
    await user.keyboard("{Home}{Enter}o");
    expect(opened()).toEqual(["false", "false", "false"]);
    expect(held).toEqual([
      "k",
      "Enter",
      "Enter",
      "j",
      "o",
      "j",
      "k",
      "o",
      "j",
      "Home",
      "j",
      "Enter",
      "Enter",
      "Home",
    ]);

    // A pointer's press on the statement's words does what Enter on its row does.
    const words = rows[1]?.querySelector("[data-node-words]");
    expect(words?.textContent).toBe(served.text);
    await user.click(words as Element);
    expect(opened()).toEqual(["true", "true", "false"]);
    await user.click(words as Element);
    expect(opened()).toEqual(["false", "false", "false"]);
    // And a press on a check's latest result or on its words opens its facts, which stay drawn
    // once the focus has gone.
    await user.click(rows[2]?.querySelector("[data-verification-result]") as Element);
    await user.click(rows[3]?.querySelector("[data-verification-check-words]") as Element);
    await user.keyboard("{Home}");
    expect(standing().focus).toBe("issue");
    expect(opened()).toEqual(["true", "true", "false"]);

    // The page's keymap listens on `window`, as `KeymapProvider` binds it. While a chord the
    // reader began is pending, no key is the tree's: `j` moves nothing, and `k` goes on with
    // `t` to the keymap's `t k`.
    let turnedOff = 0;
    const unregister = appKeymap.register("global", [
      {
        id: "verification-off",
        keys: "t k",
        label: "Turn the view off",
        run: () => {
          turnedOff += 1;
        },
      },
    ]);
    const keymap = render(<KeymapProvider>{null}</KeymapProvider>);
    try {
      held.length = 0;
      await user.keyboard("t");
      expect(appKeymap.pending()).toBe("t");
      await user.keyboard("j");
      expect(standing()).toEqual({ focus: "issue", stop: ["issue"] });
      await user.keyboard("tk");
      expect(turnedOff).toBe(1);
      expect(appKeymap.pending()).toBe("");
      expect(standing()).toEqual({ focus: "issue", stop: ["issue"] });
      // The keymap's own hold comes after the document's listener; the tree held back none.
      expect(held).toEqual([]);
    } finally {
      keymap.unmount();
      unregister();
      // A chord a failed step left half typed is let go, so no later test types into it.
      appKeymap.handleKeyDown(new KeyboardEvent("keydown", { key: "Escape" }));
    }
  } finally {
    stop();
    view.unmount();
  }
});

// Part V's R8, and the reviewer's finding on pull request #4221 at the rows' ids: each row of
// the tree carries its own id (`data-verification-row`), which the tree's keys and presses read
// from the row itself, so no id is paired with a row by where it stands among the others. The
// ids are the tree's rows in reading order, the issue's first and each check under a statement
// by that placement of it; and a key that moves onto a check that serves no statement, or onto a
// node an address named, gives the tab stop to that row's own element, where Enter presses that
// check and no other.
test("each row of the view's tree carries its own id, and a key gives the tab stop to the row it moves onto", async () => {
  const user = userEvent.setup();
  const one = passing("c-one");
  const loose = passing("c-loose");
  const served = statementNode("r1", "The page draws it.", [checkNode(one)]);
  const unserved = statementNode("r2", "Nothing serves this one.", []);
  const named = node({ id: "comment-1", text: "A comment an address named.", type: "comment" });
  const view = drawBody([served, unserved, checkNode(loose), named], [one, loose], {
    named: [named.id],
  });
  try {
    const rows = [...view.container.querySelectorAll<HTMLElement>("[data-node-row]")];
    expect(rows.map((row) => row.getAttribute("data-verification-row"))).toEqual([
      ISSUE_KEY,
      served.id,
      `${served.id} ${checkNode(one).id}`,
      unserved.id,
      checkNode(loose).id,
      named.id,
    ]);
    expect(view.container.querySelectorAll('[role="treeitem"]')).toHaveLength(rows.length);
    // The loose check's own element, and the named node's.
    expect(rows[4]?.hasAttribute("data-verification-loose")).toBe(true);
    expect(rows[5]?.hasAttribute("data-verification-other")).toBe(true);
    const names = ["issue", "r1", "c-one", "r2", "c-loose", "comment"];
    /** The row the focus is on, and each row whose own element holds the tree's tab stop, by
     *  their names. */
    const standing = () => ({
      focus: names[rows.indexOf(document.activeElement as HTMLElement)],
      stops: rows.flatMap((row, at) => (row.getAttribute("tabindex") === "0" ? [names[at]] : [])),
    });

    act(() => rows[0]?.focus());
    await user.keyboard("jjj");
    expect(standing()).toEqual({ focus: "r2", stops: ["r2"] });
    await user.keyboard("j");
    expect(standing()).toEqual({ focus: "c-loose", stops: ["c-loose"] });
    await user.keyboard("j");
    expect(standing()).toEqual({ focus: "comment", stops: ["comment"] });
    await user.keyboard("k");
    expect(standing()).toEqual({ focus: "c-loose", stops: ["c-loose"] });
    // On the loose check's row the focus drew its facts; Enter shuts them, on that row alone.
    const opened = () =>
      [...view.container.querySelectorAll("[data-verification-check]")].map((row) =>
        row.getAttribute("aria-expanded")
      );
    expect(opened()).toEqual(["false", "true"]);
    await user.keyboard("{Enter}");
    expect(opened()).toEqual(["false", "false"]);
  } finally {
    view.unmount();
  }
});
