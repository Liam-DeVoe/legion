import { type QueryStatus, useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { artifactBlocksQuery, issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import type { CheckBlock } from "../../../api/types";
import { useAddressTarget } from "../address-target";
import { issueAddress } from "../issue-address";
import { NoTooltips } from "../NoTooltips";
import { EMPTY_STATE_LINES } from "../page-words";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { useIssueTree } from "../useIssueTree";
import {
  VerificationFilterBody,
  type VerificationRead,
  type VerificationReads,
} from "../verification-filter/VerificationFilterBody";

/** Filled by INFRA-1196 part V, "The Verification view". */
export function ViewVerificationSlot({ issue, route }: SlotProps): ReactNode {
  // The Verification view (part V's R1): the issue's one tree as the Verification view leaves
  // it, each statement with the checks that serve it and each check beside its newest result.
  const { hash, search } = useLocation();
  const address = issueAddress(route, search, hash, primarySpec(issue)?.slug);
  const { root } = useIssueTree(issue);
  const { checks, reads } = useVerificationReads(issue.key);
  // The address's own node is resolved against the whole tree and then kept by the view, so an
  // address that names a node the view holds no row for still draws it (R1), as the outline
  // resolves it (part I's `useAddressTarget`, decision 94).
  const { named } = useAddressTarget(root, address);
  const view = useMemo(
    () => presetView(root, "verification", address.filter, named),
    [address.filter, named, root]
  );
  // The view with no words query, which is what the issue holds whatever a query left of it.
  // With no query typed it is the view itself, so it is built a second time only under one.
  const whole = useMemo(
    () => (address.filter === "" ? view.root : presetView(root, "verification", "", named).root),
    [address.filter, named, root, view.root]
  );
  return (
    <NoTooltips>
      {/* The stage's column, which the body draws its own measure in (part V's R6). */}
      <div className="flex w-full min-w-0 flex-col">
        <VerificationFilterBody
          checks={checks}
          context={view.context}
          emptyLine={
            address.filter === "" ? EMPTY_STATE_LINES.verification : EMPTY_STATE_LINES.no_match
          }
          reads={reads}
          view={view.root}
          whole={whole}
        />
      </div>
    </NoTooltips>
  );
}

/**
 * The reads the Verification view is drawn from, as its body takes them (the `checks` and
 * `reads` of `VerificationFilterBody`): the coverage read, the results read with the issue's
 * checks, and the blocks read of the Verification document, and how the three stand. They are
 * the slot's own observers, apart from `useIssueTree`'s of the same keys, so what they fetch is
 * read alone (the architect's decision 180 on dispatch://INFRA-1196).
 */
export function useVerificationReads(issueKey: string): {
  checks: readonly CheckBlock[];
  reads: VerificationReads;
} {
  // The coverage read, and the issue's checks with the results recorded against them, under the
  // keys `useIssueTree.ts` already reads them with, so the view sends no request of its own
  // (R1). The results read holds a result's commit, its verdict and its withdrawal, which the
  // tree does not carry (`verification-filter/statement-state.ts`). Staleness is per observer
  // (the architect's decision 180): with the app's 30 s (`main.tsx`), pressing the view more
  // than 30 s after the page read them read both again. Each holds what it read until an event
  // refreshes its key (`staleTime: Infinity`), and the window's focus asks again only a read in
  // error (`refetchFailedOnFocus`), as the page's reference reads do.
  const coverage = useQuery({
    ...issueCoverageQuery(issueKey),
    staleTime: Number.POSITIVE_INFINITY,
    refetchOnWindowFocus: refetchFailedOnFocus,
  });
  const results = useQuery({
    ...issueResultsQuery(issueKey),
    staleTime: Number.POSITIVE_INFINITY,
    refetchOnWindowFocus: refetchFailedOnFocus,
  });
  const checks = useMemo(() => results.data?.checks ?? [], [results.data]);
  // The blocks read of the Verification document too: a check learns the statements it serves
  // from its block's `serves` pointer, which only that read carries (`tree-drafts.ts`). Drawn
  // before it answers, every check would stand at the table's foot as one that serves no
  // statement, every statement would read Unknown, and R9's line would call the issue's own
  // checks "elsewhere" (part V's R1 and R9). The results read answers the checks of one
  // document, the issue's Verification document (`api/results.go`'s `verificationDocument`),
  // which each check names (`CheckBlock.artifact_id`) and whose blocks `useIssueTree` reads; no
  // other document's blocks place a check. The slot watches that read under the same key with
  // an observer that is never enabled, so it sends no request of its own (R1) and sees whatever
  // `useIssueTree`'s observer fetches; its `refetch` still fetches, which is what Retry asks.
  // Where the results read holds no check no blocks read is needed, and the observer of no
  // document never fetches. It is this one read and not `useIssueTree`'s `blocksHeld`
  // (decision 98), which counts all four of the tree's documents: a refused plan or decisions
  // read must not hold this view empty, and the failed line needs this read's own error and
  // Retry (the architect's decision 148).
  // This observer reads the key `useIssueTree` fills and fetches nothing of its own.
  const documentId = checks[0]?.artifact_id;
  const blocks = useQuery({ ...artifactBlocksQuery(documentId ?? ""), enabled: false });
  // Each read has answered where it holds data, and not by its status: a background refetch
  // that fails sets TanStack Query's status to error and keeps the data it last answered, and
  // the query cache keeps that data when the stage mounts this slot again as the reader comes
  // back to the view. A view drawn by `isSuccess` would empty itself on one failed refetch, and
  // draw nothing on its return for as long as the refetch kept failing, and take its tab stop
  // and the reader's presses with it. A read has failed where it holds no data and its status
  // is error (`failedWithNothing`): it was refused before it first answered. The view draws its
  // rows only once all three have answered, so it never says that an issue has no checks before
  // they are in (part V's departure 6); where a read failed it says which, each with a Retry
  // that asks that read alone again, rather than draw an empty view (the architect's decisions
  // 117 and 126 on dispatch://INFRA-1196).
  const answered =
    coverage.data !== undefined &&
    results.data !== undefined &&
    (documentId === undefined || blocks.data !== undefined);
  const failed: VerificationRead[] = [];
  if (failedWithNothing(coverage)) failed.push("coverage");
  if (failedWithNothing(results)) failed.push("results");
  if (documentId !== undefined && failedWithNothing(blocks)) failed.push("blocks");
  const reads: VerificationReads =
    failed.length > 0
      ? {
          failed,
          onRetry: (read) => {
            if (read === "coverage") void coverage.refetch();
            else if (read === "results") void results.refetch();
            else void blocks.refetch();
          },
          state: "failed",
        }
      : answered
        ? { state: "answered" }
        : { state: "waiting" };
  return { checks, reads };
}

/** Window focus asks a read again only while it is in error, so a read refused past its
 *  retries recovers on the open page while one that answered stays quiet, and it is asked even
 *  over the answer it holds, which no age makes stale (`staleTime: Infinity`). A copy of
 *  `refetchFailedOnFocus` in `features/refs/reference-target.ts`, which part F exports (its
 *  R12): the slot imports F's once F's export is under this branch (the architect's decision 94
 *  rule for copies). */
const refetchFailedOnFocus = (query: { readonly state: { readonly status: QueryStatus } }) =>
  query.state.status === "error" ? "always" : false;

/** Whether `read` failed with no answer held: a read asked again that fails keeps the answer it
 *  had, which the view still draws from, so only a read that left nothing to draw says it
 *  failed (the architect's decisions 98 and 117 on dispatch://INFRA-1196), as part J's Spec
 *  view reads its own. */
function failedWithNothing(read: { readonly data: unknown; readonly isError: boolean }): boolean {
  return read.isError && read.data === undefined;
}
