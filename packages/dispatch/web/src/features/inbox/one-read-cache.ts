/**
 * The optimistic writes over the one read's cache, `meInboxKey` (dispatch://INFRA-1152). The
 * reader's own asks, their Reply and Agent rows and their unread count are drawn from it, so every
 * write that moves one of them on the press - a hand-on, an assignee, a priority, a snooze, an
 * answer or a decline, Mark all read, a dismissal - moves it here as well as in the open asks'
 * reads it moves (`open-asks-cache.ts`), puts it back where the server refuses the write, and
 * reads the one read again once the server has decided it.
 *
 * Each edit leaves alone what it does not name: the count where it is given no count edit, and an
 * answer none of whose entries it changes. Nothing here is a hook, so the writes call it from
 * their own callbacks.
 */

import type { QueryClient, QueryKey } from "@tanstack/react-query";

import {
  meInboxCountQuery,
  meInboxDismissedQuery,
  meInboxKey,
  meInboxQuery,
} from "../../api/queries";
import { replaceQueryData } from "../../api/query-client";
import type { MeInboxEntry, MeInboxResponse, MyReplyRow, OpenAskRow } from "../../api/types";

/** Every cached read of the one read as it stood before a write moved it, for the rollback. */
export type MeInboxSnapshot = readonly (readonly [QueryKey, unknown])[];

/** How a write leaves one entry of an answer: the entry it leaves, or undefined where it takes the
 *  entry out. The entry itself where it changes nothing. */
export type MeInboxEntryEdit = (entry: MeInboxEntry) => MeInboxEntry | undefined;

/** `answer` as `edit` leaves its entries, each entry taken out lowering its `total`; the answer
 *  itself where no entry changed. */
function editAnswer(answer: MeInboxResponse, edit: MeInboxEntryEdit): MeInboxResponse {
  const entries = answer.entries ?? [];
  let changed = false;
  const kept: MeInboxEntry[] = [];
  for (const entry of entries) {
    const next = edit(entry);
    if (next !== entry) changed = true;
    if (next !== undefined) kept.push(next);
  }
  if (!changed) return answer;
  return {
    ...answer,
    entries: kept,
    total: Math.max(0, answer.total - (entries.length - kept.length)),
  };
}

/** One cached read of the one read as a write leaves it: an answer's entries or the pages of the
 *  paged read (`meInboxDismissedQuery`) as `edit` leaves them, a count as `count` leaves it, and
 *  anything else - no data, a count with no count edit - as it is. */
function editRead(
  data: unknown,
  edit: MeInboxEntryEdit,
  count: ((count: number) => number) | undefined
): unknown {
  if (typeof data === "number") return count === undefined ? data : count(data);
  const read = data as { readonly entries?: unknown; readonly pages?: unknown } | undefined;
  if (Array.isArray(read?.pages)) {
    const paged = data as { readonly pages: readonly MeInboxResponse[] };
    const pages = paged.pages.map((page) => editAnswer(page, edit));
    return pages.every((page, index) => page === paged.pages[index]) ? data : { ...paged, pages };
  }
  return Array.isArray(read?.entries) ? editAnswer(data as MeInboxResponse, edit) : data;
}

/** What every cached read of the one read holds now, which a write keeps before it edits them. */
export function meInboxSnapshot(queryClient: QueryClient): MeInboxSnapshot {
  return queryClient.getQueriesData({ queryKey: meInboxKey });
}

/** Cancels every read of the one read already out, so none lands over the edit a write is about
 *  to make, as each write cancels the other reads it edits. */
export function cancelMeInbox(queryClient: QueryClient): Promise<void> {
  return queryClient.cancelQueries({ queryKey: meInboxKey });
}

/** Puts every cached read of the one read back as `snapshot` holds it. */
export function restoreMeInbox(
  queryClient: QueryClient,
  snapshot: MeInboxSnapshot | undefined
): void {
  for (const [key, data] of snapshot ?? []) queryClient.setQueryData(key, data);
}

/** Reads the one read again, which a write asks for once the server has decided it; a write
 *  that stays pending until its reads land awaits the answer. */
export function invalidateMeInbox(queryClient: QueryClient): Promise<void> {
  return queryClient.invalidateQueries({ queryKey: meInboxKey });
}

/** Resolves once no read of the one read is out, answered or refused, and sends none of its own:
 *  what a held Send waits for once its write has landed, whose landing reads the Inbox again
 *  itself (an answer's `onSettled`, a send's `refreshAfter`). The reads out are taken in the next
 *  task, not now: a keyed write tells its landing from `onSuccess`, which a surface can draw at
 *  once, and TanStack runs its `onSettled`, which starts the read, in the promise reactions after
 *  it, all of them before the next task. It looks again after each read comes back: where a read
 *  was already out at the landing, Dispatch's client queues the landing's read as a follow-up that
 *  starts only once that read settles (`DispatchQueryClient`), so the first read back can still
 *  hold the row. `refetchQueries` would not do: Dispatch's client never joins a read already out,
 *  and fetches once more when it settles, so the Send would wait through a second read it sent
 *  itself. The holds' write deadline bounds the wait. */
export async function meInboxReadsBack(queryClient: QueryClient): Promise<void> {
  for (;;) {
    const nextTask = Promise.withResolvers<void>();
    setTimeout(nextTask.resolve, 0);
    await nextTask.promise;
    const out = queryClient
      .getQueryCache()
      .findAll({ fetchStatus: "fetching", queryKey: meInboxKey });
    if (out.length === 0) return;
    await Promise.all(
      out.map((query) =>
        query.promise?.then(
          () => undefined,
          () => undefined
        )
      )
    );
  }
}

/** Edits every cached read of the one read: each answer's entries by `edit`, and the unread count
 *  by `count` where one is given. */
export function patchMeInbox(
  queryClient: QueryClient,
  edit: MeInboxEntryEdit,
  count?: (count: number) => number
): void {
  queryClient.setQueriesData({ queryKey: meInboxKey }, (data: unknown) =>
    editRead(data, edit, count)
  );
}

/** The entry of the one read's Inbox answer that lists `ask`, as Dispatch lists an open ask: what
 *  a write that makes an ask wait on the reader adds to that answer before it reads again. */
export function meInboxAskEntry(ask: OpenAskRow): MeInboxEntry {
  return { ask, moment: ask.created_at, node: { id: ask.id, kind: "ask" }, relations: [] };
}

/**
 * Edits the ask rows of every cached read of the one read: `edit` leaves each ask, or takes its
 * entry out with undefined. Each ask of `joined` - a row the write makes wait on the reader, whom
 * the one read will list it for once it reads again - the reader's own Inbox answer gains an entry
 * for where it holds none, so the row joins their list on the press as it does in the open asks'
 * reads, and the list keeps it (`useInboxAsks`, which draws the reader's own asks from the one
 * read alone).
 */
export function patchMeInboxAsks(
  queryClient: QueryClient,
  edit: (ask: OpenAskRow) => OpenAskRow | undefined,
  joined: readonly OpenAskRow[] = []
): void {
  patchMeInbox(queryClient, (entry) => {
    if (entry.ask === undefined || entry.ask === null) return entry;
    const ask = edit(entry.ask);
    if (ask === undefined) return undefined;
    return ask === entry.ask ? entry : { ...entry, ask };
  });
  if (joined.length === 0) return;
  queryClient.setQueryData<MeInboxResponse>(meInboxQuery().queryKey, (answer) => {
    if (answer === undefined) return answer;
    const listed = new Set(
      (answer.entries ?? []).flatMap((entry) => (entry.ask ? [entry.ask.id] : []))
    );
    const added = joined.filter((ask) => !listed.has(ask.id)).map(meInboxAskEntry);
    if (added.length === 0) return answer;
    return {
      ...answer,
      entries: [...(answer.entries ?? []), ...added],
      total: answer.total + added.length,
    };
  });
}

/**
 * Edits the Reply and Agent rows of every cached read of the one read, and the unread count by
 * `count` where one is given: `edit` leaves each row, or takes its entry out with undefined. Each
 * read is written over a read of it still out (`replaceQueryData`): that read was asked before the
 * edit, and its answer would put the rows back as they stood. Mark all read is the one caller.
 */
export function patchMeInboxThreads(
  queryClient: QueryClient,
  edit: (thread: MyReplyRow) => MyReplyRow | undefined,
  count?: (count: number) => number
): void {
  const threadEdit: MeInboxEntryEdit = (entry) => {
    if (entry.thread !== undefined && entry.thread !== null) {
      const thread = edit(entry.thread);
      if (thread === undefined) return undefined;
      return thread === entry.thread ? entry : { ...entry, thread };
    }
    if (entry.agent !== undefined && entry.agent !== null) {
      const agent = edit(entry.agent as MyReplyRow);
      if (agent === undefined) return undefined;
      return agent === entry.agent ? entry : { ...entry, agent: agent as typeof entry.agent };
    }
    return entry;
  };
  for (const [queryKey] of meInboxSnapshot(queryClient)) {
    replaceQueryData(queryClient, queryKey, (data: unknown) => editRead(data, threadEdit, count));
  }
}

/** The Reply and Agent rows the reader's own Inbox answer of the one read holds, or of its
 *  `dismissed` pages where `dismissed` is set: what a write reads to know which rows it moves. */
export function meInboxThreadRows(
  queryClient: QueryClient,
  dismissed = false
): readonly MyReplyRow[] {
  const rowsOf = (answer: MeInboxResponse | undefined): MyReplyRow[] =>
    (answer?.entries ?? []).flatMap((entry) => {
      if (entry.thread !== undefined && entry.thread !== null) return [entry.thread];
      if (entry.agent !== undefined && entry.agent !== null) return [entry.agent as MyReplyRow];
      return [];
    });
  if (!dismissed) return rowsOf(queryClient.getQueryData<MeInboxResponse>(meInboxQuery().queryKey));
  const paged = queryClient.getQueryData<{ readonly pages: readonly MeInboxResponse[] }>(
    meInboxDismissedQuery().queryKey
  );
  return (paged?.pages ?? []).flatMap(rowsOf);
}

/**
 * Takes the Reply and Agent rows `drops` names out of one view of the one read - the reader's own
 * Inbox answer, or the pages of what they dismissed where `dismissed` is set - and answers the rows
 * it took. A row taken out of the Inbox answer that is unread lowers the unread count by one, as
 * the dismissal that takes it marks it read; a row taken out of the dismissed pages was read when
 * it was dismissed, and counts for nothing.
 */
export function dropMeInboxThreads(
  queryClient: QueryClient,
  dismissed: boolean,
  drops: (thread: MyReplyRow) => boolean
): readonly MyReplyRow[] {
  const dropped: MyReplyRow[] = [];
  const edit: MeInboxEntryEdit = (entry) => {
    const thread = entry.thread ?? (entry.agent as MyReplyRow | undefined | null);
    if (thread === undefined || thread === null || !drops(thread)) return entry;
    dropped.push(thread);
    return undefined;
  };
  const key = dismissed ? meInboxDismissedQuery().queryKey : meInboxQuery().queryKey;
  queryClient.setQueryData(key, (data: unknown) => editRead(data, edit, undefined) as never);
  const unread = dropped.filter((thread) => thread.unread).length;
  if (!dismissed && unread > 0) {
    queryClient.setQueryData<number>(meInboxCountQuery().queryKey, (count) =>
      count === undefined ? count : Math.max(0, count - unread)
    );
  }
  return dropped;
}
