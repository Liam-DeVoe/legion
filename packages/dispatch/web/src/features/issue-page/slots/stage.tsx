import { type ReactNode, useEffect, useRef } from "react";
import { useLocation } from "react-router-dom";

import { primarySpec } from "../../../api/issue-cache";
import { DocumentStages } from "../document-stage";
import { issueAddress } from "../issue-address";
import type { Preset } from "../page-words";
import type { SlotProps } from "../slot-props";
import { ViewReadProvider } from "../tree-read/view-read";
import { BodySlot } from "./body";
import { ViewDecisionsSlot } from "./view-decisions";
import { ViewPlanSlot } from "./view-plan";
import { ViewSpecSlot } from "./view-spec";
import { ViewVerificationSlot } from "./view-verification";

/**
 * The stage: the outline, or one view in its place (dispatch://INFRA-1196 R2, part I's R1). The
 * `preset` the address opens chooses one slot: the body, which draws the outline, with no view
 * on, and that view's own slot under Spec, Verification, Plan or Decisions. Each slot draws its
 * own measure, so the stage adds no box and no width. A view whose part has not filled its slot
 * yet draws nothing.
 *
 * `DocumentStages` stands around the chosen slot: a document has one editor on the page, which
 * the row being edited in takes onto its own stage (`document-stage.tsx`).
 */
export function StageSlot(props: SlotProps): ReactNode {
  const { hash, search } = useLocation();
  const { preset } = issueAddress(props.route, search, hash, primarySpec(props.issue)?.slug);
  const View = VIEW_SLOTS[preset];
  return (
    <ViewReadProvider issue={props.issue} preset={preset}>
      <DocumentStages>
        {View === undefined ? (
          <BodySlot {...props} />
        ) : (
          <ViewStage view={preset}>
            <View {...props} />
          </ViewStage>
        )}
      </DocumentStages>
    </ViewReadProvider>
  );
}

/** The slot each view draws in place of the outline; none for Conversation, which is the
 *  outline itself. */
const VIEW_SLOTS: Readonly<Record<Preset, ((props: SlotProps) => ReactNode) | undefined>> = {
  conversation: undefined,
  decisions: ViewDecisionsSlot,
  plan: ViewPlanSlot,
  spec: ViewSpecSlot,
  verification: ViewVerificationSlot,
};

/** The row of a view's tree that holds its one tab stop (the WAI-ARIA tree pattern, R9). */
const TAB_STOP_ROW = '[role="tree"] [role="treeitem"][tabindex="0"]';

/**
 * One view, drawn where the outline is, which takes the keyboard focus on its tree's tab-stop
 * row once that row is drawn (the tester's finding F23): a page opened at a view's address, or
 * a view turned on while the focus was on the outline it replaced, leaves the focus on the
 * view and never on the page's body. The focus is taken only while nothing holds it, so a
 * reader who pressed a control keeps it there, and once it is taken this does nothing more for
 * that view.
 *
 * It adds no box (`display: contents`): it is a place to look for the tree, and the view draws
 * its own measure.
 */
export function ViewStage({ children, view }: { children: ReactNode; view: Preset }): ReactNode {
  const box = useRef<HTMLDivElement>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: each view takes the focus once, so a new view is what starts this again.
  useEffect(() => {
    const stage = box.current;
    if (stage === null) return;
    // True once the focus is placed, or once something else holds it.
    const take = (): boolean => {
      const active = document.activeElement;
      if (active !== null && active !== document.body) return true;
      const row = stage.querySelector<HTMLElement>(TAB_STOP_ROW);
      if (row === null) return false;
      row.focus({ preventScroll: true });
      return true;
    };
    if (take()) return;
    // A view draws its tree once its reads have answered, a frame or several after it mounts.
    const drawn = new MutationObserver(() => {
      if (take()) drawn.disconnect();
    });
    drawn.observe(stage, { childList: true, subtree: true });
    return () => drawn.disconnect();
  }, [view]);
  return (
    <div className="contents" ref={box}>
      {children}
    </div>
  );
}
