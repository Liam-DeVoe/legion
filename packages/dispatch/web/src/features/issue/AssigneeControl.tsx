import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { usersQuery } from "../../api/queries";
import { MultiSelect } from "../../components/MultiSelect";
import { QueryError } from "../../components/QueryError";
import { badgeLow, badgePrimary } from "../../theme/classes";
import {
  badgeReachHandlers,
  badgeSelectBadge,
  badgeSelectTrigger,
  badgeSelectWrapper,
} from "./badge-select";
import { useIssueAssignee } from "./useIssueAssignee";

/** The option that stands for nobody: the issue goes back to having no assignee. */
const UNASSIGNED = "";

/**
 * The single assignee editor: shows who answers the issue's asks as a badge (their lowercase
 * email, or a muted "Unassigned" tag), and the badge is the trigger of the shared list's single
 * pick (`MultiSelect`) over everyone who has signed in (`GET /users`) with Unassigned first,
 * whose text box narrows the people as the reader types - the same list on desktop and iPhone,
 * laid over the badge as the ask card's hand-on is. The list is read only once the reader
 * reaches for the control (pointer over it, a touch on it, or focus) and then kept for the
 * session: a fresh issue load makes no request for it, and the badge never waits on it; until it
 * answers the list holds Unassigned and the person the issue names, says the people are loading
 * where a typed word leaves nothing, and says so where the read failed. Anyone signed in may
 * reassign: a pick writes at once, and a pick of the person the issue already names writes
 * nothing, as a `<select>` set to the value it holds fires nothing. The write goes through
 * `useIssueAssignee`, so the issue detail, the project lists and the inbox's partitions update
 * at once and roll back together; a refused person shows the server's reason. Like
 * `PriorityControl`, the control stays enabled while a save is in flight - a disabled control
 * drops the focus it holds and leaves the tab order - and a pick made meanwhile is queued behind
 * the save. Labelled `Assignee of <KEY>`, with the badge's words as its description.
 */
export function AssigneeControl({
  assignee,
  disabled = false,
  issueKey,
}: {
  assignee: string | null;
  disabled?: boolean;
  issueKey: string;
}): ReactNode {
  const [wanted, setWanted] = useState(false);
  const users = useQuery({ ...usersQuery(), enabled: wanted });
  const [open, setOpen] = useState(false);
  const write = useIssueAssignee(issueKey);
  const logins = (users.data ?? []).map((user) => user.login);
  // An assignee the list does not carry still names who holds the issue; keep it selectable
  // so the control never shows a value its options lack.
  const people = assignee === null || logins.includes(assignee) ? logins : [assignee, ...logins];
  const value = assignee ?? UNASSIGNED;
  const tone = assignee === null ? badgeLow : badgePrimary;
  return (
    <>
      <span
        className={`${badgeSelectWrapper} ${disabled ? "opacity-50" : ""}`}
        {...badgeReachHandlers(() => setWanted(true))}
      >
        <MultiSelect
          chevron={false}
          disabled={disabled}
          emptyMessage={users.isPending ? "Loading people…" : "Nobody matches that."}
          error={users.isError ? "Could not load the people list." : null}
          label={`Assignee of ${issueKey}`}
          mode="single"
          onOpenChange={setOpen}
          onPick={(picked) => {
            if (picked !== value) write.submit(picked === UNASSIGNED ? null : picked);
          }}
          open={open}
          optionLabel={(login) => (login === UNASSIGNED ? "Unassigned" : login)}
          options={[UNASSIGNED, ...people]}
          searchLabel="Search people"
          triggerClassName={badgeSelectTrigger}
          value={value}
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
