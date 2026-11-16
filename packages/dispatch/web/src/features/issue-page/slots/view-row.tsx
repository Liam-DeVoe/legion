import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { whoAmIQuery } from "../../../api/queries";
import { pageLineBorder, pagePaperBg } from "../../../theme/classes";
import { isViewer } from "../../refs/actor";
import { useMediaQuery } from "../../shell/useDialog";
import { CONTENTS_PANE_QUERY, OutlineSheet, PAGE_INSET } from "../contents-frame";
import { IssuePresets } from "../IssuePresets";
import { issueAddress } from "../issue-address";
import type { TreeNode } from "../issue-tree";
import { filterSignals, isFilter, presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { useIssueKeymap } from "../useIssueKeymap";
import { useIssueTree } from "../useIssueTree";

/**
 * The view row (dispatch://INFRA-1196 R2, R11): the five view controls and Keys
 * (`IssuePresets.tsx`), each control's signal read off the issue's one tree, and below 1024 px,
 * with no view on, the Outline control beside the one control (`contents-frame.tsx`). It binds
 * the views' chords (`useIssueKeymap.ts`).
 *
 * The row's rule runs the column's whole width, as the prototype's `#tabs` does, and its
 * controls keep the page's inset. At 760 px and below it stays at the top of the screen as the
 * page scrolls; above, it scrolls away with the header.
 */
export function ViewRowSlot(props: SlotProps): ReactNode {
  const { issue, route } = props;
  const { hash, search } = useLocation();
  const address = issueAddress(route, search, hash, primarySpec(issue)?.slug);
  const { root } = useIssueTree(issue);
  // Who the reader is, which Conversation's count is read for: a thread is counted where the
  // reader began it.
  const viewer = useQuery(whoAmIQuery()).data?.login;
  // What each control draws after its name: a view's count of its main kind and what of it asks
  // something of the reader, and for Conversation how many entries the Contents list holds, as
  // the prototype's signal counts its branches. Each follows `?q=`.
  const signals = useMemo(
    () => ({
      ...filterSignals(root, address.filter),
      conversation: {
        count: contentsEntries(presetView(root, "conversation", address.filter).root, viewer),
      },
    }),
    [address.filter, root, viewer]
  );
  const paned = useMediaQuery(CONTENTS_PANE_QUERY);
  useIssueKeymap(issue.key);
  return (
    <div
      className={`border-b ${PAGE_INSET} ${pageLineBorder} ${pagePaperBg} max-[760px]:sticky max-[760px]:top-0 max-[760px]:z-[25]`}
    >
      <IssuePresets
        active={address.preset}
        issueKey={issue.key}
        outline={paned || isFilter(address.preset) ? undefined : <OutlineSheet slot={props} />}
        signals={signals}
      />
    </div>
  );
}

/**
 * How many entries the Contents list draws for `node`'s branch: each ask, and each comment or
 * message the reader wrote that begins a thread, at any depth. A node drawn under a second
 * parent is counted once, at its own place: a transcluded copy and the branch under it are the
 * same nodes as at the edited place.
 */
function contentsEntries(node: TreeNode, viewer: string | undefined): number {
  let entries = 0;
  for (const child of node.children) {
    if (child.transcluded) continue;
    const begunByReader =
      (child.type === "comment" || child.type === "message") &&
      node.type !== "comment" &&
      node.type !== "message" &&
      child.author !== undefined &&
      isViewer(child.author, viewer);
    if (child.type === "ask" || begunByReader) entries += 1;
    entries += contentsEntries(child, viewer);
  }
  return entries;
}
