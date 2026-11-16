import { type Browser, expect, type Locator, test } from "@playwright/test";

import { type FakeSession, setLiveSessions } from "./agents";
import {
  answerAsk,
  createAsk,
  createComment,
  createIssue,
  createProject,
  getAsk,
  patchIssue,
  resolveAsk,
} from "./api";
import { documentEditor, indexedBlockAsk, marginCard, setSheet } from "./editor";
import { askCard } from "./inbox-pane";
import { resetDatabase } from "./seed";
import { sessionIds } from "./session-ids";
import { asUser } from "./users";

/**
 * INFRA-341, R1: one ask header, with the same fields, wherever an ask is drawn, and each place
 * keeping its own layout. Each place below draws every state it draws, and each header is swept
 * through its own width, from 220 px to the widest it is drawn at, 4 px at a time, the way a host
 * of that width would draw it: the header reads its own width (`@container`) and nothing else,
 * so a host can only change the header's width. At every step the row asserts five things of
 * what the page draws:
 *
 * - no drawn part of the header overlaps another;
 * - no drawn part crosses the header's own edge, read from the text's own ranges, clipped where
 *   a box clips it, and never from the box around the text: a time spilling out of a `min-w-0`
 *   box that has shrunk to nothing is drawn outside that box, and the box alone says nothing;
 * - the copy control is on the first line, the line of the row's first part;
 * - every field the header draws there is drawn and visible, a name that truncates keeping at
 *   least 12 px of itself;
 * - the row draws the lines its place's layout draws at that width (`Lines`), so a header
 *   stacked into rows where its place draws one line fails, as a header that never wraps does.
 *
 * Each project sweeps at its own size of control: the desktop's 32 px boxes, and the phone's
 * 44 px touch boxes, which every button is below 1280 px (`web/src/styles.css`), a tablet's
 * width included. That is why the phone sweeps past its own width, to 960 px: a header at a
 * tablet's width has the phone's boxes.
 *
 * Each row's failures name the widths they were read at. The controls each row was run against,
 * and the widths each reddened at, are in the INFRA-341 hand-off that brought these rows in.
 */

/** The one session every ask here is asked by, and whose turn an agent's turn is: its title is
 *  the longest name a header draws, the name that has to give up its width. */
const planner: FakeSession = {
  capabilities: ["steer"],
  dir: "/workspaces/planner",
  last_seen: 1_700_000_000_000,
  machine_id: "e2e",
  roles: [],
  session_id: sessionIds.planner,
  title: "Release planner for the ledger mirror",
};

const asker = { actor: { id: sessionIds.planner, kind: "session" as const }, as: "agent" as const };

test.beforeEach(async () => {
  await resetDatabase();
  await setLiveSessions([planner]);
});

/** The width the sweep starts from: the narrowest the Inbox's reading pane draws a card's
 *  header, about 218 px at 1280 with the pane dragged to its 30%. */
const NARROWEST = 220;
/** The width every header is swept to at least: past the widest layout switch (the block's
 *  36rem), and the width of a tablet's widest header. A header its host draws wider is swept to
 *  its own width. */
const WIDEST = 960;
const STEP = 4;

/** How many lines a row draws, by the header's width: each band is `[from, least, most]`, read
 *  from the widest down, and the first whose `from` the width reaches decides. */
type Lines = readonly (readonly [from: number, least: number, most: number])[];

/** What one header draws in its place, for the sweep to hold at every width. */
interface Drawn {
  /** Each field the header draws there, by name, as a selector inside the header. */
  readonly fields: Readonly<Record<string, string>>;
  /** The lines the row draws, where the place's layout decides them; a record's sentence wraps
   *  as text does, so it gives none. */
  readonly lines?: Lines;
  /** The row the copy control and the lines are read in, as a selector inside the header, or
   *  matching the header itself where the header is its own row (a decision block's). */
  readonly row: string;
}

/** The Inbox card's row (`CardAskHeader`): one line from 27.5rem, two below it. */
const CARD_LINES: Lines = [
  [440, 1, 1],
  [0, 2, 2],
];

/** A read-only decision block's header (`BlockAskHeader`): from 36rem one line, with "Answered
 *  by" on it or on the next; below, the urgency, `Decision` and the label - in two lines where
 *  even those do not fit beside the copy control - then who asked, then who answered. */
const ANSWERED_BLOCK_LINES: Lines = [
  [576, 1, 2],
  [0, 3, 4],
];

/** The header's own parts of a card, at each place a card is drawn. */
function cardHeader(options: { anchored: boolean; notched: boolean; turn: boolean }): Drawn {
  return {
    fields: {
      asker: "[data-ask-card-asked] span.truncate[title]",
      copy: "[data-copy-ref] button",
      label: "[data-ask-state-label]",
      "live link": '[data-ask-card-asked] a[aria-label^="Open live conversation"]',
      time: "[data-ask-card-asked] time",
      ...(options.turn ? { turn: '[data-testid^="turn-"]' } : {}),
      ...(options.notched ? { urgency: "[data-ask-urgency-notch]" } : {}),
      ...(options.anchored ? { anchor: ":scope > blockquote" } : {}),
    },
    lines: CARD_LINES,
    row: "[data-ask-card-row]",
  };
}

/** The answered and resolved records' own parts (`RecordAskHeader`). */
function recordHeader(options: { anchored: boolean; notched: boolean }): Drawn {
  return {
    fields: {
      asker: "[data-ask-record-row] [data-agent-name]",
      copy: "[data-copy-ref] button",
      label: "[data-ask-state-label]",
      time: "[data-ask-record-row] time",
      ...(options.notched ? { urgency: "[data-ask-urgency-notch]" } : {}),
      ...(options.anchored ? { anchor: ":scope > blockquote" } : {}),
    },
    row: "[data-ask-record-row]",
  };
}

/** One failure, at the width it was read at. */
interface Failure {
  readonly message: string;
  readonly width: number;
}

/**
 * Sweeps `header` through its own width and returns every failure of the five the file's head
 * names, with the width it was read at. The header's own `style` is put back afterwards.
 */
function sweep(header: Locator, drawn: Drawn): Promise<Failure[]> {
  return header.evaluate(
    (node, { drawn, from, step, widest }) => {
      const root = node as HTMLElement;
      /** The sub-pixel slack two boxes laid side by side meet within. */
      const SLACK = 1;
      /** How much of a truncated name keeps it visible: a letter or two and the ellipsis. */
      const LEAST_TEXT = 12;
      interface Rect {
        bottom: number;
        left: number;
        right: number;
        top: number;
      }
      interface Part {
        readonly element: Element;
        readonly name: string;
        /** Placed against a frame outside the header (the urgency notch over the card's top
         *  edge), so the header's edge is not its edge. */
        readonly placed: boolean;
        readonly rects: readonly Rect[];
      }

      // A `contents` box draws nothing of its own, so whether it is shown is whether its parent
      // is: `checkVisibility` answers false for any element with no box.
      const shown = (element: Element): boolean =>
        getComputedStyle(element).display === "contents"
          ? element.parentElement === null || shown(element.parentElement)
          : element.checkVisibility({ opacityProperty: true, visibilityProperty: true });
      const drawnAtAll = (rect: Rect) =>
        rect.right - rect.left > 0.5 && rect.bottom - rect.top > 0.5;
      // What of `rect` is drawn: every box between it and the header that clips its overflow
      // cuts it to its own edges, as `truncate` does a name.
      const clipped = (rect: Rect, from: Element | null): Rect => {
        const kept = { bottom: rect.bottom, left: rect.left, right: rect.right, top: rect.top };
        for (let at = from; at !== null && at !== root; at = at.parentElement) {
          const style = getComputedStyle(at);
          const box = at.getBoundingClientRect();
          if (style.overflowX !== "visible") {
            kept.left = Math.max(kept.left, box.left);
            kept.right = Math.min(kept.right, box.right);
          }
          if (style.overflowY !== "visible") {
            kept.top = Math.max(kept.top, box.top);
            kept.bottom = Math.min(kept.bottom, box.bottom);
          }
        }
        return kept;
      };
      const textRects = (text: Text): Rect[] => {
        const range = document.createRange();
        range.selectNodeContents(text);
        return [...range.getClientRects()]
          .map((rect) => clipped(rect, text.parentElement))
          .filter(drawnAtAll);
      };
      const boxRects = (element: Element): Rect[] =>
        [...element.getClientRects()]
          .map((rect) => clipped(rect, element.parentElement))
          .filter(drawnAtAll);
      const placed = (element: Element) => {
        for (let at: Element | null = element; at !== null && at !== root; at = at.parentElement) {
          const { position } = getComputedStyle(at);
          if (position === "absolute" || position === "fixed") return true;
        }
        return false;
      };
      // A part drawn as a box rather than as text: a control (whose whole box is where a tap
      // lands), a mark, a pill, and any box that paints a background or a border. A link inside
      // a sentence is text: its box is padded for a tap without moving the line.
      const boxed = (element: Element) => {
        if (element.matches("button, svg, [role='img'], [data-ask-state-label]")) return true;
        const style = getComputedStyle(element);
        if (element.matches("a") && style.display !== "inline") return true;
        const painted = !/^(transparent|rgba\(.*,\s*0\))$/.test(style.backgroundColor);
        const bordered = ["top", "right", "bottom", "left"].some(
          (side) =>
            Number.parseFloat(style.getPropertyValue(`border-${side}-width`)) > 0 &&
            style.getPropertyValue(`border-${side}-style`) !== "none"
        );
        return painted || bordered;
      };
      const say = (element: Element) => {
        const words = (element.getAttribute("aria-label") ?? element.textContent ?? "")
          .trim()
          .replace(/\s+/g, " ");
        return `${element.tagName.toLowerCase()} "${words.length > 28 ? `${words.slice(0, 27)}…` : words}"`;
      };
      // Every drawn part, in the order the page draws them: each text run, and each boxed part
      // with whatever it draws inside it.
      const parts = (): Part[] => {
        const found: Part[] = [];
        const inside = (element: Element, rects: Rect[]) => {
          for (const child of element.childNodes) {
            if (child instanceof Text) {
              if (child.data.trim() !== "") rects.push(...textRects(child));
            } else if (child instanceof Element && shown(child)) {
              if (child.matches("svg, [role='img']")) rects.push(...boxRects(child));
              else inside(child, rects);
            }
          }
        };
        const walk = (element: Element) => {
          for (const child of element.childNodes) {
            if (child instanceof Text) {
              const parent = child.parentElement;
              if (child.data.trim() === "" || parent === null || !shown(parent)) continue;
              const words = child.data.trim();
              found.push({
                element: parent,
                name: JSON.stringify(words.length > 28 ? `${words.slice(0, 27)}…` : words),
                placed: placed(parent),
                rects: textRects(child),
              });
            } else if (child instanceof Element && shown(child)) {
              if (boxed(child)) {
                const rects = boxRects(child);
                inside(child, rects);
                found.push({ element: child, name: say(child), placed: placed(child), rects });
              } else {
                walk(child);
              }
            }
          }
        };
        walk(root);
        return found;
      };
      const overlap = (a: Rect, b: Rect) =>
        Math.min(a.right, b.right) - Math.max(a.left, b.left) > SLACK &&
        Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > SLACK;
      const spanOf = (rects: readonly Rect[]): Rect => ({
        bottom: Math.max(...rects.map((rect) => rect.bottom)),
        left: Math.min(...rects.map((rect) => rect.left)),
        right: Math.max(...rects.map((rect) => rect.right)),
        top: Math.min(...rects.map((rect) => rect.top)),
      });
      // How much of a field is drawn, against how much of it there is: its text, clipped where
      // it is clipped, or its box where it holds no text.
      const extent = (field: Element) => {
        let drawnWidth = 0;
        let whole = 0;
        const texts = document.createTreeWalker(field, NodeFilter.SHOW_TEXT);
        for (let text = texts.nextNode(); text !== null; text = texts.nextNode()) {
          const parent = text.parentElement;
          if (!(text instanceof Text) || text.data.trim() === "" || parent === null) continue;
          if (!shown(parent)) continue;
          const range = document.createRange();
          range.selectNodeContents(text);
          for (const rect of range.getClientRects()) {
            whole += rect.width;
            const kept = clipped(rect, parent);
            if (drawnAtAll(kept)) drawnWidth += kept.right - kept.left;
          }
        }
        if (whole > 0) return { drawn: drawnWidth, whole };
        const box = clipped(field.getBoundingClientRect(), field.parentElement);
        return {
          drawn: drawnAtAll(box) ? box.right - box.left : 0,
          whole: Number.POSITIVE_INFINITY,
        };
      };

      const failures: { message: string; width: number }[] = [];
      const own = root.getBoundingClientRect().width;
      const widths: number[] = [];
      for (let width = from; width < Math.max(widest, own); width += step) widths.push(width);
      widths.push(Math.max(widest, Math.round(own)));
      const style = root.getAttribute("style");
      try {
        for (const width of widths) {
          const fail = (message: string) => failures.push({ message, width });
          root.style.width = `${width}px`;
          root.style.maxWidth = "none";
          root.style.minWidth = "0px";
          root.style.flex = "none";
          const head = root.getBoundingClientRect();
          const all = parts();

          for (const [index, a] of all.entries()) {
            for (const b of all.slice(index + 1)) {
              if (a.element.contains(b.element) || b.element.contains(a.element)) continue;
              if (a.rects.some((x) => b.rects.some((y) => overlap(x, y)))) {
                fail(`${a.name} overlaps ${b.name}`);
              }
            }
            if (a.placed) continue;
            for (const rect of a.rects) {
              if (rect.left < head.left - SLACK) fail(`${a.name} crosses the header's left edge`);
              if (rect.right > head.right + SLACK)
                fail(`${a.name} crosses the header's right edge`);
              if (rect.bottom > head.bottom + SLACK) fail(`${a.name} crosses the header's foot`);
              if (rect.top < head.top - SLACK) fail(`${a.name} crosses the header's top`);
            }
          }

          const row = root.matches(drawn.row) ? root : root.querySelector(drawn.row);
          if (row === null) {
            fail(`no row matches ${drawn.row}`);
          } else {
            const copy = row.querySelector("[data-copy-ref] button");
            const inRow = all.filter(
              (part) =>
                row.contains(part.element) &&
                part.element.closest("[data-copy-ref]") === null &&
                part.rects.length > 0
            );
            const first = inRow[0];
            const copyPart = all.find((part) => part.element === copy);
            if (copy !== null && first !== undefined && copyPart !== undefined) {
              const control = spanOf(copyPart.rects);
              const opening = spanOf(first.rects);
              if (!(control.top < opening.bottom && control.bottom > opening.top)) {
                fail(`the copy control is not on the line of ${first.name}`);
              }
            }
            if (drawn.lines !== undefined) {
              // A part opens a line when it starts below the foot of every part on the line
              // above it.
              let lines = 0;
              let foot = Number.NEGATIVE_INFINITY;
              for (const part of inRow
                .map((each) => spanOf(each.rects))
                .sort((a, b) => a.top - b.top)) {
                if (part.top >= foot - SLACK) {
                  lines += 1;
                  foot = part.bottom;
                } else {
                  foot = Math.max(foot, part.bottom);
                }
              }
              const band = drawn.lines.find(([least]) => width >= least);
              if (band !== undefined && (lines < band[1] || lines > band[2])) {
                fail(
                  `the row draws ${lines} line${lines === 1 ? "" : "s"}, where its place draws ${band[1] === band[2] ? band[1] : `${band[1]} to ${band[2]}`}`
                );
              }
            }
          }

          for (const [name, selector] of Object.entries(drawn.fields)) {
            const field = root.querySelector(selector);
            if (field === null) {
              fail(`no ${name} is drawn`);
            } else if (!shown(field)) {
              fail(`the ${name} is hidden`);
            } else {
              const { drawn: kept, whole } = extent(field);
              if (kept <= 0 || kept < Math.min(whole, LEAST_TEXT)) {
                fail(`the ${name} is cut to ${Math.round(kept)} px`);
              }
            }
          }
        }
      } finally {
        if (style === null) root.removeAttribute("style");
        else root.setAttribute("style", style);
      }
      return failures;
    },
    { drawn, from: NARROWEST, step: STEP, widest: WIDEST }
  );
}

/** A sweep's failures as the row reports them: each failure once, with the widths it was read
 *  at as runs, so a control's report says where it reddened. */
function byWidth(failures: readonly Failure[]): string[] {
  const widths = new Map<string, number[]>();
  for (const { message, width } of failures) {
    const read = widths.get(message) ?? [];
    if (read.at(-1) !== width) read.push(width);
    widths.set(message, read);
  }
  return [...widths].map(([message, read]) => {
    const runs: string[] = [];
    let start = read[0] ?? 0;
    for (const [index, width] of read.entries()) {
      const next = read[index + 1];
      if (next === undefined || next - width > STEP) {
        runs.push(start === width ? `${width}` : `${start}-${width}`);
        if (next !== undefined) start = next;
      }
    }
    return `${runs.join(", ")} px: ${message}`;
  });
}

/** Sweeps one header and holds it to its place, as one reported check of the row. */
async function holds(header: Locator, drawn: Drawn, what: string): Promise<void> {
  await expect(header).toBeVisible();
  expect.soft(byWidth(await sweep(header, drawn)), what).toEqual([]);
}

/** An open issue holding every state a card and a record draw, each ask anchored in the spec and
 *  asked by the long-titled session: one waiting on the reader, blocking, so its card draws the
 *  urgency notch; one whose turn the reader's question handed back to the session; one the
 *  reader answered, high, so its record draws the notch too; one the reader resolved; and one
 *  the session retracted, whose label is the longest word a resolution draws. */
async function seedAsks() {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "## Specification\n\nThe mirror replays the ledger on a cold start, and the budget holds until the window closes.\n",
    title: "Header sweep",
  });
  const anchored = (quote: string) => ({ artifact: "spec", quote });
  const yours = await createAsk(
    issue.key,
    {
      anchor: anchored("replays the ledger"),
      question: "Does a cold start replay the ledger?",
      urgency: "blocking",
    },
    asker
  );
  const theirs = await createAsk(
    issue.key,
    { anchor: anchored("cold start"), question: "Which mirror keeps the budget?", urgency: "high" },
    asker
  );
  await createComment(issue.key, { ask_id: theirs.id, body: "Which mirror do you mean?" });
  await expect
    .poll(() => getAsk(theirs.id), { intervals: [50, 50, 100, 200, 500] })
    .toMatchObject({ ask: { waiting_on: "agent" } });
  const answered = await createAsk(
    issue.key,
    { anchor: anchored("the budget holds"), question: "Does the budget hold?", urgency: "high" },
    asker
  );
  await answerAsk(answered.id, { expected_edited_at: null, selected: [], text: "It holds." });
  const resolved = await createAsk(
    issue.key,
    { anchor: anchored("the window closes"), question: "Should the window close?" },
    asker
  );
  await resolveAsk(resolved.id, { kind: "resolved", reason: "Decided elsewhere." });
  const retracted = await createAsk(
    issue.key,
    { anchor: anchored("The mirror"), question: "Is the mirror the ledger?" },
    asker
  );
  await resolveAsk(retracted.id, { kind: "retracted", reason: "Superseded." }, asker);
  return { answered, issue, resolved, retracted, theirs, yours };
}

/** Opens a page as alice with its clock held 59 minutes after the seed, so every time a header
 *  draws reads "59 minutes ago", the widest time it draws in its first day. */
async function alicePage(browser: Browser) {
  const alice = await asUser(browser, "alice");
  const page = await alice.newPage();
  await page.clock.setFixedTime(Date.now() + 59 * 60_000);
  return { alice, page };
}

/** The card a record turns into while its answerer changes the answer: the card's header with
 *  the answered state's label, and no turn. */
async function changing(host: Locator): Promise<Locator> {
  await host.getByRole("button", { name: "Change answer" }).click();
  await expect(host.getByRole("button", { name: "Save answer" })).toBeVisible();
  return host.locator("[data-ask-header]").first();
}

// SLOW(>=2s): five asks seeded, then the Inbox and the Answers page each loaded once and three
// headers swept through 186 widths each.
test("the Inbox card's header holds its layout from 220 px to its widest, whoever's turn it is and while an answer changes @phone", async ({
  browser,
}) => {
  const { answered, theirs, yours } = await seedAsks();
  const { alice, page } = await alicePage(browser);
  try {
    await page.goto("/");
    for (const [state, ask, turn] of [
      ["waiting on you", yours, true],
      ["an agent's turn", theirs, true],
    ] as const) {
      const card = await askCard(page, ask.id);
      await holds(
        card.locator("[data-ask-header]").first(),
        cardHeader({ anchored: true, notched: true, turn }),
        `the Inbox card, ${state}`
      );
    }
    await page.goto("/answers");
    await page
      .locator(`[data-answer-row="${answered.id}"]`)
      .first()
      .getByRole("button", { name: "Change answer" })
      .click();
    const card = page.locator(`[data-answer-card="${answered.id}"]`);
    await expect(card.getByRole("button", { name: "Save answer" })).toBeVisible();
    await holds(
      card.locator("[data-ask-header]").first(),
      cardHeader({ anchored: true, notched: true, turn: false }),
      "the Inbox card, answered, while the answer changes"
    );
  } finally {
    await alice.close();
  }
});

// SLOW(>=2s): five asks seeded, the Conversation loaded once, and three headers swept through
// 186 widths each.
test("the compact Inbox card's header holds its layout from 220 px to its widest, whoever's turn it is and while an answer changes @phone", async ({
  browser,
}) => {
  const { answered, issue, theirs, yours } = await seedAsks();
  const { alice, page } = await alicePage(browser);
  try {
    await page.goto(`/issues/${issue.key}/conversation`);
    for (const [state, ask] of [
      ["waiting on you", yours],
      ["an agent's turn", theirs],
    ] as const) {
      await holds(
        page.locator(`[data-turn="ask:${ask.id}"] [data-ask-header]`).first(),
        cardHeader({ anchored: true, notched: true, turn: true }),
        `the compact card, ${state}`
      );
    }
    await holds(
      await changing(page.locator(`[data-turn="ask:${answered.id}"]`)),
      cardHeader({ anchored: true, notched: true, turn: false }),
      "the compact card, answered, while the answer changes"
    );
  } finally {
    await alice.close();
  }
});

// SLOW(>=2s): five asks seeded, the issue page loaded once, and three headers swept through 186
// widths each.
test("the margin card's header holds its layout from 220 px to its widest, whoever's turn it is and while an answer changes @phone", async ({
  browser,
}, testInfo) => {
  const { answered, issue, theirs, yours } = await seedAsks();
  const { alice, page } = await alicePage(browser);
  try {
    await page.goto(`/issues/${issue.key}`);
    await page.getByRole("tab", { name: "Spec" }).click();
    await setSheet(page, testInfo.project.name, true);
    for (const [state, ask] of [
      ["waiting on you", yours],
      ["an agent's turn", theirs],
    ] as const) {
      await holds(
        marginCard(page, ask.id).locator("[data-ask-header]").first(),
        cardHeader({ anchored: true, notched: true, turn: true }),
        `the margin card, ${state}`
      );
    }
    await holds(
      await changing(marginCard(page, answered.id)),
      cardHeader({ anchored: true, notched: true, turn: false }),
      "the margin card, answered, while the answer changes"
    );
  } finally {
    await alice.close();
  }
});

/** A decision block's own header where a card under it draws the provenance: the urgency and
 *  `Decision`, on one line at every width. */
const CARDED_BLOCK: Drawn = {
  fields: { decision: "strong", urgency: "[data-dispatch-ask-pill]" },
  lines: [[0, 1, 1]],
  row: "header",
};

/** A closed issue's answered block, whose own header draws everything. */
const ANSWERED_BLOCK: Drawn = {
  fields: {
    answered: "[data-dispatch-ask-answered]",
    asker: "[data-dispatch-ask-asked] > [title]",
    copy: "[data-copy-ref] button",
    decision: "strong",
    label: "[data-ask-state-label]",
    time: "[data-dispatch-ask-asked] time",
    urgency: "[data-dispatch-ask-pill]",
  },
  lines: ANSWERED_BLOCK_LINES,
  row: "header",
};

const BLOCKS =
  'Intro.\n\n:::ask{#yours urgency="blocking" multiple="false" state="open"}\nWhich way?\n\n- Left\n- Right\n:::\n\n:::ask{#theirs urgency="high" multiple="false" state="open"}\nWhich mirror?\n\n- Left\n- Right\n:::\n\n:::ask{#answered urgency="high" multiple="false" state="open"}\nShip it?\n\n- Ship\n- Hold\n:::\n';

// SLOW(>=2s): two issues of decision blocks seeded and indexed, each issue page loaded once, and
// nine headers swept through 186 widths each.
test("a decision block's headers hold their layout from 220 px to their widest, open, answered and resolved, and on a closed issue @phone", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const open = await createIssue({ project: "CORE", spec: BLOCKS, title: "Open blocks" }, asker);
  const theirs = await indexedBlockAsk(open.key, "theirs");
  await createComment(open.key, { ask_id: theirs.id, body: "Which mirror do you mean?" });
  const answered = await indexedBlockAsk(open.key, "answered");
  await answerAsk(answered.id, { expected_edited_at: null, selected: ["Ship"], text: "Ship it." });
  // A closed issue's answered block draws its own header; its resolved block, whose close
  // resolved its ask, draws the resolution's record under a carded header.
  const closed = await createIssue(
    { project: "CORE", spec: BLOCKS, title: "Closed blocks" },
    asker
  );
  const closedAnswered = await indexedBlockAsk(closed.key, "answered");
  await answerAsk(closedAnswered.id, {
    expected_edited_at: null,
    selected: ["Ship"],
    text: "Ship it.",
  });
  await indexedBlockAsk(closed.key, "yours");
  await patchIssue(closed.key, { status: "done" });
  const { alice, page } = await alicePage(browser);
  try {
    await page.goto(`/issues/${open.key}`);
    for (const [id, state, card] of [
      ["yours", "waiting on you", cardHeader({ anchored: false, notched: false, turn: true })],
      ["theirs", "an agent's turn", cardHeader({ anchored: false, notched: false, turn: true })],
      ["answered", "answered", recordHeader({ anchored: false, notched: false })],
    ] as const) {
      const block = documentEditor(page).locator(`[data-dispatch-ask-block="${id}"]`);
      await expect(block.locator("article[data-testid^=ask-]")).toBeVisible();
      await holds(
        block.locator("header[data-ask-header]"),
        CARDED_BLOCK,
        `the block's header, ${state}`
      );
      await holds(
        block.locator("article [data-ask-header]").first(),
        card,
        `the block's ${state === "answered" ? "record" : "card"}, ${state}`
      );
    }

    await page.goto(`/issues/${closed.key}`);
    const answeredBlock = documentEditor(page).locator('[data-dispatch-ask-block="answered"]');
    await expect(answeredBlock.locator("[data-dispatch-ask-answered]")).toBeVisible();
    await holds(
      answeredBlock.locator("header[data-ask-header]"),
      ANSWERED_BLOCK,
      "a closed issue's answered block"
    );
    const resolvedBlock = documentEditor(page).locator('[data-dispatch-ask-block="yours"]');
    await expect(resolvedBlock.getByTestId("ask-resolution-badge")).toBeVisible();
    await holds(
      resolvedBlock.locator("header[data-ask-header]"),
      CARDED_BLOCK,
      "a closed issue's resolved block's header"
    );
    await holds(
      resolvedBlock.locator("article [data-ask-header]").first(),
      recordHeader({ anchored: false, notched: false }),
      "a closed issue's resolved block's record"
    );
  } finally {
    await alice.close();
  }
});

// SLOW(>=2s): five asks seeded, the Conversation loaded once, and three records swept through
// 186 widths each.
test("the answered and resolved records' headers hold their layout from 220 px to their widest @phone", async ({
  browser,
}) => {
  const { answered, issue, resolved, retracted } = await seedAsks();
  const { alice, page } = await alicePage(browser);
  try {
    await page.goto(`/issues/${issue.key}/conversation`);
    for (const [state, ask, notched] of [
      ["answered", answered, true],
      ["resolved", resolved, false],
      ["retracted", retracted, false],
    ] as const) {
      const turn = page.locator(`[data-turn="ask:${ask.id}"]`);
      await expect(turn.locator("[data-ask-state-label]")).toBeVisible();
      await holds(
        turn.locator("[data-ask-header]").first(),
        recordHeader({ anchored: true, notched }),
        `the ${state} record`
      );
    }
  } finally {
    await alice.close();
  }
});
