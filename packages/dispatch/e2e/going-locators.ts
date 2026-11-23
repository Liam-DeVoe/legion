import { expect, type Locator, type Page } from "@playwright/test";

// The command bar's locators, and the keys that open it, ask it and approve its plan, built from
// those locators alone. The answer panel and its lines are read by the test ids
// `features/search/AnswerPanel.tsx` gives them, the conversation, its steps and its plan by those
// of `features/going`.
//
// This module imports nothing that reaches `./psql`, so the visual lane's bar rows
// (`e2e/going-bar.visual.e2e.ts`), which run where no `psql` binary is, take these as every row of
// the models lane does. What seeds an issue or reads the server for those rows is
// `e2e/going-panel.ts`'s.

/** How long a row waits for one answer to end: the run's own window (`GOING_RUN_MS`, 30 s) and
 *  the stream's grace past it. */
export const ANSWER_ENDS_MS = 40_000;

/** How long a row waits for a run of the fake model to reach a step, a plan or its end. */
export const RUN_STEPS_MS = 40_000;

export function bar(page: Page): Locator {
  return page.getByRole("dialog", { name: "Search" });
}

/** The bar's one box: the search box before Enter, and the same element under the conversation
 *  after it (`features/search/SearchPalette.tsx`), whose name and role change with it. */
export function searchBox(page: Page): Locator {
  return bar(page).getByTestId("bar-box");
}

/** The line under the bar's box that says what Enter does where the box's words do not. */
export function boxHint(page: Page): Locator {
  return bar(page).getByTestId("going-box-hint");
}

export function answerPanel(page: Page): Locator {
  return page.getByTestId("going-answer");
}

export function answerText(page: Page): Locator {
  return page.getByTestId("going-answer-text");
}

/** The line on top of the bar's list: the next step the words lead to (`search-option-line`). */
export function lineOnTop(page: Page): Locator {
  return bar(page).locator("#search-option-line");
}

/** The newest run of the conversation on the screen. */
export function newestRun(page: Page): Locator {
  return page.getByTestId("going-run").last();
}

/** The panel at the side of the page. */
export function sidePanel(page: Page): Locator {
  return page.getByTestId("going-side-panel");
}

/** The plan card of the conversation's newest run, in the bar or the panel. */
export function planCard(page: Page): Locator {
  return page.getByTestId("going-plan");
}

/** The rows of the open plan the page draws on itself. */
export function planOnPage(page: Page): Locator {
  return page.getByTestId("going-plan-on-page");
}

/** The plan rows of `where`, a card or the page's drawing. */
export function planRows(where: Locator): Locator {
  return where.getByTestId("going-plan-row");
}

/** Opens `path` and waits for the shell, whose search button opens the bar. */
export async function openShell(page: Page, path: string): Promise<void> {
  await page.goto(path);
  await expect(page.getByRole("button", { name: /^search/i })).toBeVisible();
}

/** Opens the bar over the page with `Control+K`, from the page's body. */
export async function openBar(page: Page): Promise<void> {
  await page.locator("body").focus();
  await page.keyboard.press("Control+k");
  await expect(searchBox(page)).toBeFocused();
}

/** Closes the bar with `Escape`. */
export async function closeBar(page: Page): Promise<void> {
  await page.keyboard.press("Escape");
  await expect(bar(page)).toHaveCount(0);
}

/** Opens `path`, waits for the shell, and opens the bar over it with `Control+K`. */
export async function openBarOver(page: Page, path: string): Promise<void> {
  await openShell(page, path);
  await openBar(page);
}

/** Puts `words` in the box in one input, so the bar's pause starts once, and waits for the answer
 *  the pause asks for to end. */
export async function askAndWait(page: Page, words: string): Promise<void> {
  await searchBox(page).fill(words);
  await expect(answerPanel(page)).toHaveAttribute("data-state", "ended", {
    timeout: ANSWER_ENDS_MS,
  });
}

/** Types `words` in the bar's box and presses Enter on the line on top, which starts a run. */
export async function askTheAgent(page: Page, words: string): Promise<void> {
  await searchBox(page).fill(words);
  await expect(lineOnTop(page)).toBeVisible();
  await searchBox(page).press("Enter");
  await expect(newestRun(page)).toBeVisible({ timeout: RUN_STEPS_MS });
}

/** Approves the newest run's waiting plan from the bar's empty box once the bar has read the plan,
 *  and waits for the run to end done. */
export async function approveAndFinish(page: Page): Promise<void> {
  await expect(newestRun(page)).toHaveAttribute("data-state", "waiting", { timeout: RUN_STEPS_MS });
  await expect(planCard(page).last()).toHaveAttribute("data-state", "waiting");
  await searchBox(page).focus();
  await searchBox(page).press("Enter");
  await expect(newestRun(page)).toHaveAttribute("data-state", "done", { timeout: RUN_STEPS_MS });
}
