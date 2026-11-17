import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec, roleDocument } from "../../../api/issue-cache";
import { artifactBlocksQuery } from "../../../api/queries";
import { useAddressTarget } from "../address-target";
import { useOutlineEditing } from "../IssuePresetPanels";
import { issueAddress } from "../issue-address";
import { artifactsBySlug } from "../issue-artifacts";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { PlanFilterBody, type PlanRead, type PlanReads } from "../plan-filter/PlanFilterBody";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { useIssueTree } from "../useIssueTree";

/**
 * Filled by INFRA-1196 part K, "The Plan view" (dispatch://INFRA-3938): the plan's tasks as a
 * chart of time, with one control in the view's first line that draws them as ordered steps
 * instead (`plan-filter/PlanFilterBody.tsx`). The steps are the outline's own rows, so their
 * props are built as `slots/body.tsx` builds the outline's, from F's Plan view of the issue's
 * one tree (`presetView`), and a row of them is edited in its document's one editor as a row of
 * the outline is (part I's `useOutlineEditing`). Two things differ from the outline's
 * (departure 2 of dispatch://INFRA-3938's plan): the address's node is the view's only where it
 * is a task, and the view is handed the whole plan beside what the words filter left of it, for
 * the order.
 */
export function ViewPlanSlot({ issue, route }: SlotProps): ReactNode {
  const { hash, search } = useLocation();
  const spec = primarySpec(issue);
  const address = issueAddress(route, search, hash, spec?.slug);
  const { root } = useIssueTree(issue);
  // The view waits on the reads of the two documents it draws from, and on no other: the plan's
  // blocks, which are its tasks, and the spec's, which are the statements a task serves
  // (dispatch://INFRA-3938 R1 and R4). It is these two reads and not `useIssueTree`'s
  // `blocksHeld` (decision 98 of dispatch://INFRA-1196), which counts the Verification and
  // decisions documents too: one of those made while the reader is on Plan opens a read that
  // holds nothing yet, and one whose first read is refused holds nothing at all, and neither
  // changes anything in Plan; and a failed line needs the failed read's own error and Retry
  // (decision 148 of dispatch://INFRA-1196, as part V reads its own). Each read is watched under
  // the key `useIssueTree` fills, by an observer that is never enabled, so the view sends no
  // request of its own (R2) and sees whatever the tree's observer fetches; its `refetch` still
  // fetches, which is what Retry asks. A document the issue does not name needs no read.
  //
  // A read has answered where it holds data, and not by its status: a refetch that fails keeps
  // the data it last answered (decision 98), so a view drawn by `isSuccess` would empty itself
  // on one failed refetch. A read has failed where it holds no data and its status is error
  // (`failedWithNothing`): it was refused before it first answered. The view draws its tasks,
  // its switch and its empty line once both have answered, so it never says the issue has no
  // plan tasks before they are in (R1; root dispatch://INFRA-1196 R26), and where a read failed
  // it says which, each with a Retry that asks that read alone again (`PlanFilterBody`). The
  // view stays mounted through all of it, as the outline does (`slots/body.tsx`), so no read
  // takes from the reader the press of "Steps", the focused row and its mark, a step being
  // edited, or the address's step having taken the focus, which it takes once (R6 and R7).
  const planId = roleDocument(issue, "plan")?.id;
  // This observer reads the key `useIssueTree` fills and fetches nothing of its own.
  const planBlocks = useQuery({ ...artifactBlocksQuery(planId ?? ""), enabled: false });
  const specId = spec?.id;
  // This observer reads the key `useIssueTree` fills and fetches nothing of its own.
  const specBlocks = useQuery({ ...artifactBlocksQuery(specId ?? ""), enabled: false });
  const answered =
    (planId === undefined || planBlocks.data !== undefined) &&
    (specId === undefined || specBlocks.data !== undefined);
  const failed: PlanRead[] = [];
  if (planId !== undefined && failedWithNothing(planBlocks)) failed.push("plan");
  if (specId !== undefined && failedWithNothing(specBlocks)) failed.push("statements");
  const reads: PlanReads =
    failed.length > 0
      ? {
          failed,
          onRetry: (read) => {
            if (read === "plan") void planBlocks.refetch();
            else void specBlocks.refetch();
          },
          state: "failed",
        }
      : answered
        ? { state: "answered" }
        : { state: "waiting" };
  // The address's own node, resolved against the whole tree as the outline resolves it
  // (`useAddressTarget`, decision 94 of dispatch://INFRA-1196; `slots/body.tsx`): a step it names
  // takes the focus (R7).
  const found = useAddressTarget(root, address).reveal;
  // Plan draws no node but a task (dispatch://INFRA-3938 R1, decision 1 of its plan; root
  // dispatch://INFRA-1196 R26), so the address's node is kept in the view, and given the focus,
  // only where it is a task. `presetView` keeps a named node of any kind (`presets.ts`'s
  // `filterView`), as `AddressedNode.named` names it: handed every node, `/plan?comment=<id>`
  // would draw the comment as a bar and `/plan#b-<statement>` the statement as a bar with its
  // tasks under it, out of the chart. Handed the node only where it is a task, both draw every
  // task and no other node, as `/plan` does.
  const reveal = useMemo(() => (found?.at(-1)?.type === "plan_task" ? found : undefined), [found]);
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  // The whole plan's tasks, with no words filter on: the order and each bar's start are worked
  // out from every task of the plan, and the words filter chooses which of its places the view
  // draws (dispatch://INFRA-3938 R2 and R4, decision 3 of its plan). With no words filter on it
  // is the view's own.
  const whole = useMemo(() => presetView(root, "plan", "", named), [named, root]);
  const view = useMemo(
    () => (address.filter === "" ? whole : presetView(root, "plan", address.filter, named)),
    [address.filter, named, root, whole]
  );
  const plan = whole.root.children;
  const artifacts = useMemo(() => artifactsBySlug(issue), [issue]);
  const request = useOutlineRequest();
  // The document a step is being edited in, and that document's one editor, which the view draws
  // for the row to take onto its own stage: part I's `useOutlineEditing`, as the outline's body
  // reads it (`slots/body.tsx`, decision 94 of dispatch://INFRA-1196).
  const { onEditing, panels } = useOutlineEditing(issue);
  const isClosed = issue.closed_at !== null;
  return (
    <NoTooltips>
      {/* The view's box, the prototype's `.plan`: at most 1300 px, in the middle of the column,
          and at 760 px and below the column less 10 px at each side (dispatch://INFRA-3938 R1
          and its Sizes). That is `max-[761px]:`, since Tailwind writes `max-[N]:` as
          `(width < N)` and 760 px itself is a phone (decision 72 of dispatch://INFRA-1196). */}
      <div className="mx-auto flex w-full max-w-[1300px] min-w-0 flex-col max-[761px]:max-w-[calc(100%-20px)]">
        <PlanFilterBody
          artifacts={artifacts}
          context={view.context}
          emptyLine={address.filter === "" ? EMPTY_STATE_LINES.plan : EMPTY_STATE_LINES.no_match}
          filtering={view.filtering}
          focusReveal
          // The words filter's Escape comes before the view is left (part N).
          holdsEscape={address.filter !== ""}
          isClosed={isClosed}
          onEditing={onEditing}
          plan={plan}
          reads={reads}
          {...(request === undefined ? {} : { request })}
          reveal={reveal}
          root={view.root}
          rootRow={view.rootRow}
        />
      </div>
      {panels}
    </NoTooltips>
  );
}

/** Whether `read` failed with no answer held: a read asked again that fails keeps the answer it
 *  had, which the view still draws from, so only a read that left nothing to draw says it
 *  failed (decisions 98 and 148 of dispatch://INFRA-1196), as part V's view reads its own. */
function failedWithNothing(read: { readonly data: unknown; readonly isError: boolean }): boolean {
  return read.isError && read.data === undefined;
}
