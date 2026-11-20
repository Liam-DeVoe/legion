import type { ReactNode } from "react";

import { borderDefault, card, linkText, textPrimaryOnSurface } from "../../theme/classes";
import { openRun, type RunEntry, shownState, useGoing } from "./going-store";

/**
 * The chip at the foot of the page (R11) while no conversation is on the screen: it says how the
 * runs started on this page stand once the bar is closed - working, a plan that waits, or done -
 * and Open shows that run's conversation again. A run the person has seen end is no longer named.
 */
export function RunChip(): ReactNode {
  const going = useGoing();
  if (going.place !== "closed") {
    return null;
  }
  // Newest first: the chip names the newest run of each kind.
  const runs = [...going.runs.values()].filter((run) => run.startedHere).reverse();
  const working = runs.filter((run) => shownState(run) === "working");
  const waiting = runs.find((run) => !run.seen && shownState(run) === "waiting");
  const ended = runs.find(
    (run) => !run.seen && shownState(run) !== "working" && shownState(run) !== "waiting"
  );
  const shown: RunEntry | undefined = waiting ?? working[0] ?? ended;
  if (shown === undefined) {
    return null;
  }
  const text =
    waiting !== undefined
      ? "A plan waits for you"
      : working.length > 0
        ? working.length === 1
          ? "1 run working"
          : `${working.length} runs working`
        : shownState(shown) === "done"
          ? "Done"
          : shownState(shown) === "failed"
            ? "A run failed"
            : "A run stopped";
  return (
    <p
      className={`flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-medium shadow-lg ${card} ${borderDefault} ${textPrimaryOnSurface}`}
      data-state={shownState(shown)}
      data-testid="going-chip"
      role="status"
    >
      {text}
      <button
        className={`underline ${linkText}`}
        data-testid="going-chip-open"
        onClick={() => openRun(shown.id)}
        type="button"
      >
        Open
      </button>
    </p>
  );
}
