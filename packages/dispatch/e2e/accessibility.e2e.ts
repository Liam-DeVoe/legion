/** The accessibility layer's real-stack half: the pages of `e2e/review-routes.ts`'s scanned set
 *  that carry a `realStackScan` (`realStackScannedRoutes`), each read in light and in dark by the
 *  scan of `e2e/accessibility-scan.ts`. Every other scanned page is read against a recording by
 *  `e2e/accessibility.stub.e2e.ts`, whose rows carry `@pr` and reach every pull request.
 *
 *  These pages stay here because a part of each is drawn from something no recording replays -
 *  the live document's Yjs socket, or a `/ws/live` frame the stubbed layer's contract fixtures do
 *  not carry - and each entry's `realStackScan` says which. Scanned against a recording, such a
 *  page would be read without that part (packages/dispatch/AGENTS.md, "The stubbed layer"). The
 *  rows are untagged: the pull-request lane already holds its 150 real-stack rows, so `chromium`
 *  runs them on the full lane, which the merge queue and the nightly run. */
import { expectNoStaleExclusion, scanRoute } from "./accessibility-scan";
import { expectTimeout } from "./expect-timeout";
import { realStackScannedRoutes } from "./review-routes";
import { seedReviewRoutes } from "./review-seed";
import { test } from "./test";
import { withAlice } from "./users";

for (const route of realStackScannedRoutes) {
  // SLOW(>=2s): a database reset, the review corpus, a sign-in and two axe scans of a drawn page.
  test(`${route.name} has no serious or critical accessibility violation in light or dark`, async ({
    browser,
  }, testInfo) => {
    // The seed's waits give up at the bound this row's own assertions use, inside the row's clock.
    const seed = await seedReviewRoutes({ boundMs: expectTimeout });
    await withAlice(browser, (page) => scanRoute(page, route, seed, testInfo));
  });
}

test.afterAll(expectNoStaleExclusion);
