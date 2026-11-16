// The views demo's screenshot run (README.md beside this file): against a running demo, it opens
// each page the design shows and writes one PNG per picture, named by what it shows, then fails
// if a page never drew, the stub left any request unanswered, the page threw, or a check failed.
//
//   bun e2e/views-demo/shots.ts --base http://127.0.0.1:5190 --out <dir> \
//     [--before | --file] [--chromium <path>] [--project <KEY>] [--only <name part>,…]
//
// --before takes the pictures of the page before the design (the List, the Board and the Legion
// view of cybermonklabs/legion#2731). --file opens the single file (--base file:///….html) with
// the network off, and clicks through it: a view tab, the Board, a drag on the Board, Save view
// and its delete, the Columns menu and the Closed filter. With neither, it takes the design's
// pictures.
import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { parseArgs } from "node:util";
import { type Browser, type CDPSession, chromium, type Locator, type Page } from "@playwright/test";

const { values: flags } = parseArgs({
  options: {
    base: { type: "string", default: "http://127.0.0.1:5190" },
    out: { type: "string" },
    before: { type: "boolean", default: false },
    file: { type: "boolean", default: false },
    chromium: { type: "string" },
    project: { type: "string", default: "INFRA" },
    // A real project with no Legion daemon in the fixtures (fixtures.ts --no-daemon-project).
    "no-daemon-project": { type: "string", default: "GTM" },
    only: { type: "string" },
    // The done issue whose empty Work time cell done-work-time-empty-1440 shows: one with Legion
    // facts and no measured work. The newest closed of them by default.
    "empty-work-time": { type: "string" },
    // The report's file name in --out, `report-<mode>.json` by default.
    report: { type: "string" },
  },
});
if (flags.out === undefined) throw new Error("name --out <dir>");
const out = flags.out;
const base = flags.base;
const project = flags.project;
const issuesPath = `/projects/${project}/issues`;
const mode = flags.before ? "before" : flags.file ? "file" : "after";

const DESKTOP = { width: 1440, height: 900 };
const LAPTOP = { width: 1024, height: 768 };
const PHONE = { width: 390, height: 844 };
/** The page's time zone and language, the same on every machine, so a time of day and a date
 *  read the same in every run (the README runs it with TZ=UTC as well). */
const TIME_ZONE = "UTC";
const LOCALE = "en-US";

/** One picture: its file name, the window it is taken in, how to reach what it shows, and
 *  whether it is the whole page rather than the window, or one element of it alone (`subject`, a
 *  CSS selector), drawn at twice the scale to be read close up. */
interface Shot {
  name: string;
  viewport: { width: number; height: number };
  take: (page: Page) => Promise<void>;
  fullPage?: boolean;
  subject?: string;
}

interface Report {
  /** What the pictures were drawn with: the browser, its time zone and language, and every
   *  font the browser drew the pictures' text in, with how many glyphs. */
  browser: { version: string; time_zone: string; locale: string };
  fonts: Record<string, number>;
  shots: string[];
  unanswered: string[];
  errors: string[];
  checks: string[];
}

const report: Report = {
  browser: { version: "", time_zone: TIME_ZONE, locale: LOCALE },
  fonts: {},
  shots: [],
  unanswered: [],
  errors: [],
  checks: [],
};

/** The address of `path`: the demo's own, or the single file's fragment, which its router reads
 *  (web/src/demo/file-router.ts). */
function address(path: string): string {
  return base.endsWith(".html") ? `${base}#${path}` : `${base}${path}`;
}

/** What a page of issues draws once its rows are in: a table row, a Board card or, before the
 *  design, a Legion view card. */
const ROWS = "[data-issue-row], [data-board-card], [data-legion-card]";

/** An Issues page, by the dev server's address or the single file's fragment. */
const ISSUES_PAGE = /\/projects\/[^/?#]+\/issues(?:[?#]|$)/;

/** Opens `path` and waits until the page has drawn what `ready` names, its rows by default. */
async function open(page: Page, path: string, ready?: Locator): Promise<void> {
  await page.goto(address(path));
  await settle(page, ready);
}

/** Waits for `ready`, the page's rows by default, and then for the drawing to settle. A page
 *  that never draws them fails its picture rather than leaving a loading state in it. The
 *  design's Issues page draws its view tabs first, which the page before the design has none
 *  of: a run of the design's pictures against that page fails at its first picture. */
async function settle(page: Page, ready?: Locator): Promise<void> {
  await page.waitForLoadState("networkidle").catch(() => undefined);
  if (mode !== "before" && ISSUES_PAGE.test(page.url())) {
    await page.locator("[data-view-tab]").first().waitFor({ timeout: 30_000 });
  }
  await (ready ?? page.locator(ROWS).first()).waitFor({ timeout: 30_000 });
  await page.waitForTimeout(1_200);
}

/** The fonts the browser drew the page's text in, from its own record of each text node
 *  (`CSS.getPlatformFontsForNode`), over the elements that hold the page's words. */
const FONT_SAMPLE =
  "h1, h2, [data-view-tab], thead th, [data-issue-row] td, [data-board-card], button, input, [data-testid='views-demo-banner'] div, [data-legion-settings] td";
async function recordFonts(cdp: CDPSession): Promise<void> {
  const { root } = await cdp.send("DOM.getDocument", { depth: 0 });
  const { nodeIds } = await cdp.send("DOM.querySelectorAll", {
    nodeId: root.nodeId,
    selector: FONT_SAMPLE,
  });
  for (const nodeId of nodeIds.slice(0, 80)) {
    const { fonts } = await cdp.send("CSS.getPlatformFontsForNode", { nodeId });
    for (const font of fonts) {
      report.fonts[font.familyName] = (report.fonts[font.familyName] ?? 0) + font.glyphCount;
    }
  }
}

function check(passed: boolean, what: string): void {
  if (passed) report.checks.push(`ok: ${what}`);
  else report.errors.push(`check failed: ${what}`);
}

const view = (id: string, extra = "", key = project) =>
  `/projects/${key}/issues?view=${id}${extra}`;
const tab = (page: Page, id: string) => page.locator(`[data-view-tab="${id}"]`);

/** Opens Open with `status=triage` added, so the view is changed to the triage issues, and opens
 *  Save view with "Triage" typed. */
async function openSaveView(page: Page): Promise<void> {
  await open(page, view("open", "&status=triage"));
  await page.getByRole("button", { name: "Save view" }).click();
  await page.getByRole("textbox", { name: "Name" }).fill("Triage");
}

/** The count a view's tab draws, from its accessible name (`Done 596`). */
async function tabCount(page: Page, id: string): Promise<number> {
  const label = (await tab(page, id).getAttribute("aria-label")) ?? "";
  return Number(/ (\d+)$/.exec(label)?.[1] ?? Number.NaN);
}

/** `/priorities` once its three tables have drawn their rows: the priorities and the two
 *  faults. The design names them "Active under no priority" and "Parked priority work"; main,
 *  where the before pictures are taken, "In flight under no priority" and "Priority work in
 *  backlog or icebox". */
async function openPriorities(page: Page): Promise<void> {
  await open(
    page,
    "/priorities",
    prioritySection(page, "Active under no priority", "In flight under no priority")
  );
  await prioritySection(page, "Parked priority work", "Priority work in backlog or icebox").waitFor(
    { timeout: 30_000 }
  );
  await prioritySection(page, "Priorities").waitFor({ timeout: 30_000 });
}

/** The first priority's own page (P1), reached by its row's link, once its issue table has
 *  drawn. */
async function openPriority(page: Page): Promise<void> {
  await openPriorities(page);
  await page.locator('section[aria-label="Priorities"] a[href*="/priorities/"]').first().click();
  await settle(page, prioritySection(page, "Work advancing this priority"));
}

/** The first row of the section one of the labels names. */
const prioritySection = (page: Page, ...labels: string[]) =>
  page
    .locator(labels.map((label) => `section[aria-label="${label}"] [data-issue-row]`).join(", "))
    .first();

const beforeShots: Shot[] = [
  { name: "before-list-1440", viewport: DESKTOP, take: (page) => open(page, issuesPath) },
  {
    name: "before-board-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, issuesPath);
      await page.getByRole("button", { name: "Board", exact: true }).click();
      await settle(page, page.locator("[data-board-card]").first());
    },
  },
  {
    name: "before-legion-view-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, issuesPath);
      await page.getByRole("button", { name: "Legion", exact: true }).click();
      await settle(page, page.locator("[data-legion-card]").first());
    },
  },
  { name: "before-list-390", viewport: PHONE, take: (page) => open(page, issuesPath) },
  // The priority pages as main serves them (cybermonklabs/legion#3767), taken on a checkout of
  // main rather than of the base above, which has none.
  { name: "before-priorities-1440", viewport: DESKTOP, fullPage: true, take: openPriorities },
  { name: "before-priority-page-1440", viewport: DESKTOP, fullPage: true, take: openPriority },
];

/** Done with Time per phase drawn in Hours. */
async function openDoneInHours(page: Page): Promise<void> {
  await open(page, view("done"));
  await page
    .getByRole("group", { name: "Time per phase as" })
    .getByRole("button", { name: "Hours" })
    .click();
}

/** Whether the bar's legend draws a scroll bar: a box of the bar, or one around it short of the
 *  box that also holds the table, holding more than it shows and scrolling to it. The legend
 *  should wrap onto as many lines as it needs instead. */
async function legendScrolls(page: Page): Promise<boolean> {
  return page.locator("[data-view-bar]").evaluate((bar) => {
    const boxes: Element[] = [bar, ...bar.querySelectorAll("*")];
    for (let up = bar.parentElement; up !== null; up = up.parentElement) {
      if (up.querySelector("table, [data-board-by]") !== null) break;
      boxes.push(up);
    }
    return boxes.some((box) => {
      const style = getComputedStyle(box);
      return (
        (/auto|scroll/.test(style.overflowX) && box.scrollWidth > box.clientWidth) ||
        (/auto|scroll/.test(style.overflowY) && box.scrollHeight > box.clientHeight)
      );
    });
  });
}

/** The headers the table draws cut: a label holding more than it shows, or one running past its
 *  header, among the headers wholly inside the table's visible part and clear of its pinned
 *  columns. A header the table scrolls to is not cut. */
async function cutHeaders(page: Page): Promise<string[]> {
  return page.getByTestId("issues-table-scroll").evaluate((box) => {
    const edge = box.getBoundingClientRect();
    const headers = [...box.querySelectorAll("thead th")];
    const pinned = (th: Element) => getComputedStyle(th).position === "sticky";
    const pinnedRight = Math.max(
      edge.left,
      ...headers.filter(pinned).map((th) => th.getBoundingClientRect().right)
    );
    return headers.flatMap((th) => {
      const own = th.getBoundingClientRect();
      if (own.right > edge.right + 1 || (!pinned(th) && own.left < pinnedRight - 1)) return [];
      const cut = [...th.querySelectorAll("*")].some((part) => {
        if (part.children.length > 0 && part.tagName !== "svg") return false;
        const drawn = part.getBoundingClientRect();
        if (drawn.width === 0) return false;
        const clips = getComputedStyle(part).overflowX !== "visible";
        return (clips && part.scrollWidth > part.clientWidth + 1) || drawn.right > own.right + 1;
      });
      return cut ? [th.textContent?.trim() ?? ""] : [];
    });
  });
}

async function checkHeaders(page: Page, name: string): Promise<void> {
  const cut = await cutHeaders(page);
  check(
    cut.length === 0,
    cut.length === 0 ? `${name}: no header is cut` : `${name}: cut headers ${cut.join(", ")}`
  );
}

/** The choices a column's filter offers, as each reads aloud: "With open asks, 13 issues". */
async function filterChoices(page: Page, column: string): Promise<string[]> {
  return page
    .getByRole("listbox", { name: `${column} options` })
    .getByRole("option")
    .evaluateAll((options) => options.map((option) => option.getAttribute("aria-label") ?? ""));
}

/** Ticks a column in the Columns menu and closes the menu. */
async function showColumn(page: Page, column: string): Promise<void> {
  await page.getByRole("button", { name: /^Columns/ }).click();
  await page.getByRole("checkbox", { name: column, exact: true }).check();
  await page.keyboard.press("Escape");
  await page.waitForTimeout(500);
}

/** Swipes the table to its right end, as a reader would, and gives how far it scrolled. */
async function swipeToEnd(page: Page): Promise<number> {
  const scrolled = await page.getByTestId("issues-table-scroll").evaluate((box) => {
    box.scrollLeft = box.scrollWidth;
    return box.scrollLeft;
  });
  await page.waitForTimeout(500);
  return scrolled;
}

/** What a Board card draws badly: a Workers fact reading None, where no worker runs on the
 *  issue and the card should leave the fact out, or a claimer's name squeezed to its dot. */
async function cardFaults(page: Page): Promise<string[]> {
  return page.locator("[data-board-card]").evaluateAll((cards) =>
    cards.flatMap((card) => {
      const key = card.getAttribute("data-board-card") ?? "a card";
      const out: string[] = [];
      const roles = card.querySelector<HTMLElement>('[data-legion-fact="roles"]');
      if (roles !== null && /None\s*$/.test(roles.innerText)) out.push(`${key} reads Workers None`);
      for (const name of card.querySelectorAll('[data-testid="issue-claim"] [data-agent-name]')) {
        const drawn = Math.round(name.getBoundingClientRect().width);
        if (drawn < 24) out.push(`${key} draws its claimer's name in ${drawn} px`);
      }
      return out;
    })
  );
}

async function checkCards(page: Page, name: string): Promise<void> {
  const drawn = await page.locator("[data-board-card]").count();
  const faults = await cardFaults(page);
  check(
    faults.length === 0,
    faults.length === 0
      ? `${name}: all ${drawn} cards leave out an empty Workers and draw their claimer's name`
      : `${name}: ${faults.length} of ${drawn} cards read badly (${faults.slice(0, 3).join("; ")})`
  );
}

/** Where a column's header stands against the table's visible part, with the table unscrolled:
 *  whether it is whole there, and how far the table scrolls sideways in all. */
async function onFirstScreen(
  page: Page,
  column: string
): Promise<{ whole: boolean; scrolled: number; scrolls: number }> {
  return page.getByTestId("issues-table-scroll").evaluate((box, name) => {
    const edge = box.getBoundingClientRect();
    const header = [...box.querySelectorAll("thead th")].find((th) =>
      th.textContent?.trim().startsWith(name)
    );
    const own = header?.getBoundingClientRect();
    return {
      whole: own !== undefined && own.left >= edge.left - 1 && own.right <= edge.right + 1,
      scrolled: box.scrollLeft,
      scrolls: box.scrollWidth - box.clientWidth,
    };
  }, column);
}

/** Checks that each named column's header stands whole on a view's first screen, the table
 *  unscrolled. */
async function checkOnFirstScreen(
  page: Page,
  name: string,
  view: string,
  columns: string[]
): Promise<void> {
  for (const column of columns) {
    const { whole, scrolled, scrolls } = await onFirstScreen(page, column);
    check(
      whole && scrolled === 0,
      `${name}: ${column} stands whole on ${view}'s first screen (scrollLeft ${scrolled}; the table scrolls ${scrolls} px in all)`
    );
  }
}

/** A change made elsewhere: another reader's write, which reaches this page only as the live
 *  event the stub publishes for every write it takes. It goes round the page's own write store,
 *  so the page holds it as it holds anyone else's. */
async function changeElsewhere(
  page: Page,
  key: string,
  patch: Record<string, unknown>
): Promise<void> {
  const status = await page.evaluate(
    async ([issue, body]) =>
      (
        await fetch(`/api/v1/issues/${issue}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        })
      ).status,
    [key, patch] as const
  );
  if (status !== 200) throw new Error(`the stub answered ${status} to the change of ${key}`);
}

/** The keys of the first `count` rows the table draws, top down. */
const firstRows = (page: Page, count = 3) =>
  page
    .locator("[data-issue-row]")
    .evaluateAll(
      (rows, n) => rows.slice(0, n).map((row) => row.getAttribute("data-issue-row") ?? ""),
      count
    );

/** The keys of the first `count` cards a Board column draws, top down. */
const firstCards = (page: Page, column: string, count = 3) =>
  page
    .locator(`[data-board-column="${column}"] [data-board-card]`)
    .evaluateAll(
      (cards, n) => cards.slice(0, n).map((card) => card.getAttribute("data-board-card") ?? ""),
      count
    );

/** The count a Board column's header draws. */
async function columnCount(page: Page, column: string): Promise<number> {
  const header = await page
    .locator(`[data-board-column="${column}"] [data-testid="board-column-header"]`)
    .first()
    .innerText();
  return Number(/(\d+)\s*$/.exec(header)?.[1] ?? Number.NaN);
}

/** The "Show N changes" pill a held table or Board draws while it holds a change back. */
const heldButton = (page: Page) => page.locator("[data-held-changes]");

type Layout = "table" | "board";

/** The header of the rows, and the first row or card, in a layout. */
const HEADER: Record<Layout, string> = {
  table: '[data-testid="issues-table-scroll"] thead',
  board: '[data-testid="board-column-header"]',
};
const FIRST: Record<Layout, string> = {
  table: "[data-issue-row]",
  board: "[data-board-card]",
};
const LIST: Record<Layout, string> = {
  table: '[data-testid="issues-table-scroll"]',
  board: "[data-board-by]",
};

/** Where the header and the first row or card stand, in px from the window's top. */
interface Tops {
  header: number;
  first: number;
}

async function topsOf(page: Page, layout: Layout): Promise<Tops> {
  return page.evaluate(
    ([header, first]) => {
      const top = (selector: string) => {
        const element = document.querySelector(selector);
        return element === null ? Number.NaN : Math.round(element.getBoundingClientRect().top);
      };
      return { header: top(header), first: top(first) };
    },
    [HEADER[layout], FIRST[layout]] as const
  );
}

/** Checks that the header and the first row or card stood still at every moment named: the
 *  pill takes no room, so nothing the reader sees moves as it comes and goes. */
function checkStill(name: string, layout: Layout, moments: [string, Tops][]): void {
  const [, first] = moments[0] ?? ["", { header: Number.NaN, first: Number.NaN }];
  const row = layout === "table" ? "row" : "card";
  check(
    moments.every(
      ([, tops]) =>
        Number.isFinite(tops.first) && tops.first === first.first && tops.header === first.header
    ),
    `${name}: the first ${row} stands at ${moments.map(([when, tops]) => `${tops.first} px ${when}`).join(", ")}; the header at ${moments.map(([, tops]) => tops.header).join(", ")}`
  );
}

/** Checks where the pill stands: one pill, floating over the header of the rows (the table's
 *  header or the Board's column headers), centred on the list, a small fraction of its width,
 *  and none in the pager row. */
async function checkPillPlace(page: Page, name: string, layout: Layout): Promise<void> {
  const place = await page.evaluate(
    ([headerAt, listAt]) => {
      const pills = [...document.querySelectorAll<HTMLElement>("[data-held-changes]")];
      const pill = pills[0];
      const own = pill?.getBoundingClientRect();
      const head = document.querySelector(headerAt)?.getBoundingClientRect();
      const list = document.querySelector(listAt)?.getBoundingClientRect();
      const pager = document.querySelector('select[aria-label$="per page"]')?.parentElement;
      const round = (value: number | undefined) => Math.round(value ?? Number.NaN);
      return {
        count: pills.length,
        top: round(own?.top),
        bottom: round(own?.bottom),
        width: round(own?.width),
        centre: round(own === undefined ? undefined : (own.left + own.right) / 2),
        headTop: round(head?.top),
        headBottom: round(head?.bottom),
        listWidth: round(list?.width),
        listCentre: round(list === undefined ? undefined : (list.left + list.right) / 2),
        inPager: pill !== undefined && pager?.contains(pill) === true,
      };
    },
    [HEADER[layout], LIST[layout]] as const
  );
  check(
    place.count === 1 &&
      place.top < place.headBottom &&
      place.bottom > place.headTop &&
      Math.abs(place.centre - place.listCentre) <= 8 &&
      place.width < place.listWidth / 3 &&
      !place.inPager,
    `${name}: the pill stands at ${place.top}-${place.bottom} px over the header at ${place.headTop}-${place.headBottom}, ${place.width} px wide, its centre at ${place.centre} of the list's ${place.listCentre} (${place.listWidth} px wide), ${place.inPager ? "in" : "not in"} the pager (${place.count} drawn)`
  );
}
/** Checks that every open tooltip's panel stands wholly inside the window. */
async function checkTooltipInWindow(page: Page, name: string): Promise<void> {
  const boxes = await page.locator('[role="tooltip"]').evaluateAll((tips) =>
    tips.map((tip) => {
      const panel = (tip.parentElement ?? tip).getBoundingClientRect();
      return {
        left: Math.round(panel.left),
        right: Math.round(panel.right),
        top: Math.round(panel.top),
        bottom: Math.round(panel.bottom),
      };
    })
  );
  const { width, height } = page.viewportSize() ?? { width: 0, height: 0 };
  check(
    boxes.length > 0 &&
      boxes.every(
        (box) => box.left >= 0 && box.top >= 0 && box.right <= width && box.bottom <= height
      ),
    `${name}: the tooltip stands inside the ${width} x ${height} window (${boxes.map((box) => `${box.left}-${box.right} x ${box.top}-${box.bottom}`).join("; ") || "none open"})`
  );
}

const same = (a: readonly string[], b: readonly string[]) =>
  a.length === b.length && a.every((each, at) => each === b[at]);

/** Open sorted by Priority, its first rows read, and its second row's priority set to P3
 *  elsewhere: a change that takes that row off the first page. Gives the first rows before the
 *  change, the changed key, and where the header and first row stood before the change and
 *  while the pill shows. */
async function holdOpenTable(
  page: Page
): Promise<{ before: string[]; moved: string; tops: [string, Tops][] }> {
  await open(page, view("open", "&sort=priority"));
  await page.mouse.move(1, 1);
  const before = await firstRows(page);
  const moved = before[1];
  if (moved === undefined) throw new Error("Open drew fewer than two rows");
  const unchanged = await topsOf(page, "table");
  await changeElsewhere(page, moved, { priority: 3 });
  await heldButton(page).waitFor({ timeout: 15_000 });
  await page.waitForTimeout(800);
  const shown = await topsOf(page, "table");
  return {
    before,
    moved,
    tops: [
      ["before the change", unchanged],
      ["while the pill shows", shown],
    ],
  };
}

/** Open's Board, the first Todo card moved to Backlog elsewhere. Gives Todo's first cards and
 *  the Todo and Backlog counts before the change, the moved key, and where the column headers
 *  and the first card stood before the change and while the pill shows. */
async function holdOpenBoard(page: Page): Promise<{
  before: string[];
  counts: number[];
  moved: string;
  tops: [string, Tops][];
}> {
  await open(page, view("open", "&layout=board"), page.locator("[data-board-card]").first());
  await page.mouse.move(1, 1);
  const before = await firstCards(page, "todo");
  const counts = [await columnCount(page, "todo"), await columnCount(page, "backlog")];
  const moved = before[0];
  if (moved === undefined) throw new Error("the Todo column draws no card");
  const unchanged = await topsOf(page, "board");
  await changeElsewhere(page, moved, { status: "backlog" });
  await heldButton(page).waitFor({ timeout: 15_000 });
  await page.waitForTimeout(800);
  const shown = await topsOf(page, "board");
  return {
    before,
    counts,
    moved,
    tops: [
      ["before the change", unchanged],
      ["while the pill shows", shown],
    ],
  };
}

/** Presses the pill, and gives where the header and the first row or card stand after it. */
async function pressPill(page: Page, layout: Layout): Promise<Tops> {
  await heldButton(page).click();
  await page.mouse.move(1, 1);
  await page.waitForTimeout(800);
  return topsOf(page, layout);
}

/** The Legion columns under their names since the owner's reviews, and the names they had. */
const LEGION_COLUMNS = [
  "Why it waits",
  "Time waiting",
  "Time in Legion phase",
  "Worker state",
  "Workers",
  "Review rounds",
  "Legion tree",
];
const OLD_COLUMN_NAMES = [
  "Waiting on",
  "Waiting",
  "Waited",
  "Queue state",
  "Time in queue",
  "In phase",
  "Activity",
  "Roles",
  "Rounds",
  "Tree",
];

const afterShots: Shot[] = [
  {
    name: "view-open-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      check(!(await legendScrolls(page)), "view-open-1440: the legend draws no scroll bar");
      await checkHeaders(page, "view-open-1440");
      await checkOnFirstScreen(page, "view-open-1440", "Open", ["Created by", "Open asks"]);
    },
  },
  // Open scrolled sideways to its last column, as a reader swipes the table: the pinned columns
  // stay, and the last column is drawn whole.
  {
    name: "view-open-last-column-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      const scrolled = await swipeToEnd(page);
      const last = await page.getByTestId("issues-table-scroll").evaluate((box) => {
        const header = [...box.querySelectorAll("thead th")].at(-1);
        if (header === undefined) return { name: "no column", whole: false };
        return {
          name: header.textContent?.trim() || `a column with no header, ${header.clientWidth} px`,
          whole: header.getBoundingClientRect().right <= box.getBoundingClientRect().right + 1,
        };
      });
      check(
        scrolled > 0 && last.whole,
        `Open scrolls ${scrolled} px to its last column, ${last.name}, drawn whole`
      );
      await checkHeaders(page, "view-open-last-column-1440");
    },
  },
  // Open in a 1024 x 768 window, where the sidebar folds away and the table scrolls sideways.
  {
    name: "view-open-1024",
    viewport: LAPTOP,
    take: async (page) => {
      await open(page, view("open"));
      check(!(await legendScrolls(page)), "view-open-1024: the legend draws no scroll bar");
      await checkHeaders(page, "view-open-1024");
    },
  },
  {
    name: "view-open-in-progress-1440",
    viewport: DESKTOP,
    take: (page) => open(page, view("open", "&status=in_progress")),
  },
  // Open sorted by Priority, held: another reader sets the second row to P3. Its Priority cell
  // reads P3 in place, the first rows stay where the reader saw them, and a pill floating over
  // the table's header offers "Show N changes" for the rows the change would take off the page
  // and bring onto it. The pill takes no room: the header and the first row do not move.
  {
    name: "table-held-live-change-1440",
    viewport: DESKTOP,
    take: async (page) => {
      const { before, moved, tops } = await holdOpenTable(page);
      const after = await firstRows(page);
      const shown = await heldButton(page).innerText();
      const priority = await page
        .locator(`[data-issue-row="${moved}"]`)
        .getByLabel(`Priority of ${moved}`)
        .inputValue();
      check(
        same(after, before),
        `the held table keeps its first rows ${after.join(", ")} after ${moved} is set to P3 elsewhere`
      );
      check(priority === "3", `${moved}'s Priority cell reads P${priority} in place`);
      check(/^Show \d+ changes?$/.test(shown), `the table's pill reads "${shown}"`);
      await checkPillPlace(page, "table-held-live-change-1440", "table");
      checkStill("table-held-live-change-1440", "table", tops);
    },
  },
  // The same table after the press on Show N changes: the changed row has left the first page,
  // the pill has gone, and the header and the first row stood still throughout.
  {
    name: "table-held-shown-1440",
    viewport: DESKTOP,
    take: async (page) => {
      const { before, moved, tops } = await holdOpenTable(page);
      const pressed = await pressPill(page, "table");
      const after = await firstRows(page);
      check(
        (await heldButton(page).count()) === 0 &&
          (await page.locator(`[data-issue-row="${moved}"]`).count()) === 0 &&
          after[0] === before[0] &&
          after[1] === before[2],
        `Show changes takes ${moved} off the page: the first rows read ${after.join(", ")}, and the pill has gone`
      );
      checkStill("table-held-shown-1440", "table", [...tops, ["after the press", pressed]]);
    },
  },
  // An older link to the Legion queue's tab, which is gone: it opens Open narrowed to the issues
  // in the daemon's queue (`f.queue=Queued`), in queue order.
  {
    name: "old-queue-link-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("queue"));
      const queued = await page.evaluate(async (key) => {
        const body: { header: { queue_length: number } } = await (
          await fetch(`/api/v1/projects/${key}/legion/board`)
        ).json();
        return body.header.queue_length;
      }, project);
      const count = await tabCount(page, "open");
      check(
        page.url().includes("f.queue=Queued") && !page.url().includes("view=queue"),
        "view=queue becomes f.queue=Queued in the address"
      );
      check(
        (await tab(page, "open").getAttribute("aria-selected")) === "true" && count === queued,
        `the old link opens Open on ${count} rows; the daemon's queue holds ${queued}`
      );
    },
  },
  {
    name: "view-done-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("done"));
      await checkHeaders(page, "view-done-1440");
    },
  },
  // Done with one week picked in the Closed column's date-range filter, the 3rd of November to
  // the 9th, both days included, and the filter opened again to show it.
  {
    name: "done-closed-filter-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("done"));
      const filter = page.getByRole("button", { name: /^Filter Closed/ });
      await filter.click();
      await page.getByLabel("Closed from").fill("2026-11-03");
      await page.getByLabel("Closed to").fill("2026-11-09");
      await page.getByRole("button", { name: "Apply", exact: true }).click();
      await settle(page);
      await filter.click();
      await page.waitForTimeout(500);
      check(
        page.url().includes("f.closed=2026-11-03..2026-11-09"),
        "the picked week writes f.closed=2026-11-03..2026-11-09"
      );
      // The page runs in UTC, so the week's days are UTC days, the 9th whole.
      const expected = await page.evaluate(async (key) => {
        const rows: unknown[] = await (
          await fetch(
            `/api/v1/issues?project=${key}&status=done&closed_since=2026-11-03T00:00:00Z&closed_before=2026-11-10T00:00:00Z`
          )
        ).json();
        return rows.length;
      }, project);
      const count = await tabCount(page, "done");
      check(
        count === expected,
        `the Done tab reads ${count} for the week; the stub's rows closed from the 3rd through the 9th say ${expected}`
      );
    },
  },
  // Done in Hours. Done scrolls sideways at 1440, and Time per phase stands on its first screen.
  {
    name: "done-time-per-phase-hours-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openDoneInHours(page);
      await page.waitForTimeout(500);
      await checkOnFirstScreen(page, "done-time-per-phase-hours-1440", "Done", ["Time per phase"]);
    },
  },
  // The pointer on the first row's Time per phase cell, its tooltip open. The pointer moves to
  // the cell's centre as a person's would, rather than through `hover()`, which would scroll the
  // cell into view itself.
  {
    name: "done-time-per-phase-tooltip-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("done"));
      await checkOnFirstScreen(page, "done-time-per-phase-tooltip-1440", "Done", [
        "Time per phase",
      ]);
      const cell = page.locator("[data-issue-row]").first().locator("[data-phase-hours]");
      const box = await cell.boundingBox();
      if (box === null) throw new Error("the first row's Time per phase cell is not drawn");
      await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
      await page.waitForTimeout(500);
      const lines = await page.locator("[data-phase-hours-line]").count();
      check(lines > 0, `the Time per phase tooltip names ${lines} phases`);
      const scrolled = await page
        .getByTestId("issues-table-scroll")
        .evaluate((table) => table.scrollLeft);
      check(
        scrolled === 0,
        `the table stands unscrolled under the tooltip (scrollLeft ${scrolled})`
      );
      await checkTooltipInWindow(page, "done-time-per-phase-tooltip-1440");
    },
  },
  // Work time's header reached with the keyboard: Tab from the filter of the column before it
  // onto Work time's sort, which opens the header's tooltip, one sentence on how Dispatch
  // measures it. The pointer stands off the page, so no hover opens it.
  {
    name: "done-work-time-tooltip-focus-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("done"));
      await page.mouse.move(1, 1);
      await page.getByRole("button", { name: "Filter Time per phase", exact: true }).focus();
      await page.keyboard.press("Tab");
      await page.waitForTimeout(500);
      const focused = await page.evaluate(() => {
        const active = document.activeElement;
        const named = active?.getAttribute("aria-describedby");
        const tip = named ? document.getElementById(named) : null;
        return {
          header: active?.closest("th")?.textContent?.trim() ?? "",
          tip: tip?.getAttribute("role") === "tooltip" ? (tip.textContent?.trim() ?? "") : "",
        };
      });
      check(
        focused.header.startsWith("Work time") &&
          /^[^.]+\.$/.test(focused.tip) &&
          focused.tip.includes("Legion queue hours"),
        `Tab onto Work time's header opens its tooltip: "${focused.tip}"`
      );
      await checkTooltipInWindow(page, "done-work-time-tooltip-focus-1440");
    },
  },
  // A done issue with Legion facts and no measured work (the newest closed, or the one
  // --empty-work-time names), among the issues closed on its day: its Work time cell is empty,
  // not "0 m", beside rows that draw one.
  {
    name: "done-work-time-empty-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("done"));
      const pick = await page.evaluate(
        async ({ key, wanted }) => {
          const board: {
            issues: Record<string, { work_first_at?: string | null; work_hours: number }>;
          } = await (await fetch(`/api/v1/projects/${key}/legion/board`)).json();
          const done: { key: string; closed_at: string | null }[] = await (
            await fetch(`/api/v1/issues?project=${key}&status=done`)
          ).json();
          const unmeasured = done
            .flatMap((issue) => {
              const facts = board.issues[issue.key];
              return issue.closed_at !== null &&
                facts !== undefined &&
                facts.work_first_at == null &&
                !(facts.work_hours > 0)
                ? [{ key: issue.key, closed: issue.closed_at }]
                : [];
            })
            .sort((a, b) => b.closed.localeCompare(a.closed));
          const issue =
            wanted === undefined ? unmeasured[0] : unmeasured.find((one) => one.key === wanted);
          return issue === undefined
            ? null
            : { key: issue.key, day: issue.closed.slice(0, 10), unmeasured: unmeasured.length };
        },
        { key: project, wanted: flags["empty-work-time"] }
      );
      if (pick === null) throw new Error("no done issue has Legion facts and no measured work");
      const row = page.locator(`[data-issue-row="${pick.key}"]`);
      // One whole day: the page runs in UTC, and a range's end day is included.
      await open(page, view("done", `&f.closed=${pick.day}..${pick.day}`));
      // The table draws the rows near the screen only, so wheel it down, as a reader would,
      // until the issue's row is drawn.
      const table = await page.getByTestId("issues-table-scroll").boundingBox();
      if (table === null) throw new Error("Done's table is not drawn");
      for (let turn = 0; turn < 60 && (await row.count()) === 0; turn += 1) {
        await page.mouse.move(table.x + table.width / 2, table.y + table.height / 2);
        await page.mouse.wheel(0, 300);
        await page.waitForTimeout(150);
      }
      await row.waitFor({ state: "attached", timeout: 10_000 });
      await row.evaluate((drawn) => drawn.scrollIntoView({ block: "center" }));
      await page.mouse.move(1, 1);
      await page.waitForTimeout(500);
      const cells = await page.getByTestId("issues-table-scroll").evaluate((box, key) => {
        const header = [...box.querySelectorAll("thead th")].find((th) =>
          th.textContent?.trim().startsWith("Work time")
        );
        if (header === undefined) return null;
        const { left, right } = header.getBoundingClientRect();
        const cellOf = (tr: Element) =>
          [...tr.querySelectorAll("td")].find((td) => {
            const middle = (td.getBoundingClientRect().left + td.getBoundingClientRect().right) / 2;
            return middle > left && middle < right;
          });
        const rows = [...box.querySelectorAll("[data-issue-row]")];
        const own = rows.find((tr) => tr.getAttribute("data-issue-row") === key);
        return {
          own: own === undefined ? null : (cellOf(own)?.textContent?.trim() ?? null),
          measured: rows.filter((tr) => (cellOf(tr)?.textContent?.trim() ?? "") !== "").length,
          rows: rows.length,
        };
      }, pick.key);
      check(
        cells !== null && cells.own === "",
        `${pick.key}, one of ${pick.unmeasured} done issues with Legion facts and no measured work, draws an empty Work time cell (it reads "${cells?.own ?? "no cell"}"); ${cells?.measured ?? 0} of the ${cells?.rows ?? 0} rows drawn of the ${await tabCount(page, "done")} closed on ${pick.day} draw a Work time`
      );
    },
  },
  {
    name: "board-open-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&layout=board"));
      await checkCards(page, "board-open-1440");
    },
  },
  {
    name: "board-open-legion-phase-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openBoardByLegionPhase(page);
      await checkCards(page, "board-open-legion-phase-1440");
    },
  },
  // A browser that kept the old Legion view as its `project.issue-view` choice: Open opens on
  // its Board by Legion phase, with no `view=` or `layout=` in the address.
  {
    name: "legion-choice-board-1440",
    viewport: DESKTOP,
    take: async (page) => {
      // The choice is kept per signed-in login, read here from the stub's whoami on a page that
      // keeps no choice of its own.
      await page.goto(address(`/projects/${project}/settings`));
      const login = await page.evaluate(async () => {
        const me: { login: string } = await (await fetch("/api/v1/whoami")).json();
        return me.login;
      });
      await page.evaluate(
        (key) => window.localStorage.setItem(key, "legion"),
        `dispatch.project.issue-view:${login}`
      );
      await open(page, issuesPath, page.locator("[data-board-card]").first());
      check(
        (await page.locator('[data-board-by="legion_phase"]').count()) === 1,
        "the kept legion choice opens Open's Board by Legion phase"
      );
      await checkCards(page, "legion-choice-board-1440");
    },
  },
  // One card of the Board by Legion phase, close up: its Legion facts under the names the
  // owner's review gave them (Time in Legion phase, Work time, Workers).
  {
    name: "board-card-legion-facts-1440",
    viewport: DESKTOP,
    subject: "[data-shot-subject]",
    take: async (page) => {
      await open(page, view("open", "&layout=board&group=legion_phase"));
      const facts = await page.locator("[data-board-card]").evaluateAll((cards) => {
        const fact = (card: Element, hook: string) =>
          card.querySelector<HTMLElement>(`[data-legion-fact="${hook}"]`)?.innerText;
        const full = (card: Element) =>
          fact(card, "phase-time") !== undefined &&
          fact(card, "work-time") !== undefined &&
          !/None$/.test(fact(card, "roles") ?? "None");
        const card =
          cards.find((each) => full(each) && fact(each, "blocked-by") !== undefined) ??
          cards.find(full);
        if (card === undefined) return [];
        card.setAttribute("data-shot-subject", "");
        // In the middle of the window, clear of the banner at its foot.
        card.scrollIntoView({ block: "center", inline: "center" });
        return [...card.querySelectorAll<HTMLElement>("[data-legion-fact]")].map((part) =>
          part.innerText.replace(/\s+/g, " ").trim()
        );
      });
      check(
        ["Time in Legion phase ", "Work time ", "Workers "].every((label) =>
          facts.some((each) => each.startsWith(label))
        ) && !facts.some((each) => /^(In phase|Roles) /.test(each)),
        `a Board card reads ${facts.join(" · ")}`
      );
    },
  },
  {
    name: "group-by-menu-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      await page.getByRole("button", { name: "Group by", exact: true }).click();
      await page.waitForTimeout(500);
    },
  },
  // The same menu on the Board, where it picks the columns: by Status or by Legion phase.
  {
    name: "board-group-by-menu-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&layout=board"), page.locator("[data-board-card]").first());
      await page.getByRole("button", { name: "Group by", exact: true }).click();
      await page.waitForTimeout(500);
      const picks = await page
        .getByRole("radio")
        .evaluateAll((radios) =>
          radios.map((radio) => radio.closest("label")?.textContent?.trim() ?? "")
        );
      check(
        picks.includes("Status") && picks.includes("Legion phase"),
        `the Board's Group by menu offers ${picks.join(", ")}`
      );
    },
  },
  // Open's Board, held: another reader moves the first Todo card to Backlog. The card stays
  // where the reader saw it, the column counts move at once, and a pill floating over the column
  // headers offers "Show 1 change". The pill takes no room: the headers and the first card do
  // not move.
  {
    name: "board-held-live-change-1440",
    viewport: DESKTOP,
    take: async (page) => {
      const { before, counts, moved, tops } = await holdOpenBoard(page);
      const after = await firstCards(page, "todo");
      const now = [await columnCount(page, "todo"), await columnCount(page, "backlog")];
      const shown = await heldButton(page).innerText();
      check(
        same(after, before) &&
          (await page
            .locator(`[data-board-column="backlog"] [data-board-card="${moved}"]`)
            .count()) === 0,
        `the held Board keeps ${moved} first in Todo after it moves to Backlog elsewhere: Todo starts ${after.join(", ")}`
      );
      check(
        now[0] === counts[0] - 1 && now[1] === counts[1] + 1,
        `the column counts move at once: Todo ${counts[0]} to ${now[0]}, Backlog ${counts[1]} to ${now[1]}`
      );
      check(shown === "Show 1 change", `the Board's pill reads "${shown}"`);
      await checkPillPlace(page, "board-held-live-change-1440", "board");
      checkStill("board-held-live-change-1440", "board", tops);
    },
  },
  // The same Board after the press on Show 1 change: the moved card has left Todo for Backlog,
  // the pill has gone, and the headers and the first card stood still throughout.
  {
    name: "board-held-shown-1440",
    viewport: DESKTOP,
    take: async (page) => {
      const { before, moved, tops } = await holdOpenBoard(page);
      const pressed = await pressPill(page, "board");
      const after = await firstCards(page, "todo");
      check(
        (await heldButton(page).count()) === 0 &&
          (await page
            .locator(`[data-board-column="backlog"] [data-board-card="${moved}"]`)
            .count()) === 1 &&
          !after.includes(moved) &&
          after[0] === before[1],
        `Show change moves ${moved} to Backlog: Todo starts ${after.join(", ")}, and the pill has gone`
      );
      checkStill("board-held-shown-1440", "board", [...tops, ["after the press", pressed]]);
    },
  },
  // The reader's own move: the first Todo card dragged onto Backlog moves at once, with no
  // Show changes.
  {
    name: "board-own-drag-applies-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&layout=board"), page.locator("[data-board-card]").first());
      const card = page.locator('[data-board-column="todo"] [data-board-card]').first();
      const key = await card.getAttribute("data-board-card");
      const counts = [await columnCount(page, "todo"), await columnCount(page, "backlog")];
      await drag(page, card, page.locator('[data-board-column="backlog"]').first());
      await page.waitForTimeout(1_500);
      const now = [await columnCount(page, "todo"), await columnCount(page, "backlog")];
      const where = {
        todo: await page.locator(`[data-board-column="todo"] [data-board-card="${key}"]`).count(),
        backlog: await page
          .locator(`[data-board-column="backlog"] [data-board-card="${key}"]`)
          .count(),
      };
      check(
        where.todo === 0 && where.backlog === 1 && (await heldButton(page).count()) === 0,
        `the reader's own drag moves ${key} from Todo to Backlog at once, with no Show changes`
      );
      check(
        now[0] === counts[0] - 1 && now[1] === counts[1] + 1,
        `after the drag Todo reads ${now[0]} (was ${counts[0]}) and Backlog ${now[1]} (was ${counts[1]})`
      );
    },
  },
  { name: "save-view-popover-1440", viewport: DESKTOP, take: openSaveView },
  {
    name: "save-view-saved-tab-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openSaveView(page);
      await page.getByRole("button", { name: "Save", exact: true }).click();
      const saved = page.locator('[data-saved-view-tab="Triage"]');
      await settle(page, saved);
      check((await saved.getAttribute("aria-selected")) === "true", "the saved Triage tab is open");
    },
  },
  // The Columns menu over Open: every column, the Legion ones under their names since the
  // owner's review, in a menu as tall as its list.
  {
    name: "columns-menu-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      await page.getByRole("button", { name: /^Columns/ }).click();
      await page.waitForTimeout(500);
      const menu = await page.getByRole("checkbox").evaluateAll((boxes) => {
        const listed = boxes.filter((box) => box.closest("table") === null);
        const names = listed.map(
          (box) => box.closest("label")?.textContent?.trim() || box.getAttribute("aria-label") || ""
        );
        // The menu's own scroller is the nearest box around its first choice that scrolls and
        // holds no table; one that holds the table is the page's.
        let scroller = listed[0]?.parentElement ?? null;
        while (scroller !== null && !/auto|scroll/.test(getComputedStyle(scroller).overflowY)) {
          scroller = scroller.parentElement;
        }
        const scrolls =
          scroller !== null &&
          scroller.querySelector("table") === null &&
          scroller.scrollHeight > scroller.clientHeight + 1;
        return { names, scrolls };
      });
      check(
        LEGION_COLUMNS.every((name) => menu.names.includes(name)) &&
          !OLD_COLUMN_NAMES.some((name) => menu.names.includes(name)),
        `the Columns menu lists ${menu.names.join(", ")}`
      );
      check(
        !menu.scrolls,
        `the Columns menu shows its ${menu.names.length} columns with no scroll`
      );
    },
  },
  // Open asks' filter open: the issues with an ask open, or with none.
  {
    name: "filter-open-asks-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      await page.getByRole("button", { name: "Filter Open asks", exact: true }).click();
      await page.waitForTimeout(500);
      const choices = await filterChoices(page, "Open asks");
      check(
        ["With open asks, ", "No open asks, "].every((choice) =>
          choices.some((each) => each.startsWith(choice))
        ),
        `Open asks filters by ${choices.join("; ")}`
      );
    },
  },
  // Time in Legion phase, ticked in the Columns menu, with its filter open: four spans of time
  // and None.
  {
    name: "filter-time-in-legion-phase-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      await showColumn(page, "Time in Legion phase");
      await page.getByRole("button", { name: "Filter Time in Legion phase", exact: true }).click();
      await page.waitForTimeout(500);
      const choices = await filterChoices(page, "Time in Legion phase");
      const spans = ["Under 1 hour", "1 hour to 1 day", "1 to 7 days", "Over 7 days", "None"];
      check(
        spans.every((span) => choices.some((each) => each.startsWith(`${span}, `))),
        `Time in Legion phase filters by ${choices.join("; ")}`
      );
    },
  },
  // Why it waits, ticked in the Columns menu, with its filter open: the daemon's reason for each
  // card in the queue, and None. Capacity to go on stands only while a card waits for a slot,
  // as the board says.
  {
    name: "filter-why-it-waits-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      const slotWaits = await page.evaluate(async (key) => {
        const board: { columns: { cards: { waiting: { reason: string } | null }[] }[] } = await (
          await fetch(`/api/v1/projects/${key}/legion/board`)
        ).json();
        return board.columns
          .flatMap((column) => column.cards)
          .filter((card) => card.waiting?.reason === "slot").length;
      }, project);
      await showColumn(page, "Why it waits");
      await page.getByRole("button", { name: "Filter Why it waits", exact: true }).click();
      await page.waitForTimeout(500);
      const choices = await filterChoices(page, "Why it waits");
      const offered = (reason: string) => choices.some((each) => each.startsWith(`${reason}, `));
      check(
        ["Ready to start", "A blocker", "A person", "None"].every(offered) &&
          offered("Capacity to go on") === slotWaits > 0 &&
          !choices.some((each) => /queue|slot/i.test(each)),
        `Why it waits filters by ${choices.join("; ")} (${slotWaits} cards wait for a slot)`
      );
    },
  },
  {
    name: "view-open-info-hint-open-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      await page.getByRole("button", { name: "Legion daemon" }).click();
      await page.waitForTimeout(500);
    },
  },
  {
    name: "project-settings-legion-panel-1440",
    viewport: DESKTOP,
    // The panel draws once the board answers; a settings row is what it waits for.
    take: (page) =>
      open(
        page,
        `/projects/${project}/settings`,
        page.locator("[data-legion-settings] tbody tr").first()
      ),
  },
  // The daemon's newest read failed (the stub's demo=read-failed flag): the alert beside the
  // tabs, and the cards of the read before it.
  {
    name: "view-open-daemon-read-failed-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&demo=read-failed"));
      await page.locator("[data-legion-read-error]").waitFor({ timeout: 30_000 });
      check(!page.url().includes("demo="), "the read-failed flag leaves the address");
    },
  },
  // The read of the Legion setting failed (the stub's demo=settings-failed flag): the alert and
  // its Retry under the tabs, and the project drawn as one with no daemon.
  {
    name: "view-open-settings-failed-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&demo=settings-failed"));
      await page.locator("[data-legion-settings-error]").waitFor({ timeout: 30_000 });
      check(!page.url().includes("demo="), "the settings-failed flag leaves the address");
    },
  },
  // A project with no Legion daemon (GTM by default): Open and Done alone, no Legion column, no
  // daemon hint.
  {
    name: "no-daemon-view-open-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "", flags["no-daemon-project"]));
      await checkOnFirstScreen(page, "no-daemon-view-open-1440", "Open", [
        "Created by",
        "Open asks",
      ]);
    },
  },
  {
    name: "no-daemon-board-open-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&layout=board", flags["no-daemon-project"]));
      await checkCards(page, "no-daemon-board-open-1440");
    },
  },
  // The priority pages (cybermonklabs/legion#3767), whose issue tables are the design's.
  { name: "priorities-1440", viewport: DESKTOP, fullPage: true, take: openPriorities },
  { name: "priority-page-1440", viewport: DESKTOP, fullPage: true, take: openPriority },
  { name: "view-open-390", viewport: PHONE, take: (page) => open(page, view("open")) },
  {
    name: "board-open-legion-phase-390",
    viewport: PHONE,
    take: async (page) => {
      await openBoardByLegionPhase(page);
      await checkCards(page, "board-open-legion-phase-390");
    },
  },
  {
    name: "done-time-per-phase-hours-390",
    viewport: PHONE,
    take: async (page) => {
      await openDoneInHours(page);
      // Swipe the column in as a reader would: its left edge just after the pinned Key.
      await page.getByTestId("issues-table-scroll").evaluate((box) => {
        const headers = [...box.querySelectorAll("th")];
        const phase = headers.find((th) => th.textContent?.includes("Time per phase"));
        const key = headers.find((th) => th.textContent?.trim().startsWith("Key"));
        if (phase === undefined || key === undefined) return;
        box.scrollLeft += phase.getBoundingClientRect().left - key.getBoundingClientRect().right;
      });
      await page.waitForTimeout(500);
      // The header stacks on a phone, so the whole Share/Hours switch stands in the window.
      const switchBox = await page
        .getByRole("group", { name: "Time per phase as" })
        .evaluate((group) => {
          const box = group.getBoundingClientRect();
          return { left: Math.round(box.left), right: Math.round(box.right) };
        });
      check(
        switchBox.left >= 0 && switchBox.right <= PHONE.width,
        `the Share/Hours switch stands at ${switchBox.left}-${switchBox.right} px of the ${PHONE.width} px window`
      );
    },
  },
  { name: "priorities-390", viewport: PHONE, fullPage: true, take: openPriorities },
];

/** Open on the Board with its columns by Legion phase (Group by's other pick). */
async function openBoardByLegionPhase(page: Page): Promise<void> {
  await open(page, view("open", "&layout=board&group=legion_phase"));
  check(
    (await page.locator('[data-board-by="legion_phase"]').count()) === 1,
    "group=legion_phase puts the Board's columns by Legion phase"
  );
}

/** The single file, clicked through with the network off. */
const fileShots: Shot[] = [
  {
    name: "file-opens-on-open-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await page.goto(base);
      await settle(page);
      check(page.url().includes(`#${issuesPath}`), "the file opens on the Issues tab");
      check((await tab(page, "open").getAttribute("aria-selected")) === "true", "Open is on");
    },
  },
  {
    name: "file-switched-to-done-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, issuesPath);
      await tab(page, "done").click();
      await settle(page);
      check(
        (await tab(page, "done").getAttribute("aria-selected")) === "true",
        "a press on the Done tab opens Done"
      );
    },
  },
  {
    name: "file-open-board-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      await page.getByRole("button", { name: "Board", exact: true }).click();
      await settle(page, page.locator("[data-board-card]").first());
      check((await page.locator("[data-board-card]").count()) > 0, "the Board layout draws cards");
    },
  },
  {
    name: "file-board-drag-todo-to-backlog-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open", "&layout=board"), page.locator("[data-board-card]").first());
      const card = page.locator('[data-board-column="todo"] [data-board-card]').first();
      const key = await card.getAttribute("data-board-card");
      const target = page.locator('[data-board-column="backlog"]').first();
      await drag(page, card, target);
      await page.waitForTimeout(1_500);
      const status = await page.evaluate(async (issue) => {
        const answer: unknown = await (await fetch(`/api/v1/issues/${issue}`)).json();
        return typeof answer === "object" && answer !== null && "status" in answer
          ? answer.status
          : undefined;
      }, key);
      check(status === "backlog", `a drag of ${key} from Todo onto Backlog sets its status`);
      check(page.url().includes(issuesPath), "the drop opens no issue: the Board stays");
      check(
        (await page.locator(`[data-board-column="todo"] [data-board-card="${key}"]`).count()) === 0,
        `${key} has left the Todo column`
      );
      check(
        (await heldButton(page).count()) === 0,
        "the reader's own drag applies at once: no Show changes"
      );
    },
  },
  {
    name: "file-save-view-saved-tab-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openSaveView(page);
      await page.getByRole("button", { name: "Save", exact: true }).click();
      const saved = page.locator('[data-saved-view-tab="Triage"]');
      await settle(page, saved);
      check((await saved.count()) === 1, "Save view adds the saved tab");
    },
  },
  {
    name: "file-saved-view-deleted-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openSaveView(page);
      await page.getByRole("button", { name: "Save", exact: true }).click();
      const saved = page.locator('[data-saved-view-tab="Triage"]');
      await settle(page, saved);
      await page.getByRole("button", { name: "Delete saved view Triage" }).click();
      await page.waitForTimeout(500);
      check((await saved.count()) === 0, "Delete takes the saved tab away");
      await page.reload();
      await settle(page);
      check((await saved.count()) === 0, "the deleted view stays gone after a reload");
    },
  },
  {
    name: "file-columns-menu-shows-work-time-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("open"));
      const header = page.locator("thead th", { hasText: "Work time" });
      check((await header.count()) === 0, "Open shows no Work time column at first");
      await page.getByRole("button", { name: /^Columns/ }).click();
      await page.getByRole("checkbox", { name: "Work time" }).check();
      await page.keyboard.press("Escape");
      await page.waitForTimeout(500);
      check((await header.count()) === 1, "ticking Work time in the Columns menu shows the column");
    },
  },
  {
    name: "file-closed-filter-7-days-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await open(page, view("done"));
      // The pager draws no total, so the rows listed are the count on the current tab.
      const before = await tabCount(page, "done");
      await page.getByRole("button", { name: /^Filter Closed/ }).click();
      await page.getByRole("button", { name: "7 days", exact: true }).click();
      // Off the rows, so no issue's hover preview stands in the picture.
      await page.mouse.move(1, 1);
      await settle(page);
      const after = await tabCount(page, "done");
      // The rows the view should list: the done issues closed in the last 7 times 24 hours of
      // the page's own clock, from the stub's own listing.
      const expected = await page.evaluate(async (key) => {
        const since = new Date(Date.now() - 7 * 86_400_000).toISOString();
        const rows: unknown[] = await (
          await fetch(`/api/v1/issues?project=${key}&status=done&closed_since=${since}`)
        ).json();
        return rows.length;
      }, project);
      check(page.url().includes("f.closed=7d"), "the 7 days preset writes f.closed=7d");
      check(
        after === expected && after > 0 && after < before,
        `Closed in the last 7 days lists ${after} rows of Done's ${before}; the stub's rows say ${expected}`
      );
    },
  },
  {
    name: "file-priorities-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openPriorities(page);
      const listed = await page.evaluate(async () => {
        const body: { priorities: unknown[] } = await (await fetch("/api/v1/priorities")).json();
        return body.priorities.length;
      });
      const rows = await page.locator('section[aria-label="Priorities"] [data-issue-row]').count();
      check(
        listed > 0 && rows === listed,
        `the priorities page lists ${rows} priorities; the stub's answer holds ${listed}`
      );
    },
  },
  {
    name: "file-priority-page-1440",
    viewport: DESKTOP,
    take: async (page) => {
      await openPriority(page);
      check(/#\/priorities\/[^/?#]+/.test(page.url()), "a press on a priority opens its own page");
      const rows = await page
        .locator('section[aria-label="Work advancing this priority"] [data-issue-row]')
        .count();
      check(rows > 0, `the priority's issue table draws ${rows} rows`);
    },
  },
];

/** A drag with the pointer, in steps, as a person makes one: dnd-kit starts a drag only once the
 *  pointer has moved past its activation distance. The card is taken by its lower right corner
 *  and let go just under the column's title, both away from any link, so the release opens no
 *  issue. */
async function drag(page: Page, from: Locator, to: Locator): Promise<void> {
  const start = await from.boundingBox();
  const end = await to.boundingBox();
  if (start === null || end === null) throw new Error("the card or the column is not drawn");
  const x = start.x + start.width - 12;
  const y = start.y + start.height - 8;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x - 10, y - 10, { steps: 5 });
  await page.mouse.move(end.x + end.width / 2, end.y + 70, { steps: 25 });
  await page.waitForTimeout(200);
  await page.mouse.up();
  // Off the cards, so no issue's hover preview stands in the picture.
  await page.mouse.move(1, 1);
}

async function takeAll(browser: Browser, shots: Shot[]): Promise<void> {
  for (const shot of shots) {
    const wanted = flags.only?.split(",");
    if (wanted !== undefined && !wanted.some((part) => shot.name.includes(part))) continue;
    // A fresh context for each picture: nothing stored, as a first visit; and for the single
    // file no network at all, every request outside the page refused and recorded.
    const context = await browser.newContext({
      viewport: shot.viewport,
      deviceScaleFactor: shot.subject === undefined ? 1 : 2,
      offline: flags.file,
      timezoneId: TIME_ZONE,
      locale: LOCALE,
    });
    if (flags.file) {
      await context.route(/^(?!file:|data:|blob:)/, (route) => {
        report.errors.push(`${shot.name}: a request left the file: ${route.request().url()}`);
        return route.abort();
      });
    }
    const page = await context.newPage();
    const cdp = await context.newCDPSession(page);
    await cdp.send("DOM.enable");
    await cdp.send("CSS.enable");
    page.on("pageerror", (error) => report.errors.push(`${shot.name}: ${error.message}`));
    page.on("console", (message) => {
      if (message.type() === "error") report.errors.push(`${shot.name}: ${message.text()}`);
    });
    try {
      await shot.take(page);
      if (shot.viewport.width < 640) {
        // The phone's banner is its short form, which holds two lines at most.
        const lines = await page
          .locator('[data-testid="views-demo-banner"] .short')
          .evaluate((short) => {
            const height = Number.parseFloat(getComputedStyle(short).lineHeight);
            return Math.round(short.getBoundingClientRect().height / height);
          });
        check(lines <= 2, `${shot.name}: the phone banner takes ${lines} lines`);
      }
      await recordFonts(cdp);
      const file = join(out, `${shot.name}.png`);
      // A whole page: the banner, fixed to the window, would stand over the middle of it, so the
      // page gets room at its foot and the banner stands there, under everything the page drew.
      if (shot.fullPage === true) {
        await page.evaluate(() => {
          const banner = document.querySelector<HTMLElement>('[data-testid="views-demo-banner"]');
          if (banner === null) return;
          const room = banner.getBoundingClientRect().height + 16;
          document.body.style.paddingBottom = `${room}px`;
          banner.style.position = "absolute";
          banner.style.bottom = "auto";
          banner.style.top = `${document.documentElement.scrollHeight - room + 8}px`;
        });
      }
      if (shot.subject === undefined) {
        await page.screenshot({ path: file, fullPage: shot.fullPage === true });
      } else {
        await page.locator(shot.subject).first().screenshot({ path: file });
      }
      report.shots.push(file);
      console.log(`wrote ${file}`);
    } catch (error) {
      report.errors.push(`${shot.name}: ${error instanceof Error ? error.message : String(error)}`);
      await page.screenshot({ path: join(out, `${shot.name}.failed.png`) }).catch(() => undefined);
    }
    const missed = await page
      .evaluate(() => window.__dispatchDemoUnanswered ?? [])
      .catch(() => [] as string[]);
    for (const entry of missed) report.unanswered.push(`${shot.name}: ${entry}`);
    await context.close();
  }
}

await mkdir(out, { recursive: true });
if (process.env.TZ !== TIME_ZONE) console.warn(`run with TZ=${TIME_ZONE}, as the README says`);
const browser = await chromium.launch({ executablePath: flags.chromium });
report.browser.version = `${browser.browserType().name()} ${browser.version()}`;
try {
  await takeAll(browser, { before: beforeShots, file: fileShots, after: afterShots }[mode]);
} finally {
  await browser.close();
}
await writeFile(join(out, flags.report ?? `report-${mode}.json`), JSON.stringify(report, null, 2));
console.log(
  `${report.shots.length} pictures, ${report.checks.length} checks passed, ${report.unanswered.length} unanswered requests, ${report.errors.length} errors`
);
for (const line of [...report.checks, ...report.unanswered, ...report.errors]) {
  console.log(`  ${line}`);
}
if (report.unanswered.length > 0 || report.errors.length > 0) process.exit(1);
