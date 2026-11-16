import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { whoAmIQuery } from "../../../api/queries";
import { type AddressTarget, issueAddress, pathToTarget } from "../issue-address";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { SpecFilterBody } from "../spec-filter/SpecFilterBody";
import { specSummary } from "../spec-summary";
import { useIssueTree } from "../useIssueTree";

/**
 * Filled by INFRA-1196 part J, "The Spec view": what the stage draws in place of the outline
 * with Spec on (`slots/stage.tsx`). It is `SpecFilterBody` over the issue's one tree as the Spec
 * view leaves it (`presetView`), at the width the approved picture draws it (the prototype's
 * `.spec`, 1080 px), and reads everything else itself: the tree (`useIssueTree`), who is reading,
 * the Contents list's last request (`useOutlineRequest`), and the node the address names, which
 * the view keeps whatever the words filter leaves and puts the focus on.
 */
export function ViewSpecSlot({ issue, route }: SlotProps): ReactNode {
  const { hash, search } = useLocation();
  const specSlug = primarySpec(issue)?.slug;
  const address = issueAddress(route, search, hash, specSlug);
  const { blocksRead, root } = useIssueTree(issue);
  // Who is reading, so a decision of theirs reads as their own words (R5).
  const viewer = useQuery(whoAmIQuery()).data?.login;
  // The address's own node, resolved against the whole tree and kept by the view. The target is
  // read by what it says rather than by the value `issueAddress` built, which is a new one on
  // every render, so the tree is walked again only when the address or the tree changes.
  const targetKind = address.target?.kind;
  const targetId = address.target?.id;
  const targetDocument = address.target?.kind === "block" ? address.target.document : undefined;
  const target = useMemo<AddressTarget | undefined>(() => {
    if (targetKind === undefined || targetId === undefined) return undefined;
    if (targetKind === "block") return { document: targetDocument, id: targetId, kind: "block" };
    return { id: targetId, kind: targetKind } as AddressTarget;
  }, [targetDocument, targetId, targetKind]);
  const revealed = useMemo(() => pathToTarget(root, target)?.at(-1)?.id, [root, target]);
  const named = useMemo(() => new Set(revealed === undefined ? [] : [revealed]), [revealed]);
  const memo = useMemo(() => specSummary(root, specSlug ?? "")?.id, [root, specSlug]);
  const view = useMemo(
    () => presetView(root, "spec", address.filter, named, memo),
    [address.filter, memo, named, root]
  );
  const request = useOutlineRequest();
  return (
    <NoTooltips>
      {/* The view's measure, the prototype's `.spec`: at most 1080 px, in the middle of the
          column; on a phone 4 px from the left edge and 10 px from the right, as the outline. */}
      <div className="mx-auto flex w-full max-w-[1080px] min-w-0 flex-col max-[760px]:-mr-1 max-[760px]:-ml-2.5 max-[760px]:w-auto">
        <SpecFilterBody
          context={view.context}
          emptyLine={address.filter === "" ? EMPTY_STATE_LINES.spec : EMPTY_STATE_LINES.no_match}
          // The words filter's Escape comes before the view is left; turning the view off with
          // Escape is part N's step of the key table.
          holdsEscape={address.filter !== ""}
          issue={issue}
          reader={viewer}
          reading={!blocksRead}
          {...(request === undefined ? {} : { request })}
          {...(revealed === undefined ? {} : { reveal: revealed })}
          root={root}
          view={view.root}
        />
      </div>
    </NoTooltips>
  );
}
