import { replaceEqualDeep, useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";

import { api } from "../../api/client";
import { type EventPages, mergeEventPages } from "../../api/event-feed";
import type { UserState } from "../../api/types";
import { Button } from "../../components/Button";
import { PinButton } from "../../components/PinButton";
import {
  dangerText,
  linkText,
  newDividerLine,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { writePin } from "../issue/pin-write";
import { eventItemId, isPinnedEvent, stateForIssue } from "../issue/pins";
import { AgentName, useAgentText } from "../refs/AgentName";
import { Timestamp } from "../refs/Timestamp";
import { ViewportAnchor } from "../shell/ViewportAnchor";
import { ActivityLine } from "./ActivityLine";
import { buildActivityItems, dateKey } from "./activity-model";

interface ActivityTabProps {
  issueKey: string;
  state: UserState | undefined;
}

/**
 * The issue's whole event history: every event one line, newest first, broken by day and by the
 * reader's unread watermark. It writes no read mark - the Conversation is `last_read_seq`'s only
 * writer, so the watermark this draws has exactly one author - and it offers no composer and no
 * filters: this is the record, not a place to write.
 */
export function ActivityTab({ issueKey, state }: ActivityTabProps): ReactNode {
  const queryClient = useQueryClient();
  const log = useInfiniteQuery({
    initialPageParam: null as number | null,
    queryKey: ["events", issueKey],
    queryFn: ({ pageParam }) =>
      api.getIssueEvents(
        issueKey,
        pageParam === null ? { limit: 200, order: "desc" } : { before: pageParam, limit: 200 }
      ),
    // A page can hold more than the server's 200 once streamed events sit above a full response
    // (mergeEventPages, prependEventToLog); it is still a full page with older events beneath it.
    getNextPageParam: (page) => (page.length >= 200 ? page.at(-1)?.seq : undefined),
    // The feed (api/event-feed.ts) prepends events as they are published; a refetch whose read
    // predates one of them must not take it back.
    structuralSharing: (current, incoming) =>
      replaceEqualDeep(
        current,
        mergeEventPages(current as EventPages | undefined, incoming as EventPages)
      ),
  });
  const issueState = stateForIssue(state, issueKey);
  const events = useMemo(() => log.data?.pages.flat() ?? [], [log.data]);
  // Each row's author is an `AgentName`, which reads the agent registry itself; the text form
  // here names the author in the row's `title` from what that read has cached.
  const agentText = useAgentText();
  // The day the reader is in, recomputed every render so a midnight rollover moves the `Today`
  // label; the model itself is rebuilt only when one of its inputs changes, since building it
  // copies, sorts, describes and re-walks the whole history.
  const today = dateKey(new Date().toISOString());
  const items = useMemo(
    () =>
      buildActivityItems({
        events,
        lastReadSeq: issueState.last_read_seq,
        today,
      }),
    [events, issueState.last_read_seq, today]
  );

  const togglePin = (eventId: number) =>
    writePin(queryClient, issueKey, (dismissed) => ({
      id: eventItemId({ id: eventId }),
      op: isPinnedEvent(dismissed, eventId) ? "unpin" : "pin",
    }));

  if (log.isPending) {
    return <p className={textMutedOnCanvas}>Loading activity…</p>;
  }
  if (log.isError) {
    return <p className={dangerText}>Could not load the activity.</p>;
  }

  // The stream prepends each published event to the front of page 0 (`prependEventToLog`) and
  // this list is newest first, so an arrival pushes every row below it down. The anchor holds
  // the row the reader is on - each row carries its own `data-event-seq` - where it was.
  return (
    <ViewportAnchor
      as="section"
      className="flex min-h-[60dvh] flex-col gap-3 pb-16"
      item="data-event-seq"
      label="Activity"
    >
      <ol aria-label="Issue activity" className="flex flex-col">
        {items.map((item) => {
          if (item.kind === "day-divider" || item.kind === "unread-divider") {
            const label = item.kind === "day-divider" ? item.label : "New since you last read";
            return (
              // A labelled list item, not a `role="separator"`: the role took the item out of
              // the list while it stayed a child of the `<ol>`, which is a list element with a
              // child that is no list item (axe's `list`, serious). The label is what a reader
              // hears, and `e2e/activity.e2e.ts` finds the dividers by it.
              <li
                aria-label={label}
                className={`flex items-center gap-3 pt-3 pb-1 text-xs font-medium first:pt-0 ${
                  item.kind === "unread-divider" ? linkText : textMutedOnCanvas
                }`}
                key={item.id}
              >
                {/* The label starts at the rows' left edge and its rule runs to the right one,
                    so a day reads as the heading of the rows under it. */}
                {label}
                <span className={`h-px flex-1 ${newDividerLine}`} />
              </li>
            );
          }

          const author = agentText.label(item.author);
          const pinned = isPinnedEvent(issueState.dismissed ?? [], item.event.id);
          return (
            <li
              className={`flex items-center gap-x-2 text-xs ${textMutedOnCanvas}`}
              data-event-seq={item.seq}
              key={item.id}
            >
              {/* The words wrap among themselves and the pin keeps the row's end, so a narrow
                  row never drops the pin to a line of its own under the words. */}
              <div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2">
                {/* Who and when are one group that never breaks, so the time never wraps alone to
                  the start of the next row when the author takes most of the line. The author
                  truncates inside it, with the full label on hover - a live session title can
                  be a whole sentence - and only the description wraps, with a floor so it
                  wraps as prose rather than one word per line. */}
                <span className="flex max-w-full min-w-0 shrink-0 items-baseline gap-x-2 whitespace-nowrap">
                  <span
                    className={`min-w-0 truncate font-medium ${textSecondaryOnCanvas}`}
                    title={author}
                  >
                    <AgentName actor={item.author} inline />
                  </span>
                  <span aria-hidden="true">·</span>
                  <Timestamp at={item.at} />
                </span>
                <span className={`min-w-40 flex-1 break-words ${textSecondaryOnCanvas}`}>
                  <ActivityLine
                    description={item.description}
                    event={item.event}
                    issueKey={issueKey}
                  />
                </span>
              </div>
              <PinButton
                label={pinned ? "Unpin" : "Pin"}
                onClick={() => togglePin(item.event.id)}
                pinned={pinned}
                quiet
              />
            </li>
          );
        })}
      </ol>
      {items.length === 0 ? <p className={textMutedOnCanvas}>No activity yet.</p> : null}
      {log.hasNextPage ? (
        <div className="flex items-center justify-center">
          <Button disabled={log.isFetchingNextPage} onClick={() => void log.fetchNextPage()}>
            {log.isFetchingNextPage ? "Loading…" : "Load older"}
          </Button>
        </div>
      ) : null}
    </ViewportAnchor>
  );
}
