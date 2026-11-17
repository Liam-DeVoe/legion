import { focusVisibleRing, hasFocusVisibleRing } from "../../theme/classes";

const badgeSelectFrame = "relative inline-flex min-h-11 shrink-0 items-center rounded-sm";

/** The "tinted badge with an invisible native select over it" control (the assignee):
 *  the wrapper is the 44 px tap target (32 px from `md`) and draws the keyboard focus ring. */
export const badgeSelectWrapper = `${badgeSelectFrame} md:min-h-8 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The same wrapper in the issue header's details rail (the repository), 28 px from `md` like
 *  the rail's other controls, so it never makes the rail's row taller. */
export const badgeSelectRailWrapper = `${badgeSelectFrame} md:min-h-7 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The trigger of a menu drawn as the same badge (`ChoiceMenu`: the snooze, the priority): the
 *  button is the 44 px tap target (32 px from `md`) and draws its own keyboard focus ring. */
export const badgeMenuTrigger = `${badgeSelectFrame} cursor-pointer outline-none md:min-h-8 focus-visible:ring-2 ${focusVisibleRing} disabled:cursor-not-allowed disabled:opacity-50`;
/** The visible badge; the caller appends its tone's `bg` and `text`. */
export const badgeSelectBadge =
  "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold";
/** The native select laid over the badge, invisible but the thing that receives the pick. */
export const badgeSelectOverlay =
  "absolute inset-0 size-full cursor-pointer appearance-none opacity-0 disabled:cursor-not-allowed";
