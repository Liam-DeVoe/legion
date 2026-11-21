import { type InfiniteData, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useRef } from "react";
import { Link } from "react-router-dom";

import type { DispatchEvent, DisplayTree } from "../../api/types";
import { PinButton } from "../../components/PinButton";
import { pageInkText, pageMutedText, pageQuietText } from "../../theme/classes";
import { SentenceParts } from "../activity/SentenceParts";
import { eventDescription } from "../issue/event-description";
import { writePin } from "../issue/pin-write";
import {
  type ConversationPinKind,
  conversationItemId,
  eventItemId,
  pinnedConversations,
  pinnedEventIds,
} from "../issue/pins";
import { EVENT_FRESH } from "../refs/reference-target";
import { buildIssuePath } from "../refs/routes";
import { textSourceKind, textSourceOf } from "../refs/text-references";
import { lineOrKindWord, useNodeLine } from "./node-line";
import { authorText, pageChrome } from "./page-structure";
import { CONTROL_LABELS } from "./page-words";
import { ROW_LINK } from "./row-references";
import { issueAsksQuery, issueCommentsQuery, issueMessagesQuery } from "./useIssueTree";

/**
 * The reader's pins, as links to what they pinned: each pinned ask, comment and message by its
 * first line, a link to its own address, which focuses its row in the outline, with its Unpin
 * beside it. A pin of what the outline draws no row for - a thread agents wrote among
 * themselves - links to its address too, which opens the history at it, and an old pin of a
 * handoff opens the history. Each line of the history the reader pinned
 * (`IssueHistory.tsx`'s Pin, an `event:<id>` pin) is listed under them: who did it and what the
 * line says, with a link that opens the history and its Unpin. The pin of a row itself is the
 * focused row's, in its facts (`FocusedNodeFacts.tsx`).
 *
 * The words come from the reads the outline and the history are drawn from, so the list costs no
 * request of its own: each read here is the outline's, by its definition (`useIssueTree.ts`) and
 * with its options (`EVENT_FRESH`: kept fresh by events, never read again by its age, and by a
 * window focus only while its last read failed), so it shares the outline's answer and is read
 * again only when the outline's is. A record's line is its one line, as the outline draws a
 * node's (`PinLine`), drawn whole, wrapping rather than cut. Where a read has not answered, has
 * failed, or holds no words for a pin - a handoff, an event older than the history has read -
 * the pin is drawn by the page's word for its kind and never by its id. Until the reader's own
 * state has answered, which pins there are is not known: the section draws its heading alone,
 * and says None only once that state holds no pin.
 */
export function IssuePins({
  dismissed,
  issueKey,
}: {
  /** The reader's pins and dismissals of this issue (`UserIssueState.dismissed`), or
   *  `undefined` while the reader's state has not answered. */
  dismissed: readonly string[] | undefined;
  issueKey: string;
}): ReactNode {
  const queryClient = useQueryClient();
  const pins = pinnedConversations([...(dismissed ?? [])]);
  const eventPins = pinnedEventIds([...(dismissed ?? [])]);
  const reading = pins.length > 0;
  const asks = useQuery({ ...issueAsksQuery(issueKey), ...EVENT_FRESH, enabled: reading });
  const comments = useQuery({ ...issueCommentsQuery(issueKey), ...EVENT_FRESH, enabled: reading });
  const messages = useQuery({ ...issueMessagesQuery(issueKey), ...EVENT_FRESH, enabled: reading });
  // The history's own log, under its key (`IssueHistory.tsx`), which the history under this list
  // reads: watched here and never asked for, so a pinned line is drawn from the pages it holds.
  const log = useQuery<InfiniteData<DispatchEvent[]>>({
    enabled: false,
    queryKey: ["events", issueKey],
  });
  const events = eventPins.length === 0 ? [] : (log.data?.pages.flat() ?? []);
  /** The record a pin names, as the read that holds it answered it: its text as stored and the
   *  server's reading of it. None where that read has not answered or holds no such record, and
   *  for a handoff, whose words no read here holds. */
  const pinRecord = (kind: ConversationPinKind, id: string): PinnedRecord | undefined => {
    if (kind === "ask") {
      const ask = asks.data?.find((one) => one.id === id);
      return ask === undefined ? undefined : { text: ask.question, tree: ask.question_tree };
    }
    if (kind === "comment") {
      const comment = comments.data?.find((one) => one.id === id);
      return comment === undefined ? undefined : { text: comment.body, tree: comment.body_tree };
    }
    if (kind !== "message") return undefined;
    const message = (messages.data ?? [])
      .flatMap((thread) => [thread.message, ...(thread.replies ?? [])])
      .find((one) => one.id === id);
    return message === undefined ? undefined : { text: message.body, tree: message.body_tree };
  };
  const list = useRef<HTMLUListElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  // An Unpin takes its pin's row away, and the focus with it: the focus goes on to the next
  // pin's link, else the one before, else the section's heading, rather than falling to the
  // page. Every row holds its pin's link as a child of its own; an event pin's actor is a link
  // further in, inside its line.
  const unpin = (id: string) => {
    const rows = [...(list.current?.children ?? [])];
    const at = rows.findIndex((row) => row instanceof HTMLElement && row.dataset.pin === id);
    const neighbour = rows[at + 1] ?? (at > 0 ? rows[at - 1] : undefined);
    const next = [...(neighbour?.children ?? [])].find(
      (child) => child instanceof HTMLAnchorElement
    );
    (next ?? heading.current)?.focus();
    writePin(queryClient, issueKey, { id, op: "unpin" });
  };
  const link = `dispatch-inline-link ${ROW_LINK} ${pageInkText}`;
  return (
    <section aria-label={CONTROL_LABELS.pinned} data-testid="issue-pins">
      <h2
        className={`text-sm font-semibold outline-none ${pageInkText}`}
        ref={heading}
        tabIndex={-1}
        {...pageChrome}
      >
        {CONTROL_LABELS.pinned}
      </h2>
      {dismissed === undefined ? null : pins.length + eventPins.length === 0 ? (
        <p className={`text-xs ${pageQuietText}`} {...pageChrome}>
          {CONTROL_LABELS.none}
        </p>
      ) : (
        <ul className="mt-1 flex min-w-0 flex-col gap-0.5" ref={list}>
          {pins.map((pin) => {
            const id = conversationItemId(pin.kind, pin.id);
            return (
              <li className="flex min-w-0 items-center gap-2 text-sm" data-pin={id} key={id}>
                <Link
                  className={`min-w-0 flex-1 [overflow-wrap:anywhere] ${link}`}
                  to={
                    pin.kind === "handoff"
                      ? buildIssuePath({ key: issueKey, kind: "activity" })
                      : buildIssuePath({ id: pin.id, key: issueKey, kind: pin.kind })
                  }
                >
                  <PinLine
                    id={pin.id}
                    issueKey={issueKey}
                    kind={pin.kind}
                    record={pinRecord(pin.kind, pin.id)}
                  />
                </Link>
                <PinButton label={CONTROL_LABELS.unpin} onClick={() => unpin(id)} pinned quiet />
              </li>
            );
          })}
          {eventPins.map((eventId) => {
            const id = eventItemId({ id: Number(eventId) });
            const event = events.find((one) => String(one.id) === eventId);
            return (
              <li className="flex min-w-0 items-center gap-2 text-sm" data-pin={id} key={id}>
                {event === undefined ? null : (
                  <span
                    className={`min-w-0 flex-1 [overflow-wrap:anywhere] ${pageMutedText}`}
                    {...authorText}
                  >
                    <SentenceParts
                      parts={[{ actor: event.actor }, " ", ...eventDescription(event)]}
                    />
                  </span>
                )}
                <Link
                  className={`${event === undefined ? "min-w-0 flex-1" : "shrink-0"} ${link}`}
                  to={buildIssuePath({ key: issueKey, kind: "activity" })}
                  {...pageChrome}
                >
                  {CONTROL_LABELS.history}
                </Link>
                <PinButton label={CONTROL_LABELS.unpin} onClick={() => unpin(id)} pinned quiet />
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/** A pinned record's text as stored, and the server's reading of it where the read sent one. */
interface PinnedRecord {
  readonly text: string;
  readonly tree: DisplayTree | undefined;
}

/** A pin's line: its record's one line, as the outline draws a node's (`node-line.tsx`'s
 *  `useNodeLine`) - its first line, projected from the server's reading of its text where the
 *  read sent one and from its text as stored otherwise, each reference in it read as its
 *  target's name, and cut by R1's first-line rule - with the record as the source its references
 *  are read against. Nothing of it until the projection has answered. The page's word for the
 *  pin's kind where the line holds no words, and at once where no read holds the record - its
 *  read has not answered or has failed, or it is a handoff, whose words no read here holds - since
 *  there are then no words to wait on. */
function PinLine({
  id,
  issueKey,
  kind,
  record,
}: {
  id: string;
  issueKey: string;
  kind: ConversationPinKind;
  record: PinnedRecord | undefined;
}): ReactNode {
  const { line, reads } = useNodeLine(
    {
      id,
      source: textSourceOf(textSourceKind(kind), id, issueKey),
      text: record?.text ?? "",
      tree: record?.tree,
      type: kind,
    },
    { surface: "issue-pin" }
  );
  const drawn = record === undefined ? "" : line;
  return (
    <>
      {reads}
      <span {...(drawn === "" ? pageChrome : authorText)}>{lineOrKindWord(drawn, kind)}</span>
    </>
  );
}
