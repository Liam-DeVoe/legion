import { createComment, createIssueArtifact, createMessage } from "./api";
import {
  expectReadsReferences,
  READING_SESSION_ID,
  READING_SESSION_TITLE,
  readingText,
  seedReadingWorld,
} from "./reading-rows";
import { conversationTurn, RENDERED_BODY } from "./reference-surfaces";
import { expect, test } from "./test";

/**
 * The reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`) across an issue's own pages:
 * the Activity tab's event body, the three turns of the Conversation tab and the quote above a
 * reply there, and a decision on the Decisions tab. Each row writes the one reading text into
 * the record its surface shows, through the API as an agent or a person writes it, reaches the
 * surface the way its row of `DISPATCH_READING_SURFACES` says, and reads there a bare key, a
 * `dispatch://` address and a picked party drawn through the one renderer (R6).
 *
 * Every surface here is a record a reader reads, so each reference is a link, but for the quote
 * above a reply: the quote is the link to its parent's turn, so the references inside it draw as
 * their names and link nothing.
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
test("activity-event-body: a pinned Activity event draws a stored key, an address and a party through the renderer", async ({
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

// SLOW(>=2s): seeds the reading world - a project, the fake registry's session and three issues -
// and a two-message thread before one cold load of the Conversation.
test("conversation-reply: a reply turn draws a stored key, an address and a party through the renderer", async ({
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

test("conversation-message: a message turn draws a stored key, an address and a party through the renderer", async ({
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

test("conversation-targeted-message: a message sent to a session draws a stored key, an address and a party through the renderer", async ({
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

test("reply-quote: the quote above a reply draws its parent's key, address and party as names inside its link", async ({
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

test("issue-decision: a decision's paragraph draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const decisions = await createIssueArtifact(
    world.host.key,
    { content: `## ${DECISION}\n\n${readingText(world)}\n`, name: "decisions.md" },
    readingAgent
  );
  expect(decisions.artifact.slug).toBe("decisions-md");

  // An agent's decision that nobody has kept waits for the reader's review.
  await page.goto(`/issues/${world.host.key}/decisions`);
  const card = page
    .getByRole("region", { name: "Waiting for your review" })
    .locator("[data-decision]");
  await expect(card.getByRole("heading", { name: DECISION })).toBeVisible();

  await expectReadsReferences(card.locator(RENDERED_BODY), world, "link");
});
