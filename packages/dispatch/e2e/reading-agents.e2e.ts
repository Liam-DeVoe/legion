/**
 * The reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`) over the agents' world: the
 * two surfaces of the Agents page that show a stored message, and the two parts of a session's
 * live view that show its own turn. Each row stores `readingText` where its surface shows it - a
 * message a person sends the session, the session's reply to one, or a turn of the replay the
 * session answers a viewer with - reaches the surface as its row's `reach` says, and holds it to
 * R6: the key and the address read as their issue's key and current title, the party as `@` and
 * its current name, and no `dispatch://` is left as text.
 *
 * All four hosts are a record a reader reads (`ReferenceHost` "record"): neither a turn of the
 * Agents page nor a part of the live view sits inside a link or a button, so each reference is a
 * link. A live turn is no record of the reference graph (the reading list's reason for both of
 * its rows), which costs it its `#id` links and nothing else: a bare key, an address and a party
 * draw there through the same renderer as they do in a stored body.
 *
 * The text names the session whose row and live view show it as its party, which is the one
 * session the reading world lists.
 */
import type { Message } from "../web/src/api/types";
import { agentRow } from "./agents";
import { createAgentMessage, publishAgentStreamFrame, replyToMessageDelivery } from "./api";
import { frontLiveView } from "./live-views";
import {
  expectReadsReferences,
  READING_SESSION_ID,
  readingText,
  seedReadingWorld,
} from "./reading-rows";
import { expect, type Locator, type Page, test } from "./test";

/** Opens the Agents page and the reading session's row. A row opens collapsed, and its
 *  conversation is on screen only once it is open. */
async function openReadingRow(page: Page): Promise<Locator> {
  await page.goto("/agents");
  const row = agentRow(page, READING_SESSION_ID);
  const toggle = row.locator("[data-agent-toggle]");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  return row;
}

/** Sets what the reading session answers a viewer's replay request with: one finished turn of
 *  its own, made of `parts`. The harness runs Dispatch with NATS disabled, so the test hook
 *  stands in for the session; answering a replay makes it a responder, as a running session
 *  with a streaming plugin is. */
async function replayTurn(parts: readonly { text: string; type: "reasoning" | "text" }[]) {
  await publishAgentStreamFrame(
    READING_SESSION_ID,
    {
      frames: [
        {
          kind: "message",
          message: { at: 1_000, id: "a1000", parts, role: "assistant", streaming: false },
          seq: 1,
          v: 1,
        },
      ],
      session_id: READING_SESSION_ID,
      v: 1,
    },
    "replay"
  );
}

// SLOW(>=2s): the reading world's five listener and API writes, a message and the session's
// reply to it, then the Agents page's first draw and the row's conversation read once it opens;
// measured at 3.4 s and 5.6 s on two runs in an issue pod on one worker.
test("agents-row-reply: a row's reply turn draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // The question is plain words, so the quote of it the reply turn draws above its own body
  // holds no reference to be read in place of the reply's.
  const asked = await createAgentMessage(READING_SESSION_ID, {
    body: "What should I read next?",
    delivery: "aside",
  });
  const reply = (await replyToMessageDelivery(
    asked.id,
    { attempt: 1, body: readingText(world) },
    { id: READING_SESSION_ID, kind: "session" }
  )) as Message;
  const row = await openReadingRow(page);
  // The reply's body alone: the turn also names its author through `AgentName` and quotes the
  // question in a preview, and neither is the stored reply.
  const body = row.locator(`li[data-reply][data-turn="message:${reply.id}"] div.dispatch-markdown`);
  await expectReadsReferences(body, world, "link");
});

// SLOW(>=2s): the reading world's five listener and API writes and a message, then the Agents
// page's first draw and the row's conversation read once it opens; measured at 2.0 s and 4.6 s on
// two runs in an issue pod on one worker.
test("agents-row-message: a row's targeted message card draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const sent = await createAgentMessage(READING_SESSION_ID, {
    body: readingText(world),
    delivery: "aside",
  });
  const row = await openReadingRow(page);
  // The message's body alone: the card also names its author and the session it went to, each
  // through `AgentName`, and neither is the stored message. No reply is seeded, so the card
  // holds no reply's body either.
  const body = row.locator(`li[data-turn="message:${sent.id}"] div.dispatch-markdown`);
  await expectReadsReferences(body, world, "link");
});

// SLOW(>=2s): the reading world's five listener and API writes and the session's replay, then the
// live view's first draw, which waits on the live socket and the replay it asks for; measured at
// 2.0 s and 2.3 s on two runs in an issue pod on one worker.
test("agent-turn-text: a live turn's text part draws a key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  await replayTurn([{ text: readingText(world), type: "text" }]);
  await page.goto(`/agents/${READING_SESSION_ID}/live`);
  // The turn holds its one text part and nothing else: no model is reported and it is not
  // streaming, so neither line above the part is drawn.
  const turn = frontLiveView(page).getByTestId("agent-message-assistant");
  await expectReadsReferences(turn.locator("div.dispatch-markdown"), world, "link");
});

// SLOW(>=2s): the reading world's five listener and API writes and the session's replay, then the
// live view's first draw, which waits on the live socket and the replay it asks for; measured at
// 1.6 s and 2.6 s on two runs in an issue pod on one worker.
test("agent-turn-reasoning: a live turn's reasoning draws a key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // The reasoning sits above the turn's answer, as a session that thought publishes it.
  await replayTurn([
    { text: readingText(world), type: "reasoning" },
    { text: "Reading both now.", type: "text" },
  ]);
  await page.goto(`/agents/${READING_SESSION_ID}/live`);
  const reasoning = frontLiveView(page).getByTestId("agent-reasoning");
  await expectReadsReferences(reasoning, world, "link");
});
