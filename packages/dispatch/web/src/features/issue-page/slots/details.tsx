import { useQuery } from "@tanstack/react-query";
import { type ReactNode, type RefCallback, useCallback, useMemo, useState } from "react";
import { createPortal } from "react-dom";

import { primarySpec } from "../../../api/issue-cache";
import { userStateQuery } from "../../../api/queries";
import { stateForIssue } from "../../issue/pins";
import { useMargin } from "../../margin/margin-context";
import { useKeymap } from "../../shell/keymap";
import { DETAILS_ROW_ATTRIBUTE, useOpenDetailsAt } from "../details-rows";
import { LABELS_CONTROL_NAME } from "../IssueAddMenu";
import type { IssueField } from "../IssueFieldForms";
import { IssueMarginCards, IssueMarginPanel } from "../IssueMargin";
import { useFillMarginSlot } from "../margin-slot";
import { NoTooltips } from "../NoTooltips";
import type { SlotProps } from "../slot-props";
import { useIssueHeaderWrites } from "../useIssueHeaderWrites";

/** The labels' control in Details' Labels row (`IssueLabels.tsx`), which the key `l` presses. */
const LABELS_CONTROL = `[${DETAILS_ROW_ATTRIBUTE}="labels"] button[aria-label="${LABELS_CONTROL_NAME}"]`;

/**
 * Details' properties (dispatch://INFRA-1196 R13, R14): the one panel `IssueMarginPanel` draws,
 * filled into the shell's Details drawer.
 *
 * The panel is drawn once for the life of the page into its own stage, an element this slot
 * holds, and the stage is appended to the host the drawer draws (`IssueMarginCards`). So the
 * panel keeps one React parent while Details closes and opens, and nothing in it - an open
 * editor, a list the reader opened - is thrown away. The drawer's own cards, the threads of an
 * open document and its composer, stand above the sections only while a document's editor is
 * on the page (`useMargin().documentBridge`, which `ProofDocument.tsx` sets): the panel holds no
 * list of the conversation, which is the outline.
 *
 * Slots share no state, so this slot keeps which of the two editors of its rows is open and
 * makes its own set of the issue's writes; the header's writes reach the same keyed line of the
 * issue (`useIssueHeaderWrites.ts`). It binds `l`, which opens Details at the Labels row with
 * Edit labels pressed.
 */
export function DetailsSlot({ issue }: SlotProps): ReactNode {
  const state = useQuery(userStateQuery()).data;
  const writes = useIssueHeaderWrites(issue, stateForIssue(state, issue.key));
  // The one editor open in a row of Details - the parent's or the route's - which the rows'
  // own controls and the add menu open.
  const [editingField, setEditingField] = useState<IssueField | null>(null);
  const stage = useMemo(() => {
    const element = document.createElement("div");
    // No box of its own: the panel is a child of the host that holds it.
    element.className = "contents";
    return element;
  }, []);
  // The host takes the stage as it is drawn, and lets go of it only while it still holds it.
  const holdPanel = useCallback(
    (host: HTMLElement | null) => {
      if (host === null) return;
      host.append(stage);
      return () => {
        if (stage.parentElement === host) stage.remove();
      };
    },
    [stage]
  );
  const openDetailsAt = useOpenDetailsAt();
  useKeymap("issue", [
    {
      // The labels' control is in Details' Labels row (dispatch://INFRA-1196 R13): the key
      // opens Details there and presses it, as a reader does.
      id: "labels",
      keys: "l",
      label: LABELS_CONTROL_NAME,
      run: () => openDetailsAt("labels", true),
      when: () => {
        const control = document.querySelector(LABELS_CONTROL);
        return control instanceof HTMLButtonElement && !control.disabled;
      },
    },
  ]);
  return (
    <>
      {createPortal(
        <NoTooltips>
          <IssueMarginPanel
            documentArtifact={primarySpec(issue)}
            editingField={editingField}
            isClosed={issue.closed_at !== null}
            issue={issue}
            onEditingField={setEditingField}
            state={state}
            writes={writes}
          />
        </NoTooltips>,
        stage
      )}
      <DetailsFill holdPanel={holdPanel} />
    </>
  );
}

/**
 * Details' fill of the shell's margin slot: the host that holds the panel's stage, with the
 * margin's own cards above it while a document's editor is on the page. It reads the margin's
 * context, which changes with every hover, selection and placement of the margin, so it is a
 * component of its own and those changes draw it alone, not the panel.
 */
function DetailsFill({ holdPanel }: { holdPanel: RefCallback<HTMLElement> }): null {
  const documentUp = useMargin().documentBridge !== undefined;
  const fill = useCallback(
    (conversation: ReactNode) => (
      <IssueMarginCards
        conversation={documentUp ? conversation : undefined}
        panelHost={holdPanel}
      />
    ),
    [documentUp, holdPanel]
  );
  useFillMarginSlot(fill);
  return null;
}
