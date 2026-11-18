import { useQueryClient } from "@tanstack/react-query";

import { api } from "../../api/client";
import { inboxQuery, whoAmIQuery } from "../../api/queries";
import type { Ask, InboxRow } from "../../api/types";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import {
  cancelMeInbox,
  invalidateMeInbox,
  meInboxSnapshot,
  patchMeInboxAsks,
  restoreMeInbox,
} from "./one-read-cache";
import { invalidateOwnerReads } from "./owner-reads";

export interface AskHandOnWrite {
  /** The server's reason after a refused hand-on, until the next attempt. */
  error: string | undefined;
  /** True after a hand-on failed until the next attempt starts. */
  failed: boolean;
  pending: boolean;
  /** Repeats the failed hand-on. */
  retry: () => void;
  /** Hands the ask to `askedOf`, the email of somebody who has signed in. */
  submit: (askedOf: string) => void;
}

/**
 * The one way the SPA hands an ask to a person. The new person lands on every inbox cache at
 * once, so the row leaves the list it was in and joins theirs on the press, rolls back if the
 * server refuses, and every read carrying the ask - the Inbox and its owner's lists
 * (`invalidateOwnerReads`), the one read behind the Inbox's opt-in (`one-read-cache.ts`), and the
 * ask's own thread - refetches once the hand-on settles, the same shape as `useAskSnooze`, over
 * `PUT /api/v1/asks/{id}/asked-of`. Behind the opt-in the reader's own asks are the one read's
 * alone, so an ask handed to the reader joins that read's Inbox answer as well, where it held no
 * entry yet; without it the row would leave the old read's list and join no other.
 *
 * What is patched is what the server will answer with: `waiting_on_person`, which is the person
 * named either way, and the three stored fields behind it - `asked_of`, empty when the ask was
 * handed to the assignee of its own issue, since an empty field means the ask follows that
 * issue again; `asked_of_by`, the reader; and `asked_of_at`, now. The reader is taken from the
 * `/auth/whoami` read the shell already holds rather than read again: it is in the cache before
 * any card can be pressed, and a hand-on made without it leaves the pair unnamed until the
 * refetch answers.
 */
export function useAskHandOn(ask: Ask): AskHandOnWrite {
  const queryClient = useQueryClient();
  const inboxKey = inboxQuery().queryKey;
  // The card says why its hand-on was refused, where the reader pressed it.
  const mutation = useKeyedWrite({
    write: (askedOf: string, bounds) => api.handAskTo(ask.id, askedOf, bounds),
    inline: true,
    label: (askedOf) => `Ask handed to ${askedOf}`,
    onMutate: async (askedOf) => {
      await Promise.all([
        queryClient.cancelQueries({ queryKey: inboxKey }),
        cancelMeInbox(queryClient),
      ]);
      const previous = queryClient.getQueriesData<InboxRow[]>({ queryKey: inboxKey });
      const oneRead = meInboxSnapshot(queryClient);
      const by = queryClient.getQueryData(whoAmIQuery().queryKey)?.login ?? null;
      const at = new Date().toISOString();
      const handedOn = (row: InboxRow): InboxRow =>
        row.id === ask.id
          ? {
              ...row,
              // Naming the assignee of the ask's own issue stores no person: the ask
              // follows that issue again, and whom it waits on is the person named either
              // way.
              asked_of: askedOf === (row.issue?.assignee ?? null) ? null : askedOf,
              asked_of_by: by,
              asked_of_at: at,
              waiting_on_person: askedOf,
            }
          : row;
      queryClient.setQueriesData<InboxRow[]>({ queryKey: inboxKey }, (current) =>
        current?.map(handedOn)
      );
      const joined =
        askedOf === by
          ? (queryClient.getQueryData<InboxRow[]>(inboxKey) ?? []).filter(
              (row) => row.id === ask.id
            )
          : [];
      patchMeInboxAsks(queryClient, handedOn, joined);
      return { inbox: previous, oneRead };
    },
    onError: (_error, _askedOf, previous) => {
      for (const [key, data] of previous?.inbox ?? []) {
        queryClient.setQueryData(key, data);
      }
      restoreMeInbox(queryClient, previous?.oneRead);
    },
    onSettled: () => {
      // Every surface that draws this ask follows the write, not the `ask.asked_of` event that
      // lands after it: on the issue page, in a document's margin and in its ask block the card
      // reads the ask from its owner's list, where the control's own value comes from, so an
      // Inbox-only refresh would snap the picker back to the person before the hand-on.
      invalidateOwnerReads(queryClient, ask);
      invalidateMeInbox(queryClient);
      // The Inbox's own Reply-thread card is the one surface that reads the ask from its thread
      // instead (`ThreadBody`, `askThreadQuery`), which is kept for the session and never goes
      // stale of itself. It is read again here rather than in `invalidateOwnerReads`, whose
      // other caller - `useAskAnswerForm` - already refreshes that thread in each branch that
      // moves it, and whose own subject is the reads that carry an ask besides its own thread.
      void queryClient.invalidateQueries({ queryKey: ["ask-thread", ask.id] });
    },
  });
  return {
    error: mutation.error?.message,
    failed: mutation.isError,
    pending: mutation.isPending,
    retry: () => {
      if (mutation.variables !== undefined) mutation.mutate(mutation.variables);
    },
    submit: (askedOf) => mutation.mutate(askedOf),
  };
}
