import { afterEach, beforeEach, jest, type Mock, spyOn } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { graphNode } from "../../__tests__/graph-fixture";
import { api } from "../../api/client";
import type { AskAnswer, AskV2, AskWithThread, Comment } from "../../api/types";

// The card `AskCard.test.tsx` and `AskCard.stopped-writes.test.tsx` both render, and the reads
// every render of it answers.

/** Installs the mocks both files' cards read through: call once at module scope. */
export function installAskCardApiMocks() {
  let whoAmI: Mock<typeof api.whoAmI>;
  let getReferences: Mock<typeof api.getReferences>;
  let getIssueSubscribers: Mock<typeof api.getIssueSubscribers>;

  beforeEach(() => {
    whoAmI = spyOn(api, "whoAmI").mockResolvedValue({
      event_head: "0",
      kind: "user",
      login: "alice",
    });
    getReferences = spyOn(api, "getReferences").mockResolvedValue({
      items: [],
      limit: 50,
      node: graphNode({ id: "ask-1", kind: "ask" }),
      offset: 0,
      total: 0,
    });
    // Every card with an issue names who the ask reaches, which reads the issue's subscribers;
    // nobody is subscribed here, so the card lists its followers alone.
    getIssueSubscribers = spyOn(api, "getIssueSubscribers").mockResolvedValue([]);
  });

  afterEach(() => {
    whoAmI.mockRestore();
    getReferences.mockRestore();
    getIssueSubscribers.mockRestore();
    jest.useRealTimers();
  });
}

export const firstAnswer: AskAnswer = {
  at: "2026-10-08T01:00:00Z",
  selected: ["Ship"],
  text: "First note",
  user: "alice",
};

export function answeredAsk(overrides: Partial<AskV2> = {}): AskV2 {
  return {
    anchor: null,
    answer: firstAnswer,
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: { id: "session-1", kind: "session" },
    block_id: null,
    created_at: "2026-10-08T00:00:00Z",
    document: null,
    edited_at: null,
    id: "ask-1",
    issue: { assignee: "alice", key: "CORE-1", title: "Fix the thing" },
    issue_key: "CORE-1",
    kind: "question",
    last_reply: null,
    multiple: false,
    opened_event_id: 1,
    options: [{ label: "Ship" }, { label: "Hold" }],
    priority: null,
    question: "Which release path?",
    ref: "dispatch://CORE-1/ask/ask-1",
    state: "answered",
    urgency: "med",
    waiting_on: "human",
    ...overrides,
  };
}

/** The ask read with its thread, as `getAsk` answers it and the card holds it: its answers
 *  (the recorded one alone, unless given) and its replies. */
export function thread(
  ask: AskV2,
  answers: AskAnswer[] = ask.answer === null ? [] : [ask.answer],
  replies: Comment[] = []
): AskWithThread {
  return {
    ...ask,
    thread: {
      answers,
      claimant: null,
      edits: [],
      followers: [],
      replies,
      route: null,
      route_status: null,
      route_holder: null,
      route_status_reason: null,
    },
  };
}

export function renderCard(node: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  const drawn = (card: ReactNode) => (
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>{card}</QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(drawn(node));
  return {
    queryClient,
    /** Draws `next` where the card was, as a host that draws one card for one ask after another
     *  does: the same card, with its state, rendered for the next ask. */
    rerender: (next: ReactNode) => view.rerender(drawn(next)),
    view,
  };
}
