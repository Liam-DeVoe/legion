import type { ReactNode } from "react";

import type { Artifact } from "../../api/types";
import { ArtifactDocument } from "../artifacts/ArtifactDocument";
import type { DocumentToolbar } from "../doc/ProofDocument";
import { DocumentStage, STAGE_IN_PANEL, useDocumentStage } from "./document-stage";

/**
 * One document's editor, on that document's own stage (dispatch://INFRA-1196 R16,
 * `document-stage.tsx`).
 *
 * The body draws it, through `IssuePresetPanels.tsx`, as the page's one editor of the document
 * a row of the outline is being edited in: one document has one room, so the row takes this
 * editor onto its own stage rather than opening a second connection to the room. Opening a
 * document at its own address is part N's.
 *
 * `resting` says whether the document's stage rests here while no row of the tree holds it.
 * The editor for an edited row rests nowhere, because that row is holding the stage itself.
 */
export function StagedDocument({
  artifact,
  askId,
  commentId,
  highlightTerm,
  isClosed,
  issueKey,
  onToolbarChange,
  onVersionChange,
  resting = true,
  showDiff,
  version,
}: {
  artifact: Artifact;
  /** The ask an address names for this document's editor to land on (`issue-address.ts`'s
   *  `documentItem`). */
  askId?: string;
  /** The comment an address names the same way. */
  commentId?: string;
  /** What a `?q=` search term marks in the document's text. */
  highlightTerm?: string;
  isClosed: boolean;
  issueKey: string;
  onToolbarChange: (toolbar: DocumentToolbar | undefined) => void;
  onVersionChange: (version: number | null) => void;
  resting?: boolean;
  showDiff: boolean;
  /** The version the address pins, or undefined for the document as it stands. */
  version?: number;
}): ReactNode {
  const stage = useDocumentStage(artifact.id, STAGE_IN_PANEL);
  return (
    <>
      {resting ? <div className="flex min-w-0 flex-col" ref={stage} /> : null}
      <DocumentStage artifactId={artifact.id}>
        <ArtifactDocument
          artifact={artifact}
          askId={askId}
          commentId={commentId}
          highlightTerm={highlightTerm}
          isClosed={isClosed}
          onToolbarChange={onToolbarChange}
          onVersionChange={onVersionChange}
          owner={{ key: issueKey, kind: "issue" }}
          showDiff={showDiff}
          version={version}
        />
      </DocumentStage>
    </>
  );
}
