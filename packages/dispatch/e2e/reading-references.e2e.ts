/**
 * The reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`) over the surfaces that show a
 * record through another record's reference to it, and over the search palette: the cards under
 * a body that is nothing but references, the two lines a Dispatch card draws of its target, the
 * hover card over a reference to a comment and to an ask, and the palette's hits. Each is a
 * stored record drawn by the one renderer (R6), so a bare key, an address and a picked party in
 * it draw as their issue's key and current title or as `@` and the session's name, and none is
 * left as a raw `dispatch://`.
 *
 * Where each of the three is a link is the host's to say. A body in the Conversation is a record
 * a reader reads, so its references and the cards after it are links. A card, the hover card and
 * a palette row are each one link or one button already, so what they draw of a record links
 * nothing of its own: the references there are inert names.
 *
 * Each row writes `readingText` through the API into the record its surface shows - a message,
 * a comment or an ask's question as an agent, the ask's answer as alice - and reaches the surface
 * the way its row of `DISPATCH_READING_SURFACES` says. The cards row writes a body of references
 * alone, since that is the only body that has cards, and says so where it does.
 */
import { answerAsk, createAsk, createComment, createMessage } from "./api";
import {
  expectReadsReferences,
  READING_SESSION_ID,
  READING_SESSION_REFERENCE,
  READING_SESSION_TITLE,
  type ReadingWorld,
  readingText,
  seedReadingWorld,
} from "./reading-rows";
import { conversationTurn, RENDERED_BODY, referenceAgent } from "./reference-surfaces";
import { expect, type Locator, type Page, test } from "./test";

/** The word the palette rows search for: a word of `readingText` that no title of the world
 *  holds, so the hits are the records holding the text and no issue besides. The text is eight
 *  words, and an ask is searched by its question and then its answer, sixteen: either is inside
 *  the 24 words of the headline's window, so a hit's snippet is its whole text
 *  (`pmdoc.SearchSnippet`) whichever of its words matched. */
const SEARCH_WORD = "ask";

/** Opens the search palette with Ctrl+K, as a reader does on any route, searches for
 *  `SEARCH_WORD`, and answers the palette. */
async function searchPalette(page: Page): Promise<Locator> {
  await page.goto("/");
  await expect(page.getByRole("button", { name: /^search/i })).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("Control+k");
  const dialog = page.getByRole("dialog", { name: "Search" });
  await expect(dialog).toBeVisible();
  await page.getByRole("combobox", { name: "Search" }).fill(SEARCH_WORD);
  return dialog;
}

/** Writes a message on the host that names `reference` in prose, hovers the link it draws for
 *  it, and answers the hover card that opens. The prose beside the reference keeps the message
 *  from being a body of references alone, which would draw a card under it as well. */
async function hoverCard(page: Page, world: ReadingWorld, reference: string): Promise<Locator> {
  const referring = await createMessage(
    world.host.key,
    { body: `Before the rollout, see ${reference} first.` },
    referenceAgent
  );
  await page.goto(`/issues/${world.host.key}/conversation`);
  const link = conversationTurn(page, "message", referring.id).locator(
    `a[data-dispatch-ref="${reference}"]`
  );
  await expect(link).toBeVisible();
  await link.hover();
  const card = page.locator("#ref-preview");
  await expect(card).toBeVisible();
  return card;
}

// SLOW(>=2s): the world's three issues and the host's Conversation drawn with a message's body
// and the three cards after it.
test("body-reference-cards: a body of a stored key, an address and a party draws each as its name, and a card for each after it", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // A body has cards only when it is its references and nothing else (`Unfurl.tsx`'s
  // `referencesOnly`), so this one is not `readingText`: the picker's party is a link whose words
  // stand beside its address, and the prose around the other two is words of its own. It holds
  // the same three references, the party written as its bare address, which reads as the
  // session's current name alone: the `@` of `readingText` is the words the picker wrote.
  const body = `${world.keyed.key} dispatch://${world.addressed.key} ${READING_SESSION_REFERENCE}`;
  const message = await createMessage(world.host.key, { body }, referenceAgent);

  await page.goto(`/issues/${world.host.key}/conversation`);
  const turn = conversationTurn(page, "message", message.id);
  // The body is a record a reader reads, so each reference is a link reading as its target's
  // current name, and no address is left as text.
  const rendered = turn.locator(RENDERED_BODY);
  const drawn = (reference: string) => rendered.locator(`[data-dispatch-ref="${reference}"]`);
  const keyed = drawn(`dispatch://${world.keyed.key}`);
  const addressed = drawn(`dispatch://${world.addressed.key}`);
  const party = drawn(READING_SESSION_REFERENCE);
  await expect(keyed).toHaveText(`${world.keyed.key} ${world.keyed.title}`);
  await expect(addressed).toHaveText(`${world.addressed.key} ${world.addressed.title}`);
  await expect(party).toHaveText(READING_SESSION_TITLE);
  for (const reference of [keyed, addressed, party]) {
    await expect(reference).toHaveAttribute("href", /.+/);
  }
  await expect(rendered).not.toContainText("dispatch://");

  // The cards after it: one for each reference, in the order the body writes them, each a link
  // to its target reading as the target's own name, which for an issue is its title alone.
  const region = turn.getByRole("region", { name: "References" });
  const cards = region.getByRole("link");
  const expected = [
    { href: `/issues/${world.keyed.key}`, title: world.keyed.title },
    { href: `/issues/${world.addressed.key}`, title: world.addressed.title },
    { href: `/agents/${READING_SESSION_ID}/live`, title: READING_SESSION_TITLE },
  ];
  await expect(cards).toHaveCount(expected.length);
  for (const [index, { href, title }] of expected.entries()) {
    await expect(cards.nth(index)).toHaveAttribute("href", href);
    await expect(cards.nth(index).locator("span").first()).toHaveText(title);
  }
  await expect(region).not.toContainText("dispatch://");
});

// SLOW(>=2s): the world's three issues, two messages, and the host's Conversation drawn with the
// card under the second.
test("dispatch-reference-card: a card's two lines draw its target's stored key, address and party as names that link nothing", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const target = await createMessage(world.host.key, { body: readingText(world) }, referenceAgent);
  // A message's card draws the message's whole text under its title (`useReferenceTarget`), and
  // a body that is the reference alone is what draws the card.
  const referring = await createMessage(
    world.host.key,
    { body: `dispatch://${world.host.key}/message/${target.id}` },
    referenceAgent
  );

  await page.goto(`/issues/${world.host.key}/conversation`);
  const card = conversationTurn(page, "message", referring.id)
    .getByRole("region", { name: "References" })
    .getByRole("link");
  await expect(card).toHaveCount(1);
  // The card is one link to the message, so what it draws of the message links nothing.
  await expectReadsReferences(card.locator("[data-markdown-preview]"), world, "inert");
});

// SLOW(>=2s): the world's three issues, a comment and a message, and the host's Conversation
// drawn before the hover card opens over it.
test("reference-hover-card: the hover card over a reference to a comment draws its stored key, address and party as names that link nothing", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const comment = await createComment(world.host.key, { body: readingText(world) }, referenceAgent);
  const card = await hoverCard(page, world, `dispatch://${world.host.key}/comment/${comment.id}`);
  // The card is one link to the comment, so what it draws of the comment links nothing.
  await expectReadsReferences(card, world, "inert");
});

// SLOW(>=2s): the world's three issues, an ask and a message, and the host's Conversation drawn
// before the hover card opens over it.
test("reference-hover-card-ask: the hover card over a reference to an ask draws its stored key, address and party as names that link nothing", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const ask = await createAsk(world.host.key, { question: readingText(world) }, referenceAgent);
  const card = await hoverCard(page, world, `dispatch://${world.host.key}/ask/${ask.id}`);
  // The card heads with the ask's question, inside the one link to the ask.
  await expectReadsReferences(card, world, "inert");
});

// SLOW(>=2s): the world's three issues, an ask answered by alice, and the palette's search
// answered by Postgres's headline.
test("search-ask-status: an ask hit draws its stored question's and answer's key, address and party as names that link nothing", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const ask = await createAsk(world.host.key, { question: readingText(world) }, referenceAgent);
  const answered = await answerAsk(ask.id, {
    expected_edited_at: null,
    selected: [],
    text: readingText(world),
  });
  expect(answered.answer).not.toBeNull();

  const dialog = await searchPalette(page);
  const hit = dialog.locator(`#search-option-ask-${ask.id}`);
  await expect(hit).toContainText("Answered");
  // The hit's two rendered texts: its snippet, which is the question and then the answer the ask
  // is searched by, and the start of the latest answer under its status line (`AskHitStatus`).
  // The row is one button, a `control` host, so neither links anything.
  const previews = hit.locator("[data-markdown-preview]");
  await expect(previews).toHaveCount(2);
  await expectReadsReferences(previews.nth(0), world, "inert");
  await expectReadsReferences(previews.nth(1), world, "inert");
});

// SLOW(>=2s): the world's three issues, a comment, and the palette's search answered by
// Postgres's headline.
test("search-result: a result row's snippet draws a stored key, an address and a party as names that link nothing", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const comment = await createComment(world.host.key, { body: readingText(world) }, referenceAgent);

  const dialog = await searchPalette(page);
  const hit = dialog.locator(`#search-option-comment-${comment.id}`);
  await expect(hit).toBeVisible();
  // The row is one button, a `control` host, so the snippet links nothing.
  await expectReadsReferences(hit.locator("[data-markdown-preview]"), world, "inert");
});
