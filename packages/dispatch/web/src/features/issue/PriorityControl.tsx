import type { ReactNode } from "react";

import type { IssuePriority } from "../../api/types";
import { ChoiceMenu, type MenuChoice } from "../../components/ChoiceMenu";
import { QueryError } from "../../components/QueryError";
import { badgeLow, priorityBadge } from "../../theme/classes";
import { badgeMenuTrigger, badgeSelectBadge } from "./badge-select";
import { type IssuePriorityWrite, useIssuePriority } from "./useIssuePriority";

/** The priority menu: Unset, then P0 to P3 on their own digits. A choice's value is the priority
 *  as text, `""` for none. */
const priorityChoices: readonly MenuChoice<string>[] = [
  { label: "Unset", value: "" },
  { digit: "0", label: "P0", value: "0" },
  { digit: "1", label: "P1", value: "1" },
  { digit: "2", label: "P2", value: "2" },
  { digit: "3", label: "P3", value: "3" },
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
 * "Priority" tag when unset), and a click, a tap, Enter or Space on it opens the priority menu
 * (`ChoiceMenu`) of Unset and P0–P3, with the current one checked and `0`–`3` writing P0–P3 -
 * the same menu on desktop and iPhone. The badge is the 44 px tap target (32 px from `md`) and
 * draws the keyboard focus ring. `write` goes through `useIssuePriority`, so every surface gets
 * the same optimistic update, rollback and list refetch. The trigger stays enabled while a save
 * is in flight: a disabled control drops the focus it holds and leaves the tab order, and a pick
 * made meanwhile is queued behind the save rather than refused. A press on the badge or in its
 * menu stays there: a board card is a drag activator, and a held tap on the badge must open the
 * menu, not lift the card. The trigger is labelled `Priority of <KEY>` and marked
 * `data-priority-menu`, which the Inbox's, the issue page's and the Board's `p` press.
 *
 * The host passes the write when it has other ways to set the same priority: the issue page
 * creates one for the page and shares it with the keyboard's `0`–`3` and the palette's Set
 * priority rows, so one refusal is reported once, here, by the control the reader is looking at.
 * `held` is the host's own write out (the issue page's other field writes): the trigger keeps the
 * focus and its Tab stop and opens nothing (`ChoiceMenu`'s `held`).
 */
export function PriorityEditor({
  disabled = false,
  held = false,
  issueKey,
  priority,
  write,
}: {
  disabled?: boolean;
  held?: boolean;
  issueKey: string;
  priority: IssuePriority | null;
  write: IssuePriorityWrite;
}): ReactNode {
  const tone = priority === null ? badgeLow : priorityBadge[priority];
  return (
    <>
      <ChoiceMenu
        checked={priority === null ? "" : `${priority}`}
        choices={priorityChoices}
        disabled={disabled}
        held={held}
        label={`Priority of ${issueKey}`}
        marks={{ "data-priority-menu": "" }}
        onChoose={(value) => write.submit(value === "" ? null : (Number(value) as IssuePriority))}
        triggerClassName={badgeMenuTrigger}
      >
        <span className={`${badgeSelectBadge} ${tone.bg} ${tone.text}`}>
          {priority === null ? "Priority" : `P${priority}`}
        </span>
      </ChoiceMenu>
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
