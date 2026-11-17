import { hasFocusVisibleRing } from "../../theme/classes";

const badgeSelectFrame = "relative inline-flex min-h-11 shrink-0 items-center rounded-sm";

/** The "tinted badge with an invisible native select over it" control (priority, assignee):
 *  the wrapper is the 44 px tap target (32 px from `md`) and draws the keyboard focus ring. */
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
/** The trigger of a list laid over the badge in `badgeSelectWrapper` (the ask card's hand-on): it
 *  shows the badge, fills the wrapper's tap target and leaves the focus ring to the wrapper. Below
 *  1280 px every button is at least 44 px tall (`styles.css`), and from `md` the wrapper is 32 px:
 *  there the trigger overhangs it by 6 px above and below rather than make its line 12 px taller,
 *  so the line is the height it was when a native select lay over the badge (`badgeSelectOverlay`),
 *  and the trigger is still the 44 px target that select was. */
export const badgeSelectTrigger =
  "flex cursor-pointer items-center self-stretch rounded-sm outline-none md:max-xl:-my-1.5";
