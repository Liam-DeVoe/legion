import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { usersQuery } from "../../api/queries";
import { MultiSelect } from "../../components/MultiSelect";
import { QueryError } from "../../components/QueryError";
import { badgeLow, badgePrimary } from "../../theme/classes";
import { badgeListTrigger, badgeSelectBadge, badgeSelectWrapper } from "./badge-select";
import { useIssueAssignee } from "./useIssueAssignee";

/**
 * The single assignee editor: shows who answers the issue's asks as a badge (their lowercase
 * email, or a muted "Unassigned" tag), and a click, a tap, Enter or Space on it opens the shared
 * list's single pick (`MultiSelect`) over everyone who has signed in (`GET /users`) with
 * Unassigned first, whose text box narrows the people as the reader types - the same list on
 * desktop and iPhone. Its closed trigger takes the list's keys (`keysOnTrigger`): an arrow, Home,
 * End, Page Up, Page Down or a letter opens the list and moves its highlight, and only Enter or a
 * click on a person writes; Escape, Tab and a press outside write nothing and leave the badge
 * showing the issue's assignee. The list is read only once the reader reaches for the control
 * (pointer over it, a touch on it, or focus) and then kept for the session: a fresh issue load
 * makes no request for it, and the badge never waits on it. Anyone signed in may reassign. The
 * write goes through `useIssueAssignee`, so the issue detail, the project lists and the inbox's
 * partitions update at once and roll back together; a refused person shows the server's reason,
 * and a pick of the person it already names writes nothing, as a `<select>` set to the value it
 * holds fires nothing. The trigger stays enabled while a save is in flight - a disabled control
 * drops the focus it holds and leaves the tab order - and a pick made meanwhile is queued behind
 * the save. Labelled `Assignee of <KEY>`. `held` is the host's own write out (the issue page's
 * other field writes): the trigger keeps the focus and its Tab stop (`aria-disabled`), and a pick
 * made then is dropped.
 */
export function AssigneeControl({
  assignee,
  disabled = false,
  held = false,
  issueKey,
}: {
  assignee: string | null;
  disabled?: boolean;
  held?: boolean;
  issueKey: string;
}): ReactNode {
  const [wanted, setWanted] = useState(false);
  const users = useQuery({ ...usersQuery(), enabled: wanted });
  const want = () => setWanted(true);
  const [open, setOpen] = useState(false);
  const write = useIssueAssignee(issueKey);
  const logins = (users.data ?? []).map((user) => user.login);
  // An assignee the list does not carry still names who holds the issue; keep it in the list so
  // the list always holds the value it marks. `""` is Unassigned.
  const people = assignee === null || logins.includes(assignee) ? logins : [assignee, ...logins];
  const tone = assignee === null ? badgeLow : badgePrimary;
  return (
    <>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handlers only keep a press on the trigger, or in the list it opens (portalled, but a React child of this one), out of the header under it, and read the people as the reader reaches for either, so the wrapper needs no role of its own. */}
      <span
        className={`${badgeSelectWrapper} ${disabled || held ? "opacity-50" : ""}`}
        onFocus={want}
        onMouseDown={(event) => event.stopPropagation()}
        onPointerDown={(event) => event.stopPropagation()}
        onPointerEnter={want}
        onTouchStart={(event) => {
          event.stopPropagation();
          want();
        }}
      >
        <MultiSelect
          ariaDisabled={held}
          disabled={disabled}
          emptyMessage={users.isPending ? "Loading people…" : "Nobody matches that."}
          keysOnTrigger
          label={`Assignee of ${issueKey}`}
          mode="single"
          onOpenChange={setOpen}
          onPick={(login) => {
            const picked = login === "" ? null : login;
            if (held || picked === assignee) return;
            write.submit(picked);
          }}
          open={open}
          optionLabel={(login) => (login === "" ? "Unassigned" : login)}
          options={["", ...people]}
          searchLabel="Search people"
          triggerClassName={badgeListTrigger}
          value={assignee ?? ""}
        >
          <span className={`${badgeSelectBadge} ${tone.bg} ${tone.text}`}>
            {assignee ?? "Unassigned"}
          </span>
        </MultiSelect>
      </span>
      {write.failed ? (
        <QueryError
          message={write.error ?? `Could not update the assignee of ${issueKey}.`}
          onRetry={write.retry}
          retrying={write.pending}
        />
      ) : null}
    </>
  );
}
