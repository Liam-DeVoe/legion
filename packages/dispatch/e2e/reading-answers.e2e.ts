/**
 * The reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`) over what a person wrote on an
 * ask or in a thread and reads back on their own pages: the Answers page's three texts - the
 * question a row answers, the answer written on it, and a reply written in its thread - and the
 * subject line the Inbox's reading column opens a Reply thread under, which is where `/replies`
 * lands. Each is a stored record, and each draws a bare key, an address and a picked party
 * through the one renderer (R6): the key and the address as their issue's key and current title,
 * the party as `@` and the session's name, and no `dispatch://` left as text. None of the four
 * hosts is a link or a button - an Answers row's Open is a link beside its texts, never around
 * them, and the reading column's subject line is a heading - so each of the three is a link
 * there.
 *
 * Each row writes `readingText` into the one record its surface shows and plain words into any
 * other record the surface needs: the question the reading session asks, the answer and the
 * reply alice writes on it, and the root of a comment thread alice starts and the session
 * answers.
 */
import { answerAsk, createAsk, createComment } from "./api";
import { inboxPane, selectRow } from "./inbox-pane";
import { replyRowId } from "./inbox-threads";
import {
  expectReadsReferences,
  READING_SESSION_ID,
  readingText,
  seedReadingWorld,
} from "./reading-rows";
import { answeredQuestionPreview, RENDERED_BODY } from "./reference-surfaces";
import { expect, type Locator, type Page, test } from "./test";

/** The reading session as an author: it asks every question here and answers alice's thread,
 *  as an agent session writes. */
const asReadingSession = {
  actor: { id: READING_SESSION_ID, kind: "session" as const },
  as: "agent" as const,
};

/** The options every ask here offers, so that alice's answer picks one. */
const OPTIONS = [{ label: "Yes" }, { label: "No" }];

/** The question of a row whose surface is its answer or its reply: it names nothing, so the one
 *  text in the row that names anything is the one the row reads. */
const PLAIN_QUESTION = "Which one goes first?";

/** The rows of the Answers page that name one ask. */
function answerRow(page: Page, askId: string): Locator {
  return page.locator(`[data-answer-row="${askId}"]`);
}

// SLOW(>=2s): the world's three issues, an ask asked and answered, and the Answers page drawn.
test("answers-subject: an Answers row's question draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // The session asks and alice answers: that is what puts the ask on her Answers page, under the
  // question the session wrote.
  const ask = await createAsk(
    world.host.key,
    { options: OPTIONS, question: readingText(world) },
    asReadingSession
  );
  await answerAsk(ask.id, { expected_edited_at: null, selected: ["Yes"] });

  await page.goto("/answers");
  // The row's subject line is the question's preview, drawn in the row itself rather than in its
  // Open link, so its references are links.
  await expectReadsReferences(answeredQuestionPreview(page, ask.id), world, "link");
});

// SLOW(>=2s): the world's three issues, an ask asked and answered, and the Answers page drawn.
test("answers-answer: an Answers row's answer draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const ask = await createAsk(
    world.host.key,
    { options: OPTIONS, question: PLAIN_QUESTION },
    asReadingSession
  );
  await answerAsk(ask.id, {
    expected_edited_at: null,
    selected: ["Yes"],
    text: readingText(world),
  });

  await page.goto("/answers");
  // An answer's row draws one rendered body, the text written with the answer, below the
  // question's preview and the picked option's pill.
  await expectReadsReferences(answerRow(page, ask.id).locator(RENDERED_BODY), world, "link");
});

// SLOW(>=2s): the world's three issues, an ask and a reply in its thread, and the Answers page
// drawn.
test("answers-reply: an Answers row's reply draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const ask = await createAsk(
    world.host.key,
    { options: OPTIONS, question: PLAIN_QUESTION },
    asReadingSession
  );
  // A comment alice writes in the ask's thread is a reply of hers on it, which the Answers page
  // lists as a row of its own although she gave no answer.
  await createComment(world.host.key, { ask_id: ask.id, body: readingText(world) });

  await page.goto("/answers");
  const row = answerRow(page, ask.id);
  await expect(row).toContainText("Replied:");
  // A reply's row draws one rendered body, the reply, after its "Replied:" label.
  await expectReadsReferences(row.locator(RENDERED_BODY), world, "link");
});

// SLOW(>=2s): the world's three issues, a comment thread and its reply, and the Inbox drawn with
// the thread open in its reading column.
test("my-reply-subject: a Reply thread's subject line draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // Alice's root and the session's reply under it make the thread a Reply row of hers, and its
  // subject line is the root's first line, which is the whole text.
  const root = await createComment(world.host.key, { body: readingText(world) });
  await createComment(world.host.key, { body: "On it.", reply_to: root.id }, asReadingSession);

  // `/replies` lands on the Inbox's Reply rows. The address is read parameter by parameter, since
  // the reading column writes the row it selects into it the moment it opens (`?item=`). At this
  // project's width the Inbox draws that column, which opens the selected thread under its
  // subject line: a heading, not a control, so its references are links.
  await page.goto("/replies");
  await expect(page).toHaveURL(
    (url) => url.pathname === "/" && url.searchParams.get("type") === "reply"
  );
  await selectRow(page, replyRowId(root.id));
  const subject = inboxPane(page).locator("[data-inbox-thread-subject]");
  await expectReadsReferences(subject, world, "link");
});
