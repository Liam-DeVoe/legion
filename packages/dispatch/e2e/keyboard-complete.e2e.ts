/** The browser checks of dispatch://INFRA-2160, every page of Dispatch by the keyboard: one row
 *  or more for each check of its Verification document, each row's title starting with the id
 *  of the check it proves, so `-g '<id>'` runs that check's rows. Every step of a check is an
 *  assertion whose message names the check and the step, so a failure names the step it stopped
 *  at - which is also how the negative control reads: the same rows run on a build of the merge
 *  base, where they fail at the first step the merge base does not do.
 *
 *  The rows write - snoozes, priorities, statuses - and count each write at the server's own
 *  route, so they run on the real stack. They import nothing this tree adds to or changes in
 *  `web/src`: what they need from the app they read off the page or work out in
 *  `e2e/keyboard-complete.ts`. */
import { setLiveSessions } from "./agents";
import {
  createArtifactAsk,
  createAsk,
  createIssue,
  createProject,
  createProjectDocument,
  patchIssue,
} from "./api";
import { drawer } from "./inbox-drawer";
import { inboxPane } from "./inbox-pane";
import { replyRowId, seedCommentThread, THREAD_SESSION } from "./inbox-threads";
import {
  asker,
  contrastOf,
  holdWrites,
  issueWrites,
  returnMornings,
  snoozedUntil,
  snoozeWrites,
  unsnoozeWrites,
} from "./keyboard-complete";
import { openIssue } from "./keyboard-issue";
import type { KeyedWrite } from "./keyed-writes";
import { expect, type Locator, type Page, test } from "./test";

// ---------------------------------------------------------------------------
// Where the Inbox draws a question and its controls.
// ---------------------------------------------------------------------------

/** The three places the Inbox draws a question with its Snooze and its priority: beside the
 *  reading column, as a card in its row below the column's width, and in the drawer over
 *  another page. */
type InboxPlace = "the reading pane" | "card mode" | "the drawer";

/** A width the Inbox draws its reading column at (it does from 1280 px). */
const WIDE = { height: 900, width: 1440 };

/** A width the Inbox draws every question as a card in its own row. */
const NARROW = { height: 900, width: 1024 };

/** The Inbox as `openInboxAt` opened it, and what the rows reach for in it. */
interface InboxAt {
  readonly place: InboxPlace;
  /** What the Inbox is drawn in: the page, or the drawer over it. */
  readonly scope: Locator;
  /** A place to press that does nothing of its own: the Inbox's heading. */
  readonly outside: Locator;
  /** The questions' ids, in the order the list draws them as it opens. */
  readonly order: readonly string[];
}

/** Opens the Inbox where `place` draws it, holding `rows` questions. The keymap binds only once
 *  sign-in resolves, so the page is read drawn before a key is pressed on it. */
async function openInboxAt(page: Page, place: InboxPlace, rows: number): Promise<InboxAt> {
  await page.setViewportSize(place === "card mode" ? NARROW : WIDE);
  let scope: Locator;
  let outside: Locator;
  if (place === "the drawer") {
    await page.goto("/projects/CORE");
    await expect(page.getByRole("button", { exact: true, name: "Board" })).toBeVisible();
    await page.locator("body").focus();
    await page.keyboard.press("i");
    scope = drawer(page);
    await expect(scope).toBeVisible();
    outside = scope.getByRole("heading", { exact: true, name: "Inbox" });
  } else {
    await page.goto("/");
    scope = page.locator("body");
    outside = page.getByRole("heading", { exact: true, level: 1, name: "Inbox" });
  }
  const list = scope.locator("[data-inbox-row]");
  await expect(list).toHaveCount(rows);
  const order = await list.evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-inbox-row") ?? "")
  );
  return { order, outside, place, scope };
}

/** The row of the question `id`. */
function rowOf(inbox: InboxAt, id: string): Locator {
  return inbox.scope.locator(`[data-inbox-row="${id}"]`);
}

/** Puts the reader on the question `id`: beside the reading column a click on the row's age
 *  selects it, which the column follows, and every place then has the focus on the row, where
 *  the Inbox's keys act on the question in hand. */
async function goTo(inbox: InboxAt, id: string): Promise<void> {
  const row = rowOf(inbox, id);
  if (inbox.place === "the reading pane") {
    await row.locator("time").first().click();
    await expect(row).toHaveAttribute("data-inbox-selected", "true");
  }
  await row.focus();
}

/** The box holding the controls of the question `id`, which the reader is on: the reading
 *  column's head, or the question's own row. */
function controlsOf(page: Page, inbox: InboxAt, id: string): Locator {
  return inbox.place === "the reading pane"
    ? inboxPane(page).locator("header").first()
    : rowOf(inbox, id);
}

/** The toggle of the Inbox's Later band while it holds `count` questions. */
function laterBand(inbox: InboxAt, count: number): Locator {
  return inbox.scope.getByRole("button", { name: new RegExp(`^Later \\(${count}\\)`) });
}

/** How many writes the lists of writes hold together. */
function countOf(writes: readonly (readonly KeyedWrite<unknown>[])[]): number {
  return writes.reduce((sum, list) => sum + list.length, 0);
}

// ---------------------------------------------------------------------------
// The menus' choices.
// ---------------------------------------------------------------------------

/** The Snooze menu's choices, in order, each with the digit that writes it. */
const SNOOZE_CHOICES = [
  ["Later today", "1"],
  ["Tomorrow", "2"],
  ["Next week", "3"],
  ["Until I clear it", "4"],
] as const;

/** The choice a snoozed question's Snooze menu adds after them. */
const UNSNOOZE_CHOICE = ["Un-snooze", "0"] as const;

/** The priority menu's choices, in order: Unset, which no digit writes, and P0 to P3 on 0 to 3. */
const PRIORITY_CHOICES = ["Unset", "P0", "P1", "P2", "P3"] as const;

/** The status menu's choices on an open issue, in order: every status but Done. */
const OPEN_STATUSES = [
  "Triage",
  "Icebox",
  "Backlog",
  "Todo",
  "In progress",
  "Testing",
  "Needs review",
  "Retro",
] as const;

/** Holds the open Snooze menu `menu` to its choices, each with its number, for the step `step`. */
async function expectSnoozeChoices(menu: Locator, step: string, snoozed = false): Promise<void> {
  const expected = snoozed ? [...SNOOZE_CHOICES, UNSNOOZE_CHOICE] : SNOOZE_CHOICES;
  const choices = menu.getByRole("menuitem");
  await expect(choices, `${step}: the menu holds ${expected.length} choices`).toHaveCount(
    expected.length
  );
  for (const [index, [label, digit]] of expected.entries()) {
    const choice = choices.nth(index);
    await expect(choice, `${step}: choice ${index + 1} is ${label}`).toHaveAccessibleName(label);
    await expect(choice, `${step}: ${label} is pressed with ${digit}`).toHaveAttribute(
      "aria-keyshortcuts",
      digit
    );
    await expect(choice.locator("kbd"), `${step}: ${label} shows its number`).toHaveText(digit);
  }
}

/** Holds the open priority menu `menu` to Unset and P0 to P3, each a `menuitemradio`, with
 *  `checked` the one checked, for the step `step`. */
async function expectPriorityChoices(menu: Locator, checked: string, step: string): Promise<void> {
  const choices = menu.getByRole("menuitemradio");
  await expect(choices, `${step}: the menu holds Unset and P0 to P3`).toHaveCount(
    PRIORITY_CHOICES.length
  );
  for (const [index, label] of PRIORITY_CHOICES.entries()) {
    const choice = choices.nth(index);
    await expect(choice, `${step}: choice ${index + 1} is ${label}`).toHaveAccessibleName(label);
    await expect(
      choice,
      `${step}: ${label} is ${label === checked ? "" : "not "}checked`
    ).toHaveAttribute("aria-checked", `${label === checked}`);
  }
}

// ---------------------------------------------------------------------------
// k-snooze-menu
// ---------------------------------------------------------------------------

/** Every step of k-snooze-menu where `place` draws the Inbox, over four asks of their own. */
async function snoozeMenuSteps(page: Page, place: InboxPlace): Promise<void> {
  await createProject({ key: "CORE", name: "Core" });
  for (const title of ["Ledger", "Mirror", "Lease", "Replay"]) {
    const issue = await createIssue({ project: "CORE", title: `${title} rollout` });
    await createAsk(issue.key, { question: `Ship the ${title.toLowerCase()} rollout?` }, asker);
  }
  const inbox = await openInboxAt(page, place, 4);
  const [first, second, third] = inbox.order;
  if (first === undefined || second === undefined || third === undefined) {
    throw new Error("the Inbox lists fewer than the four asks seeded");
  }
  const snoozes = new Map(inbox.order.map((id) => [id, snoozeWrites(page, id)] as const));
  const snoozesOf = (id: string) => snoozes.get(id) ?? [];
  const unsnoozes = unsnoozeWrites(page, first);
  const sent = () => countOf([...snoozes.values(), unsnoozes]);
  const step = (text: string) => `k-snooze-menu: ${place}: ${text}`;
  const menu = page.getByRole("menu");
  const choice = (name: string) => menu.getByRole("menuitem", { exact: true, name });

  // b on the first ask's row, then the arrows and Escape: nothing is written.
  await goTo(inbox, first);
  const band = (await rowOf(inbox, first).getAttribute("data-inbox-section")) ?? "";
  await page.keyboard.press("b");
  await expect(menu, step("b shows a menu")).toBeVisible();
  await expectSnoozeChoices(menu, step("b shows a menu"));
  await expect(choice("Later today"), step("b highlights the first choice")).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(choice("Tomorrow"), step("ArrowDown moves the highlight to Tomorrow")).toBeFocused();
  expect(sent(), step("ArrowDown sends no request")).toBe(0);
  await page.keyboard.press("Escape");
  await expect(menu, step("Escape closes the menu")).toHaveCount(0);
  expect(sent(), step("Escape sends no request")).toBe(0);
  await expect(rowOf(inbox, first), step("Escape leaves the ask in its band")).toHaveAttribute(
    "data-inbox-section",
    band
  );
  await expect(rowOf(inbox, first), step("Escape puts the focus back on its row")).toBeFocused();

  // h opens the same menu, and Enter writes the highlighted Later today.
  await page.keyboard.press("h");
  await expect(menu, step("h opens the same menu")).toBeVisible();
  await expectSnoozeChoices(menu, step("h opens the same menu"));
  await expect(choice("Later today"), step("h highlights the first choice")).toBeFocused();
  const laterFrom = Date.now();
  await page.keyboard.press("Enter");
  await expect
    .poll(() => snoozesOf(first).length, { message: step("Enter writes Later today: one request") })
    .toBe(1);
  expect(sent(), step("Enter sends one request")).toBe(1);
  const laterToday = snoozedUntil(snoozesOf(first)[0]);
  expect(laterToday, step("Enter writes Later today: a moment ahead")).toBeGreaterThan(laterFrom);
  expect(laterToday, step("Enter writes Later today: three hours on at most")).toBeLessThanOrEqual(
    Date.now() + 3 * 60 * 60 * 1000
  );
  await expect(laterBand(inbox, 1), step("Enter puts the ask into Later")).toBeVisible();

  // b on the second ask, then 2: one request, for Tomorrow.
  await goTo(inbox, second);
  await page.keyboard.press("b");
  await expect(menu, step("b opens the second ask's menu")).toBeVisible();
  const mornings = await returnMornings(page);
  await page.keyboard.press("2");
  await expect
    .poll(() => snoozesOf(second).length, { message: step("2 sends one request") })
    .toBe(1);
  expect(sent(), step("2 sends one request")).toBe(2);
  expect(snoozedUntil(snoozesOf(second)[0]), step("2 snoozes the ask until Tomorrow")).toBe(
    mornings.tomorrow
  );

  // The third ask's menu from a click on its Snooze control: Tab, and a click outside, close it.
  await goTo(inbox, third);
  const snooze = controlsOf(page, inbox, third).getByRole("button", { name: /^Snooze / });
  await snooze.click();
  await expect(menu, step("a click on the Snooze control opens its menu")).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(menu, step("Tab closes the menu")).toHaveCount(0);
  expect(sent(), step("Tab sends no request")).toBe(2);
  await expect(snooze, step("Tab leaves the focus on the Snooze control")).toBeFocused();
  await snooze.click();
  await expect(menu, step("a click on the Snooze control opens its menu again")).toBeVisible();
  await inbox.outside.click();
  await expect(menu, step("a click outside the menu closes it")).toHaveCount(0);
  expect(sent(), step("a click outside the menu sends no request")).toBe(2);

  // The second ask, out in Later, and the third, selected with x: the bar's Snooze opens the
  // same menu, and b then 3 snoozes both until Next week.
  await laterBand(inbox, 2).click();
  await expect(rowOf(inbox, second), step("the Later band opens on the second ask")).toBeVisible();
  for (const id of [second, third]) {
    await goTo(inbox, id);
    await page.keyboard.press("x");
  }
  const bar = inbox.scope.getByRole("toolbar", { name: "Selected rows" });
  await expect(bar, step("x selects the second and the third ask")).toContainText("2 selected");
  await bar.getByRole("button", { exact: true, name: "Snooze selected asks" }).click();
  await expect(menu, step("a click on the bar's Snooze opens the same menu")).toHaveAccessibleName(
    "Snooze selected asks"
  );
  await expectSnoozeChoices(menu, step("a click on the bar's Snooze opens the same menu"));
  await page.keyboard.press("Escape");
  await expect(menu, step("Escape closes the bar's menu")).toHaveCount(0);
  expect(sent(), step("Escape on the bar's menu sends no request")).toBe(2);
  await rowOf(inbox, third).focus();
  await page.keyboard.press("b");
  await expect(menu, step("b with asks selected opens the bar's menu")).toHaveAccessibleName(
    "Snooze selected asks"
  );
  const week = await returnMornings(page);
  await page.keyboard.press("3");
  await expect
    .poll(() => [snoozesOf(second).length, snoozesOf(third).length], {
      message: step("b then 3 sends one snooze for each selected ask"),
    })
    .toEqual([2, 1]);
  expect(sent(), step("b then 3 sends two requests")).toBe(4);
  for (const [id, index] of [
    [second, 1],
    [third, 0],
  ] as const) {
    expect(
      snoozedUntil(snoozesOf(id)[index]),
      step("b then 3 snoozes each selected ask until Next week")
    ).toBe(week.nextWeek);
  }
  await expect(bar, step("the bulk snooze lands and clears the selection")).toHaveCount(0);

  // The first ask, snoozed: its menu also holds Un-snooze, which 0 writes.
  await goTo(inbox, first);
  await page.keyboard.press("b");
  await expect(menu, step("b opens a snoozed ask's menu")).toBeVisible();
  await expectSnoozeChoices(menu, step("a snoozed ask's menu also holds Un-snooze"), true);
  await page.keyboard.press("0");
  await expect
    .poll(() => unsnoozes.length, { message: step("0 un-snoozes the ask: one request") })
    .toBe(1);
  expect(sent(), step("0 sends one request, the un-snooze")).toBe(5);
}

// SLOW(>=2s): four asks seeded and snoozed one by one, with the band and the bar read each time.
test("k-snooze-menu: b and h open the Snooze menu beside the reading pane, where only Enter, a click or a digit writes", async ({
  page,
}) => {
  await snoozeMenuSteps(page, "the reading pane");
});

// SLOW(>=2s): four asks seeded and snoozed one by one, with the band and the bar read each time.
test("k-snooze-menu: b and h open the Snooze menu on the Inbox's cards, where only Enter, a click or a digit writes", async ({
  page,
}) => {
  await snoozeMenuSteps(page, "card mode");
});

// SLOW(>=2s): four asks seeded and snoozed one by one, with the band and the bar read each time.
test("k-snooze-menu: b and h open the Snooze menu in the Inbox drawer, where only Enter, a click or a digit writes", async ({
  page,
}) => {
  await snoozeMenuSteps(page, "the drawer");
});

// ---------------------------------------------------------------------------
// k-priority-menu
// ---------------------------------------------------------------------------

/** An ask on an issue at P2, an ask on a project document, which has no issue, and a reply
 *  thread on an issue, all waiting on alice. */
async function seedPriorityInbox(): Promise<{
  askId: string;
  documentAskId: string;
  issueKey: string;
  threadRowId: string;
}> {
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Ledger rollout" });
  await patchIssue(issue.key, { priority: 2 });
  const ask = await createAsk(issue.key, { question: "Ship the ledger rollout?" }, asker);
  const runbook = await createProjectDocument(
    "CORE",
    { content: "# Runbook\n\nRestart, reseed, reopen.\n", name: "Runbook" },
    asker
  );
  const documentAsk = await createArtifactAsk(
    runbook.artifact.id,
    { question: "Keep the runbook in the repository?" },
    asker
  );
  const threadIssue = await createIssue({ project: "CORE", title: "Replay budget" });
  const thread = await seedCommentThread(
    threadIssue.key,
    "How far back does a replay go?",
    "One hour, then it gives up."
  );
  return {
    askId: ask.id,
    documentAskId: documentAsk.id,
    issueKey: issue.key,
    threadRowId: replyRowId(thread),
  };
}

/** The `?` help's line for the Inbox's priority key, in the Inbox's section. */
function priorityKeyHelp(page: Page): Locator {
  return page
    .getByRole("dialog", { name: "Keyboard shortcuts" })
    .getByRole("region", { exact: true, name: "Inbox" })
    .getByRole("listitem")
    .filter({ hasText: "Open the priority menu" });
}

/** The priority mark of the question `id`'s row, about the issue `issueKey`: the short row's own
 *  tag beside the reading column, else the face of the card's priority control. */
function priorityMarkOf(inbox: InboxAt, id: string, issueKey: string): Locator {
  const row = rowOf(inbox, id);
  return inbox.place === "the reading pane"
    ? row.locator("[data-inbox-priority]")
    : row.getByRole("button", { exact: true, name: `Priority of ${issueKey}` });
}

/** Holds the reader on the question `id` after a pick, for the step `step`: the focus back on its
 *  row, and beside the reading column the row still the one the column shows. */
async function expectStillOn(inbox: InboxAt, id: string, step: string): Promise<void> {
  const row = rowOf(inbox, id);
  await expect(row, `${step}: the focus is back on the ask's row`).toBeFocused();
  if (inbox.place === "the reading pane") {
    await expect(row, `${step}: the reading pane still shows the ask`).toHaveAttribute(
      "data-inbox-selected",
      "true"
    );
  }
}

/** Every Inbox step of k-priority-menu where `place` draws it. */
async function priorityMenuSteps(page: Page, place: InboxPlace): Promise<void> {
  const seed = await seedPriorityInbox();
  const inbox = await openInboxAt(page, place, 3);
  const writes = issueWrites(page, seed.issueKey);
  const step = (text: string) => `k-priority-menu: ${place}: ${text}`;
  const menu = page.getByRole("menu");
  const radio = (name: string) => menu.getByRole("menuitemradio", { exact: true, name });

  // p on the ask, then ArrowUp and Escape: nothing is written.
  await goTo(inbox, seed.askId);
  const mark = priorityMarkOf(inbox, seed.askId, seed.issueKey);
  const control = controlsOf(page, inbox, seed.askId).getByRole("button", {
    exact: true,
    name: `Priority of ${seed.issueKey}`,
  });
  await page.keyboard.press("p");
  await expect(menu, step("p shows a menu")).toBeVisible();
  await expectPriorityChoices(menu, "P2", step("p shows a menu"));
  await expect(radio("P2"), step("p highlights the checked P2")).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(radio("P1"), step("ArrowUp moves the highlight to P1")).toBeFocused();
  expect(writes, step("ArrowUp sends no request")).toHaveLength(0);
  await page.keyboard.press("Escape");
  await expect(menu, step("Escape closes the menu")).toHaveCount(0);
  expect(writes, step("Escape sends no request")).toHaveLength(0);
  await expect(mark, step("after Escape the row's mark still reads P2")).toHaveText("P2");

  // p then 0 writes P0; p, ArrowDown, Enter writes the highlighted P1.
  await page.keyboard.press("p");
  await expect(menu, step("p opens the menu again")).toBeVisible();
  await page.keyboard.press("0");
  await expect.poll(() => writes.length, { message: step("p then 0 sends one write") }).toBe(1);
  expect(writes[0]?.input, step("p then 0 writes P0")).toEqual({ priority: 0 });
  await expect(mark, step("after p then 0 the row's mark reads P0")).toHaveText("P0");
  await expectStillOn(inbox, seed.askId, step("after p then 0"));
  await page.keyboard.press("p");
  await expect(radio("P0"), step("p opens on the checked P0")).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(radio("P1"), step("ArrowDown moves the highlight to P1")).toBeFocused();
  await page.keyboard.press("Enter");
  await expect
    .poll(() => writes.length, { message: step("p, ArrowDown, Enter sends one write") })
    .toBe(2);
  expect(writes[1]?.input, step("Enter writes the highlighted P1")).toEqual({ priority: 1 });
  await expect(mark, step("after Enter the row's mark reads P1")).toHaveText("P1");
  await expectStillOn(inbox, seed.askId, step("after p, ArrowDown, Enter"));

  // A click on the priority control opens the same menu.
  await control.click();
  await expect(menu, step("a click on the priority control opens the same menu")).toBeVisible();
  await expectPriorityChoices(
    menu,
    "P1",
    step("a click on the priority control opens the same menu")
  );
  await page.keyboard.press("Escape");
  await expect(menu, step("Escape closes the clicked menu")).toHaveCount(0);

  // On the document's ask and on the thread, p opens nothing, and `?` shows the key unavailable.
  for (const [id, what] of [
    [seed.documentAskId, "the document's ask"],
    [seed.threadRowId, "the thread"],
  ] as const) {
    await goTo(inbox, id);
    await page.keyboard.press("p");
    await expect(menu, step(`on ${what} p opens nothing`)).toHaveCount(0);
    await page.keyboard.press("?");
    await expect(
      priorityKeyHelp(page),
      step(`on ${what} the ? help shows the priority key not available`)
    ).toHaveAttribute("data-enabled", "false");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toHaveCount(0);
  }
  expect(writes, step("nothing else writes the priority")).toHaveLength(2);
}

// SLOW(>=2s): three kinds of item seeded, two writes read back, and the help opened twice.
test("k-priority-menu: p opens the priority menu beside the reading pane, and on no item without an issue", async ({
  page,
}) => {
  await priorityMenuSteps(page, "the reading pane");
});

// SLOW(>=2s): three kinds of item seeded, two writes read back, and the help opened twice.
test("k-priority-menu: p opens the priority menu on the Inbox's cards, and on no item without an issue", async ({
  page,
}) => {
  await priorityMenuSteps(page, "card mode");
});

// SLOW(>=2s): the issue page, the Board and the List, each loaded and its menu opened.
test("k-priority-menu: p opens the priority menu on the issue page and on a Board card, and a click opens it on the List", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger rollout";
  const issue = await createIssue({ project: "CORE", title });
  await patchIssue(issue.key, { priority: 2 });
  await page.setViewportSize(WIDE);
  const writes = issueWrites(page, issue.key);
  const menu = page.getByRole("menu");
  const radio = (name: string) => menu.getByRole("menuitemradio", { exact: true, name });

  // Today's issue page: p opens on the checked P2, and 1 writes P1.
  await openIssue(page, issue.key, title);
  await page.keyboard.press("p");
  await expect(menu, "k-priority-menu: on the issue page p shows a menu").toBeVisible();
  await expectPriorityChoices(menu, "P2", "k-priority-menu: on the issue page p shows a menu");
  await expect(radio("P2"), "k-priority-menu: on the issue page p highlights P2").toBeFocused();
  await page.keyboard.press("1");
  await expect
    .poll(() => writes.length, { message: "k-priority-menu: on the issue page 1 sends one write" })
    .toBe(1);
  expect(writes[0]?.input, "k-priority-menu: on the issue page 1 writes P1").toEqual({
    priority: 1,
  });
  await expect(
    page
      .getByTestId("issue-header")
      .getByRole("button", { exact: true, name: `Priority of ${issue.key}` }),
    "k-priority-menu: on the issue page the header reads P1"
  ).toHaveText("P1");

  // The project Board: p on the focused card opens its menu, and Escape writes nothing.
  await page.goto("/projects/CORE");
  await page.getByRole("button", { exact: true, name: "Board" }).click();
  const card = page.getByRole("article", { name: `${issue.key} ${title}` });
  await card.focus();
  await page.keyboard.press("p");
  await expect(menu, "k-priority-menu: on the Board p opens the card's menu").toHaveAccessibleName(
    `Priority of ${issue.key}`
  );
  await expectPriorityChoices(menu, "P1", "k-priority-menu: on the Board p opens the card's menu");
  await page.keyboard.press("Escape");
  await expect(menu, "k-priority-menu: on the Board Escape closes the menu").toHaveCount(0);
  expect(writes, "k-priority-menu: on the Board Escape writes nothing").toHaveLength(1);

  // The project List: a click on a row's priority control opens the same menu.
  await page.getByRole("button", { exact: true, name: "List" }).click();
  await page
    .getByRole("row", { exact: true, name: `${issue.key} ${title}` })
    .getByRole("button", { exact: true, name: `Priority of ${issue.key}` })
    .click();
  await expect(menu, "k-priority-menu: on the List a click opens the same menu").toBeVisible();
  await expectPriorityChoices(
    menu,
    "P1",
    "k-priority-menu: on the List a click opens the same menu"
  );
  await page.keyboard.press("Escape");
  await expect(menu, "k-priority-menu: on the List Escape closes the menu").toHaveCount(0);
  expect(writes, "k-priority-menu: on the List nothing more is written").toHaveLength(1);
});

// ---------------------------------------------------------------------------
// k-status-menu
// ---------------------------------------------------------------------------

// SLOW(>=2s): the issue page loaded, its status menu opened twice and one write read back.
test("k-status-menu: s opens the status menu on the issue page, where the arrows and Escape write nothing", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger rollout";
  const issue = await createIssue({ project: "CORE", title });
  await patchIssue(issue.key, { status: "todo" });
  await page.setViewportSize(WIDE);
  const writes = issueWrites(page, issue.key);
  const menu = page.getByRole("menu");
  const radio = (name: string) => menu.getByRole("menuitemradio", { exact: true, name });
  const status = page
    .getByTestId("issue-header")
    .getByRole("button", { exact: true, name: "Status" });

  await openIssue(page, issue.key, title);
  await page.keyboard.press("s");
  await expect(menu, "k-status-menu: s shows a menu").toHaveAccessibleName("Status");
  const choices = menu.getByRole("menuitemradio");
  await expect(choices, "k-status-menu: the menu holds the issue's statuses").toHaveCount(
    OPEN_STATUSES.length
  );
  for (const [index, label] of OPEN_STATUSES.entries()) {
    await expect(
      choices.nth(index),
      `k-status-menu: choice ${index + 1} is ${label}`
    ).toHaveAccessibleName(label);
  }
  await expect(radio("Todo"), "k-status-menu: Todo is checked").toHaveAttribute(
    "aria-checked",
    "true"
  );
  await expect(radio("Todo"), "k-status-menu: s highlights the current Todo").toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(
    radio("In progress"),
    "k-status-menu: ArrowDown moves the highlight to In progress"
  ).toBeFocused();
  expect(writes, "k-status-menu: ArrowDown sends no request").toHaveLength(0);
  await page.keyboard.press("Escape");
  await expect(menu, "k-status-menu: Escape closes the menu").toHaveCount(0);
  expect(writes, "k-status-menu: Escape sends no request").toHaveLength(0);
  await expect(status, "k-status-menu: after Escape the status still reads Todo").toHaveText(
    "Todo"
  );

  await page.keyboard.press("s");
  await expect(radio("Todo"), "k-status-menu: s opens the menu again on Todo").toBeFocused();
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await expect
    .poll(() => writes.length, { message: "k-status-menu: s, ArrowDown, Enter sends one write" })
    .toBe(1);
  expect(writes[0]?.input, "k-status-menu: Enter writes the highlighted In progress").toEqual({
    status: "in_progress",
  });
  await expect(status, "k-status-menu: the header shows In progress").toHaveText("In progress");
});

// ---------------------------------------------------------------------------
// k-menu-contrast (the menus' part; the single-pick lists' part is its own row)
// ---------------------------------------------------------------------------

/** Opens a menu with `key` and reads the contrast of each of its choices, the `role` choices
 *  named `names`, failing softly for each pair under 4.5:1 with the theme, the menu and the
 *  choice named. */
async function readMenuContrast(
  page: Page,
  key: string,
  what: string,
  role: "menuitem" | "menuitemradio",
  names: readonly string[]
): Promise<void> {
  const menu = page.getByRole("menu");
  await page.keyboard.press(key);
  await expect(menu, `k-menu-contrast: ${what}: ${key} opens the menu`).toBeVisible();
  for (const name of names) {
    const reading = await contrastOf(menu.getByRole(role, { exact: true, name }));
    expect
      .soft(
        reading.ratio,
        `k-menu-contrast: ${what}, ${name}: ${reading.color} on ${reading.background}`
      )
      .toBeGreaterThanOrEqual(4.5);
  }
  await page.keyboard.press("Escape");
  await expect(menu, `k-menu-contrast: ${what}: Escape closes the menu`).toHaveCount(0);
}

// SLOW(>=2s): two themes, each with the Inbox and the issue page loaded and three menus read.
test("k-menu-contrast: every choice of the snooze, priority and status menus reads at 4.5:1 or more in the light and the dark theme", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger rollout";
  const issue = await createIssue({ project: "CORE", title });
  await patchIssue(issue.key, { priority: 2 });
  const ask = await createAsk(issue.key, { question: "Ship the ledger rollout?" }, asker);
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    const theme = `the ${colorScheme} theme`;
    const inbox = await openInboxAt(page, "the reading pane", 1);
    await goTo(inbox, ask.id);
    await readMenuContrast(
      page,
      "b",
      `${theme}, the Snooze menu`,
      "menuitem",
      SNOOZE_CHOICES.map(([label]) => label)
    );
    await readMenuContrast(
      page,
      "p",
      `${theme}, the priority menu`,
      "menuitemradio",
      PRIORITY_CHOICES
    );
    await openIssue(page, issue.key, title);
    await readMenuContrast(page, "s", `${theme}, the status menu`, "menuitemradio", OPEN_STATUSES);
  }
});

// ---------------------------------------------------------------------------
// k-phone
// ---------------------------------------------------------------------------

/** Holds each of `choices` to a tap's height, 44 px or more, naming `what` and the choice. */
async function expectTapHeight(choices: Locator, what: string): Promise<void> {
  const heights = await choices.evaluateAll((nodes) =>
    nodes.map((node) => ({
      height: node.getBoundingClientRect().height,
      name: node.textContent ?? "",
    }))
  );
  expect(heights.length, `k-phone: ${what} has choices`).toBeGreaterThan(0);
  for (const { height, name } of heights) {
    expect(
      height,
      `k-phone: ${what}'s choice ${name} is 44 px high or more`
    ).toBeGreaterThanOrEqual(44);
  }
}

// SLOW(>=2s): two asks seeded, two menus tapped open and two writes read back on a phone.
test("k-phone: a tap opens the Snooze and the priority menus, whose choices take a tap and write only when tapped @phone @phone-only", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issueOf = new Map<string, string>();
  for (const title of ["Ledger", "Mirror"]) {
    const issue = await createIssue({ project: "CORE", title: `${title} rollout` });
    await patchIssue(issue.key, { priority: 2 });
    const ask = await createAsk(
      issue.key,
      { question: `Ship the ${title.toLowerCase()} rollout?` },
      asker
    );
    issueOf.set(ask.id, issue.key);
  }
  await page.goto("/");
  const rows = page.locator("[data-inbox-row]");
  await expect(rows).toHaveCount(2);
  const [first, second] = await rows.evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-inbox-row") ?? "")
  );
  const secondKey = issueOf.get(second ?? "");
  if (first === undefined || second === undefined || secondKey === undefined) {
    throw new Error("the Inbox lists other asks than the two seeded");
  }
  const snoozes = [snoozeWrites(page, first), snoozeWrites(page, second)];
  const priorityWrites = issueWrites(page, secondKey);
  const otherWrites = [...issueOf.values()]
    .filter((key) => key !== secondKey)
    .map((key) => issueWrites(page, key));
  const sent = () => countOf([...snoozes, priorityWrites, ...otherWrites]);
  const menu = page.getByRole("menu");

  const snooze = page
    .locator(`[data-inbox-row="${first}"]`)
    .getByRole("button", { name: /^Snooze / });
  await snooze.tap();
  await expect(
    menu,
    "k-phone: a tap on the first card's Snooze opens the snooze menu"
  ).toBeVisible();
  await expectTapHeight(menu.getByRole("menuitem"), "the snooze menu");
  await page.getByRole("heading", { exact: true, level: 1, name: "Inbox" }).tap();
  await expect(menu, "k-phone: a tap outside the menu closes it").toHaveCount(0);
  expect(sent(), "k-phone: a tap outside the menu sends no request").toBe(0);
  await snooze.tap();
  await menu.getByRole("menuitem", { exact: true, name: "Tomorrow" }).tap();
  await expect
    .poll(() => snoozes[0]?.length, { message: "k-phone: a tap on Tomorrow sends one snooze" })
    .toBe(1);
  expect(sent(), "k-phone: a tap on Tomorrow sends nothing else").toBe(1);

  await page
    .locator(`[data-inbox-row="${second}"]`)
    .getByRole("button", { exact: true, name: `Priority of ${secondKey}` })
    .tap();
  await expect(menu, "k-phone: a tap on the second card's priority opens its menu").toBeVisible();
  await expectTapHeight(menu.getByRole("menuitemradio"), "the priority menu");
  await menu.getByRole("menuitemradio", { exact: true, name: "P1" }).tap();
  await expect
    .poll(() => priorityWrites.length, { message: "k-phone: a tap on P1 sends one write" })
    .toBe(1);
  expect(priorityWrites[0]?.input, "k-phone: a tap on P1 writes P1").toEqual({ priority: 1 });
  expect(sent(), "k-phone: a tap on P1 sends nothing else").toBe(2);
});

// ---------------------------------------------------------------------------
// held-press (dispatch://INFRA-4248, folded into this issue: no check of the Verification names
// it). A control whose write is out is held, not disabled, so the keyboard's focus stays on it.
// ---------------------------------------------------------------------------

/** Whether `control` is the page's `document.activeElement`. */
function isActive(control: Locator): Promise<boolean> {
  return control.evaluate((element) => element === document.activeElement);
}

/** Presses `control` by the keyboard with `key`, and again while the write it sent is held, and
 *  reads that the focus stays on it while the write is out and once it has landed, and that the
 *  two presses sent one write (`writes`, as `holdWrites` caught them). */
async function pressTwiceWhileHeld(
  page: Page,
  control: Locator,
  key: "Enter" | "Space",
  what: string,
  writes: readonly string[]
): Promise<void> {
  await control.focus();
  expect(await isActive(control), `held-press: ${what} takes the focus`).toBe(true);
  await page.keyboard.press(key);
  await expect(
    control,
    `held-press: ${what} is held (aria-disabled) while its write is out`
  ).toHaveAttribute("aria-disabled", "true");
  expect(
    await isActive(control),
    `held-press: ${what} keeps the focus while its write is out`
  ).toBe(true);
  await page.keyboard.press(key);
  expect(
    await isActive(control),
    `held-press: ${what} keeps the focus after a second ${key} while held`
  ).toBe(true);
  await expect(control, `held-press: ${what} lets go once its write lands`).not.toHaveAttribute(
    "aria-disabled",
    "true"
  );
  expect(await isActive(control), `held-press: ${what} keeps the focus once its write lands`).toBe(
    true
  );
  expect(writes, `held-press: ${what} sent exactly one write for two presses`).toHaveLength(1);
}

// SLOW(>=2s): three writes each held 400 ms, on the issue page and on Settings.
test("held-press: a button, a form's submit and a checkbox keep the keyboard's focus while their write is out, and two presses send one write", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger rollout";
  const issue = await createIssue({ project: "CORE", title });
  await page.setViewportSize(WIDE);

  // A plain button: the issue header's pin.
  const pins = await holdWrites(page, "PUT", new RegExp(`^/api/v1/me/issues/${issue.key}/state$`));
  await openIssue(page, issue.key, title);
  const pin = page.getByTestId("issue-header").getByRole("button", { name: /^(Un)?pin issue$/i });
  await pressTwiceWhileHeld(page, pin, "Enter", "the issue's Pin button", pins);
  await expect(pin, "held-press: the pin landed").toHaveAccessibleName("Unpin issue");

  // A form's submit: Settings' New token.
  const mints = await holdWrites(page, "POST", /^\/api\/v1\/me\/agent-tokens$/);
  const scopeWrites = await holdWrites(page, "PATCH", /^\/api\/v1\/me\/agent-tokens\/[^/]+$/);
  await page.goto("/settings");
  await page.getByLabel("Token label").fill("Held press");
  const mint = page.getByRole("button", { exact: true, name: "New token" });
  await pressTwiceWhileHeld(page, mint, "Enter", "Settings' New token submit", mints);
  const approve = page.getByRole("checkbox", {
    exact: true,
    name: "Approve and control credentials (Held press)",
  });
  await expect(approve, "held-press: the minted token holds Approve").toBeChecked();

  // A checkbox: the minted token's Approve scope.
  await pressTwiceWhileHeld(page, approve, "Space", "the token's Approve checkbox", scopeWrites);
  await expect(approve, "held-press: the one write cleared Approve").not.toBeChecked();
  expect(
    JSON.parse(scopeWrites[0] ?? "{}"),
    "held-press: the write names Act as you alone"
  ).toEqual({ scopes: ["person"] });
});
