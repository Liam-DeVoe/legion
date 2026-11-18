import { readFileSync } from "node:fs";

import { type Browser, devices, expect, type Locator, test } from "@playwright/test";

import { type FakeSession, setLiveSessions } from "./agents";
import {
  answerAsk,
  createAsk,
  createComment,
  createIssue,
  createProject,
  declineAsk,
  getAsk,
  patchIssue,
  resolveAsk,
} from "./api";
import { documentEditor, indexedBlockAsk, marginCard, setSheet } from "./editor";
import { askCard, inboxPane } from "./inbox-pane";
import { replyRowId } from "./inbox-threads";
import { resetDatabase } from "./seed";
import { sessionIds } from "./session-ids";
import { asUser } from "./users";

/**
 * INFRA-341, R1: one ask header, with the same fields, wherever an ask is drawn, and each place
 * keeping its own layout. Each place below draws every state it draws, and each header is swept
 * through its own width, from 216 px to the widest it is drawn at, 4 px at a time, the way a host
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
 * The rows after the sweeps read headers at the one width a reader meets each at, through the
 * same reader (`readHeader`) a sweep reads each of its widths with: the Inbox card at 1280 px,
 * the margin card beside a 1280 px document, the Inbox card on an iPhone 13, a closed issue's
 * answered block at 1280, 1024, 768 and 390 px, and the same block in the Inbox's narrowest
 * reading pane. Each holds the five assertions there, and what that width draws: the layout the
 * row's width picks, the line each part stands on, and the block's height against `main`'s.
 *
 * Each row's failures name the widths they were read at. Three controls were run against this
 * file, each from a build of its own: round 8's copy control wrapping as one more item of its
 * line, round 9's stacked layout at every width, and round 10's one line that never wraps at
 * every width. Each control's diff, and every row it reddened with the widths and the failure
 * messages, are recorded in dispatch://INFRA-341/artifact/r16-header-controls-txt.
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

/** The sheet the three layout widths are named in, as container sizes of its `@theme`. */
const STYLESHEET = readFileSync(new URL("../web/src/styles.css", import.meta.url), "utf8");

/** The width `styles.css`'s `@theme` names `--container-<name>`, in px of the root's 16 px rem:
 *  the one place each layout width is written, so these rows read the width the classes do. */
function containerWidth(name: string): number {
  const found = new RegExp(`--container-${name}:\\s*([\\d.]+)rem;`).exec(STYLESHEET);
  if (found?.[1] === undefined) {
    throw new Error(`web/src/styles.css's @theme names no --container-${name} in rem`);
  }
  return Number(found[1]) * 16;
}

/** From this width a read-only decision block's header is one line, below it rows
 *  (`--container-ask-block-line`, 36rem). */
const ASK_BLOCK_LINE = containerWidth("ask-block-line");
/** From this width an ask card's row is one line (`--container-ask-card-line`, 27.5rem). */
const ASK_CARD_LINE = containerWidth("ask-card-line");
/** From this width to `ASK_CARD_LINE` an ask card's row is two lines beside its copy control,
 *  and below it the phone's layout (`--container-ask-card-two-lines`, 19.5rem). */
const ASK_CARD_TWO_LINES = containerWidth("ask-card-two-lines");

/** The width the sweep starts from: just under the narrowest header a host draws, which is the
 *  Inbox's reading pane at 1280 px dragged to its 30% - about 218 px of a card's header there,
 *  and 217 px of a decision block's, where round 10's fault was. */
const NARROWEST = 216;
/** The width every header is swept to at least: past the widest layout switch (the block's
 *  `ASK_BLOCK_LINE`), and the width of a tablet's widest header. A header its host draws wider
 *  is swept to its own width. */
const WIDEST = 960;
const STEP = 4;

/** How many lines a row draws, by the header's width: each band is `[from, least, most]`, read
 *  from the widest down, and the first whose `from` the width reaches decides. */
type Lines = readonly (readonly [from: number, least: number, most: number])[];

/** What one header draws in its place, for the reader to hold at every width. */
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

/** The Inbox card's row (`CardAskHeader`): one line from `ASK_CARD_LINE`, two below it. */
const CARD_LINES: Lines = [
  [ASK_CARD_LINE, 1, 1],
  [0, 2, 2],
];

/** A read-only decision block's header (`BlockAskHeader`): from `ASK_BLOCK_LINE` one line, with
 *  "Answered by" on it or on the next; below, the urgency, `Decision` and the label - in two
 *  lines where even those do not fit beside the copy control - then who asked, then who
 *  answered. */
const ANSWERED_BLOCK_LINES: Lines = [
  [ASK_BLOCK_LINE, 1, 2],
  [0, 3, 4],
];

/** The header's own parts of a card, at each place a card is drawn. */
function cardHeader(options: { anchored: boolean; notched: boolean; turn: boolean }): Drawn {
  return {
    fields: {
      asked: "[data-ask-card-asked]",
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

/** A box the page draws, or the span of what a part draws, in the page's px. */
interface Rect {
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  readonly top: number;
}

/** What one field of a header draws at one width. */
interface FieldReading {
  /** How much of the field's text is drawn, cut where a box clips it; a field that holds no
   *  text is its box, and `whole` is then infinite. */
  readonly drawn: number;
  /** The lines of the row the field stands on, counted from 0, the row's first. */
  readonly lines: readonly number[];
  /** The span of everything the field draws, or null where it draws nothing. */
  readonly span: Rect | null;
  /** How wide the field's text is, whole. */
  readonly whole: number;
}

/** What a header draws at one width: the reading every assertion of this file is made on. */
interface Reading {
  /** Each failure of the five the file's head names. */
  readonly failures: readonly string[];
  /** Each field of `Drawn.fields` the header draws, by name. */
  readonly fields: Readonly<Record<string, FieldReading>>;
  /** The header's own box. */
  readonly head: Rect;
  /** How many lines the row draws. */
  readonly lines: number;
  /** The row's own box, which is the `@container` a card's layout reads, or null where no row
   *  matches `Drawn.row`. */
  readonly row: Rect | null;
  /** The header's width the reading was taken at, in whole px. */
  readonly width: number;
}

/** The widths a sweep reads a header at: from `from` up by `step`, to `widest` or the header's
 *  own width, whichever is wider. */
interface SweepWidths {
  readonly from: number;
  readonly step: number;
  readonly widest: number;
}

/**
 * The one reader of a header's geometry, run in the page: `measure` reads what the header draws
 * at the width it has, and the sweep is `measure` at each of `widths`, with the header's own
 * `style` put back afterwards. With no `widths` it is one reading at the width the header's host
 * draws it.
 */
function readHeader(
  node: Element,
  { drawn, widths }: { drawn: Drawn; widths: SweepWidths | null }
): Reading[] {
  const root = node as HTMLElement;
  /** The sub-pixel slack two boxes laid side by side meet within. */
  const SLACK = 1;
  /** How much of a truncated name keeps it visible: a letter or two and the ellipsis. */
  const LEAST_TEXT = 12;
  interface Box {
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
    readonly rects: readonly Box[];
  }

  const boxOf = ({ bottom, left, right, top }: Box): Box => ({ bottom, left, right, top });
  // A `contents` box draws nothing of its own, so whether it is shown is whether its parent
  // is: `checkVisibility` answers false for any element with no box.
  const shown = (element: Element): boolean =>
    getComputedStyle(element).display === "contents"
      ? element.parentElement === null || shown(element.parentElement)
      : element.checkVisibility({ opacityProperty: true, visibilityProperty: true });
  const drawnAtAll = (rect: Box) => rect.right - rect.left > 0.5 && rect.bottom - rect.top > 0.5;
  // What of `rect` is drawn: every box between it and the header that clips its overflow
  // cuts it to its own edges, as `truncate` does a name.
  const clippedBox = (rect: Box, from: Element | null): Box => {
    const kept = boxOf(rect);
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
  const textRects = (text: Text): Box[] => {
    const range = document.createRange();
    range.selectNodeContents(text);
    return [...range.getClientRects()]
      .map((rect) => clippedBox(rect, text.parentElement))
      .filter(drawnAtAll);
  };
  const boxRects = (element: Element): Box[] =>
    [...element.getClientRects()]
      .map((rect) => clippedBox(rect, element.parentElement))
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
    // Run in the page by `evaluate`, this is the DOM's own read of the label it names.
    const words = (element.getAttribute("aria-label") ?? element.textContent ?? "")
      .trim()
      .replace(/\s+/g, " ");
    return `${element.tagName.toLowerCase()} "${words.length > 28 ? `${words.slice(0, 27)}…` : words}"`;
  };
  // What a boxed part draws inside its box: its text and its marks.
  const inside = (element: Element, rects: Box[]) => {
    for (const child of element.childNodes) {
      if (child instanceof Text) {
        if (child.data.trim() !== "") rects.push(...textRects(child));
      } else if (child instanceof Element && shown(child)) {
        if (child.matches("svg, [role='img']")) rects.push(...boxRects(child));
        else inside(child, rects);
      }
    }
  };
  // Every drawn part under `element`, in the order the page draws them: each text run, and
  // each boxed part with whatever it draws inside it.
  const parts = (element: Element, found: Part[] = []): Part[] => {
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
          parts(child, found);
        }
      }
    }
    return found;
  };
  // Everything `field` draws: its box where it is a boxed part, and every part inside it.
  const drawnIn = (field: Element): Box[] => {
    if (boxed(field)) {
      const rects = boxRects(field);
      inside(field, rects);
      return rects;
    }
    return parts(field).flatMap((part) => part.rects);
  };
  const overlap = (a: Box, b: Box) =>
    Math.min(a.right, b.right) - Math.max(a.left, b.left) > SLACK &&
    Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > SLACK;
  // Whether two spans stand on one line: each starts above where the other ends, by more than
  // the slack two lines meet within.
  const sameLine = (a: Box, b: Box) =>
    Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > SLACK;
  const spanOf = (rects: readonly Box[]): Box => ({
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
        const kept = clippedBox(rect, parent);
        if (drawnAtAll(kept)) drawnWidth += kept.right - kept.left;
      }
    }
    if (whole > 0) return { drawn: drawnWidth, whole };
    const box = clippedBox(field.getBoundingClientRect(), field.parentElement);
    return {
      drawn: drawnAtAll(box) ? box.right - box.left : 0,
      whole: Number.POSITIVE_INFINITY,
    };
  };

  // One reading of the header: at `width` where one is given, else at the width its host
  // draws it.
  const measure = (width: number | null): Reading => {
    if (width !== null) {
      root.style.width = `${width}px`;
      root.style.maxWidth = "none";
      root.style.minWidth = "0px";
      root.style.flex = "none";
    }
    const failures: string[] = [];
    const fail = (message: string) => {
      failures.push(message);
    };
    const head = boxOf(root.getBoundingClientRect());
    const all = parts(root);

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
        if (rect.right > head.right + SLACK) fail(`${a.name} crosses the header's right edge`);
        if (rect.bottom > head.bottom + SLACK) fail(`${a.name} crosses the header's foot`);
        if (rect.top < head.top - SLACK) fail(`${a.name} crosses the header's top`);
      }
    }

    const row = root.matches(drawn.row) ? root : root.querySelector(drawn.row);
    // The row's lines, each the span of the parts on it: a part opens a line when it starts
    // below the foot of every part on the line above it.
    const lines: Box[] = [];
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
        if (!sameLine(spanOf(copyPart.rects), spanOf(first.rects))) {
          fail(`the copy control is not on the line of ${first.name}`);
        }
      }
      for (const span of inRow.map((part) => spanOf(part.rects)).sort((a, b) => a.top - b.top)) {
        const line = lines.at(-1);
        if (line === undefined || span.top >= line.bottom - SLACK) {
          lines.push(span);
        } else {
          line.bottom = Math.max(line.bottom, span.bottom);
          line.left = Math.min(line.left, span.left);
          line.right = Math.max(line.right, span.right);
        }
      }
      const at = width ?? head.right - head.left;
      const band = drawn.lines?.find(([least]) => at >= least);
      if (band !== undefined && (lines.length < band[1] || lines.length > band[2])) {
        fail(
          `the row draws ${lines.length} line${lines.length === 1 ? "" : "s"}, where its place draws ${band[1] === band[2] ? band[1] : `${band[1]} to ${band[2]}`}`
        );
      }
    }

    const fields: Record<string, FieldReading> = {};
    for (const [name, selector] of Object.entries(drawn.fields)) {
      const field = root.querySelector(selector);
      if (field === null) {
        fail(`no ${name} is drawn`);
        continue;
      }
      if (!shown(field)) {
        fail(`the ${name} is hidden`);
        continue;
      }
      const { drawn: kept, whole } = extent(field);
      if (kept <= 0 || kept < Math.min(whole, LEAST_TEXT)) {
        fail(`the ${name} is cut to ${Math.round(kept)} px`);
      }
      const rects = drawnIn(field);
      const span = rects.length === 0 ? null : spanOf(rects);
      fields[name] = {
        drawn: kept,
        lines:
          span === null
            ? []
            : lines.flatMap((line, index) => (sameLine(span, line) ? [index] : [])),
        span,
        whole,
      };
    }

    return {
      failures,
      fields,
      head,
      lines: lines.length,
      row: row === null ? null : boxOf(row.getBoundingClientRect()),
      width: width ?? Math.round(head.right - head.left),
    };
  };

  if (widths === null) return [measure(null)];
  const own = root.getBoundingClientRect().width;
  const swept: number[] = [];
  for (let width = widths.from; width < Math.max(widths.widest, own); width += widths.step) {
    swept.push(width);
  }
  swept.push(Math.max(widths.widest, Math.round(own)));
  // Run in the page by `evaluate`: the root's own style, kept so the sweep can put it back.
  const style = root.getAttribute("style");
  try {
    return swept.map((width) => measure(width));
  } finally {
    if (style === null) root.removeAttribute("style");
    else root.setAttribute("style", style);
  }
}

/** Reads `header` at the width its host draws it: the one reading of a host-width row. */
async function readAtHostWidth(header: Locator, drawn: Drawn): Promise<Reading> {
  // With no widths `readHeader` returns the one reading at the host's width, so this is never
  // undefined and the check below only narrows the type.
  const [reading] = await header.evaluate(readHeader, { drawn, widths: null });
  if (reading === undefined) throw new Error("the header was read at no width");
  return reading;
}

/** One failure, at the width it was read at. */
interface Failure {
  readonly message: string;
  readonly width: number;
}

/** Sweeps `header` through its own width and returns every failure of the five the file's head
 *  names, with the width it was read at. */
async function sweep(header: Locator, drawn: Drawn): Promise<Failure[]> {
  const readings = await header.evaluate(readHeader, {
    drawn,
    widths: { from: NARROWEST, step: STEP, widest: WIDEST },
  });
  return readings.flatMap(({ failures, width }) => failures.map((message) => ({ message, width })));
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
 *  reader answered, high, so its record draws the notch too; one the reader resolved; one the
 *  session retracted, whose label is the longest word a resolution draws; and one the reader
 *  declined to answer, through the route the card's **Decline to answer** writes to. */
async function seedAsks() {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "## Specification\n\nThe mirror replays the ledger on a cold start, and the budget holds until the window closes. The owner signs the release.\n",
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
  const declined = await createAsk(
    issue.key,
    { anchor: anchored("The owner signs"), question: "Who signs the release?" },
    asker
  );
  await declineAsk(declined.id, { note: "Ask the release owner." });
  return { answered, declined, issue, resolved, retracted, theirs, yours };
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

// SLOW(>=2s): six asks seeded, then the Inbox and the Answers page each loaded once and three
// headers swept through 187 widths each.
test("the Inbox card's header holds its layout from 216 px to its widest, whoever's turn it is and while an answer changes @phone", async ({
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

// SLOW(>=2s): six asks seeded, the Conversation loaded once, and three headers swept through
// 187 widths each.
test("the compact Inbox card's header holds its layout from 216 px to its widest, whoever's turn it is and while an answer changes @phone", async ({
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

// SLOW(>=2s): six asks seeded, the issue page loaded once, and three headers swept through 187
// widths each.
test("the margin card's header holds its layout from 216 px to its widest, whoever's turn it is and while an answer changes @phone", async ({
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
    asked: "[data-dispatch-ask-asked]",
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
  'Intro.\n\n:::ask{#yours urgency="blocking" multiple="false" state="open"}\nWhich way?\n\n- Left\n- Right\n:::\n\n:::ask{#theirs urgency="high" multiple="false" state="open"}\nWhich mirror?\n\n- Left\n- Right\n:::\n\n:::ask{#answered urgency="high" multiple="false" state="open"}\nShip it?\n\n- Ship\n- Hold\n:::\n\n:::ask{#declined urgency="high" multiple="false" state="open"}\nWho signs?\n\n- Owner\n- Planner\n:::\n';

// SLOW(>=2s): two issues of decision blocks seeded and indexed, each issue page loaded once, and
// eleven headers swept through 187 widths each.
test("a decision block's headers hold their layout from 216 px to their widest, open, answered, declined and resolved, and on a closed issue @phone", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const open = await createIssue({ project: "CORE", spec: BLOCKS, title: "Open blocks" }, asker);
  const theirs = await indexedBlockAsk(open.key, "theirs");
  await createComment(open.key, { ask_id: theirs.id, body: "Which mirror do you mean?" });
  const answered = await indexedBlockAsk(open.key, "answered");
  await answerAsk(answered.id, { expected_edited_at: null, selected: ["Ship"], text: "Ship it." });
  const declined = await indexedBlockAsk(open.key, "declined");
  await declineAsk(declined.id, { note: "Ask the release owner." });
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
    for (const [id, state, hosted] of [
      ["yours", "waiting on you", "card"],
      ["theirs", "an agent's turn", "card"],
      ["answered", "answered", "record"],
      ["declined", "declined", "record"],
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
        hosted === "card"
          ? cardHeader({ anchored: false, notched: false, turn: true })
          : recordHeader({ anchored: false, notched: false }),
        `the block's ${hosted}, ${state}`
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

// SLOW(>=2s): six asks seeded, the Conversation loaded once, and four records swept through 187
// widths each.
test("the answered, resolved, retracted and declined records' headers hold their layout from 216 px to their widest @phone", async ({
  browser,
}) => {
  const { answered, declined, issue, resolved, retracted } = await seedAsks();
  const { alice, page } = await alicePage(browser);
  try {
    await page.goto(`/issues/${issue.key}/conversation`);
    for (const [state, ask, notched, label] of [
      ["answered", answered, true, "Answered"],
      ["resolved", resolved, false, "Resolved"],
      ["retracted", retracted, false, "Retracted"],
      ["declined", declined, false, "Declined"],
    ] as const) {
      const turn = page.locator(`[data-turn="ask:${ask.id}"]`);
      await expect(turn.locator("[data-ask-state-label]")).toHaveText(label);
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

/** A field a host-width row reads, which the header must draw: a reading without it fails the
 *  row, naming it. */
function drawnField(reading: Reading, name: string): FieldReading & { readonly span: Rect } {
  const field = reading.fields[name];
  if (field?.span == null) {
    throw new Error(`the header draws no ${name}: ${reading.failures.join("; ")}`);
  }
  return { ...field, span: field.span };
}

/** The first and the last line of its row a field stands on. */
function firstLine(field: FieldReading): number {
  return Math.min(...field.lines);
}

function lastLine(field: FieldReading): number {
  return Math.max(...field.lines);
}

/** Where a read-only decision block's header puts its parts at the width its host draws it, read
 *  as every sweep reads them: the lines they stand on, whether who asked shares the label's line
 *  or takes one under it, whether "Answered by" follows the copy control or takes a line under
 *  it, and the header's own height. And what is drawn rather than the boxes around it: a line too
 *  narrow for its parts drew the time past the end of who asked's own box, over the copy
 *  control, and pushed the control past the header's edge. */
async function blockLayout(header: Locator) {
  const reading = await readAtHostWidth(header, ANSWERED_BLOCK);
  const answered = drawnField(reading, "answered");
  const asked = drawnField(reading, "asked");
  const copy = drawnField(reading, "copy");
  const label = drawnField(reading, "label");
  const time = drawnField(reading, "time");
  const { head } = reading;
  return {
    answeredAfterCopy: answered.lines.includes(firstLine(label))
      ? answered.span.left >= copy.span.right
      : firstLine(answered) > Math.max(lastLine(copy), lastLine(label)),
    answeredUnderAsked: firstLine(answered) > lastLine(asked),
    askedOnLabelRow: asked.lines.includes(firstLine(label)),
    copyAtRightEnd: Math.abs(head.right - copy.span.right) <= 1,
    copyInsideHeader: copy.span.left >= head.left - 1 && copy.span.right <= head.right + 1,
    copyOnLabelRow: copy.lines.includes(firstLine(label)),
    failures: reading.failures,
    height: Math.round(head.bottom - head.top),
    rows: reading.lines,
    timeClearOfCopy:
      !time.lines.some((line) => copy.lines.includes(line)) ||
      time.span.right <= copy.span.left + 1,
    timeInsideHeader: time.span.right <= head.right + 1,
  };
}

/** An issue whose spec holds one decision block, asked by the long-titled session and answered,
 *  with the time the server says the answer was given. Each row closes the issue once it has
 *  seeded what it needs on it: a closed issue's answered block draws its own header, since no
 *  card under it carries the provenance. */
async function seedAnsweredBlock() {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue(
    {
      project: "CORE",
      spec: 'Intro.\n\n:::ask{#decide urgency="blocking" multiple="false" state="open"}\nWhich way?\n\n- Left\n- Right\n:::\n',
      title: "Closed block header",
    },
    asker
  );
  const blockAsk = await indexedBlockAsk(issue.key, "decide");
  const answered = await answerAsk(blockAsk.id, {
    expected_edited_at: null,
    selected: ["Left"],
    text: "Left.",
  });
  if (answered.answer === null) throw new Error("the answered ask holds no answer");
  return { answeredAt: answered.answer.at, blockAsk, issue };
}

/** What the block header's two layouts hold wherever they are drawn: none of the five failures,
 *  the time clear of the copy control and both inside the header, the control on the label's
 *  line. */
const BLOCK_HEADER_DRAWN = {
  copyInsideHeader: true,
  copyOnLabelRow: true,
  failures: [],
  timeClearOfCopy: true,
  timeInsideHeader: true,
} as const;

/** From `ASK_BLOCK_LINE` of header: one line, who asked on the label's line, "Answered by" after
 *  the copy control or under the line. */
const BLOCK_HEADER_LINE = {
  ...BLOCK_HEADER_DRAWN,
  answeredAfterCopy: true,
  askedOnLabelRow: true,
} as const;

/** Below `ASK_BLOCK_LINE` of header: rows, the copy control at the end of the label's, who asked
 *  under it and "Answered by" under that. */
const BLOCK_HEADER_ROWS = {
  ...BLOCK_HEADER_DRAWN,
  answeredUnderAsked: true,
  askedOnLabelRow: false,
  copyAtRightEnd: true,
} as const;

/**
 * The read-only block's header at each width this row reads it at, for this seed. The heights
 * and rows at 1280, 1024 and 768 px are `main`'s at c69be928, before the header drew a state
 * label: this seed, its page clock pinned as this row pins it, counted as this row counts rows,
 * on a build of that tree served by its own harness. There it was 80 px in three rows in the
 * 491 px column 1280 px leaves beside the margin (the urgency and `Decision`; who asked, when
 * and the copy control; who answered), 44 px in one row in the 939 px column at 1024, and 68
 * px in two in the 683 px column at 768 - below 1280 px every button is a 44 px box
 * (`web/src/styles.css`), the copy control with them. A label that made the header taller
 * there, or pushed "Answered by" to a row of its own at 1024, fails it. A phone's 305 px header
 * is held to its rows alone: `main` wrapped who asked onto two lines there, 112 px to these
 * rows' 92.
 *
 * The time is the one part of this seed whose width moves, so the row pins it: at 1024 the one
 * row leaves 70 px to spare on `main` and 7 px here, and a time as wide as "5 minutes ago"
 * puts "Answered by" on a row of its own on both. The family the text is drawn in is pinned
 * too: the browser image the suite runs in answers `sans-serif` as DejaVu Sans, as the runner
 * did (`.github/docker/browser.Dockerfile`), and so do the machines these figures were read on.
 */
const BLOCK_HEADER_AT: Readonly<Record<number, Record<string, unknown>>> = {
  390: { ...BLOCK_HEADER_ROWS, rows: 3 },
  768: { ...BLOCK_HEADER_LINE, height: 68, rows: 2 },
  1024: { ...BLOCK_HEADER_LINE, height: 44, rows: 1 },
  1280: { ...BLOCK_HEADER_ROWS, height: 80, rows: 3 },
};

test("a read-only decision block's header is one wrapping row where its line fits and rows of their own where it does not, as tall as main's from 768 to 1280 px @phone", async ({
  browser,
}) => {
  const { answeredAt, issue } = await seedAnsweredBlock();
  await patchIssue(issue.key, { status: "done" });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The page's `Date` is held at the moment the server says the block was answered, its
    // timers left running, so both of the header's times read "just now" however long the row
    // takes, whatever the page's clock says against the server's: the width of each time is
    // part of what the row measures (`BLOCK_HEADER_AT`).
    await page.clock.setFixedTime(answeredAt);
    await page.goto(`/issues/${issue.key}`);
    const header = documentEditor(page).locator(
      '[data-dispatch-ask-block="decide"] header[data-ask-header]'
    );
    await expect(header.locator("[data-dispatch-ask-answered]")).toBeVisible();
    await expect(header.locator("time")).toHaveText(["just now", "just now"]);
    const viewport = page.viewportSize();
    if (viewport === null) throw new Error("the page has no viewport");
    // The header picks its layout by its own width, not the viewport's. A desktop project reads
    // it from 1280 down to a phone's 390 px, a phone project at its own width.
    const widths = viewport.width < 768 ? [viewport.width] : [1280, 1024, 768, 390];
    for (const width of widths) {
      const expected = BLOCK_HEADER_AT[width];
      if (expected === undefined) throw new Error(`no layout is written down for ${width} px`);
      await page.setViewportSize({ height: viewport.height, width });
      await expect
        .poll(() => blockLayout(header), {
          message: `the block header's rows at ${width} px`,
        })
        .toMatchObject(expected);
    }
    // A name the row cuts is still there whole, in the tooltip of the part that truncates.
    await expect(header.locator("[data-dispatch-ask-asked] > [title]")).toHaveAttribute(
      "title",
      new RegExp(`^${planner.title} · `)
    );
  } finally {
    await alice.close();
  }
});

test("a closed issue's answered block in the narrowest reading pane draws its time clear of its copy control, and the control inside its header", async ({
  browser,
}) => {
  const { blockAsk, issue } = await seedAnsweredBlock();
  // The asker's reply under the reader's answer keeps the ask's thread in the reader's Inbox as
  // a Reply row after the close, and the pane opens a decision block's thread in its document.
  await createComment(issue.key, { ask_id: blockAsk.id, body: "Left it is." }, asker);
  await patchIssue(issue.key, { status: "done" });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The pane shows from 1280 px (`READING_PANE_QUERY`), where its narrowest is narrowest.
    await page.setViewportSize({ height: 900, width: 1280 });
    await page.goto("/");
    const row = page.locator(`[data-inbox-row="${replyRowId(blockAsk.id)}"]`);
    await row.locator("time").first().click();
    await expect(row).toHaveAttribute("data-inbox-selected", "true");
    // The reader drags the line between the list and the pane as far right as it goes, where
    // the pane keeps 30% of the two columns (`PaneDivider`): about 266 px.
    const divider = page.getByTestId("inbox-pane-divider");
    await expect(divider).toBeVisible();
    // The wait above drew the divider, and a drawn element has a box.
    const line = await divider.boundingBox();
    if (line === null) throw new Error("the Inbox draws no pane divider");
    const y = line.y + line.height / 2;
    await page.mouse.move(line.x + line.width / 2, y);
    await page.mouse.down();
    await page.mouse.move(1279, y, { steps: 5 });
    await page.mouse.up();
    await expect(divider).toHaveAttribute("aria-valuenow", "30");
    const header = inboxPane(page).locator(
      '[data-dispatch-ask-block="decide"] header[data-ask-header]'
    );
    await expect(header.locator("[data-dispatch-ask-answered]")).toBeVisible();
    await expect
      .poll(() => blockLayout(header), {
        message: "the block header in the narrowest reading pane",
      })
      .toMatchObject(BLOCK_HEADER_ROWS);
  } finally {
    await alice.close();
  }
});

/** The session whose ask the card rows leave waiting on the reader, under a name short enough to
 *  draw whole on a phone's line. */
const shortPlanner: FakeSession = { ...planner, title: "Planner" };

/** The long-titled session whose ask the card rows hand back to it: its name is the turn an
 *  agent holds, the one part of the card's line that gives up width. */
const ledger: FakeSession = {
  ...planner,
  dir: "/workspaces/ledger",
  session_id: sessionIds.tester,
};

/** Two anchored asks on one issue's spec: the short-titled planner's, waiting on the reader, and
 *  the long-titled session's, whose turn the reader's question handed back to it. */
async function seedTurns() {
  await setLiveSessions([shortPlanner, ledger]);
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "## Specification\n\nThe mirror replays the ledger on a cold start.\n",
    title: "Copy control",
  });
  const yours = await createAsk(
    issue.key,
    {
      anchor: { artifact: "spec", quote: "replays the ledger" },
      question: "Does a cold start replay the ledger?",
    },
    asker
  );
  const theirs = await createAsk(
    issue.key,
    {
      anchor: { artifact: "spec", quote: "cold start" },
      question: "Which mirror keeps the budget?",
    },
    { actor: { id: ledger.session_id, kind: "session" }, as: "agent" }
  );
  await createComment(issue.key, { ask_id: theirs.id, body: "Which mirror do you mean?" });
  await expect
    .poll(() => getAsk(theirs.id), { intervals: [50, 50, 100, 200, 500] })
    .toMatchObject({ ask: { waiting_on: "agent" } });
  return {
    issue,
    turns: [
      { ask: yours, asker: shortPlanner, turn: "Waiting on you" },
      { ask: theirs, asker: ledger, turn: `Waiting on ${ledger.title}` },
    ],
  };
}

/** An ask card's own line at the width its host draws it, read as every sweep reads it: the
 *  width of its `@container` row, the one condition that picks its layout (`CardAskHeader`: one
 *  line from `ASK_CARD_LINE`, two lines beside the copy control from `ASK_CARD_TWO_LINES`, the
 *  phone's lines below that), the viewport's width, which no longer picks it and which a row
 *  reads only to say which reader it stands for, whether the copy control shares the label's
 *  line, whether who asked and when opens a line of its own under that one, and whether the
 *  asker's name is drawn whole. The row and who asked are read by their own marks, so a row that
 *  lost either says which. */
async function cardLine(card: Locator, name: string) {
  await expect(card.locator("[data-ask-header] [data-copy-ref] button")).toBeVisible();
  const viewport = card.page().viewportSize();
  if (viewport === null) throw new Error("the page has no viewport");
  const drawn = cardHeader({ anchored: true, notched: false, turn: true });
  const reading = await readAtHostWidth(card.locator("[data-ask-header]").first(), {
    ...drawn,
    fields: { ...drawn.fields, name: `[data-ask-card-asked] [title="${name}"]` },
  });
  const asked = drawnField(reading, "asked");
  const copy = drawnField(reading, "copy");
  const label = drawnField(reading, "label");
  const asker = drawnField(reading, "name");
  const { row } = reading;
  if (row === null) throw new Error(`the card draws no row: ${reading.failures.join("; ")}`);
  return {
    askerOwnLine:
      firstLine(asked) > Math.max(lastLine(label), lastLine(copy)) &&
      Math.abs(asked.span.left - row.left) <= 1,
    askerWhole: asker.drawn >= asker.whole - 0.5,
    copyOnLabelLine: copy.lines.includes(firstLine(label)),
    failures: reading.failures,
    row: row.right - row.left,
    viewport: viewport.width,
  };
}

test("the Inbox card's copy control ends the label's one line, whoever's turn it is", async ({
  browser,
}) => {
  const { turns } = await seedTurns();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto("/");
    for (const { ask, asker: author, turn } of turns) {
      const card = await askCard(page, ask.id);
      await expect(card.getByTestId(`turn-${ask.id}`)).toContainText(turn);
      const line = await cardLine(card, `${author.title} · ${author.session_id}`);
      expect(line.failures, `${turn}: the Inbox card's header at its own width`).toEqual([]);
      // The card's widest layout: a row of `ASK_CARD_LINE` or more, where label, turn, asker and
      // copy control are one line.
      expect(line.row, `${turn}: the Inbox card's row is 27.5rem or wider`).toBeGreaterThanOrEqual(
        ASK_CARD_LINE
      );
      expect(line.copyOnLabelLine, `${turn}: the copy control is on the label's line`).toBe(true);
    }
  } finally {
    await alice.close();
  }
});

test("the margin card's copy control stands beside its two lines, on the label's, whoever's turn it is", async ({
  browser,
}) => {
  const { issue, turns } = await seedTurns();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${issue.key}`);
    await page.getByRole("tab", { name: "Spec" }).click();
    for (const { ask, asker: author, turn } of turns) {
      const card = marginCard(page, ask.id);
      await expect(card.getByTestId(`turn-${ask.id}`)).toContainText(turn);
      const line = await cardLine(card, `${author.title} · ${author.session_id}`);
      expect(line.failures, `${turn}: the margin card's header at its own width`).toEqual([]);
      // The margin: a row from `ASK_CARD_TWO_LINES` to under `ASK_CARD_LINE`, which breaks the
      // label and turn from the asker and stands the copy control beside both lines. The
      // viewport check no longer picks that layout; it says this row reads the margin as a
      // desktop reader does, at `md` or wider.
      expect(line.viewport, `${turn}: the viewport is md or wider`).toBeGreaterThanOrEqual(768);
      expect(line.row, `${turn}: the margin card's row is 19.5rem or wider`).toBeGreaterThanOrEqual(
        ASK_CARD_TWO_LINES
      );
      expect(line.row, `${turn}: the margin card's row is under 27.5rem`).toBeLessThan(
        ASK_CARD_LINE
      );
      expect(line.copyOnLabelLine, `${turn}: the copy control is on the label's line`).toBe(true);
    }
  } finally {
    await alice.close();
  }
});

test("on a phone the Inbox card's copy control ends the label's line and the asker has the next line whole, whoever's turn it is @phone", async ({
  browser,
}) => {
  const { turns } = await seedTurns();
  const alice = await asUser(browser, "alice", devices["iPhone 13"]);
  try {
    const page = await alice.newPage();
    await page.goto("/");
    for (const { ask, asker: author, turn } of turns) {
      const card = await askCard(page, ask.id);
      await expect(card.getByTestId(`turn-${ask.id}`)).toContainText(turn);
      const title = `${author.title} · ${author.session_id}`;
      const line = await cardLine(card, title);
      expect(line.failures, `${turn}: the phone card's header at its own width`).toEqual([]);
      // A phone: a row under `ASK_CARD_TWO_LINES`, where the copy control ends the label's line
      // and the asker has the next line to itself. The viewport check no longer picks that
      // layout; it says this row reads the card as a phone does, under `md`, where the copy
      // control and the live link are 44 px touch boxes.
      expect(line.viewport, `${turn}: the viewport is under md`).toBeLessThan(768);
      expect(line.row, `${turn}: the phone card's row is under 19.5rem`).toBeLessThan(
        ASK_CARD_TWO_LINES
      );
      expect(line.copyOnLabelLine, `${turn}: the copy control is on the label's line`).toBe(true);
      expect(line.askerOwnLine, `${turn}: the asker opens the next line`).toBe(true);
      await expect(card.locator(`[data-ask-header] [title="${title}"]`)).toHaveText(author.title);
      // A name that fits that line is drawn whole. The agent's own title does not fit it at
      // all - 223 px of name beside the live link and the time, which leave it 160 - so it
      // truncates there whatever the copy control does, and its tooltip holds it whole.
      if (author === ledger) continue;
      expect(line.askerWhole, `${turn}: the asker's name is not cut short`).toBe(true);
    }
  } finally {
    await alice.close();
  }
});
