/*
 * The helpers part N's rows read (dispatch://INFRA-1196 decision 2): `e2e/page-inventory.ts`'s
 * functions, which read what a page offers for the inventory row and the recorder, and each helper
 * of the checkpoint's `e2e/issue-page.ts` that no earlier part's helper file holds. Every other
 * helper is imported from the part that brought it.
 */

import { expect, type Locator, type Page } from "@playwright/test";
import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";
import { getIssue } from "./api";
import { connectedDot, documentEditor } from "./editor";
import { issuePage, openMenu, outlineControl } from "./issue-page";
import { drawnRows, nodeRows } from "./issue-page-g";
import type { Box } from "./layout";
import { navigateInApp } from "./sends";
import { assertPageFits } from "./unclipped";

/** The address that opens an issue's spec with its editor: the spec's own address,
 *  `/issues/KEY/artifacts/<slug>`, which draws it in place of the outline. `/issues/KEY/spec`
 *  is the Spec view and the issue's own address the outline, and neither draws the spec's text
 *  (dispatch://INFRA-1196 R3, R20). The checkpoint's `e2e/editor.ts` held it; no earlier part
 *  brought it, so it is here (decision 2). */
export async function specAddress(issueKey: string): Promise<string> {
  const issue = await getIssue(issueKey);
  const spec = (issue.artifacts ?? []).find(
    (artifact) => artifact.id === issue.primary_artifact_id
  );
  if (spec === undefined) throw new Error(`${issueKey} has no spec to open`);
  return `/issues/${issueKey}/artifacts/${spec.slug}`;
}

/** Opens an issue's spec at its own address and waits until its editor shows `text` and its
 *  document is connected, so a selection or a margin card the row reaches for next is live.
 *  `e2e/editor.ts`'s `openSpec` opens `/spec`, which on the new page is the Spec view and draws
 *  no editor. */
export async function openSpecDocument(page: Page, issueKey: string, text: string): Promise<void> {
  await page.goto(await specAddress(issueKey));
  await expect(documentEditor(page)).toContainText(text);
  await expect(connectedDot(page)).toHaveText("connected");
}

/** The selectors the readers below pass into the browser. Nothing outside this module spells one:
 *  a test reads the page through the locators and readers this module exports. */
const SELECTORS = {
  answerAuthor: "[data-answer-author]",
  answerLine: "[data-node-answer]",
  answerText: "[data-answer-text]",
  authorText: "[data-author-text]",
  branchMore: "[data-branch-more]",
  details: '[data-testid="desktop-margin-shell"]',
  childRow: "[data-child-row]",
  /** The whole filter row: the five controls or the one control, Outline, and the small
   *  controls at its right end (`IssuePresets.tsx`). */
  filterRow: "[data-filter-row]",
  gap: "[data-gap]",
  /** The part of the identity block that stays at the top of the screen from 761 px up: the
   *  key's row and the title (`IssueHeader.tsx`). */
  identityBand: "[data-identity-block]",
  identityBlock: "header[data-issue-header]",
  issuePage: "[data-issue-page]",
  marginSlot: '[data-testid="issue-margin"]',
  nodeAuthor: "[data-node-author]",
  nodeBody: "[data-node-body]",
  nodeChip: "[data-node-chip]",
  nodeFirstLine: "[data-node-first-line]",
  nodeGlyph: "svg[data-glyph]",
  nodeRow: "[data-node-row]",
  nodeUnsure: "[data-node-unsure]",
  nodeTag: "[data-node-tag]",
  nodeWords: "[data-node-words]",
  nodeState: "[data-node-state]",
  pageChrome: "[data-page-chrome]",
  parentLine: "[data-node-parent-line]",
  presetCount: "[data-preset-count]",
  presetRow: '[data-testid="issue-presets"]',
  tree: '[role="tree"]',
} as const;

// ---------------------------------------------------------------------------------------------
// The page's parts, as locators.
// ---------------------------------------------------------------------------------------------

export function identityBlock(page: Page): Locator {
  return page.locator(SELECTORS.identityBlock);
}

export function nodeMenuButton(page: Page): Locator {
  return issuePage(page).getByRole("button", { exact: true, name: CONTROL_LABELS.other_nodes });
}

export function nodeMenuItems(page: Page): Locator {
  return openMenu(page).getByRole("menuitem");
}

/** The hidden file input the upload opens, which is how a test hands it a file. */
export function uploadInput(page: Page): Locator {
  return page.locator('input[type="file"]');
}

/** The box of the filter by words, which is `?q=`: drawn while it holds words or has the caret,
 *  and in the Outline sheet at 760 px and below. */
export function filterBox(page: Page): Locator {
  return page.getByTestId("issue-filter");
}

/** The filter by words' small control at the right end of the filter row, which opens its box
 *  with the caret in it; `t f` does the same (`IssuePresets.tsx`). */
export function filterControl(page: Page): Locator {
  return issuePage(page).getByRole("button", { exact: true, name: CONTROL_LABELS.filter });
}

/** The version control: the version picker, the name-a-version control, the link, and beside an
 *  open editor its live connection (`VersionControl.tsx`). It is drawn - and counted - while a
 *  filter is on or a document is open, behind its small control at the right end of the filter
 *  row, which `openVersionControl` presses; at 760 px and below it is in the Outline sheet. */
export function versionControl(page: Page): Locator {
  return page.getByTestId("issue-version-control");
}

/** The version control's own small control at the right end of the filter row. */
export function versionControlButton(page: Page): Locator {
  return issuePage(page).getByRole("button", { exact: true, name: CONTROL_LABELS.version });
}

/** Opens the version control as a reader does, unless it is open: from its small control at the
 *  right end of the filter row, or, at 760 px and below where the row draws none, by opening the
 *  Outline sheet that holds it. */
export async function openVersionControl(page: Page): Promise<void> {
  if (await versionControl(page).isVisible()) return;
  const own = versionControlButton(page);
  await ((await own.count()) > 0 ? own : outlineControl(page)).click();
  await expect(versionControl(page), "the version control did not open").toBeVisible();
}

/** Which version the version control is showing, as the picker's own value: the empty string
 *  for the current version and the number of a pinned one. The control's text cannot say it - a
 *  `select`'s option texts are part of its container's `innerText`, so "Version 1" is in that
 *  text whichever version is shown - so the value is what is read, open or shut. */
export function versionPicker(page: Page): Locator {
  return versionControl(page).locator(`select[aria-label="${CONTROL_LABELS.version}"]`);
}

/** The document or file opened in place of the outline (`ArtifactRoutePanel.tsx`). */
export function openedArtifact(page: Page): Locator {
  return page.getByTestId("opened-artifact");
}

/** The editor of a document opened in place of the outline. */
export function openedEditor(page: Page): Locator {
  return openedArtifact(page).getByRole("textbox", { name: "Document editor" });
}

/** A control of the page by the role and accessible name the old page's inventory recorded for it
 *  (`e2e/fixtures/issue-page-inventory.json`), which is how c-nothing-lost acts on each name that
 *  writes today. */
export function offered(page: Page, role: Parameters<Page["getByRole"]>[0], name: string): Locator {
  return page.getByRole(role, { exact: true, name });
}

/** What a `select` offers, as its value and its label: a test that writes a property picks the
 *  option it means rather than an index (c-nothing-lost). */
export function optionsOf(control: Locator): Promise<{ label: string; value: string }[]> {
  return control.evaluateAll((found) =>
    found.flatMap((element) =>
      [...(element as HTMLSelectElement).options].map((option) => ({
        label: option.label,
        value: option.value,
      }))
    )
  );
}

/** What the page says in its own words: the text of each `[data-page-chrome]` element with every
 *  `[data-author-text]` descendant removed, which is what c-plain-words reads word by word. */
export function chromeTexts(page: Page): Promise<string[]> {
  return page.evaluate((selectors) => {
    const found: string[] = [];
    for (const element of document.querySelectorAll(
      `${selectors.issuePage} ${selectors.pageChrome}`
    )) {
      const copy = element.cloneNode(true) as Element;
      for (const authored of copy.querySelectorAll(selectors.authorText)) authored.remove();
      const text = (copy.textContent ?? "").replace(/\s+/g, " ").trim();
      if (text !== "") found.push(text);
    }
    return found;
  }, SELECTORS);
}

/** Every element of the page that carries visible text and no `[data-page-chrome]` or
 *  `[data-author-text]` ancestor or self, which is text the page divides into neither. */
export function undividedTexts(page: Page): Promise<string[]> {
  return page.evaluate((selectors) => {
    const found: string[] = [];
    const walker = document.createTreeWalker(
      document.querySelector(selectors.issuePage) ?? document.createElement("div"),
      NodeFilter.SHOW_TEXT
    );
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const text = (node.textContent ?? "").replace(/\s+/g, " ").trim();
      if (text === "") continue;
      const element = node.parentElement;
      if (element === null) continue;
      if (element.closest(`${selectors.pageChrome}, ${selectors.authorText}`) !== null) continue;
      if (element.getClientRects().length === 0) continue;
      found.push(text);
    }
    return found;
  }, SELECTORS);
}

/** The filter row as it is drawn (ruling 5: one line at every width). */
export interface FilterRowReading {
  /** The name of each control the row draws, left to right: what a reader hears it called. */
  readonly controls: readonly string[];
  /** The row's own height in CSS pixels. */
  readonly height: number;
  /** How many lines its controls stand on: controls whose boxes overlap vertically are one. */
  readonly lines: number;
  /** How far the row's content runs past its own right edge, in px; 0 where it fits. */
  readonly overflow: number;
}

/** `FilterRowReading` of the page, or null where it draws no filter row. A control is a button,
 *  a box or a select the row draws on the page itself: one inside a menu, a dialog or a part
 *  the row keeps shut is not on the row. */
export function filterRowReading(page: Page): Promise<FilterRowReading | null> {
  return page.evaluate((selectors) => {
    const row = document.querySelector(selectors.filterRow);
    if (row === null) return null;
    const controls = [...row.querySelectorAll<HTMLElement>("button, input, select")]
      .filter(
        (control) =>
          control.checkVisibility() &&
          control.closest('[role="menu"], [role="dialog"], [hidden]') === null &&
          control.getAttribute("type") !== "file"
      )
      .map((control) => {
        // The name a reader hears: the label, what names it, or its own words less the parts
        // hidden from them (a control's `▾`).
        const heard = control.cloneNode(true) as HTMLElement;
        for (const hidden of heard.querySelectorAll('[aria-hidden="true"]')) hidden.remove();
        const named = control.getAttribute("aria-labelledby");
        const name =
          control.getAttribute("aria-label") ??
          (named === null
            ? null
            : named
                .split(" ")
                .map((id) => document.getElementById(id)?.textContent ?? "")
                .join(" ")) ??
          heard.textContent ??
          "";
        return { box: control.getBoundingClientRect(), name: name.replace(/\s+/g, " ").trim() };
      })
      .sort((a, b) => a.box.left - b.box.left);
    let lines = 0;
    let lineBottom = Number.NEGATIVE_INFINITY;
    for (const { box } of [...controls].sort((a, b) => a.box.top - b.box.top)) {
      if (box.top >= lineBottom - 1) lines += 1;
      lineBottom = Math.max(lineBottom, box.bottom);
    }
    const edge = row.getBoundingClientRect();
    return {
      controls: controls.map((control) => control.name),
      height: edge.height,
      lines,
      overflow: Math.max(0, ...controls.map((control) => control.box.right - edge.right)),
    };
  }, SELECTORS);
}

/** The three things inside a node's own text that may scroll sideways (R17): a code block, a
 *  table and a picture. c-reflow names them by selector and leaves them out of its walk. */
export const AUTHOR_SCROLLERS = ["pre", "table", "img"] as const;

/** Each of those three that is drawn outside a `[data-author-text]` element, which is where the
 *  exception does not reach. */
export function scrollersOutsideAuthorText(page: Page): Promise<string[]> {
  return page.evaluate(
    ({ scrollers, selectors }) => {
      const found: string[] = [];
      for (const kind of scrollers) {
        for (const element of document.querySelectorAll(`${selectors.issuePage} ${kind}`)) {
          if (element.closest(selectors.authorText) !== null) continue;
          if (element.getClientRects().length === 0) continue;
          found.push(`${kind} in ${element.parentElement?.tagName.toLowerCase() ?? "?"}`);
        }
      }
      return found;
    },
    { scrollers: AUTHOR_SCROLLERS, selectors: SELECTORS }
  );
}

/** Every row of the tree that holds none of those three, which is the set c-reflow walks with
 *  `assertWhole`. */
export async function rowsWithoutAuthorScrollers(page: Page): Promise<Locator[]> {
  const rows = await drawnRows(page);
  const held = await page.evaluate(
    ({ scrollers, selectors }) =>
      [...document.querySelectorAll(`${selectors.issuePage} ${selectors.nodeRow}`)].map(
        (row) => row.querySelector(scrollers.join(", ")) !== null
      ),
    { scrollers: AUTHOR_SCROLLERS, selectors: SELECTORS }
  );
  const walked: Locator[] = [];
  rows.forEach((row, index) => {
    if (held[index] === true || !row.visible) return;
    walked.push(nodeRows(page).nth(index));
  });
  return walked;
}

// ---------------------------------------------------------------------------------------------
// Opening the page.
// ---------------------------------------------------------------------------------------------

/** Opens one of the addresses the old tabs answered, from wherever the page is on that issue:
 *  every one of them still resolves (R19, c-urls), and a spec that only needs to be there goes
 *  straight to it rather than pressing a control that no longer exists. The move is inside the
 *  page, as the tab it replaces was: a reload would drop a staged upload, a held transport and
 *  everything else the page is holding. */
export async function openIssueAddress(page: Page, tab: string): Promise<void> {
  const key = new URL(page.url()).pathname.match(/^\/issues\/([A-Z][A-Z0-9]*-\d+)/)?.[1] ?? "";
  await navigateInApp(page, `/issues/${key}/${tab}`);
}

/** The identity block's one actions control, open. Close, reopen and the control that gives the
 *  issue a parent are behind it now (R11), where the nine-tab page drew them as bare buttons.
 *  The control toggles, so this opens it only while it is shut: a spec that asks twice leaves
 *  the actions on screen rather than hiding them again. */
export async function openIssueActions(page: Page): Promise<void> {
  const control = offered(page, "button", CONTROL_LABELS.issue_actions);
  if ((await control.getAttribute("aria-expanded")) !== "true") {
    await control.click();
  }
}

/**
 * What a page offers a reader, recorded before a redesign and asserted after it: every landmark
 * and every control, each as the role it exposes and the name a screen reader announces for it.
 *
 * The walk is Playwright's own accessibility snapshot (`page.ariaSnapshotJSON`), so a name is the
 * one Playwright computes from `aria-label`, `aria-labelledby`, the element's own text, `title`
 * and `alt`, which is the name `getByRole(role, { name })` resolves and the name a screen reader
 * reads. Nothing here extracts DOM text by hand but the text of the outline's `treeitem` rows,
 * for the one kind of entry a parent row carries (`ParentRowMapping`), and nothing here knows
 * anything about the redesign: this module records what is on a page and asserts that a later
 * page still carries it.
 *
 * The snapshot leaves out what no reader can reach - an element with `display: none`, `hidden`,
 * or `aria-hidden` - and renders only one view of the page at a time, so a page that hides part
 * of itself behind a tab is recorded one view at a time and the views merged (`mergeInventory`).
 */
export interface InventoryEntry {
  /** The accessible name, with every server-generated uuid in it written as `<id>` (`collect`),
   *  and empty for a control that exposes no name at all. */
  readonly name: string;
  readonly role: string;
}

/** The landmarks an inventory records: the regions a reader navigates a page by. */
export const INVENTORY_LANDMARKS = [
  "banner",
  "complementary",
  "contentinfo",
  "form",
  "main",
  "navigation",
  "region",
  "search",
] as const;

/** The controls an inventory records: everything a reader can act on. */
export const INVENTORY_CONTROLS = [
  "button",
  "checkbox",
  "combobox",
  "link",
  "menuitem",
  "option",
  "radio",
  "searchbox",
  "slider",
  "spinbutton",
  "switch",
  "tab",
  "textbox",
] as const;

const inventoryRoles: ReadonlySet<string> = new Set<string>([
  ...INVENTORY_LANDMARKS,
  ...INVENTORY_CONTROLS,
]);

/** One entry with where it sits, which is how reachability is read. A node the snapshot gave no
 *  box for is recorded with none and only its presence is asserted. */
interface MeasuredEntry extends InventoryEntry {
  readonly box?: Box;
}

/** `page.ariaSnapshotJSON` answers a free-form value, so every field is read defensively. */
interface AriaSnapshotNode {
  readonly box?: unknown;
  readonly children?: unknown;
  readonly name?: unknown;
  readonly role?: unknown;
}

function readBox(value: unknown): Box | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const { height, width, x, y } = value as Record<string, unknown>;
  if (
    typeof height !== "number" ||
    typeof width !== "number" ||
    typeof x !== "number" ||
    typeof y !== "number"
  ) {
    return undefined;
  }
  return { height, width, x, y };
}

/**
 * What a name carries that belongs to the run rather than to the page, and what it is recorded
 * as. A control names the row it acts on - `Copy reference dispatch://LAY-2/comment/<id>`, `2
 * replies · last reply <time>` - so its name holds the uuid the server gave that row and the
 * relative time `Timestamp` draws for it (`web/src/features/refs/Timestamp.tsx`), and neither is a
 * property of the page: the uuid is new in every run, and the time crosses from `just now` to `1
 * minute ago` while the recording is still being made. Recording them as placeholders is what
 * makes two runs of one page give the same list.
 */
const runSpecific: readonly (readonly [RegExp, string])[] = [
  [/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/g, "<id>"],
  [
    /just now|yesterday|tomorrow|(?:last|next) (?:week|month|year)|(?:in )?\d+ (?:second|minute|hour|day|week|month|year)s?(?: ago)?/g,
    "<time>",
  ],
];

/** Every landmark and control of one accessibility snapshot, in the order the tree holds them. */
function collect(node: unknown, found: MeasuredEntry[]): void {
  if (Array.isArray(node)) {
    for (const child of node) collect(child, found);
    return;
  }
  if (typeof node !== "object" || node === null) return;
  const { box, children, name, role } = node as AriaSnapshotNode;
  if (typeof role === "string" && inventoryRoles.has(role)) {
    const announced =
      typeof name === "string"
        ? runSpecific.reduce(
            (text, [carried, placeholder]) => text.replace(carried, placeholder),
            name
          )
        : "";
    found.push({ box: readBox(box), name: announced, role });
  }
  collect(children, found);
}

/** The key two entries are the same control under: its role and its name. */
function entryKey(entry: InventoryEntry): string {
  return `${entry.role}\u0000${entry.name}`;
}

/** How an entry is written where a person reads it: in a failure, and as both halves of the
 *  fixture's rename table, so a rename is pasted straight from the failure that named it. */
function describe(entry: InventoryEntry): string {
  return `${entry.role} "${entry.name}"`;
}

/** The entry `described` names, read back from the form `describe` writes. A value that is not
 *  that form names no entry, and the caller fails rather than quietly looking for nothing. */
function entryOf(described: string): InventoryEntry | undefined {
  const opened = described.indexOf(' "');
  if (opened <= 0 || !described.endsWith('"')) return undefined;
  return { name: described.slice(opened + 2, -1), role: described.slice(0, opened) };
}

/**
 * One list out of however many views were recorded: every `{role, name}` once, ordered by role
 * and then by name, so the view order, the DOM order and two runs of the same page all give the
 * same list.
 */
export function mergeInventory(entries: readonly InventoryEntry[]): InventoryEntry[] {
  const byKey = new Map<string, InventoryEntry>();
  for (const entry of entries) {
    byKey.set(entryKey(entry), { name: entry.name, role: entry.role });
  }
  return [...byKey.values()].sort(
    (a, b) => a.role.localeCompare(b.role) || a.name.localeCompare(b.name)
  );
}

/** How long one reading waits before it is taken again, and how long the page has to stop. */
const SETTLE_INTERVAL_MS = 400;

const SETTLE_TIMEOUT_MS = 20_000;

/** Every landmark and every control the page offers, de-duplicated and ordered, once the page has
 *  stopped changing them. */
export async function recordInventory(page: Page): Promise<InventoryEntry[]> {
  return mergeInventory(await settled(() => measured(page.ariaSnapshotJSON({ boxes: true }))));
}

/** The same walk over one part of the page, which is how a view that keeps its other panels
 *  `hidden` records what one panel of it offers. */
export async function recordScopeInventory(scope: Locator): Promise<InventoryEntry[]> {
  return mergeInventory(await settled(() => measured(scope.ariaSnapshotJSON({ boxes: true }))));
}

/**
 * One reading of `read` once two readings `SETTLE_INTERVAL_MS` apart agree. A panel still filling
 * - a reference chip that arrives with its own read, a card the list mounts late - is on the page
 * after the network has gone quiet, so a single reading records a half-drawn page and two runs of
 * it differ. A page that never stops changing fails here rather than being recorded mid-render.
 */
async function settled(read: () => Promise<MeasuredEntry[]>): Promise<MeasuredEntry[]> {
  const deadline = Date.now() + SETTLE_TIMEOUT_MS;
  let before = mergeInventory(await read())
    .map(describe)
    .join("\n");
  for (;;) {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, SETTLE_INTERVAL_MS);
    await promise;
    const entries = await read();
    const after = mergeInventory(entries).map(describe).join("\n");
    if (after === before) return entries;
    expect(
      Date.now() <= deadline,
      `what the page offers kept changing for ${SETTLE_TIMEOUT_MS} ms; it last went from\n${before}\nto\n${after}`
    ).toBe(true);
    before = after;
  }
}

async function measured(snapshot: Promise<unknown>): Promise<MeasuredEntry[]> {
  const found: MeasuredEntry[] = [];
  collect(await snapshot, found);
  return found;
}

/**
 * One link of the old page from a node to another node, which the new page carries by drawing
 * the target as that node's parent row, the row the left arrow reaches from it - the one form of
 * carrying an entry that is no landmark and no control of its own. A decision's card linked the
 * ask it answers; the outline draws the decision under that ask instead (R4), so the link's job
 * is done by the tree itself, and the entry is carried exactly when the tree does it.
 */
export interface ParentRowMapping {
  /** Words of the node the old link sat on, which its row holds. */
  readonly node: string;
  /** Words of the node the old link pointed at, which the node's parent row holds. */
  readonly parent: string;
  /** Why the parent row stands in for the link. */
  readonly reason: string;
}

export interface CarriedOptions {
  /**
   * The fixture's rename table: each recorded entry, written as the `role "name"` a failure
   * prints it as, mapped to the entry the page offers in its place, in the same form. One form
   * serves a name that changed and a role that changed with it - `tab "Children"` became
   * `button "Children"` when the tabs became presets - and a rename is pasted straight from
   * the failure that named it. Where the page offers a different entry at a different width -
   * the preset row is a row of tabs on a desktop and one menu on a phone - the value names
   * each of them, joined by ` | `, and the entry is carried while any one of them is there. A
   * renamed entry is looked for as the entries it maps to and nothing else, so a rename is not
   * reported as a loss; a value that is not that form fails the check rather than quietly
   * matching nothing.
   */
  readonly renames: Readonly<Record<string, string>>;
  /** Recorded entries, in the same `role "name"` form, that only a part not yet on the page
   *  carries (`AWAITING_THE_BOX`): none is looked for, and one the page offers fails the check,
   *  since that part's own pull request is what moves it back into the reading. */
  readonly awaiting?: readonly string[];
  /**
   * The fixture's links carried by a parent row (`ParentRowMapping`), keyed by the recorded
   * entry in the same `role "name"` form. Such an entry is looked for in the outline's
   * `treeitem` rows and nowhere else: it is carried when a row holding the node's words is drawn
   * under a row holding the target's words, one level up, and the left arrow from that row
   * reaches that parent. Every other entry is read as a landmark or a control, as above.
   */
  readonly parentRows?: Readonly<Record<string, ParentRowMapping>>;
  /** Puts the page where the outline draws the rows a parent-row mapping names, both of them:
   *  the walk's own views leave the page wherever the last one did. */
  readonly outline?: (mapping: ParentRowMapping) => Promise<void>;
  /** Which recorded state this is (`open-1440`), named in every failure. */
  readonly state: string;
  /**
   * The page's own views, each a call that puts the page in one of them and answers whether
   * that view is there. The recording is the merge of the views the old page had - its nine
   * tab panels, one at a time, because a page renders one and hides the rest
   * (`record-page-inventory.e2e.ts`) - so the page it is read against is merged the same way,
   * or a page that still offers everything would be read as having lost whatever its first
   * view does not show. Each view is read in turn and the readings merged, and every one of
   * them passes `assertPageFits` on its own.
   *
   * A view the page does not have at this width - the eighth row of a tree that draws five, a
   * control a wider page never folds away - answers `false` and leaves the page exactly as the
   * view before it left it, and the walk reads nothing for it: a reading there would record the
   * view before it a second time, under its own settle and its own fit. So a walk may name
   * more views than a width has without paying for the ones it does not, and a walk that
   * reaches no view at all fails the check below for want of anything to merge.
   */
  readonly views: readonly (() => Promise<boolean>)[];
}

/** How many presses of the left arrow reach a parent row: a row that holds rows of its own and
 *  is open folds on the first press, and moves to its parent on the second. */
const LEFT_ARROW_PRESSES = 2;

/**
 * Whether the outline carries one parent-row mapping: a `treeitem` holding the node's words,
 * drawn one level under the nearest `treeitem` above it that holds the target's words, from
 * which the left arrow moves the focus to that parent. Undefined where it does, and otherwise
 * what the rows showed instead.
 */
async function parentRowMissing(
  page: Page,
  mapping: ParentRowMapping
): Promise<string | undefined> {
  const rows = page.getByRole("treeitem");
  const drawn = await rows.evaluateAll((items) =>
    items.map((item) => ({
      level: Number(item.getAttribute("aria-level")),
      text: (item.textContent ?? "").replace(/\s+/g, " "),
    }))
  );
  const placements = drawn.flatMap((row, at) => {
    if (!row.text.includes(mapping.node)) return [];
    let parent = at - 1;
    while (parent >= 0 && drawn[parent]?.level !== row.level - 1) parent -= 1;
    return [{ at, parent }];
  });
  if (placements.length === 0) return `no row of the outline holds "${mapping.node}"`;
  const under = placements.find(
    ({ parent }) => drawn[parent]?.text.includes(mapping.parent) === true
  );
  if (under === undefined) {
    const parents = placements.map(({ parent }) => drawn[parent]?.text ?? "the outline's top");
    return `"${mapping.node}" is drawn under ${JSON.stringify(parents)}, not under "${mapping.parent}"`;
  }
  await rows.nth(under.at).focus();
  for (let press = 0; press < LEFT_ARROW_PRESSES; press += 1) {
    await page.keyboard.press("ArrowLeft");
    const reached = await rows
      .nth(under.parent)
      .evaluate((row) => row === document.activeElement || row.contains(document.activeElement));
    if (reached) return undefined;
  }
  return `the left arrow from "${mapping.node}" does not reach its parent row "${mapping.parent}"`;
}

/**
 * Nothing recorded was lost: every recorded `{role, name}`, under the name the rename table gives
 * it, is a landmark or control of the page as it stands, and a reader reaches each of them
 * without scrolling sideways - the page is no wider than its viewport (`assertPageFits`) and
 * every instance of the entry sits inside the viewport's own width, so nothing is reached by
 * scrolling a row or a card sideways.
 *
 * What a state lost fails the row softly (`expect.soft`): the row goes on to the next state it
 * reads and fails at its end, so one run names the losses of every state rather than those of
 * the first state that lost anything.
 *
 * It asserts nothing about the redesign: a page may offer more than it did, in any order.
 */
export async function assertInventoryCarried(
  page: Page,
  recorded: readonly InventoryEntry[],
  options: CarriedOptions
): Promise<void> {
  // One row per recorded entry, each naming every entry that carries it: itself, or whatever
  // the rename table says stands in its place. An entry a parent row carries is read off the
  // outline's rows below, and is no row of this list.
  const seen = new Set<string>();
  const wanted: { carried: InventoryEntry[]; described: string; instances: MeasuredEntry[] }[] = [];
  const byParentRow: { described: string; mapping: ParentRowMapping }[] = [];
  for (const entry of recorded) {
    const described = describe(entry);
    if (seen.has(described)) continue;
    seen.add(described);
    if (options.awaiting?.includes(described) === true) continue;
    const mapping = options.parentRows?.[described];
    if (mapping !== undefined) {
      byParentRow.push({ described, mapping });
      continue;
    }
    const renamed = options.renames[described];
    const carried =
      renamed === undefined ? [entry] : renamed.split(" | ").map((one) => entryOf(one));
    expect(
      carried.every((one) => one !== undefined),
      `${options.state}: the rename of ${described} reads ${JSON.stringify(renamed)}, which is no \`role "name"\``
    ).toBe(true);
    wanted.push({
      carried: carried.filter((one): one is InventoryEntry => one !== undefined),
      described,
      instances: [],
    });
  }
  const present = new Map<string, MeasuredEntry[]>();
  let read = 0;
  for (const view of options.views) {
    if ((await view()) === false) continue;
    await assertPageFits(page);
    read += 1;
    for (const entry of await settled(() => measured(page.ariaSnapshotJSON({ boxes: true })))) {
      const key = entryKey(entry);
      present.set(key, [...(present.get(key) ?? []), entry]);
    }
  }
  expect(
    read,
    `${options.state}: every one of the ${options.views.length} views answered that it is not there, so nothing was read`
  ).toBeGreaterThan(0);
  for (const one of wanted) {
    one.instances = one.carried.flatMap((entry) => present.get(entryKey(entry)) ?? []);
  }
  const lost = wanted.filter((one) => one.instances.length === 0).map((one) => one.described);
  // Each link a parent row carries, read once the views are done: the left arrow moves the
  // focus and may fold a row, which no view's own reading should meet.
  for (const { described, mapping } of byParentRow) {
    expect(
      options.outline,
      `${options.state}: ${described} is carried by a parent row, and the check names no way to the outline`
    ).toBeDefined();
    await options.outline?.(mapping);
    const missing = await parentRowMissing(page, mapping);
    if (missing !== undefined) lost.push(`${described} (${missing})`);
  }
  expect.soft(lost, `${options.state} no longer offers: ${lost.join(", ")}`).toEqual([]);
  const early = (options.awaiting ?? []).filter((described) => {
    const entry = entryOf(described);
    return entry !== undefined && (present.get(entryKey(entry)) ?? []).length > 0;
  });
  expect
    .soft(early, `${options.state} offers what only part L's box carries: ${early.join(", ")}`)
    .toEqual([]);

  const viewport = await page.evaluate(() => document.documentElement.clientWidth);
  const sideways = wanted
    .filter((one) => {
      // A control a page hides from sight and keeps for the keyboard - the shell's "Skip to
      // content", which is `sr-only` until it is focused - is a 1 by 1 clipped box wherever it
      // sits. It is reached by Tab and not by scrolling, so its box says nothing about
      // scrolling sideways and the rule passes over it.
      const placed = one.instances.filter(
        (instance) =>
          instance.box !== undefined && instance.box.width > 1 && instance.box.height > 1
      );
      return (
        placed.length > 0 &&
        placed.every(({ box }) => box !== undefined && (box.x < 0 || box.x + box.width > viewport))
      );
    })
    .map((one) => one.described);
  expect(
    sideways,
    `${options.state} reaches these only by scrolling sideways in a ${viewport}px viewport: ${sideways.join(", ")}`
  ).toEqual([]);
}

/**
 * The rename table of the inventory row (part N's R7): each entry today's page offered, written
 * as the `role "name"` a failure prints it as, mapped to what the new page offers in its place,
 * from the Design's table "Where each thing goes" and ruling 5. The checkpoint kept it in the
 * fixture, which the recorder writes again after each merge of `main`; declared here, a new
 * recording does not lose it.
 */
export const INVENTORY_RENAMES: Readonly<Record<string, string>> = {
  'tab "Spec"': 'button "Spec" | menuitem "Spec"',
  'tab "Verification"': 'button "Verification" | menuitem "Verification"',
  'tab "Plan"': 'button "Plan" | menuitem "Plan"',
  'tab "Conversation"': 'region "Conversation"',
  'tab "Children"': 'button "Children"',
  'tab "Artifacts 7"': 'button "Files and documents"',
  'tab "Activity"': 'region "Activity"',
  'tab "Decisions"': 'button "Decisions" | menuitem "Decisions"',
  'tab "Collaborators"': 'button "Collaborators"',
  'button "Reopen issue"': 'button "Reopen into Backlog"',
  'button "Upload"': 'menuitem "Upload"',
  'button "Upload artifact"': 'button "Upload a file"',
  'searchbox "Filter artifacts"': 'searchbox "Filter files and documents"',
  'button "New document"': 'menuitem "New document"',
  'link "Block r1"': 'link "Every property of the issue that carries a value is visible …"',
  'link "Block r2"': 'link "Requirement 2 of the redesign stands on a row of its own."',
  'link "Block r3"': 'link "Requirement 3 of the redesign stands on a row of its own."',
  'link "Block r4"': 'link "Requirement 4 of the redesign stands on a row of its own."',
  'link "Block r5"': 'link "Requirement 5 of the redesign stands on a row of its own."',
  'region "Open questions"': 'region "Decisions"',
  'region "Waiting for your review"': 'region "Decisions"',
  'region "Your decisions"': 'region "Decisions"',
  'region "Replaced"': 'region "Decisions"',
  'link "session:e2e-seed…"': 'link "Recorder (issue layout)"',
  'button "Hide margin"': 'button "Close"',
  'button "Hide sidebar"': 'button "Open navigation"',
  'complementary "Navigation"': 'navigation "App"',
  'banner ""': 'navigation "Navigation"',
  'button "Open review panel (1 open ask)"': 'button "Details"',
  'button "Open review panel (0 open asks)"': 'button "Details"',
  'complementary "Review panel"': 'complementary "Review margin"',
};

/** Why a rename is the same control under a new name, keyed as `INVENTORY_RENAMES` is, for a
 *  rename that no view-to-control or block-to-words rule explains on its own. */
export const INVENTORY_RENAME_REASONS: Readonly<Record<string, string>> = {
  'button "Hide margin"':
    "The margin's collapse is Details' Close now: the panel opens over the right of the page as a drawer and closing it is its collapse (R14), the same control under the drawer's word.",
  'button "Hide sidebar"':
    "On the issue page the sidebar is the 56 px icon rail (R14), already collapsed; its one control over the navigation is the rail's foot, Open navigation, which opens the whole list over the page, or the header's on a phone.",
  'complementary "Navigation"':
    "The sidebar's landmark on the issue page is the icon rail's (R14), a navigation landmark named App so it is not taken for the whole navigation the rail's foot opens.",
  'banner ""':
    "On a phone the shell's bar was the issue page's banner, holding Dispatch, the inbox count and Menu; on the issue page the header's `☰` opens the navigation that holds them (R14), so the bar is that navigation now.",
  'button "Open review panel (1 open ask)"':
    "The phone's control that opened the panel is the header's Details control (R14); the panel it opens is the same.",
  'button "Open review panel (0 open asks)"':
    "As above, on the closed issue, which waits on no ask.",
  'complementary "Review panel"':
    "The phone's review sheet is the margin inside Details, which on the issue page is one box at every width and named as the margin is (R14); Details opens it over the whole screen.",
};

/** Links of today's page that a parent row of the outline carries (`ParentRowMapping`), keyed
 *  as `INVENTORY_RENAMES` is. */
export const INVENTORY_PARENT_ROWS: Readonly<Record<string, ParentRowMapping>> = {
  'link "The ask it answers"': {
    node: "Keep every address the old page answered",
    parent: "Which addresses must the new page still answer?",
    reason:
      "A decision's card linked the ask it answers; the outline draws the decision under that ask, its parent row, which the left arrow reaches (R4).",
  },
};

/**
 * What only part L's box carries (part N's R11): today's composer, as the fixture recorded it in
 * every state. While pull request 3125 and part L have not merged, `slots/foot.tsx` draws
 * nothing, so the inventory row looks for none of these and fails if the page offers one. Part
 * L's pull request empties this list, and the row then reads each of them again.
 */
export const AWAITING_THE_BOX: readonly string[] = [
  'form "Comment composer"',
  'textbox "Comment"',
  'button "Send"',
  'button "Messages default to Planner (issue layout)"',
];
