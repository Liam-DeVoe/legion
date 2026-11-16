import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import { type AddressTarget, issueAddress, pathToTarget } from "../issue-address";
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
  // A read has answered where it holds data, and not by its status: a background refetch that
  // fails sets TanStack Query's status to error and keeps the data it last answered, and a view
  // drawn by `isSuccess` would empty itself on one failed refetch and take its tab stop and the
  // reader's presses with it.
  const answered = coverage.data !== undefined && results.data !== undefined;
  const checks = useMemo(() => results.data?.checks ?? [], [results.data]);
  // The address's own node is resolved against the whole tree and then kept by the view, so an
  // address that names a node the view holds no row for still draws it (R1). The target is read
  // by what it says rather than by the value `issueAddress` built, which is a new one on every
  // render: the tree is walked again only when the address or the tree changes.
  const targetKind = address.target?.kind;
  const targetId = address.target?.id;
  // Which document a block target belongs to, where its address names one: a block id alone
  // names a block of any document of the issue (`pathToTarget`).
  const targetDocument = address.target?.kind === "block" ? address.target.document : undefined;
  const target = useMemo<AddressTarget | undefined>(() => {
    if (targetKind === undefined || targetId === undefined) return undefined;
    if (targetKind === "block") return { document: targetDocument, id: targetId, kind: "block" };
    return { id: targetId, kind: targetKind } as AddressTarget;
  }, [targetDocument, targetId, targetKind]);
  const named = useMemo(() => {
    const node = pathToTarget(root, target)?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [root, target]);
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
