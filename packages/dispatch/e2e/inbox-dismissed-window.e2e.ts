import type { MeInboxResponse } from "../web/src/api/types";
import { setLiveSessions } from "./agents";
import { createIssue, createProject, dismissInbox } from "./api";
import { countDismissedThreads, seedDismissedThreads } from "./dismissed-seed";
import { drawer } from "./inbox-drawer";
import { inboxList } from "./inbox-pane";
import { replyRowId, THREAD_SESSION } from "./inbox-threads";
import { expandedState } from "./settled";
import {
  type BrowserContext,
  expect,
  type Locator,
  type Page,
  type Response,
  test,
} from "./test";
import { asUser } from "./users";

/**
 * The Dismissed view over a person's whole history of dismissals (R7). The view reads the
 * server's pages of 200, newest dismissal first, and draws a window of the rows it has loaded:
 * the rows the list column shows and twelve above and below them (`DismissedWindow`,
 * web/src/features/inbox/Inbox.tsx). Its first row comes no later with 5,000 dismissed threads
 * than with 250, it reads the next page as the window nears the end of the loaded rows, and the
 * keys and the focus rules of `web/src/components/focus-place.ts` reach every loaded row, drawn
 * or not.
 *
 * Everything here is made up: this repository is public, so nothing names a real person, host or
 * project.
 */

/** The size the view is measured at: wide enough for the reading column, beside which the list
 *  column scrolls in itself. */
const WIDE = { height: 900, width: 1440 };

/** Rows the window draws past each edge of what the list column shows (`DISMISSED_OVERSCAN`). */
const OVERSCAN = 12;

/** How many times each pile is opened and timed. */
const READINGS = 5;

/** The first row's median time with 5,000 dismissed threads is at most this many times the
 *  median with 250. */
const SLOWER_AT_MOST = 1.25;

/** The Dismissed view's own address. */
const DISMISSED = "/?show=dismissed";

/** What one opening of the Dismissed view read. */
interface Opening {
  /** The size in bytes of the first page's answer. */
  readonly bytes: number;
  /** The cursor the first page answered for the next one. */
  readonly cursor: string | undefined;
  /** The row ids the first page answered, in its order. */
  readonly ids: readonly string[];
  /** The time from the navigation to the first drawn row, in milliseconds. */
  readonly ms: number;
}

/** Whether `response` answers a read of the Dismissed view's pages: the first, or with `cursor`
 *  the page that cursor names. */
function isDismissedPage(response: Response, cursor?: string): boolean {
  const url = new URL(response.url());
  return (
    url.pathname === "/api/v1/me/inbox" &&
    url.searchParams.get("state") === "dismissed" &&
    url.searchParams.get("cursor") === (cursor ?? null)
  );
}

/** The row ids one page of the read answers, in its order. */
function pageIds(answer: MeInboxResponse): string[] {
  return (answer.entries ?? []).flatMap((entry) =>
    entry.thread === undefined || entry.thread === null ? [] : [replyRowId(entry.thread.thread_id)]
  );
}

/** Notes, from the start of every document the page loads, when the first Inbox row is drawn:
 *  `performance.now()`, which counts from the start of the navigation, on the root element. */
async function noteFirstRow(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const watch = new MutationObserver(() => {
      if (document.querySelector("[data-inbox-row]") === null) return;
      document.documentElement.dataset.firstInboxRowAt = String(performance.now());
      watch.disconnect();
    });
    watch.observe(document, { childList: true, subtree: true });
  });
}

/** Opens the Dismissed view afresh and reads the time from the navigation to its first drawn row
 *  and the first page's answer. It asserts nothing, so a build that fails later still prints
 *  these readings first. The page leaves what it showed first, so no read the old page still has
 *  out, as a live update makes one, answers for the new page's or runs beside it. */
async function openDismissed(page: Page): Promise<Opening> {
  await page.goto("about:blank");
  const answered = page.waitForResponse((response) => isDismissedPage(response));
  await page.goto(DISMISSED);
  const response = await answered;
  const body = await response.body();
  await inboxList(page).locator("[data-inbox-row]").first().waitFor();
  const ms = await page.evaluate(() =>
    Number(document.documentElement.dataset.firstInboxRowAt ?? Number.NaN)
  );
  const answer = JSON.parse(body.toString("utf8")) as MeInboxResponse;
  return {
    bytes: body.byteLength,
    cursor: answer.next_cursor ?? undefined,
    ids: pageIds(answer),
    ms,
  };
}

/** A page of `context` at the measured size that notes each document's first row, with the
 *  build's code already in its cache: it opens the Inbox once first. */
async function timedPage(context: BrowserContext): Promise<Page> {
  const page = await context.newPage();
  await page.setViewportSize(WIDE);
  await noteFirstRow(page);
  await page.goto("/");
  await inboxList(page).waitFor();
  return page;
}

/** The middle of `values`, or the mean of the two middle ones. */
function median(values: readonly number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  const upper = sorted[middle] ?? Number.NaN;
  return sorted.length % 2 === 1 ? upper : ((sorted[middle - 1] ?? Number.NaN) + upper) / 2;
}

/** One row of `within`'s list, by the id its `data-inbox-row` names. */
function row(within: Page | Locator, id: string): Locator {
  return inboxList(within).locator(`[data-inbox-row="${id}"]`);
}

/** A row's own checkbox. */
function mark(within: Page | Locator, id: string): Locator {
  return row(within, id).getByRole("checkbox");
}

/** What the list draws now in `box`, the box its rows scroll in: the ids of the rows it draws, in
 *  order; how many of them the box shows, any part of a row in sight counting; and how many rows
 *  the view has loaded, which each drawn row says (`aria-setsize`). */
function readWindow(box: Locator): Promise<{ drawn: string[]; loaded: number; shown: number }> {
  return box.evaluate((scroller) => {
    const sight = scroller.getBoundingClientRect();
    const top = Math.max(sight.top, 0);
    const bottom = Math.min(sight.bottom, window.innerHeight);
    const rows = [...scroller.querySelectorAll<HTMLElement>("[data-inbox-row]")];
    const shown = rows.filter((each) => {
      const drawn = each.getBoundingClientRect();
      return drawn.bottom > top && drawn.top < bottom;
    });
    return {
      drawn: rows.map((each) => each.dataset.inboxRow ?? ""),
      loaded: Number(rows[0]?.getAttribute("aria-setsize") ?? 0),
      shown: shown.length,
    };
  });
}

/** One frame and one task on, by when the page has drawn what it last read and its focus rescue
 *  has answered a loss. */
function frameAndTask(page: Page): Promise<void> {
  return page.evaluate(() => {
    const done = Promise.withResolvers<void>();
    requestAnimationFrame(() => setTimeout(done.resolve, 0));
    return done.promise;
  });
}

/** The element holding the focus, named for a failure: the body, or its tag, its accessible name,
 *  and the row or dialog it is in. */
function focusHolder(page: Page): Promise<string> {
  return page.evaluate(() => {
    const active = document.activeElement;
    if (!(active instanceof HTMLElement) || active === document.body) return "the page's body";
    const name = (active.getAttribute("aria-label") ?? active.textContent ?? "")
      .replace(/\s+/g, " ")
      .trim()
      .slice(0, 80);
    const inRow = active.closest("[data-inbox-row]")?.getAttribute("data-inbox-row") ?? "no row";
    const testId = active.getAttribute("data-testid") ?? "";
    return `<${active.tagName.toLowerCase()}${testId === "" ? "" : ` data-testid="${testId}"`}> name="${name}" in ${inRow}`;
  });
}

/** Whether `target` holds the focus. */
function holdsFocus(target: Locator): Promise<boolean> {
  return target.evaluate((node) => node === document.activeElement);
}

/** The row the focus is in, or null for none. */
function focusedRow(page: Page): Promise<string | null> {
  return page.evaluate(
    () =>
      document.activeElement?.closest("[data-inbox-row]")?.getAttribute("data-inbox-row") ?? null
  );
}

/** Presses Tab from where the focus is until `target` holds it, calling `atStop` at each stop
 *  before it. */
async function tabTo(
  page: Page,
  target: Locator,
  what: string,
  atStop: () => Promise<void> = async () => {}
): Promise<void> {
  // Each read follows a key press whose focus move the browser makes before the press resolves,
  // and the line after the loop reads the focus again and fails with its holder.
  for (let pressed = 0; pressed < 120 && !(await holdsFocus(target)); pressed += 1) {
    await page.keyboard.press("Tab");
    if (!(await holdsFocus(target))) await atStop();
  }
  expect(
    await holdsFocus(target),
    `Tab reaches ${what}; the focus is on ${await focusHolder(page)}`
  ).toBe(true);
}

/** Scrolls `box` down, as far as a person's scroll goes at a time, until row `id` is no longer
 *  in the page, and answers where the box was left. */
async function scrollUntilGone(page: Page, within: Page | Locator, box: Locator, id: string) {
  for (let step = 0; step < 40 && (await row(within, id).count()) > 0; step += 1) {
    await box.evaluate((scroller) => {
      scroller.scrollTop += 400;
    });
    await frameAndTask(page);
  }
  await expect(row(within, id)).toHaveCount(0);
  return box.evaluate((scroller) => scroller.scrollTop);
}

/** Picks one choice of the View menu's Show section from the trigger inside `within`, and shuts
 *  the menu again. */
async function chooseShow(page: Page, within: Page | Locator, label: string): Promise<void> {
  const trigger = within.getByRole("button", { exact: true, name: "View" });
  if ((await expandedState("the View menu", trigger)) === "shut") await trigger.click();
  const menu = page.getByRole("dialog", { name: "View" });
  await menu.getByRole("group", { name: "Show" }).getByRole("radio", { name: label }).click();
  if ((await expandedState("the View menu", trigger)) === "open") await trigger.click();
  await expect(menu).toHaveCount(0);
}

// R7. A person's whole history of dismissals: 5,000 threads, of which the view reads the newest
// 200 and draws the ones the list column shows and twelve either side. Its first row is timed
// against a second person's 250 dismissals, on the same build in the same run. Neither person has
// a thread in the Inbox, so the two loads differ in the Dismissed pile alone: a pile taken down
// would stand in its person's Inbox, and the page's Inbox read would be timed as well. Every
// timing is read before the first hard assertion, so a build without the window prints its
// medians and sizes before it fails.
// SLOW(>=2s): 5,250 threads seeded and dismissed, ten timed page loads, three more pages read
// and thirty steps of the selection walked.
test("the Dismissed view draws a window of its rows over thousands dismissed", async ({
  browser,
}, testInfo) => {
  test.setTimeout(300_000);
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const pile = await createIssue({ project: "CORE", title: "Mirror retries" });
  const short = await createIssue({ project: "CORE", title: "Lease handover" });
  await seedDismissedThreads({ count: 5_000, issueKey: pile.key, prefix: "Mirror" });
  await seedDismissedThreads({ count: 250, issueKey: short.key, login: "bob", prefix: "Lease" });
  expect.soft(await countDismissedThreads("alice"), "alice's dismissed threads").toBe(5_000);
  expect.soft(await countDismissedThreads("bob"), "bob's dismissed threads").toBe(250);

  const alice = await asUser(browser, "alice");
  const bob = await asUser(browser, "bob");
  try {
    const page = await timedPage(alice);
    const many: Opening[] = [];
    for (let reading = 0; reading < READINGS; reading += 1) many.push(await openDismissed(page));
    const bobPage = await timedPage(bob);
    const few: Opening[] = [];
    for (let reading = 0; reading < READINGS; reading += 1) few.push(await openDismissed(bobPage));
    await bobPage.close();

    const manyMedian = median(many.map(({ ms }) => ms));
    const fewMedian = median(few.map(({ ms }) => ms));
    const report = [
      `5,000 dismissed: median ${manyMedian.toFixed(1)} ms to the first row over ${many
        .map(({ ms }) => ms.toFixed(1))
        .join(", ")}; first response ${many.map(({ bytes }) => bytes).join(", ")} bytes`,
      `250 dismissed: median ${fewMedian.toFixed(1)} ms to the first row over ${few
        .map(({ ms }) => ms.toFixed(1))
        .join(", ")}; first response ${few.map(({ bytes }) => bytes).join(", ")} bytes`,
    ].join("\n");
    console.log(report);
    await testInfo.attach("dismissed-window-timings", { body: report, contentType: "text/plain" });

    // Only now does it assert.
    expect(
      manyMedian,
      `the first row's median with 5,000 dismissed (${manyMedian.toFixed(1)} ms) is at most ${SLOWER_AT_MOST} times the median with 250 (${fewMedian.toFixed(1)} ms)`
    ).toBeLessThanOrEqual(SLOWER_AT_MOST * fewMedian);

    // The first person again, for the rest.
    const nextPages: string[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      const cursor = url.searchParams.get("cursor");
      if (
        url.pathname === "/api/v1/me/inbox" &&
        url.searchParams.get("state") === "dismissed" &&
        cursor !== null
      ) {
        nextPages.push(cursor);
      }
    });
    const opened = await openDismissed(page);
    expect(opened.ids.length).toBeGreaterThan(0);
    expect(opened.ids.length).toBeLessThanOrEqual(200);
    expect(opened.cursor).toBeDefined();

    // The window: the rows the list column shows, and at most twelve either side of them, out of
    // the 200 loaded.
    const box = inboxList(page);
    // Two frames, by when the window has measured the rows it first drew and drawn again.
    await frameAndTask(page);
    await frameAndTask(page);
    const first = await readWindow(box);
    expect(
      first.drawn.length,
      `the list draws ${first.drawn.length} rows, and shows ${first.shown}`
    ).toBeLessThanOrEqual(first.shown + 2 * OVERSCAN);
    expect(first.drawn.length).toBeLessThan(opened.ids.length);
    expect(first.drawn).toEqual(opened.ids.slice(0, first.drawn.length));

    // The end of the loaded rows: one read of the next page, from the first page's cursor, and
    // 400 rows loaded.
    const second = page.waitForResponse((response) => isDismissedPage(response, opened.cursor));
    await box.evaluate((scroller) => {
      scroller.scrollTop = scroller.scrollHeight;
    });
    const secondAnswer = (await (await second).json()) as MeInboxResponse;
    await expect.poll(async () => (await readWindow(box)).loaded).toBe(400);
    expect(nextPages).toEqual([opened.cursor]);

    // **Show more** still reads the next page from the keyboard. The focus on it brings the end
    // of the loaded rows into view, which reads the next page as any scroll there does; Enter
    // then reads one page more, from the cursor the last page answered.
    const more = inboxList(page).locator('[data-inbox-show-more="dismissed"]');
    const third = page.waitForResponse((response) =>
      isDismissedPage(response, secondAnswer.next_cursor ?? undefined)
    );
    await more.focus();
    const thirdAnswer = (await (await third).json()) as MeInboxResponse;
    await expect.poll(async () => (await readWindow(box)).loaded).toBe(600);
    await expect(more).toHaveText("Show 200 more");
    const fourth = page.waitForResponse((response) =>
      isDismissedPage(response, thirdAnswer.next_cursor ?? undefined)
    );
    await page.keyboard.press("Enter");
    await fourth;
    await expect.poll(async () => (await readWindow(box)).loaded).toBe(800);
    expect(nextPages).toEqual([
      opened.cursor,
      secondAnswer.next_cursor,
      thirdAnswer.next_cursor,
    ]);

    // `j` from the first row to the 30th: the row in hand is drawn and in view at each step.
    await box.evaluate((scroller) => {
      scroller.scrollTop = 0;
    });
    const firstId = opened.ids[0] ?? "";
    await row(page, firstId).locator("time").first().click();
    await expect(row(page, firstId)).toHaveAttribute("data-inbox-selected", "true");
    for (let at = 1; at < 30; at += 1) {
      await page.keyboard.press("j");
      const id = opened.ids[at] ?? "";
      await expect(row(page, id)).toHaveAttribute("data-inbox-selected", "true");
      await expect
        .poll(
          () =>
            row(page, id).evaluate((node) => {
              const list = node.closest('[data-testid="inbox-list"]');
              if (list === null) return false;
              const sight = list.getBoundingClientRect();
              const drawn = node.getBoundingClientRect();
              return drawn.top >= sight.top && drawn.bottom <= sight.bottom;
            }),
          { message: `row ${at + 1}, the row in hand, is in view` }
        )
        .toBe(true);
    }

    // Move to Inbox on the row in hand: it leaves the view and stands in the Inbox.
    const back = opened.ids[29] ?? "";
    await row(page, back).locator(`[data-inbox-dismiss="${back}"]`).click();
    await expect(row(page, back)).toHaveCount(0);
    await chooseShow(page, page, "Inbox");
    await expect(row(page, back)).toBeVisible();
  } finally {
    await bob.close();
    await alice.close();
  }
});

// R7 under the focus rules of web/src/components/focus-place.ts. A focused row the person scrolls
// out of the window is no loss: nothing moves the focus or scrolls, and in the drawer the drawer
// takes the focus. A row another tab restores stays drawn while it is the row under the hand,
// and goes once it is not.
// SLOW(>=2s): 1,000 threads seeded and dismissed, scrolls on the page and in the drawer waited
// out, and a live update from a second tab.
test("a focused row scrolled out of the window is no loss of focus", async ({ browser }) => {
  test.setTimeout(180_000);
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const pile = await createIssue({ project: "CORE", title: "Mirror retries" });
  await seedDismissedThreads({ count: 1_000, issueKey: pile.key, prefix: "Mirror" });
  expect(await countDismissedThreads()).toBe(1_000);

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.setViewportSize(WIDE);
    const opened = await openDismissed(page);
    const box = inboxList(page);
    const fifth = opened.ids[4] ?? "";

    // The person tabs to the fifth row's checkbox, and scrolls the list until the window has
    // taken that row away.
    await page.locator("body").focus();
    await tabTo(page, mark(page, fifth), "the fifth row's checkbox");
    const pageAt = await page.evaluate(() => window.scrollY);
    const listAt = await scrollUntilGone(page, page, box, fifth);
    await frameAndTask(page);
    expect(await box.evaluate((scroller) => scroller.scrollTop)).toBe(listAt);
    expect(await page.evaluate(() => window.scrollY)).toBe(pageAt);
    expect(await focusedRow(page), `the focus is on ${await focusHolder(page)}`).toBeNull();
    expect(await focusHolder(page)).toBe("the page's body");

    // Scrolled back, the row is drawn again.
    await box.evaluate((scroller) => {
      scroller.scrollTop = 0;
    });
    await expect(row(page, fifth)).toHaveCount(1);

    // The drawer, on the Dismissed view: the same scroll leaves the focus on the drawer itself.
    await page.goto("/agents");
    await expect(page.getByRole("heading", { name: "Agents" })).toBeVisible();
    await page.locator("body").focus();
    await page.keyboard.press("i");
    const peek = drawer(page);
    await expect(peek).toBeVisible();
    await chooseShow(page, peek, "Dismissed");
    await expect(row(peek, fifth)).toHaveCount(1);
    await tabTo(page, mark(peek, fifth), "the fifth row's checkbox in the drawer");
    const pageBefore = await page.evaluate(() => window.scrollY);
    const drawerAt = await scrollUntilGone(page, peek, peek, fifth);
    await frameAndTask(page);
    expect(await holdsFocus(peek), `the focus is on ${await focusHolder(page)}`).toBe(true);
    expect(await peek.evaluate((scroller) => scroller.scrollTop)).toBe(drawerAt);
    expect(await page.evaluate(() => window.scrollY)).toBe(pageBefore);
    await page.keyboard.press("Escape");
    await expect(peek).toHaveCount(0);

    // Last, a second tab restores the row whose checkbox holds the focus, with the pointer on no
    // row: the row stays drawn in its place, with the focus, and nothing scrolls.
    await openDismissed(page);
    await page.mouse.move(1, 1);
    const third = opened.ids[2] ?? "";
    const next = opened.ids[3] ?? "";
    await mark(page, third).focus();
    const before = (await readWindow(box)).drawn;
    const listBefore = await box.evaluate((scroller) => scroller.scrollTop);
    const reread = page.waitForResponse((response) => isDismissedPage(response));
    await dismissInbox({ restore: true, threads: [third] });
    await reread;
    await frameAndTask(page);
    expect((await readWindow(box)).drawn).toEqual(before);
    expect(await holdsFocus(mark(page, third)), `the focus is on ${await focusHolder(page)}`).toBe(
      true
    );
    expect(await box.evaluate((scroller) => scroller.scrollTop)).toBe(listBefore);

    // At each of the row's own stops it stays drawn; once the focus is on the next row's
    // checkbox, the view draws it no more, and the other rows keep their order.
    await tabTo(page, mark(page, next), "the next row's checkbox", async () => {
      if ((await focusedRow(page)) === third) await expect(row(page, third)).toHaveCount(1);
    });
    await expect(row(page, third)).toHaveCount(0);
    const after = (await readWindow(box)).drawn;
    const others = before.filter((id) => id !== third);
    expect(after.slice(0, others.length)).toEqual(others.slice(0, after.length));
  } finally {
    await alice.close();
  }
});
