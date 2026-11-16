import {
  pageStatusDoneText,
  pageStatusIdleText,
  pageStatusProgressText,
} from "../../theme/classes";

/** The statuses of work that moves: started, and not done. */
const MOVING_STATUSES: ReadonlySet<string> = new Set(["in_progress", "needs_review", "testing"]);

/** The status's colour, as the prototype's `.st-*` gives it. */
export function statusTone(status: string): string {
  if (status === "done") return pageStatusDoneText;
  return MOVING_STATUSES.has(status) ? pageStatusProgressText : pageStatusIdleText;
}

/** The status's shape, the prototype's: a half-filled circle while the work moves, a tick once
 *  it is done, an empty circle before it starts. The word beside it says the same. */
export function statusGlyph(status: string): string {
  if (status === "done") return "✓";
  return MOVING_STATUSES.has(status) ? "◐" : "○";
}
