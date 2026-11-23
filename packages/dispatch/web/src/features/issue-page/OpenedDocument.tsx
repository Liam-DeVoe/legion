import { type ReactNode, useContext, useEffect, useMemo, useRef } from "react";
import { useNavigate } from "react-router-dom";

import type { Artifact } from "../../api/types";
import {
  focusVisibleRing,
  secondaryButtonBorder,
  secondaryButtonHoverBorder,
  secondaryButtonText,
} from "../../theme/classes";
import { ArtifactDocument } from "../artifacts/ArtifactDocument";
import type { DocumentToolbar } from "../doc/ProofDocument";
import { DocumentRuntime, type DocumentRuntimeValue } from "../doc/runtime";
import type { IssueRoute } from "../refs/routes";
import { useKeymap } from "../shell/keymap";
import { ArtifactRoutePanel } from "./ArtifactRoutePanel";
import { DocumentStage, useDocumentStage } from "./document-stage";
import { pageChrome } from "./page-structure";
import { blockAttributeLabel, CONTROL_LABELS, nodeTypeName } from "./page-words";
import { EditorBox } from "./StagedDocument";

/** The page's words for a typed block's type and its attributes (`page-words.ts`), which the
 *  opened document draws in place of the schema's tokens (part N's R9, R12). */
const BLOCK_NAMES = { attribute: blockAttributeLabel, type: nodeTypeName } as const;

/**
 * A document or file of the issue, opened in the main column in place of the outline
 * (dispatch://INFRA-1196 R3): what its own address `/issues/<key>/artifacts/<slug>` draws, and
 * what choosing it in the node menu opens. A document is drawn with its editor, so the caret,
 * the collaborative cursors and the comment-on-selection chord work in it as they do anywhere a
 * document is edited; its versions, their downloads and what references it are drawn under it.
 *
 * Escape returns to the outline, in the key table's order (the Design's keyboard table): a
 * caret in the document's text is left first, and the next Escape closes the document. The
 * editor takes Escape for itself before the page's keyboard registry sees it, so the caret is
 * left by a listener of this box's own in the capture phase, as a row of the outline leaves its
 * caret (`useNodeEditing.ts`); the close is the registry's, which a menu or a dialog that is
 * open over the page answers first.
 *
 * An ask or comment `?mark=` names is resolved to its whole id before this is drawn
 * (`slots/stage.tsx`), so the document's own landing reads it under that id and writes no
 * address of its own.
 */
export function OpenedDocument({
  artifact,
  artifactRoute,
  highlightTerm,
  isClosed,
  issueKey,
  item,
  onShowDiff,
  onToolbarChange,
  onVersionChange,
  outline,
  showDiff,
}: {
  artifact: Artifact | undefined;
  artifactRoute: Extract<IssueRoute, { kind: "artifact" }>;
  highlightTerm: string | undefined;
  isClosed: boolean;
  issueKey: string;
  /** The ask or comment the address names for the document to land on, marked in its text
   *  (`issue-address.ts`'s `documentItem`). */
  item: { readonly kind: "ask" | "comment"; readonly id: string } | undefined;
  onShowDiff: (next: boolean) => void;
  onToolbarChange: (next: DocumentToolbar | undefined) => void;
  onVersionChange: (version: number | null) => void;
  /** The address of the outline the reader returns to. */
  outline: string;
  showDiff: boolean;
}): ReactNode {
  const navigate = useNavigate();
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const held = box.current;
    if (held === null) return;
    const leave = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !active.isContentEditable) return;
      event.preventDefault();
      active.blur();
      held.focus({ preventScroll: true });
    };
    held.addEventListener("keydown", leave, true);
    return () => held.removeEventListener("keydown", leave, true);
  }, []);

  // The document's typed blocks are drawn in the page's words: the editor this page's runtime
  // makes is given them once it is made (`ask-block.ts`'s `nameTypedBlocks`). `ask-block.ts` is
  // imported here as `editor.ts` imports it, with `import()`: it is written against the editor's
  // library, which a static import would put in the page's own chunk before its first paint. By
  // the time an editor is made, that lazy chunk is loaded.
  const runtime = useContext(DocumentRuntime);
  const namedRuntime = useMemo<DocumentRuntimeValue>(
    () => ({
      ...runtime,
      createEditor: async (root, options) => {
        const handle = await runtime.createEditor(root, options);
        const { nameTypedBlocks } = await import("../doc/ask-block");
        nameTypedBlocks(handle.view, options.blockSchema, BLOCK_NAMES);
        return handle;
      },
    }),
    [runtime]
  );

  useKeymap("issue", [
    {
      id: "close-document",
      keys: "Escape",
      label: CONTROL_LABELS.close_document,
      palette: false,
      run: () => navigate(outline),
      // A menu open over the document closes first, on the same press (`IssuePresets.tsx`).
      when: () => document.querySelector('[role="menu"]') === null,
    },
  ]);

  return (
    <div className="flex min-w-0 flex-col gap-3 outline-none" ref={box} tabIndex={-1}>
      <div>
        <button
          className={`inline-flex min-h-11 items-center rounded-lg px-2 text-sm font-medium md:min-h-9 ${secondaryButtonBorder} ${secondaryButtonText} ${secondaryButtonHoverBorder} ${focusVisibleRing}`}
          onClick={() => navigate(outline)}
          type="button"
          {...pageChrome}
        >
          {CONTROL_LABELS.close_document}
        </button>
      </div>
      <ArtifactRoutePanel artifact={artifact} artifactRoute={artifactRoute} isClosed={isClosed}>
        {artifact === undefined || artifact.kind !== "doc" ? null : (
          <DocumentRuntime.Provider value={namedRuntime}>
            <OpenedEditor
              artifact={artifact}
              askId={item?.kind === "ask" ? item.id : undefined}
              commentId={item?.kind === "comment" ? item.id : undefined}
              highlightTerm={highlightTerm}
              isClosed={isClosed}
              issueKey={issueKey}
              onToolbarChange={onToolbarChange}
              onVersionChange={(version) => {
                onShowDiff(false);
                onVersionChange(version);
              }}
              showDiff={showDiff}
              version={artifactRoute.version}
            />
          </DocumentRuntime.Provider>
        )}
      </ArtifactRoutePanel>
    </div>
  );
}

/**
 * The opened document's one editor, on its document's stage (`document-stage.tsx`), which rests
 * in this box. `StagedDocument` is a row's editor, with no landing, version or search term of
 * its own, so the document an address opens is drawn as `ArtifactDocument` draws one: on the
 * item the address names, marked, at the version it pins, with the words it searches for
 * marked, in the box a row's editor scrolls in (`EditorBox`).
 */
function OpenedEditor({
  artifact,
  askId,
  commentId,
  highlightTerm,
  isClosed,
  issueKey,
  onToolbarChange,
  onVersionChange,
  showDiff,
  version,
}: {
  artifact: Artifact;
  askId: string | undefined;
  commentId: string | undefined;
  highlightTerm: string | undefined;
  isClosed: boolean;
  issueKey: string;
  onToolbarChange: (next: DocumentToolbar | undefined) => void;
  onVersionChange: (version: number | null) => void;
  showDiff: boolean;
  version: number | undefined;
}): ReactNode {
  const stage = useDocumentStage(artifact.id);
  return (
    <>
      <div className="flex min-w-0 flex-col" ref={stage} />
      <DocumentStage artifactId={artifact.id}>
        <EditorBox>
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
        </EditorBox>
      </DocumentStage>
    </>
  );
}
