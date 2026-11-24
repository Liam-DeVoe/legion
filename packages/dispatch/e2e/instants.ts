/** The assertion a row makes on the moment a `<time>` element names in its `datetime`.
 *
 *  It is a module of its own because two files of rows read such a moment, and in both the
 *  server wrote it: the times of an unresponsive claim holder (`claim-unresponsive.e2e.ts`), and
 *  the deadline of an expired credential request, which the landing reads from the broker's list
 *  and writes again (`credential-expired.e2e.ts`). */

import { expect, type Locator } from "@playwright/test";

// expectInstant reads a <time> element's `datetime` and compares the moment it names with want,
// rather than its spelling: the server writes the moment as Go formats it, which drops a trailing
// zero from the milliseconds (`…:32.15Z` for 150 ms), while `Date.toISOString` always writes
// three digits. A comparison of the strings fails on that difference alone whenever the
// milliseconds end in zero, which is about one run in ten, and it failed so in both files above.
// Each of them rounds its moment to a millisecond that ends in zero, so every run meets Go's
// spelling and this comparison of moments is what their rows rest on.
export async function expectInstant(locator: Locator, want: number, what: string) {
  await expect(locator).toBeVisible();
  await expect
    .poll(async () => Date.parse((await locator.getAttribute("datetime")) ?? ""), {
      message: `${what} names the moment ${new Date(want).toISOString()}`,
    })
    .toBe(want);
}
