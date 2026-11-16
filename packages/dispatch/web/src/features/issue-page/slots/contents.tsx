import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { useContentsChosen } from "../contents-chosen";
import { ContentsList } from "../IssueContents";
import { IssueThreeThings } from "../IssueThreeThings";
import { issueAddress } from "../issue-address";
import { NoTooltips } from "../NoTooltips";
import { useAskOutline, useFocusBranch } from "../outline-request";
import { isFilter, presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { specSummary } from "../spec-summary";
import { useIssueTree } from "../useIssueTree";

/**
 * Filled by INFRA-1196 part J, "The Spec view": what part D's Contents pane holds from 1024 px up,
 * and its Outline sheet below that (`contents-frame.tsx`), with no view on (R1, R2). From the top:
 * the issue's three sentences under "In short" (`IssueThreeThings`), then the Contents list of
 * each question and each thread the reader began (`ContentsList`). Under a view it draws nothing:
 * the Spec view draws the three sentences as its own first group, and no other view draws them.
 *
 * The list reads the tree the outline draws (`useIssueTree`), as the Conversation view leaves it
 * with the words filter's `?q=`. An entry chosen sends the outline to its node through part I's
 * `useAskOutline()`, then closes the Outline sheet the list is drawn in (`useContentsChosen`),
 * which does nothing in the pane. Focus branch is part I's `useFocusBranch()`, so the pane and the
 * sheet share it.
 */
export function ContentsSlot({ issue, route }: SlotProps): ReactNode {
  const { hash, search } = useLocation();
  const specSlug = primarySpec(issue)?.slug ?? "";
  const address = issueAddress(route, search, hash, specSlug);
  const { blocksRead, root } = useIssueTree(issue);
  const memo = useMemo(() => specSummary(root, specSlug), [root, specSlug]);
  const view = useMemo(
    () => presetView(root, "conversation", address.filter, undefined, memo?.id),
    [address.filter, memo, root]
  );
  const ask = useAskOutline();
  const [focusBranch, setFocusBranch] = useFocusBranch();
  const chosen = useContentsChosen();
  if (isFilter(address.preset)) return null;
  return (
    <NoTooltips>
      <IssueThreeThings look="contents" reading={!blocksRead} summary={memo} />
      <ContentsList
        focusBranch={focusBranch}
        onFocusBranch={(current) => {
          setFocusBranch(!focusBranch);
          ask(current, !focusBranch);
        }}
        onGo={(id) => {
          ask(id, focusBranch);
          chosen();
        }}
        root={view.root}
      />
    </NoTooltips>
  );
}
