import type { ReactNode } from "react";

import type { IssuePriority } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { usePageWords } from "../../components/tooltips";
import { badgeLow, pageInkText, pageQuietText, priorityBadge } from "../../theme/classes";
import {
  badgeSelectBadge,
  badgeSelectHeaderWrapper,
  badgeSelectOverlay,
  badgeSelectWrapper,
} from "./badge-select";
import { type IssuePriorityWrite, useIssuePriority } from "./useIssuePriority";

const priorityOptions: readonly { label: string; value: IssuePriority | null }[] = [
  { label: "Unset", value: null },
  { label: "P0", value: 0 },
  { label: "P1", value: 1 },
  { label: "P2", value: 2 },
  { label: "P3", value: 3 },
];

/**
 * The priority editor for a surface that owns no write of its own - a list row, a board card,
 * an Inbox row - where this control is the only thing that writes that issue's priority.
 */
export function PriorityControl({
  disabled,
  issueKey,
  priority,
}: {
  disabled?: boolean;
  issueKey: string;
  priority: IssuePriority | null;
}): ReactNode {
  const write = useIssuePriority(issueKey);
  return (
    <PriorityEditor disabled={disabled} issueKey={issueKey} priority={priority} write={write} />
  );
}

/**
 * The single priority editor: shows the issue's priority as the `P0`–`P3` badge (a muted
 * "Priority" tag when unset) and, on click, tap, Enter or Space, opens a native `<select>` to
 * pick P0–P3 or Unset — the same picker on desktop and iPhone. The select is laid invisibly over
 * the badge so the tap target stays 44 px (32 px from `md`) while the badge keeps its size, and
 * the wrapper draws the keyboard focus ring. `write` goes through `useIssuePriority`, so every
 * surface gets the same optimistic update, rollback and list refetch. The select stays enabled
 * while a save is in flight: a disabled control drops the focus it holds and leaves the tab
 * order, and a pick made meanwhile is queued behind the save rather than refused. A press on the
 * select stays on the select: a board card is a drag activator, and a held tap on the badge must
 * open the picker, not lift the card. The select is labelled `Priority of <KEY>`; the board's `p`
 * reaches it through the focused card.
 *
 * The host passes the write when it has other ways to set the same priority: the issue page
 * creates one for the page and shares it with the keyboard's `0`–`3` and the palette's Set
 * priority rows, so one refusal is reported once, here, by the control the reader is looking at.
 *
 * `variant="header"` draws the new issue page header's shape (the prototype's `.prio`,
 * dispatch://INFRA-1196 R13): three bars filled by how urgent the priority is, beside `P1` in
 * the ink, or the word Priority in the quiet grey while it has none. The select over it is the
 * same either way.
 */
export function PriorityEditor({
  disabled = false,
  issueKey,
  priority,
  variant = "badge",
  write,
}: {
  disabled?: boolean;
  issueKey: string;
  priority: IssuePriority | null;
  variant?: "badge" | "header";
  write: IssuePriorityWrite;
}): ReactNode {
  const page = usePageWords();
  const tone = priority === null ? badgeLow : priorityBadge[priority];
  const shown = priority === null ? (page?.PROPERTY_LABELS.priority ?? "Priority") : `P${priority}`;
  const mark = priority === null ? page?.pageChrome : page?.authorText;
  return (
    <>
      <span
        className={`${variant === "header" ? badgeSelectHeaderWrapper : badgeSelectWrapper} ${disabled ? "opacity-50" : ""}`}
      >
        {variant === "header" ? (
          <span
            aria-hidden="true"
            className={`inline-flex items-center gap-[5px] text-sm leading-5 font-semibold ${
              priority === null ? pageQuietText : pageInkText
            }`}
            {...mark}
          >
            <PriorityBars priority={priority} />
            {shown}
          </span>
        ) : (
          <span
            aria-hidden="true"
            className={`${badgeSelectBadge} ${tone.bg} ${tone.text}`}
            {...mark}
          >
            {shown}
          </span>
        )}
        <select
          aria-label={`Priority of ${issueKey}`}
          className={badgeSelectOverlay}
          disabled={disabled}
          onMouseDown={(event) => event.stopPropagation()}
          onPointerDown={(event) => event.stopPropagation()}
          onTouchStart={(event) => event.stopPropagation()}
          onChange={(event) => {
            const { value } = event.target;
            write.submit(value === "" ? null : (Number(value) as IssuePriority));
          }}
          value={priority ?? ""}
        >
          {priorityOptions.map((option) => (
            <option key={option.label} value={option.value ?? ""}>
              {option.label}
            </option>
          ))}
        </select>
      </span>
      {write.failed ? (
        <QueryError
          message={`Could not update the priority of ${issueKey}.`}
          onRetry={write.retry}
          retrying={write.pending}
        />
      ) : null}
    </>
  );
}

/** The prototype's priority shape: three rising bars, as many filled as the priority is urgent
 *  - three for P0, none for P3 or none set. `P1` beside it says the same in words. */
function PriorityBars({ priority }: { priority: IssuePriority | null }): ReactNode {
  const filled = priority === null ? 0 : 3 - priority;
  return (
    <svg aria-hidden="true" className="size-3.5 shrink-0" viewBox="0 0 14 14">
      {[
        { height: 5, x: 1, y: 8 },
        { height: 8, x: 5.5, y: 5 },
        { height: 11, x: 10, y: 2 },
      ].map((bar, index) => (
        <rect
          className={index < filled ? "fill-page-ink" : "fill-page-hair"}
          height={bar.height}
          key={bar.x}
          rx="1"
          width="3"
          x={bar.x}
          y={bar.y}
        />
      ))}
    </svg>
  );
}
