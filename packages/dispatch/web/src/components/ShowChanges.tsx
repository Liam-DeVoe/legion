import type { ReactNode } from "react";
import { focusVisibleRing, primaryButtonBg, primaryButtonHoverBg } from "../theme/classes";

/** The changes a held list holds back from the reader (`held-rows.ts`), and what draws them. */
export interface HeldChanges {
  readonly count: number;
  readonly show: () => void;
}

/**
 * The one control that draws what a held list holds back: "Show N changes", where N counts the
 * rows a change from elsewhere would add, remove or move (`changedRows`). A small pill in the
 * primary button's style, centred, floating over the top edge of the rows it holds - the table's
 * header, or the Board's column headers - as the agent live view's Jump to latest floats over its
 * transcript. It takes no layout space, so nothing on screen moves as it comes and goes, which is
 * the whole point of holding the reader's place.
 *
 * Its host is the box right above the rows, positioned (`relative`), with this as its first child:
 * it stands just under that box's bottom edge. Being first, it changes no sibling's spacing either.
 * The table and the Board draw it in the same place.
 */
export function ShowChanges({ held }: { held: HeldChanges }): ReactNode {
  return (
    <div
      aria-live="polite"
      className="pointer-events-none absolute inset-x-0 top-full z-40 flex justify-center"
    >
      {held.count === 0 ? null : (
        <button
          className={`pointer-events-auto mt-3 min-h-8 rounded-full px-4 text-sm font-semibold whitespace-nowrap shadow-lg outline-none focus-visible:ring-2 ${primaryButtonBg} ${primaryButtonHoverBg} ${focusVisibleRing}`}
          data-held-changes={held.count}
          onClick={held.show}
          type="button"
        >
          Show {held.count} {held.count === 1 ? "change" : "changes"}
        </button>
      )}
    </div>
  );
}
