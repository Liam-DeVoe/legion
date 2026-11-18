/**
 * The optimistic writes over the one read behind the Inbox's opt-in (dispatch://INFRA-1152,
 * `one-read-cache.ts`): each write that moves one of the reader's own rows on the press moves it
 * in the one read's cache too, puts it back where the server refuses the write, and reads the one
 * read again once the server has decided. A hand-on and an assignee that make an ask wait on the
 * reader add it to the one read, which is where the opt-in draws the reader's own asks from, so the
 * row stays in their list while the write is out.
 */

import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import {
  answerInboxDismisses,
  answerInboxReads,
  answerIssuePatches,
  answerRoute,
} from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { inboxQuery, meInboxCountQuery, meInboxQuery, whoAmIQuery } from "../../api/queries";
import type { InboxRow, MeInboxEntry, MeInboxResponse, MyReplyRow } from "../../api/types";
import { useIssueAssignee } from "../issue/useIssueAssignee";
import { useIssuePriority } from "../issue/useIssuePriority";
import { replyRow } from "../threads/replies-fixture";
import { issueAsk } from "./inbox-fixture";
import { markAllReadBody, useMarkAllRead } from "./mark-all";
import { useInboxAsks } from "./one-read";
import { useOutstandingAnswerWrite } from "./outstanding-answers";
import { useAskHandOn } from "./useAskHandOn";
import { useAskSnooze, useAskSnoozeMany } from "./useAskSnooze";
import { dismissThreadRow, useInboxDismiss } from "./useInboxDismiss";

const AS_OF = "2026-10-12T02:00:00Z";

/** Every API mock a test installed, restored after it. */
const restores: { mockRestore: () => void }[] = [];

afterEach(() => {
  cleanup();
  for (const mock of restores.splice(0)) mock.mockRestore();
});

/** One answer of the one read, its entries carrying `asks` and `threads`. */
function oneRead(asks: readonly InboxRow[], threads: readonly MyReplyRow[] = []): MeInboxResponse {
  const entries: MeInboxEntry[] = [
    ...asks.map(
      (ask): MeInboxEntry => ({
        ask,
        moment: ask.created_at,
        node: { id: ask.id, kind: "ask" },
        relations: [],
      })
    ),
    ...threads.map(
      (thread): MeInboxEntry => ({
        moment: thread.reply.created_at,
        node: { id: thread.thread_id, kind: "comment" },
        relations: [],
        thread,
      })
    ),
  ];
  return {
    as_of: AS_OF,
    entries,
    horizon: 1,
    limit: 0,
    offset: 0,
    person: "alice",
    total: entries.length,
  };
}

/** A client signed in as alice holding the Inbox read `inbox`, and the one read's Inbox answer
 *  and unread count. */
function seeded(inbox: readonly InboxRow[], answer: MeInboxResponse, unread = 0): QueryClient {
  const client = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
    },
  });
  client.setQueryData(whoAmIQuery().queryKey, { event_head: "0", kind: "user", login: "alice" });
  client.setQueryData(inboxQuery().queryKey, [...inbox]);
  client.setQueryData(meInboxQuery().queryKey, answer);
  client.setQueryData(meInboxCountQuery().queryKey, unread);
  return client;
}

function wrapper(client: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <MemoryRouter initialEntries={["/?one=1"]}>
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
}

/** The one read's cached Inbox answer. */
function cached(client: QueryClient): MeInboxResponse | undefined {
  return client.getQueryData<MeInboxResponse>(meInboxQuery().queryKey);
}

function cachedAsk(client: QueryClient, id: string): InboxRow | undefined {
  return cached(client)?.entries?.find((entry) => entry.ask?.id === id)?.ask;
}

function cachedThread(client: QueryClient, id: string): MyReplyRow | undefined {
  return cached(client)?.entries?.find((entry) => entry.thread?.thread_id === id)?.thread;
}

/** A write's answer the test decides: `refuse` rejects it as the server refusing it. */
function deferred<T>() {
  const answer = Promise.withResolvers<T>();
  return {
    promise: answer.promise,
    refuse: () => answer.reject(new ApiError(409, { error: "refused" })),
    resolve: answer.resolve,
  };
}

/** Whether the one read's Inbox answer is marked to be read again. */
function invalidated(client: QueryClient): boolean {
  return client.getQueryState(meInboxQuery().queryKey)?.isInvalidated === true;
}

/** The one read's cached unread count. */
function unreadCount(client: QueryClient): number | undefined {
  return client.getQueryData<number>([...meInboxCountQuery().queryKey]);
}

/** The asks the Inbox lists behind the opt-in, beside a hand-on of `ask` (`useInboxAsks`). */
function HandOnList({
  ask,
  onHandOn,
}: {
  ask: InboxRow;
  onHandOn: (submit: (to: string) => void) => void;
}): ReactNode {
  const inbox = useQuery({ queryFn: async () => [ask], queryKey: inboxQuery().queryKey });
  const listed = useInboxAsks(inbox.data);
  const handOn = useAskHandOn(ask);
  onHandOn(handOn.submit);
  return (
    <ul>
      {(listed.asks ?? []).map((row) => (
        <li data-ask={row.id} data-waiting={row.waiting_on_person} key={row.id} />
      ))}
    </ul>
  );
}

test("a hand-on to the reader keeps the ask in their list behind the opt-in, and a refusal puts the one read back", async () => {
  // carol's ask, which alice hands to herself: the one read lists none of it yet.
  const ask = issueAsk({
    id: "ask-b",
    issue: { assignee: "carol", key: "CORE-1", title: "Fix the thing" },
  });
  const client = seeded([ask], oneRead([]));
  restores.push(spyOn(api, "getMeInbox").mockResolvedValue(oneRead([])));
  const answer = deferred<unknown>();
  answerRoute("PUT", "/api/v1/asks/*/asked-of").mockReturnValue(answer.promise);
  let submit: ((to: string) => void) | undefined;
  render(<HandOnList ask={ask} onHandOn={(handOn) => (submit = handOn)} />, {
    wrapper: wrapper(client),
  });
  await waitFor(() => expect(document.querySelector('[data-ask="ask-b"]')).not.toBeNull());

  act(() => submit?.("alice"));
  await waitFor(() => expect(cachedAsk(client, "ask-b")?.waiting_on_person).toBe("alice"));
  expect(document.querySelector('[data-ask="ask-b"]')?.getAttribute("data-waiting")).toBe("alice");

  await act(async () => answer.refuse());
  await waitFor(() => expect(cached(client)?.entries).toEqual([]));
  expect(document.querySelector('[data-ask="ask-b"]')?.getAttribute("data-waiting")).toBe("carol");
  await waitFor(() => expect(api.getMeInbox).toHaveBeenCalled());
});

test("taking an unassigned ask adds it to the one read, and a refusal takes it out again", async () => {
  // Nobody holds CORE-1, so the ask waits on the project's default until alice takes the issue.
  const ask = issueAsk({
    id: "ask-c",
    issue: { assignee: null, key: "CORE-1", title: "Fix the thing" },
  });
  const client = seeded([ask], oneRead([]));
  const answer = deferred<never>();
  answerIssuePatches().mockReturnValue(answer.promise);
  const hook = renderHook(() => useIssueAssignee("CORE-1"), { wrapper: wrapper(client) });

  act(() => hook.result.current.submit("alice"));
  await waitFor(() => expect(cachedAsk(client, "ask-c")?.waiting_on_person).toBe("alice"));
  expect(cachedAsk(client, "ask-c")?.issue?.assignee).toBe("alice");

  await act(async () => answer.refuse());
  await waitFor(() => expect(cached(client)?.entries).toEqual([]));
  expect(invalidated(client)).toBe(true);
});

test("a priority lands on the one read's ask, and a refusal takes it back", async () => {
  const ask = issueAsk({ id: "ask-a" });
  const client = seeded([ask], oneRead([ask]));
  const answer = deferred<never>();
  answerIssuePatches().mockReturnValue(answer.promise);
  const hook = renderHook(() => useIssuePriority("CORE-1"), { wrapper: wrapper(client) });

  act(() => hook.result.current.submit(1));
  await waitFor(() => expect(cachedAsk(client, "ask-a")?.priority).toBe(1));

  await act(async () => answer.refuse());
  await waitFor(() => expect(cachedAsk(client, "ask-a")?.priority).toBeNull());
  expect(invalidated(client)).toBe(true);
});

test("a snooze lands on the one read's ask, and a refusal brings it back", async () => {
  const ask = issueAsk({ id: "ask-a" });
  const client = seeded([ask], oneRead([ask]));
  const answer = deferred<never>();
  answerRoute("PUT", "/api/v1/me/asks/*/snooze").mockReturnValue(answer.promise);
  const hook = renderHook(() => useAskSnooze("ask-a"), { wrapper: wrapper(client) });

  act(() => hook.result.current.submit("2026-10-20T09:00:00Z"));
  await waitFor(() =>
    expect(cachedAsk(client, "ask-a")?.snoozed_until).toBe("2026-10-20T09:00:00Z")
  );

  await act(async () => answer.refuse());
  await waitFor(() => expect(cachedAsk(client, "ask-a")?.snoozed_until).toBeNull());
  expect(invalidated(client)).toBe(true);
});

test("a bulk snooze lands on the one read's asks, and a refusal brings back only the refused ask", async () => {
  const taken = issueAsk({ id: "ask-a" });
  const refused = issueAsk({ id: "ask-b", snoozed_until: "2026-10-15T09:00:00Z" });
  const client = seeded([taken, refused], oneRead([taken, refused]));
  const refusal = deferred<never>();
  answerRoute<(id: string) => Promise<unknown>>(
    "PUT",
    "/api/v1/me/asks/*/snooze"
  ).mockImplementation((id) => (id === "ask-b" ? refusal.promise : Promise.resolve(undefined)));
  const hook = renderHook(() => useAskSnoozeMany(), { wrapper: wrapper(client) });

  let settled: Promise<unknown> | undefined;
  act(() => {
    settled = hook.result.current.submit(["ask-a", "ask-b"], "2026-10-20T09:00:00Z");
  });
  await waitFor(() =>
    expect(cachedAsk(client, "ask-b")?.snoozed_until).toBe("2026-10-20T09:00:00Z")
  );
  expect(cachedAsk(client, "ask-a")?.snoozed_until).toBe("2026-10-20T09:00:00Z");

  await act(async () => {
    refusal.refuse();
    await settled;
  });
  // The refused ask goes back to the moment it held before, not to no snooze at all.
  expect(cachedAsk(client, "ask-b")?.snoozed_until).toBe("2026-10-15T09:00:00Z");
  expect(cachedAsk(client, "ask-a")?.snoozed_until).toBe("2026-10-20T09:00:00Z");
  expect(invalidated(client)).toBe(true);
});

test("an answer or a decline takes the ask out of the one read, and a refusal puts it back", async () => {
  const ask = issueAsk({ id: "ask-a" });
  const client = seeded([ask], oneRead([ask]));
  const answer = deferred<unknown>();
  const hook = renderHook(
    () =>
      useOutstandingAnswerWrite({
        askId: "ask-a",
        label: () => "Ask declined",
        write: () => answer.promise,
      }),
    { wrapper: wrapper(client) }
  );

  act(() => hook.result.current.mutate(undefined as never));
  await waitFor(() => expect(cachedAsk(client, "ask-a")).toBeUndefined());
  expect(cached(client)?.total).toBe(0);

  await act(async () => answer.refuse());
  await waitFor(() => expect(cachedAsk(client, "ask-a")?.id).toBe("ask-a"));
  expect(invalidated(client)).toBe(true);
});

test("Mark all read draws the one read's rows read and its count at zero, and a refusal brings both back", async () => {
  const thread = replyRow("c1");
  const client = seeded([], oneRead([], [thread]), 1);
  const answer = deferred<never>();
  answerInboxReads().mockReturnValue(answer.promise);
  const hook = renderHook(
    () => useMarkAllRead({ asOf: AS_OF, filters: {}, rows: [{ kind: "thread", thread }] }),
    { wrapper: wrapper(client) }
  );
  expect(markAllReadBody({}, AS_OF).read_through).toBe(AS_OF);

  act(() => hook.result.current.markAll());
  await waitFor(() => expect(cachedThread(client, "c1")?.unread).toBe(false));
  expect(unreadCount(client)).toBe(0);

  await act(async () => answer.refuse());
  await waitFor(() => expect(cachedThread(client, "c1")?.unread).toBe(true));
  expect(unreadCount(client)).toBe(1);
});

test("a dismissal takes the row out of the one read and lowers its count, and a refusal puts both back", async () => {
  const thread = replyRow("c1");
  const client = seeded([], oneRead([], [thread]), 1);
  const answer = deferred<never>();
  answerInboxDismisses().mockReturnValue(answer.promise);
  const settled: string[][] = [];
  const hook = renderHook(() => useInboxDismiss({ onSettled: (ids) => settled.push([...ids]) }), {
    wrapper: wrapper(client),
  });

  act(() => hook.result.current.submit({ rows: [dismissThreadRow(thread)] }));
  await waitFor(() => expect(cachedThread(client, "c1")).toBeUndefined());
  expect(unreadCount(client)).toBe(0);

  await act(async () => answer.refuse());
  await waitFor(() => expect(cachedThread(client, "c1")?.thread_id).toBe("c1"));
  expect(unreadCount(client)).toBe(1);
  expect(screen.queryByRole("alert")).toBeNull();
  expect(settled).toEqual([["reply:c1"]]);
});
