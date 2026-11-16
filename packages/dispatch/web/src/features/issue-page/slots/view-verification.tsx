import { useQueries, useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { artifactBlocksQuery, issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import { useAddressTarget } from "../address-target";
import { issueAddress } from "../issue-address";
import { NoTooltips } from "../NoTooltips";
import { EMPTY_STATE_LINES } from "../page-words";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { useIssueTree } from "../useIssueTree";
import { VerificationFilterBody } from "../verification-filter/VerificationFilterBody";

/** Filled by INFRA-1196 part V, "The Verification view". */
export function ViewVerificationSlot({ issue, route }: SlotProps): ReactNode {
  // The Verification view (part V's R1): the issue's one tree as the Verification view leaves
  // it, each statement with the checks that serve it and each check beside its newest result.
  const { hash, search } = useLocation();
  const address = issueAddress(route, search, hash, primarySpec(issue)?.slug);
  const { root } = useIssueTree(issue);
  // The coverage read, and the issue's checks with the results recorded against them, under the
  // keys `useIssueTree.ts` already reads them with, so the view sends no request of its own
  // (R1). The results read holds a result's commit, its verdict and its withdrawal, which the
  // tree does not carry (`verification-filter/statement-state.ts`). The view draws its rows only
  // once both have answered, so it never says that an issue has no checks before they are in
  // (part V's departure 6).
  const coverage = useQuery(issueCoverageQuery(issue.key));
  const results = useQuery(issueResultsQuery(issue.key));
  const checks = useMemo(() => results.data?.checks ?? [], [results.data]);
  // The view waits for the blocks read of each document its checks live in too: a check learns
  // the statements it serves from its block's `serves` pointer, which only that read carries
  // (`tree-drafts.ts`). Drawn before it answers, every check would stand at the table's foot as
  // one that serves no statement, every statement would read Unknown, and R9's line would call
  // the issue's own checks "elsewhere" (part V's R1 and R9). The results read names each check's
  // document (`CheckBlock.artifact_id`), the issue's Verification document, whose blocks
  // `useIssueTree` reads; no other document's blocks place a check, so no other matters here.
  // The slot watches those reads under the same keys with observers that are never enabled, so
  // they send no request of their own (R1) and see whatever `useIssueTree`'s observers fetch.
  //
  // Each read has answered where it holds data, and not by its status: a background refetch
  // that fails sets TanStack Query's status to error and keeps the data it last answered, and
  // the query cache keeps that data when the stage mounts this slot again as the reader comes
  // back to the view. A view drawn by `isSuccess` would empty itself on one failed refetch, and
  // draw nothing on its return for as long as the refetch kept failing, and take its tab stop
  // and the reader's presses with it.
  const documents = useMemo(() => [...new Set(checks.map((check) => check.artifact_id))], [checks]);
  const blocksHeld = useQueries({
    combine: (answers) => answers.every((answer) => answer.data !== undefined),
    queries: documents.map((id) => ({ ...artifactBlocksQuery(id), enabled: false })),
  });
  const answered = coverage.data !== undefined && results.data !== undefined && blocksHeld;
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
          answered={answered}
          checks={checks}
          context={view.context}
          emptyLine={
            address.filter === "" ? EMPTY_STATE_LINES.verification : EMPTY_STATE_LINES.no_match
          }
          view={view.root}
          whole={whole}
        />
      </div>
    </NoTooltips>
  );
}
