/**
 * The Inbox's **Mark all read**: what one press sends, what it changes on the page before the
 * server answers, and what comes back when the server refuses it.
 *
 * One press is one `PATCH /api/v2/me/marks` (`marksRequest`) whose `filter` is the narrowing the
 * list is under and whose `set` is the read mark the list was read at (`as_of`), so the server
 * marks exactly the threads the reader is looking at: no row that arrived after that read is
 * swallowed, and no row the Filter hid is marked. The press is applied here first - the rows are
 * drawn as read and every unread count drops - because the reader pressed it, and is taken back
 * whole when Dispatch refuses it, which an old server with no such route is one way of doing. A
 * press the reader stopped is no refusal - it may have marked the rows - so the list is read again
 * instead.
 *
 * It takes no row out of the list. A thread stays in the Inbox, read or not, until it is
 * dismissed (`useInboxDismiss.ts`), as an email stays until it is archived.
 */

import { useQueryClient } from "@tanstack/react-query";

import { marksRequest, type WholeList } from "../../api/client";
import {
  inboxThreadsQuery,
  myRepliesCountQuery,
  myRepliesKey,
  userAgentStateQuery,
} from "../../api/queries";
import type { MarkUpdatesV2, MyReplyRow, MyThreadsV2 } from "../../api/types";
import { compareTimestamps } from "../../lib/timestamps";
import { SendStoppedError, stoppedWriteNote } from "../writes/keyed-write";
import { useKeyedWrite, useRefusalOfSubject } from "../writes/useKeyedWrite";
import type { InboxFilters } from "./InboxBar";
import { type InboxListRow, rowId } from "./list-row";
import { type InboxWriteScope, inboxWriteScope, inWriteScope, sameWriteScope } from "./write-scope";

/** What the page says when the server refused the press and nothing was marked. */
export const MARK_ALL_REFUSED_TEXT = "Nothing was marked read.";

/** What one press sends: the list's own narrowing as the filter, and the read mark it sets on
 *  every thread under it. */
export interface MarkAllReadBody {
  readonly filter: InboxWriteScope;
  readonly set: { readonly read_through: string };
}

/** What the page says of a press the reader stopped, which may have marked the rows: the list is
 *  read again from Dispatch, and the button is back while a thread there is still unread. */
export const MARK_ALL_STOPPED_TEXT = stoppedWriteNote("the read mark", "saved", {
  anew: "The Inbox is read again from Dispatch, and Mark all read sends a new mark for any thread still unread.",
});

/**
 * The body one press sends: the read mark the list was read at, and the list's own narrowing
 * (`inboxWriteScope`).
 */
export function markAllReadBody(filters: InboxFilters, readThrough: string): MarkAllReadBody {
  return { filter: inboxWriteScope(filters), set: { read_through: readThrough } };
}

/**
 * Whether one press marks `thread`: the server's own rule (`inboxReadScope`), read here so the
 * page draws as read exactly the rows the request marks. A thread written in since the read mark
 * stays unread - the reader has not seen that write - and the list's narrowing decides the rest
 * (`inWriteScope`).
 */
export function marksThread(thread: MyReplyRow, body: MarkAllReadBody): boolean {
  if (!thread.unread) return false;
  if (compareTimestamps(thread.reply.created_at, body.set.read_through) > 0) return false;
  return inWriteScope(thread, body.filter);
}

/** A thread row as a read mark leaves it: read, with nothing unread in it. A Reply row's mark
 *  moves to `readThrough`; an Agent row has none - its replies are read by id - so only its
 *  unread ids are emptied. The press reads every row it marks through this. */
export function readRow(thread: MyReplyRow, readThrough: string): MyReplyRow {
  if (thread.kind === "agent") {
    return { ...thread, unread: false, unread_count: 0, unread_reply_ids: [] };
  }
  return { ...thread, read_through: readThrough, unread: false, unread_count: 0 };
}

/** The list as the press leaves it: every row it marked drawn as read, and not one of them taken
 *  out of the list. A read thread is still in the Inbox - only a dismiss takes it out - so the
 *  rows stay where the reader can see what their press did. */
export function applyMarkAll<T extends Pick<MyThreadsV2, "items">>(
  answer: T,
  marked: ReadonlySet<string>,
  readThrough: string
): T {
  if (marked.size === 0) return answer;
  return {
    ...answer,
    items: (answer.items ?? []).map((row) =>
      marked.has(rowId({ kind: "thread", thread: row })) ? readRow(row, readThrough) : row
    ),
  };
}

/** What the press rolls back to: the two reads it wrote, as they stood before it. */
interface MarkAllRollback {
  readonly count: number | undefined;
  readonly threads: WholeList<"/api/v2/me/threads"> | undefined;
}

export interface MarkAllRead {
  /** Marks every unread thread the list is showing read. */
  readonly markAll: () => void;
  /** How the last press ended where it did not land, until the next press: `refused` by
   *  Dispatch, which takes the press back (`MARK_ALL_REFUSED_TEXT`), or `stopped` by the reader,
   *  which reads the list again (`MARK_ALL_STOPPED_TEXT`). The one thing this says to the reader,
   *  and only beside the list that press was made over: it goes once the reader narrows the list
   *  another way. */
  readonly failure: "refused" | "stopped" | undefined;
  /** Whether the list holds a row this would mark: the button shows only then. */
  readonly offered: boolean;
}

/**
 * The Inbox's **Mark all read**, for one list: its own filters, its own read mark, its own
 * optimistic apply and its own rollback. The drawer mounts its own Inbox, so it gets its own.
 *
 * The press writes two reads at once - the Inbox's own list and the unread count every badge
 * shares - so the rows are drawn as read and the Type filter's counts, the sidebar's Inbox count
 * and the compact header's Inbox link are all at zero before the server answers. The Agents
 * page's **New replies** badge is the server's own count of unread agent replies, so it drops
 * when the answer lands and the viewer's agent state is read again.
 *
 * The Dismissed view has no press of its own: it holds what the reader has already dealt with, so
 * the Inbox passes no read mark there and nothing is offered (`asOf`).
 */
export function useMarkAllRead({
  asOf,
  filters,
  rows,
}: {
  /** The `as_of` of the list read the page is showing, which is the read mark the press sends;
   *  undefined while the list has not been read, where the press is not offered. */
  readonly asOf: string | undefined;
  readonly filters: InboxFilters;
  /** The rows the list is showing, narrowed as the reader narrowed it. The button is offered
   *  while one of them is a thread this would mark, so a list with no thread row in it - an
   *  agent filter, `?section=needs-you`, a selection naming somebody else - offers none. */
  readonly rows: readonly InboxListRow[];
}): MarkAllRead {
  const queryClient = useQueryClient();
  const threadsKey = inboxThreadsQuery().queryKey;
  const countKey = myRepliesCountQuery().queryKey;
  const scope = inboxWriteScope(filters);
  const body = asOf === undefined ? undefined : markAllReadBody(filters, asOf);
  // What a press may have marked is read again from the server: the list, its counts, and the
  // per-session agent state the Agents page's **New replies** badge counts.
  const readAgain = () => {
    void queryClient.invalidateQueries({ queryKey: myRepliesKey });
    void queryClient.invalidateQueries({ queryKey: userAgentStateQuery().queryKey });
  };
  // The button is offered only while the list holds an unread Reply or Agent row, which is what
  // the spec asks for: a list showing none has nothing for one press to mark.
  const offered =
    body !== undefined &&
    rows.some((row) => row.kind === "thread" && marksThread(row.thread, body));

  const write = useKeyedWrite<MarkUpdatesV2, MarkAllReadBody, MarkAllRollback>({
    inline: true,
    label: () => "Inbox marked read",
    onError: (error, _input, rollback) => {
      // A press the reader stopped may have marked the rows, so they are not drawn unread again:
      // what Dispatch holds is read instead.
      if (error instanceof SendStoppedError) {
        readAgain();
        return;
      }
      if (rollback === undefined) return;
      queryClient.setQueryData(threadsKey, rollback.threads);
      queryClient.setQueryData(countKey, rollback.count);
    },
    onMutate: (input) => {
      const threads = queryClient.getQueryData(threadsKey);
      const count = queryClient.getQueryData<number>(countKey);
      const marked = new Set(
        (threads?.items ?? [])
          .filter((thread) => marksThread(thread, input))
          .map((thread) => rowId({ kind: "thread", thread }))
      );
      if (threads !== undefined) {
        queryClient.setQueryData(threadsKey, applyMarkAll(threads, marked, input.set.read_through));
      }
      // The count every badge shares drops by what this press marks, clamped at zero: an
      // unfiltered press marks every unread row and reads zero, and a filtered one takes away
      // only its own rows rather than counting the rest of the Inbox away.
      if (count !== undefined) {
        queryClient.setQueryData(countKey, Math.max(0, count - marked.size));
      }
      return { count, threads };
    },
    onSuccess: readAgain,
    request: (input) => marksRequest(input),
    target: () => "inbox:read",
  });
  // A press belongs to the list it was made over, which is its narrowing - the four fields its
  // filter carries (`sameWriteScope`): what it marked or did not mark is said beside that list
  // alone, and once the reader narrows the list another way the bar says nothing of a press they
  // did not make there (`useRefusalOfSubject`).
  const failed = useRefusalOfSubject(write, (input) => sameWriteScope(input.filter, scope));

  return {
    failure: failed ? (write.error instanceof SendStoppedError ? "stopped" : "refused") : undefined,
    markAll: () => {
      if (body !== undefined) write.mutate(body);
    },
    offered,
  };
}
