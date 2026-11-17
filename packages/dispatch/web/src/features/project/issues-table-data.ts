import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { advancesIssuesQuery, projectIssuesQuery } from "../../api/queries";
import { claimIsTakeable } from "../issue/ClaimChip";
import { useAgentText } from "../refs/AgentName";
import type { AgentRegistry } from "../refs/agent-registry";
import { openIssueStatuses } from "./board-model";
import { buildItems } from "./issues-table-model";

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
 * has: each of those reads the full listing once the reader lets closed issues through.
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

/**
 * One project's issues as the table shows them - or, under a scope, every project's issues that
 * advance one priority: the open listing, and the full one once the reader lets closed issues
 * through.
 *
 * Two reads, never three. The open-only listing stops once the full one has answered, so a live
 * event refetches one listing.
 */
export function useIssueTableItems(
  listing: IssueListing,
  { wantsDone, registry }: { wantsDone: boolean; registry: AgentRegistry }
) {
  const text = useAgentText();
  // Scoped, the listing is the priority's own, which names no project: a priority belongs to
  // none, so the work under it is every project's. A listing its open read answers whole never
  // asks for this one (`answeredWhole`, which `IssuesTable` reads).
  const everyIssue = useQuery({
    ...("project" in listing
      ? projectIssuesQuery(listing.project, [])
      : advancesIssuesQuery(listing)),
    enabled: wantsDone,
  });
  const doneLoaded = wantsDone && everyIssue.data !== undefined;
  const openIssues = useQuery({
    ...("project" in listing
      ? projectIssuesQuery(listing.project, [], true)
      : advancesIssuesQuery(listing, true)),
    enabled: !doneLoaded,
  });
  const issues = doneLoaded ? everyIssue : openIssues;
  const listingError =
    wantsDone && everyIssue.isError ? everyIssue : issues.isError ? issues : undefined;
  const all = useMemo(() => issues.data ?? [], [issues.data]);
  const allItems = useMemo(
    () =>
      buildItems(
        all,
        text.label,
        (issue) => issue.claim === null || claimIsTakeable(issue.claim, registry)
      ),
    [all, text, registry]
  );
  return {
    all,
    allItems,
    doneLoaded,
    everyIssue,
    issues,
    listingError,
  };
}
