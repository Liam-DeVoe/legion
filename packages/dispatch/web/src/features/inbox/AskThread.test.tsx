import { beforeAll, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import { commentDeliveryFields } from "../../__tests__/comment-fixture";
import { api } from "../../api/client";
import type { AskV2, AskWithThread, Comment, CreateCommentInput } from "../../api/types";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import { questionsRead } from "../refs/reference-fixture";
import { AskThread, type AskThreadQuery, type CreateReply } from "./AskThread";

beforeAll(async () => {
  await warmMarkdownRenderer();
});

const answeredAsk: AskV2 = {
  anchor: null,
  answer: { at: "2026-09-09T01:00:00Z", selected: [], text: "Done.", user: "alice" },
  artifact_id: null,
  asked_of: null,
  asked_of_at: null,
  asked_of_by: null,
  author: { id: "session-1", kind: "session" },
  block_id: null,
  created_at: "2026-09-09T00:00:00Z",
  document: null,
  edited_at: null,
  id: "ask-1",
  issue: { assignee: "alice", key: "CORE-1", title: "Fix the thing" },
  issue_key: "CORE-1",
  kind: "question",
  last_reply: null,
  multiple: false,
  opened_event_id: 1,
  options: [],
  priority: null,
  question: "Should this ship?",
  ref: "dispatch://CORE-1/ask/ask-1",
  state: "answered",
  urgency: "med",
};

/** The question block of CORE-1's spec, as the issue's ask list carries it: what a map built
 *  from that list would have named `#q2`, and what the reply below does not mean. */
const specQuestionAsk: AskV2 = {
  ...answeredAsk,
  answer: null,
  author: { id: "planner", kind: "session" },
  block_artifact: { id: "artifact-1", primary: true, slug: "spec" },
  block_id: "q2",
  id: "ask-2",
  question: "Which way round?",
  ref: "dispatch://CORE-1/ask/ask-2",
  state: "open",
};

const reply: Comment = {
  anchor: null,
  ask_id: answeredAsk.id,
  author: { id: "alice", kind: "user" },
  body: "Holding on #q2 for now.",
  created_at: "2026-09-09T02:00:00Z",
  edited_at: null,
  id: "comment-1",
  issue_key: "CORE-1",
  reply_to: null,
  resolved: false,
  resolved_at: null,
  resolved_by: null,
  suggestion: null,
  turn: null,
  ...commentDeliveryFields(),
};

const read: AskWithThread = {
  ...answeredAsk,
  thread: {
    answers: [],
    claimant: null,
    edits: [],
    followers: [],
    replies: [reply],
    route: null,
    route_holder: null,
    route_status: null,
    route_status_reason: null,
  },
};

/** The ask `#q2` names in the reply, read by the reference the reply draws to title it. */
const designAsk: AskV2 = {
  ...answeredAsk,
  answer: null,
  id: "ask-design",
  question: "Which way round?",
  ref: "dispatch://CORE-1/ask/ask-design",
  state: "open",
};

test("an Inbox reply's `#q2` names the question of the document its comment is anchored to", async () => {
  // The reply is anchored to `design.md`, which is no primary document of the issue: the server
  // scopes the comment's text to that document (`refs.ScopeOf`), so `#q2` is that document's
  // question. A map built from the issue's ask list would have chosen the spec's `q2` instead.
  const listIssueAsks = spyOn(api, "listIssueAsks").mockResolvedValue([specQuestionAsk]);
  const getAsk = spyOn(api, "getAsk").mockResolvedValue({
    ...designAsk,
    thread: { ...read.thread, replies: [] },
  });
  const getReferenceQuestions = spyOn(api, "getReferenceQuestions").mockResolvedValue(
    questionsRead([{ ...designAsk, question_block_id: "q2" }], "artifact-design")
  );
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  const thread = { data: read } as unknown as AskThreadQuery;
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <AskThread ask={answeredAsk} order="oldest" showComposer={false} thread={thread} />
      </QueryClientProvider>
    </MemoryRouter>
  );

  try {
    const linked = () =>
      [...view.container.querySelectorAll("[data-reply] [data-dispatch-ref]")].map((anchor) =>
        anchor.getAttribute("data-dispatch-ref")
      );
    await waitFor(() => expect(linked()).toEqual(["dispatch://CORE-1/ask/ask-design"]));
    expect(getReferenceQuestions).toHaveBeenCalledWith("comment", "comment-1");
    // One map: the page reads the server's, never the issue's whole ask list.
    expect(listIssueAsks).not.toHaveBeenCalled();
  } finally {
    view.unmount();
    getAsk.mockRestore();
    getReferenceQuestions.mockRestore();
    listIssueAsks.mockRestore();
  }
});

// A settled question's conversation goes on: however an ask was resolved, its thread keeps the
// reply composer an answered ask's has, and what it sends is a reply on that ask.
test("a resolved ask's thread takes a reply on that ask, whichever way it was resolved", async () => {
  const user = userEvent.setup();
  for (const kind of ["resolved", "retracted", "declined", "withdrawn"]) {
    const resolved: AskV2 = {
      ...answeredAsk,
      answer: null,
      resolution: {
        actor: { id: "session-1", kind: "session" },
        at: "2026-09-09T03:00:00Z",
        kind,
        reason: "Settled elsewhere.",
      },
      state: "resolved",
    };
    const sent: [string, CreateCommentInput][] = [];
    const createReply: CreateReply = async (issueKey, input) => {
      sent.push([issueKey, input]);
      return { ...reply, body: input.body ?? "", id: `reply-${kind}` };
    };
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
    });
    const thread = {
      data: { ...read, ...resolved, thread: { ...read.thread, replies: [] } },
    } as unknown as AskThreadQuery;
    const view = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <AskThread ask={resolved} createReply={createReply} order="oldest" thread={thread} />
        </QueryClientProvider>
      </MemoryRouter>
    );

    try {
      await user.click(view.getByRole("textbox", { name: "Reply" }));
      await user.paste(`Still worth a look (${kind}).`);
      await user.click(view.getByRole("button", { name: "Reply" }));
      await waitFor(() =>
        expect(sent).toEqual([
          ["CORE-1", { ask_id: "ask-1", body: `Still worth a look (${kind}).` }],
        ])
      );
    } finally {
      view.unmount();
    }
  }
});
