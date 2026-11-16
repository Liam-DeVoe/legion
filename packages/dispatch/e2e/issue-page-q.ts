import { expect, type Locator, type Page } from "@playwright/test";

import { getIssue } from "./api";
import { issuePage, presetRow } from "./issue-page";

/**
 * How part Q's rows read the Decisions view of the new issue page (dispatch://INFRA-1196 part Q,
 * `e2e/issue-page-q.e2e.ts`): the readers that D's `e2e/issue-page.ts`, G's `e2e/issue-page-g.ts`
 * and I's `e2e/issue-page-i.ts` do not hold (decision 2 of the parts). No later part changes this
 * file; a later part's rows import from it.
 *
 * What the view draws (`decisions-filter/DecisionsFilterBody.tsx`):
 *
 * - **The list** `[data-decisions-body]`, a region named by the view, holding the tree.
 * - **Each part** `[data-decisions-part="open_questions" | "review" | "yours" | "others" |
 *   "agents"]` with its heading `[data-decisions-heading]` and count `[data-decisions-count]`,
 *   its rows the outline's own `[data-node-row]`; the foot's control is the `button` in
 *   `[data-decisions-foot]`.
 * - **The Decisions control's flag** `[data-preset-flag="to_review"]` in D's view row, whose
 *   number is how many decisions wait for the reader's review.
 */

/** The selectors the readers pass into the browser. */
const SELECTORS = {
  issuePage: "[data-issue-page]",
} as const;

/** One part of the Decisions view, as the page draws it. */
export interface DrawnPart {
  readonly count: string;
  readonly heading: string;
  readonly part: string;
  readonly rows: readonly string[];
}

/** Each part of the Decisions view, in the order it is drawn. */
export function decisionsPartsDrawn(page: Page): Promise<DrawnPart[]> {
  return page.evaluate((selector) => {
    const text = (element: Element | null) =>
      (element?.textContent ?? "").replace(/\s+/g, " ").trim();
    return [...document.querySelectorAll(`${selector} [data-decisions-part]`)].map((part) => ({
      count: text(part.querySelector("[data-decisions-count]")),
      heading: text(part.querySelector("[data-decisions-heading]")),
      part: part.getAttribute("data-decisions-part") ?? "",
      rows: [...part.querySelectorAll("[data-node-row]")].map(
        (row) => row.getAttribute("data-node-id") ?? ""
      ),
    }));
  }, SELECTORS.issuePage);
}

/** The control at the Decisions view's foot, which unfolds the agents' untied decisions. */
export function decisionsFoot(page: Page): Locator {
  return issuePage(page).locator("[data-decisions-foot] button");
}

/** The number the Decisions control of D's view row draws before "to review", where it draws
 *  the five controls apart (from 1280 px up). It is not drawn where nothing waits. */
export function reviewFlag(page: Page): Locator {
  return presetRow(page).locator('[data-preset-flag="to_review"] [data-author-text]');
}

/** The slug of the issue's own spec, which is how its blocks are addressed. */
export async function primarySlug(key: string): Promise<string> {
  const primary = ((await getIssue(key)).artifacts ?? []).find((artifact) => artifact.primary);
  expect(primary, `${key} carries no primary document`).toBeDefined();
  return primary?.slug ?? "";
}
