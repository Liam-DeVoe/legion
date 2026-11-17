/** What the palette rows open before they press a key, shared by `keyboard-palette.e2e.ts` and
 *  the rows of that family which run against a recording (`keyboard-palette.stub.e2e.ts`). */
import { expect, type Page } from "@playwright/test";

// The keymap is bound only once sign-in resolves (`AuthGate` renders a skeleton until
// `/auth/whoami` answers), so a key pressed before the page renders reaches no handler.
export async function openIssue(page: Page, issueKey: string, title: string): Promise<void> {
  await page.goto(`/issues/${issueKey}`);
  await expect(
    page.getByTestId("issue-header").getByRole("heading", { level: 1, name: title })
  ).toBeVisible();
  await page.locator("body").focus();
}

// The path every palette search takes, whatever tier asks it: `/api/v2/search-results`, with
// the keystroke tier's `keywords_only` and a project or an issue page's `number_project` in its
// query. A stub or a hold in either file matches the path, so no tier's request gets past it.
export const searchPath = (url: URL): boolean => url.pathname === "/api/v2/search-results";
