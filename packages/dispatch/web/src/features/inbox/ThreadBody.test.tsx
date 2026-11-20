import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { messageCorrectionFields } from "../../__tests__/message-fixture";
import { ApiError, api } from "../../api/client";
import type {
  Agent,
  Comment,
  IssueDetails,
  Message,
  MessageRead,
  MyReplyRow,
  MyReplyThreadRow,
} from "../../api/types";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import { agentRow, plannerAgentRow } from "../threads/replies-fixture";
import { ThreadRowCard } from "./ThreadRowCard";

/** Every API mock a test installed, restored after it. */
const restores: { mockRestore: () => void }[] = [];

afterEach(() => {
  cleanup();
  for (const mock of restores.splice(0)) {
    mock.mockRestore();
  }
  window.localStorage.clear();
});

function spy<K extends keyof typeof api>(name: K, value: unknown): void {
  restores.push(spyOn(api, name).mockResolvedValue(value as never));
}

const session: Agent = {
  capabilities: ["steer", "btw"],
  dir: "/work",
  host: "box",
  last_activity: "2026-10-12T00:00:00Z",
  last_seen: Date.now(),
  machine_id: "box",
  open_asks: 0,
  roles: [],
  session_id: "planner",
  state: "live",
  title: "Planner",
};

function message(id: string, body: string, overrides: Partial<Message> = {}): Message {
  return {
    author: { id: "alice", kind: "user" },
    body,
    broadcast_id: null,
    created_at: "2026-10-12T00:00:00Z",
    deliveries: [],
    ...messageCorrectionFields(),
    id,
    in_reply_to: null,
    issue_key: null,
    reply_block: null,
    target: "session:planner",
    ...overrides,
  };
}

function commentRow(overrides: Partial<MyReplyThreadRow> = {}): MyReplyThreadRow {
  return {
    issue: { key: "CORE-1", title: "Fix the thing" },
    kind: "comment",
    read_through: "2026-10-12T00:30:00Z",
    ref: "/issues/CORE-1?comment=comment-1",
    reply: {
      author: { id: "bob", kind: "user" },
      created_at: "2026-10-12T01:00:00Z",
      first_line: "Agreed.",
      id: "comment-2",
      kind: "reply",
    },
    subject: "The retry budget",
    thread_id: "comment-1",
    unread: true,
    unread_count: 1,
    version: 1,
    ...overrides,
  };
}

function comment(id: string, body: string, overrides: Partial<Comment> = {}): Comment {
  return {
    anchor: null,
    answers: null,
    artifact_id: null,
    ask_id: null,
    author: { id: "alice", kind: "user" },
    body,
    created_at: "2026-10-12T00:00:00Z",
    deleted_at: null,
    deleted_by: null,
    deliveries: [],
    edited_at: null,
    id,
    issue_key: "CORE-1",
    mentions: [],
    reply_block: null,
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
    ...overrides,
  };
}

function renderCard(thread: MyReplyRow): QueryClient {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const tree: ReactNode = (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <ThreadRowCard thread={thread} />
      </QueryClientProvider>
    </MemoryRouter>
  );
  render(tree);
  return client;
}

test("an Agent row's card draws the message, the session's reply and the composer", async () => {
  spy("listAgents", [session]);
  spy("listAgentMessages", [
    {
      message: message("message-1", "Have a look at the build"),
      replies: [
        message("reply-1", "Looked at it.", {
          author: { id: "planner", kind: "session" },
          created_at: "2026-10-12T01:00:00Z",
          in_reply_to: "message-1",
        }),
      ],
    } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow());

  const body = await screen.findByTestId("inbox-thread-agent");
  // Every message is drawn whole, by the one markdown renderer, which warms asynchronously.
  await waitFor(() => expect(body.textContent).toContain("Have a look at the build"));
  expect(body.textContent).toContain("Looked at it.");
  // The row names the unread reply, so only that message is marked New.
  const turns = [...body.querySelectorAll("[data-inbox-thread-turn]")];
  expect(turns.map((turn) => turn.getAttribute("data-inbox-thread-turn"))).toEqual([
    "message-1",
    "reply-1",
  ]);
  expect(turns[0]?.hasAttribute("data-inbox-thread-unread")).toBe(false);
  expect(turns[1]?.hasAttribute("data-inbox-thread-unread")).toBe(true);
  expect(within(turns[1] as HTMLElement).getByText("New")).toBeTruthy();
  await waitFor(() => expect(body.querySelector("[data-inbox-agent-composer]")).not.toBeNull());
});

test("a reply the session took as a user turn says so and links the live view", async () => {
  spy("listAgents", [session]);
  spy("listAgentMessages", [
    {
      message: message("message-1", "Have a look at the build"),
      replies: [
        message("reply-1", "Running it now.", {
          created_at: "2026-10-12T01:00:00Z",
          deliveries: [
            {
              accepted_as: "user_turn",
              accepted_at: "2026-10-12T01:00:05Z",
              attempt: 1,
              created_at: "2026-10-12T01:00:01Z",
              delivery: "steer",
              duplicate: false,
              envelope_id: "envelope-1",
              error: null,
              message_id: "reply-1",
              reply_id: null,
              requested_by: null,
              session_id: "planner",
              state: "sent",
            },
          ],
          in_reply_to: "message-1",
        }),
      ],
    } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow({ unread_reply_ids: [] }));

  const note = await screen.findByTestId("agent-reply-accepted");
  expect(note.textContent).toBe("Taken as a turn of the live conversation.");
  const live = screen.getByRole("link", { name: "Live view" });
  expect(live.getAttribute("href")).toBe("/agents/planner/live");
});

test("a session the live list no longer holds says so and offers no composer", async () => {
  // The reply is read as soon as the ended note shows, and only a loaded engine has drawn it by
  // then (`MarkdownBody`): the first body of a file takes the asynchronous path.
  await warmMarkdownRenderer();
  spy("listAgents", []);
  spy("listAgentMessages", [
    {
      message: message("message-1", "Have a look at the build"),
      replies: [
        message("reply-1", "Looked at it.", {
          author: { id: "planner", kind: "session" },
          in_reply_to: "message-1",
        }),
      ],
    } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow());

  const ended = await screen.findByTestId("agent-session-ended");
  expect(ended.textContent).toBe("Not in the live session list — it may have ended.");
  expect(document.querySelector("[data-inbox-agent-composer]")).toBeNull();
  // The thread is still there to read, whatever became of the session.
  expect(screen.getByTestId("inbox-thread-agent").textContent).toContain("Looked at it.");
});

test("a listed session that advertises no delivery mode warns above its composer", async () => {
  spy("listAgents", [{ ...session, capabilities: [] }]);
  spy("listAgentMessages", [
    { message: message("message-1", "Have a look"), replies: [] } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow({ unread_reply_ids: [] }));

  await screen.findByTestId("inbox-thread-agent");
  await waitFor(() => expect(screen.getByText("Takes no messages")).toBeTruthy());
  expect(screen.getByTestId("agent-takes-no-messages").textContent).toContain(
    "records a failed attempt"
  );
});

// A list that holds only capabilities that are no delivery mode takes no message either, and the
// warning says that is why, not that the plugin cannot reach the conversation.
test("a listed session whose capabilities hold no delivery mode warns the same way", async () => {
  spy("listAgents", [{ ...session, capabilities: ["future-capability"] }]);
  spy("listAgentMessages", [
    { message: message("message-1", "Have a look"), replies: [] } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow({ unread_reply_ids: [] }));

  await screen.findByTestId("inbox-thread-agent");
  await waitFor(() => expect(screen.getByText("Takes no messages")).toBeTruthy());
  expect(screen.getByTestId("agent-takes-no-messages").textContent).toBe(
    "This session advertises no delivery mode, so a message sent here records a failed attempt. None of the capabilities it lists is a delivery mode this Dispatch knows."
  );
});

test("a comment thread's card draws the quote it is anchored to and the thread whole", async () => {
  // The thread is read in the commit that mounts its card, which only a loaded engine draws
  // (`MarkdownBody`): the first body of a file takes the asynchronous path.
  await warmMarkdownRenderer();
  spy("getIssue", { closed_at: null } as unknown as IssueDetails);
  spy("listComments", [
    comment("comment-1", "The retry budget is too small."),
    comment("comment-2", "Agreed.", {
      author: { id: "bob", kind: "user" },
      created_at: "2026-10-12T01:00:00Z",
      reply_to: "comment-1",
    }),
  ]);
  renderCard(
    commentRow({
      anchor: {
        artifact_id: "artifact-1",
        block_id: "b-1",
        mark_id: "mark-1",
        orphaned: false,
        quote: "three attempts",
        version: 2,
      },
    })
  );

  const body = await screen.findByTestId("inbox-thread-comment");
  expect(body.querySelector("[data-inbox-thread-quote]")?.textContent).toBe("three attempts");
  expect(body.textContent).toContain("The retry budget is too small.");
  expect(body.textContent).toContain("Agreed.");
  // Written after the row's read mark, so only the reply is new.
  const turns = [...body.querySelectorAll("[data-inbox-thread-turn]")];
  expect(turns[0]?.hasAttribute("data-inbox-thread-unread")).toBe(false);
  expect(turns[1]?.hasAttribute("data-inbox-thread-unread")).toBe(true);
});

test("an inbox thread marks an edited message and reads a deleted one as deleted", async () => {
  spy("listAgents", [session]);
  spy("listAgentMessages", [
    {
      message: message("message-1", "Have a look at the build", {
        edited_at: "2026-10-12T00:30:00Z",
      }),
      replies: [
        message("reply-1", "", {
          author: { id: "planner", kind: "session" },
          created_at: "2026-10-12T01:00:00Z",
          deleted_at: "2026-10-12T01:05:00Z",
          deleted_by: { id: "planner", kind: "session" },
          in_reply_to: "message-1",
        }),
      ],
    } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow());

  const body = await screen.findByTestId("inbox-thread-agent");
  await waitFor(() => expect(body.textContent).toContain("Have a look at the build"));
  const turns = [...body.querySelectorAll("[data-inbox-thread-turn]")];
  expect(turns[0]?.textContent).toContain("edited");
  await waitFor(() => expect(turns[1]?.textContent).toContain("Deleted by"));
});

// A thread box in the Inbox stands for its thread: its reply is the box's own address, which
// nothing ends, so it draws no Cancel reply. Pressed, that control would do nothing and leave the
// draft in the field (MentionComposer's `standing`).
test("an Inbox thread box, comment or Agent, draws no Cancel reply", async () => {
  spy("getIssue", { closed_at: null } as unknown as IssueDetails);
  spy("listComments", [comment("comment-1", "The retry budget is too small.")]);
  renderCard(commentRow());
  const thread = await screen.findByTestId("inbox-thread-comment");
  await waitFor(() =>
    expect(within(thread).queryAllByRole("form", { name: "Comment composer" }).length).toBe(1)
  );
  expect(within(thread).queryAllByRole("button", { name: "Cancel reply" }).length).toBe(0);
  cleanup();

  spy("listAgents", [session]);
  spy("listAgentMessages", [
    {
      message: message("message-1", "Have a look at the build"),
      replies: [],
    } satisfies MessageRead,
  ]);
  renderCard(plannerAgentRow());
  const agent = await screen.findByTestId("inbox-thread-agent");
  await waitFor(() => expect(agent.querySelector("[data-inbox-agent-composer]")).not.toBeNull());
  expect(within(agent).queryAllByRole("button", { name: "Cancel reply" }).length).toBe(0);
});

// A root its author deleted takes no reply (409 POST_DELETED), so the Inbox thread draws no box
// under it, as the margin's card draws no reply composer there, and offers no Send. A reply the
// reader typed in the box before the delete is not lost: it is drawn as the reader's own words,
// saying the comment was deleted and giving no word of it, with Discard my draft, which drops it.
test("a comment thread whose root is deleted draws no box, and keeps the reply typed there", async () => {
  const user = userEvent.setup();
  spy("getIssue", { closed_at: null } as unknown as IssueDetails);
  const listComments = spyOn(api, "listComments").mockResolvedValue([
    comment("comment-1", "The retry budget is too small."),
  ]);
  restores.push(listComments);
  const client = renderCard(commentRow());
  const thread = await screen.findByTestId("inbox-thread-comment");
  const typed = "Raise it to five.";
  await user.click(await within(thread).findByRole("textbox", { name: "Reply" }));
  await user.paste(typed);

  listComments.mockResolvedValue([
    comment("comment-1", "", {
      deleted_at: "2026-10-12T02:00:00Z",
      deleted_by: { id: "alice", kind: "user" },
    }),
  ]);
  await act(() => client.invalidateQueries({ queryKey: ["comments", "CORE-1"] }));
  await waitFor(() => expect(thread.textContent).toContain("Deleted by alice"));
  const deleted = {
    box: within(thread).queryByRole("form", { name: "Comment composer" }) !== null,
    lines: within(thread)
      .queryAllByRole("status")
      .map((status) => status.textContent ?? "")
      .filter((text) => text.includes(typed)),
    send: within(thread).queryByRole("button", { name: "Send" }) !== null,
  };
  const discard = within(thread).queryByRole("button", { name: "Discard my draft" });
  if (discard !== null) await user.click(discard);
  expect({ deleted, discarded: thread.textContent?.includes(typed) }).toEqual({
    deleted: {
      box: false,
      lines: [
        `The comment by alice that your reply answered was deleted, so it takes no reply. Your reply is kept here:${typed}`,
      ],
      send: false,
    },
    discarded: false,
  });
});

// A message its author deleted takes no reply (409 POST_DELETED), so an Agent card's box answers
// the newest message of its thread that still stands, and its Send goes there. A thread none of
// whose messages stands draws no box, as a comment thread draws none under a deleted root, and a
// reply the reader typed in the box before the deletes is drawn itself, with Discard my draft.
// A typed draft records no message it answered, and the box moved from one to another, so its
// line names no author (FixAudit3 F7).
test("an Agent card's box answers the newest message that still stands, and a thread with none draws no box", async () => {
  const user = userEvent.setup();
  spy("listAgents", [session]);
  const planner = { id: "planner", kind: "session" } as const;
  const deletedBy = (author: Message["author"]) => ({
    deleted_at: "2026-10-12T03:00:00Z",
    deleted_by: author,
  });
  const answer = message("reply-1", "Looked at it.", {
    author: planner,
    created_at: "2026-10-12T01:00:00Z",
    in_reply_to: "message-1",
  });
  const followUp = message("reply-2", "", {
    ...deletedBy(planner),
    author: planner,
    created_at: "2026-10-12T02:00:00Z",
    in_reply_to: "reply-1",
  });
  const list = spyOn(api, "listAgentMessages").mockResolvedValue([
    { message: message("message-1", "Have a look at the build"), replies: [answer, followUp] },
  ]);
  restores.push(list);
  const createAgentMessage = spyOn(api, "createAgentMessage").mockImplementation(
    async (_session, input) => message("sent", input.body)
  );
  restores.push(createAgentMessage);
  const client = renderCard(plannerAgentRow());
  const card = await screen.findByTestId("inbox-thread-agent");
  const field = () => within(card).getByRole<HTMLTextAreaElement>("textbox", { name: "Comment" });

  await user.click(await within(card).findByRole("textbox", { name: "Comment" }));
  await user.paste("Ship it.");
  await user.click(within(card).getByRole("button", { name: "Send" }));
  await waitFor(() => expect(createAgentMessage).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(field().value).toBe(""));
  const typed = "Raise it to five.";
  await user.click(field());
  await user.paste(typed);

  list.mockResolvedValue([
    {
      message: message("message-1", "", deletedBy({ id: "alice", kind: "user" })),
      replies: [{ ...answer, ...deletedBy(planner), body: "" }, followUp],
    },
  ]);
  await act(() => client.invalidateQueries());
  await waitFor(() => expect(card.querySelector("[data-inbox-agent-composer]")).toBeNull());
  expect({
    box: within(card).queryByRole("textbox", { name: "Comment" }) !== null,
    lines: within(card)
      .queryAllByRole("status")
      .map((status) => status.textContent ?? "")
      .filter((text) => text.includes(typed)),
    sent: createAgentMessage.mock.calls.map(([sessionId, input]) => [sessionId, input.in_reply_to]),
  }).toEqual({
    box: false,
    lines: [
      `The message that your reply answered was deleted, so it takes no reply. Your reply is kept here:${typed}`,
    ],
    sent: [["planner", "reply-1"]],
  });
});

/** A card for each of `threads`, as the Inbox lists them below 1280 px; the setter draws the list
 *  again with the threads it is handed, over the same client. */
function renderCards(threads: readonly MyReplyRow[]): (threads: readonly MyReplyRow[]) => void {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const tree = (rows: readonly MyReplyRow[]): ReactNode => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        {rows.map((row) => (
          <ThreadRowCard key={row.thread_id} thread={row} />
        ))}
      </QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(tree(threads));
  return (next) => view.rerender(tree(next));
}

/** The card drawing thread `id`. */
function cardOf(id: string): HTMLElement {
  const found = document
    .querySelector(`[data-inbox-thread="${id}"]`)
    ?.closest<HTMLElement>("[data-inbox-thread-card]");
  if (found === null || found === undefined) throw new Error(`no card for ${id}`);
  return found;
}

// Two cards can be two threads of one session, each with a reply box of its own. A reply Dispatch
// refused in one is that thread's: a card for the other thread mounts with none of it - no draft,
// no refusal, no pending turn, no Retry that would send it as a reply there - and the refused
// card's Retry replies under its own thread's newest message.
test("an Agent card shows nothing of a reply refused in another card of its session", async () => {
  spy("listAgents", [session]);
  spy(
    "listAgentMessages",
    ["1", "2"].map(
      (n) =>
        ({
          message: message(`message-${n}`, `Question ${n}`),
          replies: [
            message(`reply-${n}`, `Answer ${n}`, {
              author: { id: "planner", kind: "session" },
              created_at: "2026-10-12T01:00:00Z",
              in_reply_to: `message-${n}`,
            }),
          ],
        }) satisfies MessageRead
    )
  );
  const createAgentMessage = spyOn(api, "createAgentMessage")
    .mockRejectedValueOnce(
      new ApiError(400, { code: "MESSAGE_INPUT", error: "Dispatch refused the message" })
    )
    .mockResolvedValue({} as never);
  restores.push(createAgentMessage);
  const first = plannerAgentRow();
  const second = agentRow("message-2", {
    ...first,
    reply: { ...first.reply, id: "reply-2" },
    thread_id: "message-2",
    unread_reply_ids: ["reply-2"],
  });
  const show = renderCards([first]);
  const user = userEvent.setup();

  await waitFor(() => expect(cardOf("message-1").querySelector("textarea")).not.toBeNull());
  const refusedCard = within(cardOf("message-1"));
  await user.click(refusedCard.getByRole("textbox", { name: "Comment" }));
  await user.paste("Reply meant for the first");
  await user.click(refusedCard.getByRole("button", { name: "Send" }));
  await refusedCard.findByText("Couldn't send — Dispatch refused the message");

  show([first, second]);
  const other = within(cardOf("message-2"));
  expect(other.getByRole<HTMLTextAreaElement>("textbox", { name: "Comment" }).value).toBe("");
  expect(other.queryByRole("alert")).toBeNull();
  expect(other.queryByTestId("agent-send-held")).toBeNull();

  await user.click(refusedCard.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(createAgentMessage).toHaveBeenCalledTimes(2));
  expect(
    createAgentMessage.mock.calls.map(([sessionId, input]) => [sessionId, input.in_reply_to])
  ).toEqual([
    ["planner", "reply-1"],
    ["planner", "reply-1"],
  ]);
});

// An Agent card's box replies under its thread's newest message, which no press of the reader's
// moves. A message that comes while the box holds a refused reply moves the box onto it, so the
// refusal has no Retry there, and the box says what it can do: Discard, to write a reply to the
// newer message - never "reply to that message again", which it offers no way to do.
test("an Agent card whose thread moves on under a refused reply says what its box can do", async () => {
  spy("listAgents", [session]);
  const answer = (id: string, body: string, at: string) =>
    message(id, body, {
      author: { id: "planner", kind: "session" },
      created_at: at,
      in_reply_to: "message-1",
    });
  const thread = (replies: Message[]): MessageRead[] => [
    { message: message("message-1", "Have a look at the build"), replies },
  ];
  const first = answer("reply-1", "Looked at it.", "2026-10-12T01:00:00Z");
  const list = spyOn(api, "listAgentMessages").mockResolvedValue(thread([first]));
  restores.push(list);
  const createAgentMessage = spyOn(api, "createAgentMessage").mockRejectedValue(
    new ApiError(400, { code: "MESSAGE_INPUT", error: "Dispatch refused the message" })
  );
  restores.push(createAgentMessage);
  const client = renderCard(plannerAgentRow());
  const user = userEvent.setup();

  await user.click(await screen.findByRole("textbox", { name: "Comment" }));
  await user.paste("Reply to the first answer");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await screen.findByText("Couldn't send — Dispatch refused the message");

  list.mockResolvedValue(
    thread([first, answer("reply-2", "And one more thing.", "2026-10-12T02:00:00Z")])
  );
  await act(() => client.invalidateQueries());
  expect(
    await screen.findByText(
      "This message was written as a reply to an earlier message, and this box now replies to a newer one. Discard it to write a reply here."
    )
  ).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  expect(screen.getByRole("button", { name: "Discard draft" })).toBeTruthy();
  expect(createAgentMessage).toHaveBeenCalledTimes(1);
});

// FixAudit3 F6 of dispatch://INFRA-1523: a delete moves an Agent card's box back onto the newest
// message that still stands, an older one. A reply Dispatch refused for the message deleted since
// says what is true of it - the message it answered was deleted and takes no reply - and never
// that the box now replies to a newer message.
test("an Agent card whose replied message is deleted under a refused reply says that message was deleted", async () => {
  spy("listAgents", [session]);
  const planner = { id: "planner", kind: "session" } as const;
  const answer = message("reply-1", "Looked at it.", {
    author: planner,
    created_at: "2026-10-12T01:00:00Z",
    in_reply_to: "message-1",
  });
  const list = spyOn(api, "listAgentMessages").mockResolvedValue([
    { message: message("message-1", "Have a look at the build"), replies: [answer] },
  ]);
  restores.push(list);
  const createAgentMessage = spyOn(api, "createAgentMessage").mockRejectedValue(
    new ApiError(400, { code: "MESSAGE_INPUT", error: "Dispatch refused the message" })
  );
  restores.push(createAgentMessage);
  const client = renderCard(plannerAgentRow());
  const user = userEvent.setup();

  await user.click(await screen.findByRole("textbox", { name: "Comment" }));
  await user.paste("Reply to the answer");
  await user.click(screen.getByRole("button", { name: "Send" }));
  await screen.findByText("Couldn't send — Dispatch refused the message");

  list.mockResolvedValue([
    {
      message: message("message-1", "Have a look at the build"),
      replies: [{ ...answer, body: "", deleted_at: "2026-10-12T03:00:00Z", deleted_by: planner }],
    },
  ]);
  await act(() => client.invalidateQueries());
  expect(
    await screen.findByText(
      "This message was written as a reply to a message that was deleted, which takes no reply. Discard it to write here."
    )
  ).toBeTruthy();
  expect(screen.queryByText(/this box now replies to a newer one/u)).toBeNull();
  expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  expect(
    createAgentMessage.mock.calls.map(([sessionId, input]) => [sessionId, input.in_reply_to])
  ).toEqual([["planner", "reply-1"]]);
});

// FixAudit3 F7 of dispatch://INFRA-1523: a reply the reader kept under an Agent thread none of
// whose messages stands answered one message of it, which need not be the newest. A send Dispatch
// refused knows the message it answered (`replyTo`), so its kept line names that message's
// author, never the author of the thread's newest message.
test("a refused reply kept under an Agent thread whose messages are all deleted names the author of the message it answered", async () => {
  spy("listAgents", [session]);
  const planner = { id: "planner", kind: "session" } as const;
  const alice = { id: "alice", kind: "user" } as const;
  const answer = message("reply-1", "Looked at it.", {
    author: planner,
    created_at: "2026-10-12T01:00:00Z",
    in_reply_to: "message-1",
  });
  const followUp = message("reply-2", "Any news?", {
    author: alice,
    created_at: "2026-10-12T02:00:00Z",
    in_reply_to: "reply-1",
  });
  const root = message("message-1", "Have a look at the build");
  const list = spyOn(api, "listAgentMessages").mockResolvedValue([
    { message: root, replies: [answer] },
  ]);
  restores.push(list);
  const createAgentMessage = spyOn(api, "createAgentMessage").mockRejectedValue(
    new ApiError(400, { code: "MESSAGE_INPUT", error: "Dispatch refused the message" })
  );
  restores.push(createAgentMessage);
  const client = renderCard(plannerAgentRow());
  const user = userEvent.setup();
  const card = await screen.findByTestId("inbox-thread-agent");
  const refused = "Reply to the answer";

  await user.click(await within(card).findByRole("textbox", { name: "Comment" }));
  await user.paste(refused);
  await user.click(within(card).getByRole("button", { name: "Send" }));
  await within(card).findByText("Couldn't send — Dispatch refused the message");

  const deleted = (post: Message, by: Message["author"]) => ({
    ...post,
    body: "",
    deleted_at: "2026-10-12T03:00:00Z",
    deleted_by: by,
  });
  list.mockResolvedValue([
    {
      message: deleted(root, alice),
      replies: [deleted(answer, planner), deleted(followUp, alice)],
    },
  ]);
  await act(() => client.invalidateQueries());
  await waitFor(() => expect(card.querySelector("[data-inbox-agent-composer]")).toBeNull());
  expect(
    within(card)
      .queryAllByRole("status")
      .map((status) => status.textContent ?? "")
      .filter((text) => text.includes(refused))
  ).toEqual([
    `The message by Planner that your reply answered was deleted, so it takes no reply. Your reply is kept here:${refused}`,
  ]);
  expect(
    createAgentMessage.mock.calls.map(([sessionId, input]) => [sessionId, input.in_reply_to])
  ).toEqual([["planner", "reply-1"]]);
});
