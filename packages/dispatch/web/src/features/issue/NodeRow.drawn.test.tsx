import { beforeAll, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type RenderResult, render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { MemoryRouter } from "react-router-dom";

import { displayTree } from "../../__tests__/display-trees";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import type { TreeNode } from "./issue-tree";
import { NodeRow } from "./NodeRow";
import {
  type AuthorKind,
  authorKind,
  CONTROL_LABELS,
  type GapKind,
  NODE_TYPE_NAMES,
  type NodeType,
} from "./page-words";

/**
 * What one row of the tree draws, read as a reader reads it (dispatch://INFRA-1196 R1, R6, R9,
 * R34, and the architect's ruling 1 on R1).
 *
 * The row is rendered on its own rather than through `IssueTree`, because what these rows are
 * about is the words and the shapes one node draws: the tree's fold, its keys and its placements
 * are read in `IssueTree.test.tsx` and `issue-tree.test.ts`. Where a row is cut is read from the
 * browser's own layout, which no DOM of a unit test lays out, so the cut is read by the browser
 * suite's c-row.
 */

function node(partial: Partial<TreeNode> & { id: string; type: NodeType }): TreeNode {
  return {
    alsoUnder: [],
    children: [],
    chips: [],
    facts: [],
    gaps: [],
    held: 0,
    question: false,
    replaced: false,
    text: partial.id,
    transcluded: false,
    waiting: false,
    ...partial,
  };
}

/** Who made a node, as the tree names them (`AgentText.author`). */
type Author = { kind: AuthorKind; name: string; reader: boolean };

/** Who made a node the model holds no author for, as the tree names one (`AgentText.author`). */
const NOBODY_RECORDED: Author = {
  kind: authorKind(undefined),
  name: CONTROL_LABELS.unknown_author,
  reader: false,
};

/** One row, drawn with its body open, as the focused row draws it, unless `body` is false;
 *  `author` is who made it, as the tree names them (`AgentText.author`); `answerer` the person
 *  whose answer an ask carries; `expanded` and `folded` its fold, for a node that holds any. */
function renderRow(
  drawn: TreeNode,
  {
    answerer,
    author = NOBODY_RECORDED,
    body = true,
    expanded,
    folded,
  }: {
    answerer?: string;
    author?: Author;
    body?: boolean;
    expanded?: boolean;
    folded?: number;
  } = {}
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <NodeRow
          {...(answerer === undefined ? {} : { answerer })}
          author={author}
          body={body}
          context={false}
          {...(expanded === undefined ? {} : { expanded })}
          {...(folded === undefined ? {} : { folded })}
          focused={false}
          isClosed={false}
          level={1}
          node={drawn}
          onFocus={() => {}}
          onPin={() => {}}
          onToggle={() => {}}
          ownAnswer={false}
          pinned={false}
          rowKey="row"
          tabStop={false}
        />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

beforeAll(async () => {
  await warmMarkdownRenderer();
});

// R9: a node whose author the model holds says who made it by a name and a shape, never by
// colour alone and never by an id, and two authors of different kinds never share a shape.
test("an author is drawn by a name and a shape of its own kind", () => {
  // A comment, which a person and an agent both write: a decision's body draws Keep and
  // Change, which are writes, and this row is about the words and the shape alone.
  const drawn = (author: Author) => {
    const view = renderRow(node({ id: "comment-1", type: "comment" }), { author });
    try {
      const shown = view.container.querySelector("[data-node-author]");
      const shape = shown?.querySelector("svg[data-glyph]");
      return {
        chrome: shown?.querySelector("[data-page-chrome]") !== null,
        margin: shown?.closest("[data-node-margin]") !== null,
        name: shape?.getAttribute("aria-label"),
        role: shape?.getAttribute("role"),
        text: shown?.textContent,
        token: shape?.getAttribute("data-glyph"),
      };
    } finally {
      view.unmount();
    }
  };
  // The word is the author's name, and the shape is drawn beside it rather than in place of it,
  // exactly as the type's shape and the state's shape are, at the right of the row (R34). The
  // shape carries its own accessible name: a reader who cannot see it still hears what kind of
  // author it stands for.
  expect(drawn({ kind: "person", name: "alice", reader: false })).toEqual({
    chrome: false,
    margin: true,
    name: "Written by a person",
    role: "img",
    text: "alice",
    token: "author-person",
  });
  expect(drawn({ kind: "agent", name: "Planner", reader: false })).toEqual({
    chrome: false,
    margin: true,
    name: "Written by an agent",
    role: "img",
    text: "Planner",
    token: "author-agent",
  });
  // Where the model holds no author the row says so in the page's own words, which are marked
  // as the page's rather than as somebody's name, with a shape neither other kind draws.
  expect(drawn(NOBODY_RECORDED)).toEqual({
    chrome: true,
    margin: true,
    name: "Written by an author nobody recorded",
    role: "img",
    text: "Author unknown",
    token: "author-unknown",
  });
});

// R34: a typed node draws its kind as one tag, its word and its shape, in the margin at the
// right of its row and never before its words; a turn of the conversation draws no tag. R9: the
// time it was made is drawn after its author in the page's short form.
test("a typed node draws its kind as one tag at the right, and a turn draws none", () => {
  // Noon of a day of the year the test runs in, so its day is the 5th from UTC-11 to UTC+11 and
  // the page's short form draws no year.
  const at = `${new Date().getFullYear()}-11-05T12:00:00Z`;
  const drawn = (type: NodeType) => {
    const view = renderRow(node({ at, id: `${type}-1`, text: "Words of the node.", type }));
    try {
      const tag = view.container.querySelector("[data-node-tag]");
      return {
        afterWords: tag === null || tag.closest("[data-node-margin]") !== null,
        tag: tag?.textContent ?? null,
        token: tag?.querySelector("svg")?.getAttribute("data-glyph") ?? null,
        time: view.container.querySelector("[data-node-time]")?.textContent ?? null,
      };
    } finally {
      view.unmount();
    }
  };
  expect(drawn("requirement")).toEqual({
    afterWords: true,
    tag: NODE_TYPE_NAMES.requirement,
    time: "5 Nov",
    token: "requirement",
  });
  expect(drawn("check")).toMatchObject({ tag: NODE_TYPE_NAMES.check, token: "check" });
  expect(drawn("comment")).toMatchObject({ tag: null, time: "5 Nov" });
  expect(drawn("message")).toMatchObject({ tag: null });
});

// R1, ruling 1: a folded branch is the one place a row draws one line of its node: the first
// line, cut at its first sentence end, and how many nodes it holds. The focus draws its words
// whole.
test("a folded branch draws its first line and its count, and its focus its words", () => {
  const text = "The page draws the whole issue in one reading. Nothing is lost on the way.";
  const folded = node({ held: 3, id: "comment-1", text, type: "comment" });
  const shut = renderRow(folded, { body: false, expanded: false, folded: 3 });
  try {
    expect(shut.container.querySelector("[data-node-first-line]")?.textContent).toBe(
      "The page draws the whole issue in one reading.…"
    );
    expect(shut.container.querySelector("[data-node-count]")?.textContent).toBe(
      `3 ${CONTROL_LABELS.folded_rows}`
    );
    expect(shut.container.querySelector("[data-node-words]")).toBeNull();
    // The bullet is the branch's fold control, named by what pressing it does.
    expect(shut.getByRole("button", { name: CONTROL_LABELS.unfold })).toBeDefined();
  } finally {
    shut.unmount();
  }
  const focused = renderRow(folded, { body: true, expanded: false, folded: 3 });
  try {
    expect(focused.container.querySelector("[data-node-first-line]")).toBeNull();
    expect(focused.container.querySelector("[data-node-words]")?.textContent).toBe(text);
  } finally {
    focused.unmount();
  }
});

// R1, R9, INFRA-1150 R3, ruling 1: an answered ask carries the person's answer in its own row,
// and no row of its own: who answered by a name and a shape, what they chose among the ask's own
// options, and what they wrote as the store holds it, character for character and whole.
test("an answered ask draws the answer in its own row, word for word", () => {
  const text = "The preset row keeps its labels above that width.\n\nBelow it, one control.";
  const asked = node({
    answer: { by: { id: "alice", kind: "user" }, selected: ["1280 px"], text },
    id: "ask-1",
    options: [{ description: "Where the rail fits.", label: "1280 px" }, { label: "1024 px" }],
    state: "answered",
    text: "At which width does the identity block condense?",
    type: "ask",
  });
  const planner: Author = { kind: "agent", name: "Planner", reader: false };
  const view = renderRow(asked, { answerer: "alice", author: planner });
  try {
    const rows = view.container.querySelectorAll("[data-node-row]");
    expect(rows.length).toBe(1);
    const line = view.container.querySelector("[data-node-answer]");
    expect(line?.querySelector("[data-page-chrome]")?.textContent).toBe("Answer");
    expect(line?.querySelector("[data-answer-author]")?.textContent).toBe("alice");
    expect(line?.querySelector("[data-answer-author] svg")?.getAttribute("data-glyph")).toBe(
      "author-person"
    );
    expect(line?.querySelector("[data-answer-text]")?.textContent).toBe(text);
    // The option the person chose is marked in the ask's own list, and the body adds what an
    // option says beside its name.
    expect(
      [...view.container.querySelectorAll("[data-node-option]")].map((one) => [
        one.getAttribute("data-node-option"),
        one.textContent,
      ])
    ).toEqual([
      ["chosen", "●1280 px — Where the rail fits."],
      ["", "○1024 px"],
    ]);
    // The row's own author is still the asker's, and the answerer's name is no row author.
    expect(view.container.querySelector("[data-node-author]")?.textContent).toBe("Planner");
  } finally {
    view.unmount();
  }
});

// R9: a decision the model marks unsure says so in one word on its own row, and a decision it
// does not mark says nothing of the kind.
test("a decision the model marks unsure says so in one word", () => {
  const unsure = (marked: boolean) => {
    const view = renderRow(
      node({ id: "decisions-md#b-d3", type: "decision", ...(marked ? { unsure: true } : {}) })
    );
    try {
      return view.container.querySelector("[data-node-unsure]")?.textContent ?? null;
    } finally {
      view.unmount();
    }
  };
  expect(unsure(true)).toBe("Unsure");
  expect(unsure(false)).toBeNull();
});

/** The few words a row draws for `gap`, and the sentence its body draws under them. */
function drawnGap(drawn: TreeNode): { chip: string | undefined; line: string | undefined } {
  const view = renderRow(drawn);
  try {
    return {
      chip: view.container.querySelector("[data-gap]")?.textContent ?? undefined,
      line: [...view.container.querySelectorAll("[data-node-body] p")]
        .map((paragraph) => paragraph.textContent ?? "")
        .find((text) => text.endsWith(".")),
    };
  } finally {
    view.unmount();
  }
}

// R8: a citation that must name a requirement and names something else is a gap, drawn on the
// node that holds the citation and never as an error.
test("a check whose citation names no statement says so, in words", () => {
  const gaps: GapKind[] = ["serves_not_requirement"];
  expect(drawnGap(node({ gaps, id: "verification-md#b-c1", type: "check" }))).toEqual({
    chip: "Not a statement",
    line: "What this says it serves is not a statement.",
  });
});

// R8: a plan task that names no requirement it serves and no declaration it attempts is work
// no reason stands behind, and the row says that rather than leaving the reader to notice it,
// in words that pass no judgement on the work.
test("a plan task nobody asked for says so, in words", () => {
  const gaps: GapKind[] = ["nobody_asked"];
  expect(drawnGap(node({ gaps, id: "plan-md#b-t9", type: "plan_task" }))).toEqual({
    chip: "No reason given",
    line: "This task names no statement it serves and nothing a person asked for.",
  });
});

/** One row the way the tree draws it: unfocused until a focus lands inside it, and then the
 *  focused row, so a test reads what that focus does to the page. */
function FocusableRow({ drawn }: { drawn: TreeNode }): ReactNode {
  const [focused, setFocused] = useState(false);
  return (
    <NodeRow
      author={NOBODY_RECORDED}
      body
      context={false}
      focused={focused}
      isClosed={false}
      level={1}
      node={drawn}
      onFocus={() => setFocused(true)}
      onPin={() => {}}
      onToggle={() => {}}
      ownAnswer={false}
      pinned={false}
      rowKey="row"
      tabStop={false}
    />
  );
}

// R9 brings the focused row on screen, but a press inside an unfocused row focuses the row
// before the press ends: a scroll then moves what the pointer went down on, and the press ends
// on something else and does nothing. Measured at 1440 px on the Decisions preset, with the
// row's top 24 px under the identity block, the page scrolled 32 px between mousedown and
// mouseup and a reader's Keep was never sent. A key's focus still brings the row on screen.
test("a press inside a row does not scroll the page under it, and a key's focus still does", async () => {
  const user = userEvent.setup();
  const scrollBy = spyOn(window, "scrollBy").mockImplementation(() => {});
  // The row's top sits above the line the identity block leaves clear, so R9 would scroll.
  const box = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
    new DOMRect(0, -24, 400, 300)
  );
  const drawn = node({ id: "comment-1", type: "comment" });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = () =>
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <FocusableRow drawn={drawn} />
        </MemoryRouter>
      </QueryClientProvider>
    );
  const pressed = draw();
  try {
    const control = pressed.getByRole("button", { name: "Keep open" });
    await user.pointer({ keys: "[MouseLeft>]", target: control });
    expect(
      pressed.container.querySelector("[data-node-row]")?.contains(document.activeElement)
    ).toBe(true);
    expect(scrollBy, "the press scrolled the page under the pointer").not.toHaveBeenCalled();
    await user.pointer({ keys: "[/MouseLeft]" });
  } finally {
    pressed.unmount();
  }
  const keyed = draw();
  try {
    const row = keyed.container.querySelector<HTMLElement>("[data-node-row]");
    act(() => row?.focus());
    expect(scrollBy, "a key's focus no longer brings the row on screen").toHaveBeenCalledTimes(1);
  } finally {
    keyed.unmount();
    box.mockRestore();
    scrollBy.mockRestore();
  }
});

/** A stored text holding a heading, bold, a list item and a code span: every one of them a
 *  symbol a reader must never see drawn as written. */
const MARKED = "## Store the notices\n\nA **decided** thing.\n\n- one `code` item";

/** What a reader reads of one drawn row: whether its words hold any symbol the author typed,
 *  and what its words format. */
function drawnWords(view: RenderResult) {
  const words = view.container.querySelector("[data-node-words]");
  return {
    bold: words?.querySelector("strong")?.textContent,
    code: words?.querySelector("code")?.textContent,
    item: words?.querySelector("li")?.textContent,
    symbols: /\*\*|##|`/u.test(view.container.querySelector("[data-node-row]")?.textContent ?? ""),
  };
}

/** What `drawnWords` reads of a row drawn from the reading of `MARKED`. */
const MARKED_DRAWN = {
  bold: "decided",
  code: "code",
  item: "one code item",
  symbols: false,
};

// R20, c-plain-words: a decision's text is stored markdown, and the decisions read sends the
// server's reading of it (`Decision.text_tree`). The row draws that reading, so the reader sees
// a heading, bold words and a list, never the `##`, `**` and `-` the author typed, and the
// browser parses nothing to get there. Folded, its one line holds none of those symbols either.
test("a decision whose text holds bold draws no literal asterisks", () => {
  const decision = node({
    id: "decisions-md#b-d1",
    text: MARKED,
    tree: displayTree(MARKED),
    type: "decision",
  });
  const view = renderRow(decision);
  try {
    expect(drawnWords(view)).toEqual(MARKED_DRAWN);
  } finally {
    view.unmount();
  }
  const folded = renderRow({ ...decision, held: 1 }, { body: false, expanded: false, folded: 1 });
  try {
    expect(folded.container.querySelector("[data-node-first-line]")?.textContent).toBe(
      "Store the notices A decided thing.…"
    );
  } finally {
    folded.unmount();
  }
});

// The same for every other kind whose read sends the server's reading of its text: an ask's
// question (`question_tree`), a comment's and a message's body (`body_tree`).
test("an ask, a comment and a message draw the server's reading of their text", () => {
  for (const [type, id] of [
    ["ask", "ask-1"],
    ["comment", "comment-1"],
    ["message", "message-1"],
  ] as const) {
    const view = renderRow(node({ id, text: MARKED, tree: displayTree(MARKED), type }));
    try {
      expect({ type, ...drawnWords(view) }).toEqual({ type, ...MARKED_DRAWN });
    } finally {
      view.unmount();
    }
  }
});

// R20: a node whose read sends no reading of its text - a requirement, a check, a result, and a
// block of a document named by the batched reference read - draws the text as stored. The page
// parses no stored markdown of its own to draw it otherwise.
test("a node whose read sends no reading of its text draws the text as stored", () => {
  const view = renderRow(node({ id: "spec#b-r1", text: MARKED, type: "requirement" }));
  try {
    const words = view.container.querySelector("[data-node-words]");
    expect(words?.textContent).toContain("A **decided** thing.");
    expect(words?.querySelector("strong")).toBeNull();
  } finally {
    view.unmount();
  }
});

// R9: the tree is one tab stop and nothing inside a row takes one; a key reaches a row's links
// (`useTreeKeymap.ts`'s `rowLinks`). Words drawn from a reading hold real links, and each is
// out of the tab order too.
test("the links a row's words draw are out of the tab order", async () => {
  const text =
    "Read [the notes](https://example.com/notes) and [the plan](https://example.com/plan).";
  const view = renderRow(node({ id: "comment-1", text, tree: displayTree(text), type: "comment" }));
  try {
    await waitFor(() => {
      const links = [...view.container.querySelectorAll<HTMLElement>("[data-node-words] a[href]")];
      expect(links.length).toBe(2);
      expect(links.map((link) => link.tabIndex)).toEqual([-1, -1]);
    });
  } finally {
    view.unmount();
  }
});
