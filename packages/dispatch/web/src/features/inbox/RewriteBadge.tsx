import type { ReactNode } from "react";

import type { TextRewrite } from "../../api/types";
import {
  badgeBlocking,
  badgeBlockingHoverBg,
  badgeLow,
  badgePrimary,
  badgePrimaryEdge,
  badgePrimaryHoverBg,
  badgePrimaryOutline,
  borderTransparent,
  groupFocusVisibleRing,
} from "../../theme/classes";

/** Every state is the same box: a 1 px border, clear unless the state draws it (Original). */
const PILL =
  "inline-flex shrink-0 items-center rounded-full border px-2 py-px text-xs leading-4 font-medium whitespace-nowrap";
/** A pressable state's button is the pill's hit area: below 1280 px every button is a 44 px tap
 *  target (`styles.css`), and the pill inside keeps its own size; the focus ring is the pill's. */
const HIT = "group inline-flex shrink-0 items-center justify-center outline-none";

/**
 * The one mark a rewrite leaves on an agent's text - an ask's card, a comment, a message, an
 * issue's title - beside who wrote it and when: **Rewriting…** while a person's Rewrite runs,
 * **Rewritten** once a rewrite is in place, whoever wrote it (a person's Rewrite, or the reviewer
 * before the reader saw the text), and **Rewrite failed** when Dispatch did not apply one.
 * Rewritten shows the original wording in place and reads **Original** until it is pressed again
 * (`toggleKey`, where the surface has one); Rewrite failed tries again (`onRetry`, and `r`), and
 * why it failed is its tooltip alone. `onRetry` is absent where nothing can be rewritten again
 * from the page. A state this release does not know draws nothing.
 */
export function RewriteBadge({
  onRetry,
  onToggle,
  rewrite,
  running = false,
  showingOriginal,
  toggleKey,
}: {
  onRetry?: () => void;
  onToggle: () => void;
  rewrite: Pick<TextRewrite, "reason" | "state"> | undefined;
  /** The reader's own rewrite is out, before the read says so. */
  running?: boolean;
  showingOriginal: boolean;
  /** The key that shows or hides the original on this surface, which the tooltip names. */
  toggleKey?: string;
}): ReactNode {
  if (running || rewrite?.state === "rewriting") {
    return (
      <span
        className={`${PILL} animate-pulse ${borderTransparent} ${badgeLow.bg} ${badgeLow.text}`}
        data-ask-rewrite="rewriting"
        role="status"
      >
        Rewriting…
      </span>
    );
  }
  if (rewrite?.state === "failed") {
    const reason = rewrite.reason ?? "Dispatch did not apply the rewrite.";
    return (
      <button
        className={HIT}
        data-ask-rewrite="failed"
        onClick={onRetry}
        title={onRetry === undefined ? reason : `${reason} Try again (r)`}
        type="button"
      >
        <span
          className={`${PILL} ${groupFocusVisibleRing} ${borderTransparent} ${badgeBlocking.bg} ${badgeBlocking.text} ${badgeBlockingHoverBg}`}
        >
          Rewrite failed
        </span>
      </button>
    );
  }
  if (rewrite?.state !== "rewritten") return null;
  const hint = toggleKey === undefined ? "" : ` (${toggleKey})`;
  const tone = showingOriginal
    ? badgePrimaryOutline
    : `${badgePrimaryEdge} ${badgePrimary.bg} ${badgePrimary.text} ${badgePrimaryHoverBg}`;
  return (
    <button
      aria-pressed={showingOriginal}
      className={HIT}
      data-ask-rewrite={showingOriginal ? "original" : "rewritten"}
      onClick={onToggle}
      title={showingOriginal ? `Show the rewrite${hint}` : `Show the original wording${hint}`}
      type="button"
    >
      <span className={`${PILL} ${groupFocusVisibleRing} ${tone}`}>
        {showingOriginal ? "Original" : "Rewritten"}
      </span>
    </button>
  );
}
