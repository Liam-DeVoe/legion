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
 * the outline is (`IssuePresetPanels`).
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
  const reveal = useMemo(() => pathToTarget(root, target), [root, target]);
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  const view = useMemo(
    () => presetView(root, "plan", address.filter, named),
    [address.filter, named, root]
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
