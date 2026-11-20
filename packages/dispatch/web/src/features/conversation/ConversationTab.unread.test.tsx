import { expect, test } from "bun:test";
import { render, screen, waitFor, within } from "@testing-library/react";

import { commentDeliveryFields } from "../../__tests__/comment-fixture";
import { newQueryClient, stubLists, tab } from "../../__tests__/draft-matrix-fixture";
import { expectSameElements } from "../../__tests__/same-elements";
import type { Comment, DispatchEvent, UserState } from "../../api/types";

/**
 * The Conversation tab's `New` marks over one visit (`ConversationTab`'s `visitMarks`). It is a
 * file of its own beside `ConversationTab.test.tsx`, over the same fixture
 * (`draft-matrix-fixture.tsx`).
 */

function comment(id: string, body: string, createdAt: string): Comment {
  return {
    ...commentDeliveryFields(),
    anchor: null,
    ask_id: null,
    author: { id: "alice", kind: "user" },
    body,
    created_at: createdAt,
    edited_at: null,
    id,
    issue_key: "CORE-1",
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
  };
}

/** One event of CORE-1's log, as the tab's one-row reads of the log answer it. */
function logEvent(seq: number, at: string): DispatchEvent {
  return {
    actor: { id: "alice", kind: "user" },
    created_at: at,
    id: seq,
    issue_key: "CORE-1",
    notify: false,
    payload: {},
    seq,
    type: "issue.created",
  } as DispatchEvent;
}

/** The reader's state of CORE-1, read through event `seq`. */
function readThrough(seq: number): UserState {
  return { "CORE-1": { dismissed: [], last_read_seq: seq, pinned: false, seq: 0 } };
}

/** The list's turns in page order. */
function turnElements(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>("[data-turn]")];
}

/** Each turn's `data-turn`, and whether it draws a `New` mark. */
function marks(): [string | null, boolean][] {
  return turnElements().map((turn) => [
    turn.getAttribute("data-turn"),
    within(turn).queryByText("New", { exact: true }) !== null,
  ]);
}

// dispatch://INFRA-2160, R1: the read mark, written a second after the newest card is on screen,
// moved `lastReadSeq`, which is in the unread boundary read's key; the read under the new key
// started empty, every `New` went with it, and each turn below one moved up by its height, under
// a press the reader was making on a link there.
test("the read mark moving lastReadSeq leaves every New drawn and moves no turn", async () => {
  const stub = stubLists({
    comments: [
      comment("comment-1", "An older comment", "2026-09-20T00:00:00Z"),
      comment("comment-2", "A newer comment", "2026-09-20T00:05:00Z"),
    ],
    head: [logEvent(9, "2026-09-20T00:05:00Z")],
    unread: [logEvent(4, "2026-09-20T00:00:00Z")],
  });
  const queryClient = newQueryClient();
  const view = render(tab(readThrough(3), true, queryClient));
  try {
    await screen.findByText("A newer comment");
    await waitFor(() => expect(marks().map(([, marked]) => marked)).toEqual([true, true]));
    const drawn = marks();
    const elements = turnElements();

    // The read mark through the head: nothing is unread under the new key.
    stub.lists.unread = [];
    view.rerender(tab(readThrough(9), true, queryClient));
    expect(marks()).toEqual(drawn);
    await waitFor(() =>
      expect(queryClient.getQueryState(["events", "CORE-1", "unread-boundary", 9])?.status).toBe(
        "success"
      )
    );
    expect(marks()).toEqual(drawn);
    expectSameElements(turnElements(), elements);
  } finally {
    view.unmount();
    stub.restore();
  }
});
