import type { ReactNode } from "react";

import {
  askBlockPill,
  borderDefault,
  borderStrong,
  borderTransparent,
  chipHover,
  surfaceMutedBg,
  surfaceMutedStrongBg,
  textMutedOnSurfaceMuted,
  textSecondaryOnSurface,
  toolbarControlHeight,
} from "../theme/classes";
import { TruncatedText } from "./TruncatedText";

/** The fill a chip takes while selected: the neutral strong surface, or an ask urgency's badge
 *  (the margin composer's Urgency row). At rest every tone is the same quiet outlined pill. */
export type ChipTone = "neutral" | keyof typeof askBlockPill;

const restingChip = `${borderDefault} ${surfaceMutedBg} ${textMutedOnSurfaceMuted}`;
const selectedChip: Record<ChipTone, string> = {
  neutral: `${borderStrong} ${surfaceMutedStrongBg} ${textSecondaryOnSurface}`,
  blocking: `${borderTransparent} ${askBlockPill.blocking.bg} ${askBlockPill.blocking.text}`,
  high: `${borderTransparent} ${askBlockPill.high.bg} ${askBlockPill.high.text}`,
  low: `${borderTransparent} ${askBlockPill.low.bg} ${askBlockPill.low.text}`,
  med: `${borderTransparent} ${askBlockPill.med.bg} ${askBlockPill.med.text}`,
};

/**
 * The one interactive chip: a single element carrying the border, the fill and the hit area, so
 * the outline is always the shaded shape (a border on a tall button wrapped around a small
 * `Pill` draws a bigger ring than the fill). It is the badge family's shape at 28 px, above the least
 * a target may be (WCAG 2.5.8); below 1280 px `styles.css` lifts it to 44 px like every control. `selected` renders `aria-pressed` and the
 * selected fill; `removable` appends a `×` glyph and the whole chip is the remove control, so the
 * host names the action in `aria-label` (`Remove Label: docs filter`). The chip is never wider
 * than its row: text that does not fit ends in an ellipsis, and a host whose text can be that
 * long passes the whole of it as `title`. `data-*` attributes pass through to the button.
 * `inToolbar` is a host's that stands it among a table toolbar's controls, where it takes their
 * one height (`toolbarControlHeight`: 34 px from 1280 px up, 32 px below) rather than its own
 * 28 px.
 */
export function Chip({
  "aria-label": ariaLabel,
  children,
  inToolbar = false,
  onClick,
  removable = false,
  selected,
  title,
  tone = "neutral",
  ...data
}: {
  /** `data-*` attributes go to the button itself, so a host's tests and key handlers can find
   *  the chip (`data-inbox-filter-token`). Nothing else passes through. */
  [key: `data-${string}`]: string | undefined;
  "aria-label"?: string;
  children: ReactNode;
  inToolbar?: boolean;
  onClick: () => void;
  removable?: boolean;
  selected?: boolean;
  title?: string;
  tone?: ChipTone;
}): ReactNode {
  return (
    <button
      aria-label={ariaLabel}
      aria-pressed={selected}
      className={`inline-flex ${inToolbar ? `h-8 ${toolbarControlHeight}` : "h-7"} max-w-full shrink-0 items-center gap-1 rounded border px-2.5 text-[13px] font-medium whitespace-nowrap ${
        selected === true ? selectedChip[tone] : `${restingChip} ${chipHover}`
      }`}
      onClick={onClick}
      type="button"
      {...data}
    >
      <TruncatedText title={title}>{children}</TruncatedText>
      {removable ? <span aria-hidden="true">×</span> : null}
    </button>
  );
}
