import { focusVisibleRing, hasFocusVisibleRing } from "../../theme/classes";

const badgeSelectFrame = "relative inline-flex min-h-11 shrink-0 items-center rounded-sm";

/** The "tinted badge whose press opens a list" control (the assignee, an ask's hand-on): the
 *  wrapper is the 44 px tap target (32 px from `md`) and draws the keyboard focus ring of the
 *  list's trigger inside it (`badgeListTrigger`). */
export const badgeSelectWrapper = `${badgeSelectFrame} md:min-h-8 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The trigger of a menu drawn as the same badge (`ChoiceMenu`: the snooze, the priority): the
 *  button is the 44 px tap target (32 px from `md`) and draws its own keyboard focus ring. It
 *  looks the same disabled or held (`refused:`, `styles.css`). */
export const badgeMenuTrigger = `${badgeSelectFrame} cursor-pointer outline-none md:min-h-8 focus-visible:ring-2 ${focusVisibleRing} refused:cursor-not-allowed refused:opacity-50`;
/** The visible badge; the caller appends its tone's `bg` and `text`. */
export const badgeSelectBadge =
  "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold";
/** The trigger of a list drawn as the badge (`MultiSelect`'s single pick: the assignee, an
 *  ask's hand-on), inside a wrapper above: it fills the wrapper's height, draws no
 *  ring of its own, and shows a held trigger's press as refused. */
export const badgeListTrigger =
  "flex cursor-pointer items-center self-stretch rounded-sm outline-none refused:cursor-not-allowed";
