import { useQueries } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { artifactBlocksQuery } from "../../../api/queries";
import { buildIssuePath } from "../../refs/routes";
import { useAddressTarget } from "../address-target";
import { IssuePresetPanels } from "../IssuePresetPanels";
import { issueAddress } from "../issue-address";
import { artifactsBySlug } from "../issue-artifacts";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { PlanFilterBody } from "../plan-filter/PlanFilterBody";
import { planDocumentIds } from "../plan-filter/plan-order";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { useToolbarSink } from "../useDocumentToolbars";
import { useIssueTree } from "../useIssueTree";

/**
 * Filled by INFRA-1196 part K, "The Plan view" (dispatch://INFRA-3938): the plan's tasks as a
 * chart of time, with one control in the view's first line that draws them as ordered steps
 * instead (`plan-filter/PlanFilterBody.tsx`). The steps are the outline's own rows, so their
 * props are built as `slots/body.tsx` builds the outline's, from F's Plan view of the issue's
 * one tree (`presetView`), and a row of them is edited in its document's one editor as a row of
 * the outline is (`IssuePresetPanels`). Two things differ from the outline's (departure 2 of
 * dispatch://INFRA-3938's plan): the address's node is the view's only where it is a task, and
 * the view is handed the whole plan beside what the words filter left of it, for the order.
 */
export function ViewPlanSlot({ issue, route }: SlotProps): ReactNode {
  const navigate = useNavigate();
  const { hash, search } = useLocation();
  const specSlug = primarySpec(issue)?.slug;
  const address = issueAddress(route, search, hash, specSlug);
  const { root } = useIssueTree(issue);
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
  // The sink alone, which never changes, as the outline's body reads it (`slots/body.tsx`): the
  // toolbars it fills are no part of the view, so each one an editor publishes while a step is
  // edited draws neither the view nor its tree again.
  const toolbarSink = useToolbarSink();
  // The document a step is being edited in, whose one editor the view draws for the row to take
  // onto its own stage (`IssuePresetPanels`, `document-stage.tsx`).
  const [editing, setEditing] = useState<string | undefined>(undefined);
  const edited =
    editing === undefined
      ? undefined
      : issue.artifacts?.find((artifact) => artifact.id === editing);
  const isClosed = issue.closed_at !== null;
  // The view is drawn once every blocks read of the documents it draws from holds data: the
  // issue's spec, whose statements the tasks serve, and its plan, whose blocks are the tasks,
  // each the one the issue read names by its id, as the tree chooses them (`planDocumentIds`,
  // `useIssueTree.ts`'s `treeDocumentIds`). Before then the plan holds no task, or tasks that
  // serve no statement, and the view would say the issue has no task and draw no switch
  // (dispatch://INFRA-3938 R1; root dispatch://INFRA-1196 R26); a first read that failed holds
  // no data either, so the box stays empty rather than say that. The rule reads the blocks held,
  // not the last read's success: a blocks query keeps its data while it reads again, and after a
  // background read fails, which TanStack marks as no success while it keeps the data. So no
  // such read takes the view down, with the reader's press of "Steps" and the focus (R6, R7),
  // and the view drawn again on a return to Plan draws the blocks held. The reads are
  // `useIssueTree`'s, under the same keys and for the same documents, so the view sends no
  // request of its own (dispatch://INFRA-3938 R2). An issue with neither document has no read
  // to wait for, so its view is drawn at once and draws its empty line.
  //
  // This reading is the Plan view's stopgap for decision 98 of dispatch://INFRA-1196, under
  // which a view draws from part F's `IssueTreeView.blocksHeld`, true while every blocks query
  // of the tree holds data, and keeps no reading of its own. That field does not exist at part
  // K's base, so `useIssueTree(issue).blocksHeld` replaces this reading once part F's commit
  // 74b90a32 is under that base.
  const drawn = useQueries({
    combine: (answers) => answers.every((answer) => answer.data !== undefined),
    queries: planDocumentIds(issue).map((id) => artifactBlocksQuery(id)),
  });
  return (
    <NoTooltips>
      {/* The view's box, the prototype's `.plan`: at most 1300 px, in the middle of the column,
          and at 760 px and below the column less 10 px at each side (dispatch://INFRA-3938 R1
          and its Sizes). That is `max-[761px]:`, since Tailwind writes `max-[N]:` as
          `(width < N)` and 760 px itself is a phone (decision 72 of dispatch://INFRA-1196).
          It draws nothing until the blocks it draws from are held (`drawn`, above). */}
      <div className="mx-auto flex w-full max-w-[1300px] min-w-0 flex-col max-[761px]:max-w-[calc(100%-20px)]">
        {drawn ? (
          <PlanFilterBody
            artifacts={artifacts}
            context={view.context}
            emptyLine={address.filter === "" ? EMPTY_STATE_LINES.plan : EMPTY_STATE_LINES.no_match}
            filtering={view.filtering}
            focusReveal
            // The words filter's Escape comes before the view is left (part N).
            holdsEscape={address.filter !== ""}
            isClosed={isClosed}
            onEditing={setEditing}
            plan={plan}
            {...(request === undefined ? {} : { request })}
            reveal={reveal}
            root={view.root}
            rootRow={view.rootRow}
          />
        ) : null}
      </div>
      <IssuePresetPanels
        editedDocument={edited}
        isClosed={isClosed}
        issueKey={issue.key}
        onToolbarSink={toolbarSink}
        // A version picked in the edited document's own editor opens that document at it.
        onVersionChange={(artifact, version) =>
          navigate(
            buildIssuePath({
              key: issue.key,
              kind: "artifact",
              slug: artifact.slug,
              ...(version === null ? {} : { version }),
            })
          )
        }
      />
    </NoTooltips>
  );
}
