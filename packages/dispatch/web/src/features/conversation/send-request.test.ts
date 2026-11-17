import * as bun from "bun:test";
import { expect, test } from "bun:test";

import * as client from "../../api/client";
import { ApiError } from "../../api/client";
import { SendInterruptedError, SendStoppedError } from "../writes/keyed-write";
import * as sending from "./send-request";
import { requestFor, type SentRequest } from "./send-request";

/** A message to the Planner's session, as Send froze it, under key `send-1`. */
const refused: SentRequest = {
  anchor: undefined,
  ask: { multiple: false, options: [], urgency: "med" },
  draft: { body: "Status please", replacement: "" },
  edit: undefined,
  kind: "comment",
  owner: { kind: "session", sessionId: "planner-session" },
  pickedDelivery: "aside",
  plainDelivery: "steer",
  replyTo: null,
  sendKey: "send-1",
};

/** The same send as the composer shows it now, with `draft` over its draft. */
function shown(draft: Partial<SentRequest["draft"]> = {}): Omit<SentRequest, "sendKey"> {
  const { sendKey: _key, pickedDelivery: _picked, ...rest } = refused;
  return { ...rest, draft: { ...refused.draft, ...draft } };
}

const verdict = new ApiError(400, { code: "MESSAGE_INPUT", error: "Dispatch refused it" });

// A send that ended without Dispatch's verdict - the reader stopped it, or a reload interrupted it
// too long ago - may have landed, so its unedited Retry is that request itself, key included, and
// Dispatch answers it with what the first did.
test("an unedited Retry after Stop trying or an interrupted reload is the refused request, key and all", () => {
  for (const refusal of [new SendStoppedError(), new SendInterruptedError()]) {
    expect(requestFor(refused, refusal, shown())).toBe(refused);
  }
});

// After Stop trying, an edit that leaves the message on the wire what it was - here a
// suggestion's replacement, which a session message never carries - is still that one write,
// under its key; an edit of the words is another message, under a key of its own.
test("an edited Retry after Stop trying keeps the key only while the wire message is the same", () => {
  const stopped = new SendStoppedError();
  const sameWire = requestFor(refused, stopped, {
    ...shown({ replacement: "The replacement it would suggest" }),
    pickedDelivery: "aside",
  });
  expect(sameWire.sendKey).toBe("send-1");

  const otherWords = requestFor(refused, stopped, shown({ body: "Status please, now" }));
  expect(otherWords.sendKey).not.toBe("send-1");
  expect(otherWords.draft.body).toBe("Status please, now");
});

// Dispatch's refusal is its verdict on the key, which it would answer the same way for a day: a
// Retry after it is a new action under a new key. An unedited message to one session still goes
// as it was sent - its picked mode included - and an edited one as the composer shows it now.
test("a Retry after Dispatch's refusal goes under a new key, unedited or not", () => {
  const unedited = requestFor(refused, verdict, shown());
  expect(unedited.sendKey).not.toBe("send-1");
  expect({ ...unedited, sendKey: "send-1" }).toEqual(refused);

  const edited = requestFor(refused, verdict, shown({ body: "Status please, now" }));
  expect(edited.sendKey).not.toBe("send-1");
  expect(edited.pickedDelivery).toBeUndefined();
});

// A comment refused by Dispatch is sent again as the composer shows it now, in the modes its
// mentioned sessions advertise now, under a key of its own.
test("a comment's Retry after Dispatch's refusal is the request the composer shows now", () => {
  const comment: SentRequest = {
    ...refused,
    owner: { issueKey: "CORE-1", kind: "issue" },
    pickedDelivery: undefined,
  };
  const next = { ...shown(), owner: comment.owner, plainDelivery: "aside" as const };
  const retried = requestFor(comment, verdict, next);
  expect(retried.plainDelivery).toBe("aside");
  expect(retried.sendKey).not.toBe("send-1");
  expect(requestFor(undefined, undefined, next).sendKey).not.toBe("send-1");
});

/** A comment on issue CORE-1 as Send froze it, under key `send-1`: the session message above,
 *  addressed to the issue, with `request` over its fields. */
function issueComment(request: Partial<SentRequest>): SentRequest {
  return {
    ...refused,
    owner: { issueKey: "CORE-1", kind: "issue" },
    pickedDelivery: undefined,
    ...request,
  };
}

// A reply to an ask names the ask by `ask_id`, which the server files under the ask in any state -
// an open ask's question back, and a word under an answered or resolved one - where `reply_to`
// would name a comment of that id.
test("a reply to an ask is sent with ask_id", async () => {
  const createComment = bun
    .spyOn(client.api, "createComment")
    .mockResolvedValue(undefined as never);
  try {
    const sent = issueComment({
      draft: { body: "The second layout, please.", replacement: "" },
      replyTo: {
        actor: { id: "planner", kind: "user" },
        excerpt: "Which layout should the phone take?",
        id: "ask-7",
        parentKind: "ask",
      },
    });
    const bounds = { key: sent.sendKey };
    await sending.sendAttempt(sent)(bounds);

    expect(createComment.mock.calls).toStrictEqual([
      ["CORE-1", { ask_id: "ask-7", body: "The second layout, please." }, bounds],
    ]);
  } finally {
    createComment.mockRestore();
  }
});

// The issue page's box anchors a comment on a statement by the document and the statement's whole
// words alone: the server finds the words and anchors the comment to the block that holds them,
// so the request names no occurrence and no mark.
test("a comment on a statement is sent with its quote and no occurrence", async () => {
  const createComment = bun
    .spyOn(client.api, "createComment")
    .mockResolvedValue(undefined as never);
  try {
    const quote = "The box stays at the foot of the screen while the outline scrolls.";
    const sent = issueComment({
      draft: { body: "Even on a phone?", replacement: "" },
      statement: { artifact: "core-1-spec", quote },
    });
    const bounds = { key: sent.sendKey };
    await sending.sendAttempt(sent)(bounds);

    expect(createComment.mock.calls).toStrictEqual([
      ["CORE-1", { anchor: { artifact: "core-1-spec", quote }, body: "Even on a phone?" }, bounds],
    ]);
  } finally {
    createComment.mockRestore();
  }
});
