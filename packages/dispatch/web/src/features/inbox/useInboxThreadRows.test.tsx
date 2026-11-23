/**
 * `useInboxThreadRows` over the Dismissed view's paged read (INFRA-450): the pages are flattened
 * once per set of pages, so the rows the hook hands the motion hold keep their identity between
 * renders.
 *
 * The hold adopts what it is given by identity and sets state whenever it differs
 * (`motion-hold.ts`), so a flatten made afresh on every render sets state on every render and
 * never settles - React ends that loop by throwing "Maximum update depth exceeded" and the page
 * draws its error boundary instead of the reader's dismissed threads.
 */

import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, waitFor } from "@testing-library/react";
import { type ReactNode, useRef } from "react";

import { api } from "../../api/client";
import { dismissedThreadsQuery, whoAmIQuery } from "../../api/queries";
import type { MyRepliesResponse, MyReplyRow } from "../../api/types";
import { replyRow } from "../threads/replies-fixture";
import { rowId } from "./list-row";
import { useInboxThreadRows } from "./useInboxThreadRows";

const AS_OF = "2026-10-12T02:00:00Z";

/** Every API mock a test installed, restored after it. */
const restores: { mockRestore: () => void }[] = [];

afterEach(() => {
  cleanup();
  for (const mock of restores.splice(0)) mock.mockRestore();
});

function dismissedPage(rows: readonly MyReplyRow[]): MyRepliesResponse {
  return { as_of: AS_OF, limit: 0, offset: 0, rows: [...rows], total: rows.length };
}

/** A client holding the sign-in read and one settled page of the Dismissed read, so the hook
 *  renders over pages it never has to fetch. */
function seeded(rows: readonly MyReplyRow[]): QueryClient {
  restores.push(spyOn(api, "listMyReplies").mockResolvedValue(dismissedPage(rows)));
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  client.setQueryData(dismissedThreadsQuery().queryKey, {
    pageParams: [undefined],
    pages: [dismissedPage(rows)],
  });
  return client;
}

/** How many times the harness has rendered, which is what a hook that sets state on every render
 *  runs away with. */
let renders = 0;

function Harness(): ReactNode {
  renders += 1;
  const listRef = useRef<HTMLUListElement>(null);
  const threads = useInboxThreadRows({ asksOnly: false, listRef, show: "dismissed" });
  return (
    <ul ref={listRef}>
      {threads.rows.map((row) => (
        <li data-inbox-row={rowId(row)} key={rowId(row)} />
      ))}
    </ul>
  );
}

function rowIds(): string[] {
  return [...document.querySelectorAll("[data-inbox-row]")].map(
    (row) => row.getAttribute("data-inbox-row") ?? ""
  );
}

test("the Dismissed pages are flattened once, so a second render over them sets no state", async () => {
  const client = seeded([replyRow("c1"), replyRow("c9")]);
  renders = 0;
  const view = render(
    <QueryClientProvider client={client}>
      <Harness />
    </QueryClientProvider>
  );

  try {
    await waitFor(() => expect(rowIds()).toEqual(["reply:c1", "reply:c9"]));
    const settled = renders;

    // One render asked for, one render made: the rows the hold compares are the same array, so
    // its layout effect has nothing to adopt and asks for no render of its own.
    view.rerender(
      <QueryClientProvider client={client}>
        <Harness />
      </QueryClientProvider>
    );
    expect(renders).toBe(settled + 1);
    expect(rowIds()).toEqual(["reply:c1", "reply:c9"]);
  } finally {
    view.unmount();
  }
});

/** The Inbox view's thread rows, which the reader's unpaged read fills. */
function InboxThreads(): ReactNode {
  const listRef = useRef<HTMLUListElement>(null);
  const threads = useInboxThreadRows({ asksOnly: false, listRef, show: "inbox" });
  return (
    <ul ref={listRef}>
      {threads.rows.map((row) => (
        <li data-inbox-row={rowId(row)} key={rowId(row)} />
      ))}
    </ul>
  );
}

// The reader's threads are read under the `person` scope. A person's Inbox reads them; an agent
// viewer's, whose bearer may hold none, asks for none and lists none.
test("a person's Inbox reads their threads, and an agent viewer's asks for none", async () => {
  const read = spyOn(api, "listMyReplies").mockResolvedValue(dismissedPage([replyRow("c1")]));
  restores.push(read);
  for (const viewer of [
    { event_head: "0", kind: "user" as const, login: "alice" },
    { event_head: "0", kind: "agent" as const, login: "agent", session: null },
  ]) {
    read.mockClear();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(whoAmIQuery().queryKey, viewer);
    const view = render(
      <QueryClientProvider client={client}>
        <InboxThreads />
      </QueryClientProvider>
    );
    try {
      if (viewer.kind === "user") {
        await waitFor(() => expect(rowIds()).toEqual(["reply:c1"]));
        expect(read).toHaveBeenCalledTimes(1);
      } else {
        await act(async () => {});
        expect(read).not.toHaveBeenCalled();
        expect(rowIds()).toEqual([]);
      }
    } finally {
      view.unmount();
    }
  }
});
