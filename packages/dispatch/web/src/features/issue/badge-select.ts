import type { SyntheticEvent } from "react";

import { hasFocusVisibleRing } from "../../theme/classes";

const badgeSelectFrame = "relative inline-flex min-h-11 shrink-0 items-center rounded-sm";

/** The "tinted badge with a control over it" wrappers: the wrapper is the 44 px tap target
 *  (32 px from `md`) and draws the keyboard focus ring. Under it is either a native select laid
 *  invisibly over the badge (priority, snooze: `badgeSelectOverlay`) or the shared list's
 *  single pick whose trigger shows the badge (the issue header's assignee, an ask card's hand-on:
 *  `badgeSelectTrigger`). */
export const badgeSelectWrapper = `${badgeSelectFrame} md:min-h-8 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The same wrapper in the issue header's details rail (the repository), 28 px from `md` like
 *  the rail's other controls, so it never makes the rail's row taller. */
export const badgeSelectRailWrapper = `${badgeSelectFrame} md:min-h-7 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The visible badge; the caller appends its tone's `bg` and `text`. */
export const badgeSelectBadge =
  "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold";
/** The native select laid over the badge, invisible but the thing that receives the pick. */
export const badgeSelectOverlay =
  "absolute inset-0 size-full cursor-pointer appearance-none opacity-0 disabled:cursor-not-allowed";
/** The trigger of a list laid over the badge, filling its wrapper's tap target and leaving the
 *  focus ring to the wrapper; each wrapper's overhang is added below. */
const badgeSelectTriggerFrame =
  "flex cursor-pointer items-center self-stretch rounded-sm outline-none disabled:cursor-not-allowed";
/** The trigger of a list laid over the badge in `badgeSelectWrapper` (the issue header's
 *  assignee, the ask card's hand-on): it shows the badge. Below 1280 px every button is at least
 *  44 px tall (`styles.css`), and from `md` the wrapper is 32 px: there the trigger overhangs it
 *  by 6 px above and below rather than make its line 12 px taller, so the line is the height it
 *  was when a native select lay over the badge (`badgeSelectOverlay`), and the trigger is still
 *  the 44 px target that select was. */
export const badgeSelectTrigger = `${badgeSelectTriggerFrame} md:max-xl:-my-1.5`;
/** The same trigger in `badgeSelectRailWrapper` (the issue header's repository), which is 28 px
 *  from `md`: there the 44 px trigger overhangs it by 8 px above and below, so the rail's line is
 *  the 28 px it was when a native select lay over the badge. */
export const badgeSelectRailTrigger = `${badgeSelectTriggerFrame} md:max-xl:-my-2`;

/** The handlers a badge-select wrapper spreads when its list reads its options only once the
 *  reader reaches for it (the assignee, the repository, an ask card's hand-on): a press on the
 *  trigger, or in the list it opens (portalled, but a React child of the wrapper), is kept out
 *  of the row, card or header under it, and the pointer over the wrapper, a touch on it or the
 *  focus in it calls `want`, which starts that read. Nothing here makes the wrapper interactive,
 *  so it needs no role of its own; and every open panel reads a press in the capture phase
 *  (`AnchoredPanel`), so stopping it here hides it from none of them. */
export function badgeReachHandlers(want: () => void) {
  return {
    onFocus: want,
    onMouseDown: (event: SyntheticEvent) => event.stopPropagation(),
    onPointerDown: (event: SyntheticEvent) => event.stopPropagation(),
    onPointerEnter: want,
    onTouchStart: (event: SyntheticEvent) => {
      event.stopPropagation();
      want();
    },
  };
}
