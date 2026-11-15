import { type QueryKey, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";

import { issuePatchRequest } from "../../api/client";
import { mergeIssue } from "../../api/issue-cache";
import { inboxQuery } from "../../api/queries";
import type { InboxRow, IssueDetails, IssueSummary, UpdateIssueInput } from "../../api/types";
import { useSubmitGuard } from "../../hooks/useSubmitGuard";
import { stoppedNote, useKeyedWrite } from "../writes/useKeyedWrite";

export interface IssueFieldWrite<Value> {
  /** The server's reason after a refused save, or what a save the reader stopped may have done
   *  and what Retry does with it (`stopped`), until the next attempt. */
  error: string | undefined;
  /** True after a save failed until the next attempt starts. */
  failed: boolean;
  pending: boolean;
  /** Repeats the failed save; a no-op while one is in flight. */
  retry: () => void;
  /** What a save the reader stopped may have done and what Retry does with it, until the next
   *  attempt; undefined after any other end. */
  stopped: string | undefined;
  /** Saves `value`. A pick made while a save is in flight shows at once and is saved when that
   *  save settles; the newest such pick is the one saved. */
  submit: (value: Value) => void;
}

interface Snapshot {
  details: IssueDetails | undefined;
  inbox: [QueryKey, InboxRow[] | undefined][];
  lists: [QueryKey, IssueSummary[] | undefined][];
}

export const projectListsKey = ["issues", "project"] as const;
export const inboxKey = inboxQuery().queryKey;

/** One save of a field: the issue it is of and the value it sets. The control that makes it can
 *  outlive that issue - the reading column's head is the same control for each ask the reader
 *  moves to - so the save carries its issue, and its request, its Retry and what the page shows
 *  of it are of that issue, not of the one the control is drawn for by then. */
interface FieldSave<Value> {
  readonly issue: string;
  readonly value: Value;
}

/**
 * The optimistic write engine behind every single-field issue write (priority, assignee). The
 * caller supplies `patch`, the request body for a value, and `show`, which lands the value in
 * the issue detail, every project issue list and every inbox row that shows the issue. The engine
 * snapshots those caches before showing a pick, rolls them back on failure, merges the server's
 * answer on success, and refetches once the save settles. Requests go one at a time, so the
 * server sees picks in the order they were made: a pick during a save is queued behind it, and
 * the in-flight answer is not merged over a newer pick the reader can already see.
 */
export function useIssueFieldWrite<Value>(
  issueKey: string,
  field: {
    patch: (value: Value) => UpdateIssueInput;
    /** Lands `value` in every cache that shows `issue`, the issue the save is of. */
    show: (value: Value, issue: string) => void;
    /** What the shell's indicator names one save of this field of `issue`. */
    label: (value: Value, issue: string) => string;
    /** What the field of `issue` is in the sentence a save the reader stopped says
     *  (`stoppedWriteNote`): `the priority of KEY`. */
    what: (issue: string) => string;
  }
): IssueFieldWrite<Value> {
  const queryClient = useQueryClient();
  const guard = useSubmitGuard();
  // A pick made during a save, with the cache as it stood before the pick was shown: the base
  // the chained request rolls back to, so its failure never lands on the pick that failed.
  const queued = useRef<{ save: FieldSave<Value>; snapshot: Snapshot } | null>(null);
  const chainedSnapshot = useRef<Snapshot | null>(null);
  const takeSnapshot = (issue: string): Snapshot => ({
    details: queryClient.getQueryData<IssueDetails>(["issue", issue]),
    inbox: queryClient.getQueriesData<InboxRow[]>({ queryKey: inboxKey }),
    lists: queryClient.getQueriesData<IssueSummary[]>({ queryKey: projectListsKey }),
  });
  // The control says why its save was refused and offers Retry where the reader just picked, so
  // the shell's indicator shows the save only while it is out. Every write to the issue's fields
  // goes in the issue's line (`issue:<key>`), so one still being tried after its surface is gone
  // never lands after a newer one.
  const mutation = useKeyedWrite({
    inline: true,
    label: ({ issue, value }: FieldSave<Value>) => field.label(value, issue),
    target: ({ issue }) => `issue:${issue}`,
    request: ({ issue, value }: FieldSave<Value>) => issuePatchRequest(issue, field.patch(value)),
    onMutate: async ({ issue, value }): Promise<Snapshot> => {
      await Promise.all(
        [["issue", issue], projectListsKey, inboxKey].map((queryKey) =>
          queryClient.cancelQueries({ queryKey })
        )
      );
      const snapshot = chainedSnapshot.current ?? takeSnapshot(issue);
      chainedSnapshot.current = null;
      field.show(value, issue);
      return snapshot;
    },
    onError: (_error, { issue }, snapshot) => {
      if (snapshot === undefined) {
        return;
      }
      if (snapshot.details !== undefined) {
        queryClient.setQueryData<IssueDetails>(["issue", issue], snapshot.details);
      }
      for (const [key, data] of snapshot.lists) {
        queryClient.setQueryData(key, data);
      }
      for (const [key, data] of snapshot.inbox) {
        queryClient.setQueryData(key, data);
      }
      if (queued.current !== null) {
        // The cache is back at this save's base, which is now the queued pick's base too.
        queued.current.snapshot = snapshot;
      }
    },
    onSuccess: (next) => {
      // Saved with no answer to give: `onSettled` reads the issue again.
      if (queued.current === null && next !== null) {
        mergeIssue(queryClient, next);
      }
    },
    onSettled: (_next, _error, { issue }) => {
      guard.release();
      const next = queued.current;
      if (next !== null) {
        queued.current = null;
        chainedSnapshot.current = next.snapshot;
        guard.guard(() => mutation.mutate(next.save));
        return;
      }
      // Another write to the same issue (a status change from the header) may have settled in
      // between; refetching the detail makes it server-authoritative rather than trusting the
      // arrival order of the two responses or a pre-write snapshot.
      void queryClient.invalidateQueries({ queryKey: ["issue", issue] });
      void queryClient.invalidateQueries({ queryKey: ["issues"] });
      void queryClient.invalidateQueries({ queryKey: inboxKey });
    },
  });
  // A save the reader stopped may have been made, and Retry makes that very press again
  // (`retryLast`): the error's own words are a composer's, of a message, and name the issue that
  // press was of.
  const stopped = stoppedNote(mutation, field.what(mutation.variables?.issue ?? issueKey), "saved");
  return {
    error: stopped ?? mutation.error?.message,
    failed: mutation.isError,
    pending: mutation.isPending,
    retry: () => {
      guard.retryLast(mutation);
    },
    stopped,
    submit: (value) => {
      const save = { issue: issueKey, value };
      if (guard.guard(() => mutation.mutate(save))) {
        return;
      }
      queued.current = { save, snapshot: queued.current?.snapshot ?? takeSnapshot(issueKey) };
      field.show(value, issueKey);
    },
  };
}
