import type { ReactNode } from "react";

import {
  buttonGhostMuted,
  buttonPrimary,
  buttonSecondary,
  pageInkFill,
  pageLineBorder,
  pageMutedText,
  pageQuietText,
  railControlHoverBg,
  railHoverText,
  railMutedText,
} from "../theme/classes";
import { useTooltips } from "./tooltips";

/** The one pin glyph: an outline that fills with `currentColor` once pinned. */
function PinIcon({ pinned }: { pinned: boolean }): ReactNode {
  return (
    <svg aria-hidden="true" className="h-4 w-4" fill="none" viewBox="0 0 16 16">
      <path
        d="M5 2.5h6v3l1.5 2v1H9v4.75L8 14.5l-1-1.25V8.5H3.5v-1L5 5.5z"
        fill={pinned ? "currentColor" : "none"}
        stroke="currentColor"
        strokeLinejoin="round"
        strokeWidth="1.25"
      />
    </svg>
  );
}

/** A pin toggle: the system's square icon button, 32 px with the secondary's rule, `aria-pressed`
 *  for the pinned state, filled primary once pinned. `quiet` is for hosts that show one per row
 *  (conversation turns, the rail's pinned agents): the 28 px ghost, with no resting rule, so a
 *  list is not a column of boxes. Its size is fixed at each width (44 px below 1280 px, where
 *  `styles.css` lifts every control), so a turn's height never depends on its asynchronously
 *  rendered Markdown body (`ViewportAnchor` compensates React commits, not that later DOM
 *  swap). Inside `NoTooltips` it keeps the new issue page's own sizes. */
export function PinButton({
  className,
  disabled = false,
  label,
  onClick,
  onRail = false,
  pinned,
  quiet = false,
  title,
}: {
  className?: string;
  disabled?: boolean;
  label: string;
  onClick: () => void;
  /** The pin is drawn on the navigation rail, which is dark in both schemes: a 24 px control in
   *  the rail's own muted text, whose filled glyph alone says it is pinned. */
  onRail?: boolean;
  pinned: boolean;
  quiet?: boolean;
  /** A shorter tooltip than `label`, for a host whose label names a long thing. Drawn only on
   *  a surface that offers tooltips (`components/tooltips.tsx`). */
  title?: string;
}): ReactNode {
  const tooltips = useTooltips();
  const resting = quiet ? buttonGhostMuted : buttonSecondary;
  // Inside `NoTooltips` each colour is one of the new issue page's: pinned is filled with the
  // page's ink, as its one filled control is; unpinned is its header control's rule and muted
  // words, or quiet words that turn muted under the pointer for a quiet pin.
  const look = onRail
    ? `${railMutedText} ${railHoverText} ${railControlHoverBg}`
    : tooltips
      ? pinned
        ? buttonPrimary
        : resting
      : pinned
        ? pageInkFill
        : quiet
          ? `${pageQuietText} hover:text-page-muted`
          : `${pageLineBorder} ${pageMutedText}`;
  return (
    <button
      aria-label={label}
      aria-pressed={pinned}
      className={`flex shrink-0 items-center justify-center disabled:cursor-not-allowed disabled:opacity-50 ${
        onRail
          ? "size-6 rounded"
          : tooltips
            ? quiet
              ? "size-7 rounded-md"
              : "size-8 rounded-md"
            : `min-h-11 min-w-11 rounded-lg ${quiet ? "" : "md:min-h-8 md:min-w-8"}`
      } ${look} ${className ?? ""}`}
      disabled={disabled}
      onClick={onClick}
      title={tooltips ? (title ?? label) : undefined}
      type="button"
    >
      <PinIcon pinned={pinned} />
    </button>
  );
}
