import { type ReactNode, useRef } from "react";

import { useFootInset } from "../../hooks/useFootInset";
import { card, kbdKey, textMutedOnSurface } from "../../theme/classes";
import { usePendingChord } from "./keymap";

/** Shows a half-typed chord (`g …`) so the pending key is visible until it completes or times out.
 *  It is a float of the foot's top layer (`hooks/useFootInset.ts` lists the foot's layers and the
 *  elements in each): it stands 1rem above the status layer's property, which the hook chooses so
 *  the indicator lies over no foot element and stays on the screen, and never lower than its
 *  floor (`--foot-floor`, which its classes set per breakpoint). */
export function ChordIndicator(): ReactNode {
  const pending = usePendingChord();
  const indicatorRef = useRef<HTMLOutputElement>(null);
  useFootInset(indicatorRef, pending !== "", "top");
  if (pending === "") {
    return null;
  }
  return (
    <output
      className={`fixed right-4 bottom-[max(var(--foot-floor),calc(var(--foot-status-inset,0px)_+_1rem))] z-40 flex h-8 items-center gap-1.5 rounded-md border px-2 text-sm shadow-lg [--foot-floor:8rem] xl:[--foot-floor:4rem] ${card} ${textMutedOnSurface}`}
      data-testid="chord-indicator"
      ref={indicatorRef}
    >
      <kbd
        className={`inline-flex h-5 min-w-5 items-center justify-center rounded border px-1 font-sans text-xs font-medium ${kbdKey}`}
      >
        {pending}
      </kbd>
      <span aria-hidden="true">…</span>
      <span className="sr-only">then…</span>
    </output>
  );
}
