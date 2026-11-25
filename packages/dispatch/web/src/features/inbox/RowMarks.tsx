/**
 * The three marks every Inbox row wears, drawn the same way in the short row beside the reading
 * pane and in the card below its width: what kind of row it is, how its issue is prioritised,
 * and how urgent the question is. Each one is `shrink-0` and never wraps, so none of them can
 * grow over the title or over the issue key beside it - the row's own text is what shortens.
 */

import type { ReactNode } from "react";

import type { AskUrgency, IssuePriority } from "../../api/types";
import { type BadgeTone, badgeClassName } from "../../components/Badge";
import { badgeMed } from "../../theme/classes";
import { URGENCY_LABELS } from "./ask-urgency";
import { TYPE_LABELS } from "./grouping";
import type { InboxRowType } from "./list-row";

/** One character per kind, in place of the word the row used to spend a badge on. */
const TYPE_GLYPHS: Record<InboxRowType, string> = {
  agent: "✦",
  approval: "✓",
  decision: "◆",
  question: "?",
  reply: "↩",
};

/** The badge tone of each urgency: the system's own (`Badge`), where colour means state. */
const URGENCY_TONES: Record<AskUrgency, BadgeTone> = {
  blocking: "danger",
  high: "warning",
  low: "neutral",
  med: "info",
};

/** The same for an issue's priority, P0 to P3, as the board and the issue header tone it. */
const PRIORITY_TONES: readonly BadgeTone[] = ["danger", "warning", "neutral", "neutral"];

/** What the row is, at the left edge of its first line. The glyph is decoration; the name is
 *  the image's own label, so a reader who cannot see it is told the kind just the same. */
export function RowKindIcon({ type }: { type: InboxRowType }): ReactNode {
  return (
    <span
      aria-label={TYPE_LABELS[type]}
      className={`grid size-5 shrink-0 place-items-center rounded text-xs leading-none ${badgeMed.bg} ${badgeMed.text}`}
      data-inbox-kind={type}
      role="img"
    >
      <span aria-hidden="true">{TYPE_GLYPHS[type]}</span>
    </span>
  );
}

/** The owning issue's priority, as the coloured tag the board and the issue header use; a row
 *  whose issue has none, and every row with no issue at all, wears nothing. */
export function RowPriorityTag({ priority }: { priority: IssuePriority | null }): ReactNode {
  if (priority === null) return null;
  return (
    <span className={badgeClassName(PRIORITY_TONES[priority])} data-inbox-priority={`P${priority}`}>
      P{priority}
    </span>
  );
}

/** The ask's urgency, in its own colour and in its own word. A thread has no urgency, so its
 *  row wears no mark at all. */
export function RowUrgencyMark({ urgency }: { urgency: AskUrgency | null }): ReactNode {
  if (urgency === null) return null;
  return (
    <span className={badgeClassName(URGENCY_TONES[urgency])} data-inbox-urgency={urgency}>
      {URGENCY_LABELS[urgency]}
    </span>
  );
}
