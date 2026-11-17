import { createAsk, createComment, createHandoff, createIssueArtifact, createMessage } from "./api";
import { expectIssuePageDrawn, nodeRow } from "./issue-page-g";
import { withIssuePage } from "./issue-page-option";
import {
  expectReadsReferences,
  formReadings,
  READING_LEAD,
  READING_SESSION_ID,
  READING_SESSION_TITLE,
  readingQuestionBlock,
  readingText,
  scopedTo,
  seedReadingWorld,
} from "./reading-rows";
import { conversationTurn, RENDERED_BODY } from "./reference-surfaces";
import { expect, test } from "./test";

/**
 * The reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`) across an issue's own pages:
 * the Activity tab's event body and an event's line, the three turns of the Conversation tab, the
 * quote above a reply and a handoff card's body there, and a decision on the Decisions tab. Each
 * row writes the one reading text into the record its surface shows, through the API as an agent
 * or a person writes it, reaches the surface the way its row of `DISPATCH_READING_SURFACES` says,
 * and reads there every form a text names a node by, drawn through the one renderer (R6).
 *
 * Every surface here is a record a reader reads, so each reference is a link, but for the quote
 * above a reply: the quote is the link to its parent's turn, so the references inside it draw as
 * their names and link nothing. A handoff is no record of the reference graph, so a `#id` in
 * its words names nothing, and its driver holds the other five forms.
 *
 * The new issue page's outline is one more of an issue's own pages (dispatch://INFRA-1196,
 * `RowText.tsx`): a row draws its node's words through the renderer, and the row's own pass makes
 * the chips its references read as. A row leaves a `#id` as written, so its driver holds the
 * other five forms too.
 */

/** The agent session these rows write as, so a write is never the reader's own. */
const readingAgent = {
  actor: { id: "e2e-reading-agent", kind: "session" as const },
  as: "agent" as const,
};

/** The heading of the one decision the decision row writes. */
const DECISION = "Read the references";

// SLOW(>=2s): seeds the reading world and a comment, loads the Activity tab, pins the comment's
// row, and opens the margin's Pinned tab, which reads the pinned event back before it draws it.
test("activity-event-body: a pinned Activity event draws every form of a stored reference through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  await createComment(world.host.key, { body: readingText(world) }, readingAgent);

  // The Activity tab draws an event's line and no body; the margin's Pinned tab is where it draws
  // an event's body (`EventBody`), so the comment's row is pinned first.
  await page.goto(`/issues/${world.host.key}/activity`);
  const row = page
    .getByRole("list", { name: "Issue activity" })
    .getByRole("listitem")
    .filter({ hasText: "commented on" });
  await expect(row).toHaveCount(1);
  await row.getByRole("button", { name: "Pin" }).click();
  await expect(row.getByRole("button", { name: "Unpin" })).toHaveAttribute("aria-pressed", "true");
  const margin = page.getByTestId("margin-sheet");
  await margin.getByRole("tab", { name: "Pinned" }).click();

  await expectReadsReferences(margin.locator("article").locator(RENDERED_BODY), world, "link");
});

// SLOW(>=2s): the reading world's seeding and an ask, then one cold load of the Activity tab;
// measured at 4.4 s on one run in an issue pod on one worker.
test("event-line: an Activity event's line draws every form of the record's text it quotes through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // An ask's question is the text of the record its `asked “…”` line quotes. The ask is the
  // host's, so a `#id` in its question is read against the host's spec, which declares it.
  await createAsk(world.host.key, { question: readingText(world) }, readingAgent);

  await page.goto(`/issues/${world.host.key}/activity`);
  // The question block the host's spec declares is an ask of its own, with an `asked “…”` line
  // too; the reading text is the one that opens with the lead word.
  const line = page
    .getByRole("list", { name: "Issue activity" })
    .getByRole("listitem")
    .filter({ hasText: `asked “${READING_LEAD}` });
  await expect(line).toHaveCount(1);

  // The line is in no link or button, so the question it quotes draws inline through the
  // renderer with its references linked (`SentenceParts`).
  await expectReadsReferences(line.locator(".dispatch-markdown"), world, "link");
});

// SLOW(>=2s): seeds the reading world - a project, the fake registry's session, four issues, a
// comment and a settled question - and a two-message thread before one cold load of the
// Conversation.
test("conversation-reply: a reply turn draws every form of a stored reference through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const root = await createMessage(world.host.key, { body: "Where does the reading start?" });
  const reply = await createMessage(
    world.host.key,
    { body: readingText(world), in_reply_to: root.id },
    readingAgent
  );

  await page.goto(`/issues/${world.host.key}/conversation`);
  // The reply's turn holds the quote of its parent as well, which is a preview and no body.
  const turn = conversationTurn(page, "message", reply.id);
  await expect(turn).toHaveAttribute("data-reply", "");

  await expectReadsReferences(turn.locator(RENDERED_BODY), world, "link");
});

test("conversation-message: a message turn draws every form of a stored reference through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const message = await createMessage(world.host.key, { body: readingText(world) }, readingAgent);

  await page.goto(`/issues/${world.host.key}/conversation`);

  await expectReadsReferences(
    conversationTurn(page, "message", message.id).locator(RENDERED_BODY),
    world,
    "link"
  );
});

test("conversation-targeted-message: a message sent to a session draws every form of a stored reference through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // A person's message sent to the live session the world lists, which the Conversation draws as
  // a targeted card, with a line naming whom it was sent to, rather than as a plain message turn.
  const message = await createMessage(world.host.key, {
    body: readingText(world),
    delivery: "steer",
    target: `session:${READING_SESSION_ID}`,
  });

  await page.goto(`/issues/${world.host.key}/conversation`);
  const card = conversationTurn(page, "message", message.id);
  await expect(card).toContainText(`Sent to ${READING_SESSION_TITLE} (Send)`);

  await expectReadsReferences(card.locator(RENDERED_BODY), world, "link");
});

test("reply-quote: the quote above a reply draws every form of its parent's references as a name inside its link", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const parent = await createMessage(world.host.key, { body: readingText(world) }, readingAgent);
  const reply = await createMessage(world.host.key, {
    body: "Reading it now.",
    in_reply_to: parent.id,
  });

  await page.goto(`/issues/${world.host.key}/conversation`);
  // The quote is the one link to the parent's turn (`ReplyQuote`'s `ReferenceHostIs` control),
  // so the parent's references inside it are names that link nothing.
  const quote = conversationTurn(page, "message", reply.id).locator(
    "a[href] [data-markdown-preview]"
  );

  await expectReadsReferences(quote, world, "inert");
});

// SLOW(>=2s): the reading world's seeding and a handoff, then one cold load of the Conversation
// and the handoff's own read once its card opens; measured at 4.2 s on one run in an issue pod on
// one worker.
test("conversation-handoff-body: a handoff card's field of words draws every form but a question id through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // A handoff's body is the JSON object its phase wrote; a string field of it is the words its
  // session wrote, which the card draws from the reading the server sends for that field.
  await createHandoff(
    world.host.key,
    "implement",
    { phase: "implement", summary: readingText(world) },
    readingAgent.actor
  );

  await page.goto(`/issues/${world.host.key}/conversation`);
  const card = page
    .getByRole("list", { name: "Conversation turns" })
    .locator(":scope > li[data-turn]")
    .filter({ has: page.getByText("implement handoff, version 1", { exact: true }) });
  await card.getByRole("button", { name: "Show fields" }).click();

  // The card's fields are in no link or button, so the summary draws as a block of the renderer
  // with its references linked.
  await expectReadsReferences(
    card.locator('[data-handoff-field="summary"] .dispatch-markdown'),
    world,
    "link"
  );
});

test("issue-decision: a decision's paragraph draws every form of a stored reference through the renderer", async ({
  page,
}) => {
  const seeded = await seedReadingWorld();
  // A decision is a passage of `decisions.md`, so a `#id` in it is read against that document:
  // the document declares the question block itself, ahead of its first decision, and the world
  // is scoped to it.
  const decisions = await createIssueArtifact(
    seeded.host.key,
    {
      content: `${readingQuestionBlock()}\n\n## ${DECISION}\n\n${readingText(seeded)}\n`,
      name: "decisions.md",
    },
    readingAgent
  );
  expect(decisions.artifact.slug).toBe("decisions-md");
  const world = await scopedTo(seeded, decisions.artifact.id);

  // A decision whose text names a question is one a person's answer made (`readDecisions`), so
  // the panel lists it among the reader's own rather than as an agent's waiting for review.
  await page.goto(`/issues/${world.host.key}/decisions`);
  const card = page.getByRole("region", { name: "Your decisions" }).locator("[data-decision]");
  await expect(card.getByRole("heading", { name: DECISION })).toBeVisible();

  // The card draws its heading through the renderer as well, as one line; its paragraphs are
  // its one block body, which is where the stored text is.
  await expectReadsReferences(card.locator(`div${RENDERED_BODY}`), world, "link");
});

// SLOW(>=2s): the reading world's seeding and a comment, then one cold load of the new issue page,
// whose one batched read names what the comment's words cite.
test("issue-page-row-text: a comment's row on the new issue page draws every form but a question id, a bare key, an address and a short id as the row's own chips", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // The reader's own comment, which the outline's Conversation, the view the page opens on, draws
  // as a row of its own.
  const comment = await createComment(world.host.key, { body: readingText(world) });
  await withIssuePage(page.context(), "alice");
  await page.goto(`/issues/${world.host.key}`);
  await expectIssuePageDrawn(page);

  // The renderer draws the row's words from the reading the server sent, and links nothing in
  // them itself: the row's own pass makes a chip of each bare key, address and short id, named
  // from the page's batched read, under the reference as the text writes it, a bare key as its
  // issue's own (`linkRowReferences`). A dashboard URL and a party are links the text writes,
  // which the renderer draws as it draws a link anywhere.
  const words = nodeRow(page, comment.id).first().locator("[data-node-words] .dispatch-markdown");
  for (const { form, opens, reads, reference, unread, written } of formReadings(world)) {
    const chip = form === "bare-key" || form === "address" || form === "short-id";
    const drawn = chip
      ? words.locator(`a[data-row-reference="${form === "bare-key" ? reference : written}"]`)
      : words.locator(`a[data-dispatch-ref="${reference}"]`);
    await expect(drawn, `the ${form} reads as its node's name`).toHaveText(reads);
    // A short id's chip opens the address as the text writes it, whose page resolves the id and
    // puts the whole one in its place (`IssuePage`); every other link opens its node's own path.
    const path =
      form === "short-id" && typeof opens === "string"
        ? opens.replace(/[^/]+$/u, written.slice(written.lastIndexOf("/") + 1))
        : opens;
    await expect(drawn, `the ${form} opens its node`).toHaveAttribute("href", path);
    if (unread !== undefined) {
      await expect(words, `the ${form} is not left as written`).not.toContainText(unread);
    }
  }
  await expect(words).not.toContainText("dispatch://");
});
