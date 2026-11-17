/** The browser checks of dispatch://INFRA-2160, every page of Dispatch by the keyboard: one row
 *  or more for each check of its Verification document, each row's title starting with the id
 *  of the check it proves, so `-g '<id>'` runs that check's rows. Every step of a check is an
 *  assertion whose message names the check and the step, so a failure names the step it stopped
 *  at - which is also how the negative control reads: the same rows run on a build of the merge
 *  base, where they fail at the first step the merge base does not do.
 *
 *  The rows write - snoozes, priorities, statuses, assignees, repositories, hand-ons - and count
 *  each write at the server's own route, so they run on the real stack. They import nothing this
 *  tree adds to or changes in `web/src`: what they need from the app they read off the page or
 *  work out in `e2e/keyboard-complete.ts`. */
import { setLiveSessions } from "./agents";
import {
  baseUrl,
  createArtifactAsk,
  createAsk,
  createComment,
  createIssue,
  createProject,
  createProjectDocument,
  editArtifact,
  getIssueArtifactText,
  patchIssue,
  putRepoProject,
  requestApproval,
  withOpenedAsk,
} from "./api";
import { recordClipboard } from "./clipboard";
import { documentEditor, openSpec, placeCaret } from "./editor";
import { drawer } from "./inbox-drawer";
import { inboxPane, paneScroller } from "./inbox-pane";
import { replyRowId, seedCommentThread, THREAD_SESSION } from "./inbox-threads";
import {
  asker,
  contrastOf,
  handOnControl,
  handOnWrites,
  holdWrites,
  issueWrites,
  markDocument,
  newPageAt,
  OTHER_SITE,
  reloaded,
  returnMornings,
  snoozedUntil,
  snoozeWrites,
  unsnoozeWrites,
} from "./keyboard-complete";
import { openIssue } from "./keyboard-issue";
import type { KeyedWrite } from "./keyed-writes";
import { type BrowserContext, expect, type Locator, type Page, test } from "./test";

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
// k-choice-writes-on-enter
// ---------------------------------------------------------------------------

/** The ids k-choice-writes-on-enter and k-menu-contrast's lists read, and the issue's title. */
interface ChoiceSeeds {
  readonly askId: string;
  readonly issueKey: string;
  readonly title: string;
}

/** The seeds of k-choice-writes-on-enter: alice and bob, who have signed in (the reset seeds
 *  both); CORE, with two repositories mapped to it, acme/widgets its default; an open issue of
 *  CORE, which alice holds as its creator; and a question on it that waits on her. */
async function seedChoices(): Promise<ChoiceSeeds> {
  await createProject({ key: "CORE", name: "Core" });
  await putRepoProject("acme/widgets", { default: true, project: "CORE" });
  await putRepoProject("acme/gadgets", { project: "CORE" });
  const title = "Ledger rollout";
  const issue = await createIssue({ project: "CORE", title });
  const ask = await createAsk(issue.key, { question: "Ship the ledger rollout?" }, asker);
  return { askId: ask.id, issueKey: issue.key, title };
}

/** The keys k-choice-writes-on-enter presses on a choice control, in order, before the letter
 *  that starts a choice. */
const CHOICE_KEYS = ["ArrowDown", "ArrowUp", "Home", "End", "PageDown", "PageUp"] as const;

/** How many presses of Tab may pass before the focus reaches a control from the page's start. */
const TAB_PRESSES = 200;

/** One choice control of R6, as k-choice-writes-on-enter drives it. */
interface ChoiceControl {
  /** What the failures call it: "the assignee". */
  readonly what: string;
  readonly control: Locator;
  /** Every write the control sends, counted at the server's route. */
  readonly writes: readonly KeyedWrite<unknown>[];
  /** A letter that starts a choice other than the item's value. */
  readonly letter: string;
  /** The body of the write of the choice the control shows as `choice`. */
  readonly body: (choice: string) => unknown;
}

/** What a choice control shows: a native select's own value; else, while its list is open, the
 *  option the list's text box highlights, and whether that option is the item's value
 *  (`aria-selected`); else the text of the control itself. */
interface Shown {
  readonly open: boolean;
  readonly text: string;
  /** A native select's value. */
  readonly value?: string;
  /** Whether the option an open list highlights is the one that holds the item's value. */
  readonly picked?: boolean;
}

/** Reads what `control` shows now (`Shown`). */
function shownBy(control: Locator): Promise<Shown> {
  return control.evaluate((element): Shown => {
    if (element instanceof HTMLSelectElement) {
      const text = element.selectedOptions[0]?.textContent ?? "";
      return { open: false, text: text.trim(), value: element.value };
    }
    const expanded = element.getAttribute("aria-expanded") === "true";
    const panel = expanded
      ? document.getElementById(element.getAttribute("aria-controls") ?? "")
      : null;
    const box = panel?.querySelector('[role="combobox"]');
    const active = document.getElementById(box?.getAttribute("aria-activedescendant") ?? "");
    if (active !== null) {
      return {
        open: true,
        picked: active.getAttribute("aria-selected") === "true",
        text: (active.textContent ?? "").replace("✓", "").trim(),
      };
    }
    return { open: panel !== null, text: (element.textContent ?? "").trim() };
  });
}

/** Whether `shown` is a choice other than the item's value, which the control showed as `rest`
 *  with the focus on it and nothing pressed. */
function showsOther(shown: Shown, rest: Shown): boolean {
  if (shown.value !== undefined) return shown.value !== rest.value;
  if (shown.picked !== undefined) return !shown.picked;
  return shown.text !== rest.text;
}

/** Puts the focus on `control` with the keyboard: Tab, press by press from where the focus is,
 *  until the control holds it. */
async function tabTo(page: Page, control: Locator, message: string): Promise<void> {
  for (let press = 0; press < TAB_PRESSES && !(await isActive(control)); press += 1) {
    await page.keyboard.press("Tab");
  }
  expect(await isActive(control), message).toBe(true);
}

/** Presses `CHOICE_KEYS` and then `choice.letter`, reading after each press that no request went
 *  out, and that at least one press showed a choice other than the item's value (`rest`). The
 *  choice shown after the last press is the answer. */
async function pressChoiceKeys(
  page: Page,
  choice: ChoiceControl,
  rest: Shown,
  step: string
): Promise<Shown> {
  const say = `k-choice-writes-on-enter: ${choice.what}, ${step}`;
  let shown = rest;
  let other = false;
  for (const key of [...CHOICE_KEYS, choice.letter]) {
    await page.keyboard.press(key);
    shown = await shownBy(choice.control);
    other ||= showsOther(shown, rest);
    expect(choice.writes, `${say}: ${key} sends no request`).toHaveLength(0);
  }
  expect(other, `${say}: a key shows a choice other than the item's value, ${rest.text}`).toBe(
    true
  );
  return shown;
}

/** Reads that `choice` shows the item's value again (`rest`), with its list closed, and that no
 *  request went out, after the step `step`. */
async function expectItemValue(choice: ChoiceControl, rest: Shown, step: string): Promise<void> {
  const say = `k-choice-writes-on-enter: ${choice.what}, ${step}`;
  await expect
    .poll(
      async () => {
        const shown = await shownBy(choice.control);
        return !shown.open && (shown.value ?? shown.text) === (rest.value ?? rest.text);
      },
      { message: `${say} shows the item's value, ${rest.text}` }
    )
    .toBe(true);
  expect(choice.writes, `${say} sends no request`).toHaveLength(0);
}

/** Every step of k-choice-writes-on-enter on one choice control, on the page that draws it. */
async function choiceWritesOnEnter(page: Page, choice: ChoiceControl): Promise<void> {
  const say = `k-choice-writes-on-enter: ${choice.what}`;
  await tabTo(page, choice.control, `${say}: Tab reaches the control`);
  const rest = await shownBy(choice.control);

  // The keys with the focus on the control, then Escape.
  await pressChoiceKeys(page, choice, rest, "the keys on the focused control");
  await page.keyboard.press("Escape");
  await expectItemValue(choice, rest, "Escape after the keys on the focused control");

  // The keys in the list a click opened, then Escape.
  await choice.control.click();
  await pressChoiceKeys(page, choice, rest, "the keys in the list a click opened");
  await page.keyboard.press("Escape");
  await expectItemValue(choice, rest, "Escape after the keys in the list a click opened");

  // The keys again, then Tab.
  await expect(choice.control, `${say}: Escape puts the focus back on the control`).toBeFocused();
  await pressChoiceKeys(page, choice, rest, "the keys before Tab");
  await page.keyboard.press("Tab");
  await expectItemValue(choice, rest, "Tab after the keys");

  // The keys again, then Enter: one request, of the choice shown.
  await expect(choice.control, `${say}: Tab leaves the focus on the control`).toBeFocused();
  const chosen = await pressChoiceKeys(page, choice, rest, "the keys before Enter");
  await page.keyboard.press("Enter");
  await expect
    .poll(() => choice.writes.length, { message: `${say}: Enter sends one request` })
    .toBe(1);
  expect(choice.writes[0]?.input, `${say}: Enter writes the choice shown, ${chosen.text}`).toEqual(
    choice.body(chosen.text)
  );
}

// SLOW(>=2s): the issue page loaded and its assignee driven through four passes of seven keys.
// @webkit @firefox: the keys each engine sends to a focused control and to its list's text box,
// and the Tab that leaves that box.
test("k-choice-writes-on-enter: on the issue page the assignee's keys only change the choice shown, and only Enter writes @firefox @webkit", async ({
  page,
}) => {
  const { issueKey, title } = await seedChoices();
  await page.setViewportSize(WIDE);
  const writes = issueWrites(page, issueKey);
  await openIssue(page, issueKey, title);
  await choiceWritesOnEnter(page, {
    body: (choice) => ({ assignee: choice }),
    control: page.getByLabel(`Assignee of ${issueKey}`, { exact: true }),
    letter: "b",
    what: "the assignee",
    writes,
  });
});

// SLOW(>=2s): the issue page loaded and its repository driven through four passes of seven keys.
// @webkit @firefox: the keys each engine sends to a focused control and to its list's text box,
// and the Tab that leaves that box.
test("k-choice-writes-on-enter: on the issue page the repository's keys only change the choice shown, and only Enter writes @firefox @webkit", async ({
  page,
}) => {
  const { issueKey, title } = await seedChoices();
  await page.setViewportSize(WIDE);
  const writes = issueWrites(page, issueKey);
  await openIssue(page, issueKey, title);
  await choiceWritesOnEnter(page, {
    body: (choice) => ({ repo: choice }),
    control: page.getByLabel(`Repository of ${issueKey}`, { exact: true }),
    letter: "a",
    what: "the repository",
    writes,
  });
});

// SLOW(>=2s): the Inbox loaded in card mode and a question's hand-on driven through four passes.
// @webkit @firefox: the keys each engine sends to a focused control and to its list's text box,
// and the Tab that leaves that box.
test("k-choice-writes-on-enter: on a question's card the hand-on's keys only change the choice shown, and only Enter writes @firefox @webkit", async ({
  page,
}) => {
  const { askId } = await seedChoices();
  const writes = handOnWrites(page, askId);
  const inbox = await openInboxAt(page, "card mode", 1);
  await choiceWritesOnEnter(page, {
    body: (choice) => ({ asked_of: choice }),
    control: handOnControl(rowOf(inbox, askId), askId),
    letter: "b",
    what: "the hand-on control",
    writes,
  });
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

/** Reads the contrast of the choice control `control` and of each choice of its list, failing
 *  softly for each pair under 4.5:1, and for a native `<select>` or `<option>` whose own
 *  background is transparent, with the theme, the control and the choice named. A native select
 *  is read with its options; the shared list's trigger is read on the element that draws its
 *  text, and its options are read in the list a click on it opens. */
async function readChoiceContrast(control: Locator, what: string): Promise<void> {
  const native = await control.evaluate((element) => element instanceof HTMLSelectElement);
  const measure = async (target: Locator, name: string) => {
    const reading = await contrastOf(target);
    const say = `k-menu-contrast: ${what}, ${name}`;
    if (native) {
      expect
        .soft(reading.ownBackgroundAlpha, `${say}: a native ${name} with a transparent background`)
        .toBeGreaterThan(0);
    }
    expect
      .soft(reading.ratio, `${say}: ${reading.color} on ${reading.background}`)
      .toBeGreaterThanOrEqual(4.5);
  };
  if (native) {
    await measure(control, "select");
    const options = control.locator("option");
    for (let index = 0; index < (await options.count()); index += 1) {
      const option = options.nth(index);
      await measure(option, `option ${(await option.textContent()) ?? index}`);
    }
    return;
  }
  await measure(control.locator("xpath=.//*[text()[normalize-space()]]").first(), "the control");
  await control.click();
  const list = control.locator("xpath=id(./@aria-controls)");
  const options = list.getByRole("option");
  await expect(options.first(), `k-menu-contrast: ${what}: a click opens its list`).toBeVisible();
  for (let index = 0; index < (await options.count()); index += 1) {
    const option = options.nth(index);
    await measure(option, `the choice ${(await option.innerText()).replace("✓", "").trim()}`);
  }
  await control.page().keyboard.press("Escape");
  await expect(list, `k-menu-contrast: ${what}: Escape closes its list`).toHaveCount(0);
}

// SLOW(>=2s): two themes, each with the issue page and the Inbox loaded and three lists read.
test("k-menu-contrast: every choice of the assignee, repository and hand-on lists reads at 4.5:1 or more in the light and the dark theme", async ({
  page,
}) => {
  const { askId, issueKey, title } = await seedChoices();
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    const theme = `the ${colorScheme} theme`;
    await page.setViewportSize(WIDE);
    await openIssue(page, issueKey, title);
    await readChoiceContrast(
      page.getByLabel(`Assignee of ${issueKey}`, { exact: true }),
      `${theme}, the assignee`
    );
    await readChoiceContrast(
      page.getByLabel(`Repository of ${issueKey}`, { exact: true }),
      `${theme}, the repository`
    );
    const inbox = await openInboxAt(page, "card mode", 1);
    await readChoiceContrast(
      handOnControl(rowOf(inbox, askId), askId),
      `${theme}, the hand-on control`
    );
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

// ---------------------------------------------------------------------------
// k-link-click, k-link-keys and k-link-copy (R1, R2, R23): a link of a document, a comment, a
// question or a past version opens on a click and from the keyboard, and `y` copies the address
// of the link the focus is on.
// ---------------------------------------------------------------------------

/** The other site every web link below points to, which `newPageAt` serves. */
const AWAY = `${OTHER_SITE}/a`;
/** The other site the link inside the spec's commented text points to. */
const COMMENTED_AWAY = `${OTHER_SITE}/b`;

/** The issue the links name, and the issue whose spec holds them. */
interface LinkSeed {
  readonly other: { readonly key: string; readonly title: string };
  readonly issueKey: string;
  /** The comment on the issue that holds the first three links, on its Conversation tab. */
  readonly commentId: string;
  /** The approval request of the spec, which the Inbox's reading pane draws as the spec. */
  readonly approvalAskId: string;
}

/** The seeds of k-link-click and k-link-keys: an open issue whose spec holds a link to another
 *  site, a `dispatch://` link to another issue, that issue's bare key, its address as a link
 *  whose text is the address (whose title the editor draws beside a link it hides), and a web
 *  link inside text a comment is anchored to; a comment on the issue holding the first three
 *  links; a later version of the spec, so its version 1 is a past version; and an approval
 *  request of the spec. */
async function seedLinkSpec(): Promise<LinkSeed> {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger handover";
  const other = await createIssue({ project: "CORE", title });
  const issue = await createIssue({
    project: "CORE",
    spec: [
      "# Mirror replay",
      "",
      `Read [a page elsewhere](${AWAY}) first.`,
      "",
      `See [the other issue](dispatch://${other.key}) for the handover.`,
      "",
      `The bare key ${other.key} names it too.`,
      "",
      `Its address <dispatch://${other.key}> reads as its title.`,
      "",
      `A reviewer asked about [the commented page](${COMMENTED_AWAY}) here.`,
      "",
    ].join("\n"),
    title: "Mirror replay",
  });
  await createComment(issue.key, {
    anchor: { artifact: "spec", quote: "the commented page" },
    body: "Is this the right page?",
  });
  const comment = await createComment(issue.key, {
    body: `Read [a page elsewhere](${AWAY}), see [the other issue](dispatch://${other.key}) and ${other.key}.`,
  });
  await editArtifact(
    issue.primary_artifact_id,
    { ops: [{ after: "end", markdown: "A later line.", op: "insert" }] },
    asker
  );
  const approval = withOpenedAsk(
    await requestApproval(issue.primary_artifact_id, { summary: "The replay draft." }, asker)
  );
  return {
    approvalAskId: approval.ask.id,
    commentId: comment.id,
    issueKey: issue.key,
    other: { key: other.key, title },
  };
}

/** The four places a reader reads the spec's links in: the issue's Spec tab, which the reader
 *  edits, version 1 of it, the Inbox's reading pane on the approval request of it, and the
 *  comment on the issue's Conversation tab. */
type LinkView = "the editable spec" | "the past version" | "the reading pane" | "the comment";

/** The three links of a view: the web link, the `dispatch://` link, and the title drawn for the
 *  bare key (in the comment, the reference link the body draws for it). */
interface ViewLinks {
  readonly scope: Locator;
  readonly away: Locator;
  readonly issue: Locator;
  readonly key: Locator;
}

/** Opens `view` at 1440x900 and answers its links, once the key's title is drawn. */
async function openLinkView(page: Page, seed: LinkSeed, view: LinkView): Promise<ViewLinks> {
  await page.setViewportSize(WIDE);
  let scope: Locator;
  if (view === "the editable spec") {
    await openSpec(page, seed.issueKey, "here.");
    scope = documentEditor(page);
  } else if (view === "the past version") {
    await page.goto(`/issues/${seed.issueKey}/artifacts/spec?v=1`);
    scope = page
      .getByRole("region", { name: "Document version 1" })
      .getByRole("textbox", { name: "Document editor" });
  } else if (view === "the reading pane") {
    await page.goto("/?assignee=everyone");
    const row = page.locator(`[data-inbox-row="${seed.approvalAskId}"]`);
    await row.locator("time").first().click();
    await expect(row).toHaveAttribute("data-inbox-selected", "true");
    scope = inboxPane(page).getByRole("textbox", { name: "Document editor" });
  } else {
    await page.goto(`/issues/${seed.issueKey}/conversation`);
    scope = page.locator(`[data-turn="comment:${seed.commentId}"]`);
  }
  const reference = `dispatch://${seed.other.key}`;
  const links: ViewLinks =
    view === "the comment"
      ? {
          away: scope.getByRole("link", { exact: true, name: "a page elsewhere" }),
          issue: scope.locator(`a[data-dispatch-ref="${reference}"]`).nth(0),
          key: scope.locator(`a[data-dispatch-ref="${reference}"]`).nth(1),
          scope,
        }
      : {
          away: scope.getByRole("link", { exact: true, name: "a page elsewhere" }),
          issue: scope.getByRole("link", { exact: true, name: "the other issue" }),
          key: scope.locator(`a.dispatch-reference[data-dispatch-href="${reference}"]`).first(),
          scope,
        };
  await expect(links.away, `${view} draws the web link`).toBeVisible();
  await expect(links.key, `${view} draws the key's title`).toContainText(seed.other.title);
  return links;
}

/** What `act` opens in a new page of `context`, whose address it answers once loaded; the page is
 *  closed. */
async function openedAddress(
  context: BrowserContext,
  act: () => Promise<unknown>
): Promise<string> {
  const opened = await newPageAt(context, act);
  const address = opened.url();
  await opened.close();
  return address;
}

/** Whether `page` shows the other issue's page, reached with no new load of the application. */
async function expectInApp(page: Page, seed: LinkSeed, what: string): Promise<void> {
  await expect(page, `${what} moves the page to ${seed.other.key}`).toHaveURL(
    new RegExp(`/issues/${seed.other.key}(/|$)`)
  );
  expect(await reloaded(page), `${what} loads no new document`).toBe(false);
}

/** The spec's stored Markdown, as the server holds it. */
async function storedSpec(seed: LinkSeed): Promise<string> {
  return (await getIssueArtifactText(seed.issueKey, "spec")).markdown;
}

/** A plain primary click on each of `view`'s three links: the web link opens a new page at the
 *  other site and the Dispatch page stays where it was; the `dispatch://` link and the key move
 *  the same page to the other issue, with no new load of the application. */
async function clickEachLink(
  context: BrowserContext,
  page: Page,
  seed: LinkSeed,
  view: LinkView
): Promise<void> {
  let links = await openLinkView(page, seed, view);
  const at = page.url();
  expect(
    await openedAddress(context, () => links.away.click()),
    `k-link-click: in ${view}, a click on the web link opens a new page at ${AWAY}`
  ).toBe(AWAY);
  expect(page.url(), `k-link-click: in ${view}, the Dispatch page stays at its address`).toBe(at);

  await markDocument(page);
  await links.issue.click();
  await expectInApp(page, seed, `k-link-click: in ${view}, a click on the dispatch:// link`);

  links = await openLinkView(page, seed, view);
  await markDocument(page);
  await links.key.click();
  await expectInApp(page, seed, `k-link-click: in ${view}, a click on the key's title`);
}

// SLOW(>=2s): the spec opened four times, two new pages loaded, and four new tabs of Dispatch.
test("k-link-click: in the spec the reader edits, a plain click opens each link, a pointer over one shows no Open link card, and Ctrl and a middle click open a new tab", async ({
  context,
  page,
}) => {
  const seed = await seedLinkSpec();
  await clickEachLink(context, page, seed, "the editable spec");

  // The web link inside the text a comment is anchored to opens too, rather than the comment.
  await openLinkView(page, seed, "the editable spec");
  const commented = documentEditor(page).getByRole("link", {
    exact: true,
    name: "the commented page",
  });
  expect(
    await openedAddress(context, () => commented.click()),
    `k-link-click: in the editable spec, a click on the link inside commented text opens ${COMMENTED_AWAY}`
  ).toBe(COMMENTED_AWAY);

  const links = await openLinkView(page, seed, "the editable spec");
  await links.away.hover();
  // The Open link card showed as the pointer rested on a link, so the pointer rests the
  // half second the check names before the row looks for the card.
  await page.waitForTimeout(500);
  const cards = await page
    .getByText("Open link", { exact: true })
    .evaluateAll((nodes) => nodes.filter((node) => (node as HTMLElement).checkVisibility()).length);
  expect(cards, "k-link-click: a pointer over the web link shows no Open link card").toBe(0);

  // Headless Chromium has no primary selection, so a middle click pastes nothing here whether or
  // not the editor prevents its press and release: that part of the check is held by the
  // editor's own unit test, "a middle button's press and release on a link are prevented, so the
  // browser pastes nothing" (packages/proof-editor/tests/dispatch-link-open.test.ts). The row
  // reads that the Markdown is the same after the middle clicks all the same.
  const before = await storedSpec(seed);
  const otherPage = new URL(`/issues/${seed.other.key}`, page.url()).href;
  for (const [link, what] of [
    [links.issue, "the dispatch:// link"],
    [links.key, "the key's title"],
  ] as const) {
    expect(
      await openedAddress(context, () => link.click({ modifiers: ["ControlOrMeta"] })),
      `k-link-click: a Ctrl+click on ${what} opens a new page at the other issue`
    ).toMatch(new RegExp(`^${otherPage}(/|$)`));
    expect(
      await openedAddress(context, () => link.click({ button: "middle" })),
      `k-link-click: a middle click on ${what} opens a new page at the other issue`
    ).toMatch(new RegExp(`^${otherPage}(/|$)`));
  }
  expect(
    page.url(),
    "k-link-click: the spec stays open through the Ctrl and middle clicks"
  ).toContain(`/issues/${seed.issueKey}/spec`);
  expect(
    await storedSpec(seed),
    "k-link-click: the middle clicks leave the spec's Markdown as it was"
  ).toBe(before);
});

for (const view of ["the past version", "the reading pane", "the comment"] as const) {
  // SLOW(>=2s): the view opened twice, a new page loaded and two moves to the other issue.
  test(`k-link-click: in ${view}, a plain click opens each link`, async ({ context, page }) => {
    const seed = await seedLinkSpec();
    await clickEachLink(context, page, seed, view);
  });
}

// SLOW(>=2s): the spec opened three times and a new page loaded.
test("k-link-keys: in the spec the reader edits, Alt+Enter with the caret in a link or beside a key's title opens it and changes no text", async ({
  context,
  page,
}) => {
  const seed = await seedLinkSpec();
  await openLinkView(page, seed, "the editable spec");
  const before = await storedSpec(seed);

  // The caret into the web link's words, "Read a p|age elsewhere", by the arrow keys.
  await placeCaret(page, "before", "Read");
  for (let step = 0; step < 8; step += 1) await page.keyboard.press("ArrowRight");
  expect(
    await openedAddress(context, () => page.keyboard.press("Alt+Enter")),
    `k-link-keys: Alt+Enter in the web link opens a new page at ${AWAY}`
  ).toBe(AWAY);
  expect(await storedSpec(seed), "k-link-keys: Alt+Enter in the web link changes no text").toBe(
    before
  );

  // "See th|e other issue".
  await placeCaret(page, "before", "See");
  for (let step = 0; step < 6; step += 1) await page.keyboard.press("ArrowRight");
  await markDocument(page);
  await page.keyboard.press("Alt+Enter");
  await expectInApp(page, seed, "k-link-keys: Alt+Enter in the dispatch:// link");
  expect(
    await storedSpec(seed),
    "k-link-keys: Alt+Enter in the dispatch:// link changes no text"
  ).toBe(before);

  // The caret beside the key's title: one step on from the space before the key.
  await openLinkView(page, seed, "the editable spec");
  await placeCaret(page, "after", "The bare key");
  await page.keyboard.press("ArrowRight");
  await markDocument(page);
  await page.keyboard.press("Alt+Enter");
  await expectInApp(page, seed, "k-link-keys: Alt+Enter beside the key's title");
  expect(
    await storedSpec(seed),
    "k-link-keys: Alt+Enter beside the key's title changes no text"
  ).toBe(before);
});

/** One stop of the focus inside a view: the text a reader sees on it, and whether it draws
 *  anything at all (a box of some size holding visible text). */
interface TabStop {
  readonly text: string;
  readonly drawn: boolean;
}

/** Presses Tab until the focus has entered `scope` and left it again, at most `limit` times, and
 *  answers each stop inside it, in order. */
async function tabThrough(page: Page, scope: Locator, limit = 60): Promise<TabStop[]> {
  const stops: TabStop[] = [];
  for (let press = 0; press < limit; press += 1) {
    await page.keyboard.press("Tab");
    const stop = await scope.evaluate((root): TabStop | null => {
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !root.contains(active)) return null;
      const box = active.getBoundingClientRect();
      const text = active.innerText.trim();
      return { drawn: box.width > 0 && box.height > 0 && text !== "", text };
    });
    if (stop !== null) {
      stops.push(stop);
    } else if (stops.length > 0) {
      break;
    }
  }
  return stops;
}

// SLOW(>=2s): two views walked by Tab, and each of their three links opened by Enter.
test("k-link-keys: in the past version and the reading pane, Tab reaches each link, the key's title included, and Enter opens it", async ({
  context,
  page,
}) => {
  const seed = await seedLinkSpec();
  const titleText = `${seed.other.key} ${seed.other.title}`;
  for (const view of ["the past version", "the reading pane"] as const) {
    let links = await openLinkView(page, seed, view);
    if (view === "the past version") {
      await page.getByRole("button", { exact: true, name: "Version" }).focus();
    } else {
      await page.keyboard.press("l");
      await expect(paneScroller(page), "k-link-keys: l puts the focus in the pane").toBeFocused();
    }
    const stops = await tabThrough(page, links.scope);
    const texts = stops.map((stop) => stop.text);
    const order = ["a page elsewhere", "the other issue", titleText].map((text) =>
      texts.indexOf(text)
    );
    expect(
      order.every((index, at) => index !== -1 && (at === 0 || index > (order[at - 1] ?? -1))),
      `k-link-keys: in ${view}, Tab reaches the web link, the dispatch:// link and the key's title in turn: ${JSON.stringify(texts)}`
    ).toBe(true);
    expect(
      stops.filter((stop) => !stop.drawn),
      `k-link-keys: in ${view}, Tab stops on nothing that draws nothing`
    ).toEqual([]);

    await links.away.focus();
    expect(
      await openedAddress(context, () => page.keyboard.press("Enter")),
      `k-link-keys: in ${view}, Enter on the web link opens a new page at ${AWAY}`
    ).toBe(AWAY);
    await links.issue.focus();
    await markDocument(page);
    await page.keyboard.press("Enter");
    await expectInApp(page, seed, `k-link-keys: in ${view}, Enter on the dispatch:// link`);
    links = await openLinkView(page, seed, view);
    await links.key.focus();
    await markDocument(page);
    await page.keyboard.press("Enter");
    await expectInApp(page, seed, `k-link-keys: in ${view}, Enter on the key's title`);
  }
});

/** The issue the copied links name, and the question, comment and spec that hold them. */
interface CopySeed {
  readonly otherKey: string;
  readonly hostKey: string;
  readonly askId: string;
  readonly commentId: string;
}

/** The seeds of k-link-copy: a question whose text holds a link to another site, a `dispatch://`
 *  link to another issue, that issue's bare key and a web link to that issue's Dispatch page; a
 *  comment anchored in the spec of the question's issue that holds the same four; and that spec
 *  at version 2, whose version 1 holds the same four. */
async function seedLinkCopy(): Promise<CopySeed> {
  await createProject({ key: "CORE", name: "Core" });
  const other = await createIssue({ project: "CORE", title: "Ledger handover" });
  const fourLinks = (page: string) =>
    `Read [a page elsewhere](${AWAY}), see [the other issue](dispatch://${other.key}), the bare key ${other.key} and [the other issue's page](${page}).`;
  const host = await createIssue({
    project: "CORE",
    spec: `# Mirror replay\n\n${fourLinks(`${baseUrl}/issues/${other.key}`)}\n\nThe anchor of a review comment.\n`,
    title: "Mirror replay",
  });
  const comment = await createComment(host.key, {
    anchor: { artifact: "spec", quote: "The anchor of a review comment." },
    body: fourLinks(`${baseUrl}/issues/${other.key}`),
  });
  await editArtifact(
    host.primary_artifact_id,
    { ops: [{ after: "end", markdown: "A later line.", op: "insert" }] },
    asker
  );
  const ask = await createAsk(
    host.key,
    { question: `Which page? ${fourLinks(`${baseUrl}/issues/${other.key}`)}` },
    asker
  );
  return { askId: ask.id, commentId: comment.id, hostKey: host.key, otherKey: other.key };
}

/** Presses `y` with the focus on `link` and answers what the page put on the clipboard for it
 *  (`recordClipboard`), or undefined where it put nothing. */
async function copiedFrom(
  page: Page,
  link: Locator,
  copied: () => Promise<string[]>
): Promise<string | undefined> {
  await link.focus();
  await expect(link).toBeFocused();
  const count = (await copied()).length;
  await page.keyboard.press("y");
  await expect
    .poll(async () => (await copied()).length, { message: "k-link-copy: y copies something" })
    .toBeGreaterThan(count);
  return (await copied()).at(-1);
}

/** The four links of a rendered body: the web link, then the three a body draws as reference
 *  links to the other issue - the `dispatch://` link, the bare key and the link to its page. */
function bodyLinks(scope: Locator, seed: CopySeed): Array<[Locator, string, string]> {
  const reference = `dispatch://${seed.otherKey}`;
  const references = scope.locator(`a[data-dispatch-ref="${reference}"]`);
  return [
    [scope.getByRole("link", { exact: true, name: "a page elsewhere" }), "the web link", AWAY],
    [references.nth(0), "the dispatch:// link", reference],
    [references.nth(1), "the bare key's link", reference],
    [references.nth(2), "the link to the other issue's page", reference],
  ];
}

// SLOW(>=2s): the Inbox's reading pane walked by Tab, then the drawer opened over a project page.
test("k-link-copy: y on a focused link copies its address in the Inbox's reading pane and in the drawer, before the Inbox's own y", async ({
  page,
}) => {
  const copied = await recordClipboard(page);
  const seed = await seedLinkCopy();
  await page.setViewportSize(WIDE);
  await page.goto("/");
  const row = page.locator(`[data-inbox-row="${seed.askId}"]`);
  await row.locator("time").first().click();
  await expect(row).toHaveAttribute("data-inbox-selected", "true");
  await page.keyboard.press("l");
  await expect(paneScroller(page), "k-link-copy: l puts the focus in the pane").toBeFocused();
  for (const [link, what, address] of bodyLinks(paneScroller(page), seed)) {
    await tabTo(page, link, `k-link-copy: in the reading pane, Tab reaches ${what}`);
    expect(
      await copiedFrom(page, link, copied),
      `k-link-copy: in the reading pane, y on ${what} copies ${address}`
    ).toBe(address);
  }

  await page.goto("/projects/CORE");
  await expect(page.getByRole("button", { exact: true, name: "Board" })).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("i");
  await expect(drawer(page), "k-link-copy: i opens the drawer").toBeVisible();
  expect(
    await copiedFrom(
      page,
      drawer(page).getByRole("link", { exact: true, name: "a page elsewhere" }),
      copied
    ),
    "k-link-copy: in the drawer, y on the question's web link copies its URL"
  ).toBe(AWAY);
});

// SLOW(>=2s): the Conversation tab, version 1 of the spec and the margin's sheet, each opened.
test("k-link-copy: y on a focused link copies its address on the Conversation tab, in a past version and in the margin's sheet", async ({
  page,
}) => {
  const copied = await recordClipboard(page);
  const seed = await seedLinkCopy();
  const reference = `dispatch://${seed.otherKey}`;
  await page.setViewportSize(WIDE);

  await page.goto(`/issues/${seed.hostKey}/conversation`);
  const turn = page.locator(`[data-turn="comment:${seed.commentId}"]`);
  expect(
    await copiedFrom(
      page,
      turn.getByRole("link", { exact: true, name: "a page elsewhere" }),
      copied
    ),
    "k-link-copy: on the Conversation tab, y on the comment's web link copies its URL, not the comment's reference"
  ).toBe(AWAY);

  await page.goto(`/issues/${seed.hostKey}/artifacts/spec?v=1`);
  const version = page
    .getByRole("region", { name: "Document version 1" })
    .getByRole("textbox", { name: "Document editor" });
  const title = version.locator(`a.dispatch-reference[data-dispatch-href="${reference}"]`);
  await expect(title, "k-link-copy: version 1 draws the bare key's title").toBeVisible();
  for (const [link, what, address] of [
    [version.getByRole("link", { exact: true, name: "a page elsewhere" }), "the web link", AWAY],
    [
      version.getByRole("link", { exact: true, name: "the other issue" }),
      "the dispatch:// link",
      reference,
    ],
    [title, "the bare key's title", reference],
    [
      version.getByRole("link", { exact: true, name: "the other issue's page" }),
      "the link to the other issue's page",
      reference,
    ],
  ] as const) {
    expect(
      await copiedFrom(page, link, copied),
      `k-link-copy: in version 1, y on ${what} copies ${address}`
    ).toBe(address);
  }

  // The margin is a sheet below 1280 px, opened with its own control.
  await page.setViewportSize({ height: 768, width: 1024 });
  await page.goto(`/issues/${seed.hostKey}/spec`);
  await page.getByRole("button", { name: /Open review panel/ }).click();
  const sheet = page.getByTestId("margin-sheet");
  await expect(sheet, "k-link-copy: the margin's sheet opens").toHaveAttribute(
    "data-expanded",
    "true"
  );
  const card = sheet.locator(`[data-margin-item="${seed.commentId}"]`);
  expect(
    await copiedFrom(
      page,
      card.getByRole("link", { exact: true, name: "a page elsewhere" }),
      copied
    ),
    "k-link-copy: in the margin's sheet, y on the comment's web link copies its URL"
  ).toBe(AWAY);
});
