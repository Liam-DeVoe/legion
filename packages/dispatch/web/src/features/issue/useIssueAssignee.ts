import { useQueryClient } from "@tanstack/react-query";

import { projectDefaultAssigneesQuery, whoAmIQuery } from "../../api/queries";
import type { InboxRow, IssueDetails, IssueSummary } from "../../api/types";
import { issueProject } from "../../lib/issue-key";
import { patchMeInboxAsks } from "../inbox/one-read-cache";
import { inboxKey, projectListsKey, useIssueFieldWrite } from "./useIssueFieldWrite";

export interface IssueAssigneeWrite {
  /** The server's reason after a refused save (an unlisted login), until the next attempt. */
  error: string | undefined;
  /** True after a save failed until the next attempt starts. */
  failed: boolean;
  pending: boolean;
  /** Repeats the failed save; a no-op while one is in flight. */
  retry: () => void;
  /** Saves `assignee` (a login, or null to unassign). A pick made while a save is in flight
   *  shows at once and is saved when that save settles; the newest such pick is the one saved. */
  submit: (assignee: string | null) => void;
}

/**
 * The one way the SPA writes an issue's assignee. The issue header's picker and the inbox's
 * "Assign to me" share this: the new login lands optimistically in the issue detail, every
 * project issue list and every inbox row that belongs to the issue (so a row the Inbox's
 * assignee filter judges by that login moves in or out of the list at once), rolls back on
 * failure, and the lists refetch once the server has answered (see `useIssueFieldWrite`).
 *
 * An inbox row is judged by the person it waits on, which the server answers as
 * `coalesce(a.asked_of, i.assignee, p.default_assignee)`: so a row whose ask nobody has handed on
 * follows the new assignee here, and one somebody handed to a person keeps that person whoever
 * holds its issue. An unassign moves a row nobody handed on to its project's default assignee,
 * taken from the defaults read the Settings page holds (`projectDefaultAssigneesQuery`) where the
 * client has it; where it does not, the row keeps the person it waited on until the refetch names
 * the default. It never names nobody: every project names a default, so no row the server
 * answers does, and one written so would read as an ask no person waits on.
 *
 * The one read behind the Inbox's opt-in, which the reader's own asks are drawn from, takes the
 * same move, and a row the write makes wait on the reader joins its Inbox answer where it held no
 * entry (`patchMeInboxAsks`): without it, taking an unassigned ask would take the row out of the
 * old read's list and put it in no other.
 */
export function useIssueAssignee(issueKey: string): IssueAssigneeWrite {
  const queryClient = useQueryClient();
  const detailKey = ["issue", issueKey] as const;
  const showAssignee = (assignee: string | null) => {
    queryClient.setQueryData<IssueDetails>(detailKey, (current) =>
      current === undefined ? undefined : { ...current, assignee }
    );
    queryClient.setQueriesData<IssueSummary[]>({ queryKey: projectListsKey }, (current) =>
      current?.map((issue) => (issue.key === issueKey ? { ...issue, assignee } : issue))
    );
    // Every row the update moves is of this issue, so of its project: the person a row nobody
    // handed on waits on now is the new assignee, else that project's default where the client
    // holds the defaults read, looked up once.
    const project = issueProject(issueKey);
    const person =
      assignee ??
      queryClient
        .getQueryData(projectDefaultAssigneesQuery().queryKey)
        ?.find((entry) => entry.project === project)?.assignee;
    const assigned = (row: InboxRow): InboxRow => {
      if (row.issue_key !== issueKey || row.issue === undefined) return row;
      const moved = { ...row, issue: { ...row.issue, assignee } };
      if (row.asked_of !== null && row.asked_of !== undefined) return moved;
      return person === undefined ? moved : { ...moved, waiting_on_person: person };
    };
    queryClient.setQueriesData<InboxRow[]>({ queryKey: inboxKey }, (current) =>
      current?.map(assigned)
    );
    const viewer = queryClient.getQueryData(whoAmIQuery().queryKey)?.login;
    const joined = (queryClient.getQueryData<InboxRow[]>(inboxKey) ?? []).filter(
      (row) =>
        row.issue_key === issueKey && viewer !== undefined && row.waiting_on_person === viewer
    );
    patchMeInboxAsks(queryClient, assigned, joined);
  };
  return useIssueFieldWrite<string | null>(issueKey, {
    label: (assignee) => `${issueKey} assigned to ${assignee ?? "nobody"}`,
    patch: (assignee) => ({ assignee }),
    show: showAssignee,
  });
}
