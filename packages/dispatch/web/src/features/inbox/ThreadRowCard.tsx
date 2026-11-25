import { inboxThreadRow } from "@legion/contracts";
import { useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { api } from "../../api/client";
import { myRepliesKey } from "../../api/queries";
import type { MyReplyRow, UserAgentState } from "../../api/types";
import { Button, buttonClassName } from "../../components/Button";
import { QueryError } from "../../components/QueryError";
import { borderDefault, card, textMutedOnCanvas } from "../../theme/classes";
import { storeAgentState } from "../agents/unread";
import { Timestamp } from "../refs/Timestamp";
import { stoppedNote, useKeyedWrite } from "../writes/useKeyedWrite";
import { ThreadOwnerLine } from "./AskOwnerLink";
import { DismissControl } from "./DismissControl";
import { rowPriority, rowType } from "./list-row";
import { RowKindIcon, RowPriorityTag } from "./RowMarks";
import { ThreadBody } from "./ThreadBody";
import { ThreadTitle } from "./ThreadTitle";

/**
 * A Reply or an Agent row as a card: the layout below the reading pane's width, and the one the
 * drawer shows at every width.
 *
 * It draws the head on the canvas above the card, as a question row draws its own - the row's
 * mark, the kind icon, the title and the time of the last write, then the priority tag and the
 * whole issue key and title - and in the card the thread itself, exactly as the reading pane
 * draws it and collapsed the same way (`ThreadBody`), and the three controls a thread row offers:
 * **Open**, **Mark read**, and the **Dismiss** that takes the row out of the Inbox - **Move to
 * Inbox** in the Dismissed view - which the list it is drawn in provides (`DismissControl`). A
 * thread anchored in a document shows the text it is anchored to rather than the document: a card
 * is no place to open one.
 */
export function ThreadRowCard({
  check,
  thread,
}: {
  /** The row's mark checkbox, which the list owns: the first thing on the head's line. */
  check?: ReactNode;
  thread: MyReplyRow;
}): ReactNode {
  const queryClient = useQueryClient();
  // The press carries the row as it stood when Mark read was pressed: its newest reply and its
  // unread ids move as replies arrive, and the Retry of a press the reader stopped marks what
  // that press marked.
  const markRead = useKeyedWrite<UserAgentState | undefined, MyReplyRow>({
    label: () => "Thread marked read",
    // Either mark is a `PUT`.
    method: "PUT",
    // An Agent row is read by id, as every read of one is: its session's own read mark covers
    // replies in the live conversation and in its other threads, which this press has not
    // shown. Every other row carries a thread mark, which this moves to its newest write.
    write: async (pressed, bounds) => {
      const read = inboxThreadRow(pressed);
      if (read.kind === "agent") {
        return api.putAgentState(read.session.id, { read_replies: read.unread_reply_ids }, bounds);
      }
      await api.markThreadRead(pressed.thread_id, pressed.reply.created_at, bounds);
      return undefined;
    },
    onSuccess: (state, pressed) => {
      const read = inboxThreadRow(pressed);
      if (state !== null && state !== undefined && read.kind === "agent") {
        storeAgentState(queryClient, read.session.id, state);
      }
      return queryClient.invalidateQueries({ queryKey: myRepliesKey });
    },
  });
  const row = { kind: "thread", thread } as const;
  return (
    <div data-inbox-thread-card="">
      <div className="flex min-w-0 items-center gap-2">
        {check}
        <RowKindIcon type={rowType(row)} />
        <ThreadTitle className="min-w-0 grow truncate text-sm" thread={thread} />
        <span className={`shrink-0 text-xs ${textMutedOnCanvas}`}>
          <Timestamp at={thread.reply.created_at} />
        </span>
      </div>
      {/* Under the title, as the short row's second line is: checkbox, gap, icon, gap. */}
      <div
        className={`mt-1 mb-2 flex min-w-0 flex-wrap items-baseline gap-2 ${check === undefined ? "" : "pl-13"}`}
      >
        <RowPriorityTag priority={rowPriority(row)} />
        <ThreadOwnerLine className={`truncate text-sm ${textMutedOnCanvas}`} thread={thread} />
      </div>
      <div className={`rounded-lg border px-3 pt-1 pb-3 ${card} ${borderDefault}`}>
        <ThreadBody carded quote thread={thread} />
        {/* Ghost presses, pulled left by their own inset so their words start at the card's
            text edge. */}
        <div className="mt-2 -ml-2.5 flex flex-wrap items-center gap-1">
          {/* The server's `ref` names the thread's root, never the reply: an anchored root
            redirects to its document, and a comment's thread opens as it lands. */}
          <Link
            className={buttonClassName({ size: "sm", variant: "ghost" })}
            data-inbox-owner=""
            to={thread.ref}
          >
            Open
          </Link>
          <Button
            disabled={markRead.isPending}
            onClick={() => markRead.mutate(thread)}
            size="sm"
            variant="ghost"
          >
            {markRead.isPending ? "Marking read…" : "Mark read"}
          </Button>
          <DismissControl shown thread={thread} />
        </div>
        {markRead.isError ? (
          <div className="mt-2">
            {/* A mark the reader stopped may have been saved, and Retry makes it again (`retry`):
                the error's own words are a composer's, of a message. */}
            <QueryError
              message={
                stoppedNote(markRead, "this thread's read mark", "saved") ?? markRead.error.message
              }
              onRetry={markRead.retry}
            />
          </div>
        ) : null}
      </div>
    </div>
  );
}
