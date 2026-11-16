import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useEffect, useState } from "react";
import { MemoryRouter } from "react-router-dom";

import { commentDeliveryFields } from "../../__tests__/comment-fixture";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { ApiError, api } from "../../api/client";
import { userStateQuery } from "../../api/queries";
import type {
  Comment,
  CommentDelivery,
  DispatchEvent,
  IssueDetails,
  Subscriber,
  UserState,
} from "../../api/types";
import { queuedWait } from "../conversation/delivery";
import { conversationItemId, pinnedItemMarker } from "../issue/pins";
import { DetailsFocusedNode, DetailsHistory, DetailsPeople } from "./details-more";
import { type FocusedNode, FocusedNodeProvider, useSetFocusedNode } from "./focused-node";
import { type MarginDrawer, MarginSlotProvider } from "./margin-slot";
import { type OutlineWriter, OutlineWriterProvider, useOutlineWriter } from "./outline-writer";
import { PROPERTY_LABELS } from "./page-structure";
import {
  ANSWER_LINES,
  CONTROL_LABELS,
  DELIVERY_STATE_WORDS,
  issueStatusWord,
  NODE_REFERENCES_WORDS,
  NODE_STATE_WORDS,
  NODE_TYPE_NAMES,
  STATE_LINES,
  sentenceCase,
} from "./page-words";

const KEY = "CORE-1";

const issue = {
  artifacts: [],
  key: KEY,
  status: "in_progress",
  title: "Review the spec",
} as unknown as IssueDetails;

/** The reader's own comment, which heads a thread and was delivered to nobody. */
const OWN_COMMENT = {
  ask_id: null,
  author: { id: "alice", kind: "user" },
  body: "The reader's own words.",
  created_at: "2026-11-15T10:00:00Z",
  deliveries: [],
  id: "comment-1",
  reply_to: null,
  resolved: false,
  suggestion: null,
} as unknown as Comment;

/** A client holding every read the facts and the writer make, so the drawing sends none. */
function readsHeld(): QueryClient {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["agents"], []);
  client.setQueryData(["asks", KEY], []);
  client.setQueryData(["comments", KEY], [OWN_COMMENT]);
  client.setQueryData(["issue-messages", KEY], []);
  client.setQueryData(["whoami"], { login: "alice" });
  return client;
}

/** Gives `node` the reader's focus, as the outline does when a row takes it. */
function Focus({ node }: { node: FocusedNode }): ReactNode {
  const setFocused = useSetFocusedNode();
  useEffect(() => setFocused(node), [node, setFocused]);
  return null;
}

// R6 of dispatch://INFRA-3948: until the outline gives a row the focus, and whenever the reader
// is on no row, the facts are the issue's own - its kind, its title and its status - and the
// issue draws no list of what references it, because Details' "Referenced by" row holds it.
test("with no node focused, the facts are the issue's", () => {
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>
        <FocusedNodeProvider>
          <DetailsFocusedNode issue={issue} state={undefined} />
        </FocusedNodeProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    const facts = screen.getByTestId("focused-node-facts");
    expect(facts.getAttribute("data-focused-node")).toBe(KEY);
    expect(
      within(facts).getByRole("heading", { name: PROPERTY_LABELS.focused_node })
    ).toBeDefined();
    const text = facts.textContent ?? "";
    expect(text).toContain(`${sentenceCase(NODE_TYPE_NAMES.issue)}:`);
    expect(text).toContain(issue.title);
    expect(text).toContain(issueStatusWord("in_progress"));
    expect(
      within(facts).queryByRole("button", { name: NODE_REFERENCES_WORDS.what_references })
    ).toBeNull();
  } finally {
    view.unmount();
  }
});

// R7 of dispatch://INFRA-3948: Edit on the reader's own comment is drawn only under the page's
// writer, and sets it to edit that comment and its words; the box that saves it is part L's.
test("Edit on the reader's own comment sets the writer to edit it", async () => {
  let writer: OutlineWriter | undefined;
  function Probe(): ReactNode {
    writer = useOutlineWriter();
    return null;
  }
  const comment: FocusedNode = {
    author: OWN_COMMENT.author,
    id: "comment-1",
    item: { id: "comment-1", kind: "comment" },
    reference: `dispatch://${KEY}/comment/comment-1`,
    title: OWN_COMMENT.body,
    type: "comment",
  };
  const facts = (
    <FocusedNodeProvider>
      <Focus node={comment} />
      <DetailsFocusedNode issue={issue} state={undefined} />
    </FocusedNodeProvider>
  );
  // With no writer above them, the facts draw no Edit.
  const alone = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>{facts}</QueryClientProvider>
    </MemoryRouter>
  );
  await waitFor(() =>
    expect(screen.getByTestId("focused-node-facts").getAttribute("data-focused-node")).toBe(
      "comment-1"
    )
  );
  expect(screen.queryByRole("button", { name: CONTROL_LABELS.edit })).toBeNull();
  alone.unmount();

  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>
        <OutlineWriterProvider issueKey={KEY}>
          <Probe />
          {facts}
        </OutlineWriterProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    const edit = await screen.findByRole("button", { name: CONTROL_LABELS.edit });
    expect(writer?.editing, "the writer edited something before Edit").toBeUndefined();

    await userEvent.click(edit);

    await waitFor(() =>
      expect(writer?.editing).toEqual({ body: OWN_COMMENT.body, id: "comment-1" })
    );
    expect(writer?.reply).toBeNull();
  } finally {
    view.unmount();
  }
});

/** The reader's state of the issue, read through `seq`, with nothing pinned. */
function readTo(seq: number): UserState {
  return { [KEY]: { dismissed: [], last_read_seq: seq, pinned: false, seq: 0 } };
}

/** An agent subscribed to the issue's own topic, so Unsubscribe can take it off. */
const SUBSCRIBER: Subscriber = {
  last_seen: Date.parse("2026-11-15T10:00:00Z"),
  live: false,
  removable: true,
  session_id: "planner",
  title: "Planner",
  topics: [`dispatch.issue.${KEY}`],
};

/** The People rows of Details, at the issue's own address, over the reads `readsHeld` holds:
 *  the subscribers' read is each case's own. */
function renderPeople() {
  return render(
    <MemoryRouter initialEntries={[`/issues/${KEY}`]}>
      <QueryClientProvider client={readsHeld()}>
        <DetailsPeople issue={issue} state={undefined} />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

/** The Subscribers row's control, which carries the count once the read answers. */
const SUBSCRIBERS = new RegExp(`^${PROPERTY_LABELS.subscribers}`, "u");

// R5 of dispatch://INFRA-3948: the subscribers' read, which counts them on every load, says so
// where it failed, with no count it cannot make, and its Retry reads them again.
test("a failed read of the subscribed agents says so, with Retry", async () => {
  const getIssueSubscribers = spyOn(api, "getIssueSubscribers")
    .mockRejectedValueOnce(
      new ApiError(503, { code: "ENVOY_UNAVAILABLE", error: "envoy listener unreachable" })
    )
    .mockResolvedValue([SUBSCRIBER]);
  const view = renderPeople();
  try {
    expect((await screen.findByRole("alert")).textContent).toBe(STATE_LINES.subscribers_unreadable);
    const subscribers = screen.getByRole("button", { name: SUBSCRIBERS });
    expect(subscribers.textContent).toBe(PROPERTY_LABELS.subscribers);

    await userEvent.click(screen.getByRole("button", { name: CONTROL_LABELS.retry }));

    await waitFor(() => expect(getIssueSubscribers).toHaveBeenCalledTimes(2));
    expect(getIssueSubscribers).toHaveBeenLastCalledWith(KEY);
    await waitFor(() => expect(subscribers.textContent).toBe(`${PROPERTY_LABELS.subscribers}:1`));
    expect(screen.queryByRole("alert")).toBeNull();
  } finally {
    view.unmount();
    getIssueSubscribers.mockRestore();
  }
});

// R5 of dispatch://INFRA-3948: Unsubscribe asks for Confirm first; a write Dispatch refused says
// so, and its Retry sends the same session's unsubscribe again.
test("a failed Unsubscribe says so, with Retry", async () => {
  let subscribed = [SUBSCRIBER];
  const getIssueSubscribers = spyOn(api, "getIssueSubscribers").mockImplementation(
    async () => subscribed
  );
  const unsubscribeIssueSession = spyOn(api, "unsubscribeIssueSession")
    .mockRejectedValueOnce(new ApiError(403, { code: "FORBIDDEN", error: "not yours to remove" }))
    .mockImplementation(async () => {
      subscribed = [];
    });
  const view = renderPeople();
  try {
    const subscribers = await screen.findByRole("button", { name: SUBSCRIBERS });
    await waitFor(() => expect(subscribers.textContent).toBe(`${PROPERTY_LABELS.subscribers}:1`));
    await userEvent.click(subscribers);
    await userEvent.click(screen.getByRole("button", { name: "Unsubscribe" }));
    expect(unsubscribeIssueSession, "Unsubscribe wrote before Confirm").not.toHaveBeenCalled();
    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Confirm" })
    );

    expect((await screen.findByRole("alert")).textContent).toBe(ANSWER_LINES.unsubscribe_failed);
    expect(unsubscribeIssueSession).toHaveBeenCalledTimes(1);
    expect(unsubscribeIssueSession).toHaveBeenLastCalledWith(KEY, "planner", keyedAttempt);

    await userEvent.click(screen.getByRole("button", { name: CONTROL_LABELS.retry }));

    await waitFor(() => expect(unsubscribeIssueSession).toHaveBeenCalledTimes(2));
    expect(unsubscribeIssueSession).toHaveBeenLastCalledWith(KEY, "planner", keyedAttempt);
    await waitFor(() => expect(subscribers.textContent).toBe(`${PROPERTY_LABELS.subscribers}:0`));
    expect(screen.queryByRole("alert")).toBeNull();
  } finally {
    view.unmount();
    getIssueSubscribers.mockRestore();
    unsubscribeIssueSession.mockRestore();
  }
});

// R6 of dispatch://INFRA-3948: under "The row you are on", the facts of the row the reader is on:
// its kind with its first line, its state where its kind has one, and who wrote it.
test("the facts name the focused node's kind, first line, state and author", async () => {
  const question: FocusedNode = {
    author: { id: "bob", kind: "user" },
    id: "ask-1",
    state: NODE_STATE_WORDS.ask.open,
    title: "Which store holds the drafts?",
    type: "ask",
  };
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>
        <FocusedNodeProvider>
          <Focus node={question} />
          <DetailsFocusedNode issue={issue} state={undefined} />
        </FocusedNodeProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    await waitFor(() =>
      expect(screen.getByTestId("focused-node-facts").getAttribute("data-focused-node")).toBe(
        "ask-1"
      )
    );
    const facts = screen.getByTestId("focused-node-facts");
    expect(within(facts).getByRole("heading", { level: 2 }).textContent).toBe(
      PROPERTY_LABELS.focused_node
    );
    // Each fact as `[label, value]`, in the order Details draws them.
    expect(
      [...facts.querySelectorAll("dl > div")].map((fact) => [
        fact.querySelector("dt")?.textContent,
        fact.querySelector("dd")?.textContent,
      ])
    ).toEqual([
      [`${sentenceCase(NODE_TYPE_NAMES.ask)}:`, "Which store holds the drafts?"],
      [`${PROPERTY_LABELS.status}:`, NODE_STATE_WORDS.ask.open],
      [`${CONTROL_LABELS.written_by}:`, "bob"],
    ]);
  } finally {
    view.unmount();
  }
});

/** A comment of bob's that heads a thread, in the outline's read of the issue's comments. */
function bobsComment(id: string, over: Partial<Comment> = {}): Comment {
  return {
    ...commentDeliveryFields(),
    anchor: null,
    ask_id: null,
    author: { id: "bob", kind: "user" },
    body: `Bob's comment ${id}.`,
    created_at: "2026-11-15T10:00:00Z",
    edited_at: null,
    id,
    issue_key: KEY,
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
    ...over,
  };
}

/** The focused row of `comment`, as the outline hands it to Details. */
function commentNode(comment: Comment): FocusedNode {
  return {
    author: comment.author,
    id: comment.id,
    item: { id: comment.id, kind: "comment" },
    title: comment.body,
    type: "comment",
  };
}

/** The facts of the focused row over the reader's state as the page holds it (`["user-state"]`),
 *  which a pin writes at once and Dispatch's answer then replaces. */
function FactsOverState(): ReactNode {
  const state = useQuery(userStateQuery()).data;
  return <DetailsFocusedNode issue={issue} state={state} />;
}

// R6 of dispatch://INFRA-3948: the card controls of a comment that heads a thread and suggests a
// change follow its facts. Each sends its own write, and the facts then draw what that write
// left: Unpin once it is pinned, Reopen once its thread is resolved - with its Accept and Reject
// still there, since resolving decides no suggestion - and neither once the suggestion is taken.
test("a comment's facts pin it, resolve its thread and accept its suggestion", async () => {
  const suggested = bobsComment("comment-3", {
    suggestion: { accepted: null, replace_with: "the drafts store" },
  });
  let comments = [suggested];
  const listComments = spyOn(api, "listComments").mockImplementation(async () => comments);
  const getMyState = spyOn(api, "getMyState").mockResolvedValue(readTo(0));
  const putIssueState = spyOn(api, "putIssueState").mockImplementation(async (_key, input) => ({
    dismissed: input.dismissed ?? [],
    last_read_seq: 0,
    pinned: false,
    seq: input.seq ?? 0,
  }));
  const resolveComment = spyOn(api, "resolveComment").mockImplementation(async () => {
    const resolved = { ...suggested, resolved: true };
    comments = [resolved];
    return resolved;
  });
  const acceptComment = spyOn(api, "acceptComment").mockImplementation(async () => {
    const accepted = {
      ...suggested,
      resolved: true,
      suggestion: { accepted: true, replace_with: "the drafts store" },
    };
    comments = [accepted];
    return { ...accepted, lost: false };
  });
  const client = readsHeld();
  client.setQueryData(["comments", KEY], comments);
  client.setQueryData(["user-state"], readTo(0));
  const node = commentNode(suggested);
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <FocusedNodeProvider>
          <Focus node={node} />
          <FactsOverState />
        </FocusedNodeProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    await screen.findByRole("button", { name: CONTROL_LABELS.resolve });
    const controls = document.querySelector<HTMLElement>("[data-focused-controls]");
    if (controls === null) throw new Error("the comment's facts draw no controls");
    const control = (name: string) => within(controls).queryByRole("button", { name });
    expect(control(CONTROL_LABELS.accept)).not.toBeNull();
    expect(control(CONTROL_LABELS.reject)).not.toBeNull();

    await userEvent.click(within(controls).getByRole("button", { name: CONTROL_LABELS.pin }));
    await waitFor(() =>
      expect(putIssueState).toHaveBeenCalledWith(
        KEY,
        { dismissed: [pinnedItemMarker(conversationItemId("comment", "comment-3"))], seq: 1 },
        keyedAttempt
      )
    );
    expect(control(CONTROL_LABELS.unpin)?.getAttribute("aria-pressed")).toBe("true");

    await userEvent.click(within(controls).getByRole("button", { name: CONTROL_LABELS.resolve }));
    await waitFor(() => expect(resolveComment).toHaveBeenCalledWith("comment-3", keyedAttempt));
    await within(controls).findByRole("button", { name: CONTROL_LABELS.reopen_thread });
    expect(control(CONTROL_LABELS.resolve)).toBeNull();
    expect(control(CONTROL_LABELS.reject)).not.toBeNull();

    await userEvent.click(within(controls).getByRole("button", { name: CONTROL_LABELS.accept }));
    await waitFor(() => expect(acceptComment).toHaveBeenCalledWith("comment-3", keyedAttempt));
    await waitFor(() => expect(control(CONTROL_LABELS.accept)).toBeNull());
    expect(control(CONTROL_LABELS.reject)).toBeNull();
    expect(control(CONTROL_LABELS.unpin)).not.toBeNull();
    expect(control(CONTROL_LABELS.reopen_thread)).not.toBeNull();
  } finally {
    view.unmount();
    listComments.mockRestore();
    getMyState.mockRestore();
    putIssueState.mockRestore();
    resolveComment.mockRestore();
    acceptComment.mockRestore();
  }
});

/** One attempt to deliver `comment-2`'s mention to `target`. */
function attempt(target: string, over: Partial<CommentDelivery>): CommentDelivery {
  return {
    attempt: 1,
    comment_id: "comment-2",
    created_at: new Date().toISOString(),
    delivery: "btw",
    duplicate: false,
    envelope_id: null,
    error: null,
    reply_id: null,
    resolve_error: null,
    session_id: null,
    state: "sent",
    target,
    ...over,
  };
}

// R6 of dispatch://INFRA-3948, and the Spec's `activity-feed-names-mention-delivery-outcomes`:
// each delivery of the focused comment says how it stands. One that could not reach its agent
// reads "Not delivered", with Retry only while a Retry cannot deliver it twice (`isSafeRetry`),
// and one the listener holds for a stopped session names the wait, as `queuedParts` builds it.
test("a delivery that could not reach its agent reads Not delivered with Retry, and a queued one names its wait", async () => {
  const mentioned = bobsComment("comment-2", {
    deliveries: [
      attempt("session:planner", { error: "no live session planner", state: "failed" }),
      attempt("session:tester", {
        created_at: new Date(Date.now() - 4 * 24 * 60 * 60 * 1000).toISOString(),
        error: "no live session tester",
        state: "failed",
      }),
      attempt("session:reviewer", { state: "queued" }),
    ],
  });
  const listComments = spyOn(api, "listComments").mockResolvedValue([mentioned]);
  const createCommentDelivery = spyOn(api, "createCommentDelivery").mockImplementation(
    async (_id, target, delivery) => attempt(target ?? "", { attempt: 2, delivery })
  );
  const client = readsHeld();
  client.setQueryData(["comments", KEY], [mentioned]);
  const node = commentNode(mentioned);
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <FocusedNodeProvider>
          <Focus node={node} />
          <DetailsFocusedNode issue={issue} state={undefined} />
        </FocusedNodeProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    await waitFor(() =>
      expect(document.querySelectorAll("[data-deliveries] > li")).toHaveLength(3)
    );
    const [failed, old, queued] = [
      ...document.querySelectorAll<HTMLElement>("[data-deliveries] > li"),
    ];
    if (failed === undefined || old === undefined || queued === undefined) {
      throw new Error("the facts draw fewer than three deliveries");
    }
    const retryOf = (row: HTMLElement) =>
      within(row).queryByRole("button", { name: CONTROL_LABELS.retry });
    expect(failed.textContent).toContain(DELIVERY_STATE_WORDS.failed);
    expect(failed.textContent).not.toContain(DELIVERY_STATE_WORDS.sent);
    expect(retryOf(failed)).not.toBeNull();
    // Past the window in which a repeat is recognised, a Retry could deliver it twice.
    expect(old.textContent).toContain(DELIVERY_STATE_WORDS.failed);
    expect(retryOf(old)).toBeNull();
    // A queued mention is neither delivered nor refused, so it has nothing to retry.
    const reviewer = queued.querySelector('[data-agent-name="reviewer"]')?.textContent;
    expect(queued.textContent).toContain(`Queued: ${reviewer} ${queuedWait}`);
    expect(queued.textContent).not.toContain(DELIVERY_STATE_WORDS.sent);
    expect(retryOf(queued)).toBeNull();

    await userEvent.click(within(failed).getByRole("button", { name: CONTROL_LABELS.retry }));

    await waitFor(() =>
      expect(createCommentDelivery).toHaveBeenCalledWith(
        "comment-2",
        "session:planner",
        "btw",
        keyedAttempt
      )
    );
    expect(createCommentDelivery).toHaveBeenCalledTimes(1);
  } finally {
    view.unmount();
    listComments.mockRestore();
    createCommentDelivery.mockRestore();
  }
});

/** A claim of the issue, which the history draws as one line, made `10 - seq` seconds ago, so a
 *  higher sequence is newer and each is today's. */
function claim(seq: number): DispatchEvent {
  return {
    actor: { id: "planner", kind: "session" },
    created_at: new Date(Date.now() - (10 - seq) * 1000).toISOString(),
    id: seq,
    issue_key: KEY,
    notify: false,
    payload: {},
    seq,
    type: "issue.claimed",
  } as DispatchEvent;
}

/** What opens and shuts Details around the history, and moves the reader's read mark: the
 *  test's hold on `HistoryInDrawer`. */
interface DrawerHold {
  open?: (open: boolean) => void;
  read?: (state: UserState) => void;
}

/** The history in Details as the page draws it: inside the drawer, which starts shut, over the
 *  reader's state of the issue, read through its second event. */
function HistoryInDrawer({ hold }: { hold: DrawerHold }): ReactNode {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState(() => readTo(2));
  hold.open = setOpen;
  hold.read = setState;
  const drawer: MarginDrawer = { open, setOpen, setPage: () => undefined };
  return (
    <MarginSlotProvider drawer={drawer}>
      <DetailsHistory issue={issue} state={state} />
    </MarginSlotProvider>
  );
}

// R2 of dispatch://INFRA-3948: the line that says what is new stands where the reader's mark
// stood when the history first drew in this visit. The history is not drawn while Details is
// shut, so `DetailsHistory` holds that mark: a move of the mark in between - another tab read
// the issue - leaves the line where the reader found it when Details opens again.
test("the line that says what is new holds across a close and an open of Details", async () => {
  const getIssueEvents = spyOn(api, "getIssueEvents").mockResolvedValue(
    [4, 3, 2, 1].map((seq) => claim(seq))
  );
  const hold: DrawerHold = {};
  /** The history's entries in the order it draws them: each event by its sequence, and the
   *  line that says what is new by its words. */
  const drawn = () =>
    [...document.querySelectorAll("[data-history-day] > ol > li")].map(
      (entry) => entry.getAttribute("data-event-seq") ?? entry.textContent
    );
  const atTheMark = ["4", "3", CONTROL_LABELS.history_unread, "2", "1"];
  const view = render(
    <MemoryRouter initialEntries={[`/issues/${KEY}`]}>
      <QueryClientProvider client={readsHeld()}>
        <HistoryInDrawer hold={hold} />
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    // Shut, Details draws no history and reads no log.
    expect(screen.queryByTestId("issue-history")).toBeNull();
    expect(getIssueEvents).not.toHaveBeenCalled();

    act(() => hold.open?.(true));
    await waitFor(() => expect(drawn()).toEqual(atTheMark));

    act(() => hold.open?.(false));
    expect(screen.queryByTestId("issue-history")).toBeNull();
    act(() => hold.read?.(readTo(4)));
    act(() => hold.open?.(true));

    await waitFor(() => expect(document.querySelectorAll("[data-event-seq]")).toHaveLength(4));
    expect(drawn()).toEqual(atTheMark);
  } finally {
    view.unmount();
    getIssueEvents.mockRestore();
  }
});
