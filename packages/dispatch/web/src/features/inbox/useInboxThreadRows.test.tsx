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
 * The Inbox draws these rows from the one read alone.
 */

import { afterEach, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, waitFor } from "@testing-library/react";
import { type ReactNode, useRef } from "react";

import { meInboxDismissedQuery, whoAmIQuery } from "../../api/queries";
import type { MyReplyRow } from "../../api/types";
import type { InboxShow } from "../refs/routes";
import { replyRow } from "../threads/replies-fixture";
import { answerOneRead, ONE_READ_AS_OF, oneReadDismissedPages } from "./inbox-fixture";
import { rowId } from "./list-row";
import { useInboxThreadRows } from "./useInboxThreadRows";

/** Every API mock a test installed, restored after it. */
const restores: { mockRestore: () => void }[] = [];

afterEach(() => {
  cleanup();
  for (const mock of restores.splice(0)) mock.mockRestore();
});

/** A client holding the sign-in read and one settled page of the one read's Dismissed view, so
 *  the hook renders over pages it never has to fetch. */
function seeded(rows: readonly MyReplyRow[]): QueryClient {
  restores.push(answerOneRead({ dismissed: rows }));
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  client.setQueryData(meInboxDismissedQuery().queryKey, oneReadDismissedPages(rows));
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

/** The harness under `client`, showing `show`. */
function page(client: QueryClient, show?: InboxShow): ReactNode {
  return (
    <QueryClientProvider client={client}>
      <Harness show={show} />
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

test("the Inbox's threads and its dismissed ones are the one read's", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  const getMeInbox = answerOneRead({
    dismissed: [replyRow("c7")],
    threads: [replyRow("c1"), replyRow("c9")],
  });
  restores.push(getMeInbox);

  const view = render(page(client, "inbox"));
  try {
    await waitFor(() => expect(rowIds()).toEqual(["reply:c1", "reply:c9"]));
    expect(document.querySelector("ul")?.getAttribute("data-as-of")).toBe(ONE_READ_AS_OF);
    view.rerender(page(client, "dismissed"));
    await waitFor(() => expect(rowIds()).toEqual(["reply:c7"]));
    expect(getMeInbox.mock.calls.map(([query]) => query)).toEqual([
      { state: "inbox" },
      { limit: 200, state: "dismissed" },
    ]);
  } finally {
    view.unmount();
  }
});
