import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { priorityHistoryQuery } from "../../api/queries";
import type { PriorityAct } from "../../api/types";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import {
  borderDefault,
  dangerText,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { dateKey, dayLabel } from "../activity/activity-model";
import { AgentName } from "../refs/AgentName";
import { Timestamp } from "../refs/Timestamp";

/** One act of the history in the words a person reads, with no field name and no value a
 *  reader cannot use: a write names what it wrote, a description names itself rather than
 *  quoting a markdown body, and an `advances` act names the issue at its other end. An act of
 *  a field written after this was (`prioritystore`'s attributes) still reads as a write of it. */
function actWords(act: PriorityAct): string {
  switch (act.field) {
    case "advances":
      return act.asserted
        ? `related the issue ${act.issue}`
        : `retracted the relation to the issue ${act.issue}`;
    case "title":
      return act.value === "" ? "wrote the title" : `wrote the title “${act.value}”`;
    case "description":
      return "wrote the description";
    case "owner":
      return act.value === "" ? "wrote the owner" : `wrote the owner “${act.value}”`;
    case "rank":
      return "moved the order";
    case "state":
      return act.value === "gone" ? "ended this priority" : "wrote this priority";
    case "visibility":
      return "wrote who can see this priority";
    default:
      return `wrote the ${act.field}`;
  }
}

/** Everything that happened to the priority, newest first, as the server answers it
 *  (`GET /api/v1/priorities/{id}/history`): each write of its title, description, owner and
 *  order, and each `advances` relation an issue asserted or retracted on it, each naming who
 *  stated it and when, under the day it happened on. */
function PriorityHistory({ id }: { id: string }): ReactNode {
  const history = useQuery(priorityHistoryQuery(id));
  if (history.isPending) return <LoadingSkeleton label="Loading what happened" />;
  if (history.isError)
    return (
      <p className={`text-sm ${dangerText}`} role="alert">
        Could not load what happened to this priority.
      </p>
    );
  // The day a reading stands on, read once for the whole list: a reading that crossed midnight
  // part way down it would head two entries of the same day differently.
  const today = dateKey(new Date().toISOString());
  let day = "";
  return (
    <ol className="space-y-3">
      {history.data.map((act) => {
        const heading = dayLabel(dateKey(act.at), today);
        const first = heading !== day;
        day = heading;
        return (
          <li
            className="space-y-1 text-sm"
            key={`${act.at}|${act.field}|${act.issue}|${act.value}`}
          >
            {first ? (
              <p className={`text-xs font-semibold tracking-wide uppercase ${textMutedOnCanvas}`}>
                {heading}
              </p>
            ) : null}
            <p className={textSecondaryOnCanvas}>
              <AgentName actor={act.actor} inline /> ·{" "}
              <Timestamp at={act.at} className={textMutedOnCanvas} />
            </p>
            <p>{actWords(act)}</p>
          </li>
        );
      })}
    </ol>
  );
}

/**
 * The margin of a priority: the list of what happened to it, beside the page, newest first,
 * each act naming who made it and when. That history is the whole margin, with no control and
 * no view of its own: a priority carries no comments, questions or pins until
 * dispatch://INFRA-1098 makes its description a document, so a choice of what the margin
 * shows would only offer views that are always empty.
 */
export function PriorityMargin({ id }: { id: string }): ReactNode {
  return (
    <aside
      aria-label="Margin"
      className={`w-full shrink-0 space-y-3 border-t pt-4 lg:w-80 lg:border-t-0 lg:border-l lg:pt-0 lg:pl-4 ${borderDefault}`}
      data-testid="priority-margin"
    >
      <PriorityHistory id={id} />
    </aside>
  );
}
