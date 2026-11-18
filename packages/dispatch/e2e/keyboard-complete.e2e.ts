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
import { setTimeout as sleep } from "node:timers/promises";

import { setLiveSessions } from "./agents";
import {
  answerAsk,
  baseUrl,
  createArtifactAsk,
  createAsk,
  createComment,
  createHandoff,
  createIssue,
  createIssueArtifact,
  createMessage,
  createProject,
  createProjectDocument,
  editArtifact,
  editAsk,
  getInbox,
  getIssueArtifactText,
  listIssueAsks,
  markThreadRead,
  patchIssue,
  putRepoProject,
  requestApproval,
  reviewArtifact,
  snoozeAsk,
  withOpenedAsk,
} from "./api";
import { recordClipboard } from "./clipboard";
import { documentEditor, openSpec, placeCaret } from "./editor";
import { drawer } from "./inbox-drawer";
import { inboxPane, inboxRow, paneScroller, selectedRow, selectRow } from "./inbox-pane";
import {
  DOCUMENT_FOOT,
  headingOffset,
  inboxAt,
  restingColumn,
  SECTION_HEADING,
  seedApprovalWithMarks,
  seedQuestionColumn,
  waitOutPlacement,
} from "./inbox-pane-scroll";
import {
  agentRowId,
  replyRowId,
  seedAgentThread,
  seedCommentThread,
  THREAD_SESSION,
} from "./inbox-threads";
import {
  answerWrites,
  asker,
  contrastOf,
  dismissThread,
  dismissWrites,
  handOnControl,
  handOnWrites,
  holdWrites,
  issueWrites,
  markDocument,
  newPageAt,
  OTHER_SITE,
  reloaded,
  returnMornings,
  reviewWrites,
  type SeededApproval,
  seedApprovalAtVersionTwo,
  seedTallAsks,
  snoozedUntil,
  snoozeWrites,
  unsnoozeWrites,
  versionReviewWrites,
} from "./keyboard-complete";
import { openIssue } from "./keyboard-issue";
import type { KeyedWrite } from "./keyed-writes";
import { type BrowserContext, expect, type Locator, type Page, test } from "./test";
import { asUser } from "./users";

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
  // A Tab moves the focus inside its own key press, so each read after one is settled.
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

/** Reads the contrast of the shared list's trigger `control` and of each choice of its list,
 *  failing softly for each pair under 4.5:1, with the theme, the control and the choice named.
 *  The trigger is read on the element that draws its text, and its choices in the list a click
 *  on it opens, once that list holds `fromRead`, a choice only the list's own read brings. */
async function readChoiceContrast(control: Locator, what: string, fromRead: string): Promise<void> {
  const measure = async (target: Locator, name: string) => {
    const reading = await contrastOf(target);
    expect
      .soft(
        reading.ratio,
        `k-menu-contrast: ${what}, ${name}: ${reading.color} on ${reading.background}`
      )
      .toBeGreaterThanOrEqual(4.5);
  };
  await measure(control.locator("xpath=.//*[text()[normalize-space()]]").first(), "the control");
  await control.click();
  const list = control.locator("xpath=id(./@aria-controls)");
  const options = list.getByRole("option");
  await expect(
    list.getByRole("option", { exact: true, name: fromRead }),
    `k-menu-contrast: ${what}: a click opens its list, and its read brings ${fromRead}`
  ).toBeVisible();
  // The wait above saw a choice only the list's read brings, so every choice is drawn.
  const count = await options.count();
  for (let index = 0; index < count; index += 1) {
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
      `${theme}, the assignee`,
      "bob"
    );
    await readChoiceContrast(
      page.getByLabel(`Repository of ${issueKey}`, { exact: true }),
      `${theme}, the repository`,
      "acme/gadgets"
    );
    const inbox = await openInboxAt(page, "card mode", 1);
    await readChoiceContrast(
      handOnControl(rowOf(inbox, askId), askId),
      `${theme}, the hand-on control`,
      "bob"
    );
  }
});

// ---------------------------------------------------------------------------
// k-phone
// ---------------------------------------------------------------------------

/** Holds each of `choices` to a tap's height, 44 px or more, naming `what` and the choice. */
async function expectTapHeight(choices: Locator, what: string): Promise<void> {
  await expect(choices.first(), `k-phone: ${what} has choices`).toBeVisible();
  // A menu draws all its choices in the render that drew the first, waited for above.
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
  // The count above waited for both rows, so their ids are read once they are drawn.
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
    // A Tab moves the focus inside its own key press, so this reads where it settled.
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

// ---------------------------------------------------------------------------
// k-list-and-document (R3): the open document's own keys, pressed from the list. Space and
// Shift+Space page it, `g g` and Shift+G take it to its ends, and the selection and the focus
// stay where they are; on a control that Space presses itself the key is the control's.
// ---------------------------------------------------------------------------

/** Where the reading column stands, how tall it is, and how tall the document drawn in it is. */
interface ColumnAt {
  readonly height: number;
  readonly scrollHeight: number;
  readonly top: number;
}

function columnAt(page: Page): Promise<ColumnAt> {
  return paneScroller(page).evaluate((column) => ({
    height: column.clientHeight,
    scrollHeight: column.scrollHeight,
    top: column.scrollTop,
  }));
}

/** What the document's keys leave alone, in words: the row the column shows, and the element
 *  that holds the focus - by its row, as the page body, or by its tag and name. */
function handOf(page: Page): Promise<string> {
  return page.evaluate(() => {
    const selected = document
      .querySelector('[data-inbox-selected="true"]')
      ?.getAttribute("data-inbox-row");
    const active = document.activeElement;
    const focus =
      active === null || active === document.body
        ? "the body"
        : (active.getAttribute("data-inbox-row") ??
          `${active.tagName} ${active.getAttribute("aria-label") ?? ""}`);
    return `${selected ?? "nothing"} selected, the focus on ${focus}`;
  });
}

/** Presses `keys` one after the other and reads that the column moved as `moved` says of where
 *  it stood and where it stands, and that the selection and the focus are what they were. */
async function pressInDocument(
  page: Page,
  keys: readonly string[],
  step: string,
  moved: (before: ColumnAt, after: ColumnAt) => boolean
): Promise<void> {
  const before = await columnAt(page);
  const hand = await handOf(page);
  for (const key of keys) {
    await page.keyboard.press(key);
  }
  await expect
    .poll(async () => moved(before, await columnAt(page)), {
      message: `k-list-and-document: ${step}`,
    })
    .toBe(true);
  expect(
    await handOf(page),
    `k-list-and-document: ${step}, and the selection and the focus stay where they were`
  ).toBe(hand);
}

/** The tall approval requests, each of a spec at least four times the column's height, and a
 *  question its asker reworded, whose card draws the `<summary>` of its earlier wording. */
async function seedListAndDocument(): Promise<{
  readonly reworded: string;
  readonly tall: readonly SeededApproval[];
}> {
  await createProject({ key: "CORE", name: "Core" });
  const tall = await seedTallAsks("CORE");
  const issue = await createIssue({ force: true, project: "CORE", title: "Lease audit" });
  const ask = await createAsk(issue.key, { question: "Which lease does the audit read?" }, asker);
  await editAsk(ask.id, { question: "Which lease window does the audit read?" }, asker);
  return { reworded: ask.id, tall };
}

/** Waits for the approval request the column shows to draw its spec whole: the last of its
 *  numbered paragraphs (`seedTallAsks`). */
async function tallDrawn(page: Page, title: string): Promise<void> {
  await expect(
    paneScroller(page).getByText(`${title} note 160:`),
    `k-list-and-document: the column draws the whole of the ${title} spec`
  ).toHaveCount(1);
  const drawn = await columnAt(page);
  expect(
    drawn.scrollHeight,
    `k-list-and-document: the ${title} spec is at least four times the column's height`
  ).toBeGreaterThanOrEqual(4 * drawn.height);
}

/** Holds every read of `page` that one of `reads` matches until the answer is called, then
 *  serves them all. A read the page gave up on while it was held - a query the column cancelled
 *  as it mounted again - is already answered by then, and the hold lets it go unanswered rather
 *  than failing the row on a read nobody waits for. */
async function holdReads(page: Page, reads: readonly string[]): Promise<() => Promise<void>> {
  const held = Promise.withResolvers<void>();
  for (const read of reads) {
    await page.route(read, async (route) => {
      await held.promise;
      await route.continue().catch(() => undefined);
    });
  }
  return async () => {
    held.resolve();
    for (const read of reads) {
      await page.unroute(read);
    }
  };
}

// SLOW(>=2s): three tall specs drawn in the column, then a document whose reads are held and
// served, and the placement's bound waited out.
test("k-list-and-document: Space, Shift+Space, g g and Shift+G scroll the open document from the list, and leave the selection, the focus and a control's own Space alone", async ({
  page,
}) => {
  const copied = await recordClipboard(page);
  const seed = await seedListAndDocument();
  const [first, second] = seed.tall;
  if (first === undefined || second === undefined) throw new Error("seedTallAsks seeded two");
  const pagedDown = (before: ColumnAt, after: ColumnAt) =>
    after.top - before.top >= before.height / 2 && after.top - before.top <= before.height;
  const pagedUp = (before: ColumnAt, after: ColumnAt) =>
    before.top - after.top >= before.height / 2 && before.top - after.top <= before.height;
  await page.setViewportSize(WIDE);
  await page.goto("/");

  // A click on the first row selects it and leaves the focus on it.
  await selectRow(page, first.askId);
  const firstRow = page.locator(`[data-inbox-row="${first.askId}"]`);
  await expect(
    firstRow,
    "k-list-and-document: the click leaves the focus on the row"
  ).toBeFocused();
  await tallDrawn(page, "Ledger handover");
  await pressInDocument(
    page,
    ["Space"],
    "Space scrolls the column down by half to all of its height",
    pagedDown
  );
  await pressInDocument(
    page,
    ["Shift+Space"],
    "Shift+Space scrolls it back up by half to all of its height",
    pagedUp
  );
  await pressInDocument(
    page,
    ["Shift+G"],
    "Shift+G puts the column at its end",
    (_before, after) => Math.abs(after.top + after.height - after.scrollHeight) <= 1
  );
  await pressInDocument(
    page,
    ["g", "g"],
    "g then g puts the column at its top",
    (_before, after) => after.top === 0
  );

  // With the focus on the page body, as on the row.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  expect(
    await page.evaluate(() => document.activeElement === document.body),
    "k-list-and-document: the row let go, so the body has the focus"
  ).toBe(true);
  await pressInDocument(
    page,
    ["Space"],
    "with the focus on the body, Space scrolls the column down by half to all of its height",
    pagedDown
  );

  await page.keyboard.press("j");
  await expect(selectedRow(page), "k-list-and-document: j selects the second ask").toHaveAttribute(
    "data-inbox-row",
    second.askId
  );
  await tallDrawn(page, "Mirror replay");

  // On the pane head's Copy reference, Space presses the button and scrolls nothing.
  const copy = inboxPane(page)
    .locator("header")
    .first()
    .getByRole("button", { name: /^Copy reference/ });
  await copy.focus();
  const copiedBefore = (await copied()).length;
  const beforeCopy = await columnAt(page);
  await page.keyboard.press("Space");
  await expect
    .poll(async () => (await copied()).length, {
      message: "k-list-and-document: Space on the pane head's Copy reference presses it",
    })
    .toBe(copiedBefore + 1);
  expect((await copied()).at(-1), "k-list-and-document: it copies the second ask").toContain(
    second.askId
  );
  expect(
    (await columnAt(page)).top,
    "k-list-and-document: Space on Copy reference moves no scrollTop"
  ).toBe(beforeCopy.top);

  // On a focused `<summary>` of the pane, Space opens or closes it and scrolls nothing.
  await selectRow(page, seed.reworded);
  const summary = inboxPane(page).locator("summary", { hasText: "Show 1 previous version" });
  await expect(summary, "k-list-and-document: the reworded question draws a summary").toBeVisible();
  await summary.focus();
  const opened = () =>
    summary.evaluate((node) => (node.parentElement as HTMLDetailsElement | null)?.open === true);
  const wasOpen = await opened();
  const beforeSummary = await columnAt(page);
  await page.keyboard.press("Space");
  await expect
    .poll(opened, {
      message: "k-list-and-document: Space on a focused summary of the pane opens or closes it",
    })
    .toBe(!wasOpen);
  expect(
    (await columnAt(page)).top,
    "k-list-and-document: Space on the summary moves no scrollTop"
  ).toBe(beforeSummary.top);

  // An ask whose automatic placement is still running: its document's reads are held, so the
  // column places the question's section, far down the document, as soon as they return.
  const placed = await seedQuestionColumn("heading");
  const release = await holdReads(page, [
    `**/api/v1/artifacts/${placed.artifactId}`,
    `**/api/v1/artifacts/${placed.artifactId}/text`,
  ]);
  await page.goto(inboxAt(placed.item));
  await expect(selectedRow(page), "k-list-and-document: the held ask is selected").toHaveAttribute(
    "data-inbox-row",
    placed.item
  );
  await expect(paneScroller(page), "k-list-and-document: its column is drawn").toBeVisible();
  await page.keyboard.press("Space");
  const spaced = (await columnAt(page)).top;
  await release();
  await expect(
    paneScroller(page).getByText(DOCUMENT_FOOT),
    "k-list-and-document: the held document is drawn once its reads return"
  ).toHaveCount(1);
  await waitOutPlacement(page);
  expect(
    Math.abs((await restingColumn(page)).top - spaced),
    "k-list-and-document: Space pressed while the placement runs leaves the column where the Space put it after the reads return"
  ).toBeLessThanOrEqual(1);
  expect(
    await headingOffset(page, SECTION_HEADING),
    "k-list-and-document: the section the placement would have opened at is still below the column's top"
  ).toBeGreaterThan(8);
});

// ---------------------------------------------------------------------------
// k-other-key (R17): the digit after a question's last real option is Other's, each option row of
// the item in hand shows its digit, and a pick of Other puts the caret in the answer box on every
// surface that draws a question.
// ---------------------------------------------------------------------------

/** The two options of the questions below that need no more than two. */
const SHIP_OR_HOLD = [{ label: "Ship" }, { label: "Hold" }];

/** The decision block of k-other-key's spec. */
const LEASE_BLOCK = "lease-window";

/** A decision block `id` asking `question` with `options`, as a spec writes one. */
function decisionBlock(id: string, question: string, options: readonly string[]): string {
  return [
    `:::ask{#${id} urgency="med" multiple="false" state="open"}`,
    question,
    "",
    ...options.map((option) => `- ${option}`),
    ":::",
  ].join("\n");
}

/** A spec holding a decision block with two options, and the line an ask is anchored to. */
const DECISION_SPEC = [
  "# Lease handover",
  "",
  "The mirror hands the lease back when the replay is done.",
  "",
  decisionBlock(LEASE_BLOCK, "Which window does the lease cover?", [
    "The last hour",
    "The last day",
  ]),
  "",
].join("\n");

/** The decision block of nine options. The server takes at most eight options on an ask it is
 *  sent (`maxAskOptions`, `packages/envoy/internal/dispatch/api/asks.go`), so the question of
 *  nine is one a spec writes, which the server indexes into an ask. */
const NINE_BLOCK = "replay-window";

/** The id of the ask the server indexed the decision block `blockId` of `issueKey`'s spec into,
 *  once it has. */
async function blockAsk(issueKey: string, blockId: string): Promise<string> {
  let askId: string | undefined;
  await expect
    .poll(
      async () => {
        askId = (await listIssueAsks(issueKey)).find((ask) => ask.block_id === blockId)?.id;
        return askId !== undefined;
      },
      { message: `the decision block ${blockId} is indexed into an ask` }
    )
    .toBe(true);
  if (askId === undefined) throw new Error(`the decision block ${blockId} was never indexed`);
  return askId;
}

interface OtherSeed {
  readonly answeredId: string;
  readonly anchoredId: string;
  readonly decisionIssueKey: string;
  readonly nineId: string;
  readonly threeId: string;
}

/** A question of three options and one of nine, both waiting on alice; a question alice
 *  answered with its first option; and an issue whose spec holds a decision block with two
 *  options and whose conversation holds an ask anchored to that spec, so its margin draws it.
 *  Each issue is made past the server's duplicate check (`force`): their titles share words. */
async function seedOtherKey(): Promise<OtherSeed> {
  await createProject({ key: "CORE", name: "Core" });
  const threeIssue = await createIssue({ force: true, project: "CORE", title: "Ledger rollout" });
  const three = await createAsk(
    threeIssue.key,
    {
      options: [{ label: "Ship" }, { label: "Hold" }, { label: "Drop" }],
      question: "Which way does the ledger roll out?",
    },
    asker
  );
  const nineIssue = await createIssue({
    force: true,
    project: "CORE",
    spec: `# Mirror replay\n\n${decisionBlock(
      NINE_BLOCK,
      "Which window does the mirror replay?",
      Array.from({ length: 9 }, (_, index) => `Window ${index + 1}`)
    )}\n`,
    title: "Mirror replay",
  });
  const answeredIssue = await createIssue({ force: true, project: "CORE", title: "Clock audit" });
  const answered = await createAsk(
    answeredIssue.key,
    { options: SHIP_OR_HOLD, question: "Ship the clock audit?" },
    asker
  );
  await answerAsk(answered.id, { expected_edited_at: null, selected: ["Ship"] });
  const decided = await createIssue({
    force: true,
    project: "CORE",
    spec: DECISION_SPEC,
    title: "Lease handover",
  });
  const anchored = await createAsk(
    decided.key,
    {
      anchor: { artifact: "spec", quote: "hands the lease back" },
      options: SHIP_OR_HOLD,
      question: "Hand the lease back at once?",
    },
    asker
  );
  return {
    answeredId: answered.id,
    anchoredId: anchored.id,
    decisionIssueKey: decided.key,
    nineId: await blockAsk(nineIssue.key, NINE_BLOCK),
    threeId: three.id,
  };
}

/** Clicks the Other of the question drawn in `scope` and reads that the caret is in its answer
 *  box, as the step `step` says. */
async function clickOther(scope: Locator, step: string): Promise<void> {
  await scope.getByRole("radio", { name: "Other" }).click();
  await expect(scope.locator("[data-ask-answer]"), `k-other-key: ${step}`).toBeFocused();
}

// SLOW(>=2s): two answers in the reading pane, then card mode, the Spec tab with its decision
// block and margin, the Conversation tab and the Answers page, each opened.
test("k-other-key: the digit after a question's last option picks Other and puts the caret in its answer box, and a click on Other does on every surface", async ({
  page,
}) => {
  const seed = await seedOtherKey();
  await page.setViewportSize(WIDE);
  await page.goto("/");
  const pane = inboxPane(page);

  // The question of three options, from its row: each option row shows its digit, Other's 4
  // included, and 4 is Other.
  await selectRow(page, seed.threeId);
  const three = pane.getByTestId(`ask-${seed.threeId}`);
  await expect(three.getByRole("radio", { name: "Other" })).toBeVisible();
  await expect(
    three.locator("label:has(input[type=radio]) kbd"),
    "k-other-key: each of the first question's four option rows, Other's included, shows its digit, 1 to 4"
  ).toHaveText(["1", "2", "3", "4"]);
  const answers = answerWrites(page, seed.threeId);
  await page.keyboard.press("4");
  await expect(
    three.getByRole("radio", { name: "Other" }),
    "k-other-key: 4 checks Other on the question with three options"
  ).toBeChecked();
  await expect(
    three.locator("[data-ask-answer]"),
    "k-other-key: after 4 the caret is in the answer box"
  ).toBeFocused();
  await page.keyboard.type("my own words");
  await page.keyboard.press("Control+Enter");
  await expect
    .poll(() => answers.length, { message: "k-other-key: Ctrl+Enter sends one answer" })
    .toBe(1);
  expect(
    answers[0]?.input,
    "k-other-key: the answer is the words alone, with no option"
  ).toMatchObject({ selected: [], text: "my own words" });

  // The question of nine options, a decision block the column draws in its document: Other is
  // the tenth, `0`, and its pick opens the block card's note box with the caret in it.
  await selectRow(page, seed.nineId);
  const nine = pane.getByTestId(`ask-${seed.nineId}`);
  await expect(nine.getByRole("radio", { name: "Other" })).toBeVisible();
  await page.keyboard.press("0");
  await expect(
    nine.getByRole("radio", { name: "Other" }),
    "k-other-key: 0 checks Other on the question with nine options"
  ).toBeChecked();
  await expect(
    nine.locator("[data-ask-answer]"),
    "k-other-key: after 0 the caret is in the answer box"
  ).toBeFocused();

  // A click on Other, on every surface that draws a question.
  await page.setViewportSize(NARROW);
  await page.goto("/");
  await clickOther(
    page.getByTestId(`ask-${seed.nineId}`),
    "in card mode, a click on Other puts the caret in the answer box"
  );

  await page.setViewportSize(WIDE);
  await openSpec(page, seed.decisionIssueKey, "hands the lease back");
  await clickOther(
    documentEditor(page).locator(`[data-dispatch-ask-block="${LEASE_BLOCK}"]`),
    "on the decision block of the Spec tab, a click on Other puts the caret in the answer box"
  );

  await page.goto(`/issues/${seed.decisionIssueKey}/conversation`);
  await clickOther(
    page.locator(`[data-turn="ask:${seed.anchoredId}"]`),
    "on the ask of the Conversation tab, a click on Other puts the caret in the answer box"
  );

  await openSpec(page, seed.decisionIssueKey, "hands the lease back");
  await clickOther(
    page.locator(`[data-margin-item="${seed.anchoredId}"]`),
    "on that ask in the margin of the Spec tab, a click on Other puts the caret in the answer box"
  );

  // The Answers page, where a change of an answer given with a real option begins.
  await page.goto("/answers");
  await page
    .locator(`[data-answer-row="${seed.answeredId}"]`)
    .first()
    .getByRole("button", { name: "Change answer" })
    .click();
  const changing = page.locator(`[data-answer-card="${seed.answeredId}"]`);
  await expect(changing.getByRole("radio", { name: "Ship" })).toBeChecked();
  await clickOther(
    changing,
    "on the Answers page, a click on Other where a change of the answer begins puts the caret in the answer box"
  );
});

// ---------------------------------------------------------------------------
// k-answer-box (R18): Enter puts the caret in the answer box of the item in hand from its row,
// the page body, the column after `l` and an option of its card, and `r` in the reply box of the
// thread in hand.
// ---------------------------------------------------------------------------

/** The ask of a decision block alice answered and a session replied under, which the Inbox lists
 *  as a Reply row; its block, in the column, folds the reply box behind "Write a reply". */
async function seedDecisionThread(): Promise<string> {
  const issue = await createIssue({
    force: true,
    project: "CORE",
    spec: DECISION_SPEC,
    title: "Lease handover",
  });
  const askId = await blockAsk(issue.key, LEASE_BLOCK);
  await answerAsk(askId, { expected_edited_at: null, selected: ["The last hour"] });
  await createComment(issue.key, { ask_id: askId, body: "The hour holds for now." }, asker);
  return askId;
}

// SLOW(>=2s): four starts in the reading pane, card mode, then two threads opened in the pane.
test("k-answer-box: Enter puts the caret in the answer box from the row, the body, the column and an option, and r puts it in a thread's reply box", async ({
  page,
}) => {
  const copied = await recordClipboard(page);
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const asks: string[] = [];
  for (const title of ["Ledger rollout", "Mirror replay", "Clock audit"]) {
    const issue = await createIssue({ force: true, project: "CORE", title });
    asks.push(
      (await createAsk(issue.key, { options: SHIP_OR_HOLD, question: `Ship the ${title}?` }, asker))
        .id
    );
  }
  const [firstAsk, secondAsk, thirdAsk] = asks;
  if (firstAsk === undefined || secondAsk === undefined || thirdAsk === undefined) {
    throw new Error("three asks were seeded");
  }
  const threadIssue = await createIssue({ force: true, project: "CORE", title: "Replay budget" });
  const thread = await seedCommentThread(
    threadIssue.key,
    "How far back does a replay go?",
    "One hour, then it gives up."
  );
  const decisionThread = await seedDecisionThread();
  await page.setViewportSize(WIDE);
  await page.goto("/");
  const pane = inboxPane(page);
  const box = pane.getByTestId(`ask-${firstAsk}`).locator("[data-ask-answer]");
  await selectRow(page, firstAsk);
  const row = page.locator(`[data-inbox-row="${firstAsk}"]`);
  await expect(box).toBeVisible();

  // From the row.
  await row.focus();
  await page.keyboard.press("Enter");
  await expect(
    box,
    "k-answer-box: Enter from the row puts the caret in the answer box"
  ).toBeFocused();
  // From the page body.
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  await page.keyboard.press("Enter");
  await expect(
    box,
    "k-answer-box: Enter from the page body puts the caret in the answer box"
  ).toBeFocused();
  // From the document column, after `l`.
  await row.focus();
  await page.keyboard.press("l");
  await expect(paneScroller(page), "k-answer-box: l puts the focus in the column").toBeFocused();
  await page.keyboard.press("Enter");
  await expect(
    box,
    "k-answer-box: Enter from the document column puts the caret in the answer box"
  ).toBeFocused();
  // From an option of the card, after a click on it.
  const ship = pane.getByTestId(`ask-${firstAsk}`).getByRole("radio", { name: "Ship" });
  await ship.click();
  await expect(ship).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(
    box,
    "k-answer-box: Enter after a click on an option puts the caret in the answer box"
  ).toBeFocused();

  // On the pane head's Copy reference, Enter presses the button and the caret stays out.
  const copy = pane
    .locator("header")
    .first()
    .getByRole("button", { name: /^Copy reference/ });
  await copy.focus();
  const copiedBefore = (await copied()).length;
  await page.keyboard.press("Enter");
  await expect
    .poll(async () => (await copied()).length, {
      message: "k-answer-box: Enter on the pane head's Copy reference presses it",
    })
    .toBe(copiedBefore + 1);
  await expect(copy, "k-answer-box: the focus stays on Copy reference").toBeFocused();
  await expect(box, "k-answer-box: the caret is not in the answer box").not.toBeFocused();

  // The first question, answered with 1, Enter and Ctrl+Enter: the second question's row has the
  // focus (R8) - it is the item after the first question in the list, or the item before it
  // where the first is the last - and Enter from there puts the caret in its answer box.
  const listed = await page
    .locator("[data-inbox-row]")
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute("data-inbox-row") ?? ""));
  const answeredAt = listed.indexOf(firstAsk);
  expect(
    listed[answeredAt + 1] ?? listed[answeredAt - 1],
    "k-answer-box: the second question is the item after the first, or the one before it"
  ).toBe(secondAsk);
  const answers = answerWrites(page, firstAsk);
  await row.focus();
  await page.keyboard.press("1");
  await page.keyboard.press("Enter");
  await expect(box, "k-answer-box: 1 then Enter puts the caret in the answer box").toBeFocused();
  await page.keyboard.press("Control+Enter");
  await expect(
    page.locator(`[data-inbox-row="${secondAsk}"]`),
    "k-answer-box: after the answer the second question's row has the focus"
  ).toBeFocused();
  expect(answers, "k-answer-box: Ctrl+Enter sends one answer").toHaveLength(1);
  const secondBox = pane.getByTestId(`ask-${secondAsk}`).locator("[data-ask-answer]");
  await expect(secondBox, "k-answer-box: the column shows the second question").toBeVisible();
  await page.keyboard.press("Enter");
  await expect(
    secondBox,
    "k-answer-box: Enter from the second question's row puts the caret in its answer box"
  ).toBeFocused();

  // In card mode, after a click on an option of the third card.
  await page.setViewportSize(NARROW);
  await page.goto("/");
  const third = page.getByTestId(`ask-${thirdAsk}`);
  await third.getByRole("radio", { name: "Ship" }).click();
  await page.keyboard.press("Enter");
  await expect(
    third.locator("[data-ask-answer]"),
    "k-answer-box: in card mode, Enter after a click on an option of the third card puts the caret in its answer box"
  ).toBeFocused();

  // `r` on the reply thread's row puts the caret in the thread's reply box.
  await page.setViewportSize(WIDE);
  await page.goto("/");
  await selectRow(page, replyRowId(thread));
  await expect(pane.locator("[data-inbox-thread-body] textarea").first()).toBeVisible();
  await page.keyboard.press("r");
  expect(
    await page.evaluate(
      () =>
        document.activeElement instanceof HTMLTextAreaElement &&
        document.activeElement.closest('[data-testid="inbox-pane"] [data-inbox-thread-body]') !==
          null
    ),
    "k-answer-box: r on the reply thread's row puts the caret in the thread's reply box"
  ).toBe(true);

  // On the thread of a decision block, whose block folds the box behind "Write a reply", `r`
  // presses that first.
  await selectRow(page, replyRowId(decisionThread));
  const writeReply = pane.getByRole("button", { name: "Write a reply" });
  await expect(writeReply).toHaveAttribute("aria-expanded", "false");
  await page.keyboard.press("r");
  await expect(
    writeReply,
    "k-answer-box: r opens the decision block's Write a reply"
  ).toHaveAttribute("aria-expanded", "true");
  expect(
    await page.evaluate(
      () =>
        document.activeElement instanceof HTMLTextAreaElement &&
        document.activeElement.closest('[data-testid="inbox-pane"]') !== null
    ),
    "k-answer-box: r on a decision block's thread puts the caret in the reply box it opens"
  ).toBe(true);
});

// ---------------------------------------------------------------------------
// k-new-tab (R20), the Inbox's part: Shift+O opens what `o` opens - the item's owner link, which
// `o` follows - in a new page of the browser, beside the reading pane, in card mode and in the
// drawer, and the first page keeps its address.
// ---------------------------------------------------------------------------

/** Presses Shift+O on `page` and reads that one new page of `context` opened at `address`, and
 *  that `page` kept its own address. */
async function shiftOOpens(
  context: BrowserContext,
  page: Page,
  address: string,
  place: string
): Promise<void> {
  const before = page.url();
  const pages = context.pages().length;
  const opened = await test.step(`k-new-tab: ${place}, Shift+O opens a new page`, () =>
    newPageAt(context, () => page.keyboard.press("Shift+O")));
  expect(opened.url(), `k-new-tab: ${place}, the new page is at the address o opens`).toBe(address);
  expect(context.pages().length, `k-new-tab: ${place}, Shift+O opens one new page`).toBe(pages + 1);
  expect(page.url(), `k-new-tab: ${place}, the first page keeps its address`).toBe(before);
  await opened.close();
}

/** The absolute address of the owner link in `scope`, the link `o` follows. */
function ownerAddress(scope: Locator): Promise<string> {
  return scope
    .locator("[data-inbox-owner]")
    .first()
    .evaluate((link) => (link as HTMLAnchorElement).href);
}

// SLOW(>=2s): the Inbox opened three times, each with a page opened from it.
test("k-new-tab: Shift+O on the Inbox opens the item o opens in a new page, beside the reading pane, in card mode and in the drawer", async ({
  context,
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Ledger rollout" });
  await createAsk(issue.key, { question: "Ship the ledger rollout?" }, asker);
  const runbook = await createProjectDocument(
    "CORE",
    { content: "# Runbook\n\nRestart, reseed, reopen.\n", name: "Runbook" },
    asker
  );
  await createArtifactAsk(
    runbook.artifact.id,
    { question: "Keep the runbook in the repository?" },
    asker
  );

  // Beside the reading pane, `j` selects the second item, and the column's head holds its link.
  await page.setViewportSize(WIDE);
  await page.goto("/");
  const rows = page.locator("[data-inbox-row]");
  await expect(rows).toHaveCount(2);
  const order = await rows.evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-inbox-row") ?? "")
  );
  await expect(selectedRow(page)).toHaveAttribute("data-inbox-row", order[0] ?? "");
  await page.locator("body").focus();
  await page.keyboard.press("j");
  await expect(selectedRow(page), "k-new-tab: j selects the second item").toHaveAttribute(
    "data-inbox-row",
    order[1] ?? ""
  );
  await expect(inboxPane(page).getByTestId(`ask-${order[1]}`)).toBeVisible();
  await shiftOOpens(
    context,
    page,
    await ownerAddress(inboxPane(page).locator("header").first()),
    "beside the reading pane"
  );

  // In card mode, `j` focuses the first row.
  await page.setViewportSize(NARROW);
  await page.goto("/");
  await expect(rows).toHaveCount(2);
  await page.locator("body").focus();
  await page.keyboard.press("j");
  const focused = page.locator("[data-inbox-row]:focus");
  await expect(focused, "k-new-tab: in card mode, j focuses a row").toHaveCount(1);
  await shiftOOpens(context, page, await ownerAddress(focused), "in card mode");

  // In the drawer over a project page.
  await page.setViewportSize(WIDE);
  await page.goto("/projects/CORE");
  await expect(page.getByRole("button", { exact: true, name: "Board" })).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("i");
  await expect(drawer(page), "k-new-tab: i opens the drawer").toBeVisible();
  await expect(drawer(page).locator("[data-inbox-row]")).toHaveCount(2);
  await page.keyboard.press("j");
  const inDrawer = drawer(page).locator("[data-inbox-row]:focus");
  await expect(inDrawer, "k-new-tab: in the drawer, j focuses a row").toHaveCount(1);
  await shiftOOpens(context, page, await ownerAddress(inDrawer), "in the drawer");
  await expect(drawer(page), "k-new-tab: the drawer stays open").toBeVisible();
});

// ---------------------------------------------------------------------------
// k-approval-keys (R7), the Inbox's part: on the approval request in hand `a` approves the version
// it shows with one press and Shift+R opens Request changes with the focus in its reason, beside
// the reading pane, in card mode and in the drawer; before the document's read returns, and on a
// question, both send nothing.
// ---------------------------------------------------------------------------

/** How late the reading pane's read of the first request's document is served, in milliseconds. */
const LATE_READ_MS = 1000;

/** The reason the rows' Request changes gives. */
const ROLLBACK_REASON = "Name the rollback path.";

/** Every review a page sends of one approval request, on each route a review can take: the
 *  versioned route `a`, Shift+R and Approve post to, the route with no version the merge base's
 *  Approve posts to, and the ask's answer, which a card sends. */
interface ReviewsSent {
  readonly answers: KeyedWrite<unknown>[];
  readonly unversioned: KeyedWrite<unknown>[];
  readonly versioned: KeyedWrite<unknown>[];
}

function watchReviews(page: Page, approvals: readonly SeededApproval[]): ReviewsSent[] {
  return approvals.map((approval) => ({
    answers: answerWrites(page, approval.askId),
    unversioned: reviewWrites(page, approval.artifactId),
    versioned: versionReviewWrites(page, approval.artifactId, approval.version),
  }));
}

/** How many reviews the watched lists hold together, on every route. */
function reviewCount(reviews: readonly ReviewsSent[]): number {
  return countOf(reviews.flatMap((sent) => [sent.answers, sent.unversioned, sent.versioned]));
}

/** The lines of the `?` help's Inbox section. */
function inboxKeyHelp(page: Page): Locator {
  return page
    .getByRole("dialog", { name: "Keyboard shortcuts" })
    .getByRole("region", { exact: true, name: "Inbox" })
    .getByRole("listitem");
}

// SLOW(>=2s): three approval requests seeded at version 2, one document's read held for a second,
// two reviews read back, and the help opened once.
test("k-approval-keys: beside the reading pane a approves the version the request shows once its document is read, Shift+R opens Request changes, and on a question both send nothing", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const approvals: SeededApproval[] = [];
  for (const title of ["Ledger handover", "Mirror replay", "Clock audit"]) {
    approvals.push(await seedApprovalAtVersionTwo("CORE", title));
  }
  const [first, second] = approvals;
  if (first === undefined || second === undefined) throw new Error("three requests were seeded");
  const question = await createAsk(
    first.issueKey,
    { question: "Which ledger does the handover read?" },
    asker
  );
  const reviews = watchReviews(page, approvals);
  const questionAnswers = answerWrites(page, question.id);
  const sent = () => reviewCount(reviews) + questionAnswers.length;
  const step = (text: string) => `k-approval-keys: the reading pane: ${text}`;
  const head = inboxPane(page).locator("header").first();
  // The Approve that takes a press: the head draws Approve from its first draw and holds it
  // (`aria-disabled`) until the document's read returns.
  const approve = head.getByRole("button", { disabled: false, exact: true, name: "Approve" });
  const dialog = page.getByRole("dialog", { name: "Request changes" });

  // The first request's document is read late, and `a` is pressed while that read is out.
  const documentPath = `/api/v1/artifacts/${first.artifactId}`;
  const documentRead = `**${documentPath}`;
  await page.route(documentRead, async (route) => {
    // Served late on purpose: a key pressed while the read is out must find nothing to press.
    await sleep(LATE_READ_MS);
    await route.continue().catch(() => undefined);
  });
  const requested = page.waitForRequest(
    (request) => new URL(request.url()).pathname === documentPath
  );
  await page.setViewportSize(WIDE);
  await page.goto(inboxAt(first.askId));
  await expect(selectedRow(page), step("the first request is selected")).toHaveAttribute(
    "data-inbox-row",
    first.askId
  );
  const firstRow = page.locator(`[data-inbox-row="${first.askId}"]`);
  await firstRow.focus();
  await requested;
  await page.keyboard.press("a");
  await expect(
    approve,
    step("the head's Approve takes no press yet, so a was pressed before the read returned")
  ).toHaveCount(0);
  expect(sent(), step("a before the document's read returns sends nothing")).toBe(0);
  await expect(
    approve,
    step("the read returns and the head's Approve takes a press")
  ).toBeVisible();
  await page.unroute(documentRead);
  await expect(firstRow, step("the focus is still on the request's row")).toBeFocused();
  await page.keyboard.press("a");
  await expect
    .poll(() => reviews[0]?.versioned.length, { message: step("a sends one review of version 2") })
    .toBe(1);
  expect(reviews[0]?.versioned[0]?.input, step("the review approves")).toEqual({
    state: "approved",
  });
  expect(sent(), step("a sends that one review and nothing else")).toBe(1);
  await expect(dialog, step("a opens no dialog")).toHaveCount(0);

  // The second request: Shift+R opens Request changes with the focus in its reason, Escape closes
  // it sending nothing, and a reason sent with Ctrl+Enter is one change request.
  await selectRow(page, second.askId);
  await expect(approve, step("the second request's head's Approve takes a press")).toBeVisible();
  await page.keyboard.press("Shift+R");
  await expect(dialog, step("Shift+R opens Request changes")).toBeVisible();
  await expect(
    dialog.getByLabel("Reason"),
    step("Shift+R puts the focus in the reason field")
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog, step("Escape closes Request changes")).toHaveCount(0);
  expect(sent(), step("Escape sends nothing")).toBe(1);
  await page.keyboard.press("Shift+R");
  await expect(
    dialog.getByLabel("Reason"),
    step("Shift+R again puts the focus in the reason field")
  ).toBeFocused();
  await page.keyboard.type(ROLLBACK_REASON);
  await page.keyboard.press("Control+Enter");
  await expect
    .poll(() => reviews[1]?.versioned.length, {
      message: step("Ctrl+Enter sends one review of version 2"),
    })
    .toBe(1);
  expect(
    reviews[1]?.versioned[0]?.input,
    step("the review requests changes, with the reason")
  ).toEqual({ reason: ROLLBACK_REASON, state: "changes_requested" });
  expect(sent(), step("Ctrl+Enter sends that one review and nothing else")).toBe(2);

  // On the question there is nothing to approve: both keys send nothing, and `?` shows both not
  // available.
  await selectRow(page, question.id);
  await expect(inboxPane(page).getByTestId(`ask-${question.id}`)).toBeVisible();
  await page.keyboard.press("a");
  await page.keyboard.press("Shift+R");
  await expect(dialog, step("on the question Shift+R opens nothing")).toHaveCount(0);
  expect(sent(), step("on the question a and Shift+R send nothing")).toBe(2);
  await page.keyboard.press("?");
  for (const label of ["Approve", "Request changes"]) {
    await expect(
      inboxKeyHelp(page).filter({ hasText: new RegExp(`^${label}`) }),
      step(`on the question the ? help shows ${label} not available`)
    ).toHaveAttribute("data-enabled", "false");
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toHaveCount(0);
});

/** The steps of k-approval-keys where `place` draws the approval request as a card, with
 *  Approve and Request changes as its options: Shift+R picks Request changes, Escape in its
 *  reason lets go of it, and `a` approves with one press, through the ask's answer. */
async function approvalCardSteps(page: Page, place: "card mode" | "the drawer"): Promise<void> {
  await createProject({ key: "CORE", name: "Core" });
  const approval = await seedApprovalAtVersionTwo("CORE", "Clock audit");
  const reviews = watchReviews(page, [approval]);
  const step = (text: string) => `k-approval-keys: ${place}: ${text}`;
  const inbox = await openInboxAt(page, place, 1);
  const row = rowOf(inbox, approval.askId);
  // Each option's name is its label and the description the server writes under it.
  const requestChanges = row.getByRole("radio", { name: /^Request changes/ });
  await expect(
    row.getByRole("radio", { name: /^Approve/ }),
    step("the request's card draws Approve")
  ).toBeVisible();
  await row.focus();

  await page.keyboard.press("Shift+R");
  await expect(requestChanges, step("Shift+R picks Request changes")).toBeChecked();
  await expect(
    row.locator("[data-ask-answer]"),
    step("Shift+R puts the focus in the reason field")
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(requestChanges, step("Escape lets go of Request changes")).not.toBeChecked();
  await expect(row, step("Escape puts the focus back on the row")).toBeFocused();
  expect(reviewCount(reviews), step("Shift+R and Escape send nothing")).toBe(0);

  await page.keyboard.press("a");
  await expect
    .poll(() => reviews[0]?.answers.length, { message: step("a sends one answer") })
    .toBe(1);
  expect(reviews[0]?.answers[0]?.input, step("the answer approves")).toMatchObject({
    selected: ["Approve"],
  });
  expect(reviewCount(reviews), step("a sends that one review and nothing else")).toBe(1);
}

// SLOW(>=2s): an approval request seeded at version 2, then picked twice and answered.
test("k-approval-keys: in card mode a approves the request in hand with one press, and Shift+R picks Request changes, whose reason Escape lets go of", async ({
  page,
}) => {
  await approvalCardSteps(page, "card mode");
});

// SLOW(>=2s): an approval request seeded at version 2, then picked twice and answered.
test("k-approval-keys: in the drawer a approves the request in hand with one press, and Shift+R picks Request changes, whose reason Escape lets go of", async ({
  page,
}) => {
  await approvalCardSteps(page, "the drawer");
});

// ---------------------------------------------------------------------------
// k-approval-keys (R7), today's issue page: `a` and Shift+R press the header's Approve and Request
// changes while the issue's spec has an approval request that waits on the reader, send nothing
// while the focus is on the turn of a question on the Conversation tab (R22's turns take it), and
// send nothing while only another document's request waits - the Verification document's, since
// Dispatch refuses an approval request of a plan (`PLAN_APPROVED_BY_REVIEW`, the plan's D1).
// ---------------------------------------------------------------------------

/** The lines of the `?` help's Issue section. */
function issueKeyHelp(page: Page): Locator {
  return page
    .getByRole("dialog", { name: "Keyboard shortcuts" })
    .getByRole("region", { exact: true, name: "Issue" })
    .getByRole("listitem");
}

// SLOW(>=2s): an approval request and a question seeded, the issue page loaded on two tabs and
// one review read back.
test("k-approval-keys: on today's issue page a approves the spec from the header's Approve and Shift+R opens its Request changes while the spec's request waits on the reader, and on the Conversation tab a question's turn holds both back", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger handover";
  const approval = await seedApprovalAtVersionTwo("CORE", title);
  const question = await createAsk(
    approval.issueKey,
    { question: "Which ledger does the handover read?" },
    asker
  );
  const reviews = watchReviews(page, [approval]);
  const step = (text: string) => `k-approval-keys: the issue page: ${text}`;
  const header = page.getByTestId("issue-header");
  const dialog = page.getByRole("dialog", { name: "Request changes" });
  const approve = header.getByRole("button", { disabled: false, exact: true, name: "Approve" });

  // On the Conversation tab, with the focus on the question's turn, the reader is on that turn
  // and not on the header.
  await page.setViewportSize(WIDE);
  await page.goto(`/issues/${approval.issueKey}/conversation`);
  await expect(
    approve,
    step("the header draws the spec's Approve on the Conversation tab")
  ).toBeVisible();
  const questionTurn = page.locator(`#issue-conversation-panel [data-turn="ask:${question.id}"]`);
  await questionTurn.focus();
  await expect(questionTurn, step("the question's turn takes the focus")).toBeFocused();
  await page.keyboard.press("a");
  await page.keyboard.press("Shift+R");
  await expect(dialog, step("on the question's turn Shift+R opens nothing")).toHaveCount(0);
  await expect(questionTurn, step("on the question's turn the focus stays there")).toBeFocused();
  expect(reviewCount(reviews), step("on the question's turn a and Shift+R send nothing")).toBe(0);

  await openIssue(page, approval.issueKey, title);
  await expect(approve, step("the header draws the spec's Approve")).toBeVisible();

  await page.keyboard.press("Shift+R");
  await expect(dialog, step("Shift+R opens Request changes")).toBeVisible();
  await expect(
    dialog.getByLabel("Reason"),
    step("Shift+R puts the focus in the reason field")
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog, step("Escape closes Request changes")).toHaveCount(0);
  expect(reviewCount(reviews), step("Shift+R and Escape send nothing")).toBe(0);

  await page.keyboard.press("a");
  await expect
    .poll(() => reviews[0]?.versioned.length, {
      message: step("a sends one review of the spec's version 2"),
    })
    .toBe(1);
  expect(reviews[0]?.versioned[0]?.input, step("the review approves")).toEqual({
    state: "approved",
  });
  expect(reviewCount(reviews), step("a sends that one review and nothing else")).toBe(1);
  await expect(dialog, step("a opens no dialog")).toHaveCount(0);
});

// SLOW(>=2s): an approved spec, a Verification document and its approval request seeded, the
// issue page loaded and the help opened.
test("k-approval-keys: on today's issue page a and Shift+R send nothing while only the Verification document's request waits on the reader, and ? shows both not available", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Mirror replay";
  const issue = await createIssue({ project: "CORE", spec: `# ${title}\n\nThe plan.\n`, title });
  // The spec is approved, so while another document's request is open the header still draws
  // the spec's Request changes beside its pill: a key that read that button alone would act.
  await reviewArtifact(issue.primary_artifact_id, { state: "approved" }, { login: "alice" });
  const verification = await createIssueArtifact(issue.key, {
    content: "# Verification\n\nThe mirror replays the ledger.\n",
    name: "verification.md",
  });
  const requested = withOpenedAsk(
    await requestApproval(verification.artifact.id, { summary: "The checks." }, asker)
  );
  expect(
    requested.ask,
    "k-approval-keys: the Verification document's request waits on the reader"
  ).toMatchObject({ state: "open", waiting_on_person: "alice" });
  const writes = [
    reviewWrites(page, issue.primary_artifact_id),
    versionReviewWrites(page, issue.primary_artifact_id, 1),
    reviewWrites(page, verification.artifact.id),
    versionReviewWrites(page, verification.artifact.id, requested.version),
    answerWrites(page, requested.ask.id),
  ];
  const step = (text: string) =>
    `k-approval-keys: the issue whose Verification document alone waits: ${text}`;
  const header = page.getByTestId("issue-header");
  const dialog = page.getByRole("dialog", { name: "Request changes" });

  await page.setViewportSize(WIDE);
  await openIssue(page, issue.key, title);
  await expect(
    header.getByRole("button", { exact: true, name: "Request changes" }),
    step("the header draws the spec's Request changes")
  ).toBeVisible();
  await page.keyboard.press("a");
  await page.keyboard.press("Shift+R");
  await expect(dialog, step("Shift+R opens no Request changes")).toHaveCount(0);
  await page.keyboard.press("?");
  for (const label of ["Approve", "Request changes"]) {
    await expect(
      issueKeyHelp(page).filter({ hasText: new RegExp(`^${label}`) }),
      step(`the ? help shows ${label} not available`)
    ).toHaveAttribute("data-enabled", "false");
  }
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toHaveCount(0);
  expect(countOf(writes), step("a and Shift+R send nothing")).toBe(0);
});

// ---------------------------------------------------------------------------
// k-next-item (R8): after the reader snoozes, dismisses, moves back to the Inbox, approves,
// requests changes on or answers the item in hand, the item after it in the list is selected and
// its row has the keyboard focus - the item before it where none came after, and the list's empty
// message where none is left - with the Later band folded and open, beside the reading pane, in
// card mode and in the drawer.
// ---------------------------------------------------------------------------

/** The word a search narrows k-next-item's list by: the title of the second thread's issue
 *  holds it, and nothing else the list draws does. */
const NARROWING_WORD = "zephyr";

/** The reason k-next-item's request for changes gives. */
const NEXT_ITEM_REASON = "Name the lease owner.";

/** k-next-item's list, by row id, top to bottom as the Inbox draws it. */
interface NextItemSeed {
  /** Two approval requests, each of its own issue's spec at version 2: the first two items. */
  readonly approvals: readonly [SeededApproval, SeededApproval];
  /** A question with two options, after them. */
  readonly question: string;
  /** Asks A, B, C, D and E, in that order, after the question. */
  readonly asks: readonly [string, string, string, string, string];
  /** Two reply threads, the last two items; only the second one's issue says `NARROWING_WORD`. */
  readonly threads: readonly [string, string];
  /** A reply thread the reader has dismissed: in the Dismissed view alone. */
  readonly dismissed: string;
  /** Every row the Inbox lists, top to bottom. */
  readonly order: readonly string[];
}

/** Seeds k-next-item's list bottom first. Every item is on an issue at no priority, so one band
 *  holds them all, newest first: the item made last is the first row. The dismissed thread is
 *  made first, so the Move to Inbox puts it back as the last row. */
async function seedNextItem(): Promise<NextItemSeed> {
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const thread = async (title: string, root: string): Promise<string> => {
    const issue = await createIssue({ force: true, project: "CORE", title });
    return replyRowId(await seedCommentThread(issue.key, root, "Noted, I will look."));
  };
  const dismissed = await thread("Mirror window", "Which mirror window holds?");
  await dismissThread(dismissed);
  const secondThread = await thread("Zephyr budget", "How far back does a replay go?");
  const firstThread = await thread("Ledger cutover", "How long does a lease last?");
  const asks: string[] = [];
  for (const name of ["Echo", "Delta", "Charlie", "Bravo", "Alpha"]) {
    const issue = await createIssue({ force: true, project: "CORE", title: `${name} rollout` });
    const ask = await createAsk(issue.key, { question: `Ship the ${name} rollout?` }, asker);
    asks.unshift(ask.id);
  }
  const questionIssue = await createIssue({ force: true, project: "CORE", title: "Clock audit" });
  const question = (
    await createAsk(
      questionIssue.key,
      { options: SHIP_OR_HOLD, question: "Ship the clock audit?" },
      asker
    )
  ).id;
  const secondApproval = await seedApprovalAtVersionTwo("CORE", "Quota review");
  const firstApproval = await seedApprovalAtVersionTwo("CORE", "Lease handover");
  const [a, b, c, d, e] = asks;
  if (a === undefined || b === undefined || c === undefined || d === undefined || e === undefined) {
    throw new Error("five asks were seeded");
  }
  return {
    approvals: [firstApproval, secondApproval],
    asks: [a, b, c, d, e],
    dismissed,
    order: [
      firstApproval.askId,
      secondApproval.askId,
      question,
      ...asks,
      firstThread,
      secondThread,
    ],
    question,
    threads: [firstThread, secondThread],
  };
}

/** Opens k-next-item's list where `place` draws it, and holds the list to the order seeded, so
 *  every "the item after" below names the row that came after. */
async function openNextItem(page: Page, place: InboxPlace): Promise<[InboxAt, NextItemSeed]> {
  const seed = await seedNextItem();
  const inbox = await openInboxAt(page, place, seed.order.length);
  expect(inbox.order, `k-next-item: ${place}: the Inbox lists the items as seeded`).toEqual(
    seed.order
  );
  return [inbox, seed];
}

/** Puts the reader on the item `id` (`goTo`), then takes the pointer off every row: the list
 *  holds a row under the pointer where the list moves it, and each step reads where the list
 *  hands the reader once the row they acted on goes. */
async function putOn(inbox: InboxAt, id: string): Promise<void> {
  await goTo(inbox, id);
  await inbox.outside.hover();
}

/** Reads that the item `id` is in hand, for the step `step`: beside the reading column its row
 *  is the one `aria-current` row, in card mode and in the drawer no row is, and in every place
 *  its row holds the keyboard focus (`document.activeElement`). */
async function expectInHand(inbox: InboxAt, id: string, step: string): Promise<void> {
  const current = inbox.scope.locator('[data-inbox-row][aria-current="true"]');
  if (inbox.place === "the reading pane") {
    await expect(rowOf(inbox, id), `${step}: the item is selected`).toHaveAttribute(
      "aria-current",
      "true"
    );
    await expect(current, `${step}: no other item is selected`).toHaveCount(1);
  } else {
    await expect(current, `${step}: no row is selected here`).toHaveCount(0);
  }
  await expect(rowOf(inbox, id), `${step}: the item's row has the focus`).toBeFocused();
}

/** Shows the view `label` names, Inbox or Dismissed: from the address on the Inbox page, and from
 *  the drawer's own View menu, since the drawer writes nothing to the address of the page it is
 *  over. */
async function showView(page: Page, inbox: InboxAt, label: "Inbox" | "Dismissed"): Promise<void> {
  if (inbox.place !== "the drawer") {
    await page.goto(label === "Dismissed" ? "/?show=dismissed" : "/");
    return;
  }
  const view = inbox.scope.getByRole("button", { exact: true, name: "View" });
  const menu = page.getByRole("dialog", { name: "View" });
  await view.click();
  await menu.getByRole("group", { name: "Show" }).getByRole("radio", { name: label }).click();
  // The pick leaves the menu open, or closes it where the drawer draws the view anew: the
  // trigger is pressed until the menu is shut, its state read again on each pass.
  await expect(async () => {
    if ((await view.getAttribute("aria-expanded")) === "true") await view.click();
    await expect(menu).toHaveCount(0, { timeout: 1_000 });
  }).toPass();
}

/** k-next-item's snoozes where `place` draws the Inbox: one with the Later band folded, one with
 *  it open, and one of two selected asks. */
async function nextItemSnoozeSteps(page: Page, place: InboxPlace): Promise<void> {
  const [inbox, seed] = await openNextItem(page, place);
  const [, b, c, d, e] = seed.asks;
  const [firstThread] = seed.threads;
  const step = (text: string) => `k-next-item: ${place}: ${text}`;
  const menu = page.getByRole("menu");

  // With the Later band folded: B, snoozed with b and 1, hands the reader on to C.
  await putOn(inbox, b);
  await page.keyboard.press("b");
  await expect(menu, step("b opens B's Snooze menu")).toBeVisible();
  await page.keyboard.press("1");
  await expectInHand(inbox, c, step("with the Later band folded, a snooze of B hands on to C"));
  await expect(rowOf(inbox, b), step("B folds into Later")).toHaveCount(0);

  // With the Later band open: C, snoozed, hands the reader on to D, and C, drawn in the band, is
  // not the item in hand.
  await laterBand(inbox, 1).click();
  await expect(rowOf(inbox, b), step("the Later band opens on B")).toBeVisible();
  await putOn(inbox, c);
  await page.keyboard.press("b");
  await expect(menu, step("b opens C's Snooze menu")).toBeVisible();
  await page.keyboard.press("1");
  await expectInHand(inbox, d, step("with the Later band open, a snooze of C hands on to D"));
  await expect(rowOf(inbox, c), step("the snoozed C is drawn in Later")).toHaveAttribute(
    "data-inbox-section",
    "later"
  );
  await expect(rowOf(inbox, c), step("the snoozed C is not selected")).not.toHaveAttribute(
    "aria-current",
    "true"
  );

  // D and E selected with x, both snoozed: the item after E, the first thread, is in hand.
  for (const id of [d, e]) {
    await putOn(inbox, id);
    await page.keyboard.press("x");
  }
  const bar = inbox.scope.getByRole("toolbar", { name: "Selected rows" });
  await expect(bar, step("x selects D and E")).toContainText("2 selected");
  await page.keyboard.press("b");
  await expect(menu, step("b opens the bar's Snooze menu")).toHaveAccessibleName(
    "Snooze selected asks"
  );
  await page.keyboard.press("1");
  await expectInHand(
    inbox,
    firstThread,
    step("a snooze of D and E, selected, hands on to the item after E")
  );
  await expect(bar, step("the bulk snooze lands and the bar goes")).toHaveCount(0);
}

/** k-next-item's dismissals where `place` draws the Inbox: the Dismissed view's Move to Inbox of
 *  its only thread, a dismiss of the first thread, and a dismiss of the last item. */
async function nextItemDismissSteps(page: Page, place: InboxPlace): Promise<void> {
  const [inbox, seed] = await openNextItem(page, place);
  const [firstThread, secondThread] = seed.threads;
  const step = (text: string) => `k-next-item: ${place}: ${text}`;
  const dismissals = dismissWrites(page);
  const empty = inbox.scope.getByRole("region", { name: "Inbox empty state" });

  // In the Dismissed view, e on its one thread moves it back to the Inbox: no item is left in
  // the view, so its empty message has the focus.
  await showView(page, inbox, "Dismissed");
  await expect(
    inbox.scope.locator("[data-inbox-row]"),
    step("the Dismissed view lists the dismissed thread alone")
  ).toHaveCount(1);
  await putOn(inbox, seed.dismissed);
  await page.keyboard.press("e");
  await expect(empty, step("e in the Dismissed view leaves its empty message")).toHaveText(
    "Nothing dismissed"
  );
  await expect(empty, step("the Dismissed view's empty message has the focus")).toBeFocused();
  expect(dismissals, step("e sends one Move to Inbox")).toHaveLength(1);

  // Back in the Inbox, the moved thread is the last item. e on the first thread hands the reader
  // on to the item after it.
  await showView(page, inbox, "Inbox");
  await expect(rowOf(inbox, seed.dismissed), step("the thread is back in the Inbox")).toHaveCount(
    1
  );
  await putOn(inbox, firstThread);
  await page.keyboard.press("e");
  await expectInHand(
    inbox,
    secondThread,
    step("a dismiss of the first thread hands on to the next")
  );

  // The last item of the list: the item before it is in hand.
  await putOn(inbox, seed.dismissed);
  await page.keyboard.press("e");
  await expectInHand(
    inbox,
    secondThread,
    step("a dismiss of the last item hands on to the item before it")
  );
  await expect.poll(() => dismissals.length, { message: step("each e sends one write") }).toBe(3);
}

/** k-next-item's reviews and answer where `place` draws the Inbox - `a` on the first approval
 *  request, Shift+R on the second, an answer to the question - and the only item a search leaves,
 *  dismissed. */
async function nextItemReviewSteps(page: Page, place: InboxPlace): Promise<void> {
  const [inbox, seed] = await openNextItem(page, place);
  const [firstApproval, secondApproval] = seed.approvals;
  const [a] = seed.asks;
  const [, secondThread] = seed.threads;
  const step = (text: string) => `k-next-item: ${place}: ${text}`;
  const reviews = watchReviews(page, seed.approvals);
  const answers = answerWrites(page, seed.question);
  const pane = place === "the reading pane";
  const head = inboxPane(page).locator("header").first();

  // `a` approves the first request once its document is read beside the column, where the head
  // holds its Approve until then; a card draws its Approve at once.
  await putOn(inbox, firstApproval.askId);
  await expect(
    pane
      ? head.getByRole("button", { disabled: false, exact: true, name: "Approve" })
      : rowOf(inbox, firstApproval.askId).getByRole("radio", { name: /^Approve/ }),
    step("the first request draws its Approve")
  ).toBeVisible();
  await page.keyboard.press("a");
  await expectInHand(
    inbox,
    secondApproval.askId,
    step("a on the first request hands on to the second")
  );
  expect(reviewCount(reviews), step("a sends one review")).toBe(1);

  // Shift+R on the second, a reason, and Ctrl+Enter.
  await expect(
    pane
      ? head.getByRole("button", { disabled: false, exact: true, name: "Request changes" })
      : rowOf(inbox, secondApproval.askId).getByRole("radio", { name: /^Request changes/ }),
    step("the second request draws its Request changes")
  ).toBeVisible();
  await page.keyboard.press("Shift+R");
  await expect(
    pane
      ? page.getByRole("dialog", { name: "Request changes" }).getByLabel("Reason")
      : rowOf(inbox, secondApproval.askId).locator("[data-ask-answer]"),
    step("Shift+R puts the focus in the reason")
  ).toBeFocused();
  await page.keyboard.type(NEXT_ITEM_REASON);
  await page.keyboard.press("Control+Enter");
  await expectInHand(
    inbox,
    seed.question,
    step("a request for changes on the second request hands on to the question")
  );
  expect(reviewCount(reviews), step("Ctrl+Enter sends one review more")).toBe(2);

  // The question, answered with 1, Enter and Ctrl+Enter.
  const card = (pane ? inboxPane(page) : rowOf(inbox, seed.question)).getByTestId(
    `ask-${seed.question}`
  );
  await expect(
    card.locator("[data-ask-answer]"),
    step("the question's card is drawn")
  ).toBeVisible();
  await page.keyboard.press("1");
  await expect(card.getByRole("radio", { name: "Ship" }), step("1 picks Ship")).toBeChecked();
  await page.keyboard.press("Enter");
  await expect(
    card.locator("[data-ask-answer]"),
    step("Enter puts the caret in the answer box")
  ).toBeFocused();
  await page.keyboard.press("Control+Enter");
  await expectInHand(inbox, a, step("an answer to the question hands on to A"));
  expect(answers, step("Ctrl+Enter sends one answer")).toHaveLength(1);

  // A search that leaves one item, the second thread, which e dismisses: the list's empty message
  // has the focus, and the Inbox's keys work from it.
  await page.keyboard.press("f");
  await page.keyboard.type(NARROWING_WORD);
  await expect(
    inbox.scope.locator("[data-inbox-row]"),
    step("the search leaves the second thread alone")
  ).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(rowOf(inbox, secondThread), step("Escape puts the focus on its row")).toBeFocused();
  await page.keyboard.press("e");
  const empty = inbox.scope.getByRole("region", { name: "Inbox empty state" });
  await expect(empty, step("e on the only item leaves the empty message")).toHaveText(
    "Nothing matches this text"
  );
  await expect(empty, step("the empty message has the focus")).toBeFocused();
  if (place === "the drawer") {
    // The drawer is a dialog, whose scope is the only one a key is read through, so the page's
    // `?` is not one of its keys: its own `f` is.
    await page.keyboard.press("f");
    await expect(
      inbox.scope.getByRole("searchbox", { name: "Search asks" }),
      step("f from the empty message puts the focus in the search box")
    ).toBeFocused();
    return;
  }
  await page.keyboard.press("?");
  await expect(
    page.getByRole("dialog", { name: "Keyboard shortcuts" }),
    step("? from the empty message opens the help")
  ).toBeVisible();
}

/** Snoozes the item in hand until later today with `h`, by whichever control the build draws, so
 *  the row below reaches its case on the merge base too: this tree's `h` opens the Snooze menu,
 *  where `1` picks Later today; the merge base's `h` puts the focus on the column head's native
 *  snooze select, whose Later today is picked as a select's option is. */
async function snoozeLaterToday(page: Page, step: string): Promise<void> {
  await page.keyboard.press("h");
  const menu = page.getByRole("menu");
  const select = page.locator("select:focus");
  await expect(
    menu.or(select),
    `${step}: h opens the Snooze menu, or puts the focus on the snooze select`
  ).toHaveCount(1);
  // The wait above settled on the menu or the select, so one count tells which one it is.
  if ((await menu.count()) === 0) {
    await select.selectOption({ label: "Later today" });
    return;
  }
  await page.keyboard.press("1");
}

// The check's negative control: at the merge base, with the Later band open, a snooze of the
// selected ask leaves the selection on that ask, now in Later. The case stands first in a row of
// its own, and its snooze takes either build's control, so the base fails it by name.
// SLOW(>=2s): ten items seeded, one snoozed by the API, and one snoozed beside the reading pane.
test("k-next-item: beside the reading pane, with the Later band open, a snooze of the selected ask selects and focuses the item after it, and not the ask now in Later", async ({
  page,
}) => {
  const seed = await seedNextItem();
  const [, b, c, d] = seed.asks;
  const step = (text: string) => `k-next-item: the reading pane: ${text}`;
  // B is in Later before the page opens, so the band is there to open.
  await snoozeAsk(b, new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString());
  const listed = seed.order.filter((id) => id !== b);
  const inbox = await openInboxAt(page, "the reading pane", listed.length);
  expect(
    inbox.order,
    step("the Inbox lists the items as seeded, B in the folded Later band")
  ).toEqual(listed);
  await laterBand(inbox, 1).click();
  await expect(rowOf(inbox, b), step("the Later band opens on B")).toBeVisible();

  await putOn(inbox, c);
  await snoozeLaterToday(page, step("C's snooze"));
  await expectInHand(inbox, d, step("with the Later band open, a snooze of C hands on to D"));
  await expect(rowOf(inbox, c), step("the snoozed C is drawn in Later")).toHaveAttribute(
    "data-inbox-section",
    "later"
  );
  await expect(rowOf(inbox, c), step("the snoozed C is not selected")).not.toHaveAttribute(
    "aria-current",
    "true"
  );
});

// SLOW(>=2s): ten items seeded, three snoozes written and read back beside the reading pane.
test("k-next-item: beside the reading pane a snooze, with the Later band folded and open, and of two selected asks, selects and focuses the item after", async ({
  page,
}) => {
  await nextItemSnoozeSteps(page, "the reading pane");
});

// SLOW(>=2s): ten items seeded, three snoozes written and read back on the cards.
test("k-next-item: in card mode a snooze, with the Later band folded and open, and of two selected asks, focuses the row of the item after", async ({
  page,
}) => {
  await nextItemSnoozeSteps(page, "card mode");
});

// SLOW(>=2s): ten items seeded, three snoozes written and read back in the drawer.
test("k-next-item: in the drawer a snooze, with the Later band folded and open, and of two selected asks, focuses the row of the item after", async ({
  page,
}) => {
  await nextItemSnoozeSteps(page, "the drawer");
});

// SLOW(>=2s): ten items seeded, a thread moved back and two dismissed beside the reading pane.
test("k-next-item: beside the reading pane a dismiss selects and focuses the item after, the item before at the end, and the Dismissed view's empty message once it is empty", async ({
  page,
}) => {
  await nextItemDismissSteps(page, "the reading pane");
});

// SLOW(>=2s): ten items seeded, a thread moved back and two dismissed on the cards.
test("k-next-item: in card mode a dismiss focuses the item after, the item before at the end, and the Dismissed view's empty message once it is empty", async ({
  page,
}) => {
  await nextItemDismissSteps(page, "card mode");
});

// SLOW(>=2s): ten items seeded, a thread moved back and two dismissed in the drawer.
test("k-next-item: in the drawer a dismiss focuses the item after, the item before at the end, and the Dismissed view's empty message once it is empty", async ({
  page,
}) => {
  await nextItemDismissSteps(page, "the drawer");
});

// SLOW(>=2s): ten items seeded, two reviews and an answer written, and a search beside the pane.
test("k-next-item: beside the reading pane an approval, a request for changes and an answer select and focus the item after, and the only item a search leaves hands the focus to the empty message", async ({
  page,
}) => {
  await nextItemReviewSteps(page, "the reading pane");
});

// SLOW(>=2s): ten items seeded, two reviews and an answer written, and a search on the cards.
test("k-next-item: in card mode an approval, a request for changes and an answer focus the item after, and the only item a search leaves hands the focus to the empty message", async ({
  page,
}) => {
  await nextItemReviewSteps(page, "card mode");
});

// SLOW(>=2s): ten items seeded, two reviews and an answer written, and a search in the drawer.
test("k-next-item: in the drawer an approval, a request for changes and an answer focus the item after, and the only item a search leaves hands the focus to the empty message", async ({
  page,
}) => {
  await nextItemReviewSteps(page, "the drawer");
});

// ---------------------------------------------------------------------------
// k-every-action (R11), a first form for the Inbox: every button and menu trigger the Inbox draws
// for an item - on its row, its card, the reading pane's head and the bar of selected rows - is
// an action of the spec's keys table or of the command bar's list, and each such action has its
// key in the `?` help under Inbox, or is a row of ⌘K with the item in hand, its key beside it
// where it has one.
// ---------------------------------------------------------------------------

/** One action of the spec's Design ("The keys" and the command bar's actions), as the page names
 *  it: its label in `?` and in ⌘K, and the key both show beside it, where it has one. */
interface InboxAction {
  readonly label: string;
  readonly key?: string;
}

/** The row's table: the name of each control the Inbox draws for an item, by a pattern, and the
 *  action it is. A key reads as `?` and ⌘K draw it: `b` or `h` as `borh`, Escape as `Esc`. */
const INBOX_ACTIONS: readonly (readonly [RegExp, InboxAction])[] = [
  [/^Approve( v\d+)?$/, { key: "a", label: "Approve" }],
  [/^Request changes$/, { key: "Shift+R", label: "Request changes" }],
  // The bar's Snooze selected asks is the same action, on every marked ask.
  [/^Snooze /, { key: "borh", label: "Snooze the focused ask, or every selected ask" }],
  [/^Un-snooze /, { label: "Un-snooze" }],
  [/^Priority of /, { key: "p", label: "Open the priority menu" }],
  [/^Assign \S+ to me$/, { label: "Assign to me" }],
  [/^Copy reference/, { key: "y", label: "Copy the row's reference" }],
  [/^(Dismiss|Move to Inbox): /, { key: "e", label: "Dismiss, or Move to Inbox" }],
  [/^Mark read$/, { label: "Mark read" }],
  [/^Show (\d+ earlier|fewer) repl/, { label: "Show earlier replies" }],
  // The asker's button, which names the session by the short form of its id.
  [new RegExp(`^session:${asker.actor.id.slice(0, 8)}`), { label: "Show the asker's handles" }],
  [/^Hand this question to/, { label: "Hand on" }],
  [/^Referenced by/, { label: "Referenced by" }],
  [/^(Show|Hide) diff$/, { label: "Show the earlier approval's diff" }],
  // Answer is Ctrl+Enter in the answer box, and the note's disclosure is Enter's: both are the
  // answer box Enter puts the caret in.
  [
    /^(Answer|Add a note or answer in your own words)$/,
    { key: "Enter", label: "Answer the ask in hand" },
  ],
  [/^Ask back$/, { label: "Ask back" }],
  [/^Decline to answer$/, { label: "Decline" }],
  // Recipients names how many it reaches once the ask's thread is read.
  [/^(Recipients|Reaches \d+)$/, { label: "Recipients" }],
  [/^Skip to the answer$/, { label: "Skip to the answer" }],
  [/^Next change$/, { label: "Next change" }],
  [/^(Awaiting approval|Approved|Changes requested)/, { label: "Show the reviews" }],
  [
    /^(Dismiss|Move to Inbox) selected threads$/,
    { label: "Dismiss, or Move to Inbox, the selected items" },
  ],
  [/^Select all$/, { label: "Select all" }],
  [/^Clear selection$/, { key: "Esc", label: "Clear the selection" }],
  // A thread's reply box, which `r` puts the caret in: its Send is Ctrl+Enter there, and its own
  // controls are reached by Tab from it.
  [/^(Send|Cancel reply)$/, { key: "r", label: "Reply in the thread of the item in hand" }],
];

/** The names of the buttons and menu triggers `scope` shows that take a press now: each one's
 *  `aria-label`, else its text without what it hides from assistive tech (the key a control
 *  draws beside its label in the pane's head). A disabled one - Ask back with nothing typed,
 *  Answer with nothing picked - is no action of the item in hand until it takes a press; the
 *  question the reader asks back on reads Ask back once it does. */
function controlNames(scope: Locator): Promise<string[]> {
  return scope.locator('button, [role="button"]').evaluateAll((nodes) =>
    nodes
      .filter((node) => node.getClientRects().length > 0 && !node.matches(":disabled"))
      .map((node) => {
        const label = node.getAttribute("aria-label");
        if (label !== null) return label.replace(/\s+/g, " ").trim();
        const named = node.cloneNode(true) as Element;
        for (const hidden of named.querySelectorAll('[aria-hidden="true"]')) hidden.remove();
        return (named.textContent ?? "").replace(/\s+/g, " ").trim();
      })
  );
}

/** The labels of the `?` help's Inbox section, opened over the item in hand and closed again. */
async function inboxHelpLabels(page: Page): Promise<string[]> {
  await page.keyboard.press("?");
  const help = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(help).toBeVisible();
  const labels = await inboxKeyHelp(page).evaluateAll((items) =>
    items.map((item) => item.querySelector("span")?.textContent?.trim() ?? "")
  );
  await page.keyboard.press("Escape");
  await expect(help).toHaveCount(0);
  return labels;
}

/** The text of each row of ⌘K's Actions, its label and the key drawn beside it, opened over the
 *  item in hand and closed again. */
async function commandBarRows(page: Page): Promise<string[]> {
  await page.keyboard.press("Control+k");
  const bar = page.getByRole("dialog", { name: "Search" });
  const rows = bar.getByRole("group", { exact: true, name: "Actions" }).getByRole("option");
  await expect(rows.first()).toBeVisible();
  const texts = await rows.evaluateAll((options) =>
    options.map((option) => (option.textContent ?? "").replace(/\s+/g, ""))
  );
  await page.keyboard.press("Escape");
  await expect(bar).toHaveCount(0);
  return texts;
}

/** Holds every control `names` names to the table, and each action they are to `?` and ⌘K, read
 *  over the item in hand, for the step `step`. */
async function expectEveryAction(
  page: Page,
  names: readonly string[],
  step: (text: string) => string
): Promise<void> {
  const actions = new Map<string, InboxAction>();
  for (const name of names) {
    const action = INBOX_ACTIONS.find(([pattern]) => pattern.test(name))?.[1];
    // Soft, so one run names every control the table does not.
    expect
      .soft(
        action,
        step(`the control "${name}" is an action of the keys table or of the command bar`)
      )
      .toBeDefined();
    if (action !== undefined) actions.set(action.label, action);
  }
  const help = await inboxHelpLabels(page);
  const bar = await commandBarRows(page);
  const squeezed = (text: string) => text.replace(/\s+/g, "");
  for (const action of actions.values()) {
    const listed = bar.includes(squeezed(action.label + (action.key ?? "")));
    expect
      .soft(
        listed || (action.key !== undefined && help.includes(action.label)),
        step(`${action.label}: ? lists its key under Inbox, or ⌘K lists it with the item in hand`)
      )
      .toBe(true);
    if (action.key !== undefined) {
      expect
        .soft(
          bar.includes(squeezed(action.label)),
          step(`${action.label}: ⌘K shows its key ${action.key} beside it`)
        )
        .toBe(false);
    }
  }
}

/** k-every-action's items: a question nobody holds, a question with options the reader can ask
 *  back on, an approval request, a snoozed question, a reply thread and an agent thread. */
interface EveryActionSeed {
  readonly unassigned: string;
  readonly askBack: string;
  readonly approval: string;
  readonly snoozed: string;
  readonly reply: string;
  readonly agent: string;
}

async function seedEveryAction(): Promise<EveryActionSeed> {
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const question = async (title: string, input: Parameters<typeof createAsk>[1]) =>
    (
      await createAsk(
        (
          await createIssue({ force: true, project: "CORE", title })
        ).key,
        input,
        asker
      )
    ).id;
  const unassigned = await question("Lease rollout", { question: "Who takes the lease rollout?" });
  const askBack = await question("Mirror replay", {
    options: SHIP_OR_HOLD,
    question: "Ship the mirror replay?",
  });
  const approval = (await seedApprovalAtVersionTwo("CORE", "Clock audit")).askId;
  const snoozed = await question("Key rotation", { question: "Rotate the keys this week?" });
  await snoozeAsk(snoozed, new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString());
  const threadIssue = await createIssue({ force: true, project: "CORE", title: "Replay budget" });
  const reply = replyRowId(
    await seedCommentThread(threadIssue.key, "How far back does a replay go?", "One hour.")
  );
  const agent = agentRowId(
    await seedAgentThread({ body: "Is the mirror caught up?", replies: ["It is, as of noon."] })
  );
  return { agent, approval, askBack, reply, snoozed, unassigned };
}

/** Every step of k-every-action's first form where `place` draws the Inbox. */
async function everyActionSteps(
  page: Page,
  place: "the reading pane" | "card mode"
): Promise<void> {
  const seed = await seedEveryAction();
  const step = (text: string) => `k-every-action: ${place}: ${text}`;
  // Every project names a default assignee, so no seed makes a question nobody holds: the Inbox's
  // reads answer as if the first one were.
  await page.route("**/api/v1/inbox", async (route) => {
    const response = await route.fetch();
    const rows = (await response.json()) as { id: string }[];
    await route.fulfill({
      json: rows.map((row) =>
        row.id === seed.unassigned ? { ...row, waiting_on_person: null } : row
      ),
      response,
    });
  });
  await page.setViewportSize(place === "card mode" ? NARROW : WIDE);
  await page.goto("/?assignee=everyone");
  await expect(page.locator(`[data-inbox-row="${seed.unassigned}"]`)).toBeVisible();
  await page.getByRole("button", { name: /^Later \(1\)/ }).click();
  const pane = inboxPane(page);
  const row = (id: string) => page.locator(`[data-inbox-row="${id}"]`);

  const items = [
    [seed.unassigned, "the question nobody holds"],
    [seed.askBack, "the question the reader asks back on"],
    [seed.approval, "the approval request"],
    [seed.snoozed, "the snoozed question"],
    [seed.reply, "the reply thread"],
    [seed.agent, "the agent thread"],
  ] as const;
  for (const [id, what] of items) {
    await expect(row(id), step(`${what} is listed`)).toBeVisible();
    if (place === "the reading pane") {
      await selectRow(page, id);
    }
    if (id === seed.askBack) {
      // Ask back takes a press once a question is typed in the answer box.
      const card = place === "the reading pane" ? pane.getByTestId(`ask-${id}`) : row(id);
      await card.locator("[data-ask-answer]").fill("Which format does it ship in?");
    }
    await row(id).focus();
    const names = await controlNames(row(id));
    if (place === "the reading pane") {
      await expect(pane.locator("header").first(), step(`${what}'s head is drawn`)).toBeVisible();
      names.push(...(await controlNames(pane.locator("header").first())));
      const card = pane.getByTestId(`ask-${id}`);
      // The pane draws its body in the render that drew its head, which was waited for above.
      if ((await card.count()) > 0) names.push(...(await controlNames(card)));
      const thread = pane.locator("[data-inbox-thread-body]");
      // The same render drew any thread body of the item, so this count is the drawn one.
      if ((await thread.count()) > 0) names.push(...(await controlNames(thread.first())));
    }
    await expectEveryAction(page, names, (text) => step(`${what}: ${text}`));
  }

  // With two items marked, the bar of selected rows.
  for (const id of [seed.reply, seed.askBack]) {
    if (place === "the reading pane") await selectRow(page, id);
    await row(id).focus();
    await page.keyboard.press("x");
  }
  const bar = page.getByRole("toolbar", { name: "Selected rows" });
  await expect(bar, step("x marks two items")).toContainText("2 selected");
  await expectEveryAction(page, await controlNames(bar), (text) =>
    step(`the bar of selected rows: ${text}`)
  );
}

// SLOW(>=2s): six items of four kinds seeded, each read with ? and ⌘K, then two marked.
test("k-every-action: beside the reading pane every control of an item is a key or a command bar action of the item in hand", async ({
  page,
}) => {
  await everyActionSteps(page, "the reading pane");
});

// SLOW(>=2s): six items of four kinds seeded, each read with ? and ⌘K, then two marked.
test("k-every-action: in card mode every control of an item is a key or a command bar action of the item in hand", async ({
  page,
}) => {
  await everyActionSteps(page, "card mode");
});

// SLOW(>=2s): the command bar opened six times over a project page, each row followed.
test("k-every-action: on a project page the command bar goes to every page the navigation always shows", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const bar = page.getByRole("dialog", { name: "Search" });
  for (const [label, address] of [
    ["Go to Inbox", "/"],
    ["Go to Agents", "/agents"],
    ["Go to Priorities", "/priorities"],
    ["Go to Delivery", "/delivery"],
    ["Go to Pipeline", "/pipeline"],
    ["Go to Settings", "/settings"],
  ] as const) {
    await page.goto("/projects/CORE");
    await expect(page.getByRole("button", { exact: true, name: "Board" })).toBeVisible();
    await page.locator("body").focus();
    await page.keyboard.press("Control+k");
    const go = bar
      .getByRole("group", { exact: true, name: "Actions" })
      .getByRole("option", { name: new RegExp(`^${label}`) });
    await expect(go, `k-every-action: on a project page ⌘K lists ${label}`).toHaveCount(1);
    await go.click();
    await expect(page, `k-every-action: ${label} moves the page to ${address}`).toHaveURL(
      new RegExp(`${address === "/" ? "/$" : `${address}$`}`)
    );
  }
});

// ---------------------------------------------------------------------------
// k-pane-head (R9): the controls of the reading pane's head in a row of their own above the
// title, each in one place whatever the title's length and whenever the document's read returns,
// and each showing its key.
// ---------------------------------------------------------------------------

/** A title of one character, and one of 300, the longest k-pane-head asks for (the server takes
 *  1,000). */
const SHORT_TITLE = "Q";
const LONG_TITLE = `${"The mirror replays the ledger on a cold start, window by window, and hands the lease back. ".repeat(4).slice(0, 299)}.`;

/** A box in the reading pane, in px from the pane's own top left corner. */
interface PaneRect {
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  readonly top: number;
}

/** Where `box` stands in the reading pane: its own box, relative to the pane's. */
function paneRectOf(box: Locator): Promise<PaneRect> {
  return box.evaluate((node) => {
    const pane = node.closest('[data-testid="inbox-pane"]');
    if (pane === null) throw new Error("the box is not in the reading pane");
    const own = node.getBoundingClientRect();
    const frame = pane.getBoundingClientRect();
    return {
      bottom: own.bottom - frame.top,
      left: own.left - frame.left,
      right: own.right - frame.left,
      top: own.top - frame.top,
    };
  });
}

/** A control of the pane's head: its name in the rows' messages, the key it shows where it has
 *  one, and how the head draws it. */
interface HeadControl {
  readonly name: string;
  readonly key?: string;
  readonly find: (head: Locator) => Locator;
}

const APPROVE: HeadControl = {
  find: (head) => head.getByRole("button", { exact: true, name: "Approve" }),
  key: "a",
  name: "Approve",
};
const REQUEST_CHANGES: HeadControl = {
  find: (head) => head.getByRole("button", { exact: true, name: "Request changes" }),
  key: "Shift+R",
  name: "Request changes",
};
const OPEN_PAGE: HeadControl = {
  find: (head) => head.getByRole("link", { exact: true, name: "Open page" }),
  name: "Open page",
};
const PRIORITY: HeadControl = {
  find: (head) => head.getByRole("button", { name: /^Priority of / }),
  key: "p",
  name: "the priority",
};
const SNOOZE: HeadControl = {
  find: (head) => head.getByRole("button", { name: /^Snooze / }),
  key: "b",
  name: "Snooze",
};
const COPY_REFERENCE: HeadControl = {
  find: (head) => head.getByRole("button", { name: /^Copy reference / }),
  key: "y",
  name: "Copy reference",
};
const DISMISS: HeadControl = {
  find: (head) => head.getByRole("button", { name: /^Dismiss: / }),
  key: "e",
  name: "Dismiss",
};

/** The controls of an approval request's head, Approve first, which the check's negative control
 *  names; of a question's; and of a reply thread's, whose Open page is what `o` opens. */
const APPROVAL_CONTROLS = [APPROVE, REQUEST_CHANGES, OPEN_PAGE, PRIORITY, SNOOZE, COPY_REFERENCE];
const QUESTION_CONTROLS = [OPEN_PAGE, PRIORITY, SNOOZE, COPY_REFERENCE];
const THREAD_CONTROLS = [{ ...OPEN_PAGE, key: "o" }, COPY_REFERENCE, DISMISS];

/** The reading pane's head, by the element it has been at every build. */
function paneHead(page: Page): Locator {
  return inboxPane(page).locator("header").first();
}

/** What a row reads of the head of the item the pane shows: the box of each control, by name,
 *  and the title's heading, which the merge base does not draw. */
interface HeadReading {
  readonly controls: ReadonlyMap<string, PaneRect>;
  readonly title:
    | { readonly rect: PaneRect; readonly text: string; readonly whole: boolean }
    | undefined;
}

async function readHead(page: Page, controls: readonly HeadControl[]): Promise<HeadReading> {
  const head = paneHead(page);
  const rects = new Map<string, PaneRect>();
  for (const control of controls) {
    rects.set(control.name, await paneRectOf(control.find(head)));
  }
  const title = await head.evaluate((node) => {
    const heading = node.querySelector("h2");
    const pane = node.closest('[data-testid="inbox-pane"]');
    if (heading === null || pane === null) return undefined;
    const own = heading.getBoundingClientRect();
    const frame = pane.getBoundingClientRect();
    // Whole: nothing in the heading is wider than its own box, and nothing draws an ellipsis.
    const cut = [heading, ...heading.querySelectorAll("*")].some(
      (element) =>
        element.scrollWidth > element.clientWidth ||
        getComputedStyle(element).textOverflow === "ellipsis"
    );
    return {
      rect: {
        bottom: own.bottom - frame.top,
        left: own.left - frame.left,
        right: own.right - frame.left,
        top: own.top - frame.top,
      },
      text: heading.textContent ?? "",
      whole: !cut,
    };
  });
  return { controls: rects, title };
}

/** Holds each of `controls` to the same left and top, within 1 px, in two readings, for the step
 *  `step`. */
function expectSamePlaces(
  before: HeadReading,
  after: HeadReading,
  controls: readonly HeadControl[],
  step: (text: string) => string
): void {
  for (const { name } of controls) {
    const was = before.controls.get(name);
    const is = after.controls.get(name);
    if (was === undefined || is === undefined) throw new Error(`${name} was not read twice`);
    expect(Math.abs(is.left - was.left), step(`${name}'s left is the same`)).toBeLessThanOrEqual(1);
    expect(Math.abs(is.top - was.top), step(`${name}'s top is the same`)).toBeLessThanOrEqual(1);
  }
}

/** Holds a reading's title to a heading of its own holding `title` whole, under every control
 *  read with it, for the step `step`. */
function expectTitleWhole(
  reading: HeadReading,
  title: string,
  step: (text: string) => string
): void {
  const heading = reading.title;
  expect(heading, step("the title is a heading of its own")).toBeDefined();
  if (heading === undefined) return;
  expect(heading.text, step("the heading holds the whole title")).toContain(title);
  expect(heading.whole, step("the title is drawn whole, with nothing cut or elided")).toBe(true);
  for (const [name, rect] of reading.controls) {
    expect(rect.bottom, step(`${name} is above the title`)).toBeLessThanOrEqual(
      heading.rect.top + 1
    );
  }
}

/** Holds each of `controls` that has a key to showing it, for the step `step`. */
async function expectKeysShown(
  page: Page,
  controls: readonly HeadControl[],
  step: (text: string) => string
): Promise<void> {
  for (const { find, key, name } of controls) {
    if (key === undefined) continue;
    await expect(find(paneHead(page)).locator("kbd"), step(`${name} shows ${key}`)).toHaveText(key);
  }
}

/** Opens the item `id` in the reading pane and waits for `drawn`, the control that says its head
 *  is the one the row reads. */
async function openInPane(
  page: Page,
  id: string,
  drawn: Locator,
  step: (text: string) => string
): Promise<void> {
  await page.goto(inboxAt(id));
  await expect(selectedRow(page), step("the item is selected")).toHaveAttribute(
    "data-inbox-row",
    id
  );
  await expect(drawn, step("its head is drawn")).toBeVisible();
}

/** The document an approval request the Inbox lists asks about. */
async function approvalArtifactOf(askId: string): Promise<string> {
  const row = (await getInbox()).find((listed) => listed.id === askId);
  const artifactId = row?.approval?.artifact_id;
  if (artifactId === undefined) throw new Error(`the Inbox lists no approval request ${askId}`);
  return artifactId;
}

// SLOW(>=2s): five items seeded, each opened in the reading pane and its head read control by
// control.
test("k-pane-head: on approval requests and questions whose titles are 1 and 300 characters, and on a thread, each control of the head keeps one place above the whole title and shows its key", async ({
  page,
}) => {
  expect(LONG_TITLE).toHaveLength(300);
  await createProject({ key: "CORE", name: "Core" });
  // Each pair shows the same controls: no issue has a priority, and each is the project's
  // default assignee's, so neither draws Assign to me.
  const approvals: string[] = [];
  const questions: string[] = [];
  for (const title of [SHORT_TITLE, LONG_TITLE]) {
    const forApproval = await createIssue({
      force: true,
      project: "CORE",
      spec: "# Rollout\n\nThe mirror hands the lease back.\n",
      title,
    });
    const requested = withOpenedAsk(
      await requestApproval(forApproval.primary_artifact_id, { summary: "The rollout." }, asker)
    );
    approvals.push(requested.ask.id);
    const forQuestion = await createIssue({ force: true, project: "CORE", title });
    questions.push(
      (await createAsk(forQuestion.key, { question: "Which window does it replay?" }, asker)).id
    );
  }
  const threadIssue = await createIssue({ force: true, project: "CORE", title: "Lease handover" });
  const thread = replyRowId(
    await seedCommentThread(threadIssue.key, "Who holds the lease?", "The mirror does.")
  );
  await page.setViewportSize(WIDE);
  const head = paneHead(page);

  let requestChangesRight: number | undefined;
  for (const [what, ids, controls, drawn] of [
    [
      "the approval requests",
      approvals,
      APPROVAL_CONTROLS,
      head.getByRole("button", { disabled: false, exact: true, name: "Request changes" }),
    ],
    ["the questions", questions, QUESTION_CONTROLS, SNOOZE.find(head)],
  ] as const) {
    const step = (text: string) => `k-pane-head: ${what}: ${text}`;
    const [short, long] = ids;
    if (short === undefined || long === undefined) throw new Error(`${what} were not seeded`);
    await openInPane(page, short, drawn, step);
    const shortHead = await readHead(page, controls);
    await openInPane(page, long, drawn, step);
    const longHead = await readHead(page, controls);
    expectSamePlaces(shortHead, longHead, controls, (text) =>
      step(`for the 1- and the 300-character title, ${text}`)
    );
    expectTitleWhole(shortHead, SHORT_TITLE, (text) => step(`the 1-character title: ${text}`));
    expectTitleWhole(longHead, LONG_TITLE, (text) => step(`the 300-character title: ${text}`));
    await expectKeysShown(page, controls, step);
    requestChangesRight ??= longHead.controls.get(REQUEST_CHANGES.name)?.right;
  }

  // The thread draws Dismiss at the end of the row, where Request changes ends.
  const step = (text: string) => `k-pane-head: the reply thread: ${text}`;
  await openInPane(page, thread, DISMISS.find(head), step);
  const threadHead = await readHead(page, THREAD_CONTROLS);
  if (requestChangesRight === undefined) throw new Error("Request changes was not read");
  expect(
    Math.abs((threadHead.controls.get(DISMISS.name)?.right ?? 0) - requestChangesRight),
    step("Dismiss's right edge is Request changes' right edge")
  ).toBeLessThanOrEqual(1);
  expectTitleWhole(threadHead, threadIssue.key, step);
  await expectKeysShown(page, THREAD_CONTROLS, step);
});

// SLOW(>=2s): an approval request seeded, its document's read held while the head is read and
// then let through.
test("k-pane-head: no control of the head moves when the document's read of an approval request returns", async ({
  page,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "# Ledger handover\n\nThe plan.\n",
    title: "Ledger handover",
  });
  const request = withOpenedAsk(
    await requestApproval(issue.primary_artifact_id, { summary: "The handover." }, asker)
  );
  await page.setViewportSize(WIDE);
  const head = paneHead(page);
  const step = (text: string) => `k-pane-head: the late read: ${text}`;
  const path = `/api/v1/artifacts/${issue.primary_artifact_id}`;
  const release = await holdReads(page, [`**${path}`]);
  const requested = page.waitForRequest((sent) => new URL(sent.url()).pathname === path);
  await page.goto(inboxAt(request.ask.id));
  await expect(selectedRow(page), step("the request is selected")).toHaveAttribute(
    "data-inbox-row",
    request.ask.id
  );
  await requested;
  for (const control of [APPROVE, REQUEST_CHANGES]) {
    await expect(
      control.find(head),
      step(`${control.name} is drawn before the read returns`)
    ).toBeVisible();
  }
  const before = await readHead(page, APPROVAL_CONTROLS);
  await release();
  await expect(
    head.getByRole("button", { disabled: false, exact: true, name: "Approve" }),
    step("the read returns and Approve takes a press")
  ).toBeVisible();
  expectSamePlaces(before, await readHead(page, APPROVAL_CONTROLS), APPROVAL_CONTROLS, step);
});

// SLOW(>=2s): a request of a document approved before seeded, its document's read held while the
// head is read and then answered as approved at its latest version.
test("k-pane-head: on a request of a document approved at its latest version, the place at the row's end keeps its box and no other control moves when the read returns", async ({
  page,
}) => {
  // A request of version 2 of a document approved at version 1, which the row's read then
  // answers as approved at version 2: no route of the API leaves an open request of a version
  // already approved.
  const approved = await seedApprovalWithMarks();
  const path = `/api/v1/artifacts/${await approvalArtifactOf(approved.approvalAskId)}`;
  await page.setViewportSize(WIDE);
  const head = paneHead(page);
  const step = (text: string) => `k-pane-head: the approved document: ${text}`;
  const held = Promise.withResolvers<void>();
  await page.route(`**${path}`, async (route) => {
    await held.promise;
    // A read the page gave up on while it was held is answered already, and nobody waits on it.
    await route
      .fetch()
      .then(async (response) => {
        const read = (await response.json()) as { approval?: { latest_version: number } };
        await route.fulfill({
          json:
            read.approval === undefined
              ? read
              : {
                  ...read,
                  approval: {
                    ...read.approval,
                    state: "approved",
                    version: read.approval.latest_version,
                  },
                },
          response,
        });
      })
      .catch(() => undefined);
  });
  const requested = page.waitForRequest((sent) => new URL(sent.url()).pathname === path);
  await page.goto(inboxAt(approved.approvalAskId));
  await expect(selectedRow(page), step("the request is selected")).toHaveAttribute(
    "data-inbox-row",
    approved.approvalAskId
  );
  await requested;
  const place = head.locator("[data-inbox-head-place]");
  await expect(place, step("the head draws its place before the read returns")).toBeVisible();
  const placeBefore = await paneRectOf(place);
  const before = await readHead(page, APPROVAL_CONTROLS);
  held.resolve();
  await expect(
    head.getByRole("button", { name: /^Approved v/ }),
    step("the read returns the document approved at its latest version")
  ).toBeVisible();
  await expect(APPROVE.find(head), step("an approved document draws no Approve")).toHaveCount(0);
  const placeAfter = await paneRectOf(place);
  for (const side of ["left", "top", "right", "bottom"] as const) {
    expect(
      Math.abs(placeAfter[side] - placeBefore[side]),
      step(`the place's ${side} edge is the same`)
    ).toBeLessThanOrEqual(1);
  }
  const drawnBoth = APPROVAL_CONTROLS.filter((control) => control !== APPROVE);
  expectSamePlaces(before, await readHead(page, drawnBoth), drawnBoth, step);
});

// SLOW(>=2s): a request whose version changed six paragraphs, its baseline's read held while the
// head is read the first time.
test("k-pane-head: no control drawn before Next change moves when Next change appears", async ({
  page,
}) => {
  const seed = await seedApprovalWithMarks();
  const artifactId = await approvalArtifactOf(seed.approvalAskId);
  const step = (text: string) => `k-pane-head: Next change: ${text}`;
  // The version the request is marked against: until it is read, nothing is marked.
  const release = await holdReads(page, [`**/api/v1/artifacts/${artifactId}/versions/1`]);
  await page.setViewportSize(WIDE);
  const head = paneHead(page);
  const nextChange = head.getByRole("button", { exact: true, name: "Next change" });
  await openInPane(
    page,
    seed.approvalAskId,
    head.getByRole("button", { disabled: false, exact: true, name: "Request changes" }),
    step
  );
  await expect(nextChange, step("nothing is marked yet, so no Next change is drawn")).toHaveCount(
    0
  );
  const before = await readHead(page, APPROVAL_CONTROLS);
  await release();
  await expect(nextChange, step("the version is marked and Next change appears")).toBeVisible();
  const after = await readHead(page, APPROVAL_CONTROLS);
  expectSamePlaces(before, after, APPROVAL_CONTROLS, step);
  // It appears after the other controls, in reading order: on Copy reference's line to its
  // right, or on a line below it. The controls of one line share their centre line.
  const copy = after.controls.get(COPY_REFERENCE.name);
  const appeared = await paneRectOf(nextChange);
  if (copy === undefined) throw new Error("Copy reference was not read");
  const below = (appeared.top + appeared.bottom) / 2 - (copy.top + copy.bottom) / 2;
  expect(
    below > 1 || (Math.abs(below) <= 1 && appeared.left > copy.right),
    step("Next change comes after Copy reference")
  ).toBe(true);
});

// ---------------------------------------------------------------------------
// k-remap (R14): Settings' Keyboard shortcuts lists every action with its keys, changes them for
// the signed-in person, and every open page of the browser takes the change at once.
// ---------------------------------------------------------------------------

/** Snooze's label, as Settings, `?` and ⌘K write it. */
const SNOOZE_LABEL = "Snooze the focused ask, or every selected ask";

/** The Inbox's Copy reference, as Settings, `?` and ⌘K write it. */
const COPY_REFERENCE_LABEL = "Copy the row's reference";

/** The labels of the actions whose first press is `g`, of which the refusal of `g` names one. */
const FIRST_PRESS_G =
  /Go to (Inbox|Agents|Settings|Documents|project…)|Scroll the open document to its top/;

/** Settings' Keyboard shortcuts section. */
function shortcutsSection(page: Page): Locator {
  return page.getByRole("region", { exact: true, name: "Keyboard shortcuts" });
}

/** The line of the action `label` under `scope` in Settings' Keyboard shortcuts. */
function shortcutLine(page: Page, scope: string, label: string): Locator {
  return shortcutsSection(page)
    .getByRole("region", { exact: true, name: scope })
    .getByRole("listitem", { exact: true, name: label });
}

/** Each action Settings lists, with the keys it draws for it: `inbox.snooze: b h`. */
function settingsKeys(page: Page): Promise<string[]> {
  return shortcutsSection(page)
    .locator("li[data-keymap-action]")
    .evaluateAll((items) =>
      items.map(
        (item) =>
          `${item.getAttribute("data-keymap-action")}: ${[...item.querySelectorAll("kbd")]
            .map((kbd) => kbd.textContent)
            .join(" ")}`
      )
    );
}

/** The line of the action `label` under `scope` in the open `?` help. */
function helpLine(page: Page, scope: string, label: string): Locator {
  return page
    .getByRole("dialog", { name: "Keyboard shortcuts" })
    .getByRole("region", { exact: true, name: scope })
    .getByRole("listitem")
    .filter({ hasText: label });
}

/** Opens the `?` help over the focused row, holds it to `check`, and closes it again. */
async function inHelp(page: Page, check: () => Promise<void>): Promise<void> {
  const help = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await page.keyboard.press("?");
  await expect(help).toBeVisible();
  await check();
  await page.keyboard.press("Escape");
  await expect(help).toHaveCount(0);
}

/** Selects the ask `askId` on the Inbox `page` shows, and focuses its row, where the Inbox's keys
 *  act on it. */
async function selectAsk(page: Page, askId: string): Promise<Locator> {
  const row = page.locator(`[data-inbox-row="${askId}"]`);
  await row.locator("time").first().click();
  await expect(row).toHaveAttribute("data-inbox-selected", "true");
  await row.focus();
  return row;
}

// SLOW(>=2s): two pages of one browser, six keys recorded on one (each waiting out the second
// press), and each read on the other through its keys, `?`, ⌘K and the rail, then both reloaded.
test("k-remap: a key changed in Settings works at once on the Inbox in another page, a key another action holds is refused, and Reset all puts every key back", async ({
  browser,
}) => {
  const step = (text: string) => `k-remap: ${text}`;
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Ledger rollout" });
  const ask = await createAsk(issue.key, { question: "Ship the ledger rollout?" }, asker);
  const context = await asUser(browser, "alice", { viewport: WIDE });
  try {
    const settings = await context.newPage();
    const inbox = await context.newPage();
    const copied = await recordClipboard(inbox);

    // On Settings: every action, by its scope, whether or not its page is open.
    await settings.goto("/settings");
    await expect(
      shortcutsSection(settings),
      step("Settings has a Keyboard shortcuts section")
    ).toBeVisible();
    const snooze = shortcutLine(settings, "Inbox", SNOOZE_LABEL);
    await expect(snooze.locator("kbd"), step("Settings lists Snooze with b and h")).toHaveText([
      "b",
      "h",
    ]);
    await expect(
      shortcutLine(settings, "Board", "Move card down").locator("kbd"),
      step("Settings lists the Board's actions, though no Board is open")
    ).toHaveText(["Shift+J"]);
    const agentsSearch = shortcutLine(settings, "Agents", "Search agents");
    await expect(
      agentsSearch.locator("kbd"),
      step("Settings lists the Agents page's actions, though no Agents page is open")
    ).toHaveText(["/"]);
    await expect(
      agentsSearch,
      step("the Agents page's own / says that Search does not work there")
    ).toContainText("Search does not work on the Agents page.");
    const defaults = await settingsKeys(settings);
    expect(defaults.length, step("Settings lists every action of the keymap")).toBeGreaterThan(100);

    // On the Inbox, before any change: y copies the ask's reference.
    await inbox.goto("/");
    let row = await selectAsk(inbox, ask.id);
    await inbox.keyboard.press("y");
    await expect
      .poll(async () => (await copied()).length, { message: step("y copies before the change") })
      .toBe(1);

    // b becomes Shift+B on Settings, and the Inbox takes it with no reload.
    await snooze.getByRole("button", { exact: true, name: "Change b" }).click();
    const recorder = settings.getByRole("button", { name: "Press the new key" });
    await expect(recorder, step("Change b waits for the new key")).toBeFocused();
    await settings.keyboard.press("Shift+B");
    // The recorder draws a press it holds as a key too, so the change is read once it is kept.
    await expect(
      snooze.getByRole("button", { exact: true, name: "Change Shift+B" }),
      step("b changes to Shift+B")
    ).toBeVisible();
    await expect(snooze.locator("kbd"), step("Snooze's keys are Shift+B and h")).toHaveText([
      "Shift+B",
      "h",
    ]);
    // The Inbox takes the change when the browser tells it, with no reload; the pane head's
    // Snooze shows it as it does.
    const headSnooze = paneHead(inbox).getByRole("button", { name: /^Snooze / });
    await expect(
      headSnooze.locator("kbd"),
      step("the pane head's Snooze shows Shift+B")
    ).toHaveText("Shift+B");
    const menu = inbox.getByRole("menu");
    await row.focus();
    await inbox.keyboard.press("Shift+B");
    await expect(menu, step("on the Inbox Shift+B opens the snooze menu")).toBeVisible();
    await inbox.keyboard.press("Escape");
    await expect(menu).toHaveCount(0);
    await expect(row, step("Escape puts the focus back on the row")).toBeFocused();
    await inbox.keyboard.press("b");
    await expect(menu, step("on the Inbox b opens nothing")).toHaveCount(0);
    await inbox.keyboard.press("h");
    await expect(menu, step("on the Inbox h still opens the snooze menu")).toBeVisible();
    await inbox.keyboard.press("Escape");
    await expect(menu).toHaveCount(0);
    await inHelp(inbox, async () => {
      await expect(
        helpLine(inbox, "Inbox", SNOOZE_LABEL).locator("kbd"),
        step("? lists Shift+B and h for Snooze")
      ).toHaveText(["Shift+B", "h"]);
    });
    const squeezed = (text: string) => text.replace(/\s+/g, "");
    expect(await commandBarRows(inbox), step("⌘K shows Shift+B beside Snooze")).toContain(
      squeezed(`${SNOOZE_LABEL} Shift+B or h`)
    );

    // Keys another action holds are refused, each naming it; c is taken, with a notice.
    await snooze.getByRole("button", { exact: true, name: "Add key" }).click();
    await expect(recorder, step("Add key waits for the new key")).toBeFocused();
    const refusal = snooze.getByRole("alert");
    await settings.keyboard.press("e");
    await expect(refusal, step("e is refused, and the refusal names Dismiss")).toContainText(
      "Dismiss"
    );
    await settings.keyboard.press("g");
    await expect(
      refusal,
      step("g is refused, and the refusal names an action whose first press is g")
    ).toHaveText(FIRST_PRESS_G);
    await settings.keyboard.press("1");
    await expect(refusal, step("1 is refused")).toHaveText(/^1 /);
    await settings.keyboard.press("c");
    await expect(
      snooze.getByRole("button", { exact: true, name: "Change c" }),
      step("c is taken")
    ).toBeVisible();
    await expect(refusal, step("c is taken, and no refusal shows")).toHaveCount(0);
    await expect(snooze.locator("kbd"), step("c is one more key of Snooze")).toHaveText([
      "Shift+B",
      "h",
      "c",
    ]);
    await expect(
      snooze,
      step("c is taken, and Settings says that Create issue does not work on the Inbox")
    ).toContainText("Create issue does not work on the Inbox.");

    // Go to Priorities takes g then r, two presses within one second.
    const priorities = shortcutLine(settings, "Global", "Go to Priorities");
    await priorities.getByRole("button", { exact: true, name: "Add key" }).click();
    await expect(recorder, step("Go to Priorities' Add key waits for the new key")).toBeFocused();
    await settings.keyboard.press("g");
    await settings.keyboard.press("r");
    await expect(
      priorities.getByRole("button", { exact: true, name: "Change g then r" }),
      step("g then r within one second is one key of two presses")
    ).toBeVisible();
    await expect(priorities.locator("kbd"), step("Go to Priorities' key is g then r")).toHaveText([
      "g",
      "r",
    ]);
    await row.focus();
    await inHelp(inbox, async () => {
      await expect(
        helpLine(inbox, "Global", "Go to Priorities").locator("kbd"),
        step("on the Inbox ? lists g r for Go to Priorities")
      ).toHaveText(["g", "r"]);
    });
    await inbox.keyboard.press("g");
    await inbox.keyboard.press("r");
    await expect(inbox, step("on the Inbox g then r opens the Priorities page")).toHaveURL(
      /\/priorities$/
    );
    await inbox.goto("/");
    row = await selectAsk(inbox, ask.id);

    // Create issue takes n.
    const create = shortcutLine(settings, "Global", "Create issue");
    await create.getByRole("button", { exact: true, name: "Change c" }).click();
    await expect(recorder, step("Change c waits for the new key")).toBeFocused();
    await settings.keyboard.press("n");
    await expect(
      create.getByRole("button", { exact: true, name: "Change n" }),
      step("Create issue's c changes to n")
    ).toBeVisible();
    const newIssue = inbox.getByRole("button", { name: /^New issue/ });
    await expect(newIssue.locator("kbd"), step("the rail's New issue shows n")).toHaveText("n");
    await expect(newIssue, step("the rail's New issue is pressed with N")).toHaveAttribute(
      "aria-keyshortcuts",
      "N"
    );
    const createDialog = inbox.getByRole("dialog", { name: "Create issue" });
    await row.focus();
    await inbox.keyboard.press("n");
    await expect(createDialog, step("on the Inbox n opens the create dialog")).toBeVisible();
    await inbox.keyboard.press("Escape");
    await expect(createDialog).toHaveCount(0);

    // Copy reference loses its only key: y does nothing, and ⌘K still lists the action.
    const copyReference = shortcutLine(settings, "Inbox", COPY_REFERENCE_LABEL);
    await copyReference.getByRole("button", { exact: true, name: "Remove y" }).click();
    await expect(copyReference.locator("kbd"), step("Copy reference has no key")).toHaveCount(0);
    await expect(
      paneHead(inbox)
        .getByRole("button", { name: /^Copy reference / })
        .locator("kbd"),
      step("the pane head's Copy reference shows no key")
    ).toHaveCount(0);
    // The recorder starts again on each page load, so it is read just before the press.
    const copiedBefore = (await copied()).length;
    await row.focus();
    await inbox.keyboard.press("y");
    expect(await copied(), step("on the Inbox y copies nothing")).toHaveLength(copiedBefore);
    expect(await commandBarRows(inbox), step("⌘K still lists Copy reference")).toContain(
      squeezed(COPY_REFERENCE_LABEL)
    );

    // A reload of both pages keeps each change.
    await settings.reload();
    await expect(snooze.locator("kbd"), step("a reload keeps Snooze's keys")).toHaveText([
      "Shift+B",
      "h",
      "c",
    ]);
    await expect(
      priorities.locator("kbd"),
      step("a reload keeps Go to Priorities' g r")
    ).toHaveText(["g", "r"]);
    await expect(create.locator("kbd"), step("a reload keeps Create issue's n")).toHaveText(["n"]);
    await expect(
      copyReference.locator("kbd"),
      step("a reload keeps Copy reference with no key")
    ).toHaveCount(0);
    await inbox.reload();
    row = await selectAsk(inbox, ask.id);
    await inHelp(inbox, async () => {
      await expect(
        helpLine(inbox, "Inbox", SNOOZE_LABEL).locator("kbd"),
        step("after a reload ? lists Shift+B, h and c for Snooze")
      ).toHaveText(["Shift+B", "h", "c"]);
      await expect(
        helpLine(inbox, "Global", "Go to Priorities").locator("kbd"),
        step("after a reload ? lists g r for Go to Priorities")
      ).toHaveText(["g", "r"]);
      await expect(
        helpLine(inbox, "Global", "Create issue").locator("kbd"),
        step("after a reload ? lists n for Create issue")
      ).toHaveText(["n"]);
      await expect(
        helpLine(inbox, "Inbox", COPY_REFERENCE_LABEL),
        step("after a reload ? lists no key for Copy reference")
      ).toHaveCount(0);
    });

    // Reset all puts back each key, on Settings and on the Inbox.
    await shortcutsSection(settings)
      .getByRole("button", { exact: true, name: "Reset all" })
      .click();
    await expect
      .poll(() => settingsKeys(settings), { message: step("Reset all puts back each key") })
      .toEqual(defaults);
    await expect(newIssue.locator("kbd"), step("after Reset all the rail shows c")).toHaveText("c");
    await inHelp(inbox, async () => {
      await expect(
        helpLine(inbox, "Inbox", SNOOZE_LABEL).locator("kbd"),
        step("after Reset all ? lists b and h for Snooze")
      ).toHaveText(["b", "h"]);
      await expect(
        helpLine(inbox, "Global", "Create issue").locator("kbd"),
        step("after Reset all ? lists c for Create issue")
      ).toHaveText(["c"]);
      await expect(
        helpLine(inbox, "Global", "Go to Priorities"),
        step("after Reset all ? lists no key for Go to Priorities")
      ).toHaveCount(0);
      await expect(
        helpLine(inbox, "Inbox", COPY_REFERENCE_LABEL).locator("kbd"),
        step("after Reset all ? lists y for Copy reference")
      ).toHaveText(["y"]);
    });
    await inbox.keyboard.press("b");
    await expect(menu, step("after Reset all b opens the snooze menu")).toBeVisible();
    await inbox.keyboard.press("Escape");
    await expect(menu).toHaveCount(0);

    // The help has a link that opens the section.
    await inbox.keyboard.press("?");
    const help = inbox.getByRole("dialog", { name: "Keyboard shortcuts" });
    await help.getByRole("link", { name: /Settings/ }).click();
    await expect(inbox, step("the help's link opens Settings at the section")).toHaveURL(
      /\/settings#keyboard-shortcuts$/
    );
    await expect(help, step("the help closes on the move")).toHaveCount(0);
    await expect(shortcutsSection(inbox), step("the section is in view")).toBeInViewport();
    await expect(shortcutsSection(inbox), step("the section has the focus")).toBeFocused();
  } finally {
    await context.close();
  }
});

// ---------------------------------------------------------------------------
// k-issue-tabs (R21): on today's issue page `]` and `[` walk the tab strip in its order and wrap
// at either end, the `t` chords stay, and the chord hint lists each second key of `t` and `g` with
// its action until the second press or the end of the one second; Escape in the spec the reader
// edits takes the focus out of the editor, so the page's keys work, and Tab back into the document
// puts the caret where it was.
// ---------------------------------------------------------------------------

/** The issue page's tabs in the order of its strip: each tab's address segment and its name. */
const ISSUE_TABS = [
  ["spec", "Spec"],
  ["verification", "Verification"],
  ["plan", "Plan"],
  ["conversation", "Conversation"],
  ["children", "Children"],
  ["artifacts", "Artifacts"],
  ["activity", "Activity"],
  ["decisions", "Decisions"],
  ["collaborators", "Collaborators"],
] as const;

type IssueTabAt = (typeof ISSUE_TABS)[number];

/** Each key the chord hint lists after `t`, with the action it names. */
const TAB_CHORD_HINT = [
  "s Spec tab",
  "k Verification tab",
  "p Plan tab",
  "c Conversation tab",
  "h Children tab",
  "a Artifacts tab",
  "v Activity tab",
  "d Decisions tab",
  "o Collaborators tab",
];

/** Each key the chord hint lists after `g`, with the action it names. */
const GO_CHORD_HINT = [
  "i Go to Inbox",
  "a Go to Agents",
  "s Go to Settings",
  "d Go to Documents",
  "p Go to project…",
];

/** The paragraph and the list item of k-issue-tabs' spec the rows click into. */
const TABS_PARAGRAPH = "The ledger keeps one clock for every mirror.";
const TABS_ITEM = "The second mirror replays the ledger.";

/** An open issue of CORE whose spec holds a paragraph and a bulleted list, and whose plan and
 *  Verification document each hold text. */
async function seedIssueTabs(): Promise<{ key: string; title: string }> {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger tabs";
  const issue = await createIssue({
    project: "CORE",
    spec: `# ${title}\n\n${TABS_PARAGRAPH}\n\n- The first mirror reads the ledger.\n- ${TABS_ITEM}\n`,
    title,
  });
  await createIssueArtifact(issue.key, {
    content: "# Plan\n\nRead the ledger, then each mirror.\n",
    name: "plan.md",
  });
  await createIssueArtifact(issue.key, {
    content: "# Verification\n\nEach mirror agrees with the ledger.\n",
    name: "verification.md",
  });
  return { key: issue.key, title };
}

/** Opens the issue's Spec tab with the focus on the page itself, outside the document. */
async function openSpecTab(page: Page, key: string, title: string): Promise<void> {
  await page.goto(`/issues/${key}/spec`);
  await expect(
    page.getByTestId("issue-header").getByRole("heading", { level: 1, name: title })
  ).toBeVisible();
  await page.locator("body").focus();
}

/** Waits until the strip has `tab` selected and the address names it. */
async function expectTab(page: Page, key: string, tab: IssueTabAt, message: string): Promise<void> {
  const [segment, name] = tab;
  await expect(
    page
      .getByRole("tablist", { name: "Issue detail" })
      .getByRole("tab", { name: new RegExp(`^${name}`) }),
    message
  ).toHaveAttribute("aria-selected", "true");
  await expect(page, `${message}, and the address follows`).toHaveURL(
    new RegExp(`/issues/${key}/${segment}$`)
  );
}

/** Each line the chord hint lists, its second key and the action it names, in key order. */
function chordHint(page: Page): Promise<string[]> {
  return page
    .getByTestId("chord-indicator")
    .getByRole("listitem")
    .evaluateAll((items) =>
      items
        .map(
          (item) =>
            `${item.querySelector("kbd")?.textContent} ${item.querySelector("span")?.textContent}`
        )
        .sort()
    );
}

// SLOW(>=2s): the issue page loaded, every tab of its strip opened twice, and a chord hint left
// to its one second.
test("k-issue-tabs: ] and [ walk the tab strip in its order and wrap, the t chords stay, and the chord hint lists each second key of t and g with its action", async ({
  page,
}) => {
  const seed = await seedIssueTabs();
  const step = (text: string) => `k-issue-tabs: ${text}`;
  const hint = page.getByTestId("chord-indicator");
  await page.setViewportSize(WIDE);
  await openSpecTab(page, seed.key, seed.title);

  for (const tab of [...ISSUE_TABS.slice(1), ISSUE_TABS[0]]) {
    await page.keyboard.press("]");
    await expectTab(page, seed.key, tab, step(`] selects ${tab[1]}`));
  }
  for (const tab of [...ISSUE_TABS].reverse()) {
    await page.keyboard.press("[");
    await expectTab(page, seed.key, tab, step(`[ selects ${tab[1]}`));
  }

  await page.keyboard.press("t");
  await expect(hint.locator("kbd").first(), step("t shows the chord hint")).toHaveText("t");
  await expect
    .poll(() => chordHint(page), {
      message: step("after t the chord hint lists each tab's key with its tab"),
    })
    .toEqual([...TAB_CHORD_HINT].sort());
  await page.keyboard.press("c");
  await expect(hint, step("c ends the chord and its hint")).toHaveCount(0);
  await expectTab(
    page,
    seed.key,
    ["conversation", "Conversation"],
    step("t c selects Conversation")
  );

  await page.locator("body").focus();
  await page.keyboard.press("g");
  await expect
    .poll(() => chordHint(page), {
      message: step("after g the chord hint lists each page's key with its page"),
    })
    .toEqual([...GO_CHORD_HINT].sort());
  await expect(
    hint,
    step("the hint goes at the end of the one second, with no second press")
  ).toHaveCount(0, { timeout: 5_000 });
  await expect(page, step("a g with no second press goes nowhere")).toHaveURL(
    new RegExp(`/issues/${seed.key}/conversation$`)
  );
});

/** Whether the focus is inside the Spec tab's editor. */
function focusInSpec(page: Page): Promise<boolean> {
  return specEditor(page).evaluate((root) => root.contains(document.activeElement));
}

/** The Spec tab's editor. */
function specEditor(page: Page): Locator {
  return page
    .getByRole("tabpanel", { name: "Spec" })
    .getByRole("textbox", { name: "Document editor" });
}

/** Where the page's caret stands: the text of its node and its offset there. */
interface Caret {
  readonly collapsed: boolean;
  readonly offset: number;
  readonly text: string;
}

/** Where the page's caret stands now, or null with no selection. */
function caretOf(page: Page): Promise<Caret | null> {
  return page.evaluate(() => {
    const selection = window.getSelection();
    if (selection === null || selection.rangeCount === 0) return null;
    return {
      collapsed: selection.isCollapsed,
      offset: selection.anchorOffset,
      text: selection.anchorNode?.textContent ?? "",
    };
  });
}

// SLOW(>=2s): the spec opened and connected, two tabs left and come back to twice, and Tab walked
// back into the document.
test("k-issue-tabs: Escape in the spec the reader edits takes the focus out of the editor so ] works, changes no text, and Tab back in puts the caret where it was", async ({
  page,
}) => {
  const seed = await seedIssueTabs();
  const step = (text: string) => `k-issue-tabs: ${text}`;
  await page.setViewportSize(WIDE);
  await openSpec(page, seed.key, TABS_PARAGRAPH);
  const before = (await getIssueArtifactText(seed.key, "spec")).markdown;
  const editor = specEditor(page);

  let caret: Caret | null = null;
  for (const [text, where] of [
    [TABS_PARAGRAPH, "a paragraph"],
    [TABS_ITEM, "an item of the list"],
  ] as const) {
    await editor.getByText(text).click();
    await expect(editor, step(`a click puts the caret in ${where}`)).toBeFocused();
    caret = await caretOf(page);
    expect(caret?.text, step(`the caret stands in ${where}`)).toBe(text);
    await page.keyboard.press("Escape");
    expect(
      await focusInSpec(page),
      step(`Escape in ${where} takes the focus out of the editor`)
    ).toBe(false);
    await page.keyboard.press("]");
    await expectTab(
      page,
      seed.key,
      ["verification", "Verification"],
      step(`after Escape in ${where}, ] selects the next tab`)
    );
    expect(
      (await getIssueArtifactText(seed.key, "spec")).markdown,
      step(`Escape in ${where} leaves the spec's Markdown as it was`)
    ).toBe(before);
    await page.keyboard.press("[");
    await expectTab(page, seed.key, ["spec", "Spec"], step("[ goes back to the Spec tab"));
  }

  // Back on the Spec tab, Tab goes on from where the focus last was until it is in the document.
  for (let press = 0; press < TAB_PRESSES && !(await focusInSpec(page)); press += 1) {
    await page.keyboard.press("Tab");
  }
  expect(await focusInSpec(page), step("Tab goes back into the document")).toBe(true);
  await expect
    .poll(() => caretOf(page), {
      message: step("Tab back into the document puts the caret where it was"),
    })
    .toEqual(caret);
  expect(
    (await getIssueArtifactText(seed.key, "spec")).markdown,
    step("the spec's Markdown is as it was")
  ).toBe(before);
});

// ---------------------------------------------------------------------------
// k-threads (R22): on an issue's Conversation tab `j` and `k` walk the turns, an open thread's
// replies among them; Enter and `o` open and close a thread or a fold with the focus kept on the
// turn; `r` puts the caret in the thread's reply box, and Escape in the empty box gives the focus
// back; `y` and Shift+Y copy a turn's reference and its web address, and nothing on a handoff.
// Each comment, reply and message of the issue has a Copy reference that Tab reaches, on the
// Conversation tab, in the margin of the spec, and in the Inbox's reading pane, cards and drawer.
// ---------------------------------------------------------------------------

/** The text of k-threads' spec that its comment thread is anchored to. */
const THREAD_ANCHOR = "The mirror renews the ledger's lease every hour.";

/** k-threads' issue, and the ids of what its conversation holds, each list oldest first. */
interface ThreadSeed {
  readonly key: string;
  readonly title: string;
  readonly message: string;
  readonly messageReply: string;
  readonly comment: string;
  readonly commentReplies: readonly string[];
  readonly ask: string;
  readonly askReplies: readonly string[];
  readonly handoff: string;
}

/** An issue of CORE whose conversation holds the reader's message with bob's reply, the reader's
 *  comment anchored in the spec with three of bob's replies, the reader's question with four of
 *  them, and a handoff, so the Inbox lists each of the three threads. Each thread is marked read
 *  through its newest reply: the Inbox and the Conversation then fold the same earlier replies
 *  from their first draw, and no read mark a row's dwell writes folds one under the keys. */
async function seedThreads(): Promise<ThreadSeed> {
  await createProject({ key: "CORE", name: "Core" });
  const title = "Ledger lease";
  const issue = await createIssue({
    project: "CORE",
    spec: `# ${title}\n\n${THREAD_ANCHOR}\n`,
    title,
  });
  const bob = { login: "bob" };
  const message = await createMessage(issue.key, { body: "Who holds the ledger's lease?" });
  const messageReply = await createMessage(
    issue.key,
    { body: "The mirror holds it.", in_reply_to: message.id },
    bob
  );
  const comment = await createComment(issue.key, {
    anchor: { artifact: "spec", quote: THREAD_ANCHOR },
    body: "Is an hour long enough?",
  });
  const commentReplies = [];
  for (const body of ["First thread reply.", "Second thread reply.", "Third thread reply."]) {
    commentReplies.push(await createComment(issue.key, { body, reply_to: comment.id }, bob));
  }
  const ask = await createAsk(issue.key, { question: "Do we shorten the lease?" });
  const askReplies = [];
  for (const body of ["First ask reply.", "Second ask reply.", "Third ask reply.", "Fourth."]) {
    askReplies.push(await createComment(issue.key, { ask_id: ask.id, body }, bob));
  }
  const handoff = await createHandoff(issue.key, "implement", { summary: "The lease renews." });
  const newestComment = commentReplies.at(-1);
  const newestAskReply = askReplies.at(-1);
  if (newestComment === undefined || newestAskReply === undefined) {
    throw new Error("k-threads: the replies were seeded");
  }
  await markThreadRead(message.id, messageReply.created_at);
  await markThreadRead(comment.id, newestComment.created_at);
  await markThreadRead(ask.id, newestAskReply.created_at);
  return {
    ask: ask.id,
    askReplies: askReplies.map(({ id }) => id),
    comment: comment.id,
    commentReplies: commentReplies.map(({ id }) => id),
    handoff: handoff.id,
    key: issue.key,
    message: message.id,
    messageReply: messageReply.id,
    title,
  };
}

/** The Conversation tab of `seed`'s issue at 1440x900, every conversation drawn, the focus on
 *  the page itself; answers the list of its turns. */
async function openConversation(page: Page, seed: ThreadSeed): Promise<Locator> {
  await page.setViewportSize(WIDE);
  await page.goto(`/issues/${seed.key}/conversation`);
  const turns = page.getByRole("list", { name: "Conversation turns" });
  for (const turn of [
    `handoff:${seed.handoff}`,
    `ask:${seed.ask}`,
    `comment:${seed.comment}`,
    `message:${seed.message}`,
    `message:${seed.messageReply}`,
  ]) {
    await expect(turns.locator(`[data-turn="${turn}"]`)).toBeVisible();
  }
  await page.locator("body").focus();
  return turns;
}

/** The Copy reference names of every comment, reply and message of `seed`'s three threads, by
 *  thread. */
function threadCopyNames(seed: ThreadSeed): Record<"ask" | "comment" | "message", string[]> {
  const name = (kind: "ask" | "comment" | "message", id: string) =>
    `Copy reference dispatch://${seed.key}/${kind}/${id}`;
  return {
    ask: [name("ask", seed.ask), ...seed.askReplies.map((id) => name("comment", id))],
    comment: [
      name("comment", seed.comment),
      ...seed.commentReplies.map((id) => name("comment", id)),
    ],
    message: [name("message", seed.message), name("message", seed.messageReply)],
  };
}

/** Presses Tab from where the focus is until it has been inside `scope` and left it again, at
 *  most `TAB_PRESSES` times, and answers the name of each control inside `scope` it stopped on:
 *  its `aria-label`, else its text. */
async function tabNames(page: Page, scope: Locator): Promise<string[]> {
  const names: string[] = [];
  for (let press = 0; press < TAB_PRESSES; press += 1) {
    await page.keyboard.press("Tab");
    // The focus moves inside the Tab press itself, so one read once it returns is settled.
    const name = await scope.evaluate((root) => {
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !root.contains(active)) return null;
      return active.getAttribute("aria-label") ?? active.innerText.trim();
    });
    // That settled read says whether the walk is still inside the scope, or has left it.
    if (name !== null) {
      names.push(name);
    } else if (names.length > 0) {
      break;
    }
  }
  return names;
}

/** Holds that Tab, walking `scope` from where the focus is, reaches each control `expected`
 *  names, in `where`. */
async function expectTabReaches(
  page: Page,
  scope: Locator,
  expected: readonly string[],
  where: string
): Promise<void> {
  const names = await tabNames(page, scope);
  for (const name of expected) {
    expect(names, `k-threads: ${where}, Tab reaches ${name}`).toContain(name);
  }
}

// SLOW(>=2s): an issue with four conversations and eight replies seeded, the Conversation tab
// walked by its keys and five copies read back.
test("k-threads: j and k walk the turns and an open thread's replies, Enter and o open and close a thread with the focus kept on the turn, r reaches its reply box and Escape comes back, and y and Shift+Y copy a turn's reference and address", async ({
  page,
}) => {
  const copied = await recordClipboard(page);
  const seed = await seedThreads();
  const step = (text: string) => `k-threads: ${text}`;
  const turns = await openConversation(page, seed);
  const turn = (id: string) => turns.locator(`[data-turn="${id}"]`);
  const askReply = (id: string) =>
    turns.locator(`[data-reply][data-turn-ref="dispatch://${seed.key}/comment/${id}"]`);
  const commentTurn = turn(`comment:${seed.comment}`);
  const replies = commentTurn.locator("[data-reply]");
  const origin = new URL(page.url()).origin;
  const lastCopy = async () => (await copied()).at(-1);

  // Newest activity first: the handoff, the question with its two newest replies (the fold holds
  // the two before them), the comment with its thread closed, the message and its reply.
  const walk: readonly (readonly [Locator, string])[] = [
    [turn(`handoff:${seed.handoff}`), "the handoff"],
    [turn(`ask:${seed.ask}`), "the question"],
    [askReply(seed.askReplies[2] ?? ""), "the question's third reply"],
    [askReply(seed.askReplies[3] ?? ""), "the question's fourth reply"],
    [commentTurn, "the comment"],
    [turn(`message:${seed.message}`), "the message"],
    [turn(`message:${seed.messageReply}`), "the message's reply"],
  ];
  for (const [row, what] of walk) {
    await page.keyboard.press("j");
    await expect(row, step(`j focuses ${what}`)).toBeFocused();
  }
  await expect(
    turns.locator("[data-turn], [data-reply]"),
    step("j walked every turn and reply the tab draws")
  ).toHaveCount(walk.length);
  await page.keyboard.press("k");
  await expect(turn(`message:${seed.message}`), step("k goes back to the message")).toBeFocused();
  await page.keyboard.press("k");
  await expect(commentTurn, step("k goes back to the comment")).toBeFocused();

  // The collapsed comment thread: Enter opens it with the focus kept on the turn, and `j` then
  // walks its replies.
  await expect(replies, step("the comment's thread is closed")).toHaveCount(0);
  await page.keyboard.press("Enter");
  await expect(replies, step("Enter opens the comment's thread")).toHaveCount(3);
  await expect(commentTurn, step("Enter keeps the focus on the comment's turn")).toBeFocused();
  await page.keyboard.press("j");
  await expect(replies.first(), step("j then focuses the thread's first reply")).toBeFocused();
  await page.keyboard.press("r");
  await expect(
    commentTurn.getByRole("textbox", { name: "Reply" }),
    step("r on the reply puts the caret in the thread's reply box")
  ).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(
    replies.first(),
    step("Escape in the empty box puts the focus back on the reply")
  ).toBeFocused();
  await expect(replies, step("Escape keeps the thread open")).toHaveCount(3);
  await page.keyboard.press("k");
  await expect(commentTurn, step("k goes back to the comment's turn")).toBeFocused();
  await page.keyboard.press("o");
  await expect(replies, step("o on the thread closes it")).toHaveCount(0);
  await expect(commentTurn, step("o keeps the focus on the comment's turn")).toBeFocused();

  // `y` and Shift+Y on the comment: its reference and its page's address.
  await page.keyboard.press("y");
  await expect
    .poll(lastCopy, { message: step("y copies the comment's reference") })
    .toBe(`dispatch://${seed.key}/comment/${seed.comment}`);
  await page.keyboard.press("Shift+Y");
  await expect
    .poll(lastCopy, { message: step("Shift+Y copies the comment's web address") })
    .toBe(`${origin}/issues/${seed.key}/comments/${seed.comment}`);

  // The handoff: Enter opens its fields, and it has no reference to copy. The next copy, `y` on
  // the question, is then the first copy since the handoff's keys.
  const handoff = turn(`handoff:${seed.handoff}`);
  for (const [row, what] of [...walk.slice(0, 4)].reverse()) {
    await page.keyboard.press("k");
    await expect(row, step(`k goes back to ${what}`)).toBeFocused();
  }
  await page.keyboard.press("Enter");
  await expect(
    handoff.getByRole("button", { name: "Hide fields" }),
    step("Enter on the handoff opens its fields")
  ).toBeVisible();
  await expect(handoff, step("Enter keeps the focus on the handoff")).toBeFocused();
  const copiesBefore = (await copied()).length;
  await page.keyboard.press("y");
  await page.keyboard.press("Shift+Y");
  await page.keyboard.press("j");
  await expect(turn(`ask:${seed.ask}`), step("j goes on to the question")).toBeFocused();
  await page.keyboard.press("y");
  await expect
    .poll(async () => (await copied()).slice(copiesBefore), {
      message: step("y and Shift+Y on the handoff copy nothing, and y on the question copies it"),
    })
    .toEqual([`dispatch://${seed.key}/ask/${seed.ask}`]);

  // On the third reply of the comment's thread, `y` copies that reply's reference.
  for (const [row, what] of walk.slice(2, 5)) {
    await page.keyboard.press("j");
    await expect(row, step(`j focuses ${what}`)).toBeFocused();
  }
  await page.keyboard.press("Enter");
  await expect(replies, step("Enter opens the comment's thread again")).toHaveCount(3);
  await page.keyboard.press("j");
  await page.keyboard.press("j");
  await page.keyboard.press("j");
  await expect(replies.nth(2), step("j focuses the thread's third reply")).toBeFocused();
  await page.keyboard.press("y");
  await expect
    .poll(lastCopy, { message: step("y on the third reply copies its reference") })
    .toBe(`dispatch://${seed.key}/comment/${seed.commentReplies[2]}`);
});

// SLOW(>=2s): an issue with four conversations seeded, the Conversation tab and the spec's margin
// each walked by Tab.
test("k-threads: each comment, reply and message on the Conversation tab and in the spec's margin has a Copy reference that Tab reaches", async ({
  page,
}) => {
  const seed = await seedThreads();
  const names = threadCopyNames(seed);

  // The Conversation tab, with the comment's thread and the question's earlier replies open, walked
  // from its filter, above the turns.
  const turns = await openConversation(page, seed);
  await turns
    .locator(`[data-turn="comment:${seed.comment}"]`)
    .getByRole("button", { name: "Expand thread" })
    .click();
  await turns
    .locator(`[data-turn="ask:${seed.ask}"]`)
    .getByRole("button", { name: "Show 2 earlier replies" })
    .click();
  await expect(turns.locator("[data-reply]")).toHaveCount(8);
  await page
    .getByRole("group", { name: "Conversations" })
    .getByRole("button", { name: "All" })
    .click();
  await expectTabReaches(
    page,
    turns,
    [...names.message, ...names.comment, ...names.ask],
    "on the Conversation tab"
  );

  // The spec's margin: the comment's card, open, walked from the text it quotes.
  await page.goto(`/issues/${seed.key}/spec`);
  const card = page.locator(`[data-margin-item="${seed.comment}"]`);
  await expectTabReaches(page, card, [names.comment[0] ?? ""], "on the margin's closed card");
  await card.getByRole("button", { expanded: false }).click();
  await expect(card.locator("[data-reply]")).toHaveCount(3);
  await card.locator("blockquote").first().click();
  await expectTabReaches(page, card, names.comment, "on the margin's open card");
});

// SLOW(>=2s): an issue with four conversations seeded, and the Inbox's reading pane, cards and
// drawer each walked by Tab over its three threads.
test("k-threads: each comment, reply and message of an issue in the Inbox's reading pane, cards and drawer has a Copy reference that Tab reaches", async ({
  page,
}) => {
  const seed = await seedThreads();
  const names = threadCopyNames(seed);
  // Each thread, the fold its older replies are drawn behind, and its Copy reference names.
  const threads = [
    [seed.message, undefined, names.message, "the message's thread"],
    [seed.comment, "Show 1 earlier reply", names.comment, "the comment's thread"],
    [seed.ask, "Show 2 earlier replies", names.ask, "the question's thread"],
  ] as const;

  // The reading pane: each thread selected, its fold opened, and its column walked after `l`.
  await page.setViewportSize(WIDE);
  await page.goto("/");
  for (const [thread, fold, expected, what] of threads) {
    await selectRow(page, replyRowId(thread));
    if (fold !== undefined) await paneScroller(page).getByRole("button", { name: fold }).click();
    await inboxRow(page, replyRowId(thread)).locator("time").first().click();
    await page.keyboard.press("l");
    await expect(paneScroller(page), `k-threads: l puts the focus in ${what}`).toBeFocused();
    await expectTabReaches(page, paneScroller(page), expected, `in the reading pane, in ${what}`);
  }

  // The cards, below the reading pane's width, and the drawer over a project page: each thread's
  // card, its fold opened, walked from the card's row.
  for (const place of ["card mode", "the drawer"] as const) {
    let scope: Locator;
    if (place === "card mode") {
      await page.setViewportSize(NARROW);
      await page.goto("/");
      scope = page.getByTestId("inbox-list");
    } else {
      await page.setViewportSize(WIDE);
      await page.goto("/projects/CORE");
      await expect(page.getByRole("button", { exact: true, name: "Board" })).toBeVisible();
      await page.locator("body").focus();
      await page.keyboard.press("i");
      await expect(drawer(page), "k-threads: i opens the drawer").toBeVisible();
      scope = drawer(page);
    }
    for (const [thread, fold, expected, what] of threads) {
      const row = scope.locator(`[data-inbox-row="${replyRowId(thread)}"]`);
      if (fold !== undefined) await row.getByRole("button", { name: fold }).click();
      await row.focus();
      await expect(row, `k-threads: in ${place}, ${what}'s row takes the focus`).toBeFocused();
      await expectTabReaches(page, row, expected, `in ${place}, in ${what}`);
    }
  }
});
