import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import type { CheckBlock } from "../../../api/types";
import { EVENT_FRESH, standsFailed } from "../../refs/reference-target";
import { useAddressTarget } from "../address-target";
import { issueAddress } from "../issue-address";
import { NoTooltips } from "../NoTooltips";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import type { TreeBlocksRead } from "../tree-read/tree-read";
import { useIssueTree } from "../useIssueTree";
import {
  type FailedRead,
  queryTyped,
  VerificationFilterBody,
  type VerificationRead,
  type VerificationReads,
} from "../verification-filter/VerificationFilterBody";

/** Filled by INFRA-1196 part V, "The Verification view". */
export function ViewVerificationSlot({ issue, route }: SlotProps): ReactNode {
  // The Verification view: the issue's one tree as the Verification view leaves it, each statement
  // with the checks that serve it and each check beside its newest result.
  const { hash, search } = useLocation();
  const address = issueAddress(route, search, hash, primarySpec(issue)?.slug);
  const { blocksOf, root } = useIssueTree(issue);
  const { checks, reads } = useVerificationReads(issue.key, blocksOf("verification"));
  // The address's own node is resolved against the whole tree and then kept by the view, so an
  // address that names a node the view holds no row for still draws it, as the outline resolves
  // it (`useAddressTarget`).
  const { named } = useAddressTarget(root, address);
  const view = useMemo(
    () => presetView(root, "verification", address.filter, named),
    [address.filter, named, root]
  );
  // The view with no words query, where a query is typed (`queryTyped`, the body's one rule): what
  // the issue holds whatever the query left of it. A query of spaces alone draws as no query does;
  // with none typed the body reads the view itself, and nothing is built a second time.
  const typed = queryTyped(address.filter);
  const whole = useMemo(
    () => (typed ? presetView(root, "verification", "", named).root : undefined),
    [named, root, typed]
  );
  return (
    <NoTooltips>
      {/* The stage's column, which the body draws its own measure in. */}
      <div className="flex w-full min-w-0 flex-col">
        <VerificationFilterBody
          checks={checks}
          context={view.context}
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
 * checks, the Verification document's blocks it hands in, and how the three stand. Both reads are
 * the slot's own observers, apart from `useIssueTree`'s of the same keys, so what they fetch is
 * read by their own options alone.
 */
export function useVerificationReads(
  issueKey: string,
  blocks: TreeBlocksRead
): {
  checks: readonly CheckBlock[];
  reads: VerificationReads;
} {
  // The coverage read, and the issue's checks with the results recorded against them, under the
  // keys `useIssueTree.ts` already reads them with, so the view sends no request of its own. The
  // results read holds a result's commit, its verdict and its withdrawal, which the tree does
  // not carry (`verification-filter/statement-state.ts`). Both take the tree's `EVENT_FRESH`
  // (held until an event refreshes their key; the window's focus asks again only a read in
  // error), as `useIssueTree`'s own observers of the same keys do, so no observer reads them
  // again by age and the view's reads cannot drift from the tree's.
  const coverage = useQuery({ ...issueCoverageQuery(issueKey), ...EVENT_FRESH });
  const results = useQuery({ ...issueResultsQuery(issueKey), ...EVENT_FRESH });
  const checks = useMemo(() => results.data?.checks ?? [], [results.data]);
  // The blocks read of the Verification document too: a check learns the statements it serves
  // from its block's `serves` pointer, which only that read carries (`tree-nodes.ts`). Drawn
  // before it answers, every check would stand at the table's foot as one that serves no
  // statement, every statement would read Unknown, and the line that counts a statement's checks
  // elsewhere would call the issue's own checks "elsewhere". The results read answers the checks
  // of one document, the issue's Verification document (`api/results.go`'s
  // `verificationDocument`), and names it in a field of its own (`verification_artifact_id`),
  // which no line here reads. It must agree with the document whose blocks
  // `useIssueTree` reads, which that hook finds from the issue read (`roleDocument`); no other
  // document's blocks place a check. The slot hands those blocks in from its `useIssueTree`
  // (`blocks`, the tree read's), so it sends no request of its own and sees whatever
  // `useIssueTree`'s observer fetches; its `refetch` still fetches, which is what Retry asks.
  // The tree read holds the statements too, so the view waits on it whether or not the results
  // read holds a check, and a read of it refused before it first answered is a line of its own:
  // the one read the blocks are in, which, refused, must not hold this
  // view empty, and the failed line needs this read's own error and Retry.
  // Each read has answered where it holds data, and not by its status: a background refetch
  // that fails sets TanStack Query's status to error and keeps the data it last answered, and
  // the query cache keeps that data when the stage mounts this slot again as the reader comes
  // back to the view. A view drawn by `isSuccess` would empty itself on one failed refetch, and
  // draw nothing on its return for as long as the refetch kept failing, and take its tab stop
  // and the reader's presses with it. A read has failed where it holds no data and a fetch of it
  // was refused, whether it is in error, runs again, or waits to run again while the browser is
  // offline (`standsFailed`, the page's one rule, which the outline uses for the same reads): it
  // was refused before it first answered. The view draws its rows only once all three have
  // answered, so it never says that an issue has no checks before they are in; where a read
  // failed it says which, each with a Retry that asks that read alone again, rather than draw an
  // empty view that gives the reader nothing to act on.
  const answered =
    coverage.data !== undefined &&
    results.data !== undefined &&
    // The blocks are the tree read's, which holds the statements too, so it is waited on always.
    blocks.data !== undefined;
  // Each read that failed with no answer held, by its name, with the press that asks that read
  // alone again and whether it runs again; the body says them in an order of its own.
  const failed: Partial<Record<VerificationRead, FailedRead>> = {};
  if (standsFailed(coverage)) failed.coverage = failedRead(coverage);
  if (standsFailed(results)) failed.results = failedRead(results);
  if (standsFailed(blocks)) failed.blocks = failedRead(blocks);
  const reads: VerificationReads =
    Object.keys(failed).length > 0
      ? { failed, state: "failed" }
      : answered
        ? { state: "answered" }
        : { state: "waiting" };
  return { checks, reads };
}

/** What the line of a read that failed is given: which refusal it says, the read's own count of
 *  its refusals (`errorUpdateCount`), as the outline gives it (`outline-reads.ts`); the press
 *  that asks that read alone again; and whether it runs again (`isFetching`), while which the
 *  line's Retry asks nothing more. */
function failedRead(read: {
  readonly errorUpdateCount: number;
  readonly isFetching: boolean;
  readonly refetch: () => Promise<unknown>;
}): FailedRead {
  return {
    failure: read.errorUpdateCount,
    retry: () => void read.refetch(),
    retrying: read.isFetching,
  };
}
