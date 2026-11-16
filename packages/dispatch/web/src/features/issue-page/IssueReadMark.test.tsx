/**
 * The issue's read mark at the end of the new page's outline (dispatch://INFRA-3949 R9): the end
 * three-quarters on screen for a second writes the issue's head as read, once, and a mark already
 * at the head is written again by nothing.
 */

import { afterEach, expect, jest, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, waitFor } from "@testing-library/react";

import { jump, realClock } from "../../__tests__/fake-clock";
import { dwellOn, installIntersectionObserver } from "../../__tests__/intersection-observer";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerIssueStates } from "../../__tests__/send-answers";
import { api } from "../../api/client";
import type { DispatchEvent, UserIssueState, UserState } from "../../api/types";
import { IssueReadMark } from "./IssueReadMark";

afterEach(() => {
  cleanup();
});

/** The issue's newest event, which is what a dwell on the end reads the issue through. */
function headEvent(seq: number): DispatchEvent {
  return {
    actor: { id: "bob", kind: "user" },
    created_at: "2026-11-16T00:00:00Z",
    id: seq,
    issue_key: "CORE-1",
    notify: false,
    payload: {},
    seq,
    type: "issue.created",
  } as DispatchEvent;
}

function issueState(lastReadSeq: number): UserIssueState {
  return { dismissed: [], last_read_seq: lastReadSeq, pinned: false, seq: 0 };
}

/** Draws the mark for CORE-1 on a page of its own, signed in as alice, and hands back the mark's
 *  element; the reads it makes are the test's to answer. */
function drawMark() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  // The mark goes as the person reading.
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const view = render(
    <QueryClientProvider client={client}>
      <IssueReadMark issueKey="CORE-1" />
    </QueryClientProvider>
  );
  const mark = view.container.querySelector("[data-outline-end]");
  if (mark === null) throw new Error("expected the end of the outline");
  return { client, mark, view };
}

test("the end on screen for a second writes the mark once, and nothing when it is there", async () => {
  const viewport = installIntersectionObserver();
  let stored = 1;
  const getMyState = spyOn(api, "getMyState").mockImplementation(
    async (): Promise<UserState> => ({ "CORE-1": issueState(stored) })
  );
  const getIssueEvents = spyOn(api, "getIssueEvents").mockResolvedValue([headEvent(7)]);
  const putIssueState = answerIssueStates().mockImplementation(async (_key, input) =>
    issueState(input.last_read_seq ?? 0)
  );
  try {
    // Under the head: the end on screen for a second reads the issue through it, as the reader.
    const unread = drawMark();
    await waitFor(() =>
      expect(unread.client.getQueryData<UserState>(["user-state"])?.["CORE-1"]?.last_read_seq).toBe(
        1
      )
    );
    await waitFor(() => expect(viewport.observed()).toContain(unread.mark));
    expect(getIssueEvents).toHaveBeenCalledWith("CORE-1", { limit: 1, order: "desc" });
    jest.useFakeTimers();
    try {
      await dwellOn(viewport, unread.mark, { height: 1 });
      expect(putIssueState.mock.calls).toEqual([["CORE-1", { last_read_seq: 7 }, keyedAttempt]]);
      expect(putIssueState.mock.calls[0]?.[2]?.login).toBe("alice");
      expect(unread.client.getQueryData<UserState>(["user-state"])?.["CORE-1"]?.last_read_seq).toBe(
        7
      );
      // Once: the end on screen again, and for longer, writes nothing more.
      await dwellOn(viewport, unread.mark, { height: 1 });
      await jump(2_000);
      expect(putIssueState).toHaveBeenCalledTimes(1);
    } finally {
      realClock();
    }
    unread.view.unmount();

    // At the head already: the same dwell writes nothing.
    stored = 7;
    const read = drawMark();
    await waitFor(() =>
      expect(read.client.getQueryData<UserState>(["user-state"])?.["CORE-1"]?.last_read_seq).toBe(7)
    );
    await waitFor(() => expect(viewport.observed()).toContain(read.mark));
    jest.useFakeTimers();
    try {
      await dwellOn(viewport, read.mark, { height: 1 });
      await jump(2_000);
    } finally {
      realClock();
    }
    expect(putIssueState).toHaveBeenCalledTimes(1);
  } finally {
    getMyState.mockRestore();
    getIssueEvents.mockRestore();
    putIssueState.mockRestore();
    viewport.restore();
  }
});
