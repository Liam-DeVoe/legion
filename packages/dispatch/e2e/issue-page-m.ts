import { expect, type Locator, type Page } from "@playwright/test";

import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";
import { gotoIssue, marginShell, openDetails } from "./issue-page";
import { focusRow, nodeRow, unfoldEveryBranch } from "./issue-page-g";
import { focusedNodeId, openEveryDeeperBranch } from "./issue-page-i";
import { settledState } from "./settled";

/**
 * What the rows of `e2e/issue-page-m.e2e.ts` read of Details on the new issue page: the
 * history, the facts of the focused row, and the way to them.
 *
 * This file is those rows' own: it imports the readers of `e2e/issue-page.ts`,
 * `e2e/issue-page-g.ts` and `e2e/issue-page-i.ts`, and `settledState` of `e2e/settled.ts`, and
 * copies none of them.
 *
 * The DOM a row reads, all of it inside Details (`[data-testid="desktop-margin-shell"]`):
 *
 * - **The history** `[data-testid="issue-history"]`, a `section` named "History"
 *   (`IssueHistory.tsx`). Each day is `[data-history-day="<YYYY-MM-DD>"]`, a day before today a
 *   `button` "<day> · <count>" with `aria-expanded` ("<day>" alone for the oldest day while older
 *   pages of the log are left, whose count is not known yet); each entry carries
 *   `data-history-entry` and `data-event-seq` (0 for a thread whose first turn's event is on a
 *   page not read yet); a turn of an agents' thread is `[data-history-item="<id>"]`, its Reply
 *   `[data-history-reply]` and its time the link `[data-history-time]`; an upload run is
 *   `[data-history-uploads="<n>"]`.
 * - **The facts** `[data-testid="focused-node-facts"]`, carrying `data-focused-node` with the
 *   focused node's id, or the issue's key with no node focused (`FocusedNodeFacts.tsx`); its
 *   deliveries are `[data-deliveries] li`, its controls `[data-focused-controls]`, and what
 *   references the row is `[data-node-references]` (`node-references/NodeReferences.tsx`).
 * - **The people** in the People section: "Subscribers" and the row
 *   `[data-details-row="collaborators"]` (`details-more.tsx`).
 */

/** The issue's history, in Details. */
export function historyPanel(page: Page): Locator {
  return page.getByTestId("issue-history");
}

/** One turn of an agents' thread in the history, by the comment's or the message's id. */
export function historyItem(page: Page, id: string): Locator {
  return historyPanel(page).locator(`[data-history-item="${id}"]`);
}

/** Every entry of the history, newest first. */
export function historyEntries(page: Page): Locator {
  return historyPanel(page).locator("[data-event-seq]");
}

/**
 * The day the history files a time under, worked out in the page from the browser's own clock as
 * the page works it out at each render: the local date (`dateKey` of
 * `features/activity/activity-model.ts`), which `data-history-day` carries, and the name the
 * day's line reads, "Today", "Yesterday" or the date written out (`buildHistory`, `dayLabel`).
 * The server stamps each seed with its own time, so a seed made before the reader's midnight and
 * read after it is a day before the reader's today: a row names a day by this, never by "Today"
 * or "Yesterday" written out.
 */
export interface HistoryDayRead {
  readonly date: string;
  readonly label: string;
  readonly today: boolean;
}

/** The day the history files the time `at` under, and its name (`HistoryDayRead`). */
export function historyDayOf(page: Page, at: string): Promise<HistoryDayRead> {
  return page.evaluate(
    ({ time, words }) => {
      const key = (moment: Date) =>
        `${moment.getFullYear()}-${String(moment.getMonth() + 1).padStart(2, "0")}-${String(moment.getDate()).padStart(2, "0")}`;
      const today = key(new Date());
      const date = key(new Date(time));
      // Yesterday is read back from today's noon, as the page reads it.
      const noon = new Date(`${today}T12:00:00`);
      noon.setDate(noon.getDate() - 1);
      const [year, month, day] = date.split("-").map(Number);
      const written = new Intl.DateTimeFormat(undefined, {
        day: "numeric",
        month: "long",
        weekday: "long",
        ...(date.slice(0, 4) === today.slice(0, 4) ? {} : { year: "numeric" }),
      }).format(new Date(year ?? 1970, (month ?? 1) - 1, day ?? 1));
      const label = date === today ? words.today : date === key(noon) ? words.yesterday : written;
      return { date, label, today: date === today };
    },
    {
      time: at,
      words: { today: CONTROL_LABELS.history_today, yesterday: CONTROL_LABELS.history_yesterday },
    }
  );
}

/**
 * Waits until the history draws `entry`, the entry of a seed made at `at`, found by its id
 * (`historyItem`, `[data-event-seq]`, ...). The history draws every day before the reader's
 * today folded to one line, and the page reads its today from the browser's clock at each render,
 * so a seed made before the reader's midnight and drawn after it is in a folded day: where the
 * page drew the seed's own day folded, that day is opened first. So no row assumes which day a
 * seed falls in.
 */
export async function seededHistoryEntry(page: Page, entry: Locator, at: string): Promise<Locator> {
  const { date } = await historyDayOf(page, at);
  return drawnInItsDay(
    entry,
    historyPanel(page).locator(`[data-history-day="${date}"]`),
    `the history's entry ${entry.toString()}, of ${date}`
  );
}

/** Waits until the history draws its newest entry, whatever seed it is: the first entry of its
 *  first day, that day opened first where the page drew it folded (`seededHistoryEntry`). */
export function newestHistoryEntry(page: Page): Promise<Locator> {
  const day = historyPanel(page).locator("[data-history-day]").first();
  return drawnInItsDay(day.locator("[data-event-seq]").first(), day, "the history's newest entry");
}

/** `entry` drawn, its `day` opened first where the page drew that day folded; `what` names the
 *  entry for the failure. */
async function drawnInItsDay(entry: Locator, day: Locator, what: string): Promise<Locator> {
  const line = day.locator(':scope > button[aria-expanded="false"]');
  const shown = await settledState(what, [
    ["drawn", entry],
    ["folded", line],
  ]);
  // The day is opened for the row's own reading, so its line is pressed without taking the
  // focus: a row that reads where the reader's focus is finds it where the row left it.
  if (shown === "folded") await line.dispatchEvent("click");
  await expect(entry, `${what} is not drawn`).toBeVisible();
  return entry;
}

/** The facts of the row the reader's focus is on, in Details. */
export function focusedNodeFacts(page: Page): Locator {
  return marginShell(page).getByTestId("focused-node-facts");
}

/**
 * Opens the issue, puts the focus on node `id`'s row as a reader's press does, and opens Details
 * at its facts. An item's own address (`/asks/<id>`, `/comments/<id>`) does not put the focus on
 * its node yet, so a row reaches the node through the outline instead
 * (`focusedRowFacts`).
 */
export async function rowFacts(page: Page, issueKey: string, id: string): Promise<Locator> {
  await gotoIssue(page, `/issues/${issueKey}`);
  return focusedRowFacts(page, id);
}

/**
 * On a page already on the issue, puts the focus on node `id`'s row and opens Details at its
 * facts. The comments, messages and questions arrive after the tree (`useIssueTree.ts`), and a
 * node that arrives late may arrive folded, so every branch the outline folds is opened again
 * until the row is drawn, as `expectSeedDrawn` does.
 */
export async function focusedRowFacts(page: Page, id: string): Promise<Locator> {
  await expect
    .poll(async () => {
      await unfoldEveryBranch(page);
      await openEveryDeeperBranch(page);
      return nodeRow(page, id).count();
    }, `${id} is never drawn as a row of the outline`)
    .toBeGreaterThan(0);
  await focusRow(page, id);
  await expect.poll(() => focusedNodeId(page), `${id} did not take the focus`).toBe(id);
  await openDetails(page);
  const facts = focusedNodeFacts(page);
  await expect(facts).toHaveAttribute("data-focused-node", id);
  return facts;
}

/** The facts of the ask `askId`, focused through the outline (`rowFacts`): its hand-on control
 *  and who it is asked of are there. */
export function askFacts(page: Page, issueKey: string, askId: string): Promise<Locator> {
  return rowFacts(page, issueKey, askId);
}
