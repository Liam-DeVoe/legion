/** The priority, read and written in a browser against the real stack: the list page and its two
 *  fault lists, a priority's own page and its margin, the line an issue's header carries, and the
 *  rail's count. Five checks of dispatch://INFRA-2947 are this one file - the page's order, the
 *  writes a person makes, the margin, the issue's line and the two absences - so a row that fails
 *  fails all five, and each row below is one of their sentences.
 *
 *  The pages' own components are read by `web/src/features/priorities/*.test.tsx`; what is here is
 *  what only a browser against a server can say: that a write lands in the store and comes back
 *  after a reload, that a count moves with no reload at all, and that two pages agree about one
 *  relation.
 */
import { readFileSync } from "node:fs";

import { PRIORITY_ID_MAX } from "@legion/contracts";

import {
  claimIssue,
  createIssue,
  createPriority,
  createProject,
  dashboardOrigin,
  endPriority,
  getIssue,
  getPriorityHistory,
  listPriorities,
  patchIssue,
  patchPriority,
} from "./api";
import { actionBar } from "./editor";
import { pauseClock } from "./page-clock";
import { expect, type Locator, type Page, test } from "./test";
import { asUser } from "./users";

const PRIORITIES_PATH = "/priorities";

/** One case of the rule by which the server makes a priority's id from a title the create
 *  names no id beside: the title, and the id it gives. */
interface IdCase {
  readonly title: string;
  readonly id: string;
}

/** The cases of that rule, written once for the server's tests and the row below
 *  (`packages/contracts/fixtures/priority-ids.json`). The file is read by its path rather than
 *  imported, so it stays out of this package's TypeScript program. */
const idFixture: { readonly cases: readonly IdCase[] } = JSON.parse(
  readFileSync(new URL("../../contracts/fixtures/priority-ids.json", import.meta.url), "utf8")
);

/** An id the server makes from a title's hash, where the title gives no slug it can use
 *  (`priority-` and the first 12 hex characters of the title's SHA-256). */
const HASHED_ID = /^priority-[0-9a-f]{12}$/;

/** The case of that list with the longest title whose id is its slug. Its slug is longer than
 *  `PRIORITY_ID_MAX`, so the id the server derives from it is the slug cut at a word's end. A
 *  case whose id is made from the title's hash is no slug cut. */
const LONG_CASE = idFixture.cases
  .filter((one) => !HASHED_ID.test(one.id))
  .reduce((longest, one) => (one.title.length > longest.title.length ? one : longest));

/** The two fault tables, by the names the Issues table gives them: the `label` each fault list of
 *  `PrioritiesPage.tsx` passes, which `features/project/IssuesTable.tsx` names its table by with
 *  " table" after it. */
const NO_PRIORITY_TABLE = "Active under no priority table";
const PARKED_TABLE = "Parked priority work table";

/** What the server says of a deployment with no live priority, in its own words
 *  (`priorityNoneWritten`, `internal/dispatch/api/priority_queries.go`): the empty page draws the
 *  sentence the read answers rather than a sentence of its own. */
const NONE_LIVE =
  "No priority is live now, so the list is empty. Write one, then say which work advances it.";

/** The columns each fault list opens with, by their headers (INFRA-2947, R6): the issue, its
 *  title, its status, who holds it, how long it has stood and its project; and on the parked
 *  list when it was moved there and by whom, beside the priority it is parked under. There the
 *  move is how long the work has stood, so Updated opens hidden, one tick away under Columns.
 *  Every other column of the Issues table opens hidden, for a reader to show from its column
 *  control; no list has a Reason column until a reason is recorded (dispatch://INFRA-2934). */
const FAULT_COLUMNS = ["Key", "Title", "Status", "Claimed by", "Updated", "Project"] as const;
const PARKED_COLUMNS = ["Advances", "Moved there"] as const;
const HIDDEN_COLUMNS = [
  "Priority",
  "Queue",
  "Epic",
  "Assignee",
  "Created by",
  "Labels",
  "Open asks",
  "Created",
  "Advances",
] as const;

/** The corpus every row seeds: three priorities in one order, work under each in three
 *  projects, an issue in each of the four in-flight statuses that advances none, an issue that
 *  advances one through its parent and stands in the backlog, and one whose own row names it
 *  that stands there too. */
interface Seeded {
  /** The three priority ids, in rank order: `#1`, `#2`, `#3` on the page. */
  readonly ids: readonly [string, string, string];
  readonly titles: readonly [string, string, string];
  /** The description `#1` carries, several paragraphs of it. */
  readonly description: string;
  /** `#1`'s work: one issue in flight and one waiting. */
  readonly rollout: string;
  readonly smoke: string;
  /** `#2`'s one issue, in `testing`. */
  readonly queue: string;
  /** `#3`'s work: an issue in `needs_review`, the parent that states the relation, the child
   *  in `backlog` that inherits it, and the issue in `backlog` that states it on its own row -
   *  the one priority work that stands parked (`own`). */
  readonly ledger: string;
  readonly cutover: string;
  readonly child: string;
  readonly archive: string;
  /** Two of the issues in flight under no priority this file writes to. */
  readonly clock: string;
  readonly lease: string;
}

const FIRST_DESCRIPTION =
  "Every machine of the fleet runs the daemon, or the rollout is not done.\n\n" +
  "The work under this priority is the rollout itself, the smoke suite that proves a machine " +
  "took the build, and the roll-back a machine that refused it needs.\n\n" +
  "It is finished when no machine is left on the old build and nothing is waiting on one.";

async function seedPriorities(): Promise<Seeded> {
  await Promise.all([
    createProject({ key: "CORE", name: "Core" }),
    createProject({ key: "OPS", name: "Ops" }),
    createProject({ key: "WEB", name: "Web" }),
  ]);

  // Created one after another, because a priority takes the last place of the one order as it
  // is written (`POST /api/v1/priorities`), and these three are read as #1, #2 and #3.
  const first = await createPriority({
    description: FIRST_DESCRIPTION,
    owner: "alice",
    title: "Ship the daemon everywhere",
  });
  const second = await createPriority({ owner: "bob", title: "Cut the review queue" });
  const third = await createPriority({ owner: "alice", title: "Retire the old ledger" });

  const rollout = await createIssue({ project: "CORE", title: "Daemon rollout to the fleet" });
  const smoke = await createIssue({ project: "CORE", title: "Daemon smoke suite" });
  const queue = await createIssue({ project: "OPS", title: "Review queue triage" });
  const ledger = await createIssue({ project: "WEB", title: "Ledger read path" });
  const cutover = await createIssue({ project: "WEB", title: "Ledger cut-over" });
  // The child states nothing of its own: it advances `#3` because the issue above it does
  // (INFRA-2947, R4), which is what the issue page's inherited entry reads. Standing in the
  // backlog, it is no parked work: the parked list holds the work whose own row names a
  // priority, which the archive below is.
  const child = await createIssue({
    parent: cutover.key,
    project: "WEB",
    title: "Ledger client migration",
  });
  const archive = await createIssue({ project: "WEB", title: "Ledger archive" });
  // One issue in each of the four statuses the first fault is about, spread over the three
  // projects, so the count line under its rule names more than one board.
  const clock = await createIssue({ project: "CORE", title: "Audit the clock" });
  const lease = await createIssue({ project: "CORE", title: "Lease handover" });
  const mirror = await createIssue({ project: "OPS", title: "Mirror retries" });
  const sweep = await createIssue({ project: "WEB", title: "Phone layout sweep" });

  // One write per issue: a status, and the one relation it states. A write naming no revision
  // asserts one relation at a time, which is what each of these is (`api/issue_advances.go`).
  await Promise.all([
    patchIssue(rollout.key, { advances: [first.id], status: "in_progress" }),
    patchIssue(smoke.key, { advances: [first.id], status: "todo" }),
    patchIssue(queue.key, { advances: [second.id], status: "testing" }),
    patchIssue(ledger.key, { advances: [third.id], status: "needs_review" }),
    patchIssue(cutover.key, { advances: [third.id], status: "todo" }),
    patchIssue(child.key, { status: "backlog" }),
    patchIssue(archive.key, { advances: [third.id], status: "backlog" }),
    patchIssue(clock.key, { status: "in_progress" }),
    patchIssue(lease.key, { status: "retro" }),
    patchIssue(mirror.key, { status: "testing" }),
    patchIssue(sweep.key, { status: "needs_review" }),
  ]);
  // One of the two fault rows read cell by cell below is claimed and the other is not, while
  // both are assigned to alice: "Claimed by" names the claim and nothing else, as the issues
  // table of this application reads it (`features/project/issues-table-columns.tsx`).
  await claimIssue(clock.key, { as: "user", login: "alice" });

  return {
    archive: archive.key,
    child: child.key,
    clock: clock.key,
    cutover: cutover.key,
    description: FIRST_DESCRIPTION,
    ids: [first.id, second.id, third.id],
    ledger: ledger.key,
    lease: lease.key,
    queue: queue.key,
    rollout: rollout.key,
    smoke: smoke.key,
    titles: [first.title, second.title, third.title],
  };
}

/** A page of alice's at the desktop size the page is measured at (1440 by 900 px, INFRA-2947
 *  R7), which is wider than the width the sidebar's rail is drawn from (1280 px,
 *  `WIDE_LAYOUT_MIN_PX` in `e2e/review-routes.ts`), so three rows below read the Priorities
 *  entry from it. */
async function priorityPage(page: Page): Promise<void> {
  await page.setViewportSize({ height: 900, width: 1440 });
}

/** The phone the page is measured on (390 by 844 px, INFRA-2947 R7). */
const PHONE = { height: 844, width: 390 } as const;

const section = (page: Page, label: string): Locator =>
  page.locator(`section[aria-label="${label}"]`);

/** A priority's title on its own page: the heading of the page's header, which the description,
 *  drawn as the markdown it is, may hold first-level headings beside. */
const titleHeading = (page: Page): Locator =>
  page.getByTestId("priority-header").getByRole("heading", { level: 1 });

/** The box a priority's title is written in, which a click on its heading opens, as the reader
 *  opens it. */
async function openTitle(page: Page): Promise<Locator> {
  await titleHeading(page).click();
  return page.getByRole("textbox", { name: "Title" });
}

/** Where a section's box starts down the page, which is what says one part of a page stands
 *  above or below another, or has moved. Read once the section shows. */
async function topOf(locator: Locator): Promise<number> {
  await expect(locator).toBeVisible();
  // The wait above drew the section, and a drawn element has a box, so the box is there.
  const box = await locator.boundingBox();
  if (box === null) throw new Error("the section this row reads is not on the page");
  return box.y;
}

/** The page's three tabs, by the names their buttons read, which end in the count each holds:
 *  `Priorities 3`. Each is written into the address as `?tab=` with its key. */
const TABS = {
  order: /^Priorities \d+$/,
  none: /^Active under no priority \d+$/,
  parked: /^Parked priority work \d+$/,
} as const;

/** Chooses one of the page's tabs, and waits for the address to name it. */
async function chooseTab(page: Page, tab: keyof typeof TABS): Promise<void> {
  await page.getByRole("tab", { name: TABS[tab] }).click();
  await expect(page).toHaveURL(new RegExp(`[?&]tab=${tab}(&|$)`));
}

/** How far below the window's bottom edge the pager under a tab's table ends: at or above it,
 *  where that table is the last thing on the page and its box takes the window rather than
 *  standing under another table below the fold. */
function pagerBelowWindow(page: Page): Promise<number> {
  return page
    .getByTestId("issues-table-pager")
    .evaluate((pager) => pager.getBoundingClientRect().bottom - window.innerHeight);
}

/** How much wider than its box each table the page draws is, by the table's name: nothing,
 *  where every shown column fits the box and no scrollbar runs along its bottom edge. The box is
 *  the one each table scrolls in, named by its component's own test identifier
 *  (`priorities-table-scroll`, `issues-table-scroll`). */
function tablesOverflow(page: Page): Promise<Record<string, number>> {
  return page.locator("table").evaluateAll((tables) =>
    Object.fromEntries(
      tables.map((table) => {
        const box = table.closest('[data-testid$="-table-scroll"]');
        if (box === null) throw new Error("a table on the page stands in no scroll box");
        return [table.getAttribute("aria-label") ?? "", box.scrollWidth - box.clientWidth];
      })
    )
  );
}

/** How much wider than the window the page's document is: nothing, where the page itself does
 *  not scroll sideways. */
function documentOverflow(page: Page): Promise<number> {
  return page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
}

/** That the page as drawn says no date anything is wanted by, no portion of it done and no word
 *  judging how it goes: every `<time>` is a moment that has passed, and no relative label reads
 *  forwards. Read once the caller has waited for what the page draws, so one read is enough. */
async function expectNoForecast(page: Page): Promise<void> {
  const future = await page
    .locator("time[datetime]")
    .evaluateAll(
      (nodes, now) =>
        nodes
          .map((node) => node.getAttribute("datetime") ?? "")
          .filter((at) => Date.parse(at) > now),
      Date.now()
    );
  expect(future).toEqual([]);
  const drawn = await page.locator("body").innerText();
  expect(drawn).not.toMatch(/\bin \d+ (second|minute|hour|day|week|month|year)s?\b/);
  expect(drawn).not.toMatch(/\d\s?%/);
  expect(drawn).not.toMatch(/\b(on track|at risk|off track|behind schedule|ahead of schedule)\b/i);
}

/** Every row of a table, by the name a reader hears: `#1 <title>` on the priorities table and
 *  `<KEY> <title>` on a fault list. */
function rowNames(scope: Locator): Promise<string[]> {
  return scope
    .locator("tr[data-issue-row]")
    .evaluateAll((rows) => rows.map((row) => row.getAttribute("aria-label") ?? ""));
}

/** The rows a table holds, whatever order it draws them in, once the read behind it has
 *  answered: a table a navigation has just reached holds no row yet, and the rows of a fault
 *  list or of the work under a priority are a set rather than an order - the order each table
 *  opens in is its own and is read where a row is about it. */
async function expectRowNames(scope: Locator, names: readonly string[]): Promise<void> {
  await expect.poll(async () => (await rowNames(scope)).sort()).toEqual([...names].sort());
}

/** The rows a table holds in the order it draws them, which on the priorities table is the one
 *  order a reader drags and so is the claim itself. */
async function expectRowOrder(scope: Locator, names: readonly string[]): Promise<void> {
  await expect.poll(() => rowNames(scope)).toEqual([...names]);
}

/** Where a column stands in a table, by the text its header starts with: a reader drags the
 *  columns of the Issues table, so a column's place is the reader's, and its header is what it
 *  is. Its filter control follows the header's name in the same cell, so the name is the
 *  header's start, as `PrioritiesPage.test.tsx` reads it. */
async function columnIndex(table: Locator, header: string): Promise<number> {
  const headers = await table.getByRole("columnheader").allTextContents();
  const at = headers.findIndex((text) => text.trim().startsWith(header));
  if (at === -1) throw new Error(`the table draws no ${header} column`);
  return at;
}

/** A row's cell under the column whose header is `header`. */
async function cellUnder(table: Locator, row: Locator, header: string): Promise<Locator> {
  return row.getByRole("cell").nth(await columnIndex(table, header));
}

/** The text of one column of every row of a table, which is how a row reads the cells R6 names
 *  without spelling each row out. */
async function columnTexts(table: Locator, header: string): Promise<string[]> {
  const at = await columnIndex(table, header);
  return table
    .locator("tr[data-issue-row]")
    .evaluateAll(
      (rows, index) =>
        rows.map((row) => row.querySelectorAll("td")[index]?.textContent?.trim() ?? ""),
      at
    );
}

/** That a table draws the columns `shown` and none of `hidden`, read off its headers, each of
 *  which starts with its column's name. */
async function expectColumns(
  table: Locator,
  shown: readonly string[],
  hidden: readonly string[]
): Promise<void> {
  const headers = (await table.getByRole("columnheader").allTextContents()).map((text) =>
    text.trim()
  );
  for (const name of shown)
    expect(
      headers.some((text) => text.startsWith(name)),
      name
    ).toBe(true);
  for (const name of hidden)
    expect(
      headers.some((text) => text.startsWith(name)),
      name
    ).toBe(false);
}

/** Chooses a fault's tab and reloads the page, which opens on the tab the address kept. */
async function chooseTabAndReload(page: Page, tab: "none" | "parked"): Promise<void> {
  await chooseTab(page, tab);
  await page.reload();
  await expect(page).toHaveURL(new RegExp(`[?&]tab=${tab}(&|$)`));
  await expect(page.getByRole("tab", { name: TABS[tab] })).toHaveAttribute("aria-selected", "true");
}

/** The badge beside the rail's Priorities entry, which is its last span; an entry with no count
 *  draws two spans and no third. */
const railEntry = (page: Page): Locator => page.getByRole("link", { name: /^Priorities/ });

/** The priorities an issue advances in its header from a tablet's width up: the chips and the
 *  picker's "+", which stand in the header's state row, the row of the status, the P0-P3 tag and
 *  the assignee. On a phone they open the details row instead. */
const chipsLine = (page: Page): Locator =>
  page.getByTestId("issue-header").getByTestId("issue-state-actions").getByTestId("issue-advances");

// SLOW(>=2s): three projects, three priorities and ten issues, then the whole page read cell by
// cell, on a desktop and on a phone, and two reloads.
test("the priorities page opens on the order, and each fault list is a tab of its own", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);
    const main = page.getByTestId("main-content");

    // The page is its heading and three tabs, and nothing under the heading: no subtitle, and
    // no heading over any table. The order is the tab an address naming none opens, and it
    // draws its one table.
    await expect(main.getByRole("heading", { level: 1, name: "Priorities" })).toBeVisible();
    await expect(main.getByRole("heading")).toHaveCount(1);
    await expect(page.locator("h1 + p")).toHaveCount(0);
    await expect(main.getByText(/read now|their owner drags/)).toHaveCount(0);
    const tabs = page.getByRole("tab");
    await expect(tabs).toHaveCount(3);
    await expect(tabs.nth(0)).toHaveAccessibleName(TABS.order);
    await expect(tabs.nth(1)).toHaveAccessibleName(TABS.none);
    await expect(tabs.nth(2)).toHaveAccessibleName(TABS.parked);
    await expect(page.getByRole("tab", { name: TABS.order })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    const priorities = section(page, "Priorities");
    const prioritiesTable = page.getByRole("table", { exact: true, name: "Priorities table" });
    await expect(prioritiesTable).toBeVisible();
    await expect(page.getByRole("table")).toHaveCount(1);

    // The order itself: the rows in rank order, each named by its place and its title, and
    // each title a link to the priority's own page, beside its owner, the three counts of the
    // work under it as it stands - in flight, waiting, and in backlog or icebox, where the
    // child that inherits #3 stands - and when anything under it last moved.
    await expectRowOrder(priorities, [
      `#1 ${seed.titles[0]}`,
      `#2 ${seed.titles[1]}`,
      `#3 ${seed.titles[2]}`,
    ]);
    const rows = [
      { counts: ["1", "1", "0"], owner: "alice" },
      { counts: ["1", "0", "0"], owner: "bob" },
      { counts: ["1", "1", "2"], owner: "alice" },
    ];
    for (const [place, { counts, owner }] of rows.entries()) {
      const row = priorities.getByRole("row", {
        exact: true,
        name: `#${place + 1} ${seed.titles[place]}`,
      });
      await expect(
        row.getByRole("link", { exact: true, name: seed.titles[place] })
      ).toHaveAttribute("href", `/priorities/${seed.ids[place]}`);
      const cell = (header: string) => cellUnder(prioritiesTable, row, header);
      await expect(await cell("Owner")).toHaveText(owner);
      await expect(await cell("Active")).toHaveText(counts[0] ?? "");
      await expect(await cell("Triage, Todo")).toHaveText(counts[1] ?? "");
      await expect(await cell("Backlog, icebox")).toHaveText(counts[2] ?? "");
      await expect((await cell("Newest event")).locator("time")).toHaveCount(1);
    }
    // A closed row shows the first line of its description and no more of it, under its title.
    const firstRow = priorities.getByRole("row", { name: `#1 ${seed.titles[0]}` });
    await expect(firstRow).toContainText(
      "Every machine of the fleet runs the daemon, or the rollout is not done."
    );
    await expect(firstRow).not.toContainText("It is finished when no machine is left");

    // At 1440 by 900 the table fits its box: no column runs past the box's right edge, and the
    // pager under the box is on the first screen.
    await expect.poll(() => tablesOverflow(page)).toEqual({ "Priorities table": 0 });
    await expect.poll(() => documentOverflow(page)).toBeLessThanOrEqual(0);
    await expect.poll(() => pagerBelowWindow(page)).toBeLessThanOrEqual(0);

    // The grouping control reads "Group by".
    await expect(priorities.getByRole("combobox", { exact: true, name: "Group by" })).toBeVisible();

    // The count closed in the last 7 days is hidden until the column control turns it on.
    const closed = prioritiesTable.getByRole("columnheader", { name: /^Closed, 7 days/ });
    await expect(closed).toHaveCount(0);
    await priorities.getByRole("button", { exact: true, name: "Columns" }).click();
    await page
      .getByRole("dialog", { name: "Columns" })
      .getByRole("checkbox", { name: "Closed, 7 days" })
      .check();
    await expect(closed).toBeVisible();
    await page.keyboard.press("Escape");
    // Each column reads in at most 16 characters (INFRA-3221, R13): the counts read "Active",
    // "Triage, Todo" and "Backlog, icebox".
    // Each header's words are its text less its filter control's word ("Filter") and its sort
    // arrow.
    const headers = (await prioritiesTable.locator("thead th").allTextContents())
      .map((text) =>
        text
          .replace(/Filter$/, "")
          .replace(/[▲▼]/g, "")
          .trim()
      )
      .filter((text) => text !== "");
    expect(headers).toEqual(expect.arrayContaining(["Active", "Triage, Todo", "Backlog, icebox"]));
    for (const header of headers) expect(header.length, header).toBeLessThanOrEqual(16);

    // At 1440 by 900 every header's words show whole, Closed's among them: none is cut at its
    // column's edge. A count is no value a reader filters by, so a count's header is its words
    // and its sort arrow alone, and its numbers start where its words do.
    const cutHeaders = await prioritiesTable
      .locator('thead th button[title^="Sort"]')
      .evaluateAll((buttons) =>
        buttons
          .filter((button) => button.scrollWidth > button.clientWidth)
          .map((button) => button.textContent ?? "")
      );
    expect(cutHeaders).toEqual([]);
    for (const header of ["Active", "Triage, Todo", "Backlog, icebox", "Closed, 7 days"]) {
      const column = prioritiesTable.getByRole("columnheader", { name: new RegExp(`^${header}`) });
      await expect(column.getByRole("button", { name: /^Filter/ })).toHaveCount(0);
      const words = await column.locator('button[title^="Sort"]').boundingBox();
      const count = await (await cellUnder(prioritiesTable, firstRow, header))
        .locator("span")
        .boundingBox();
      expect(Math.abs((words?.x ?? Number.NaN) - (count?.x ?? Number.NaN)), header).toBeLessThan(1);
    }

    // Three rows are fewer than the window holds, so the box ends with its last row, its bottom
    // border under it, rather than standing empty down to the window's foot.
    await expect
      .poll(() =>
        page.getByTestId("priorities-table-scroll").evaluate((box) => {
          const last = [...box.querySelectorAll("tr[data-issue-row]")].at(-1);
          if (last === undefined) return Number.NaN;
          return box.getBoundingClientRect().bottom - last.getBoundingClientRect().bottom;
        })
      )
      .toBeLessThanOrEqual(2);

    // Opened, the row holds the whole description, every paragraph of the seed's.
    await priorities.getByRole("button", { name: `Open #1 ${seed.titles[0]}` }).click();
    const description = page.getByRole("textbox", { exact: true, name: "Description of #1" });
    for (const paragraph of seed.description.split("\n\n"))
      await expect(description).toContainText(paragraph);
    await expect(description.locator("p")).toHaveCount(3);
    await priorities.getByRole("button", { name: `Close #1 ${seed.titles[0]}` }).click();

    // Each fault is a tab of its own, which the address keeps across a reload, and which reads
    // its name and its one count; nothing stands between the tab and its one table - no line of
    // description and no counts - which is this repository's Issues table, read by its own box's
    // test identifier.
    await chooseTabAndReload(page, "none");
    const unplaced = section(page, "Active under no priority");
    await expect(page.getByRole("tab", { name: "Active under no priority 4" })).toBeVisible();
    await expect(main.getByText(/advance no priority|\d+ issues? · /)).toHaveCount(0);
    await expect(prioritiesTable).toHaveCount(0);
    const unplacedTable = page
      .getByTestId("issues-table-scroll")
      .getByRole("table", { exact: true, name: NO_PRIORITY_TABLE });
    await expect(page.getByRole("table")).toHaveCount(1);
    await expect(unplaced.getByRole("table")).toHaveAccessibleName(NO_PRIORITY_TABLE);

    // Its rows are a read of their own beside the priorities read, so they are waited for: one
    // issue in each of the four in-flight statuses, and nothing that advances a priority.
    const clockRow = unplacedTable.getByRole("row", {
      exact: true,
      name: `${seed.clock} Audit the clock`,
    });
    await expect(clockRow).toBeVisible();
    await expect
      .poll(async () => (await columnTexts(unplacedTable, "Status")).sort())
      .toEqual(["In progress", "Needs review", "Retro", "Testing"]);
    // The table opens with R6's columns alone, and at 1440 by 900 they fit its box; its box
    // takes the window, down to the pager under it.
    await expectColumns(unplacedTable, FAULT_COLUMNS, [...PARKED_COLUMNS, ...HIDDEN_COLUMNS]);
    await expect.poll(() => tablesOverflow(page)).toEqual({ [NO_PRIORITY_TABLE]: 0 });
    await expect.poll(() => documentOverflow(page)).toBeLessThanOrEqual(0);
    await expect.poll(() => pagerBelowWindow(page)).toBeLessThanOrEqual(0);
    // Every status pill is whole, "Needs review" the longest: no pill runs past its own box, and
    // none past the edge of its cell, which clips what passes it.
    const statusAt = await columnIndex(unplacedTable, "Status");
    expect(
      await unplacedTable.locator("tr[data-issue-row]").evaluateAll(
        (rows, at) =>
          rows.flatMap((row) => {
            const cell = row.querySelectorAll("td")[at];
            const pill = cell?.firstElementChild;
            if (cell === undefined || pill === null || pill === undefined) return ["no pill"];
            const cut =
              pill.scrollWidth > pill.clientWidth ||
              cell.scrollWidth > cell.clientWidth ||
              pill.getBoundingClientRect().right > cell.getBoundingClientRect().right;
            return cut ? [pill.textContent ?? ""] : [];
          }),
        statusAt
      )
    ).toEqual([]);
    // And one row of it cell by cell (R6), each cell read under its column's header: the issue,
    // its title, its status, its project, who holds it, and how long it has stood as it is.
    const clockCell = (header: string) => cellUnder(unplacedTable, clockRow, header);
    await expect((await clockCell("Key")).getByRole("link")).toHaveText(seed.clock);
    await expect(
      (await clockCell("Title")).getByRole("link", {
        exact: true,
        name: `${seed.clock} Audit the clock`,
      })
    ).toHaveText("Audit the clock");
    await expect(await clockCell("Status")).toHaveText("In progress");
    await expect(await clockCell("Project")).toHaveText("CORE");
    // Who holds it is who claimed it: alice claimed this one in the seed, and the cell is the
    // claim's chip naming her.
    await expect((await clockCell("Claimed by")).getByTestId("issue-claim")).toContainText("alice");
    await expect((await clockCell("Updated")).locator("time")).toHaveCount(1);
    // The row's issue is a link to its own page, and back is the tab the reader left.
    await (await clockCell("Key")).getByRole("link").click();
    await expect(page).toHaveURL(`/issues/${seed.clock}`);
    await page.goBack();
    await expect(page).toHaveURL(/[?&]tab=none(&|$)/);
    await expect(clockRow).toBeVisible();

    // The second fault's tab, whose one row is the child, which advances #3 through the issue
    // above it, and it names when it was moved out of the way and by whom.
    await chooseTabAndReload(page, "parked");
    const parked = section(page, "Parked priority work");
    await expect(page.getByRole("tab", { name: "Parked priority work 1" })).toBeVisible();
    await expect(main.getByText(/backlog or the icebox|\d+ issues? · /)).toHaveCount(0);
    const parkedTable = page
      .getByTestId("issues-table-scroll")
      .getByRole("table", { exact: true, name: PARKED_TABLE });
    await expect(parked.getByRole("table")).toHaveAccessibleName(PARKED_TABLE);
    await expect(page.getByRole("table")).toHaveCount(1);
    await expectRowOrder(parkedTable, [`${seed.archive} Ledger archive`]);
    // It opens with the fault's columns but Updated, beside the priority each row is parked under
    // and the move, which is how long the work has stood.
    await expectColumns(
      parkedTable,
      [...FAULT_COLUMNS.filter((name) => name !== "Updated"), ...PARKED_COLUMNS],
      [...HIDDEN_COLUMNS.filter((name) => name !== "Advances"), "Updated", "Reason"]
    );
    await expect.poll(() => tablesOverflow(page)).toEqual({ [PARKED_TABLE]: 0 });
    await expect.poll(() => documentOverflow(page)).toBeLessThanOrEqual(0);
    // The one parked row is the archive, whose own row names #3: the child that inherits #3 and
    // stands in the backlog beside it is no parked work.
    const childRow = parkedTable.getByRole("row", {
      exact: true,
      name: `${seed.archive} Ledger archive`,
    });
    const childCell = (header: string) => cellUnder(parkedTable, childRow, header);
    await expect((await childCell("Key")).getByRole("link")).toHaveAttribute(
      "href",
      `/issues/${seed.archive}`
    );
    await expect(await childCell("Project")).toHaveText("WEB");
    await expect(await childCell("Status")).toHaveText("Backlog");
    // Nobody claimed this one: this column says who holds the work, so it is empty here, as
    // every Issues table draws an issue nobody holds.
    await expect(await childCell("Claimed by")).toHaveText("");
    // Updated is one tick away under Columns, unticked.
    await parked.getByRole("button", { name: "Columns" }).click();
    await expect(
      page.getByRole("dialog", { name: "Columns" }).getByRole("checkbox", { name: "Updated" })
    ).not.toBeChecked();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Columns" })).toHaveCount(0);
    // Moved there: the moment the seed moved it into the backlog, and who moved it.
    const moved = await childCell("Moved there");
    await expect(moved.locator("time")).toHaveCount(1);
    await expect(moved).toContainText("alice");
    // Advances: the priority it is parked under, as the chip of its place in the order, #3, and
    // then its title, drawn whole: which priority the work is parked under is what the tab is
    // for, and a phone has no tooltip to read it in. Both are one link to that priority, whose
    // name and tooltip carry the title.
    const advancesCell = await childCell("Advances");
    const advances = advancesCell.getByRole("link");
    await expect(advances.locator("span").first()).toHaveText("#3");
    await expect(advances).toHaveAccessibleName(`#3 ${seed.titles[2]}`);
    await expect(advances).toHaveAttribute("title", seed.titles[2]);
    await expect(advances).toHaveAttribute("href", `/priorities/${seed.ids[2]}`);
    const drawnTitle = advancesCell.getByText(seed.titles[2], { exact: true });
    await expect(drawnTitle).toBeVisible();
    // Whole: it wraps rather than ends in an ellipsis or a clamp, nothing of it lies past its own
    // box, and the cell, which clips what passes its edge, holds all of it.
    expect(
      await drawnTitle.evaluate((title) => {
        const style = getComputedStyle(title);
        const box = title.getBoundingClientRect();
        const cell = title.closest("td")?.getBoundingClientRect();
        return {
          clamp: style.webkitLineClamp,
          cut: title.scrollWidth > title.clientWidth || title.scrollHeight > title.clientHeight,
          inside: cell !== undefined && box.right <= cell.right && box.bottom <= cell.bottom,
          overflow: style.textOverflow,
          wrap: style.whiteSpace,
        };
      })
    ).toEqual({ clamp: "none", cut: false, inside: true, overflow: "clip", wrap: "normal" });

    // On a phone the page's document is no wider than the window on any tab: each fault's
    // sentence wraps and its counts take a line of their own, and a table scrolls inside its box.
    await page.setViewportSize(PHONE);
    for (const tab of ["order", "none", "parked"] as const) {
      await chooseTab(page, tab);
      await expect(page.getByRole("table")).toHaveCount(1);
      await expect(page.getByRole("table")).toBeVisible();
      await expect.poll(() => documentOverflow(page)).toBeLessThanOrEqual(0);
    }
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): fifty-one issues written and moved into flight, and the tab's two pages read.
test("a fault tab of more than one page counts every row, keeps its status strip, and pages to the last", async ({
  browser,
}) => {
  await createProject({ key: "BULK", name: "Bulk" });
  // Fifty-one issues in progress that advance no priority: one more than a page of the table.
  const keys = await Promise.all(
    Array.from({ length: 51 }, async (_, at) => {
      const created = await createIssue({ project: "BULK", title: `Active issue ${at + 1}` });
      await patchIssue(created.key, { status: "in_progress" });
      return created.key;
    })
  );
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`${PRIORITIES_PATH}?tab=none`);
    // The tab counts all fifty-one, and its table is the Issues table with its status strip.
    await expect(page.getByRole("tab", { name: "Active under no priority 51" })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    const unplaced = section(page, "Active under no priority");
    await expect(unplaced.getByRole("group", { name: "Issues by status" })).toBeVisible();
    const table = page.getByRole("table", { exact: true, name: NO_PRIORITY_TABLE });
    await expect(table.locator("tr[data-issue-row]").first()).toBeVisible();
    // A page holds fifty rows, and the pager counts all fifty-one; its next page holds the one
    // row the first left over, the fifty-first. The rows of a page are drawn as they scroll into
    // the box, so the second page's one row is what this reads.
    const pager = page.getByTestId("issues-table-pager");
    await expect(pager).toContainText("51");
    await pager.getByRole("button", { name: "Next ›" }).click();
    // The second page's one row, read until it is the only row drawn: its key is one of the
    // fifty-one written, taken as the row name's first word.
    await expect
      .poll(async () =>
        (await rowNames(table)).map((name) => keys.includes(name.split(" ", 1).join("")))
      )
      .toEqual([true]);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): twenty priorities written one after another, and the box scrolled.
test("more priorities than the window holds scroll inside the table, under a header that stays", async ({
  browser,
}) => {
  // One after another, so the order is the order they were written in. Each carries a
  // description, whose first line every closed row shows under its title: twenty rows two lines
  // tall are more than the box a 1440x900 window gives the table.
  for (let place = 1; place <= 20; place += 1)
    await createPriority({
      description: `What priority ${place} is for.`,
      owner: "alice",
      title: `Priority number ${place}`,
    });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);
    const box = page.getByTestId("priorities-table-scroll");
    await expect(box.getByRole("row", { name: "#1 Priority number 1" })).toBeVisible();

    // The table and its pager fit the window: the page itself scrolls nowhere, sideways or down,
    // and the twenty rows scroll inside the box, which is the scroll this row reads next.
    await expect.poll(() => tablesOverflow(page)).toEqual({ "Priorities table": 0 });
    await expect.poll(() => pagerBelowWindow(page)).toBeLessThanOrEqual(0);
    await expect
      .poll(() =>
        page.evaluate(
          () => document.documentElement.scrollHeight - document.documentElement.clientHeight
        )
      )
      .toBeLessThanOrEqual(0);
    expect(
      await box.evaluate((element) => element.scrollHeight - element.clientHeight)
    ).toBeGreaterThan(0);

    // Scrolled, the rows move under the header, which stays at the box's top edge.
    const headerTop = () =>
      box.evaluate(
        (element) =>
          (element.querySelector("thead th")?.getBoundingClientRect().top ?? Number.NaN) -
          element.getBoundingClientRect().top
      );
    const before = await headerTop();
    await box.evaluate((element) => {
      element.scrollTop = 300;
    });
    await expect.poll(() => box.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    expect(await headerTop()).toBe(before);
    await expect(box.getByRole("columnheader", { name: /^Priority/ })).toBeInViewport();
    await expect(box.getByRole("row", { name: "#1 Priority number 1" })).not.toBeInViewport();
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): an empty page, the first priority written through it, and a reload that reads it.
test("with no priority the page reads the server's sentence, and writes the first one", async ({
  browser,
}) => {
  // No seed: the row starts from the emptied database (`e2e/test.ts`), which holds no priority.
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);
    await expect(page.getByRole("heading", { level: 1, name: "Priorities" })).toBeVisible();

    // The sentence is the server's own, and no table is drawn where there is nothing to order.
    // The rail's entry carries no number: nothing is off a list that does not exist.
    await expect(page.getByText(NONE_LIVE, { exact: true })).toBeVisible();
    await expect(page.getByRole("table", { exact: true, name: "Priorities table" })).toHaveCount(0);
    await expect(railEntry(page).locator("span")).toHaveCount(2);

    // The page with no priority is where the first is written, with the control the table's
    // toolbar carries once there is one.
    const added = "Ship the daemon everywhere";
    await page.getByRole("button", { exact: true, name: "New priority" }).click();
    await page.getByLabel("Title of the new priority").fill(added);
    await page
      .locator('form[aria-label="New priority"]')
      .getByRole("button", { name: "Add priority" })
      .click();
    await expectRowOrder(section(page, "Priorities"), [`#1 ${added}`]);
    await expect(page.getByText(NONE_LIVE, { exact: true })).toHaveCount(0);

    // The store's, not the page's: read back from the server, and from a page loaded again.
    await expect
      .poll(async () => (await listPriorities()).priorities?.map((row) => row.title))
      .toEqual([added]);
    await page.reload();
    await expectRowOrder(section(page, "Priorities"), [`#1 ${added}`]);
    await expect(page.getByText(NONE_LIVE, { exact: true })).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): a priority written through the page's form, its row read, and a refused create.
test("a long title through the New priority form lands under its cut id", async ({ browser }) => {
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);

    // The form sends the title alone, so the id is the one the server derives from it.
    await page.getByRole("button", { exact: true, name: "New priority" }).click();
    await page.getByLabel("Title of the new priority").fill(LONG_CASE.title);
    await page
      .locator('form[aria-label="New priority"]')
      .getByRole("button", { name: "Add priority" })
      .click();

    // The table holds the whole title, and the title's link opens the page of the cut id.
    await expectRowOrder(section(page, "Priorities"), [`#1 ${LONG_CASE.title}`]);
    const row = page.getByRole("row", { exact: true, name: `#1 ${LONG_CASE.title}` });
    await expect(row.getByRole("link", { exact: true, name: LONG_CASE.title })).toHaveAttribute(
      "href",
      `/priorities/${LONG_CASE.id}`
    );

    // An id the caller names is not cut: one character past the bound is refused, by the bound.
    // `page.request` sends the page's session cookie and no `Origin`, and the server refuses a
    // cookie-authenticated write whose `Origin` is not the dashboard's (enforceCookieOrigin).
    const refused = await page.request.post("/api/v1/priorities", {
      data: { id: "n".repeat(PRIORITY_ID_MAX + 1), title: "Name the id by hand" },
      headers: { Origin: dashboardOrigin },
    });
    expect(refused.status()).toBe(400);
    const body = await refused.json();
    expect(body.code).toBe("PRIORITY_INPUT");
    expect(body.error).toContain(String(PRIORITY_ID_MAX));
    expect((await listPriorities()).priorities?.map((stored) => stored.id)).toEqual([LONG_CASE.id]);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, three writes a person makes on a priority's page and on a row, and a
// reload that reads them back.
test("a priority row opens its description, its title its page, and what is written there reads back on the row after a reload", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);

    // An opened row shows the priority's description and nothing more: the tab draws the one
    // table, so the keys a table is walked with are its alone, and `j` on a focused priority
    // moves to the next priority.
    await page.getByRole("button", { name: `Open #3 ${seed.titles[2]}` }).click();
    await expect(
      page.getByRole("textbox", { exact: true, name: "Description of #3" })
    ).toBeVisible();
    await expect(page.getByRole("table")).toHaveCount(1);
    await expect(page.locator('section[aria-label^="Work advancing"]')).toHaveCount(0);
    await page.locator(`tr[data-issue-row="${seed.ids[1]}"]`).focus();
    await page.keyboard.press("j");
    await expect(page.locator(`tr[data-issue-row="${seed.ids[2]}"]`)).toBeFocused();
    await page.getByRole("button", { name: `Close #3 ${seed.titles[2]}` }).click();

    // The work is on the priority's own page, which the title opens: there the work table is
    // the one table, and the same keys walk its rows.
    await page.getByRole("link", { exact: true, name: seed.titles[2] }).click();
    await expect(page).toHaveURL(`/priorities/${seed.ids[2]}`);
    const work = section(page, "Work advancing this priority");
    await expectRowNames(work, [
      `${seed.archive} Ledger archive`,
      `${seed.child} Ledger client migration`,
      `${seed.cutover} Ledger cut-over`,
      `${seed.ledger} Ledger read path`,
    ]);
    const workRows = work.locator("tr[data-issue-row]");
    const firstKey = await workRows.first().getAttribute("data-issue-row");
    const secondKey = await workRows.nth(1).getAttribute("data-issue-row");
    await workRows.first().focus();
    await page.keyboard.press("j");
    await expect(work.locator(`tr[data-issue-row="${secondKey}"]`)).toBeFocused();
    await page.keyboard.press("k");
    await expect(work.locator(`tr[data-issue-row="${firstKey}"]`)).toBeFocused();
    await page.getByRole("link", { name: "‹ Priorities" }).click();
    await expect(page.getByRole("table", { name: "Priorities table" })).toBeVisible();

    // Renamed and described on its own page, which its title opens: no edit mode and no Save
    // there, so leaving each field writes it, and the list it is read from again names it so.
    // The title reads as the page's heading until a click opens it.
    const renamed = "Cut the review queue in half";
    const explained = "Every review is answered within a day.";
    await page.getByRole("link", { exact: true, name: seed.titles[1] }).click();
    await expect(page).toHaveURL(`/priorities/${seed.ids[1]}`);
    const ownTitle = await openTitle(page);
    await ownTitle.fill(renamed);
    await ownTitle.blur();
    await expect(titleHeading(page)).toHaveText(renamed);
    const ownEditor = page.getByRole("textbox", { exact: true, name: "Description" });
    await ownEditor.click();
    await page.keyboard.type(explained);
    await ownEditor.blur();
    await page.getByRole("link", { name: "‹ Priorities" }).click();
    await expect(page.getByRole("row", { name: `#2 ${renamed}` })).toBeVisible();

    // Described in the row that holds it too, in the same documents' editor, every paragraph of
    // it, and written where it is read. It is the editor Dispatch's documents are written in, as
    // on the priority's own page, and not a plain text box: a selection in it offers no comment,
    // suggestion or ask, since a priority takes none.
    const described = "The fleet is one build, or the rollout is not done.";
    const editor = page.getByRole("textbox", { exact: true, name: "Description of #1" });
    const finished = "It is finished when no machine is left on the old build";
    await page.getByRole("button", { name: `Open #1 ${seed.titles[0]}` }).click();
    await expect(editor).toContainText(finished);
    await expect(editor).toHaveClass(/\bProseMirror\b/);
    await expect(editor).toHaveAttribute("contenteditable", "true");
    // The row's column wraps the paragraph over several lines, and End and Shift+Home select one
    // of them: a run of the paragraph's words, which is what a selection here is.
    await editor.getByText(finished).click();
    await page.keyboard.press("End");
    await page.keyboard.press("Shift+Home");
    const paragraph =
      "It is finished when no machine is left on the old build and nothing is waiting on one.";
    await expect
      .poll(() => page.evaluate(() => String(window.getSelection()).trim().length))
      .toBeGreaterThan(0);
    expect(paragraph).toContain((await page.evaluate(() => String(window.getSelection()))).trim());
    await expect(actionBar(page)).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^(Comment|Suggest|Ask)$/ })).toHaveCount(0);
    await page.keyboard.press("ControlOrMeta+a");
    await page.keyboard.type(described);
    await editor.blur();
    await expect(editor).toHaveText(described);

    // Each is the store's now, not the page's: read back from the server, and then from a page
    // that was loaded again, on the rows of the list.
    const stored = async (id: string) =>
      (await listPriorities()).priorities?.find((row) => row.id === id);
    await expect
      .poll(async () => {
        const second = await stored(seed.ids[1]);
        return [second?.title, second?.description, (await stored(seed.ids[0]))?.description];
      })
      .toEqual([renamed, `${explained}\n`, `${described}\n`]);
    await page.reload();
    await expectRowOrder(section(page, "Priorities"), [
      `#1 ${seed.titles[0]}`,
      `#2 ${renamed}`,
      `#3 ${seed.titles[2]}`,
    ]);
    await page.getByRole("button", { name: `Open #2 ${renamed}` }).click();
    await expect(page.getByRole("textbox", { exact: true, name: "Description of #2" })).toHaveText(
      explained
    );
    await page.getByRole("button", { name: `Open #1 ${seed.titles[0]}` }).click();
    await expect(editor).toHaveText(described);
    // The store's text: what the reader wrote, and no Proof mark of the editor's own.
    expect((await stored(seed.ids[0]))?.description).not.toContain("data-proof");

    // What the page does not say about a priority (INFRA-2947, R10): no date it is wanted by,
    // no portion of it done, and no word judging how it is going. Every `<time>` the page draws
    // is a moment that has passed, and no relative label reads forwards. Each absence is read on
    // each tab once the parts that carry dates and counts have drawn - the opened description
    // and each fault list, each a read of its own - since a page still loading them would pass
    // it on text it never drew.
    await expectNoForecast(page);
    await chooseTab(page, "none");
    await expect(
      page
        .getByRole("table", { exact: true, name: NO_PRIORITY_TABLE })
        .locator("tr[data-issue-row]")
    ).toHaveCount(4);
    await expectNoForecast(page);
    await chooseTab(page, "parked");
    await expectRowOrder(page.getByRole("table", { exact: true, name: PARKED_TABLE }), [
      `${seed.archive} Ledger archive`,
    ]);
    await expectNoForecast(page);
  } finally {
    await context.close();
  }
});

// @firefox: this row proves that a drag by the grip writes the order in Firefox as in Chromium.
// It does not prove the grip's `setData` line (`priorities-columns.tsx`), which a person's Firefox
// needs, since it starts no HTML drag whose `dragstart` carries no data: Playwright's Firefox
// passes this row with that line taken out. The unit row "dragging a row onto another writes the
// place it landed" (`PrioritiesPage.test.tsx`) is what holds it. @webkit: WebKit dispatches the
// drag's `dragover` and `drop` to the row under it by its own rule, which the row's
// `preventDefault` must meet for the drop to land.
// SLOW(>=2s): the seed, a priority added, a row dragged, and a reload that reads the order back.
test("a priority added through the form and a row dragged write the order, which reads back after a reload @firefox @webkit", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);
    const priorities = section(page, "Priorities");
    await expectRowOrder(priorities, [
      `#1 ${seed.titles[0]}`,
      `#2 ${seed.titles[1]}`,
      `#3 ${seed.titles[2]}`,
    ]);

    // Added, which takes the last place of the order.
    const added = "Keep the fleet on one build";
    await page.getByRole("button", { exact: true, name: "New priority" }).click();
    await page.getByLabel("Title of the new priority").fill(added);
    await page
      .locator('form[aria-label="New priority"]')
      .getByRole("button", { name: "Add priority" })
      .click();
    await expect(page.getByRole("row", { name: `#4 ${added}` })).toBeVisible();

    // And dragged by its grip: the third row onto the second, which writes the place the reader
    // can see.
    const handle = page.getByRole("button", {
      name: `Move #3 ${seed.titles[2]}: drag it, or press the up and down arrows`,
    });
    await expect(handle).toBeVisible();
    await handle.dragTo(page.locator(`tr[data-issue-row="${seed.ids[1]}"]`));
    await expect(page.getByRole("row", { name: `#2 ${seed.titles[2]}` })).toBeVisible();

    // Both are the store's now, not the page's: read back from the server, and then from a page
    // that was loaded again.
    await expect
      .poll(async () => (await listPriorities()).priorities?.map((row) => row.title))
      .toEqual([seed.titles[0], seed.titles[2], seed.titles[1], added]);
    await page.reload();
    await expectRowOrder(priorities, [
      `#1 ${seed.titles[0]}`,
      `#2 ${seed.titles[2]}`,
      `#3 ${seed.titles[1]}`,
      `#4 ${added}`,
    ]);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, the rail's entry followed and held open across two writes, and a third
// read after a reload.
test("the rail's Priorities count falls as work leaves flight and as a parent link moves", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    // Read from another page, because the number is the rail's and not the priorities page's.
    await page.goto(`/issues/${seed.rollout}`);
    const entry = railEntry(page);
    await expect(entry).toBeVisible();
    await expect(entry.locator("span").last()).toHaveText("5");

    // The entry leads to the priorities page, which nothing below reloads until the two counts
    // have fallen. A mark on the document would be gone after a reload, so its presence then
    // says there was none.
    await entry.click();
    await expect(page).toHaveURL(PRIORITIES_PATH);
    await expect(page.getByRole("heading", { level: 1, name: "Priorities" })).toBeVisible();
    // The page it opens is the order's tab, the one an address naming none opens.
    await expect(page.getByRole("tab", { name: TABS.order })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    await page.evaluate(() => {
      document.documentElement.dataset.heldOpen = "yes";
    });

    // Four in flight under no priority plus one parked. An issue that leaves `in_progress` is
    // no longer in flight, so the first fault loses it - with no reload: the event that moved
    // it refreshes the two counts (`web/src/api/event-refresh.ts`).
    await patchIssue(seed.clock, { status: "done" });
    await expect(entry.locator("span").last()).toHaveText("4");

    // And an issue in flight under no priority placed under a parent that advances #1 now
    // advances #1 through it (INFRA-2947, R4), so it leaves the first fault, and the count
    // falls again.
    await patchIssue(seed.lease, { parent: seed.rollout });
    await expect(entry.locator("span").last()).toHaveText("3");
    expect(await page.evaluate(() => document.documentElement.dataset.heldOpen)).toBe("yes");

    // A deployment that has written no priority has nothing off a list that does not exist, so
    // the entry carries no number: its label and its line, and no badge beside them.
    for (const id of seed.ids) await endPriority(id);
    await page.reload();
    await expect(railEntry(page)).toBeVisible();
    await expect(railEntry(page).locator("span")).toHaveCount(2);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, two people's pages, a rename made on one and a delete beside it, each
// read on the other with no reload.
test("another person's rename and delete of a priority redraw an open page with no reload", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  // #3's one issue in flight leaves flight first, so the delete below moves no issue into the
  // first fault list, and what the rail counts can only fall by the work parked under #3.
  await patchIssue(seed.ledger, { status: "todo" });
  const alice = await asUser(browser, "alice");
  const bob = await asUser(browser, "bob");
  try {
    // Reader A: alice, on the priorities page, which nothing below reloads. A mark on the
    // document would be gone after a reload, so its presence at the end says there was none.
    const reader = await alice.newPage();
    await priorityPage(reader);
    await reader.goto(PRIORITIES_PATH);
    await reader.evaluate(() => {
      document.documentElement.dataset.heldOpen = "yes";
    });
    const priorities = section(reader, "Priorities");
    const parkedTable = reader.getByRole("table", { exact: true, name: PARKED_TABLE });
    const childRow = parkedTable.getByRole("row", {
      exact: true,
      name: `${seed.archive} Ledger archive`,
    });
    const badge = railEntry(reader).locator("span").last();
    await expectRowOrder(priorities, [
      `#1 ${seed.titles[0]}`,
      `#2 ${seed.titles[1]}`,
      `#3 ${seed.titles[2]}`,
    ]);
    // Four in flight under no priority plus the one parked.
    await expect(badge).toHaveText("5");

    // Reader B: bob renames #3 on its own page. Alice's row takes the new title, and so does
    // the parked work's Advances cell on its tab, which names a priority by the title a person
    // wrote: the title the page's one read of the priorities holds, which choosing a tab does
    // not read again.
    const renamed = "Retire the old ledger for good";
    const writer = await bob.newPage();
    await writer.goto(`/priorities/${seed.ids[2]}`);
    const writersTitle = await openTitle(writer);
    await writersTitle.fill(renamed);
    await writersTitle.blur();
    await expectRowOrder(priorities, [
      `#1 ${seed.titles[0]}`,
      `#2 ${seed.titles[1]}`,
      `#3 ${renamed}`,
    ]);
    await chooseTab(reader, "parked");
    await expect(childRow).toBeVisible();
    // The Advances column opens shown on the parked tab: the priority each row is parked under,
    // whose chip carries the title in its name, and which draws the title after the chip.
    const parkedUnder = await cellUnder(parkedTable, childRow, "Advances");
    await expect(parkedUnder.getByRole("link")).toHaveAccessibleName(`#3 ${renamed}`);
    await expect(parkedUnder.getByText(renamed, { exact: true })).toBeVisible();

    // Then bob retires #3, over the API, since neither page offers a delete. The archive
    // advanced only #3, so it is no fault at all now: it leaves the parked list in front of
    // alice, the tab's count and the rail's fall by it, and the order loses the row.
    await endPriority(seed.ids[2], { login: "bob" });
    await expectRowNames(parkedTable, []);
    await expect(reader.getByRole("tab", { name: "Parked priority work 0" })).toBeVisible();
    await expect(badge).toHaveText("4");
    await chooseTab(reader, "order");
    await expectRowOrder(priorities, [`#1 ${seed.titles[0]}`, `#2 ${seed.titles[1]}`]);

    expect(await reader.evaluate(() => document.documentElement.dataset.heldOpen)).toBe("yes");
  } finally {
    await Promise.all([alice.close(), bob.close()]);
  }
});

// SLOW(>=2s): the seed, two writes read back from the server, and a reload.
test("a priority's own page writes its title and description and lists only its work", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  // A description with a heading and a list, so what the page draws of it is read as those
  // elements rather than as the markdown source it is stored as.
  const outline = "# Why\n\n- Every machine runs the daemon\n- Nothing waits on the old build";
  await patchPriority(seed.ids[0], { description: outline });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/priorities/${seed.ids[0]}`);

    await expect(page.getByRole("link", { name: "‹ Priorities" })).toBeVisible();
    // The header's heading is the title, whole, and no field shows until a click opens it. The
    // owner is a chip under it, which a click opens into its field; left, it is the chip again.
    await expect(titleHeading(page)).toHaveText(seed.titles[0]);
    await expect(page.getByRole("textbox", { name: "Title" })).toHaveCount(0);
    await page.getByRole("button", { name: "Owner alice" }).click();
    const owner = page.getByRole("combobox", { name: "Owner" });
    await expect(owner).toHaveValue("alice");
    await owner.blur();
    await expect(owner).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Owner alice" })).toBeVisible();
    // One page and no tab strip: everything of the priority is on this page, and what happened
    // to it is in the margin beside it.
    await expect(page.getByRole("tablist")).toHaveCount(0);
    await expect(page.getByRole("tab")).toHaveCount(0);

    const retitled = "Ship the daemon to every machine";
    const title = await openTitle(page);
    await title.fill(retitled);
    await title.blur();

    // The description is the editor Dispatch's documents are written in (`features/doc/
    // editor.ts`), named for the description: the markdown it is, drawn as a heading and a
    // list, with no source and no edit mode.
    const editor = page.getByRole("textbox", { exact: true, name: "Description" });
    await expect(editor).toHaveClass(/\bProseMirror\b/);
    await expect(editor).toHaveAttribute("contenteditable", "true");
    await expect(editor.getByRole("heading", { name: "Why" })).toBeVisible();
    await expect(editor.getByRole("listitem")).toHaveText([
      "Every machine runs the daemon",
      "Nothing waits on the old build",
    ]);

    // A selection in it offers no comment, suggestion or ask, since a priority takes none: the
    // documents' selection bar, which their editor draws as it is built, is not drawn at all.
    // The selection is the keyboard's, made once the click has put the caret in the editor:
    // ProseMirror writes its own selection back over the page's just after it takes the focus.
    await editor.getByText("Nothing waits on the old build").click();
    await page.keyboard.press("End");
    await page.keyboard.press("Shift+Home");
    await expect
      .poll(() => page.evaluate(() => String(window.getSelection())))
      .toBe("Nothing waits on the old build");
    await expect(actionBar(page)).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^(Comment|Suggest|Ask)$/ })).toHaveCount(0);

    // A line typed after the list, with one word made bold through the editor's own Mod-b, and
    // left: leaving it writes it at once.
    const line = "The roll-back is written down";
    await editor.getByRole("listitem").last().click();
    await page.keyboard.press("End");
    await page.keyboard.press("Enter");
    await page.keyboard.type("The ");
    await page.keyboard.press("ControlOrMeta+b");
    await page.keyboard.type("roll-back");
    await page.keyboard.press("ControlOrMeta+b");
    await page.keyboard.type(" is written down");
    await expect(editor.getByRole("listitem").last()).toHaveText(line);
    await expect(editor.getByRole("listitem").last().locator("strong")).toHaveText("roll-back");
    await editor.blur();

    // The store holds both, the line as markdown with its word in `**`, as the editor writes a
    // list, and nothing the reader did not write: no Proof mark of the editor's own, such as the
    // authorship of what was typed (`<span data-proof="authored" ...>`). The write has landed
    // once the store holds the last words typed; the page loaded again draws the text from the
    // store, and what is read back is the store's, not the editor's.
    const described =
      "# Why\n\n* Every machine runs the daemon\n* Nothing waits on the old build\n" +
      "* The **roll-back** is written down\n";
    const stored = async () =>
      (await listPriorities()).priorities?.find((one) => one.id === seed.ids[0]);
    await expect.poll(async () => (await stored())?.description ?? "").toContain("written down");
    await page.reload();
    await expect(titleHeading(page)).toHaveText(retitled);
    await expect(editor.getByRole("listitem").last()).toHaveText(line);
    await expect(editor.getByRole("listitem").last().locator("strong")).toHaveText("roll-back");
    const row = await stored();
    expect(row?.description).not.toContain("data-proof");
    expect([row?.title, row?.description]).toEqual([retitled, described]);

    // The keyboard reaches the editor as it reaches any field: the next stop after the
    // disclosure above it.
    const description = section(page, "Description");
    await description.getByRole("button", { exact: true, name: "Description" }).focus();
    await page.keyboard.press("Tab");
    await expect(editor).toBeFocused();
    await editor.blur();

    // The work is this priority's and nothing else's: the two issues that advance it, and
    // neither of the issues under the other two.
    const work = section(page, "Work advancing this priority");
    await expectRowNames(work, [
      `${seed.rollout} Daemon rollout to the fleet`,
      `${seed.smoke} Daemon smoke suite`,
    ]);

    // The description is a disclosure, and it shuts and opens; shut, the work moves up into
    // the room it left.
    const open = await topOf(work);
    await description.getByRole("button", { exact: true, name: "Description" }).click();
    await expect(editor).toHaveCount(0);
    await expect.poll(() => topOf(work)).toBeLessThan(open);
    await description.getByRole("button", { exact: true, name: "Description, closed" }).click();
    await expect(editor.getByRole("listitem").last()).toHaveText(line);
    await expect.poll(() => topOf(work)).toBe(open);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, two reloads, and two writes read back from the server.
test("what a reader types into a priority survives a reload made before the typing rests", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    // The page's own clock, held while the reader types (`pauseClock`): the half-second timer
    // cannot write then, so only leaving the page can. The reloaded page reads the priorities on
    // its own timers, so its clock runs again once it has loaded.
    await page.clock.install();
    await page.goto(`/priorities/${seed.ids[0]}`);
    const stored = async () =>
      (await listPriorities()).priorities?.find((one) => one.id === seed.ids[0]);

    // The title, typed and the page reloaded at once: inside the half second a write waits for
    // the typing to rest. Leaving by a reload or by closing the tab is leaving the field as much
    // as a click elsewhere is, and what the reader typed is theirs.
    // The title is the header's heading, which a click opens into the box it is written in.
    const title = titleHeading(page);
    await expect(title).toHaveText(seed.titles[0]);
    await pauseClock(page);
    await title.click();
    await expect(page.getByRole("textbox", { name: "Title" })).toBeFocused();
    await page.keyboard.press("End");
    await page.keyboard.type(" now");
    await page.reload();
    await page.clock.resume();
    await expect.poll(async () => (await stored())?.title).toBe(`${seed.titles[0]} now`);

    // The description, the same way.
    const editor = page.getByRole("textbox", { exact: true, name: "Description" });
    await expect(editor).toContainText("nothing is waiting on one");
    await pauseClock(page);
    await editor.click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type(" Typed before the reload.");
    await page.reload();
    await page.clock.resume();
    await expect
      .poll(async () => (await stored())?.description ?? "")
      .toContain("Typed before the reload.");
    await expect(editor).toContainText("Typed before the reload.");
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, a description typed and the tab closed at once, and the server read.
test("what a reader types into a priority's description is written as the tab closes", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    // The page's clock is held while the reader types (`pauseClock`): the half-second timer
    // cannot write then, so only the tab closing can, inside 500 ms of the last key.
    await page.clock.install();
    await page.goto(`/priorities/${seed.ids[0]}`);
    const editor = page.getByRole("textbox", { exact: true, name: "Description" });
    await expect(editor).toContainText("nothing is waiting on one");
    await pauseClock(page);
    await editor.click();
    await page.keyboard.press("ControlOrMeta+End");
    await page.keyboard.type(" Typed before the tab closed.");
    // Closing the tab is leaving the field: the write goes out as the page goes (`pagehide`,
    // sent with `keepalive`), since nothing of the tab is left to send it later.
    await page.close();
    await expect
      .poll(
        async () =>
          (await listPriorities()).priorities?.find((one) => one.id === seed.ids[0])?.description ??
          ""
      )
      .toContain("Typed before the tab closed.");
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, a write held until another person's lands, and a reload that resumes one.
test("a priority write another person's change refused leaves no 'Not saved' line and no Retry of the refused text", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    // Every write of #1 the page sends, with its answer: a body the server refused 409
    // PRIORITY_CHANGED names a text it went over, and sending that same body again is refused
    // the same way, whoever presses it.
    const refused: string[] = [];
    page.on("response", (answer) => {
      const sent = answer.request();
      if (sent.method() === "PATCH" && sent.url().endsWith(`/api/v1/priorities/${seed.ids[0]}`)) {
        if (answer.status() === 409) refused.push(sent.postData() ?? "");
      }
    });
    // The title is the header's heading, which a click opens into the box it is written in.
    const heading = titleHeading(page);
    const title = page.getByRole("textbox", { name: "Title" });
    const notice = page.getByRole("alert", { name: "Title, changed by another person" });

    // Bob's rename lands after alice's edit began and before her write reaches the server.
    let first = true;
    await page.route(`**/api/v1/priorities/${seed.ids[0]}`, async (route) => {
      if (route.request().method() === "PATCH" && first) {
        first = false;
        await patchPriority(seed.ids[0], { title: "Bob's rename" }, { login: "bob" });
      }
      await route.continue();
    });
    await page.goto(`/priorities/${seed.ids[0]}`);
    await expect(heading).toHaveText(seed.titles[0]);
    await heading.click();
    await page.keyboard.press("End");
    await page.keyboard.type(" first");
    await title.blur();
    // The notice holds her text. That does not say the page took the 409 - the field draws the
    // same notice when Bob's text reaches the page while her write is still out - so the count
    // waits for that answer itself.
    await expect(notice).toContainText(`${seed.titles[0]} first`);
    await expect.poll(() => refused.length).toBe(1);
    // She uses her text, and it lands over Bob's.
    const used = page.waitForResponse(
      (answer) =>
        answer.request().method() === "PATCH" &&
        answer.url().endsWith(`/api/v1/priorities/${seed.ids[0]}`)
    );
    await notice.getByRole("button", { name: "Use my text" }).click();
    await used;
    await expect
      .poll(
        async () =>
          (await listPriorities()).priorities?.find((one) => one.id === seed.ids[0])?.title
      )
      .toBe(`${seed.titles[0]} first`);
    // The page has taken Dispatch's answer to that write once the shell's line of pending
    // writes, which names the write from its press until its answer is taken, is gone: the
    // answer has reached the page by now, so only then does the absence of a refusal say
    // anything.
    await expect(page.getByTestId("pending-writes")).toHaveCount(0);
    // Her text is written, so nothing on the page says it was not - neither a refusal nor a
    // conflict on the shell's line - and no control on it sends the refused body again.
    await expect(page.getByText(/^Not saved/)).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(0);
    await page.unroute(`**/api/v1/priorities/${seed.ids[0]}`);

    // A write a reload cut off, resumed after Bob wrote again: refused 409. Whatever the page
    // offers then, it does not send that refused body a second time.
    let hold = true;
    await page.route(`**/api/v1/priorities/${seed.ids[0]}`, async (route) => {
      if (route.request().method() === "PATCH" && hold) {
        hold = false;
        return;
      }
      await route.continue();
    });
    await heading.click();
    await page.keyboard.press("End");
    await page.keyboard.type(" second");
    await title.blur();
    await patchPriority(seed.ids[0], { title: "Bob's second rename" }, { login: "bob" });
    await page.reload();
    await expect(heading).toHaveText("Bob's second rename");
    await expect.poll(() => refused.length).toBe(2);
    // The page has taken the resumed write's 409 once its notice holds her text: after the
    // reload no write of hers is out, so only that refusal draws it. Only then does the line's
    // absence say anything.
    await expect(notice).toContainText(`${seed.titles[0]} first second`);
    await expect(page.getByText(/^Not saved/)).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(0);
    expect(new Set(refused).size).toBe(refused.length);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed and three issue pages.
test("an issue's header names the priorities it advances and links to each", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/issues/${seed.rollout}`);
    // Each priority is a chip of its place alone, in the header's state row directly after the
    // P0-P3 tag, with its title in its name and its tooltip: no word of the header says
    // "Advances", and no row of the header is the priorities'. The details row under it, which
    // scrolls sideways, holds none of them.
    const header = page.getByTestId("issue-header");
    const stateRow = header.getByTestId("issue-state-actions");
    const line = chipsLine(page);
    const link = line.getByRole("link", { name: `#1 ${seed.titles[0]}` });
    await expect(link).toBeVisible();
    await expect(link).toHaveText("#1");
    await expect(link).toHaveAttribute("title", seed.titles[0]);
    // An own chip is a filled badge with a solid border; an inherited one, below, is dashed.
    await expect(link.locator("span")).toHaveCSS("border-top-style", "solid");
    await expect(header).not.toContainText("Advances");
    await expect(
      header.getByTestId("issue-metadata-rail").getByTestId("issue-advances")
    ).toHaveCount(0);
    expect(
      await line.evaluate(
        (element) =>
          element.previousElementSibling?.querySelector("select")?.getAttribute("aria-label") ??
          null
      )
    ).toBe(`Priority of ${seed.rollout}`);
    const tagBox = await stateRow.getByLabel(/^Priority of /).boundingBox();
    const lineBox = await line.boundingBox();
    expect(lineBox?.x ?? Number.NaN).toBeGreaterThan(tagBox?.x ?? Number.NaN);
    expect(lineBox?.width ?? Number.POSITIVE_INFINITY).toBeLessThan(120);
    // That row wraps rather than scrolls at 1440 by 900: nothing of it lies past its edge, and the
    // chips stand inside it on the tag's line. Below 768 px the state row has no room left on its
    // first line, so the chips stand first in the details row instead, which scrolls: the first
    // item of that row, inside the viewport with the row unscrolled, and the state row holds none.
    const rail = header.getByTestId("issue-metadata-rail");
    for (const size of [{ height: 900, width: 1440 }, PHONE]) {
      await page.setViewportSize(size);
      await expect
        .poll(() =>
          stateRow.evaluate((row) => {
            const style = getComputedStyle(row);
            return {
              overflow: style.overflowX,
              past: row.scrollWidth - row.clientWidth,
              wrap: style.flexWrap,
            };
          })
        )
        .toEqual({ overflow: "visible", past: 0, wrap: "wrap" });
      if (size === PHONE) {
        await expect(chipsLine(page)).toHaveCount(0);
        const inRail = rail.getByTestId("issue-advances");
        await expect(inRail.getByRole("link", { name: `#1 ${seed.titles[0]}` })).toBeVisible();
        expect(
          await rail.evaluate(
            (element) => element.firstElementChild?.getAttribute("data-testid") ?? null
          )
        ).toBe("issue-advances");
        expect(await rail.evaluate((element) => element.scrollLeft)).toBe(0);
        const railBox = await rail.boundingBox();
        const inRailBox = await inRail.boundingBox();
        const right = (inRailBox?.x ?? Number.NaN) + (inRailBox?.width ?? 0);
        expect(inRailBox?.x ?? Number.NaN).toBeGreaterThanOrEqual(0);
        expect(right).toBeLessThanOrEqual((railBox?.x ?? Number.NaN) + (railBox?.width ?? 0));
        expect(right).toBeLessThanOrEqual(PHONE.width);
      } else {
        const rowBox = await stateRow.boundingBox();
        const chipsBox = await line.boundingBox();
        expect((chipsBox?.x ?? Number.NaN) + (chipsBox?.width ?? 0)).toBeLessThanOrEqual(
          (rowBox?.x ?? Number.NaN) + (rowBox?.width ?? 0)
        );
        const statusBox = await stateRow.getByLabel("Status", { exact: true }).boundingBox();
        const statusBottom = (statusBox?.y ?? Number.NaN) + (statusBox?.height ?? 0);
        expect(chipsBox?.y ?? Number.NaN).toBeLessThan(statusBottom);
      }
      await expect.poll(() => documentOverflow(page)).toBeLessThanOrEqual(0);
    }
    await priorityPage(page);
    await link.click();
    await expect(page).toHaveURL(`/priorities/${seed.ids[0]}`);
    await expect(titleHeading(page)).toHaveText(seed.titles[0]);

    // An issue that advances none carries no line at all: no label, no name, only the trigger of
    // the picker that places it under a priority.
    await page.goto(`/issues/${seed.clock}`);
    await expect(chipsLine(page).getByRole("link")).toHaveCount(0);
    await expect(chipsLine(page)).not.toContainText("Advances");
    await expect(page.getByRole("button", { name: "Edit priorities" })).toBeVisible();

    // A priority an issue inherits says so, and the picker offers no row that would take it off:
    // that relation is the ancestor's, and a relation on a child places nothing above it. The
    // row the child could add is read first, so the absence is read off a list that has loaded.
    await page.goto(`/issues/${seed.child}`);
    const inherited = chipsLine(page);
    const parked = inherited.getByRole("link", { name: `#3 ${seed.titles[2]}` });
    await expect(parked).toBeVisible();
    await expect(parked).toHaveText("#3");
    await expect(parked).toHaveAccessibleName(`#3 ${seed.titles[2]}, inherited`);
    await expect(parked).toHaveAttribute(
      "title",
      `${seed.titles[2]}, inherited from an issue above`
    );
    // It draws unlike an own chip: a dashed, muted border, where an own chip's is solid.
    await expect(parked.locator("span")).toHaveCSS("border-top-style", "dashed");
    await page.getByRole("button", { name: "Edit priorities" }).click();
    const offered = page.getByRole("listbox", { name: "Priorities options" });
    await expect(
      offered.getByRole("option", { exact: true, name: `#1 ${seed.titles[0]}` })
    ).toBeVisible();
    await expect(offered.getByRole("option", { name: `#3 ${seed.titles[2]}` })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(offered).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, five writes, and the margin read for its history and for what it lacks.
test("a priority's margin is its history alone, newest first, each act with who made it and when", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  // Two acts after the making, in a known order, so "newest first" is a claim with two ends.
  await patchPriority(seed.ids[0], { owner: "bob" });
  const retitled = "Ship the daemon everywhere, and prove it";
  await patchPriority(seed.ids[0], { title: retitled });
  // Then three saves of the description by one person, one after another, as the page makes
  // them while a reader writes it: the history keeps each of them.
  for (const text of ["Every machine.", "Every machine runs it.", "Every machine runs the daemon."])
    await patchPriority(seed.ids[0], { description: text });

  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/priorities/${seed.ids[0]}`);
    const margin = page.getByRole("complementary", { name: "Margin" });
    const entries = margin.getByRole("listitem");
    // The history has answered once the rename is drawn, so what the margin lacks below is read
    // off a margin that is all there rather than off its skeleton.
    await expect(margin).toContainText(`wrote the title “${retitled}”`);

    // The page carries no tab strip.
    await expect(page.getByRole("tablist")).toHaveCount(0);
    await expect(page.getByRole("tab")).toHaveCount(0);

    // The margin carries no control of states and no view that can only be empty: a priority
    // carries no comments, questions or pins until dispatch://INFRA-1098 makes its description
    // a document (dispatch://INFRA-2947/ask/e31ca6f8-216b-4729-933a-333c949a2a59).
    await expect(margin.getByRole("combobox")).toHaveCount(0);
    await expect(margin.getByRole("listbox")).toHaveCount(0);
    await expect(margin.locator("select")).toHaveCount(0);
    for (const word of ["Words anyone wrote", "Questions", "Pinned"]) {
      await expect(margin).not.toContainText(word);
    }
    // Nor a pin control, nor an entry for a document: the description is no document.
    await expect(margin.getByRole("button", { name: /pin/i })).toHaveCount(0);
    await expect(margin.getByRole("link", { name: /pin|document/i })).toHaveCount(0);
    await expect(margin).not.toContainText(/document/i);

    // The server holds every act, newest first: the three saves of the description, the
    // rename, the owner write, then everything before them. The order is the record's own, not
    // the wall clock's: each write stamps its own moment and the rows are kept in the order they
    // reached the table, so two writes a millisecond apart can carry their moments the other way
    // round, which a comparison of timestamps would read as a failure of an order that is right.
    const acts = await getPriorityHistory(seed.ids[0]);
    expect(acts.slice(0, 5).map((act) => act.field)).toEqual([
      "description",
      "description",
      "description",
      "title",
      "owner",
    ]);
    expect(acts[3]?.value).toBe(retitled);
    expect(acts[4]?.value).toBe("bob");
    // The margin lists them in that order, with a run of writes of one field by one person -
    // here the three saves - read as one entry, its newest. A write of another field, or by
    // another person, ends a run. So each entry is read against the act the server holds at
    // its place once runs are folded: who made it, and the moment it carries.
    const listed = acts.filter((act, at) => {
      const newer = acts[at - 1];
      return (
        newer === undefined ||
        act.field === "advances" ||
        newer.field !== act.field ||
        newer.actor.kind !== act.actor.kind ||
        newer.actor.id !== act.actor.id
      );
    });
    expect(listed.slice(0, 3)).toEqual([acts[0], acts[3], acts[4]]);
    await expect(entries).toHaveCount(listed.length);
    for (const [place, act] of listed.entries()) {
      const entry = entries.nth(place);
      await expect(entry).toContainText(act.actor.id);
      await expect(entry.locator("time")).toHaveAttribute("datetime", act.at);
    }
    await expect(entries.nth(0)).toContainText("wrote the description");
    await expect(entries.nth(1)).toContainText(`wrote the title “${retitled}”`);
    await expect(entries.nth(2)).toContainText("wrote the owner “bob”");
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, a relation written and retracted from an issue, and three pages that
// read each of those two writes.
test("a person places an issue under a priority from the issue and takes it off again", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/issues/${seed.clock}`);

    // The picker after the chips in the header's state row, as the labels' is in the row under
    // it: a tick stages the set and the close writes it.
    const picker = chipsLine(page).getByRole("button", { name: "Edit priorities" });
    const option = page
      .getByRole("listbox", { name: "Priorities options" })
      .getByRole("option", { exact: true, name: `#2 ${seed.titles[1]}` });
    await picker.click();
    await option.click();
    await expect(option).toHaveAttribute("aria-selected", "true");
    await picker.click();
    const named = chipsLine(page).getByRole("link", { name: `#2 ${seed.titles[1]}` });
    await expect(named).toBeVisible();
    await page.reload();
    await expect(named).toBeVisible();

    // The priority's own page lists the issue among its work, which is the same relation read
    // from the other end.
    await page.goto(`/priorities/${seed.ids[1]}`);
    const work = section(page, "Work advancing this priority");
    await expectRowNames(work, [
      `${seed.clock} Audit the clock`,
      `${seed.queue} Review queue triage`,
    ]);

    // Taken off from the same picker, and gone from both ends after a reload.
    await page.goto(`/issues/${seed.clock}`);
    await expect(named).toBeVisible();
    await picker.click();
    await expect(option).toHaveAttribute("aria-selected", "true");
    await option.click();
    await picker.click();
    await expect(chipsLine(page).getByRole("link")).toHaveCount(0);
    await page.reload();
    await expect(chipsLine(page).getByRole("link")).toHaveCount(0);
    await page.goto(`/priorities/${seed.ids[1]}`);
    await expectRowNames(section(page, "Work advancing this priority"), [
      `${seed.queue} Review queue triage`,
    ]);

    // And the priority's margin holds both acts, newest first, each in the words
    // `PriorityMargin.tsx` writes it in and each naming the person who made it: a relation is
    // an act of the priority as much as of the issue, and the retraction is not the assertion.
    // The margin reads the history apart from the work table above, so each entry is waited
    // for rather than read off a margin that may not have drawn yet.
    const entries = page.getByRole("complementary", { name: "Margin" }).getByRole("listitem");
    await expect
      .poll(() => entries.nth(0).innerText())
      .toContain(`retracted the relation to the issue ${seed.clock}`);
    await expect.poll(() => entries.nth(0).innerText()).toContain("alice");
    await expect
      .poll(() => entries.nth(1).innerText())
      .toContain(`related the issue ${seed.clock}`);
    await expect.poll(() => entries.nth(1).innerText()).toContain("alice");
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, an issue page held open across another person's delete, a write from its
// header, and a reload that reads it back.
test("another person's delete of a priority leaves an open issue page, whose next write lands", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  // The daemon's smoke suite advances #1 already; it states #3 beside it - one relation more,
  // which a write with no revision may assert - so the delete of #3 below leaves the issue one
  // relation of its own for the next write to keep.
  await patchIssue(seed.smoke, { advances: [seed.ids[0], seed.ids[2]] });
  const alice = await asUser(browser, "alice");
  try {
    // The reader: alice, on the issue's page, which nothing below reloads until the write has
    // landed. A mark on the document would be gone after a reload, so its presence then says
    // there was none.
    const reader = await alice.newPage();
    await priorityPage(reader);
    await reader.goto(`/issues/${seed.smoke}`);
    await reader.evaluate(() => {
      document.documentElement.dataset.heldOpen = "yes";
    });
    const line = reader.getByTestId("issue-advances");
    const kept = line.getByRole("link", { name: `#1 ${seed.titles[0]}` });
    const deleted = line.locator(`a[href="/priorities/${seed.ids[2]}"]`);
    await expect(kept).toBeVisible();
    await expect(deleted).toHaveText("#3");
    await expect(deleted).toHaveAccessibleName(`#3 ${seed.titles[2]}`);

    // Another person retires #3 over the API. The open header stops naming it, by its title or
    // by its id, with no reload: the issue's read is fetched again, so the set the next write
    // sends is the one the server holds.
    await endPriority(seed.ids[2], { login: "bob" });
    await expect(deleted).toHaveCount(0);
    await expect(kept).toBeVisible();

    // The reader places the issue under #2 from the header's own picker, which holds #1 ticked
    // and writes on its close. The write sends #1 and #2, which the server takes; a set still
    // naming #3 would be refused, a priority that is gone.
    const picker = reader.getByRole("button", { name: "Edit priorities" });
    const offered = reader.getByRole("listbox", { name: "Priorities options" });
    await picker.click();
    await expect(
      offered.getByRole("option", { exact: true, name: `#1 ${seed.titles[0]}` })
    ).toHaveAttribute("aria-selected", "true");
    await offered.getByRole("option", { exact: true, name: `#2 ${seed.titles[1]}` }).click();
    await picker.click();
    const added = line.getByRole("link", { name: `#2 ${seed.titles[1]}` });
    await expect(added).toBeVisible();
    expect(await reader.evaluate(() => document.documentElement.dataset.heldOpen)).toBe("yes");

    // What the server holds, read back after a reload: #1 and #2, and nothing of #3.
    await expect
      .poll(async () =>
        ((await getIssue(seed.smoke)).advances ?? []).map((entry) => entry.id).sort()
      )
      .toEqual([seed.ids[0], seed.ids[1]].sort());
    await reader.reload();
    await expect(kept).toBeVisible();
    await expect(added).toBeVisible();
    await expect(deleted).toHaveCount(0);
  } finally {
    await alice.close();
  }
});

// SLOW(>=2s): the seed and one page read for what it does not draw.
test("a priority's page offers no versions and no delete, and carries no line about its order", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/priorities/${seed.ids[0]}`);
    const main = page.getByTestId("main-content");
    await expect(titleHeading(page)).toHaveText(seed.titles[0]);

    // The description is an attribute of the node and not a Dispatch document, so nothing here
    // offers its versions, and there is no delete: a priority is retired by its state.
    await expect(main.getByRole("button", { name: /version/i })).toHaveCount(0);
    await expect(main.getByRole("link", { name: /version/i })).toHaveCount(0);
    await expect(main.getByText(/\bv\d+\b/)).toHaveCount(0);
    await expect(main.getByRole("button", { name: /delete/i })).toHaveCount(0);
    await expect(main.getByRole("link", { name: /delete/i })).toHaveCount(0);

    // Its place in the order and the counts of its work are the priorities list's to show
    // (INFRA-3221, R13): this page carries no line about either, and no `#N`. Its way out to the
    // work is one link per project, which reads "Open in" and the project's key.
    await expect(main.getByText(/in the order/)).toHaveCount(0);
    await expect(main.getByText(/^#\d+$/)).toHaveCount(0);
    await expect(
      main.getByText(/\b(Active|In flight|Triage, Todo|Backlog, icebox) \d/)
    ).toHaveCount(0);
    await expect(main.getByRole("link", { exact: true, name: "Open in CORE" })).toHaveAttribute(
      "href",
      /^\/projects\/CORE\/issues\?/
    );
  } finally {
    await context.close();
  }
});
