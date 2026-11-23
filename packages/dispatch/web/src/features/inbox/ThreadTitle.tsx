import type { MouseEvent, ReactNode } from "react";

import type { MyReplyRow } from "../../api/types";
import { textMutedOnCanvas, textPrimaryOnCanvas, unreadDotBg } from "../../theme/classes";
import { useAgentText } from "../refs/AgentName";
import { EditedMark } from "../refs/PostBody";
import { deletedLineText, postDeletion } from "../refs/post-marks";
import { questionLine } from "./question-line";

/**
 * What a thread row is about as plain text, for every name a row's controls carry - its
 * checkbox's, its Dismiss's, its title's hover text: the first line of the thread's subject, or,
 * for a root its author deleted, whose subject the delete cleared, the deleted line's words
 * (`deletedLineText`). One reading, so every name a row carries says the same thing.
 */
export function useThreadLine(thread: MyReplyRow): string {
  const text = useAgentText();
  const deleted = postDeletion(thread);
  return deleted === null ? questionLine(thread.subject) : deletedLineText(deleted, text);
}

/**
 * What a thread row is about, in one line, bold with a dot while the thread holds a write the
 * reader has not read. A root its author deleted reads as the deleted line, and a root its
 * author edited carries ` · edited` after the line, outside the truncation that would cut it.
 * The deleted line here is its words alone (`deletedLineText`), in the deleted line's own italic,
 * with no time: the row already shows its own age, and that age is the one time a row holds, the
 * part of a short row that is neither the owner link nor a control, where the pointer selects
 * the row (`selectRow` in the browser suite).
 *
 * The short row draws it as the link `o` follows (`data-inbox-owner`); the drawer's card draws it
 * as plain text, beside its **Open**. Each host sizes its own line, as the three-site owner line
 * does (`ThreadOwnerLine` in `AskOwnerLink.tsx`); what unread looks like is this one place.
 */
export function ThreadTitle({
  className,
  href,
  onOpen,
  thread,
}: {
  /** The classes the host's line gives the title; the unread weight is added to them. */
  className: string;
  /** Where the title links, for the host that draws it as a link; plain text without one. */
  href?: string;
  /** What a plain click on the link does instead of following it: below 1280 px the Inbox opens
   *  the row in place. A click with a modifier or another button still follows the link. */
  onOpen?: () => void;
  thread: MyReplyRow;
}): ReactNode {
  const openInPlace =
    onOpen === undefined
      ? undefined
      : (event: MouseEvent<HTMLAnchorElement>) => {
          if (
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey
          ) {
            return;
          }
          event.preventDefault();
          onOpen();
        };
  const title = useThreadLine(thread);
  const deleted = postDeletion(thread);
  const words =
    deleted === null ? (
      title
    ) : (
      <span className="italic" data-post-deleted="">
        {title}
      </span>
    );
  const text = `${className} ${
    thread.unread ? `font-semibold ${textPrimaryOnCanvas}` : textMutedOnCanvas
  }`;
  return (
    <>
      {href === undefined ? (
        <span className={text} title={title}>
          {words}
        </span>
      ) : (
        <a className={text} data-inbox-owner="" href={href} onClick={openInPlace} title={title}>
          {words}
        </a>
      )}
      <EditedMark
        at={thread.edited_at}
        className={`shrink-0 text-xs ${textMutedOnCanvas}`}
        deleted={deleted}
      />
      {thread.unread ? (
        <span
          aria-label="Unread"
          className={`size-2 shrink-0 rounded-full ${unreadDotBg}`}
          role="img"
        />
      ) : null}
    </>
  );
}
