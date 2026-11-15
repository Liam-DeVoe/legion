import { createAsk, createComment, createIssue, createMessage, createProject } from "./api";
import { nodeRow, openIssueAddress, tree } from "./issue-page";
import { boxesApart } from "./layout";
import { type BrowserContext, expect, type Locator, type Page, type Route, test } from "./test";
import { asUser } from "./users";

// The iPhone 13 viewport INFRA-95 measured the landing fault at, below the 390x844 `phone`
// layout `phone-conversation.e2e.ts` uses.
const iphone = { height: 664, width: 390 };
const session = {
  actor: { kind: "session" as const, id: "e2e-phone-landing" },
  as: "agent" as const,
};

// The panel's list of the conversation, which these rows held to opening on its newest message
// whichever of its reads answered last (INFRA-303), is gone (dispatch://INFRA-1196 R14): the
// conversation is the outline, read from the same three reads, and what a phone reader must
// have on the first screen of it is the question that waits on them (R6), clear of the line to
// write in at the foot (R16). These rows hold that landing to the same three orders and the same
// failure and absence the list's rows held its own landing to. The issue's own read carries its
// open questions (`tree-drafts.ts`'s `addAsks`), so a question waiting on the reader is drawn
// even while the asks read has failed or not answered, and the reader lands on it then too.

/** Seeds project CORE and an issue titled `title` whose conversation holds an open question
 *  waiting on its person, a root comment a person wrote, and nine messages an agent wrote. */
async function seedConversation(title: string) {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title });
  const question = await createAsk(
    issue.key,
    { question: "Which landing should the reader get?" },
    session
  );
  await createComment(issue.key, { body: "A root comment under the ask" });
  for (let index = 0; index < 9; index += 1) {
    await createMessage(issue.key, { body: `Agent message ${index}` }, session);
  }
  return { issue, question };
}

/** The three reads the outline's conversation is built from, under the keys the page reads them
 *  by: the asks, the comments, the messages. */
const READS = ["asks", "comments", "messages"] as const;

/** Matches the outline's read `read` for `issueKey`. */
function conversationRead(issueKey: string, read: (typeof READS)[number]): (url: URL) => boolean {
  return (url) => url.pathname === `/api/v1/issues/${issueKey}/${read}`;
}

/** The question's row in the outline. */
function questionRow(page: Page, askId: string): Locator {
  return nodeRow(page, askId).first();
}

/** A new page in `context` at the `iphone` viewport. */
async function openPhonePage(context: BrowserContext): Promise<Page> {
  const page = await context.newPage();
  await page.setViewportSize(iphone);
  return page;
}

/** Waits until `row` has finished rendering: visible, then its rect unchanged over 10 consecutive
 *  frames, sampled in the page. It only samples: nothing here scrolls, focuses, taps or hovers,
 *  so where the page put the row is where it is measured. */
async function settle(row: Locator, label: string): Promise<void> {
  await expect(row, `${label}: the question's row`).toBeVisible({ timeout: 20_000 });
  const never = await row.evaluate((element) => {
    const { promise, resolve } = Promise.withResolvers<string | null>();
    const deadline = Date.now() + 20_000;
    let last = "";
    let unchanged = 0;
    const sample = () => {
      const rect = JSON.stringify(element.getBoundingClientRect());
      unchanged = rect === last ? unchanged + 1 : 0;
      last = rect;
      if (unchanged >= 10) resolve(null);
      else if (Date.now() > deadline) resolve(last);
      else requestAnimationFrame(sample);
    };
    sample();
    return promise;
  });
  if (never !== null) {
    throw new Error(`${label}: the question's row never held still within 20 s (last ${never})`);
  }
}

/** One-shot: the question's row starts inside the first screen and shares no point with the
 *  line to write in at the foot of the outline. */
async function expectQuestionInView(page: Page, row: Locator, label: string): Promise<void> {
  const box = await row.locator("[data-node-card]").first().boundingBox();
  const composer = await page.getByTestId("issue-composer").boundingBox();
  const view = await page.evaluate(() => ({ height: window.innerHeight, scrollY: window.scrollY }));
  if (box === null) throw new Error(`${label}: expected the question's row to have a box`);
  if (composer === null) throw new Error(`${label}: expected the composer to have a box`);
  const where = `the question at ${JSON.stringify(box)}, the composer at ${JSON.stringify(composer)}, the viewport ${view.height} tall at window.scrollY ${view.scrollY}`;
  expect(
    box.y >= 0 && box.y < view.height,
    `${label}: the question is off the first screen: ${where}`
  ).toBe(true);
  expect(
    boxesApart(box, composer),
    `${label}: the question shares a point with the composer: ${where}`
  ).toBe(true);
}

/** Opens the conversation on a fresh page with `last` held until the other two reads have
 *  answered and the page has had time to draw what they brought, then lets `last` answer. */
async function openWithReadLast(
  context: BrowserContext,
  issueKey: string,
  last: (typeof READS)[number]
): Promise<Page> {
  const page = await openPhonePage(context);
  const held: Route[] = [];
  let released = false;
  const heldRead = conversationRead(issueKey, last);
  const hold = async (route: Route) => {
    if (released) await route.continue();
    else held.push(route);
  };
  await page.route(heldRead, hold);
  const answered = READS.filter((read) => read !== last).map((read) =>
    page.waitForEvent("requestfinished", {
      predicate: (request) => conversationRead(issueKey, read)(new URL(request.url())),
    })
  );
  await page.goto(`/issues/${issueKey}/conversation`);
  await Promise.all(answered);
  await expect.poll(() => held.length).toBeGreaterThanOrEqual(1);
  // No in-page signal says the page has drawn the partial data, so this fixed margin - two
  // macrotasks, two frames and 300 ms - is the defended choice.
  await page.evaluate(async () => {
    for (let i = 0; i < 2; i++) {
      const turn = Promise.withResolvers<void>();
      setTimeout(turn.resolve, 0);
      await turn.promise;
    }
    for (let i = 0; i < 2; i++) {
      const frame = Promise.withResolvers<void>();
      requestAnimationFrame(() => frame.resolve());
      await frame.promise;
    }
    const margin = Promise.withResolvers<void>();
    setTimeout(margin.resolve, 300);
    await margin.promise;
  });
  // The held reads answer before the handler goes, which would otherwise hand them on itself.
  released = true;
  for (const route of held) await route.continue();
  await page.unroute(heldRead, hold);
  return page;
}

// SLOW(>=2s): three opens, each a fresh page, each holding the 300 ms margin the partial-data
// state has no in-page signal for.
// @pr: The phone conversation's first paint: the question waiting on the reader is on the first screen whichever of its reads answers last.
test("on a phone, the conversation opens with its open question in view whichever read answers last @phone @pr", async ({
  browser,
}) => {
  const { issue, question } = await seedConversation("Landing on the open question");
  const alice = await asUser(browser, "alice");

  try {
    let page: Page | undefined;
    for (const last of READS) {
      const label = `${last} answering last`;
      await page?.close();
      page = await openWithReadLast(alice, issue.key, last);
      await settle(questionRow(page, question.id), label);
      await expectQuestionInView(page, questionRow(page, question.id), label);
    }
  } finally {
    await alice.close();
  }
});

// A failed asks read leaves the question the issue's own read carries, and the reader lands on it;
// a refetch that then brings the asks keeps them there. The refetch is TanStack Query's own on
// focus: the `visibilitychange` this row dispatches on `document` bubbles to `window`, where its
// focusManager listens, and a read in error holds no data, so it is fetched again.
test("on a phone, a conversation whose asks read failed opens on its open question, and a refetch keeps it there @phone", async ({
  browser,
}) => {
  const { issue, question } = await seedConversation("Landing after a failed read");
  const alice = await asUser(browser, "alice");

  try {
    const page = await openPhonePage(alice);
    const asksRead = conversationRead(issue.key, "asks");
    let failing = true;
    let failed = 0;
    await page.route(asksRead, async (route) => {
      if (!failing) {
        await route.continue();
        return;
      }
      failed += 1;
      await route.fulfill({
        body: JSON.stringify({ code: "SERVER_ERROR", error: "boom" }),
        contentType: "application/json",
        status: 500,
      });
    });
    await page.goto(`/issues/${issue.key}/conversation`);
    await expect(tree(page)).toBeVisible();
    // The app's own retries of the asks read fail too before the page settles on its failure.
    await expect.poll(() => failed, { timeout: 15_000 }).toBeGreaterThanOrEqual(3);
    await settle(questionRow(page, question.id), "with the asks read failed");
    await expectQuestionInView(page, questionRow(page, question.id), "with the asks read failed");

    failing = false;
    const answered = page.waitForResponse(
      (response) => asksRead(new URL(response.url())) && response.status() === 200
    );
    await page.evaluate(() =>
      document.dispatchEvent(new Event("visibilitychange", { bubbles: true }))
    );
    await answered;
    await settle(questionRow(page, question.id), "after the refetch");
    await expectQuestionInView(page, questionRow(page, question.id), "after the refetch");
  } finally {
    await alice.close();
  }
});

// INFRA-303: a reader who leaves the conversation while it is still being read was never in it,
// so coming back is a first open, not a return to a place: they land on the question, which they
// had on the first screen before the asks read answered too.
test("on a phone, a reader who leaves the conversation while it loads lands on its open question when they come back @phone", async ({
  browser,
}) => {
  const { issue, question } = await seedConversation("Leaving while it loads");
  const alice = await asUser(browser, "alice");

  try {
    const page = await openPhonePage(alice);
    const asksRead = conversationRead(issue.key, "asks");
    const held: Route[] = [];
    await page.route(asksRead, (route) => {
      held.push(route);
    });
    await page.goto(`/issues/${issue.key}/conversation`);
    await expect.poll(() => held.length).toBeGreaterThanOrEqual(1);
    await settle(questionRow(page, question.id), "before the asks read answers");
    await expectQuestionInView(
      page,
      questionRow(page, question.id),
      "before the asks read answers"
    );

    await openIssueAddress(page, "activity");
    const answered = page.waitForResponse(
      (response) => asksRead(new URL(response.url())) && response.status() === 200
    );
    for (const route of held) await route.continue();
    await page.unroute(asksRead);
    await answered;

    await openIssueAddress(page, "conversation");
    await settle(questionRow(page, question.id), "back on the conversation");
    await expectQuestionInView(page, questionRow(page, question.id), "back on the conversation");
  } finally {
    await alice.close();
  }
});
