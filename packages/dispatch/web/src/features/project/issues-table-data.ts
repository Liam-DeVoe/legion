import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import {
  advancesIssuesQuery,
  projectDoneIssuesQuery,
  projectDonePageQuery,
  projectIssuesQuery,
} from "../../api/queries";
import type { IssueSummary, LegionBoardResponse } from "../../api/types";
import { claimIsTakeable } from "../issue/ClaimChip";
import { useAgentText } from "../refs/AgentName";
import type { AgentRegistry } from "../refs/agent-registry";
import { openIssueStatuses } from "./board-model";
import { buildItems } from "./issues-table-model";

const NO_ISSUES: readonly IssueSummary[] = [];

/**
 * What a table lists. A project's table lists that project's issues; a scoped table lists the
 * issues that advance one priority, where `advances` is the priority node's id and the server
 * narrows the listing by it (`GET /api/v1/issues?advances=<id>`). A priority belongs to no
 * project, so a scoped table lists the work of every project and stands on none of them: it
 * can make no write that names a project, and it offers none (`issues-table-actions.ts`).
 */
export type IssueListing = { readonly project: string } | AdvancesListing;

/**
 * The issues of every project the server narrows by what they advance: one priority's id, or
 * `none` for the work that advances no live priority and `any` for the work that advances one.
 * `statuses`, where it is set, is the whole of what the listing holds - the issues standing in
 * one of those statuses, which is how the priorities page asks for each of its two faults (see
 * `answeredWhole` for when its open read is every row of it). `moved` asks for the time each row
 * standing in backlog or icebox was moved there and who moved it.
 */
export type AdvancesListing = {
  readonly advances: string;
  readonly statuses?: readonly string[];
  readonly moved?: boolean;
};

/**
 * Whether a listing's one open read (`open: true`) answers every row its table can show, so the
 * table never reads the closed issues beside it: a listing that names its statuses, every one of
 * them open. A listing whose statuses include `done` holds closed issues the open read leaves
 * out, and a project's listing, or a listing that names no status, holds every closed issue it
 * has: each of those reads its closed issues once the reader lets them through.
 */
export function answeredWhole(listing: IssueListing): boolean {
  return (
    "advances" in listing &&
    listing.statuses !== undefined &&
    listing.statuses.every((status) => (openIssueStatuses as readonly string[]).includes(status))
  );
}

/** The name what a table keeps of its listing is held under: the action a reload interrupted.
 *  Two listings of different rows never share one. */
export const listingName = (listing: IssueListing): string =>
  "project" in listing
    ? listing.project
    : [`advances:${listing.advances}`, ...(listing.statuses ?? [])].join(":");

/** One page of the done issues, in the order they closed: what the Done view reads while
 *  nothing narrows it but a Closed window. */
export interface DonePage {
  readonly sort: "closed_at" | "-closed_at";
  readonly limit: number;
  readonly offset: number;
}

/** Which done issues a view reads: those closed in a window, every done issue (`{}`), one page
 *  of either (`page`), or none (`undefined`), for a view that lists no closed issue. */
export type DoneRead =
  | {
      readonly closed_since?: string;
      readonly closed_before?: string;
      readonly page?: DonePage;
    }
  | undefined;

/**
 * One listing's issues as the table shows them: the open listing, unless the view lists no open
 * issue (`readsOpen` false: Done), and the done issues a view that lists closed issues reads -
 * only those closed in its window where it has one, and only the page on screen where it reads
 * a page (`total` then counts every done issue the window holds). No view of a project reads
 * the project's full listing. A scoped listing (`advances`) is narrowed by the server already,
 * so its done issues come from the listing read whole, and a listing its open read answers
 * whole (`answeredWhole`) reads no done issue at all.
 *
 * A done read whose rows carry no `closed_at` (an older server during a rolling deploy, which
 * ignores `closed_since`) lists no row for a window, rather than every done issue.
 */
export function useIssueTableItems(
  listing: IssueListing,
  {
    done,
    readsOpen,
    registry,
    board,
    hasLegion,
    viewer,
  }: {
    done: DoneRead;
    readsOpen: boolean;
    registry: AgentRegistry;
    board: LegionBoardResponse | undefined;
    hasLegion: boolean;
    viewer: string | undefined;
  }
) {
  const text = useAgentText();
  const issues = useQuery({
    ...("project" in listing
      ? projectIssuesQuery(listing.project, [], true)
      : advancesIssuesQuery(listing, true)),
    enabled: readsOpen,
  });
  const openRows = readsOpen ? issues.data : NO_ISSUES;
  const reads = done !== undefined && !answeredWhole(listing);
  const closedWindow = {
    ...(done?.closed_since === undefined ? {} : { closed_since: done.closed_since }),
    ...(done?.closed_before === undefined ? {} : { closed_before: done.closed_before }),
  };
  const page = "project" in listing ? done?.page : undefined;
  const doneIssues = useQuery({
    ...("project" in listing
      ? projectDoneIssuesQuery(listing.project, closedWindow)
      : advancesIssuesQuery(listing)),
    enabled: reads && page === undefined,
  });
  const donePage = useQuery({
    ...projectDonePageQuery(
      "project" in listing ? listing.project : "",
      closedWindow,
      page ?? { sort: "-closed_at", limit: 1, offset: 0 }
    ),
    enabled: reads && page !== undefined,
  });
  const doneRead = page === undefined ? doneIssues : donePage;
  const windowed = reads && done?.closed_since !== undefined;
  const doneRows = !reads
    ? NO_ISSUES
    : page === undefined
      ? (doneIssues.data ?? NO_ISSUES)
      : (donePage.data?.issues ?? NO_ISSUES);
  const doneLoaded = reads && doneRead.data !== undefined;
  const listingError =
    readsOpen && issues.isError ? issues : doneRead.isError ? doneRead : undefined;
  const all = useMemo(() => {
    const open = openRows ?? NO_ISSUES;
    if (doneRows.length === 0) return open;
    const keys = new Set(open.map((issue) => issue.key));
    const closed = doneRows.filter(
      (issue) => !keys.has(issue.key) && (!windowed || issue.closed_at !== null)
    );
    return [...open, ...closed];
  }, [openRows, doneRows, windowed]);
  const allItems = useMemo(
    () =>
      buildItems(
        all,
        text.label,
        (issue) => issue.claim === null || claimIsTakeable(issue.claim, registry),
        { board, hasLegion, viewer }
      ),
    [all, text, registry, board, hasLegion, viewer]
  );
  return {
    all,
    allItems,
    doneIssues: doneRead,
    doneLoaded,
    /** Every done issue the paged read's window holds, where the view reads a page. */
    doneTotal: page === undefined ? undefined : donePage.data?.total,
    issues,
    listingError,
    /** The open listing is answered, or the view does not read it. */
    openLoaded: openRows !== undefined,
  };
}
