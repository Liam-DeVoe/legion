/**
 * The Inbox's switch to the one read, `GET /api/v1/me/inbox`. The page at `/` draws the
 * reader's own rows from it by default, with no opt-in: the address's `one=1`
 * (`InboxRoute.one`) and the browser preference `inbox.one_read`, which opted in while the
 * default was still the reads it replaces, change nothing now. The surfaces it moves are the
 * reader's own: the asks that wait on them (`useInboxAsks`), their Reply and Agent rows
 * (`useInboxThreadRows`), and the two numbers the Inbox entry adds up (`useNeedsYouCount`,
 * `useUnreadThreadCount`). Another person's asks are the Inbox read's either way.
 */

import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";

import { meInboxQuery, whoAmIQuery } from "../../api/queries";
import type { OpenAskRow } from "../../api/types";
import { useIsHuman } from "../../hooks/useIsHuman";
import { meInboxAsks } from "./one-read-rows";
import { isMine } from "./sections";

/** Whether the reader's own rows come from the one read: always, on every page, the drawer's
 *  Inbox included, since the one read is the Inbox's default. */
export function useOneRead(): boolean {
  return true;
}

/** The open asks the Inbox lists (`useInboxAsks`), and the state of the one read they are drawn
 *  from behind the opt-in. */
export interface InboxAsks {
  readonly asks: OpenAskRow[] | undefined;
  /** Whether the one read the reader's own asks come from has not answered yet: false without
   *  the opt-in, where no such read is made. */
  readonly pending: boolean;
  /** Whether that read failed: false without the opt-in. */
  readonly failed: boolean;
  /** When the read `askId` is listed from answered, given when the Inbox read did
   *  (`inboxUpdatedAt`): the one read's time for an ask drawn from it, the Inbox read's for every
   *  other. A card seeds its thread from its row (`AskCard`'s `initialThreadUpdatedAt`), and that
   *  seed is as old as the read the row came from, not as new as the other read. */
  readonly updatedAtOf: (askId: string, inboxUpdatedAt: number) => number;
}

/**
 * The open asks the Inbox lists: `asks`, every one the Inbox read answered, as they are; and
 * behind the opt-in, those that wait on the reader taken from the one read in their place, each
 * once, with every other person's still the Inbox read's. The same array while neither read
 * moves, since the motion hold adopts what it is given by identity (`motion-hold.ts`); undefined
 * until the Inbox read has answered, as `asks` is. While the one read has not answered, or where
 * it failed, the list holds none of the reader's own asks, so it says so (`pending`, `failed`)
 * for the page to draw its loading state or its error rather than a list without them.
 */
export function useInboxAsks(asks: OpenAskRow[] | undefined): InboxAsks {
  const oneRead = useOneRead();
  const human = useIsHuman();
  const viewer = useQuery(whoAmIQuery()).data?.login;
  const own = useQuery({ ...meInboxQuery(), enabled: oneRead && human });
  const answer = own.data;
  const { listed, ids } = useMemo(() => {
    if (!oneRead || asks === undefined) return { ids: new Set<string>(), listed: asks };
    const mine = meInboxAsks(answer);
    const ids = new Set(mine.map((ask) => ask.id));
    const others = asks.filter(
      (ask) => !ids.has(ask.id) && (viewer === undefined || !isMine({ ask, kind: "ask" }, viewer))
    );
    return { ids, listed: [...others, ...mine] };
  }, [asks, oneRead, answer, viewer]);
  const oneReadAt = own.dataUpdatedAt;
  const updatedAtOf = useCallback(
    (askId: string, inboxUpdatedAt: number) => (ids.has(askId) ? oneReadAt : inboxUpdatedAt),
    [ids, oneReadAt]
  );
  const reading = oneRead && human;
  return {
    asks: listed,
    failed: reading && own.isError,
    pending: reading && own.isPending,
    updatedAtOf,
  };
}
