import { type ReactNode, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { buildIssuePath } from "../../refs/routes";
import { IssuePresetPanels } from "../IssuePresetPanels";
import { IssueTree } from "../IssueTree";
import { type AddressTarget, issueAddress, pathToTarget } from "../issue-address";
import { artifactsBySlug } from "../issue-artifacts";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { specSummary } from "../spec-summary";
import { useDocumentToolbars } from "../useDocumentToolbars";
import { useIssueTree } from "../useIssueTree";

/** Filled by INFRA-1196 part G, "A node's row", then part I, "The outline". */
export function BodySlot({ issue, route }: SlotProps): ReactNode {
  // The outline (part I's R3): the issue's one tree as the Conversation view leaves it, folded to
  // the frontier, in place of part G's open rows. The spec's summary callout is no row of it: the
  // Spec view draws it as the issue's three sentences.
  const navigate = useNavigate();
  const { hash, search } = useLocation();
  const specSlug = primarySpec(issue)?.slug;
  const address = issueAddress(route, search, hash, specSlug);
  const { root } = useIssueTree(issue);
  // The address's own node is resolved against the whole tree and then kept by the view, so an
  // address that names a node the words filter would not have matched still draws it. The
  // target is read by what it says rather than by the value `issueAddress` built, which is a new
  // one on every render: the tree is walked again only when the address or the tree changes.
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
  // Every target the address names, `/children` among them, is unfolded and focused (R6, R20).
  const reveal = useMemo(() => pathToTarget(root, target), [root, target]);
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  const memo = useMemo(() => specSummary(root, specSlug ?? "")?.id, [root, specSlug]);
  const view = useMemo(
    () => presetView(root, "conversation", address.filter, named, memo),
    [address.filter, memo, named, root]
  );
  const artifacts = useMemo(() => artifactsBySlug(issue), [issue]);
  const request = useOutlineRequest();
  const { toolbarSink } = useDocumentToolbars();
  // The document a row of the outline is being edited in, so the body draws that document's one
  // editor for the row to take onto its own stage (`IssuePresetPanels`, `document-stage.tsx`).
  const [editing, setEditing] = useState<string | undefined>(undefined);
  const edited =
    editing === undefined
      ? undefined
      : issue.artifacts?.find((artifact) => artifact.id === editing);
  const isClosed = issue.closed_at !== null;
  return (
    <NoTooltips>
      {/* The outline's measure, the prototype's `#pane`: at most 1000 px, in the middle of the
          column. On a phone 4 px from the left edge and 10 px from the right, as the
          prototype's `#reading` is there, so the bullets keep the picture's place. */}
      <div className="mx-auto flex w-full max-w-[1000px] min-w-0 flex-col max-[761px]:-mr-1 max-[761px]:-ml-2.5 max-[761px]:w-auto">
        <IssueTree
          artifacts={artifacts}
          context={view.context}
          emptyLine={
            address.filter === "" ? EMPTY_STATE_LINES.conversation : EMPTY_STATE_LINES.no_match
          }
          filtering={view.filtering}
          focusReveal
          // The words filter's Escape comes before the outline is left (part N).
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
