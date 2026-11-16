import { expect, type Locator, type Page, test } from "@playwright/test";

import { createAsk, createIssue, createIssueArtifact, createProject, patchIssue } from "./api";
import { filterRow } from "./issue-page";
import { assertWhole } from "./unclipped";

/**
 * What the header's rows read (dispatch://INFRA-3943, part P of dispatch://INFRA-1196): the
 * header, its properties line, its readings against R3's limits and the typeface, and the one
 * issue they read it on. The rows open the page with part D's `phase` and `gotoIssue`
 * (`e2e/issue-page.ts`) and measure the header to D's `filterRow`.
 *
 * This is part P's file (decision 2 of the parts): no other part's rows read it.
 *
 * - **The header** `header[data-issue-header]` inside `[data-issue-page]`, drawing no box of its
 *   own (`display: contents`), and in it the band that stays from 761 px up,
 *   `[data-identity-block]`, with `data-condensed` on the header once the page has scrolled more
 *   than 40 px (`IssueHeader.tsx`).
 * - **The properties line** `[data-testid="issue-state-actions"]`: the status
 *   (`select[aria-label="Status"]`), the priority and the assignee (each a `select` named
 *   `Priority of <key>` and `Assignee of <key>`, drawn over an `aria-hidden` badge), the
 *   children's progress `[data-testid="issue-progress-children"]` and
 *   `[data-testid="issue-progress-subtree"]`, the count `[data-testid="issue-whose-turn"]`, the
 *   approval control, the route's marker `[data-testid="issue-route-unreachable"]`, the labels
 *   `[data-testid="issue-header-labels"] > [data-header-label]` and their count
 *   `[data-testid="issue-labels-left"]` (`IssuePropertiesRow.tsx`).
 */

/** The desktop the rows read at 1440 px. */
export const DESKTOP = { height: 900, width: 1440 } as const;

/** The iPhone 13's viewport, the suite's phone (`e2e/playwright.config.ts`'s `iphone` project),
 *  which is the screen the header's limit at 390 px is for. */
export const IPHONE_13 = { height: 664, width: 390 } as const;

/** The viewport a row reads `width` in: the iPhone 13's at 390 px, else a phone's height below
 *  1024 px and the desktop's from there. */
export function viewportAt(width: number): { readonly height: number; readonly width: number } {
  if (width === IPHONE_13.width) return IPHONE_13;
  return { height: width < 1024 ? 844 : DESKTOP.height, width };
}

/** INFRA-1196's own title, 115 characters: the one the header's widths were read on
 *  (dispatch://INFRA-1196/comment/f31b8ac4-874c-4735-94c8-8af17a95eba2). */
export const MEASURED_TITLE =
  "Dispatch's issue page is redesigned from design research: an ordered body and a header that fits, with nothing lost";

/** Twenty labels, the most Dispatch lets an issue hold (`maxIssueLabels`,
 *  `packages/envoy/internal/dispatch/api/issue_queries.go`): more than the header's line holds
 *  at 1440 px and at 390 px, so it ends with the count of those left over. */
export const TWENTY_LABELS = [
  "dispatch",
  "layout",
  "redesign",
  "header",
  "details",
  "keyboard",
  "phone",
  "typeface",
  "accessibility",
  "approval",
  "outline",
  "presets",
  "contents",
  "composer",
  "decisions",
  "verification",
  "plan",
  "children",
  "routing",
  "web",
] as const;

/** The project the seed writes in. */
export const HEADER_PROJECT = "HDR";

/** The agent session that asks the seed's questions: a question it asks on an issue alice holds
 *  waits on alice (`waiting_on_person`). */
export const asAgent = {
  actor: { id: "issue-header-agent", kind: "session" as const },
  as: "agent" as const,
};

/** What `seedHeader` wrote. */
export interface HeaderSeed {
  readonly childKeys: readonly string[];
  readonly key: string;
  readonly parentKey: string;
  readonly specArtifactId: string;
}

/**
 * One issue holding every part of the header's list (R1 of dispatch://INFRA-3943), written
 * through `e2e/api.ts`: a parent whose route reaches nobody, so the parent draws the header's one
 * route marker; the issue under it in progress, at P1, assigned to alice, with a spec and a
 * Verification document, three children (one done, one in progress, one to do) and two questions
 * an agent asked, which wait on alice. No approval request is open: a row that needs one asks
 * for it.
 */
export async function seedHeader(): Promise<HeaderSeed> {
  await createProject({ key: HEADER_PROJECT, name: "Header" });
  const parent = await createIssue({
    force: true,
    project: HEADER_PROJECT,
    spec: "The parent's plan.",
    title: "The parent of the header's issue",
  });
  await patchIssue(parent.key, { route: "role:nobody-holds-this" });
  const issue = await createIssue({
    force: true,
    parent: parent.key,
    project: HEADER_PROJECT,
    spec: ":::requirement{#r1}\nThe header fits.\n:::\n",
    title: "The header's issue",
  });
  await patchIssue(issue.key, { assignee: "alice", priority: 1, status: "in_progress" });
  await createIssueArtifact(issue.key, {
    content: `:::check{#c1 serves="dispatch://${issue.key}/spec#b-r1" surface="browser" run="bun run e2e -- e2e/issue-page-p.e2e.ts"}\nThe header fits.\n:::\n`,
    name: "verification.md",
  });
  const childKeys: string[] = [];
  for (const [title, status] of [
    ["The child that is done", "done"],
    ["The child in progress", "in_progress"],
    ["The child to do", "todo"],
  ] as const) {
    const child = await createIssue({
      force: true,
      parent: issue.key,
      project: HEADER_PROJECT,
      title,
    });
    await patchIssue(child.key, { status });
    childKeys.push(child.key);
  }
  await createAsk(issue.key, { question: "Which width does it read first?" }, asAgent);
  await createAsk(issue.key, { question: "And which typeface?" }, asAgent);
  return {
    childKeys,
    key: issue.key,
    parentKey: parent.key,
    specArtifactId: issue.primary_artifact_id ?? "",
  };
}

/** The new page's header. */
export function issueHeader(page: Page): Locator {
  return page.locator("[data-issue-page] header[data-issue-header]");
}

/** The part of the header that stays at the top of the screen from 761 px up: the key's row
 *  and the title. */
export function identityBand(page: Page): Locator {
  return issueHeader(page).locator("[data-identity-block]");
}

/** The header's one properties line. */
export function propertiesLine(page: Page): Locator {
  return issueHeader(page).getByTestId("issue-state-actions");
}

/** Waits for the header's properties line: every row's first step, so a page that draws only
 *  part A's key and title fails on this and not on a later reading. */
export async function expectHeaderDrawn(page: Page): Promise<void> {
  await expect(propertiesLine(page), "the new page draws no properties line").toBeVisible();
}

/** The labels the header's line draws, in order: the chips that fit whole. */
export function headerLabels(page: Page): Locator {
  return issueHeader(page).locator('[data-testid="issue-header-labels"] > [data-header-label]');
}

/** The count that ends the line with the labels left over, which opens Details at Labels. */
export function labelsLeft(page: Page): Locator {
  return issueHeader(page).getByTestId("issue-labels-left");
}

/** The count of what waits on the reader, or whose turn it is when nothing waits. */
export function waitingCount(page: Page): Locator {
  return issueHeader(page).getByTestId("issue-whose-turn");
}

/** Details, the drawer over the right of the page. */
export function details(page: Page): Locator {
  return page.locator("aside[data-details]");
}

/** A `"` inside an attribute value would end the selector's string. */
function cssValue(value: string): string {
  expect(value, `${value} cannot be written into a selector`).not.toContain('"');
  return value;
}

/** The value a badge-and-select control shows a reader: the badge drawn under the `select`
 *  named `label` (`web/src/features/issue/badge-select.ts`). The badge is hidden from assistive
 *  technology, because the select carries the name. */
export function badgeValue(page: Page, label: string): Locator {
  return issueHeader(page).locator(
    `:has(> select[aria-label="${cssValue(label)}"]) > [aria-hidden="true"]`
  );
}

/** Every element the header draws that a reader reads or presses, by what it is: its test id,
 *  or its tag with the name a reader hears. Text it lays out unseen is left out. */
export function headerParts(page: Page): Promise<string[]> {
  return issueHeader(page).evaluate((header) =>
    [...header.querySelectorAll("a, button, select, h1, textarea, [data-testid]")]
      .filter((part) => part.checkVisibility())
      .map(
        (part) =>
          part.getAttribute("data-testid") ??
          `${part.tagName.toLowerCase()} ${part.getAttribute("aria-label") ?? (part.textContent ?? "").replace(/\s+/g, " ").trim()}`
      )
  );
}

/**
 * The header's controls that fail WCAG 2.2's target size (2.5.8) as written: each target at
 * least 24 by 24 CSS px, or, where one is smaller, a 24 px circle centred on it that overlaps no
 * other target.
 */
export function undersizedTargets(page: Page): Promise<string[]> {
  return issueHeader(page).evaluate((header) => {
    const targets = [...header.querySelectorAll<HTMLElement>("a, button, select")]
      .filter((target) => target.checkVisibility())
      .map((target) => ({
        box: target.getBoundingClientRect(),
        name: target.getAttribute("aria-label") ?? (target.textContent ?? "").trim(),
      }));
    const overlaps = (centre: { x: number; y: number }, box: DOMRect) => {
      const x = Math.max(box.left, Math.min(centre.x, box.right));
      const y = Math.max(box.top, Math.min(centre.y, box.bottom));
      return (x - centre.x) ** 2 + (y - centre.y) ** 2 < 12 ** 2;
    };
    return targets
      .filter(({ box }, index) => {
        if (box.width >= 24 - 0.5 && box.height >= 24 - 0.5) return false;
        const centre = { x: box.left + box.width / 2, y: box.top + box.height / 2 };
        return targets.some((other, at) => at !== index && overlaps(centre, other.box));
      })
      .map(({ box, name }) => `${name}: ${box.width.toFixed(1)} x ${box.height.toFixed(1)}`);
  });
}

/** The controls R3 holds to 24 px tall at every width (the tester's finding F13): the status,
 *  the priority, the assignee, the count, Approve, Request changes and the labels' count, each
 *  as `<name>: <height>` where it is drawn shorter. */
export function shortControls(page: Page, key: string): Promise<string[]> {
  return issueHeader(page).evaluate((header, key) => {
    const named: [string, Element | null][] = [
      ["the status", header.querySelector('select[aria-label="Status"]')],
      ["the priority", header.querySelector(`select[aria-label="Priority of ${key}"]`)],
      ["the assignee", header.querySelector(`select[aria-label="Assignee of ${key}"]`)],
      ["the count", header.querySelector('button[data-testid="issue-whose-turn"]')],
      ["the labels' count", header.querySelector('[data-testid="issue-labels-left"]')],
      ...[...header.querySelectorAll("button")]
        .filter((button) => /^(Approve|Request changes)$/.test((button.textContent ?? "").trim()))
        .map((button): [string, Element] => [(button.textContent ?? "").trim(), button]),
    ];
    return named
      .filter(([, control]) => control !== null)
      .map(([name, control]) => [name, control?.getBoundingClientRect().height ?? 0] as const)
      .filter(([, height]) => height < 24 - 0.5)
      .map(([name, height]) => `${name}: ${height.toFixed(1)} px`);
  }, key);
}

/** The header as R3 measures it: from the top of the header to the top of the view row. */
export interface HeaderReading {
  readonly height: number;
  /** The words of each line drawn between the two, top to bottom. */
  readonly lines: readonly string[];
}

/**
 * The header's band and the lines drawn in it. A line is a run of drawn text: every text node of
 * the page inside the band and inside the header's own column, one box per line the text wraps
 * to, and a form control's value as a box of its own font size at the control's middle, since a
 * select draws its value without a text node. Boxes that overlap vertically are one line. Text a
 * box of at most a pixel clips away, or that is invisible or transparent, is not drawn. Null when
 * the page draws no header or no view row.
 */
export function headerReading(page: Page): Promise<HeaderReading | null> {
  return page.evaluate(() => {
    const root = document.querySelector("[data-issue-page]");
    const header = root?.querySelector("header[data-issue-header]") ?? null;
    const row = root?.querySelector("[data-filter-row]") ?? null;
    if (root === null || root === undefined || header === null || row === null) return null;
    // The `<header>` draws no box of its own (`display: contents`), so its column is the box
    // around what it holds: the band, the properties line and what a write answers.
    const parts = [...header.children].map((part) => part.getBoundingClientRect());
    const column = {
      left: Math.min(...parts.map((part) => part.left)),
      right: Math.max(...parts.map((part) => part.right)),
      top: Math.min(...parts.filter((part) => part.height > 0).map((part) => part.top)),
    };
    const top = column.top;
    const bottom = row.getBoundingClientRect().top;
    const inBand = (box: DOMRect): boolean =>
      box.width > 0 &&
      box.height > 0 &&
      box.bottom > top &&
      box.top < bottom &&
      box.right > column.left &&
      box.left < column.right;
    const drawn = (element: Element): boolean => {
      for (let at: Element | null = element; at !== null; at = at.parentElement) {
        const style = getComputedStyle(at);
        if (style.visibility !== "visible" || style.opacity === "0") return false;
        const box = at.getBoundingClientRect();
        if (style.overflow !== "visible" && (box.width <= 1 || box.height <= 1)) return false;
      }
      return true;
    };
    const pieces: { bottom: number; left: number; text: string; top: number }[] = [];
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const content = node.textContent ?? "";
      const element = node.parentElement;
      if (content.trim() === "" || element === null || element.closest("select") !== null) continue;
      const range = document.createRange();
      range.selectNodeContents(node);
      const boxes = [...range.getClientRects()].filter(inBand);
      if (boxes.length === 0 || !drawn(element)) continue;
      // A text that wraps is one box per line; each line is given the characters drawn in it,
      // so a failure names the words of each line rather than the whole text on each.
      const words = boxes.length === 1 ? [content] : boxes.map(() => "");
      for (let at = 0; boxes.length > 1 && at < content.length; at += 1) {
        range.setStart(node, at);
        range.setEnd(node, at + 1);
        const glyph = range.getClientRects()[0];
        if (glyph === undefined) continue;
        const line = boxes.findIndex(
          (box) => glyph.top >= box.top - 1 && glyph.bottom <= box.bottom + 1
        );
        if (line >= 0) words[line] += content[at];
      }
      boxes.forEach((box, line) => {
        const text = (words[line] ?? "").replace(/\s+/g, " ").trim();
        pieces.push({ bottom: box.bottom, left: box.left, text, top: box.top });
      });
    }
    for (const control of root.querySelectorAll("select, input:not([type='hidden']), textarea")) {
      const box = control.getBoundingClientRect();
      if (!inBand(box) || !drawn(control)) continue;
      const size = Number.parseFloat(getComputedStyle(control).fontSize);
      const middle = (box.top + box.bottom) / 2;
      const text =
        control instanceof HTMLSelectElement
          ? (control.selectedOptions[0]?.textContent ?? "").trim()
          : (control as HTMLInputElement).value;
      pieces.push({ bottom: middle + size / 2, left: box.left, text, top: middle - size / 2 });
    }
    pieces.sort((a, b) => a.top - b.top);
    const lines: (typeof pieces)[] = [];
    let lineBottom = Number.NEGATIVE_INFINITY;
    for (const piece of pieces) {
      const line = lines.at(-1);
      if (line !== undefined && piece.top < lineBottom - 1) {
        line.push(piece);
        lineBottom = Math.max(lineBottom, piece.bottom);
      } else {
        lines.push([piece]);
        lineBottom = piece.bottom;
      }
    }
    return {
      height: bottom - top,
      lines: lines.map((line) =>
        line
          .sort((a, b) => a.left - b.left)
          .map((piece) => piece.text)
          .join(" ")
      ),
    };
  });
}

/**
 * R3's limit on the header at `width`: 3 lines and 130 px at 1440 px; at 390 px, on the 20-label
 * seed and the measured title, 8 lines and 250 px while no approval request waits on the reader
 * and 9 lines and 280 px while one does (ruling 8 of
 * dispatch://INFRA-1196/artifact/build-rulings-md). No other width has a limit.
 */
export function headerLimit(
  width: number,
  requestWaiting: boolean
): { readonly height: number; readonly lines: number } | undefined {
  if (width === DESKTOP.width) return { height: 130, lines: 3 };
  if (width !== IPHONE_13.width) return undefined;
  return requestWaiting ? { height: 280, lines: 9 } : { height: 250, lines: 8 };
}

/**
 * The header read against its limit at `width`, softly so one run reports every width and seed,
 * with the page at its top; at 390 px the title is drawn whole. `drawnIn` names the typeface the
 * page is drawn in, for the message. The header is read alone: no row of the outline is waited
 * for.
 */
export async function expectHeaderWithinItsLimit(
  page: Page,
  width: number,
  requestWaiting: boolean,
  drawnIn: string
): Promise<void> {
  const limit = headerLimit(width, requestWaiting);
  if (limit === undefined) return;
  const where = `at ${width} px in ${drawnIn} with ${requestWaiting ? "a request" : "no request"} waiting`;
  await expect(filterRow(page), `the page draws no view row ${where}`).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  const reading = await headerReading(page);
  if (reading === null) throw new Error(`the page draws no header or no view row ${where}`);
  const drawnAs = `${reading.lines.length} lines, ${reading.height.toFixed(1)} px`;
  test.info().annotations.push({ description: `${where}: ${drawnAs}`, type: "header" });
  expect
    .soft(
      reading.lines.length,
      `the header takes ${drawnAs} ${where}: ${JSON.stringify(reading.lines)}`
    )
    .toBeLessThanOrEqual(limit.lines);
  expect
    .soft(reading.height, `the header takes ${drawnAs} ${where}`)
    .toBeLessThanOrEqual(limit.height);
  if (width === IPHONE_13.width) {
    await assertWhole(issueHeader(page).getByRole("heading", { level: 1 }));
  }
}

/** What stays at the top of the screen and what scrolls away (ruling 5), where the page stands
 *  now. */
export interface StickyReading {
  /** The band - the key's row and the title - by its box on the screen. */
  readonly block: { readonly bottom: number; readonly height: number; readonly top: number };
  /** Whether the header says it is stuck (`data-condensed`), which draws its shadow. */
  readonly condensed: boolean;
  /** The band's `box-shadow`, as computed. */
  readonly shadow: string;
  /** The properties line, by its box on the screen. */
  readonly properties: { readonly bottom: number; readonly top: number };
  /** The height the page publishes for whatever stays (`--issue-identity-height`), in px. */
  readonly published: number;
  /** How far the page has scrolled. */
  readonly scrolled: number;
  /** The title's box and its type, as computed. */
  readonly title: {
    readonly bottom: number;
    readonly clamp: string;
    readonly line: string;
    readonly size: string;
    readonly spacing: string;
    readonly top: number;
    readonly weight: string;
    readonly wordsWidth: number;
  };
}

/** `StickyReading` of the page, or null where it draws no header. */
export function stickyReading(page: Page): Promise<StickyReading | null> {
  return page.evaluate(() => {
    const header = document.querySelector("[data-issue-page] header[data-issue-header]");
    const block = header?.querySelector("[data-identity-block]") ?? null;
    const title = header?.querySelector("h1") ?? null;
    const properties = header?.querySelector('[data-testid="issue-state-actions"]') ?? null;
    if (header === null || header === undefined || block === null || title === null) return null;
    if (properties === null) return null;
    const box = (element: Element) => {
      const rect = element.getBoundingClientRect();
      return { bottom: rect.bottom, height: rect.height, top: rect.top };
    };
    const type = getComputedStyle(title);
    // The box the title's words wrap in: the heading less its padding and its rule.
    const wordsWidth =
      title.getBoundingClientRect().width -
      Number.parseFloat(type.paddingLeft) -
      Number.parseFloat(type.paddingRight) -
      Number.parseFloat(type.borderLeftWidth) -
      Number.parseFloat(type.borderRightWidth);
    return {
      block: box(block),
      condensed: header.getAttribute("data-condensed") === "true",
      properties: box(properties),
      published: Number.parseFloat(
        getComputedStyle(document.documentElement).getPropertyValue("--issue-identity-height")
      ),
      scrolled: window.scrollY,
      shadow: getComputedStyle(block).boxShadow,
      title: {
        ...box(title),
        clamp: type.webkitLineClamp,
        line: type.lineHeight,
        size: type.fontSize,
        spacing: type.letterSpacing,
        weight: type.fontWeight,
        wordsWidth,
      },
    };
  });
}

/** The family `web/src/styles.css` declares for the typeface the new page ships and draws in
 *  (part B). */
export const TYPEFACE = "Dispatch Inter";

/** The address the page's stylesheet loads the typeface from, read from its own `@font-face`
 *  rule; null when no rule declares the family. */
export function typefaceAddress(page: Page): Promise<string | null> {
  return page.evaluate((family) => {
    const find = (rules: CSSRuleList, base: string): string | null => {
      for (const rule of rules) {
        if (rule instanceof CSSFontFaceRule) {
          const named = rule.style.getPropertyValue("font-family").replace(/["']/g, "").trim();
          const source = /url\(["']?([^"')]+)["']?\)/.exec(rule.style.getPropertyValue("src"));
          if (named === family && source?.[1] !== undefined) return new URL(source[1], base).href;
        } else if (rule instanceof CSSGroupingRule) {
          const found = find(rule.cssRules, base);
          if (found !== null) return found;
        }
      }
      return null;
    };
    for (const sheet of document.styleSheets) {
      const found = find(sheet.cssRules, sheet.href ?? document.baseURI);
      if (found !== null) return found;
    }
    return null;
  }, TYPEFACE);
}

/** How each face of the typeface's family stands in the document's font set: `loaded` once the
 *  file arrived, `error` once its request failed. */
export function typefaceStatuses(page: Page): Promise<string[]> {
  return page.evaluate(
    (family) =>
      [...document.fonts]
        .filter((face) => face.family.replace(/["']/g, "") === family)
        .map((face) => face.status),
    TYPEFACE
  );
}

/** The stack the header's band is drawn in, as its computed `font-family`, each family
 *  unquoted. */
export function headerFamilies(page: Page): Promise<string[]> {
  return identityBand(page).evaluate((band) =>
    getComputedStyle(band)
      .fontFamily.split(",")
      .map((family) => family.replace(/["']/g, "").trim())
  );
}

/** What the count's press left in the history entry: its `waitingPress` number, or null. */
export function waitingPressInHistory(page: Page): Promise<number | null> {
  return page.evaluate(() => {
    const found = /"waitingPress":(-?\d+(?:\.\d+)?(?:e[+-]?\d+)?)/.exec(
      JSON.stringify(history.state) ?? ""
    );
    return found?.[1] === undefined ? null : Number(found[1]);
  });
}

/** A title of `lines` lines at the header's width: words added until the heading, drawn whole,
 *  takes that many lines of its own line height. */
export async function titleOfLines(page: Page, lines: number): Promise<string> {
  const words = ["The", "header", "keeps", "the", "press", "that", "ends", "a", "title", "edit"];
  let title = "Edit";
  for (let index = 0; index < 400; index += 1) {
    title = `${title} ${words[index % words.length]}`;
    const drawn = await page.evaluate((text) => {
      const heading = document.querySelector<HTMLElement>(
        "[data-issue-page] [data-issue-header] h1"
      );
      if (heading === null) return 0;
      const probe = heading.cloneNode() as HTMLElement;
      probe.textContent = text;
      probe.style.display = "block";
      probe.style.webkitLineClamp = "unset";
      probe.style.width = `${heading.getBoundingClientRect().width}px`;
      probe.style.position = "absolute";
      probe.style.visibility = "hidden";
      heading.parentElement?.append(probe);
      const lineHeight = Number.parseFloat(getComputedStyle(probe).lineHeight);
      const count = Math.round((probe.getBoundingClientRect().height - 2) / lineHeight);
      probe.remove();
      return count;
    }, title);
    if (drawn === lines) return title;
  }
  throw new Error(`no title takes ${lines} lines`);
}
