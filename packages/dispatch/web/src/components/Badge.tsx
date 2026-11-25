import type { ReactNode } from "react";

import {
  badgeBlocking,
  badgeHigh,
  badgeLow,
  badgeMed,
  badgeSuccess,
  borderStrong,
  chipRing,
  textSecondaryOnSurface,
} from "../theme/classes";

/**
 * The one shape every badge, pill and chip shares (`DESIGN.md`): 20 px tall, 12 px type, 6 px of
 * inset, 4 px corners, its words centred on the line. `Pill`'s and `Chip`'s class strings are
 * built on it, so a count, a label and a state sitting in one row are the same height.
 */
export const badgeShapeClassName =
  "inline-flex h-5 shrink-0 items-center rounded px-1.5 text-xs leading-5 font-medium whitespace-nowrap";

export type BadgeTone = "neutral" | "info" | "success" | "warning" | "danger" | "outline";

const TONES: Record<BadgeTone, string> = {
  danger: `${badgeBlocking.bg} ${badgeBlocking.text}`,
  info: `${badgeMed.bg} ${badgeMed.text}`,
  neutral: `${badgeLow.bg} ${badgeLow.text} ${chipRing}`,
  outline: `border ${borderStrong} ${textSecondaryOnSurface}`,
  success: `${badgeSuccess.bg} ${badgeSuccess.text}`,
  warning: `${badgeHigh.bg} ${badgeHigh.text}`,
};

/** A badge's whole class string, for a host that must be another element (a link, a button). */
export function badgeClassName(tone: BadgeTone = "neutral"): string {
  return `${badgeShapeClassName} ${TONES[tone]}`;
}

/**
 * A static badge: a word or a count that marks a row. The tone says a state (`danger` blocks,
 * `warning` needs attention, `success` is well, `info` is the accent) and `neutral` says none.
 * A label or a status reads better as `Pill`; a thing the reader presses is a `Chip`.
 */
export function Badge({
  children,
  className,
  title,
  tone = "neutral",
}: {
  children: ReactNode;
  /** Placement only. */
  className?: string;
  title?: string;
  tone?: BadgeTone;
}): ReactNode {
  return (
    <span className={`${badgeClassName(tone)} ${className ?? ""}`} title={title}>
      {children}
    </span>
  );
}

/** A count in a badge: at least as wide as it is tall, its digits centred and tabular, so a
 *  column of counts lines up. */
export function CountBadge({
  count,
  title,
  tone = "neutral",
}: {
  count: number | string;
  title?: string;
  tone?: BadgeTone;
}): ReactNode {
  return (
    <Badge className="min-w-5 justify-center tabular-nums" title={title} tone={tone}>
      {count}
    </Badge>
  );
}

/** The open-ask count on board cards and list rows. Priority uses the same tag shape but is
 *  editable everywhere it appears, so it lives in `features/issue/PriorityControl`. */
export function AttentionBadge({ count }: { count: number }): ReactNode {
  return <CountBadge count={count} tone="danger" />;
}
