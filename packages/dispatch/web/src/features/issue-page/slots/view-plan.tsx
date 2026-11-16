import { type ReactNode, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { buildIssuePath } from "../../refs/routes";
import { IssuePresetPanels } from "../IssuePresetPanels";
import { type AddressTarget, issueAddress, pathToTarget } from "../issue-address";
import { artifactsBySlug } from "../issue-artifacts";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { PlanFilterBody } from "../plan-filter/PlanFilterBody";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { useDocumentToolbars } from "../useDocumentToolbars";
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
  // The address's own node, resolved against the whole tree and read by what it says, as the
  // outline resolves it (`slots/body.tsx`): a step it names takes the focus (R7).
  const targetKind = address.target?.kind;
  const targetId = address.target?.id;
  const targetDocument = address.target?.kind === "block" ? address.target.document : undefined;
  const target = useMemo<AddressTarget | undefined>(() => {
    if (targetKind === undefined || targetId === undefined) return undefined;
    if (targetKind === "block") return { document: targetDocument, id: targetId, kind: "block" };
    return { id: targetId, kind: targetKind } as AddressTarget;
  }, [targetDocument, targetId, targetKind]);
  const found = useMemo(() => pathToTarget(root, target), [root, target]);
  // Plan draws no node but a task (dispatch://INFRA-3938 R1, decision 1 of its plan; root
  // dispatch://INFRA-1196 R26), so the address's node is kept in the view, and given the focus,
  // only where it is a task. `presetView` keeps a named node of any kind (`presets.ts`'s
  // `filterView`): handed every node, `/plan?comment=<id>` drew the comment as a bar and
  // `/plan#b-<statement>` the statement as a bar with its tasks under it, out of the chart. Now
  // both draw every task and no other node, as `/plan` does.
  const reveal = useMemo(() => (found?.at(-1)?.type === "plan_task" ? found : undefined), [found]);
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  const view = useMemo(
    () => presetView(root, "plan", address.filter, named),
    [address.filter, named, root]
  );
  // The whole plan's tasks, with no words filter on: the order and each bar's start are worked
  // out from every task of the plan, and the words filter chooses which of its places the view
  // draws (dispatch://INFRA-3938 R2 and R4, decision 3 of its plan). With no words filter on it
  // is the view's own.
  const plan = useMemo(
    () =>
      address.filter === ""
        ? view.root.children
        : presetView(root, "plan", "", named).root.children,
    [address.filter, named, root, view.root.children]
  );
  const artifacts = useMemo(() => artifactsBySlug(issue), [issue]);
  const request = useOutlineRequest();
  const { toolbarSink } = useDocumentToolbars();
  // The document a step is being edited in, whose one editor the view draws for the row to take
  // onto its own stage (`IssuePresetPanels`, `document-stage.tsx`).
  const [editing, setEditing] = useState<string | undefined>(undefined);
  const edited =
    editing === undefined
      ? undefined
      : issue.artifacts?.find((artifact) => artifact.id === editing);
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
          onEditing={setEditing}
          plan={plan}
          {...(request === undefined ? {} : { request })}
          reveal={reveal}
          root={view.root}
          rootRow={view.rootRow}
        />
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
