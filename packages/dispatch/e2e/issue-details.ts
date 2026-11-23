import { expect, type Locator, type Page } from "@playwright/test";

/**
 * Today's issue header's Details (`web/src/features/issue/HeaderDetails.tsx`): the properties a
 * reader does not act on in the header itself - what the issue advances, who opened it, its
 * message route, subscribers, what references it, its parent, repository, components and links -
 * each a row of a name and a value with the control that edits it. Closed until the reader opens
 * it; then kept open for them in that browser, on every issue, until they close it.
 */
export function issueDetailsButton(page: Page): Locator {
  return page.getByTestId("issue-header").getByRole("button", { exact: true, name: "Details" });
}

/** Opens Details as a reader does, by its button, unless it is open already, and answers its
 *  panel once it is drawn. */
export async function openIssueDetails(page: Page): Promise<Locator> {
  const button = issueDetailsButton(page);
  await expect(button).toBeVisible();
  if ((await button.getAttribute("aria-expanded")) !== "true") await button.click();
  const panel = page.getByTestId("issue-header").getByTestId("issue-details");
  await expect(panel).toBeVisible();
  return panel;
}

/** One row of Details by its id (`issue-details-<id>`): `advances`, `opened-by`, `route`,
 *  `subscribers`, `referenced-by`, `parent`, `repository`, `components`, `pull-requests`, `links`. */
export function issueDetailsRow(page: Page, id: string): Locator {
  return page.getByTestId("issue-header").getByTestId(`issue-details-${id}`);
}
