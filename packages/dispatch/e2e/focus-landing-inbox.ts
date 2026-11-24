/** The Inbox's and the Inbox drawer's cases of `focus-landing.e2e.ts`: the rows each runs, and how
 *  each case stages its shape. Where a case follows a row of `table-controls.e2e.ts`, it names
 *  that row by its title. */

import type { Locator, Route } from "@playwright/test";

import { setInterests, setLiveSessions } from "./agents";
import {
  createAsk,
  createComment,
  createIssue,
  createMessage,
  createProject,
  markInboxRead,
  patchIssue,
  requestApproval,
} from "./api";
import { controlRow, listTrigger, pickFromList } from "./filters";
import {
  type Arrange,
  type CaseContext,
  pressEnter,
  type Scene,
  type SceneRow,
  scrollTo,
} from "./focus-landing-cases";
import { inboxSearchBox } from "./inbox-filter";
import { askCard, inboxPane, inboxRow, selectRow } from "./inbox-pane";
import { seedCommentThread } from "./inbox-threads";
import { openSurface, SURFACE_SESSION, seedSurfaces } from "./table-controls";
import { expect, type Page } from "./test";
import { withAlice } from "./users";

/** The project every case of these rows writes into beside the seed's CORE, for a message that
 *  mentions a question; made once a row, by the first case that needs it. */
async function opsProject(): Promise<void> {
  await createProject({ key: "OPS", name: "Operations" }).catch((cause: unknown) => {
    if (!String(cause).includes("PROJECT_EXISTS")) throw cause;
  });
}

/** A question of the case's own, waiting on the reader, with two options. */
async function question(title: string, asker = SURFACE_SESSION): Promise<string> {
  // `force`: cases make near copies of each other's questions by design, which the create's
  // near-copy gate refuses with 409 POSSIBLE_DUPLICATE otherwise.
  const issue = await createIssue({ force: true, project: "CORE", title });
  await patchIssue(issue.key, { assignee: "alice" });
  const ask = await createAsk(
    issue.key,
    { options: [{ label: "Ship" }, { label: "Hold" }], question: `${title}?` },
    asker
  );
  return ask.id;
}

/** Two unread comment threads and, newer than both, a question waiting on the reader. The
 *  threads keep Mark all read offered. The question is the newest row, so the Inbox selects it
 *  first, and its reading column shows no thread: a thread on screen there for a second is marked
 *  read, which would take Mark all read away before the cause. */
async function seedUnread(n: number): Promise<void> {
  for (const word of ["mirror", "ledger"]) {
    const issue = await createIssue({ project: "CORE", title: `Unread ${word} thread ${n}` });
    await seedCommentThread(issue.key, `Is the ${word} caught up? (${n})`, "It is.");
  }
  const asking = await createIssue({ project: "CORE", title: `Waiting question ${n}` });
  await createAsk(asking.key, { question: `Which goes first? (${n})` }, SURFACE_SESSION);
}

/** The row's Mark all read, open on `surface`, with the focus on it. It goes once no thread the
 *  list holds is unread, and its press takes it away at once, before its write lands. */
async function markAllRead(
  { n, page, scene }: CaseContext,
  surface: "Inbox" | "Inbox drawer"
): Promise<{ markAll: Locator; row: Locator }> {
  await seedUnread(n);
  const { row } = await openSurface(page, surface, scene);
  const markAll = row.getByRole("button", { exact: true, name: "Mark all read" });
  await expect(markAll).toBeVisible();
  await markAll.focus();
  return { markAll, row };
}

/** The reader's own Mark all read: Blocked on you, beside it in the row, moves into its place. */
function markAllPressed(surface: "Inbox" | "Inbox drawer"): Arrange {
  return async (context) => {
    const { markAll, row } = await markAllRead(context, surface);
    return {
      answered: () => expect(markAll).toHaveCount(0),
      cause: pressEnter(context.page),
      caused: true,
      control: markAll,
      named: {
        at: row.getByRole("button", { name: /^Blocked on you/ }),
        name: 'button "Blocked on you…"',
      },
      staged: { rule: "moved-up" },
    };
  };
}

/** The causes the Inbox and the drawer share: Mark all read taken away by the reader's press, by
 *  a Mark all read made through the API, and by one pressed on the other device. */
function markAllRow(surface: "Inbox" | "Inbox drawer"): SceneRow {
  return {
    what: "a press, a live update or another device",
    cases: {
      press: { arrange: markAllPressed(surface) },
      live: {
        arrange: async (context) => {
          const { markAll } = await markAllRead(context, surface);
          return {
            answered: () => expect(markAll).toHaveCount(0),
            cause: () => markInboxRead({ read_through: new Date().toISOString() }),
            caused: false,
            control: markAll,
          };
        },
      },
      device: {
        arrange: async (context) => {
          const { markAll } = await markAllRead(context, surface);
          return {
            answered: () => expect(markAll).toHaveCount(0),
            cause: () =>
              withAlice(context.browser, async (other) => {
                await other.goto("/");
                const theirs = controlRow(other.getByTestId("main-content")).getByRole("button", {
                  exact: true,
                  name: "Mark all read",
                });
                await theirs.click();
                await expect(theirs).toHaveCount(0);
              }),
            caused: false,
            control: markAll,
          };
        },
      },
    },
  };
}

/** Three comment threads of the case's own on one issue, newest first, and the Inbox open on
 *  them: the first thread's row, the row after it, and that row's Dismiss. */
async function threeThreads({ n, page }: CaseContext): Promise<{
  dismiss: Locator;
  next: Locator;
  row: Locator;
}> {
  const issue = await createIssue({ project: "CORE", title: `Mirror lag ${n}` });
  for (const word of ["first", "second", "third"]) {
    await seedCommentThread(issue.key, `Is the ${word} mirror caught up (${n})?`, "It is.");
  }
  await page.goto("/");
  const mine = page
    .locator("[data-inbox-row]")
    .filter({ has: page.locator("[data-inbox-dismiss]") })
    .filter({ hasText: `(${n})` });
  await expect(mine).toHaveCount(3);
  // One read of the order: the wait above found the three thread rows drawn, newest first.
  const ids = await mine.evaluateAll((rows) =>
    rows.map((row) => row.getAttribute("data-inbox-row") ?? "")
  );
  const row = page.locator(`[data-inbox-row="${ids[0] ?? ""}"]`);
  const next = page.locator(`[data-inbox-row="${ids[1] ?? ""}"]`).locator("[data-inbox-dismiss]");
  return { dismiss: row.locator("[data-inbox-dismiss]"), next, row };
}

/** A thread's Dismiss, pressed: its row goes once its write lands, and the next thread's row moves
 *  up into its place. At 390 px a thread's row is about 459 px tall, and the focus put on Dismiss
 *  scrolls the window to it, as the tester's row "a thread's Dismiss in the Inbox's list scrolls
 *  nothing and hands the focus to the next row's Dismiss in sight" stages it. */
const threadDismiss: Arrange = async (context) => {
  const { dismiss, next, row } = await threeThreads(context);
  await dismiss.focus();
  return {
    answered: () => expect(row).toHaveCount(0),
    cause: pressEnter(context.page),
    caused: true,
    control: dismiss,
    named: { at: next, name: "the next thread's Dismiss" },
    staged: { rule: "moved-up" },
  };
};

/**
 * `e` on the last unread thread's row at 390 px, with the row standing just under the window's
 * top. Every thread the row's earlier cases seeded is marked read first, so the case's own thread
 * is the list's one unread row and its newest, first in one band (`?group=none`). The row goes,
 * and Mark all read with it: below `sm` its line in the control row closes, above the list. The
 * row after the thread moves up into its place, and the window scrolls up by the line that closed,
 * so that row stands where the thread's row stood (`view kept (shrunk above)`). The press is
 * answered once the row and Mark all read have gone, the control row is shorter, and the row
 * after the thread stands where the thread's row stood, within a pixel.
 */
const lastUnreadKeyed: Arrange = async ({ n, page }) => {
  await markInboxRead({ read_through: new Date().toISOString() });
  const issue = await createIssue({ project: "CORE", title: `Last unread thread ${n}` });
  await seedCommentThread(issue.key, `Is the last mirror caught up (${n})?`, "It is.");
  await page.goto("/?group=none");
  const row = page.locator("[data-inbox-row]").filter({ hasText: `last mirror caught up (${n})` });
  await expect(row).toHaveCount(1);
  const line = controlRow(page.getByTestId("main-content"));
  const markAll = line.getByRole("button", { exact: true, name: "Mark all read" });
  await expect(markAll).toBeVisible();
  // One read of the row after the thread: the waits above found the list drawn, and nothing moves
  // it before the press.
  const nextId = await row.evaluate((node) => {
    let after = node.nextElementSibling;
    while (after !== null && !after.hasAttribute("data-inbox-row")) {
      after = after.nextElementSibling;
    }
    return after?.getAttribute("data-inbox-row") ?? "";
  });
  expect(nextId, "no row stands after the case's own thread").not.toBe("");
  const next = inboxRow(page, nextId);
  await row.focus();
  await scrollTo(row, 0.03);
  // One read each of where the thread's row stands and how tall the control row is: the scroll
  // above is the last thing that moves either before the press.
  const stood = (await row.boundingBox())?.y ?? Number.NaN;
  const tall = (await line.boundingBox())?.height ?? 0;
  return {
    answered: async () => {
      await expect(row).toHaveCount(0);
      await expect(markAll).toHaveCount(0);
      await expect
        .poll(
          async () => (await line.boundingBox())?.height ?? tall,
          "Mark all read's line did not close in the control row"
        )
        .toBeLessThan(tall - 1);
      await expect
        .poll(
          async () => Math.abs(((await next.boundingBox())?.y ?? Number.NaN) - stood),
          "the row after the thread does not stand where the thread's row stood"
        )
        .toBeLessThanOrEqual(1);
    },
    cause: () => page.keyboard.press("e"),
    caused: true,
    control: row,
    staged: { rule: "moved-up" },
  };
};

/** A thread's Dismiss pressed offline: its write is held past the request deadline. */
const threadDismissOffline: Arrange = async (context) => {
  const { dismiss } = await threeThreads(context);
  await dismiss.focus();
  return {
    // Offline, the row goes at once or its Dismiss is held while the write is out.
    answered: () =>
      expect
        .poll(
          async () =>
            (await dismiss.count()) === 0 ||
            (await dismiss.getAttribute("aria-disabled")) === "true"
        )
        .toBe(true),
    cause: pressEnter(context.page),
    caused: true,
    control: dismiss,
  };
};

/** The selection bar's toolbar on the Inbox. */
function inboxBar(page: Page): Locator {
  return page.getByRole("toolbar", { name: "Selected rows" });
}

/** Marks `ids`' rows by their checkboxes with Space, the last of `ids` last. */
async function markRows(page: Page, ids: readonly string[]): Promise<void> {
  for (const id of ids) {
    const box = inboxRow(page, id).getByRole("checkbox").first();
    await box.focus();
    await page.keyboard.press("Space");
    await expect(box).toBeChecked();
  }
}

/**
 * The tester's row "a bulk Dismiss deep in the Inbox's list hands the focus to the row now at the
 * marked row's place in sight, and scrolls by no more than the line that closes above the list":
 * 38 asks and threads beside the seed's six, listed in one band (`?group=none`), with two threads
 * twelve rows down marked by Space, the upper one last, scrolled to 40% of the box that shows
 * them, and the bar's Dismiss pressed. The marked row is taken away.
 */
const bulkDismissDeep: Arrange = async ({ n, page }) => {
  for (let index = 0; index < 12; index++) await question(`Early ask ${index} (${n})`);
  for (let index = 0; index < 6; index++) {
    const issue = await createIssue({ project: "CORE", title: `Thread issue ${index} (${n})` });
    await seedCommentThread(issue.key, `Thread ${index}: when does it run (${n})?`, "At 03:00.");
  }
  for (let index = 0; index < 20; index++) await question(`Late ask ${index} (${n})`);
  await page.goto("/?group=none");
  const rows = page.locator("[data-inbox-row]");
  await expect.poll(() => rows.count()).toBeGreaterThanOrEqual(30);
  // One read of the order: the poll above found the list drawn, and nothing moves it before the
  // marks.
  const order = await rows.evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-inbox-row") ?? "")
  );
  let first = -1;
  for (let index = 12; first === -1 && index + 2 < order.length; index++) {
    if (order.slice(index, index + 2).every((id) => id.startsWith("reply:"))) first = index;
  }
  expect(first, "no two threads twelve rows down").toBeGreaterThanOrEqual(12);
  const marked = order.slice(first, first + 2);
  await scrollTo(inboxRow(page, marked[0] ?? ""), 0.4);
  await markRows(page, [...marked].reverse());
  const dismiss = inboxBar(page).getByRole("button", {
    exact: true,
    name: "Dismiss selected threads",
  });
  await dismiss.focus();
  return {
    answered: async () => {
      for (const id of marked) await expect(inboxRow(page, id)).toHaveCount(0);
      await expect(inboxBar(page)).toHaveCount(0);
    },
    cause: pressEnter(page),
    caused: true,
    control: dismiss,
    named: {
      at: inboxRow(page, order[first + 2] ?? "")
        .getByRole("checkbox")
        .first(),
      name: "the checkbox of the row after the marked ones",
    },
    staged: { rule: "bar-marked" },
  };
};

/** Two questions of the case's own, newest of all, marked by Space, the upper one last, with the
 *  focus on the bar's `control`. */
async function twoMarked(
  { n, page }: CaseContext,
  word: string
): Promise<{ ids: string[]; trigger: Locator }> {
  const ids = [await question(`${word} lower ${n}`), await question(`${word} upper ${n}`)];
  await page.goto("/");
  for (const id of ids) await expect(inboxRow(page, id)).toBeVisible();
  await markRows(page, ids);
  const trigger = listTrigger(inboxBar(page), "Snooze selected asks");
  await trigger.focus();
  return { ids, trigger };
}

/** The bar's Snooze, picked for the marked rows: they fold into the Later band, out of the view,
 *  and the bar goes. */
const bulkSnoozeLeaves: Arrange = async (context) => {
  const { trigger } = await twoMarked(context, "Snoozed");
  return {
    answered: () => expect(inboxBar(context.page)).toHaveCount(0),
    cause: () => pickFromList(trigger, "Tomorrow"),
    caused: true,
    control: trigger,
    staged: { rule: "bar-marked" },
  };
};

/** The bar's Snooze picked offline: its writes are never sent and stay out past the request
 *  deadline. The focus is read then, wherever the page holds the bar. */
const bulkSnoozeOffline: Arrange = async (context) => {
  const { trigger } = await twoMarked(context, "Offline snoozed");
  return {
    answered: () => expect(inboxBar(context.page)).toContainText("Snoozing"),
    cause: () => pickFromList(trigger, "Tomorrow"),
    caused: true,
    control: trigger,
  };
};

/** The bar's Clear selection: the bar goes and the row marked last stands. */
const clearSelection: Arrange = async ({ n, page }) => {
  const id = await question(`Cleared ask ${n}`);
  await page.goto("/");
  await expect(inboxRow(page, id)).toBeVisible();
  await markRows(page, [id]);
  const clear = inboxBar(page).getByRole("button", { exact: true, name: "Clear selection" });
  await clear.focus();
  return {
    answered: () => expect(inboxBar(page)).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: clear,
    named: {
      at: inboxRow(page, id).getByRole("checkbox").first(),
      name: "the marked row's checkbox",
    },
    staged: { rule: "bar-marked" },
  };
};

/** The list narrowed by the search box to two threads of the case's own, both marked and dismissed
 *  from the bar: the view empties. */
const emptiedView: Arrange = async ({ n, page }) => {
  const word = `Emptied${n}x`;
  const issue = await createIssue({ project: "CORE", title: `${word} threads` });
  for (const which of ["one", "two"]) {
    await seedCommentThread(issue.key, `${word} thread ${which}?`, "Done.");
  }
  await page.goto("/");
  // The Inbox's search box, which stands in its header line beside the title.
  await inboxSearchBox(page).fill(word);
  const rows = page.locator("[data-inbox-row]");
  await expect(rows).toHaveCount(2);
  // One read of the two rows: the wait above found exactly the case's own threads drawn.
  const ids = await rows.evaluateAll((nodes) =>
    nodes.map((node) => node.getAttribute("data-inbox-row") ?? "")
  );
  await markRows(page, [...ids].reverse());
  const dismiss = inboxBar(page).getByRole("button", {
    exact: true,
    name: "Dismiss selected threads",
  });
  await dismiss.focus();
  return {
    answered: async () => {
      await expect(rows).toHaveCount(0);
      await expect(inboxBar(page)).toHaveCount(0);
    },
    cause: pressEnter(page),
    caused: true,
    control: dismiss,
    staged: { rule: "stop" },
  };
};

/**
 * Mark all read with unread threads above the list's view and at its top, at 1440 px, where the
 * list scrolls in its own box beside the reading column: ten questions under eight threads under
 * one newer question, which the reading column shows, and the box scrolled so the sixth thread
 * stands at its top. The press marks every thread read, above the view and at it, and takes no row
 * out of the list (`features/inbox/mark-all.ts`); its own button goes. So nothing moves the box.
 */
const markAllAbove: Arrange = async ({ n, page }) => {
  for (let index = 0; index < 10; index++) await question(`Below ask ${index} (${n})`);
  const issue = await createIssue({ project: "CORE", title: `Above threads ${n}` });
  for (let index = 0; index < 8; index++) {
    await seedCommentThread(issue.key, `Is mirror ${index} caught up (${n})?`, "It is.");
  }
  await question(`Newest ask ${n}`);
  await page.goto("/");
  const threads = page.locator("[data-inbox-row]").filter({ hasText: `caught up (${n})` });
  await expect(threads).toHaveCount(8);
  await scrollTo(threads.nth(5), 0);
  const markAll = controlRow(page.getByTestId("main-content")).getByRole("button", {
    exact: true,
    name: "Mark all read",
  });
  await markAll.focus();
  return {
    answered: () => expect(markAll).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: markAll,
    staged: { rule: "moved-up" },
  };
};

/** The reading column's head's Approve, at 1440 px, on a first request to approve a spec: the
 *  question goes once its answer lands, and the column shows the next one. */
const approveFromHead: Arrange = async ({ n, page }) => {
  const issue = await createIssue({
    project: "CORE",
    spec: `# Handover ${n}\n\nThe lease moves with the ledger.\n`,
    title: `Approved handover ${n}`,
  });
  const requested = await requestApproval(
    issue.primary_artifact_id,
    { summary: `The first handover draft ${n}.` },
    SURFACE_SESSION
  );
  const ask = requested.ask?.id ?? "";
  await page.goto("/");
  await expect(inboxRow(page, ask)).toBeVisible();
  await selectRow(page, ask);
  const approve = inboxPane(page)
    .locator("header")
    .first()
    .getByRole("button", { exact: true, name: "Approve" });
  await expect(approve).toBeVisible();
  await approve.focus();
  return {
    answered: () => expect(inboxRow(page, ask)).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: approve,
  };
};

/** A session that asks the held rows' questions, so each waits on the reader. */
const HELD_ASKER = {
  actor: { id: "e2e-landing-asker", kind: "session" as const },
  as: "agent" as const,
};

/**
 * The tester's row "the Inbox's Decline dialog keeps the keyboard focus inside itself while its
 * write is out, and on a control after it lands": three questions; the reader opens the oldest
 * one's Decline to answer from its card, types a note, and presses the dialog's Decline to answer
 * with the decline held; a Tab while it is held; then the decline lands, and the row goes with
 * the dialog.
 */
const declineDialog: Arrange = async ({ n, page }) => {
  const held: Route[] = [];
  const pattern = "**/api/v1/asks/*/decline";
  const hold = async (route: Route) => {
    if (route.request().method() !== "POST") return route.fallback();
    held.push(route);
  };
  await page.route(pattern, hold);
  const asks: string[] = [];
  for (const what of ["lease format", "rollout order", "cache size"]) {
    asks.push(await question(`Which ${what} (${n})`, HELD_ASKER));
  }
  await page.goto("/");
  for (const id of asks) await expect(inboxRow(page, id)).toBeVisible();
  const target = asks[0] ?? "";
  const card = await askCard(page, target);
  await card.getByRole("button", { exact: true, name: "Decline to answer" }).focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Decline to answer" });
  await expect(dialog.getByLabel("Note (optional)")).toBeFocused();
  await page.keyboard.type("Not ours to decide.");
  const decline = dialog.getByRole("button", { exact: true, name: "Decline to answer" });
  await decline.focus();
  return {
    after: () => page.unroute(pattern, hold),
    answered: async () => {
      await expect.poll(() => held.length, "the decline was never sent").toBe(1);
      await expect(dialog.getByRole("button", { name: "Declining…" })).toBeVisible();
    },
    cause: pressEnter(page),
    caused: true,
    control: decline,
    held: {
      during: () => page.keyboard.press("Tab"),
      landed: async () => {
        await expect(dialog).toHaveCount(0);
        await expect(inboxRow(page, target)).toHaveCount(0);
      },
      release: async () => {
        for (const route of held.splice(0)) await route.continue();
      },
    },
  };
};

/** A failed read's answer: a 500 with Dispatch's own error body. */
function failedRead(route: Route, what: string): Promise<void> {
  return route.fulfill({
    body: JSON.stringify({ code: "INTERNAL", error: `The ${what} read failed.` }),
    contentType: "application/json",
    status: 500,
  });
}

/**
 * The tester's row "the Inbox card's Referenced by Retry keeps the keyboard focus while its read is
 * out": a message on another issue mentions a question, the reader opens Referenced by (1) on its
 * card, a second message makes the live feed read the backlinks again, which fails, and the reader
 * presses the panel's Retry with the read it sends held. The read answers, and the failed line goes
 * with Retry.
 */
const referencedByRetry: Arrange = async ({ n, page }) => {
  await opsProject();
  const reads = { held: [] as Route[], mode: "answer" as "answer" | "fail" | "hold", target: "" };
  const matches = (url: URL) =>
    url.pathname === "/api/v1/references" &&
    reads.target !== "" &&
    (url.searchParams.get("to") ?? "").includes(reads.target);
  const answer = async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    if (reads.mode === "hold") {
      reads.held.push(route);
      return;
    }
    if (reads.mode === "answer") return route.continue();
    await failedRead(route, "backlinks");
  };
  await page.route(matches, answer);
  const issue = await createIssue({ project: "CORE", title: `Rollout order ${n}` });
  await patchIssue(issue.key, { assignee: "alice" });
  const { id: ask } = await createAsk(
    issue.key,
    { options: [{ label: "Ship" }, { label: "Hold" }], question: `Which rollout order (${n})?` },
    HELD_ASKER
  );
  const issueKey = issue.key;
  const source = await createIssue({ project: "OPS", title: `Mirror plan ${n}` });
  await createMessage(source.key, { body: `Depends on dispatch://${issueKey}/ask/${ask}.` });
  reads.target = ask;
  await page.goto("/");
  await expect(inboxRow(page, ask)).toBeVisible();
  const card = await askCard(page, ask);
  await card.getByRole("button", { name: /^Referenced by \(1\)$/ }).focus();
  await page.keyboard.press("Enter");
  const panel = card.getByRole("region", { name: "Referenced by" });
  await expect(panel.getByText(`Message · ${source.key}`).first()).toBeVisible();
  reads.mode = "fail";
  await createMessage(source.key, { body: `Again dispatch://${issueKey}/ask/${ask}.` });
  const failed = panel.getByText("Referenced by unavailable.");
  await expect(failed).toBeVisible();
  const retry = panel.getByRole("button", { exact: true, name: "Retry" });
  await retry.focus();
  reads.mode = "hold";
  return {
    after: () => page.unroute(matches, answer),
    answered: () => expect.poll(() => reads.held.length, "Retry sent no read").toBe(1),
    cause: pressEnter(page),
    caused: true,
    control: retry,
    held: {
      landed: () => expect(failed).toHaveCount(0),
      release: async () => {
        reads.mode = "answer";
        for (const route of reads.held.splice(0)) await route.continue();
      },
    },
  };
};

/**
 * The tester's row "Unfollow and Unsubscribe on an Inbox card's recipients keep the keyboard focus
 * on a control through their write": a question that reaches its asker, a session that replied to
 * it and a session subscribed to its issue; the reader opens Reaches 3, presses the replier's
 * Unfollow or the subscriber's Unsubscribe and the dialog's Confirm, with the write held. Confirm
 * closes the dialog in its own press, which hands the focus back to the control that opened it.
 * The write lands, and that recipient's chip goes.
 */
function recipientRemoval(label: "Unfollow" | "Unsubscribe"): Arrange {
  return async ({ n, page }) => {
    const asker = { id: "e2e-landing-recipients-asker", kind: "session" as const };
    const replier = { id: "e2e-landing-recipients-replier", kind: "session" as const };
    const watcher = "e2e-landing-recipients-watcher";
    await setLiveSessions([
      { session_id: asker.id, title: "Asker (e2e)" },
      { session_id: replier.id, title: "Replier (e2e)" },
      { session_id: watcher, title: "Watcher (e2e)" },
    ]);
    const writes = { held: [] as Route[], path: "" };
    const matches = (url: URL) => url.pathname === writes.path;
    const hold = async (route: Route) => {
      if (route.request().method() !== "DELETE") return route.fallback();
      writes.held.push(route);
    };
    await page.route(matches, hold);
    const issue = await createIssue({ project: "CORE", title: `Who hears the ${label} ${n}` });
    await patchIssue(issue.key, { assignee: "alice" });
    const ask = await createAsk(
      issue.key,
      {
        options: [{ label: "Ship" }, { label: "Hold" }],
        question: `Ship the ${label} (${n})?`,
      },
      { actor: asker, as: "agent" }
    );
    await createComment(
      issue.key,
      { ask_id: ask.id, body: "Tests are green on my side." },
      { actor: replier, as: "agent" }
    );
    await setInterests([
      { session_id: watcher, topics: [`notifications.dispatch.issue.${issue.key}.>`] },
    ]);
    const who = label === "Unfollow" ? replier.id : watcher;
    writes.path =
      label === "Unfollow"
        ? `/api/v1/asks/${ask.id}/followers/${replier.id}`
        : `/api/v1/issues/${issue.key}/subscribers/${watcher}`;
    await page.goto("/");
    await expect(inboxRow(page, ask.id)).toBeVisible();
    const card = await askCard(page, ask.id);
    const recipients = card.getByRole("region", { name: "Recipients" });
    const reaches = recipients.getByRole("button", { name: /^Reaches/ });
    await expect(reaches).toHaveText(/^Reaches 3/);
    await reaches.focus();
    await page.keyboard.press("Enter");
    const chip = recipients.getByRole("listitem").filter({ has: page.locator(`[title="${who}"]`) });
    await chip.getByRole("button", { exact: true, name: label }).focus();
    await page.keyboard.press("Enter");
    const dialog = page.getByRole("dialog", { name: label });
    const confirm = dialog.getByRole("button", { exact: true, name: "Confirm" });
    await confirm.focus();
    return {
      after: () => page.unroute(matches, hold),
      answered: async () => {
        await expect.poll(() => writes.held.length, "Confirm sent no write").toBe(1);
        await expect(dialog).toHaveCount(0);
      },
      cause: pressEnter(page),
      caused: true,
      control: confirm,
      held: {
        landed: () => expect(chip).toHaveCount(0),
        release: async () => {
          for (const route of writes.held.splice(0)) await route.continue();
        },
      },
    };
  };
}

const ONE_WIDTH_ONLY = {
  approve:
    "below 1280 px the Inbox draws no reading column, so there is no head of it to approve from",
  keyedLastUnread:
    "from sm up the control row's first line is one line, so Mark all read's going closes no line above the list",
  markAllAbove:
    "below 1280 px the Inbox's list scrolls in the window under its control row, so Mark all read leaves the reader's sight before any row goes above the view",
};

/** The Inbox's rows. */
export const INBOX_SCENE: Scene = {
  seed: seedSurfaces,
  rows: [
    markAllRow("Inbox"),
    {
      what: "the reader's press on a row",
      cases: {
        "thread-dismiss": { arrange: threadDismiss },
        "keyed-last-unread": {
          arrange: lastUnreadKeyed,
          only: { none: ONE_WIDTH_ONLY.keyedLastUnread, width: 390 },
        },
        "mark-all-above": {
          arrange: markAllAbove,
          only: { none: ONE_WIDTH_ONLY.markAllAbove, width: 1440 },
        },
        "approve-head": {
          arrange: approveFromHead,
          only: { none: ONE_WIDTH_ONLY.approve, width: 1440 },
        },
      },
    },
    {
      what: "the selection bar's going",
      cases: {
        "bar-marked-taken": { arrange: bulkDismissDeep },
        "bar-marked-leaves": { arrange: bulkSnoozeLeaves },
        "bar-marked-stands": { arrange: clearSelection },
        "bar-emptied": { arrange: emptiedView },
        "bar-none-marked": {
          none: "the Inbox draws its selection bar only once a row is marked, by its own checkbox or by x, so every bar there has a row marked last",
        },
      },
    },
    {
      what: "the reader's press on a held control",
      cases: {
        "held-decline": { arrange: declineDialog },
        "held-referenced-by": { arrange: referencedByRetry },
        "held-unfollow": { arrange: recipientRemoval("Unfollow") },
        "held-unsubscribe": { arrange: recipientRemoval("Unsubscribe") },
      },
    },
    {
      what: "an offline press past the request deadline",
      cases: {
        "offline-mark-all": { arrange: markAllPressed("Inbox"), offline: true },
        "offline-dismiss": { arrange: threadDismissOffline, offline: true },
        "offline-bulk-snooze": { arrange: bulkSnoozeOffline, offline: true },
      },
    },
  ],
};

/** The drawer's rows: the causes the Inbox's own row holds, over the project List. */
export const DRAWER_SCENE: Scene = {
  seed: seedSurfaces,
  rows: [
    markAllRow("Inbox drawer"),
    {
      what: "an offline press past the request deadline",
      cases: {
        "offline-mark-all": { arrange: markAllPressed("Inbox drawer"), offline: true },
      },
    },
  ],
};
