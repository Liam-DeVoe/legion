import { hasFocusVisibleRing } from "../../theme/classes";

const badgeSelectFrame = "relative inline-flex min-h-11 shrink-0 items-center rounded-sm";

/** The "tinted badge with an invisible native select over it" control (priority, assignee):
 *  the wrapper is the 44 px tap target (32 px from `md`) and draws the keyboard focus ring. */
export const badgeSelectWrapper = `${badgeSelectFrame} md:min-h-8 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The same wrapper in the issue header's details rail (the repository), 28 px from `md` like
 *  the rail's other controls, so it never makes the rail's row taller. */
export const badgeSelectRailWrapper = `${badgeSelectFrame} md:min-h-7 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The same wrapper in the issue page's header properties line, where the controls are the
 *  line's own height, as the pictures draw them: 22 px, and 24 px on a phone, the least a touch
 *  target may be (WCAG 2.5.8), because the header's six lines at 390 px leave no room for more
 *  (dispatch://INFRA-1196 R13). */
export const badgeSelectHeaderWrapper = `relative inline-flex min-h-6 shrink-0 items-center rounded-sm md:min-h-[22px] has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The visible badge; the caller appends its tone's `bg` and `text`. */
export const badgeSelectBadge =
  "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold";
/** The native select laid over the badge, invisible but the thing that receives the pick. */
export const badgeSelectOverlay =
  "absolute inset-0 size-full cursor-pointer appearance-none opacity-0 disabled:cursor-not-allowed";
