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

import { issueStatusScale } from "../web/src/theme/classes";
import {
  claimIssue,
  createIssue,
  createPriority,
  createProject,
  endPriority,
  getIssue,
  getPriorityHistory,
  listPriorities,
  patchIssue,
  patchPriority,
} from "./api";
import { expect, type Locator, type Page, test } from "./test";
import { asUser } from "./users";

const PRIORITIES_PATH = "/priorities";

/** The two fault tables, by the names the Issues table gives them: the `label` each fault list of
 *  `PrioritiesPage.tsx` passes, which `features/project/IssuesTable.tsx` names its table by with
 *  " table" after it. */
const NO_PRIORITY_TABLE = "Issues active under no priority table";
const PARKED_TABLE = "Parked priority issues table";

/** What the server says of a deployment with no live priority, in its own words
 *  (`priorityNoneWritten`, `internal/dispatch/api/priority_queries.go`): the empty page draws the
 *  sentence the read answers rather than a sentence of its own. */
const NONE_LIVE =
  "No priority is live now, so the list is empty. Write one, then say which work advances it.";

/** What the margin says of each state but Everything (`PriorityMargin.tsx`). */
const MARGIN_SENTENCES: readonly (readonly [string, string])[] = [
  [
    "Words anyone wrote",
    "A priority carries no comments yet, so nobody has written anything here.",
  ],
  ["Questions", "A priority carries no asks yet, so no question has been asked about this one."],
  ["Pinned", "A priority carries nothing a reader can pin yet."],
];

/** The corpus every row seeds: three priorities in one order, work under each in three
 *  projects, an issue in each of the four in-flight statuses that advances none, and an issue
 *  that advances one through its parent and stands in the backlog. */
interface Seeded {
  /** The three priority ids, in rank order: `P1`, `P2`, `P3` on the page. */
  readonly ids: readonly [string, string, string];
  readonly titles: readonly [string, string, string];
  /** The description `P1` carries, several paragraphs of it. */
  readonly description: string;
  /** `P1`'s work: one issue in flight and one waiting. */
  readonly rollout: string;
  readonly smoke: string;
  /** `P2`'s one issue, in `testing`. */
  readonly queue: string;
  /** `P3`'s work: an issue in `needs_review`, the parent that states the relation, and the
   *  child in `backlog` that inherits it. */
  readonly ledger: string;
  readonly cutover: string;
  readonly child: string;
  /** The one issue active under no priority this file writes to. */
  readonly clock: string;
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
  // is written (`POST /api/v1/priorities`), and these three are read as P1, P2 and P3.
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
  // The child states nothing of its own: it advances `P3` because the issue above it does
  // (INFRA-2947, R4), which is what the parked list's row and the rail's second count read.
  const child = await createIssue({
    parent: cutover.key,
    project: "WEB",
    title: "Ledger client migration",
  });
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
    child: child.key,
    clock: clock.key,
    cutover: cutover.key,
    description: FIRST_DESCRIPTION,
    ids: [first.id, second.id, third.id],
    ledger: ledger.key,
    queue: queue.key,
    rollout: rollout.key,
    smoke: smoke.key,
    titles: [first.title, second.title, third.title],
  };
}

/** A page of alice's at a width the sidebar's rail is drawn at (1280 px, `WIDE_LAYOUT_MIN_PX`
 *  in `e2e/review-routes.ts`), which three rows below read the Priorities entry from. */
async function priorityPage(page: Page): Promise<void> {
  await page.setViewportSize({ height: 900, width: 1280 });
}

const section = (page: Page, label: string): Locator =>
  page.locator(`section[aria-label="${label}"]`);

/** Where a section's box starts down the page, which is what says the page draws the order
 *  first and the faults under it. */
async function topOf(locator: Locator): Promise<number> {
  const box = await locator.boundingBox();
  if (box === null) throw new Error("the section this row reads is not on the page");
  return box.y;
}

/** Every row of a table, by the name a reader hears: `P1 <title>` on the priorities table, whose
 *  rows carry `data-priority-row`, and `<KEY> <title>` on a fault list, whose carry
 *  `data-issue-row`. */
function rowNames(scope: Locator): Promise<string[]> {
  return scope
    .locator("tr[data-issue-row], tr[data-priority-row]")
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
    .locator("tr[data-issue-row], tr[data-priority-row]")
    .evaluateAll(
      (rows, index) =>
        rows.map((row) => row.querySelectorAll("td")[index]?.textContent?.trim() ?? ""),
      at
    );
}

/** The badge beside the rail's Priorities entry, which is its last span; an entry with no count
 *  draws two spans and no third. */
const railEntry = (page: Page): Locator => page.getByRole("link", { name: /^Priorities/ });

// SLOW(>=2s): three projects, three priorities and ten issues, then the whole page read cell by
// cell.
test("the priorities page draws the order first and the two fault lists under it", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);
    await expect(page.getByRole("heading", { level: 1, name: "Priorities" })).toBeVisible();

    // The order itself, which is the page's first part: the rows in rank order, each named by
    // its place and its title.
    const priorities = section(page, "Priorities");
    await expect(page.getByRole("table", { name: "Priorities table" })).toBeVisible();
    await expectRowOrder(priorities, [
      `P1 ${seed.titles[0]}`,
      `P2 ${seed.titles[1]}`,
      `P3 ${seed.titles[2]}`,
    ]);
    // A closed row shows the first line of its description and no more of it.
    const firstRow = priorities.getByRole("row", { name: `P1 ${seed.titles[0]}` });
    await expect(firstRow).toContainText(
      "Every machine of the fleet runs the daemon, or the rollout is not done."
    );
    await expect(firstRow).not.toContainText("It is finished when no machine is left");
    // The counts under each priority: one in flight and one waiting under P1.
    await expect(firstRow.getByRole("cell").nth(3)).toHaveText("1");
    await expect(firstRow.getByRole("cell").nth(4)).toHaveText("1");

    // The agreed picture (dispatch://INFRA-2947/artifact/priorities-round10-png): the
    // priorities, then the work active under none, then the priority work parked.
    const unplaced = section(page, "Active under no priority");
    const parked = section(page, "Parked priority work");
    const tops = await Promise.all([topOf(priorities), topOf(unplaced), topOf(parked)]);
    expect(tops[0]).toBeLessThan(tops[1]);
    expect(tops[1]).toBeLessThan(tops[2]);

    // Each fault's heading names it and counts its issues, and the page says no more of it.
    await expect(unplaced.getByRole("heading", { level: 2 })).toHaveText(
      "Active under no priority 4"
    );
    await expect(parked.getByRole("heading", { level: 2 })).toHaveText("Parked priority work 1");

    // Each fault list is the application's one Issues table over its own issue listing, which
    // is a read of its own beside the priorities read: its rows are waited for, not assumed
    // there once the heading above them counts them.
    const unplacedTable = page.getByRole("table", { exact: true, name: NO_PRIORITY_TABLE });
    const parkedTable = page.getByRole("table", { exact: true, name: PARKED_TABLE });
    await expect(unplaced.getByRole("table")).toHaveAccessibleName(NO_PRIORITY_TABLE);
    await expect(parked.getByRole("table")).toHaveAccessibleName(PARKED_TABLE);

    // The first fault's rows: one issue in each of the four in-flight statuses, and nothing
    // that advances a priority.
    const clockRow = unplacedTable.getByRole("row", {
      exact: true,
      name: `${seed.clock} Audit the clock`,
    });
    await expect(clockRow).toBeVisible();
    await expect
      .poll(async () => (await columnTexts(unplacedTable, "Status")).sort())
      .toEqual(["In progress", "Needs review", "Retro", "Testing"]);
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
    // When it last changed, which the fault lists show by default and a project's table does
    // not (`PrioritiesPage.tsx`'s two layouts).
    await expect((await clockCell("Updated")).locator("time")).toHaveCount(1);
    // Nor does it have the parked list's Reason column: this list asks for no move.
    expect(
      (await unplacedTable.getByRole("columnheader").allTextContents()).some((text) =>
        text.trim().startsWith("Reason")
      )
    ).toBe(false);

    // The second fault's one row is the child, which advances P3 through the issue above it,
    // and it names the priority it advances and when it was moved out of the way.
    await expectRowOrder(parkedTable, [`${seed.child} Ledger client migration`]);
    const childRow = parkedTable.getByRole("row", {
      exact: true,
      name: `${seed.child} Ledger client migration`,
    });
    const childCell = (header: string) => cellUnder(parkedTable, childRow, header);
    await expect((await childCell("Key")).getByRole("link")).toHaveText(seed.child);
    await expect(await childCell("Project")).toHaveText("WEB");
    await expect(await childCell("Status")).toHaveText("Backlog");
    // Nobody claimed this one, though it is assigned to alice as the row above is: an assignee
    // is who the work is meant for, and this column says who holds it, so it is empty here, as
    // every Issues table draws an issue nobody holds.
    await expect(await childCell("Claimed by")).toHaveText("");
    await expect((await childCell("Updated")).locator("time")).toHaveCount(1);
    const advances = (await childCell("Advances")).getByRole("link");
    await expect(advances).toHaveText(seed.titles[2]);
    await expect(advances).toHaveAttribute("href", `/priorities/${seed.ids[2]}`);
    // Moved there: the moment the seed moved it into the backlog, and who moved it.
    const moved = await childCell("Moved there");
    await expect(moved.locator("time")).toHaveCount(1);
    await expect(moved).toContainText("alice");
    // Reason: why it was moved there, which nothing records until dispatch://INFRA-2934 does,
    // and the cell says so with a link to that work.
    const reason = await childCell("Reason");
    await expect(reason).toHaveText("Not recorded until INFRA-2934");
    await expect(reason.getByRole("link", { name: "INFRA-2934" })).toHaveAttribute(
      "href",
      "/issues/INFRA-2934"
    );
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
    await expectRowOrder(section(page, "Priorities"), [`P1 ${added}`]);
    await expect(page.getByText(NONE_LIVE, { exact: true })).toHaveCount(0);

    // The store's, not the page's: read back from the server, and from a page loaded again.
    await expect
      .poll(async () => (await listPriorities()).priorities?.map((row) => row.title))
      .toEqual([added]);
    await page.reload();
    await expectRowOrder(section(page, "Priorities"), [`P1 ${added}`]);
    await expect(page.getByText(NONE_LIVE, { exact: true })).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, four writes a person makes, and two reloads that read them back.
test("a priority row opens its work by status, and four writes survive a reload", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(PRIORITIES_PATH);

    // An opened row shows the work advancing that priority, read by status, which is the
    // grouping the page writes into the address when it opens the row.
    await page.getByRole("button", { name: `Open P3 ${seed.titles[2]}` }).click();
    const work = section(page, `Work advancing ${seed.titles[2]}`);
    await expect(page).toHaveURL(/w\.group=status/);
    await expect(work.getByRole("button", { name: /Needs review, 1 issue/ })).toBeVisible();
    await expect(work.getByRole("button", { name: /Todo, 1 issue/ })).toBeVisible();
    await expect(work.getByRole("button", { name: /Backlog, 1 issue/ })).toBeVisible();
    await expectRowNames(work, [
      `${seed.child} Ledger client migration`,
      `${seed.cutover} Ledger cut-over`,
      `${seed.ledger} Ledger read path`,
    ]);
    await page.getByRole("button", { name: `Close P3 ${seed.titles[2]}` }).click();
    await expect(section(page, `Work advancing ${seed.titles[2]}`)).toHaveCount(0);

    // Renamed where it is read: no edit mode and no Save, so leaving the field writes it.
    const renamed = "Cut the review queue in half";
    await page.getByLabel("Title of P2").fill(renamed);
    await page.getByLabel("Title of P2").blur();

    // Described in the row that holds it, which is where the whole description is shown: read
    // rendered, every paragraph of it, and opened into its editor by a click on what it reads.
    const described = "The fleet is one build, or the rollout is not done.";
    const rendered = page.getByRole("group", { exact: true, name: "Description of P1" });
    const editor = page.getByRole("textbox", { exact: true, name: "Description of P1" });
    const finished = "It is finished when no machine is left on the old build";
    await page.getByRole("button", { name: `Open P1 ${seed.titles[0]}` }).click();
    await expect(rendered).toContainText(finished);
    await rendered.getByText(finished).click();
    await expect(editor).toHaveValue(seed.description);
    await editor.fill(described);
    await editor.blur();
    await expect(rendered).toContainText(described);

    // Added, which takes the last place of the order.
    const added = "Keep the fleet on one build";
    await page.getByRole("button", { name: "New priority" }).click();
    await page.getByLabel("Title of the new priority").fill(added);
    await page
      .locator('form[aria-label="New priority"]')
      .getByRole("button", {
        name: "Add priority",
      })
      .click();
    await expect(page.getByRole("row", { name: `P4 ${added}` })).toBeVisible();

    // And dragged: the third row onto the second, which writes the place the reader can see.
    const handle = page.getByRole("button", {
      name: `Move P3 ${seed.titles[2]}: drag it, or press the up and down arrows`,
    });
    await handle.dragTo(page.locator(`tr[data-priority-row="${seed.ids[1]}"]`));
    await expect(page.getByRole("row", { name: `P2 ${seed.titles[2]}` })).toBeVisible();

    // Every one of the four is the store's now, not the page's: read back from the server, and
    // then from a page that was loaded again.
    await expect
      .poll(async () => (await listPriorities()).priorities?.map((row) => row.title))
      .toEqual([seed.titles[0], seed.titles[2], renamed, added]);
    await page.reload();
    await expectRowOrder(section(page, "Priorities"), [
      `P1 ${seed.titles[0]}`,
      `P2 ${seed.titles[2]}`,
      `P3 ${renamed}`,
      `P4 ${added}`,
    ]);
    await page.getByRole("button", { name: `Open P1 ${seed.titles[0]}` }).click();
    await expect(rendered).toContainText(described);
    await expect(rendered).not.toContainText("It is finished when no machine is left");

    // What the page does not say about a priority (INFRA-2947, R10): no date it is wanted by,
    // no portion of it done, and no word judging how it is going. Every `<time>` the page draws
    // is a moment that has passed, and no relative label reads forwards.
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
    expect(drawn).not.toMatch(
      /\b(on track|at risk|off track|behind schedule|ahead of schedule)\b/i
    );
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, a page held open across two writes, and a third read after a reload.
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

    // Four active under no priority plus one parked. An issue that leaves `in_progress` is
    // no longer active, so the first fault loses it - with no reload: the event that moved
    // it refreshes the two counts (`web/src/api/event-refresh.ts`).
    await patchIssue(seed.clock, { status: "done" });
    await expect(entry.locator("span").last()).toHaveText("4");

    // And the child advances P3 only through the issue above it, so moving that link out
    // leaves it parked under nothing, which is no fault at all.
    await patchIssue(seed.child, { parent: null });
    await expect(entry.locator("span").last()).toHaveText("3");

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
  // P3's one issue in flight leaves flight first, so the delete below moves no issue into the
  // first fault list, and what the rail counts can only fall by the work parked under P3.
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
      name: `${seed.child} Ledger client migration`,
    });
    const badge = railEntry(reader).locator("span").last();
    await expectRowOrder(priorities, [
      `P1 ${seed.titles[0]}`,
      `P2 ${seed.titles[1]}`,
      `P3 ${seed.titles[2]}`,
    ]);
    await expect(childRow).toBeVisible();
    // Four active under no priority plus the one parked.
    await expect(badge).toHaveText("5");

    // Reader B: bob renames P3 on his own page. Alice's row takes the new title, and so does
    // the parked work's Advances cell, which names a priority by the title a person wrote.
    const renamed = "Retire the old ledger for good";
    const writer = await bob.newPage();
    await writer.goto(PRIORITIES_PATH);
    await writer.getByLabel("Title of P3").fill(renamed);
    await writer.getByLabel("Title of P3").blur();
    await expectRowOrder(priorities, [
      `P1 ${seed.titles[0]}`,
      `P2 ${seed.titles[1]}`,
      `P3 ${renamed}`,
    ]);
    await expect((await cellUnder(parkedTable, childRow, "Advances")).getByRole("link")).toHaveText(
      renamed
    );

    // Then bob retires P3, over the API, since neither page offers a delete. The child advanced
    // only P3, through its parent, so it is no fault at all now: it leaves the parked list, and
    // the rail's count falls by it.
    await endPriority(seed.ids[2], { login: "bob" });
    await expectRowOrder(priorities, [`P1 ${seed.titles[0]}`, `P2 ${seed.titles[1]}`]);
    await expectRowNames(parkedTable, []);
    await expect(
      section(reader, "Parked priority work").getByRole("heading", { level: 2 })
    ).toHaveText("Parked priority work 0");
    await expect(badge).toHaveText("4");

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
    await expect(page.getByRole("textbox", { name: "Title" })).toHaveValue(seed.titles[0]);
    await expect(page.getByRole("combobox", { name: "Owner" })).toHaveValue("alice");
    // One page and no tab strip: a tab and a margin state are the same object, and the margin
    // holds the states (dispatch://INFRA-1196).
    await expect(page.getByRole("tablist")).toHaveCount(0);
    await expect(page.getByRole("tab")).toHaveCount(0);

    const retitled = "Ship the daemon to every machine";
    await page.getByRole("textbox", { name: "Title" }).fill(retitled);
    await page.getByRole("textbox", { name: "Title" }).blur();

    // The description reads rendered, and no editor is drawn until the reader asks for one.
    const rendered = page.getByRole("group", { exact: true, name: "Description of P1" });
    const editor = page.getByRole("textbox", { exact: true, name: "Description of P1" });
    await expect(rendered.getByRole("heading", { name: "Why" })).toBeVisible();
    await expect(rendered.getByRole("listitem")).toHaveText([
      "Every machine runs the daemon",
      "Nothing waits on the old build",
    ]);
    await expect(editor).toHaveCount(0);

    // A click on it opens its source in its place, with the caret at the end, so the line typed
    // there follows the text; leaving the field saves it and gives the rendered block back.
    const line = "The roll-back is written down";
    const described = `${outline}\n- ${line}`;
    await rendered.getByRole("listitem").last().click();
    await expect(editor).toBeFocused();
    await expect(editor).toHaveValue(outline);
    await editor.pressSequentially(`\n- ${line}`);
    await editor.blur();
    await expect(editor).toHaveCount(0);
    await expect(rendered.getByRole("listitem")).toHaveText([
      "Every machine runs the daemon",
      "Nothing waits on the old build",
      line,
    ]);

    // The store holds both, and the page that is loaded again reads them from it.
    await expect
      .poll(async () => {
        const stored = (await listPriorities()).priorities?.find((row) => row.id === seed.ids[0]);
        return [stored?.title, stored?.description];
      })
      .toEqual([retitled, described]);
    await page.reload();
    await expect(page.getByRole("textbox", { name: "Title" })).toHaveValue(retitled);
    await expect(rendered.getByRole("listitem").last()).toHaveText(line);
    // The keyboard's way in is the one button in the block, named by the words it shows, which
    // is drawn only once it holds the focus; Enter on it opens the editor as a click does.
    const edit = rendered.getByRole("button", { exact: true, name: "Edit the description of P1" });
    await expect(edit).toHaveText("Edit the description of P1");
    expect((await edit.boundingBox())?.width ?? 0).toBeLessThanOrEqual(1);
    await edit.focus();
    expect((await edit.boundingBox())?.width ?? 0).toBeGreaterThan(1);
    await page.keyboard.press("Enter");
    await expect(editor).toHaveValue(described);
    await editor.blur();
    await expect(editor).toHaveCount(0);

    // The description is a disclosure, and it shuts and opens.
    const description = section(page, "Description");
    await description.getByRole("button", { exact: true, name: "Description" }).click();
    await expect(rendered).toHaveCount(0);
    await description.getByRole("button", { exact: true, name: "Description, closed" }).click();
    await expect(rendered.getByRole("listitem").last()).toHaveText(line);

    // The work is this priority's and nothing else's: the two issues that advance it, and
    // neither of the issues under the other two.
    const work = section(page, "Work advancing this priority");
    await expectRowNames(work, [
      `${seed.rollout} Daemon rollout to the fleet`,
      `${seed.smoke} Daemon smoke suite`,
    ]);
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
    const line = page.getByTestId("issue-advances");
    await expect(line).toContainText("Advances");
    const link = line.getByRole("link", { name: `P1 ${seed.titles[0]}` });
    await expect(link).toBeVisible();
    await link.click();
    await expect(page).toHaveURL(`/priorities/${seed.ids[0]}`);
    await expect(page.getByRole("textbox", { name: "Title" })).toHaveValue(seed.titles[0]);

    // An issue that advances none carries no line at all: no label, no name, only the control
    // that places it under a priority.
    await page.goto(`/issues/${seed.clock}`);
    await expect(page.getByTestId("issue-advances").getByRole("link")).toHaveCount(0);
    await expect(page.getByTestId("issue-advances")).not.toContainText("Advances");
    await expect(
      page.getByRole("button", { name: `Add a priority to ${seed.clock}` })
    ).toBeVisible();

    // A priority an issue inherits says so and carries no remove: that relation is the
    // ancestor's, and a relation on a child places nothing above it.
    await page.goto(`/issues/${seed.child}`);
    const inherited = page.getByTestId("issue-advances");
    await expect(inherited.getByRole("link", { name: `P3 ${seed.titles[2]}` })).toBeVisible();
    await expect(inherited).toContainText("inherited");
    await expect(
      page.getByRole("button", { name: `Remove P3 ${seed.titles[2]} from ${seed.child}` })
    ).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, two writes, and the margin read in each of its four states.
test("the margin lists a priority's history newest first and keeps the state a reader chose", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  // Two acts after the making, in a known order, so "newest first" is a claim with two ends.
  await patchPriority(seed.ids[0], { owner: "bob" });
  const retitled = "Ship the daemon everywhere, and prove it";
  await patchPriority(seed.ids[0], { title: retitled });

  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/priorities/${seed.ids[0]}`);
    const margin = page.getByRole("complementary", { name: "Margin" });
    const state = margin.getByRole("combobox", { name: /Margin/ });
    await expect(state).toHaveValue("everything");

    // The acts the server holds are in the order they were recorded, newest first, and that is
    // the order the list draws them in: the rename above the owner write, and both above the
    // making. The order is the record's own, not the wall clock's: each write stamps its own
    // moment and the rows are kept in the order they reached the table, so two writes a
    // millisecond apart can carry their moments the other way round, which a comparison of
    // timestamps would read as a failure of an order that is right.
    const acts = await getPriorityHistory(seed.ids[0]);
    expect(acts[0]?.field).toBe("title");
    expect(acts[0]?.value).toBe(retitled);
    expect(acts[1]?.field).toBe("owner");
    expect(acts[1]?.value).toBe("bob");
    const entries = await margin.getByRole("listitem").allInnerTexts();
    expect(entries.length).toBe(acts.length);
    const renamedAt = entries.findIndex((entry) => entry.includes(retitled));
    const ownedAt = entries.findIndex((entry) => entry.includes("bob"));
    expect(renamedAt).toBe(0);
    expect(ownedAt).toBeGreaterThan(renamedAt);
    expect(entries[0]).toContain("alice");

    // The other three states are what a priority carries none of, each said in one sentence
    // rather than drawn as an empty list.
    for (const [label, sentence] of MARGIN_SENTENCES) {
      await state.selectOption({ label });
      await expect(margin.getByText(sentence)).toBeVisible();
      await expect(margin.getByRole("listitem")).toHaveCount(0);
    }

    // The state a reader chose is the one they come back to.
    await state.selectOption({ label: "Pinned" });
    await page.goto(PRIORITIES_PATH);
    await expect(page.getByRole("heading", { level: 1, name: "Priorities" })).toBeVisible();
    await page.goto(`/priorities/${seed.ids[0]}`);
    await expect(
      page.getByRole("complementary", { name: "Margin" }).getByRole("combobox")
    ).toHaveValue("pinned");
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

    await page.getByRole("button", { name: `Add a priority to ${seed.clock}` }).click();
    await page.getByRole("button", { name: "Priority to advance" }).click();
    await page
      .getByRole("listbox", { name: "Priority to advance options" })
      .getByRole("option", { exact: true, name: `P2 ${seed.titles[1]}` })
      .click();
    await page.getByRole("button", { name: "Add", exact: true }).click();
    const named = page
      .getByTestId("issue-advances")
      .getByRole("link", { name: `P2 ${seed.titles[1]}` });
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

    // Taken off from the same line, and gone from both ends after a reload.
    await page.goto(`/issues/${seed.clock}`);
    await page
      .getByRole("button", { name: `Remove P2 ${seed.titles[1]} from ${seed.clock}` })
      .click();
    await expect(page.getByTestId("issue-advances").getByRole("link")).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("issue-advances").getByRole("link")).toHaveCount(0);
    await page.goto(`/priorities/${seed.ids[1]}`);
    await expectRowNames(section(page, "Work advancing this priority"), [
      `${seed.queue} Review queue triage`,
    ]);

    // And the priority's margin holds both acts, newest first, each in the words
    // `PriorityMargin.tsx` writes it in and each naming the person who made it: a relation is
    // an act of the priority as much as of the issue, and the retraction is not the assertion.
    const entries = await page
      .getByRole("complementary", { name: "Margin" })
      .getByRole("listitem")
      .allInnerTexts();
    expect(entries[0]).toContain(`retracted the relation to the issue ${seed.clock}`);
    expect(entries[0]).toContain("alice");
    expect(entries[1]).toContain(`related the issue ${seed.clock}`);
    expect(entries[1]).toContain("alice");
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
  // The daemon's smoke suite advances P1 already; it states P3 beside it - one relation more,
  // which a write with no revision may assert - so the delete of P3 below leaves the issue one
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
    const kept = line.getByRole("link", { name: `P1 ${seed.titles[0]}` });
    const deleted = line.locator(`a[href="/priorities/${seed.ids[2]}"]`);
    await expect(kept).toBeVisible();
    await expect(deleted).toHaveText(`P3 ${seed.titles[2]}`);

    // Another person retires P3 over the API. The open header stops naming it, by its title or
    // by its id, with no reload: the issue's read is fetched again, so the set the next write
    // sends is the one the server holds.
    await endPriority(seed.ids[2], { login: "bob" });
    await expect(deleted).toHaveCount(0);
    await expect(kept).toBeVisible();

    // The reader places the issue under P2 from the header's own control. The write sends P1 and
    // P2, which the server takes; a set still naming P3 would be refused, a priority that is gone.
    await reader.getByRole("button", { name: `Add a priority to ${seed.smoke}` }).click();
    await reader.getByRole("button", { name: "Priority to advance" }).click();
    await reader
      .getByRole("listbox", { name: "Priority to advance options" })
      .getByRole("option", { exact: true, name: `P2 ${seed.titles[1]}` })
      .click();
    await reader.getByRole("button", { name: "Add", exact: true }).click();
    const added = line.getByRole("link", { name: `P2 ${seed.titles[1]}` });
    await expect(added).toBeVisible();
    expect(await reader.evaluate(() => document.documentElement.dataset.heldOpen)).toBe("yes");

    // What the server holds, read back after a reload: P1 and P2, and nothing of P3.
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

// SLOW(>=2s): the seed and two pages read for what they do not draw.
test("a priority's page offers no versions and no delete, and the parked list says no reason is recorded", async ({
  browser,
}) => {
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await priorityPage(page);
    await page.goto(`/priorities/${seed.ids[0]}`);
    const main = page.getByTestId("main-content");
    await expect(page.getByRole("textbox", { name: "Title" })).toHaveValue(seed.titles[0]);

    // The description is an attribute of the node and not a Dispatch document, so nothing here
    // offers its versions, and there is no delete: a priority is retired by its state.
    await expect(main.getByRole("button", { name: /version/i })).toHaveCount(0);
    await expect(main.getByRole("link", { name: /version/i })).toHaveCount(0);
    await expect(main.getByText(/\bv\d+\b/)).toHaveCount(0);
    await expect(main.getByRole("button", { name: /delete/i })).toHaveCount(0);
    await expect(main.getByRole("link", { name: /delete/i })).toHaveCount(0);

    // And the second fault's Reason cell says the one thing it cannot say about a parked issue:
    // why it was moved. Nothing records a reason until dispatch://INFRA-2934 does.
    await page.goto(PRIORITIES_PATH);
    const parkedTable = page.getByRole("table", { exact: true, name: PARKED_TABLE });
    const childRow = parkedTable.getByRole("row", {
      exact: true,
      name: `${seed.child} Ledger client migration`,
    });
    await expect(childRow).toBeVisible();
    await expect(await cellUnder(parkedTable, childRow, "Reason")).toHaveText(
      "Not recorded until INFRA-2934"
    );
  } finally {
    await context.close();
  }
});

// The priority pages with less text (dispatch://INFRA-3221, Verification k17, at v64): each read
// at 1280 and 390 px set beside what the same read gives at the base, the stage A base B
// (1dc37c1ef909), on this file's seed. The base's reads are written down here as they were read
// there (Job infra-3221-t16b-p17b-snsk6, 2026-11-17); every read the check lets differ is written
// with the head's value and its reason.

/** One column's header as the base drew it: its words and its width, in px. */
type BaseColumn = readonly [string, number];

/** The issue tables' columns at the base in their desktop layout (a box 640 px wide or more),
 *  in their order, less Epic, which the head draws no more. Project, Advances, Moved there,
 *  Reason and Updated show only in the tables that open with them. */
const BASE_DESKTOP: readonly BaseColumn[] = [
  ["", 44],
  ["Priority", 140],
  ["Key", 108],
  ["Title", 255.19],
  ["Queue", 124],
  ["Project", 120],
  ["Advances", 220],
  ["Moved there", 220],
  ["Reason", 240],
  ["Status", 132],
  ["Assignee", 180],
  ["Created by", 200],
  ["Labels", 200],
  ["Claimed by", 220],
  ["Open asks", 148],
  ["Created", 148],
  ["Updated", 148],
  ["", 104],
];

/** The same in the phone layout (a box under 640 px), where Select, Key and Title are pinned and
 *  Title takes what is left: 158.69 px of a 342 px box, 350.8 px of a priority page's 568 px. */
const BASE_PHONE: readonly BaseColumn[] = [
  ["", 44],
  ["Key", 88],
  ["Title", 0],
  ["Priority", 140],
  ...BASE_DESKTOP.slice(4),
];
const BASE_PHONE_TITLE: Readonly<Record<number, number>> = { 342: 158.69, 568: 350.8 };

/** The priorities table's columns at the base, each under the words the head gives it. */
const BASE_PRIORITIES: Readonly<Record<number, readonly BaseColumn[]>> = {
  1280: [
    ["Order", 88],
    ["Priority", 420],
    ["Owner", 200],
    ["Active", 132],
    ["Triage, Todo", 132],
    ["Backlog, icebox", 180],
    ["Closed, 7 days", 132],
    ["Newest event", 160],
  ],
  390: [
    ["Order", 88],
    ["Priority", 202.69],
    ["Owner", 200],
    ["Active", 132],
    ["Triage, Todo", 132],
    ["Backlog, icebox", 180],
    ["Closed, 7 days", 132],
    ["Newest event", 160],
  ],
};

/** The head's widths of the priorities table's columns whose base minimum cut their header: the
 *  width the header, its filter button and, the table being sorted by Order, Order's sort mark
 *  need. At 390 px the Priority column takes what is left, which the wider Order narrows. */
const HEAD_PRIORITIES: Readonly<Record<string, number>> = {
  Order: 118,
  "Triage, Todo": 152,
  "Closed, 7 days": 168,
  "Newest event": 164,
  "Backlog, icebox": 180,
};
const HEAD_PRIORITY_AT_390 = 180;

/** The phone layout's Key at the head: wide enough for its header and its filter button. */
const HEAD_PHONE_KEY = 100;

/** The issue tables' Group by choices at the base, less Epic, and the priorities table's. */
const BASE_GROUP_BY = [
  "Nothing",
  "Priority",
  "Status",
  "Assignee",
  "Created by",
  "Claimed by",
  "Created",
  "Updated",
];
const BASE_PRIORITIES_GROUP_BY = ["Nothing", "Owner", "Newest event"];

/** One table of the page as k17 reads it. */
interface TableRead {
  readonly label: string;
  readonly box: number;
  readonly headers: readonly { label: string; width: number; left: number; pinned: string }[];
  readonly rows: readonly string[];
  readonly groups: readonly string[];
  readonly strip: readonly { text: string; fill: string; scale: string }[];
  readonly stripText: string;
  readonly tabs: number;
  readonly pager: string;
  readonly menu: string;
}

/** Every table of the page: its headers with their widths and pins, its rows and group headings,
 *  its strip with each status's fill beside the fill `issueStatusScale` gives that status, any
 *  tab in its section, its pager and its display menu's words. */
function readTables(page: Page): Promise<TableRead[]> {
  return page.evaluate((scale: Record<string, string>) => {
    const text = (node: Element | null | undefined) =>
      (node?.textContent ?? "").replace(/\s+/g, " ").trim();
    const words = Object.fromEntries(
      [
        ["Triage", "triage"],
        ["Icebox", "icebox"],
        ["Backlog", "backlog"],
        ["Todo", "todo"],
        ["In progress", "in_progress"],
        ["Testing", "testing"],
        ["Needs review", "needs_review"],
        ["Retro", "retro"],
        ["Done", "done"],
      ].map(([name, status]) => [name, status])
    ) as Record<string, string>;
    return [...document.querySelectorAll('[data-control="data-table"] table')].map((table) => {
      const box = table.closest('[data-control="data-table"]') as HTMLElement;
      const left = box.getBoundingClientRect().left;
      const scope = box.closest("section[aria-label]") ?? box.parentElement;
      const strip = scope?.querySelector('[aria-label="Issues by status"]');
      return {
        label: table.getAttribute("aria-label") ?? "",
        box: Math.round(box.getBoundingClientRect().width),
        headers: [...table.querySelectorAll("thead th")].map((cell) => ({
          // A column with no sort (Project) has no button: its words are the cell's own.
          label: (text(cell.querySelector("button")) || text(cell).replace(/Filter$/, ""))
            .replace(/[▲▼]/g, "")
            .trim(),
          width: cell.getBoundingClientRect().width,
          left: cell.getBoundingClientRect().left - left,
          pinned: getComputedStyle(cell).left,
        })),
        rows: [...table.querySelectorAll("tr[data-issue-row], tr[data-priority-row]")].map(
          (row) => row.getAttribute("aria-label") ?? ""
        ),
        groups: [...table.querySelectorAll("tbody tr td[colspan] button[aria-expanded]")].map(
          (button) => button.getAttribute("aria-label") ?? ""
        ),
        strip: [...(strip?.querySelectorAll("button") ?? [])].map((button) => {
          const name = text(button).replace(/\s*\d+$/, "");
          const probe = document.createElement("span");
          probe.className = scale[words[name] ?? ""] ?? "";
          document.body.appendChild(probe);
          const fill = getComputedStyle(probe).backgroundColor;
          probe.remove();
          return {
            text: text(button),
            fill: getComputedStyle(button).backgroundColor,
            scale: fill,
          };
        }),
        stripText: text(strip),
        tabs: scope?.querySelectorAll('[role="tablist"], [role="tab"]').length ?? 0,
        pager: text(scope?.querySelector('[data-control="pager"]')),
        menu: text(scope?.querySelector('button[data-control="display-menu"]')),
      };
    });
  }, issueStatusScale);
}

/** Where an issue table's headers differ from the base's, by its layout: every width the base's
 *  but the phone layout's Key, at the head as wide as its header and filter button need, and its
 *  Title, which takes what is left and so ends where the base's Title ends. */
function issueTableDiffers(read: TableRead, where: string): string[] {
  const phone = read.box < 640;
  const base = phone ? BASE_PHONE : BASE_DESKTOP;
  const drawn = read.headers.map((header) => header.label);
  const wrong: string[] = [];
  // The base's order, each header where the base drew it, and no Epic.
  const places = drawn.map((label, index) =>
    label === "" ? (index === 0 ? 0 : base.length - 1) : base.findIndex(([own]) => own === label)
  );
  if (
    places.some((place, index) => place === -1 || (index > 0 && place <= (places[index - 1] ?? -1)))
  )
    wrong.push(`${where} ${read.label}: the headers are ${drawn.join(", ")}`);
  for (const [index, header] of read.headers.entries()) {
    const own = base[places[index] ?? -1];
    let width = own?.[1] ?? Number.NaN;
    if (phone && header.label === "Key") width = HEAD_PHONE_KEY;
    if (phone && header.label === "Title")
      width = (BASE_PHONE_TITLE[read.box] ?? Number.NaN) + 88 - HEAD_PHONE_KEY;
    if (Math.abs(header.width - width) > 0.5)
      wrong.push(
        `${where} ${read.label}: ${header.label || "(no words)"} is ${header.width} px, ${width} px is wanted`
      );
  }
  // The pins: Select, Priority, Key and Title on a desktop, Select, Key and Title on a phone.
  const pinned = read.headers
    .filter((header) => header.pinned !== "auto")
    .map((header) => header.label);
  const pins = phone ? ["", "Key", "Title"] : ["", "Priority", "Key", "Title"];
  if (JSON.stringify(pinned) !== JSON.stringify(pins))
    wrong.push(`${where} ${read.label}: ${pinned.join(", ")} are pinned`);
  return wrong;
}

/** The checks every embedded issue table answers at the head: today's strip and legend in
 *  `issueStatusScale`'s colours with none of the three numbers, a Queue column, no tab, a pager
 *  with no total, and a display menu named Group by. */
function embeddedDiffers(read: TableRead, where: string): string[] {
  const wrong: string[] = [];
  const name = `${where} ${read.label}`;
  if (read.strip.length === 0) wrong.push(`${name}: no strip`);
  for (const part of read.strip)
    if (part.fill !== part.scale)
      wrong.push(`${name}: ${part.text} is ${part.fill}, ${part.scale} is its scale's`);
  if (/queue|running|epics/i.test(read.stripText))
    wrong.push(`${name}: the strip reads ${read.stripText}`);
  if (!read.headers.some((header) => header.label === "Queue"))
    wrong.push(`${name}: no Queue column`);
  if (read.tabs !== 0) wrong.push(`${name}: a tab is drawn`);
  if (
    !/^Rows \d+–\d+‹ PreviousPage \d+ of \d+Next ›/.test(read.pager) ||
    /Rows \d+–\d+ of/.test(read.pager)
  )
    wrong.push(`${name}: the pager reads ${read.pager}`);
  if (read.menu !== "Group by") wrong.push(`${name}: the display menu reads ${read.menu}`);
  return wrong;
}

/** Opens the display menu or the Columns menu `trigger` names in `scope` and reads its choices,
 *  each with whether it is taken. */
async function menuChoices(page: Page, scope: Locator, control: string): Promise<string[]> {
  const trigger = scope.locator(`button[data-control="${control}"]`).first();
  await trigger.click();
  const menu = page.getByRole("dialog").last();
  await expect(menu).toBeVisible();
  const choices = await menu
    .locator("label")
    .evaluateAll((labels) =>
      labels.map(
        (label) =>
          `${(label.textContent ?? "").replace(/\s+/g, " ").trim()}${label.querySelector("input")?.checked ? " (on)" : ""}`
      )
    );
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  return choices;
}

// SLOW(>=2s): the seed, then /priorities read at two widths with a priority row opened and each
// table's menus opened.
test("on /priorities at 1280 and 390 px each heading reads its name and one count, the priorities table reads Active and Triage, Todo with the base's counts, and every other read equals the base's but the widths the check names", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    for (const [width, height] of [
      [1280, 900],
      [390, 844],
    ] as const) {
      const where = `at ${width} px`;
      await page.setViewportSize({ height, width });
      await page.goto(PRIORITIES_PATH);
      const priorities = section(page, "Priorities");
      await expect(priorities.locator("tr[data-priority-row]").first()).toBeVisible();
      await expect(page.locator("tr[data-issue-row]").first()).toBeVisible();

      // No subtitle; each section heading its name and one count, with nothing under it.
      const main = page.getByTestId("main-content");
      const heading = main.getByRole("heading", { level: 1, name: "Priorities" });
      expect(
        await heading.evaluate(
          (node) => node.parentElement?.querySelectorAll(":scope > p").length ?? 0
        ),
        `${where}: a subtitle`
      ).toBe(0);
      const unplaced = section(page, "Active under no priority");
      const parked = section(page, "Parked priority work");
      await expect(unplaced.getByRole("heading", { level: 2 })).toHaveText(
        "Active under no priority 4"
      );
      await expect(parked.getByRole("heading", { level: 2 })).toHaveText("Parked priority work 1");
      for (const part of [unplaced, parked])
        expect(await part.locator(":scope > p").count(), `${where}: a line under a heading`).toBe(
          0
        );
      await expect(
        page.locator('section[aria-label="Priority work in backlog or icebox"]')
      ).toHaveCount(0);
      await expect(page.locator('section[aria-label="In flight under no priority"]')).toHaveCount(
        0
      );

      // The priorities table: its headers, at most 16 characters, its widths, the counts under
      // Active and Triage, Todo, and its grouping control.
      const tables = await readTables(page);
      const table = tables.find((one) => one.label === "Priorities table");
      if (table === undefined) throw new Error(`${where}: no priorities table`);
      const base = BASE_PRIORITIES[width] ?? [];
      expect(table.headers.map((header) => header.label)).toEqual(base.map(([label]) => label));
      for (const header of table.headers) {
        expect(header.label.length, `${where}: ${header.label} is long`).toBeLessThanOrEqual(16);
        const wanted =
          header.label === "Priority" && width === 390
            ? HEAD_PRIORITY_AT_390
            : (HEAD_PRIORITIES[header.label] ??
              base.find(([label]) => label === header.label)?.[1]);
        expect(
          Math.abs(header.width - (wanted ?? Number.NaN)),
          `${where}: ${header.label} is ${header.width} px`
        ).toBeLessThanOrEqual(0.5);
      }
      const prioritiesTable = page.getByRole("table", { name: "Priorities table" });
      // Active counts In progress, Testing, Needs review and Retro, Triage, Todo those two: the
      // base's In flight and Waiting under each priority, as the seed writes its work.
      expect(await columnTexts(prioritiesTable, "Active")).toEqual(["1", "1", "1"]);
      expect(await columnTexts(prioritiesTable, "Triage, Todo")).toEqual(["1", "0", "1"]);
      expect(await columnTexts(prioritiesTable, "Backlog, icebox")).toEqual(["0", "0", "1"]);
      expect(table.menu, `${where}: the priorities table's grouping control`).toBe("Group by");
      expect(await menuChoices(page, priorities, "display-menu")).toEqual([
        `${BASE_PRIORITIES_GROUP_BY[0]} (on)`,
        ...BASE_PRIORITIES_GROUP_BY.slice(1),
      ]);
      expect(table.pager).toMatch(/^Rows 1–3‹ Previous/);

      // The two fault lists: every read the base's but the widths the check names.
      for (const name of [NO_PRIORITY_TABLE, PARKED_TABLE]) {
        const read = tables.find((one) => one.label === name);
        if (read === undefined) throw new Error(`${where}: no ${name}`);
        expect([...issueTableDiffers(read, where), ...embeddedDiffers(read, where)]).toEqual([]);
      }
      expect(tables.find((one) => one.label === NO_PRIORITY_TABLE)?.rows).toEqual([
        `${seed.clock} Audit the clock`,
        expect.stringMatching(/^OPS-2 /),
        expect.stringMatching(/^WEB-4 /),
        expect.stringMatching(/^CORE-4 /),
      ]);
      const unplacedScope = page.locator(`section[aria-label="Issues active under no priority"]`);
      expect(await menuChoices(page, unplacedScope, "display-menu")).toEqual([
        `${BASE_GROUP_BY[0]} (on)`,
        ...BASE_GROUP_BY.slice(1),
      ]);
      const columns = await menuChoices(page, unplacedScope, "columns-menu");
      expect(columns).not.toContain(expect.stringMatching(/^Epic/));
      expect(columns.at(-1)).toBe("Closed");

      // No heading or column header names the four statuses In progress or running, and no
      // label uses Active for anything else.
      const labels = await main.evaluate((node) =>
        [...node.querySelectorAll("h1, h2, th, legend, [data-control='chip'], button")].map((one) =>
          (one.textContent ?? "").replace(/\s+/g, " ").trim()
        )
      );
      for (const label of labels.filter((one) => /\bActive\b/.test(one)))
        expect(label, `${where}: a label uses Active`).toMatch(
          /^(?:Active|Active under no priority \d+|Active(?:Filter)?|Filter Active)$/
        );
      const headings = await main.locator("h1, h2, th").allTextContents();
      for (const one of headings)
        expect(one, `${where}: a heading`).not.toMatch(/running|In progress/);

      // A priority row opened: its work by status, the base's rows and group headings.
      await page.getByRole("button", { name: `Open P3 ${seed.titles[2]}` }).click();
      const work = section(page, `Work advancing ${seed.titles[2]}`);
      await expect(work.locator("tr[data-issue-row]").first()).toBeVisible();
      const opened = (await readTables(page)).find((one) => one.label === "Priority issues table");
      if (opened === undefined) throw new Error(`${where}: the opened row draws no work table`);
      expect([...issueTableDiffers(opened, where), ...embeddedDiffers(opened, where)]).toEqual([]);
      expect(opened.rows).toEqual([
        `${seed.child} Ledger client migration`,
        `${seed.cutover} Ledger cut-over`,
        `${seed.ledger} Ledger read path`,
      ]);
      expect(opened.groups.map((group) => group.replace(/^Collapse /, ""))).toEqual([
        "Backlog, 1 issue",
        "Todo, 1 issue",
        "Needs review, 1 issue",
      ]);
    }
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed, then a priority's page read at two widths, a header dragged and the page
// reloaded at each.
test("a priority's own page at 1280 and 390 px has no line of counts or of order, links Open in its project, draws its table in the phone layout as the base does but for Key, and draws a dragged Title where the base draws it after a reload", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    for (const [width, height] of [
      [1280, 900],
      [390, 844],
    ] as const) {
      const where = `at ${width} px`;
      await page.setViewportSize({ height, width });
      await page.evaluate(() => localStorage.clear()).catch(() => undefined);
      await page.goto(`/priorities/${seed.ids[2]}`);
      const table = page.getByRole("table", { name: "Priority issues table" });
      await expect(table.locator("tr[data-issue-row]").first()).toBeVisible();
      const header = page.getByTestId("main-content").locator("header").first();
      await expect(header).not.toContainText(/dragged|·|In flight|waiting/);
      await expect(page.getByRole("link", { name: "Open in WEB" })).toBeVisible();
      await expect(page.getByText(/Leave this scope/)).toHaveCount(0);
      const [read] = await readTables(page);
      if (read === undefined) throw new Error(`${where}: no work table`);
      // A priority's page draws its table in a box under 640 px at both widths: the phone layout.
      expect(read.box, `${where}: the table's box`).toBeLessThan(640);
      expect([...issueTableDiffers(read, where), ...embeddedDiffers(read, where)]).toEqual([]);
      expect(read.rows.filter((row) => row !== "")).toEqual([
        `${seed.child} Ledger client migration`,
        `${seed.cutover} Ledger cut-over`,
        `${seed.ledger} Ledger read path`,
      ]);
      // The Priority tag of an issue with no priority.
      await expect(table.locator(`tr[data-issue-row="${seed.child}"]`)).toContainText("Priority");

      // Title dragged 300 px wider and the page reloaded: the phone layout's Title takes what is
      // left, so it ends where it ended before the drag, as the base's does.
      const title = table
        .locator("thead th")
        .filter({ hasText: /^Title/ })
        .first();
      const before = await title.boundingBox();
      if (before === null) throw new Error("the Title header has no box");
      const ends = async () =>
        title.evaluate((node) => {
          const box = node.closest('[data-control="data-table"]')?.getBoundingClientRect();
          return node.getBoundingClientRect().right - (box?.left ?? 0);
        });
      const end = await ends();
      await page.mouse.move(before.x + before.width - 2, before.y + before.height / 2);
      await page.mouse.down();
      await page.mouse.move(before.x + before.width + 300, before.y + before.height / 2, {
        steps: 12,
      });
      await page.mouse.up();
      await page.reload();
      await expect(table.locator("tr[data-issue-row]").first()).toBeVisible();
      expect(Math.abs((await ends()) - end), `${where}: where Title ends`).toBeLessThanOrEqual(1);
      // The base's Title ended at its Key's start, 132 px, and its own width on: 291.69 px of a
      // 342 px box, 483.8 px of a 568 px one.
      expect(Math.abs(end - (132 + (BASE_PHONE_TITLE[read.box] ?? 0)) - 1)).toBeLessThanOrEqual(1);
    }
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the seed with two Todo issues labelled legion, then Start next pressed twice on a
// priority's page and the Open asks filter read on both pages.
test("on the priority pages the Open asks filter picks With open asks or No open asks, and Start next's texts name the Todo issues labelled legion", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  const seed = await seedPriorities();
  const context = await asUser(browser, "alice");
  try {
    const first = await createIssue({ project: "CORE", title: "Queue the fleet probe" });
    const second = await createIssue({ project: "CORE", title: "Queue the fleet report" });
    for (const issue of [first, second])
      await patchIssue(issue.key, { advances: [seed.ids[0]], labels: ["legion"], status: "todo" });
    const page = await context.newPage();
    await priorityPage(page);
    for (const path of [PRIORITIES_PATH, `/priorities/${seed.ids[0]}`]) {
      await page.goto(path);
      await expect(page.locator("tr[data-issue-row]").first()).toBeVisible();
      const trigger = page
        .getByRole("button", { name: /^Filter Open asks(?:: \d+ picked)?$/ })
        .first();
      await trigger.click();
      const options = page.getByRole("listbox", { name: "Open asks options" });
      await expect(options).toBeVisible();
      expect(
        (await options.getByRole("option").allTextContents()).map((one) =>
          one.replace(/\s*\d+$/, "").trim()
        ),
        `${path}: the Open asks picks`
      ).toEqual(["With open asks", "No open asks"]);
      await page.keyboard.press("Escape");
    }

    // Start next on the second of the two moves it ahead, in words that name the set.
    const work = page.getByRole("table", { name: "Priority issues table" });
    const row = work.getByRole("row", {
      exact: true,
      name: `${second.key} Queue the fleet report`,
    });
    await row.hover();
    await row.getByRole("button", { exact: true, name: "Start next" }).click();
    await expect(page.getByRole("status").filter({ hasText: "starts next" })).toHaveText(
      `${second.key} “Queue the fleet report” now starts next.`
    );
    // A selection with none of them is refused in words that name the set.
    await work.getByRole("checkbox", { name: `Select ${seed.rollout}` }).check();
    await page
      .locator('[data-control="selection-bar"]')
      .getByRole("button", { exact: true, name: "Start next" })
      .click();
    await expect(page.getByRole("alert").filter({ hasText: "Start next" })).toHaveText(
      "Start next moves only Todo issues labelled legion."
    );
  } finally {
    await context.close();
  }
});
