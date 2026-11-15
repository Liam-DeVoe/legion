import {
  answerAsk,
  claimIssue,
  createAsk,
  createComment,
  createIssue,
  createIssueArtifact,
  createMessage,
  createProject,
  getIssueEvents,
  patchIssue,
  putIssueState,
  resolveAsk,
  resolveComment,
} from "./api";
import {
  focusedNodeFacts,
  focusedNodeId,
  historyEntries,
  historyItem,
  historyPanel,
  nodeRow,
  openDetails,
  tree,
} from "./issue-page";
import { setEventCreatedAt } from "./seed";
import { expect, test } from "./test";
import { asUser } from "./users";

const agent = {
  actor: { id: "e2e-activity-agent", kind: "session" as const },
  as: "agent" as const,
};

// The issue's history is the list in Details that `/activity` opens (dispatch://INFRA-1196 R15):
// every event no row of the outline draws, and the threads agents wrote among themselves, whole.
// What the outline draws - an ask, answered or retracted - is not in it, and nothing the
// history draws is a row of the outline.
// @pr: The issue's event history, which no other row reads whole.
test("the history lists the events and the agents' threads the outline does not draw @pr", async ({
  browser,
}, testInfo) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "Review this specification.",
    title: "One card per conversation",
  });
  const answered = await createAsk(
    issue.key,
    { options: [{ label: "Ship" }], question: "Ship it?" },
    agent
  );
  await answerAsk(answered.id, { expected_edited_at: null, selected: ["Ship"] }, { login: "bob" });
  const retracted = await createAsk(issue.key, { question: "Withdrawn question?" }, agent);
  await resolveAsk(retracted.id, { kind: "retracted", reason: "Superseded." }, agent);
  const resolved = await createComment(issue.key, { body: "A resolved thread." }, agent);
  await resolveComment(resolved.id);
  const open = await createComment(issue.key, { body: "An open thread." }, agent);
  const message = await createMessage(issue.key, { body: "A message thread." }, agent);
  await patchIssue(issue.key, { status: "in_progress" });
  await claimIssue(issue.key, agent);
  await createIssueArtifact(issue.key, { content: "# Note\n", name: "note.md" });

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${issue.key}/activity`);
    const history = historyPanel(page);
    await expect(history).toBeVisible();
    const rows = historyEntries(page);
    // The issue's own events, each a line of its own.
    for (const description of ["updated the issue", "claimed the issue", "added note.md"]) {
      await expect(rows.filter({ hasText: description })).toHaveCount(1);
    }
    // The threads agents wrote among themselves, each once, whole, with its Reply - and no row
    // of the outline.
    for (const id of [resolved.id, open.id, message.id]) {
      await expect(historyItem(page, id)).toHaveCount(1);
      await expect(history.locator(`[data-history-reply="${id}"]`)).toHaveCount(1);
      await expect(nodeRow(page, id)).toHaveCount(0);
    }
    await expect(historyItem(page, open.id)).toContainText("An open thread.");
    // The asks are rows of the outline, answered and retracted, and no line of the history.
    for (const ask of [answered, retracted]) await expect(nodeRow(page, ask.id)).not.toHaveCount(0);
    await expect(history).not.toContainText("Ship it?");
    await expect(history).not.toContainText("Withdrawn question?");
    await expect(tree(page)).not.toContainText("An open thread.");
    // The panel's old list of the conversation is gone.
    await expect(page.getByRole("list", { name: "Conversation turns" })).toHaveCount(0);
    await page.screenshot({
      fullPage: true,
      path: testInfo.outputPath(`activity-rows-${testInfo.project.name}.png`),
    });
  } finally {
    await alice.close();
  }
});

// SLOW(>=2s): four status moves, three of them back-dated a day and the read watermark set,
// before one cold load of the history. A day before today is one line with its count, which
// opens it; the reader's watermark is drawn where it falls, inside that day.
test("the history folds each day before today and marks where the reader stopped", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Dividers" });
  await patchIssue(issue.key, { status: "in_progress" });
  await patchIssue(issue.key, { status: "testing" });
  await patchIssue(issue.key, { status: "needs_review" });
  await patchIssue(issue.key, { status: "in_progress" });

  const events = await getIssueEvents(issue.key, { limit: 100 });
  const created = events.find((event) => event.type === "issue.created");
  const updates = events
    .filter((event) => event.type === "issue.updated")
    .sort((one, other) => one.seq - other.seq);
  const [first, second, third, fourth] = updates;
  if (
    created === undefined ||
    first === undefined ||
    second === undefined ||
    third === undefined ||
    fourth === undefined
  ) {
    throw new Error("seeded divider events were not recorded");
  }
  const yesterday = new Date(Date.now() - 24 * 60 * 60 * 1_000).toISOString();
  await Promise.all([
    setEventCreatedAt(created.id, yesterday),
    setEventCreatedAt(first.id, yesterday),
    setEventCreatedAt(second.id, yesterday),
  ]);
  await putIssueState(issue.key, { last_read_seq: second.seq });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The outline's end is the one writer of the watermark (`IssueReadMark.tsx`), and it is on
    // this short page's first screen beside Details: held here, so the watermark this row reads
    // stays where the seed put it.
    await page.route(`**/api/v1/me/issues/${issue.key}/state`, (route) =>
      route.request().method() === "PUT" ? route.abort() : route.fallback()
    );
    await page.goto(`/issues/${issue.key}/activity`);

    const days = historyPanel(page).locator("[data-history-day]");
    await expect(days).toHaveCount(2);
    await expect(days.first()).toContainText("Today");
    const earlier = days.nth(1).getByRole("button", { name: "Yesterday · 3" });
    await expect(earlier).toHaveAttribute("aria-expanded", "false");
    // Today's lines are drawn; yesterday's are behind its one line.
    const order = () =>
      historyPanel(page)
        .locator("[data-event-seq], li[aria-label]")
        .evaluateAll((items) =>
          items.map(
            (item) => item.getAttribute("data-event-seq") ?? item.getAttribute("aria-label")
          )
        );
    expect(await order()).toEqual([String(fourth.seq), String(third.seq)]);

    await earlier.click();
    await expect(earlier).toHaveAttribute("aria-expanded", "true");
    // Newest first: the watermark sits below the oldest unread line and above the newest line
    // the reader had already seen, which here is the first line of the day before.
    expect(await order()).toEqual([
      String(fourth.seq),
      String(third.seq),
      "New since you last read",
      String(second.seq),
      String(first.seq),
      String(created.seq),
    ]);
  } finally {
    await alice.close();
  }
});

// A line of the history pins its event: the line's own Pin, which a reload keeps, and its Unpin.
// The margin's Pinned tab, where an event pin used to be read back, is drawn beside an opened
// document alone now (`e2e/stub/moved-row-notes.ts`), so the line itself is where it is read.
test("a history line pins its event, the pin outlives a reload, and the line unpins it", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Pin an event" });
  await patchIssue(issue.key, { status: "in_progress" });

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${issue.key}/activity`);
    const row = historyEntries(page).filter({ hasText: "updated the issue" });
    await expect(row).toHaveCount(1);
    await row.getByRole("button", { name: "Pin" }).click();
    await expect(row.getByRole("button", { name: "Unpin" })).toHaveAttribute(
      "aria-pressed",
      "true"
    );
    // The pin is one write of the page's store: nothing is pending once Dispatch has it.
    await expect(page.getByRole("region", { name: "Pending changes" })).toHaveCount(0);

    await page.reload();
    const again = historyEntries(page).filter({ hasText: "updated the issue" });
    await expect(again.getByRole("button", { name: "Unpin" })).toHaveAttribute(
      "aria-pressed",
      "true"
    );
    await again.getByRole("button", { name: "Unpin" }).click();
    await expect(again.getByRole("button", { name: "Pin" })).toHaveAttribute(
      "aria-pressed",
      "false"
    );
  } finally {
    await alice.close();
  }
});

// The fake listener holds no record of `ghost`, so the mention is refused as `no live session
// ghost` and its receipt settles `failed`. A person's comment is a row of the outline, and its
// deliveries are the focused row's facts in Details: the one there must not read as delivered.
// The history draws no event of a row the outline draws (R15), so the line the old Activity tab
// drew for this delivery is no line now.
test("a mention that could not be delivered reads as not delivered in the focused row's facts", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Mention of a missing session" });
  const comment = await createComment(
    issue.key,
    { body: "Look at this, @session:ghost.", mentions: [{ target: "session:ghost" }] },
    { login: "alice" }
  );
  await expect
    .poll(async () =>
      (await getIssueEvents(issue.key, { limit: 100 })).flatMap((event) =>
        event.type === "comment.delivery" ? [event.payload.state] : []
      )
    )
    .toEqual(["failed"]);

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${issue.key}/comments/${comment.id}`);
    await expect.poll(() => focusedNodeId(page)).toBe(comment.id);
    await openDetails(page);
    const deliveries = focusedNodeFacts(page).locator("[data-deliveries] li");
    await expect(deliveries).toHaveCount(1);
    await expect(deliveries).toContainText("Not delivered");
    await expect(deliveries).not.toContainText("Delivered");
    // Details' history, open beside the facts, draws no line of the comment or its delivery.
    await expect(historyPanel(page)).toBeVisible();
    await expect(historyEntries(page).filter({ hasText: "mention" })).toHaveCount(0);
  } finally {
    await alice.close();
  }
});

// A comment body is bounded block by block, so one far longer than an event carries rides its
// event as its first part. The history draws a thread agents wrote among themselves from the
// comment itself, not from its event, so the whole body is there, and the comment's time links
// to its own address.
test("the history draws an agent's comment longer than one event whole, linked to its address", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "A comment longer than one event" });
  // Two 20,000-unit paragraphs: each is inside the 32,000-unit block bound, and together they
  // are past the 32,000 units one event carries, so the event keeps the first paragraph alone.
  const first = "A".repeat(20_000);
  const second = "B".repeat(20_000);
  const comment = await createComment(issue.key, { body: `${first}\n\n${second}` }, agent);

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${issue.key}/activity`);
    const item = historyItem(page, comment.id);
    await expect(item).toHaveCount(1);
    await expect(item).toContainText(first);
    await expect(item).toContainText(second);

    // The turn's time is the link to its own address; its author's name links to the agent.
    await item.locator(`a[href$="/comments/${comment.id}"]`).click();
    await expect(page).toHaveURL(
      new RegExp(`/issues/${issue.key}/comments/${comment.id}(\\?|#|$)`)
    );
    await expect(historyItem(page, comment.id)).toHaveAttribute("aria-current", "true");
  } finally {
    await alice.close();
  }
});
