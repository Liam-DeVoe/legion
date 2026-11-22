import type { ReactNode } from "react";

import { card, kbdHint, textMutedOnSurface } from "../../theme/classes";
import { usePendingChord } from "./keymap";

/** Shows a half-typed chord (`g …`) so the pending key is visible until it completes or times out.
 *  It rides 1rem above the top layer of the foot of the screen, and never below its own floor
 *  (`hooks/useFootInset.ts` lists the foot's layers and the elements in each), so nothing fixed or
 *  stuck at that foot lies over it. */
export function ChordIndicator(): ReactNode {
  const pending = usePendingChord();
  if (pending === "") {
    return null;
  }
  return (
    <output
      className={`fixed right-4 bottom-[max(8rem,calc(var(--foot-status-inset,0px)_+_1rem))] z-40 flex items-center gap-1.5 rounded-full border px-3 py-1.5 text-sm shadow-lg xl:bottom-[max(4rem,calc(var(--foot-status-inset,0px)_+_1rem))] ${card} ${textMutedOnSurface}`}
      data-testid="chord-indicator"
    >
      <kbd className={`rounded px-1.5 py-0.5 font-mono text-xs font-medium ${kbdHint}`}>
        {pending}
      </kbd>
      <span aria-hidden="true">…</span>
      <span className="sr-only">then…</span>
    </output>
  );
}
