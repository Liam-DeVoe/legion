import { useQueries } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { api } from "../../../api/client";
import { primarySpec } from "../../../api/issue-cache";
import { issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { buildIssuePath } from "../../refs/routes";
import { useAddressTarget } from "../address-target";
import { IssuePresetPanels } from "../IssuePresetPanels";
import { IssueTree } from "../IssueTree";
import { issueAddress } from "../issue-address";
import { artifactsBySlug } from "../issue-artifacts";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { specSummary } from "../spec-summary";
import { useToolbarSink } from "../useDocumentToolbars";
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
  const { blocksHeld, root } = useIssueTree(issue);
  // Whether every read the tree's nodes come from holds its answer: the issue's children come
  // with the issue, the documents' blocks are `blocksHeld`, and every other node comes from one
  // of these, read under the keys `useIssueTree` reads them by, so they cost no request of their
  // own. Until then a tree with no node is one whose nodes have not arrived, and the outline's
  // line that the issue holds nothing would be false on every cold load, so the outline draws
  // nothing in its place. A read that failed holds nothing either: the page cannot say what it
  // would have held.
  const key = issue.key;
  const nodesHeld = useQueries({
    combine: (answers) => answers.every((answer) => answer.data !== undefined),
    queries: [
      issueCoverageQuery(key),
      issueDecisionsQuery(key, true),
      issueResultsQuery(key),
      { queryFn: () => api.listIssueAsks(key, "all"), queryKey: ["asks", key] },
      { queryFn: () => api.listComments(key), queryKey: ["comments", key] },
      { queryFn: () => api.listIssueMessages(key), queryKey: ["issue-messages", key] },
    ],
  });
  // The address's own node is resolved against the whole tree and then kept by the view, so an
  // address that names a node the words filter would not have matched still draws it.
  const { named, reveal } = useAddressTarget(root, address);
  const memo = useMemo(() => specSummary(root, specSlug ?? "")?.id, [root, specSlug]);
  const view = useMemo(
    () => presetView(root, "conversation", address.filter, named, memo),
    [address.filter, memo, named, root]
  );
  const artifacts = useMemo(() => artifactsBySlug(issue), [issue]);
  const request = useOutlineRequest();
  // The sink alone, which never changes: the toolbars it fills are no part of the outline, so
  // each one an editor publishes while a row is edited draws nothing here again.
  const toolbarSink = useToolbarSink();
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
          {...(nodesHeld && blocksHeld
            ? {
                emptyLine:
                  address.filter === ""
                    ? EMPTY_STATE_LINES.conversation
                    : EMPTY_STATE_LINES.no_match,
              }
            : {})}
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
