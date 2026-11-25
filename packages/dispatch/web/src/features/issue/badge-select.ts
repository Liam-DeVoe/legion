import { badgeShapeClassName } from "../../components/Badge";
import { hasFocusVisibleRing } from "../../theme/classes";

/** The "tinted badge with an invisible native select over it" control (priority, assignee,
 *  snooze): the wrapper is the badge's own 20 px box from 1280 px, so the control sits on a row
 *  as any badge does, and the 44 px tap target below that, where `styles.css` lifts the select
 *  inside it to 44 px too. It draws the keyboard focus ring. From 1280 px the select over the
 *  badge reaches 6 px past the wrapper above and below, so the press target is 32 px while the
 *  badge and the row it sits on stay 20 px (`e2e/board.e2e.ts` holds the target to 32 px). */
export const badgeSelectWrapper = `group/badge-select relative inline-flex min-h-11 shrink-0 items-center rounded xl:min-h-5 xl:[&>select]:-inset-y-1.5 xl:[&>select]:h-8 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The same wrapper in the new issue page's header properties line, where the controls are the
 *  line's own height, as the pictures draw them: 24 px, the least a touch target may be (WCAG
 *  2.5.8), because the header at 390 px is held to eight lines and 250 px, nine and 280 px while
 *  an approval request waits on the reader, which leaves no room for more. It is 24 px at every
 *  width, from `md` too, since a 24 px target holds at every width and not only on a phone. It
 *  holds a value - a person, a priority - so it narrows to its line rather than keep its width,
 *  and the value inside breaks anywhere (`min-w-0 max-w-full`). */
export const badgeSelectHeaderWrapper = `relative inline-flex min-h-6 min-w-0 max-w-full items-center rounded-sm has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
/** The visible badge, in the one badge shape (`components/Badge.tsx`); the caller appends its
 *  tone's `bg` and `text`. Under the pointer it darkens a little, the one cue that it opens. */
export const badgeSelectBadge = `${badgeShapeClassName} group-hover/badge-select:brightness-95 dark:group-hover/badge-select:brightness-125`;
/** Inside `NoTooltips` (the new issue page) every value a shared control draws wraps: it keeps
 *  `max-w-full` and breaks anywhere, and nothing around it keeps its width, so a long login with
 *  no space stays inside Details' row rather than running past it. These are
 *  `badgeSelectWrapper` in that shape, with `min-w-0 max-w-full` in place of `shrink-0`, and
 *  `badgeSelectBadge` with `max-w-full [overflow-wrap:anywhere]`. The select over the badge
 *  stays usable however many lines the badge takes: the wrapper is `relative` and as tall as the
 *  badge it holds, and `badgeSelectOverlay` is `absolute inset-0 size-full` over the wrapper, so
 *  it covers every line of a badge that wraps. */
const badgeSelectPageFrame =
  "relative inline-flex min-h-11 min-w-0 max-w-full items-center rounded-sm";
export const badgeSelectPageWrapper = `${badgeSelectPageFrame} md:min-h-8 has-focus-visible:ring-2 ${hasFocusVisibleRing}`;
export const badgeSelectPageBadge =
  "inline-flex items-center rounded-sm px-1.5 py-0.5 text-xs font-semibold max-w-full [overflow-wrap:anywhere]";
/** The native select laid over the badge, invisible but the thing that receives the pick. */
export const badgeSelectOverlay =
  "absolute inset-0 size-full cursor-pointer appearance-none opacity-0 disabled:cursor-not-allowed";
