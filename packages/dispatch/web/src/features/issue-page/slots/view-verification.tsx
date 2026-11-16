import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { issueResultsQuery } from "../../../api/queries";
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
  // The issue's checks with the results recorded against them, under the key `useIssueTree.ts`
  // already reads them with, so the view sends no request of its own (R1): it is what holds a
  // result's commit, its verdict and its withdrawal, which the tree does not carry
  // (`verification-filter/statement-state.ts`).
  const results = useQuery(issueResultsQuery(issue.key)).data;
  const checks = useMemo(() => results?.checks ?? [], [results]);
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
  return (
    <NoTooltips>
      {/* The view's own measure, as the checkpoint's `IssueBody.tsx` drew it under this view: at
          most 1180 px (the prototype's `.ver`), in the middle of the column. On a phone 4 px
          from the left edge and 10 px from the right, as the outline's measure is there. */}
      <div className="mx-auto flex w-full max-w-[1180px] min-w-0 flex-col max-[760px]:-mr-1 max-[760px]:-ml-2.5 max-[760px]:w-auto">
        <VerificationFilterBody
          checks={checks}
          context={view.context}
          emptyLine={
            address.filter === "" ? EMPTY_STATE_LINES.verification : EMPTY_STATE_LINES.no_match
          }
          view={view.root}
        />
      </div>
    </NoTooltips>
  );
}
