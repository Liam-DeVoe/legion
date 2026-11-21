/**
 * `useInboxThreadRows` over the Dismissed view's paged read (INFRA-450): the pages are flattened
 * once per set of pages, so the rows the hook hands the motion hold keep their identity between
 * renders.
 *
 * The hold adopts what it is given by identity and sets state whenever it differs
 * (`motion-hold.ts`), so a flatten made afresh on every render sets state on every render and
 * never settles - React ends that loop by throwing "Maximum update depth exceeded" and the page
 * draws its error boundary instead of the reader's dismissed threads.
 *
 * The Inbox draws these rows from the one read, with no opt-in (`one=1` on the page's address
 * is left over from when it was one), and the threads' own read is not asked.
 */

import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, waitFor } from "@testing-library/react";
import { type ReactNode, useRef } from "react";
import { MemoryRouter } from "react-router-dom";

import { api } from "../../api/client";
import { meInboxDismissedQuery, whoAmIQuery } from "../../api/queries";
import type { MyReplyRow } from "../../api/types";
import type { InboxShow } from "../refs/routes";
import { replyRow } from "../threads/replies-fixture";
import { answerOneRead, oneReadAnswer } from "./inbox-fixture";
import { rowId } from "./list-row";
import { useInboxThreadRows } from "./useInboxThreadRows";

const AS_OF = "2026-10-12T02:00:00Z";

/** Every API mock a test installed, restored after it. */
const restores: { mockRestore: () => void }[] = [];

afterEach(() => {
  cleanup();
  for (const mock of restores.splice(0)) mock.mockRestore();
});

/** A client holding the sign-in read and one settled page of the one read's Dismissed view, so
 *  the hook renders over pages it never has to fetch. */
function seeded(rows: readonly MyReplyRow[]): QueryClient {
  restores.push(answerOneRead({ dismissed: rows }, { asOf: AS_OF }));
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  client.setQueryData(meInboxDismissedQuery().queryKey, {
    pageParams: [undefined],
    pages: [oneReadAnswer(rows, { asOf: AS_OF })],
  });
  return client;
}

/** How many times the harness has rendered, which is what a hook that sets state on every render
 *  runs away with. */
let renders = 0;

function Harness({ show = "dismissed" }: { show?: InboxShow }): ReactNode {
  renders += 1;
  const listRef = useRef<HTMLUListElement>(null);
  const threads = useInboxThreadRows({ asksOnly: false, listRef, show });
  return (
    <ul data-as-of={threads.asOf} ref={listRef}>
      {threads.rows.map((row) => (
        <li data-inbox-row={rowId(row)} key={rowId(row)} />
      ))}
    </ul>
  );
}

/** The harness under `client`, at the Inbox page's address `path`. */
function page(client: QueryClient, path = "/", show?: InboxShow): ReactNode {
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Harness show={show} />
      </MemoryRouter>
    </QueryClientProvider>
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
  const view = render(page(client));

  try {
    await waitFor(() => expect(rowIds()).toEqual(["reply:c1", "reply:c9"]));
    const settled = renders;

    // One render asked for, one render made: the rows the hold compares are the same array, so
    // its layout effect has nothing to adopt and asks for no render of its own.
    view.rerender(page(client));
    expect(renders).toBe(settled + 1);
    expect(rowIds()).toEqual(["reply:c1", "reply:c9"]);
  } finally {
    view.unmount();
  }
});

/** One answer of the one read holding `rows` as its Reply rows, in that order. */
function threadsAnswer(rows: readonly MyReplyRow[]) {
  return oneReadAnswer(rows, { asOf: AS_OF });
}

test("behind one=1 the Inbox's threads and its dismissed ones are the one read's", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  const listMyReplies = spyOn(api, "listMyReplies");
  restores.push(listMyReplies);
  const getMeInbox = spyOn(api, "getMeInbox").mockImplementation(async (query = {}) =>
    threadsAnswer(query.state === "dismissed" ? [replyRow("c7")] : [replyRow("c1"), replyRow("c9")])
  );
  restores.push(getMeInbox);

  const view = render(page(client, "/?one=1", "inbox"));
  try {
    await waitFor(() => expect(rowIds()).toEqual(["reply:c1", "reply:c9"]));
    expect(document.querySelector("ul")?.getAttribute("data-as-of")).toBe(AS_OF);
    view.rerender(page(client, "/?one=1", "dismissed"));
    await waitFor(() => expect(rowIds()).toEqual(["reply:c7"]));
    expect(getMeInbox.mock.calls.map(([query]) => query)).toEqual([
      { state: "inbox" },
      { limit: 200, state: "dismissed" },
    ]);
    expect(listMyReplies).not.toHaveBeenCalled();
  } finally {
    view.unmount();
  }
});
