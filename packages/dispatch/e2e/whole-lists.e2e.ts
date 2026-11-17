import {
  createComment,
  createIssue,
  createProject,
  getIssueEvents,
  listComments,
  patchIssue,
} from "./api";
import { accessLines, accessLogReadable } from "./harness-logs";
import { expect, test } from "./test";
import { asUser } from "./users";

// A list the page reads whole is read a page at a time at the route's own default limit, until it
// holds every row the route counts (`readWholeList`, web/src/api/client.ts): an issue's comments
// at 50 a page, so 250 of them are five reads, and its Activity a page of 200 at a time behind
// Load older. Before /api/v2 the page read each list in one answer, which a long issue made the
// heaviest read it had.

const COMMENTS = 250;
const EVENTS = 450;
/** The route's default page of comments, which the page asks for by naming no `limit`. */
const COMMENT_PAGE = 50;

// No `@pr` tag: the pull-request lane is held to 150 real-stack rows (dispatch://INFRA-1407's R1,
// e2e/__tests__/pull-request-lane.test.ts) and holds 150 already, and this row cannot be stubbed,
// since it reads the live server's access log. It runs on the full lane - `bun run e2e`, the merge
// queue and the nightly - so it still gates `main`.
test("an issue with 250 comments and 450 events shows every one, its comments read 50 a page", async ({
  browser,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== "chromium",
    "the reads are the page's client, which one browser proves"
  );
  test.skip(
    !accessLogReadable(),
    "the Dispatch server's access log is in a file only in a run whose stacks e2e/harness-pool.ts started"
  );
  test.setTimeout(240_000);

  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "A long conversation" });
  // Each comment is one `comment.created`, and each label write one `issue.updated`, so the
  // issue's log is made exactly as long as the row needs.
  for (let first = 1; first <= COMMENTS; first += 25) {
    await Promise.all(
      Array.from({ length: Math.min(25, COMMENTS - first + 1) }, (_, index) =>
        createComment(issue.key, { body: `Long comment ${first + index}` })
      )
    );
  }
  const held = (await getIssueEvents(issue.key)).length;
  for (let round = 1; round <= EVENTS - held; round += 1) {
    await patchIssue(issue.key, { labels: [`round-${round}`] });
  }
  expect(await listComments(issue.key), "the seeded comments").toHaveLength(COMMENTS);
  expect(await getIssueEvents(issue.key), "the seeded events").toHaveLength(EVENTS);

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The issue's own comment list, as the access log names its path.
    const path = `/api/v2/issues/${encodeURIComponent(issue.key)}/comments`;
    const logged = () =>
      accessLines().filter((line) => line.method === "GET" && line.path === path);
    const before = logged().length;
    const sent: URL[] = [];
    page.on("request", (request) => {
      const url = new URL(request.url());
      if (request.method() === "GET" && url.pathname === path) sent.push(url);
    });

    await page.goto(`/issues/${issue.key}/conversation`);
    const turns = page.getByRole("list", { name: "Conversation turns" });
    await expect(turns.getByText(/^Long comment \d+$/)).toHaveCount(COMMENTS, { timeout: 30_000 });

    // The page asked for the issue's comments 50 at a time: no `limit`, so the route's default,
    // and each page starting where the rows it held ended. A read of one document's comments
    // (`artifact=`) is another list, and is left out of the pages.
    const whole = sent.filter((url) => !url.searchParams.has("artifact"));
    expect(whole.map((url) => url.searchParams.get("limit"))).toEqual(
      Array.from({ length: COMMENTS / COMMENT_PAGE }, () => null)
    );
    expect(whole.map((url) => Number(url.searchParams.get("offset") ?? "0"))).toEqual(
      Array.from({ length: COMMENTS / COMMENT_PAGE }, (_, index) => index * COMMENT_PAGE)
    );
    // And the server's own record agrees: one access line for each of those reads and no other,
    // so it served the issue's 250 comments as five pages of 50.
    await expect.poll(() => logged().length - before, { timeout: 10_000 }).toBe(sent.length);
    const served = logged().slice(before);
    expect(served.map((line) => line.status)).toEqual(sent.map(() => 200));
    expect(served.length - (sent.length - whole.length)).toBe(COMMENTS / COMMENT_PAGE);

    // Every event of the issue is a row of its Activity, newest first, 200 at a time behind
    // Load older.
    await page.goto(`/issues/${issue.key}/activity`);
    const events = page.getByRole("list", { name: "Issue activity" }).locator("[data-event-seq]");
    const older = page.getByRole("button", { name: "Load older" });
    for (let shown = 200; shown < EVENTS; shown += 200) {
      await expect(events).toHaveCount(shown);
      await older.click();
    }
    await expect(events).toHaveCount(EVENTS);
    await expect(older).toHaveCount(0);
  } finally {
    await alice.close();
  }
});
